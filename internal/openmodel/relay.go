package openmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
)

// The relay (Repo C design note OPEN_MODEL_ENGINES §9.1). A provider whose
// manifest says relay gets, for each build, an HTTP listener on 127.0.0.1
// inside the build's own `abcp run` process, from just before Ralphex starts
// until the build gives its slot back. Codex's base_url is the relay's; the
// relay sends each request on to the provider's base URL and nowhere else,
// applies the build model's compat fixes to POST <base>/responses, and
// streams every answer back unchanged, except a chat-bridged one and what a
// stream fix changes (stream.go). It never reads or adds a key: Codex's own
// Authorization header goes on as it came. It logs one line per request
// (the model, the path, the status, the fixes it applied and how long it
// took, and when a stream fix ended an answer early or its usage never came,
// that and an estimate of its tokens), never a header, a key or a body.

// MaxRelayBody bounds a request the relay reads, and a bridged answer.
const MaxRelayBody = 64 << 20

// RelayOptions are a relay's seams; zero values are the live ones.
type RelayOptions struct {
	// Transport reaches the provider: a copy of http.DefaultTransport that
	// asks for no compression when nil. The relay never follows a redirect.
	Transport http.RoundTripper
	// Log takes the relay's lines; none are written when nil.
	Log io.Writer
	// Run names the build in each line.
	Run string
	// Now and IDs stamp a bridged answer.
	Now func() time.Time
	IDs ResponseIDs
}

// Relay is one build's loopback relay to its provider for one model.
type Relay struct {
	provider  Provider
	model     Model
	upstream  *url.URL
	base      string
	listener  net.Listener
	server    *http.Server
	transport http.RoundTripper
	options   RelayOptions
	logMu     sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

// StartRelay listens on a free port of 127.0.0.1 and serves the model's
// requests until Close.
func StartRelay(p Provider, m Model, options RelayOptions) (*Relay, error) {
	if !p.Relay {
		return nil, errors.New("the provider's manifest does not ask for the relay")
	}
	if p.WireAPI != WireResponses {
		return nil, errors.New(`the relay needs wire_api "responses"`)
	}
	if err := validBaseURL(p.BaseURL); err != nil {
		return nil, fmt.Errorf("the relay's provider base_url %w", err)
	}
	upstream, err := url.Parse(p.BaseURL)
	if err != nil {
		return nil, err
	}
	if options.Transport == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DisableCompression = true
		options.Transport = transport
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.IDs == nil {
		options.IDs = randomID
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("the relay cannot listen on 127.0.0.1: %w", err)
	}
	relay := &Relay{provider: p, model: m, upstream: upstream, base: strings.TrimSuffix(upstream.Path, "/"),
		listener: listener, transport: options.Transport, options: options}
	relay.server = &http.Server{Handler: relay, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute,
		MaxHeaderBytes: 1 << 20, ErrorLog: log.New(io.Discard, "", 0)}
	go relay.server.Serve(listener)
	return relay, nil
}

// Address answers the relay's 127.0.0.1:<port>.
func (r *Relay) Address() string { return r.listener.Addr().String() }

// URL answers the base_url Codex is given: the relay's address with the
// provider's own path (http://127.0.0.1:<port>/v1).
func (r *Relay) URL() string { return "http://" + r.Address() + r.base }

// Close stops the relay; once is enough, more are harmless.
func (r *Relay) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		// Closing the listener too stops a relay closed before its Serve began.
		r.closeErr = r.server.Close()
		if err := r.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			r.closeErr = errors.Join(r.closeErr, err)
		}
	})
	return r.closeErr
}

// hopHeaders are never passed on, in either direction; the relay sets its
// own length, and asks for no compression so the answer passes as it is.
var hopHeaders = []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection",
	"Te", "Trailer", "Trailers", "Transfer-Encoding", "Upgrade", "Host", "Content-Length", "Accept-Encoding"}

func copyHeaders(to, from http.Header) {
	dropped := map[string]bool{}
	for _, name := range hopHeaders {
		dropped[http.CanonicalHeaderKey(name)] = true
	}
	for _, listed := range from.Values("Connection") {
		for _, name := range strings.Split(listed, ",") {
			dropped[http.CanonicalHeaderKey(strings.TrimSpace(name))] = true
		}
	}
	for name, values := range from {
		if !dropped[http.CanonicalHeaderKey(name)] {
			to[name] = slices.Clone(values)
		}
	}
}

// relayLine is what the relay logs about one request.
type relayLine struct {
	method, path, model, fault string
	status                     int
	applied                    Applied
	started                    time.Time
	// cut says what ended an answer at its first call (stop_at_first_call):
	// the next item, an error or the stream's end.
	cut string
	// usageLost: a fixed stream ended without the provider's usage (it sends
	// usage only in its last response.completed, which a cut or a broken
	// stream never reaches), so the line gives an estimate in its place: the
	// request sent, and the text, arguments and input the model streamed
	// (its reasoning, which Together does not stream, is not in it).
	usageLost                     bool
	estimatedIn, estimatedVisible int
}

func (r *Relay) logLine(line relayLine) {
	if r.options.Log == nil {
		return
	}
	model := line.model
	if model == "" {
		model = "-"
	}
	text := fmt.Sprintf("%s abcp open-model relay", r.options.Now().UTC().Format(time.RFC3339))
	if r.options.Run != "" {
		text += " run=" + r.options.Run
	}
	text += fmt.Sprintf(" %s/%s %s %s model=%s status=%d fixes=%s duration=%s", r.provider.ID, r.model.Name, line.method,
		line.path, model, line.status, line.applied, time.Since(line.started).Round(time.Millisecond))
	if line.cut != "" {
		text += " cut=" + line.cut
	}
	if line.usageLost {
		text += fmt.Sprintf(" usage=lost est_input_tokens=%d est_output_tokens=%d", line.estimatedIn, line.estimatedVisible)
	}
	if line.fault != "" {
		text += " error=" + line.fault
	}
	r.logMu.Lock()
	defer r.logMu.Unlock()
	io.WriteString(r.options.Log, text+"\n")
}

// refuse answers an error of the relay's own, in the provider's error shape.
func refuse(w http.ResponseWriter, status int, code, message string) {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error", "code": code}})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(status)
	w.Write(body)
}

// ServeHTTP relays one request.
func (r *Relay) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	line := relayLine{method: request.Method, path: "-", applied: Applied{}, started: time.Now()}
	defer func() { r.logLine(line) }()
	var fix *streamFix
	fail := func(status int, code, fault, message string) {
		line.status, line.fault = status, fault
		refuse(w, status, code, message)
	}
	requestPath := request.URL.Path
	if path.Clean(requestPath) != requestPath || !strings.HasPrefix(requestPath, r.base+"/") {
		fail(http.StatusNotFound, "abcp_relay_path", "path", "the ABCP relay serves the provider's API only")
		return
	}
	line.path = requestPath
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		fail(http.StatusMethodNotAllowed, "abcp_relay_method", "method", "the ABCP relay takes GET and POST only")
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, MaxRelayBody+1))
	if err != nil {
		fail(http.StatusBadRequest, "abcp_relay_request", "request_read", "the ABCP relay could not read the request")
		return
	}
	if len(body) > MaxRelayBody {
		fail(http.StatusRequestEntityTooLarge, "abcp_relay_request", "request_too_large", "the request is larger than the ABCP relay takes")
		return
	}
	if request.Method == http.MethodPost && strings.TrimSuffix(requestPath, "/") == r.base+"/responses" {
		fields, err := parseObject(body)
		if err != nil {
			fail(http.StatusBadRequest, "abcp_relay_request", "request_json", "the ABCP relay takes a JSON object")
			return
		}
		if model := fields.text("model"); model != r.model.ID {
			fail(http.StatusBadRequest, "abcp_relay_model", "model_refused", "this build's ABCP relay serves "+r.model.ID+" only")
			return
		}
		line.model = r.model.ID
		rewritten, applied, custom, err := rewrite(body, r.model.Compat)
		if err != nil {
			fail(http.StatusBadRequest, "abcp_relay_request", "request_json", "the ABCP relay takes a JSON object")
			return
		}
		line.applied = applied
		if slices.Contains(r.model.Compat, CompatChatBridge) {
			applied[CompatChatBridge] = 1
			r.bridge(w, request, rewritten, fields, &line)
			return
		}
		body = rewritten
		fix = newStreamFix(slices.Contains(r.model.Compat, CompatStopAtFirstCall), custom, r.options.IDs)
	}
	r.forward(w, request, body, &line, fix)
}

// upstreamRequest answers the provider's request at that path (under the
// provider's own host only) with the client's headers but the hop-by-hop ones.
func (r *Relay) upstreamRequest(ctx context.Context, method, requestPath, query string, header http.Header, body []byte) (*http.Request, error) {
	target := *r.upstream
	target.Path, target.RawPath, target.RawQuery = requestPath, "", query
	upstream, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyHeaders(upstream.Header, header)
	upstream.ContentLength = int64(len(body))
	if len(body) == 0 {
		upstream.Body = http.NoBody
	}
	return upstream, nil
}

// forward sends the request on and streams the answer back as it comes,
// through the stream fix when one applies to a streamed answer.
func (r *Relay) forward(w http.ResponseWriter, request *http.Request, body []byte, line *relayLine, fix *streamFix) {
	// Cancelled when forward returns, so an answer the fix ended early
	// closes the provider's stream.
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	upstream, err := r.upstreamRequest(ctx, request.Method, request.URL.Path, request.URL.RawQuery, request.Header, body)
	if err != nil {
		line.status, line.fault = http.StatusBadGateway, "upstream_request"
		refuse(w, http.StatusBadGateway, "abcp_relay_upstream", "the ABCP relay could not reach the provider")
		return
	}
	response, err := r.transport.RoundTrip(upstream)
	if err != nil {
		line.status, line.fault = http.StatusBadGateway, "upstream_unreachable"
		refuse(w, http.StatusBadGateway, "abcp_relay_upstream", "the ABCP relay could not reach the provider")
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		line.status, line.fault = http.StatusBadGateway, "redirect_refused"
		refuse(w, http.StatusBadGateway, "abcp_relay_redirect", "the provider answered a redirect, which the ABCP relay does not follow")
		return
	}
	copyHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	line.status = response.StatusCode
	flusher, _ := w.(http.Flusher)
	if fix.active() && response.StatusCode == http.StatusOK && eventStream(response.Header) {
		flush := func() {
			if flusher != nil {
				flusher.Flush()
			}
		}
		err := fix.copyStream(w, flush, response.Body)
		if fix.cut != "" {
			line.applied[CompatStopAtFirstCall] = 1
		}
		if fix.converted > 0 {
			line.applied[CompatCustomToolAsFunction] += fix.converted
		}
		line.cut = fix.cut
		if fix.usage == nil {
			line.usageLost, line.estimatedIn, line.estimatedVisible = true, estimatedTokens(len(body)), estimatedTokens(fix.visible)
		}
		switch {
		case errors.Is(err, errClientGone) || (err != nil && request.Context().Err() != nil):
			line.fault = "client_gone"
		case errors.Is(err, errEventTooLarge):
			line.fault = "upstream_event_too_large"
		case err != nil:
			line.fault = "upstream_cut"
		}
		return
	}
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			if _, err := w.Write(buffer[:n]); err != nil {
				line.fault = "client_gone"
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr == io.EOF {
			return
		}
		if readErr != nil {
			line.fault = "upstream_cut"
			if request.Context().Err() != nil {
				line.fault = "client_gone"
			}
			return
		}
	}
}

// bridge sends a Responses request to the provider's /chat/completions and
// answers Codex in Responses events (chatbridge.go).
func (r *Relay) bridge(w http.ResponseWriter, request *http.Request, body []byte, fields object, line *relayLine) {
	chat, err := ChatRequest(body)
	if err != nil {
		line.status, line.fault = http.StatusBadRequest, "bridge_request"
		refuse(w, http.StatusBadRequest, "abcp_relay_request", "the ABCP relay could not read the request for the chat bridge")
		return
	}
	header := http.Header{}
	for _, name := range []string{"Authorization", "User-Agent"} {
		if value := request.Header.Get(name); value != "" {
			header.Set(name, value)
		}
	}
	if header.Get("User-Agent") == "" {
		header.Set("User-Agent", "abcp-open-model-relay/1")
	}
	header.Set("Content-Type", "application/json")
	header.Set("Accept", "application/json")
	upstream, err := r.upstreamRequest(request.Context(), http.MethodPost, r.base+"/chat/completions", "", header, chat)
	if err != nil {
		line.status, line.fault = http.StatusBadGateway, "upstream_request"
		refuse(w, http.StatusBadGateway, "abcp_relay_upstream", "the ABCP relay could not reach the provider")
		return
	}
	response, err := r.transport.RoundTrip(upstream)
	if err != nil {
		line.status, line.fault = http.StatusBadGateway, "upstream_unreachable"
		refuse(w, http.StatusBadGateway, "abcp_relay_upstream", "the ABCP relay could not reach the provider")
		return
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(response.Body, MaxRelayBody+1))
	if err != nil || len(answer) > MaxRelayBody {
		line.status, line.fault = http.StatusBadGateway, "upstream_cut"
		refuse(w, http.StatusBadGateway, "abcp_relay_upstream", "the provider's answer could not be read")
		return
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		line.status, line.fault = http.StatusBadGateway, "redirect_refused"
		refuse(w, http.StatusBadGateway, "abcp_relay_redirect", "the provider answered a redirect, which the ABCP relay does not follow")
		return
	}
	if response.StatusCode != http.StatusOK {
		// The provider's refusal goes to Codex as it came, so Codex's own
		// retries (and Retry-After) apply.
		copyHeaders(w.Header(), response.Header)
		w.Header().Set("Content-Length", fmt.Sprint(len(answer)))
		w.WriteHeader(response.StatusCode)
		w.Write(answer)
		line.status = response.StatusCode
		return
	}
	model, _ := fields.get("model")
	translated, err := ResponseFromChat(answer, model, r.options.Now(), r.options.IDs)
	if err != nil {
		line.status, line.fault = http.StatusBadGateway, "bridge_answer"
		refuse(w, http.StatusBadGateway, "abcp_relay_bridge", "the provider's chat answer could not be read")
		return
	}
	var data []byte
	if raw, ok := fields.get("stream"); ok && string(bytes.TrimSpace(raw)) == "true" {
		data = ResponseEvents(translated)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		data = mustJSON(translated)
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(http.StatusOK)
	line.status = http.StatusOK
	if _, err := w.Write(data); err != nil {
		line.fault = "client_gone"
	}
}

// eventStream reports whether an answer is a server-sent event stream.
func eventStream(header http.Header) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(header.Get("Content-Type"))), "text/event-stream")
}

package openmodel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer is a log the relay's handlers may write from many requests.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// lines waits a moment for n lines: the relay logs a request once its
// answer is out, so a client may read the answer first.
func (l *lockedBuffer) lines(n int) string {
	for deadline := time.Now().Add(2 * time.Second); strings.Count(l.String(), "\n") < n && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	return l.String()
}

// together is the provider of the relay tests: Together's id, the stand-in's
// address, and the round-1 models with their compat lists.
func together(baseURL string) Provider {
	return Provider{
		ID: "together", BaseURL: baseURL, KeyFile: "/home/devagent/runtime/secrets/together-api-key", WireAPI: WireResponses,
		CodexHome: "/home/devagent/.codex/lanes/together", Relay: true,
		Models: []Model{
			{Name: "nemotron-3.5-super", ID: "nvidia/NVIDIA-Nemotron-3.5-Super-VL", Origin: "US", ContextWindow: 262144,
				Compat: []string{CompatDropNullReasoningContent, CompatMessageBeforeCalls, CompatOutputAfterCall}},
			{Name: "beam-1.0", ID: "reflection/Beam-1.0-501B-A23B", Origin: "US", ContextWindow: 1048576, Compat: []string{CompatChatBridge}},
		},
	}
}

func startRelay(t *testing.T, provider Provider, model int, log io.Writer) *Relay {
	t.Helper()
	if err := provider.Validate(); err != nil {
		t.Fatal(err)
	}
	relay, err := StartRelay(provider, provider.Models[model], RelayOptions{Log: log, Run: "run-1",
		Now: func() time.Time { return time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { relay.Close() })
	return relay
}

func post(t *testing.T, url string, body []byte, header map[string]string) (*http.Response, []byte) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+testKey)
	for name, value := range header {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response, data
}

// TestTheRelayFixesARequestAndStreamsTheAnswerAsItCame: end to end against a
// stand-in for Together, nemotron's refused turn reaches the provider fixed,
// with Codex's own key and headers, and the provider's stream reaches the
// client byte for byte, each piece as it is sent.
func TestTheRelayFixesARequestAndStreamsTheAnswerAsItCame(t *testing.T) {
	release := make(chan struct{})
	fake := &fakeTogether{}
	stream := []string{
		"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"caf\\u00e9 \"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":8425}}}\n\n",
	}
	fake.answer = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if r.Method != http.MethodPost || togetherRefusal(body) != "" {
			return false
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "together-7")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(stream[0]))
		w.(http.Flusher).Flush()
		<-release
		for _, piece := range stream[1:] {
			w.Write([]byte(piece))
			w.(http.Flusher).Flush()
		}
		return true
	}
	server := httptest.NewServer(fake)
	defer server.Close()
	log := &lockedBuffer{}
	relay := startRelay(t, together(server.URL+"/v1"), 0, log)
	if host, _, _ := net.SplitHostPort(relay.Address()); host != "127.0.0.1" || !strings.HasPrefix(relay.URL(), "http://127.0.0.1:") || !strings.HasSuffix(relay.URL(), "/v1") {
		t.Fatalf("the relay listens at %s (%s)", relay.Address(), relay.URL())
	}
	request := codexRequest("nvidia/NVIDIA-Nemotron-3.5-Super-VL",
		withInput(codexOpening(), reasoningItem("rs_1"), call("call_a"), call("call_b"), said("msg_1", "Both."), output("call_a"), output("call_b")), nil)

	httpRequest, _ := http.NewRequest(http.MethodPost, relay.URL()+"/responses", bytes.NewReader(request))
	httpRequest.Header.Set("Authorization", "Bearer "+testKey)
	httpRequest.Header.Set("Accept", "text/event-stream")
	httpRequest.Header.Set("Accept-Encoding", "gzip")
	httpRequest.Header.Set("Session_id", "01a19875")
	httpRequest.Header.Set("Connection", "X-Hop")
	httpRequest.Header.Set("X-Hop", "dropped")
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	first := make([]byte, len(stream[0]))
	if _, err := io.ReadFull(reader, first); err != nil || string(first) != stream[0] {
		t.Fatalf("the first event did not come before the provider finished: %q, %v", first, err)
	}
	close(release)
	rest, _ := io.ReadAll(reader)
	if string(first)+string(rest) != strings.Join(stream, "") || response.StatusCode != http.StatusOK ||
		response.Header.Get("X-Request-Id") != "together-7" || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("the answer changed on its way: %d %v %q", response.StatusCode, response.Header, rest)
	}

	if len(fake.requests) != 1 {
		t.Fatalf("the provider got %d requests", len(fake.requests))
	}
	got := fake.requests[0]
	if got.path != "/v1/responses" || got.header.Get("Authorization") != "Bearer "+testKey || got.header.Get("Session_id") != "01a19875" ||
		got.header.Get("Accept-Encoding") != "" || got.header.Get("X-Hop") != "" || got.header.Get("Accept") != "text/event-stream" {
		t.Fatalf("the provider got %s with %v", got.path, got.header)
	}
	if refusal := togetherRefusal(got.body); refusal != "" {
		t.Fatalf("the provider would refuse what the relay sent: %s", refusal)
	}
	wantLine := "2026-11-02T09:00:00Z abcp open-model relay run=run-1 together/nemotron-3.5-super POST /v1/responses model=nvidia/NVIDIA-Nemotron-3.5-Super-VL status=200 fixes=drop_null_reasoning_content:1,message_before_calls:1,output_after_call:2 duration="
	if lines := strings.Split(strings.TrimSpace(log.lines(1)), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], wantLine) {
		t.Fatalf("the relay logged %q", log.String())
	}

	// Anything but POST /responses goes on unchanged; so does a refusal, with
	// its Retry-After, for Codex's own retries.
	fake.answer = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" && r.URL.RawQuery == "limit=5" && len(body) == 0 {
			fmt.Fprint(w, `[{"id":"nvidia/NVIDIA-Nemotron-3.5-Super-VL"}]`)
			return true
		}
		if r.URL.Path == "/v1/responses" && bytes.Contains(body, []byte(`"busy"`)) {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"busy"}}`)
			return true
		}
		return false
	}
	listed, err := http.Get(relay.URL() + "/models?limit=5")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(listed.Body)
	listed.Body.Close()
	if listed.StatusCode != http.StatusOK || string(body) != `[{"id":"nvidia/NVIDIA-Nemotron-3.5-Super-VL"}]` {
		t.Fatalf("GET /models through the relay: %d %s", listed.StatusCode, body)
	}
	busy, data := post(t, relay.URL()+"/responses", codexRequest("nvidia/NVIDIA-Nemotron-3.5-Super-VL", codexOpening(), func(r map[string]any) { r["user"] = "busy" }), nil)
	if busy.StatusCode != http.StatusTooManyRequests || busy.Header.Get("Retry-After") != "7" || string(data) != `{"error":{"message":"busy"}}` {
		t.Fatalf("a refusal came back as %d %v %s", busy.StatusCode, busy.Header, data)
	}
}

// TestTheRelayGoesNowhereButItsProvidersAPIForItsModel: another path, a
// path that climbs out, another method, another model, a body that is not
// JSON and a redirect are refused, and none of them reaches the provider
// (the redirect is not followed).
func TestTheRelayGoesNowhereButItsProvidersAPIForItsModel(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the relay followed a redirect")
	}))
	defer elsewhere.Close()
	fake := &fakeTogether{answer: func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if bytes.Contains(body, []byte(`"moved"`)) {
			http.Redirect(w, r, elsewhere.URL+"/v1/responses", http.StatusTemporaryRedirect)
			return true
		}
		return false
	}}
	server := httptest.NewServer(fake)
	defer server.Close()
	log := &lockedBuffer{}
	relay := startRelay(t, together(server.URL+"/v1"), 0, log)
	nemotron := codexRequest("nvidia/NVIDIA-Nemotron-3.5-Super-VL", codexOpening(), nil)
	cases := []struct {
		name, method, path string
		body               []byte
		status             int
		code               string
	}{
		{"another path", http.MethodPost, "/admin/responses", nemotron, http.StatusNotFound, "abcp_relay_path"},
		{"a path that climbs out", http.MethodPost, "/v1/../responses", nemotron, http.StatusNotFound, "abcp_relay_path"},
		{"another method", http.MethodDelete, "/v1/responses", nemotron, http.StatusMethodNotAllowed, "abcp_relay_method"},
		{"another model", http.MethodPost, "/v1/responses", codexRequest("deepseek-ai/DeepSeek-V4.1-Flash", codexOpening(), nil), http.StatusBadRequest, "abcp_relay_model"},
		{"not JSON", http.MethodPost, "/v1/responses", []byte(`model=x`), http.StatusBadRequest, "abcp_relay_request"},
		{"a redirect", http.MethodPost, "/v1/responses", codexRequest("nvidia/NVIDIA-Nemotron-3.5-Super-VL", codexOpening(), func(r map[string]any) { r["user"] = "moved" }), http.StatusBadGateway, "abcp_relay_redirect"},
	}
	for _, c := range cases {
		connection, err := net.Dial("tcp", relay.Address())
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(connection, "%s %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", c.method, c.path, relay.Address(), len(c.body), c.body)
		response, err := http.ReadResponse(bufio.NewReader(connection), nil)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		data, _ := io.ReadAll(response.Body)
		connection.Close()
		var refusal struct {
			Error struct{ Code string } `json:"error"`
		}
		json.Unmarshal(data, &refusal)
		if response.StatusCode != c.status || refusal.Error.Code != c.code {
			t.Errorf("%s: %d %s", c.name, response.StatusCode, data)
		}
	}
	if len(fake.requests) != 1 || !bytes.Contains(fake.requests[0].body, []byte(`"moved"`)) {
		t.Errorf("the provider got %d requests", len(fake.requests))
	}
	logged := log.lines(len(cases))
	for _, fault := range []string{"error=path", "error=method", "model=- status=400 fixes=none duration=", "error=model_refused", "error=request_json", "error=redirect_refused"} {
		if !strings.Contains(logged, fault) {
			t.Errorf("the log does not say %q:\n%s", fault, logged)
		}
	}

	closed := startRelay(t, together(server.URL+"/v1"), 0, nil)
	address := closed.Address()
	closed.Close()
	if connection, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		connection.Close()
		t.Error("a closed relay still answers")
	}
	direct := together(server.URL + "/v1")
	direct.Relay = false
	if _, err := StartRelay(direct, direct.Models[0], RelayOptions{}); err == nil {
		t.Error("a relay started for a provider that does not ask for one")
	}
}

// TestTheRelayBridgesBeamToChatCompletions: end to end, Beam's probe request
// goes to the provider's /chat/completions with the turn's reasoning, and the
// answer comes back to Codex as a Responses stream; a refusal comes back as
// the provider gave it.
func TestTheRelayBridgesBeamToChatCompletions(t *testing.T) {
	fake := &fakeTogether{}
	server := httptest.NewServer(fake)
	defer server.Close()
	log := &lockedBuffer{}
	relay := startRelay(t, together(server.URL+"/v1"), 1, log)
	beam := "reflection/Beam-1.0-501B-A23B"
	request := codexRequest(beam, withInput(codexOpening(), thought("rs_1", "Inspect first."), call("call_1"), output("call_1")), nil)
	response, data := post(t, relay.URL()+"/responses", request, map[string]string{"User-Agent": "codex_exec/0.163.0", "Session_id": "s"})
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" ||
		!strings.HasPrefix(string(data), "event: response.created\n") || !strings.Contains(string(data), `{"text":"Checked.","type":"reasoning_text"}`) ||
		!strings.HasSuffix(string(data), "\n\n") || !strings.Contains(string(data), "event: response.completed\n") {
		t.Fatalf("the bridged answer: %d %v\n%s", response.StatusCode, response.Header, data)
	}
	got := fake.requests[0]
	if got.path != "/v1/chat/completions" || chatRefusal(got.body) != "" || !bytes.Contains(got.body, []byte(`"reasoning_content":"Inspect first."`)) ||
		got.header.Get("Authorization") != "Bearer "+testKey || got.header.Get("User-Agent") != "codex_exec/0.163.0" ||
		got.header.Get("Content-Type") != "application/json" || got.header.Get("Session_id") != "" {
		t.Fatalf("the provider got %s %v\n%s", got.path, got.header, got.body)
	}
	// Without stream, the answer is the Responses object itself.
	response, data = post(t, relay.URL()+"/responses", codexRequest(beam, codexOpening(), func(r map[string]any) { r["stream"] = false }), nil)
	var object map[string]any
	if response.StatusCode != http.StatusOK || json.Unmarshal(data, &object) != nil || object["object"] != "response" || object["status"] != "completed" {
		t.Fatalf("a bridged answer without stream: %d %s", response.StatusCode, data)
	}
	fake.answer = func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"message":"overloaded"}}`)
		return true
	}
	response, data = post(t, relay.URL()+"/responses", request, nil)
	if response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Retry-After") != "3" || string(data) != `{"error":{"message":"overloaded"}}` {
		t.Fatalf("a bridged refusal: %d %v %s", response.StatusCode, response.Header, data)
	}
	logged := log.lines(3)
	if strings.Count(logged, "together/beam-1.0 POST /v1/responses model=reflection/Beam-1.0-501B-A23B status=") != 3 ||
		strings.Count(logged, "fixes=chat_completions_bridge:1") != 3 || !strings.Contains(logged, "status=503") {
		t.Fatalf("the relay logged:\n%s", logged)
	}
}

// TestTheRelayLogsNoHeaderKeyOrBody: one line per request, with the model,
// the path, the status, the fixes and the time it took, and nothing a
// request or an answer carried: not the key, not a header, not a word of
// either body, not the query.
func TestTheRelayLogsNoHeaderKeyOrBody(t *testing.T) {
	secret := "do-not-log-this-prompt-text"
	fake := &fakeTogether{answer: func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		w.Header().Set("Set-Cookie", "session=answer-header-secret")
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"invalid key `+testKey+` answer-body-secret"}}`)
			return true
		}
		fmt.Fprint(w, "event: response.completed\ndata: {\"text\":\"answer-body-secret\"}\n\n")
		return true
	}}
	server := httptest.NewServer(fake)
	defer server.Close()
	log := &lockedBuffer{}
	relay := startRelay(t, together(server.URL+"/v1"), 0, log)
	request := codexRequest("nvidia/NVIDIA-Nemotron-3.5-Super-VL", withInput(codexOpening(), said("m", secret)), nil)
	post(t, relay.URL()+"/responses", request, map[string]string{"X-Custom-Header": "header-value-secret", "Cookie": "c=cookie-secret"})
	post(t, relay.URL()+"/responses", codexRequest("someone/else", codexOpening(), func(r map[string]any) { r["note"] = secret }), nil)
	listed, _ := http.Get(relay.URL() + "/models?api_key=" + testKey)
	listed.Body.Close()
	logged := log.lines(3)
	lines := strings.Split(strings.TrimSpace(logged), "\n")
	if len(lines) != 3 {
		t.Fatalf("the relay logged %d lines:\n%s", len(lines), logged)
	}
	for _, leak := range []string{testKey, "Bearer", secret, "header-value-secret", "X-Custom-Header", "cookie-secret", "answer-header-secret",
		"answer-body-secret", "api_key", "someone/else", "You are a coding agent", "Authorization"} {
		if strings.Contains(logged, leak) {
			t.Errorf("the log holds %q:\n%s", leak, logged)
		}
	}
	for index, want := range []string{
		"together/nemotron-3.5-super POST /v1/responses model=nvidia/NVIDIA-Nemotron-3.5-Super-VL status=200 fixes=none duration=",
		"together/nemotron-3.5-super POST /v1/responses model=- status=400 fixes=none duration=",
		"together/nemotron-3.5-super GET /v1/models model=- status=401 fixes=none duration=",
	} {
		if !strings.Contains(lines[index], want) || !strings.HasPrefix(lines[index], "2026-11-02T09:00:00Z abcp open-model relay run=run-1 ") {
			t.Errorf("line %d = %q, want %q", index, lines[index], want)
		}
	}
}

package githubmergeprovider

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/mergelifecycle"
)

type callMeter struct {
	budget     mergelifecycle.ProviderBudgetV1
	accounting mergelifecycle.ProviderAccountingV1
	started    time.Time
	calls      int
}

func (m *callMeter) beforeRequest(bodyBytes int64, limits githublifecycle.Limits) error {
	if bodyBytes < 0 || bodyBytes > int64(limits.MaxRequestBodyBytes) {
		return errors.New("request body exceeds the fixed provider limit")
	}
	if m.calls >= limits.MaxHTTPCalls || m.accounting.RequestBytes+bodyBytes > m.budget.RequestBytes ||
		m.accounting.HeaderBytes >= m.budget.HeaderBytes || m.accounting.CompressedResponseBytes >= m.budget.CompressedResponseBytes ||
		m.accounting.DecompressedResponseBytes >= m.budget.DecompressedResponseBytes || m.accounting.ActiveNanos >= m.budget.ActiveNanos {
		return errors.New("remaining controller provider budget is exhausted")
	}
	m.calls++
	m.accounting.RequestBytes += bodyBytes
	return nil
}

func (m *callMeter) rollbackUnwrittenBody(bodyBytes int64) {
	if bodyBytes > 0 && m.accounting.RequestBytes >= bodyBytes {
		m.accounting.RequestBytes -= bodyBytes
	}
}

func (m *callMeter) addHeaders(value int64) error {
	if value < 0 || m.accounting.HeaderBytes+value > m.budget.HeaderBytes {
		return errors.New("cumulative response header budget exceeded")
	}
	m.accounting.HeaderBytes += value
	return nil
}

func (m *callMeter) addCompressed(value int64) error {
	if value < 0 || m.accounting.CompressedResponseBytes+value > m.budget.CompressedResponseBytes {
		return errors.New("cumulative compressed response budget exceeded")
	}
	m.accounting.CompressedResponseBytes += value
	return nil
}

func (m *callMeter) addDecompressed(value int64) error {
	if value < 0 || m.accounting.DecompressedResponseBytes+value > m.budget.DecompressedResponseBytes {
		return errors.New("cumulative decompressed response budget exceeded")
	}
	m.accounting.DecompressedResponseBytes += value
	return nil
}

func (m *callMeter) addActive(value int64) error {
	if value < 0 || m.accounting.ActiveNanos+value > m.budget.ActiveNanos {
		return errors.New("cumulative active provider time budget exceeded")
	}
	m.accounting.ActiveNanos += value
	return nil
}

type submissionTracker struct {
	mu                  sync.Mutex
	plainttextAttempted bool
	plainttextBytes     int64
}

func (t *submissionTracker) recordWrite(n int) {
	t.mu.Lock()
	t.plainttextAttempted = true
	if n > 0 {
		t.plainttextBytes += int64(n)
	}
	t.mu.Unlock()
}

func (t *submissionTracker) possible() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.plainttextAttempted
}

type trackingConn struct {
	net.Conn
	tracker *submissionTracker
}

func (c *trackingConn) Write(data []byte) (int, error) {
	c.tracker.recordWrite(len(data))
	return c.Conn.Write(data)
}

type sealedRoundTripper struct {
	auth *Authenticator
	base http.RoundTripper
}

func (s sealedRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || request.URL.Scheme != "https" || request.URL.Host != apiHost ||
		request.URL.User != nil || request.URL.Fragment != "" || request.Host != "" && request.Host != apiHost {
		return nil, errors.New("request origin is not the sealed GitHub origin")
	}
	if !validClosedRoute(request.Method, request.URL.RequestURI()) {
		return nil, errors.New("request is outside the closed provider route set")
	}
	for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "If-Modified-Since", "If-None-Match"} {
		if len(request.Header.Values(name)) != 0 {
			return nil, errors.New("caller supplied a forbidden credential, forwarding, or conditional header")
		}
	}
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Accept", "application/vnd.github+json")
	clone.Header.Set("X-GitHub-Api-Version", apiVersion)
	clone.Header.Set("User-Agent", defaultUserAgent)
	clone.Header.Set("Accept-Encoding", "gzip")
	clone.Header.Set("Authorization", s.auth.authorizationValue())
	return s.base.RoundTrip(clone)
}

func productionTransport(limits githublifecycle.Limits) *http.Transport {
	return &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: -1}).DialContext,
		ForceAttemptHTTP2:      false,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: int64(limits.MaxResponseHeaderBytes),
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, ServerName: apiHost},
	}
}

func productionReadClient(auth *Authenticator, limits githublifecycle.Limits) *http.Client {
	return &http.Client{
		Transport:     sealedRoundTripper{auth: auth, base: productionTransport(limits)},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       limits.CallTimeout,
	}
}

func productionMutationClient(auth *Authenticator, limits githublifecycle.Limits, tracker *submissionTracker) *http.Client {
	transport := productionTransport(limits)
	transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != apiHost+":443" {
			return nil, errors.New("TLS destination is not the sealed GitHub origin")
		}
		raw, err := (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: -1}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		tlsConn := tls.Client(raw, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: apiHost})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, err
		}
		return &trackingConn{Conn: tlsConn, tracker: tracker}, nil
	}
	return &http.Client{
		Transport:     sealedRoundTripper{auth: auth, base: transport},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       limits.CallTimeout,
	}
}

type trackerRoundTripper struct {
	base    http.RoundTripper
	tracker *submissionTracker
}

func (t trackerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	t.tracker.recordWrite(1)
	return t.base.RoundTrip(request)
}

func newTestProvider(auth *Authenticator, limits githublifecycle.Limits, base http.RoundTripper) (*Provider, error) {
	readClient := &http.Client{Transport: sealedRoundTripper{auth: auth, base: base}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return newProvider(auth, limits, readClient, func(tracker *submissionTracker) *http.Client {
		return &http.Client{Transport: sealedRoundTripper{auth: auth, base: trackerRoundTripper{base: base, tracker: tracker}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	})
}

type httpResult struct {
	Status       int
	Body         []byte
	RequestID    string
	ObservedNano int64
	Link         string
	LinkObserved bool
}

type responseStatusError struct {
	status int
}

func (e *responseStatusError) Error() string {
	return "GitHub returned HTTP status " + strconv.Itoa(e.status)
}

func (p *Provider) readJSON(ctx context.Context, meter *callMeter, method, path string, body []byte) (httpResult, error) {
	var last httpResult
	for attempt := 0; attempt < maxReadAttempts; attempt++ {
		result, err := p.requestJSON(ctx, meter, p.readClient, nil, method, path, body)
		last = result
		if err == nil {
			if result.Status == http.StatusOK {
				return result, nil
			}
			err = &responseStatusError{status: result.Status}
		}
		if !retryableReadStatus(result.Status) || attempt+1 == maxReadAttempts {
			return last, sanitizeTransportError(err)
		}
	}
	return last, errors.New("bounded read attempts exhausted")
}

func retryableReadStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500 && status <= 599
}

func (p *Provider) requestJSON(ctx context.Context, meter *callMeter, client *http.Client, tracker *submissionTracker, method, path string, body []byte) (httpResult, error) {
	if ctx == nil || meter == nil || client == nil {
		return httpResult{}, errors.New("request context and owned client are required")
	}
	if method != http.MethodGet && method != http.MethodPost {
		return httpResult{}, errors.New("request method is not in the provider route set")
	}
	if !validClosedRoute(method, path) {
		return httpResult{}, errors.New("request path is outside the provider route set")
	}
	if err := meter.beforeRequest(int64(len(body)), p.limits); err != nil {
		return httpResult{}, err
	}
	requestURL := apiOrigin + path
	request, err := http.NewRequestWithContext(ctx, method, requestURL, bytes.NewReader(body))
	if err != nil {
		meter.rollbackUnwrittenBody(int64(len(body)))
		return httpResult{}, errors.New("request construction failed")
	}
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	started := p.now()
	response, doErr := client.Do(request)
	active := p.now().Sub(started).Nanoseconds()
	activeErr := meter.addActive(active)
	if doErr != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if tracker != nil && !tracker.possible() {
			meter.rollbackUnwrittenBody(int64(len(body)))
		}
		return httpResult{}, errors.Join(sanitizeTransportError(doErr), activeErr)
	}
	if response == nil || response.Body == nil {
		return httpResult{}, errors.Join(errors.New("GitHub response has no body"), activeErr)
	}
	result, readErr := p.consumeResponse(response, meter)
	return result, errors.Join(readErr, activeErr)
}

var (
	repositoryRouteComponent = `[A-Za-z0-9][A-Za-z0-9._-]{0,99}`
	objectIDRouteComponent   = `[0-9a-f]{40}(?:[0-9a-f]{24})?`
	paginationRoute          = regexp.MustCompile(`^/repos/` + repositoryRouteComponent + `/` + repositoryRouteComponent + `/(?:commits/` + objectIDRouteComponent + `/(?:check-runs|statuses)|pulls/[1-9][0-9]*/reviews)$`)
	commitCreateRoute        = regexp.MustCompile(`^/repos/` + repositoryRouteComponent + `/` + repositoryRouteComponent + `/git/commits$`)
	commitReadRoute          = regexp.MustCompile(`^/repos/` + repositoryRouteComponent + `/` + repositoryRouteComponent + `/git/commits/` + objectIDRouteComponent + `$`)
	refReadRoute             = regexp.MustCompile(`^/repos/` + repositoryRouteComponent + `/` + repositoryRouteComponent + `/git/ref/heads/[A-Za-z0-9._~%/-]+$`)
	compareReadRoute         = regexp.MustCompile(`^/repos/` + repositoryRouteComponent + `/` + repositoryRouteComponent + `/compare/` + objectIDRouteComponent + `\.\.\.` + objectIDRouteComponent + `$`)
)

func validClosedRoute(method, path string) bool {
	if path == githublifecycle.GitHubGraphQLPathV1 {
		return method == http.MethodPost
	}
	parsed, err := url.ParseRequestURI(path)
	if err != nil || parsed.Fragment != "" || parsed.Host != "" || parsed.Scheme != "" || strings.Contains(strings.ToLower(parsed.EscapedPath()), "%2f") || hasTraversalComponent(parsed.Path) {
		return false
	}
	if method == http.MethodPost {
		return parsed.RawQuery == "" && commitCreateRoute.MatchString(parsed.EscapedPath())
	}
	if method != http.MethodGet {
		return false
	}
	if paginationRoute.MatchString(parsed.EscapedPath()) {
		values := parsed.Query()
		if len(values["page"]) != 1 || len(values["per_page"]) != 1 || len(values) < 2 || len(values) > 3 {
			return false
		}
		if len(values) == 3 && len(values["filter"]) != 1 {
			return false
		}
		return true
	}
	return parsed.RawQuery == "" && (commitReadRoute.MatchString(parsed.EscapedPath()) || refReadRoute.MatchString(parsed.EscapedPath()) || compareReadRoute.MatchString(parsed.EscapedPath()))
}

func hasTraversalComponent(path string) bool {
	for _, component := range strings.Split(path, "/") {
		if component == "." || component == ".." {
			return true
		}
	}
	return false
}

func (p *Provider) consumeResponse(response *http.Response, meter *callMeter) (result httpResult, returnedErr error) {
	defer func() {
		if err := response.Body.Close(); err != nil {
			returnedErr = errors.Join(returnedErr, errors.New("response body close failed"))
		}
	}()
	result.Status = response.StatusCode
	result.ObservedNano = p.now().UnixNano()
	result.LinkObserved = true
	result.Link = response.Header.Get("Link")
	if len(result.Link) > p.limits.MaxLinkHeaderBytes {
		return result, errors.New("Link response header exceeds the fixed provider limit")
	}
	headerBytes := canonicalHeaderBytes(response.Header)
	if headerBytes > int64(p.limits.MaxResponseHeaderBytes) {
		return result, errors.New("response headers exceed the fixed provider limit")
	}
	if err := meter.addHeaders(headerBytes); err != nil {
		return result, err
	}
	ids := response.Header.Values("X-GitHub-Request-Id")
	if len(ids) != 1 || !validRequestID(ids[0], p.limits.MaxRequestIDBytes) {
		return result, errors.New("GitHub request identity is missing, duplicated, or unsafe")
	}
	result.RequestID = ids[0]
	encoding := strings.TrimSpace(strings.ToLower(response.Header.Get("Content-Encoding")))
	if encoding == "" {
		encoding = "identity"
	}
	if encoding != "identity" && encoding != "gzip" || strings.Contains(encoding, ",") {
		return result, errors.New("unsupported or multiple content encodings")
	}
	compressedRemaining := meter.budget.CompressedResponseBytes - meter.accounting.CompressedResponseBytes
	compressedLimit := minInt64(int64(p.limits.MaxCompressedResponseBodyBytes), compressedRemaining)
	if compressedLimit <= 0 {
		return result, errors.New("compressed response budget is exhausted")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, compressedLimit+1))
	if int64(len(raw)) > compressedLimit {
		return result, errors.New("compressed response body exceeds its limit")
	}
	if addErr := meter.addCompressed(int64(len(raw))); addErr != nil {
		return result, addErr
	}
	if err != nil {
		return result, errors.New("response body read failed")
	}
	decoded := raw
	if encoding == "gzip" {
		reader, gzipErr := gzip.NewReader(bytes.NewReader(raw))
		if gzipErr != nil {
			return result, errors.New("gzip response cannot be decoded")
		}
		decompressedRemaining := meter.budget.DecompressedResponseBytes - meter.accounting.DecompressedResponseBytes
		decompressedLimit := minInt64(int64(p.limits.MaxDecompressedResponseBodyBytes), decompressedRemaining)
		decoded, err = io.ReadAll(io.LimitReader(reader, decompressedLimit+1))
		closeErr := reader.Close()
		if int64(len(decoded)) > decompressedLimit {
			return result, errors.New("decompressed response body exceeds its limit")
		}
		if err != nil || closeErr != nil {
			return result, errors.New("gzip response read or close failed")
		}
	}
	if int64(len(decoded)) > int64(p.limits.MaxDecompressedResponseBodyBytes) {
		return result, errors.New("decompressed response body exceeds the fixed provider limit")
	}
	if err := meter.addDecompressed(int64(len(decoded))); err != nil {
		return result, err
	}
	if len(decoded) == 0 {
		return result, errors.New("GitHub response body is empty")
	}
	decoder := jsonDecoder(decoded)
	var value any
	if err := decoder.Decode(&value); err != nil {
		return result, errors.New("GitHub response is not valid JSON")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return result, err
	}
	result.Body = append([]byte(nil), decoded...)
	return result, nil
}

func jsonDecoder(data []byte) *json.Decoder {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder
}

func canonicalHeaderBytes(header http.Header) int64 {
	var total int64 = 2
	for name, values := range header {
		canonicalName := http.CanonicalHeaderKey(name)
		for _, value := range values {
			total += int64(len(canonicalName) + 2 + len(value) + 2)
		}
	}
	return total
}

func validRequestID(value string, limit int) bool {
	if len(value) == 0 || len(value) > limit {
		return false
	}
	for index, char := range []byte(value) {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' ||
			index > 0 && (char == ':' || char == '.' || char == '_' || char == '-') {
			continue
		}
		return false
	}
	return true
}

func sanitizeTransportError(err error) error {
	if err == nil {
		return nil
	}
	var status *responseStatusError
	if errors.As(err, &status) {
		return status
	}
	return errors.New("GitHub transport or response validation failed")
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func escapedSegment(value string) string { return url.PathEscape(value) }

func escapedBranchPath(value string) string {
	parts := strings.Split(value, "/")
	for index := range parts {
		parts[index] = escapedSegment(parts[index])
	}
	return strings.Join(parts, "/")
}

func repoPath(repository githublifecycle.Repository) string {
	return "/repos/" + escapedSegment(repository.Owner()) + "/" + escapedSegment(repository.Name())
}

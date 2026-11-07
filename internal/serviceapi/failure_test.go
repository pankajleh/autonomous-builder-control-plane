package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type testRunFailures struct {
	runs []string
	body json.RawMessage
	err  error
}

func (f *testRunFailures) ReadRunFailure(_ context.Context, run string) (json.RawMessage, error) {
	f.runs = append(f.runs, run)
	return f.body, f.err
}

const runFailureBody = `{"schema_version":"RunFailureV1","run_id":"run-1","projection_revision":"r","current_state":"FAILED","failure":{"cause":"VALIDATION_FAILED"}}`

// GET /v1/runs/{run}/failure (R1) routes to the failure reader for a registered run with an empty GET, answers its JSON
// as it came, and answers the read API's usual errors otherwise.
func TestRunFailureRoute(t *testing.T) {
	s, backend, catalog := forgetServer(t, PrincipalService)
	failures := &testRunFailures{body: json.RawMessage(runFailureBody)}
	s.reserved.Failures = failures
	get := func(method, path string, body []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		var reader *bytes.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		s.Handler().ServeHTTP(w, authenticatedRequest(method, path, reader))
		return w
	}
	w := get(http.MethodGet, "/v1/runs/run-1/failure", nil)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != runFailureBody || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") ||
		len(failures.runs) != 1 || failures.runs[0] != "run-1" {
		t.Fatalf("GET = %d %q %q, runs %v", w.Code, w.Body.String(), w.Header().Get("Content-Type"), failures.runs)
	}
	for _, request := range []struct {
		method, path string
		body         []byte
	}{
		{http.MethodGet, "/v1/runs/run-1/failure?page_size=1", nil},
		{http.MethodGet, "/v1/runs/run-1/failure", []byte(`{}`)},
		{http.MethodPost, "/v1/runs/run-1/failure", []byte(`{}`)},
	} {
		if w := get(request.method, request.path, request.body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"invalid_request"`) {
			t.Fatalf("%s %s = %d %q", request.method, request.path, w.Code, w.Body.String())
		}
	}
	if w := get(http.MethodGet, "/v1/runs/run-1/failure/more", nil); w.Code != http.StatusNotFound {
		t.Fatalf("a deeper path = %d", w.Code)
	}
	catalog.readErr = os.ErrNotExist
	if w := get(http.MethodGet, "/v1/runs/run-1/failure", nil); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
		t.Fatalf("unregistered = %d %q", w.Code, w.Body.String())
	}
	catalog.readErr = nil
	// Its check output is evidence: a forgotten run's is gone, as its evidence is.
	backend.forgotten = true
	if w := get(http.MethodGet, "/v1/runs/run-1/failure", nil); w.Code != http.StatusGone || !strings.Contains(w.Body.String(), `"code":"run_forgotten"`) {
		t.Fatalf("forgotten = %d %q", w.Code, w.Body.String())
	}
	backend.forgotten = false
	for err, code := range map[error]int{
		ErrAuthoritativeReadBusy: http.StatusServiceUnavailable, ErrProjectionIntegrity: http.StatusInternalServerError, ErrDependencyNotFound: http.StatusNotFound,
	} {
		failures.err = err
		if w := get(http.MethodGet, "/v1/runs/run-1/failure", nil); w.Code != code {
			t.Fatalf("%v = %d %q, want %d", err, w.Code, w.Body.String(), code)
		}
	}
	s.reserved.Failures = nil
	if w := get(http.MethodGet, "/v1/runs/run-1/failure", nil); w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), `"code":"unsupported_capability"`) {
		t.Fatalf("no reader = %d %q", w.Code, w.Body.String())
	}
}

// The route is new and additive: the run's detail, its capabilities and every other route answer exactly as before.
func TestRunFailureLeavesTheRunDetailAndCapabilitiesAsTheyWere(t *testing.T) {
	now := time.Now().UTC()
	dependencies := &testFutureDependencies{}
	server := newFutureTestServer(t, now, dependencies, dependencies, dependencies, dependencies, dependencies)
	before := httptest.NewRecorder()
	server.Handler().ServeHTTP(before, authenticatedRequest(http.MethodGet, "/v1/capabilities", nil))
	server.reserved.Failures = &testRunFailures{body: json.RawMessage(runFailureBody)}
	after := httptest.NewRecorder()
	server.Handler().ServeHTTP(after, authenticatedRequest(http.MethodGet, "/v1/capabilities", nil))
	if before.Body.String() != after.Body.String() {
		t.Fatalf("capabilities changed: %q -> %q", before.Body.String(), after.Body.String())
	}
	detail := httptest.NewRecorder()
	server.Handler().ServeHTTP(detail, authenticatedRequest(http.MethodGet, "/v1/runs/run-1", nil))
	if detail.Code != http.StatusOK || strings.Contains(detail.Body.String(), "failure") {
		t.Fatalf("run detail = %d %q", detail.Code, detail.Body.String())
	}
	anonymous := httptest.NewRecorder()
	server.Handler().ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/v1/runs/run-1/failure", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", anonymous.Code)
	}
}

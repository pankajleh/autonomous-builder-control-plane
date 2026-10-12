package serviceapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testEngines struct {
	answer EnginesV1
	err    error
	calls  int
}

func (e *testEngines) Engines(context.Context) (EnginesV1, error) { e.calls++; return e.answer, e.err }

func TestEnginesAnswersEachProfilesEngineOnlyToAnAuthenticatedGet(t *testing.T) {
	now := time.Now().UTC()
	server := newTestServer(t, &testCatalog{}, now)
	anonymous := httptest.NewRecorder()
	server.Handler().ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/v1/engines", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", anonymous.Code)
	}
	unsupported := httptest.NewRecorder()
	server.Handler().ServeHTTP(unsupported, authenticatedRequest(http.MethodGet, "/v1/engines", nil))
	if unsupported.Code != http.StatusNotImplemented || !strings.Contains(unsupported.Body.String(), "unsupported_capability") {
		t.Fatalf("without a catalog = %d %q", unsupported.Code, unsupported.Body.String())
	}

	catalog := &testEngines{answer: EnginesV1{SchemaVersion: "EnginesV1", Engines: []EngineV1{
		{ProfileID: "local-p02", Executor: "codex", TaskModel: "gpt-6-astra", TaskEffort: "xhigh", Access: "sign-in", Ready: true},
		{ProfileID: "local-p02-claude-api", Executor: "claude", Lane: "claude-api", LaneModel: "claude-sonnet-5-5", Access: "lane", Reason: "lane_no_key"},
	}}}
	server.reserved.Engines = catalog
	ok := httptest.NewRecorder()
	server.Handler().ServeHTTP(ok, authenticatedRequest(http.MethodGet, "/v1/engines", nil))
	want := `{"schema_version":"EnginesV1","engines":[` +
		`{"profile_id":"local-p02","executor":"codex","task_model":"gpt-6-astra","task_effort":"xhigh","access":"sign-in","ready":true},` +
		`{"profile_id":"local-p02-claude-api","executor":"claude","lane":"claude-api","lane_model":"claude-sonnet-5-5","access":"lane","ready":false,"reason":"lane_no_key"}]}` + "\n"
	if ok.Code != http.StatusOK || ok.Body.String() != want {
		t.Fatalf("engines = %d %q", ok.Code, ok.Body.String())
	}
	for _, request := range []*http.Request{
		authenticatedRequest(http.MethodPost, "/v1/engines", nil),
		authenticatedRequest(http.MethodGet, "/v1/engines?all=1", nil),
	} {
		refused := httptest.NewRecorder()
		server.Handler().ServeHTTP(refused, request)
		if refused.Code != http.StatusBadRequest {
			t.Fatalf("%s %s = %d", request.Method, request.URL, refused.Code)
		}
	}
	catalog.err = errors.New("profiles gone")
	failed := httptest.NewRecorder()
	server.Handler().ServeHTTP(failed, authenticatedRequest(http.MethodGet, "/v1/engines", nil))
	if failed.Code != http.StatusServiceUnavailable || !strings.Contains(failed.Body.String(), "engines_unavailable") || strings.Contains(failed.Body.String(), "profiles gone") {
		t.Fatalf("a failing catalog = %d %q", failed.Code, failed.Body.String())
	}
	if catalog.calls != 2 {
		t.Fatalf("refused requests never reach the catalog: %d calls", catalog.calls)
	}
}

package serviceapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const forgetRequestID = "0f8fad5b-d9cb-469f-a165-70867728950e"

type testForgetter struct {
	result    RunForgetV1
	err       error
	forgotten bool
	readErr   error
	calls     int
	lastRun   string
	last      RunForgetRequestV1
}

func (t *testForgetter) ForgetRun(_ context.Context, _ Principal, run string, request RunForgetRequestV1) (RunForgetV1, error) {
	t.calls++
	t.lastRun, t.last = run, request
	return t.result, t.err
}
func (t *testForgetter) RunForgotten(string) (bool, error) { return t.forgotten, t.readErr }

func forgetServer(t *testing.T, kind PrincipalType, authorities ...string) (*Server, *testForgetter, *testCatalog) {
	catalog := &testCatalog{}
	s := newTestServer(t, catalog, time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC))
	p := Principal{PrincipalID: "gateway", PrincipalType: kind, AuthnMethod: "test-v1"}
	s.authenticator = fixedAuthenticator{p}
	g := authorityGrant{authorities: map[string]struct{}{}, mayDelegate: true}
	for _, a := range authorities {
		g.authorities[a] = struct{}{}
	}
	s.authority.grants[p.PrincipalID] = g
	backend := &testForgetter{result: RunForgetV1{SchemaVersion: "RunForgetV1", RunID: "run-1", Status: "FORGOTTEN", Reason: "app-deleted", RequestID: forgetRequestID,
		ForgottenAt: "2026-11-05T10:00:00Z", RefsRemoved: []ForgottenRefV1{{Ref: "refs/heads/abcp/run-1", SHA: strings.Repeat("a", 40)}, {Ref: "refs/abcp/checkpoints/run-1/" + strings.Repeat("b", 40), SHA: strings.Repeat("b", 40)}}}}
	s.reserved.Forget = backend
	return s, backend, catalog
}

const forgetBody = `{"schema_version":1,"reason":"app-deleted","request_id":"` + forgetRequestID + `"}`

func TestRunForgetRequiresTheRunForgetGrant(t *testing.T) {
	for _, tc := range []struct {
		name        string
		kind        PrincipalType
		authorities []string
		code        int
	}{
		{"granted service", PrincipalService, []string{"run.forget"}, 200},
		{"worktree release only", PrincipalService, []string{"worktree.release", "preview.control"}, 403},
		{"no grant", PrincipalService, nil, 403},
		{"user", PrincipalUser, []string{"run.forget"}, 403},
		{"operator", PrincipalOperator, []string{"run.forget"}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, backend, _ := forgetServer(t, tc.kind, tc.authorities...)
			w := post(s, "/v1/runs/run-1/forget", forgetBody)
			if w.Code != tc.code {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.code == 403 && (backend.calls != 0 || !strings.Contains(w.Body.String(), `"code":"authority_denied"`)) {
				t.Fatal("a denied forget reached the backend", w.Body.String())
			}
		})
	}
}

func TestRunForgetTakesAStrictBodyAndMapsOutcomes(t *testing.T) {
	s, backend, catalog := forgetServer(t, PrincipalService, "run.forget")
	for name, body := range map[string]string{
		"empty":            ``,
		"empty object":     `{}`,
		"unknown field":    `{"schema_version":1,"reason":"app-deleted","request_id":"` + forgetRequestID + `","extra":1}`,
		"duplicate field":  `{"schema_version":1,"reason":"app-deleted","reason":"account-deleted","request_id":"` + forgetRequestID + `"}`,
		"other reason":     `{"schema_version":1,"reason":"task-closed","request_id":"` + forgetRequestID + `"}`,
		"schema version 2": `{"schema_version":2,"reason":"app-deleted","request_id":"` + forgetRequestID + `"}`,
		"upper-case uuid":  `{"schema_version":1,"reason":"app-deleted","request_id":"` + strings.ToUpper(forgetRequestID) + `"}`,
		"not a uuid":       `{"schema_version":1,"reason":"app-deleted","request_id":"request-1"}`,
		"missing request":  `{"schema_version":1,"reason":"app-deleted"}`,
		"two values":       forgetBody + forgetBody,
		"over 1 KiB":       `{"schema_version":1,"reason":"app-deleted","request_id":"` + forgetRequestID + `"` + strings.Repeat(" ", 1024) + `}`,
	} {
		if w := post(s, "/v1/runs/run-1/forget", body); w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"invalid_request"`) {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	for name, request := range map[string]*http.Request{
		"query": authenticatedRequest(http.MethodPost, "/v1/runs/run-1/forget?force=1", nil),
		"get":   authenticatedRequest(http.MethodGet, "/v1/runs/run-1/forget", nil),
	} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, request)
		if w.Code != 400 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if backend.calls != 0 {
		t.Fatal("an invalid request reached the backend")
	}
	w := post(s, "/v1/runs/run-1/forget", forgetBody)
	var got RunForgetV1
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Status != "FORGOTTEN" || len(got.RefsRemoved) != 2 ||
		backend.lastRun != "run-1" || backend.last != (RunForgetRequestV1{SchemaVersion: 1, Reason: "app-deleted", RequestID: forgetRequestID}) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("a forget answer may be cached")
	}
	for err, want := range map[error]string{ErrRunNotFinished: `"code":"run_not_finished"`, ErrRunOnHold: `"code":"run_on_hold"`} {
		backend.err = err
		if w := post(s, "/v1/runs/run-1/forget", forgetBody); w.Code != 409 || !strings.Contains(w.Body.String(), want) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	// A backend answer that breaks the contract is never passed through.
	backend.err = nil
	for name, mutate := range map[string]func(*RunForgetV1){
		"status":  func(v *RunForgetV1) { v.Status = "RELEASED" },
		"run":     func(v *RunForgetV1) { v.RunID = "run-2" },
		"time":    func(v *RunForgetV1) { v.ForgottenAt = "" },
		"no refs": func(v *RunForgetV1) { v.RefsRemoved = nil },
		"foreign": func(v *RunForgetV1) {
			v.RefsRemoved = []ForgottenRefV1{{Ref: "refs/heads/main", SHA: strings.Repeat("a", 40)}}
		},
		"other's": func(v *RunForgetV1) {
			v.RefsRemoved = []ForgottenRefV1{{Ref: "refs/abcp/checkpoints/run-2/x", SHA: strings.Repeat("a", 40)}}
		},
		"short sha": func(v *RunForgetV1) { v.RefsRemoved = []ForgottenRefV1{{Ref: "refs/heads/abcp/run-1", SHA: "abc"}} },
	} {
		good := backend.result
		bad := good
		bad.RefsRemoved = append([]ForgottenRefV1{}, good.RefsRemoved...)
		mutate(&bad)
		backend.result = bad
		if w := post(s, "/v1/runs/run-1/forget", forgetBody); w.Code != 500 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
		backend.result = good
	}
	calls := backend.calls
	catalog.readErr = os.ErrNotExist
	if w := post(s, "/v1/runs/run-1/forget", forgetBody); w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"not_found"`) || backend.calls != calls {
		t.Fatal("unknown run", w.Code, w.Body.String())
	}
	catalog.readErr = nil
	s.reserved.Forget = nil
	if w := post(s, "/v1/runs/run-1/forget", forgetBody); w.Code != 501 {
		t.Fatal(w.Code, w.Body.String())
	}
}

// A forgotten run's activity, timeline, evidence, previews and worktree release answer 410 run_forgotten; its detail and
// events (the ledger, kept) still answer.
func TestAForgottenRunsReadsAnswerGone(t *testing.T) {
	s, backend, _ := forgetServer(t, PrincipalService, "run.forget", "preview.control", "worktree.release")
	deps := &testFutureDependencies{}
	s.reserved.RunProjections, s.reserved.Events, s.reserved.Timeline, s.reserved.Evidence = deps, deps, deps, deps
	s.reserved.Activity = &activityTestReader{}
	s.reserved.Preview = &testPreviewController{available: true, result: previewDTO()}
	s.reserved.Worktrees = &testWorktrees{run: WorktreeReleaseV1{SchemaVersion: "WorktreeReleaseV1", RunID: "run-1", Status: "NO_WORKTREE"}}
	preview := strings.Repeat("a", 64)
	gone := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/runs/run-1/activity", ""},
		{http.MethodGet, "/v1/runs/run-1/activity/stream", ""},
		{http.MethodGet, "/v1/runs/run-1/timeline", ""},
		{http.MethodGet, "/v1/runs/run-1/evidence", ""},
		{http.MethodGet, "/v1/runs/run-1/evidence/item-1", ""},
		{http.MethodGet, "/v1/runs/run-1/previews", ""},
		{http.MethodGet, "/v1/runs/run-1/previews/" + preview, ""},
		{http.MethodGet, "/v1/runs/run-1/previews/" + preview + "/route", ""},
		{http.MethodPost, "/v1/runs/run-1/previews", `{}`},
		{http.MethodPost, "/v1/runs/run-1/worktree/release", `{"schema_version":1,"reason":"task-closed"}`},
	}
	serve := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		var request *http.Request
		if body == "" {
			request = authenticatedRequest(method, path, nil)
		} else {
			request = httptest.NewRequest(method, path, strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer valid")
		}
		s.Handler().ServeHTTP(w, request)
		return w
	}
	// Before the forget, each answers as it did (the stream, which stays open, aside).
	for _, r := range gone[:6] {
		if strings.HasSuffix(r.path, "/stream") {
			continue
		}
		if w := serve(r.method, r.path, r.body); w.Code != 200 {
			t.Fatalf("before: %s %s = %d %s", r.method, r.path, w.Code, w.Body.String())
		}
	}
	backend.forgotten = true
	for _, r := range gone {
		w := serve(r.method, r.path, r.body)
		if w.Code != http.StatusGone || !strings.Contains(w.Body.String(), `"code":"run_forgotten"`) {
			t.Fatalf("%s %s = %d %s", r.method, r.path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/v1/runs/run-1", "/v1/runs/run-1/events"} {
		if w := serve(http.MethodGet, path, ""); w.Code != 200 {
			t.Fatalf("%s = %d %s", path, w.Code, w.Body.String())
		}
	}
	// A record that cannot be read fails closed.
	backend.forgotten, backend.readErr = false, errors.New("unreadable")
	if w := serve(http.MethodGet, "/v1/runs/run-1/timeline", ""); w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
}

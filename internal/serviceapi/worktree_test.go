package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testWorktrees struct {
	run      WorktreeReleaseV1
	repo     RepositoryWorktreeReleaseV1
	err      error
	calls    int
	lastRun  string
	lastRepo RepositoryWorktreeReleaseRequestV1
}

func (t *testWorktrees) ReleaseRunWorktree(_ context.Context, _ Principal, run string, _ WorktreeReleaseRequestV1) (WorktreeReleaseV1, error) {
	t.calls++
	t.lastRun = run
	return t.run, t.err
}
func (t *testWorktrees) ReleaseRepositoryWorktrees(_ context.Context, _ Principal, r RepositoryWorktreeReleaseRequestV1) (RepositoryWorktreeReleaseV1, error) {
	t.calls++
	t.lastRepo = r
	return t.repo, t.err
}

func worktreeServer(t *testing.T, grant bool, kind PrincipalType) (*Server, *testWorktrees) {
	s := newTestServer(t, &testCatalog{}, time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC))
	p := Principal{PrincipalID: "gateway", PrincipalType: kind, AuthnMethod: "test-v1"}
	s.authenticator = fixedAuthenticator{p}
	g := authorityGrant{authorities: map[string]struct{}{"preview.control": {}}}
	if grant {
		g.authorities["worktree.release"] = struct{}{}
	}
	s.authority.grants[p.PrincipalID] = g
	backend := &testWorktrees{
		run:  WorktreeReleaseV1{SchemaVersion: "WorktreeReleaseV1", RunID: "run-1", Status: "RELEASED", Reason: "task-closed", ReleasedAt: "2026-09-28T10:00:00Z"},
		repo: RepositoryWorktreeReleaseV1{SchemaVersion: "RepositoryWorktreeReleaseV1", RepositoryIdentity: "owner/repo", Reason: "tenant-deleted", Released: 2, Unfinished: 1},
	}
	s.reserved.Worktrees = backend
	return s, backend
}

func post(s *Server, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authenticatedRequest(http.MethodPost, path, bytes.NewReader([]byte(body))))
	return w
}

func TestWorktreeReleaseRequiresTheServiceGrant(t *testing.T) {
	for _, tc := range []struct {
		name  string
		grant bool
		kind  PrincipalType
		code  int
	}{
		{"granted service", true, PrincipalService, 200},
		{"preview-only grant", false, PrincipalService, 403},
		{"user", true, PrincipalUser, 403},
		{"operator", true, PrincipalOperator, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, backend := worktreeServer(t, tc.grant, tc.kind)
			if w := post(s, "/v1/runs/run-1/worktree/release", `{"schema_version":1,"reason":"task-closed"}`); w.Code != tc.code {
				t.Fatal(w.Code, w.Body.String())
			}
			if w := post(s, "/v1/worktrees/release", `{"schema_version":1,"repository_identity":"owner/repo","reason":"tenant-deleted"}`); w.Code != tc.code {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.code != 200 && backend.calls != 0 {
				t.Fatal("a denied release reached the backend")
			}
		})
	}
}

func TestWorktreeReleaseValidatesRequestsAndMapsOutcomes(t *testing.T) {
	s, backend := worktreeServer(t, true, PrincipalService)
	for _, body := range []string{``, `{}`, `{"schema_version":1,"reason":"idle"}`, `{"schema_version":2,"reason":"task-closed"}`,
		`{"schema_version":1,"reason":"task-closed","extra":1}`, `{"schema_version":1,"reason":"tenant-deleted"}`} {
		if w := post(s, "/v1/runs/run-1/worktree/release", body); w.Code != 400 {
			t.Fatalf("run body %q: %d", body, w.Code)
		}
	}
	for _, body := range []string{`{"schema_version":1,"repository_identity":"","reason":"tenant-deleted"}`,
		`{"schema_version":1,"repository_identity":"owner/repo","reason":"task-closed"}`,
		`{"schema_version":1,"repository_identity":"` + strings.Repeat("a", 513) + `","reason":"tenant-deleted"}`} {
		if w := post(s, "/v1/worktrees/release", body); w.Code != 400 {
			t.Fatalf("repository body %q: %d", body, w.Code)
		}
	}
	if backend.calls != 0 {
		t.Fatal("an invalid request reached the backend")
	}
	w := post(s, "/v1/runs/run-1/worktree/release", `{"schema_version":1,"reason":"task-closed"}`)
	var got WorktreeReleaseV1
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got != backend.run || backend.lastRun != "run-1" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = post(s, "/v1/worktrees/release", `{"schema_version":1,"repository_identity":"owner/repo","reason":"tenant-deleted"}`)
	var repo RepositoryWorktreeReleaseV1
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &repo) != nil || repo != backend.repo || backend.lastRepo.RepositoryIdentity != "owner/repo" {
		t.Fatal(w.Code, w.Body.String())
	}
	backend.err = ErrRunNotFinished
	if w := post(s, "/v1/runs/run-1/worktree/release", `{"schema_version":1,"reason":"task-closed"}`); w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"run_not_finished"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	// A backend answer that breaks the contract is never passed through.
	backend.err, backend.run.ReleasedAt = nil, ""
	if w := post(s, "/v1/runs/run-1/worktree/release", `{"schema_version":1,"reason":"task-closed"}`); w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.reserved.Worktrees = nil
	if w := post(s, "/v1/runs/run-1/worktree/release", `{"schema_version":1,"reason":"task-closed"}`); w.Code != 501 {
		t.Fatal(w.Code, w.Body.String())
	}
}

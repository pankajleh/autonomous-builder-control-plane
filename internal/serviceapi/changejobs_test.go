package serviceapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

type testChangeJobs struct {
	calls []string
	err   error
}

var sampleChangeJob = ChangeJobV1{SchemaVersion: ChangeJobSchemaV1, ID: strings.Repeat("a", 64), RequestID: "req-1", Kind: "fix", State: "QUEUED",
	CreatedAt: "2026-10-28T04:00:00Z", Before: []StackPartV1{}, After: []StackPartV1{}}

func (t *testChangeJobs) StartChangeJob(_ context.Context, r ChangeJobCreateV1) (ChangeJobV1, error) {
	t.calls = append(t.calls, "start:"+r.RequestID+":"+r.Source+":"+r.Kind+":"+r.Words)
	return sampleChangeJob, t.err
}
func (t *testChangeJobs) ChangeJob(_ context.Context, id string) (ChangeJobV1, error) {
	t.calls = append(t.calls, "get:"+id[:4])
	return sampleChangeJob, t.err
}
func (t *testChangeJobs) ChangeJobPatch(_ context.Context, id string) ([]byte, error) {
	t.calls = append(t.calls, "patch:"+id[:4])
	return []byte("diff --git a/x b/x\n"), t.err
}
func (t *testChangeJobs) StopChangeJob(_ context.Context, id string) (ChangeJobV1, error) {
	t.calls = append(t.calls, "stop:"+id[:4])
	return sampleChangeJob, t.err
}

func changeJobServer(t *testing.T, grant bool) (*Server, *testChangeJobs) {
	s := newTestServer(t, &testCatalog{}, time.Date(2026, 10, 28, 4, 0, 0, 0, time.UTC))
	p := Principal{PrincipalID: "repo-c-service", PrincipalType: PrincipalService, AuthnMethod: "test-v1"}
	s.authenticator = fixedAuthenticator{p}
	g := authorityGrant{authorities: map[string]struct{}{"stack.run": {}}}
	if grant {
		g.authorities["change.run"] = struct{}{}
	}
	s.authority.grants[p.PrincipalID] = g
	backend := &testChangeJobs{}
	s.reserved.ChangeJobs = backend
	return s, backend
}

func TestChangeJobsStartReadPatchAndStopWithTheirGrant(t *testing.T) {
	s, backend := changeJobServer(t, true)
	key := strings.Repeat("b", 64)
	body := `{"request_id":"req-1","source":"project-1","cache_key":"` + key + `","kind":"fix","words":"The total is wrong."}`
	if r := call(s, http.MethodPost, "/v1/change-jobs", body); r.Code != http.StatusAccepted ||
		!strings.Contains(r.Body.String(), `"schema_version":"ChangeJobV1"`) || !strings.Contains(r.Body.String(), `"before":[]`) {
		t.Fatalf("start = %d %s", r.Code, r.Body.String())
	}
	id := strings.Repeat("a", 64)
	if r := call(s, http.MethodGet, "/v1/change-jobs/"+id, ""); r.Code != http.StatusOK {
		t.Fatalf("get = %d", r.Code)
	}
	r := call(s, http.MethodGet, "/v1/change-jobs/"+id+"/patch", "")
	if r.Code != http.StatusOK || r.Header().Get("Content-Type") != "text/x-diff; charset=utf-8" || r.Header().Get("Cache-Control") != "no-store" ||
		r.Body.String() != "diff --git a/x b/x\n" {
		t.Fatalf("patch = %d %v %q", r.Code, r.Header(), r.Body.String())
	}
	if r := call(s, http.MethodPost, "/v1/change-jobs/"+id+"/stop", ""); r.Code != http.StatusOK {
		t.Fatalf("stop = %d", r.Code)
	}
	if got := strings.Join(backend.calls, ","); got != "start:req-1:project-1:fix:The total is wrong.,get:aaaa,patch:aaaa,stop:aaaa" {
		t.Fatalf("calls = %s", got)
	}
	for _, bad := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/change-jobs", ""},
		{http.MethodPost, "/v1/change-jobs", strings.Replace(body, `"kind":"fix"`, `"kind":"deploy"`, 1)},
		{http.MethodPost, "/v1/change-jobs", strings.Replace(body, `"words":"The total is wrong."`, `"words":"  "`, 1)},
		{http.MethodPost, "/v1/change-jobs", strings.Replace(body, `"words":"The total is wrong."`, `"words":"`+strings.Repeat("x", 4001)+`"`, 1)},
		{http.MethodPost, "/v1/change-jobs", strings.Replace(body, `"words":"The total is wrong."`, `"words":"a\u0000b"`, 1)},
		{http.MethodPost, "/v1/change-jobs", strings.Replace(body, `}`, `,"engine":"x"}`, 1)},
		{http.MethodGet, "/v1/change-jobs/not-an-id", ""},
		{http.MethodGet, "/v1/change-jobs/" + id + "/x/y", ""},
		{http.MethodPost, "/v1/change-jobs/" + id + "/stop", `{"x":1}`},
		{http.MethodGet, "/v1/change-jobs?all=1", ""},
	} {
		if r := call(s, bad.method, bad.path, bad.body); r.Code != http.StatusBadRequest {
			t.Errorf("%s %s %.80q = %d, want 400", bad.method, bad.path, bad.body, r.Code)
		}
	}
	if r := call(s, http.MethodDelete, "/v1/change-jobs/"+id, ""); r.Code != http.StatusMethodNotAllowed {
		t.Fatalf("delete = %d", r.Code)
	}
	if len(backend.calls) != 4 {
		t.Fatalf("a refused request reached the backend: %v", backend.calls)
	}
}

func TestChangeJobsAnswerTheirCodesAndNeedTheirGrant(t *testing.T) {
	id := strings.Repeat("a", 64)
	for err, want := range map[error]struct {
		status int
		code   string
	}{
		ErrChangeJobNotAccepted:  {http.StatusUnprocessableEntity, "change_job_not_accepted"},
		ErrChangeJobNotFound:     {http.StatusNotFound, "change_job_not_found"},
		ErrChangeJobConflict:     {http.StatusConflict, "change_job_conflict"},
		ErrChangeJobsBusy:        {http.StatusTooManyRequests, "change_jobs_busy"},
		ErrChangeJobsUnavailable: {http.StatusServiceUnavailable, "change_jobs_unavailable"},
	} {
		s, backend := changeJobServer(t, true)
		backend.err = err
		for _, path := range []string{"/v1/change-jobs/" + id, "/v1/change-jobs/" + id + "/patch"} {
			if r := call(s, http.MethodGet, path, ""); r.Code != want.status || !strings.Contains(r.Body.String(), `"code":"`+want.code+`"`) {
				t.Errorf("%s %v = %d %s", path, err, r.Code, r.Body.String())
			}
		}
	}
	// stack.run alone does not reach change jobs.
	s, backend := changeJobServer(t, false)
	if r := call(s, http.MethodGet, "/v1/change-jobs/"+id, ""); r.Code != http.StatusForbidden || len(backend.calls) != 0 {
		t.Fatalf("without change.run = %d %v", r.Code, backend.calls)
	}
	s.reserved.ChangeJobs = nil
	if r := call(s, http.MethodGet, "/v1/change-jobs/"+id, ""); r.Code != http.StatusServiceUnavailable || !strings.Contains(r.Body.String(), "change_jobs_unavailable") {
		t.Fatalf("not configured = %d %s", r.Code, r.Body.String())
	}
}

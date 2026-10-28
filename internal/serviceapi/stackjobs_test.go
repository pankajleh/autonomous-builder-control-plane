package serviceapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

type testStackJobs struct {
	calls []string
	err   error
}

var sampleStackJob = StackJobV1{SchemaVersion: StackJobSchemaV1, ID: strings.Repeat("a", 64), RequestID: "req-1", State: "QUEUED",
	CreatedAt: "2026-10-28T03:00:00Z", Parts: []StackPartV1{}}

func (t *testStackJobs) StartStackJob(_ context.Context, r StackJobCreateV1) (StackJobV1, error) {
	t.calls = append(t.calls, "start:"+r.RequestID+":"+r.Source+":"+r.CacheKey[:4])
	return sampleStackJob, t.err
}
func (t *testStackJobs) StackJob(_ context.Context, id string) (StackJobV1, error) {
	t.calls = append(t.calls, "get:"+id[:4])
	return sampleStackJob, t.err
}
func (t *testStackJobs) StopStackJob(_ context.Context, id string) (StackJobV1, error) {
	t.calls = append(t.calls, "stop:"+id[:4])
	return sampleStackJob, t.err
}

func stackJobServer(t *testing.T, grant bool) (*Server, *testStackJobs) {
	s := newTestServer(t, &testCatalog{}, time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC))
	p := Principal{PrincipalID: "repo-c-service", PrincipalType: PrincipalService, AuthnMethod: "test-v1"}
	s.authenticator = fixedAuthenticator{p}
	g := authorityGrant{authorities: map[string]struct{}{"targets.control": {}}}
	if grant {
		g.authorities["stack.run"] = struct{}{}
	}
	s.authority.grants[p.PrincipalID] = g
	backend := &testStackJobs{}
	s.reserved.StackJobs = backend
	return s, backend
}

func TestStackJobsStartReadAndStopWithTheirGrant(t *testing.T) {
	s, backend := stackJobServer(t, true)
	key := strings.Repeat("b", 64)
	if r := call(s, http.MethodPost, "/v1/stack-jobs", `{"request_id":"req-1","source":"project-1","cache_key":"`+key+`"}`); r.Code != http.StatusAccepted ||
		!strings.Contains(r.Body.String(), `"schema_version":"StackJobV1"`) || !strings.Contains(r.Body.String(), `"parts":[]`) {
		t.Fatalf("start = %d %s", r.Code, r.Body.String())
	}
	id := strings.Repeat("a", 64)
	if r := call(s, http.MethodGet, "/v1/stack-jobs/"+id, ""); r.Code != http.StatusOK {
		t.Fatalf("get = %d", r.Code)
	}
	if r := call(s, http.MethodPost, "/v1/stack-jobs/"+id+"/stop", ""); r.Code != http.StatusOK {
		t.Fatalf("stop = %d", r.Code)
	}
	if got := strings.Join(backend.calls, ","); got != "start:req-1:project-1:bbbb,get:aaaa,stop:aaaa" {
		t.Fatalf("calls = %s", got)
	}
	for _, bad := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/stack-jobs", ""},
		{http.MethodPost, "/v1/stack-jobs", `{"request_id":"req-1","source":"project-1"}`},
		{http.MethodPost, "/v1/stack-jobs", `{"request_id":"req-1","source":"project-1","cache_key":"` + key + `","image":"x"}`},
		{http.MethodPost, "/v1/stack-jobs", `{"request_id":"req-1","source":"` + strings.Repeat("a", 3000) + `","cache_key":"` + key + `"}`},
		{http.MethodGet, "/v1/stack-jobs/not-an-id", ""},
		{http.MethodGet, "/v1/stack-jobs/" + id + "/x/y", ""},
		{http.MethodPost, "/v1/stack-jobs/" + id + "/stop", `{"x":1}`},
		{http.MethodGet, "/v1/stack-jobs?all=1", ""},
	} {
		if r := call(s, bad.method, bad.path, bad.body); r.Code != http.StatusBadRequest {
			t.Errorf("%s %s %q = %d, want 400", bad.method, bad.path, bad.body, r.Code)
		}
	}
	if r := call(s, http.MethodDelete, "/v1/stack-jobs/"+id, ""); r.Code != http.StatusMethodNotAllowed {
		t.Fatalf("delete = %d", r.Code)
	}
	if len(backend.calls) != 3 {
		t.Fatalf("a refused request reached the backend: %v", backend.calls)
	}
}

func TestStackJobsAnswerTheirCodesAndNeedTheirGrant(t *testing.T) {
	id := strings.Repeat("a", 64)
	for err, want := range map[error]struct {
		status int
		code   string
	}{
		ErrStackJobNotAccepted:  {http.StatusUnprocessableEntity, "stack_job_not_accepted"},
		ErrStackJobNotFound:     {http.StatusNotFound, "stack_job_not_found"},
		ErrStackJobConflict:     {http.StatusConflict, "stack_job_conflict"},
		ErrStackJobsBusy:        {http.StatusTooManyRequests, "stack_jobs_busy"},
		ErrStackJobsUnavailable: {http.StatusServiceUnavailable, "stack_jobs_unavailable"},
	} {
		s, backend := stackJobServer(t, true)
		backend.err = err
		if r := call(s, http.MethodGet, "/v1/stack-jobs/"+id, ""); r.Code != want.status || !strings.Contains(r.Body.String(), `"code":"`+want.code+`"`) {
			t.Errorf("%v = %d %s", err, r.Code, r.Body.String())
		}
	}
	s, backend := stackJobServer(t, false)
	if r := call(s, http.MethodGet, "/v1/stack-jobs/"+id, ""); r.Code != http.StatusForbidden || len(backend.calls) != 0 {
		t.Fatalf("without stack.run = %d %v", r.Code, backend.calls)
	}
	s.reserved.StackJobs = nil
	if r := call(s, http.MethodGet, "/v1/stack-jobs/"+id, ""); r.Code != http.StatusServiceUnavailable || !strings.Contains(r.Body.String(), "stack_jobs_unavailable") {
		t.Fatalf("not configured = %d %s", r.Code, r.Body.String())
	}
}

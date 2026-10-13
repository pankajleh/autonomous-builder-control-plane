package serviceapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testDeployments struct {
	calls []string
	got   int64
	err   error
}

var sampleOperation = OperationV1{SchemaVersion: OperationSchemaV1, OperationID: "op-0123456789abcdef", Target: "srv-0123456789ab", Kind: "apply",
	Site: "shop", PlanID: "pln-0123456789abcdef", RequestID: "req-12345678", State: "queued", StartedAt: "2026-10-14T01:00:00Z",
	Steps: []OperationStepV1{{Name: "send_files", State: "pending"}}}

func (d *testDeployments) PutRelease(_ context.Context, digest string, body io.Reader) (ReleaseV1, bool, error) {
	n, _ := io.Copy(io.Discard, body)
	d.got = n
	d.calls = append(d.calls, "put:"+digest)
	return ReleaseV1{SchemaVersion: ReleaseSchemaV1, Digest: digest, Kind: "static", Files: 2}, true, d.err
}
func (d *testDeployments) Release(_ context.Context, digest string) (ReleaseV1, error) {
	d.calls = append(d.calls, "get:"+digest)
	return ReleaseV1{SchemaVersion: ReleaseSchemaV1, Digest: digest}, d.err
}
func (d *testDeployments) CreatePlan(_ context.Context, target string, c PlanCreateV1) (PlanV1, error) {
	d.calls = append(d.calls, "plan:"+target+":"+c.Release[:8]+":"+c.Site.Name+":"+c.Site.Host)
	return PlanV1{SchemaVersion: PlanSchemaV1, PlanID: "pln-0123456789abcdef", Target: target, Steps: []string{"send_files"}, Hash: strings.Repeat("a", 64)}, d.err
}
func (d *testDeployments) Apply(_ context.Context, target string, a ApplyV1) (OperationV1, error) {
	d.calls = append(d.calls, "apply:"+target+":"+a.PlanID+":"+a.RequestID)
	return sampleOperation, d.err
}
func (d *testDeployments) Operation(_ context.Context, target, id string) (OperationV1, error) {
	d.calls = append(d.calls, "op:"+target+":"+id)
	return sampleOperation, d.err
}
func (d *testDeployments) Sites(_ context.Context, target string) (SitesV1, error) {
	d.calls = append(d.calls, "sites:"+target)
	return SitesV1{SchemaVersion: SitesSchemaV1, Target: target, Sites: []SiteV1{{Name: "shop", Host: "13-232-1-2.sslip.io", State: "live"}}}, d.err
}
func (d *testDeployments) RemoveSite(_ context.Context, target, site string, r SiteRemoveV1) (OperationV1, error) {
	d.calls = append(d.calls, "remove:"+target+":"+site+":"+r.RequestID)
	return sampleOperation, d.err
}

func deploymentServer(t *testing.T, grant bool) (*Server, *testDeployments) {
	s, _ := targetServer(t, grant)
	backend := &testDeployments{}
	s.reserved.Deployments = backend
	return s, backend
}

func upload(s *Server, digest string, body []byte, contentType string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := authenticatedRequest(http.MethodPut, "/v1/releases/"+digest, bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	s.Handler().ServeHTTP(response, request)
	return response
}

func TestReleasesPlansAndOperationsAreRoutedWithTheTargetsGrant(t *testing.T) {
	s, backend := deploymentServer(t, true)
	digest := strings.Repeat("ab", 32)
	// A release may be larger than any other request body.
	big := bytes.Repeat([]byte{'x'}, 3<<20)
	if r := upload(s, digest, big, "application/x-tar"); r.Code != http.StatusCreated || backend.got != int64(len(big)) || !strings.Contains(r.Body.String(), `"schema_version":"ReleaseV1"`) {
		t.Fatalf("put = %d %s (got %d bytes)", r.Code, r.Body.String(), backend.got)
	}
	if r := call(s, http.MethodGet, "/v1/releases/"+digest, ""); r.Code != http.StatusOK {
		t.Fatalf("get = %d", r.Code)
	}
	if r := call(s, http.MethodPost, "/v1/targets/srv-0123456789ab/plans", `{"release":"`+digest+`","site":{"name":"shop","host":"13-232-1-2.sslip.io"}}`); r.Code != http.StatusCreated || !strings.Contains(r.Body.String(), `"plan_id":"pln-0123456789abcdef"`) {
		t.Fatalf("plan = %d %s", r.Code, r.Body.String())
	}
	if r := call(s, http.MethodPost, "/v1/targets/srv-0123456789ab/apply", `{"plan_id":"pln-0123456789abcdef","plan_hash":"`+strings.Repeat("a", 64)+`","request_id":"req-12345678"}`); r.Code != http.StatusAccepted || !strings.Contains(r.Body.String(), `"operation_id":"op-0123456789abcdef"`) {
		t.Fatalf("apply = %d %s", r.Code, r.Body.String())
	}
	if r := call(s, http.MethodGet, "/v1/targets/srv-0123456789ab/operations/op-0123456789abcdef", ""); r.Code != http.StatusOK {
		t.Fatalf("operation = %d", r.Code)
	}
	if r := call(s, http.MethodGet, "/v1/targets/srv-0123456789ab/sites", ""); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"host":"13-232-1-2.sslip.io"`) {
		t.Fatalf("sites = %d %s", r.Code, r.Body.String())
	}
	if r := call(s, http.MethodPost, "/v1/targets/srv-0123456789ab/sites/shop/remove", `{"request_id":"req-87654321"}`); r.Code != http.StatusAccepted {
		t.Fatalf("remove = %d", r.Code)
	}
	want := []string{"put:" + digest, "get:" + digest, "plan:srv-0123456789ab:abababab:shop:13-232-1-2.sslip.io",
		"apply:srv-0123456789ab:pln-0123456789abcdef:req-12345678", "op:srv-0123456789ab:op-0123456789abcdef", "sites:srv-0123456789ab",
		"remove:srv-0123456789ab:shop:req-87654321"}
	if strings.Join(backend.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v", backend.calls)
	}
	backend.calls = nil
	for _, bad := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/releases/ABC", ""},
		{http.MethodGet, "/v1/releases/" + digest + "?x=1", ""},
		{http.MethodDelete, "/v1/releases/" + digest, ""},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/plans", `{"release":"x","site":{"name":"shop","host":"a.b"},"extra":1}`},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/apply", ""},
		{http.MethodGet, "/v1/targets/srv-0123456789ab/operations/op-zz", ""},
		{http.MethodGet, "/v1/targets/srv-0123456789ab/operations", ""},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/sites/Shop/remove", `{"request_id":"req-87654321"}`},
		{http.MethodDelete, "/v1/targets/srv-0123456789ab/sites/shop", ""},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/sites/shop/remove/now", `{}`},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/reboot/now", ""},
	} {
		if r := call(s, bad.method, bad.path, bad.body); r.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d", bad.method, bad.path, r.Code)
		}
	}
	if r := upload(s, digest, []byte("x"), "application/json"); r.Code != http.StatusBadRequest {
		t.Fatalf("a release is a tar: %d", r.Code)
	}
	// Any other request keeps its one-megabyte bound.
	if r := call(s, http.MethodPost, "/v1/targets/srv-0123456789ab/plans", strings.Repeat("x", 2<<20)); r.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a large plan body = %d", r.Code)
	}
	if r := upload(s, digest, bytes.Repeat([]byte{'x'}, MaxReleaseBytes+1), "application/x-tar"); r.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a release over the bound = %d", r.Code)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("calls = %v", backend.calls)
	}
}

func TestDeploymentsNeedTheGrantAndMapErrorsToStableCodes(t *testing.T) {
	s, backend := deploymentServer(t, false)
	digest := strings.Repeat("ab", 32)
	if r := upload(s, digest, []byte("x"), "application/x-tar"); r.Code != http.StatusForbidden || len(backend.calls) != 0 {
		t.Fatalf("without targets.control = %d %v", r.Code, backend.calls)
	}
	if r := call(s, http.MethodGet, "/v1/targets/srv-0123456789ab/sites", ""); r.Code != http.StatusForbidden || len(backend.calls) != 0 {
		t.Fatalf("sites without targets.control = %d", r.Code)
	}
	s, backend = deploymentServer(t, true)
	for err, want := range map[error]struct {
		status int
		code   string
	}{
		ErrReleaseInvalid:    {http.StatusBadRequest, "release_invalid"},
		ErrReleaseUnknown:    {http.StatusNotFound, "release_unknown"},
		ErrReleaseLimit:      {http.StatusConflict, "release_limit"},
		ErrPlanStale:         {http.StatusConflict, "plan_stale"},
		ErrTargetBusy:        {http.StatusConflict, "target_busy"},
		ErrTargetNotReady:    {http.StatusConflict, "target_not_ready"},
		ErrSiteUnknown:       {http.StatusNotFound, "site_unknown"},
		ErrOperationUnknown:  {http.StatusNotFound, "operation_unknown"},
		ErrTargetUnknown:     {http.StatusNotFound, "target_unknown"},
		ErrTargetInvalid:     {http.StatusBadRequest, "invalid_request"},
		ErrTargetLimit:       {http.StatusConflict, "target_limit"},
		ErrTargetUnavailable: {http.StatusServiceUnavailable, "target_unavailable"},
	} {
		backend.err = err
		r := call(s, http.MethodPost, "/v1/targets/srv-0123456789ab/apply", `{"plan_id":"pln-0123456789abcdef","plan_hash":"`+strings.Repeat("a", 64)+`","request_id":"req-12345678"}`)
		if r.Code != want.status || !strings.Contains(r.Body.String(), `"code":"`+want.code+`"`) {
			t.Errorf("%v = %d %s", err, r.Code, r.Body.String())
		}
	}
	s.reserved.Deployments = nil
	if r := call(s, http.MethodGet, "/v1/targets/srv-0123456789ab/sites", ""); r.Code != http.StatusNotImplemented {
		t.Fatalf("without deployments = %d", r.Code)
	}
	if r := call(s, http.MethodGet, "/v1/releases/"+digest, ""); r.Code != http.StatusNotImplemented {
		t.Fatalf("releases without deployments = %d", r.Code)
	}
}

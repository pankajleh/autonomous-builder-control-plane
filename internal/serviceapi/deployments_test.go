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
	if c.Restore != nil {
		d.calls = append(d.calls, "restore:"+target+":"+c.Restore.Site+":"+c.Restore.Backup)
	} else {
		d.calls = append(d.calls, "plan:"+target+":"+c.Release[:8]+":"+c.Site.Name+":"+c.Site.Host)
	}
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

func (d *testDeployments) Backups(_ context.Context, target, site string) (BackupsV1, error) {
	d.calls = append(d.calls, "backups:"+target+":"+site)
	return BackupsV1{SchemaVersion: BackupsSchemaV1, Target: target, Site: site, Database: "app_shop",
		Backups: []BackupV1{{BackupID: "bk-20261014T013000Z-0a1b", Reason: "before_update", TakenAt: "2026-10-14T01:30:00Z", Bytes: 2048}}}, d.err
}
func (d *testDeployments) BackUp(_ context.Context, target, site string, c BackupCreateV1) (OperationV1, error) {
	d.calls = append(d.calls, "backup:"+target+":"+site+":"+c.RequestID)
	return sampleOperation, d.err
}

func (d *testDeployments) CheckBackup(_ context.Context, target, site, backup string, c BackupCreateV1) (OperationV1, error) {
	d.calls = append(d.calls, "check:"+target+":"+site+":"+backup+":"+c.RequestID)
	return sampleOperation, d.err
}

func (d *testDeployments) StopSite(_ context.Context, target, site string, r OperationRequestV1) (OperationV1, error) {
	d.calls = append(d.calls, "stop:"+target+":"+site+":"+r.RequestID)
	return sampleOperation, d.err
}
func (d *testDeployments) StartSite(_ context.Context, target, site string, r OperationRequestV1) (OperationV1, error) {
	d.calls = append(d.calls, "start:"+target+":"+site+":"+r.RequestID)
	return sampleOperation, d.err
}
func (d *testDeployments) Databases(_ context.Context, target string) (DatabasesV1, error) {
	d.calls = append(d.calls, "databases:"+target)
	return DatabasesV1{SchemaVersion: DatabasesSchemaV1, Target: target, Databases: []DatabaseV1{{Site: "shop", Database: "app_shop", CreatedAt: "2026-10-14T01:00:00Z"}}}, d.err
}
func (d *testDeployments) RemoveData(_ context.Context, target, site string, r ConfirmedRequestV1) (OperationV1, error) {
	d.calls = append(d.calls, "remove-data:"+target+":"+site+":"+r.RequestID+":"+r.Confirm)
	return sampleOperation, d.err
}
func (d *testDeployments) CleanTarget(_ context.Context, target string, r ConfirmedRequestV1) (OperationV1, error) {
	d.calls = append(d.calls, "clean:"+target+":"+r.RequestID+":"+r.Confirm)
	return sampleOperation, d.err
}
func (d *testDeployments) CheckDrift(_ context.Context, target string, r OperationRequestV1) (OperationV1, error) {
	d.calls = append(d.calls, "drift:"+target+":"+r.RequestID)
	return sampleOperation, d.err
}
func (d *testDeployments) Drift(_ context.Context, target string) (DriftV1, error) {
	d.calls = append(d.calls, "drift-report:"+target)
	return DriftV1{SchemaVersion: DriftSchemaV1, Target: target, Checked: true, CheckedAt: "2026-10-14T01:00:00Z", OK: true,
		Findings: []DriftFindingV1{}, Reconciled: []string{}}, d.err
}

func (d *testDeployments) Name(_ context.Context, target, host string) (NameV1, error) {
	d.calls = append(d.calls, "name:"+target+":"+host)
	return NameV1{SchemaVersion: NameSchemaV1, Target: target, Host: host, Address: "13.232.1.2", Addresses: []string{"13.232.1.2"}, Points: true,
		CheckedAt: "2026-10-14T01:00:00Z"}, d.err
}
func (d *testDeployments) CheckSite(_ context.Context, target, site string, r OperationRequestV1) (OperationV1, error) {
	d.calls = append(d.calls, "check-site:"+target+":"+site+":"+r.RequestID)
	return sampleOperation, d.err
}
func (d *testDeployments) SiteSetup(_ context.Context, target, site string) (SiteSetupV1, error) {
	d.calls = append(d.calls, "setup:"+target+":"+site)
	return SiteSetupV1{SchemaVersion: SiteSetupSchemaV1, Target: target, Site: site, Host: "crm.example.com", Token: strings.Repeat("ab", 16)}, d.err
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
	if r := call(s, http.MethodGet, "/v1/targets/srv-0123456789ab/sites/shop/backups", ""); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"database":"app_shop"`) ||
		!strings.Contains(r.Body.String(), `"reason":"before_update"`) {
		t.Fatalf("backups = %d %s", r.Code, r.Body.String())
	}
	if r := call(s, http.MethodPost, "/v1/targets/srv-0123456789ab/sites/shop/backups", `{"request_id":"req-backup-01"}`); r.Code != http.StatusAccepted {
		t.Fatalf("back up = %d", r.Code)
	}
	if r := call(s, http.MethodPost, "/v1/targets/srv-0123456789ab/plans", `{"restore":{"site":"shop","backup":"bk-20261014T013000Z-0a1b"}}`); r.Code != http.StatusCreated {
		t.Fatalf("restore plan = %d %s", r.Code, r.Body.String())
	}
	if r := call(s, http.MethodPost, "/v1/targets/srv-0123456789ab/sites/shop/backups/bk-20261014T013000Z-0a1b/check", `{"request_id":"req-check-001"}`); r.Code != http.StatusAccepted {
		t.Fatalf("check = %d %s", r.Code, r.Body.String())
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/targets/srv-0123456789ab/sites/shop/stop", `{"request_id":"req-stop-0001"}`},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/sites/shop/start", `{"request_id":"req-start-001"}`},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/databases/shop/remove", `{"request_id":"req-data-0001","confirm":"app_shop"}`},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/clean", `{"request_id":"req-clean-001","confirm":"srv-0123456789ab"}`},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/drift", `{"request_id":"req-drift-001"}`},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/sites/shop/check", `{"request_id":"req-site-0001"}`},
	} {
		if r := call(s, c.method, c.path, c.body); r.Code != http.StatusAccepted {
			t.Fatalf("%s = %d %s", c.path, r.Code, r.Body.String())
		}
	}
	if r := call(s, http.MethodGet, "/v1/targets/srv-0123456789ab/databases", ""); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"on_server":false`) {
		t.Fatalf("databases = %d %s", r.Code, r.Body.String())
	}
	if r := call(s, http.MethodGet, "/v1/targets/srv-0123456789ab/drift", ""); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"schema_version":"DriftV1"`) {
		t.Fatalf("drift = %d %s", r.Code, r.Body.String())
	}
	if r := call(s, http.MethodGet, "/v1/targets/srv-0123456789ab/names/shop.example.com", ""); r.Code != http.StatusOK ||
		!strings.Contains(r.Body.String(), `"schema_version":"NameV1"`) || !strings.Contains(r.Body.String(), `"points":true`) {
		t.Fatalf("name = %d %s", r.Code, r.Body.String())
	}
	if r := call(s, http.MethodGet, "/v1/targets/srv-0123456789ab/sites/shop/setup", ""); r.Code != http.StatusOK ||
		!strings.Contains(r.Body.String(), `"schema_version":"SiteSetupV1"`) || !strings.Contains(r.Body.String(), `"token":"`+strings.Repeat("ab", 16)+`"`) {
		t.Fatalf("setup = %d %s", r.Code, r.Body.String())
	}
	want := []string{"put:" + digest, "get:" + digest, "plan:srv-0123456789ab:abababab:shop:13-232-1-2.sslip.io",
		"apply:srv-0123456789ab:pln-0123456789abcdef:req-12345678", "op:srv-0123456789ab:op-0123456789abcdef", "sites:srv-0123456789ab",
		"remove:srv-0123456789ab:shop:req-87654321",
		"backups:srv-0123456789ab:shop", "backup:srv-0123456789ab:shop:req-backup-01",
		"restore:srv-0123456789ab:shop:bk-20261014T013000Z-0a1b", "check:srv-0123456789ab:shop:bk-20261014T013000Z-0a1b:req-check-001",
		"stop:srv-0123456789ab:shop:req-stop-0001", "start:srv-0123456789ab:shop:req-start-001",
		"remove-data:srv-0123456789ab:shop:req-data-0001:app_shop", "clean:srv-0123456789ab:req-clean-001:srv-0123456789ab",
		"drift:srv-0123456789ab:req-drift-001", "check-site:srv-0123456789ab:shop:req-site-0001", "databases:srv-0123456789ab",
		"drift-report:srv-0123456789ab", "name:srv-0123456789ab:shop.example.com", "setup:srv-0123456789ab:shop"}
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
		{http.MethodGet, "/v1/targets/srv-0123456789ab/sites/Shop/backups", ""},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/sites/shop/backups", `{"request_id":"req-backup-01","extra":1}`},
		{http.MethodDelete, "/v1/targets/srv-0123456789ab/sites/shop/backups", ""},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/plans", `{"restore":{"site":"shop","backup":"bk-1","extra":1}}`},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/sites/shop/backups/bk-1/check", `{"request_id":"req-check-001"}`},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/sites/shop/backups/bk-20261014T013000Z-0a1b/check", `{"request_id":"req-check-001","x":1}`},
		{http.MethodGet, "/v1/targets/srv-0123456789ab/sites/shop/backups/bk-20261014T013000Z-0a1b/check", ""},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/sites/shop/backups/bk-20261014T013000Z-0a1b/restore", `{"request_id":"req-check-001"}`},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/sites/shop/backups/bk-20261014T013000Z-0a1b/check/now", `{"request_id":"req-check-001"}`},
		{http.MethodGet, "/v1/targets/srv-0123456789ab/sites/shop/stop", ""},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/sites/shop/pause", `{"request_id":"req-stop-0001"}`},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/sites/shop/stop", `{"request_id":"req-stop-0001","x":1}`},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/databases/Shop/remove", `{"request_id":"req-data-0001","confirm":"app_shop"}`},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/databases/shop/remove", `{"request_id":"req-data-0001"}x`},
		{http.MethodDelete, "/v1/targets/srv-0123456789ab/databases", ""},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/clean", `{"request_id":"req-clean-001","confirm":"x","y":1}`},
		{http.MethodPut, "/v1/targets/srv-0123456789ab/drift", ""},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/drift/now", `{"request_id":"req-drift-001"}`},
		{http.MethodGet, "/v1/targets/srv-0123456789ab/sites/shop/check", ""},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/sites/Shop/check", `{"request_id":"req-site-0001"}`},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/sites/shop/check", `{"request_id":"req-site-0001","x":1}`},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/names/shop.example.com", ""},
		{http.MethodGet, "/v1/targets/srv-0123456789ab/names", ""},
		{http.MethodGet, "/v1/targets/srv-0123456789ab/names/shop.example.com/x", ""},
		{http.MethodPost, "/v1/targets/srv-0123456789ab/sites/shop/setup", ""},
		{http.MethodGet, "/v1/targets/srv-0123456789ab/sites/Shop/setup", ""},
		{http.MethodGet, "/v1/targets/srv-0123456789ab/sites/shop/setup/x", ""},
		{http.MethodGet, "/v1/targets/srv-0123456789ab/sites/shop/setup", "{}"},
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
		ErrBackupUnknown:     {http.StatusNotFound, "backup_unknown"},
		ErrSiteStopped:       {http.StatusConflict, "site_stopped"},
		ErrDataInUse:         {http.StatusConflict, "data_in_use"},
		ErrTargetInUse:       {http.StatusConflict, "target_in_use"},
		ErrSetupPending:      {http.StatusConflict, "setup_pending"},
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

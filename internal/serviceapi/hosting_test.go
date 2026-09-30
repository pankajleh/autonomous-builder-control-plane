package serviceapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

type testHosting struct {
	hosted  HostedV1
	backup  HostedBackupV1
	route   HostedRouteV1
	err     error
	calls   []string
	lastKey string
}

func (t *testHosting) record(op, key string) { t.calls = append(t.calls, op); t.lastKey = key }
func (t *testHosting) ReadHosted(_ context.Context, _ Principal, key string) (HostedV1, error) {
	t.record("read", key)
	return t.hosted, t.err
}
func (t *testHosting) StartHosted(_ context.Context, _ Principal, _ string, key string, _ HostedStartRequestV1) (HostedV1, error) {
	t.record("start", key)
	return t.hosted, t.err
}
func (t *testHosting) StopHosted(_ context.Context, _ Principal, _ string, key string, _ HostedCommandRequestV1) (HostedV1, error) {
	t.record("stop", key)
	return t.hosted, t.err
}
func (t *testHosting) ResolveHostedRoute(_ context.Context, _ Principal, key string) (HostedRouteV1, error) {
	t.record("route", key)
	return t.route, t.err
}
func (t *testHosting) BackupHosted(_ context.Context, _ Principal, _ string, key string, _ HostedBackupRequestV1) (HostedBackupV1, error) {
	t.record("backup", key)
	return t.backup, t.err
}
func (t *testHosting) ListHostedBackups(_ context.Context, _ Principal, key string) (HostedBackupListV1, error) {
	t.record("backups", key)
	return HostedBackupListV1{SchemaVersion: "HostedBackupListV1", HostingKey: key, Backups: []HostedBackupV1{t.backup}}, t.err
}
func (t *testHosting) RestoreHosted(_ context.Context, _ Principal, _ string, key string, _ HostedRestoreRequestV1) (HostedV1, error) {
	t.record("restore", key)
	return t.hosted, t.err
}
func (t *testHosting) PurgeHosted(_ context.Context, _ Principal, _ string, key string, _ HostedCommandRequestV1) (HostedV1, error) {
	t.record("purge", key)
	return t.hosted, t.err
}

func readyHosted() HostedV1 {
	return HostedV1{SchemaVersion: "HostedV1", HostingKey: "app-1", Desired: "RUNNING", Status: "READY", Health: "HEALTHY", Generation: 1,
		RunID: "run-1", CheckpointActivityID: strings.Repeat("b", 64), SourceSHA: strings.Repeat("c", 40), ProfileID: "node-pg-hosted-v1", ProfileDigest: strings.Repeat("d", 64),
		ProductAuthorizationID: "auth-1", ProductTaskID: "task-1", ProductVersionID: "version-1", RouteHandle: strings.Repeat("e", 64), Data: "PRESENT",
		CreatedAt: "2026-09-30T10:00:00Z", UpdatedAt: "2026-09-30T10:01:00Z"}
}

func hostingServer(t *testing.T, grant, delegate bool, kind PrincipalType) (*Server, *testHosting) {
	s := newTestServer(t, &testCatalog{runs: []runtimecatalog.RunRegistrationV1{{RunID: "run-1"}}}, time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC))
	p := Principal{PrincipalID: "gateway", PrincipalType: kind, AuthnMethod: "test-v1"}
	s.authenticator = fixedAuthenticator{p}
	g := authorityGrant{authorities: map[string]struct{}{"preview.control": {}}, mayDelegate: delegate}
	if grant {
		g.authorities["hosting.control"] = struct{}{}
	}
	s.authority.grants[p.PrincipalID] = g
	backend := &testHosting{
		hosted: readyHosted(),
		backup: HostedBackupV1{SchemaVersion: "HostedBackupV1", HostingKey: "app-1", BackupID: strings.Repeat("f", 64), Reason: "manual", TakenAt: "2026-09-30T10:02:00Z", Bytes: 10, Generation: 1, SourceSHA: strings.Repeat("c", 40)},
		route:  HostedRouteV1{SchemaVersion: "HostedRouteV1", HostingKey: "app-1", RouteHandle: strings.Repeat("e", 64), TargetURL: "http://127.0.0.1:4321"},
	}
	s.reserved.Hosting = backend
	return s, backend
}

func get(s *Server, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, authenticatedRequest(http.MethodGet, path, nil))
	return w
}

const actor = `"delegated_actor":{"subject_id":"alice","subject_type":"user"}`

var startBody = `{"schema_version":1,"request_id":"start-1","run_id":"run-1","checkpoint_activity_id":"` + strings.Repeat("b", 64) + `","profile_id":"node-pg-hosted-v1",` + actor + `}`
var commandBody = `{"schema_version":1,"request_id":"cmd-1",` + actor + `}`
var backupBody = `{"schema_version":1,"request_id":"backup-1","reason":"pre-update",` + actor + `}`
var restoreBody = `{"schema_version":1,"request_id":"restore-1","backup_id":"` + strings.Repeat("f", 64) + `",` + actor + `}`

func TestHostedRoutesRequireTheHostingGrant(t *testing.T) {
	for _, tc := range []struct {
		name            string
		grant, delegate bool
		kind            PrincipalType
		read, write     int
	}{
		{"granted service", true, true, PrincipalService, 200, 202},
		{"preview-only grant", false, true, PrincipalService, 403, 403},
		{"no delegation", true, false, PrincipalService, 200, 403},
		{"user", true, true, PrincipalUser, 403, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, backend := hostingServer(t, tc.grant, tc.delegate, tc.kind)
			if w := get(s, "/v1/hosted/app-1"); w.Code != tc.read {
				t.Fatal("read", w.Code, w.Body.String())
			}
			if w := post(s, "/v1/hosted/app-1/start", startBody); w.Code != tc.write {
				t.Fatal("start", w.Code, w.Body.String())
			}
			if tc.write != 202 {
				for _, call := range backend.calls {
					if call != "read" {
						t.Fatal("a denied command reached the backend", backend.calls)
					}
				}
			}
		})
	}
}

func TestHostedRoutesServeEveryOperation(t *testing.T) {
	s, backend := hostingServer(t, true, true, PrincipalService)
	for _, tc := range []struct {
		method, path, body string
		code               int
		op                 string
	}{
		{"GET", "/v1/hosted/app-1", "", 200, "read"},
		{"GET", "/v1/hosted/app-1/route", "", 200, "route"},
		{"GET", "/v1/hosted/app-1/backups", "", 200, "backups"},
		{"POST", "/v1/hosted/app-1/start", startBody, 202, "start"},
		{"POST", "/v1/hosted/app-1/stop", commandBody, 200, "stop"},
		{"POST", "/v1/hosted/app-1/backups", backupBody, 200, "backup"},
		{"POST", "/v1/hosted/app-1/restore", restoreBody, 200, "restore"},
		{"POST", "/v1/hosted/app-1/purge", commandBody, 200, "purge"},
	} {
		backend.calls = nil
		var w *httptest.ResponseRecorder
		if tc.method == "GET" {
			w = get(s, tc.path)
		} else {
			w = post(s, tc.path, tc.body)
		}
		if w.Code != tc.code || len(backend.calls) != 1 || backend.calls[0] != tc.op || backend.lastKey != "app-1" {
			t.Fatalf("%s %s: %d %s calls %v", tc.method, tc.path, w.Code, w.Body.String(), backend.calls)
		}
	}
	var route HostedRouteV1
	if json.Unmarshal(get(s, "/v1/hosted/app-1/route").Body.Bytes(), &route) != nil || route.TargetURL != "http://127.0.0.1:4321" {
		t.Fatal("route body", route)
	}
}

func TestHostedRoutesRefuseBadRequests(t *testing.T) {
	s, backend := hostingServer(t, true, true, PrincipalService)
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{"GET", "/v1/hosted/-bad", "", 400},
		{"GET", "/v1/hosted/", "", 400},
		{"GET", "/v1/hosted/app-1/unknown", "", 404},
		{"GET", "/v1/hosted/app-1/start", "", 400},
		{"GET", "/v1/hosted/app-1/a/b", "", 400},
		{"POST", "/v1/hosted/app-1", commandBody, 400},
		{"POST", "/v1/hosted/app-1/start", `{}`, 400},
		{"POST", "/v1/hosted/app-1/start", strings.Replace(startBody, "}", `,"extra":1}`, 1), 400},
		{"POST", "/v1/hosted/app-1/backups", strings.Replace(backupBody, "pre-update", "nightly", 1), 400},
		{"POST", "/v1/hosted/app-1/restore", strings.Replace(restoreBody, strings.Repeat("f", 64), "not-an-id", 1), 400},
		{"POST", "/v1/hosted/app-1/stop", strings.Replace(commandBody, `"user"`, `"service"`, 1), 400},
	} {
		backend.calls = nil
		var w *httptest.ResponseRecorder
		if tc.method == "GET" {
			w = get(s, tc.path)
		} else {
			w = post(s, tc.path, tc.body)
		}
		if w.Code != tc.code || len(backend.calls) != 0 {
			t.Fatalf("%s %s %s: %d %s calls %v", tc.method, tc.path, tc.body, w.Code, w.Body.String(), backend.calls)
		}
	}
	s.catalog.(*testCatalog).readErr = os.ErrNotExist
	if w := post(s, "/v1/hosted/app-1/start", startBody); w.Code != 404 || len(backend.calls) != 0 {
		t.Fatalf("a start for an unregistered run: %d calls %v", w.Code, backend.calls)
	}
}

func TestHostedRoutesMapBackendErrorsAndInvalidResults(t *testing.T) {
	s, backend := hostingServer(t, true, true, PrincipalService)
	for _, tc := range []struct {
		err  error
		code int
		name string
	}{
		{ErrHostedConflict, 409, "hosted_state_conflict"},
		{ErrHostedCapacity, 409, "hosted_capacity"},
		{ErrDependencyNotFound, 404, "not_found"},
		{ErrPreviewUnavailable, 503, "NOT_AVAILABLE"},
		{ErrPreviewProfile, 404, "unknown_preview_profile"},
		{ErrRequestIDConflict, 409, "request_id_conflict"},
	} {
		backend.err = tc.err
		w := post(s, "/v1/hosted/app-1/start", startBody)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), `"code":"`+tc.name+`"`) {
			t.Fatalf("%v: %d %s", tc.err, w.Code, w.Body.String())
		}
	}
	backend.err = nil
	backend.hosted.HostingKey = "app-2"
	if w := get(s, "/v1/hosted/app-1"); w.Code != 500 {
		t.Fatal("a result for another key was served", w.Code)
	}
	backend.hosted = readyHosted()
	backend.hosted.RouteHandle = ""
	if w := get(s, "/v1/hosted/app-1"); w.Code != 500 {
		t.Fatal("a ready instance without a route was served", w.Code)
	}
	backend.route.TargetURL = "http://example.com:80"
	if w := get(s, "/v1/hosted/app-1/route"); w.Code != 500 {
		t.Fatal("a non-loopback route was served", w.Code)
	}
	s.reserved.Hosting = nil
	if w := get(s, "/v1/hosted/app-1"); w.Code != 501 {
		t.Fatal("missing hosting capability", w.Code)
	}
}

func TestHostedV1Validation(t *testing.T) {
	good := readyHosted()
	if ValidateHostedV1(good, "app-1") != nil {
		t.Fatal("ready instance rejected")
	}
	cases := map[string]func(*HostedV1){
		"stopped-with-route":   func(v *HostedV1) { v.Status, v.Desired, v.Health = "STOPPED", "STOPPED", "UNKNOWN" },
		"stopped-but-desired":  func(v *HostedV1) { v.Status, v.Health, v.RouteHandle = "STOPPED", "UNKNOWN", "" },
		"purged-while-running": func(v *HostedV1) { v.Data = "PURGED" },
		"no-generation":        func(v *HostedV1) { v.Generation = 0 },
		"unknown-status":       func(v *HostedV1) { v.Status = "PAUSED" },
		"ready-unknown-health": func(v *HostedV1) { v.Health = "UNKNOWN" },
		"bad-sha":              func(v *HostedV1) { v.SourceSHA = "HEAD" },
	}
	for name, mutate := range cases {
		v := readyHosted()
		mutate(&v)
		if ValidateHostedV1(v, "app-1") == nil {
			t.Error(name, "was accepted")
		}
	}
}

package serviceapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	setup   HostedSetupV1
	errors  HostedErrorsV1
	file    string
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
func (t *testHosting) OpenHostedBackup(_ context.Context, _ Principal, key, _ string) (io.ReadCloser, int64, error) {
	t.record("backup-file", key)
	if t.err != nil {
		return nil, 0, t.err
	}
	return io.NopCloser(strings.NewReader(t.file)), int64(len(t.file)), nil
}
func (t *testHosting) PurgeHosted(_ context.Context, _ Principal, _ string, key string, _ HostedCommandRequestV1) (HostedV1, error) {
	t.record("purge", key)
	return t.hosted, t.err
}
func (t *testHosting) ReadHostedSetup(_ context.Context, _ Principal, key string) (HostedSetupV1, error) {
	t.record("setup", key)
	return t.setup, t.err
}
func (t *testHosting) HostedErrors(_ context.Context, _ Principal, key string) (HostedErrorsV1, error) {
	t.record("errors", key)
	return t.errors, t.err
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
		setup:  HostedSetupV1{SchemaVersion: "HostedSetupV1", HostingKey: "app-1", Token: strings.Repeat("0a", 16)},
		errors: HostedErrorsV1{SchemaVersion: "HostedErrorsV1", HostingKey: "app-1", ReadAt: "2026-09-30T10:03:00Z", Since: "2026-09-30T09:00:00Z", Running: true, Restarts: 1, Lines: []string{"Error: one"}},
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
		{"GET", "/v1/hosted/app-1/setup", "", 200, "setup"},
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
	var setup HostedSetupV1
	if json.Unmarshal(get(s, "/v1/hosted/app-1/setup").Body.Bytes(), &setup) != nil || setup != backend.setup {
		t.Fatal("setup body", setup)
	}
}

// BF-07c2 (Repo C design note BF07_WHEN_A_RUNNING_APP_GOES_WRONG §13): a hosted app's recent errors are read, never
// written, through the hosting grant, never kept by a cache, and never served past their bounds.
func TestAHostedAppsRecentErrorsAreReadOnlyAndBounded(t *testing.T) {
	s, backend := hostingServer(t, true, true, PrincipalService)
	w := get(s, "/v1/hosted/app-1/errors")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || backend.lastKey != "app-1" {
		t.Fatal("errors", w.Code, w.Header(), w.Body.String())
	}
	var got HostedErrorsV1
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Restarts != 1 || !got.Running || len(got.Lines) != 1 || got.ExitCode != nil || got.Since != backend.errors.Since {
		t.Fatal("errors body", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "exit_code") {
		t.Fatal("no end, no exit code", w.Body.String())
	}
	backend.calls = nil
	if w := post(s, "/v1/hosted/app-1/errors", commandBody); w.Code != 400 || len(backend.calls) != 0 {
		t.Fatal("a write to the errors", w.Code, backend.calls)
	}
	many := make([]string, SiteErrorLines+1)
	for i := range many {
		many[i] = "Error"
	}
	for name, bad := range map[string]HostedErrorsV1{
		"other key":     {SchemaVersion: "HostedErrorsV1", HostingKey: "app-2", ReadAt: "2026-09-30T10:03:00Z", Lines: []string{}},
		"no lines":      {SchemaVersion: "HostedErrorsV1", HostingKey: "app-1", ReadAt: "2026-09-30T10:03:00Z"},
		"too many":      {SchemaVersion: "HostedErrorsV1", HostingKey: "app-1", ReadAt: "2026-09-30T10:03:00Z", Lines: many},
		"long line":     {SchemaVersion: "HostedErrorsV1", HostingKey: "app-1", ReadAt: "2026-09-30T10:03:00Z", Lines: []string{strings.Repeat("x", HostedErrorLineBytes+1)}},
		"two lines":     {SchemaVersion: "HostedErrorsV1", HostingKey: "app-1", ReadAt: "2026-09-30T10:03:00Z", Lines: []string{"Error: a\nsecond"}},
		"bad since":     {SchemaVersion: "HostedErrorsV1", HostingKey: "app-1", ReadAt: "2026-09-30T10:03:00Z", Since: "yesterday", Lines: []string{}},
		"less restarts": {SchemaVersion: "HostedErrorsV1", HostingKey: "app-1", ReadAt: "2026-09-30T10:03:00Z", Restarts: -1, Lines: []string{}},
	} {
		backend.errors = bad
		if w := get(s, "/v1/hosted/app-1/errors"); w.Code != 500 {
			t.Fatal(name, w.Code, w.Body.String())
		}
	}
	backend.err = ErrDependencyNotFound
	if w := get(s, "/v1/hosted/app-1/errors"); w.Code != 404 {
		t.Fatal("unknown key", w.Code)
	}
	s2, _ := hostingServer(t, false, true, PrincipalService)
	if w := get(s2, "/v1/hosted/app-1/errors"); w.Code != 403 {
		t.Fatal("without the hosting grant", w.Code)
	}
}

// H6 (Repo C design note H6_FIRST_ADMIN_ON_HOSTING.md): a hosted app's setup token is read, never written, through
// the hosting grant; one not made yet is setup_pending, and one of another shape or key is never served.
func TestTheHostedSetupTokenIsReadOnlyPendingUntilMadeAndNeverServedMalformed(t *testing.T) {
	s, backend := hostingServer(t, true, true, PrincipalService)
	if w := post(s, "/v1/hosted/app-1/setup", commandBody); w.Code != 400 || len(backend.calls) != 0 {
		t.Fatal("a write to the setup token", w.Code, backend.calls)
	}
	backend.err = ErrSetupPending
	if w := get(s, "/v1/hosted/app-1/setup"); w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"setup_pending"`) {
		t.Fatal("a pending token", w.Code, w.Body.String())
	}
	backend.err = nil
	for _, bad := range []HostedSetupV1{
		{SchemaVersion: "HostedSetupV1", HostingKey: "app-1", Token: "try-autobuild"},
		{SchemaVersion: "HostedSetupV1", HostingKey: "app-1", Token: strings.Repeat("A", 32)},
		{SchemaVersion: "HostedSetupV1", HostingKey: "app-2", Token: strings.Repeat("0a", 16)},
		{SchemaVersion: "SiteSetupV1", HostingKey: "app-1", Token: strings.Repeat("0a", 16)},
	} {
		backend.setup = bad
		if w := get(s, "/v1/hosted/app-1/setup"); w.Code != 500 || strings.Contains(w.Body.String(), bad.Token) {
			t.Fatal("a malformed token was served", bad, w.Code, w.Body.String())
		}
	}
	s2, _ := hostingServer(t, false, true, PrincipalService)
	if w := get(s2, "/v1/hosted/app-1/setup"); w.Code != 403 {
		t.Fatal("the token without the hosting grant", w.Code)
	}
}

// Repo C BF-10c: one backup's file, as it is, for the product to hand to the app's owner; read through the hosting grant.
func TestAHostedBackupFileIsServedAsItIs(t *testing.T) {
	s, backend := hostingServer(t, true, true, PrincipalService)
	backend.file = "backup-bytes"
	path := "/v1/hosted/app-1/backups/" + strings.Repeat("f", 64)
	w := get(s, path)
	if w.Code != 200 || w.Body.String() != "backup-bytes" || w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("Content-Length") != "12" ||
		w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || len(backend.calls) != 1 || backend.calls[0] != "backup-file" || backend.lastKey != "app-1" {
		t.Fatalf("file = %d %q %v calls %v", w.Code, w.Body.String(), w.Header(), backend.calls)
	}
	for _, bad := range []string{"/v1/hosted/app-1/backups/not-an-id", "/v1/hosted/app-1/backups/" + strings.Repeat("F", 64), path + "/more", "/v1/hosted/app-1/route/" + strings.Repeat("f", 64)} {
		backend.calls = nil
		if w := get(s, bad); w.Code != 400 || len(backend.calls) != 0 {
			t.Fatalf("%s: %d calls %v", bad, w.Code, backend.calls)
		}
	}
	backend.calls = nil
	if w := post(s, path, commandBody); w.Code != 400 || len(backend.calls) != 0 {
		t.Fatalf("a write to a backup file: %d calls %v", w.Code, backend.calls)
	}
	backend.err = ErrDependencyNotFound
	if w := get(s, path); w.Code != 404 {
		t.Fatal("an unknown backup file", w.Code)
	}
	backend.err, backend.file = nil, ""
	if w := get(s, path); w.Code != 500 {
		t.Fatal("an empty backup file was served", w.Code)
	}
	denied, deniedBackend := hostingServer(t, false, true, PrincipalService)
	if w := get(denied, path); w.Code != 403 || len(deniedBackend.calls) != 0 {
		t.Fatal("a backup file without the hosting grant", w.Code, deniedBackend.calls)
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

type testFeed struct{ after, limit []uint64 }

func (f *testFeed) Events(after uint64, limit int) AppHealthEventsV1 {
	f.after, f.limit = append(f.after, after), append(f.limit, uint64(limit))
	return AppHealthEventsV1{SchemaVersion: AppHealthEventsSchemaV1, LastSeq: 3, Events: []AppHealthEventV1{
		{Seq: 3, At: "2026-10-24T16:10:00Z", Kind: "hosted", HostingKey: "app-1", State: "down", Since: "2026-10-24T16:00:00Z"}}}
}

// BF-07e: the app health events are read with a cursor, by the principal that controls hosting only, never cached; a
// bad cursor, limit or parameter and any write are refused.
func TestAppHealthEventsAreReadAfterACursorByTheHostingPrincipal(t *testing.T) {
	s, _ := hostingServer(t, true, true, PrincipalService)
	feed := &testFeed{}
	s.reserved.AppHealth = feed
	w := get(s, "/v1/app-health/events?after=2&limit=10")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), `"state":"down"`) || strings.Contains(w.Body.String(), `"target"`) {
		t.Fatal("events", w.Code, w.Header(), w.Body.String())
	}
	if w := get(s, "/v1/app-health/events"); w.Code != 200 || feed.after[1] != 0 || feed.limit[1] != AppHealthEventLimit {
		t.Fatal("defaults", w.Code, feed.after, feed.limit)
	}
	if feed.after[0] != 2 || feed.limit[0] != 10 {
		t.Fatal("cursor", feed.after, feed.limit)
	}
	for _, bad := range []string{"?after=-1", "?after=x", "?limit=0", "?limit=101", "?other=1", "?after=1&after=2"} {
		if w := get(s, "/v1/app-health/events"+bad); w.Code != 400 {
			t.Fatal(bad, w.Code)
		}
	}
	if w := post(s, "/v1/app-health/events", "{}"); w.Code != 400 {
		t.Fatal("a write", w.Code)
	}
	s2, _ := hostingServer(t, false, true, PrincipalService)
	s2.reserved.AppHealth = feed
	if w := get(s2, "/v1/app-health/events"); w.Code != 403 {
		t.Fatal("without the hosting grant", w.Code)
	}
	s3, _ := hostingServer(t, true, true, PrincipalService)
	if w := get(s3, "/v1/app-health/events"); w.Code == 200 {
		t.Fatal("no watch, no events", w.Code)
	}
}

// BF-06a: a hosted backup's counts are whole or absent, bounded and in name order.
func TestAHostedBackupsCountsAreWholeAndBounded(t *testing.T) {
	two, five := 2, int64(5)
	b := HostedBackupV1{SchemaVersion: "HostedBackupV1", HostingKey: "app-1", BackupID: strings.Repeat("a", 64), Reason: "manual", TakenAt: "2026-10-24T17:00:00Z",
		Bytes: 10, Generation: 1, SourceSHA: strings.Repeat("b", 40), Tables: &two, Rows: &five,
		Counts: []TableRowsV1{{Table: "public.a", Rows: 2}, {Table: "public.b", Rows: 3}}}
	if err := ValidateHostedBackupV1(b, "app-1"); err != nil {
		t.Fatal(err)
	}
	none := b
	none.Tables, none.Rows, none.Counts = nil, nil, nil
	if err := ValidateHostedBackupV1(none, "app-1"); err != nil {
		t.Fatal("a backup without counts", err)
	}
	one := 1
	for name, bad := range map[string]func(*HostedBackupV1){
		"tables without rows":     func(v *HostedBackupV1) { v.Rows = nil },
		"counts without totals":   func(v *HostedBackupV1) { v.Tables, v.Rows = nil, nil },
		"fewer tables than named": func(v *HostedBackupV1) { v.Tables = &one },
		"out of order": func(v *HostedBackupV1) {
			v.Counts = []TableRowsV1{{Table: "public.b", Rows: 3}, {Table: "public.a", Rows: 2}}
		},
		"negative": func(v *HostedBackupV1) { v.Counts = []TableRowsV1{{Table: "public.a", Rows: -1}} },
		"unnamed":  func(v *HostedBackupV1) { v.Counts = []TableRowsV1{{Table: "", Rows: 1}} },
	} {
		v := b
		bad(&v)
		if ValidateHostedBackupV1(v, "app-1") == nil {
			t.Fatal("taken:", name)
		}
	}
	many := make([]TableRowsV1, BackupCountLimit+1)
	for i := range many {
		many[i] = TableRowsV1{Table: fmt.Sprintf("public.t%03d", i), Rows: 1}
	}
	if ValidateTableRows(many) == nil {
		t.Fatal("more than the limit")
	}
}

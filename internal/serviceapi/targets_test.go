package serviceapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

type testTargets struct {
	calls []string
	err   error
}

var sampleTarget = TargetV1{Key: "host-1", Address: "13.232.1.2", Port: 22, User: "autobuild",
	PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOk autobuild-host-1", State: "confirmed",
	HostKeyFingerprint: "SHA256:abc", CreatedAt: "2026-10-14T01:00:00Z",
	Preflight: &TargetPreflightV1{CheckedAt: "2026-10-14T01:01:00Z", Outcome: "checked", Ready: true,
		Facts:  &TargetFactsV1{OS: "Ubuntu 24.04.1 LTS", OSID: "ubuntu", OSVersion: "24.04", Arch: "x86_64", CPUs: 2, MemoryMB: 3833, DiskFreeMB: 43945, ContainerRuntime: "docker 27.3.1", Rootless: true, GVisor: true},
		Checks: []TargetCheckV1{{Name: "arch", OK: true, Required: true, Value: "x86_64"}}}}

func (t *testTargets) Targets(context.Context) (TargetsV1, error) {
	t.calls = append(t.calls, "list")
	return TargetsV1{SchemaVersion: TargetsSchemaV1, Targets: []TargetV1{sampleTarget}}, t.err
}
func (t *testTargets) Target(_ context.Context, key string) (TargetV1, error) {
	t.calls = append(t.calls, "get:"+key)
	return sampleTarget, t.err
}
func (t *testTargets) CreateTarget(_ context.Context, key string, c TargetCreateV1) (TargetV1, error) {
	t.calls = append(t.calls, "create:"+key+":"+c.Address)
	return sampleTarget, t.err
}
func (t *testTargets) ConfirmTarget(_ context.Context, key string, c TargetConfirmV1) (TargetV1, error) {
	t.calls = append(t.calls, "confirm:"+key+":"+c.Fingerprint)
	return sampleTarget, t.err
}
func (t *testTargets) PreflightTarget(_ context.Context, key string) (TargetV1, error) {
	t.calls = append(t.calls, "preflight:"+key)
	return sampleTarget, t.err
}
func (t *testTargets) DeleteTarget(_ context.Context, key string) error {
	t.calls = append(t.calls, "delete:"+key)
	return t.err
}

func targetServer(t *testing.T, grant bool) (*Server, *testTargets) {
	s := newTestServer(t, &testCatalog{}, time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC))
	p := Principal{PrincipalID: "repo-c-service", PrincipalType: PrincipalService, AuthnMethod: "test-v1"}
	s.authenticator = fixedAuthenticator{p}
	g := authorityGrant{authorities: map[string]struct{}{"hosting.control": {}}}
	if grant {
		g.authorities["targets.control"] = struct{}{}
	}
	s.authority.grants[p.PrincipalID] = g
	backend := &testTargets{}
	s.reserved.Targets = backend
	return s, backend
}

func TestTargetsAreRegisteredConfirmedCheckedAndRemovedWithTheirGrant(t *testing.T) {
	s, backend := targetServer(t, true)
	if r := call(s, http.MethodGet, "/v1/targets", ""); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"schema_version":"TargetsV1"`) || !strings.Contains(r.Body.String(), `"memory_mb":3833`) {
		t.Fatalf("list = %d %s", r.Code, r.Body.String())
	}
	if r := call(s, http.MethodPut, "/v1/targets/host-1", `{"address":"13.232.1.2"}`); r.Code != http.StatusCreated || !strings.Contains(r.Body.String(), `"public_key":"ssh-ed25519 `) {
		t.Fatalf("create = %d %s", r.Code, r.Body.String())
	}
	if r := call(s, http.MethodGet, "/v1/targets/host-1", ""); r.Code != http.StatusOK {
		t.Fatalf("get = %d", r.Code)
	}
	if r := call(s, http.MethodPost, "/v1/targets/host-1/preflight", ""); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"outcome":"checked"`) {
		t.Fatalf("preflight = %d %s", r.Code, r.Body.String())
	}
	if r := call(s, http.MethodPost, "/v1/targets/host-1/confirm", `{"fingerprint":"SHA256:abc"}`); r.Code != http.StatusOK {
		t.Fatalf("confirm = %d", r.Code)
	}
	if r := call(s, http.MethodDelete, "/v1/targets/host-1", ""); r.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", r.Code)
	}
	want := []string{"list", "create:host-1:13.232.1.2", "get:host-1", "preflight:host-1", "confirm:host-1:SHA256:abc", "delete:host-1"}
	if strings.Join(backend.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v", backend.calls)
	}
	for _, bad := range []struct{ method, path, body string }{
		{http.MethodPut, "/v1/targets/host-1", ""},
		{http.MethodPut, "/v1/targets/host-1", `{"address":"13.232.1.2","key":"x"}`},
		{http.MethodPut, "/v1/targets/host-1", `{"address":5}`},
		{http.MethodPut, "/v1/targets/Host_1", `{"address":"13.232.1.2"}`},
		{http.MethodPut, "/v1/targets/host-1", `{"address":"` + strings.Repeat("a", 5000) + `"}`},
		{http.MethodPost, "/v1/targets/host-1/confirm", ""},
		{http.MethodPost, "/v1/targets/host-1/preflight", `{"x":1}`},
		{http.MethodPost, "/v1/targets/host-1/reboot", ""},
		{http.MethodPost, "/v1/targets/host-1/preflight/again", ""},
		{http.MethodPost, "/v1/targets", `{}`},
		{http.MethodDelete, "/v1/targets", ""},
		{http.MethodGet, "/v1/targets?all=1", ""},
		{http.MethodGet, "/v1/targets/", ""},
	} {
		if r := call(s, bad.method, bad.path, bad.body); r.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d", bad.method, bad.path, r.Code)
		}
	}
}

func TestTargetsNeedTheirGrantAndMapErrorsToStableCodes(t *testing.T) {
	s, backend := targetServer(t, false)
	if r := call(s, http.MethodGet, "/v1/targets", ""); r.Code != http.StatusForbidden || len(backend.calls) != 0 {
		t.Fatalf("without targets.control = %d %v", r.Code, backend.calls)
	}
	s, backend = targetServer(t, true)
	for err, want := range map[error]struct {
		status int
		code   string
	}{
		ErrTargetUnknown:             {http.StatusNotFound, "target_unknown"},
		ErrTargetInvalid:             {http.StatusBadRequest, "invalid_request"},
		ErrTargetAddressRefused:      {http.StatusBadRequest, "target_address_refused"},
		ErrTargetExists:              {http.StatusConflict, "target_exists"},
		ErrTargetFingerprintMismatch: {http.StatusConflict, "target_fingerprint_mismatch"},
		ErrTargetLimit:               {http.StatusConflict, "target_limit"},
		ErrTargetUnavailable:         {http.StatusServiceUnavailable, "target_unavailable"},
	} {
		backend.err = err
		r := call(s, http.MethodPut, "/v1/targets/host-1", `{"address":"13.232.1.2"}`)
		if r.Code != want.status || !strings.Contains(r.Body.String(), `"code":"`+want.code+`"`) {
			t.Errorf("%v = %d %s", err, r.Code, r.Body.String())
		}
	}
	s.reserved.Targets = nil
	if r := call(s, http.MethodGet, "/v1/targets", ""); r.Code != http.StatusNotImplemented {
		t.Fatalf("without targets = %d", r.Code)
	}
}

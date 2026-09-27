package serviceapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func previewRouteDTO() PreviewRouteV1 {
	return PreviewRouteV1{SchemaVersion: "PreviewRouteV1", RunID: "run-1", PreviewID: strings.Repeat("a", 64), RouteHandle: strings.Repeat("b", 64), ExpiresAt: "2026-09-26T10:01:00Z", TargetURL: "http://127.0.0.1:23456"}
}

func TestPreviewRouteDTOValidationAndEncoding(t *testing.T) {
	v := previewRouteDTO()
	if err := ValidatePreviewRouteV1(v); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(v)
	want := `{"schema_version":"PreviewRouteV1","run_id":"run-1","preview_id":"` + v.PreviewID + `","route_handle":"` + v.RouteHandle + `","expires_at":"2026-09-26T10:01:00Z","target_url":"http://127.0.0.1:23456"}`
	if err != nil || string(data) != want {
		t.Fatalf("route wire contract: %s, %v", data, err)
	}
	for _, change := range []func(*PreviewRouteV1){
		func(v *PreviewRouteV1) { v.SchemaVersion = "PreviewV1" },
		func(v *PreviewRouteV1) { v.RunID = "../private" },
		func(v *PreviewRouteV1) { v.PreviewID = strings.Repeat("A", 64) },
		func(v *PreviewRouteV1) { v.RouteHandle = "/private/container" },
		func(v *PreviewRouteV1) { v.ExpiresAt = "tomorrow" },
		func(v *PreviewRouteV1) { v.TargetURL = "" },
	} {
		bad := v
		change(&bad)
		if ValidatePreviewRouteV1(bad) == nil {
			t.Fatal("invalid route accepted", bad)
		}
	}
	for _, target := range []string{"http://127.0.0.1:1", "http://127.0.0.2:65535", "http://[::1]:23456"} {
		v.TargetURL = target
		if err := ValidatePreviewRouteV1(v); err != nil {
			t.Fatal(target, err)
		}
	}
	for _, target := range []string{
		"", "http://localhost:80", "http://example.com:80", "http://0.0.0.0:80", "http://10.88.0.2:8080", "http://[::]:80", "http://[fe80::1%eth0]:80", "http://[::1%eth0]:80",
		"https://127.0.0.1:80", "HTTP://127.0.0.1:80", "//127.0.0.1:80", "http://user:secret@127.0.0.1:80",
		"http://127.0.0.1", "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:+80", "http://127.0.0.1:080", "http://127.0.0.1:80/", "http://127.0.0.1:80/private", "http://127.0.0.1:80?", "http://127.0.0.1:80#",
		"http://127.1:80", "http://2130706433:80", "http://127.0.0.1:80\\@example.com", "http://127.0.0.1:80\n", "file:///private/source",
	} {
		v.TargetURL = target
		if ValidatePreviewRouteV1(v) == nil {
			t.Fatal("unsafe target accepted", target)
		}
	}
}

func TestPreviewRouteEndpointAuthorityAndExactResponse(t *testing.T) {
	for _, tc := range []struct {
		name            string
		kind            PrincipalType
		grant, delegate bool
		status          int
	}{
		{"service", PrincipalService, true, true, 200},
		{"service-without-delegation", PrincipalService, true, false, 200},
		{"unrelated-grant", PrincipalService, false, true, 403},
		{"user", PrincipalUser, true, true, 403},
		{"operator", PrincipalOperator, true, true, 403},
		{"test", PrincipalTest, true, true, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t, &testCatalog{}, time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC))
			p := Principal{PrincipalID: "gateway", PrincipalType: tc.kind, AuthnMethod: "test-v1"}
			s.authenticator = fixedAuthenticator{p}
			grant := authorityGrant{authorities: map[string]struct{}{"preview.other": {}}, mayDelegate: tc.delegate}
			if tc.grant {
				grant.authorities["preview.control"] = struct{}{}
			}
			s.authority.grants[p.PrincipalID] = grant
			backend := &testPreviewController{route: previewRouteDTO()}
			s.reserved.Preview = backend
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, authenticatedRequest(http.MethodGet, "/v1/runs/run-1/previews/"+backend.route.PreviewID+"/route", nil))
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.status != 200 {
				if backend.calls != 0 || strings.Contains(w.Body.String(), backend.route.TargetURL) {
					t.Fatal("authority denial leaked route")
				}
				return
			}
			var got PreviewRouteV1
			if json.Unmarshal(w.Body.Bytes(), &got) != nil || got != backend.route || backend.calls != 1 || backend.principal != p || backend.routeRun != got.RunID || backend.routeID != got.PreviewID {
				t.Fatal("route or authenticated context changed", w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("server-only response headers", w.Header())
			}
		})
	}
}

func TestPreviewRouteEndpointRejectsMalformedRequestsAndDependencies(t *testing.T) {
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	s := newTestServer(t, &testCatalog{}, now)
	grantPreviewControl(s)
	backend := &testPreviewController{route: previewRouteDTO()}
	s.reserved.Preview = backend
	path := "/v1/runs/run-1/previews/" + backend.route.PreviewID + "/route"
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodPost, path, `{}`, 400}, {http.MethodDelete, path, "", 400}, {http.MethodHead, path, "", 400},
		{http.MethodGet, path, `{}`, 400}, {http.MethodGet, path + "?route_handle=" + backend.route.RouteHandle, "", 400},
		{http.MethodGet, strings.Replace(path, backend.route.PreviewID, "bad", 1), "", 400},
		{http.MethodGet, path + "/extra", "", 404},
	} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, authenticatedRequest(tc.method, tc.path, bytes.NewReader([]byte(tc.body))))
		if w.Code != tc.status || backend.calls != 0 {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != 401 || backend.calls != 0 {
		t.Fatal("unauthenticated route lookup", w.Code)
	}
	for _, tc := range []struct {
		name   string
		change func()
		status int
	}{
		{"wrong-run", func() { backend.route.RunID = "run-2" }, 500},
		{"wrong-preview", func() { backend.route.PreviewID = strings.Repeat("c", 64) }, 500},
		{"bad-handle", func() { backend.route.RouteHandle = "/private/secret" }, 500},
		{"non-loopback", func() { backend.route.TargetURL = "http://10.88.0.2:8080" }, 500},
		{"expired", func() { backend.route.ExpiresAt = now.Format(time.RFC3339Nano) }, 503},
		{"unknown-or-unowned", func() { backend.err = ErrDependencyNotFound }, 404},
		{"unavailable", func() { backend.err = ErrPreviewUnavailable }, 503},
		{"private-error", func() { backend.err = errors.New("/private/secret") }, 500},
		{"missing-runtime", func() { s.reserved.Preview = nil }, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend.route, backend.err = previewRouteDTO(), nil
			s.reserved.Preview = backend
			tc.change()
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, authenticatedRequest(http.MethodGet, path, nil))
			if w.Code != tc.status || strings.Contains(w.Body.String(), "target_url") || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "10.88.") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

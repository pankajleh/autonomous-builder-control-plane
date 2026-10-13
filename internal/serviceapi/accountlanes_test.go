package serviceapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testAccountLanes struct {
	signIns  []string
	signOuts []string
	err      error
}

func (t *testAccountLanes) AccountLanes(context.Context) (AccountLanesV1, error) {
	return AccountLanesV1{SchemaVersion: AccountLanesSchemaV1, Lanes: []AccountLaneV1{
		{Lane: "codex-account", Executor: "codex", State: "signing_in", Device: &AccountLaneDeviceV1{VerificationURL: "https://auth.openai.com/codex/device", UserCode: "U2UU-AQ0TL", ExpiresAt: "2026-10-13T07:15:00Z"}},
		{Lane: "claude-account", Executor: "claude", State: "signed_in", Since: "2026-10-13T07:00:00Z"},
	}}, t.err
}

func (t *testAccountLanes) SignIn(_ context.Context, lane string, request AccountLaneSignInV1) (AccountLaneV1, error) {
	t.signIns = append(t.signIns, lane+":"+request.Token)
	return AccountLaneV1{Lane: lane, Executor: "claude", State: "signed_in", Since: "2026-10-13T07:00:00Z"}, t.err
}

func (t *testAccountLanes) SignOut(_ context.Context, lane string) (AccountLaneV1, error) {
	t.signOuts = append(t.signOuts, lane)
	return AccountLaneV1{Lane: lane, Executor: "claude", State: "signed_out"}, t.err
}

func accountServer(t *testing.T, grant bool) (*Server, *testAccountLanes) {
	s := newTestServer(t, &testCatalog{}, time.Date(2026, 10, 13, 7, 0, 0, 0, time.UTC))
	p := Principal{PrincipalID: "repo-c-service", PrincipalType: PrincipalService, AuthnMethod: "test-v1"}
	s.authenticator = fixedAuthenticator{p}
	g := authorityGrant{authorities: map[string]struct{}{"hosting.control": {}}}
	if grant {
		g.authorities["accounts.control"] = struct{}{}
	}
	s.authority.grants[p.PrincipalID] = g
	backend := &testAccountLanes{}
	s.reserved.AccountLanes = backend
	return s, backend
}

func call(s *Server, method, path, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	var reader *bytes.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	s.Handler().ServeHTTP(response, authenticatedRequest(method, path, reader))
	return response
}

func TestAccountLanesAreListedSignedInAndOutWithTheirGrant(t *testing.T) {
	s, backend := accountServer(t, true)
	list := call(s, http.MethodGet, "/v1/account-lanes", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"user_code":"U2UU-AQ0TL"`) || !strings.Contains(list.Body.String(), `"state":"signed_in"`) {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	token := "sk-ant-oat01-abcdefghijklmnopqrstuvwxyz0123456789"
	signIn := call(s, http.MethodPost, "/v1/account-lanes/claude-account/sign-in", `{"token":"`+token+`"}`)
	if signIn.Code != http.StatusOK || strings.Contains(signIn.Body.String(), token) || backend.signIns[0] != "claude-account:"+token {
		t.Fatalf("sign in = %d %s %v", signIn.Code, signIn.Body.String(), backend.signIns)
	}
	if codex := call(s, http.MethodPost, "/v1/account-lanes/codex-account/sign-in", `{}`); codex.Code != http.StatusOK || backend.signIns[1] != "codex-account:" {
		t.Fatalf("device sign in = %d %v", codex.Code, backend.signIns)
	}
	if out := call(s, http.MethodDelete, "/v1/account-lanes/claude-account", ""); out.Code != http.StatusOK || backend.signOuts[0] != "claude-account" {
		t.Fatalf("sign out = %d %v", out.Code, backend.signOuts)
	}
	for _, bad := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/account-lanes/claude-account/sign-in", ""},
		{http.MethodPost, "/v1/account-lanes/claude-account/sign-in", `{"token":"x","lane":"other"}`},
		{http.MethodPost, "/v1/account-lanes/claude-account/sign-in", `{"token":1}`},
		{http.MethodPost, "/v1/account-lanes/claude-account/sign-in", `{"token":"` + strings.Repeat("a", 5000) + `"}`},
		{http.MethodPost, "/v1/account-lanes/Claude_Account/sign-in", `{}`},
		{http.MethodPost, "/v1/account-lanes/claude-account", `{}`},
		{http.MethodGet, "/v1/account-lanes/claude-account", ""},
		{http.MethodDelete, "/v1/account-lanes", ""},
		{http.MethodDelete, "/v1/account-lanes/a/b", ""},
		{http.MethodGet, "/v1/account-lanes?all=1", ""},
	} {
		if response := call(s, bad.method, bad.path, bad.body); response.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d", bad.method, bad.path, response.Code)
		}
	}
	if len(backend.signIns) != 2 || len(backend.signOuts) != 1 {
		t.Fatalf("nothing refused reached the lanes: %v %v", backend.signIns, backend.signOuts)
	}
	for err, want := range map[error]string{
		ErrAccountLaneUnknown: "account_lane_unknown", ErrAccountLaneInvalid: "invalid_request",
		ErrAccountLaneSignedIn: "account_lane_signed_in", ErrAccountLaneUnavailable: "account_lane_unavailable",
	} {
		backend.err = err
		response := call(s, http.MethodPost, "/v1/account-lanes/codex-account/sign-in", `{}`)
		if !strings.Contains(response.Body.String(), `"code":"`+want+`"`) {
			t.Errorf("%v = %d %s", err, response.Code, response.Body.String())
		}
	}
}

func TestAccountLanesNeedTheirGrantAndTheirFile(t *testing.T) {
	s, backend := accountServer(t, false)
	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/account-lanes", ""},
		{http.MethodPost, "/v1/account-lanes/codex-account/sign-in", `{}`},
		{http.MethodDelete, "/v1/account-lanes/codex-account", ""},
	} {
		if response := call(s, request.method, request.path, request.body); response.Code != http.StatusForbidden {
			t.Errorf("ungranted %s %s = %d", request.method, request.path, response.Code)
		}
	}
	if len(backend.signIns)+len(backend.signOuts) != 0 {
		t.Fatal("an ungranted call reaches nothing")
	}
	s.reserved.AccountLanes = nil
	if response := call(s, http.MethodGet, "/v1/account-lanes", ""); response.Code != http.StatusNotImplemented {
		t.Fatalf("without the file = %d", response.Code)
	}
}

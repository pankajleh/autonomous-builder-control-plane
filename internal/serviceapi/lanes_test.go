package serviceapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testLaneKey = "tgp_v1_Zk9uZWtleWZvcnRoZXRlc3Rpbmdvbmx5LTAxMjM0NTY3"

type testLaneKeys struct {
	sets    []string
	removes []string
	err     error
}

func (t *testLaneKeys) Lanes(context.Context) (LanesV1, error) {
	return LanesV1{SchemaVersion: LanesSchemaV1, Lanes: []LaneV1{
		{Lane: "groq", Kind: LaneKindKey, State: LaneKeyMissing, Profiles: []string{"local-p02-groq"}},
		{Lane: "together", Kind: LaneKindKey, State: LaneKeySet, Fingerprint: "1a2b3c4d", Profiles: []string{"local-p02-together"}},
	}}, t.err
}

func (t *testLaneKeys) SetKey(_ context.Context, lane string, request LaneKeyRequestV1) (LaneV1, error) {
	t.sets = append(t.sets, lane+":"+request.Key)
	return LaneV1{Lane: lane, Kind: LaneKindKey, State: LaneKeySet, Fingerprint: "1a2b3c4d", Profiles: []string{"local-p02-together"}}, t.err
}

func (t *testLaneKeys) RemoveKey(_ context.Context, lane string) (LaneV1, error) {
	t.removes = append(t.removes, lane)
	return LaneV1{Lane: lane, Kind: LaneKindKey, State: LaneKeyMissing, Profiles: []string{"local-p02-together"}}, t.err
}

func laneServer(t *testing.T, grant bool) (*Server, *testLaneKeys, *bytes.Buffer) {
	t.Helper()
	s := newTestServer(t, &testCatalog{}, time.Date(2026, 11, 7, 18, 0, 0, 0, time.UTC))
	p := Principal{PrincipalID: "repo-c-service", PrincipalType: PrincipalService, AuthnMethod: "test-v1"}
	s.authenticator = fixedAuthenticator{p}
	g := authorityGrant{authorities: map[string]struct{}{"accounts.control": {}, "hosting.control": {}}}
	if grant {
		g.authorities["lanes.control"] = struct{}{}
	}
	s.authority.grants[p.PrincipalID] = g
	backend := &testLaneKeys{}
	s.reserved.LaneKeys = backend
	log := &bytes.Buffer{}
	s.reserved.RequestLog = log
	return s, backend, log
}

func TestLaneKeysAreListedSetAndRemovedWithTheirGrant(t *testing.T) {
	s, backend, log := laneServer(t, true)
	list := call(s, http.MethodGet, "/v1/lanes", "")
	if list.Code != http.StatusOK || list.Body.String() != `{"schema_version":"LanesV1","lanes":[`+
		`{"lane":"groq","kind":"key","state":"missing","profiles":["local-p02-groq"]},`+
		`{"lane":"together","kind":"key","state":"set","fingerprint":"1a2b3c4d","profiles":["local-p02-together"]}]}`+"\n" {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	set := call(s, http.MethodPut, "/v1/lanes/together/key", `{"key":"`+testLaneKey+`"}`)
	if set.Code != http.StatusOK || set.Body.String() != `{"lane":"together","kind":"key","state":"set","fingerprint":"1a2b3c4d","profiles":["local-p02-together"]}`+"\n" ||
		len(backend.sets) != 1 || backend.sets[0] != "together:"+testLaneKey {
		t.Fatalf("set = %d %s %v", set.Code, set.Body.String(), backend.sets)
	}
	removed := call(s, http.MethodDelete, "/v1/lanes/together/key", "")
	if removed.Code != http.StatusOK || !strings.Contains(removed.Body.String(), `"state":"missing"`) || len(backend.removes) != 1 || backend.removes[0] != "together" {
		t.Fatalf("remove = %d %s %v", removed.Code, removed.Body.String(), backend.removes)
	}
	if strings.Contains(log.String(), testLaneKey) || strings.Count(log.String(), "\n") != 3 || !strings.Contains(log.String(), "method=PUT path=/v1/lanes/together/key status=200") {
		t.Fatalf("the request log holds the key, or not one line a request:\n%s", log.String())
	}
	if text := fmt.Sprintf("%v %+v %#v %s", LaneKeyRequestV1{Key: testLaneKey}, LaneKeyRequestV1{Key: testLaneKey}, LaneKeyRequestV1{Key: testLaneKey}, LaneKeyRequestV1{Key: testLaneKey}); strings.Contains(text, testLaneKey) {
		t.Fatalf("a request prints its key: %s", text)
	}
}

func TestLaneKeyRequestsOfAnyOtherShapeAreRefusedBeforeTheLanes(t *testing.T) {
	s, backend, _ := laneServer(t, true)
	good := `{"key":"` + testLaneKey + `"}`
	for _, bad := range []struct{ method, path, body string }{
		{http.MethodPut, "/v1/lanes/together/key", ""},
		{http.MethodPut, "/v1/lanes/together/key", `{}x`},
		{http.MethodPut, "/v1/lanes/together/key", `{"key":"` + testLaneKey + `","lane":"groq"}`},
		{http.MethodPut, "/v1/lanes/together/key", `{"key":1}`},
		{http.MethodPut, "/v1/lanes/together/key", `{"key":"a","key":"b"}`},
		{http.MethodPut, "/v1/lanes/together/key", `{"Key":"` + testLaneKey + `"}`},
		{http.MethodPut, "/v1/lanes/together/key", "{\"key\":\"\xff\"}"},
		{http.MethodPut, "/v1/lanes/Together/key", good},
		{http.MethodPut, "/v1/lanes/together_ai/key", good},
		{http.MethodPut, "/v1/lanes/together", good},
		{http.MethodPut, "/v1/lanes/together/keys", good},
		{http.MethodPut, "/v1/lanes/together/key/", good},
		{http.MethodPut, "/v1/lanes//key", good},
		{http.MethodPut, "/v1/lanes/a/b/key", good},
		{http.MethodPut, "/v1/lanes/together/key?x=1", good},
		{http.MethodPut, "/v1/lanes", good},
		{http.MethodPost, "/v1/lanes/together/key", good},
		{http.MethodPatch, "/v1/lanes/together/key", good},
		{http.MethodGet, "/v1/lanes/together/key", ""},
		{http.MethodGet, "/v1/lanes?all=1", ""},
		{http.MethodGet, "/v1/lanes", good},
		{http.MethodDelete, "/v1/lanes/together/key", good},
		{http.MethodDelete, "/v1/lanes", ""},
	} {
		if response := call(s, bad.method, bad.path, bad.body); response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) ||
			strings.Contains(response.Body.String(), testLaneKey) {
			t.Errorf("%s %s %.40q = %d %s", bad.method, bad.path, bad.body, response.Code, response.Body.String())
		}
	}
	escaped := httptest.NewRecorder()
	s.Handler().ServeHTTP(escaped, authenticatedRequest(http.MethodPut, "/v1/lanes/to%67ether/key", bytes.NewReader([]byte(good))))
	if escaped.Code != http.StatusBadRequest {
		t.Errorf("an escaped lane = %d", escaped.Code)
	}
	// The body is bounded: past 4 KiB it is refused whether or not it says its length.
	large := `{"key":"` + strings.Repeat("a", MaxLaneKeyBody) + `"}`
	if response := call(s, http.MethodPut, "/v1/lanes/together/key", large); response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), `"code":"request_too_large"`) {
		t.Errorf("a large body = %d %s", response.Code, response.Body.String())
	}
	chunked := httptest.NewRecorder()
	request := authenticatedRequest(http.MethodPut, "/v1/lanes/together/key", bytes.NewReader([]byte(large)))
	request.ContentLength = -1
	s.Handler().ServeHTTP(chunked, request)
	if chunked.Code != http.StatusRequestEntityTooLarge || !strings.Contains(chunked.Body.String(), `"code":"request_too_large"`) {
		t.Errorf("a large body of no stated length = %d %s", chunked.Code, chunked.Body.String())
	}
	if len(backend.sets) != 0 || len(backend.removes) != 0 {
		t.Fatalf("a refused request reached the lanes: %v %v", backend.sets, backend.removes)
	}
	// Exactly 4 KiB is still read.
	exact := `{"key":"` + strings.Repeat("a", MaxLaneKeyBody-len(`{"key":""}`)) + `"}`
	if len(exact) != MaxLaneKeyBody {
		t.Fatalf("the body is %d bytes", len(exact))
	}
	if response := call(s, http.MethodPut, "/v1/lanes/together/key", exact); response.Code != http.StatusOK || len(backend.sets) != 1 {
		t.Fatalf("a body of 4 KiB = %d %s", response.Code, response.Body.String())
	}
}

func TestLaneKeyErrorsAnswerStableCodesWithoutTheKey(t *testing.T) {
	s, backend, _ := laneServer(t, true)
	for err, want := range map[error]struct {
		status int
		code   string
	}{
		ErrLaneUnknown: {http.StatusNotFound, "lane_unknown"},
		ErrLaneNotKey:  {http.StatusConflict, "lane_not_key"},
		fmt.Errorf("%w: a groq key is …", ErrLaneKeyInvalid):  {http.StatusBadRequest, "lane_key_invalid"},
		fmt.Errorf("%w: "+testLaneKey, ErrLaneKeyUnavailable): {http.StatusServiceUnavailable, "lane_key_unavailable"},
		context.DeadlineExceeded:                              {http.StatusServiceUnavailable, "lane_key_unavailable"},
	} {
		backend.err = err
		for _, request := range []struct{ method, path, body string }{
			{http.MethodPut, "/v1/lanes/together/key", `{"key":"` + testLaneKey + `"}`},
			{http.MethodDelete, "/v1/lanes/together/key", ""},
			{http.MethodGet, "/v1/lanes", ""},
		} {
			response := call(s, request.method, request.path, request.body)
			if response.Code != want.status || !strings.Contains(response.Body.String(), `"code":"`+want.code+`"`) || strings.Contains(response.Body.String(), testLaneKey) ||
				strings.Contains(response.Body.String(), "groq key") {
				t.Errorf("%v on %s %s = %d %s", err, request.method, request.path, response.Code, response.Body.String())
			}
		}
	}
}

func TestLaneKeysNeedTheirGrantAndTheirLanes(t *testing.T) {
	s, backend, _ := laneServer(t, false)
	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/lanes", ""},
		{http.MethodPut, "/v1/lanes/together/key", `{"key":"` + testLaneKey + `"}`},
		{http.MethodDelete, "/v1/lanes/together/key", ""},
	} {
		if response := call(s, request.method, request.path, request.body); response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"authority_denied"`) {
			t.Errorf("ungranted %s %s = %d %s", request.method, request.path, response.Code, response.Body.String())
		}
	}
	// A principal that is not a service is refused even with the grant.
	s, backend, _ = laneServer(t, true)
	user := Principal{PrincipalID: "repo-c-service", PrincipalType: PrincipalUser, AuthnMethod: "test-v1"}
	s.authenticator = fixedAuthenticator{user}
	if response := call(s, http.MethodPut, "/v1/lanes/together/key", `{"key":"`+testLaneKey+`"}`); response.Code != http.StatusForbidden {
		t.Errorf("a user principal = %d %s", response.Code, response.Body.String())
	}
	// Anonymous and wrong credentials are refused before anything else.
	s = newTestServer(t, &testCatalog{}, time.Now())
	s.reserved.LaneKeys = backend
	for _, path := range []string{"/v1/lanes", "/v1/lanes/together/key"} {
		anonymous := httptest.NewRecorder()
		s.Handler().ServeHTTP(anonymous, httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"key":"`+testLaneKey+`"}`)))
		if anonymous.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s = %d", path, anonymous.Code)
		}
	}
	if len(backend.sets) != 0 || len(backend.removes) != 0 {
		t.Fatalf("a refused request reached the lanes: %v %v", backend.sets, backend.removes)
	}
	// Without lanes the routes say so.
	s, _, _ = laneServer(t, true)
	s.reserved.LaneKeys = nil
	if response := call(s, http.MethodGet, "/v1/lanes", ""); response.Code != http.StatusNotImplemented || !strings.Contains(response.Body.String(), `"code":"unsupported_capability"`) {
		t.Errorf("without lanes = %d %s", response.Code, response.Body.String())
	}
}

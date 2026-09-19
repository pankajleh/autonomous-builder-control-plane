package serviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

type testFutureDependencies struct {
	mu           sync.Mutex
	err          error
	eventsPage   PageRequestV1
	timelinePage PageRequestV1
	evidencePage PageRequestV1
	actionKinds  []ActionKind
}

type testRunAdmissionController struct {
	mu        sync.Mutex
	principal Principal
	request   RunAdmissionRequestV1
	response  RunAdmissionResponseV1
	err       error
	calls     int
}

func (c *testRunAdmissionController) AdmitRun(_ context.Context, principal Principal, request RunAdmissionRequestV1) (RunAdmissionResponseV1, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.principal = principal
	c.request = request
	return c.response, c.err
}

type heldResponseWriter struct {
	header  http.Header
	entered chan<- struct{}
	release <-chan struct{}
	status  int
	body    bytes.Buffer
}

func (w *heldResponseWriter) Header() http.Header { return w.header }

func (w *heldResponseWriter) WriteHeader(status int) { w.status = status }

func (w *heldResponseWriter) Write(data []byte) (int, error) {
	w.entered <- struct{}{}
	<-w.release
	return w.body.Write(data)
}

type observedCancelContext struct {
	context.Context
	done     chan struct{}
	observed chan struct{}
	once     sync.Once
}

func (c *observedCancelContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.done
}

func (c *observedCancelContext) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
		return nil
	}
}

type trackedEvidenceReader struct {
	lists    chan<- struct{}
	reads    chan<- struct{}
	listData json.RawMessage
	data     []byte
	err      error
}

func (r *trackedEvidenceReader) ListEvidence(context.Context, string, PageRequestV1) (json.RawMessage, error) {
	if r.lists != nil {
		r.lists <- struct{}{}
	}
	if r.listData != nil {
		return r.listData, r.err
	}
	return json.RawMessage(`{"kind":"evidence"}`), r.err
}

func (r *trackedEvidenceReader) ReadEvidence(context.Context, string, string) ([]byte, error) {
	r.reads <- struct{}{}
	return r.data, r.err
}

func (d *testFutureDependencies) ReadRunProjection(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"kind":"run"}`), d.err
}

func (d *testFutureDependencies) ReadEventProjection(_ context.Context, _ string, page PageRequestV1) (json.RawMessage, error) {
	d.mu.Lock()
	d.eventsPage = page
	d.mu.Unlock()
	return json.RawMessage(`{"kind":"events"}`), d.err
}

func (d *testFutureDependencies) ReadTimeline(_ context.Context, _ string, page PageRequestV1) (json.RawMessage, error) {
	d.mu.Lock()
	d.timelinePage = page
	d.mu.Unlock()
	return json.RawMessage(`{"kind":"timeline"}`), d.err
}

func (d *testFutureDependencies) ListEvidence(_ context.Context, _ string, page PageRequestV1) (json.RawMessage, error) {
	d.mu.Lock()
	d.evidencePage = page
	d.mu.Unlock()
	return json.RawMessage(`{"kind":"evidence"}`), d.err
}

func (d *testFutureDependencies) ReadEvidence(context.Context, string, string) ([]byte, error) {
	return []byte("verified evidence"), d.err
}

func (d *testFutureDependencies) AdmitAction(_ context.Context, _ Principal, runID string, kind ActionKind, _ CommandEnvelopeV1) (ActionStatusV1, error) {
	d.mu.Lock()
	d.actionKinds = append(d.actionKinds, kind)
	d.mu.Unlock()
	operationID := "op-" + string(kind)
	return ActionStatusV1{OperationID: operationID, Status: "RECEIVED", StatusURL: "/v1/runs/" + runID + "/actions/" + operationID}, d.err
}

func (d *testFutureDependencies) ReadAction(_ context.Context, _ Principal, runID, operationID string) (ActionStatusV1, error) {
	return ActionStatusV1{OperationID: operationID, Status: "APPLIED", StatusURL: "/v1/runs/" + runID + "/actions/" + operationID, AuthoritativeEventIDs: []string{"event-1"}}, d.err
}

type testAuthenticator struct{}

func (testAuthenticator) Authenticate(request *http.Request) (Principal, error) {
	if request.Header.Get("Authorization") != "Bearer valid" {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{PrincipalID: "test", PrincipalType: PrincipalTest, AuthnMethod: "test-v1"}, nil
}

type testCatalog struct {
	mu      sync.Mutex
	runs    []runtimecatalog.RunRegistrationV1
	entered chan struct{}
	release chan struct{}
	readErr error
}

func (c *testCatalog) ListRuns(after string, limit int) ([]runtimecatalog.RunRegistrationV1, bool, error) {
	if c.entered != nil {
		c.entered <- struct{}{}
		<-c.release
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	runs := append([]runtimecatalog.RunRegistrationV1(nil), c.runs...)
	sort.Slice(runs, func(i, j int) bool { return runs[i].RunID < runs[j].RunID })
	page := make([]runtimecatalog.RunRegistrationV1, 0, limit)
	more := false
	for _, run := range runs {
		if run.RunID <= after {
			continue
		}
		if len(page) == limit {
			more = true
			continue
		}
		page = append(page, run)
	}
	return page, more, nil
}

func (c *testCatalog) ReadRun(string) (runtimecatalog.RunRegistrationV1, error) {
	return runtimecatalog.RunRegistrationV1{}, c.readErr
}

func newTestServer(t *testing.T, catalog CatalogReader, now time.Time) *Server {
	t.Helper()
	signer, err := NewCursorSignerWithClock("key-v1", make([]byte, 32), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{
		Authenticator: testAuthenticator{}, Authority: &AuthorityMatcher{grants: map[string]authorityGrant{}, digest: strings.Repeat("a", 64)},
		Catalog: catalog, CursorSigner: signer, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func authenticatedRequest(method, target string, body *bytes.Reader) *http.Request {
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(method, target, nil)
	} else {
		request = httptest.NewRequest(method, target, body)
	}
	request.Header.Set("Authorization", "Bearer valid")
	return request
}

func TestEveryV1RouteAuthenticatesAndCapabilitiesAreDeterministic(t *testing.T) {
	now := time.Now().UTC()
	server := newTestServer(t, &testCatalog{}, now)
	for _, path := range []string{
		"/v1", "/v1/capabilities", "/v1/runs", "/v1/runs/run", "/v1/runs/run/events",
		"/v1/runs/run/timeline", "/v1/runs/run/evidence", "/v1/runs/run/evidence/item",
		"/v1/runs/run/actions/cancel", "/v1/runs/run/actions/operation", "/v1/unknown",
	} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s = %d", path, response.Code)
		}
	}
	first := httptest.NewRecorder()
	server.Handler().ServeHTTP(first, authenticatedRequest(http.MethodGet, "/v1/capabilities", nil))
	second := httptest.NewRecorder()
	server.Handler().ServeHTTP(second, authenticatedRequest(http.MethodGet, "/v1/capabilities", nil))
	if first.Code != http.StatusOK || first.Body.String() != second.Body.String() || first.Body.String() != "{\"run_admission\":false,\"retry\":false,\"resume\":false,\"recovery\":false,\"cancel\":false,\"decision\":false,\"evidence_download\":false}\n" {
		t.Fatalf("capabilities response = %d %q", first.Code, first.Body.String())
	}
	unsupported := httptest.NewRecorder()
	server.Handler().ServeHTTP(unsupported, authenticatedRequest(http.MethodPost, "/v1/runs/run/actions/cancel", nil))
	if unsupported.Code != http.StatusNotImplemented || !strings.Contains(unsupported.Body.String(), "unsupported_capability") {
		t.Fatalf("unsupported route = %d %q", unsupported.Code, unsupported.Body.String())
	}
}

func TestRunAdmissionCapabilityAuthenticationAndAcceptedResponse(t *testing.T) {
	controller := &testRunAdmissionController{response: RunAdmissionResponseV1{RunID: "admission-run-1", RunURL: "/v1/runs/admission-run-1"}}
	server := newTestServer(t, &testCatalog{}, time.Now().UTC())
	server.reserved.RunAdmission = controller

	capabilities := httptest.NewRecorder()
	server.Handler().ServeHTTP(capabilities, authenticatedRequest(http.MethodGet, "/v1/capabilities", nil))
	if capabilities.Code != http.StatusOK || !strings.Contains(capabilities.Body.String(), `"run_admission":true`) {
		t.Fatalf("admission capability = %d %q", capabilities.Code, capabilities.Body.String())
	}
	body := validRunAdmissionJSON("request-1", "task")
	unauthenticated := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodPost, "/v1/runs", strings.NewReader(body)))
	if unauthenticated.Code != http.StatusUnauthorized || controller.calls != 0 {
		t.Fatalf("unauthenticated admission = %d, calls=%d", unauthenticated.Code, controller.calls)
	}
	accepted := httptest.NewRecorder()
	server.Handler().ServeHTTP(accepted, authenticatedRequest(http.MethodPost, "/v1/runs", bytes.NewReader([]byte(body))))
	if accepted.Code != http.StatusAccepted || accepted.Body.String() != `{"run_id":"admission-run-1","run_url":"/v1/runs/admission-run-1"}`+"\n" {
		t.Fatalf("accepted admission = %d %q", accepted.Code, accepted.Body.String())
	}
	if controller.calls != 1 || controller.principal.PrincipalID != "test" || controller.request.DelegatedActor.SubjectType != PrincipalUser {
		t.Fatalf("admission dispatch = calls=%d principal=%+v request=%+v", controller.calls, controller.principal, controller.request)
	}
}

func TestRunAdmissionStrictDecodeAndTypedErrors(t *testing.T) {
	controller := &testRunAdmissionController{response: RunAdmissionResponseV1{RunID: "admission-run-1", RunURL: "/v1/runs/admission-run-1"}}
	server := newTestServer(t, &testCatalog{}, time.Now().UTC())
	server.reserved.RunAdmission = controller
	valid := validRunAdmissionJSON("request-1", "task")
	for name, body := range map[string]string{
		"unknown field":   strings.TrimSuffix(valid, "}") + `,"manifest_path":"/private"}`,
		"duplicate field": strings.Replace(valid, `"profile_id":"default"`, `"profile_id":"default","profile_id":"other"`, 1),
		"partial SHA":     strings.Replace(valid, strings.Repeat("b", 40), strings.Repeat("b", 12), 1),
		"missing actor":   strings.Replace(valid, `,"delegated_actor":{"subject_id":"human-1","subject_type":"user"}`, "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, authenticatedRequest(http.MethodPost, "/v1/runs", bytes.NewReader([]byte(body))))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
				t.Fatalf("strict admission = %d %q", response.Code, response.Body.String())
			}
		})
	}
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrRequestIDConflict, http.StatusConflict, "request_id_conflict"},
		{ErrUnknownAdmissionProfile, http.StatusNotFound, "unknown_profile"},
		{ErrRepositoryBaseMismatch, http.StatusConflict, "repository_base_mismatch"},
		{ErrAdmissionUnavailable, http.StatusServiceUnavailable, "admission_unavailable"},
		{ErrUnsafeAdmissionMaterialization, http.StatusInternalServerError, "unsafe_admission_materialization"},
	} {
		controller.err = test.err
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, authenticatedRequest(http.MethodPost, "/v1/runs", bytes.NewReader([]byte(valid))))
		if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) || strings.Contains(response.Body.String(), "/private") {
			t.Fatalf("mapped admission error %v = %d %q", test.err, response.Code, response.Body.String())
		}
	}
}

func validRunAdmissionJSON(requestID, task string) string {
	request := RunAdmissionRequestV1{
		SchemaVersion: 1, RequestID: requestID, ProfileID: "default",
		ProductAuthorizationID: "authorization-1", ProductTaskID: "task-1", ProductVersionID: "version-1",
		ProductManifestSHA256: strings.Repeat("a", 64), RepositoryBaseSHA: strings.Repeat("b", 40),
		TaskMarkdown: task, DelegatedActor: DelegatedActorV1{SubjectID: "human-1", SubjectType: PrincipalUser},
	}
	data, _ := json.Marshal(request)
	return string(data)
}

func TestFutureRoutesDispatchTypedInputsAndDeriveCapabilities(t *testing.T) {
	now := time.Now().UTC()
	dependencies := &testFutureDependencies{}
	server := newFutureTestServer(t, now, dependencies, dependencies, dependencies, dependencies, dependencies)

	capabilities := httptest.NewRecorder()
	server.Handler().ServeHTTP(capabilities, authenticatedRequest(http.MethodGet, "/v1/capabilities", nil))
	if capabilities.Code != http.StatusOK || capabilities.Body.String() != "{\"run_admission\":false,\"retry\":false,\"resume\":false,\"recovery\":false,\"cancel\":true,\"decision\":true,\"evidence_download\":true}\n" {
		t.Fatalf("installed-dependency capabilities = %d %q", capabilities.Code, capabilities.Body.String())
	}

	for _, target := range []string{
		"/v1/runs/run-1",
		"/v1/runs/run-1/events?page_size=321&cursor=events-cursor",
		"/v1/runs/run-1/timeline?page_size=123&cursor=timeline-cursor",
		"/v1/runs/run-1/evidence?page_size=42&cursor=evidence-cursor",
	} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, authenticatedRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %q", target, response.Code, response.Body.String())
		}
	}
	if dependencies.eventsPage != (PageRequestV1{PageSize: 321, Cursor: "events-cursor"}) || dependencies.timelinePage != (PageRequestV1{PageSize: 123, Cursor: "timeline-cursor"}) || dependencies.evidencePage != (PageRequestV1{PageSize: 42, Cursor: "evidence-cursor"}) {
		t.Fatalf("typed pages = events %+v, timeline %+v, evidence %+v", dependencies.eventsPage, dependencies.timelinePage, dependencies.evidencePage)
	}

	download := httptest.NewRecorder()
	server.Handler().ServeHTTP(download, authenticatedRequest(http.MethodGet, "/v1/runs/run-1/evidence/evidence-1", nil))
	if download.Code != http.StatusOK || download.Body.String() != "verified evidence" || download.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("evidence download = %d %q", download.Code, download.Body.String())
	}

	command := func(decision bool) *bytes.Reader {
		delegated := ""
		if decision {
			delegated = `,"delegated_actor":{"subject_id":"human-1","subject_type":"user"}`
		}
		return bytes.NewReader([]byte(`{"schema_version":1,"request_id":"request-1","attempt_id":"attempt-1","expected_state":"RUNNING","expected_revision":"` + strings.Repeat("a", 64) + `","reason":"operator request"` + delegated + `,"payload":{}}`))
	}
	for _, action := range []struct {
		kind     ActionKind
		decision bool
	}{{ActionCancel, false}, {ActionDecision, true}} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, authenticatedRequest(http.MethodPost, "/v1/runs/run-1/actions/"+string(action.kind), command(action.decision)))
		if response.Code != http.StatusAccepted {
			t.Fatalf("POST %s = %d %q", action.kind, response.Code, response.Body.String())
		}
	}
	if len(dependencies.actionKinds) != 2 || dependencies.actionKinds[0] != ActionCancel || dependencies.actionKinds[1] != ActionDecision {
		t.Fatalf("typed action kinds = %#v", dependencies.actionKinds)
	}
	status := httptest.NewRecorder()
	server.Handler().ServeHTTP(status, authenticatedRequest(http.MethodGet, "/v1/runs/run-1/actions/operation-1", nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"operation_id":"operation-1"`) {
		t.Fatalf("action status = %d %q", status.Code, status.Body.String())
	}
}

func TestEvidenceBufferGuardCoversMixedRoutesThroughResponseCompletion(t *testing.T) {
	reads := make(chan struct{}, maxEvidenceBuffers)
	lists := make(chan struct{}, 1)
	evidence := &trackedEvidenceReader{lists: lists, reads: reads, data: []byte("verified evidence")}
	server := newFutureTestServer(t, time.Now().UTC(), nil, nil, nil, evidence, nil)
	if capacity := cap(server.evidenceBuffers); capacity != 4 {
		t.Fatalf("evidence buffer guard capacity = %d", capacity)
	}

	writesEntered := make(chan struct{}, maxEvidenceBuffers)
	releaseWrites := make([]chan struct{}, maxEvidenceBuffers)
	var requests sync.WaitGroup
	for index := 0; index < maxEvidenceBuffers; index++ {
		releaseWrites[index] = make(chan struct{})
		requests.Add(1)
		go func(release <-chan struct{}) {
			defer requests.Done()
			response := &heldResponseWriter{header: make(http.Header), entered: writesEntered, release: release}
			server.Handler().ServeHTTP(response, authenticatedRequest(http.MethodGet, "/v1/runs/run-1/evidence/evidence-1", nil))
			if response.status != http.StatusOK || response.body.String() != "verified evidence" {
				t.Errorf("evidence response = %d %q", response.status, response.body.String())
			}
		}(releaseWrites[index])
	}
	for index := 0; index < maxEvidenceBuffers; index++ {
		<-reads
		<-writesEntered
	}
	if held := len(server.evidenceBuffers); held != maxEvidenceBuffers {
		t.Fatalf("buffer permits held during download writes = %d", held)
	}

	cancelContext := &observedCancelContext{Context: context.Background(), done: make(chan struct{}), observed: make(chan struct{})}
	fifthRequest := authenticatedRequest(http.MethodGet, "/v1/runs/run-1/evidence", nil).WithContext(cancelContext)
	fifthResponse := httptest.NewRecorder()
	fifthDone := make(chan struct{})
	go func() {
		server.Handler().ServeHTTP(fifthResponse, fifthRequest)
		close(fifthDone)
	}()
	<-cancelContext.observed
	select {
	case <-lists:
		t.Fatal("evidence list proceeded while all buffer permits were held")
	default:
	}
	select {
	case <-fifthDone:
		t.Fatal("fifth request completed before cancellation or response release")
	default:
	}
	close(cancelContext.done)
	<-fifthDone
	if fifthResponse.Code != http.StatusServiceUnavailable || !strings.Contains(fifthResponse.Body.String(), `"code":"authoritative_read_busy"`) {
		t.Fatalf("canceled fifth response = %d %q", fifthResponse.Code, fifthResponse.Body.String())
	}
	if held := len(server.evidenceBuffers); held != maxEvidenceBuffers {
		t.Fatalf("canceled acquisition changed held permits: %d", held)
	}

	waitContext := &observedCancelContext{Context: context.Background(), done: make(chan struct{}), observed: make(chan struct{})}
	listRequest := authenticatedRequest(http.MethodGet, "/v1/runs/run-1/evidence", nil).WithContext(waitContext)
	listResponse := httptest.NewRecorder()
	listDone := make(chan struct{})
	go func() {
		server.Handler().ServeHTTP(listResponse, listRequest)
		close(listDone)
	}()
	<-waitContext.observed
	select {
	case <-lists:
		t.Fatal("evidence list proceeded before a download write released its permit")
	default:
	}

	close(releaseWrites[0])
	<-lists
	<-listDone
	if listResponse.Code != http.StatusOK || listResponse.Body.String() != "{\"kind\":\"evidence\"}\n" {
		t.Fatalf("evidence list response = %d %q", listResponse.Code, listResponse.Body.String())
	}
	if held := len(server.evidenceBuffers); held != maxEvidenceBuffers-1 {
		t.Fatalf("buffer permits held after list completion = %d", held)
	}

	for index := 1; index < maxEvidenceBuffers; index++ {
		close(releaseWrites[index])
	}
	requests.Wait()
	if held := len(server.evidenceBuffers); held != 0 {
		t.Fatalf("buffer permits held after all responses completed = %d", held)
	}
}

func TestEvidenceBufferGuardCoversListJSONWriteCompletion(t *testing.T) {
	lists := make(chan struct{}, 1)
	evidence := &trackedEvidenceReader{lists: lists}
	server := newFutureTestServer(t, time.Now().UTC(), nil, nil, nil, evidence, nil)
	writesEntered := make(chan struct{}, 1)
	releaseWrite := make(chan struct{})
	response := &heldResponseWriter{header: make(http.Header), entered: writesEntered, release: releaseWrite}
	done := make(chan struct{})
	go func() {
		server.Handler().ServeHTTP(response, authenticatedRequest(http.MethodGet, "/v1/runs/run-1/evidence", nil))
		close(done)
	}()
	<-lists
	<-writesEntered
	if held := len(server.evidenceBuffers); held != 1 {
		t.Fatalf("buffer permits held during list JSON write = %d", held)
	}
	close(releaseWrite)
	<-done
	if response.status != http.StatusOK || response.body.String() != "{\"kind\":\"evidence\"}\n" {
		t.Fatalf("evidence list response = %d %q", response.status, response.body.String())
	}
	if held := len(server.evidenceBuffers); held != 0 {
		t.Fatalf("buffer permits held after list JSON write = %d", held)
	}
}

func TestEvidenceBufferGuardReleasesAfterDependencyFailures(t *testing.T) {
	tests := []struct {
		name   string
		reader *trackedEvidenceReader
		target string
		status int
		code   string
	}{
		{
			name:   "download dependency error",
			reader: &trackedEvidenceReader{reads: make(chan struct{}, 1), err: ErrEvidenceIntegrityChanged},
			target: "/v1/runs/run-1/evidence/evidence-1",
			status: http.StatusConflict,
			code:   `"code":"evidence_integrity_changed"`,
		},
		{
			name:   "oversized artifact",
			reader: &trackedEvidenceReader{reads: make(chan struct{}, 1), data: make([]byte, MaxEvidenceDownloadSize+1)},
			target: "/v1/runs/run-1/evidence/evidence-1",
			status: http.StatusConflict,
			code:   `"code":"evidence_integrity_changed"`,
		},
		{
			name:   "list dependency error",
			reader: &trackedEvidenceReader{err: ErrEvidenceIntegrityChanged},
			target: "/v1/runs/run-1/evidence",
			status: http.StatusConflict,
			code:   `"code":"evidence_integrity_changed"`,
		},
		{
			name:   "invalid list response",
			reader: &trackedEvidenceReader{listData: json.RawMessage(`not-json`)},
			target: "/v1/runs/run-1/evidence",
			status: http.StatusInternalServerError,
			code:   `"code":"internal_durable_substrate_failure"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newFutureTestServer(t, time.Now().UTC(), nil, nil, nil, test.reader, nil)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, authenticatedRequest(http.MethodGet, test.target, nil))
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("evidence failure = %d %q", response.Code, response.Body.String())
			}
			if held := len(server.evidenceBuffers); held != 0 {
				t.Fatalf("buffer permits held after evidence failure = %d", held)
			}
		})
	}
}

func TestFutureRoutesRemainUnsupportedWithNilDependencies(t *testing.T) {
	server := newTestServer(t, &testCatalog{}, time.Now().UTC())
	requests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/runs/run-1"},
		{http.MethodGet, "/v1/runs/run-1/events"},
		{http.MethodGet, "/v1/runs/run-1/timeline"},
		{http.MethodGet, "/v1/runs/run-1/evidence"},
		{http.MethodGet, "/v1/runs/run-1/evidence/evidence-1"},
		{http.MethodPost, "/v1/runs/run-1/actions/cancel"},
		{http.MethodPost, "/v1/runs/run-1/actions/decision"},
		{http.MethodGet, "/v1/runs/run-1/actions/operation-1"},
	}
	for _, test := range requests {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, authenticatedRequest(test.method, test.path, nil))
		if response.Code != http.StatusNotImplemented || !strings.Contains(response.Body.String(), `"code":"unsupported_capability"`) {
			t.Fatalf("%s %s = %d %q", test.method, test.path, response.Code, response.Body.String())
		}
	}
	unknown := httptest.NewRecorder()
	server.Handler().ServeHTTP(unknown, authenticatedRequest(http.MethodPost, "/v1/runs/run-1/actions/retry", nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("deferred retry route = %d %q", unknown.Code, unknown.Body.String())
	}
}

func TestFutureRoutesRequireAnImmutableCatalogRegistration(t *testing.T) {
	dependencies := &testFutureDependencies{}
	server := newFutureTestServer(t, time.Now().UTC(), dependencies, nil, nil, nil, nil)
	server.catalog = &testCatalog{readErr: os.ErrNotExist}
	missing := httptest.NewRecorder()
	server.Handler().ServeHTTP(missing, authenticatedRequest(http.MethodGet, "/v1/runs/missing", nil))
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), `"code":"not_found"`) {
		t.Fatalf("unregistered future read = %d %q", missing.Code, missing.Body.String())
	}
	server.catalog = &testCatalog{readErr: runtimecatalog.ErrIntegrity}
	unsafe := httptest.NewRecorder()
	server.Handler().ServeHTTP(unsafe, authenticatedRequest(http.MethodGet, "/v1/runs/unsafe", nil))
	if unsafe.Code != http.StatusInternalServerError || !strings.Contains(unsafe.Body.String(), `"code":"catalog_integrity_failure"`) {
		t.Fatalf("unsafe registration read = %d %q", unsafe.Code, unsafe.Body.String())
	}
}

func TestActionDecodingIsStrictAndBounded(t *testing.T) {
	now := time.Now().UTC()
	dependencies := &testFutureDependencies{}
	server := newFutureTestServer(t, now, nil, nil, nil, nil, dependencies)
	base := `{"schema_version":1,"request_id":"request-1","attempt_id":"attempt-1","expected_state":"RUNNING","expected_revision":"` + strings.Repeat("a", 64) + `","reason":"reason","payload":{}}`
	for name, body := range map[string]string{
		"unknown field":   strings.TrimSuffix(base, "}") + `,"unknown":true}`,
		"duplicate field": strings.Replace(base, `"reason":"reason"`, `"reason":"one","reason":"two"`, 1),
		"deep payload":    strings.Replace(base, `"payload":{}`, `"payload":[[[[[[[[[0]]]]]]]]]`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, authenticatedRequest(http.MethodPost, "/v1/runs/run-1/actions/cancel", bytes.NewReader([]byte(body))))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("strict decode = %d %q", response.Code, response.Body.String())
			}
		})
	}
	oversizedRequest := authenticatedRequest(http.MethodPost, "/v1/runs/run-1/actions/cancel", bytes.NewReader(make([]byte, MaxRequestBodyBytes+1)))
	oversizedRequest.ContentLength = -1
	oversized := httptest.NewRecorder()
	server.Handler().ServeHTTP(oversized, oversizedRequest)
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked oversized action = %d %q", oversized.Code, oversized.Body.String())
	}
	invalidPage := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalidPage, authenticatedRequest(http.MethodGet, "/v1/runs/run-1/events?page_size=501", nil))
	if invalidPage.Code != http.StatusBadRequest {
		t.Fatalf("oversized events page = %d %q", invalidPage.Code, invalidPage.Body.String())
	}
}

func TestCanonicalDependencyErrorMapping(t *testing.T) {
	now := time.Now().UTC()
	for _, test := range []struct {
		name      string
		err       error
		status    int
		code      string
		retryable bool
		reconcile bool
	}{
		{"not found", ErrDependencyNotFound, http.StatusNotFound, "not_found", false, false},
		{"busy", ErrAuthoritativeReadBusy, http.StatusServiceUnavailable, "authoritative_read_busy", true, false},
		{"lineage", ErrProjectionLineageChanged, http.StatusConflict, "projection_lineage_changed", false, false},
		{"cursor epoch", ErrCursorEpochChanged, http.StatusConflict, "cursor_epoch_changed", false, false},
		{"invalid cursor", ErrInvalidCursor, http.StatusBadRequest, "invalid_cursor", false, false},
		{"integrity", ErrProjectionIntegrity, http.StatusInternalServerError, "projection_integrity_failure", false, false},
		{"stale state", ErrStaleExpectedState, http.StatusConflict, "stale_expected_state", false, false},
		{"request conflict", ErrRequestIDConflict, http.StatusConflict, "request_id_conflict", false, false},
		{"reconcile", ErrReconciliationRequired, http.StatusConflict, "reconciliation_required", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencies := &testFutureDependencies{err: test.err}
			server := newFutureTestServer(t, now, dependencies, nil, nil, nil, nil)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, authenticatedRequest(http.MethodGet, "/v1/runs/run-1", nil))
			var decoded ErrorResponseV1
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status || decoded.Error.Code != test.code || decoded.Error.Retryable != test.retryable || decoded.Error.ReconciliationRequired != test.reconcile {
				t.Fatalf("mapped error = %d %+v", response.Code, decoded.Error)
			}
		})
	}
}

func newFutureTestServer(t *testing.T, now time.Time, runs RunProjectionReader, events EventProjectionReader, timeline TimelineReader, evidence EvidenceReader, actions ActionController) *Server {
	t.Helper()
	signer, err := NewCursorSignerWithClock("key-v1", make([]byte, 32), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{
		Authenticator: testAuthenticator{}, Authority: &AuthorityMatcher{grants: map[string]authorityGrant{}, digest: strings.Repeat("a", 64)},
		Catalog: &testCatalog{}, CursorSigner: signer, Clock: func() time.Time { return now },
		RunProjections: runs, Events: events, Timeline: timeline, Evidence: evidence, Actions: actions,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func TestRunListUsesSignedLexicalKeysetAndOnlyPublicRegistrationFacts(t *testing.T) {
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	makeRun := func(id string) runtimecatalog.RunRegistrationV1 {
		return runtimecatalog.RunRegistrationV1{
			Kind: "RunRegistrationV1", RunID: id, RepositoryIdentityDigest: strings.Repeat("a", 64),
			AuthorityDigest: strings.Repeat("b", 64), CanonicalLedgerPath: "/private/ledger/" + id,
			CanonicalEvidenceRoot: "/private/evidence/" + id, InitialRegistrationTimestamp: now.Format(time.RFC3339Nano),
		}
	}
	catalog := &testCatalog{runs: []runtimecatalog.RunRegistrationV1{makeRun("run-c"), makeRun("run-a"), makeRun("run-b")}}
	server := newTestServer(t, catalog, now)
	first := httptest.NewRecorder()
	server.Handler().ServeHTTP(first, authenticatedRequest(http.MethodGet, "/v1/runs?page_size=2", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first page = %d %q", first.Code, first.Body.String())
	}
	var page RunsPageV1
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 2 || page.Runs[0].RunID != "run-a" || page.Runs[1].RunID != "run-b" || page.NextCursor == "" {
		t.Fatalf("first page = %+v", page)
	}
	if strings.Contains(first.Body.String(), "/private/") || strings.Contains(first.Body.String(), "authority") || strings.Contains(first.Body.String(), "active") {
		t.Fatalf("run list disclosed internal metadata: %q", first.Body.String())
	}
	// Insertions at or before the last key are skipped by this continuation;
	// greater keys may appear, exactly as the weak-consistency contract states.
	catalog.mu.Lock()
	catalog.runs = append(catalog.runs, makeRun("run-aa"), makeRun("run-d"))
	catalog.mu.Unlock()
	second := httptest.NewRecorder()
	server.Handler().ServeHTTP(second, authenticatedRequest(http.MethodGet, "/v1/runs?page_size=2&cursor="+page.NextCursor, nil))
	if second.Code != http.StatusOK {
		t.Fatalf("second page = %d %q", second.Code, second.Body.String())
	}
	page = RunsPageV1{}
	if err := json.Unmarshal(second.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 2 || page.Runs[0].RunID != "run-c" || page.Runs[1].RunID != "run-d" {
		t.Fatalf("continuation page = %+v", page)
	}
}

func TestServerBoundsBodiesConcurrencyAddressesAndTimeouts(t *testing.T) {
	if err := ValidateLoopbackAddress("0.0.0.0:8080"); err == nil {
		t.Fatal("non-loopback address accepted")
	}
	if err := ValidateLoopbackAddress("localhost:8080"); err == nil {
		t.Fatal("hostname listener accepted")
	}
	server := newTestServer(t, &testCatalog{}, time.Now().UTC())
	httpServer, err := server.HTTPServer("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if httpServer.ReadHeaderTimeout != 5*time.Second || httpServer.ReadTimeout != 10*time.Second || httpServer.WriteTimeout != 30*time.Second || httpServer.IdleTimeout != 60*time.Second {
		t.Fatalf("HTTP timeouts = %+v", httpServer)
	}
	body := bytes.NewReader(make([]byte, MaxRequestBodyBytes+1))
	request := authenticatedRequest(http.MethodPost, "/v1/capabilities", body)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d", response.Code)
	}

	blocking := &testCatalog{entered: make(chan struct{}, MaxInFlightRequests), release: make(chan struct{})}
	server = newTestServer(t, blocking, time.Now().UTC())
	var wait sync.WaitGroup
	for index := 0; index < MaxInFlightRequests; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, authenticatedRequest(http.MethodGet, "/v1/runs", nil))
		}()
	}
	for index := 0; index < MaxInFlightRequests; index++ {
		<-blocking.entered
	}
	overflow := httptest.NewRecorder()
	server.Handler().ServeHTTP(overflow, authenticatedRequest(http.MethodGet, "/v1/runs", nil))
	if overflow.Code != http.StatusServiceUnavailable {
		t.Fatalf("request 65 = %d %q", overflow.Code, overflow.Body.String())
	}
	close(blocking.release)
	wait.Wait()
}

func TestServerRejectsMissingSecurityDependenciesAndHidesSecrets(t *testing.T) {
	signer, _ := NewCursorSigner("key", make([]byte, 32))
	catalog := &testCatalog{}
	base := ServerConfig{Authenticator: testAuthenticator{}, Authority: &AuthorityMatcher{grants: map[string]authorityGrant{}, digest: strings.Repeat("a", 64)}, Catalog: catalog, CursorSigner: signer}
	for _, mutate := range []func(*ServerConfig){
		func(c *ServerConfig) { c.Authenticator = nil }, func(c *ServerConfig) { c.Authority = nil },
		func(c *ServerConfig) { c.Catalog = nil }, func(c *ServerConfig) { c.CursorSigner = nil },
	} {
		config := base
		mutate(&config)
		if _, err := NewServer(config); err == nil {
			t.Fatal("server accepted a missing security dependency")
		}
	}
	server := newTestServer(t, catalog, time.Now().UTC())
	request := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
	request.Header.Set("Authorization", "Bearer do-not-disclose-this-token")
	request.Header.Set("X-Request-ID", "/private/path")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if strings.Contains(response.Body.String(), "do-not-disclose") || strings.Contains(response.Body.String(), "/private/path") {
		t.Fatalf("safe error disclosed sensitive input: %q", response.Body.String())
	}
}

func TestFrozenServiceResourceCeilings(t *testing.T) {
	if MaxInFlightRequests != 64 || MaxRequestBodyBytes != 1<<20 || DefaultRunsPageSize != 50 || MaxRunsPageSize != 200 {
		t.Fatal("HTTP request or pagination ceiling changed")
	}
	if maxEvidenceBuffers != 4 || MaxEvidenceDownloadSize != 16<<20 {
		t.Fatal("evidence response or artifact ceiling changed")
	}
	if MaxCursorTokenBytes != 4<<10 || MaxCursorLifetime != 15*time.Minute || MaxTokenFileBytes != 4<<10 || MaxCursorKeyBytes != 8<<10 || MaxGrantFileBytes != 64<<10 {
		t.Fatal("security configuration or cursor ceiling changed")
	}
	if MaxCommandReasonBytes != 1<<10 || MaxActionPayloadBytes != 16<<10 || MaxActionPayloadDepth != 8 {
		t.Fatal("command envelope ceiling changed")
	}
}

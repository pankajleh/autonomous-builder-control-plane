package githubmergeprovider

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/mergelifecycle"
)

func TestTask3SealedTransportAndCredentialBoundary(t *testing.T) {
	auth := mustValue(NewUserAuthenticator("credential-that-must-not-leak", "U_actor"))
	var calls int
	base := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Header.Get("Authorization") != "Bearer credential-that-must-not-leak" ||
			request.Header.Get("X-GitHub-Api-Version") != apiVersion || request.Header.Get("Accept") != "application/vnd.github+json" {
			t.Fatal("sealed headers were not injected exactly")
		}
		return githubResponse(200, "request-1", []byte(`{}`)), nil
	})
	sealed := sealedRoundTripper{auth: auth, base: base}
	for _, rawURL := range []string{"https://evil.example/graphql", "https://api.github.com/evil", "https://api.github.com:444/graphql"} {
		request := mustValue(http.NewRequest(http.MethodPost, rawURL, strings.NewReader(`{}`)))
		if _, err := sealed.RoundTrip(request); err == nil || strings.Contains(err.Error(), "credential-that-must-not-leak") {
			t.Fatalf("unsafe origin/path was not safely rejected: %s, %v", rawURL, err)
		}
	}
	request := mustValue(http.NewRequest(http.MethodPost, apiOrigin+"/graphql", strings.NewReader(`{}`)))
	request.Header.Set("Authorization", "attacker")
	if _, err := sealed.RoundTrip(request); err == nil {
		t.Fatal("caller authorization header was accepted")
	}
	request = mustValue(http.NewRequest(http.MethodPost, apiOrigin+"/graphql", strings.NewReader(`{}`)))
	response := mustValue(sealed.RoundTrip(request))
	_ = response.Body.Close()
	if calls != 1 {
		t.Fatalf("base transport calls = %d", calls)
	}
	limits := githublifecycle.DefaultLimits()
	transport := productionTransport(limits)
	if transport.Proxy != nil || !transport.DisableKeepAlives || transport.ForceAttemptHTTP2 || !transport.DisableCompression || transport.MaxResponseHeaderBytes != int64(limits.MaxResponseHeaderBytes) {
		t.Fatal("production connection policy is not sealed")
	}
	client := productionReadClient(auth, limits)
	if err := client.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatal("redirects are not disabled")
	}
}

func TestTask3ResponseAndBodyResourceLimits(t *testing.T) {
	limits := githublifecycle.DefaultLimits()
	provider := &Provider{limits: limits, now: time.Now}
	budget := mergelifecycle.ProviderBudgetV1{RequestBytes: limits.MaxCumulativeRequestBytes, HeaderBytes: limits.MaxCumulativeResponseHeaderBytes,
		CompressedResponseBytes: limits.MaxCumulativeCompressedResponseBytes, DecompressedResponseBytes: limits.MaxCumulativeDecompressedResponseBytes,
		ActiveNanos: int64(limits.MaxCumulativeActiveProviderCallTime)}
	jsonAtLimit := append([]byte{'"'}, bytes.Repeat([]byte{'a'}, limits.MaxDecompressedResponseBodyBytes-2)...)
	jsonAtLimit = append(jsonAtLimit, '"')
	body := &closeTrackingBody{Reader: bytes.NewReader(jsonAtLimit)}
	header := http.Header{"X-Github-Request-Id": []string{"request-limit"}}
	meter := &callMeter{budget: budget, started: time.Now()}
	result, err := provider.consumeResponse(&http.Response{StatusCode: 200, Header: header, Body: body}, meter)
	if err != nil || len(result.Body) != limits.MaxDecompressedResponseBodyBytes || !body.wasClosed() {
		t.Fatalf("exact response limit = %d, %v, closed=%v", len(result.Body), err, body.wasClosed())
	}
	over := append(append([]byte(nil), jsonAtLimit[:len(jsonAtLimit)-1]...), 'a', '"')
	body = &closeTrackingBody{Reader: bytes.NewReader(over)}
	meter = &callMeter{budget: budget, started: time.Now()}
	if _, err := provider.consumeResponse(&http.Response{StatusCode: 200, Header: header, Body: body}, meter); err == nil || !body.wasClosed() {
		t.Fatal("limit+1 response did not fail closed and close its body")
	}
	header = http.Header{"X-Github-Request-Id": []string{"request-header"}}
	baseBytes := canonicalHeaderBytes(header)
	valueLength := limits.MaxResponseHeaderBytes - int(baseBytes) - len("X-Test") - 4
	header.Set("X-Test", strings.Repeat("h", valueLength))
	if canonicalHeaderBytes(header) != int64(limits.MaxResponseHeaderBytes) {
		t.Fatal("test failed to construct exact header limit")
	}
	body = &closeTrackingBody{Reader: strings.NewReader(`{}`)}
	meter = &callMeter{budget: budget, started: time.Now()}
	if _, err := provider.consumeResponse(&http.Response{StatusCode: 200, Header: header, Body: body}, meter); err != nil {
		t.Fatalf("exact header limit failed: %v", err)
	}
	header.Set("X-Test", strings.Repeat("h", valueLength+1))
	body = &closeTrackingBody{Reader: strings.NewReader(`{}`)}
	meter = &callMeter{budget: budget, started: time.Now()}
	if _, err := provider.consumeResponse(&http.Response{StatusCode: 200, Header: header, Body: body}, meter); err == nil || !body.wasClosed() {
		t.Fatal("header limit+1 did not fail closed")
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte(`{"compressed":true}`))
	_ = writer.Close()
	header = http.Header{"X-Github-Request-Id": []string{"request-gzip"}, "Content-Encoding": []string{"gzip"}}
	meter = &callMeter{budget: budget, started: time.Now()}
	if result, err := provider.consumeResponse(&http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(bytes.NewReader(compressed.Bytes()))}, meter); err != nil || !bytes.Equal(result.Body, []byte(`{"compressed":true}`)) {
		t.Fatalf("bounded gzip response = %q, %v", result.Body, err)
	}
	for _, ids := range [][]string{{"one", "two"}, {"unsafe request id"}, {strings.Repeat("x", limits.MaxRequestIDBytes+1)}} {
		header = http.Header{"X-Github-Request-Id": ids}
		body = &closeTrackingBody{Reader: strings.NewReader(`{}`)}
		meter = &callMeter{budget: budget, started: time.Now()}
		if _, err := provider.consumeResponse(&http.Response{StatusCode: 200, Header: header, Body: body}, meter); err == nil || !body.wasClosed() {
			t.Fatalf("unsafe request IDs accepted: %v", ids)
		}
	}
	body = &closeTrackingBody{Reader: strings.NewReader(`{}`), err: errors.New("close failure")}
	meter = &callMeter{budget: budget, started: time.Now()}
	if _, err := provider.consumeResponse(&http.Response{StatusCode: 200, Header: http.Header{"X-Github-Request-Id": []string{"close-error"}}, Body: body}, meter); err == nil {
		t.Fatal("body close failure was accepted")
	}
}

func TestTask3FrozenCapabilityValidation(t *testing.T) {
	record, recordDigest, err := validateCapabilityRecord(embeddedCapabilityRecord)
	if err != nil || recordDigest != frozenCapabilitySHA256 || record.Name != githublifecycle.GitHubAtomicBaseHeadCapabilityV1 {
		t.Fatalf("embedded capability = %+v, %s, %v", record, recordDigest, err)
	}
	fixture := newProviderFixture(t, nil)
	capability := mustValue(fixture.provider.Capability("R_repo"))
	if capability.SHA256() != fixture.sealed.MergeInput().Capability().SHA256() {
		t.Fatal("compiled provider capability differs from admitted capability")
	}
	for _, mutation := range []func(*capabilityRecordV1){
		func(record *capabilityRecordV1) { record.ContractSchemaSHA256 = strings.Repeat("0", 64) },
		func(record *capabilityRecordV1) { record.SupportsSameOIDNoOp = false },
		func(record *capabilityRecordV1) { record.AllOrNothing = false },
		func(record *capabilityRecordV1) { record.BaseThenHeadOrder = false },
	} {
		changed := record
		changed.UpdateRefsInput = cloneMap(record.UpdateRefsInput)
		changed.RefUpdate = cloneMap(record.RefUpdate)
		mutation(&changed)
		data := mustValue(json.Marshal(changed))
		if _, _, err := validateCapabilityRecord(data); err == nil {
			t.Fatal("capability mismatch was accepted")
		}
	}
}

func TestTask3PullRequestEligibilityAndForkRejection(t *testing.T) {
	fixture := newProviderFixture(t, nil)
	provider, transport := authorizationProvider(t, fixture, nil, []byte(`[]`))
	observation, err := provider.ObserveAuthorization(context.Background(), mergelifecycle.ObservationInitial, fixture.authority)
	if err != nil || githublifecycle.ValidateAuthoritativePullRequestSnapshotV1(fixture.authority, observation.PullRequest, fixture.limits) != nil {
		t.Fatalf("eligible authorization observation failed: %v", err)
	}
	if len(transport.snapshot()) != 4 {
		t.Fatalf("authorization HTTP calls = %d", len(transport.snapshot()))
	}
	for name, mutate := range map[string]func(*authorizationQueryResponse){
		"closed": func(response *authorizationQueryResponse) { response.Data.Repository.PullRequest.State = "CLOSED" },
		"draft": func(response *authorizationQueryResponse) {
			yes := true
			response.Data.Repository.PullRequest.IsDraft = &yes
		},
		"merged": func(response *authorizationQueryResponse) {
			yes := true
			response.Data.Repository.PullRequest.Merged = &yes
		},
		"fork": func(response *authorizationQueryResponse) {
			response.Data.Repository.PullRequest.HeadRepository.ID = "R_fork"
		},
		"repository": func(response *authorizationQueryResponse) { response.Data.Repository.ID = "R_other" },
		"principal":  func(response *authorizationQueryResponse) { response.Data.Viewer.ID = "U_other" },
	} {
		t.Run(name, func(t *testing.T) {
			provider, _ := authorizationProvider(t, fixture, mutate, []byte(`[]`))
			if _, err := provider.ObserveAuthorization(context.Background(), mergelifecycle.ObservationInitial, fixture.authority); err == nil {
				t.Fatal("ineligible PR/repository/principal was accepted")
			}
		})
	}
	provider, _ = authorizationProvider(t, fixture, nil, []byte(`[]`))
	initial := mustValue(provider.ObserveAuthorization(context.Background(), mergelifecycle.ObservationInitial, fixture.authority))
	final := mustValue(provider.ObserveAuthorization(context.Background(), mergelifecycle.ObservationFinal, fixture.authority))
	initialIDs := observationRequestIDs(initial)
	for id := range observationRequestIDs(final) {
		if initialIDs[id] {
			t.Fatalf("final observation reused admission request identity %q", id)
		}
	}
	if final.StartedUnixNano <= initial.CompletedUnixNano || final.Counters.FinalRevalidationHTTPCalls == 0 || final.Counters.AdmissionHTTPCalls != 0 {
		t.Fatal("final observation is not phase-fresh or phase-local")
	}
}

func TestTask3PaginationClosureAndDismissedReview(t *testing.T) {
	reviewer := githublifecycle.StableIdentityV1{DatabaseID: 41, NodeID: "U_reviewer"}
	fixture := newProviderFixture(t, []githublifecycle.StableIdentityV1{reviewer})
	reviewBody, _ := json.Marshal([]reviewResponse{{ID: 51, NodeID: "PRR_review", State: "DISMISSED", CommitID: fixture.headSHA.String(), User: &struct {
		ID     int64  `json:"id"`
		NodeID string `json:"node_id"`
	}{ID: reviewer.DatabaseID, NodeID: reviewer.NodeID}}})
	provider, _ := authorizationProvider(t, fixture, nil, reviewBody)
	observation, err := provider.ObserveAuthorization(context.Background(), mergelifecycle.ObservationInitial, fixture.authority)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "review") {
		t.Fatalf("dismissed exact-head review did not block: err=%v reviews=%+v eligible=%+v policy=%v", err,
			observation.PullRequest.Input().Reviews, fixture.authority.MergePolicy().Input().EligibleReviewers,
			githublifecycle.EvaluateMergePolicyV1(fixture.authority, observation.PullRequest, observation.Checks, observation.CheckRunsClosure, observation.CommitStatusClosure, fixture.limits))
	}
	if has, _, err := nextRESTPage(`<https://api.github.com/repos/octo-org/control-plane/pulls/17/reviews?page=2&per_page=100>; rel="next"`); err != nil || !has {
		t.Fatalf("valid next link failed: %v", err)
	}
	for _, malformed := range []string{
		`<https://evil.example/x?page=2>; rel="next"`,
		`<https://api.github.com/x?page=2>; rel="next", <https://api.github.com/y?page=3>; rel="next"`,
		`not-a-link`,
	} {
		if _, _, err := nextRESTPage(malformed); err == nil {
			t.Fatalf("malformed pagination link accepted: %s", malformed)
		}
	}
	duplicateBody, _ := json.Marshal([]reviewResponse{
		{ID: 1, NodeID: "same", State: "COMMENTED", CommitID: fixture.headSHA.String(), User: &struct {
			ID     int64  `json:"id"`
			NodeID string `json:"node_id"`
		}{1, "U1"}},
		{ID: 1, NodeID: "same", State: "COMMENTED", CommitID: fixture.headSHA.String(), User: &struct {
			ID     int64  `json:"id"`
			NodeID string `json:"node_id"`
		}{1, "U1"}},
	})
	provider, _ = authorizationProvider(t, fixture, nil, duplicateBody)
	if _, err := provider.ObserveAuthorization(context.Background(), mergelifecycle.ObservationInitial, fixture.authority); err == nil {
		t.Fatal("duplicate pagination item identity was accepted")
	}
}

func TestTask3ExactCommitPreparationObservation(t *testing.T) {
	fixture := newProviderFixture(t, nil)
	transport := &scriptTransport{}
	transport.handler = func(call recordedRequest) (*http.Response, error) {
		switch call.Method {
		case http.MethodPost:
			want := mustValue(encodeCreateCommit(fixture.recipe))
			if !bytes.Equal(call.Body, want) {
				t.Fatal("commit creation body differs from deterministic recipe")
			}
			return githubResponse(201, "commit-create", remoteCommitBody(t, fixture.recipe)), nil
		case http.MethodGet:
			return githubResponse(200, "commit-observe", remoteCommitBody(t, fixture.recipe)), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", call.Method)
		}
	}
	provider := mustValue(newTestProvider(fixture.auth, fixture.limits, transport))
	preparation, err := provider.PrepareResultCommit(context.Background(), fixture.recipe)
	if err != nil || preparation.ResultSHA != fixture.recipe.ExpectedResultSHA().String() ||
		!bytes.Equal(preparation.Observation.ObjectBytes, fixture.recipe.CommitBytes()) || len(transport.snapshot()) != 2 {
		t.Fatalf("exact commit preparation = %+v, %v, calls=%d", preparation, err, len(transport.snapshot()))
	}
	for name, status := range map[string]int{"rejected": 422, "unavailable": 500, "unexpected-success": 200} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64
			transport := &scriptTransport{handler: func(call recordedRequest) (*http.Response, error) {
				calls.Add(1)
				return githubResponse(status, "commit-class", []byte(`{"message":"classified"}`)), nil
			}}
			provider := mustValue(newTestProvider(fixture.auth, fixture.limits, transport))
			if _, err := provider.PrepareResultCommit(context.Background(), fixture.recipe); err == nil || calls.Load() != 1 {
				t.Fatalf("commit response class status=%d, calls=%d, err=%v", status, calls.Load(), err)
			}
		})
	}
	transport = &scriptTransport{handler: func(call recordedRequest) (*http.Response, error) {
		return githubResponse(200, "commit-reconcile", remoteCommitBody(t, fixture.recipe)), nil
	}}
	provider = mustValue(newTestProvider(fixture.auth, fixture.limits, transport))
	if _, err := provider.ReconcileResultCommit(context.Background(), fixture.recipe); err != nil || len(transport.snapshot()) != 1 || transport.snapshot()[0].Method != http.MethodGet {
		t.Fatalf("commit reconciliation mutated or failed: %v", err)
	}
}

func TestTask3AtomicUpdateRefsWireAndForbiddenEndpoints(t *testing.T) {
	fixture := newProviderFixture(t, nil)
	transport := &scriptTransport{}
	appliedTargetScript(t, fixture, transport)
	provider := mustValue(newTestProvider(fixture.auth, fixture.limits, transport))
	outcome, err := provider.SubmitTarget(context.Background(), targetExecution(t, fixture))
	if err != nil || outcome.Disposition != githublifecycle.ReconciliationApplied || githublifecycle.ValidateMergeResult(fixture.sealed, outcome.Result, fixture.limits) != nil {
		t.Fatalf("atomic target result = %s, %v", outcome.Disposition, err)
	}
	calls := transport.snapshot()
	mutations := 0
	for _, call := range calls {
		if call.Method == http.MethodPost {
			mutations++
			if call.URL != apiOrigin+githublifecycle.GitHubGraphQLPathV1 || !bytes.Equal(call.Body, fixture.submission.RequestBody()) {
				t.Fatal("target mutation did not transmit the published submission bytes")
			}
		}
		if call.Method == http.MethodPatch || strings.Contains(call.URL, "/merge") || strings.Contains(call.URL, "/git/refs/") {
			t.Fatalf("forbidden mutation endpoint became reachable: %s %s", call.Method, call.URL)
		}
	}
	if mutations != 1 || len(calls) != 4 {
		t.Fatalf("mutation calls=%d total=%d", mutations, len(calls))
	}
	var request struct {
		Query     string `json:"query"`
		Variables struct {
			Input struct {
				ClientMutationID string `json:"clientMutationId"`
				RefUpdates       []struct {
					AfterOID  string `json:"afterOid"`
					BeforeOID string `json:"beforeOid"`
					Force     bool   `json:"force"`
					Name      string `json:"name"`
				} `json:"refUpdates"`
				RepositoryID string `json:"repositoryId"`
			} `json:"input"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(fixture.submission.RequestBody(), &request); err != nil || len(request.Variables.Input.RefUpdates) != 2 {
		t.Fatal("target request did not contain exactly two updates")
	}
	updates := request.Variables.Input.RefUpdates
	if updates[0].Name != "refs/heads/main" || updates[0].BeforeOID != fixture.baseSHA.String() || updates[0].AfterOID != fixture.recipe.ExpectedResultSHA().String() || updates[0].Force ||
		updates[1].Name != "refs/heads/feature/exact-head" || updates[1].BeforeOID != fixture.headSHA.String() || updates[1].AfterOID != fixture.headSHA.String() || updates[1].Force {
		t.Fatal("target request changed base/head CAS semantics")
	}
}

func TestTask3SubmissionByteBoundaryNoRetry(t *testing.T) {
	fixture := newProviderFixture(t, nil)
	var calls atomic.Int64
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("timeout after possible bytes")
	})
	provider := mustValue(newTestProvider(fixture.auth, fixture.limits, transport))
	outcome, err := provider.SubmitTarget(context.Background(), targetExecution(t, fixture))
	if err != nil || outcome.Disposition != githublifecycle.ReconciliationUnknown || calls.Load() != 1 || outcome.Accounting.RequestBytes != fixture.submission.RequestBodyBytes() {
		t.Fatalf("possible-byte classification = %s, calls=%d, bytes=%d, err=%v", outcome.Disposition, calls.Load(), outcome.Accounting.RequestBytes, err)
	}
	zeroCalls := atomic.Int64{}
	zeroBase := roundTripFunc(func(*http.Request) (*http.Response, error) {
		zeroCalls.Add(1)
		return nil, errors.New("dial failed before plaintext")
	})
	readClient := &http.Client{Transport: sealedRoundTripper{auth: fixture.auth, base: zeroBase}}
	provider = mustValue(newProvider(fixture.auth, fixture.limits, readClient, func(*submissionTracker) *http.Client {
		return &http.Client{Transport: sealedRoundTripper{auth: fixture.auth, base: zeroBase}}
	}))
	outcome, err = provider.SubmitTarget(context.Background(), targetExecution(t, fixture))
	if err != nil || outcome.Disposition != githublifecycle.ReconciliationNotApplied || outcome.NotAppliedProof.Input().Kind != githublifecycle.NotAppliedZeroRequestBytes ||
		outcome.Accounting.RequestBytes != 0 || zeroCalls.Load() != 1 {
		t.Fatalf("zero-byte classification = %s, calls=%d, bytes=%d, err=%v", outcome.Disposition, zeroCalls.Load(), outcome.Accounting.RequestBytes, err)
	}
	closeBody := &closeTrackingBody{Reader: strings.NewReader(`{"data":{"updateRefs":{"clientMutationId":"merge-write-1"}}}`), err: errors.New("close")}
	provider = mustValue(newTestProvider(fixture.auth, fixture.limits, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Github-Request-Id": []string{"close-target"}}, Body: closeBody}, nil
	})))
	outcome, err = provider.SubmitTarget(context.Background(), targetExecution(t, fixture))
	if err != nil || outcome.Disposition != githublifecycle.ReconciliationUnknown || !closeBody.wasClosed() {
		t.Fatalf("body-close ambiguity = %s, %v", outcome.Disposition, err)
	}
}

func TestTask3ReconciliationDispositions(t *testing.T) {
	fixture := newProviderFixture(t, nil)
	reconcileInput := mustValue(githublifecycle.NewMergeReconcileWriteInput(fixture.sealed, fixture.submission,
		[]ledger.EvidenceRef{{URI: "evidence/submission", Kind: "target-submission", SHA256: strings.Repeat("a", 64)}}, fixture.limits))
	transport := &scriptTransport{}
	appliedTargetScript(t, fixture, transport)
	provider := mustValue(newTestProvider(fixture.auth, fixture.limits, transport))
	outcome, err := provider.ReconcileTarget(context.Background(), reconcileInput)
	if err != nil || outcome.Disposition != githublifecycle.ReconciliationApplied {
		t.Fatalf("applied reconciliation = %s, %v", outcome.Disposition, err)
	}
	for _, call := range transport.snapshot() {
		if call.Method != http.MethodGet {
			t.Fatalf("reconciliation performed mutation: %s", call.Method)
		}
	}
	transport = &scriptTransport{}
	appliedTargetScript(t, fixture, transport)
	original := transport.handler
	transport.handler = func(call recordedRequest) (*http.Response, error) {
		if strings.Contains(call.URL, "/git/ref/heads/main") {
			body, _ := json.Marshal(gitRefResponse{Ref: "refs/heads/main", Object: struct {
				SHA  string `json:"sha"`
				Type string `json:"type"`
			}{fixture.baseSHA.String(), "commit"}})
			return githubResponse(200, "old-base", body), nil
		}
		return original(call)
	}
	provider = mustValue(newTestProvider(fixture.auth, fixture.limits, transport))
	outcome = mustValue(provider.ReconcileTarget(context.Background(), reconcileInput))
	if outcome.Disposition != githublifecycle.ReconciliationUnknown {
		t.Fatalf("old target incorrectly proved non-application: %s", outcome.Disposition)
	}
	rejectionBody := []byte(`{"data":{"updateRefs":null},"errors":[{"type":"FAILED_PRECONDITION","path":["updateRefs","refUpdates","beforeOid"],"extensions":{"code":"UPDATE_REFS_BEFORE_OID_MISMATCH","ref_update_index":0}}]}`)
	provider = mustValue(newTestProvider(fixture.auth, fixture.limits, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return githubResponse(200, "atomic-rejection", rejectionBody), nil
	})))
	outcome = mustValue(provider.SubmitTarget(context.Background(), targetExecution(t, fixture)))
	if outcome.Disposition != githublifecycle.ReconciliationNotApplied || outcome.NotAppliedProof.Input().Kind != githublifecycle.NotAppliedAtomicBaseRejected {
		t.Fatalf("typed rejection = %s, %s", outcome.Disposition, outcome.NotAppliedProof.Input().Kind)
	}
	provider = mustValue(newTestProvider(fixture.auth, fixture.limits, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return githubResponse(200, "generic-error", []byte(`{"data":{"updateRefs":null},"errors":[{"message":"before oid changed"}]}`)), nil
	})))
	outcome = mustValue(provider.SubmitTarget(context.Background(), targetExecution(t, fixture)))
	if outcome.Disposition != githublifecycle.ReconciliationUnknown {
		t.Fatalf("generic error text proved non-application: %s", outcome.Disposition)
	}
}

func TestTask3PostMergeContainment(t *testing.T) {
	fixture := newProviderFixture(t, nil)
	result := observeAppliedResult(t, fixture)
	observeInput := mustValue(githublifecycle.NewObservePostMergeInput(fixture.sealed, result, fixture.limits))
	for name, test := range map[string]struct {
		tip, status, mergeBase string
		ahead, behind          int
		wantOK                 bool
	}{
		"identical":        {fixture.recipe.ExpectedResultSHA().String(), "identical", fixture.recipe.ExpectedResultSHA().String(), 0, 0, true},
		"ahead":            {strings.Repeat("5", 40), "ahead", fixture.recipe.ExpectedResultSHA().String(), 1, 0, true},
		"sibling":          {strings.Repeat("6", 40), "diverged", fixture.recipe.ExpectedResultSHA().String(), 0, 0, false},
		"behind":           {strings.Repeat("7", 40), "behind", fixture.recipe.ExpectedResultSHA().String(), 0, 1, false},
		"wrong-merge-base": {strings.Repeat("8", 40), "ahead", fixture.baseSHA.String(), 1, 0, false},
		"excessive":        {strings.Repeat("9", 40), "ahead", fixture.recipe.ExpectedResultSHA().String(), fixture.limits.MaxDescendantDistance + 1, 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			transport := postMergeTransport(t, fixture, test.tip, test.status, test.mergeBase, test.ahead, test.behind)
			provider := mustValue(newTestProvider(fixture.auth, fixture.limits, transport))
			outcome, err := provider.ObservePostMerge(context.Background(), observeInput)
			if test.wantOK {
				if err != nil || githublifecycle.VerifyPostMerge(fixture.sealed, result, outcome.Observation, fixture.limits) != nil {
					t.Fatalf("post-merge proof failed: %v calls=%+v", err, transport.snapshot())
				}
			} else if err == nil {
				t.Fatal("invalid containment proof was accepted")
			}
		})
	}
}

func TestTask3CumulativeProviderBudgets(t *testing.T) {
	limits := githublifecycle.DefaultLimits()
	budget := mergelifecycle.ProviderBudgetV1{RequestBytes: int64(limits.MaxRequestBodyBytes), HeaderBytes: 100,
		CompressedResponseBytes: 100, DecompressedResponseBytes: 100, ActiveNanos: 100}
	meter := &callMeter{budget: budget, started: time.Now()}
	if err := meter.beforeRequest(int64(limits.MaxRequestBodyBytes), limits); err != nil {
		t.Fatalf("exact request budget failed: %v", err)
	}
	if err := meter.beforeRequest(1, limits); err == nil {
		t.Fatal("cumulative request limit+1 was accepted")
	}
	for name, apply := range map[string]func(*callMeter) error{
		"headers":      func(value *callMeter) error { _ = value.addHeaders(100); return value.addHeaders(1) },
		"compressed":   func(value *callMeter) error { _ = value.addCompressed(100); return value.addCompressed(1) },
		"decompressed": func(value *callMeter) error { _ = value.addDecompressed(100); return value.addDecompressed(1) },
		"active":       func(value *callMeter) error { _ = value.addActive(100); return value.addActive(1) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := apply(&callMeter{budget: budget}); err == nil {
				t.Fatal("cumulative limit+1 was accepted")
			}
		})
	}
	meter = &callMeter{budget: mergelifecycle.ProviderBudgetV1{RequestBytes: 1 << 20, HeaderBytes: 1 << 20, CompressedResponseBytes: 1 << 20, DecompressedResponseBytes: 1 << 20, ActiveNanos: 1 << 20}}
	meter.calls = limits.MaxHTTPCalls
	if err := meter.beforeRequest(0, limits); err == nil {
		t.Fatal("cumulative call limit+1 was accepted")
	}
}

func TestTask3UnsupportedModesAndCapabilities(t *testing.T) {
	fixture := newProviderFixture(t, nil)
	compatibility := fixture.authority.Input()
	compatibility.ReadyBinding = githublifecycle.ReadyAuthorityBindingV1{}
	compatibility.MergePolicy = githublifecycle.MergePolicyV1{}
	compatibility.AllowedMergeMethod = githublifecycle.MergeMethodSquash
	squash := mustValue(githublifecycle.NewAuthority(compatibility))
	if err := fixture.provider.validateAuthority(squash); err == nil {
		t.Fatal("squash production execution was accepted")
	}
	compatibility.AllowedMergeMethod = githublifecycle.MergeMethodRebase
	rebase := mustValue(githublifecycle.NewAuthority(compatibility))
	if err := fixture.provider.validateAuthority(rebase); err == nil {
		t.Fatal("rebase production execution was accepted")
	}
	original := append([]byte(nil), embeddedCapabilityRecord...)
	defer func() { embeddedCapabilityRecord = original }()
	embeddedCapabilityRecord = bytes.Replace(original, []byte(`"supports_same_oid_no_op":true`), []byte(`"supports_same_oid_no_op":false`), 1)
	if err := fixture.provider.validateSealedCapability(fixture.sealed); err == nil {
		t.Fatal("missing no-op capability was accepted before mutation")
	}
}

func TestTask3ConcurrentMutationCeilings(t *testing.T) {
	fixture := newProviderFixture(t, nil)
	transport := &scriptTransport{handler: func(call recordedRequest) (*http.Response, error) {
		if call.Method == http.MethodPost && strings.Contains(call.URL, "/git/commits") {
			return githubResponse(201, fmt.Sprintf("create-%d", time.Now().UnixNano()), remoteCommitBody(t, fixture.recipe)), nil
		}
		if call.Method == http.MethodGet && strings.Contains(call.URL, "/git/commits/") {
			return githubResponse(200, fmt.Sprintf("observe-%d", time.Now().UnixNano()), remoteCommitBody(t, fixture.recipe)), nil
		}
		return nil, fmt.Errorf("unexpected concurrent commit request")
	}}
	provider := mustValue(newTestProvider(fixture.auth, fixture.limits, transport))
	var wait sync.WaitGroup
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() { defer wait.Done(); _, _ = provider.PrepareResultCommit(context.Background(), fixture.recipe) }()
	}
	wait.Wait()
	commitWrites := 0
	for _, call := range transport.snapshot() {
		if call.Method == http.MethodPost {
			commitWrites++
		}
	}
	if commitWrites != 1 {
		t.Fatalf("concurrent commit creation writes = %d", commitWrites)
	}
	transport = &scriptTransport{}
	appliedTargetScript(t, fixture, transport)
	provider = mustValue(newTestProvider(fixture.auth, fixture.limits, transport))
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _ = provider.SubmitTarget(context.Background(), targetExecution(t, fixture))
		}()
	}
	wait.Wait()
	targetWrites := 0
	for _, call := range transport.snapshot() {
		if call.Method == http.MethodPost && strings.HasSuffix(call.URL, "/graphql") {
			targetWrites++
		}
	}
	if targetWrites != 1 {
		t.Fatalf("concurrent target writes = %d", targetWrites)
	}
}

func authorizationProvider(t *testing.T, fixture providerFixture, mutate func(*authorizationQueryResponse), reviews []byte) (*Provider, *scriptTransport) {
	t.Helper()
	var sequence atomic.Int64
	transport := &scriptTransport{}
	transport.handler = func(call recordedRequest) (*http.Response, error) {
		id := fmt.Sprintf("auth-%d", sequence.Add(1))
		switch {
		case strings.HasSuffix(call.URL, "/graphql"):
			return githubResponse(200, id, authorizationBody(fixture, mutate)), nil
		case strings.Contains(call.URL, "/pulls/17/reviews"):
			return githubResponse(200, id, reviews), nil
		case strings.Contains(call.URL, "/check-runs"):
			return githubResponse(200, id, []byte(`{"check_runs":[]}`)), nil
		case strings.Contains(call.URL, "/statuses"):
			return githubResponse(200, id, []byte(`[]`)), nil
		default:
			return nil, fmt.Errorf("unexpected authorization request %s", call.URL)
		}
	}
	return mustValue(newTestProvider(fixture.auth, fixture.limits, transport)), transport
}

func observationRequestIDs(observation mergelifecycle.AuthorizationObservation) map[string]bool {
	ids := map[string]bool{observation.PullRequest.Input().Snapshot.RequestID(): true}
	for _, closure := range []githublifecycle.PaginationClosureV1{observation.PullRequest.Input().ReviewsClosure, observation.CheckRunsClosure, observation.CommitStatusClosure} {
		for _, page := range closure.Input().Pages {
			ids[page.Input().Response.RequestID()] = true
		}
	}
	return ids
}

func postMergeTransport(t *testing.T, fixture providerFixture, tip, status, mergeBase string, ahead, behind int) *scriptTransport {
	t.Helper()
	return &scriptTransport{handler: func(call recordedRequest) (*http.Response, error) {
		switch {
		case strings.Contains(call.URL, "/git/commits/"):
			return githubResponse(200, "post-object", remoteCommitBody(t, fixture.recipe)), nil
		case strings.Contains(call.URL, "/git/ref/heads/main"):
			body, _ := json.Marshal(gitRefResponse{Ref: "refs/heads/main", Object: struct {
				SHA  string `json:"sha"`
				Type string `json:"type"`
			}{tip, "commit"}})
			return githubResponse(200, "post-ref", body), nil
		case strings.Contains(call.URL, "/compare/"):
			comparison := compareResponse{Status: status, AheadBy: ahead, BehindBy: behind, TotalCommits: ahead}
			comparison.BaseCommit.SHA = fixture.recipe.ExpectedResultSHA().String()
			comparison.MergeBaseCommit.SHA = mergeBase
			body, _ := json.Marshal(comparison)
			return githubResponse(200, "post-compare", body), nil
		default:
			return nil, fmt.Errorf("unexpected post-merge request %s", call.URL)
		}
	}}
}

func cloneMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

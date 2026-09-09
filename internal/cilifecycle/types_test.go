package cilifecycle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

const (
	testSHA40 = "0123456789abcdef0123456789abcdef01234567"
	testSHA64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testTime  = "9999-12-31T23:59:59.999999999Z"
)

func ptr(s string) *string { return &s }

func TestObservationCanonicalReadersAndDefensiveCopies(t *testing.T) {
	conclusion := "success"
	suite, err := NewCheckSuiteObservationV1(CheckSuiteObservationV1Input{
		ProviderID: 7, ProviderNodeID: "CS_kw/+=", AppID: 9, HeadSHA: testSHA40,
		Status: "completed", Conclusion: &conclusion, CreatedAt: "2026-09-08T12:00:00Z", UpdatedAt: "2026-09-08T12:01:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	conclusion = "failure"
	if got := *suite.Input().Conclusion; got != "success" {
		t.Fatalf("constructor retained caller pointer: %q", got)
	}
	want := `{"provider_id":7,"provider_node_id":"CS_kw/+=","app_id":9,"head_sha":"0123456789abcdef0123456789abcdef01234567","status":"completed","conclusion":"success","created_at":"2026-09-08T12:00:00Z","updated_at":"2026-09-08T12:01:00Z"}`
	if got := string(suite.CanonicalJSON()); got != want {
		t.Fatalf("canonical suite mismatch\n got: %s\nwant: %s", got, want)
	}
	parsed, err := ReadCheckSuiteObservationV1(suite.CanonicalJSON())
	if err != nil || parsed.SHA256() != suite.SHA256() {
		t.Fatalf("strict suite round trip: %v", err)
	}
	withUnknown := append([]byte(nil), suite.CanonicalJSON()...)
	withUnknown = bytes.Replace(withUnknown, []byte(`"provider_id":7`), []byte(`"unknown":1,"provider_id":7`), 1)
	if _, err := ReadCheckSuiteObservationV1(withUnknown); err == nil {
		t.Fatal("strict reader accepted unknown field")
	}
	pretty, _ := json.MarshalIndent(json.RawMessage(suite.CanonicalJSON()), "", "  ")
	if _, err := ReadCheckSuiteObservationV1(pretty); err == nil {
		t.Fatal("strict reader accepted noncanonical whitespace")
	}
	if _, err := ReadCheckSuiteObservationV1(append(suite.CanonicalJSON(), []byte("\n{}")...)); err == nil {
		t.Fatal("strict reader accepted trailing value")
	}
}

func TestRawStateAndRelationshipValidation(t *testing.T) {
	baseSuite := CheckSuiteObservationV1Input{ProviderID: 1, AppID: 2, HeadSHA: testSHA40, Status: "queued", CreatedAt: "2026-09-08T12:00:00Z", UpdatedAt: "2026-09-08T12:00:00Z"}
	if _, err := NewCheckSuiteObservationV1(baseSuite); err != nil {
		t.Fatal(err)
	}
	baseSuite.Conclusion = ptr("success")
	if _, err := NewCheckSuiteObservationV1(baseSuite); err == nil {
		t.Fatal("queued suite accepted a conclusion")
	}
	baseSuite.Status, baseSuite.Conclusion = "completed", ptr("startup_failure")
	suite, err := NewCheckSuiteObservationV1(baseSuite)
	if err != nil {
		t.Fatalf("suite-only conclusion rejected: %v", err)
	}
	runInput := CheckRunObservationV1Input{ProviderID: 3, CheckSuiteID: 1, AppID: 2, Name: "Build", HeadSHA: testSHA40, Status: "completed", Conclusion: ptr("startup_failure"), CompletedAt: ptr("2026-09-08T12:01:00Z")}
	if _, err := NewCheckRunObservationV1(runInput); err == nil {
		t.Fatal("run accepted suite-only startup_failure")
	}
	runInput.Conclusion = ptr("failure")
	run, err := NewCheckRunObservationV1(runInput)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewCISemanticSweepV1(CISemanticSweepV1Input{RepositoryOwner: "Owner", RepositoryName: "Repo", HeadSHA: testSHA40, CheckSuiteTotalCount: 1, CheckRunTotalCount: 1, CheckSuites: []CheckSuiteObservationV1{suite}, CheckRuns: []CheckRunObservationV1{run}})
	if err != nil {
		t.Fatal(err)
	}
	runInput.AppID = 99
	badRun, _ := NewCheckRunObservationV1(runInput)
	if _, err := NewCISemanticSweepV1(CISemanticSweepV1Input{RepositoryOwner: "Owner", RepositoryName: "Repo", HeadSHA: testSHA40, CheckSuiteTotalCount: 1, CheckRunTotalCount: 1, CheckSuites: []CheckSuiteObservationV1{suite}, CheckRuns: []CheckRunObservationV1{badRun}}); err == nil {
		t.Fatal("sweep accepted app-inconsistent run relationship")
	}
}

func TestSweepSortsNumericallyPreservesCaseAndCopiesSlices(t *testing.T) {
	s1, _ := NewCheckSuiteObservationV1(CheckSuiteObservationV1Input{ProviderID: 1, ProviderNodeID: "one", AppID: 7, HeadSHA: testSHA40, Status: "queued", CreatedAt: "2026-09-08T12:00:00Z", UpdatedAt: "2026-09-08T12:00:00Z"})
	s2, _ := NewCheckSuiteObservationV1(CheckSuiteObservationV1Input{ProviderID: 2, ProviderNodeID: "two", AppID: 7, HeadSHA: testSHA40, Status: "queued", CreatedAt: "2026-09-08T12:00:00Z", UpdatedAt: "2026-09-08T12:00:00Z"})
	r1, _ := NewCheckRunObservationV1(CheckRunObservationV1Input{ProviderID: 4, CheckSuiteID: 1, AppID: 7, Name: "build", HeadSHA: testSHA40, Status: "pending"})
	r2, _ := NewCheckRunObservationV1(CheckRunObservationV1Input{ProviderID: 3, CheckSuiteID: 2, AppID: 7, Name: "Build", HeadSHA: testSHA40, Status: "pending"})
	statusA, _ := NewCommitStatusObservationV1(CommitStatusObservationV1Input{ProviderID: 6, State: "failure", Context: "lint", HeadSHA: testSHA40, CreatedAt: "2026-09-08T12:00:00Z", UpdatedAt: "2026-09-08T12:00:00Z", CreatorID: 8, CreatorLogin: "Bot", CreatorType: "App"})
	statusB, _ := NewCommitStatusObservationV1(CommitStatusObservationV1Input{ProviderID: 5, State: "success", Context: "Lint", HeadSHA: testSHA40, CreatedAt: "2026-09-08T12:00:00Z", UpdatedAt: "2026-09-08T12:00:00Z", CreatorID: 8, CreatorLogin: "Bot", CreatorType: "App"})
	suites := []CheckSuiteObservationV1{s2, s1}
	runs := []CheckRunObservationV1{r1, r2}
	statuses := []CommitStatusObservationV1{statusA, statusB}
	sweep, err := NewCISemanticSweepV1(CISemanticSweepV1Input{RepositoryOwner: "Owner", RepositoryName: "Repo", HeadSHA: testSHA40, CheckSuiteTotalCount: 2, CheckRunTotalCount: 2, CheckSuites: suites, CheckRuns: runs, CommitStatuses: statuses})
	if err != nil {
		t.Fatal(err)
	}
	suites[0] = CheckSuiteObservationV1{}
	runs[0] = CheckRunObservationV1{}
	statuses[0] = CommitStatusObservationV1{}
	in := sweep.Input()
	if in.CheckSuites[0].Input().ProviderID != 1 || in.CheckRuns[0].Input().ProviderID != 3 || in.CommitStatuses[0].Input().ProviderID != 5 {
		t.Fatal("sweep did not sort by numeric provider ID or retained caller slices")
	}
	if in.CheckRuns[0].Input().Name != "Build" || in.CheckRuns[1].Input().Name != "build" || in.CommitStatuses[0].Input().Context != "Lint" || in.CommitStatuses[1].Input().Context != "lint" {
		t.Fatal("case-distinct evidence was normalized")
	}
	parsed, err := ReadCISemanticSweepV1(sweep.CanonicalJSON())
	if err != nil || parsed.SHA256() != sweep.SHA256() {
		t.Fatalf("strict sweep round trip: %v", err)
	}
}

func TestProductionLimitsIdentityIsCompleteAndStable(t *testing.T) {
	b := ProductionLimitsCanonicalJSON()
	for _, field := range []string{"max_suites", "max_collection_requests", "max_response_body_bytes", "max_semantic_sweep_bytes", "max_attempts_global", "max_ledger_scan_bytes", "collection_timeout_nanos", "max_transport_retries"} {
		if !bytes.Contains(b, []byte(`"`+field+`"`)) {
			t.Fatalf("limits identity omits %s", field)
		}
	}
	if got := len(ProductionLimitsSHA256()); got != 64 {
		t.Fatalf("limits digest length = %d", got)
	}
	copyBytes := ProductionLimitsCanonicalJSON()
	copyBytes[0] = 'x'
	if bytes.Equal(copyBytes, ProductionLimitsCanonicalJSON()) {
		t.Fatal("production limits bytes were not defensively copied")
	}
}

func maxEscapedText(i int) string {
	prefix := fmt.Sprintf("%03d<>&\"\\\u2028\u2029", i)
	return prefix + strings.Repeat("&", MaxTextBytes-len(prefix))
}

func maxProfile(t *testing.T) CISemanticSweepV1 {
	t.Helper()
	suites := make([]CheckSuiteObservationV1, MaxSuites)
	runs := make([]CheckRunObservationV1, MaxRuns)
	statuses := make([]CommitStatusObservationV1, MaxStatuses)
	for i := 0; i < MaxSuites; i++ {
		text := maxEscapedText(i)
		var err error
		suites[i], err = NewCheckSuiteObservationV1(CheckSuiteObservationV1Input{ProviderID: int64(i + 1), ProviderNodeID: text, AppID: math.MaxInt64, HeadSHA: testSHA64, Status: "completed", Conclusion: ptr("startup_failure"), CreatedAt: testTime, UpdatedAt: testTime})
		if err != nil {
			t.Fatalf("maximum suite %d: %v", i, err)
		}
		runs[i], err = NewCheckRunObservationV1(CheckRunObservationV1Input{ProviderID: int64(i + 1), ProviderNodeID: text, CheckSuiteID: int64(i + 1), AppID: math.MaxInt64, Name: text, HeadSHA: testSHA64, Status: "completed", Conclusion: ptr("action_required"), StartedAt: ptr(testTime), CompletedAt: ptr(testTime)})
		if err != nil {
			t.Fatalf("maximum run %d: %v", i, err)
		}
		statuses[i], err = NewCommitStatusObservationV1(CommitStatusObservationV1Input{ProviderID: int64(i + 1), ProviderNodeID: text, State: "failure", Context: text, Description: ptr(text), HeadSHA: testSHA64, CreatedAt: testTime, UpdatedAt: testTime, CreatorID: math.MaxInt64, CreatorNodeID: text, CreatorLogin: text, CreatorType: text})
		if err != nil {
			t.Fatalf("maximum status %d: %v", i, err)
		}
	}
	if len(suites[0].CanonicalJSON()) > MaxSuiteObjectBytes || len(runs[0].CanonicalJSON()) > MaxRunObjectBytes || len(statuses[0].CanonicalJSON()) > MaxStatusObjectBytes {
		t.Fatal("maximum observation exceeds documented object cap")
	}
	sweep, err := NewCISemanticSweepV1(CISemanticSweepV1Input{RepositoryOwner: strings.Repeat("O", 100), RepositoryName: strings.Repeat("R", 100), HeadSHA: testSHA64, CheckSuiteTotalCount: MaxSuites, CheckRunTotalCount: MaxRuns, CheckSuites: suites, CheckRuns: runs, CommitStatuses: statuses})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(sweep.CanonicalJSON()); got > MaxSemanticSweepBytes {
		t.Fatalf("maximum sweep = %d, cap = %d", got, MaxSemanticSweepBytes)
	}
	return sweep
}

func TestExactMaximumEscapingAndAggregateProfiles(t *testing.T) {
	sweep := maxProfile(t)
	for _, escape := range []string{`\u003c`, `\u003e`, `\u0026`, `\"`, `\\`, `\u2028`, `\u2029`} {
		if !bytes.Contains(sweep.CanonicalJSON(), []byte(escape)) {
			t.Fatalf("maximum profile lacks production JSON escape %q", escape)
		}
	}
	tooMany := append(sweep.Input().CheckSuites, sweep.Input().CheckSuites[0])
	in := sweep.Input()
	in.CheckSuites, in.CheckSuiteTotalCount = tooMany, len(tooMany)
	if _, err := NewCISemanticSweepV1(in); err == nil {
		t.Fatal("boundary cardinality +1 was accepted")
	}

	heads := make([]HeadObservationV1, 3)
	for i, phase := range []string{"H0", "H1", "H2"} {
		heads[i], _ = NewHeadObservationV1(HeadObservationV1Input{Phase: phase, Ref: strings.Repeat("r", MaxTextBytes), ObjectType: "commit", SHA: testSHA64, RequestSequence: i + 2, ResponseObservedUnixNano: int64(i + 3)})
	}
	provenance := make([]RequestProvenanceV1, MaxCollectionRequests)
	for i := range provenance {
		path := strings.Repeat("&", 300)
		short := strings.Repeat("&", 50)
		var err error
		provenanceInput := RequestProvenanceV1Input{Sequence: i + 1, Phase: "statuses_b", Page: 5, Method: "GET", PathTemplate: path, EscapedPath: path, CanonicalQuery: short, APIOrigin: short, APIVersion: short, Accept: short, HTTPStatus: 200, ResponseBodySHA256: testSHA64, RequestID: maxEscapedText(i), RequestStartedUnixNano: int64(i + 1), ResponseObservedUnixNano: int64(i + 2), ResponseBytes: MaxResponseBodyBytes}
		provenanceInput.RequestSHA256 = requestIdentitySHA256(provenanceInput)
		provenanceInput.ResponseEnvelopeSHA256 = responseEnvelopeSHA256(provenanceInput)
		provenance[i], err = NewRequestProvenanceV1(provenanceInput)
		if err != nil {
			t.Fatalf("maximum provenance %d: %v", i, err)
		}
		if len(provenance[i].CanonicalJSON()) > MaxProvenanceBytes {
			t.Fatalf("provenance %d exceeds cap", i)
		}
	}
	digest := sweep.SHA256()
	chain, links, err := requestResponseChain(provenance)
	if err != nil {
		t.Fatal(err)
	}
	identity := collectionIdentitySHA256(strings.Repeat("O", 100), strings.Repeat("R", 100), testSHA64, digest, links)
	runID, attemptID := strings.Repeat("r", MaxTextBytes), strings.Repeat("a", MaxAttemptIDBytes)
	owner, repository, branch := strings.Repeat("O", 100), strings.Repeat("R", 100), strings.Repeat("h", 255)
	actingKind, actingSubject := "user", "github-user-id:9223372036854775807"
	attemptKey := deriveAttemptKey(runID, attemptID, testSHA64, owner, repository, branch, testSHA64, actingKind, actingSubject)
	for i := range heads {
		headInput := heads[i].Input()
		headInput.Ref = "refs/heads/" + branch
		heads[i], err = NewHeadObservationV1(headInput)
		if err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := NewCIEvidenceBundleV1(CIEvidenceBundleV1Input{RunID: runID, AttemptID: attemptID, AttemptKeySHA256: attemptKey, AuthoritySHA256: testSHA64, LimitsSHA256: ProductionLimitsSHA256(), RepositoryOwner: owner, RepositoryName: repository, HeadBranch: branch, HeadSHA: testSHA64, ActingKind: actingKind, ActingSubject: actingSubject, AuthenticatedID: math.MaxInt64, AuthenticatedNode: maxEscapedText(1), AuthenticatedLogin: maxEscapedText(2), Outcome: OutcomeStable, AttemptStartedUnixNano: 1, AttemptEndedUnixNano: 100, CollectionStartedUnixNano: 2, CollectionEndedUnixNano: 99, FirstResponseObservedUnixNano: 2, LastResponseObservedUnixNano: 31, EarliestProviderStateAt: testTime, LatestProviderStateAt: testTime, HeadObservations: heads, SweepA: &sweep, SweepB: &sweep, SemanticDigestA: digest, SemanticDigestB: digest, RequestProvenance: provenance, RequestResponseChainSHA256: chain, CollectionIdentitySHA256: identity})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(bundle.CanonicalJSON()); got > MaxDerivedBundleProfile || got > MaxBundleBytes {
		t.Fatalf("maximum derived bundle = %d, derived cap = %d, hard cap = %d", got, MaxDerivedBundleProfile, MaxBundleBytes)
	}
	parsed, err := ReadCIEvidenceBundleV1(bundle.CanonicalJSON())
	if err != nil || parsed.SHA256() != bundle.SHA256() {
		t.Fatalf("maximum bundle strict round trip: %v", err)
	}
}

func TestDuplicateAndConflictingProviderIdentities(t *testing.T) {
	a, _ := NewCheckSuiteObservationV1(CheckSuiteObservationV1Input{ProviderID: 1, ProviderNodeID: "same", AppID: 2, HeadSHA: testSHA40, Status: "queued", CreatedAt: "2026-09-08T12:00:00Z", UpdatedAt: "2026-09-08T12:00:00Z"})
	b, _ := NewCheckSuiteObservationV1(CheckSuiteObservationV1Input{ProviderID: 2, ProviderNodeID: "same", AppID: 2, HeadSHA: testSHA40, Status: "queued", CreatedAt: "2026-09-08T12:00:00Z", UpdatedAt: "2026-09-08T12:00:00Z"})
	_, err := NewCISemanticSweepV1(CISemanticSweepV1Input{RepositoryOwner: "o", RepositoryName: "r", HeadSHA: testSHA40, CheckSuiteTotalCount: 2, CheckSuites: []CheckSuiteObservationV1{a, b}})
	if err == nil {
		t.Fatal("conflicting provider node identity accepted")
	}
	_, err = NewCISemanticSweepV1(CISemanticSweepV1Input{RepositoryOwner: "o", RepositoryName: "r", HeadSHA: testSHA40, CheckSuiteTotalCount: 2, CheckSuites: []CheckSuiteObservationV1{a, a}})
	if err == nil {
		t.Fatal("duplicate numeric provider identity accepted")
	}
}

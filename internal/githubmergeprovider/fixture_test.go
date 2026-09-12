package githubmergeprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

type providerFixture struct {
	limits     githublifecycle.Limits
	auth       *Authenticator
	provider   *Provider
	repository githublifecycle.Repository
	base       githublifecycle.Branch
	head       githublifecycle.Branch
	baseSHA    githublifecycle.GitSHA
	headSHA    githublifecycle.GitSHA
	treeSHA    githublifecycle.GitSHA
	pr         githublifecycle.PullRequestIdentity
	actor      githublifecycle.ActingIdentity
	authority  githublifecycle.Authority
	recipe     githublifecycle.MergeCommitRecipeV1
	sealed     githublifecycle.SealedMergeAuthorizationV1
	submission githublifecycle.TargetSubmissionV1
}

func newProviderFixture(t *testing.T, eligible []githublifecycle.StableIdentityV1) providerFixture {
	t.Helper()
	f := providerFixture{limits: githublifecycle.DefaultLimits()}
	f.auth = mustValue(NewUserAuthenticator("test-secret-token", "U_actor"))
	f.provider = mustValue(newTestProvider(f.auth, f.limits, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("unexpected fixture transport call")
	})))
	f.repository = mustValue(githublifecycle.NewRepository("octo-org", "control-plane"))
	f.base = mustValue(githublifecycle.NewBranch("main"))
	f.head = mustValue(githublifecycle.NewBranch("feature/exact-head"))
	f.baseSHA = mustValue(githublifecycle.NewGitSHA(strings.Repeat("3", 40)))
	f.headSHA = mustValue(githublifecycle.NewGitSHA(strings.Repeat("1", 40)))
	f.treeSHA = mustValue(githublifecycle.NewGitSHA(strings.Repeat("2", 40)))
	f.pr = mustValue(githublifecycle.NewPullRequestIdentity(17, "PR_node_17"))
	f.actor = mustValue(githublifecycle.NewUserIdentity("U_actor"))
	readyRef := ledger.EvidenceRef{URI: "evidence/ready.json", Kind: "serial-integration-gate-decision", SHA256: strings.Repeat("d", 64)}
	expectedWire := struct {
		DerivationPolicy string             `json:"derivation_policy"`
		SourceHead       string             `json:"source_integrated_head_sha"`
		SourceBase       string             `json:"source_baseline_sha"`
		SourceEvidence   ledger.EvidenceRef `json:"source_integration_evidence"`
		Git              struct {
			Path         string `json:"path"`
			Version      string `json:"version"`
			BinarySHA256 string `json:"binary_sha256"`
		} `json:"pinned_git"`
		ExpectedTree string `json:"expected_result_tree_sha"`
	}{DerivationPolicy: githublifecycle.ExpectedMergeContentPolicy, SourceHead: f.headSHA.String(), SourceBase: f.baseSHA.String(), SourceEvidence: readyRef, ExpectedTree: f.treeSHA.String()}
	expectedWire.Git.Path = "/usr/bin/git"
	expectedWire.Git.Version = "git version test"
	expectedWire.Git.BinarySHA256 = strings.Repeat("e", 64)
	expectedBytes := mustValue(json.Marshal(expectedWire))
	expected := mustValue(githublifecycle.ParseCanonicalExpectedMergeContent(expectedBytes))
	configRef := ledger.EvidenceRef{URI: "evidence/repository.json", Kind: "repository-binding", SHA256: strings.Repeat("a", 64)}
	repositoryBinding := mustValue(githublifecycle.NewRepositoryBindingV1(githublifecycle.RepositoryBindingV1Input{
		Phase3RepositoryIdentity: "repo-id", Phase3RepositoryPath: "/work/repo", Phase3CanonicalRemote: "https://github.com/octo-org/control-plane",
		Phase3StartSHA: f.baseSHA, GitHubRepository: f.repository, GitHubRepositoryNodeID: "R_repo",
		GitHubRepositoryDatabaseID: 99, ConfigurationEvidence: configRef,
	}))
	phase3JSON := mustValue(json.Marshal(map[string]any{
		"run_id": "run-1", "repository": map[string]any{"path": "/work/repo", "identity": "repo-id", "remotes": map[string]string{"origin": "https://github.com/octo-org/control-plane"}, "start_sha": f.baseSHA.String()},
		"plan": map[string]string{"path": "/work/repo/plan.md", "sha256": strings.Repeat("b", 64)}, "policy_version": "phase3-policy-v1",
	}))
	readyEvent := ledger.Event{SchemaVersion: 1, EventID: "ready-event-1", Timestamp: time.Unix(1700000000, 1).UTC(),
		ProjectID: "project-1", PlanID: "plan-1", RunID: "run-1", AttemptID: "attempt-1", EventType: "STATE_TRANSITION",
		StateFrom: domain.StateIntegrationAccepted, StateTo: domain.StateReadyForMerge, Actor: "controller", Source: "integration-gate", EvidenceRefs: []ledger.EvidenceRef{readyRef}}
	readyEventJSON := mustValue(json.Marshal(readyEvent))
	states := []domain.State{domain.StateRunCreated, domain.StateAuthorityValidated, domain.StateExecutionStarting, domain.StateImplementing,
		domain.StateImplementationCompleted, domain.StateBranchAcceptancePending, domain.StateBranchAccepted, domain.StateIntegrationPending,
		domain.StateIntegrating, domain.StateIntegrationAccepted, domain.StateReadyForMerge}
	var ledgerPrefix []byte
	var readyOffset int64
	for index := 0; index < len(states)-1; index++ {
		event := ledger.Event{SchemaVersion: 1, EventID: fmt.Sprintf("transition-event-%02d", index+1), Timestamp: time.Unix(1699999900+int64(index), 1).UTC(),
			ProjectID: "project-1", PlanID: "plan-1", RunID: "run-1", AttemptID: "attempt-1", EventType: "STATE_TRANSITION",
			StateFrom: states[index], StateTo: states[index+1], Actor: "controller", Source: "fixture"}
		if event.StateTo == domain.StateReadyForMerge {
			event = readyEvent
			readyOffset = int64(len(ledgerPrefix))
		}
		ledgerPrefix = append(ledgerPrefix, mustValue(json.Marshal(event))...)
		ledgerPrefix = append(ledgerPrefix, '\n')
	}
	readySequence := int64(len(states) - 1)
	ready := mustValue(githublifecycle.NewReadyAuthorityBindingV1(githublifecycle.ReadyAuthorityBindingV1Input{
		Phase3AuthorityJSON: phase3JSON, Phase3AuthoritySHA256: digest(phase3JSON), ProjectID: "project-1", PlanID: "plan-1", RunID: "run-1", AttemptID: "attempt-1",
		AcceptedSources: []githublifecycle.AcceptedSourceCandidateV1{{ProjectID: "source-project", PlanID: "source-plan", RunID: "source-run", AttemptID: "source-attempt",
			RepositoryIdentity: "repo-id", Branch: f.head.String(), StartSHA: f.baseSHA.String(), AcceptedHeadSHA: f.headSHA.String(), AcceptancePolicyIdentity: "accept-v1", AcceptanceEvidence: []ledger.EvidenceRef{readyRef}}},
		RepositoryBinding: repositoryBinding, ReadyEventJSON: readyEventJSON, ReadyEventSHA256: digest(readyEventJSON), ReadyEventID: readyEvent.EventID,
		ReadyEventUnixNano: readyEvent.Timestamp.UnixNano(), LedgerIdentity: "ledger-dev-ino", ReadyEventByteOffset: readyOffset,
		ReadyRunStateSequence: readySequence, LedgerPrefixLength: int64(len(ledgerPrefix)), LedgerPrefixSHA256: digest(ledgerPrefix), ReadyTransitionOrdinal: readySequence,
		ReadyEvidenceRefs: []ledger.EvidenceRef{readyRef}, ReadyDecisionRef: readyRef, EvidenceClosureRefs: []ledger.EvidenceRef{readyRef, configRef},
		IntegratedHeadSHA: f.headSHA, BaselineSHA: f.baseSHA, ExpectedTreeSHA: f.treeSHA,
	}, f.limits))
	policySource := ledger.EvidenceRef{URI: "evidence/merge-policy.json", Kind: "merge-policy", SHA256: strings.Repeat("f", 64)}
	policyAuthority := mustValue(githublifecycle.NewPolicyAuthorityBindingV1(githublifecycle.PolicyAuthorityBindingV1Input{
		SourceConfiguration: policySource, RepositoryBindingSHA256: repositoryBinding.SHA256(), Phase3AuthoritySHA256: digest(phase3JSON),
		ReadyBindingSHA256: ready.SHA256(), RequiredActingPrincipal: f.actor,
	}))
	commitIdentity := githublifecycle.MergeCommitIdentityV1{Name: "ABCP", Email: "abcp@example.com", Timezone: "+0000"}
	policy := mustValue(githublifecycle.NewMergePolicyV1(githublifecycle.MergePolicyV1Input{
		PolicyVersion: "merge-v1", AuthorityBinding: policyAuthority, Method: githublifecycle.MergeMethodMerge,
		EligibleReviewers: eligible, MinimumApprovals: 0,
		Recipe: githublifecycle.MergeCommitRecipePolicyV1{MessageTemplate: "Merge authorized head", TrailerTemplate: "ABCP-Write-ID",
			Author: commitIdentity, Committer: commitIdentity, TimestampDerivation: "ready-event-time", ObjectFormat: "sha1", OrderedParents: true},
	}, f.limits))
	f.authority = mustValue(githublifecycle.NewAuthority(githublifecycle.AuthorityInput{
		Repository: f.repository, BaseBranch: f.base, HeadBranch: f.head, HeadSHA: f.headSHA, ExpectedBaseTipSHA: f.baseSHA,
		PullRequest: &f.pr, AllowedMergeMethod: githublifecycle.MergeMethodMerge, Actor: f.actor, ExpectedContent: expected,
		ReadyBinding: ready, MergePolicy: policy,
	}))
	initialReviews := emptyClosure(t, f, githublifecycle.PaginationReviews, &f.pr, "reviews-initial", 1700000000100000000)
	initialChecks := emptyClosure(t, f, githublifecycle.PaginationCheckRuns, nil, "checks-initial", 1700000000200000000)
	initialStatuses := emptyClosure(t, f, githublifecycle.PaginationCommitStatuses, nil, "statuses-initial", 1700000000300000000)
	initialPR := authoritativePR(t, f, initialReviews, nil, "pr-initial", 1700000000400000000)
	capability := mustValue(f.provider.Capability("R_repo"))
	f.recipe = mustValue(githublifecycle.NewMergeCommitRecipeV1("merge-write-1", f.authority, f.limits))
	approval := ledger.EvidenceRef{URI: "evidence/approval", Kind: "approval", SHA256: strings.Repeat("8", 64)}
	mergeInput := mustValue(githublifecycle.NewMergeInput(githublifecycle.MergeAuthorizationInputV1{
		Authority: f.authority, PolicyDecisionSHA256: strings.Repeat("9", 64), InitialPullRequest: initialPR,
		CheckRunsClosure: initialChecks, CommitStatusesClosure: initialStatuses, Capability: capability, Recipe: f.recipe,
		EvidenceRefs: []ledger.EvidenceRef{approval},
	}, "merge-write-1", f.limits))
	readyInput := ready.Input()
	ledgerEvidence := ledger.EvidenceRef{URI: "evidence/current-ready-ledger", Kind: githublifecycle.CurrentReadyLedgerEvidenceKindV1, SHA256: digest(ledgerPrefix)}
	readyProof := mustValue(githublifecycle.NewCurrentReadyProofV1(githublifecycle.CurrentReadyProofV1Input{
		ReadyBinding: ready, ControllerSequence: 10, ObservedUnixNano: 1700000001000000000, ObservedLedgerIdentity: readyInput.LedgerIdentity,
		ObservedBoundPrefixSHA256: readyInput.LedgerPrefixSHA256, ObservedLedgerLength: int64(len(ledgerPrefix)), ObservedLedgerSHA256: digest(ledgerPrefix),
		ObservedLedgerJSONL: ledgerPrefix, NoLaterTransition: true, EvidenceRefs: []ledger.EvidenceRef{ledgerEvidence},
	}, f.limits))
	finalReviews := emptyClosure(t, f, githublifecycle.PaginationReviews, &f.pr, "reviews-final", 1700000002200000000)
	finalChecks := emptyClosure(t, f, githublifecycle.PaginationCheckRuns, nil, "checks-final", 1700000002300000000)
	finalStatuses := emptyClosure(t, f, githublifecycle.PaginationCommitStatuses, nil, "statuses-final", 1700000002400000000)
	finalPR := authoritativePR(t, f, finalReviews, nil, "pr-final", 1700000002500000000)
	finalEvidence := []ledger.EvidenceRef{approval, ledgerEvidence}
	finalEvidence = append(finalEvidence, finalPR.Input().EvidenceRefs...)
	finalEvidence = append(finalEvidence, finalReviews.Input().EvidenceRefs...)
	finalEvidence = append(finalEvidence, finalChecks.Input().EvidenceRefs...)
	finalEvidence = append(finalEvidence, finalStatuses.Input().EvidenceRefs...)
	admissionPages := len(initialReviews.Input().Pages) + len(initialChecks.Input().Pages) + len(initialStatuses.Input().Pages)
	finalPages := len(finalReviews.Input().Pages) + len(finalChecks.Input().Pages) + len(finalStatuses.Input().Pages)
	admissionBytes := int64(len(initialReviews.CanonicalJSON()) + len(initialChecks.CanonicalJSON()) + len(initialStatuses.CanonicalJSON()))
	finalBytes := int64(len(finalReviews.CanonicalJSON()) + len(finalChecks.CanonicalJSON()) + len(finalStatuses.CanonicalJSON()))
	counters := githublifecycle.AuthorizationCountersV1{
		AdmissionHTTPCalls: admissionPages + 1, AdmissionPaginationSources: 3, AdmissionPaginationPages: admissionPages,
		AdmissionPaginationClosureBytes: admissionBytes, FinalRevalidationHTTPCalls: finalPages + 1,
		FinalPaginationSources: 3, FinalPaginationPages: finalPages, FinalPaginationClosureBytes: finalBytes,
		ReadyLedgerBytes: int64(len(ledgerPrefix)), ReadyLedgerRecords: bytes.Count(ledgerPrefix, []byte{'\n'}),
		PreSubmitHTTPCalls: admissionPages + finalPages + 2, TotalHTTPCalls: admissionPages + finalPages + 2,
		ControllerInvocationNanos: 1_000_000_000,
	}
	final := mustValue(githublifecycle.NewFinalRevalidationV1(githublifecycle.FinalRevalidationV1Input{
		MergeInput: mergeInput, ControllerSequence: 11, StartedUnixNano: 1700000002000000000, CompletedUnixNano: 1700000003000000000,
		CurrentReadyProof: readyProof, PullRequest: finalPR, CheckRunsClosure: finalChecks, CommitStatusesClosure: finalStatuses,
		Capability: capability, Recipe: f.recipe, Counters: counters, NoTargetRequestAttempted: true, EvidenceRefs: finalEvidence,
	}, f.limits))
	seal := mustValue(githublifecycle.NewAuthorizationSealV1(githublifecycle.AuthorizationSealV1Input{MergeInput: mergeInput, FinalRevalidation: final}, f.limits))
	commitment := mustValue(githublifecycle.NewTargetRefCommitmentV1(mergeInput, seal, f.limits))
	f.sealed = mustValue(githublifecycle.NewSealedMergeAuthorizationV1(githublifecycle.SealedMergeAuthorizationV1Input{MergeInput: mergeInput, Seal: seal, Commitment: commitment}, f.limits))
	f.submission = mustValue(githublifecycle.NewTargetSubmissionV1("target-invocation-1", f.sealed, f.limits))
	return f
}

func emptyClosure(t *testing.T, f providerFixture, source githublifecycle.PaginationSourceKind, pr *githublifecycle.PullRequestIdentity, requestID string, observed int64) githublifecycle.PaginationClosureV1 {
	t.Helper()
	query := mustValue(githublifecycle.DerivePaginationQueryV1(githublifecycle.PaginationQueryScopeV1{
		Source: source, Repository: f.repository, RepositoryNodeID: "R_repo", PullRequest: pr, HeadSHA: f.headSHA,
	}, f.limits))
	snapshot := mustValue(githublifecycle.NewSnapshotIdentity("github", requestID, observed))
	bodyRef := ledger.EvidenceRef{URI: "evidence/" + requestID + "-body", Kind: githublifecycle.GitHubPaginationBodyEvidenceKindV1, SHA256: strings.Repeat("7", 64)}
	pageInput := githublifecycle.PaginationPageV1Input{Query: query, Ordinal: 0, RequestedPage: 1, Response: snapshot,
		RawBodySHA256: bodyRef.SHA256, ResponseEvidence: bodyRef, RESTLinkObserved: true}
	pageInput.EnvelopeEvidence = mustValue(githublifecycle.NewPaginationEnvelopeEvidenceV1("evidence/"+requestID+"-envelope", pageInput, f.limits))
	page := mustValue(githublifecycle.NewPaginationPageV1(pageInput, f.limits))
	return mustValue(githublifecycle.NewPaginationClosureV1(githublifecycle.PaginationClosureV1Input{
		Query: query, Pages: []githublifecycle.PaginationPageV1{page}, EvidenceRefs: []ledger.EvidenceRef{bodyRef, pageInput.EnvelopeEvidence},
	}, f.limits))
}

func authoritativePR(t *testing.T, f providerFixture, reviews githublifecycle.PaginationClosureV1, values []githublifecycle.Review, requestID string, observed int64) githublifecycle.AuthoritativePullRequestSnapshotV1 {
	t.Helper()
	open, no := githublifecycle.PullRequestOpen, false
	body := []byte(`{"fixture":"pull-request"}`)
	bodyRef := evidence("evidence/"+requestID+"-body", githublifecycle.GitHubPullRequestResponseEvidenceKindV1, body)
	input := githublifecycle.AuthoritativePullRequestSnapshotV1Input{
		Snapshot: mustValue(githublifecycle.NewSnapshotIdentity("github", requestID, observed)), ResponseBodySHA256: digest(body),
		APIVersion: apiVersion, RepositoryBinding: f.authority.ReadyBinding().RepositoryBinding(), PullRequest: f.pr, PullRequestDatabaseID: 17,
		BaseRepositoryNodeID: "R_repo", BaseRef: "refs/heads/" + f.base.String(), BaseOID: f.baseSHA,
		HeadRepositoryNodeID: "R_repo", HeadRef: "refs/heads/" + f.head.String(), HeadOID: f.headSHA,
		State: &open, IsDraft: &no, Merged: &no, Actor: f.actor, Reviews: values, ReviewsClosure: reviews,
	}
	setPREvidence(t, &input, bodyRef)
	return mustValue(githublifecycle.NewAuthoritativePullRequestSnapshotV1(input, f.limits))
}

func setPREvidence(t *testing.T, input *githublifecycle.AuthoritativePullRequestSnapshotV1Input, bodyRef ledger.EvidenceRef) {
	t.Helper()
	envelope := mustValue(githublifecycle.NewPullRequestEnvelopeEvidenceV1("evidence/"+input.Snapshot.RequestID()+"-envelope", *input, githublifecycle.DefaultLimits()))
	input.EvidenceRefs = []ledger.EvidenceRef{bodyRef, envelope}
}

func mustValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type recordedRequest struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

type scriptTransport struct {
	mu      sync.Mutex
	handler func(recordedRequest) (*http.Response, error)
	calls   []recordedRequest
}

func (s *scriptTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	call := recordedRequest{Method: request.Method, URL: request.URL.String(), Header: request.Header.Clone(), Body: body}
	s.mu.Lock()
	s.calls = append(s.calls, call)
	handler := s.handler
	s.mu.Unlock()
	return handler(call)
}

func (s *scriptTransport) snapshot() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedRequest(nil), s.calls...)
}

type closeTrackingBody struct {
	io.Reader
	mu     sync.Mutex
	closed bool
	err    error
}

func (b *closeTrackingBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return b.err
}

func (b *closeTrackingBody) wasClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

func githubResponse(status int, requestID string, body []byte) *http.Response {
	header := make(http.Header)
	if requestID != "" {
		header["X-Github-Request-Id"] = []string{requestID}
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(body))}
}

func authorizationBody(f providerFixture, mutate func(*authorizationQueryResponse)) []byte {
	var response authorizationQueryResponse
	response.Data.Viewer.ID = f.actor.Subject()
	repository := struct {
		ID          string `json:"id"`
		DatabaseID  int64  `json:"databaseId"`
		PullRequest *struct {
			ID             string  `json:"id"`
			DatabaseID     int64   `json:"databaseId"`
			Number         int64   `json:"number"`
			State          string  `json:"state"`
			IsDraft        *bool   `json:"isDraft"`
			Merged         *bool   `json:"merged"`
			MergedAt       *string `json:"mergedAt"`
			BaseRefName    string  `json:"baseRefName"`
			BaseRefOID     string  `json:"baseRefOid"`
			BaseRepository *struct {
				ID string `json:"id"`
			} `json:"baseRepository"`
			HeadRefName    string `json:"headRefName"`
			HeadRefOID     string `json:"headRefOid"`
			HeadRepository *struct {
				ID string `json:"id"`
			} `json:"headRepository"`
		} `json:"pullRequest"`
	}{ID: "R_repo", DatabaseID: 99}
	no := false
	repository.PullRequest = &struct {
		ID             string  `json:"id"`
		DatabaseID     int64   `json:"databaseId"`
		Number         int64   `json:"number"`
		State          string  `json:"state"`
		IsDraft        *bool   `json:"isDraft"`
		Merged         *bool   `json:"merged"`
		MergedAt       *string `json:"mergedAt"`
		BaseRefName    string  `json:"baseRefName"`
		BaseRefOID     string  `json:"baseRefOid"`
		BaseRepository *struct {
			ID string `json:"id"`
		} `json:"baseRepository"`
		HeadRefName    string `json:"headRefName"`
		HeadRefOID     string `json:"headRefOid"`
		HeadRepository *struct {
			ID string `json:"id"`
		} `json:"headRepository"`
	}{ID: f.pr.NodeID(), DatabaseID: 17, Number: f.pr.Number(), State: "OPEN", IsDraft: &no, Merged: &no,
		BaseRefName: f.base.String(), BaseRefOID: f.baseSHA.String(), BaseRepository: &struct {
			ID string `json:"id"`
		}{"R_repo"},
		HeadRefName: f.head.String(), HeadRefOID: f.headSHA.String(), HeadRepository: &struct {
			ID string `json:"id"`
		}{"R_repo"}}
	response.Data.Repository = &repository
	if mutate != nil {
		mutate(&response)
	}
	body, _ := json.Marshal(response)
	return body
}

func remoteCommitBody(t *testing.T, recipe githublifecycle.MergeCommitRecipeV1) []byte {
	t.Helper()
	input := recipe.Input()
	remote := gitCommitResponse{SHA: input.ExpectedResultSHA.String(), Message: input.Message}
	remote.Tree.SHA = input.ExpectedResultTree.String()
	remote.Parents = make([]struct {
		SHA string `json:"sha"`
	}, len(input.Parents))
	for index := range input.Parents {
		remote.Parents[index].SHA = input.Parents[index].String()
	}
	remote.Author.Name, remote.Author.Email = input.Author.Name, input.Author.Email
	remote.Committer.Name, remote.Committer.Email = input.Committer.Name, input.Committer.Email
	remote.Author.Date = mustValue(githubDate(input.AuthorUnix, input.Author.Timezone))
	remote.Committer.Date = mustValue(githubDate(input.CommitterUnix, input.Committer.Timezone))
	return mustValue(json.Marshal(remote))
}

func targetExecution(t *testing.T, f providerFixture) githublifecycle.MergeExecutionInputV1 {
	t.Helper()
	return mustValue(githublifecycle.NewMergeExecutionInputV1(f.sealed, f.submission, f.limits))
}

func appliedTargetScript(t *testing.T, f providerFixture, transport *scriptTransport) {
	t.Helper()
	transport.handler = func(call recordedRequest) (*http.Response, error) {
		switch {
		case call.Method == http.MethodPost && strings.HasSuffix(call.URL, "/graphql"):
			return githubResponse(200, "target-request", []byte(`{"data":{"updateRefs":{"clientMutationId":"merge-write-1"}}}`)), nil
		case strings.Contains(call.URL, "/git/ref/heads/main"):
			body, _ := json.Marshal(gitRefResponse{Ref: "refs/heads/main", Object: struct {
				SHA  string `json:"sha"`
				Type string `json:"type"`
			}{f.recipe.ExpectedResultSHA().String(), "commit"}})
			return githubResponse(200, "base-ref-request", body), nil
		case strings.Contains(call.URL, "/git/ref/heads/feature/exact-head"):
			body, _ := json.Marshal(gitRefResponse{Ref: "refs/heads/feature/exact-head", Object: struct {
				SHA  string `json:"sha"`
				Type string `json:"type"`
			}{f.headSHA.String(), "commit"}})
			return githubResponse(200, "head-ref-request", body), nil
		case strings.Contains(call.URL, "/git/commits/"):
			return githubResponse(200, "result-object-request", remoteCommitBody(t, f.recipe)), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", call.Method, call.URL)
		}
	}
}

func observeAppliedResult(t *testing.T, f providerFixture) githublifecycle.MergeResult {
	t.Helper()
	transport := &scriptTransport{}
	appliedTargetScript(t, f, transport)
	provider := mustValue(newTestProvider(f.auth, f.limits, transport))
	outcome := mustValue(provider.SubmitTarget(context.Background(), targetExecution(t, f)))
	if outcome.Disposition != githublifecycle.ReconciliationApplied {
		t.Fatalf("target disposition = %s", outcome.Disposition)
	}
	return outcome.Result
}

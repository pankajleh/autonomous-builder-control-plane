package githublifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

type fixture struct {
	repository Repository
	base       Branch
	head       Branch
	headSHA    GitSHA
	headTree   GitSHA
	baseSHA    GitSHA
	resultSHA  GitSHA
	resultTree GitSHA
	pr         PullRequestIdentity
	actor      ActingIdentity
	snapshot   SnapshotIdentity
	authority  Authority
	limits     Limits
	mergeWrite MergeInput
	sealed     SealedMergeAuthorizationV1
	prAuth     AuthoritativePullRequestSnapshotV1
	checkRuns  PaginationClosureV1
	statuses   PaginationClosureV1
	recipe     MergeCommitRecipeV1
	readyProof CurrentReadyProofV1
}

func newFixture(t *testing.T, method MergeMethod) fixture {
	t.Helper()
	must := func(value any, err error) any {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	f := fixture{limits: DefaultLimits()}
	f.repository = must(NewRepository("octo-org", "control-plane")).(Repository)
	f.base = must(NewBranch("main")).(Branch)
	f.head = must(NewBranch("feature/exact-head")).(Branch)
	f.headSHA = must(NewGitSHA(strings.Repeat("1", 40))).(GitSHA)
	f.headTree = must(NewGitSHA(strings.Repeat("2", 40))).(GitSHA)
	f.baseSHA = must(NewGitSHA(strings.Repeat("3", 40))).(GitSHA)
	f.resultSHA = must(NewGitSHA(strings.Repeat("4", 40))).(GitSHA)
	f.resultTree = f.headTree
	f.pr = must(NewPullRequestIdentity(17, "PR_node_17")).(PullRequestIdentity)
	f.actor = must(NewAppInstallationIdentity("github-app:builder", 90210)).(ActingIdentity)
	f.snapshot = must(NewSnapshotIdentity("github", "request-initial", time.Unix(1700000000, 100).UnixNano())).(SnapshotIdentity)
	expected := fakeExpectedContent(t, f.headSHA, f.baseSHA, f.headTree)
	readyRef := expected.SourceIntegrationEvidence()
	configRef := ledger.EvidenceRef{URI: "evidence/repository.json", Kind: "repository-binding", SHA256: strings.Repeat("a", 64)}
	repositoryBinding := must(NewRepositoryBindingV1(RepositoryBindingV1Input{
		Phase3RepositoryIdentity: "repo-id", Phase3RepositoryPath: "/work/repo", Phase3CanonicalRemote: "https://github.com/octo-org/control-plane",
		Phase3StartSHA: f.baseSHA, GitHubRepository: f.repository, GitHubRepositoryNodeID: "R_repo", GitHubRepositoryDatabaseID: 99,
		ConfigurationEvidence: configRef,
	})).(RepositoryBindingV1)
	phase3JSON, _ := json.Marshal(map[string]any{
		"run_id": "run-1", "repository": map[string]any{"path": "/work/repo", "identity": "repo-id", "remotes": map[string]string{"origin": "https://github.com/octo-org/control-plane"}, "start_sha": f.baseSHA.String()},
		"plan": map[string]string{"path": "/work/repo/plan.md", "sha256": strings.Repeat("b", 64)}, "policy_version": "phase3-policy-v1",
	})
	readyEvent := ledger.Event{SchemaVersion: 1, EventID: "ready-event-1", Timestamp: time.Unix(1700000000, 1).UTC(), ProjectID: "project-1", PlanID: "plan-1", RunID: "run-1", AttemptID: "attempt-1", EventType: "STATE_TRANSITION", StateFrom: domain.StateIntegrationAccepted, StateTo: domain.StateReadyForMerge, Actor: "controller", Source: "integration-gate", EvidenceRefs: []ledger.EvidenceRef{readyRef}}
	readyEventJSON, _ := json.Marshal(readyEvent)
	states := []domain.State{
		domain.StateRunCreated, domain.StateAuthorityValidated, domain.StateExecutionStarting, domain.StateImplementing,
		domain.StateImplementationCompleted, domain.StateBranchAcceptancePending, domain.StateBranchAccepted,
		domain.StateIntegrationPending, domain.StateIntegrating, domain.StateIntegrationAccepted, domain.StateReadyForMerge,
	}
	ledgerPrefix := []byte{}
	readyOffset := int64(0)
	for index := 0; index < len(states)-1; index++ {
		event := ledger.Event{
			SchemaVersion: 1, EventID: fmt.Sprintf("transition-event-%02d", index+1), Timestamp: time.Unix(1699999900+int64(index), 1).UTC(),
			ProjectID: "project-1", PlanID: "plan-1", RunID: "run-1", AttemptID: "attempt-1", EventType: "STATE_TRANSITION",
			StateFrom: states[index], StateTo: states[index+1], Actor: "controller", Source: "fixture",
		}
		if event.StateTo == domain.StateReadyForMerge {
			event = readyEvent
			readyOffset = int64(len(ledgerPrefix))
		}
		eventJSON, _ := json.Marshal(event)
		ledgerPrefix = append(ledgerPrefix, eventJSON...)
		ledgerPrefix = append(ledgerPrefix, '\n')
	}
	readySequence := int64(len(states) - 1)
	ready := must(NewReadyAuthorityBindingV1(ReadyAuthorityBindingV1Input{
		Phase3AuthorityJSON: phase3JSON, Phase3AuthoritySHA256: digestBytes(phase3JSON), ProjectID: "project-1", PlanID: "plan-1", RunID: "run-1", AttemptID: "attempt-1",
		AcceptedSources:   []AcceptedSourceCandidateV1{{ProjectID: "source-project", PlanID: "source-plan", RunID: "source-run", AttemptID: "source-attempt", RepositoryIdentity: "repo-id", Branch: f.head.String(), StartSHA: f.baseSHA.String(), AcceptedHeadSHA: f.headSHA.String(), AcceptancePolicyIdentity: "accept-v1", AcceptanceEvidence: []ledger.EvidenceRef{readyRef}}},
		RepositoryBinding: repositoryBinding, ReadyEventJSON: readyEventJSON, ReadyEventSHA256: digestBytes(readyEventJSON), ReadyEventID: readyEvent.EventID, ReadyEventUnixNano: readyEvent.Timestamp.UnixNano(), LedgerIdentity: "ledger-dev-ino", ReadyEventByteOffset: readyOffset, ReadyRunStateSequence: readySequence, LedgerPrefixLength: int64(len(ledgerPrefix)), LedgerPrefixSHA256: digestBytes(ledgerPrefix), ReadyTransitionOrdinal: readySequence,
		ReadyEvidenceRefs: []ledger.EvidenceRef{readyRef}, ReadyDecisionRef: readyRef, EvidenceClosureRefs: []ledger.EvidenceRef{readyRef, configRef}, IntegratedHeadSHA: f.headSHA, BaselineSHA: f.baseSHA, ExpectedTreeSHA: f.headTree,
	}, f.limits)).(ReadyAuthorityBindingV1)
	policySource := ledger.EvidenceRef{URI: "evidence/merge-policy.json", Kind: "merge-policy", SHA256: strings.Repeat("f", 64)}
	policyAuthority := must(NewPolicyAuthorityBindingV1(PolicyAuthorityBindingV1Input{policySource, repositoryBinding.SHA256(), digestBytes(phase3JSON), ready.SHA256(), f.actor})).(PolicyAuthorityBindingV1)
	identity := MergeCommitIdentityV1{Name: "ABCP", Email: "abcp@example.com", Timezone: "+0000"}
	policy := must(NewMergePolicyV1(MergePolicyV1Input{PolicyVersion: "merge-v1", AuthorityBinding: policyAuthority, Method: MergeMethodMerge,
		RequiredChecks: []TrustedCheckIdentityV1{}, EligibleReviewers: []StableIdentityV1{}, RequiredReviewers: []StableIdentityV1{}, MinimumApprovals: 0,
		Recipe: MergeCommitRecipePolicyV1{MessageTemplate: "Merge authorized head", TrailerTemplate: "ABCP-Write-ID", Author: identity, Committer: identity, TimestampDerivation: "ready-event-time", ObjectFormat: "sha1", OrderedParents: true}}, f.limits)).(MergePolicyV1)
	f.authority = must(NewAuthority(AuthorityInput{
		Repository: f.repository, BaseBranch: f.base, HeadBranch: f.head, HeadSHA: f.headSHA,
		ExpectedBaseTipSHA: f.baseSHA, PullRequest: &f.pr, AllowedMergeMethod: MergeMethodMerge, Actor: f.actor, ExpectedContent: expected, ReadyBinding: ready, MergePolicy: policy,
	})).(Authority)
	if method != MergeMethodMerge {
		return f
	}
	f.checkRuns = emptyPaginationClosure(t, f, PaginationCheckRuns, nil, "request-checks-initial", f.snapshot.ObservedUnixNano())
	f.statuses = emptyPaginationClosure(t, f, PaginationCommitStatuses, nil, "request-statuses-initial", f.snapshot.ObservedUnixNano())
	reviews := emptyPaginationClosure(t, f, PaginationReviews, &f.pr, "request-reviews-initial", f.snapshot.ObservedUnixNano())
	open, no := PullRequestOpen, false
	prSnapshot := must(NewSnapshotIdentity("github", "request-pr-initial", f.snapshot.ObservedUnixNano())).(SnapshotIdentity)
	prBodySHA := digestBytes([]byte(`{"fixture":"pull-request"}`))
	prBodyEvidence := ledger.EvidenceRef{URI: "evidence/pr-body-initial", Kind: GitHubPullRequestResponseEvidenceKindV1, SHA256: prBodySHA}
	prInput := AuthoritativePullRequestSnapshotV1Input{Snapshot: prSnapshot, ResponseBodySHA256: prBodySHA, APIVersion: GitHubAPIVersionV1, RepositoryBinding: repositoryBinding, PullRequest: f.pr, PullRequestDatabaseID: 17, BaseRepositoryNodeID: "R_repo", BaseRef: "refs/heads/" + f.base.String(), BaseOID: f.baseSHA, HeadRepositoryNodeID: "R_repo", HeadRef: "refs/heads/" + f.head.String(), HeadOID: f.headSHA, State: &open, IsDraft: &no, Merged: &no, Actor: f.actor, Reviews: []Review{}, ReviewsClosure: reviews}
	prEnvelopeEvidence := must(NewPullRequestEnvelopeEvidenceV1("evidence/pr-envelope-initial", prInput, f.limits)).(ledger.EvidenceRef)
	prInput.EvidenceRefs = []ledger.EvidenceRef{prBodyEvidence, prEnvelopeEvidence}
	f.prAuth = must(NewAuthoritativePullRequestSnapshotV1(prInput, f.limits)).(AuthoritativePullRequestSnapshotV1)
	capability := must(NewProviderCapabilityV1(ProviderCapabilityV1Input{Name: GitHubAtomicBaseHeadCapabilityV1, RepositoryNodeID: "R_repo", APIVersion: "2026-03-10", Atomic: true, AllOrNothing: true, SupportsNoOp: true, BaseThenHeadOrder: true, ForceFalse: true, EvidenceRefs: []ledger.EvidenceRef{configRef}}, f.limits)).(ProviderCapabilityV1)
	f.recipe = must(NewMergeCommitRecipeV1("merge-write-1", f.authority, f.limits)).(MergeCommitRecipeV1)
	f.resultSHA = f.recipe.ExpectedResultSHA()
	approval := ledger.EvidenceRef{URI: "evidence/approval", Kind: "approval", SHA256: strings.Repeat("8", 64)}
	f.mergeWrite = must(NewMergeInput(MergeAuthorizationInputV1{f.authority, strings.Repeat("9", 64), f.prAuth, []Check{}, f.checkRuns, f.statuses, capability, f.recipe, []ledger.EvidenceRef{approval}}, "merge-write-1", f.limits)).(MergeInput)
	ledgerEvidence := ledger.EvidenceRef{URI: "evidence/current-ready-ledger", Kind: CurrentReadyLedgerEvidenceKindV1, SHA256: digestBytes(ledgerPrefix)}
	f.readyProof = must(NewCurrentReadyProofV1(CurrentReadyProofV1Input{ReadyBinding: ready, ControllerSequence: 10, ObservedUnixNano: 1700000001000000000, ObservedLedgerIdentity: ready.input.LedgerIdentity, ObservedBoundPrefixSHA256: ready.input.LedgerPrefixSHA256, ObservedLedgerLength: int64(len(ledgerPrefix)), ObservedLedgerSHA256: digestBytes(ledgerPrefix), ObservedLedgerJSONL: ledgerPrefix, NoLaterTransition: true, EvidenceRefs: []ledger.EvidenceRef{ledgerEvidence}}, f.limits)).(CurrentReadyProofV1)
	finalReviews := emptyPaginationClosure(t, f, PaginationReviews, &f.pr, "request-reviews-final", 1700000002200000000)
	finalChecks := emptyPaginationClosure(t, f, PaginationCheckRuns, nil, "request-checks-final", 1700000002300000000)
	finalStatuses := emptyPaginationClosure(t, f, PaginationCommitStatuses, nil, "request-statuses-final", 1700000002400000000)
	finalPRInput := f.prAuth.Input()
	finalPRInput.Snapshot = must(NewSnapshotIdentity("github", "request-pr-final", 1700000002500000000)).(SnapshotIdentity)
	finalPRInput.ReviewsClosure = finalReviews
	finalPREnvelope := must(NewPullRequestEnvelopeEvidenceV1("evidence/pr-envelope-final", finalPRInput, f.limits)).(ledger.EvidenceRef)
	finalPRInput.EvidenceRefs = []ledger.EvidenceRef{prBodyEvidence, finalPREnvelope}
	finalPR := must(NewAuthoritativePullRequestSnapshotV1(finalPRInput, f.limits)).(AuthoritativePullRequestSnapshotV1)
	finalEvidence := []ledger.EvidenceRef{approval, ledgerEvidence, prBodyEvidence, finalPREnvelope}
	finalEvidence = append(finalEvidence, finalReviews.input.EvidenceRefs...)
	finalEvidence = append(finalEvidence, finalChecks.input.EvidenceRefs...)
	finalEvidence = append(finalEvidence, finalStatuses.input.EvidenceRefs...)
	admissionStats, err := validatePaginationBoundaryV1(reviews, f.checkRuns, f.statuses, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	finalStats, err := validatePaginationBoundaryV1(finalReviews, finalChecks, finalStatuses, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	admissionCalls, finalCalls := admissionStats.Pages+1, finalStats.Pages+1
	counters := AuthorizationCountersV1{
		AdmissionHTTPCalls: admissionCalls, AdmissionObservedChecks: 0, AdmissionObservedReviews: 0,
		AdmissionPaginationSources: admissionStats.Sources, AdmissionPaginationPages: admissionStats.Pages,
		AdmissionPaginationItems: admissionStats.Items, AdmissionPaginationClosureBytes: admissionStats.ClosureBytes,
		FinalRevalidationHTTPCalls: finalCalls, FinalObservedChecks: 0, FinalObservedReviews: 0,
		FinalPaginationSources: finalStats.Sources, FinalPaginationPages: finalStats.Pages,
		FinalPaginationItems: finalStats.Items, FinalPaginationClosureBytes: finalStats.ClosureBytes,
		ReadyLedgerBytes: int64(len(ledgerPrefix)), ReadyLedgerRecords: bytes.Count(ledgerPrefix, []byte{'\n'}),
		PreSubmitHTTPCalls: admissionCalls + finalCalls, TotalHTTPCalls: admissionCalls + finalCalls,
		ControllerInvocationNanos: 1_000_000_000,
	}
	final := must(NewFinalRevalidationV1(FinalRevalidationV1Input{
		MergeInput: f.mergeWrite, ControllerSequence: 11, StartedUnixNano: 1700000002000000000, CompletedUnixNano: 1700000003000000000,
		CurrentReadyProof: f.readyProof, PullRequest: finalPR, Checks: []Check{}, CheckRunsClosure: finalChecks,
		CommitStatusesClosure: finalStatuses, Capability: capability, Recipe: f.recipe, Counters: counters,
		NoTargetRequestAttempted: true, EvidenceRefs: finalEvidence,
	}, f.limits)).(FinalRevalidationV1)
	seal := must(NewAuthorizationSealV1(AuthorizationSealV1Input{MergeInput: f.mergeWrite, FinalRevalidation: final}, f.limits)).(AuthorizationSealV1)
	commitment := must(NewTargetRefCommitmentV1(f.mergeWrite, seal, f.limits)).(TargetRefCommitmentV1)
	f.sealed = must(NewSealedMergeAuthorizationV1(SealedMergeAuthorizationV1Input{f.mergeWrite, seal, commitment}, f.limits)).(SealedMergeAuthorizationV1)
	return f
}

func newTargetSubmission(t *testing.T, f fixture, invocationID string) TargetSubmissionV1 {
	t.Helper()
	submission, err := NewTargetSubmissionV1(invocationID, f.sealed, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	return submission
}

func newTargetResponseEnvelope(t *testing.T, f fixture, submission TargetSubmissionV1, providerRequestID string, observedUnixNano int64, status int, body []byte) TargetResponseEnvelopeV1 {
	t.Helper()
	response, err := NewSnapshotIdentity("github", providerRequestID, observedUnixNano)
	if err != nil {
		t.Fatal(err)
	}
	bodyEvidence := ledger.EvidenceRef{
		URI: "evidence/target-response-body-" + providerRequestID, Kind: GitHubTargetResponseBodyEvidenceKindV1, SHA256: digestBytes(body),
	}
	envelope, err := NewTargetResponseEnvelopeV1(TargetResponseEnvelopeV1Input{
		Response: response, HTTPStatus: status, ResponseBody: body, BodyEvidence: bodyEvidence,
		EnvelopeURI: "evidence/target-response-envelope-" + providerRequestID,
	}, submission, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func targetSuccessResponseBody(t *testing.T, mutationID string) []byte {
	t.Helper()
	response := gitHubUpdateRefsSuccessV1{}
	response.Data.UpdateRefs.ClientMutationID = mutationID
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func emptyPaginationClosure(t *testing.T, f fixture, source PaginationSourceKind, pr *PullRequestIdentity, requestID string, observedUnixNano int64) PaginationClosureV1 {
	t.Helper()
	query, err := DerivePaginationQueryV1(PaginationQueryScopeV1{Source: source, Repository: f.repository, RepositoryNodeID: "R_repo", PullRequest: pr, HeadSHA: f.headSHA}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	response, err := NewSnapshotIdentity("github", requestID, observedUnixNano)
	if err != nil {
		t.Fatal(err)
	}
	bodyEvidence := ledger.EvidenceRef{URI: "evidence/response-" + requestID, Kind: GitHubPaginationBodyEvidenceKindV1, SHA256: strings.Repeat("7", 64)}
	pageInput := PaginationPageV1Input{Query: query, Ordinal: 0, RequestedPage: 1, Response: response, RawBodySHA256: bodyEvidence.SHA256, ResponseEvidence: bodyEvidence, Items: []CanonicalPaginationItemV1{}, RESTLinkHeader: "", RESTLinkObserved: true}
	pageInput.EnvelopeEvidence, err = NewPaginationEnvelopeEvidenceV1("evidence/response-envelope-"+requestID, pageInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	page, err := NewPaginationPageV1(pageInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	closure, err := NewPaginationClosureV1(PaginationClosureV1Input{Query: query, Pages: []PaginationPageV1{page}, EvidenceRefs: []ledger.EvidenceRef{bodyEvidence, pageInput.EnvelopeEvidence}}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	return closure
}

func fakeExpectedContent(t *testing.T, head, base, tree GitSHA) ExpectedMergeContent {
	t.Helper()
	ref := ledger.EvidenceRef{URI: "evidence/ready.json", Kind: phase3DecisionKind, SHA256: strings.Repeat("d", 64)}
	git := PinnedGitIdentity{path: "/usr/bin/git", version: "git version test", binarySHA256: strings.Repeat("e", 64)}
	canonical, digest, err := canonicalExpectedContent(ExpectedMergeContentPolicy, head, base, ref, git, tree)
	if err != nil {
		t.Fatal(err)
	}
	return ExpectedMergeContent{ExpectedMergeContentPolicy, head, base, ref, git, tree, canonical, digest}
}

func (f fixture) prInput() PullRequestSnapshotInput {
	return PullRequestSnapshotInput{
		Snapshot: f.snapshot, Repository: f.repository, PullRequest: f.pr, BaseBranch: f.base,
		BaseTipSHA: f.baseSHA, HeadBranch: f.head, HeadSHA: f.headSHA, State: PullRequestOpen,
		MergeMethod: f.authority.AllowedMergeMethod(),
	}
}

func (f fixture) mergeResultInput() MergeResultInput {
	return MergeResultInput{Snapshot: f.snapshot, Repository: f.repository, PullRequest: f.pr, Actor: f.actor,
		AcceptedHeadSHA: f.headSHA, AcceptedHeadTree: f.headTree, BaseBeforeSHA: f.baseSHA, Method: MergeMethodMerge,
		ResultSHA: f.resultSHA, ResultTree: f.resultTree, Parents: []GitSHA{f.baseSHA, f.headSHA}, Attempt: f.mergeWrite.Attempt(),
		ExpectedContent: f.authority.ExpectedContent(), SealedAuthorization: f.sealed, Recipe: f.recipe}
}

func (f fixture) validMergeResult(t *testing.T) MergeResult {
	t.Helper()
	result, err := NewMergeResult(f.mergeResultInput(), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f fixture) validPostMergeObservation(t *testing.T, result MergeResult, tip GitSHA, distance int) PostMergeObservation {
	t.Helper()
	evidence := []ledger.EvidenceRef{{URI: "evidence/post-merge", Kind: "post-merge", SHA256: strings.Repeat("5", 64)}}
	object, err := NewResultCommitObservationV1(ResultCommitObservationV1Input{Snapshot: f.snapshot, Repository: f.repository, ResultSHA: f.resultSHA, ResultTree: f.resultTree, Parents: []GitSHA{f.baseSHA, f.headSHA}, Message: f.recipe.input.Message, Author: f.recipe.input.Author, Committer: f.recipe.input.Committer, AuthorUnix: f.recipe.input.AuthorUnix, CommitterUnix: f.recipe.input.CommitterUnix, RecipeSHA256: f.recipe.SHA256(), EvidenceRefs: evidence}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	status := TargetContainmentAhead
	if tip == f.resultSHA {
		status = TargetContainmentIdentical
	}
	containment, err := NewTargetContainmentProofV1(TargetContainmentProofV1Input{Snapshot: f.snapshot, Repository: f.repository, TargetRef: "refs/heads/" + f.base.String(), ResultSHA: f.resultSHA, ObservedTargetTipSHA: tip, Mechanism: GitHubCompareProofV1, Status: status, MergeBaseSHA: f.resultSHA, DescendantDistance: distance, EvidenceRefs: evidence}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := NewPostMergeObservation(PostMergeObservationInput{Snapshot: f.snapshot, Repository: f.repository, BaseBranch: f.base, PullRequest: f.pr, Actor: f.actor, AcceptedHeadSHA: f.headSHA, AcceptedHeadTree: f.headTree, BaseBeforeSHA: f.baseSHA, Method: MergeMethodMerge, ResultSHA: f.resultSHA, ObservedTargetTipSHA: tip, ResultTree: f.resultTree, Parents: []GitSHA{f.baseSHA, f.headSHA}, Attempt: f.mergeWrite.Attempt(), ExpectedContent: f.authority.ExpectedContent(), SealedAuthorization: f.sealed, ResultObject: object, ContainmentProof: containment, EvidenceRefs: evidence}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	return observation
}

func TestAuthorityAndRemoteIdentityDriftFailClosed(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	valid, err := NewPullRequestSnapshot(f.prInput(), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePullRequest(f.authority, valid, f.limits); err != nil {
		t.Fatalf("valid PR: %v", err)
	}

	otherHead, _ := NewGitSHA(strings.Repeat("6", 40))
	movedHead := f.prInput()
	movedHead.HeadSHA = otherHead
	snapshot, err := NewPullRequestSnapshot(movedHead, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePullRequest(f.authority, snapshot, f.limits); err == nil || !strings.Contains(err.Error(), "head moved") {
		t.Fatalf("expected moved-head failure, got %v", err)
	}

	otherBase, _ := NewGitSHA(strings.Repeat("7", 40))
	movedBase := f.prInput()
	movedBase.BaseTipSHA = otherBase
	snapshot, err = NewPullRequestSnapshot(movedBase, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePullRequest(f.authority, snapshot, f.limits); err == nil || !strings.Contains(err.Error(), "base tip moved") {
		t.Fatalf("expected moved-base failure, got %v", err)
	}

	changedMethod := f.prInput()
	changedMethod.MergeMethod = MergeMethodSquash
	snapshot, err = NewPullRequestSnapshot(changedMethod, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePullRequest(f.authority, snapshot, f.limits); err == nil || !strings.Contains(err.Error(), "merge method") {
		t.Fatalf("expected changed-method failure, got %v", err)
	}

	unsupported := f.prInput()
	unsupported.MergeMethod = MergeMethod("octopus")
	if _, err := NewPullRequestSnapshot(unsupported, f.limits); err == nil {
		t.Fatal("unsupported merge method accepted")
	}
}

func TestAuthorityCopiesOptionalPRAndPreservesExactCase(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	originalDigest, err := f.authority.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	input := f.authority.Input()
	input.PullRequest.number = 999
	input.ExpectedContent.canonical[0] = '!'
	pr, ok := f.authority.PullRequest()
	if !ok || pr.Number() != 17 {
		t.Fatal("authority optional PR identity was aliased")
	}
	if digest, err := f.authority.SHA256(); err != nil || digest != originalDigest {
		t.Fatal("authority identity changed after caller mutation")
	}
	caseRepository, err := NewRepository("Octo-Org", "control-plane")
	if err != nil {
		t.Fatal(err)
	}
	if caseRepository == f.repository || caseRepository.String() == f.repository.String() {
		t.Fatal("repository identity was silently case-normalized")
	}
	snapshot, err := NewPullRequestSnapshot(f.prInput(), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	writeInput, err := NewUpsertPullRequestInput(f.authority, "title", "", "pr-write-1", f.limits)
	if err != nil {
		t.Fatal(err)
	}
	writeResult, err := NewPullRequestWriteResult(writeInput, snapshot, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	wrongActor, _ := NewUserIdentity("different-user")
	writeResult.attempt.actor = wrongActor
	if err := ValidatePullRequestWriteResult(writeInput, writeResult, f.limits); err == nil {
		t.Fatal("pull request write result accepted the wrong acting identity")
	}
}

func TestStaleCIAndAmbiguousPullRequestsAreRejected(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	staleSHA, _ := NewGitSHA(strings.Repeat("8", 40))
	ci, err := NewCISnapshot(CISnapshotInput{
		Snapshot: f.snapshot, Repository: f.repository, HeadSHA: staleSHA,
		Checks: []Check{{NodeID: "check-1", Name: "test", Identity: TrustedCheckIdentityV1{Context: "test", Source: CheckSourceCheckRun, Producer: StableIdentityV1{DatabaseID: 1, NodeID: "producer-1"}}, Status: CheckCompleted, Conclusion: ConclusionSuccess, HeadSHA: staleSHA}},
	}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCI(f.authority, ci, f.limits); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("expected stale CI failure, got %v", err)
	}

	first, err := NewPullRequestSnapshot(f.prInput(), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	secondInput := f.prInput()
	secondInput.Snapshot, _ = NewSnapshotIdentity("github", "request-2", time.Now().Add(time.Second).UnixNano())
	second, err := NewPullRequestSnapshot(secondInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPullRequestPage(1, 2, []PullRequestSnapshot{first, second}, f.limits); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected duplicate PR identity failure, got %v", err)
	}

	otherPR, _ := NewPullRequestIdentity(18, "PR_node_18")
	secondInput.PullRequest = otherPR
	second, err = NewPullRequestSnapshot(secondInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SelectPullRequest(f.authority, []PullRequestSnapshot{second, first}, f.limits); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected ambiguous selection failure, got %v", err)
	}
}

func TestStrategyAwarePostMergeAllowsDivergentSHAWithExactProof(t *testing.T) {
	f := newFixture(t, MergeMethodSquash)
	lineage := []CommitLineage{{SourceSHA: f.headSHA, SourceTree: f.headTree, ResultSHA: f.resultSHA, ResultTree: f.resultTree, Parents: []GitSHA{f.baseSHA}}}
	if f.resultSHA == f.headSHA {
		t.Fatal("fixture must exercise a synthesized SHA")
	}
	if err := verifyStrategy(MergeMethodSquash, f.headSHA, f.headTree, f.baseSHA, f.resultSHA, f.resultTree, []GitSHA{f.baseSHA}, lineage); err != nil {
		t.Fatalf("valid divergent post-merge proof: %v", err)
	}
	badTree, _ := NewGitSHA(strings.Repeat("9", 40))
	lineage[0].SourceTree = badTree
	if err := verifyStrategy(MergeMethodSquash, f.headSHA, f.headTree, f.baseSHA, f.resultSHA, f.resultTree, []GitSHA{f.baseSHA}, lineage); err == nil || !strings.Contains(err.Error(), "lineage") {
		t.Fatalf("expected incorrect tree-lineage failure, got %v", err)
	}
}

func TestMergeCommitRequiresExactOrderedLineage(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	input := f.mergeResultInput()
	result, err := NewMergeResult(input, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMergeResult(f.sealed, result, f.limits); err != nil {
		t.Fatal(err)
	}
	input.Parents = []GitSHA{f.headSHA, f.baseSHA}
	result, err = NewMergeResult(input, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMergeResult(f.sealed, result, f.limits); err == nil {
		t.Fatal("reversed merge parents accepted")
	}
}

func TestRebaseRequiresContinuousOrderedLineage(t *testing.T) {
	f := newFixture(t, MergeMethodRebase)
	firstSource, _ := NewGitSHA(strings.Repeat("6", 40))
	firstTree, _ := NewGitSHA(strings.Repeat("7", 40))
	firstResult, _ := NewGitSHA(strings.Repeat("8", 40))
	firstResultTree, _ := NewGitSHA(strings.Repeat("9", 40))
	lineage := []CommitLineage{
		{SourceSHA: firstSource, SourceTree: firstTree, ResultSHA: firstResult, ResultTree: firstResultTree, Parents: []GitSHA{f.baseSHA}},
		{SourceSHA: f.headSHA, SourceTree: f.headTree, ResultSHA: f.resultSHA, ResultTree: f.resultTree, Parents: []GitSHA{firstResult}},
	}
	err := verifyStrategy(MergeMethodRebase, f.headSHA, f.headTree, f.baseSHA, f.resultSHA, f.resultTree, []GitSHA{firstResult}, lineage)
	if err != nil {
		t.Fatal(err)
	}
	lineage[0].Parents[0] = f.headSHA
	if err := verifyStrategy(MergeMethodRebase, f.headSHA, f.headTree, f.baseSHA, f.resultSHA, f.resultTree, []GitSHA{firstResult}, lineage); err == nil || !strings.Contains(err.Error(), "chain") {
		t.Fatalf("expected broken rebase lineage failure, got %v", err)
	}
}

func TestSubmittedCancellationAndDeadlineAreAmbiguousAndNotRetried(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	attempt := f.mergeWrite.Attempt()
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		failure := NewWriteExecutionError(attempt, true, cause)
		if failure.Class() != FailureAmbiguousWrite || !failure.Submitted() {
			t.Fatalf("submitted %v was not ambiguous", cause)
		}
		if CanRetry(failure, 0, f.limits, &attempt, nil) {
			t.Fatal("ambiguous write retried without reconciliation")
		}
	}
	preSubmit := NewWriteExecutionError(attempt, false, context.DeadlineExceeded)
	if preSubmit.Class() != FailureProviderUnavailable {
		t.Fatalf("pre-submit class = %s", preSubmit.Class())
	}

	evidence := []ledger.EvidenceRef{{URI: "evidence/reconcile.json", Kind: "reconciliation", SHA256: strings.Repeat("a", 64)}}
	response, _ := NewSnapshotIdentity("github", "github-response-target-attempt-2", 1700000004000000000)
	responseBody := atomicRejectionResponseBody(t, 0)
	proofEvidence := ledger.EvidenceRef{URI: "evidence/not-applied.json", Kind: NotAppliedAtomicRejectionEvidenceKindV1, SHA256: digestBytes(responseBody)}
	targetSubmission := newTargetSubmission(t, f, "target-attempt-2")
	responseEnvelope := newTargetResponseEnvelope(t, f, targetSubmission, response.RequestID(), response.ObservedUnixNano(), 200, responseBody)
	proof, err := NewNotAppliedProofV1(NotAppliedProofV1Input{Kind: NotAppliedAtomicBaseRejected, RequestBytes: 50, ResponseEnvelope: &responseEnvelope, Response: &response, HTTPStatus: 200, ResponseBodySHA256: digestBytes(responseBody), ResponseBody: responseBody, EvidenceRef: proofEvidence}, f.sealed, targetSubmission, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	evidence = append(evidence, proofEvidence, responseEnvelope.input.BodyEvidence, responseEnvelope.EvidenceRef())
	reconciled, err := NewMergeReconciliationResult(f.sealed, targetSubmission, ReconciliationNotApplied, nil, &proof, evidence, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	failure := NewWriteExecutionError(attempt, true, errors.New("lost response"))
	if CanRetry(failure, 0, f.limits, &attempt, &reconciled) {
		t.Fatal("merge reconciliation improperly granted mutation retry authority")
	}
	unknown, err := NewMergeReconciliationResult(f.sealed, targetSubmission, ReconciliationUnknown, nil, nil, evidence, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if CanRetry(failure, 0, f.limits, &attempt, &unknown) {
		t.Fatal("unknown reconciliation authorized retry")
	}
	otherAttempt := attempt
	otherAttempt.writeID = "other-write"
	reconciled.attempt = otherAttempt
	if CanRetry(failure, 0, f.limits, &attempt, &reconciled) {
		t.Fatal("mismatched reconciliation authorized retry")
	}
}

func TestBoundsUnsafeIdentifiersAndSubstantiveClasses(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	if _, err := NewRepository("owner/escape", "repo"); err == nil {
		t.Fatal("unsafe repository accepted")
	}
	for _, branch := range []string{"../main", "refs heads/main", "main.lock", "main@{1}", "Main\nnext"} {
		if _, err := NewBranch(branch); err == nil {
			t.Fatalf("unsafe branch %q accepted", branch)
		}
	}
	for _, sha := range []string{strings.Repeat("A", 40), strings.Repeat("a", 39), "not-a-sha"} {
		if _, err := NewGitSHA(sha); err == nil {
			t.Fatalf("unsafe SHA %q accepted", sha)
		}
	}
	if _, err := NewUpsertPullRequestInput(f.authority, strings.Repeat("x", f.limits.MaxTextBytes+1), "", "pr-write", f.limits); err == nil {
		t.Fatal("oversized text accepted")
	}
	invalidLimits := f.limits
	invalidLimits.CallTimeout = 0
	if err := invalidLimits.Validate(); err == nil {
		t.Fatal("missing per-call timeout accepted")
	}
	checks := make([]Check, f.limits.MaxObservedChecks+1)
	if _, err := NewCISnapshot(CISnapshotInput{Snapshot: f.snapshot, Repository: f.repository, HeadSHA: f.headSHA, Checks: checks}, f.limits); err == nil || !strings.Contains(err.Error(), "item limit") {
		t.Fatalf("expected oversized collection failure, got %v", err)
	}
	tooManyRefs := make([]ledger.EvidenceRef, f.limits.MaxEvidenceRefs+1)
	input := f.prInput()
	input.EvidenceRefs = tooManyRefs
	if _, err := NewPullRequestSnapshot(input, f.limits); err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("expected evidence bound failure, got %v", err)
	}
	if _, err := NewSubstantiveError("policy", FailureProviderUnavailable, nil); err == nil {
		t.Fatal("availability mislabeled substantive")
	}
	policyFailure, err := NewSubstantiveError("approval", FailurePolicy, errors.New("approval missing"))
	if err != nil || policyFailure.Class() != FailurePolicy || CanRetry(policyFailure, 0, f.limits, nil, nil) {
		t.Fatal("policy failure classification/retry is incorrect")
	}
}

func TestSnapshotsAreCopySafeAndCanonical(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	refA := ledger.EvidenceRef{URI: "evidence/a", Kind: "check", SHA256: strings.Repeat("a", 64)}
	refB := ledger.EvidenceRef{URI: "evidence/b", Kind: "check", SHA256: strings.Repeat("b", 64)}
	reviewA := Review{NodeID: "review-a", DatabaseID: 1, Reviewer: StableIdentityV1{DatabaseID: 11, NodeID: "user-a"}, State: ReviewApproved, CommitSHA: f.headSHA}
	reviewB := Review{NodeID: "review-b", DatabaseID: 2, Reviewer: StableIdentityV1{DatabaseID: 12, NodeID: "user-b"}, State: ReviewCommented, CommitSHA: f.headSHA}
	reviews := []Review{reviewB, reviewA}
	refs := []ledger.EvidenceRef{refB, refA}
	metadata := map[string]string{"z": "last", "a": "first"}
	input := f.prInput()
	input.Reviews, input.EvidenceRefs, input.Metadata = reviews, refs, metadata
	first, err := NewPullRequestSnapshot(input, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	originalJSON, originalDigest := string(first.CanonicalJSON()), first.SHA256()

	reviews[0].NodeID = "mutated"
	refs[0].URI = "mutated"
	metadata["a"] = "mutated"
	returned := first.Input()
	returned.Reviews[0].NodeID = "also-mutated"
	returned.EvidenceRefs[0].URI = "also-mutated"
	returned.Metadata["z"] = "also-mutated"
	if string(first.CanonicalJSON()) != originalJSON || first.SHA256() != originalDigest {
		t.Fatal("caller mutation changed immutable snapshot")
	}

	reversed := f.prInput()
	reversed.Reviews = []Review{reviewA, reviewB}
	reversed.EvidenceRefs = []ledger.EvidenceRef{refA, refB}
	reversed.Metadata = map[string]string{"a": "first", "z": "last"}
	second, err := NewPullRequestSnapshot(reversed, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.CanonicalJSON()) != string(second.CanonicalJSON()) || first.SHA256() != second.SHA256() {
		t.Fatal("nondeterministic input ordering changed canonical identity")
	}
}

func TestMergeInputDefensivelyCopiesEvidence(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	input := f.mergeWrite
	evidence := input.Evidence()
	evidence[0].URI = "changed"
	returned := input.Evidence()
	returned[0].URI = "changed-again"
	if got := input.Evidence()[0].URI; got != "evidence/approval" {
		t.Fatalf("merge evidence mutated: %s", got)
	}
}

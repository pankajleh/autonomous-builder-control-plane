package githublifecycle

import (
	"context"
	"encoding/json"
	"errors"
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
	f.snapshot = must(NewSnapshotIdentity("github", "request-1", time.Now().UnixNano())).(SnapshotIdentity)
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
	ready := must(NewReadyAuthorityBindingV1(ReadyAuthorityBindingV1Input{
		Phase3AuthorityJSON: phase3JSON, Phase3AuthoritySHA256: digestBytes(phase3JSON), ProjectID: "project-1", PlanID: "plan-1", RunID: "run-1", AttemptID: "attempt-1",
		AcceptedSources:   []AcceptedSourceCandidateV1{{ProjectID: "source-project", PlanID: "source-plan", RunID: "source-run", AttemptID: "source-attempt", RepositoryIdentity: "repo-id", Branch: f.head.String(), StartSHA: f.baseSHA.String(), AcceptedHeadSHA: f.headSHA.String(), AcceptancePolicyIdentity: "accept-v1", AcceptanceEvidence: []ledger.EvidenceRef{readyRef}}},
		RepositoryBinding: repositoryBinding, ReadyEventJSON: readyEventJSON, ReadyEventSHA256: digestBytes(readyEventJSON), ReadyEventID: readyEvent.EventID, ReadyEventUnixNano: readyEvent.Timestamp.UnixNano(), LedgerIdentity: "ledger-dev-ino", ReadyEventByteOffset: 10, ReadyRunStateSequence: 9, LedgerPrefixLength: 100, LedgerPrefixSHA256: strings.Repeat("c", 64), ReadyTransitionOrdinal: 9,
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
	f.checkRuns = emptyPaginationClosure(t, f, PaginationCheckRuns, "/repos/octo-org/control-plane/commits/"+f.headSHA.String()+"/check-runs", nil)
	f.statuses = emptyPaginationClosure(t, f, PaginationCommitStatuses, "/repos/octo-org/control-plane/commits/"+f.headSHA.String()+"/statuses", nil)
	reviews := emptyPaginationClosure(t, f, PaginationReviews, "/repos/octo-org/control-plane/pulls/17/reviews", &f.pr)
	open, no := PullRequestOpen, false
	f.prAuth = must(NewAuthoritativePullRequestSnapshotV1(AuthoritativePullRequestSnapshotV1Input{Snapshot: f.snapshot, ResponseBodySHA256: strings.Repeat("1", 64), APIVersion: "2026-03-10", RepositoryBinding: repositoryBinding, PullRequest: f.pr, PullRequestDatabaseID: 17, BaseRepositoryNodeID: "R_repo", BaseRef: "refs/heads/" + f.base.String(), BaseOID: f.baseSHA, HeadRepositoryNodeID: "R_repo", HeadRef: "refs/heads/" + f.head.String(), HeadOID: f.headSHA, State: &open, IsDraft: &no, Merged: &no, Actor: f.actor, Reviews: []Review{}, ReviewsClosure: reviews, EvidenceRefs: []ledger.EvidenceRef{readyRef}}, f.limits)).(AuthoritativePullRequestSnapshotV1)
	capability := must(NewProviderCapabilityV1(ProviderCapabilityV1Input{Name: GitHubAtomicBaseHeadCapabilityV1, RepositoryNodeID: "R_repo", APIVersion: "2026-03-10", Atomic: true, AllOrNothing: true, SupportsNoOp: true, BaseThenHeadOrder: true, ForceFalse: true, EvidenceRefs: []ledger.EvidenceRef{configRef}}, f.limits)).(ProviderCapabilityV1)
	authoritySHA, _ := f.authority.SHA256()
	f.recipe = must(NewMergeCommitRecipeV1(MergeCommitRecipeV1Input{Repository: f.repository, TargetRef: "refs/heads/" + f.base.String(), ExpectedResultTree: f.headTree, Parents: []GitSHA{f.baseSHA, f.headSHA}, Message: "Merge authorized head\n\nABCP-Write-ID: merge-write-1", Author: identity, Committer: identity, AuthorUnix: 1700000000, CommitterUnix: 1700000000, ObjectFormat: "sha1", WriteID: "merge-write-1", AuthoritySHA256: authoritySHA, PolicySHA256: policy.SHA256(), ReadyBindingSHA256: ready.SHA256()}, policy, f.limits)).(MergeCommitRecipeV1)
	f.resultSHA = f.recipe.ExpectedResultSHA()
	approval := ledger.EvidenceRef{URI: "evidence/approval", Kind: "approval", SHA256: strings.Repeat("8", 64)}
	f.mergeWrite = must(NewMergeInput(MergeAuthorizationInputV1{f.authority, strings.Repeat("9", 64), f.prAuth, []Check{}, f.checkRuns, f.statuses, capability, f.recipe, []ledger.EvidenceRef{approval}}, "merge-write-1", f.limits)).(MergeInput)
	seal := must(NewAuthorizationSealV1(AuthorizationSealV1Input{MergeInput: f.mergeWrite, FinalPullRequest: f.prAuth, FinalChecks: []Check{}, FinalCheckRunsClosure: f.checkRuns, FinalCommitStatusesClosure: f.statuses, PolicyDecisionSHA256: strings.Repeat("9", 64), Verdict: "authorized", PREligible: true, Capability: capability, Recipe: f.recipe, Counters: AuthorizationCountersV1{}, NoTargetRequestAttempted: true, EvidenceRefs: []ledger.EvidenceRef{approval}}, f.limits)).(AuthorizationSealV1)
	commitment := must(NewTargetRefCommitmentV1(f.mergeWrite, seal, "mutation-1", f.limits)).(TargetRefCommitmentV1)
	f.sealed = must(NewSealedMergeAuthorizationV1(SealedMergeAuthorizationV1Input{f.mergeWrite, seal, commitment}, f.limits)).(SealedMergeAuthorizationV1)
	return f
}

func emptyPaginationClosure(t *testing.T, f fixture, source PaginationSourceKind, path string, pr *PullRequestIdentity) PaginationClosureV1 {
	t.Helper()
	query := PaginationQueryV1{Source: source, Protocol: PaginationREST, Method: "GET", PathOrDocumentSHA256: path, APIVersion: "2026-03-10", RepositoryNodeID: "R_repo", HeadSHA: f.headSHA.String(), Variables: map[string]string{}, PerPage: f.limits.MaxItemsPerPage}
	if pr != nil {
		query.PullRequestNumber, query.PullRequestNodeID = pr.Number(), pr.NodeID()
	}
	page, err := NewPaginationPageV1(PaginationPageV1Input{Ordinal: 0, RequestedPage: 1, Response: f.snapshot, RawBodySHA256: strings.Repeat("7", 64), Items: []CanonicalPaginationItemV1{}, RESTLinkHeader: ""}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	closure, err := NewPaginationClosureV1(PaginationClosureV1Input{Query: query, Pages: []PaginationPageV1{page}, EvidenceRefs: []ledger.EvidenceRef{{URI: "evidence/page-" + string(source), Kind: "pagination", SHA256: strings.Repeat("6", 64)}}}, f.limits)
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
	proof := NotAppliedProofV1{Kind: NotAppliedAtomicBaseRejected, CommitmentSHA256: f.sealed.Commitment().SHA256(), ResponseRequestID: "request-2", EvidenceRef: evidence[0]}
	reconciled, err := NewMergeReconciliationResult(f.sealed, ReconciliationNotApplied, nil, &proof, evidence, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	failure := NewWriteExecutionError(attempt, true, errors.New("lost response"))
	if CanRetry(failure, 0, f.limits, &attempt, &reconciled) {
		t.Fatal("merge reconciliation improperly granted mutation retry authority")
	}
	unknown, err := NewMergeReconciliationResult(f.sealed, ReconciliationUnknown, nil, nil, evidence, f.limits)
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
	checks := make([]Check, f.limits.MaxTotalItems+1)
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

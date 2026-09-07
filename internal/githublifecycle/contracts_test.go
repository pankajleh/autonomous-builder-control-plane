package githublifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	f.resultTree = must(NewGitSHA(strings.Repeat("5", 40))).(GitSHA)
	f.pr = must(NewPullRequestIdentity(17, "PR_node_17")).(PullRequestIdentity)
	f.actor = must(NewAppInstallationIdentity("github-app:builder", 90210)).(ActingIdentity)
	f.snapshot = must(NewSnapshotIdentity("github", "request-1", time.Now().UnixNano())).(SnapshotIdentity)
	f.authority = must(NewAuthority(AuthorityInput{
		Repository: f.repository, BaseBranch: f.base, HeadBranch: f.head, HeadSHA: f.headSHA,
		ExpectedBaseTipSHA: f.baseSHA, PullRequest: &f.pr, AllowedMergeMethod: method, Actor: f.actor,
	})).(Authority)
	return f
}

func (f fixture) prInput() PullRequestSnapshotInput {
	return PullRequestSnapshotInput{
		Snapshot: f.snapshot, Repository: f.repository, PullRequest: f.pr, BaseBranch: f.base,
		BaseTipSHA: f.baseSHA, HeadBranch: f.head, HeadSHA: f.headSHA, State: PullRequestOpen,
		MergeMethod: f.authority.AllowedMergeMethod(),
	}
}

func TestAuthorityAndRemoteIdentityDriftFailClosed(t *testing.T) {
	f := newFixture(t, MergeMethodSquash)
	valid, err := NewPullRequestSnapshot(f.prInput(), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePullRequest(f.authority, valid); err != nil {
		t.Fatalf("valid PR: %v", err)
	}

	otherHead, _ := NewGitSHA(strings.Repeat("6", 40))
	movedHead := f.prInput()
	movedHead.HeadSHA = otherHead
	snapshot, err := NewPullRequestSnapshot(movedHead, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePullRequest(f.authority, snapshot); err == nil || !strings.Contains(err.Error(), "head moved") {
		t.Fatalf("expected moved-head failure, got %v", err)
	}

	otherBase, _ := NewGitSHA(strings.Repeat("7", 40))
	movedBase := f.prInput()
	movedBase.BaseTipSHA = otherBase
	snapshot, err = NewPullRequestSnapshot(movedBase, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePullRequest(f.authority, snapshot); err == nil || !strings.Contains(err.Error(), "base tip moved") {
		t.Fatalf("expected moved-base failure, got %v", err)
	}

	changedMethod := f.prInput()
	changedMethod.MergeMethod = MergeMethodMerge
	snapshot, err = NewPullRequestSnapshot(changedMethod, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePullRequest(f.authority, snapshot); err == nil || !strings.Contains(err.Error(), "merge method") {
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
	wrongActor, _ := NewUserIdentity("different-user")
	writeResult, err := NewPullRequestWriteResult(wrongActor, "pr-write-1", snapshot, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePullRequestWriteResult(f.authority, writeResult); err == nil {
		t.Fatal("pull request write result accepted the wrong acting identity")
	}
}

func TestStaleCIAndAmbiguousPullRequestsAreRejected(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	staleSHA, _ := NewGitSHA(strings.Repeat("8", 40))
	ci, err := NewCISnapshot(CISnapshotInput{
		Snapshot: f.snapshot, Repository: f.repository, HeadSHA: staleSHA,
		Checks: []Check{{NodeID: "check-1", Name: "test", Status: CheckCompleted, Conclusion: ConclusionSuccess, HeadSHA: staleSHA}},
	}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCI(f.authority, ci); err == nil || !strings.Contains(err.Error(), "stale") {
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
	mergeInput := MergeResultInput{
		Snapshot: f.snapshot, Repository: f.repository, PullRequest: f.pr, Actor: f.actor,
		AcceptedHeadSHA: f.headSHA, AcceptedHeadTree: f.headTree, BaseBeforeSHA: f.baseSHA,
		Method: MergeMethodSquash, ResultSHA: f.resultSHA, ResultTree: f.resultTree,
		Parents: []GitSHA{f.baseSHA}, Lineage: lineage,
	}
	merge, err := NewMergeResult(mergeInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := NewPostMergeObservation(PostMergeObservationInput{
		Snapshot: f.snapshot, Repository: f.repository, BaseBranch: f.base, PullRequest: f.pr, Actor: f.actor,
		AcceptedHeadSHA: f.headSHA, AcceptedHeadTree: f.headTree, BaseBeforeSHA: f.baseSHA,
		Method: MergeMethodSquash, ResultSHA: f.resultSHA, BaseAfterSHA: f.resultSHA, ResultTree: f.resultTree,
		Parents: []GitSHA{f.baseSHA}, Lineage: lineage,
	}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if f.resultSHA == f.headSHA {
		t.Fatal("fixture must exercise a synthesized SHA")
	}
	if err := VerifyPostMerge(f.authority, merge, observation); err != nil {
		t.Fatalf("valid divergent post-merge proof: %v", err)
	}

	badTree, _ := NewGitSHA(strings.Repeat("9", 40))
	badMergeInput := mergeInput
	badMergeInput.Lineage = cloneLineage(lineage)
	badMergeInput.Lineage[0].SourceTree = badTree
	badMerge, err := NewMergeResult(badMergeInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMergeResult(f.authority, badMerge); err == nil || !strings.Contains(err.Error(), "tree") {
		t.Fatalf("expected incorrect tree-lineage failure, got %v", err)
	}

	wrongActor, _ := NewUserIdentity("different-user")
	wrongActorInput := mergeInput
	wrongActorInput.Actor = wrongActor
	wrongActorResult, err := NewMergeResult(wrongActorInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMergeResult(f.authority, wrongActorResult); err == nil || !strings.Contains(err.Error(), "actor") {
		t.Fatalf("expected wrong actor failure, got %v", err)
	}
}

func TestMergeCommitRequiresExactOrderedLineage(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	input := MergeResultInput{
		Snapshot: f.snapshot, Repository: f.repository, PullRequest: f.pr, Actor: f.actor,
		AcceptedHeadSHA: f.headSHA, AcceptedHeadTree: f.headTree, BaseBeforeSHA: f.baseSHA,
		Method: MergeMethodMerge, ResultSHA: f.resultSHA, ResultTree: f.resultTree,
		Parents: []GitSHA{f.baseSHA, f.headSHA},
	}
	result, err := NewMergeResult(input, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMergeResult(f.authority, result); err != nil {
		t.Fatal(err)
	}
	input.Parents = []GitSHA{f.headSHA, f.baseSHA}
	result, err = NewMergeResult(input, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMergeResult(f.authority, result); err == nil {
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
	input := MergeResultInput{
		Snapshot: f.snapshot, Repository: f.repository, PullRequest: f.pr, Actor: f.actor,
		AcceptedHeadSHA: f.headSHA, AcceptedHeadTree: f.headTree, BaseBeforeSHA: f.baseSHA,
		Method: MergeMethodRebase, ResultSHA: f.resultSHA, ResultTree: f.resultTree,
		Parents: []GitSHA{firstResult}, Lineage: lineage,
	}
	result, err := NewMergeResult(input, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	lineage[0].Parents[0] = f.headSHA
	if err := ValidateMergeResult(f.authority, result); err != nil {
		t.Fatalf("valid copied rebase lineage: %v", err)
	}
	broken := input
	broken.Lineage = cloneLineage(input.Lineage)
	broken.Lineage[1].Parents[0] = f.baseSHA
	brokenResult, err := NewMergeResult(broken, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMergeResult(f.authority, brokenResult); err == nil || !strings.Contains(err.Error(), "chain") {
		t.Fatalf("expected broken rebase lineage failure, got %v", err)
	}
}

func TestSubmittedCancellationAndDeadlineAreAmbiguousAndNotRetried(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	attempt, err := NewWriteAttempt(f.repository, f.actor, "merge-write-1", f.limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		failure := NewWriteExecutionError("merge", attempt, true, cause)
		if failure.Class() != FailureAmbiguousWrite || !failure.Submitted() {
			t.Fatalf("submitted %v was not ambiguous", cause)
		}
		if CanRetry(failure, 0, f.limits, nil) {
			t.Fatal("ambiguous write retried without reconciliation")
		}
	}
	preSubmit := NewWriteExecutionError("merge", attempt, false, context.DeadlineExceeded)
	if preSubmit.Class() != FailureProviderUnavailable {
		t.Fatalf("pre-submit class = %s", preSubmit.Class())
	}

	evidence := []ledger.EvidenceRef{{URI: "evidence/reconcile.json", Kind: "reconciliation", SHA256: strings.Repeat("a", 64)}}
	reconciled, err := NewReconciliationResult(f.repository, f.actor, "merge-write-1", ReconciliationNotApplied, evidence, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	failure := NewWriteExecutionError("merge", attempt, true, errors.New("lost response"))
	if !CanRetry(failure, 0, f.limits, &reconciled) {
		t.Fatal("proved-not-applied write did not receive bounded retry authority")
	}
	unknown, err := NewReconciliationResult(f.repository, f.actor, "merge-write-1", ReconciliationUnknown, evidence, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if CanRetry(failure, 0, f.limits, &unknown) {
		t.Fatal("unknown reconciliation authorized retry")
	}
	other, err := NewReconciliationResult(f.repository, f.actor, "other-write", ReconciliationNotApplied, evidence, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if CanRetry(failure, 0, f.limits, &other) {
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
	if _, err := NewUpsertPullRequestInput(f.authority, strings.Repeat("x", f.limits.MaxTextBytes+1), "", f.limits); err == nil {
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
	if err != nil || policyFailure.Class() != FailurePolicy || CanRetry(policyFailure, 0, f.limits, nil) {
		t.Fatal("policy failure classification/retry is incorrect")
	}
}

func TestSnapshotsAreCopySafeAndCanonical(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	refA := ledger.EvidenceRef{URI: "evidence/a", Kind: "check", SHA256: strings.Repeat("a", 64)}
	refB := ledger.EvidenceRef{URI: "evidence/b", Kind: "check", SHA256: strings.Repeat("b", 64)}
	reviewA := Review{NodeID: "review-a", ReviewerID: "user-a", State: ReviewApproved, CommitSHA: f.headSHA}
	reviewB := Review{NodeID: "review-b", ReviewerID: "user-b", State: ReviewCommented, CommitSHA: f.headSHA}
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
	evidence := []ledger.EvidenceRef{{URI: "evidence/approval", Kind: "approval", SHA256: strings.Repeat("c", 64)}}
	input, err := NewMergeInput(f.authority, f.headTree, evidence, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	evidence[0].URI = "changed"
	returned := input.Evidence()
	returned[0].URI = "changed-again"
	if got := input.Evidence()[0].URI; got != "evidence/approval" {
		t.Fatalf("merge evidence mutated: %s", got)
	}
}

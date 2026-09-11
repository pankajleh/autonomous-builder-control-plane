package githublifecycle

import (
	"errors"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

func TestLimitsCanonicalIdentityCoversEveryGovernedField(t *testing.T) {
	base := DefaultLimits()
	baseJSON, err := base.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	baseSHA, err := base.SHA256()
	if err != nil || len(baseSHA) != 64 {
		t.Fatalf("limits digest = %q, %v", baseSHA, err)
	}
	mutations := []func(*Limits){
		func(l *Limits) { l.MaxRequiredTrustedChecks++ },
		func(l *Limits) { l.MaxEligibleReviewers++ },
		func(l *Limits) { l.MaxRequiredReviewers-- },
		func(l *Limits) { l.MaxMinimumApprovals-- },
		func(l *Limits) { l.MaxObservedChecks++ },
		func(l *Limits) { l.MaxObservedReviews++ },
		func(l *Limits) { l.MaxPaginationPages++; l.MaxPages++ },
		func(l *Limits) { l.MaxPaginationItemsPerPage++; l.MaxItemsPerPage++ },
		func(l *Limits) { l.MaxObservedItemsPerPaginationSource++; l.MaxTotalItems++ },
		func(l *Limits) { l.RequiredPaginationSources++ },
		func(l *Limits) { l.MaxPaginationClosureBytes++ },
		func(l *Limits) { l.MaxCumulativePaginationClosureBytes++ },
		func(l *Limits) { l.MaxTextBytes++ },
		func(l *Limits) { l.MaxEvidenceRefs++ },
		func(l *Limits) { l.MaxMetadataItems++ },
		func(l *Limits) { l.MaxReadyEvidenceClosureRefs++ },
		func(l *Limits) { l.MaxReadyAcceptedSources++ },
		func(l *Limits) { l.MaxReadyEvidenceArtifactBytes++ },
		func(l *Limits) { l.MaxReadyEvidenceClosureBytes++ },
		func(l *Limits) { l.MaxContractParents++; l.MaxParents++ },
		func(l *Limits) { l.MaxContractLineageEntries++; l.MaxLineageEntries++ },
		func(l *Limits) { l.RequiredProductionMergeParents++ },
		func(l *Limits) { l.MaxProductionMergeLineageEntries++ },
		func(l *Limits) { l.MaxDescendantDistance++ },
		func(l *Limits) { l.MaxCanonicalObjectBytes++ },
		func(l *Limits) { l.MaxCancellationAuthorityBytes++ },
		func(l *Limits) { l.MaxLedgerLineBytes++ },
		func(l *Limits) { l.MaxReadyLedgerSnapshotBytes++ },
		func(l *Limits) { l.MaxLedgerScanRecords++ },
		func(l *Limits) { l.MaxRequestBodyBytes++ },
		func(l *Limits) { l.MaxResponseHeaderBytes++ },
		func(l *Limits) { l.MaxLinkHeaderBytes++ },
		func(l *Limits) { l.MaxRequestIDBytes++ },
		func(l *Limits) { l.MaxCompressedResponseBodyBytes++ },
		func(l *Limits) { l.MaxDecompressedResponseBodyBytes++ },
		func(l *Limits) { l.MaxPreSubmitHTTPCalls++ },
		func(l *Limits) { l.MaxCommitObjectCreationSubmissions++ },
		func(l *Limits) { l.MaxTargetRefUpdateSubmissions++ },
		func(l *Limits) { l.MaxPostMergeHTTPCalls++ },
		func(l *Limits) { l.MaxReconciliationRounds++ },
		func(l *Limits) { l.MaxReconciliationCallsPerRound++ },
		func(l *Limits) { l.MaxPrincipalValidationCalls++ },
		func(l *Limits) { l.MaxHTTPCalls++ },
		func(l *Limits) { l.MaxCumulativeRequestBytes++ },
		func(l *Limits) { l.MaxCumulativeResponseHeaderBytes++ },
		func(l *Limits) { l.MaxCumulativeCompressedResponseBytes++ },
		func(l *Limits) { l.MaxCumulativeDecompressedResponseBytes++ },
		func(l *Limits) { l.CallTimeout++ },
		func(l *Limits) { l.MaxCumulativeActiveProviderCallTime++ },
		func(l *Limits) { l.MaxControllerInvocationTime++ },
		func(l *Limits) { l.MaxReadRetries++ },
		func(l *Limits) { l.MaxWriteRetries++ },
	}
	for index, mutate := range mutations {
		changed := base
		mutate(&changed)
		digest, err := changed.SHA256()
		if err != nil {
			t.Fatalf("mutation %d: %v", index, err)
		}
		if digest == baseSHA {
			t.Fatalf("mutation %d did not change policy identity", index)
		}
	}
	again, _ := base.CanonicalJSON()
	if string(again) != string(baseJSON) {
		t.Fatal("limits canonical JSON is nondeterministic")
	}
}

func TestStrictControllerRejectsLooserProviderSnapshotsEvenWithForgedPolicyID(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	loose := f.limits
	loose.MaxTextBytes = f.limits.MaxTextBytes * 2
	input := f.prInput()
	input.Reviews = []Review{{NodeID: strings.Repeat("n", f.limits.MaxTextBytes+1), DatabaseID: 1, Reviewer: StableIdentityV1{DatabaseID: 2, NodeID: "reviewer"}, State: ReviewApproved, CommitSHA: f.headSHA}}
	snapshot, err := NewPullRequestSnapshot(input, loose)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePullRequest(f.authority, snapshot, f.limits); err == nil || !strings.Contains(err.Error(), "limits policy") {
		t.Fatalf("expected limits identity mismatch, got %v", err)
	}
	strictSHA, _ := f.limits.SHA256()
	snapshot.immutable.data.LimitsSHA256 = strictSHA
	if err := ValidatePullRequest(f.authority, snapshot, f.limits); err == nil || !strings.Contains(err.Error(), "bounded revalidation") {
		t.Fatalf("forged policy identity bypassed actual rescan: %v", err)
	}

	ci, err := NewCISnapshot(CISnapshotInput{Snapshot: f.snapshot, Repository: f.repository, HeadSHA: f.headSHA,
		Checks: []Check{{NodeID: "check", Name: strings.Repeat("x", f.limits.MaxTextBytes+1), Identity: TrustedCheckIdentityV1{Context: strings.Repeat("x", f.limits.MaxTextBytes+1), Source: CheckSourceCheckRun, Producer: StableIdentityV1{DatabaseID: 1, NodeID: "producer"}}, Status: CheckCompleted, Conclusion: ConclusionSuccess, HeadSHA: f.headSHA}}}, loose)
	if err != nil {
		t.Fatal(err)
	}
	ci.immutable.data.LimitsSHA256 = strictSHA
	if err := ValidateCI(f.authority, ci, f.limits); err == nil || !strings.Contains(err.Error(), "bounded revalidation") {
		t.Fatalf("CI validator did not rescan strict text limits: %v", err)
	}

	if _, err := SelectPullRequest(f.authority, []PullRequestSnapshot{snapshot}, f.limits); err == nil || !strings.Contains(err.Error(), "bounded revalidation") {
		t.Fatalf("PR selection did not rescan strict limits: %v", err)
	}
}

func TestWriteAttemptBindsOperationAuthorityAndCanonicalPayload(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	first, err := NewUpsertPullRequestInput(f.authority, "first", "body", "same-write", f.limits)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewUpsertPullRequestInput(f.authority, "second", "body", "same-write", f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if first.Attempt().PayloadSHA256() == second.Attempt().PayloadSHA256() {
		t.Fatal("different PR payloads shared an attempt digest")
	}
	if first.Attempt().Operation() == f.mergeWrite.Attempt().Operation() || first.Attempt().AuthoritySHA256() == "" {
		t.Fatal("typed operation or authority digest was not bound")
	}

	evidence := []ledger.EvidenceRef{{URI: "evidence/reconcile.json", Kind: "reconciliation", SHA256: strings.Repeat("a", 64)}}
	mergeAttempt := f.mergeWrite.Attempt()
	failure := NewWriteExecutionError(mergeAttempt, true, errors.New("lost response"))
	crossOperation, err := NewReconciliationResult(first.Attempt(), ReconciliationNotApplied, evidence, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if CanRetry(failure, 0, f.limits, &mergeAttempt, &crossOperation) {
		t.Fatal("same write ID from another operation authorized replay")
	}
	wrongPayload, err := NewReconciliationResult(second.Attempt(), ReconciliationNotApplied, evidence, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	prFailure := NewWriteExecutionError(first.Attempt(), true, errors.New("lost response"))
	firstAttempt := first.Attempt()
	if CanRetry(prFailure, 0, f.limits, &firstAttempt, &wrongPayload) {
		t.Fatal("same write ID with another payload authorized replay")
	}
	valid, err := NewReconciliationResult(first.Attempt(), ReconciliationNotApplied, evidence, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	otherRepository, _ := NewRepository("other", "repository")
	otherActor, _ := NewUserIdentity("other-actor")
	mutations := []func(*WriteAttempt){
		func(a *WriteAttempt) { a.repository = otherRepository },
		func(a *WriteAttempt) { a.actor = otherActor },
		func(a *WriteAttempt) { a.writeID = "other-write" },
		func(a *WriteAttempt) { a.authoritySHA256 = strings.Repeat("1", 64) },
		func(a *WriteAttempt) { a.payloadSHA256 = strings.Repeat("2", 64) },
	}
	for index, mutate := range mutations {
		mismatch := valid
		mutate(&mismatch.attempt)
		if CanRetry(prFailure, 0, f.limits, &firstAttempt, &mismatch) {
			t.Fatalf("reconciliation field mutation %d authorized replay", index)
		}
	}
}

func TestSuccessfulResultsCannotReplaceOrOmitWriteAttempt(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	snapshot, err := NewPullRequestSnapshot(f.prInput(), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewUpsertPullRequestInput(f.authority, "title", "body", "pr-write", f.limits)
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewPullRequestWriteResult(request, snapshot, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	result.attempt = WriteAttempt{}
	if err := ValidatePullRequestWriteResult(request, result, f.limits); err == nil || !strings.Contains(err.Error(), "attempt") {
		t.Fatalf("missing PR write attempt accepted: %v", err)
	}
	result.attempt = request.Attempt()
	result.attempt.payloadSHA256 = strings.Repeat("b", 64)
	if err := ValidatePullRequestWriteResult(request, result, f.limits); err == nil || !strings.Contains(err.Error(), "attempt") {
		t.Fatalf("forged PR payload identity accepted: %v", err)
	}
}

func TestProviderSelfCertifiedWrongExpectedTreeFailsControllerValidation(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	wrongTree, _ := NewGitSHA(strings.Repeat("9", 40))
	forged := fakeExpectedContent(t, f.headSHA, f.baseSHA, wrongTree)
	data := MergeResultInput{
		Snapshot: f.snapshot, Repository: f.repository, PullRequest: f.pr, Actor: f.actor,
		AcceptedHeadSHA: f.headSHA, AcceptedHeadTree: wrongTree, BaseBeforeSHA: f.baseSHA,
		Method: MergeMethodMerge, ResultSHA: f.resultSHA, ResultTree: wrongTree,
		Parents: []GitSHA{f.baseSHA, f.headSHA}, Attempt: f.mergeWrite.Attempt(), ExpectedContent: forged, SealedAuthorization: f.sealed, Recipe: f.recipe,
	}
	if _, err := NewMergeResult(data, f.limits); err == nil {
		t.Fatalf("provider-certified wrong tree accepted: %v", err)
	}
}

func TestMergeAndPostMergeValidatorsRescanCardinalityAndLimitsIdentity(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	strict := f.limits
	strict.MaxPaginationClosureBytes--
	validResult := f.validMergeResult(t)
	observation := f.validPostMergeObservation(t, validResult, f.resultSHA, 0)
	if err := ValidateMergeResult(f.sealed, validResult, strict); err == nil {
		t.Fatal("merge validator accepted changed limits identity")
	}
	if err := VerifyPostMerge(f.sealed, validResult, observation, strict); err == nil {
		t.Fatal("post-merge validator accepted changed limits identity")
	}
}

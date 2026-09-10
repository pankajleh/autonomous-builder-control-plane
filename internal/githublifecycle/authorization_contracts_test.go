package githublifecycle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

func TestStrictAuthorityMergeInputAndSealRecovery(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	authorityJSON, err := f.authority.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	recoveredAuthority, err := ParseCanonicalAuthority(authorityJSON, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	originalSHA, _ := f.authority.SHA256()
	recoveredSHA, _ := recoveredAuthority.SHA256()
	if recoveredSHA != originalSHA {
		t.Fatal("authority recovery changed identity")
	}
	if _, err := ParseCanonicalAuthority(append(authorityJSON, '\n'), f.limits); err == nil {
		t.Fatal("non-canonical authority accepted")
	}
	unknown := bytes.Replace(authorityJSON, []byte(`"repository":`), []byte(`"unknown":1,"repository":`), 1)
	if _, err := ParseCanonicalAuthority(unknown, f.limits); err == nil {
		t.Fatal("unknown authority field accepted")
	}

	recoveredInput, err := ParseCanonicalMergeInput(f.mergeWrite.CanonicalPayload(), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredInput.SHA256() != f.mergeWrite.SHA256() || recoveredInput.Attempt() != f.mergeWrite.Attempt() {
		t.Fatal("merge input recovery changed attempt or digest")
	}
	tampered := bytes.Replace(f.mergeWrite.CanonicalPayload(), []byte(f.recipe.SHA256()), []byte(strings.Repeat("0", 64)), 1)
	if _, err := ParseCanonicalMergeInput(tampered, f.limits); err == nil {
		t.Fatal("tampered nested recipe digest accepted")
	}

	recoveredSealed, err := ParseCanonicalSealedMergeAuthorizationV1(f.sealed.CanonicalJSON(), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredSealed.SHA256() != f.sealed.SHA256() || recoveredSealed.MergeInput().Attempt() != f.mergeWrite.Attempt() {
		t.Fatal("sealed recovery changed identity")
	}
	sealedUnknown := bytes.Replace(f.sealed.CanonicalJSON(), []byte(`"limits_sha256":`), []byte(`"unexpected":true,"limits_sha256":`), 1)
	if _, err := ParseCanonicalSealedMergeAuthorizationV1(sealedUnknown, f.limits); err == nil {
		t.Fatal("unknown sealed field accepted")
	}
}

func TestAuthoritativePREligibilityAndDismissedReviewFailClosed(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	base := f.prAuth.Input()
	for name, mutate := range map[string]func(*AuthoritativePullRequestSnapshotV1Input){
		"closed":        func(i *AuthoritativePullRequestSnapshotV1Input) { state := PullRequestClosed; i.State = &state },
		"draft":         func(i *AuthoritativePullRequestSnapshotV1Input) { value := true; i.IsDraft = &value },
		"merged":        func(i *AuthoritativePullRequestSnapshotV1Input) { value := true; i.Merged = &value },
		"merged_at":     func(i *AuthoritativePullRequestSnapshotV1Input) { value := int64(1); i.MergedAtUnixNano = &value },
		"fork":          func(i *AuthoritativePullRequestSnapshotV1Input) { i.HeadRepositoryNodeID = "R_fork" },
		"synthetic_ref": func(i *AuthoritativePullRequestSnapshotV1Input) { i.HeadRef = "refs/pull/17/head" },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			mutate(&input)
			if _, err := NewAuthoritativePullRequestSnapshotV1(input, f.limits); err == nil {
				t.Fatal("ineligible PR accepted")
			}
		})
	}

	reviewer := StableIdentityV1{DatabaseID: 42, NodeID: "U_reviewer"}
	policyInput := f.authority.MergePolicy().Input()
	policyInput.EligibleReviewers = []StableIdentityV1{reviewer}
	policyInput.RequiredReviewers = []StableIdentityV1{reviewer}
	policyInput.MinimumApprovals = 1
	policy, err := NewMergePolicyV1(policyInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	authorityInput := f.authority.Input()
	authorityInput.MergePolicy = policy
	// The policy-authority digest deliberately remains fixed to the same READY,
	// repository, principal, and source authority; only reviewer rules change.
	authority, err := NewAuthority(authorityInput)
	if err != nil {
		t.Fatal(err)
	}
	reviews := []Review{{NodeID: "R1", DatabaseID: 1, Reviewer: reviewer, State: ReviewDismissed, CommitSHA: f.headSHA}, {NodeID: "R2", DatabaseID: 2, Reviewer: reviewer, State: ReviewApproved, CommitSHA: f.headSHA}}
	items := reviewPaginationItems(reviews)
	closure := paginationClosureWithItems(t, f, PaginationReviews, "/repos/octo-org/control-plane/pulls/17/reviews", &f.pr, items)
	prInput := base
	prInput.Reviews, prInput.ReviewsClosure = reviews, closure
	pr, err := NewAuthoritativePullRequestSnapshotV1(prInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := EvaluateMergePolicyV1(authority, pr, nil, f.checkRuns, f.statuses, f.limits); err == nil || !strings.Contains(err.Error(), "unblocked") {
		t.Fatalf("dismissed exact-head review plus approval did not block: %v", err)
	}
}

func TestTrustedChecksAndIndependentPaginationBoundaries(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	bad := Check{NodeID: "check", Name: "build", Status: CheckCompleted, Conclusion: ConclusionSuccess, HeadSHA: f.headSHA}
	if _, err := NewCISnapshot(CISnapshotInput{Snapshot: f.snapshot, Repository: f.repository, HeadSHA: f.headSHA, Checks: []Check{bad}}, f.limits); err == nil {
		t.Fatal("check without stable producer provenance accepted")
	}

	limits := f.limits
	limits.MaxPages = 2
	limits.MaxItemsPerPage = 1
	limits.MaxTotalItems = 2
	scope := PaginationQueryScopeV1{Source: PaginationCheckRuns, Repository: f.repository, RepositoryNodeID: "R_repo", HeadSHA: f.headSHA}
	query, err := DerivePaginationQueryV1(scope, limits)
	if err != nil {
		t.Fatal(err)
	}
	item1 := CanonicalPaginationItemV1{Key: "one", SHA256: strings.Repeat("1", 64)}
	item2 := CanonicalPaginationItemV1{Key: "two", SHA256: strings.Repeat("2", 64)}
	response1, _ := NewSnapshotIdentity("github", "pagination-request-1", 1700000004000000000)
	response2, _ := NewSnapshotIdentity("github", "pagination-request-2", 1700000005000000000)
	evidence1 := ledger.EvidenceRef{URI: "evidence/page-1", Kind: "github-response-body", SHA256: strings.Repeat("3", 64)}
	evidence2 := ledger.EvidenceRef{URI: "evidence/page-2", Kind: "github-response-body", SHA256: strings.Repeat("4", 64)}
	link := "<https://api.github.com/repos/octo-org/control-plane/commits/" + f.headSHA.String() + "/check-runs?filter=all&page=2&per_page=1>; rel=\"next\""
	p1Input := PaginationPageV1Input{Ordinal: 0, RequestedPage: 1, Response: response1, RawBodySHA256: evidence1.SHA256, ResponseEvidence: evidence1, Items: []CanonicalPaginationItemV1{item1}, RESTLinkHeader: link, RESTLinkObserved: true}
	p1Input.EnvelopeEvidence, err = NewPaginationEnvelopeEvidenceV1("evidence/page-envelope-1", p1Input, limits)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := NewPaginationPageV1(p1Input, limits)
	if err != nil {
		t.Fatal(err)
	}
	p2Input := PaginationPageV1Input{Ordinal: 1, RequestedPage: 2, Response: response2, RawBodySHA256: evidence2.SHA256, ResponseEvidence: evidence2, Items: []CanonicalPaginationItemV1{item2}, RESTLinkObserved: true}
	p2Input.EnvelopeEvidence, err = NewPaginationEnvelopeEvidenceV1("evidence/page-envelope-2", p2Input, limits)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := NewPaginationPageV1(p2Input, limits)
	if err != nil {
		t.Fatal(err)
	}
	evidence := []ledger.EvidenceRef{evidence1, p1Input.EnvelopeEvidence, evidence2, p2Input.EnvelopeEvidence}
	closure, err := NewPaginationClosureV1(PaginationClosureV1Input{query, []PaginationPageV1{p1, p2}, evidence}, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePaginationClosureV1(scope, closure, []CanonicalPaginationItemV1{item2, item1}, limits); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPaginationClosureV1(PaginationClosureV1Input{query, []PaginationPageV1{p1}, evidence}, limits); err == nil {
		t.Fatal("missing terminal page accepted")
	}
	duplicate := p2.Input()
	duplicate.Items = []CanonicalPaginationItemV1{item1}
	duplicate.EnvelopeEvidence, err = NewPaginationEnvelopeEvidenceV1("evidence/page-envelope-2-duplicate", duplicate, limits)
	if err != nil {
		t.Fatal(err)
	}
	p2dup, err := NewPaginationPageV1(duplicate, limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPaginationClosureV1(PaginationClosureV1Input{query, []PaginationPageV1{p1, p2dup}, evidence}, limits); err == nil {
		t.Fatal("cross-page duplicate accepted")
	}
	excess := limits
	excess.MaxPages = 1
	if _, err := NewPaginationClosureV1(PaginationClosureV1Input{query, []PaginationPageV1{p1, p2}, evidence}, excess); err == nil {
		t.Fatal("pagination limit+1 accepted")
	}
	if _, err := ParseCanonicalPaginationClosureV1(append(closure.CanonicalJSON(), ' '), limits); err == nil {
		t.Fatal("non-canonical pagination closure accepted")
	}
}

func TestDeterministicRecipeCapabilityCommitmentAndReconciliation(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	unsupportedPolicy := f.authority.MergePolicy().Input()
	unsupportedPolicy.Method = MergeMethodSquash
	if _, err := NewMergePolicyV1(unsupportedPolicy, f.limits); err == nil {
		t.Fatal("production-v1 squash policy accepted")
	}
	recipeAgain, err := NewMergeCommitRecipeV1("merge-write-1", f.authority, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if recipeAgain.ExpectedResultSHA() != f.resultSHA || !bytes.Equal(recipeAgain.CommitBytes(), f.recipe.CommitBytes()) {
		t.Fatal("deterministic recipe changed result OID")
	}
	tamperedRecipe := bytes.Replace(f.recipe.CanonicalJSON(), []byte("Merge authorized head"), []byte("Merge caller-changed head"), 1)
	if _, err := ParseCanonicalMergeCommitRecipeV1(tamperedRecipe, f.authority, f.limits); err == nil {
		t.Fatal("caller-tampered policy recipe accepted")
	}
	badCapability := f.mergeWrite.Capability().input
	badCapability.SupportsNoOp = false
	if _, err := NewProviderCapabilityV1(badCapability, f.limits); err == nil {
		t.Fatal("capability without head no-op support accepted")
	}
	commitment := f.sealed.Commitment()
	if !commitment.valid() || commitment.updates[0].AfterOID != f.resultSHA || commitment.updates[1].BeforeOID != f.headSHA || commitment.updates[1].AfterOID != f.headSHA || commitment.updates[0].Force || commitment.updates[1].Force {
		t.Fatal("target commitment is not exact base update plus head no-op CAS")
	}
	if _, err := NewReconcileWriteInput(f.sealed, f.limits); err == nil {
		t.Fatal("attempt-only merge reconciliation input accepted")
	}
	reconcileEvidence := []ledger.EvidenceRef{{URI: "evidence/reconcile-observation", Kind: "reconciliation-observation", SHA256: strings.Repeat("c", 64)}}
	reconcileInput, err := NewMergeReconcileWriteInput(f.sealed, reconcileEvidence, f.limits)
	if err != nil || reconcileInput.Attempt() != f.mergeWrite.Attempt() || len(reconcileInput.ObservationEvidence()) != 1 {
		t.Fatalf("full merge reconciliation input: %v", err)
	}

	result := f.validMergeResult(t)
	applied, err := NewMergeReconciliationResult(f.sealed, ReconciliationApplied, &result, nil, []ledger.EvidenceRef{{URI: "evidence/reconcile", Kind: "reconciliation", SHA256: strings.Repeat("a", 64)}}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateReconciliationResult(f.sealed, applied, f.limits); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMergeReconciliationResult(f.sealed, ReconciliationApplied, nil, nil, applied.Evidence(), f.limits); err == nil {
		t.Fatal("APPLIED reconciliation without materialized MergeResult accepted")
	}
	other := f.sealed
	other.digest = strings.Repeat("b", 64)
	if err := ValidateReconciliationResult(other, applied, f.limits); err == nil {
		t.Fatal("reconciliation accepted a different sealed input")
	}
}

func TestCancellationAuthorityStrictDurableAndAppliedPrecedence(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	ready := f.authority.ReadyBinding()
	authRef := ledger.EvidenceRef{URI: "evidence/cancel-authn", Kind: "authentication", SHA256: strings.Repeat("1", 64)}
	policyRef := ledger.EvidenceRef{URI: "evidence/cancel-policy", Kind: "cancellation-policy", SHA256: strings.Repeat("2", 64)}
	requestRef := ledger.EvidenceRef{URI: "evidence/cancel-request", Kind: "cancellation-request", SHA256: strings.Repeat("3", 64)}
	attempt := f.mergeWrite.Attempt()
	response, _ := NewSnapshotIdentity("github", "target-attempt-1", 1700000004000000000)
	responseBody := atomicRejectionResponseBody(t, 1)
	proofRef := ledger.EvidenceRef{URI: "evidence/not-applied", Kind: NotAppliedAtomicRejectionEvidenceKindV1, SHA256: digestBytes(responseBody)}
	notApplied, err := NewNotAppliedProofV1(NotAppliedProofV1Input{Kind: NotAppliedAtomicHeadRejected, RequestID: "target-attempt-1", RequestBodySHA256: f.sealed.Commitment().SHA256(), RequestBytes: 100, Response: &response, HTTPStatus: 200, ResponseBodySHA256: digestBytes(responseBody), ResponseBody: responseBody, EvidenceRef: proofRef}, f.sealed, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	submissionProof, err := NewCancellationSubmissionProofV1(CancellationSubmissionProofV1Input{Kind: CancellationProofAuthenticatedNotApplied, AdmissionSHA256: f.mergeWrite.SHA256(), Attempt: &attempt, SealSHA256: f.sealed.Seal().SHA256(), CommitmentSHA256: f.sealed.Commitment().SHA256(), RequestBytes: 100, SubmissionState: ReconciliationNotApplied, NotAppliedProof: &notApplied, EvidenceRef: proofRef}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCancellationSubmissionProofV1(CancellationSubmissionProofV1Input{Kind: CancellationProofAuthenticatedNotApplied, AdmissionSHA256: f.mergeWrite.SHA256(), Attempt: &attempt, SealSHA256: f.sealed.Seal().SHA256(), CommitmentSHA256: f.sealed.Commitment().SHA256(), RequestBytes: 101, SubmissionState: ReconciliationNotApplied, NotAppliedProof: &notApplied, EvidenceRef: proofRef}, f.limits); err == nil {
		t.Fatal("cancellation submission proof accepted a byte count different from its exact NOT_APPLIED proof")
	}
	cancellationEvidence := []ledger.EvidenceRef{authRef, policyRef, requestRef, proofRef}
	cancellationEvidence = append(cancellationEvidence, f.readyProof.input.EvidenceRefs...)
	input := CancellationAuthorityV1Input{ProjectID: ready.input.ProjectID, PlanID: ready.input.PlanID, RunID: ready.input.RunID, RepositoryBindingSHA256: ready.RepositoryBinding().SHA256(), Phase3AuthoritySHA256: ready.input.Phase3AuthoritySHA256, ReadyEventSHA256: ready.input.ReadyEventSHA256, ReadyEventID: ready.input.ReadyEventID, ReadyRunStateSequence: ready.input.ReadyRunStateSequence, ReadyBindingSHA256: ready.SHA256(), LedgerPrefixSHA256: ready.input.LedgerPrefixSHA256, LedgerPrefixLength: ready.input.LedgerPrefixLength, CurrentReadyProof: f.readyProof, Boundary: CancellationTargetNotApplied, ReceiptUnixNano: 1700000005000000000, IngressSequence: 12, AdmissionSHA256: f.mergeWrite.SHA256(), Attempt: &attempt, SealSHA256: f.sealed.Seal().SHA256(), CommitmentSHA256: f.sealed.Commitment().SHA256(), SubmissionProof: submissionProof, Requester: StablePrincipalV1{Kind: "user", Identity: StableIdentityV1{DatabaseID: 7, NodeID: "U_cancel"}}, AuthenticationEvidence: authRef, CancellationPolicyVersion: "cancel-v1", CancellationPolicySource: policyRef, CancellationPolicySHA256: strings.Repeat("6", 64), ScopedGrantSHA256: strings.Repeat("7", 64), AllowDecisionSHA256: strings.Repeat("8", 64), SourceRequestID: "cancel-request-1", SourceKind: "api", RequestEvidence: requestRef, IngressID: "ingress-1", EvidenceRefs: cancellationEvidence}
	authority, err := NewCancellationAuthorityV1(input, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := ParseCanonicalCancellationAuthorityV1(authority.CanonicalJSON(), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	expected := CancellationAuthorityExpectationV1{ProjectID: input.ProjectID, PlanID: input.PlanID, RunID: input.RunID, ReadyBinding: ready, CurrentReadyProof: input.CurrentReadyProof, AdmissionSHA256: input.AdmissionSHA256, Requester: input.Requester, AuthenticationEvidence: input.AuthenticationEvidence, CancellationPolicyVersion: input.CancellationPolicyVersion, CancellationPolicySource: input.CancellationPolicySource, CancellationPolicySHA256: input.CancellationPolicySHA256, ScopedGrantSHA256: input.ScopedGrantSHA256, AllowDecisionSHA256: input.AllowDecisionSHA256, Boundary: input.Boundary, Attempt: &attempt, SealSHA256: input.SealSHA256, CommitmentSHA256: input.CommitmentSHA256, SealedAuthorization: f.sealed, SubmissionProof: input.SubmissionProof, SourceRequestID: input.SourceRequestID, SourceKind: input.SourceKind, RequestEvidence: input.RequestEvidence, IngressID: input.IngressID, ReceiptUnixNano: input.ReceiptUnixNano, IngressSequence: input.IngressSequence, EvidenceRefs: input.EvidenceRefs}
	if err := ValidateCancellationAuthorityV1(recovered, expected, f.limits); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*CancellationAuthorityExpectationV1){
		"current READY proof": func(v *CancellationAuthorityExpectationV1) { v.CurrentReadyProof.digest = strings.Repeat("0", 64) },
		"authentication":      func(v *CancellationAuthorityExpectationV1) { v.AuthenticationEvidence = requestRef },
		"policy version":      func(v *CancellationAuthorityExpectationV1) { v.CancellationPolicyVersion = "other" },
		"grant":               func(v *CancellationAuthorityExpectationV1) { v.ScopedGrantSHA256 = strings.Repeat("0", 64) },
		"sealed authorization": func(v *CancellationAuthorityExpectationV1) {
			v.SealedAuthorization = SealedMergeAuthorizationV1{}
		},
		"source kind":      func(v *CancellationAuthorityExpectationV1) { v.SourceKind = "cli" },
		"request evidence": func(v *CancellationAuthorityExpectationV1) { v.RequestEvidence = authRef },
		"evidence closure": func(v *CancellationAuthorityExpectationV1) { v.EvidenceRefs = v.EvidenceRefs[:1] },
	} {
		forged := expected
		forged.EvidenceRefs = append([]ledger.EvidenceRef(nil), expected.EvidenceRefs...)
		mutate(&forged)
		if err := ValidateCancellationAuthorityV1(recovered, forged, f.limits); err == nil {
			t.Fatalf("cancellation accepted forged %s expectation", name)
		}
	}
	forgedInput := input
	forgedInput.SubmissionProof = cloneCancellationSubmissionProof(input.SubmissionProof)
	forgedInput.SubmissionProof.input.RequestBytes++
	if _, err := NewCancellationAuthorityV1(forgedInput, f.limits); err == nil {
		t.Fatal("cancellation authority accepted submission-proof fields inconsistent with its exact canonical bytes")
	}
	if err := AuthorizeCancelledV1(DurableCancellationAuthorityV1{}, expected, ReconciliationNotApplied, f.limits, notApplied); err == nil {
		t.Fatal("non-durable cancellation authority selected CANCELLED")
	}
	channel := ledger.EvidenceRef{URI: "evidence/cancel-channel", Kind: "durable-channel", SHA256: strings.Repeat("9", 64)}
	replayEvidence := ledger.EvidenceRef{URI: "evidence/cancel-replay", Kind: "replay-index", SHA256: strings.Repeat("a", 64)}
	replay, err := NewCancellationReplayIdentityV1(authority, replayEvidence, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	forgedReplay := replay
	forgedReplay.sourceKind = "cli"
	if _, err := NewDurableCancellationAuthorityV1(authority, channel, forgedReplay, f.limits); err == nil {
		t.Fatal("durable cancellation accepted a replay identity under a different source-kind/request-ID key")
	}
	durable, err := NewDurableCancellationAuthorityV1(authority, channel, replay, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := AuthorizeCancelledV1(durable, expected, ReconciliationNotApplied, f.limits, notApplied); err != nil {
		t.Fatal(err)
	}
	if err := AuthorizeCancelledV1(durable, expected, ReconciliationApplied, f.limits); err == nil {
		t.Fatal("APPLIED did not defeat cancellation")
	}
	if _, err := ParseCanonicalCancellationAuthorityV1(append(authority.CanonicalJSON(), '\n'), f.limits); err == nil {
		t.Fatal("non-canonical cancellation authority accepted")
	}
}

func TestPostMergeExactObjectAndDescendantContainment(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	result := f.validMergeResult(t)
	identical := f.validPostMergeObservation(t, result, f.resultSHA, 0)
	if err := VerifyPostMerge(f.sealed, result, identical, f.limits); err != nil {
		t.Fatal(err)
	}
	descendant, _ := NewGitSHA(strings.Repeat("a", 40))
	advanced := f.validPostMergeObservation(t, result, descendant, 3)
	if err := VerifyPostMerge(f.sealed, result, advanced, f.limits); err != nil {
		t.Fatalf("proved descendant target rejected: %v", err)
	}
	bad := advanced.Input()
	bad.ResultObject.input.ResultTree = descendant
	forged := advanced
	forged.immutable.data = bad
	if err := VerifyPostMerge(f.sealed, result, forged, f.limits); err == nil {
		t.Fatal("wrong exact result object accepted")
	}
	proof := advanced.Input().ContainmentProof.input
	proof.MergeBaseSHA = f.baseSHA
	if _, err := NewTargetContainmentProofV1(proof, f.limits); err == nil {
		t.Fatal("sibling/non-descendant containment accepted")
	}
	proof = advanced.Input().ContainmentProof.input
	proof.DescendantDistance = f.limits.MaxDescendantDistance + 1
	if _, err := NewTargetContainmentProofV1(proof, f.limits); err == nil {
		t.Fatal("containment distance limit+1 accepted")
	}
}

func reviewPaginationItems(reviews []Review) []CanonicalPaginationItemV1 {
	items := make([]CanonicalPaginationItemV1, len(reviews))
	for index, review := range reviews {
		raw, _ := json.Marshal(reviewWire{review.NodeID, review.DatabaseID, review.Reviewer, review.State, review.CommitSHA.String()})
		items[index] = CanonicalPaginationItemV1{Key: fmt.Sprintf("%d/%s", review.DatabaseID, review.NodeID), SHA256: digestBytes(raw)}
	}
	return items
}

func paginationClosureWithItems(t *testing.T, f fixture, source PaginationSourceKind, path string, pr *PullRequestIdentity, items []CanonicalPaginationItemV1) PaginationClosureV1 {
	t.Helper()
	_ = path
	query, err := DerivePaginationQueryV1(PaginationQueryScopeV1{Source: source, Repository: f.repository, RepositoryNodeID: "R_repo", PullRequest: pr, HeadSHA: f.headSHA}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	response, err := NewSnapshotIdentity("github", "pagination-items-"+string(source), f.snapshot.ObservedUnixNano())
	if err != nil {
		t.Fatal(err)
	}
	bodyEvidence := ledger.EvidenceRef{URI: "evidence/items-body-" + string(source), Kind: GitHubPaginationBodyEvidenceKindV1, SHA256: strings.Repeat("7", 64)}
	pageInput := PaginationPageV1Input{Ordinal: 0, RequestedPage: 1, Response: response, RawBodySHA256: bodyEvidence.SHA256, ResponseEvidence: bodyEvidence, Items: items, RESTLinkObserved: true}
	pageInput.EnvelopeEvidence, err = NewPaginationEnvelopeEvidenceV1("evidence/items-envelope-"+string(source), pageInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	page, err := NewPaginationPageV1(pageInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	closure, err := NewPaginationClosureV1(PaginationClosureV1Input{query, []PaginationPageV1{page}, []ledger.EvidenceRef{bodyEvidence, pageInput.EnvelopeEvidence}}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	return closure
}

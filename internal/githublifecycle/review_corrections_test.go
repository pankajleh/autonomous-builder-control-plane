package githublifecycle

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

func TestFinalRevalidationRejectsAdmissionObservationReuseAndDecisionForgery(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	final := f.sealed.Seal().input.FinalRevalidation
	input := final.Input()

	reused := input.PullRequest.Input()
	snapshot, err := NewSnapshotIdentity("github", f.prAuth.input.Snapshot.RequestID(), input.PullRequest.input.Snapshot.ObservedUnixNano())
	if err != nil {
		t.Fatal(err)
	}
	reused.Snapshot = snapshot
	reusedPR, err := NewAuthoritativePullRequestSnapshotV1(reused, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	input.PullRequest = reusedPR
	if _, err := NewFinalRevalidationV1(input, f.limits); err == nil || !strings.Contains(err.Error(), "reused") {
		t.Fatalf("admission request identity reused as final evidence: %v", err)
	}

	forged := bytes.Replace(final.CanonicalJSON(), []byte(final.FinalDecisionSHA256()), []byte(strings.Repeat("0", 64)), 1)
	if _, err := ParseCanonicalFinalRevalidationV1(forged, f.mergeWrite, f.limits); err == nil {
		t.Fatal("forged final authorization decision digest accepted")
	}

	sealInput := f.sealed.Seal().Input()
	sealInput.FinalRevalidation.input.CurrentReadyProof.input.NoLaterTransition = false
	if _, err := NewAuthorizationSealV1(sealInput, f.limits); err == nil {
		t.Fatal("seal accepted a forged non-current READY proof")
	}
	readyProofInput := final.Input().CurrentReadyProof.Input()
	readyProofInput.ObservedLedgerSHA256 = strings.Repeat("f", 64)
	if _, err := NewCurrentReadyProofV1(readyProofInput, f.limits); err == nil {
		t.Fatal("current READY proof accepted a changed same-length ledger prefix")
	}
}

func TestReadyAuthorityRejectsIncompleteClosureControllerAndLedgerForgery(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	ready := f.authority.ReadyBinding().Input()

	incomplete := ready
	incomplete.EvidenceClosureRefs = append([]ledger.EvidenceRef(nil), ready.ReadyEvidenceRefs...)
	if _, err := NewReadyAuthorityBindingV1(incomplete, f.limits); err == nil {
		t.Fatal("READY binding accepted incomplete accepted-source/configuration evidence closure")
	}

	wrongController := ready
	var event map[string]any
	if err := json.Unmarshal(wrongController.ReadyEventJSON, &event); err != nil {
		t.Fatal(err)
	}
	event["actor"] = "caller"
	wrongController.ReadyEventJSON, _ = json.Marshal(event)
	wrongController.ReadyEventSHA256 = digestBytes(wrongController.ReadyEventJSON)
	wrongController.LedgerPrefixLength = wrongController.ReadyEventByteOffset + int64(len(wrongController.ReadyEventJSON)) + 1
	if _, err := NewReadyAuthorityBindingV1(wrongController, f.limits); err == nil {
		t.Fatal("READY binding accepted a caller actor")
	}

	incoherent := ready
	incoherent.LedgerPrefixLength++
	if _, err := NewReadyAuthorityBindingV1(incoherent, f.limits); err == nil {
		t.Fatal("READY binding accepted incoherent event offset/prefix length")
	}
}

func TestPaginationRejectsSelfCertifiedSourcePathAndAmbiguousTerminal(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	scope := PaginationQueryScopeV1{Source: PaginationCheckRuns, Repository: f.repository, RepositoryNodeID: "R_repo", HeadSHA: f.headSHA}
	wrong := f.checkRuns.Input()
	wrong.Query.Source = PaginationCommitStatuses
	wrong.Query.PathOrDocumentSHA256 = "/repos/octo-org/control-plane/commits/" + f.headSHA.String() + "/statuses"
	wrong.Query.Variables = map[string]string{}
	wrongClosure, err := NewPaginationClosureV1(wrong, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePaginationClosureV1(scope, wrongClosure, nil, f.limits); err == nil {
		t.Fatal("self-certified wrong pagination source accepted")
	}

	wrongPath := f.checkRuns.Input()
	wrongPath.Query.PathOrDocumentSHA256 = "/repos/octo-org/control-plane/check-runs"
	wrongPathClosure, err := NewPaginationClosureV1(wrongPath, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePaginationClosureV1(scope, wrongPathClosure, nil, f.limits); err == nil {
		t.Fatal("self-certified wrong pagination path accepted")
	}

	ambiguousPageInput := f.checkRuns.input.Pages[0].Input()
	ambiguousPageInput.RESTLinkObserved = false
	ambiguousPage, err := NewPaginationPageV1(ambiguousPageInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	ambiguous := f.checkRuns.Input()
	ambiguous.Pages = []PaginationPageV1{ambiguousPage}
	if _, err := NewPaginationClosureV1(ambiguous, f.limits); err == nil {
		t.Fatal("empty terminal page without retained Link-header observation accepted")
	}

	duplicatePageInput := f.statuses.input.Pages[0].Input()
	duplicatePageInput.Response, _ = NewSnapshotIdentity("github", f.checkRuns.input.Pages[0].input.Response.RequestID(), duplicatePageInput.Response.ObservedUnixNano())
	duplicatePage, err := NewPaginationPageV1(duplicatePageInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	duplicateStatusesInput := f.statuses.Input()
	duplicateStatusesInput.Pages = []PaginationPageV1{duplicatePage}
	duplicateStatuses, err := NewPaginationClosureV1(duplicateStatusesInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	merge := f.mergeWrite
	if _, err := NewMergeInput(MergeAuthorizationInputV1{
		Authority: merge.authority, PolicyDecisionSHA256: merge.policyDecisionSHA256, InitialPullRequest: merge.initialPullRequest,
		Checks: merge.checks, CheckRunsClosure: merge.checkRunsClosure, CommitStatusesClosure: duplicateStatuses,
		Capability: merge.capability, Recipe: merge.recipe, EvidenceRefs: merge.Evidence(),
	}, merge.attempt.WriteID(), f.limits); err == nil {
		t.Fatal("duplicate response request identity across admission sources accepted")
	}
}

func TestRecipeIsPolicyDerivedObjectFormatConsistentAndMutationIdentityIsUnique(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	recipe := f.recipe.Input()
	if recipe.Message != "Merge authorized head\n\nABCP-Write-ID: "+f.mergeWrite.Attempt().WriteID() ||
		recipe.AuthorUnix != f.authority.ReadyBinding().input.ReadyEventUnixNano/1_000_000_000 ||
		recipe.CommitterUnix != recipe.AuthorUnix || recipe.Author != f.authority.MergePolicy().input.Recipe.Author ||
		recipe.Committer != f.authority.MergePolicy().input.Recipe.Committer {
		t.Fatal("merge recipe bytes were not wholly derived from policy, READY time, and write identity")
	}

	sha256PolicyInput := f.authority.MergePolicy().Input()
	sha256PolicyInput.Recipe.ObjectFormat = "sha256"
	sha256Policy, err := NewMergePolicyV1(sha256PolicyInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	sha256AuthorityInput := f.authority.Input()
	sha256AuthorityInput.MergePolicy = sha256Policy
	sha256Authority, err := NewAuthority(sha256AuthorityInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewMergeCommitRecipeV1("merge-write-1", sha256Authority, f.limits); err == nil {
		t.Fatal("SHA-256 recipe accepted 40-hex SHA-1 authority OIDs")
	}

	seal := f.sealed.Seal()
	first, err := NewTargetRefCommitmentV1(f.mergeWrite, seal, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewTargetRefCommitmentV1(f.mergeWrite, seal, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if first.clientMutationID != f.mergeWrite.Attempt().WriteID() || first.SHA256() != second.SHA256() ||
		!bytes.Equal(first.CanonicalJSON(), second.CanonicalJSON()) {
		t.Fatal("one seal did not deterministically produce one write-derived mutation identity")
	}
	forged := bytes.Replace(first.CanonicalJSON(), []byte(f.mergeWrite.Attempt().WriteID()), []byte("caller-mutation-id"), 1)
	if _, err := ParseCanonicalTargetRefCommitmentV1(forged, f.mergeWrite, seal, f.limits); err == nil {
		t.Fatal("caller-selected clientMutationID accepted")
	}
}

func TestNotAppliedProofRejectsForgedBindingAndAmbiguousZeroBytes(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	response, _ := NewSnapshotIdentity("github", "not-applied-response", 1700000007000000000)
	evidence := ledger.EvidenceRef{URI: "evidence/atomic-rejection", Kind: NotAppliedAtomicRejectionEvidenceKindV1, SHA256: strings.Repeat("a", 64)}
	proof, err := NewNotAppliedProofV1(NotAppliedProofV1Input{
		Kind: NotAppliedAtomicBaseRejected, RequestID: "target-request-1", RequestBodySHA256: f.sealed.Commitment().SHA256(),
		RequestBytes: 200, Response: &response, HTTPStatus: 200, ResponseBodySHA256: strings.Repeat("b", 64), EvidenceRef: evidence,
	}, f.sealed, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCanonicalNotAppliedProofV1(proof.CanonicalJSON(), f.sealed, f.limits); err != nil {
		t.Fatal(err)
	}
	for name, forged := range map[string][]byte{
		"repository":  bytes.Replace(proof.CanonicalJSON(), []byte("octo-org"), []byte("evil-org"), 1),
		"predicate":   bytes.Replace(proof.CanonicalJSON(), []byte("ref_updates[0].before_oid_mismatch"), []byte("ref_updates[1].before_oid_mismatch"), 1),
		"disposition": bytes.Replace(proof.CanonicalJSON(), []byte(NotAppliedAllOrNothingDispositionV1), []byte("collector_says_not_applied"), 1),
		"raw digest":  bytes.Replace(proof.CanonicalJSON(), []byte(evidence.SHA256), []byte(strings.Repeat("c", 64)), 1),
	} {
		if _, err := ParseCanonicalNotAppliedProofV1(forged, f.sealed, f.limits); err == nil {
			t.Fatalf("forged NOT_APPLIED %s accepted", name)
		}
	}

	zeroEvidence := ledger.EvidenceRef{URI: "evidence/zero-bytes", Kind: NotAppliedZeroByteEvidenceKindV1, SHA256: strings.Repeat("d", 64)}
	if _, err := NewNotAppliedProofV1(NotAppliedProofV1Input{Kind: NotAppliedZeroRequestBytes, RequestID: "target-request-zero", RequestBytes: 0, Response: &response, EvidenceRef: zeroEvidence}, f.sealed, f.limits); err == nil {
		t.Fatal("zero-byte proof accepted a provider response")
	}
	if _, err := NewNotAppliedProofV1(NotAppliedProofV1Input{Kind: NotAppliedZeroRequestBytes, RequestID: "target-request-zero", RequestBytes: 0, EvidenceRef: zeroEvidence}, f.sealed, f.limits); err != nil {
		t.Fatalf("strict zero-byte proof rejected: %v", err)
	}
}

func TestGenericSquashAndRebaseRemainRepresentableWithoutProductionAuthority(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	evidence := []ledger.EvidenceRef{{URI: "evidence/generic-strategy", Kind: "generic-strategy", SHA256: strings.Repeat("e", 64)}}
	resultSHA, _ := NewGitSHA(strings.Repeat("6", 40))

	for _, method := range []MergeMethod{MergeMethodSquash, MergeMethodRebase} {
		lineage := []CommitLineage{{SourceSHA: f.headSHA, SourceTree: f.headTree, ResultSHA: resultSHA, ResultTree: f.headTree, Parents: []GitSHA{f.baseSHA}}}
		result, err := NewGenericStrategyResultV1(GenericStrategyResultV1Input{
			Snapshot: f.snapshot, Repository: f.repository, PullRequest: f.pr, BaseBranch: f.base,
			AcceptedHeadSHA: f.headSHA, AcceptedHeadTree: f.headTree, BaseBeforeSHA: f.baseSHA, Method: method,
			ResultSHA: resultSHA, ResultTree: f.headTree, Parents: []GitSHA{f.baseSHA}, Lineage: lineage, EvidenceRefs: evidence,
		}, f.limits)
		if err != nil {
			t.Fatalf("generic %s result was not representable: %v", method, err)
		}
		containment, err := NewTargetContainmentProofV1(TargetContainmentProofV1Input{Snapshot: f.snapshot, Repository: f.repository, TargetRef: "refs/heads/" + f.base.String(), ResultSHA: resultSHA, ObservedTargetTipSHA: resultSHA, Mechanism: GitHubCompareProofV1, Status: TargetContainmentIdentical, MergeBaseSHA: resultSHA, EvidenceRefs: evidence}, f.limits)
		if err != nil {
			t.Fatal(err)
		}
		post, err := NewGenericStrategyPostMergeV1(GenericStrategyPostMergeV1Input{Result: result, Snapshot: f.snapshot, ObservedTargetTipSHA: resultSHA, ContainmentProof: containment, EvidenceRefs: evidence}, f.limits)
		if err != nil || post.SHA256() == "" {
			t.Fatalf("generic %s post-merge proof was not representable: %v", method, err)
		}
	}

	unsupported := f.authority.MergePolicy().Input()
	unsupported.Method = MergeMethodSquash
	if _, err := NewMergePolicyV1(unsupported, f.limits); err == nil {
		t.Fatal("generic squash representation widened production-v1 policy")
	}
}

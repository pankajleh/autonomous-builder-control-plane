package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

func TestCheckpointChainRejectsForkAndCHeadChange(t *testing.T) {
	grantDigest := strings.Repeat("1", 64)
	design := sealCheckpoint(t, PhaseCheckpointV1{
		Kind: CheckpointDesignAccepted, Repository: "example/project", CapsuleFileSHA256: hashChar("a"), CapsuleSHA256: hashChar("b"),
		CandidateSHA: oidChar("c"), Operation: contextcapsule.OperationDesignReview, Verdict: "DESIGN_ACCEPTED",
		Evidence: []EvidenceBindingV1{{Ref: "evidence/design", SHA256: hashChar("d")}}, ControllerPolicyIdentity: "policy-v3",
		ControllerEventIdentity: "event-0", SemanticRegistrySHA256: hashChar("e"), NextStageGrantSHA256: grantDigest,
	})
	if err := ValidateCheckpointTransitionV1(nil, design); err != nil {
		t.Fatal(err)
	}
	converged := sealCheckpoint(t, PhaseCheckpointV1{
		Kind: CheckpointImplementationConverged, Repository: design.Repository, CapsuleFileSHA256: hashChar("f"), CapsuleSHA256: hashChar("1"),
		CandidateSHA: oidChar("2"), Operation: contextcapsule.OperationImplementationReview, Verdict: "IMPLEMENTATION_CONVERGED_C0_M0",
		Evidence: []EvidenceBindingV1{{Ref: "evidence/converged", SHA256: hashChar("3")}}, ControllerPolicyIdentity: "policy-v3",
		Sequence: 1, PredecessorCheckpointSHA256: design.CheckpointSHA256, ControllerEventIdentity: "event-1",
		ReviewScopeTipSHA256: hashChar("4"), NextStageGrantSHA256: hashChar("5"), ReviewedBlockingScopeIDs: []string{"rule.invariant"},
	})
	if err := ValidateCheckpointTransitionV1(&design, converged); err != nil {
		t.Fatal(err)
	}
	fork := converged
	fork.PredecessorCheckpointSHA256 = hashChar("9")
	fork = sealCheckpoint(t, fork)
	if err := ValidateCheckpointTransitionV1(&design, fork); ClassOf(err) != CheckpointChainInvalid {
		t.Fatalf("fork class = %q, err=%v", ClassOf(err), err)
	}
	acceptance := sealCheckpoint(t, PhaseCheckpointV1{
		Kind: CheckpointAcceptancePassed, Repository: design.Repository, CapsuleFileSHA256: hashChar("6"), CapsuleSHA256: hashChar("7"),
		CandidateSHA: converged.CandidateSHA, Operation: contextcapsule.OperationAcceptance, Verdict: "ACCEPTANCE_PASSED", AcceptanceResultSHA256: hashChar("0"),
		Evidence: []EvidenceBindingV1{{Ref: "evidence/acceptance", SHA256: hashChar("8")}}, ControllerPolicyIdentity: "policy-v3",
		Sequence: 2, PredecessorCheckpointSHA256: converged.CheckpointSHA256, ControllerEventIdentity: "event-2",
	})
	if err := ValidateCheckpointTransitionV1(&converged, acceptance); err != nil {
		t.Fatal(err)
	}
	final := sealCheckpoint(t, PhaseCheckpointV1{
		Kind: CheckpointFinalReviewClean, Repository: design.Repository, CapsuleFileSHA256: acceptance.CapsuleFileSHA256, CapsuleSHA256: acceptance.CapsuleSHA256,
		CandidateSHA: oidChar("9"), Operation: contextcapsule.OperationFinalReview, Verdict: "FINAL_REVIEW_CLEAN_C0_M0", ReviewScopeTipSHA256: hashChar("b"), ReviewedBlockingScopeIDs: []string{"rule.invariant"},
		Evidence: []EvidenceBindingV1{{Ref: "evidence/final", SHA256: hashChar("a")}}, ControllerPolicyIdentity: "policy-v3",
		Sequence: 3, PredecessorCheckpointSHA256: acceptance.CheckpointSHA256, ControllerEventIdentity: "event-3",
	})
	if err := ValidateCheckpointTransitionV1(&acceptance, final); ClassOf(err) != FinalReviewInvalidated {
		t.Fatalf("head-change class = %q, err=%v", ClassOf(err), err)
	}
}

func TestDerivationRejectsMandatoryFloorRemoval(t *testing.T) {
	registry := fixtureRegistry(t)
	parent := fixtureCapsule(contextcapsule.StageADesign, registry.RegistrySHA256)
	parentFile := hashChar("a")
	grant := fixtureGrant(registry.RegistrySHA256)
	checkpoint := PhaseCheckpointV1{
		Kind: CheckpointDesignAccepted, Repository: "example/project", CapsuleFileSHA256: parentFile, CapsuleSHA256: parent.CapsuleSHA256,
		CandidateSHA: oidChar("2"), Operation: contextcapsule.OperationDesignReview, Verdict: "ACCEPTED",
		Evidence: []EvidenceBindingV1{{Ref: "design", SHA256: hashChar("3")}}, ControllerPolicyIdentity: "policy-v3",
		ControllerEventIdentity: "event-design", SemanticRegistrySHA256: registry.RegistrySHA256,
	}
	checkpoint, grant, err := SealCheckpointWithNextStageGrantV1(checkpoint, grant)
	if err != nil {
		t.Fatal(err)
	}
	child := fixtureCapsule(contextcapsule.StageBImplementation, registry.RegistrySHA256)
	child.BaseSHA = checkpoint.CandidateSHA
	child.PhaseAuthority.Parent = &contextcapsule.PhaseParentV1{
		CapsuleFileSHA256: parentFile, CapsuleSHA256: parent.CapsuleSHA256, Stage: contextcapsule.StageADesign,
		CheckpointSHA256: checkpoint.CheckpointSHA256, CandidateSHA: checkpoint.CandidateSHA, GrantSHA256: grant.GrantSHA256,
	}
	input := DerivationV3{ParentCapsuleFileSHA256: parentFile, ParentCapsule: parent, Checkpoint: checkpoint, Grant: grant, ChildCapsule: child}
	if err := ValidateDerivationV3(input); err != nil {
		t.Fatal(err)
	}
	child.PhaseAuthority.AuthorizedInvariantIDs = []string{"rule.other"}
	if err := ValidateDerivationV3(DerivationV3{ParentCapsuleFileSHA256: parentFile, ParentCapsule: parent, Checkpoint: checkpoint, Grant: grant, ChildCapsule: child}); ClassOf(err) != CapsuleLineageInvalid {
		t.Fatalf("floor removal class = %q, err=%v", ClassOf(err), err)
	}
}

func TestControllerRejectsUngrantableBCapsuleDerivation(t *testing.T) {
	repository, base, identity := governanceRepository(t)
	registry := fixtureRegistry(t)
	capsule := fixtureCapsule(contextcapsule.StageBImplementation, registry.RegistrySHA256)
	capsule.BaseSHA = base
	capsule.Repository = identity
	backend := newTestWorkflowAuthorityStoreV1()
	controller, err := OpenControllerWithAuthorityBackendV1(repository, backend.client("host-a"))
	if err != nil {
		t.Fatal(err)
	}
	backend.seed(t, controller, identity)
	if err := controller.ValidateCapsuleUsageV3(capsule, contextcapsule.OperationImplementation, true, ""); ClassOf(err) != CapsuleLineageInvalid {
		t.Fatalf("ungranted B class = %q, err=%v", ClassOf(err), err)
	}
	authorizeBCapsule(t, controller, repository, &capsule)
	if err := controller.ValidateCapsuleUsageV3(capsule, contextcapsule.OperationImplementation, true, ""); err != nil {
		t.Fatal(err)
	}
	tampered := capsule
	tampered.PhaseAuthority = clonePhaseAuthority(capsule.PhaseAuthority)
	tampered.PhaseAuthority.AllowedPaths = []string{"internal/governance/**", "outside/**"}
	if err := controller.ValidateCapsuleUsageV3(tampered, contextcapsule.OperationImplementation, true, ""); ClassOf(err) != CapsuleLineageInvalid {
		t.Fatalf("grant maxima bypass class = %q, err=%v", ClassOf(err), err)
	}
}

func TestControllerRequiresLeaseAfterReviewBegins(t *testing.T) {
	repository, base, identity := governanceRepository(t)
	registry := fixtureRegistry(t)
	capsule := fixtureCapsule(contextcapsule.StageBImplementation, registry.RegistrySHA256)
	capsule.BaseSHA = base
	capsule.Repository = identity
	backend := newTestWorkflowAuthorityStoreV1()
	controller, err := OpenControllerWithAuthorityBackendV1(repository, backend.client("host-a"))
	if err != nil {
		t.Fatal(err)
	}
	backend.seed(t, controller, identity)
	authorizeBCapsule(t, controller, repository, &capsule)
	report := sealReport(t, capsule, hashChar("a"), registry, nil, ReviewScopeReportV1{
		Kind: "ReviewScopeReportV1", CapsuleFileSHA256: hashChar("a"), CapsuleSHA256: capsule.CapsuleSHA256,
		SemanticRegistrySHA256: registry.RegistrySHA256, ReviewedPreFixHEAD: base, ActiveFindings: []ActiveFindingV1{},
		RequestedMutationIDs: []string{}, DeferredObservations: []DeferredObservationV1{}, ReviewerIdentity: "reviewer", ProviderIdentity: "provider", ReviewEvidenceSHA256: hashChar("2"),
	})
	if err := controller.AdvanceReviewTipV1(repository, capsule, hashChar("a"), registry, report); err != nil {
		t.Fatal(err)
	}
	if err := controller.ValidateCapsuleUsageV3(capsule, contextcapsule.OperationImplementation, true, ""); ClassOf(err) != MutationScopeViolation {
		t.Fatalf("post-review implementation class = %q, err=%v", ClassOf(err), err)
	}
	if _, err := controller.ReserveRalphexInvocationV1(capsule, contextcapsule.OperationImplementation, true, ""); ClassOf(err) != MutationScopeViolation {
		t.Fatalf("post-review reservation class = %q, err=%v", ClassOf(err), err)
	}
}

func TestControllerIssuesLeaseForLaterDurableReport(t *testing.T) {
	repository, base, identity := governanceRepository(t)
	registry := fixtureRegistry(t)
	capsule := fixtureCapsule(contextcapsule.StageBImplementation, registry.RegistrySHA256)
	capsule.BaseSHA = base
	capsule.Repository = identity
	backend := newTestWorkflowAuthorityStoreV1()
	controller, err := OpenControllerWithAuthorityBackendV1(repository, backend.client("host-a"))
	if err != nil {
		t.Fatal(err)
	}
	backend.seed(t, controller, identity)
	authorizeBCapsule(t, controller, repository, &capsule)
	report0 := sealReport(t, capsule, hashChar("a"), registry, nil, ReviewScopeReportV1{
		Kind: "ReviewScopeReportV1", CapsuleFileSHA256: hashChar("a"), CapsuleSHA256: capsule.CapsuleSHA256,
		SemanticRegistrySHA256: registry.RegistrySHA256, ReviewedPreFixHEAD: base,
		ActiveFindings:       []ActiveFindingV1{{FindingID: "finding.one", RuleID: "rule.invariant", Severity: SeverityMajor, Evidence: []EvidenceBindingV1{{Ref: "review/zero"}}}},
		RequestedMutationIDs: []string{}, DeferredObservations: []DeferredObservationV1{}, ReviewerIdentity: "reviewer", ProviderIdentity: "provider", ReviewEvidenceSHA256: hashChar("2"),
	})
	recordReportEvidence(t, controller, repository, capsule, registry, report0)
	if err := controller.AdvanceReviewTipV1(repository, capsule, hashChar("a"), registry, report0); err != nil {
		t.Fatal(err)
	}
	report1 := report0
	report1.Sequence = 1
	report1.PredecessorReportSHA256 = report0.ReportSHA256
	report1.RequestedMutationIDs = []string{"finding.one"}
	report1.ActiveFindings = []ActiveFindingV1{{FindingID: "finding.one", RuleID: "rule.invariant", Severity: SeverityMajor, Evidence: []EvidenceBindingV1{{Ref: "review/one"}}}}
	report1.ReviewEvidenceSHA256 = hashChar("3")
	report1.ReportSHA256 = ""
	report1 = sealReport(t, capsule, hashChar("a"), registry, &report0, report1)
	recordReportEvidence(t, controller, repository, capsule, registry, report1)
	if err := controller.AdvanceReviewTipV1(repository, capsule, hashChar("a"), registry, report1); err != nil {
		t.Fatal(err)
	}
	lease, err := controller.IssueMutationLeaseV1(capsule, registry, report1, MutationLimitsV1{MaxChangedFiles: 2, MaxChangedBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if lease.ReportSHA256 != report1.ReportSHA256 {
		t.Fatalf("lease report = %s, want later tip %s", lease.ReportSHA256, report1.ReportSHA256)
	}
}

func TestReviewSetCannotGrowAndLeaseIsOneUse(t *testing.T) {
	registry := fixtureRegistry(t)
	capsule := fixtureCapsule(contextcapsule.StageBImplementation, registry.RegistrySHA256)
	capsuleFile := hashChar("a")
	report0 := sealReport(t, capsule, capsuleFile, registry, nil, ReviewScopeReportV1{
		Kind: "ReviewScopeReportV1", CapsuleFileSHA256: capsuleFile, CapsuleSHA256: capsule.CapsuleSHA256,
		SemanticRegistrySHA256: registry.RegistrySHA256, ReviewedPreFixHEAD: oidChar("1"),
		ActiveFindings:       []ActiveFindingV1{{FindingID: "finding.one", RuleID: "rule.invariant", Severity: SeverityMajor, Evidence: []EvidenceBindingV1{{Ref: "review/one"}}}},
		RequestedMutationIDs: []string{"finding.one"}, DeferredObservations: []DeferredObservationV1{},
		ReviewerIdentity: "reviewer", ProviderIdentity: "provider", ReviewEvidenceSHA256: hashChar("2"),
	})
	report1 := report0
	report1.Sequence = 1
	report1.PredecessorReportSHA256 = report0.ReportSHA256
	report1.ReportSHA256 = ""
	report1.ActiveFindings = append(report1.ActiveFindings, ActiveFindingV1{FindingID: "finding.two", RuleID: "rule.invariant", Severity: SeverityMajor, Evidence: []EvidenceBindingV1{{Ref: "review/two"}}})
	evidence1 := bindReportEvidence(t, capsule, registry, &report1)
	if _, err := SealReviewScopeReportV1(capsule, capsuleFile, registry, evidence1, &report0, report1); ClassOf(err) != ScopeExpansionRequired {
		t.Fatalf("growth class = %q, err=%v", ClassOf(err), err)
	}
	lease, err := IssueMutationLeaseV1(capsule, registry, findingEvidenceForReport(t, capsule, registry, report0), report0, MutationLimitsV1{MaxChangedFiles: 2, MaxChangedBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(lease.AllowedPaths) != 1 || lease.AllowedPaths[0] != "internal/governance/**" {
		t.Fatalf("lease path intersection = %#v", lease.AllowedPaths)
	}
	state, err := NewMutationStateV1(lease, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := BeginMutationLeaseV1(&state, lease); err != nil {
		t.Fatal(err)
	}
	if err := BeginMutationLeaseV1(&state, lease); ClassOf(err) != MutationScopeViolation {
		t.Fatalf("replay class = %q, err=%v", ClassOf(err), err)
	}
	paths := []string{"internal/governance/governance.go"}
	pathDigest := sha256.Sum256(pathsRawForDigest(paths))
	proof := CandidateProofV1{BaseSHA: lease.PreFixHEAD, CandidateSHA: oidChar("3"), ChangedPaths: paths, ChangedPathSHA256: hex.EncodeToString(pathDigest[:]), ChangedFiles: 1, ChangedBytes: 50, WorkspaceClean: true}
	receipt := MutationReceiptV1{
		Kind: "MutationReceiptV1", LeaseSHA256: lease.LeaseSHA256, ReportSHA256: report0.ReportSHA256,
		PreFixHEAD: lease.PreFixHEAD, ResultHEAD: proof.CandidateSHA, ChangedPaths: proof.ChangedPaths,
		ChangedPathSHA256: proof.ChangedPathSHA256, ChangedFiles: proof.ChangedFiles, ChangedBytes: proof.ChangedBytes,
	}
	receipt, err = SealMutationReceiptV1(lease, state, proof, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMutationReceiptV1(lease, &state, proof, receipt); err != nil {
		t.Fatal(err)
	}
	if state.LeaseStatus != LeaseConsumed {
		t.Fatalf("lease state = %s", state.LeaseStatus)
	}
	if err := ValidateMutationReceiptV1(lease, &state, proof, receipt); ClassOf(err) != MutationScopeViolation {
		t.Fatalf("receipt replay class = %q, err=%v", ClassOf(err), err)
	}
}

func TestCandidateProofRejectsPathEscapeAndCMutation(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repository")
	gitTest(t, "", "init", "-b", "main", repository)
	gitTest(t, repository, "config", "user.email", "governance@example.test")
	gitTest(t, repository, "config", "user.name", "Governance Test")
	remoteDigest := sha256.Sum256([]byte(repository))
	remoteURL := "https://" + hex.EncodeToString(remoteDigest[:8]) + ".example.test/example/project.git"
	gitTest(t, repository, "remote", "add", "origin", remoteURL)
	if err := os.MkdirAll(filepath.Join(repository, "internal", "governance"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "internal", "governance", "base.go"), []byte("package governance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", ".")
	gitTest(t, repository, "commit", "-m", "base")
	base := gitTest(t, repository, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repository, "escape.txt"), []byte("escape\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", ".")
	gitTest(t, repository, "commit", "-m", "escape")
	candidate := gitTest(t, repository, "rev-parse", "HEAD")
	capsule := fixtureCapsule(contextcapsule.StageBImplementation, hashChar("a"))
	capsule.BaseSHA = base
	if _, err := ValidateCandidateV1(repository, capsule, candidate); ClassOf(err) != MutationScopeViolation {
		t.Fatalf("path escape class = %q, err=%v", ClassOf(err), err)
	}
	capsule = fixtureCapsule(contextcapsule.StageCAcceptanceMerge, hashChar("a"))
	capsule.BaseSHA = base
	if _, err := ValidateCandidateV1(repository, capsule, candidate); ClassOf(err) != MutationScopeViolation && ClassOf(err) != FinalReviewInvalidated {
		t.Fatalf("C mutation class = %q, err=%v", ClassOf(err), err)
	}
}

func TestActivationGrandfathersOnlyExactPreActivationDigest(t *testing.T) {
	activation, err := SealGovernanceActivationV1(GovernanceActivationV1{
		Kind: "GovernanceActivationV1", PolicyVersion: contextcapsule.PolicyVersionV3, PolicySHA256: hashChar("1"),
		ActivationRepositoryCommit: oidChar("2"), ActivationSequence: 9, ActivationTime: time.Unix(0, 0).UTC().Format(time.RFC3339),
		GrandfatheredV2Digests: []string{hashChar("3")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateActivatedWorkflowPolicyV1(contextcapsule.PolicyVersionV2, hashChar("3"), 8, activation); err != nil {
		t.Fatal(err)
	}
	if err := ValidateActivatedWorkflowPolicyV1(contextcapsule.PolicyVersionV2, hashChar("3"), 9, activation); ClassOf(err) != CapsuleLineageInvalid {
		t.Fatalf("post-activation V2 class = %q, err=%v", ClassOf(err), err)
	}
	if err := ValidateActivatedWorkflowPolicyV1(contextcapsule.PolicyVersionV2, hashChar("4"), 8, activation); ClassOf(err) != CapsuleLineageInvalid {
		t.Fatalf("unlisted V2 class = %q, err=%v", ClassOf(err), err)
	}
}

func TestActivationInstallVerifiesCommitAndCommittedPolicy(t *testing.T) {
	repository, base, identity := governanceRepository(t)
	backend := newTestWorkflowAuthorityStoreV1()
	controller, err := OpenControllerWithAuthorityBackendV1(repository, backend.client("host-a"))
	if err != nil {
		t.Fatal(err)
	}
	backend.seed(t, controller, identity)
	policySHA := committedPolicySHA256(t, repository, base)
	blob := gitTest(t, repository, "hash-object", "-w", GovernancePolicyPathV1)
	activation := func(commit, digest string) GovernanceActivationV1 {
		value, sealErr := SealGovernanceActivationV1(GovernanceActivationV1{
			Kind: "GovernanceActivationV1", PolicyVersion: contextcapsule.PolicyVersionV3, PolicySHA256: digest,
			ActivationRepositoryCommit: commit, ActivationSequence: 1, ActivationTime: time.Unix(0, 0).UTC().Format(time.RFC3339), GrandfatheredV2Digests: []string{},
		})
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		return value
	}
	if err := controller.InstallActivationV1(repository, identity, activation(blob, policySHA)); ClassOf(err) != CapsuleLineageInvalid {
		t.Fatalf("blob activation class = %q, err=%v", ClassOf(err), err)
	}
	if err := controller.InstallActivationV1(repository, identity, activation(base, hashChar("1"))); ClassOf(err) != CapsuleLineageInvalid {
		t.Fatalf("policy mismatch class = %q, err=%v", ClassOf(err), err)
	}
	if err := controller.InstallActivationV1(repository, identity, activation(base, policySHA)); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptancePassedRequiresDerivedDeterministicResult(t *testing.T) {
	capsule := fixtureCapsule(contextcapsule.StageCAcceptanceMerge, hashChar("a"))
	result := fixtureAcceptanceResult(t, capsule)
	checkpoint := PhaseCheckpointV1{
		Kind: CheckpointAcceptancePassed, Repository: capsule.Repository, CapsuleFileSHA256: hashChar("b"), CapsuleSHA256: capsule.CapsuleSHA256,
		CandidateSHA: capsule.BaseSHA, Operation: contextcapsule.OperationAcceptance, Verdict: "caller says pass",
		Evidence: append([]EvidenceBindingV1(nil), result.Evidence...), ControllerPolicyIdentity: "policy-v3", ControllerEventIdentity: "event-acceptance",
		AcceptanceResultSHA256: result.AcceptanceSHA256,
	}
	if _, err := SealPhaseCheckpointV1(checkpoint); ClassOf(err) != CheckpointChainInvalid {
		t.Fatalf("arbitrary verdict class = %q, err=%v", ClassOf(err), err)
	}
	opaque := result
	opaque.AcceptanceSHA256 = ""
	opaque.Evidence = append(opaque.Evidence, EvidenceBindingV1{Ref: "acceptance/unused", SHA256: hashChar("9")})
	if _, err := SealDeterministicAcceptanceResultV1(opaque); ClassOf(err) != CheckpointChainInvalid {
		t.Fatalf("opaque acceptance evidence class = %q, err=%v", ClassOf(err), err)
	}
	checkpoint.Verdict = "ACCEPTANCE_PASSED"
	checkpoint = sealCheckpoint(t, checkpoint)
	if err := validateAcceptancePassedCheckpointV1(capsule, checkpoint, result); err != nil {
		t.Fatal(err)
	}
	tampered := result
	tampered.Checks[0].Outcome = "FAIL"
	if err := validateAcceptancePassedCheckpointV1(capsule, checkpoint, tampered); ClassOf(err) != CheckpointChainInvalid {
		t.Fatalf("failed deterministic check class = %q, err=%v", ClassOf(err), err)
	}
}

func TestAggregateElapsedIncludesHandoffsAndFinishOverrun(t *testing.T) {
	t.Run("handoff", func(t *testing.T) {
		repository, base, identity := governanceRepository(t)
		registry := fixtureRegistry(t)
		capsule := fixtureCapsule(contextcapsule.StageBImplementation, registry.RegistrySHA256)
		capsule.BaseSHA = base
		capsule.Repository = identity
		backend := newTestWorkflowAuthorityStoreV1()
		controller, err := OpenControllerWithAuthorityBackendV1(repository, backend.client("host-a"))
		if err != nil {
			t.Fatal(err)
		}
		backend.seed(t, controller, identity)
		authorizeBCapsule(t, controller, repository, &capsule)
		reservation, err := controller.ReserveRalphexInvocationV1(capsule, contextcapsule.OperationImplementation, true, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := controller.FinishRalphexInvocationV1(reservation); err != nil {
			t.Fatal(err)
		}
		backend.rewriteState(t, controller, func(state *ControllerStateV1) {
			then := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano)
			state.BWorkflowStartedAt = then
			state.AggregateUpdatedAt = then
		})
		report := sealReport(t, capsule, hashChar("a"), registry, nil, ReviewScopeReportV1{
			Kind: "ReviewScopeReportV1", CapsuleFileSHA256: hashChar("a"), CapsuleSHA256: capsule.CapsuleSHA256,
			SemanticRegistrySHA256: registry.RegistrySHA256, ReviewedPreFixHEAD: base, ActiveFindings: []ActiveFindingV1{}, RequestedMutationIDs: []string{},
			DeferredObservations: []DeferredObservationV1{}, ReviewerIdentity: "reviewer", ProviderIdentity: "provider", ReviewEvidenceSHA256: hashChar("2"),
		})
		if err := controller.AdvanceReviewTipV1(repository, capsule, hashChar("a"), registry, report); err != nil {
			t.Fatal(err)
		}
		snapshot, err := controller.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		elapsed, err := time.ParseDuration(snapshot.ExecutionState.AggregateElapsed)
		if err != nil || elapsed < 10*time.Minute {
			t.Fatalf("handoff elapsed = %s, err=%v", snapshot.ExecutionState.AggregateElapsed, err)
		}
	})

	t.Run("finish overrun", func(t *testing.T) {
		repository, base, identity := governanceRepository(t)
		capsule := fixtureCapsule(contextcapsule.StageBImplementation, hashChar("a"))
		capsule.BaseSHA = base
		capsule.Repository = identity
		backend := newTestWorkflowAuthorityStoreV1()
		controller, err := OpenControllerWithAuthorityBackendV1(repository, backend.client("host-a"))
		if err != nil {
			t.Fatal(err)
		}
		backend.seed(t, controller, identity)
		authorizeBCapsule(t, controller, repository, &capsule)
		reservation, err := controller.ReserveRalphexInvocationV1(capsule, contextcapsule.OperationImplementation, true, "")
		if err != nil {
			t.Fatal(err)
		}
		backend.rewriteState(t, controller, func(state *ControllerStateV1) {
			then := time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339Nano)
			state.BWorkflowStartedAt = then
			state.AggregateUpdatedAt = then
			state.ExecutionState.AggregateElapsed = "0s"
		})
		if err := controller.FinishRalphexInvocationV1(reservation); ClassOf(err) != ExecutionBoundsInvalid {
			t.Fatalf("finish overrun class = %q, err=%v", ClassOf(err), err)
		}
		snapshot, err := controller.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		elapsed, _ := time.ParseDuration(snapshot.ExecutionState.AggregateElapsed)
		if snapshot.ActiveInvocation != nil || elapsed < 2*time.Hour {
			t.Fatalf("overrun was not durably closed: active=%#v elapsed=%s", snapshot.ActiveInvocation, snapshot.ExecutionState.AggregateElapsed)
		}
	})
}

func TestControllerActivationCannotBeBypassedByOmittedCallerState(t *testing.T) {
	repository, base, identity := governanceRepository(t)
	localOnly, err := OpenControllerV1(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := localOnly.AdmitWorkflowAuthority(repository, identity, contextcapsule.PolicyVersionV2, hashChar("0")); ClassOf(err) != ExecutionBoundsInvalid {
		t.Fatalf("implicit host-local authority class = %q, err=%v", ClassOf(err), err)
	}
	backend := newTestWorkflowAuthorityStoreV1()
	controller, err := OpenControllerWithAuthorityBackendV1(repository, backend.client("host-a/user-a/home-a"))
	if err != nil {
		t.Fatal(err)
	}
	backend.seed(t, controller, identity)
	grandfathered := hashChar("3")
	if err := controller.AdmitWorkflowAuthority(repository, identity, contextcapsule.PolicyVersionV2, grandfathered); err != nil {
		t.Fatal(err)
	}
	activation, err := SealGovernanceActivationV1(GovernanceActivationV1{
		Kind: "GovernanceActivationV1", PolicyVersion: contextcapsule.PolicyVersionV3, PolicySHA256: committedPolicySHA256(t, repository, base),
		ActivationRepositoryCommit: base, ActivationSequence: 2, ActivationTime: time.Unix(0, 0).UTC().Format(time.RFC3339),
		GrandfatheredV2Digests: []string{grandfathered},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.InstallActivationV1(repository, identity, activation); err != nil {
		t.Fatal(err)
	}
	freshClone := filepath.Join(t.TempDir(), "fresh-clone")
	gitTest(t, "", "clone", "--quiet", repository, freshClone)
	gitTest(t, freshClone, "remote", "set-url", "origin", "https://mirror.example.test/"+identity+".git")
	reopened, err := OpenControllerWithAuthorityBackendV1(freshClone, backend.client("host-b/user-b/home-b"))
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ControllerIdentity() != controller.ControllerIdentity() {
		t.Fatal("fresh clone did not resolve the durable repository controller identity")
	}
	if err := reopened.AdmitWorkflowAuthority(freshClone, identity, contextcapsule.PolicyVersionV2, grandfathered); err != nil {
		t.Fatalf("durably grandfathered authority was rejected: %v", err)
	}
	if err := reopened.AdmitWorkflowAuthority(freshClone, identity, contextcapsule.PolicyVersionV2, hashChar("4")); ClassOf(err) != CapsuleLineageInvalid {
		t.Fatalf("post-activation omitted-state bypass class = %q, err=%v", ClassOf(err), err)
	}
	if err := reopened.AdmitWorkflowAuthority(freshClone, identity, contextcapsule.PolicyVersionV3, hashChar("5")); err != nil {
		t.Fatal(err)
	}

	missingUniverse := newTestWorkflowAuthorityStoreV1WithDomain(backend.domain)
	isolated, err := OpenControllerWithAuthorityBackendV1(freshClone, missingUniverse.client("host-c/user-c/home-c"))
	if err != nil {
		t.Fatal(err)
	}
	if err := isolated.AdmitWorkflowAuthority(freshClone, identity, contextcapsule.PolicyVersionV2, hashChar("4")); ClassOf(err) != ExecutionBoundsInvalid {
		t.Fatalf("missing authority universe class = %q, err=%v", ClassOf(err), err)
	}
	if _, err := isolated.Snapshot(); ClassOf(err) != ExecutionBoundsInvalid {
		t.Fatalf("missing state initialized a zero universe: class=%q err=%v", ClassOf(err), err)
	}
}

func TestControllerCountersAndInFlightReservationAreDurable(t *testing.T) {
	repository, base, identity := governanceRepository(t)
	capsule := fixtureCapsule(contextcapsule.StageBImplementation, hashChar("a"))
	capsule.BaseSHA = base
	capsule.Repository = identity
	capsule.PhaseAuthority.ExecutionBounds.MaxRalphexInvocations = 1
	backend := newTestWorkflowAuthorityStoreV1()
	first, err := OpenControllerWithAuthorityBackendV1(repository, backend.client("host-a/user-a/home-a"))
	if err != nil {
		t.Fatal(err)
	}
	backend.seed(t, first, identity)
	authorizeBCapsule(t, first, repository, &capsule)
	remote := gitTest(t, repository, "remote", "get-url", "origin")
	freshClone := filepath.Join(t.TempDir(), "fresh-clone")
	gitTest(t, "", "clone", "--quiet", repository, freshClone)
	gitTest(t, freshClone, "remote", "set-url", "origin", remote)
	second, err := OpenControllerWithAuthorityBackendV1(freshClone, backend.client("host-b/user-b/home-b"))
	if err != nil {
		t.Fatal(err)
	}
	if first.ControllerIdentity() != second.ControllerIdentity() {
		t.Fatal("fresh clone did not share the repository controller identity")
	}
	reservation, err := first.ReserveRalphexInvocationV1(capsule, contextcapsule.OperationImplementation, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.ReserveRalphexInvocationV1(capsule, contextcapsule.OperationImplementation, true, ""); ClassOf(err) != ExecutionBoundsInvalid {
		t.Fatalf("parallel reservation class = %q, err=%v", ClassOf(err), err)
	}
	if err := second.FinishRalphexInvocationV1(reservation); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ReserveRalphexInvocationV1(capsule, contextcapsule.OperationImplementation, true, ""); ClassOf(err) != ExecutionBoundsInvalid {
		t.Fatalf("counter reset class = %q, err=%v", ClassOf(err), err)
	}
	missingUniverse := newTestWorkflowAuthorityStoreV1WithDomain(backend.domain)
	isolated, err := OpenControllerWithAuthorityBackendV1(freshClone, missingUniverse.client("host-c/user-c/home-c"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := isolated.ReserveRalphexInvocationV1(capsule, contextcapsule.OperationImplementation, true, ""); ClassOf(err) != ExecutionBoundsInvalid {
		t.Fatalf("missing authority backend reset counters: class=%q err=%v", ClassOf(err), err)
	}
	snapshot, err := second.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ExecutionState.RalphexInvocations != 1 || snapshot.ActiveInvocation != nil {
		t.Fatalf("durable invocation state = %#v", snapshot.ExecutionState)
	}
	if _, err := time.ParseDuration(snapshot.ExecutionState.AggregateElapsed); err != nil {
		t.Fatalf("aggregate elapsed was not durably advanced: %v", err)
	}
}

func TestRepositoryControllerRejectsAlternatePathsAndCopiedState(t *testing.T) {
	repository, _, identity := governanceRepository(t)
	backend := newTestWorkflowAuthorityStoreV1()
	controller, err := OpenControllerWithAuthorityBackendV1(repository, backend.client("host-a"))
	if err != nil {
		t.Fatal(err)
	}
	backend.seed(t, controller, identity)
	if err := controller.AdmitWorkflowAuthority(repository, identity, contextcapsule.PolicyVersionV2, hashChar("1")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenControllerWithAuthorityBackendV1(filepath.Join(repository, "caller-selected-state.json"), backend.client("host-a")); ClassOf(err) != ExecutionBoundsInvalid {
		t.Fatalf("caller-selected state path class = %q, err=%v", ClassOf(err), err)
	}

	otherRepository, _, _ := governanceRepository(t)
	other, err := OpenControllerWithAuthorityBackendV1(otherRepository, backend.client("host-b"))
	if err != nil {
		t.Fatal(err)
	}
	backend.copyRecord(t, controller.identity, other.identity)
	if _, err := other.Snapshot(); ClassOf(err) != ExecutionBoundsInvalid {
		t.Fatalf("copied controller state class = %q, err=%v", ClassOf(err), err)
	}
}

func TestControllerLeaseCASAndReceiptUseIndependentRepositoryProof(t *testing.T) {
	repository, base, identity := governanceRepository(t)
	registry := fixtureRegistry(t)
	capsule := fixtureCapsule(contextcapsule.StageBImplementation, registry.RegistrySHA256)
	capsule.BaseSHA = base
	capsule.Repository = identity
	capsuleFile := hashChar("a")
	report := sealReport(t, capsule, capsuleFile, registry, nil, ReviewScopeReportV1{
		Kind: "ReviewScopeReportV1", CapsuleFileSHA256: capsuleFile, CapsuleSHA256: capsule.CapsuleSHA256,
		SemanticRegistrySHA256: registry.RegistrySHA256, ReviewedPreFixHEAD: base,
		ActiveFindings:       []ActiveFindingV1{{FindingID: "finding.one", RuleID: "rule.invariant", Severity: SeverityMajor, Evidence: []EvidenceBindingV1{{Ref: "review/one"}}}},
		RequestedMutationIDs: []string{"finding.one"}, DeferredObservations: []DeferredObservationV1{},
		ReviewerIdentity: "reviewer", ProviderIdentity: "provider", ReviewEvidenceSHA256: hashChar("2"),
	})
	backend := newTestWorkflowAuthorityStoreV1()
	controller, err := OpenControllerWithAuthorityBackendV1(repository, backend.client("host-a/user-a/home-a"))
	if err != nil {
		t.Fatal(err)
	}
	backend.seed(t, controller, identity)
	authorizeBCapsule(t, controller, repository, &capsule)
	if err := controller.AdvanceReviewTipV1(repository, capsule, capsuleFile, registry, report); ClassOf(err) != ReviewChainInvalid {
		t.Fatalf("opaque report evidence class = %q, err=%v", ClassOf(err), err)
	}
	recordReportEvidence(t, controller, repository, capsule, registry, report)
	if err := controller.AdvanceReviewTipV1(repository, capsule, capsuleFile, registry, report); err != nil {
		t.Fatal(err)
	}
	if err := controller.AdvanceReviewTipV1(repository, capsule, capsuleFile, registry, report); err != nil {
		t.Fatalf("create-or-verify report replay failed: %v", err)
	}
	lease, err := controller.IssueMutationLeaseV1(capsule, registry, report, MutationLimitsV1{MaxChangedFiles: 2, MaxChangedBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	remote := gitTest(t, repository, "remote", "get-url", "origin")
	freshClone := filepath.Join(t.TempDir(), "fresh-clone")
	gitTest(t, "", "clone", "--quiet", repository, freshClone)
	gitTest(t, freshClone, "remote", "set-url", "origin", remote)
	reopened, err := OpenControllerWithAuthorityBackendV1(freshClone, backend.client("host-b/user-b/home-b"))
	if err != nil {
		t.Fatal(err)
	}
	begin := make(chan struct{})
	results := make(chan error, 2)
	for _, contender := range []*ControllerV1{controller, reopened} {
		go func(contender *ControllerV1) {
			<-begin
			results <- contender.BeginMutationLeaseV1(lease.LeaseSHA256)
		}(contender)
	}
	close(begin)
	successes := 0
	rejections := 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if ClassOf(err) == MutationScopeViolation {
			rejections++
		} else {
			t.Fatalf("parallel lease CAS error = %v", err)
		}
	}
	if successes != 1 || rejections != 1 {
		t.Fatalf("parallel lease CAS successes=%d rejections=%d", successes, rejections)
	}
	missingUniverse := newTestWorkflowAuthorityStoreV1WithDomain(backend.domain)
	isolated, err := OpenControllerWithAuthorityBackendV1(freshClone, missingUniverse.client("host-c/user-c/home-c"))
	if err != nil {
		t.Fatal(err)
	}
	if err := isolated.BeginMutationLeaseV1(lease.LeaseSHA256); ClassOf(err) != ExecutionBoundsInvalid {
		t.Fatalf("missing authority backend independently consumed lease: class=%q err=%v", ClassOf(err), err)
	}
	reservation, err := reopened.ReserveRalphexInvocationV1(capsule, contextcapsule.OperationImplementationReview, true, lease.LeaseSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.FinishRalphexInvocationV1(reservation); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "internal", "governance", "base.go"), []byte("package governance\n\nconst fixed = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", "internal/governance/base.go")
	gitTest(t, repository, "commit", "-m", "fix")
	result := gitTest(t, repository, "rev-parse", "HEAD")
	receipt, err := reopened.CompleteMutationReceiptV1(repository, capsule, lease.LeaseSHA256, result)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ResultHEAD != result || receipt.ChangedFiles != 1 || receipt.ChangedPaths[0] != "internal/governance/base.go" {
		t.Fatalf("independent receipt = %#v", receipt)
	}
	if _, err := controller.CompleteMutationReceiptV1(repository, capsule, lease.LeaseSHA256, result); ClassOf(err) != MutationScopeViolation {
		t.Fatalf("receipt replay class = %q, err=%v", ClassOf(err), err)
	}
	if _, err := controller.IssueMutationLeaseV1(capsule, registry, report, MutationLimitsV1{MaxChangedFiles: 2, MaxChangedBytes: 100}); ClassOf(err) != MutationScopeViolation {
		t.Fatalf("consumed report reissued a lease: class=%q err=%v", ClassOf(err), err)
	}
	snapshot, err := controller.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.MutationState == nil || snapshot.MutationState.LeaseStatus != LeaseConsumed || snapshot.ExecutionState.ReviewReports != 1 || snapshot.ExecutionState.MutationLeases != 1 || snapshot.ExecutionState.TotalFixBatches != 1 {
		t.Fatalf("durable mutation state = %#v", snapshot)
	}
}

func TestMutationUsageRejectsCallerAssertedLeaseDigest(t *testing.T) {
	capsule := fixtureCapsule(contextcapsule.StageBImplementation, hashChar("a"))
	if err := ValidateCapsuleUsageV3(capsule, contextcapsule.OperationImplementationReview, true, hashChar("b")); ClassOf(err) != MutationScopeViolation {
		t.Fatalf("caller lease assertion class = %q, err=%v", ClassOf(err), err)
	}
}

func TestActiveFindingMustMatchRegistryEvidenceContract(t *testing.T) {
	registry := fixtureRegistry(t)
	capsule := fixtureCapsule(contextcapsule.StageBImplementation, registry.RegistrySHA256)
	base := ReviewScopeReportV1{
		Kind: "ReviewScopeReportV1", CapsuleFileSHA256: hashChar("a"), CapsuleSHA256: capsule.CapsuleSHA256,
		SemanticRegistrySHA256: registry.RegistrySHA256, ReviewedPreFixHEAD: oidChar("1"), ReviewedBlockingScopeIDs: append([]string(nil), capsule.PhaseAuthority.BlockingScopeIDs...),
		ActiveFindings:       []ActiveFindingV1{{FindingID: "finding.one", RuleID: "rule.invariant", Severity: SeverityMajor, Evidence: []EvidenceBindingV1{{Ref: "review/one", SHA256: hashChar("8")}}}},
		RequestedMutationIDs: []string{"finding.one"}, DeferredObservations: []DeferredObservationV1{}, ReviewerIdentity: "reviewer", ProviderIdentity: "provider", ReviewEvidenceSHA256: hashChar("2"),
	}
	if _, err := SealReviewScopeReportV1(capsule, base.CapsuleFileSHA256, registry, nil, nil, base); ClassOf(err) != ReviewChainInvalid {
		t.Fatalf("opaque evidence ref class = %q, err=%v", ClassOf(err), err)
	}
	wrongClass := fixtureFindingEvidence(capsule, registry, base.ActiveFindings[0], "review/one")
	wrongClass.EvidenceClass = "WRONG"
	if _, err := SealFindingEvidenceV1(capsule, registry, wrongClass); ClassOf(err) != ReviewChainInvalid {
		t.Fatalf("evidence-class mismatch = %q, err=%v", ClassOf(err), err)
	}
	modelOverride := fixtureFindingEvidence(capsule, registry, base.ActiveFindings[0], "review/one")
	modelOverride.ModelJudgmentUsed = true
	if _, err := SealFindingEvidenceV1(capsule, registry, modelOverride); ClassOf(err) != ReviewChainInvalid {
		t.Fatalf("model override of deterministic validator = %q, err=%v", ClassOf(err), err)
	}
	badRegistry, err := SealSemanticAuthorityRegistryV1(SemanticAuthorityRegistryV1{Kind: "SemanticAuthorityRegistryV1", Entries: []SemanticRuleV1{
		{RuleID: "rule.invariant", Kind: RegistryInvariant, Obligation: "Invariant.", EvidenceClass: "TEST", CorrectionRelation: "SAME_ROOT_CAUSE", OwningComponent: "governance", AllowedCorrectionPaths: []string{"internal/governance/**"}, ValidatorIdentity: "governance-test"},
		{RuleID: "rule.non-goal", Kind: RegistryNonGoal, Obligation: "Non-goal.", EvidenceClass: "REVIEW", CorrectionRelation: "NONE", OwningComponent: "governance", AllowedCorrectionPaths: []string{}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	badCapsule := fixtureCapsule(contextcapsule.StageBImplementation, badRegistry.RegistrySHA256)
	badCapsule.PhaseAuthority.ObservationScopeIDs = []string{"rule.invariant", "rule.non-goal"}
	badCapsule.PhaseAuthority.BlockingScopeIDs = []string{"rule.invariant", "rule.non-goal"}
	badCapsule.PhaseAuthority.AuthorizedInvariantIDs = []string{"rule.invariant", "rule.non-goal"}
	badFinding := ActiveFindingV1{FindingID: "finding.one", RuleID: "rule.non-goal", Severity: SeverityMajor, Evidence: []EvidenceBindingV1{{Ref: "review/one"}}}
	badEvidence := fixtureFindingEvidence(badCapsule, badRegistry, badFinding, "review/one")
	if _, err := SealFindingEvidenceV1(badCapsule, badRegistry, badEvidence); ClassOf(err) != ScopeExpansionRequired {
		t.Fatalf("non-blocking registry kind class = %q, err=%v", ClassOf(err), err)
	}
}

func TestControllerResolvesFindingEvidenceAndRunsRegisteredValidator(t *testing.T) {
	repository, base, identity := governanceRepository(t)
	registry := fixtureRegistry(t)
	capsule := fixtureCapsule(contextcapsule.StageBImplementation, registry.RegistrySHA256)
	capsule.BaseSHA = base
	capsule.Repository = identity
	artifact := []byte("deterministic validator output")
	resolver := &testFindingEvidenceResolver{artifacts: map[string][]byte{"review/resolved": artifact}}
	backend := newTestWorkflowAuthorityStoreV1()
	controller, err := OpenControllerWithAuthorityBackendAndFindingEvidenceV1(repository, backend.client("host-a"), resolver)
	if err != nil {
		t.Fatal(err)
	}
	backend.seed(t, controller, identity)
	authorizeBCapsule(t, controller, repository, &capsule)
	evidence, err := controller.RecordFindingEvidenceV1(repository, capsule, registry, FindingEvidenceRequestV1{
		Ref: "review/resolved", CandidateSHA: base, FindingID: "finding.one", RuleID: "rule.invariant",
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(artifact)
	if evidence.ArtifactSHA256 != hex.EncodeToString(digest[:]) || evidence.RegisteredKind != RegistryInvariant || evidence.EvidenceClass != "TEST" || evidence.ValidatorIdentity != "governance-test" || evidence.ModelJudgmentUsed || evidence.Outcome != "VIOLATION_CONFIRMED" {
		t.Fatalf("controller-derived evidence metadata = %#v", evidence)
	}
	if len(resolver.validated) != 1 || resolver.validated[0] != "governance-test" {
		t.Fatalf("registered validator calls = %v", resolver.validated)
	}

	resolver.artifacts["review/rejected"] = []byte("caller-asserted passing output")
	resolver.err = errors.New("violation not confirmed")
	if _, err := controller.RecordFindingEvidenceV1(repository, capsule, registry, FindingEvidenceRequestV1{
		Ref: "review/rejected", CandidateSHA: base, FindingID: "finding.two", RuleID: "rule.invariant",
	}); ClassOf(err) != ReviewChainInvalid {
		t.Fatalf("unconfirmed deterministic evidence class = %q, err=%v", ClassOf(err), err)
	}

	withoutResolver, err := OpenControllerWithAuthorityBackendV1(repository, backend.client("host-b"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withoutResolver.RecordFindingEvidenceV1(repository, capsule, registry, FindingEvidenceRequestV1{
		Ref: "review/unresolved", CandidateSHA: base, FindingID: "finding.three", RuleID: "rule.invariant",
	}); ClassOf(err) != ReviewChainInvalid {
		t.Fatalf("caller-only evidence class = %q, err=%v", ClassOf(err), err)
	}
}

func TestGrantDigestBindsParentCheckpoint(t *testing.T) {
	grant := fixtureGrant(hashChar("a"))
	grant.ParentCheckpointSHA256 = hashChar("b")
	grant = sealGrant(t, grant)
	grant.ParentCheckpointSHA256 = hashChar("c")
	if err := ValidateNextStageGrantV1(grant); ClassOf(err) != CapsuleLineageInvalid {
		t.Fatalf("rebound grant class = %q, err=%v", ClassOf(err), err)
	}
}

func TestCleanCheckpointsBindValidatedTipZeroVerdictAndFinalFloors(t *testing.T) {
	registry := fixtureRegistry(t)
	bCapsule := fixtureCapsule(contextcapsule.StageBImplementation, registry.RegistrySHA256)
	capsuleFile := hashChar("a")
	cleanB := sealReport(t, bCapsule, capsuleFile, registry, nil, ReviewScopeReportV1{
		Kind: "ReviewScopeReportV1", CapsuleFileSHA256: capsuleFile, CapsuleSHA256: bCapsule.CapsuleSHA256,
		SemanticRegistrySHA256: registry.RegistrySHA256, ReviewedPreFixHEAD: oidChar("2"), ActiveFindings: []ActiveFindingV1{},
		RequestedMutationIDs: []string{}, DeferredObservations: []DeferredObservationV1{}, ReviewerIdentity: "reviewer", ProviderIdentity: "provider", ReviewEvidenceSHA256: hashChar("3"),
	})
	checkpoint := PhaseCheckpointV1{
		Kind: CheckpointImplementationConverged, Repository: bCapsule.Repository, CapsuleFileSHA256: capsuleFile, CapsuleSHA256: bCapsule.CapsuleSHA256,
		CandidateSHA: cleanB.ReviewedPreFixHEAD, Operation: contextcapsule.OperationImplementationReview, Verdict: "IMPLEMENTATION_CONVERGED_C0_M0",
		Evidence: []EvidenceBindingV1{{Ref: "review/clean", SHA256: cleanB.ReviewEvidenceSHA256}}, ControllerPolicyIdentity: "policy-v3",
		Sequence: 1, PredecessorCheckpointSHA256: hashChar("4"), ControllerEventIdentity: "event-converged", ReviewScopeTipSHA256: cleanB.ReportSHA256,
		ReviewedBlockingScopeIDs: append([]string(nil), cleanB.ReviewedBlockingScopeIDs...),
	}
	checkpoint, cGrant, err := SealCheckpointWithNextStageGrantV1(checkpoint, fixtureCGrant(registry.RegistrySHA256))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateImplementationConvergedCheckpointV1(bCapsule, capsuleFile, registry, nil, cleanB, checkpoint, cGrant); err != nil {
		t.Fatal(err)
	}
	tampered := checkpoint
	tampered.ReviewScopeTipSHA256 = hashChar("9")
	tampered.CheckpointSHA256 = ""
	tampered = sealCheckpoint(t, tampered)
	if err := validateImplementationConvergedCheckpointV1(bCapsule, capsuleFile, registry, nil, cleanB, tampered, cGrant); ClassOf(err) != CheckpointChainInvalid {
		t.Fatalf("unbound clean tip class = %q, err=%v", ClassOf(err), err)
	}

	cCapsule := fixtureCapsule(contextcapsule.StageCAcceptanceMerge, registry.RegistrySHA256)
	cCapsule.BaseSHA = cleanB.ReviewedPreFixHEAD
	cCapsule.PhaseAuthority.Parent.GrantSHA256 = cGrant.GrantSHA256
	cleanC := sealReport(t, cCapsule, hashChar("5"), registry, nil, ReviewScopeReportV1{
		Kind: "ReviewScopeReportV1", CapsuleFileSHA256: hashChar("5"), CapsuleSHA256: cCapsule.CapsuleSHA256,
		SemanticRegistrySHA256: registry.RegistrySHA256, ReviewedPreFixHEAD: cCapsule.BaseSHA, ActiveFindings: []ActiveFindingV1{},
		RequestedMutationIDs: []string{}, DeferredObservations: []DeferredObservationV1{}, ReviewerIdentity: "final-reviewer", ProviderIdentity: "provider", ReviewEvidenceSHA256: hashChar("6"),
	})
	final := sealCheckpoint(t, PhaseCheckpointV1{
		Kind: CheckpointFinalReviewClean, Repository: cCapsule.Repository, CapsuleFileSHA256: hashChar("5"), CapsuleSHA256: cCapsule.CapsuleSHA256,
		CandidateSHA: cCapsule.BaseSHA, Operation: contextcapsule.OperationFinalReview, Verdict: "FINAL_REVIEW_CLEAN_C0_M0",
		Evidence: []EvidenceBindingV1{{Ref: "review/final", SHA256: cleanC.ReviewEvidenceSHA256}}, ControllerPolicyIdentity: "policy-v3",
		Sequence: 3, PredecessorCheckpointSHA256: hashChar("7"), ControllerEventIdentity: "event-final", ReviewScopeTipSHA256: cleanC.ReportSHA256,
		ReviewedBlockingScopeIDs: append([]string(nil), cleanC.ReviewedBlockingScopeIDs...),
	})
	if err := validateFinalReviewCleanCheckpointV1(cCapsule, hashChar("5"), registry, nil, cleanC, final, cGrant); err != nil {
		t.Fatal(err)
	}
	missingFloorCapsule := cCapsule
	missingFloorCapsule.PhaseAuthority = clonePhaseAuthority(cCapsule.PhaseAuthority)
	missingFloorCapsule.PhaseAuthority.BlockingScopeIDs = []string{"rule.other"}
	missingFloorReport := cleanC
	missingFloorReport.ReviewedBlockingScopeIDs = []string{"rule.other"}
	missingFloorReport.ReportSHA256 = ""
	missingFloorReport = sealReport(t, missingFloorCapsule, hashChar("5"), registry, nil, missingFloorReport)
	missingFloorFinal := final
	missingFloorFinal.ReviewScopeTipSHA256 = missingFloorReport.ReportSHA256
	missingFloorFinal.ReviewedBlockingScopeIDs = []string{"rule.other"}
	missingFloorFinal.CheckpointSHA256 = ""
	missingFloorFinal = sealCheckpoint(t, missingFloorFinal)
	if err := validateFinalReviewCleanCheckpointV1(missingFloorCapsule, hashChar("5"), registry, nil, missingFloorReport, missingFloorFinal, cGrant); ClassOf(err) != FinalReviewInvalidated {
		t.Fatalf("missing final floor class = %q, err=%v", ClassOf(err), err)
	}
}

func TestControllerCheckpointGateConsumesOnlyDurableCleanReviewTips(t *testing.T) {
	repository, base, identity := governanceRepository(t)
	registry := fixtureRegistry(t)
	backend := newTestWorkflowAuthorityStoreV1()
	controller, err := OpenControllerWithAuthorityBackendV1(repository, backend.client("host-a"))
	if err != nil {
		t.Fatal(err)
	}
	backend.seed(t, controller, identity)

	aCapsule := fixtureCapsule(contextcapsule.StageADesign, registry.RegistrySHA256)
	aCapsule.BaseSHA = base
	aCapsule.Repository = identity
	aCapsule.CapsuleSHA256 = hashChar("6")
	aFile := hashChar("a")
	design := PhaseCheckpointV1{
		Kind: CheckpointDesignAccepted, Repository: aCapsule.Repository, CapsuleFileSHA256: aFile, CapsuleSHA256: aCapsule.CapsuleSHA256,
		CandidateSHA: base, Operation: contextcapsule.OperationDesignReview, Verdict: "DESIGN_ACCEPTED",
		Evidence: []EvidenceBindingV1{{Ref: "design/accepted", SHA256: hashChar("1")}}, ControllerPolicyIdentity: "policy-v3",
		ControllerEventIdentity: "event-design", SemanticRegistrySHA256: registry.RegistrySHA256,
	}
	bGrant := fixtureGrant(registry.RegistrySHA256)
	bGrant.BaseSHA = base
	design, bGrant, err = SealCheckpointWithNextStageGrantV1(design, bGrant)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.AdvanceCheckpointV1(CheckpointAdvanceV1{Repository: repository, Capsule: aCapsule, CapsuleFileSHA256: aFile, Registry: registry, Checkpoint: design, NextStageGrant: &bGrant}); err != nil {
		t.Fatal(err)
	}

	bCapsule := fixtureCapsule(contextcapsule.StageBImplementation, registry.RegistrySHA256)
	bCapsule.BaseSHA = base
	bCapsule.Repository = identity
	bCapsule.CapsuleSHA256 = hashChar("7")
	bCapsule.PhaseAuthority.Parent = &contextcapsule.PhaseParentV1{CapsuleFileSHA256: aFile, CapsuleSHA256: aCapsule.CapsuleSHA256, Stage: contextcapsule.StageADesign, CheckpointSHA256: design.CheckpointSHA256, CandidateSHA: base, GrantSHA256: bGrant.GrantSHA256}
	bFile := hashChar("b")
	blocking := sealReport(t, bCapsule, bFile, registry, nil, ReviewScopeReportV1{
		Kind: "ReviewScopeReportV1", CapsuleFileSHA256: bFile, CapsuleSHA256: bCapsule.CapsuleSHA256,
		SemanticRegistrySHA256: registry.RegistrySHA256, ReviewedPreFixHEAD: base,
		ActiveFindings:       []ActiveFindingV1{{FindingID: "finding.one", RuleID: "rule.invariant", Severity: SeverityMajor, Evidence: []EvidenceBindingV1{{Ref: "review/blocking"}}}},
		RequestedMutationIDs: []string{}, DeferredObservations: []DeferredObservationV1{}, ReviewerIdentity: "reviewer", ProviderIdentity: "provider", ReviewEvidenceSHA256: hashChar("2"),
	})
	recordReportEvidence(t, controller, repository, bCapsule, registry, blocking)
	if err := controller.AdvanceReviewTipV1(repository, bCapsule, bFile, registry, blocking); err != nil {
		t.Fatal(err)
	}
	cleanB := blocking
	cleanB.Sequence = 1
	cleanB.PredecessorReportSHA256 = blocking.ReportSHA256
	cleanB.ActiveFindings = []ActiveFindingV1{}
	cleanB.RequestedMutationIDs = []string{}
	cleanB.ReviewEvidenceSHA256 = hashChar("3")
	cleanB.ReportSHA256 = ""
	cleanB = sealReport(t, bCapsule, bFile, registry, &blocking, cleanB)

	converged := PhaseCheckpointV1{
		Kind: CheckpointImplementationConverged, Repository: bCapsule.Repository, CapsuleFileSHA256: bFile, CapsuleSHA256: bCapsule.CapsuleSHA256,
		CandidateSHA: base, Operation: contextcapsule.OperationImplementationReview, Verdict: "IMPLEMENTATION_CONVERGED_C0_M0",
		Evidence: []EvidenceBindingV1{{Ref: "review/clean", SHA256: cleanB.ReviewEvidenceSHA256}}, ControllerPolicyIdentity: "policy-v3",
		Sequence: 1, PredecessorCheckpointSHA256: design.CheckpointSHA256, ControllerEventIdentity: "event-converged",
		ReviewScopeTipSHA256: cleanB.ReportSHA256, ReviewedBlockingScopeIDs: append([]string(nil), cleanB.ReviewedBlockingScopeIDs...),
	}
	cGrant := fixtureCGrant(registry.RegistrySHA256)
	cGrant.BaseSHA = base
	converged, cGrant, err = SealCheckpointWithNextStageGrantV1(converged, cGrant)
	if err != nil {
		t.Fatal(err)
	}
	convergenceInput := CheckpointAdvanceV1{Repository: repository, Capsule: bCapsule, CapsuleFileSHA256: bFile, Registry: registry, Checkpoint: converged, NextStageGrant: &cGrant}
	if err := controller.AdvanceCheckpointV1(convergenceInput); ClassOf(err) != CheckpointChainInvalid {
		t.Fatalf("caller-supplied clean report bypass class = %q, err=%v", ClassOf(err), err)
	}
	if err := controller.AdvanceReviewTipV1(repository, bCapsule, bFile, registry, cleanB); err != nil {
		t.Fatal(err)
	}
	if err := controller.AdvanceCheckpointV1(convergenceInput); err != nil {
		t.Fatal(err)
	}

	cCapsule := fixtureCapsule(contextcapsule.StageCAcceptanceMerge, registry.RegistrySHA256)
	cCapsule.BaseSHA = base
	cCapsule.Repository = identity
	cCapsule.CapsuleSHA256 = hashChar("8")
	cCapsule.PhaseAuthority.Parent = &contextcapsule.PhaseParentV1{CapsuleFileSHA256: bFile, CapsuleSHA256: bCapsule.CapsuleSHA256, Stage: contextcapsule.StageBImplementation, CheckpointSHA256: converged.CheckpointSHA256, CandidateSHA: base, GrantSHA256: cGrant.GrantSHA256}
	cFile := hashChar("c")
	acceptanceResult := fixtureAcceptanceResult(t, cCapsule)
	accepted := sealCheckpoint(t, PhaseCheckpointV1{
		Kind: CheckpointAcceptancePassed, Repository: cCapsule.Repository, CapsuleFileSHA256: cFile, CapsuleSHA256: cCapsule.CapsuleSHA256,
		CandidateSHA: base, Operation: contextcapsule.OperationAcceptance, Verdict: "ACCEPTANCE_PASSED", AcceptanceResultSHA256: acceptanceResult.AcceptanceSHA256,
		Evidence: append([]EvidenceBindingV1(nil), acceptanceResult.Evidence...), ControllerPolicyIdentity: "policy-v3",
		Sequence: 2, PredecessorCheckpointSHA256: converged.CheckpointSHA256, ControllerEventIdentity: "event-acceptance",
	})
	if err := controller.AdvanceCheckpointV1(CheckpointAdvanceV1{Repository: repository, Capsule: cCapsule, CapsuleFileSHA256: cFile, Registry: registry, Checkpoint: accepted, AcceptanceResult: &acceptanceResult}); err != nil {
		t.Fatal(err)
	}
	cleanC := sealReport(t, cCapsule, cFile, registry, nil, ReviewScopeReportV1{
		Kind: "ReviewScopeReportV1", CapsuleFileSHA256: cFile, CapsuleSHA256: cCapsule.CapsuleSHA256,
		SemanticRegistrySHA256: registry.RegistrySHA256, ReviewedPreFixHEAD: base, ActiveFindings: []ActiveFindingV1{},
		RequestedMutationIDs: []string{}, DeferredObservations: []DeferredObservationV1{}, ReviewerIdentity: "final-reviewer", ProviderIdentity: "provider", ReviewEvidenceSHA256: hashChar("5"),
	})
	final := sealCheckpoint(t, PhaseCheckpointV1{
		Kind: CheckpointFinalReviewClean, Repository: cCapsule.Repository, CapsuleFileSHA256: cFile, CapsuleSHA256: cCapsule.CapsuleSHA256,
		CandidateSHA: base, Operation: contextcapsule.OperationFinalReview, Verdict: "FINAL_REVIEW_CLEAN_C0_M0",
		Evidence: []EvidenceBindingV1{{Ref: "review/final", SHA256: cleanC.ReviewEvidenceSHA256}}, ControllerPolicyIdentity: "policy-v3",
		Sequence: 3, PredecessorCheckpointSHA256: accepted.CheckpointSHA256, ControllerEventIdentity: "event-final",
		ReviewScopeTipSHA256: cleanC.ReportSHA256, ReviewedBlockingScopeIDs: append([]string(nil), cleanC.ReviewedBlockingScopeIDs...),
	})
	finalInput := CheckpointAdvanceV1{Repository: repository, Capsule: cCapsule, CapsuleFileSHA256: cFile, Registry: registry, Checkpoint: final}
	if err := controller.AdvanceCheckpointV1(finalInput); ClassOf(err) != FinalReviewInvalidated {
		t.Fatalf("unstored final report bypass class = %q, err=%v", ClassOf(err), err)
	}
	if err := controller.AdvanceReviewTipV1(repository, cCapsule, cFile, registry, cleanC); err != nil {
		t.Fatal(err)
	}
	if err := controller.AdvanceCheckpointV1(finalInput); err != nil {
		t.Fatal(err)
	}
}

func fixtureRegistry(t *testing.T) SemanticAuthorityRegistryV1 {
	t.Helper()
	registry, err := SealSemanticAuthorityRegistryV1(SemanticAuthorityRegistryV1{
		Kind: "SemanticAuthorityRegistryV1",
		Entries: []SemanticRuleV1{{
			RuleID: "rule.invariant", Kind: RegistryInvariant, Obligation: "The validator fails closed.", EvidenceClass: "TEST",
			CorrectionRelation: "SAME_ROOT_CAUSE", OwningComponent: "governance", AllowedCorrectionPaths: []string{"internal/governance/**"}, ValidatorIdentity: "governance-test", ModelJudgmentMayObserve: true,
		}, {
			RuleID: "rule.other", Kind: RegistryInvariant, Obligation: "Other reviewed rule.", EvidenceClass: "TEST",
			CorrectionRelation: "SAME_ROOT_CAUSE", OwningComponent: "governance", AllowedCorrectionPaths: []string{"internal/governance/**"}, ValidatorIdentity: "other-test",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func fixtureCapsule(stage contextcapsule.Stage, registryDigest string) contextcapsule.Capsule {
	phase := &contextcapsule.PhaseAuthorityV3{
		Stage: stage, SemanticRegistrySHA256: registryDigest, ObservationScopeIDs: []string{"rule.invariant", "rule.other"},
		BlockingScopeIDs: []string{"rule.invariant", "rule.other"}, MutationScopeIDs: []string{"rule.invariant"}, AuthorizedFindingIDs: []string{},
		AuthorizedInvariantIDs: []string{"rule.invariant", "rule.other"}, AllowedPaths: []string{"internal/governance/**"}, ReviewProfile: contextcapsule.ReviewProfileNone,
	}
	switch stage {
	case contextcapsule.StageADesign:
		phase.AllowedOperations = []contextcapsule.OperationKind{contextcapsule.OperationDesignPlanning, contextcapsule.OperationDesignReview}
	case contextcapsule.StageBImplementation:
		phase.AllowedOperations = []contextcapsule.OperationKind{contextcapsule.OperationImplementation, contextcapsule.OperationImplementationReview}
		phase.Parent = &contextcapsule.PhaseParentV1{CapsuleFileSHA256: hashChar("a"), CapsuleSHA256: hashChar("b"), Stage: contextcapsule.StageADesign, CheckpointSHA256: hashChar("c"), CandidateSHA: oidChar("d"), GrantSHA256: hashChar("e")}
		phase.ReviewProfile = contextcapsule.ReviewProfileInitialImplementation
		phase.ExecutionBounds = fixtureBounds()
	case contextcapsule.StageCAcceptanceMerge:
		phase.AllowedOperations = []contextcapsule.OperationKind{contextcapsule.OperationAcceptance, contextcapsule.OperationFinalReview, contextcapsule.OperationMergeAuthorization, contextcapsule.OperationPostMergeAcceptance, contextcapsule.OperationPRPublication}
		phase.Parent = &contextcapsule.PhaseParentV1{CapsuleFileSHA256: hashChar("a"), CapsuleSHA256: hashChar("b"), Stage: contextcapsule.StageBImplementation, CheckpointSHA256: hashChar("c"), CandidateSHA: oidChar("d"), GrantSHA256: hashChar("e")}
		phase.MutationScopeIDs = []string{}
	}
	return contextcapsule.Capsule{PolicyVersion: contextcapsule.PolicyVersionV3, Repository: "example/project", BaseSHA: oidChar("d"), CapsuleSHA256: hashChar("f"), PhaseAuthority: phase}
}

func fixtureGrant(registryDigest string) NextStageGrantV1 {
	return NextStageGrantV1{
		Kind: "NextStageGrantV1", Stage: contextcapsule.StageBImplementation, BaseSHA: oidChar("2"), SemanticRegistrySHA256: registryDigest,
		AllowedOperations:   []contextcapsule.OperationKind{contextcapsule.OperationImplementation, contextcapsule.OperationImplementationReview},
		ObservationScopeIDs: []string{"rule.invariant", "rule.other"}, BlockingScopeIDs: []string{"rule.invariant", "rule.other"}, MutationScopeIDs: []string{"rule.invariant"},
		AuthorizedFindingIDs: []string{}, AuthorizedInvariantIDs: []string{"rule.invariant", "rule.other"}, AllowedPaths: []string{"internal/governance/**"},
		ReviewProfile: contextcapsule.ReviewProfileInitialImplementation, ExecutionBounds: fixtureBounds(),
		RequiredBlockingScopeIDs: []string{"rule.invariant"}, RequiredInvariantIDs: []string{"rule.invariant"},
		RequiredOperations: []contextcapsule.OperationKind{contextcapsule.OperationImplementation}, RequiredFinalReviewIDs: []string{"rule.invariant"},
	}
}

func fixtureCGrant(registryDigest string) NextStageGrantV1 {
	return NextStageGrantV1{
		Kind: "NextStageGrantV1", Stage: contextcapsule.StageCAcceptanceMerge, BaseSHA: oidChar("2"), SemanticRegistrySHA256: registryDigest,
		AllowedOperations:   []contextcapsule.OperationKind{contextcapsule.OperationAcceptance, contextcapsule.OperationFinalReview, contextcapsule.OperationMergeAuthorization, contextcapsule.OperationPostMergeAcceptance, contextcapsule.OperationPRPublication},
		ObservationScopeIDs: []string{"rule.invariant", "rule.other"}, BlockingScopeIDs: []string{"rule.invariant", "rule.other"}, MutationScopeIDs: []string{},
		AuthorizedFindingIDs: []string{}, AuthorizedInvariantIDs: []string{"rule.invariant", "rule.other"}, AllowedPaths: []string{"internal/governance/**"},
		ReviewProfile: contextcapsule.ReviewProfileNone, RequiredBlockingScopeIDs: []string{"rule.invariant"}, RequiredInvariantIDs: []string{"rule.invariant"},
		RequiredOperations: []contextcapsule.OperationKind{contextcapsule.OperationAcceptance, contextcapsule.OperationFinalReview}, RequiredFinalReviewIDs: []string{"rule.invariant"},
	}
}

func fixtureBounds() *contextcapsule.ExecutionBoundsV1 {
	return &contextcapsule.ExecutionBoundsV1{MaxIterations: 3, SessionTimeout: "30m0s", IdleTimeout: "10m0s", WallClockTimeout: "1h0m0s", AggregateWallClockTimeout: "2h0m0s", MaxIncompleteTasks: 1, MaxInitialActiveFindings: 4, MaxRalphexInvocations: 3, MaxReviewReports: 4, MaxMutationLeases: 3, MaxTotalFixBatches: 3, MaxChangedFiles: 10, MaxChangedBytes: 1000}
}

func fixtureAcceptanceResult(t *testing.T, capsule contextcapsule.Capsule) DeterministicAcceptanceResultV1 {
	t.Helper()
	result, err := SealDeterministicAcceptanceResultV1(DeterministicAcceptanceResultV1{
		Kind: "DeterministicAcceptanceResultV1", CapsuleSHA256: capsule.CapsuleSHA256, CandidateSHA: capsule.BaseSHA,
		Checks:              []AcceptanceCheckV1{{Name: "go test ./...", Required: true, Outcome: "PASS", EvidenceRefs: []string{"acceptance/test"}}},
		FinalGitEvidenceRef: "acceptance/git", FinalRepositoryHEAD: capsule.BaseSHA, RepositoryClean: true,
		Evidence: []EvidenceBindingV1{{Ref: "acceptance/git", SHA256: hashChar("4")}, {Ref: "acceptance/test", SHA256: hashChar("5")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func authorizeBCapsule(t *testing.T, controller *ControllerV1, repository string, child *contextcapsule.Capsule) {
	t.Helper()
	if child == nil || child.PhaseAuthority == nil || child.PhaseAuthority.Stage != contextcapsule.StageBImplementation {
		t.Fatal("B capsule is required")
	}
	phase := child.PhaseAuthority
	aCapsule := contextcapsule.Capsule{
		PolicyVersion: contextcapsule.PolicyVersionV3,
		Repository:    child.Repository,
		BaseSHA:       child.BaseSHA,
		CapsuleSHA256: hashChar("6"),
		PhaseAuthority: &contextcapsule.PhaseAuthorityV3{
			Stage:                  contextcapsule.StageADesign,
			AllowedOperations:      []contextcapsule.OperationKind{contextcapsule.OperationDesignPlanning, contextcapsule.OperationDesignReview},
			SemanticRegistrySHA256: phase.SemanticRegistrySHA256,
			ObservationScopeIDs:    append([]string(nil), phase.ObservationScopeIDs...),
			BlockingScopeIDs:       append([]string(nil), phase.BlockingScopeIDs...),
			MutationScopeIDs:       append([]string(nil), phase.MutationScopeIDs...),
			AuthorizedFindingIDs:   []string{},
			AuthorizedInvariantIDs: append([]string(nil), phase.AuthorizedInvariantIDs...),
			AllowedPaths:           append([]string(nil), phase.AllowedPaths...),
			ReviewProfile:          contextcapsule.ReviewProfileNone,
		},
	}
	aFile := hashChar("7")
	requiredID := ""
	for _, blocker := range phase.BlockingScopeIDs {
		if stringSubset([]string{blocker}, phase.AuthorizedInvariantIDs) {
			requiredID = blocker
			break
		}
	}
	if requiredID == "" {
		t.Fatal("B fixture lacks a blocker/invariant floor")
	}
	bounds := *phase.ExecutionBounds
	grant := NextStageGrantV1{
		Kind: "NextStageGrantV1", Stage: contextcapsule.StageBImplementation, BaseSHA: child.BaseSHA,
		SemanticRegistrySHA256: phase.SemanticRegistrySHA256,
		AllowedOperations:      append([]contextcapsule.OperationKind(nil), phase.AllowedOperations...), ObservationScopeIDs: append([]string(nil), phase.ObservationScopeIDs...),
		BlockingScopeIDs: append([]string(nil), phase.BlockingScopeIDs...), MutationScopeIDs: append([]string(nil), phase.MutationScopeIDs...),
		AuthorizedFindingIDs: append([]string{}, phase.AuthorizedFindingIDs...), AuthorizedInvariantIDs: append([]string{}, phase.AuthorizedInvariantIDs...),
		AllowedPaths: append([]string(nil), phase.AllowedPaths...), ReviewProfile: phase.ReviewProfile, ExecutionBounds: &bounds,
		RequiredBlockingScopeIDs: []string{requiredID}, RequiredInvariantIDs: []string{requiredID},
		RequiredOperations: []contextcapsule.OperationKind{contextcapsule.OperationImplementation}, RequiredFinalReviewIDs: []string{requiredID},
	}
	design := PhaseCheckpointV1{
		Kind: CheckpointDesignAccepted, Repository: child.Repository, CapsuleFileSHA256: aFile, CapsuleSHA256: aCapsule.CapsuleSHA256,
		CandidateSHA: child.BaseSHA, Operation: contextcapsule.OperationDesignReview, Verdict: "DESIGN_ACCEPTED",
		Evidence: []EvidenceBindingV1{{Ref: "design/accepted", SHA256: hashChar("8")}}, ControllerPolicyIdentity: "policy-v3",
		ControllerEventIdentity: "event-design", SemanticRegistrySHA256: phase.SemanticRegistrySHA256,
	}
	var err error
	design, grant, err = SealCheckpointWithNextStageGrantV1(design, grant)
	if err != nil {
		t.Fatal(err)
	}
	child.PhaseAuthority.Parent = &contextcapsule.PhaseParentV1{
		CapsuleFileSHA256: aFile, CapsuleSHA256: aCapsule.CapsuleSHA256, Stage: contextcapsule.StageADesign,
		CheckpointSHA256: design.CheckpointSHA256, CandidateSHA: design.CandidateSHA, GrantSHA256: grant.GrantSHA256,
	}
	if err := controller.AdvanceCheckpointV1(CheckpointAdvanceV1{Repository: repository, Capsule: aCapsule, CapsuleFileSHA256: aFile, Checkpoint: design, NextStageGrant: &grant}); err != nil {
		t.Fatal(err)
	}
}

func clonePhaseAuthority(input *contextcapsule.PhaseAuthorityV3) *contextcapsule.PhaseAuthorityV3 {
	clone := *input
	clone.AllowedOperations = append([]contextcapsule.OperationKind(nil), input.AllowedOperations...)
	clone.ObservationScopeIDs = append([]string(nil), input.ObservationScopeIDs...)
	clone.BlockingScopeIDs = append([]string(nil), input.BlockingScopeIDs...)
	clone.MutationScopeIDs = append([]string(nil), input.MutationScopeIDs...)
	clone.AuthorizedFindingIDs = append([]string{}, input.AuthorizedFindingIDs...)
	clone.AuthorizedInvariantIDs = append([]string(nil), input.AuthorizedInvariantIDs...)
	clone.AllowedPaths = append([]string(nil), input.AllowedPaths...)
	return &clone
}

func sealCheckpoint(t *testing.T, checkpoint PhaseCheckpointV1) PhaseCheckpointV1 {
	t.Helper()
	value, err := SealPhaseCheckpointV1(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func sealGrant(t *testing.T, grant NextStageGrantV1) NextStageGrantV1 {
	t.Helper()
	value, err := SealNextStageGrantV1(grant)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func sealReport(t *testing.T, capsule contextcapsule.Capsule, file string, registry SemanticAuthorityRegistryV1, previous *ReviewScopeReportV1, report ReviewScopeReportV1) ReviewScopeReportV1 {
	t.Helper()
	if report.ReviewedBlockingScopeIDs == nil {
		report.ReviewedBlockingScopeIDs = append([]string(nil), capsule.PhaseAuthority.BlockingScopeIDs...)
	}
	evidence := bindReportEvidence(t, capsule, registry, &report)
	if previous != nil {
		evidence = append(evidence, findingEvidenceForReport(t, capsule, registry, *previous)...)
	}
	value, err := SealReviewScopeReportV1(capsule, file, registry, evidence, previous, report)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func fixtureFindingEvidence(capsule contextcapsule.Capsule, registry SemanticAuthorityRegistryV1, finding ActiveFindingV1, ref string) FindingEvidenceV1 {
	rule := registryMap(registry)[finding.RuleID]
	artifactDigest := sha256.Sum256(findingArtifact(ref))
	return FindingEvidenceV1{
		Kind: "FindingEvidenceV1", Ref: ref, CapsuleSHA256: capsule.CapsuleSHA256, SemanticRegistrySHA256: registry.RegistrySHA256,
		CandidateSHA: capsule.BaseSHA, FindingID: finding.FindingID, RuleID: finding.RuleID, RegisteredKind: rule.Kind,
		EvidenceClass: rule.EvidenceClass, ValidatorIdentity: rule.ValidatorIdentity, Outcome: "VIOLATION_CONFIRMED", ArtifactSHA256: hex.EncodeToString(artifactDigest[:]),
	}
}

func findingArtifact(ref string) []byte { return []byte("controller artifact for " + ref) }

type testFindingEvidenceResolver struct {
	artifacts map[string][]byte
	validated []string
	err       error
}

func (r *testFindingEvidenceResolver) ResolveArtifactV1(_, _, ref string) ([]byte, error) {
	artifact, ok := r.artifacts[ref]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), artifact...), nil
}

func (r *testFindingEvidenceResolver) ConfirmViolationV1(_ string, _ string, rule SemanticRuleV1, _ []byte) error {
	r.validated = append(r.validated, rule.ValidatorIdentity)
	return r.err
}

func findingEvidenceForReport(t *testing.T, capsule contextcapsule.Capsule, registry SemanticAuthorityRegistryV1, report ReviewScopeReportV1) []FindingEvidenceV1 {
	t.Helper()
	var records []FindingEvidenceV1
	for _, finding := range report.ActiveFindings {
		for _, binding := range finding.Evidence {
			record := fixtureFindingEvidence(capsule, registry, finding, binding.Ref)
			record.CandidateSHA = report.ReviewedPreFixHEAD
			sealed, err := SealFindingEvidenceV1(capsule, registry, record)
			if err != nil {
				t.Fatal(err)
			}
			records = append(records, sealed)
		}
	}
	return records
}

func bindReportEvidence(t *testing.T, capsule contextcapsule.Capsule, registry SemanticAuthorityRegistryV1, report *ReviewScopeReportV1) []FindingEvidenceV1 {
	t.Helper()
	records := findingEvidenceForReport(t, capsule, registry, *report)
	byRef := make(map[string]string, len(records))
	for _, record := range records {
		byRef[record.Ref] = record.EvidenceSHA256
	}
	for findingIndex := range report.ActiveFindings {
		for evidenceIndex := range report.ActiveFindings[findingIndex].Evidence {
			binding := &report.ActiveFindings[findingIndex].Evidence[evidenceIndex]
			binding.SHA256 = byRef[binding.Ref]
		}
	}
	return records
}

func recordReportEvidence(t *testing.T, controller *ControllerV1, repository string, capsule contextcapsule.Capsule, registry SemanticAuthorityRegistryV1, report ReviewScopeReportV1) {
	t.Helper()
	resolver := &testFindingEvidenceResolver{artifacts: make(map[string][]byte)}
	controller.findingEvidenceResolver = resolver
	for _, record := range findingEvidenceForReport(t, capsule, registry, report) {
		resolver.artifacts[record.Ref] = findingArtifact(record.Ref)
		resolved, err := controller.RecordFindingEvidenceV1(repository, capsule, registry, FindingEvidenceRequestV1{
			Ref: record.Ref, CandidateSHA: record.CandidateSHA, FindingID: record.FindingID, RuleID: record.RuleID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if resolved.EvidenceSHA256 != record.EvidenceSHA256 {
			t.Fatalf("controller-resolved evidence digest = %s, want %s", resolved.EvidenceSHA256, record.EvidenceSHA256)
		}
	}
}

type testWorkflowAuthorityRecordV1 struct {
	canonicalState []byte
	revision       uint64
}

type testWorkflowAuthorityStoreV1 struct {
	mu      sync.Mutex
	domain  string
	records map[string]testWorkflowAuthorityRecordV1
}

type testWorkflowAuthorityClientV1 struct {
	store       *testWorkflowAuthorityStoreV1
	environment string
}

func newTestWorkflowAuthorityStoreV1() *testWorkflowAuthorityStoreV1 {
	return newTestWorkflowAuthorityStoreV1WithDomain(hashChar("f"))
}

func newTestWorkflowAuthorityStoreV1WithDomain(domain string) *testWorkflowAuthorityStoreV1 {
	return &testWorkflowAuthorityStoreV1{domain: domain, records: make(map[string]testWorkflowAuthorityRecordV1)}
}

func (s *testWorkflowAuthorityStoreV1) client(environment string) *testWorkflowAuthorityClientV1 {
	return &testWorkflowAuthorityClientV1{store: s, environment: environment}
}

func (c *testWorkflowAuthorityClientV1) AuthorityDomainV1() (string, error) {
	if c == nil || c.store == nil || c.environment == "" {
		return "", errors.New("test authority client unavailable")
	}
	return c.store.domain, nil
}

func (c *testWorkflowAuthorityClientV1) LoadWorkflowStateV1(controllerIdentity string) ([]byte, uint64, error) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	record, ok := c.store.records[controllerIdentity]
	if !ok {
		return nil, 0, errors.New("authoritative workflow state is not initialized")
	}
	return append([]byte(nil), record.canonicalState...), record.revision, nil
}

func (c *testWorkflowAuthorityClientV1) CompareAndSwapWorkflowStateV1(controllerIdentity string, expectedRevision uint64, canonicalState []byte) (bool, error) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	record, ok := c.store.records[controllerIdentity]
	if !ok {
		return false, errors.New("authoritative workflow state is not initialized")
	}
	if record.revision != expectedRevision {
		return false, nil
	}
	var state ControllerStateV1
	if err := ParseCanonical(canonicalState, &state); err != nil {
		return false, err
	}
	c.store.records[controllerIdentity] = testWorkflowAuthorityRecordV1{canonicalState: append([]byte(nil), canonicalState...), revision: state.Revision}
	return true, nil
}

func (s *testWorkflowAuthorityStoreV1) seed(t *testing.T, controller *ControllerV1, repositoryIdentity string) {
	t.Helper()
	state := ControllerStateV1{
		Kind: "GovernanceControllerStateV1", ControllerIdentity: controller.identity, RepositoryIdentity: repositoryIdentity, Revision: 1,
		IssuedV2Authorities: []IssuedAuthorityV1{}, ExecutionState: ralphex.ExecutionStateV1{AggregateElapsed: "0s"}, FindingEvidence: []FindingEvidenceV1{},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.records[controller.identity]; exists {
		t.Fatal("test authority state already initialized")
	}
	s.records[controller.identity] = testWorkflowAuthorityRecordV1{canonicalState: data, revision: state.Revision}
}

func (s *testWorkflowAuthorityStoreV1) copyRecord(t *testing.T, sourceIdentity, targetIdentity string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[sourceIdentity]
	if !ok {
		t.Fatal("source authority record is absent")
	}
	s.records[targetIdentity] = testWorkflowAuthorityRecordV1{canonicalState: append([]byte(nil), record.canonicalState...), revision: record.revision}
}

func (s *testWorkflowAuthorityStoreV1) rewriteState(t *testing.T, controller *ControllerV1, mutate func(*ControllerStateV1)) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[controller.identity]
	if !ok {
		t.Fatal("controller state is absent")
	}
	var state ControllerStateV1
	if err := ParseCanonical(record.canonicalState, &state); err != nil {
		t.Fatal(err)
	}
	mutate(&state)
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	s.records[controller.identity] = testWorkflowAuthorityRecordV1{canonicalState: data, revision: record.revision}
}

func hashChar(character string) string { return strings.Repeat(character, 64) }
func oidChar(character string) string  { return strings.Repeat(character, 40) }

func gitTest(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func governanceRepository(t *testing.T) (string, string, string) {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	gitTest(t, "", "init", "-b", "main", repository)
	gitTest(t, repository, "config", "user.email", "governance@example.test")
	gitTest(t, repository, "config", "user.name", "Governance Test")
	remoteDigest := sha256.Sum256([]byte(repository))
	identity := "example/project-" + hex.EncodeToString(remoteDigest[:8])
	remoteURL := "https://example.test/" + identity + ".git"
	gitTest(t, repository, "remote", "add", "origin", remoteURL)
	if err := os.MkdirAll(filepath.Join(repository, "internal", "governance"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repository, filepath.Dir(GovernancePolicyPathV1)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "internal", "governance", "base.go"), []byte("package governance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, GovernancePolicyPathV1), []byte("context authority policy v3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", ".")
	gitTest(t, repository, "commit", "-m", "base")
	return repository, gitTest(t, repository, "rev-parse", "HEAD"), identity
}

func committedPolicySHA256(t *testing.T, repository, commit string) string {
	t.Helper()
	data := gitTest(t, repository, "show", commit+":"+GovernancePolicyPathV1)
	digest := sha256.Sum256([]byte(data + "\n"))
	return hex.EncodeToString(digest[:])
}

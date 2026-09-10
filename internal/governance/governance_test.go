package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
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
		CandidateSHA: converged.CandidateSHA, Operation: contextcapsule.OperationAcceptance, Verdict: "PASSED",
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

func TestReviewSetCannotGrowAndLeaseIsOneUse(t *testing.T) {
	registry := fixtureRegistry(t)
	capsule := fixtureCapsule(contextcapsule.StageBImplementation, registry.RegistrySHA256)
	capsuleFile := hashChar("a")
	report0 := sealReport(t, capsule, capsuleFile, registry, nil, ReviewScopeReportV1{
		Kind: "ReviewScopeReportV1", CapsuleFileSHA256: capsuleFile, CapsuleSHA256: capsule.CapsuleSHA256,
		SemanticRegistrySHA256: registry.RegistrySHA256, ReviewedPreFixHEAD: oidChar("1"),
		ActiveFindings:       []ActiveFindingV1{{FindingID: "finding.one", RuleID: "rule.invariant", Severity: SeverityMajor, EvidenceRefs: []string{"review/one"}}},
		RequestedMutationIDs: []string{"finding.one"}, DeferredObservations: []DeferredObservationV1{},
		ReviewerIdentity: "reviewer", ProviderIdentity: "provider", ReviewEvidenceSHA256: hashChar("2"),
	})
	report1 := report0
	report1.Sequence = 1
	report1.PredecessorReportSHA256 = report0.ReportSHA256
	report1.ReportSHA256 = ""
	report1.ActiveFindings = append(report1.ActiveFindings, ActiveFindingV1{FindingID: "finding.two", RuleID: "rule.invariant", Severity: SeverityMajor, EvidenceClass: "TEST", ValidatorIdentity: "governance-test", EvidenceRefs: []string{"review/two"}})
	if _, err := SealReviewScopeReportV1(capsule, capsuleFile, registry, &report0, report1); ClassOf(err) != ScopeExpansionRequired {
		t.Fatalf("growth class = %q, err=%v", ClassOf(err), err)
	}
	lease, err := IssueMutationLeaseV1(capsule, registry, report0, MutationLimitsV1{MaxChangedFiles: 2, MaxChangedBytes: 100})
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
	gitTest(t, repository, "remote", "add", "origin", "https://example.test/example/project.git")
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

func TestControllerActivationCannotBeBypassedByOmittedCallerState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "controller", "state.json")
	controller, err := OpenControllerV1(statePath)
	if err != nil {
		t.Fatal(err)
	}
	grandfathered := hashChar("3")
	if err := controller.AdmitWorkflowAuthority(contextcapsule.PolicyVersionV2, grandfathered); err != nil {
		t.Fatal(err)
	}
	activation, err := SealGovernanceActivationV1(GovernanceActivationV1{
		Kind: "GovernanceActivationV1", PolicyVersion: contextcapsule.PolicyVersionV3, PolicySHA256: hashChar("1"),
		ActivationRepositoryCommit: oidChar("2"), ActivationSequence: 2, ActivationTime: time.Unix(0, 0).UTC().Format(time.RFC3339),
		GrandfatheredV2Digests: []string{grandfathered},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.InstallActivationV1(activation); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenControllerV1(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.AdmitWorkflowAuthority(contextcapsule.PolicyVersionV2, grandfathered); err != nil {
		t.Fatalf("durably grandfathered authority was rejected: %v", err)
	}
	if err := reopened.AdmitWorkflowAuthority(contextcapsule.PolicyVersionV2, hashChar("4")); ClassOf(err) != CapsuleLineageInvalid {
		t.Fatalf("post-activation omitted-state bypass class = %q, err=%v", ClassOf(err), err)
	}
	if err := reopened.AdmitWorkflowAuthority(contextcapsule.PolicyVersionV3, hashChar("5")); err != nil {
		t.Fatal(err)
	}
}

func TestControllerCountersAndInFlightReservationAreDurable(t *testing.T) {
	capsule := fixtureCapsule(contextcapsule.StageBImplementation, hashChar("a"))
	capsule.PhaseAuthority.ExecutionBounds.MaxRalphexInvocations = 1
	path := filepath.Join(t.TempDir(), "state.json")
	first, err := OpenControllerV1(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenControllerV1(path)
	if err != nil {
		t.Fatal(err)
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

func TestControllerLeaseCASAndReceiptUseIndependentRepositoryProof(t *testing.T) {
	repository, base := governanceRepository(t)
	registry := fixtureRegistry(t)
	capsule := fixtureCapsule(contextcapsule.StageBImplementation, registry.RegistrySHA256)
	capsule.BaseSHA = base
	capsuleFile := hashChar("a")
	report := sealReport(t, capsule, capsuleFile, registry, nil, ReviewScopeReportV1{
		Kind: "ReviewScopeReportV1", CapsuleFileSHA256: capsuleFile, CapsuleSHA256: capsule.CapsuleSHA256,
		SemanticRegistrySHA256: registry.RegistrySHA256, ReviewedPreFixHEAD: base,
		ActiveFindings:       []ActiveFindingV1{{FindingID: "finding.one", RuleID: "rule.invariant", Severity: SeverityMajor, EvidenceRefs: []string{"review/one"}}},
		RequestedMutationIDs: []string{"finding.one"}, DeferredObservations: []DeferredObservationV1{},
		ReviewerIdentity: "reviewer", ProviderIdentity: "provider", ReviewEvidenceSHA256: hashChar("2"),
	})
	path := filepath.Join(t.TempDir(), "state.json")
	controller, err := OpenControllerV1(path)
	if err != nil {
		t.Fatal(err)
	}
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
	if err := controller.BeginMutationLeaseV1(lease.LeaseSHA256); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenControllerV1(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.BeginMutationLeaseV1(lease.LeaseSHA256); ClassOf(err) != MutationScopeViolation {
		t.Fatalf("lease replay class = %q, err=%v", ClassOf(err), err)
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
		ActiveFindings:       []ActiveFindingV1{{FindingID: "finding.one", RuleID: "rule.invariant", Severity: SeverityMajor, EvidenceClass: "WRONG", ValidatorIdentity: "governance-test", EvidenceRefs: []string{"review/one"}}},
		RequestedMutationIDs: []string{"finding.one"}, DeferredObservations: []DeferredObservationV1{}, ReviewerIdentity: "reviewer", ProviderIdentity: "provider", ReviewEvidenceSHA256: hashChar("2"),
	}
	if _, err := SealReviewScopeReportV1(capsule, base.CapsuleFileSHA256, registry, nil, base); ClassOf(err) != ReviewChainInvalid {
		t.Fatalf("evidence-class mismatch = %q, err=%v", ClassOf(err), err)
	}
	base.ActiveFindings[0].EvidenceClass = "TEST"
	base.ActiveFindings[0].ModelJudgmentUsed = true
	if _, err := SealReviewScopeReportV1(capsule, base.CapsuleFileSHA256, registry, nil, base); ClassOf(err) != ReviewChainInvalid {
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
	bad := base
	bad.CapsuleSHA256 = badCapsule.CapsuleSHA256
	bad.SemanticRegistrySHA256 = badRegistry.RegistrySHA256
	bad.ReviewedBlockingScopeIDs = append([]string(nil), badCapsule.PhaseAuthority.BlockingScopeIDs...)
	bad.ActiveFindings = []ActiveFindingV1{{FindingID: "finding.one", RuleID: "rule.non-goal", Severity: SeverityMajor, EvidenceClass: "REVIEW", EvidenceRefs: []string{"review/one"}}}
	if _, err := SealReviewScopeReportV1(badCapsule, bad.CapsuleFileSHA256, badRegistry, nil, bad); ClassOf(err) != ScopeExpansionRequired {
		t.Fatalf("non-blocking registry kind class = %q, err=%v", ClassOf(err), err)
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
	if err := ValidateImplementationConvergedCheckpointV1(bCapsule, capsuleFile, registry, nil, cleanB, checkpoint, cGrant); err != nil {
		t.Fatal(err)
	}
	tampered := checkpoint
	tampered.ReviewScopeTipSHA256 = hashChar("9")
	tampered.CheckpointSHA256 = ""
	tampered = sealCheckpoint(t, tampered)
	if err := ValidateImplementationConvergedCheckpointV1(bCapsule, capsuleFile, registry, nil, cleanB, tampered, cGrant); ClassOf(err) != CheckpointChainInvalid {
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
	if err := ValidateFinalReviewCleanCheckpointV1(cCapsule, hashChar("5"), registry, cleanC, final, cGrant); err != nil {
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
	if err := ValidateFinalReviewCleanCheckpointV1(missingFloorCapsule, hashChar("5"), registry, missingFloorReport, missingFloorFinal, cGrant); ClassOf(err) != FinalReviewInvalidated {
		t.Fatalf("missing final floor class = %q, err=%v", ClassOf(err), err)
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

func clonePhaseAuthority(input *contextcapsule.PhaseAuthorityV3) *contextcapsule.PhaseAuthorityV3 {
	clone := *input
	clone.AllowedOperations = append([]contextcapsule.OperationKind(nil), input.AllowedOperations...)
	clone.ObservationScopeIDs = append([]string(nil), input.ObservationScopeIDs...)
	clone.BlockingScopeIDs = append([]string(nil), input.BlockingScopeIDs...)
	clone.MutationScopeIDs = append([]string(nil), input.MutationScopeIDs...)
	clone.AuthorizedFindingIDs = append([]string(nil), input.AuthorizedFindingIDs...)
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
	rules := registryMap(registry)
	for index := range report.ActiveFindings {
		rule := rules[report.ActiveFindings[index].RuleID]
		report.ActiveFindings[index].EvidenceClass = rule.EvidenceClass
		report.ActiveFindings[index].ValidatorIdentity = rule.ValidatorIdentity
	}
	value, err := SealReviewScopeReportV1(capsule, file, registry, previous, report)
	if err != nil {
		t.Fatal(err)
	}
	return value
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

func governanceRepository(t *testing.T) (string, string) {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	gitTest(t, "", "init", "-b", "main", repository)
	gitTest(t, repository, "config", "user.email", "governance@example.test")
	gitTest(t, repository, "config", "user.name", "Governance Test")
	gitTest(t, repository, "remote", "add", "origin", "https://example.test/example/project.git")
	if err := os.MkdirAll(filepath.Join(repository, "internal", "governance"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "internal", "governance", "base.go"), []byte("package governance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", ".")
	gitTest(t, repository, "commit", "-m", "base")
	return repository, gitTest(t, repository, "rev-parse", "HEAD")
}

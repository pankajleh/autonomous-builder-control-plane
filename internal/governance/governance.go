// Package governance implements the controller-owned evidence validators for
// the context-capsule-v3 A/B/C autonomous-development workflow.
package governance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

// FailureClass is a stable machine-readable fail-closed conclusion.
type FailureClass string

const (
	CapsuleStageInvalid    FailureClass = "CAPSULE_STAGE_INVALID"
	CapsuleLineageInvalid  FailureClass = "CAPSULE_LINEAGE_INVALID"
	CapsuleUsageInvalid    FailureClass = "CAPSULE_USAGE_INVALID"
	CheckpointChainInvalid FailureClass = "CHECKPOINT_CHAIN_INVALID"
	CandidateHeadInvalid   FailureClass = "CANDIDATE_HEAD_INVALID"
	MutationScopeViolation FailureClass = "MUTATION_SCOPE_VIOLATION"
	ScopeExpansionRequired FailureClass = "SCOPE_EXPANSION_REQUIRED"
	DesignGap              FailureClass = "DESIGN_GAP"
	ReviewChainInvalid     FailureClass = "REVIEW_CHAIN_INVALID"
	ExecutionBoundsInvalid FailureClass = "EXECUTION_BOUNDS_INVALID"
	FinalReviewInvalidated FailureClass = "FINAL_REVIEW_INVALIDATED"
)

// ValidationError preserves a deterministic failure class without granting
// retry, mutation, or scope expansion.
type ValidationError struct {
	Class  FailureClass `json:"class"`
	Detail string       `json:"detail"`
}

func (e *ValidationError) Error() string { return string(e.Class) + ": " + e.Detail }

func fail(class FailureClass, format string, arguments ...any) error {
	return &ValidationError{Class: class, Detail: fmt.Sprintf(format, arguments...)}
}

// ClassOf returns the stable class carried by a validator error.
func ClassOf(err error) FailureClass {
	var validation *ValidationError
	if errors.As(err, &validation) {
		return validation.Class
	}
	return ""
}

// RegistryKind is the closed semantic registry entry vocabulary.
type RegistryKind string

const (
	RegistryInvariant    RegistryKind = "INVARIANT"
	RegistryKnownFinding RegistryKind = "KNOWN_FINDING"
	RegistryNonGoal      RegistryKind = "NON_GOAL"
	RegistryScope        RegistryKind = "SCOPE"
)

// SemanticRuleV1 binds one immutable semantic predicate to its proof and
// correction authority.
type SemanticRuleV1 struct {
	RuleID                  string       `json:"rule_id"`
	Kind                    RegistryKind `json:"kind"`
	Obligation              string       `json:"obligation"`
	EvidenceClass           string       `json:"evidence_class"`
	CorrectionRelation      string       `json:"correction_relation"`
	OwningComponent         string       `json:"owning_component"`
	AllowedCorrectionPaths  []string     `json:"allowed_correction_paths"`
	ValidatorIdentity       string       `json:"validator_identity,omitempty"`
	ModelJudgmentMayObserve bool         `json:"model_judgment_may_observe"`
}

// SemanticAuthorityRegistryV1 is reviewed under A and referred to by digest
// from all descendants.
type SemanticAuthorityRegistryV1 struct {
	Kind           string           `json:"kind"`
	Entries        []SemanticRuleV1 `json:"entries"`
	RegistrySHA256 string           `json:"registry_sha256"`
}

// SealSemanticAuthorityRegistryV1 validates and hashes a registry.
func SealSemanticAuthorityRegistryV1(registry SemanticAuthorityRegistryV1) (SemanticAuthorityRegistryV1, error) {
	registry.RegistrySHA256 = ""
	if err := validateRegistryPayload(registry); err != nil {
		return SemanticAuthorityRegistryV1{}, err
	}
	digest, err := digestJSON(struct {
		Kind    string           `json:"kind"`
		Entries []SemanticRuleV1 `json:"entries"`
	}{registry.Kind, registry.Entries})
	if err != nil {
		return SemanticAuthorityRegistryV1{}, err
	}
	registry.RegistrySHA256 = digest
	return registry, nil
}

// ValidateSemanticAuthorityRegistryV1 validates the payload and internal hash.
func ValidateSemanticAuthorityRegistryV1(registry SemanticAuthorityRegistryV1) error {
	if err := validateRegistryPayload(registry); err != nil {
		return err
	}
	sealed, err := SealSemanticAuthorityRegistryV1(registry)
	if err != nil {
		return err
	}
	if registry.RegistrySHA256 != sealed.RegistrySHA256 {
		return fail(CapsuleLineageInvalid, "semantic registry digest mismatch")
	}
	return nil
}

func validateRegistryPayload(registry SemanticAuthorityRegistryV1) error {
	if registry.Kind != "SemanticAuthorityRegistryV1" {
		return fail(CapsuleLineageInvalid, "semantic registry kind is invalid")
	}
	if len(registry.Entries) == 0 || len(registry.Entries) > 256 {
		return fail(CapsuleLineageInvalid, "semantic registry must contain between 1 and 256 entries")
	}
	previous := ""
	for index, entry := range registry.Entries {
		if !validID(entry.RuleID) || entry.RuleID <= previous {
			return fail(CapsuleLineageInvalid, "registry entries must have sorted unique rule IDs at index %d", index)
		}
		previous = entry.RuleID
		switch entry.Kind {
		case RegistryInvariant, RegistryKnownFinding, RegistryNonGoal, RegistryScope:
		default:
			return fail(CapsuleLineageInvalid, "registry rule %s has invalid kind", entry.RuleID)
		}
		for name, value := range map[string]string{
			"obligation": entry.Obligation, "evidence_class": entry.EvidenceClass,
			"correction_relation": entry.CorrectionRelation, "owning_component": entry.OwningComponent,
		} {
			if !validText(value, 4096) {
				return fail(CapsuleLineageInvalid, "registry rule %s has invalid %s", entry.RuleID, name)
			}
		}
		if entry.ValidatorIdentity != "" && !validText(entry.ValidatorIdentity, 512) {
			return fail(CapsuleLineageInvalid, "registry rule %s has invalid validator_identity", entry.RuleID)
		}
		if err := validatePaths(entry.AllowedCorrectionPaths, true); err != nil {
			return fail(CapsuleLineageInvalid, "registry rule %s: %v", entry.RuleID, err)
		}
	}
	return nil
}

// CheckpointKind is the closed controller checkpoint sequence.
type CheckpointKind string

const (
	CheckpointDesignAccepted          CheckpointKind = "DESIGN_ACCEPTED"
	CheckpointImplementationConverged CheckpointKind = "IMPLEMENTATION_CONVERGED"
	CheckpointAcceptancePassed        CheckpointKind = "ACCEPTANCE_PASSED"
	CheckpointFinalReviewClean        CheckpointKind = "FINAL_REVIEW_CLEAN"
	CheckpointPRPublished             CheckpointKind = "PR_PUBLISHED"
	CheckpointMergeAuthorized         CheckpointKind = "MERGE_AUTHORIZED"
	CheckpointPostMergeAccepted       CheckpointKind = "POST_MERGE_ACCEPTED"
)

// EvidenceBindingV1 names and hashes exact immutable checkpoint evidence.
type EvidenceBindingV1 struct {
	Ref    string `json:"ref"`
	SHA256 string `json:"sha256"`
}

// LifecycleBindingV1 carries the EP-005 identities which C adds to, but never
// replaces, at publication and merge authorization.
type LifecycleBindingV1 struct {
	Repository             string `json:"repository"`
	PRIdentity             string `json:"pr_identity"`
	BaseBranch             string `json:"base_branch"`
	ExpectedBaseOID        string `json:"expected_base_oid"`
	HeadOID                string `json:"head_oid"`
	MergeMethod            string `json:"merge_method,omitempty"`
	ActingPrincipal        string `json:"acting_principal,omitempty"`
	PolicyEvidenceSHA256   string `json:"policy_evidence_sha256,omitempty"`
	ExpectedResultOID      string `json:"expected_result_oid,omitempty"`
	ProviderEvidenceSHA256 string `json:"provider_evidence_sha256,omitempty"`
}

// PhaseCheckpointV1 is one immutable link in the non-forkable workflow chain.
type PhaseCheckpointV1 struct {
	Kind                        CheckpointKind               `json:"kind"`
	Repository                  string                       `json:"repository"`
	CapsuleFileSHA256           string                       `json:"capsule_file_sha256"`
	CapsuleSHA256               string                       `json:"capsule_sha256"`
	CandidateSHA                string                       `json:"candidate_sha"`
	Operation                   contextcapsule.OperationKind `json:"operation"`
	Verdict                     string                       `json:"verdict"`
	Evidence                    []EvidenceBindingV1          `json:"evidence"`
	ControllerPolicyIdentity    string                       `json:"controller_policy_identity"`
	Sequence                    uint64                       `json:"sequence"`
	PredecessorCheckpointSHA256 string                       `json:"predecessor_checkpoint_sha256,omitempty"`
	ControllerEventIdentity     string                       `json:"controller_event_identity"`
	SemanticRegistrySHA256      string                       `json:"semantic_registry_sha256,omitempty"`
	NextStageGrantSHA256        string                       `json:"next_stage_grant_sha256,omitempty"`
	ReviewScopeTipSHA256        string                       `json:"review_scope_tip_sha256,omitempty"`
	ReviewCritical              int                          `json:"review_critical"`
	ReviewMajor                 int                          `json:"review_major"`
	ReviewedBlockingScopeIDs    []string                     `json:"reviewed_blocking_scope_ids,omitempty"`
	Lifecycle                   *LifecycleBindingV1          `json:"lifecycle,omitempty"`
	CheckpointSHA256            string                       `json:"checkpoint_sha256"`
}

// CheckpointAdvanceV1 is the complete controller input for advancing the one
// repository-owned checkpoint tip. Review predecessors are deliberately not
// accepted here; convergence gates consume the controller's durable tip.
type CheckpointAdvanceV1 struct {
	Repository        string                      `json:"repository"`
	Capsule           contextcapsule.Capsule      `json:"capsule"`
	CapsuleFileSHA256 string                      `json:"capsule_file_sha256"`
	Registry          SemanticAuthorityRegistryV1 `json:"registry"`
	Checkpoint        PhaseCheckpointV1           `json:"checkpoint"`
	NextStageGrant    *NextStageGrantV1           `json:"next_stage_grant,omitempty"`
}

// SealPhaseCheckpointV1 returns a validated checkpoint with its digest set.
func SealPhaseCheckpointV1(checkpoint PhaseCheckpointV1) (PhaseCheckpointV1, error) {
	checkpoint.CheckpointSHA256 = ""
	if err := validateCheckpointPayload(checkpoint); err != nil {
		return PhaseCheckpointV1{}, err
	}
	digest, err := checkpointDigest(checkpoint)
	if err != nil {
		return PhaseCheckpointV1{}, err
	}
	checkpoint.CheckpointSHA256 = digest
	return checkpoint, nil
}

// ValidatePhaseCheckpointV1 validates a strict checkpoint and its digest.
func ValidatePhaseCheckpointV1(checkpoint PhaseCheckpointV1) error {
	if err := validateCheckpointPayload(checkpoint); err != nil {
		return err
	}
	digest, err := checkpointDigest(checkpoint)
	if err != nil {
		return err
	}
	if checkpoint.CheckpointSHA256 != digest {
		return fail(CheckpointChainInvalid, "checkpoint digest mismatch")
	}
	return nil
}

// ValidateCheckpointTransitionV1 enforces the single durable tip, exact next
// sequence, and the fixed C exact-head sequence.
func ValidateCheckpointTransitionV1(current *PhaseCheckpointV1, next PhaseCheckpointV1) error {
	if err := ValidatePhaseCheckpointV1(next); err != nil {
		return err
	}
	if current == nil {
		if next.Sequence != 0 || next.PredecessorCheckpointSHA256 != "" || next.Kind != CheckpointDesignAccepted {
			return fail(CheckpointChainInvalid, "first checkpoint must be DESIGN_ACCEPTED sequence 0 with no predecessor")
		}
		return nil
	}
	if err := ValidatePhaseCheckpointV1(*current); err != nil {
		return err
	}
	if next.CheckpointSHA256 == current.CheckpointSHA256 {
		return nil
	}
	if next.Sequence != current.Sequence+1 || next.PredecessorCheckpointSHA256 != current.CheckpointSHA256 {
		return fail(CheckpointChainInvalid, "checkpoint does not extend the exact durable tip")
	}
	if next.Repository != current.Repository {
		return fail(CheckpointChainInvalid, "repository identity changed")
	}
	want := map[CheckpointKind]CheckpointKind{
		CheckpointDesignAccepted:          CheckpointImplementationConverged,
		CheckpointImplementationConverged: CheckpointAcceptancePassed,
		CheckpointAcceptancePassed:        CheckpointFinalReviewClean,
		CheckpointFinalReviewClean:        CheckpointPRPublished,
		CheckpointPRPublished:             CheckpointMergeAuthorized,
		CheckpointMergeAuthorized:         CheckpointPostMergeAccepted,
	}
	if want[current.Kind] != next.Kind {
		return fail(CheckpointChainInvalid, "%s may not extend %s", next.Kind, current.Kind)
	}
	if checkpointAtOrAfter(current.Kind, CheckpointImplementationConverged) && next.CandidateSHA != current.CandidateSHA {
		return fail(FinalReviewInvalidated, "C checkpoint candidate changed from %s to %s", current.CandidateSHA, next.CandidateSHA)
	}
	if checkpointAtOrAfter(current.Kind, CheckpointAcceptancePassed) && (next.CapsuleFileSHA256 != current.CapsuleFileSHA256 || next.CapsuleSHA256 != current.CapsuleSHA256) {
		return fail(FinalReviewInvalidated, "C capsule changed inside exact-head chain")
	}
	if current.Kind == CheckpointPRPublished && next.Kind == CheckpointMergeAuthorized {
		if current.Lifecycle == nil || next.Lifecycle == nil || current.Lifecycle.Repository != next.Lifecycle.Repository || current.Lifecycle.PRIdentity != next.Lifecycle.PRIdentity || current.Lifecycle.BaseBranch != next.Lifecycle.BaseBranch || current.Lifecycle.ExpectedBaseOID != next.Lifecycle.ExpectedBaseOID || current.Lifecycle.HeadOID != next.Lifecycle.HeadOID {
			return fail(CheckpointChainInvalid, "merge authorization drifted from published PR identity")
		}
	}
	return nil
}

func validateCheckpointPayload(checkpoint PhaseCheckpointV1) error {
	operation := map[CheckpointKind]contextcapsule.OperationKind{
		CheckpointDesignAccepted:          contextcapsule.OperationDesignReview,
		CheckpointImplementationConverged: contextcapsule.OperationImplementationReview,
		CheckpointAcceptancePassed:        contextcapsule.OperationAcceptance,
		CheckpointFinalReviewClean:        contextcapsule.OperationFinalReview,
		CheckpointPRPublished:             contextcapsule.OperationPRPublication,
		CheckpointMergeAuthorized:         contextcapsule.OperationMergeAuthorization,
		CheckpointPostMergeAccepted:       contextcapsule.OperationPostMergeAcceptance,
	}
	want, ok := operation[checkpoint.Kind]
	validOperation := ok && checkpoint.Operation == want
	if checkpoint.Kind == CheckpointImplementationConverged && checkpoint.Operation == contextcapsule.OperationImplementation {
		validOperation = true
	}
	if !validOperation {
		return fail(CheckpointChainInvalid, "checkpoint kind/operation pair is invalid")
	}
	if !validText(checkpoint.Repository, 512) || !validText(checkpoint.Verdict, 512) || !validText(checkpoint.ControllerPolicyIdentity, 512) || !validText(checkpoint.ControllerEventIdentity, 512) {
		return fail(CheckpointChainInvalid, "checkpoint text identity is invalid")
	}
	for name, value := range map[string]string{"capsule_file_sha256": checkpoint.CapsuleFileSHA256, "capsule_sha256": checkpoint.CapsuleSHA256} {
		if !validSHA256(value) {
			return fail(CheckpointChainInvalid, "%s is invalid", name)
		}
	}
	if !validOID(checkpoint.CandidateSHA) {
		return fail(CheckpointChainInvalid, "candidate_sha is invalid")
	}
	if checkpoint.Sequence == 0 && checkpoint.PredecessorCheckpointSHA256 != "" || checkpoint.Sequence > 0 && !validSHA256(checkpoint.PredecessorCheckpointSHA256) {
		return fail(CheckpointChainInvalid, "predecessor checkpoint binding is invalid")
	}
	if len(checkpoint.Evidence) == 0 || len(checkpoint.Evidence) > 64 {
		return fail(CheckpointChainInvalid, "checkpoint evidence count is invalid")
	}
	previous := ""
	for _, evidence := range checkpoint.Evidence {
		if !validText(evidence.Ref, 2048) || !validSHA256(evidence.SHA256) || evidence.Ref <= previous {
			return fail(CheckpointChainInvalid, "checkpoint evidence must be sorted, unique, and hashed")
		}
		previous = evidence.Ref
	}
	if checkpoint.Kind == CheckpointDesignAccepted {
		if !validSHA256(checkpoint.SemanticRegistrySHA256) || !validSHA256(checkpoint.NextStageGrantSHA256) {
			return fail(CheckpointChainInvalid, "DESIGN_ACCEPTED must bind registry and B grant digests")
		}
	}
	if checkpoint.Kind == CheckpointImplementationConverged {
		if !validSHA256(checkpoint.ReviewScopeTipSHA256) || !validSHA256(checkpoint.NextStageGrantSHA256) || checkpoint.Verdict != "IMPLEMENTATION_CONVERGED_C0_M0" || checkpoint.ReviewCritical != 0 || checkpoint.ReviewMajor != 0 || len(checkpoint.ReviewedBlockingScopeIDs) == 0 || validateIDs(checkpoint.ReviewedBlockingScopeIDs) != nil {
			return fail(CheckpointChainInvalid, "IMPLEMENTATION_CONVERGED must bind review tip and C grant digests")
		}
	}
	if checkpoint.Kind == CheckpointFinalReviewClean {
		if !validSHA256(checkpoint.ReviewScopeTipSHA256) || checkpoint.Verdict != "FINAL_REVIEW_CLEAN_C0_M0" || checkpoint.ReviewCritical != 0 || checkpoint.ReviewMajor != 0 || len(checkpoint.ReviewedBlockingScopeIDs) == 0 || validateIDs(checkpoint.ReviewedBlockingScopeIDs) != nil {
			return fail(FinalReviewInvalidated, "FINAL_REVIEW_CLEAN must bind an exact 0C/0M review tip and blocker scope")
		}
	}
	if checkpoint.Kind == CheckpointPRPublished || checkpoint.Kind == CheckpointMergeAuthorized {
		if err := validateLifecycle(checkpoint.Kind, checkpoint.Lifecycle, checkpoint.CandidateSHA); err != nil {
			return err
		}
	} else if checkpoint.Lifecycle != nil {
		return fail(CheckpointChainInvalid, "lifecycle binding is not valid for %s", checkpoint.Kind)
	}
	return nil
}

func checkpointDigest(checkpoint PhaseCheckpointV1) (string, error) {
	checkpoint.CheckpointSHA256 = ""
	type alias PhaseCheckpointV1
	return digestJSON(alias(checkpoint))
}

// NextStageGrantV1 fixes maxima and non-removable floors for B or C.
type NextStageGrantV1 struct {
	Kind                     string                            `json:"kind"`
	Stage                    contextcapsule.Stage              `json:"stage"`
	ParentCheckpointSHA256   string                            `json:"parent_checkpoint_sha256"`
	BaseSHA                  string                            `json:"base_sha"`
	SemanticRegistrySHA256   string                            `json:"semantic_registry_sha256"`
	AllowedOperations        []contextcapsule.OperationKind    `json:"allowed_operations"`
	ObservationScopeIDs      []string                          `json:"observation_scope_ids"`
	BlockingScopeIDs         []string                          `json:"blocking_scope_ids"`
	MutationScopeIDs         []string                          `json:"mutation_scope_ids"`
	AuthorizedFindingIDs     []string                          `json:"authorized_finding_ids"`
	AuthorizedInvariantIDs   []string                          `json:"authorized_invariant_ids"`
	AllowedPaths             []string                          `json:"allowed_paths"`
	ReviewProfile            contextcapsule.ReviewProfile      `json:"review_profile"`
	ExecutionBounds          *contextcapsule.ExecutionBoundsV1 `json:"execution_bounds,omitempty"`
	RequiredBlockingScopeIDs []string                          `json:"required_blocking_scope_ids"`
	RequiredInvariantIDs     []string                          `json:"required_invariant_ids"`
	RequiredOperations       []contextcapsule.OperationKind    `json:"required_operations"`
	RequiredFinalReviewIDs   []string                          `json:"required_final_review_ids"`
	GrantSHA256              string                            `json:"grant_sha256"`
}

// SealNextStageGrantV1 validates and hashes one grant.
func SealNextStageGrantV1(grant NextStageGrantV1) (NextStageGrantV1, error) {
	grant.GrantSHA256 = ""
	if err := validateGrantPayload(grant); err != nil {
		return NextStageGrantV1{}, err
	}
	digest, err := grantDigest(grant)
	if err != nil {
		return NextStageGrantV1{}, err
	}
	grant.GrantSHA256 = digest
	return grant, nil
}

// ValidateNextStageGrantV1 validates a strict grant and its digest.
func ValidateNextStageGrantV1(grant NextStageGrantV1) error {
	if err := validateGrantPayload(grant); err != nil {
		return err
	}
	digest, err := grantDigest(grant)
	if err != nil {
		return err
	}
	if grant.GrantSHA256 != digest {
		return fail(CapsuleLineageInvalid, "next-stage grant digest mismatch")
	}
	return nil
}

// SealCheckpointWithNextStageGrantV1 seals the non-cyclic checkpoint/grant
// pair. The grant hashes the immutable checkpoint core (all checkpoint fields
// except the child grant digest); the final checkpoint then hashes the sealed
// grant digest. Thus both records are exact and neither can be rebound.
func SealCheckpointWithNextStageGrantV1(checkpoint PhaseCheckpointV1, grant NextStageGrantV1) (PhaseCheckpointV1, NextStageGrantV1, error) {
	if checkpoint.Kind != CheckpointDesignAccepted && checkpoint.Kind != CheckpointImplementationConverged {
		return PhaseCheckpointV1{}, NextStageGrantV1{}, fail(CheckpointChainInvalid, "only a stage-boundary checkpoint can seal a next-stage grant")
	}
	checkpoint.CheckpointSHA256 = ""
	checkpoint.NextStageGrantSHA256 = ""
	parent, err := checkpointGrantParentDigest(checkpoint)
	if err != nil {
		return PhaseCheckpointV1{}, NextStageGrantV1{}, err
	}
	grant.ParentCheckpointSHA256 = parent
	grant.GrantSHA256 = ""
	grant, err = SealNextStageGrantV1(grant)
	if err != nil {
		return PhaseCheckpointV1{}, NextStageGrantV1{}, err
	}
	checkpoint.NextStageGrantSHA256 = grant.GrantSHA256
	checkpoint, err = SealPhaseCheckpointV1(checkpoint)
	if err != nil {
		return PhaseCheckpointV1{}, NextStageGrantV1{}, err
	}
	return checkpoint, grant, nil
}

func checkpointGrantParentDigest(checkpoint PhaseCheckpointV1) (string, error) {
	checkpoint.CheckpointSHA256 = ""
	checkpoint.NextStageGrantSHA256 = ""
	type alias PhaseCheckpointV1
	return digestJSON(alias(checkpoint))
}

func validateGrantPayload(grant NextStageGrantV1) error {
	if grant.Kind != "NextStageGrantV1" || !validSHA256(grant.ParentCheckpointSHA256) || !validOID(grant.BaseSHA) || !validSHA256(grant.SemanticRegistrySHA256) {
		return fail(CapsuleLineageInvalid, "grant identity or digest binding is invalid")
	}
	if grant.Stage != contextcapsule.StageBImplementation && grant.Stage != contextcapsule.StageCAcceptanceMerge {
		return fail(CapsuleLineageInvalid, "grant stage is invalid")
	}
	for name, values := range map[string][]string{
		"observation_scope_ids": grant.ObservationScopeIDs, "blocking_scope_ids": grant.BlockingScopeIDs,
		"mutation_scope_ids": grant.MutationScopeIDs, "authorized_finding_ids": grant.AuthorizedFindingIDs,
		"authorized_invariant_ids": grant.AuthorizedInvariantIDs, "required_blocking_scope_ids": grant.RequiredBlockingScopeIDs,
		"required_invariant_ids": grant.RequiredInvariantIDs, "required_final_review_ids": grant.RequiredFinalReviewIDs,
	} {
		if err := validateIDs(values); err != nil {
			return fail(CapsuleLineageInvalid, "%s: %v", name, err)
		}
	}
	if err := validateOperations(grant.AllowedOperations); err != nil {
		return err
	}
	if err := validateOperations(grant.RequiredOperations); err != nil {
		return err
	}
	if !stringSubset(grant.BlockingScopeIDs, grant.ObservationScopeIDs) || !stringSubset(grant.MutationScopeIDs, grant.BlockingScopeIDs) || !stringSubset(grant.RequiredBlockingScopeIDs, grant.BlockingScopeIDs) || !stringSubset(grant.RequiredInvariantIDs, grant.AuthorizedInvariantIDs) || !operationSubset(grant.RequiredOperations, grant.AllowedOperations) || !stringSubset(grant.RequiredFinalReviewIDs, grant.AuthorizedInvariantIDs) || !stringSubset(grant.RequiredFinalReviewIDs, grant.BlockingScopeIDs) {
		return fail(CapsuleLineageInvalid, "grant scope or mandatory floor exceeds its maximum")
	}
	if len(grant.RequiredBlockingScopeIDs) == 0 || len(grant.RequiredInvariantIDs) == 0 || len(grant.RequiredOperations) == 0 || len(grant.RequiredFinalReviewIDs) == 0 {
		return fail(CapsuleLineageInvalid, "grant mandatory floors must not be empty")
	}
	if err := validatePaths(grant.AllowedPaths, false); err != nil {
		return fail(CapsuleLineageInvalid, "grant allowed_paths: %v", err)
	}
	if grant.Stage == contextcapsule.StageBImplementation {
		if !operationSubset(grant.AllowedOperations, []contextcapsule.OperationKind{contextcapsule.OperationImplementation, contextcapsule.OperationImplementationReview}) {
			return fail(CapsuleLineageInvalid, "B grant contains an operation outside B")
		}
		if grant.ReviewProfile != contextcapsule.ReviewProfileInitialImplementation && grant.ReviewProfile != contextcapsule.ReviewProfileCorrection || grant.ExecutionBounds == nil {
			return fail(CapsuleLineageInvalid, "B grant review profile or bounds are invalid")
		}
		if err := contextcapsule.ValidateExecutionBoundsV1(*grant.ExecutionBounds); err != nil {
			return fail(ExecutionBoundsInvalid, "%v", err)
		}
		if grant.ReviewProfile == contextcapsule.ReviewProfileCorrection && len(grant.AuthorizedFindingIDs) == 0 || grant.ReviewProfile == contextcapsule.ReviewProfileInitialImplementation && len(grant.AuthorizedFindingIDs) != 0 {
			return fail(CapsuleLineageInvalid, "B grant finding allowlist does not match review profile")
		}
	} else {
		if !operationSubset(grant.AllowedOperations, []contextcapsule.OperationKind{contextcapsule.OperationAcceptance, contextcapsule.OperationFinalReview, contextcapsule.OperationMergeAuthorization, contextcapsule.OperationPostMergeAcceptance, contextcapsule.OperationPRPublication}) {
			return fail(CapsuleLineageInvalid, "C grant contains an operation outside C")
		}
		if grant.ReviewProfile != contextcapsule.ReviewProfileNone || grant.ExecutionBounds != nil || len(grant.MutationScopeIDs) != 0 || len(grant.AuthorizedFindingIDs) != 0 {
			return fail(CapsuleLineageInvalid, "C grant must be read-only with no execution bounds")
		}
	}
	return nil
}

func grantDigest(grant NextStageGrantV1) (string, error) {
	grant.GrantSHA256 = ""
	type alias NextStageGrantV1
	return digestJSON(alias(grant))
}

func checkpointAtOrAfter(actual, threshold CheckpointKind) bool {
	order := map[CheckpointKind]int{
		CheckpointDesignAccepted: 0, CheckpointImplementationConverged: 1,
		CheckpointAcceptancePassed: 2, CheckpointFinalReviewClean: 3,
		CheckpointPRPublished: 4, CheckpointMergeAuthorized: 5, CheckpointPostMergeAccepted: 6,
	}
	return order[actual] >= order[threshold]
}

// DerivationV3 is the complete evidence needed to validate A->B or B->C.
type DerivationV3 struct {
	ParentCapsuleFileSHA256 string                 `json:"parent_capsule_file_sha256"`
	ParentCapsule           contextcapsule.Capsule `json:"parent_capsule"`
	Checkpoint              PhaseCheckpointV1      `json:"checkpoint"`
	Grant                   NextStageGrantV1       `json:"grant"`
	UpstreamGrant           *NextStageGrantV1      `json:"upstream_grant,omitempty"`
	ChildCapsule            contextcapsule.Capsule `json:"child_capsule"`
}

// ValidateDerivationV3 validates exact controller evidence, set narrowing,
// numeric tightening, and mandatory-floor preservation.
func ValidateDerivationV3(input DerivationV3) error {
	if !validSHA256(input.ParentCapsuleFileSHA256) || input.ParentCapsule.PolicyVersion != contextcapsule.PolicyVersionV3 || input.ChildCapsule.PolicyVersion != contextcapsule.PolicyVersionV3 || input.ParentCapsule.PhaseAuthority == nil || input.ChildCapsule.PhaseAuthority == nil {
		return fail(CapsuleLineageInvalid, "derivation requires exact V3 parent and child capsule bindings")
	}
	if err := contextcapsule.ValidatePhaseAuthorityV3(*input.ParentCapsule.PhaseAuthority); err != nil {
		return fail(CapsuleLineageInvalid, "parent phase authority: %v", err)
	}
	if err := contextcapsule.ValidatePhaseAuthorityV3(*input.ChildCapsule.PhaseAuthority); err != nil {
		return fail(CapsuleLineageInvalid, "child phase authority: %v", err)
	}
	if err := ValidatePhaseCheckpointV1(input.Checkpoint); err != nil {
		return err
	}
	if err := ValidateNextStageGrantV1(input.Grant); err != nil {
		return err
	}
	parentStage := input.ParentCapsule.PhaseAuthority.Stage
	childStage := input.ChildCapsule.PhaseAuthority.Stage
	wantCheckpoint := CheckpointDesignAccepted
	if parentStage == contextcapsule.StageADesign && childStage == contextcapsule.StageBImplementation {
		wantCheckpoint = CheckpointDesignAccepted
	} else if parentStage == contextcapsule.StageBImplementation && childStage == contextcapsule.StageCAcceptanceMerge {
		wantCheckpoint = CheckpointImplementationConverged
	} else {
		return fail(CapsuleLineageInvalid, "only A_DESIGN->B_IMPLEMENTATION and B_IMPLEMENTATION->C_ACCEPTANCE_MERGE are valid")
	}
	if input.Checkpoint.Kind != wantCheckpoint || input.Checkpoint.CapsuleFileSHA256 != input.ParentCapsuleFileSHA256 || input.Checkpoint.CapsuleSHA256 != input.ParentCapsule.CapsuleSHA256 || input.Checkpoint.CandidateSHA != input.ChildCapsule.BaseSHA || input.Checkpoint.NextStageGrantSHA256 != input.Grant.GrantSHA256 {
		return fail(CapsuleLineageInvalid, "checkpoint does not bind parent, exact child base, and grant")
	}
	if input.Checkpoint.Repository != input.ParentCapsule.Repository || input.ChildCapsule.Repository != input.ParentCapsule.Repository {
		return fail(CapsuleLineageInvalid, "repository identity changed across derivation")
	}
	if parentStage == contextcapsule.StageADesign && (input.Checkpoint.SemanticRegistrySHA256 != input.ParentCapsule.PhaseAuthority.SemanticRegistrySHA256 || input.Checkpoint.SemanticRegistrySHA256 != input.Grant.SemanticRegistrySHA256) {
		return fail(CapsuleLineageInvalid, "A registry binding changed across design acceptance")
	}
	grantParent, err := checkpointGrantParentDigest(input.Checkpoint)
	if err != nil {
		return err
	}
	if input.Grant.Stage != childStage || input.Grant.ParentCheckpointSHA256 != grantParent || input.Grant.BaseSHA != input.ChildCapsule.BaseSHA || input.Grant.SemanticRegistrySHA256 != input.ChildCapsule.PhaseAuthority.SemanticRegistrySHA256 {
		return fail(CapsuleLineageInvalid, "grant does not bind checkpoint and child")
	}
	parent := input.ChildCapsule.PhaseAuthority.Parent
	if parent == nil || parent.CapsuleFileSHA256 != input.ParentCapsuleFileSHA256 || parent.CapsuleSHA256 != input.ParentCapsule.CapsuleSHA256 || parent.Stage != parentStage || parent.CheckpointSHA256 != input.Checkpoint.CheckpointSHA256 || parent.CandidateSHA != input.Checkpoint.CandidateSHA || parent.GrantSHA256 != input.Grant.GrantSHA256 {
		return fail(CapsuleLineageInvalid, "child parent binding is incomplete or mismatched")
	}
	if input.ChildCapsule.BaseSHA != input.Checkpoint.CandidateSHA || input.ChildCapsule.PhaseAuthority.ReviewProfile != input.Grant.ReviewProfile || !operationSubset(input.ChildCapsule.PhaseAuthority.AllowedOperations, input.Grant.AllowedOperations) || !stringSubset(input.ChildCapsule.PhaseAuthority.ObservationScopeIDs, input.Grant.ObservationScopeIDs) || !stringSubset(input.ChildCapsule.PhaseAuthority.BlockingScopeIDs, input.Grant.BlockingScopeIDs) || !stringSubset(input.ChildCapsule.PhaseAuthority.MutationScopeIDs, input.Grant.MutationScopeIDs) || !stringSubset(input.ChildCapsule.PhaseAuthority.AuthorizedFindingIDs, input.Grant.AuthorizedFindingIDs) || !stringSubset(input.ChildCapsule.PhaseAuthority.AuthorizedInvariantIDs, input.Grant.AuthorizedInvariantIDs) || !pathSubset(input.ChildCapsule.PhaseAuthority.AllowedPaths, input.Grant.AllowedPaths) {
		return fail(CapsuleLineageInvalid, "child authority exceeds grant maxima")
	}
	if !stringSubset(input.Grant.RequiredBlockingScopeIDs, input.ChildCapsule.PhaseAuthority.BlockingScopeIDs) || !stringSubset(input.Grant.RequiredInvariantIDs, input.ChildCapsule.PhaseAuthority.AuthorizedInvariantIDs) || !operationSubset(input.Grant.RequiredOperations, input.ChildCapsule.PhaseAuthority.AllowedOperations) || !stringSubset(input.Grant.RequiredFinalReviewIDs, input.ChildCapsule.PhaseAuthority.AuthorizedInvariantIDs) || !stringSubset(input.Grant.RequiredFinalReviewIDs, input.ChildCapsule.PhaseAuthority.BlockingScopeIDs) {
		return fail(CapsuleLineageInvalid, "child removed a mandatory grant floor")
	}
	if input.Grant.ExecutionBounds != nil {
		if input.ChildCapsule.PhaseAuthority.ExecutionBounds == nil || !boundsTighten(*input.ChildCapsule.PhaseAuthority.ExecutionBounds, *input.Grant.ExecutionBounds) {
			return fail(CapsuleLineageInvalid, "child execution bounds widen grant ceilings")
		}
	}
	if childStage == contextcapsule.StageCAcceptanceMerge {
		if input.UpstreamGrant == nil || ValidateNextStageGrantV1(*input.UpstreamGrant) != nil || input.ParentCapsule.PhaseAuthority.Parent == nil || input.ParentCapsule.PhaseAuthority.Parent.GrantSHA256 != input.UpstreamGrant.GrantSHA256 || !stringSubset(input.UpstreamGrant.RequiredInvariantIDs, input.Grant.RequiredInvariantIDs) || !stringSubset(input.UpstreamGrant.RequiredFinalReviewIDs, input.Grant.RequiredFinalReviewIDs) {
			return fail(CapsuleLineageInvalid, "B->C grant dropped a transitive A/B mandatory floor")
		}
	}
	return nil
}

// ValidateCapsuleUsageV3 enforces stage membership and read-only boundaries.
// A mutation operation must additionally present a validated lease digest.
func ValidateCapsuleUsageV3(capsule contextcapsule.Capsule, operation contextcapsule.OperationKind, mutation bool, leaseSHA256 string) error {
	if err := validateCapsuleUsageV3(capsule, operation, mutation); err != nil {
		return err
	}
	if operation == contextcapsule.OperationImplementationReview && mutation {
		return fail(MutationScopeViolation, "implementation-review mutation requires the durable controller lease tip")
	}
	if leaseSHA256 != "" {
		return fail(MutationScopeViolation, "caller-supplied lease digests are not admission proof")
	}
	return nil
}

func validateCapsuleUsageV3(capsule contextcapsule.Capsule, operation contextcapsule.OperationKind, mutation bool) error {
	if capsule.PolicyVersion != contextcapsule.PolicyVersionV3 || capsule.PhaseAuthority == nil {
		return fail(CapsuleUsageInvalid, "context-capsule-v3 phase authority is required")
	}
	if err := contextcapsule.ValidatePhaseAuthorityV3(*capsule.PhaseAuthority); err != nil {
		return fail(CapsuleStageInvalid, "%v", err)
	}
	if !operationSubset([]contextcapsule.OperationKind{operation}, capsule.PhaseAuthority.AllowedOperations) {
		return fail(CapsuleUsageInvalid, "operation %s is outside stage %s", operation, capsule.PhaseAuthority.Stage)
	}
	if operation == contextcapsule.OperationImplementation && !mutation {
		return fail(CapsuleUsageInvalid, "implementation must declare repository mutation")
	}
	if capsule.PhaseAuthority.Stage == contextcapsule.StageCAcceptanceMerge && mutation {
		return fail(CapsuleUsageInvalid, "C_ACCEPTANCE_MERGE is repository read-only")
	}
	if operation == contextcapsule.OperationDesignReview || operation == contextcapsule.OperationFinalReview || operation == contextcapsule.OperationAcceptance || operation == contextcapsule.OperationPRPublication || operation == contextcapsule.OperationMergeAuthorization || operation == contextcapsule.OperationPostMergeAcceptance {
		if mutation {
			return fail(CapsuleUsageInvalid, "%s is read-only", operation)
		}
	}
	return nil
}

// CandidateProofV1 is the independently computed base...candidate diff proof.
type CandidateProofV1 struct {
	Repository        string   `json:"repository"`
	BaseSHA           string   `json:"base_sha"`
	CandidateSHA      string   `json:"candidate_sha"`
	ChangedPaths      []string `json:"changed_paths"`
	ChangedPathSHA256 string   `json:"changed_path_sha256"`
	ChangedFiles      int      `json:"changed_files"`
	ChangedBytes      int64    `json:"changed_bytes"`
	WorkspaceClean    bool     `json:"workspace_clean"`
}

// ValidateCandidateV1 proves exact commit identity and ancestry with
// replacement objects disabled, then enforces path and diff ceilings.
func ValidateCandidateV1(repository string, capsule contextcapsule.Capsule, candidateSHA string) (CandidateProofV1, error) {
	proof := CandidateProofV1{Repository: capsule.Repository, BaseSHA: capsule.BaseSHA, CandidateSHA: candidateSHA}
	if capsule.PolicyVersion != contextcapsule.PolicyVersionV3 || capsule.PhaseAuthority == nil {
		return proof, fail(CandidateHeadInvalid, "V3 capsule phase authority is required")
	}
	if !validOID(candidateSHA) {
		return proof, fail(CandidateHeadInvalid, "candidate must be an exact lowercase object ID")
	}
	root, err := canonicalRepository(repository)
	if err != nil {
		return proof, fail(CandidateHeadInvalid, "%v", err)
	}
	if !repositoryIdentityMatches(root, capsule.Repository) {
		return proof, fail(CandidateHeadInvalid, "repository identity does not match capsule")
	}
	resolved, err := gitText(root, "rev-parse", "--verify", candidateSHA+"^{commit}")
	if err != nil || resolved != candidateSHA {
		return proof, fail(CandidateHeadInvalid, "candidate is not the exact replacement-resistant commit")
	}
	if err := gitRun(root, "merge-base", "--is-ancestor", capsule.BaseSHA, candidateSHA); err != nil {
		return proof, fail(CandidateHeadInvalid, "capsule base is not an ancestor of candidate")
	}
	status, err := gitBytes(root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil || len(status) != 0 {
		return proof, fail(CandidateHeadInvalid, "governed workspace is not clean")
	}
	if capsule.PhaseAuthority.Stage == contextcapsule.StageCAcceptanceMerge {
		head, headErr := gitText(root, "rev-parse", "--verify", "HEAD^{commit}")
		if headErr != nil || head != candidateSHA {
			return proof, fail(FinalReviewInvalidated, "C checks require the exact candidate checkout")
		}
	}
	submodules, err := gitText(root, "submodule", "status", "--recursive")
	if err != nil {
		return proof, fail(CandidateHeadInvalid, "cannot prove safe submodule state")
	}
	for _, line := range strings.Split(submodules, "\n") {
		if line != "" && strings.ContainsRune("-+U", rune(line[0])) {
			return proof, fail(CandidateHeadInvalid, "unsafe submodule state")
		}
	}
	pathsRaw, err := gitBytes(root, "diff", "--no-renames", "--name-only", "-z", "--diff-filter=ACDMRTUXB", capsule.BaseSHA+"..."+candidateSHA)
	if err != nil {
		return proof, fail(CandidateHeadInvalid, "compute candidate paths: %v", err)
	}
	paths, err := splitNUL(pathsRaw)
	if err != nil {
		return proof, fail(CandidateHeadInvalid, "candidate path proof is malformed")
	}
	sort.Strings(paths)
	for index, path := range paths {
		if index > 0 && path == paths[index-1] || !pathAllowed(path, capsule.PhaseAuthority.AllowedPaths) {
			return proof, fail(MutationScopeViolation, "candidate path %q is duplicate or outside authority", path)
		}
	}
	if capsule.PhaseAuthority.Stage == contextcapsule.StageCAcceptanceMerge && (candidateSHA != capsule.BaseSHA || len(paths) != 0) {
		return proof, fail(FinalReviewInvalidated, "C requires the exact converged base HEAD with no content delta")
	}
	changedBytes := int64(0)
	for _, path := range paths {
		baseBytes, baseErr := regularTreeObjectSize(root, capsule.BaseSHA, path)
		candidateBytes, candidateErr := regularTreeObjectSize(root, candidateSHA, path)
		if baseErr != nil || candidateErr != nil {
			return proof, fail(MutationScopeViolation, "changed path %q is not a bounded regular file: %v", path, errors.Join(baseErr, candidateErr))
		}
		changedBytes += baseBytes + candidateBytes
	}
	if bounds := capsule.PhaseAuthority.ExecutionBounds; bounds != nil {
		if len(paths) > bounds.MaxChangedFiles || changedBytes > bounds.MaxChangedBytes {
			return proof, fail(MutationScopeViolation, "candidate diff exceeds changed-file or byte ceiling")
		}
	}
	digest := sha256.Sum256(pathsRawForDigest(paths))
	proof.ChangedPaths = paths
	proof.ChangedPathSHA256 = hex.EncodeToString(digest[:])
	proof.ChangedFiles = len(paths)
	proof.ChangedBytes = changedBytes
	proof.WorkspaceClean = true
	return proof, nil
}

// ValidateMutationCandidateV1 independently proves the exact pre-fix...result
// commit range named by a persisted lease. Caller-provided diff summaries are
// never accepted as receipt evidence.
func ValidateMutationCandidateV1(repository string, capsule contextcapsule.Capsule, lease MutationLeaseV1, candidateSHA string) (CandidateProofV1, error) {
	proof := CandidateProofV1{Repository: capsule.Repository, BaseSHA: lease.PreFixHEAD, CandidateSHA: candidateSHA}
	if digest, err := leaseDigest(lease); err != nil || digest != lease.LeaseSHA256 || lease.CapsuleSHA256 != capsule.CapsuleSHA256 {
		return proof, fail(MutationScopeViolation, "persisted mutation lease is invalid for the capsule")
	}
	if _, err := ValidateCandidateV1(repository, capsule, candidateSHA); err != nil {
		return proof, err
	}
	root, err := canonicalRepository(repository)
	if err != nil {
		return proof, fail(CandidateHeadInvalid, "%v", err)
	}
	head, err := gitText(root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || head != candidateSHA {
		return proof, fail(CandidateHeadInvalid, "mutation result is not the exact clean repository HEAD")
	}
	resolved, err := gitText(root, "rev-parse", "--verify", lease.PreFixHEAD+"^{commit}")
	if err != nil || resolved != lease.PreFixHEAD || gitRun(root, "merge-base", "--is-ancestor", lease.PreFixHEAD, candidateSHA) != nil {
		return proof, fail(CandidateHeadInvalid, "lease pre-fix HEAD is not an exact ancestor of the result")
	}
	pathsRaw, err := gitBytes(root, "diff", "--no-renames", "--name-only", "-z", "--diff-filter=ACDMRTUXB", lease.PreFixHEAD+".."+candidateSHA)
	if err != nil {
		return proof, fail(CandidateHeadInvalid, "compute mutation paths: %v", err)
	}
	paths, err := splitNUL(pathsRaw)
	if err != nil {
		return proof, fail(CandidateHeadInvalid, "mutation path proof is malformed")
	}
	sort.Strings(paths)
	changedBytes := int64(0)
	for index, path := range paths {
		if index > 0 && path == paths[index-1] || !pathAllowed(path, lease.AllowedPaths) {
			return proof, fail(MutationScopeViolation, "mutation path %q is duplicate or outside the persisted lease", path)
		}
		baseBytes, baseErr := regularTreeObjectSize(root, lease.PreFixHEAD, path)
		candidateBytes, candidateErr := regularTreeObjectSize(root, candidateSHA, path)
		if baseErr != nil || candidateErr != nil {
			return proof, fail(MutationScopeViolation, "mutation path %q is not a bounded regular file: %v", path, errors.Join(baseErr, candidateErr))
		}
		changedBytes += baseBytes + candidateBytes
	}
	if len(paths) < 1 || len(paths) > lease.MaxChangedFiles || changedBytes > lease.MaxChangedBytes {
		return proof, fail(MutationScopeViolation, "mutation diff exceeds the persisted lease ceilings")
	}
	digest := sha256.Sum256(pathsRawForDigest(paths))
	proof.ChangedPaths = paths
	proof.ChangedPathSHA256 = hex.EncodeToString(digest[:])
	proof.ChangedFiles = len(paths)
	proof.ChangedBytes = changedBytes
	proof.WorkspaceClean = true
	return proof, nil
}

func regularTreeObjectSize(repository, commit, path string) (int64, error) {
	record, err := gitBytes(repository, "ls-tree", "-z", commit, "--", path)
	if err != nil {
		return 0, err
	}
	if len(record) == 0 {
		return 0, nil
	}
	if record[len(record)-1] != 0 || bytes.Count(record, []byte{0}) != 1 {
		return 0, errors.New("ambiguous tree entry")
	}
	line := strings.TrimSuffix(string(record), "\x00")
	tab := strings.IndexByte(line, '\t')
	if tab < 0 || line[tab+1:] != path {
		return 0, errors.New("tree path mismatch")
	}
	fields := strings.Fields(line[:tab])
	if len(fields) != 3 || fields[1] != "blob" || fields[0] != "100644" && fields[0] != "100755" {
		return 0, errors.New("symlink, submodule, or non-regular mode")
	}
	size, err := gitText(repository, "cat-file", "-s", fields[2])
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(size, 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("invalid object size")
	}
	return value, nil
}

// FindingSeverity is the only severity allowed to block B convergence.
type FindingSeverity string

const (
	SeverityCritical FindingSeverity = "CRITICAL"
	SeverityMajor    FindingSeverity = "MAJOR"
)

// ActiveFindingV1 maps a reviewer finding to an immutable registry rule.
type ActiveFindingV1 struct {
	FindingID string              `json:"finding_id"`
	RuleID    string              `json:"rule_id"`
	Severity  FindingSeverity     `json:"severity"`
	Evidence  []EvidenceBindingV1 `json:"evidence"`
}

// FindingEvidenceV1 is a content-addressed controller evidence record. Review
// reports name these records by Ref, but cannot assert their evidence class,
// validator, invariant mapping, or model-judgment status themselves.
type FindingEvidenceV1 struct {
	Kind                   string       `json:"kind"`
	Ref                    string       `json:"ref"`
	CapsuleSHA256          string       `json:"capsule_sha256"`
	SemanticRegistrySHA256 string       `json:"semantic_registry_sha256"`
	CandidateSHA           string       `json:"candidate_sha"`
	FindingID              string       `json:"finding_id"`
	RuleID                 string       `json:"rule_id"`
	RegisteredKind         RegistryKind `json:"registered_kind"`
	EvidenceClass          string       `json:"evidence_class"`
	ValidatorIdentity      string       `json:"validator_identity,omitempty"`
	ModelJudgmentUsed      bool         `json:"model_judgment_used"`
	Outcome                string       `json:"outcome"`
	ArtifactSHA256         string       `json:"artifact_sha256"`
	EvidenceSHA256         string       `json:"evidence_sha256"`
}

// FindingEvidenceRequestV1 is the complete untrusted input accepted by the
// durable controller. Artifact hashes and registry/validator metadata are
// deliberately absent: the controller derives them after resolving Ref.
type FindingEvidenceRequestV1 struct {
	Ref          string `json:"ref"`
	CandidateSHA string `json:"candidate_sha"`
	FindingID    string `json:"finding_id"`
	RuleID       string `json:"rule_id"`
}

// FindingEvidenceResolverV1 is installed by the controller composition, not
// selected by a review report. ResolveArtifactV1 must return the exact bounded
// immutable artifact bytes for ref. ConfirmViolationV1 must run the named
// registry validator against the exact repository candidate and return nil
// only when that deterministic validator confirms a violation.
type FindingEvidenceResolverV1 interface {
	ResolveArtifactV1(repository, candidateSHA, ref string) ([]byte, error)
	ConfirmViolationV1(repository, candidateSHA string, rule SemanticRuleV1, artifact []byte) error
}

// SealFindingEvidenceV1 binds one exact violation record to the immutable
// capsule, registry rule, candidate, and underlying artifact digest.
func SealFindingEvidenceV1(capsule contextcapsule.Capsule, registry SemanticAuthorityRegistryV1, evidence FindingEvidenceV1) (FindingEvidenceV1, error) {
	evidence.EvidenceSHA256 = ""
	if err := validateFindingEvidenceV1(capsule, registry, evidence); err != nil {
		return FindingEvidenceV1{}, err
	}
	digest, err := findingEvidenceDigest(evidence)
	if err != nil {
		return FindingEvidenceV1{}, err
	}
	evidence.EvidenceSHA256 = digest
	return evidence, nil
}

// ValidateFindingEvidenceV1 rejects evidence whose record or registry-bound
// semantics differ from the exact content-addressed controller artifact.
func ValidateFindingEvidenceV1(capsule contextcapsule.Capsule, registry SemanticAuthorityRegistryV1, evidence FindingEvidenceV1) error {
	if err := validateFindingEvidenceV1(capsule, registry, evidence); err != nil {
		return err
	}
	digest, err := findingEvidenceDigest(evidence)
	if err != nil || digest != evidence.EvidenceSHA256 {
		return fail(ReviewChainInvalid, "finding evidence digest mismatch")
	}
	return nil
}

func validateFindingEvidenceV1(capsule contextcapsule.Capsule, registry SemanticAuthorityRegistryV1, evidence FindingEvidenceV1) error {
	if err := ValidateSemanticAuthorityRegistryV1(registry); err != nil {
		return err
	}
	if capsule.PolicyVersion != contextcapsule.PolicyVersionV3 || capsule.PhaseAuthority == nil || evidence.Kind != "FindingEvidenceV1" || !validText(evidence.Ref, 2048) || !validOID(evidence.CandidateSHA) || !validSHA256(evidence.ArtifactSHA256) {
		return fail(ReviewChainInvalid, "finding evidence identity is invalid")
	}
	if evidence.CapsuleSHA256 != capsule.CapsuleSHA256 || evidence.SemanticRegistrySHA256 != registry.RegistrySHA256 || registry.RegistrySHA256 != capsule.PhaseAuthority.SemanticRegistrySHA256 || !validID(evidence.FindingID) || !validID(evidence.RuleID) || evidence.Outcome != "VIOLATION_CONFIRMED" {
		return fail(ReviewChainInvalid, "finding evidence does not bind the exact capsule, registry, finding, and violation outcome")
	}
	rule, ok := registryMap(registry)[evidence.RuleID]
	if !ok || rule.Kind != RegistryInvariant && rule.Kind != RegistryKnownFinding {
		return fail(ScopeExpansionRequired, "finding evidence has no blocking registry mapping")
	}
	if evidence.RegisteredKind != rule.Kind || evidence.EvidenceClass != rule.EvidenceClass || evidence.ValidatorIdentity != rule.ValidatorIdentity {
		return fail(ReviewChainInvalid, "finding evidence differs from the registered kind, evidence class, or validator identity")
	}
	if evidence.ModelJudgmentUsed && (!rule.ModelJudgmentMayObserve || rule.ValidatorIdentity != "") {
		return fail(ReviewChainInvalid, "model judgment cannot establish this blocking evidence")
	}
	if !stringSubset([]string{evidence.RuleID}, capsule.PhaseAuthority.BlockingScopeIDs) || !stringSubset([]string{evidence.RuleID}, capsule.PhaseAuthority.AuthorizedInvariantIDs) {
		return fail(ScopeExpansionRequired, "finding evidence is outside blocker or invariant authority")
	}
	return nil
}

func findingEvidenceDigest(evidence FindingEvidenceV1) (string, error) {
	evidence.EvidenceSHA256 = ""
	type alias FindingEvidenceV1
	return digestJSON(alias(evidence))
}

// DeferredObservationV1 is reportable but cannot block or receive a lease.
type DeferredObservationV1 struct {
	ObservationID string   `json:"observation_id"`
	RuleID        string   `json:"rule_id"`
	EvidenceRefs  []string `json:"evidence_refs"`
}

// ReviewScopeReportV1 is one immutable report in the B review chain.
type ReviewScopeReportV1 struct {
	Kind                     string                  `json:"kind"`
	CapsuleFileSHA256        string                  `json:"capsule_file_sha256"`
	CapsuleSHA256            string                  `json:"capsule_sha256"`
	SemanticRegistrySHA256   string                  `json:"semantic_registry_sha256"`
	Sequence                 uint64                  `json:"sequence"`
	PredecessorReportSHA256  string                  `json:"predecessor_report_sha256,omitempty"`
	ReviewedPreFixHEAD       string                  `json:"reviewed_pre_fix_head"`
	ActiveFindings           []ActiveFindingV1       `json:"active_findings"`
	DeferredObservations     []DeferredObservationV1 `json:"deferred_observations"`
	RequestedMutationIDs     []string                `json:"requested_mutation_justification_ids"`
	ReviewerIdentity         string                  `json:"reviewer_identity"`
	ProviderIdentity         string                  `json:"provider_identity"`
	ReviewedBlockingScopeIDs []string                `json:"reviewed_blocking_scope_ids"`
	ReviewEvidenceSHA256     string                  `json:"review_evidence_sha256"`
	ReportSHA256             string                  `json:"report_sha256"`
}

// SealReviewScopeReportV1 validates and hashes one report relative to the
// exact durable predecessor tip.
func SealReviewScopeReportV1(capsule contextcapsule.Capsule, capsuleFileSHA256 string, registry SemanticAuthorityRegistryV1, evidence []FindingEvidenceV1, previous *ReviewScopeReportV1, report ReviewScopeReportV1) (ReviewScopeReportV1, error) {
	report.ReportSHA256 = ""
	if err := validateReviewPayload(capsule, capsuleFileSHA256, registry, evidence, previous, report); err != nil {
		return ReviewScopeReportV1{}, err
	}
	digest, err := reviewDigest(report)
	if err != nil {
		return ReviewScopeReportV1{}, err
	}
	report.ReportSHA256 = digest
	return report, nil
}

// ValidateReviewScopeReportV1 rejects chain forks, active-set growth,
// unregistered blockers, and deferred/blocker authority confusion.
func ValidateReviewScopeReportV1(capsule contextcapsule.Capsule, capsuleFileSHA256 string, registry SemanticAuthorityRegistryV1, evidence []FindingEvidenceV1, previous *ReviewScopeReportV1, report ReviewScopeReportV1) error {
	if previous != nil && report.ReportSHA256 == previous.ReportSHA256 {
		return validateReviewTip(report, capsule, capsuleFileSHA256, registry, evidence)
	}
	if err := validateReviewPayload(capsule, capsuleFileSHA256, registry, evidence, previous, report); err != nil {
		return err
	}
	digest, err := reviewDigest(report)
	if err != nil {
		return err
	}
	if report.ReportSHA256 != digest {
		return fail(ReviewChainInvalid, "review report digest mismatch")
	}
	return nil
}

// ValidateReviewCandidateV1 binds report validation to the controller's exact
// current pre-fix candidate instead of accepting a reviewer assertion.
func ValidateReviewCandidateV1(report ReviewScopeReportV1, expectedPreFixHEAD string) error {
	if !validOID(expectedPreFixHEAD) || report.ReviewedPreFixHEAD != expectedPreFixHEAD {
		return fail(ReviewChainInvalid, "reviewed pre-fix HEAD does not match controller candidate")
	}
	return nil
}

func validateReviewPayload(capsule contextcapsule.Capsule, capsuleFileSHA256 string, registry SemanticAuthorityRegistryV1, evidence []FindingEvidenceV1, previous *ReviewScopeReportV1, report ReviewScopeReportV1) error {
	if capsule.PolicyVersion != contextcapsule.PolicyVersionV3 || capsule.PhaseAuthority == nil || capsule.PhaseAuthority.Stage != contextcapsule.StageBImplementation && capsule.PhaseAuthority.Stage != contextcapsule.StageCAcceptanceMerge {
		return fail(ReviewChainInvalid, "review report requires B or C context-capsule-v3")
	}
	if err := ValidateSemanticAuthorityRegistryV1(registry); err != nil {
		return err
	}
	if report.Kind != "ReviewScopeReportV1" || report.CapsuleFileSHA256 != capsuleFileSHA256 || report.CapsuleSHA256 != capsule.CapsuleSHA256 || report.SemanticRegistrySHA256 != registry.RegistrySHA256 || registry.RegistrySHA256 != capsule.PhaseAuthority.SemanticRegistrySHA256 {
		return fail(ReviewChainInvalid, "report does not bind the exact capsule and registry")
	}
	if !validOID(report.ReviewedPreFixHEAD) || !validText(report.ReviewerIdentity, 512) || !validText(report.ProviderIdentity, 512) || !validSHA256(report.ReviewEvidenceSHA256) {
		return fail(ReviewChainInvalid, "review head/provider/evidence identity is invalid")
	}
	if len(report.ReviewedBlockingScopeIDs) == 0 || validateIDs(report.ReviewedBlockingScopeIDs) != nil || !equalStrings(report.ReviewedBlockingScopeIDs, capsule.PhaseAuthority.BlockingScopeIDs) {
		return fail(ReviewChainInvalid, "reviewed blocking scope does not equal the capsule blocker scope")
	}
	if previous == nil {
		if report.Sequence != 0 || report.PredecessorReportSHA256 != "" {
			return fail(ReviewChainInvalid, "first report must be sequence 0 without predecessor")
		}
	} else {
		if err := ValidateReviewScopeReportV1(capsule, capsuleFileSHA256, registry, evidence, nilForPreviousValidation(previous), *previous); err != nil {
			// A caller need not provide the entire chain, but the supplied durable tip
			// must at least have a valid internal digest and common bindings.
			if err := validateReviewTip(*previous, capsule, capsuleFileSHA256, registry, evidence); err != nil {
				return err
			}
		}
		if report.Sequence != previous.Sequence+1 || report.PredecessorReportSHA256 != previous.ReportSHA256 {
			return fail(ReviewChainInvalid, "report does not extend the exact durable tip")
		}
	}
	rules := registryMap(registry)
	activeIDs := make([]string, 0, len(report.ActiveFindings))
	activeRules := make(map[string]string, len(report.ActiveFindings))
	previousFinding := ""
	for _, finding := range report.ActiveFindings {
		if !validID(finding.FindingID) || finding.FindingID <= previousFinding || !validID(finding.RuleID) || finding.Severity != SeverityCritical && finding.Severity != SeverityMajor || validateEvidenceBindings(finding.Evidence) != nil {
			return fail(ReviewChainInvalid, "active findings are not canonical or valid")
		}
		previousFinding = finding.FindingID
		rule, ok := rules[finding.RuleID]
		if !ok {
			return fail(ScopeExpansionRequired, "finding %s has no immutable registry mapping", finding.FindingID)
		}
		if rule.Kind != RegistryInvariant && rule.Kind != RegistryKnownFinding {
			return fail(ScopeExpansionRequired, "finding %s maps to non-blocking registry kind %s", finding.FindingID, rule.Kind)
		}
		if err := resolveFindingEvidenceV1(capsule, registry, evidence, report.ReviewedPreFixHEAD, finding); err != nil {
			return err
		}
		if rule.CorrectionRelation == "DESIGN_GAP" {
			return fail(DesignGap, "finding %s requires new A-governed design authority", finding.FindingID)
		}
		if !stringSubset([]string{finding.RuleID}, capsule.PhaseAuthority.BlockingScopeIDs) || !stringSubset([]string{finding.RuleID}, capsule.PhaseAuthority.AuthorizedInvariantIDs) {
			return fail(ScopeExpansionRequired, "finding %s is outside B blocker authority", finding.FindingID)
		}
		activeIDs = append(activeIDs, finding.FindingID)
		activeRules[finding.FindingID] = finding.RuleID
	}
	if capsule.PhaseAuthority.Stage == contextcapsule.StageCAcceptanceMerge {
		if previous != nil || report.Sequence != 0 || len(report.ActiveFindings) != 0 || len(report.RequestedMutationIDs) != 0 {
			return fail(FinalReviewInvalidated, "C final review must be a clean read-only report")
		}
	}
	if capsule.PhaseAuthority.ReviewProfile == contextcapsule.ReviewProfileInitialImplementation {
		if previous == nil && len(report.ActiveFindings) > capsule.PhaseAuthority.ExecutionBounds.MaxInitialActiveFindings {
			return fail(ScopeExpansionRequired, "initial active finding ceiling exceeded")
		}
		if previous != nil && !stringSubset(activeIDs, findingIDs(previous.ActiveFindings)) {
			return fail(ScopeExpansionRequired, "active blocker set grew after pass 0")
		}
	} else if !stringSubset(activeIDs, capsule.PhaseAuthority.AuthorizedFindingIDs) {
		return fail(ScopeExpansionRequired, "correction report contains an unauthorized finding")
	}
	if previous != nil && !stringSubset(activeIDs, findingIDs(previous.ActiveFindings)) {
		return fail(ScopeExpansionRequired, "active blocker set is not monotone decreasing")
	}
	if previous != nil {
		previousRules := make(map[string]string, len(previous.ActiveFindings))
		for _, finding := range previous.ActiveFindings {
			previousRules[finding.FindingID] = finding.RuleID
		}
		for findingID, ruleID := range activeRules {
			if previousRules[findingID] != ruleID {
				return fail(ReviewChainInvalid, "active finding %s changed its immutable registry mapping", findingID)
			}
		}
	}
	deferredIDs := make(map[string]struct{}, len(report.DeferredObservations))
	previousObservation := ""
	for _, observation := range report.DeferredObservations {
		if !validID(observation.ObservationID) || observation.ObservationID <= previousObservation || !validID(observation.RuleID) || validateEvidenceRefs(observation.EvidenceRefs) != nil {
			return fail(ReviewChainInvalid, "deferred observations are not canonical or valid")
		}
		previousObservation = observation.ObservationID
		if _, duplicate := activeRules[observation.ObservationID]; duplicate {
			return fail(ReviewChainInvalid, "one ID cannot be both active and deferred")
		}
		if _, ok := rules[observation.RuleID]; !ok || !stringSubset([]string{observation.RuleID}, capsule.PhaseAuthority.ObservationScopeIDs) {
			return fail(ScopeExpansionRequired, "deferred observation is outside observation authority")
		}
		deferredIDs[observation.ObservationID] = struct{}{}
	}
	if err := validateIDs(report.RequestedMutationIDs); err != nil {
		return fail(ReviewChainInvalid, "requested mutation IDs: %v", err)
	}
	for _, findingID := range report.RequestedMutationIDs {
		ruleID, active := activeRules[findingID]
		if !active {
			if _, deferred := deferredIDs[findingID]; deferred {
				return fail(MutationScopeViolation, "deferred observation %s cannot justify mutation", findingID)
			}
			return fail(MutationScopeViolation, "mutation request %s is not active", findingID)
		}
		if !stringSubset([]string{ruleID}, capsule.PhaseAuthority.MutationScopeIDs) {
			return fail(MutationScopeViolation, "finding %s has no B mutation authority", findingID)
		}
	}
	return nil
}

func nilForPreviousValidation(_ *ReviewScopeReportV1) *ReviewScopeReportV1 { return nil }

func validateReviewTip(report ReviewScopeReportV1, capsule contextcapsule.Capsule, capsuleFileSHA256 string, registry SemanticAuthorityRegistryV1, evidence []FindingEvidenceV1) error {
	if report.CapsuleFileSHA256 != capsuleFileSHA256 || report.CapsuleSHA256 != capsule.CapsuleSHA256 || report.SemanticRegistrySHA256 != registry.RegistrySHA256 || !validSHA256(report.ReportSHA256) {
		return fail(ReviewChainInvalid, "supplied review tip binding is invalid")
	}
	digest, err := reviewDigest(report)
	if err != nil || digest != report.ReportSHA256 {
		return fail(ReviewChainInvalid, "supplied review tip digest is invalid")
	}
	for _, finding := range report.ActiveFindings {
		if err := resolveFindingEvidenceV1(capsule, registry, evidence, report.ReviewedPreFixHEAD, finding); err != nil {
			return err
		}
	}
	return nil
}

func resolveFindingEvidenceV1(capsule contextcapsule.Capsule, registry SemanticAuthorityRegistryV1, records []FindingEvidenceV1, candidateSHA string, finding ActiveFindingV1) error {
	byRef := make(map[string]FindingEvidenceV1, len(records))
	for _, record := range records {
		if _, duplicate := byRef[record.Ref]; duplicate {
			return fail(ReviewChainInvalid, "controller evidence contains a competing ref %q", record.Ref)
		}
		byRef[record.Ref] = record
	}
	for _, binding := range finding.Evidence {
		record, ok := byRef[binding.Ref]
		if !ok {
			return fail(ReviewChainInvalid, "finding %s evidence ref %q is not controller-owned", finding.FindingID, binding.Ref)
		}
		if err := ValidateFindingEvidenceV1(capsule, registry, record); err != nil {
			return err
		}
		if binding.SHA256 != record.EvidenceSHA256 || record.CandidateSHA != candidateSHA || record.FindingID != finding.FindingID || record.RuleID != finding.RuleID {
			return fail(ReviewChainInvalid, "finding %s evidence does not bind the exact candidate and invariant mapping", finding.FindingID)
		}
	}
	return nil
}

// validateImplementationConvergedCheckpointV1 is called only while holding the
// controller lock with report equal to the durable review tip.
func validateImplementationConvergedCheckpointV1(capsule contextcapsule.Capsule, capsuleFileSHA256 string, registry SemanticAuthorityRegistryV1, evidence []FindingEvidenceV1, report ReviewScopeReportV1, checkpoint PhaseCheckpointV1, nextGrant NextStageGrantV1) error {
	if capsule.PolicyVersion != contextcapsule.PolicyVersionV3 || capsule.PhaseAuthority == nil || capsule.PhaseAuthority.Stage != contextcapsule.StageBImplementation {
		return fail(CheckpointChainInvalid, "implementation convergence requires B context-capsule-v3")
	}
	if err := validateReviewTip(report, capsule, capsuleFileSHA256, registry, evidence); err != nil {
		return err
	}
	if err := validateCleanReviewBinding(capsule, capsuleFileSHA256, report, checkpoint, CheckpointImplementationConverged); err != nil {
		return err
	}
	if err := ValidateNextStageGrantV1(nextGrant); err != nil {
		return err
	}
	if nextGrant.Stage != contextcapsule.StageCAcceptanceMerge || checkpoint.NextStageGrantSHA256 != nextGrant.GrantSHA256 || !stringSubset(nextGrant.RequiredFinalReviewIDs, checkpoint.ReviewedBlockingScopeIDs) {
		return fail(CheckpointChainInvalid, "convergence checkpoint or C grant dropped a mandatory final-review floor")
	}
	return nil
}

// validateFinalReviewCleanCheckpointV1 is called only while holding the
// controller lock with report and grant loaded from the durable workflow tip.
func validateFinalReviewCleanCheckpointV1(capsule contextcapsule.Capsule, capsuleFileSHA256 string, registry SemanticAuthorityRegistryV1, evidence []FindingEvidenceV1, report ReviewScopeReportV1, checkpoint PhaseCheckpointV1, grant NextStageGrantV1) error {
	if capsule.PolicyVersion != contextcapsule.PolicyVersionV3 || capsule.PhaseAuthority == nil || capsule.PhaseAuthority.Stage != contextcapsule.StageCAcceptanceMerge {
		return fail(FinalReviewInvalidated, "final review requires C context-capsule-v3")
	}
	if err := validateReviewTip(report, capsule, capsuleFileSHA256, registry, evidence); err != nil {
		return err
	}
	if err := validateCleanReviewBinding(capsule, capsuleFileSHA256, report, checkpoint, CheckpointFinalReviewClean); err != nil {
		return err
	}
	if err := ValidateNextStageGrantV1(grant); err != nil {
		return err
	}
	if grant.Stage != contextcapsule.StageCAcceptanceMerge || capsule.PhaseAuthority.Parent == nil || capsule.PhaseAuthority.Parent.GrantSHA256 != grant.GrantSHA256 || !stringSubset(grant.RequiredFinalReviewIDs, checkpoint.ReviewedBlockingScopeIDs) || !stringSubset(grant.RequiredFinalReviewIDs, capsule.PhaseAuthority.BlockingScopeIDs) {
		return fail(FinalReviewInvalidated, "C final review omitted a mandatory blocker floor")
	}
	return nil
}

func validateCleanReviewBinding(capsule contextcapsule.Capsule, capsuleFileSHA256 string, report ReviewScopeReportV1, checkpoint PhaseCheckpointV1, kind CheckpointKind) error {
	if err := ValidatePhaseCheckpointV1(checkpoint); err != nil {
		return err
	}
	if checkpoint.Kind != kind || checkpoint.CapsuleFileSHA256 != capsuleFileSHA256 || checkpoint.CapsuleSHA256 != capsule.CapsuleSHA256 || checkpoint.CandidateSHA != report.ReviewedPreFixHEAD || checkpoint.ReviewScopeTipSHA256 != report.ReportSHA256 || checkpoint.ReviewCritical != 0 || checkpoint.ReviewMajor != 0 || len(report.ActiveFindings) != 0 || len(report.RequestedMutationIDs) != 0 || !equalStrings(checkpoint.ReviewedBlockingScopeIDs, report.ReviewedBlockingScopeIDs) {
		return fail(CheckpointChainInvalid, "%s is not bound to the exact clean review tip", kind)
	}
	return nil
}

func reviewDigest(report ReviewScopeReportV1) (string, error) {
	report.ReportSHA256 = ""
	type alias ReviewScopeReportV1
	return digestJSON(alias(report))
}

// MutationLimitsV1 lets a controller tighten, never widen, B diff ceilings.
type MutationLimitsV1 struct {
	MaxChangedFiles int   `json:"max_changed_files"`
	MaxChangedBytes int64 `json:"max_changed_bytes"`
}

// MutationLeaseV1 is a one-use authority derived from one validated report.
type MutationLeaseV1 struct {
	Kind              string   `json:"kind"`
	CapsuleSHA256     string   `json:"capsule_sha256"`
	ReportSHA256      string   `json:"report_sha256"`
	PreFixHEAD        string   `json:"pre_fix_head"`
	BlockerFindingIDs []string `json:"blocker_finding_ids"`
	MutationRuleIDs   []string `json:"mutation_rule_ids"`
	AllowedPaths      []string `json:"allowed_paths"`
	MaxChangedFiles   int      `json:"max_changed_files"`
	MaxChangedBytes   int64    `json:"max_changed_bytes"`
	LeaseSHA256       string   `json:"lease_sha256"`
}

// IssueMutationLeaseV1 deterministically intersects B path authority with the
// correction path family of every selected registry rule.
func IssueMutationLeaseV1(capsule contextcapsule.Capsule, registry SemanticAuthorityRegistryV1, evidence []FindingEvidenceV1, report ReviewScopeReportV1, limits MutationLimitsV1) (MutationLeaseV1, error) {
	if err := ValidateReviewScopeReportV1(capsule, report.CapsuleFileSHA256, registry, evidence, nil, report); err != nil {
		return MutationLeaseV1{}, err
	}
	return issueMutationLease(capsule, registry, report, limits)
}

// IssueMutationLeaseForTransitionV1 issues from a later report only after
// validating its exact predecessor report tip.
func IssueMutationLeaseForTransitionV1(capsule contextcapsule.Capsule, capsuleFileSHA256 string, registry SemanticAuthorityRegistryV1, evidence []FindingEvidenceV1, previous ReviewScopeReportV1, report ReviewScopeReportV1, limits MutationLimitsV1) (MutationLeaseV1, error) {
	if err := ValidateReviewScopeReportV1(capsule, capsuleFileSHA256, registry, evidence, &previous, report); err != nil {
		return MutationLeaseV1{}, err
	}
	return issueMutationLease(capsule, registry, report, limits)
}

func issueMutationLease(capsule contextcapsule.Capsule, registry SemanticAuthorityRegistryV1, report ReviewScopeReportV1, limits MutationLimitsV1) (MutationLeaseV1, error) {
	if len(report.RequestedMutationIDs) == 0 {
		return MutationLeaseV1{}, fail(MutationScopeViolation, "cannot issue an empty mutation lease")
	}
	bounds := capsule.PhaseAuthority.ExecutionBounds
	if bounds == nil || limits.MaxChangedFiles < 1 || limits.MaxChangedBytes < 1 || limits.MaxChangedFiles > bounds.MaxChangedFiles || limits.MaxChangedBytes > bounds.MaxChangedBytes {
		return MutationLeaseV1{}, fail(MutationScopeViolation, "lease diff ceilings must positively tighten B bounds")
	}
	findings := make(map[string]ActiveFindingV1, len(report.ActiveFindings))
	for _, finding := range report.ActiveFindings {
		findings[finding.FindingID] = finding
	}
	rules := registryMap(registry)
	mutationRules := make([]string, 0, len(report.RequestedMutationIDs))
	pathSet := make(map[string]struct{})
	for _, findingID := range report.RequestedMutationIDs {
		finding := findings[findingID]
		mutationRules = append(mutationRules, finding.RuleID)
		for _, allowed := range intersectPaths(capsule.PhaseAuthority.AllowedPaths, rules[finding.RuleID].AllowedCorrectionPaths) {
			pathSet[allowed] = struct{}{}
		}
	}
	mutationRules = sortedUnique(mutationRules)
	paths := mapKeys(pathSet)
	if len(paths) == 0 {
		return MutationLeaseV1{}, fail(MutationScopeViolation, "semantic correction authority has no intersection with B paths")
	}
	lease := MutationLeaseV1{
		Kind: "MutationLeaseV1", CapsuleSHA256: capsule.CapsuleSHA256, ReportSHA256: report.ReportSHA256,
		PreFixHEAD: report.ReviewedPreFixHEAD, BlockerFindingIDs: append([]string(nil), report.RequestedMutationIDs...),
		MutationRuleIDs: mutationRules, AllowedPaths: paths, MaxChangedFiles: limits.MaxChangedFiles, MaxChangedBytes: limits.MaxChangedBytes,
	}
	digest, err := leaseDigest(lease)
	if err != nil {
		return MutationLeaseV1{}, err
	}
	lease.LeaseSHA256 = digest
	return lease, nil
}

// ValidateMutationLeaseV1 proves a lease equals the controller-derived value.
func ValidateMutationLeaseV1(capsule contextcapsule.Capsule, registry SemanticAuthorityRegistryV1, evidence []FindingEvidenceV1, report ReviewScopeReportV1, lease MutationLeaseV1) error {
	expected, err := IssueMutationLeaseV1(capsule, registry, evidence, report, MutationLimitsV1{lease.MaxChangedFiles, lease.MaxChangedBytes})
	if err != nil {
		return err
	}
	actual, marshalErr := json.Marshal(lease)
	canonical, expectedErr := json.Marshal(expected)
	if marshalErr != nil || expectedErr != nil || !bytes.Equal(actual, canonical) {
		return fail(MutationScopeViolation, "lease is not the deterministic report-derived authority")
	}
	return nil
}

// ValidateMutationLeaseForTransitionV1 proves a later-report lease against
// the exact report transition which authorized it.
func ValidateMutationLeaseForTransitionV1(capsule contextcapsule.Capsule, capsuleFileSHA256 string, registry SemanticAuthorityRegistryV1, evidence []FindingEvidenceV1, previous ReviewScopeReportV1, report ReviewScopeReportV1, lease MutationLeaseV1) error {
	expected, err := IssueMutationLeaseForTransitionV1(capsule, capsuleFileSHA256, registry, evidence, previous, report, MutationLimitsV1{lease.MaxChangedFiles, lease.MaxChangedBytes})
	if err != nil {
		return err
	}
	actual, marshalErr := json.Marshal(lease)
	canonical, expectedErr := json.Marshal(expected)
	if marshalErr != nil || expectedErr != nil || !bytes.Equal(actual, canonical) {
		return fail(MutationScopeViolation, "lease is not the deterministic report-transition authority")
	}
	return nil
}

func leaseDigest(lease MutationLeaseV1) (string, error) {
	lease.LeaseSHA256 = ""
	type alias MutationLeaseV1
	return digestJSON(alias(lease))
}

// LeaseStatus is the atomic one-use controller state.
type LeaseStatus string

const (
	LeaseIssued    LeaseStatus = "ISSUED"
	LeaseConsuming LeaseStatus = "CONSUMING"
	LeaseConsumed  LeaseStatus = "CONSUMED"
)

// MutationStateV1 is persisted under one workflow lock.
type MutationStateV1 struct {
	LeaseTipSHA256   string      `json:"lease_tip_sha256"`
	LeaseStatus      LeaseStatus `json:"lease_status"`
	ReceiptTipSHA256 string      `json:"receipt_tip_sha256,omitempty"`
}

// NewMutationStateV1 creates the only valid ISSUED state for a lease.
func NewMutationStateV1(lease MutationLeaseV1, receiptTipSHA256 string) (MutationStateV1, error) {
	digest, err := leaseDigest(lease)
	if err != nil || digest != lease.LeaseSHA256 {
		return MutationStateV1{}, fail(MutationScopeViolation, "cannot persist an invalid lease")
	}
	if receiptTipSHA256 != "" && !validSHA256(receiptTipSHA256) {
		return MutationStateV1{}, fail(MutationScopeViolation, "receipt tip is invalid")
	}
	return MutationStateV1{LeaseTipSHA256: lease.LeaseSHA256, LeaseStatus: LeaseIssued, ReceiptTipSHA256: receiptTipSHA256}, nil
}

// BeginMutationLeaseV1 is the fail-closed ISSUED->CONSUMING CAS transition.
func BeginMutationLeaseV1(state *MutationStateV1, lease MutationLeaseV1) error {
	if state == nil || !validSHA256(lease.LeaseSHA256) || state.LeaseTipSHA256 != lease.LeaseSHA256 || state.LeaseStatus != LeaseIssued {
		return fail(MutationScopeViolation, "lease is absent, replayed, forked, or not ISSUED")
	}
	digest, err := leaseDigest(lease)
	if err != nil || digest != lease.LeaseSHA256 {
		return fail(MutationScopeViolation, "lease digest is invalid")
	}
	state.LeaseStatus = LeaseConsuming
	return nil
}

// MutationReceiptV1 binds the independently validated result of one fix batch.
type MutationReceiptV1 struct {
	Kind                     string   `json:"kind"`
	LeaseSHA256              string   `json:"lease_sha256"`
	ReportSHA256             string   `json:"report_sha256"`
	PreFixHEAD               string   `json:"pre_fix_head"`
	ResultHEAD               string   `json:"result_head"`
	ChangedPaths             []string `json:"changed_paths"`
	ChangedPathSHA256        string   `json:"changed_path_sha256"`
	ChangedFiles             int      `json:"changed_files"`
	ChangedBytes             int64    `json:"changed_bytes"`
	PredecessorReceiptSHA256 string   `json:"predecessor_receipt_sha256,omitempty"`
	ReceiptSHA256            string   `json:"receipt_sha256"`
}

// SealMutationReceiptV1 validates receipt evidence and computes its digest.
func SealMutationReceiptV1(lease MutationLeaseV1, state MutationStateV1, proof CandidateProofV1, receipt MutationReceiptV1) (MutationReceiptV1, error) {
	receipt.ReceiptSHA256 = ""
	if err := validateReceiptPayload(lease, state, proof, receipt); err != nil {
		return MutationReceiptV1{}, err
	}
	digest, err := receiptDigest(receipt)
	if err != nil {
		return MutationReceiptV1{}, err
	}
	receipt.ReceiptSHA256 = digest
	return receipt, nil
}

// ValidateMutationReceiptV1 validates and atomically advances receipt/lease
// state. A CONSUMING crash remains blocking; the lease is never reset.
func ValidateMutationReceiptV1(lease MutationLeaseV1, state *MutationStateV1, proof CandidateProofV1, receipt MutationReceiptV1) error {
	if state == nil {
		return fail(MutationScopeViolation, "mutation state is required")
	}
	if err := validateReceiptPayload(lease, *state, proof, receipt); err != nil {
		return err
	}
	digest, err := receiptDigest(receipt)
	if err != nil || receipt.ReceiptSHA256 != digest {
		return fail(MutationScopeViolation, "receipt digest is invalid")
	}
	state.ReceiptTipSHA256 = receipt.ReceiptSHA256
	state.LeaseStatus = LeaseConsumed
	return nil
}

func validateReceiptPayload(lease MutationLeaseV1, state MutationStateV1, proof CandidateProofV1, receipt MutationReceiptV1) error {
	if state.LeaseStatus != LeaseConsuming || state.LeaseTipSHA256 != lease.LeaseSHA256 {
		return fail(MutationScopeViolation, "receipt has no exact CONSUMING lease tip")
	}
	if receipt.Kind != "MutationReceiptV1" || receipt.LeaseSHA256 != lease.LeaseSHA256 || receipt.ReportSHA256 != lease.ReportSHA256 || receipt.PreFixHEAD != lease.PreFixHEAD || receipt.PreFixHEAD != proof.BaseSHA || receipt.ResultHEAD != proof.CandidateSHA {
		return fail(MutationScopeViolation, "receipt does not bind lease, report, and exact result")
	}
	if !proof.WorkspaceClean || !validOID(proof.BaseSHA) || !validOID(proof.CandidateSHA) || !validSHA256(proof.ChangedPathSHA256) || proof.ChangedFiles != len(proof.ChangedPaths) || proof.ChangedBytes < 0 {
		return fail(MutationScopeViolation, "candidate proof is incomplete or not clean")
	}
	if receipt.PredecessorReceiptSHA256 != state.ReceiptTipSHA256 || receipt.ChangedPathSHA256 != proof.ChangedPathSHA256 || receipt.ChangedFiles != proof.ChangedFiles || receipt.ChangedBytes != proof.ChangedBytes || !equalStrings(receipt.ChangedPaths, proof.ChangedPaths) {
		return fail(MutationScopeViolation, "receipt does not extend the receipt tip or exact diff proof")
	}
	if receipt.ChangedFiles < 1 || receipt.ChangedFiles > lease.MaxChangedFiles || receipt.ChangedBytes > lease.MaxChangedBytes {
		return fail(MutationScopeViolation, "receipt exceeds lease diff ceilings")
	}
	previous := ""
	for _, path := range receipt.ChangedPaths {
		if path <= previous {
			return fail(MutationScopeViolation, "receipt paths are not sorted and unique")
		}
		previous = path
		if !pathAllowed(path, lease.AllowedPaths) {
			return fail(MutationScopeViolation, "receipt path %q exceeds lease", path)
		}
	}
	digest := sha256.Sum256(pathsRawForDigest(receipt.ChangedPaths))
	if receipt.ChangedPathSHA256 != hex.EncodeToString(digest[:]) {
		return fail(MutationScopeViolation, "receipt path digest is invalid")
	}
	return nil
}

func receiptDigest(receipt MutationReceiptV1) (string, error) {
	receipt.ReceiptSHA256 = ""
	type alias MutationReceiptV1
	return digestJSON(alias(receipt))
}

// GovernanceActivationV1 is the precise repository/controller cutover from
// legacy V2 A/B/C authorities to mandatory V3.
type GovernanceActivationV1 struct {
	Kind                       string   `json:"kind"`
	PolicyVersion              string   `json:"policy_version"`
	PolicySHA256               string   `json:"policy_sha256"`
	ActivationRepositoryCommit string   `json:"activation_repository_commit"`
	ActivationSequence         uint64   `json:"activation_sequence"`
	ActivationTime             string   `json:"activation_time"`
	GrandfatheredV2Digests     []string `json:"grandfathered_v2_digests"`
	ActivationSHA256           string   `json:"activation_sha256"`
}

// SealGovernanceActivationV1 validates and hashes an activation record.
func SealGovernanceActivationV1(activation GovernanceActivationV1) (GovernanceActivationV1, error) {
	activation.ActivationSHA256 = ""
	if err := validateActivationPayload(activation); err != nil {
		return GovernanceActivationV1{}, err
	}
	digest, err := activationDigest(activation)
	if err != nil {
		return GovernanceActivationV1{}, err
	}
	activation.ActivationSHA256 = digest
	return activation, nil
}

// ValidateGovernanceActivationV1 validates the durable cutover record.
func ValidateGovernanceActivationV1(activation GovernanceActivationV1) error {
	if err := validateActivationPayload(activation); err != nil {
		return err
	}
	digest, err := activationDigest(activation)
	if err != nil || activation.ActivationSHA256 != digest {
		return fail(CapsuleLineageInvalid, "activation digest mismatch")
	}
	return nil
}

// ValidateActivatedWorkflowPolicyV1 rejects post-activation V2 assertion. V2
// may finish only when its exact digest was durably issued before activation
// and is named in the finite grandfather set.
func ValidateActivatedWorkflowPolicyV1(policyVersion, authorityDigest string, issuedSequence uint64, activation GovernanceActivationV1) error {
	if err := ValidateGovernanceActivationV1(activation); err != nil {
		return err
	}
	if policyVersion == contextcapsule.PolicyVersionV3 {
		return nil
	}
	if policyVersion != contextcapsule.PolicyVersionV2 || issuedSequence >= activation.ActivationSequence || !stringSubset([]string{authorityDigest}, activation.GrandfatheredV2Digests) {
		return fail(CapsuleLineageInvalid, "A/B/C workflow requires V3 after activation; V2 is not grandfathered")
	}
	return nil
}

func validateActivationPayload(activation GovernanceActivationV1) error {
	if activation.Kind != "GovernanceActivationV1" || activation.PolicyVersion != contextcapsule.PolicyVersionV3 || !validSHA256(activation.PolicySHA256) || !validOID(activation.ActivationRepositoryCommit) || activation.ActivationSequence == 0 {
		return fail(CapsuleLineageInvalid, "activation policy, commit, or sequence is invalid")
	}
	if activation.GrandfatheredV2Digests == nil {
		return fail(CapsuleLineageInvalid, "grandfathered V2 digests must be an explicit array")
	}
	when, err := time.Parse(time.RFC3339, activation.ActivationTime)
	if err != nil || when.UTC().Format(time.RFC3339) != activation.ActivationTime {
		return fail(CapsuleLineageInvalid, "activation_time must be canonical UTC RFC3339")
	}
	previous := ""
	for _, digest := range activation.GrandfatheredV2Digests {
		if !validSHA256(digest) || digest <= previous {
			return fail(CapsuleLineageInvalid, "grandfathered V2 digests must be sorted, unique, and exact")
		}
		previous = digest
	}
	return nil
}

func activationDigest(activation GovernanceActivationV1) (string, error) {
	activation.ActivationSHA256 = ""
	type alias GovernanceActivationV1
	return digestJSON(alias(activation))
}

// IssuedAuthorityV1 is controller-owned proof that one exact legacy authority
// existed before activation. The sequence is never accepted from a manifest.
type IssuedAuthorityV1 struct {
	AuthoritySHA256 string `json:"authority_sha256"`
	Sequence        uint64 `json:"sequence"`
}

// InvocationReservationV1 is a durable in-flight marker. A controller crash
// leaves the workflow blocked instead of silently resetting cumulative state.
type InvocationReservationV1 struct {
	Token               string `json:"token"`
	CapsuleSHA256       string `json:"capsule_sha256"`
	StartedAt           string `json:"started_at"`
	MutationLeaseSHA256 string `json:"mutation_lease_sha256,omitempty"`
}

// ControllerStateV1 is the single non-forkable durable tip for one workflow.
// It is always advanced through the workflow-wide authority backend CAS.
type ControllerStateV1 struct {
	Kind                  string                   `json:"kind"`
	ControllerIdentity    string                   `json:"controller_identity"`
	RepositoryIdentity    string                   `json:"repository_identity,omitempty"`
	Revision              uint64                   `json:"revision"`
	NextAuthoritySequence uint64                   `json:"next_authority_sequence"`
	IssuedV2Authorities   []IssuedAuthorityV1      `json:"issued_v2_authorities"`
	Activation            *GovernanceActivationV1  `json:"activation,omitempty"`
	BCapsuleSHA256        string                   `json:"b_capsule_sha256,omitempty"`
	ExecutionBoundsSHA256 string                   `json:"execution_bounds_sha256,omitempty"`
	ExecutionState        ralphex.ExecutionStateV1 `json:"execution_state"`
	ActiveInvocation      *InvocationReservationV1 `json:"active_invocation,omitempty"`
	ReviewTip             *ReviewScopeReportV1     `json:"review_tip,omitempty"`
	ReviewCapsuleSHA256   string                   `json:"review_capsule_sha256,omitempty"`
	FindingEvidence       []FindingEvidenceV1      `json:"finding_evidence"`
	Lease                 *MutationLeaseV1         `json:"lease,omitempty"`
	MutationState         *MutationStateV1         `json:"mutation_state,omitempty"`
	FixBatchLeaseSHA256   string                   `json:"fix_batch_lease_sha256,omitempty"`
	CheckpointTip         *PhaseCheckpointV1       `json:"checkpoint_tip,omitempty"`
	NextStageGrant        *NextStageGrantV1        `json:"next_stage_grant,omitempty"`
}

// WorkflowAuthorityBackendV1 is the controller-owned, workflow-wide CAS
// authority. Implementations must provide one durable, linearizable revision
// stream for an authority domain across every controller host and user. A
// host-local file, process-local mutex, or per-host flock does not satisfy this
// contract.
//
// LoadWorkflowStateV1 must fail when the authoritative record is unavailable
// or has not been explicitly initialized by the control plane. It must never
// synthesize an empty pre-activation state. CompareAndSwapWorkflowStateV1 must
// atomically replace exactly expectedRevision and return swapped=false when a
// competing controller advanced the record first.
type WorkflowAuthorityBackendV1 interface {
	AuthorityDomainV1() (string, error)
	LoadWorkflowStateV1(controllerIdentity string) (canonicalState []byte, revision uint64, err error)
	CompareAndSwapWorkflowStateV1(controllerIdentity string, expectedRevision uint64, canonicalState []byte) (swapped bool, err error)
}

// ControllerV1 owns the one strict-canonical state record derived from the
// durable repository identity and controller-owned authority domain.
type ControllerV1 struct {
	repository              string
	repositoryIdentity      string
	repositoryControllerKey string
	authorityDomain         string
	identity                string
	backend                 WorkflowAuthorityBackendV1
	findingEvidenceResolver FindingEvidenceResolverV1
}

// OpenControllerV1 preserves the legacy constructor shape but deliberately
// has no implicit storage fallback. Durable operations fail closed until
// controller composition supplies a workflow-wide authority backend.
func OpenControllerV1(repository string) (*ControllerV1, error) {
	return openControllerV1(repository, nil, nil)
}

// OpenControllerWithFindingEvidenceV1 installs the trusted artifact resolver
// and deterministic validator used by controller finding ingestion. The
// resolver belongs to controller composition and is never read from a report.
// Like OpenControllerV1, this legacy constructor has no implicit authority
// backend and therefore cannot perform durable operations.
func OpenControllerWithFindingEvidenceV1(repository string, resolver FindingEvidenceResolverV1) (*ControllerV1, error) {
	if resolver == nil {
		return nil, fail(ReviewChainInvalid, "controller finding evidence resolver is required")
	}
	return openControllerV1(repository, nil, resolver)
}

// OpenControllerWithAuthorityBackendV1 binds a repository controller to the
// explicit workflow-wide CAS authority selected by trusted controller
// composition. The backend, not a manifest or repository path, owns the
// authority-domain selection.
func OpenControllerWithAuthorityBackendV1(repository string, backend WorkflowAuthorityBackendV1) (*ControllerV1, error) {
	return openControllerV1(repository, backend, nil)
}

// OpenControllerWithAuthorityBackendAndFindingEvidenceV1 additionally binds
// the trusted finding-evidence resolver used by B-IMPL-08 validation.
func OpenControllerWithAuthorityBackendAndFindingEvidenceV1(repository string, backend WorkflowAuthorityBackendV1, resolver FindingEvidenceResolverV1) (*ControllerV1, error) {
	if resolver == nil {
		return nil, fail(ReviewChainInvalid, "controller finding evidence resolver is required")
	}
	return openControllerV1(repository, backend, resolver)
}

func openControllerV1(repository string, backend WorkflowAuthorityBackendV1, resolver FindingEvidenceResolverV1) (*ControllerV1, error) {
	root, err := canonicalRepository(repository)
	if err != nil {
		return nil, fail(ExecutionBoundsInvalid, "resolve controller repository: %v", err)
	}
	repositoryIdentity, err := repositoryControllerIdentity(root)
	if err != nil {
		return nil, fail(ExecutionBoundsInvalid, "derive durable repository identity: %v", err)
	}
	repositoryKey, err := repositoryControllerKey(repositoryIdentity)
	if err != nil {
		return nil, fail(ExecutionBoundsInvalid, "derive durable repository key: %v", err)
	}
	authorityDomain := ""
	if backend != nil {
		authorityDomain, err = backend.AuthorityDomainV1()
		if err != nil {
			return nil, fail(ExecutionBoundsInvalid, "resolve workflow-wide authority domain: %v", err)
		}
		if !validSHA256(authorityDomain) {
			return nil, fail(ExecutionBoundsInvalid, "workflow-wide authority domain identity is invalid")
		}
	}
	identity, err := digestJSON(struct {
		Kind            string `json:"kind"`
		RepositoryKey   string `json:"repository_key"`
		AuthorityDomain string `json:"authority_domain"`
	}{Kind: "GovernanceControllerIdentityV1", RepositoryKey: repositoryKey, AuthorityDomain: authorityDomain})
	if err != nil {
		return nil, fail(ExecutionBoundsInvalid, "derive controller identity: %v", err)
	}
	return &ControllerV1{
		repository: root, repositoryIdentity: repositoryIdentity, repositoryControllerKey: repositoryKey, identity: identity,
		authorityDomain: authorityDomain, backend: backend, findingEvidenceResolver: resolver,
	}, nil
}

// ControllerIdentity returns the stable repository-owned controller identity.
func (c *ControllerV1) ControllerIdentity() string {
	if c == nil {
		return ""
	}
	return c.identity
}

// Snapshot returns a detached copy of the durable controller state.
func (c *ControllerV1) Snapshot() (ControllerStateV1, error) {
	var result ControllerStateV1
	err := c.withState(func(state *ControllerStateV1) (bool, error) {
		data, err := json.Marshal(state)
		if err != nil {
			return false, err
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return false, err
		}
		return false, nil
	})
	return result, err
}

// RecordFindingEvidenceV1 resolves artifact bytes and deterministic validator
// results through controller-owned dependencies, derives all registry metadata,
// and persists the resulting immutable record before a report can name it.
func (c *ControllerV1) RecordFindingEvidenceV1(repository string, capsule contextcapsule.Capsule, registry SemanticAuthorityRegistryV1, request FindingEvidenceRequestV1) (FindingEvidenceV1, error) {
	var recorded FindingEvidenceV1
	if err := c.validateRepository(repository, capsule.Repository); err != nil {
		return recorded, err
	}
	if c.findingEvidenceResolver == nil {
		return recorded, fail(ReviewChainInvalid, "controller finding evidence resolver is unavailable")
	}
	if err := ValidateSemanticAuthorityRegistryV1(registry); err != nil {
		return recorded, err
	}
	if capsule.PolicyVersion != contextcapsule.PolicyVersionV3 || capsule.PhaseAuthority == nil || registry.RegistrySHA256 != capsule.PhaseAuthority.SemanticRegistrySHA256 {
		return recorded, fail(ReviewChainInvalid, "finding evidence request is outside the exact V3 registry authority")
	}
	if !validText(request.Ref, 2048) || !validOID(request.CandidateSHA) || !validID(request.FindingID) || !validID(request.RuleID) {
		return recorded, fail(ReviewChainInvalid, "finding evidence request identity is invalid")
	}
	rule, ok := registryMap(registry)[request.RuleID]
	if !ok || rule.Kind != RegistryInvariant && rule.Kind != RegistryKnownFinding {
		return recorded, fail(ScopeExpansionRequired, "finding evidence has no blocking registry mapping")
	}
	if !stringSubset([]string{request.RuleID}, capsule.PhaseAuthority.BlockingScopeIDs) || !stringSubset([]string{request.RuleID}, capsule.PhaseAuthority.AuthorizedInvariantIDs) {
		return recorded, fail(ScopeExpansionRequired, "finding evidence is outside blocker or invariant authority")
	}
	if _, err := ValidateCandidateV1(repository, capsule, request.CandidateSHA); err != nil {
		return recorded, err
	}
	artifact, err := c.findingEvidenceResolver.ResolveArtifactV1(repository, request.CandidateSHA, request.Ref)
	if err != nil {
		return recorded, fail(ReviewChainInvalid, "resolve controller finding artifact: %v", err)
	}
	if len(artifact) == 0 || len(artifact) > 16<<20 {
		return recorded, fail(ReviewChainInvalid, "controller finding artifact is empty or exceeds the bounded limit")
	}
	modelJudgment := rule.ValidatorIdentity == ""
	if modelJudgment {
		if !rule.ModelJudgmentMayObserve {
			return recorded, fail(ReviewChainInvalid, "registry rule has no deterministic validator and forbids model judgment")
		}
	} else if err := c.findingEvidenceResolver.ConfirmViolationV1(repository, request.CandidateSHA, rule, append([]byte(nil), artifact...)); err != nil {
		return recorded, fail(ReviewChainInvalid, "registered deterministic validator did not confirm violation: %v", err)
	}
	artifactDigest := sha256.Sum256(artifact)
	evidence, err := SealFindingEvidenceV1(capsule, registry, FindingEvidenceV1{
		Kind: "FindingEvidenceV1", Ref: request.Ref, CapsuleSHA256: capsule.CapsuleSHA256,
		SemanticRegistrySHA256: registry.RegistrySHA256, CandidateSHA: request.CandidateSHA,
		FindingID: request.FindingID, RuleID: request.RuleID, RegisteredKind: rule.Kind,
		EvidenceClass: rule.EvidenceClass, ValidatorIdentity: rule.ValidatorIdentity,
		ModelJudgmentUsed: modelJudgment, Outcome: "VIOLATION_CONFIRMED",
		ArtifactSHA256: hex.EncodeToString(artifactDigest[:]),
	})
	if err != nil {
		return recorded, err
	}
	err = c.withState(func(state *ControllerStateV1) (bool, error) {
		bound, err := c.bindCapsuleRepository(state, capsule)
		if err != nil {
			return false, err
		}
		head, err := gitText(repository, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil || head != evidence.CandidateSHA {
			return false, fail(ReviewChainInvalid, "finding evidence is not for the controller's exact current candidate")
		}
		for _, existing := range state.FindingEvidence {
			if existing.Ref != evidence.Ref {
				continue
			}
			if existing.EvidenceSHA256 == evidence.EvidenceSHA256 {
				recorded = existing
				return bound, nil
			}
			return false, fail(ReviewChainInvalid, "a competing controller evidence record already uses ref %q", evidence.Ref)
		}
		if len(state.FindingEvidence) >= 256 {
			return false, fail(ReviewChainInvalid, "controller finding evidence limit is exhausted")
		}
		state.FindingEvidence = append(state.FindingEvidence, evidence)
		sort.Slice(state.FindingEvidence, func(i, j int) bool { return state.FindingEvidence[i].Ref < state.FindingEvidence[j].Ref })
		recorded = evidence
		return true, nil
	})
	return recorded, err
}

// AdvanceCheckpointV1 validates and atomically advances the sole durable
// checkpoint tip. Clean B/C gates use only the controller's stored review tip
// and stored C grant; caller-provided predecessor reports cannot satisfy them.
func (c *ControllerV1) AdvanceCheckpointV1(input CheckpointAdvanceV1) error {
	if err := c.validateRepository(input.Repository, input.Capsule.Repository); err != nil {
		return err
	}
	if input.Checkpoint.Repository != input.Capsule.Repository || input.Checkpoint.CapsuleFileSHA256 != input.CapsuleFileSHA256 || input.Checkpoint.CapsuleSHA256 != input.Capsule.CapsuleSHA256 {
		return fail(CheckpointChainInvalid, "checkpoint does not bind the controller repository and exact capsule")
	}
	if _, err := ValidateCandidateV1(input.Repository, input.Capsule, input.Checkpoint.CandidateSHA); err != nil {
		return err
	}
	return c.withState(func(state *ControllerStateV1) (bool, error) {
		bound, err := c.bindCapsuleRepository(state, input.Capsule)
		if err != nil {
			return false, err
		}
		if err := ValidateCheckpointTransitionV1(state.CheckpointTip, input.Checkpoint); err != nil {
			return false, err
		}
		if state.CheckpointTip != nil && state.CheckpointTip.CheckpointSHA256 == input.Checkpoint.CheckpointSHA256 {
			if input.Checkpoint.NextStageGrantSHA256 == "" {
				if input.NextStageGrant != nil {
					return false, fail(CheckpointChainInvalid, "checkpoint replay supplied an unexpected stage grant")
				}
			} else if input.NextStageGrant == nil || state.NextStageGrant == nil || input.NextStageGrant.GrantSHA256 != input.Checkpoint.NextStageGrantSHA256 || state.NextStageGrant.GrantSHA256 != input.Checkpoint.NextStageGrantSHA256 {
				return false, fail(CheckpointChainInvalid, "checkpoint replay does not verify the durable stage grant")
			}
			return bound, nil
		}
		switch input.Checkpoint.Kind {
		case CheckpointDesignAccepted:
			if input.NextStageGrant == nil {
				return false, fail(CheckpointChainInvalid, "DESIGN_ACCEPTED requires the exact B grant")
			}
			if err := validateCheckpointGrantBinding(input.Checkpoint, *input.NextStageGrant, contextcapsule.StageBImplementation); err != nil {
				return false, err
			}
		case CheckpointImplementationConverged:
			if input.NextStageGrant == nil || state.NextStageGrant == nil || state.NextStageGrant.Stage != contextcapsule.StageBImplementation || state.ReviewTip == nil || state.ReviewCapsuleSHA256 != input.Capsule.CapsuleSHA256 {
				return false, fail(CheckpointChainInvalid, "IMPLEMENTATION_CONVERGED requires the exact durable B review tip and C grant")
			}
			if state.MutationState != nil && state.MutationState.LeaseStatus != LeaseConsumed {
				return false, fail(MutationScopeViolation, "unfinished mutation lease blocks implementation convergence")
			}
			if err := validateImplementationConvergedCheckpointV1(input.Capsule, input.CapsuleFileSHA256, input.Registry, state.FindingEvidence, *state.ReviewTip, input.Checkpoint, *input.NextStageGrant); err != nil {
				return false, err
			}
			if err := validateCheckpointGrantBinding(input.Checkpoint, *input.NextStageGrant, contextcapsule.StageCAcceptanceMerge); err != nil {
				return false, err
			}
			if !stringSubset(state.NextStageGrant.RequiredInvariantIDs, input.NextStageGrant.RequiredInvariantIDs) || !stringSubset(state.NextStageGrant.RequiredFinalReviewIDs, input.NextStageGrant.RequiredFinalReviewIDs) {
				return false, fail(CheckpointChainInvalid, "C grant dropped a durable B mandatory final-review floor")
			}
		case CheckpointAcceptancePassed:
			if state.NextStageGrant == nil || input.Capsule.PhaseAuthority == nil || input.Capsule.PhaseAuthority.Parent == nil || input.Capsule.PhaseAuthority.Parent.GrantSHA256 != state.NextStageGrant.GrantSHA256 || input.Capsule.PhaseAuthority.Parent.CheckpointSHA256 != state.CheckpointTip.CheckpointSHA256 {
				return false, fail(CheckpointChainInvalid, "ACCEPTANCE_PASSED is not under the durable C grant")
			}
		case CheckpointFinalReviewClean:
			if input.NextStageGrant != nil || state.NextStageGrant == nil || state.ReviewTip == nil || state.ReviewCapsuleSHA256 != input.Capsule.CapsuleSHA256 {
				return false, fail(FinalReviewInvalidated, "FINAL_REVIEW_CLEAN requires the durable C review and grant tips")
			}
			if err := validateFinalReviewCleanCheckpointV1(input.Capsule, input.CapsuleFileSHA256, input.Registry, state.FindingEvidence, *state.ReviewTip, input.Checkpoint, *state.NextStageGrant); err != nil {
				return false, err
			}
		default:
			if input.NextStageGrant != nil {
				return false, fail(CheckpointChainInvalid, "%s cannot replace the durable stage grant", input.Checkpoint.Kind)
			}
		}
		checkpoint := input.Checkpoint
		state.CheckpointTip = &checkpoint
		if input.NextStageGrant != nil {
			grant := *input.NextStageGrant
			state.NextStageGrant = &grant
		}
		return true, nil
	})
}

func validateCheckpointGrantBinding(checkpoint PhaseCheckpointV1, grant NextStageGrantV1, stage contextcapsule.Stage) error {
	if err := ValidateNextStageGrantV1(grant); err != nil {
		return err
	}
	parent, err := checkpointGrantParentDigest(checkpoint)
	if err != nil {
		return err
	}
	if grant.Stage != stage || checkpoint.NextStageGrantSHA256 != grant.GrantSHA256 || grant.ParentCheckpointSHA256 != parent || grant.BaseSHA != checkpoint.CandidateSHA {
		return fail(CheckpointChainInvalid, "checkpoint does not bind the exact next-stage grant")
	}
	return nil
}

// AdmitWorkflowAuthority records pre-activation V2 issuance and, after
// activation, accepts only the exact controller-recorded grandfather set for
// the repository physically owned by this controller.
func (c *ControllerV1) AdmitWorkflowAuthority(repository, repositoryIdentity, policyVersion, authorityDigest string) error {
	if !validSHA256(authorityDigest) {
		return fail(CapsuleLineageInvalid, "authority digest is invalid")
	}
	if err := c.validateRepository(repository, repositoryIdentity); err != nil {
		return err
	}
	return c.withState(func(state *ControllerStateV1) (bool, error) {
		bound, err := c.bindRepositoryState(state, repositoryIdentity)
		if err != nil {
			return false, err
		}
		if state.Activation != nil {
			if policyVersion == contextcapsule.PolicyVersionV3 {
				return bound, nil
			}
			if policyVersion != contextcapsule.PolicyVersionV2 {
				return false, fail(CapsuleLineageInvalid, "A/B/C workflow requires V3 after activation")
			}
			for _, issued := range state.IssuedV2Authorities {
				if issued.AuthoritySHA256 == authorityDigest && issued.Sequence < state.Activation.ActivationSequence && stringSubset([]string{authorityDigest}, state.Activation.GrandfatheredV2Digests) {
					return bound, nil
				}
			}
			return false, fail(CapsuleLineageInvalid, "V2 authority was not durably issued and grandfathered before activation")
		}
		if policyVersion != contextcapsule.PolicyVersionV2 {
			return bound, nil
		}
		for _, issued := range state.IssuedV2Authorities {
			if issued.AuthoritySHA256 == authorityDigest {
				return bound, nil
			}
		}
		state.NextAuthoritySequence++
		state.IssuedV2Authorities = append(state.IssuedV2Authorities, IssuedAuthorityV1{AuthoritySHA256: authorityDigest, Sequence: state.NextAuthoritySequence})
		sort.Slice(state.IssuedV2Authorities, func(i, j int) bool {
			return state.IssuedV2Authorities[i].AuthoritySHA256 < state.IssuedV2Authorities[j].AuthoritySHA256
		})
		return true, nil
	})
}

func (c *ControllerV1) validateRepository(repository, identity string) error {
	if c == nil || c.identity == "" || c.repositoryIdentity == "" || c.repositoryControllerKey == "" || !validText(identity, 512) {
		return fail(CapsuleLineageInvalid, "repository controller identity is invalid")
	}
	root, err := canonicalRepository(repository)
	if err != nil || !repositoryIdentityMatches(root, identity) {
		return fail(CapsuleLineageInvalid, "repository identity is not controller-owned")
	}
	repositoryIdentity, err := repositoryControllerIdentity(root)
	if err != nil || repositoryIdentity != identity || repositoryIdentity != c.repositoryIdentity {
		return fail(CapsuleLineageInvalid, "repository origin identity differs from durable controller identity")
	}
	repositoryKey, err := repositoryControllerKey(repositoryIdentity)
	if err != nil || repositoryKey != c.repositoryControllerKey {
		return fail(CapsuleLineageInvalid, "repository origin belongs to a different governance controller")
	}
	return nil
}

func (c *ControllerV1) bindRepositoryState(state *ControllerStateV1, identity string) (bool, error) {
	if state.ControllerIdentity != c.identity {
		return false, fail(ExecutionBoundsInvalid, "durable state was copied from a different repository controller")
	}
	if state.RepositoryIdentity == "" {
		state.RepositoryIdentity = identity
		return true, nil
	}
	if state.RepositoryIdentity != identity {
		return false, fail(CapsuleLineageInvalid, "durable controller repository identity changed")
	}
	return false, nil
}

func (c *ControllerV1) bindCapsuleRepository(state *ControllerStateV1, capsule contextcapsule.Capsule) (bool, error) {
	if err := c.validateRepository(c.repository, capsule.Repository); err != nil {
		return false, err
	}
	return c.bindRepositoryState(state, capsule.Repository)
}

// InstallActivationV1 records the one durable activation tip. Its grandfather
// set must already exist in controller issuance state.
func (c *ControllerV1) InstallActivationV1(repository, repositoryIdentity string, activation GovernanceActivationV1) error {
	if err := ValidateGovernanceActivationV1(activation); err != nil {
		return err
	}
	if err := c.validateRepository(repository, repositoryIdentity); err != nil {
		return err
	}
	return c.withState(func(state *ControllerStateV1) (bool, error) {
		bound, err := c.bindRepositoryState(state, repositoryIdentity)
		if err != nil {
			return false, err
		}
		if state.Activation != nil {
			if state.Activation.ActivationSHA256 == activation.ActivationSHA256 {
				return bound, nil
			}
			return false, fail(CapsuleLineageInvalid, "a competing governance activation tip already exists")
		}
		if activation.ActivationSequence != state.NextAuthoritySequence+1 {
			return false, fail(CapsuleLineageInvalid, "activation sequence does not extend controller issuance state")
		}
		issued := make(map[string]uint64, len(state.IssuedV2Authorities))
		for _, authority := range state.IssuedV2Authorities {
			issued[authority.AuthoritySHA256] = authority.Sequence
		}
		for _, digest := range activation.GrandfatheredV2Digests {
			sequence, ok := issued[digest]
			if !ok || sequence >= activation.ActivationSequence {
				return false, fail(CapsuleLineageInvalid, "activation grandfathers a V2 digest without durable pre-activation issuance")
			}
		}
		copy := activation
		state.Activation = &copy
		return true, nil
	})
}

// ReserveRalphexInvocationV1 atomically consumes one B-wide invocation slot
// and, for mutation, one fix-batch slot backed by the exact CONSUMING lease.
func (c *ControllerV1) ReserveRalphexInvocationV1(capsule contextcapsule.Capsule, operation contextcapsule.OperationKind, mutation bool, leaseSHA256 string) (InvocationReservationV1, error) {
	var reservation InvocationReservationV1
	if capsule.PolicyVersion != contextcapsule.PolicyVersionV3 || capsule.PhaseAuthority == nil || capsule.PhaseAuthority.Stage != contextcapsule.StageBImplementation || capsule.PhaseAuthority.ExecutionBounds == nil {
		return reservation, fail(ExecutionBoundsInvalid, "durable invocation reservation requires B context-capsule-v3")
	}
	if err := validateCapsuleUsageV3(capsule, operation, mutation); err != nil {
		return reservation, err
	}
	bounds := *capsule.PhaseAuthority.ExecutionBounds
	boundsDigest, err := digestJSON(bounds)
	if err != nil {
		return reservation, err
	}
	err = c.withState(func(state *ControllerStateV1) (bool, error) {
		if _, err := c.bindCapsuleRepository(state, capsule); err != nil {
			return false, err
		}
		if err := bindBWorkflowState(state, capsule.CapsuleSHA256, boundsDigest); err != nil {
			return false, err
		}
		if state.ActiveInvocation != nil {
			return false, fail(ExecutionBoundsInvalid, "an invocation is already durably in flight")
		}
		if err := ralphex.ValidateExecutionStateV1(bounds, state.ExecutionState); err != nil {
			return false, fail(ExecutionBoundsInvalid, "%v", err)
		}
		if state.ExecutionState.RalphexInvocations >= bounds.MaxRalphexInvocations {
			return false, fail(ExecutionBoundsInvalid, "B-wide Ralphex invocation ceiling is exhausted")
		}
		fixBatch := operation == contextcapsule.OperationImplementationReview && mutation
		if operation != contextcapsule.OperationImplementation && operation != contextcapsule.OperationImplementationReview {
			return false, fail(CapsuleUsageInvalid, "Ralphex reservation operation is outside B")
		}
		if fixBatch {
			if state.Lease == nil || state.MutationState == nil || state.Lease.LeaseSHA256 != leaseSHA256 || state.MutationState.LeaseTipSHA256 != leaseSHA256 || state.MutationState.LeaseStatus != LeaseConsuming {
				return false, fail(MutationScopeViolation, "mutation has no exact controller-owned CONSUMING lease")
			}
			if state.ExecutionState.TotalFixBatches >= bounds.MaxTotalFixBatches {
				return false, fail(ExecutionBoundsInvalid, "B-wide fix-batch ceiling is exhausted")
			}
			if state.FixBatchLeaseSHA256 != "" {
				return false, fail(MutationScopeViolation, "a fix batch was already reserved for the CONSUMING lease")
			}
			state.ExecutionState.TotalFixBatches++
			state.FixBatchLeaseSHA256 = leaseSHA256
		} else if leaseSHA256 != "" {
			return false, fail(MutationScopeViolation, "read-only invocation supplied a mutation lease")
		}
		state.ExecutionState.RalphexInvocations++
		started := time.Now().UTC().Format(time.RFC3339Nano)
		token, digestErr := digestJSON(struct {
			Capsule  string `json:"capsule"`
			Revision uint64 `json:"revision"`
			Started  string `json:"started"`
			Lease    string `json:"lease,omitempty"`
		}{capsule.CapsuleSHA256, state.Revision + 1, started, leaseSHA256})
		if digestErr != nil {
			return false, digestErr
		}
		reservation = InvocationReservationV1{Token: token, CapsuleSHA256: capsule.CapsuleSHA256, StartedAt: started, MutationLeaseSHA256: leaseSHA256}
		state.ActiveInvocation = &reservation
		return true, nil
	})
	return reservation, err
}

// FinishRalphexInvocationV1 atomically accrues real elapsed wall time. Only
// the exact active token can finish, so another process cannot reset time.
func (c *ControllerV1) FinishRalphexInvocationV1(reservation InvocationReservationV1) error {
	return c.withState(func(state *ControllerStateV1) (bool, error) {
		if state.ActiveInvocation == nil || state.ActiveInvocation.Token != reservation.Token || state.ActiveInvocation.CapsuleSHA256 != reservation.CapsuleSHA256 || state.ActiveInvocation.StartedAt != reservation.StartedAt || state.ActiveInvocation.MutationLeaseSHA256 != reservation.MutationLeaseSHA256 {
			return false, fail(ExecutionBoundsInvalid, "invocation reservation is absent, replayed, or forked")
		}
		started, err := time.Parse(time.RFC3339Nano, state.ActiveInvocation.StartedAt)
		if err != nil {
			return false, fail(ExecutionBoundsInvalid, "durable invocation start time is invalid")
		}
		prior, err := time.ParseDuration(state.ExecutionState.AggregateElapsed)
		if err != nil || prior < 0 {
			return false, fail(ExecutionBoundsInvalid, "durable aggregate elapsed time is invalid")
		}
		elapsed := time.Since(started)
		if elapsed < 0 {
			return false, fail(ExecutionBoundsInvalid, "controller clock moved before the invocation reservation")
		}
		state.ExecutionState.AggregateElapsed = (prior + elapsed).Round(time.Nanosecond).String()
		state.ActiveInvocation = nil
		return true, nil
	})
}

// AdvanceReviewTipV1 validates the exact repository candidate under the lock,
// consumes one report slot, and advances the sole durable review tip.
func (c *ControllerV1) AdvanceReviewTipV1(repository string, capsule contextcapsule.Capsule, capsuleFileSHA256 string, registry SemanticAuthorityRegistryV1, report ReviewScopeReportV1) error {
	if capsule.PhaseAuthority == nil || capsule.PhaseAuthority.Stage != contextcapsule.StageBImplementation && capsule.PhaseAuthority.Stage != contextcapsule.StageCAcceptanceMerge {
		return fail(ReviewChainInvalid, "durable review advancement requires B or C authority")
	}
	if err := c.validateRepository(repository, capsule.Repository); err != nil {
		return err
	}
	return c.withState(func(state *ControllerStateV1) (bool, error) {
		bound, err := c.bindCapsuleRepository(state, capsule)
		if err != nil {
			return false, err
		}
		var bounds *contextcapsule.ExecutionBoundsV1
		var previous *ReviewScopeReportV1
		if capsule.PhaseAuthority.Stage == contextcapsule.StageBImplementation {
			if capsule.PhaseAuthority.ExecutionBounds == nil {
				return false, fail(ReviewChainInvalid, "durable B review advancement requires execution bounds")
			}
			value := *capsule.PhaseAuthority.ExecutionBounds
			bounds = &value
			boundsDigest, digestErr := digestJSON(value)
			if digestErr != nil {
				return false, digestErr
			}
			if err := bindBWorkflowState(state, capsule.CapsuleSHA256, boundsDigest); err != nil {
				return false, err
			}
			if state.ReviewCapsuleSHA256 == capsule.CapsuleSHA256 {
				previous = state.ReviewTip
			} else if state.ReviewTip != nil {
				return false, fail(ReviewChainInvalid, "B review cannot replace another durable review chain")
			}
		} else {
			if state.CheckpointTip == nil || state.CheckpointTip.Kind != CheckpointAcceptancePassed || state.NextStageGrant == nil || capsule.PhaseAuthority.Parent == nil || state.CheckpointTip.CandidateSHA != capsule.BaseSHA || state.CheckpointTip.PredecessorCheckpointSHA256 != capsule.PhaseAuthority.Parent.CheckpointSHA256 || state.NextStageGrant.GrantSHA256 != capsule.PhaseAuthority.Parent.GrantSHA256 {
				return false, fail(FinalReviewInvalidated, "C final review requires the durable exact-head acceptance checkpoint")
			}
			if state.ReviewCapsuleSHA256 == capsule.CapsuleSHA256 {
				previous = state.ReviewTip
			} else {
				previous = nil
			}
		}
		if _, err := ValidateCandidateV1(repository, capsule, report.ReviewedPreFixHEAD); err != nil {
			return false, err
		}
		head, err := gitText(repository, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil || head != report.ReviewedPreFixHEAD {
			return false, fail(ReviewChainInvalid, "review report does not name the controller's exact current candidate")
		}
		if err := ValidateReviewScopeReportV1(capsule, capsuleFileSHA256, registry, state.FindingEvidence, previous, report); err != nil {
			return false, err
		}
		if previous != nil && previous.ReportSHA256 == report.ReportSHA256 {
			return bound, nil
		}
		if bounds != nil {
			if state.ExecutionState.ReviewReports >= bounds.MaxReviewReports {
				return false, fail(ExecutionBoundsInvalid, "B-wide review-report ceiling is exhausted")
			}
			state.ExecutionState.ReviewReports++
		}
		copy := report
		state.ReviewTip = &copy
		state.ReviewCapsuleSHA256 = capsule.CapsuleSHA256
		return true, nil
	})
}

// IssueMutationLeaseV1 validates against the durable report tip and persists
// the sole ISSUED lease in the same atomic state transition.
func (c *ControllerV1) IssueMutationLeaseV1(capsule contextcapsule.Capsule, registry SemanticAuthorityRegistryV1, report ReviewScopeReportV1, limits MutationLimitsV1) (MutationLeaseV1, error) {
	var issued MutationLeaseV1
	if capsule.PhaseAuthority == nil || capsule.PhaseAuthority.ExecutionBounds == nil {
		return issued, fail(MutationScopeViolation, "durable lease issuance requires B execution bounds")
	}
	bounds := *capsule.PhaseAuthority.ExecutionBounds
	boundsDigest, err := digestJSON(bounds)
	if err != nil {
		return issued, err
	}
	err = c.withState(func(state *ControllerStateV1) (bool, error) {
		if _, err := c.bindCapsuleRepository(state, capsule); err != nil {
			return false, err
		}
		if err := bindBWorkflowState(state, capsule.CapsuleSHA256, boundsDigest); err != nil {
			return false, err
		}
		if state.ReviewTip == nil || state.ReviewCapsuleSHA256 != capsule.CapsuleSHA256 || state.ReviewTip.ReportSHA256 != report.ReportSHA256 {
			return false, fail(ReviewChainInvalid, "lease report is not the exact durable review tip")
		}
		if state.Lease != nil && state.MutationState != nil && state.MutationState.LeaseStatus != LeaseConsumed {
			return false, fail(MutationScopeViolation, "a prior mutation lease remains ISSUED or CONSUMING")
		}
		if state.Lease != nil && state.Lease.ReportSHA256 == report.ReportSHA256 {
			return false, fail(MutationScopeViolation, "the durable review tip already consumed its one-use lease authority")
		}
		if state.ExecutionState.MutationLeases >= bounds.MaxMutationLeases {
			return false, fail(ExecutionBoundsInvalid, "B-wide mutation-lease ceiling is exhausted")
		}
		lease, err := IssueMutationLeaseV1(capsule, registry, state.FindingEvidence, report, limits)
		if err != nil {
			return false, err
		}
		receiptTip := ""
		if state.MutationState != nil {
			receiptTip = state.MutationState.ReceiptTipSHA256
		}
		mutation, err := NewMutationStateV1(lease, receiptTip)
		if err != nil {
			return false, err
		}
		state.ExecutionState.MutationLeases++
		state.Lease = &lease
		state.MutationState = &mutation
		issued = lease
		return true, nil
	})
	return issued, err
}

func bindBWorkflowState(state *ControllerStateV1, capsuleSHA256, boundsSHA256 string) error {
	if !validSHA256(capsuleSHA256) || !validSHA256(boundsSHA256) {
		return fail(ExecutionBoundsInvalid, "B workflow identity is invalid")
	}
	if state.BCapsuleSHA256 == "" {
		state.BCapsuleSHA256 = capsuleSHA256
		state.ExecutionBoundsSHA256 = boundsSHA256
		state.ExecutionState.AggregateElapsed = "0s"
		return nil
	}
	if state.BCapsuleSHA256 != capsuleSHA256 || state.ExecutionBoundsSHA256 != boundsSHA256 {
		return fail(ExecutionBoundsInvalid, "B capsule or cumulative bounds changed for the workflow")
	}
	return nil
}

// BeginMutationLeaseV1 performs the durable ISSUED->CONSUMING CAS. The lease
// body comes from controller state; only its digest is accepted from a caller.
func (c *ControllerV1) BeginMutationLeaseV1(leaseSHA256 string) error {
	return c.withState(func(state *ControllerStateV1) (bool, error) {
		if state.Lease == nil || state.MutationState == nil || state.Lease.LeaseSHA256 != leaseSHA256 {
			return false, fail(MutationScopeViolation, "lease digest is not the durable controller tip")
		}
		if err := BeginMutationLeaseV1(state.MutationState, *state.Lease); err != nil {
			return false, err
		}
		return true, nil
	})
}

// ValidateCapsuleUsageV3 admits leased mutation only from the controller's
// exact durable CONSUMING tip; a manifest digest is never proof by itself.
func (c *ControllerV1) ValidateCapsuleUsageV3(capsule contextcapsule.Capsule, operation contextcapsule.OperationKind, mutation bool, leaseSHA256 string) error {
	if err := validateCapsuleUsageV3(capsule, operation, mutation); err != nil {
		return err
	}
	return c.withState(func(state *ControllerStateV1) (bool, error) {
		bound, err := c.bindCapsuleRepository(state, capsule)
		if err != nil {
			return false, err
		}
		if operation != contextcapsule.OperationImplementationReview || !mutation {
			if leaseSHA256 != "" {
				return false, fail(MutationScopeViolation, "non-fix invocation supplied a mutation lease")
			}
			return bound, nil
		}
		if state.Lease == nil || state.MutationState == nil || state.Lease.LeaseSHA256 != leaseSHA256 || state.Lease.CapsuleSHA256 != capsule.CapsuleSHA256 || state.MutationState.LeaseTipSHA256 != leaseSHA256 || state.MutationState.LeaseStatus != LeaseConsuming {
			return false, fail(MutationScopeViolation, "mutation has no exact durable CONSUMING lease tip")
		}
		return bound, nil
	})
}

// CompleteMutationReceiptV1 derives repository proof itself and atomically
// advances both receipt tip and lease status to CONSUMED.
func (c *ControllerV1) CompleteMutationReceiptV1(repository string, capsule contextcapsule.Capsule, leaseSHA256, candidateSHA string) (MutationReceiptV1, error) {
	var completed MutationReceiptV1
	if err := c.validateRepository(repository, capsule.Repository); err != nil {
		return completed, err
	}
	err := c.withState(func(state *ControllerStateV1) (bool, error) {
		if _, err := c.bindCapsuleRepository(state, capsule); err != nil {
			return false, err
		}
		if state.Lease == nil || state.MutationState == nil || state.Lease.LeaseSHA256 != leaseSHA256 || state.MutationState.LeaseStatus != LeaseConsuming || state.FixBatchLeaseSHA256 != leaseSHA256 {
			return false, fail(MutationScopeViolation, "receipt has no exact durable CONSUMING lease")
		}
		proof, err := ValidateMutationCandidateV1(repository, capsule, *state.Lease, candidateSHA)
		if err != nil {
			return false, err
		}
		receipt := MutationReceiptV1{
			Kind: "MutationReceiptV1", LeaseSHA256: state.Lease.LeaseSHA256, ReportSHA256: state.Lease.ReportSHA256,
			PreFixHEAD: state.Lease.PreFixHEAD, ResultHEAD: proof.CandidateSHA, ChangedPaths: proof.ChangedPaths,
			ChangedPathSHA256: proof.ChangedPathSHA256, ChangedFiles: proof.ChangedFiles, ChangedBytes: proof.ChangedBytes,
			PredecessorReceiptSHA256: state.MutationState.ReceiptTipSHA256,
		}
		receipt, err = SealMutationReceiptV1(*state.Lease, *state.MutationState, proof, receipt)
		if err != nil {
			return false, err
		}
		mutation := *state.MutationState
		if err := ValidateMutationReceiptV1(*state.Lease, &mutation, proof, receipt); err != nil {
			return false, err
		}
		state.MutationState = &mutation
		state.FixBatchLeaseSHA256 = ""
		completed = receipt
		return true, nil
	})
	return completed, err
}

func (c *ControllerV1) withState(update func(*ControllerStateV1) (bool, error)) error {
	if c == nil || c.identity == "" || c.repository == "" || c.repositoryIdentity == "" || c.repositoryControllerKey == "" {
		return fail(ExecutionBoundsInvalid, "durable governance controller is required")
	}
	if c.backend == nil || c.authorityDomain == "" {
		return fail(ExecutionBoundsInvalid, "workflow-wide authority backend is required; host-local governance state is not authoritative")
	}
	domain, err := c.backend.AuthorityDomainV1()
	if err != nil || domain != c.authorityDomain {
		return fail(ExecutionBoundsInvalid, "workflow-wide authority backend identity is unavailable or changed")
	}
	for attempt := 0; attempt < 32; attempt++ {
		state, revision, err := c.loadState()
		if err != nil {
			return err
		}
		changed, err := update(&state)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		if revision == ^uint64(0) {
			return fail(ExecutionBoundsInvalid, "workflow-wide authority revision is exhausted")
		}
		state.Revision = revision + 1
		swapped, err := c.saveState(revision, state)
		if err != nil {
			return err
		}
		if swapped {
			return nil
		}
	}
	return fail(ExecutionBoundsInvalid, "workflow-wide authority CAS contention exceeded the bounded retry limit")
}

func (c *ControllerV1) loadState() (ControllerStateV1, uint64, error) {
	data, revision, err := c.backend.LoadWorkflowStateV1(c.identity)
	if err != nil {
		return ControllerStateV1{}, 0, fail(ExecutionBoundsInvalid, "load workflow-wide authority state: %v", err)
	}
	if len(data) == 0 || revision == 0 {
		return ControllerStateV1{}, 0, fail(ExecutionBoundsInvalid, "workflow-wide authority state is missing or uninitialized")
	}
	var state ControllerStateV1
	if err := ParseCanonical(data, &state); err != nil {
		return ControllerStateV1{}, 0, fail(ExecutionBoundsInvalid, "decode workflow-wide authority state: %v", err)
	}
	if state.Kind != "GovernanceControllerStateV1" || state.ControllerIdentity != c.identity || state.RepositoryIdentity != c.repositoryIdentity || state.Revision != revision || state.IssuedV2Authorities == nil || state.FindingEvidence == nil || state.ExecutionState.AggregateElapsed == "" {
		return ControllerStateV1{}, 0, fail(ExecutionBoundsInvalid, "workflow-wide authority state payload is invalid or divergent")
	}
	return state, revision, nil
}

func (c *ControllerV1) saveState(expectedRevision uint64, state ControllerStateV1) (bool, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return false, fail(ExecutionBoundsInvalid, "encode workflow-wide authority state: %v", err)
	}
	if len(data) == 0 || len(data) > 1<<20 {
		return false, fail(ExecutionBoundsInvalid, "workflow-wide authority state exceeds the bounded canonical size")
	}
	swapped, err := c.backend.CompareAndSwapWorkflowStateV1(c.identity, expectedRevision, data)
	if err != nil {
		return false, fail(ExecutionBoundsInvalid, "compare-and-swap workflow-wide authority state: %v", err)
	}
	return swapped, nil
}

func validateLifecycle(kind CheckpointKind, lifecycle *LifecycleBindingV1, candidate string) error {
	if lifecycle == nil || !validText(lifecycle.Repository, 512) || !validText(lifecycle.PRIdentity, 512) || !validText(lifecycle.BaseBranch, 256) || !validOID(lifecycle.ExpectedBaseOID) || lifecycle.HeadOID != candidate {
		return fail(CheckpointChainInvalid, "%s lacks exact lifecycle repository/PR/base/head binding", kind)
	}
	if kind == CheckpointMergeAuthorized {
		if !validText(lifecycle.MergeMethod, 64) || !validText(lifecycle.ActingPrincipal, 512) || !validSHA256(lifecycle.PolicyEvidenceSHA256) || !validSHA256(lifecycle.ProviderEvidenceSHA256) {
			return fail(CheckpointChainInvalid, "MERGE_AUTHORIZED lacks EP-005 policy/principal/provider binding")
		}
		if lifecycle.ExpectedResultOID != "" && !validOID(lifecycle.ExpectedResultOID) {
			return fail(CheckpointChainInvalid, "expected result OID is invalid")
		}
	}
	return nil
}

func validateOperations(values []contextcapsule.OperationKind) error {
	if values == nil || len(values) == 0 {
		return fail(CapsuleLineageInvalid, "operation arrays must be non-empty")
	}
	previous := ""
	for _, value := range values {
		current := string(value)
		if current <= previous {
			return fail(CapsuleLineageInvalid, "operation arrays must be sorted and unique")
		}
		switch value {
		case contextcapsule.OperationDesignPlanning, contextcapsule.OperationDesignReview,
			contextcapsule.OperationImplementation, contextcapsule.OperationImplementationReview,
			contextcapsule.OperationAcceptance, contextcapsule.OperationFinalReview,
			contextcapsule.OperationPRPublication, contextcapsule.OperationMergeAuthorization,
			contextcapsule.OperationPostMergeAcceptance:
		default:
			return fail(CapsuleLineageInvalid, "operation %q is not in the V3 vocabulary", value)
		}
		previous = current
	}
	return nil
}

func validateIDs(values []string) error {
	if values == nil || len(values) > 256 {
		return errors.New("must be an explicit bounded array")
	}
	previous := ""
	for _, value := range values {
		if !validID(value) || value <= previous {
			return errors.New("must contain sorted unique conservative identifiers")
		}
		previous = value
	}
	return nil
}

func validatePaths(paths []string, allowEmpty bool) error {
	if paths == nil || !allowEmpty && len(paths) == 0 || len(paths) > 256 {
		return errors.New("must be an explicit bounded path array")
	}
	previous := ""
	for _, path := range paths {
		if err := contextcapsule.ValidateAllowedPathV3(path); err != nil {
			return err
		}
		if path <= previous {
			return errors.New("paths must be sorted and unique")
		}
		previous = path
	}
	return nil
}

func validateEvidenceRefs(refs []string) error {
	if len(refs) == 0 || len(refs) > 64 {
		return errors.New("evidence refs must be non-empty and bounded")
	}
	previous := ""
	for _, ref := range refs {
		if !validText(ref, 2048) || ref <= previous {
			return errors.New("evidence refs must be sorted and unique")
		}
		previous = ref
	}
	return nil
}

func validateEvidenceBindings(bindings []EvidenceBindingV1) error {
	if len(bindings) == 0 || len(bindings) > 64 {
		return errors.New("evidence bindings must be non-empty and bounded")
	}
	previous := ""
	for _, binding := range bindings {
		if !validText(binding.Ref, 2048) || !validSHA256(binding.SHA256) || binding.Ref <= previous {
			return errors.New("evidence bindings must be sorted, unique, and content-addressed")
		}
		previous = binding.Ref
	}
	return nil
}

func validText(value string, maximum int) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\x00\r\n") && len(value) <= maximum
}

func validID(value string) bool {
	if len(value) < 1 || len(value) > 128 || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') && !strings.ContainsRune("._:-", character) {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validOID(value string) bool {
	if value != strings.ToLower(value) || len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func digestJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// ParseCanonical decodes one strict canonical JSON record into target.
func ParseCanonical(data []byte, target any) error {
	if len(data) == 0 || len(data) > 1<<20 {
		return errors.New("canonical JSON input is empty or exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("canonical JSON contains trailing data")
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, canonical) {
		return errors.New("JSON is not strict canonical encoding")
	}
	return nil
}

func canonicalRepository(repository string) (string, error) {
	absolute, err := filepath.Abs(repository)
	if err != nil {
		return "", err
	}
	root, err := gitText(absolute, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	requested, err := filepath.EvalSymlinks(absolute)
	if err != nil || filepath.Clean(root) != filepath.Clean(requested) {
		return "", errors.New("repository must be the canonical Git root")
	}
	return root, nil
}

func repositoryControllerIdentity(repository string) (string, error) {
	remoteURLs, err := gitText(repository, "remote", "get-url", "--all", "origin")
	if err != nil || remoteURLs == "" {
		return "", errors.New("canonical origin remote is required")
	}
	identities := make([]string, 0, 2)
	seen := make(map[string]struct{})
	for _, remoteURL := range strings.Split(remoteURLs, "\n") {
		identity, err := controllerRemoteRepositoryIdentity(repository, remoteURL)
		if err != nil {
			return "", err
		}
		if _, duplicate := seen[identity]; duplicate {
			continue
		}
		seen[identity] = struct{}{}
		identities = append(identities, identity)
	}
	if len(identities) == 0 {
		return "", errors.New("canonical origin remote is empty")
	}
	sort.Strings(identities)
	if len(identities) != 1 {
		return "", errors.New("origin remote URLs do not resolve to one repository identity")
	}
	return identities[0], nil
}

func repositoryControllerKey(repositoryIdentity string) (string, error) {
	if !validText(repositoryIdentity, 512) {
		return "", errors.New("repository controller identity is invalid")
	}
	return digestJSON(struct {
		Kind               string `json:"kind"`
		RepositoryIdentity string `json:"repository_identity"`
	}{Kind: "RepositoryControllerKeyV1", RepositoryIdentity: repositoryIdentity})
}

func controllerRemoteRepositoryIdentity(repository, remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	if remote == "" || strings.ContainsAny(remote, "\r\n") {
		return "", errors.New("origin remote identity is invalid")
	}
	if parsed, err := url.Parse(remote); err == nil && parsed.Scheme != "" {
		if parsed.Path == "" {
			return "", errors.New("origin remote URL is incomplete")
		}
		identity := strings.Trim(strings.ReplaceAll(parsed.Path, "\\", "/"), "/")
		identity = strings.TrimSuffix(identity, ".git")
		if identity == "" {
			return "", errors.New("origin remote repository identity is empty")
		}
		return identity, nil
	}
	if colon := strings.IndexByte(remote, ':'); colon > 0 && !strings.Contains(remote[:colon], "/") {
		identity := strings.Trim(strings.ReplaceAll(remote[colon+1:], "\\", "/"), "/")
		identity = strings.TrimSuffix(identity, ".git")
		if identity == "" {
			return "", errors.New("origin scp-style remote is incomplete")
		}
		return identity, nil
	}
	if !filepath.IsAbs(remote) {
		remote = filepath.Join(repository, remote)
	}
	absolute, err := filepath.Abs(remote)
	if err != nil {
		return "", err
	}
	if canonical, evalErr := filepath.EvalSymlinks(absolute); evalErr == nil {
		absolute = canonical
	}
	identity := strings.Trim(filepath.ToSlash(filepath.Clean(absolute)), "/")
	identity = strings.TrimSuffix(identity, ".git")
	if identity == "" {
		return "", errors.New("origin file repository identity is empty")
	}
	return identity, nil
}

func repositoryIdentityMatches(repository, identity string) bool {
	names, err := gitText(repository, "remote")
	if err != nil {
		return false
	}
	for _, name := range strings.Fields(names) {
		urls, err := gitText(repository, "remote", "get-url", "--all", name)
		if err != nil {
			return false
		}
		for _, remoteURL := range strings.Split(urls, "\n") {
			path := remoteURL
			if parsed, parseErr := url.Parse(remoteURL); parseErr == nil && parsed.Scheme != "" {
				path = parsed.Path
			} else if colon := strings.IndexByte(remoteURL, ':'); colon >= 0 && !strings.Contains(remoteURL[:colon], "/") {
				path = remoteURL[colon+1:]
			}
			path = strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
			if strings.TrimSuffix(path, ".git") == identity {
				return true
			}
		}
	}
	return false
}

func gitRun(repository string, arguments ...string) error {
	command := exec.Command("git", arguments...)
	command.Dir = repository
	command.Env = gitexec.Environment()
	return command.Run()
}

func gitBytes(repository string, arguments ...string) ([]byte, error) {
	command := exec.Command("git", arguments...)
	command.Dir = repository
	command.Env = gitexec.Environment()
	return command.Output()
}

func gitText(repository string, arguments ...string) (string, error) {
	output, err := gitBytes(repository, arguments...)
	return strings.TrimSpace(string(output)), err
}

func unilateralPathIntersection(left, right string) (string, bool) {
	leftPrefix, leftTree := strings.CutSuffix(left, "/**")
	rightPrefix, rightTree := strings.CutSuffix(right, "/**")
	switch {
	case !leftTree && !rightTree:
		return left, left == right
	case leftTree && !rightTree:
		return right, right == leftPrefix || strings.HasPrefix(right, leftPrefix+"/")
	case !leftTree && rightTree:
		return left, left == rightPrefix || strings.HasPrefix(left, rightPrefix+"/")
	default:
		if leftPrefix == rightPrefix || strings.HasPrefix(leftPrefix, rightPrefix+"/") {
			return left, true
		}
		if strings.HasPrefix(rightPrefix, leftPrefix+"/") {
			return right, true
		}
	}
	return "", false
}

func intersectPaths(left, right []string) []string {
	values := make(map[string]struct{})
	for _, a := range left {
		for _, b := range right {
			if intersection, ok := unilateralPathIntersection(a, b); ok {
				values[intersection] = struct{}{}
			}
		}
	}
	return mapKeys(values)
}

func pathAllowed(path string, allowed []string) bool {
	for _, rule := range allowed {
		prefix, tree := strings.CutSuffix(rule, "/**")
		if path == rule || tree && (path == prefix || strings.HasPrefix(path, prefix+"/")) {
			return true
		}
	}
	return false
}

func pathSubset(subset, superset []string) bool {
	for _, path := range subset {
		if _, ok := firstPathContaining(path, superset); !ok {
			return false
		}
	}
	return true
}

func firstPathContaining(path string, rules []string) (string, bool) {
	pathPrefix, pathTree := strings.CutSuffix(path, "/**")
	for _, rule := range rules {
		rulePrefix, ruleTree := strings.CutSuffix(rule, "/**")
		if pathTree {
			if ruleTree && (pathPrefix == rulePrefix || strings.HasPrefix(pathPrefix, rulePrefix+"/")) {
				return rule, true
			}
		} else if pathAllowed(path, []string{rule}) {
			return rule, true
		}
	}
	return "", false
}

func stringSubset(subset, superset []string) bool {
	set := make(map[string]struct{}, len(superset))
	for _, value := range superset {
		set[value] = struct{}{}
	}
	for _, value := range subset {
		if _, ok := set[value]; !ok {
			return false
		}
	}
	return true
}

func operationSubset(subset, superset []contextcapsule.OperationKind) bool {
	left := make([]string, len(subset))
	right := make([]string, len(superset))
	for index := range subset {
		left[index] = string(subset[index])
	}
	for index := range superset {
		right[index] = string(superset[index])
	}
	return stringSubset(left, right)
}

func boundsTighten(child, parent contextcapsule.ExecutionBoundsV1) bool {
	childDurations := []string{child.SessionTimeout, child.IdleTimeout, child.WallClockTimeout, child.AggregateWallClockTimeout}
	parentDurations := []string{parent.SessionTimeout, parent.IdleTimeout, parent.WallClockTimeout, parent.AggregateWallClockTimeout}
	for index := range childDurations {
		childValue, childErr := time.ParseDuration(childDurations[index])
		parentValue, parentErr := time.ParseDuration(parentDurations[index])
		if childErr != nil || parentErr != nil || childValue > parentValue {
			return false
		}
	}
	return !child.Finalize && child.MaxIterations <= parent.MaxIterations && child.MaxIncompleteTasks == 1 &&
		child.MaxInitialActiveFindings <= parent.MaxInitialActiveFindings && child.MaxRalphexInvocations <= parent.MaxRalphexInvocations &&
		child.MaxReviewReports <= parent.MaxReviewReports && child.MaxMutationLeases <= parent.MaxMutationLeases &&
		child.MaxTotalFixBatches <= parent.MaxTotalFixBatches && child.MaxChangedFiles <= parent.MaxChangedFiles && child.MaxChangedBytes <= parent.MaxChangedBytes
}

func registryMap(registry SemanticAuthorityRegistryV1) map[string]SemanticRuleV1 {
	result := make(map[string]SemanticRuleV1, len(registry.Entries))
	for _, entry := range registry.Entries {
		result[entry.RuleID] = entry
	}
	return result
}

func findingIDs(findings []ActiveFindingV1) []string {
	result := make([]string, len(findings))
	for index := range findings {
		result[index] = findings[index].FindingID
	}
	return result
}

func sortedUnique(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return mapKeys(set)
}

func mapKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func splitNUL(data []byte) ([]string, error) {
	if len(data) == 0 {
		return []string{}, nil
	}
	if data[len(data)-1] != 0 {
		return nil, errors.New("missing NUL terminator")
	}
	parts := bytes.Split(data[:len(data)-1], []byte{0})
	result := make([]string, len(parts))
	for index, part := range parts {
		if len(part) == 0 {
			return nil, errors.New("empty item")
		}
		result[index] = string(part)
	}
	return result, nil
}

func pathsRawForDigest(paths []string) []byte {
	var buffer bytes.Buffer
	for _, path := range paths {
		buffer.WriteString(path)
		buffer.WriteByte(0)
	}
	return buffer.Bytes()
}

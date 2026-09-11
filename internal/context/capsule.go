// Package context builds and verifies compact, deterministic context capsules.
package context

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
)

const (
	// PolicyVersionV1 is the historical deterministic context-capsule format.
	PolicyVersionV1 = "context-capsule-v1"
	// PolicyVersionV2 adds purpose-specific operation authority.
	PolicyVersionV2 = "context-capsule-v2"
	// PolicyVersionV3 adds immutable three-stage semantic authority.
	PolicyVersionV3 = "context-capsule-v3"
	// PolicyVersion remains the pre-activation default. A/B/C controllers select
	// V3 explicitly only under GovernanceActivationV1.
	PolicyVersion = PolicyVersionV2

	// Conservative bounds keep capsules small enough to be fresh-task context.
	MaxSources      = 32
	MaxOutcomes     = 32
	MaxListItems    = 32
	MaxStringBytes  = 2048
	MaxSourceBytes  = 4 << 20
	MaxCapsuleBytes = 64 << 10
)

// OperationKind identifies the governed purpose for one context capsule.
type OperationKind string

const (
	OperationDesignPlanning       OperationKind = "design-planning"
	OperationDesignReview         OperationKind = "design-review"
	OperationImplementation       OperationKind = "implementation"
	OperationImplementationReview OperationKind = "implementation-review"
	OperationAcceptance           OperationKind = "acceptance"
	OperationFinalReview          OperationKind = "final-review"
	OperationPRPublication        OperationKind = "pr-publication"
	OperationMergeAuthorization   OperationKind = "merge-authorization"
	OperationPostMergeAcceptance  OperationKind = "post-merge-acceptance"
	OperationDeployment           OperationKind = "deployment"
	OperationRecovery             OperationKind = "recovery"
	OperationMaintenance          OperationKind = "maintenance"
)

// Stage is one of the three immutable autonomous-development authority stages.
type Stage string

const (
	StageADesign          Stage = "A_DESIGN"
	StageBImplementation  Stage = "B_IMPLEMENTATION"
	StageCAcceptanceMerge Stage = "C_ACCEPTANCE_MERGE"
)

// ReviewProfile fixes how blocker authority is established for a B capsule.
type ReviewProfile string

const (
	ReviewProfileNone                  ReviewProfile = "NONE"
	ReviewProfileInitialImplementation ReviewProfile = "INITIAL_IMPLEMENTATION"
	ReviewProfileCorrection            ReviewProfile = "CORRECTION"
)

// PhaseParentV1 binds a child capsule to controller-owned predecessor evidence.
type PhaseParentV1 struct {
	CapsuleFileSHA256 string `json:"capsule_file_sha256"`
	CapsuleSHA256     string `json:"capsule_sha256"`
	Stage             Stage  `json:"stage"`
	CheckpointSHA256  string `json:"checkpoint_sha256"`
	CandidateSHA      string `json:"candidate_sha"`
	GrantSHA256       string `json:"grant_sha256"`
}

// ExecutionBoundsV1 contains per-invocation and B-lineage cumulative ceilings.
// Duration values use Go's canonical duration syntax and are canonicalized by
// Build before they are hashed.
type ExecutionBoundsV1 struct {
	MaxIterations             int    `json:"max_iterations"`
	SessionTimeout            string `json:"session_timeout"`
	IdleTimeout               string `json:"idle_timeout"`
	WallClockTimeout          string `json:"wall_clock_timeout"`
	AggregateWallClockTimeout string `json:"aggregate_wall_clock_timeout"`
	Finalize                  bool   `json:"finalize"`
	MaxIncompleteTasks        int    `json:"max_incomplete_tasks"`
	MaxInitialActiveFindings  int    `json:"max_initial_active_findings"`
	MaxRalphexInvocations     int    `json:"max_ralphex_invocations"`
	MaxReviewReports          int    `json:"max_review_reports"`
	MaxMutationLeases         int    `json:"max_mutation_leases"`
	MaxTotalFixBatches        int    `json:"max_total_fix_batches"`
	MaxChangedFiles           int    `json:"max_changed_files"`
	MaxChangedBytes           int64  `json:"max_changed_bytes"`
}

// PhaseAuthorityV3 is the immutable semantic and operational authority carried
// by a V3 capsule. All slices must already be canonical sorted unique arrays.
type PhaseAuthorityV3 struct {
	Stage                  Stage              `json:"stage"`
	AllowedOperations      []OperationKind    `json:"allowed_operations"`
	Parent                 *PhaseParentV1     `json:"parent,omitempty"`
	SemanticRegistrySHA256 string             `json:"semantic_registry_sha256"`
	ObservationScopeIDs    []string           `json:"observation_scope_ids"`
	BlockingScopeIDs       []string           `json:"blocking_scope_ids"`
	MutationScopeIDs       []string           `json:"mutation_scope_ids"`
	AuthorizedFindingIDs   []string           `json:"authorized_finding_ids"`
	AuthorizedInvariantIDs []string           `json:"authorized_invariant_ids"`
	AllowedPaths           []string           `json:"allowed_paths"`
	ReviewProfile          ReviewProfile      `json:"review_profile"`
	ExecutionBounds        *ExecutionBoundsV1 `json:"execution_bounds,omitempty"`
}

// OperationContext bounds one governed operation to an explicit purpose,
// owned scope, and blocking policy.
type OperationContext struct {
	Kind             OperationKind `json:"kind"`
	OwnedScope       []string      `json:"owned_scope"`
	BlockingCriteria []string      `json:"blocking_criteria"`
}

// Spec is the structured input used to build a Capsule. Sources contains only
// repository-relative paths; source contents are never copied into a capsule.
type Spec struct {
	PolicyVersion       string            `json:"policy_version"`
	Project             string            `json:"project"`
	Plan                string            `json:"plan"`
	RoadmapPhase        string            `json:"roadmap_phase"`
	ExecutionPack       string            `json:"execution_pack"`
	Task                string            `json:"task"`
	OperationContext    *OperationContext `json:"operation_context,omitempty"`
	PhaseAuthority      *PhaseAuthorityV3 `json:"phase_authority,omitempty"`
	Repository          string            `json:"repository"`
	BaseSHA             string            `json:"base_sha"`
	Invariants          []string          `json:"invariants"`
	NonGoals            []string          `json:"non_goals"`
	PredecessorOutcomes []Outcome         `json:"predecessor_outcomes"`
	Sources             []string          `json:"sources"`
}

// Source binds a repository-relative path to the SHA256 of its exact bytes.
type Source struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Outcome compactly records an accepted predecessor result.
type Outcome struct {
	Task      string `json:"task"`
	Summary   string `json:"summary"`
	CommitSHA string `json:"commit_sha"`
}

// Capsule is the compact, self-verifying context supplied to a fresh task.
// CapsuleSHA256 hashes the canonical JSON payload excluding that field.
type Capsule struct {
	PolicyVersion       string            `json:"policy_version"`
	Project             string            `json:"project"`
	Plan                string            `json:"plan"`
	RoadmapPhase        string            `json:"roadmap_phase"`
	ExecutionPack       string            `json:"execution_pack"`
	Task                string            `json:"task"`
	OperationContext    *OperationContext `json:"operation_context,omitempty"`
	PhaseAuthority      *PhaseAuthorityV3 `json:"phase_authority,omitempty"`
	Repository          string            `json:"repository"`
	BaseSHA             string            `json:"base_sha"`
	Invariants          []string          `json:"invariants"`
	NonGoals            []string          `json:"non_goals"`
	PredecessorOutcomes []Outcome         `json:"predecessor_outcomes"`
	Sources             []Source          `json:"sources"`
	CapsuleSHA256       string            `json:"capsule_sha256"`
}

// Verification summarizes a successful fail-closed capsule verification.
type Verification struct {
	SHA256          string        `json:"sha256,omitempty"`
	CapsuleSHA256   string        `json:"capsule_sha256"`
	BaseSHA         string        `json:"base_sha"`
	SourcesVerified int           `json:"sources_verified"`
	PolicyVersion   string        `json:"policy_version"`
	OperationKind   OperationKind `json:"operation_kind,omitempty"`
}

type payload struct {
	PolicyVersion       string            `json:"policy_version"`
	Project             string            `json:"project"`
	Plan                string            `json:"plan"`
	RoadmapPhase        string            `json:"roadmap_phase"`
	ExecutionPack       string            `json:"execution_pack"`
	Task                string            `json:"task"`
	OperationContext    *OperationContext `json:"operation_context,omitempty"`
	PhaseAuthority      *PhaseAuthorityV3 `json:"phase_authority,omitempty"`
	Repository          string            `json:"repository"`
	BaseSHA             string            `json:"base_sha"`
	Invariants          []string          `json:"invariants"`
	NonGoals            []string          `json:"non_goals"`
	PredecessorOutcomes []Outcome         `json:"predecessor_outcomes"`
	Sources             []Source          `json:"sources"`
}

// Build resolves and hashes the explicit sources in spec and returns canonical
// capsule JSON. V1/V2 require checkout HEAD at base; V3 reads the immutable
// base commit tree and does not require current HEAD equality.
func Build(repository string, spec Spec) (Capsule, []byte, error) {
	if err := validateSpec(spec); err != nil {
		return Capsule{}, nil, err
	}
	var repositoryRoot string
	var err error
	if spec.PolicyVersion == PolicyVersionV3 {
		repositoryRoot, err = verifyRepositoryAtCommit(repository, spec.Repository, spec.BaseSHA)
	} else {
		repositoryRoot, err = verifyRepository(repository, spec.Repository, spec.BaseSHA)
	}
	if err != nil {
		return Capsule{}, nil, err
	}
	repository = repositoryRoot
	if spec.PolicyVersion == PolicyVersionV3 {
		for _, allowed := range spec.PhaseAuthority.AllowedPaths {
			if err := validateAllowedPathAtCommit(repository, spec.BaseSHA, allowed); err != nil {
				return Capsule{}, nil, fmt.Errorf("phase_authority.allowed_paths %q: %w", allowed, err)
			}
		}
	}

	paths := append([]string(nil), spec.Sources...)
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if _, exists := seen[path]; exists {
			return Capsule{}, nil, fmt.Errorf("duplicate source path %q", path)
		}
		seen[path] = struct{}{}
	}
	sort.Strings(paths)
	sources := make([]Source, 0, len(paths))
	for _, path := range paths {
		var hash string
		if spec.PolicyVersion == PolicyVersionV3 {
			hash, err = hashRepositoryFileAtCommit(repository, spec.BaseSHA, path)
		} else {
			hash, err = hashRepositoryFile(repository, path)
		}
		if err != nil {
			return Capsule{}, nil, fmt.Errorf("source %q: %w", path, err)
		}
		sources = append(sources, Source{Path: path, SHA256: hash})
	}

	capsule := Capsule{
		PolicyVersion:       spec.PolicyVersion,
		Project:             spec.Project,
		Plan:                spec.Plan,
		RoadmapPhase:        spec.RoadmapPhase,
		ExecutionPack:       spec.ExecutionPack,
		Task:                spec.Task,
		OperationContext:    cloneOperationContext(spec.OperationContext),
		PhaseAuthority:      clonePhaseAuthority(spec.PhaseAuthority),
		Repository:          spec.Repository,
		BaseSHA:             strings.ToLower(spec.BaseSHA),
		Invariants:          append([]string{}, spec.Invariants...),
		NonGoals:            append([]string{}, spec.NonGoals...),
		PredecessorOutcomes: append([]Outcome{}, spec.PredecessorOutcomes...),
		Sources:             sources,
	}
	capsule.CapsuleSHA256, err = payloadHash(capsule)
	if err != nil {
		return Capsule{}, nil, err
	}
	data, err := json.Marshal(capsule)
	if err != nil {
		return Capsule{}, nil, fmt.Errorf("marshal capsule: %w", err)
	}
	if len(data) > MaxCapsuleBytes {
		return Capsule{}, nil, fmt.Errorf("capsule is %d bytes; maximum is %d", len(data), MaxCapsuleBytes)
	}
	return capsule, data, nil
}

// Parse accepts only the canonical JSON representation emitted by Build.
func Parse(data []byte) (Capsule, error) {
	if len(data) == 0 {
		return Capsule{}, errors.New("capsule is empty")
	}
	if len(data) > MaxCapsuleBytes {
		return Capsule{}, fmt.Errorf("capsule is %d bytes; maximum is %d", len(data), MaxCapsuleBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var capsule Capsule
	if err := decoder.Decode(&capsule); err != nil {
		return Capsule{}, fmt.Errorf("decode capsule: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Capsule{}, errors.New("decode capsule: multiple JSON values")
		}
		return Capsule{}, fmt.Errorf("decode capsule: %w", err)
	}
	canonical, err := json.Marshal(capsule)
	if err != nil {
		return Capsule{}, fmt.Errorf("marshal canonical capsule: %w", err)
	}
	if !bytes.Equal(data, canonical) {
		return Capsule{}, errors.New("capsule JSON is not canonical")
	}
	return capsule, nil
}

// Verify checks capsule hash, bounds, repository identity, and every exact
// source byte hash. V1/V2 use live base checkout bytes; V3 uses base-tree
// blobs. Verification fails on duplicates, traversal, or symlink modes.
func Verify(repository string, capsule Capsule) (Verification, error) {
	if err := validateCapsule(capsule); err != nil {
		return Verification{}, err
	}
	var err error
	if capsule.PolicyVersion == PolicyVersionV3 {
		repository, err = verifyRepositoryAtCommit(repository, capsule.Repository, capsule.BaseSHA)
	} else {
		repository, err = verifyRepository(repository, capsule.Repository, capsule.BaseSHA)
	}
	if err != nil {
		return Verification{}, err
	}
	if capsule.PolicyVersion == PolicyVersionV3 {
		for _, allowed := range capsule.PhaseAuthority.AllowedPaths {
			if err := validateAllowedPathAtCommit(repository, capsule.BaseSHA, allowed); err != nil {
				return Verification{}, fmt.Errorf("phase_authority.allowed_paths %q: %w", allowed, err)
			}
		}
	}
	expected, err := payloadHash(capsule)
	if err != nil {
		return Verification{}, err
	}
	if capsule.CapsuleSHA256 != expected {
		return Verification{}, fmt.Errorf("capsule SHA256 mismatch: expected %s, got %s", expected, capsule.CapsuleSHA256)
	}

	previous := ""
	for _, source := range capsule.Sources {
		if source.Path <= previous {
			if source.Path == previous {
				return Verification{}, fmt.Errorf("duplicate source path %q", source.Path)
			}
			return Verification{}, errors.New("capsule sources are not in canonical path order")
		}
		previous = source.Path
		var actual string
		if capsule.PolicyVersion == PolicyVersionV3 {
			actual, err = hashRepositoryFileAtCommit(repository, capsule.BaseSHA, source.Path)
		} else {
			actual, err = hashRepositoryFile(repository, source.Path)
		}
		if err != nil {
			return Verification{}, fmt.Errorf("source %q: %w", source.Path, err)
		}
		if actual != source.SHA256 {
			return Verification{}, fmt.Errorf("source %q SHA256 mismatch: expected %s, got %s", source.Path, source.SHA256, actual)
		}
	}
	verified := Verification{
		CapsuleSHA256:   capsule.CapsuleSHA256,
		BaseSHA:         capsule.BaseSHA,
		SourcesVerified: len(capsule.Sources),
		PolicyVersion:   capsule.PolicyVersion,
	}
	if capsule.OperationContext != nil {
		verified.OperationKind = capsule.OperationContext.Kind
	}
	return verified, nil
}

// VerifyFile parses and verifies a canonical capsule file. The capsule path
// itself must be a regular file reached without traversing symlinks.
func VerifyFile(repository, capsulePath string) (Verification, error) {
	file, err := openRegularNoSymlinks(capsulePath, MaxCapsuleBytes)
	if err != nil {
		return Verification{}, fmt.Errorf("open capsule: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxCapsuleBytes+1))
	if err != nil {
		return Verification{}, fmt.Errorf("read capsule: %w", err)
	}
	capsule, err := Parse(data)
	if err != nil {
		return Verification{}, err
	}
	verified, err := Verify(repository, capsule)
	if err != nil {
		return Verification{}, err
	}
	digest := sha256.Sum256(data)
	verified.SHA256 = hex.EncodeToString(digest[:])
	return verified, nil
}

func validateSpec(spec Spec) error {
	switch spec.PolicyVersion {
	case PolicyVersionV1:
		if spec.OperationContext != nil || spec.PhaseAuthority != nil {
			return errors.New("operation_context and phase_authority are not valid for context-capsule-v1")
		}
	case PolicyVersionV2:
		if spec.PhaseAuthority != nil {
			return errors.New("phase_authority is not valid for context-capsule-v2")
		}
		if spec.OperationContext == nil {
			return errors.New("operation_context is required for context-capsule-v2")
		}
		if err := validateOperationContext(*spec.OperationContext); err != nil {
			return err
		}
		if spec.PredecessorOutcomes == nil {
			return errors.New("predecessor_outcomes must be an explicit array for context-capsule-v2")
		}
	case PolicyVersionV3:
		if spec.OperationContext != nil {
			return errors.New("operation_context is not valid for context-capsule-v3")
		}
		if spec.PhaseAuthority == nil {
			return errors.New("phase_authority is required for context-capsule-v3")
		}
		if err := ValidatePhaseAuthorityV3(*spec.PhaseAuthority); err != nil {
			return err
		}
		if spec.PredecessorOutcomes == nil {
			return errors.New("predecessor_outcomes must be an explicit array for context-capsule-v3")
		}
	default:
		return fmt.Errorf("unsupported context policy version %q", spec.PolicyVersion)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "project", value: spec.Project},
		{name: "plan", value: spec.Plan},
		{name: "roadmap_phase", value: spec.RoadmapPhase},
		{name: "execution_pack", value: spec.ExecutionPack},
		{name: "task", value: spec.Task},
		{name: "repository", value: spec.Repository},
	} {
		if err := validateText(field.name, field.value); err != nil {
			return err
		}
	}
	if err := validateCommitSHA("base_sha", spec.BaseSHA); err != nil {
		return err
	}
	if len(spec.Invariants) == 0 || len(spec.Invariants) > MaxListItems {
		return fmt.Errorf("invariants count must be between 1 and %d", MaxListItems)
	}
	if len(spec.NonGoals) == 0 || len(spec.NonGoals) > MaxListItems {
		return fmt.Errorf("non_goals count must be between 1 and %d", MaxListItems)
	}
	if len(spec.Sources) == 0 || len(spec.Sources) > MaxSources {
		return fmt.Errorf("sources count must be between 1 and %d", MaxSources)
	}
	if len(spec.PredecessorOutcomes) > MaxOutcomes {
		return fmt.Errorf("predecessor_outcomes count must not exceed %d", MaxOutcomes)
	}
	for index, item := range spec.Invariants {
		if err := validateText(fmt.Sprintf("invariants[%d]", index), item); err != nil {
			return err
		}
	}
	for index, item := range spec.NonGoals {
		if err := validateText(fmt.Sprintf("non_goals[%d]", index), item); err != nil {
			return err
		}
	}
	for index, outcome := range spec.PredecessorOutcomes {
		if err := validateOutcome(index, outcome); err != nil {
			return err
		}
	}
	for index, path := range spec.Sources {
		if err := validateSourcePath(path); err != nil {
			return fmt.Errorf("sources[%d]: %w", index, err)
		}
	}
	return nil
}

func validateCapsule(capsule Capsule) error {
	spec := Spec{
		PolicyVersion: capsule.PolicyVersion, Project: capsule.Project, Plan: capsule.Plan,
		RoadmapPhase: capsule.RoadmapPhase, ExecutionPack: capsule.ExecutionPack, Task: capsule.Task,
		OperationContext: capsule.OperationContext,
		PhaseAuthority:   capsule.PhaseAuthority,
		Repository:       capsule.Repository, BaseSHA: capsule.BaseSHA, Invariants: capsule.Invariants,
		NonGoals: capsule.NonGoals, PredecessorOutcomes: capsule.PredecessorOutcomes,
		Sources: make([]string, len(capsule.Sources)),
	}
	for index, source := range capsule.Sources {
		spec.Sources[index] = source.Path
		if err := validateSHA256(fmt.Sprintf("sources[%d].sha256", index), source.SHA256); err != nil {
			return err
		}
	}
	if err := validateSpec(spec); err != nil {
		return err
	}
	if err := validateSHA256("capsule_sha256", capsule.CapsuleSHA256); err != nil {
		return err
	}
	data, err := json.Marshal(capsule)
	if err != nil {
		return fmt.Errorf("marshal capsule: %w", err)
	}
	if len(data) > MaxCapsuleBytes {
		return fmt.Errorf("capsule is %d bytes; maximum is %d", len(data), MaxCapsuleBytes)
	}
	return nil
}

func validateOperationContext(operation OperationContext) error {
	switch operation.Kind {
	case OperationDesignPlanning, OperationDesignReview, OperationImplementation,
		OperationImplementationReview, OperationAcceptance, OperationMergeAuthorization,
		OperationDeployment, OperationRecovery, OperationMaintenance:
	default:
		return fmt.Errorf("unsupported operation kind %q", operation.Kind)
	}
	if len(operation.OwnedScope) == 0 || len(operation.OwnedScope) > MaxListItems {
		return fmt.Errorf("operation_context.owned_scope count must be between 1 and %d", MaxListItems)
	}
	if len(operation.BlockingCriteria) == 0 || len(operation.BlockingCriteria) > MaxListItems {
		return fmt.Errorf("operation_context.blocking_criteria count must be between 1 and %d", MaxListItems)
	}
	for index, item := range operation.OwnedScope {
		if err := validateText(fmt.Sprintf("operation_context.owned_scope[%d]", index), item); err != nil {
			return err
		}
	}
	for index, item := range operation.BlockingCriteria {
		if err := validateText(fmt.Sprintf("operation_context.blocking_criteria[%d]", index), item); err != nil {
			return err
		}
	}
	return nil
}

// ValidatePhaseAuthorityV3 validates the closed V3 stage, operation, semantic,
// path, review-profile, parent, and execution-bound vocabulary.
func ValidatePhaseAuthorityV3(authority PhaseAuthorityV3) error {
	var operations []OperationKind
	var wantProfile ReviewProfile
	switch authority.Stage {
	case StageADesign:
		operations = []OperationKind{OperationDesignPlanning, OperationDesignReview}
		wantProfile = ReviewProfileNone
		if authority.Parent != nil {
			return errors.New("CAPSULE_STAGE_INVALID: A_DESIGN parent must be absent")
		}
		if authority.ExecutionBounds != nil {
			return errors.New("CAPSULE_STAGE_INVALID: A_DESIGN execution_bounds must be absent")
		}
	case StageBImplementation:
		operations = []OperationKind{OperationImplementation, OperationImplementationReview}
		if authority.Parent == nil || authority.Parent.Stage != StageADesign {
			return errors.New("CAPSULE_LINEAGE_INVALID: B_IMPLEMENTATION requires an A_DESIGN parent")
		}
		if authority.ReviewProfile != ReviewProfileInitialImplementation && authority.ReviewProfile != ReviewProfileCorrection {
			return errors.New("CAPSULE_STAGE_INVALID: B_IMPLEMENTATION review_profile is invalid")
		}
		if authority.ExecutionBounds == nil {
			return errors.New("EXECUTION_BOUNDS_INVALID: B_IMPLEMENTATION execution_bounds are required")
		}
		if err := ValidateExecutionBoundsV1(*authority.ExecutionBounds); err != nil {
			return err
		}
	case StageCAcceptanceMerge:
		operations = []OperationKind{OperationAcceptance, OperationFinalReview, OperationMergeAuthorization, OperationPostMergeAcceptance, OperationPRPublication}
		wantProfile = ReviewProfileNone
		if authority.Parent == nil || authority.Parent.Stage != StageBImplementation {
			return errors.New("CAPSULE_LINEAGE_INVALID: C_ACCEPTANCE_MERGE requires a B_IMPLEMENTATION parent")
		}
		if authority.ExecutionBounds != nil {
			return errors.New("CAPSULE_STAGE_INVALID: C_ACCEPTANCE_MERGE execution_bounds must be absent")
		}
		if len(authority.MutationScopeIDs) != 0 || len(authority.AuthorizedFindingIDs) != 0 {
			return errors.New("CAPSULE_STAGE_INVALID: C_ACCEPTANCE_MERGE is read-only")
		}
	default:
		return fmt.Errorf("CAPSULE_STAGE_INVALID: unsupported stage %q", authority.Stage)
	}
	if wantProfile != "" && authority.ReviewProfile != wantProfile {
		return fmt.Errorf("CAPSULE_STAGE_INVALID: stage %s requires review_profile %s", authority.Stage, wantProfile)
	}
	if !equalOperations(authority.AllowedOperations, operations) {
		return fmt.Errorf("CAPSULE_STAGE_INVALID: stage %s requires allowed_operations %v", authority.Stage, operations)
	}
	if err := validateSHA256("phase_authority.semantic_registry_sha256", authority.SemanticRegistrySHA256); err != nil {
		return err
	}
	for name, values := range map[string][]string{
		"observation_scope_ids":    authority.ObservationScopeIDs,
		"blocking_scope_ids":       authority.BlockingScopeIDs,
		"mutation_scope_ids":       authority.MutationScopeIDs,
		"authorized_finding_ids":   authority.AuthorizedFindingIDs,
		"authorized_invariant_ids": authority.AuthorizedInvariantIDs,
	} {
		if err := validateCanonicalIDs("phase_authority."+name, values); err != nil {
			return err
		}
	}
	if !isSubset(authority.BlockingScopeIDs, authority.ObservationScopeIDs) {
		return errors.New("CAPSULE_STAGE_INVALID: blocking_scope_ids must be a subset of observation_scope_ids")
	}
	if !isSubset(authority.MutationScopeIDs, authority.BlockingScopeIDs) {
		return errors.New("CAPSULE_STAGE_INVALID: mutation_scope_ids must be a subset of blocking_scope_ids")
	}
	if authority.ReviewProfile == ReviewProfileCorrection && len(authority.AuthorizedFindingIDs) == 0 {
		return errors.New("CAPSULE_STAGE_INVALID: CORRECTION requires authorized_finding_ids")
	}
	if authority.ReviewProfile != ReviewProfileCorrection && len(authority.AuthorizedFindingIDs) != 0 {
		return errors.New("CAPSULE_STAGE_INVALID: authorized_finding_ids require CORRECTION")
	}
	if len(authority.AuthorizedInvariantIDs) == 0 {
		return errors.New("CAPSULE_STAGE_INVALID: authorized_invariant_ids must not be empty")
	}
	if len(authority.AllowedPaths) == 0 || len(authority.AllowedPaths) > MaxListItems {
		return fmt.Errorf("CAPSULE_STAGE_INVALID: allowed_paths count must be between 1 and %d", MaxListItems)
	}
	previous := ""
	for index, path := range authority.AllowedPaths {
		if err := ValidateAllowedPathV3(path); err != nil {
			return fmt.Errorf("CAPSULE_STAGE_INVALID: phase_authority.allowed_paths[%d]: %w", index, err)
		}
		if path <= previous {
			return errors.New("CAPSULE_STAGE_INVALID: allowed_paths must be sorted and unique")
		}
		previous = path
	}
	if authority.Parent != nil {
		for name, digest := range map[string]string{
			"capsule_file_sha256": authority.Parent.CapsuleFileSHA256,
			"capsule_sha256":      authority.Parent.CapsuleSHA256,
			"checkpoint_sha256":   authority.Parent.CheckpointSHA256,
			"grant_sha256":        authority.Parent.GrantSHA256,
		} {
			if err := validateSHA256("phase_authority.parent."+name, digest); err != nil {
				return fmt.Errorf("CAPSULE_LINEAGE_INVALID: %w", err)
			}
		}
		if err := validateCommitSHA("phase_authority.parent.candidate_sha", authority.Parent.CandidateSHA); err != nil {
			return fmt.Errorf("CAPSULE_LINEAGE_INVALID: %w", err)
		}
	}
	return nil
}

// ValidateExecutionBoundsV1 validates the initial product ceiling profile.
func ValidateExecutionBoundsV1(bounds ExecutionBoundsV1) error {
	if bounds.MaxIterations < 1 || bounds.MaxIterations > 10 {
		return errors.New("EXECUTION_BOUNDS_INVALID: max_iterations must be between 1 and 10")
	}
	for _, item := range []struct {
		name string
		text string
		max  time.Duration
	}{
		{name: "session_timeout", text: bounds.SessionTimeout, max: 90 * time.Minute},
		{name: "idle_timeout", text: bounds.IdleTimeout, max: 45 * time.Minute},
		{name: "wall_clock_timeout", text: bounds.WallClockTimeout, max: 3 * time.Hour},
		{name: "aggregate_wall_clock_timeout", text: bounds.AggregateWallClockTimeout},
	} {
		duration, err := time.ParseDuration(item.text)
		if err != nil || duration <= 0 || item.max > 0 && duration > item.max || duration.String() != item.text {
			return fmt.Errorf("EXECUTION_BOUNDS_INVALID: %s must be canonical, positive, and within its product ceiling", item.name)
		}
	}
	wall, _ := time.ParseDuration(bounds.WallClockTimeout)
	aggregate, _ := time.ParseDuration(bounds.AggregateWallClockTimeout)
	if wall > aggregate {
		return errors.New("EXECUTION_BOUNDS_INVALID: wall_clock_timeout exceeds aggregate_wall_clock_timeout")
	}
	if bounds.Finalize {
		return errors.New("EXECUTION_BOUNDS_INVALID: finalize must be false")
	}
	if bounds.MaxIncompleteTasks != 1 {
		return errors.New("EXECUTION_BOUNDS_INVALID: max_incomplete_tasks must equal 1")
	}
	for name, value := range map[string]int{
		"max_initial_active_findings": bounds.MaxInitialActiveFindings,
		"max_ralphex_invocations":     bounds.MaxRalphexInvocations,
		"max_review_reports":          bounds.MaxReviewReports,
		"max_mutation_leases":         bounds.MaxMutationLeases,
		"max_total_fix_batches":       bounds.MaxTotalFixBatches,
		"max_changed_files":           bounds.MaxChangedFiles,
	} {
		if value < 1 {
			return fmt.Errorf("EXECUTION_BOUNDS_INVALID: %s must be positive", name)
		}
	}
	if bounds.MaxChangedBytes < 1 {
		return errors.New("EXECUTION_BOUNDS_INVALID: max_changed_bytes must be positive")
	}
	return nil
}

// ValidateAllowedPathV3 accepts one exact repository path or one directory/**
// prefix. It deliberately rejects broad roots and all other glob syntax.
func ValidateAllowedPathV3(path string) error {
	if path == "*" || path == "**" || path == "**/*" || path == "." || path == "./**" {
		return errors.New("broad-root patterns are forbidden")
	}
	prefix := strings.TrimSuffix(path, "/**")
	if prefix != path && (prefix == "" || strings.Contains(prefix, "*")) {
		return errors.New("dir/** must have a concrete directory prefix")
	}
	if strings.ContainsAny(prefix, "*?[]{}()|^$") {
		return errors.New("only an exact path or a dir/** prefix is allowed")
	}
	return validateSourcePath(prefix)
}

func validateCanonicalIDs(field string, values []string) error {
	if values == nil || len(values) > MaxListItems {
		return fmt.Errorf("%s must be an explicit array with no more than %d items", field, MaxListItems)
	}
	previous := ""
	for index, value := range values {
		if len(value) < 1 || len(value) > 128 || value != strings.TrimSpace(value) {
			return fmt.Errorf("%s[%d] has invalid length or whitespace", field, index)
		}
		for _, character := range value {
			if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') && !strings.ContainsRune("._:-", character) {
				return fmt.Errorf("%s[%d] contains an invalid character", field, index)
			}
		}
		if value <= previous {
			return fmt.Errorf("%s must be sorted and unique", field)
		}
		previous = value
	}
	return nil
}

func equalOperations(actual, expected []OperationKind) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range expected {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}

func isSubset(subset, superset []string) bool {
	allowed := make(map[string]struct{}, len(superset))
	for _, value := range superset {
		allowed[value] = struct{}{}
	}
	for _, value := range subset {
		if _, ok := allowed[value]; !ok {
			return false
		}
	}
	return true
}

func validateOutcome(index int, outcome Outcome) error {
	if err := validateText(fmt.Sprintf("predecessor_outcomes[%d].task", index), outcome.Task); err != nil {
		return err
	}
	if err := validateText(fmt.Sprintf("predecessor_outcomes[%d].summary", index), outcome.Summary); err != nil {
		return err
	}
	return validateCommitSHA(fmt.Sprintf("predecessor_outcomes[%d].commit_sha", index), outcome.CommitSHA)
}

func validateText(field, value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be non-empty without surrounding whitespace", field)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s must be valid UTF-8 without NUL bytes", field)
	}
	if len(value) > MaxStringBytes {
		return fmt.Errorf("%s exceeds %d bytes", field, MaxStringBytes)
	}
	return nil
}

func validateCommitSHA(field, value string) error {
	if value != strings.ToLower(value) || (len(value) != 40 && len(value) != 64) {
		return fmt.Errorf("%s must be a lowercase full Git object ID", field)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%s must be hexadecimal", field)
	}
	return nil
}

func validateSHA256(field, value string) error {
	if value != strings.ToLower(value) || len(value) != sha256.Size*2 {
		return fmt.Errorf("%s must be 64 lowercase hexadecimal characters", field)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%s must be 64 lowercase hexadecimal characters", field)
	}
	return nil
}

func validateSourcePath(path string) error {
	if path == "" || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return errors.New("source path must be repository-relative")
	}
	if strings.Contains(path, "\\") || filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) != path || path == "." {
		return errors.New("source path must be a canonical slash-separated repository-relative path")
	}
	if path == ".." || strings.HasPrefix(path, "../") {
		return errors.New("source path escapes repository")
	}
	return nil
}

func verifyRepository(repository, identity, baseSHA string) (string, error) {
	if err := validateCommitSHA("base_sha", strings.ToLower(baseSHA)); err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(repository)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	repository, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	info, err := os.Stat(repository)
	if err != nil || !info.IsDir() {
		return "", errors.New("repository path must be an existing directory")
	}
	root, err := gitOutput(repository, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("resolve Git repository root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve Git repository root: %w", err)
	}
	if filepath.Clean(root) != filepath.Clean(repository) {
		return "", fmt.Errorf("repository path %q is not Git root %q", repository, root)
	}
	head, err := gitOutput(repository, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve repository HEAD: %w", err)
	}
	if head != strings.ToLower(baseSHA) {
		return "", fmt.Errorf("repository HEAD %s does not match capsule base SHA %s", head, strings.ToLower(baseSHA))
	}
	remotes, err := gitOutput(repository, "remote")
	if err != nil {
		return "", fmt.Errorf("enumerate repository remotes: %w", err)
	}
	identityMatched := false
	for _, name := range strings.Fields(remotes) {
		remoteURLs, remoteErr := gitOutput(repository, "remote", "get-url", "--all", name)
		if remoteErr != nil {
			return "", fmt.Errorf("resolve repository remote %q: %w", name, remoteErr)
		}
		for _, remoteURL := range strings.Split(remoteURLs, "\n") {
			identityMatched = identityMatched || remoteIdentity(remoteURL) == identity
		}
	}
	if !identityMatched {
		return "", fmt.Errorf("capsule repository %q does not match any repository remote", identity)
	}
	return filepath.Clean(repository), nil
}

func verifyRepositoryAtCommit(repository, identity, baseSHA string) (string, error) {
	if err := validateCommitSHA("base_sha", baseSHA); err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(repository)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	repository, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	info, err := os.Stat(repository)
	if err != nil || !info.IsDir() {
		return "", errors.New("repository path must be an existing directory")
	}
	root, err := gitOutput(repository, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("resolve Git repository root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil || filepath.Clean(root) != filepath.Clean(repository) {
		return "", errors.New("repository path is not the canonical Git root")
	}
	commit, err := gitOutput(repository, "rev-parse", "--verify", baseSHA+"^{commit}")
	if err != nil || commit != baseSHA {
		return "", fmt.Errorf("base_sha %s is not the exact replacement-resistant commit", baseSHA)
	}
	remotes, err := gitOutput(repository, "remote")
	if err != nil {
		return "", fmt.Errorf("enumerate repository remotes: %w", err)
	}
	matched := false
	for _, name := range strings.Fields(remotes) {
		urls, remoteErr := gitOutput(repository, "remote", "get-url", "--all", name)
		if remoteErr != nil {
			return "", fmt.Errorf("resolve repository remote %q: %w", name, remoteErr)
		}
		for _, remoteURL := range strings.Split(urls, "\n") {
			matched = matched || remoteIdentity(remoteURL) == identity
		}
	}
	if !matched {
		return "", fmt.Errorf("capsule repository %q does not match any repository remote", identity)
	}
	return filepath.Clean(repository), nil
}

func remoteIdentity(remoteURL string) string {
	path := remoteURL
	if parsed, err := url.Parse(remoteURL); err == nil && parsed.Scheme != "" {
		path = parsed.Path
	} else if colon := strings.IndexByte(remoteURL, ':'); colon >= 0 && !strings.Contains(remoteURL[:colon], "/") {
		path = remoteURL[colon+1:]
	}
	path = strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
	return strings.TrimSuffix(path, ".git")
}

func hashRepositoryFile(repository, relative string) (string, error) {
	if err := validateSourcePath(relative); err != nil {
		return "", err
	}
	path := filepath.Join(repository, filepath.FromSlash(relative))
	file, err := openRegularNoSymlinks(path, MaxSourceBytes)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func hashRepositoryFileAtCommit(repository, commit, relative string) (string, error) {
	if err := validateSourcePath(relative); err != nil {
		return "", err
	}
	output, err := gitOutputBytes(repository, "ls-tree", "-z", commit, "--", relative)
	if err != nil {
		return "", err
	}
	if len(output) == 0 {
		return "", errors.New("source does not exist in immutable base tree")
	}
	if output[len(output)-1] != 0 || bytes.Count(output, []byte{0}) != 1 {
		return "", errors.New("source tree lookup was ambiguous")
	}
	record := strings.TrimSuffix(string(output), "\x00")
	tab := strings.IndexByte(record, '\t')
	fields := strings.Fields(record[:max(tab, 0)])
	if tab < 0 || len(fields) != 3 || record[tab+1:] != relative {
		return "", errors.New("source tree entry is malformed")
	}
	if (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" {
		return "", errors.New("source must be a regular file in immutable base tree")
	}
	sizeText, err := gitOutput(repository, "cat-file", "-s", fields[2])
	if err != nil {
		return "", err
	}
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil || size < 0 || size > MaxSourceBytes {
		return "", fmt.Errorf("source blob size is invalid or exceeds %d", MaxSourceBytes)
	}
	data, err := gitOutputBytes(repository, "cat-file", "blob", fields[2])
	if err != nil {
		return "", err
	}
	if int64(len(data)) != size {
		return "", errors.New("source blob size changed while reading")
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func validateAllowedPathAtCommit(repository, commit, rule string) error {
	path, directoryRule := strings.CutSuffix(rule, "/**")
	components := strings.Split(path, "/")
	for index := range components {
		prefix := strings.Join(components[:index+1], "/")
		output, err := gitOutputBytes(repository, "ls-tree", "-z", commit, "--", prefix)
		if err != nil {
			return err
		}
		if len(output) == 0 {
			return nil
		}
		if output[len(output)-1] != 0 || bytes.Count(output, []byte{0}) != 1 {
			return errors.New("tree lookup is ambiguous")
		}
		record := strings.TrimSuffix(string(output), "\x00")
		tab := strings.IndexByte(record, '\t')
		if tab < 0 || record[tab+1:] != prefix {
			return errors.New("tree lookup path mismatch")
		}
		fields := strings.Fields(record[:tab])
		if len(fields) != 3 {
			return errors.New("tree lookup is malformed")
		}
		last := index == len(components)-1
		if !last && (fields[0] != "040000" || fields[1] != "tree") {
			return errors.New("path is derived through a symlink, submodule, or non-directory")
		}
		if last && directoryRule && (fields[0] != "040000" || fields[1] != "tree") {
			return errors.New("dir/** prefix names a non-directory")
		}
		if last && !directoryRule && fields[0] != "100644" && fields[0] != "100755" {
			return errors.New("exact path names a symlink, submodule, or non-regular file")
		}
	}
	return nil
}

func openRegularNoSymlinks(path string, maximum int64) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	volume := filepath.VolumeName(absolute)
	cursor := volume + string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(absolute, cursor), string(filepath.Separator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		cursor = filepath.Join(cursor, part)
		info, err := os.Lstat(cursor)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("path component %q is a symlink", cursor)
		}
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("must be a regular file")
	}
	if info.Size() > maximum {
		file.Close()
		return nil, fmt.Errorf("file is %d bytes; maximum is %d", info.Size(), maximum)
	}
	return file, nil
}

func payloadHash(capsule Capsule) (string, error) {
	data, err := json.Marshal(payload{
		PolicyVersion: capsule.PolicyVersion, Project: capsule.Project, Plan: capsule.Plan,
		RoadmapPhase: capsule.RoadmapPhase, ExecutionPack: capsule.ExecutionPack, Task: capsule.Task,
		OperationContext: capsule.OperationContext,
		PhaseAuthority:   capsule.PhaseAuthority,
		Repository:       capsule.Repository, BaseSHA: capsule.BaseSHA, Invariants: capsule.Invariants,
		NonGoals: capsule.NonGoals, PredecessorOutcomes: capsule.PredecessorOutcomes, Sources: capsule.Sources,
	})
	if err != nil {
		return "", fmt.Errorf("marshal capsule payload: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func cloneOperationContext(operation *OperationContext) *OperationContext {
	if operation == nil {
		return nil
	}
	clone := *operation
	clone.OwnedScope = append([]string{}, operation.OwnedScope...)
	clone.BlockingCriteria = append([]string{}, operation.BlockingCriteria...)
	return &clone
}

func clonePhaseAuthority(authority *PhaseAuthorityV3) *PhaseAuthorityV3 {
	if authority == nil {
		return nil
	}
	clone := *authority
	clone.AllowedOperations = append([]OperationKind{}, authority.AllowedOperations...)
	clone.ObservationScopeIDs = append([]string{}, authority.ObservationScopeIDs...)
	clone.BlockingScopeIDs = append([]string{}, authority.BlockingScopeIDs...)
	clone.MutationScopeIDs = append([]string{}, authority.MutationScopeIDs...)
	clone.AuthorizedFindingIDs = append([]string{}, authority.AuthorizedFindingIDs...)
	clone.AuthorizedInvariantIDs = append([]string{}, authority.AuthorizedInvariantIDs...)
	clone.AllowedPaths = append([]string{}, authority.AllowedPaths...)
	if authority.Parent != nil {
		parent := *authority.Parent
		clone.Parent = &parent
	}
	if authority.ExecutionBounds != nil {
		bounds := *authority.ExecutionBounds
		clone.ExecutionBounds = &bounds
	}
	return &clone
}

func gitOutput(repository string, args ...string) (string, error) {
	output, err := gitOutputBytes(repository, args...)
	return strings.TrimSpace(string(output)), err
}

func gitOutputBytes(repository string, args ...string) ([]byte, error) {
	command := exec.Command("git", args...)
	command.Dir = repository
	command.Env = gitexec.Environment()
	return command.Output()
}

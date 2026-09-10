// Package authority validates and freezes the inputs that govern one run.
package authority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	governancev3 "github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

// Manifest is the mutable, serializable input used to construct an Authority.
type Manifest struct {
	RunID          string                  `json:"run_id"`
	Repository     RepositoryManifest      `json:"repository"`
	Plan           PlanManifest            `json:"plan"`
	ContextCapsule *ContextCapsuleManifest `json:"context_capsule,omitempty"`
	MergeReview    *ReviewPolicy           `json:"merge_review,omitempty"`
	Governance     *GovernanceManifest     `json:"governance,omitempty"`
	Ralphex        RalphexManifest         `json:"ralphex"`
	Executor       ExecutorPolicy          `json:"executor"`
	Worktree       WorktreePolicy          `json:"worktree"`
	Acceptance     []AcceptanceCommand     `json:"acceptance"`
	PolicyVersion  string                  `json:"policy_version"`
}

// ReviewRequirement binds one controller-selected component to the exact
// commit and verdict required by the serial merge gate.
type ReviewRequirement struct {
	Component   string `json:"component"`
	ReviewedSHA string `json:"reviewed_sha"`
	Verdict     string `json:"verdict"`
}

// ReviewPolicy is optional for authorities created before the serial merge
// gate. The merge gate itself requires it and never accepts caller-selected
// requirements as a substitute.
type ReviewPolicy struct {
	PolicyIdentity string              `json:"policy_identity"`
	Required       []ReviewRequirement `json:"required"`
}

// RepositoryManifest pins the governed repository and its starting identity.
// Remotes is keyed by remote name (for example, "origin").
type RepositoryManifest struct {
	Path          string            `json:"path"`
	Identity      string            `json:"identity,omitempty"`
	Remotes       map[string]string `json:"remotes,omitempty"`
	DefaultBranch string            `json:"default_branch,omitempty"`
	StartSHA      string            `json:"start_sha"`
}

// PlanManifest identifies the exact plan bytes authorized for the run.
type PlanManifest struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// ContextCapsuleManifest optionally binds a run to one exact verified capsule.
// It is omitted for manifests created before the context-capsule policy.
type ContextCapsuleManifest struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// RalphexManifest pins the executable, its source metadata, and invocation mode.
type RalphexManifest struct {
	BinaryPath     string                    `json:"binary_path"`
	BinarySHA256   string                    `json:"binary_sha256"`
	SourceSHA      string                    `json:"source_sha,omitempty"`
	Mode           ralphex.Mode              `json:"mode"`
	Timeout        string                    `json:"timeout"`
	WaitOnLimit    string                    `json:"wait_on_limit"`
	Capability     *ralphex.CapabilityV1     `json:"capability,omitempty"`
	ExecutionState *ralphex.ExecutionStateV1 `json:"execution_state,omitempty"`
}

// GovernanceManifest selects one V3 operation and binds activation evidence
// when the durable A/B/C cutover has occurred.
type GovernanceManifest struct {
	Operation      contextcapsule.OperationKind         `json:"operation"`
	Mutation       bool                                 `json:"mutation"`
	LeaseSHA256    string                               `json:"lease_sha256,omitempty"`
	IssuedSequence uint64                               `json:"issued_sequence,omitempty"`
	Activation     *governancev3.GovernanceActivationV1 `json:"activation,omitempty"`
}

// ExecutorPolicy records the selected executor and model/effort settings.
type ExecutorPolicy struct {
	Executor     string `json:"executor,omitempty"`
	TaskModel    string `json:"task_model,omitempty"`
	TaskEffort   string `json:"task_effort,omitempty"`
	ReviewModel  string `json:"review_model,omitempty"`
	ReviewEffort string `json:"review_effort,omitempty"`
}

// WorktreePolicy records how Ralphex should isolate the governed run.
type WorktreePolicy struct {
	Enabled bool   `json:"enabled"`
	Branch  string `json:"branch,omitempty"`
}

// AcceptanceCommand is one controller-owned deterministic acceptance check.
type AcceptanceCommand struct {
	Name     string   `json:"name,omitempty"`
	Class    string   `json:"class,omitempty"`
	Required bool     `json:"required"`
	Timeout  string   `json:"timeout"`
	Argv     []string `json:"argv"`
}

// Authority is an immutable, validated value. Its internals are deliberately
// private; accessors return copies for fields containing mutable Go values.
type Authority struct {
	manifest      Manifest
	canonicalJSON []byte
	sha256        string
}

// New validates, canonicalizes, and freezes a run manifest.
func New(input Manifest) (Authority, error) {
	manifest := cloneManifest(input)
	if manifest.MergeReview != nil {
		sort.Slice(manifest.MergeReview.Required, func(i, j int) bool {
			return manifest.MergeReview.Required[i].Component < manifest.MergeReview.Required[j].Component
		})
	}
	if err := canonicalizeExecutorPolicy(&manifest.Executor); err != nil {
		return Authority{}, err
	}
	if err := validateRequired(manifest); err != nil {
		return Authority{}, err
	}

	repositoryPath, err := canonicalDirectory(manifest.Repository.Path)
	if err != nil {
		return Authority{}, fmt.Errorf("repository path: %w", err)
	}
	manifest.Repository.Path = repositoryPath

	planPath := manifest.Plan.Path
	if !filepath.IsAbs(planPath) {
		planPath = filepath.Join(repositoryPath, planPath)
	}
	planPath, err = canonicalFile(planPath)
	if err != nil {
		return Authority{}, fmt.Errorf("plan path: %w", err)
	}
	if err := requireWithin(repositoryPath, planPath); err != nil {
		return Authority{}, fmt.Errorf("plan path: %w", err)
	}
	manifest.Plan.Path = planPath
	if manifest.ContextCapsule != nil {
		capsulePath := manifest.ContextCapsule.Path
		if !filepath.IsAbs(capsulePath) {
			capsulePath = filepath.Join(repositoryPath, capsulePath)
		}
		if _, err := contextcapsule.VerifyFile(repositoryPath, capsulePath); err != nil {
			return Authority{}, fmt.Errorf("verify context capsule: %w", err)
		}
		capsulePath, err = canonicalFileNoSymlink(capsulePath)
		if err != nil {
			return Authority{}, fmt.Errorf("context capsule path: %w", err)
		}
		manifest.ContextCapsule.Path = capsulePath
		manifest.ContextCapsule.SHA256, err = validateFileHash(capsulePath, manifest.ContextCapsule.SHA256)
		if err != nil {
			return Authority{}, fmt.Errorf("context capsule SHA256: %w", err)
		}
	}

	binaryPath, err := canonicalFile(manifest.Ralphex.BinaryPath)
	if err != nil {
		return Authority{}, fmt.Errorf("ralphex binary path: %w", err)
	}
	manifest.Ralphex.BinaryPath = binaryPath

	manifest.Plan.SHA256, err = validateFileHash(planPath, manifest.Plan.SHA256)
	if err != nil {
		return Authority{}, fmt.Errorf("plan SHA256: %w", err)
	}
	manifest.Ralphex.BinarySHA256, err = validateFileHash(binaryPath, manifest.Ralphex.BinarySHA256)
	if err != nil {
		return Authority{}, fmt.Errorf("ralphex binary SHA256: %w", err)
	}
	manifest.Ralphex.Timeout = canonicalDuration(manifest.Ralphex.Timeout)
	manifest.Ralphex.WaitOnLimit = canonicalDuration(manifest.Ralphex.WaitOnLimit)
	for index := range manifest.Acceptance {
		manifest.Acceptance[index].Timeout = canonicalDuration(manifest.Acceptance[index].Timeout)
	}
	if err := validateGovernanceAdmission(manifest); err != nil {
		return Authority{}, err
	}

	canonicalJSON, err := json.Marshal(manifest)
	if err != nil {
		return Authority{}, fmt.Errorf("serialize canonical authority: %w", err)
	}
	digest := sha256.Sum256(canonicalJSON)

	return Authority{
		manifest:      manifest,
		canonicalJSON: canonicalJSON,
		sha256:        hex.EncodeToString(digest[:]),
	}, nil
}

// Manifest returns a deep copy of the canonical validated manifest.
func (a Authority) Manifest() Manifest {
	return cloneManifest(a.manifest)
}

// CanonicalJSON returns deterministic JSON bytes for the validated authority.
func (a Authority) CanonicalJSON() []byte {
	return append([]byte(nil), a.canonicalJSON...)
}

// SHA256 returns the lowercase hexadecimal hash of CanonicalJSON.
func (a Authority) SHA256() string {
	return a.sha256
}

// RunID returns the governed run identifier.
func (a Authority) RunID() string {
	return a.manifest.RunID
}

// Repository returns a copy of the canonical repository identity.
func (a Authority) Repository() RepositoryManifest {
	return cloneRepository(a.manifest.Repository)
}

// Plan returns the canonical plan identity.
func (a Authority) Plan() PlanManifest {
	return a.manifest.Plan
}

// ContextCapsule returns the optional canonical capsule binding.
func (a Authority) ContextCapsule() (ContextCapsuleManifest, bool) {
	if a.manifest.ContextCapsule == nil {
		return ContextCapsuleManifest{}, false
	}
	return *a.manifest.ContextCapsule, true
}

// MergeReviewPolicy returns the optional immutable controller review policy.
func (a Authority) MergeReviewPolicy() (ReviewPolicy, bool) {
	if a.manifest.MergeReview == nil {
		return ReviewPolicy{}, false
	}
	return cloneReviewPolicy(*a.manifest.MergeReview), true
}

// Governance returns the optional V3 operation/activation admission record.
func (a Authority) Governance() (GovernanceManifest, bool) {
	if a.manifest.Governance == nil {
		return GovernanceManifest{}, false
	}
	copy := cloneManifest(Manifest{Governance: a.manifest.Governance})
	return *copy.Governance, true
}

// Ralphex returns the canonical Ralphex identity and mode.
func (a Authority) Ralphex() RalphexManifest {
	return a.manifest.Ralphex
}

// Executor returns the executor/model/effort policy.
func (a Authority) Executor() ExecutorPolicy {
	return a.manifest.Executor
}

// Worktree returns the configured worktree policy.
func (a Authority) Worktree() WorktreePolicy {
	return a.manifest.Worktree
}

// Acceptance returns a deep copy of the acceptance command policy.
func (a Authority) Acceptance() []AcceptanceCommand {
	return cloneAcceptance(a.manifest.Acceptance)
}

// PolicyVersion returns the authority policy version.
func (a Authority) PolicyVersion() string {
	return a.manifest.PolicyVersion
}

func validateGovernanceAdmission(manifest Manifest) error {
	if manifest.ContextCapsule == nil {
		if manifest.Governance != nil {
			return errors.New("CAPSULE_USAGE_INVALID: governance admission requires a context capsule")
		}
		return nil
	}
	data, err := os.ReadFile(manifest.ContextCapsule.Path)
	if err != nil {
		return fmt.Errorf("read governance context capsule: %w", err)
	}
	capsule, err := contextcapsule.Parse(data)
	if err != nil {
		return fmt.Errorf("parse governance context capsule: %w", err)
	}
	if manifest.Governance != nil && manifest.Governance.Activation != nil {
		if err := governancev3.ValidateActivatedWorkflowPolicyV1(capsule.PolicyVersion, manifest.ContextCapsule.SHA256, manifest.Governance.IssuedSequence, *manifest.Governance.Activation); err != nil {
			return err
		}
	}
	if capsule.PolicyVersion != contextcapsule.PolicyVersionV3 {
		if manifest.Governance != nil && manifest.Governance.Activation == nil {
			return errors.New("CAPSULE_LINEAGE_INVALID: V2 cannot assert A/B/C governance without activation/grandfather evidence")
		}
		return nil
	}
	if manifest.Governance == nil {
		return errors.New("CAPSULE_USAGE_INVALID: V3 requires explicit governance operation admission")
	}
	if err := governancev3.ValidateCapsuleUsageV3(capsule, manifest.Governance.Operation, manifest.Governance.Mutation, manifest.Governance.LeaseSHA256); err != nil {
		return err
	}
	if capsule.PhaseAuthority == nil || capsule.PhaseAuthority.Stage != contextcapsule.StageBImplementation {
		return errors.New("CAPSULE_STAGE_INVALID: Ralphex execution is authorized only by B_IMPLEMENTATION")
	}
	if manifest.Governance.Operation != contextcapsule.OperationImplementation && manifest.Governance.Operation != contextcapsule.OperationImplementationReview {
		return errors.New("CAPSULE_USAGE_INVALID: Ralphex B admission requires implementation or implementation-review")
	}
	bounds := capsule.PhaseAuthority.ExecutionBounds
	if bounds == nil || manifest.Ralphex.Capability == nil || manifest.Ralphex.ExecutionState == nil {
		return errors.New("EXECUTION_BOUNDS_INVALID: V3 requires execution bounds, capability, and durable counters")
	}
	if err := ralphex.ValidateCapabilityV1(*manifest.Ralphex.Capability, manifest.Ralphex.BinarySHA256, manifest.Ralphex.SourceSHA, manifest.Ralphex.Mode); err != nil {
		return err
	}
	if err := ralphex.ValidateExecutionStateV1(*bounds, *manifest.Ralphex.ExecutionState); err != nil {
		return err
	}
	if manifest.Executor.Executor != "codex" || manifest.Executor.TaskEffort != "xhigh" || manifest.Executor.ReviewEffort != "xhigh" {
		return errors.New("EXECUTION_BOUNDS_INVALID: V3 requires Codex task and review effort exactly xhigh")
	}
	wall, _ := time.ParseDuration(bounds.WallClockTimeout)
	manifestTimeout, _ := time.ParseDuration(manifest.Ralphex.Timeout)
	if manifestTimeout != wall {
		return errors.New("EXECUTION_BOUNDS_INVALID: Ralphex timeout must equal V3 wall_clock_timeout")
	}
	wait, _ := time.ParseDuration(manifest.Ralphex.WaitOnLimit)
	aggregate, _ := time.ParseDuration(bounds.AggregateWallClockTimeout)
	elapsed, _ := time.ParseDuration(manifest.Ralphex.ExecutionState.AggregateElapsed)
	if wait < 0 || elapsed+wall+wait > aggregate {
		return errors.New("EXECUTION_BOUNDS_INVALID: rate-limit wait exceeds aggregate wall clock")
	}
	plan, err := os.ReadFile(manifest.Plan.Path)
	if err != nil {
		return fmt.Errorf("read governed plan for execution bounds: %w", err)
	}
	if err := ralphex.ValidateSingleIncompleteTaskV1(plan); err != nil {
		return err
	}
	return nil
}

func validateRequired(manifest Manifest) error {
	var missing []string
	if manifest.RunID == "" {
		missing = append(missing, "run_id")
	}
	if manifest.Repository.Path == "" {
		missing = append(missing, "repository.path")
	}
	if manifest.Repository.Identity == "" {
		missing = append(missing, "repository.identity")
	}
	if len(manifest.Repository.Remotes) == 0 {
		missing = append(missing, "repository.remotes")
	}
	if manifest.Repository.DefaultBranch == "" {
		missing = append(missing, "repository.default_branch")
	}
	if manifest.Repository.StartSHA == "" {
		missing = append(missing, "repository.start_sha")
	}
	if manifest.Plan.Path == "" {
		missing = append(missing, "plan.path")
	}
	if manifest.Plan.SHA256 == "" {
		missing = append(missing, "plan.sha256")
	}
	if manifest.ContextCapsule != nil {
		if manifest.ContextCapsule.Path == "" {
			missing = append(missing, "context_capsule.path")
		}
		if manifest.ContextCapsule.SHA256 == "" {
			missing = append(missing, "context_capsule.sha256")
		}
	}
	if manifest.MergeReview != nil {
		if err := validateReviewPolicy(*manifest.MergeReview); err != nil {
			return err
		}
	}
	if manifest.Ralphex.BinaryPath == "" {
		missing = append(missing, "ralphex.binary_path")
	}
	if manifest.Ralphex.BinarySHA256 == "" {
		missing = append(missing, "ralphex.binary_sha256")
	}
	if manifest.Ralphex.Timeout == "" {
		missing = append(missing, "ralphex.timeout")
	}
	if manifest.Ralphex.WaitOnLimit == "" {
		missing = append(missing, "ralphex.wait_on_limit")
	}
	if len(missing) > 0 {
		return fmt.Errorf("required authority fields missing: %s", strings.Join(missing, ", "))
	}

	switch manifest.Ralphex.Mode {
	case ralphex.ModeFull, ralphex.ModeTasksOnly, ralphex.ModeReview:
	default:
		return fmt.Errorf("unsupported ralphex mode %q", manifest.Ralphex.Mode)
	}
	if err := validateDuration("ralphex.timeout", manifest.Ralphex.Timeout, false); err != nil {
		return err
	}
	if err := validateDuration("ralphex.wait_on_limit", manifest.Ralphex.WaitOnLimit, true); err != nil {
		return err
	}
	if manifest.Worktree.Enabled && manifest.Worktree.Branch == "" {
		return errors.New("worktree.branch is required when worktree is enabled")
	}
	if manifest.Worktree.Enabled && manifest.Ralphex.Mode == ralphex.ModeReview {
		return errors.New("worktree is not supported in review mode")
	}
	if !manifest.Worktree.Enabled && manifest.Worktree.Branch != "" {
		return errors.New("worktree.branch requires worktree.enabled")
	}
	if manifest.Worktree.Branch != strings.TrimSpace(manifest.Worktree.Branch) || strings.HasPrefix(manifest.Worktree.Branch, "-") {
		return errors.New("worktree.branch must be a non-option Git branch name without surrounding whitespace")
	}
	if len(manifest.Acceptance) == 0 {
		return errors.New("at least one acceptance command is required")
	}
	hasRequiredAcceptance := false
	for index, command := range manifest.Acceptance {
		hasRequiredAcceptance = hasRequiredAcceptance || command.Required
		if command.Timeout == "" {
			return fmt.Errorf("acceptance command %d timeout is required", index)
		}
		if err := validateDuration(fmt.Sprintf("acceptance command %d timeout", index), command.Timeout, false); err != nil {
			return err
		}
		if len(command.Argv) == 0 {
			return fmt.Errorf("acceptance command %d argv must not be empty", index)
		}
		for argIndex, arg := range command.Argv {
			if arg == "" {
				return fmt.Errorf("acceptance command %d argv[%d] must not be empty", index, argIndex)
			}
		}
	}
	if !hasRequiredAcceptance {
		return errors.New("at least one required acceptance command is required")
	}
	if manifest.Repository.Identity != strings.TrimSpace(manifest.Repository.Identity) {
		return errors.New("repository.identity must not contain surrounding whitespace")
	}
	if manifest.Repository.DefaultBranch != strings.TrimSpace(manifest.Repository.DefaultBranch) {
		return errors.New("repository.default_branch must not contain surrounding whitespace")
	}
	identityMatched := false
	for name, remoteURL := range manifest.Repository.Remotes {
		if name == "" || name != strings.TrimSpace(name) || strings.ContainsAny(name, "\r\n") {
			return errors.New("repository remote names must be non-empty and contain no surrounding whitespace or newlines")
		}
		if remoteURL == "" || remoteURL != strings.TrimSpace(remoteURL) || strings.ContainsAny(remoteURL, "\r\n") {
			return fmt.Errorf("repository remote %q URL must be non-empty and contain no surrounding whitespace or newlines", name)
		}
		if err := rejectRemoteCredentials(remoteURL); err != nil {
			return fmt.Errorf("repository remote %q: %w", name, err)
		}
		identityMatched = identityMatched || remoteIdentity(remoteURL) == manifest.Repository.Identity
	}
	if !identityMatched {
		return fmt.Errorf("repository.identity %q does not match any governed remote URL", manifest.Repository.Identity)
	}
	return nil
}

func validateReviewPolicy(policy ReviewPolicy) error {
	if policy.PolicyIdentity == "" || policy.PolicyIdentity != strings.TrimSpace(policy.PolicyIdentity) || strings.ContainsAny(policy.PolicyIdentity, "\r\n") {
		return errors.New("merge_review.policy_identity must be non-empty and contain no surrounding whitespace or newlines")
	}
	if len(policy.Required) == 0 {
		return errors.New("merge_review.required must contain at least one component")
	}
	seen := make(map[string]struct{}, len(policy.Required))
	for index, requirement := range policy.Required {
		if requirement.Component == "" || requirement.Component != strings.TrimSpace(requirement.Component) || strings.ContainsAny(requirement.Component, "\r\n") {
			return fmt.Errorf("merge_review.required[%d].component is invalid", index)
		}
		if _, exists := seen[requirement.Component]; exists {
			return fmt.Errorf("merge_review.required repeats component %q", requirement.Component)
		}
		seen[requirement.Component] = struct{}{}
		if !validObjectID(requirement.ReviewedSHA) {
			return fmt.Errorf("merge_review.required[%d].reviewed_sha must be an exact lowercase Git object ID", index)
		}
		if requirement.Verdict == "" || requirement.Verdict != strings.TrimSpace(requirement.Verdict) || strings.ContainsAny(requirement.Verdict, "\r\n") {
			return fmt.Errorf("merge_review.required[%d].verdict is invalid", index)
		}
	}
	return nil
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func rejectRemoteCredentials(remoteURL string) error {
	parsed, err := url.Parse(remoteURL)
	if err != nil || parsed.Scheme == "" {
		return nil
	}
	scheme := strings.ToLower(parsed.Scheme)
	if parsed.User != nil {
		_, hasPassword := parsed.User.Password()
		if hasPassword || (scheme != "ssh" && scheme != "git+ssh") {
			return errors.New("URL must not contain credentials")
		}
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("URL must not contain query credentials or fragments")
	}
	return nil
}

func canonicalizeExecutorPolicy(policy *ExecutorPolicy) error {
	policy.Executor = strings.ToLower(strings.TrimSpace(policy.Executor))
	if policy.Executor == "" {
		policy.Executor = "claude"
	}
	if policy.Executor != "claude" && policy.Executor != "codex" {
		return fmt.Errorf("unsupported executor %q", policy.Executor)
	}
	for field, value := range map[string]string{
		"executor.task_model":    policy.TaskModel,
		"executor.task_effort":   policy.TaskEffort,
		"executor.review_model":  policy.ReviewModel,
		"executor.review_effort": policy.ReviewEffort,
	} {
		if strings.Contains(value, ":") {
			return fmt.Errorf("%s must not contain ':'", field)
		}
	}
	return nil
}

func remoteIdentity(remoteURL string) string {
	path := remoteURL
	if parsed, err := url.Parse(remoteURL); err == nil && parsed.Scheme != "" {
		path = parsed.Path
	} else if colon := strings.IndexByte(remoteURL, ':'); colon >= 0 && !strings.Contains(remoteURL[:colon], "/") {
		// Git's SCP-like syntax, for example git@example.test:owner/repository.git.
		path = remoteURL[colon+1:]
	}
	path = strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
	return strings.TrimSuffix(path, ".git")
}

func validateDuration(field, value string, allowZero bool) error {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s must be a valid duration: %w", field, err)
	}
	if duration < 0 || (!allowZero && duration == 0) {
		requirement := "positive"
		if allowZero {
			requirement = "non-negative"
		}
		return fmt.Errorf("%s must be %s", field, requirement)
	}
	return nil
}

func canonicalDuration(value string) string {
	duration, _ := time.ParseDuration(value)
	return duration.String()
}

func canonicalDirectory(path string) (string, error) {
	canonical, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("must be a directory")
	}
	return canonical, nil
}

func canonicalFile(path string) (string, error) {
	canonical, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("must be a regular file")
	}
	return canonical, nil
}

func canonicalFileNoSymlink(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("must not be a symlink")
	}
	return canonicalFile(absolute)
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(canonical), nil
}

func requireWithin(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%q resolves outside governed repository %q", path, root)
	}
	return nil
}

func validateFileHash(path, expected string) (string, error) {
	expected = strings.ToLower(expected)
	decoded, err := hex.DecodeString(expected)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("must be 64 hexadecimal characters")
	}

	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if actual != expected {
		return "", fmt.Errorf("mismatch: expected %s, got %s", expected, actual)
	}
	return expected, nil
}

func cloneManifest(input Manifest) Manifest {
	clone := input
	clone.Repository = cloneRepository(input.Repository)
	if input.ContextCapsule != nil {
		binding := *input.ContextCapsule
		clone.ContextCapsule = &binding
	}
	if input.MergeReview != nil {
		policy := cloneReviewPolicy(*input.MergeReview)
		clone.MergeReview = &policy
	}
	if input.Governance != nil {
		governance := *input.Governance
		if input.Governance.Activation != nil {
			activation := *input.Governance.Activation
			activation.GrandfatheredV2Digests = append([]string(nil), input.Governance.Activation.GrandfatheredV2Digests...)
			governance.Activation = &activation
		}
		clone.Governance = &governance
	}
	if input.Ralphex.Capability != nil {
		capability := *input.Ralphex.Capability
		clone.Ralphex.Capability = &capability
	}
	if input.Ralphex.ExecutionState != nil {
		state := *input.Ralphex.ExecutionState
		clone.Ralphex.ExecutionState = &state
	}
	clone.Acceptance = cloneAcceptance(input.Acceptance)
	return clone
}

func cloneReviewPolicy(input ReviewPolicy) ReviewPolicy {
	input.Required = append([]ReviewRequirement(nil), input.Required...)
	return input
}

func cloneRepository(input RepositoryManifest) RepositoryManifest {
	clone := input
	clone.Remotes = make(map[string]string, len(input.Remotes))
	for name, url := range input.Remotes {
		clone.Remotes[name] = url
	}
	return clone
}

func cloneAcceptance(input []AcceptanceCommand) []AcceptanceCommand {
	clone := make([]AcceptanceCommand, len(input))
	for index, command := range input {
		clone[index] = command
		clone[index].Argv = append([]string(nil), command.Argv...)
	}
	return clone
}

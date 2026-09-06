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
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

// Manifest is the mutable, serializable input used to construct an Authority.
type Manifest struct {
	RunID         string              `json:"run_id"`
	Repository    RepositoryManifest  `json:"repository"`
	Plan          PlanManifest        `json:"plan"`
	Ralphex       RalphexManifest     `json:"ralphex"`
	Executor      ExecutorPolicy      `json:"executor"`
	Worktree      WorktreePolicy      `json:"worktree"`
	Acceptance    []AcceptanceCommand `json:"acceptance"`
	PolicyVersion string              `json:"policy_version"`
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

// RalphexManifest pins the executable, its source metadata, and invocation mode.
type RalphexManifest struct {
	BinaryPath   string       `json:"binary_path"`
	BinarySHA256 string       `json:"binary_sha256"`
	SourceSHA    string       `json:"source_sha,omitempty"`
	Mode         ralphex.Mode `json:"mode"`
	Timeout      string       `json:"timeout"`
	WaitOnLimit  string       `json:"wait_on_limit"`
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
		identityMatched = identityMatched || remoteIdentity(remoteURL) == manifest.Repository.Identity
	}
	if !identityMatched {
		return fmt.Errorf("repository.identity %q does not match any governed remote URL", manifest.Repository.Identity)
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
	clone.Acceptance = cloneAcceptance(input.Acceptance)
	return clone
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

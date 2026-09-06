// Package recovery inspects governed execution ownership without mutating
// process, worktree, or repository state.
package recovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
)

// ErrProcessIdentityUnsupported means the host cannot provide a strong
// process identity. Callers must treat this as ambiguous, never owner-dead.
var ErrProcessIdentityUnsupported = errors.New("strong process identity proof is unsupported on this platform")

// Classification is the controller's read-only conclusion about governed
// execution state. Only StaleOwnerDead is positive proof that the recorded
// process owner can no longer mutate the worktree.
type Classification string

const (
	ClassificationActive         Classification = "active"
	ClassificationStaleOwnerDead Classification = "stale-owner-dead"
	ClassificationMissing        Classification = "missing"
	ClassificationAmbiguous      Classification = "ambiguous"
)

// OwnerProof describes how process ownership contributed to a classification.
type OwnerProof string

const (
	OwnerProofMatching         OwnerProof = "matching-process-identity"
	OwnerProofProcessAbsent    OwnerProof = "process-absent"
	OwnerProofIdentityMismatch OwnerProof = "process-identity-mismatch"
	OwnerProofProcessExited    OwnerProof = "process-exited"
	OwnerProofUnavailable      OwnerProof = "proof-unavailable"
)

// AttemptIdentity preserves the controller identity hierarchy for one
// governed attempt. RunID and AttemptID are the minimum recovery identity;
// the other fields keep provenance linked when those scopes exist.
type AttemptIdentity struct {
	ProjectID      string `json:"project_id,omitempty"`
	PlanID         string `json:"plan_id,omitempty"`
	RunID          string `json:"run_id"`
	AttemptID      string `json:"attempt_id"`
	TaskID         string `json:"task_id,omitempty"`
	AgentSessionID string `json:"agent_session_id,omitempty"`
}

// ProcessIdentity is a durable identity for one process owner. LinuxStartTicks
// is the kernel start-time field from /proc/<pid>/stat. BootID prevents a
// start-time collision across host reboots.
type ProcessIdentity struct {
	PID             int    `json:"pid"`
	LinuxStartTicks uint64 `json:"linux_start_ticks"`
	BootID          string `json:"boot_id"`
}

// WorktreeIdentity pins the repository, controller-authorized worktree root,
// exact governed worktree, and branch that belong to an attempt.
type WorktreeIdentity struct {
	RepositoryPath string `json:"repository_path"`
	RootPath       string `json:"root_path"`
	Path           string `json:"path"`
	Branch         string `json:"branch"`
}

// Ownership is the controller-owned process and worktree metadata for one
// governed attempt.
type Ownership struct {
	Attempt  AttemptIdentity  `json:"attempt"`
	Process  ProcessIdentity  `json:"process"`
	Worktree WorktreeIdentity `json:"worktree"`
}

// Inspection is a read-only classification. Reason is safe diagnostic text;
// ObservedProcess is present only when the operating system returned a
// complete strong identity.
type Inspection struct {
	Classification  Classification   `json:"classification"`
	OwnerProof      OwnerProof       `json:"owner_proof"`
	Ownership       Ownership        `json:"ownership"`
	ObservedProcess *ProcessIdentity `json:"observed_process,omitempty"`
	ObservedBranch  string           `json:"observed_branch,omitempty"`
	Reason          string           `json:"reason,omitempty"`
}

// CaptureOwnership validates attempt metadata and canonical worktree paths,
// then captures a strong platform process identity.
func CaptureOwnership(attempt AttemptIdentity, worktree WorktreeIdentity, pid int) (Ownership, error) {
	if err := validateAttempt(attempt); err != nil {
		return Ownership{}, err
	}
	canonical, present, err := validateWorktreePaths(worktree)
	if err != nil {
		return Ownership{}, err
	}
	if !present {
		return Ownership{}, errors.New("governed worktree does not exist")
	}
	process, err := CaptureProcessIdentity(pid)
	if err != nil {
		return Ownership{}, fmt.Errorf("capture process identity: %w", err)
	}
	return Ownership{Attempt: attempt, Process: process, Worktree: canonical}, nil
}

// CaptureProcessIdentity records the platform-specific strong identity for a
// live process. Unsupported or incomplete identity capture returns an error.
func CaptureProcessIdentity(pid int) (ProcessIdentity, error) {
	observation, err := observeProcess(pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	if !observation.alive {
		return ProcessIdentity{}, errors.New("process is not live")
	}
	return observation.identity, nil
}

// Inspect verifies the governed worktree and branch before inspecting the
// recorded process owner. It never writes to the repository or signals a
// process. All invalid, unsupported, or uncertain states fail closed as
// ambiguous.
func Inspect(ctx context.Context, ownership Ownership) Inspection {
	result := Inspection{
		Classification: ClassificationAmbiguous,
		OwnerProof:     OwnerProofUnavailable,
		Ownership:      ownership,
	}
	if ctx == nil {
		result.Reason = "context is required"
		return result
	}
	if err := validateAttempt(ownership.Attempt); err != nil {
		result.Reason = err.Error()
		return result
	}
	worktree, present, err := validateWorktreePaths(ownership.Worktree)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	if !present {
		result.Classification = ClassificationMissing
		result.Reason = "governed worktree is missing"
		return result
	}
	result.Ownership.Worktree = worktree

	branch, err := inspectRegisteredWorktree(ctx, worktree)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	result.ObservedBranch = branch

	if err := validateProcessIdentity(ownership.Process); err != nil {
		result.Reason = err.Error()
		return result
	}
	observed, err := observeProcess(ownership.Process.PID)
	if errors.Is(err, errProcessNotFound) {
		result.Classification = ClassificationStaleOwnerDead
		result.OwnerProof = OwnerProofProcessAbsent
		result.Reason = "recorded process no longer exists"
		return result
	}
	if err != nil {
		result.Reason = fmt.Sprintf("inspect process identity: %v", err)
		return result
	}
	observedIdentity := observed.identity
	result.ObservedProcess = &observedIdentity
	if observed.identity != ownership.Process {
		result.Classification = ClassificationStaleOwnerDead
		result.OwnerProof = OwnerProofIdentityMismatch
		result.Reason = "PID is occupied by a different process identity"
		return result
	}
	if !observed.alive {
		result.Classification = ClassificationStaleOwnerDead
		result.OwnerProof = OwnerProofProcessExited
		result.Reason = "recorded process has exited"
		return result
	}
	result.Classification = ClassificationActive
	result.OwnerProof = OwnerProofMatching
	result.Reason = "recorded process identity is live"
	return result
}

func validateAttempt(attempt AttemptIdentity) error {
	if strings.TrimSpace(attempt.RunID) == "" || strings.TrimSpace(attempt.AttemptID) == "" {
		return errors.New("run ID and attempt ID are required")
	}
	for name, value := range map[string]string{
		"project ID": attempt.ProjectID, "plan ID": attempt.PlanID,
		"run ID": attempt.RunID, "attempt ID": attempt.AttemptID,
		"task ID": attempt.TaskID, "agent session ID": attempt.AgentSessionID,
	} {
		if value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s must not contain surrounding whitespace or newlines", name)
		}
	}
	return nil
}

func validateProcessIdentity(identity ProcessIdentity) error {
	if identity.PID <= 0 || identity.LinuxStartTicks == 0 || strings.TrimSpace(identity.BootID) == "" {
		return errors.New("complete strong process identity is required")
	}
	return nil
}

func validateWorktreePaths(worktree WorktreeIdentity) (WorktreeIdentity, bool, error) {
	if worktree.RepositoryPath == "" || worktree.RootPath == "" || worktree.Path == "" || worktree.Branch == "" {
		return WorktreeIdentity{}, false, errors.New("repository path, worktree root, worktree path, and branch are required")
	}
	if worktree.Branch != strings.TrimSpace(worktree.Branch) || strings.HasPrefix(worktree.Branch, "-") || strings.ContainsAny(worktree.Branch, "\r\n") {
		return WorktreeIdentity{}, false, errors.New("branch must be a non-option name without surrounding whitespace or newlines")
	}
	if !filepath.IsAbs(worktree.RepositoryPath) || !filepath.IsAbs(worktree.RootPath) || !filepath.IsAbs(worktree.Path) {
		return WorktreeIdentity{}, false, errors.New("repository, worktree root, and worktree paths must be absolute")
	}
	repositoryPath := filepath.Clean(worktree.RepositoryPath)
	rootPath := filepath.Clean(worktree.RootPath)
	worktreePath := filepath.Clean(worktree.Path)
	if repositoryPath != worktree.RepositoryPath || rootPath != worktree.RootPath || worktreePath != worktree.Path {
		return WorktreeIdentity{}, false, errors.New("repository, worktree root, and worktree paths must be clean canonical paths")
	}
	repositoryInfo, err := os.Lstat(repositoryPath)
	if err != nil {
		return WorktreeIdentity{}, false, fmt.Errorf("inspect repository path: %w", err)
	}
	if !repositoryInfo.IsDir() || repositoryInfo.Mode()&os.ModeSymlink != 0 {
		return WorktreeIdentity{}, false, errors.New("repository path must be a real directory, not a symlink")
	}
	resolvedRepository, err := filepath.EvalSymlinks(repositoryPath)
	if err != nil || resolvedRepository != repositoryPath {
		return WorktreeIdentity{}, false, errors.New("repository path must not traverse symlinks")
	}

	rootInfo, err := os.Lstat(rootPath)
	if err != nil {
		return WorktreeIdentity{}, false, fmt.Errorf("inspect worktree root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return WorktreeIdentity{}, false, errors.New("worktree root must be a real directory, not a symlink")
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootPath)
	if err != nil || resolvedRoot != rootPath {
		return WorktreeIdentity{}, false, errors.New("worktree root must not traverse symlinks")
	}

	contained, err := pathContained(rootPath, worktreePath)
	if err != nil || !contained {
		return WorktreeIdentity{}, false, errors.New("worktree path escapes the governed worktree root")
	}
	present, err := rejectSymlinkComponents(rootPath, worktreePath)
	if err != nil {
		return WorktreeIdentity{}, false, err
	}
	if !present {
		return WorktreeIdentity{RepositoryPath: repositoryPath, RootPath: rootPath, Path: worktreePath, Branch: worktree.Branch}, false, nil
	}
	info, err := os.Lstat(worktreePath)
	if err != nil {
		return WorktreeIdentity{}, false, fmt.Errorf("inspect worktree path: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return WorktreeIdentity{}, false, errors.New("worktree path must be a real directory, not a symlink")
	}
	resolvedWorktree, err := filepath.EvalSymlinks(worktreePath)
	if err != nil || resolvedWorktree != worktreePath {
		return WorktreeIdentity{}, false, errors.New("worktree path must not traverse symlinks")
	}
	return WorktreeIdentity{RepositoryPath: repositoryPath, RootPath: rootPath, Path: worktreePath, Branch: worktree.Branch}, true, nil
}

func pathContained(root, path string) (bool, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

func rejectSymlinkComponents(root, path string) (bool, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false, fmt.Errorf("resolve worktree path: %w", err)
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "." || component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("inspect worktree path component: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("worktree path must not traverse symlinks")
		}
	}
	return true, nil
}

func inspectRegisteredWorktree(ctx context.Context, worktree WorktreeIdentity) (string, error) {
	command := exec.CommandContext(ctx, "git", "-C", worktree.RepositoryPath, "worktree", "list", "--porcelain", "-z")
	command.Env = gitexec.Environment()
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("inspect registered Git worktrees: %w", err)
	}
	branch, found, err := parseWorktreeBranch(output, worktree.Path)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("governed path is not a registered Git worktree")
	}
	if branch == "" {
		return "", errors.New("governed Git worktree is detached or has no branch identity")
	}
	if branch != worktree.Branch {
		return branch, fmt.Errorf("governed worktree branch mismatch: observed %q", branch)
	}
	return branch, nil
}

func parseWorktreeBranch(output []byte, target string) (string, bool, error) {
	var path, branch string
	flush := func() (string, bool) {
		if path == target {
			return branch, true
		}
		return "", false
	}
	for _, line := range strings.Split(string(output), "\x00") {
		if line == "" {
			if foundBranch, found := flush(); found {
				return foundBranch, true, nil
			}
			path, branch = "", ""
			continue
		}
		key, value, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		switch key {
		case "worktree":
			path = value
		case "branch":
			const heads = "refs/heads/"
			if !strings.HasPrefix(value, heads) {
				return "", false, fmt.Errorf("unexpected Git branch ref %q", value)
			}
			branch = strings.TrimPrefix(value, heads)
		}
	}
	if foundBranch, found := flush(); found {
		return foundBranch, true, nil
	}
	return "", false, nil
}

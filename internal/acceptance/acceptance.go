// Package acceptance independently evaluates a governed branch under controller
// authority. It never derives acceptance from the implementation subprocess.
package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

// Status is the controller's conclusion after independent branch acceptance.
type Status string

const (
	StatusPass        Status = "PASS"
	StatusFail        Status = "FAIL"
	StatusUnavailable Status = "UNAVAILABLE"
)

// CommandRunner is the structured subprocess operation used by the executor.
// supervisor.Runner satisfies this interface.
type CommandRunner interface {
	Run(context.Context, supervisor.Command) (supervisor.Result, error)
}

// CommandResult records one configured acceptance command and its policy
// metadata. Process contains the exact argv, cwd, timestamps, exit/signal
// semantics, and stdout/stderr evidence references.
type CommandResult struct {
	Index         int
	Name          string
	Class         string
	Required      bool
	PolicyVersion string
	Process       supervisor.Result
	MetadataRef   ledger.EvidenceRef
}

// GitEvidence is the controller-captured final repository state. All Git
// operations are structured argv executions with their own process evidence.
type GitEvidence struct {
	Branch        string
	HeadSHA       string
	Dirty         bool
	HeadProcess   supervisor.Result
	StatusProcess supervisor.Result
	BranchProcess supervisor.Result
	MetadataRef   ledger.EvidenceRef
}

// Result has no exported PASS field. Passed rechecks all invariants, ensuring a
// caller cannot obtain BRANCH_ACCEPTED without complete controller evidence.
type Result struct {
	status        Status
	commands      []CommandResult
	git           GitEvidence
	gitCaptured   bool
	gitAttempted  bool
	failureReason string
}

// Status returns the acceptance conclusion.
func (r Result) Status() Status {
	if r.status == StatusPass && !r.Passed() {
		return StatusFail
	}
	return r.status
}

// Passed reports whether every required command passed and complete final Git
// evidence was captured.
func (r Result) Passed() bool {
	if r.status != StatusPass || !r.gitCaptured || r.git.HeadSHA == "" || !validEvidenceRef(r.git.MetadataRef) {
		return false
	}
	if r.git.HeadProcess.Outcome != supervisor.OutcomeSucceeded || r.git.StatusProcess.Outcome != supervisor.OutcomeSucceeded ||
		r.git.BranchProcess.Outcome != supervisor.OutcomeSucceeded {
		return false
	}
	if !completeProcessEvidence(r.git.HeadProcess) || !completeProcessEvidence(r.git.StatusProcess) ||
		!completeProcessEvidence(r.git.BranchProcess) {
		return false
	}
	for _, command := range r.commands {
		if !validEvidenceRef(command.MetadataRef) || !completeProcessEvidence(command.Process) {
			return false
		}
		if command.Required && command.Process.Outcome != supervisor.OutcomeSucceeded {
			return false
		}
	}
	return true
}

// Commands returns a copy of the configured command records.
func (r Result) Commands() []CommandResult {
	commands := make([]CommandResult, len(r.commands))
	for index, command := range r.commands {
		commands[index] = cloneCommandResult(command)
	}
	return commands
}

// FinalGit returns a copy of final Git evidence when it was fully captured.
func (r Result) FinalGit() (GitEvidence, bool) {
	if !r.gitCaptured {
		return GitEvidence{}, false
	}
	return cloneGitEvidence(r.git), true
}

// FailureReason describes the first failed or unavailable gate.
func (r Result) FailureReason() string {
	return r.failureReason
}

// EvidenceRefs returns every successfully published artifact, including
// partial command or Git evidence from an unavailable acceptance attempt.
func (r Result) EvidenceRefs() []ledger.EvidenceRef {
	var refs []ledger.EvidenceRef
	for _, command := range r.commands {
		refs = appendValidRefs(refs, command.Process.StdoutRef, command.Process.StderrRef, command.MetadataRef)
	}
	if r.gitAttempted {
		refs = appendValidRefs(refs,
			r.git.HeadProcess.StdoutRef, r.git.HeadProcess.StderrRef,
			r.git.StatusProcess.StdoutRef, r.git.StatusProcess.StderrRef,
			r.git.BranchProcess.StdoutRef, r.git.BranchProcess.StderrRef,
			r.git.MetadataRef,
		)
	}
	return refs
}

// Target identifies the checkout and candidate branch being accepted.
type Target struct {
	RepositoryPath string `json:"repository_path"`
	Branch         string `json:"branch"`
	HeadSHA        string `json:"head_sha"`
}

// Executor runs acceptance commands and captures immutable evidence.
type Executor struct {
	runner    CommandRunner
	artifacts supervisor.ArtifactWriter
}

// New returns an independent branch acceptance executor.
func New(runner CommandRunner, artifacts supervisor.ArtifactWriter) *Executor {
	return &Executor{runner: runner, artifacts: artifacts}
}

// Run executes the commands frozen in governed authority. Required command
// failure stops evaluation immediately. Optional failures remain recorded but
// do not prevent PASS when all required checks and final Git capture succeed.
func (e *Executor) Run(ctx context.Context, governed authority.Authority, target Target) (Result, error) {
	result := Result{status: StatusUnavailable}
	if ctx == nil {
		return result, errors.New("context is required")
	}
	if e == nil || e.runner == nil {
		return result, errors.New("command runner is required")
	}
	if e.artifacts == nil {
		return result, errors.New("evidence writer is required")
	}

	repository := target.RepositoryPath
	commands := governed.Acceptance()
	if repository == "" || target.Branch == "" || target.HeadSHA == "" || len(commands) == 0 {
		return result, errors.New("validated authority with candidate repository, branch, HEAD, and acceptance commands is required")
	}

	for index, configured := range commands {
		process, err := e.runCommand(ctx, repository, index, configured)
		record := CommandResult{
			Index:         index + 1,
			Name:          configured.Name,
			Class:         configured.Class,
			Required:      configured.Required,
			PolicyVersion: governed.PolicyVersion(),
			Process:       process,
		}
		result.commands = append(result.commands, record)
		if err != nil {
			result.failureReason = fmt.Sprintf("acceptance command %d unavailable: %v", index+1, err)
			return result, fmt.Errorf("run acceptance command %d: %w", index+1, err)
		}
		metadataRef, err := e.writeCommandMetadata(record)
		if err != nil {
			result.failureReason = fmt.Sprintf("acceptance command %d metadata unavailable: %v", index+1, err)
			return result, err
		}
		result.commands[len(result.commands)-1].MetadataRef = metadataRef

		if configured.Required && process.Outcome != supervisor.OutcomeSucceeded {
			result.status = StatusFail
			result.failureReason = fmt.Sprintf("required acceptance command %d failed with outcome %s", index+1, process.Outcome)
			return result, nil
		}
	}

	result.gitAttempted = true
	gitEvidence, err := e.captureGit(ctx, repository, target.Branch, target.HeadSHA, governed.PolicyVersion())
	result.git = gitEvidence
	if err != nil {
		result.failureReason = fmt.Sprintf("final Git evidence unavailable: %v", err)
		return result, err
	}
	result.git = gitEvidence
	result.gitCaptured = true
	result.status = StatusPass
	if !result.Passed() {
		result.status = StatusUnavailable
		result.failureReason = "acceptance evidence is incomplete"
		return result, errors.New(result.failureReason)
	}
	return result, nil
}

func (e *Executor) runCommand(ctx context.Context, repository string, index int, configured authority.AcceptanceCommand) (supervisor.Result, error) {
	prefix := fmt.Sprintf("acceptance-command-%03d", index+1)
	timeout, err := time.ParseDuration(configured.Timeout)
	if err != nil {
		return supervisor.Result{}, fmt.Errorf("parse governed timeout: %w", err)
	}
	return e.runner.Run(ctx, supervisor.Command{
		Argv:    append([]string(nil), configured.Argv...),
		Cwd:     repository,
		Timeout: timeout,
		Stdout: supervisor.EvidenceSink{
			Writer: e.artifacts,
			Name:   prefix + "-stdout.log",
			Kind:   "acceptance-command-stdout",
		},
		Stderr: supervisor.EvidenceSink{
			Writer: e.artifacts,
			Name:   prefix + "-stderr.log",
			Kind:   "acceptance-command-stderr",
		},
	})
}

func (e *Executor) writeCommandMetadata(record CommandResult) (ledger.EvidenceRef, error) {
	data, err := json.Marshal(struct {
		Index         int                  `json:"index"`
		Name          string               `json:"name,omitempty"`
		Class         string               `json:"class,omitempty"`
		Required      bool                 `json:"required"`
		PolicyVersion string               `json:"policy_version"`
		Process       supervisor.Result    `json:"process"`
		EvidenceRefs  []ledger.EvidenceRef `json:"evidence_refs"`
	}{
		Index:         record.Index,
		Name:          record.Name,
		Class:         record.Class,
		Required:      record.Required,
		PolicyVersion: record.PolicyVersion,
		Process:       record.Process,
		EvidenceRefs:  []ledger.EvidenceRef{record.Process.StdoutRef, record.Process.StderrRef},
	})
	if err != nil {
		return ledger.EvidenceRef{}, fmt.Errorf("marshal acceptance command metadata: %w", err)
	}
	name := fmt.Sprintf("acceptance-command-%03d.json", record.Index)
	ref, err := e.artifacts.WriteBytes(name, "acceptance-command-metadata", data)
	if err != nil {
		return ledger.EvidenceRef{}, fmt.Errorf("publish acceptance command metadata: %w", err)
	}
	return ref, nil
}

func (e *Executor) captureGit(ctx context.Context, repository, branch, expectedHeadSHA, policyVersion string) (GitEvidence, error) {
	gitEvidence := GitEvidence{Branch: branch}
	head, err := e.runGit(ctx, repository, "head", []string{"git", "rev-parse", "--verify", "HEAD"})
	gitEvidence.HeadProcess = head
	if err != nil {
		return gitEvidence, fmt.Errorf("capture HEAD: %w", err)
	}
	if head.Outcome != supervisor.OutcomeSucceeded {
		return gitEvidence, fmt.Errorf("capture HEAD: git outcome %s", head.Outcome)
	}
	headBytes, err := readVerifiedArtifact(head.StdoutRef)
	if err != nil {
		return gitEvidence, fmt.Errorf("read HEAD evidence: %w", err)
	}
	headSHA := strings.TrimSpace(string(headBytes))
	if !validGitSHA(headSHA) {
		return gitEvidence, fmt.Errorf("capture HEAD: invalid Git object ID %q", headSHA)
	}
	gitEvidence.HeadSHA = headSHA
	if headSHA != expectedHeadSHA {
		return gitEvidence, fmt.Errorf("capture HEAD: candidate changed from %s to %s during acceptance", expectedHeadSHA, headSHA)
	}

	status, err := e.runGit(ctx, repository, "status", []string{"git", "status", "--porcelain=v1", "--untracked-files=normal"})
	gitEvidence.StatusProcess = status
	if err != nil {
		return gitEvidence, fmt.Errorf("capture status: %w", err)
	}
	if status.Outcome != supervisor.OutcomeSucceeded {
		return gitEvidence, fmt.Errorf("capture status: git outcome %s", status.Outcome)
	}
	statusBytes, err := readVerifiedArtifact(status.StdoutRef)
	if err != nil {
		return gitEvidence, fmt.Errorf("read status evidence: %w", err)
	}

	gitEvidence.Dirty = len(statusBytes) > 0
	branchRef := "refs/heads/" + branch
	branchProcess, err := e.runGit(ctx, repository, "branch", []string{"git", "show-ref", "--verify", "--hash", branchRef})
	gitEvidence.BranchProcess = branchProcess
	if err != nil {
		return gitEvidence, fmt.Errorf("capture candidate branch: %w", err)
	}
	if branchProcess.Outcome != supervisor.OutcomeSucceeded {
		return gitEvidence, fmt.Errorf("capture candidate branch: git outcome %s", branchProcess.Outcome)
	}
	branchBytes, err := readVerifiedArtifact(branchProcess.StdoutRef)
	if err != nil {
		return gitEvidence, fmt.Errorf("read candidate branch evidence: %w", err)
	}
	branchSHA := strings.TrimSpace(string(branchBytes))
	if !validGitSHA(branchSHA) || branchSHA != expectedHeadSHA {
		return gitEvidence, fmt.Errorf("candidate branch %q points to %q, expected %s", branch, branchSHA, expectedHeadSHA)
	}

	metadata, err := json.Marshal(struct {
		Branch        string            `json:"branch,omitempty"`
		HeadSHA       string            `json:"head_sha"`
		Dirty         bool              `json:"dirty"`
		PolicyVersion string            `json:"policy_version"`
		HeadProcess   supervisor.Result `json:"head_process"`
		StatusProcess supervisor.Result `json:"status_process"`
		BranchProcess supervisor.Result `json:"branch_process"`
	}{
		Branch:        gitEvidence.Branch,
		HeadSHA:       gitEvidence.HeadSHA,
		Dirty:         gitEvidence.Dirty,
		PolicyVersion: policyVersion,
		HeadProcess:   gitEvidence.HeadProcess,
		StatusProcess: gitEvidence.StatusProcess,
		BranchProcess: gitEvidence.BranchProcess,
	})
	if err != nil {
		return gitEvidence, fmt.Errorf("marshal final Git metadata: %w", err)
	}
	gitEvidence.MetadataRef, err = e.artifacts.WriteBytes("acceptance-final-git.json", "acceptance-final-git", metadata)
	if err != nil {
		return gitEvidence, fmt.Errorf("publish final Git metadata: %w", err)
	}
	return gitEvidence, nil
}

func appendValidRefs(refs []ledger.EvidenceRef, candidates ...ledger.EvidenceRef) []ledger.EvidenceRef {
	for _, ref := range candidates {
		if validEvidenceRef(ref) {
			refs = append(refs, ref)
		}
	}
	return refs
}

func (e *Executor) runGit(ctx context.Context, repository, label string, argv []string) (supervisor.Result, error) {
	return e.runner.Run(ctx, supervisor.Command{
		Argv: argv,
		Cwd:  repository,
		Stdout: supervisor.EvidenceSink{
			Writer: e.artifacts,
			Name:   "acceptance-git-" + label + "-stdout.log",
			Kind:   "acceptance-git-stdout",
		},
		Stderr: supervisor.EvidenceSink{
			Writer: e.artifacts,
			Name:   "acceptance-git-" + label + "-stderr.log",
			Kind:   "acceptance-git-stderr",
		},
	})
}

func readVerifiedArtifact(ref ledger.EvidenceRef) ([]byte, error) {
	if !validEvidenceRef(ref) {
		return nil, errors.New("incomplete evidence reference")
	}
	data, err := os.ReadFile(ref.URI)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != ref.SHA256 {
		return nil, errors.New("evidence SHA256 mismatch")
	}
	return data, nil
}

func validGitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !unicode.Is(unicode.ASCII_Hex_Digit, character) {
			return false
		}
	}
	return true
}

func completeProcessEvidence(process supervisor.Result) bool {
	return len(process.Argv) > 0 && process.Cwd != "" && !process.StartedAt.IsZero() && !process.EndedAt.IsZero() &&
		validEvidenceRef(process.StdoutRef) && validEvidenceRef(process.StderrRef)
}

func validEvidenceRef(ref ledger.EvidenceRef) bool {
	return ref.URI != "" && ref.SHA256 != "" && ref.Kind != ""
}

func cloneCommandResult(command CommandResult) CommandResult {
	command.Process.Argv = append([]string(nil), command.Process.Argv...)
	return command
}

func cloneGitEvidence(git GitEvidence) GitEvidence {
	git.HeadProcess.Argv = append([]string(nil), git.HeadProcess.Argv...)
	git.StatusProcess.Argv = append([]string(nil), git.StatusProcess.Argv...)
	git.BranchProcess.Argv = append([]string(nil), git.BranchProcess.Argv...)
	return git
}

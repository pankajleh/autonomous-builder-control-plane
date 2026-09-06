// Package run orchestrates one governed EP-002 Ralphex lifecycle. Its
// authority ends at BRANCH_ACCEPTED; integration and completion states are
// deliberately outside this package.
package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/acceptance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

const (
	eventStateTransition = "STATE_TRANSITION"
	actorController      = "control-plane"
)

// EventAppender is the durable, append-only operation required by a Runner.
// ledger.JSONLLedger satisfies this interface.
type EventAppender interface {
	Append(ledger.Event) error
}

// CommandRunner is the supervised structured-command operation required by a
// Runner. supervisor.Runner satisfies this interface.
type CommandRunner interface {
	Run(context.Context, supervisor.Command) (supervisor.Result, error)
}

// Result is the terminal EP-002 conclusion and its controller-owned evidence.
type Result struct {
	State                 domain.State
	Ralphex               supervisor.Result
	RalphexMetadataRef    ledger.EvidenceRef
	Acceptance            acceptance.Result
	AuthorityEvidenceRef  ledger.EvidenceRef
	ValidationEvidenceRef ledger.EvidenceRef
	FailureReason         string
}

// Accepted reports whether independent controller acceptance produced the
// terminal state BRANCH_ACCEPTED.
func (r Result) Accepted() bool {
	return r.State == domain.StateBranchAccepted && r.Acceptance.Passed()
}

// Runner owns the state transitions for one validated authority.
type Runner struct {
	governed  authority.Authority
	events    EventAppender
	artifacts supervisor.ArtifactWriter
	processes CommandRunner
}

// New constructs an EP-002 runner from validated authority and explicit
// ledger, evidence, and subprocess dependencies.
func New(governed authority.Authority, events EventAppender, artifacts supervisor.ArtifactWriter, processes CommandRunner) (*Runner, error) {
	if governed.RunID() == "" || governed.SHA256() == "" {
		return nil, errors.New("validated authority is required")
	}
	if events == nil {
		return nil, errors.New("event ledger is required")
	}
	if artifacts == nil {
		return nil, errors.New("evidence writer is required")
	}
	if processes == nil {
		return nil, errors.New("process supervisor is required")
	}
	return &Runner{governed: governed, events: events, artifacts: artifacts, processes: processes}, nil
}

// Run executes exactly one governed implementation and branch-acceptance
// lifecycle. Process and acceptance failures are represented in Result;
// errors indicate that the controller itself could not preserve governance.
func (r *Runner) Run(ctx context.Context) (Result, error) {
	var result Result
	if ctx == nil {
		return result, errors.New("context is required")
	}

	authorityRef, err := r.artifacts.WriteBytes("authority.json", "validated-authority", r.governed.CanonicalJSON())
	if err != nil {
		return result, fmt.Errorf("publish authority evidence: %w", err)
	}
	result.AuthorityEvidenceRef = authorityRef
	if err := r.appendCreated(authorityRef); err != nil {
		return result, err
	}
	result.State = domain.StateRunCreated

	validation, err := validatePinnedIdentity(ctx, r.governed)
	if err != nil {
		result.State = domain.StateFailed
		result.FailureReason = err.Error()
		if appendErr := r.transition(domain.StateRunCreated, domain.StateFailed, "authority-validator", map[string]any{"reason": err.Error()}, []ledger.EvidenceRef{authorityRef}); appendErr != nil {
			return result, errors.Join(err, appendErr)
		}
		return result, fmt.Errorf("validate pinned authority: %w", err)
	}
	validationRef, err := r.writeJSON("authority-validation.json", "authority-validation", validation)
	if err != nil {
		return result, fmt.Errorf("publish authority validation evidence: %w", err)
	}
	result.ValidationEvidenceRef = validationRef
	if err := r.transition(domain.StateRunCreated, domain.StateAuthorityValidated, "authority-validator", map[string]any{
		"authority_sha256": r.governed.SHA256(),
	}, []ledger.EvidenceRef{authorityRef, validationRef}); err != nil {
		return result, err
	}
	result.State = domain.StateAuthorityValidated

	invocation, err := r.invocation()
	if err != nil {
		return r.fail(result, domain.StateAuthorityValidated, "ralphex-adapter", err, nil)
	}
	argv, err := invocation.Argv()
	if err != nil {
		return r.fail(result, domain.StateAuthorityValidated, "ralphex-adapter", err, nil)
	}
	if err := r.transition(domain.StateAuthorityValidated, domain.StateExecutionStarting, "governed-runner", map[string]any{"argv": argv}, nil); err != nil {
		return result, err
	}
	result.State = domain.StateExecutionStarting
	if err := r.transition(domain.StateExecutionStarting, domain.StateImplementing, "ralphex-adapter", map[string]any{"argv": argv}, nil); err != nil {
		return result, err
	}
	result.State = domain.StateImplementing

	process, processErr := r.processes.Run(ctx, supervisor.Command{
		Argv:   argv,
		Cwd:    r.governed.Repository().Path,
		Stdout: supervisor.EvidenceSink{Writer: r.artifacts, Name: "ralphex-stdout.log", Kind: "ralphex-stdout"},
		Stderr: supervisor.EvidenceSink{Writer: r.artifacts, Name: "ralphex-stderr.log", Kind: "ralphex-stderr"},
	})
	result.Ralphex = process
	if processErr != nil {
		return r.fail(result, domain.StateImplementing, "ralphex-adapter", processErr, processRefs(process))
	}
	metadataRef, err := r.writeJSON("ralphex-process.json", "ralphex-process-metadata", struct {
		Process      supervisor.Result    `json:"process"`
		EvidenceRefs []ledger.EvidenceRef `json:"evidence_refs"`
	}{Process: process, EvidenceRefs: processRefs(process)})
	if err != nil {
		return r.fail(result, domain.StateImplementing, "ralphex-adapter", err, processRefs(process))
	}
	result.RalphexMetadataRef = metadataRef
	implementationRefs := append(processRefs(process), metadataRef)

	if process.Outcome != supervisor.OutcomeSucceeded {
		terminal := domain.StateFailed
		if process.Outcome == supervisor.OutcomeCanceled {
			terminal = domain.StateCancelled
		}
		reason := fmt.Sprintf("Ralphex ended with outcome %s", process.Outcome)
		result.State = terminal
		result.FailureReason = reason
		if err := r.transition(domain.StateImplementing, terminal, "ralphex-adapter", map[string]any{
			"outcome": process.Outcome, "exit_code": process.ExitCode, "signal": process.TerminatingSignal,
		}, implementationRefs); err != nil {
			return result, err
		}
		return result, nil
	}

	if err := r.transition(domain.StateImplementing, domain.StateImplementationCompleted, "ralphex-adapter", map[string]any{
		"outcome": process.Outcome, "exit_code": process.ExitCode,
	}, implementationRefs); err != nil {
		return result, err
	}
	result.State = domain.StateImplementationCompleted
	if err := r.transition(domain.StateImplementationCompleted, domain.StateBranchAcceptancePending, "acceptance-controller", nil, implementationRefs); err != nil {
		return result, err
	}
	result.State = domain.StateBranchAcceptancePending

	accepted, acceptanceErr := acceptance.New(r.processes, r.artifacts).Run(ctx, r.governed)
	result.Acceptance = accepted
	acceptanceRefs := collectAcceptanceRefs(accepted)
	if acceptanceErr != nil {
		result.State = domain.StateValidationUnavailable
		result.FailureReason = accepted.FailureReason()
		if result.FailureReason == "" {
			result.FailureReason = acceptanceErr.Error()
		}
		if err := r.transition(domain.StateBranchAcceptancePending, domain.StateValidationUnavailable, "acceptance-controller", map[string]any{
			"status": accepted.Status(), "reason": result.FailureReason,
		}, acceptanceRefs); err != nil {
			return result, errors.Join(acceptanceErr, err)
		}
		return result, fmt.Errorf("run branch acceptance: %w", acceptanceErr)
	}
	if !accepted.Passed() {
		result.State = domain.StateFailed
		result.FailureReason = accepted.FailureReason()
		if err := r.transition(domain.StateBranchAcceptancePending, domain.StateFailed, "acceptance-controller", map[string]any{
			"status": accepted.Status(), "reason": result.FailureReason,
		}, acceptanceRefs); err != nil {
			return result, err
		}
		return result, nil
	}

	if err := r.transition(domain.StateBranchAcceptancePending, domain.StateBranchAccepted, "acceptance-controller", map[string]any{
		"status": accepted.Status(),
	}, acceptanceRefs); err != nil {
		return result, err
	}
	result.State = domain.StateBranchAccepted
	return result, nil
}

func (r *Runner) invocation() (ralphex.Invocation, error) {
	policy := r.governed.Executor()
	executor := strings.ToLower(strings.TrimSpace(policy.Executor))
	if executor != "" && executor != "claude" && executor != "codex" {
		return ralphex.Invocation{}, fmt.Errorf("unsupported executor %q", executor)
	}
	return ralphex.Invocation{
		BinaryPath:   r.governed.Ralphex().BinaryPath,
		PlanPath:     r.governed.Plan().Path,
		Mode:         r.governed.Ralphex().Mode,
		Codex:        executor == "codex",
		Worktree:     r.governed.Worktree().Enabled,
		TaskModel:    policy.TaskModel,
		TaskEffort:   policy.TaskEffort,
		ReviewModel:  policy.ReviewModel,
		ReviewEffort: policy.ReviewEffort,
	}, nil
}

func (r *Runner) appendCreated(authorityRef ledger.EvidenceRef) error {
	event, err := ledger.NewEvent(r.governed.RunID(), string(domain.StateRunCreated), actorController, "governed-runner")
	if err != nil {
		return fmt.Errorf("create RUN_CREATED event: %w", err)
	}
	event.Payload = map[string]any{"state": domain.StateRunCreated, "authority_sha256": r.governed.SHA256()}
	event.EvidenceRefs = []ledger.EvidenceRef{authorityRef}
	if err := r.events.Append(event); err != nil {
		return fmt.Errorf("append RUN_CREATED event: %w", err)
	}
	return nil
}

func (r *Runner) transition(from, to domain.State, source string, payload map[string]any, refs []ledger.EvidenceRef) error {
	if !ep002State(to) {
		return fmt.Errorf("EP-002 runner cannot transition to %s", to)
	}
	event, err := ledger.NewEvent(r.governed.RunID(), eventStateTransition, actorController, source)
	if err != nil {
		return fmt.Errorf("create %s transition: %w", to, err)
	}
	event.StateFrom = from
	event.StateTo = to
	event.Payload = payload
	event.EvidenceRefs = append([]ledger.EvidenceRef(nil), refs...)
	if err := r.events.Append(event); err != nil {
		return fmt.Errorf("append %s transition: %w", to, err)
	}
	return nil
}

func (r *Runner) fail(result Result, from domain.State, source string, cause error, refs []ledger.EvidenceRef) (Result, error) {
	result.State = domain.StateFailed
	result.FailureReason = cause.Error()
	if err := r.transition(from, domain.StateFailed, source, map[string]any{"reason": cause.Error()}, refs); err != nil {
		return result, errors.Join(cause, err)
	}
	return result, cause
}

func (r *Runner) writeJSON(name, kind string, value any) (ledger.EvidenceRef, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return ledger.EvidenceRef{}, err
	}
	return r.artifacts.WriteBytes(name, kind, data)
}

func ep002State(state domain.State) bool {
	switch state {
	case domain.StateAuthorityValidated, domain.StateExecutionStarting, domain.StateImplementing,
		domain.StateImplementationCompleted, domain.StateBranchAcceptancePending, domain.StateBranchAccepted,
		domain.StateValidationUnavailable, domain.StateFailed, domain.StateCancelled:
		return true
	default:
		return false
	}
}

type identityValidation struct {
	RepositoryPath  string            `json:"repository_path"`
	RepositoryID    string            `json:"repository_identity,omitempty"`
	HeadSHA         string            `json:"head_sha"`
	Branch          string            `json:"branch,omitempty"`
	Remotes         map[string]string `json:"remotes,omitempty"`
	PlanSHA256      string            `json:"plan_sha256"`
	BinarySHA256    string            `json:"ralphex_binary_sha256"`
	AuthoritySHA256 string            `json:"authority_sha256"`
}

func validatePinnedIdentity(ctx context.Context, governed authority.Authority) (identityValidation, error) {
	repository := governed.Repository()
	plan := governed.Plan()
	binary := governed.Ralphex()
	validation := identityValidation{
		RepositoryPath:  repository.Path,
		RepositoryID:    repository.Identity,
		Remotes:         make(map[string]string),
		AuthoritySHA256: governed.SHA256(),
	}

	var err error
	validation.PlanSHA256, err = hashFile(plan.Path)
	if err != nil {
		return validation, fmt.Errorf("hash governed plan: %w", err)
	}
	if validation.PlanSHA256 != plan.SHA256 {
		return validation, errors.New("governed plan SHA256 changed after authority validation")
	}
	validation.BinarySHA256, err = hashFile(binary.BinaryPath)
	if err != nil {
		return validation, fmt.Errorf("hash Ralphex binary: %w", err)
	}
	if validation.BinarySHA256 != binary.BinarySHA256 {
		return validation, errors.New("Ralphex binary SHA256 changed after authority validation")
	}

	validation.HeadSHA, err = gitOutput(ctx, repository.Path, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return validation, fmt.Errorf("resolve repository HEAD: %w", err)
	}
	if validation.HeadSHA != repository.StartSHA {
		return validation, fmt.Errorf("repository HEAD %s does not match governed start SHA %s", validation.HeadSHA, repository.StartSHA)
	}
	if repository.DefaultBranch != "" {
		validation.Branch, err = gitOutput(ctx, repository.Path, "symbolic-ref", "--quiet", "--short", "HEAD")
		if err != nil {
			return validation, fmt.Errorf("resolve repository branch: %w", err)
		}
		if validation.Branch != repository.DefaultBranch {
			return validation, fmt.Errorf("repository branch %q does not match governed default branch %q", validation.Branch, repository.DefaultBranch)
		}
	}

	names := make([]string, 0, len(repository.Remotes))
	for name := range repository.Remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		actual, remoteErr := gitOutput(ctx, repository.Path, "remote", "get-url", "--all", name)
		if remoteErr != nil {
			return validation, fmt.Errorf("resolve repository remote %q: %w", name, remoteErr)
		}
		if actual != repository.Remotes[name] {
			return validation, fmt.Errorf("repository remote %q does not match governed URL", name)
		}
		validation.Remotes[name] = actual
	}
	return validation, nil
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func gitOutput(ctx context.Context, repository string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = repository
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func processRefs(process supervisor.Result) []ledger.EvidenceRef {
	refs := make([]ledger.EvidenceRef, 0, 2)
	if process.StdoutRef.URI != "" {
		refs = append(refs, process.StdoutRef)
	}
	if process.StderrRef.URI != "" {
		refs = append(refs, process.StderrRef)
	}
	return refs
}

func collectAcceptanceRefs(result acceptance.Result) []ledger.EvidenceRef {
	var refs []ledger.EvidenceRef
	for _, command := range result.Commands() {
		refs = append(refs, command.Process.StdoutRef, command.Process.StderrRef, command.MetadataRef)
	}
	if git, ok := result.FinalGit(); ok {
		refs = append(refs,
			git.HeadProcess.StdoutRef, git.HeadProcess.StderrRef,
			git.StatusProcess.StdoutRef, git.StatusProcess.StderrRef,
			git.MetadataRef,
		)
	}
	return refs
}

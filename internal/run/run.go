// Package run orchestrates one governed EP-002 Ralphex lifecycle. Its
// authority ends at BRANCH_ACCEPTED; integration and completion states are
// deliberately outside this package.
package run

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/acceptance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
	governancev3 "github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

const (
	eventStateTransition     = "STATE_TRANSITION"
	actorController          = "control-plane"
	ralphexEnvironmentPolicy = "ralphex-env-v2"
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

// ContainedCommandRunner is the mandatory V3 Linux handoff. Implementations
// must create the scope before spawn, place every descendant in it, and prove
// the scope empty before returning.
type ContainedCommandRunner interface {
	RunContained(context.Context, supervisor.Command, ContainmentRequestV1) (supervisor.Result, ContainmentEvidenceV1, error)
}

// ContainmentRequestV1 is the controller-selected, non-reusable scope request.
type ContainmentRequestV1 struct {
	Identity         string `json:"identity"`
	WallClockTimeout string `json:"wall_clock_timeout"`
}

// ContainmentEvidenceV1 proves membership and verified empty-scope teardown.
type ContainmentEvidenceV1 struct {
	Kind               string `json:"kind"`
	ScopeIdentity      string `json:"scope_identity"`
	Primitive          string `json:"primitive"`
	MembershipVerified bool   `json:"membership_verified"`
	EmptyScopeVerified bool   `json:"empty_scope_verified"`
}

// Result is the terminal EP-002 conclusion and its controller-owned evidence.
type Result struct {
	State                 domain.State
	Ralphex               supervisor.Result
	RalphexMetadataRef    ledger.EvidenceRef
	Acceptance            acceptance.Result
	AuthorityEvidenceRef  ledger.EvidenceRef
	ValidationEvidenceRef ledger.EvidenceRef
	CandidateEvidenceRef  ledger.EvidenceRef
	MutationReceiptRef    ledger.EvidenceRef
	FailureReason         string
}

// Accepted reports whether independent controller acceptance produced the
// terminal state BRANCH_ACCEPTED.
func (r Result) Accepted() bool {
	return r.State == domain.StateBranchAccepted && r.Acceptance.Passed()
}

// Runner owns the state transitions for one validated authority.
type Runner struct {
	governed      authority.Authority
	capsule       authority.ContextCapsuleManifest
	events        EventAppender
	artifacts     supervisor.ArtifactWriter
	processes     CommandRunner
	controller    *governancev3.ControllerV1
	parsedCapsule contextcapsule.Capsule
}

// New constructs an EP-002 runner from validated authority and explicit
// ledger, evidence, and subprocess dependencies.
func New(governed authority.Authority, events EventAppender, artifacts supervisor.ArtifactWriter, processes CommandRunner) (*Runner, error) {
	return newRunner(governed, events, artifacts, processes, nil)
}

// NewWithController constructs the mandatory durable V3 execution path.
func NewWithController(governed authority.Authority, events EventAppender, artifacts supervisor.ArtifactWriter, processes CommandRunner, controller *governancev3.ControllerV1) (*Runner, error) {
	return newRunner(governed, events, artifacts, processes, controller)
}

func newRunner(governed authority.Authority, events EventAppender, artifacts supervisor.ArtifactWriter, processes CommandRunner, controller *governancev3.ControllerV1) (*Runner, error) {
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
	capsule, err := validateOperationAuthority(governed)
	if err != nil {
		return nil, err
	}
	capsuleData, err := os.ReadFile(capsule.Path)
	if err != nil {
		return nil, fmt.Errorf("read admitted context capsule: %w", err)
	}
	parsedCapsule, err := contextcapsule.Parse(capsuleData)
	if err != nil {
		return nil, fmt.Errorf("parse admitted context capsule: %w", err)
	}
	if autonomousDevelopmentCapsule(parsedCapsule) {
		if controller == nil || !governed.ControllerAdmitted() || governed.ControllerIdentity() == "" || governed.ControllerIdentity() != controller.ControllerIdentity() {
			return nil, errors.New("CAPSULE_LINEAGE_INVALID: exact repository governance controller admission is required")
		}
		if err := controller.AdmitWorkflowAuthority(governed.Repository().Path, governed.Repository().Identity, parsedCapsule.PolicyVersion, governed.SHA256()); err != nil {
			return nil, fmt.Errorf("revalidate repository governance controller admission: %w", err)
		}
	}
	if parsedCapsule.PolicyVersion == contextcapsule.PolicyVersionV3 {
		if _, ok := processes.(ContainedCommandRunner); !ok {
			return nil, errors.New("EXECUTION_BOUNDS_INVALID: Linux containment handoff is unavailable")
		}
	}
	return &Runner{governed: governed, capsule: capsule, events: events, artifacts: artifacts, processes: processes, controller: controller, parsedCapsule: parsedCapsule}, nil
}

func autonomousDevelopmentCapsule(capsule contextcapsule.Capsule) bool {
	if capsule.PolicyVersion == contextcapsule.PolicyVersionV3 {
		return true
	}
	if capsule.PolicyVersion != contextcapsule.PolicyVersionV2 || capsule.OperationContext == nil {
		return false
	}
	switch capsule.OperationContext.Kind {
	case contextcapsule.OperationDesignPlanning, contextcapsule.OperationDesignReview,
		contextcapsule.OperationImplementation, contextcapsule.OperationImplementationReview,
		contextcapsule.OperationAcceptance, contextcapsule.OperationFinalReview,
		contextcapsule.OperationPRPublication, contextcapsule.OperationMergeAuthorization,
		contextcapsule.OperationPostMergeAcceptance:
		return true
	default:
		return false
	}
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
		if ctx.Err() != nil {
			return r.cancel(result, domain.StateRunCreated, "authority-validator", err, []ledger.EvidenceRef{authorityRef})
		}
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

	configDir, err := os.MkdirTemp("", "abcp-ralphex-config-")
	if err != nil {
		return r.fail(result, domain.StateAuthorityValidated, "ralphex-adapter", fmt.Errorf("create isolated Ralphex config directory: %w", err), nil)
	}
	defer os.RemoveAll(configDir)
	invocation, err := r.invocation(configDir)
	if err != nil {
		return r.fail(result, domain.StateAuthorityValidated, "ralphex-adapter", err, nil)
	}
	argv, err := invocation.Argv()
	if err != nil {
		return r.fail(result, domain.StateAuthorityValidated, "ralphex-adapter", err, nil)
	}
	ralphexTimeout, err := time.ParseDuration(r.governed.Ralphex().Timeout)
	if err != nil {
		return r.fail(result, domain.StateAuthorityValidated, "ralphex-adapter", fmt.Errorf("parse governed Ralphex timeout: %w", err), nil)
	}
	if err := r.transition(domain.StateAuthorityValidated, domain.StateExecutionStarting, "governed-runner", map[string]any{
		"argv": argv, "timeout": r.governed.Ralphex().Timeout, "environment_policy": ralphexEnvironmentPolicy,
	}, nil); err != nil {
		return result, err
	}
	result.State = domain.StateExecutionStarting
	if err := r.transition(domain.StateExecutionStarting, domain.StateImplementing, "ralphex-adapter", map[string]any{"argv": argv}, nil); err != nil {
		return result, err
	}
	result.State = domain.StateImplementing

	command := supervisor.Command{
		Argv:    argv,
		Cwd:     r.governed.Repository().Path,
		Env:     ralphexEnvironment(r.governed.Executor().Executor, r.capsule),
		Timeout: ralphexTimeout,
		Stdout:  supervisor.EvidenceSink{Writer: r.artifacts, Name: "ralphex-stdout.log", Kind: "ralphex-stdout"},
		Stderr:  supervisor.EvidenceSink{Writer: r.artifacts, Name: "ralphex-stderr.log", Kind: "ralphex-stderr"},
	}
	var reservation governancev3.InvocationReservationV1
	if invocation.Bounds != nil {
		admission, _ := r.governed.Governance()
		reservation, err = r.controller.ReserveRalphexInvocationV1(r.parsedCapsule, admission.Operation, admission.Mutation, admission.LeaseSHA256)
		if err != nil {
			return r.fail(result, domain.StateImplementing, "governance-controller", err, nil)
		}
	}
	var process supervisor.Result
	var processErr error
	var containment *ContainmentEvidenceV1
	if invocation.Bounds != nil {
		contained, ok := r.processes.(ContainedCommandRunner)
		if !ok {
			return r.fail(result, domain.StateImplementing, "ralphex-adapter", errors.New("EXECUTION_BOUNDS_INVALID: Linux containment handoff is unavailable"), nil)
		}
		evidence, containedErr := ContainmentEvidenceV1{}, error(nil)
		process, evidence, containedErr = contained.RunContained(ctx, command, ContainmentRequestV1{
			Identity: r.governed.RunID(), WallClockTimeout: invocation.Bounds.WallClockTimeout,
		})
		processErr = containedErr
		containment = &evidence
		if processErr == nil && (evidence.Kind != "ContainmentEvidenceV1" || evidence.ScopeIdentity == "" || evidence.Primitive != "cgroup-v2" || !evidence.MembershipVerified || !evidence.EmptyScopeVerified) {
			processErr = errors.New("EXECUTION_BOUNDS_INVALID: containment membership or empty-scope proof is invalid")
		}
	} else {
		process, processErr = r.processes.Run(ctx, command)
	}
	if invocation.Bounds != nil {
		if finishErr := r.controller.FinishRalphexInvocationV1(reservation); finishErr != nil {
			processErr = errors.Join(processErr, finishErr)
		}
	}
	result.Ralphex = process
	if processErr != nil {
		return r.fail(result, domain.StateImplementing, "ralphex-adapter", processErr, processRefs(process))
	}
	metadataRef, err := r.writeJSON("ralphex-process.json", "ralphex-process-metadata", struct {
		Process           supervisor.Result      `json:"process"`
		EnvironmentPolicy string                 `json:"environment_policy"`
		EvidenceRefs      []ledger.EvidenceRef   `json:"evidence_refs"`
		Containment       *ContainmentEvidenceV1 `json:"containment,omitempty"`
	}{Process: process, EnvironmentPolicy: ralphexEnvironmentPolicy, EvidenceRefs: processRefs(process), Containment: containment})
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
	if invocation.Bounds != nil {
		admission, _ := r.governed.Governance()
		if admission.Operation == contextcapsule.OperationImplementationReview && admission.Mutation {
			candidateSHA, headErr := gitOutput(ctx, r.governed.Repository().Path, "rev-parse", "--verify", "HEAD^{commit}")
			if headErr != nil {
				return r.fail(result, domain.StateImplementing, "governance-controller", fmt.Errorf("resolve mutation result HEAD: %w", headErr), implementationRefs)
			}
			receipt, receiptErr := r.controller.CompleteMutationReceiptV1(r.governed.Repository().Path, r.parsedCapsule, admission.LeaseSHA256, candidateSHA)
			if receiptErr != nil {
				return r.fail(result, domain.StateImplementing, "governance-controller", receiptErr, implementationRefs)
			}
			receiptRef, writeErr := r.writeJSON("mutation-receipt.json", "mutation-receipt", receipt)
			if writeErr != nil {
				return r.fail(result, domain.StateImplementing, "governance-controller", writeErr, implementationRefs)
			}
			result.MutationReceiptRef = receiptRef
			implementationRefs = append(implementationRefs, receiptRef)
		}
	}

	if err := r.transition(domain.StateImplementing, domain.StateImplementationCompleted, "ralphex-adapter", map[string]any{
		"outcome": process.Outcome, "exit_code": process.ExitCode,
	}, implementationRefs); err != nil {
		return result, err
	}
	result.State = domain.StateImplementationCompleted
	if invocation.Bounds != nil {
		// B can produce implementation/review evidence only. Acceptance and
		// BRANCH_ACCEPTED-equivalent authority require a derived exact-head C.
		return result, nil
	}
	target, cleanup, err := r.prepareAcceptanceTarget(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return r.cancel(result, domain.StateImplementationCompleted, "acceptance-controller", err, implementationRefs)
		}
		return r.fail(result, domain.StateImplementationCompleted, "acceptance-controller", err, implementationRefs)
	}
	candidateRef, err := r.writeJSON("candidate-branch.json", "candidate-branch", target)
	if err != nil {
		cleanupErr := cleanup()
		return r.fail(result, domain.StateImplementationCompleted, "acceptance-controller", errors.Join(err, cleanupErr), implementationRefs)
	}
	result.CandidateEvidenceRef = candidateRef
	implementationRefs = append(implementationRefs, candidateRef)
	if err := r.transition(domain.StateImplementationCompleted, domain.StateBranchAcceptancePending, "acceptance-controller", nil, implementationRefs); err != nil {
		return result, errors.Join(err, cleanup())
	}
	result.State = domain.StateBranchAcceptancePending

	accepted, acceptanceErr := acceptance.New(r.processes, r.artifacts).Run(ctx, r.governed, target)
	cleanupErr := cleanup()
	if cleanupErr != nil {
		acceptanceErr = errors.Join(acceptanceErr, cleanupErr)
	}
	result.Acceptance = accepted
	acceptanceRefs := collectAcceptanceRefs(accepted)
	acceptanceRefs = append(acceptanceRefs, candidateRef)
	if ctx.Err() != nil {
		result.State = domain.StateCancelled
		result.FailureReason = ctx.Err().Error()
		if err := r.transition(domain.StateBranchAcceptancePending, domain.StateCancelled, "acceptance-controller", map[string]any{
			"status": accepted.Status(), "reason": result.FailureReason,
		}, acceptanceRefs); err != nil {
			return result, errors.Join(acceptanceErr, err)
		}
		return result, cleanupErr
	}
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

func (r *Runner) invocation(configDir string) (ralphex.Invocation, error) {
	policy := r.governed.Executor()
	executor := strings.ToLower(strings.TrimSpace(policy.Executor))
	if executor != "" && executor != "claude" && executor != "codex" {
		return ralphex.Invocation{}, fmt.Errorf("unsupported executor %q", executor)
	}
	invocation := ralphex.Invocation{
		BinaryPath:   r.governed.Ralphex().BinaryPath,
		PlanPath:     r.governed.Plan().Path,
		ConfigDir:    configDir,
		Mode:         r.governed.Ralphex().Mode,
		Codex:        executor == "codex",
		Worktree:     r.governed.Worktree().Enabled,
		Branch:       r.governed.Worktree().Branch,
		TaskModel:    policy.TaskModel,
		TaskEffort:   policy.TaskEffort,
		ReviewModel:  policy.ReviewModel,
		ReviewEffort: policy.ReviewEffort,
		WaitOnLimit:  r.governed.Ralphex().WaitOnLimit,
	}
	data, err := os.ReadFile(r.capsule.Path)
	if err != nil {
		return ralphex.Invocation{}, fmt.Errorf("read context capsule for invocation: %w", err)
	}
	capsule, err := contextcapsule.Parse(data)
	if err != nil {
		return ralphex.Invocation{}, fmt.Errorf("parse context capsule for invocation: %w", err)
	}
	if capsule.PolicyVersion == contextcapsule.PolicyVersionV3 {
		if capsule.PhaseAuthority == nil || capsule.PhaseAuthority.ExecutionBounds == nil {
			return ralphex.Invocation{}, errors.New("EXECUTION_BOUNDS_INVALID: B V3 execution bounds are absent")
		}
		runtime := r.governed.Ralphex()
		invocation.BaseRef = r.governed.Repository().StartSHA
		invocation.Bounds = capsule.PhaseAuthority.ExecutionBounds
		invocation.Capability = runtime.Capability
		invocation.BinarySHA256 = runtime.BinarySHA256
		invocation.SourceSHA = runtime.SourceSHA
	}
	return invocation, nil
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
	if err := domain.ValidateTransition(from, to); err != nil {
		return fmt.Errorf("validate %s -> %s transition: %w", from, to, err)
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

func (r *Runner) cancel(result Result, from domain.State, source string, cause error, refs []ledger.EvidenceRef) (Result, error) {
	result.State = domain.StateCancelled
	result.FailureReason = cause.Error()
	if err := r.transition(from, domain.StateCancelled, source, map[string]any{"reason": cause.Error()}, refs); err != nil {
		return result, errors.Join(cause, err)
	}
	return result, nil
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
	RepositoryPath       string            `json:"repository_path"`
	RepositoryID         string            `json:"repository_identity,omitempty"`
	HeadSHA              string            `json:"head_sha"`
	Branch               string            `json:"branch,omitempty"`
	WorkingTreeClean     bool              `json:"working_tree_clean"`
	Remotes              map[string]string `json:"remotes,omitempty"`
	PlanSHA256           string            `json:"plan_sha256"`
	BinarySHA256         string            `json:"ralphex_binary_sha256"`
	ContextCapsuleSHA256 string            `json:"context_capsule_sha256,omitempty"`
	AuthoritySHA256      string            `json:"authority_sha256"`
}

func validatePinnedIdentity(ctx context.Context, governed authority.Authority) (identityValidation, error) {
	repository := governed.Repository()
	plan := governed.Plan()
	binary := governed.Ralphex()
	validation := identityValidation{
		RepositoryPath:  repository.Path,
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
	capsule, present := governed.ContextCapsule()
	if !present {
		return validation, errors.New("verified v2 context capsule is required before execution")
	}
	verified, err := contextcapsule.VerifyFile(repository.Path, capsule.Path)
	if err != nil {
		return validation, fmt.Errorf("verify context capsule before execution: %w", err)
	}
	validation.ContextCapsuleSHA256 = verified.SHA256
	if validation.ContextCapsuleSHA256 != capsule.SHA256 {
		return validation, errors.New("context capsule SHA256 changed after authority validation")
	}
	if verified.PolicyVersion != contextcapsule.PolicyVersionV2 && verified.PolicyVersion != contextcapsule.PolicyVersionV3 {
		return validation, fmt.Errorf("context capsule policy version changed to %q after authority validation", verified.PolicyVersion)
	}
	repositoryRoot, err := gitOutput(ctx, repository.Path, "rev-parse", "--show-toplevel")
	if err != nil {
		return validation, fmt.Errorf("resolve repository root: %w", err)
	}
	repositoryRoot, err = filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return validation, fmt.Errorf("canonicalize repository root: %w", err)
	}
	if filepath.Clean(repositoryRoot) != repository.Path {
		return validation, fmt.Errorf("governed repository path %q is not Git repository root %q", repository.Path, repositoryRoot)
	}
	if governed.Worktree().Enabled {
		branch := governed.Worktree().Branch
		checked, checkErr := gitOutput(ctx, repository.Path, "check-ref-format", "--branch", branch)
		if checkErr != nil || checked != branch {
			return validation, fmt.Errorf("invalid governed worktree branch %q", branch)
		}
		exists, existsErr := localBranchExists(ctx, repository.Path, branch)
		if existsErr != nil {
			return validation, fmt.Errorf("inspect governed worktree branch: %w", existsErr)
		}
		if exists {
			return validation, fmt.Errorf("governed worktree branch %q already exists", branch)
		}
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
	status, err := gitOutput(ctx, repository.Path, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return validation, fmt.Errorf("inspect repository working tree: %w", err)
	}
	validation.WorkingTreeClean = status == ""
	if !validation.WorkingTreeClean {
		return validation, errors.New("governed repository working tree is not clean")
	}
	if err := validateRalphexLocalConfiguration(repository.Path); err != nil {
		return validation, err
	}

	names := make([]string, 0, len(repository.Remotes))
	for name := range repository.Remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	actualNamesOutput, err := gitOutput(ctx, repository.Path, "remote")
	if err != nil {
		return validation, fmt.Errorf("enumerate repository remotes: %w", err)
	}
	var actualNames []string
	if actualNamesOutput != "" {
		actualNames = strings.Split(actualNamesOutput, "\n")
	}
	sort.Strings(actualNames)
	if len(actualNames) != len(names) {
		return validation, fmt.Errorf("repository remote set does not match governed remotes")
	}
	for index := range names {
		if actualNames[index] != names[index] {
			return validation, fmt.Errorf("repository remote set does not match governed remotes")
		}
	}
	for _, name := range names {
		actual, remoteErr := gitOutput(ctx, repository.Path, "remote", "get-url", "--all", name)
		if remoteErr != nil {
			return validation, fmt.Errorf("resolve repository remote %q: %w", name, remoteErr)
		}
		if actual != repository.Remotes[name] {
			return validation, fmt.Errorf("repository remote %q does not match governed URL", name)
		}
		push, pushErr := gitOutput(ctx, repository.Path, "remote", "get-url", "--push", "--all", name)
		if pushErr != nil {
			return validation, fmt.Errorf("resolve repository remote %q push URL: %w", name, pushErr)
		}
		if push != repository.Remotes[name] {
			return validation, fmt.Errorf("repository remote %q push URL does not match governed URL", name)
		}
		validation.Remotes[name] = actual
	}
	validation.RepositoryID = repository.Identity
	return validation, nil
}

func validateRalphexLocalConfiguration(repositoryPath string) error {
	boundary := filepath.Join(repositoryPath, ".ralphex")
	info, err := os.Lstat(boundary)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect repository-local Ralphex configuration boundary: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("repository-local .ralphex configuration boundary must not be a symlink")
	}
	if !info.IsDir() {
		return errors.New("repository-local .ralphex configuration boundary must be a directory")
	}

	for _, name := range []string{"config", "prompts", "agents"} {
		override := filepath.Join(boundary, name)
		if _, err := os.Lstat(override); err == nil {
			return fmt.Errorf("repository-local .ralphex/%s configuration is not allowed for governed execution", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect repository-local .ralphex/%s configuration: %w", name, err)
		}
	}
	return nil
}

func ralphexEnvironment(executor string, capsule authority.ContextCapsuleManifest) []string {
	keys := []string{
		"HOME", "PATH", "LANG", "LANGUAGE", "LC_ALL", "LC_CTYPE", "TZ", "TERM",
		"TMPDIR", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "SSL_CERT_FILE", "SSL_CERT_DIR",
		"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY",
		"https_proxy", "http_proxy", "all_proxy", "no_proxy",
	}
	switch executor {
	case "codex":
		keys = append(keys,
			"CODEX_HOME", "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_ORG_ID", "OPENAI_PROJECT_ID",
			"AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT",
		)
	case "claude":
		keys = append(keys,
			"CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
		)
	}
	environment := make([]string, 0, len(keys)+2)
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			environment = append(environment, key+"="+value)
		}
	}
	environment = append(environment,
		"ABCP_CONTEXT_CAPSULE_PATH="+capsule.Path,
		"ABCP_CONTEXT_CAPSULE_SHA256="+capsule.SHA256,
	)
	return environment
}

func validateOperationAuthority(governed authority.Authority) (authority.ContextCapsuleManifest, error) {
	capsule, present := governed.ContextCapsule()
	if !present {
		return authority.ContextCapsuleManifest{}, errors.New("verified v2 context capsule is required for legacy execution; V3 successor is required after activation")
	}
	verified, err := contextcapsule.VerifyFile(governed.Repository().Path, capsule.Path)
	if err != nil {
		return authority.ContextCapsuleManifest{}, fmt.Errorf("verify operation context capsule: %w", err)
	}
	if verified.SHA256 != capsule.SHA256 {
		return authority.ContextCapsuleManifest{}, fmt.Errorf("context capsule SHA256 mismatch: authority binds %s, verified exact bytes are %s", capsule.SHA256, verified.SHA256)
	}
	mode := governed.Ralphex().Mode
	if verified.PolicyVersion == contextcapsule.PolicyVersionV2 {
		if verified.BaseSHA != governed.Repository().StartSHA {
			return authority.ContextCapsuleManifest{}, fmt.Errorf("context capsule base SHA %s does not match governed start SHA %s", verified.BaseSHA, governed.Repository().StartSHA)
		}
		if err := validateOperationMode(verified.OperationKind, mode, false); err != nil {
			return authority.ContextCapsuleManifest{}, err
		}
	} else if verified.PolicyVersion == contextcapsule.PolicyVersionV3 {
		admission, present := governed.Governance()
		if !present {
			return authority.ContextCapsuleManifest{}, errors.New("CAPSULE_USAGE_INVALID: V3 governance admission is required")
		}
		capsuleData, err := os.ReadFile(capsule.Path)
		if err != nil {
			return authority.ContextCapsuleManifest{}, fmt.Errorf("read V3 operation capsule: %w", err)
		}
		parsed, err := contextcapsule.Parse(capsuleData)
		if err != nil {
			return authority.ContextCapsuleManifest{}, fmt.Errorf("parse V3 operation capsule: %w", err)
		}
		if _, err := governancev3.ValidateCandidateV1(governed.Repository().Path, parsed, governed.Repository().StartSHA); err != nil {
			return authority.ContextCapsuleManifest{}, fmt.Errorf("validate V3 invocation candidate: %w", err)
		}
		if err := validateOperationMode(admission.Operation, mode, admission.Mutation); err != nil {
			return authority.ContextCapsuleManifest{}, err
		}
	} else {
		return authority.ContextCapsuleManifest{}, fmt.Errorf("context capsule policy version %q cannot authorize governed execution; %s or activated %s is required", verified.PolicyVersion, contextcapsule.PolicyVersionV2, contextcapsule.PolicyVersionV3)
	}
	policy := governed.Executor()
	if strings.EqualFold(strings.TrimSpace(policy.Executor), "codex") {
		if policy.TaskEffort != "xhigh" {
			return authority.ContextCapsuleManifest{}, fmt.Errorf("Codex task_effort must be xhigh, got %q", policy.TaskEffort)
		}
		if policy.ReviewEffort != "xhigh" {
			return authority.ContextCapsuleManifest{}, fmt.Errorf("Codex review_effort must be xhigh, got %q", policy.ReviewEffort)
		}
	}
	if mode == ralphex.ModeFull || mode == ralphex.ModeTasksOnly {
		plan, err := os.ReadFile(governed.Plan().Path)
		if err != nil {
			return authority.ContextCapsuleManifest{}, fmt.Errorf("read implementation task plan: %w", err)
		}
		sections, err := countIncompleteExecutableSections(plan)
		if err != nil {
			return authority.ContextCapsuleManifest{}, fmt.Errorf("inspect implementation task plan: %w", err)
		}
		if sections > 1 {
			return authority.ContextCapsuleManifest{}, fmt.Errorf("implementation operation plan contains %d incomplete executable Task/Iteration sections; maximum is 1", sections)
		}
	}
	return capsule, nil
}

func validateOperationMode(kind contextcapsule.OperationKind, mode ralphex.Mode, mutation bool) error {
	switch mode {
	case ralphex.ModeFull:
		if mutation {
			return fmt.Errorf("Ralphex full mode cannot expose the controller review-to-lease-to-fix boundary")
		}
		if kind != contextcapsule.OperationImplementation {
			return fmt.Errorf("Ralphex mode %q requires operation kind %q, got %q", mode, contextcapsule.OperationImplementation, kind)
		}
	case ralphex.ModeTasksOnly:
		if kind != contextcapsule.OperationImplementation && !(kind == contextcapsule.OperationImplementationReview && mutation) {
			return fmt.Errorf("Ralphex mode %q requires operation kind %q or a leased %q fix, got %q", mode, contextcapsule.OperationImplementation, contextcapsule.OperationImplementationReview, kind)
		}
	case ralphex.ModeReview:
		if mutation {
			return fmt.Errorf("Ralphex review mode is read-only and cannot consume a mutation lease")
		}
		if kind != contextcapsule.OperationDesignReview && kind != contextcapsule.OperationImplementationReview {
			return fmt.Errorf("Ralphex mode %q requires operation kind %q or %q, got %q", mode, contextcapsule.OperationDesignReview, contextcapsule.OperationImplementationReview, kind)
		}
	default:
		return fmt.Errorf("unsupported Ralphex mode %q for operation kind %q", mode, kind)
	}
	return nil
}

func countIncompleteExecutableSections(plan []byte) (int, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(plan)))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	inExecutableSection := false
	currentIncomplete := false
	inFence := false
	fenceMarker := byte(0)
	fenceLength := 0
	fenceLine := 0
	incompleteSections := 0
	finishSection := func() {
		if inExecutableSection && currentIncomplete {
			incompleteSections++
		}
		inExecutableSection = false
		currentIncomplete = false
	}
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if inFence {
			if closesMarkdownFence(line, fenceMarker, fenceLength) {
				inFence = false
				fenceMarker = 0
				fenceLength = 0
				fenceLine = 0
			}
			continue
		}
		if marker, length, opens := opensMarkdownFence(line); opens {
			inFence = true
			fenceMarker = marker
			fenceLength = length
			fenceLine = lineNumber
			continue
		}
		level := markdownHeadingLevel(line)
		if level > 0 && level <= 3 {
			finishSection()
			inExecutableSection = level == 3 && executableSectionHeading(line)
			continue
		}
		if inExecutableSection && incompleteCheckbox(strings.TrimSpace(line)) {
			currentIncomplete = true
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	if inFence {
		return 0, fmt.Errorf("unterminated Markdown %c fence of length %d opened on line %d", fenceMarker, fenceLength, fenceLine)
	}
	finishSection()
	return incompleteSections, nil
}

func opensMarkdownFence(line string) (byte, int, bool) {
	content, validIndent := markdownFenceContent(line)
	if !validIndent || len(content) < 3 || content[0] != '`' && content[0] != '~' {
		return 0, 0, false
	}
	marker := content[0]
	count := 0
	for count < len(content) && content[count] == marker {
		count++
	}
	if count < 3 || marker == '`' && strings.ContainsRune(content[count:], '`') {
		return 0, 0, false
	}
	return marker, count, true
}

func closesMarkdownFence(line string, marker byte, openingLength int) bool {
	content, validIndent := markdownFenceContent(line)
	if !validIndent || len(content) < openingLength || content[0] != marker {
		return false
	}
	count := 0
	for count < len(content) && content[count] == marker {
		count++
	}
	return count >= openingLength && strings.Trim(content[count:], " \t") == ""
}

func markdownFenceContent(line string) (string, bool) {
	column := 0
	index := 0
	for index < len(line) {
		switch line[index] {
		case ' ':
			column++
		case '\t':
			column += 4 - column%4
		default:
			return line[index:], column <= 3
		}
		if column > 3 {
			return "", false
		}
		index++
	}
	return "", true
}

func markdownHeadingLevel(line string) int {
	spaces := 0
	for spaces < len(line) && line[spaces] == ' ' {
		spaces++
	}
	if spaces > 3 {
		return 0
	}
	line = line[spaces:]
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level == len(line) || line[level] != ' ' && line[level] != '\t' {
		return 0
	}
	return level
}

func executableSectionHeading(line string) bool {
	spaces := 0
	for spaces < len(line) && line[spaces] == ' ' {
		spaces++
	}
	title := strings.TrimSpace(line[spaces+3:])
	for _, prefix := range []string{"Task ", "Iteration "} {
		if !strings.HasPrefix(title, prefix) {
			continue
		}
		remainder := title[len(prefix):]
		digits := 0
		for digits < len(remainder) && remainder[digits] >= '0' && remainder[digits] <= '9' {
			digits++
		}
		return digits > 0 && digits < len(remainder) && remainder[digits] == ':'
	}
	return false
}

func incompleteCheckbox(trimmed string) bool {
	if len(trimmed) < len("- [ ]") {
		return false
	}
	return strings.HasPrefix(trimmed, "- [ ]") || strings.HasPrefix(trimmed, "* [ ]") || strings.HasPrefix(trimmed, "+ [ ]")
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
	command.Env = gitexec.Environment()
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func localBranchExists(ctx context.Context, repository, branch string) (bool, error) {
	command := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	command.Dir = repository
	command.Env = gitexec.Environment()
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func (r *Runner) prepareAcceptanceTarget(ctx context.Context) (acceptance.Target, func() error, error) {
	repository := r.governed.Repository()
	branch := r.governed.Worktree().Branch
	if !r.governed.Worktree().Enabled {
		currentBranch, err := gitOutput(ctx, repository.Path, "symbolic-ref", "--quiet", "--short", "HEAD")
		if err != nil {
			return acceptance.Target{}, func() error { return nil }, fmt.Errorf("discover candidate branch: %w", err)
		}
		headSHA, err := gitOutput(ctx, repository.Path, "rev-parse", "--verify", "HEAD")
		if err != nil {
			return acceptance.Target{}, func() error { return nil }, fmt.Errorf("discover candidate HEAD: %w", err)
		}
		if err := requireDescendsFrom(ctx, repository.Path, repository.StartSHA, headSHA); err != nil {
			return acceptance.Target{}, func() error { return nil }, fmt.Errorf("candidate branch %q does not descend from governed start SHA: %w", currentBranch, err)
		}
		if r.governed.Ralphex().Mode != ralphex.ModeReview && headSHA == repository.StartSHA {
			return acceptance.Target{}, func() error { return nil }, fmt.Errorf("candidate branch %q did not advance beyond governed start SHA", currentBranch)
		}
		branch = currentBranch
		return materializeAcceptanceTarget(ctx, repository.Path, branch, headSHA)
	}

	headSHA, err := gitOutput(ctx, repository.Path, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return acceptance.Target{}, func() error { return nil }, fmt.Errorf("resolve candidate branch %q: %w", branch, err)
	}
	if err := requireDescendsFrom(ctx, repository.Path, repository.StartSHA, headSHA); err != nil {
		return acceptance.Target{}, func() error { return nil }, fmt.Errorf("candidate branch %q does not descend from governed start SHA: %w", branch, err)
	}
	if r.governed.Ralphex().Mode != ralphex.ModeReview && headSHA == repository.StartSHA {
		return acceptance.Target{}, func() error { return nil }, fmt.Errorf("candidate branch %q did not advance beyond governed start SHA", branch)
	}

	return materializeAcceptanceTarget(ctx, repository.Path, branch, headSHA)
}

func materializeAcceptanceTarget(ctx context.Context, repository, branch, headSHA string) (acceptance.Target, func() error, error) {
	temporaryRoot, err := os.MkdirTemp("", "abcp-acceptance-")
	if err != nil {
		return acceptance.Target{}, func() error { return nil }, fmt.Errorf("create acceptance checkout root: %w", err)
	}
	checkout := filepath.Join(temporaryRoot, "checkout")
	add := exec.CommandContext(ctx, "git", "worktree", "add", "--detach", checkout, headSHA)
	add.Dir = repository
	add.Env = gitexec.Environment()
	if output, addErr := add.CombinedOutput(); addErr != nil {
		_ = os.RemoveAll(temporaryRoot)
		return acceptance.Target{}, func() error { return nil }, fmt.Errorf("materialize candidate checkout: %w: %s", addErr, strings.TrimSpace(string(output)))
	}

	cleanup := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		remove := exec.CommandContext(cleanupCtx, "git", "worktree", "remove", "--force", checkout)
		remove.Dir = repository
		remove.Env = gitexec.Environment()
		removeErr := remove.Run()
		filesystemErr := os.RemoveAll(temporaryRoot)
		if removeErr != nil || filesystemErr != nil {
			return fmt.Errorf("remove temporary acceptance checkout: %w", errors.Join(removeErr, filesystemErr))
		}
		return nil
	}
	return acceptance.Target{RepositoryPath: checkout, Branch: branch, HeadSHA: headSHA}, cleanup, nil
}

func requireDescendsFrom(ctx context.Context, repository, startSHA, headSHA string) error {
	ancestor := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", startSHA, headSHA)
	ancestor.Dir = repository
	ancestor.Env = gitexec.Environment()
	return ancestor.Run()
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
	return result.EvidenceRefs()
}

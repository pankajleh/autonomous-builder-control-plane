// Package run orchestrates one governed EP-002 Ralphex lifecycle. Its
// authority ends at BRANCH_ACCEPTED; integration and completion states are
// deliberately outside this package.
package run

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/acceptance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
	governancev3 "github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

const (
	eventStateTransition       = "STATE_TRANSITION"
	actorController            = "control-plane"
	ralphexEnvironmentPolicy   = "ralphex-env-v2"
	runnerSnapshotCloseWait    = 2 * time.Second
	executionPlanOwnerKind     = "ExecutionPlanHandoffOwnerV1"
	executionPlanOwnerRoot     = "abcp-ralphex-plan-handoffs"
	executionPlanLockName      = "abcp-ralphex-execution.lock"
	maxExecutionPlanOwners     = 1024
	maxExecutionPlanOwnerBytes = 4096
)

const apiCancelEventDomain = "ep006-api-cancel-request-v1"

// APICancelProvenanceV1 is the only controller provenance attached to a
// service-caused CANCELLED transition.
type APICancelProvenanceV1 struct {
	OperationID  string
	OwnerLeaseID string
}

type apiCancelCause struct{ provenance APICancelProvenanceV1 }

func (c *apiCancelCause) Error() string { return "service API cancellation requested" }

// NewAPICancelCause constructs the typed, generation-bound cancellation cause
// used by the in-process owner watcher. It carries no caller-selected path,
// process identity, or recovery authority.
func NewAPICancelCause(operationID, ownerLeaseID string) error {
	if runtimecatalog.ValidateIdentifier(operationID) != nil || !validLowerDigest(ownerLeaseID) {
		return errors.New("invalid service API cancellation provenance")
	}
	return &apiCancelCause{provenance: APICancelProvenanceV1{OperationID: operationID, OwnerLeaseID: ownerLeaseID}}
}

func APICancelProvenance(ctx context.Context) (APICancelProvenanceV1, bool) {
	if ctx == nil {
		return APICancelProvenanceV1{}, false
	}
	var cause *apiCancelCause
	if !errors.As(context.Cause(ctx), &cause) || cause == nil {
		return APICancelProvenanceV1{}, false
	}
	return cause.provenance, true
}

func validLowerDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

type eventSnapshotter interface {
	Snapshot() ([]byte, string, error)
}

type atomicTransitionEventAppender interface {
	eventSnapshotter
	AcquireRunTransition(string) (*ledger.RunTransitionLease, error)
	AppendOrVerifyLeased(ledger.Event, *ledger.RunTransitionLease) error
}

// FinalizationHook freezes any external admission surface before Runner makes
// a successful terminal selection. BeginClose must not wait for external work
// to drain: Runner may still need the transition lease to append its selected
// lifecycle edge.
type FinalizationHook interface {
	BeginClose(context.Context) error
}

// SnapshotCoordinator reserves one service-root-wide authoritative-snapshot
// slot around a complete transition decision. Service-root runners configure
// it before Run starts; standalone runners leave it unset.
type SnapshotCoordinator interface {
	WithSnapshot(context.Context, func() error) error
}

type apiCancellationSelected struct{ cause error }

func (e *apiCancellationSelected) Error() string {
	return "service API cancellation won transition selection"
}
func (e *apiCancellationSelected) Unwrap() error { return e.cause }

func (r *Runner) cancellationPayload(ctx context.Context, base map[string]any) (map[string]any, error) {
	provenance, api := APICancelProvenance(ctx)
	if !api {
		return base, nil
	}
	requestEventID, err := r.proveAPICancelRequest(provenance)
	if err != nil {
		return nil, err
	}
	result := make(map[string]any, len(base)+3)
	for key, value := range base {
		result[key] = value
	}
	result["operation_id"] = provenance.OperationID
	result["owner_lease_id"] = provenance.OwnerLeaseID
	result["request_event_id"] = requestEventID
	return result, nil
}

func (r *Runner) proveAPICancelRequest(provenance APICancelProvenanceV1) (string, error) {
	snapshotter, ok := r.events.(eventSnapshotter)
	if !ok {
		return "", errors.New("durable API cancellation request proof is unavailable")
	}
	data, _, err := snapshotter.Snapshot()
	if err != nil || len(data) == 0 || data[len(data)-1] != '\n' {
		return "", errors.Join(errors.New("durable API cancellation request proof is unavailable"), err)
	}
	sum := sha256.Sum256([]byte(apiCancelEventDomain + "\x00" + provenance.OperationID))
	expectedID := hex.EncodeToString(sum[:])
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 256<<10)
	found := false
	for scanner.Scan() {
		var event ledger.Event
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Validate() != nil {
			return "", errors.New("durable API cancellation request proof is invalid")
		}
		if event.EventID != expectedID {
			continue
		}
		if found || !r.validAPICancelRequestEvent(event, provenance) {
			return "", errors.New("durable API cancellation request proof conflicts")
		}
		found = true
	}
	if scanner.Err() != nil || !found {
		return "", errors.Join(fmt.Errorf("durable API cancellation request event is absent"), scanner.Err())
	}
	return expectedID, nil
}

func (r *Runner) validAPICancelRequestEvent(event ledger.Event, provenance APICancelProvenanceV1) bool {
	if event.SchemaVersion != ledger.CurrentSchemaVersion || event.RunID != r.governed.RunID() || event.AttemptID != r.provenance.AttemptID ||
		event.ProjectID != r.provenance.ProjectID || event.PlanID != r.provenance.PlanID || event.TaskID != "" || event.AgentSessionID != "" ||
		event.CorrelationID != "" || event.EventType != "API_CANCEL_REQUESTED" || event.StateFrom != "" || event.StateTo != "" ||
		event.Actor != "control-plane" || event.Source != "service-action-watcher" || len(event.EvidenceRefs) != 0 ||
		payloadNumber(event.Payload, "record_schema_version") != 1 || payloadText(event.Payload, "action") != "cancel" ||
		payloadText(event.Payload, "operation_id") != provenance.OperationID || payloadText(event.Payload, "owner_lease_id") != provenance.OwnerLeaseID ||
		runtimecatalog.ValidateIdentifier(payloadText(event.Payload, "principal_id")) != nil || runtimecatalog.ValidateIdentifier(payloadText(event.Payload, "request_id")) != nil ||
		!validLowerDigest(payloadText(event.Payload, "request_sha256")) || payloadText(event.Payload, "expected_state") == "" ||
		!validLowerDigest(payloadText(event.Payload, "expected_revision")) || runtimecatalog.ValidateIdentifier(payloadText(event.Payload, "admitted_state_transition_event_id")) != nil {
		return false
	}
	principalType := payloadText(event.Payload, "principal_type")
	if principalType != "service" && principalType != "user" && principalType != "operator" && principalType != "test" {
		return false
	}
	_, delegatedID := event.Payload["delegated_actor_id"]
	_, delegatedType := event.Payload["delegated_actor_type"]
	if delegatedID != delegatedType {
		return false
	}
	wantFields := 12
	if delegatedID {
		wantFields = 14
		actorType := payloadText(event.Payload, "delegated_actor_type")
		if runtimecatalog.ValidateIdentifier(payloadText(event.Payload, "delegated_actor_id")) != nil || (actorType != "user" && actorType != "operator") {
			return false
		}
	}
	return len(event.Payload) == wantFields
}

func payloadText(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func payloadNumber(payload map[string]any, key string) int {
	switch value := payload[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return 0
	}
}

// EventAppender is the durable, append-only operation required by a Runner.
// ledger.JSONLLedger satisfies this interface.
type EventAppender interface {
	Append(ledger.Event) error
}

// TransitionProvenance is controller-owned identity copied onto every state
// transition so later lifecycle stages can reconstruct one exact run history.
type TransitionProvenance struct {
	ProjectID string
	PlanID    string
	AttemptID string
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
	provenance    TransitionProvenance
	events        EventAppender
	artifacts     supervisor.ArtifactWriter
	processes     CommandRunner
	controller    *governancev3.ControllerV1
	parsedCapsule contextcapsule.Capsule
	lifecycleMu   sync.Mutex
	started       bool
	finalization  FinalizationHook
	snapshots     SnapshotCoordinator
}

// New constructs an EP-002 runner from validated authority and explicit
// ledger, evidence, and subprocess dependencies.
func New(governed authority.Authority, events EventAppender, artifacts supervisor.ArtifactWriter, processes CommandRunner) (*Runner, error) {
	return newRunner(governed, DerivedTransitionProvenance(governed), events, artifacts, processes, nil)
}

// NewWithController constructs the mandatory durable V3 execution path.
func NewWithController(governed authority.Authority, events EventAppender, artifacts supervisor.ArtifactWriter, processes CommandRunner, controller *governancev3.ControllerV1) (*Runner, error) {
	return newRunner(governed, DerivedTransitionProvenance(governed), events, artifacts, processes, controller)
}

// DerivedTransitionProvenance supplies a stable legacy mapping when an outer
// controller has not assigned explicit project/plan/attempt identifiers.
func DerivedTransitionProvenance(governed authority.Authority) TransitionProvenance {
	return TransitionProvenance{ProjectID: governed.Repository().Identity, PlanID: governed.Plan().SHA256, AttemptID: governed.RunID()}
}

// NewWithProvenance constructs a runner with explicit controller provenance.
func NewWithProvenance(governed authority.Authority, provenance TransitionProvenance, events EventAppender, artifacts supervisor.ArtifactWriter, processes CommandRunner) (*Runner, error) {
	return newRunner(governed, provenance, events, artifacts, processes, nil)
}

func newRunner(governed authority.Authority, provenance TransitionProvenance, events EventAppender, artifacts supervisor.ArtifactWriter, processes CommandRunner, controller *governancev3.ControllerV1) (*Runner, error) {
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
	if err := validateTransitionProvenance(provenance); err != nil {
		return nil, err
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
	return &Runner{governed: governed, capsule: capsule, provenance: provenance, events: events, artifacts: artifacts, processes: processes, controller: controller, parsedCapsule: parsedCapsule}, nil
}

// SetFinalizationHook binds the service-owned admission freeze used by the
// successful return edges. It must be configured before Run starts.
func (r *Runner) SetFinalizationHook(hook FinalizationHook) error {
	if r == nil || hook == nil {
		return errors.New("finalization hook is required")
	}
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.started {
		return errors.New("runner has already started")
	}
	if r.finalization != nil {
		return errors.New("finalization hook is already configured")
	}
	r.finalization = hook
	return nil
}

// SetSnapshotCoordinator binds the service-root-wide snapshot authority used
// by every transition-state reconstruction and API-cancellation proof. It must
// be configured before Run starts.
func (r *Runner) SetSnapshotCoordinator(coordinator SnapshotCoordinator) error {
	if r == nil || coordinator == nil {
		return errors.New("snapshot coordinator is required")
	}
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.started {
		return errors.New("runner has already started")
	}
	if r.snapshots != nil {
		return errors.New("snapshot coordinator is already configured")
	}
	r.snapshots = coordinator
	return nil
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
func (r *Runner) Run(ctx context.Context) (result Result, runErr error) {
	if ctx == nil {
		return result, errors.New("context is required")
	}
	r.lifecycleMu.Lock()
	if r.started {
		r.lifecycleMu.Unlock()
		return result, errors.New("runner can execute only once")
	}
	r.started = true
	r.lifecycleMu.Unlock()
	defer func() {
		var selected *apiCancellationSelected
		if errors.As(runErr, &selected) {
			result.State = domain.StateCancelled
			result.FailureReason = selected.cause.Error()
			runErr = nil
		}
	}()

	authorityRef, err := r.artifacts.WriteBytes("authority.json", "validated-authority", r.governed.CanonicalJSON())
	if err != nil {
		return result, fmt.Errorf("publish authority evidence: %w", err)
	}
	result.AuthorityEvidenceRef = authorityRef
	if err := r.appendCreated(authorityRef); err != nil {
		return result, err
	}
	result.State = domain.StateRunCreated
	failAuthorityValidation := func(validationErr error) (Result, error) {
		if ctx.Err() != nil {
			return r.cancel(ctx, result, domain.StateRunCreated, "authority-validator", validationErr, []ledger.EvidenceRef{authorityRef})
		}
		result.State = domain.StateFailed
		result.FailureReason = validationErr.Error()
		if appendErr := r.transition(ctx, domain.StateRunCreated, domain.StateFailed, "authority-validator", map[string]any{"reason": validationErr.Error()}, []ledger.EvidenceRef{authorityRef}); appendErr != nil {
			return result, errors.Join(validationErr, appendErr)
		}
		return result, fmt.Errorf("validate pinned authority: %w", validationErr)
	}
	var repositoryLease *repositoryExecutionLease
	if repositoryExecutionLeasingSupported() {
		repositoryLease, err = acquireRepositoryExecutionLease(ctx, r.governed.Repository().Path)
		if err != nil {
			return failAuthorityValidation(fmt.Errorf("acquire repository execution lease: %w", err))
		}
	}
	defer func() {
		if repositoryLease != nil {
			runErr = errors.Join(runErr, repositoryLease.Close())
		}
	}()
	if repositoryLease != nil {
		if err := recoverExecutionPlanHandoffs(repositoryLease); err != nil {
			return failAuthorityValidation(fmt.Errorf("recover Ralphex execution plan handoffs: %w", err))
		}
	}

	validation, err := validatePinnedIdentity(ctx, r.governed)
	if err != nil {
		return failAuthorityValidation(err)
	}
	validationRef, err := r.writeJSON("authority-validation.json", "authority-validation", validation)
	if err != nil {
		return result, fmt.Errorf("publish authority validation evidence: %w", err)
	}
	result.ValidationEvidenceRef = validationRef
	if err := r.transition(ctx, domain.StateRunCreated, domain.StateAuthorityValidated, "authority-validator", map[string]any{
		"authority_sha256": r.governed.SHA256(),
	}, []ledger.EvidenceRef{authorityRef, validationRef}); err != nil {
		return result, err
	}
	result.State = domain.StateAuthorityValidated

	configDir, err := os.MkdirTemp("", "abcp-ralphex-config-")
	if err != nil {
		return r.fail(ctx, result, domain.StateAuthorityValidated, "ralphex-adapter", fmt.Errorf("create isolated Ralphex config directory: %w", err), nil)
	}
	defer os.RemoveAll(configDir)
	validationSpecPath, err := r.prepareValidationSpec(configDir)
	if err != nil {
		return r.fail(ctx, result, domain.StateAuthorityValidated, "ralphex-adapter", err, nil)
	}
	executionPlanPath, cleanupExecutionPlan, err := r.prepareExecutionPlan(ctx, repositoryLease)
	if err != nil {
		return r.fail(ctx, result, domain.StateAuthorityValidated, "ralphex-adapter", err, nil)
	}
	defer func() {
		if cleanupExecutionPlan != nil {
			runErr = errors.Join(runErr, cleanupExecutionPlan())
		}
	}()
	invocation, err := r.invocation(configDir, executionPlanPath)
	if err == nil {
		invocation.ValidationSpecPath = validationSpecPath
	}
	if err != nil {
		return r.fail(ctx, result, domain.StateAuthorityValidated, "ralphex-adapter", err, nil)
	}
	argv, err := invocation.Argv()
	if err != nil {
		return r.fail(ctx, result, domain.StateAuthorityValidated, "ralphex-adapter", err, nil)
	}
	ralphexTimeout, err := time.ParseDuration(r.governed.Ralphex().Timeout)
	if err != nil {
		return r.fail(ctx, result, domain.StateAuthorityValidated, "ralphex-adapter", fmt.Errorf("parse governed Ralphex timeout: %w", err), nil)
	}
	if err := r.transition(ctx, domain.StateAuthorityValidated, domain.StateExecutionStarting, "governed-runner", map[string]any{
		"argv": argv, "timeout": r.governed.Ralphex().Timeout, "environment_policy": ralphexEnvironmentPolicy,
	}, nil); err != nil {
		return result, err
	}
	result.State = domain.StateExecutionStarting
	if err := r.transition(ctx, domain.StateExecutionStarting, domain.StateImplementing, "ralphex-adapter", map[string]any{"argv": argv}, nil); err != nil {
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
			return r.fail(ctx, result, domain.StateImplementing, "governance-controller", err, nil)
		}
	}
	var process supervisor.Result
	var processErr error
	var containment *ContainmentEvidenceV1
	if invocation.Bounds != nil {
		contained, ok := r.processes.(ContainedCommandRunner)
		if !ok {
			return r.fail(ctx, result, domain.StateImplementing, "ralphex-adapter", errors.New("EXECUTION_BOUNDS_INVALID: Linux containment handoff is unavailable"), nil)
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
	if cleanupExecutionPlan != nil {
		processErr = errors.Join(processErr, cleanupExecutionPlan())
		cleanupExecutionPlan = nil
	}
	if repositoryLease != nil {
		processErr = errors.Join(processErr, repositoryLease.Close())
		repositoryLease = nil
	}
	if invocation.Bounds != nil {
		if finishErr := r.controller.FinishRalphexInvocationV1(reservation); finishErr != nil {
			processErr = errors.Join(processErr, finishErr)
		}
	}
	if invocation.Mode == ralphex.ModeReview {
		if reviewErr := validateReadOnlyReviewRepositoryV1(ctx, r.governed.Repository().Path, r.governed.Repository().StartSHA); reviewErr != nil {
			processErr = errors.Join(processErr, reviewErr)
		}
	}
	result.Ralphex = process
	if processErr != nil {
		if _, api := APICancelProvenance(ctx); api {
			return r.cancel(ctx, result, domain.StateImplementing, "ralphex-adapter", processErr, processRefs(process))
		}
		return r.fail(ctx, result, domain.StateImplementing, "ralphex-adapter", processErr, processRefs(process))
	}
	metadataRef, err := r.writeJSON("ralphex-process.json", "ralphex-process-metadata", struct {
		Process           supervisor.Result      `json:"process"`
		EnvironmentPolicy string                 `json:"environment_policy"`
		EvidenceRefs      []ledger.EvidenceRef   `json:"evidence_refs"`
		Containment       *ContainmentEvidenceV1 `json:"containment,omitempty"`
	}{Process: process, EnvironmentPolicy: ralphexEnvironmentPolicy, EvidenceRefs: processRefs(process), Containment: containment})
	if err != nil {
		return r.fail(ctx, result, domain.StateImplementing, "ralphex-adapter", err, processRefs(process))
	}
	result.RalphexMetadataRef = metadataRef
	implementationRefs := append(processRefs(process), metadataRef)

	_, apiCancelActive := APICancelProvenance(ctx)
	if process.Outcome != supervisor.OutcomeSucceeded || apiCancelActive {
		terminal := domain.StateFailed
		if process.Outcome == supervisor.OutcomeCanceled || apiCancelActive {
			terminal = domain.StateCancelled
		}
		reason := fmt.Sprintf("Ralphex ended with outcome %s", process.Outcome)
		result.State = terminal
		result.FailureReason = reason
		payload := map[string]any{
			"outcome": process.Outcome, "exit_code": process.ExitCode, "signal": process.TerminatingSignal,
		}
		if err := r.transition(ctx, domain.StateImplementing, terminal, "ralphex-adapter", payload, implementationRefs); err != nil {
			return result, err
		}
		return result, nil
	}
	if invocation.Bounds != nil {
		admission, _ := r.governed.Governance()
		if admission.Operation == contextcapsule.OperationImplementationReview && admission.Mutation {
			candidateSHA, headErr := gitOutput(ctx, r.governed.Repository().Path, "rev-parse", "--verify", "HEAD^{commit}")
			if headErr != nil {
				return r.fail(ctx, result, domain.StateImplementing, "governance-controller", fmt.Errorf("resolve mutation result HEAD: %w", headErr), implementationRefs)
			}
			receipt, receiptErr := r.controller.CompleteMutationReceiptV1(r.governed.Repository().Path, r.parsedCapsule, admission.LeaseSHA256, candidateSHA)
			if receiptErr != nil {
				return r.fail(ctx, result, domain.StateImplementing, "governance-controller", receiptErr, implementationRefs)
			}
			receiptRef, writeErr := r.writeJSON("mutation-receipt.json", "mutation-receipt", receipt)
			if writeErr != nil {
				return r.fail(ctx, result, domain.StateImplementing, "governance-controller", writeErr, implementationRefs)
			}
			result.MutationReceiptRef = receiptRef
			implementationRefs = append(implementationRefs, receiptRef)
		}
	}

	transition := r.transition
	if invocation.Bounds != nil {
		transition = r.finalTransition
	}
	if err := transition(ctx, domain.StateImplementing, domain.StateImplementationCompleted, "ralphex-adapter", map[string]any{
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
			return r.cancel(ctx, result, domain.StateImplementationCompleted, "acceptance-controller", err, implementationRefs)
		}
		return r.fail(ctx, result, domain.StateImplementationCompleted, "acceptance-controller", err, implementationRefs)
	}
	candidateRef, err := r.writeJSON("candidate-branch.json", "candidate-branch", target)
	if err != nil {
		cleanupErr := cleanup()
		return r.fail(ctx, result, domain.StateImplementationCompleted, "acceptance-controller", errors.Join(err, cleanupErr), implementationRefs)
	}
	result.CandidateEvidenceRef = candidateRef
	implementationRefs = append(implementationRefs, candidateRef)
	if err := r.transition(ctx, domain.StateImplementationCompleted, domain.StateBranchAcceptancePending, "acceptance-controller", nil, implementationRefs); err != nil {
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
		payload := map[string]any{
			"status": accepted.Status(), "reason": result.FailureReason,
		}
		if err := r.transition(ctx, domain.StateBranchAcceptancePending, domain.StateCancelled, "acceptance-controller", payload, acceptanceRefs); err != nil {
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
		if err := r.transition(ctx, domain.StateBranchAcceptancePending, domain.StateValidationUnavailable, "acceptance-controller", map[string]any{
			"status": accepted.Status(), "reason": result.FailureReason,
		}, acceptanceRefs); err != nil {
			return result, errors.Join(acceptanceErr, err)
		}
		return result, fmt.Errorf("run branch acceptance: %w", acceptanceErr)
	}
	if !accepted.Passed() {
		result.State = domain.StateFailed
		result.FailureReason = accepted.FailureReason()
		if err := r.transition(ctx, domain.StateBranchAcceptancePending, domain.StateFailed, "acceptance-controller", map[string]any{
			"status": accepted.Status(), "reason": result.FailureReason,
		}, acceptanceRefs); err != nil {
			return result, err
		}
		return result, nil
	}

	if err := r.finalTransition(ctx, domain.StateBranchAcceptancePending, domain.StateBranchAccepted, "acceptance-controller", map[string]any{
		"status": accepted.Status(),
	}, acceptanceRefs); err != nil {
		return result, err
	}
	result.State = domain.StateBranchAccepted
	return result, nil
}

func validateReadOnlyReviewRepositoryV1(ctx context.Context, repository, expectedHEAD string) error {
	head, err := gitOutput(ctx, repository, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || head != expectedHEAD {
		return errors.New("FINAL_REVIEW_INVALIDATED: read-only review changed the exact repository HEAD")
	}
	status, err := gitOutput(ctx, repository, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil || status != "" {
		return errors.New("FINAL_REVIEW_INVALIDATED: read-only review left repository content dirty")
	}
	return nil
}

func (r *Runner) invocation(configDir, executionPlanPath string) (ralphex.Invocation, error) {
	policy := r.governed.Executor()
	executor := strings.ToLower(strings.TrimSpace(policy.Executor))
	if executor != "" && executor != "claude" && executor != "codex" {
		return ralphex.Invocation{}, fmt.Errorf("unsupported executor %q", executor)
	}
	invocation := ralphex.Invocation{
		BinaryPath:                r.governed.Ralphex().BinaryPath,
		PlanPath:                  executionPlanPath,
		ConfigDir:                 configDir,
		Mode:                      r.governed.Ralphex().Mode,
		Codex:                     executor == "codex",
		Worktree:                  r.governed.Worktree().Enabled,
		Branch:                    r.governed.Worktree().Branch,
		TaskModel:                 policy.TaskModel,
		TaskEffort:                policy.TaskEffort,
		ReviewModel:               policy.ReviewModel,
		ReviewEffort:              policy.ReviewEffort,
		WaitOnLimit:               r.governed.Ralphex().WaitOnLimit,
		MaxIterations:             r.governed.Ralphex().MaxIterations,
		SessionTimeout:            r.governed.Ralphex().SessionTimeout,
		IdleTimeout:               r.governed.Ralphex().IdleTimeout,
		MaxInternalReviewPasses:   r.governed.Ralphex().MaxInternalReviewPasses,
		LongRunningSubprocessMode: r.governed.Ralphex().LongRunningSubprocessMode,
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

func (r *Runner) prepareValidationSpec(configDir string) (string, error) {
	runtime := r.governed.Ralphex()
	if runtime.LongRunningSubprocessMode != "orchestrator" {
		return "", nil
	}
	if len(runtime.Validation) == 0 {
		return "", errors.New("governed orchestrator validation commands are absent")
	}
	type command struct {
		ID               string   `json:"id"`
		Argv             []string `json:"argv"`
		Cwd              string   `json:"cwd,omitempty"`
		Timeout          string   `json:"timeout"`
		ExpectedDuration string   `json:"expected_duration,omitempty"`
		StallTimeout     string   `json:"stall_timeout,omitempty"`
	}
	spec := struct {
		SchemaVersion int       `json:"schema_version"`
		Commands      []command `json:"commands"`
	}{SchemaVersion: 1}
	for _, configured := range runtime.Validation {
		argv := append([]string(nil), configured.Argv...)
		for i, arg := range argv {
			if arg == "{{repository_base_sha}}" {
				argv[i] = r.governed.Repository().StartSHA
			}
		}
		spec.Commands = append(spec.Commands, command{ID: configured.ID, Argv: argv, Cwd: configured.Cwd, Timeout: configured.Timeout, ExpectedDuration: configured.ExpectedDuration, StallTimeout: configured.StallTimeout})
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("marshal governed validation spec: %w", err)
	}
	path := filepath.Join(configDir, "abcp-validation-v1.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write governed validation spec: %w", err)
	}
	return path, nil
}

// prepareExecutionPlan keeps the immutable authority plan separate from the
// mutable plan Ralphex ticks and archives. A tracked authority plan is already
// present in a worktree. Product-admission plans are deliberately Git-ignored,
// however, so Ralphex cannot detect and copy them when it creates its worktree.
// For that case the controller creates a bounded untracked copy only after the
// clean-tree and authority-hash checks have passed.
func (r *Runner) prepareExecutionPlan(ctx context.Context, lease *repositoryExecutionLease) (string, func() error, error) {
	plan := r.governed.Plan()
	if !r.governed.Worktree().Enabled {
		return plan.Path, nil, nil
	}
	repository := r.governed.Repository()
	relative, err := filepath.Rel(repository.Path, plan.Path)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", nil, errors.New("prepare Ralphex execution plan: governed plan is outside the repository")
	}
	tracked := exec.CommandContext(ctx, "git", "cat-file", "blob", repository.StartSHA+":"+filepath.ToSlash(relative))
	tracked.Dir = repository.Path
	tracked.Env = gitexec.Environment()
	trackedContents, err := tracked.Output()
	if err == nil {
		trackedSum := sha256.Sum256(trackedContents)
		if hex.EncodeToString(trackedSum[:]) != plan.SHA256 {
			return "", nil, errors.New("governed plan blob in start commit differs from governed plan")
		}
		return plan.Path, nil, nil
	} else {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 128 {
			return "", nil, fmt.Errorf("inspect governed plan in start commit: %w", err)
		}
	}

	contents, err := os.ReadFile(plan.Path)
	if err != nil {
		return "", nil, fmt.Errorf("read governed plan for Ralphex execution copy: %w", err)
	}
	sum := sha256.Sum256([]byte(r.governed.RunID()))
	runDigest := hex.EncodeToString(sum[:])
	relativePath := filepath.ToSlash(filepath.Join(ralphex.ExecutionPlanHandoffPrefixV1+runDigest, "plan.md"))
	directory := filepath.Join(repository.Path, filepath.Dir(filepath.FromSlash(relativePath)))
	owner := executionPlanHandoffOwnerV1{
		Kind: executionPlanOwnerKind, SchemaVersion: 1, RunID: r.governed.RunID(),
		RelativePath: relativePath, SHA256: plan.SHA256,
	}
	ownerPath, err := writeExecutionPlanHandoffOwner(lease, owner)
	if err != nil {
		return "", nil, fmt.Errorf("record Ralphex execution plan ownership: %w", err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		_ = removeExecutionPlanHandoff(lease, ownerPath)
		return "", nil, fmt.Errorf("create Ralphex execution plan directory: %w", err)
	}
	path := filepath.Join(directory, "plan.md")
	cleanup := func() error {
		return removeExecutionPlanHandoff(lease, ownerPath)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("create Ralphex execution plan copy: %w", err)
	}
	written, writeErr := file.Write(contents)
	if writeErr == nil && written != len(contents) {
		writeErr = io.ErrShortWrite
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("write Ralphex execution plan copy: %w", errors.Join(writeErr, syncErr, closeErr))
	}
	copySHA, err := hashFile(path)
	if err != nil || copySHA != plan.SHA256 {
		_ = cleanup()
		return "", nil, errors.Join(errors.New("Ralphex execution plan copy differs from governed plan"), err)
	}
	copyRelative, err := filepath.Rel(repository.Path, path)
	if err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("resolve Ralphex execution plan copy: %w", err)
	}
	status, err := gitOutput(ctx, repository.Path, "status", "--porcelain=v1", "--untracked-files=all", "--", filepath.ToSlash(copyRelative))
	if err != nil || status != "?? "+filepath.ToSlash(copyRelative) {
		_ = cleanup()
		return "", nil, errors.Join(errors.New("Ralphex execution plan copy is not visible to Git for worktree handoff"), err)
	}
	return path, cleanup, nil
}

type repositoryExecutionLease struct {
	file       *os.File
	repository string
	ownersDir  string
}

type executionPlanHandoffOwnerV1 struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	RelativePath  string `json:"relative_path"`
	SHA256        string `json:"sha256"`
}

func recoverExecutionPlanHandoffs(lease *repositoryExecutionLease) error {
	if lease == nil || lease.file == nil {
		return errors.New("repository execution lease is required")
	}
	if err := ensureProtectedRunDirectory(filepath.Dir(lease.ownersDir)); err != nil {
		return err
	}
	if err := ensureProtectedRunDirectory(lease.ownersDir); err != nil {
		return err
	}
	entries, err := os.ReadDir(lease.ownersDir)
	if err != nil {
		return err
	}
	if len(entries) > maxExecutionPlanOwners {
		return errors.New("too many Ralphex execution plan ownership records")
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".json.tmp") && validLowerDigest(strings.TrimSuffix(name, ".json.tmp")) {
			info, infoErr := entry.Info()
			if infoErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
				return errors.Join(errors.New("invalid temporary Ralphex execution plan ownership record"), infoErr)
			}
			if err := os.Remove(filepath.Join(lease.ownersDir, name)); err != nil {
				return err
			}
			continue
		}
		if !strings.HasSuffix(name, ".json") || !validLowerDigest(strings.TrimSuffix(name, ".json")) {
			return errors.New("invalid Ralphex execution plan ownership record name")
		}
		if err := removeExecutionPlanHandoff(lease, filepath.Join(lease.ownersDir, name)); err != nil {
			return err
		}
	}
	return syncRunDirectory(lease.ownersDir)
}

func writeExecutionPlanHandoffOwner(lease *repositoryExecutionLease, owner executionPlanHandoffOwnerV1) (string, error) {
	if lease == nil || lease.file == nil {
		return "", errors.New("repository execution lease is required")
	}
	if err := ensureProtectedRunDirectory(filepath.Dir(lease.ownersDir)); err != nil {
		return "", err
	}
	if err := ensureProtectedRunDirectory(lease.ownersDir); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(owner.RunID))
	name := hex.EncodeToString(digest[:]) + ".json"
	if err := validateExecutionPlanHandoffOwner(owner, name); err != nil {
		return "", err
	}
	data, err := json.Marshal(owner)
	if err != nil {
		return "", err
	}
	path := filepath.Join(lease.ownersDir, name)
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	written, writeErr := file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	closeErr := errors.Join(file.Sync(), file.Close())
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(temporary)
		return "", errors.Join(writeErr, closeErr)
	}
	if err := os.Link(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return "", err
	}
	if err := os.Remove(temporary); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := syncRunDirectory(lease.ownersDir); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func removeExecutionPlanHandoff(lease *repositoryExecutionLease, ownerPath string) error {
	owner, err := readExecutionPlanHandoffOwner(ownerPath)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(owner.RunID))
	if filepath.Base(ownerPath) != hex.EncodeToString(digest[:])+".json" {
		return errors.New("Ralphex execution plan ownership filename does not match run")
	}
	path := filepath.Join(lease.repository, filepath.FromSlash(owner.RelativePath))
	directory := filepath.Dir(path)
	if info, statErr := os.Lstat(path); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("owned Ralphex execution plan is not a protected regular file")
		}
		observed, hashErr := hashFile(path)
		if hashErr != nil || observed != owner.SHA256 {
			return errors.Join(errors.New("owned Ralphex execution plan hash changed"), hashErr)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if info, statErr := os.Lstat(directory); statErr == nil {
		if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("owned Ralphex execution plan directory is not protected")
		}
		if err := os.Remove(directory); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err := os.Remove(ownerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncRunDirectory(lease.ownersDir)
}

func readExecutionPlanHandoffOwner(path string) (executionPlanHandoffOwnerV1, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return executionPlanHandoffOwnerV1{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxExecutionPlanOwnerBytes {
		return executionPlanHandoffOwnerV1{}, errors.New("invalid Ralphex execution plan ownership record")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return executionPlanHandoffOwnerV1{}, err
	}
	var owner executionPlanHandoffOwnerV1
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&owner); err != nil {
		return executionPlanHandoffOwnerV1{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return executionPlanHandoffOwnerV1{}, errors.New("Ralphex execution plan ownership record has trailing data")
	}
	if err := validateExecutionPlanHandoffOwner(owner, filepath.Base(path)); err != nil {
		return executionPlanHandoffOwnerV1{}, err
	}
	return owner, nil
}

func validateExecutionPlanHandoffOwner(owner executionPlanHandoffOwnerV1, name string) error {
	if owner.Kind != executionPlanOwnerKind || owner.SchemaVersion != 1 || owner.RunID == "" || !validLowerDigest(owner.SHA256) {
		return errors.New("invalid Ralphex execution plan ownership record")
	}
	digest := sha256.Sum256([]byte(owner.RunID))
	runDigest := hex.EncodeToString(digest[:])
	wantRelative := ralphex.ExecutionPlanHandoffPrefixV1 + runDigest + "/plan.md"
	if owner.RelativePath != wantRelative || name != runDigest+".json" {
		return errors.New("Ralphex execution plan ownership binding is invalid")
	}
	return nil
}

func ensureProtectedRunDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.Join(errors.New("Ralphex execution plan ownership directory is not protected"), err)
	}
	return nil
}

func executionPlanOwnersDirectory(common, repository string) string {
	sum := sha256.Sum256([]byte(repository))
	return filepath.Join(common, executionPlanOwnerRoot, hex.EncodeToString(sum[:]))
}

func syncRunDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func (r *Runner) appendCreated(authorityRef ledger.EvidenceRef) error {
	event, err := ledger.NewEvent(r.governed.RunID(), string(domain.StateRunCreated), actorController, "governed-runner")
	if err != nil {
		return fmt.Errorf("create RUN_CREATED event: %w", err)
	}
	event.Payload = map[string]any{"state": domain.StateRunCreated, "authority_sha256": r.governed.SHA256()}
	event.EvidenceRefs = []ledger.EvidenceRef{authorityRef}
	r.applyTransitionProvenance(&event)
	if err := r.events.Append(event); err != nil {
		return fmt.Errorf("append RUN_CREATED event: %w", err)
	}
	return nil
}

func (r *Runner) transition(ctx context.Context, from, to domain.State, source string, payload map[string]any, refs []ledger.EvidenceRef) error {
	return r.transitionWithFinalization(ctx, from, to, source, payload, refs, false)
}

func (r *Runner) finalTransition(ctx context.Context, from, to domain.State, source string, payload map[string]any, refs []ledger.EvidenceRef) error {
	return r.transitionWithFinalization(ctx, from, to, source, payload, refs, true)
}

func (r *Runner) transitionWithFinalization(ctx context.Context, from, to domain.State, source string, payload map[string]any, refs []ledger.EvidenceRef, successfulReturn bool) (resultErr error) {
	if ctx == nil {
		return errors.New("transition context is required")
	}
	if !ep002State(to) {
		return fmt.Errorf("EP-002 runner cannot transition to %s", to)
	}
	if err := domain.ValidateTransition(from, to); err != nil {
		return fmt.Errorf("validate %s -> %s transition: %w", from, to, err)
	}
	r.lifecycleMu.Lock()
	snapshots := r.snapshots
	r.lifecycleMu.Unlock()
	if snapshots != nil {
		// Cancellation is part of the durable transition being selected, not a
		// reason to abandon its authoritative reconstruction. Acquisition starts
		// with the caller context; if cancellation wins before the operation is
		// invoked, one bounded close-out attempt can still select the durable
		// terminal edge. Acquiring before the transition lease preserves the
		// service action lock order.
		invoked := false
		operation := func() error {
			invoked = true
			return r.transitionWithReservedSnapshot(ctx, from, to, source, payload, refs, successfulReturn)
		}
		if ctx.Err() == nil {
			err := snapshots.WithSnapshot(ctx, operation)
			if invoked || ctx.Err() == nil || (!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)) {
				return err
			}
		}
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runnerSnapshotCloseWait)
		defer cancel()
		return snapshots.WithSnapshot(closeCtx, operation)
	}
	return r.transitionWithReservedSnapshot(ctx, from, to, source, payload, refs, successfulReturn)
}

func (r *Runner) transitionWithReservedSnapshot(ctx context.Context, from, to domain.State, source string, payload map[string]any, refs []ledger.EvidenceRef, successfulReturn bool) (resultErr error) {
	atomic, ok := r.events.(atomicTransitionEventAppender)
	if !ok {
		return errors.New("atomic run-transition appender is required")
	}
	lease, err := atomic.AcquireRunTransition(r.governed.RunID())
	if err != nil {
		return fmt.Errorf("acquire %s transition selection: %w", to, err)
	}
	defer func() { resultErr = errors.Join(resultErr, lease.Close()) }()
	current, err := r.currentStateUnderTransitionLease(atomic)
	if err != nil {
		return err
	}
	if current != from {
		return fmt.Errorf("run transition selection advanced from %s to %s", from, current)
	}
	selectedCancellation := false
	if _, api := APICancelProvenance(ctx); api {
		if to != domain.StateCancelled {
			selectedCancellation = true
			to = domain.StateCancelled
			payload = map[string]any{"reason": context.Cause(ctx).Error()}
			if err := domain.ValidateTransition(from, to); err != nil {
				return fmt.Errorf("select API cancellation from %s: %w", from, err)
			}
		}
		payload, err = r.cancellationPayload(ctx, payload)
		if err != nil {
			return err
		}
	}
	if successfulReturn && !selectedCancellation && r.finalization != nil {
		if err := r.finalization.BeginClose(ctx); err != nil {
			return fmt.Errorf("freeze successful run finalization: %w", err)
		}
	}
	event, err := ledger.NewEvent(r.governed.RunID(), eventStateTransition, actorController, source)
	if err != nil {
		return fmt.Errorf("create %s transition: %w", to, err)
	}
	event.StateFrom = from
	event.StateTo = to
	r.applyTransitionProvenance(&event)
	event.Payload = payload
	event.EvidenceRefs = append([]ledger.EvidenceRef(nil), refs...)
	if err := atomic.AppendOrVerifyLeased(event, lease); err != nil {
		return fmt.Errorf("append %s transition: %w", to, err)
	}
	if selectedCancellation {
		return &apiCancellationSelected{cause: context.Cause(ctx)}
	}
	return nil
}

func (r *Runner) currentStateUnderTransitionLease(snapshotter eventSnapshotter) (domain.State, error) {
	data, _, err := snapshotter.Snapshot()
	if err != nil || len(data) == 0 || data[len(data)-1] != '\n' {
		return "", errors.Join(errors.New("atomic run-transition history is unavailable"), err)
	}
	current := domain.State("")
	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 256<<10)
	for scanner.Scan() {
		var event ledger.Event
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Validate() != nil || event.RunID != r.governed.RunID() {
			return "", errors.New("atomic run-transition history is invalid")
		}
		if _, duplicate := seen[event.EventID]; duplicate {
			return "", errors.New("atomic run-transition history contains a duplicate event")
		}
		seen[event.EventID] = struct{}{}
		if event.StateFrom == "" {
			if event.EventType == string(domain.StateRunCreated) {
				state, _ := event.Payload["state"].(string)
				if current != "" || state != string(domain.StateRunCreated) {
					return "", errors.New("atomic run-transition initial state is invalid")
				}
				current = domain.StateRunCreated
			}
			continue
		}
		if current == "" || event.StateFrom != current {
			return "", errors.New("atomic run-transition chronology is invalid")
		}
		current = event.StateTo
	}
	if scanner.Err() != nil || current == "" {
		return "", errors.Join(errors.New("atomic run-transition history is incomplete"), scanner.Err())
	}
	return current, nil
}

func (r *Runner) applyTransitionProvenance(event *ledger.Event) {
	event.ProjectID = r.provenance.ProjectID
	event.PlanID = r.provenance.PlanID
	event.AttemptID = r.provenance.AttemptID
}

func validateTransitionProvenance(value TransitionProvenance) error {
	for name, field := range map[string]string{"project": value.ProjectID, "plan": value.PlanID, "attempt": value.AttemptID} {
		if field == "" || len(field) > 4096 || strings.TrimSpace(field) != field || strings.IndexAny(field, "\r\n\x00") >= 0 {
			return fmt.Errorf("%s transition provenance is invalid", name)
		}
	}
	return nil
}

func (r *Runner) fail(ctx context.Context, result Result, from domain.State, source string, cause error, refs []ledger.EvidenceRef) (Result, error) {
	result.State = domain.StateFailed
	result.FailureReason = cause.Error()
	if err := r.transition(ctx, from, domain.StateFailed, source, map[string]any{"reason": cause.Error()}, refs); err != nil {
		return result, errors.Join(cause, err)
	}
	return result, cause
}

func (r *Runner) cancel(ctx context.Context, result Result, from domain.State, source string, cause error, refs []ledger.EvidenceRef) (Result, error) {
	result.State = domain.StateCancelled
	result.FailureReason = cause.Error()
	if err := r.transition(ctx, from, domain.StateCancelled, source, map[string]any{"reason": cause.Error()}, refs); err != nil {
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

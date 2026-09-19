package actionapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/actioncontrol"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

const decisionPolicyVersion = "ep006-governed-action-v1"

type projectionService interface {
	Snapshot(context.Context, string) (readmodel.Snapshot, error)
	WithExistingWritableLedgerAndSnapshot(context.Context, string, func(*ledger.JSONLLedger, func() (readmodel.Snapshot, error)) error) error
}

type ControllerConfig struct {
	Catalog   *runtimecatalog.Catalog
	ReadModel projectionService
	Journal   *actioncontrol.Journal
	Authority *serviceapi.AuthorityMatcher
}

type Controller struct {
	catalog   *runtimecatalog.Catalog
	model     projectionService
	journal   *actioncontrol.Journal
	authority *serviceapi.AuthorityMatcher
	ctx       context.Context
	cancel    context.CancelFunc
	queue     chan actionWork
	mu        sync.Mutex
	queued    map[string]struct{}
	wg        sync.WaitGroup
}

type actionWork struct{ runID, operationID string }

func NewController(config ControllerConfig) (*Controller, error) {
	if config.Catalog == nil || config.ReadModel == nil || config.Journal == nil || config.Authority == nil || config.Authority.Digest() == "" {
		return nil, errors.New("complete action controller dependencies are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	controller := &Controller{catalog: config.Catalog, model: config.ReadModel, journal: config.Journal, authority: config.Authority,
		ctx: ctx, cancel: cancel, queue: make(chan actionWork, 256), queued: map[string]struct{}{}}
	for index := 0; index < 2; index++ {
		controller.wg.Add(1)
		go controller.worker()
	}
	return controller, nil
}

func (c *Controller) Close() error {
	if c == nil {
		return nil
	}
	c.cancel()
	c.wg.Wait()
	return nil
}

func (c *Controller) AdmitAction(ctx context.Context, principal serviceapi.Principal, runID string, kind serviceapi.ActionKind, command serviceapi.CommandEnvelopeV1) (serviceapi.ActionStatusV1, error) {
	if c == nil || ctx == nil || runtimecatalog.ValidateIdentifier(runID) != nil || serviceapi.ValidateCommandEnvelopeV1(command, kind == serviceapi.ActionDecision) != nil {
		return serviceapi.ActionStatusV1{}, serviceapi.ErrDecisionRequestInvalid
	}
	if kind != serviceapi.ActionCancel && kind != serviceapi.ActionDecision {
		return serviceapi.ActionStatusV1{}, serviceapi.ErrUnsupportedCapability
	}
	if serviceapi.ValidatePrincipalID(principal.PrincipalID) != nil || (principal.PrincipalType != serviceapi.PrincipalService && principal.PrincipalType != serviceapi.PrincipalUser && principal.PrincipalType != serviceapi.PrincipalOperator && principal.PrincipalType != serviceapi.PrincipalTest) || principal.AuthnMethod == "" {
		return serviceapi.ActionStatusV1{}, serviceapi.ErrAuthorityDenied
	}
	var canonicalPayload json.RawMessage
	var decisionPayload DecisionPayloadV1
	var err error
	if kind == serviceapi.ActionCancel {
		canonicalPayload, err = DecodeCancelPayload(command.Payload)
	} else {
		decisionPayload, canonicalPayload, err = DecodeDecisionPayload(command.Payload)
	}
	if err != nil {
		return serviceapi.ActionStatusV1{}, err
	}
	requestDigest := semanticDigest(principal, runID, kind, command, canonicalPayload)
	replay := actioncontrol.ReceiptReplayInput{PrincipalID: principal.PrincipalID, PrincipalType: string(principal.PrincipalType),
		RequestID: command.RequestID, RequestSHA256: requestDigest, Action: string(kind), RunID: runID, AttemptID: command.AttemptID}
	if existing, lookupErr := c.journal.ReplayReceipt(ctx, replay); lookupErr == nil {
		if existing.Receipt.Action == string(serviceapi.ActionCancel) && existing.Status == actioncontrol.StatusReceived {
			existing, lookupErr = c.rejectCancelOutsideOwnerWindow(ctx, existing)
			if lookupErr != nil {
				return serviceapi.ActionStatusV1{}, lookupErr
			}
		}
		if existing.Receipt.Action == string(serviceapi.ActionDecision) && !isTerminalStatus(existing.Status) {
			c.enqueue(actionWork{runID, existing.Receipt.OperationID})
		}
		return statusFor(existing), nil
	} else if !errors.Is(lookupErr, actioncontrol.ErrNotFound) {
		return serviceapi.ActionStatusV1{}, mapJournalError(lookupErr)
	}
	if kind == serviceapi.ActionDecision {
		if _, decisionErr := c.journal.ReadByDecisionRequest(ctx, runID, decisionPayload.DecisionRequestID); decisionErr == nil {
			return serviceapi.ActionStatusV1{}, serviceapi.ErrDecisionAlreadyRecorded
		} else if !errors.Is(decisionErr, actioncontrol.ErrNotFound) {
			return serviceapi.ActionStatusV1{}, mapJournalError(decisionErr)
		}
	}
	snapshot, transitionID, err := c.validateCommon(ctx, runID, command)
	if err != nil {
		return serviceapi.ActionStatusV1{}, err
	}
	if kind == serviceapi.ActionDecision {
		if _, err := c.validateDecision(principal, command, snapshot, runID, decisionPayload); err != nil {
			return serviceapi.ActionStatusV1{}, err
		}
	}
	input := actioncontrol.ReceiptInput{
		PrincipalID: principal.PrincipalID, PrincipalType: string(principal.PrincipalType), RequestID: command.RequestID,
		RequestSHA256: requestDigest, Action: string(kind), RunID: runID, AttemptID: command.AttemptID,
		ExpectedState: command.ExpectedState, ExpectedRevision: command.ExpectedRevision, Reason: command.Reason,
		Payload: canonicalPayload, StateTransitionID: transitionID,
	}
	if kind == serviceapi.ActionDecision {
		input.PolicyVersion, input.AuthorityGrantSHA256 = decisionPolicyVersion, c.authority.Digest()
	}
	if command.DelegatedActor != nil {
		input.DelegatedActor = &actioncontrol.DelegatedActorV1{SubjectID: command.DelegatedActor.SubjectID, SubjectType: string(command.DelegatedActor.SubjectType)}
	}
	var binder func(uint64) (actioncontrol.AdmissionBinding, error)
	if kind == serviceapi.ActionCancel {
		binder = func(sequence uint64) (actioncontrol.AdmissionBinding, error) {
			guard, err := c.catalog.AcquireOwnerLeaseGuard(runID)
			if err != nil {
				return actioncontrol.AdmissionBinding{}, err
			}
			owner, err := guard.Lease()
			if err != nil || owner.RunID != runID || owner.AttemptID != command.AttemptID || owner.State != runtimecatalog.OwnerLeaseActive || runtimecatalog.VerifyLiveOwner(owner) != nil {
				_ = guard.Close()
				return actioncontrol.AdmissionBinding{}, serviceapi.ErrActionTargetNotActive
			}
			_ = sequence
			return actioncontrol.AdmissionBinding{OwnerLeaseID: owner.LeaseID, Release: guard.Close}, nil
		}
	}
	receipt, _, err := c.journal.CreateReceipt(ctx, input, binder)
	if err != nil {
		return serviceapi.ActionStatusV1{}, mapJournalError(err)
	}
	operation, err := c.journal.Read(ctx, runID, receipt.OperationID)
	if err != nil {
		return serviceapi.ActionStatusV1{}, mapJournalError(err)
	}
	if kind == serviceapi.ActionDecision && !isTerminalStatus(operation.Status) {
		c.enqueue(actionWork{runID, receipt.OperationID})
	}
	return statusFor(operation), nil
}

func (c *Controller) rejectCancelOutsideOwnerWindow(ctx context.Context, operation actioncontrol.Operation) (actioncontrol.Operation, error) {
	guard, err := c.catalog.AcquireOwnerLeaseGuard(operation.Receipt.RunID)
	if err != nil {
		return operation, err
	}
	owner, leaseErr := guard.Lease()
	closeErr := guard.Close()
	if closeErr != nil {
		return operation, closeErr
	}
	eligible := leaseErr == nil && owner.LeaseID == operation.Receipt.OwnerLeaseID && owner.RunID == operation.Receipt.RunID &&
		owner.AttemptID == operation.Receipt.AttemptID && runtimecatalog.VerifyLiveOwner(owner) == nil &&
		(owner.State == runtimecatalog.OwnerLeaseActive || (owner.State == runtimecatalog.OwnerLeaseClosing &&
			owner.DrainThroughJournalSequence != nil && operation.Receipt.Sequence <= *owner.DrainThroughJournalSequence))
	if eligible {
		return operation, nil
	}
	if leaseErr != nil && !errors.Is(leaseErr, runtimecatalog.ErrOwnerNotActive) && !errors.Is(leaseErr, os.ErrNotExist) {
		return operation, leaseErr
	}
	if _, err := c.journal.AppendOutcome(ctx, operation.Receipt.RunID, operation.Receipt.OperationID, actioncontrol.StatusRejected, nil, "cancel_outside_owner_watermark"); err != nil {
		return operation, mapJournalError(err)
	}
	settled, err := c.journal.Read(ctx, operation.Receipt.RunID, operation.Receipt.OperationID)
	return settled, mapJournalError(err)
}

func (c *Controller) ReadAction(ctx context.Context, principal serviceapi.Principal, runID, operationID string) (serviceapi.ActionStatusV1, error) {
	if c == nil || ctx == nil || runtimecatalog.ValidateIdentifier(runID) != nil || runtimecatalog.ValidateIdentifier(operationID) != nil {
		return serviceapi.ActionStatusV1{}, serviceapi.ErrActionStatusNotFound
	}
	operation, err := c.journal.Read(ctx, runID, operationID)
	if err != nil {
		return serviceapi.ActionStatusV1{}, mapJournalError(err)
	}
	if operation.Receipt.PrincipalID != principal.PrincipalID {
		return serviceapi.ActionStatusV1{}, serviceapi.ErrActionStatusNotFound
	}
	if operation.Receipt.Action == string(serviceapi.ActionDecision) && !isTerminalStatus(operation.Status) {
		c.enqueue(actionWork{runID, operationID})
	} else if operation.Receipt.Action == string(serviceapi.ActionCancel) && operation.Claim != nil &&
		(operation.Status == actioncontrol.StatusClaimed || operation.Status == actioncontrol.StatusReconciliationRequired) {
		if changed, reconcileErr := c.reconcileAbandonedCancel(ctx, operation); reconcileErr != nil {
			return serviceapi.ActionStatusV1{}, reconcileErr
		} else if changed {
			operation, err = c.journal.Read(ctx, runID, operationID)
			if err != nil {
				return serviceapi.ActionStatusV1{}, mapJournalError(err)
			}
		}
	}
	return statusFor(operation), nil
}

func (c *Controller) reconcileAbandonedCancel(ctx context.Context, operation actioncontrol.Operation) (bool, error) {
	guard, err := c.catalog.AcquireOwnerLeaseGuard(operation.Receipt.RunID)
	if err != nil {
		return false, err
	}
	owner, leaseErr := guard.Lease()
	closeErr := guard.Close()
	if closeErr != nil {
		return false, closeErr
	}
	if leaseErr == nil && owner.LeaseID == operation.Receipt.OwnerLeaseID && runtimecatalog.VerifyLiveOwner(owner) == nil &&
		(owner.State == runtimecatalog.OwnerLeaseActive || owner.State == runtimecatalog.OwnerLeaseClosing) {
		return false, nil
	}
	if leaseErr != nil && !errors.Is(leaseErr, runtimecatalog.ErrOwnerNotActive) {
		// A missing/stale active record can be reconciled from authoritative
		// ledger proof; structural catalog corruption cannot.
		if !errors.Is(leaseErr, os.ErrNotExist) {
			return false, leaseErr
		}
	}
	if operation.Status == actioncontrol.StatusClaimed {
		if _, err := c.journal.AppendOutcome(ctx, operation.Receipt.RunID, operation.Receipt.OperationID, actioncontrol.StatusReconciliationRequired, nil, "cancel_owner_unavailable"); err != nil {
			return false, mapJournalError(err)
		}
	}
	snapshot, err := c.model.Snapshot(ctx, operation.Receipt.RunID)
	if err != nil {
		return true, err
	}
	requestEventID, cancelledEventID, proof, err := actioncontrol.ReconcileCancelHistory(snapshot, operation)
	if err != nil {
		return true, err
	}
	switch proof {
	case actioncontrol.CancelApplied:
		_, err = c.journal.AppendOutcome(ctx, operation.Receipt.RunID, operation.Receipt.OperationID, actioncontrol.StatusReconciledApplied, []string{requestEventID, cancelledEventID}, "")
	case actioncontrol.CancelNotApplied:
		ids := []string(nil)
		if requestEventID != "" {
			ids = []string{requestEventID}
		}
		_, err = c.journal.AppendOutcome(ctx, operation.Receipt.RunID, operation.Receipt.OperationID, actioncontrol.StatusReconciledNotApplied, ids, "cancel_owner_unavailable")
	case actioncontrol.CancelUnresolved:
		return true, nil
	}
	return true, mapJournalError(err)
}

func (c *Controller) validateCommon(ctx context.Context, runID string, command serviceapi.CommandEnvelopeV1) (readmodel.Snapshot, string, error) {
	if _, err := c.catalog.ReadAttempt(runID, command.AttemptID); err != nil {
		return readmodel.Snapshot{}, "", serviceapi.ErrActionStateAdvanced
	}
	snapshot, err := c.model.Snapshot(ctx, runID)
	if err != nil {
		return readmodel.Snapshot{}, "", err
	}
	if snapshot.Projection.CurrentState != command.ExpectedState {
		return readmodel.Snapshot{}, "", serviceapi.ErrStaleExpectedState
	}
	if snapshot.Projection.ProjectionRevision != command.ExpectedRevision {
		return readmodel.Snapshot{}, "", serviceapi.ErrStaleExpectedRevision
	}
	transition := latestStateTransition(snapshot.Events)
	if transition == nil || transition.RunID != runID || transition.AttemptID != command.AttemptID || transition.EventID == "" {
		return readmodel.Snapshot{}, "", serviceapi.ErrActionStateAdvanced
	}
	return snapshot, transition.EventID, nil
}

func (c *Controller) validateDecision(principal serviceapi.Principal, command serviceapi.CommandEnvelopeV1, snapshot readmodel.Snapshot, runID string, payload DecisionPayloadV1) (HumanDecisionRequestV1, error) {
	if command.ExpectedState != string(domain.StateHumanDecisionRequired) || command.DelegatedActor == nil {
		return HumanDecisionRequestV1{}, serviceapi.ErrDecisionRequestInvalid
	}
	request, err := DecodeHumanDecision(snapshot, runID, command.AttemptID, payload.DecisionRequestID)
	if err != nil {
		return HumanDecisionRequestV1{}, err
	}
	if !acceptedAnswer(request, payload.Answer) {
		return HumanDecisionRequestV1{}, serviceapi.ErrDecisionRequestInvalid
	}
	if !c.authority.MayAssertDelegatedActor(principal) || c.authority.Match(principal, request.RequiredAuthority) != nil {
		return HumanDecisionRequestV1{}, serviceapi.ErrAuthorityDenied
	}
	return request, nil
}

func (c *Controller) worker() {
	defer c.wg.Done()
	for {
		select {
		case <-c.ctx.Done():
			return
		case work := <-c.queue:
			if c.ctx.Err() != nil {
				return
			}
			_ = c.ProcessOperation(c.ctx, work.runID, work.operationID)
			c.mu.Lock()
			delete(c.queued, work.runID+"\x00"+work.operationID)
			c.mu.Unlock()
		}
	}
}

func (c *Controller) enqueue(work actionWork) {
	key := work.runID + "\x00" + work.operationID
	c.mu.Lock()
	if _, exists := c.queued[key]; exists {
		c.mu.Unlock()
		return
	}
	select {
	case c.queue <- work:
		c.queued[key] = struct{}{}
	default:
	}
	c.mu.Unlock()
}

// ProcessOperation is a deterministic worker entry point exposed for focused
// controller tests. It never processes cancel operations; those belong only
// to the exact owner-generation watcher.
func (c *Controller) ProcessOperation(ctx context.Context, runID, operationID string) error {
	operation, err := c.journal.Read(ctx, runID, operationID)
	if err != nil {
		return mapJournalError(err)
	}
	if operation.Receipt.Action != string(serviceapi.ActionDecision) || isTerminalStatus(operation.Status) {
		return nil
	}
	if operation.Claim != nil {
		return c.resolveDecision(ctx, runID, operationID, false)
	}
	principal := receiptPrincipal(operation.Receipt)
	command := receiptCommand(operation.Receipt)
	payload, _, decodeErr := DecodeDecisionPayload(operation.Receipt.Payload)
	if operation.Receipt.PolicyVersion != decisionPolicyVersion || operation.Receipt.AuthorityGrantSHA256 != c.authority.Digest() {
		decodeErr = serviceapi.ErrAuthorityDenied
	}
	snapshot, snapshotErr := c.model.Snapshot(ctx, runID)
	if snapshotErr != nil {
		return snapshotErr
	}
	if decodeErr == nil {
		if !receiptSnapshotMatches(snapshot, operation.Receipt) {
			decodeErr = serviceapi.ErrActionStateAdvanced
		} else {
			_, decodeErr = c.validateDecision(principal, command, snapshot, runID, payload)
		}
	}
	if decodeErr != nil {
		_, appendErr := c.journal.AppendOutcome(ctx, runID, operationID, actioncontrol.StatusRejected, nil, rejectionCode(decodeErr))
		return errors.Join(decodeErr, mapJournalError(appendErr))
	}
	_, created, err := c.journal.CreateClaim(ctx, runID, operationID, "", actioncontrol.DecisionEventDomain)
	if err != nil {
		return mapJournalError(err)
	}
	if !created {
		return c.resolveDecision(ctx, runID, operationID, false)
	}
	return c.resolveDecision(ctx, runID, operationID, true)
}

func (c *Controller) resolveDecision(ctx context.Context, runID, operationID string, mayApply bool) (resultErr error) {
	authority, operation, err := c.journal.AcquireDecisionEffect(ctx, runID, operationID)
	if err != nil {
		return mapJournalError(err)
	}
	defer func() { resultErr = errors.Join(resultErr, mapJournalError(authority.Close())) }()
	if isTerminalStatus(operation.Status) {
		return nil
	}
	if mayApply && operation.Status == actioncontrol.StatusClaimed {
		return c.applyDecision(ctx, authority, operation)
	}
	return c.reconcileDecision(ctx, authority)
}

func (c *Controller) applyDecision(ctx context.Context, authority *actioncontrol.DecisionEffectLease, operation actioncontrol.Operation) error {
	receipt, claim := operation.Receipt, *operation.Claim
	var appendAttempted bool
	err := c.model.WithExistingWritableLedgerAndSnapshot(ctx, receipt.RunID, func(writer *ledger.JSONLLedger, authoritativeSnapshot func() (readmodel.Snapshot, error)) (resultErr error) {
		lease, err := writer.AcquireRunTransition(receipt.RunID)
		if err != nil {
			return err
		}
		defer func() { resultErr = errors.Join(resultErr, lease.Close()) }()
		snapshot, err := authoritativeSnapshot()
		if err != nil {
			return err
		}
		principal := receiptPrincipal(receipt)
		command := receiptCommand(receipt)
		payload, _, err := DecodeDecisionPayload(receipt.Payload)
		if err != nil {
			return err
		}
		request, err := c.validateDecision(principal, command, snapshot, receipt.RunID, payload)
		if err != nil || request.TransitionEventID != receipt.AdmittedStateTransitionID {
			return errors.Join(serviceapi.ErrActionStateAdvanced, err)
		}
		if receipt.PolicyVersion != decisionPolicyVersion || receipt.AuthorityGrantSHA256 != c.authority.Digest() {
			return serviceapi.ErrAuthorityDenied
		}
		event, err := decisionEvent(receipt, claim, request, payload)
		if err != nil {
			return err
		}
		if err := authority.Revalidate(); err != nil {
			return mapJournalError(err)
		}
		appendAttempted = true
		return writer.AppendOrVerifyLeased(event, lease)
	})
	if err == nil {
		if err := authority.Revalidate(); err != nil {
			return mapJournalError(err)
		}
		_, outcomeErr := c.journal.AppendOutcome(ctx, receipt.RunID, receipt.OperationID, actioncontrol.StatusApplied, []string{claim.ControllerEventID}, "")
		return mapJournalError(outcomeErr)
	}
	_, markerErr := c.journal.AppendOutcome(ctx, receipt.RunID, receipt.OperationID, actioncontrol.StatusReconciliationRequired, nil, "decision_effect_ambiguous")
	if markerErr != nil {
		return errors.Join(err, mapJournalError(markerErr))
	}
	if !appendAttempted {
		if authorityErr := authority.Revalidate(); authorityErr != nil {
			return errors.Join(err, mapJournalError(authorityErr))
		}
		_, resolvedErr := c.journal.AppendOutcome(ctx, receipt.RunID, receipt.OperationID, actioncontrol.StatusReconciledNotApplied, nil, "decision_precondition_advanced")
		return errors.Join(err, mapJournalError(resolvedErr))
	}
	return errors.Join(err, c.reconcileDecision(ctx, authority))
}

func (c *Controller) reconcileDecision(ctx context.Context, authority *actioncontrol.DecisionEffectLease) error {
	operation, err := authority.Operation(ctx)
	if err != nil {
		return mapJournalError(err)
	}
	if isTerminalStatus(operation.Status) {
		return nil
	}
	if operation.Status == actioncontrol.StatusClaimed {
		if _, err := c.journal.AppendOutcome(ctx, operation.Receipt.RunID, operation.Receipt.OperationID, actioncontrol.StatusReconciliationRequired, nil, "decision_claim_recovery"); err != nil {
			return mapJournalError(err)
		}
		operation.Status = actioncontrol.StatusReconciliationRequired
	}
	if operation.Status != actioncontrol.StatusReconciliationRequired {
		return mapJournalError(actioncontrol.ErrIntegrity)
	}
	snapshot, err := c.model.Snapshot(ctx, operation.Receipt.RunID)
	if err != nil {
		return err
	}
	var observed *ledger.Event
	for index := range snapshot.Events {
		if snapshot.Events[index].EventID == operation.Claim.ControllerEventID {
			if observed != nil {
				return serviceapi.ErrProjectionIntegrity
			}
			value := snapshot.Events[index]
			observed = &value
		}
	}
	status := actioncontrol.StatusReconciledNotApplied
	eventIDs := []string(nil)
	reason := "decision_event_absent"
	if observed != nil {
		if !matchesDecisionEvent(*observed, operation, snapshot) {
			return serviceapi.ErrProjectionIntegrity
		}
		status, eventIDs, reason = actioncontrol.StatusReconciledApplied, []string{observed.EventID}, ""
	}
	if err := authority.Revalidate(); err != nil {
		return mapJournalError(err)
	}
	current, err := authority.Operation(ctx)
	if err != nil {
		return mapJournalError(err)
	}
	if isTerminalStatus(current.Status) {
		return nil
	}
	if current.Status != actioncontrol.StatusReconciliationRequired || current.Claim == nil || current.Claim.ControllerEventID != operation.Claim.ControllerEventID {
		return mapJournalError(actioncontrol.ErrIntegrity)
	}
	_, err = c.journal.AppendOutcome(ctx, operation.Receipt.RunID, operation.Receipt.OperationID, status, eventIDs, reason)
	return mapJournalError(err)
}

func decisionEvent(receipt actioncontrol.ActionReceiptV1, claim actioncontrol.ActionClaimV1, request HumanDecisionRequestV1, payload DecisionPayloadV1) (ledger.Event, error) {
	timestamp, err := time.Parse(time.RFC3339Nano, claim.ControllerEventTimestamp)
	if err != nil {
		return ledger.Event{}, err
	}
	event := ledger.Event{SchemaVersion: ledger.CurrentSchemaVersion, EventID: claim.ControllerEventID, Timestamp: timestamp,
		ProjectID: request.OriginatingEvent.ProjectID, PlanID: request.OriginatingEvent.PlanID, RunID: receipt.RunID, AttemptID: receipt.AttemptID,
		EventType: "API_HUMAN_DECISION_RECORDED", Actor: "control-plane", Source: "service-action-controller",
		Payload: map[string]any{
			"record_schema_version": 1, "decision_request_id": payload.DecisionRequestID, "answer": payload.Answer,
			"required_authority": request.RequiredAuthority, "principal_id": receipt.PrincipalID, "principal_type": receipt.PrincipalType,
			"delegated_actor_id": receipt.DelegatedActor.SubjectID, "delegated_actor_type": receipt.DelegatedActor.SubjectType,
			"policy_version": receipt.PolicyVersion, "originating_policy_version": request.PolicyVersion,
			"authority_grant_file_sha256": receipt.AuthorityGrantSHA256, "request_sha256": receipt.RequestSHA256, "operation_id": receipt.OperationID,
		},
	}
	return event, event.Validate()
}

func matchesDecisionEvent(event ledger.Event, operation actioncontrol.Operation, snapshot readmodel.Snapshot) bool {
	claim, receipt := operation.Claim, operation.Receipt
	if claim == nil {
		return false
	}
	payload, _, err := DecodeDecisionPayload(receipt.Payload)
	if err != nil {
		return false
	}
	request, err := decodeOrigin(snapshot.Events, receipt.RunID, receipt.AttemptID, payload.DecisionRequestID)
	if err != nil || request.TransitionEventID != receipt.AdmittedStateTransitionID {
		return false
	}
	expected, err := decisionEvent(receipt, *claim, request, payload)
	if err != nil {
		return false
	}
	observedJSON, observedErr := json.Marshal(event)
	expectedJSON, expectedErr := json.Marshal(expected)
	return observedErr == nil && expectedErr == nil && bytes.Equal(observedJSON, expectedJSON)
}

func semanticDigest(principal serviceapi.Principal, runID string, kind serviceapi.ActionKind, command serviceapi.CommandEnvelopeV1, payload json.RawMessage) string {
	value := struct {
		SchemaVersion    int                          `json:"schema_version"`
		PrincipalID      string                       `json:"principal_id"`
		PrincipalType    serviceapi.PrincipalType     `json:"principal_type"`
		RunID            string                       `json:"run_id"`
		Action           serviceapi.ActionKind        `json:"action"`
		AttemptID        string                       `json:"attempt_id"`
		ExpectedState    string                       `json:"expected_state"`
		ExpectedRevision string                       `json:"expected_revision"`
		Reason           string                       `json:"reason"`
		DelegatedActor   *serviceapi.DelegatedActorV1 `json:"delegated_actor,omitempty"`
		Payload          json.RawMessage              `json:"payload"`
	}{1, principal.PrincipalID, principal.PrincipalType, runID, kind, command.AttemptID, command.ExpectedState, command.ExpectedRevision, command.Reason, command.DelegatedActor, payload}
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func receiptPrincipal(receipt actioncontrol.ActionReceiptV1) serviceapi.Principal {
	return serviceapi.Principal{PrincipalID: receipt.PrincipalID, PrincipalType: serviceapi.PrincipalType(receipt.PrincipalType), AuthnMethod: "journal-replay-v1"}
}

func receiptCommand(receipt actioncontrol.ActionReceiptV1) serviceapi.CommandEnvelopeV1 {
	command := serviceapi.CommandEnvelopeV1{SchemaVersion: 1, RequestID: receipt.RequestID, AttemptID: receipt.AttemptID,
		ExpectedState: receipt.ExpectedState, ExpectedRevision: receipt.ExpectedRevision, Reason: receipt.Reason, Payload: append(json.RawMessage(nil), receipt.Payload...)}
	if receipt.DelegatedActor != nil {
		command.DelegatedActor = &serviceapi.DelegatedActorV1{SubjectID: receipt.DelegatedActor.SubjectID, SubjectType: serviceapi.PrincipalType(receipt.DelegatedActor.SubjectType)}
	}
	return command
}

func statusFor(operation actioncontrol.Operation) serviceapi.ActionStatusV1 {
	return serviceapi.ActionStatusV1{OperationID: operation.Receipt.OperationID, Status: string(operation.Status),
		StatusURL:             "/v1/runs/" + operation.Receipt.RunID + "/actions/" + operation.Receipt.OperationID,
		AuthoritativeEventIDs: operation.AuthoritativeEventIDs()}
}

func isTerminalStatus(status actioncontrol.OutcomeStatus) bool {
	return status == actioncontrol.StatusRejected || status == actioncontrol.StatusApplied || status == actioncontrol.StatusReconciledApplied || status == actioncontrol.StatusReconciledNotApplied
}

func mapJournalError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, actioncontrol.ErrExhausted):
		return serviceapi.ErrActionJournalExhausted
	case errors.Is(err, actioncontrol.ErrBusy):
		return serviceapi.ErrAuthoritativeReadBusy
	case errors.Is(err, actioncontrol.ErrConflict):
		return serviceapi.ErrRequestIDConflict
	case errors.Is(err, actioncontrol.ErrDecisionExists):
		return serviceapi.ErrDecisionAlreadyRecorded
	case errors.Is(err, actioncontrol.ErrNotFound):
		return serviceapi.ErrActionStatusNotFound
	case errors.Is(err, actioncontrol.ErrIntegrity):
		return serviceapi.ErrInternalDurableSubstrate
	case errors.Is(err, actioncontrol.ErrReceiptPending):
		return serviceapi.ErrInternalDurableSubstrate
	default:
		return err
	}
}

func rejectionCode(err error) string {
	switch {
	case errors.Is(err, serviceapi.ErrStaleExpectedState):
		return "stale_expected_state"
	case errors.Is(err, serviceapi.ErrStaleExpectedRevision):
		return "stale_expected_revision"
	case errors.Is(err, serviceapi.ErrDecisionAlreadyRecorded):
		return "decision_already_recorded"
	case errors.Is(err, serviceapi.ErrDecisionAlreadyResolved):
		return "decision_already_resolved"
	case errors.Is(err, serviceapi.ErrAuthorityDenied):
		return "authority_denied"
	default:
		return "action_state_advanced"
	}
}

func receiptSnapshotMatches(snapshot readmodel.Snapshot, receipt actioncontrol.ActionReceiptV1) bool {
	latest := latestStateTransition(snapshot.Events)
	return snapshot.Projection.RunID == receipt.RunID && snapshot.Projection.CurrentState == receipt.ExpectedState && latest != nil &&
		latest.EventID == receipt.AdmittedStateTransitionID && latest.AttemptID == receipt.AttemptID
}

var _ serviceapi.ActionController = (*Controller)(nil)

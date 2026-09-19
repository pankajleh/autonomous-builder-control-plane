package actioncontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type watcherReadModel interface {
	Snapshot(context.Context, string) (readmodel.Snapshot, error)
	WithExistingWritableLedgerAndSnapshot(context.Context, string, func(*ledger.JSONLLedger, func() (readmodel.Snapshot, error)) error) error
}

type CancelWatcherConfig struct {
	Journal     *Journal
	Catalog     *runtimecatalog.Catalog
	ReadModel   watcherReadModel
	Owner       runtimecatalog.ActiveOwnerLeaseV1
	CancelCause context.CancelCauseFunc
	PollEvery   time.Duration
}

// CancelWatcher is the single bounded mailbox consumer for one exact owner
// generation. It never signals a process and never transfers work to a later
// generation.
type CancelWatcher struct {
	journal     *Journal
	catalog     *runtimecatalog.Catalog
	model       watcherReadModel
	owner       runtimecatalog.ActiveOwnerLeaseV1
	cancelCause context.CancelCauseFunc
	pollEvery   time.Duration
	ctx         context.Context
	cancel      context.CancelFunc
	closing     chan uint64
	done        chan struct{}
	start       sync.Once
	delivered   map[string]string
	closeMu     sync.Mutex
	beginning   bool
	beginWait   chan struct{}
	begun       bool
	watermark   uint64
	retiring    bool
	retireWait  chan struct{}
	retired     bool
	errMu       sync.Mutex
	err         error
}

func NewCancelWatcher(config CancelWatcherConfig) (*CancelWatcher, error) {
	if config.Journal == nil || config.Catalog == nil || config.ReadModel == nil || config.CancelCause == nil ||
		config.Owner.State != runtimecatalog.OwnerLeaseActive || runtimecatalog.VerifyLiveOwner(config.Owner) != nil {
		return nil, errors.New("exact live owner watcher dependencies are required")
	}
	if config.PollEvery == 0 {
		config.PollEvery = 100 * time.Millisecond
	}
	if config.PollEvery < time.Millisecond || config.PollEvery > MaxWatcherPoll {
		return nil, errors.New("cancel watcher poll interval is outside bounds")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &CancelWatcher{journal: config.Journal, catalog: config.Catalog, model: config.ReadModel, owner: config.Owner,
		cancelCause: config.CancelCause, pollEvery: config.PollEvery, ctx: ctx, cancel: cancel,
		closing: make(chan uint64, 1), done: make(chan struct{}), delivered: map[string]string{}}, nil
}

func (w *CancelWatcher) Start() {
	if w == nil {
		return
	}
	w.start.Do(func() { go w.watch() })
}

// BeginClose freezes the exact action-journal watermark under journal ->
// catalog order and changes only this owner generation to CLOSING. It is
// idempotent and deliberately does not wait for the final drain.
func (w *CancelWatcher) BeginClose(ctx context.Context) error {
	if w == nil || ctx == nil {
		return errors.New("watcher begin-close context is required")
	}
	w.Start()
	for {
		w.closeMu.Lock()
		if w.begun {
			w.closeMu.Unlock()
			return nil
		}
		if w.beginning {
			wait := w.beginWait
			w.closeMu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wait:
				continue
			}
		}
		w.beginning = true
		w.beginWait = make(chan struct{})
		wait := w.beginWait
		w.closeMu.Unlock()

		watermark, err := w.freeze(ctx)

		w.closeMu.Lock()
		w.beginning = false
		if err == nil {
			w.begun = true
			w.watermark = watermark
			select {
			case w.closing <- watermark:
			default:
			}
		}
		close(wait)
		w.closeMu.Unlock()
		if errors.Is(err, runtimecatalog.ErrOwnerNotActive) {
			w.cancel()
		}
		return err
	}
}

func (w *CancelWatcher) freeze(ctx context.Context) (uint64, error) {
	var watermark uint64
	err := w.journal.BindSequence(ctx, w.owner.RunID, func(sequence uint64) error {
		guard, err := w.catalog.AcquireOwnerLeaseGuard(w.owner.RunID)
		if err != nil {
			return err
		}
		current, leaseErr := guard.Lease()
		if leaseErr == nil && !w.sameOwner(current) {
			leaseErr = runtimecatalog.ErrOwnerNotActive
		}
		if leaseErr == nil {
			switch current.State {
			case runtimecatalog.OwnerLeaseActive:
				current, leaseErr = guard.MarkClosing(w.owner.LeaseID, sequence)
				if leaseErr == nil {
					watermark = sequence
				}
			case runtimecatalog.OwnerLeaseClosing:
				if current.DrainThroughJournalSequence == nil {
					leaseErr = runtimecatalog.ErrIntegrity
				} else {
					watermark = *current.DrainThroughJournalSequence
				}
			default:
				leaseErr = runtimecatalog.ErrOwnerNotActive
			}
		}
		closeErr := guard.Close()
		if leaseErr != nil || closeErr != nil {
			return errors.Join(leaseErr, closeErr)
		}
		if current.DrainThroughJournalSequence == nil || *current.DrainThroughJournalSequence != watermark || watermark > sequence {
			return runtimecatalog.ErrIntegrity
		}
		return nil
	})
	return watermark, err
}

// Close completes only the already frozen exact-watermark drain and retires
// only the same owner generation. A timed-out caller does not stop the drain;
// a later Close can finish it.
func (w *CancelWatcher) Close(ctx context.Context) error {
	if w == nil || ctx == nil {
		return errors.New("watcher close context is required")
	}
	if err := w.BeginClose(ctx); err != nil {
		if errors.Is(err, runtimecatalog.ErrOwnerNotActive) {
			select {
			case <-w.done:
			case <-ctx.Done():
			}
		}
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
	}
	if err := w.watchError(); err != nil {
		return err
	}
	return w.retire(ctx)
}

func (w *CancelWatcher) retire(ctx context.Context) error {
	for {
		w.closeMu.Lock()
		if w.retired {
			w.closeMu.Unlock()
			return nil
		}
		if w.retiring {
			wait := w.retireWait
			w.closeMu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wait:
				continue
			}
		}
		w.retiring = true
		w.retireWait = make(chan struct{})
		wait := w.retireWait
		watermark := w.watermark
		w.closeMu.Unlock()

		retired, err := w.retireExact(watermark)

		w.closeMu.Lock()
		w.retiring = false
		w.retired = retired
		close(wait)
		w.closeMu.Unlock()
		return err
	}
}

func (w *CancelWatcher) retireExact(watermark uint64) (bool, error) {
	guard, err := w.catalog.AcquireOwnerLeaseGuard(w.owner.RunID)
	if err != nil {
		return false, err
	}
	current, leaseErr := guard.Lease()
	if leaseErr == nil && !w.sameOwner(current) {
		leaseErr = runtimecatalog.ErrOwnerNotActive
	}
	retired := false
	if leaseErr == nil {
		switch current.State {
		case runtimecatalog.OwnerLeaseClosing:
			if current.DrainThroughJournalSequence == nil || *current.DrainThroughJournalSequence != watermark {
				leaseErr = runtimecatalog.ErrIntegrity
			} else {
				_, leaseErr = guard.Retire(w.owner.LeaseID)
				retired = leaseErr == nil
			}
		case runtimecatalog.OwnerLeaseRetired:
			if current.DrainThroughJournalSequence == nil || *current.DrainThroughJournalSequence != watermark {
				leaseErr = runtimecatalog.ErrIntegrity
			} else {
				retired = true
			}
		default:
			leaseErr = runtimecatalog.ErrOwnerNotActive
		}
	}
	closeErr := guard.Close()
	return retired && closeErr == nil, errors.Join(leaseErr, closeErr)
}

func (w *CancelWatcher) sameOwner(current runtimecatalog.ActiveOwnerLeaseV1) bool {
	return current.LeaseID == w.owner.LeaseID && current.RunID == w.owner.RunID && current.AttemptID == w.owner.AttemptID &&
		current.LeaseGeneration == w.owner.LeaseGeneration && runtimecatalog.LeaseMatchesProcess(current, w.owner.Process)
}

func (w *CancelWatcher) watch() {
	defer close(w.done)
	ticker := time.NewTicker(w.pollEvery)
	defer ticker.Stop()
	var through *uint64
	for {
		if err := w.poll(through); fatalWatcherError(err) {
			w.setError(err)
			return
		}
		if through != nil {
			drained, err := w.drained(*through)
			if err != nil {
				w.setError(err)
			} else if drained {
				return
			}
		}
		select {
		case <-w.ctx.Done():
			return
		case value := <-w.closing:
			through = new(uint64)
			*through = value
		case <-ticker.C:
		}
	}
}

func (w *CancelWatcher) poll(through *uint64) error {
	operations, err := w.journal.OperationsForLease(w.ctx, w.owner.RunID, w.owner.LeaseID, through)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		switch operation.Status {
		case StatusReceived:
			if err := w.claimAndDeliver(operation); err != nil {
				return err
			}
		case StatusClaimed:
			if _, ownDelivery := w.delivered[operation.Receipt.OperationID]; ownDelivery {
				if err := w.observeDelivered(operation); err != nil {
					return err
				}
			} else if err := w.reconcileLostClaim(operation); err != nil {
				return err
			}
		case StatusReconciliationRequired:
			if err := w.reconcileCancel(operation, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *CancelWatcher) claimAndDeliver(operation Operation) error {
	receipt := operation.Receipt
	snapshot, err := w.model.Snapshot(w.ctx, receipt.RunID)
	if err != nil {
		return err
	}
	if !cancelSnapshotMatches(snapshot, receipt) {
		_, err = w.journal.AppendOutcome(w.ctx, receipt.RunID, receipt.OperationID, StatusRejected, nil, "action_state_advanced")
		return err
	}
	claim, created, err := w.journal.CreateClaim(w.ctx, receipt.RunID, receipt.OperationID, receipt.OwnerLeaseID, CancelEventDomain)
	if err != nil {
		return err
	}
	operation.Claim = &claim
	operation.Status = StatusClaimed
	if !created {
		return w.reconcileLostClaim(operation)
	}
	return w.deliver(operation)
}

func (w *CancelWatcher) deliver(operation Operation) error {
	receipt, claim := operation.Receipt, *operation.Claim
	appendAttempted := false
	err := w.model.WithExistingWritableLedgerAndSnapshot(w.ctx, receipt.RunID, func(writer *ledger.JSONLLedger, authoritativeSnapshot func() (readmodel.Snapshot, error)) (resultErr error) {
		lease, err := writer.AcquireRunTransition(receipt.RunID)
		if err != nil {
			return err
		}
		defer func() { resultErr = errors.Join(resultErr, lease.Close()) }()
		guard, err := w.catalog.AcquireOwnerLeaseGuard(receipt.RunID)
		if err != nil {
			return err
		}
		current, proofErr := guard.Lease()
		closeErr := guard.Close()
		if proofErr != nil || closeErr != nil || !w.deliveryEligible(current, receipt.Sequence) {
			return errors.Join(serviceapi.ErrActionTargetNotActive, proofErr, closeErr)
		}
		snapshot, err := authoritativeSnapshot()
		if err != nil {
			return err
		}
		if !cancelSnapshotMatches(snapshot, receipt) {
			return serviceapi.ErrActionStateAdvanced
		}
		event, err := cancelRequestEvent(receipt, claim, snapshot)
		if err != nil {
			return err
		}
		appendAttempted = true
		if err := writer.AppendOrVerifyLeased(event, lease); err != nil {
			return err
		}
		cause := runctl.NewAPICancelCause(receipt.OperationID, receipt.OwnerLeaseID)
		if _, valid := runctl.APICancelProvenance(contextWithCause(cause)); !valid {
			return errors.New("typed API cancellation cause is invalid")
		}
		w.cancelCause(cause)
		return nil
	})
	if err == nil {
		w.delivered[receipt.OperationID] = claim.ControllerEventID
		return nil
	}
	_, markerErr := w.journal.AppendOutcome(w.ctx, receipt.RunID, receipt.OperationID, StatusReconciliationRequired, nil, "cancel_effect_ambiguous")
	if markerErr != nil {
		return errors.Join(err, markerErr)
	}
	if !appendAttempted {
		_, resolvedErr := w.journal.AppendOutcome(w.ctx, receipt.RunID, receipt.OperationID, StatusReconciledNotApplied, nil, "cancel_delivery_ineligible")
		if resolvedErr == nil {
			return nil
		}
		return errors.Join(err, resolvedErr)
	}
	// The original append result is no longer actionable once the durable
	// marker exists: replay is forbidden and the one bounded reconciliation
	// attempt below is authoritative for watcher liveness. Preserve any
	// reconciliation integrity/authority failure, but do not let the already
	// recorded ambiguous append error terminate this owner generation.
	if reconciliationErr := w.reconcileCancel(operation, true); reconciliationErr != nil {
		return errors.Join(err, reconciliationErr)
	}
	return nil
}

func (w *CancelWatcher) observeDelivered(operation Operation) error {
	snapshot, err := w.model.Snapshot(w.ctx, operation.Receipt.RunID)
	if err != nil {
		return err
	}
	requestID, transitionID, state, proofErr := ReconcileCancelHistory(snapshot, operation)
	if proofErr != nil {
		return proofErr
	}
	if state == CancelApplied {
		_, err := w.journal.AppendOutcome(w.ctx, operation.Receipt.RunID, operation.Receipt.OperationID, StatusApplied, []string{requestID, transitionID}, "")
		delete(w.delivered, operation.Receipt.OperationID)
		return err
	}
	if state == CancelNotApplied {
		if _, err := w.journal.AppendOutcome(w.ctx, operation.Receipt.RunID, operation.Receipt.OperationID, StatusReconciliationRequired, []string{requestID}, "cancel_terminal_without_matching_provenance"); err != nil {
			return err
		}
		_, err := w.journal.AppendOutcome(w.ctx, operation.Receipt.RunID, operation.Receipt.OperationID, StatusReconciledNotApplied, []string{requestID}, "cancel_terminal_without_matching_provenance")
		delete(w.delivered, operation.Receipt.OperationID)
		return err
	}
	return nil
}

func (w *CancelWatcher) reconcileLostClaim(operation Operation) error {
	if operation.Status == StatusClaimed {
		if _, err := w.journal.AppendOutcome(w.ctx, operation.Receipt.RunID, operation.Receipt.OperationID, StatusReconciliationRequired, nil, "cancel_claim_recovery"); err != nil {
			return err
		}
		operation.Status = StatusReconciliationRequired
	}
	return w.reconcileCancel(operation, true)
}

func (w *CancelWatcher) reconcileCancel(operation Operation, recovered bool) error {
	snapshot, err := w.model.Snapshot(w.ctx, operation.Receipt.RunID)
	if err != nil {
		return err
	}
	requestID, transitionID, state, proofErr := ReconcileCancelHistory(snapshot, operation)
	if proofErr != nil {
		return proofErr
	}
	switch state {
	case CancelApplied:
		status := StatusReconciledApplied
		if !recovered {
			status = StatusApplied
		}
		_, err = w.journal.AppendOutcome(w.ctx, operation.Receipt.RunID, operation.Receipt.OperationID, status, []string{requestID, transitionID}, "")
	case CancelNotApplied:
		ids := []string(nil)
		if requestID != "" {
			ids = []string{requestID}
		}
		_, err = w.journal.AppendOutcome(w.ctx, operation.Receipt.RunID, operation.Receipt.OperationID, StatusReconciledNotApplied, ids, "cancel_not_applied")
	case CancelUnresolved:
		return nil
	}
	return err
}

type CancelProofState int

const (
	CancelUnresolved CancelProofState = iota
	CancelApplied
	CancelNotApplied
)

// ReconcileCancelHistory accepts only one byte-semantic deterministic request
// event followed by one transition carrying the matching API provenance. A
// duplicate, conflicting, or reordered candidate is an integrity failure, not
// evidence that cancellation was applied.
func ReconcileCancelHistory(snapshot readmodel.Snapshot, operation Operation) (string, string, CancelProofState, error) {
	if operation.Claim == nil {
		return "", "", CancelNotApplied, nil
	}
	expectedRequest, err := cancelRequestEvent(operation.Receipt, *operation.Claim, snapshot)
	if err != nil {
		return "", "", CancelUnresolved, serviceapi.ErrProjectionIntegrity
	}
	requestID, transitionID := "", ""
	requestIndex, transitionIndex := -1, -1
	for index, event := range snapshot.Events {
		if event.EventID == operation.Claim.ControllerEventID {
			if requestID != "" || !exactEvent(event, expectedRequest) {
				return "", "", CancelUnresolved, serviceapi.ErrProjectionIntegrity
			}
			requestID, requestIndex = event.EventID, index
		}
		if referencesCancelOperation(event, operation) {
			if transitionID != "" || !matchesCancelledTransition(event, expectedRequest, operation) {
				return "", "", CancelUnresolved, serviceapi.ErrProjectionIntegrity
			}
			transitionID, transitionIndex = event.EventID, index
		}
	}
	admittedIndex := -1
	for index := range snapshot.Events {
		if snapshot.Events[index].EventID == operation.Receipt.AdmittedStateTransitionID {
			admittedIndex = index
			break
		}
	}
	if requestID != "" {
		if admittedIndex < 0 || admittedIndex >= requestIndex {
			return "", "", CancelUnresolved, serviceapi.ErrProjectionIntegrity
		}
		for index := admittedIndex + 1; index < requestIndex; index++ {
			if snapshot.Events[index].StateFrom != "" {
				return "", "", CancelUnresolved, serviceapi.ErrProjectionIntegrity
			}
		}
	}
	if transitionID != "" {
		if requestID == "" || requestIndex >= transitionIndex {
			return "", "", CancelUnresolved, serviceapi.ErrProjectionIntegrity
		}
		for index := requestIndex + 1; index < transitionIndex; index++ {
			if snapshot.Events[index].StateFrom != "" {
				return "", "", CancelUnresolved, serviceapi.ErrProjectionIntegrity
			}
		}
		return requestID, transitionID, CancelApplied, nil
	}
	if requestID == "" {
		return "", "", CancelNotApplied, nil
	}
	if snapshot.Projection.TerminalStatus.Terminal {
		return requestID, "", CancelNotApplied, nil
	}
	return requestID, "", CancelUnresolved, nil
}

func cancelSnapshotMatches(snapshot readmodel.Snapshot, receipt ActionReceiptV1) bool {
	if snapshot.Projection.RunID != receipt.RunID || snapshot.Projection.CurrentState != receipt.ExpectedState {
		return false
	}
	latest := latestTransition(snapshot.Events)
	return latest != nil && latest.EventID == receipt.AdmittedStateTransitionID && latest.AttemptID == receipt.AttemptID
}

func latestTransition(events []ledger.Event) *ledger.Event {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].StateFrom != "" && events[index].StateTo != "" {
			value := events[index]
			return &value
		}
	}
	if len(events) > 0 && events[0].EventType == string(domain.StateRunCreated) {
		value := events[0]
		return &value
	}
	return nil
}

func cancelRequestEvent(receipt ActionReceiptV1, claim ActionClaimV1, snapshot readmodel.Snapshot) (ledger.Event, error) {
	timestamp, err := time.Parse(time.RFC3339Nano, claim.ControllerEventTimestamp)
	if err != nil {
		return ledger.Event{}, err
	}
	admitted, err := exactAdmittedTransition(snapshot.Events, receipt)
	if err != nil {
		return ledger.Event{}, err
	}
	event := ledger.Event{SchemaVersion: ledger.CurrentSchemaVersion, EventID: claim.ControllerEventID, Timestamp: timestamp,
		ProjectID: admitted.ProjectID, PlanID: admitted.PlanID, RunID: receipt.RunID, AttemptID: receipt.AttemptID,
		EventType: "API_CANCEL_REQUESTED", Actor: "control-plane", Source: "service-action-watcher",
		Payload: map[string]any{
			"record_schema_version": 1, "action": receipt.Action, "operation_id": receipt.OperationID,
			"principal_id": receipt.PrincipalID, "principal_type": receipt.PrincipalType, "request_id": receipt.RequestID,
			"request_sha256": receipt.RequestSHA256, "expected_state": receipt.ExpectedState,
			"expected_revision": receipt.ExpectedRevision, "reason": receipt.Reason,
			"admitted_state_transition_event_id": receipt.AdmittedStateTransitionID, "owner_lease_id": receipt.OwnerLeaseID,
		},
	}
	if receipt.DelegatedActor != nil {
		event.Payload["delegated_actor_id"] = receipt.DelegatedActor.SubjectID
		event.Payload["delegated_actor_type"] = receipt.DelegatedActor.SubjectType
	}
	return event, event.Validate()
}

func exactAdmittedTransition(events []ledger.Event, receipt ActionReceiptV1) (ledger.Event, error) {
	var admitted *ledger.Event
	for index := range events {
		if events[index].EventID != receipt.AdmittedStateTransitionID {
			continue
		}
		if admitted != nil || events[index].RunID != receipt.RunID || events[index].AttemptID != receipt.AttemptID ||
			(events[index].StateFrom == "" && (events[index].EventType != string(domain.StateRunCreated) || receipt.ExpectedState != string(domain.StateRunCreated) ||
				payloadValue(events[index].Payload, "state") != string(domain.StateRunCreated))) ||
			(events[index].StateFrom != "" && events[index].StateTo != domain.State(receipt.ExpectedState)) {
			return ledger.Event{}, serviceapi.ErrProjectionIntegrity
		}
		value := events[index]
		admitted = &value
	}
	if admitted == nil {
		return ledger.Event{}, serviceapi.ErrProjectionIntegrity
	}
	return *admitted, nil
}

func exactEvent(observed, expected ledger.Event) bool {
	observedJSON, observedErr := json.Marshal(observed)
	expectedJSON, expectedErr := json.Marshal(expected)
	return observedErr == nil && expectedErr == nil && bytes.Equal(observedJSON, expectedJSON)
}

func referencesCancelOperation(event ledger.Event, operation Operation) bool {
	if operation.Claim == nil || event.EventID == operation.Claim.ControllerEventID {
		return false
	}
	return payloadValue(event.Payload, "operation_id") == operation.Receipt.OperationID ||
		payloadValue(event.Payload, "request_event_id") == operation.Claim.ControllerEventID
}

func matchesCancelledTransition(event, request ledger.Event, operation Operation) bool {
	receipt, claim := operation.Receipt, operation.Claim
	if claim == nil || event.SchemaVersion != ledger.CurrentSchemaVersion || event.EventID == "" || event.Timestamp.IsZero() ||
		event.ProjectID != request.ProjectID || event.PlanID != request.PlanID || event.RunID != receipt.RunID || event.AttemptID != receipt.AttemptID ||
		event.TaskID != "" || event.AgentSessionID != "" || event.CorrelationID != "" || event.EventType != "STATE_TRANSITION" ||
		event.StateFrom != domain.State(receipt.ExpectedState) || event.StateTo != domain.StateCancelled || event.Actor != "control-plane" ||
		(event.Source != "authority-validator" && event.Source != "governed-runner" && event.Source != "ralphex-adapter" &&
			event.Source != "governance-controller" && event.Source != "acceptance-controller") {
		return false
	}
	return payloadValue(event.Payload, "operation_id") == receipt.OperationID && payloadValue(event.Payload, "owner_lease_id") == receipt.OwnerLeaseID &&
		payloadValue(event.Payload, "request_event_id") == claim.ControllerEventID
}

func payloadValue(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func (w *CancelWatcher) deliveryEligible(current runtimecatalog.ActiveOwnerLeaseV1, receiptSequence uint64) bool {
	if current.LeaseID != w.owner.LeaseID || current.RunID != w.owner.RunID || current.AttemptID != w.owner.AttemptID ||
		!runtimecatalog.LeaseMatchesProcess(current, w.owner.Process) || runtimecatalog.VerifyLiveOwner(current) != nil {
		return false
	}
	if current.State == runtimecatalog.OwnerLeaseActive {
		return true
	}
	return current.State == runtimecatalog.OwnerLeaseClosing && current.DrainThroughJournalSequence != nil && receiptSequence <= *current.DrainThroughJournalSequence
}

func (w *CancelWatcher) drained(through uint64) (bool, error) {
	operations, err := w.journal.OperationsForLease(w.ctx, w.owner.RunID, w.owner.LeaseID, &through)
	if err != nil {
		return false, err
	}
	for _, operation := range operations {
		// RECONCILIATION_REQUIRED is the durable boundary after the watcher has
		// made its one bounded delivery/reconciliation attempt. It remains
		// visible for later read-only reconciliation, but must not keep this
		// exact owner generation alive. RECEIVED and CLAIMED still require the
		// live watcher and therefore continue to block retirement.
		if operation.Status == StatusReceived || operation.Status == StatusClaimed {
			return false, nil
		}
	}
	return true, nil
}

func (w *CancelWatcher) setError(err error) {
	w.errMu.Lock()
	w.err = errors.Join(w.err, err)
	w.errMu.Unlock()
}

func (w *CancelWatcher) watchError() error {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	return w.err
}

func contextWithCause(cause error) context.Context {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	return ctx
}

func fatalWatcherError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrBusy) ||
		errors.Is(err, runtimecatalog.ErrBusy) || errors.Is(err, serviceapi.ErrAuthoritativeReadBusy) {
		return false
	}
	return true
}

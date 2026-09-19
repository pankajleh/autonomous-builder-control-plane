//go:build linux

package actionapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/actioncontrol"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/blocker"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/recovery"
	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

var actionTestTime = time.Date(2026, 9, 14, 7, 0, 0, 0, time.UTC)

type actionFixture struct {
	root       string
	runID      string
	attemptID  string
	catalog    *runtimecatalog.Catalog
	journal    *actioncontrol.Journal
	model      *actioncontrol.GuardedReadModel
	controller *Controller
	ledger     *ledger.JSONLLedger
	blockerID  string
	matcher    *serviceapi.AuthorityMatcher
}

type blockingDecisionProjection struct {
	projectionService
	entered chan struct{}
	release chan struct{}
}

func (m *blockingDecisionProjection) WithExistingWritableLedgerAndSnapshot(ctx context.Context, runID string, operation func(*ledger.JSONLLedger, func() (readmodel.Snapshot, error)) error) error {
	return m.projectionService.WithExistingWritableLedgerAndSnapshot(ctx, runID, func(writer *ledger.JSONLLedger, snapshot func() (readmodel.Snapshot, error)) error {
		m.entered <- struct{}{}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.release:
		}
		return operation(writer, snapshot)
	})
}

type decisionSnapshotBarrier struct {
	projectionService
	reached chan struct{}
	release chan struct{}
}

func (m *decisionSnapshotBarrier) Snapshot(ctx context.Context, runID string) (readmodel.Snapshot, error) {
	m.reached <- struct{}{}
	select {
	case <-ctx.Done():
		return readmodel.Snapshot{}, ctx.Err()
	case <-m.release:
	}
	return m.projectionService.Snapshot(ctx, runID)
}

func TestDecisionActionIsIdempotentAuthorizedAndNonTransitioning(t *testing.T) {
	fixture := newActionFixture(t, true, "decision.authority")
	defer fixture.close()
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := ledger.NewTransitionBarrier(fixture.runID, fixture.attemptID, digestForTest("pending-transition"), actionTestTime)
	if err != nil || fixture.ledger.InstallTransitionBarrier(barrier) != nil {
		t.Fatalf("install transition barrier: %v", err)
	}
	principal := serviceapi.Principal{PrincipalID: "repo-c-service", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test"}
	command := serviceapi.CommandEnvelopeV1{SchemaVersion: 1, RequestID: "decision-request-1", AttemptID: fixture.attemptID,
		ExpectedState: string(domain.StateHumanDecisionRequired), ExpectedRevision: snapshot.Projection.ProjectionRevision, Reason: "record reviewed answer",
		DelegatedActor: &serviceapi.DelegatedActorV1{SubjectID: "human-1", SubjectType: serviceapi.PrincipalUser},
		Payload:        mustJSON(t, DecisionPayloadV1{DecisionRequestID: fixture.blockerID, Answer: "approve"})}
	status, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionDecision, command)
	if err != nil || status.OperationID == "" {
		t.Fatalf("decision admission = %+v, err=%v", status, err)
	}
	status = awaitActionStatus(t, fixture.controller, principal, fixture.runID, status.OperationID, "APPLIED", "RECONCILED_APPLIED")
	if len(status.AuthoritativeEventIDs) != 1 {
		t.Fatalf("decision status = %+v", status)
	}
	after, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Projection.CurrentState != string(domain.StateHumanDecisionRequired) {
		t.Fatalf("decision changed lifecycle state to %s", after.Projection.CurrentState)
	}
	recorded := 0
	for _, event := range after.Events {
		if event.EventType == "API_HUMAN_DECISION_RECORDED" {
			recorded++
			if event.StateFrom != "" || event.StateTo != "" || payloadString(event.Payload, "decision_request_id") != fixture.blockerID ||
				payloadString(event.Payload, "delegated_actor_id") != "human-1" || payloadString(event.Payload, "authority_grant_file_sha256") != fixture.matcher.Digest() {
				t.Fatalf("decision event = %+v", event)
			}
		}
	}
	if recorded != 1 {
		t.Fatalf("decision event count = %d", recorded)
	}
	if active, found, err := fixture.ledger.ActiveTransitionBarrier(fixture.runID); err != nil || !found || active.SHA256 != barrier.SHA256 {
		t.Fatalf("decision consumed transition barrier: found=%v active=%+v err=%v", found, active, err)
	}
	replay, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionDecision, command)
	if err != nil || replay.OperationID != status.OperationID {
		t.Fatalf("idempotent replay = %+v, err=%v", replay, err)
	}
	second := command
	second.RequestID = "decision-request-2"
	second.Payload = mustJSON(t, DecisionPayloadV1{DecisionRequestID: fixture.blockerID, Answer: "deny"})
	if _, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionDecision, second); !errors.Is(err, serviceapi.ErrDecisionAlreadyRecorded) {
		t.Fatalf("second answer error = %v", err)
	}
}

func TestDecisionEffectInProgressCannotBeReconciledNotApplied(t *testing.T) {
	fixture := newActionFixture(t, true, "decision.authority")
	defer fixture.close()
	fixture.controller.Close()
	receipt, _, _ := createDecisionReceipt(t, fixture, "effect-in-progress")

	blockedModel := &blockingDecisionProjection{projectionService: fixture.model, entered: make(chan struct{}, 1), release: make(chan struct{})}
	applying := &Controller{catalog: fixture.catalog, model: blockedModel, journal: fixture.journal, authority: fixture.matcher}
	peerJournal, err := actioncontrol.Open(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	defer peerJournal.Close()
	recovering := &Controller{catalog: fixture.catalog, model: fixture.model, journal: peerJournal, authority: fixture.matcher}

	applyResult := make(chan error, 1)
	go func() {
		applyResult <- applying.ProcessOperation(context.Background(), fixture.runID, receipt.OperationID)
	}()
	select {
	case <-blockedModel.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("decision effect did not reach the in-progress gate")
	}
	operation, err := peerJournal.Read(context.Background(), fixture.runID, receipt.OperationID)
	if err != nil || operation.Status != actioncontrol.StatusClaimed {
		t.Fatalf("pre-effect claim = %+v err=%v", operation, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err = recovering.ProcessOperation(ctx, fixture.runID, receipt.OperationID)
	cancel()
	if !errors.Is(err, serviceapi.ErrAuthoritativeReadBusy) {
		t.Fatalf("in-progress recovery contender error = %v", err)
	}
	operation, err = peerJournal.Read(context.Background(), fixture.runID, receipt.OperationID)
	if err != nil || operation.Status != actioncontrol.StatusClaimed {
		t.Fatalf("in-progress effect was prematurely reconciled: operation=%+v err=%v", operation, err)
	}
	close(blockedModel.release)
	if err := <-applyResult; err != nil {
		t.Fatal(err)
	}
	operation, err = peerJournal.Read(context.Background(), fixture.runID, receipt.OperationID)
	if err != nil || operation.Status != actioncontrol.StatusApplied {
		t.Fatalf("serialized effect result = %+v err=%v", operation, err)
	}
	if countDecisionEvents(t, fixture, receipt.OperationID) != 1 {
		t.Fatal("serialized decision effect was not appended exactly once")
	}
}

func TestDecisionClaimCrashRecoveryPreventsDelayedOriginalEffect(t *testing.T) {
	fixture := newActionFixture(t, true, "decision.authority")
	defer fixture.close()
	fixture.controller.Close()
	receipt, _, _, _ := createClaimedDecision(t, fixture, "claim-crash-recovery")
	peerJournal, err := actioncontrol.Open(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	defer peerJournal.Close()
	recovering := &Controller{catalog: fixture.catalog, model: fixture.model, journal: peerJournal, authority: fixture.matcher}
	if err := recovering.ProcessOperation(context.Background(), fixture.runID, receipt.OperationID); err != nil {
		t.Fatal(err)
	}
	operation, err := fixture.journal.Read(context.Background(), fixture.runID, receipt.OperationID)
	if err != nil || operation.Status != actioncontrol.StatusReconciledNotApplied {
		t.Fatalf("crash recovery result = %+v err=%v", operation, err)
	}
	if err := fixture.controller.resolveDecision(context.Background(), fixture.runID, receipt.OperationID, true); err != nil {
		t.Fatal(err)
	}
	if countDecisionEvents(t, fixture, receipt.OperationID) != 0 {
		t.Fatal("delayed original contender appended after not-applied recovery")
	}
}

func TestDecisionConflictingReplayWorkersShareEffectAuthority(t *testing.T) {
	fixture := newActionFixture(t, true, "decision.authority")
	defer fixture.close()
	fixture.controller.Close()
	receipt, _, _ := createDecisionReceipt(t, fixture, "conflicting-replay-workers")
	peerJournal, err := actioncontrol.Open(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	defer peerJournal.Close()
	barrier := &decisionSnapshotBarrier{projectionService: fixture.model, reached: make(chan struct{}, 2), release: make(chan struct{})}
	controllers := []*Controller{
		{catalog: fixture.catalog, model: barrier, journal: fixture.journal, authority: fixture.matcher},
		{catalog: fixture.catalog, model: barrier, journal: peerJournal, authority: fixture.matcher},
	}
	start := make(chan struct{})
	results := make(chan error, len(controllers))
	for _, controller := range controllers {
		go func(controller *Controller) {
			<-start
			results <- controller.ProcessOperation(context.Background(), fixture.runID, receipt.OperationID)
		}(controller)
	}
	close(start)
	for range controllers {
		select {
		case <-barrier.reached:
		case <-time.After(2 * time.Second):
			t.Fatal("conflicting replay workers did not reach the pre-claim barrier")
		}
	}
	close(barrier.release)
	for range controllers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	operation, err := fixture.journal.Read(context.Background(), fixture.runID, receipt.OperationID)
	if err != nil || !isTerminalStatus(operation.Status) {
		t.Fatalf("conflicting replay result = %+v err=%v", operation, err)
	}
	events := countDecisionEvents(t, fixture, receipt.OperationID)
	if events > 1 || (operation.Status == actioncontrol.StatusApplied && events != 1) || (operation.Status == actioncontrol.StatusReconciledNotApplied && events != 0) {
		t.Fatalf("conflicting replay status=%s events=%d", operation.Status, events)
	}
}

func TestDecisionDecoderAndAuthorityFailClosedBeforeReceipt(t *testing.T) {
	fixture := newActionFixture(t, true, "different.authority")
	defer fixture.close()
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeHumanDecision(snapshot, fixture.runID, "wrong-attempt", fixture.blockerID); !errors.Is(err, serviceapi.ErrDecisionRequestInvalid) {
		t.Fatalf("wrong attempt error = %v", err)
	}
	if _, err := DecodeHumanDecision(snapshot, fixture.runID, fixture.attemptID, "wrong-event"); !errors.Is(err, serviceapi.ErrDecisionRequestInvalid) {
		t.Fatalf("wrong origin error = %v", err)
	}
	principal := serviceapi.Principal{PrincipalID: "repo-c-service", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test"}
	command := serviceapi.CommandEnvelopeV1{SchemaVersion: 1, RequestID: "unauthorized-decision", AttemptID: fixture.attemptID,
		ExpectedState: string(domain.StateHumanDecisionRequired), ExpectedRevision: snapshot.Projection.ProjectionRevision,
		DelegatedActor: &serviceapi.DelegatedActorV1{SubjectID: "human-1", SubjectType: serviceapi.PrincipalOperator},
		Payload:        mustJSON(t, DecisionPayloadV1{DecisionRequestID: fixture.blockerID, Answer: "approve"})}
	if _, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionDecision, command); !errors.Is(err, serviceapi.ErrAuthorityDenied) {
		t.Fatalf("wrong authority error = %v", err)
	}
	if _, err := fixture.journal.ReadByRequest(context.Background(), fixture.runID, principal.PrincipalID, command.RequestID); !errors.Is(err, actioncontrol.ErrNotFound) {
		t.Fatalf("unauthorized decision created receipt: %v", err)
	}
	command.RequestID = "wrong-answer"
	command.Payload = mustJSON(t, DecisionPayloadV1{DecisionRequestID: fixture.blockerID, Answer: "maybe"})
	if _, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionDecision, command); !errors.Is(err, serviceapi.ErrDecisionRequestInvalid) {
		t.Fatalf("wrong answer error = %v", err)
	}
	forbiddenPayloads := []json.RawMessage{
		json.RawMessage(`{"decision_request_id":"` + fixture.blockerID + `","answer":"approve","path":"/tmp/x"}`),
		json.RawMessage(`{"decision_request_id":"` + fixture.blockerID + `","answer":"approve","answer":"deny"}`),
		json.RawMessage(`null`),
	}
	for _, payload := range forbiddenPayloads {
		if _, _, err := DecodeDecisionPayload(payload); !errors.Is(err, serviceapi.ErrDecisionRequestInvalid) {
			t.Fatalf("forbidden decision payload %s error = %v", payload, err)
		}
	}
	for _, payload := range []json.RawMessage{
		json.RawMessage(`{"pid":123}`),
		json.RawMessage(`null`),
		json.RawMessage(`[]`),
		json.RawMessage(`{} {}`),
	} {
		if _, err := DecodeCancelPayload(payload); !errors.Is(err, ErrCancelRequestInvalid) || errors.Is(err, serviceapi.ErrDecisionRequestInvalid) {
			t.Fatalf("malformed cancel payload %s error = %v", payload, err)
		}
	}
}

func TestCancelWatcherWinsTransitionWithMatchingCancelledProvenance(t *testing.T) {
	fixture := newActionFixture(t, false, "decision.authority")
	defer fixture.close()
	process, err := recovery.CaptureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := fixture.catalog.InstallOwnerLease(fixture.runID, fixture.attemptID, process)
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancelCause := context.WithCancelCause(context.Background())
	watcher, err := actioncontrol.NewCancelWatcher(actioncontrol.CancelWatcherConfig{Journal: fixture.journal, Catalog: fixture.catalog,
		ReadModel: fixture.model, Owner: owner, CancelCause: cancelCause, PollEvery: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	principal := serviceapi.Principal{PrincipalID: "repo-c-service", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test"}
	command := serviceapi.CommandEnvelopeV1{SchemaVersion: 1, RequestID: "cancel-request-1", AttemptID: fixture.attemptID,
		ExpectedState: string(domain.StateImplementing), ExpectedRevision: snapshot.Projection.ProjectionRevision, Reason: "operator cancellation", Payload: json.RawMessage(`{}`)}
	status, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, command)
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := ledger.NewEvent(fixture.runID, "UNRELATED_OBSERVATION", "control-plane", "test")
	if err != nil {
		t.Fatal(err)
	}
	unrelated.AttemptID, unrelated.ProjectID, unrelated.PlanID = fixture.attemptID, "project-1", "plan-1"
	if err := fixture.ledger.Append(unrelated); err != nil {
		t.Fatal(err)
	}
	watcher.Start()
	select {
	case <-runContext.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not invoke in-process cancellation")
	}
	provenance, ok := runctl.APICancelProvenance(runContext)
	if !ok || provenance.OperationID != status.OperationID || provenance.OwnerLeaseID != owner.LeaseID {
		t.Fatalf("cancel provenance = %+v, ok=%v", provenance, ok)
	}
	requestEventID := actioncontrol.DeterministicEventID(actioncontrol.CancelEventDomain, status.OperationID)
	transition := testTransition(t, fixture.runID, fixture.attemptID, domain.StateImplementing, domain.StateCancelled, actionTestTime.Add(20*time.Second))
	transition.Source = "ralphex-adapter"
	transition.Payload = map[string]any{"operation_id": status.OperationID, "owner_lease_id": owner.LeaseID, "request_event_id": requestEventID}
	if err := fixture.ledger.Append(transition); err != nil {
		t.Fatal(err)
	}
	status = awaitActionStatus(t, fixture.controller, principal, fixture.runID, status.OperationID, "APPLIED")
	if len(status.AuthoritativeEventIDs) != 2 || status.AuthoritativeEventIDs[0] != requestEventID || status.AuthoritativeEventIDs[1] != transition.EventID {
		t.Fatalf("cancel outcome = %+v", status)
	}
	closeContext, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	if err := watcher.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	guard, err := fixture.catalog.AcquireOwnerLeaseGuard(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	retired, err := guard.Lease()
	guard.Close()
	if err != nil || retired.State != runtimecatalog.OwnerLeaseRetired {
		t.Fatalf("retired owner = %+v, err=%v", retired, err)
	}
}

func TestCancelAdmissionRejectsStalePreconditionsAndMissingOwner(t *testing.T) {
	fixture := newActionFixture(t, false, "decision.authority")
	defer fixture.close()
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	principal := serviceapi.Principal{PrincipalID: "repo-c-service", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test"}
	base := serviceapi.CommandEnvelopeV1{SchemaVersion: 1, RequestID: "cancel-no-owner", AttemptID: fixture.attemptID,
		ExpectedState: string(domain.StateImplementing), ExpectedRevision: snapshot.Projection.ProjectionRevision, Payload: json.RawMessage(`{}`)}
	if _, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, base); !errors.Is(err, serviceapi.ErrActionTargetNotActive) {
		t.Fatalf("missing owner error = %v", err)
	}
	process, err := recovery.CaptureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.catalog.InstallOwnerLease(fixture.runID, fixture.attemptID, process); err != nil {
		t.Fatal(err)
	}
	wrongRevision := base
	wrongRevision.RequestID, wrongRevision.ExpectedRevision = "cancel-stale-revision", digestForTest("stale")
	if _, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, wrongRevision); !errors.Is(err, serviceapi.ErrStaleExpectedRevision) {
		t.Fatalf("stale revision error = %v", err)
	}
	wrongState := base
	wrongState.RequestID, wrongState.ExpectedState = "cancel-stale-state", string(domain.StateAuthorityValidated)
	if _, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, wrongState); !errors.Is(err, serviceapi.ErrStaleExpectedState) {
		t.Fatalf("stale state error = %v", err)
	}
	wrongAttempt := base
	wrongAttempt.RequestID, wrongAttempt.AttemptID = "cancel-stale-attempt", "attempt-2"
	if _, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, wrongAttempt); !errors.Is(err, serviceapi.ErrActionStateAdvanced) {
		t.Fatalf("stale attempt error = %v", err)
	}
}

func TestCancelAcceptsDelegatedActorAsDurableRequestSemantics(t *testing.T) {
	fixture := newActionFixture(t, false, "decision.authority")
	defer fixture.close()
	process, err := recovery.CaptureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.catalog.InstallOwnerLease(fixture.runID, fixture.attemptID, process); err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	principal := serviceapi.Principal{PrincipalID: "repo-c-service", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test"}
	command := serviceapi.CommandEnvelopeV1{SchemaVersion: 1, RequestID: "delegated-cancel", AttemptID: fixture.attemptID,
		ExpectedState: string(domain.StateImplementing), ExpectedRevision: snapshot.Projection.ProjectionRevision, Reason: "requested by operator",
		DelegatedActor: &serviceapi.DelegatedActorV1{SubjectID: "human-1", SubjectType: serviceapi.PrincipalUser}, Payload: json.RawMessage(`{}`)}
	status, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, command)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, command)
	if err != nil || replay.OperationID != status.OperationID {
		t.Fatalf("delegated cancel replay = %+v, err=%v", replay, err)
	}
	operation, err := fixture.journal.Read(context.Background(), fixture.runID, status.OperationID)
	if err != nil || operation.Receipt.DelegatedActor == nil || operation.Receipt.DelegatedActor.SubjectID != "human-1" ||
		operation.Receipt.DelegatedActor.SubjectType != string(serviceapi.PrincipalUser) {
		t.Fatalf("delegated cancel receipt = %+v, err=%v", operation.Receipt, err)
	}
	conflict := command
	conflict.DelegatedActor = &serviceapi.DelegatedActorV1{SubjectID: "human-2", SubjectType: serviceapi.PrincipalUser}
	if _, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, conflict); !errors.Is(err, serviceapi.ErrRequestIDConflict) {
		t.Fatalf("changed delegated cancel actor error = %v", err)
	}
}

func TestStaleAdmissionDoesNotCreateJournalOrRequestIndexState(t *testing.T) {
	fixture := newActionFixture(t, false, "decision.authority")
	defer fixture.close()
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	principal := serviceapi.Principal{PrincipalID: "repo-c-service", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test"}
	command := serviceapi.CommandEnvelopeV1{SchemaVersion: 1, RequestID: "stale-no-mutation", AttemptID: fixture.attemptID,
		ExpectedState: string(domain.StateImplementing), ExpectedRevision: digestForTest(snapshot.Projection.ProjectionRevision + "-stale"), Payload: json.RawMessage(`{}`)}
	if _, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, command); !errors.Is(err, serviceapi.ErrStaleExpectedRevision) {
		t.Fatalf("stale admission error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "actions", fixture.runID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale admission created run journal state: %v", err)
	}
	lookup := actioncontrol.LookupKey(principal.PrincipalID, command.RequestID)
	if _, err := os.Stat(filepath.Join(fixture.root, "actions", "request-index", lookup[:2])); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale admission created request-index shard: %v", err)
	}
}

func TestCancelWatcherRejectsInterveningStateTransitionWithoutDelivery(t *testing.T) {
	fixture := newActionFixture(t, false, "decision.authority")
	defer fixture.close()
	process, err := recovery.CaptureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := fixture.catalog.InstallOwnerLease(fixture.runID, fixture.attemptID, process)
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancelCause := context.WithCancelCause(context.Background())
	watcher, err := actioncontrol.NewCancelWatcher(actioncontrol.CancelWatcherConfig{Journal: fixture.journal, Catalog: fixture.catalog,
		ReadModel: fixture.model, Owner: owner, CancelCause: cancelCause, PollEvery: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	principal := serviceapi.Principal{PrincipalID: "repo-c-service", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test"}
	command := serviceapi.CommandEnvelopeV1{SchemaVersion: 1, RequestID: "cancel-transition-race", AttemptID: fixture.attemptID,
		ExpectedState: string(domain.StateImplementing), ExpectedRevision: snapshot.Projection.ProjectionRevision, Payload: json.RawMessage(`{}`)}
	status, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, command)
	if err != nil {
		t.Fatal(err)
	}
	advanced := testTransition(t, fixture.runID, fixture.attemptID, domain.StateImplementing, domain.StateFailed, actionTestTime.Add(30*time.Second))
	if err := fixture.ledger.Append(advanced); err != nil {
		t.Fatal(err)
	}
	watcher.Start()
	awaitActionStatus(t, fixture.controller, principal, fixture.runID, status.OperationID, "REJECTED")
	select {
	case <-runContext.Done():
		t.Fatal("stale cancel was delivered")
	default:
	}
	closeContext, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	if err := watcher.Close(closeContext); err != nil {
		t.Fatal(err)
	}
}

func TestCancelWatcherDoesNotTransferClaimToReplacementOwnerGeneration(t *testing.T) {
	fixture := newActionFixture(t, false, "decision.authority")
	defer fixture.close()
	process, err := recovery.CaptureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	original, err := fixture.catalog.InstallOwnerLease(fixture.runID, fixture.attemptID, process)
	if err != nil {
		t.Fatal(err)
	}
	delivered := make(chan error, 1)
	watcher, err := actioncontrol.NewCancelWatcher(actioncontrol.CancelWatcherConfig{Journal: fixture.journal, Catalog: fixture.catalog,
		ReadModel: fixture.model, Owner: original, CancelCause: func(cause error) { delivered <- cause }, PollEvery: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	principal := serviceapi.Principal{PrincipalID: "repo-c-service", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test"}
	command := serviceapi.CommandEnvelopeV1{SchemaVersion: 1, RequestID: "replacement-owner-cancel", AttemptID: fixture.attemptID,
		ExpectedState: string(domain.StateImplementing), ExpectedRevision: snapshot.Projection.ProjectionRevision, Payload: json.RawMessage(`{}`)}
	status, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, command)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := fixture.journal.Read(context.Background(), fixture.runID, status.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := fixture.catalog.AcquireOwnerLeaseGuard(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.MarkClosing(original.LeaseID, operation.Receipt.Sequence); err != nil {
		guard.Close()
		t.Fatal(err)
	}
	if _, err := guard.Retire(original.LeaseID); err != nil {
		guard.Close()
		t.Fatal(err)
	}
	guard.Close()
	replacement, err := fixture.catalog.InstallOwnerLease(fixture.runID, fixture.attemptID, process)
	if err != nil || replacement.LeaseID == original.LeaseID {
		t.Fatalf("replacement owner = %+v, err=%v", replacement, err)
	}
	watcher.Start()
	awaitActionStatus(t, fixture.controller, principal, fixture.runID, status.OperationID, string(actioncontrol.StatusReconciledNotApplied))
	select {
	case cause := <-delivered:
		t.Fatalf("replacement owner received prior generation cancel: %v", cause)
	default:
	}
	closeContext, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err := watcher.Close(closeContext); err == nil {
		t.Fatal("prior-generation watcher closed the replacement generation")
	}
	guard, err = fixture.catalog.AcquireOwnerLeaseGuard(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := guard.Lease()
	guard.Close()
	if err != nil || current.LeaseID != replacement.LeaseID || current.State != runtimecatalog.OwnerLeaseActive {
		t.Fatalf("replacement owner changed = %+v, err=%v", current, err)
	}
}

func TestCancelWatcherClosingWatermarkPerformsExactFinalDrain(t *testing.T) {
	fixture := newActionFixture(t, false, "decision.authority")
	defer fixture.close()
	process, err := recovery.CaptureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := fixture.catalog.InstallOwnerLease(fixture.runID, fixture.attemptID, process)
	if err != nil {
		t.Fatal(err)
	}
	causes := make(chan error, 1)
	watcher, err := actioncontrol.NewCancelWatcher(actioncontrol.CancelWatcherConfig{Journal: fixture.journal, Catalog: fixture.catalog,
		ReadModel: fixture.model, Owner: owner, CancelCause: func(cause error) { causes <- cause }, PollEvery: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	principal := serviceapi.Principal{PrincipalID: "repo-c-service", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test"}
	command := serviceapi.CommandEnvelopeV1{SchemaVersion: 1, RequestID: "closing-final-drain", AttemptID: fixture.attemptID,
		ExpectedState: string(domain.StateImplementing), ExpectedRevision: snapshot.Projection.ProjectionRevision, Payload: json.RawMessage(`{}`)}
	status, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, command)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := fixture.journal.Read(context.Background(), fixture.runID, status.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	closeResult := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		closeResult <- watcher.Close(ctx)
	}()
	var cause error
	select {
	case cause = <-causes:
	case <-time.After(2 * time.Second):
		t.Fatal("closing watcher did not drain admitted cancellation")
	}
	provenance, ok := runctl.APICancelProvenance(contextWithCancelCauseForTest(cause))
	if !ok || provenance.OperationID != status.OperationID {
		t.Fatalf("final-drain provenance = %+v, ok=%v", provenance, ok)
	}
	requestID := actioncontrol.DeterministicEventID(actioncontrol.CancelEventDomain, status.OperationID)
	cancelled := testTransition(t, fixture.runID, fixture.attemptID, domain.StateImplementing, domain.StateCancelled, actionTestTime.Add(50*time.Second))
	cancelled.Source = "ralphex-adapter"
	cancelled.Payload = map[string]any{"operation_id": status.OperationID, "owner_lease_id": owner.LeaseID, "request_event_id": requestID}
	if err := fixture.ledger.Append(cancelled); err != nil {
		t.Fatal(err)
	}
	if err := <-closeResult; err != nil {
		t.Fatal(err)
	}
	status = awaitActionStatus(t, fixture.controller, principal, fixture.runID, status.OperationID, string(actioncontrol.StatusApplied))
	guard, err := fixture.catalog.AcquireOwnerLeaseGuard(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	retired, err := guard.Lease()
	guard.Close()
	if err != nil || retired.State != runtimecatalog.OwnerLeaseRetired || retired.DrainThroughJournalSequence == nil ||
		*retired.DrainThroughJournalSequence < operation.Receipt.Sequence {
		t.Fatalf("closing watermark = %+v, err=%v", retired, err)
	}
}

func TestCancelAdmissionAndBeginCloseSerializeAtWatermark(t *testing.T) {
	for iteration := 0; iteration < 10; iteration++ {
		fixture := newActionFixture(t, false, "decision.authority")
		process, err := recovery.CaptureProcessIdentity(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		owner, err := fixture.catalog.InstallOwnerLease(fixture.runID, fixture.attemptID, process)
		if err != nil {
			t.Fatal(err)
		}
		causes := make(chan error, 1)
		watcher, err := actioncontrol.NewCancelWatcher(actioncontrol.CancelWatcherConfig{Journal: fixture.journal, Catalog: fixture.catalog,
			ReadModel: fixture.model, Owner: owner, CancelCause: func(cause error) { causes <- cause }, PollEvery: time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
		if err != nil {
			t.Fatal(err)
		}
		principal := serviceapi.Principal{PrincipalID: "repo-c-service", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test"}
		command := serviceapi.CommandEnvelopeV1{SchemaVersion: 1, RequestID: fmt.Sprintf("close-race-%d", iteration), AttemptID: fixture.attemptID,
			ExpectedState: string(domain.StateImplementing), ExpectedRevision: snapshot.Projection.ProjectionRevision, Payload: json.RawMessage(`{}`)}
		type admissionResult struct {
			status serviceapi.ActionStatusV1
			err    error
		}
		start := make(chan struct{})
		admission := make(chan admissionResult, 1)
		closed := make(chan error, 1)
		go func() {
			<-start
			status, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, command)
			admission <- admissionResult{status: status, err: err}
		}()
		go func() {
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			closed <- watcher.BeginClose(ctx)
		}()
		close(start)
		result := <-admission
		if result.err == nil {
			select {
			case cause := <-causes:
				provenance, ok := runctl.APICancelProvenance(contextWithCancelCauseForTest(cause))
				if !ok || provenance.OperationID != result.status.OperationID {
					t.Fatalf("race provenance = %+v, ok=%v", provenance, ok)
				}
				cancelled := testTransition(t, fixture.runID, fixture.attemptID, domain.StateImplementing, domain.StateCancelled, actionTestTime.Add(time.Duration(60+iteration)*time.Second))
				cancelled.Source = "ralphex-adapter"
				cancelled.Payload = map[string]any{"operation_id": provenance.OperationID, "owner_lease_id": provenance.OwnerLeaseID,
					"request_event_id": actioncontrol.DeterministicEventID(actioncontrol.CancelEventDomain, provenance.OperationID)}
				if err := fixture.ledger.Append(cancelled); err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("admitted close-race cancellation was outside the final drain")
			}
			awaitActionStatus(t, fixture.controller, principal, fixture.runID, result.status.OperationID, string(actioncontrol.StatusApplied))
		} else if !errors.Is(result.err, serviceapi.ErrActionTargetNotActive) {
			t.Fatalf("close-race admission error = %v", result.err)
		}
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := watcher.Close(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		fixture.close()
	}
}

func TestCancelWatcherBeginCloseIsIdempotentAndRetiresExactWatermark(t *testing.T) {
	fixture := newActionFixture(t, false, "decision.authority")
	defer fixture.close()
	process, err := recovery.CaptureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := fixture.catalog.InstallOwnerLease(fixture.runID, fixture.attemptID, process)
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := actioncontrol.NewCancelWatcher(actioncontrol.CancelWatcherConfig{Journal: fixture.journal, Catalog: fixture.catalog,
		ReadModel: fixture.model, Owner: owner, CancelCause: func(error) { t.Error("empty final drain delivered cancellation") }, PollEvery: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var exact uint64
	if err := fixture.journal.BindSequence(context.Background(), fixture.runID, func(sequence uint64) error {
		exact = sequence
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := watcher.BeginClose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := watcher.BeginClose(context.Background()); err != nil {
		t.Fatalf("idempotent BeginClose failed: %v", err)
	}
	guard, err := fixture.catalog.AcquireOwnerLeaseGuard(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	closing, err := guard.Lease()
	guard.Close()
	if err != nil || closing.State != runtimecatalog.OwnerLeaseClosing || closing.LeaseID != owner.LeaseID ||
		closing.DrainThroughJournalSequence == nil || *closing.DrainThroughJournalSequence != exact {
		t.Fatalf("closing owner = %+v, exact=%d, err=%v", closing, exact, err)
	}
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	principal := serviceapi.Principal{PrincipalID: "repo-c-service", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test"}
	command := serviceapi.CommandEnvelopeV1{SchemaVersion: 1, RequestID: "post-begin-close", AttemptID: fixture.attemptID,
		ExpectedState: string(domain.StateImplementing), ExpectedRevision: snapshot.Projection.ProjectionRevision, Payload: json.RawMessage(`{}`)}
	if _, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, command); !errors.Is(err, serviceapi.ErrActionTargetNotActive) {
		t.Fatalf("post-BeginClose admission error = %v", err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := watcher.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	guard, err = fixture.catalog.AcquireOwnerLeaseGuard(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	retired, err := guard.Lease()
	guard.Close()
	if err != nil || retired.State != runtimecatalog.OwnerLeaseRetired || retired.LeaseID != owner.LeaseID ||
		retired.DrainThroughJournalSequence == nil || *retired.DrainThroughJournalSequence != exact {
		t.Fatalf("retired owner = %+v, exact=%d, err=%v", retired, exact, err)
	}
	replacement, err := fixture.catalog.InstallOwnerLease(fixture.runID, fixture.attemptID, process)
	if err != nil || replacement.LeaseID == owner.LeaseID {
		t.Fatalf("replacement owner = %+v, err=%v", replacement, err)
	}
	if err := watcher.Close(context.Background()); err != nil {
		t.Fatalf("idempotent Close after replacement = %v", err)
	}
	guard, err = fixture.catalog.AcquireOwnerLeaseGuard(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := guard.Lease()
	guard.Close()
	if err != nil || current.LeaseID != replacement.LeaseID || current.State != runtimecatalog.OwnerLeaseActive {
		t.Fatalf("replacement changed by prior Close = %+v, err=%v", current, err)
	}
}

func TestFinalTransitionWinsAndCloseDrainsAfterRunnerReturn(t *testing.T) {
	fixture := newActionFixture(t, false, "decision.authority")
	defer fixture.close()
	process, err := recovery.CaptureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := fixture.catalog.InstallOwnerLease(fixture.runID, fixture.attemptID, process)
	if err != nil {
		t.Fatal(err)
	}
	causes := make(chan error, 1)
	watcher, err := actioncontrol.NewCancelWatcher(actioncontrol.CancelWatcherConfig{Journal: fixture.journal, Catalog: fixture.catalog,
		ReadModel: fixture.model, Owner: owner, CancelCause: func(cause error) { causes <- cause }, PollEvery: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	principal := serviceapi.Principal{PrincipalID: "repo-c-service", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test"}
	command := serviceapi.CommandEnvelopeV1{SchemaVersion: 1, RequestID: "final-transition-wins", AttemptID: fixture.attemptID,
		ExpectedState: string(domain.StateImplementing), ExpectedRevision: snapshot.Projection.ProjectionRevision, Payload: json.RawMessage(`{}`)}
	status, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, command)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := fixture.ledger.AcquireRunTransition(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := watcher.BeginClose(context.Background()); err != nil {
		lease.Close()
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		operation, readErr := fixture.journal.Read(context.Background(), fixture.runID, status.OperationID)
		if readErr != nil {
			lease.Close()
			t.Fatal(readErr)
		}
		if operation.Status == actioncontrol.StatusClaimed {
			break
		}
		if time.Now().After(deadline) {
			lease.Close()
			t.Fatalf("watcher did not claim before final transition: %+v", operation)
		}
		time.Sleep(time.Millisecond)
	}
	expired, expire := context.WithCancel(context.Background())
	expire()
	if err := watcher.Close(expired); !errors.Is(err, context.Canceled) {
		lease.Close()
		t.Fatalf("bounded Close while transition lease held = %v", err)
	}
	completed := testTransition(t, fixture.runID, fixture.attemptID, domain.StateImplementing, domain.StateImplementationCompleted, actionTestTime.Add(70*time.Second))
	completed.Source = "ralphex-adapter"
	if err := fixture.ledger.AppendOrVerifyLeased(completed, lease); err != nil {
		lease.Close()
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := watcher.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case cause := <-causes:
		t.Fatalf("final-transition winner delivered cancel: %v", cause)
	default:
	}
	operation, err := fixture.journal.Read(context.Background(), fixture.runID, status.OperationID)
	if err != nil || (operation.Status != actioncontrol.StatusRejected && operation.Status != actioncontrol.StatusReconciledNotApplied) {
		t.Fatalf("final-transition cancel outcome = %+v, err=%v", operation, err)
	}
	history, _, err := fixture.ledger.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(history, []byte(`"state_to":"CANCELLED"`)) || !bytes.Contains(history, []byte(`"state_to":"IMPLEMENTATION_COMPLETED"`)) {
		t.Fatalf("final-transition history = %s", history)
	}
}

func TestCancelReplayOutsideFrozenWatermarkIsRejected(t *testing.T) {
	fixture := newActionFixture(t, false, "decision.authority")
	defer fixture.close()
	process, err := recovery.CaptureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := fixture.catalog.InstallOwnerLease(fixture.runID, fixture.attemptID, process)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	principal := serviceapi.Principal{PrincipalID: "repo-c-service", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test"}
	command := serviceapi.CommandEnvelopeV1{SchemaVersion: 1, RequestID: "replay-outside-watermark", AttemptID: fixture.attemptID,
		ExpectedState: string(domain.StateImplementing), ExpectedRevision: snapshot.Projection.ProjectionRevision, Payload: json.RawMessage(`{}`)}
	status, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, command)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := fixture.journal.Read(context.Background(), fixture.runID, status.OperationID)
	if err != nil || operation.Receipt.Sequence == 0 {
		t.Fatalf("admitted operation = %+v, err=%v", operation, err)
	}
	guard, err := fixture.catalog.AcquireOwnerLeaseGuard(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.MarkClosing(owner.LeaseID, operation.Receipt.Sequence-1); err != nil {
		guard.Close()
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := fixture.controller.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionCancel, command)
	if err != nil || replay.OperationID != status.OperationID || replay.Status != string(actioncontrol.StatusRejected) {
		t.Fatalf("out-of-watermark replay = %+v, err=%v", replay, err)
	}
}

func TestDecisionClaimReconcilesDeterministicAppendWithoutReplay(t *testing.T) {
	fixture := newActionFixture(t, true, "decision.authority")
	defer fixture.close()
	fixture.controller.Close()
	receipt, claim, request, payload := createClaimedDecision(t, fixture, "reconcile-decision")
	event, err := decisionEvent(receipt, claim, request, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.ledger.Append(event); err != nil {
		t.Fatal(err)
	}
	if err := fixture.controller.ProcessOperation(context.Background(), fixture.runID, receipt.OperationID); err != nil {
		t.Fatal(err)
	}
	operation, err := fixture.journal.Read(context.Background(), fixture.runID, receipt.OperationID)
	if err != nil || operation.Status != actioncontrol.StatusReconciledApplied {
		t.Fatalf("reconciled operation = %+v, err=%v", operation, err)
	}
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, observed := range snapshot.Events {
		if observed.EventID == event.EventID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("deterministic event count = %d", count)
	}
}

func TestDecisionReconciliationRequiresTheCompleteDeterministicEvent(t *testing.T) {
	fixture := newActionFixture(t, true, "decision.authority")
	defer fixture.close()
	fixture.controller.Close()
	receipt, claim, request, payload := createClaimedDecision(t, fixture, "exact-decision-event")
	exact, err := decisionEvent(receipt, claim, request, payload)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	operation := actioncontrol.Operation{Receipt: receipt, Claim: &claim, Status: actioncontrol.StatusClaimed}
	if !matchesDecisionEvent(exact, operation, snapshot) {
		t.Fatal("complete deterministic decision event did not reconcile")
	}
	mutations := map[string]func(*ledger.Event){
		"record schema":        func(e *ledger.Event) { e.Payload["record_schema_version"] = 2 },
		"required authority":   func(e *ledger.Event) { e.Payload["required_authority"] = "other.authority" },
		"principal id":         func(e *ledger.Event) { e.Payload["principal_id"] = "other-service" },
		"principal type":       func(e *ledger.Event) { e.Payload["principal_type"] = "operator" },
		"delegated actor id":   func(e *ledger.Event) { e.Payload["delegated_actor_id"] = "human-2" },
		"delegated actor type": func(e *ledger.Event) { e.Payload["delegated_actor_type"] = "operator" },
		"policy version":       func(e *ledger.Event) { e.Payload["policy_version"] = "other-policy" },
		"origin policy":        func(e *ledger.Event) { e.Payload["originating_policy_version"] = "other-origin" },
		"grant digest":         func(e *ledger.Event) { e.Payload["authority_grant_file_sha256"] = digestForTest("other-grant") },
		"request digest":       func(e *ledger.Event) { e.Payload["request_sha256"] = digestForTest("other-request") },
		"operation id":         func(e *ledger.Event) { e.Payload["operation_id"] = "other-operation" },
		"decision request":     func(e *ledger.Event) { e.Payload["decision_request_id"] = "other-decision" },
		"answer":               func(e *ledger.Event) { e.Payload["answer"] = "deny" },
		"extra payload":        func(e *ledger.Event) { e.Payload["extra"] = "forbidden" },
		"ledger schema":        func(e *ledger.Event) { e.SchemaVersion++ },
		"event id":             func(e *ledger.Event) { e.EventID = "other-event" },
		"timestamp":            func(e *ledger.Event) { e.Timestamp = e.Timestamp.Add(time.Nanosecond) },
		"project":              func(e *ledger.Event) { e.ProjectID = "other-project" },
		"plan":                 func(e *ledger.Event) { e.PlanID = "other-plan" },
		"run":                  func(e *ledger.Event) { e.RunID = "other-run" },
		"attempt":              func(e *ledger.Event) { e.AttemptID = "other-attempt" },
		"task":                 func(e *ledger.Event) { e.TaskID = "unexpected-task" },
		"agent session":        func(e *ledger.Event) { e.AgentSessionID = "unexpected-session" },
		"correlation":          func(e *ledger.Event) { e.CorrelationID = "unexpected-correlation" },
		"event type":           func(e *ledger.Event) { e.EventType = "OTHER" },
		"transition shape": func(e *ledger.Event) {
			e.StateFrom, e.StateTo = domain.StateImplementing, domain.StateHumanDecisionRequired
		},
		"actor":    func(e *ledger.Event) { e.Actor = "other-actor" },
		"source":   func(e *ledger.Event) { e.Source = "other-source" },
		"evidence": func(e *ledger.Event) { e.EvidenceRefs = []ledger.EvidenceRef{{URI: "/unexpected"}} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := cloneLedgerEvent(t, exact)
			mutate(&candidate)
			if matchesDecisionEvent(candidate, operation, snapshot) {
				t.Fatalf("mismatched %s reconciled as applied", name)
			}
		})
	}
}

func TestCancelReconciliationRejectsConflictingDuplicateAndReorderedHistory(t *testing.T) {
	fixture := newActionFixture(t, false, "decision.authority")
	defer fixture.close()
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	latest := latestStateTransition(snapshot.Events)
	receipt, created, err := fixture.journal.CreateReceipt(context.Background(), actioncontrol.ReceiptInput{
		PrincipalID: "repo-c-service", PrincipalType: "service", RequestID: "exact-cancel-history", RequestSHA256: digestForTest("exact-cancel-history"),
		Action: "cancel", RunID: fixture.runID, AttemptID: fixture.attemptID, ExpectedState: string(domain.StateImplementing),
		ExpectedRevision: snapshot.Projection.ProjectionRevision, Reason: "exact proof", Payload: json.RawMessage(`{}`), StateTransitionID: latest.EventID,
	}, func(uint64) (actioncontrol.AdmissionBinding, error) {
		return actioncontrol.AdmissionBinding{OwnerLeaseID: digestForTest("exact-owner")}, nil
	})
	if err != nil || !created {
		t.Fatalf("cancel receipt = %+v, created=%v err=%v", receipt, created, err)
	}
	claim, created, err := fixture.journal.CreateClaim(context.Background(), fixture.runID, receipt.OperationID, receipt.OwnerLeaseID, actioncontrol.CancelEventDomain)
	if err != nil || !created {
		t.Fatalf("cancel claim = %+v, created=%v err=%v", claim, created, err)
	}
	operation := actioncontrol.Operation{Receipt: receipt, Claim: &claim, Status: actioncontrol.StatusClaimed}
	request := expectedCancelRequestEvent(t, receipt, claim)
	cancelled := testTransition(t, fixture.runID, fixture.attemptID, domain.StateImplementing, domain.StateCancelled, actionTestTime.Add(40*time.Second))
	cancelled.Source = "ralphex-adapter"
	cancelled.Payload = map[string]any{"operation_id": receipt.OperationID, "owner_lease_id": receipt.OwnerLeaseID, "request_event_id": claim.ControllerEventID}
	base := append([]ledger.Event(nil), snapshot.Events...)

	exactHistory := snapshot
	exactHistory.Events = append(append([]ledger.Event(nil), base...), request, cancelled)
	requestID, transitionID, state, err := actioncontrol.ReconcileCancelHistory(exactHistory, operation)
	if err != nil || state != actioncontrol.CancelApplied || requestID != request.EventID || transitionID != cancelled.EventID {
		t.Fatalf("exact cancel history proof = %q %q %v err=%v", requestID, transitionID, state, err)
	}
	requestOnly := snapshot
	requestOnly.Events = append(append([]ledger.Event(nil), base...), request)
	if _, _, state, err := actioncontrol.ReconcileCancelHistory(requestOnly, operation); err != nil || state != actioncontrol.CancelUnresolved {
		t.Fatalf("request-only proof state=%v err=%v", state, err)
	}

	conflictingRequest := cloneLedgerEvent(t, request)
	conflictingRequest.Payload["principal_type"] = "operator"
	conflictingTransition := cloneLedgerEvent(t, cancelled)
	conflictingTransition.Source = "other-source"
	cases := map[string][]ledger.Event{
		"reordered":              {cancelled, request},
		"request absent":         {cancelled},
		"conflicting request":    {conflictingRequest, cancelled},
		"duplicate request":      {request, request, cancelled},
		"conflicting transition": {request, conflictingTransition},
		"duplicate transition":   {request, cancelled, cancelled},
	}
	for name, suffix := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := snapshot
			candidate.Events = append(append([]ledger.Event(nil), base...), suffix...)
			if _, _, _, err := actioncontrol.ReconcileCancelHistory(candidate, operation); !errors.Is(err, serviceapi.ErrProjectionIntegrity) {
				t.Fatalf("%s history error = %v", name, err)
			}
		})
	}
}

func TestClaimedDecisionMissingLedgerDoesNotCreateIt(t *testing.T) {
	fixture := newActionFixture(t, true, "decision.authority")
	defer fixture.close()
	fixture.controller.Close()
	receipt, _, _, _ := createClaimedDecision(t, fixture, "missing-ledger-decision")
	ledgerPath := fixture.ledger.Path()
	if err := fixture.ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ledgerPath); err != nil {
		t.Fatal(err)
	}
	if err := fixture.controller.ProcessOperation(context.Background(), fixture.runID, receipt.OperationID); err == nil {
		t.Fatal("missing authoritative ledger was treated as a decision success")
	}
	if _, err := os.Stat(ledgerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("decision observation created missing ledger: %v", err)
	}
}

func TestAbandonedCancelClaimReconcilesWithoutResend(t *testing.T) {
	fixture := newActionFixture(t, false, "decision.authority")
	defer fixture.close()
	process, err := recovery.CaptureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := fixture.catalog.InstallOwnerLease(fixture.runID, fixture.attemptID, process)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	latest := latestStateTransition(snapshot.Events)
	receipt, created, err := fixture.journal.CreateReceipt(context.Background(), actioncontrol.ReceiptInput{
		PrincipalID: "repo-c-service", PrincipalType: "service", RequestID: "abandoned-cancel", RequestSHA256: digestForTest("abandoned-cancel"),
		Action: "cancel", RunID: fixture.runID, AttemptID: fixture.attemptID, ExpectedState: string(domain.StateImplementing),
		ExpectedRevision: snapshot.Projection.ProjectionRevision, Payload: json.RawMessage(`{}`), StateTransitionID: latest.EventID,
	}, func(uint64) (actioncontrol.AdmissionBinding, error) {
		return actioncontrol.AdmissionBinding{OwnerLeaseID: owner.LeaseID}, nil
	})
	if err != nil || !created {
		t.Fatalf("cancel receipt = %+v, created=%v err=%v", receipt, created, err)
	}
	claim, created, err := fixture.journal.CreateClaim(context.Background(), fixture.runID, receipt.OperationID, owner.LeaseID, actioncontrol.CancelEventDomain)
	if err != nil || !created {
		t.Fatalf("cancel claim = %+v, created=%v err=%v", claim, created, err)
	}
	requestEvent := expectedCancelRequestEvent(t, receipt, claim)
	if err := fixture.ledger.Append(requestEvent); err != nil {
		t.Fatal(err)
	}
	cancelled := testTransition(t, fixture.runID, fixture.attemptID, domain.StateImplementing, domain.StateCancelled, actionTestTime.Add(40*time.Second))
	cancelled.Source = "ralphex-adapter"
	cancelled.Payload = map[string]any{"operation_id": receipt.OperationID, "owner_lease_id": owner.LeaseID, "request_event_id": requestEvent.EventID}
	if err := fixture.ledger.Append(cancelled); err != nil {
		t.Fatal(err)
	}
	guard, err := fixture.catalog.AcquireOwnerLeaseGuard(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.MarkClosing(owner.LeaseID, receipt.Sequence); err != nil {
		guard.Close()
		t.Fatal(err)
	}
	if _, err := guard.Retire(owner.LeaseID); err != nil {
		guard.Close()
		t.Fatal(err)
	}
	guard.Close()
	principal := serviceapi.Principal{PrincipalID: receipt.PrincipalID, PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test"}
	status, err := fixture.controller.ReadAction(context.Background(), principal, fixture.runID, receipt.OperationID)
	if err != nil || status.Status != string(actioncontrol.StatusReconciledApplied) || len(status.AuthoritativeEventIDs) != 2 {
		t.Fatalf("abandoned cancel status = %+v, err=%v", status, err)
	}
}

func newActionFixture(t *testing.T, humanDecision bool, grantedAuthority string) *actionFixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "service")
	catalog, err := runtimecatalog.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	runID, attemptID := "action-run", "attempt-1"
	ledgerDirectory := filepath.Join(base, "ledger")
	if err := os.Mkdir(ledgerDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(ledgerDirectory, "events.jsonl")
	events, err := ledger.NewJSONLLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	created, err := ledger.NewEvent(runID, string(domain.StateRunCreated), "control-plane", "test")
	if err != nil {
		t.Fatal(err)
	}
	created.Timestamp, created.ProjectID, created.PlanID, created.AttemptID = actionTestTime, "project-1", "plan-1", attemptID
	created.Payload = map[string]any{"state": string(domain.StateRunCreated)}
	if err := events.Append(created); err != nil {
		t.Fatal(err)
	}
	from := domain.StateRunCreated
	for index, to := range []domain.State{domain.StateAuthorityValidated, domain.StateExecutionStarting, domain.StateImplementing} {
		event := testTransition(t, runID, attemptID, from, to, actionTestTime.Add(time.Duration(index+1)*time.Second))
		if err := events.Append(event); err != nil {
			t.Fatal(err)
		}
		from = to
	}
	blockerID := ""
	if humanDecision {
		decision, err := recovery.NewBlockerDecision(recovery.BlockerDecisionInput{
			Attempt:     recovery.AttemptIdentity{ProjectID: "project-1", PlanID: "plan-1", RunID: runID, AttemptID: attemptID},
			StateFrom:   domain.StateImplementing,
			Failure:     blocker.FailureInput{Phase: blocker.PhaseExecution, Diagnostics: "human decision required"},
			Requirement: recovery.BlockerRequirement{HumanDecision: &recovery.HumanDecisionRequirement{Question: "Proceed?", AcceptedAnswers: []string{"approve", "deny"}, RequiredAuthority: "decision.authority"}},
			Actor:       "control-plane", Timestamp: actionTestTime.Add(4 * time.Second), PolicyVersion: "recovery-v1",
			EvidenceRefs: []ledger.EvidenceRef{{URI: filepath.Join(base, "decision-evidence"), SHA256: digestForTest("evidence"), Kind: "classification"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		blockerEvent, err := decision.Event("recovery-controller")
		if err != nil {
			t.Fatal(err)
		}
		if err := events.Append(blockerEvent); err != nil {
			t.Fatal(err)
		}
		blockerID = blockerEvent.EventID
	}
	authorityDigest := digestForTest("authority")
	evidenceRoot := filepath.Join(base, "evidence")
	if err := os.Mkdir(evidenceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runRegistration, err := runtimecatalog.NewRunRegistrationV1(runID, "example/action", authorityDigest, ledgerPath, evidenceRoot, actionTestTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterRun(runRegistration); err != nil {
		t.Fatal(err)
	}
	attemptRegistration, err := runtimecatalog.NewAttemptRegistrationV1(runID, attemptID, authorityDigest, actionTestTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterAttempt(attemptRegistration); err != nil {
		t.Fatal(err)
	}
	signer, err := serviceapi.NewCursorSigner("action-test-key", []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	localModel, err := readmodel.New(catalog, signer)
	if err != nil {
		t.Fatal(err)
	}
	model, err := actioncontrol.NewGuardedReadModel(root, localModel)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := actioncontrol.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	grantsPath := filepath.Join(base, "grants.json")
	grants := serviceapi.AuthorityGrantFileV1{Principals: []serviceapi.AuthorityGrantV1{{PrincipalID: "repo-c-service", RequiredAuthorities: []string{grantedAuthority}, MayAssertDelegatedActor: true}}}
	if err := os.WriteFile(grantsPath, mustJSON(t, grants), 0o600); err != nil {
		t.Fatal(err)
	}
	matcher, err := serviceapi.LoadAuthorityMatcher(grantsPath)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewController(ControllerConfig{Catalog: catalog, ReadModel: model, Journal: journal, Authority: matcher})
	if err != nil {
		t.Fatal(err)
	}
	return &actionFixture{root: root, runID: runID, attemptID: attemptID, catalog: catalog, journal: journal, model: model, controller: controller, ledger: events, blockerID: blockerID, matcher: matcher}
}

func (f *actionFixture) close() {
	f.controller.Close()
	f.model.Close()
	f.journal.Close()
	f.ledger.Close()
	f.catalog.Close()
}

func testTransition(t *testing.T, runID, attemptID string, from, to domain.State, at time.Time) ledger.Event {
	t.Helper()
	event, err := ledger.NewEvent(runID, "STATE_TRANSITION", "control-plane", "test")
	if err != nil {
		t.Fatal(err)
	}
	event.Timestamp, event.ProjectID, event.PlanID, event.AttemptID = at, "project-1", "plan-1", attemptID
	event.StateFrom, event.StateTo = from, to
	return event
}

func awaitActionStatus(t *testing.T, controller *Controller, principal serviceapi.Principal, runID, operationID string, wanted ...string) serviceapi.ActionStatusV1 {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, err := controller.ReadAction(context.Background(), principal, runID, operationID)
		if err == nil {
			for _, value := range wanted {
				if status.Status == value {
					return status
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, err := controller.ReadAction(context.Background(), principal, runID, operationID)
	t.Fatalf("action did not reach %v: %+v err=%v", wanted, status, err)
	return serviceapi.ActionStatusV1{}
}

func createClaimedDecision(t *testing.T, fixture *actionFixture, requestID string) (actioncontrol.ActionReceiptV1, actioncontrol.ActionClaimV1, HumanDecisionRequestV1, DecisionPayloadV1) {
	t.Helper()
	receipt, request, payload := createDecisionReceipt(t, fixture, requestID)
	claim, created, err := fixture.journal.CreateClaim(context.Background(), fixture.runID, receipt.OperationID, "", actioncontrol.DecisionEventDomain)
	if err != nil || !created {
		t.Fatalf("create decision claim = %+v, created=%v err=%v", claim, created, err)
	}
	return receipt, claim, request, payload
}

func createDecisionReceipt(t *testing.T, fixture *actionFixture, requestID string) (actioncontrol.ActionReceiptV1, HumanDecisionRequestV1, DecisionPayloadV1) {
	t.Helper()
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	payload := DecisionPayloadV1{DecisionRequestID: fixture.blockerID, Answer: "approve"}
	canonical := mustJSON(t, payload)
	receipt, created, err := fixture.journal.CreateReceipt(context.Background(), actioncontrol.ReceiptInput{
		PrincipalID: "repo-c-service", PrincipalType: "service", RequestID: requestID, RequestSHA256: digestForTest(requestID), Action: "decision",
		RunID: fixture.runID, AttemptID: fixture.attemptID, ExpectedState: string(domain.StateHumanDecisionRequired), ExpectedRevision: snapshot.Projection.ProjectionRevision,
		DelegatedActor: &actioncontrol.DelegatedActorV1{SubjectID: "human-1", SubjectType: "user"}, Payload: canonical,
		PolicyVersion: decisionPolicyVersion, AuthorityGrantSHA256: fixture.matcher.Digest(), StateTransitionID: fixture.blockerID,
	}, nil)
	if err != nil || !created {
		t.Fatalf("create decision receipt = %+v, created=%v err=%v", receipt, created, err)
	}
	request, err := DecodeHumanDecision(snapshot, fixture.runID, fixture.attemptID, fixture.blockerID)
	if err != nil {
		t.Fatal(err)
	}
	return receipt, request, payload
}

func countDecisionEvents(t *testing.T, fixture *actionFixture, operationID string) int {
	t.Helper()
	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range snapshot.Events {
		if event.EventType == "API_HUMAN_DECISION_RECORDED" && payloadString(event.Payload, "operation_id") == operationID {
			count++
		}
	}
	return count
}

func expectedCancelRequestEvent(t *testing.T, receipt actioncontrol.ActionReceiptV1, claim actioncontrol.ActionClaimV1) ledger.Event {
	t.Helper()
	claimedAt, err := time.Parse(time.RFC3339Nano, claim.ControllerEventTimestamp)
	if err != nil {
		t.Fatal(err)
	}
	event := ledger.Event{SchemaVersion: ledger.CurrentSchemaVersion, EventID: claim.ControllerEventID, Timestamp: claimedAt,
		ProjectID: "project-1", PlanID: "plan-1", RunID: receipt.RunID, AttemptID: receipt.AttemptID,
		EventType: "API_CANCEL_REQUESTED", Actor: "control-plane", Source: "service-action-watcher",
		Payload: map[string]any{
			"record_schema_version": 1, "action": receipt.Action, "operation_id": receipt.OperationID,
			"principal_id": receipt.PrincipalID, "principal_type": receipt.PrincipalType, "request_id": receipt.RequestID,
			"request_sha256": receipt.RequestSHA256, "expected_state": receipt.ExpectedState,
			"expected_revision": receipt.ExpectedRevision, "reason": receipt.Reason,
			"admitted_state_transition_event_id": receipt.AdmittedStateTransitionID, "owner_lease_id": receipt.OwnerLeaseID,
		}}
	if receipt.DelegatedActor != nil {
		event.Payload["delegated_actor_id"] = receipt.DelegatedActor.SubjectID
		event.Payload["delegated_actor_type"] = receipt.DelegatedActor.SubjectType
	}
	return event
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func cloneLedgerEvent(t *testing.T, event ledger.Event) ledger.Event {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var clone ledger.Event
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func contextWithCancelCauseForTest(cause error) context.Context {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	return ctx
}

func digestForTest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

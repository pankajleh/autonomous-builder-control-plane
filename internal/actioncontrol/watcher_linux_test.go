//go:build linux

package actioncontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/recovery"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type watcherDrainFixture struct {
	runID     string
	attemptID string
	catalog   *runtimecatalog.Catalog
	journal   *Journal
	ledger    *ledger.JSONLLedger
	owner     runtimecatalog.ActiveOwnerLeaseV1
}

type watcherDrainModel struct {
	mu            sync.Mutex
	snapshot      readmodel.Snapshot
	snapshotErr   error
	snapshotCalls int
	writableCalls int
}

type ambiguousAppendWatcherModel struct {
	mu                    sync.Mutex
	journal               *Journal
	receipt               ActionReceiptV1
	writer                *ledger.JSONLLedger
	beforeAppend          readmodel.Snapshot
	corruptReconciliation bool
	snapshotCalls         int
	writableCalls         int
}

func (m *ambiguousAppendWatcherModel) Snapshot(ctx context.Context, _ string) (readmodel.Snapshot, error) {
	m.mu.Lock()
	m.snapshotCalls++
	m.mu.Unlock()
	operation, err := m.journal.Read(ctx, m.receipt.RunID, m.receipt.OperationID)
	if err != nil {
		return readmodel.Snapshot{}, err
	}
	snapshot := m.beforeAppend
	snapshot.Events = append([]ledger.Event(nil), m.beforeAppend.Events...)
	if operation.Claim == nil {
		return snapshot, nil
	}
	request, err := cancelRequestEvent(m.receipt, *operation.Claim, snapshot)
	if err != nil {
		return readmodel.Snapshot{}, err
	}
	if m.corruptReconciliation {
		request.Actor = "tampered-actor"
	}
	snapshot.Events = append(snapshot.Events, request)
	return snapshot, nil
}

func (m *ambiguousAppendWatcherModel) WithExistingWritableLedgerAndSnapshot(_ context.Context, _ string, operation func(*ledger.JSONLLedger, func() (readmodel.Snapshot, error)) error) error {
	m.mu.Lock()
	m.writableCalls++
	calls := m.writableCalls
	m.mu.Unlock()
	if calls != 1 {
		return errors.New("ambiguous cancellation must not be replayed")
	}
	return operation(m.writer, func() (readmodel.Snapshot, error) {
		if err := m.writer.Close(); err != nil {
			return readmodel.Snapshot{}, err
		}
		return m.beforeAppend, nil
	})
}

func (m *ambiguousAppendWatcherModel) calls() (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotCalls, m.writableCalls
}

func (m *watcherDrainModel) Snapshot(context.Context, string) (readmodel.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshotCalls++
	return m.snapshot, m.snapshotErr
}

func (m *watcherDrainModel) WithExistingWritableLedgerAndSnapshot(context.Context, string, func(*ledger.JSONLLedger, func() (readmodel.Snapshot, error)) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writableCalls++
	return errors.New("reconciliation-required operation must not replay its effect")
}

func (m *watcherDrainModel) calls() (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotCalls, m.writableCalls
}

func TestCancelWatcherRemainsLiveAndRetiresAfterDurableReconciliationRequired(t *testing.T) {
	fixture := newWatcherDrainFixture(t)
	receipt := fixture.createReceipt(t, "ambiguous-cancel")
	delivered := make(chan error, 1)
	model := &ambiguousAppendWatcherModel{
		journal: fixture.journal, receipt: receipt, writer: fixture.ledger, beforeAppend: admittedCancelSnapshot(receipt),
	}
	watcher, err := NewCancelWatcher(CancelWatcherConfig{
		Journal: fixture.journal, Catalog: fixture.catalog, ReadModel: model, Owner: fixture.owner,
		CancelCause: func(cause error) { delivered <- cause }, PollEvery: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	watcher.Start()

	operation := waitForWatcherStatus(t, fixture.journal, fixture.runID, receipt.OperationID, StatusReconciliationRequired)
	if operation.Status != StatusReconciliationRequired || len(operation.Outcomes) != 1 ||
		operation.Outcomes[0].ReasonCode != "cancel_effect_ambiguous" {
		t.Fatalf("ambiguous append did not leave durable reconciliation status: operation=%+v", operation)
	}
	waitForWatcherRepoll(t, watcher, model)
	if snapshotCalls, writableCalls := model.calls(); snapshotCalls < 2 || writableCalls != 1 {
		t.Fatalf("ambiguous delivery calls: snapshots=%d writable=%d", snapshotCalls, writableCalls)
	}
	select {
	case cause := <-delivered:
		t.Fatalf("failed append delivered cancellation: %v", cause)
	default:
	}

	if err := watcher.BeginClose(context.Background()); err != nil {
		t.Fatal(err)
	}
	closing := fixture.ownerLease(t)
	if closing.State != runtimecatalog.OwnerLeaseClosing || closing.LeaseID != fixture.owner.LeaseID ||
		closing.DrainThroughJournalSequence == nil || *closing.DrainThroughJournalSequence < receipt.Sequence {
		t.Fatalf("owner did not freeze the admitted cancel in CLOSING: %+v", closing)
	}

	closeContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := watcher.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	retired := fixture.ownerLease(t)
	if retired.State != runtimecatalog.OwnerLeaseRetired || retired.LeaseID != fixture.owner.LeaseID ||
		retired.DrainThroughJournalSequence == nil || *retired.DrainThroughJournalSequence != *closing.DrainThroughJournalSequence {
		t.Fatalf("exact closing generation was not retired: %+v", retired)
	}
	operation, err = fixture.journal.Read(context.Background(), fixture.runID, receipt.OperationID)
	if err != nil || operation.Status != StatusReconciliationRequired || len(operation.Outcomes) != 1 {
		t.Fatalf("ambiguous cancel lost its durable reconciliation status: operation=%+v err=%v", operation, err)
	}
	unresolved := model.beforeAppend
	unresolved.Events = append([]ledger.Event(nil), unresolved.Events...)
	request, requestErr := cancelRequestEvent(receipt, *operation.Claim, unresolved)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	unresolved.Events = append(unresolved.Events, request)
	if _, _, state, err := ReconcileCancelHistory(unresolved, operation); err != nil || state != CancelUnresolved {
		t.Fatalf("retired cancel is not available for read-only reconciliation: state=%v err=%v", state, err)
	}
	select {
	case cause := <-delivered:
		t.Fatalf("reconciliation replayed cancellation: %v", cause)
	default:
	}
	if _, writableCalls := model.calls(); writableCalls != 1 {
		t.Fatalf("reconciliation replayed cancellation: writable calls=%d", writableCalls)
	}

	replacement, err := fixture.catalog.InstallOwnerLease(fixture.runID, fixture.attemptID, fixture.owner.Process)
	if err != nil || replacement.LeaseID == fixture.owner.LeaseID {
		t.Fatalf("install replacement owner = %+v, err=%v", replacement, err)
	}
	if err := watcher.Close(context.Background()); err != nil {
		t.Fatalf("idempotent prior-generation close: %v", err)
	}
	current := fixture.ownerLease(t)
	if current.State != runtimecatalog.OwnerLeaseActive || current.LeaseID != replacement.LeaseID {
		t.Fatalf("prior watcher changed replacement generation: %+v", current)
	}
}

func TestCancelWatcherStopsOnReconciliationIntegrityFailure(t *testing.T) {
	fixture := newWatcherDrainFixture(t)
	receipt := fixture.createReceipt(t, "integrity-failure")
	delivered := make(chan error, 1)
	model := &ambiguousAppendWatcherModel{
		journal: fixture.journal, receipt: receipt, writer: fixture.ledger, beforeAppend: admittedCancelSnapshot(receipt),
		corruptReconciliation: true,
	}
	watcher, err := NewCancelWatcher(CancelWatcherConfig{
		Journal: fixture.journal, Catalog: fixture.catalog, ReadModel: model, Owner: fixture.owner,
		CancelCause: func(cause error) { delivered <- cause }, PollEvery: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	watcher.Start()
	select {
	case <-watcher.done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop on reconciliation integrity failure")
	}
	if err := watcher.watchError(); !errors.Is(err, serviceapi.ErrProjectionIntegrity) {
		t.Fatalf("watcher error = %v, want projection integrity", err)
	}
	operation, err := fixture.journal.Read(context.Background(), fixture.runID, receipt.OperationID)
	if err != nil || operation.Status != StatusReconciliationRequired {
		t.Fatalf("integrity failure lost durable reconciliation marker: operation=%+v err=%v", operation, err)
	}
	if _, writableCalls := model.calls(); writableCalls != 1 {
		t.Fatalf("integrity failure replayed cancellation: writable calls=%d", writableCalls)
	}
	select {
	case cause := <-delivered:
		t.Fatalf("failed append delivered cancellation: %v", cause)
	default:
	}
}

func waitForWatcherStatus(t *testing.T, journal *Journal, runID, operationID string, want OutcomeStatus) Operation {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last Operation
	var lastErr error
	for time.Now().Before(deadline) {
		last, lastErr = journal.Read(context.Background(), runID, operationID)
		if lastErr == nil && last.Status == want {
			return last
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("operation did not reach %s: operation=%+v err=%v", want, last, lastErr)
	return Operation{}
}

func waitForWatcherRepoll(t *testing.T, watcher *CancelWatcher, model *ambiguousAppendWatcherModel) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshotCalls, writableCalls := model.calls()
		if snapshotCalls >= 3 && writableCalls == 1 {
			return
		}
		select {
		case <-watcher.done:
			t.Fatalf("same watcher stopped after durable reconciliation-required: %v", watcher.watchError())
		default:
		}
		time.Sleep(time.Millisecond)
	}
	snapshotCalls, writableCalls := model.calls()
	t.Fatalf("same watcher did not repoll reconciliation-required operation: snapshots=%d writable=%d", snapshotCalls, writableCalls)
}

func TestCancelWatcherDoesNotRetireReceivedOrClaimed(t *testing.T) {
	for _, test := range []struct {
		name  string
		claim bool
	}{
		{name: "received"},
		{name: "claimed", claim: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWatcherDrainFixture(t)
			receipt := fixture.createReceipt(t, "blocked-"+test.name)
			var claim ActionClaimV1
			if test.claim {
				var created bool
				var err error
				claim, created, err = fixture.journal.CreateClaim(context.Background(), fixture.runID, receipt.OperationID, fixture.owner.LeaseID, CancelEventDomain)
				if err != nil || !created {
					t.Fatalf("create cancel claim = %+v, created=%v, err=%v", claim, created, err)
				}
			}
			model := &watcherDrainModel{snapshotErr: serviceapi.ErrAuthoritativeReadBusy}
			watcher, err := NewCancelWatcher(CancelWatcherConfig{
				Journal: fixture.journal, Catalog: fixture.catalog, ReadModel: model, Owner: fixture.owner,
				CancelCause: func(error) { t.Error("unresolved operation delivered cancellation") }, PollEvery: time.Millisecond,
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.claim {
				watcher.delivered[receipt.OperationID] = claim.ControllerEventID
			}

			closeContext, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			err = watcher.Close(closeContext)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Close with unresolved %s = %v, want deadline", test.name, err)
			}
			watcher.cancel()
			select {
			case <-watcher.done:
			case <-time.After(time.Second):
				t.Fatal("watcher did not stop after test cancellation")
			}

			closing := fixture.ownerLease(t)
			if closing.State != runtimecatalog.OwnerLeaseClosing || closing.LeaseID != fixture.owner.LeaseID || closing.DrainThroughJournalSequence == nil {
				t.Fatalf("unresolved %s did not keep exact owner CLOSING: %+v", test.name, closing)
			}
			operation, err := fixture.journal.Read(context.Background(), fixture.runID, receipt.OperationID)
			want := StatusReceived
			if test.claim {
				want = StatusClaimed
			}
			if err != nil || operation.Status != want {
				t.Fatalf("unresolved %s status = %s, err=%v", test.name, operation.Status, err)
			}
			probe := &CancelWatcher{journal: fixture.journal, owner: fixture.owner, ctx: context.Background()}
			drained, err := probe.drained(*closing.DrainThroughJournalSequence)
			if err != nil || drained {
				t.Fatalf("unresolved %s drained=%v err=%v", test.name, drained, err)
			}
		})
	}
}

func newWatcherDrainFixture(t *testing.T) *watcherDrainFixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "service")
	catalog, err := runtimecatalog.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	ledgerDirectory := filepath.Join(base, "ledger")
	if err := os.Mkdir(ledgerDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(ledgerDirectory, "events.jsonl")
	events, err := ledger.NewJSONLLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	runID, attemptID := "watcher-drain-run", "attempt-1"
	seed, err := ledger.NewEvent(runID, "WATCHER_DRAIN_FIXTURE", "control-plane", "test")
	if err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if err := events.Append(seed); err != nil {
		t.Fatalf("append ledger seed: %v", err)
	}
	evidenceRoot := filepath.Join(base, "evidence")
	if err := os.Mkdir(evidenceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	authorityDigest := digestText("watcher-drain-authority")
	registration, err := runtimecatalog.NewRunRegistrationV1(runID, "example/watcher-drain", authorityDigest, ledgerPath, evidenceRoot, journalTestTime)
	if err != nil {
		t.Fatalf("register run: %v", err)
	}
	if err := catalog.RegisterRun(registration); err != nil {
		t.Fatalf("persist run registration: %v", err)
	}
	attempt, err := runtimecatalog.NewAttemptRegistrationV1(runID, attemptID, authorityDigest, journalTestTime)
	if err != nil {
		t.Fatalf("register attempt: %v", err)
	}
	if err := catalog.RegisterAttempt(attempt); err != nil {
		t.Fatalf("persist attempt registration: %v", err)
	}
	process, err := recovery.CaptureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := catalog.InstallOwnerLease(runID, attemptID, process)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &watcherDrainFixture{runID: runID, attemptID: attemptID, catalog: catalog, journal: journal, ledger: events, owner: owner}
	t.Cleanup(func() {
		if err := errors.Join(journal.Close(), events.Close(), catalog.Close()); err != nil {
			t.Errorf("close watcher drain fixture: %v", err)
		}
	})
	return fixture
}

func (f *watcherDrainFixture) createReceipt(t *testing.T, requestID string) ActionReceiptV1 {
	t.Helper()
	input := journalReceiptInputForRun(f.runID)
	input.RequestID = requestID
	input.RequestSHA256 = digestText(requestID)
	receipt, created, err := f.journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: f.owner.LeaseID}, nil
	})
	if err != nil || !created {
		t.Fatalf("create cancel receipt = %+v, created=%v, err=%v", receipt, created, err)
	}
	return receipt
}

func (f *watcherDrainFixture) ownerLease(t *testing.T) runtimecatalog.ActiveOwnerLeaseV1 {
	t.Helper()
	guard, err := f.catalog.AcquireOwnerLeaseGuard(f.runID)
	if err != nil {
		t.Fatal(err)
	}
	lease, leaseErr := guard.Lease()
	closeErr := guard.Close()
	if err := errors.Join(leaseErr, closeErr); err != nil {
		t.Fatal(err)
	}
	return lease
}

func unresolvedCancelSnapshot(t *testing.T, receipt ActionReceiptV1, claim ActionClaimV1) readmodel.Snapshot {
	t.Helper()
	snapshot := admittedCancelSnapshot(receipt)
	request, err := cancelRequestEvent(receipt, claim, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Events = append(snapshot.Events, request)
	return snapshot
}

func admittedCancelSnapshot(receipt ActionReceiptV1) readmodel.Snapshot {
	admitted := ledger.Event{
		SchemaVersion: ledger.CurrentSchemaVersion, EventID: receipt.AdmittedStateTransitionID, Timestamp: journalTestTime,
		ProjectID: "project-1", PlanID: "plan-1", RunID: receipt.RunID, AttemptID: receipt.AttemptID,
		EventType: "STATE_TRANSITION", StateFrom: domain.StateExecutionStarting, StateTo: domain.StateImplementing,
		Actor: "control-plane", Source: "test",
	}
	return readmodel.Snapshot{
		Projection: readmodel.RunProjectionV1{RunID: receipt.RunID, CurrentState: receipt.ExpectedState},
		Events:     []ledger.Event{admitted},
	}
}

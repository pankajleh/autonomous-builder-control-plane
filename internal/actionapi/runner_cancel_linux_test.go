//go:build linux

package actionapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/actioncontrol"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	governancev3 "github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/recovery"
	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

func TestRunnerRunCarriesExactAPICancelProvenanceAcrossEveryExit(t *testing.T) {
	t.Run("validation identity failure", func(t *testing.T) {
		gate := newRunnerAPIGate()
		fixture := newRunnerAPIIntegrationFixture(t, runnerAPIFixtureOptions{
			ledger: &runnerAPILedgerGates{created: gate},
		})
		done := fixture.run()
		gate.wait(t)
		status := fixture.admitCancel(t, domain.StateRunCreated, "validation-cancel")
		fixture.waitForCancelCause(t, status.OperationID)
		gate.release()
		result := fixture.assertCancelled(t, done, status, domain.StateRunCreated, "authority-validator")
		if !strings.Contains(result.FailureReason, "resolve repository root") {
			t.Fatalf("identity-validation cancellation reason = %q", result.FailureReason)
		}
	})

	t.Run("supervisor process error", func(t *testing.T) {
		processes := newRunnerAPIProcessError()
		t.Cleanup(processes.gate.release)
		fixture := newRunnerAPIIntegrationFixture(t, runnerAPIFixtureOptions{processes: processes})
		done := fixture.run()
		processes.gate.wait(t)
		status := fixture.admitCancel(t, domain.StateImplementing, "process-error-cancel")
		fixture.waitForCancelCause(t, status.OperationID)
		processes.gate.release()
		result := fixture.assertCancelled(t, done, status, domain.StateImplementing, "ralphex-adapter")
		if !strings.Contains(result.FailureReason, "injected supervisor process error") {
			t.Fatalf("process-error cancellation reason = %q", result.FailureReason)
		}
	})

	t.Run("supervised process cancellation", func(t *testing.T) {
		ready := filepath.Join(t.TempDir(), "ralphex-ready")
		fixture := newRunnerAPIIntegrationFixture(t, runnerAPIFixtureOptions{ralphexScript: fmt.Sprintf(
			"#!/bin/sh\nprintf ready > %q\nwhile :; do sleep 60; done\n", ready,
		)})
		done := fixture.run()
		waitForRunnerAPIFile(t, ready)
		status := fixture.admitCancel(t, domain.StateImplementing, "supervised-cancel")
		fixture.waitForCancelCause(t, status.OperationID)
		result := fixture.assertCancelled(t, done, status, domain.StateImplementing, "ralphex-adapter")
		if result.Ralphex.Outcome != supervisor.OutcomeCanceled {
			t.Fatalf("supervised outcome = %s, want %s", result.Ralphex.Outcome, supervisor.OutcomeCanceled)
		}
	})

	t.Run("acceptance stage cancellation", func(t *testing.T) {
		ready := filepath.Join(t.TempDir(), "acceptance-ready")
		acceptance := filepath.Join(t.TempDir(), "blocking-acceptance")
		writeRunnerAPIFile(t, acceptance, []byte(fmt.Sprintf(
			"#!/bin/sh\nprintf ready > %q\nwhile :; do sleep 60; done\n", ready,
		)), 0o700)
		fixture := newRunnerAPIIntegrationFixture(t, runnerAPIFixtureOptions{acceptanceArgv: []string{acceptance}})
		done := fixture.run()
		waitForRunnerAPIFile(t, ready)
		status := fixture.admitCancel(t, domain.StateBranchAcceptancePending, "acceptance-cancel")
		fixture.waitForCancelCause(t, status.OperationID)
		result := fixture.assertCancelled(t, done, status, domain.StateBranchAcceptancePending, "acceptance-controller")
		commands := result.Acceptance.Commands()
		if len(commands) != 1 || commands[0].Process.Outcome != supervisor.OutcomeCanceled {
			t.Fatalf("acceptance cancellation result = %+v", commands)
		}
	})
}

func TestRunnerRunAPICancelWinsEverySuccessfulTransitionRace(t *testing.T) {
	tests := []struct {
		name        string
		acquireCall int
		from        domain.State
		source      string
	}{
		{"authority validation", 1, domain.StateRunCreated, "authority-validator"},
		{"execution start", 2, domain.StateAuthorityValidated, "governed-runner"},
		{"implementation start", 3, domain.StateExecutionStarting, "ralphex-adapter"},
		{"implementation completion", 4, domain.StateImplementing, "ralphex-adapter"},
		{"pre acceptance", 5, domain.StateImplementationCompleted, "acceptance-controller"},
		{"branch acceptance", 6, domain.StateBranchAcceptancePending, "acceptance-controller"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gate := newRunnerAPIGate()
			fixture := newRunnerAPIIntegrationFixture(t, runnerAPIFixtureOptions{
				ledger: &runnerAPILedgerGates{acquire: map[int]*runnerAPIGate{test.acquireCall: gate}},
			})
			done := fixture.run()
			gate.wait(t)
			status := fixture.admitCancel(t, test.from, fmt.Sprintf("successful-race-%d", test.acquireCall))
			fixture.waitForCancelCause(t, status.OperationID)
			gate.release()
			fixture.assertCancelled(t, done, status, test.from, test.source)
		})
	}
}

type runnerAPIFixtureOptions struct {
	ledger         *runnerAPILedgerGates
	processes      runctl.CommandRunner
	ralphexScript  string
	acceptanceArgv []string
}

type runnerAPIIntegrationFixture struct {
	runID      string
	attemptID  string
	principal  serviceapi.Principal
	runContext context.Context
	owner      runtimecatalog.ActiveOwnerLeaseV1
	runner     *runctl.Runner
	watcher    *actioncontrol.CancelWatcher
	controller *Controller
	journal    *actioncontrol.Journal
	model      *actioncontrol.GuardedReadModel
}

type runnerAPIResponse struct {
	result runctl.Result
	err    error
}

func newRunnerAPIIntegrationFixture(t *testing.T, options runnerAPIFixtureOptions) *runnerAPIIntegrationFixture {
	t.Helper()
	if options.ledger == nil {
		options.ledger = &runnerAPILedgerGates{}
	}
	if options.ralphexScript == "" {
		options.ralphexScript = "#!/bin/sh\nprintf candidate > candidate.txt\ngit add candidate.txt || exit 90\ngit commit -qm candidate || exit 91\n"
	}
	if len(options.acceptanceArgv) == 0 {
		// Acceptance must be a command that could fail. Manifest validation
		// rejects no-op commands such as /usr/bin/true, so the fixture uses a
		// real executable instead.
		validator := filepath.Join(t.TempDir(), "acceptance-validator")
		if err := os.WriteFile(validator, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		options.acceptanceArgv = []string{validator}
	}

	repository := filepath.Join(t.TempDir(), "repository")
	runRunnerAPIGit(t, "", "init", "-b", "main", repository)
	runRunnerAPIGit(t, repository, "config", "user.email", "runner-api@example.test")
	runRunnerAPIGit(t, repository, "config", "user.name", "Runner API Test")
	repositoryDigest := sha256.Sum256([]byte(repository))
	repositoryIdentity := "example/runner-api-" + hex.EncodeToString(repositoryDigest[:8])
	remoteURL := "https://example.test/" + repositoryIdentity + ".git"
	runRunnerAPIGit(t, repository, "remote", "add", "origin", remoteURL)
	planPath := filepath.Join(repository, "plan.md")
	writeRunnerAPIFile(t, planPath, []byte("# governed plan\n"), 0o600)
	writeRunnerAPIFile(t, filepath.Join(repository, "context.md"), []byte("governed context\n"), 0o600)
	if err := os.MkdirAll(filepath.Join(repository, filepath.Dir(governancev3.GovernancePolicyPathV1)), 0o700); err != nil {
		t.Fatal(err)
	}
	writeRunnerAPIFile(t, filepath.Join(repository, governancev3.GovernancePolicyPathV1), []byte("context authority policy v3\n"), 0o600)
	runRunnerAPIGit(t, repository, "add", "plan.md", "context.md", governancev3.GovernancePolicyPathV1)
	runRunnerAPIGit(t, repository, "commit", "-m", "initial plan")
	startSHA := runRunnerAPIGit(t, repository, "rev-parse", "HEAD")

	binaryPath := filepath.Join(t.TempDir(), "fake-ralphex")
	writeRunnerAPIFile(t, binaryPath, []byte(options.ralphexScript), 0o700)
	runID := "runner-api-run"
	manifest := authority.Manifest{
		RunID: runID,
		Repository: authority.RepositoryManifest{
			Path: repository, Identity: repositoryIdentity, Remotes: map[string]string{"origin": remoteURL}, DefaultBranch: "main", StartSHA: startSHA,
		},
		Plan: authority.PlanManifest{Path: planPath, SHA256: runnerAPIFileHash(t, planPath)},
		Ralphex: authority.RalphexManifest{
			BinaryPath: binaryPath, BinarySHA256: runnerAPIFileHash(t, binaryPath), SourceSHA: "runner-api-source", Mode: ralphex.ModeTasksOnly, Timeout: "5s", WaitOnLimit: "0s",
		},
		Executor: authority.ExecutorPolicy{
			Executor: "codex", TaskModel: "test-model", TaskEffort: "xhigh", ReviewModel: "test-review", ReviewEffort: "xhigh",
		},
		Acceptance:    []authority.AcceptanceCommand{{Name: "deterministic check", Class: "unit", Required: true, Timeout: "5s", Argv: options.acceptanceArgv}},
		PolicyVersion: "runner-api-test-v1",
	}
	bindRunnerAPICapsule(t, &manifest)
	governance := newRunnerAPIGovernanceController(t, repository, repositoryIdentity)
	governed, err := authority.NewWithGovernanceController(manifest, governance)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	serviceRoot := filepath.Join(root, "service")
	ledgerPath := filepath.Join(root, "ledger", "events.jsonl")
	events, err := ledger.NewJSONLLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := events.Snapshot(); err != nil {
		t.Fatal(err)
	}
	options.ledger.base = events
	artifacts, err := evidence.NewStore(filepath.Join(root, "evidence"), runID)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := runtimecatalog.Open(serviceRoot)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	registration, err := runtimecatalog.NewRunRegistrationV1(runID, repositoryIdentity, governed.SHA256(), ledgerPath, artifacts.RunDir(), at)
	if err != nil {
		t.Fatalf("register runner API run: %v", err)
	}
	if err := catalog.RegisterRun(registration); err != nil {
		t.Fatalf("register runner API run: %v", err)
	}
	attemptID := runID
	attempt, err := runtimecatalog.NewAttemptRegistrationV1(runID, attemptID, governed.SHA256(), at)
	if err != nil {
		t.Fatalf("register runner API attempt: %v", err)
	}
	if err := catalog.RegisterAttempt(attempt); err != nil {
		t.Fatalf("register runner API attempt: %v", err)
	}
	process, err := recovery.CaptureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := catalog.InstallOwnerLease(runID, attemptID, process)
	if err != nil {
		t.Fatal(err)
	}

	signer, err := serviceapi.NewCursorSigner("runner-api", []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	localModel, err := readmodel.New(catalog, signer)
	if err != nil {
		t.Fatal(err)
	}
	model, err := actioncontrol.NewGuardedReadModel(serviceRoot, localModel)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := actioncontrol.Open(serviceRoot)
	if err != nil {
		t.Fatal(err)
	}
	grantsPath := filepath.Join(root, "authority-grants.json")
	grants := serviceapi.AuthorityGrantFileV1{Principals: []serviceapi.AuthorityGrantV1{{
		PrincipalID: "runner-api-service", RequiredAuthorities: []string{"decision.authority"}, MayAssertDelegatedActor: true,
	}}}
	writeRunnerAPIFile(t, grantsPath, mustJSON(t, grants), 0o600)
	matcher, err := serviceapi.LoadAuthorityMatcher(grantsPath)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := NewController(ControllerConfig{Catalog: catalog, ReadModel: model, Journal: journal, Authority: matcher})
	if err != nil {
		t.Fatal(err)
	}

	runContext, cancelRun := context.WithCancelCause(context.Background())
	watcher, err := actioncontrol.NewCancelWatcher(actioncontrol.CancelWatcherConfig{
		Journal: journal, Catalog: catalog, ReadModel: model, Owner: owner, CancelCause: cancelRun, PollEvery: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	processes := options.processes
	if processes == nil {
		processes = supervisor.New()
	}
	runner, err := runctl.NewWithController(governed, options.ledger, artifacts, processes, governance)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.SetFinalizationHook(watcher); err != nil {
		t.Fatal(err)
	}
	watcher.Start()

	fixture := &runnerAPIIntegrationFixture{
		runID: runID, attemptID: attemptID,
		principal:  serviceapi.Principal{PrincipalID: "runner-api-service", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test"},
		runContext: runContext, owner: owner, runner: runner, watcher: watcher,
		controller: actions, journal: journal, model: model,
	}
	t.Cleanup(func() {
		options.ledger.releaseAll()
		cancelRun(errors.New("test cleanup"))
		closeCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		_ = watcher.Close(closeCtx)
		cancel()
		_ = actions.Close()
		_ = model.Close()
		_ = journal.Close()
		_ = events.Close()
		_ = catalog.Close()
	})
	return fixture
}

func (f *runnerAPIIntegrationFixture) run() <-chan runnerAPIResponse {
	done := make(chan runnerAPIResponse, 1)
	go func() {
		result, err := f.runner.Run(f.runContext)
		done <- runnerAPIResponse{result: result, err: err}
	}()
	return done
}

func (f *runnerAPIIntegrationFixture) admitCancel(t *testing.T, expected domain.State, requestID string) serviceapi.ActionStatusV1 {
	t.Helper()
	snapshot, err := f.model.Snapshot(context.Background(), f.runID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Projection.CurrentState != string(expected) {
		t.Fatalf("state at cancel admission = %s, want %s", snapshot.Projection.CurrentState, expected)
	}
	status, err := f.controller.AdmitAction(context.Background(), f.principal, f.runID, serviceapi.ActionCancel, serviceapi.CommandEnvelopeV1{
		SchemaVersion: 1, RequestID: requestID, AttemptID: f.attemptID, ExpectedState: string(expected),
		ExpectedRevision: snapshot.Projection.ProjectionRevision, Reason: "prove Runner.Run cancellation", Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func (f *runnerAPIIntegrationFixture) waitForCancelCause(t *testing.T, operationID string) {
	t.Helper()
	select {
	case <-f.runContext.Done():
		provenance, ok := runctl.APICancelProvenance(f.runContext)
		if !ok || provenance.OperationID != operationID || provenance.OwnerLeaseID != f.owner.LeaseID {
			t.Fatalf("runner cancellation provenance = %+v, present=%v", provenance, ok)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owner watcher did not deliver API cancellation")
	}
}

func (f *runnerAPIIntegrationFixture) assertCancelled(t *testing.T, done <-chan runnerAPIResponse, status serviceapi.ActionStatusV1, from domain.State, source string) runctl.Result {
	t.Helper()
	var response runnerAPIResponse
	select {
	case response = <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("Runner.Run did not return after API cancellation")
	}
	if response.err != nil || response.result.State != domain.StateCancelled {
		t.Fatalf("Runner.Run cancellation result = %+v, err=%v", response.result, response.err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.watcher.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	settled := awaitActionStatus(t, f.controller, f.principal, f.runID, status.OperationID, string(actioncontrol.StatusApplied))

	snapshot, err := f.model.Snapshot(context.Background(), f.runID)
	if err != nil {
		t.Fatal(err)
	}
	requestID := actioncontrol.DeterministicEventID(actioncontrol.CancelEventDomain, status.OperationID)
	requestIndex, cancelledIndex := -1, -1
	var cancelled ledger.Event
	requestCount, cancelledCount := 0, 0
	for index, event := range snapshot.Events {
		if event.EventID == requestID {
			requestCount++
			requestIndex = index
			if event.EventType != "API_CANCEL_REQUESTED" || payloadString(event.Payload, "operation_id") != status.OperationID ||
				payloadString(event.Payload, "owner_lease_id") != f.owner.LeaseID {
				t.Fatalf("durable cancel request = %+v", event)
			}
		}
		if event.StateTo == domain.StateCancelled {
			cancelledCount++
			cancelledIndex, cancelled = index, event
		}
		if event.StateFrom == from && event.StateTo != domain.StateCancelled {
			t.Fatalf("competing transition from %s survived: %+v", from, event)
		}
	}
	if requestCount != 1 || cancelledCount != 1 || requestIndex < 0 || requestIndex >= cancelledIndex {
		t.Fatalf("authoritative cancellation counts request=%d cancelled=%d indexes=%d/%d", requestCount, cancelledCount, requestIndex, cancelledIndex)
	}
	if cancelled.StateFrom != from || cancelled.Source != source || payloadString(cancelled.Payload, "operation_id") != status.OperationID ||
		payloadString(cancelled.Payload, "owner_lease_id") != f.owner.LeaseID || payloadString(cancelled.Payload, "request_event_id") != requestID {
		t.Fatalf("authoritative CANCELLED transition = %+v", cancelled)
	}
	for index := cancelledIndex + 1; index < len(snapshot.Events); index++ {
		if snapshot.Events[index].StateFrom != "" {
			t.Fatalf("state transition followed authoritative cancellation: %+v", snapshot.Events[index])
		}
	}
	if len(settled.AuthoritativeEventIDs) != 2 || settled.AuthoritativeEventIDs[0] != requestID || settled.AuthoritativeEventIDs[1] != cancelled.EventID {
		t.Fatalf("settled cancel outcome = %+v", settled)
	}
	operation, err := f.journal.Read(context.Background(), f.runID, status.OperationID)
	if err != nil || operation.Receipt.OwnerLeaseID != f.owner.LeaseID || operation.Status != actioncontrol.StatusApplied {
		t.Fatalf("durable cancel operation = %+v, err=%v", operation, err)
	}
	return response.result
}

type runnerAPIGate struct {
	reached     chan struct{}
	releaseCh   chan struct{}
	reachedOnce sync.Once
	releaseOnce sync.Once
}

func newRunnerAPIGate() *runnerAPIGate {
	return &runnerAPIGate{reached: make(chan struct{}), releaseCh: make(chan struct{})}
}

func (g *runnerAPIGate) block() {
	g.reachedOnce.Do(func() { close(g.reached) })
	<-g.releaseCh
}

func (g *runnerAPIGate) wait(t *testing.T) {
	t.Helper()
	select {
	case <-g.reached:
	case <-time.After(8 * time.Second):
		t.Fatal("Runner.Run did not reach the cancellation gate")
	}
}

func (g *runnerAPIGate) release() { g.releaseOnce.Do(func() { close(g.releaseCh) }) }

type runnerAPILedgerGates struct {
	base    *ledger.JSONLLedger
	created *runnerAPIGate
	acquire map[int]*runnerAPIGate
	mu      sync.Mutex
	calls   int
}

func (l *runnerAPILedgerGates) Append(event ledger.Event) error {
	if err := l.base.Append(event); err != nil {
		return err
	}
	if event.EventType == string(domain.StateRunCreated) && l.created != nil {
		l.created.block()
	}
	return nil
}

func (l *runnerAPILedgerGates) Snapshot() ([]byte, string, error) { return l.base.Snapshot() }

func (l *runnerAPILedgerGates) AcquireRunTransition(runID string) (*ledger.RunTransitionLease, error) {
	l.mu.Lock()
	l.calls++
	gate := l.acquire[l.calls]
	l.mu.Unlock()
	if gate != nil {
		gate.block()
	}
	return l.base.AcquireRunTransition(runID)
}

func (l *runnerAPILedgerGates) AppendOrVerifyLeased(event ledger.Event, lease *ledger.RunTransitionLease) error {
	return l.base.AppendOrVerifyLeased(event, lease)
}

func (l *runnerAPILedgerGates) releaseAll() {
	if l.created != nil {
		l.created.release()
	}
	for _, gate := range l.acquire {
		gate.release()
	}
}

type runnerAPIProcessError struct {
	gate *runnerAPIGate
}

func newRunnerAPIProcessError() *runnerAPIProcessError {
	return &runnerAPIProcessError{gate: newRunnerAPIGate()}
}

func (r *runnerAPIProcessError) Run(context.Context, supervisor.Command) (supervisor.Result, error) {
	r.gate.block()
	return supervisor.Result{}, errors.New("injected supervisor process error")
}

type runnerAPIGovernanceRecord struct {
	data     []byte
	revision uint64
}

type runnerAPIGovernanceBackend struct {
	mu      sync.Mutex
	records map[string]runnerAPIGovernanceRecord
}

func (b *runnerAPIGovernanceBackend) AuthorityDomainV1() (string, error) {
	return strings.Repeat("f", 64), nil
}

func (b *runnerAPIGovernanceBackend) LoadWorkflowStateV1(controllerIdentity string) ([]byte, uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	record, ok := b.records[controllerIdentity]
	if !ok {
		return nil, 0, errors.New("authority state is not initialized")
	}
	return append([]byte(nil), record.data...), record.revision, nil
}

func (b *runnerAPIGovernanceBackend) CompareAndSwapWorkflowStateV1(controllerIdentity string, expectedRevision uint64, data []byte) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	record, ok := b.records[controllerIdentity]
	if !ok {
		return false, errors.New("authority state is not initialized")
	}
	if record.revision != expectedRevision {
		return false, nil
	}
	var state governancev3.ControllerStateV1
	if err := governancev3.ParseCanonical(data, &state); err != nil {
		return false, err
	}
	b.records[controllerIdentity] = runnerAPIGovernanceRecord{data: append([]byte(nil), data...), revision: state.Revision}
	return true, nil
}

func newRunnerAPIGovernanceController(t *testing.T, repository, repositoryIdentity string) *governancev3.ControllerV1 {
	t.Helper()
	backend := &runnerAPIGovernanceBackend{records: make(map[string]runnerAPIGovernanceRecord)}
	controller, err := governancev3.OpenControllerWithAuthorityBackendV1(repository, backend)
	if err != nil {
		t.Fatal(err)
	}
	state := governancev3.ControllerStateV1{
		Kind: "GovernanceControllerStateV1", ControllerIdentity: controller.ControllerIdentity(), RepositoryIdentity: repositoryIdentity, Revision: 1,
		IssuedV2Authorities: []governancev3.IssuedAuthorityV1{}, ExecutionState: ralphex.ExecutionStateV1{AggregateElapsed: "0s"}, FindingEvidence: []governancev3.FindingEvidenceV1{},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	backend.records[controller.ControllerIdentity()] = runnerAPIGovernanceRecord{data: data, revision: state.Revision}
	return controller
}

func bindRunnerAPICapsule(t *testing.T, manifest *authority.Manifest) {
	t.Helper()
	spec := contextcapsule.Spec{
		PolicyVersion: contextcapsule.PolicyVersionV2,
		Project:       "ABCP", Plan: "governed plan", RoadmapPhase: "test", ExecutionPack: "test", Task: "Task 4",
		Repository: manifest.Repository.Identity, BaseSHA: manifest.Repository.StartSHA,
		OperationContext: &contextcapsule.OperationContext{
			Kind: contextcapsule.OperationImplementation, OwnedScope: []string{"Task 4"},
			BlockingCriteria: []string{"Current owned-scope Critical or Major findings."},
		},
		Invariants: []string{"Fail closed."}, NonGoals: []string{"No out-of-scope changes."},
		PredecessorOutcomes: []contextcapsule.Outcome{}, Sources: []string{"context.md"},
	}
	_, data, err := contextcapsule.Build(manifest.Repository.Path, spec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "context-capsule.json")
	writeRunnerAPIFile(t, path, data, 0o600)
	manifest.ContextCapsule = &authority.ContextCapsuleManifest{Path: path, SHA256: runnerAPIFileHash(t, path)}
}

func waitForRunnerAPIFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runRunnerAPIGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeRunnerAPIFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func runnerAPIFileHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

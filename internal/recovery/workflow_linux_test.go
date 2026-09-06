//go:build linux

package recovery

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/blocker"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

func TestRecoveryWorkflowSuccessfulStaleRecoveryPreservesDirtyStateAndBranch(t *testing.T) {
	fixture := newWorktreeFixture(t)
	ownership := deadOwnership(t, fixture)
	dirtyContents := []byte("fixture\nvaluable uncommitted recovery work\n")
	writeTestRecoveryFile(t, filepath.Join(fixture.worktree, "README.md"), dirtyContents)
	progressRoot := filepath.Join(t.TempDir(), "progress")
	if err := os.Mkdir(progressRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	progressPath := filepath.Join(progressRoot, "attempt.log")
	writeTestRecoveryFile(t, progressPath, []byte("last governed task state\n"))
	ungovernedProgressPath := filepath.Join(progressRoot, "other-attempt.log")
	writeTestRecoveryFile(t, ungovernedProgressPath, []byte("must remain\n"))
	branchHead := strings.TrimSpace(gitRecoveryOutput(t, fixture.repository, "rev-parse", fixture.branch))

	workflow, ledgerPath := newRecoveryWorkflow(t, ownership.Attempt.RunID, GovernedCleaner{})
	request := successfulWorkflowRequest(ownership)
	request.ProgressRoot = progressRoot
	request.ProgressPaths = []string{progressPath}
	result, err := workflow.Recover(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Boundary != domain.StateExecutionStarting || result.Authorized.AttemptID != "attempt-2" {
		t.Fatalf("restart result = boundary %s, attempt %#v", result.Boundary, result.Authorized)
	}
	if result.Classification.Class != blocker.ClassUnknownAmbiguous || result.Classification.RecommendedState != domain.StateRecoveryRequired {
		t.Fatalf("classification = %#v", result.Classification)
	}
	if _, err := os.Stat(fixture.worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("governed stale worktree still exists: %v", err)
	}
	if _, err := os.Stat(progressPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("governed progress file still exists: %v", err)
	}
	if contents, err := os.ReadFile(ungovernedProgressPath); err != nil || string(contents) != "must remain\n" {
		t.Fatalf("cleanup changed ungoverned progress file: contents=%q error=%v", contents, err)
	}
	if got := strings.TrimSpace(gitRecoveryOutput(t, fixture.repository, "rev-parse", fixture.branch)); got != branchHead {
		t.Fatalf("branch history changed during cleanup: got %s, want %s", got, branchHead)
	}
	diff := readVerifiedRecoveryArtifact(t, result.Snapshot.Metadata.Diff.Ref)
	if !strings.Contains(string(diff), "+valuable uncommitted recovery work") {
		t.Fatalf("snapshot did not preserve dirty state: %q", diff)
	}
	progress := readVerifiedRecoveryArtifact(t, result.Snapshot.Metadata.Progress.Ref)
	if !strings.Contains(string(progress), "last governed task state") {
		t.Fatalf("snapshot did not preserve progress: %q", progress)
	}

	events := readRecoveryEvents(t, ledgerPath)
	wantTypes := []string{
		EventRecoveryInspected,
		EventRecoverySnapshotPublished,
		EventBlockerDecision,
		EventRecoveryCleanupAuthorized,
		EventRecoveryCleanupCompleted,
		EventResumeAuthority,
		EventRestartPrepared,
	}
	gotTypes := make([]string, len(events))
	for index, event := range events {
		gotTypes[index] = event.EventType
		for _, ref := range event.EvidenceRefs {
			readVerifiedRecoveryArtifact(t, ref)
		}
		if event.StateTo == domain.StateIntegrationPending || event.StateTo == domain.StateReadyForMerge ||
			event.StateTo == domain.StateMerged || event.StateTo == domain.StateCompleted {
			t.Fatalf("recovery crossed forbidden boundary to %s", event.StateTo)
		}
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("event order\nwant: %#v\n got: %#v", wantTypes, gotTypes)
	}
}

func TestRecoveryWorkflowRefusesLiveAndAmbiguousOwners(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(Ownership) Ownership
		want   Classification
	}{
		{name: "live", mutate: func(ownership Ownership) Ownership { return ownership }, want: ClassificationActive},
		{name: "ambiguous", mutate: func(ownership Ownership) Ownership {
			ownership.Process.BootID = ""
			return ownership
		}, want: ClassificationAmbiguous},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorktreeFixture(t)
			ownership := test.mutate(fixture.ownership(t, os.Getpid()))
			cleaner := &trackingCleaner{}
			workflow, ledgerPath := newRecoveryWorkflow(t, ownership.Attempt.RunID, cleaner)
			request := successfulWorkflowRequest(ownership)

			result, err := workflow.Recover(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), "positive owner-dead proof") {
				t.Fatalf("owner refusal error = %v", err)
			}
			if result.Inspection.Classification != test.want {
				t.Fatalf("classification = %s, want %s", result.Inspection.Classification, test.want)
			}
			if cleaner.called {
				t.Fatal("cleanup ran without positive owner-dead proof")
			}
			if _, err := os.Stat(fixture.worktree); err != nil {
				t.Fatalf("refusal changed governed worktree: %v", err)
			}
			events := readRecoveryEvents(t, ledgerPath)
			if len(events) != 1 || events[0].EventType != EventRecoveryInspected {
				t.Fatalf("refusal events = %#v", events)
			}
		})
	}
}

func TestRecoveryWorkflowRequiresExplicitAuthorityBeforeCleanup(t *testing.T) {
	fixture := newWorktreeFixture(t)
	ownership := deadOwnership(t, fixture)
	cleaner := &trackingCleaner{}
	workflow, ledgerPath := newRecoveryWorkflow(t, ownership.Attempt.RunID, cleaner)
	request := successfulWorkflowRequest(ownership)
	request.Authorization = nil

	_, err := workflow.Recover(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "explicit recovery authorization") {
		t.Fatalf("missing authority error = %v", err)
	}
	if cleaner.called {
		t.Fatal("cleanup ran without explicit authority")
	}
	if _, err := os.Stat(fixture.worktree); err != nil {
		t.Fatalf("missing authority changed governed worktree: %v", err)
	}
	events := readRecoveryEvents(t, ledgerPath)
	want := []string{EventRecoveryInspected, EventRecoverySnapshotPublished, EventBlockerDecision}
	got := make([]string, len(events))
	for index := range events {
		got[index] = events[index].EventType
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("missing-authority events = %#v, want %#v", got, want)
	}
}

func TestRecoveryWorkflowRejectsForbiddenBoundaryBeforeCleanup(t *testing.T) {
	fixture := newWorktreeFixture(t)
	ownership := deadOwnership(t, fixture)
	cleaner := &trackingCleaner{}
	workflow, _ := newRecoveryWorkflow(t, ownership.Attempt.RunID, cleaner)
	request := successfulWorkflowRequest(ownership)
	request.Authorization.Boundary = domain.StateReadyForMerge

	_, err := workflow.Recover(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("forbidden boundary error = %v", err)
	}
	if cleaner.called {
		t.Fatal("cleanup ran for forbidden integration/merge boundary")
	}
	if _, err := os.Stat(fixture.worktree); err != nil {
		t.Fatalf("forbidden-boundary refusal changed worktree: %v", err)
	}
}

func TestRecoveryWorkflowRecordsCleanupFailureAndDoesNotPrepareRestart(t *testing.T) {
	fixture := newWorktreeFixture(t)
	ownership := deadOwnership(t, fixture)
	cleaner := &trackingCleaner{err: errors.New("injected cleanup failure")}
	workflow, ledgerPath := newRecoveryWorkflow(t, ownership.Attempt.RunID, cleaner)

	_, err := workflow.Recover(context.Background(), successfulWorkflowRequest(ownership))
	if err == nil || !strings.Contains(err.Error(), "injected cleanup failure") {
		t.Fatalf("cleanup failure error = %v", err)
	}
	if !cleaner.called {
		t.Fatal("injected cleaner was not called")
	}
	events := readRecoveryEvents(t, ledgerPath)
	want := []string{
		EventRecoveryInspected, EventRecoverySnapshotPublished, EventBlockerDecision,
		EventRecoveryCleanupAuthorized, EventRecoveryCleanupFailed,
	}
	got := make([]string, len(events))
	for index := range events {
		got[index] = events[index].EventType
		if events[index].EventType == EventResumeAuthority || events[index].EventType == EventRestartPrepared {
			t.Fatalf("cleanup failure prepared restart: %#v", events[index])
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup-failure events = %#v, want %#v", got, want)
	}
}

func TestGovernedCleanerRefusesStateChangedAfterSnapshot(t *testing.T) {
	fixture := newWorktreeFixture(t)
	ownership := deadOwnership(t, fixture)
	store, err := evidence.NewStore(t.TempDir(), ownership.Attempt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, err := NewSnapshotter(store)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotter.Capture(context.Background(), SnapshotRequest{
		SnapshotID: "changed-after-snapshot", Ownership: ownership,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeTestRecoveryFile(t, filepath.Join(fixture.worktree, "late-change.txt"), []byte("not in snapshot\n"))

	_, err = (GovernedCleaner{}).Cleanup(context.Background(), CleanupRequest{
		Ownership: ownership, Snapshot: snapshot,
	})
	if err == nil || !strings.Contains(err.Error(), "changed after snapshot") {
		t.Fatalf("changed-state cleanup error = %v", err)
	}
	if _, err := os.Stat(fixture.worktree); err != nil {
		t.Fatalf("changed-state refusal removed worktree: %v", err)
	}
}

type trackingCleaner struct {
	called bool
	err    error
}

func (cleaner *trackingCleaner) Cleanup(_ context.Context, request CleanupRequest) (CleanupReport, error) {
	cleaner.called = true
	return CleanupReport{
		RepositoryPath: request.Ownership.Worktree.RepositoryPath,
		WorktreePath:   request.Ownership.Worktree.Path,
		Branch:         request.Ownership.Worktree.Branch,
	}, cleaner.err
}

func successfulWorkflowRequest(ownership Ownership) WorkflowRequest {
	authorized := ownership.Attempt
	authorized.AttemptID = "attempt-2"
	authorized.AgentSessionID = "session-2"
	return WorkflowRequest{
		SnapshotID: "workflow-e2e",
		Ownership:  ownership,
		StateFrom:  domain.StateImplementing,
		Failure: blocker.FailureInput{
			Phase: blocker.PhaseExecution, Diagnostics: "unclassified controller failure",
		},
		Requirement: BlockerRequirement{Recovery: &RecoveryRequirement{
			Reason: "no deterministic classifier rule matched", RequiredAuthority: "recovery-controller",
		}},
		Authorization: &RecoveryAuthorization{
			AuthorizedAttempt: authorized,
			Actor:             "repository-owner",
			Decision:          "replace stale governed attempt after immutable snapshot",
			Action:            ResumeActionRestart,
			Timestamp:         time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC),
			PolicyVersion:     "recovery-v1",
			Boundary:          domain.StateExecutionStarting,
		},
	}
}

func newRecoveryWorkflow(t *testing.T, runID string, cleaner Cleaner) (*Workflow, string) {
	t.Helper()
	root := t.TempDir()
	store, err := evidence.NewStore(filepath.Join(root, "evidence"), runID)
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(root, "ledger", "events.jsonl")
	events, err := ledger.NewJSONLLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := NewWorkflow(events, store, cleaner)
	if err != nil {
		t.Fatal(err)
	}
	return workflow, ledgerPath
}

func readRecoveryEvents(t *testing.T, path string) []ledger.Event {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var events []ledger.Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event ledger.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

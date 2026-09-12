package ledger

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
)

func TestTransitionBarrierBlocksCompetingWriterAndAllowsExactTerminal(t *testing.T) {
	value, err := NewJSONLLedger(t.TempDir() + "/events.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := NewTransitionBarrier("run-1", "attempt-1", strings.Repeat("a", 64), time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := value.InstallTransitionBarrier(barrier); err != nil {
		t.Fatal(err)
	}
	competing := transitionEvent(t, "run-1", "attempt-other", "competing", domain.StateFailed)
	if err := value.Append(competing); err == nil || !strings.Contains(err.Error(), "barrier") {
		t.Fatalf("competing transition passed barrier: %v", err)
	}
	terminal := transitionEvent(t, "run-1", "attempt-1", "terminal", domain.StateMerged)
	if err := value.AppendOrVerifyTransition(terminal, barrier.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := value.AppendOrVerifyTransition(terminal, barrier.SHA256); err != nil {
		t.Fatalf("exact terminal replay failed: %v", err)
	}
	forged := terminal
	forged.StateTo = domain.StateFailed
	if err := value.AppendOrVerifyTransition(forged, barrier.SHA256); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting event identity was accepted: %v", err)
	}
	if err := value.ResolveTransitionBarrier(barrier); err != nil {
		t.Fatal(err)
	}
	if _, active, err := value.ActiveTransitionBarrier("run-1"); err != nil || active {
		t.Fatalf("resolved barrier remains active: active=%v err=%v", active, err)
	}
}

func TestTransitionBarrierStrictRecoveryAndStagedInstall(t *testing.T) {
	value, err := NewJSONLLedger(t.TempDir() + "/events.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := NewTransitionBarrier("run-2", "attempt-2", strings.Repeat("b", 64), time.Unix(1700000001, 0))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(barrier)
	if _, err := ParseTransitionBarrier(append(data, []byte("{}")...)); err == nil {
		t.Fatal("barrier parser accepted trailing JSON")
	}
	if err := value.InstallTransitionBarrier(barrier); err != nil {
		t.Fatal(err)
	}
	if err := value.InstallTransitionBarrier(barrier); err != nil {
		t.Fatalf("identical install was not idempotent: %v", err)
	}
}

func transitionEvent(t *testing.T, runID, attemptID, eventID string, to domain.State) Event {
	t.Helper()
	event := Event{SchemaVersion: CurrentSchemaVersion, EventID: eventID, Timestamp: time.Unix(1700000010, 0).UTC(),
		ProjectID: "project-1", PlanID: "plan-1", RunID: runID, AttemptID: attemptID, EventType: "STATE_TRANSITION",
		StateFrom: domain.StateReadyForMerge, StateTo: to, Actor: "controller", Source: "test"}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	return event
}

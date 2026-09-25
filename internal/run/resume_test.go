package run

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/recovery"
)

const humanDecisionRalphexScript = "#!/bin/sh\nprintf 'human decision required: authority is unavailable\\n' >&2\nexit 17\n"

// recordDecision appends the API_HUMAN_DECISION_RECORDED event an action
// controller would append for one open blocker decision.
func recordDecision(t *testing.T, ledgerPath, blockerID, answer string) {
	t.Helper()
	events, err := ledger.NewJSONLLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	event, err := ledger.NewEvent("run-fixture", "API_HUMAN_DECISION_RECORDED", "control-plane", "service-action-controller")
	if err != nil {
		t.Fatal(err)
	}
	event.Payload = map[string]any{
		"record_schema_version": 1,
		"decision_request_id":   blockerID,
		"answer":                answer,
	}
	if err := events.Append(event); err != nil {
		t.Fatal(err)
	}
}

func resumeRunner(t *testing.T, f runFixture) *Runner {
	t.Helper()
	runner, err := f.construct(t, f.authority)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

// An abort decision terminates a paused run as FAILED without re-implementation.
func TestRunnerResumeAbort(t *testing.T) {
	fixture := newRunFixtureWithScript(t, humanDecisionRalphexScript,
		authority.WorktreePolicy{Enabled: true, Branch: "abcp/resume-abort"}, acceptanceValidator(t))
	first := fixture.execute(t)
	if first.State != domain.StateHumanDecisionRequired {
		t.Fatalf("first state = %s, want HUMAN_DECISION_REQUIRED", first.State)
	}

	blocker := findEventByType(t, readEvents(t, fixture.ledgerPath), recovery.EventBlockerDecision)
	recordDecision(t, fixture.ledgerPath, blocker.EventID, "abort")

	runner := resumeRunner(t, fixture)
	resumed, err := runner.RunResume(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != domain.StateFailed {
		t.Fatalf("abort resume state = %s, want FAILED", resumed.State)
	}

	events := readEvents(t, fixture.ledgerPath)
	if countEventType(events, recovery.EventBlockerDecision) != 1 {
		t.Fatalf("abort must not re-implement; blockers = %d, want 1", countEventType(events, recovery.EventBlockerDecision))
	}
	if lastState(events) != domain.StateFailed {
		t.Fatalf("last state = %s, want FAILED", lastState(events))
	}
}

// A proceed decision restarts the implementation attempt, so a second Ralphex
// failure produces a second blocker decision.
func TestRunnerResumeProceedReimplements(t *testing.T) {
	fixture := newRunFixtureWithScript(t, humanDecisionRalphexScript,
		authority.WorktreePolicy{Enabled: true, Branch: "abcp/resume-proceed"}, acceptanceValidator(t))
	first := fixture.execute(t)
	if first.State != domain.StateHumanDecisionRequired {
		t.Fatalf("first state = %s, want HUMAN_DECISION_REQUIRED", first.State)
	}

	blocker := findEventByType(t, readEvents(t, fixture.ledgerPath), recovery.EventBlockerDecision)
	recordDecision(t, fixture.ledgerPath, blocker.EventID, "proceed")

	runner := resumeRunner(t, fixture)
	resumed, err := runner.RunResume(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != domain.StateHumanDecisionRequired {
		t.Fatalf("proceed resume state = %s, want HUMAN_DECISION_REQUIRED (second pause)", resumed.State)
	}

	events := readEvents(t, fixture.ledgerPath)
	if countEventType(events, recovery.EventBlockerDecision) != 2 {
		t.Fatalf("proceed must re-implement once; blockers = %d, want 2", countEventType(events, recovery.EventBlockerDecision))
	}
	// The resume transition out of the first pause must be recorded.
	transitions := stateTransitions(events)
	foundResume := false
	for _, tr := range transitions {
		if tr.from == domain.StateHumanDecisionRequired && tr.to == domain.StateAuthorityValidated {
			foundResume = true
		}
	}
	if !foundResume {
		t.Fatalf("no HUMAN_DECISION_REQUIRED -> AUTHORITY_VALIDATED resume transition recorded")
	}
}

type stateTransition struct{ from, to domain.State }

func stateTransitions(events []ledger.Event) []stateTransition {
	var out []stateTransition
	for _, event := range events {
		if event.StateFrom != "" {
			out = append(out, stateTransition{event.StateFrom, event.StateTo})
		}
	}
	return out
}

var _ = json.Marshal

package run

import (
	"encoding/json"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/recovery"
)

// A Ralphex failure whose diagnostics match the classifier's human-decision
// rule must pause the run in HUMAN_DECISION_REQUIRED and record exactly one
// blocker decision, rather than failing outright.
func TestRunnerPausesForHumanDecisionOnClassifiedFailure(t *testing.T) {
	script := "#!/bin/sh\nprintf 'human decision required: authority is unavailable\\n' >&2\nexit 17\n"
	fixture := newRunFixtureWithScript(t, script, authority.WorktreePolicy{Enabled: true, Branch: "abcp/human-decision-test"}, acceptanceValidator(t))
	result := fixture.execute(t)

	if result.State != domain.StateHumanDecisionRequired {
		t.Fatalf("state = %s, want HUMAN_DECISION_REQUIRED", result.State)
	}
	if result.Accepted() {
		t.Fatal("a paused run must not be accepted")
	}

	events := readEvents(t, fixture.ledgerPath)
	blocker := findEventByType(t, events, recovery.EventBlockerDecision)
	if countEventType(events, recovery.EventBlockerDecision) != 1 {
		t.Fatalf("blocker decisions = %d, want 1", countEventType(events, recovery.EventBlockerDecision))
	}
	if lastState(events) != domain.StateHumanDecisionRequired {
		t.Fatalf("last state = %s, want HUMAN_DECISION_REQUIRED", lastState(events))
	}

	payload, err := json.Marshal(blocker.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Requirement recovery.BlockerRequirement `json:"requirement"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	human := decoded.Requirement.HumanDecision
	if human == nil {
		t.Fatal("blocker decision has no human decision requirement")
	}
	if human.Question != "Proceed with the next attempt?" ||
		len(human.AcceptedAnswers) != 2 ||
		human.AcceptedAnswers[0] != "proceed" || human.AcceptedAnswers[1] != "abort" {
		t.Fatalf("unexpected human decision requirement: %+v", human)
	}
}

// A failure that does not classify to human decision must keep failing exactly
// as before.
func TestRunnerStillFailsUnclassifiedFailure(t *testing.T) {
	fixture := newRunFixture(t, 99, acceptanceValidator(t))
	result := fixture.execute(t)
	if result.State != domain.StateFailed {
		t.Fatalf("state = %s, want FAILED", result.State)
	}
	events := readEvents(t, fixture.ledgerPath)
	if countEventType(events, recovery.EventBlockerDecision) != 0 {
		t.Fatal("unclassified failure must not record a blocker decision")
	}
}

func findEventByType(t *testing.T, events []ledger.Event, eventType string) ledger.Event {
	t.Helper()
	for _, event := range events {
		if event.EventType == eventType {
			return event
		}
	}
	t.Fatalf("no event of type %s", eventType)
	return ledger.Event{}
}

func countEventType(events []ledger.Event, eventType string) int {
	count := 0
	for _, event := range events {
		if event.EventType == eventType {
			count++
		}
	}
	return count
}

func lastState(events []ledger.Event) domain.State {
	var state domain.State
	for _, event := range events {
		if event.StateTo != "" {
			state = event.StateTo
		}
	}
	return state
}

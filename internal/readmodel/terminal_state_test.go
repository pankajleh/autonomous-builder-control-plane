package readmodel

import (
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
)

// A branch-accepted run has finished the lifecycle this controller implements,
// so it must report as terminal carrying the exact state it reached. Reporting
// BRANCH_ACCEPTED as terminal must not claim COMPLETED: integration, merge and
// production acceptance are separate human-authorized concerns.
func TestTerminalStateCoversTheImplementedRunLifecycle(t *testing.T) {
	terminal := []domain.State{
		domain.StateCompleted,
		domain.StateBranchAccepted,
		domain.StateFailed,
		domain.StateCancelled,
	}
	for _, state := range terminal {
		if !terminalState(state) {
			t.Fatalf("%s must be terminal: the controller takes no further action for it", state)
		}
	}

	// States that still have automated successors must not be terminal.
	nonTerminal := []domain.State{
		domain.StateRunCreated,
		domain.StateAuthorityValidated,
		domain.StateExecutionStarting,
		domain.StateImplementing,
		domain.StateImplementationCompleted,
		domain.StateBranchAcceptancePending,
		domain.StateIntegrationPending,
		domain.StateIntegrating,
		domain.StateIntegrationAccepted,
		domain.StateReadyForMerge,
		domain.StateMerged,
		domain.StateProductionAcceptancePending,
		domain.StateProductionAccepted,
		domain.StateRetrying,
		domain.StateHumanDecisionRequired,
		domain.StateValidationUnavailable,
		domain.StateRecoveryRequired,
	}
	for _, state := range nonTerminal {
		if terminalState(state) {
			t.Fatalf("%s must not be terminal while it has an automated successor", state)
		}
	}
}

// BRANCH_ACCEPTED is terminal but is not COMPLETED. The distinction is the whole
// point: a consumer must be able to see the exact state reached rather than an
// implication that integration and merge happened.
func TestBranchAcceptedIsTerminalButNotCompleted(t *testing.T) {
	if !terminalState(domain.StateBranchAccepted) {
		t.Fatal("BRANCH_ACCEPTED must be terminal")
	}
	if domain.StateBranchAccepted == domain.StateCompleted {
		t.Fatal("BRANCH_ACCEPTED must remain distinct from COMPLETED")
	}
	if !terminalState(domain.StateCompleted) {
		t.Fatal("COMPLETED must remain terminal")
	}
}

package domain

import "testing"

func TestValidateTransitionHappyPath(t *testing.T) {
	path := []State{
		StateRunCreated,
		StateAuthorityValidated,
		StateExecutionStarting,
		StateImplementing,
		StateImplementationCompleted,
		StateBranchAcceptancePending,
		StateBranchAccepted,
		StateIntegrationPending,
		StateIntegrating,
		StateIntegrationAccepted,
		StateReadyForMerge,
		StateMerged,
		StateProductionAcceptancePending,
		StateProductionAccepted,
		StateCompleted,
	}
	for i := 0; i < len(path)-1; i++ {
		if err := ValidateTransition(path[i], path[i+1]); err != nil {
			t.Fatalf("expected %s -> %s to be valid: %v", path[i], path[i+1], err)
		}
	}
}

func TestValidateTransitionRejectsImplementationShortcut(t *testing.T) {
	if err := ValidateTransition(StateImplementationCompleted, StateReadyForMerge); err == nil {
		t.Fatal("expected implementation-completed shortcut to READY_FOR_MERGE to be rejected")
	}
}

func TestValidateTransitionRejectsRunCreatedToCompleted(t *testing.T) {
	if err := ValidateTransition(StateRunCreated, StateCompleted); err == nil {
		t.Fatal("expected RUN_CREATED -> COMPLETED to be rejected")
	}
}

func TestValidateTransitionRejectsBranchAcceptedToMerged(t *testing.T) {
	if err := ValidateTransition(StateBranchAccepted, StateMerged); err == nil {
		t.Fatal("expected BRANCH_ACCEPTED -> MERGED to be rejected")
	}
}

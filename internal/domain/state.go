package domain

import "fmt"

type State string

const (
	StateRunCreated                  State = "RUN_CREATED"
	StateAuthorityValidated          State = "AUTHORITY_VALIDATED"
	StateExecutionStarting           State = "EXECUTION_STARTING"
	StateImplementing                State = "IMPLEMENTING"
	StateImplementationCompleted     State = "IMPLEMENTATION_COMPLETED"
	StateBranchAcceptancePending     State = "BRANCH_ACCEPTANCE_PENDING"
	StateBranchAccepted              State = "BRANCH_ACCEPTED"
	StateIntegrationPending          State = "INTEGRATION_PENDING"
	StateIntegrating                 State = "INTEGRATING"
	StateIntegrationConflict         State = "INTEGRATION_CONFLICT"
	StateSemanticConflict            State = "SEMANTIC_CONFLICT"
	StateIntegrationAccepted         State = "INTEGRATION_ACCEPTED"
	StateReadyForMerge               State = "READY_FOR_MERGE"
	StateMerged                      State = "MERGED"
	StateProductionAcceptancePending State = "PRODUCTION_ACCEPTANCE_PENDING"
	StateProductionAccepted          State = "PRODUCTION_ACCEPTED"
	StateCompleted                   State = "COMPLETED"
	StateRetrying                    State = "RETRYING"
	StateCapacityWait                State = "CAPACITY_WAIT"
	StateRecoveryRequired            State = "RECOVERY_REQUIRED"
	StateHumanDecisionRequired       State = "HUMAN_DECISION_REQUIRED"
	StateSecretRequired              State = "SECRET_REQUIRED"
	StateExternalDependency          State = "EXTERNAL_DEPENDENCY"
	StateValidationUnavailable       State = "VALIDATION_UNAVAILABLE"
	StatePolicyBlocked               State = "POLICY_BLOCKED"
	StateFailed                      State = "FAILED"
	StateCancelled                   State = "CANCELLED"
)

var allowedTransitions = map[State]map[State]struct{}{
	StateRunCreated: {
		StateAuthorityValidated: {}, StateCancelled: {}, StateFailed: {},
	},
	StateAuthorityValidated: {
		StateExecutionStarting: {}, StatePolicyBlocked: {}, StateHumanDecisionRequired: {},
		StateSecretRequired: {}, StateExternalDependency: {}, StateCancelled: {}, StateFailed: {},
	},
	StateExecutionStarting: {
		StateImplementing: {}, StateCapacityWait: {}, StateRecoveryRequired: {},
		StateFailed: {}, StateCancelled: {},
	},
	StateImplementing: {
		StateImplementationCompleted: {}, StateRetrying: {}, StateCapacityWait: {},
		StateRecoveryRequired: {}, StateHumanDecisionRequired: {}, StateSecretRequired: {},
		StateExternalDependency: {}, StateValidationUnavailable: {}, StatePolicyBlocked: {},
		StateFailed: {}, StateCancelled: {},
	},
	StateRetrying: {
		StateExecutionStarting: {}, StateFailed: {}, StateCancelled: {},
	},
	StateCapacityWait: {
		StateExecutionStarting: {}, StateHumanDecisionRequired: {}, StateFailed: {}, StateCancelled: {},
	},
	StateRecoveryRequired: {
		StateExecutionStarting: {}, StateHumanDecisionRequired: {}, StateFailed: {}, StateCancelled: {},
	},
	StateHumanDecisionRequired: {
		StateAuthorityValidated: {}, StateFailed: {}, StateCancelled: {},
	},
	StateSecretRequired: {
		StateAuthorityValidated: {}, StateFailed: {}, StateCancelled: {},
	},
	StateExternalDependency: {
		StateAuthorityValidated: {}, StateFailed: {}, StateCancelled: {},
	},
	StateValidationUnavailable: {
		StateBranchAcceptancePending: {}, StateHumanDecisionRequired: {}, StateFailed: {}, StateCancelled: {},
	},
	StatePolicyBlocked: {
		StateHumanDecisionRequired: {}, StateFailed: {}, StateCancelled: {},
	},
	StateImplementationCompleted: {
		StateBranchAcceptancePending: {}, StateFailed: {}, StateCancelled: {},
	},
	StateBranchAcceptancePending: {
		StateBranchAccepted: {}, StateValidationUnavailable: {}, StateRetrying: {}, StateFailed: {}, StateCancelled: {},
	},
	StateBranchAccepted: {
		StateIntegrationPending: {}, StateFailed: {}, StateCancelled: {},
	},
	StateIntegrationPending: {
		StateIntegrating: {}, StateFailed: {}, StateCancelled: {},
	},
	StateIntegrating: {
		StateIntegrationAccepted: {}, StateIntegrationConflict: {}, StateSemanticConflict: {},
		StateValidationUnavailable: {}, StateFailed: {}, StateCancelled: {},
	},
	StateIntegrationConflict: {
		StateIntegrating: {}, StateHumanDecisionRequired: {}, StateFailed: {}, StateCancelled: {},
	},
	StateSemanticConflict: {
		StateHumanDecisionRequired: {}, StateFailed: {}, StateCancelled: {},
	},
	StateIntegrationAccepted: {
		StateReadyForMerge: {}, StateFailed: {}, StateCancelled: {},
	},
	StateReadyForMerge: {
		StateMerged: {}, StateFailed: {}, StateCancelled: {},
	},
	StateMerged: {
		StateProductionAcceptancePending: {}, StateCompleted: {}, StateFailed: {},
	},
	StateProductionAcceptancePending: {
		StateProductionAccepted: {}, StateValidationUnavailable: {}, StateFailed: {},
	},
	StateProductionAccepted: {
		StateCompleted: {}, StateFailed: {},
	},
}

func ValidateTransition(from, to State) error {
	if from == "" || to == "" {
		return fmt.Errorf("state transition requires non-empty from and to")
	}
	if from == to {
		return fmt.Errorf("self transition %s -> %s is not allowed", from, to)
	}
	allowed, ok := allowedTransitions[from]
	if !ok {
		return fmt.Errorf("state %s has no outgoing transitions", from)
	}
	if _, ok := allowed[to]; !ok {
		return fmt.Errorf("transition %s -> %s is not allowed", from, to)
	}
	return nil
}

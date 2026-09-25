package actionapi

import (
	"context"
	"sync"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// The resume hook must fire exactly once after a decision is durably applied,
// carrying the run id and the decision request id, so the admission controller
// can re-launch the paused run. It must fire for an "abort" as well as a
// "proceed" so an abort still terminates the run.
func TestDecisionAppliesResumeHookForEveryAnswer(t *testing.T) {
	for _, answer := range []string{"approve", "deny"} {
		t.Run(answer, func(t *testing.T) {
			fixture := newActionFixture(t, true, "decision.authority")
			defer fixture.close()

			var mu sync.Mutex
			var invocations [][2]string
			hookController, err := NewController(ControllerConfig{
				Catalog: fixture.catalog, ReadModel: fixture.model, Journal: fixture.journal, Authority: fixture.matcher,
				ResumeHook: func(_ context.Context, runID, decisionRequestID string) error {
					mu.Lock()
					invocations = append(invocations, [2]string{runID, decisionRequestID})
					mu.Unlock()
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer hookController.Close()

			snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
			if err != nil {
				t.Fatal(err)
			}
			principal := serviceapi.Principal{PrincipalID: "repo-c-service", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test"}
			command := serviceapi.CommandEnvelopeV1{SchemaVersion: 1, RequestID: "decision-request-" + answer, AttemptID: fixture.attemptID,
				ExpectedState: string(domain.StateHumanDecisionRequired), ExpectedRevision: snapshot.Projection.ProjectionRevision, Reason: "record reviewed answer",
				DelegatedActor: &serviceapi.DelegatedActorV1{SubjectID: "human-1", SubjectType: serviceapi.PrincipalUser},
				Payload:        mustJSON(t, DecisionPayloadV1{DecisionRequestID: fixture.blockerID, Answer: answer})}

			status, err := hookController.AdmitAction(context.Background(), principal, fixture.runID, serviceapi.ActionDecision, command)
			if err != nil || status.OperationID == "" {
				t.Fatalf("decision admission = %+v, err=%v", status, err)
			}
			_ = awaitActionStatus(t, hookController, principal, fixture.runID, status.OperationID, "APPLIED", "RECONCILED_APPLIED")

			mu.Lock()
			count := len(invocations)
			var first [2]string
			if count > 0 {
				first = invocations[0]
			}
			mu.Unlock()
			if count != 1 {
				t.Fatalf("resume hook invocations = %d, want 1", count)
			}
			if first[0] != fixture.runID || first[1] != fixture.blockerID {
				t.Fatalf("resume hook args = %v, want [%s %s]", first, fixture.runID, fixture.blockerID)
			}
		})
	}
}

// A nil resume hook must record the decision without error (the run stays
// paused, to be resumed by some external mechanism).
func TestDecisionWithNilResumeHookRecordsOnly(t *testing.T) {
	fixture := newActionFixture(t, true, "decision.authority")
	defer fixture.close()

	snapshot, err := fixture.model.Snapshot(context.Background(), fixture.runID)
	if err != nil {
		t.Fatal(err)
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
}

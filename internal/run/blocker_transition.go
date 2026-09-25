package run

import (
	"context"
	"errors"
	"fmt"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

// isHumanDecisionFailure reports whether the classifier escalates one Ralphex
// failure to HUMAN_DECISION_REQUIRED. It is the caller's gate: only failures
// that return true are paused for a decision; every other failure keeps its
// original terminal behaviour.
func isHumanDecisionFailure(process supervisor.Result, processErr error) bool {
	return classificationOfRalphexFailure(process, processErr).RecommendedState == domain.StateHumanDecisionRequired
}

// blockHumanDecision records the blocker decision (which is itself the
// IMPLEMENTING -> HUMAN_DECISION_REQUIRED transition) and leaves the run paused
// awaiting an inline human decision. It assumes the caller has already confirmed
// isHumanDecisionFailure; a construction/append failure fails the run with the
// original cause so the pause path can never silently succeed.
func (r *Runner) blockHumanDecision(ctx context.Context, result Result, process supervisor.Result, processErr error, refs []ledger.EvidenceRef) (Result, error) {
	decision, err := r.humanDecisionBlockerBuild(domain.StateImplementing, process, processErr, refs)
	if err != nil {
		return r.fail(ctx, result, domain.StateImplementing, "ralphex-adapter", err, refs)
	}
	event, err := decision.Event("ralphex-adapter")
	if err != nil {
		return r.fail(ctx, result, domain.StateImplementing, "ralphex-adapter", err, refs)
	}
	if err := r.appendBlockedTransition(ctx, event); err != nil {
		return r.fail(ctx, result, domain.StateImplementing, "ralphex-adapter", err, refs)
	}
	result.State = domain.StateHumanDecisionRequired
	result.FailureReason = string(decision.Classification().Class)
	return result, nil
}

// appendBlockedTransition appends one already-validated blocker decision event
// under the run-transition lease, verifying that the reconstructed current state
// still matches the event's source state. The blocker event is itself the state
// transition, so chronology is preserved without a second STATE_TRANSITION.
func (r *Runner) appendBlockedTransition(ctx context.Context, event ledger.Event) error {
	if ctx == nil {
		return errors.New("transition context is required")
	}
	if event.EventType == "" || event.StateFrom == "" || event.StateTo == "" {
		return errors.New("blocker decision event must carry a state transition")
	}
	atomic, ok := r.events.(atomicTransitionEventAppender)
	if !ok {
		return errors.New("atomic run-transition appender is required")
	}
	lease, err := atomic.AcquireRunTransition(r.governed.RunID())
	if err != nil {
		return fmt.Errorf("acquire blocker transition selection: %w", err)
	}
	defer func() { _ = lease.Close() }()
	current, err := r.currentStateUnderTransitionLease(atomic)
	if err != nil {
		return err
	}
	if current != event.StateFrom {
		return fmt.Errorf("run transition selection advanced from %s to %s", event.StateFrom, current)
	}
	if err := atomic.AppendOrVerifyLeased(event, lease); err != nil {
		return fmt.Errorf("append blocker decision: %w", err)
	}
	return nil
}

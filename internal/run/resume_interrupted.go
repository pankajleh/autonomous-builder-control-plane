package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runfailure"
)

// R6's resume (Repo C BUILD_RELIABILITY.md §5.6; docs/plans/build-recovery.md §6.2). A build is interrupted when the
// host restarts, the kernel's out-of-memory killer takes abcp run, or someone kills it; it was a run that ended
// CANCELLED (SIGTERM) or one left in IMPLEMENTING with no owner (SIGKILL). A run whose profile allows a fresh attempt
// (ralphex.fresh_attempts, R3) now goes on instead, once:
//
//   - abcp run, sent SIGTERM (not an API cancellation) while its builder runs, records IMPLEMENTING -> RECOVERY_REQUIRED
//     with R1's cause INTERRUPTED and recovery RESUME, in place of CANCELLED;
//   - abcp serve finds the registered run whose owner is gone (internal/runadmission's resume pass) and launches abcp run
//     --resume-interrupted for it once (RunResumeInterrupted), which takes the catalog's ownership over, records
//     RECOVERY_REQUIRED first when the run stopped without it, and starts Ralphex again as R3's fresh attempt does:
//     RECOVERY_REQUIRED -> EXECUTION_STARTING (its start evidence names the attempt and resume_after INTERRUPTED) ->
//     IMPLEMENTING, the retained worktree removed first (inside the wall), the attempt numbered from the ledger, its
//     Ralphex evidence and walled plan copy named -fresh-<attempt>;
//   - one resume per run, ever: a run the ledger shows resumed once ends CANCELLED (INTERRUPTED) at the next SIGTERM, as
//     before, and is never launched again; nor is a run its owner asked to stop (an API cancellation).

// RecoveryResume is the recovery a RECOVERY_REQUIRED transition names when the run may be resumed after an interruption.
const RecoveryResume = "RESUME"

// The interruption a RECOVERY_REQUIRED transition names: the signal abcp run received, or its owner gone without one.
const (
	InterruptionSIGTERM   = "SIGTERM"
	InterruptionOwnerGone = "OWNER_GONE"
)

// ErrNotResumable says abcp run --resume-interrupted found nothing it may resume; it recorded nothing.
var ErrNotResumable = errors.New("RESUME_NOT_ALLOWED: the run cannot be resumed after an interruption")

// signalCause is the cause abcp run cancels its run with when the process receives a signal (NewSignalCause).
type signalCause struct{ signal string }

func (c *signalCause) Error() string { return "abcp run received " + c.signal }

// NewSignalCause is the cause abcp run cancels its run's context with when the process receives sig. Only SIGTERM (the
// host going down, a plain kill) lets a run that may be resumed stop at RECOVERY_REQUIRED; SIGINT (a person at the
// terminal) and any other signal stop it as before.
func NewSignalCause(sig os.Signal) error {
	name := "a signal"
	if s, ok := sig.(syscall.Signal); ok && s == syscall.SIGTERM {
		name = InterruptionSIGTERM
	} else if sig != nil {
		name = sig.String()
	}
	return &signalCause{signal: name}
}

// interruptedBySigterm reports a context ended by SIGTERM, not by an API cancellation (which has a cause of its own).
func interruptedBySigterm(ctx context.Context) bool {
	if ctx == nil || ctx.Err() == nil {
		return false
	}
	var cause *signalCause
	return errors.As(context.Cause(ctx), &cause) && cause.signal == InterruptionSIGTERM
}

// Interruption is what a run's ledger says about resuming it after an interruption.
type Interruption struct {
	// State is the run's current state.
	State domain.State
	// Attempts counts the builder attempts started (EXECUTION_STARTING edges).
	Attempts int
	// Resumed: the run was resumed once already (a RECOVERY_REQUIRED -> EXECUTION_STARTING edge).
	Resumed bool
	// CancelRequested: someone asked the run to stop (an API cancellation request); it is never resumed.
	CancelRequested bool
	// FreshAttemptUsed: R3's fresh attempt ran (a RETRYING edge), so the resumed run gets none.
	FreshAttemptUsed bool
	// LastEvent is when the run's ledger last recorded anything.
	LastEvent time.Time
	// recoveryResume: the run's RECOVERY_REQUIRED names recovery RESUME (only this package records one).
	recoveryResume bool
}

// InterruptionOf reads a run's events, in their order.
func InterruptionOf(events []ledger.Event) Interruption {
	var facts Interruption
	for _, event := range events {
		if event.Timestamp.After(facts.LastEvent) {
			facts.LastEvent = event.Timestamp
		}
		if event.EventType == "API_CANCEL_REQUESTED" {
			facts.CancelRequested = true
		}
		if event.EventType == string(domain.StateRunCreated) && event.StateFrom == "" {
			facts.State = domain.StateRunCreated
			continue
		}
		if event.StateTo == "" {
			continue
		}
		facts.State = event.StateTo
		switch event.StateTo {
		case domain.StateExecutionStarting:
			facts.Attempts++
			if event.StateFrom == domain.StateRecoveryRequired {
				facts.Resumed = true
			}
		case domain.StateRetrying:
			facts.FreshAttemptUsed = true
		case domain.StateRecoveryRequired:
			recovery, _ := event.Payload["recovery"].(string)
			facts.recoveryResume = recovery == RecoveryResume
		}
	}
	return facts
}

// Resumable: the ledger allows the run its one resume. It stopped while its builder was being started or ran, either at
// the RECOVERY_REQUIRED a SIGTERM recorded or with no word at all (its owner killed outright), it was never resumed
// before, and nobody asked it to stop.
func (i Interruption) Resumable() bool {
	if i.Resumed || i.CancelRequested || i.Attempts < 1 {
		return false
	}
	switch i.State {
	case domain.StateExecutionStarting, domain.StateImplementing:
		return true
	case domain.StateRecoveryRequired:
		return i.recoveryResume
	}
	return false
}

// Settled: the run can never become resumable, whatever happens to it next (abcp serve stops reading it).
func (i Interruption) Settled() bool {
	if i.Resumed || i.CancelRequested {
		return true
	}
	switch i.State {
	case domain.StateFailed, domain.StateCancelled, domain.StateBranchAccepted, domain.StateIntegrationPending, domain.StateIntegrating,
		domain.StateIntegrationConflict, domain.StateSemanticConflict, domain.StateIntegrationAccepted, domain.StateReadyForMerge,
		domain.StateMerged, domain.StateProductionAcceptancePending, domain.StateProductionAccepted, domain.StateCompleted:
		return true
	case domain.StateRecoveryRequired:
		return !i.recoveryResume
	}
	return false
}

// ResumesAfterInterruption reports whether a run of manifest may be resumed after an interruption: its profile allows a
// fresh attempt (ralphex.fresh_attempts, R3, which needs Ralphex's pinned task prompt), and it is a worktree run that
// implements with Ralphex (not a review, not an exact edit).
func ResumesAfterInterruption(manifest authority.Manifest) bool {
	return manifest.Ralphex.FreshAttempts >= 1 && manifest.Worktree.Enabled && manifest.ExactEdit == nil && manifest.Ralphex.Mode != ralphex.ModeReview
}

// resumeGate is ResumesAfterInterruption for this runner's authority, outside V3 governed bounds.
func (r *Runner) resumeGate() bool {
	return ResumesAfterInterruption(r.governed.Manifest()) && r.parsedCapsule.PolicyVersion != contextcapsule.PolicyVersionV3
}

// RunResumeInterrupted resumes a run whose builder was interrupted (abcp run --resume-interrupted): it goes on from
// RECOVERY_REQUIRED as a fresh attempt, through branch acceptance. A run the ledger or its profile does not allow a
// resume is refused with ErrNotResumable, nothing recorded.
func (r *Runner) RunResumeInterrupted(ctx context.Context) (Result, error) {
	r.interrupted = true
	return r.Run(ctx)
}

// beginResumeAfterInterruption checks the run may be resumed and, when its owner stopped without a word, records the
// RECOVERY_REQUIRED a SIGTERM would have. It answers the attempt the resume is.
func (r *Runner) beginResumeAfterInterruption(ctx context.Context) (int, error) {
	if !r.resumeGate() {
		return 0, fmt.Errorf("%w: its profile allows no fresh attempt, or it is not a worktree run that implements", ErrNotResumable)
	}
	snapshotter, ok := r.events.(eventSnapshotter)
	if !ok {
		return 0, errors.New("durable ledger snapshot is required for a resume")
	}
	events, err := readRunEvents(snapshotter)
	if err != nil {
		return 0, fmt.Errorf("read run events for a resume: %w", err)
	}
	facts := InterruptionOf(events)
	if !facts.Resumable() {
		return 0, fmt.Errorf("%w: the run is %s (resumed before %t, a stop asked for %t)", ErrNotResumable, facts.State, facts.Resumed, facts.CancelRequested)
	}
	r.freshAttemptUsed = facts.FreshAttemptUsed
	r.attempt = facts.Attempts
	if facts.State != domain.StateRecoveryRequired {
		payload, _ := r.withFailureDetail(map[string]any{"interruption": InterruptionOwnerGone},
			r.failureDetail(runfailure.CauseInterrupted, runfailure.PhaseFrom(facts.State)), nil, nil)
		payload["recovery"], payload["next_attempt"] = RecoveryResume, facts.Attempts+1
		if err := r.transition(ctx, facts.State, domain.StateRecoveryRequired, "resume-controller", payload, nil); err != nil {
			return 0, err
		}
	}
	return facts.Attempts + 1, nil
}

// resumableInterruption: the builder's run ended because abcp run received SIGTERM (not an API cancellation), and the
// run may stop at RECOVERY_REQUIRED for abcp serve to resume it, as its profile allows, once, and not after a stop was
// asked for.
func (r *Runner) resumableInterruption(ctx context.Context, invocation ralphex.Invocation, walled, planHandoff bool) bool {
	if !interruptedBySigterm(ctx) || !r.resumeGate() || !r.attemptCanStartAgain(invocation, walled, planHandoff) {
		return false
	}
	snapshotter, ok := r.events.(eventSnapshotter)
	if !ok {
		return false
	}
	events, err := readRunEvents(snapshotter)
	if err != nil {
		return false
	}
	facts := InterruptionOf(events)
	return !facts.Resumed && !facts.CancelRequested
}

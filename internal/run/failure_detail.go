package run

import (
	"context"
	"errors"
	"fmt"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/acceptance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/blocker"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/openmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runfailure"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

// R1 (Repo C design note BUILD_RELIABILITY.md §5.6; docs/plans/build-recovery.md): every transition that ends a run
// without an accepted branch (FAILED, CANCELLED, VALIDATION_UNAVAILABLE) records a runfailure.Detail in its payload,
// under runfailure.PayloadKey, and, when there is a failing check's output or the builder's last error to keep, a
// runfailure.Evidence body beside its other evidence. GET /v1/runs/{run}/failure reads both back.

const (
	// maxRalphexStdoutRead and maxRalphexStderrRead bound what is read of Ralphex's captured streams to find how it
	// ended (each stream is captured up to supervisor.DefaultOutputLimitBytes).
	maxRalphexStdoutRead = 16 << 20
	maxRalphexStderrRead = 1 << 20
	// maxFailureDetails bounds the failure-detail bodies of one run.
	maxFailureDetails = 16
)

// failureDetail is the run's own part of every detail: its engine and the budgets it ran with.
func (r *Runner) failureDetail(cause runfailure.Cause, phase runfailure.Phase) runfailure.Detail {
	policy := r.governed.Executor()
	detail := runfailure.Detail{Cause: cause, Phase: phase, Engine: policy.Executor, Attempts: max(r.attempt, 1), TaskRetryLimit: r.taskRetryLimit()}
	if policy.Provider != nil {
		detail.Provider = policy.Provider.ID
	}
	return detail
}

// taskRetryLimit is how often Ralphex retries a task after the builder signals FAILED: the profile's
// ralphex.task_retry_count (R2), else Ralphex's own default, 1.
func (r *Runner) taskRetryLimit() int {
	if retries := r.governed.Ralphex().TaskRetryCount; retries > 0 {
		return retries
	}
	return 1
}

// stopDetail is the detail of a stop by one of ABCP's own steps (fail, cancel), from the step and its cause.
func (r *Runner) stopDetail(ctx context.Context, from domain.State, source string, cause error, cancelled bool) runfailure.Detail {
	phase := runfailure.PhaseFrom(from)
	if cancelled {
		if _, api := APICancelProvenance(ctx); api {
			return r.failureDetail(runfailure.CauseCancelled, phase)
		}
		return r.failureDetail(runfailure.CauseInterrupted, phase)
	}
	var unavailable *openmodel.UnavailableError
	switch {
	case errors.Is(cause, ErrExactEditDoesNotApply):
		return r.failureDetail(runfailure.CauseExactEditDoesNotApply, phase)
	case errors.Is(cause, ErrBuildWallUnavailable), errors.Is(cause, ErrBranchRewritten), source == "build-wall":
		return r.failureDetail(runfailure.CauseSandbox, phase)
	case source == "open-model-engine" && errors.As(cause, &unavailable):
		return r.failureDetail(runfailure.CauseProviderLimit, phase)
	case source == "open-model-engine":
		return r.failureDetail(runfailure.CauseProviderRefused, phase)
	case source == "authority-validator" || source == "resume-controller":
		return r.failureDetail(runfailure.CauseAdmissionFailed, phase)
	case ledgerSource.MatchString(source):
		return r.failureDetail(runfailure.CauseControllerError, phase)
	default:
		return r.failureDetail(runfailure.CauseUnclassified, phase)
	}
}

// ralphexEnding is the detail of a Ralphex run that did not succeed, read from Ralphex's own lines, and the evidence
// body that keeps the failing check's output and the error Ralphex ended with.
func (r *Runner) ralphexEnding(ctx context.Context, process supervisor.Result, classification blocker.Classification, cancelled bool) (runfailure.Detail, *runfailure.Evidence) {
	stdout, _ := readBoundedEvidenceTail(process.StdoutRef, maxRalphexStdoutRead)
	stderr, _ := readBoundedEvidenceTail(process.StderrRef, maxRalphexStderrRead)
	transcript := runfailure.ReadRalphex(stdout, stderr)
	ending := runfailure.EndingOf(transcript, string(classification.Class))
	switch {
	case cancelled:
		ending.Cause = r.stopDetail(ctx, domain.StateImplementing, "ralphex-adapter", nil, true).Cause
	case process.Outcome == supervisor.OutcomeTimedOut:
		ending.Cause = runfailure.CauseTimeout
	case process.Outcome == supervisor.OutcomeSignaled:
		ending.Cause = runfailure.CauseInterrupted
	case process.Outcome == supervisor.OutcomeOutputLimit || process.Outcome == supervisor.OutcomeWaitDelay:
		ending.Cause = runfailure.CauseControllerError
	}
	detail := r.failureDetail(ending.Cause, ending.Phase)
	detail.RepairTurns, detail.TaskRetries = transcript.RepairTurns, transcript.TaskRetries
	body := &runfailure.Evidence{SchemaVersion: runfailure.EvidenceSchemaVersion, Cause: ending.Cause}
	if failure := ending.Failure; failure != nil && runfailure.ValidCheck(failure.Check) {
		detail.Check, detail.CheckState = failure.Check, failure.State
		if failure.State == runfailure.CheckExited {
			code := failure.ExitCode
			detail.CheckExitCode = &code
		}
		body.Check = failure.Check
		body.CheckOutput, body.CheckOutputTruncated = runfailure.Clean(failure.Output, runfailure.MaxCheckOutputBytes)
	}
	body.LastError, body.LastErrorTruncated = runfailure.Clean(transcript.FinalError, runfailure.MaxLastErrorBytes)
	if body.CheckOutput == "" && body.LastError == "" {
		return detail, nil
	}
	return detail, body
}

// acceptanceEnding is the detail of a branch that failed ABCP's final acceptance: its first failing required command,
// and that command's own output.
func (r *Runner) acceptanceEnding(accepted acceptance.Result) (runfailure.Detail, *runfailure.Evidence) {
	detail := r.failureDetail(runfailure.CauseAcceptanceFailed, runfailure.PhaseAcceptance)
	body := &runfailure.Evidence{SchemaVersion: runfailure.EvidenceSchemaVersion, Cause: runfailure.CauseAcceptanceFailed}
	for _, command := range accepted.Commands() {
		if !command.Required || command.Process.Outcome == supervisor.OutcomeSucceeded {
			continue
		}
		check := acceptanceCheck(command)
		detail.Check = check
		switch command.Process.Outcome {
		case supervisor.OutcomeTimedOut:
			detail.CheckState = runfailure.CheckTimedOut
		default:
			detail.CheckState = runfailure.CheckExited
			code := command.Process.ExitCode
			if code < -1 || code > 255 {
				code = -1
			}
			detail.CheckExitCode = &code
		}
		body.Check = check
		body.CheckOutput, body.CheckOutputTruncated = runfailure.Clean(commandOutput(command), runfailure.MaxCheckOutputBytes)
		break
	}
	if body.CheckOutput == "" {
		return detail, nil
	}
	return detail, body
}

// withFailureDetail adds detail to a stop transition's payload and, when there is one, its evidence body to refs.
func (r *Runner) withFailureDetail(payload map[string]any, detail runfailure.Detail, body *runfailure.Evidence, refs []ledger.EvidenceRef) (map[string]any, []ledger.EvidenceRef) {
	result := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		result[key] = value
	}
	result[runfailure.PayloadKey] = detail.Payload()
	if body == nil || r.artifacts == nil {
		return result, refs
	}
	for i := 1; i <= maxFailureDetails; i++ {
		name := runfailure.EvidenceName
		if i > 1 {
			name = fmt.Sprintf("failure-detail-%d.json", i)
		}
		ref, err := r.writeJSON(name, runfailure.EvidenceKind, body)
		if err == nil {
			return result, append(append([]ledger.EvidenceRef{}, refs...), ref)
		}
		if !errors.Is(err, evidence.ErrArtifactExists) {
			break
		}
	}
	return result, refs
}

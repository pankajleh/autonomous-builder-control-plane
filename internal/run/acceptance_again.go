package run

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/acceptance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runfailure"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

// R5, acceptance run again once (Repo C BUILD_RELIABILITY.md §5.6; docs/plans/build-recovery.md §6). When the profile
// says acceptance_reruns: 1, the run's own governed validation passed on its last run (Ralphex's "validation completed
// successfully" for each of its checks, none failing after), and ABCP's final acceptance then fails a required command
// by its exit, the acceptance runs once more on a fresh checkout of the same candidate, its evidence named beside the
// first run's. Passing then, the branch is accepted, its BRANCH_ACCEPTED payload says acceptance_runs 2 and which check
// was flaky, and acceptance-flaky.json keeps the first failure (R1 records nothing: the run did not fail). Failing again,
// the run fails as before, with the second run's output. A timeout, a signal, a dirty checkout or an acceptance that
// could not run is not run again.

const (
	// AcceptanceFlakyKind is the evidence kind of the first failure of an acceptance that passed when run again.
	AcceptanceFlakyKind = "acceptance-flaky"
	// AcceptanceFlakySchemaVersion is the schema of that evidence.
	AcceptanceFlakySchemaVersion = "AcceptanceFlakyV1"
	// AcceptanceFlakyWords are its fixed words, as the kits say it of a test file (Repo C BUILD_RELIABILITY.md §5.1).
	AcceptanceFlakyWords = "FLAKY: failed, then passed when run again"
)

// AcceptanceFlakyV1 is the first failure of an acceptance that passed when run again: the check, how it ended, and its
// own output, redacted and bounded as R1 bounds a check's output.
type AcceptanceFlakyV1 struct {
	SchemaVersion        string             `json:"schema_version"`
	Words                string             `json:"words"`
	Check                string             `json:"check"`
	CommandIndex         int                `json:"command_index"`
	FirstOutcome         supervisor.Outcome `json:"first_outcome"`
	FirstExitCode        int                `json:"first_exit_code"`
	CheckOutput          string             `json:"check_output,omitempty"`
	CheckOutputTruncated bool               `json:"check_output_truncated,omitempty"`
}

// ownValidationPassed reports whether the attempt's own governed checks were each last reported passing by Ralphex.
// Read only when the profile allows an acceptance rerun.
func (r *Runner) ownValidationPassed(process supervisor.Result) bool {
	if r.governed.AcceptanceReruns() < 1 {
		return false
	}
	var ids []string
	for _, command := range r.governed.Ralphex().Validation {
		ids = append(ids, command.ID)
	}
	stdout, err := readBoundedEvidenceTail(process.StdoutRef, maxRalphexStdoutRead)
	if err != nil {
		return false
	}
	return runfailure.ReadRalphex(stdout, nil).ValidationPassed(ids)
}

// acceptanceFailedByExit is the first failing required command of an acceptance that failed by that command's exit;
// false for any other ending (a timeout, a signal, a dirty checkout).
func acceptanceFailedByExit(accepted acceptance.Result) (acceptance.CommandResult, bool) {
	if accepted.Status() != acceptance.StatusFail {
		return acceptance.CommandResult{}, false
	}
	for _, command := range accepted.Commands() {
		if command.Required && command.Process.Outcome != supervisor.OutcomeSucceeded {
			return command, command.Process.Outcome == supervisor.OutcomeExited
		}
	}
	return acceptance.CommandResult{}, false
}

// acceptanceCheck is the name R1 records for an acceptance command: its own when it is an identifier, else
// acceptance-<n>.
func acceptanceCheck(command acceptance.CommandResult) string {
	if runfailure.ValidCheck(command.Name) {
		return command.Name
	}
	return fmt.Sprintf("acceptance-%d", command.Index)
}

// commandOutput is an acceptance command's own output, standard error first, read back from its evidence.
func commandOutput(command acceptance.CommandResult) string {
	stderr, _ := readBoundedEvidenceTail(command.Process.StderrRef, runfailure.MaxEvidenceBytes*4)
	stdout, _ := readBoundedEvidenceTail(command.Process.StdoutRef, runfailure.MaxEvidenceBytes*4)
	output := strings.TrimSpace(string(stderr))
	if out := strings.TrimSpace(string(stdout)); out != "" {
		output = strings.TrimSpace(output + "\n" + out)
	}
	return output
}

// acceptAgain runs the acceptance once more on a fresh checkout of the candidate the first run accepted from: the same
// branch at the same commit, or the second run is unavailable.
func (r *Runner) acceptAgain(ctx context.Context, first acceptance.Target) (acceptance.Result, error) {
	target, cleanup, err := r.prepareAcceptanceTarget(ctx)
	if err != nil {
		err = fmt.Errorf("prepare the acceptance's second run: %w", err)
		return acceptance.UnavailableResult(err.Error()), err
	}
	if target.Branch != first.Branch || target.HeadSHA != first.HeadSHA {
		err = fmt.Errorf("the candidate moved between the acceptance's two runs: %s at %s, then %s at %s",
			first.Branch, first.HeadSHA, target.Branch, target.HeadSHA)
		return acceptance.UnavailableResult(err.Error()), errors.Join(err, cleanup())
	}
	accepted, err := acceptance.New(r.processes, r.artifacts).Again().Run(ctx, r.governed, target)
	return accepted, errors.Join(err, cleanup())
}

// writeAcceptanceFlaky keeps the first failure of an acceptance that passed when run again.
func (r *Runner) writeAcceptanceFlaky(first acceptance.CommandResult) (ledger.EvidenceRef, error) {
	body := AcceptanceFlakyV1{
		SchemaVersion: AcceptanceFlakySchemaVersion, Words: AcceptanceFlakyWords, Check: acceptanceCheck(first),
		CommandIndex: first.Index, FirstOutcome: first.Process.Outcome, FirstExitCode: first.Process.ExitCode,
	}
	body.CheckOutput, body.CheckOutputTruncated = runfailure.Clean(commandOutput(first), runfailure.MaxCheckOutputBytes)
	return r.writeJSON("acceptance-flaky.json", AcceptanceFlakyKind, body)
}

// acceptanceRuns adds acceptance_runs to an acceptance ending's payload when the acceptance ran twice.
func acceptanceRuns(payload map[string]any, runs int) map[string]any {
	if runs > 1 {
		payload["acceptance_runs"] = runs
	}
	return payload
}

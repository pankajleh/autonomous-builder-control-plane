package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runfailure"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

// R3, a fresh second attempt (Repo C BUILD_RELIABILITY.md §5.6; docs/plans/build-recovery.md §5). When the profile says
// ralphex.fresh_attempts: 1 and the first attempt's builder ended without finishing (R1's VALIDATION_FAILED or
// MODEL_SIGNALLED_FAILED: Ralphex's own repair turns and retries used), the run starts Ralphex once more on its branch:
//
//   - IMPLEMENTING -> RETRYING records how the first attempt ended (its failure_detail, recovery FRESH_ATTEMPT), then
//     RETRYING -> EXECUTION_STARTING -> IMPLEMENTING as for the first; R1's attempts says 2 from then on;
//   - the first attempt's retained worktree is removed first (Ralphex refuses to start where one is), inside the wall
//     for a walled run, whose repository is the build's to write; Ralphex then adds the worktree again from the branch,
//     so every commit of the first attempt is kept, at the same path, and the activity binding resolves as before;
//   - Ralphex appends that start to the same progress file (a failed run's "Failed:" footer is never archived, its
//     issue #288), so the activity provider keeps one session; a start in place, without a worktree, would add a
//     second progress session the collector refuses;
//   - the task prompt is Ralphex's own with what the first attempt ended with after it (ralphex.FreshAttemptPrompt);
//   - a walled run's plan copy has a folder of its own for the attempt (walledRun.executionPlanFor).

// firstAttempt is how the first attempt ended, for the fresh attempt's prompt.
type firstAttempt struct {
	detail runfailure.Detail
	body   *runfailure.Evidence
}

// maxFreshAttemptCheckOutput bounds the first attempt's check output in the fresh attempt's prompt.
const maxFreshAttemptCheckOutput = 2 << 10

// freshAttemptWorktreeRemoval bounds removing the first attempt's worktree.
const freshAttemptWorktreeRemoval = 2 * time.Minute

// freshAttemptEligible: the profile allows a fresh attempt, the run is a worktree run that implements outside V3
// bounds, the first attempt's builder ended without finishing, and its plan can be handed to Ralphex again (a walled
// run's copy gets a folder of its own; an unwalled run's untracked copy would be the same file again).
func (r *Runner) freshAttemptEligible(invocation ralphex.Invocation, walled, planHandoff bool, detail runfailure.Detail) bool {
	if detail.Cause != runfailure.CauseValidationFailed && detail.Cause != runfailure.CauseModelSignalledFailed {
		return false
	}
	return r.attemptCanStartAgain(invocation, walled, planHandoff)
}

// attemptCanStartAgain: Ralphex can be started once more on the run's branch (R3's fresh attempt, R6's resume). The
// profile allows a fresh attempt, the run is a worktree run that implements outside V3 bounds, and its plan can be
// handed to Ralphex again (a walled run's copy gets a folder of its own; an unwalled run's untracked copy would be the
// same file again).
func (r *Runner) attemptCanStartAgain(invocation ralphex.Invocation, walled, planHandoff bool) bool {
	if r.governed.Ralphex().FreshAttempts < 1 || !r.governed.Worktree().Enabled || invocation.Bounds != nil || invocation.Mode == ralphex.ModeReview {
		return false
	}
	return walled || !planHandoff
}

// freshAttemptSummary is what the fresh attempt's prompt says about the first: fixed words for its codes, and the
// check's own output (already redacted and bounded by R1), at most 2 KiB of it.
func freshAttemptSummary(detail runfailure.Detail, body *runfailure.Evidence) string {
	var lines []string
	switch detail.Cause {
	case runfailure.CauseValidationFailed:
		lines = append(lines, "- How it ended: a governed validation check still failed when the attempt ended.")
	default:
		lines = append(lines, "- How it ended: the builder signalled FAILED with no failing check pending.")
	}
	if detail.Check != "" {
		state := "failed"
		switch {
		case detail.CheckState == runfailure.CheckTimedOut:
			state = "timed out"
		case detail.CheckState == runfailure.CheckStalled:
			state = "stalled (no output within its stall timeout)"
		case detail.CheckExitCode != nil:
			state = fmt.Sprintf("exited %d", *detail.CheckExitCode)
		}
		lines = append(lines, fmt.Sprintf("- The check: %s, which %s.", detail.Check, state))
	}
	if detail.Phase == runfailure.PhaseReview {
		lines = append(lines, "- It failed after a review pass changed the code.")
	}
	lines = append(lines, fmt.Sprintf("- Turns given a failed check's evidence: %d. Retries after the builder signalled FAILED: %d of %d.",
		detail.RepairTurns, detail.TaskRetries, max(detail.TaskRetryLimit, 1)))
	if body != nil && body.CheckOutput != "" && body.Check == detail.Check {
		output, _ := runfailure.Clean(body.CheckOutput, maxFreshAttemptCheckOutput)
		lines = append(lines, "- The check's last output:", output)
	}
	return strings.Join(lines, "\n")
}

// retainedWorktree is where Ralphex keeps the run's worktree: <repository>/.ralphex/worktrees/<branch>.
func (r *Runner) retainedWorktree(repository string) string {
	return filepath.Join(repository, ".ralphex", "worktrees", filepath.FromSlash(r.governed.Worktree().Branch))
}

// removeRetainedWorktree removes the first attempt's worktree before the fresh attempt starts, with Git's own
// "worktree remove --force" and "worktree prune": inside the wall (wallArgs) for a walled run, whose repository is the
// build's to write, else in the repository itself. Nothing is removed when Ralphex kept no worktree there. The
// branch keeps every commit; only the checkout of the first attempt (and anything it left uncommitted) goes.
func (r *Runner) removeRetainedWorktree(ctx context.Context, repository string, wallArgs, environment []string) error {
	worktree := r.retainedWorktree(repository)
	if err := realDirectory(repository); err != nil {
		return fmt.Errorf("the run's repository is not a real folder: %w", err)
	}
	commands := [][]string{{"git", "worktree", "prune"}}
	if _, err := os.Lstat(worktree); err == nil {
		commands = [][]string{{"git", "worktree", "remove", "--force", worktree}, {"git", "worktree", "prune"}}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect the first attempt's worktree: %w", err)
	}
	for index, args := range commands {
		if wallArgs == nil {
			if _, err := gitOutput(ctx, repository, args[1:]...); err != nil {
				return fmt.Errorf("remove the first attempt's worktree: %w", err)
			}
			continue
		}
		name := fmt.Sprintf("fresh-attempt-worktree-%d", index+1)
		process, err := r.processes.Run(ctx, supervisor.Command{
			Argv: append(append([]string{}, wallArgs...), args...), Cwd: repository, Env: environment, Timeout: freshAttemptWorktreeRemoval,
			Stdout: supervisor.EvidenceSink{Writer: r.artifacts, Name: r.ralphexArtifactName(name + "-stdout.log"), Kind: "fresh-attempt-worktree-stdout"},
			Stderr: supervisor.EvidenceSink{Writer: r.artifacts, Name: r.ralphexArtifactName(name + "-stderr.log"), Kind: "fresh-attempt-worktree-stderr"},
		})
		if err != nil || process.Outcome != supervisor.OutcomeSucceeded {
			return fmt.Errorf("%w: remove the first attempt's worktree inside the wall: %s exit %d: %v", ErrBuildWallUnavailable, process.Outcome, process.ExitCode, err)
		}
	}
	if _, err := os.Lstat(worktree); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("the first attempt's worktree is still there after its removal: %v", err)
	}
	return nil
}

func copyStrings(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

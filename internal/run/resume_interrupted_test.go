package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runfailure"
)

// resumeRalphex is freshAttemptRalphex's Ralphex stand-in (the pinned Ralphex's worktree rules) for interruptions: a
// start listed in hang commits its work in progress, says it is ready (<counter>.ready-<n>) and runs until it is
// stopped; one listed in fail fails as the Thali change did; any other commits candidate.txt.
func resumeRalphex(counter, hang, fail string) string {
	return fmt.Sprintf(`#!/bin/sh
config="$2"
branch=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--branch" ]; then branch="$2"; shift 2; else shift; fi
done
wt=".ralphex/worktrees/$branch"
if [ -e "$wt" ]; then
  printf 'error: create worktree: worktree already exists at %%s, another instance may be running\n' "$wt" >&2
  exit 1
fi
mkdir -p .ralphex/worktrees
grep -qx '.ralphex/' .git/info/exclude 2>/dev/null || echo '.ralphex/' >> .git/info/exclude
if git rev-parse --verify -q "refs/heads/$branch" > /dev/null; then
  git worktree add -q "$wt" "$branch" || exit 20
else
  git worktree add -q -b "$branch" "$wt" HEAD || exit 20
fi
n=$(cat %[1]s 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > %[1]s
[ -f "$config/prompts/task.txt" ] && cp "$config/prompts/task.txt" %[1]s.prompt-$n
case " %[2]s " in *" $n "*)
  printf "attempt $n\n" > "$wt/attempt-$n.txt"
  git -C "$wt" add "attempt-$n.txt" && git -C "$wt" commit -qm "attempt $n: work in progress" || exit 21
  printf 'not committed\n' > "$wt/uncommitted-$n.txt"
  printf ready > %[1]s.ready-$n
  while :; do sleep 1; done;;
esac
case " %[3]s " in *" $n "*)
  printf '[26-11-07 10:00:00] validation started: unit\n'
  printf '[26-11-07 10:00:05] deterministic validation failed; returning evidence to model for one repair turn: validation unit exit 1: not ok 2 - checkout totals\n'
  printf '[26-11-07 10:01:00] task failed, retrying...\n'
  printf 'error: runner: task phase: task execution failed after retry (FAILED signal received)\n' >&2
  exit 1;;
esac
printf 'candidate\n' > "$wt/candidate.txt"
git -C "$wt" add candidate.txt && git -C "$wt" commit -qm "attempt $n: candidate" || exit 22
`, counter, hang, fail)
}

// resumeFixture is a worktree run whose profile allows a fresh attempt (and so a resume), accepted when candidate.txt
// is there.
func resumeFixture(t *testing.T, hang, fail string, edit func(*authority.Manifest)) (runFixture, string) {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "starts")
	fixture := newRunFixtureEdited(t, resumeRalphex(counter, hang, fail), authority.WorktreePolicy{Enabled: true, Branch: "abcp/resumed-run"}, func(manifest *authority.Manifest) {
		manifest.Ralphex.SourceSHA = ralphex.TaskPromptSourceSHA
		manifest.Ralphex.FreshAttempts = 1
		manifest.Ralphex.Timeout = "1m"
		if edit != nil {
			edit(manifest)
		}
	}, commandPath(t, "test"), "-f", "candidate.txt")
	return fixture, counter
}

type runResponse struct {
	result Result
	err    error
}

// interrupt runs run until Ralphex's start n is ready, then ends its context with cause (or what ready answers), as
// abcp run does on a signal or an API cancellation.
func interrupt(t *testing.T, counter string, n int, cause error, run func(context.Context) (Result, error), ready ...func() error) (Result, error) {
	t.Helper()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	done := make(chan runResponse, 1)
	go func() {
		result, err := run(ctx)
		done <- runResponse{result, err}
	}()
	waitForRunFile(t, fmt.Sprintf("%s.ready-%d", counter, n))
	if len(ready) == 1 && ready[0] != nil {
		cause = ready[0]()
	}
	cancel(cause)
	got := <-done
	return got.result, got.err
}

func sigterm() error { return NewSignalCause(syscall.SIGTERM) }

func transitionTo(t *testing.T, events []ledger.Event, from, to domain.State) ledger.Event {
	t.Helper()
	for _, event := range events {
		if event.StateFrom == from && event.StateTo == to {
			return event
		}
	}
	t.Fatalf("no %s -> %s in %v", from, to, eventStates(events))
	return ledger.Event{}
}

// R6: a SIGTERM while the builder runs stops the attempt at RECOVERY_REQUIRED (R1's INTERRUPTED, recovery RESUME), not
// CANCELLED. The resume removes the attempt's worktree (its uncommitted work with it), starts Ralphex again on the same
// branch as attempt 2 with the resumed attempt's prompt, and the run ends accepted with both attempts' commits. A
// third launch is refused and records nothing.
func TestASigtermMidImplementationIsResumedOnceAndAccepted(t *testing.T) {
	fixture, counter := resumeFixture(t, "1", "", nil)
	result, err := interrupt(t, counter, 1, sigterm(), fixture.runner(t).Run)
	if err != nil || result.State != domain.StateRecoveryRequired {
		t.Fatalf("interrupted run = %s %q, %v", result.State, result.FailureReason, err)
	}
	events := readEvents(t, fixture.ledgerPath)
	if got, want := eventStates(events), []domain.State{domain.StateRunCreated, domain.StateAuthorityValidated, domain.StateExecutionStarting,
		domain.StateImplementing, domain.StateRecoveryRequired}; !reflect.DeepEqual(got, want) {
		t.Fatalf("states = %v", got)
	}
	stopped := events[len(events)-1]
	detail := recordedFailure(t, stopped)
	if stopped.StateFrom != domain.StateImplementing || stopped.Payload["recovery"] != RecoveryResume || stopped.Payload["next_attempt"] != float64(2) ||
		stopped.Payload["interruption"] != InterruptionSIGTERM || stopped.Payload["outcome"] != "canceled" ||
		detail.Cause != runfailure.CauseInterrupted || detail.Attempts != 1 || detail.Phase != runfailure.PhaseImplementation {
		t.Fatalf("RECOVERY_REQUIRED = %v, detail %+v", stopped.Payload, detail)
	}
	if facts := InterruptionOf(events); !facts.Resumable() || facts.Settled() || facts.Attempts != 1 {
		t.Fatalf("the ledger's interruption = %+v", facts)
	}

	result, err = fixture.runner(t).RunResumeInterrupted(context.Background())
	if err != nil || !result.Accepted() {
		t.Fatalf("resumed run = %s %q, %v", result.State, result.FailureReason, err)
	}
	if got := starts(t, counter); got != "2" {
		t.Fatalf("Ralphex started %s times, want 2", got)
	}
	events = readEvents(t, fixture.ledgerPath)
	assertEventEvidence(t, events)
	if got, want := eventStates(events)[5:], []domain.State{domain.StateExecutionStarting, domain.StateImplementing,
		domain.StateImplementationCompleted, domain.StateBranchAcceptancePending, domain.StateBranchAccepted}; !reflect.DeepEqual(got, want) {
		t.Fatalf("states after the resume = %v", got)
	}
	restart := transitionTo(t, events, domain.StateRecoveryRequired, domain.StateExecutionStarting)
	if restart.Payload["attempt"] != float64(2) || restart.Payload["resume_after"] != "INTERRUPTED" || restart.Payload["retained_worktree_removed"] != true {
		t.Fatalf("the resumed attempt's start = %v", restart.Payload)
	}
	prompt, err := os.ReadFile(counter + ".prompt-2")
	if err != nil || !bytes.Contains(prompt, []byte("RESUMED ATTEMPT")) || !bytes.Contains(prompt, []byte("Read the plan file at {{PLAN_FILE}}")) {
		t.Fatalf("the resumed attempt's prompt (%v):\n%s", err, prompt)
	}
	repository := fixture.authority.Repository().Path
	log := runGit(t, repository, "log", "--format=%s", "abcp/resumed-run")
	if !strings.Contains(log, "attempt 1: work in progress") || !strings.Contains(log, "attempt 2: candidate") {
		t.Fatalf("branch log:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(repository, ".ralphex", "worktrees", "abcp", "resumed-run", "uncommitted-1.txt")); !os.IsNotExist(err) {
		t.Fatalf("the interrupted attempt's uncommitted work outlived its worktree: %v", err)
	}
	for _, name := range []string{"ralphex-stdout.log", "ralphex-stdout-fresh-2.log", "ralphex-process-fresh-2.json"} {
		if _, err := os.Stat(filepath.Join(fixture.evidence, fixture.authority.RunID(), name)); err != nil {
			t.Fatalf("evidence %s: %v", name, err)
		}
	}
	if facts := InterruptionOf(events); facts.Resumable() || !facts.Settled() || !facts.Resumed {
		t.Fatalf("the ledger's interruption after the resume = %+v", facts)
	}
	before := len(events)
	if _, err := fixture.runner(t).RunResumeInterrupted(context.Background()); !errors.Is(err, ErrNotResumable) {
		t.Fatalf("a second resume = %v", err)
	}
	if after := len(readEvents(t, fixture.ledgerPath)); after != before {
		t.Fatalf("a refused resume recorded %d events", after-before)
	}
}

// R6: one resume per run, ever. A SIGTERM during the resumed attempt ends the run CANCELLED with INTERRUPTED and R1's two
// attempts, and the run cannot be resumed again.
func TestASecondInterruptionEndsTheRunWithoutLooping(t *testing.T) {
	fixture, counter := resumeFixture(t, "1 2", "", nil)
	if result, err := interrupt(t, counter, 1, sigterm(), fixture.runner(t).Run); err != nil || result.State != domain.StateRecoveryRequired {
		t.Fatalf("first interruption = %s, %v", result.State, err)
	}
	result, err := interrupt(t, counter, 2, sigterm(), fixture.runner(t).RunResumeInterrupted)
	if err != nil || result.State != domain.StateCancelled {
		t.Fatalf("second interruption = %s %q, %v", result.State, result.FailureReason, err)
	}
	events := readEvents(t, fixture.ledgerPath)
	stopped := events[len(events)-1]
	if detail := recordedFailure(t, stopped); stopped.StateFrom != domain.StateImplementing || stopped.StateTo != domain.StateCancelled ||
		detail.Cause != runfailure.CauseInterrupted || detail.Attempts != 2 || stopped.Payload["recovery"] != nil {
		t.Fatalf("the second interruption = %s -> %s %v %+v", stopped.StateFrom, stopped.StateTo, stopped.Payload, detail)
	}
	if facts := InterruptionOf(events); facts.Resumable() || !facts.Settled() {
		t.Fatalf("the ledger's interruption = %+v", facts)
	}
	if _, err := fixture.runner(t).RunResumeInterrupted(context.Background()); !errors.Is(err, ErrNotResumable) {
		t.Fatalf("a resume after the second interruption = %v", err)
	}
	if got := starts(t, counter); got != "2" {
		t.Fatalf("Ralphex started %s times, want 2", got)
	}
}

// R6: a run whose owner went without a word (SIGKILL, the host reset) is left in IMPLEMENTING. Its resume records
// IMPLEMENTING -> RECOVERY_REQUIRED (OWNER_GONE) first, then goes on as after a SIGTERM.
func TestARunWhoseOwnerWasKilledIsResumedFromImplementing(t *testing.T) {
	fixture, counter := resumeFixture(t, "1", "", nil)
	if result, err := interrupt(t, counter, 1, sigterm(), fixture.runner(t).Run); err != nil || result.State != domain.StateRecoveryRequired {
		t.Fatalf("interruption = %s, %v", result.State, err)
	}
	// What SIGKILL leaves: the same run, its builder stopped, its ledger without the last transition.
	data, err := os.ReadFile(fixture.ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if err := os.Truncate(fixture.ledgerPath, int64(len(data)-len(lines[len(lines)-1])-1)); err != nil {
		t.Fatal(err)
	}
	if events := readEvents(t, fixture.ledgerPath); events[len(events)-1].StateTo != domain.StateImplementing || !InterruptionOf(events).Resumable() {
		t.Fatalf("the killed run's ledger ends %s", events[len(events)-1].StateTo)
	}
	result, err := fixture.runner(t).RunResumeInterrupted(context.Background())
	if err != nil || !result.Accepted() {
		t.Fatalf("resumed run = %s %q, %v", result.State, result.FailureReason, err)
	}
	events := readEvents(t, fixture.ledgerPath)
	recorded := transitionTo(t, events, domain.StateImplementing, domain.StateRecoveryRequired)
	if detail := recordedFailure(t, recorded); recorded.Source != "resume-controller" || recorded.Payload["interruption"] != InterruptionOwnerGone ||
		recorded.Payload["recovery"] != RecoveryResume || recorded.Payload["next_attempt"] != float64(2) ||
		detail.Cause != runfailure.CauseInterrupted || detail.Attempts != 1 {
		t.Fatalf("RECOVERY_REQUIRED = %s %v %+v", recorded.Source, recorded.Payload, detail)
	}
	if restart := transitionTo(t, events, domain.StateRecoveryRequired, domain.StateExecutionStarting); restart.Payload["attempt"] != float64(2) {
		t.Fatalf("the resumed attempt's start = %v", restart.Payload)
	}
}

// R6 and R1: a resumed attempt whose builder gives up still gets R3's fresh attempt when the run had not used it, and
// the run's failure counts every attempt; a run that used it before the interruption gets none after.
func TestAResumedRunsAttemptsAndCauseAreR1s(t *testing.T) {
	t.Run("the fresh attempt follows the resume", func(t *testing.T) {
		fixture, counter := resumeFixture(t, "1", "2 3", nil)
		if result, err := interrupt(t, counter, 1, sigterm(), fixture.runner(t).Run); err != nil || result.State != domain.StateRecoveryRequired {
			t.Fatalf("interruption = %s, %v", result.State, err)
		}
		result, err := fixture.runner(t).RunResumeInterrupted(context.Background())
		if err != nil || result.State != domain.StateFailed || starts(t, counter) != "3" {
			t.Fatalf("resumed run = %s, %v after %s starts", result.State, err, starts(t, counter))
		}
		events := readEvents(t, fixture.ledgerPath)
		retrying := transitionTo(t, events, domain.StateImplementing, domain.StateRetrying)
		if detail := recordedFailure(t, retrying); retrying.Payload["next_attempt"] != float64(3) || detail.Attempts != 2 || detail.Cause != runfailure.CauseValidationFailed {
			t.Fatalf("RETRYING = %v %+v", retrying.Payload, detail)
		}
		if detail := recordedFailure(t, events[len(events)-1]); detail.Attempts != 3 || detail.Cause != runfailure.CauseValidationFailed || detail.Check != "unit" {
			t.Fatalf("the failure = %+v", detail)
		}
		if _, err := os.Stat(filepath.Join(fixture.evidence, fixture.authority.RunID(), "ralphex-stdout-fresh-3.log")); err != nil {
			t.Fatalf("the fresh attempt's evidence: %v", err)
		}
	})
	t.Run("the fresh attempt was used before", func(t *testing.T) {
		fixture, counter := resumeFixture(t, "2", "1 3", nil)
		if result, err := interrupt(t, counter, 2, sigterm(), fixture.runner(t).Run); err != nil || result.State != domain.StateRecoveryRequired {
			t.Fatalf("interruption of the fresh attempt = %s, %v", result.State, err)
		}
		stopped := lastEvent(t, fixture)
		if detail := recordedFailure(t, stopped); stopped.Payload["next_attempt"] != float64(3) || detail.Attempts != 2 || detail.Cause != runfailure.CauseInterrupted {
			t.Fatalf("RECOVERY_REQUIRED = %v %+v", stopped.Payload, detail)
		}
		result, err := fixture.runner(t).RunResumeInterrupted(context.Background())
		if err != nil || result.State != domain.StateFailed || starts(t, counter) != "3" {
			t.Fatalf("resumed run = %s, %v after %s starts", result.State, err, starts(t, counter))
		}
		retrying := 0
		for _, event := range readEvents(t, fixture.ledgerPath) {
			if event.StateTo == domain.StateRetrying {
				retrying++
			}
		}
		terminal := lastEvent(t, fixture)
		if detail := recordedFailure(t, terminal); retrying != 1 || terminal.StateFrom != domain.StateImplementing || detail.Attempts != 3 {
			t.Fatalf("RETRYING %d times; the failure %s -> %s %+v", retrying, terminal.StateFrom, terminal.StateTo, detail)
		}
	})
}

// R6: an API cancellation (Stop build, Repo C's stop for credits) stays CANCELLED and is never resumed; nor is a run
// stopped by SIGTERM after a stop was asked of it, one whose profile allows no fresh attempt, or one stopped by SIGINT.
func TestAStopTheOwnerAskedForIsNeverResumed(t *testing.T) {
	// stopAsked runs the run until its builder is ready, records the stop the person asked for in its ledger (as the
	// service's cancel watcher does), then ends the run's context with cause.
	stopAsked := func(t *testing.T, fixture runFixture, counter string, cause func(owner string) error) Result {
		t.Helper()
		events, err := ledger.NewJSONLLedger(fixture.ledgerPath)
		if err != nil {
			t.Fatal(err)
		}
		defer events.Close()
		runner := fixture.runner(t)
		owner := digestForRunTest("owner-lease")
		result, err := interrupt(t, counter, 1, nil, func(ctx context.Context) (Result, error) {
			return runner.Run(ctx)
		}, func() error {
			implementing := transitionTo(t, readEvents(t, fixture.ledgerPath), domain.StateExecutionStarting, domain.StateImplementing)
			if err := events.Append(apiCancelRequestForRunner(t, runner, "op-stop", owner, implementing.EventID, domain.StateImplementing)); err != nil {
				t.Fatal(err)
			}
			return cause(owner)
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	t.Run("an API cancellation", func(t *testing.T) {
		fixture, counter := resumeFixture(t, "1", "", nil)
		result := stopAsked(t, fixture, counter, func(owner string) error { return NewAPICancelCause("op-stop", owner) })
		stopped := lastEvent(t, fixture)
		if result.State != domain.StateCancelled || stopped.StateTo != domain.StateCancelled || stopped.Payload["operation_id"] != "op-stop" {
			t.Fatalf("the stop = %s, %s %v", result.State, stopped.StateTo, stopped.Payload)
		}
		if facts := InterruptionOf(readEvents(t, fixture.ledgerPath)); facts.Resumable() || !facts.Settled() || !facts.CancelRequested {
			t.Fatalf("the ledger's interruption = %+v", facts)
		}
		if _, err := fixture.runner(t).RunResumeInterrupted(context.Background()); !errors.Is(err, ErrNotResumable) {
			t.Fatalf("a resume after an API cancellation = %v", err)
		}
	})
	t.Run("a SIGTERM after a stop was asked for", func(t *testing.T) {
		fixture, counter := resumeFixture(t, "1", "", nil)
		result := stopAsked(t, fixture, counter, func(string) error { return sigterm() })
		if detail := recordedFailure(t, lastEvent(t, fixture)); result.State != domain.StateCancelled || detail.Cause != runfailure.CauseInterrupted {
			t.Fatalf("the stop = %s %+v", result.State, detail)
		}
		if _, err := fixture.runner(t).RunResumeInterrupted(context.Background()); !errors.Is(err, ErrNotResumable) {
			t.Fatalf("a resume after a stop was asked for = %v", err)
		}
	})
	t.Run("a profile without a fresh attempt", func(t *testing.T) {
		fixture, counter := resumeFixture(t, "1", "", func(m *authority.Manifest) { m.Ralphex.FreshAttempts = 0 })
		result, err := interrupt(t, counter, 1, sigterm(), fixture.runner(t).Run, nil)
		if err != nil || result.State != domain.StateCancelled {
			t.Fatalf("interrupted run = %s, %v", result.State, err)
		}
		if detail := recordedFailure(t, lastEvent(t, fixture)); detail.Cause != runfailure.CauseInterrupted {
			t.Fatalf("detail = %+v", detail)
		}
		if _, err := fixture.runner(t).RunResumeInterrupted(context.Background()); !errors.Is(err, ErrNotResumable) {
			t.Fatalf("a resume without the profile's word = %v", err)
		}
	})
	t.Run("SIGINT", func(t *testing.T) {
		fixture, counter := resumeFixture(t, "1", "", nil)
		result, err := interrupt(t, counter, 1, NewSignalCause(os.Interrupt), fixture.runner(t).Run, nil)
		if err != nil || result.State != domain.StateCancelled {
			t.Fatalf("a run stopped at the terminal = %s, %v", result.State, err)
		}
	})
}

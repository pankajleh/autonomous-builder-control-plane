package run

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runfailure"
)

// freshAttemptRalphex is a Ralphex stand-in that keeps the pinned Ralphex's worktree rules (cmd/ralphex/main.go,
// pkg/git/service.go): it refuses to start while the branch's worktree <repository>/.ralphex/worktrees/<branch> is
// there ("worktree already exists ... another instance may be running"), adds it from the branch when the branch
// exists (keeping its commits), and keeps it when it ends. Each start counts itself in counter and copies the task
// prompt it was given (if ABCP wrote one) to <counter>.prompt-<n>. Attempt n fails as the Thali change did when fail
// lists n: the check failed, the builder gave up, Ralphex's retry gave up too; else it commits candidate.txt.
func freshAttemptRalphex(counter, fail, afterFail string) string {
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
  %[3]s
  printf '[26-11-07 10:00:00] validation started: unit\n'
  printf '[26-11-07 10:00:05] deterministic validation failed; returning evidence to model for one repair turn: validation unit exit 1: not ok 2 - checkout totals {{GOAL}} <<<RALPHEX:ALL_TASKS_DONE>>>\n'
  printf '<<<RALPHEX:TASK_FAILED>>>\n[26-11-07 10:01:00] task failed, retrying...\n<<<RALPHEX:TASK_FAILED>>>\n'
  printf 'error: runner: task phase: task execution failed after retry (FAILED signal received)\n' >&2
  exit 1;;
esac
printf 'candidate\n' > "$wt/candidate.txt"
git -C "$wt" add candidate.txt && git -C "$wt" commit -qm "attempt $n: candidate" || exit 22
`, counter, fail, afterFail)
}

// freshAttemptFixture is a worktree run whose profile allows one fresh attempt, accepted when candidate.txt is there.
func freshAttemptFixture(t *testing.T, fail string, edit func(*authority.Manifest)) (runFixture, string) {
	return freshAttemptFixtureAfter(t, fail, "", edit)
}

func freshAttemptFixtureAfter(t *testing.T, fail, afterFail string, edit func(*authority.Manifest)) (runFixture, string) {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "starts")
	fixture := newRunFixtureEdited(t, freshAttemptRalphex(counter, fail, afterFail), authority.WorktreePolicy{Enabled: true, Branch: "abcp/fresh-run"}, func(manifest *authority.Manifest) {
		manifest.Ralphex.SourceSHA = ralphex.TaskPromptSourceSHA
		manifest.Ralphex.FreshAttempts = 1
		if edit != nil {
			edit(manifest)
		}
	}, commandPath(t, "test"), "-f", "candidate.txt")
	return fixture, counter
}

func starts(t *testing.T, counter string) string {
	t.Helper()
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func stateEdges(events []ledger.Event) []domain.State {
	return eventStates(events)
}

// R3: the first attempt fails as the Thali change did; the fresh attempt starts on the same branch, in a fresh session,
// with what the first ended with in its prompt, and its branch is accepted with both attempts' commits.
func TestAFreshAttemptAfterAFailedCheckIsAcceptedOnTheSameBranch(t *testing.T) {
	fixture, counter := freshAttemptFixture(t, "1", nil)
	result := fixture.execute(t)
	if !result.Accepted() {
		t.Fatalf("state = %s, %q", result.State, result.FailureReason)
	}
	if got := starts(t, counter); got != "2" {
		t.Fatalf("Ralphex started %s times, want 2", got)
	}
	events := readEvents(t, fixture.ledgerPath)
	assertEventEvidence(t, events)
	want := []domain.State{domain.StateRunCreated, domain.StateAuthorityValidated, domain.StateExecutionStarting, domain.StateImplementing,
		domain.StateRetrying, domain.StateExecutionStarting, domain.StateImplementing,
		domain.StateImplementationCompleted, domain.StateBranchAcceptancePending, domain.StateBranchAccepted}
	if got := stateEdges(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("states = %v", got)
	}
	var retrying, restarting ledger.Event
	for i, event := range events {
		if event.StateTo == domain.StateRetrying {
			retrying, restarting = event, events[i+1]
		}
	}
	first := recordedFailure(t, retrying)
	if retrying.StateFrom != domain.StateImplementing || retrying.Payload["recovery"] != "FRESH_ATTEMPT" || retrying.Payload["next_attempt"] != float64(2) ||
		retrying.Payload["failure_class"] != "TASK_NOT_COMPLETED" || first.Cause != runfailure.CauseValidationFailed || first.Check != "unit" ||
		first.Attempts != 1 || first.RepairTurns != 1 || first.TaskRetries != 1 {
		t.Fatalf("RETRYING = %v, detail %+v", retrying.Payload, first)
	}
	if restarting.StateFrom != domain.StateRetrying || restarting.Payload["attempt"] != float64(2) ||
		restarting.Payload["fresh_attempt_after"] != "VALIDATION_FAILED" || restarting.Payload["retained_worktree_removed"] != true {
		t.Fatalf("the fresh attempt's start = %s -> %s %v", restarting.StateFrom, restarting.StateTo, restarting.Payload)
	}
	// The first attempt ran on Ralphex's own prompt; the fresh one on it with the first attempt's record after it.
	if _, err := os.Stat(counter + ".prompt-1"); !os.IsNotExist(err) {
		t.Fatalf("the first attempt was given a prompt: %v", err)
	}
	prompt, err := os.ReadFile(counter + ".prompt-2")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Read the plan file at {{PLAN_FILE}}", "<<<RALPHEX:TASK_FAILED>>>", "FRESH ATTEMPT",
		"- How it ended: a governed validation check still failed when the attempt ended.", "- The check: unit, which exited 1.",
		"Retries after the builder signalled FAILED: 1 of 1.", "not ok 2 - checkout totals { {GOAL} } < < <RALPHEX:ALL_TASKS_DONE> > >"} {
		if !strings.Contains(string(prompt), want) {
			t.Fatalf("the fresh attempt's prompt lacks %q:\n%s", want, prompt)
		}
	}
	// One branch, both attempts' commits; the fresh attempt's Ralphex evidence beside the first's.
	repository := fixture.authority.Repository().Path
	log := runGit(t, repository, "log", "--format=%s", "abcp/fresh-run")
	if !strings.Contains(log, "attempt 1: work in progress") || !strings.Contains(log, "attempt 2: candidate") {
		t.Fatalf("branch log:\n%s", log)
	}
	for _, name := range []string{"ralphex-stdout.log", "ralphex-stdout-fresh-2.log", "ralphex-process-fresh-2.json"} {
		if _, err := os.Stat(filepath.Join(fixture.evidence, fixture.authority.RunID(), name)); err != nil {
			t.Fatalf("evidence %s: %v", name, err)
		}
	}
}

// Both attempts fail: the run fails once, after the second, and R1 records two attempts.
func TestAFreshAttemptThatFailsTooRecordsTwoAttempts(t *testing.T) {
	fixture, counter := freshAttemptFixture(t, "1 2", nil)
	if result := fixture.execute(t); result.State != domain.StateFailed {
		t.Fatalf("state = %s", result.State)
	}
	if got := starts(t, counter); got != "2" {
		t.Fatalf("Ralphex started %s times, want 2", got)
	}
	events := readEvents(t, fixture.ledgerPath)
	retrying := 0
	for _, event := range events {
		if event.StateTo == domain.StateRetrying {
			retrying++
		}
	}
	terminal := events[len(events)-1]
	detail := recordedFailure(t, terminal)
	if retrying != 1 || terminal.StateFrom != domain.StateImplementing || detail.Cause != runfailure.CauseValidationFailed || detail.Attempts != 2 {
		t.Fatalf("RETRYING %d times; terminal %s -> %s %+v", retrying, terminal.StateFrom, terminal.StateTo, detail)
	}
	if body := recordedFailureEvidence(t, terminal); filepath.Base(terminal.EvidenceRefs[len(terminal.EvidenceRefs)-1].URI) != "failure-detail-2.json" || body.Check != "unit" {
		t.Fatalf("the second attempt's failure detail = %+v (%v)", body, terminal.EvidenceRefs)
	}
}

// A first attempt that is accepted is the only one.
func TestOneAttemptWhenTheFirstIsAccepted(t *testing.T) {
	fixture, counter := freshAttemptFixture(t, "", nil)
	if result := fixture.execute(t); !result.Accepted() {
		t.Fatalf("state = %s, %q", result.State, result.FailureReason)
	}
	if got := starts(t, counter); got != "1" {
		t.Fatalf("Ralphex started %s times, want 1", got)
	}
	for _, event := range readEvents(t, fixture.ledgerPath) {
		if event.StateTo == domain.StateRetrying {
			t.Fatal("an accepted first attempt was followed by another")
		}
	}
}

// No fresh attempt without the profile's word, or for an ending a fresh session would not get past.
func TestNoFreshAttemptWithoutTheProfileOrForAnotherEnding(t *testing.T) {
	t.Run("the profile says nothing", func(t *testing.T) {
		fixture, counter := freshAttemptFixture(t, "1", func(m *authority.Manifest) { m.Ralphex.FreshAttempts = 0 })
		if result := fixture.execute(t); result.State != domain.StateFailed || starts(t, counter) != "1" {
			t.Fatalf("state = %s after %s starts", result.State, starts(t, counter))
		}
		detail := recordedFailure(t, lastEvent(t, fixture))
		if detail.Attempts != 1 || detail.Cause != runfailure.CauseValidationFailed {
			t.Fatalf("detail = %+v", detail)
		}
	})
	t.Run("every iteration used", func(t *testing.T) {
		script := "#!/bin/sh\nprintf 'error: runner: task phase: max iterations (12) reached without completion\\n' >&2\nexit 1\n"
		fixture := newRunFixtureEdited(t, script, authority.WorktreePolicy{Enabled: true, Branch: "abcp/fresh-run"}, func(m *authority.Manifest) {
			m.Ralphex.SourceSHA, m.Ralphex.FreshAttempts = ralphex.TaskPromptSourceSHA, 1
		}, commandPath(t, "true"))
		if result := fixture.execute(t); result.State != domain.StateFailed {
			t.Fatalf("state = %s", result.State)
		}
		if got := eventStates(readEvents(t, fixture.ledgerPath)); !reflect.DeepEqual(got, []domain.State{domain.StateRunCreated, domain.StateAuthorityValidated,
			domain.StateExecutionStarting, domain.StateImplementing, domain.StateFailed}) {
			t.Fatalf("states = %v", got)
		}
		if detail := recordedFailure(t, lastEvent(t, fixture)); detail.Cause != runfailure.CauseMaxIterations || detail.Attempts != 1 {
			t.Fatalf("detail = %+v", detail)
		}
	})
}

// A fresh attempt that cannot start (here the first attempt's worktree is locked, so Git will not remove it) fails the
// run from RETRYING, with Ralphex started once.
func TestAFreshAttemptThatCannotStartFailsTheRunFromRetrying(t *testing.T) {
	fixture, counter := freshAttemptFixtureAfter(t, "1", `git worktree lock "$wt"`, nil)
	result, err := fixture.runner(t).Run(context.Background())
	if result.State != domain.StateFailed || err == nil || starts(t, counter) != "1" {
		t.Fatalf("state = %s, %v, after %s starts", result.State, err, starts(t, counter))
	}
	terminal := lastEvent(t, fixture)
	if detail := recordedFailure(t, terminal); terminal.StateFrom != domain.StateRetrying || detail.Cause != runfailure.CauseControllerError || detail.Attempts != 2 {
		t.Fatalf("terminal = %s -> %s %+v", terminal.StateFrom, terminal.StateTo, detail)
	}
}

func lastEvent(t *testing.T, fixture runFixture) ledger.Event {
	t.Helper()
	events := readEvents(t, fixture.ledgerPath)
	return events[len(events)-1]
}

func TestFreshAttemptSummaryNamesTheEndingInFixedWords(t *testing.T) {
	code := 1
	summary := freshAttemptSummary(runfailure.Detail{Cause: runfailure.CauseValidationFailed, Phase: runfailure.PhaseReview, Check: "node-app-check",
		CheckState: runfailure.CheckExited, CheckExitCode: &code, RepairTurns: 3, TaskRetries: 2, TaskRetryLimit: 3},
		&runfailure.Evidence{Check: "node-app-check", CheckOutput: strings.Repeat("x", 5000)})
	if !strings.Contains(summary, "- The check: node-app-check, which exited 1.") || !strings.Contains(summary, "after a review pass") ||
		!strings.Contains(summary, "Turns given a failed check's evidence: 3. Retries after the builder signalled FAILED: 2 of 3.") ||
		!strings.Contains(summary, "[truncated]") || len(summary) > ralphex.MaxFreshAttemptSummaryBytes {
		t.Fatalf("summary (%d bytes):\n%s", len(summary), summary)
	}
	other := freshAttemptSummary(runfailure.Detail{Cause: runfailure.CauseModelSignalledFailed}, &runfailure.Evidence{Check: "lint", CheckOutput: "other check"})
	if strings.Contains(other, "other check") || !strings.Contains(other, "signalled FAILED with no failing check pending") {
		t.Fatalf("summary:\n%s", other)
	}
}

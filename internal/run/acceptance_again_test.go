package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runfailure"
)

// acceptanceAgainRalphex commits a candidate in place and prints Ralphex's own validation lines for "unit" (passed
// unless passed is false: then started and never reported).
func acceptanceAgainRalphex(passed bool) string {
	report := `printf '[26-11-07 10:00:01] validation completed successfully: unit (1s)\n'`
	if !passed {
		report = ""
	}
	return fmt.Sprintf(`#!/bin/sh
printf '[26-11-07 10:00:00] validation started: unit\n'
%s
printf 'candidate\n' > candidate.txt
git add candidate.txt || exit 90
git commit -qm 'candidate implementation' || exit 91
`, report)
}

// flakyAcceptance is an acceptance check that counts its runs in counter and fails the runs fails lists, as the kit's
// check does on a test file that fails beside the others (e51a8b70, 2026-11-06), else passes on the candidate.
func flakyAcceptance(t *testing.T, fails string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	counter, script := filepath.Join(dir, "runs"), filepath.Join(dir, "node-app-acceptance")
	writeTestFile(t, script, []byte(fmt.Sprintf(`#!/bin/sh
n=$(cat %[1]s 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > %[1]s
case " %[2]s " in *" $n "*)
  echo "NODE_CHECK_FAILED: tests: failed: not ok 3 - tests/browser.test.js (run $n)" >&2
  exit 1;;
esac
test -f candidate.txt
`, counter, fails)), 0o700)
	return script, counter
}

func acceptanceAgainFixture(t *testing.T, ralphexScript, check string, edit func(*authority.Manifest)) runFixture {
	t.Helper()
	return newRunFixtureEdited(t, ralphexScript, authority.WorktreePolicy{}, func(manifest *authority.Manifest) {
		manifest.AcceptanceReruns = 1
		manifest.Ralphex.LongRunningSubprocessMode = "orchestrator"
		manifest.Ralphex.Validation = []authority.RalphexValidationCommand{{ID: "unit", Argv: []string{commandPath(t, "true")}, Timeout: "1m"}}
		manifest.Acceptance[0].Name = "node-app-acceptance"
		if edit != nil {
			edit(manifest)
		}
	}, check)
}

func lastEventTo(t *testing.T, events []ledger.Event, state domain.State) ledger.Event {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].StateTo == state {
			return events[i]
		}
	}
	t.Fatalf("no transition to %s in %v", state, eventStates(events))
	return ledger.Event{}
}

func evidenceNamed(event ledger.Event, name string) (ledger.EvidenceRef, bool) {
	for _, ref := range event.EvidenceRefs {
		if filepath.Base(ref.URI) == name {
			return ref, true
		}
	}
	return ledger.EvidenceRef{}, false
}

// R5: the run's own validation passed, the final acceptance failed its check once and passed it when run again on a
// fresh checkout of the same candidate: the branch is accepted, the payload says the acceptance ran twice and which
// check was flaky, acceptance-flaky.json keeps the first failure, and no failure is recorded.
func TestAnAcceptanceThatFailsOnceAndPassesAgainIsAcceptedAsFlaky(t *testing.T) {
	check, counter := flakyAcceptance(t, "1")
	fixture := acceptanceAgainFixture(t, acceptanceAgainRalphex(true), check, nil)
	result := fixture.execute(t)
	if !result.Accepted() {
		t.Fatalf("state = %s, %q", result.State, result.FailureReason)
	}
	if got := starts(t, counter); got != "2" {
		t.Fatalf("the acceptance ran %s times, want 2", got)
	}
	events := readEvents(t, fixture.ledgerPath)
	accepted := lastEventTo(t, events, domain.StateBranchAccepted)
	if accepted.StateFrom != domain.StateBranchAcceptancePending || accepted.Payload["acceptance_runs"] != float64(2) ||
		accepted.Payload["acceptance_flaky"] != "node-app-acceptance" || accepted.Payload[runfailure.PayloadKey] != nil {
		t.Fatalf("BRANCH_ACCEPTED = %v", accepted.Payload)
	}
	for _, name := range []string{"acceptance-command-001-stderr.log", "acceptance-command-001-stderr-again.log", "acceptance-final-git-again.json"} {
		if _, ok := evidenceNamed(accepted, name); !ok {
			t.Fatalf("no %s in the acceptance's evidence", name)
		}
	}
	ref, ok := evidenceNamed(accepted, "acceptance-flaky.json")
	if !ok || ref.Kind != AcceptanceFlakyKind {
		t.Fatalf("no acceptance-flaky.json: %v", accepted.EvidenceRefs)
	}
	data, err := os.ReadFile(ref.URI)
	if err != nil {
		t.Fatal(err)
	}
	var flaky AcceptanceFlakyV1
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&flaky); err != nil {
		t.Fatal(err)
	}
	if flaky.SchemaVersion != AcceptanceFlakySchemaVersion || flaky.Words != AcceptanceFlakyWords || flaky.Check != "node-app-acceptance" ||
		flaky.CommandIndex != 1 || flaky.FirstExitCode != 1 || !strings.Contains(flaky.CheckOutput, "not ok 3 - tests/browser.test.js (run 1)") {
		t.Fatalf("acceptance-flaky.json = %+v", flaky)
	}
	for _, event := range events {
		if event.StateTo == domain.StateFailed || event.StateTo == domain.StateRetrying {
			t.Fatalf("the run recorded %s", event.StateTo)
		}
	}
}

// R5: failing again, the run fails as before, with the second run's output, and says the acceptance ran twice.
func TestAnAcceptanceThatFailsTwiceFailsWithTheSecondRunsOutput(t *testing.T) {
	check, counter := flakyAcceptance(t, "1 2")
	fixture := acceptanceAgainFixture(t, acceptanceAgainRalphex(true), check, nil)
	result := fixture.execute(t)
	if result.State != domain.StateFailed || starts(t, counter) != "2" {
		t.Fatalf("state = %s after %s acceptance runs", result.State, starts(t, counter))
	}
	failed := lastEventTo(t, readEvents(t, fixture.ledgerPath), domain.StateFailed)
	detail := recordedFailure(t, failed)
	if failed.StateFrom != domain.StateBranchAcceptancePending || failed.Payload["acceptance_runs"] != float64(2) ||
		detail.Cause != runfailure.CauseAcceptanceFailed || detail.Check != "node-app-acceptance" {
		t.Fatalf("FAILED = %v, detail %+v", failed.Payload, detail)
	}
	if body := recordedFailureEvidence(t, failed); !strings.Contains(body.CheckOutput, "(run 2)") {
		t.Fatalf("the failure keeps %q, not the second run's output", body.CheckOutput)
	}
	if _, ok := evidenceNamed(failed, "acceptance-flaky.json"); ok {
		t.Fatal("a failing acceptance was called flaky")
	}
}

// R5 runs the acceptance again only when the profile says so, the run's own validation passed, and a required check
// failed by its exit: otherwise the run fails after one acceptance run, as before.
func TestNoAcceptanceRerunWithoutTheProfileTheRunsOwnValidationOrAnExit(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		passed  bool
		edit    func(*authority.Manifest)
		timeout bool
	}{
		{name: "without the profile's acceptance_reruns", passed: true, edit: func(m *authority.Manifest) { m.AcceptanceReruns = 0 }},
		{name: "the run's own validation never reported passing", passed: false},
		{name: "the check timed out", passed: true, timeout: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			check, counter := flakyAcceptance(t, "1")
			edit := scenario.edit
			if scenario.timeout {
				writeTestFile(t, check, []byte("#!/bin/sh\necho run >> "+counter+".log\nn=$(wc -l < "+counter+".log); echo $n > "+counter+"\nexec sleep 30\n"), 0o700)
				edit = func(m *authority.Manifest) { m.Acceptance[0].Timeout = "1s" }
			}
			fixture := acceptanceAgainFixture(t, acceptanceAgainRalphex(scenario.passed), check, edit)
			result := fixture.execute(t)
			if result.State != domain.StateFailed || strings.TrimSpace(starts(t, counter)) != "1" {
				t.Fatalf("state = %s after %s acceptance runs", result.State, starts(t, counter))
			}
			failed := lastEventTo(t, readEvents(t, fixture.ledgerPath), domain.StateFailed)
			if _, twice := failed.Payload["acceptance_runs"]; twice {
				t.Fatalf("FAILED = %v", failed.Payload)
			}
		})
	}
}

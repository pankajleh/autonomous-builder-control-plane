package run

import (
	"context"
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

// A Ralphex stand-in shaped as the Thali Calorie Lens change ended (Repo C BUILD_RELIABILITY.md §3.5): node-app-check
// failed twice, each time with its evidence returned to the builder, then the builder signalled FAILED, Ralphex retried
// once and the run ended "task execution failed after retry".
const failedCheckRalphex = `#!/bin/sh
printf '[26-11-07 10:20:04] validation started: node-app-check\n'
printf '[26-11-07 10:21:40] deterministic validation failed; returning evidence to model for one repair turn: validation node-app-check exit 1: NODE_APP_CHECK_FAILED: tests\nnot ok 7 - editor approval flow\n'
printf '\n--- task iteration 2 ---\n'
printf '[26-11-07 10:25:11] validation started: node-app-check\n'
printf '[26-11-07 10:26:30] deterministic validation failed; returning evidence to model for one repair turn: validation node-app-check exit 1: NODE_APP_CHECK_FAILED: tests\nnot ok 3 - tests/browser.test.js\n  api_key=sk-abcdefghijklmnop1234\n'
printf '\n--- task iteration 3 ---\n<<<RALPHEX:TASK_FAILED>>>\n'
printf '[26-11-07 10:28:00] task failed, retrying...\n'
printf '\n--- task iteration 4 ---\n<<<RALPHEX:TASK_FAILED>>>\n'
printf 'error: runner: task phase: task execution failed after retry (FAILED signal received)\n' >&2
exit 1
`

func TestAFailedCheckTheBuilderGaveUpOnIsTheRunsRecordedCause(t *testing.T) {
	fixture := newRunFixtureWithScript(t, failedCheckRalphex, authority.WorktreePolicy{}, commandPath(t, "true"))
	result := fixture.execute(t)
	if result.State != domain.StateFailed {
		t.Fatalf("state = %s, %q", result.State, result.FailureReason)
	}
	events := readEvents(t, fixture.ledgerPath)
	assertEventEvidence(t, events)
	terminal := events[len(events)-1]
	if terminal.StateFrom != domain.StateImplementing || terminal.Payload["failure_class"] != "TASK_NOT_COMPLETED" {
		t.Fatalf("terminal = %s -> %s, %v", terminal.StateFrom, terminal.StateTo, terminal.Payload)
	}
	detail := recordedFailure(t, terminal)
	if detail.Cause != runfailure.CauseValidationFailed || detail.Phase != runfailure.PhaseImplementation || detail.Check != "node-app-check" ||
		detail.CheckState != runfailure.CheckExited || detail.CheckExitCode == nil || *detail.CheckExitCode != 1 ||
		detail.RepairTurns != 2 || detail.TaskRetries != 1 || detail.TaskRetryLimit != 1 || detail.Attempts != 1 || detail.Engine != "codex" {
		t.Fatalf("detail = %+v", detail)
	}
	body := recordedFailureEvidence(t, terminal)
	if body.Cause != runfailure.CauseValidationFailed || body.Check != "node-app-check" || body.CheckOutputTruncated ||
		!strings.HasPrefix(body.CheckOutput, "NODE_APP_CHECK_FAILED: tests\nnot ok 3 - tests/browser.test.js\n  api_key=") ||
		strings.Contains(body.CheckOutput, "sk-abcdefghijklmnop1234") || strings.Contains(body.CheckOutput, "editor approval") ||
		body.LastError != "runner: task phase: task execution failed after retry (FAILED signal received)" {
		t.Fatalf("evidence = %+v", body)
	}
	// The ledger keeps codes and counts only; the check's words are evidence.
	ledgerBytes, err := os.ReadFile(fixture.ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ledgerBytes), "browser.test.js") || strings.Contains(string(ledgerBytes), "NODE_APP_CHECK_FAILED") {
		t.Fatal("the ledger carries the check's output")
	}
}

// A check that fails twice and then passes leaves an accepted branch and no failure.
func TestAValidationThatFailsTwiceThenPassesIsAccepted(t *testing.T) {
	script := `#!/bin/sh
printf '[26-11-07 10:00:00] validation started: unit\n'
printf '[26-11-07 10:00:05] deterministic validation failed; returning evidence to model for one repair turn: validation unit exit 1: FAIL a\n'
printf '[26-11-07 10:01:00] validation started: unit\n'
printf '[26-11-07 10:01:05] deterministic validation failed; returning evidence to model for one repair turn: validation unit exit 1: FAIL b\n'
printf '[26-11-07 10:02:00] validation started: unit\n'
printf '[26-11-07 10:02:05] validation completed successfully: unit (5s)\n'
printf 'candidate\n' > candidate.txt
git add candidate.txt || exit 90
git commit -qm 'candidate implementation' || exit 91
`
	fixture := newRunFixtureWithScript(t, script, authority.WorktreePolicy{}, commandPath(t, "true"))
	result := fixture.execute(t)
	if !result.Accepted() {
		t.Fatalf("state = %s, %q", result.State, result.FailureReason)
	}
	for _, event := range readEvents(t, fixture.ledgerPath) {
		if _, ok := event.Payload[runfailure.PayloadKey]; ok {
			t.Fatalf("an accepted run recorded a failure: %+v", event)
		}
	}
}

func TestAnAcceptanceFailureNamesItsCommandAndKeepsItsOutput(t *testing.T) {
	check := filepath.Join(t.TempDir(), "product-verification")
	writeTestFile(t, check, []byte("#!/bin/sh\nprintf 'checked 3 pages\\n'\nprintf 'ACCEPTANCE_FAILED: index.html is missing at the repository root\\n' >&2\nexit 3\n"), 0o700)
	fixture := newRunFixture(t, 0, check)
	result := fixture.execute(t)
	if result.State != domain.StateFailed {
		t.Fatalf("state = %s", result.State)
	}
	events := readEvents(t, fixture.ledgerPath)
	assertEventEvidence(t, events)
	terminal := events[len(events)-1]
	detail := recordedFailure(t, terminal)
	// The fixture's command is named "deterministic check", which is not an identifier: it is named by its place.
	if detail.Cause != runfailure.CauseAcceptanceFailed || detail.Phase != runfailure.PhaseAcceptance || detail.Check != "acceptance-1" ||
		detail.CheckState != runfailure.CheckExited || detail.CheckExitCode == nil || *detail.CheckExitCode != 3 {
		t.Fatalf("detail = %+v", detail)
	}
	body := recordedFailureEvidence(t, terminal)
	if body.CheckOutput != "ACCEPTANCE_FAILED: index.html is missing at the repository root\nchecked 3 pages" || body.Check != "acceptance-1" {
		t.Fatalf("evidence = %+v", body)
	}
}

// Every other way a run stops records its fixed cause too, with no evidence body when there is no check output or
// last error to keep.
func TestEveryStopRecordsItsCause(t *testing.T) {
	t.Run("authority that does not validate", func(t *testing.T) {
		fixture := newRunFixture(t, 0, commandPath(t, "true"))
		writeTestFile(t, filepath.Join(fixture.authority.Repository().Path, "untracked.txt"), []byte("x\n"), 0o600)
		if _, err := fixture.runner(t).Run(context.Background()); err == nil {
			t.Fatal("a dirty repository ran")
		}
		events := readEvents(t, fixture.ledgerPath)
		assertCause(t, events[len(events)-1], runfailure.CauseAdmissionFailed, runfailure.PhaseAdmission)
	})
	t.Run("a timeout", func(t *testing.T) {
		fixture := newRunFixtureWithScript(t, "#!/bin/sh\nwhile :; do sleep 60; done\n", authority.WorktreePolicy{}, commandPath(t, "true"))
		manifest := fixture.authority.Manifest()
		manifest.Ralphex.Timeout = "50ms"
		fixture.authority = fixture.admit(t, manifest)
		fixture.execute(t)
		events := readEvents(t, fixture.ledgerPath)
		assertCause(t, events[len(events)-1], runfailure.CauseTimeout, runfailure.PhaseImplementation)
	})
	t.Run("an exit with no words", func(t *testing.T) {
		fixture := newRunFixture(t, 17, commandPath(t, "true"))
		fixture.execute(t)
		events := readEvents(t, fixture.ledgerPath)
		terminal := events[len(events)-1]
		assertCause(t, terminal, runfailure.CauseUnclassified, runfailure.PhaseImplementation)
		for _, ref := range terminal.EvidenceRefs {
			if ref.Kind == runfailure.EvidenceKind {
				t.Fatal("an evidence body with nothing to keep")
			}
		}
	})
	t.Run("a stop by a signal", func(t *testing.T) {
		readyPath := filepath.Join(t.TempDir(), "ralphex-ready")
		fixture := newRunFixtureWithScript(t, fmt.Sprintf("#!/bin/sh\nprintf ready > %s\nwhile :; do sleep 60; done\n", readyPath), authority.WorktreePolicy{}, commandPath(t, "true"))
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		done := make(chan error, 1)
		runner := fixture.runner(t)
		go func() {
			_, err := runner.Run(ctx)
			done <- err
		}()
		waitForRunFile(t, readyPath)
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		events := readEvents(t, fixture.ledgerPath)
		terminal := events[len(events)-1]
		if terminal.StateTo != domain.StateCancelled {
			t.Fatalf("state = %s", terminal.StateTo)
		}
		assertCause(t, terminal, runfailure.CauseInterrupted, runfailure.PhaseImplementation)
	})
}

func assertCause(t *testing.T, event ledger.Event, cause runfailure.Cause, phase runfailure.Phase) {
	t.Helper()
	detail := recordedFailure(t, event)
	if detail.Cause != cause || detail.Phase != phase || detail.Engine != "codex" || detail.Attempts != 1 {
		t.Fatalf("detail = %+v, want %s in %s", detail, cause, phase)
	}
}

func recordedFailure(t *testing.T, event ledger.Event) runfailure.Detail {
	t.Helper()
	detail, ok := runfailure.FromPayload(event.Payload[runfailure.PayloadKey])
	if !ok {
		t.Fatalf("no failure detail in %s -> %s: %v", event.StateFrom, event.StateTo, event.Payload)
	}
	return detail
}

func recordedFailureEvidence(t *testing.T, event ledger.Event) runfailure.Evidence {
	t.Helper()
	for _, ref := range event.EvidenceRefs {
		if ref.Kind != runfailure.EvidenceKind {
			continue
		}
		if filepath.Base(ref.URI) != runfailure.EvidenceName {
			t.Fatalf("evidence name = %s", ref.URI)
		}
		data, err := os.ReadFile(ref.URI)
		if err != nil {
			t.Fatal(err)
		}
		var body runfailure.Evidence
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil || body.SchemaVersion != runfailure.EvidenceSchemaVersion {
			t.Fatalf("evidence body %s: %v", data, err)
		}
		return body
	}
	t.Fatalf("no %s evidence in %v", runfailure.EvidenceKind, event.EvidenceRefs)
	return runfailure.Evidence{}
}

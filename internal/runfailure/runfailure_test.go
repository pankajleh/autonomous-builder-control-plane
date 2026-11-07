package runfailure

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// The Thali Calorie Lens change of 2026-11-07 (Repo C BUILD_RELIABILITY.md §3.5), as Ralphex printed it: two new
// tests failed node-app-check, the builder repaired them, the check then failed on tests/browser.test.js, the builder
// found no reason and signalled FAILED, and Ralphex's one retry did the same.
const thaliStdout = `[26-11-07 10:07:12] starting task execution phase

--- task iteration 1 ---
the builder writes the change and its tests
[26-11-07 10:20:03] validation started: product-sandbox-smoke
[26-11-07 10:20:04] validation completed successfully: product-sandbox-smoke (1s)
[26-11-07 10:20:04] validation started: node-app-check
[26-11-07 10:21:40] deterministic validation failed; returning evidence to model for one repair turn: validation node-app-check exit 1: NODE_APP_CHECK_FAILED: tests
not ok 7 - editor approval flow
not ok 9 - administrator approval sessions

--- task iteration 2 ---
the builder repairs both tests
[26-11-07 10:25:10] validation started: product-sandbox-smoke
[26-11-07 10:25:11] validation completed successfully: product-sandbox-smoke (1s)
[26-11-07 10:25:11] validation started: node-app-check
[26-11-07 10:26:30] deterministic validation failed; returning evidence to model for one repair turn: validation node-app-check exit 1: NODE_APP_CHECK_FAILED: tests
not ok 3 - tests/browser.test.js

--- task iteration 3 ---
the builder ran the file itself: 28 of 28 passed; no assertion details, stack trace, or process exit signal
<<<RALPHEX:TASK_FAILED>>>
[26-11-07 10:28:00] task failed, retrying...

--- task iteration 4 ---
<<<RALPHEX:TASK_FAILED>>>
`

const thaliStderr = `worktree retained at /builds/run/repo/.ralphex/worktrees/branch: removal is owned by the caller
error: runner: task phase: task execution failed after retry (FAILED signal received)
`

func TestThaliEndsAsAFailedCheckTheBuilderGaveUpOn(t *testing.T) {
	transcript := ReadRalphex([]byte(thaliStdout), []byte(thaliStderr))
	if transcript.RepairTurns != 2 || transcript.TaskRetries != 1 {
		t.Fatalf("repair turns %d, retries %d", transcript.RepairTurns, transcript.TaskRetries)
	}
	ending := EndingOf(transcript, "TASK_NOT_COMPLETED")
	if ending.Cause != CauseValidationFailed || ending.Phase != PhaseImplementation || ending.Failure == nil {
		t.Fatalf("ending = %+v", ending)
	}
	failure := ending.Failure
	if failure.Check != "node-app-check" || failure.State != CheckExited || failure.ExitCode != 1 {
		t.Fatalf("failure = %+v", failure)
	}
	if failure.Output != "NODE_APP_CHECK_FAILED: tests\nnot ok 3 - tests/browser.test.js" {
		t.Fatalf("output = %q", failure.Output)
	}
}

func TestEndings(t *testing.T) {
	failedOnce := "[26-11-07 10:00:00] validation started: unit\n" +
		"[26-11-07 10:00:05] deterministic validation failed; returning evidence to model for one repair turn: validation unit exit 2: FAIL x\n"
	passedAgain := failedOnce + "[26-11-07 10:01:00] validation started: unit\n[26-11-07 10:01:05] validation completed successfully: unit (5s)\n"
	review := "[26-11-07 10:00:00] validation started: unit\n[26-11-07 10:00:05] deterministic validation failed after first review pass; invoking one model repair turn\n"
	for _, test := range []struct {
		name, stdout, stderr, class string
		cause                       Cause
		phase                       Phase
		check                       string
		state                       CheckState
		output                      string
	}{
		{"the builder gave up with no check failing", passedAgain, "error: runner: task phase: task execution failed after retry (FAILED signal received)\n", "TASK_NOT_COMPLETED",
			CauseModelSignalledFailed, PhaseImplementation, "", "", ""},
		{"a check still failing after the review's repair", review,
			"error: runner: first review: validation still failing after one repair turn: validation unit exit 1: FAIL a\nFAIL b\n", "TASK_NOT_COMPLETED",
			CauseValidationFailed, PhaseReview, "unit", CheckExited, "FAIL a\nFAIL b"},
		{"a timed-out check after the review's repair", review,
			"error: runner: final review: validation still failing after one repair turn: validation unit timed out: context deadline exceeded\n", "",
			CauseValidationFailed, PhaseReview, "unit", CheckTimedOut, "context deadline exceeded"},
		{"the builder gave up on the review's repair", review, "error: runner: first review: validation repair after first review pass failed\n", "",
			CauseValidationFailed, PhaseReview, "unit", "", ""},
		{"the review's repair timed out", review, "error: runner: first review: validation repair after first review pass timed out\n", "",
			CauseTimeout, PhaseReview, "unit", "", ""},
		{"every iteration used, a check failing", failedOnce, "error: runner: task phase: max iterations (12) reached without completion\n", "",
			CauseMaxIterations, PhaseImplementation, "unit", CheckExited, "FAIL x"},
		{"the review signalled FAILED", "", "error: runner: first review: review failed (FAILED signal received)\n", "TASK_NOT_COMPLETED",
			CauseReviewFailed, PhaseReview, "", "", ""},
		{"a review pass broke its contract", "", "error: runner: first review: first review pass emitted REVIEW_DONE after changing HEAD\n", "",
			CauseReviewFailed, PhaseReview, "", "", ""},
		{"a limit pattern", "", "error: runner: task phase: codex pattern handling: detected limit pattern: \"You've hit your usage limit\"\n", "HARD_QUOTA_EXHAUSTED",
			CauseProviderLimit, PhaseImplementation, "", "", ""},
		{"an error pattern for a rate limit", "", "error: runner: task phase: codex pattern handling: detected error pattern: \"Rate limit exceeded\"\n", "CAPACITY_RATE_LIMIT",
			CauseProviderLimit, PhaseImplementation, "", "", ""},
		{"an error pattern for a refused request", "", "error: runner: task phase: claude pattern handling: detected error pattern: \"API Error: 400\"\n", "UNKNOWN_AMBIGUOUS",
			CauseProviderRefused, PhaseImplementation, "", "", ""},
		{"an unauthorized provider", "", "error: runner: task phase: codex execution: unauthorized\n", "AUTHENTICATION_AUTHORIZATION",
			CauseProviderRefused, PhaseImplementation, "", "", ""},
		{"nothing Ralphex names", "", "error: something else\n", "UNKNOWN_AMBIGUOUS", CauseUnclassified, PhaseImplementation, "", "", ""},
		{"no error at all", "", "", "", CauseUnclassified, PhaseImplementation, "", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			ending := EndingOf(ReadRalphex([]byte(test.stdout), []byte(test.stderr)), test.class)
			if ending.Cause != test.cause || ending.Phase != test.phase {
				t.Fatalf("ending = %s %s, want %s %s", ending.Cause, ending.Phase, test.cause, test.phase)
			}
			if test.check == "" {
				if ending.Failure != nil {
					t.Fatalf("failure = %+v, want none", ending.Failure)
				}
				return
			}
			if ending.Failure == nil || ending.Failure.Check != test.check || ending.Failure.State != test.state || ending.Failure.Output != test.output {
				t.Fatalf("failure = %+v", ending.Failure)
			}
		})
	}
}

// Another check failing after the first passed again leaves the other pending; a check with a reason over several
// lines keeps them up to the next timestamped line; terminal colours are dropped.
func TestPendingFollowsTheLastFailingCheck(t *testing.T) {
	stdout := "\x1b[33m[26-11-07 10:00:00] deterministic validation failed; returning evidence to model for one repair turn: validation lint exit 1: E1\x1b[0m\n" +
		"[26-11-07 10:00:01] validation started: lint\n[26-11-07 10:00:02] validation completed successfully: lint (1s)\n" +
		"[26-11-07 10:00:02] validation started: unit\n" +
		"[26-11-07 10:00:03] deterministic validation failed; returning evidence to model for one repair turn: validation unit stalled: no subprocess output within stall timeout\n" +
		"partial output\n\n" +
		"[26-11-07 10:00:04] task failed, retrying...\n"
	transcript := ReadRalphex([]byte(stdout), nil)
	if transcript.Pending == nil || transcript.Pending.Check != "unit" || transcript.Pending.State != CheckStalled ||
		transcript.Pending.Output != "no subprocess output within stall timeout\npartial output" || transcript.RepairTurns != 2 || transcript.TaskRetries != 1 {
		t.Fatalf("transcript = %+v, pending %+v", transcript, transcript.Pending)
	}
}

// R5 reads whether the run's own governed checks last passed: the Thali run's node-app-check failed last, so no; a run
// whose checks failed and then passed again, yes; a check Ralphex never ran, no.
func TestValidationPassedIsEachChecksLastReport(t *testing.T) {
	thali := ReadRalphex([]byte(thaliStdout), []byte(thaliStderr))
	if thali.ValidationPassed([]string{"product-sandbox-smoke", "node-app-check"}) || !thali.ValidationPassed([]string{"product-sandbox-smoke"}) {
		t.Fatal("the Thali run's checks read as passed")
	}
	repaired := thaliStdout + "[26-11-07 10:31:00] validation started: product-sandbox-smoke\n" +
		"[26-11-07 10:31:01] validation completed successfully: product-sandbox-smoke (1s)\n" +
		"[26-11-07 10:31:01] validation started: node-app-check\n" +
		"[26-11-07 10:32:10] validation completed successfully: node-app-check (69s)\n"
	transcript := ReadRalphex([]byte(repaired), nil)
	if !transcript.ValidationPassed([]string{"product-sandbox-smoke", "node-app-check"}) {
		t.Fatal("checks that passed last read as failing")
	}
	if transcript.ValidationPassed([]string{"node-app-check", "lint"}) || transcript.ValidationPassed(nil) {
		t.Fatal("a check never run, or none, read as passed")
	}
	review := repaired + "[26-11-07 10:40:00] validation started: node-app-check\n" +
		"[26-11-07 10:41:00] deterministic validation failed after review pass 1; invoking one model repair turn\n"
	if ReadRalphex([]byte(review), nil).ValidationPassed([]string{"node-app-check"}) {
		t.Fatal("a check the review's repair turn was invoked for read as passed")
	}
}

// A line longer than any scanner buffer does not hide the lines after it.
func TestALongLineHidesNothingAfterIt(t *testing.T) {
	stdout := strings.Repeat("x", 3<<20) + "\n[26-11-07 10:00:00] deterministic validation failed; returning evidence to model for one repair turn: validation unit exit 1: FAIL\n"
	if transcript := ReadRalphex([]byte(stdout), nil); transcript.RepairTurns != 1 || transcript.Pending == nil || transcript.Pending.Check != "unit" {
		t.Fatalf("transcript = %+v", transcript)
	}
}

func TestDetailPayloadRoundTripsThroughTheLedgerAndRefusesAnythingElse(t *testing.T) {
	code := 1
	detail := Detail{Cause: CauseValidationFailed, Phase: PhaseImplementation, Check: "node-app-check", CheckState: CheckExited, CheckExitCode: &code,
		RepairTurns: 2, TaskRetries: 1, TaskRetryLimit: 3, Attempts: 1, Engine: "codex", Provider: "together"}
	encoded, err := json.Marshal(detail.Payload())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"attempts":1,"cause":"VALIDATION_FAILED","check":"node-app-check","check_exit_code":1,"check_state":"EXITED","engine":"codex","phase":"IMPLEMENTATION","provider":"together","record_schema_version":1,"repair_turns":2,"task_retries":1,"task_retry_limit":3}`
	if string(encoded) != want {
		t.Fatalf("payload = %s", encoded)
	}
	decoded := ledgerValue(t, encoded)
	got, ok := FromPayload(decoded)
	if !ok || got.Cause != detail.Cause || got.Check != detail.Check || got.CheckExitCode == nil || *got.CheckExitCode != 1 ||
		got.RepairTurns != 2 || got.TaskRetries != 1 || got.TaskRetryLimit != 3 || got.Attempts != 1 || got.Engine != "codex" || got.Provider != "together" {
		t.Fatalf("round trip = %+v, %v", got, ok)
	}
	minimal, _ := json.Marshal(Detail{Cause: CauseSandbox, Phase: PhaseSetup, Check: "has spaces"}.Payload())
	if string(minimal) != `{"cause":"SANDBOX","phase":"SETUP","record_schema_version":1}` {
		t.Fatalf("minimal payload = %s", minimal)
	}
	for name, raw := range map[string]string{
		"unknown key":          `{"record_schema_version":1,"cause":"SANDBOX","phase":"SETUP","words":"x"}`,
		"unknown cause":        `{"record_schema_version":1,"cause":"SOMETHING","phase":"SETUP"}`,
		"lower-case cause":     `{"record_schema_version":1,"cause":"sandbox","phase":"SETUP"}`,
		"unknown phase":        `{"record_schema_version":1,"cause":"SANDBOX","phase":"LUNCH"}`,
		"no cause":             `{"record_schema_version":1,"phase":"SETUP"}`,
		"other version":        `{"record_schema_version":2,"cause":"SANDBOX","phase":"SETUP"}`,
		"free-text check":      `{"record_schema_version":1,"cause":"VALIDATION_FAILED","phase":"REVIEW","check":"the /home/x check"}`,
		"exit code, no check":  `{"record_schema_version":1,"cause":"VALIDATION_FAILED","phase":"REVIEW","check_exit_code":1}`,
		"exit code, timed out": `{"record_schema_version":1,"cause":"VALIDATION_FAILED","phase":"REVIEW","check":"unit","check_state":"TIMED_OUT","check_exit_code":1}`,
		"fractional count":     `{"record_schema_version":1,"cause":"SANDBOX","phase":"SETUP","repair_turns":1.5}`,
		"string count":         `{"record_schema_version":1,"cause":"SANDBOX","phase":"SETUP","repair_turns":"2"}`,
		"engine with a path":   `{"record_schema_version":1,"cause":"SANDBOX","phase":"SETUP","engine":"/usr/bin/codex"}`,
		"not an object":        `"VALIDATION_FAILED"`,
	} {
		if got, ok := FromPayload(ledgerValue(t, []byte(raw))); ok {
			t.Fatalf("%s: read %+v", name, got)
		}
	}
}

func ledgerValue(t *testing.T, data []byte) any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestCleanRedactsAndBoundsTheCheckOutput(t *testing.T) {
	text, truncated := Clean("\x1b[31mnot ok 1 - login\x1b[0m\x00\n  token=sk-abcdefghijklmnop1234\n", MaxCheckOutputBytes)
	if truncated || strings.Contains(text, "sk-abcdefghijklmnop1234") || strings.Contains(text, "\x1b") || strings.Contains(text, "\x00") ||
		!strings.HasPrefix(text, "not ok 1 - login\n  token=") {
		t.Fatalf("clean = %q, %v", text, truncated)
	}
	long := "HEAD " + strings.Repeat("é", 4000) + " TAIL"
	text, truncated = Clean(long, MaxCheckOutputBytes)
	if !truncated || len(text) > MaxCheckOutputBytes || !utf8.ValidString(text) || !strings.HasPrefix(text, "HEAD ") || !strings.HasSuffix(text, " TAIL") ||
		!strings.Contains(text, "[truncated]") {
		t.Fatalf("bounded clean: %d bytes, truncated %v, valid %v", len(text), truncated, utf8.ValidString(text))
	}
}

func TestEveryCauseIsKnownAndUpperCase(t *testing.T) {
	for cause := range causes {
		if !cause.Known() || strings.ToUpper(string(cause)) != string(cause) {
			t.Fatalf("cause %q", cause)
		}
	}
	if Cause("TASK_NOT_COMPLETED").Known() {
		t.Fatal("the classifier's class is not a cause")
	}
}

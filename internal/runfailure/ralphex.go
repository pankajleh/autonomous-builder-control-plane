package runfailure

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
)

// Ralphex's own lines this reads (the pinned governance release; Ralphex
// prints them, the builder does not):
//
//   - task phase, after a builder turn whose governed validation failed:
//     "deterministic validation failed; returning evidence to model for one
//     repair turn: validation <id> exit N: <output>" (or "timed out", "stalled");
//   - review phase, after a pass that changed HEAD: "deterministic validation
//     failed after <pass>; invoking one model repair turn" (the evidence is in
//     the error it ends with);
//   - "validation started: <id>", "validation completed successfully: <id> (…)";
//   - "task failed, retrying..." before Ralphex's retry after a FAILED signal;
//   - on standard error, the error the run ended with: "error: <error>", whose
//     own lines follow it.
const (
	taskRepairPrefix   = "deterministic validation failed; returning evidence to model for one repair turn: "
	reviewRepairPrefix = "deterministic validation failed after "
	startedPrefix      = "validation started: "
	completedPrefix    = "validation completed successfully: "
	retryLine          = "task failed, retrying..."
	errorPrefix        = "error: "
	stillFailingPhrase = "validation still failing after one repair turn: "
)

var (
	// timestampPattern is Ralphex's log prefix, "[YY-MM-DD HH:MM:SS] ".
	timestampPattern = regexp.MustCompile(`^\[\d{2}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\] `)
	sectionPattern   = regexp.MustCompile(`^--- .+ ---$`)
	failurePattern   = regexp.MustCompile(`^validation ([A-Za-z0-9][A-Za-z0-9._-]{0,127}) (exit (-?\d{1,3})|timed out|stalled)(?::|$)`)
)

// ValidationFailure is one failed governed validation, as Ralphex reported it.
type ValidationFailure struct {
	Check    string
	State    CheckState
	ExitCode int
	Output   string
	Review   bool
}

// Transcript is what a Ralphex run's own lines say about how it ended.
type Transcript struct {
	// RepairTurns counts the turns the builder was given a failed check's
	// evidence; TaskRetries Ralphex's retries after a FAILED signal.
	RepairTurns int
	TaskRetries int
	// Pending is the last failed validation not passed again since; nil when
	// none failed or the failing check passed afterwards.
	Pending *ValidationFailure
	// FinalError is the error Ralphex ended with, from standard error.
	FinalError string
	// lastPassed is each governed check's last report: true for "validation completed successfully", false for a
	// failure Ralphex reported after it (R5).
	lastPassed map[string]bool
}

// ValidationPassed reports whether each of the governed checks ids was last reported passing by Ralphex: run, and not
// failed since (R5, docs/plans/build-recovery.md §6). False when ids is empty.
func (t Transcript) ValidationPassed(ids []string) bool {
	for _, id := range ids {
		if !t.lastPassed[id] {
			return false
		}
	}
	return len(ids) > 0
}

// maxCapturedOutput bounds the output kept while reading; Clean bounds it again.
const maxCapturedOutput = 64 << 10

// ReadRalphex reads Ralphex's standard output and error.
func ReadRalphex(stdout, stderr []byte) Transcript {
	t := Transcript{lastPassed: map[string]bool{}}
	lastStarted := ""
	var capturing *ValidationFailure
	var captured strings.Builder
	flush := func() {
		if capturing != nil {
			capturing.Output = strings.TrimSpace(capturing.Output + captured.String())
			capturing = nil
		}
		captured.Reset()
	}
	scanLines(stdout, func(raw string) {
		line := strings.TrimRight(ansiPattern.ReplaceAllString(raw, ""), "\r")
		stamp := timestampPattern.FindString(line)
		if stamp == "" {
			if sectionPattern.MatchString(strings.TrimSpace(line)) {
				flush()
				return
			}
			if capturing != nil && captured.Len() < maxCapturedOutput {
				captured.WriteByte('\n')
				captured.WriteString(line)
			}
			return
		}
		flush()
		message := line[len(stamp):]
		switch {
		case strings.HasPrefix(message, taskRepairPrefix):
			t.RepairTurns++
			failure, ok := parseFailure(strings.TrimPrefix(message, taskRepairPrefix))
			if !ok {
				failure = ValidationFailure{Check: lastStarted}
			}
			t.Pending = &failure
			capturing = t.Pending
			t.lastPassed[failure.Check] = false
			lastStarted = ""
		case strings.HasPrefix(message, reviewRepairPrefix) && strings.HasSuffix(message, "; invoking one model repair turn"):
			t.RepairTurns++
			t.Pending = &ValidationFailure{Check: lastStarted, Review: true}
			t.lastPassed[lastStarted] = false
			lastStarted = ""
		case strings.HasPrefix(message, startedPrefix):
			lastStarted = strings.TrimSpace(strings.TrimPrefix(message, startedPrefix))
		case strings.HasPrefix(message, completedPrefix):
			passed := strings.TrimPrefix(message, completedPrefix)
			if index := strings.Index(passed, " ("); index >= 0 {
				passed = passed[:index]
			}
			if t.Pending != nil && t.Pending.Check == strings.TrimSpace(passed) {
				t.Pending = nil
			}
			t.lastPassed[strings.TrimSpace(passed)] = true
			lastStarted = ""
		case message == retryLine:
			t.TaskRetries++
		}
	})
	flush()
	// The error Ralphex ended with is its last "error: " line; the lines after it
	// are the error's own (a validation failure's output).
	var final []string
	scanLines(stderr, func(raw string) {
		line := strings.TrimRight(ansiPattern.ReplaceAllString(raw, ""), "\r")
		if strings.HasPrefix(line, errorPrefix) {
			final = []string{strings.TrimPrefix(line, errorPrefix)}
			return
		}
		if final != nil && len(final) < 4096 {
			final = append(final, line)
		}
	})
	t.FinalError = strings.TrimSpace(strings.Join(final, "\n"))
	return t
}

// scanLines calls line for each line of data, however long: a builder's streamed output can hold a line longer than
// any scanner buffer, and the lines after it still count.
func scanLines(data []byte, line func(string)) {
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			line(string(data))
			return
		}
		line(string(data[:end]))
		data = data[end+1:]
	}
}

// parseFailure reads "validation <id> exit N: <output>" (or "timed out",
// "stalled"); the output may go on over the next lines.
func parseFailure(text string) (ValidationFailure, bool) {
	match := failurePattern.FindStringSubmatch(text)
	if match == nil {
		return ValidationFailure{}, false
	}
	failure := ValidationFailure{Check: match[1], Output: strings.TrimSpace(strings.TrimPrefix(text[len(match[0]):], " "))}
	switch {
	case match[3] != "":
		code, err := strconv.Atoi(match[3])
		if err != nil {
			return ValidationFailure{}, false
		}
		failure.State, failure.ExitCode = CheckExited, code
	case match[2] == "timed out":
		failure.State = CheckTimedOut
	default:
		failure.State = CheckStalled
	}
	return failure, true
}

// Ending is how a Ralphex run that did not succeed ended, read from its
// transcript: the cause, the phase, and the failing check when one is known.
type Ending struct {
	Cause   Cause
	Phase   Phase
	Failure *ValidationFailure
}

// EndingOf classifies the ending from the error Ralphex ended with and the
// transcript. providerClass is the classifier's class of the run's output
// (internal/blocker), used only when Ralphex's own words name no cause.
func EndingOf(t Transcript, providerClass string) Ending {
	final := strings.ToLower(t.FinalError)
	phase := PhaseImplementation
	if strings.Contains(final, "first review:") || strings.Contains(final, "final review:") || strings.Contains(final, "review failed") {
		phase = PhaseReview
	}
	pending := t.Pending
	if pending != nil && pending.Review {
		phase = PhaseReview
	}
	ending := Ending{Phase: phase}
	switch {
	case strings.Contains(final, stillFailingPhrase):
		ending.Cause = CauseValidationFailed
		ending.Phase = PhaseReview
		text := t.FinalError[strings.Index(final, stillFailingPhrase)+len(stillFailingPhrase):]
		if failure, ok := parseFailure(text); ok {
			failure.Review = true
			ending.Failure = &failure
		} else {
			ending.Failure = pending
		}
	case strings.Contains(final, "validation repair after") && strings.Contains(final, "timed out"):
		ending.Cause, ending.Phase, ending.Failure = CauseTimeout, PhaseReview, pending
	case strings.Contains(final, "validation repair after") && strings.HasSuffix(strings.TrimSpace(final), "failed"):
		ending.Cause, ending.Phase, ending.Failure = CauseValidationFailed, PhaseReview, pending
	case strings.Contains(final, "task execution failed after retry"):
		ending.Cause = CauseModelSignalledFailed
		if pending != nil {
			ending.Cause, ending.Failure = CauseValidationFailed, pending
		}
	case strings.Contains(final, "max iterations (") && strings.Contains(final, "reached without completion"):
		ending.Cause, ending.Failure = CauseMaxIterations, pending
	case strings.Contains(final, "review failed (failed signal received)"),
		strings.Contains(final, "did not emit review_done"),
		strings.Contains(final, "reported findings without applying fixes"),
		strings.Contains(final, "emitted review_done after changing head"):
		ending.Cause, ending.Phase = CauseReviewFailed, PhaseReview
	case strings.Contains(final, "detected limit pattern"):
		ending.Cause = CauseProviderLimit
	case strings.Contains(final, " timed out"):
		ending.Cause = CauseTimeout
	default:
		ending.Cause = CauseOfClass(providerClass)
		if strings.Contains(final, "detected error pattern") && ending.Cause != CauseProviderLimit {
			ending.Cause = CauseProviderRefused
		}
	}
	return ending
}

// CauseOfClass maps the classifier's class of the run's output to a cause.
func CauseOfClass(class string) Cause {
	switch class {
	case "CAPACITY_RATE_LIMIT", "HARD_QUOTA_EXHAUSTED", "BILLING_ACCOUNT_FAILURE":
		return CauseProviderLimit
	case "AUTHENTICATION_AUTHORIZATION", "SECRET_REQUIRED":
		return CauseProviderRefused
	case "TASK_NOT_COMPLETED":
		return CauseModelSignalledFailed
	case "VALIDATION_UNAVAILABLE", "POLICY_BLOCKED", "EXTERNAL_DEPENDENCY":
		return CauseControllerError
	default:
		return CauseUnclassified
	}
}

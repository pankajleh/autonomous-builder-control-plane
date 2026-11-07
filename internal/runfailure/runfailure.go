// Package runfailure is the structured failure of a run that ended without an
// accepted branch (Repo C design note BUILD_RELIABILITY.md §5.6, R1;
// docs/plans/build-recovery.md): a cause from a fixed list, the phase, the
// check that failed and how often the builder was sent back to repair it, kept
// in the ledger as fixed codes and small numbers only, and the check's own
// output (redacted, at most 4 KB) kept as evidence beside it.
//
// The runner writes it (internal/run); GET /v1/runs/{run}/failure reads it
// back (internal/timeline). Neither the ledger form nor the evidence form ever
// carries free text the person wrote: the ledger is kept when a run is
// forgotten, so the check's output, which can name the person's files, is
// evidence only.
package runfailure

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/blocker"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
)

// Cause says why a run ended without an accepted branch. A reader treats a
// cause it does not know as UNCLASSIFIED.
type Cause string

const (
	// CauseValidationFailed: a governed validation check still failed when the
	// builder's attempt ended (its repair turns and retries used up, or the
	// builder gave up on it). Check names it.
	CauseValidationFailed Cause = "VALIDATION_FAILED"
	// CauseModelSignalledFailed: the builder signalled FAILED, with no failing
	// check pending, after its retry.
	CauseModelSignalledFailed Cause = "MODEL_SIGNALLED_FAILED"
	// CauseMaxIterations: the builder used every iteration without finishing.
	CauseMaxIterations Cause = "MAX_ITERATIONS"
	// CauseReviewFailed: the review signalled FAILED or broke its contract.
	CauseReviewFailed Cause = "REVIEW_FAILED"
	// CauseAcceptanceFailed: the built branch failed ABCP's final acceptance.
	CauseAcceptanceFailed Cause = "ACCEPTANCE_FAILED"
	// CauseAcceptanceUnavailable: ABCP's final acceptance could not run.
	CauseAcceptanceUnavailable Cause = "ACCEPTANCE_UNAVAILABLE"
	// CauseProviderRefused: the engine's provider refused the request (its
	// shape, the key, the account), or an open-model engine could not be readied.
	CauseProviderRefused Cause = "PROVIDER_REFUSED"
	// CauseProviderLimit: a rate limit, quota, usage limit or exhausted balance.
	CauseProviderLimit Cause = "PROVIDER_LIMIT"
	// CauseTimeout: the build or one of its steps ran past its time limit.
	CauseTimeout Cause = "TIMEOUT"
	// CauseSandbox: the build wall could not start, or the build rewrote its
	// branch inside it.
	CauseSandbox Cause = "SANDBOX"
	// CauseInterrupted: the build was stopped by a signal, not by a request (a
	// restart of ABCP or the host).
	CauseInterrupted Cause = "INTERRUPTED"
	// CauseCancelled: the build was stopped by a cancel request (Stop build, or
	// a stop for credits).
	CauseCancelled Cause = "CANCELLED"
	// CauseAdmissionFailed: the run's authority did not validate before it ran.
	CauseAdmissionFailed Cause = "ADMISSION_FAILED"
	// CauseExactEditDoesNotApply: an exact edit's text is not in its file.
	CauseExactEditDoesNotApply Cause = "EXACT_EDIT_DOES_NOT_APPLY"
	// CauseControllerError: one of ABCP's own steps failed.
	CauseControllerError Cause = "CONTROLLER_ERROR"
	// CauseUnclassified: none of the above could be told.
	CauseUnclassified Cause = "UNCLASSIFIED"
)

var causes = map[Cause]bool{
	CauseValidationFailed: true, CauseModelSignalledFailed: true, CauseMaxIterations: true, CauseReviewFailed: true,
	CauseAcceptanceFailed: true, CauseAcceptanceUnavailable: true, CauseProviderRefused: true, CauseProviderLimit: true,
	CauseTimeout: true, CauseSandbox: true, CauseInterrupted: true, CauseCancelled: true, CauseAdmissionFailed: true,
	CauseExactEditDoesNotApply: true, CauseControllerError: true, CauseUnclassified: true,
}

// Known reports whether cause is one of the fixed list.
func (c Cause) Known() bool { return causes[c] }

// Phase is the part of the run that failed.
type Phase string

const (
	PhaseAdmission      Phase = "ADMISSION"
	PhaseSetup          Phase = "SETUP"
	PhaseImplementation Phase = "IMPLEMENTATION"
	PhaseReview         Phase = "REVIEW"
	PhaseAcceptance     Phase = "ACCEPTANCE"
)

var phases = map[Phase]bool{PhaseAdmission: true, PhaseSetup: true, PhaseImplementation: true, PhaseReview: true, PhaseAcceptance: true}

// PhaseFrom is the phase a run was in when it left state.
func PhaseFrom(state domain.State) Phase {
	switch state {
	case domain.StateRunCreated, domain.StateHumanDecisionRequired:
		return PhaseAdmission
	case domain.StateAuthorityValidated, domain.StateExecutionStarting:
		return PhaseSetup
	case domain.StateImplementationCompleted, domain.StateBranchAcceptancePending:
		return PhaseAcceptance
	default:
		return PhaseImplementation
	}
}

// CheckState is how the failing check ended.
type CheckState string

const (
	CheckExited   CheckState = "EXITED"
	CheckTimedOut CheckState = "TIMED_OUT"
	CheckStalled  CheckState = "STALLED"
)

var checkStates = map[CheckState]bool{CheckExited: true, CheckTimedOut: true, CheckStalled: true}

// RecordSchemaVersion is the version of the ledger form.
const RecordSchemaVersion = 1

// PayloadKey is the key of the ledger form in a stop transition's payload.
const PayloadKey = "failure_detail"

// Detail is the ledger form: fixed codes and small counts only.
type Detail struct {
	Cause Cause
	Phase Phase
	// Check is the failing validation command's id, or the failing acceptance
	// command's name (acceptance-<n> when its name is not an identifier).
	Check      string
	CheckState CheckState
	// CheckExitCode is set when CheckState is EXITED.
	CheckExitCode *int
	// RepairTurns counts the builder's turns given a failed check's evidence
	// (Ralphex's "deterministic validation failed" lines).
	RepairTurns int
	// TaskRetries counts Ralphex's retries after the builder signalled FAILED
	// ("task failed, retrying..."); TaskRetryLimit is the profile's
	// ralphex.task_retry_count, or Ralphex's own default of 1.
	TaskRetries    int
	TaskRetryLimit int
	// Attempts is the number of builder attempts the run made (1 until a fresh
	// second attempt exists, R3).
	Attempts int
	// Engine is the executor (codex, claude); Provider the open-model provider.
	Engine   string
	Provider string
}

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	enginePattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// ValidCheck reports whether a check name may be kept in the ledger: the
// identifier shape Ralphex gives validation command ids.
func ValidCheck(name string) bool { return identifierPattern.MatchString(name) }

const maxCount = 1 << 20

// Payload is the ledger form of d. Empty fields are left out.
func (d Detail) Payload() map[string]any {
	payload := map[string]any{"record_schema_version": RecordSchemaVersion, "cause": string(d.Cause), "phase": string(d.Phase)}
	if d.Check != "" && ValidCheck(d.Check) {
		payload["check"] = d.Check
		if checkStates[d.CheckState] {
			payload["check_state"] = string(d.CheckState)
		}
		if d.CheckState == CheckExited && d.CheckExitCode != nil && *d.CheckExitCode >= -1 && *d.CheckExitCode <= 255 {
			payload["check_exit_code"] = *d.CheckExitCode
		}
	}
	for key, value := range map[string]int{"repair_turns": d.RepairTurns, "task_retries": d.TaskRetries, "task_retry_limit": d.TaskRetryLimit, "attempts": d.Attempts} {
		if value > 0 {
			payload[key] = min(value, maxCount)
		}
	}
	if enginePattern.MatchString(d.Engine) {
		payload["engine"] = d.Engine
	}
	if enginePattern.MatchString(d.Provider) {
		payload["provider"] = d.Provider
	}
	return payload
}

// FromPayload reads the ledger form back. Anything but exactly that form (an
// unknown key, a value of another shape, an unknown cause or phase) answers
// false, so no free text reaches a reader through it.
func FromPayload(value any) (Detail, bool) {
	payload, ok := value.(map[string]any)
	if !ok {
		return Detail{}, false
	}
	var d Detail
	for key, raw := range payload {
		switch key {
		case "record_schema_version":
			if number, ok := count(raw); !ok || number != RecordSchemaVersion {
				return Detail{}, false
			}
		case "cause":
			text, _ := raw.(string)
			if !Cause(text).Known() {
				return Detail{}, false
			}
			d.Cause = Cause(text)
		case "phase":
			text, _ := raw.(string)
			if !phases[Phase(text)] {
				return Detail{}, false
			}
			d.Phase = Phase(text)
		case "check":
			text, _ := raw.(string)
			if !ValidCheck(text) {
				return Detail{}, false
			}
			d.Check = text
		case "check_state":
			text, _ := raw.(string)
			if !checkStates[CheckState(text)] {
				return Detail{}, false
			}
			d.CheckState = CheckState(text)
		case "check_exit_code":
			number, ok := integer(raw)
			if !ok || number < -1 || number > 255 {
				return Detail{}, false
			}
			d.CheckExitCode = &number
		case "repair_turns", "task_retries", "task_retry_limit", "attempts":
			number, ok := count(raw)
			if !ok || number < 1 {
				return Detail{}, false
			}
			switch key {
			case "repair_turns":
				d.RepairTurns = number
			case "task_retries":
				d.TaskRetries = number
			case "task_retry_limit":
				d.TaskRetryLimit = number
			default:
				d.Attempts = number
			}
		case "engine", "provider":
			text, _ := raw.(string)
			if !enginePattern.MatchString(text) {
				return Detail{}, false
			}
			if key == "engine" {
				d.Engine = text
			} else {
				d.Provider = text
			}
		default:
			return Detail{}, false
		}
	}
	if d.Cause == "" || d.Phase == "" || (d.Check == "" && (d.CheckState != "" || d.CheckExitCode != nil)) ||
		(d.CheckExitCode != nil && d.CheckState != CheckExited) {
		return Detail{}, false
	}
	return d, true
}

func count(raw any) (int, bool) {
	number, ok := integer(raw)
	return number, ok && number >= 0 && number <= maxCount
}

func integer(raw any) (int, bool) {
	switch value := raw.(type) {
	case json.Number:
		parsed, err := value.Int64()
		if err != nil || parsed < math.MinInt32 || parsed > math.MaxInt32 {
			return 0, false
		}
		return int(parsed), true
	case float64:
		if value != math.Trunc(value) || value < math.MinInt32 || value > math.MaxInt32 {
			return 0, false
		}
		return int(value), true
	case int:
		return value, true
	default:
		return 0, false
	}
}

// Evidence is the failure's evidence body: the failing check's own output and
// the builder's last error, redacted and bounded. It is removed with the run's
// other evidence bodies when the run is forgotten.
type Evidence struct {
	SchemaVersion        string `json:"schema_version"`
	Cause                Cause  `json:"cause"`
	Check                string `json:"check,omitempty"`
	CheckOutput          string `json:"check_output,omitempty"`
	CheckOutputTruncated bool   `json:"check_output_truncated,omitempty"`
	LastError            string `json:"last_error,omitempty"`
	LastErrorTruncated   bool   `json:"last_error_truncated,omitempty"`
}

const (
	// EvidenceSchemaVersion is Evidence's schema_version.
	EvidenceSchemaVersion = "RunFailureDetailV1"
	// EvidenceKind is the kind of the evidence body's reference.
	EvidenceKind = "run-failure-detail"
	// EvidenceName is the body's name; a later one of the same run is
	// failure-detail-2.json and so on.
	EvidenceName = "failure-detail.json"
	// MaxCheckOutputBytes bounds CheckOutput: the design's "the check's own
	// first 4 KB", kept as its head and its tail when longer.
	MaxCheckOutputBytes = 4 << 10
	// MaxLastErrorBytes bounds LastError.
	MaxLastErrorBytes = 1 << 10
	// MaxEvidenceBytes bounds the encoded body a reader accepts.
	MaxEvidenceBytes = 16 << 10
)

var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// Clean redacts text (blocker.Redact), drops terminal escapes and control
// characters but newlines and tabs, and bounds it to limit bytes of valid
// UTF-8: when longer, three quarters from its start and the rest from its end,
// around a "[truncated]" line.
func Clean(text string, limit int) (string, bool) {
	text = strings.ToValidUTF8(text, "�")
	text = ansiPattern.ReplaceAllString(text, "")
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, text)
	text = strings.TrimSpace(blocker.Redact(text))
	if len(text) <= limit {
		return text, false
	}
	const marker = "\n…[truncated]…\n"
	room := limit - len(marker)
	if room < 8 {
		return cutBefore(text, limit), true
	}
	head := room * 3 / 4
	return cutBefore(text, head) + marker + cutAfter(text, len(text)-(room-head)), true
}

// cutBefore is text's longest prefix of at most n bytes ending on a rune boundary.
func cutBefore(text string, n int) string {
	if n >= len(text) {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}

// cutAfter is text from byte at, moved forward to a rune boundary.
func cutAfter(text string, at int) string {
	if at <= 0 {
		return text
	}
	for at < len(text) && !utf8.RuneStart(text[at]) {
		at++
	}
	return text[at:]
}

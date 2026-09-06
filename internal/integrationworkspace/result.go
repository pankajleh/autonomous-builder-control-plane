// Package integrationworkspace evaluates accepted candidates in a
// controller-owned disposable Git repository. It produces textual integration
// evidence only; it cannot classify semantic conflicts or authorize a merge.
package integrationworkspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/scheduler"
)

// Status is the fail-closed textual integration outcome.
type Status string

const (
	StatusClean       Status = "CLEAN_TEXTUAL_INTEGRATION"
	StatusConflict    Status = "TEXTUAL_CONFLICT"
	StatusUnavailable Status = "UNAVAILABLE"
)

// StepOutcome describes what happened when one exact candidate head was
// applied in the disposable repository.
type StepOutcome string

const (
	StepMerged           StepOutcome = "MERGED"
	StepAlreadyContained StepOutcome = "ALREADY_CONTAINED"
	StepConflict         StepOutcome = "TEXTUAL_CONFLICT"
)

// CommandEvidence contains the complete bounded output and exact structured
// argv for one Git command. A truncated command can never appear in a clean or
// conflict result.
type CommandEvidence struct {
	Argv             []string `json:"argv"`
	ExitCode         int      `json:"exit_code"`
	Stdout           []byte   `json:"stdout"`
	Stderr           []byte   `json:"stderr"`
	StdoutSHA256     string   `json:"stdout_sha256"`
	StderrSHA256     string   `json:"stderr_sha256"`
	StdoutBytes      int      `json:"stdout_bytes"`
	StderrBytes      int      `json:"stderr_bytes"`
	StdoutLimitBytes int      `json:"stdout_limit_bytes"`
	StderrLimitBytes int      `json:"stderr_limit_bytes"`
	StdoutTruncated  bool     `json:"stdout_truncated"`
	StderrTruncated  bool     `json:"stderr_truncated"`
}

// CandidateStep binds an application outcome to an immutable accepted
// candidate and the exact disposable-repository heads before and after it.
type CandidateStep struct {
	Candidate     scheduler.AcceptedCandidate `json:"candidate"`
	Outcome       StepOutcome                 `json:"outcome"`
	BeforeSHA     string                      `json:"before_sha"`
	AfterSHA      string                      `json:"after_sha,omitempty"`
	ConflictPaths []string                    `json:"conflict_paths,omitempty"`
	CommandStart  int                         `json:"command_start"`
	CommandEnd    int                         `json:"command_end"`
}

// CleanupEvidence records the deterministic cleanup outcome. The exact
// disposable path is operational data kept only in the separate cleanup
// receipt, so random workspace identity cannot affect canonical result bytes.
type CleanupEvidence struct {
	WorkspaceRemoved bool `json:"workspace_removed"`
}

// operationalCleanupEvidence is intentionally confined to the non-canonical
// cleanup receipt that is published after the exact workspace is removed.
type operationalCleanupEvidence struct {
	TemporaryRoot    string `json:"temporary_root"`
	WorkspacePath    string `json:"workspace_path"`
	WorkspaceID      string `json:"workspace_id"`
	WorkspaceRemoved bool   `json:"workspace_removed"`
}

type resultRecord struct {
	SchemaVersion    int                           `json:"schema_version"`
	Status           Status                        `json:"status"`
	Failure          string                        `json:"failure,omitempty"`
	BaselineSHA      string                        `json:"baseline_sha,omitempty"`
	RiskReportSHA256 string                        `json:"risk_report_sha256,omitempty"`
	RiskClass        scheduler.RiskClass           `json:"risk_class,omitempty"`
	Candidates       []scheduler.AcceptedCandidate `json:"candidates,omitempty"`
	Steps            []CandidateStep               `json:"steps,omitempty"`
	Commands         []CommandEvidence             `json:"commands,omitempty"`
	CaptureSHA256    string                        `json:"capture_sha256,omitempty"`
	Cleanup          CleanupEvidence               `json:"cleanup"`
}

// Result is an immutable textual integration result. Every accessor returns a
// defensive copy, and MarshalJSON emits the canonical bytes hashed by SHA256.
type Result struct {
	record        resultRecord
	canonicalJSON []byte
	digest        string
	captureRef    ledger.EvidenceRef
	cleanupRef    ledger.EvidenceRef
}

func newResult(record resultRecord, captureRef, cleanupRef ledger.EvidenceRef) Result {
	record = cloneResultRecord(record)
	canonical, err := json.Marshal(record)
	if err != nil {
		// All fields are JSON-safe concrete values, so this is unreachable unless
		// a future field violates the result contract. Preserve fail-closed state.
		record = resultRecord{SchemaVersion: resultSchemaVersion, Status: StatusUnavailable, Failure: "marshal integration result: " + err.Error()}
		canonical, _ = json.Marshal(record)
	}
	digest := sha256.Sum256(canonical)
	return Result{
		record: record, canonicalJSON: canonical, digest: hex.EncodeToString(digest[:]),
		captureRef: captureRef, cleanupRef: cleanupRef,
	}
}

// Status returns the textual integration status.
func (r Result) Status() Status { return r.record.Status }

// Failure returns the fail-closed reason for an unavailable result.
func (r Result) Failure() string { return r.record.Failure }

// BaselineSHA returns the exact baseline commit used for evaluation.
func (r Result) BaselineSHA() string { return r.record.BaselineSHA }

// RiskReportSHA256 returns the frozen scheduler risk report digest.
func (r Result) RiskReportSHA256() string { return r.record.RiskReportSHA256 }

// RiskClass returns the scheduler's frozen final-diff risk classification.
func (r Result) RiskClass() scheduler.RiskClass { return r.record.RiskClass }

// Candidates returns defensive copies in governed application order.
func (r Result) Candidates() []scheduler.AcceptedCandidate {
	return cloneCandidates(r.record.Candidates)
}

// Steps returns defensive copies of completed or conflicted candidate steps.
func (r Result) Steps() []CandidateStep { return cloneSteps(r.record.Steps) }

// Commands returns defensive copies of every exact Git command outcome.
func (r Result) Commands() []CommandEvidence { return cloneCommands(r.record.Commands) }

// CaptureRef returns the immutable evidence published before cleanup.
func (r Result) CaptureRef() ledger.EvidenceRef { return r.captureRef }

// Cleanup returns the bounded cleanup outcome.
func (r Result) Cleanup() CleanupEvidence { return r.record.Cleanup }

// CleanupRef returns the immutable cleanup receipt reference.
func (r Result) CleanupRef() ledger.EvidenceRef { return r.cleanupRef }

// CanonicalJSON returns a defensive copy of the deterministic result bytes.
func (r Result) CanonicalJSON() []byte { return append([]byte(nil), r.canonicalJSON...) }

// SHA256 returns the lowercase digest of CanonicalJSON.
func (r Result) SHA256() string { return r.digest }

// MarshalJSON emits the deterministic result bytes.
func (r Result) MarshalJSON() ([]byte, error) {
	if len(r.canonicalJSON) == 0 {
		return nil, errors.New("integration result is incomplete")
	}
	return append([]byte(nil), r.canonicalJSON...), nil
}

func cloneResultRecord(record resultRecord) resultRecord {
	record.Candidates = cloneCandidates(record.Candidates)
	record.Steps = cloneSteps(record.Steps)
	record.Commands = cloneCommands(record.Commands)
	return record
}

func cloneCandidates(values []scheduler.AcceptedCandidate) []scheduler.AcceptedCandidate {
	cloned := make([]scheduler.AcceptedCandidate, len(values))
	for index, candidate := range values {
		input := candidate.Input()
		cloned[index], _ = scheduler.NewAcceptedCandidate(input)
	}
	return cloned
}

func cloneSteps(values []CandidateStep) []CandidateStep {
	cloned := make([]CandidateStep, len(values))
	for index, step := range values {
		cloned[index] = step
		candidate, _ := scheduler.NewAcceptedCandidate(step.Candidate.Input())
		cloned[index].Candidate = candidate
		cloned[index].ConflictPaths = append([]string(nil), step.ConflictPaths...)
	}
	return cloned
}

func cloneCommands(values []CommandEvidence) []CommandEvidence {
	cloned := make([]CommandEvidence, len(values))
	for index, command := range values {
		cloned[index] = command
		cloned[index].Argv = append([]string(nil), command.Argv...)
		cloned[index].Stdout = append([]byte(nil), command.Stdout...)
		cloned[index].Stderr = append([]byte(nil), command.Stderr...)
	}
	return cloned
}

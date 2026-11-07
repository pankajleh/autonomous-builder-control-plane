package timeline

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runfailure"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// RunFailureSchemaVersion is the schema of GET /v1/runs/{run}/failure (R1, docs/plans/build-recovery.md).
const RunFailureSchemaVersion = "RunFailureV1"

// RunFailureV1 says why a run ended without an accepted branch. Failure is absent while the run has not stopped, and
// for a run whose branch was accepted.
type RunFailureV1 struct {
	SchemaVersion      string     `json:"schema_version"`
	RunID              string     `json:"run_id"`
	ProjectionRevision string     `json:"projection_revision"`
	CurrentState       string     `json:"current_state"`
	Failure            *FailureV1 `json:"failure,omitempty"`
}

// FailureV1 is the stop: the transition that ended the run, R1's detail as the run recorded it (Recorded), or as it
// can be told from an older run's fixed codes (not Recorded), the failing check's own output, and the stop's evidence,
// each downloadable through GET /v1/runs/{run}/evidence/{evidence_id}. Every field but CheckOutput is a fixed code or
// a count; CheckOutput is the check's own output, redacted, at most 4 KB.
type FailureV1 struct {
	Event                EventIdentityV1     `json:"event"`
	State                string              `json:"state"`
	StateFrom            string              `json:"state_from"`
	Recorded             bool                `json:"recorded"`
	Cause                string              `json:"cause"`
	Phase                string              `json:"phase,omitempty"`
	Reason               string              `json:"reason,omitempty"`
	FailureClass         string              `json:"failure_class,omitempty"`
	Retryability         string              `json:"retryability,omitempty"`
	Check                string              `json:"check,omitempty"`
	CheckState           string              `json:"check_state,omitempty"`
	CheckExitCode        *int                `json:"check_exit_code,omitempty"`
	CheckOutput          string              `json:"check_output,omitempty"`
	CheckOutputTruncated bool                `json:"check_output_truncated,omitempty"`
	RepairTurns          int                 `json:"repair_turns"`
	TaskRetries          int                 `json:"task_retries"`
	TaskRetryLimit       int                 `json:"task_retry_limit,omitempty"`
	Attempts             int                 `json:"attempts,omitempty"`
	Engine               string              `json:"engine,omitempty"`
	Provider             string              `json:"provider,omitempty"`
	Evidence             []FailureEvidenceV1 `json:"evidence"`
}

// FailureEvidenceV1 is one evidence item of the stop.
type FailureEvidenceV1 struct {
	EvidenceID string `json:"evidence_id"`
	Kind       string `json:"kind,omitempty"`
}

var _ serviceapi.RunFailureReader = (*Service)(nil)

// ReadRunFailure answers RunFailureV1 from one authoritative snapshot of the run's ledger and, for the check's
// output, its verified failure-detail evidence. It writes nothing.
func (s *Service) ReadRunFailure(ctx context.Context, runID string) (json.RawMessage, error) {
	if s == nil || ctx == nil || runtimecatalog.ValidateIdentifier(runID) != nil {
		return nil, serviceapi.ErrDependencyNotFound
	}
	registration, snapshot, err := s.authoritativeSnapshot(ctx, runID)
	if err != nil {
		return nil, err
	}
	response := RunFailureV1{
		SchemaVersion: RunFailureSchemaVersion, RunID: runID,
		ProjectionRevision: snapshot.Projection.ProjectionRevision, CurrentState: snapshot.Projection.CurrentState,
	}
	index := -1
	for i := len(snapshot.Events) - 1; i >= 0; i-- {
		if snapshot.Events[i].StateTo != "" {
			index = i
			break
		}
	}
	if index >= 0 && stopState(snapshot.Events[index].StateTo) && string(snapshot.Events[index].StateTo) == response.CurrentState {
		failure, err := s.describeFailure(ctx, registration, snapshot.Events[index], uint64(index+1))
		if err != nil {
			return nil, err
		}
		response.Failure = &failure
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, serviceapi.ErrProjectionIntegrity
	}
	return encoded, nil
}

// stopState: the states a run stops in without an accepted branch.
func stopState(state domain.State) bool {
	return state == domain.StateFailed || state == domain.StateCancelled || state == domain.StateValidationUnavailable
}

func (s *Service) describeFailure(ctx context.Context, registration runtimecatalog.RunRegistrationV1, event ledger.Event, ordinal uint64) (FailureV1, error) {
	failure := FailureV1{
		Event: EventIdentityV1{
			Ordinal: ordinal, EventID: safeExternal(event.EventID, registration), Timestamp: event.Timestamp.UTC().Format(time.RFC3339Nano),
		},
		State: string(event.StateTo), StateFrom: string(event.StateFrom),
		Reason:   fixedCode(event.Payload, "reason"),
		Evidence: make([]FailureEvidenceV1, 0, len(event.EvidenceRefs)),
	}
	if event.StateTo == domain.StateFailed {
		failure.FailureClass, failure.Retryability = fixedCode(event.Payload, "failure_class"), fixedCode(event.Payload, "retryability")
	}
	if detail, ok := runfailure.FromPayload(event.Payload[runfailure.PayloadKey]); ok {
		failure.Recorded = true
		failure.Cause, failure.Phase = string(detail.Cause), string(detail.Phase)
		failure.Check, failure.CheckState, failure.CheckExitCode = detail.Check, string(detail.CheckState), detail.CheckExitCode
		failure.RepairTurns, failure.TaskRetries, failure.TaskRetryLimit, failure.Attempts = detail.RepairTurns, detail.TaskRetries, detail.TaskRetryLimit, detail.Attempts
		failure.Engine, failure.Provider = detail.Engine, detail.Provider
	} else {
		failure.Cause, failure.Phase = string(derivedCause(event, failure.Reason, failure.FailureClass)), string(runfailure.PhaseFrom(event.StateFrom))
	}
	for refIndex, ref := range event.EvidenceRefs {
		item := evidenceItem{Event: event, EventOrdinal: ordinal, Ref: ref, RefOrdinal: uint64(refIndex + 1)}
		failure.Evidence = append(failure.Evidence, FailureEvidenceV1{EvidenceID: evidenceIdentity(registration.RunID, item), Kind: safeExternal(ref.Kind, registration)})
		if ref.Kind != runfailure.EvidenceKind || failure.CheckOutput != "" || !failure.Recorded {
			continue
		}
		body, err := s.failureEvidence(ctx, registration, ref)
		if err != nil {
			return FailureV1{}, err
		}
		if body != nil && string(body.Cause) == failure.Cause && body.Check == failure.Check && failure.Check != "" {
			failure.CheckOutput, failure.CheckOutputTruncated = body.CheckOutput, body.CheckOutputTruncated
		}
	}
	return failure, nil
}

// failureEvidence reads and checks one failure-detail body. One that cannot be read (forgotten, removed) or is not
// exactly a RunFailureDetailV1 answers nil: the detail's codes stand without the check's output.
func (s *Service) failureEvidence(ctx context.Context, registration runtimecatalog.RunRegistrationV1, ref ledger.EvidenceRef) (*runfailure.Evidence, error) {
	if !downloadEligible(registration.CanonicalEvidenceRoot, ref) {
		return nil, nil
	}
	if err := s.acquireDownload(ctx); err != nil {
		return nil, err
	}
	data, err := s.readArtifact(registration.CanonicalEvidenceRoot, ref, runfailure.MaxEvidenceBytes)
	s.releaseDownload()
	if err != nil || len(data) > runfailure.MaxEvidenceBytes || digest(data) != ref.SHA256 {
		return nil, nil
	}
	var body runfailure.Evidence
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var extra any
	if decoder.Decode(&body) != nil || decoder.Decode(&extra) != io.EOF {
		return nil, nil
	}
	if body.SchemaVersion != runfailure.EvidenceSchemaVersion || !body.Cause.Known() || len(body.CheckOutput) > runfailure.MaxCheckOutputBytes ||
		!utf8.ValidString(body.CheckOutput) || (body.Check != "" && !runfailure.ValidCheck(body.Check)) {
		return nil, nil
	}
	return &body, nil
}

// derivedCause tells the cause of a stop recorded before R1 (or by a step that records no detail, such as a transition
// an API cancellation took over) from its fixed codes only.
func derivedCause(event ledger.Event, reason, failureClass string) runfailure.Cause {
	if event.StateTo == domain.StateCancelled {
		if _, api := event.Payload["operation_id"]; api {
			return runfailure.CauseCancelled
		}
		return runfailure.CauseInterrupted
	}
	switch reason {
	case "AUTHORITY_VALIDATOR_FAILED", "RESUME_CONTROLLER_FAILED":
		return runfailure.CauseAdmissionFailed
	case "BUILD_WALL_FAILED":
		return runfailure.CauseSandbox
	case "OPEN_MODEL_ENGINE_FAILED":
		return runfailure.CauseProviderRefused
	case "EXACT_EDIT_DOES_NOT_APPLY":
		return runfailure.CauseExactEditDoesNotApply
	case "ACCEPTANCE_FAILED":
		return runfailure.CauseAcceptanceFailed
	case "ACCEPTANCE_UNAVAILABLE":
		return runfailure.CauseAcceptanceUnavailable
	case "":
	default:
		if strings.HasSuffix(reason, "_FAILED") {
			return runfailure.CauseControllerError
		}
	}
	if outcome, _ := event.Payload["outcome"].(string); outcome == "timed_out" {
		return runfailure.CauseTimeout
	}
	if cause := runfailure.CauseOfClass(failureClass); cause == runfailure.CauseProviderLimit || cause == runfailure.CauseProviderRefused {
		return cause
	}
	return runfailure.CauseUnclassified
}

// fixedCode is a payload value that is a fixed upper-case identifier; anything else reads as absent.
func fixedCode(payload map[string]any, key string) string {
	value, ok := payload[key].(string)
	if !ok || len(value) == 0 || len(value) > 64 {
		return ""
	}
	for _, r := range value {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return ""
		}
	}
	return value
}

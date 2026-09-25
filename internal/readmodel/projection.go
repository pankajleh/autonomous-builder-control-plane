package readmodel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

func buildSnapshot(data []byte, physicalIdentity string, registration runtimecatalog.RunRegistrationV1) (Snapshot, error) {
	if physicalIdentity == "" || physicalIdentity == "ledger-physical-identity-unavailable" {
		return Snapshot{}, integrityError()
	}
	records, err := decodeRecords(data, registration.RunID)
	if err != nil {
		return Snapshot{}, err
	}
	physicalDigest := digest([]byte(physicalIdentity))
	snapshotDigest := digest(data)
	revision := projectionRevision(physicalDigest, snapshotDigest, uint64(len(data)), records)
	projection, err := projectRun(records, registration, revision)
	if err != nil {
		return Snapshot{}, err
	}
	events := make([]ledger.Event, len(records))
	clonedRecords := make([]record, len(records))
	for index := range records {
		events[index] = cloneEvent(records[index].event)
		clonedRecords[index] = record{event: cloneEvent(records[index].event), line: append([]byte(nil), records[index].line...), lineSHA256: records[index].lineSHA256}
	}
	return Snapshot{
		Projection: projection, Events: events, PhysicalIdentitySHA256: physicalDigest,
		SnapshotSHA256: snapshotDigest, SnapshotLength: uint64(len(data)),
		records: clonedRecords, raw: append([]byte(nil), data...), registration: registration,
	}, nil
}

func decodeRecords(data []byte, expectedRunID string) ([]record, error) {
	if len(data) == 0 || len(data) > maxLedgerBytes || data[len(data)-1] != '\n' {
		return nil, integrityError()
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) == 0 || len(lines) > maxLedgerRecords {
		return nil, integrityError()
	}
	result := make([]record, 0, len(lines))
	seenIDs := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		if len(line) == 0 || len(line)+1 > maxLedgerLineBytes || !utf8.Valid(line) {
			return nil, integrityError()
		}
		var event ledger.Event
		if err := decodeStrictJSON(line, &event); err != nil || event.SchemaVersion != ledger.CurrentSchemaVersion || event.Validate() != nil ||
			event.RunID != expectedRunID || (event.AttemptID != "" && runtimecatalog.ValidateIdentifier(event.AttemptID) != nil) {
			return nil, integrityError()
		}
		if _, exists := seenIDs[event.EventID]; exists {
			return nil, integrityError()
		}
		seenIDs[event.EventID] = struct{}{}
		result = append(result, record{event: event, line: append([]byte(nil), line...), lineSHA256: digest(line)})
	}
	return result, nil
}

func projectRun(records []record, registration runtimecatalog.RunRegistrationV1, revision string) (RunProjectionV1, error) {
	if len(records) == 0 {
		return RunProjectionV1{}, integrityError()
	}
	result := RunProjectionV1{
		SchemaVersion: ProjectionSchemaVersion, RunID: registration.RunID,
		RepositoryIdentityDigest:     registration.RepositoryIdentityDigest,
		InitialRegistrationTimestamp: registration.InitialRegistrationTimestamp,
		ProjectionRevision:           revision,
		ProjectIDs:                   []string{}, PlanIDs: []string{}, Attempts: []IdentitySummaryV1{}, Tasks: []IdentitySummaryV1{},
		AgentSessions: []IdentitySummaryV1{}, StateTransitions: []StateTransitionSummaryV1{},
		Blockers: []RecognizedSummaryV1{}, Decisions: []RecognizedSummaryV1{}, Lifecycle: []RecognizedSummaryV1{},
	}
	result.FirstEvent = identity(records[0].event, 1, registration)
	result.LastEvent = identity(records[len(records)-1].event, uint64(len(records)), registration)
	result.EventCount = uint64(len(records))
	attemptIndex, taskIndex, sessionIndex := map[string]int{}, map[string]int{}, map[string]int{}
	projectIDs, planIDs := map[string]struct{}{}, map[string]struct{}{}
	initialized := false
	current := domain.State("")
	observedProjectID, observedPlanID := "", ""
	for index := range records {
		event := records[index].event
		ordinal := uint64(index + 1)
		if event.ProjectID != "" {
			if observedProjectID != "" && observedProjectID != event.ProjectID {
				return RunProjectionV1{}, integrityError()
			}
			observedProjectID = event.ProjectID
			appendUniqueExternal(&result.ProjectIDs, projectIDs, event.ProjectID, registration)
		}
		if event.PlanID != "" {
			if observedPlanID != "" && observedPlanID != event.PlanID {
				return RunProjectionV1{}, integrityError()
			}
			observedPlanID = event.PlanID
			appendUniqueExternal(&result.PlanIDs, planIDs, event.PlanID, registration)
		}
		result.EvidenceCount += uint64(len(event.EvidenceRefs))
		updateIdentityHistory(&result.Attempts, attemptIndex, event.AttemptID, event, ordinal, registration)
		updateIdentityHistory(&result.Tasks, taskIndex, event.TaskID, event, ordinal, registration)
		updateIdentityHistory(&result.AgentSessions, sessionIndex, event.AgentSessionID, event, ordinal, registration)

		initial := event.EventType == string(domain.StateRunCreated) && event.StateFrom == "" && event.StateTo == "" && payloadInitialState(event.Payload)
		if !initialized {
			if !initial {
				return RunProjectionV1{}, integrityError()
			}
			initialized, current = true, domain.StateRunCreated
		} else if initial || event.EventType == string(domain.StateRunCreated) {
			return RunProjectionV1{}, integrityError()
		}
		if event.StateFrom != "" {
			if !initialized || event.StateFrom != current || domain.ValidateTransition(event.StateFrom, event.StateTo) != nil {
				return RunProjectionV1{}, integrityError()
			}
			current = event.StateTo
			result.StateTransitions = append(result.StateTransitions, StateTransitionSummaryV1{
				EventIdentityV1: identity(event, ordinal, registration),
				StateFrom:       string(event.StateFrom), StateTo: string(event.StateTo),
			})
		}
		appendRecognizedSummaries(&result, event, ordinal, registration)
	}
	if !initialized {
		return RunProjectionV1{}, integrityError()
	}
	result.CurrentState = string(current)
	result.TerminalStatus.Terminal = terminalState(current)
	if result.TerminalStatus.Terminal {
		result.TerminalStatus.State = string(current)
	}
	return result, nil
}

func projectionRevision(physicalDigest, snapshotDigest string, snapshotLength uint64, records []record) string {
	last := records[len(records)-1]
	context := struct {
		SchemaVersion          string `json:"schema_version"`
		PhysicalIdentitySHA256 string `json:"physical_identity_sha256"`
		SnapshotSHA256         string `json:"snapshot_sha256"`
		SnapshotLength         uint64 `json:"snapshot_length"`
		LastOrdinal            uint64 `json:"last_ordinal"`
		LastEventID            string `json:"last_event_id"`
	}{ProjectionSchemaVersion, physicalDigest, snapshotDigest, snapshotLength, uint64(len(records)), last.event.EventID}
	encoded, _ := json.Marshal(context)
	return digest(encoded)
}

func payloadInitialState(payload map[string]any) bool {
	value, ok := payload["state"]
	if !ok {
		return false
	}
	state, ok := value.(string)
	return ok && state == string(domain.StateRunCreated)
}

// terminalState reports whether a run has reached the end of the lifecycle this
// controller actually implements.
//
// The governed run lifecycle is "exactly one governed implementation and
// branch-acceptance lifecycle" (internal/run). A run that reached
// BRANCH_ACCEPTED has completed every automated step available to the
// controller and the controller will take no further action for it. Integration,
// merge and production acceptance are separate, human-authorized concerns
// outside the run lifecycle; they are not run states this controller produces,
// so their absence must not make a finished run report as non-terminal.
//
// Reporting a branch-accepted run as terminal does not claim integration or
// merge occurred: TerminalStatus.State carries the exact state reached
// (BRANCH_ACCEPTED), and COMPLETED remains reserved for a ledger that genuinely
// reaches it. If a future controller advances a run past BRANCH_ACCEPTED the
// projection is recomputed from the ledger, so the run correctly reports
// non-terminal again while it is in an integration state.
func terminalState(state domain.State) bool {
	switch state {
	case domain.StateCompleted, domain.StateBranchAccepted, domain.StateFailed, domain.StateCancelled:
		return true
	default:
		return false
	}
}

func updateIdentityHistory(values *[]IdentitySummaryV1, positions map[string]int, id string, event ledger.Event, ordinal uint64, registration runtimecatalog.RunRegistrationV1) {
	if id == "" {
		return
	}
	position, found := positions[id]
	if !found {
		positions[id] = len(*values)
		*values = append(*values, IdentitySummaryV1{
			ID: safeExternal(id, registration), FirstEvent: identity(event, ordinal, registration),
			LastEvent: identity(event, ordinal, registration), EventCount: 1, EvidenceCount: uint64(len(event.EvidenceRefs)),
		})
		return
	}
	value := &(*values)[position]
	value.LastEvent = identity(event, ordinal, registration)
	value.EventCount++
	value.EvidenceCount += uint64(len(event.EvidenceRefs))
}

func appendUniqueExternal(values *[]string, seen map[string]struct{}, value string, registration runtimecatalog.RunRegistrationV1) {
	if _, exists := seen[value]; exists {
		return
	}
	seen[value] = struct{}{}
	*values = append(*values, safeExternal(value, registration))
}

func appendRecognizedSummaries(result *RunProjectionV1, event ledger.Event, ordinal uint64, registration runtimecatalog.RunRegistrationV1) {
	summary := RecognizedSummaryV1{
		EventIdentityV1: identity(event, ordinal, registration), EventType: safeExternal(event.EventType, registration),
		State: string(event.StateTo), Source: safeExternal(event.Source, registration),
	}
	if event.EventType == "BLOCKER_DECISION_RECORDED" && recognizedBlockerPayload(event.Payload) {
		result.Blockers = append(result.Blockers, summary)
	}
	if event.EventType == "RESUME_AUTHORITY_RECORDED" || event.EventType == "API_HUMAN_DECISION_RECORDED" {
		result.Decisions = append(result.Decisions, summary)
	}
	if event.Source == "integration-gate" || event.Source == "merge-lifecycle" || event.Source == "prlifecycle" || event.EventType == "pr_lifecycle_terminal" {
		result.Lifecycle = append(result.Lifecycle, summary)
	}
}

func recognizedBlockerPayload(payload map[string]any) bool {
	version, versionOK := payload["record_schema_version"].(json.Number)
	_, classificationOK := payload["classification"].(map[string]any)
	_, requirementOK := payload["requirement"].(map[string]any)
	return versionOK && version.String() == "1" && classificationOK && requirementOK
}

func identity(event ledger.Event, ordinal uint64, registration runtimecatalog.RunRegistrationV1) EventIdentityV1 {
	return EventIdentityV1{Ordinal: ordinal, EventID: safeExternal(event.EventID, registration), Timestamp: event.Timestamp.UTC().Format(time.RFC3339Nano)}
}

func safeExternal(value string, registration runtimecatalog.RunRegistrationV1) string {
	for _, private := range []string{registration.CanonicalLedgerPath, registration.CanonicalEvidenceRoot} {
		if private != "" && strings.Contains(value, private) {
			return "[redacted]"
		}
	}
	return value
}

func cloneEvent(event ledger.Event) ledger.Event {
	data, _ := json.Marshal(event)
	var clone ledger.Event
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	_ = decoder.Decode(&clone)
	return clone
}

func decodeStrictJSON(data []byte, target any) error {
	if len(data) == 0 || hasDuplicateJSONFields(data) {
		return errors.New("invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func hasDuplicateJSONFields(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	_, err := consumeJSONValue(decoder, 0)
	if err != nil {
		return true
	}
	var extra any
	return decoder.Decode(&extra) != io.EOF
}

func consumeJSONValue(decoder *json.Decoder, depth int) (struct{}, error) {
	if depth > 64 {
		return struct{}{}, errors.New("JSON nesting exceeds projection bound")
	}
	token, err := decoder.Token()
	if err != nil {
		return struct{}{}, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return struct{}{}, nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return struct{}{}, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return struct{}{}, errors.New("invalid object key")
			}
			if _, exists := seen[key]; exists {
				return struct{}{}, errors.New("duplicate object key")
			}
			seen[key] = struct{}{}
			if _, err := consumeJSONValue(decoder, depth+1); err != nil {
				return struct{}{}, err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return struct{}{}, errors.New("unterminated object")
		}
	case '[':
		for decoder.More() {
			if _, err := consumeJSONValue(decoder, depth+1); err != nil {
				return struct{}{}, err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return struct{}{}, errors.New("unterminated array")
		}
	default:
		return struct{}{}, errors.New("unexpected delimiter")
	}
	return struct{}{}, nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func integrityError() error {
	return fmt.Errorf("%w", serviceapi.ErrProjectionIntegrity)
}

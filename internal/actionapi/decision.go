// Package actionapi implements the frozen EP-006 action payload decoders and
// the concrete serviceapi action controller.
package actionapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/blocker"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/recovery"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type CancelPayloadV1 struct{}

// ErrCancelRequestInvalid classifies malformed cancel-specific payloads.
var ErrCancelRequestInvalid = errors.New("cancel request invalid")

type DecisionPayloadV1 struct {
	DecisionRequestID string `json:"decision_request_id"`
	Answer            string `json:"answer"`
}

type HumanDecisionRequestV1 struct {
	DecisionRequestID string
	RunID             string
	AttemptID         string
	Question          string
	AcceptedAnswers   []string
	RequiredAuthority string
	PolicyVersion     string
	TransitionEventID string
	OriginatingEvent  ledger.Event
}

type blockerPayloadV1 struct {
	RecordSchemaVersion int                         `json:"record_schema_version"`
	Classification      blocker.Classification      `json:"classification"`
	Requirement         recovery.BlockerRequirement `json:"requirement"`
	PolicyVersion       string                      `json:"policy_version"`
}

func DecodeCancelPayload(data json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return nil, ErrCancelRequestInvalid
	}
	var payload CancelPayloadV1
	if err := decodeStrict(data, &payload); err != nil {
		return nil, ErrCancelRequestInvalid
	}
	return json.Marshal(payload)
}

func DecodeDecisionPayload(data json.RawMessage) (DecisionPayloadV1, json.RawMessage, error) {
	var payload DecisionPayloadV1
	if err := decodeStrict(data, &payload); err != nil || runtimecatalog.ValidateIdentifier(payload.DecisionRequestID) != nil || !validDecisionText(payload.Answer) {
		return DecisionPayloadV1{}, nil, serviceapi.ErrDecisionRequestInvalid
	}
	canonical, _ := json.Marshal(payload)
	return payload, canonical, nil
}

// DecodeHumanDecision accepts only the exact unresolved blocker event for the
// snapshot's current HUMAN_DECISION_REQUIRED state.
func DecodeHumanDecision(snapshot readmodel.Snapshot, runID, attemptID, decisionRequestID string) (HumanDecisionRequestV1, error) {
	if snapshot.Projection.RunID != runID || snapshot.Projection.CurrentState != string(domain.StateHumanDecisionRequired) {
		return HumanDecisionRequestV1{}, serviceapi.ErrDecisionAlreadyResolved
	}
	origin, err := decodeOrigin(snapshot.Events, runID, attemptID, decisionRequestID)
	if err != nil {
		return HumanDecisionRequestV1{}, err
	}
	latest := latestStateTransition(snapshot.Events)
	if latest == nil || latest.EventID != origin.TransitionEventID || latest.StateTo != domain.StateHumanDecisionRequired {
		return HumanDecisionRequestV1{}, serviceapi.ErrDecisionAlreadyResolved
	}
	for _, event := range snapshot.Events {
		if event.EventType == "API_HUMAN_DECISION_RECORDED" && payloadString(event.Payload, "decision_request_id") == decisionRequestID {
			return HumanDecisionRequestV1{}, serviceapi.ErrDecisionAlreadyRecorded
		}
	}
	return origin, nil
}

func decodeOrigin(events []ledger.Event, runID, attemptID, decisionRequestID string) (HumanDecisionRequestV1, error) {
	if runtimecatalog.ValidateIdentifier(runID) != nil || runtimecatalog.ValidateIdentifier(attemptID) != nil || runtimecatalog.ValidateIdentifier(decisionRequestID) != nil {
		return HumanDecisionRequestV1{}, serviceapi.ErrDecisionRequestInvalid
	}
	var found *ledger.Event
	for index := range events {
		if events[index].EventID == decisionRequestID {
			if found != nil {
				return HumanDecisionRequestV1{}, serviceapi.ErrProjectionIntegrity
			}
			clone := events[index]
			found = &clone
		}
	}
	if found == nil || found.EventType != recovery.EventBlockerDecision || found.RunID != runID || found.AttemptID != attemptID ||
		found.StateTo != domain.StateHumanDecisionRequired || found.StateFrom == "" || domain.ValidateTransition(found.StateFrom, found.StateTo) != nil {
		return HumanDecisionRequestV1{}, serviceapi.ErrDecisionRequestInvalid
	}
	data, err := json.Marshal(found.Payload)
	if err != nil {
		return HumanDecisionRequestV1{}, serviceapi.ErrDecisionRequestInvalid
	}
	var payload blockerPayloadV1
	if err := decodeStrict(data, &payload); err != nil || payload.RecordSchemaVersion != 1 || payload.Classification.RecommendedState != domain.StateHumanDecisionRequired || !validDecisionText(payload.PolicyVersion) {
		return HumanDecisionRequestV1{}, serviceapi.ErrDecisionRequestInvalid
	}
	if payload.Classification.Class != blocker.ClassHumanDecision && payload.Classification.Class != blocker.ClassHardQuotaExhausted && payload.Classification.Class != blocker.ClassBillingAccount {
		return HumanDecisionRequestV1{}, serviceapi.ErrDecisionRequestInvalid
	}
	requirement := payload.Requirement
	if requirement.HumanDecision == nil || requirement.Secret != nil || requirement.ExternalDependency != nil || requirement.Validation != nil || requirement.Policy != nil || requirement.Recovery != nil {
		return HumanDecisionRequestV1{}, serviceapi.ErrDecisionRequestInvalid
	}
	human := requirement.HumanDecision
	if !validDecisionText(human.Question) || !validDecisionText(human.RequiredAuthority) || len(human.Question) > 4096 || len(human.RequiredAuthority) > 256 ||
		strings.ContainsAny(human.RequiredAuthority, "*?[]\r\n") || len(human.AcceptedAnswers) == 0 || len(human.AcceptedAnswers) > 256 || !validHumanClassification(payload.Classification) {
		return HumanDecisionRequestV1{}, serviceapi.ErrDecisionRequestInvalid
	}
	seen := make(map[string]struct{}, len(human.AcceptedAnswers))
	for _, answer := range human.AcceptedAnswers {
		if !validDecisionText(answer) || len(answer) > 4096 {
			return HumanDecisionRequestV1{}, serviceapi.ErrDecisionRequestInvalid
		}
		if _, duplicate := seen[answer]; duplicate {
			return HumanDecisionRequestV1{}, serviceapi.ErrDecisionRequestInvalid
		}
		seen[answer] = struct{}{}
	}
	return HumanDecisionRequestV1{
		DecisionRequestID: decisionRequestID, RunID: runID, AttemptID: attemptID,
		Question: human.Question, AcceptedAnswers: append([]string(nil), human.AcceptedAnswers...), RequiredAuthority: human.RequiredAuthority,
		PolicyVersion: payload.PolicyVersion, TransitionEventID: found.EventID, OriginatingEvent: *found,
	}, nil
}

func latestStateTransition(events []ledger.Event) *ledger.Event {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].StateFrom != "" && events[index].StateTo != "" {
			value := events[index]
			return &value
		}
	}
	if len(events) > 0 && events[0].EventType == string(domain.StateRunCreated) {
		value := events[0]
		return &value
	}
	return nil
}

func acceptedAnswer(request HumanDecisionRequestV1, answer string) bool {
	for _, accepted := range request.AcceptedAnswers {
		if accepted == answer {
			return true
		}
	}
	return false
}

func validDecisionText(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validHumanClassification(value blocker.Classification) bool {
	if value.RecommendedState != domain.StateHumanDecisionRequired || value.AutomaticRetryAllowed || value.DestructiveActionAllowed {
		return false
	}
	switch value.Class {
	case blocker.ClassHumanDecision:
		return value.Retryability == blocker.RetryRequiresAuthority && value.RecommendedAction == blocker.ActionRequestHumanDecision
	case blocker.ClassHardQuotaExhausted:
		return value.Retryability == blocker.RetryRequiresAuthority && value.RecommendedAction == blocker.ActionRequestUsageDecision
	case blocker.ClassBillingAccount:
		return value.Retryability == blocker.RetryableAfterChange && value.RecommendedAction == blocker.ActionResolveAccount
	default:
		return false
	}
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func decodeStrict(data []byte, target any) error {
	if len(data) == 0 || len(data) > 16<<10 || duplicateFields(data) {
		return errors.New("invalid action JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing action JSON")
	}
	return nil
}

func duplicateFields(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var consume func(int) error
	consume = func(depth int) error {
		if depth > 8 {
			return errors.New("too deep")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("invalid key")
				}
				if _, exists := seen[key]; exists {
					return errors.New("duplicate key")
				}
				seen[key] = struct{}{}
				if err := consume(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := consume(depth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("invalid delimiter")
		}
	}
	if err := consume(0); err != nil {
		return true
	}
	var extra any
	return decoder.Decode(&extra) != io.EOF
}

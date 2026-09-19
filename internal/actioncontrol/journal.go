// Package actioncontrol owns the durable EP-006 action journal and the
// generation-bound cancel watcher.  The journal is deliberately separate
// from the lifecycle ledger: it is an operational idempotency and delivery
// record, never lifecycle authority.
package actioncontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

const (
	JournalSchemaVersion = 1
	MaxSegmentBytes      = 8 << 20
	MaxSegmentRecords    = 32_768
	MaxJournalEpochs     = 8
	MaxJournalRecords    = 262_144
	MaxJournalRuns       = 10_000
	RequestIndexShards   = 256
	MaxRequestsPerShard  = 4_096
	MaxRequestIndexBytes = 32 << 10
	JournalLockTimeout   = 2 * time.Second
	MaxWatcherPoll       = 250 * time.Millisecond
	maxReceiptRequestID  = 128
	maxReceiptReason     = 1 << 10

	CancelEventDomain   = "ep006-api-cancel-request-v1"
	DecisionEventDomain = "ep006-human-decision-record-v1"
)

var receiptStateNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

var (
	ErrUnsupported    = errors.New("governed actions are unsupported on this platform")
	ErrIntegrity      = errors.New("action journal integrity failure")
	ErrBusy           = errors.New("action journal lock acquisition timed out")
	ErrExhausted      = errors.New("action journal exhausted")
	ErrConflict       = errors.New("action request identity conflict")
	ErrDecisionExists = errors.New("decision request already has an action")
	ErrNotFound       = errors.New("action operation not found")
	ErrReceiptPending = errors.New("action receipt identity is durable but receipt is not materialized")
)

type DelegatedActorV1 struct {
	SubjectID   string `json:"subject_id"`
	SubjectType string `json:"subject_type"`
}

// ActionReceiptV1 is the create-once durable admission record. Payload is the
// already strictly decoded and canonical action-specific payload.
type ActionReceiptV1 struct {
	Kind                          string            `json:"kind"`
	SchemaVersion                 int               `json:"schema_version"`
	Epoch                         uint64            `json:"epoch"`
	Sequence                      uint64            `json:"sequence"`
	PriorSegmentFinalRecordSHA256 string            `json:"prior_segment_final_record_sha256,omitempty"`
	LookupKeySHA256               string            `json:"lookup_key_sha256"`
	OperationID                   string            `json:"operation_id"`
	PrincipalID                   string            `json:"principal_id"`
	PrincipalType                 string            `json:"principal_type"`
	RequestID                     string            `json:"request_id"`
	RequestSHA256                 string            `json:"request_sha256"`
	Action                        string            `json:"action"`
	RunID                         string            `json:"run_id"`
	AttemptID                     string            `json:"attempt_id"`
	ExpectedState                 string            `json:"expected_state"`
	ExpectedRevision              string            `json:"expected_revision"`
	Reason                        string            `json:"reason"`
	DelegatedActor                *DelegatedActorV1 `json:"delegated_actor,omitempty"`
	Payload                       json.RawMessage   `json:"payload"`
	PolicyVersion                 string            `json:"policy_version,omitempty"`
	AuthorityGrantSHA256          string            `json:"authority_grant_file_sha256,omitempty"`
	AdmittedStateTransitionID     string            `json:"admitted_state_transition_event_id"`
	OwnerLeaseID                  string            `json:"owner_lease_id,omitempty"`
	ReceivedAt                    string            `json:"received_at"`
}

type ActionClaimV1 struct {
	Kind                          string `json:"kind"`
	SchemaVersion                 int    `json:"schema_version"`
	Epoch                         uint64 `json:"epoch"`
	Sequence                      uint64 `json:"sequence"`
	PriorSegmentFinalRecordSHA256 string `json:"prior_segment_final_record_sha256,omitempty"`
	OperationID                   string `json:"operation_id"`
	AdmittedStateTransitionID     string `json:"admitted_state_transition_event_id"`
	OwnerLeaseID                  string `json:"owner_lease_id,omitempty"`
	ClaimedAt                     string `json:"claimed_at"`
	ControllerEventID             string `json:"controller_event_id"`
	ControllerEventTimestamp      string `json:"controller_event_timestamp"`
}

type OutcomeStatus string

const (
	StatusReceived               OutcomeStatus = "RECEIVED"
	StatusRejected               OutcomeStatus = "REJECTED"
	StatusClaimed                OutcomeStatus = "CLAIMED"
	StatusApplied                OutcomeStatus = "APPLIED"
	StatusReconciliationRequired OutcomeStatus = "RECONCILIATION_REQUIRED"
	StatusReconciledApplied      OutcomeStatus = "RECONCILED_APPLIED"
	StatusReconciledNotApplied   OutcomeStatus = "RECONCILED_NOT_APPLIED"
)

type ActionOutcomeV1 struct {
	Kind                          string        `json:"kind"`
	SchemaVersion                 int           `json:"schema_version"`
	Epoch                         uint64        `json:"epoch"`
	Sequence                      uint64        `json:"sequence"`
	PriorSegmentFinalRecordSHA256 string        `json:"prior_segment_final_record_sha256,omitempty"`
	OperationID                   string        `json:"operation_id"`
	Status                        OutcomeStatus `json:"status"`
	RecordedAt                    string        `json:"recorded_at"`
	AuthoritativeEventIDs         []string      `json:"authoritative_event_ids,omitempty"`
	ReasonCode                    string        `json:"reason_code,omitempty"`
}

type ReceiptInput struct {
	PrincipalID          string
	PrincipalType        string
	RequestID            string
	RequestSHA256        string
	Action               string
	RunID                string
	AttemptID            string
	ExpectedState        string
	ExpectedRevision     string
	Reason               string
	DelegatedActor       *DelegatedActorV1
	Payload              json.RawMessage
	PolicyVersion        string
	AuthorityGrantSHA256 string
	StateTransitionID    string
}

// ReceiptReplayInput is the complete immutable request identity needed to
// distinguish an exact replay from conflicting intent before consulting any
// run journal. RequestSHA256 binds the remaining canonical command fields.
type ReceiptReplayInput struct {
	PrincipalID   string
	PrincipalType string
	RequestID     string
	RequestSHA256 string
	Action        string
	RunID         string
	AttemptID     string
}

type AdmissionBinding struct {
	OwnerLeaseID string
	// Release is invoked by CreateReceipt only after the receipt append has
	// been fsynced (or the append has failed). Cancel admission uses it to keep
	// the nested catalog guard held for the complete journal transaction.
	Release func() error
}

// Operation is a bounded reconstruction of one journal operation.
type Operation struct {
	Receipt  ActionReceiptV1
	Claim    *ActionClaimV1
	Outcomes []ActionOutcomeV1
	Status   OutcomeStatus
}

func (o Operation) AuthoritativeEventIDs() []string {
	if len(o.Outcomes) == 0 {
		return nil
	}
	return append([]string(nil), o.Outcomes[len(o.Outcomes)-1].AuthoritativeEventIDs...)
}

func LookupKey(principalID, requestID string) string {
	data, _ := json.Marshal(struct {
		PrincipalID string `json:"principal_id"`
		RequestID   string `json:"request_id"`
	}{principalID, requestID})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func DeterministicEventID(domain, operationID string) string {
	sum := sha256.Sum256([]byte(domain + "\x00" + operationID))
	return hex.EncodeToString(sum[:])
}

func operationIDForLookup(lookup string) string {
	sum := sha256.Sum256([]byte("ep006-action-operation-v1\x00" + lookup))
	return hex.EncodeToString(sum[:])
}

func canonicalTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func validCanonicalTime(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && value == canonicalTime(parsed)
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateReceipt(value ActionReceiptV1) error {
	if value.Kind != "ActionReceiptV1" || value.SchemaVersion != JournalSchemaVersion || value.Epoch == 0 || value.Sequence == 0 ||
		!validDigest(value.LookupKeySHA256) || runtimecatalog.ValidateIdentifier(value.OperationID) != nil ||
		runtimecatalog.ValidateIdentifier(value.RunID) != nil || runtimecatalog.ValidateIdentifier(value.AttemptID) != nil ||
		runtimecatalog.ValidateIdentifier(value.PrincipalID) != nil || len(value.PrincipalID) > 128 || runtimecatalog.ValidateIdentifier(value.RequestID) != nil || len(value.RequestID) > maxReceiptRequestID ||
		!validDigest(value.RequestSHA256) || !validDigest(value.ExpectedRevision) || !receiptStateNamePattern.MatchString(value.ExpectedState) ||
		runtimecatalog.ValidateIdentifier(value.AdmittedStateTransitionID) != nil || !validCanonicalTime(value.ReceivedAt) ||
		len(value.Payload) == 0 || !json.Valid(value.Payload) || len(value.Payload) > 16<<10 || len(value.Reason) > maxReceiptReason ||
		!utf8.ValidString(value.Reason) || strings.ContainsRune(value.Reason, 0) {
		return ErrIntegrity
	}
	if value.Action != "cancel" && value.Action != "decision" {
		return ErrIntegrity
	}
	if value.PrincipalType != "service" && value.PrincipalType != "user" && value.PrincipalType != "operator" && value.PrincipalType != "test" {
		return ErrIntegrity
	}
	if err := validateJournalPayload(value.Action, value.Payload); err != nil {
		return err
	}
	if value.Action == "cancel" {
		if !validDigest(value.OwnerLeaseID) || value.PolicyVersion != "" || value.AuthorityGrantSHA256 != "" {
			return ErrIntegrity
		}
	} else {
		if value.OwnerLeaseID != "" || value.DelegatedActor == nil || value.PolicyVersion == "" || len(value.PolicyVersion) > 128 || !validDigest(value.AuthorityGrantSHA256) {
			return ErrIntegrity
		}
	}
	if value.DelegatedActor != nil {
		if runtimecatalog.ValidateIdentifier(value.DelegatedActor.SubjectID) != nil || len(value.DelegatedActor.SubjectID) > 128 ||
			(value.DelegatedActor.SubjectType != "user" && value.DelegatedActor.SubjectType != "operator") {
			return ErrIntegrity
		}
	}
	if value.PriorSegmentFinalRecordSHA256 != "" && !validDigest(value.PriorSegmentFinalRecordSHA256) {
		return ErrIntegrity
	}
	return nil
}

func validateJournalPayload(action string, data json.RawMessage) error {
	if action == "cancel" {
		if !bytes.Equal(data, []byte("{}")) {
			return ErrIntegrity
		}
		return nil
	}
	var payload struct {
		DecisionRequestID string `json:"decision_request_id"`
		Answer            string `json:"answer"`
	}
	if json.Unmarshal(data, &payload) != nil || runtimecatalog.ValidateIdentifier(payload.DecisionRequestID) != nil ||
		payload.Answer == "" || len(payload.Answer) > 16<<10 || strings.TrimSpace(payload.Answer) != payload.Answer || strings.ContainsRune(payload.Answer, 0) {
		return ErrIntegrity
	}
	canonical, err := json.Marshal(payload)
	if err != nil || !bytes.Equal(canonical, data) {
		return ErrIntegrity
	}
	return nil
}

func journalDecisionRequestID(data json.RawMessage) (string, error) {
	var payload struct {
		DecisionRequestID string `json:"decision_request_id"`
		Answer            string `json:"answer"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.DecisionRequestID == "" {
		return "", ErrIntegrity
	}
	return payload.DecisionRequestID, nil
}

func validateClaim(value ActionClaimV1) error {
	if value.Kind != "ActionClaimV1" || value.SchemaVersion != JournalSchemaVersion || value.Epoch == 0 || value.Sequence == 0 ||
		runtimecatalog.ValidateIdentifier(value.OperationID) != nil || runtimecatalog.ValidateIdentifier(value.AdmittedStateTransitionID) != nil ||
		!validCanonicalTime(value.ClaimedAt) || !validCanonicalTime(value.ControllerEventTimestamp) || !validDigest(value.ControllerEventID) ||
		(value.PriorSegmentFinalRecordSHA256 != "" && !validDigest(value.PriorSegmentFinalRecordSHA256)) {
		return ErrIntegrity
	}
	if value.OwnerLeaseID != "" && !validDigest(value.OwnerLeaseID) {
		return ErrIntegrity
	}
	return nil
}

func validateOutcome(value ActionOutcomeV1) error {
	if value.Kind != "ActionOutcomeV1" || value.SchemaVersion != JournalSchemaVersion || value.Epoch == 0 || value.Sequence == 0 ||
		runtimecatalog.ValidateIdentifier(value.OperationID) != nil || !validCanonicalTime(value.RecordedAt) ||
		len(value.AuthoritativeEventIDs) > 2 || len(value.ReasonCode) > 128 ||
		(value.PriorSegmentFinalRecordSHA256 != "" && !validDigest(value.PriorSegmentFinalRecordSHA256)) {
		return ErrIntegrity
	}
	for _, id := range value.AuthoritativeEventIDs {
		if runtimecatalog.ValidateIdentifier(id) != nil {
			return ErrIntegrity
		}
	}
	switch value.Status {
	case StatusRejected, StatusApplied, StatusReconciliationRequired, StatusReconciledApplied, StatusReconciledNotApplied:
		return nil
	default:
		return ErrIntegrity
	}
}

func validateProgress(operation Operation) error {
	status := StatusReceived
	if operation.Claim != nil {
		status = StatusClaimed
	}
	for _, outcome := range operation.Outcomes {
		switch {
		case status == StatusReceived && outcome.Status == StatusRejected:
			status = StatusRejected
		case status == StatusClaimed && outcome.Status == StatusApplied:
			status = StatusApplied
		case status == StatusClaimed && outcome.Status == StatusReconciliationRequired:
			status = StatusReconciliationRequired
		case status == StatusReconciliationRequired && (outcome.Status == StatusReconciledApplied || outcome.Status == StatusReconciledNotApplied):
			status = outcome.Status
		default:
			return fmt.Errorf("%w: invalid action outcome progression", ErrIntegrity)
		}
	}
	operation.Status = status
	return nil
}

func isTerminal(status OutcomeStatus) bool {
	return status == StatusRejected || status == StatusApplied || status == StatusReconciledApplied || status == StatusReconciledNotApplied
}

// BindSequence runs with the action-journal lock held. It exists only for the
// journal -> catalog owner-closing transaction.
func (j *Journal) BindSequence(ctx context.Context, runID string, operation func(uint64) error) error {
	if operation == nil {
		return errors.New("journal sequence binder is required")
	}
	guard, err := j.acquire(ctx, runID)
	if err != nil {
		return err
	}
	defer guard.close()
	state, err := guard.scan()
	if err != nil {
		return err
	}
	return operation(state.sequence)
}

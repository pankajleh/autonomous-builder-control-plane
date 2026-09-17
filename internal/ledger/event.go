package ledger

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
)

const CurrentSchemaVersion = 1

type EvidenceRef struct {
	URI    string `json:"uri"`
	SHA256 string `json:"sha256,omitempty"`
	Kind   string `json:"kind,omitempty"`
}

type Event struct {
	SchemaVersion  int            `json:"schema_version"`
	EventID        string         `json:"event_id"`
	Timestamp      time.Time      `json:"timestamp"`
	ProjectID      string         `json:"project_id,omitempty"`
	PlanID         string         `json:"plan_id,omitempty"`
	RunID          string         `json:"run_id"`
	AttemptID      string         `json:"attempt_id,omitempty"`
	TaskID         string         `json:"task_id,omitempty"`
	AgentSessionID string         `json:"agent_session_id,omitempty"`
	CorrelationID  string         `json:"correlation_id,omitempty"`
	EventType      string         `json:"event_type"`
	StateFrom      domain.State   `json:"state_from,omitempty"`
	StateTo        domain.State   `json:"state_to,omitempty"`
	Actor          string         `json:"actor"`
	Source         string         `json:"source"`
	Payload        map[string]any `json:"payload,omitempty"`
	EvidenceRefs   []EvidenceRef  `json:"evidence_refs,omitempty"`
}

func NewEvent(runID, eventType, actor, source string) (Event, error) {
	id, err := randomID()
	if err != nil {
		return Event{}, err
	}
	e := Event{
		SchemaVersion: CurrentSchemaVersion,
		EventID:       id,
		Timestamp:     time.Now().UTC(),
		RunID:         runID,
		EventType:     eventType,
		Actor:         actor,
		Source:        source,
	}
	return e, e.Validate()
}

func (e Event) Validate() error {
	if e.SchemaVersion <= 0 {
		return errors.New("schema_version must be positive")
	}
	if e.EventID == "" || e.RunID == "" || e.EventType == "" || e.Actor == "" || e.Source == "" {
		return errors.New("event_id, run_id, event_type, actor and source are required")
	}
	if e.Timestamp.IsZero() {
		return errors.New("timestamp is required")
	}
	if (e.StateFrom == "") != (e.StateTo == "") {
		return errors.New("state_from and state_to must be supplied together")
	}
	if e.StateFrom != "" {
		if err := domain.ValidateTransition(e.StateFrom, e.StateTo); err != nil {
			return err
		}
	}
	return nil
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ValidateRetainedEventID accepts exactly the two historical producer widths:
// NewEvent's 16 random bytes and the deterministic PR/merge/CI constructors'
// full 32 SHA-256 bytes. It validates without rewriting either representation.
func ValidateRetainedEventID(value string) error {
	if value != strings.ToLower(value) || len(value) != 32 && len(value) != 64 {
		return errors.New("retained ledger event ID must be 32 or 64 lowercase hexadecimal characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 && len(decoded) != 32 {
		return errors.New("retained ledger event ID must encode exactly 16 or 32 bytes")
	}
	if hex.EncodeToString(decoded) != value {
		return errors.New("retained ledger event ID is not canonical lowercase hexadecimal")
	}
	return nil
}

// ValidateV4EventID accepts only the V4 effect-event subtype. A retained
// deterministic 64-character ID is intentionally not a V4 ID.
func ValidateV4EventID(value string) error {
	if len(value) != 32 || value != strings.ToLower(value) {
		return errors.New("V4 ledger event ID must be 32 lowercase hexadecimal characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != value {
		return errors.New("V4 ledger event ID must encode exactly 16 bytes")
	}
	return nil
}

// DeriveV4EffectEventID implements the frozen ABCP-V4-LEDGER-EVENT-ID-V1
// preimage. Digest arguments remain their 64-character UTF-8 spellings; they
// are not decoded before hashing.
func DeriveV4EffectEventID(effectKind, intentDigest, winningOutcomeDigest, stateTo string) (string, error) {
	if effectKind != "PR" && effectKind != "MERGE" {
		return "", errors.New("V4 effect kind must be PR or MERGE")
	}
	if err := validateSHA256Text(intentDigest); err != nil {
		return "", fmt.Errorf("intent digest: %w", err)
	}
	if err := validateSHA256Text(winningOutcomeDigest); err != nil {
		return "", fmt.Errorf("winning outcome digest: %w", err)
	}
	if stateTo == "" || strings.ContainsRune(stateTo, 0) {
		return "", errors.New("V4 destination state is required and cannot contain NUL")
	}
	preimage := strings.Join([]string{
		"ABCP-V4-LEDGER-EVENT-ID-V1",
		effectKind,
		intentDigest,
		winningOutcomeDigest,
		stateTo,
	}, "\x00")
	digest := sha256.Sum256([]byte(preimage))
	identifier := hex.EncodeToString(digest[:16])
	if err := ValidateV4EventID(identifier); err != nil {
		return "", err
	}
	return identifier, nil
}

func validateSHA256Text(value string) error {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return errors.New("must be 64 lowercase hexadecimal characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != value {
		return errors.New("must encode exactly 32 bytes")
	}
	return nil
}

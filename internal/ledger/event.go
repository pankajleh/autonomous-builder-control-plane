package ledger

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
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

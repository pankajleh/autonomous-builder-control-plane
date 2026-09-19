// Package readmodel builds bounded, deterministic projections from one
// authoritative ledger snapshot per request.
package readmodel

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

const (
	ProjectionSchemaVersion = "RunProjectionV1"
	EventPageSchemaVersion  = "EventProjectionPageV1"
	LedgerCursorSchemaV1    = "LedgerCursorPayloadV1"
	MaxOpenLedgerObjects    = 8
	MaxConcurrentSnapshots  = 4

	maxLedgerLineBytes = 256 << 10
	maxLedgerBytes     = 64 << 20
	maxLedgerRecords   = 262144
)

// CatalogReader is the immutable registration dependency used by projections.
type CatalogReader interface {
	ReadRun(string) (runtimecatalog.RunRegistrationV1, error)
}

// ExistingReadOnlyLedger is intentionally the same narrow surface as the
// restricted ledger reader. Tests may supply an instrumented implementation.
type ExistingReadOnlyLedger interface {
	Snapshot() ([]byte, string, error)
	Close() error
}

type ReadOnlyLedgerOpener func(string, ledger.PhysicalGeneration) (ExistingReadOnlyLedger, error)

// Config constructs one coordinator. Its semaphores are shared by every read
// and governed writer performed through the resulting Service.
type Config struct {
	Catalog            CatalogReader
	CursorSigner       *serviceapi.CursorSigner
	Clock              func() time.Time
	OpenReadOnlyLedger ReadOnlyLedgerOpener
}

type Service struct {
	catalog       CatalogReader
	cursors       *serviceapi.CursorSigner
	now           func() time.Time
	openReadOnly  ReadOnlyLedgerOpener
	ledgerSlots   chan struct{}
	snapshotSlots chan struct{}
}

type EventIdentityV1 struct {
	Ordinal   uint64 `json:"ordinal"`
	EventID   string `json:"event_id"`
	Timestamp string `json:"timestamp"`
}

type IdentitySummaryV1 struct {
	ID            string          `json:"id"`
	FirstEvent    EventIdentityV1 `json:"first_event"`
	LastEvent     EventIdentityV1 `json:"last_event"`
	EventCount    uint64          `json:"event_count"`
	EvidenceCount uint64          `json:"evidence_count"`
}

type StateTransitionSummaryV1 struct {
	EventIdentityV1
	StateFrom string `json:"state_from"`
	StateTo   string `json:"state_to"`
}

type RecognizedSummaryV1 struct {
	EventIdentityV1
	EventType string `json:"event_type"`
	State     string `json:"state,omitempty"`
	Source    string `json:"source"`
}

type TerminalStatusV1 struct {
	Terminal bool   `json:"terminal"`
	State    string `json:"state,omitempty"`
}

type RunProjectionV1 struct {
	SchemaVersion                string                     `json:"schema_version"`
	RunID                        string                     `json:"run_id"`
	RepositoryIdentityDigest     string                     `json:"repository_identity_digest"`
	InitialRegistrationTimestamp string                     `json:"initial_registration_timestamp"`
	ProjectIDs                   []string                   `json:"project_ids"`
	PlanIDs                      []string                   `json:"plan_ids"`
	FirstEvent                   EventIdentityV1            `json:"first_event"`
	LastEvent                    EventIdentityV1            `json:"last_event"`
	CurrentState                 string                     `json:"current_state"`
	ProjectionRevision           string                     `json:"projection_revision"`
	EventCount                   uint64                     `json:"event_count"`
	EvidenceCount                uint64                     `json:"evidence_count"`
	TerminalStatus               TerminalStatusV1           `json:"terminal_status"`
	Attempts                     []IdentitySummaryV1        `json:"attempts"`
	Tasks                        []IdentitySummaryV1        `json:"tasks"`
	AgentSessions                []IdentitySummaryV1        `json:"agent_sessions"`
	StateTransitions             []StateTransitionSummaryV1 `json:"state_transitions"`
	Blockers                     []RecognizedSummaryV1      `json:"blockers"`
	Decisions                    []RecognizedSummaryV1      `json:"decisions"`
	Lifecycle                    []RecognizedSummaryV1      `json:"lifecycle"`
}

// ProjectedEventV1 preserves authoritative envelope data while deliberately
// omitting payloads and evidence URIs, which can contain controller paths.
type ProjectedEventV1 struct {
	Ordinal             uint64                   `json:"ordinal"`
	LineSHA256          string                   `json:"line_sha256"`
	LedgerSchemaVersion int                      `json:"ledger_schema_version"`
	EventID             string                   `json:"event_id"`
	Timestamp           string                   `json:"timestamp"`
	ProjectID           string                   `json:"project_id,omitempty"`
	PlanID              string                   `json:"plan_id,omitempty"`
	RunID               string                   `json:"run_id"`
	AttemptID           string                   `json:"attempt_id,omitempty"`
	TaskID              string                   `json:"task_id,omitempty"`
	AgentSessionID      string                   `json:"agent_session_id,omitempty"`
	CorrelationID       string                   `json:"correlation_id,omitempty"`
	EventType           string                   `json:"event_type"`
	StateFrom           string                   `json:"state_from,omitempty"`
	StateTo             string                   `json:"state_to,omitempty"`
	Actor               string                   `json:"actor"`
	Source              string                   `json:"source"`
	EvidenceCount       uint64                   `json:"evidence_count"`
	EvidenceRefs        []ProjectedEvidenceRefV1 `json:"evidence_refs"`
}

type ProjectedEvidenceRefV1 struct {
	Ordinal uint64 `json:"ordinal"`
	SHA256  string `json:"sha256,omitempty"`
	Kind    string `json:"kind,omitempty"`
}

type EventProjectionPageV1 struct {
	SchemaVersion      string             `json:"schema_version"`
	RunID              string             `json:"run_id"`
	ProjectionRevision string             `json:"projection_revision"`
	Events             []ProjectedEventV1 `json:"events"`
	NextCursor         string             `json:"next_cursor,omitempty"`
}

// LedgerCursorPayloadV1 is protected by serviceapi.CursorEnvelopeV1. The
// prior snapshot digest covers every byte through PriorSnapshotLength, while
// the last-record fields make the terminal bound explicit and auditable. Only
// a digest of the terminal EventID is included because EventIDs are legacy
// ledger input and may contain private registered paths.
type LedgerCursorPayloadV1 struct {
	SchemaVersion          string `json:"schema_version"`
	RunID                  string `json:"run_id"`
	PhysicalIdentitySHA256 string `json:"physical_identity_sha256"`
	PriorSnapshotSHA256    string `json:"prior_snapshot_sha256"`
	PriorSnapshotLength    uint64 `json:"prior_snapshot_length"`
	PriorLastOrdinal       uint64 `json:"prior_last_ordinal"`
	PriorLastEventIDSHA256 string `json:"prior_last_event_id_sha256"`
	PriorLastLineSHA256    string `json:"prior_last_line_sha256"`
	PagePosition           uint64 `json:"page_position"`
}

type record struct {
	event      ledger.Event
	line       []byte
	lineSHA256 string
}

// Snapshot is an in-process, immutable projection view. Events returns
// defensive copies so later action tracks can inspect exact authoritative
// semantics without performing a second read.
type Snapshot struct {
	Projection             RunProjectionV1
	Events                 []ledger.Event
	PhysicalIdentitySHA256 string
	SnapshotSHA256         string
	SnapshotLength         uint64
	records                []record
	raw                    []byte
	registration           runtimecatalog.RunRegistrationV1
}

var (
	_ serviceapi.RunProjectionReader   = (*Service)(nil)
	_ serviceapi.EventProjectionReader = (*Service)(nil)
	_ interface {
		Snapshot(context.Context, string) (Snapshot, error)
	} = (*Service)(nil)
	_ json.Marshaler = json.RawMessage(nil)
)

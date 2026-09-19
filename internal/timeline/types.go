// Package timeline exposes normalized timeline and evidence views built from
// one authoritative read-model snapshot per request.
package timeline

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

const (
	TimelinePageSchemaVersion = "TimelinePageV1"
	EvidencePageSchemaVersion = "EvidencePageV1"
	MaxConcurrentDownloads    = 4
)

// SnapshotReader is the frozen Track-B snapshot seam consumed by Track C.
type SnapshotReader interface {
	Snapshot(context.Context, string) (readmodel.Snapshot, error)
}

// CatalogReader resolves only immutable registered run authority. Caller
// paths and evidence URIs never cross this seam.
type CatalogReader interface {
	ReadRun(string) (runtimecatalog.RunRegistrationV1, error)
}

type ArtifactReader func(string, ledger.EvidenceRef, int64) ([]byte, error)

type Config struct {
	Snapshots    SnapshotReader
	Catalog      CatalogReader
	CursorSigner *serviceapi.CursorSigner
	Clock        func() time.Time
	ReadArtifact ArtifactReader
}

type Service struct {
	snapshots     SnapshotReader
	catalog       CatalogReader
	cursors       *serviceapi.CursorSigner
	now           func() time.Time
	readArtifact  ArtifactReader
	downloadSlots chan struct{}
}

type EventIdentityV1 struct {
	Ordinal   uint64 `json:"ordinal"`
	EventID   string `json:"event_id"`
	Timestamp string `json:"timestamp"`
}

// TimelineEntryV1 contains only authoritative event-envelope fields. Payload
// data is intentionally absent because it is untyped and may contain private
// controller paths or misleading lifecycle narration.
type TimelineEntryV1 struct {
	Ordinal        uint64   `json:"ordinal"`
	EventID        string   `json:"event_id"`
	Timestamp      string   `json:"timestamp"`
	ProjectID      string   `json:"project_id,omitempty"`
	PlanID         string   `json:"plan_id,omitempty"`
	RunID          string   `json:"run_id"`
	AttemptID      string   `json:"attempt_id,omitempty"`
	TaskID         string   `json:"task_id,omitempty"`
	AgentSessionID string   `json:"agent_session_id,omitempty"`
	CorrelationID  string   `json:"correlation_id,omitempty"`
	EventType      string   `json:"event_type"`
	StateFrom      string   `json:"state_from,omitempty"`
	StateTo        string   `json:"state_to,omitempty"`
	Actor          string   `json:"actor"`
	Source         string   `json:"source"`
	EvidenceCount  uint64   `json:"evidence_count"`
	EvidenceIDs    []string `json:"evidence_ids"`
}

type TimelinePageV1 struct {
	SchemaVersion      string            `json:"schema_version"`
	RunID              string            `json:"run_id"`
	ProjectionRevision string            `json:"projection_revision"`
	Entries            []TimelineEntryV1 `json:"entries"`
	NextCursor         string            `json:"next_cursor,omitempty"`
}

type EvidenceDescriptorV1 struct {
	EvidenceID   string          `json:"evidence_id"`
	Kind         string          `json:"kind,omitempty"`
	SHA256       string          `json:"sha256,omitempty"`
	SourceEvent  EventIdentityV1 `json:"source_event"`
	ByteSize     *int64          `json:"byte_size,omitempty"`
	Downloadable bool            `json:"downloadable"`
}

type EvidencePageV1 struct {
	SchemaVersion      string                 `json:"schema_version"`
	RunID              string                 `json:"run_id"`
	ProjectionRevision string                 `json:"projection_revision"`
	Evidence           []EvidenceDescriptorV1 `json:"evidence"`
	NextCursor         string                 `json:"next_cursor,omitempty"`
}

var (
	_ serviceapi.TimelineReader = (*Service)(nil)
	_ serviceapi.EvidenceReader = (*Service)(nil)
	_ json.Marshaler            = json.RawMessage(nil)
)

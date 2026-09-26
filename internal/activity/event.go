// Package activity exposes read-only implementation telemetry. Nothing in this
// package can append to the authoritative run ledger or drive a run transition.
package activity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	DefaultPageSize = 100
	MaxPageSize     = 500
	MaxEvents       = 100000
	MaxBytes        = 64 << 20
	MaxStreams      = 16
	MaxSourceID     = 512
)

var (
	ErrIntegrity   = errors.New("activity integrity failure")
	ErrUnavailable = errors.New("activity unavailable")
	ErrExhausted   = errors.New("activity resource ceiling reached")
)

type Event struct {
	SchemaVersion   string `json:"schema_version"`
	Ordinal         uint64 `json:"ordinal"`
	ActivityID      string `json:"activity_id"`
	RunID           string `json:"run_id"`
	OccurredAt      string `json:"occurred_at"`
	ObservedAt      string `json:"observed_at"`
	AuthorityLevel  string `json:"authority_level"`
	Category        string `json:"category"`
	Phase           string `json:"phase"`
	Status          string `json:"status"`
	Title           string `json:"title"`
	Detail          string `json:"detail"`
	TaskNumber      int    `json:"task_number"`
	IterationNumber int    `json:"iteration_number"`
	SourceKind      string `json:"source_kind"`
	SourceSessionID string `json:"source_session_id"`
	SourceEventID   string `json:"source_event_id"`
	SourceDigest    string `json:"source_digest"`
	CheckpointSHA   string `json:"checkpoint_sha,omitempty"`
	CheckpointClean bool   `json:"checkpoint_clean"`
}

type Page struct {
	SchemaVersion string  `json:"schema_version"`
	RunID         string  `json:"run_id"`
	Events        []Event `json:"events"`
	NextCursor    string  `json:"next_cursor"`
}

// ProviderEvent is the pinned provider's machine payload, never a public DTO.
type ProviderEvent struct {
	Type         string `json:"type"`
	Phase        string `json:"phase"`
	Section      string `json:"section,omitempty"`
	Text         string `json:"text"`
	Timestamp    string `json:"timestamp"`
	Signal       string `json:"signal,omitempty"`
	TaskNum      int    `json:"task_num,omitempty"`
	IterationNum int    `json:"iteration_num,omitempty"`
}

var sensitive = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)(?:password|passwd|secret|token|api[_-]?key|authorization|credential)["']?\s*[:=]\s*(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s"'<>]+)`),
	regexp.MustCompile(`(?is)-----BEGIN[^\n]*PRIVATE KEY-----.*?(?:-----END[^\n]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`(?i)(?:bearer\s+|(?:password|passwd|secret|token|api[_-]?key|authorization|credential)\s*[=:]\s*["']?)[^\s"'<>]+`),
	regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]+|gh[pousr]_[a-z0-9_]+|github_pat_[a-z0-9_]+|AKIA[A-Z0-9]{16}|eyJ[a-z0-9_-]+\.[a-z0-9_-]+\.[a-z0-9_-]+)\b`),
	regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*:(?://)?[^\s<>"']+`),
	regexp.MustCompile(`(?:[A-Za-z]:[\\/]|\\\\|/)[^\s<>"']+`),
	regexp.MustCompile(`[A-Za-z0-9_+/=-]{40,}`),
}

func redact(value string, limit int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, value)
	for _, pattern := range sensitive {
		value = pattern.ReplaceAllString(value, "[redacted]")
	}
	value = html.EscapeString(value)
	if len(value) > limit {
		value = value[:limit]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func digest(data []byte) string       { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func identity(parts ...string) string { encoded, _ := json.Marshal(parts); return digest(encoded) }
func jsonDigest(value any) string     { data, _ := json.Marshal(value); return digest(data) }
func stamp(t time.Time) string        { return t.UTC().Format(time.RFC3339Nano) }

// Source IDs are opaque hashes in the projection. Their original bytes are
// inputs to identity but are never persisted where they could disclose secrets.
func sourceID(value string) string {
	if value == "" {
		return ""
	}
	return "sha256-" + digest([]byte(value))
}

func baseEvent(run string, now time.Time) Event {
	return Event{SchemaVersion: "ActivityEventV1", RunID: run, OccurredAt: stamp(now), ObservedAt: stamp(now), AuthorityLevel: "PROVIDER_DETAIL", Phase: "abcp"}
}

func normalizeProvider(run, session, id string, p ProviderEvent, now time.Time) (Event, error) {
	if id == "" || len(id) > MaxSourceID || session == "" || len(session) > MaxSourceID || p.Timestamp == "" || p.TaskNum < 0 || p.IterationNum < 0 || !utf8.ValidString(p.Text) || !utf8.ValidString(p.Section) {
		return Event{}, ErrIntegrity
	}
	e := baseEvent(run, now)
	occurred, err := time.Parse(time.RFC3339Nano, p.Timestamp)
	if err != nil || occurred.IsZero() {
		return Event{}, ErrIntegrity
	}
	e.OccurredAt = stamp(occurred)
	p.Timestamp = e.OccurredAt
	e.SourceKind, e.SourceSessionID, e.SourceEventID = "RALPHEX_PROGRESS", sourceID(session), sourceID(id)
	switch p.Phase {
	case "task", "review", "codex", "eval":
		e.Phase = p.Phase
	case "claude-eval":
		e.Phase = "eval"
	case "plan", "finalize":
		e.Phase = "task"
	default:
		return Event{}, ErrIntegrity
	}
	e.TaskNumber, e.IterationNumber = p.TaskNum, p.IterationNum
	e.Category, e.Status, e.Title = "IMPLEMENTATION", "PROGRESS", "Implementation progress"
	switch {
	case p.Type == "error" || p.Signal == "FAILED":
		e.Category, e.Status, e.Title = "ERROR", "FAILED", "Implementation workflow failed"
	case p.Type == "signal" && p.Signal == "COMPLETED":
		e.Category, e.Status, e.Title = "LIFECYCLE", "COMPLETED", "Implementation workflow completed"
	case p.Type == "signal" && (p.Signal == "REVIEW_DONE" || p.Signal == "CODEX_REVIEW_DONE"):
		e.Category, e.Status, e.Title = "REVIEW", "COMPLETED", "Review completed"
	case p.Type == "task_start":
		e.Status, e.Title = "STARTED", "Implementation task started"
	case p.Type == "task_end":
		e.Status, e.Title = "COMPLETED", "Implementation task completed"
	case p.Type == "iteration_start" && (p.Phase == "review" || p.Phase == "codex"):
		e.Category, e.Status, e.Title = "REVIEW", "STARTED", "Review iteration started"
	case p.Type == "warn":
		e.Category, e.Status, e.Title = "WARNING", "UNKNOWN", "Implementation warning"
	case p.Phase == "review" || p.Phase == "codex":
		e.Category, e.Title = "REVIEW", "Review progress"
	case e.Phase == "eval":
		e.Category, e.Title = "REVIEW", "Review evaluation progress"
	case p.Phase == "finalize":
		e.Category, e.Title = "LIFECYCLE", "Implementation workflow progress"
	}
	e.Detail = redact(p.Text, 4096)
	// Digest the normalized payload, excluding observation time and ordinals.
	p.Text, p.Section = redact(p.Text, 4096), redact(p.Section, 256)
	e.SourceDigest = jsonDigest(p)
	e.ActivityID = identity(run, session, id, e.SourceDigest)
	return e, nil
}

func normalizeLedger(f ledger.Event, now time.Time) (Event, bool) {
	e := baseEvent(f.RunID, now)
	e.SourceKind, e.SourceEventID, e.SourceDigest = "ABCP_LEDGER", sourceID(f.EventID), jsonDigest(f)
	e.AuthorityLevel = "ABCP_STATE"
	e.OccurredAt = stamp(f.Timestamp)
	// Only the validated ledger state edge carries state authority. Payload text
	// and an event type that merely spells ACCEPTED cannot establish acceptance.
	switch string(f.StateTo) {
	case "HUMAN_DECISION_REQUIRED":
		e.Category, e.Status, e.Title = "HUMAN_REQUIRED", "REQUIRED", "Human decision required"
	case "BRANCH_ACCEPTANCE_PENDING":
		e.Category, e.Status, e.Title = "ACCEPTANCE", "PROGRESS", "Branch acceptance pending"
	case "BRANCH_ACCEPTED":
		e.AuthorityLevel = "ABCP_ACCEPTANCE"
		e.Category, e.Status, e.Title = "ACCEPTANCE", "ACCEPTED", "Branch accepted"
	case "FAILED":
		e.Category, e.Status, e.Title = "LIFECYCLE", "FAILED", "Run failed"
	case "CANCELLED":
		e.Category, e.Status, e.Title = "LIFECYCLE", "FAILED", "Run cancelled"
	default:
		return Event{}, false
	}
	e.ActivityID = identity(f.RunID, "ledger", f.EventID, e.SourceDigest)
	return e, true
}

func unknown(run, key, title string, now time.Time) Event {
	e := baseEvent(run, now)
	e.Category, e.Status, e.Title = "WARNING", "UNKNOWN", title
	e.SourceKind, e.SourceDigest = "RALPHEX_PROGRESS", identity(run, key)
	e.ActivityID = identity(run, "unknown", key)
	return e
}

package readmodel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

var testNow = time.Unix(1_800_000_000, 123).UTC()

type testCatalog struct {
	registrations map[string]runtimecatalog.RunRegistrationV1
}

func (c testCatalog) ReadRun(runID string) (runtimecatalog.RunRegistrationV1, error) {
	value, ok := c.registrations[runID]
	if !ok {
		return runtimecatalog.RunRegistrationV1{}, serviceapi.ErrDependencyNotFound
	}
	return value, nil
}

func testRegistration(runID, path, evidenceRoot string) runtimecatalog.RunRegistrationV1 {
	generation := runtimecatalog.LedgerGenerationV1{
		Kind: "LedgerGenerationV1", ParentDevice: 1, ParentInode: 1, FileDevice: 1, FileInode: 1,
	}
	if reader, err := ledger.OpenExistingReadOnlyJSONLLedger(path); err == nil {
		if observed, generationErr := reader.PhysicalGeneration(); generationErr == nil {
			generation.ParentDevice, generation.ParentInode = observed.ParentDevice, observed.ParentInode
			generation.FileDevice, generation.FileInode = observed.FileDevice, observed.FileInode
		}
		_ = reader.Close()
	}
	return runtimecatalog.RunRegistrationV1{
		Kind: "RunRegistrationV1", RunID: runID,
		RepositoryIdentityDigest: strings.Repeat("a", 64), AuthorityDigest: strings.Repeat("b", 64),
		CanonicalLedgerPath: path, LedgerGeneration: generation, CanonicalEvidenceRoot: evidenceRoot,
		InitialRegistrationTimestamp: testNow.Format(time.RFC3339Nano),
	}
}

func testSigner(t *testing.T) *serviceapi.CursorSigner {
	t.Helper()
	value, err := serviceapi.NewCursorSignerWithClock("test-key", []byte("0123456789abcdef0123456789abcdef"), func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func testService(t *testing.T, registration runtimecatalog.RunRegistrationV1) *Service {
	t.Helper()
	value, err := NewService(Config{
		Catalog:      testCatalog{registrations: map[string]runtimecatalog.RunRegistrationV1{registration.RunID: registration}},
		CursorSigner: testSigner(t), Clock: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func appendEvent(t *testing.T, target *ledger.JSONLLedger, event ledger.Event) {
	t.Helper()
	if err := target.Append(event); err != nil {
		t.Fatal(err)
	}
}

func event(runID, id, eventType string, offset int) ledger.Event {
	return ledger.Event{
		SchemaVersion: ledger.CurrentSchemaVersion, EventID: id,
		Timestamp: testNow.Add(time.Duration(offset) * time.Second), RunID: runID,
		EventType: eventType, Actor: "controller", Source: "test",
	}
}

func created(runID, id string) ledger.Event {
	value := event(runID, id, string(domain.StateRunCreated), 0)
	value.ProjectID, value.PlanID, value.AttemptID = "project-1", "plan-1", "attempt-1"
	value.Payload = map[string]any{"state": string(domain.StateRunCreated)}
	return value
}

func transition(runID, id string, from, to domain.State, offset int) ledger.Event {
	value := event(runID, id, "STATE_TRANSITION", offset)
	value.ProjectID, value.PlanID, value.AttemptID = "project-1", "plan-1", "attempt-1"
	value.StateFrom, value.StateTo = from, to
	return value
}

func makeLedger(t *testing.T, runID string, events ...ledger.Event) (*ledger.JSONLLedger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger", "events.jsonl")
	value, err := ledger.NewJSONLLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range events {
		appendEvent(t, value, item)
	}
	return value, path
}

func decodeRun(t *testing.T, data json.RawMessage) RunProjectionV1 {
	t.Helper()
	var value RunProjectionV1
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func decodePage(t *testing.T, data json.RawMessage) EventProjectionPageV1 {
	t.Helper()
	var value EventProjectionPageV1
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestProjectionPreservesHistoryAndDerivesStateOnlyFromEdges(t *testing.T) {
	runID := "projection-run"
	events := []ledger.Event{created(runID, "event-1")}
	events[0].TaskID, events[0].AgentSessionID = "task-1", "session-1"
	events[0].EvidenceRefs = []ledger.EvidenceRef{{URI: "/private/evidence/authority.json", SHA256: strings.Repeat("c", 64)}}
	events = append(events,
		transition(runID, "event-2", domain.StateRunCreated, domain.StateAuthorityValidated, 1),
		transition(runID, "event-3", domain.StateAuthorityValidated, domain.StateExecutionStarting, 2),
		transition(runID, "event-4", domain.StateExecutionStarting, domain.StateImplementing, 3),
	)
	events[1].TaskID, events[1].AgentSessionID = "task-1", "session-1"
	events[2].AttemptID, events[2].TaskID, events[2].AgentSessionID = "attempt-2", "task-2", "session-2"
	events[3].AttemptID, events[3].TaskID, events[3].AgentSessionID = "attempt-1", "task-1", "session-1"
	blocker := event(runID, "event-5", "BLOCKER_DECISION_RECORDED", 4)
	blocker.ProjectID, blocker.PlanID, blocker.AttemptID = "project-1", "plan-1", "attempt-2"
	blocker.StateFrom, blocker.StateTo = domain.StateImplementing, domain.StateHumanDecisionRequired
	blocker.Payload = map[string]any{
		"record_schema_version": 1,
		"classification":        map[string]any{"category": "decision"},
		"requirement":           map[string]any{"human_decision": map[string]any{"question": "continue?"}},
	}
	events = append(events, blocker)
	metadata := event(runID, "event-6", "OBSERVATION", 5)
	metadata.AttemptID, metadata.TaskID, metadata.AgentSessionID = "attempt-2", "task-2", "session-2"
	metadata.Payload = map[string]any{"state": string(domain.StateFailed)}
	events = append(events, metadata)
	terminal := transition(runID, "event-7", domain.StateHumanDecisionRequired, domain.StateFailed, 6)
	terminal.AttemptID = "attempt-2"
	events = append(events, terminal)

	ledgerValue, path := makeLedger(t, runID, events...)
	defer ledgerValue.Close()
	registration := testRegistration(runID, path, "/private/evidence")
	service := testService(t, registration)
	raw, err := service.ReadRunProjection(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	projection := decodeRun(t, raw)
	if projection.CurrentState != string(domain.StateFailed) || !projection.TerminalStatus.Terminal || projection.TerminalStatus.State != string(domain.StateFailed) {
		t.Fatalf("terminal state was not derived from state edges: %+v", projection.TerminalStatus)
	}
	if projection.EventCount != 7 || projection.EvidenceCount != 1 || len(projection.Attempts) != 2 || len(projection.Tasks) != 2 || len(projection.AgentSessions) != 2 {
		t.Fatalf("history was overwritten or miscounted: %+v", projection)
	}
	if projection.Attempts[0].ID != "attempt-1" || projection.Attempts[0].EventCount != 3 || projection.Attempts[1].ID != "attempt-2" || projection.Attempts[1].EventCount != 4 {
		t.Fatalf("attempt order/history = %+v", projection.Attempts)
	}
	if len(projection.Blockers) != 1 || projection.Blockers[0].EventID != "event-5" || len(projection.StateTransitions) != 5 {
		t.Fatalf("recognized summaries = blockers %+v transitions %+v", projection.Blockers, projection.StateTransitions)
	}
	if strings.Contains(string(raw), registration.CanonicalLedgerPath) || strings.Contains(string(raw), registration.CanonicalEvidenceRoot) {
		t.Fatal("run projection disclosed a registered controller path")
	}
}

func TestProjectionRejectsMalformedAndImpossibleHistory(t *testing.T) {
	runID := "bad-history"
	registration := testRegistration(runID, "/unused", "/private/evidence")
	physical := "ledger-dev-1-inode-2"
	encode := func(t *testing.T, events ...ledger.Event) []byte {
		t.Helper()
		var result []byte
		for _, item := range events {
			line, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			result = append(result, line...)
			result = append(result, '\n')
		}
		return result
	}
	wrongInitial := created(runID, "initial")
	wrongInitial.Payload["state"] = string(domain.StateFailed)
	wrongRun := created("other-run", "initial")
	badEdge := transition(runID, "bad-edge", domain.StateImplementing, domain.StateFailed, 1)
	duplicate := created(runID, "initial")
	conflictingProject := transition(runID, "project-conflict", domain.StateRunCreated, domain.StateAuthorityValidated, 1)
	conflictingProject.ProjectID = "different-project"
	for name, data := range map[string][]byte{
		"empty":                    nil,
		"partial":                  encode(t, created(runID, "initial"))[:len(encode(t, created(runID, "initial")))-1],
		"wrong initial payload":    encode(t, wrongInitial),
		"wrong run":                encode(t, wrongRun),
		"invalid chronology":       encode(t, created(runID, "initial"), badEdge),
		"conflicting project":      encode(t, created(runID, "initial"), conflictingProject),
		"duplicate event identity": encode(t, created(runID, "initial"), duplicate),
		"duplicate JSON field":     []byte(`{"schema_version":1,"schema_version":1}` + "\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := buildSnapshot(data, physical, registration); !errors.Is(err, serviceapi.ErrProjectionIntegrity) {
				t.Fatalf("error = %v, want projection integrity", err)
			}
		})
	}
	oversized := created(runID, "oversized")
	oversized.Payload = map[string]any{"state": string(domain.StateRunCreated), "padding": strings.Repeat("x", maxLedgerLineBytes)}
	if _, err := buildSnapshot(encode(t, oversized), physical, registration); !errors.Is(err, serviceapi.ErrProjectionIntegrity) {
		t.Fatalf("oversized line error = %v", err)
	}
	if _, err := buildSnapshot(encode(t, created(runID, "initial")), "ledger-physical-identity-unavailable", registration); !errors.Is(err, serviceapi.ErrProjectionIntegrity) {
		t.Fatalf("unavailable identity error = %v", err)
	}
}

func TestProjectionRejectsInvalidServiceVisibleAttemptID(t *testing.T) {
	runID := "invalid-attempt-run"
	initial := created(runID, "event-1")
	initial.AttemptID = "../outside"
	ledgerValue, path := makeLedger(t, runID, initial)
	defer ledgerValue.Close()
	service := testService(t, testRegistration(runID, path, filepath.Join(filepath.Dir(path), "evidence")))

	if _, err := service.ReadRunProjection(context.Background(), runID); !errors.Is(err, serviceapi.ErrProjectionIntegrity) {
		t.Fatalf("run projection error = %v, want projection integrity", err)
	}
	if _, err := service.ReadEventProjection(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 1}); !errors.Is(err, serviceapi.ErrProjectionIntegrity) {
		t.Fatalf("event projection error = %v, want projection integrity", err)
	}
}

func TestLifecycleSummaryRedactsRegisteredPathsFromEventType(t *testing.T) {
	runID := "lifecycle-redaction-run"
	root := t.TempDir()
	path := filepath.Join(root, "ledger", "events.jsonl")
	evidenceRoot := filepath.Join(root, "evidence")
	value, err := ledger.NewJSONLLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Close()
	appendEvent(t, value, created(runID, "event-1"))
	ledgerPathEvent := event(runID, "event-2", path, 1)
	ledgerPathEvent.Source = "merge-lifecycle"
	appendEvent(t, value, ledgerPathEvent)
	evidencePathEvent := event(runID, "event-3", filepath.Join(evidenceRoot, "artifact.json"), 2)
	evidencePathEvent.Source = "merge-lifecycle"
	appendEvent(t, value, evidencePathEvent)

	service := testService(t, testRegistration(runID, path, evidenceRoot))
	raw, err := service.ReadRunProjection(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	projection := decodeRun(t, raw)
	if len(projection.Lifecycle) != 2 || projection.Lifecycle[0].EventType != "[redacted]" || projection.Lifecycle[1].EventType != "[redacted]" {
		t.Fatalf("lifecycle summaries were not redacted: %+v", projection.Lifecycle)
	}
	if strings.Contains(string(raw), path) || strings.Contains(string(raw), evidenceRoot) {
		t.Fatal("lifecycle summary disclosed a registered path")
	}
}

func TestProjectionRevisionIsDeterministicAndBindsSnapshotAndIdentity(t *testing.T) {
	runID := "revision-run"
	registration := testRegistration(runID, "/unused", "/evidence")
	line, _ := json.Marshal(created(runID, "event-1"))
	data := append(line, '\n')
	first, err := buildSnapshot(data, "ledger-dev-1-inode-1", registration)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildSnapshot(append([]byte(nil), data...), "ledger-dev-1-inode-1", registration)
	if err != nil {
		t.Fatal(err)
	}
	otherIdentity, err := buildSnapshot(data, "ledger-dev-1-inode-2", registration)
	if err != nil {
		t.Fatal(err)
	}
	if first.Projection.ProjectionRevision != second.Projection.ProjectionRevision || first.Projection.ProjectionRevision == otherIdentity.Projection.ProjectionRevision {
		t.Fatal("revision is not deterministic or does not bind physical identity")
	}
	more := transition(runID, "event-2", domain.StateRunCreated, domain.StateAuthorityValidated, 1)
	moreLine, _ := json.Marshal(more)
	extended, err := buildSnapshot(append(append([]byte(nil), data...), append(moreLine, '\n')...), "ledger-dev-1-inode-1", registration)
	if err != nil {
		t.Fatal(err)
	}
	if extended.Projection.ProjectionRevision == first.Projection.ProjectionRevision {
		t.Fatal("revision did not bind exact snapshot bytes and last record")
	}
}

func TestServiceMissingRegisteredLedgerIsIntegrityAndIsNotCreated(t *testing.T) {
	runID := "missing-ledger-run"
	path := filepath.Join(t.TempDir(), "missing-parent", "events.jsonl")
	service := testService(t, testRegistration(runID, path, filepath.Join(filepath.Dir(path), "evidence")))
	if _, err := service.ReadRunProjection(context.Background(), runID); !errors.Is(err, serviceapi.ErrProjectionIntegrity) {
		t.Fatalf("missing ledger error = %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("projection materialized a missing ledger parent: %v", err)
	}
}

func TestRegisteredGenerationReplacementBlocksFreshReadAndWritablePaths(t *testing.T) {
	for _, attack := range []string{"file", "parent"} {
		t.Run(attack, func(t *testing.T) {
			root := t.TempDir()
			runID := "registered-replacement-" + attack
			parent := filepath.Join(root, "ledger")
			path := filepath.Join(parent, "events.jsonl")
			value, err := ledger.NewJSONLLedger(path)
			if err != nil {
				t.Fatal(err)
			}
			defer value.Close()
			appendEvent(t, value, created(runID, "event-1"))
			evidenceRoot := filepath.Join(root, "evidence")
			if err := os.Mkdir(evidenceRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			catalog, err := runtimecatalog.Open(filepath.Join(root, "service"))
			if err != nil {
				t.Fatal(err)
			}
			defer catalog.Close()
			registration, err := runtimecatalog.NewRunRegistrationV1(runID, "example/repository", strings.Repeat("a", 64), path, evidenceRoot, testNow)
			if err != nil {
				t.Fatal(err)
			}
			if err := catalog.RegisterRun(registration); err != nil {
				t.Fatal(err)
			}
			service, err := New(catalog, testSigner(t))
			if err != nil {
				t.Fatal(err)
			}
			lease, err := value.AcquireRunTransition(runID)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if attack == "file" {
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(parent, parent+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(parent, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := service.ReadRunProjection(context.Background(), runID); !errors.Is(err, serviceapi.ErrProjectionIntegrity) {
				t.Fatalf("fresh projection replacement error = %v", err)
			}
			entered := false
			if err := service.WithExistingWritableLedger(context.Background(), runID, func(*ledger.JSONLLedger) error {
				entered = true
				return nil
			}); !errors.Is(err, serviceapi.ErrProjectionIntegrity) || entered {
				t.Fatalf("writable replacement error = %v, entered=%v", err, entered)
			}
			if attack == "parent" {
				if _, err := os.Lstat(path + ".run-locks"); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("replacement parent gained a second run-lock generation: %v", err)
				}
			}
			if err := lease.Close(); err == nil {
				t.Fatal("original transition lease did not fail closed after replacement")
			}
		})
	}
}

func TestEventPaginationCursorLineageAndPathNonDisclosure(t *testing.T) {
	runID := "cursor-run"
	values := []ledger.Event{
		created(runID, "event-1"),
		transition(runID, "event-2", domain.StateRunCreated, domain.StateAuthorityValidated, 1),
		transition(runID, "event-3", domain.StateAuthorityValidated, domain.StateExecutionStarting, 2),
		transition(runID, "event-4", domain.StateExecutionStarting, domain.StateImplementing, 3),
	}
	values[0].Payload["private"] = "/private/evidence/secret.json"
	values[0].EvidenceRefs = []ledger.EvidenceRef{{URI: "/private/evidence/secret.json"}}
	ledgerValue, path := makeLedger(t, runID, values...)
	defer ledgerValue.Close()
	registration := testRegistration(runID, path, "/private/evidence")
	service := testService(t, registration)
	firstRaw, err := service.ReadEventProjection(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	first := decodePage(t, firstRaw)
	if len(first.Events) != 2 || first.Events[0].EventID != "event-1" || first.Events[1].EventID != "event-2" || first.NextCursor == "" {
		t.Fatalf("first page order/cursor = %+v", first)
	}
	if strings.Contains(string(firstRaw), path) || strings.Contains(string(firstRaw), registration.CanonicalEvidenceRoot) {
		t.Fatal("event projection disclosed a registered path")
	}

	appendEvent(t, ledgerValue, transition(runID, "event-5", domain.StateImplementing, domain.StateImplementationCompleted, 4))
	secondRaw, err := service.ReadEventProjection(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	second := decodePage(t, secondRaw)
	if len(second.Events) != 2 || second.Events[0].EventID != "event-3" || second.Events[1].EventID != "event-4" || second.NextCursor == "" {
		t.Fatalf("append-only continuation = %+v", second)
	}
	lastRaw, err := service.ReadEventProjection(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 2, Cursor: second.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	last := decodePage(t, lastRaw)
	if len(last.Events) != 1 || last.Events[0].EventID != "event-5" || last.NextCursor != "" {
		t.Fatalf("last page = %+v", last)
	}

	replacement := byte('A')
	if first.NextCursor[len(first.NextCursor)-1] == replacement {
		replacement = 'B'
	}
	tampered := first.NextCursor[:len(first.NextCursor)-1] + string(replacement)
	if _, err := service.ReadEventProjection(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 2, Cursor: tampered}); !errors.Is(err, serviceapi.ErrInvalidCursor) {
		t.Fatalf("tampered cursor error = %v", err)
	}
	wrongKind, err := service.cursors.Sign(serviceapi.CursorKindCatalog, LedgerFilterIdentity(runID), map[string]any{"value": "wrong-kind"}, testNow, testNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadEventProjection(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 2, Cursor: wrongKind}); !errors.Is(err, serviceapi.ErrInvalidCursor) {
		t.Fatalf("wrong-kind cursor error = %v", err)
	}
	invalidPayload, err := service.cursors.Sign(serviceapi.CursorKindLedger, LedgerFilterIdentity(runID), LedgerCursorPayloadV1{SchemaVersion: LedgerCursorSchemaV1, RunID: runID}, testNow, testNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadEventProjection(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 2, Cursor: invalidPayload}); !errors.Is(err, serviceapi.ErrInvalidCursor) {
		t.Fatalf("invalid signed payload error = %v", err)
	}
	otherRunRegistration := registration
	otherRunRegistration.RunID = "other-run"
	otherService := testService(t, otherRunRegistration)
	if _, err := otherService.ReadEventProjection(context.Background(), "other-run", serviceapi.PageRequestV1{PageSize: 2, Cursor: first.NextCursor}); !errors.Is(err, serviceapi.ErrInvalidCursor) {
		t.Fatalf("filter-bound cursor error = %v", err)
	}
	epochSigner, _ := serviceapi.NewCursorSignerWithClock("other-key", []byte("abcdef0123456789abcdef0123456789"), func() time.Time { return testNow })
	epochService, _ := NewService(Config{Catalog: service.catalog, CursorSigner: epochSigner, Clock: func() time.Time { return testNow }})
	if _, err := epochService.ReadEventProjection(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 2, Cursor: first.NextCursor}); !errors.Is(err, serviceapi.ErrCursorEpochChanged) {
		t.Fatalf("cursor epoch error = %v", err)
	}
}

func TestEventCursorHashesPathShapedTerminalEventID(t *testing.T) {
	for _, test := range []struct {
		name       string
		terminalID func(string, string) string
	}{
		{name: "registered ledger path", terminalID: func(ledgerPath, _ string) string { return ledgerPath }},
		{name: "registered evidence root", terminalID: func(_, evidenceRoot string) string { return evidenceRoot }},
	} {
		t.Run(test.name, func(t *testing.T) {
			runID := "path-event-id-run"
			root := t.TempDir()
			ledgerPath := filepath.Join(root, "ledger", "events.jsonl")
			evidenceRoot := filepath.Join(root, "evidence")
			terminalID := test.terminalID(ledgerPath, evidenceRoot)
			value, err := ledger.NewJSONLLedger(ledgerPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = value.Close() })
			appendEvent(t, value, created(runID, "event-1"))
			appendEvent(t, value, transition(runID, terminalID, domain.StateRunCreated, domain.StateAuthorityValidated, 1))

			registration := testRegistration(runID, ledgerPath, evidenceRoot)
			service := testService(t, registration)
			raw, err := service.ReadEventProjection(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 1})
			if err != nil {
				t.Fatal(err)
			}
			cursor := decodePage(t, raw).NextCursor
			if cursor == "" {
				t.Fatal("first page did not return a cursor")
			}
			decodedCursor, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(decodedCursor), ledgerPath) || strings.Contains(string(decodedCursor), evidenceRoot) {
				t.Fatalf("decoded cursor disclosed a registered path: %s", decodedCursor)
			}
			var payload LedgerCursorPayloadV1
			if err := service.cursors.Verify(cursor, serviceapi.CursorKindLedger, LedgerFilterIdentity(runID), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.PriorLastEventIDSHA256 != digest([]byte(terminalID)) {
				t.Fatalf("terminal EventID digest = %q", payload.PriorLastEventIDSHA256)
			}

			mismatched := payload
			mismatched.PriorLastEventIDSHA256 = digest([]byte("different-terminal-event"))
			mismatchedCursor, err := service.cursors.Sign(serviceapi.CursorKindLedger, LedgerFilterIdentity(runID), mismatched, testNow, testNow.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.ReadEventProjection(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 1, Cursor: mismatchedCursor}); !errors.Is(err, serviceapi.ErrProjectionLineageChanged) {
				t.Fatalf("mismatched terminal EventID digest error = %v", err)
			}

			appendEvent(t, value, transition(runID, "event-3", domain.StateAuthorityValidated, domain.StateExecutionStarting, 2))
			continuedRaw, err := service.ReadEventProjection(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 2, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			continued := decodePage(t, continuedRaw)
			if len(continued.Events) != 2 || continued.Events[0].EventID != "[redacted]" || continued.Events[1].EventID != "event-3" || continued.NextCursor != "" {
				t.Fatalf("append-only continuation = %+v", continued)
			}
		})
	}
}

func TestEventCursorRejectsTruncationReplacementMutationAndPartialLine(t *testing.T) {
	newFixture := func(t *testing.T) (*Service, *ledger.JSONLLedger, string, string) {
		t.Helper()
		runID := "lineage-run"
		value, path := makeLedger(t, runID,
			created(runID, "event-1"),
			transition(runID, "event-2", domain.StateRunCreated, domain.StateAuthorityValidated, 1),
			transition(runID, "event-3", domain.StateAuthorityValidated, domain.StateExecutionStarting, 2),
		)
		service := testService(t, testRegistration(runID, path, filepath.Join(filepath.Dir(path), "evidence")))
		raw, err := service.ReadEventProjection(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 1})
		if err != nil {
			t.Fatal(err)
		}
		return service, value, path, decodePage(t, raw).NextCursor
	}
	assertLineage := func(t *testing.T, service *Service, cursor string) {
		t.Helper()
		if _, err := service.ReadEventProjection(context.Background(), "lineage-run", serviceapi.PageRequestV1{PageSize: 1, Cursor: cursor}); !errors.Is(err, serviceapi.ErrProjectionLineageChanged) {
			t.Fatalf("lineage error = %v", err)
		}
	}

	t.Run("truncation", func(t *testing.T) {
		service, value, path, cursor := newFixture(t)
		_ = value.Close()
		data, _ := os.ReadFile(path)
		last := strings.LastIndex(strings.TrimSuffix(string(data), "\n"), "\n")
		if err := os.WriteFile(path, data[:last+1], 0o600); err != nil {
			t.Fatal(err)
		}
		assertLineage(t, service, cursor)
	})
	t.Run("replacement", func(t *testing.T) {
		service, value, path, cursor := newFixture(t)
		_ = value.Close()
		data, _ := os.ReadFile(path)
		if err := os.Rename(path, path+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		assertLineage(t, service, cursor)
	})
	t.Run("prior bytes changed", func(t *testing.T) {
		service, value, path, cursor := newFixture(t)
		_ = value.Close()
		data, _ := os.ReadFile(path)
		data = []byte(strings.Replace(string(data), `"actor":"controller"`, `"actor":"replacement"`, 1))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		assertLineage(t, service, cursor)
	})
	t.Run("partial newly appended record is integrity", func(t *testing.T) {
		service, value, path, cursor := newFixture(t)
		_ = value.Close()
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.WriteString(`{"schema_version":1`)
		_ = file.Close()
		if _, err := service.ReadEventProjection(context.Background(), "lineage-run", serviceapi.PageRequestV1{PageSize: 1, Cursor: cursor}); !errors.Is(err, serviceapi.ErrProjectionIntegrity) || errors.Is(err, serviceapi.ErrProjectionLineageChanged) {
			t.Fatalf("partial append error = %v, want projection integrity", err)
		}
	})
	t.Run("malformed append is integrity", func(t *testing.T) {
		service, value, path, cursor := newFixture(t)
		_ = value.Close()
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.WriteString(`{"complete":"but not a ledger event"}` + "\n")
		_ = file.Close()
		if _, err := service.ReadEventProjection(context.Background(), "lineage-run", serviceapi.PageRequestV1{PageSize: 1, Cursor: cursor}); !errors.Is(err, serviceapi.ErrProjectionIntegrity) || errors.Is(err, serviceapi.ErrProjectionLineageChanged) {
			t.Fatalf("malformed append error = %v", err)
		}
	})
}

type blockingReader struct {
	data      []byte
	identity  string
	release   <-chan struct{}
	active    *atomic.Int32
	maximum   *atomic.Int32
	snapshots *atomic.Int32
}

func (r *blockingReader) Snapshot() ([]byte, string, error) {
	current := r.active.Add(1)
	for {
		prior := r.maximum.Load()
		if current <= prior || r.maximum.CompareAndSwap(prior, current) {
			break
		}
	}
	r.snapshots.Add(1)
	<-r.release
	r.active.Add(-1)
	return append([]byte(nil), r.data...), r.identity, nil
}

func (*blockingReader) Close() error { return nil }

func TestServiceSnapshotSemaphoreCeilingCancellationAndNoCache(t *testing.T) {
	runID := "bounded-run"
	line, _ := json.Marshal(created(runID, "event-1"))
	data := append(line, '\n')
	releaseSnapshots := make(chan struct{})
	var active, maximum, snapshots, opens atomic.Int32
	registration := testRegistration(runID, "/registered/events.jsonl", "/registered/evidence")
	service, err := NewService(Config{
		Catalog:      testCatalog{registrations: map[string]runtimecatalog.RunRegistrationV1{runID: registration}},
		CursorSigner: testSigner(t), Clock: func() time.Time { return testNow },
		OpenReadOnlyLedger: func(string, ledger.PhysicalGeneration) (ExistingReadOnlyLedger, error) {
			opens.Add(1)
			return &blockingReader{data: data, identity: "ledger-dev-9-inode-9", release: releaseSnapshots, active: &active, maximum: &maximum, snapshots: &snapshots}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	errorsOut := make(chan error, MaxConcurrentSnapshots)
	for index := 0; index < MaxConcurrentSnapshots; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := service.ReadRunProjection(context.Background(), runID)
			errorsOut <- err
		}()
	}
	deadline := time.Now().Add(time.Second)
	for snapshots.Load() != MaxConcurrentSnapshots && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshots.Load() != MaxConcurrentSnapshots || maximum.Load() > MaxConcurrentSnapshots {
		t.Fatalf("snapshot concurrency active=%d maximum=%d", snapshots.Load(), maximum.Load())
	}
	cancelContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.ReadRunProjection(cancelContext, runID); !errors.Is(err, context.Canceled) || !errors.Is(err, serviceapi.ErrAuthoritativeReadBusy) {
		t.Fatalf("cancelled semaphore wait error = %v", err)
	}
	close(releaseSnapshots)
	group.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}
	if opens.Load() != MaxConcurrentSnapshots || snapshots.Load() != MaxConcurrentSnapshots {
		t.Fatalf("one-read/one-snapshot invariant opens=%d snapshots=%d", opens.Load(), snapshots.Load())
	}
	if _, err := service.ReadRunProjection(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	if opens.Load() != MaxConcurrentSnapshots+1 {
		t.Fatal("service cached a ledger object across requests")
	}
}

func TestWritableLedgerSnapshotsShareFourSnapshotCeiling(t *testing.T) {
	runID := "writable-snapshot-ceiling"
	value, path := makeLedger(t, runID, created(runID, "event-1"))
	if err := value.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	releaseSnapshots := make(chan struct{})
	var active, maximum, snapshots atomic.Int32
	registration := testRegistration(runID, path, filepath.Join(filepath.Dir(path), "evidence"))
	service, err := NewService(Config{
		Catalog:      testCatalog{registrations: map[string]runtimecatalog.RunRegistrationV1{runID: registration}},
		CursorSigner: testSigner(t), Clock: func() time.Time { return testNow },
		OpenReadOnlyLedger: func(string, ledger.PhysicalGeneration) (ExistingReadOnlyLedger, error) {
			return &blockingReader{data: data, identity: "ledger-dev-9-inode-9", release: releaseSnapshots, active: &active, maximum: &maximum, snapshots: &snapshots}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	errorsOut := make(chan error, MaxConcurrentSnapshots)
	for index := 0; index < MaxConcurrentSnapshots; index++ {
		go func() {
			_, readErr := service.ReadRunProjection(context.Background(), runID)
			errorsOut <- readErr
		}()
	}
	deadline := time.Now().Add(time.Second)
	for snapshots.Load() != MaxConcurrentSnapshots && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshots.Load() != MaxConcurrentSnapshots {
		close(releaseSnapshots)
		t.Fatalf("only %d snapshots reached the shared ceiling", snapshots.Load())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	entered := false
	err = service.WithExistingWritableLedger(ctx, runID, func(writable *ledger.JSONLLedger) error {
		entered = true
		_, _, snapshotErr := writable.Snapshot()
		return snapshotErr
	})
	if !entered || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, serviceapi.ErrAuthoritativeReadBusy) {
		close(releaseSnapshots)
		t.Fatalf("writable snapshot ceiling error = %v, entered=%v", err, entered)
	}

	close(releaseSnapshots)
	for index := 0; index < MaxConcurrentSnapshots; index++ {
		if err := <-errorsOut; err != nil {
			t.Fatal(err)
		}
	}
	if err := service.WithExistingWritableLedger(context.Background(), runID, func(writable *ledger.JSONLLedger) error {
		snapshot, _, snapshotErr := writable.Snapshot()
		if snapshotErr == nil && !bytes.Equal(snapshot, data) {
			t.Fatal("writable snapshot did not return the authoritative bytes")
		}
		return snapshotErr
	}); err != nil {
		t.Fatal(err)
	}
}

func TestServiceGlobalWritableObjectCeilingIsEight(t *testing.T) {
	runID := "writer-ceiling"
	value, path := makeLedger(t, runID, created(runID, "event-1"))
	if err := value.Close(); err != nil {
		t.Fatal(err)
	}
	service := testService(t, testRegistration(runID, path, filepath.Join(filepath.Dir(path), "evidence")))
	releaseWriters := make(chan struct{})
	entered := make(chan struct{}, MaxOpenLedgerObjects)
	errorsOut := make(chan error, MaxOpenLedgerObjects)
	for index := 0; index < MaxOpenLedgerObjects; index++ {
		go func() {
			errorsOut <- service.WithExistingWritableLedger(context.Background(), runID, func(*ledger.JSONLLedger) error {
				entered <- struct{}{}
				<-releaseWriters
				return nil
			})
		}()
	}
	for index := 0; index < MaxOpenLedgerObjects; index++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("eight ledger objects did not enter")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := service.WithExistingWritableLedger(ctx, runID, func(*ledger.JSONLLedger) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ninth object error = %v", err)
	}
	close(releaseWriters)
	for index := 0; index < MaxOpenLedgerObjects; index++ {
		if err := <-errorsOut; err != nil {
			t.Fatal(err)
		}
	}
}

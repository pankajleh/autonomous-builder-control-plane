package timeline

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

var timelineTestNow = time.Unix(1_900_000_000, 123).UTC()

type snapshotFixture struct {
	mu       sync.RWMutex
	snapshot readmodel.Snapshot
	calls    atomic.Int32
	err      error
}

func (s *snapshotFixture) Snapshot(context.Context, string) (readmodel.Snapshot, error) {
	s.calls.Add(1)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot, s.err
}

func (s *snapshotFixture) set(snapshot readmodel.Snapshot) {
	s.mu.Lock()
	s.snapshot = snapshot
	s.mu.Unlock()
}

type catalogFixture struct {
	registration runtimecatalog.RunRegistrationV1
	err          error
}

func (c catalogFixture) ReadRun(string) (runtimecatalog.RunRegistrationV1, error) {
	return c.registration, c.err
}

func TestTimelinePreservesAuthoritativeOrderAndHistoricalIdentities(t *testing.T) {
	runID := "timeline-history"
	root := t.TempDir()
	registration := timelineRegistration(runID, root)
	events := []ledger.Event{
		timelineCreated(runID, "event-1"),
		timelineEvent(runID, "event-2", 1),
		timelineEvent(runID, "event-3", 2),
		timelineEvent(runID, "event-4", 3),
	}
	events[1].AttemptID, events[1].TaskID, events[1].AgentSessionID = "attempt-1", "task-1", "session-1"
	events[1].StateFrom, events[1].StateTo = domain.StateRunCreated, domain.StateAuthorityValidated
	events[2].AttemptID, events[2].TaskID, events[2].AgentSessionID = "attempt-2", "task-2", "session-2"
	events[2].StateFrom, events[2].StateTo = domain.StateAuthorityValidated, domain.StateExecutionStarting
	events[3].AttemptID, events[3].TaskID, events[3].AgentSessionID = "attempt-1", "task-1", "session-1"
	events[3].Payload = map[string]any{"state": string(domain.StateCompleted), "path": registration.CanonicalEvidenceRoot}
	events[3].CorrelationID = registration.CanonicalLedgerPath
	snapshots := &snapshotFixture{snapshot: timelineSnapshot(t, runID, events)}
	service := timelineService(t, snapshots, registration, nil)

	firstRaw, err := service.ReadTimeline(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	first := decodeTimelinePage(t, firstRaw)
	if len(first.Entries) != 2 || first.Entries[0].EventID != "event-1" || first.Entries[1].EventID != "event-2" || first.NextCursor == "" {
		t.Fatalf("first timeline page = %+v", first)
	}
	secondRaw, err := service.ReadTimeline(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	second := decodeTimelinePage(t, secondRaw)
	if len(second.Entries) != 2 || second.Entries[0].AttemptID != "attempt-2" || second.Entries[0].TaskID != "task-2" || second.Entries[0].AgentSessionID != "session-2" ||
		second.Entries[1].AttemptID != "attempt-1" || second.Entries[1].TaskID != "task-1" || second.Entries[1].AgentSessionID != "session-1" {
		t.Fatalf("historical identities were not preserved: %+v", second.Entries)
	}
	if second.Entries[1].StateFrom != "" || second.Entries[1].StateTo != "" {
		t.Fatalf("payload fabricated a lifecycle transition: %+v", second.Entries[1])
	}
	if second.Entries[1].CorrelationID != "[redacted]" || strings.Contains(string(secondRaw), registration.CanonicalLedgerPath) || strings.Contains(string(secondRaw), registration.CanonicalEvidenceRoot) {
		t.Fatalf("timeline disclosed registered paths: %s", secondRaw)
	}
	if snapshots.calls.Load() != 2 {
		t.Fatalf("snapshot calls = %d, want one per page", snapshots.calls.Load())
	}
}

func TestServiceConsumesFrozenReadModelSnapshotContract(t *testing.T) {
	runID := "readmodel-contract"
	root := t.TempDir()
	registration := timelineRegistration(runID, root)
	value, err := ledger.NewJSONLLedger(registration.CanonicalLedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = value.Close() })
	created := timelineCreated(runID, "event-1")
	observed := timelineEvent(runID, "event-2", 1)
	observed.AttemptID, observed.TaskID, observed.AgentSessionID = "attempt-1", "task-1", "session-1"
	if err := value.Append(created); err != nil {
		t.Fatal(err)
	}
	if err := value.Append(observed); err != nil {
		t.Fatal(err)
	}
	catalog := catalogFixture{registration: registration}
	readService, err := readmodel.NewService(readmodel.Config{
		Catalog: catalog, CursorSigner: timelineSigner(t), Clock: func() time.Time { return timelineTestNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(readService, catalog, timelineSigner(t))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := service.ReadTimeline(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	page := decodeTimelinePage(t, raw)
	if len(page.Entries) != 2 || page.Entries[0].EventID != "event-1" || page.Entries[1].AttemptID != "attempt-1" || page.ProjectionRevision == "" {
		t.Fatalf("read-model-backed timeline = %+v", page)
	}
}

func TestTimelineCursorTamperRouteBindingAndFailClosedLineage(t *testing.T) {
	runID := "timeline-cursor"
	root := t.TempDir()
	registration := timelineRegistration(runID, root)
	events := []ledger.Event{timelineCreated(runID, "event-1"), timelineEvent(runID, "event-2", 1), timelineEvent(runID, "event-3", 2)}
	snapshots := &snapshotFixture{snapshot: timelineSnapshot(t, runID, events)}
	service := timelineService(t, snapshots, registration, nil)

	raw, err := service.ReadTimeline(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	cursor := decodeTimelinePage(t, raw).NextCursor
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(decoded), registration.CanonicalLedgerPath) || strings.Contains(string(decoded), registration.CanonicalEvidenceRoot) {
		t.Fatalf("cursor disclosed registered paths: %s", decoded)
	}
	replacement := byte('A')
	if cursor[len(cursor)-1] == replacement {
		replacement = 'B'
	}
	if _, err := service.ReadTimeline(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 1, Cursor: cursor[:len(cursor)-1] + string(replacement)}); !errors.Is(err, serviceapi.ErrInvalidCursor) {
		t.Fatalf("tampered cursor error = %v", err)
	}
	if _, err := service.ListEvidence(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 1, Cursor: cursor}); !errors.Is(err, serviceapi.ErrInvalidCursor) {
		t.Fatalf("cross-route cursor error = %v", err)
	}

	appended := append(append([]ledger.Event(nil), events...), timelineEvent(runID, "event-4", 3))
	snapshots.set(timelineSnapshot(t, runID, appended))
	if _, err := service.ReadTimeline(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 1, Cursor: cursor}); !errors.Is(err, serviceapi.ErrProjectionLineageChanged) {
		t.Fatalf("changed-lineage error = %v", err)
	}

	otherSigner, err := serviceapi.NewCursorSignerWithClock("other-epoch", []byte("0123456789abcdef0123456789abcdef"), func() time.Time { return timelineTestNow })
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewService(Config{Snapshots: snapshots, Catalog: catalogFixture{registration: registration}, CursorSigner: otherSigner, Clock: func() time.Time { return timelineTestNow }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.ReadTimeline(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 1, Cursor: cursor}); !errors.Is(err, serviceapi.ErrCursorEpochChanged) {
		t.Fatalf("cursor epoch error = %v", err)
	}
}

func TestEvidenceIndexIsOpaqueOrderedBoundedAndNonDisclosing(t *testing.T) {
	runID := "evidence-index"
	root := t.TempDir()
	registration := timelineRegistration(runID, root)
	artifact := filepath.Join(registration.CanonicalEvidenceRoot, "artifact.txt")
	if err := os.MkdirAll(registration.CanonicalEvidenceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte("downloadable bytes")
	if err := os.WriteFile(artifact, data, 0o600); err != nil {
		t.Fatal(err)
	}
	valid := ledger.EvidenceRef{URI: artifact, SHA256: digest(data), Kind: "stdout"}
	refs := []ledger.EvidenceRef{
		valid,
		{URI: "relative/artifact", SHA256: digest(data), Kind: "relative"},
		{URI: "https://example.invalid/artifact", SHA256: digest(data), Kind: "remote"},
		{URI: "artifact:opaque", SHA256: digest(data), Kind: "abstract"},
		{URI: filepath.Join(registration.CanonicalEvidenceRoot, "no-digest"), Kind: "missing-digest"},
		{URI: filepath.Join(registration.CanonicalEvidenceRoot, "malformed"), SHA256: registration.CanonicalLedgerPath, Kind: registration.CanonicalEvidenceRoot},
		{URI: filepath.Join(root, "outside"), SHA256: digest(data), Kind: "outside"},
		{URI: filepath.Join(registration.CanonicalEvidenceRoot, "missing"), SHA256: digest(data), Kind: "missing"},
	}
	events := []ledger.Event{timelineCreated(runID, "event-1"), timelineEvent(runID, registration.CanonicalEvidenceRoot, 1)}
	events[1].EvidenceRefs = refs
	snapshots := &snapshotFixture{snapshot: timelineSnapshot(t, runID, events)}
	service := timelineService(t, snapshots, registration, nil)

	raw, err := service.ListEvidence(context.Background(), runID, serviceapi.PageRequestV1{PageSize: len(refs)})
	if err != nil {
		t.Fatal(err)
	}
	page := decodeEvidencePage(t, raw)
	if len(page.Evidence) != len(refs) {
		t.Fatalf("evidence count = %d, want %d", len(page.Evidence), len(refs))
	}
	idPattern := regexp.MustCompile(`^[0-9a-f]{64}$`)
	seen := map[string]struct{}{}
	for index, descriptor := range page.Evidence {
		if !idPattern.MatchString(descriptor.EvidenceID) {
			t.Fatalf("descriptor %d has non-opaque ID %q", index, descriptor.EvidenceID)
		}
		if _, duplicate := seen[descriptor.EvidenceID]; duplicate {
			t.Fatalf("duplicate evidence ID %q", descriptor.EvidenceID)
		}
		seen[descriptor.EvidenceID] = struct{}{}
		if descriptor.SourceEvent.Ordinal != 2 || descriptor.SourceEvent.EventID != "[redacted]" {
			t.Fatalf("source event identity = %+v", descriptor.SourceEvent)
		}
	}
	if !page.Evidence[0].Downloadable || page.Evidence[0].ByteSize == nil || *page.Evidence[0].ByteSize != int64(len(data)) || page.Evidence[0].SHA256 != digest(data) {
		t.Fatalf("valid descriptor = %+v", page.Evidence[0])
	}
	for index := 1; index < len(page.Evidence); index++ {
		if page.Evidence[index].Downloadable || page.Evidence[index].ByteSize != nil {
			t.Fatalf("metadata-only descriptor %d = %+v", index, page.Evidence[index])
		}
	}
	for _, private := range []string{registration.CanonicalLedgerPath, registration.CanonicalEvidenceRoot, artifact, refs[1].URI, refs[2].URI} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("evidence response disclosed %q: %s", private, raw)
		}
	}
	if page.Evidence[5].SHA256 != "" || page.Evidence[5].Kind != "[redacted]" {
		t.Fatalf("malformed private metadata was exposed: %+v", page.Evidence[5])
	}
}

func TestEvidenceIDBindsRunEventOrdinalAndCompleteReference(t *testing.T) {
	ref := ledger.EvidenceRef{URI: "/evidence/a", SHA256: strings.Repeat("a", 64), Kind: "kind"}
	base := evidenceItem{Event: timelineEvent("run-a", "event-a", 1), EventOrdinal: 2, Ref: ref, RefOrdinal: 1}
	identities := []string{
		evidenceIdentity("run-a", base),
		evidenceIdentity("run-b", base),
		evidenceIdentity("run-a", withEventID(base, "event-b")),
		evidenceIdentity("run-a", withEventOrdinal(base, 3)),
		evidenceIdentity("run-a", withRefOrdinal(base, 2)),
		evidenceIdentity("run-a", withRefDigest(base, strings.Repeat("b", 64))),
		evidenceIdentity("run-a", withRefURI(base, "/evidence/b")),
	}
	seen := map[string]struct{}{}
	for _, identity := range identities {
		if _, duplicate := seen[identity]; duplicate {
			t.Fatalf("evidence identity did not bind every component: %v", identities)
		}
		seen[identity] = struct{}{}
	}
}

func TestEvidenceDownloadUsesFreshBindingExactRootAndReturnsNoUnverifiedBytes(t *testing.T) {
	runID := "fresh-download"
	root := t.TempDir()
	registration := timelineRegistration(runID, root)
	data := []byte("fresh artifact")
	ref := ledger.EvidenceRef{URI: filepath.Join(registration.CanonicalEvidenceRoot, "artifact"), SHA256: digest(data), Kind: "test"}
	event := timelineEvent(runID, "event-2", 1)
	event.EvidenceRefs = []ledger.EvidenceRef{ref}
	snapshots := &snapshotFixture{snapshot: timelineSnapshot(t, runID, []ledger.Event{timelineCreated(runID, "event-1"), event})}
	var roots []string
	var maximums []int64
	service := timelineService(t, snapshots, registration, func(root string, got ledger.EvidenceRef, maximum int64) ([]byte, error) {
		roots = append(roots, root)
		maximums = append(maximums, maximum)
		if got != ref {
			t.Fatalf("reader ref = %+v, want %+v", got, ref)
		}
		return append([]byte(nil), data...), nil
	})
	id := evidenceIdentity(runID, evidenceItem{Event: event, EventOrdinal: 2, Ref: ref, RefOrdinal: 1})

	got, err := service.ReadEvidence(context.Background(), runID, id)
	if err != nil || string(got) != string(data) {
		t.Fatalf("evidence read = %q, %v", got, err)
	}
	if len(roots) != 1 || roots[0] != registration.CanonicalEvidenceRoot || len(maximums) != 1 || maximums[0] != int64(serviceapi.MaxEvidenceDownloadSize) {
		t.Fatalf("reader authority roots=%v maximums=%v", roots, maximums)
	}

	withoutRef := timelineEvent(runID, "event-2", 1)
	snapshots.set(timelineSnapshot(t, runID, []ledger.Event{timelineCreated(runID, "event-1"), withoutRef}))
	got, err = service.ReadEvidence(context.Background(), runID, id)
	if !errors.Is(err, serviceapi.ErrDependencyNotFound) || got != nil || len(roots) != 1 {
		t.Fatalf("stale binding read = %q, %v; reader calls=%d", got, err, len(roots))
	}

	snapshots.set(timelineSnapshot(t, runID, []ledger.Event{timelineCreated(runID, "event-1"), event}))
	corruptService := timelineService(t, snapshots, registration, func(string, ledger.EvidenceRef, int64) ([]byte, error) {
		return []byte("unverified bytes"), nil
	})
	got, err = corruptService.ReadEvidence(context.Background(), runID, id)
	if !errors.Is(err, serviceapi.ErrEvidenceIntegrityChanged) || got != nil {
		t.Fatalf("digest-mismatch service read = %q, %v", got, err)
	}
	oversizedService := timelineService(t, snapshots, registration, func(string, ledger.EvidenceRef, int64) ([]byte, error) {
		return make([]byte, serviceapi.MaxEvidenceDownloadSize+1), nil
	})
	got, err = oversizedService.ReadEvidence(context.Background(), runID, id)
	if !errors.Is(err, serviceapi.ErrEvidenceIntegrityChanged) || got != nil {
		t.Fatalf("oversized service read = %d bytes, %v", len(got), err)
	}
}

func TestEvidencePageHardMaximumAndFlattenedCursor(t *testing.T) {
	runID := "evidence-pages"
	registration := timelineRegistration(runID, t.TempDir())
	event := timelineEvent(runID, "event-2", 1)
	for index := 0; index < serviceapi.MaxEvidencePageSize+1; index++ {
		event.EvidenceRefs = append(event.EvidenceRefs, ledger.EvidenceRef{URI: "metadata-" + string(rune(index+1)), Kind: "metadata"})
	}
	snapshots := &snapshotFixture{snapshot: timelineSnapshot(t, runID, []ledger.Event{timelineCreated(runID, "event-1"), event})}
	service := timelineService(t, snapshots, registration, nil)

	if _, err := service.ListEvidence(context.Background(), runID, serviceapi.PageRequestV1{PageSize: serviceapi.MaxEvidencePageSize + 1}); !errors.Is(err, serviceapi.ErrInvalidCursor) {
		t.Fatalf("oversized page error = %v", err)
	}
	if snapshots.calls.Load() != 0 {
		t.Fatal("invalid page consumed an authoritative snapshot")
	}
	firstRaw, err := service.ListEvidence(context.Background(), runID, serviceapi.PageRequestV1{PageSize: serviceapi.MaxEvidencePageSize})
	if err != nil {
		t.Fatal(err)
	}
	first := decodeEvidencePage(t, firstRaw)
	if len(first.Evidence) != serviceapi.MaxEvidencePageSize || first.NextCursor == "" {
		t.Fatalf("first evidence page count/cursor = %d, %q", len(first.Evidence), first.NextCursor)
	}
	lastRaw, err := service.ListEvidence(context.Background(), runID, serviceapi.PageRequestV1{PageSize: serviceapi.MaxEvidencePageSize, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	last := decodeEvidencePage(t, lastRaw)
	if len(last.Evidence) != 1 || last.NextCursor != "" {
		t.Fatalf("last evidence page = %+v", last)
	}
}

func TestEvidenceDownloadConcurrencyCancellationAndNoCache(t *testing.T) {
	runID := "download-concurrency"
	registration := timelineRegistration(runID, t.TempDir())
	data := []byte("shared bytes")
	event := timelineEvent(runID, "event-2", 1)
	for index := 0; index < MaxConcurrentDownloads+1; index++ {
		event.EvidenceRefs = append(event.EvidenceRefs, ledger.EvidenceRef{
			URI: filepath.Join(registration.CanonicalEvidenceRoot, "artifact-"+string(rune('a'+index))), SHA256: digest(data), Kind: "test",
		})
	}
	snapshots := &snapshotFixture{snapshot: timelineSnapshot(t, runID, []ledger.Event{timelineCreated(runID, "event-1"), event})}
	release := make(chan struct{})
	var active, maximum, calls atomic.Int32
	reader := func(string, ledger.EvidenceRef, int64) ([]byte, error) {
		current := active.Add(1)
		calls.Add(1)
		for {
			prior := maximum.Load()
			if current <= prior || maximum.CompareAndSwap(prior, current) {
				break
			}
		}
		<-release
		active.Add(-1)
		return append([]byte(nil), data...), nil
	}
	service := timelineService(t, snapshots, registration, reader)
	ids := make([]string, len(event.EvidenceRefs))
	for index, ref := range event.EvidenceRefs {
		ids[index] = evidenceIdentity(runID, evidenceItem{Event: event, EventOrdinal: 2, Ref: ref, RefOrdinal: uint64(index + 1)})
	}

	errorsOut := make(chan error, MaxConcurrentDownloads)
	for index := 0; index < MaxConcurrentDownloads; index++ {
		go func(id string) {
			_, readErr := service.ReadEvidence(context.Background(), runID, id)
			errorsOut <- readErr
		}(ids[index])
	}
	deadline := time.Now().Add(2 * time.Second)
	for active.Load() != MaxConcurrentDownloads && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if active.Load() != MaxConcurrentDownloads || maximum.Load() > MaxConcurrentDownloads {
		close(release)
		t.Fatalf("download concurrency active=%d maximum=%d", active.Load(), maximum.Load())
	}
	cancelContext, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := service.ReadEvidence(cancelContext, runID, ids[MaxConcurrentDownloads]); !errors.Is(err, context.Canceled) || !errors.Is(err, serviceapi.ErrAuthoritativeReadBusy) || got != nil {
		close(release)
		t.Fatalf("cancelled semaphore wait = %q, %v", got, err)
	}
	close(release)
	for index := 0; index < MaxConcurrentDownloads; index++ {
		if err := <-errorsOut; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != MaxConcurrentDownloads || maximum.Load() != MaxConcurrentDownloads {
		t.Fatalf("reader calls=%d maximum=%d", calls.Load(), maximum.Load())
	}
	if _, err := service.ReadEvidence(context.Background(), runID, ids[0]); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != MaxConcurrentDownloads+1 {
		t.Fatal("artifact bytes were cached across requests")
	}
}

func TestMissingArtifactAndDependencyErrorsDoNotDisclosePaths(t *testing.T) {
	runID := "missing-artifact"
	registration := timelineRegistration(runID, t.TempDir())
	ref := ledger.EvidenceRef{URI: filepath.Join(registration.CanonicalEvidenceRoot, "missing"), SHA256: strings.Repeat("a", 64), Kind: "test"}
	event := timelineEvent(runID, "event-2", 1)
	event.EvidenceRefs = []ledger.EvidenceRef{ref}
	snapshots := &snapshotFixture{snapshot: timelineSnapshot(t, runID, []ledger.Event{timelineCreated(runID, "event-1"), event})}
	service := timelineService(t, snapshots, registration, nil)
	id := evidenceIdentity(runID, evidenceItem{Event: event, EventOrdinal: 2, Ref: ref, RefOrdinal: 1})

	if got, err := service.ReadEvidence(context.Background(), runID, id); !errors.Is(err, serviceapi.ErrEvidenceIntegrityChanged) || got != nil || strings.Contains(err.Error(), registration.CanonicalEvidenceRoot) {
		t.Fatalf("missing artifact read = %q, %v", got, err)
	}
	badCatalog := catalogFixture{registration: registration, err: errors.New("cannot open " + registration.CanonicalLedgerPath)}
	bad, err := NewService(Config{Snapshots: snapshots, Catalog: badCatalog, CursorSigner: timelineSigner(t), Clock: func() time.Time { return timelineTestNow }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.ReadTimeline(context.Background(), runID, serviceapi.PageRequestV1{PageSize: 1}); !errors.Is(err, serviceapi.ErrProjectionIntegrity) || strings.Contains(err.Error(), registration.CanonicalLedgerPath) {
		t.Fatalf("dependency error disclosed a path: %v", err)
	}
}

func TestArtifactReaderFailuresMapToFrozenIntegritySeam(t *testing.T) {
	runID := "artifact-integrity-map"
	registration := timelineRegistration(runID, t.TempDir())
	ref := ledger.EvidenceRef{
		URI: filepath.Join(registration.CanonicalEvidenceRoot, "artifact"), SHA256: strings.Repeat("a", 64), Kind: "test",
	}
	event := timelineEvent(runID, "event-2", 1)
	event.EvidenceRefs = []ledger.EvidenceRef{ref}
	snapshots := &snapshotFixture{snapshot: timelineSnapshot(t, runID, []ledger.Event{timelineCreated(runID, "event-1"), event})}
	id := evidenceIdentity(runID, evidenceItem{Event: event, EventOrdinal: 2, Ref: ref, RefOrdinal: 1})
	for _, failure := range []string{"symlink", "hard-link", "special-file", "path-replacement", "digest-mismatch"} {
		t.Run(failure, func(t *testing.T) {
			service := timelineService(t, snapshots, registration, func(string, ledger.EvidenceRef, int64) ([]byte, error) {
				return []byte("must be discarded"), errors.New(failure + " at " + ref.URI)
			})
			got, err := service.ReadEvidence(context.Background(), runID, id)
			if got != nil || !errors.Is(err, serviceapi.ErrEvidenceIntegrityChanged) || strings.Contains(err.Error(), ref.URI) {
				t.Fatalf("mapped failure = %q, %v", got, err)
			}
		})
	}
}

func withEventID(item evidenceItem, value string) evidenceItem {
	item.Event.EventID = value
	return item
}

func withEventOrdinal(item evidenceItem, value uint64) evidenceItem {
	item.EventOrdinal = value
	return item
}

func withRefOrdinal(item evidenceItem, value uint64) evidenceItem {
	item.RefOrdinal = value
	return item
}

func withRefDigest(item evidenceItem, value string) evidenceItem {
	item.Ref.SHA256 = value
	return item
}

func withRefURI(item evidenceItem, value string) evidenceItem {
	item.Ref.URI = value
	return item
}

func timelineService(t *testing.T, snapshots SnapshotReader, registration runtimecatalog.RunRegistrationV1, reader ArtifactReader) *Service {
	t.Helper()
	service, err := NewService(Config{
		Snapshots: snapshots, Catalog: catalogFixture{registration: registration}, CursorSigner: timelineSigner(t),
		Clock: func() time.Time { return timelineTestNow }, ReadArtifact: reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func timelineSigner(t *testing.T) *serviceapi.CursorSigner {
	t.Helper()
	signer, err := serviceapi.NewCursorSignerWithClock("timeline-test-key", []byte("abcdef0123456789abcdef0123456789"), func() time.Time { return timelineTestNow })
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func timelineRegistration(runID, root string) runtimecatalog.RunRegistrationV1 {
	return runtimecatalog.RunRegistrationV1{
		Kind: "RunRegistrationV1", RunID: runID, CanonicalLedgerPath: filepath.Join(root, "ledger", "events.jsonl"),
		CanonicalEvidenceRoot: filepath.Join(root, "evidence"), RepositoryIdentityDigest: strings.Repeat("a", 64),
		AuthorityDigest: strings.Repeat("b", 64), InitialRegistrationTimestamp: timelineTestNow.Format(time.RFC3339Nano),
	}
}

func timelineSnapshot(t *testing.T, runID string, events []ledger.Event) readmodel.Snapshot {
	t.Helper()
	var data []byte
	var evidenceCount uint64
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
		evidenceCount += uint64(len(event.EvidenceRefs))
	}
	return readmodel.Snapshot{
		Projection: readmodel.RunProjectionV1{
			SchemaVersion: readmodel.ProjectionSchemaVersion, RunID: runID, ProjectionRevision: digest(append([]byte("revision:"), data...)),
			EventCount: uint64(len(events)), EvidenceCount: evidenceCount,
		},
		Events: events, PhysicalIdentitySHA256: digest([]byte("physical:" + runID)), SnapshotSHA256: digest(data), SnapshotLength: uint64(len(data)),
	}
}

func timelineCreated(runID, eventID string) ledger.Event {
	value := timelineEvent(runID, eventID, 0)
	value.EventType = string(domain.StateRunCreated)
	value.Payload = map[string]any{"state": string(domain.StateRunCreated)}
	return value
}

func timelineEvent(runID, eventID string, offset int) ledger.Event {
	return ledger.Event{
		SchemaVersion: ledger.CurrentSchemaVersion, EventID: eventID, Timestamp: timelineTestNow.Add(time.Duration(offset) * time.Second),
		RunID: runID, EventType: "OBSERVATION", Actor: "controller", Source: "test",
	}
}

func decodeTimelinePage(t *testing.T, raw json.RawMessage) TimelinePageV1 {
	t.Helper()
	var page TimelinePageV1
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func decodeEvidencePage(t *testing.T, raw json.RawMessage) EvidencePageV1 {
	t.Helper()
	var page EvidencePageV1
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	return page
}

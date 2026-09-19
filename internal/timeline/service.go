package timeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

func New(snapshots SnapshotReader, catalog CatalogReader, signer *serviceapi.CursorSigner) (*Service, error) {
	return NewService(Config{Snapshots: snapshots, Catalog: catalog, CursorSigner: signer})
}

func NewService(config Config) (*Service, error) {
	if config.Snapshots == nil || config.Catalog == nil || config.CursorSigner == nil || config.CursorSigner.KeyID() == "" {
		return nil, errors.New("timeline snapshots, catalog, and cursor signer are required")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.ReadArtifact == nil {
		config.ReadArtifact = evidence.ReadVerifiedLocal
	}
	return &Service{
		snapshots: config.Snapshots, catalog: config.Catalog, cursors: config.CursorSigner,
		now: config.Clock, readArtifact: config.ReadArtifact,
		downloadSlots: make(chan struct{}, MaxConcurrentDownloads),
	}, nil
}

func (s *Service) ReadTimeline(ctx context.Context, runID string, request serviceapi.PageRequestV1) (json.RawMessage, error) {
	if s == nil || ctx == nil || runtimecatalog.ValidateIdentifier(runID) != nil || request.PageSize <= 0 || request.PageSize > serviceapi.MaxTimelinePageSize {
		return nil, serviceapi.ErrInvalidCursor
	}
	payload, err := s.decodeCursor(request.Cursor, timelineFilterIdentity(runID), runID)
	if err != nil {
		return nil, err
	}
	registration, snapshot, err := s.authoritativeSnapshot(ctx, runID)
	if err != nil {
		return nil, err
	}
	position, err := verifyCursor(payload, request.Cursor != "", snapshot)
	if err != nil {
		return nil, err
	}
	if position > uint64(len(snapshot.Events)) {
		return nil, serviceapi.ErrInvalidCursor
	}
	end := boundedEnd(position, request.PageSize, len(snapshot.Events))
	page := TimelinePageV1{
		SchemaVersion: TimelinePageSchemaVersion, RunID: runID,
		ProjectionRevision: snapshot.Projection.ProjectionRevision,
		Entries:            make([]TimelineEntryV1, 0, end-position),
	}
	for index := position; index < end; index++ {
		page.Entries = append(page.Entries, timelineEntry(snapshot.Events[index], index+1, registration))
	}
	if end < uint64(len(snapshot.Events)) {
		page.NextCursor, err = s.signCursor(timelineFilterIdentity(runID), snapshot, end)
		if err != nil {
			return nil, err
		}
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		return nil, serviceapi.ErrProjectionIntegrity
	}
	return encoded, nil
}

func (s *Service) ListEvidence(ctx context.Context, runID string, request serviceapi.PageRequestV1) (json.RawMessage, error) {
	if s == nil || ctx == nil || runtimecatalog.ValidateIdentifier(runID) != nil || request.PageSize <= 0 || request.PageSize > serviceapi.MaxEvidencePageSize {
		return nil, serviceapi.ErrInvalidCursor
	}
	payload, err := s.decodeCursor(request.Cursor, evidenceFilterIdentity(runID), runID)
	if err != nil {
		return nil, err
	}
	registration, snapshot, err := s.authoritativeSnapshot(ctx, runID)
	if err != nil {
		return nil, err
	}
	position, err := verifyCursor(payload, request.Cursor != "", snapshot)
	if err != nil {
		return nil, err
	}
	items := evidenceItems(snapshot.Events)
	if position > uint64(len(items)) {
		return nil, serviceapi.ErrInvalidCursor
	}
	end := boundedEnd(position, request.PageSize, len(items))
	page := EvidencePageV1{
		SchemaVersion: EvidencePageSchemaVersion, RunID: runID,
		ProjectionRevision: snapshot.Projection.ProjectionRevision,
		Evidence:           make([]EvidenceDescriptorV1, 0, end-position),
	}
	for index := position; index < end; index++ {
		descriptor, describeErr := s.describeEvidence(ctx, registration, items[index])
		if describeErr != nil {
			return nil, describeErr
		}
		page.Evidence = append(page.Evidence, descriptor)
	}
	if end < uint64(len(items)) {
		page.NextCursor, err = s.signCursor(evidenceFilterIdentity(runID), snapshot, end)
		if err != nil {
			return nil, err
		}
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		return nil, serviceapi.ErrProjectionIntegrity
	}
	return encoded, nil
}

func (s *Service) ReadEvidence(ctx context.Context, runID, evidenceID string) ([]byte, error) {
	if s == nil || ctx == nil || runtimecatalog.ValidateIdentifier(runID) != nil || !validEvidenceID(evidenceID) {
		return nil, serviceapi.ErrDependencyNotFound
	}
	if err := s.acquireDownload(ctx); err != nil {
		return nil, err
	}
	defer s.releaseDownload()

	registration, snapshot, err := s.authoritativeSnapshot(ctx, runID)
	if err != nil {
		return nil, err
	}
	var selected *evidenceItem
	for _, item := range evidenceItems(snapshot.Events) {
		if evidenceIdentity(runID, item) != evidenceID {
			continue
		}
		if selected != nil {
			return nil, serviceapi.ErrEvidenceIntegrityChanged
		}
		candidate := item
		selected = &candidate
	}
	if selected == nil || !downloadEligible(registration.CanonicalEvidenceRoot, selected.Ref) {
		return nil, serviceapi.ErrDependencyNotFound
	}
	data, readErr := s.readArtifact(registration.CanonicalEvidenceRoot, selected.Ref, int64(serviceapi.MaxEvidenceDownloadSize))
	if readErr != nil || len(data) > serviceapi.MaxEvidenceDownloadSize || digest(data) != selected.Ref.SHA256 {
		return nil, serviceapi.ErrEvidenceIntegrityChanged
	}
	return data, nil
}

func (s *Service) authoritativeSnapshot(ctx context.Context, runID string) (runtimecatalog.RunRegistrationV1, readmodel.Snapshot, error) {
	registration, err := s.catalog.ReadRun(runID)
	if err != nil {
		return runtimecatalog.RunRegistrationV1{}, readmodel.Snapshot{}, safeDependencyError(err)
	}
	if registration.RunID != runID || !canonicalAbsolute(registration.CanonicalLedgerPath) || !canonicalAbsolute(registration.CanonicalEvidenceRoot) {
		return runtimecatalog.RunRegistrationV1{}, readmodel.Snapshot{}, serviceapi.ErrProjectionIntegrity
	}
	snapshot, err := s.snapshots.Snapshot(ctx, runID)
	if err != nil {
		return runtimecatalog.RunRegistrationV1{}, readmodel.Snapshot{}, safeDependencyError(err)
	}
	if !validSnapshot(snapshot, runID) {
		return runtimecatalog.RunRegistrationV1{}, readmodel.Snapshot{}, serviceapi.ErrProjectionIntegrity
	}
	return registration, snapshot, nil
}

func validSnapshot(snapshot readmodel.Snapshot, runID string) bool {
	if snapshot.Projection.SchemaVersion != readmodel.ProjectionSchemaVersion || snapshot.Projection.RunID != runID ||
		!validDigest(snapshot.Projection.ProjectionRevision) || !validDigest(snapshot.PhysicalIdentitySHA256) ||
		!validDigest(snapshot.SnapshotSHA256) || snapshot.SnapshotLength == 0 ||
		snapshot.Projection.EventCount != uint64(len(snapshot.Events)) || len(snapshot.Events) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(snapshot.Events))
	var evidenceCount uint64
	for _, event := range snapshot.Events {
		if event.SchemaVersion != ledger.CurrentSchemaVersion || event.RunID != runID || event.Validate() != nil {
			return false
		}
		if _, duplicate := seen[event.EventID]; duplicate {
			return false
		}
		seen[event.EventID] = struct{}{}
		evidenceCount += uint64(len(event.EvidenceRefs))
	}
	return snapshot.Projection.EvidenceCount == evidenceCount
}

func safeDependencyError(err error) error {
	switch {
	case errors.Is(err, serviceapi.ErrDependencyNotFound):
		return serviceapi.ErrDependencyNotFound
	case errors.Is(err, context.Canceled):
		return errors.Join(serviceapi.ErrAuthoritativeReadBusy, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return errors.Join(serviceapi.ErrAuthoritativeReadBusy, context.DeadlineExceeded)
	case errors.Is(err, serviceapi.ErrAuthoritativeReadBusy), errors.Is(err, runtimecatalog.ErrBusy):
		return serviceapi.ErrAuthoritativeReadBusy
	case errors.Is(err, serviceapi.ErrProjectionLineageChanged):
		return serviceapi.ErrProjectionLineageChanged
	case errors.Is(err, serviceapi.ErrCursorEpochChanged):
		return serviceapi.ErrCursorEpochChanged
	case errors.Is(err, serviceapi.ErrInvalidCursor):
		return serviceapi.ErrInvalidCursor
	default:
		return serviceapi.ErrProjectionIntegrity
	}
}

func boundedEnd(position uint64, pageSize, length int) uint64 {
	end := position + uint64(pageSize)
	if end > uint64(length) {
		return uint64(length)
	}
	return end
}

func timelineEntry(event ledger.Event, ordinal uint64, registration runtimecatalog.RunRegistrationV1) TimelineEntryV1 {
	entry := TimelineEntryV1{
		Ordinal: ordinal, EventID: safeExternal(event.EventID, registration),
		Timestamp: event.Timestamp.UTC().Format(time.RFC3339Nano),
		ProjectID: safeExternal(event.ProjectID, registration), PlanID: safeExternal(event.PlanID, registration),
		RunID: safeExternal(event.RunID, registration), AttemptID: safeExternal(event.AttemptID, registration),
		TaskID: safeExternal(event.TaskID, registration), AgentSessionID: safeExternal(event.AgentSessionID, registration),
		CorrelationID: safeExternal(event.CorrelationID, registration), EventType: safeExternal(event.EventType, registration),
		StateFrom: string(event.StateFrom), StateTo: string(event.StateTo), Actor: safeExternal(event.Actor, registration),
		Source: safeExternal(event.Source, registration), EvidenceCount: uint64(len(event.EvidenceRefs)),
		EvidenceIDs: make([]string, 0, len(event.EvidenceRefs)),
	}
	for index, ref := range event.EvidenceRefs {
		entry.EvidenceIDs = append(entry.EvidenceIDs, evidenceIdentity(event.RunID, evidenceItem{
			Event: event, EventOrdinal: ordinal, Ref: ref, RefOrdinal: uint64(index + 1),
		}))
	}
	return entry
}

func safeExternal(value string, registration runtimecatalog.RunRegistrationV1) string {
	if value == "" {
		return ""
	}
	for _, private := range []string{registration.CanonicalLedgerPath, registration.CanonicalEvidenceRoot} {
		if private != "" && strings.Contains(value, private) {
			return "[redacted]"
		}
	}
	lower := strings.ToLower(value)
	if filepath.IsAbs(value) || strings.HasPrefix(value, `\\`) || strings.ContainsAny(value, `/\\`) || strings.Contains(lower, "://") || hasURIScheme(value) || windowsAbsolute(value) || strings.ContainsRune(value, 0) {
		return "[redacted]"
	}
	return value
}

func hasURIScheme(value string) bool {
	colon := strings.IndexByte(value, ':')
	if colon < 1 {
		return false
	}
	for index := 0; index < colon; index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(index > 0 && ((character >= '0' && character <= '9') || character == '+' || character == '-' || character == '.')) {
			continue
		}
		return false
	}
	return true
}

func windowsAbsolute(value string) bool {
	return len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) &&
		value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}

func canonicalAbsolute(value string) bool {
	return value != "" && !strings.ContainsRune(value, 0) && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func digest(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

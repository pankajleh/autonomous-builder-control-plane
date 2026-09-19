package readmodel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

func New(catalog CatalogReader, signer *serviceapi.CursorSigner) (*Service, error) {
	return NewService(Config{Catalog: catalog, CursorSigner: signer})
}

func NewService(config Config) (*Service, error) {
	if config.Catalog == nil || config.CursorSigner == nil || config.CursorSigner.KeyID() == "" {
		return nil, errors.New("read-model catalog and cursor signer are required")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.OpenReadOnlyLedger == nil {
		config.OpenReadOnlyLedger = func(path string, generation ledger.PhysicalGeneration) (ExistingReadOnlyLedger, error) {
			if !generation.Valid() {
				return ledger.OpenExistingReadOnlyJSONLLedger(path)
			}
			return ledger.OpenRegisteredReadOnlyJSONLLedger(path, generation)
		}
	}
	return &Service{
		catalog: config.Catalog, cursors: config.CursorSigner, now: config.Clock,
		openReadOnly: config.OpenReadOnlyLedger,
		ledgerSlots:  make(chan struct{}, MaxOpenLedgerObjects), snapshotSlots: make(chan struct{}, MaxConcurrentSnapshots),
	}, nil
}

func (s *Service) Snapshot(ctx context.Context, runID string) (Snapshot, error) {
	data, physicalIdentity, registration, err := s.snapshotImage(ctx, runID)
	if err != nil {
		return Snapshot{}, err
	}
	return buildSnapshot(data, physicalIdentity, registration)
}

func (s *Service) snapshotImage(ctx context.Context, runID string) ([]byte, string, runtimecatalog.RunRegistrationV1, error) {
	if s == nil || s.catalog == nil || s.openReadOnly == nil || ctx == nil || runtimecatalog.ValidateIdentifier(runID) != nil {
		return nil, "", runtimecatalog.RunRegistrationV1{}, integrityError()
	}
	registration, err := s.catalog.ReadRun(runID)
	if err != nil {
		return nil, "", runtimecatalog.RunRegistrationV1{}, err
	}
	if registration.RunID != runID {
		return nil, "", runtimecatalog.RunRegistrationV1{}, integrityError()
	}
	generation, err := registeredLedgerGeneration(registration)
	if err != nil {
		return nil, "", runtimecatalog.RunRegistrationV1{}, integrityError()
	}
	if err := acquire(ctx, s.ledgerSlots); err != nil {
		return nil, "", runtimecatalog.RunRegistrationV1{}, err
	}
	defer release(s.ledgerSlots)
	if err := acquire(ctx, s.snapshotSlots); err != nil {
		return nil, "", runtimecatalog.RunRegistrationV1{}, err
	}
	defer release(s.snapshotSlots)
	reader, err := s.openReadOnly(registration.CanonicalLedgerPath, generation)
	if err != nil {
		return nil, "", runtimecatalog.RunRegistrationV1{}, mapLedgerError(err)
	}
	data, physicalIdentity, snapshotErr := reader.Snapshot()
	closeErr := reader.Close()
	if snapshotErr != nil {
		return nil, "", runtimecatalog.RunRegistrationV1{}, mapLedgerError(snapshotErr)
	}
	if closeErr != nil {
		return nil, "", runtimecatalog.RunRegistrationV1{}, integrityError()
	}
	if err := ctx.Err(); err != nil {
		return nil, "", runtimecatalog.RunRegistrationV1{}, errors.Join(serviceapi.ErrAuthoritativeReadBusy, err)
	}
	return data, physicalIdentity, registration, nil
}

func (s *Service) ReadRunProjection(ctx context.Context, runID string) (json.RawMessage, error) {
	snapshot, err := s.Snapshot(ctx, runID)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(snapshot.Projection)
	if err != nil {
		return nil, integrityError()
	}
	return encoded, nil
}

func (s *Service) ReadEventProjection(ctx context.Context, runID string, request serviceapi.PageRequestV1) (json.RawMessage, error) {
	if request.PageSize <= 0 || request.PageSize > serviceapi.MaxEventsPageSize {
		return nil, serviceapi.ErrInvalidCursor
	}
	var cursorPayload LedgerCursorPayloadV1
	var err error
	if request.Cursor != "" {
		cursorPayload, err = s.decodeLedgerCursor(request.Cursor, runID)
		if err != nil {
			return nil, err
		}
	}
	data, physicalIdentity, registration, err := s.snapshotImage(ctx, runID)
	if err != nil {
		if request.Cursor != "" && errors.Is(err, ledger.ErrRegisteredGenerationChanged) {
			return nil, serviceapi.ErrProjectionLineageChanged
		}
		return nil, err
	}
	position := uint64(0)
	if request.Cursor != "" {
		position, err = verifyRawLedgerLineage(cursorPayload, data, physicalIdentity)
		if err != nil {
			return nil, err
		}
	}
	snapshot, err := buildSnapshot(data, physicalIdentity, registration)
	if err != nil {
		return nil, err
	}
	if position > uint64(len(snapshot.records)) {
		return nil, serviceapi.ErrProjectionLineageChanged
	}
	end := position + uint64(request.PageSize)
	if end > uint64(len(snapshot.records)) {
		end = uint64(len(snapshot.records))
	}
	page := EventProjectionPageV1{
		SchemaVersion: EventPageSchemaVersion, RunID: runID,
		ProjectionRevision: snapshot.Projection.ProjectionRevision,
		Events:             make([]ProjectedEventV1, 0, end-position),
	}
	for index := position; index < end; index++ {
		page.Events = append(page.Events, projectedEvent(snapshot.records[index], index+1, snapshot.registration))
	}
	if end < uint64(len(snapshot.records)) {
		page.NextCursor, err = s.signLedgerCursor(snapshot, runID, end)
		if err != nil {
			return nil, err
		}
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		return nil, integrityError()
	}
	return encoded, nil
}

// WithExistingWritableLedger shares the service-wide eight-object ceiling
// with readers and resolves the path only from immutable catalog authority.
// Its coordinated opener routes every writable-ledger snapshot through the
// same four-snapshot ceiling as ordinary read requests without limiting
// non-snapshot writer operations to four.
func (s *Service) WithExistingWritableLedger(ctx context.Context, runID string, operation func(*ledger.JSONLLedger) error) error {
	if s == nil || ctx == nil || operation == nil || runtimecatalog.ValidateIdentifier(runID) != nil {
		return integrityError()
	}
	registration, err := s.catalog.ReadRun(runID)
	if err != nil {
		return err
	}
	if registration.RunID != runID {
		return integrityError()
	}
	generation, err := registeredLedgerGeneration(registration)
	if err != nil {
		return integrityError()
	}
	if err := acquire(ctx, s.ledgerSlots); err != nil {
		return err
	}
	defer release(s.ledgerSlots)
	coordinator := func(snapshot func() error) error {
		if err := acquire(ctx, s.snapshotSlots); err != nil {
			return err
		}
		defer release(s.snapshotSlots)
		return snapshot()
	}
	var value *ledger.JSONLLedger
	if generation.Valid() {
		value, err = ledger.OpenRegisteredJSONLLedgerWithSnapshotCoordinator(registration.CanonicalLedgerPath, generation, coordinator)
	} else {
		value, err = ledger.OpenExistingJSONLLedgerWithSnapshotCoordinator(registration.CanonicalLedgerPath, coordinator)
	}
	if err != nil {
		return mapLedgerError(err)
	}
	operationErr := operation(value)
	closeErr := value.Close()
	if operationErr != nil {
		return operationErr
	}
	if closeErr != nil {
		return integrityError()
	}
	return nil
}

func acquire(ctx context.Context, semaphore chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(serviceapi.ErrAuthoritativeReadBusy, err)
	}
	select {
	case semaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return errors.Join(serviceapi.ErrAuthoritativeReadBusy, ctx.Err())
	}
}

func release(semaphore chan struct{}) { <-semaphore }

func registeredLedgerGeneration(registration runtimecatalog.RunRegistrationV1) (ledger.PhysicalGeneration, error) {
	authority := registration.LedgerGeneration
	if authority == (runtimecatalog.LedgerGenerationV1{}) {
		// Compatibility for in-memory pre-correction CatalogReader fixtures.
		// Persisted runtime-catalog records reject an absent generation while
		// decoding, so production service opens never take this branch.
		return ledger.PhysicalGeneration{}, nil
	}
	generation := ledger.PhysicalGeneration{
		ParentDevice: authority.ParentDevice, ParentInode: authority.ParentInode,
		FileDevice: authority.FileDevice, FileInode: authority.FileInode,
	}
	if !authority.Valid() || !generation.Valid() {
		return ledger.PhysicalGeneration{}, errors.New("registered ledger generation is incomplete")
	}
	return generation, nil
}

func mapLedgerError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "ledger file lock is busy") {
		return errors.Join(serviceapi.ErrAuthoritativeReadBusy, err)
	}
	if errors.Is(err, ledger.ErrRegisteredGenerationChanged) {
		return errors.Join(integrityError(), ledger.ErrRegisteredGenerationChanged)
	}
	return integrityError()
}

func projectedEvent(value record, ordinal uint64, registration runtimecatalog.RunRegistrationV1) ProjectedEventV1 {
	event := value.event
	result := ProjectedEventV1{
		Ordinal: ordinal, LineSHA256: value.lineSHA256,
		LedgerSchemaVersion: event.SchemaVersion,
		EventID:             safeExternal(event.EventID, registration), Timestamp: event.Timestamp.UTC().Format(time.RFC3339Nano),
		ProjectID: safeExternal(event.ProjectID, registration), PlanID: safeExternal(event.PlanID, registration),
		RunID: safeExternal(event.RunID, registration), AttemptID: safeExternal(event.AttemptID, registration),
		TaskID: safeExternal(event.TaskID, registration), AgentSessionID: safeExternal(event.AgentSessionID, registration),
		CorrelationID: safeExternal(event.CorrelationID, registration), EventType: safeExternal(event.EventType, registration),
		StateFrom: string(event.StateFrom), StateTo: string(event.StateTo),
		Actor: safeExternal(event.Actor, registration), Source: safeExternal(event.Source, registration),
		EvidenceCount: uint64(len(event.EvidenceRefs)), EvidenceRefs: make([]ProjectedEvidenceRefV1, 0, len(event.EvidenceRefs)),
	}
	for index, ref := range event.EvidenceRefs {
		result.EvidenceRefs = append(result.EvidenceRefs, ProjectedEvidenceRefV1{
			Ordinal: uint64(index + 1), SHA256: safeExternal(ref.SHA256, registration), Kind: safeExternal(ref.Kind, registration),
		})
	}
	return result
}

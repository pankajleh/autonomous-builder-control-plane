package readmodel

import (
	"bytes"
	"errors"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

func LedgerFilterIdentity(runID string) string {
	return "events-v1:run=" + runID
}

func (s *Service) decodeLedgerCursor(token, runID string) (LedgerCursorPayloadV1, error) {
	var payload LedgerCursorPayloadV1
	if err := s.cursors.Verify(token, serviceapi.CursorKindLedger, LedgerFilterIdentity(runID), &payload); err != nil {
		return LedgerCursorPayloadV1{}, err
	}
	if payload.SchemaVersion != LedgerCursorSchemaV1 || payload.RunID != runID ||
		!validDigest(payload.PhysicalIdentitySHA256) || !validDigest(payload.PriorSnapshotSHA256) ||
		!validDigest(payload.PriorLastLineSHA256) || !validDigest(payload.PriorLastEventIDSHA256) ||
		payload.PriorSnapshotLength == 0 || payload.PriorLastOrdinal == 0 ||
		payload.PagePosition > payload.PriorLastOrdinal {
		return LedgerCursorPayloadV1{}, serviceapi.ErrInvalidCursor
	}
	return payload, nil
}

func verifyRawLedgerLineage(payload LedgerCursorPayloadV1, data []byte, physicalIdentity string) (uint64, error) {
	if payload.PhysicalIdentitySHA256 != digest([]byte(physicalIdentity)) ||
		payload.PriorSnapshotLength > uint64(len(data)) {
		return 0, serviceapi.ErrProjectionLineageChanged
	}
	prefix := data[:payload.PriorSnapshotLength]
	if len(prefix) == 0 || prefix[len(prefix)-1] != '\n' ||
		digest(prefix) != payload.PriorSnapshotSHA256 {
		return 0, serviceapi.ErrProjectionLineageChanged
	}
	lines := bytes.Split(prefix[:len(prefix)-1], []byte{'\n'})
	if uint64(len(lines)) != payload.PriorLastOrdinal || len(lines) == 0 {
		return 0, serviceapi.ErrProjectionLineageChanged
	}
	lastLine := lines[len(lines)-1]
	var lastEvent ledger.Event
	if digest(lastLine) != payload.PriorLastLineSHA256 || decodeStrictJSON(lastLine, &lastEvent) != nil ||
		digest([]byte(lastEvent.EventID)) != payload.PriorLastEventIDSHA256 {
		return 0, serviceapi.ErrProjectionLineageChanged
	}
	return payload.PagePosition, nil
}

func (s *Service) signLedgerCursor(snapshot Snapshot, runID string, position uint64) (string, error) {
	if len(snapshot.records) == 0 || position > uint64(len(snapshot.records)) {
		return "", serviceapi.ErrInvalidCursor
	}
	last := snapshot.records[len(snapshot.records)-1]
	payload := LedgerCursorPayloadV1{
		SchemaVersion: LedgerCursorSchemaV1, RunID: runID,
		PhysicalIdentitySHA256: snapshot.PhysicalIdentitySHA256,
		PriorSnapshotSHA256:    snapshot.SnapshotSHA256, PriorSnapshotLength: snapshot.SnapshotLength,
		PriorLastOrdinal: uint64(len(snapshot.records)), PriorLastEventIDSHA256: digest([]byte(last.event.EventID)),
		PriorLastLineSHA256: last.lineSHA256, PagePosition: position,
	}
	issued := s.now().UTC()
	token, err := s.cursors.Sign(serviceapi.CursorKindLedger, LedgerFilterIdentity(runID), payload, issued, issued.Add(serviceapi.MaxCursorLifetime))
	if err != nil {
		return "", errors.Join(serviceapi.ErrInvalidCursor, err)
	}
	return token, nil
}

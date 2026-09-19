package timeline

import (
	"encoding/json"
	"errors"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

func timelineFilterIdentity(runID string) string { return "timeline-v1:run=" + runID }
func evidenceFilterIdentity(runID string) string { return "evidence-v1:run=" + runID }

func (s *Service) decodeCursor(token, filterIdentity, runID string) (readmodel.LedgerCursorPayloadV1, error) {
	if token == "" {
		return readmodel.LedgerCursorPayloadV1{}, nil
	}
	var payload readmodel.LedgerCursorPayloadV1
	if err := s.cursors.Verify(token, serviceapi.CursorKindLedger, filterIdentity, &payload); err != nil {
		return readmodel.LedgerCursorPayloadV1{}, safeDependencyError(err)
	}
	if payload.SchemaVersion != readmodel.LedgerCursorSchemaV1 || payload.RunID != runID ||
		!validDigest(payload.PhysicalIdentitySHA256) || !validDigest(payload.PriorSnapshotSHA256) ||
		!validDigest(payload.PriorLastEventIDSHA256) || !validDigest(payload.PriorLastLineSHA256) ||
		payload.PriorSnapshotLength == 0 || payload.PriorLastOrdinal == 0 {
		return readmodel.LedgerCursorPayloadV1{}, serviceapi.ErrInvalidCursor
	}
	return payload, nil
}

func verifyCursor(payload readmodel.LedgerCursorPayloadV1, presented bool, snapshot readmodel.Snapshot) (uint64, error) {
	if !presented {
		return 0, nil
	}
	last := snapshot.Events[len(snapshot.Events)-1]
	line, err := json.Marshal(last)
	if err != nil {
		return 0, serviceapi.ErrProjectionIntegrity
	}
	// Track B does not export the prior raw ledger prefix needed to prove an
	// append byte-for-byte. Track C therefore accepts only the exact bound
	// snapshot and fails closed on all changes, including valid appends.
	if payload.PhysicalIdentitySHA256 != snapshot.PhysicalIdentitySHA256 ||
		payload.PriorSnapshotSHA256 != snapshot.SnapshotSHA256 ||
		payload.PriorSnapshotLength != snapshot.SnapshotLength ||
		payload.PriorLastOrdinal != uint64(len(snapshot.Events)) ||
		payload.PriorLastEventIDSHA256 != digest([]byte(last.EventID)) ||
		payload.PriorLastLineSHA256 != digest(line) {
		return 0, serviceapi.ErrProjectionLineageChanged
	}
	return payload.PagePosition, nil
}

func (s *Service) signCursor(filterIdentity string, snapshot readmodel.Snapshot, position uint64) (string, error) {
	if len(snapshot.Events) == 0 {
		return "", serviceapi.ErrInvalidCursor
	}
	last := snapshot.Events[len(snapshot.Events)-1]
	line, err := json.Marshal(last)
	if err != nil {
		return "", serviceapi.ErrProjectionIntegrity
	}
	payload := readmodel.LedgerCursorPayloadV1{
		SchemaVersion: readmodel.LedgerCursorSchemaV1, RunID: snapshot.Projection.RunID,
		PhysicalIdentitySHA256: snapshot.PhysicalIdentitySHA256,
		PriorSnapshotSHA256:    snapshot.SnapshotSHA256, PriorSnapshotLength: snapshot.SnapshotLength,
		PriorLastOrdinal: uint64(len(snapshot.Events)), PriorLastEventIDSHA256: digest([]byte(last.EventID)),
		PriorLastLineSHA256: digest(line), PagePosition: position,
	}
	issued := s.now().UTC()
	token, err := s.cursors.Sign(serviceapi.CursorKindLedger, filterIdentity, payload, issued, issued.Add(serviceapi.MaxCursorLifetime))
	if err != nil {
		return "", errors.Join(serviceapi.ErrInvalidCursor, err)
	}
	return token, nil
}

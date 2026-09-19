package timeline

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type evidenceItem struct {
	Event        ledger.Event
	EventOrdinal uint64
	Ref          ledger.EvidenceRef
	RefOrdinal   uint64
}

func evidenceItems(events []ledger.Event) []evidenceItem {
	count := 0
	for _, event := range events {
		count += len(event.EvidenceRefs)
	}
	items := make([]evidenceItem, 0, count)
	for eventIndex, event := range events {
		for refIndex, ref := range event.EvidenceRefs {
			items = append(items, evidenceItem{
				Event: event, EventOrdinal: uint64(eventIndex + 1), Ref: ref, RefOrdinal: uint64(refIndex + 1),
			})
		}
	}
	return items
}

func evidenceIdentity(runID string, item evidenceItem) string {
	refBytes, _ := json.Marshal(struct {
		URI    string `json:"uri"`
		SHA256 string `json:"sha256"`
		Kind   string `json:"kind"`
	}{item.Ref.URI, item.Ref.SHA256, item.Ref.Kind})
	identity, _ := json.Marshal(struct {
		SchemaVersion      string `json:"schema_version"`
		RunID              string `json:"run_id"`
		SourceEventID      string `json:"source_event_id"`
		SourceEventOrdinal uint64 `json:"source_event_ordinal"`
		EvidenceRefOrdinal uint64 `json:"evidence_ref_ordinal"`
		EvidenceRefSHA256  string `json:"evidence_ref_sha256"`
	}{"EvidenceIdentityV1", runID, item.Event.EventID, item.EventOrdinal, item.RefOrdinal, digest(refBytes)})
	return digest(identity)
}

func validEvidenceID(value string) bool { return validDigest(value) }

func (s *Service) describeEvidence(ctx context.Context, registration runtimecatalog.RunRegistrationV1, item evidenceItem) (EvidenceDescriptorV1, error) {
	descriptor := EvidenceDescriptorV1{
		EvidenceID: evidenceIdentity(registration.RunID, item),
		Kind:       safeExternal(item.Ref.Kind, registration),
		SourceEvent: EventIdentityV1{
			Ordinal: item.EventOrdinal, EventID: safeExternal(item.Event.EventID, registration),
			Timestamp: item.Event.Timestamp.UTC().Format(time.RFC3339Nano),
		},
	}
	if validDigest(item.Ref.SHA256) {
		descriptor.SHA256 = item.Ref.SHA256
	}
	if !downloadEligible(registration.CanonicalEvidenceRoot, item.Ref) {
		return descriptor, nil
	}
	if err := s.acquireDownload(ctx); err != nil {
		return EvidenceDescriptorV1{}, err
	}
	data, err := s.readArtifact(registration.CanonicalEvidenceRoot, item.Ref, int64(serviceapi.MaxEvidenceDownloadSize))
	s.releaseDownload()
	if err != nil || len(data) > serviceapi.MaxEvidenceDownloadSize || digest(data) != item.Ref.SHA256 {
		return descriptor, nil
	}
	size := int64(len(data))
	descriptor.ByteSize = &size
	descriptor.Downloadable = true
	return descriptor, nil
}

func downloadEligible(root string, ref ledger.EvidenceRef) bool {
	if !canonicalAbsolute(root) || !canonicalAbsolute(ref.URI) || ref.URI == root || ref.Kind == "" || !validDigest(ref.SHA256) {
		return false
	}
	relative, err := filepath.Rel(root, ref.URI)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func (s *Service) acquireDownload(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(serviceapi.ErrAuthoritativeReadBusy, err)
	}
	select {
	case s.downloadSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return errors.Join(serviceapi.ErrAuthoritativeReadBusy, ctx.Err())
	}
}

func (s *Service) releaseDownload() { <-s.downloadSlots }

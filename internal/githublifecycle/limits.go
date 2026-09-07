package githublifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// Limits bounds every provider-facing collection and field. A Limits value is
// immutable because it contains scalars only.
type Limits struct {
	MaxPages            int
	MaxItemsPerPage     int
	MaxTotalItems       int
	MaxTextBytes        int
	MaxEvidenceRefs     int
	MaxMetadataItems    int
	MaxParents          int
	MaxLineageEntries   int
	CallTimeout         time.Duration
	MaxReadRetries      int
	MaxWriteRetries     int
	MaxAmbiguousRetries int
}

func DefaultLimits() Limits {
	return Limits{
		MaxPages: 10, MaxItemsPerPage: 100, MaxTotalItems: 500,
		MaxTextBytes: 4096, MaxEvidenceRefs: 64, MaxMetadataItems: 32,
		MaxParents: 16, MaxLineageEntries: 256, CallTimeout: 30 * time.Second,
		MaxReadRetries: 2, MaxWriteRetries: 1, MaxAmbiguousRetries: 0,
	}
}

func (l Limits) Validate() error {
	if l.MaxPages <= 0 || l.MaxItemsPerPage <= 0 || l.MaxTotalItems <= 0 ||
		l.MaxTextBytes <= 0 || l.MaxEvidenceRefs <= 0 || l.MaxMetadataItems <= 0 ||
		l.MaxParents <= 0 || l.MaxLineageEntries <= 0 || l.CallTimeout <= 0 ||
		l.MaxReadRetries < 0 || l.MaxWriteRetries < 0 || l.MaxAmbiguousRetries != 0 {
		return errors.New("resource limits must be positive and ambiguous-write retries must be zero")
	}
	if l.MaxItemsPerPage > l.MaxTotalItems {
		return errors.New("items per page cannot exceed total items")
	}
	return nil
}

// CanonicalJSON returns the stable, versioned representation of every field
// governed by Limits. Durations are represented as nanoseconds so the policy
// identity does not depend on display formatting.
func (l Limits) CanonicalJSON() ([]byte, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		SchemaVersion       int   `json:"schema_version"`
		MaxPages            int   `json:"max_pages"`
		MaxItemsPerPage     int   `json:"max_items_per_page"`
		MaxTotalItems       int   `json:"max_total_items"`
		MaxTextBytes        int   `json:"max_text_bytes"`
		MaxEvidenceRefs     int   `json:"max_evidence_refs"`
		MaxMetadataItems    int   `json:"max_metadata_items"`
		MaxParents          int   `json:"max_parents"`
		MaxLineageEntries   int   `json:"max_lineage_entries"`
		CallTimeoutNanos    int64 `json:"call_timeout_nanos"`
		MaxReadRetries      int   `json:"max_read_retries"`
		MaxWriteRetries     int   `json:"max_write_retries"`
		MaxAmbiguousRetries int   `json:"max_ambiguous_retries"`
	}{1, l.MaxPages, l.MaxItemsPerPage, l.MaxTotalItems, l.MaxTextBytes, l.MaxEvidenceRefs,
		l.MaxMetadataItems, l.MaxParents, l.MaxLineageEntries, int64(l.CallTimeout),
		l.MaxReadRetries, l.MaxWriteRetries, l.MaxAmbiguousRetries})
}

// SHA256 is the deterministic identity of the complete validated policy.
func (l Limits) SHA256() (string, error) {
	data, err := l.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func requireLimitsSHA(expected Limits, actual string) error {
	digest, err := expected.SHA256()
	if err != nil {
		return err
	}
	if actual != digest {
		return errors.New("resource limits policy identity does not match controller policy")
	}
	return nil
}

func validatePage(l Limits, page, itemCount, totalCount int) error {
	if err := l.Validate(); err != nil {
		return err
	}
	if page <= 0 || page > l.MaxPages || itemCount < 0 || itemCount > l.MaxItemsPerPage || totalCount < itemCount || totalCount > l.MaxTotalItems {
		return errors.New("remote pagination exceeds governed limits")
	}
	return nil
}

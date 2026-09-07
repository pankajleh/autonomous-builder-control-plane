package githublifecycle

import (
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

func validatePage(l Limits, page, itemCount, totalCount int) error {
	if err := l.Validate(); err != nil {
		return err
	}
	if page <= 0 || page > l.MaxPages || itemCount < 0 || itemCount > l.MaxItemsPerPage || totalCount < itemCount || totalCount > l.MaxTotalItems {
		return errors.New("remote pagination exceeds governed limits")
	}
	return nil
}

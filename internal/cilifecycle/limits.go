package cilifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

const (
	SchemaVersionV1 = 1

	MaxSuites                = 256
	MaxRuns                  = 256
	MaxStatuses              = 256
	ItemsPerPage             = 64
	MaxSuitePages            = 4
	MaxRunPages              = 4
	MaxStatusPages           = 5
	MaxCollectionRequests    = 30
	MaxResponseBodyBytes     = 2 << 20
	MaxResponseHeaderBytes   = 32 << 10
	MaxLinkBytes             = 8 << 10
	MaxRequestIDBytes        = 256
	MaxTextBytes             = 256
	MaxStateBytes            = 64
	MaxTimestampBytes        = 30
	MaxProvenanceBytes       = 8 << 10
	MaxNonSweepBundleBytes   = 320 << 10
	MaxBundleBytes           = 8 << 20
	MaxSuiteObjectBytes      = 1846
	MaxRunObjectBytes        = 3431
	MaxStatusObjectBytes     = 9728
	MaxSemanticSweepBytes    = 3842529
	MaxDerivedBundleProfile  = 8012738
	MaxLedgerEventBytes      = 64 << 10
	MaxEvidenceURIBytes      = 1024
	MaxReservationBytes      = 8 << 10
	MaxCompletedAttemptBytes = 9 << 20
	MaxAttemptsPerRun        = 16
	MaxAttemptsGlobal        = 64
	MaxReservedEvidenceBytes = 576 << 20
	MaxLedgerScanBytes       = 64 << 20
	MaxLedgerLineBytes       = 256 << 10
	MaxLedgerLines           = 262144
	MaxAttemptIDBytes        = 128
)

const (
	CallTimeout         = 10 * time.Second
	CollectionTimeout   = 6 * time.Minute
	MaxTransportRetries = 0
)

// Limits is an immutable controller policy. Collect never accepts one from a
// caller; the unexported constructor permits component-wise stricter tests.
type Limits struct {
	maxSuites, maxRuns, maxStatuses               int
	itemsPerPage, maxSuitePages, maxRunPages      int
	maxStatusPages, maxCollectionRequests         int
	maxResponseBodyBytes, maxResponseHeaderBytes  int
	maxLinkBytes, maxRequestIDBytes, maxTextBytes int
	maxStateBytes, maxTimestampBytes              int
	maxProvenanceBytes, maxNonSweepBytes          int
	maxBundleBytes                                int
	callTimeout, collectionTimeout                time.Duration
	maxTransportRetries                           int
}

func productionLimits() Limits {
	return Limits{
		MaxSuites, MaxRuns, MaxStatuses, ItemsPerPage, MaxSuitePages,
		MaxRunPages, MaxStatusPages, MaxCollectionRequests,
		MaxResponseBodyBytes, MaxResponseHeaderBytes, MaxLinkBytes,
		MaxRequestIDBytes, MaxTextBytes, MaxStateBytes, MaxTimestampBytes,
		MaxProvenanceBytes, MaxNonSweepBundleBytes, MaxBundleBytes,
		CallTimeout, CollectionTimeout, MaxTransportRetries,
	}
}

func (l Limits) validate() error {
	positive := []int{l.maxSuites, l.maxRuns, l.maxStatuses, l.itemsPerPage,
		l.maxSuitePages, l.maxRunPages, l.maxStatusPages, l.maxCollectionRequests,
		l.maxResponseBodyBytes, l.maxResponseHeaderBytes, l.maxLinkBytes,
		l.maxRequestIDBytes, l.maxTextBytes, l.maxStateBytes, l.maxTimestampBytes,
		l.maxProvenanceBytes, l.maxNonSweepBytes, l.maxBundleBytes}
	for _, value := range positive {
		if value <= 0 {
			return errors.New("CI collection limits must be positive")
		}
	}
	if l.itemsPerPage > l.maxSuites || l.itemsPerPage > l.maxRuns || l.itemsPerPage > l.maxStatuses ||
		l.maxSuitePages*l.itemsPerPage < l.maxSuites || l.maxRunPages*l.itemsPerPage < l.maxRuns ||
		(l.maxStatusPages-1)*l.itemsPerPage < l.maxStatuses || l.callTimeout <= 0 ||
		l.collectionTimeout < l.callTimeout || l.maxTransportRetries != 0 {
		return errors.New("CI collection limits are internally inconsistent")
	}
	return nil
}

type limitsWireV1 struct {
	SchemaVersion            int   `json:"schema_version"`
	MaxSuites                int   `json:"max_suites"`
	MaxRuns                  int   `json:"max_runs"`
	MaxStatuses              int   `json:"max_statuses"`
	ItemsPerPage             int   `json:"items_per_page"`
	MaxSuitePages            int   `json:"max_suite_pages"`
	MaxRunPages              int   `json:"max_run_pages"`
	MaxStatusPages           int   `json:"max_status_pages"`
	MaxCollectionRequests    int   `json:"max_collection_requests"`
	MaxResponseBodyBytes     int   `json:"max_response_body_bytes"`
	MaxResponseHeaderBytes   int   `json:"max_response_header_bytes"`
	MaxLinkBytes             int   `json:"max_link_bytes"`
	MaxRequestIDBytes        int   `json:"max_request_id_bytes"`
	MaxTextBytes             int   `json:"max_text_bytes"`
	MaxStateBytes            int   `json:"max_state_bytes"`
	MaxTimestampBytes        int   `json:"max_timestamp_bytes"`
	MaxProvenanceBytes       int   `json:"max_provenance_bytes"`
	MaxNonSweepBytes         int   `json:"max_non_sweep_bundle_bytes"`
	MaxBundleBytes           int   `json:"max_bundle_bytes"`
	MaxSuiteObjectBytes      int   `json:"max_suite_object_bytes"`
	MaxRunObjectBytes        int   `json:"max_run_object_bytes"`
	MaxStatusObjectBytes     int   `json:"max_status_object_bytes"`
	MaxSemanticSweepBytes    int   `json:"max_semantic_sweep_bytes"`
	MaxDerivedBundleBytes    int   `json:"max_derived_bundle_profile_bytes"`
	MaxLedgerEventBytes      int   `json:"max_ledger_event_bytes"`
	MaxEvidenceURIBytes      int   `json:"max_evidence_uri_bytes"`
	MaxReservationBytes      int   `json:"max_reservation_bytes"`
	MaxCompletedAttemptBytes int   `json:"max_completed_attempt_bytes"`
	MaxAttemptsPerRun        int   `json:"max_attempts_per_run"`
	MaxAttemptsGlobal        int   `json:"max_attempts_global"`
	MaxReservedEvidenceBytes int   `json:"max_reserved_evidence_bytes"`
	MaxLedgerScanBytes       int   `json:"max_ledger_scan_bytes"`
	MaxLedgerLineBytes       int   `json:"max_ledger_line_bytes"`
	MaxLedgerLines           int   `json:"max_ledger_lines"`
	MaxAttemptIDBytes        int   `json:"max_attempt_id_bytes"`
	CallTimeoutNanos         int64 `json:"call_timeout_nanos"`
	CollectionTimeoutNanos   int64 `json:"collection_timeout_nanos"`
	MaxTransportRetries      int   `json:"max_transport_retries"`
}

func (l Limits) wire() limitsWireV1 {
	return limitsWireV1{SchemaVersionV1, l.maxSuites, l.maxRuns, l.maxStatuses,
		l.itemsPerPage, l.maxSuitePages, l.maxRunPages, l.maxStatusPages,
		l.maxCollectionRequests, l.maxResponseBodyBytes, l.maxResponseHeaderBytes,
		l.maxLinkBytes, l.maxRequestIDBytes, l.maxTextBytes, l.maxStateBytes,
		l.maxTimestampBytes, l.maxProvenanceBytes, l.maxNonSweepBytes,
		l.maxBundleBytes, MaxSuiteObjectBytes, MaxRunObjectBytes, MaxStatusObjectBytes,
		MaxSemanticSweepBytes, MaxDerivedBundleProfile, MaxLedgerEventBytes,
		MaxEvidenceURIBytes, MaxReservationBytes, MaxCompletedAttemptBytes,
		MaxAttemptsPerRun, MaxAttemptsGlobal, MaxReservedEvidenceBytes,
		MaxLedgerScanBytes, MaxLedgerLineBytes, MaxLedgerLines, MaxAttemptIDBytes,
		int64(l.callTimeout), int64(l.collectionTimeout),
		l.maxTransportRetries}
}

func (l Limits) CanonicalJSON() ([]byte, error) {
	if err := l.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(l.wire())
}

func (l Limits) SHA256() (string, error) {
	b, err := l.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

// ProductionLimitsSHA256 identifies every controller-owned production bound.
func ProductionLimitsSHA256() string {
	digest, err := productionLimits().SHA256()
	if err != nil {
		panic(err)
	}
	return digest
}

// ProductionLimitsCanonicalJSON returns a defensive copy of the complete
// versioned production policy used to bind evidence.
func ProductionLimitsCanonicalJSON() []byte {
	b, err := productionLimits().CanonicalJSON()
	if err != nil {
		panic(err)
	}
	return b
}

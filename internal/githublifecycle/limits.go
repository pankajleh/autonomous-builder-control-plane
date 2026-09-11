package githublifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// Limits is the canonical identity of the stage-specific production budgets
// consumed by the lifecycle contracts. Fields are deliberately not shared by
// unrelated stages even when their production-v1 values happen to be equal.
// A Limits value is immutable because it contains scalars only.
type Limits struct {
	// Deprecated compatibility mirrors for the pre-M-01 prlifecycle adapter.
	// They are not governing fields and Validate requires exact agreement with
	// their stage-specific replacements.
	MaxPages          int
	MaxItemsPerPage   int
	MaxTotalItems     int
	MaxParents        int
	MaxLineageEntries int

	MaxRequiredTrustedChecks               int
	MaxEligibleReviewers                   int
	MaxRequiredReviewers                   int
	MaxMinimumApprovals                    int
	MaxObservedChecks                      int
	MaxObservedReviews                     int
	MaxPaginationPages                     int
	MaxPaginationItemsPerPage              int
	MaxObservedItemsPerPaginationSource    int
	RequiredPaginationSources              int
	MaxPaginationClosureBytes              int
	MaxCumulativePaginationClosureBytes    int
	MaxTextBytes                           int
	MaxEvidenceRefs                        int
	MaxMetadataItems                       int
	MaxReadyEvidenceClosureRefs            int
	MaxReadyAcceptedSources                int
	MaxReadyEvidenceArtifactBytes          int
	MaxReadyEvidenceClosureBytes           int
	MaxContractParents                     int
	MaxContractLineageEntries              int
	RequiredProductionMergeParents         int
	MaxProductionMergeLineageEntries       int
	MaxDescendantDistance                  int
	MaxCanonicalObjectBytes                int
	MaxCancellationAuthorityBytes          int
	MaxLedgerLineBytes                     int
	MaxReadyLedgerSnapshotBytes            int
	MaxLedgerScanRecords                   int
	MaxRequestBodyBytes                    int
	MaxResponseHeaderBytes                 int
	MaxLinkHeaderBytes                     int
	MaxRequestIDBytes                      int
	MaxCompressedResponseBodyBytes         int
	MaxDecompressedResponseBodyBytes       int
	MaxTargetResponseEnvelopeBytes         int
	MaxPreSubmitHTTPCalls                  int
	MaxCommitObjectCreationSubmissions     int
	MaxTargetRefUpdateSubmissions          int
	MaxPostMergeHTTPCalls                  int
	MaxReconciliationRounds                int
	MaxReconciliationCallsPerRound         int
	MaxPrincipalValidationCalls            int
	MaxHTTPCalls                           int
	MaxCumulativeRequestBytes              int64
	MaxCumulativeResponseHeaderBytes       int64
	MaxCumulativeCompressedResponseBytes   int64
	MaxCumulativeDecompressedResponseBytes int64
	CallTimeout                            time.Duration
	MaxCumulativeActiveProviderCallTime    time.Duration
	MaxControllerInvocationTime            time.Duration
	MaxReadRetries                         int
	MaxWriteRetries                        int
	MaxAmbiguousRetries                    int
}

func DefaultLimits() Limits {
	limits := Limits{
		MaxPages: 10, MaxItemsPerPage: 100, MaxTotalItems: 500, MaxParents: 16, MaxLineageEntries: 256,
		MaxRequiredTrustedChecks: 64, MaxEligibleReviewers: 64, MaxRequiredReviewers: 64, MaxMinimumApprovals: 64,
		MaxObservedChecks: 500, MaxObservedReviews: 500,
		MaxPaginationPages: 10, MaxPaginationItemsPerPage: 100, MaxObservedItemsPerPaginationSource: 500,
		RequiredPaginationSources: 3, MaxPaginationClosureBytes: 1 * 1024 * 1024, MaxCumulativePaginationClosureBytes: 3 * 1024 * 1024,
		MaxTextBytes: 4096, MaxEvidenceRefs: 64, MaxMetadataItems: 32, MaxReadyEvidenceClosureRefs: 256, MaxReadyAcceptedSources: 500,
		MaxReadyEvidenceArtifactBytes: 16 * 1024 * 1024, MaxReadyEvidenceClosureBytes: 64 * 1024 * 1024,
		MaxContractParents: 16, MaxContractLineageEntries: 256, RequiredProductionMergeParents: 2, MaxProductionMergeLineageEntries: 0,
		MaxDescendantDistance: 500, MaxCanonicalObjectBytes: 256 * 1024, MaxCancellationAuthorityBytes: 256 * 1024,
		MaxLedgerLineBytes: 256 * 1024, MaxReadyLedgerSnapshotBytes: 64 * 1024 * 1024, MaxLedgerScanRecords: 262144,
		MaxRequestBodyBytes: 16 * 1024, MaxResponseHeaderBytes: 32 * 1024, MaxLinkHeaderBytes: 8 * 1024, MaxRequestIDBytes: 256,
		MaxCompressedResponseBodyBytes: 4 * 1024 * 1024, MaxDecompressedResponseBodyBytes: 4 * 1024 * 1024,
		MaxPreSubmitHTTPCalls: 28, MaxCommitObjectCreationSubmissions: 1, MaxTargetRefUpdateSubmissions: 1,
		MaxPostMergeHTTPCalls: 8, MaxReconciliationRounds: 8, MaxReconciliationCallsPerRound: 3,
		MaxPrincipalValidationCalls: 2, MaxHTTPCalls: 64,
		MaxCumulativeRequestBytes: 1 * 1024 * 1024, MaxCumulativeResponseHeaderBytes: 2 * 1024 * 1024,
		MaxCumulativeCompressedResponseBytes: 256 * 1024 * 1024, MaxCumulativeDecompressedResponseBytes: 256 * 1024 * 1024,
		CallTimeout: 30 * time.Second, MaxCumulativeActiveProviderCallTime: 10 * time.Minute, MaxControllerInvocationTime: 15 * time.Minute,
		MaxReadRetries: 2, MaxWriteRetries: 1, MaxAmbiguousRetries: 0,
	}
	limits.MaxTargetResponseEnvelopeBytes = maxTargetResponseEnvelopeBytesV1(limits)
	return limits
}

func (l Limits) Validate() error {
	if l.MaxPages != l.MaxPaginationPages || l.MaxItemsPerPage != l.MaxPaginationItemsPerPage ||
		l.MaxTotalItems != l.MaxObservedItemsPerPaginationSource || l.MaxParents != l.MaxContractParents ||
		l.MaxLineageEntries != l.MaxContractLineageEntries {
		return errors.New("deprecated resource-limit mirrors disagree with stage-specific limits")
	}
	if l.MaxRequiredTrustedChecks <= 0 || l.MaxEligibleReviewers <= 0 || l.MaxRequiredReviewers <= 0 || l.MaxMinimumApprovals <= 0 ||
		l.MaxObservedChecks <= 0 || l.MaxObservedReviews <= 0 || l.MaxPaginationPages <= 0 || l.MaxPaginationItemsPerPage <= 0 ||
		l.MaxObservedItemsPerPaginationSource <= 0 || l.RequiredPaginationSources <= 0 || l.MaxPaginationClosureBytes <= 0 ||
		l.MaxCumulativePaginationClosureBytes <= 0 || l.MaxTextBytes <= 0 || l.MaxEvidenceRefs <= 0 || l.MaxMetadataItems <= 0 ||
		l.MaxReadyEvidenceClosureRefs <= 0 || l.MaxReadyAcceptedSources <= 0 || l.MaxReadyEvidenceArtifactBytes <= 0 || l.MaxReadyEvidenceClosureBytes <= 0 ||
		l.MaxContractParents <= 0 || l.MaxContractLineageEntries <= 0 || l.RequiredProductionMergeParents <= 0 ||
		l.MaxProductionMergeLineageEntries < 0 || l.MaxDescendantDistance <= 0 || l.MaxCanonicalObjectBytes <= 0 ||
		l.MaxCancellationAuthorityBytes <= 0 || l.MaxLedgerLineBytes <= 0 || l.MaxReadyLedgerSnapshotBytes <= 0 ||
		l.MaxLedgerScanRecords <= 0 || l.MaxRequestBodyBytes <= 0 || l.MaxResponseHeaderBytes <= 0 || l.MaxLinkHeaderBytes <= 0 ||
		l.MaxRequestIDBytes <= 0 || l.MaxCompressedResponseBodyBytes <= 0 || l.MaxDecompressedResponseBodyBytes <= 0 ||
		l.MaxTargetResponseEnvelopeBytes <= 0 ||
		l.MaxPreSubmitHTTPCalls <= 0 || l.MaxCommitObjectCreationSubmissions <= 0 || l.MaxTargetRefUpdateSubmissions <= 0 ||
		l.MaxPostMergeHTTPCalls <= 0 || l.MaxReconciliationRounds <= 0 || l.MaxReconciliationCallsPerRound <= 0 ||
		l.MaxPrincipalValidationCalls <= 0 || l.MaxHTTPCalls <= 0 || l.MaxCumulativeRequestBytes <= 0 ||
		l.MaxCumulativeResponseHeaderBytes <= 0 || l.MaxCumulativeCompressedResponseBytes <= 0 ||
		l.MaxCumulativeDecompressedResponseBytes <= 0 || l.CallTimeout <= 0 || l.MaxCumulativeActiveProviderCallTime <= 0 ||
		l.MaxControllerInvocationTime <= 0 || l.MaxReadRetries < 0 || l.MaxWriteRetries < 0 || l.MaxAmbiguousRetries != 0 {
		return errors.New("resource limits must be positive and ambiguous-write retries must be zero")
	}
	if l.MaxRequiredReviewers > l.MaxEligibleReviewers || l.MaxMinimumApprovals > l.MaxEligibleReviewers {
		return errors.New("required reviewers and minimum approvals cannot exceed eligible reviewers")
	}
	if l.MaxPaginationItemsPerPage > l.MaxObservedItemsPerPaginationSource {
		return errors.New("pagination items per page cannot exceed observed items per source")
	}
	if l.MaxPaginationClosureBytes > l.MaxCumulativePaginationClosureBytes {
		return errors.New("one pagination closure cannot exceed the cumulative closure budget")
	}
	if l.MaxReadyEvidenceArtifactBytes > l.MaxReadyEvidenceClosureBytes || l.MaxLedgerLineBytes > l.MaxReadyLedgerSnapshotBytes {
		return errors.New("one READY artifact or ledger line cannot exceed its cumulative snapshot budget")
	}
	if maxCanonicalCheckRecordsBytesV1(l) == 0 {
		return errors.New("canonical check-record byte bound overflows the platform integer range")
	}
	if l.MaxTargetResponseEnvelopeBytes <= l.MaxDecompressedResponseBodyBytes {
		return errors.New("target response envelope must have an independent bound above the response-body bound")
	}
	return nil
}

const (
	canonicalJSONMaxByteExpansionV1 = 6 // '<', '>', and '&' become one six-byte \u00xx escape.
	stableIdentityNodeIDBytesV1     = 256

	// These skeletons preserve the exact checkWire and ledger.EvidenceRef field
	// order. Variable string contents and evidence array members are empty so
	// the independently governed maxima can be added without allocating a
	// potentially very large maximum record.
	maxCheckWireSkeletonV1     = `{"node_id":"","name":"","identity":{"context":"","source":"commit_status","producer":{"database_id":9223372036854775807,"node_id":""},"app":{"database_id":9223372036854775807,"node_id":""}},"status":"completed","conclusion":"cancelled","head_sha":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","evidence_refs":[]}`
	maxCheckEvidenceSkeletonV1 = `{"uri":"","sha256":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","kind":""}`
)

// maxCanonicalCheckRecordsBytesV1 derives a safe canonical byte ceiling for
// the decoded checkWire array. It composes the existing check-count, text,
// stable-identity, Git-object-ID, state/conclusion, and per-check evidence
// limits; pagination-closure bytes are deliberately absent. Zero means the
// configured limits cannot be represented by the platform int used by byte
// slices.
func maxCanonicalCheckRecordsBytesV1(l Limits) int {
	escapedText := checkedLimitProduct(l.MaxTextBytes, canonicalJSONMaxByteExpansionV1)
	escapedStableNode := checkedLimitProduct(stableIdentityNodeIDBytesV1, canonicalJSONMaxByteExpansionV1)
	if escapedText == 0 || escapedStableNode == 0 {
		return 0
	}
	evidenceBytes := checkedLimitSum(len(maxCheckEvidenceSkeletonV1), checkedLimitProduct(2, escapedText))
	if evidenceBytes == 0 {
		return 0
	}
	evidenceArrayBytes := checkedCanonicalArrayBytes(l.MaxEvidenceRefs, evidenceBytes)
	if evidenceArrayBytes == 0 {
		return 0
	}
	// The skeleton already contains the empty evidence array, so only its
	// members and separating commas are added here.
	evidenceMembersBytes := evidenceArrayBytes - len("[]")
	checkBytes := checkedLimitSum(
		len(maxCheckWireSkeletonV1),
		checkedLimitProduct(3, escapedText), // node_id, name, and identity.context
		checkedLimitProduct(2, escapedStableNode),
		evidenceMembersBytes,
	)
	if checkBytes == 0 {
		return 0
	}
	return checkedCanonicalArrayBytes(l.MaxObservedChecks, checkBytes)
}

func checkedCanonicalArrayBytes(count, maxItemBytes int) int {
	if count < 0 || maxItemBytes <= 0 {
		return 0
	}
	if count == 0 {
		return len("[]")
	}
	members := checkedLimitProduct(count, checkedLimitSum(maxItemBytes, 1))
	if members == 0 {
		return 0
	}
	return checkedLimitSum(1, members)
}

func checkedLimitProduct(left, right int) int {
	maxInt := int(^uint(0) >> 1)
	if left <= 0 || right <= 0 || left > maxInt/right {
		return 0
	}
	return left * right
}

func checkedLimitSum(values ...int) int {
	maxInt := int(^uint(0) >> 1)
	total := 0
	for _, value := range values {
		if value < 0 || total > maxInt-value {
			return 0
		}
		total += value
	}
	return total
}

// CanonicalJSON returns the stable, versioned representation of every field
// governed by Limits. Durations are represented as nanoseconds so the policy
// identity does not depend on display formatting.
func (l Limits) CanonicalJSON() ([]byte, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		SchemaVersion                            int   `json:"schema_version"`
		MaxRequiredTrustedChecks                 int   `json:"max_required_trusted_checks"`
		MaxEligibleReviewers                     int   `json:"max_eligible_reviewers"`
		MaxRequiredReviewers                     int   `json:"max_required_reviewers"`
		MaxMinimumApprovals                      int   `json:"max_minimum_approvals"`
		MaxObservedChecks                        int   `json:"max_observed_checks"`
		MaxObservedReviews                       int   `json:"max_observed_reviews"`
		MaxPaginationPages                       int   `json:"max_pagination_pages"`
		MaxPaginationItemsPerPage                int   `json:"max_pagination_items_per_page"`
		MaxObservedItemsPerPaginationSource      int   `json:"max_observed_items_per_pagination_source"`
		RequiredPaginationSources                int   `json:"required_pagination_sources"`
		MaxPaginationClosureBytes                int   `json:"max_pagination_closure_bytes"`
		MaxCumulativePaginationClosureBytes      int   `json:"max_cumulative_pagination_closure_bytes"`
		MaxTextBytes                             int   `json:"max_text_bytes"`
		MaxEvidenceRefs                          int   `json:"max_evidence_refs"`
		MaxMetadataItems                         int   `json:"max_metadata_items"`
		MaxReadyEvidenceClosureRefs              int   `json:"max_ready_evidence_closure_refs"`
		MaxReadyAcceptedSources                  int   `json:"max_ready_accepted_sources"`
		MaxReadyEvidenceArtifactBytes            int   `json:"max_ready_evidence_artifact_bytes"`
		MaxReadyEvidenceClosureBytes             int   `json:"max_ready_evidence_closure_bytes"`
		MaxContractParents                       int   `json:"max_contract_parents"`
		MaxContractLineageEntries                int   `json:"max_contract_lineage_entries"`
		RequiredProductionMergeParents           int   `json:"required_production_merge_parents"`
		MaxProductionMergeLineageEntries         int   `json:"max_production_merge_lineage_entries"`
		MaxDescendantDistance                    int   `json:"max_descendant_distance"`
		MaxCanonicalObjectBytes                  int   `json:"max_canonical_object_bytes"`
		MaxCancellationAuthorityBytes            int   `json:"max_cancellation_authority_bytes"`
		MaxLedgerLineBytes                       int   `json:"max_ledger_line_bytes"`
		MaxReadyLedgerSnapshotBytes              int   `json:"max_ready_ledger_snapshot_bytes"`
		MaxLedgerScanRecords                     int   `json:"max_ledger_scan_records"`
		MaxRequestBodyBytes                      int   `json:"max_request_body_bytes"`
		MaxResponseHeaderBytes                   int   `json:"max_response_header_bytes"`
		MaxLinkHeaderBytes                       int   `json:"max_link_header_bytes"`
		MaxRequestIDBytes                        int   `json:"max_request_id_bytes"`
		MaxCompressedResponseBodyBytes           int   `json:"max_compressed_response_body_bytes"`
		MaxDecompressedResponseBodyBytes         int   `json:"max_decompressed_response_body_bytes"`
		MaxTargetResponseEnvelopeBytes           int   `json:"max_target_response_envelope_bytes"`
		MaxPreSubmitHTTPCalls                    int   `json:"max_pre_submit_http_calls"`
		MaxCommitObjectCreationSubmissions       int   `json:"max_commit_object_creation_submissions"`
		MaxTargetRefUpdateSubmissions            int   `json:"max_target_ref_update_submissions"`
		MaxPostMergeHTTPCalls                    int   `json:"max_post_merge_http_calls"`
		MaxReconciliationRounds                  int   `json:"max_reconciliation_rounds"`
		MaxReconciliationCallsPerRound           int   `json:"max_reconciliation_calls_per_round"`
		MaxPrincipalValidationCalls              int   `json:"max_principal_validation_calls"`
		MaxHTTPCalls                             int   `json:"max_http_calls"`
		MaxCumulativeRequestBytes                int64 `json:"max_cumulative_request_bytes"`
		MaxCumulativeResponseHeaderBytes         int64 `json:"max_cumulative_response_header_bytes"`
		MaxCumulativeCompressedResponseBytes     int64 `json:"max_cumulative_compressed_response_bytes"`
		MaxCumulativeDecompressedResponseBytes   int64 `json:"max_cumulative_decompressed_response_bytes"`
		CallTimeoutNanos                         int64 `json:"call_timeout_nanos"`
		MaxCumulativeActiveProviderCallTimeNanos int64 `json:"max_cumulative_active_provider_call_time_nanos"`
		MaxControllerInvocationTimeNanos         int64 `json:"max_controller_invocation_time_nanos"`
		MaxReadRetries                           int   `json:"max_read_retries"`
		MaxWriteRetries                          int   `json:"max_write_retries"`
		MaxAmbiguousRetries                      int   `json:"max_ambiguous_retries"`
	}{
		3, l.MaxRequiredTrustedChecks, l.MaxEligibleReviewers, l.MaxRequiredReviewers, l.MaxMinimumApprovals,
		l.MaxObservedChecks, l.MaxObservedReviews, l.MaxPaginationPages, l.MaxPaginationItemsPerPage,
		l.MaxObservedItemsPerPaginationSource, l.RequiredPaginationSources, l.MaxPaginationClosureBytes,
		l.MaxCumulativePaginationClosureBytes, l.MaxTextBytes, l.MaxEvidenceRefs, l.MaxMetadataItems,
		l.MaxReadyEvidenceClosureRefs, l.MaxReadyAcceptedSources, l.MaxReadyEvidenceArtifactBytes, l.MaxReadyEvidenceClosureBytes,
		l.MaxContractParents, l.MaxContractLineageEntries, l.RequiredProductionMergeParents,
		l.MaxProductionMergeLineageEntries, l.MaxDescendantDistance, l.MaxCanonicalObjectBytes,
		l.MaxCancellationAuthorityBytes, l.MaxLedgerLineBytes, l.MaxReadyLedgerSnapshotBytes, l.MaxLedgerScanRecords,
		l.MaxRequestBodyBytes, l.MaxResponseHeaderBytes, l.MaxLinkHeaderBytes, l.MaxRequestIDBytes,
		l.MaxCompressedResponseBodyBytes, l.MaxDecompressedResponseBodyBytes, l.MaxTargetResponseEnvelopeBytes, l.MaxPreSubmitHTTPCalls,
		l.MaxCommitObjectCreationSubmissions, l.MaxTargetRefUpdateSubmissions, l.MaxPostMergeHTTPCalls,
		l.MaxReconciliationRounds, l.MaxReconciliationCallsPerRound, l.MaxPrincipalValidationCalls, l.MaxHTTPCalls,
		l.MaxCumulativeRequestBytes, l.MaxCumulativeResponseHeaderBytes, l.MaxCumulativeCompressedResponseBytes,
		l.MaxCumulativeDecompressedResponseBytes, int64(l.CallTimeout), int64(l.MaxCumulativeActiveProviderCallTime),
		int64(l.MaxControllerInvocationTime), l.MaxReadRetries, l.MaxWriteRetries, l.MaxAmbiguousRetries,
	})
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
	if page <= 0 || page > l.MaxPaginationPages || itemCount < 0 || itemCount > l.MaxPaginationItemsPerPage ||
		totalCount < itemCount || totalCount > l.MaxObservedItemsPerPaginationSource {
		return errors.New("remote pagination exceeds governed limits")
	}
	return nil
}

func requireCanonicalObjectSize(data []byte, limit int, object string) error {
	if len(data) == 0 || len(data) > limit {
		return errors.New(object + " canonical object exceeds its governed byte limit")
	}
	return nil
}

const (
	retainedCanonicalRecordSchemaV1 = "retained-canonical-record-reference-v1"
	retainedCanonicalInlineBytesV1  = 32 * 1024
)

// CanonicalRecordSetV1 is a network-free recovery input for content-addressed
// records retained outside a bounded canonical container. It deliberately has
// no filesystem, provider, or callback behavior; Task 2 owns durable storage.
type CanonicalRecordSetV1 struct {
	records map[string][]byte
}

// NewCanonicalRecordSetV1 copies exact retained bytes into an immutable,
// content-addressed in-memory set suitable for strict contract parsing.
func NewCanonicalRecordSetV1(records ...[]byte) (CanonicalRecordSetV1, error) {
	set := CanonicalRecordSetV1{records: make(map[string][]byte, len(records))}
	for _, record := range records {
		if len(record) == 0 {
			return CanonicalRecordSetV1{}, errors.New("retained canonical record is empty")
		}
		reference := retainedCanonicalReference(record)
		if existing, ok := set.records[reference]; ok && !bytes.Equal(existing, record) {
			return CanonicalRecordSetV1{}, errors.New("retained canonical record reference is ambiguous")
		}
		set.records[reference] = append([]byte(nil), record...)
	}
	return set, nil
}

type retainedCanonicalRecordV1 struct {
	Schema         string `json:"schema"`
	Kind           string `json:"kind"`
	Reference      string `json:"reference"`
	SHA256         string `json:"sha256"`
	CanonicalBytes int    `json:"canonical_bytes"`
	InlineBytes    []byte `json:"inline_bytes,omitempty"`
}

func retainedCanonicalReference(record []byte) string {
	return "sha256:" + digestBytes(record)
}

func newRetainedCanonicalRecordV1(kind string, record []byte, maxBytes int, limits Limits) (retainedCanonicalRecordV1, error) {
	if !validOpaqueID(kind, 256) || len(record) == 0 || maxBytes <= 0 || len(record) > maxBytes {
		return retainedCanonicalRecordV1{}, errors.New("retained canonical record identity or stage bound is invalid")
	}
	return makeRetainedCanonicalRecordV1(kind, record), nil
}

func makeRetainedCanonicalRecordV1(kind string, record []byte) retainedCanonicalRecordV1 {
	digest := digestBytes(record)
	value := retainedCanonicalRecordV1{
		Schema: retainedCanonicalRecordSchemaV1, Kind: kind, Reference: "sha256:" + digest,
		SHA256: digest, CanonicalBytes: len(record),
	}
	if len(record) <= retainedCanonicalInlineBytesV1 {
		value.InlineBytes = append([]byte(nil), record...)
	}
	return value
}

func resolveRetainedCanonicalRecordV1(binding retainedCanonicalRecordV1, expectedKind string, maxBytes int, limits Limits, sets []CanonicalRecordSetV1) ([]byte, error) {
	if binding.Schema != retainedCanonicalRecordSchemaV1 || binding.Kind != expectedKind ||
		!validSHA256(binding.SHA256) || binding.Reference != "sha256:"+binding.SHA256 ||
		binding.CanonicalBytes <= 0 || maxBytes <= 0 || binding.CanonicalBytes > maxBytes {
		return nil, errors.New("retained canonical record reference identity is invalid")
	}
	inline := retainedCanonicalInlineBytesV1
	if len(binding.InlineBytes) > 0 {
		if binding.CanonicalBytes > inline || len(binding.InlineBytes) != binding.CanonicalBytes ||
			digestBytes(binding.InlineBytes) != binding.SHA256 {
			return nil, errors.New("inline retained canonical record disagrees with its reference")
		}
		return binding.InlineBytes, nil
	}
	if binding.CanonicalBytes <= inline || len(sets) != 1 || sets[0].records == nil {
		return nil, errors.New("referenced canonical record is missing")
	}
	record, ok := sets[0].records[binding.Reference]
	if !ok {
		return nil, errors.New("referenced canonical record is missing")
	}
	if len(record) != binding.CanonicalBytes || digestBytes(record) != binding.SHA256 {
		return nil, errors.New("referenced canonical record is substituted or digest-mismatched")
	}
	return record, nil
}

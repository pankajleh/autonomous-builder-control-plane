package cilifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
)

const (
	SemanticSweepKindV1  = "ci_semantic_sweep_v1"
	EvidenceBundleKindV1 = "ci_evidence_bundle_v1"
)

type CollectionOutcome string

const (
	OutcomeStable               CollectionOutcome = "STABLE"
	OutcomeUnstable             CollectionOutcome = "UNSTABLE"
	OutcomeStaleHead            CollectionOutcome = "STALE_HEAD"
	OutcomeTruncated            CollectionOutcome = "TRUNCATED"
	OutcomeMalformed            CollectionOutcome = "MALFORMED"
	OutcomeProviderUnavailable  CollectionOutcome = "PROVIDER_UNAVAILABLE"
	OutcomeUnsupportedPrincipal CollectionOutcome = "UNSUPPORTED_PRINCIPAL"
	OutcomeIntegrityFailure     CollectionOutcome = "INTEGRITY_FAILURE"
)

func (o CollectionOutcome) Valid() bool {
	switch o {
	case OutcomeStable, OutcomeUnstable, OutcomeStaleHead, OutcomeTruncated,
		OutcomeMalformed, OutcomeProviderUnavailable, OutcomeUnsupportedPrincipal,
		OutcomeIntegrityFailure:
		return true
	default:
		return false
	}
}

// CollectionError reports a durably completed non-STABLE evidence outcome.
// Cause is never included in canonical evidence and must not contain provider
// response bodies or secret-bearing transport strings.
type CollectionError struct {
	Outcome     CollectionOutcome
	FailureCode string
	FailedPhase string
	FailedPage  int
	Cause       error
}

func (e *CollectionError) Error() string {
	if e == nil {
		return ""
	}
	if e.FailureCode == "" {
		return string(e.Outcome)
	}
	return string(e.Outcome) + ": " + e.FailureCode
}

func (e *CollectionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type CheckSuiteObservationV1Input struct {
	ProviderID     int64
	ProviderNodeID string
	AppID          int64
	HeadSHA        string
	Status         string
	Conclusion     *string
	CreatedAt      string
	UpdatedAt      string
}

type CheckSuiteObservationV1 struct {
	value immutableV1[CheckSuiteObservationV1Input]
}

type checkSuiteWireV1 struct {
	ProviderID     int64   `json:"provider_id"`
	ProviderNodeID string  `json:"provider_node_id"`
	AppID          int64   `json:"app_id"`
	HeadSHA        string  `json:"head_sha"`
	Status         string  `json:"status"`
	Conclusion     *string `json:"conclusion"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

func NewCheckSuiteObservationV1(in CheckSuiteObservationV1Input) (CheckSuiteObservationV1, error) {
	in.Conclusion = cloneString(in.Conclusion)
	if in.ProviderID <= 0 || in.AppID <= 0 || !validOptionalText(in.ProviderNodeID, MaxTextBytes) ||
		!validGitSHA(in.HeadSHA) || !suiteStatus[in.Status] || !validTimestamp(in.CreatedAt) || !validTimestamp(in.UpdatedAt) {
		return CheckSuiteObservationV1{}, errors.New("check suite observation contains invalid provider data")
	}
	if err := validateConclusion(in.Status, in.Conclusion, suiteConclusion); err != nil {
		return CheckSuiteObservationV1{}, err
	}
	if timestampAfter(in.CreatedAt, in.UpdatedAt) {
		return CheckSuiteObservationV1{}, errors.New("check suite update precedes creation")
	}
	v, err := newImmutable(in, checkSuiteWire(in))
	result := CheckSuiteObservationV1{v}
	if err == nil && len(result.CanonicalJSON()) > MaxSuiteObjectBytes {
		return CheckSuiteObservationV1{}, errors.New("check suite observation exceeds encoded size limit")
	}
	return result, err
}

func checkSuiteWire(in CheckSuiteObservationV1Input) checkSuiteWireV1 {
	return checkSuiteWireV1{in.ProviderID, in.ProviderNodeID, in.AppID, in.HeadSHA, in.Status, cloneString(in.Conclusion), in.CreatedAt, in.UpdatedAt}
}
func (o CheckSuiteObservationV1) Input() CheckSuiteObservationV1Input {
	in := o.value.data
	in.Conclusion = cloneString(in.Conclusion)
	return in
}
func (o CheckSuiteObservationV1) CanonicalJSON() []byte        { return o.value.bytes() }
func (o CheckSuiteObservationV1) SHA256() string               { return o.value.digest }
func (o CheckSuiteObservationV1) MarshalJSON() ([]byte, error) { return o.value.marshal() }

type CheckRunObservationV1Input struct {
	ProviderID     int64
	ProviderNodeID string
	CheckSuiteID   int64
	AppID          int64
	Name           string
	HeadSHA        string
	Status         string
	Conclusion     *string
	StartedAt      *string
	CompletedAt    *string
}
type CheckRunObservationV1 struct {
	value immutableV1[CheckRunObservationV1Input]
}
type checkRunWireV1 struct {
	ProviderID     int64   `json:"provider_id"`
	ProviderNodeID string  `json:"provider_node_id"`
	CheckSuiteID   int64   `json:"check_suite_id"`
	AppID          int64   `json:"app_id"`
	Name           string  `json:"name"`
	HeadSHA        string  `json:"head_sha"`
	Status         string  `json:"status"`
	Conclusion     *string `json:"conclusion"`
	StartedAt      *string `json:"started_at"`
	CompletedAt    *string `json:"completed_at"`
}

func NewCheckRunObservationV1(in CheckRunObservationV1Input) (CheckRunObservationV1, error) {
	in.Conclusion, in.StartedAt, in.CompletedAt = cloneString(in.Conclusion), cloneString(in.StartedAt), cloneString(in.CompletedAt)
	if in.ProviderID <= 0 || in.CheckSuiteID <= 0 || in.AppID <= 0 || !validOptionalText(in.ProviderNodeID, MaxTextBytes) ||
		!validText(in.Name, MaxTextBytes) || !validGitSHA(in.HeadSHA) || !runStatus[in.Status] ||
		!validOptionalTimestamp(in.StartedAt) || !validOptionalTimestamp(in.CompletedAt) {
		return CheckRunObservationV1{}, errors.New("check run observation contains invalid provider data")
	}
	if err := validateConclusion(in.Status, in.Conclusion, runConclusion); err != nil {
		return CheckRunObservationV1{}, err
	}
	if in.Status == "completed" && in.CompletedAt == nil {
		return CheckRunObservationV1{}, errors.New("completed check run requires completed_at")
	}
	if in.Status != "completed" && in.CompletedAt != nil {
		return CheckRunObservationV1{}, errors.New("incomplete check run cannot have completed_at")
	}
	if in.StartedAt != nil && in.CompletedAt != nil && timestampAfter(*in.StartedAt, *in.CompletedAt) {
		return CheckRunObservationV1{}, errors.New("check run completion precedes start")
	}
	v, err := newImmutable(in, checkRunWire(in))
	result := CheckRunObservationV1{v}
	if err == nil && len(result.CanonicalJSON()) > MaxRunObjectBytes {
		return CheckRunObservationV1{}, errors.New("check run observation exceeds encoded size limit")
	}
	return result, err
}
func checkRunWire(in CheckRunObservationV1Input) checkRunWireV1 {
	return checkRunWireV1{in.ProviderID, in.ProviderNodeID, in.CheckSuiteID, in.AppID, in.Name, in.HeadSHA, in.Status, cloneString(in.Conclusion), cloneString(in.StartedAt), cloneString(in.CompletedAt)}
}
func (o CheckRunObservationV1) Input() CheckRunObservationV1Input {
	in := o.value.data
	in.Conclusion, in.StartedAt, in.CompletedAt = cloneString(in.Conclusion), cloneString(in.StartedAt), cloneString(in.CompletedAt)
	return in
}
func (o CheckRunObservationV1) CanonicalJSON() []byte        { return o.value.bytes() }
func (o CheckRunObservationV1) SHA256() string               { return o.value.digest }
func (o CheckRunObservationV1) MarshalJSON() ([]byte, error) { return o.value.marshal() }

type CommitStatusObservationV1Input struct {
	ProviderID     int64
	ProviderNodeID string
	State          string
	Context        string
	Description    *string
	HeadSHA        string
	CreatedAt      string
	UpdatedAt      string
	CreatorID      int64
	CreatorNodeID  string
	CreatorLogin   string
	CreatorType    string
}
type CommitStatusObservationV1 struct {
	value immutableV1[CommitStatusObservationV1Input]
}
type commitStatusWireV1 struct {
	ProviderID     int64   `json:"provider_id"`
	ProviderNodeID string  `json:"provider_node_id"`
	State          string  `json:"state"`
	Context        string  `json:"context"`
	Description    *string `json:"description"`
	HeadSHA        string  `json:"head_sha"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	CreatorID      int64   `json:"creator_id"`
	CreatorNodeID  string  `json:"creator_node_id"`
	CreatorLogin   string  `json:"creator_login"`
	CreatorType    string  `json:"creator_type"`
}

func NewCommitStatusObservationV1(in CommitStatusObservationV1Input) (CommitStatusObservationV1, error) {
	in.Description = cloneString(in.Description)
	if in.ProviderID <= 0 || in.CreatorID <= 0 || !validOptionalText(in.ProviderNodeID, MaxTextBytes) || !legacyState[in.State] ||
		!validText(in.Context, MaxTextBytes) || !validOptionalString(in.Description, MaxTextBytes) || !validGitSHA(in.HeadSHA) ||
		!validTimestamp(in.CreatedAt) || !validTimestamp(in.UpdatedAt) || !validOptionalText(in.CreatorNodeID, MaxTextBytes) ||
		!validText(in.CreatorLogin, MaxTextBytes) || !validText(in.CreatorType, MaxTextBytes) {
		return CommitStatusObservationV1{}, errors.New("commit status observation contains invalid provider data")
	}
	if timestampAfter(in.CreatedAt, in.UpdatedAt) {
		return CommitStatusObservationV1{}, errors.New("commit status update precedes creation")
	}
	v, err := newImmutable(in, commitStatusWire(in))
	result := CommitStatusObservationV1{v}
	if err == nil && len(result.CanonicalJSON()) > MaxStatusObjectBytes {
		return CommitStatusObservationV1{}, errors.New("commit status observation exceeds encoded size limit")
	}
	return result, err
}
func commitStatusWire(in CommitStatusObservationV1Input) commitStatusWireV1 {
	return commitStatusWireV1{in.ProviderID, in.ProviderNodeID, in.State, in.Context, cloneString(in.Description), in.HeadSHA, in.CreatedAt, in.UpdatedAt, in.CreatorID, in.CreatorNodeID, in.CreatorLogin, in.CreatorType}
}
func (o CommitStatusObservationV1) Input() CommitStatusObservationV1Input {
	in := o.value.data
	in.Description = cloneString(in.Description)
	return in
}
func (o CommitStatusObservationV1) CanonicalJSON() []byte        { return o.value.bytes() }
func (o CommitStatusObservationV1) SHA256() string               { return o.value.digest }
func (o CommitStatusObservationV1) MarshalJSON() ([]byte, error) { return o.value.marshal() }

type CISemanticSweepV1Input struct {
	RepositoryOwner      string
	RepositoryName       string
	HeadSHA              string
	CheckSuiteTotalCount int
	CheckRunTotalCount   int
	CheckSuites          []CheckSuiteObservationV1
	CheckRuns            []CheckRunObservationV1
	CommitStatuses       []CommitStatusObservationV1
}
type CISemanticSweepV1 struct {
	value immutableV1[CISemanticSweepV1Input]
}
type semanticSweepWireV1 struct {
	SchemaVersion        int                         `json:"schema_version"`
	Kind                 string                      `json:"kind"`
	RepositoryOwner      string                      `json:"repository_owner"`
	RepositoryName       string                      `json:"repository_name"`
	HeadSHA              string                      `json:"head_sha"`
	CheckSuiteTotalCount int                         `json:"check_suite_total_count"`
	CheckRunTotalCount   int                         `json:"check_run_total_count"`
	CheckSuites          []CheckSuiteObservationV1   `json:"check_suites"`
	CheckRuns            []CheckRunObservationV1     `json:"check_runs"`
	CommitStatuses       []CommitStatusObservationV1 `json:"commit_statuses"`
}

func NewCISemanticSweepV1(in CISemanticSweepV1Input) (CISemanticSweepV1, error) {
	in = cloneSweepInput(in)
	if !validRepositoryComponent(in.RepositoryOwner) || !validRepositoryComponent(in.RepositoryName) || !validGitSHA(in.HeadSHA) ||
		in.CheckSuiteTotalCount != len(in.CheckSuites) || in.CheckRunTotalCount != len(in.CheckRuns) ||
		len(in.CheckSuites) > MaxSuites || len(in.CheckRuns) > MaxRuns || len(in.CommitStatuses) > MaxStatuses {
		return CISemanticSweepV1{}, errors.New("semantic sweep identity or counts are invalid")
	}
	if err := normalizeAndValidateSweep(&in); err != nil {
		return CISemanticSweepV1{}, err
	}
	value, err := newImmutable(in, semanticSweepWire(in))
	if err != nil {
		return CISemanticSweepV1{}, err
	}
	result := CISemanticSweepV1{value}
	if len(result.CanonicalJSON()) > MaxSemanticSweepBytes {
		return CISemanticSweepV1{}, errors.New("semantic sweep exceeds encoded size limit")
	}
	return result, nil
}
func semanticSweepWire(in CISemanticSweepV1Input) semanticSweepWireV1 {
	return semanticSweepWireV1{SchemaVersionV1, SemanticSweepKindV1, in.RepositoryOwner, in.RepositoryName, in.HeadSHA, in.CheckSuiteTotalCount, in.CheckRunTotalCount, append([]CheckSuiteObservationV1(nil), in.CheckSuites...), append([]CheckRunObservationV1(nil), in.CheckRuns...), append([]CommitStatusObservationV1(nil), in.CommitStatuses...)}
}
func (s CISemanticSweepV1) Input() CISemanticSweepV1Input { return cloneSweepInput(s.value.data) }
func (s CISemanticSweepV1) CanonicalJSON() []byte         { return s.value.bytes() }
func (s CISemanticSweepV1) SHA256() string                { return s.value.digest }
func (s CISemanticSweepV1) MarshalJSON() ([]byte, error)  { return s.value.marshal() }

type HeadObservationV1Input struct {
	Phase                    string
	Ref                      string
	ObjectType               string
	SHA                      string
	RequestSequence          int
	ResponseObservedUnixNano int64
}
type HeadObservationV1 struct {
	value immutableV1[HeadObservationV1Input]
}
type headObservationWireV1 struct {
	Phase                    string `json:"phase"`
	Ref                      string `json:"ref"`
	ObjectType               string `json:"object_type"`
	SHA                      string `json:"sha"`
	RequestSequence          int    `json:"request_sequence"`
	ResponseObservedUnixNano int64  `json:"response_observed_unix_nano"`
}

func NewHeadObservationV1(in HeadObservationV1Input) (HeadObservationV1, error) {
	if !headPhase[in.Phase] || !validText(in.Ref, MaxTextBytes) || in.ObjectType != "commit" || !validGitSHA(in.SHA) || in.RequestSequence <= 0 || in.ResponseObservedUnixNano <= 0 {
		return HeadObservationV1{}, errors.New("head observation is invalid")
	}
	v, err := newImmutable(in, headObservationWireV1{in.Phase, in.Ref, in.ObjectType, in.SHA, in.RequestSequence, in.ResponseObservedUnixNano})
	return HeadObservationV1{v}, err
}
func (o HeadObservationV1) Input() HeadObservationV1Input { return o.value.data }
func (o HeadObservationV1) CanonicalJSON() []byte         { return o.value.bytes() }
func (o HeadObservationV1) SHA256() string                { return o.value.digest }
func (o HeadObservationV1) MarshalJSON() ([]byte, error)  { return o.value.marshal() }

type RequestProvenanceV1Input struct {
	Sequence                 int
	Phase                    string
	Page                     int
	Method                   string
	PathTemplate             string
	EscapedPath              string
	CanonicalQuery           string
	APIOrigin                string
	APIVersion               string
	Accept                   string
	RequestSHA256            string
	HTTPStatus               int
	ResponseBodySHA256       string
	ResponseEnvelopeSHA256   string
	CapturedPrefixSHA256     string
	BodyTruncated            bool
	RequestID                string
	RequestStartedUnixNano   int64
	ResponseObservedUnixNano int64
	ResponseBytes            int64
	FailureCode              string
}
type RequestProvenanceV1 struct {
	value immutableV1[RequestProvenanceV1Input]
}
type requestProvenanceWireV1 struct {
	Sequence                 int    `json:"sequence"`
	Phase                    string `json:"phase"`
	Page                     int    `json:"page"`
	Method                   string `json:"method"`
	PathTemplate             string `json:"path_template"`
	EscapedPath              string `json:"escaped_path"`
	CanonicalQuery           string `json:"canonical_query"`
	APIOrigin                string `json:"api_origin"`
	APIVersion               string `json:"api_version"`
	Accept                   string `json:"accept"`
	RequestSHA256            string `json:"request_sha256"`
	HTTPStatus               int    `json:"http_status,omitempty"`
	ResponseBodySHA256       string `json:"response_body_sha256,omitempty"`
	ResponseEnvelopeSHA256   string `json:"response_envelope_sha256"`
	CapturedPrefixSHA256     string `json:"captured_prefix_sha256,omitempty"`
	BodyTruncated            bool   `json:"body_truncated"`
	RequestID                string `json:"request_id,omitempty"`
	RequestStartedUnixNano   int64  `json:"request_started_unix_nano"`
	ResponseObservedUnixNano int64  `json:"response_observed_unix_nano,omitempty"`
	ResponseBytes            int64  `json:"response_bytes"`
	FailureCode              string `json:"failure_code,omitempty"`
}

func NewRequestProvenanceV1(in RequestProvenanceV1Input) (RequestProvenanceV1, error) {
	if in.Sequence <= 0 || in.Sequence > MaxCollectionRequests || !validPhase(in.Phase) || in.Page < 0 || in.Method != "GET" || !validText(in.PathTemplate, MaxLinkBytes) || !validText(in.EscapedPath, MaxLinkBytes) || len(in.CanonicalQuery) > MaxLinkBytes || !validOptionalText(in.CanonicalQuery, MaxLinkBytes) || !validText(in.APIOrigin, MaxTextBytes) || !validText(in.APIVersion, MaxTextBytes) || !validText(in.Accept, MaxTextBytes) || !validDigest(in.RequestSHA256) || !validOptionalDigest(in.ResponseBodySHA256) || !validDigest(in.ResponseEnvelopeSHA256) || !validOptionalDigest(in.CapturedPrefixSHA256) || !validOptionalText(in.RequestID, MaxRequestIDBytes) || in.RequestStartedUnixNano <= 0 || in.ResponseObservedUnixNano < 0 || in.ResponseBytes < 0 || !validOptionalToken(in.FailureCode, MaxStateBytes) {
		return RequestProvenanceV1{}, errors.New("request provenance is invalid")
	}
	if in.HTTPStatus < 0 || in.HTTPStatus > 999 {
		return RequestProvenanceV1{}, errors.New("HTTP status is invalid")
	}
	if in.ResponseObservedUnixNano > 0 && in.ResponseObservedUnixNano < in.RequestStartedUnixNano {
		return RequestProvenanceV1{}, errors.New("response precedes request")
	}
	if in.BodyTruncated && in.CapturedPrefixSHA256 == "" {
		return RequestProvenanceV1{}, errors.New("truncated response requires prefix digest")
	}
	v, err := newImmutable(in, requestProvenanceWire(in))
	result := RequestProvenanceV1{v}
	if err == nil && len(result.CanonicalJSON()) > MaxProvenanceBytes {
		return RequestProvenanceV1{}, errors.New("request provenance exceeds encoded size limit")
	}
	return result, err
}
func requestProvenanceWire(in RequestProvenanceV1Input) requestProvenanceWireV1 {
	return requestProvenanceWireV1{in.Sequence, in.Phase, in.Page, in.Method, in.PathTemplate, in.EscapedPath, in.CanonicalQuery, in.APIOrigin, in.APIVersion, in.Accept, in.RequestSHA256, in.HTTPStatus, in.ResponseBodySHA256, in.ResponseEnvelopeSHA256, in.CapturedPrefixSHA256, in.BodyTruncated, in.RequestID, in.RequestStartedUnixNano, in.ResponseObservedUnixNano, in.ResponseBytes, in.FailureCode}
}
func (o RequestProvenanceV1) Input() RequestProvenanceV1Input { return o.value.data }
func (o RequestProvenanceV1) CanonicalJSON() []byte           { return o.value.bytes() }
func (o RequestProvenanceV1) SHA256() string                  { return o.value.digest }
func (o RequestProvenanceV1) MarshalJSON() ([]byte, error)    { return o.value.marshal() }

type CIEvidenceBundleV1Input struct {
	RunID                         string
	AttemptID                     string
	AttemptKeySHA256              string
	AuthoritySHA256               string
	LimitsSHA256                  string
	RepositoryOwner               string
	RepositoryName                string
	HeadBranch                    string
	HeadSHA                       string
	ActingKind                    string
	ActingSubject                 string
	AuthenticatedID               int64
	AuthenticatedNode             string
	AuthenticatedLogin            string
	Outcome                       CollectionOutcome
	FailureCode                   string
	FailedPhase                   string
	FailedPage                    int
	AttemptStartedUnixNano        int64
	AttemptEndedUnixNano          int64
	CollectionStartedUnixNano     int64
	CollectionEndedUnixNano       int64
	FirstResponseObservedUnixNano int64
	LastResponseObservedUnixNano  int64
	EarliestProviderStateAt       string
	LatestProviderStateAt         string
	HeadObservations              []HeadObservationV1
	SweepA                        *CISemanticSweepV1
	SweepB                        *CISemanticSweepV1
	SemanticDigestA               string
	SemanticDigestB               string
	RequestProvenance             []RequestProvenanceV1
	RequestResponseChainSHA256    string
	CollectionIdentitySHA256      string
}
type CIEvidenceBundleV1 struct {
	value immutableV1[CIEvidenceBundleV1Input]
}
type evidenceBundleWireV1 struct {
	SchemaVersion                 int                   `json:"schema_version"`
	Kind                          string                `json:"kind"`
	RunID                         string                `json:"run_id"`
	AttemptID                     string                `json:"attempt_id"`
	AttemptKeySHA256              string                `json:"attempt_key_sha256"`
	AuthoritySHA256               string                `json:"authority_sha256"`
	LimitsSHA256                  string                `json:"limits_sha256"`
	RepositoryOwner               string                `json:"repository_owner"`
	RepositoryName                string                `json:"repository_name"`
	HeadBranch                    string                `json:"head_branch"`
	HeadSHA                       string                `json:"head_sha"`
	ActingKind                    string                `json:"acting_kind"`
	ActingSubject                 string                `json:"acting_subject"`
	AuthenticatedID               int64                 `json:"authenticated_user_id,omitempty"`
	AuthenticatedNode             string                `json:"authenticated_user_node_id,omitempty"`
	AuthenticatedLogin            string                `json:"authenticated_user_login,omitempty"`
	Outcome                       CollectionOutcome     `json:"outcome"`
	FailureCode                   string                `json:"failure_code,omitempty"`
	FailedPhase                   string                `json:"failed_phase,omitempty"`
	FailedPage                    int                   `json:"failed_page,omitempty"`
	AttemptStartedUnixNano        int64                 `json:"attempt_started_unix_nano"`
	AttemptEndedUnixNano          int64                 `json:"attempt_ended_unix_nano"`
	CollectionStartedUnixNano     int64                 `json:"collection_started_unix_nano,omitempty"`
	CollectionEndedUnixNano       int64                 `json:"collection_ended_unix_nano,omitempty"`
	FirstResponseObservedUnixNano int64                 `json:"first_response_observed_unix_nano,omitempty"`
	LastResponseObservedUnixNano  int64                 `json:"last_response_observed_unix_nano,omitempty"`
	EarliestProviderStateAt       string                `json:"earliest_provider_state_at,omitempty"`
	LatestProviderStateAt         string                `json:"latest_provider_state_at,omitempty"`
	HeadObservations              []HeadObservationV1   `json:"head_observations"`
	SweepA                        *CISemanticSweepV1    `json:"sweep_a,omitempty"`
	SweepB                        *CISemanticSweepV1    `json:"sweep_b,omitempty"`
	SemanticDigestA               string                `json:"semantic_digest_a,omitempty"`
	SemanticDigestB               string                `json:"semantic_digest_b,omitempty"`
	RequestProvenance             []RequestProvenanceV1 `json:"request_provenance"`
	RequestResponseChainSHA256    string                `json:"request_response_chain_sha256"`
	CollectionIdentitySHA256      string                `json:"collection_identity_sha256,omitempty"`
}

func NewCIEvidenceBundleV1(in CIEvidenceBundleV1Input) (CIEvidenceBundleV1, error) {
	in = cloneBundleInput(in)
	if err := validateBundleInput(in); err != nil {
		return CIEvidenceBundleV1{}, err
	}
	value, err := newImmutable(in, evidenceBundleWire(in))
	if err != nil {
		return CIEvidenceBundleV1{}, err
	}
	result := CIEvidenceBundleV1{value}
	if len(result.CanonicalJSON()) > MaxBundleBytes {
		return CIEvidenceBundleV1{}, errors.New("CI evidence bundle exceeds encoded size limit")
	}
	return result, nil
}
func evidenceBundleWire(in CIEvidenceBundleV1Input) evidenceBundleWireV1 {
	return evidenceBundleWireV1{SchemaVersionV1, EvidenceBundleKindV1, in.RunID, in.AttemptID, in.AttemptKeySHA256, in.AuthoritySHA256, in.LimitsSHA256, in.RepositoryOwner, in.RepositoryName, in.HeadBranch, in.HeadSHA, in.ActingKind, in.ActingSubject, in.AuthenticatedID, in.AuthenticatedNode, in.AuthenticatedLogin, in.Outcome, in.FailureCode, in.FailedPhase, in.FailedPage, in.AttemptStartedUnixNano, in.AttemptEndedUnixNano, in.CollectionStartedUnixNano, in.CollectionEndedUnixNano, in.FirstResponseObservedUnixNano, in.LastResponseObservedUnixNano, in.EarliestProviderStateAt, in.LatestProviderStateAt, append([]HeadObservationV1(nil), in.HeadObservations...), cloneSweep(in.SweepA), cloneSweep(in.SweepB), in.SemanticDigestA, in.SemanticDigestB, append([]RequestProvenanceV1(nil), in.RequestProvenance...), in.RequestResponseChainSHA256, in.CollectionIdentitySHA256}
}
func (b CIEvidenceBundleV1) Input() CIEvidenceBundleV1Input { return cloneBundleInput(b.value.data) }
func (b CIEvidenceBundleV1) CanonicalJSON() []byte          { return b.value.bytes() }
func (b CIEvidenceBundleV1) SHA256() string                 { return b.value.digest }
func (b CIEvidenceBundleV1) MarshalJSON() ([]byte, error)   { return b.value.marshal() }

func validateBundleInput(in CIEvidenceBundleV1Input) error {
	repository, repositoryErr := githublifecycle.NewRepository(in.RepositoryOwner, in.RepositoryName)
	branch, branchErr := githublifecycle.NewBranch(in.HeadBranch)
	sha, shaErr := githublifecycle.NewGitSHA(in.HeadSHA)
	if !validText(in.RunID, MaxTextBytes) || !validOpaque(in.AttemptID, MaxAttemptIDBytes) || !validDigest(in.AttemptKeySHA256) || !validDigest(in.AuthoritySHA256) || in.LimitsSHA256 != ProductionLimitsSHA256() || repositoryErr != nil || repository.String() == "" || branchErr != nil || branch.String() == "" || shaErr != nil || sha.String() == "" || in.ActingKind != "user" || !validUserSubject(in.ActingSubject) || !in.Outcome.Valid() {
		return errors.New("CI evidence bundle identity is invalid")
	}
	if in.AttemptStartedUnixNano <= 0 || in.AttemptEndedUnixNano < in.AttemptStartedUnixNano {
		return errors.New("CI evidence attempt interval is invalid")
	}
	if in.CollectionStartedUnixNano < 0 || in.CollectionEndedUnixNano < 0 || (in.CollectionEndedUnixNano > 0 && in.CollectionEndedUnixNano < in.CollectionStartedUnixNano) {
		return errors.New("CI evidence collection interval is invalid")
	}
	if in.FirstResponseObservedUnixNano < 0 || in.LastResponseObservedUnixNano < in.FirstResponseObservedUnixNano {
		return errors.New("CI evidence response interval is invalid")
	}
	if !validOptionalTimestampValue(in.EarliestProviderStateAt) || !validOptionalTimestampValue(in.LatestProviderStateAt) {
		return errors.New("CI evidence provider timestamp range is invalid")
	}
	if in.EarliestProviderStateAt != "" && in.LatestProviderStateAt != "" && timestampAfter(in.EarliestProviderStateAt, in.LatestProviderStateAt) {
		return errors.New("CI evidence provider timestamp range is inverted")
	}
	if len(in.HeadObservations) > 3 || len(in.RequestProvenance) > MaxCollectionRequests {
		return errors.New("CI evidence bundle exceeds observation limits")
	}
	for i, p := range in.RequestProvenance {
		if p.value.data.Sequence != i+1 {
			return fmt.Errorf("request provenance %d is not in sequence", i)
		}
	}
	if !validOptionalToken(in.FailureCode, MaxStateBytes) || !validOptionalToken(in.FailedPhase, MaxStateBytes) || in.FailedPage < 0 {
		return errors.New("CI evidence failure fields are invalid")
	}
	if !validOptionalDigest(in.RequestResponseChainSHA256) || !validOptionalDigest(in.CollectionIdentitySHA256) || !validOptionalDigest(in.SemanticDigestA) || !validOptionalDigest(in.SemanticDigestB) {
		return errors.New("CI evidence digest is invalid")
	}
	if in.SweepA != nil && in.SemanticDigestA != in.SweepA.SHA256() {
		return errors.New("sweep A digest mismatch")
	}
	if in.SweepB != nil && in.SemanticDigestB != in.SweepB.SHA256() {
		return errors.New("sweep B digest mismatch")
	}
	for _, sweep := range []*CISemanticSweepV1{in.SweepA, in.SweepB} {
		if sweep != nil {
			d := sweep.value.data
			if d.RepositoryOwner != in.RepositoryOwner || d.RepositoryName != in.RepositoryName || d.HeadSHA != in.HeadSHA {
				return errors.New("semantic sweep is bound to a different collection identity")
			}
		}
	}
	if in.AuthenticatedID < 0 || !validOptionalText(in.AuthenticatedNode, MaxTextBytes) || !validOptionalText(in.AuthenticatedLogin, MaxTextBytes) ||
		(in.AuthenticatedID == 0 && (in.AuthenticatedNode != "" || in.AuthenticatedLogin != "")) {
		return errors.New("authenticated principal evidence is invalid")
	}
	if in.Outcome == OutcomeStable {
		if in.SweepA == nil || in.SweepB == nil || in.SemanticDigestA == "" || in.SemanticDigestA != in.SemanticDigestB || in.CollectionIdentitySHA256 == "" || in.RequestResponseChainSHA256 == "" || len(in.HeadObservations) != 3 || in.FailureCode != "" || in.AuthenticatedID <= 0 {
			return errors.New("STABLE bundle does not prove repeated observational stability")
		}
		for i, head := range in.HeadObservations {
			if head.value.data.Phase != []string{"H0", "H1", "H2"}[i] || head.value.data.SHA != in.HeadSHA {
				return errors.New("STABLE bundle head observations are incomplete or mismatched")
			}
		}
	} else if in.CollectionIdentitySHA256 != "" {
		return errors.New("only STABLE evidence has a collection identity")
	} else if in.FailureCode == "" {
		return errors.New("non-STABLE evidence requires a controller failure code")
	}
	nonSweep := in
	nonSweep.SweepA, nonSweep.SweepB = nil, nil
	b, err := json.Marshal(evidenceBundleWire(nonSweep))
	if err != nil || len(b) > MaxNonSweepBundleBytes {
		return errors.New("CI evidence non-sweep envelope exceeds encoded size limit")
	}
	return nil
}

func cloneSweepInput(in CISemanticSweepV1Input) CISemanticSweepV1Input {
	in.CheckSuites = append([]CheckSuiteObservationV1(nil), in.CheckSuites...)
	in.CheckRuns = append([]CheckRunObservationV1(nil), in.CheckRuns...)
	in.CommitStatuses = append([]CommitStatusObservationV1(nil), in.CommitStatuses...)
	if in.CheckSuites == nil {
		in.CheckSuites = []CheckSuiteObservationV1{}
	}
	if in.CheckRuns == nil {
		in.CheckRuns = []CheckRunObservationV1{}
	}
	if in.CommitStatuses == nil {
		in.CommitStatuses = []CommitStatusObservationV1{}
	}
	return in
}
func cloneSweep(in *CISemanticSweepV1) *CISemanticSweepV1 {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}
func cloneBundleInput(in CIEvidenceBundleV1Input) CIEvidenceBundleV1Input {
	in.HeadObservations = append([]HeadObservationV1(nil), in.HeadObservations...)
	in.RequestProvenance = append([]RequestProvenanceV1(nil), in.RequestProvenance...)
	if in.HeadObservations == nil {
		in.HeadObservations = []HeadObservationV1{}
	}
	if in.RequestProvenance == nil {
		in.RequestProvenance = []RequestProvenanceV1{}
	}
	in.SweepA = cloneSweep(in.SweepA)
	in.SweepB = cloneSweep(in.SweepB)
	return in
}

var suiteStatus = map[string]bool{"queued": true, "in_progress": true, "completed": true}
var runStatus = map[string]bool{"queued": true, "in_progress": true, "completed": true, "waiting": true, "requested": true, "pending": true}
var suiteConclusion = map[string]bool{"success": true, "failure": true, "neutral": true, "cancelled": true, "skipped": true, "timed_out": true, "action_required": true, "stale": true, "startup_failure": true}
var runConclusion = map[string]bool{"success": true, "failure": true, "neutral": true, "cancelled": true, "skipped": true, "timed_out": true, "action_required": true, "stale": true}
var legacyState = map[string]bool{"error": true, "failure": true, "pending": true, "success": true}
var headPhase = map[string]bool{"H0": true, "H1": true, "H2": true}

func validateConclusion(status string, c *string, allowed map[string]bool) error {
	if status == "completed" {
		if c == nil || !allowed[*c] {
			return errors.New("completed check requires a supported conclusion")
		}
		return nil
	}
	if c != nil {
		return errors.New("non-completed check requires a null conclusion")
	}
	return nil
}

func validUserSubject(subject string) bool {
	value, ok := strings.CutPrefix(subject, "github-user-id:")
	if !ok || value == "" || strings.HasPrefix(value, "+") || (len(value) > 1 && value[0] == '0') {
		return false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

// Compile-time assertions ensure custom canonical JSON is used when nested.
var _ json.Marshaler = CheckSuiteObservationV1{}
var _ json.Marshaler = CheckRunObservationV1{}
var _ json.Marshaler = CommitStatusObservationV1{}
var _ json.Marshaler = CISemanticSweepV1{}
var _ json.Marshaler = HeadObservationV1{}
var _ json.Marshaler = RequestProvenanceV1{}
var _ json.Marshaler = CIEvidenceBundleV1{}

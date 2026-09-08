// Package prlifecycle implements the controller-owned, exact-head GitHub
// pull-request metadata lifecycle. It deliberately owns no merge, CI, or
// domain-state-transition behavior.
package prlifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	SchemaVersion                              = 1
	MaxRequestBytes                            = 16 << 10
	MaxResponseBytes                           = 4 << 20
	MaxResponseHeaderBytes                     = 32 << 10
	MaxLinkHeaderBytes                         = 8 << 10
	MaxRequestIDBytes                          = 256
	MaxTerminalBytes                           = 256 << 10
	MaxReconciliationRounds                    = 8
	MaxResumeRounds                            = 4
	MinReconciliationInterval                  = 30 * time.Second
	MaxTerminalArtifactBytes                   = 32 << 10
	MaxTerminalSnapshotBytes                   = 16 << 10
	MaxPrepareRecordBytes                      = 256 << 10
	MaxRunIDBytes                              = 1024
	PRLifecycleDocumentMaxBytes                = 1024
	MaxPrepareHistoryRecordsPerPendingRevision = 24
	maxLifecycleRemoteText                     = PRLifecycleDocumentMaxBytes
)

const (
	CodePolicyMismatch            = "ADMISSION_POLICY_MISMATCH"
	CodeCapacityExhausted         = "ADMISSION_CAPACITY_EXHAUSTED"
	CodeIntegrityFailure          = "ADMISSION_INTEGRITY_FAILURE"
	CodeRemoteHeadDiverged        = "REMOTE_HEAD_UNPUBLISHED_OR_DIVERGED"
	CodeStaleAuthority            = "STALE_READY_FOR_MERGE_AUTHORITY"
	CodeExistingIneligible        = "EXISTING_INELIGIBLE_OPEN_PR"
	CodeDiscoveryTruncated        = "DISCOVERY_TRUNCATED"
	CodeRevisionConflict          = "REVISION_CONFLICT"
	CodeResumeBudgetExhausted     = "RESUME_BUDGET_EXHAUSTED"
	CodeReconcileBudgetExhausted  = "RECONCILIATION_BUDGET_EXHAUSTED"
	CodeUnsupportedNotApplied     = "UNSUPPORTED_NOT_APPLIED_PROOF"
	CodeLedgerUnavailable         = "LEDGER_RECOVERY_UNAVAILABLE"
	CodeLedgerIntegrity           = "LEDGER_INTEGRITY_FAILURE"
	CodeRemoteDivergedAfterWrite  = "REMOTE_DIVERGED_AFTER_WRITE"
	CodePreflightBudgetExhausted  = "PREFLIGHT_BUDGET_EXHAUSTED"
	CodePreflightHistoryExhausted = "PREFLIGHT_HISTORY_EXHAUSTED"
	CodeAmbiguousUnresolved       = "AMBIGUOUS_WRITE_UNRESOLVED"
	CodeRemoteReadFailed          = "REMOTE_READ_FAILED"
	CodeRemoteWriteFailed         = "REMOTE_WRITE_FAILED"
)

type Error struct {
	Code      string
	Submitted bool
	Attempt   string
	Cause     error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause == nil {
		return e.Code
	}
	return e.Code + ": " + e.Cause.Error()
}
func (e *Error) Unwrap() error { return e.Cause }

type PRAdmissionPolicyV1 struct {
	SchemaVersion         int   `json:"schema_version"`
	MaxAdmissionResources int64 `json:"max_admission_resources"`
	MaxAdmissionFiles     int64 `json:"max_admission_files"`
	MaxAdmissionBytes     int64 `json:"max_admission_bytes"`
}

func DefaultAdmissionPolicy() PRAdmissionPolicyV1 {
	return PRAdmissionPolicyV1{SchemaVersion, 4096, 65536, 512 << 20}
}

func (p PRAdmissionPolicyV1) validate() error {
	if p.SchemaVersion != SchemaVersion || p.MaxAdmissionResources <= 0 || p.MaxAdmissionFiles <= 0 || p.MaxAdmissionBytes <= 0 {
		return errors.New("invalid PR admission policy")
	}
	return nil
}
func (p PRAdmissionPolicyV1) SHA256() (string, error) { return canonicalDigest(p) }

type PRReconciliationPolicyV1 struct {
	SchemaVersion       int   `json:"schema_version"`
	MaxRounds           int   `json:"max_rounds"`
	MaxBytesPerRound    int   `json:"max_bytes_per_round"`
	MaxAggregateBytes   int   `json:"max_aggregate_bytes"`
	MinimumIntervalNano int64 `json:"minimum_interval_nanos"`
}

func DefaultReconciliationPolicy() PRReconciliationPolicyV1 {
	limits := githublifecycle.DefaultLimits()
	return PRReconciliationPolicyV1{SchemaVersion, MaxReconciliationRounds, 8 * limits.MaxTextBytes, 64 * limits.MaxTextBytes, int64(MinReconciliationInterval)}
}

// PRResourceKeyV1 is the digest of the physical same-repository base/head PR
// relationship. Actor, document, SHA, mode, and run identity never enter it.
type PRResourceKeyV1 string

func NewPRResourceKey(repository githublifecycle.Repository, base, head githublifecycle.Branch) (PRResourceKeyV1, error) {
	if repository.String() == "" || base.String() == "" || head.String() == "" || base == head {
		return "", errors.New("invalid PR resource identity")
	}
	wire := struct {
		SchemaVersion int    `json:"schema_version"`
		Owner         string `json:"repository_owner"`
		Name          string `json:"repository_name"`
		Base          string `json:"base_branch"`
		HeadOwner     string `json:"head_repository_owner"`
		HeadName      string `json:"head_repository_name"`
		Head          string `json:"head_branch"`
	}{SchemaVersion, repository.Owner(), repository.Name(), base.String(), repository.Owner(), repository.Name(), head.String()}
	d, err := canonicalDigest(wire)
	return PRResourceKeyV1(d), err
}

func (k PRResourceKeyV1) String() string { return string(k) }
func (k PRResourceKeyV1) valid() bool    { return validDigest(string(k)) }

type HTTPRequestIdentity struct {
	Method               string `json:"method"`
	PathTemplate         string `json:"path_template"`
	EscapedPath          string `json:"escaped_path"`
	CanonicalQuery       string `json:"canonical_query,omitempty"`
	APIOrigin            string `json:"api_origin"`
	APIVersion           string `json:"api_version"`
	Accept               string `json:"accept"`
	ContentType          string `json:"content_type,omitempty"`
	ExpectedStatus       []int  `json:"expected_status"`
	RequestBodySHA256    string `json:"request_body_sha256,omitempty"`
	BoundedResponseBytes int    `json:"bounded_response_bytes"`
}

type RemoteRefObservation struct {
	Request          HTTPRequestIdentity `json:"request"`
	EchoedRef        string              `json:"echoed_ref"`
	ObjectType       string              `json:"object_type"`
	SHA              string              `json:"sha"`
	RequestID        string              `json:"request_id,omitempty"`
	ObservedUnixNano int64               `json:"observed_unix_nano"`
	LimitsSHA256     string              `json:"limits_sha256"`
}

type RemotePrincipalObservation struct {
	Request          HTTPRequestIdentity `json:"request"`
	ID               int64               `json:"id"`
	NodeID           string              `json:"node_id"`
	Login            string              `json:"login"`
	RequestID        string              `json:"request_id,omitempty"`
	ObservedUnixNano int64               `json:"observed_unix_nano"`
	LimitsSHA256     string              `json:"limits_sha256"`
}

type RemotePRObservation struct {
	Request          HTTPRequestIdentity `json:"request"`
	Number           int64               `json:"number"`
	NodeID           string              `json:"node_id"`
	RepositoryOwner  string              `json:"repository_owner"`
	RepositoryName   string              `json:"repository_name"`
	BaseRepository   string              `json:"base_repository"`
	HeadRepository   string              `json:"head_repository"`
	BaseRef          string              `json:"base_ref"`
	HeadRef          string              `json:"head_ref"`
	BaseSHA          string              `json:"base_sha"`
	HeadSHA          string              `json:"head_sha"`
	HeadLabel        string              `json:"head_label"`
	AuthorID         int64               `json:"author_id"`
	AuthorNodeID     string              `json:"author_node_id"`
	AuthorLogin      string              `json:"author_login"`
	State            string              `json:"state"`
	Merged           bool                `json:"merged"`
	Title            string              `json:"title"`
	Body             string              `json:"body"`
	RequestID        string              `json:"request_id,omitempty"`
	ObservedUnixNano int64               `json:"observed_unix_nano"`
	LimitsSHA256     string              `json:"limits_sha256"`
}

type PRDocumentObservation struct {
	PullRequest RemotePRObservation  `json:"pull_request"`
	Head        RemoteRefObservation `json:"head_ref"`
	Base        RemoteRefObservation `json:"base_ref"`
	Snapshot    json.RawMessage      `json:"snapshot"`
	Digest      string               `json:"digest"`
}

type Disposition string

const (
	AppliedConfirmed         Disposition = "applied_confirmed"
	AppliedReconciled        Disposition = "applied_reconciled"
	RemoteDivergedAfterWrite Disposition = "remote_diverged_after_write"
)

type PRLifecycleResultCoreV1 struct {
	SchemaVersion      int         `json:"schema_version"`
	Disposition        Disposition `json:"disposition"`
	ResourceKey        string      `json:"resource_key"`
	Revision           uint64      `json:"revision"`
	Generation         uint64      `json:"generation"`
	WriteID            string      `json:"write_id"`
	Repository         string      `json:"repository"`
	BaseBranch         string      `json:"base_branch"`
	HeadBranch         string      `json:"head_branch"`
	HeadSHA            string      `json:"head_sha"`
	ExpectedBaseTipSHA string      `json:"expected_base_tip_sha"`
	PRNumber           int64       `json:"pr_number,omitempty"`
	PRNodeID           string      `json:"pr_node_id,omitempty"`
	DocumentSHA256     string      `json:"document_sha256"`
	PrincipalID        int64       `json:"principal_id"`
	PrincipalNodeID    string      `json:"principal_node_id"`
	PrincipalLogin     string      `json:"principal_login"`
	SnapshotSHA256     string      `json:"snapshot_sha256"`
	PRObservationSHA   string      `json:"pr_observation_sha256"`
	HeadObservationSHA string      `json:"head_observation_sha256"`
	BaseObservationSHA string      `json:"base_observation_sha256"`
	ReconciliationSHA  string      `json:"reconciliation_sha256,omitempty"`
}

func (c PRLifecycleResultCoreV1) CanonicalJSON() ([]byte, error) {
	snapshotValid := validDigest(c.SnapshotSHA256)
	if c.Disposition == RemoteDivergedAfterWrite {
		snapshotValid = c.SnapshotSHA256 == "" || snapshotValid
	}
	if c.SchemaVersion != SchemaVersion || !validDigest(c.ResourceKey) || c.Revision == 0 || c.Generation != 1 || c.WriteID == "" ||
		(c.Disposition != AppliedConfirmed && c.Disposition != AppliedReconciled && c.Disposition != RemoteDivergedAfterWrite) ||
		!validDigest(c.DocumentSHA256) || !snapshotValid || !validDigest(c.PRObservationSHA) || !validDigest(c.HeadObservationSHA) || !validDigest(c.BaseObservationSHA) {
		return nil, errors.New("incomplete PR lifecycle result core")
	}
	return json.Marshal(c)
}
func (c PRLifecycleResultCoreV1) SHA256() (string, error) { return canonicalDigest(c) }

type PRLifecycleResultV1 struct {
	core         PRLifecycleResultCoreV1
	terminalSHA  string
	evidenceRefs []ledger.EvidenceRef
}

func (r PRLifecycleResultV1) Core() PRLifecycleResultCoreV1 { return r.core }
func (r PRLifecycleResultV1) TerminalSHA256() string        { return r.terminalSHA }
func (r PRLifecycleResultV1) EvidenceRefs() []ledger.EvidenceRef {
	return append([]ledger.EvidenceRef(nil), r.evidenceRefs...)
}
func (r PRLifecycleResultV1) CanonicalJSON() []byte {
	b, _ := json.Marshal(struct {
		Core        PRLifecycleResultCoreV1 `json:"core"`
		TerminalSHA string                  `json:"terminal_sha256"`
		Evidence    []ledger.EvidenceRef    `json:"immutable_evidence_refs"`
	}{r.core, r.terminalSHA, r.evidenceRefs})
	return b
}
func (r PRLifecycleResultV1) MarshalJSON() ([]byte, error) { return r.CanonicalJSON(), nil }

type TerminalBudgetV1 struct {
	AuthorityBytes            int `json:"authority_bytes"`
	AttemptBytes              int `json:"attempt_bytes"`
	DocumentBytes             int `json:"document_bytes"`
	SourceAuthorityCap        int `json:"source_authority_cap"`
	DerivedAuthorityCap       int `json:"derived_authority_cap"`
	AttemptCap                int `json:"attempt_cap"`
	SnapshotCap               int `json:"snapshot_cap"`
	PrincipalObservationCap   int `json:"principal_observation_cap"`
	PullRequestObservationCap int `json:"pull_request_observation_cap"`
	RefObservationsCap        int `json:"ref_observations_cap"`
	ReconciliationCap         int `json:"reconciliation_cap"`
	TerminalArtifactCap       int `json:"terminal_artifact_cap"`
	ResultCoreCap             int `json:"result_core_cap"`
	FramingAndEventCap        int `json:"framing_and_event_cap"`
	WorstCaseBytes            int `json:"worst_case_bytes"`
}

func observationDigest(v any, max int) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if len(b) > max {
		return "", fmt.Errorf("observation exceeds %d-byte cap", max)
	}
	return digestBytes(b), nil
}

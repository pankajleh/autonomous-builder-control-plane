package mergelifecycle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	CodeUnsupportedPlatform          = "UNSUPPORTED_PLATFORM"
	CodeInvalidAuthority             = "AUTHORITY_INVALID"
	CodeStaleReadyAuthority          = "AUTHORITY_STALE"
	CodeUnsupportedMergeMethod       = "UNSUPPORTED_MERGE_METHOD"
	CodeAuthorizationFailed          = "POLICY_REJECTED"
	CodePullRequestIneligible        = "PR_INELIGIBLE"
	CodePaginationInvalid            = "PAGINATION_INVALID"
	CodePreSubmitUnavailable         = "PRE_SUBMIT_VALIDATION_UNAVAILABLE"
	CodeUnsupportedAtomicCAS         = "UNSUPPORTED_ATOMIC_BASE_HEAD_CAS"
	CodeCommitPreparationFailed      = "COMMIT_PREPARATION_FAILED"
	CodeTargetNotSubmitted           = "TARGET_UPDATE_NOT_SUBMITTED"
	CodeTargetNotAppliedBase         = "TARGET_UPDATE_NOT_APPLIED_STALE_BASE"
	CodeTargetNotAppliedHead         = "TARGET_UPDATE_NOT_APPLIED_STALE_HEAD"
	CodeTargetNotAppliedRefs         = "TARGET_UPDATE_NOT_APPLIED_STALE_REFS"
	CodeTargetNotApplied             = CodeTargetNotAppliedBase
	CodeTargetUnknown                = "TARGET_REF_UPDATE_UNKNOWN"
	CodePostMergeAcceptanceFailed    = "POST_MERGE_ACCEPTANCE_FAILED"
	CodeLocalStorageIntegrityFailure = "LOCAL_STORAGE_INTEGRITY_FAILURE"
	CodeLocalPublicationFailed       = "LOCAL_PUBLICATION_FAILED"
	CodeResourceLimitExhausted       = "RESOURCE_LIMIT_EXHAUSTED"
	CodeLocalCleanupFailed           = "LOCAL_CLEANUP_FAILED"
	CodeLocalCleanupExhausted        = "LOCAL_CLEANUP_RECOVERY_EXHAUSTED"
	CodeCancelledBeforeSubmission    = "CANCELLED_BEFORE_TARGET_SUBMISSION"
	CodeCancelledAfterNotApplied     = "CANCELLED_AFTER_NOT_APPLIED"
	CodeMergeAppliedAccepted         = "MERGE_APPLIED_ACCEPTED"
)

var errUnsupportedDurability = errors.New("merge lifecycle requires verified Linux no-follow, flock, and durable-directory semantics")

type Error struct {
	Code      string
	Submitted bool
	AttemptID string
	Cause     error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause == nil {
		return e.Code
	}
	return e.Code + ": " + e.Cause.Error()
}
func (e *Error) Unwrap() error { return e.Cause }

type terminalFailureError struct {
	reason string
	cause  error
}

func (e *terminalFailureError) Error() string { return e.cause.Error() }
func (e *terminalFailureError) Unwrap() error { return e.cause }

func typedTerminalFailure(reason string, cause error) error {
	if cause == nil {
		cause = errors.New(reason)
	}
	return &terminalFailureError{reason: reason, cause: cause}
}

type ObservationPhase string

const (
	ObservationInitial ObservationPhase = "initial"
	ObservationFinal   ObservationPhase = "final"
)

// PolicyDefinition is loaded from controller configuration. It is never
// accepted on ExecuteRequest.
type PolicyDefinition struct {
	Version             string
	SourceConfiguration ledger.EvidenceRef
	RequiredPrincipal   githublifecycle.ActingIdentity
	RequiredChecks      []githublifecycle.TrustedCheckIdentityV1
	EligibleReviewers   []githublifecycle.StableIdentityV1
	RequiredReviewers   []githublifecycle.StableIdentityV1
	MinimumApprovals    int
	Recipe              githublifecycle.MergeCommitRecipePolicyV1
}

// GovernedAuthority is the durable controller input used to reconstruct READY
// authority. Provider observations and execution requests cannot override it.
type GovernedAuthority struct {
	Phase3Authority    authority.Authority
	ProjectID          string
	PlanID             string
	AttemptID          string
	AcceptedSources    []githublifecycle.AcceptedSourceCandidateV1
	RepositoryBinding  githublifecycle.RepositoryBindingV1Input
	Repository         githublifecycle.Repository
	BaseBranch         githublifecycle.Branch
	HeadBranch         githublifecycle.Branch
	PullRequest        githublifecycle.PullRequestIdentity
	ExpectedContent    githublifecycle.ExpectedMergeContent
	Policy             PolicyDefinition
	ProviderCapability githublifecycle.ProviderCapabilityV1
	EvidenceClosure    []ledger.EvidenceRef
	// EvidenceRoot is selected by controller configuration, never by an
	// ExecuteRequest. Every authority-bearing evidence URI must be an absolute
	// artifact beneath this root and is read and digest-verified before the
	// lifecycle Authority is constructed.
	EvidenceRoot string
}

type AuthoritySource interface {
	Resolve(context.Context, string) (GovernedAuthority, error)
}

// CancellationSource resolves the authenticated, policy-scoped grant for one
// source request. CancelRequest itself deliberately carries no policy,
// principal, repository, or merge authority.
type CancellationSource interface {
	ResolveCancellation(context.Context, string, string) (CancellationGrant, error)
}

type CancellationGrant struct {
	Requester                 githublifecycle.StablePrincipalV1
	AuthenticationEvidence    ledger.EvidenceRef
	CancellationPolicyVersion string
	CancellationPolicySource  ledger.EvidenceRef
	CancellationPolicySHA256  string
	ScopedGrantSHA256         string
	AllowDecisionSHA256       string
	SourceKind                string
	RequestEvidence           ledger.EvidenceRef
	IngressID                 string
	EvidenceRefs              []ledger.EvidenceRef
}

type AuthorizationObservation struct {
	PullRequest         githublifecycle.AuthoritativePullRequestSnapshotV1
	Checks              []githublifecycle.Check
	CheckRunsClosure    githublifecycle.PaginationClosureV1
	CommitStatusClosure githublifecycle.PaginationClosureV1
	EvidenceRefs        []ledger.EvidenceRef
	StartedUnixNano     int64
	CompletedUnixNano   int64
	Counters            githublifecycle.AuthorizationCountersV1
	Accounting          ProviderAccountingV1
}

// CommitPreparationObservationV1 is the independently checkable exact Git
// object returned by preparation or preparation reconciliation. The object
// bytes are retained because matching only a claimed OID and recipe digest is
// not proof that GitHub stored the controller's exact commit object.
type CommitPreparationObservationV1 struct {
	Schema            string                                `json:"schema"`
	Repository        string                                `json:"repository"`
	ResultSHA         string                                `json:"result_sha"`
	ResultTree        string                                `json:"result_tree"`
	Parents           []string                              `json:"parents"`
	Message           string                                `json:"message"`
	Author            githublifecycle.MergeCommitIdentityV1 `json:"author"`
	Committer         githublifecycle.MergeCommitIdentityV1 `json:"committer"`
	AuthorUnix        int64                                 `json:"author_unix"`
	CommitterUnix     int64                                 `json:"committer_unix"`
	ObjectFormat      string                                `json:"object_format"`
	ObjectBytes       []byte                                `json:"object_bytes"`
	ObjectBytesSHA256 string                                `json:"object_bytes_sha256"`
	RecipeSHA256      string                                `json:"recipe_sha256"`
	EvidenceRefs      []ledger.EvidenceRef                  `json:"evidence_refs"`
}

type CommitPreparation struct {
	Schema       string                         `json:"schema"`
	RecipeSHA256 string                         `json:"recipe_sha256"`
	ResultSHA    string                         `json:"result_sha"`
	Observation  CommitPreparationObservationV1 `json:"observation"`
	EvidenceRefs []ledger.EvidenceRef           `json:"evidence_refs"`
	Accounting   ProviderAccountingV1           `json:"accounting"`
}

func (p CommitPreparation) validate(recipe githublifecycle.MergeCommitRecipeV1) error {
	if p.Schema != "merge-commit-preparation-v1" || p.RecipeSHA256 != recipe.SHA256() || p.ResultSHA != recipe.ExpectedResultSHA().String() || len(p.EvidenceRefs) == 0 {
		return errors.New("commit preparation does not prove the exact deterministic recipe")
	}
	if err := p.Observation.validate(recipe); err != nil {
		return err
	}
	if err := validateEvidence(p.EvidenceRefs); err != nil {
		return err
	}
	for _, observed := range p.Observation.EvidenceRefs {
		if !containsRef(p.EvidenceRefs, observed) {
			return errors.New("commit preparation omits exact-object observation evidence")
		}
	}
	return p.Accounting.validate()
}

func (o CommitPreparationObservationV1) validate(recipe githublifecycle.MergeCommitRecipeV1) error {
	i := recipe.Input()
	parents := make([]string, len(i.Parents))
	for index := range i.Parents {
		parents[index] = i.Parents[index].String()
	}
	if o.Schema != "merge-commit-preparation-observation-v1" || o.Repository != i.Repository.String() ||
		o.ResultSHA != i.ExpectedResultSHA.String() || o.ResultTree != i.ExpectedResultTree.String() ||
		!equalStringSlices(o.Parents, parents) || o.Message != i.Message || o.Author != i.Author || o.Committer != i.Committer ||
		o.AuthorUnix != i.AuthorUnix || o.CommitterUnix != i.CommitterUnix || o.ObjectFormat != i.ObjectFormat ||
		o.RecipeSHA256 != recipe.SHA256() || !bytes.Equal(o.ObjectBytes, recipe.CommitBytes()) ||
		o.ObjectBytesSHA256 != digest(recipe.CommitBytes()) || validateEvidence(o.EvidenceRefs) != nil {
		return errors.New("prepared commit object does not match the exact canonical recipe")
	}
	return nil
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// ProviderAccountingV1 is the exact post-operation observation consumed by
// the controller's durable cumulative accounting channel.
type ProviderAccountingV1 struct {
	RequestBytes              int64 `json:"request_bytes"`
	HeaderBytes               int64 `json:"header_bytes"`
	CompressedResponseBytes   int64 `json:"compressed_response_bytes"`
	DecompressedResponseBytes int64 `json:"decompressed_response_bytes"`
	ActiveNanos               int64 `json:"active_nanos"`
	InvocationNanos           int64 `json:"invocation_nanos"`
}

type ProviderBudgetV1 struct {
	RequestBytes              int64
	HeaderBytes               int64
	CompressedResponseBytes   int64
	DecompressedResponseBytes int64
	ActiveNanos               int64
}

type providerBudgetContextKey struct{}

func ProviderBudgetFromContext(ctx context.Context) (ProviderBudgetV1, bool) {
	if ctx == nil {
		return ProviderBudgetV1{}, false
	}
	budget, ok := ctx.Value(providerBudgetContextKey{}).(ProviderBudgetV1)
	return budget, ok
}

func (a ProviderAccountingV1) validate() error {
	if a.RequestBytes < 0 || a.HeaderBytes < 0 || a.CompressedResponseBytes < 0 ||
		a.DecompressedResponseBytes < 0 || a.ActiveNanos < 0 || a.InvocationNanos < 0 {
		return errors.New("provider accounting contains a negative value")
	}
	return nil
}

type TargetOutcome struct {
	Disposition     githublifecycle.ReconciliationDisposition
	Result          githublifecycle.MergeResult
	NotAppliedProof githublifecycle.NotAppliedProofV1
	EvidenceRefs    []ledger.EvidenceRef
	RequestBytes    int64
	Accounting      ProviderAccountingV1
}

type PostMergeOutcome struct {
	Observation githublifecycle.PostMergeObservation
	Accounting  ProviderAccountingV1
}

type Provider interface {
	ObserveAuthorization(context.Context, ObservationPhase, githublifecycle.Authority) (AuthorizationObservation, error)
	PrepareResultCommit(context.Context, githublifecycle.MergeCommitRecipeV1) (CommitPreparation, error)
	ReconcileResultCommit(context.Context, githublifecycle.MergeCommitRecipeV1) (CommitPreparation, error)
	SubmitTarget(context.Context, githublifecycle.MergeExecutionInputV1) (TargetOutcome, error)
	ReconcileTarget(context.Context, githublifecycle.ReconcileWriteInput) (TargetOutcome, error)
	ObservePostMerge(context.Context, githublifecycle.ObservePostMergeInput) (PostMergeOutcome, error)
}

type ExecuteRequest struct {
	RunID string
}

type CancelRequest struct {
	RunID           string
	SourceRequestID string
}

type Result struct {
	State            domain.State
	ReasonCode       string
	AttemptID        string
	TerminalSHA256   string
	MergeResult      githublifecycle.MergeResult
	PostMergeProof   githublifecycle.PostMergeObservation
	CleanupIncidents []LocalCleanupIncidentV1
	Unresolved       bool
}

type admissionRecordV1 struct {
	Schema           string          `json:"schema"`
	RunID            string          `json:"run_id"`
	ProjectID        string          `json:"project_id"`
	PlanID           string          `json:"plan_id"`
	AttemptID        string          `json:"attempt_id"`
	WriteID          string          `json:"write_id"`
	MergeInput       json.RawMessage `json:"merge_input"`
	MergeInputSHA256 string          `json:"merge_input_sha256"`
	LimitsSHA256     string          `json:"limits_sha256"`
}

type targetOutcomeRecordV1 struct {
	Schema        string                                    `json:"schema"`
	Disposition   githublifecycle.ReconciliationDisposition `json:"disposition"`
	RequestBytes  int64                                     `json:"request_bytes"`
	Result        json.RawMessage                           `json:"result,omitempty"`
	ResultSHA256  string                                    `json:"result_sha256,omitempty"`
	NotApplied    json.RawMessage                           `json:"not_applied,omitempty"`
	NotAppliedSHA string                                    `json:"not_applied_sha256,omitempty"`
	EvidenceRefs  []ledger.EvidenceRef                      `json:"evidence_refs"`
	Accounting    ProviderAccountingV1                      `json:"accounting"`
}

type reconciliationRecordV1 struct {
	Schema           string          `json:"schema"`
	SealSHA256       string          `json:"seal_sha256"`
	SubmissionSHA256 string          `json:"submission_sha256"`
	LimitsSHA256     string          `json:"limits_sha256"`
	Outcome          json.RawMessage `json:"outcome"`
	OutcomeSHA256    string          `json:"outcome_sha256"`
}

type TerminalCoreV1 struct {
	Schema                string       `json:"schema"`
	ProjectID             string       `json:"project_id"`
	PlanID                string       `json:"plan_id"`
	RunID                 string       `json:"run_id"`
	AttemptID             string       `json:"attempt_id"`
	WriteID               string       `json:"write_id"`
	ReadyEventID          string       `json:"ready_event_id"`
	ReadyEventSHA256      string       `json:"ready_event_sha256"`
	ReadySequence         int64        `json:"ready_sequence"`
	AuthoritySHA256       string       `json:"authority_sha256"`
	PolicySHA256          string       `json:"policy_sha256"`
	MergeInputSHA256      string       `json:"merge_input_sha256,omitempty"`
	SealSHA256            string       `json:"seal_sha256,omitempty"`
	CommitmentSHA256      string       `json:"commitment_sha256,omitempty"`
	SubmissionSHA256      string       `json:"submission_sha256,omitempty"`
	ResultSHA256          string       `json:"result_sha256,omitempty"`
	VerificationSHA256    string       `json:"verification_sha256,omitempty"`
	CancellationSHA256    string       `json:"cancellation_sha256,omitempty"`
	NotAppliedProofSHA256 string       `json:"not_applied_proof_sha256,omitempty"`
	Destination           domain.State `json:"destination"`
	ReasonCode            string       `json:"reason_code"`
	SelectedUnixNano      int64        `json:"selected_unix_nano"`
}

type FinalTerminalV1 struct {
	Schema             string          `json:"schema"`
	Core               json.RawMessage `json:"core"`
	CoreSHA256         string          `json:"core_sha256"`
	Event              json.RawMessage `json:"event"`
	EventSHA256        string          `json:"event_sha256"`
	LedgerAppendProved bool            `json:"ledger_append_proved"`
}

type LocalCleanupIncidentV1 struct {
	Schema             string `json:"schema"`
	IncidentID         string `json:"incident_id"`
	Sequence           int    `json:"sequence"`
	AttemptID          string `json:"attempt_id"`
	WriteID            string `json:"write_id"`
	TerminalCoreSHA256 string `json:"terminal_core_sha256"`
	EventID            string `json:"event_id"`
	Operation          string `json:"operation"`
	Boundary           string `json:"boundary"`
	ErrorClass         string `json:"error_class"`
	ErrorSHA256        string `json:"error_sha256"`
	ResultSHA256       string `json:"result_sha256,omitempty"`
	VerificationSHA256 string `json:"verification_sha256,omitempty"`
}

func ParseTerminalCoreV1(data []byte) (TerminalCoreV1, error) {
	var core TerminalCoreV1
	if err := strictCanonical(data, &core); err != nil {
		return core, err
	}
	if err := core.validate(); err != nil {
		return core, err
	}
	return core, nil
}

func ParseFinalTerminalV1(data []byte) (FinalTerminalV1, error) {
	var terminal FinalTerminalV1
	if err := strictCanonical(data, &terminal); err != nil {
		return terminal, err
	}
	core, err := ParseTerminalCoreV1(terminal.Core)
	if err != nil || digest(terminal.Core) != terminal.CoreSHA256 || digest(terminal.Event) != terminal.EventSHA256 ||
		terminal.Schema != "merge-final-terminal-v1" || !terminal.LedgerAppendProved {
		return terminal, errors.Join(errors.New("final terminal binding is invalid"), err)
	}
	expected, expectedBytes, eventErr := deterministicTerminalEvent(core, terminal.CoreSHA256)
	var event ledger.Event
	if decodeErr := strictCanonical(terminal.Event, &event); decodeErr != nil || eventErr != nil || event.Validate() != nil ||
		event.EventID != expected.EventID || !bytes.Equal(expectedBytes, terminal.Event) {
		return terminal, errors.Join(errors.New("final terminal event does not match its core"), decodeErr, eventErr)
	}
	return terminal, nil
}

func ParseLocalCleanupIncidentV1(data []byte) (LocalCleanupIncidentV1, error) {
	var incident LocalCleanupIncidentV1
	if err := strictCanonical(data, &incident); err != nil {
		return incident, err
	}
	if incident.Schema != "merge-local-cleanup-incident-v1" || !validDigest(incident.IncidentID) || incident.Sequence <= 0 ||
		incident.AttemptID == "" || incident.WriteID == "" || !validDigest(incident.TerminalCoreSHA256) || incident.EventID == "" ||
		incident.Operation == "" || incident.Boundary == "" || incident.ErrorClass == "" || !validDigest(incident.ErrorSHA256) {
		return incident, errors.New("cleanup incident is invalid")
	}
	copy := incident
	copy.IncidentID = ""
	dataWithoutID, _ := json.Marshal(copy)
	if digest(dataWithoutID) != incident.IncidentID {
		return incident, errors.New("cleanup incident digest disagrees")
	}
	return incident, nil
}

func (c TerminalCoreV1) validate() error {
	if c.Schema != "merge-terminal-core-v1" || c.ProjectID == "" || c.PlanID == "" || c.RunID == "" || c.AttemptID == "" || c.WriteID == "" ||
		c.ReadyEventID == "" || !validDigest(c.ReadyEventSHA256) || c.ReadySequence <= 0 || c.Destination == "" || c.ReasonCode == "" || c.SelectedUnixNano <= 0 {
		return errors.New("terminal core is incomplete")
	}
	preAuthorityFailure := c.Destination == domain.StateFailed && c.ReasonCode == CodeInvalidAuthority && c.AuthoritySHA256 == "" && c.PolicySHA256 == ""
	if !preAuthorityFailure && (!validDigest(c.AuthoritySHA256) || !validDigest(c.PolicySHA256)) {
		return errors.New("terminal core lacks its stage-qualified authority identity")
	}
	if c.Destination != domain.StateMerged && c.Destination != domain.StateFailed && c.Destination != domain.StateCancelled {
		return errors.New("terminal core destination is not legal")
	}
	if !legalTerminalReason(c.Destination, c.ReasonCode) {
		return errors.New("terminal core reason is not canonical for its destination")
	}
	for _, value := range []string{c.PolicySHA256, c.MergeInputSHA256, c.SealSHA256, c.CommitmentSHA256, c.SubmissionSHA256,
		c.ResultSHA256, c.VerificationSHA256, c.CancellationSHA256, c.NotAppliedProofSHA256} {
		if value != "" && !validDigest(value) {
			return errors.New("terminal core contains an invalid optional digest")
		}
	}
	if c.Destination == domain.StateMerged && (!validDigest(c.ResultSHA256) || !validDigest(c.VerificationSHA256)) {
		return errors.New("MERGED terminal core lacks result or verification proof")
	}
	if c.Destination == domain.StateCancelled && !validDigest(c.CancellationSHA256) {
		return errors.New("CANCELLED terminal core lacks cancellation authority")
	}
	return nil
}

func legalTerminalReason(destination domain.State, reason string) bool {
	if destination == domain.StateMerged {
		return reason == CodeMergeAppliedAccepted
	}
	if destination == domain.StateCancelled {
		return reason == CodeCancelledBeforeSubmission || reason == CodeCancelledAfterNotApplied
	}
	if destination != domain.StateFailed {
		return false
	}
	switch reason {
	case CodeInvalidAuthority, CodeStaleReadyAuthority, CodePullRequestIneligible, CodeAuthorizationFailed,
		CodePaginationInvalid, CodePreSubmitUnavailable, CodeUnsupportedAtomicCAS, CodeCommitPreparationFailed,
		CodeTargetNotSubmitted, CodeTargetNotAppliedBase, CodeTargetNotAppliedHead, CodeTargetNotAppliedRefs,
		CodeLocalPublicationFailed, CodeResourceLimitExhausted, CodeLocalCleanupFailed,
		CodeLocalStorageIntegrityFailure, CodePostMergeAcceptanceFailed:
		return true
	default:
		return false
	}
}

func strictCanonical(data []byte, value any) error {
	if len(data) == 0 {
		return errors.New("canonical record is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("canonical record contains trailing data")
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, data) {
		return errors.New("record JSON is not canonical")
	}
	return nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateEvidence(refs []ledger.EvidenceRef) error {
	if len(refs) == 0 || len(refs) > 256 {
		return errors.New("evidence closure is empty or excessive")
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref.URI) == "" || len(ref.URI) > 4096 || !validDigest(ref.SHA256) || strings.TrimSpace(ref.Kind) == "" {
			return errors.New("evidence reference is invalid")
		}
		key := ref.URI + "\x00" + ref.SHA256 + "\x00" + ref.Kind
		if _, ok := seen[key]; ok {
			return errors.New("evidence reference is duplicated")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func wrap(code string, submitted bool, attempt string, err error) error {
	if err == nil {
		err = fmt.Errorf("%s", code)
	}
	return &Error{Code: code, Submitted: submitted, AttemptID: attempt, Cause: err}
}

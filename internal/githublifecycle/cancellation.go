package githublifecycle

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	CancellationAuthoritySchemaV1        = "cancellation-authority-v1"
	DurableCancellationAuthoritySchemaV1 = "durable-cancellation-authority-v1"
)

type CancellationBoundaryV1 string

const (
	CancellationPreAdmission                CancellationBoundaryV1 = "PRE_ADMISSION"
	CancellationAdmittedPreTargetSubmission CancellationBoundaryV1 = "ADMITTED_PRE_TARGET_SUBMISSION"
	CancellationTargetSubmissionUnknown     CancellationBoundaryV1 = "TARGET_SUBMISSION_UNKNOWN"
	CancellationTargetNotApplied            CancellationBoundaryV1 = "TARGET_NOT_APPLIED"
)

type CancellationSubmissionProofKindV1 string

const (
	CancellationProofNoAdmission             CancellationSubmissionProofKindV1 = "no_admission"
	CancellationProofZeroRequestBytes        CancellationSubmissionProofKindV1 = "zero_request_bytes"
	CancellationProofSealedZeroRequestBytes  CancellationSubmissionProofKindV1 = "sealed_zero_request_bytes"
	CancellationProofUnresolvedSubmission    CancellationSubmissionProofKindV1 = "unresolved_submission"
	CancellationProofAuthenticatedNotApplied CancellationSubmissionProofKindV1 = "authenticated_not_applied"
)

type StablePrincipalV1 struct {
	Kind     string           `json:"kind"`
	Identity StableIdentityV1 `json:"identity"`
}

func (p StablePrincipalV1) valid(l Limits) bool {
	return validOpaqueID(p.Kind, l.MaxTextBytes) && p.Identity.valid()
}

type CancellationAuthorityV1Input struct {
	ProjectID                 string
	PlanID                    string
	RunID                     string
	RepositoryBindingSHA256   string
	Phase3AuthoritySHA256     string
	ReadyEventSHA256          string
	ReadyEventID              string
	ReadyRunStateSequence     int64
	ReadyBindingSHA256        string
	LedgerPrefixSHA256        string
	LedgerPrefixLength        int64
	CurrentReadyProof         CurrentReadyProofV1
	Boundary                  CancellationBoundaryV1
	ReceiptUnixNano           int64
	IngressSequence           int64
	AdmissionSHA256           string
	Attempt                   *WriteAttempt
	SealSHA256                string
	CommitmentSHA256          string
	SubmissionProof           CancellationSubmissionProofV1
	Requester                 StablePrincipalV1
	AuthenticationEvidence    ledger.EvidenceRef
	CancellationPolicyVersion string
	CancellationPolicySource  ledger.EvidenceRef
	CancellationPolicySHA256  string
	ScopedGrantSHA256         string
	AllowDecisionSHA256       string
	SourceRequestID           string
	SourceKind                string
	RequestEvidence           ledger.EvidenceRef
	IngressID                 string
	EvidenceRefs              []ledger.EvidenceRef
}

type CancellationAuthorityV1 struct {
	input     CancellationAuthorityV1Input
	id        string
	canonical []byte
	digest    string
	limitsSHA string
}

func NewCancellationAuthorityV1(input CancellationAuthorityV1Input, limits Limits) (CancellationAuthorityV1, error) {
	input = cloneCancellationInput(input)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return CancellationAuthorityV1{}, err
	}
	if !validText(input.ProjectID, limits.MaxTextBytes, false) || !validText(input.PlanID, limits.MaxTextBytes, false) || !validText(input.RunID, limits.MaxTextBytes, false) || !validSHA256(input.RepositoryBindingSHA256) || !validSHA256(input.Phase3AuthoritySHA256) || !validSHA256(input.ReadyEventSHA256) || !validOpaqueID(input.ReadyEventID, limits.MaxTextBytes) || input.ReadyRunStateSequence <= 0 || !validSHA256(input.ReadyBindingSHA256) || !validSHA256(input.LedgerPrefixSHA256) || input.LedgerPrefixLength <= 0 || !input.CurrentReadyProof.valid() || input.ReceiptUnixNano < input.CurrentReadyProof.input.ObservedUnixNano || input.IngressSequence <= 0 || !input.SubmissionProof.valid() || requireLimitsSHA(limits, input.SubmissionProof.limitsSHA) != nil || !input.Requester.valid(limits) || !validEvidenceRef(input.AuthenticationEvidence) || !validText(input.CancellationPolicyVersion, limits.MaxTextBytes, false) || !validEvidenceRef(input.CancellationPolicySource) || !validSHA256(input.CancellationPolicySHA256) || !validSHA256(input.ScopedGrantSHA256) || !validSHA256(input.AllowDecisionSHA256) || !validOpaqueID(input.SourceRequestID, limits.MaxTextBytes) || !validOpaqueID(input.SourceKind, limits.MaxTextBytes) || !validEvidenceRef(input.RequestEvidence) || !validOpaqueID(input.IngressID, limits.MaxTextBytes) {
		return CancellationAuthorityV1{}, errors.New("cancellation authority identity, principal, policy, READY, or request binding is invalid")
	}
	rebuiltReady, err := NewCurrentReadyProofV1(input.CurrentReadyProof.input, limits)
	if err != nil || rebuiltReady.SHA256() != input.CurrentReadyProof.SHA256() ||
		!bytes.Equal(rebuiltReady.CanonicalJSON(), input.CurrentReadyProof.CanonicalJSON()) {
		return CancellationAuthorityV1{}, errors.New("cancellation current-READY proof fails independent validation")
	}
	ready := input.CurrentReadyProof.input.ReadyBinding
	if input.ProjectID != ready.input.ProjectID || input.PlanID != ready.input.PlanID || input.RunID != ready.input.RunID ||
		input.RepositoryBindingSHA256 != ready.RepositoryBinding().SHA256() || input.Phase3AuthoritySHA256 != ready.input.Phase3AuthoritySHA256 ||
		input.ReadyEventSHA256 != ready.input.ReadyEventSHA256 || input.ReadyEventID != ready.input.ReadyEventID ||
		input.ReadyRunStateSequence != ready.input.ReadyRunStateSequence || input.ReadyBindingSHA256 != ready.SHA256() ||
		input.LedgerPrefixSHA256 != ready.input.LedgerPrefixSHA256 || input.LedgerPrefixLength != ready.input.LedgerPrefixLength {
		return CancellationAuthorityV1{}, errors.New("cancellation authority changed its current READY proof identities")
	}
	rebuiltSubmission, err := NewCancellationSubmissionProofV1(input.SubmissionProof.input, limits)
	if err != nil || rebuiltSubmission.SHA256() != input.SubmissionProof.SHA256() ||
		!bytes.Equal(rebuiltSubmission.CanonicalJSON(), input.SubmissionProof.CanonicalJSON()) {
		return CancellationAuthorityV1{}, errors.New("cancellation submission proof fails independent canonical validation")
	}
	if len(input.EvidenceRefs) == 0 || canonicalizeEvidence(&input.EvidenceRefs, limits) != nil || !containsEvidence(input.EvidenceRefs, input.AuthenticationEvidence) || !containsEvidence(input.EvidenceRefs, input.CancellationPolicySource) || !containsEvidence(input.EvidenceRefs, input.RequestEvidence) {
		return CancellationAuthorityV1{}, errors.New("cancellation evidence closure is incomplete")
	}
	for _, evidence := range input.CurrentReadyProof.input.EvidenceRefs {
		if !containsEvidence(input.EvidenceRefs, evidence) {
			return CancellationAuthorityV1{}, errors.New("cancellation evidence closure omits current-READY evidence")
		}
	}
	if proof := input.SubmissionProof.input.NotAppliedProof; proof != nil && !containsNotAppliedEvidence(input.EvidenceRefs, *proof) {
		return CancellationAuthorityV1{}, errors.New("cancellation evidence closure omits NOT_APPLIED response evidence")
	}
	if err := validateCancellationBoundary(input, limits); err != nil {
		return CancellationAuthorityV1{}, err
	}
	core := cancellationWire(input, "", limitsSHA)
	core.AuthorityID = ""
	coreJSON, _, err := canonicalJSON(core)
	if err != nil {
		return CancellationAuthorityV1{}, err
	}
	id := digestBytes(coreJSON)
	wire := cancellationWire(input, id, limitsSHA)
	canonical, digest, err := canonicalJSON(wire)
	if err != nil {
		return CancellationAuthorityV1{}, err
	}
	return CancellationAuthorityV1{input, id, canonical, digest, limitsSHA}, nil
}
func (c CancellationAuthorityV1) Input() CancellationAuthorityV1Input {
	return cloneCancellationInput(c.input)
}
func (c CancellationAuthorityV1) AuthorityID() string   { return c.id }
func (c CancellationAuthorityV1) CanonicalJSON() []byte { return append([]byte(nil), c.canonical...) }
func (c CancellationAuthorityV1) SHA256() string        { return c.digest }
func (c CancellationAuthorityV1) MarshalJSON() ([]byte, error) {
	if !c.valid() {
		return nil, errors.New("cancellation authority incomplete")
	}
	return c.CanonicalJSON(), nil
}
func (c CancellationAuthorityV1) valid() bool {
	return len(c.canonical) > 0 && validSHA256(c.id) && validSHA256(c.digest) && digestBytes(c.canonical) == c.digest && validSHA256(c.limitsSHA)
}

type cancellationAuthorityWireV1 struct {
	Schema                    string                            `json:"schema"`
	AuthorityID               string                            `json:"authority_id"`
	ProjectID                 string                            `json:"project_id"`
	PlanID                    string                            `json:"plan_id"`
	RunID                     string                            `json:"run_id"`
	RepositoryBindingSHA256   string                            `json:"repository_binding_sha256"`
	Phase3AuthoritySHA256     string                            `json:"phase3_authority_sha256"`
	ReadyEventSHA256          string                            `json:"ready_event_sha256"`
	ReadyEventID              string                            `json:"ready_event_id"`
	ReadyRunStateSequence     int64                             `json:"ready_run_state_sequence"`
	ReadyBindingSHA256        string                            `json:"ready_binding_sha256"`
	LedgerPrefixSHA256        string                            `json:"ledger_prefix_sha256"`
	LedgerPrefixLength        int64                             `json:"ledger_prefix_length"`
	CurrentReadyProof         json.RawMessage                   `json:"current_ready_proof"`
	CurrentReadyProofSHA256   string                            `json:"current_ready_proof_sha256"`
	Boundary                  CancellationBoundaryV1            `json:"requested_at_boundary"`
	ReceiptUnixNano           int64                             `json:"receipt_unix_nano"`
	IngressSequence           int64                             `json:"ingress_sequence"`
	AdmissionSHA256           string                            `json:"admission_sha256,omitempty"`
	Attempt                   *writeAttemptWire                 `json:"write_attempt,omitempty"`
	SealSHA256                string                            `json:"seal_sha256,omitempty"`
	CommitmentSHA256          string                            `json:"commitment_sha256,omitempty"`
	SubmissionProofKind       CancellationSubmissionProofKindV1 `json:"submission_proof_kind"`
	SubmissionProof           json.RawMessage                   `json:"submission_proof"`
	SubmissionProofSHA256     string                            `json:"submission_proof_sha256"`
	Requester                 StablePrincipalV1                 `json:"requester"`
	AuthenticationEvidence    ledger.EvidenceRef                `json:"authentication_evidence"`
	CancellationPolicyVersion string                            `json:"cancellation_policy_version"`
	CancellationPolicySource  ledger.EvidenceRef                `json:"cancellation_policy_source"`
	CancellationPolicySHA256  string                            `json:"cancellation_policy_sha256"`
	ScopedGrantSHA256         string                            `json:"scoped_grant_sha256"`
	AllowDecisionSHA256       string                            `json:"allow_decision_sha256"`
	SourceRequestID           string                            `json:"source_request_id"`
	SourceKind                string                            `json:"source_kind"`
	RequestEvidence           ledger.EvidenceRef                `json:"request_evidence"`
	IngressID                 string                            `json:"ingress_id"`
	EvidenceRefs              []ledger.EvidenceRef              `json:"evidence_refs"`
	LimitsSHA256              string                            `json:"limits_sha256"`
}

func cancellationWire(i CancellationAuthorityV1Input, id, limitsSHA string) cancellationAuthorityWireV1 {
	var attempt *writeAttemptWire
	if i.Attempt != nil {
		wire := attemptWire(*i.Attempt)
		attempt = &wire
	}
	return cancellationAuthorityWireV1{CancellationAuthoritySchemaV1, id, i.ProjectID, i.PlanID, i.RunID, i.RepositoryBindingSHA256, i.Phase3AuthoritySHA256, i.ReadyEventSHA256, i.ReadyEventID, i.ReadyRunStateSequence, i.ReadyBindingSHA256, i.LedgerPrefixSHA256, i.LedgerPrefixLength, i.CurrentReadyProof.CanonicalJSON(), i.CurrentReadyProof.SHA256(), i.Boundary, i.ReceiptUnixNano, i.IngressSequence, i.AdmissionSHA256, attempt, i.SealSHA256, i.CommitmentSHA256, i.SubmissionProof.input.Kind, i.SubmissionProof.CanonicalJSON(), i.SubmissionProof.SHA256(), i.Requester, i.AuthenticationEvidence, i.CancellationPolicyVersion, i.CancellationPolicySource, i.CancellationPolicySHA256, i.ScopedGrantSHA256, i.AllowDecisionSHA256, i.SourceRequestID, i.SourceKind, i.RequestEvidence, i.IngressID, i.EvidenceRefs, limitsSHA}
}

func validateCancellationBoundary(i CancellationAuthorityV1Input, limits Limits) error {
	hasAttempt := i.Attempt != nil
	if hasAttempt && (!i.Attempt.valid(limits) || i.Attempt.operation != OperationMerge) {
		return errors.New("cancellation attempt is invalid")
	}
	ready := i.CurrentReadyProof.input.ReadyBinding
	if hasAttempt && (i.Attempt.readyBindingSHA256 != ready.SHA256() || i.Attempt.repository != ready.RepositoryBinding().input.GitHubRepository ||
		i.AdmissionSHA256 != i.Attempt.payloadSHA256) {
		return errors.New("cancellation attempt does not belong to the bound READY admission")
	}
	p := i.SubmissionProof.input
	if p.Kind == "" || !equalOptionalAttempt(p.Attempt, i.Attempt) || p.AdmissionSHA256 != i.AdmissionSHA256 ||
		p.SealSHA256 != i.SealSHA256 || p.CommitmentSHA256 != i.CommitmentSHA256 ||
		!containsEvidence(i.EvidenceRefs, p.EvidenceRef) {
		return errors.New("cancellation boundary does not match its canonical typed submission proof")
	}
	switch i.Boundary {
	case CancellationPreAdmission:
		if hasAttempt || i.AdmissionSHA256 != "" || i.SealSHA256 != "" || i.CommitmentSHA256 != "" || p.Kind != CancellationProofNoAdmission {
			return errors.New("pre-admission cancellation contains admitted attempt state")
		}
	case CancellationAdmittedPreTargetSubmission:
		if !hasAttempt || !validSHA256(i.AdmissionSHA256) || i.SealSHA256 != "" || i.CommitmentSHA256 != "" || p.Kind != CancellationProofZeroRequestBytes {
			return errors.New("admitted pre-submit cancellation lacks exact zero-byte attempt proof")
		}
	case CancellationTargetSubmissionUnknown:
		if !hasAttempt || !validSHA256(i.AdmissionSHA256) || !validSHA256(i.SealSHA256) || !validSHA256(i.CommitmentSHA256) || p.Kind != CancellationProofUnresolvedSubmission {
			return errors.New("unknown-submission cancellation lacks exact sealed commitment proof")
		}
	case CancellationTargetNotApplied:
		if !hasAttempt || !validSHA256(i.AdmissionSHA256) || !validSHA256(i.SealSHA256) || !validSHA256(i.CommitmentSHA256) ||
			(p.Kind != CancellationProofAuthenticatedNotApplied && p.Kind != CancellationProofSealedZeroRequestBytes) {
			return errors.New("not-applied cancellation lacks authenticated all-or-nothing proof")
		}
	default:
		return errors.New("unsupported cancellation boundary")
	}
	return nil
}

type CancellationAuthorityExpectationV1 struct {
	ProjectID                 string
	PlanID                    string
	RunID                     string
	ReadyBinding              ReadyAuthorityBindingV1
	CurrentReadyProof         CurrentReadyProofV1
	AdmissionSHA256           string
	Requester                 StablePrincipalV1
	AuthenticationEvidence    ledger.EvidenceRef
	CancellationPolicyVersion string
	CancellationPolicySource  ledger.EvidenceRef
	CancellationPolicySHA256  string
	ScopedGrantSHA256         string
	AllowDecisionSHA256       string
	Boundary                  CancellationBoundaryV1
	Attempt                   *WriteAttempt
	SealSHA256                string
	CommitmentSHA256          string
	SealedAuthorization       SealedMergeAuthorizationV1
	SubmissionProof           CancellationSubmissionProofV1
	SourceRequestID           string
	SourceKind                string
	RequestEvidence           ledger.EvidenceRef
	IngressID                 string
	ReceiptUnixNano           int64
	IngressSequence           int64
	EvidenceRefs              []ledger.EvidenceRef
}

func ValidateCancellationAuthorityV1(authority CancellationAuthorityV1, expected CancellationAuthorityExpectationV1, limits Limits) error {
	if !authority.valid() {
		return errors.New("cancellation authority is incomplete")
	}
	if err := requireLimitsSHA(limits, authority.limitsSHA); err != nil {
		return err
	}
	rebuilt, err := NewCancellationAuthorityV1(authority.input, limits)
	if err != nil || rebuilt.id != authority.id || rebuilt.digest != authority.digest || !bytes.Equal(rebuilt.canonical, authority.canonical) {
		return errors.New("cancellation authority fails independent validation")
	}
	i := authority.input
	if !expected.ReadyBinding.valid() || !expected.CurrentReadyProof.valid() || !expected.SubmissionProof.valid() ||
		i.ProjectID != expected.ProjectID || i.PlanID != expected.PlanID || i.RunID != expected.RunID ||
		i.RepositoryBindingSHA256 != expected.ReadyBinding.RepositoryBinding().SHA256() || i.Phase3AuthoritySHA256 != expected.ReadyBinding.input.Phase3AuthoritySHA256 ||
		i.ReadyEventSHA256 != expected.ReadyBinding.input.ReadyEventSHA256 || i.ReadyEventID != expected.ReadyBinding.input.ReadyEventID ||
		i.ReadyRunStateSequence != expected.ReadyBinding.input.ReadyRunStateSequence || i.ReadyBindingSHA256 != expected.ReadyBinding.SHA256() ||
		i.LedgerPrefixSHA256 != expected.ReadyBinding.input.LedgerPrefixSHA256 || i.LedgerPrefixLength != expected.ReadyBinding.input.LedgerPrefixLength ||
		i.CurrentReadyProof.SHA256() != expected.CurrentReadyProof.SHA256() ||
		!bytes.Equal(i.CurrentReadyProof.CanonicalJSON(), expected.CurrentReadyProof.CanonicalJSON()) || i.AdmissionSHA256 != expected.AdmissionSHA256 ||
		i.Requester != expected.Requester || i.AuthenticationEvidence != expected.AuthenticationEvidence ||
		i.CancellationPolicyVersion != expected.CancellationPolicyVersion || i.CancellationPolicySource != expected.CancellationPolicySource ||
		i.CancellationPolicySHA256 != expected.CancellationPolicySHA256 || i.ScopedGrantSHA256 != expected.ScopedGrantSHA256 ||
		i.AllowDecisionSHA256 != expected.AllowDecisionSHA256 || i.Boundary != expected.Boundary || !equalOptionalAttempt(i.Attempt, expected.Attempt) ||
		i.SealSHA256 != expected.SealSHA256 || i.CommitmentSHA256 != expected.CommitmentSHA256 ||
		i.SubmissionProof.SHA256() != expected.SubmissionProof.SHA256() || !bytes.Equal(i.SubmissionProof.CanonicalJSON(), expected.SubmissionProof.CanonicalJSON()) ||
		i.SourceRequestID != expected.SourceRequestID || i.SourceKind != expected.SourceKind || i.RequestEvidence != expected.RequestEvidence ||
		i.IngressID != expected.IngressID || i.ReceiptUnixNano != expected.ReceiptUnixNano || i.IngressSequence != expected.IngressSequence ||
		!equalEvidence(i.EvidenceRefs, expected.EvidenceRefs) {
		return errors.New("cancellation authority does not match independent READY, policy, principal, request, attempt, or boundary expectations")
	}
	if err := validateCancellationSubmissionExpectationV1(expected, limits); err != nil {
		return err
	}
	return nil
}

func validateCancellationSubmissionExpectationV1(expected CancellationAuthorityExpectationV1, limits Limits) error {
	rebuiltReady, err := NewCurrentReadyProofV1(expected.CurrentReadyProof.input, limits)
	if err != nil || rebuiltReady.SHA256() != expected.CurrentReadyProof.SHA256() ||
		!bytes.Equal(rebuiltReady.CanonicalJSON(), expected.CurrentReadyProof.CanonicalJSON()) ||
		expected.CurrentReadyProof.input.ReadyBinding.SHA256() != expected.ReadyBinding.SHA256() {
		return errors.New("expected cancellation current-READY proof fails independent validation")
	}
	rebuiltProof, err := NewCancellationSubmissionProofV1(expected.SubmissionProof.input, limits)
	if err != nil || rebuiltProof.SHA256() != expected.SubmissionProof.SHA256() ||
		!bytes.Equal(rebuiltProof.CanonicalJSON(), expected.SubmissionProof.CanonicalJSON()) {
		return errors.New("expected cancellation submission proof fails independent validation")
	}
	if expected.Boundary != CancellationTargetSubmissionUnknown && expected.Boundary != CancellationTargetNotApplied {
		return nil
	}
	sealed := expected.SealedAuthorization
	if !sealed.valid() {
		return errors.New("submitted cancellation expectation lacks the exact sealed authorization")
	}
	recovered, err := ParseCanonicalSealedMergeAuthorizationV1(sealed.CanonicalJSON(), limits)
	if err != nil || recovered.SHA256() != sealed.SHA256() || !bytes.Equal(recovered.CanonicalJSON(), sealed.CanonicalJSON()) {
		return errors.New("submitted cancellation expectation sealed authorization fails independent validation")
	}
	mergeInput := sealed.input.MergeInput
	proof := expected.SubmissionProof.input
	if mergeInput.authority.ReadyBinding().SHA256() != expected.ReadyBinding.SHA256() ||
		expected.AdmissionSHA256 != mergeInput.SHA256() || !equalOptionalAttempt(expected.Attempt, &mergeInput.attempt) ||
		expected.SealSHA256 != sealed.input.Seal.SHA256() || expected.CommitmentSHA256 != sealed.input.Commitment.SHA256() ||
		proof.AdmissionSHA256 != mergeInput.SHA256() || !equalOptionalAttempt(proof.Attempt, &mergeInput.attempt) ||
		proof.SealSHA256 != sealed.input.Seal.SHA256() || proof.CommitmentSHA256 != sealed.input.Commitment.SHA256() {
		return errors.New("submitted cancellation expectation changed its sealed admission, attempt, seal, or commitment")
	}
	if proof.TargetSubmission == nil || ValidateTargetSubmissionV1(sealed, *proof.TargetSubmission, limits) != nil {
		return errors.New("submitted cancellation expectation lacks the exact target submission record")
	}
	if expected.Boundary == CancellationTargetNotApplied {
		if proof.NotAppliedProof == nil || ValidateNotAppliedProofV1(sealed, *proof.TargetSubmission, *proof.NotAppliedProof, limits) != nil {
			return errors.New("submitted cancellation expectation lacks the exact sealed NOT_APPLIED proof")
		}
	}
	return nil
}

func ParseCanonicalCancellationAuthorityV1(data []byte, limits Limits) (CancellationAuthorityV1, error) {
	var w cancellationAuthorityWireV1
	if err := strictDecode(data, &w); err != nil {
		return CancellationAuthorityV1{}, err
	}
	if w.Schema != CancellationAuthoritySchemaV1 {
		return CancellationAuthorityV1{}, errors.New("unsupported cancellation authority schema")
	}
	readyProof, err := ParseCanonicalCurrentReadyProofV1(w.CurrentReadyProof, limits)
	if err != nil || readyProof.SHA256() != w.CurrentReadyProofSHA256 {
		return CancellationAuthorityV1{}, errors.New("cancellation current-READY proof identity disagrees")
	}
	var attempt *WriteAttempt
	if w.Attempt != nil {
		value, err := writeAttemptFromWire(*w.Attempt)
		if err != nil {
			return CancellationAuthorityV1{}, err
		}
		attempt = &value
	}
	submissionProof, err := ParseCanonicalCancellationSubmissionProofV1(w.SubmissionProof, limits)
	if err != nil || submissionProof.SHA256() != w.SubmissionProofSHA256 || submissionProof.input.Kind != w.SubmissionProofKind {
		return CancellationAuthorityV1{}, errors.New("cancellation submission proof identity disagrees")
	}
	input := CancellationAuthorityV1Input{
		ProjectID: w.ProjectID, PlanID: w.PlanID, RunID: w.RunID, RepositoryBindingSHA256: w.RepositoryBindingSHA256,
		Phase3AuthoritySHA256: w.Phase3AuthoritySHA256, ReadyEventSHA256: w.ReadyEventSHA256, ReadyEventID: w.ReadyEventID,
		ReadyRunStateSequence: w.ReadyRunStateSequence, ReadyBindingSHA256: w.ReadyBindingSHA256, LedgerPrefixSHA256: w.LedgerPrefixSHA256,
		LedgerPrefixLength: w.LedgerPrefixLength, CurrentReadyProof: readyProof, Boundary: w.Boundary,
		ReceiptUnixNano: w.ReceiptUnixNano, IngressSequence: w.IngressSequence, AdmissionSHA256: w.AdmissionSHA256, Attempt: attempt,
		SealSHA256: w.SealSHA256, CommitmentSHA256: w.CommitmentSHA256, SubmissionProof: submissionProof, Requester: w.Requester,
		AuthenticationEvidence: w.AuthenticationEvidence, CancellationPolicyVersion: w.CancellationPolicyVersion,
		CancellationPolicySource: w.CancellationPolicySource, CancellationPolicySHA256: w.CancellationPolicySHA256,
		ScopedGrantSHA256: w.ScopedGrantSHA256, AllowDecisionSHA256: w.AllowDecisionSHA256, SourceRequestID: w.SourceRequestID,
		SourceKind: w.SourceKind, RequestEvidence: w.RequestEvidence, IngressID: w.IngressID, EvidenceRefs: w.EvidenceRefs,
	}
	value, err := NewCancellationAuthorityV1(input, limits)
	if err != nil {
		return CancellationAuthorityV1{}, err
	}
	if value.id != w.AuthorityID || w.LimitsSHA256 != value.limitsSHA {
		return CancellationAuthorityV1{}, errors.New("cancellation authority ID or limits digest disagrees")
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return CancellationAuthorityV1{}, err
	}
	return value, nil
}

type DurableCancellationAuthorityV1 struct {
	authority       CancellationAuthorityV1
	channelEvidence ledger.EvidenceRef
	replayIdentity  CancellationReplayIdentityV1
	canonical       []byte
	digest          string
}

type durableCancellationAuthorityWireV1 struct {
	Schema               string             `json:"schema"`
	Authority            json.RawMessage    `json:"authority"`
	AuthoritySHA256      string             `json:"authority_sha256"`
	ChannelEvidence      ledger.EvidenceRef `json:"channel_evidence"`
	ReplayIdentity       json.RawMessage    `json:"replay_identity"`
	ReplayIdentitySHA256 string             `json:"replay_identity_sha256"`
}

func NewDurableCancellationAuthorityV1(authority CancellationAuthorityV1, channelEvidence ledger.EvidenceRef, replayIdentity CancellationReplayIdentityV1, limits Limits) (DurableCancellationAuthorityV1, error) {
	if !authority.valid() || requireLimitsSHA(limits, authority.limitsSHA) != nil || !validEvidenceRef(channelEvidence) ||
		validateCancellationReplayIdentityV1(authority, replayIdentity, limits) != nil {
		return DurableCancellationAuthorityV1{}, errors.New("durable cancellation authority requires validated fsynced channel and replay-index evidence")
	}
	wire := durableCancellationAuthorityWireV1{
		DurableCancellationAuthoritySchemaV1, authority.CanonicalJSON(), authority.SHA256(), channelEvidence,
		replayIdentity.CanonicalJSON(), replayIdentity.SHA256(),
	}
	canonical, digest, err := canonicalJSON(wire)
	if err != nil {
		return DurableCancellationAuthorityV1{}, err
	}
	return DurableCancellationAuthorityV1{authority, channelEvidence, replayIdentity, canonical, digest}, nil
}
func (d DurableCancellationAuthorityV1) CanonicalJSON() []byte {
	return append([]byte(nil), d.canonical...)
}
func (d DurableCancellationAuthorityV1) SHA256() string { return d.digest }
func (d DurableCancellationAuthorityV1) Authority() CancellationAuthorityV1 {
	return cloneCancellationAuthority(d.authority)
}
func (d DurableCancellationAuthorityV1) valid() bool {
	return d.authority.valid() && d.replayIdentity.valid() && validEvidenceRef(d.channelEvidence) &&
		validSHA256(d.digest) && digestBytes(d.canonical) == d.digest
}

func ParseCanonicalDurableCancellationAuthorityV1(data []byte, limits Limits) (DurableCancellationAuthorityV1, error) {
	var wire durableCancellationAuthorityWireV1
	if err := strictDecode(data, &wire); err != nil {
		return DurableCancellationAuthorityV1{}, err
	}
	if wire.Schema != DurableCancellationAuthoritySchemaV1 {
		return DurableCancellationAuthorityV1{}, errors.New("unsupported durable cancellation authority schema")
	}
	authority, err := ParseCanonicalCancellationAuthorityV1(wire.Authority, limits)
	if err != nil || authority.SHA256() != wire.AuthoritySHA256 {
		return DurableCancellationAuthorityV1{}, errors.New("durable cancellation authority identity disagrees")
	}
	replay, err := ParseCanonicalCancellationReplayIdentityV1(wire.ReplayIdentity, authority, limits)
	if err != nil || replay.SHA256() != wire.ReplayIdentitySHA256 {
		return DurableCancellationAuthorityV1{}, errors.New("durable cancellation replay identity disagrees")
	}
	value, err := NewDurableCancellationAuthorityV1(authority, wire.ChannelEvidence, replay, limits)
	if err != nil {
		return DurableCancellationAuthorityV1{}, err
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return DurableCancellationAuthorityV1{}, err
	}
	return value, nil
}

func AuthorizeCancelledV1(durable DurableCancellationAuthorityV1, expected CancellationAuthorityExpectationV1, disposition ReconciliationDisposition, limits Limits, notAppliedProof ...NotAppliedProofV1) error {
	recovered, err := ParseCanonicalDurableCancellationAuthorityV1(durable.CanonicalJSON(), limits)
	if !durable.valid() || err != nil || recovered.SHA256() != durable.SHA256() ||
		validateCancellationReplayIdentityV1(durable.authority, durable.replayIdentity, limits) != nil {
		return errors.New("CANCELLED requires a prior durable cancellation authority")
	}
	if err := ValidateCancellationAuthorityV1(durable.authority, expected, limits); err != nil {
		return err
	}
	boundary := durable.authority.input.Boundary
	if disposition == ReconciliationApplied {
		return errors.New("APPLIED always defeats cancellation")
	}
	if boundary == CancellationTargetSubmissionUnknown || boundary == CancellationTargetNotApplied {
		boundSubmission := durable.authority.input.SubmissionProof.input.TargetSubmission
		if disposition != ReconciliationNotApplied || len(notAppliedProof) != 1 || !expected.SealedAuthorization.valid() ||
			boundSubmission == nil || ValidateTargetSubmissionV1(expected.SealedAuthorization, *boundSubmission, limits) != nil ||
			expected.SealedAuthorization.Seal().SHA256() != durable.authority.input.SealSHA256 ||
			expected.SealedAuthorization.Commitment().SHA256() != durable.authority.input.CommitmentSHA256 ||
			ValidateNotAppliedProofV1(expected.SealedAuthorization, *boundSubmission, notAppliedProof[0], limits) != nil ||
			notAppliedProof[0].CommitmentSHA256() != durable.authority.input.CommitmentSHA256 {
			return errors.New("submitted cancellation requires exact typed NOT_APPLIED proof")
		}
		if boundary == CancellationTargetSubmissionUnknown && notAppliedProof[0].input.RequestBytes != durable.authority.input.SubmissionProof.input.RequestBytes {
			return errors.New("unknown-submission cancellation changed the reconciled target request byte count")
		}
		if boundary == CancellationTargetNotApplied {
			bound := durable.authority.input.SubmissionProof.input.NotAppliedProof
			if bound == nil || bound.SHA256() != notAppliedProof[0].SHA256() || !bytes.Equal(bound.CanonicalJSON(), notAppliedProof[0].CanonicalJSON()) {
				return errors.New("TARGET_NOT_APPLIED cancellation changed its exact typed proof")
			}
		}
	} else if disposition != ReconciliationNotApplied || len(notAppliedProof) != 0 {
		return errors.New("pre-submit cancellation requires the bound no-admission or zero-request-byte disposition")
	}
	return nil
}

func writeAttemptFromWire(w writeAttemptWire) (WriteAttempt, error) {
	repository, err := NewRepository(w.Repository.Owner, w.Repository.Name)
	if err != nil {
		return WriteAttempt{}, err
	}
	actor, err := actorFromWire(w.Actor)
	if err != nil {
		return WriteAttempt{}, err
	}
	value := WriteAttempt{repository: repository, actor: actor, operation: w.Operation, writeID: w.WriteID, authoritySHA256: w.AuthoritySHA256, readyBindingSHA256: w.ReadyBindingSHA256, policySHA256: w.PolicySHA256, payloadSHA256: w.PayloadSHA256, limitsSHA256: w.LimitsSHA256}
	if !value.structurallyValid() {
		return WriteAttempt{}, errors.New("write attempt wire is invalid")
	}
	canonical, err := value.CanonicalJSON()
	if err != nil {
		return WriteAttempt{}, err
	}
	encoded, _ := json.Marshal(w)
	if !bytes.Equal(canonical, encoded) {
		return WriteAttempt{}, errors.New("write attempt wire is non-canonical")
	}
	return value, nil
}
func cloneCancellationInput(i CancellationAuthorityV1Input) CancellationAuthorityV1Input {
	if i.Attempt != nil {
		v := *i.Attempt
		i.Attempt = &v
	}
	i.CurrentReadyProof = cloneCurrentReadyProof(i.CurrentReadyProof)
	i.SubmissionProof = cloneCancellationSubmissionProof(i.SubmissionProof)
	i.EvidenceRefs = append([]ledger.EvidenceRef(nil), i.EvidenceRefs...)
	return i
}
func cloneCancellationAuthority(c CancellationAuthorityV1) CancellationAuthorityV1 {
	c.input = cloneCancellationInput(c.input)
	c.canonical = append([]byte(nil), c.canonical...)
	return c
}
func equalOptionalAttempt(a, b *WriteAttempt) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

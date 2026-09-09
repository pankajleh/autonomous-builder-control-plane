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
	CurrentReadyProofSHA256   string
	Boundary                  CancellationBoundaryV1
	ReceiptUnixNano           int64
	IngressSequence           int64
	AdmissionSHA256           string
	Attempt                   *WriteAttempt
	SealSHA256                string
	CommitmentSHA256          string
	SubmissionProofKind       CancellationSubmissionProofKindV1
	SubmissionProofSHA256     string
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
	if !validText(input.ProjectID, limits.MaxTextBytes, false) || !validText(input.PlanID, limits.MaxTextBytes, false) || !validText(input.RunID, limits.MaxTextBytes, false) || !validSHA256(input.RepositoryBindingSHA256) || !validSHA256(input.Phase3AuthoritySHA256) || !validSHA256(input.ReadyEventSHA256) || !validOpaqueID(input.ReadyEventID, limits.MaxTextBytes) || input.ReadyRunStateSequence <= 0 || !validSHA256(input.ReadyBindingSHA256) || !validSHA256(input.LedgerPrefixSHA256) || input.LedgerPrefixLength <= 0 || !validSHA256(input.CurrentReadyProofSHA256) || input.ReceiptUnixNano <= 0 || input.IngressSequence <= 0 || !input.Requester.valid(limits) || !validEvidenceRef(input.AuthenticationEvidence) || !validText(input.CancellationPolicyVersion, limits.MaxTextBytes, false) || !validEvidenceRef(input.CancellationPolicySource) || !validSHA256(input.CancellationPolicySHA256) || !validSHA256(input.ScopedGrantSHA256) || !validSHA256(input.AllowDecisionSHA256) || !validOpaqueID(input.SourceRequestID, limits.MaxTextBytes) || !validOpaqueID(input.SourceKind, limits.MaxTextBytes) || !validEvidenceRef(input.RequestEvidence) || !validOpaqueID(input.IngressID, limits.MaxTextBytes) {
		return CancellationAuthorityV1{}, errors.New("cancellation authority identity, principal, policy, READY, or request binding is invalid")
	}
	if len(input.EvidenceRefs) == 0 || canonicalizeEvidence(&input.EvidenceRefs, limits) != nil || !containsEvidence(input.EvidenceRefs, input.AuthenticationEvidence) || !containsEvidence(input.EvidenceRefs, input.CancellationPolicySource) || !containsEvidence(input.EvidenceRefs, input.RequestEvidence) {
		return CancellationAuthorityV1{}, errors.New("cancellation evidence closure is incomplete")
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
	CurrentReadyProofSHA256   string                            `json:"current_ready_proof_sha256"`
	Boundary                  CancellationBoundaryV1            `json:"requested_at_boundary"`
	ReceiptUnixNano           int64                             `json:"receipt_unix_nano"`
	IngressSequence           int64                             `json:"ingress_sequence"`
	AdmissionSHA256           string                            `json:"admission_sha256,omitempty"`
	Attempt                   *writeAttemptWire                 `json:"write_attempt,omitempty"`
	SealSHA256                string                            `json:"seal_sha256,omitempty"`
	CommitmentSHA256          string                            `json:"commitment_sha256,omitempty"`
	SubmissionProofKind       CancellationSubmissionProofKindV1 `json:"submission_proof_kind"`
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
	return cancellationAuthorityWireV1{CancellationAuthoritySchemaV1, id, i.ProjectID, i.PlanID, i.RunID, i.RepositoryBindingSHA256, i.Phase3AuthoritySHA256, i.ReadyEventSHA256, i.ReadyEventID, i.ReadyRunStateSequence, i.ReadyBindingSHA256, i.LedgerPrefixSHA256, i.LedgerPrefixLength, i.CurrentReadyProofSHA256, i.Boundary, i.ReceiptUnixNano, i.IngressSequence, i.AdmissionSHA256, attempt, i.SealSHA256, i.CommitmentSHA256, i.SubmissionProofKind, i.SubmissionProofSHA256, i.Requester, i.AuthenticationEvidence, i.CancellationPolicyVersion, i.CancellationPolicySource, i.CancellationPolicySHA256, i.ScopedGrantSHA256, i.AllowDecisionSHA256, i.SourceRequestID, i.SourceKind, i.RequestEvidence, i.IngressID, i.EvidenceRefs, limitsSHA}
}

func validateCancellationBoundary(i CancellationAuthorityV1Input, limits Limits) error {
	hasAttempt := i.Attempt != nil
	if hasAttempt && (!i.Attempt.valid(limits) || i.Attempt.operation != OperationMerge) {
		return errors.New("cancellation attempt is invalid")
	}
	if !validSHA256(i.SubmissionProofSHA256) {
		return errors.New("cancellation boundary proof digest is invalid")
	}
	switch i.Boundary {
	case CancellationPreAdmission:
		if hasAttempt || i.AdmissionSHA256 != "" || i.SealSHA256 != "" || i.CommitmentSHA256 != "" || i.SubmissionProofKind != CancellationProofNoAdmission {
			return errors.New("pre-admission cancellation contains admitted attempt state")
		}
	case CancellationAdmittedPreTargetSubmission:
		if !hasAttempt || !validSHA256(i.AdmissionSHA256) || i.CommitmentSHA256 != "" || i.SubmissionProofKind != CancellationProofZeroRequestBytes {
			return errors.New("admitted pre-submit cancellation lacks exact zero-byte attempt proof")
		}
	case CancellationTargetSubmissionUnknown:
		if !hasAttempt || !validSHA256(i.AdmissionSHA256) || !validSHA256(i.SealSHA256) || !validSHA256(i.CommitmentSHA256) || i.SubmissionProofKind != CancellationProofUnresolvedSubmission {
			return errors.New("unknown-submission cancellation lacks exact sealed commitment proof")
		}
	case CancellationTargetNotApplied:
		if !hasAttempt || !validSHA256(i.AdmissionSHA256) || !validSHA256(i.SealSHA256) || !validSHA256(i.CommitmentSHA256) || i.SubmissionProofKind != CancellationProofAuthenticatedNotApplied {
			return errors.New("not-applied cancellation lacks authenticated all-or-nothing proof")
		}
	default:
		return errors.New("unsupported cancellation boundary")
	}
	return nil
}

type CancellationAuthorityExpectationV1 struct {
	ProjectID             string
	PlanID                string
	RunID                 string
	ReadyBinding          ReadyAuthorityBindingV1
	PolicySHA256          string
	Requester             StablePrincipalV1
	Boundary              CancellationBoundaryV1
	Attempt               *WriteAttempt
	SealSHA256            string
	CommitmentSHA256      string
	SubmissionProofKind   CancellationSubmissionProofKindV1
	SubmissionProofSHA256 string
	SourceRequestID       string
	IngressID             string
	ReceiptUnixNano       int64
	IngressSequence       int64
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
	if !expected.ReadyBinding.valid() || i.ProjectID != expected.ProjectID || i.PlanID != expected.PlanID || i.RunID != expected.RunID || i.RepositoryBindingSHA256 != expected.ReadyBinding.RepositoryBinding().SHA256() || i.Phase3AuthoritySHA256 != expected.ReadyBinding.input.Phase3AuthoritySHA256 || i.ReadyEventSHA256 != expected.ReadyBinding.input.ReadyEventSHA256 || i.ReadyEventID != expected.ReadyBinding.input.ReadyEventID || i.ReadyRunStateSequence != expected.ReadyBinding.input.ReadyRunStateSequence || i.ReadyBindingSHA256 != expected.ReadyBinding.SHA256() || i.LedgerPrefixSHA256 != expected.ReadyBinding.input.LedgerPrefixSHA256 || i.LedgerPrefixLength != expected.ReadyBinding.input.LedgerPrefixLength || i.CancellationPolicySHA256 != expected.PolicySHA256 || i.Requester != expected.Requester || i.Boundary != expected.Boundary || !equalOptionalAttempt(i.Attempt, expected.Attempt) || i.SealSHA256 != expected.SealSHA256 || i.CommitmentSHA256 != expected.CommitmentSHA256 || i.SubmissionProofKind != expected.SubmissionProofKind || i.SubmissionProofSHA256 != expected.SubmissionProofSHA256 || i.SourceRequestID != expected.SourceRequestID || i.IngressID != expected.IngressID || i.ReceiptUnixNano != expected.ReceiptUnixNano || i.IngressSequence != expected.IngressSequence {
		return errors.New("cancellation authority does not match independent READY, policy, principal, request, attempt, or boundary expectations")
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
	var attempt *WriteAttempt
	if w.Attempt != nil {
		value, err := writeAttemptFromWire(*w.Attempt)
		if err != nil {
			return CancellationAuthorityV1{}, err
		}
		attempt = &value
	}
	input := CancellationAuthorityV1Input{w.ProjectID, w.PlanID, w.RunID, w.RepositoryBindingSHA256, w.Phase3AuthoritySHA256, w.ReadyEventSHA256, w.ReadyEventID, w.ReadyRunStateSequence, w.ReadyBindingSHA256, w.LedgerPrefixSHA256, w.LedgerPrefixLength, w.CurrentReadyProofSHA256, w.Boundary, w.ReceiptUnixNano, w.IngressSequence, w.AdmissionSHA256, attempt, w.SealSHA256, w.CommitmentSHA256, w.SubmissionProofKind, w.SubmissionProofSHA256, w.Requester, w.AuthenticationEvidence, w.CancellationPolicyVersion, w.CancellationPolicySource, w.CancellationPolicySHA256, w.ScopedGrantSHA256, w.AllowDecisionSHA256, w.SourceRequestID, w.SourceKind, w.RequestEvidence, w.IngressID, w.EvidenceRefs}
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
	authority           CancellationAuthorityV1
	channelEvidence     ledger.EvidenceRef
	replayIndexEvidence ledger.EvidenceRef
	canonical           []byte
	digest              string
}

func NewDurableCancellationAuthorityV1(authority CancellationAuthorityV1, channelEvidence, replayIndexEvidence ledger.EvidenceRef, limits Limits) (DurableCancellationAuthorityV1, error) {
	if !authority.valid() || requireLimitsSHA(limits, authority.limitsSHA) != nil || !validEvidenceRef(channelEvidence) || !validEvidenceRef(replayIndexEvidence) {
		return DurableCancellationAuthorityV1{}, errors.New("durable cancellation authority requires validated fsynced channel and replay-index evidence")
	}
	wire := struct {
		Schema              string             `json:"schema"`
		Authority           json.RawMessage    `json:"authority"`
		AuthoritySHA256     string             `json:"authority_sha256"`
		ChannelEvidence     ledger.EvidenceRef `json:"channel_evidence"`
		ReplayIndexEvidence ledger.EvidenceRef `json:"replay_index_evidence"`
	}{DurableCancellationAuthoritySchemaV1, authority.CanonicalJSON(), authority.SHA256(), channelEvidence, replayIndexEvidence}
	canonical, digest, err := canonicalJSON(wire)
	if err != nil {
		return DurableCancellationAuthorityV1{}, err
	}
	return DurableCancellationAuthorityV1{authority, channelEvidence, replayIndexEvidence, canonical, digest}, nil
}
func (d DurableCancellationAuthorityV1) CanonicalJSON() []byte {
	return append([]byte(nil), d.canonical...)
}
func (d DurableCancellationAuthorityV1) SHA256() string { return d.digest }
func (d DurableCancellationAuthorityV1) Authority() CancellationAuthorityV1 {
	return cloneCancellationAuthority(d.authority)
}
func AuthorizeCancelledV1(durable DurableCancellationAuthorityV1, expected CancellationAuthorityExpectationV1, disposition ReconciliationDisposition, limits Limits, notAppliedProof ...NotAppliedProofV1) error {
	if len(durable.canonical) == 0 || digestBytes(durable.canonical) != durable.digest || !validEvidenceRef(durable.channelEvidence) || !validEvidenceRef(durable.replayIndexEvidence) {
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
		if disposition != ReconciliationNotApplied || len(notAppliedProof) != 1 || !notAppliedProof[0].valid(limits) || notAppliedProof[0].CommitmentSHA256 != durable.authority.input.CommitmentSHA256 {
			return errors.New("submitted cancellation requires exact typed NOT_APPLIED proof")
		}
	} else if len(notAppliedProof) != 0 {
		return errors.New("pre-submit cancellation cannot adopt target reconciliation proof")
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

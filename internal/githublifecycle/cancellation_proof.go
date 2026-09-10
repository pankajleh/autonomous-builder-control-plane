package githublifecycle

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const CancellationSubmissionProofSchemaV1 = "cancellation-submission-proof-v1"

type CancellationSubmissionProofV1Input struct {
	Kind             CancellationSubmissionProofKindV1
	AdmissionSHA256  string
	Attempt          *WriteAttempt
	SealSHA256       string
	CommitmentSHA256 string
	RequestBytes     int64
	SubmissionState  ReconciliationDisposition
	NotAppliedProof  *NotAppliedProofV1
	EvidenceRef      ledger.EvidenceRef
}

type CancellationSubmissionProofV1 struct {
	input     CancellationSubmissionProofV1Input
	canonical []byte
	digest    string
	limitsSHA string
}

type cancellationSubmissionProofWireV1 struct {
	Schema                string                            `json:"schema"`
	Kind                  CancellationSubmissionProofKindV1 `json:"kind"`
	AdmissionSHA256       string                            `json:"admission_sha256,omitempty"`
	Attempt               *writeAttemptWire                 `json:"write_attempt,omitempty"`
	SealSHA256            string                            `json:"seal_sha256,omitempty"`
	CommitmentSHA256      string                            `json:"commitment_sha256,omitempty"`
	RequestBytes          int64                             `json:"request_bytes"`
	SubmissionState       ReconciliationDisposition         `json:"submission_state"`
	NotAppliedProof       json.RawMessage                   `json:"not_applied_proof,omitempty"`
	NotAppliedProofSHA256 string                            `json:"not_applied_proof_sha256,omitempty"`
	EvidenceRef           ledger.EvidenceRef                `json:"evidence_ref"`
	LimitsSHA256          string                            `json:"limits_sha256"`
}

func NewCancellationSubmissionProofV1(input CancellationSubmissionProofV1Input, limits Limits) (CancellationSubmissionProofV1, error) {
	input = cloneCancellationSubmissionProofInput(input)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return CancellationSubmissionProofV1{}, err
	}
	if !validEvidenceRef(input.EvidenceRef) || input.RequestBytes < 0 {
		return CancellationSubmissionProofV1{}, errors.New("cancellation submission proof evidence or byte count is invalid")
	}
	hasAttempt := input.Attempt != nil
	if hasAttempt && (!input.Attempt.valid(limits) || input.Attempt.operation != OperationMerge) {
		return CancellationSubmissionProofV1{}, errors.New("cancellation submission proof attempt is invalid")
	}
	switch input.Kind {
	case CancellationProofNoAdmission:
		if hasAttempt || input.AdmissionSHA256 != "" || input.SealSHA256 != "" || input.CommitmentSHA256 != "" ||
			input.RequestBytes != 0 || input.SubmissionState != ReconciliationNotApplied || input.NotAppliedProof != nil {
			return CancellationSubmissionProofV1{}, errors.New("no-admission proof contains admitted or submitted state")
		}
	case CancellationProofZeroRequestBytes:
		if !hasAttempt || !validSHA256(input.AdmissionSHA256) || input.SealSHA256 != "" || input.CommitmentSHA256 != "" ||
			input.RequestBytes != 0 || input.SubmissionState != ReconciliationNotApplied || input.NotAppliedProof != nil {
			return CancellationSubmissionProofV1{}, errors.New("zero-byte proof does not bind exactly one admitted unsubmitted attempt")
		}
	case CancellationProofSealedZeroRequestBytes:
		if !hasAttempt || !validSHA256(input.AdmissionSHA256) || !validSHA256(input.SealSHA256) || !validSHA256(input.CommitmentSHA256) ||
			input.RequestBytes != 0 || input.SubmissionState != ReconciliationNotApplied || input.NotAppliedProof == nil ||
			!input.NotAppliedProof.valid() || requireLimitsSHA(limits, input.NotAppliedProof.limitsSHA) != nil || input.NotAppliedProof.input.Kind != NotAppliedZeroRequestBytes ||
			input.NotAppliedProof.CommitmentSHA256() != input.CommitmentSHA256 || input.NotAppliedProof.input.RequestBytes != 0 ||
			input.NotAppliedProof.input.EvidenceRef != input.EvidenceRef {
			return CancellationSubmissionProofV1{}, errors.New("sealed zero-byte proof does not bind the exact committed unsubmitted attempt")
		}
	case CancellationProofUnresolvedSubmission:
		if !hasAttempt || !validSHA256(input.AdmissionSHA256) || !validSHA256(input.SealSHA256) || !validSHA256(input.CommitmentSHA256) ||
			input.RequestBytes <= 0 || input.SubmissionState != ReconciliationUnknown || input.NotAppliedProof != nil {
			return CancellationSubmissionProofV1{}, errors.New("unresolved proof does not bind the exact sealed submitted attempt")
		}
	case CancellationProofAuthenticatedNotApplied:
		if !hasAttempt || !validSHA256(input.AdmissionSHA256) || !validSHA256(input.SealSHA256) || !validSHA256(input.CommitmentSHA256) ||
			input.RequestBytes <= 0 || input.SubmissionState != ReconciliationNotApplied || input.NotAppliedProof == nil ||
			!input.NotAppliedProof.valid() || requireLimitsSHA(limits, input.NotAppliedProof.limitsSHA) != nil || input.NotAppliedProof.CommitmentSHA256() != input.CommitmentSHA256 ||
			input.NotAppliedProof.input.RequestBytes != input.RequestBytes || input.NotAppliedProof.input.EvidenceRef != input.EvidenceRef {
			return CancellationSubmissionProofV1{}, errors.New("authenticated NOT_APPLIED proof does not bind the exact sealed submitted attempt")
		}
	default:
		return CancellationSubmissionProofV1{}, errors.New("unsupported cancellation submission proof kind")
	}
	wire := cancellationSubmissionProofWire(input, limitsSHA)
	canonical, digest, err := canonicalJSON(wire)
	if err != nil {
		return CancellationSubmissionProofV1{}, err
	}
	return CancellationSubmissionProofV1{input, canonical, digest, limitsSHA}, nil
}

func cancellationSubmissionProofWire(input CancellationSubmissionProofV1Input, limitsSHA string) cancellationSubmissionProofWireV1 {
	var attempt *writeAttemptWire
	if input.Attempt != nil {
		value := attemptWire(*input.Attempt)
		attempt = &value
	}
	var notApplied json.RawMessage
	var notAppliedSHA string
	if input.NotAppliedProof != nil {
		notApplied = input.NotAppliedProof.CanonicalJSON()
		notAppliedSHA = input.NotAppliedProof.SHA256()
	}
	return cancellationSubmissionProofWireV1{
		CancellationSubmissionProofSchemaV1, input.Kind, input.AdmissionSHA256, attempt, input.SealSHA256, input.CommitmentSHA256,
		input.RequestBytes, input.SubmissionState, notApplied, notAppliedSHA, input.EvidenceRef, limitsSHA,
	}
}

func (p CancellationSubmissionProofV1) Input() CancellationSubmissionProofV1Input {
	return cloneCancellationSubmissionProofInput(p.input)
}
func (p CancellationSubmissionProofV1) CanonicalJSON() []byte {
	return append([]byte(nil), p.canonical...)
}
func (p CancellationSubmissionProofV1) SHA256() string { return p.digest }
func (p CancellationSubmissionProofV1) valid() bool {
	return len(p.canonical) > 0 && validSHA256(p.digest) && digestBytes(p.canonical) == p.digest && validSHA256(p.limitsSHA)
}

func ParseCanonicalCancellationSubmissionProofV1(data []byte, limits Limits) (CancellationSubmissionProofV1, error) {
	var wire cancellationSubmissionProofWireV1
	if err := strictDecode(data, &wire); err != nil {
		return CancellationSubmissionProofV1{}, err
	}
	if wire.Schema != CancellationSubmissionProofSchemaV1 {
		return CancellationSubmissionProofV1{}, errors.New("unsupported cancellation submission proof schema")
	}
	var attempt *WriteAttempt
	if wire.Attempt != nil {
		value, err := writeAttemptFromWire(*wire.Attempt)
		if err != nil {
			return CancellationSubmissionProofV1{}, err
		}
		attempt = &value
	}
	var notApplied *NotAppliedProofV1
	if len(wire.NotAppliedProof) > 0 {
		value, err := parseUnboundNotAppliedProof(wire.NotAppliedProof, limits)
		if err != nil {
			return CancellationSubmissionProofV1{}, err
		}
		notApplied = &value
	}
	value, err := NewCancellationSubmissionProofV1(CancellationSubmissionProofV1Input{
		Kind: wire.Kind, AdmissionSHA256: wire.AdmissionSHA256, Attempt: attempt, SealSHA256: wire.SealSHA256,
		CommitmentSHA256: wire.CommitmentSHA256, RequestBytes: wire.RequestBytes, SubmissionState: wire.SubmissionState,
		NotAppliedProof: notApplied, EvidenceRef: wire.EvidenceRef,
	}, limits)
	if err != nil {
		return CancellationSubmissionProofV1{}, err
	}
	if wire.NotAppliedProofSHA256 != func() string {
		if notApplied == nil {
			return ""
		}
		return notApplied.SHA256()
	}() || wire.LimitsSHA256 != value.limitsSHA {
		return CancellationSubmissionProofV1{}, errors.New("cancellation submission proof nested digest or limits disagree")
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return CancellationSubmissionProofV1{}, err
	}
	return value, nil
}

func parseUnboundNotAppliedProof(data []byte, limits Limits) (NotAppliedProofV1, error) {
	var wire notAppliedProofWireV1
	if err := strictDecode(data, &wire); err != nil {
		return NotAppliedProofV1{}, err
	}
	if wire.Schema != NotAppliedProofSchemaV1 || wire.LimitsSHA256 == "" {
		return NotAppliedProofV1{}, errors.New("invalid nested NOT_APPLIED proof")
	}
	var response *SnapshotIdentity
	if wire.Response != nil {
		value, err := NewSnapshotIdentity(wire.Response.Provider, wire.Response.RequestID, wire.Response.ObservedUnixNano)
		if err != nil {
			return NotAppliedProofV1{}, err
		}
		response = &value
	}
	canonical, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(canonical, data) {
		return NotAppliedProofV1{}, errors.New("nested NOT_APPLIED proof is not canonical")
	}
	input := NotAppliedProofV1Input{
		Kind: wire.Kind, RequestID: wire.RequestID, RequestBodySHA256: wire.RequestBodySHA256, RequestBytes: wire.RequestBytes,
		Response: response, HTTPStatus: wire.HTTPStatus, ResponseBodySHA256: wire.ResponseBodySHA256, ResponseBody: wire.ResponseBody, EvidenceRef: wire.EvidenceRef,
	}
	return NotAppliedProofV1{input: input, canonical: append([]byte(nil), data...), digest: digestBytes(data), limitsSHA: wire.LimitsSHA256}, nil
}

func cloneCancellationSubmissionProofInput(input CancellationSubmissionProofV1Input) CancellationSubmissionProofV1Input {
	if input.Attempt != nil {
		value := *input.Attempt
		input.Attempt = &value
	}
	if input.NotAppliedProof != nil {
		value := cloneNotAppliedProof(*input.NotAppliedProof)
		input.NotAppliedProof = &value
	}
	return input
}

func cloneCancellationSubmissionProof(proof CancellationSubmissionProofV1) CancellationSubmissionProofV1 {
	proof.input = cloneCancellationSubmissionProofInput(proof.input)
	proof.canonical = append([]byte(nil), proof.canonical...)
	return proof
}

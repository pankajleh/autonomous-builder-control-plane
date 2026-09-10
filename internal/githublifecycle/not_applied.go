package githublifecycle

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const NotAppliedProofSchemaV1 = "not-applied-proof-v1"

type NotAppliedProofKindV1 string

const (
	NotAppliedZeroRequestBytes   NotAppliedProofKindV1 = "zero_request_bytes"
	NotAppliedAtomicBaseRejected NotAppliedProofKindV1 = "atomic_base_before_oid_rejected"
	NotAppliedAtomicHeadRejected NotAppliedProofKindV1 = "atomic_head_before_oid_rejected"
)

const NotAppliedAllOrNothingDispositionV1 = "all_or_nothing_not_applied"

const NotAppliedBeforeOIDMismatchCodeV1 = "UPDATE_REFS_BEFORE_OID_MISMATCH"

const (
	NotAppliedZeroByteEvidenceKindV1        = "not-applied-zero-byte-proof"
	NotAppliedAtomicRejectionEvidenceKindV1 = "github-update-refs-atomic-rejection"
)

type NotAppliedProofV1Input struct {
	Kind               NotAppliedProofKindV1
	RequestBytes       int64
	Response           *SnapshotIdentity
	HTTPStatus         int
	ResponseBodySHA256 string
	ResponseBody       []byte
	EvidenceRef        ledger.EvidenceRef
}

type NotAppliedProofV1 struct {
	input      NotAppliedProofV1Input
	submission TargetSubmissionV1
	canonical  []byte
	digest     string
	limitsSHA  string
}

type notAppliedProofWireV1 struct {
	Schema                  string                `json:"schema"`
	Kind                    NotAppliedProofKindV1 `json:"kind"`
	Repository              repoWire              `json:"repository"`
	RepositoryNodeID        string                `json:"repository_node_id"`
	RefUpdates              []refUpdateWireV1     `json:"ref_updates"`
	RejectedPredicate       string                `json:"rejected_predicate"`
	Capability              json.RawMessage       `json:"capability"`
	CapabilitySHA256        string                `json:"capability_sha256"`
	AuthorizationSealSHA256 string                `json:"authorization_seal_sha256"`
	CommitmentSHA256        string                `json:"commitment_sha256"`
	WriteID                 string                `json:"write_id"`
	ClientMutationID        string                `json:"client_mutation_id"`
	TargetSubmission        json.RawMessage       `json:"target_submission"`
	TargetSubmissionSHA256  string                `json:"target_submission_sha256"`
	RequestBytes            int64                 `json:"request_bytes"`
	Response                *identityWire         `json:"response,omitempty"`
	HTTPStatus              int                   `json:"http_status,omitempty"`
	ResponseBodySHA256      string                `json:"response_body_sha256,omitempty"`
	ResponseBody            json.RawMessage       `json:"response_body,omitempty"`
	RawEvidenceSHA256       string                `json:"raw_evidence_sha256"`
	EvidenceRef             ledger.EvidenceRef    `json:"evidence_ref"`
	AllOrNothing            bool                  `json:"all_or_nothing"`
	Disposition             string                `json:"disposition"`
	LimitsSHA256            string                `json:"limits_sha256"`
}

func NewNotAppliedProofV1(input NotAppliedProofV1Input, sealed SealedMergeAuthorizationV1, submission TargetSubmissionV1, limits Limits) (NotAppliedProofV1, error) {
	input = cloneNotAppliedInput(input)
	submission = cloneTargetSubmission(submission)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return NotAppliedProofV1{}, err
	}
	if !sealed.valid() {
		return NotAppliedProofV1{}, errors.New("NOT_APPLIED proof requires the full sealed authorization")
	}
	if _, err := ParseCanonicalSealedMergeAuthorizationV1(sealed.CanonicalJSON(), limits); err != nil {
		return NotAppliedProofV1{}, errors.New("NOT_APPLIED proof sealed authorization fails independent validation")
	}
	if ValidateTargetSubmissionV1(sealed, submission, limits) != nil || !validEvidenceRef(input.EvidenceRef) {
		return NotAppliedProofV1{}, errors.New("NOT_APPLIED request or raw evidence identity is invalid")
	}
	switch input.Kind {
	case NotAppliedZeroRequestBytes:
		if input.EvidenceRef.Kind != NotAppliedZeroByteEvidenceKindV1 || input.RequestBytes != 0 || input.Response != nil || input.HTTPStatus != 0 || input.ResponseBodySHA256 != "" || len(input.ResponseBody) != 0 {
			return NotAppliedProofV1{}, errors.New("zero-byte NOT_APPLIED proof contains submitted request or response state")
		}
	case NotAppliedAtomicBaseRejected, NotAppliedAtomicHeadRejected:
		if input.EvidenceRef.Kind != NotAppliedAtomicRejectionEvidenceKindV1 || input.RequestBytes <= 0 || input.RequestBytes > int64(limits.MaxPaginationClosureBytes) || input.Response == nil || !input.Response.valid() ||
			input.Response.Provider() != "github" || input.Response.RequestID() != submission.requestID ||
			input.Response.ObservedUnixNano() < sealed.input.Seal.input.FinalRevalidation.input.CompletedUnixNano ||
			input.HTTPStatus != 200 || len(input.ResponseBody) == 0 || len(input.ResponseBody) > limits.MaxPaginationClosureBytes ||
			!validSHA256(input.ResponseBodySHA256) || input.ResponseBodySHA256 != digestBytes(input.ResponseBody) ||
			input.EvidenceRef.SHA256 != input.ResponseBodySHA256 {
			return NotAppliedProofV1{}, errors.New("atomic rejection proof lacks authenticated request/response evidence")
		}
		kind, err := parseAtomicRejectionResponse(input.ResponseBody)
		if err != nil || kind != input.Kind {
			return NotAppliedProofV1{}, errors.New("atomic rejection response does not prove the claimed before-OID predicate")
		}
	default:
		return NotAppliedProofV1{}, errors.New("unsupported NOT_APPLIED proof kind")
	}
	wire := notAppliedProofWire(input, sealed, submission, limitsSHA)
	canonical, digest, err := canonicalJSON(wire)
	if err != nil {
		return NotAppliedProofV1{}, err
	}
	return NotAppliedProofV1{input, submission, canonical, digest, limitsSHA}, nil
}

func notAppliedProofWire(input NotAppliedProofV1Input, sealed SealedMergeAuthorizationV1, submission TargetSubmissionV1, limitsSHA string) notAppliedProofWireV1 {
	commitment := sealed.input.Commitment
	updates := []refUpdateWireV1{refUpdateWire(commitment.updates[0]), refUpdateWire(commitment.updates[1])}
	predicate := "request_bytes_equal_zero"
	if input.Kind == NotAppliedAtomicBaseRejected {
		predicate = "ref_updates[0].before_oid_mismatch"
	} else if input.Kind == NotAppliedAtomicHeadRejected {
		predicate = "ref_updates[1].before_oid_mismatch"
	}
	var response *identityWire
	if input.Response != nil {
		wire := snapshotWire(*input.Response)
		response = &wire
	}
	return notAppliedProofWireV1{
		NotAppliedProofSchemaV1, input.Kind, repositoryWire(sealed.input.MergeInput.authority.Repository()), commitment.repositoryNodeID,
		updates, predicate, sealed.input.MergeInput.capability.CanonicalJSON(), sealed.input.MergeInput.capability.SHA256(),
		sealed.input.Seal.SHA256(), commitment.SHA256(), sealed.input.MergeInput.attempt.WriteID(), commitment.clientMutationID,
		submission.CanonicalJSON(), submission.SHA256(), input.RequestBytes, response, input.HTTPStatus, input.ResponseBodySHA256,
		input.ResponseBody, input.EvidenceRef.SHA256, input.EvidenceRef, true, NotAppliedAllOrNothingDispositionV1, limitsSHA,
	}
}

func (p NotAppliedProofV1) Input() NotAppliedProofV1Input { return cloneNotAppliedInput(p.input) }
func (p NotAppliedProofV1) CanonicalJSON() []byte         { return append([]byte(nil), p.canonical...) }
func (p NotAppliedProofV1) SHA256() string                { return p.digest }
func (p NotAppliedProofV1) TargetSubmission() TargetSubmissionV1 {
	return cloneTargetSubmission(p.submission)
}
func (p NotAppliedProofV1) CommitmentSHA256() string {
	if !p.valid() {
		return ""
	}
	var wire notAppliedProofWireV1
	_ = json.Unmarshal(p.canonical, &wire)
	return wire.CommitmentSHA256
}
func (p NotAppliedProofV1) valid() bool {
	return len(p.canonical) > 0 && validSHA256(p.digest) && digestBytes(p.canonical) == p.digest && validSHA256(p.limitsSHA)
}

func ValidateNotAppliedProofV1(sealed SealedMergeAuthorizationV1, submission TargetSubmissionV1, proof NotAppliedProofV1, limits Limits) error {
	if !proof.valid() || requireLimitsSHA(limits, proof.limitsSHA) != nil {
		return errors.New("NOT_APPLIED proof is incomplete or uses different limits")
	}
	if proof.submission.SHA256() != submission.SHA256() || !bytes.Equal(proof.submission.CanonicalJSON(), submission.CanonicalJSON()) {
		return errors.New("NOT_APPLIED proof does not match the independently supplied target submission")
	}
	rebuilt, err := NewNotAppliedProofV1(proof.input, sealed, submission, limits)
	if err != nil || rebuilt.digest != proof.digest || !bytes.Equal(rebuilt.canonical, proof.canonical) {
		return errors.New("NOT_APPLIED proof fails independent sealed-input validation")
	}
	return nil
}

func ParseCanonicalNotAppliedProofV1(data []byte, sealed SealedMergeAuthorizationV1, submission TargetSubmissionV1, limits Limits) (NotAppliedProofV1, error) {
	var wire notAppliedProofWireV1
	if err := strictDecode(data, &wire); err != nil {
		return NotAppliedProofV1{}, err
	}
	if wire.Schema != NotAppliedProofSchemaV1 {
		return NotAppliedProofV1{}, errors.New("unsupported NOT_APPLIED proof schema")
	}
	var response *SnapshotIdentity
	if wire.Response != nil {
		value, err := NewSnapshotIdentity(wire.Response.Provider, wire.Response.RequestID, wire.Response.ObservedUnixNano)
		if err != nil {
			return NotAppliedProofV1{}, err
		}
		response = &value
	}
	parsedSubmission, err := ParseCanonicalTargetSubmissionV1(wire.TargetSubmission, sealed, limits)
	if err != nil || parsedSubmission.SHA256() != wire.TargetSubmissionSHA256 || parsedSubmission.SHA256() != submission.SHA256() ||
		!bytes.Equal(parsedSubmission.CanonicalJSON(), submission.CanonicalJSON()) {
		return NotAppliedProofV1{}, errors.New("NOT_APPLIED proof target submission identity disagrees")
	}
	value, err := NewNotAppliedProofV1(NotAppliedProofV1Input{
		Kind: wire.Kind, RequestBytes: wire.RequestBytes,
		Response: response, HTTPStatus: wire.HTTPStatus, ResponseBodySHA256: wire.ResponseBodySHA256, ResponseBody: wire.ResponseBody, EvidenceRef: wire.EvidenceRef,
	}, sealed, submission, limits)
	if err != nil {
		return NotAppliedProofV1{}, err
	}
	if wire.RawEvidenceSHA256 != wire.EvidenceRef.SHA256 {
		return NotAppliedProofV1{}, errors.New("NOT_APPLIED proof derived repository, refs, predicate, capability, or disposition disagrees")
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return NotAppliedProofV1{}, err
	}
	return value, nil
}

func cloneNotAppliedInput(input NotAppliedProofV1Input) NotAppliedProofV1Input {
	if input.Response != nil {
		value := *input.Response
		input.Response = &value
	}
	input.ResponseBody = append([]byte(nil), input.ResponseBody...)
	return input
}

type atomicRejectionResponseV1 struct {
	Data struct {
		UpdateRefs json.RawMessage `json:"updateRefs"`
	} `json:"data"`
	Errors []struct {
		Type       string   `json:"type"`
		Path       []string `json:"path"`
		Extensions struct {
			Code           string `json:"code"`
			RefUpdateIndex int    `json:"ref_update_index"`
		} `json:"extensions"`
	} `json:"errors"`
}

func parseAtomicRejectionResponse(data []byte) (NotAppliedProofKindV1, error) {
	var response atomicRejectionResponseV1
	if err := strictDecode(data, &response); err != nil || len(response.Errors) != 1 ||
		!bytes.Equal(response.Data.UpdateRefs, []byte("null")) || response.Errors[0].Type != "FAILED_PRECONDITION" ||
		response.Errors[0].Extensions.Code != NotAppliedBeforeOIDMismatchCodeV1 ||
		!equalStrings(response.Errors[0].Path, []string{"updateRefs", "refUpdates", "beforeOid"}) {
		return "", errors.New("atomic rejection response is malformed or unsupported")
	}
	canonical, _ := json.Marshal(response)
	if !bytes.Equal(canonical, data) {
		return "", errors.New("atomic rejection response is not canonical")
	}
	switch response.Errors[0].Extensions.RefUpdateIndex {
	case 0:
		return NotAppliedAtomicBaseRejected, nil
	case 1:
		return NotAppliedAtomicHeadRejected, nil
	default:
		return "", errors.New("atomic rejection response names an unknown ref update")
	}
}

func equalStrings(left, right []string) bool {
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

func cloneNotAppliedProof(proof NotAppliedProofV1) NotAppliedProofV1 {
	proof.input = cloneNotAppliedInput(proof.input)
	proof.submission = cloneTargetSubmission(proof.submission)
	proof.canonical = append([]byte(nil), proof.canonical...)
	return proof
}

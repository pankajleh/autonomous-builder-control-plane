package githublifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
)

const TargetSubmissionSchemaV1 = "target-submission-v1"

// TargetSubmissionV1 is the immutable identity of the one transport request
// made for a sealed target-ref commitment. It is published before transport
// invocation and later supplied independently to reconciliation.
type TargetSubmissionV1 struct {
	sealedSHA         string
	sealSHA           string
	commitmentSHA     string
	writeID           string
	clientMutationID  string
	requestID         string
	requestBody       []byte
	requestBodySHA256 string
	requestBodyBytes  int64
	canonical         []byte
	digest            string
	limitsSHA         string
}

type targetSubmissionWireV1 struct {
	Schema                  string          `json:"schema"`
	SealedAuthorizationSHA  string          `json:"sealed_authorization_sha256"`
	AuthorizationSealSHA256 string          `json:"authorization_seal_sha256"`
	CommitmentSHA256        string          `json:"commitment_sha256"`
	WriteID                 string          `json:"write_id"`
	ClientMutationID        string          `json:"client_mutation_id"`
	RequestID               string          `json:"request_id"`
	RequestBody             json.RawMessage `json:"request_body"`
	RequestBodySHA256       string          `json:"request_body_sha256"`
	RequestBodyBytes        int64           `json:"request_body_bytes"`
	LimitsSHA256            string          `json:"limits_sha256"`
}

func NewTargetSubmissionV1(requestID string, sealed SealedMergeAuthorizationV1, limits Limits) (TargetSubmissionV1, error) {
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return TargetSubmissionV1{}, err
	}
	if !sealed.valid() {
		return TargetSubmissionV1{}, errors.New("target submission requires the full sealed authorization")
	}
	if _, err := ParseCanonicalSealedMergeAuthorizationV1(sealed.CanonicalJSON(), limits); err != nil {
		return TargetSubmissionV1{}, errors.New("target submission sealed authorization fails independent validation")
	}
	if !validOpaqueID(requestID, limits.MaxTextBytes) {
		return TargetSubmissionV1{}, errors.New("target submission request identity is invalid")
	}
	body := sealed.input.Commitment.CanonicalJSON()
	if len(body) == 0 || len(body) > limits.MaxPaginationClosureBytes {
		return TargetSubmissionV1{}, errors.New("target submission request body is invalid or unbounded")
	}
	wire := targetSubmissionWire(sealed, requestID, body, limitsSHA)
	canonical, digest, err := canonicalJSON(wire)
	if err != nil {
		return TargetSubmissionV1{}, err
	}
	return TargetSubmissionV1{
		sealedSHA: sealed.SHA256(), sealSHA: sealed.input.Seal.SHA256(), commitmentSHA: sealed.input.Commitment.SHA256(),
		writeID: sealed.input.MergeInput.attempt.WriteID(), clientMutationID: sealed.input.Commitment.clientMutationID,
		requestID: requestID, requestBody: append([]byte(nil), body...), requestBodySHA256: digestBytes(body),
		requestBodyBytes: int64(len(body)), canonical: canonical, digest: digest, limitsSHA: limitsSHA,
	}, nil
}

func targetSubmissionWire(sealed SealedMergeAuthorizationV1, requestID string, body []byte, limitsSHA string) targetSubmissionWireV1 {
	commitment := sealed.input.Commitment
	return targetSubmissionWireV1{
		Schema: TargetSubmissionSchemaV1, SealedAuthorizationSHA: sealed.SHA256(), AuthorizationSealSHA256: sealed.input.Seal.SHA256(),
		CommitmentSHA256: commitment.SHA256(), WriteID: sealed.input.MergeInput.attempt.WriteID(), ClientMutationID: commitment.clientMutationID,
		RequestID: requestID, RequestBody: append(json.RawMessage(nil), body...), RequestBodySHA256: digestBytes(body),
		RequestBodyBytes: int64(len(body)), LimitsSHA256: limitsSHA,
	}
}

func (s TargetSubmissionV1) RequestID() string         { return s.requestID }
func (s TargetSubmissionV1) RequestBody() []byte       { return append([]byte(nil), s.requestBody...) }
func (s TargetSubmissionV1) RequestBodySHA256() string { return s.requestBodySHA256 }
func (s TargetSubmissionV1) RequestBodyBytes() int64   { return s.requestBodyBytes }
func (s TargetSubmissionV1) CanonicalJSON() []byte     { return append([]byte(nil), s.canonical...) }
func (s TargetSubmissionV1) SHA256() string            { return s.digest }
func (s TargetSubmissionV1) MarshalJSON() ([]byte, error) {
	if !s.valid() {
		return nil, errors.New("target submission is incomplete")
	}
	return s.CanonicalJSON(), nil
}
func (s TargetSubmissionV1) valid() bool {
	if !validSHA256(s.sealedSHA) || !validSHA256(s.sealSHA) || !validSHA256(s.commitmentSHA) || s.writeID == "" ||
		s.clientMutationID != s.writeID || s.requestID == "" || len(s.requestBody) == 0 || !validSHA256(s.requestBodySHA256) ||
		digestBytes(s.requestBody) != s.requestBodySHA256 || int64(len(s.requestBody)) != s.requestBodyBytes ||
		len(s.canonical) == 0 || !validSHA256(s.digest) || digestBytes(s.canonical) != s.digest || !validSHA256(s.limitsSHA) {
		return false
	}
	var wire targetSubmissionWireV1
	if json.Unmarshal(s.canonical, &wire) != nil {
		return false
	}
	return wire.Schema == TargetSubmissionSchemaV1 && wire.SealedAuthorizationSHA == s.sealedSHA &&
		wire.AuthorizationSealSHA256 == s.sealSHA && wire.CommitmentSHA256 == s.commitmentSHA && wire.WriteID == s.writeID &&
		wire.ClientMutationID == s.clientMutationID && wire.RequestID == s.requestID && bytes.Equal(wire.RequestBody, s.requestBody) &&
		wire.RequestBodySHA256 == s.requestBodySHA256 && wire.RequestBodyBytes == s.requestBodyBytes &&
		wire.LimitsSHA256 == s.limitsSHA
}

func ValidateTargetSubmissionV1(sealed SealedMergeAuthorizationV1, submission TargetSubmissionV1, limits Limits) error {
	if !submission.valid() || requireLimitsSHA(limits, submission.limitsSHA) != nil {
		return errors.New("target submission is incomplete or uses different limits")
	}
	rebuilt, err := NewTargetSubmissionV1(submission.requestID, sealed, limits)
	if err != nil || rebuilt.digest != submission.digest || !bytes.Equal(rebuilt.canonical, submission.canonical) {
		return errors.New("target submission does not match the exact sealed request")
	}
	return nil
}

func ParseCanonicalTargetSubmissionV1(data []byte, sealed SealedMergeAuthorizationV1, limits Limits) (TargetSubmissionV1, error) {
	value, err := parseUnboundTargetSubmissionV1(data, limits)
	if err != nil {
		return TargetSubmissionV1{}, err
	}
	if err := ValidateTargetSubmissionV1(sealed, value, limits); err != nil {
		return TargetSubmissionV1{}, err
	}
	return value, nil
}

func parseUnboundTargetSubmissionV1(data []byte, limits Limits) (TargetSubmissionV1, error) {
	var wire targetSubmissionWireV1
	if err := strictDecode(data, &wire); err != nil {
		return TargetSubmissionV1{}, err
	}
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return TargetSubmissionV1{}, err
	}
	if wire.Schema != TargetSubmissionSchemaV1 || !validSHA256(wire.SealedAuthorizationSHA) ||
		!validSHA256(wire.AuthorizationSealSHA256) || !validSHA256(wire.CommitmentSHA256) ||
		!validOpaqueID(wire.WriteID, limits.MaxTextBytes) || wire.ClientMutationID != wire.WriteID ||
		!validOpaqueID(wire.RequestID, limits.MaxTextBytes) || len(wire.RequestBody) == 0 || len(wire.RequestBody) > limits.MaxPaginationClosureBytes ||
		wire.RequestBodySHA256 != digestBytes(wire.RequestBody) || wire.RequestBodyBytes != int64(len(wire.RequestBody)) ||
		wire.LimitsSHA256 != limitsSHA {
		return TargetSubmissionV1{}, errors.New("target submission wire is invalid or unbounded")
	}
	canonical, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(canonical, data) {
		return TargetSubmissionV1{}, errors.New("target submission is not canonical")
	}
	return TargetSubmissionV1{
		sealedSHA: wire.SealedAuthorizationSHA, sealSHA: wire.AuthorizationSealSHA256, commitmentSHA: wire.CommitmentSHA256,
		writeID: wire.WriteID, clientMutationID: wire.ClientMutationID, requestID: wire.RequestID,
		requestBody: append([]byte(nil), wire.RequestBody...), requestBodySHA256: wire.RequestBodySHA256,
		requestBodyBytes: wire.RequestBodyBytes, canonical: append([]byte(nil), data...),
		digest: digestBytes(data), limitsSHA: wire.LimitsSHA256,
	}, nil
}

func cloneTargetSubmission(submission TargetSubmissionV1) TargetSubmissionV1 {
	submission.requestBody = append([]byte(nil), submission.requestBody...)
	submission.canonical = append([]byte(nil), submission.canonical...)
	return submission
}

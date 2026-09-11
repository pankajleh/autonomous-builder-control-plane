package githublifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	TargetSubmissionSchemaV1                   = "target-submission-v1"
	TargetResponseEnvelopeSchemaV1             = "github-target-response-envelope-v1"
	GitHubTargetResponseBodyEvidenceKindV1     = "github-target-response-body"
	GitHubTargetResponseEnvelopeEvidenceKindV1 = "github-target-response-envelope"
	GitHubUpdateRefsDocumentV1                 = "mutation UpdateRefs($input:UpdateRefsInput!){updateRefs(input:$input){clientMutationId}}"
	GitHubGraphQLMethodV1                      = "POST"
	GitHubGraphQLPathV1                        = "/graphql"
	MaxGitHubTargetRequestBodyBytesV1          = 16 * 1024
	MaxGitHubTargetResponseBodyBytesV1         = 16 * 1024
)

// TargetSubmissionV1 is the immutable identity of the one transport request
// made for a sealed target-ref commitment. It is published before transport
// invocation and later supplied independently to reconciliation.
type TargetSubmissionV1 struct {
	sealedSHA         string
	sealSHA           string
	commitmentSHA     string
	writeID           string
	clientMutationID  string
	invocationID      string
	method            string
	path              string
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
	InvocationID            string          `json:"invocation_id"`
	Method                  string          `json:"method"`
	Path                    string          `json:"path"`
	RequestBody             json.RawMessage `json:"request_body"`
	RequestBodySHA256       string          `json:"request_body_sha256"`
	RequestBodyBytes        int64           `json:"request_body_bytes"`
	LimitsSHA256            string          `json:"limits_sha256"`
}

type gitHubUpdateRefsRequestV1 struct {
	Query     string                      `json:"query"`
	Variables gitHubUpdateRefsVariablesV1 `json:"variables"`
}

type gitHubUpdateRefsVariablesV1 struct {
	Input gitHubUpdateRefsInputV1 `json:"input"`
}

type gitHubUpdateRefsInputV1 struct {
	ClientMutationID string                   `json:"clientMutationId"`
	RefUpdates       []gitHubRefUpdateInputV1 `json:"refUpdates"`
	RepositoryID     string                   `json:"repositoryId"`
}

type gitHubRefUpdateInputV1 struct {
	AfterOID  string `json:"afterOid"`
	BeforeOID string `json:"beforeOid"`
	Force     bool   `json:"force"`
	Name      string `json:"name"`
}

func NewTargetSubmissionV1(invocationID string, sealed SealedMergeAuthorizationV1, limits Limits) (TargetSubmissionV1, error) {
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
	if !validOpaqueID(invocationID, limits.MaxTextBytes) {
		return TargetSubmissionV1{}, errors.New("target submission invocation identity is invalid")
	}
	body, err := targetRequestBody(sealed)
	if err != nil || len(body) > limits.MaxPaginationClosureBytes || validateTargetRequestBody(body, limits.MaxTextBytes) != nil {
		return TargetSubmissionV1{}, errors.New("target submission GraphQL request body is invalid or unbounded")
	}
	wire := targetSubmissionWire(sealed, invocationID, body, limitsSHA)
	canonical, digest, err := canonicalJSON(wire)
	if err != nil {
		return TargetSubmissionV1{}, err
	}
	return TargetSubmissionV1{
		sealedSHA: sealed.SHA256(), sealSHA: sealed.input.Seal.SHA256(), commitmentSHA: sealed.input.Commitment.SHA256(),
		writeID: sealed.input.MergeInput.attempt.WriteID(), clientMutationID: sealed.input.Commitment.clientMutationID,
		invocationID: invocationID, method: GitHubGraphQLMethodV1, path: GitHubGraphQLPathV1,
		requestBody: append([]byte(nil), body...), requestBodySHA256: digestBytes(body),
		requestBodyBytes: int64(len(body)), canonical: canonical, digest: digest, limitsSHA: limitsSHA,
	}, nil
}

func targetSubmissionWire(sealed SealedMergeAuthorizationV1, invocationID string, body []byte, limitsSHA string) targetSubmissionWireV1 {
	commitment := sealed.input.Commitment
	return targetSubmissionWireV1{
		Schema: TargetSubmissionSchemaV1, SealedAuthorizationSHA: sealed.SHA256(), AuthorizationSealSHA256: sealed.input.Seal.SHA256(),
		CommitmentSHA256: commitment.SHA256(), WriteID: sealed.input.MergeInput.attempt.WriteID(), ClientMutationID: commitment.clientMutationID,
		InvocationID: invocationID, Method: GitHubGraphQLMethodV1, Path: GitHubGraphQLPathV1,
		RequestBody: append(json.RawMessage(nil), body...), RequestBodySHA256: digestBytes(body),
		RequestBodyBytes: int64(len(body)), LimitsSHA256: limitsSHA,
	}
}

func (s TargetSubmissionV1) InvocationID() string      { return s.invocationID }
func (s TargetSubmissionV1) Method() string            { return s.method }
func (s TargetSubmissionV1) Path() string              { return s.path }
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
		s.clientMutationID != s.writeID || s.invocationID == "" || s.method != GitHubGraphQLMethodV1 || s.path != GitHubGraphQLPathV1 ||
		validateTargetRequestBody(s.requestBody, MaxGitHubTargetRequestBodyBytesV1) != nil || !validSHA256(s.requestBodySHA256) ||
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
		wire.ClientMutationID == s.clientMutationID && wire.InvocationID == s.invocationID && wire.Method == s.method && wire.Path == s.path &&
		bytes.Equal(wire.RequestBody, s.requestBody) &&
		wire.RequestBodySHA256 == s.requestBodySHA256 && wire.RequestBodyBytes == s.requestBodyBytes &&
		wire.LimitsSHA256 == s.limitsSHA
}

func ValidateTargetSubmissionV1(sealed SealedMergeAuthorizationV1, submission TargetSubmissionV1, limits Limits) error {
	if !submission.valid() || requireLimitsSHA(limits, submission.limitsSHA) != nil {
		return errors.New("target submission is incomplete or uses different limits")
	}
	rebuilt, err := NewTargetSubmissionV1(submission.invocationID, sealed, limits)
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
		!validOpaqueID(wire.InvocationID, limits.MaxTextBytes) || wire.Method != GitHubGraphQLMethodV1 || wire.Path != GitHubGraphQLPathV1 ||
		len(wire.RequestBody) > limits.MaxPaginationClosureBytes || validateTargetRequestBody(wire.RequestBody, limits.MaxTextBytes) != nil ||
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
		writeID: wire.WriteID, clientMutationID: wire.ClientMutationID, invocationID: wire.InvocationID,
		method: wire.Method, path: wire.Path,
		requestBody: append([]byte(nil), wire.RequestBody...), requestBodySHA256: wire.RequestBodySHA256,
		requestBodyBytes: wire.RequestBodyBytes, canonical: append([]byte(nil), data...),
		digest: digestBytes(data), limitsSHA: wire.LimitsSHA256,
	}, nil
}

func targetRequestBody(sealed SealedMergeAuthorizationV1) ([]byte, error) {
	commitment := sealed.input.Commitment
	updates := make([]gitHubRefUpdateInputV1, len(commitment.updates))
	for index, update := range commitment.updates {
		updates[index] = gitHubRefUpdateInputV1{
			AfterOID: update.AfterOID.String(), BeforeOID: update.BeforeOID.String(), Force: update.Force, Name: update.Name,
		}
	}
	return json.Marshal(gitHubUpdateRefsRequestV1{
		Query: GitHubUpdateRefsDocumentV1,
		Variables: gitHubUpdateRefsVariablesV1{Input: gitHubUpdateRefsInputV1{
			ClientMutationID: commitment.clientMutationID, RefUpdates: updates, RepositoryID: commitment.repositoryNodeID,
		}},
	})
}

func validateTargetRequestBody(body []byte, maxTextBytes int) error {
	if len(body) == 0 || len(body) > MaxGitHubTargetRequestBodyBytesV1 {
		return errors.New("target GraphQL request body is empty or exceeds the fixed limit")
	}
	var request gitHubUpdateRefsRequestV1
	if err := strictDecode(body, &request); err != nil {
		return err
	}
	input := request.Variables.Input
	if request.Query != GitHubUpdateRefsDocumentV1 || !validOpaqueID(input.ClientMutationID, maxTextBytes) ||
		!validOpaqueID(input.RepositoryID, maxTextBytes) || len(input.RefUpdates) != 2 {
		return errors.New("target GraphQL request document or variables are invalid")
	}
	for index, update := range input.RefUpdates {
		if !strings.HasPrefix(update.Name, "refs/heads/") || !validText(update.Name, maxTextBytes, false) || update.Force {
			return errors.New("target GraphQL ref update is invalid")
		}
		if _, err := NewGitSHA(update.BeforeOID); err != nil {
			return fmt.Errorf("target GraphQL ref update %d before OID: %w", index, err)
		}
		if _, err := NewGitSHA(update.AfterOID); err != nil {
			return fmt.Errorf("target GraphQL ref update %d after OID: %w", index, err)
		}
	}
	if input.RefUpdates[0].Name == input.RefUpdates[1].Name {
		return errors.New("target GraphQL ref updates are not distinct")
	}
	canonical, err := json.Marshal(request)
	if err != nil || !bytes.Equal(canonical, body) {
		return errors.New("target GraphQL request body is not canonical")
	}
	return nil
}

// TargetResponseEnvelopeV1 binds a provider-assigned response identity and
// exact response body to the locally assigned pre-transport invocation. The
// two identities are intentionally distinct.
type TargetResponseEnvelopeV1Input struct {
	Response     SnapshotIdentity
	HTTPStatus   int
	ResponseBody []byte
	BodyEvidence ledger.EvidenceRef
	EnvelopeURI  string
}

type TargetResponseEnvelopeV1 struct {
	input      TargetResponseEnvelopeV1Input
	submission TargetSubmissionV1
	canonical  []byte
	digest     string
	limitsSHA  string
}

type targetResponseEnvelopeWireV1 struct {
	Schema                 string             `json:"schema"`
	TargetSubmissionSHA256 string             `json:"target_submission_sha256"`
	InvocationID           string             `json:"invocation_id"`
	Response               identityWire       `json:"response"`
	HTTPStatus             int                `json:"http_status"`
	ResponseBody           json.RawMessage    `json:"response_body"`
	ResponseBodySHA256     string             `json:"response_body_sha256"`
	BodyEvidence           ledger.EvidenceRef `json:"body_evidence"`
	EnvelopeURI            string             `json:"envelope_uri"`
	LimitsSHA256           string             `json:"limits_sha256"`
}

func NewTargetResponseEnvelopeV1(input TargetResponseEnvelopeV1Input, submission TargetSubmissionV1, limits Limits) (TargetResponseEnvelopeV1, error) {
	input.ResponseBody = append([]byte(nil), input.ResponseBody...)
	submission = cloneTargetSubmission(submission)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return TargetResponseEnvelopeV1{}, err
	}
	if !submission.valid() || requireLimitsSHA(limits, submission.limitsSHA) != nil || !input.Response.valid() ||
		input.Response.Provider() != "github" || input.Response.RequestID() == submission.invocationID || input.HTTPStatus < 100 || input.HTTPStatus > 599 ||
		len(input.ResponseBody) == 0 || len(input.ResponseBody) > MaxGitHubTargetResponseBodyBytesV1 ||
		!validEvidenceRef(input.BodyEvidence) || input.BodyEvidence.Kind != GitHubTargetResponseBodyEvidenceKindV1 ||
		input.BodyEvidence.SHA256 != digestBytes(input.ResponseBody) || !validText(input.EnvelopeURI, limits.MaxTextBytes, false) {
		return TargetResponseEnvelopeV1{}, errors.New("target response envelope identity or evidence is invalid")
	}
	var raw json.RawMessage
	if err := strictDecode(input.ResponseBody, &raw); err != nil {
		return TargetResponseEnvelopeV1{}, errors.New("target response body is not a single JSON value")
	}
	canonicalBody, err := json.Marshal(raw)
	if err != nil || !bytes.Equal(canonicalBody, input.ResponseBody) {
		return TargetResponseEnvelopeV1{}, errors.New("target response body is not strict canonical JSON")
	}
	wire := targetResponseEnvelopeWireV1{
		Schema: TargetResponseEnvelopeSchemaV1, TargetSubmissionSHA256: submission.SHA256(), InvocationID: submission.invocationID,
		Response: snapshotWire(input.Response), HTTPStatus: input.HTTPStatus, ResponseBody: append(json.RawMessage(nil), input.ResponseBody...),
		ResponseBodySHA256: digestBytes(input.ResponseBody), BodyEvidence: input.BodyEvidence, EnvelopeURI: input.EnvelopeURI,
		LimitsSHA256: limitsSHA,
	}
	canonical, digest, err := canonicalJSON(wire)
	if err != nil || len(canonical) > limits.MaxPaginationClosureBytes {
		return TargetResponseEnvelopeV1{}, errors.New("target response envelope is invalid or unbounded")
	}
	return TargetResponseEnvelopeV1{input: input, submission: submission, canonical: canonical, digest: digest, limitsSHA: limitsSHA}, nil
}

func (e TargetResponseEnvelopeV1) Input() TargetResponseEnvelopeV1Input {
	input := e.input
	input.ResponseBody = append([]byte(nil), input.ResponseBody...)
	return input
}
func (e TargetResponseEnvelopeV1) CanonicalJSON() []byte { return append([]byte(nil), e.canonical...) }
func (e TargetResponseEnvelopeV1) SHA256() string        { return e.digest }
func (e TargetResponseEnvelopeV1) EvidenceRef() ledger.EvidenceRef {
	if !validText(e.input.EnvelopeURI, 4096, false) || !validSHA256(e.digest) || digestBytes(e.canonical) != e.digest {
		return ledger.EvidenceRef{}
	}
	return ledger.EvidenceRef{URI: e.input.EnvelopeURI, Kind: GitHubTargetResponseEnvelopeEvidenceKindV1, SHA256: e.digest}
}
func (e TargetResponseEnvelopeV1) valid() bool {
	return e.submission.valid() && e.input.Response.valid() && e.input.Response.Provider() == "github" &&
		e.input.Response.RequestID() != e.submission.invocationID &&
		e.input.HTTPStatus >= 100 && e.input.HTTPStatus <= 599 && len(e.input.ResponseBody) > 0 &&
		len(e.input.ResponseBody) <= MaxGitHubTargetResponseBodyBytesV1 && validEvidenceRef(e.input.BodyEvidence) &&
		e.input.BodyEvidence.Kind == GitHubTargetResponseBodyEvidenceKindV1 && e.input.BodyEvidence.SHA256 == digestBytes(e.input.ResponseBody) &&
		validEvidenceRef(e.EvidenceRef()) && validSHA256(e.digest) && digestBytes(e.canonical) == e.digest && validSHA256(e.limitsSHA)
}

func ValidateTargetResponseEnvelopeV1(submission TargetSubmissionV1, envelope TargetResponseEnvelopeV1, limits Limits) error {
	if !envelope.valid() || requireLimitsSHA(limits, envelope.limitsSHA) != nil ||
		envelope.submission.SHA256() != submission.SHA256() || !bytes.Equal(envelope.submission.CanonicalJSON(), submission.CanonicalJSON()) {
		return errors.New("target response envelope does not bind the exact submitted invocation")
	}
	rebuilt, err := NewTargetResponseEnvelopeV1(envelope.input, submission, limits)
	if err != nil || rebuilt.digest != envelope.digest || !bytes.Equal(rebuilt.canonical, envelope.canonical) {
		return errors.New("target response envelope fails independent validation")
	}
	return nil
}

func ParseCanonicalTargetResponseEnvelopeV1(data []byte, submission TargetSubmissionV1, limits Limits) (TargetResponseEnvelopeV1, error) {
	var wire targetResponseEnvelopeWireV1
	if err := strictDecode(data, &wire); err != nil {
		return TargetResponseEnvelopeV1{}, err
	}
	if wire.Schema != TargetResponseEnvelopeSchemaV1 || wire.TargetSubmissionSHA256 != submission.SHA256() ||
		wire.InvocationID != submission.invocationID || wire.ResponseBodySHA256 != digestBytes(wire.ResponseBody) {
		return TargetResponseEnvelopeV1{}, errors.New("target response envelope wire identity disagrees")
	}
	response, err := NewSnapshotIdentity(wire.Response.Provider, wire.Response.RequestID, wire.Response.ObservedUnixNano)
	if err != nil {
		return TargetResponseEnvelopeV1{}, err
	}
	value, err := NewTargetResponseEnvelopeV1(TargetResponseEnvelopeV1Input{
		Response: response, HTTPStatus: wire.HTTPStatus, ResponseBody: wire.ResponseBody,
		BodyEvidence: wire.BodyEvidence, EnvelopeURI: wire.EnvelopeURI,
	}, submission, limits)
	if err != nil || wire.LimitsSHA256 != value.limitsSHA {
		return TargetResponseEnvelopeV1{}, errors.New("target response envelope limits or evidence disagrees")
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return TargetResponseEnvelopeV1{}, err
	}
	return value, nil
}

type gitHubUpdateRefsSuccessV1 struct {
	Data struct {
		UpdateRefs struct {
			ClientMutationID string `json:"clientMutationId"`
		} `json:"updateRefs"`
	} `json:"data"`
}

func validateTargetSuccessResponse(envelope TargetResponseEnvelopeV1, submission TargetSubmissionV1) error {
	if envelope.input.HTTPStatus != 200 {
		return errors.New("target success response has a non-success HTTP status")
	}
	var response gitHubUpdateRefsSuccessV1
	if err := strictDecode(envelope.input.ResponseBody, &response); err != nil ||
		response.Data.UpdateRefs.ClientMutationID != submission.clientMutationID {
		return errors.New("target success response does not echo the exact client mutation identity")
	}
	canonical, _ := json.Marshal(response)
	if !bytes.Equal(canonical, envelope.input.ResponseBody) {
		return errors.New("target success response is not canonical")
	}
	return nil
}

type MergeExecutionInputV1 struct {
	sealed     SealedMergeAuthorizationV1
	submission TargetSubmissionV1
	limitsSHA  string
}

func NewMergeExecutionInputV1(sealed SealedMergeAuthorizationV1, submission TargetSubmissionV1, limits Limits) (MergeExecutionInputV1, error) {
	if !sealed.valid() || ValidateTargetSubmissionV1(sealed, submission, limits) != nil {
		return MergeExecutionInputV1{}, errors.New("merge execution requires the exact pre-published target submission")
	}
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return MergeExecutionInputV1{}, err
	}
	return MergeExecutionInputV1{cloneSealedAuthorization(sealed), cloneTargetSubmission(submission), limitsSHA}, nil
}

func (i MergeExecutionInputV1) SealedAuthorization() SealedMergeAuthorizationV1 {
	return cloneSealedAuthorization(i.sealed)
}
func (i MergeExecutionInputV1) TargetSubmission() TargetSubmissionV1 {
	return cloneTargetSubmission(i.submission)
}
func (i MergeExecutionInputV1) LimitsSHA256() string { return i.limitsSHA }

func validateMergeExecutionInputV1(input MergeExecutionInputV1, limits Limits) error {
	if !input.sealed.valid() || requireLimitsSHA(limits, input.limitsSHA) != nil ||
		ValidateTargetSubmissionV1(input.sealed, input.submission, limits) != nil {
		return errors.New("merge execution input changed its sealed authorization or target submission")
	}
	return nil
}

type MergeExecutionResultV1 struct {
	input            MergeExecutionInputV1
	responseEnvelope TargetResponseEnvelopeV1
	result           MergeResult
	limitsSHA        string
}

func NewMergeExecutionResultV1(input MergeExecutionInputV1, responseEnvelope TargetResponseEnvelopeV1, result MergeResult, limits Limits) (MergeExecutionResultV1, error) {
	if err := validateMergeExecutionInputV1(input, limits); err != nil {
		return MergeExecutionResultV1{}, err
	}
	if err := ValidateMergeResult(input.sealed, result, limits); err != nil {
		return MergeExecutionResultV1{}, err
	}
	if ValidateTargetResponseEnvelopeV1(input.submission, responseEnvelope, limits) != nil ||
		validateTargetSuccessResponse(responseEnvelope, input.submission) != nil ||
		result.immutable.data.Snapshot != responseEnvelope.input.Response ||
		responseEnvelope.input.Response.ObservedUnixNano() < input.sealed.input.Seal.input.FinalRevalidation.input.CompletedUnixNano ||
		!containsEvidence(result.immutable.data.EvidenceRefs, responseEnvelope.input.BodyEvidence) ||
		!containsEvidence(result.immutable.data.EvidenceRefs, responseEnvelope.EvidenceRef()) {
		return MergeExecutionResultV1{}, errors.New("merge execution result lacks the exact submitted request/response binding")
	}
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return MergeExecutionResultV1{}, err
	}
	return MergeExecutionResultV1{
		input:            MergeExecutionInputV1{cloneSealedAuthorization(input.sealed), cloneTargetSubmission(input.submission), input.limitsSHA},
		responseEnvelope: cloneTargetResponseEnvelope(responseEnvelope), result: cloneMergeResult(result), limitsSHA: limitsSHA,
	}, nil
}

func (r MergeExecutionResultV1) TargetSubmission() TargetSubmissionV1 {
	return cloneTargetSubmission(r.input.submission)
}
func (r MergeExecutionResultV1) MergeResult() MergeResult { return cloneMergeResult(r.result) }
func (r MergeExecutionResultV1) ResponseEnvelope() TargetResponseEnvelopeV1 {
	return cloneTargetResponseEnvelope(r.responseEnvelope)
}
func (r MergeExecutionResultV1) LimitsSHA256() string { return r.limitsSHA }

func ValidateMergeExecutionResultV1(input MergeExecutionInputV1, result MergeExecutionResultV1, limits Limits) error {
	if err := validateMergeExecutionInputV1(input, limits); err != nil {
		return err
	}
	if requireLimitsSHA(limits, result.limitsSHA) != nil ||
		result.input.submission.SHA256() != input.submission.SHA256() ||
		!bytes.Equal(result.input.submission.CanonicalJSON(), input.submission.CanonicalJSON()) ||
		result.input.sealed.SHA256() != input.sealed.SHA256() ||
		ValidateTargetResponseEnvelopeV1(input.submission, result.responseEnvelope, limits) != nil {
		return errors.New("merge execution result does not bind the invoked target submission")
	}
	rebuilt, err := NewMergeExecutionResultV1(input, result.responseEnvelope, result.result, limits)
	if err != nil || rebuilt.limitsSHA != result.limitsSHA {
		return errors.New("merge execution result fails independent request/response validation")
	}
	return nil
}

func cloneMergeResult(result MergeResult) MergeResult {
	result.immutable.data = cloneMergeInput(result.immutable.data)
	result.immutable.canonical = append([]byte(nil), result.immutable.canonical...)
	return result
}

func cloneTargetSubmission(submission TargetSubmissionV1) TargetSubmissionV1 {
	submission.requestBody = append([]byte(nil), submission.requestBody...)
	submission.canonical = append([]byte(nil), submission.canonical...)
	return submission
}

func cloneTargetResponseEnvelope(envelope TargetResponseEnvelopeV1) TargetResponseEnvelopeV1 {
	envelope.input.ResponseBody = append([]byte(nil), envelope.input.ResponseBody...)
	envelope.submission = cloneTargetSubmission(envelope.submission)
	envelope.canonical = append([]byte(nil), envelope.canonical...)
	return envelope
}

package githublifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	TargetSubmissionSchemaV1          = "target-submission-v1"
	GitHubUpdateRefsDocumentV1        = "mutation UpdateRefs($input:UpdateRefsInput!){updateRefs(input:$input){clientMutationId}}"
	GitHubGraphQLMethodV1             = "POST"
	GitHubGraphQLPathV1               = "/graphql"
	MaxGitHubTargetRequestBodyBytesV1 = 16 * 1024
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
	requestID         string
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
	RequestID               string          `json:"request_id"`
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
	body, err := targetRequestBody(sealed)
	if err != nil || len(body) > limits.MaxPaginationClosureBytes || validateTargetRequestBody(body, limits.MaxTextBytes) != nil {
		return TargetSubmissionV1{}, errors.New("target submission GraphQL request body is invalid or unbounded")
	}
	wire := targetSubmissionWire(sealed, requestID, body, limitsSHA)
	canonical, digest, err := canonicalJSON(wire)
	if err != nil {
		return TargetSubmissionV1{}, err
	}
	return TargetSubmissionV1{
		sealedSHA: sealed.SHA256(), sealSHA: sealed.input.Seal.SHA256(), commitmentSHA: sealed.input.Commitment.SHA256(),
		writeID: sealed.input.MergeInput.attempt.WriteID(), clientMutationID: sealed.input.Commitment.clientMutationID,
		requestID: requestID, method: GitHubGraphQLMethodV1, path: GitHubGraphQLPathV1,
		requestBody: append([]byte(nil), body...), requestBodySHA256: digestBytes(body),
		requestBodyBytes: int64(len(body)), canonical: canonical, digest: digest, limitsSHA: limitsSHA,
	}, nil
}

func targetSubmissionWire(sealed SealedMergeAuthorizationV1, requestID string, body []byte, limitsSHA string) targetSubmissionWireV1 {
	commitment := sealed.input.Commitment
	return targetSubmissionWireV1{
		Schema: TargetSubmissionSchemaV1, SealedAuthorizationSHA: sealed.SHA256(), AuthorizationSealSHA256: sealed.input.Seal.SHA256(),
		CommitmentSHA256: commitment.SHA256(), WriteID: sealed.input.MergeInput.attempt.WriteID(), ClientMutationID: commitment.clientMutationID,
		RequestID: requestID, Method: GitHubGraphQLMethodV1, Path: GitHubGraphQLPathV1,
		RequestBody: append(json.RawMessage(nil), body...), RequestBodySHA256: digestBytes(body),
		RequestBodyBytes: int64(len(body)), LimitsSHA256: limitsSHA,
	}
}

func (s TargetSubmissionV1) RequestID() string         { return s.requestID }
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
		s.clientMutationID != s.writeID || s.requestID == "" || s.method != GitHubGraphQLMethodV1 || s.path != GitHubGraphQLPathV1 ||
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
		wire.ClientMutationID == s.clientMutationID && wire.RequestID == s.requestID && wire.Method == s.method && wire.Path == s.path &&
		bytes.Equal(wire.RequestBody, s.requestBody) &&
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
		!validOpaqueID(wire.RequestID, limits.MaxTextBytes) || wire.Method != GitHubGraphQLMethodV1 || wire.Path != GitHubGraphQLPathV1 ||
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
		writeID: wire.WriteID, clientMutationID: wire.ClientMutationID, requestID: wire.RequestID,
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
	input     MergeExecutionInputV1
	result    MergeResult
	limitsSHA string
}

func NewMergeExecutionResultV1(input MergeExecutionInputV1, result MergeResult, limits Limits) (MergeExecutionResultV1, error) {
	if err := validateMergeExecutionInputV1(input, limits); err != nil {
		return MergeExecutionResultV1{}, err
	}
	if err := ValidateMergeResult(input.sealed, result, limits); err != nil {
		return MergeExecutionResultV1{}, err
	}
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return MergeExecutionResultV1{}, err
	}
	return MergeExecutionResultV1{
		input:  MergeExecutionInputV1{cloneSealedAuthorization(input.sealed), cloneTargetSubmission(input.submission), input.limitsSHA},
		result: cloneMergeResult(result), limitsSHA: limitsSHA,
	}, nil
}

func (r MergeExecutionResultV1) TargetSubmission() TargetSubmissionV1 {
	return cloneTargetSubmission(r.input.submission)
}
func (r MergeExecutionResultV1) MergeResult() MergeResult { return cloneMergeResult(r.result) }
func (r MergeExecutionResultV1) LimitsSHA256() string     { return r.limitsSHA }

func ValidateMergeExecutionResultV1(input MergeExecutionInputV1, result MergeExecutionResultV1, limits Limits) error {
	if err := validateMergeExecutionInputV1(input, limits); err != nil {
		return err
	}
	if requireLimitsSHA(limits, result.limitsSHA) != nil ||
		result.input.submission.SHA256() != input.submission.SHA256() ||
		!bytes.Equal(result.input.submission.CanonicalJSON(), input.submission.CanonicalJSON()) ||
		result.input.sealed.SHA256() != input.sealed.SHA256() {
		return errors.New("merge execution result does not bind the invoked target submission")
	}
	return ValidateMergeResult(input.sealed, result.result, limits)
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

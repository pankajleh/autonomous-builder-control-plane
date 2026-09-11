package githublifecycle

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	MergeCommitRecipeSchemaV1        = "merge-commit-recipe-v1"
	GitHubAtomicBaseHeadCapabilityV1 = "github-update-refs-atomic-base-head-v1"
	ProviderCapabilitySchemaV1       = "provider-capability-v1"
	AuthorizationSealSchemaV1        = "authorization-seal-v1"
	SealedMergeAuthorizationSchemaV1 = "sealed-merge-authorization-v1"
	TargetRefCommitmentSchemaV1      = "target-ref-commitment-v1"
)

type MergeCommitRecipeV1Input struct {
	Repository         Repository
	TargetRef          string
	ExpectedResultTree GitSHA
	Parents            []GitSHA
	Message            string
	Author             MergeCommitIdentityV1
	Committer          MergeCommitIdentityV1
	AuthorUnix         int64
	CommitterUnix      int64
	ObjectFormat       string
	ExpectedResultSHA  GitSHA
	WriteID            string
	AuthoritySHA256    string
	PolicySHA256       string
	ReadyBindingSHA256 string
	LimitsSHA256       string
}

type MergeCommitRecipeV1 struct {
	input       MergeCommitRecipeV1Input
	canonical   []byte
	digest      string
	commitBytes []byte
}

func NewMergeCommitRecipeV1(writeID string, authority Authority, limits Limits) (MergeCommitRecipeV1, error) {
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return MergeCommitRecipeV1{}, err
	}
	if err := requireAuthority(authority); err != nil {
		return MergeCommitRecipeV1{}, err
	}
	if !validOpaqueID(writeID, limits.MaxTextBytes) {
		return MergeCommitRecipeV1{}, errors.New("merge commit recipe write identity is invalid")
	}
	policy := authority.MergePolicy()
	if !policy.valid() || policy.input.Method != MergeMethodMerge || policy.input.Recipe.TimestampDerivation != "ready-event-time" || !policy.input.Recipe.OrderedParents {
		return MergeCommitRecipeV1{}, errors.New("merge commit recipe does not match controller policy")
	}
	authoritySHA, err := authority.SHA256()
	if err != nil {
		return MergeCommitRecipeV1{}, err
	}
	ready := authority.ReadyBinding()
	timestamp := ready.input.ReadyEventUnixNano / 1_000_000_000
	message := policy.input.Recipe.MessageTemplate + "\n\n" + policy.input.Recipe.TrailerTemplate + ": " + writeID
	input := MergeCommitRecipeV1Input{
		Repository: authority.Repository(), TargetRef: "refs/heads/" + authority.BaseBranch().String(),
		ExpectedResultTree: authority.ExpectedContent().ExpectedResultTreeSHA(),
		Parents:            []GitSHA{authority.ExpectedBaseTipSHA(), authority.HeadSHA()},
		Message:            message, Author: policy.input.Recipe.Author, Committer: policy.input.Recipe.Committer,
		AuthorUnix: timestamp, CommitterUnix: timestamp, ObjectFormat: policy.input.Recipe.ObjectFormat,
		WriteID: writeID, AuthoritySHA256: authoritySHA, PolicySHA256: policy.SHA256(),
		ReadyBindingSHA256: ready.SHA256(), LimitsSHA256: limitsSHA,
	}
	if len(input.Parents) != limits.RequiredProductionMergeParents ||
		!validCommitMessage(input.Message, limits.MaxTextBytes) || strings.HasSuffix(input.Message, "\n\n") || input.AuthorUnix <= 0 ||
		!oidsMatchObjectFormat(input.ObjectFormat, append([]GitSHA{input.ExpectedResultTree}, input.Parents...)) {
		return MergeCommitRecipeV1{}, errors.New("policy-derived merge commit recipe is invalid or uses inconsistent object IDs")
	}
	commit, err := canonicalCommitBytes(input)
	if err != nil {
		return MergeCommitRecipeV1{}, err
	}
	expected, err := gitObjectID(input.ObjectFormat, commit)
	if err != nil {
		return MergeCommitRecipeV1{}, err
	}
	expectedSHA, err := NewGitSHA(expected)
	if err != nil {
		return MergeCommitRecipeV1{}, err
	}
	input.ExpectedResultSHA = expectedSHA
	canonical, digest, err := canonicalJSON(mergeCommitRecipeWire(input))
	if err != nil {
		return MergeCommitRecipeV1{}, err
	}
	return MergeCommitRecipeV1{input, canonical, digest, commit}, nil
}

func oidsMatchObjectFormat(format string, values []GitSHA) bool {
	want := 0
	if format == "sha1" {
		want = 40
	} else if format == "sha256" {
		want = 64
	} else {
		return false
	}
	for _, value := range values {
		if !value.valid() || len(value.String()) != want {
			return false
		}
	}
	return true
}
func (r MergeCommitRecipeV1) Input() MergeCommitRecipeV1Input {
	i := r.input
	i.Parents = append([]GitSHA(nil), i.Parents...)
	return i
}
func (r MergeCommitRecipeV1) CanonicalJSON() []byte     { return append([]byte(nil), r.canonical...) }
func (r MergeCommitRecipeV1) SHA256() string            { return r.digest }
func (r MergeCommitRecipeV1) CommitBytes() []byte       { return append([]byte(nil), r.commitBytes...) }
func (r MergeCommitRecipeV1) ExpectedResultSHA() GitSHA { return r.input.ExpectedResultSHA }
func (r MergeCommitRecipeV1) MarshalJSON() ([]byte, error) {
	if !r.valid() {
		return nil, errors.New("merge commit recipe incomplete")
	}
	return r.CanonicalJSON(), nil
}
func (r MergeCommitRecipeV1) valid() bool {
	return len(r.canonical) > 0 && validSHA256(r.digest) && digestBytes(r.canonical) == r.digest && len(r.commitBytes) > 0
}

type mergeCommitRecipeWireV1 struct {
	Schema             string                `json:"schema"`
	Repository         repoWire              `json:"repository"`
	TargetRef          string                `json:"target_ref"`
	ExpectedResultTree string                `json:"expected_result_tree"`
	Parents            []string              `json:"parents"`
	Message            string                `json:"message"`
	Author             MergeCommitIdentityV1 `json:"author"`
	Committer          MergeCommitIdentityV1 `json:"committer"`
	AuthorUnix         int64                 `json:"author_unix"`
	CommitterUnix      int64                 `json:"committer_unix"`
	ObjectFormat       string                `json:"object_format"`
	ExpectedResultSHA  string                `json:"expected_result_sha"`
	WriteID            string                `json:"write_id"`
	AuthoritySHA256    string                `json:"authority_sha256"`
	PolicySHA256       string                `json:"policy_sha256"`
	ReadyBindingSHA256 string                `json:"ready_binding_sha256"`
	LimitsSHA256       string                `json:"limits_sha256"`
}

func mergeCommitRecipeWire(i MergeCommitRecipeV1Input) mergeCommitRecipeWireV1 {
	return mergeCommitRecipeWireV1{MergeCommitRecipeSchemaV1, repositoryWire(i.Repository), i.TargetRef, i.ExpectedResultTree.String(), shaStrings(i.Parents), i.Message, i.Author, i.Committer, i.AuthorUnix, i.CommitterUnix, i.ObjectFormat, i.ExpectedResultSHA.String(), i.WriteID, i.AuthoritySHA256, i.PolicySHA256, i.ReadyBindingSHA256, i.LimitsSHA256}
}

func canonicalCommitBytes(i MergeCommitRecipeV1Input) ([]byte, error) {
	if strings.ContainsAny(i.Author.Name+i.Committer.Name+i.Author.Email+i.Committer.Email, "\n\r<>\x00") || strings.Contains(i.Message, "\x00") {
		return nil, errors.New("merge commit identity or message contains unsafe Git syntax")
	}
	var body strings.Builder
	body.WriteString("tree " + i.ExpectedResultTree.String() + "\n")
	for _, p := range i.Parents {
		body.WriteString("parent " + p.String() + "\n")
	}
	body.WriteString("author " + i.Author.Name + " <" + i.Author.Email + "> " + strconv.FormatInt(i.AuthorUnix, 10) + " " + i.Author.Timezone + "\n")
	body.WriteString("committer " + i.Committer.Name + " <" + i.Committer.Email + "> " + strconv.FormatInt(i.CommitterUnix, 10) + " " + i.Committer.Timezone + "\n\n")
	body.WriteString(i.Message)
	if !strings.HasSuffix(i.Message, "\n") {
		body.WriteByte('\n')
	}
	return []byte(body.String()), nil
}

func validCommitMessage(value string, max int) bool {
	if value == "" || len(value) > max || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r") {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\t' {
			return false
		}
	}
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimRight(line, " \t") != line {
			return false
		}
	}
	return true
}
func gitObjectID(format string, body []byte) (string, error) {
	header := []byte("commit " + strconv.Itoa(len(body)) + "\x00")
	data := append(header, body...)
	if format == "sha1" {
		sum := sha1.Sum(data)
		return hex.EncodeToString(sum[:]), nil
	}
	if format == "sha256" {
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:]), nil
	}
	return "", errors.New("unsupported Git object format")
}

func ParseCanonicalMergeCommitRecipeV1(data []byte, authority Authority, limits Limits) (MergeCommitRecipeV1, error) {
	var w mergeCommitRecipeWireV1
	if err := strictDecode(data, &w); err != nil {
		return MergeCommitRecipeV1{}, err
	}
	if w.Schema != MergeCommitRecipeSchemaV1 {
		return MergeCommitRecipeV1{}, errors.New("unsupported merge recipe schema")
	}
	value, err := NewMergeCommitRecipeV1(w.WriteID, authority, limits)
	if err != nil {
		return MergeCommitRecipeV1{}, err
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return MergeCommitRecipeV1{}, err
	}
	return value, nil
}

type ProviderCapabilityV1Input struct {
	Name              string
	RepositoryNodeID  string
	APIVersion        string
	Atomic            bool
	AllOrNothing      bool
	SupportsNoOp      bool
	BaseThenHeadOrder bool
	ForceFalse        bool
	EvidenceRefs      []ledger.EvidenceRef
}
type ProviderCapabilityV1 struct {
	input     ProviderCapabilityV1Input
	canonical []byte
	digest    string
	limitsSHA string
}

func NewProviderCapabilityV1(input ProviderCapabilityV1Input, limits Limits) (ProviderCapabilityV1, error) {
	input.EvidenceRefs = append([]ledger.EvidenceRef(nil), input.EvidenceRefs...)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return ProviderCapabilityV1{}, err
	}
	if input.Name != GitHubAtomicBaseHeadCapabilityV1 || !validOpaqueID(input.RepositoryNodeID, limits.MaxTextBytes) || !validText(input.APIVersion, limits.MaxTextBytes, false) || !input.Atomic || !input.AllOrNothing || !input.SupportsNoOp || !input.BaseThenHeadOrder || !input.ForceFalse || len(input.EvidenceRefs) == 0 || canonicalizeEvidence(&input.EvidenceRefs, limits) != nil {
		return ProviderCapabilityV1{}, errors.New("frozen exact-base/exact-head capability is unavailable")
	}
	wire := providerCapabilityWireV1{ProviderCapabilitySchemaV1, input.Name, input.RepositoryNodeID, input.APIVersion, input.Atomic, input.AllOrNothing, input.SupportsNoOp, input.BaseThenHeadOrder, input.ForceFalse, input.EvidenceRefs, limitsSHA}
	canonical, digest, err := canonicalJSON(wire)
	if err != nil {
		return ProviderCapabilityV1{}, err
	}
	return ProviderCapabilityV1{input, canonical, digest, limitsSHA}, nil
}

type providerCapabilityWireV1 struct {
	Schema            string               `json:"schema"`
	Name              string               `json:"name"`
	RepositoryNodeID  string               `json:"repository_node_id"`
	APIVersion        string               `json:"api_version"`
	Atomic            bool                 `json:"atomic"`
	AllOrNothing      bool                 `json:"all_or_nothing"`
	SupportsNoOp      bool                 `json:"supports_no_op"`
	BaseThenHeadOrder bool                 `json:"base_then_head_order"`
	ForceFalse        bool                 `json:"force_false"`
	EvidenceRefs      []ledger.EvidenceRef `json:"evidence_refs"`
	LimitsSHA256      string               `json:"limits_sha256"`
}

func ParseCanonicalProviderCapabilityV1(data []byte, limits Limits) (ProviderCapabilityV1, error) {
	var wire providerCapabilityWireV1
	if err := strictDecode(data, &wire); err != nil {
		return ProviderCapabilityV1{}, err
	}
	if wire.Schema != ProviderCapabilitySchemaV1 {
		return ProviderCapabilityV1{}, errors.New("unsupported provider capability schema")
	}
	value, err := NewProviderCapabilityV1(ProviderCapabilityV1Input{wire.Name, wire.RepositoryNodeID, wire.APIVersion, wire.Atomic, wire.AllOrNothing, wire.SupportsNoOp, wire.BaseThenHeadOrder, wire.ForceFalse, wire.EvidenceRefs}, limits)
	if err != nil {
		return ProviderCapabilityV1{}, err
	}
	if wire.LimitsSHA256 != value.limitsSHA {
		return ProviderCapabilityV1{}, errors.New("provider capability limits digest disagrees")
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return ProviderCapabilityV1{}, err
	}
	return value, nil
}
func (c ProviderCapabilityV1) CanonicalJSON() []byte { return append([]byte(nil), c.canonical...) }
func (c ProviderCapabilityV1) SHA256() string        { return c.digest }
func (c ProviderCapabilityV1) MarshalJSON() ([]byte, error) {
	if !c.valid() {
		return nil, errors.New("provider capability incomplete")
	}
	return c.CanonicalJSON(), nil
}
func (c ProviderCapabilityV1) valid() bool {
	return len(c.canonical) > 0 && validSHA256(c.digest) && digestBytes(c.canonical) == c.digest && validSHA256(c.limitsSHA)
}

type MergeAuthorizationInputV1 struct {
	Authority             Authority
	PolicyDecisionSHA256  string
	InitialPullRequest    AuthoritativePullRequestSnapshotV1
	Checks                []Check
	CheckRunsClosure      PaginationClosureV1
	CommitStatusesClosure PaginationClosureV1
	Capability            ProviderCapabilityV1
	Recipe                MergeCommitRecipeV1
	EvidenceRefs          []ledger.EvidenceRef
}

type AuthorizationCountersV1 struct {
	AdmissionHTTPCalls                  int   `json:"admission_http_calls"`
	AdmissionObservedChecks             int   `json:"admission_observed_checks"`
	AdmissionObservedReviews            int   `json:"admission_observed_reviews"`
	AdmissionPaginationSources          int   `json:"admission_pagination_sources"`
	AdmissionPaginationPages            int   `json:"admission_pagination_pages"`
	AdmissionPaginationItems            int   `json:"admission_pagination_items"`
	AdmissionPaginationClosureBytes     int64 `json:"admission_pagination_closure_bytes"`
	FinalRevalidationHTTPCalls          int   `json:"final_revalidation_http_calls"`
	FinalObservedChecks                 int   `json:"final_observed_checks"`
	FinalObservedReviews                int   `json:"final_observed_reviews"`
	FinalPaginationSources              int   `json:"final_pagination_sources"`
	FinalPaginationPages                int   `json:"final_pagination_pages"`
	FinalPaginationItems                int   `json:"final_pagination_items"`
	FinalPaginationClosureBytes         int64 `json:"final_pagination_closure_bytes"`
	ReadyLedgerBytes                    int64 `json:"ready_ledger_bytes"`
	ReadyLedgerRecords                  int   `json:"ready_ledger_records"`
	PreSubmitHTTPCalls                  int   `json:"pre_submit_http_calls"`
	CommitObjectCreationSubmissions     int   `json:"commit_object_creation_submissions"`
	TargetRefUpdateSubmissions          int   `json:"target_ref_update_submissions"`
	PostMergeHTTPCalls                  int   `json:"post_merge_http_calls"`
	ReconciliationRounds                int   `json:"reconciliation_rounds"`
	ReconciliationHTTPCalls             int   `json:"reconciliation_http_calls"`
	PrincipalValidationHTTPCalls        int   `json:"principal_validation_http_calls"`
	TotalHTTPCalls                      int   `json:"total_http_calls"`
	CumulativeRequestBytes              int64 `json:"cumulative_request_bytes"`
	CumulativeResponseHeaderBytes       int64 `json:"cumulative_response_header_bytes"`
	CumulativeCompressedResponseBytes   int64 `json:"cumulative_compressed_response_bytes"`
	CumulativeDecompressedResponseBytes int64 `json:"cumulative_decompressed_response_bytes"`
	CumulativeActiveProviderCallNanos   int64 `json:"cumulative_active_provider_call_nanos"`
	ControllerInvocationNanos           int64 `json:"controller_invocation_nanos"`
}

func (c AuthorizationCountersV1) valid(limits Limits) bool {
	if c.AdmissionHTTPCalls < 0 || c.AdmissionObservedChecks < 0 || c.AdmissionObservedReviews < 0 ||
		c.AdmissionPaginationSources < 0 || c.AdmissionPaginationPages < 0 || c.AdmissionPaginationItems < 0 ||
		c.AdmissionPaginationClosureBytes < 0 || c.FinalRevalidationHTTPCalls < 0 || c.FinalObservedChecks < 0 ||
		c.FinalObservedReviews < 0 || c.FinalPaginationSources < 0 || c.FinalPaginationPages < 0 ||
		c.FinalPaginationItems < 0 || c.FinalPaginationClosureBytes < 0 || c.ReadyLedgerBytes < 0 ||
		c.ReadyLedgerRecords < 0 || c.PreSubmitHTTPCalls < 0 || c.CommitObjectCreationSubmissions < 0 ||
		c.TargetRefUpdateSubmissions < 0 || c.PostMergeHTTPCalls < 0 || c.ReconciliationRounds < 0 ||
		c.ReconciliationHTTPCalls < 0 || c.PrincipalValidationHTTPCalls < 0 || c.TotalHTTPCalls < 0 ||
		c.CumulativeRequestBytes < 0 || c.CumulativeResponseHeaderBytes < 0 || c.CumulativeCompressedResponseBytes < 0 ||
		c.CumulativeDecompressedResponseBytes < 0 || c.CumulativeActiveProviderCallNanos < 0 || c.ControllerInvocationNanos < 0 {
		return false
	}
	if c.AdmissionObservedChecks > limits.MaxObservedChecks || c.FinalObservedChecks > limits.MaxObservedChecks ||
		c.AdmissionObservedReviews > limits.MaxObservedReviews || c.FinalObservedReviews > limits.MaxObservedReviews ||
		c.AdmissionPaginationSources != limits.RequiredPaginationSources || c.FinalPaginationSources != limits.RequiredPaginationSources ||
		!withinProduct(c.AdmissionPaginationPages, limits.RequiredPaginationSources, limits.MaxPaginationPages) ||
		!withinProduct(c.FinalPaginationPages, limits.RequiredPaginationSources, limits.MaxPaginationPages) ||
		!withinCombinedLimit(c.AdmissionPaginationItems, limits.MaxObservedChecks, limits.MaxObservedReviews) ||
		!withinCombinedLimit(c.FinalPaginationItems, limits.MaxObservedChecks, limits.MaxObservedReviews) ||
		c.AdmissionPaginationClosureBytes > int64(limits.MaxCumulativePaginationClosureBytes) ||
		c.FinalPaginationClosureBytes > int64(limits.MaxCumulativePaginationClosureBytes) ||
		c.ReadyLedgerBytes > int64(limits.MaxReadyLedgerSnapshotBytes) || c.ReadyLedgerRecords > limits.MaxLedgerScanRecords {
		return false
	}
	if c.PreSubmitHTTPCalls > limits.MaxPreSubmitHTTPCalls ||
		c.CommitObjectCreationSubmissions > limits.MaxCommitObjectCreationSubmissions ||
		c.TargetRefUpdateSubmissions > limits.MaxTargetRefUpdateSubmissions ||
		c.PostMergeHTTPCalls > limits.MaxPostMergeHTTPCalls ||
		c.ReconciliationRounds > limits.MaxReconciliationRounds ||
		!withinProduct(c.ReconciliationHTTPCalls, limits.MaxReconciliationRounds, limits.MaxReconciliationCallsPerRound) ||
		!withinProduct(c.ReconciliationHTTPCalls, c.ReconciliationRounds, limits.MaxReconciliationCallsPerRound) ||
		c.PrincipalValidationHTTPCalls > limits.MaxPrincipalValidationCalls || c.TotalHTTPCalls > limits.MaxHTTPCalls {
		return false
	}
	if !sumEquals(c.PreSubmitHTTPCalls, c.AdmissionHTTPCalls, c.FinalRevalidationHTTPCalls) {
		return false
	}
	if !sumEquals(c.TotalHTTPCalls, c.PreSubmitHTTPCalls, c.CommitObjectCreationSubmissions,
		c.TargetRefUpdateSubmissions, c.PostMergeHTTPCalls, c.ReconciliationHTTPCalls, c.PrincipalValidationHTTPCalls) {
		return false
	}
	if c.CumulativeRequestBytes > limits.MaxCumulativeRequestBytes ||
		c.CumulativeResponseHeaderBytes > limits.MaxCumulativeResponseHeaderBytes ||
		c.CumulativeCompressedResponseBytes > limits.MaxCumulativeCompressedResponseBytes ||
		c.CumulativeDecompressedResponseBytes > limits.MaxCumulativeDecompressedResponseBytes ||
		c.CumulativeActiveProviderCallNanos > int64(limits.MaxCumulativeActiveProviderCallTime) ||
		c.ControllerInvocationNanos > int64(limits.MaxControllerInvocationTime) ||
		c.CumulativeActiveProviderCallNanos > c.ControllerInvocationNanos {
		return false
	}
	return true
}

func withinProduct(value, left, right int) bool {
	if value == 0 {
		return true
	}
	return left > 0 && right > 0 && (value-1)/right < left
}

func withinCombinedLimit(value, left, right int) bool {
	if value <= left {
		return true
	}
	return value-left <= right
}

func sumEquals(total int, values ...int) bool {
	remaining := total
	for _, value := range values {
		if value < 0 || value > remaining {
			return false
		}
		remaining -= value
	}
	return remaining == 0
}

type AuthorizationSealV1Input struct {
	MergeInput        MergeInput
	FinalRevalidation FinalRevalidationV1
}
type AuthorizationSealV1 struct {
	input     AuthorizationSealV1Input
	canonical []byte
	digest    string
	limitsSHA string
}

func NewAuthorizationSealV1(input AuthorizationSealV1Input, limits Limits) (AuthorizationSealV1, error) {
	input = cloneAuthorizationSealInput(input)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return AuthorizationSealV1{}, err
	}
	if err := validateMergeInput(input.MergeInput, limits); err != nil {
		return AuthorizationSealV1{}, err
	}
	if !input.FinalRevalidation.valid() || input.FinalRevalidation.input.MergeInput.SHA256() != input.MergeInput.SHA256() {
		return AuthorizationSealV1{}, errors.New("authorization seal does not bind one final authorized unsubmitted attempt")
	}
	revalidated, err := NewFinalRevalidationV1(input.FinalRevalidation.input, limits)
	if err != nil || revalidated.SHA256() != input.FinalRevalidation.SHA256() ||
		revalidated.FinalDecisionSHA256() != input.FinalRevalidation.FinalDecisionSHA256() ||
		!bytes.Equal(revalidated.CanonicalJSON(), input.FinalRevalidation.CanonicalJSON()) {
		return AuthorizationSealV1{}, errors.New("authorization seal final revalidation fails independent validation")
	}
	canonical, digest, err := canonicalJSON(authorizationSealWire(input, limitsSHA))
	if err != nil {
		return AuthorizationSealV1{}, err
	}
	if err := requireCanonicalObjectSize(canonical, limits.MaxCanonicalObjectBytes, "authorization decision seal"); err != nil {
		return AuthorizationSealV1{}, err
	}
	return AuthorizationSealV1{input, canonical, digest, limitsSHA}, nil
}
func (s AuthorizationSealV1) Input() AuthorizationSealV1Input {
	return cloneAuthorizationSealInput(s.input)
}
func (s AuthorizationSealV1) CanonicalJSON() []byte { return append([]byte(nil), s.canonical...) }
func (s AuthorizationSealV1) SHA256() string        { return s.digest }
func (s AuthorizationSealV1) MarshalJSON() ([]byte, error) {
	if !s.valid() {
		return nil, errors.New("authorization seal incomplete")
	}
	return s.CanonicalJSON(), nil
}
func (s AuthorizationSealV1) valid() bool {
	return len(s.canonical) > 0 && validSHA256(s.digest) && digestBytes(s.canonical) == s.digest && validSHA256(s.limitsSHA)
}

type authorizationSealWireV1 struct {
	Schema                           string                  `json:"schema"`
	MergeInputSHA256                 string                  `json:"merge_input_sha256"`
	Attempt                          writeAttemptWire        `json:"write_attempt"`
	AuthoritySHA256                  string                  `json:"authority_sha256"`
	ReadyBindingSHA256               string                  `json:"ready_binding_sha256"`
	PolicySHA256                     string                  `json:"policy_sha256"`
	FinalRevalidation                json.RawMessage         `json:"final_revalidation"`
	FinalRevalidationSHA256          string                  `json:"final_revalidation_sha256"`
	FinalDecisionSHA256              string                  `json:"final_decision_sha256"`
	FinalPullRequest                 json.RawMessage         `json:"final_pull_request"`
	FinalPullRequestSHA256           string                  `json:"final_pull_request_sha256"`
	FinalChecks                      []checkWire             `json:"final_checks"`
	FinalCheckRunsClosure            json.RawMessage         `json:"final_check_runs_closure"`
	FinalCheckRunsClosureSHA256      string                  `json:"final_check_runs_closure_sha256"`
	FinalCommitStatusesClosure       json.RawMessage         `json:"final_commit_statuses_closure"`
	FinalCommitStatusesClosureSHA256 string                  `json:"final_commit_statuses_closure_sha256"`
	PolicyDecisionSHA256             string                  `json:"policy_decision_sha256"`
	Verdict                          string                  `json:"verdict"`
	PREligible                       bool                    `json:"pr_eligible"`
	Capability                       json.RawMessage         `json:"provider_capability"`
	CapabilitySHA256                 string                  `json:"provider_capability_sha256"`
	Recipe                           json.RawMessage         `json:"recipe"`
	RecipeSHA256                     string                  `json:"recipe_sha256"`
	ExpectedResultSHA                string                  `json:"expected_result_sha"`
	BaseRef                          string                  `json:"base_ref"`
	BaseOID                          string                  `json:"base_oid"`
	HeadRef                          string                  `json:"head_ref"`
	HeadOID                          string                  `json:"head_oid"`
	Counters                         AuthorizationCountersV1 `json:"counters"`
	NoTargetRequestAttempted         bool                    `json:"no_target_request_attempted"`
	EvidenceRefs                     []ledger.EvidenceRef    `json:"evidence_refs"`
	LimitsSHA256                     string                  `json:"limits_sha256"`
}

func authorizationSealWire(i AuthorizationSealV1Input, limitsSHA string) authorizationSealWireV1 {
	authoritySHA, _ := i.MergeInput.authority.SHA256()
	final := i.FinalRevalidation.input
	checks := checkWires(final.Checks)
	return authorizationSealWireV1{AuthorizationSealSchemaV1, i.MergeInput.SHA256(), attemptWire(i.MergeInput.attempt), authoritySHA, i.MergeInput.authority.ReadyBinding().SHA256(), i.MergeInput.authority.MergePolicy().SHA256(),
		i.FinalRevalidation.CanonicalJSON(), i.FinalRevalidation.SHA256(), i.FinalRevalidation.FinalDecisionSHA256(),
		final.PullRequest.CanonicalJSON(), final.PullRequest.SHA256(), checks, final.CheckRunsClosure.CanonicalJSON(), final.CheckRunsClosure.SHA256(),
		final.CommitStatusesClosure.CanonicalJSON(), final.CommitStatusesClosure.SHA256(), i.FinalRevalidation.FinalDecisionSHA256(), "authorized", true,
		final.Capability.CanonicalJSON(), final.Capability.SHA256(), final.Recipe.CanonicalJSON(), final.Recipe.SHA256(), final.Recipe.ExpectedResultSHA().String(),
		"refs/heads/" + i.MergeInput.authority.BaseBranch().String(), i.MergeInput.authority.ExpectedBaseTipSHA().String(),
		"refs/heads/" + i.MergeInput.authority.HeadBranch().String(), i.MergeInput.authority.HeadSHA().String(), final.Counters,
		final.NoTargetRequestAttempted, final.EvidenceRefs, limitsSHA}
}

func ParseCanonicalAuthorizationSealV1(data []byte, input MergeInput, limits Limits) (AuthorizationSealV1, error) {
	if err := limits.Validate(); err != nil {
		return AuthorizationSealV1{}, err
	}
	if err := requireCanonicalObjectSize(data, limits.MaxCanonicalObjectBytes, "authorization decision seal"); err != nil {
		return AuthorizationSealV1{}, err
	}
	var wire authorizationSealWireV1
	if err := strictDecode(data, &wire); err != nil {
		return AuthorizationSealV1{}, err
	}
	if wire.Schema != AuthorizationSealSchemaV1 {
		return AuthorizationSealV1{}, errors.New("unsupported authorization seal schema")
	}
	if err := validateMergeInput(input, limits); err != nil {
		return AuthorizationSealV1{}, err
	}
	final, err := ParseCanonicalFinalRevalidationV1(wire.FinalRevalidation, input, limits)
	if err != nil {
		return AuthorizationSealV1{}, err
	}
	pr, err := ParseCanonicalAuthoritativePullRequestSnapshotV1(wire.FinalPullRequest, limits)
	if err != nil {
		return AuthorizationSealV1{}, err
	}
	checks := make([]Check, len(wire.FinalChecks))
	for index, item := range wire.FinalChecks {
		head, err := NewGitSHA(item.HeadSHA)
		if err != nil {
			return AuthorizationSealV1{}, err
		}
		checks[index] = Check{item.NodeID, item.Name, item.Identity, item.Status, item.Conclusion, head, append([]ledger.EvidenceRef(nil), item.EvidenceRefs...)}
	}
	checkRuns, err := ParseCanonicalPaginationClosureV1(wire.FinalCheckRunsClosure, limits)
	if err != nil {
		return AuthorizationSealV1{}, err
	}
	statuses, err := ParseCanonicalPaginationClosureV1(wire.FinalCommitStatusesClosure, limits)
	if err != nil {
		return AuthorizationSealV1{}, err
	}
	capability, err := ParseCanonicalProviderCapabilityV1(wire.Capability, limits)
	if err != nil {
		return AuthorizationSealV1{}, err
	}
	recipe, err := ParseCanonicalMergeCommitRecipeV1(wire.Recipe, input.authority, limits)
	if err != nil {
		return AuthorizationSealV1{}, err
	}
	value, err := NewAuthorizationSealV1(AuthorizationSealV1Input{input, final}, limits)
	if err != nil {
		return AuthorizationSealV1{}, err
	}
	authoritySHA, _ := input.authority.SHA256()
	if wire.MergeInputSHA256 != input.SHA256() || wire.Attempt != attemptWire(input.attempt) || wire.AuthoritySHA256 != authoritySHA ||
		wire.ReadyBindingSHA256 != input.authority.ReadyBinding().SHA256() || wire.PolicySHA256 != input.authority.MergePolicy().SHA256() ||
		wire.FinalRevalidationSHA256 != final.SHA256() || wire.FinalDecisionSHA256 != final.FinalDecisionSHA256() ||
		wire.FinalPullRequestSHA256 != pr.SHA256() || pr.SHA256() != final.input.PullRequest.SHA256() ||
		wire.FinalCheckRunsClosureSHA256 != checkRuns.SHA256() || checkRuns.SHA256() != final.input.CheckRunsClosure.SHA256() ||
		wire.FinalCommitStatusesClosureSHA256 != statuses.SHA256() || statuses.SHA256() != final.input.CommitStatusesClosure.SHA256() ||
		wire.PolicyDecisionSHA256 != final.FinalDecisionSHA256() || wire.Verdict != "authorized" || !wire.PREligible ||
		wire.CapabilitySHA256 != capability.SHA256() || capability.SHA256() != final.input.Capability.SHA256() ||
		wire.RecipeSHA256 != recipe.SHA256() || recipe.SHA256() != final.input.Recipe.SHA256() ||
		wire.ExpectedResultSHA != recipe.ExpectedResultSHA().String() || wire.Counters != final.input.Counters ||
		wire.NoTargetRequestAttempted != final.input.NoTargetRequestAttempted || !equalEvidence(wire.EvidenceRefs, final.input.EvidenceRefs) ||
		wire.LimitsSHA256 != value.limitsSHA {
		return AuthorizationSealV1{}, errors.New("authorization seal nested identity or digest disagrees")
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return AuthorizationSealV1{}, err
	}
	return value, nil
}

type RefUpdateV1 struct {
	Name      string `json:"name"`
	BeforeOID GitSHA `json:"-"`
	AfterOID  GitSHA `json:"-"`
	Force     bool   `json:"force"`
}
type TargetRefCommitmentV1 struct {
	repositoryNodeID string
	updates          [2]RefUpdateV1
	capabilitySHA    string
	sealSHA          string
	clientMutationID string
	canonical        []byte
	digest           string
}

func NewTargetRefCommitmentV1(input MergeInput, seal AuthorizationSealV1, limits Limits) (TargetRefCommitmentV1, error) {
	if err := validateMergeInput(input, limits); err != nil {
		return TargetRefCommitmentV1{}, err
	}
	if !seal.valid() || seal.input.MergeInput.SHA256() != input.SHA256() {
		return TargetRefCommitmentV1{}, errors.New("target commitment does not match sealed merge input")
	}
	clientMutationID := input.attempt.WriteID()
	repoNode := input.authority.ReadyBinding().input.RepositoryBinding.input.GitHubRepositoryNodeID
	updates := [2]RefUpdateV1{{"refs/heads/" + input.authority.BaseBranch().String(), input.authority.ExpectedBaseTipSHA(), input.recipe.ExpectedResultSHA(), false}, {"refs/heads/" + input.authority.HeadBranch().String(), input.authority.HeadSHA(), input.authority.HeadSHA(), false}}
	wire := targetRefCommitmentWireV1{TargetRefCommitmentSchemaV1, repoNode, []refUpdateWireV1{refUpdateWire(updates[0]), refUpdateWire(updates[1])}, input.capability.SHA256(), seal.SHA256(), clientMutationID}
	canonical, digest, err := canonicalJSON(wire)
	if err != nil {
		return TargetRefCommitmentV1{}, err
	}
	return TargetRefCommitmentV1{repoNode, updates, input.capability.SHA256(), seal.SHA256(), clientMutationID, canonical, digest}, nil
}
func (c TargetRefCommitmentV1) CanonicalJSON() []byte { return append([]byte(nil), c.canonical...) }
func (c TargetRefCommitmentV1) SHA256() string        { return c.digest }
func (c TargetRefCommitmentV1) valid() bool {
	return len(c.canonical) > 0 && validSHA256(c.digest) && digestBytes(c.canonical) == c.digest && c.updates[0].Name != c.updates[1].Name && !c.updates[0].Force && !c.updates[1].Force && c.updates[1].BeforeOID == c.updates[1].AfterOID
}

type refUpdateWireV1 struct {
	Name      string `json:"name"`
	BeforeOID string `json:"before_oid"`
	AfterOID  string `json:"after_oid"`
	Force     bool   `json:"force"`
}

func refUpdateWire(u RefUpdateV1) refUpdateWireV1 {
	return refUpdateWireV1{u.Name, u.BeforeOID.String(), u.AfterOID.String(), u.Force}
}

type targetRefCommitmentWireV1 struct {
	Schema                  string            `json:"schema"`
	RepositoryNodeID        string            `json:"repository_node_id"`
	RefUpdates              []refUpdateWireV1 `json:"ref_updates"`
	CapabilitySHA256        string            `json:"capability_sha256"`
	AuthorizationSealSHA256 string            `json:"authorization_seal_sha256"`
	ClientMutationID        string            `json:"client_mutation_id"`
}

func ParseCanonicalTargetRefCommitmentV1(data []byte, input MergeInput, seal AuthorizationSealV1, limits Limits) (TargetRefCommitmentV1, error) {
	var wire targetRefCommitmentWireV1
	if err := strictDecode(data, &wire); err != nil {
		return TargetRefCommitmentV1{}, err
	}
	if wire.Schema != TargetRefCommitmentSchemaV1 {
		return TargetRefCommitmentV1{}, errors.New("unsupported target commitment schema")
	}
	value, err := NewTargetRefCommitmentV1(input, seal, limits)
	if err != nil {
		return TargetRefCommitmentV1{}, err
	}
	if wire.ClientMutationID != input.attempt.WriteID() {
		return TargetRefCommitmentV1{}, errors.New("target commitment mutation identity was not derived from the write attempt")
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return TargetRefCommitmentV1{}, err
	}
	return value, nil
}

type SealedMergeAuthorizationV1Input struct {
	MergeInput MergeInput
	Seal       AuthorizationSealV1
	Commitment TargetRefCommitmentV1
}
type SealedMergeAuthorizationV1 struct {
	input     SealedMergeAuthorizationV1Input
	canonical []byte
	digest    string
	limitsSHA string
}

func NewSealedMergeAuthorizationV1(input SealedMergeAuthorizationV1Input, limits Limits) (SealedMergeAuthorizationV1, error) {
	input = cloneSealedInput(input)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return SealedMergeAuthorizationV1{}, err
	}
	if err := validateMergeInput(input.MergeInput, limits); err != nil {
		return SealedMergeAuthorizationV1{}, err
	}
	if !input.Seal.valid() || input.Seal.input.MergeInput.SHA256() != input.MergeInput.SHA256() || !input.Commitment.valid() || input.Commitment.sealSHA != input.Seal.SHA256() || input.Commitment.capabilitySHA != input.MergeInput.capability.SHA256() {
		return SealedMergeAuthorizationV1{}, errors.New("sealed authorization chain is inconsistent")
	}
	wire := sealedMergeAuthorizationWireV1{SealedMergeAuthorizationSchemaV1, input.MergeInput.CanonicalPayload(), input.MergeInput.SHA256(), input.Seal.CanonicalJSON(), input.Seal.SHA256(), input.Commitment.CanonicalJSON(), input.Commitment.SHA256(), limitsSHA}
	canonical, digest, err := canonicalJSON(wire)
	if err != nil {
		return SealedMergeAuthorizationV1{}, err
	}
	return SealedMergeAuthorizationV1{input, canonical, digest, limitsSHA}, nil
}

type sealedMergeAuthorizationWireV1 struct {
	Schema           string          `json:"schema"`
	MergeInput       json.RawMessage `json:"merge_input"`
	MergeInputSHA256 string          `json:"merge_input_sha256"`
	Seal             json.RawMessage `json:"authorization_seal"`
	SealSHA256       string          `json:"authorization_seal_sha256"`
	Commitment       json.RawMessage `json:"target_ref_commitment"`
	CommitmentSHA256 string          `json:"target_ref_commitment_sha256"`
	LimitsSHA256     string          `json:"limits_sha256"`
}

func ParseCanonicalSealedMergeAuthorizationV1(data []byte, limits Limits) (SealedMergeAuthorizationV1, error) {
	var wire sealedMergeAuthorizationWireV1
	if err := strictDecode(data, &wire); err != nil {
		return SealedMergeAuthorizationV1{}, err
	}
	if wire.Schema != SealedMergeAuthorizationSchemaV1 {
		return SealedMergeAuthorizationV1{}, errors.New("unsupported sealed authorization schema")
	}
	input, err := ParseCanonicalMergeInput(wire.MergeInput, limits)
	if err != nil {
		return SealedMergeAuthorizationV1{}, err
	}
	seal, err := ParseCanonicalAuthorizationSealV1(wire.Seal, input, limits)
	if err != nil {
		return SealedMergeAuthorizationV1{}, err
	}
	commitment, err := ParseCanonicalTargetRefCommitmentV1(wire.Commitment, input, seal, limits)
	if err != nil {
		return SealedMergeAuthorizationV1{}, err
	}
	value, err := NewSealedMergeAuthorizationV1(SealedMergeAuthorizationV1Input{input, seal, commitment}, limits)
	if err != nil {
		return SealedMergeAuthorizationV1{}, err
	}
	if wire.MergeInputSHA256 != input.SHA256() || wire.SealSHA256 != seal.SHA256() || wire.CommitmentSHA256 != commitment.SHA256() || wire.LimitsSHA256 != value.limitsSHA {
		return SealedMergeAuthorizationV1{}, errors.New("sealed authorization nested digest or limits disagree")
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return SealedMergeAuthorizationV1{}, err
	}
	return value, nil
}
func (s SealedMergeAuthorizationV1) MergeInput() MergeInput {
	return cloneLifecycleMergeInput(s.input.MergeInput)
}
func (s SealedMergeAuthorizationV1) Seal() AuthorizationSealV1 {
	return cloneAuthorizationSeal(s.input.Seal)
}
func (s SealedMergeAuthorizationV1) Commitment() TargetRefCommitmentV1 {
	return cloneTargetCommitment(s.input.Commitment)
}
func (s SealedMergeAuthorizationV1) CanonicalJSON() []byte {
	return append([]byte(nil), s.canonical...)
}
func (s SealedMergeAuthorizationV1) SHA256() string { return s.digest }
func (s SealedMergeAuthorizationV1) MarshalJSON() ([]byte, error) {
	if !s.valid() {
		return nil, errors.New("sealed authorization incomplete")
	}
	return s.CanonicalJSON(), nil
}
func (s SealedMergeAuthorizationV1) valid() bool {
	return len(s.canonical) > 0 && validSHA256(s.digest) && digestBytes(s.canonical) == s.digest && validSHA256(s.limitsSHA)
}

func checkWires(checks []Check) []checkWire {
	result := make([]checkWire, len(checks))
	for i, c := range checks {
		result[i] = checkWire{c.NodeID, c.Name, c.Identity, c.Status, c.Conclusion, c.HeadSHA.String(), c.EvidenceRefs}
	}
	return result
}
func cloneChecks(checks []Check) []Check {
	result := append([]Check(nil), checks...)
	for i := range result {
		result[i].EvidenceRefs = append([]ledger.EvidenceRef(nil), result[i].EvidenceRefs...)
	}
	return result
}
func cloneRecipe(r MergeCommitRecipeV1) MergeCommitRecipeV1 {
	r.input.Parents = append([]GitSHA(nil), r.input.Parents...)
	r.canonical = append([]byte(nil), r.canonical...)
	r.commitBytes = append([]byte(nil), r.commitBytes...)
	return r
}
func cloneCapability(c ProviderCapabilityV1) ProviderCapabilityV1 {
	c.input.EvidenceRefs = append([]ledger.EvidenceRef(nil), c.input.EvidenceRefs...)
	c.canonical = append([]byte(nil), c.canonical...)
	return c
}
func cloneAuthorizationSealInput(i AuthorizationSealV1Input) AuthorizationSealV1Input {
	i.MergeInput = cloneLifecycleMergeInput(i.MergeInput)
	i.FinalRevalidation = cloneFinalRevalidation(i.FinalRevalidation)
	return i
}
func cloneAuthorizationSeal(s AuthorizationSealV1) AuthorizationSealV1 {
	s.input = cloneAuthorizationSealInput(s.input)
	s.canonical = append([]byte(nil), s.canonical...)
	return s
}
func cloneAuthoritativePR(s AuthoritativePullRequestSnapshotV1) AuthoritativePullRequestSnapshotV1 {
	s.input = cloneAuthoritativePRInput(s.input)
	s.canonical = append([]byte(nil), s.canonical...)
	return s
}
func cloneTargetCommitment(c TargetRefCommitmentV1) TargetRefCommitmentV1 {
	c.canonical = append([]byte(nil), c.canonical...)
	return c
}
func cloneSealedInput(i SealedMergeAuthorizationV1Input) SealedMergeAuthorizationV1Input {
	i.MergeInput = cloneLifecycleMergeInput(i.MergeInput)
	i.Seal = cloneAuthorizationSeal(i.Seal)
	i.Commitment = cloneTargetCommitment(i.Commitment)
	return i
}

type mergeInputWireV1 struct {
	Authority                   json.RawMessage      `json:"authority"`
	PolicyDecisionSHA256        string               `json:"policy_decision_sha256"`
	InitialPullRequest          json.RawMessage      `json:"initial_pull_request"`
	InitialPullRequestSHA256    string               `json:"initial_pull_request_sha256"`
	Checks                      []checkWire          `json:"checks"`
	CheckRunsClosure            json.RawMessage      `json:"check_runs_closure"`
	CheckRunsClosureSHA256      string               `json:"check_runs_closure_sha256"`
	CommitStatusesClosure       json.RawMessage      `json:"commit_statuses_closure"`
	CommitStatusesClosureSHA256 string               `json:"commit_statuses_closure_sha256"`
	Capability                  json.RawMessage      `json:"provider_capability"`
	CapabilitySHA256            string               `json:"provider_capability_sha256"`
	Recipe                      json.RawMessage      `json:"merge_commit_recipe"`
	RecipeSHA256                string               `json:"merge_commit_recipe_sha256"`
	ExpectedResultSHA           string               `json:"expected_result_sha"`
	EvidenceRefs                []ledger.EvidenceRef `json:"approval_evidence"`
	LimitsSHA256                string               `json:"limits_sha256"`
}

func mergeAuthorizationPayloadWire(input MergeAuthorizationInputV1, authority Authority, limitsSHA string) mergeInputWireV1 {
	authorityJSON, _ := authority.CanonicalJSON()
	return mergeInputWireV1{authorityJSON, input.PolicyDecisionSHA256, input.InitialPullRequest.CanonicalJSON(), input.InitialPullRequest.SHA256(), checkWires(input.Checks), input.CheckRunsClosure.CanonicalJSON(), input.CheckRunsClosure.SHA256(), input.CommitStatusesClosure.CanonicalJSON(), input.CommitStatusesClosure.SHA256(), input.Capability.CanonicalJSON(), input.Capability.SHA256(), input.Recipe.CanonicalJSON(), input.Recipe.SHA256(), input.Recipe.ExpectedResultSHA().String(), input.EvidenceRefs, limitsSHA}
}

func ParseCanonicalMergeInput(data []byte, limits Limits) (MergeInput, error) {
	if err := limits.Validate(); err != nil {
		return MergeInput{}, err
	}
	if err := requireCanonicalObjectSize(data, limits.MaxCanonicalObjectBytes, "merge input"); err != nil {
		return MergeInput{}, err
	}
	var wire mergeInputWireV1
	if err := strictDecode(data, &wire); err != nil {
		return MergeInput{}, err
	}
	authority, err := ParseCanonicalAuthority(wire.Authority, limits)
	if err != nil {
		return MergeInput{}, err
	}
	pr, err := ParseCanonicalAuthoritativePullRequestSnapshotV1(wire.InitialPullRequest, limits)
	if err != nil {
		return MergeInput{}, err
	}
	checks := make([]Check, len(wire.Checks))
	for index, item := range wire.Checks {
		head, err := NewGitSHA(item.HeadSHA)
		if err != nil {
			return MergeInput{}, err
		}
		checks[index] = Check{item.NodeID, item.Name, item.Identity, item.Status, item.Conclusion, head, append([]ledger.EvidenceRef(nil), item.EvidenceRefs...)}
	}
	checkRuns, err := ParseCanonicalPaginationClosureV1(wire.CheckRunsClosure, limits)
	if err != nil {
		return MergeInput{}, err
	}
	statuses, err := ParseCanonicalPaginationClosureV1(wire.CommitStatusesClosure, limits)
	if err != nil {
		return MergeInput{}, err
	}
	capability, err := ParseCanonicalProviderCapabilityV1(wire.Capability, limits)
	if err != nil {
		return MergeInput{}, err
	}
	recipe, err := ParseCanonicalMergeCommitRecipeV1(wire.Recipe, authority, limits)
	if err != nil {
		return MergeInput{}, err
	}
	value, err := NewMergeInput(MergeAuthorizationInputV1{authority, wire.PolicyDecisionSHA256, pr, checks, checkRuns, statuses, capability, recipe, wire.EvidenceRefs}, recipe.input.WriteID, limits)
	if err != nil {
		return MergeInput{}, err
	}
	if wire.InitialPullRequestSHA256 != pr.SHA256() || wire.CheckRunsClosureSHA256 != checkRuns.SHA256() || wire.CommitStatusesClosureSHA256 != statuses.SHA256() || wire.CapabilitySHA256 != capability.SHA256() || wire.RecipeSHA256 != recipe.SHA256() || wire.ExpectedResultSHA != recipe.ExpectedResultSHA().String() || wire.LimitsSHA256 != value.limitsSHA256 {
		return MergeInput{}, errors.New("merge input nested digest, result OID, or limits identity disagrees")
	}
	if err := requireCanonical(data, value.canonicalPayload); err != nil {
		return MergeInput{}, err
	}
	return value, nil
}

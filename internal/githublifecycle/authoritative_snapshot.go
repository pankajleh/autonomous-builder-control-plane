package githublifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	AuthoritativePullRequestSnapshotSchemaV1 = "authoritative-pull-request-snapshot-v1"
	GitHubPullRequestResponseEvidenceKindV1  = "github-pull-request-response-body"
	GitHubPullRequestEnvelopeEvidenceKindV1  = "github-pull-request-response-envelope"
	GitHubPullRequestEnvelopeSchemaV1        = "github-pull-request-response-envelope-v1"
)

type AuthoritativePullRequestSnapshotV1Input struct {
	Snapshot              SnapshotIdentity
	ResponseBodySHA256    string
	APIVersion            string
	RepositoryBinding     RepositoryBindingV1
	PullRequest           PullRequestIdentity
	PullRequestDatabaseID int64
	BaseRepositoryNodeID  string
	BaseRef               string
	BaseOID               GitSHA
	HeadRepositoryNodeID  string
	HeadRef               string
	HeadOID               GitSHA
	State                 *PullRequestState
	IsDraft               *bool
	Merged                *bool
	MergedAtUnixNano      *int64
	Actor                 ActingIdentity
	Reviews               []Review
	ReviewsClosure        PaginationClosureV1
	EvidenceRefs          []ledger.EvidenceRef
}

type AuthoritativePullRequestSnapshotV1 struct {
	input     AuthoritativePullRequestSnapshotV1Input
	canonical []byte
	digest    string
	limitsSHA string
}

func NewPullRequestEnvelopeEvidenceV1(uri string, input AuthoritativePullRequestSnapshotV1Input, limits Limits) (ledger.EvidenceRef, error) {
	if err := limits.Validate(); err != nil {
		return ledger.EvidenceRef{}, err
	}
	if !validText(uri, limits.MaxTextBytes, false) || !input.Snapshot.valid() || input.Snapshot.Provider() != "github" ||
		!validSHA256(input.ResponseBodySHA256) || !input.RepositoryBinding.valid() || !input.PullRequest.valid() ||
		!input.BaseOID.valid() || !input.HeadOID.valid() || !input.Actor.valid() {
		return ledger.EvidenceRef{}, errors.New("authoritative PR response envelope identity is invalid")
	}
	canonical, _, err := canonicalJSON(pullRequestResponseEnvelopeWire(input))
	if err != nil {
		return ledger.EvidenceRef{}, err
	}
	return ledger.EvidenceRef{URI: uri, Kind: GitHubPullRequestEnvelopeEvidenceKindV1, SHA256: digestBytes(canonical)}, nil
}

type pullRequestResponseEnvelopeWireV1 struct {
	Schema                  string            `json:"schema"`
	Snapshot                identityWire      `json:"snapshot"`
	ResponseBodySHA256      string            `json:"response_body_sha256"`
	APIVersion              string            `json:"api_version"`
	RepositoryBindingSHA256 string            `json:"repository_binding_sha256"`
	PullRequest             prIdentityWire    `json:"pull_request"`
	PullRequestDatabaseID   int64             `json:"pull_request_database_id"`
	BaseRepositoryNodeID    string            `json:"base_repository_node_id"`
	BaseRef                 string            `json:"base_ref"`
	BaseOID                 string            `json:"base_oid"`
	HeadRepositoryNodeID    string            `json:"head_repository_node_id"`
	HeadRef                 string            `json:"head_ref"`
	HeadOID                 string            `json:"head_oid"`
	State                   *PullRequestState `json:"state"`
	IsDraft                 *bool             `json:"is_draft"`
	Merged                  *bool             `json:"merged"`
	MergedAtUnixNano        *int64            `json:"merged_at_unix_nano"`
	Actor                   actorWire         `json:"actor"`
}

func pullRequestResponseEnvelopeWire(input AuthoritativePullRequestSnapshotV1Input) pullRequestResponseEnvelopeWireV1 {
	return pullRequestResponseEnvelopeWireV1{
		GitHubPullRequestEnvelopeSchemaV1, snapshotWire(input.Snapshot), input.ResponseBodySHA256, input.APIVersion, input.RepositoryBinding.SHA256(),
		pullRequestWire(input.PullRequest), input.PullRequestDatabaseID, input.BaseRepositoryNodeID, input.BaseRef,
		input.BaseOID.String(), input.HeadRepositoryNodeID, input.HeadRef, input.HeadOID.String(), input.State,
		input.IsDraft, input.Merged, input.MergedAtUnixNano, actingWire(input.Actor),
	}
}

func NewAuthoritativePullRequestSnapshotV1(input AuthoritativePullRequestSnapshotV1Input, limits Limits) (AuthoritativePullRequestSnapshotV1, error) {
	input = cloneAuthoritativePRInput(input)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return AuthoritativePullRequestSnapshotV1{}, err
	}
	if !input.Snapshot.valid() || input.Snapshot.Provider() != "github" || !validSHA256(input.ResponseBodySHA256) || !validText(input.APIVersion, limits.MaxTextBytes, false) ||
		!input.RepositoryBinding.valid() || !input.PullRequest.valid() || input.PullRequestDatabaseID <= 0 || !input.Actor.valid() || !input.BaseOID.valid() || !input.HeadOID.valid() {
		return AuthoritativePullRequestSnapshotV1{}, errors.New("authoritative PR snapshot identity is invalid")
	}
	repositoryNode := input.RepositoryBinding.input.GitHubRepositoryNodeID
	if input.BaseRepositoryNodeID != repositoryNode || input.HeadRepositoryNodeID != repositoryNode {
		return AuthoritativePullRequestSnapshotV1{}, errors.New("fork or mismatched PR repository is not eligible")
	}
	baseBranch, err := branchFromFullRef(input.BaseRef)
	if err != nil {
		return AuthoritativePullRequestSnapshotV1{}, err
	}
	headBranch, err := branchFromFullRef(input.HeadRef)
	if err != nil {
		return AuthoritativePullRequestSnapshotV1{}, err
	}
	if baseBranch == headBranch {
		return AuthoritativePullRequestSnapshotV1{}, errors.New("base and head refs must be distinct")
	}
	if input.State == nil || *input.State != PullRequestOpen || input.IsDraft == nil || *input.IsDraft || input.Merged == nil || *input.Merged || input.MergedAtUnixNano != nil {
		return AuthoritativePullRequestSnapshotV1{}, errors.New("pull request is not authoritatively open, non-draft, and unmerged")
	}
	if len(input.Reviews) > limits.MaxTotalItems {
		return AuthoritativePullRequestSnapshotV1{}, errors.New("review collection exceeds limits")
	}
	reviewItems := make([]CanonicalPaginationItemV1, len(input.Reviews))
	seen := map[string]struct{}{}
	for index, review := range input.Reviews {
		if !validOpaqueID(review.NodeID, limits.MaxTextBytes) || review.DatabaseID <= 0 || !review.Reviewer.valid() || !review.CommitSHA.valid() ||
			(review.State != ReviewApproved && review.State != ReviewChangesRequested && review.State != ReviewCommented && review.State != ReviewDismissed) {
			return AuthoritativePullRequestSnapshotV1{}, fmt.Errorf("review %d is invalid", index)
		}
		key := fmt.Sprintf("%d/%s", review.DatabaseID, review.NodeID)
		if _, ok := seen[key]; ok {
			return AuthoritativePullRequestSnapshotV1{}, errors.New("review identity is duplicated")
		}
		seen[key] = struct{}{}
		raw, _ := json.Marshal(reviewWire{review.NodeID, review.DatabaseID, review.Reviewer, review.State, review.CommitSHA.String()})
		reviewItems[index] = CanonicalPaginationItemV1{Key: key, SHA256: digestBytes(raw)}
	}
	if !input.ReviewsClosure.valid() || input.ReviewsClosure.input.Query.Source != PaginationReviews {
		return AuthoritativePullRequestSnapshotV1{}, errors.New("independent reviews pagination closure is required")
	}
	scope := PaginationQueryScopeV1{
		Source: PaginationReviews, Repository: input.RepositoryBinding.input.GitHubRepository,
		RepositoryNodeID: repositoryNode, PullRequest: &input.PullRequest, HeadSHA: input.HeadOID,
	}
	if err := ValidatePaginationClosureV1(scope, input.ReviewsClosure, reviewItems, limits); err != nil {
		return AuthoritativePullRequestSnapshotV1{}, err
	}
	sort.Slice(input.Reviews, func(i, j int) bool {
		left := fmt.Sprintf("%020d/%s", input.Reviews[i].DatabaseID, input.Reviews[i].NodeID)
		right := fmt.Sprintf("%020d/%s", input.Reviews[j].DatabaseID, input.Reviews[j].NodeID)
		return left < right
	})
	if input.APIVersion != GitHubAPIVersionV1 {
		return AuthoritativePullRequestSnapshotV1{}, errors.New("authoritative PR API version is not the frozen version")
	}
	if len(input.EvidenceRefs) == 0 || canonicalizeEvidence(&input.EvidenceRefs, limits) != nil {
		return AuthoritativePullRequestSnapshotV1{}, errors.New("authoritative PR evidence is invalid")
	}
	if !containsEvidenceDigest(input.EvidenceRefs, GitHubPullRequestResponseEvidenceKindV1, input.ResponseBodySHA256) {
		return AuthoritativePullRequestSnapshotV1{}, errors.New("authoritative PR evidence does not retain the exact GitHub response body")
	}
	responseEnvelope, _, err := canonicalJSON(pullRequestResponseEnvelopeWire(input))
	if err != nil || !containsEvidenceDigest(input.EvidenceRefs, GitHubPullRequestEnvelopeEvidenceKindV1, digestBytes(responseEnvelope)) {
		return AuthoritativePullRequestSnapshotV1{}, errors.New("authoritative PR evidence does not bind the GitHub request, body, and decoded response fields")
	}
	canonical, digest, err := canonicalJSON(authoritativePRWire(input, limitsSHA))
	if err != nil {
		return AuthoritativePullRequestSnapshotV1{}, err
	}
	return AuthoritativePullRequestSnapshotV1{input, canonical, digest, limitsSHA}, nil
}

func (s AuthoritativePullRequestSnapshotV1) Input() AuthoritativePullRequestSnapshotV1Input {
	return cloneAuthoritativePRInput(s.input)
}
func (s AuthoritativePullRequestSnapshotV1) CanonicalJSON() []byte {
	return append([]byte(nil), s.canonical...)
}
func (s AuthoritativePullRequestSnapshotV1) SHA256() string { return s.digest }
func (s AuthoritativePullRequestSnapshotV1) MarshalJSON() ([]byte, error) {
	if !s.valid() {
		return nil, errors.New("authoritative PR snapshot incomplete")
	}
	return s.CanonicalJSON(), nil
}
func (s AuthoritativePullRequestSnapshotV1) valid() bool {
	return len(s.canonical) > 0 && validSHA256(s.digest) && digestBytes(s.canonical) == s.digest && validSHA256(s.limitsSHA)
}

type authoritativePRWireV1 struct {
	Schema                string               `json:"schema"`
	Snapshot              identityWire         `json:"snapshot"`
	ResponseBodySHA256    string               `json:"response_body_sha256"`
	APIVersion            string               `json:"api_version"`
	RepositoryBinding     json.RawMessage      `json:"repository_binding"`
	PullRequest           prIdentityWire       `json:"pull_request"`
	PullRequestDatabaseID int64                `json:"pull_request_database_id"`
	BaseRepositoryNodeID  string               `json:"base_repository_node_id"`
	BaseRef               string               `json:"base_ref"`
	BaseOID               string               `json:"base_oid"`
	HeadRepositoryNodeID  string               `json:"head_repository_node_id"`
	HeadRef               string               `json:"head_ref"`
	HeadOID               string               `json:"head_oid"`
	State                 *PullRequestState    `json:"state"`
	IsDraft               *bool                `json:"is_draft"`
	Merged                *bool                `json:"merged"`
	MergedAtUnixNano      *int64               `json:"merged_at_unix_nano"`
	Actor                 actorWire            `json:"actor"`
	Reviews               []reviewWire         `json:"reviews"`
	ReviewsClosure        json.RawMessage      `json:"reviews_closure"`
	ReviewsClosureSHA256  string               `json:"reviews_closure_sha256"`
	EvidenceRefs          []ledger.EvidenceRef `json:"evidence_refs"`
	LimitsSHA256          string               `json:"limits_sha256"`
}

func authoritativePRWire(input AuthoritativePullRequestSnapshotV1Input, limitsSHA string) authoritativePRWireV1 {
	reviews := make([]reviewWire, len(input.Reviews))
	for index, review := range input.Reviews {
		reviews[index] = reviewWire{review.NodeID, review.DatabaseID, review.Reviewer, review.State, review.CommitSHA.String()}
	}
	return authoritativePRWireV1{AuthoritativePullRequestSnapshotSchemaV1, snapshotWire(input.Snapshot), input.ResponseBodySHA256, input.APIVersion,
		input.RepositoryBinding.CanonicalJSON(), pullRequestWire(input.PullRequest), input.PullRequestDatabaseID, input.BaseRepositoryNodeID,
		input.BaseRef, input.BaseOID.String(), input.HeadRepositoryNodeID, input.HeadRef, input.HeadOID.String(), input.State, input.IsDraft, input.Merged,
		input.MergedAtUnixNano, actingWire(input.Actor), reviews, input.ReviewsClosure.CanonicalJSON(), input.ReviewsClosure.SHA256(), input.EvidenceRefs, limitsSHA}
}

func ValidateAuthoritativePullRequestSnapshotV1(authority Authority, snapshot AuthoritativePullRequestSnapshotV1, limits Limits) error {
	if err := requireAuthority(authority); err != nil {
		return err
	}
	if !snapshot.valid() {
		return errors.New("authoritative PR snapshot incomplete")
	}
	if err := requireLimitsSHA(limits, snapshot.limitsSHA); err != nil {
		return err
	}
	rebuilt, err := NewAuthoritativePullRequestSnapshotV1(snapshot.input, limits)
	if err != nil || rebuilt.digest != snapshot.digest || !bytes.Equal(rebuilt.canonical, snapshot.canonical) {
		return errors.New("authoritative PR snapshot fails independent validation")
	}
	input := snapshot.input
	pr, ok := authority.PullRequest()
	if !ok || input.PullRequest != pr || input.RepositoryBinding.SHA256() != authority.ReadyBinding().RepositoryBinding().SHA256() || input.Actor != authority.Actor() ||
		input.BaseRef != "refs/heads/"+authority.BaseBranch().String() || input.HeadRef != "refs/heads/"+authority.HeadBranch().String() ||
		input.BaseOID != authority.ExpectedBaseTipSHA() || input.HeadOID != authority.HeadSHA() {
		return errors.New("authoritative PR snapshot does not match authority")
	}
	return nil
}

func ParseCanonicalAuthoritativePullRequestSnapshotV1(data []byte, limits Limits) (AuthoritativePullRequestSnapshotV1, error) {
	var wire authoritativePRWireV1
	if err := strictDecode(data, &wire); err != nil {
		return AuthoritativePullRequestSnapshotV1{}, err
	}
	if wire.Schema != AuthoritativePullRequestSnapshotSchemaV1 {
		return AuthoritativePullRequestSnapshotV1{}, errors.New("unsupported authoritative PR schema")
	}
	snapshot, err := NewSnapshotIdentity(wire.Snapshot.Provider, wire.Snapshot.RequestID, wire.Snapshot.ObservedUnixNano)
	if err != nil {
		return AuthoritativePullRequestSnapshotV1{}, err
	}
	repositoryBinding, err := ParseCanonicalRepositoryBindingV1(wire.RepositoryBinding)
	if err != nil {
		return AuthoritativePullRequestSnapshotV1{}, err
	}
	pr, err := NewPullRequestIdentity(wire.PullRequest.Number, wire.PullRequest.NodeID)
	if err != nil {
		return AuthoritativePullRequestSnapshotV1{}, err
	}
	base, err := NewGitSHA(wire.BaseOID)
	if err != nil {
		return AuthoritativePullRequestSnapshotV1{}, err
	}
	head, err := NewGitSHA(wire.HeadOID)
	if err != nil {
		return AuthoritativePullRequestSnapshotV1{}, err
	}
	actor, err := actorFromWire(wire.Actor)
	if err != nil {
		return AuthoritativePullRequestSnapshotV1{}, err
	}
	reviews := make([]Review, len(wire.Reviews))
	for index, item := range wire.Reviews {
		sha, err := NewGitSHA(item.CommitSHA)
		if err != nil {
			return AuthoritativePullRequestSnapshotV1{}, err
		}
		reviews[index] = Review{item.NodeID, item.DatabaseID, item.Reviewer, item.State, sha}
	}
	closure, err := ParseCanonicalPaginationClosureV1(wire.ReviewsClosure, limits)
	if err != nil {
		return AuthoritativePullRequestSnapshotV1{}, err
	}
	value, err := NewAuthoritativePullRequestSnapshotV1(AuthoritativePullRequestSnapshotV1Input{snapshot, wire.ResponseBodySHA256, wire.APIVersion, repositoryBinding, pr, wire.PullRequestDatabaseID, wire.BaseRepositoryNodeID, wire.BaseRef, base, wire.HeadRepositoryNodeID, wire.HeadRef, head, wire.State, wire.IsDraft, wire.Merged, wire.MergedAtUnixNano, actor, reviews, closure, wire.EvidenceRefs}, limits)
	if err != nil {
		return AuthoritativePullRequestSnapshotV1{}, err
	}
	if wire.ReviewsClosureSHA256 != closure.SHA256() || wire.LimitsSHA256 != value.limitsSHA {
		return AuthoritativePullRequestSnapshotV1{}, errors.New("authoritative PR nested digest or limits disagree")
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return AuthoritativePullRequestSnapshotV1{}, err
	}
	return value, nil
}

func EvaluateMergePolicyV1(authority Authority, pr AuthoritativePullRequestSnapshotV1, checks []Check, checkRuns, commitStatuses PaginationClosureV1, limits Limits) error {
	if err := ValidateAuthoritativePullRequestSnapshotV1(authority, pr, limits); err != nil {
		return err
	}
	checks, err := canonicalizeChecksForHead(checks, authority.HeadSHA(), limits)
	if err != nil {
		return err
	}
	policy := authority.MergePolicy()
	runItems, statusItems := []CanonicalPaginationItemV1{}, []CanonicalPaginationItemV1{}
	observed, success := map[string]int{}, map[string]bool{}
	for index, check := range checks {
		raw, err := json.Marshal(checkWire{check.NodeID, check.Name, check.Identity, check.Status, check.Conclusion, check.HeadSHA.String(), check.EvidenceRefs})
		if err != nil {
			return fmt.Errorf("check %d canonicalization failed: %w", index, err)
		}
		item := CanonicalPaginationItemV1{Key: check.NodeID, SHA256: digestBytes(raw)}
		if check.Identity.Source == CheckSourceCheckRun {
			runItems = append(runItems, item)
		} else {
			statusItems = append(statusItems, item)
		}
		key := checkIdentityKey(check.Identity)
		observed[key]++
		success[key] = check.Status == CheckCompleted && check.Conclusion == ConclusionSuccess
	}
	if checkRuns.input.Query.Source != PaginationCheckRuns || commitStatuses.input.Query.Source != PaginationCommitStatuses {
		return errors.New("both independent check source closures are required")
	}
	repositoryBinding := authority.ReadyBinding().RepositoryBinding()
	if err := ValidatePaginationClosureV1(PaginationQueryScopeV1{
		Source: PaginationCheckRuns, Repository: authority.Repository(),
		RepositoryNodeID: repositoryBinding.input.GitHubRepositoryNodeID, HeadSHA: authority.HeadSHA(),
	}, checkRuns, runItems, limits); err != nil {
		return err
	}
	if err := ValidatePaginationClosureV1(PaginationQueryScopeV1{
		Source: PaginationCommitStatuses, Repository: authority.Repository(),
		RepositoryNodeID: repositoryBinding.input.GitHubRepositoryNodeID, HeadSHA: authority.HeadSHA(),
	}, commitStatuses, statusItems, limits); err != nil {
		return err
	}
	for _, required := range policy.input.RequiredChecks {
		key := checkIdentityKey(required)
		if observed[key] != 1 || !success[key] {
			return errors.New("required trusted check is missing, duplicate, pending, failed, or untrusted")
		}
	}
	approved, blocked, eligible := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, identity := range policy.input.EligibleReviewers {
		eligible[stableIdentityKey(identity)] = true
	}
	for _, review := range pr.input.Reviews {
		key := stableIdentityKey(review.Reviewer)
		if !eligible[key] || review.CommitSHA != authority.HeadSHA() || review.State == ReviewCommented {
			continue
		}
		if review.State == ReviewChangesRequested || review.State == ReviewDismissed {
			blocked[key] = true
		}
		if review.State == ReviewApproved {
			approved[key] = true
		}
	}
	count := 0
	for key := range approved {
		if !blocked[key] {
			count++
		}
	}
	for _, required := range policy.input.RequiredReviewers {
		key := stableIdentityKey(required)
		if !approved[key] || blocked[key] {
			return errors.New("required reviewer lacks an unblocked exact-head approval")
		}
	}
	if count < policy.input.MinimumApprovals {
		return errors.New("minimum unique exact-head approvals not met")
	}
	for key := range blocked {
		if blocked[key] {
			return errors.New("exact-head changes-requested or dismissed review blocks authorization; review history is unproven")
		}
	}
	return nil
}

func branchFromFullRef(ref string) (Branch, error) {
	if !strings.HasPrefix(ref, "refs/heads/") {
		return Branch{}, errors.New("PR ref is not a full branch ref")
	}
	return NewBranch(strings.TrimPrefix(ref, "refs/heads/"))
}

func cloneAuthoritativePRInput(input AuthoritativePullRequestSnapshotV1Input) AuthoritativePullRequestSnapshotV1Input {
	input.RepositoryBinding = cloneRepositoryBinding(input.RepositoryBinding)
	input.Reviews = append([]Review(nil), input.Reviews...)
	input.ReviewsClosure = clonePaginationClosure(input.ReviewsClosure)
	input.EvidenceRefs = append([]ledger.EvidenceRef(nil), input.EvidenceRefs...)
	if input.State != nil {
		value := *input.State
		input.State = &value
	}
	if input.IsDraft != nil {
		value := *input.IsDraft
		input.IsDraft = &value
	}
	if input.Merged != nil {
		value := *input.Merged
		input.Merged = &value
	}
	if input.MergedAtUnixNano != nil {
		value := *input.MergedAtUnixNano
		input.MergedAtUnixNano = &value
	}
	return input
}

func cloneRepositoryBinding(input RepositoryBindingV1) RepositoryBindingV1 {
	input.canonical = append([]byte(nil), input.canonical...)
	return input
}

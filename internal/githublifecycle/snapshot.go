package githublifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

type PullRequestState string

const (
	PullRequestOpen   PullRequestState = "open"
	PullRequestClosed PullRequestState = "closed"
	PullRequestMerged PullRequestState = "merged"
)

type ReviewState string

const (
	ReviewApproved         ReviewState = "approved"
	ReviewChangesRequested ReviewState = "changes_requested"
	ReviewCommented        ReviewState = "commented"
	ReviewDismissed        ReviewState = "dismissed"
)

type Review struct {
	NodeID     string           `json:"node_id"`
	DatabaseID int64            `json:"database_id"`
	Reviewer   StableIdentityV1 `json:"reviewer"`
	State      ReviewState      `json:"state"`
	CommitSHA  GitSHA           `json:"-"`
}

type PullRequestSnapshotInput struct {
	Snapshot     SnapshotIdentity
	Repository   Repository
	PullRequest  PullRequestIdentity
	BaseBranch   Branch
	BaseTipSHA   GitSHA
	HeadBranch   Branch
	HeadSHA      GitSHA
	State        PullRequestState
	MergeMethod  MergeMethod
	Reviews      []Review
	EvidenceRefs []ledger.EvidenceRef
	Metadata     map[string]string
	LimitsSHA256 string
}

// PullRequestSnapshot is a bounded canonical observation of one remote PR.
type PullRequestSnapshot struct {
	immutable immutableRecord[PullRequestSnapshotInput]
}

func NewPullRequestSnapshot(input PullRequestSnapshotInput, limits Limits) (PullRequestSnapshot, error) {
	input = clonePRInput(input)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return PullRequestSnapshot{}, err
	}
	input.LimitsSHA256 = limitsSHA
	if !input.Snapshot.valid() || !input.Repository.valid() || !input.PullRequest.valid() ||
		!input.BaseBranch.valid() || !input.BaseTipSHA.valid() || !input.HeadBranch.valid() || !input.HeadSHA.valid() {
		return PullRequestSnapshot{}, errors.New("pull request snapshot contains an invalid identity")
	}
	if input.State != PullRequestOpen && input.State != PullRequestClosed && input.State != PullRequestMerged {
		return PullRequestSnapshot{}, errors.New("pull request state is unsupported")
	}
	if input.MergeMethod != "" && !input.MergeMethod.Valid() {
		return PullRequestSnapshot{}, errors.New("pull request merge method is unsupported")
	}
	if len(input.Reviews) > limits.MaxTotalItems {
		return PullRequestSnapshot{}, errors.New("pull request reviews exceed item limit")
	}
	seen := make(map[string]struct{}, len(input.Reviews))
	for index, review := range input.Reviews {
		if !validOpaqueID(review.NodeID, limits.MaxTextBytes) || review.DatabaseID <= 0 || !review.Reviewer.valid() || !review.CommitSHA.valid() ||
			(review.State != ReviewApproved && review.State != ReviewChangesRequested && review.State != ReviewCommented && review.State != ReviewDismissed) {
			return PullRequestSnapshot{}, fmt.Errorf("review %d is invalid", index)
		}
		if _, exists := seen[review.NodeID]; exists {
			return PullRequestSnapshot{}, fmt.Errorf("review %d duplicates node identity", index)
		}
		seen[review.NodeID] = struct{}{}
	}
	sort.Slice(input.Reviews, func(i, j int) bool { return reviewKey(input.Reviews[i]) < reviewKey(input.Reviews[j]) })
	if err := canonicalizeCommon(&input.EvidenceRefs, input.Metadata, limits); err != nil {
		return PullRequestSnapshot{}, err
	}
	canonical, digest, err := canonicalJSON(prWire(input))
	if err != nil {
		return PullRequestSnapshot{}, err
	}
	return PullRequestSnapshot{immutableRecord[PullRequestSnapshotInput]{data: input, canonical: canonical, digest: digest}}, nil
}

func (s PullRequestSnapshot) Input() PullRequestSnapshotInput { return clonePRInput(s.immutable.data) }
func (s PullRequestSnapshot) CanonicalJSON() []byte {
	return append([]byte(nil), s.immutable.canonical...)
}
func (s PullRequestSnapshot) SHA256() string               { return s.immutable.digest }
func (s PullRequestSnapshot) MarshalJSON() ([]byte, error) { return s.immutable.marshal() }
func (s PullRequestSnapshot) valid() bool {
	return len(s.immutable.canonical) > 0 && s.immutable.digest != ""
}

type CheckStatus string

const (
	CheckQueued     CheckStatus = "queued"
	CheckInProgress CheckStatus = "in_progress"
	CheckCompleted  CheckStatus = "completed"
)

type CheckConclusion string

const (
	ConclusionSuccess   CheckConclusion = "success"
	ConclusionFailure   CheckConclusion = "failure"
	ConclusionCancelled CheckConclusion = "cancelled"
	ConclusionNeutral   CheckConclusion = "neutral"
	ConclusionSkipped   CheckConclusion = "skipped"
	ConclusionTimedOut  CheckConclusion = "timed_out"
)

type Check struct {
	NodeID       string                 `json:"node_id"`
	Name         string                 `json:"name"`
	Identity     TrustedCheckIdentityV1 `json:"identity"`
	Status       CheckStatus            `json:"status"`
	Conclusion   CheckConclusion        `json:"conclusion,omitempty"`
	HeadSHA      GitSHA                 `json:"-"`
	EvidenceRefs []ledger.EvidenceRef   `json:"evidence_refs,omitempty"`
}

type CISnapshotInput struct {
	Snapshot     SnapshotIdentity
	Repository   Repository
	HeadSHA      GitSHA
	Checks       []Check
	EvidenceRefs []ledger.EvidenceRef
	Metadata     map[string]string
	LimitsSHA256 string
}

// CISnapshot is bounded check evidence tied to exactly one candidate SHA.
type CISnapshot struct {
	immutable immutableRecord[CISnapshotInput]
}

func NewCISnapshot(input CISnapshotInput, limits Limits) (CISnapshot, error) {
	input = cloneCIInput(input)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return CISnapshot{}, err
	}
	input.LimitsSHA256 = limitsSHA
	if !input.Snapshot.valid() || !input.Repository.valid() || !input.HeadSHA.valid() {
		return CISnapshot{}, errors.New("CI snapshot contains an invalid identity")
	}
	if len(input.Checks) > limits.MaxTotalItems {
		return CISnapshot{}, errors.New("CI checks exceed item limit")
	}
	seen := make(map[string]struct{}, len(input.Checks))
	for index := range input.Checks {
		check := &input.Checks[index]
		if !validOpaqueID(check.NodeID, limits.MaxTextBytes) || !validText(check.Name, limits.MaxTextBytes, false) || !check.Identity.valid(limits) || check.Name != check.Identity.Context || !check.HeadSHA.valid() || check.HeadSHA != input.HeadSHA ||
			(check.Status != CheckQueued && check.Status != CheckInProgress && check.Status != CheckCompleted) {
			return CISnapshot{}, fmt.Errorf("check %d is invalid or tied to a different head SHA", index)
		}
		if check.Status == CheckCompleted {
			if check.Conclusion != ConclusionSuccess && check.Conclusion != ConclusionFailure && check.Conclusion != ConclusionCancelled && check.Conclusion != ConclusionNeutral && check.Conclusion != ConclusionSkipped && check.Conclusion != ConclusionTimedOut {
				return CISnapshot{}, fmt.Errorf("check %d has invalid conclusion", index)
			}
		} else if check.Conclusion != "" {
			return CISnapshot{}, fmt.Errorf("check %d has a conclusion before completion", index)
		}
		if _, exists := seen[check.NodeID]; exists {
			return CISnapshot{}, fmt.Errorf("check %d duplicates node identity", index)
		}
		seen[check.NodeID] = struct{}{}
		if err := canonicalizeEvidence(&check.EvidenceRefs, limits); err != nil {
			return CISnapshot{}, fmt.Errorf("check %d: %w", index, err)
		}
	}
	sort.Slice(input.Checks, func(i, j int) bool { return checkKey(input.Checks[i]) < checkKey(input.Checks[j]) })
	if err := canonicalizeCommon(&input.EvidenceRefs, input.Metadata, limits); err != nil {
		return CISnapshot{}, err
	}
	canonical, digest, err := canonicalJSON(ciWire(input))
	if err != nil {
		return CISnapshot{}, err
	}
	return CISnapshot{immutableRecord[CISnapshotInput]{data: input, canonical: canonical, digest: digest}}, nil
}

func (s CISnapshot) Input() CISnapshotInput       { return cloneCIInput(s.immutable.data) }
func (s CISnapshot) CanonicalJSON() []byte        { return append([]byte(nil), s.immutable.canonical...) }
func (s CISnapshot) SHA256() string               { return s.immutable.digest }
func (s CISnapshot) MarshalJSON() ([]byte, error) { return s.immutable.marshal() }
func (s CISnapshot) valid() bool                  { return len(s.immutable.canonical) > 0 && s.immutable.digest != "" }

// PullRequestPage is one bounded provider page. It rejects repeated PR
// identities instead of silently selecting among ambiguous remote records.
type PullRequestPage struct {
	page      int
	total     int
	items     []PullRequestSnapshot
	canonical []byte
	digest    string
	limitsSHA string
}

func NewPullRequestPage(page, total int, items []PullRequestSnapshot, limits Limits) (PullRequestPage, error) {
	if err := validatePage(limits, page, len(items), total); err != nil {
		return PullRequestPage{}, err
	}
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return PullRequestPage{}, err
	}
	copyItems := append([]PullRequestSnapshot(nil), items...)
	seen := make(map[string]struct{}, len(copyItems))
	for index, item := range copyItems {
		if !item.valid() {
			return PullRequestPage{}, fmt.Errorf("pull request item %d is incomplete", index)
		}
		if err := requireLimitsSHA(limits, item.immutable.data.LimitsSHA256); err != nil {
			return PullRequestPage{}, fmt.Errorf("pull request item %d: %w", index, err)
		}
		input := item.immutable.data
		key := fmt.Sprintf("%s\x00%d\x00%s", input.Repository.String(), input.PullRequest.Number(), input.PullRequest.NodeID())
		if _, exists := seen[key]; exists {
			return PullRequestPage{}, errors.New("duplicate or ambiguous pull request identity")
		}
		seen[key] = struct{}{}
	}
	sort.Slice(copyItems, func(i, j int) bool { return copyItems[i].SHA256() < copyItems[j].SHA256() })
	wires := make([]json.RawMessage, len(copyItems))
	for index, item := range copyItems {
		wires[index] = item.CanonicalJSON()
	}
	canonical, digest, err := canonicalJSON(struct {
		Page         int               `json:"page"`
		Total        int               `json:"total"`
		Items        []json.RawMessage `json:"items"`
		LimitsSHA256 string            `json:"limits_sha256"`
	}{page, total, wires, limitsSHA})
	if err != nil {
		return PullRequestPage{}, err
	}
	return PullRequestPage{page: page, total: total, items: copyItems, canonical: canonical, digest: digest, limitsSHA: limitsSHA}, nil
}

func (p PullRequestPage) Items() []PullRequestSnapshot {
	return append([]PullRequestSnapshot(nil), p.items...)
}
func (p PullRequestPage) CanonicalJSON() []byte { return append([]byte(nil), p.canonical...) }
func (p PullRequestPage) SHA256() string        { return p.digest }
func (p PullRequestPage) LimitsSHA256() string  { return p.limitsSHA }
func (p PullRequestPage) MarshalJSON() ([]byte, error) {
	if len(p.canonical) == 0 {
		return nil, errors.New("pull request page is incomplete")
	}
	return append([]byte(nil), p.canonical...), nil
}

type immutableRecord[T any] struct {
	data      T
	canonical []byte
	digest    string
}

func (r immutableRecord[T]) marshal() ([]byte, error) {
	if len(r.canonical) == 0 {
		return nil, errors.New("snapshot is incomplete")
	}
	return append([]byte(nil), r.canonical...), nil
}

func canonicalJSON(value any) ([]byte, string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, "", fmt.Errorf("marshal canonical JSON: %w", err)
	}
	digest := sha256.Sum256(data)
	return data, hex.EncodeToString(digest[:]), nil
}

func canonicalizeCommon(evidence *[]ledger.EvidenceRef, metadata map[string]string, limits Limits) error {
	if err := canonicalizeEvidence(evidence, limits); err != nil {
		return err
	}
	if len(metadata) > limits.MaxMetadataItems {
		return errors.New("metadata exceeds item limit")
	}
	for key, value := range metadata {
		if !validOpaqueID(key, limits.MaxTextBytes) || !validText(value, limits.MaxTextBytes, true) {
			return errors.New("metadata contains an invalid bounded key or value")
		}
	}
	return nil
}

func canonicalizeEvidence(refs *[]ledger.EvidenceRef, limits Limits) error {
	if len(*refs) > limits.MaxEvidenceRefs {
		return errors.New("evidence references exceed limit")
	}
	seen := make(map[string]struct{}, len(*refs))
	for index, ref := range *refs {
		if !validText(ref.URI, limits.MaxTextBytes, false) || !validOpaqueID(ref.Kind, limits.MaxTextBytes) || len(ref.SHA256) != sha256.Size*2 || !isLowerHex(ref.SHA256) {
			return fmt.Errorf("evidence reference %d is invalid", index)
		}
		key := evidenceKey(ref)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("evidence reference %d is duplicated", index)
		}
		seen[key] = struct{}{}
	}
	sort.Slice(*refs, func(i, j int) bool { return evidenceKey((*refs)[i]) < evidenceKey((*refs)[j]) })
	return nil
}

func evidenceKey(ref ledger.EvidenceRef) string {
	return strings.Join([]string{ref.URI, ref.SHA256, ref.Kind}, "\x00")
}
func reviewKey(review Review) string {
	return strings.Join([]string{stableIdentityKey(review.Reviewer), review.NodeID, string(review.State), review.CommitSHA.String()}, "\x00")
}
func checkKey(check Check) string {
	return strings.Join([]string{checkIdentityKey(check.Identity), check.NodeID, check.HeadSHA.String()}, "\x00")
}

func clonePRInput(input PullRequestSnapshotInput) PullRequestSnapshotInput {
	input.Reviews = append([]Review(nil), input.Reviews...)
	input.EvidenceRefs = append([]ledger.EvidenceRef(nil), input.EvidenceRefs...)
	input.Metadata = cloneMap(input.Metadata)
	return input
}

func cloneCIInput(input CISnapshotInput) CISnapshotInput {
	input.Checks = append([]Check(nil), input.Checks...)
	for index := range input.Checks {
		input.Checks[index].EvidenceRefs = append([]ledger.EvidenceRef(nil), input.Checks[index].EvidenceRefs...)
	}
	input.EvidenceRefs = append([]ledger.EvidenceRef(nil), input.EvidenceRefs...)
	input.Metadata = cloneMap(input.Metadata)
	return input
}

func cloneMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	copy := make(map[string]string, len(input))
	for key, value := range input {
		copy[key] = value
	}
	return copy
}

type identityWire struct {
	Provider         string `json:"provider"`
	RequestID        string `json:"request_id"`
	ObservedUnixNano int64  `json:"observed_unix_nano"`
}
type repoWire struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}
type prIdentityWire struct {
	Number int64  `json:"number"`
	NodeID string `json:"node_id"`
}
type reviewWire struct {
	NodeID     string           `json:"node_id"`
	DatabaseID int64            `json:"database_id"`
	Reviewer   StableIdentityV1 `json:"reviewer"`
	State      ReviewState      `json:"state"`
	CommitSHA  string           `json:"commit_sha"`
}
type checkWire struct {
	NodeID       string                 `json:"node_id"`
	Name         string                 `json:"name"`
	Identity     TrustedCheckIdentityV1 `json:"identity"`
	Status       CheckStatus            `json:"status"`
	Conclusion   CheckConclusion        `json:"conclusion,omitempty"`
	HeadSHA      string                 `json:"head_sha"`
	EvidenceRefs []ledger.EvidenceRef   `json:"evidence_refs,omitempty"`
}

func snapshotWire(s SnapshotIdentity) identityWire {
	return identityWire{s.Provider(), s.RequestID(), s.ObservedUnixNano()}
}
func repositoryWire(r Repository) repoWire { return repoWire{r.Owner(), r.Name()} }
func pullRequestWire(p PullRequestIdentity) prIdentityWire {
	return prIdentityWire{p.Number(), p.NodeID()}
}
func prWire(input PullRequestSnapshotInput) any {
	reviews := make([]reviewWire, len(input.Reviews))
	for i, r := range input.Reviews {
		reviews[i] = reviewWire{r.NodeID, r.DatabaseID, r.Reviewer, r.State, r.CommitSHA.String()}
	}
	return struct {
		Snapshot     identityWire         `json:"snapshot"`
		Repository   repoWire             `json:"repository"`
		PullRequest  prIdentityWire       `json:"pull_request"`
		BaseBranch   string               `json:"base_branch"`
		BaseTipSHA   string               `json:"base_tip_sha"`
		HeadBranch   string               `json:"head_branch"`
		HeadSHA      string               `json:"head_sha"`
		State        PullRequestState     `json:"state"`
		MergeMethod  MergeMethod          `json:"merge_method,omitempty"`
		Reviews      []reviewWire         `json:"reviews"`
		EvidenceRefs []ledger.EvidenceRef `json:"evidence_refs,omitempty"`
		Metadata     map[string]string    `json:"metadata,omitempty"`
		LimitsSHA256 string               `json:"limits_sha256"`
	}{snapshotWire(input.Snapshot), repositoryWire(input.Repository), pullRequestWire(input.PullRequest), input.BaseBranch.String(), input.BaseTipSHA.String(), input.HeadBranch.String(), input.HeadSHA.String(), input.State, input.MergeMethod, reviews, input.EvidenceRefs, input.Metadata, input.LimitsSHA256}
}
func ciWire(input CISnapshotInput) any {
	checks := make([]checkWire, len(input.Checks))
	for i, c := range input.Checks {
		checks[i] = checkWire{c.NodeID, c.Name, c.Identity, c.Status, c.Conclusion, c.HeadSHA.String(), c.EvidenceRefs}
	}
	return struct {
		Snapshot     identityWire         `json:"snapshot"`
		Repository   repoWire             `json:"repository"`
		HeadSHA      string               `json:"head_sha"`
		Checks       []checkWire          `json:"checks"`
		EvidenceRefs []ledger.EvidenceRef `json:"evidence_refs,omitempty"`
		Metadata     map[string]string    `json:"metadata,omitempty"`
		LimitsSHA256 string               `json:"limits_sha256"`
	}{snapshotWire(input.Snapshot), repositoryWire(input.Repository), input.HeadSHA.String(), checks, input.EvidenceRefs, input.Metadata, input.LimitsSHA256}
}

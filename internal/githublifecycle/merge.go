package githublifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

// CommitLineage binds an original commit/tree to a provider-created
// commit/tree and its ordered Git parents. It supports squash/rebase proof
// without pretending that a synthesized result SHA equals the accepted head.
type CommitLineage struct {
	SourceSHA  GitSHA
	SourceTree GitSHA
	ResultSHA  GitSHA
	ResultTree GitSHA
	Parents    []GitSHA
}

type MergeResultInput struct {
	Snapshot         SnapshotIdentity
	Repository       Repository
	PullRequest      PullRequestIdentity
	Actor            ActingIdentity
	AcceptedHeadSHA  GitSHA
	AcceptedHeadTree GitSHA
	BaseBeforeSHA    GitSHA
	Method           MergeMethod
	ResultSHA        GitSHA
	ResultTree       GitSHA
	Parents          []GitSHA
	Lineage          []CommitLineage
	EvidenceRefs     []ledger.EvidenceRef
	Metadata         map[string]string
	Attempt          WriteAttempt
	ExpectedContent  ExpectedMergeContent
	LimitsSHA256     string
}

// MergeResult is immutable bounded evidence returned by a write operation.
type MergeResult struct {
	immutable immutableRecord[MergeResultInput]
}

func NewMergeResult(input MergeResultInput, limits Limits) (MergeResult, error) {
	input = cloneMergeInput(input)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return MergeResult{}, err
	}
	input.LimitsSHA256 = limitsSHA
	if err := validateMergeFields(input.Snapshot, input.Repository, input.PullRequest, input.Actor, input.AcceptedHeadSHA,
		input.AcceptedHeadTree, input.BaseBeforeSHA, input.Method, input.ResultSHA, input.ResultTree, input.Parents, input.Lineage,
		input.Attempt, input.ExpectedContent, limits); err != nil {
		return MergeResult{}, err
	}
	if err := canonicalizeCommon(&input.EvidenceRefs, input.Metadata, limits); err != nil {
		return MergeResult{}, err
	}
	canonical, digest, err := canonicalJSON(mergeWire(input))
	if err != nil {
		return MergeResult{}, err
	}
	return MergeResult{immutableRecord[MergeResultInput]{data: input, canonical: canonical, digest: digest}}, nil
}

func (r MergeResult) Input() MergeResultInput      { return cloneMergeInput(r.immutable.data) }
func (r MergeResult) CanonicalJSON() []byte        { return append([]byte(nil), r.immutable.canonical...) }
func (r MergeResult) SHA256() string               { return r.immutable.digest }
func (r MergeResult) MarshalJSON() ([]byte, error) { return r.immutable.marshal() }
func (r MergeResult) valid() bool                  { return len(r.immutable.canonical) > 0 && r.immutable.digest != "" }

type PostMergeObservationInput struct {
	Snapshot         SnapshotIdentity
	Repository       Repository
	BaseBranch       Branch
	PullRequest      PullRequestIdentity
	Actor            ActingIdentity
	AcceptedHeadSHA  GitSHA
	AcceptedHeadTree GitSHA
	BaseBeforeSHA    GitSHA
	Method           MergeMethod
	ResultSHA        GitSHA
	BaseAfterSHA     GitSHA
	ResultTree       GitSHA
	Parents          []GitSHA
	Lineage          []CommitLineage
	EvidenceRefs     []ledger.EvidenceRef
	Metadata         map[string]string
	Attempt          WriteAttempt
	ExpectedContent  ExpectedMergeContent
	LimitsSHA256     string
}

// PostMergeObservation is an immutable target-branch observation. ResultSHA
// and BaseAfterSHA are distinct fields and must be proved equal explicitly.
type PostMergeObservation struct {
	immutable immutableRecord[PostMergeObservationInput]
}

func NewPostMergeObservation(input PostMergeObservationInput, limits Limits) (PostMergeObservation, error) {
	input = clonePostMergeInput(input)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return PostMergeObservation{}, err
	}
	input.LimitsSHA256 = limitsSHA
	if !input.BaseBranch.valid() || !input.BaseAfterSHA.valid() {
		return PostMergeObservation{}, errors.New("post-merge base identity is invalid")
	}
	if err := validateMergeFields(input.Snapshot, input.Repository, input.PullRequest, input.Actor, input.AcceptedHeadSHA,
		input.AcceptedHeadTree, input.BaseBeforeSHA, input.Method, input.ResultSHA, input.ResultTree, input.Parents, input.Lineage,
		input.Attempt, input.ExpectedContent, limits); err != nil {
		return PostMergeObservation{}, err
	}
	if err := canonicalizeCommon(&input.EvidenceRefs, input.Metadata, limits); err != nil {
		return PostMergeObservation{}, err
	}
	canonical, digest, err := canonicalJSON(postMergeWire(input))
	if err != nil {
		return PostMergeObservation{}, err
	}
	return PostMergeObservation{immutableRecord[PostMergeObservationInput]{data: input, canonical: canonical, digest: digest}}, nil
}

func (o PostMergeObservation) Input() PostMergeObservationInput {
	return clonePostMergeInput(o.immutable.data)
}
func (o PostMergeObservation) CanonicalJSON() []byte {
	return append([]byte(nil), o.immutable.canonical...)
}
func (o PostMergeObservation) SHA256() string               { return o.immutable.digest }
func (o PostMergeObservation) MarshalJSON() ([]byte, error) { return o.immutable.marshal() }
func (o PostMergeObservation) valid() bool {
	return len(o.immutable.canonical) > 0 && o.immutable.digest != ""
}

func validateMergeFields(snapshot SnapshotIdentity, repository Repository, pr PullRequestIdentity, actor ActingIdentity,
	acceptedHead, acceptedTree, baseBefore GitSHA, method MergeMethod, result, resultTree GitSHA,
	parents []GitSHA, lineage []CommitLineage, attempt WriteAttempt, expected ExpectedMergeContent, limits Limits) error {
	if err := limits.Validate(); err != nil {
		return err
	}
	if !snapshot.valid() || !repository.valid() || !pr.valid() || !actor.valid() || !acceptedHead.valid() || !acceptedTree.valid() ||
		!baseBefore.valid() || !method.Valid() || !result.valid() || !resultTree.valid() || !attempt.valid(limits) || !expected.valid() {
		return errors.New("merge result contains an invalid identity")
	}
	if attempt.operation != OperationMerge || attempt.repository != repository || attempt.actor != actor ||
		expected.SourceIntegratedHeadSHA() != acceptedHead || acceptedTree != expected.ExpectedResultTreeSHA() || resultTree != expected.ExpectedResultTreeSHA() {
		return errors.New("merge result does not bind the merge attempt or controller-expected tree")
	}
	if len(parents) > limits.MaxParents || len(lineage) > limits.MaxLineageEntries {
		return errors.New("merge lineage exceeds governed limits")
	}
	if err := validateSHAs(parents, "result parents"); err != nil {
		return err
	}
	seenResults := make(map[GitSHA]struct{}, len(lineage))
	for index, entry := range lineage {
		if !entry.SourceSHA.valid() || !entry.SourceTree.valid() || !entry.ResultSHA.valid() || !entry.ResultTree.valid() || len(entry.Parents) > limits.MaxParents {
			return fmt.Errorf("lineage entry %d is invalid", index)
		}
		if err := validateSHAs(entry.Parents, fmt.Sprintf("lineage entry %d parents", index)); err != nil {
			return err
		}
		if _, exists := seenResults[entry.ResultSHA]; exists {
			return fmt.Errorf("lineage entry %d duplicates result SHA", index)
		}
		seenResults[entry.ResultSHA] = struct{}{}
	}
	return nil
}

func validateSHAs(values []GitSHA, label string) error {
	seen := make(map[GitSHA]struct{}, len(values))
	for index, value := range values {
		if !value.valid() {
			return fmt.Errorf("%s %d is invalid", label, index)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s %d is duplicated", label, index)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateMergeInput(input MergeInput, limits Limits) error {
	if err := requireAuthority(input.authority); err != nil {
		return err
	}
	if err := requireLimitsSHA(limits, input.limitsSHA256); err != nil {
		return err
	}
	if _, ok := input.authority.PullRequest(); !ok {
		return errors.New("merge input requires an exact pull request identity")
	}
	if !input.expectedContent.valid() || input.expectedContent.SHA256() != input.authority.ExpectedContent().SHA256() {
		return errors.New("merge input expected content does not match authority")
	}
	if !input.attempt.valid(limits) || !input.attempt.matchesAuthority(input.authority) || input.attempt.operation != OperationMerge {
		return errors.New("merge write attempt is invalid")
	}
	evidence := append([]ledger.EvidenceRef(nil), input.approvalEvidence...)
	if len(evidence) == 0 || canonicalizeEvidence(&evidence, limits) != nil {
		return errors.New("merge approval evidence fails bounded revalidation")
	}
	payload, payloadSHA, err := canonicalJSON(struct {
		Authority       Authority            `json:"authority"`
		ExpectedContent ExpectedMergeContent `json:"expected_merge_content"`
		Evidence        []ledger.EvidenceRef `json:"approval_evidence"`
		LimitsSHA256    string               `json:"limits_sha256"`
	}{input.authority, input.expectedContent, evidence, input.limitsSHA256})
	if err != nil || payloadSHA != input.attempt.payloadSHA256 || !bytes.Equal(payload, input.canonicalPayload) {
		return errors.New("merge canonical payload does not match write attempt")
	}
	return nil
}

// ValidatePullRequest proves that one remote PR still matches exact authority.
func ValidatePullRequest(authority Authority, snapshot PullRequestSnapshot, limits Limits) error {
	if err := requireAuthority(authority); err != nil {
		return err
	}
	if !snapshot.valid() {
		return errors.New("pull request snapshot is incomplete")
	}
	data := snapshot.immutable.data
	if err := requireLimitsSHA(limits, data.LimitsSHA256); err != nil {
		return err
	}
	rebuilt, err := NewPullRequestSnapshot(data, limits)
	if err != nil || rebuilt.SHA256() != snapshot.SHA256() || !bytes.Equal(rebuilt.CanonicalJSON(), snapshot.CanonicalJSON()) {
		return errors.New("pull request snapshot fails independent bounded revalidation")
	}
	if data.Repository != authority.data.Repository || data.BaseBranch != authority.data.BaseBranch || data.HeadBranch != authority.data.HeadBranch {
		return errors.New("pull request repository or branch identity does not match authority")
	}
	if data.HeadSHA != authority.data.HeadSHA {
		return errors.New("pull request head moved or does not match authority")
	}
	if data.BaseTipSHA != authority.data.ExpectedBaseTipSHA {
		return errors.New("pre-merge base tip moved or does not match authority")
	}
	if expected, ok := authority.PullRequest(); ok && data.PullRequest != expected {
		return errors.New("pull request identity does not match authority")
	}
	if data.MergeMethod != "" && data.MergeMethod != authority.data.AllowedMergeMethod {
		return errors.New("remote merge method changed or is not allowed")
	}
	return nil
}

// SelectPullRequest returns exactly one authority-matching PR and rejects
// absence or duplicate/ambiguous identity.
func SelectPullRequest(authority Authority, snapshots []PullRequestSnapshot, limits Limits) (PullRequestSnapshot, error) {
	if err := requireAuthority(authority); err != nil {
		return PullRequestSnapshot{}, err
	}
	if err := limits.Validate(); err != nil {
		return PullRequestSnapshot{}, err
	}
	if len(snapshots) > limits.MaxTotalItems {
		return PullRequestSnapshot{}, errors.New("pull request candidates exceed item limit")
	}
	var matched []PullRequestSnapshot
	for index, snapshot := range snapshots {
		if !snapshot.valid() {
			return PullRequestSnapshot{}, errors.New("pull request candidate is incomplete")
		}
		data := snapshot.immutable.data
		if err := requireLimitsSHA(limits, data.LimitsSHA256); err != nil {
			return PullRequestSnapshot{}, fmt.Errorf("pull request candidate %d: %w", index, err)
		}
		rebuilt, err := NewPullRequestSnapshot(data, limits)
		if err != nil || rebuilt.SHA256() != snapshot.SHA256() || !bytes.Equal(rebuilt.CanonicalJSON(), snapshot.CanonicalJSON()) {
			return PullRequestSnapshot{}, fmt.Errorf("pull request candidate %d fails independent bounded revalidation", index)
		}
		if data.Repository == authority.data.Repository && data.BaseBranch == authority.data.BaseBranch && data.HeadBranch == authority.data.HeadBranch {
			matched = append(matched, snapshot)
		}
	}
	if len(matched) != 1 {
		return PullRequestSnapshot{}, fmt.Errorf("expected exactly one pull request identity, observed %d", len(matched))
	}
	if err := ValidatePullRequest(authority, matched[0], limits); err != nil {
		return PullRequestSnapshot{}, err
	}
	return matched[0], nil
}

// ValidateCI proves that every check and the snapshot itself are tied to the
// authority's exact accepted head SHA.
func ValidateCI(authority Authority, snapshot CISnapshot, limits Limits) error {
	if err := requireAuthority(authority); err != nil {
		return err
	}
	if !snapshot.valid() {
		return errors.New("CI snapshot is incomplete")
	}
	data := snapshot.immutable.data
	if err := requireLimitsSHA(limits, data.LimitsSHA256); err != nil {
		return err
	}
	rebuilt, err := NewCISnapshot(data, limits)
	if err != nil || rebuilt.SHA256() != snapshot.SHA256() || !bytes.Equal(rebuilt.CanonicalJSON(), snapshot.CanonicalJSON()) {
		return errors.New("CI snapshot fails independent bounded revalidation")
	}
	if data.Repository != authority.data.Repository {
		return errors.New("CI repository does not match authority")
	}
	if data.HeadSHA != authority.data.HeadSHA {
		return errors.New("CI snapshot is stale for a different head SHA")
	}
	for _, check := range data.Checks {
		if check.HeadSHA != authority.data.HeadSHA {
			return errors.New("CI check is stale for a different head SHA")
		}
	}
	return nil
}

// ValidateMergeResult binds the provider write result back to every merge
// authority field, including the authenticated actor and pre-merge base tip.
func ValidateMergeResult(input MergeInput, result MergeResult, limits Limits) error {
	if err := validateMergeInput(input, limits); err != nil {
		return err
	}
	if !result.valid() {
		return errors.New("merge result is incomplete")
	}
	data := result.immutable.data
	if err := requireLimitsSHA(limits, data.LimitsSHA256); err != nil {
		return err
	}
	rebuilt, err := NewMergeResult(data, limits)
	if err != nil || rebuilt.SHA256() != result.SHA256() || !bytes.Equal(rebuilt.CanonicalJSON(), result.CanonicalJSON()) {
		return errors.New("merge result fails independent bounded revalidation")
	}
	authority := input.authority
	pr, ok := authority.PullRequest()
	if !ok {
		return errors.New("merge authority requires an exact pull request identity")
	}
	if data.Repository != authority.data.Repository || data.PullRequest != pr || data.Actor != authority.data.Actor || data.AcceptedHeadSHA != authority.data.HeadSHA ||
		data.BaseBeforeSHA != authority.data.ExpectedBaseTipSHA || data.Method != authority.data.AllowedMergeMethod {
		return errors.New("merge result identity, actor, head, base, or method does not match authority")
	}
	if data.Attempt != input.attempt || data.Attempt.operation != OperationMerge {
		return errors.New("merge result replaced or mismatched the write attempt")
	}
	if data.ExpectedContent.SHA256() != input.expectedContent.SHA256() || data.ResultTree != input.expectedContent.ExpectedResultTreeSHA() ||
		data.AcceptedHeadTree != input.expectedContent.ExpectedResultTreeSHA() {
		return errors.New("merge result does not match controller-owned expected merge content")
	}
	return verifyStrategy(data.Method, data.AcceptedHeadSHA, data.AcceptedHeadTree, data.BaseBeforeSHA, data.ResultSHA, data.ResultTree, data.Parents, data.Lineage)
}

// VerifyPostMerge proves agreement among authority, write result, observed base
// tip, content tree, ordered parents, and method-specific lineage.
func VerifyPostMerge(input MergeInput, result MergeResult, observation PostMergeObservation, limits Limits) error {
	if err := ValidateMergeResult(input, result, limits); err != nil {
		return err
	}
	if !observation.valid() {
		return errors.New("post-merge observation is incomplete")
	}
	m, o := result.immutable.data, observation.immutable.data
	if err := requireLimitsSHA(limits, o.LimitsSHA256); err != nil {
		return err
	}
	rebuilt, err := NewPostMergeObservation(o, limits)
	if err != nil || rebuilt.SHA256() != observation.SHA256() || !bytes.Equal(rebuilt.CanonicalJSON(), observation.CanonicalJSON()) {
		return errors.New("post-merge observation fails independent bounded revalidation")
	}
	authority := input.authority
	if o.Repository != authority.data.Repository || o.BaseBranch != authority.data.BaseBranch || o.PullRequest != m.PullRequest || o.Actor != authority.data.Actor {
		return errors.New("post-merge repository, branch, PR, or acting identity does not match authority")
	}
	if o.AcceptedHeadSHA != m.AcceptedHeadSHA || o.AcceptedHeadTree != m.AcceptedHeadTree || o.BaseBeforeSHA != m.BaseBeforeSHA || o.Method != m.Method {
		return errors.New("post-merge accepted head, tree, base-before, or method changed")
	}
	if o.ResultSHA != m.ResultSHA || o.BaseAfterSHA != m.ResultSHA || o.ResultTree != m.ResultTree {
		return errors.New("post-merge result SHA, base-after SHA, or result tree does not match merge result")
	}
	if o.Attempt != input.attempt || o.ExpectedContent.SHA256() != input.expectedContent.SHA256() ||
		o.ResultTree != input.expectedContent.ExpectedResultTreeSHA() {
		return errors.New("post-merge observation does not bind the write attempt and controller-expected tree")
	}
	if !equalSHAs(o.Parents, m.Parents) || !equalLineage(o.Lineage, m.Lineage) {
		return errors.New("post-merge parent or lineage proof does not match merge result")
	}
	return verifyStrategy(o.Method, o.AcceptedHeadSHA, o.AcceptedHeadTree, o.BaseBeforeSHA, o.ResultSHA, o.ResultTree, o.Parents, o.Lineage)
}

func verifyStrategy(method MergeMethod, acceptedHead, acceptedTree, baseBefore, result, resultTree GitSHA, parents []GitSHA, lineage []CommitLineage) error {
	switch method {
	case MergeMethodMerge:
		if len(parents) != 2 || parents[0] != baseBefore || parents[1] != acceptedHead || len(lineage) != 0 {
			return errors.New("merge-commit proof requires ordered base/head parents and no rewritten lineage")
		}
	case MergeMethodSquash:
		if len(parents) != 1 || parents[0] != baseBefore || len(lineage) != 1 {
			return errors.New("squash proof requires the base parent and one content-lineage entry")
		}
		entry := lineage[0]
		if entry.SourceSHA != acceptedHead || entry.SourceTree != acceptedTree || entry.ResultSHA != result || entry.ResultTree != resultTree || !equalSHAs(entry.Parents, parents) {
			return errors.New("squash content-lineage proof does not bind accepted and result trees")
		}
	case MergeMethodRebase:
		if len(lineage) == 0 || len(parents) != 1 {
			return errors.New("rebase proof requires an ordered rewritten lineage")
		}
		previous := baseBefore
		for index, entry := range lineage {
			if len(entry.Parents) != 1 || entry.Parents[0] != previous {
				return fmt.Errorf("rebase lineage entry %d does not extend the proved chain", index)
			}
			previous = entry.ResultSHA
		}
		last := lineage[len(lineage)-1]
		if last.SourceSHA != acceptedHead || last.SourceTree != acceptedTree || last.ResultSHA != result || last.ResultTree != resultTree || parents[0] != last.Parents[0] {
			// Result parents describe the result commit, so they equal the final
			// lineage entry parents rather than the beginning of the chain.
			return errors.New("rebase lineage does not bind the accepted head/tree to the result")
		}
	default:
		return errors.New("unsupported merge method")
	}
	return nil
}

func cloneLineage(input []CommitLineage) []CommitLineage {
	result := append([]CommitLineage(nil), input...)
	for index := range result {
		result[index].Parents = append([]GitSHA(nil), result[index].Parents...)
	}
	return result
}
func cloneMergeInput(input MergeResultInput) MergeResultInput {
	input.Parents = append([]GitSHA(nil), input.Parents...)
	input.Lineage = cloneLineage(input.Lineage)
	input.EvidenceRefs = append([]ledger.EvidenceRef(nil), input.EvidenceRefs...)
	input.Metadata = cloneMap(input.Metadata)
	input.ExpectedContent = cloneExpectedContent(input.ExpectedContent)
	return input
}
func clonePostMergeInput(input PostMergeObservationInput) PostMergeObservationInput {
	input.Parents = append([]GitSHA(nil), input.Parents...)
	input.Lineage = cloneLineage(input.Lineage)
	input.EvidenceRefs = append([]ledger.EvidenceRef(nil), input.EvidenceRefs...)
	input.Metadata = cloneMap(input.Metadata)
	input.ExpectedContent = cloneExpectedContent(input.ExpectedContent)
	return input
}
func equalSHAs(a, b []GitSHA) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
func equalLineage(a, b []CommitLineage) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index].SourceSHA != b[index].SourceSHA || a[index].SourceTree != b[index].SourceTree || a[index].ResultSHA != b[index].ResultSHA || a[index].ResultTree != b[index].ResultTree || !equalSHAs(a[index].Parents, b[index].Parents) {
			return false
		}
	}
	return true
}

type actorWire struct {
	Kind           ActingKind `json:"kind"`
	Subject        string     `json:"subject"`
	InstallationID int64      `json:"installation_id,omitempty"`
}
type lineageWire struct {
	SourceSHA  string   `json:"source_sha"`
	SourceTree string   `json:"source_tree"`
	ResultSHA  string   `json:"result_sha"`
	ResultTree string   `json:"result_tree"`
	Parents    []string `json:"parents"`
}

func actingWire(a ActingIdentity) actorWire {
	return actorWire{a.Kind(), a.Subject(), a.InstallationID()}
}
func shaStrings(values []GitSHA) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = value.String()
	}
	return result
}
func lineageWires(values []CommitLineage) []lineageWire {
	result := make([]lineageWire, len(values))
	for i, value := range values {
		result[i] = lineageWire{value.SourceSHA.String(), value.SourceTree.String(), value.ResultSHA.String(), value.ResultTree.String(), shaStrings(value.Parents)}
	}
	return result
}
func mergeWire(input MergeResultInput) any {
	return struct {
		Snapshot         identityWire         `json:"snapshot"`
		Repository       repoWire             `json:"repository"`
		PullRequest      prIdentityWire       `json:"pull_request"`
		Actor            actorWire            `json:"actor"`
		AcceptedHeadSHA  string               `json:"accepted_head_sha"`
		AcceptedHeadTree string               `json:"accepted_head_tree"`
		BaseBeforeSHA    string               `json:"base_before_sha"`
		Method           MergeMethod          `json:"method"`
		ResultSHA        string               `json:"result_sha"`
		ResultTree       string               `json:"result_tree"`
		Parents          []string             `json:"parents"`
		Lineage          []lineageWire        `json:"lineage"`
		EvidenceRefs     []ledger.EvidenceRef `json:"evidence_refs,omitempty"`
		Metadata         map[string]string    `json:"metadata,omitempty"`
		Attempt          writeAttemptWire     `json:"write_attempt"`
		ExpectedContent  json.RawMessage      `json:"expected_merge_content"`
		LimitsSHA256     string               `json:"limits_sha256"`
	}{snapshotWire(input.Snapshot), repositoryWire(input.Repository), pullRequestWire(input.PullRequest), actingWire(input.Actor), input.AcceptedHeadSHA.String(), input.AcceptedHeadTree.String(), input.BaseBeforeSHA.String(), input.Method, input.ResultSHA.String(), input.ResultTree.String(), shaStrings(input.Parents), lineageWires(input.Lineage), input.EvidenceRefs, input.Metadata, attemptWire(input.Attempt), input.ExpectedContent.CanonicalJSON(), input.LimitsSHA256}
}
func postMergeWire(input PostMergeObservationInput) any {
	return struct {
		Snapshot         identityWire         `json:"snapshot"`
		Repository       repoWire             `json:"repository"`
		BaseBranch       string               `json:"base_branch"`
		PullRequest      prIdentityWire       `json:"pull_request"`
		Actor            actorWire            `json:"actor"`
		AcceptedHeadSHA  string               `json:"accepted_head_sha"`
		AcceptedHeadTree string               `json:"accepted_head_tree"`
		BaseBeforeSHA    string               `json:"base_before_sha"`
		Method           MergeMethod          `json:"method"`
		ResultSHA        string               `json:"result_sha"`
		BaseAfterSHA     string               `json:"base_after_sha"`
		ResultTree       string               `json:"result_tree"`
		Parents          []string             `json:"parents"`
		Lineage          []lineageWire        `json:"lineage"`
		EvidenceRefs     []ledger.EvidenceRef `json:"evidence_refs,omitempty"`
		Metadata         map[string]string    `json:"metadata,omitempty"`
		Attempt          writeAttemptWire     `json:"write_attempt"`
		ExpectedContent  json.RawMessage      `json:"expected_merge_content"`
		LimitsSHA256     string               `json:"limits_sha256"`
	}{snapshotWire(input.Snapshot), repositoryWire(input.Repository), input.BaseBranch.String(), pullRequestWire(input.PullRequest), actingWire(input.Actor), input.AcceptedHeadSHA.String(), input.AcceptedHeadTree.String(), input.BaseBeforeSHA.String(), input.Method, input.ResultSHA.String(), input.BaseAfterSHA.String(), input.ResultTree.String(), shaStrings(input.Parents), lineageWires(input.Lineage), input.EvidenceRefs, input.Metadata, attemptWire(input.Attempt), input.ExpectedContent.CanonicalJSON(), input.LimitsSHA256}
}

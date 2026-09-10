package githublifecycle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

type FindPullRequestsInput struct {
	Repository   Repository
	BaseBranch   Branch
	HeadBranch   Branch
	Page         int
	PerPage      int
	LimitsSHA256 string
}

func NewFindPullRequestsInput(repository Repository, base, head Branch, page, perPage int, limits Limits) (FindPullRequestsInput, error) {
	if !repository.valid() || !base.valid() || !head.valid() || base == head {
		return FindPullRequestsInput{}, errors.New("pull request query identity is invalid")
	}
	if err := validatePage(limits, page, perPage, perPage); err != nil {
		return FindPullRequestsInput{}, err
	}
	digest, _ := limits.SHA256()
	return FindPullRequestsInput{repository, base, head, page, perPage, digest}, nil
}

type GetCIInput struct {
	Repository   Repository
	HeadSHA      GitSHA
	Page         int
	PerPage      int
	LimitsSHA256 string
}

func NewGetCIInput(repository Repository, head GitSHA, page, perPage int, limits Limits) (GetCIInput, error) {
	if !repository.valid() || !head.valid() {
		return GetCIInput{}, errors.New("CI query identity is invalid")
	}
	if err := validatePage(limits, page, perPage, perPage); err != nil {
		return GetCIInput{}, err
	}
	digest, _ := limits.SHA256()
	return GetCIInput{repository, head, page, perPage, digest}, nil
}

type OperationKind string

const (
	OperationPullRequestUpsert OperationKind = "pr_upsert"
	OperationMerge             OperationKind = "merge"
)

func (k OperationKind) valid() bool { return k == OperationPullRequestUpsert || k == OperationMerge }

// WriteAttempt is the immutable identity of one exact mutation. PayloadSHA256
// covers the canonical provider input excluding the attempt itself.
type WriteAttempt struct {
	repository         Repository
	actor              ActingIdentity
	operation          OperationKind
	writeID            string
	authoritySHA256    string
	readyBindingSHA256 string
	policySHA256       string
	payloadSHA256      string
	limitsSHA256       string
}

func newWriteAttempt(operation OperationKind, authority Authority, writeID, payloadSHA256 string, limits Limits) (WriteAttempt, error) {
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return WriteAttempt{}, err
	}
	if !operation.valid() || requireAuthority(authority) != nil || !validOpaqueID(writeID, limits.MaxTextBytes) ||
		len(payloadSHA256) != sha256.Size*2 || !isLowerHex(payloadSHA256) {
		return WriteAttempt{}, errors.New("write attempt operation, authority, identity, and payload digest are required")
	}
	authoritySHA, err := authority.SHA256()
	if err != nil {
		return WriteAttempt{}, err
	}
	readySHA, policySHA := "", ""
	if operation == OperationMerge {
		readySHA, policySHA = authority.ReadyBinding().SHA256(), authority.MergePolicy().SHA256()
	}
	return WriteAttempt{authority.Repository(), authority.Actor(), operation, writeID, authoritySHA, readySHA, policySHA, payloadSHA256, limitsSHA}, nil
}

func (a WriteAttempt) Repository() Repository     { return a.repository }
func (a WriteAttempt) Actor() ActingIdentity      { return a.actor }
func (a WriteAttempt) Operation() OperationKind   { return a.operation }
func (a WriteAttempt) WriteID() string            { return a.writeID }
func (a WriteAttempt) AuthoritySHA256() string    { return a.authoritySHA256 }
func (a WriteAttempt) ReadyBindingSHA256() string { return a.readyBindingSHA256 }
func (a WriteAttempt) PolicySHA256() string       { return a.policySHA256 }
func (a WriteAttempt) PayloadSHA256() string      { return a.payloadSHA256 }
func (a WriteAttempt) LimitsSHA256() string       { return a.limitsSHA256 }
func (a WriteAttempt) CanonicalJSON() ([]byte, error) {
	if !a.structurallyValid() {
		return nil, errors.New("write attempt is incomplete")
	}
	data, _, err := canonicalJSON(attemptWire(a))
	return data, err
}
func (a WriteAttempt) MarshalJSON() ([]byte, error) { return a.CanonicalJSON() }
func (a WriteAttempt) structurallyValid() bool {
	return a.repository.valid() && a.actor.valid() && a.operation.valid() && validOpaqueID(a.writeID, 4096) &&
		len(a.authoritySHA256) == sha256.Size*2 && isLowerHex(a.authoritySHA256) &&
		((a.operation == OperationMerge && validSHA256(a.readyBindingSHA256) && validSHA256(a.policySHA256)) ||
			(a.operation == OperationPullRequestUpsert && a.readyBindingSHA256 == "" && a.policySHA256 == "")) &&
		len(a.payloadSHA256) == sha256.Size*2 && isLowerHex(a.payloadSHA256) &&
		len(a.limitsSHA256) == sha256.Size*2 && isLowerHex(a.limitsSHA256)
}
func (a WriteAttempt) valid(limits Limits) bool {
	return a.structurallyValid() && validOpaqueID(a.writeID, limits.MaxTextBytes) && requireLimitsSHA(limits, a.limitsSHA256) == nil
}
func (a WriteAttempt) matchesAuthority(authority Authority) bool {
	digest, err := authority.SHA256()
	if err != nil || a.repository != authority.Repository() || a.actor != authority.Actor() || a.authoritySHA256 != digest {
		return false
	}
	if a.operation == OperationMerge {
		return a.readyBindingSHA256 == authority.ReadyBinding().SHA256() && a.policySHA256 == authority.MergePolicy().SHA256()
	}
	return a.readyBindingSHA256 == "" && a.policySHA256 == ""
}

type writeAttemptWire struct {
	Repository         repoWire      `json:"repository"`
	Actor              actorWire     `json:"actor"`
	Operation          OperationKind `json:"operation_kind"`
	WriteID            string        `json:"write_id"`
	AuthoritySHA256    string        `json:"authority_sha256"`
	ReadyBindingSHA256 string        `json:"ready_binding_sha256,omitempty"`
	PolicySHA256       string        `json:"policy_sha256,omitempty"`
	PayloadSHA256      string        `json:"canonical_payload_sha256"`
	LimitsSHA256       string        `json:"limits_sha256"`
}

func attemptWire(a WriteAttempt) writeAttemptWire {
	return writeAttemptWire{repositoryWire(a.repository), actingWire(a.actor), a.operation, a.writeID, a.authoritySHA256, a.readyBindingSHA256, a.policySHA256, a.payloadSHA256, a.limitsSHA256}
}

type UpsertPullRequestInput struct {
	authority        Authority
	title            string
	body             string
	attempt          WriteAttempt
	canonicalPayload []byte
	limitsSHA256     string
}

func NewUpsertPullRequestInput(authority Authority, title, body, writeID string, limits Limits) (UpsertPullRequestInput, error) {
	if err := requireAuthority(authority); err != nil {
		return UpsertPullRequestInput{}, err
	}
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return UpsertPullRequestInput{}, err
	}
	if !validText(title, limits.MaxTextBytes, false) || !validText(body, limits.MaxTextBytes, true) {
		return UpsertPullRequestInput{}, errors.New("pull request title or body exceeds bounded safe text limits")
	}
	payload, payloadSHA, err := canonicalJSON(struct {
		Authority    Authority `json:"authority"`
		Title        string    `json:"title"`
		Body         string    `json:"body"`
		LimitsSHA256 string    `json:"limits_sha256"`
	}{authority, title, body, limitsSHA})
	if err != nil {
		return UpsertPullRequestInput{}, err
	}
	attempt, err := newWriteAttempt(OperationPullRequestUpsert, authority, writeID, payloadSHA, limits)
	if err != nil {
		return UpsertPullRequestInput{}, err
	}
	return UpsertPullRequestInput{authority, title, body, attempt, payload, limitsSHA}, nil
}

func (i UpsertPullRequestInput) Authority() Authority  { return i.authority }
func (i UpsertPullRequestInput) Title() string         { return i.title }
func (i UpsertPullRequestInput) Body() string          { return i.body }
func (i UpsertPullRequestInput) Attempt() WriteAttempt { return i.attempt }
func (i UpsertPullRequestInput) LimitsSHA256() string  { return i.limitsSHA256 }
func (i UpsertPullRequestInput) CanonicalPayload() []byte {
	return append([]byte(nil), i.canonicalPayload...)
}

type MergeInput struct {
	authority             Authority
	expectedContent       ExpectedMergeContent
	approvalEvidence      []ledger.EvidenceRef
	policyDecisionSHA256  string
	initialPullRequest    AuthoritativePullRequestSnapshotV1
	checks                []Check
	checkRunsClosure      PaginationClosureV1
	commitStatusesClosure PaginationClosureV1
	capability            ProviderCapabilityV1
	recipe                MergeCommitRecipeV1
	attempt               WriteAttempt
	canonicalPayload      []byte
	digest                string
	limitsSHA256          string
}

func NewMergeInput(input MergeAuthorizationInputV1, writeID string, limits Limits) (MergeInput, error) {
	input.Checks = cloneChecks(input.Checks)
	input.EvidenceRefs = append([]ledger.EvidenceRef(nil), input.EvidenceRefs...)
	authority := input.Authority
	if err := requireAuthority(authority); err != nil {
		return MergeInput{}, err
	}
	if !authority.ReadyBinding().valid() || !authority.MergePolicy().valid() {
		return MergeInput{}, errors.New("merge input requires complete READY, repository, and policy authority bindings")
	}
	if _, ok := authority.PullRequest(); !ok {
		return MergeInput{}, errors.New("merge input requires an exact pull request identity")
	}
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return MergeInput{}, err
	}
	if !validSHA256(input.PolicyDecisionSHA256) || !input.InitialPullRequest.valid() || !input.Capability.valid() || !input.Recipe.valid() {
		return MergeInput{}, errors.New("merge authorization snapshot, decision, capability, or recipe is invalid")
	}
	input.Checks, err = canonicalizeChecksForHead(input.Checks, authority.HeadSHA(), limits)
	if err != nil {
		return MergeInput{}, err
	}
	derivedRecipe, err := NewMergeCommitRecipeV1(writeID, authority, limits)
	if err != nil || input.Recipe.SHA256() != derivedRecipe.SHA256() || !bytes.Equal(input.Recipe.CanonicalJSON(), derivedRecipe.CanonicalJSON()) {
		return MergeInput{}, errors.New("merge recipe was not deterministically derived from authority, policy, and write identity")
	}
	if input.Recipe.input.ObjectFormat != "sha1" {
		return MergeInput{}, errors.New("production-v1 merge admission requires the proved SHA-1 object format")
	}
	if err := EvaluateMergePolicyV1(authority, input.InitialPullRequest, input.Checks, input.CheckRunsClosure, input.CommitStatusesClosure, limits); err != nil {
		return MergeInput{}, err
	}
	if err := addUniqueRequestIDs(map[string]struct{}{}, input.InitialPullRequest, input.CheckRunsClosure, input.CommitStatusesClosure); err != nil {
		return MergeInput{}, err
	}
	sort.Slice(input.Checks, func(i, j int) bool { return checkKey(input.Checks[i]) < checkKey(input.Checks[j]) })
	authoritySHA, _ := authority.SHA256()
	readySHA := authority.ReadyBinding().SHA256()
	policySHA := authority.MergePolicy().SHA256()
	recipeInput := input.Recipe.input
	if input.Capability.input.RepositoryNodeID != authority.ReadyBinding().input.RepositoryBinding.input.GitHubRepositoryNodeID ||
		input.Recipe.input.Repository != authority.Repository() || input.Recipe.input.TargetRef != "refs/heads/"+authority.BaseBranch().String() ||
		input.Recipe.input.ExpectedResultTree != authority.ExpectedContent().ExpectedResultTreeSHA() || !equalSHAs(input.Recipe.input.Parents, []GitSHA{authority.ExpectedBaseTipSHA(), authority.HeadSHA()}) ||
		recipeInput.WriteID != writeID || recipeInput.AuthoritySHA256 != authoritySHA || recipeInput.PolicySHA256 != policySHA || recipeInput.ReadyBindingSHA256 != readySHA {
		return MergeInput{}, errors.New("merge recipe or capability does not match authority and write identity")
	}
	copyEvidence := append([]ledger.EvidenceRef(nil), input.EvidenceRefs...)
	if len(copyEvidence) == 0 {
		return MergeInput{}, errors.New("merge approval evidence is required")
	}
	if err := canonicalizeEvidence(&copyEvidence, limits); err != nil {
		return MergeInput{}, err
	}
	expected := authority.ExpectedContent()
	input.EvidenceRefs = copyEvidence
	payload, payloadSHA, err := canonicalJSON(mergeAuthorizationPayloadWire(input, authority, limitsSHA))
	if err != nil {
		return MergeInput{}, err
	}
	attempt, err := newWriteAttempt(OperationMerge, authority, writeID, payloadSHA, limits)
	if err != nil {
		return MergeInput{}, err
	}
	return MergeInput{authority, expected, copyEvidence, input.PolicyDecisionSHA256, cloneAuthoritativePR(input.InitialPullRequest), cloneChecks(input.Checks), clonePaginationClosure(input.CheckRunsClosure), clonePaginationClosure(input.CommitStatusesClosure), cloneCapability(input.Capability), cloneRecipe(input.Recipe), attempt, payload, payloadSHA, limitsSHA}, nil
}

func (i MergeInput) Authority() Authority { return i.authority }
func (i MergeInput) ExpectedContent() ExpectedMergeContent {
	return cloneExpectedContent(i.expectedContent)
}
func (i MergeInput) Attempt() WriteAttempt { return i.attempt }
func (i MergeInput) SHA256() string        { return i.digest }
func (i MergeInput) InitialPullRequest() AuthoritativePullRequestSnapshotV1 {
	return cloneAuthoritativePR(i.initialPullRequest)
}
func (i MergeInput) Recipe() MergeCommitRecipeV1      { return cloneRecipe(i.recipe) }
func (i MergeInput) Capability() ProviderCapabilityV1 { return cloneCapability(i.capability) }
func (i MergeInput) LimitsSHA256() string             { return i.limitsSHA256 }
func (i MergeInput) CanonicalPayload() []byte         { return append([]byte(nil), i.canonicalPayload...) }
func (i MergeInput) Evidence() []ledger.EvidenceRef {
	return append([]ledger.EvidenceRef(nil), i.approvalEvidence...)
}

func cloneLifecycleMergeInput(input MergeInput) MergeInput {
	input.authority = Authority{data: cloneAuthorityInput(input.authority.data)}
	input.expectedContent = cloneExpectedContent(input.expectedContent)
	input.approvalEvidence = append([]ledger.EvidenceRef(nil), input.approvalEvidence...)
	input.initialPullRequest = cloneAuthoritativePR(input.initialPullRequest)
	input.checks = cloneChecks(input.checks)
	input.checkRunsClosure = clonePaginationClosure(input.checkRunsClosure)
	input.commitStatusesClosure = clonePaginationClosure(input.commitStatusesClosure)
	input.capability = cloneCapability(input.capability)
	input.recipe = cloneRecipe(input.recipe)
	input.canonicalPayload = append([]byte(nil), input.canonicalPayload...)
	return input
}

func cloneSealedAuthorization(input SealedMergeAuthorizationV1) SealedMergeAuthorizationV1 {
	input.input = cloneSealedInput(input.input)
	input.canonical = append([]byte(nil), input.canonical...)
	return input
}

type ObservePostMergeInput struct {
	sealed    SealedMergeAuthorizationV1
	merge     MergeResult
	limitsSHA string
}

func NewObservePostMergeInput(sealed SealedMergeAuthorizationV1, merge MergeResult, limits Limits) (ObservePostMergeInput, error) {
	if err := ValidateMergeResult(sealed, merge, limits); err != nil {
		return ObservePostMergeInput{}, err
	}
	digest, _ := limits.SHA256()
	return ObservePostMergeInput{cloneSealedAuthorization(sealed), merge, digest}, nil
}

func (i ObservePostMergeInput) Authority() Authority  { return i.sealed.input.MergeInput.authority }
func (i ObservePostMergeInput) Merge() MergeResult    { return i.merge }
func (i ObservePostMergeInput) Attempt() WriteAttempt { return i.sealed.input.MergeInput.attempt }
func (i ObservePostMergeInput) LimitsSHA256() string  { return i.limitsSHA }

type ReconcileWriteInput struct {
	attempt             WriteAttempt
	sealed              SealedMergeAuthorizationV1
	targetSubmission    TargetSubmissionV1
	observationIdentity string
	observationEvidence []ledger.EvidenceRef
	limitsSHA           string
}

func NewReconcileWriteInput(value any, limits Limits) (ReconcileWriteInput, error) {
	var attempt WriteAttempt
	var sealed SealedMergeAuthorizationV1
	switch typed := value.(type) {
	case WriteAttempt:
		attempt = typed
		if typed.operation == OperationMerge {
			return ReconcileWriteInput{}, errors.New("merge reconciliation requires the complete sealed authorization")
		}
	case SealedMergeAuthorizationV1:
		return ReconcileWriteInput{}, errors.New("merge reconciliation requires exact durable observation evidence")
	default:
		return ReconcileWriteInput{}, errors.New("reconciliation requires a write attempt or sealed merge authorization")
	}
	if !attempt.valid(limits) {
		return ReconcileWriteInput{}, errors.New("reconciliation input write attempt is invalid")
	}
	digest, _ := limits.SHA256()
	identity := attempt.writeID
	if sealed.valid() {
		identity = sealed.input.Commitment.SHA256()
	}
	return ReconcileWriteInput{attempt: attempt, sealed: sealed, observationIdentity: identity, limitsSHA: digest}, nil
}

func NewMergeReconcileWriteInput(sealed SealedMergeAuthorizationV1, submission TargetSubmissionV1, observationEvidence []ledger.EvidenceRef, limits Limits) (ReconcileWriteInput, error) {
	if !sealed.valid() || ValidateTargetSubmissionV1(sealed, submission, limits) != nil {
		return ReconcileWriteInput{}, errors.New("sealed merge authorization is invalid")
	}
	evidence := append([]ledger.EvidenceRef(nil), observationEvidence...)
	if len(evidence) == 0 || canonicalizeEvidence(&evidence, limits) != nil {
		return ReconcileWriteInput{}, errors.New("merge reconciliation durable observation evidence is invalid")
	}
	digest, err := limits.SHA256()
	if err != nil {
		return ReconcileWriteInput{}, err
	}
	return ReconcileWriteInput{attempt: sealed.input.MergeInput.attempt, sealed: cloneSealedAuthorization(sealed), targetSubmission: cloneTargetSubmission(submission), observationIdentity: submission.SHA256(), observationEvidence: evidence, limitsSHA: digest}, nil
}

func (i ReconcileWriteInput) Attempt() WriteAttempt { return i.attempt }
func (i ReconcileWriteInput) SealedAuthorization() (SealedMergeAuthorizationV1, bool) {
	return cloneSealedAuthorization(i.sealed), i.sealed.valid()
}
func (i ReconcileWriteInput) TargetSubmission() (TargetSubmissionV1, bool) {
	return cloneTargetSubmission(i.targetSubmission), i.targetSubmission.valid()
}
func (i ReconcileWriteInput) ObservationEvidence() []ledger.EvidenceRef {
	return append([]ledger.EvidenceRef(nil), i.observationEvidence...)
}
func (i ReconcileWriteInput) LimitsSHA256() string { return i.limitsSHA }

type PullRequestWriteResult struct {
	attempt   WriteAttempt
	snapshot  PullRequestSnapshot
	limitsSHA string
}

func NewPullRequestWriteResult(input UpsertPullRequestInput, snapshot PullRequestSnapshot, limits Limits) (PullRequestWriteResult, error) {
	if err := validateUpsertInput(input, limits); err != nil {
		return PullRequestWriteResult{}, err
	}
	if err := ValidatePullRequest(input.authority, snapshot, limits); err != nil {
		return PullRequestWriteResult{}, err
	}
	digest, _ := limits.SHA256()
	return PullRequestWriteResult{input.attempt, snapshot, digest}, nil
}

func (r PullRequestWriteResult) Attempt() WriteAttempt         { return r.attempt }
func (r PullRequestWriteResult) Snapshot() PullRequestSnapshot { return r.snapshot }
func (r PullRequestWriteResult) LimitsSHA256() string          { return r.limitsSHA }

func ValidatePullRequestWriteResult(input UpsertPullRequestInput, result PullRequestWriteResult, limits Limits) error {
	if err := validateUpsertInput(input, limits); err != nil {
		return err
	}
	if result.attempt != input.attempt || result.attempt.operation != OperationPullRequestUpsert {
		return errors.New("pull request write result replaced or mismatched the write attempt")
	}
	if err := requireLimitsSHA(limits, result.limitsSHA); err != nil {
		return err
	}
	return ValidatePullRequest(input.authority, result.snapshot, limits)
}

func validateUpsertInput(input UpsertPullRequestInput, limits Limits) error {
	if err := requireAuthority(input.authority); err != nil {
		return err
	}
	if err := requireLimitsSHA(limits, input.limitsSHA256); err != nil {
		return err
	}
	if !validText(input.title, limits.MaxTextBytes, false) || !validText(input.body, limits.MaxTextBytes, true) {
		return errors.New("pull request payload fails bounded text revalidation")
	}
	if !input.attempt.valid(limits) || !input.attempt.matchesAuthority(input.authority) || input.attempt.operation != OperationPullRequestUpsert {
		return errors.New("pull request write attempt is invalid")
	}
	payload, payloadSHA, err := canonicalJSON(struct {
		Authority    Authority `json:"authority"`
		Title        string    `json:"title"`
		Body         string    `json:"body"`
		LimitsSHA256 string    `json:"limits_sha256"`
	}{input.authority, input.title, input.body, input.limitsSHA256})
	if err != nil || payloadSHA != input.attempt.payloadSHA256 || string(payload) != string(input.canonicalPayload) {
		return errors.New("pull request canonical payload does not match write attempt")
	}
	return nil
}

type ReconciliationDisposition string

const (
	ReconciliationApplied    ReconciliationDisposition = "applied"
	ReconciliationNotApplied ReconciliationDisposition = "not_applied"
	ReconciliationUnknown    ReconciliationDisposition = "unknown"
)

type ReconciliationResult struct {
	attempt          WriteAttempt
	sealed           SealedMergeAuthorizationV1
	targetSubmission TargetSubmissionV1
	disposition      ReconciliationDisposition
	mergeResult      *MergeResult
	notAppliedProof  *NotAppliedProofV1
	evidence         []ledger.EvidenceRef
	limitsSHA        string
}

func NewReconciliationResult(attempt WriteAttempt, disposition ReconciliationDisposition, evidenceRefs []ledger.EvidenceRef, limits Limits) (ReconciliationResult, error) {
	if !attempt.valid(limits) || attempt.operation == OperationMerge || (disposition != ReconciliationApplied && disposition != ReconciliationNotApplied && disposition != ReconciliationUnknown) {
		return ReconciliationResult{}, errors.New("reconciliation result attempt or disposition is invalid")
	}
	copyEvidence := append([]ledger.EvidenceRef(nil), evidenceRefs...)
	if len(copyEvidence) == 0 {
		return ReconciliationResult{}, errors.New("reconciliation evidence is required")
	}
	if err := canonicalizeEvidence(&copyEvidence, limits); err != nil {
		return ReconciliationResult{}, err
	}
	digest, _ := limits.SHA256()
	return ReconciliationResult{attempt: attempt, disposition: disposition, evidence: copyEvidence, limitsSHA: digest}, nil
}

func (r ReconciliationResult) Attempt() WriteAttempt                  { return r.attempt }
func (r ReconciliationResult) Disposition() ReconciliationDisposition { return r.disposition }
func (r ReconciliationResult) LimitsSHA256() string                   { return r.limitsSHA }
func (r ReconciliationResult) TargetSubmission() (TargetSubmissionV1, bool) {
	return cloneTargetSubmission(r.targetSubmission), r.targetSubmission.valid()
}
func (r ReconciliationResult) Evidence() []ledger.EvidenceRef {
	return append([]ledger.EvidenceRef(nil), r.evidence...)
}
func (r ReconciliationResult) MergeResult() (MergeResult, bool) {
	if r.mergeResult == nil {
		return MergeResult{}, false
	}
	return *r.mergeResult, true
}

func NewMergeReconciliationResult(sealed SealedMergeAuthorizationV1, submission TargetSubmissionV1, disposition ReconciliationDisposition, mergeResult *MergeResult, notAppliedProof *NotAppliedProofV1, evidenceRefs []ledger.EvidenceRef, limits Limits) (ReconciliationResult, error) {
	if !sealed.valid() || ValidateTargetSubmissionV1(sealed, submission, limits) != nil || (disposition != ReconciliationApplied && disposition != ReconciliationNotApplied && disposition != ReconciliationUnknown) {
		return ReconciliationResult{}, errors.New("merge reconciliation input or disposition is invalid")
	}
	evidence := append([]ledger.EvidenceRef(nil), evidenceRefs...)
	if len(evidence) == 0 || canonicalizeEvidence(&evidence, limits) != nil {
		return ReconciliationResult{}, errors.New("merge reconciliation evidence is invalid")
	}
	result := ReconciliationResult{attempt: sealed.input.MergeInput.attempt, sealed: cloneSealedAuthorization(sealed), targetSubmission: cloneTargetSubmission(submission), disposition: disposition, evidence: evidence}
	switch disposition {
	case ReconciliationApplied:
		if mergeResult == nil || notAppliedProof != nil {
			return ReconciliationResult{}, errors.New("APPLIED reconciliation requires exactly one materialized merge result")
		}
		if err := ValidateMergeResult(sealed, *mergeResult, limits); err != nil {
			return ReconciliationResult{}, err
		}
		copy := *mergeResult
		result.mergeResult = &copy
	case ReconciliationNotApplied:
		if mergeResult != nil || notAppliedProof == nil || ValidateNotAppliedProofV1(sealed, submission, *notAppliedProof, limits) != nil ||
			!containsEvidence(evidence, notAppliedProof.input.EvidenceRef) {
			return ReconciliationResult{}, errors.New("NOT_APPLIED reconciliation requires exact typed authenticated proof")
		}
		copy := cloneNotAppliedProof(*notAppliedProof)
		result.notAppliedProof = &copy
	case ReconciliationUnknown:
		if mergeResult != nil || notAppliedProof != nil {
			return ReconciliationResult{}, errors.New("UNKNOWN reconciliation cannot claim a result or non-application proof")
		}
	}
	result.limitsSHA, _ = limits.SHA256()
	return result, nil
}

func ValidateReconciliationResult(sealed SealedMergeAuthorizationV1, submission TargetSubmissionV1, result ReconciliationResult, limits Limits) error {
	if !sealed.valid() || ValidateTargetSubmissionV1(sealed, submission, limits) != nil || result.sealed.SHA256() != sealed.SHA256() ||
		result.targetSubmission.SHA256() != submission.SHA256() || !bytes.Equal(result.targetSubmission.CanonicalJSON(), submission.CanonicalJSON()) ||
		result.attempt != sealed.input.MergeInput.attempt {
		return errors.New("reconciliation result does not bind the full sealed input")
	}
	rebuilt, err := NewMergeReconciliationResult(sealed, submission, result.disposition, result.mergeResult, result.notAppliedProof, result.evidence, limits)
	if err != nil {
		return err
	}
	if rebuilt.limitsSHA != result.limitsSHA {
		return errors.New("reconciliation limits identity changed")
	}
	return nil
}

type Provider interface {
	FindPullRequests(context.Context, FindPullRequestsInput) (PullRequestPage, error)
	GetCI(context.Context, GetCIInput) (CISnapshot, error)
	UpsertPullRequest(context.Context, UpsertPullRequestInput) (PullRequestWriteResult, error)
	Merge(context.Context, MergeExecutionInputV1) (MergeExecutionResultV1, error)
	ObservePostMerge(context.Context, ObservePostMergeInput) (PostMergeObservation, error)
	ReconcileWrite(context.Context, ReconcileWriteInput) (ReconciliationResult, error)
}

type FailureClass string

const (
	FailureProviderUnavailable FailureClass = "provider_unavailable"
	FailureAmbiguousWrite      FailureClass = "ambiguous_write"
	FailurePolicy              FailureClass = "policy_failure"
	FailureCI                  FailureClass = "ci_failure"
	FailureReview              FailureClass = "review_failure"
	FailureInvalidRemote       FailureClass = "invalid_remote_evidence"
)

type OperationError struct {
	class               FailureClass
	operation           string
	write               bool
	submitted           bool
	cause               error
	attempt             WriteAttempt
	hasAttempt          bool
	targetSubmission    TargetSubmissionV1
	hasTargetSubmission bool
}

func NewWriteExecutionError(attempt WriteAttempt, submitted bool, cause error) *OperationError {
	if cause == nil {
		cause = errors.New("provider execution failed")
	}
	class := FailureProviderUnavailable
	if submitted {
		class = FailureAmbiguousWrite
	}
	return &OperationError{class: class, operation: string(attempt.operation), write: true, submitted: submitted, cause: cause, attempt: attempt, hasAttempt: true}
}

func NewMergeExecutionError(input MergeExecutionInputV1, submitted bool, cause error, limits Limits) (*OperationError, error) {
	if err := validateMergeExecutionInputV1(input, limits); err != nil {
		return nil, err
	}
	failure := NewWriteExecutionError(input.sealed.input.MergeInput.attempt, submitted, cause)
	failure.targetSubmission = cloneTargetSubmission(input.submission)
	failure.hasTargetSubmission = true
	return failure, nil
}

func NewReadExecutionError(operation string, cause error) *OperationError {
	if cause == nil {
		cause = errors.New("provider execution failed")
	}
	return &OperationError{class: FailureProviderUnavailable, operation: operation, cause: cause}
}

func NewSubstantiveError(operation string, class FailureClass, cause error) (*OperationError, error) {
	if class != FailurePolicy && class != FailureCI && class != FailureReview && class != FailureInvalidRemote {
		return nil, errors.New("substantive error requires a policy, CI, review, or invalid-remote class")
	}
	if cause == nil {
		cause = errors.New(string(class))
	}
	return &OperationError{class: class, operation: operation, cause: cause}, nil
}

func (e *OperationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: %s: %v", e.operation, e.class, e.cause)
}
func (e *OperationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}
func (e *OperationError) Class() FailureClass {
	if e == nil {
		return ""
	}
	return e.class
}
func (e *OperationError) Submitted() bool { return e != nil && e.submitted }
func (e *OperationError) Write() bool     { return e != nil && e.write }
func (e *OperationError) Attempt() (WriteAttempt, bool) {
	if e == nil || !e.hasAttempt {
		return WriteAttempt{}, false
	}
	return e.attempt, true
}
func (e *OperationError) TargetSubmission() (TargetSubmissionV1, bool) {
	if e == nil || !e.hasTargetSubmission {
		return TargetSubmissionV1{}, false
	}
	return cloneTargetSubmission(e.targetSubmission), true
}

func ValidateMergeExecutionError(input MergeExecutionInputV1, failure *OperationError, limits Limits) error {
	if err := validateMergeExecutionInputV1(input, limits); err != nil {
		return err
	}
	if failure == nil || !failure.write || !failure.hasAttempt || failure.attempt != input.sealed.input.MergeInput.attempt ||
		!failure.hasTargetSubmission || failure.targetSubmission.SHA256() != input.submission.SHA256() ||
		!bytes.Equal(failure.targetSubmission.CanonicalJSON(), input.submission.CanonicalJSON()) {
		return errors.New("merge execution error does not bind the invoked target submission")
	}
	return nil
}

func CanRetry(failure *OperationError, attempts int, limits Limits, expected *WriteAttempt, reconciliation *ReconciliationResult) bool {
	if failure == nil || attempts < 0 || limits.Validate() != nil {
		return false
	}
	switch failure.class {
	case FailureAmbiguousWrite:
		if failure.hasAttempt && failure.attempt.operation == OperationMerge {
			return false
		}
		if expected == nil || reconciliation == nil || !failure.hasAttempt || failure.attempt != *expected || !failure.attempt.valid(limits) ||
			reconciliation.disposition != ReconciliationNotApplied || reconciliation.attempt != failure.attempt ||
			requireLimitsSHA(limits, reconciliation.limitsSHA) != nil || canonicalizeEvidenceCopy(reconciliation.evidence, limits) != nil {
			return false
		}
		return attempts < limits.MaxWriteRetries
	case FailureProviderUnavailable:
		if failure.write {
			return expected != nil && failure.hasAttempt && failure.attempt == *expected && failure.attempt.valid(limits) && attempts < limits.MaxWriteRetries
		}
		return attempts < limits.MaxReadRetries
	default:
		return false
	}
}

func canonicalizeEvidenceCopy(refs []ledger.EvidenceRef, limits Limits) error {
	copyRefs := append([]ledger.EvidenceRef(nil), refs...)
	return canonicalizeEvidence(&copyRefs, limits)
}

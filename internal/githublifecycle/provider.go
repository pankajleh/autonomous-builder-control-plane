package githublifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

// FindPullRequestsInput is structured read authority; it cannot carry a URL or
// shell fragment.
type FindPullRequestsInput struct {
	Repository Repository
	BaseBranch Branch
	HeadBranch Branch
	Page       int
	PerPage    int
}

func NewFindPullRequestsInput(repository Repository, base, head Branch, page, perPage int, limits Limits) (FindPullRequestsInput, error) {
	if !repository.valid() || !base.valid() || !head.valid() || base == head {
		return FindPullRequestsInput{}, errors.New("pull request query identity is invalid")
	}
	if err := validatePage(limits, page, perPage, perPage); err != nil {
		return FindPullRequestsInput{}, err
	}
	return FindPullRequestsInput{Repository: repository, BaseBranch: base, HeadBranch: head, Page: page, PerPage: perPage}, nil
}

type GetCIInput struct {
	Repository Repository
	HeadSHA    GitSHA
	Page       int
	PerPage    int
}

func NewGetCIInput(repository Repository, head GitSHA, page, perPage int, limits Limits) (GetCIInput, error) {
	if !repository.valid() || !head.valid() {
		return GetCIInput{}, errors.New("CI query identity is invalid")
	}
	if err := validatePage(limits, page, perPage, perPage); err != nil {
		return GetCIInput{}, err
	}
	return GetCIInput{Repository: repository, HeadSHA: head, Page: page, PerPage: perPage}, nil
}

// UpsertPullRequestInput binds a write to exact branch/SHA authority and the
// authenticated non-secret actor. Title/body are bounded display data only.
type UpsertPullRequestInput struct {
	authority Authority
	title     string
	body      string
}

func NewUpsertPullRequestInput(authority Authority, title, body string, limits Limits) (UpsertPullRequestInput, error) {
	if err := requireAuthority(authority); err != nil {
		return UpsertPullRequestInput{}, err
	}
	if err := limits.Validate(); err != nil {
		return UpsertPullRequestInput{}, err
	}
	if !validText(title, limits.MaxTextBytes, false) || !validText(body, limits.MaxTextBytes, true) {
		return UpsertPullRequestInput{}, errors.New("pull request title or body exceeds bounded safe text limits")
	}
	return UpsertPullRequestInput{authority: authority, title: title, body: body}, nil
}

func (i UpsertPullRequestInput) Authority() Authority { return i.authority }
func (i UpsertPullRequestInput) Title() string        { return i.title }
func (i UpsertPullRequestInput) Body() string         { return i.body }

// MergeInput carries exact accepted content, expected base tip, allowed method,
// PR identity, and authenticated actor through its immutable Authority.
type MergeInput struct {
	authority        Authority
	acceptedHeadTree GitSHA
	approvalEvidence []ledger.EvidenceRef
}

func NewMergeInput(authority Authority, acceptedHeadTree GitSHA, evidence []ledger.EvidenceRef, limits Limits) (MergeInput, error) {
	if err := requireAuthority(authority); err != nil {
		return MergeInput{}, err
	}
	if _, ok := authority.PullRequest(); !ok {
		return MergeInput{}, errors.New("merge input requires an exact pull request identity")
	}
	if !acceptedHeadTree.valid() {
		return MergeInput{}, errors.New("accepted head tree is invalid")
	}
	copyEvidence := append([]ledger.EvidenceRef(nil), evidence...)
	if len(copyEvidence) == 0 {
		return MergeInput{}, errors.New("merge approval evidence is required")
	}
	if err := canonicalizeEvidence(&copyEvidence, limits); err != nil {
		return MergeInput{}, err
	}
	return MergeInput{authority: authority, acceptedHeadTree: acceptedHeadTree, approvalEvidence: copyEvidence}, nil
}

func (i MergeInput) Authority() Authority     { return i.authority }
func (i MergeInput) AcceptedHeadTree() GitSHA { return i.acceptedHeadTree }
func (i MergeInput) Evidence() []ledger.EvidenceRef {
	return append([]ledger.EvidenceRef(nil), i.approvalEvidence...)
}

type ObservePostMergeInput struct {
	authority Authority
	merge     MergeResult
}

func NewObservePostMergeInput(authority Authority, merge MergeResult) (ObservePostMergeInput, error) {
	if err := ValidateMergeResult(authority, merge); err != nil {
		return ObservePostMergeInput{}, err
	}
	return ObservePostMergeInput{authority: authority, merge: merge}, nil
}

func (i ObservePostMergeInput) Authority() Authority { return i.authority }
func (i ObservePostMergeInput) Merge() MergeResult   { return i.merge }

type ReconcileWriteInput struct {
	repository Repository
	actor      ActingIdentity
	writeID    string
}

func NewReconcileWriteInput(repository Repository, actor ActingIdentity, writeID string, limits Limits) (ReconcileWriteInput, error) {
	if err := limits.Validate(); err != nil {
		return ReconcileWriteInput{}, err
	}
	if !repository.valid() || !actor.valid() || !validOpaqueID(writeID, limits.MaxTextBytes) {
		return ReconcileWriteInput{}, errors.New("reconciliation input identity is invalid")
	}
	return ReconcileWriteInput{repository: repository, actor: actor, writeID: writeID}, nil
}

func (i ReconcileWriteInput) Repository() Repository { return i.repository }
func (i ReconcileWriteInput) Actor() ActingIdentity  { return i.actor }
func (i ReconcileWriteInput) WriteID() string        { return i.writeID }

type PullRequestWriteResult struct {
	actor    ActingIdentity
	writeID  string
	snapshot PullRequestSnapshot
}

func NewPullRequestWriteResult(actor ActingIdentity, writeID string, snapshot PullRequestSnapshot, limits Limits) (PullRequestWriteResult, error) {
	if err := limits.Validate(); err != nil {
		return PullRequestWriteResult{}, err
	}
	if !actor.valid() || !validOpaqueID(writeID, limits.MaxTextBytes) || !snapshot.valid() {
		return PullRequestWriteResult{}, errors.New("pull request write result identity is invalid")
	}
	return PullRequestWriteResult{actor: actor, writeID: writeID, snapshot: snapshot}, nil
}

func (r PullRequestWriteResult) Actor() ActingIdentity         { return r.actor }
func (r PullRequestWriteResult) WriteID() string               { return r.writeID }
func (r PullRequestWriteResult) Snapshot() PullRequestSnapshot { return r.snapshot }

func ValidatePullRequestWriteResult(authority Authority, result PullRequestWriteResult) error {
	if err := requireAuthority(authority); err != nil {
		return err
	}
	if result.actor != authority.Actor() {
		return errors.New("pull request write acting identity does not match authority")
	}
	return ValidatePullRequest(authority, result.snapshot)
}

type ReconciliationDisposition string

const (
	ReconciliationApplied    ReconciliationDisposition = "applied"
	ReconciliationNotApplied ReconciliationDisposition = "not_applied"
	ReconciliationUnknown    ReconciliationDisposition = "unknown"
)

type ReconciliationResult struct {
	repository  Repository
	actor       ActingIdentity
	writeID     string
	disposition ReconciliationDisposition
	evidence    []ledger.EvidenceRef
}

func NewReconciliationResult(repository Repository, actor ActingIdentity, writeID string, disposition ReconciliationDisposition, evidence []ledger.EvidenceRef, limits Limits) (ReconciliationResult, error) {
	if err := limits.Validate(); err != nil {
		return ReconciliationResult{}, err
	}
	copyEvidence := append([]ledger.EvidenceRef(nil), evidence...)
	if !repository.valid() || !actor.valid() || !validOpaqueID(writeID, limits.MaxTextBytes) ||
		(disposition != ReconciliationApplied && disposition != ReconciliationNotApplied && disposition != ReconciliationUnknown) {
		return ReconciliationResult{}, errors.New("reconciliation result identity or disposition is invalid")
	}
	if len(copyEvidence) == 0 {
		return ReconciliationResult{}, errors.New("reconciliation evidence is required")
	}
	if err := canonicalizeEvidence(&copyEvidence, limits); err != nil {
		return ReconciliationResult{}, err
	}
	return ReconciliationResult{repository: repository, actor: actor, writeID: writeID, disposition: disposition, evidence: copyEvidence}, nil
}

func (r ReconciliationResult) Repository() Repository                 { return r.repository }
func (r ReconciliationResult) Actor() ActingIdentity                  { return r.actor }
func (r ReconciliationResult) WriteID() string                        { return r.writeID }
func (r ReconciliationResult) Disposition() ReconciliationDisposition { return r.disposition }
func (r ReconciliationResult) Evidence() []ledger.EvidenceRef {
	return append([]ledger.EvidenceRef(nil), r.evidence...)
}

// Provider is the complete network boundary frozen by the foundation. Every
// method is deadline/cancellation controlled by context.Context. Implementers
// must enforce Limits before returning any result.
type Provider interface {
	FindPullRequests(context.Context, FindPullRequestsInput) (PullRequestPage, error)
	GetCI(context.Context, GetCIInput) (CISnapshot, error)
	UpsertPullRequest(context.Context, UpsertPullRequestInput) (PullRequestWriteResult, error)
	Merge(context.Context, MergeInput) (MergeResult, error)
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

// OperationError separates execution availability from substantive governed
// failure. Submitted writes are always classified ambiguous, irrespective of
// the underlying cancellation, deadline, transport, or provider error.
type OperationError struct {
	class     FailureClass
	operation string
	write     bool
	submitted bool
	cause     error
	attempt   *WriteAttempt
}

// WriteAttempt is non-secret provenance for one idempotency identity. It is
// required before reconciliation can authorize replay of an ambiguous write.
type WriteAttempt struct {
	repository Repository
	actor      ActingIdentity
	writeID    string
}

func NewWriteAttempt(repository Repository, actor ActingIdentity, writeID string, limits Limits) (WriteAttempt, error) {
	if err := limits.Validate(); err != nil {
		return WriteAttempt{}, err
	}
	if !repository.valid() || !actor.valid() || !validOpaqueID(writeID, limits.MaxTextBytes) {
		return WriteAttempt{}, errors.New("write-attempt repository, actor, and identity are required")
	}
	return WriteAttempt{repository: repository, actor: actor, writeID: writeID}, nil
}

func NewWriteExecutionError(operation string, attempt WriteAttempt, submitted bool, cause error) *OperationError {
	result := NewExecutionError(operation, true, submitted, cause)
	copyAttempt := attempt
	result.attempt = &copyAttempt
	return result
}

func NewExecutionError(operation string, write, submitted bool, cause error) *OperationError {
	if cause == nil {
		cause = errors.New("provider execution failed")
	}
	class := FailureProviderUnavailable
	if write && submitted {
		class = FailureAmbiguousWrite
	}
	return &OperationError{class: class, operation: operation, write: write, submitted: submitted, cause: cause}
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

// CanRetry returns explicit retry authority. An ambiguous write is forbidden
// until matching reconciliation proves it was not applied; applied or unknown
// outcomes never authorize replay.
func CanRetry(failure *OperationError, attempts int, limits Limits, reconciliation *ReconciliationResult) bool {
	if failure == nil || attempts < 0 || limits.Validate() != nil {
		return false
	}
	switch failure.class {
	case FailureAmbiguousWrite:
		if reconciliation == nil || failure.attempt == nil || reconciliation.disposition != ReconciliationNotApplied ||
			reconciliation.repository != failure.attempt.repository || reconciliation.actor != failure.attempt.actor || reconciliation.writeID != failure.attempt.writeID {
			return false
		}
		return attempts < limits.MaxWriteRetries
	case FailureProviderUnavailable:
		if failure.write {
			return attempts < limits.MaxWriteRetries
		}
		return attempts < limits.MaxReadRetries
	default:
		return false
	}
}

// ContextError is a convenience that preserves the critical submitted-write
// rule for context cancellation and deadline errors.
func ContextError(operation string, write, submitted bool, err error) *OperationError {
	return NewExecutionError(operation, write, submitted, err)
}

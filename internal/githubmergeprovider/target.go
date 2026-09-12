package githubmergeprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/mergelifecycle"
)

const (
	targetTransportEvidenceKind = "github-target-transport-observation"
	refObservationEvidenceKind  = "github-ref-observation-body"
)

type gitRefResponse struct {
	Ref    string `json:"ref"`
	NodeID string `json:"node_id"`
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

func (p *Provider) SubmitTarget(ctx context.Context, input githublifecycle.MergeExecutionInputV1) (out mergelifecycle.TargetOutcome, returnedErr error) {
	meter := p.startMeter(ctx)
	defer func() {
		out.Accounting = p.finishMeter(meter)
		out.RequestBytes = out.Accounting.RequestBytes
	}()
	sealed := input.SealedAuthorization()
	submission := input.TargetSubmission()
	if err := githublifecycle.ValidateTargetSubmissionV1(sealed, submission, p.limits); err != nil {
		return out, errors.New("target submission fails independent validation")
	}
	if err := p.validateSealedCapability(sealed); err != nil {
		return p.zeroByteTargetOutcome(sealed, submission, err)
	}
	if submission.Method() != http.MethodPost || submission.Path() != githublifecycle.GitHubGraphQLPathV1 ||
		int64(len(submission.RequestBody())) != submission.RequestBodyBytes() || digest(submission.RequestBody()) != submission.RequestBodySHA256() {
		return p.zeroByteTargetOutcome(sealed, submission, errors.New("published target request identity diverged before transport"))
	}
	if !p.reserveTargetMutation(submission.SHA256()) {
		observation := localTargetEvidence(submission, "duplicate-target-submission-blocked")
		return mergelifecycle.TargetOutcome{Disposition: githublifecycle.ReconciliationUnknown, EvidenceRefs: []ledger.EvidenceRef{observation}}, nil
	}
	tracker := &submissionTracker{}
	response, requestErr := p.requestJSON(ctx, meter, p.mutationClient(tracker), tracker,
		submission.Method(), submission.Path(), submission.RequestBody())
	if requestErr != nil {
		if !tracker.possible() {
			return p.zeroByteTargetOutcome(sealed, submission, requestErr)
		}
		observation := localTargetEvidence(submission, "possible-request-bytes-without-valid-response")
		return mergelifecycle.TargetOutcome{Disposition: githublifecycle.ReconciliationUnknown, EvidenceRefs: []ledger.EvidenceRef{observation}}, nil
	}
	responseIdentity, err := githublifecycle.NewSnapshotIdentity("github", response.RequestID, response.ObservedNano)
	if err != nil {
		observation := localTargetEvidence(submission, "invalid-response-identity")
		return mergelifecycle.TargetOutcome{Disposition: githublifecycle.ReconciliationUnknown, EvidenceRefs: []ledger.EvidenceRef{observation}}, nil
	}
	bodyEvidence := evidence(evidenceURI("target-response-body", response.RequestID), githublifecycle.GitHubTargetResponseBodyEvidenceKindV1, response.Body)
	envelope, err := githublifecycle.NewTargetResponseEnvelopeV1(githublifecycle.TargetResponseEnvelopeV1Input{
		Response: responseIdentity, HTTPStatus: response.Status, ResponseBody: response.Body, BodyEvidence: bodyEvidence,
		EnvelopeURI: evidenceURI("target-response-envelope", response.RequestID),
	}, submission, p.limits)
	if err != nil {
		observation := localTargetEvidence(submission, "invalid-target-response-envelope")
		return mergelifecycle.TargetOutcome{Disposition: githublifecycle.ReconciliationUnknown, EvidenceRefs: []ledger.EvidenceRef{observation}}, nil
	}
	if proof, ok := p.atomicRejectionProof(sealed, submission, envelope, responseIdentity, response); ok {
		proofEvidence := proof.Input().EvidenceRef
		return mergelifecycle.TargetOutcome{
			Disposition: githublifecycle.ReconciliationNotApplied, NotAppliedProof: proof,
			EvidenceRefs: []ledger.EvidenceRef{bodyEvidence, envelope.EvidenceRef(), proofEvidence},
		}, nil
	}
	if response.Status != http.StatusOK {
		return mergelifecycle.TargetOutcome{Disposition: githublifecycle.ReconciliationUnknown,
			EvidenceRefs: []ledger.EvidenceRef{bodyEvidence, envelope.EvidenceRef()}}, nil
	}
	result, evidenceRefs, err := p.observeAppliedTarget(ctx, meter, sealed, responseIdentity,
		[]ledger.EvidenceRef{bodyEvidence, envelope.EvidenceRef()})
	if err != nil {
		return mergelifecycle.TargetOutcome{Disposition: githublifecycle.ReconciliationUnknown,
			EvidenceRefs: []ledger.EvidenceRef{bodyEvidence, envelope.EvidenceRef()}}, nil
	}
	typed, err := githublifecycle.NewMergeExecutionResultV1(input, envelope, result, p.limits)
	if err != nil || githublifecycle.ValidateMergeExecutionResultV1(input, typed, p.limits) != nil {
		return mergelifecycle.TargetOutcome{Disposition: githublifecycle.ReconciliationUnknown,
			EvidenceRefs: []ledger.EvidenceRef{bodyEvidence, envelope.EvidenceRef()}}, nil
	}
	return mergelifecycle.TargetOutcome{Disposition: githublifecycle.ReconciliationApplied, Result: result, EvidenceRefs: evidenceRefs}, nil
}

func (p *Provider) zeroByteTargetOutcome(sealed githublifecycle.SealedMergeAuthorizationV1, submission githublifecycle.TargetSubmissionV1, cause error) (mergelifecycle.TargetOutcome, error) {
	proofBytes, _ := json.Marshal(struct {
		Schema           string `json:"schema"`
		SubmissionSHA256 string `json:"submission_sha256"`
		RequestBytes     int64  `json:"request_bytes"`
		Classification   string `json:"classification"`
	}{"github-zero-request-byte-observation-v1", submission.SHA256(), 0, "pre_transport_or_proved_zero_plaintext_bytes"})
	proofEvidence := evidence(evidenceURI("target-zero-bytes", submission.InvocationID()), githublifecycle.NotAppliedZeroByteEvidenceKindV1, proofBytes)
	proof, err := githublifecycle.NewNotAppliedProofV1(githublifecycle.NotAppliedProofV1Input{
		Kind: githublifecycle.NotAppliedZeroRequestBytes, RequestBytes: 0, EvidenceRef: proofEvidence,
	}, sealed, submission, p.limits)
	if err != nil {
		return mergelifecycle.TargetOutcome{}, errors.Join(errors.New("zero-byte target proof construction failed"), cause)
	}
	return mergelifecycle.TargetOutcome{Disposition: githublifecycle.ReconciliationNotApplied, NotAppliedProof: proof,
		EvidenceRefs: []ledger.EvidenceRef{proofEvidence}}, nil
}

func (p *Provider) atomicRejectionProof(sealed githublifecycle.SealedMergeAuthorizationV1, submission githublifecycle.TargetSubmissionV1,
	envelope githublifecycle.TargetResponseEnvelopeV1, responseIdentity githublifecycle.SnapshotIdentity, response httpResult) (githublifecycle.NotAppliedProofV1, bool) {
	if response.Status != http.StatusOK {
		return githublifecycle.NotAppliedProofV1{}, false
	}
	evidenceRef := evidence(evidenceURI("target-atomic-rejection", response.RequestID), githublifecycle.NotAppliedAtomicRejectionEvidenceKindV1, response.Body)
	for _, kind := range []githublifecycle.NotAppliedProofKindV1{githublifecycle.NotAppliedAtomicBaseRejected, githublifecycle.NotAppliedAtomicHeadRejected} {
		proof, err := githublifecycle.NewNotAppliedProofV1(githublifecycle.NotAppliedProofV1Input{
			Kind: kind, RequestBytes: submission.RequestBodyBytes(), ResponseEnvelope: &envelope, Response: &responseIdentity,
			HTTPStatus: response.Status, ResponseBodySHA256: digest(response.Body), ResponseBody: response.Body, EvidenceRef: evidenceRef,
		}, sealed, submission, p.limits)
		if err == nil {
			return proof, true
		}
	}
	return githublifecycle.NotAppliedProofV1{}, false
}

func localTargetEvidence(submission githublifecycle.TargetSubmissionV1, classification string) ledger.EvidenceRef {
	data, _ := json.Marshal(struct {
		Schema           string `json:"schema"`
		SubmissionSHA256 string `json:"submission_sha256"`
		Classification   string `json:"classification"`
	}{"github-target-transport-observation-v1", submission.SHA256(), classification})
	return evidence(evidenceURI("target-transport", submission.InvocationID()), targetTransportEvidenceKind, data)
}

func (p *Provider) ReconcileTarget(ctx context.Context, input githublifecycle.ReconcileWriteInput) (out mergelifecycle.TargetOutcome, returnedErr error) {
	meter := p.startMeter(ctx)
	defer func() {
		out.Accounting = p.finishMeter(meter)
		out.RequestBytes = out.Accounting.RequestBytes
	}()
	sealed, sealedOK := input.SealedAuthorization()
	submission, submissionOK := input.TargetSubmission()
	if !sealedOK || !submissionOK || githublifecycle.ValidateTargetSubmissionV1(sealed, submission, p.limits) != nil ||
		p.validateSealedCapability(sealed) != nil {
		return out, errors.New("merge reconciliation requires the exact supported sealed submission")
	}
	result, evidenceRefs, err := p.observeAppliedTarget(ctx, meter, sealed, githublifecycle.SnapshotIdentity{}, input.ObservationEvidence())
	if err != nil {
		fallback := localTargetEvidence(submission, "read-only-reconciliation-unproved")
		return mergelifecycle.TargetOutcome{Disposition: githublifecycle.ReconciliationUnknown, EvidenceRefs: []ledger.EvidenceRef{fallback}}, nil
	}
	return mergelifecycle.TargetOutcome{Disposition: githublifecycle.ReconciliationApplied, Result: result, EvidenceRefs: evidenceRefs}, nil
}

func (p *Provider) observeAppliedTarget(ctx context.Context, meter *callMeter, sealed githublifecycle.SealedMergeAuthorizationV1,
	resultSnapshot githublifecycle.SnapshotIdentity, initialEvidence []ledger.EvidenceRef) (githublifecycle.MergeResult, []ledger.EvidenceRef, error) {
	mergeInput := sealed.MergeInput()
	authority := mergeInput.Authority()
	recipe := mergeInput.Recipe()
	base, baseSnapshot, baseEvidence, err := p.observeRef(ctx, meter, authority.Repository(), authority.BaseBranch())
	if err != nil {
		return githublifecycle.MergeResult{}, nil, err
	}
	head, _, headEvidence, err := p.observeRef(ctx, meter, authority.Repository(), authority.HeadBranch())
	if err != nil {
		return githublifecycle.MergeResult{}, nil, err
	}
	objectResponse, err := p.readJSON(ctx, meter, http.MethodGet,
		repoPath(authority.Repository())+"/git/commits/"+escapedSegment(recipe.ExpectedResultSHA().String()), nil)
	if err != nil {
		return githublifecycle.MergeResult{}, nil, err
	}
	if _, err := validateRemoteCommit(objectResponse.Body, recipe); err != nil {
		return githublifecycle.MergeResult{}, nil, err
	}
	if base != recipe.ExpectedResultSHA() || head != authority.HeadSHA() {
		return githublifecycle.MergeResult{}, nil, errors.New("independent base/head observation does not prove the exact atomic result")
	}
	objectEvidence := evidence(evidenceURI("target-result-object", objectResponse.RequestID), commitObjectEvidenceKind, objectResponse.Body)
	evidenceRefs := append([]ledger.EvidenceRef(nil), initialEvidence...)
	evidenceRefs = append(evidenceRefs, baseEvidence, headEvidence, objectEvidence)
	if resultSnapshot.Provider() == "" {
		resultSnapshot = baseSnapshot
	}
	pr, _ := authority.PullRequest()
	input := recipe.Input()
	result, err := githublifecycle.NewMergeResult(githublifecycle.MergeResultInput{
		Snapshot: resultSnapshot, Repository: authority.Repository(), PullRequest: pr, Actor: authority.Actor(),
		AcceptedHeadSHA: authority.HeadSHA(), AcceptedHeadTree: authority.ExpectedContent().ExpectedResultTreeSHA(),
		BaseBeforeSHA: authority.ExpectedBaseTipSHA(), Method: githublifecycle.MergeMethodMerge,
		ResultSHA: recipe.ExpectedResultSHA(), ResultTree: input.ExpectedResultTree, Parents: input.Parents,
		EvidenceRefs: evidenceRefs, Attempt: mergeInput.Attempt(), ExpectedContent: authority.ExpectedContent(),
		SealedAuthorization: sealed, Recipe: recipe,
	}, p.limits)
	return result, evidenceRefs, err
}

func (p *Provider) observeRef(ctx context.Context, meter *callMeter, repository githublifecycle.Repository, branch githublifecycle.Branch) (githublifecycle.GitSHA, githublifecycle.SnapshotIdentity, ledger.EvidenceRef, error) {
	path := repoPath(repository) + "/git/ref/heads/" + escapedBranchPath(branch.String())
	response, err := p.readJSON(ctx, meter, http.MethodGet, path, nil)
	if err != nil {
		return githublifecycle.GitSHA{}, githublifecycle.SnapshotIdentity{}, ledger.EvidenceRef{}, err
	}
	var remote gitRefResponse
	if err := json.Unmarshal(response.Body, &remote); err != nil || remote.Ref != "refs/heads/"+branch.String() || remote.Object.Type != "commit" {
		return githublifecycle.GitSHA{}, githublifecycle.SnapshotIdentity{}, ledger.EvidenceRef{}, errors.New("ref observation response is invalid")
	}
	sha, err := githublifecycle.NewGitSHA(remote.Object.SHA)
	if err != nil {
		return githublifecycle.GitSHA{}, githublifecycle.SnapshotIdentity{}, ledger.EvidenceRef{}, err
	}
	snapshot, err := githublifecycle.NewSnapshotIdentity("github", response.RequestID, response.ObservedNano)
	if err != nil {
		return githublifecycle.GitSHA{}, githublifecycle.SnapshotIdentity{}, ledger.EvidenceRef{}, err
	}
	return sha, snapshot, evidence(evidenceURI("ref-observation", response.RequestID), refObservationEvidenceKind, response.Body), nil
}

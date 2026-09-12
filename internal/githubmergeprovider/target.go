package githubmergeprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/mergelifecycle"
)

const (
	targetTransportEvidenceKind    = "github-target-transport-observation"
	refObservationEvidenceKind     = "github-ref-observation-body"
	targetVerificationEvidenceKind = "github-target-fixed-verification-body"
)

const targetVerificationQueryV1 = "query TargetVerification($owner:String!,$name:String!,$number:Int!,$base:String!,$head:String!,$result:GitObjectID!){viewer{id} repository(owner:$owner,name:$name){id databaseId pullRequest(number:$number){id databaseId number state isDraft merged mergedAt baseRefName baseRefOid baseRepository{id} headRefName headRefOid headRepository{id}} base:ref(qualifiedName:$base){name target{oid}} head:ref(qualifiedName:$head){name target{oid}} object:object(oid:$result){oid ... on Commit{message tree{oid} parents(first:3){nodes{oid}} author{name email date} committer{name email date}}}}}"

type targetVerificationRequest struct {
	Query     string                      `json:"query"`
	Variables targetVerificationVariables `json:"variables"`
}

type targetVerificationVariables struct {
	Owner  string `json:"owner"`
	Name   string `json:"name"`
	Number int64  `json:"number"`
	Base   string `json:"base"`
	Head   string `json:"head"`
	Result string `json:"result"`
}

type graphQLCommitObject struct {
	OID     string `json:"oid"`
	Message string `json:"message"`
	Tree    struct {
		OID string `json:"oid"`
	} `json:"tree"`
	Parents struct {
		Nodes []struct {
			OID string `json:"oid"`
		} `json:"nodes"`
	} `json:"parents"`
	Author struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Date  string `json:"date"`
	} `json:"author"`
	Committer struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Date  string `json:"date"`
	} `json:"committer"`
}

type targetVerificationResponse struct {
	Data struct {
		Viewer struct {
			ID string `json:"id"`
		} `json:"viewer"`
		Repository *struct {
			ID          string `json:"id"`
			DatabaseID  int64  `json:"databaseId"`
			PullRequest *struct {
				ID             string  `json:"id"`
				DatabaseID     int64   `json:"databaseId"`
				Number         int64   `json:"number"`
				State          string  `json:"state"`
				IsDraft        *bool   `json:"isDraft"`
				Merged         *bool   `json:"merged"`
				MergedAt       *string `json:"mergedAt"`
				BaseRefName    string  `json:"baseRefName"`
				BaseRefOID     string  `json:"baseRefOid"`
				BaseRepository *struct {
					ID string `json:"id"`
				} `json:"baseRepository"`
				HeadRefName    string `json:"headRefName"`
				HeadRefOID     string `json:"headRefOid"`
				HeadRepository *struct {
					ID string `json:"id"`
				} `json:"headRepository"`
			} `json:"pullRequest"`
			Base *struct {
				Name   string `json:"name"`
				Target *struct {
					OID string `json:"oid"`
				} `json:"target"`
			} `json:"base"`
			Head *struct {
				Name   string `json:"name"`
				Target *struct {
					OID string `json:"oid"`
				} `json:"target"`
			} `json:"head"`
			Object *graphQLCommitObject `json:"object"`
		} `json:"repository"`
	} `json:"data"`
	Errors []json.RawMessage `json:"errors,omitempty"`
}

type gitRefResponse struct {
	Ref    string `json:"ref"`
	NodeID string `json:"node_id"`
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

func (p *Provider) SubmitTarget(ctx context.Context, input githublifecycle.MergeExecutionInputV1) (out mergelifecycle.TargetOutcome, returnedErr error) {
	meter, err := p.startMeter(ctx)
	if err != nil {
		return out, err
	}
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
	response, requestErr := p.requestJSON(ctx, meter, mergelifecycle.ProviderCallTargetV1, p.mutationClient(tracker), tracker,
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
		[]ledger.EvidenceRef{bodyEvidence, envelope.EvidenceRef()}, mergelifecycle.ProviderCallPostMergeV1, false, false)
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
	meter, err := p.startMeter(ctx)
	if err != nil {
		return out, err
	}
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
	result, evidenceRefs, err := p.observeAppliedTarget(ctx, meter, sealed, githublifecycle.SnapshotIdentity{}, input.ObservationEvidence(),
		mergelifecycle.ProviderCallReconciliationV1, true, true)
	if err != nil {
		fallback := localTargetEvidence(submission, "read-only-reconciliation-unproved")
		return mergelifecycle.TargetOutcome{Disposition: githublifecycle.ReconciliationUnknown, EvidenceRefs: []ledger.EvidenceRef{fallback}}, nil
	}
	return mergelifecycle.TargetOutcome{Disposition: githublifecycle.ReconciliationApplied, Result: result, EvidenceRefs: evidenceRefs}, nil
}

func (p *Provider) observeAppliedTarget(ctx context.Context, meter *callMeter, sealed githublifecycle.SealedMergeAuthorizationV1,
	resultSnapshot githublifecycle.SnapshotIdentity, initialEvidence []ledger.EvidenceRef, class mergelifecycle.ProviderCallClassV1,
	requireMergedPR, requireContainment bool) (githublifecycle.MergeResult, []ledger.EvidenceRef, error) {
	mergeInput := sealed.MergeInput()
	authority := mergeInput.Authority()
	recipe := mergeInput.Recipe()
	pr, ok := authority.PullRequest()
	if !ok {
		return githublifecycle.MergeResult{}, nil, errors.New("target observation requires an exact pull request identity")
	}
	requestBody, err := json.Marshal(targetVerificationRequest{Query: targetVerificationQueryV1, Variables: targetVerificationVariables{
		Owner: authority.Repository().Owner(), Name: authority.Repository().Name(), Number: pr.Number(),
		Base: "refs/heads/" + authority.BaseBranch().String(), Head: "refs/heads/" + authority.HeadBranch().String(),
		Result: recipe.ExpectedResultSHA().String(),
	}})
	if err != nil {
		return githublifecycle.MergeResult{}, nil, errors.New("target verification query encoding failed")
	}
	var response httpResult
	if requireContainment {
		response, err = p.readJSON(ctx, meter, class, http.MethodPost, githublifecycle.GitHubGraphQLPathV1, requestBody)
	} else {
		response, err = p.readJSONOnce(ctx, meter, class, http.MethodPost, githublifecycle.GitHubGraphQLPathV1, requestBody)
	}
	if err != nil {
		return githublifecycle.MergeResult{}, nil, err
	}
	var remote targetVerificationResponse
	if err := json.Unmarshal(response.Body, &remote); err != nil || len(remote.Errors) != 0 || remote.Data.Repository == nil ||
		remote.Data.Repository.PullRequest == nil || remote.Data.Repository.Base == nil || remote.Data.Repository.Base.Target == nil ||
		remote.Data.Repository.Head == nil || remote.Data.Repository.Head.Target == nil || remote.Data.Repository.Object == nil {
		return githublifecycle.MergeResult{}, nil, errors.New("fixed target verification returned incomplete evidence")
	}
	repository := remote.Data.Repository
	observedPR := repository.PullRequest
	binding := authority.ReadyBinding().RepositoryBinding().Input()
	initialPR := mergeInput.InitialPullRequest().Input()
	if remote.Data.Viewer.ID != authority.Actor().Subject() || repository.ID != binding.GitHubRepositoryNodeID ||
		repository.DatabaseID != binding.GitHubRepositoryDatabaseID || observedPR.ID != pr.NodeID() || observedPR.DatabaseID != initialPR.PullRequestDatabaseID ||
		observedPR.Number != pr.Number() || observedPR.BaseRepository == nil || observedPR.HeadRepository == nil ||
		observedPR.BaseRepository.ID != binding.GitHubRepositoryNodeID || observedPR.HeadRepository.ID != binding.GitHubRepositoryNodeID ||
		observedPR.BaseRefName != authority.BaseBranch().String() || observedPR.HeadRefName != authority.HeadBranch().String() ||
		observedPR.HeadRefOID != authority.HeadSHA().String() || observedPR.IsDraft == nil || *observedPR.IsDraft {
		return githublifecycle.MergeResult{}, nil, errors.New("fresh target repository, pull request, or principal identity changed")
	}
	if requireMergedPR && (!strings.EqualFold(observedPR.State, string(githublifecycle.PullRequestMerged)) || observedPR.Merged == nil || !*observedPR.Merged || observedPR.MergedAt == nil) {
		return githublifecycle.MergeResult{}, nil, errors.New("fresh pull request evidence does not prove a merged pull request")
	}
	baseTip, err := githublifecycle.NewGitSHA(repository.Base.Target.OID)
	if err != nil || observedPR.BaseRefOID != baseTip.String() ||
		!exactGraphQLRefName(repository.Base.Name, authority.BaseBranch()) || !exactGraphQLRefName(repository.Head.Name, authority.HeadBranch()) ||
		repository.Head.Target.OID != authority.HeadSHA().String() {
		return githublifecycle.MergeResult{}, nil, errors.New("fixed target verification does not prove both governed refs")
	}
	if !requireContainment && baseTip != recipe.ExpectedResultSHA() {
		return githublifecycle.MergeResult{}, nil, errors.New("post-mutation target tip is not the exact result")
	}
	if err := validateGraphQLCommit(*repository.Object, recipe); err != nil {
		return githublifecycle.MergeResult{}, nil, err
	}
	snapshot, err := githublifecycle.NewSnapshotIdentity("github", response.RequestID, response.ObservedNano)
	if err != nil {
		return githublifecycle.MergeResult{}, nil, err
	}
	verificationEvidence := evidence(evidenceURI("target-fixed-verification", response.RequestID), targetVerificationEvidenceKind, response.Body)
	objectEvidence := evidence(evidenceURI("target-result-object", response.RequestID), commitObjectEvidenceKind, response.Body)
	evidenceRefs := append([]ledger.EvidenceRef(nil), initialEvidence...)
	evidenceRefs = append(evidenceRefs, verificationEvidence, objectEvidence)
	if requireContainment {
		comparePath := repoPath(authority.Repository()) + "/compare/" + escapedSegment(recipe.ExpectedResultSHA().String()) + "..." + escapedSegment(baseTip.String())
		comparisonResponse, compareErr := p.readJSON(ctx, meter, class, http.MethodGet, comparePath, nil)
		if compareErr != nil {
			return githublifecycle.MergeResult{}, nil, compareErr
		}
		compareEvidence, proofErr := p.validateTargetComparison(authority, recipe.ExpectedResultSHA(), baseTip, comparisonResponse)
		if proofErr != nil {
			return githublifecycle.MergeResult{}, nil, proofErr
		}
		evidenceRefs = append(evidenceRefs, compareEvidence)
	}
	if resultSnapshot.Provider() == "" {
		resultSnapshot = snapshot
	}
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

func exactGraphQLRefName(observed string, branch githublifecycle.Branch) bool {
	return observed == "refs/heads/"+branch.String()
}

func validateGraphQLCommit(observed graphQLCommitObject, recipe githublifecycle.MergeCommitRecipeV1) error {
	remote := gitCommitResponse{SHA: observed.OID, Message: observed.Message}
	remote.Tree.SHA = observed.Tree.OID
	remote.Parents = make([]struct {
		SHA string `json:"sha"`
	}, len(observed.Parents.Nodes))
	for index := range observed.Parents.Nodes {
		remote.Parents[index].SHA = observed.Parents.Nodes[index].OID
	}
	remote.Author.Name, remote.Author.Email, remote.Author.Date = observed.Author.Name, observed.Author.Email, observed.Author.Date
	remote.Committer.Name, remote.Committer.Email, remote.Committer.Date = observed.Committer.Name, observed.Committer.Email, observed.Committer.Date
	body, err := json.Marshal(remote)
	if err != nil {
		return errors.New("GraphQL result object cannot be reconstructed")
	}
	if _, err := validateRemoteCommit(body, recipe); err != nil {
		return errors.New("GraphQL result object does not match the exact deterministic recipe")
	}
	return nil
}

func (p *Provider) validateTargetComparison(authority githublifecycle.Authority, resultSHA, tip githublifecycle.GitSHA, response httpResult) (ledger.EvidenceRef, error) {
	var comparison compareResponse
	if err := json.Unmarshal(response.Body, &comparison); err != nil {
		return ledger.EvidenceRef{}, errors.New("GitHub compare response schema is invalid")
	}
	status := githublifecycle.TargetContainmentStatusV1(strings.ToLower(comparison.Status))
	if status != githublifecycle.TargetContainmentIdentical && status != githublifecycle.TargetContainmentAhead ||
		comparison.BaseCommit.SHA != resultSHA.String() || comparison.MergeBaseCommit.SHA != resultSHA.String() ||
		comparison.AheadBy < 0 || comparison.AheadBy > p.limits.MaxDescendantDistance || comparison.BehindBy != 0 ||
		status == githublifecycle.TargetContainmentIdentical && (tip != resultSHA || comparison.AheadBy != 0) ||
		status == githublifecycle.TargetContainmentAhead && (tip == resultSHA || comparison.AheadBy <= 0) {
		return ledger.EvidenceRef{}, errors.New("GitHub compare response does not prove bounded result containment")
	}
	snapshot, err := githublifecycle.NewSnapshotIdentity("github", response.RequestID, response.ObservedNano)
	if err != nil {
		return ledger.EvidenceRef{}, err
	}
	compareEvidence := evidence(evidenceURI("target-reconcile-compare", response.RequestID), compareEvidenceKind, response.Body)
	if _, err := githublifecycle.NewTargetContainmentProofV1(githublifecycle.TargetContainmentProofV1Input{
		Snapshot: snapshot, Repository: authority.Repository(), TargetRef: "refs/heads/" + authority.BaseBranch().String(),
		ResultSHA: resultSHA, ObservedTargetTipSHA: tip, Mechanism: githublifecycle.GitHubCompareProofV1,
		Status: status, MergeBaseSHA: resultSHA, DescendantDistance: comparison.AheadBy, EvidenceRefs: []ledger.EvidenceRef{compareEvidence},
	}, p.limits); err != nil {
		return ledger.EvidenceRef{}, err
	}
	return compareEvidence, nil
}

func (p *Provider) observeRef(ctx context.Context, meter *callMeter, class mergelifecycle.ProviderCallClassV1, repository githublifecycle.Repository, branch githublifecycle.Branch) (githublifecycle.GitSHA, githublifecycle.SnapshotIdentity, ledger.EvidenceRef, error) {
	path := repoPath(repository) + "/git/ref/heads/" + escapedBranchPath(branch.String())
	response, err := p.readJSON(ctx, meter, class, http.MethodGet, path, nil)
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

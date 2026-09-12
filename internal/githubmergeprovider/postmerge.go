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
	compareEvidenceKind = "github-compare-response-body"
)

type compareResponse struct {
	Status       string `json:"status"`
	AheadBy      int    `json:"ahead_by"`
	BehindBy     int    `json:"behind_by"`
	TotalCommits int    `json:"total_commits"`
	BaseCommit   struct {
		SHA string `json:"sha"`
	} `json:"base_commit"`
	MergeBaseCommit struct {
		SHA string `json:"sha"`
	} `json:"merge_base_commit"`
}

func (p *Provider) ObservePostMerge(ctx context.Context, input githublifecycle.ObservePostMergeInput) (out mergelifecycle.PostMergeOutcome, returnedErr error) {
	meter := p.startMeter(ctx)
	defer func() { out.Accounting = p.finishMeter(meter) }()
	result := input.Merge()
	resultInput := result.Input()
	sealed := resultInput.SealedAuthorization
	if err := p.validateSealedCapability(sealed); err != nil {
		return out, err
	}
	if err := githublifecycle.ValidateMergeResult(sealed, result, p.limits); err != nil {
		return out, err
	}
	authority := input.Authority()
	recipe := resultInput.Recipe
	objectResponse, err := p.readJSON(ctx, meter, http.MethodGet,
		repoPath(authority.Repository())+"/git/commits/"+escapedSegment(resultInput.ResultSHA.String()), nil)
	if err != nil {
		return out, err
	}
	if _, err := validateRemoteCommit(objectResponse.Body, recipe); err != nil {
		return out, err
	}
	objectSnapshot, err := githublifecycle.NewSnapshotIdentity("github", objectResponse.RequestID, objectResponse.ObservedNano)
	if err != nil {
		return out, err
	}
	objectEvidence := evidence(evidenceURI("post-merge-result-object", objectResponse.RequestID), commitObjectEvidenceKind, objectResponse.Body)
	resultObject, err := githublifecycle.NewResultCommitObservationV1(githublifecycle.ResultCommitObservationV1Input{
		Snapshot: objectSnapshot, Repository: authority.Repository(), ResultSHA: resultInput.ResultSHA,
		ResultTree: resultInput.ResultTree, Parents: resultInput.Parents, Message: recipe.Input().Message,
		Author: recipe.Input().Author, Committer: recipe.Input().Committer, AuthorUnix: recipe.Input().AuthorUnix,
		CommitterUnix: recipe.Input().CommitterUnix, RecipeSHA256: recipe.SHA256(), EvidenceRefs: []ledger.EvidenceRef{objectEvidence},
	}, p.limits)
	if err != nil {
		return out, err
	}
	tip, _, refEvidence, err := p.observeRef(ctx, meter, authority.Repository(), authority.BaseBranch())
	if err != nil {
		return out, err
	}
	comparePath := repoPath(authority.Repository()) + "/compare/" + escapedSegment(resultInput.ResultSHA.String()) + "..." + escapedSegment(tip.String())
	comparisonResponse, err := p.readJSON(ctx, meter, http.MethodGet, comparePath, nil)
	if err != nil {
		return out, err
	}
	var comparison compareResponse
	if err := json.Unmarshal(comparisonResponse.Body, &comparison); err != nil {
		return out, errors.New("GitHub compare response schema is invalid")
	}
	status := githublifecycle.TargetContainmentStatusV1(strings.ToLower(comparison.Status))
	if status != githublifecycle.TargetContainmentIdentical && status != githublifecycle.TargetContainmentAhead {
		return out, errors.New("target is not proved equal to or ahead of the exact result")
	}
	if comparison.BaseCommit.SHA != resultInput.ResultSHA.String() || comparison.MergeBaseCommit.SHA != resultInput.ResultSHA.String() ||
		comparison.AheadBy < 0 || comparison.AheadBy > p.limits.MaxDescendantDistance || comparison.BehindBy != 0 ||
		status == githublifecycle.TargetContainmentIdentical && (tip != resultInput.ResultSHA || comparison.AheadBy != 0) ||
		status == githublifecycle.TargetContainmentAhead && (tip == resultInput.ResultSHA || comparison.AheadBy <= 0) {
		return out, errors.New("GitHub compare response does not prove bounded result containment")
	}
	compareSnapshot, err := githublifecycle.NewSnapshotIdentity("github", comparisonResponse.RequestID, comparisonResponse.ObservedNano)
	if err != nil {
		return out, err
	}
	compareEvidence := evidence(evidenceURI("post-merge-compare", comparisonResponse.RequestID), compareEvidenceKind, comparisonResponse.Body)
	containment, err := githublifecycle.NewTargetContainmentProofV1(githublifecycle.TargetContainmentProofV1Input{
		Snapshot: compareSnapshot, Repository: authority.Repository(), TargetRef: "refs/heads/" + authority.BaseBranch().String(),
		ResultSHA: resultInput.ResultSHA, ObservedTargetTipSHA: tip, Mechanism: githublifecycle.GitHubCompareProofV1,
		Status: status, MergeBaseSHA: resultInput.ResultSHA, DescendantDistance: comparison.AheadBy,
		EvidenceRefs: []ledger.EvidenceRef{refEvidence, compareEvidence},
	}, p.limits)
	if err != nil {
		return out, err
	}
	evidenceRefs := []ledger.EvidenceRef{objectEvidence, refEvidence, compareEvidence}
	observation, err := githublifecycle.NewPostMergeObservation(githublifecycle.PostMergeObservationInput{
		Snapshot: compareSnapshot, Repository: resultInput.Repository, BaseBranch: authority.BaseBranch(),
		PullRequest: resultInput.PullRequest, Actor: resultInput.Actor, AcceptedHeadSHA: resultInput.AcceptedHeadSHA,
		AcceptedHeadTree: resultInput.AcceptedHeadTree, BaseBeforeSHA: resultInput.BaseBeforeSHA, Method: resultInput.Method,
		ResultSHA: resultInput.ResultSHA, ObservedTargetTipSHA: tip, ResultTree: resultInput.ResultTree,
		Parents: resultInput.Parents, Lineage: resultInput.Lineage, EvidenceRefs: evidenceRefs, Attempt: resultInput.Attempt,
		ExpectedContent: resultInput.ExpectedContent, SealedAuthorization: sealed, ResultObject: resultObject, ContainmentProof: containment,
	}, p.limits)
	if err != nil {
		return out, err
	}
	if err := githublifecycle.VerifyPostMerge(sealed, result, observation, p.limits); err != nil {
		return out, err
	}
	return mergelifecycle.PostMergeOutcome{Observation: observation}, nil
}

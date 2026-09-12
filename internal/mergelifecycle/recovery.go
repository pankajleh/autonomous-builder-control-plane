package mergelifecycle

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

type recoveryIdentityWire struct {
	Provider         string `json:"provider"`
	RequestID        string `json:"request_id"`
	ObservedUnixNano int64  `json:"observed_unix_nano"`
}
type recoveryRepositoryWire struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}
type recoveryPullRequestWire struct {
	Number int64  `json:"number"`
	NodeID string `json:"node_id"`
}
type recoveryActorWire struct {
	Kind           githublifecycle.ActingKind `json:"kind"`
	Subject        string                     `json:"subject"`
	InstallationID int64                      `json:"installation_id,omitempty"`
}

type recoveryMergeWire struct {
	Snapshot                  recoveryIdentityWire        `json:"snapshot"`
	Repository                recoveryRepositoryWire      `json:"repository"`
	PullRequest               recoveryPullRequestWire     `json:"pull_request"`
	Actor                     recoveryActorWire           `json:"actor"`
	AcceptedHeadSHA           string                      `json:"accepted_head_sha"`
	AcceptedHeadTree          string                      `json:"accepted_head_tree"`
	BaseBeforeSHA             string                      `json:"base_before_sha"`
	Method                    githublifecycle.MergeMethod `json:"method"`
	ResultSHA                 string                      `json:"result_sha"`
	ResultTree                string                      `json:"result_tree"`
	Parents                   []string                    `json:"parents"`
	Lineage                   []json.RawMessage           `json:"lineage"`
	EvidenceRefs              []ledger.EvidenceRef        `json:"evidence_refs,omitempty"`
	Metadata                  map[string]string           `json:"metadata,omitempty"`
	Attempt                   json.RawMessage             `json:"write_attempt"`
	ExpectedContent           json.RawMessage             `json:"expected_merge_content"`
	SealedAuthorization       json.RawMessage             `json:"sealed_authorization"`
	SealedAuthorizationSHA256 string                      `json:"sealed_authorization_sha256"`
	Recipe                    json.RawMessage             `json:"merge_commit_recipe"`
	RecipeSHA256              string                      `json:"merge_commit_recipe_sha256"`
	LimitsSHA256              string                      `json:"limits_sha256"`
}

func parseMergeResult(data []byte, sealed githublifecycle.SealedMergeAuthorizationV1, limits githublifecycle.Limits) (githublifecycle.MergeResult, error) {
	var wire recoveryMergeWire
	if err := strictCanonical(data, &wire); err != nil {
		return githublifecycle.MergeResult{}, err
	}
	input := sealed.MergeInput()
	authority := input.Authority()
	if wire.SealedAuthorizationSHA256 != sealed.SHA256() || !bytes.Equal(wire.SealedAuthorization, sealed.CanonicalJSON()) ||
		wire.RecipeSHA256 != input.Recipe().SHA256() || !bytes.Equal(wire.Recipe, input.Recipe().CanonicalJSON()) ||
		!bytes.Equal(wire.ExpectedContent, input.ExpectedContent().CanonicalJSON()) || len(wire.Lineage) != 0 ||
		wire.LimitsSHA256 != input.LimitsSHA256() {
		return githublifecycle.MergeResult{}, errors.New("merge result changed its sealed authority or production profile")
	}
	snapshot, err := githublifecycle.NewSnapshotIdentity(wire.Snapshot.Provider, wire.Snapshot.RequestID, wire.Snapshot.ObservedUnixNano)
	if err != nil {
		return githublifecycle.MergeResult{}, err
	}
	repository, err := githublifecycle.NewRepository(wire.Repository.Owner, wire.Repository.Name)
	if err != nil {
		return githublifecycle.MergeResult{}, err
	}
	pullRequest, err := githublifecycle.NewPullRequestIdentity(wire.PullRequest.Number, wire.PullRequest.NodeID)
	if err != nil {
		return githublifecycle.MergeResult{}, err
	}
	actor, err := recoverActor(wire.Actor)
	if err != nil {
		return githublifecycle.MergeResult{}, err
	}
	acceptedHead, err := recoverSHA(wire.AcceptedHeadSHA)
	if err != nil {
		return githublifecycle.MergeResult{}, err
	}
	acceptedTree, err := recoverSHA(wire.AcceptedHeadTree)
	if err != nil {
		return githublifecycle.MergeResult{}, err
	}
	baseBefore, err := recoverSHA(wire.BaseBeforeSHA)
	if err != nil {
		return githublifecycle.MergeResult{}, err
	}
	resultSHA, err := recoverSHA(wire.ResultSHA)
	if err != nil {
		return githublifecycle.MergeResult{}, err
	}
	resultTree, err := recoverSHA(wire.ResultTree)
	if err != nil {
		return githublifecycle.MergeResult{}, err
	}
	parents, err := recoverSHAs(wire.Parents)
	if err != nil {
		return githublifecycle.MergeResult{}, err
	}
	value, err := githublifecycle.NewMergeResult(githublifecycle.MergeResultInput{
		Snapshot: snapshot, Repository: repository, PullRequest: pullRequest, Actor: actor, AcceptedHeadSHA: acceptedHead,
		AcceptedHeadTree: acceptedTree, BaseBeforeSHA: baseBefore, Method: wire.Method, ResultSHA: resultSHA, ResultTree: resultTree,
		Parents: parents, EvidenceRefs: wire.EvidenceRefs, Metadata: wire.Metadata, Attempt: input.Attempt(),
		ExpectedContent: input.ExpectedContent(), SealedAuthorization: sealed, Recipe: input.Recipe(),
	}, limits)
	if err != nil || repository != authority.Repository() || actor != authority.Actor() || !bytes.Equal(value.CanonicalJSON(), data) {
		return githublifecycle.MergeResult{}, errors.Join(errors.New("merge result fails strict sealed recovery"), err)
	}
	return value, nil
}

type recoveryResultObjectWire struct {
	Schema        string                                `json:"schema"`
	Snapshot      recoveryIdentityWire                  `json:"snapshot"`
	Repository    recoveryRepositoryWire                `json:"repository"`
	ResultSHA     string                                `json:"result_sha"`
	ResultTree    string                                `json:"result_tree"`
	Parents       []string                              `json:"parents"`
	Message       string                                `json:"message"`
	Author        githublifecycle.MergeCommitIdentityV1 `json:"author"`
	Committer     githublifecycle.MergeCommitIdentityV1 `json:"committer"`
	AuthorUnix    int64                                 `json:"author_unix"`
	CommitterUnix int64                                 `json:"committer_unix"`
	RecipeSHA256  string                                `json:"recipe_sha256"`
	EvidenceRefs  []ledger.EvidenceRef                  `json:"evidence_refs"`
	LimitsSHA256  string                                `json:"limits_sha256"`
}
type recoveryContainmentWire struct {
	Schema               string                                    `json:"schema"`
	Snapshot             recoveryIdentityWire                      `json:"snapshot"`
	Repository           recoveryRepositoryWire                    `json:"repository"`
	TargetRef            string                                    `json:"target_ref"`
	ResultSHA            string                                    `json:"result_sha"`
	ObservedTargetTipSHA string                                    `json:"observed_target_tip_sha"`
	Mechanism            string                                    `json:"mechanism"`
	Status               githublifecycle.TargetContainmentStatusV1 `json:"status"`
	MergeBaseSHA         string                                    `json:"merge_base_sha"`
	DescendantDistance   int                                       `json:"descendant_distance"`
	EvidenceRefs         []ledger.EvidenceRef                      `json:"evidence_refs"`
	LimitsSHA256         string                                    `json:"limits_sha256"`
}
type recoveryPostMergeWire struct {
	Snapshot                  recoveryIdentityWire        `json:"snapshot"`
	Repository                recoveryRepositoryWire      `json:"repository"`
	BaseBranch                string                      `json:"base_branch"`
	PullRequest               recoveryPullRequestWire     `json:"pull_request"`
	Actor                     recoveryActorWire           `json:"actor"`
	AcceptedHeadSHA           string                      `json:"accepted_head_sha"`
	AcceptedHeadTree          string                      `json:"accepted_head_tree"`
	BaseBeforeSHA             string                      `json:"base_before_sha"`
	Method                    githublifecycle.MergeMethod `json:"method"`
	ResultSHA                 string                      `json:"result_sha"`
	ObservedTargetTipSHA      string                      `json:"observed_target_tip_sha"`
	ResultTree                string                      `json:"result_tree"`
	Parents                   []string                    `json:"parents"`
	Lineage                   []json.RawMessage           `json:"lineage"`
	EvidenceRefs              []ledger.EvidenceRef        `json:"evidence_refs,omitempty"`
	Metadata                  map[string]string           `json:"metadata,omitempty"`
	Attempt                   json.RawMessage             `json:"write_attempt"`
	ExpectedContent           json.RawMessage             `json:"expected_merge_content"`
	SealedAuthorization       json.RawMessage             `json:"sealed_authorization"`
	SealedAuthorizationSHA256 string                      `json:"sealed_authorization_sha256"`
	ResultObject              json.RawMessage             `json:"result_object"`
	ResultObjectSHA256        string                      `json:"result_object_sha256"`
	ContainmentProof          json.RawMessage             `json:"target_containment_proof"`
	ContainmentProofSHA256    string                      `json:"target_containment_proof_sha256"`
	LimitsSHA256              string                      `json:"limits_sha256"`
}

func parsePostMerge(data []byte, sealed githublifecycle.SealedMergeAuthorizationV1, result githublifecycle.MergeResult, limits githublifecycle.Limits) (githublifecycle.PostMergeObservation, error) {
	var wire recoveryPostMergeWire
	if err := strictCanonical(data, &wire); err != nil {
		return githublifecycle.PostMergeObservation{}, err
	}
	input := sealed.MergeInput()
	if wire.SealedAuthorizationSHA256 != sealed.SHA256() || !bytes.Equal(wire.SealedAuthorization, sealed.CanonicalJSON()) ||
		!bytes.Equal(wire.ExpectedContent, input.ExpectedContent().CanonicalJSON()) || len(wire.Lineage) != 0 || wire.LimitsSHA256 != input.LimitsSHA256() {
		return githublifecycle.PostMergeObservation{}, errors.New("post-merge proof changed its sealed authority")
	}
	resultObject, err := parseResultObject(wire.ResultObject, limits)
	if err != nil || resultObject.SHA256() != wire.ResultObjectSHA256 {
		return githublifecycle.PostMergeObservation{}, errors.Join(errors.New("result object proof recovery failed"), err)
	}
	containment, err := parseContainment(wire.ContainmentProof, limits)
	if err != nil || containment.SHA256() != wire.ContainmentProofSHA256 {
		return githublifecycle.PostMergeObservation{}, errors.Join(errors.New("containment proof recovery failed"), err)
	}
	snapshot, err := githublifecycle.NewSnapshotIdentity(wire.Snapshot.Provider, wire.Snapshot.RequestID, wire.Snapshot.ObservedUnixNano)
	if err != nil {
		return githublifecycle.PostMergeObservation{}, err
	}
	repository, err := githublifecycle.NewRepository(wire.Repository.Owner, wire.Repository.Name)
	if err != nil {
		return githublifecycle.PostMergeObservation{}, err
	}
	baseBranch, err := githublifecycle.NewBranch(wire.BaseBranch)
	if err != nil {
		return githublifecycle.PostMergeObservation{}, err
	}
	pullRequest, err := githublifecycle.NewPullRequestIdentity(wire.PullRequest.Number, wire.PullRequest.NodeID)
	if err != nil {
		return githublifecycle.PostMergeObservation{}, err
	}
	actor, err := recoverActor(wire.Actor)
	if err != nil {
		return githublifecycle.PostMergeObservation{}, err
	}
	acceptedHead, err := recoverSHA(wire.AcceptedHeadSHA)
	if err != nil {
		return githublifecycle.PostMergeObservation{}, err
	}
	acceptedTree, err := recoverSHA(wire.AcceptedHeadTree)
	if err != nil {
		return githublifecycle.PostMergeObservation{}, err
	}
	baseBefore, err := recoverSHA(wire.BaseBeforeSHA)
	if err != nil {
		return githublifecycle.PostMergeObservation{}, err
	}
	resultSHA, err := recoverSHA(wire.ResultSHA)
	if err != nil {
		return githublifecycle.PostMergeObservation{}, err
	}
	targetTip, err := recoverSHA(wire.ObservedTargetTipSHA)
	if err != nil {
		return githublifecycle.PostMergeObservation{}, err
	}
	resultTree, err := recoverSHA(wire.ResultTree)
	if err != nil {
		return githublifecycle.PostMergeObservation{}, err
	}
	parents, err := recoverSHAs(wire.Parents)
	if err != nil {
		return githublifecycle.PostMergeObservation{}, err
	}
	value, err := githublifecycle.NewPostMergeObservation(githublifecycle.PostMergeObservationInput{
		Snapshot: snapshot, Repository: repository, BaseBranch: baseBranch, PullRequest: pullRequest, Actor: actor,
		AcceptedHeadSHA: acceptedHead, AcceptedHeadTree: acceptedTree, BaseBeforeSHA: baseBefore, Method: wire.Method,
		ResultSHA: resultSHA, ObservedTargetTipSHA: targetTip, ResultTree: resultTree, Parents: parents,
		EvidenceRefs: wire.EvidenceRefs, Metadata: wire.Metadata, Attempt: input.Attempt(), ExpectedContent: input.ExpectedContent(),
		SealedAuthorization: sealed, ResultObject: resultObject, ContainmentProof: containment,
	}, limits)
	if err != nil || !bytes.Equal(value.CanonicalJSON(), data) || githublifecycle.VerifyPostMerge(sealed, result, value, limits) != nil {
		return githublifecycle.PostMergeObservation{}, errors.Join(errors.New("post-merge proof fails strict recovery"), err)
	}
	return value, nil
}

func parseResultObject(data []byte, limits githublifecycle.Limits) (githublifecycle.ResultCommitObservationV1, error) {
	var wire recoveryResultObjectWire
	if err := strictCanonical(data, &wire); err != nil {
		return githublifecycle.ResultCommitObservationV1{}, err
	}
	snapshot, err := githublifecycle.NewSnapshotIdentity(wire.Snapshot.Provider, wire.Snapshot.RequestID, wire.Snapshot.ObservedUnixNano)
	if err != nil {
		return githublifecycle.ResultCommitObservationV1{}, err
	}
	repository, err := githublifecycle.NewRepository(wire.Repository.Owner, wire.Repository.Name)
	if err != nil {
		return githublifecycle.ResultCommitObservationV1{}, err
	}
	resultSHA, err := recoverSHA(wire.ResultSHA)
	if err != nil {
		return githublifecycle.ResultCommitObservationV1{}, err
	}
	resultTree, err := recoverSHA(wire.ResultTree)
	if err != nil {
		return githublifecycle.ResultCommitObservationV1{}, err
	}
	parents, err := recoverSHAs(wire.Parents)
	if err != nil {
		return githublifecycle.ResultCommitObservationV1{}, err
	}
	value, err := githublifecycle.NewResultCommitObservationV1(githublifecycle.ResultCommitObservationV1Input{Snapshot: snapshot, Repository: repository,
		ResultSHA: resultSHA, ResultTree: resultTree, Parents: parents, Message: wire.Message, Author: wire.Author, Committer: wire.Committer,
		AuthorUnix: wire.AuthorUnix, CommitterUnix: wire.CommitterUnix, RecipeSHA256: wire.RecipeSHA256, EvidenceRefs: wire.EvidenceRefs}, limits)
	if err != nil || wire.Schema != githublifecycle.ResultCommitObservationSchemaV1 || !bytes.Equal(value.CanonicalJSON(), data) {
		return githublifecycle.ResultCommitObservationV1{}, errors.Join(errors.New("result object record fails strict recovery"), err)
	}
	return value, nil
}

func parseContainment(data []byte, limits githublifecycle.Limits) (githublifecycle.TargetContainmentProofV1, error) {
	var wire recoveryContainmentWire
	if err := strictCanonical(data, &wire); err != nil {
		return githublifecycle.TargetContainmentProofV1{}, err
	}
	snapshot, err := githublifecycle.NewSnapshotIdentity(wire.Snapshot.Provider, wire.Snapshot.RequestID, wire.Snapshot.ObservedUnixNano)
	if err != nil {
		return githublifecycle.TargetContainmentProofV1{}, err
	}
	repository, err := githublifecycle.NewRepository(wire.Repository.Owner, wire.Repository.Name)
	if err != nil {
		return githublifecycle.TargetContainmentProofV1{}, err
	}
	resultSHA, err := recoverSHA(wire.ResultSHA)
	if err != nil {
		return githublifecycle.TargetContainmentProofV1{}, err
	}
	targetTip, err := recoverSHA(wire.ObservedTargetTipSHA)
	if err != nil {
		return githublifecycle.TargetContainmentProofV1{}, err
	}
	mergeBase, err := recoverSHA(wire.MergeBaseSHA)
	if err != nil {
		return githublifecycle.TargetContainmentProofV1{}, err
	}
	value, err := githublifecycle.NewTargetContainmentProofV1(githublifecycle.TargetContainmentProofV1Input{Snapshot: snapshot, Repository: repository,
		TargetRef: wire.TargetRef, ResultSHA: resultSHA, ObservedTargetTipSHA: targetTip, Mechanism: wire.Mechanism,
		Status: wire.Status, MergeBaseSHA: mergeBase, DescendantDistance: wire.DescendantDistance, EvidenceRefs: wire.EvidenceRefs}, limits)
	if err != nil || wire.Schema != githublifecycle.TargetContainmentProofSchemaV1 || !bytes.Equal(value.CanonicalJSON(), data) {
		return githublifecycle.TargetContainmentProofV1{}, errors.Join(errors.New("containment record fails strict recovery"), err)
	}
	return value, nil
}

func recoverActor(wire recoveryActorWire) (githublifecycle.ActingIdentity, error) {
	if wire.Kind == githublifecycle.ActingKindUser {
		return githublifecycle.NewUserIdentity(wire.Subject)
	}
	if wire.Kind == githublifecycle.ActingKindAppInstallation {
		return githublifecycle.NewAppInstallationIdentity(wire.Subject, wire.InstallationID)
	}
	return githublifecycle.ActingIdentity{}, errors.New("unsupported acting identity")
}
func recoverSHA(value string) (githublifecycle.GitSHA, error) {
	return githublifecycle.NewGitSHA(value)
}
func recoverSHAs(values []string) ([]githublifecycle.GitSHA, error) {
	result := make([]githublifecycle.GitSHA, len(values))
	for index, value := range values {
		parsed, err := recoverSHA(value)
		if err != nil {
			return nil, err
		}
		result[index] = parsed
	}
	return result, nil
}

func (c *Controller) recoverAppliedRecords(attempt *attemptStore, sealed githublifecycle.SealedMergeAuthorizationV1) (
	githublifecycle.MergeResult, githublifecycle.PostMergeObservation, bool, bool, error,
) {
	var result githublifecycle.MergeResult
	var post githublifecycle.PostMergeObservation
	resultData, resultFound, err := attempt.read("merge-result.json")
	if err != nil {
		return result, post, false, false, err
	}
	postData, postFound, err := attempt.read("post-merge.json")
	if err != nil {
		return result, post, false, false, err
	}
	if postFound && !resultFound {
		return result, post, false, false, errors.New("post-merge proof exists without its merge result")
	}
	if !resultFound {
		return result, post, false, false, nil
	}
	result, err = parseMergeResult(resultData, sealed, c.contracts)
	if err != nil {
		return result, post, false, false, err
	}
	if postFound {
		post, err = parsePostMerge(postData, sealed, result, c.contracts)
		if err != nil {
			return result, post, true, false, err
		}
	}
	return result, post, true, postFound, nil
}

package githubmergeprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/mergelifecycle"
)

const (
	commitResponseEvidenceKind = "github-commit-response-body"
	commitObjectEvidenceKind   = "github-commit-object-response-body"
)

type commitIdentityRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date"`
}

type createCommitRequest struct {
	Message   string                `json:"message"`
	Tree      string                `json:"tree"`
	Parents   []string              `json:"parents"`
	Author    commitIdentityRequest `json:"author"`
	Committer commitIdentityRequest `json:"committer"`
}

type gitCommitResponse struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Tree    struct {
		SHA string `json:"sha"`
	} `json:"tree"`
	Parents []struct {
		SHA string `json:"sha"`
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

func (p *Provider) PrepareResultCommit(ctx context.Context, recipe githublifecycle.MergeCommitRecipeV1) (out mergelifecycle.CommitPreparation, returnedErr error) {
	meter, err := p.startMeter(ctx)
	if err != nil {
		return out, err
	}
	defer func() { out.Accounting = p.finishMeter(meter) }()
	input := recipe.Input()
	if err := validateRecipeForProvider(recipe); err != nil {
		return out, err
	}
	if !p.reserveCommitMutation(recipe.SHA256()) {
		return p.reconcileResultCommit(ctx, meter, recipe)
	}
	body, err := encodeCreateCommit(recipe)
	if err != nil {
		return out, err
	}
	tracker := &submissionTracker{}
	response, requestErr := p.requestJSON(ctx, meter, mergelifecycle.ProviderCallCommitSubmissionV1, p.mutationClient(tracker), tracker, http.MethodPost,
		repoPath(input.Repository)+"/git/commits", body)
	if requestErr != nil {
		return out, requestErr
	}
	if response.Status != http.StatusCreated {
		if response.Status >= 400 && response.Status <= 499 {
			return out, errors.New("GitHub rejected deterministic commit preparation")
		}
		return out, errors.New("deterministic commit preparation response is ambiguous")
	}
	var created gitCommitResponse
	if err := json.Unmarshal(response.Body, &created); err != nil || created.SHA != recipe.ExpectedResultSHA().String() {
		return out, errors.New("commit creation response does not identify the exact expected object")
	}
	creationEvidence := evidence(evidenceURI("commit-create", response.RequestID), commitResponseEvidenceKind, response.Body)
	preparation, err := p.observePreparedCommit(ctx, meter, recipe)
	if err != nil {
		return out, err
	}
	preparation.EvidenceRefs = append(preparation.EvidenceRefs, creationEvidence)
	return preparation, nil
}

func (p *Provider) ReconcileResultCommit(ctx context.Context, recipe githublifecycle.MergeCommitRecipeV1) (out mergelifecycle.CommitPreparation, returnedErr error) {
	meter, err := p.startMeter(ctx)
	if err != nil {
		return out, err
	}
	defer func() { out.Accounting = p.finishMeter(meter) }()
	return p.reconcileResultCommit(ctx, meter, recipe)
}

func (p *Provider) reconcileResultCommit(ctx context.Context, meter *callMeter, recipe githublifecycle.MergeCommitRecipeV1) (mergelifecycle.CommitPreparation, error) {
	if err := validateRecipeForProvider(recipe); err != nil {
		return mergelifecycle.CommitPreparation{}, err
	}
	return p.observePreparedCommit(ctx, meter, recipe)
}

func validateRecipeForProvider(recipe githublifecycle.MergeCommitRecipeV1) error {
	input := recipe.Input()
	if recipe.SHA256() == "" || input.ObjectFormat != "sha1" || len(input.Parents) != 2 ||
		input.ExpectedResultSHA.String() == "" || input.Repository.String() == "" || input.TargetRef == "" || len(recipe.CommitBytes()) == 0 {
		return errors.New("provider requires a complete merge-only SHA-1 commit recipe")
	}
	return nil
}

func encodeCreateCommit(recipe githublifecycle.MergeCommitRecipeV1) ([]byte, error) {
	input := recipe.Input()
	authorDate, err := githubDate(input.AuthorUnix, input.Author.Timezone)
	if err != nil {
		return nil, err
	}
	committerDate, err := githubDate(input.CommitterUnix, input.Committer.Timezone)
	if err != nil {
		return nil, err
	}
	parents := make([]string, len(input.Parents))
	for index := range input.Parents {
		parents[index] = input.Parents[index].String()
	}
	return json.Marshal(createCommitRequest{
		Message: input.Message, Tree: input.ExpectedResultTree.String(), Parents: parents,
		Author:    commitIdentityRequest{Name: input.Author.Name, Email: input.Author.Email, Date: authorDate},
		Committer: commitIdentityRequest{Name: input.Committer.Name, Email: input.Committer.Email, Date: committerDate},
	})
}

func githubDate(unix int64, timezone string) (string, error) {
	offset, err := timezoneOffset(timezone)
	if err != nil || unix <= 0 {
		return "", errors.New("commit timestamp or timezone is invalid")
	}
	return time.Unix(unix, 0).In(time.FixedZone("", offset)).Format(time.RFC3339), nil
}

func timezoneOffset(value string) (int, error) {
	if len(value) != 5 || value[0] != '+' && value[0] != '-' {
		return 0, errors.New("timezone must be a numeric four-digit offset")
	}
	hour, hourErr := strconv.Atoi(value[1:3])
	minute, minuteErr := strconv.Atoi(value[3:5])
	if hourErr != nil || minuteErr != nil || hour > 23 || minute > 59 {
		return 0, errors.New("timezone offset is invalid")
	}
	offset := hour*3600 + minute*60
	if value[0] == '-' {
		offset = -offset
	}
	return offset, nil
}

func (p *Provider) observePreparedCommit(ctx context.Context, meter *callMeter, recipe githublifecycle.MergeCommitRecipeV1) (mergelifecycle.CommitPreparation, error) {
	input := recipe.Input()
	response, err := p.readJSONOnce(ctx, meter, mergelifecycle.ProviderCallPreSubmitV1, http.MethodGet,
		repoPath(input.Repository)+"/git/commits/"+escapedSegment(input.ExpectedResultSHA.String()), nil)
	if err != nil {
		return mergelifecycle.CommitPreparation{}, err
	}
	remote, err := validateRemoteCommit(response.Body, recipe)
	if err != nil {
		return mergelifecycle.CommitPreparation{}, err
	}
	_ = remote
	objectEvidence := evidence(evidenceURI("commit-object", response.RequestID), commitObjectEvidenceKind, response.Body)
	parents := make([]string, len(input.Parents))
	for index := range input.Parents {
		parents[index] = input.Parents[index].String()
	}
	observation := mergelifecycle.CommitPreparationObservationV1{
		Schema: "merge-commit-preparation-observation-v1", Repository: input.Repository.String(),
		ResultSHA: input.ExpectedResultSHA.String(), ResultTree: input.ExpectedResultTree.String(), Parents: parents,
		Message: input.Message, Author: input.Author, Committer: input.Committer, AuthorUnix: input.AuthorUnix,
		CommitterUnix: input.CommitterUnix, ObjectFormat: input.ObjectFormat, ObjectBytes: recipe.CommitBytes(),
		ObjectBytesSHA256: digest(recipe.CommitBytes()), RecipeSHA256: recipe.SHA256(), EvidenceRefs: []ledger.EvidenceRef{objectEvidence},
	}
	return mergelifecycle.CommitPreparation{
		Schema: "merge-commit-preparation-v1", RecipeSHA256: recipe.SHA256(), ResultSHA: input.ExpectedResultSHA.String(),
		Observation: observation, EvidenceRefs: []ledger.EvidenceRef{objectEvidence},
	}, nil
}

func validateRemoteCommit(body []byte, recipe githublifecycle.MergeCommitRecipeV1) (gitCommitResponse, error) {
	var remote gitCommitResponse
	if err := json.Unmarshal(body, &remote); err != nil {
		return remote, errors.New("GitHub commit object response schema is invalid")
	}
	input := recipe.Input()
	if remote.SHA != input.ExpectedResultSHA.String() || remote.Tree.SHA != input.ExpectedResultTree.String() ||
		remote.Message != input.Message || remote.Author.Name != input.Author.Name || remote.Author.Email != input.Author.Email ||
		remote.Committer.Name != input.Committer.Name || remote.Committer.Email != input.Committer.Email || len(remote.Parents) != len(input.Parents) {
		return remote, errors.New("GitHub commit object fields do not match the deterministic recipe")
	}
	for index := range input.Parents {
		if remote.Parents[index].SHA != input.Parents[index].String() {
			return remote, errors.New("GitHub commit object parent order does not match the deterministic recipe")
		}
	}
	if err := validateRemoteCommitTime(remote.Author.Date, input.AuthorUnix, input.Author.Timezone); err != nil {
		return remote, err
	}
	if err := validateRemoteCommitTime(remote.Committer.Date, input.CommitterUnix, input.Committer.Timezone); err != nil {
		return remote, err
	}
	return remote, nil
}

func validateRemoteCommitTime(value string, unix int64, timezone string) error {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.Unix() != unix {
		return errors.New("GitHub commit timestamp does not match the deterministic recipe")
	}
	_, observedOffset := parsed.Zone()
	wantOffset, err := timezoneOffset(timezone)
	if err != nil || observedOffset != wantOffset {
		return errors.New("GitHub commit timezone does not match the deterministic recipe")
	}
	return nil
}

package githubmergeprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/mergelifecycle"
)

const authorizationQueryV1 = "query Authorization($owner:String!,$name:String!,$number:Int!){viewer{id} repository(owner:$owner,name:$name){id databaseId pullRequest(number:$number){id databaseId number state isDraft merged mergedAt baseRefName baseRefOid baseRepository{id} headRefName headRefOid headRepository{id}}}}"

type authorizationQueryRequest struct {
	Query     string                      `json:"query"`
	Variables authorizationQueryVariables `json:"variables"`
}

type authorizationQueryVariables struct {
	Name   string `json:"name"`
	Number int64  `json:"number"`
	Owner  string `json:"owner"`
}

type authorizationQueryResponse struct {
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
		} `json:"repository"`
	} `json:"data"`
	Errors []json.RawMessage `json:"errors,omitempty"`
}

type observedPR struct {
	result   httpResult
	response authorizationQueryResponse
	bodyRef  ledger.EvidenceRef
}

func (p *Provider) ObserveAuthorization(ctx context.Context, phase mergelifecycle.ObservationPhase, authority githublifecycle.Authority) (out mergelifecycle.AuthorizationObservation, returnedErr error) {
	meter := p.startMeter(ctx)
	defer func() { out.Accounting = p.finishMeter(meter) }()
	started := p.now().UnixNano()
	if phase != mergelifecycle.ObservationInitial && phase != mergelifecycle.ObservationFinal {
		return out, errors.New("unsupported authorization observation phase")
	}
	if err := p.validateAuthority(authority); err != nil {
		return out, err
	}
	prIdentity, _ := authority.PullRequest()
	remotePR, err := p.readAuthorizationSnapshot(ctx, meter, authority, prIdentity)
	if err != nil {
		return out, err
	}
	reviews, reviewsClosure, err := p.readReviews(ctx, meter, authority, prIdentity)
	if err != nil {
		return out, err
	}
	checkRuns, checkRunsClosure, err := p.readCheckRuns(ctx, meter, authority)
	if err != nil {
		return out, err
	}
	statuses, statusesClosure, err := p.readCommitStatuses(ctx, meter, authority)
	if err != nil {
		return out, err
	}
	checks := append(checkRuns, statuses...)
	prSnapshot, prEvidence, err := p.makePullRequestSnapshot(authority, prIdentity, remotePR, reviews, reviewsClosure)
	if err != nil {
		return out, err
	}
	evidenceRefs := append([]ledger.EvidenceRef(nil), prEvidence...)
	evidenceRefs = append(evidenceRefs, reviewsClosure.Input().EvidenceRefs...)
	evidenceRefs = append(evidenceRefs, checkRunsClosure.Input().EvidenceRefs...)
	evidenceRefs = append(evidenceRefs, statusesClosure.Input().EvidenceRefs...)
	completed := p.now().UnixNano()
	pages := len(reviewsClosure.Input().Pages) + len(checkRunsClosure.Input().Pages) + len(statusesClosure.Input().Pages)
	items := len(reviews) + len(checks)
	closureBytes := int64(len(reviewsClosure.CanonicalJSON()) + len(checkRunsClosure.CanonicalJSON()) + len(statusesClosure.CanonicalJSON()))
	counters := githublifecycle.AuthorizationCountersV1{}
	if phase == mergelifecycle.ObservationInitial {
		counters.AdmissionHTTPCalls = meter.calls
		counters.AdmissionObservedChecks = len(checks)
		counters.AdmissionObservedReviews = len(reviews)
		counters.AdmissionPaginationSources = p.limits.RequiredPaginationSources
		counters.AdmissionPaginationPages = pages
		counters.AdmissionPaginationItems = items
		counters.AdmissionPaginationClosureBytes = closureBytes
		counters.PreSubmitHTTPCalls = meter.calls
		counters.TotalHTTPCalls = meter.calls
	} else {
		counters.FinalRevalidationHTTPCalls = meter.calls
		counters.FinalObservedChecks = len(checks)
		counters.FinalObservedReviews = len(reviews)
		counters.FinalPaginationSources = p.limits.RequiredPaginationSources
		counters.FinalPaginationPages = pages
		counters.FinalPaginationItems = items
		counters.FinalPaginationClosureBytes = closureBytes
		counters.PreSubmitHTTPCalls = meter.calls
		counters.TotalHTTPCalls = meter.calls
	}
	counters.CumulativeRequestBytes = meter.accounting.RequestBytes
	counters.CumulativeResponseHeaderBytes = meter.accounting.HeaderBytes
	counters.CumulativeCompressedResponseBytes = meter.accounting.CompressedResponseBytes
	counters.CumulativeDecompressedResponseBytes = meter.accounting.DecompressedResponseBytes
	counters.CumulativeActiveProviderCallNanos = meter.accounting.ActiveNanos
	counters.ControllerInvocationNanos = completed - started
	out = mergelifecycle.AuthorizationObservation{
		PullRequest: prSnapshot, Checks: checks, CheckRunsClosure: checkRunsClosure, CommitStatusClosure: statusesClosure,
		EvidenceRefs: evidenceRefs, StartedUnixNano: started, CompletedUnixNano: completed, Counters: counters,
	}
	if err := githublifecycle.EvaluateMergePolicyV1(authority, prSnapshot, checks, checkRunsClosure, statusesClosure, p.limits); err != nil {
		return out, err
	}
	return out, nil
}

func (p *Provider) readAuthorizationSnapshot(ctx context.Context, meter *callMeter, authority githublifecycle.Authority, pr githublifecycle.PullRequestIdentity) (observedPR, error) {
	repository := authority.Repository()
	body, err := json.Marshal(authorizationQueryRequest{
		Query:     authorizationQueryV1,
		Variables: authorizationQueryVariables{Name: repository.Name(), Number: pr.Number(), Owner: repository.Owner()},
	})
	if err != nil {
		return observedPR{}, errors.New("authorization query encoding failed")
	}
	result, err := p.readJSON(ctx, meter, http.MethodPost, githublifecycle.GitHubGraphQLPathV1, body)
	if err != nil {
		return observedPR{}, err
	}
	var response authorizationQueryResponse
	if err := json.Unmarshal(result.Body, &response); err != nil || len(response.Errors) != 0 || response.Data.Repository == nil || response.Data.Repository.PullRequest == nil {
		return observedPR{}, errors.New("authorization query returned invalid or incomplete remote evidence")
	}
	if response.Data.Viewer.ID != authority.Actor().Subject() {
		return observedPR{}, errors.New("authenticated GitHub principal does not match authority")
	}
	return observedPR{result: result, response: response,
		bodyRef: evidence(evidenceURI("pull-request-body", result.RequestID), githublifecycle.GitHubPullRequestResponseEvidenceKindV1, result.Body)}, nil
}

func (p *Provider) makePullRequestSnapshot(authority githublifecycle.Authority, expected githublifecycle.PullRequestIdentity, observed observedPR, reviews []githublifecycle.Review, closure githublifecycle.PaginationClosureV1) (githublifecycle.AuthoritativePullRequestSnapshotV1, []ledger.EvidenceRef, error) {
	repository := observed.response.Data.Repository
	pr := repository.PullRequest
	binding := authority.ReadyBinding().RepositoryBinding()
	bindingInput := binding.Input()
	if repository.ID != bindingInput.GitHubRepositoryNodeID || repository.DatabaseID != bindingInput.GitHubRepositoryDatabaseID ||
		pr.ID != expected.NodeID() || pr.Number != expected.Number() || pr.BaseRepository == nil || pr.HeadRepository == nil {
		return githublifecycle.AuthoritativePullRequestSnapshotV1{}, nil, errors.New("stable repository or pull request identity changed")
	}
	baseOID, err := githublifecycle.NewGitSHA(pr.BaseRefOID)
	if err != nil {
		return githublifecycle.AuthoritativePullRequestSnapshotV1{}, nil, errors.New("pull request base OID is invalid")
	}
	headOID, err := githublifecycle.NewGitSHA(pr.HeadRefOID)
	if err != nil {
		return githublifecycle.AuthoritativePullRequestSnapshotV1{}, nil, errors.New("pull request head OID is invalid")
	}
	state := githublifecycle.PullRequestState(strings.ToLower(pr.State))
	var mergedAt *int64
	if pr.MergedAt != nil {
		parsed, parseErr := time.Parse(time.RFC3339Nano, *pr.MergedAt)
		if parseErr != nil {
			return githublifecycle.AuthoritativePullRequestSnapshotV1{}, nil, errors.New("pull request merged-at value is invalid")
		}
		value := parsed.UnixNano()
		mergedAt = &value
	}
	snapshot, err := githublifecycle.NewSnapshotIdentity("github", observed.result.RequestID, observed.result.ObservedNano)
	if err != nil {
		return githublifecycle.AuthoritativePullRequestSnapshotV1{}, nil, err
	}
	input := githublifecycle.AuthoritativePullRequestSnapshotV1Input{
		Snapshot: snapshot, ResponseBodySHA256: digest(observed.result.Body), APIVersion: apiVersion, RepositoryBinding: binding,
		PullRequest: expected, PullRequestDatabaseID: pr.DatabaseID, BaseRepositoryNodeID: pr.BaseRepository.ID,
		BaseRef: "refs/heads/" + pr.BaseRefName, BaseOID: baseOID, HeadRepositoryNodeID: pr.HeadRepository.ID,
		HeadRef: "refs/heads/" + pr.HeadRefName, HeadOID: headOID, State: &state, IsDraft: pr.IsDraft, Merged: pr.Merged,
		MergedAtUnixNano: mergedAt, Actor: authority.Actor(), Reviews: reviews, ReviewsClosure: closure,
	}
	envelope, err := githublifecycle.NewPullRequestEnvelopeEvidenceV1(evidenceURI("pull-request-envelope", observed.result.RequestID), input, p.limits)
	if err != nil {
		return githublifecycle.AuthoritativePullRequestSnapshotV1{}, nil, err
	}
	input.EvidenceRefs = []ledger.EvidenceRef{observed.bodyRef, envelope}
	value, err := githublifecycle.NewAuthoritativePullRequestSnapshotV1(input, p.limits)
	return value, input.EvidenceRefs, err
}

type paginationCollector func([]byte, ledger.EvidenceRef) ([]githublifecycle.CanonicalPaginationItemV1, error)

func (p *Provider) collectRESTPages(ctx context.Context, meter *callMeter, scope githublifecycle.PaginationQueryScopeV1, collect paginationCollector) (githublifecycle.PaginationClosureV1, error) {
	query, err := githublifecycle.DerivePaginationQueryV1(scope, p.limits)
	if err != nil {
		return githublifecycle.PaginationClosureV1{}, err
	}
	pages := make([]githublifecycle.PaginationPageV1, 0, p.limits.MaxPaginationPages)
	evidenceRefs := make([]ledger.EvidenceRef, 0, p.limits.MaxPaginationPages*2)
	for pageNumber := 1; pageNumber <= p.limits.MaxPaginationPages; pageNumber++ {
		parameters := url.Values{"page": {strconv.Itoa(pageNumber)}, "per_page": {strconv.Itoa(query.PerPage)}}
		for key, value := range query.Variables {
			parameters.Set(key, value)
		}
		path := query.PathOrDocumentSHA256 + "?" + parameters.Encode()
		response, err := p.readJSON(ctx, meter, http.MethodGet, path, nil)
		if err != nil {
			return githublifecycle.PaginationClosureV1{}, err
		}
		bodyEvidence := evidence(evidenceURI(string(query.Source)+"-body", response.RequestID), githublifecycle.GitHubPaginationBodyEvidenceKindV1, response.Body)
		items, err := collect(response.Body, bodyEvidence)
		if err != nil {
			return githublifecycle.PaginationClosureV1{}, err
		}
		snapshot, err := githublifecycle.NewSnapshotIdentity("github", response.RequestID, response.ObservedNano)
		if err != nil {
			return githublifecycle.PaginationClosureV1{}, err
		}
		pageInput := githublifecycle.PaginationPageV1Input{
			Query: query, Ordinal: pageNumber - 1, RequestedPage: pageNumber, Response: snapshot,
			RawBodySHA256: digest(response.Body), ResponseEvidence: bodyEvidence, Items: items,
			RESTLinkHeader: response.Link, RESTLinkObserved: response.LinkObserved,
		}
		pageInput.EnvelopeEvidence, err = githublifecycle.NewPaginationEnvelopeEvidenceV1(evidenceURI(string(query.Source)+"-envelope", response.RequestID), pageInput, p.limits)
		if err != nil {
			return githublifecycle.PaginationClosureV1{}, err
		}
		page, err := githublifecycle.NewPaginationPageV1(pageInput, p.limits)
		if err != nil {
			return githublifecycle.PaginationClosureV1{}, err
		}
		pages = append(pages, page)
		evidenceRefs = append(evidenceRefs, bodyEvidence, pageInput.EnvelopeEvidence)
		hasNext, nextPage, err := nextRESTPage(response.Link)
		if err != nil {
			return githublifecycle.PaginationClosureV1{}, err
		}
		if !hasNext {
			return githublifecycle.NewPaginationClosureV1(githublifecycle.PaginationClosureV1Input{Query: query, Pages: pages, EvidenceRefs: evidenceRefs}, p.limits)
		}
		if nextPage != pageNumber+1 || pageNumber == p.limits.MaxPaginationPages {
			return githublifecycle.PaginationClosureV1{}, errors.New("pagination is skipped, repeated, or exceeds the page limit")
		}
	}
	return githublifecycle.PaginationClosureV1{}, errors.New("pagination did not close")
}

func nextRESTPage(header string) (bool, int, error) {
	if header == "" {
		return false, 0, nil
	}
	found, page := false, 0
	for _, part := range strings.Split(header, ",") {
		segments := strings.Split(strings.TrimSpace(part), ";")
		if len(segments) < 2 || len(segments[0]) < 3 || segments[0][0] != '<' || segments[0][len(segments[0])-1] != '>' {
			return false, 0, errors.New("malformed REST Link pagination header")
		}
		relationNext := false
		for _, parameter := range segments[1:] {
			parameter = strings.TrimSpace(parameter)
			if parameter == `rel="next"` || strings.HasPrefix(parameter, `rel="`) && strings.Contains(parameter, "next") {
				relationNext = true
			}
		}
		if !relationNext {
			continue
		}
		if found {
			return false, 0, errors.New("duplicate next pagination relation")
		}
		parsed, err := url.Parse(segments[0][1 : len(segments[0])-1])
		if err != nil || parsed.Scheme != "https" || parsed.Host != apiHost {
			return false, 0, errors.New("pagination next relation changes the sealed origin")
		}
		page, err = strconv.Atoi(parsed.Query().Get("page"))
		if err != nil || page <= 0 {
			return false, 0, errors.New("pagination next relation has no valid page")
		}
		found = true
	}
	return found, page, nil
}

type reviewResponse struct {
	ID       int64  `json:"id"`
	NodeID   string `json:"node_id"`
	State    string `json:"state"`
	CommitID string `json:"commit_id"`
	User     *struct {
		ID     int64  `json:"id"`
		NodeID string `json:"node_id"`
	} `json:"user"`
}

func (p *Provider) readReviews(ctx context.Context, meter *callMeter, authority githublifecycle.Authority, pr githublifecycle.PullRequestIdentity) ([]githublifecycle.Review, githublifecycle.PaginationClosureV1, error) {
	var reviews []githublifecycle.Review
	closure, err := p.collectRESTPages(ctx, meter, githublifecycle.PaginationQueryScopeV1{
		Source: githublifecycle.PaginationReviews, Repository: authority.Repository(),
		RepositoryNodeID: authority.ReadyBinding().RepositoryBinding().Input().GitHubRepositoryNodeID, PullRequest: &pr, HeadSHA: authority.HeadSHA(),
	}, func(body []byte, _ ledger.EvidenceRef) ([]githublifecycle.CanonicalPaginationItemV1, error) {
		var page []reviewResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, errors.New("review page schema is invalid")
		}
		items := make([]githublifecycle.CanonicalPaginationItemV1, len(page))
		for index, remote := range page {
			if remote.User == nil {
				return nil, errors.New("review lacks a stable reviewer identity")
			}
			commit, err := githublifecycle.NewGitSHA(remote.CommitID)
			if err != nil {
				return nil, errors.New("review commit identity is invalid")
			}
			review := githublifecycle.Review{NodeID: remote.NodeID, DatabaseID: remote.ID,
				Reviewer: githublifecycle.StableIdentityV1{DatabaseID: remote.User.ID, NodeID: remote.User.NodeID},
				State:    githublifecycle.ReviewState(strings.ToLower(remote.State)), CommitSHA: commit}
			wire := struct {
				NodeID     string                           `json:"node_id"`
				DatabaseID int64                            `json:"database_id"`
				Reviewer   githublifecycle.StableIdentityV1 `json:"reviewer"`
				State      githublifecycle.ReviewState      `json:"state"`
				CommitSHA  string                           `json:"commit_sha"`
			}{review.NodeID, review.DatabaseID, review.Reviewer, review.State, review.CommitSHA.String()}
			raw, _ := json.Marshal(wire)
			items[index] = githublifecycle.CanonicalPaginationItemV1{Key: fmt.Sprintf("%d/%s", remote.ID, remote.NodeID), SHA256: digest(raw)}
			reviews = append(reviews, review)
		}
		return items, nil
	})
	return reviews, closure, err
}

type checkRunPage struct {
	CheckRuns []struct {
		ID         int64   `json:"id"`
		NodeID     string  `json:"node_id"`
		Name       string  `json:"name"`
		Status     string  `json:"status"`
		Conclusion *string `json:"conclusion"`
		HeadSHA    string  `json:"head_sha"`
		App        *struct {
			ID     int64  `json:"id"`
			NodeID string `json:"node_id"`
			Owner  *struct {
				ID     int64  `json:"id"`
				NodeID string `json:"node_id"`
			} `json:"owner"`
		} `json:"app"`
	} `json:"check_runs"`
}

func (p *Provider) readCheckRuns(ctx context.Context, meter *callMeter, authority githublifecycle.Authority) ([]githublifecycle.Check, githublifecycle.PaginationClosureV1, error) {
	var checks []githublifecycle.Check
	closure, err := p.collectRESTPages(ctx, meter, githublifecycle.PaginationQueryScopeV1{
		Source: githublifecycle.PaginationCheckRuns, Repository: authority.Repository(),
		RepositoryNodeID: authority.ReadyBinding().RepositoryBinding().Input().GitHubRepositoryNodeID, HeadSHA: authority.HeadSHA(),
	}, func(body []byte, bodyEvidence ledger.EvidenceRef) ([]githublifecycle.CanonicalPaginationItemV1, error) {
		var page checkRunPage
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, errors.New("check-run page schema is invalid")
		}
		items := make([]githublifecycle.CanonicalPaginationItemV1, len(page.CheckRuns))
		for index, remote := range page.CheckRuns {
			if remote.App == nil || remote.App.Owner == nil {
				return nil, errors.New("check run lacks stable producer and app identities")
			}
			head, err := githublifecycle.NewGitSHA(remote.HeadSHA)
			if err != nil {
				return nil, errors.New("check run head identity is invalid")
			}
			app := githublifecycle.StableIdentityV1{DatabaseID: remote.App.ID, NodeID: remote.App.NodeID}
			check := githublifecycle.Check{NodeID: remote.NodeID, Name: remote.Name,
				Identity: githublifecycle.TrustedCheckIdentityV1{Context: remote.Name, Source: githublifecycle.CheckSourceCheckRun,
					Producer: githublifecycle.StableIdentityV1{DatabaseID: remote.App.Owner.ID, NodeID: remote.App.Owner.NodeID}, App: &app},
				Status: githublifecycle.CheckStatus(strings.ToLower(remote.Status)), HeadSHA: head, EvidenceRefs: []ledger.EvidenceRef{bodyEvidence}}
			if remote.Conclusion != nil {
				check.Conclusion = githublifecycle.CheckConclusion(strings.ToLower(*remote.Conclusion))
			}
			raw, _ := json.Marshal(checkItemWire(check))
			items[index] = githublifecycle.CanonicalPaginationItemV1{Key: check.NodeID, SHA256: digest(raw)}
			checks = append(checks, check)
		}
		return items, nil
	})
	return checks, closure, err
}

type commitStatusResponse struct {
	ID      int64  `json:"id"`
	NodeID  string `json:"node_id"`
	Context string `json:"context"`
	State   string `json:"state"`
	SHA     string `json:"sha"`
	Creator *struct {
		ID     int64  `json:"id"`
		NodeID string `json:"node_id"`
	} `json:"creator"`
}

func (p *Provider) readCommitStatuses(ctx context.Context, meter *callMeter, authority githublifecycle.Authority) ([]githublifecycle.Check, githublifecycle.PaginationClosureV1, error) {
	var checks []githublifecycle.Check
	closure, err := p.collectRESTPages(ctx, meter, githublifecycle.PaginationQueryScopeV1{
		Source: githublifecycle.PaginationCommitStatuses, Repository: authority.Repository(),
		RepositoryNodeID: authority.ReadyBinding().RepositoryBinding().Input().GitHubRepositoryNodeID, HeadSHA: authority.HeadSHA(),
	}, func(body []byte, bodyEvidence ledger.EvidenceRef) ([]githublifecycle.CanonicalPaginationItemV1, error) {
		var page []commitStatusResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, errors.New("commit-status page schema is invalid")
		}
		items := make([]githublifecycle.CanonicalPaginationItemV1, len(page))
		for index, remote := range page {
			if remote.Creator == nil {
				return nil, errors.New("commit status lacks a stable producer identity")
			}
			head, err := githublifecycle.NewGitSHA(remote.SHA)
			if err != nil {
				return nil, errors.New("commit status head identity is invalid")
			}
			check := githublifecycle.Check{NodeID: remote.NodeID, Name: remote.Context,
				Identity: githublifecycle.TrustedCheckIdentityV1{Context: remote.Context, Source: githublifecycle.CheckSourceCommitStatus,
					Producer: githublifecycle.StableIdentityV1{DatabaseID: remote.Creator.ID, NodeID: remote.Creator.NodeID}},
				HeadSHA: head, EvidenceRefs: []ledger.EvidenceRef{bodyEvidence}}
			switch strings.ToLower(remote.State) {
			case "pending":
				check.Status = githublifecycle.CheckQueued
			case "success":
				check.Status, check.Conclusion = githublifecycle.CheckCompleted, githublifecycle.ConclusionSuccess
			case "failure", "error":
				check.Status, check.Conclusion = githublifecycle.CheckCompleted, githublifecycle.ConclusionFailure
			default:
				return nil, errors.New("commit status state is unsupported")
			}
			raw, _ := json.Marshal(checkItemWire(check))
			items[index] = githublifecycle.CanonicalPaginationItemV1{Key: check.NodeID, SHA256: digest(raw)}
			checks = append(checks, check)
		}
		return items, nil
	})
	return checks, closure, err
}

func checkItemWire(check githublifecycle.Check) any {
	return struct {
		NodeID       string                                 `json:"node_id"`
		Name         string                                 `json:"name"`
		Identity     githublifecycle.TrustedCheckIdentityV1 `json:"identity"`
		Status       githublifecycle.CheckStatus            `json:"status"`
		Conclusion   githublifecycle.CheckConclusion        `json:"conclusion,omitempty"`
		HeadSHA      string                                 `json:"head_sha"`
		EvidenceRefs []ledger.EvidenceRef                   `json:"evidence_refs,omitempty"`
	}{check.NodeID, check.Name, check.Identity, check.Status, check.Conclusion, check.HeadSHA.String(), check.EvidenceRefs}
}

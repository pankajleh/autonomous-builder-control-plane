package prlifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
)

const (
	githubAPIOrigin = "https://api.github.com"
	githubAPIVer    = "2026-03-10"
	githubAccept    = "application/vnd.github+json"
)

type GitHubAdapter struct {
	client *http.Client
	origin string
	limits githublifecycle.Limits
	now    func() time.Time
}

// NewGitHubAdapter seals the authenticated transport behind the exact
// production GitHub origin and production limits. Authorization remains the
// transport's concern and is never accepted by an adapter method.
func NewGitHubAdapter(transport http.RoundTripper) (*GitHubAdapter, error) {
	if transport == nil {
		return nil, errors.New("authenticated GitHub transport is required")
	}
	if base, ok := transport.(*http.Transport); ok {
		base = base.Clone()
		base.MaxResponseHeaderBytes = MaxResponseHeaderBytes
		transport = base
	}
	return newGitHubAdapter(transport, githubAPIOrigin, githublifecycle.DefaultLimits(), time.Now)
}

// newGitHubAdapter is the sole test boundary for a local HTTP origin and a
// component-wise stricter lifecycle profile.
func newGitHubAdapter(transport http.RoundTripper, origin string, limits githublifecycle.Limits, now func() time.Time) (*GitHubAdapter, error) {
	if transport == nil || now == nil {
		return nil, errors.New("transport and clock are required")
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("test API origin is invalid")
	}
	if !limitsAllowed(limits) {
		return nil, errors.New(CodePolicyMismatch)
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &GitHubAdapter{client: client, origin: origin, limits: limits, now: now}, nil
}

func limitsAllowed(actual githublifecycle.Limits) bool {
	if actual.Validate() != nil {
		return false
	}
	p := githublifecycle.DefaultLimits()
	return actual.MaxPages <= p.MaxPages && actual.MaxItemsPerPage <= p.MaxItemsPerPage && actual.MaxTotalItems <= p.MaxTotalItems &&
		actual.MaxTextBytes <= p.MaxTextBytes && actual.MaxEvidenceRefs <= p.MaxEvidenceRefs && actual.MaxMetadataItems <= p.MaxMetadataItems &&
		actual.MaxParents <= p.MaxParents && actual.MaxLineageEntries <= p.MaxLineageEntries && actual.CallTimeout <= p.CallTimeout &&
		actual.MaxReadRetries <= p.MaxReadRetries && actual.MaxWriteRetries <= p.MaxWriteRetries && actual.MaxAmbiguousRetries == 0
}

func (a *GitHubAdapter) Limits() githublifecycle.Limits { return a.limits }

func (a *GitHubAdapter) principal(ctx context.Context) (RemotePrincipalObservation, error) {
	id, body, requestID, err := a.request(ctx, http.MethodGet, "/user", "/user", nil, nil, http.StatusOK, false)
	if err != nil {
		return RemotePrincipalObservation{}, err
	}
	var wire struct {
		ID     int64  `json:"id"`
		NodeID string `json:"node_id"`
		Login  string `json:"login"`
	}
	if err := providerJSON(body, &wire); err != nil || wire.ID <= 0 || !validRemoteText(wire.NodeID, a.limits.MaxTextBytes, false) || !validRemoteText(wire.Login, a.limits.MaxTextBytes, false) {
		return RemotePrincipalObservation{}, errors.New("invalid authenticated-principal response")
	}
	limitsSHA, _ := a.limits.SHA256()
	return RemotePrincipalObservation{id, wire.ID, wire.NodeID, wire.Login, requestID, a.now().UTC().UnixNano(), limitsSHA}, nil
}

func (a *GitHubAdapter) ref(ctx context.Context, repository githublifecycle.Repository, branch githublifecycle.Branch) (RemoteRefObservation, error) {
	escaped := "/repos/" + url.PathEscape(repository.Owner()) + "/" + url.PathEscape(repository.Name()) + "/git/ref/heads/" + url.PathEscape(branch.String())
	id, body, requestID, err := a.request(ctx, http.MethodGet, "/repos/{owner}/{repo}/git/ref/heads/{branch}", escaped, nil, nil, http.StatusOK, false)
	if err != nil {
		return RemoteRefObservation{}, err
	}
	var wire struct {
		Ref    string `json:"ref"`
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	if err := providerJSON(body, &wire); err != nil || wire.Ref != "refs/heads/"+branch.String() || wire.Object.Type != "commit" {
		return RemoteRefObservation{}, errors.New("invalid exact-ref response")
	}
	if _, err := githublifecycle.NewGitSHA(wire.Object.SHA); err != nil {
		return RemoteRefObservation{}, errors.New("invalid exact-ref Git SHA")
	}
	limitsSHA, _ := a.limits.SHA256()
	return RemoteRefObservation{id, wire.Ref, wire.Object.Type, wire.Object.SHA, requestID, a.now().UTC().UnixNano(), limitsSHA}, nil
}

type discoveryCandidate struct {
	Number int64  `json:"number"`
	NodeID string `json:"node_id"`
}

func (a *GitHubAdapter) discover(ctx context.Context, repository githublifecycle.Repository, base, head githublifecycle.Branch) ([]discoveryCandidate, []byte, error) {
	q := url.Values{}
	q.Set("base", base.String())
	q.Set("direction", "asc")
	q.Set("head", repository.Owner()+":"+head.String())
	q.Set("page", "1")
	q.Set("per_page", strconv.Itoa(a.limits.MaxItemsPerPage))
	q.Set("sort", "created")
	q.Set("state", "open")
	escaped := "/repos/" + url.PathEscape(repository.Owner()) + "/" + url.PathEscape(repository.Name()) + "/pulls"
	_, body, _, headers, err := a.requestHeaders(ctx, http.MethodGet, "/repos/{owner}/{repo}/pulls", escaped, q, nil, http.StatusOK, false)
	if err != nil {
		return nil, nil, err
	}
	link := headers.Get("Link")
	if len(link) > MaxLinkHeaderBytes || hasNextLink(link) {
		return nil, nil, &Error{Code: CodeDiscoveryTruncated, Cause: errors.New("filtered discovery has next-page or oversized Link metadata")}
	}
	var candidates []discoveryCandidate
	if err := providerJSON(body, &candidates); err != nil {
		return nil, nil, fmt.Errorf("decode discovery: %w", err)
	}
	if len(candidates) > a.limits.MaxItemsPerPage {
		return nil, nil, &Error{Code: CodeDiscoveryTruncated, Cause: errors.New("filtered discovery exceeds page limit")}
	}
	for i, item := range candidates {
		if item.Number <= 0 || !validRemoteText(item.NodeID, a.limits.MaxTextBytes, false) {
			return nil, nil, fmt.Errorf("invalid discovery candidate %d", i)
		}
	}
	return candidates, append([]byte(nil), body...), nil
}

func hasNextLink(link string) bool {
	for _, part := range strings.Split(link, ",") {
		if strings.Contains(part, `rel="next"`) || strings.Contains(part, "rel=next") {
			return true
		}
	}
	return false
}

func (a *GitHubAdapter) pullRequest(ctx context.Context, repository githublifecycle.Repository, number int64) (RemotePRObservation, error) {
	escaped := "/repos/" + url.PathEscape(repository.Owner()) + "/" + url.PathEscape(repository.Name()) + "/pulls/" + strconv.FormatInt(number, 10)
	id, body, requestID, err := a.request(ctx, http.MethodGet, "/repos/{owner}/{repo}/pulls/{number}", escaped, nil, nil, http.StatusOK, false)
	if err != nil {
		return RemotePRObservation{}, err
	}
	return a.decodePR(id, body, requestID, repository)
}

type prResponse struct {
	Number int64   `json:"number"`
	NodeID string  `json:"node_id"`
	State  string  `json:"state"`
	Merged bool    `json:"merged"`
	Title  string  `json:"title"`
	Body   *string `json:"body"`
	User   struct {
		ID     int64  `json:"id"`
		NodeID string `json:"node_id"`
		Login  string `json:"login"`
	} `json:"user"`
	Base struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			Name  string `json:"name"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repo"`
	} `json:"base"`
	Head struct {
		Ref   string `json:"ref"`
		SHA   string `json:"sha"`
		Label string `json:"label"`
		Repo  struct {
			Name  string `json:"name"`
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repo"`
	} `json:"head"`
}

func (a *GitHubAdapter) decodePR(id HTTPRequestIdentity, body []byte, requestID string, repository githublifecycle.Repository) (RemotePRObservation, error) {
	var wire prResponse
	if err := providerJSON(body, &wire); err != nil {
		return RemotePRObservation{}, fmt.Errorf("decode full pull request: %w", err)
	}
	bodyText := ""
	if wire.Body != nil {
		bodyText = *wire.Body
	}
	fields := []string{wire.NodeID, wire.State, wire.Title, bodyText, wire.User.NodeID, wire.User.Login, wire.Base.Ref, wire.Base.SHA, wire.Base.Repo.Name, wire.Base.Repo.Owner.Login, wire.Head.Ref, wire.Head.SHA, wire.Head.Label, wire.Head.Repo.Name, wire.Head.Repo.Owner.Login}
	for _, field := range fields {
		if !validRemoteText(field, a.limits.MaxTextBytes, true) {
			return RemotePRObservation{}, errors.New("pull-request response contains oversized or invalid text")
		}
	}
	if wire.Number <= 0 || wire.User.ID <= 0 || wire.NodeID == "" || wire.User.NodeID == "" || wire.User.Login == "" {
		return RemotePRObservation{}, errors.New("pull-request response lacks stable identity")
	}
	limitsSHA, _ := a.limits.SHA256()
	return RemotePRObservation{
		Request: id, Number: wire.Number, NodeID: wire.NodeID, RepositoryOwner: repository.Owner(), RepositoryName: repository.Name(),
		BaseRepository: wire.Base.Repo.Owner.Login + "/" + wire.Base.Repo.Name, HeadRepository: wire.Head.Repo.Owner.Login + "/" + wire.Head.Repo.Name,
		BaseRef: wire.Base.Ref, HeadRef: wire.Head.Ref, BaseSHA: wire.Base.SHA, HeadSHA: wire.Head.SHA, HeadLabel: wire.Head.Label,
		AuthorID: wire.User.ID, AuthorNodeID: wire.User.NodeID, AuthorLogin: wire.User.Login, State: wire.State, Merged: wire.Merged,
		Title: wire.Title, Body: bodyText, RequestID: requestID, ObservedUnixNano: a.now().UTC().UnixNano(), LimitsSHA256: limitsSHA,
	}, nil
}

func (a *GitHubAdapter) write(ctx context.Context, repository githublifecycle.Repository, prNumber int64, title, body string, base, head githublifecycle.Branch) (RemotePRObservation, error) {
	method, template, escaped, success := http.MethodPost, "/repos/{owner}/{repo}/pulls", "/repos/"+url.PathEscape(repository.Owner())+"/"+url.PathEscape(repository.Name())+"/pulls", http.StatusCreated
	var payload any = struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Head  string `json:"head"`
		Base  string `json:"base"`
	}{title, body, repository.Owner() + ":" + head.String(), base.String()}
	if prNumber > 0 {
		method, template, escaped, success = http.MethodPatch, "/repos/{owner}/{repo}/pulls/{number}", escaped+"/"+strconv.FormatInt(prNumber, 10), http.StatusOK
		payload = struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}{title, body}
	}
	b, err := json.Marshal(payload)
	if err != nil || len(b) > MaxRequestBytes {
		return RemotePRObservation{}, errors.New("canonical PR request exceeds request cap")
	}
	id, response, requestID, err := a.request(ctx, method, template, escaped, nil, b, success, true)
	if err != nil {
		return RemotePRObservation{}, err
	}
	observed, err := a.decodePR(id, response, requestID, repository)
	if err != nil {
		return RemotePRObservation{}, &Error{Submitted: true, Cause: err}
	}
	return observed, nil
}

func (a *GitHubAdapter) request(ctx context.Context, method, template, escaped string, query url.Values, body []byte, expected int, write bool) (HTTPRequestIdentity, []byte, string, error) {
	id, data, requestID, _, err := a.requestHeaders(ctx, method, template, escaped, query, body, expected, write)
	return id, data, requestID, err
}

func (a *GitHubAdapter) requestHeaders(ctx context.Context, method, template, escaped string, query url.Values, body []byte, expected int, write bool) (HTTPRequestIdentity, []byte, string, http.Header, error) {
	if ctx == nil {
		return HTTPRequestIdentity{}, nil, "", nil, errors.New("context is required")
	}
	queryString := ""
	if query != nil {
		queryString = query.Encode()
	}
	contentType := ""
	if body != nil {
		contentType = "application/json"
	}
	id := HTTPRequestIdentity{Method: method, PathTemplate: template, EscapedPath: escaped, CanonicalQuery: queryString, APIOrigin: a.origin, APIVersion: githubAPIVer, Accept: githubAccept, ContentType: contentType, ExpectedStatus: []int{expected}, BoundedResponseBytes: MaxResponseBytes}
	if body != nil {
		id.RequestBodySHA256 = digestBytes(body)
	}
	requestCtx, cancel := context.WithTimeout(ctx, a.limits.CallTimeout)
	defer cancel()
	endpoint := a.origin + escaped
	if queryString != "" {
		endpoint += "?" + queryString
	}
	req, err := http.NewRequestWithContext(requestCtx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return id, nil, "", nil, err
	}
	req.Header.Set("Accept", githubAccept)
	req.Header.Set("X-GitHub-Api-Version", githubAPIVer)
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	response, err := a.client.Do(req)
	if err != nil {
		if write {
			return id, nil, "", nil, &Error{Submitted: true, Cause: err}
		}
		return id, nil, "", nil, err
	}
	defer response.Body.Close()
	if err := boundHeaders(response.Header); err != nil {
		if write {
			return id, nil, "", nil, &Error{Submitted: true, Cause: err}
		}
		return id, nil, "", nil, err
	}
	requestID := response.Header.Get("X-GitHub-Request-Id")
	if len(requestID) > MaxRequestIDBytes || !validRemoteText(requestID, MaxRequestIDBytes, true) {
		requestID = ""
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil || len(data) > MaxResponseBytes {
		if err == nil {
			err = errors.New("GitHub response exceeds body cap")
		}
		if write {
			return id, nil, requestID, response.Header.Clone(), &Error{Submitted: true, Cause: err}
		}
		return id, nil, requestID, response.Header.Clone(), err
	}
	if response.StatusCode != expected {
		err = fmt.Errorf("unexpected GitHub status %d", response.StatusCode)
		if write {
			return id, nil, requestID, response.Header.Clone(), &Error{Submitted: true, Cause: err}
		}
		return id, nil, requestID, response.Header.Clone(), err
	}
	return id, data, requestID, response.Header.Clone(), nil
}

func strictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}

func providerJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("provider JSON has trailing data")
	}
	return nil
}

func boundHeaders(headers http.Header) error {
	total := 0
	for key, values := range headers {
		total += len(key)
		for _, value := range values {
			total += len(value)
		}
	}
	if total > MaxResponseHeaderBytes {
		return errors.New("GitHub response headers exceed cap")
	}
	return nil
}

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
	client                *http.Client
	origin                string
	limits                githublifecycle.Limits
	now                   func() time.Time
	writeConstructionHook func() error
	principalObserveHook  func(*RemotePrincipalObservation)
}

type neverSubmittedError struct{ cause error }

func (e *neverSubmittedError) Error() string { return "GitHub request was not submitted" }
func (e *neverSubmittedError) Unwrap() error { return e.cause }

// GitHubRequestAuthenticator applies credentials to a cloned outbound request.
// Network I/O remains sealed behind the controller-owned capped transport.
type GitHubRequestAuthenticator interface {
	AuthenticateGitHubRequest(*http.Request) error
}

type authenticatedGitHubTransport struct {
	base          *http.Transport
	authenticator GitHubRequestAuthenticator
}

func (t *authenticatedGitHubTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	method, endpoint, host := clone.Method, clone.URL.String(), clone.Host
	contentLength := clone.ContentLength
	originalHeaders := clone.Header.Clone()
	if err := t.authenticator.AuthenticateGitHubRequest(clone); err != nil {
		return nil, &neverSubmittedError{cause: errors.New("GitHub request authentication failed")}
	}
	if clone.Method != method || clone.URL.String() != endpoint || clone.Host != host || clone.ContentLength != contentLength || !headersDifferOnlyByAuthorization(originalHeaders, clone.Header) {
		return nil, &neverSubmittedError{cause: errors.New("GitHub authenticator modified sealed request identity")}
	}
	return t.base.RoundTrip(clone)
}

func headersDifferOnlyByAuthorization(before, after http.Header) bool {
	for key, values := range before {
		if strings.EqualFold(key, "Authorization") {
			continue
		}
		if !equalStrings(values, after.Values(key)) {
			return false
		}
	}
	for key, values := range after {
		if strings.EqualFold(key, "Authorization") {
			continue
		}
		if !equalStrings(values, before.Values(key)) {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// NewGitHubAdapter seals the authenticated transport behind the exact
// production GitHub origin and production limits. Authorization remains the
// transport's concern and is never accepted by an adapter method.
func NewGitHubAdapter(authenticator GitHubRequestAuthenticator) (*GitHubAdapter, error) {
	if authenticator == nil {
		return nil, errors.New("GitHub request authenticator is required")
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.MaxResponseHeaderBytes = MaxResponseHeaderBytes
	transport := &authenticatedGitHubTransport{base: base, authenticator: authenticator}
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
	if err := providerJSON(body, &wire); err != nil || wire.ID <= 0 || !validRemoteText(wire.NodeID, lifecycleRemoteTextLimit(a.limits), false) || !validRemoteText(wire.Login, lifecycleRemoteTextLimit(a.limits), false) {
		return RemotePrincipalObservation{}, errors.New("invalid authenticated-principal response")
	}
	limitsSHA, _ := a.limits.SHA256()
	observation := RemotePrincipalObservation{id, wire.ID, wire.NodeID, wire.Login, requestID, a.now().UTC().UnixNano(), limitsSHA}
	if a.principalObserveHook != nil {
		a.principalObserveHook(&observation)
	}
	return observation, nil
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
		if item.Number <= 0 || !validRemoteText(item.NodeID, lifecycleRemoteTextLimit(a.limits), false) {
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
		if !validRemoteText(field, lifecycleRemoteTextLimit(a.limits), true) {
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

func lifecycleRemoteTextLimit(limits githublifecycle.Limits) int {
	if limits.MaxTextBytes < maxLifecycleRemoteText {
		return limits.MaxTextBytes
	}
	return maxLifecycleRemoteText
}

type preparedGitHubWrite struct {
	request    *http.Request
	cancel     context.CancelFunc
	identity   HTTPRequestIdentity
	expected   int
	repository githublifecycle.Repository
}

func (a *GitHubAdapter) prepareWrite(ctx context.Context, repository githublifecycle.Repository, prNumber int64, title, body string, base, head githublifecycle.Branch) (*preparedGitHubWrite, error) {
	if a.writeConstructionHook != nil {
		if err := a.writeConstructionHook(); err != nil {
			return nil, errors.New("canonical PR request construction failed")
		}
	}
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
		return nil, errors.New("canonical PR request exceeds request cap")
	}
	id, request, cancel, err := a.buildRequest(ctx, method, template, escaped, nil, b, success)
	if err != nil {
		return nil, errors.New("canonical PR HTTP request construction failed")
	}
	return &preparedGitHubWrite{request: request, cancel: cancel, identity: id, expected: success, repository: repository}, nil
}

func (a *GitHubAdapter) executeWrite(write *preparedGitHubWrite) (RemotePRObservation, error) {
	if write == nil || write.request == nil || write.cancel == nil {
		return RemotePRObservation{}, &neverSubmittedError{cause: errors.New("prepared GitHub write is incomplete")}
	}
	defer write.cancel()
	if err := write.request.Context().Err(); err != nil {
		return RemotePRObservation{}, &neverSubmittedError{cause: errors.New("prepared GitHub write expired before submission")}
	}
	response, requestID, _, err := a.doRequest(write.request, write.identity, write.expected, true)
	if err != nil {
		return RemotePRObservation{}, err
	}
	observed, err := a.decodePR(write.identity, response, requestID, write.repository)
	if err != nil {
		return RemotePRObservation{}, &Error{Code: CodeRemoteWriteFailed, Submitted: true, Cause: errors.New("invalid bounded GitHub write response")}
	}
	return observed, nil
}

// write remains a package-private adapter test convenience. Controller code
// uses prepareWrite before publishing submitted authority, then executeWrite.
func (a *GitHubAdapter) write(ctx context.Context, repository githublifecycle.Repository, prNumber int64, title, body string, base, head githublifecycle.Branch) (RemotePRObservation, error) {
	write, err := a.prepareWrite(ctx, repository, prNumber, title, body, base, head)
	if err != nil {
		return RemotePRObservation{}, err
	}
	return a.executeWrite(write)
}

func (a *GitHubAdapter) request(ctx context.Context, method, template, escaped string, query url.Values, body []byte, expected int, write bool) (HTTPRequestIdentity, []byte, string, error) {
	id, data, requestID, _, err := a.requestHeaders(ctx, method, template, escaped, query, body, expected, write)
	return id, data, requestID, err
}

func (a *GitHubAdapter) requestHeaders(ctx context.Context, method, template, escaped string, query url.Values, body []byte, expected int, write bool) (HTTPRequestIdentity, []byte, string, http.Header, error) {
	id, req, cancel, err := a.buildRequest(ctx, method, template, escaped, query, body, expected)
	if err != nil {
		return id, nil, "", nil, err
	}
	defer cancel()
	data, requestID, headers, err := a.doRequest(req, id, expected, write)
	return id, data, requestID, headers, err
}

func (a *GitHubAdapter) buildRequest(ctx context.Context, method, template, escaped string, query url.Values, body []byte, expected int) (HTTPRequestIdentity, *http.Request, context.CancelFunc, error) {
	if ctx == nil {
		return HTTPRequestIdentity{}, nil, nil, errors.New("context is required")
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
	endpoint := a.origin + escaped
	if queryString != "" {
		endpoint += "?" + queryString
	}
	req, err := http.NewRequestWithContext(requestCtx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		cancel()
		return id, nil, nil, err
	}
	req.Header.Set("Accept", githubAccept)
	req.Header.Set("X-GitHub-Api-Version", githubAPIVer)
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	return id, req, cancel, nil
}

func (a *GitHubAdapter) doRequest(req *http.Request, id HTTPRequestIdentity, expected int, write bool) ([]byte, string, http.Header, error) {
	response, err := a.client.Do(req)
	if err != nil {
		if write {
			var never *neverSubmittedError
			if errors.As(err, &never) {
				return nil, "", nil, never
			}
			return nil, "", nil, &Error{Code: CodeRemoteWriteFailed, Submitted: true, Cause: errors.New("authenticated GitHub write transport failed")}
		}
		return nil, "", nil, &Error{Code: CodeRemoteReadFailed, Cause: errors.New("authenticated GitHub read transport failed")}
	}
	defer response.Body.Close()
	if err := boundHeaders(response.Header); err != nil {
		if write {
			return nil, "", nil, &Error{Code: CodeRemoteWriteFailed, Submitted: true, Cause: errors.New("bounded GitHub write headers are invalid")}
		}
		return nil, "", nil, err
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
			return nil, requestID, response.Header.Clone(), &Error{Code: CodeRemoteWriteFailed, Submitted: true, Cause: errors.New("bounded GitHub write response could not be read")}
		}
		return nil, requestID, response.Header.Clone(), &Error{Code: CodeRemoteReadFailed, Cause: errors.New("bounded GitHub read response could not be read")}
	}
	if response.StatusCode != expected {
		err = fmt.Errorf("unexpected GitHub status %d", response.StatusCode)
		if write {
			return nil, requestID, response.Header.Clone(), &Error{Code: CodeRemoteWriteFailed, Submitted: true, Cause: errors.New("unexpected bounded GitHub write status")}
		}
		return nil, requestID, response.Header.Clone(), err
	}
	return data, requestID, response.Header.Clone(), nil
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

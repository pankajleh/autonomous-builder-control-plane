package cilifecycle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	githubAPIOrigin = "https://api.github.com"
	githubAPIVer    = "2026-03-10"
	githubAccept    = "application/vnd.github+json"
)

// GitHubRequestAuthenticator may add only the Authorization header to a
// cloned, controller-owned request. It cannot replace any request identity.
type GitHubRequestAuthenticator interface {
	AuthenticateGitHubRequest(*http.Request) error
}

// GitHubAdapter owns the exact read-only GitHub protocol used by collection.
// Its fields and read methods are deliberately not exported: callers cannot
// choose endpoints, pagination, request limits, or retry behavior.
type GitHubAdapter struct {
	client *http.Client
	origin string
	limits Limits
	now    func() time.Time
}

type requestNotSubmittedError struct {
	code      string
	integrity bool
}

func (e *requestNotSubmittedError) Error() string { return e.code }

type authenticatedGitHubTransport struct {
	base          http.RoundTripper
	authenticator GitHubRequestAuthenticator
}

func (t *authenticatedGitHubTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet || request.Body != nil || request.ContentLength != 0 || len(request.TransferEncoding) != 0 {
		return nil, &requestNotSubmittedError{code: "unsafe_request_construction", integrity: true}
	}
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	method, endpoint, host := clone.Method, clone.URL.String(), clone.Host
	contentLength, body := clone.ContentLength, clone.Body
	transferEncoding := append([]string(nil), clone.TransferEncoding...)
	originalHeaders := clone.Header.Clone()
	if err := t.authenticator.AuthenticateGitHubRequest(clone); err != nil {
		return nil, &requestNotSubmittedError{code: "authentication_failed"}
	}
	if clone.Method != method || clone.URL.String() != endpoint || clone.Host != host ||
		clone.ContentLength != contentLength || clone.Body != body || !equalStringSlices(clone.TransferEncoding, transferEncoding) ||
		!headersDifferOnlyByAuthorization(originalHeaders, clone.Header) {
		return nil, &requestNotSubmittedError{code: "authenticator_mutated_request", integrity: true}
	}
	authorization := clone.Header.Values("Authorization")
	if len(authorization) != 1 || !validAuthorizationValue(authorization[0]) {
		return nil, &requestNotSubmittedError{code: "authentication_missing"}
	}
	return t.base.RoundTrip(clone)
}

func validAuthorizationValue(value string) bool {
	return value != "" && len(value) <= MaxTextBytes && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n")
}

func headersDifferOnlyByAuthorization(before, after http.Header) bool {
	for key, values := range before {
		if strings.EqualFold(key, "Authorization") {
			continue
		}
		if !equalStringSlices(values, after.Values(key)) {
			return false
		}
	}
	for key, values := range after {
		if strings.EqualFold(key, "Authorization") {
			continue
		}
		if !equalStringSlices(values, before.Values(key)) {
			return false
		}
	}
	return true
}

func equalStringSlices(a, b []string) bool {
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

// NewGitHubAdapter constructs the production adapter with a transport-level
// response-header cap. There is no retrying transport in the chain.
func NewGitHubAdapter(authenticator GitHubRequestAuthenticator) (*GitHubAdapter, error) {
	if authenticator == nil {
		return nil, errors.New("GitHub request authenticator is required")
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.MaxResponseHeaderBytes = MaxResponseHeaderBytes
	base.DisableKeepAlives = true
	return newGitHubAdapter(base, authenticator, githubAPIOrigin, productionLimits(), time.Now)
}

// newGitHubAdapter is the unexported test seam for local servers, clocks, and
// component-wise stricter limits.
func newGitHubAdapter(base http.RoundTripper, authenticator GitHubRequestAuthenticator, origin string, limits Limits, now func() time.Time) (*GitHubAdapter, error) {
	if base == nil || authenticator == nil || now == nil {
		return nil, errors.New("transport, authenticator, and clock are required")
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("GitHub API origin is invalid")
	}
	if !limitsAtMostProduction(limits) {
		return nil, errors.New("CI collection limits are invalid or exceed production")
	}
	client := &http.Client{
		Transport: &authenticatedGitHubTransport{base: base, authenticator: authenticator},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &GitHubAdapter{client: client, origin: strings.TrimSuffix(origin, "/"), limits: limits, now: now}, nil
}

func limitsAtMostProduction(actual Limits) bool {
	if actual.validate() != nil {
		return false
	}
	p := productionLimits()
	return actual.maxSuites <= p.maxSuites && actual.maxRuns <= p.maxRuns && actual.maxStatuses <= p.maxStatuses &&
		actual.itemsPerPage <= p.itemsPerPage && actual.maxSuitePages <= p.maxSuitePages && actual.maxRunPages <= p.maxRunPages &&
		actual.maxStatusPages <= p.maxStatusPages && actual.maxCollectionRequests <= p.maxCollectionRequests &&
		actual.maxResponseBodyBytes <= p.maxResponseBodyBytes && actual.maxResponseHeaderBytes <= p.maxResponseHeaderBytes &&
		actual.maxLinkBytes <= p.maxLinkBytes && actual.maxRequestIDBytes <= p.maxRequestIDBytes && actual.maxTextBytes <= p.maxTextBytes &&
		actual.maxStateBytes <= p.maxStateBytes && actual.maxTimestampBytes <= p.maxTimestampBytes &&
		actual.maxProvenanceBytes <= p.maxProvenanceBytes && actual.maxNonSweepBytes <= p.maxNonSweepBytes &&
		actual.maxBundleBytes <= p.maxBundleBytes && actual.callTimeout <= p.callTimeout &&
		actual.collectionTimeout <= p.collectionTimeout && actual.maxTransportRetries == 0
}

type collectionFailure struct {
	outcome CollectionOutcome
	code    string
	phase   string
	page    int
	cause   error
}

func failure(outcome CollectionOutcome, code, phase string, page int, cause error) *collectionFailure {
	return &collectionFailure{outcome: outcome, code: code, phase: phase, page: page, cause: cause}
}

type requestSpec struct {
	phase, pathTemplate, escapedPath string
	page                             int
	query                            url.Values
	head                             bool
}

type responseData struct {
	status  int
	body    []byte
	headers http.Header
}

type requestSession struct {
	adapter    *GitHubAdapter
	provenance []RequestProvenanceV1
}

func (s *requestSession) requestProvenance() []RequestProvenanceV1 {
	return append([]RequestProvenanceV1(nil), s.provenance...)
}

type requestIdentityWireV1 struct {
	Method         string `json:"method"`
	PathTemplate   string `json:"path_template"`
	EscapedPath    string `json:"escaped_path"`
	CanonicalQuery string `json:"canonical_query"`
	APIOrigin      string `json:"api_origin"`
	APIVersion     string `json:"api_version"`
	Accept         string `json:"accept"`
}

type responseEnvelopeWireV1 struct {
	HTTPStatus        int    `json:"http_status,omitempty"`
	ResponseBodySHA   string `json:"response_body_sha256,omitempty"`
	FailureCode       string `json:"failure_code,omitempty"`
	CapturedPrefixSHA string `json:"captured_prefix_sha256,omitempty"`
}

func digestJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func requestIdentitySHA256(in RequestProvenanceV1Input) string {
	return digestJSON(requestIdentityWireV1{in.Method, in.PathTemplate, in.EscapedPath, in.CanonicalQuery, in.APIOrigin, in.APIVersion, in.Accept})
}

func responseEnvelopeSHA256(in RequestProvenanceV1Input) string {
	return digestJSON(responseEnvelopeWireV1{
		HTTPStatus: in.HTTPStatus, ResponseBodySHA: in.ResponseBodySHA256, FailureCode: in.FailureCode,
		CapturedPrefixSHA: in.CapturedPrefixSHA256,
	})
}

func (s *requestSession) get(ctx context.Context, spec requestSpec) (responseData, *collectionFailure) {
	if len(s.provenance) >= s.adapter.limits.maxCollectionRequests {
		return responseData{}, failure(OutcomeTruncated, "request_limit_exceeded", spec.phase, spec.page, nil)
	}
	if ctx == nil || spec.phase == "" || spec.pathTemplate == "" || spec.escapedPath == "" ||
		!strings.HasPrefix(spec.escapedPath, "/") || spec.page < 0 {
		return responseData{}, failure(OutcomeIntegrityFailure, "unsafe_request_construction", spec.phase, spec.page, nil)
	}
	query := ""
	if spec.query != nil {
		query = spec.query.Encode()
	}
	identity := requestIdentityWireV1{http.MethodGet, spec.pathTemplate, spec.escapedPath, query, s.adapter.origin, githubAPIVer, githubAccept}
	requestDigest := digestJSON(identity)
	endpoint := s.adapter.origin + spec.escapedPath
	if query != "" {
		endpoint += "?" + query
	}
	callCtx, cancel := context.WithTimeout(ctx, s.adapter.limits.callTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return responseData{}, failure(OutcomeIntegrityFailure, "unsafe_request_construction", spec.phase, spec.page, nil)
	}
	request.Header.Set("Accept", githubAccept)
	request.Header.Set("X-GitHub-Api-Version", githubAPIVer)
	started := s.adapter.now().UTC().UnixNano()
	if started <= 0 {
		return responseData{}, failure(OutcomeIntegrityFailure, "invalid_clock", spec.phase, spec.page, nil)
	}
	response, requestErr := s.adapter.client.Do(request)
	if requestErr != nil {
		observed := s.adapter.now().UTC().UnixNano()
		outcome, code := OutcomeProviderUnavailable, "transport_failure"
		var notSubmitted *requestNotSubmittedError
		if errors.As(requestErr, &notSubmitted) {
			code = notSubmitted.code
			if notSubmitted.integrity {
				outcome = OutcomeIntegrityFailure
			}
		} else if errors.Is(requestErr, context.DeadlineExceeded) {
			code = "request_timeout"
		} else if errors.Is(requestErr, context.Canceled) {
			code = "request_canceled"
		} else if strings.Contains(requestErr.Error(), "server response headers exceeded") {
			outcome, code = OutcomeTruncated, "response_headers_too_large"
		}
		envelopeDigest := digestJSON(responseEnvelopeWireV1{FailureCode: code})
		provenanceErr := s.appendProvenance(RequestProvenanceV1Input{
			Sequence: len(s.provenance) + 1, Phase: spec.phase, Page: spec.page, Method: http.MethodGet,
			PathTemplate: spec.pathTemplate, EscapedPath: spec.escapedPath, CanonicalQuery: query,
			APIOrigin: s.adapter.origin, APIVersion: githubAPIVer, Accept: githubAccept, RequestSHA256: requestDigest,
			ResponseEnvelopeSHA256: envelopeDigest, RequestStartedUnixNano: started,
			ResponseObservedUnixNano: observed, FailureCode: code,
		})
		if provenanceErr != nil {
			return responseData{}, provenanceErr
		}
		return responseData{}, failure(outcome, code, spec.phase, spec.page, requestErr)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, int64(s.adapter.limits.maxResponseBodyBytes)+1))
	observed := s.adapter.now().UTC().UnixNano()
	status := response.StatusCode
	requestID := response.Header.Get("X-GitHub-Request-Id")
	headerBytes := responseHeaderBytes(response.Header)
	link := response.Header.Get("Link")

	provenance := RequestProvenanceV1Input{
		Sequence: len(s.provenance) + 1, Phase: spec.phase, Page: spec.page, Method: http.MethodGet,
		PathTemplate: spec.pathTemplate, EscapedPath: spec.escapedPath, CanonicalQuery: query,
		APIOrigin: s.adapter.origin, APIVersion: githubAPIVer, Accept: githubAccept, RequestSHA256: requestDigest,
		HTTPStatus: status, RequestStartedUnixNano: started, ResponseObservedUnixNano: observed, ResponseBytes: int64(len(body)),
	}
	var completedFailure *collectionFailure
	if readErr != nil {
		provenance.FailureCode = "response_read_failed"
		completedFailure = failure(OutcomeProviderUnavailable, provenance.FailureCode, spec.phase, spec.page, readErr)
	} else if len(body) > s.adapter.limits.maxResponseBodyBytes {
		prefix := body[:s.adapter.limits.maxResponseBodyBytes]
		provenance.BodyTruncated = true
		provenance.CapturedPrefixSHA256 = digestBytes(prefix)
		provenance.FailureCode = "response_body_too_large"
		completedFailure = failure(OutcomeTruncated, provenance.FailureCode, spec.phase, spec.page, nil)
	} else {
		provenance.ResponseBodySHA256 = digestBytes(body)
	}
	if completedFailure == nil && headerBytes > s.adapter.limits.maxResponseHeaderBytes {
		provenance.FailureCode = "response_headers_too_large"
		completedFailure = failure(OutcomeTruncated, provenance.FailureCode, spec.phase, spec.page, nil)
	}
	if completedFailure == nil && len(link) > s.adapter.limits.maxLinkBytes {
		provenance.FailureCode = "link_header_too_large"
		completedFailure = failure(OutcomeTruncated, provenance.FailureCode, spec.phase, spec.page, nil)
	}
	if len(requestID) <= s.adapter.limits.maxRequestIDBytes && validOptionalText(requestID, s.adapter.limits.maxRequestIDBytes) {
		provenance.RequestID = requestID
	} else if completedFailure == nil {
		provenance.FailureCode = "request_id_too_large"
		completedFailure = failure(OutcomeTruncated, provenance.FailureCode, spec.phase, spec.page, nil)
	}
	if completedFailure == nil && status != http.StatusOK {
		outcome, code := OutcomeProviderUnavailable, "provider_http_status"
		if spec.head && status == http.StatusNotFound {
			outcome, code = OutcomeStaleHead, "head_ref_not_found"
		}
		provenance.FailureCode = code
		completedFailure = failure(outcome, code, spec.phase, spec.page, nil)
	}
	provenance.ResponseEnvelopeSHA256 = digestJSON(responseEnvelopeWireV1{
		HTTPStatus: status, ResponseBodySHA: provenance.ResponseBodySHA256, FailureCode: provenance.FailureCode,
		CapturedPrefixSHA: provenance.CapturedPrefixSHA256,
	})
	if provenanceErr := s.appendProvenance(provenance); provenanceErr != nil {
		return responseData{}, provenanceErr
	}
	if completedFailure != nil {
		return responseData{}, completedFailure
	}
	return responseData{status: status, body: append([]byte(nil), body...), headers: response.Header.Clone()}, nil
}

func (s *requestSession) appendProvenance(in RequestProvenanceV1Input) *collectionFailure {
	if in.ResponseObservedUnixNano <= 0 || in.ResponseObservedUnixNano < in.RequestStartedUnixNano {
		return failure(OutcomeIntegrityFailure, "clock_inversion", in.Phase, in.Page, nil)
	}
	value, err := NewRequestProvenanceV1(in)
	if err != nil || len(value.CanonicalJSON()) > s.adapter.limits.maxProvenanceBytes {
		return failure(OutcomeIntegrityFailure, "invalid_request_provenance", in.Phase, in.Page, err)
	}
	s.provenance = append(s.provenance, value)
	return nil
}

// responseHeaderBytes is the deterministic HTTP/1 field-section bound used
// even when a test transport is not net/http's production transport.
func responseHeaderBytes(header http.Header) int {
	total := 2 // terminating CRLF
	for name, values := range header {
		for _, value := range values {
			total += len(http.CanonicalHeaderKey(name)) + 2 + len(value) + 2
		}
	}
	return total
}

func decodeProviderJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("provider JSON has trailing data")
	}
	return nil
}

func hasNextLink(link string) bool {
	for _, entry := range strings.Split(link, ",") {
		parts := strings.Split(entry, ";")
		for _, parameter := range parts[1:] {
			name, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(name), "rel") {
				continue
			}
			value = strings.Trim(strings.TrimSpace(value), `"`)
			for _, relation := range strings.Fields(value) {
				if strings.EqualFold(relation, "next") {
					return true
				}
			}
		}
	}
	return false
}

func requireStatus(response responseData, spec requestSpec, head bool) *collectionFailure {
	if response.status == http.StatusOK {
		return nil
	}
	if head && response.status == http.StatusNotFound {
		return failure(OutcomeStaleHead, "head_ref_not_found", spec.phase, spec.page, nil)
	}
	return failure(OutcomeProviderUnavailable, "provider_http_status", spec.phase, spec.page, nil)
}

type principalObservation struct {
	ID     int64  `json:"id"`
	NodeID string `json:"node_id"`
	Login  string `json:"login"`
}

func (s *requestSession) principal(ctx context.Context) (principalObservation, *collectionFailure) {
	spec := requestSpec{phase: "principal", pathTemplate: "/user", escapedPath: "/user"}
	response, failed := s.get(ctx, spec)
	if failed != nil {
		return principalObservation{}, failed
	}
	if failed = requireStatus(response, spec, false); failed != nil {
		return principalObservation{}, failed
	}
	var principal principalObservation
	if err := decodeProviderJSON(response.body, &principal); err != nil {
		return principalObservation{}, failure(OutcomeMalformed, "malformed_principal", spec.phase, 0, err)
	}
	if providerTextTooLarge(s.adapter.limits.maxTextBytes, principal.NodeID, principal.Login) {
		return principalObservation{}, failure(OutcomeTruncated, "principal_text_too_large", spec.phase, 0, nil)
	}
	if principal.ID <= 0 || !validText(principal.NodeID, s.adapter.limits.maxTextBytes) || !validText(principal.Login, s.adapter.limits.maxTextBytes) {
		return principalObservation{}, failure(OutcomeMalformed, "malformed_principal", spec.phase, 0, nil)
	}
	return principal, nil
}

func repositoryPath(owner, name string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

func (s *requestSession) head(ctx context.Context, owner, name, branch, phase string) (HeadObservationV1, *collectionFailure) {
	escaped := repositoryPath(owner, name) + "/git/ref/heads/" + url.PathEscape(branch)
	spec := requestSpec{phase: phase, pathTemplate: "/repos/{owner}/{repo}/git/ref/heads/{branch}", escapedPath: escaped, head: true}
	response, failed := s.get(ctx, spec)
	if failed != nil {
		return HeadObservationV1{}, failed
	}
	if failed = requireStatus(response, spec, true); failed != nil {
		return HeadObservationV1{}, failed
	}
	var wire struct {
		Ref    string `json:"ref"`
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	if err := decodeProviderJSON(response.body, &wire); err != nil {
		return HeadObservationV1{}, failure(OutcomeMalformed, "malformed_head_ref", phase, 0, err)
	}
	wantRef := "refs/heads/" + branch
	if providerTextTooLarge(s.adapter.limits.maxTextBytes+len("refs/heads/"), wire.Ref) || providerTextTooLarge(s.adapter.limits.maxStateBytes, wire.Object.Type) {
		return HeadObservationV1{}, failure(OutcomeTruncated, "head_text_too_large", phase, 0, nil)
	}
	if wire.Ref != wantRef || wire.Object.Type != "commit" || !validGitSHA(wire.Object.SHA) {
		return HeadObservationV1{}, failure(OutcomeMalformed, "malformed_head_ref", phase, 0, nil)
	}
	provenance := s.provenance[len(s.provenance)-1].Input()
	observation, err := NewHeadObservationV1(HeadObservationV1Input{
		Phase: phase, Ref: wire.Ref, ObjectType: wire.Object.Type, SHA: wire.Object.SHA,
		RequestSequence: provenance.Sequence, ResponseObservedUnixNano: provenance.ResponseObservedUnixNano,
	})
	if err != nil {
		return HeadObservationV1{}, failure(OutcomeMalformed, "malformed_head_ref", phase, 0, err)
	}
	return observation, nil
}

type suiteProviderWire struct {
	ID         int64   `json:"id"`
	NodeID     string  `json:"node_id"`
	HeadSHA    string  `json:"head_sha"`
	Status     string  `json:"status"`
	Conclusion *string `json:"conclusion"`
	App        *struct {
		ID int64 `json:"id"`
	} `json:"app"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type runProviderWire struct {
	ID         int64   `json:"id"`
	NodeID     string  `json:"node_id"`
	Name       string  `json:"name"`
	HeadSHA    string  `json:"head_sha"`
	Status     string  `json:"status"`
	Conclusion *string `json:"conclusion"`
	CheckSuite *struct {
		ID int64 `json:"id"`
	} `json:"check_suite"`
	App *struct {
		ID int64 `json:"id"`
	} `json:"app"`
	StartedAt   *string `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
}

type statusProviderWire struct {
	ID          int64   `json:"id"`
	NodeID      string  `json:"node_id"`
	State       string  `json:"state"`
	Context     string  `json:"context"`
	Description *string `json:"description"`
	SHA         string  `json:"sha"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	Creator     *struct {
		ID     int64  `json:"id"`
		NodeID string `json:"node_id"`
		Login  string `json:"login"`
		Type   string `json:"type"`
	} `json:"creator"`
}

type pageTracker struct {
	ids         map[int64]struct{}
	nodes       map[string]int64
	pageDigests map[string]struct{}
	identities  map[string]struct{}
}

func providerTextTooLarge(limit int, values ...string) bool {
	for _, value := range values {
		if len(value) > limit {
			return true
		}
	}
	return false
}

func providerOptionalTextTooLarge(limit int, values ...*string) bool {
	for _, value := range values {
		if value != nil && len(*value) > limit {
			return true
		}
	}
	return false
}

func newPageTracker() *pageTracker {
	return &pageTracker{map[int64]struct{}{}, map[string]int64{}, map[string]struct{}{}, map[string]struct{}{}}
}

func (t *pageTracker) checkPage(raw []byte, ids []int64, nodes []string, phase string, page int) *collectionFailure {
	seenOnPage := map[int64]struct{}{}
	for i, id := range ids {
		if id <= 0 {
			return failure(OutcomeMalformed, "invalid_provider_id", phase, page, nil)
		}
		if _, exists := seenOnPage[id]; exists {
			return failure(OutcomeMalformed, "duplicate_id_in_page", phase, page, nil)
		}
		seenOnPage[id] = struct{}{}
		if _, exists := t.ids[id]; exists {
			return failure(OutcomeUnstable, "duplicate_id_across_pages", phase, page, nil)
		}
		if nodes[i] != "" {
			if old, exists := t.nodes[nodes[i]]; exists && old != id {
				return failure(OutcomeMalformed, "conflicting_provider_identity", phase, page, nil)
			}
		}
	}
	if len(ids) > 0 {
		pageDigest := digestBytes(raw)
		if _, exists := t.pageDigests[pageDigest]; exists {
			return failure(OutcomeUnstable, "repeated_page_digest", phase, page, nil)
		}
		identityIDs := append([]int64(nil), ids...)
		sort.Slice(identityIDs, func(i, j int) bool { return identityIDs[i] < identityIDs[j] })
		identity := make([]string, len(identityIDs))
		for i, id := range identityIDs {
			identity[i] = strconv.FormatInt(id, 10)
		}
		identityDigest := digestBytes([]byte(strings.Join(identity, ",")))
		if _, exists := t.identities[identityDigest]; exists {
			return failure(OutcomeUnstable, "repeated_page_identity", phase, page, nil)
		}
		t.pageDigests[pageDigest] = struct{}{}
		t.identities[identityDigest] = struct{}{}
	}
	for i, id := range ids {
		t.ids[id] = struct{}{}
		if nodes[i] != "" {
			t.nodes[nodes[i]] = id
		}
	}
	return nil
}

func paginationQuery(page, perPage int, filterAll bool) url.Values {
	query := url.Values{}
	if filterAll {
		query.Set("filter", "all")
	}
	query.Set("page", strconv.Itoa(page))
	query.Set("per_page", strconv.Itoa(perPage))
	return query
}

func (s *requestSession) suites(ctx context.Context, owner, name, sha, phase string) ([]CheckSuiteObservationV1, int, *collectionFailure) {
	escaped := repositoryPath(owner, name) + "/commits/" + url.PathEscape(sha) + "/check-suites"
	tracker := newPageTracker()
	var observations []CheckSuiteObservationV1
	declaredTotal := -1
	for page := 1; page <= s.adapter.limits.maxSuitePages; page++ {
		spec := requestSpec{phase: phase, page: page, pathTemplate: "/repos/{owner}/{repo}/commits/{sha}/check-suites", escapedPath: escaped, query: paginationQuery(page, s.adapter.limits.itemsPerPage, true)}
		response, failed := s.get(ctx, spec)
		if failed != nil {
			return nil, 0, failed
		}
		if failed = requireStatus(response, spec, false); failed != nil {
			return nil, 0, failed
		}
		var wire struct {
			TotalCount  *int                `json:"total_count"`
			CheckSuites []suiteProviderWire `json:"check_suites"`
		}
		if err := decodeProviderJSON(response.body, &wire); err != nil || wire.TotalCount == nil || *wire.TotalCount < 0 {
			return nil, 0, failure(OutcomeMalformed, "malformed_suite_page", phase, page, err)
		}
		if *wire.TotalCount > s.adapter.limits.maxSuites {
			return nil, 0, failure(OutcomeTruncated, "suite_total_too_large", phase, page, nil)
		}
		if declaredTotal < 0 {
			declaredTotal = *wire.TotalCount
		} else if declaredTotal != *wire.TotalCount {
			return nil, 0, failure(OutcomeUnstable, "suite_total_changed", phase, page, nil)
		}
		if len(wire.CheckSuites) > s.adapter.limits.itemsPerPage {
			return nil, 0, failure(OutcomeTruncated, "suite_page_too_large", phase, page, nil)
		}
		ids, nodes := suitePageIdentities(wire.CheckSuites)
		if failed = tracker.checkPage(response.body, ids, nodes, phase, page); failed != nil {
			return nil, 0, failed
		}
		for _, item := range wire.CheckSuites {
			if item.HeadSHA != sha {
				return nil, 0, failure(OutcomeStaleHead, "suite_head_mismatch", phase, page, nil)
			}
			if providerTextTooLarge(s.adapter.limits.maxTextBytes, item.NodeID) ||
				providerTextTooLarge(s.adapter.limits.maxStateBytes, item.Status) ||
				providerOptionalTextTooLarge(s.adapter.limits.maxStateBytes, item.Conclusion) ||
				providerTextTooLarge(s.adapter.limits.maxTimestampBytes, item.CreatedAt, item.UpdatedAt) {
				return nil, 0, failure(OutcomeTruncated, "suite_text_too_large", phase, page, nil)
			}
			appID := int64(0)
			if item.App != nil {
				appID = item.App.ID
			}
			observation, err := NewCheckSuiteObservationV1(CheckSuiteObservationV1Input{item.ID, item.NodeID, appID, item.HeadSHA, item.Status, item.Conclusion, item.CreatedAt, item.UpdatedAt})
			if err != nil {
				return nil, 0, failure(OutcomeMalformed, "malformed_suite", phase, page, err)
			}
			observations = append(observations, observation)
		}
		next := hasNextLink(response.headers.Get("Link"))
		if len(observations) == declaredTotal {
			if next {
				if len(observations) == s.adapter.limits.maxSuites {
					return nil, 0, failure(OutcomeTruncated, "suite_next_beyond_limit", phase, page, nil)
				}
				return nil, 0, failure(OutcomeUnstable, "suite_next_beyond_total", phase, page, nil)
			}
			return observations, declaredTotal, nil
		}
		if len(observations) > declaredTotal {
			return nil, 0, failure(OutcomeUnstable, "suite_count_exceeded", phase, page, nil)
		}
		if len(wire.CheckSuites) < s.adapter.limits.itemsPerPage {
			return nil, 0, failure(OutcomeUnstable, "suite_short_page", phase, page, nil)
		}
	}
	return nil, 0, failure(OutcomeTruncated, "suite_pages_exhausted", phase, s.adapter.limits.maxSuitePages, nil)
}

func suitePageIdentities(items []suiteProviderWire) ([]int64, []string) {
	ids, nodes := make([]int64, len(items)), make([]string, len(items))
	for i := range items {
		ids[i], nodes[i] = items[i].ID, items[i].NodeID
	}
	return ids, nodes
}

func (s *requestSession) runs(ctx context.Context, owner, name, sha, phase string) ([]CheckRunObservationV1, int, *collectionFailure) {
	escaped := repositoryPath(owner, name) + "/commits/" + url.PathEscape(sha) + "/check-runs"
	tracker := newPageTracker()
	var observations []CheckRunObservationV1
	declaredTotal := -1
	for page := 1; page <= s.adapter.limits.maxRunPages; page++ {
		spec := requestSpec{phase: phase, page: page, pathTemplate: "/repos/{owner}/{repo}/commits/{sha}/check-runs", escapedPath: escaped, query: paginationQuery(page, s.adapter.limits.itemsPerPage, true)}
		response, failed := s.get(ctx, spec)
		if failed != nil {
			return nil, 0, failed
		}
		if failed = requireStatus(response, spec, false); failed != nil {
			return nil, 0, failed
		}
		var wire struct {
			TotalCount *int              `json:"total_count"`
			CheckRuns  []runProviderWire `json:"check_runs"`
		}
		if err := decodeProviderJSON(response.body, &wire); err != nil || wire.TotalCount == nil || *wire.TotalCount < 0 {
			return nil, 0, failure(OutcomeMalformed, "malformed_run_page", phase, page, err)
		}
		if *wire.TotalCount > s.adapter.limits.maxRuns {
			return nil, 0, failure(OutcomeTruncated, "run_total_too_large", phase, page, nil)
		}
		if declaredTotal < 0 {
			declaredTotal = *wire.TotalCount
		} else if declaredTotal != *wire.TotalCount {
			return nil, 0, failure(OutcomeUnstable, "run_total_changed", phase, page, nil)
		}
		if len(wire.CheckRuns) > s.adapter.limits.itemsPerPage {
			return nil, 0, failure(OutcomeTruncated, "run_page_too_large", phase, page, nil)
		}
		ids, nodes := runPageIdentities(wire.CheckRuns)
		if failed = tracker.checkPage(response.body, ids, nodes, phase, page); failed != nil {
			return nil, 0, failed
		}
		for _, item := range wire.CheckRuns {
			if item.HeadSHA != sha {
				return nil, 0, failure(OutcomeStaleHead, "run_head_mismatch", phase, page, nil)
			}
			if providerTextTooLarge(s.adapter.limits.maxTextBytes, item.NodeID, item.Name) ||
				providerTextTooLarge(s.adapter.limits.maxStateBytes, item.Status) ||
				providerOptionalTextTooLarge(s.adapter.limits.maxStateBytes, item.Conclusion) ||
				providerOptionalTextTooLarge(s.adapter.limits.maxTimestampBytes, item.StartedAt, item.CompletedAt) {
				return nil, 0, failure(OutcomeTruncated, "run_text_too_large", phase, page, nil)
			}
			suiteID, appID := int64(0), int64(0)
			if item.CheckSuite != nil {
				suiteID = item.CheckSuite.ID
			}
			if item.App != nil {
				appID = item.App.ID
			}
			observation, err := NewCheckRunObservationV1(CheckRunObservationV1Input{item.ID, item.NodeID, suiteID, appID, item.Name, item.HeadSHA, item.Status, item.Conclusion, item.StartedAt, item.CompletedAt})
			if err != nil {
				return nil, 0, failure(OutcomeMalformed, "malformed_run", phase, page, err)
			}
			observations = append(observations, observation)
		}
		next := hasNextLink(response.headers.Get("Link"))
		if len(observations) == declaredTotal {
			if next {
				if len(observations) == s.adapter.limits.maxRuns {
					return nil, 0, failure(OutcomeTruncated, "run_next_beyond_limit", phase, page, nil)
				}
				return nil, 0, failure(OutcomeUnstable, "run_next_beyond_total", phase, page, nil)
			}
			return observations, declaredTotal, nil
		}
		if len(observations) > declaredTotal {
			return nil, 0, failure(OutcomeUnstable, "run_count_exceeded", phase, page, nil)
		}
		if len(wire.CheckRuns) < s.adapter.limits.itemsPerPage {
			return nil, 0, failure(OutcomeUnstable, "run_short_page", phase, page, nil)
		}
	}
	return nil, 0, failure(OutcomeTruncated, "run_pages_exhausted", phase, s.adapter.limits.maxRunPages, nil)
}

func runPageIdentities(items []runProviderWire) ([]int64, []string) {
	ids, nodes := make([]int64, len(items)), make([]string, len(items))
	for i := range items {
		ids[i], nodes[i] = items[i].ID, items[i].NodeID
	}
	return ids, nodes
}

func (s *requestSession) statuses(ctx context.Context, owner, name, sha, phase string) ([]CommitStatusObservationV1, *collectionFailure) {
	escaped := repositoryPath(owner, name) + "/commits/" + url.PathEscape(sha) + "/statuses"
	tracker := newPageTracker()
	var observations []CommitStatusObservationV1
	for page := 1; page <= s.adapter.limits.maxStatusPages; page++ {
		spec := requestSpec{phase: phase, page: page, pathTemplate: "/repos/{owner}/{repo}/commits/{sha}/statuses", escapedPath: escaped, query: paginationQuery(page, s.adapter.limits.itemsPerPage, false)}
		response, failed := s.get(ctx, spec)
		if failed != nil {
			return nil, failed
		}
		if failed = requireStatus(response, spec, false); failed != nil {
			return nil, failed
		}
		var wire []statusProviderWire
		if err := decodeProviderJSON(response.body, &wire); err != nil {
			return nil, failure(OutcomeMalformed, "malformed_status_page", phase, page, err)
		}
		if len(wire) > s.adapter.limits.itemsPerPage {
			return nil, failure(OutcomeTruncated, "status_page_too_large", phase, page, nil)
		}
		ids, nodes := statusPageIdentities(wire)
		if failed = tracker.checkPage(response.body, ids, nodes, phase, page); failed != nil {
			return nil, failed
		}
		for _, item := range wire {
			if item.SHA != sha {
				return nil, failure(OutcomeStaleHead, "status_head_mismatch", phase, page, nil)
			}
			creatorID, creatorNode, creatorLogin, creatorType := int64(0), "", "", ""
			if item.Creator != nil {
				creatorID, creatorNode, creatorLogin, creatorType = item.Creator.ID, item.Creator.NodeID, item.Creator.Login, item.Creator.Type
			}
			if providerTextTooLarge(s.adapter.limits.maxTextBytes, item.NodeID, item.Context, creatorNode, creatorLogin, creatorType) ||
				providerOptionalTextTooLarge(s.adapter.limits.maxTextBytes, item.Description) ||
				providerTextTooLarge(s.adapter.limits.maxStateBytes, item.State) ||
				providerTextTooLarge(s.adapter.limits.maxTimestampBytes, item.CreatedAt, item.UpdatedAt) {
				return nil, failure(OutcomeTruncated, "status_text_too_large", phase, page, nil)
			}
			observation, err := NewCommitStatusObservationV1(CommitStatusObservationV1Input{item.ID, item.NodeID, item.State, item.Context, item.Description, item.SHA, item.CreatedAt, item.UpdatedAt, creatorID, creatorNode, creatorLogin, creatorType})
			if err != nil {
				return nil, failure(OutcomeMalformed, "malformed_status", phase, page, err)
			}
			observations = append(observations, observation)
			if len(observations) > s.adapter.limits.maxStatuses {
				return nil, failure(OutcomeTruncated, "status_total_too_large", phase, page, nil)
			}
		}
		next := hasNextLink(response.headers.Get("Link"))
		if page == s.adapter.limits.maxStatusPages {
			if len(wire) != 0 || next {
				return nil, failure(OutcomeTruncated, "status_sentinel_not_empty", phase, page, nil)
			}
			return observations, nil
		}
		if len(wire) < s.adapter.limits.itemsPerPage && !next {
			return observations, nil
		}
	}
	panic("unreachable status pagination")
}

func statusPageIdentities(items []statusProviderWire) ([]int64, []string) {
	ids, nodes := make([]int64, len(items)), make([]string, len(items))
	for i := range items {
		ids[i], nodes[i] = items[i].ID, items[i].NodeID
	}
	return ids, nodes
}

func (s *requestSession) sweep(ctx context.Context, owner, name, sha, suffix string) (CISemanticSweepV1, *collectionFailure) {
	suites, suiteTotal, failed := s.suites(ctx, owner, name, sha, "suites_"+suffix)
	if failed != nil {
		return CISemanticSweepV1{}, failed
	}
	runs, runTotal, failed := s.runs(ctx, owner, name, sha, "runs_"+suffix)
	if failed != nil {
		return CISemanticSweepV1{}, failed
	}
	statuses, failed := s.statuses(ctx, owner, name, sha, "statuses_"+suffix)
	if failed != nil {
		return CISemanticSweepV1{}, failed
	}
	sweep, err := NewCISemanticSweepV1(CISemanticSweepV1Input{
		RepositoryOwner: owner, RepositoryName: name, HeadSHA: sha,
		CheckSuiteTotalCount: suiteTotal, CheckRunTotalCount: runTotal,
		CheckSuites: suites, CheckRuns: runs, CommitStatuses: statuses,
	})
	if err != nil {
		return CISemanticSweepV1{}, failure(OutcomeMalformed, "invalid_semantic_sweep", "sweep_"+suffix, 0, err)
	}
	return sweep, nil
}

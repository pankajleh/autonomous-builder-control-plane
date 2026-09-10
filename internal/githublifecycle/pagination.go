package githublifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	PaginationClosureSchemaV1 = "pagination-closure-v1"
	GitHubAPIVersionV1        = "2026-03-10"
)

type PaginationSourceKind string

const (
	PaginationCheckRuns      PaginationSourceKind = "check_runs"
	PaginationCommitStatuses PaginationSourceKind = "commit_statuses"
	PaginationReviews        PaginationSourceKind = "reviews"
)

type PaginationProtocol string

const (
	PaginationREST    PaginationProtocol = "rest"
	PaginationGraphQL PaginationProtocol = "graphql"
)

type PaginationQueryV1 struct {
	Source               PaginationSourceKind `json:"source"`
	Protocol             PaginationProtocol   `json:"protocol"`
	Method               string               `json:"method"`
	PathOrDocumentSHA256 string               `json:"path_or_document_sha256"`
	APIVersion           string               `json:"api_version"`
	RepositoryNodeID     string               `json:"repository_node_id"`
	PullRequestNumber    int64                `json:"pull_request_number,omitempty"`
	PullRequestNodeID    string               `json:"pull_request_node_id,omitempty"`
	HeadSHA              string               `json:"head_sha"`
	Variables            map[string]string    `json:"variables"`
	PerPage              int                  `json:"per_page"`
}

// PaginationQueryScopeV1 contains only authority-derived identities. The
// endpoint, method, API version, filters, and page size are frozen locally.
type PaginationQueryScopeV1 struct {
	Source           PaginationSourceKind
	Repository       Repository
	RepositoryNodeID string
	PullRequest      *PullRequestIdentity
	HeadSHA          GitSHA
}

func DerivePaginationQueryV1(scope PaginationQueryScopeV1, limits Limits) (PaginationQueryV1, error) {
	if err := limits.Validate(); err != nil {
		return PaginationQueryV1{}, err
	}
	if !scope.Repository.valid() || !validOpaqueID(scope.RepositoryNodeID, limits.MaxTextBytes) || !scope.HeadSHA.valid() {
		return PaginationQueryV1{}, errors.New("pagination authority scope is invalid")
	}
	base := "/repos/" + scope.Repository.Owner() + "/" + scope.Repository.Name()
	query := PaginationQueryV1{
		Source: scope.Source, Protocol: PaginationREST, Method: "GET", APIVersion: GitHubAPIVersionV1,
		RepositoryNodeID: scope.RepositoryNodeID, HeadSHA: scope.HeadSHA.String(), Variables: map[string]string{}, PerPage: limits.MaxItemsPerPage,
	}
	switch scope.Source {
	case PaginationCheckRuns:
		if scope.PullRequest != nil {
			return PaginationQueryV1{}, errors.New("check-run pagination cannot carry a pull request identity")
		}
		query.PathOrDocumentSHA256 = base + "/commits/" + scope.HeadSHA.String() + "/check-runs"
		query.Variables = map[string]string{"filter": "all"}
	case PaginationCommitStatuses:
		if scope.PullRequest != nil {
			return PaginationQueryV1{}, errors.New("commit-status pagination cannot carry a pull request identity")
		}
		query.PathOrDocumentSHA256 = base + "/commits/" + scope.HeadSHA.String() + "/statuses"
	case PaginationReviews:
		if scope.PullRequest == nil || !scope.PullRequest.valid() {
			return PaginationQueryV1{}, errors.New("review pagination requires the exact pull request identity")
		}
		query.PullRequestNumber = scope.PullRequest.Number()
		query.PullRequestNodeID = scope.PullRequest.NodeID()
		query.PathOrDocumentSHA256 = base + "/pulls/" + strconv.FormatInt(scope.PullRequest.Number(), 10) + "/reviews"
	default:
		return PaginationQueryV1{}, errors.New("unsupported pagination source")
	}
	return query, nil
}

func (q PaginationQueryV1) valid(limits Limits) bool {
	if (q.Source != PaginationCheckRuns && q.Source != PaginationCommitStatuses && q.Source != PaginationReviews) ||
		(q.Protocol != PaginationREST && q.Protocol != PaginationGraphQL) || !validText(q.Method, 16, false) ||
		!validText(q.PathOrDocumentSHA256, limits.MaxTextBytes, false) || !validText(q.APIVersion, limits.MaxTextBytes, false) ||
		!validOpaqueID(q.RepositoryNodeID, limits.MaxTextBytes) || q.PerPage <= 0 || q.PerPage > limits.MaxItemsPerPage {
		return false
	}
	if q.Protocol == PaginationGraphQL && !validSHA256(q.PathOrDocumentSHA256) {
		return false
	}
	if q.Protocol == PaginationREST && (!strings.HasPrefix(q.PathOrDocumentSHA256, "/") || strings.ContainsAny(q.PathOrDocumentSHA256, "?#")) {
		return false
	}
	if _, err := NewGitSHA(q.HeadSHA); err != nil {
		return false
	}
	if q.Source == PaginationReviews {
		if q.PullRequestNumber <= 0 || !validOpaqueID(q.PullRequestNodeID, limits.MaxTextBytes) {
			return false
		}
	} else if q.PullRequestNumber != 0 || q.PullRequestNodeID != "" {
		return false
	}
	return validateStringMap(q.Variables, limits) == nil
}

type CanonicalPaginationItemV1 struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
}

type PaginationPageV1Input struct {
	Ordinal            int
	RequestedPage      int
	RequestedCursor    string
	Response           SnapshotIdentity
	RawBodySHA256      string
	ResponseEvidence   ledger.EvidenceRef
	Items              []CanonicalPaginationItemV1
	RESTLinkHeader     string
	RESTLinkObserved   bool
	GraphQLHasNextPage *bool
	GraphQLEndCursor   string
}

type PaginationPageV1 struct {
	input    PaginationPageV1Input
	envelope []byte
	digest   string
}

func NewPaginationPageV1(input PaginationPageV1Input, limits Limits) (PaginationPageV1, error) {
	input = clonePaginationPageInput(input)
	if err := limits.Validate(); err != nil {
		return PaginationPageV1{}, err
	}
	if input.Ordinal < 0 || !input.Response.valid() || !validSHA256(input.RawBodySHA256) || !validEvidenceRef(input.ResponseEvidence) || input.ResponseEvidence.SHA256 != input.RawBodySHA256 || len(input.Items) > limits.MaxItemsPerPage {
		return PaginationPageV1{}, errors.New("pagination page identity or bounds are invalid")
	}
	seen := map[string]struct{}{}
	for _, item := range input.Items {
		if !validText(item.Key, limits.MaxTextBytes, false) || !validSHA256(item.SHA256) {
			return PaginationPageV1{}, errors.New("pagination item identity is invalid")
		}
		if _, ok := seen[item.Key]; ok {
			return PaginationPageV1{}, errors.New("pagination page duplicates an item key")
		}
		seen[item.Key] = struct{}{}
	}
	if len(input.RESTLinkHeader) > limits.MaxTextBytes || (input.RESTLinkObserved && (input.GraphQLHasNextPage != nil || input.GraphQLEndCursor != "")) {
		return PaginationPageV1{}, errors.New("pagination page mixes REST and GraphQL terminal evidence")
	}
	if input.GraphQLHasNextPage != nil && (input.RESTLinkObserved || input.RESTLinkHeader != "" || !validText(input.GraphQLEndCursor, limits.MaxTextBytes, true)) {
		return PaginationPageV1{}, errors.New("GraphQL page info is invalid")
	}
	if !input.RESTLinkObserved && input.RESTLinkHeader != "" {
		return PaginationPageV1{}, errors.New("unobserved REST Link header cannot contain pagination state")
	}
	envelope, digest, err := canonicalJSON(paginationPageWire(input))
	if err != nil {
		return PaginationPageV1{}, err
	}
	if len(envelope) > limits.MaxPaginationClosureBytes {
		return PaginationPageV1{}, errors.New("pagination response envelope exceeds byte limit")
	}
	return PaginationPageV1{input, envelope, digest}, nil
}
func (p PaginationPageV1) Input() PaginationPageV1Input { return clonePaginationPageInput(p.input) }
func (p PaginationPageV1) CanonicalEnvelope() []byte    { return append([]byte(nil), p.envelope...) }
func (p PaginationPageV1) SHA256() string               { return p.digest }
func (p PaginationPageV1) valid() bool {
	return len(p.envelope) > 0 && validSHA256(p.digest) && digestBytes(p.envelope) == p.digest
}

type paginationPageWireV1 struct {
	Ordinal            int                         `json:"ordinal"`
	RequestedPage      int                         `json:"requested_page,omitempty"`
	RequestedCursor    string                      `json:"requested_cursor,omitempty"`
	Response           identityWire                `json:"response"`
	RawBodySHA256      string                      `json:"raw_body_sha256"`
	ResponseEvidence   ledger.EvidenceRef          `json:"response_evidence"`
	Items              []CanonicalPaginationItemV1 `json:"items"`
	ItemSetSHA256      string                      `json:"item_set_sha256"`
	RESTLinkHeader     string                      `json:"rest_link_header"`
	RESTLinkObserved   bool                        `json:"rest_link_observed"`
	GraphQLHasNextPage *bool                       `json:"graphql_has_next_page,omitempty"`
	GraphQLEndCursor   string                      `json:"graphql_end_cursor,omitempty"`
}

func paginationPageWire(i PaginationPageV1Input) paginationPageWireV1 {
	items, _, _ := canonicalJSON(i.Items)
	return paginationPageWireV1{i.Ordinal, i.RequestedPage, i.RequestedCursor, snapshotWire(i.Response), i.RawBodySHA256, i.ResponseEvidence, i.Items, digestBytes(items), i.RESTLinkHeader, i.RESTLinkObserved, i.GraphQLHasNextPage, i.GraphQLEndCursor}
}

type PaginationClosureV1Input struct {
	Query        PaginationQueryV1
	Pages        []PaginationPageV1
	EvidenceRefs []ledger.EvidenceRef
}
type PaginationClosureV1 struct {
	input     PaginationClosureV1Input
	canonical []byte
	digest    string
	limitsSHA string
}

func NewPaginationClosureV1(input PaginationClosureV1Input, limits Limits) (PaginationClosureV1, error) {
	input = clonePaginationClosureInput(input)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return PaginationClosureV1{}, err
	}
	if !input.Query.valid(limits) || len(input.Pages) == 0 || len(input.Pages) > limits.MaxPages {
		return PaginationClosureV1{}, errors.New("pagination query or page count is invalid")
	}
	all := map[string]struct{}{}
	requests := map[string]struct{}{}
	responseEvidence := make([]ledger.EvidenceRef, 0, len(input.Pages))
	total := 0
	for index, page := range input.Pages {
		if !page.valid() {
			return PaginationClosureV1{}, fmt.Errorf("pagination page %d is incomplete", index)
		}
		rebuilt, err := NewPaginationPageV1(page.input, limits)
		if err != nil || rebuilt.digest != page.digest || !bytes.Equal(rebuilt.envelope, page.envelope) {
			return PaginationClosureV1{}, fmt.Errorf("pagination page %d fails independent validation", index)
		}
		p := page.input
		if _, duplicated := requests[p.Response.RequestID()]; duplicated {
			return PaginationClosureV1{}, errors.New("pagination response request identity is duplicated")
		}
		requests[p.Response.RequestID()] = struct{}{}
		responseEvidence = append(responseEvidence, p.ResponseEvidence)
		if p.Ordinal != index {
			return PaginationClosureV1{}, errors.New("pagination page ordinal is missing, repeated, or reordered")
		}
		total += len(p.Items)
		if total > limits.MaxTotalItems {
			return PaginationClosureV1{}, errors.New("pagination closure item limit exceeded")
		}
		for _, item := range p.Items {
			if _, ok := all[item.Key]; ok {
				return PaginationClosureV1{}, errors.New("cross-page duplicate item key")
			}
			all[item.Key] = struct{}{}
		}
		terminal := index == len(input.Pages)-1
		if input.Query.Protocol == PaginationREST {
			if p.RequestedPage != index+1 || p.RequestedCursor != "" || p.GraphQLHasNextPage != nil || !p.RESTLinkObserved {
				return PaginationClosureV1{}, errors.New("REST page chain is inconsistent")
			}
			next, hasNext, err := parseRESTNext(p.RESTLinkHeader, input.Query)
			if err != nil {
				return PaginationClosureV1{}, err
			}
			if terminal && hasNext {
				return PaginationClosureV1{}, errors.New("REST terminal page still advertises next")
			}
			if !terminal && (!hasNext || next != index+2) {
				return PaginationClosureV1{}, errors.New("REST page chain skips or invents a page")
			}
		}
		if input.Query.Protocol == PaginationGraphQL {
			if p.RequestedPage != 0 || p.RESTLinkObserved || p.RESTLinkHeader != "" || p.GraphQLHasNextPage == nil {
				return PaginationClosureV1{}, errors.New("GraphQL page chain is inconsistent")
			}
			expected := ""
			if index > 0 {
				expected = input.Pages[index-1].input.GraphQLEndCursor
			}
			if p.RequestedCursor != expected {
				return PaginationClosureV1{}, errors.New("GraphQL cursor chain skips, repeats, or reorders a page")
			}
			if terminal && *p.GraphQLHasNextPage {
				return PaginationClosureV1{}, errors.New("GraphQL terminal page still has next page")
			}
			if !terminal && (!*p.GraphQLHasNextPage || p.GraphQLEndCursor == "") {
				return PaginationClosureV1{}, errors.New("GraphQL nonterminal page lacks a next cursor")
			}
		}
	}
	if len(input.EvidenceRefs) == 0 || canonicalizeEvidence(&input.EvidenceRefs, limits) != nil {
		return PaginationClosureV1{}, errors.New("pagination closure evidence is invalid")
	}
	for _, evidence := range responseEvidence {
		if !containsEvidence(input.EvidenceRefs, evidence) {
			return PaginationClosureV1{}, errors.New("pagination closure omits retained response evidence")
		}
	}
	canonical, digest, err := canonicalJSON(paginationClosureWire(input, limitsSHA))
	if err != nil {
		return PaginationClosureV1{}, err
	}
	if len(canonical) > limits.MaxPaginationClosureBytes {
		return PaginationClosureV1{}, errors.New("pagination closure exceeds byte limit")
	}
	return PaginationClosureV1{input, canonical, digest, limitsSHA}, nil
}
func (c PaginationClosureV1) Input() PaginationClosureV1Input {
	return clonePaginationClosureInput(c.input)
}
func (c PaginationClosureV1) Query() PaginationQueryV1 { return clonePaginationQuery(c.input.Query) }
func (c PaginationClosureV1) CanonicalJSON() []byte    { return append([]byte(nil), c.canonical...) }
func (c PaginationClosureV1) SHA256() string           { return c.digest }
func (c PaginationClosureV1) MarshalJSON() ([]byte, error) {
	if !c.valid() {
		return nil, errors.New("pagination closure incomplete")
	}
	return c.CanonicalJSON(), nil
}
func (c PaginationClosureV1) valid() bool {
	return len(c.canonical) > 0 && validSHA256(c.digest) && digestBytes(c.canonical) == c.digest && validSHA256(c.limitsSHA)
}

type paginationClosureWireV1 struct {
	Schema               string               `json:"schema"`
	Query                PaginationQueryV1    `json:"query"`
	Pages                []json.RawMessage    `json:"pages"`
	ClosureItemSetSHA256 string               `json:"closure_item_set_sha256"`
	EvidenceRefs         []ledger.EvidenceRef `json:"evidence_refs"`
	LimitsSHA256         string               `json:"limits_sha256"`
}

func paginationClosureWire(i PaginationClosureV1Input, limitsSHA string) paginationClosureWireV1 {
	pages := make([]json.RawMessage, len(i.Pages))
	items := []CanonicalPaginationItemV1{}
	for x, p := range i.Pages {
		pages[x] = p.CanonicalEnvelope()
		items = append(items, p.input.Items...)
	}
	sort.Slice(items, func(a, b int) bool { return items[a].Key < items[b].Key })
	set, _, _ := canonicalJSON(items)
	return paginationClosureWireV1{PaginationClosureSchemaV1, clonePaginationQuery(i.Query), pages, digestBytes(set), i.EvidenceRefs, limitsSHA}
}

func ValidatePaginationClosureV1(scope PaginationQueryScopeV1, closure PaginationClosureV1, items []CanonicalPaginationItemV1, limits Limits) error {
	if !closure.valid() {
		return errors.New("pagination closure is incomplete")
	}
	if err := requireLimitsSHA(limits, closure.limitsSHA); err != nil {
		return err
	}
	rebuilt, err := NewPaginationClosureV1(closure.input, limits)
	if err != nil || rebuilt.digest != closure.digest || !bytes.Equal(rebuilt.canonical, closure.canonical) {
		return errors.New("pagination closure fails independent validation")
	}
	expected, err := DerivePaginationQueryV1(scope, limits)
	if err != nil {
		return err
	}
	if !equalPaginationQuery(expected, closure.input.Query) {
		return errors.New("pagination closure query identity changed")
	}
	actual := []CanonicalPaginationItemV1{}
	for _, p := range closure.input.Pages {
		actual = append(actual, p.input.Items...)
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i].Key < actual[j].Key })
	items = append([]CanonicalPaginationItemV1(nil), items...)
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	if len(actual) != len(items) {
		return errors.New("pagination closure item set is incomplete")
	}
	for i := range actual {
		if actual[i] != items[i] {
			return errors.New("pagination closure item digest disagrees with snapshot")
		}
	}
	return nil
}

func ParseCanonicalPaginationClosureV1(data []byte, limits Limits) (PaginationClosureV1, error) {
	var w paginationClosureWireV1
	if err := strictDecode(data, &w); err != nil {
		return PaginationClosureV1{}, err
	}
	if w.Schema != PaginationClosureSchemaV1 {
		return PaginationClosureV1{}, errors.New("unsupported pagination closure schema")
	}
	pages := make([]PaginationPageV1, len(w.Pages))
	for i, raw := range w.Pages {
		var pw paginationPageWireV1
		if err := strictDecode(raw, &pw); err != nil {
			return PaginationClosureV1{}, err
		}
		response, err := NewSnapshotIdentity(pw.Response.Provider, pw.Response.RequestID, pw.Response.ObservedUnixNano)
		if err != nil {
			return PaginationClosureV1{}, err
		}
		page, err := NewPaginationPageV1(PaginationPageV1Input{
			Ordinal: pw.Ordinal, RequestedPage: pw.RequestedPage, RequestedCursor: pw.RequestedCursor,
			Response: response, RawBodySHA256: pw.RawBodySHA256, ResponseEvidence: pw.ResponseEvidence,
			Items: pw.Items, RESTLinkHeader: pw.RESTLinkHeader, RESTLinkObserved: pw.RESTLinkObserved,
			GraphQLHasNextPage: pw.GraphQLHasNextPage, GraphQLEndCursor: pw.GraphQLEndCursor,
		}, limits)
		if err != nil {
			return PaginationClosureV1{}, err
		}
		if paginationPageWire(page.input).ItemSetSHA256 != pw.ItemSetSHA256 {
			return PaginationClosureV1{}, errors.New("pagination page item digest disagrees")
		}
		if err := requireCanonical(raw, page.envelope); err != nil {
			return PaginationClosureV1{}, err
		}
		pages[i] = page
	}
	value, err := NewPaginationClosureV1(PaginationClosureV1Input{w.Query, pages, w.EvidenceRefs}, limits)
	if err != nil {
		return PaginationClosureV1{}, err
	}
	rebuiltWire := paginationClosureWire(value.input, value.limitsSHA)
	if rebuiltWire.ClosureItemSetSHA256 != w.ClosureItemSetSHA256 || w.LimitsSHA256 != value.limitsSHA {
		return PaginationClosureV1{}, errors.New("pagination closure digest or limits disagree")
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return PaginationClosureV1{}, err
	}
	return value, nil
}

func parseRESTNext(header string, q PaginationQueryV1) (int, bool, error) {
	if len(header) == 0 {
		return 0, false, nil
	}
	relations := map[string]string{}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		pieces := strings.Split(part, ";")
		if len(pieces) != 2 || len(pieces[0]) < 3 || pieces[0][0] != '<' || pieces[0][len(pieces[0])-1] != '>' {
			return 0, false, errors.New("REST Link header is malformed or ambiguous")
		}
		rel := strings.TrimSpace(pieces[1])
		if !strings.HasPrefix(rel, "rel=\"") || !strings.HasSuffix(rel, "\"") {
			return 0, false, errors.New("REST Link relation is malformed")
		}
		name := strings.TrimSuffix(strings.TrimPrefix(rel, "rel=\""), "\"")
		if name != "next" && name != "prev" && name != "first" && name != "last" {
			return 0, false, errors.New("REST Link relation is unsupported")
		}
		if _, ok := relations[name]; ok {
			return 0, false, errors.New("REST Link relation is duplicated")
		}
		relations[name] = pieces[0][1 : len(pieces[0])-1]
	}
	raw, ok := relations["next"]
	if !ok {
		return 0, false, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "api.github.com" || u.Path != q.PathOrDocumentSHA256 {
		return 0, false, errors.New("REST next link changed endpoint identity")
	}
	values := u.Query()
	page, err := strconv.Atoi(values.Get("page"))
	if err != nil || page <= 0 {
		return 0, false, errors.New("REST next link page is invalid")
	}
	perPage, err := strconv.Atoi(values.Get("per_page"))
	if err != nil || perPage != q.PerPage {
		return 0, false, errors.New("REST next link per_page changed")
	}
	if len(values) != len(q.Variables)+2 {
		return 0, false, errors.New("REST next link variables changed")
	}
	for key, expected := range q.Variables {
		actual, ok := values[key]
		if !ok || len(actual) != 1 || actual[0] != expected {
			return 0, false, errors.New("REST next link query identity changed")
		}
	}
	return page, true, nil
}
func validateStringMap(values map[string]string, l Limits) error {
	if len(values) > l.MaxMetadataItems {
		return errors.New("query variables exceed item limit")
	}
	for k, v := range values {
		if !validText(k, l.MaxTextBytes, false) || !validText(v, l.MaxTextBytes, true) {
			return errors.New("query variable is invalid")
		}
	}
	return nil
}
func clonePaginationQuery(q PaginationQueryV1) PaginationQueryV1 {
	q.Variables = cloneMap(q.Variables)
	return q
}
func equalPaginationQuery(a, b PaginationQueryV1) bool {
	aa, _ := json.Marshal(clonePaginationQuery(a))
	bb, _ := json.Marshal(clonePaginationQuery(b))
	return bytes.Equal(aa, bb)
}
func clonePaginationPageInput(i PaginationPageV1Input) PaginationPageV1Input {
	i.Items = append([]CanonicalPaginationItemV1(nil), i.Items...)
	if i.GraphQLHasNextPage != nil {
		v := *i.GraphQLHasNextPage
		i.GraphQLHasNextPage = &v
	}
	return i
}
func clonePaginationPage(p PaginationPageV1) PaginationPageV1 {
	p.input = clonePaginationPageInput(p.input)
	p.envelope = append([]byte(nil), p.envelope...)
	return p
}
func clonePaginationClosureInput(i PaginationClosureV1Input) PaginationClosureV1Input {
	i.Query = clonePaginationQuery(i.Query)
	i.Pages = append([]PaginationPageV1(nil), i.Pages...)
	for x := range i.Pages {
		i.Pages[x] = clonePaginationPage(i.Pages[x])
	}
	i.EvidenceRefs = append([]ledger.EvidenceRef(nil), i.EvidenceRefs...)
	return i
}
func clonePaginationClosure(c PaginationClosureV1) PaginationClosureV1 {
	c.input = clonePaginationClosureInput(c.input)
	c.canonical = append([]byte(nil), c.canonical...)
	return c
}

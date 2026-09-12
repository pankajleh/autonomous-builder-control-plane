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
	PaginationClosureSchemaV1              = "pagination-closure-v1"
	GitHubAPIVersionV1                     = "2026-03-10"
	GitHubPaginationBodyEvidenceKindV1     = "github-response-body"
	GitHubPaginationEnvelopeEvidenceKindV1 = "github-pagination-response-envelope"
	GitHubPaginationEnvelopeSchemaV1       = "github-pagination-response-envelope-v1"
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
		RepositoryNodeID: scope.RepositoryNodeID, HeadSHA: scope.HeadSHA.String(), Variables: map[string]string{}, PerPage: limits.MaxPaginationItemsPerPage,
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
		!validOpaqueID(q.RepositoryNodeID, limits.MaxTextBytes) || q.PerPage <= 0 || q.PerPage > limits.MaxPaginationItemsPerPage {
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
	Query              PaginationQueryV1
	Ordinal            int
	RequestedPage      int
	RequestedCursor    string
	Response           SnapshotIdentity
	RawBodySHA256      string
	ResponseEvidence   ledger.EvidenceRef
	EnvelopeEvidence   ledger.EvidenceRef
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

func NewPaginationEnvelopeEvidenceV1(uri string, input PaginationPageV1Input, limits Limits) (ledger.EvidenceRef, error) {
	input = clonePaginationPageInput(input)
	if err := limits.Validate(); err != nil {
		return ledger.EvidenceRef{}, err
	}
	if !validText(uri, limits.MaxTextBytes, false) || !input.Query.valid(limits) || !input.Response.valid() || input.Response.Provider() != "github" ||
		!validSHA256(input.RawBodySHA256) || !validEvidenceRef(input.ResponseEvidence) ||
		input.ResponseEvidence.Kind != GitHubPaginationBodyEvidenceKindV1 || input.ResponseEvidence.SHA256 != input.RawBodySHA256 {
		return ledger.EvidenceRef{}, errors.New("pagination response envelope evidence identity is invalid")
	}
	canonical, _, err := canonicalJSON(paginationResponseEnvelopeWire(input))
	if err != nil || len(canonical) > limits.MaxPaginationClosureBytes {
		return ledger.EvidenceRef{}, errors.New("pagination response envelope evidence is invalid or unbounded")
	}
	return ledger.EvidenceRef{URI: uri, Kind: GitHubPaginationEnvelopeEvidenceKindV1, SHA256: digestBytes(canonical)}, nil
}

func NewPaginationPageV1(input PaginationPageV1Input, limits Limits) (PaginationPageV1, error) {
	input = clonePaginationPageInput(input)
	if err := limits.Validate(); err != nil {
		return PaginationPageV1{}, err
	}
	if !input.Query.valid(limits) || input.Ordinal < 0 || !input.Response.valid() || input.Response.Provider() != "github" || !validSHA256(input.RawBodySHA256) ||
		!validEvidenceRef(input.ResponseEvidence) || input.ResponseEvidence.Kind != GitHubPaginationBodyEvidenceKindV1 ||
		input.ResponseEvidence.SHA256 != input.RawBodySHA256 || !validEvidenceRef(input.EnvelopeEvidence) ||
		input.EnvelopeEvidence.Kind != GitHubPaginationEnvelopeEvidenceKindV1 || len(input.Items) > limits.MaxPaginationItemsPerPage {
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
	if len(input.Response.RequestID()) > limits.MaxRequestIDBytes || len(input.RESTLinkHeader) > limits.MaxLinkHeaderBytes ||
		(input.RESTLinkObserved && (input.GraphQLHasNextPage != nil || input.GraphQLEndCursor != "")) {
		return PaginationPageV1{}, errors.New("pagination page mixes REST and GraphQL terminal evidence")
	}
	if input.GraphQLHasNextPage != nil && (input.RESTLinkObserved || input.RESTLinkHeader != "" || !validText(input.GraphQLEndCursor, limits.MaxTextBytes, true)) {
		return PaginationPageV1{}, errors.New("GraphQL page info is invalid")
	}
	if !input.RESTLinkObserved && input.RESTLinkHeader != "" {
		return PaginationPageV1{}, errors.New("unobserved REST Link header cannot contain pagination state")
	}
	responseEnvelope, _, err := canonicalJSON(paginationResponseEnvelopeWire(input))
	if err != nil || input.EnvelopeEvidence.SHA256 != digestBytes(responseEnvelope) {
		return PaginationPageV1{}, errors.New("pagination response envelope evidence does not bind the exact response fields")
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
	EnvelopeEvidence   ledger.EvidenceRef          `json:"response_envelope_evidence"`
	Items              []CanonicalPaginationItemV1 `json:"items"`
	ItemSetSHA256      string                      `json:"item_set_sha256"`
	RESTLinkHeader     string                      `json:"rest_link_header"`
	RESTLinkObserved   bool                        `json:"rest_link_observed"`
	GraphQLHasNextPage *bool                       `json:"graphql_has_next_page,omitempty"`
	GraphQLEndCursor   string                      `json:"graphql_end_cursor,omitempty"`
}

func paginationPageWire(i PaginationPageV1Input) paginationPageWireV1 {
	items, _, _ := canonicalJSON(i.Items)
	return paginationPageWireV1{i.Ordinal, i.RequestedPage, i.RequestedCursor, snapshotWire(i.Response), i.RawBodySHA256, i.ResponseEvidence, i.EnvelopeEvidence, i.Items, digestBytes(items), i.RESTLinkHeader, i.RESTLinkObserved, i.GraphQLHasNextPage, i.GraphQLEndCursor}
}

type paginationResponseEnvelopeWireV1 struct {
	Schema             string                      `json:"schema"`
	Query              PaginationQueryV1           `json:"query"`
	RequestedPage      int                         `json:"requested_page,omitempty"`
	RequestedCursor    string                      `json:"requested_cursor,omitempty"`
	Response           identityWire                `json:"response"`
	RawBodySHA256      string                      `json:"raw_body_sha256"`
	Items              []CanonicalPaginationItemV1 `json:"items"`
	RESTLinkHeader     string                      `json:"rest_link_header"`
	RESTLinkObserved   bool                        `json:"rest_link_observed"`
	GraphQLHasNextPage *bool                       `json:"graphql_has_next_page,omitempty"`
	GraphQLEndCursor   string                      `json:"graphql_end_cursor,omitempty"`
}

func paginationResponseEnvelopeWire(i PaginationPageV1Input) paginationResponseEnvelopeWireV1 {
	return paginationResponseEnvelopeWireV1{
		GitHubPaginationEnvelopeSchemaV1, clonePaginationQuery(i.Query), i.RequestedPage, i.RequestedCursor,
		snapshotWire(i.Response), i.RawBodySHA256, i.Items, i.RESTLinkHeader, i.RESTLinkObserved,
		i.GraphQLHasNextPage, i.GraphQLEndCursor,
	}
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

type paginationBoundaryStatsV1 struct {
	Sources      int
	Pages        int
	Items        int
	ClosureBytes int64
}

// validatePaginationBoundaryV1 enforces the complete production-v1 source set
// and its cumulative closure budget at an authorization boundary.
func validatePaginationBoundaryV1(reviews, checkRuns, commitStatuses PaginationClosureV1, limits Limits) (paginationBoundaryStatsV1, error) {
	closures := []PaginationClosureV1{reviews, checkRuns, commitStatuses}
	if len(closures) != limits.RequiredPaginationSources {
		return paginationBoundaryStatsV1{}, errors.New("authorization boundary does not contain the required pagination source count")
	}
	expected := []PaginationSourceKind{PaginationReviews, PaginationCheckRuns, PaginationCommitStatuses}
	seen := make(map[PaginationSourceKind]struct{}, len(closures))
	stats := paginationBoundaryStatsV1{Sources: len(closures)}
	for index, closure := range closures {
		if !closure.valid() || requireLimitsSHA(limits, closure.limitsSHA) != nil ||
			closure.input.Query.Source != expected[index] || len(closure.canonical) > limits.MaxPaginationClosureBytes {
			return paginationBoundaryStatsV1{}, errors.New("authorization boundary pagination source is missing, reordered, or unbounded")
		}
		if _, exists := seen[closure.input.Query.Source]; exists {
			return paginationBoundaryStatsV1{}, errors.New("authorization boundary pagination source is duplicated")
		}
		seen[closure.input.Query.Source] = struct{}{}
		stats.Pages += len(closure.input.Pages)
		stats.ClosureBytes += int64(len(closure.canonical))
		for _, page := range closure.input.Pages {
			stats.Items += len(page.input.Items)
		}
	}
	if stats.ClosureBytes > int64(limits.MaxCumulativePaginationClosureBytes) {
		return paginationBoundaryStatsV1{}, errors.New("authorization boundary exceeds the cumulative pagination closure byte limit")
	}
	return stats, nil
}

func NewPaginationClosureV1(input PaginationClosureV1Input, limits Limits) (PaginationClosureV1, error) {
	input = clonePaginationClosureInput(input)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return PaginationClosureV1{}, err
	}
	if !input.Query.valid(limits) || len(input.Pages) == 0 || len(input.Pages) > limits.MaxPaginationPages {
		return PaginationClosureV1{}, errors.New("pagination query or page count is invalid")
	}
	all := map[string]struct{}{}
	requests := map[string]struct{}{}
	graphqlCursors := map[string]struct{}{}
	responseEvidence := make([]ledger.EvidenceRef, 0, len(input.Pages)*2)
	restLastPage := 0
	restLastObserved := false
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
		if !equalPaginationQuery(p.Query, input.Query) {
			return PaginationClosureV1{}, errors.New("pagination page response envelope changed its exact query identity")
		}
		if _, duplicated := requests[p.Response.RequestID()]; duplicated {
			return PaginationClosureV1{}, errors.New("pagination response request identity is duplicated")
		}
		requests[p.Response.RequestID()] = struct{}{}
		responseEvidence = append(responseEvidence, p.ResponseEvidence)
		responseEvidence = append(responseEvidence, p.EnvelopeEvidence)
		if p.Ordinal != index {
			return PaginationClosureV1{}, errors.New("pagination page ordinal is missing, repeated, or reordered")
		}
		total += len(p.Items)
		if total > limits.MaxObservedItemsPerPaginationSource {
			return PaginationClosureV1{}, errors.New("pagination closure item limit exceeded")
		}
		for _, item := range p.Items {
			if _, ok := all[item.Key]; ok {
				return PaginationClosureV1{}, errors.New("cross-page duplicate item key")
			}
			all[item.Key] = struct{}{}
		}
		terminal := index == len(input.Pages)-1
		if !terminal && len(p.Items) != input.Query.PerPage {
			return PaginationClosureV1{}, errors.New("pagination nonterminal page is short or empty")
		}
		if input.Query.Protocol == PaginationREST {
			if p.RequestedPage != index+1 || p.RequestedCursor != "" || p.GraphQLHasNextPage != nil || !p.RESTLinkObserved {
				return PaginationClosureV1{}, errors.New("REST page chain is inconsistent")
			}
			links, err := parseRESTLinks(p.RESTLinkHeader, input.Query, p.RequestedPage)
			if err != nil {
				return PaginationClosureV1{}, err
			}
			if links.hasLast {
				if restLastObserved && restLastPage != links.last {
					return PaginationClosureV1{}, errors.New("REST last relation changed across the page chain")
				}
				restLastPage, restLastObserved = links.last, true
			}
			if terminal && links.hasNext {
				return PaginationClosureV1{}, errors.New("REST terminal page still advertises next")
			}
			if !terminal && (!links.hasNext || links.next != index+2) {
				return PaginationClosureV1{}, errors.New("REST page chain skips or invents a page")
			}
			if terminal && restLastObserved && restLastPage != p.RequestedPage {
				return PaginationClosureV1{}, errors.New("REST page chain terminated before its advertised last page")
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
			if _, repeated := graphqlCursors[p.RequestedCursor]; repeated {
				return PaginationClosureV1{}, errors.New("GraphQL requested cursor is repeated")
			}
			graphqlCursors[p.RequestedCursor] = struct{}{}
			if terminal && *p.GraphQLHasNextPage {
				return PaginationClosureV1{}, errors.New("GraphQL terminal page still has next page")
			}
			if !terminal && (!*p.GraphQLHasNextPage || p.GraphQLEndCursor == "") {
				return PaginationClosureV1{}, errors.New("GraphQL nonterminal page lacks a next cursor")
			}
			if !terminal {
				if p.GraphQLEndCursor == p.RequestedCursor {
					return PaginationClosureV1{}, errors.New("GraphQL nonterminal page did not advance its cursor")
				}
				if _, repeated := graphqlCursors[p.GraphQLEndCursor]; repeated {
					return PaginationClosureV1{}, errors.New("GraphQL end cursor repeats an earlier cursor")
				}
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
	if err := limits.Validate(); err != nil {
		return PaginationClosureV1{}, err
	}
	if len(data) == 0 || len(data) > limits.MaxPaginationClosureBytes {
		return PaginationClosureV1{}, errors.New("pagination closure exceeds byte limit")
	}
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
			Query:   w.Query,
			Ordinal: pw.Ordinal, RequestedPage: pw.RequestedPage, RequestedCursor: pw.RequestedCursor,
			Response: response, RawBodySHA256: pw.RawBodySHA256, ResponseEvidence: pw.ResponseEvidence,
			EnvelopeEvidence: pw.EnvelopeEvidence,
			Items:            pw.Items, RESTLinkHeader: pw.RESTLinkHeader, RESTLinkObserved: pw.RESTLinkObserved,
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

type restPaginationLinks struct {
	next    int
	hasNext bool
	last    int
	hasLast bool
}

func parseRESTLinks(header string, q PaginationQueryV1, requestedPage int) (restPaginationLinks, error) {
	if len(header) == 0 {
		return restPaginationLinks{}, nil
	}
	relations := map[string]string{}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		pieces := strings.Split(part, ";")
		if len(pieces) != 2 || len(pieces[0]) < 3 || pieces[0][0] != '<' || pieces[0][len(pieces[0])-1] != '>' {
			return restPaginationLinks{}, errors.New("REST Link header is malformed or ambiguous")
		}
		rel := strings.TrimSpace(pieces[1])
		if !strings.HasPrefix(rel, "rel=\"") || !strings.HasSuffix(rel, "\"") {
			return restPaginationLinks{}, errors.New("REST Link relation is malformed")
		}
		name := strings.TrimSuffix(strings.TrimPrefix(rel, "rel=\""), "\"")
		if name != "next" && name != "prev" && name != "first" && name != "last" {
			return restPaginationLinks{}, errors.New("REST Link relation is unsupported")
		}
		if _, ok := relations[name]; ok {
			return restPaginationLinks{}, errors.New("REST Link relation is duplicated")
		}
		relations[name] = pieces[0][1 : len(pieces[0])-1]
	}
	pages := make(map[string]int, len(relations))
	for relation, raw := range relations {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host != "api.github.com" || u.User != nil || u.Fragment != "" ||
			u.Path != q.PathOrDocumentSHA256 || u.EscapedPath() != q.PathOrDocumentSHA256 {
			return restPaginationLinks{}, errors.New("REST Link relation changed endpoint identity")
		}
		values := u.Query()
		pageValues, perPageValues := values["page"], values["per_page"]
		if len(pageValues) != 1 || len(perPageValues) != 1 {
			return restPaginationLinks{}, errors.New("REST Link relation query identity changed")
		}
		page, err := strconv.Atoi(pageValues[0])
		if err != nil || page <= 0 {
			return restPaginationLinks{}, errors.New("REST Link relation page is invalid")
		}
		perPage, err := strconv.Atoi(perPageValues[0])
		if err != nil || perPage != q.PerPage || len(values) != len(q.Variables)+2 {
			return restPaginationLinks{}, errors.New("REST Link relation query identity changed")
		}
		for key, expected := range q.Variables {
			actual, ok := values[key]
			if !ok || len(actual) != 1 || actual[0] != expected {
				return restPaginationLinks{}, errors.New("REST Link relation query identity changed")
			}
		}
		pages[relation] = page
	}
	if page, ok := pages["first"]; ok && page != 1 {
		return restPaginationLinks{}, errors.New("REST first relation is contradictory")
	}
	if page, ok := pages["prev"]; ok && (requestedPage <= 1 || page != requestedPage-1) {
		return restPaginationLinks{}, errors.New("REST prev relation is contradictory")
	}
	next, hasNext := pages["next"]
	if hasNext && next != requestedPage+1 {
		return restPaginationLinks{}, errors.New("REST next relation is contradictory")
	}
	if last, ok := pages["last"]; ok && ((!hasNext && last != requestedPage) || (hasNext && last <= requestedPage)) {
		return restPaginationLinks{}, errors.New("REST last relation is contradictory")
	}
	last, hasLast := pages["last"]
	return restPaginationLinks{next: next, hasNext: hasNext, last: last, hasLast: hasLast}, nil
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
	i.Query = clonePaginationQuery(i.Query)
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

package cilifecycle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
)

// CollectRequest binds one stable retry identity to the exact governed GitHub
// lifecycle authority. AttemptID chooses a fresh observation only when the
// caller intentionally supplies a new value.
type CollectRequest struct {
	RunID     string
	AttemptID string
	Authority githublifecycle.Authority
}

type requestKeyWireV1 struct {
	SchemaVersion   int    `json:"schema_version"`
	RunID           string `json:"run_id"`
	AuthoritySHA256 string `json:"authority_sha256"`
	RepositoryOwner string `json:"repository_owner"`
	RepositoryName  string `json:"repository_name"`
	HeadBranch      string `json:"head_branch"`
	HeadSHA         string `json:"head_sha"`
	ActingKind      string `json:"acting_kind"`
	ActingSubject   string `json:"acting_subject"`
	LimitsSHA256    string `json:"limits_sha256"`
}

type attemptKeyWireV1 struct {
	RequestKey string `json:"request_key"`
	AttemptID  string `json:"attempt_id"`
}

type chainLinkWireV1 struct {
	Sequence               int    `json:"sequence"`
	RequestSHA256          string `json:"request_sha256"`
	ResponseEnvelopeSHA256 string `json:"response_envelope_sha256"`
}

type collectionIdentityWireV1 struct {
	SchemaVersion                     int               `json:"schema_version"`
	RepositoryOwner                   string            `json:"repository_owner"`
	RepositoryName                    string            `json:"repository_name"`
	HeadSHA                           string            `json:"head_sha"`
	NormalizedSemanticSweepSHA256     string            `json:"normalized_semantic_sweep_sha256"`
	OrderedRequestResponseDigestLinks []chainLinkWireV1 `json:"ordered_request_response_digests"`
}

type attemptIdentity struct {
	authoritySHA256 string
	attemptKey      string
	userID          int64
	owner           string
	repository      string
	branch          string
	headSHA         string
	actingKind      string
	actingSubject   string
}

func validateCollectRequest(request CollectRequest) (attemptIdentity, *CollectionError) {
	if !validText(request.RunID, MaxTextBytes) || !validOpaque(request.AttemptID, MaxAttemptIDBytes) {
		return attemptIdentity{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "invalid_collection_request"}
	}
	authorityJSON, err := request.Authority.CanonicalJSON()
	if err != nil || len(authorityJSON) == 0 {
		return attemptIdentity{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "invalid_authority"}
	}
	authoritySHA, err := request.Authority.SHA256()
	if err != nil || !validDigest(authoritySHA) {
		return attemptIdentity{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "invalid_authority"}
	}
	actor := request.Authority.Actor()
	userID, ok := parseCanonicalUserSubject(actor.Subject())
	if actor.Kind() != githublifecycle.ActingKindUser || actor.InstallationID() != 0 || !ok {
		return attemptIdentity{}, &CollectionError{Outcome: OutcomeUnsupportedPrincipal, FailureCode: "unsupported_principal"}
	}
	repository := request.Authority.Repository()
	identity := attemptIdentity{
		authoritySHA256: authoritySHA, userID: userID, owner: repository.Owner(), repository: repository.Name(),
		branch: request.Authority.HeadBranch().String(), headSHA: request.Authority.HeadSHA().String(),
		actingKind: string(actor.Kind()), actingSubject: actor.Subject(),
	}
	identity.attemptKey = deriveAttemptKey(request.RunID, request.AttemptID, authoritySHA, identity.owner, identity.repository,
		identity.branch, identity.headSHA, identity.actingKind, identity.actingSubject)
	return identity, nil
}

func deriveAttemptKey(runID, attemptID, authoritySHA, owner, repository, branch, headSHA, actingKind, actingSubject string) string {
	requestKey := digestJSON(requestKeyWireV1{
		SchemaVersionV1, runID, authoritySHA, owner, repository, branch, headSHA, actingKind, actingSubject, ProductionLimitsSHA256(),
	})
	return digestJSON(attemptKeyWireV1{requestKey, attemptID})
}

func parseCanonicalUserSubject(subject string) (int64, bool) {
	value, ok := strings.CutPrefix(subject, "github-user-id:")
	if !ok || value == "" || strings.HasPrefix(value, "+") || (len(value) > 1 && value[0] == '0') {
		return 0, false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

// collectRemote performs one bounded observation in memory. Task 3 wraps this
// operation in immutable attempt allocation, publication, ledger completion,
// and replay.
func (a *GitHubAdapter) collectRemote(ctx context.Context, request CollectRequest) (CIEvidenceBundleV1, error) {
	identity, requestErr := validateCollectRequest(request)
	if requestErr != nil {
		return CIEvidenceBundleV1{}, requestErr
	}
	if ctx == nil {
		return CIEvidenceBundleV1{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "missing_context"}
	}
	attemptStarted := a.now().UTC().UnixNano()
	if attemptStarted <= 0 {
		return CIEvidenceBundleV1{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "invalid_clock"}
	}
	collectionCtx, cancel := context.WithTimeout(ctx, a.limits.collectionTimeout)
	defer cancel()
	session := &requestSession{adapter: a}
	state := remoteCollectionState{request: request, identity: identity, attemptStarted: attemptStarted, session: session}

	principal, failed := session.principal(collectionCtx)
	if failed != nil {
		return state.finish(failed)
	}
	state.principal = principal
	if principal.ID != identity.userID {
		return state.finish(failure(OutcomeIntegrityFailure, "authenticated_user_mismatch", "principal", 0, nil))
	}
	state.collectionStarted = a.now().UTC().UnixNano()
	if state.collectionStarted < attemptStarted {
		return CIEvidenceBundleV1{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "clock_inversion"}
	}

	head, failed := session.head(collectionCtx, identity.owner, identity.repository, identity.branch, "H0")
	if failed != nil {
		return state.finish(failed)
	}
	state.heads = append(state.heads, head)
	if head.Input().SHA != identity.headSHA {
		return state.finish(failure(OutcomeStaleHead, "head_moved_h0", "H0", 0, nil))
	}

	sweepA, failed := session.sweep(collectionCtx, identity.owner, identity.repository, identity.headSHA, "a")
	if failed != nil {
		return state.finish(failed)
	}
	state.sweepA = &sweepA

	head, failed = session.head(collectionCtx, identity.owner, identity.repository, identity.branch, "H1")
	if failed != nil {
		return state.finish(failed)
	}
	state.heads = append(state.heads, head)
	if head.Input().SHA != identity.headSHA {
		return state.finish(failure(OutcomeStaleHead, "head_moved_h1", "H1", 0, nil))
	}

	sweepB, failed := session.sweep(collectionCtx, identity.owner, identity.repository, identity.headSHA, "b")
	if failed != nil {
		return state.finish(failed)
	}
	state.sweepB = &sweepB

	head, failed = session.head(collectionCtx, identity.owner, identity.repository, identity.branch, "H2")
	if failed != nil {
		return state.finish(failed)
	}
	state.heads = append(state.heads, head)
	if head.Input().SHA != identity.headSHA {
		return state.finish(failure(OutcomeStaleHead, "head_moved_h2", "H2", 0, nil))
	}
	state.collectionEnded = a.now().UTC().UnixNano()
	if state.collectionEnded < state.collectionStarted {
		return CIEvidenceBundleV1{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "clock_inversion"}
	}
	if sweepA.SHA256() != sweepB.SHA256() || !bytes.Equal(sweepA.CanonicalJSON(), sweepB.CanonicalJSON()) {
		return state.finish(failure(OutcomeUnstable, "semantic_sweeps_differ", "comparison", 0, nil))
	}
	return state.finish(nil)
}

type remoteCollectionState struct {
	request           CollectRequest
	identity          attemptIdentity
	attemptStarted    int64
	collectionStarted int64
	collectionEnded   int64
	principal         principalObservation
	heads             []HeadObservationV1
	sweepA            *CISemanticSweepV1
	sweepB            *CISemanticSweepV1
	session           *requestSession
}

func (s *remoteCollectionState) finish(failed *collectionFailure) (CIEvidenceBundleV1, error) {
	ended := s.session.adapter.now().UTC().UnixNano()
	provenance := s.session.requestProvenance()
	if ended < s.attemptStarted || !attemptContainsProvenance(s.attemptStarted, ended, provenance) {
		return CIEvidenceBundleV1{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "clock_inversion"}
	}
	if s.collectionStarted > 0 && s.collectionEnded == 0 {
		s.collectionEnded = ended
	}
	if s.collectionEnded > 0 && s.collectionEnded < s.collectionStarted {
		return CIEvidenceBundleV1{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "clock_inversion"}
	}
	outcome := OutcomeStable
	failureCode, failedPhase, failedPage := "", "", 0
	if failed != nil {
		outcome, failureCode, failedPhase, failedPage = failed.outcome, failed.code, failed.phase, failed.page
	}
	chainDigest, links, err := requestResponseChain(provenance)
	if err != nil {
		return CIEvidenceBundleV1{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "invalid_request_chain"}
	}
	firstResponse, lastResponse := responseRange(provenance)
	earliestProvider, latestProvider := providerTimestampRange(s.sweepA, s.sweepB)
	semanticA, semanticB, collectionIdentity := "", "", ""
	if s.sweepA != nil {
		semanticA = s.sweepA.SHA256()
	}
	if s.sweepB != nil {
		semanticB = s.sweepB.SHA256()
	}
	if outcome == OutcomeStable {
		collectionIdentity = collectionIdentitySHA256(s.identity.owner, s.identity.repository, s.identity.headSHA, semanticA, links)
	}
	input := CIEvidenceBundleV1Input{
		RunID: s.request.RunID, AttemptID: s.request.AttemptID, AttemptKeySHA256: s.identity.attemptKey,
		AuthoritySHA256: s.identity.authoritySHA256, LimitsSHA256: ProductionLimitsSHA256(),
		RepositoryOwner: s.identity.owner, RepositoryName: s.identity.repository, HeadBranch: s.identity.branch,
		HeadSHA: s.identity.headSHA, ActingKind: s.identity.actingKind, ActingSubject: s.identity.actingSubject,
		AuthenticatedID: s.principal.ID, AuthenticatedNode: s.principal.NodeID, AuthenticatedLogin: s.principal.Login,
		Outcome: outcome, FailureCode: failureCode, FailedPhase: failedPhase, FailedPage: failedPage,
		AttemptStartedUnixNano: s.attemptStarted, AttemptEndedUnixNano: ended,
		CollectionStartedUnixNano: s.collectionStarted, CollectionEndedUnixNano: s.collectionEnded,
		FirstResponseObservedUnixNano: firstResponse, LastResponseObservedUnixNano: lastResponse,
		EarliestProviderStateAt: earliestProvider, LatestProviderStateAt: latestProvider,
		HeadObservations: append([]HeadObservationV1(nil), s.heads...), SweepA: s.sweepA, SweepB: s.sweepB,
		SemanticDigestA: semanticA, SemanticDigestB: semanticB, RequestProvenance: provenance,
		RequestResponseChainSHA256: chainDigest, CollectionIdentitySHA256: collectionIdentity,
	}
	bundle, constructErr := NewCIEvidenceBundleV1(input)
	if constructErr != nil || len(bundle.CanonicalJSON()) > s.session.adapter.limits.maxBundleBytes {
		return CIEvidenceBundleV1{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "bundle_construction_failed"}
	}
	if failed != nil {
		return bundle, &CollectionError{
			Outcome: failed.outcome, FailureCode: failed.code, FailedPhase: failed.phase, FailedPage: failed.page,
			Cause: errors.New(failed.code),
		}
	}
	return bundle, nil
}

func attemptContainsProvenance(started, ended int64, provenance []RequestProvenanceV1) bool {
	for _, item := range provenance {
		input := item.Input()
		if input.RequestStartedUnixNano < started || input.ResponseObservedUnixNano > ended {
			return false
		}
	}
	return true
}

func requestResponseChain(provenance []RequestProvenanceV1) (string, []chainLinkWireV1, error) {
	links := make([]chainLinkWireV1, len(provenance))
	for i, item := range provenance {
		input := item.Input()
		if input.Sequence != i+1 || !validDigest(input.RequestSHA256) || !validDigest(input.ResponseEnvelopeSHA256) {
			return "", nil, errors.New("request provenance chain is invalid")
		}
		links[i] = chainLinkWireV1{input.Sequence, input.RequestSHA256, input.ResponseEnvelopeSHA256}
	}
	data, err := json.Marshal(links)
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), links, nil
}

func collectionIdentitySHA256(owner, repository, headSHA, semanticDigest string, links []chainLinkWireV1) string {
	return digestJSON(collectionIdentityWireV1{
		SchemaVersionV1, owner, repository, headSHA, semanticDigest, append([]chainLinkWireV1(nil), links...),
	})
}

func responseRange(provenance []RequestProvenanceV1) (int64, int64) {
	if len(provenance) == 0 {
		return 0, 0
	}
	first := provenance[0].Input().ResponseObservedUnixNano
	last := first
	for _, item := range provenance[1:] {
		observed := item.Input().ResponseObservedUnixNano
		if observed < first {
			first = observed
		}
		if observed > last {
			last = observed
		}
	}
	return first, last
}

func providerTimestampRange(sweeps ...*CISemanticSweepV1) (string, string) {
	var values []string
	for _, sweep := range sweeps {
		if sweep == nil {
			continue
		}
		input := sweep.Input()
		for _, suite := range input.CheckSuites {
			item := suite.Input()
			values = append(values, item.CreatedAt, item.UpdatedAt)
		}
		for _, run := range input.CheckRuns {
			item := run.Input()
			if item.StartedAt != nil {
				values = append(values, *item.StartedAt)
			}
			if item.CompletedAt != nil {
				values = append(values, *item.CompletedAt)
			}
		}
		for _, status := range input.CommitStatuses {
			item := status.Input()
			values = append(values, item.CreatedAt, item.UpdatedAt)
		}
	}
	if len(values) == 0 {
		return "", ""
	}
	earliest, latest := values[0], values[0]
	for _, value := range values[1:] {
		when, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			continue
		}
		first, _ := time.Parse(time.RFC3339Nano, earliest)
		last, _ := time.Parse(time.RFC3339Nano, latest)
		if when.Before(first) {
			earliest = value
		}
		if when.After(last) {
			latest = value
		}
	}
	return earliest, latest
}

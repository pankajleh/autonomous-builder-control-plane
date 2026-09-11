package githublifecycle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

func TestFinalRevalidationRejectsAdmissionObservationReuseAndDecisionForgery(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	final := f.sealed.Seal().input.FinalRevalidation
	input := final.Input()

	reused := input.PullRequest.Input()
	snapshot, err := NewSnapshotIdentity("github", f.prAuth.input.Snapshot.RequestID(), input.PullRequest.input.Snapshot.ObservedUnixNano())
	if err != nil {
		t.Fatal(err)
	}
	reused.Snapshot = snapshot
	reusedEnvelope, err := NewPullRequestEnvelopeEvidenceV1("evidence/pr-envelope-reused", reused, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	reused.EvidenceRefs = replaceEvidenceKind(reused.EvidenceRefs, GitHubPullRequestEnvelopeEvidenceKindV1, reusedEnvelope)
	reusedPR, err := NewAuthoritativePullRequestSnapshotV1(reused, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	input.PullRequest = reusedPR
	input.EvidenceRefs = replaceEvidenceKind(input.EvidenceRefs, GitHubPullRequestEnvelopeEvidenceKindV1, reusedEnvelope)
	if _, err := NewFinalRevalidationV1(input, f.limits); err == nil || !strings.Contains(err.Error(), "reused") {
		t.Fatalf("admission request identity reused as final evidence: %v", err)
	}

	forged := bytes.Replace(final.CanonicalJSON(), []byte(final.FinalDecisionSHA256()), []byte(strings.Repeat("0", 64)), 1)
	if _, err := ParseCanonicalFinalRevalidationV1(forged, f.mergeWrite, f.limits); err == nil {
		t.Fatal("forged final authorization decision digest accepted")
	}

	sealInput := f.sealed.Seal().Input()
	sealInput.FinalRevalidation.input.CurrentReadyProof.input.NoLaterTransition = false
	if _, err := NewAuthorizationSealV1(sealInput, f.limits); err == nil {
		t.Fatal("seal accepted a forged non-current READY proof")
	}
	readyProofInput := final.Input().CurrentReadyProof.Input()
	readyProofInput.ObservedLedgerIdentity = "replacement-ledger"
	if _, err := NewCurrentReadyProofV1(readyProofInput, f.limits); err == nil {
		t.Fatal("current READY proof accepted a replaced physical ledger identity")
	}

	readyInput := f.authority.ReadyBinding().Input()
	readyInput.ReadyRunStateSequence++
	readyInput.ReadyTransitionOrdinal++
	forgedSequence, err := NewReadyAuthorityBindingV1(readyInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	readyProofInput = final.Input().CurrentReadyProof.Input()
	readyProofInput.ReadyBinding = forgedSequence
	if _, err := NewCurrentReadyProofV1(readyProofInput, f.limits); err == nil {
		t.Fatal("current READY proof accepted a self-asserted ledger sequence and transition ordinal")
	}

	readyOnlyJSONL := append(append([]byte(nil), readyInput.ReadyEventJSON...), '\n')
	readyOnlyInput := f.authority.ReadyBinding().Input()
	readyOnlyInput.ReadyEventByteOffset = 0
	readyOnlyInput.ReadyRunStateSequence = 1
	readyOnlyInput.ReadyTransitionOrdinal = 1
	readyOnlyInput.LedgerPrefixLength = int64(len(readyOnlyJSONL))
	readyOnlyInput.LedgerPrefixSHA256 = digestBytes(readyOnlyJSONL)
	readyOnlyBinding, err := NewReadyAuthorityBindingV1(readyOnlyInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	readyProofInput = final.Input().CurrentReadyProof.Input()
	readyProofInput.ReadyBinding = readyOnlyBinding
	readyProofInput.ObservedBoundPrefixSHA256 = readyOnlyInput.LedgerPrefixSHA256
	readyProofInput.ObservedLedgerLength = int64(len(readyOnlyJSONL))
	readyProofInput.ObservedLedgerSHA256 = digestBytes(readyOnlyJSONL)
	readyProofInput.ObservedLedgerJSONL = readyOnlyJSONL
	readyProofInput.EvidenceRefs = []ledger.EvidenceRef{{URI: "evidence/current-ready-ledger-ready-only", Kind: CurrentReadyLedgerEvidenceKindV1, SHA256: readyProofInput.ObservedLedgerSHA256}}
	if _, err := NewCurrentReadyProofV1(readyProofInput, f.limits); err == nil {
		t.Fatal("current READY proof accepted a truncated run containing only the READY transition")
	}

	malformedLedger := final.Input().CurrentReadyProof.input.ObservedLedgerJSONL
	malformedLedger[0] = '['
	malformedReadyInput := f.authority.ReadyBinding().Input()
	malformedReadyInput.LedgerPrefixSHA256 = digestBytes(malformedLedger)
	malformedReady, err := NewReadyAuthorityBindingV1(malformedReadyInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	readyProofInput = final.Input().CurrentReadyProof.Input()
	readyProofInput.ReadyBinding = malformedReady
	readyProofInput.ObservedBoundPrefixSHA256 = malformedReadyInput.LedgerPrefixSHA256
	readyProofInput.ObservedLedgerJSONL = malformedLedger
	readyProofInput.ObservedLedgerSHA256 = digestBytes(malformedLedger)
	readyProofInput.EvidenceRefs = []ledger.EvidenceRef{{URI: "evidence/current-ready-ledger-malformed", Kind: CurrentReadyLedgerEvidenceKindV1, SHA256: readyProofInput.ObservedLedgerSHA256}}
	if _, err := NewCurrentReadyProofV1(readyProofInput, f.limits); err == nil {
		t.Fatal("current READY proof accepted an invalid JSONL prefix before the bound event")
	}

	readyProofInput = final.Input().CurrentReadyProof.Input()
	readyProofInput.ObservedLedgerSHA256 = strings.Repeat("f", 64)
	if _, err := NewCurrentReadyProofV1(readyProofInput, f.limits); err == nil {
		t.Fatal("current READY proof accepted a changed same-length ledger prefix")
	}

	later := ledger.Event{SchemaVersion: 1, EventID: "failed-event-1", Timestamp: time.Unix(1700000001, 1).UTC(), ProjectID: "project-1", PlanID: "plan-1", RunID: "run-1", AttemptID: "attempt-1", EventType: "STATE_TRANSITION", StateFrom: domain.StateReadyForMerge, StateTo: domain.StateFailed, Actor: "controller", Source: "merge-controller"}
	laterJSON, _ := json.Marshal(later)
	readyProofInput = final.Input().CurrentReadyProof.Input()
	readyProofInput.ObservedLedgerJSONL = append(readyProofInput.ObservedLedgerJSONL, laterJSON...)
	readyProofInput.ObservedLedgerJSONL = append(readyProofInput.ObservedLedgerJSONL, '\n')
	readyProofInput.ObservedLedgerLength = int64(len(readyProofInput.ObservedLedgerJSONL))
	readyProofInput.ObservedLedgerSHA256 = digestBytes(readyProofInput.ObservedLedgerJSONL)
	readyProofInput.EvidenceRefs = []ledger.EvidenceRef{{URI: "evidence/current-ready-ledger-later", Kind: CurrentReadyLedgerEvidenceKindV1, SHA256: readyProofInput.ObservedLedgerSHA256}}
	if _, err := NewCurrentReadyProofV1(readyProofInput, f.limits); err == nil {
		t.Fatal("current READY proof accepted a later transition for the bound run")
	}
}

func TestFinalRevalidationMustFollowAdmissionObservations(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	lateObservation := int64(1700000002600000000)
	lateReviews := emptyPaginationClosure(t, f, PaginationReviews, &f.pr, "request-reviews-late-admission", lateObservation)
	lateChecks := emptyPaginationClosure(t, f, PaginationCheckRuns, nil, "request-checks-late-admission", lateObservation)
	lateStatuses := emptyPaginationClosure(t, f, PaginationCommitStatuses, nil, "request-statuses-late-admission", lateObservation)
	latePRInput := f.prAuth.Input()
	latePRInput.Snapshot, _ = NewSnapshotIdentity("github", "request-pr-late-admission", lateObservation)
	latePRInput.ReviewsClosure = lateReviews
	lateEnvelope, err := NewPullRequestEnvelopeEvidenceV1("evidence/pr-envelope-late-admission", latePRInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	latePRInput.EvidenceRefs = replaceEvidenceKind(latePRInput.EvidenceRefs, GitHubPullRequestEnvelopeEvidenceKindV1, lateEnvelope)
	latePR, err := NewAuthoritativePullRequestSnapshotV1(latePRInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	lateMerge, err := NewMergeInput(MergeAuthorizationInputV1{
		Authority: f.authority, PolicyDecisionSHA256: f.mergeWrite.policyDecisionSHA256, InitialPullRequest: latePR,
		Checks: nil, CheckRunsClosure: lateChecks, CommitStatusesClosure: lateStatuses,
		Capability: f.mergeWrite.capability, Recipe: f.recipe, EvidenceRefs: f.mergeWrite.Evidence(),
	}, f.mergeWrite.attempt.WriteID(), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	finalInput := f.sealed.Seal().input.FinalRevalidation.Input()
	finalInput.MergeInput = lateMerge
	if _, err := NewFinalRevalidationV1(finalInput, f.limits); err == nil {
		t.Fatal("final revalidation accepted observations that preceded admission observations")
	}
}

func TestMergePolicyRejectsUnboundedOrMalformedDirectChecks(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	producer := StableIdentityV1{DatabaseID: 51, NodeID: "producer"}
	check := Check{
		NodeID: "bounded-check", Name: "build",
		Identity: TrustedCheckIdentityV1{Context: "build", Source: CheckSourceCheckRun, Producer: producer},
		Status:   CheckCompleted, Conclusion: ConclusionSuccess, HeadSHA: f.headSHA,
	}
	for index := 0; index <= f.limits.MaxEvidenceRefs; index++ {
		check.EvidenceRefs = append(check.EvidenceRefs, ledger.EvidenceRef{
			URI: fmt.Sprintf("evidence/check-%03d", index), Kind: "check", SHA256: fmt.Sprintf("%064x", index+1),
		})
	}
	runClosure := paginationClosureWithItems(t, f, PaginationCheckRuns, "", nil, []CanonicalPaginationItemV1{checkPaginationItem(check)})
	if err := EvaluateMergePolicyV1(f.authority, f.prAuth, []Check{check}, runClosure, f.statuses, f.limits); err == nil {
		t.Fatal("merge policy accepted a direct check with an unbounded evidence array")
	}

	check.EvidenceRefs = nil
	check.Status = CheckQueued
	check.Conclusion = ConclusionSuccess
	runClosure = paginationClosureWithItems(t, f, PaginationCheckRuns, "", nil, []CanonicalPaginationItemV1{checkPaginationItem(check)})
	if err := EvaluateMergePolicyV1(f.authority, f.prAuth, []Check{check}, runClosure, f.statuses, f.limits); err == nil {
		t.Fatal("merge policy accepted a non-completed check with a conclusion")
	}
}

func TestAuthoritativePRRequiresExactGitHubResponseEvidence(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	base := f.prAuth.Input()
	for name, mutate := range map[string]func(*AuthoritativePullRequestSnapshotV1Input){
		"missing body": func(input *AuthoritativePullRequestSnapshotV1Input) {
			input.EvidenceRefs = replaceEvidenceKind(input.EvidenceRefs, GitHubPullRequestResponseEvidenceKindV1, ledger.EvidenceRef{})
			input.EvidenceRefs = input.EvidenceRefs[:len(input.EvidenceRefs)-1]
		},
		"unbound request": func(input *AuthoritativePullRequestSnapshotV1Input) {
			input.Snapshot, _ = NewSnapshotIdentity("github", "forged-fresh-request", input.Snapshot.ObservedUnixNano())
		},
		"unbound response fields": func(input *AuthoritativePullRequestSnapshotV1Input) {
			input.PullRequestDatabaseID++
		},
		"unbound actor": func(input *AuthoritativePullRequestSnapshotV1Input) {
			input.Actor, _ = NewUserIdentity("different-authenticated-principal")
		},
		"wrong provider": func(input *AuthoritativePullRequestSnapshotV1Input) {
			input.Snapshot, _ = NewSnapshotIdentity("cache", input.Snapshot.RequestID(), input.Snapshot.ObservedUnixNano())
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			input.EvidenceRefs = append([]ledger.EvidenceRef(nil), base.EvidenceRefs...)
			mutate(&input)
			if _, err := NewAuthoritativePullRequestSnapshotV1(input, f.limits); err == nil {
				t.Fatal("authoritative PR accepted missing or unbound GitHub response evidence")
			}
		})
	}
}

func TestReadyAuthorityRejectsIncompleteClosureControllerAndLedgerForgery(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	ready := f.authority.ReadyBinding().Input()

	incomplete := ready
	incomplete.EvidenceClosureRefs = append([]ledger.EvidenceRef(nil), ready.ReadyEvidenceRefs...)
	if _, err := NewReadyAuthorityBindingV1(incomplete, f.limits); err == nil {
		t.Fatal("READY binding accepted incomplete accepted-source/configuration evidence closure")
	}

	wrongController := ready
	var event map[string]any
	if err := json.Unmarshal(wrongController.ReadyEventJSON, &event); err != nil {
		t.Fatal(err)
	}
	event["actor"] = "caller"
	wrongController.ReadyEventJSON, _ = json.Marshal(event)
	wrongController.ReadyEventSHA256 = digestBytes(wrongController.ReadyEventJSON)
	wrongController.LedgerPrefixLength = wrongController.ReadyEventByteOffset + int64(len(wrongController.ReadyEventJSON)) + 1
	if _, err := NewReadyAuthorityBindingV1(wrongController, f.limits); err == nil {
		t.Fatal("READY binding accepted a caller actor")
	}

	incoherent := ready
	incoherent.LedgerPrefixLength++
	if _, err := NewReadyAuthorityBindingV1(incoherent, f.limits); err == nil {
		t.Fatal("READY binding accepted incoherent event offset/prefix length")
	}
}

func TestPaginationRejectsSelfCertifiedSourcePathAndAmbiguousTerminal(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	wrong := f.checkRuns.Input()
	wrong.Query.Source = PaginationCommitStatuses
	wrong.Query.PathOrDocumentSHA256 = "/repos/octo-org/control-plane/commits/" + f.headSHA.String() + "/statuses"
	wrong.Query.Variables = map[string]string{}
	if _, err := NewPaginationClosureV1(wrong, f.limits); err == nil {
		t.Fatal("pagination closure transplanted response pages beneath another source query")
	}

	wrongPath := f.checkRuns.Input()
	wrongPath.Query.PathOrDocumentSHA256 = "/repos/octo-org/control-plane/check-runs"
	if _, err := NewPaginationClosureV1(wrongPath, f.limits); err == nil {
		t.Fatal("pagination closure transplanted response pages beneath another path")
	}

	transplanted := f.checkRuns.input.Pages[0].Input()
	transplanted.Query = f.statuses.Query()
	if _, err := NewPaginationPageV1(transplanted, f.limits); err == nil {
		t.Fatal("pagination page response evidence was rebound to another exact query")
	}

	ambiguousPageInput := f.checkRuns.input.Pages[0].Input()
	ambiguousPageInput.RESTLinkObserved = false
	envelopeEvidence, err := NewPaginationEnvelopeEvidenceV1("evidence/ambiguous-envelope", ambiguousPageInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	ambiguousPageInput.EnvelopeEvidence = envelopeEvidence
	ambiguousPage, err := NewPaginationPageV1(ambiguousPageInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	ambiguous := f.checkRuns.Input()
	ambiguous.Pages = []PaginationPageV1{ambiguousPage}
	if _, err := NewPaginationClosureV1(ambiguous, f.limits); err == nil {
		t.Fatal("empty terminal page without retained Link-header observation accepted")
	}

	duplicatePageInput := f.statuses.input.Pages[0].Input()
	duplicatePageInput.Response, _ = NewSnapshotIdentity("github", f.checkRuns.input.Pages[0].input.Response.RequestID(), duplicatePageInput.Response.ObservedUnixNano())
	duplicatePageInput.EnvelopeEvidence, err = NewPaginationEnvelopeEvidenceV1("evidence/duplicate-request-envelope", duplicatePageInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	duplicatePage, err := NewPaginationPageV1(duplicatePageInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	duplicateStatusesInput := f.statuses.Input()
	duplicateStatusesInput.Pages = []PaginationPageV1{duplicatePage}
	duplicateStatusesInput.EvidenceRefs = replaceEvidenceKind(duplicateStatusesInput.EvidenceRefs, GitHubPaginationEnvelopeEvidenceKindV1, duplicatePageInput.EnvelopeEvidence)
	duplicateStatuses, err := NewPaginationClosureV1(duplicateStatusesInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	merge := f.mergeWrite
	if _, err := NewMergeInput(MergeAuthorizationInputV1{
		Authority: merge.authority, PolicyDecisionSHA256: merge.policyDecisionSHA256, InitialPullRequest: merge.initialPullRequest,
		Checks: merge.checks, CheckRunsClosure: merge.checkRunsClosure, CommitStatusesClosure: duplicateStatuses,
		Capability: merge.capability, Recipe: merge.recipe, EvidenceRefs: merge.Evidence(),
	}, merge.attempt.WriteID(), f.limits); err == nil {
		t.Fatal("duplicate response request identity across admission sources accepted")
	}

	staleEnvelope := f.checkRuns.input.Pages[0].Input()
	staleEnvelope.RESTLinkHeader = "<https://api.github.com/repos/octo-org/control-plane/commits/" + f.headSHA.String() + "/check-runs?filter=all&page=2&per_page=100>; rel=\"next\""
	if _, err := NewPaginationPageV1(staleEnvelope, f.limits); err == nil {
		t.Fatal("pagination page accepted Link state not bound by its retained response envelope")
	}

	wrongProvider := f.checkRuns.input.Pages[0].Input()
	wrongProvider.Response, _ = NewSnapshotIdentity("cache", "cached-checks", wrongProvider.Response.ObservedUnixNano())
	wrongProvider.EnvelopeEvidence, _ = NewPaginationEnvelopeEvidenceV1("evidence/cached-envelope", wrongProvider, f.limits)
	if _, err := NewPaginationPageV1(wrongProvider, f.limits); err == nil {
		t.Fatal("pagination page accepted a non-GitHub response identity")
	}

	contradictory := f.checkRuns.input.Pages[0].Input()
	contradictory.RESTLinkHeader = "<https://api.github.com/repos/octo-org/control-plane/commits/" + f.headSHA.String() + "/check-runs?filter=all&page=99&per_page=100>; rel=\"last\""
	contradictory.EnvelopeEvidence, err = NewPaginationEnvelopeEvidenceV1("evidence/contradictory-envelope", contradictory, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	contradictoryPage, err := NewPaginationPageV1(contradictory, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	contradictoryClosure := f.checkRuns.Input()
	contradictoryClosure.Pages = []PaginationPageV1{contradictoryPage}
	contradictoryClosure.EvidenceRefs = replaceEvidence(contradictoryClosure.EvidenceRefs, f.checkRuns.input.Pages[0].input.EnvelopeEvidence, contradictory.EnvelopeEvidence)
	if _, err := NewPaginationClosureV1(contradictoryClosure, f.limits); err == nil {
		t.Fatal("terminal pagination closure accepted a contradictory last-page relation")
	}

	for name, link := range map[string]string{
		"userinfo": "<https://attacker@api.github.com/repos/octo-org/control-plane/commits/" + f.headSHA.String() + "/check-runs?filter=all&page=2&per_page=100>; rel=\"next\"",
		"fragment": "<https://api.github.com/repos/octo-org/control-plane/commits/" + f.headSHA.String() + "/check-runs?filter=all&page=2&per_page=100#unbound>; rel=\"next\"",
	} {
		t.Run("unsafe Link "+name, func(t *testing.T) {
			unsafe := f.checkRuns.input.Pages[0].Input()
			unsafe.RESTLinkHeader = link
			unsafe.EnvelopeEvidence, err = NewPaginationEnvelopeEvidenceV1("evidence/unsafe-link-envelope-"+name, unsafe, f.limits)
			if err != nil {
				t.Fatal(err)
			}
			page, err := NewPaginationPageV1(unsafe, f.limits)
			if err != nil {
				t.Fatal(err)
			}
			closure := f.checkRuns.Input()
			closure.Pages = []PaginationPageV1{page}
			closure.EvidenceRefs = replaceEvidence(closure.EvidenceRefs, f.checkRuns.input.Pages[0].input.EnvelopeEvidence, unsafe.EnvelopeEvidence)
			if _, err := NewPaginationClosureV1(closure, f.limits); err == nil {
				t.Fatal("pagination closure accepted an unsafe REST Link URL")
			}
		})
	}
}

func TestPaginationRejectsCrossPageLastShortAndRepeatedCursor(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	limits := f.limits
	limits.MaxPages = 3
	limits.MaxItemsPerPage = 1
	limits.MaxTotalItems = 3
	scope := PaginationQueryScopeV1{Source: PaginationCheckRuns, Repository: f.repository, RepositoryNodeID: "R_repo", HeadSHA: f.headSHA}
	query, err := DerivePaginationQueryV1(scope, limits)
	if err != nil {
		t.Fatal(err)
	}
	items := []CanonicalPaginationItemV1{
		{Key: "one", SHA256: strings.Repeat("1", 64)},
		{Key: "two", SHA256: strings.Repeat("2", 64)},
		{Key: "three", SHA256: strings.Repeat("3", 64)},
	}
	restPage := func(ordinal int, link string, pageItems []CanonicalPaginationItemV1) (PaginationPageV1, []ledger.EvidenceRef) {
		t.Helper()
		response, _ := NewSnapshotIdentity("github", fmt.Sprintf("cross-page-rest-%d", ordinal), 1700000004000000000+int64(ordinal))
		body := ledger.EvidenceRef{URI: fmt.Sprintf("evidence/cross-page-rest-body-%d", ordinal), Kind: GitHubPaginationBodyEvidenceKindV1, SHA256: strings.Repeat(fmt.Sprint(ordinal+4), 64)}
		input := PaginationPageV1Input{Query: query, Ordinal: ordinal, RequestedPage: ordinal + 1, Response: response, RawBodySHA256: body.SHA256, ResponseEvidence: body, Items: pageItems, RESTLinkHeader: link, RESTLinkObserved: true}
		input.EnvelopeEvidence, err = NewPaginationEnvelopeEvidenceV1(fmt.Sprintf("evidence/cross-page-rest-envelope-%d", ordinal), input, limits)
		if err != nil {
			t.Fatal(err)
		}
		page, err := NewPaginationPageV1(input, limits)
		if err != nil {
			t.Fatal(err)
		}
		return page, []ledger.EvidenceRef{body, input.EnvelopeEvidence}
	}
	baseURL := "https://api.github.com" + query.PathOrDocumentSHA256 + "?filter=all&per_page=1&page="
	first, firstEvidence := restPage(0, "<"+baseURL+"2>; rel=\"next\", <"+baseURL+"99>; rel=\"last\"", items[:1])
	second, secondEvidence := restPage(1, "", items[1:2])
	if _, err := NewPaginationClosureV1(PaginationClosureV1Input{Query: query, Pages: []PaginationPageV1{first, second}, EvidenceRefs: append(firstEvidence, secondEvidence...)}, limits); err == nil {
		t.Fatal("REST pagination terminated before an earlier advertised last page")
	}

	short, shortEvidence := restPage(0, "<"+baseURL+"2>; rel=\"next\"", nil)
	if _, err := NewPaginationClosureV1(PaginationClosureV1Input{Query: query, Pages: []PaginationPageV1{short, second}, EvidenceRefs: append(shortEvidence, secondEvidence...)}, limits); err == nil {
		t.Fatal("short or empty REST nonterminal page accepted")
	}

	graphqlQuery := clonePaginationQuery(query)
	graphqlQuery.Protocol = PaginationGraphQL
	graphqlQuery.Method = "POST"
	graphqlQuery.PathOrDocumentSHA256 = strings.Repeat("a", 64)
	graphqlPage := func(ordinal int, requested, end string, hasNext bool) (PaginationPageV1, []ledger.EvidenceRef) {
		t.Helper()
		response, _ := NewSnapshotIdentity("github", fmt.Sprintf("repeated-cursor-graphql-%d", ordinal), 1700000005000000000+int64(ordinal))
		body := ledger.EvidenceRef{URI: fmt.Sprintf("evidence/repeated-cursor-body-%d", ordinal), Kind: GitHubPaginationBodyEvidenceKindV1, SHA256: strings.Repeat(fmt.Sprint(ordinal+7), 64)}
		input := PaginationPageV1Input{Query: graphqlQuery, Ordinal: ordinal, RequestedCursor: requested, Response: response, RawBodySHA256: body.SHA256, ResponseEvidence: body, Items: items[ordinal : ordinal+1], GraphQLHasNextPage: &hasNext, GraphQLEndCursor: end}
		input.EnvelopeEvidence, err = NewPaginationEnvelopeEvidenceV1(fmt.Sprintf("evidence/repeated-cursor-envelope-%d", ordinal), input, limits)
		if err != nil {
			t.Fatal(err)
		}
		page, err := NewPaginationPageV1(input, limits)
		if err != nil {
			t.Fatal(err)
		}
		return page, []ledger.EvidenceRef{body, input.EnvelopeEvidence}
	}
	g1, g1Evidence := graphqlPage(0, "", "cursor-1", true)
	g2, g2Evidence := graphqlPage(1, "cursor-1", "cursor-1", true)
	g3, g3Evidence := graphqlPage(2, "cursor-1", "cursor-2", false)
	graphEvidence := append(append(g1Evidence, g2Evidence...), g3Evidence...)
	if _, err := NewPaginationClosureV1(PaginationClosureV1Input{Query: graphqlQuery, Pages: []PaginationPageV1{g1, g2, g3}, EvidenceRefs: graphEvidence}, limits); err == nil {
		t.Fatal("GraphQL pagination accepted a repeated nonterminal cursor")
	}
}

func replaceEvidenceKind(refs []ledger.EvidenceRef, kind string, replacement ledger.EvidenceRef) []ledger.EvidenceRef {
	result := make([]ledger.EvidenceRef, 0, len(refs)+1)
	for _, ref := range refs {
		if ref.Kind != kind {
			result = append(result, ref)
		}
	}
	return append(result, replacement)
}

func replaceEvidence(refs []ledger.EvidenceRef, old, replacement ledger.EvidenceRef) []ledger.EvidenceRef {
	result := append([]ledger.EvidenceRef(nil), refs...)
	for index := range result {
		if result[index] == old {
			result[index] = replacement
			return result
		}
	}
	return append(result, replacement)
}

func TestFinalRequestFreshnessCoversEverySourceAndCrossSourceDuplicate(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	base := f.sealed.Seal().input.FinalRevalidation.Input()
	admissionIDs := map[string]string{
		"pull request":    f.prAuth.input.Snapshot.RequestID(),
		"reviews":         f.prAuth.input.ReviewsClosure.input.Pages[0].input.Response.RequestID(),
		"check runs":      f.checkRuns.input.Pages[0].input.Response.RequestID(),
		"commit statuses": f.statuses.input.Pages[0].input.Response.RequestID(),
	}
	for source, requestID := range admissionIDs {
		t.Run("reused "+source, func(t *testing.T) {
			input := cloneFinalRevalidationInput(base)
			setFinalRequestID(&input, source, requestID)
			if err := requireFreshFinalRequests(input); err == nil {
				t.Fatal("final revalidation accepted an admission request identity")
			}
		})
	}

	finalIDs := map[string]string{
		"pull request":    base.PullRequest.input.Snapshot.RequestID(),
		"reviews":         base.PullRequest.input.ReviewsClosure.input.Pages[0].input.Response.RequestID(),
		"check runs":      base.CheckRunsClosure.input.Pages[0].input.Response.RequestID(),
		"commit statuses": base.CommitStatusesClosure.input.Pages[0].input.Response.RequestID(),
	}
	sources := []string{"pull request", "reviews", "check runs", "commit statuses"}
	for left := 0; left < len(sources); left++ {
		for right := left + 1; right < len(sources); right++ {
			t.Run("duplicate "+sources[left]+" with "+sources[right], func(t *testing.T) {
				input := cloneFinalRevalidationInput(base)
				setFinalRequestID(&input, sources[right], finalIDs[sources[left]])
				if err := requireFreshFinalRequests(input); err == nil {
					t.Fatal("final revalidation accepted a cross-source duplicate request identity")
				}
			})
		}
	}
}

func setFinalRequestID(input *FinalRevalidationV1Input, source, requestID string) {
	set := func(snapshot SnapshotIdentity) SnapshotIdentity {
		value, _ := NewSnapshotIdentity(snapshot.Provider(), requestID, snapshot.ObservedUnixNano())
		return value
	}
	switch source {
	case "pull request":
		input.PullRequest.input.Snapshot = set(input.PullRequest.input.Snapshot)
	case "reviews":
		input.PullRequest.input.ReviewsClosure.input.Pages[0].input.Response = set(input.PullRequest.input.ReviewsClosure.input.Pages[0].input.Response)
	case "check runs":
		input.CheckRunsClosure.input.Pages[0].input.Response = set(input.CheckRunsClosure.input.Pages[0].input.Response)
	case "commit statuses":
		input.CommitStatusesClosure.input.Pages[0].input.Response = set(input.CommitStatusesClosure.input.Pages[0].input.Response)
	}
}

func TestMergeInputCanonicalCheckOrderIncludesSourceIdentity(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	producer := StableIdentityV1{DatabaseID: 51, NodeID: "producer"}
	checks := []Check{
		{NodeID: "shared-node", Name: "build", Identity: TrustedCheckIdentityV1{Context: "build", Source: CheckSourceCheckRun, Producer: producer}, Status: CheckCompleted, Conclusion: ConclusionSuccess, HeadSHA: f.headSHA},
		{NodeID: "shared-node", Name: "status", Identity: TrustedCheckIdentityV1{Context: "status", Source: CheckSourceCommitStatus, Producer: producer}, Status: CheckCompleted, Conclusion: ConclusionSuccess, HeadSHA: f.headSHA},
	}
	runClosure := paginationClosureWithItems(t, f, PaginationCheckRuns, "", nil, []CanonicalPaginationItemV1{checkPaginationItem(checks[0])})
	statusClosure := paginationClosureWithItems(t, f, PaginationCommitStatuses, "", nil, []CanonicalPaginationItemV1{checkPaginationItem(checks[1])})
	base := MergeAuthorizationInputV1{Authority: f.authority, PolicyDecisionSHA256: f.mergeWrite.policyDecisionSHA256, InitialPullRequest: f.prAuth,
		Checks: checks, CheckRunsClosure: runClosure, CommitStatusesClosure: statusClosure, Capability: f.mergeWrite.capability, Recipe: f.recipe, EvidenceRefs: f.mergeWrite.Evidence()}
	first, err := NewMergeInput(base, f.mergeWrite.attempt.WriteID(), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	base.Checks = []Check{checks[1], checks[0]}
	second, err := NewMergeInput(base, f.mergeWrite.attempt.WriteID(), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256() != second.SHA256() || !bytes.Equal(first.CanonicalPayload(), second.CanonicalPayload()) {
		t.Fatal("cross-source checks with the same node ID produced caller-order-dependent authorization bytes")
	}
}

func checkPaginationItem(check Check) CanonicalPaginationItemV1 {
	raw, _ := json.Marshal(checkWire{check.NodeID, check.Name, check.Identity, check.Status, check.Conclusion, check.HeadSHA.String(), check.EvidenceRefs})
	return CanonicalPaginationItemV1{Key: check.NodeID, SHA256: digestBytes(raw)}
}

func TestRecipeIsPolicyDerivedObjectFormatConsistentAndMutationIdentityIsUnique(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	invalidUTF8Policy := f.authority.MergePolicy().Input()
	invalidUTF8Policy.Recipe.MessageTemplate = string([]byte{'M', 'e', 'r', 'g', 'e', ' ', 0xff})
	if _, err := NewMergePolicyV1(invalidUTF8Policy, f.limits); err == nil {
		t.Fatal("merge recipe policy accepted a commit message containing invalid UTF-8")
	}
	recipe := f.recipe.Input()
	if recipe.Message != "Merge authorized head\n\nABCP-Write-ID: "+f.mergeWrite.Attempt().WriteID() ||
		recipe.AuthorUnix != f.authority.ReadyBinding().input.ReadyEventUnixNano/1_000_000_000 ||
		recipe.CommitterUnix != recipe.AuthorUnix || recipe.Author != f.authority.MergePolicy().input.Recipe.Author ||
		recipe.Committer != f.authority.MergePolicy().input.Recipe.Committer {
		t.Fatal("merge recipe bytes were not wholly derived from policy, READY time, and write identity")
	}

	sha256PolicyInput := f.authority.MergePolicy().Input()
	sha256PolicyInput.Recipe.ObjectFormat = "sha256"
	sha256Policy, err := NewMergePolicyV1(sha256PolicyInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	sha256AuthorityInput := f.authority.Input()
	sha256AuthorityInput.MergePolicy = sha256Policy
	sha256Authority, err := NewAuthority(sha256AuthorityInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewMergeCommitRecipeV1("merge-write-1", sha256Authority, f.limits); err == nil {
		t.Fatal("SHA-256 recipe accepted 40-hex SHA-1 authority OIDs")
	}

	seal := f.sealed.Seal()
	first, err := NewTargetRefCommitmentV1(f.mergeWrite, seal, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewTargetRefCommitmentV1(f.mergeWrite, seal, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if first.clientMutationID != f.mergeWrite.Attempt().WriteID() || first.SHA256() != second.SHA256() ||
		!bytes.Equal(first.CanonicalJSON(), second.CanonicalJSON()) {
		t.Fatal("one seal did not deterministically produce one write-derived mutation identity")
	}
	forged := bytes.Replace(first.CanonicalJSON(), []byte(f.mergeWrite.Attempt().WriteID()), []byte("caller-mutation-id"), 1)
	if _, err := ParseCanonicalTargetRefCommitmentV1(forged, f.mergeWrite, seal, f.limits); err == nil {
		t.Fatal("caller-selected clientMutationID accepted")
	}
}

func TestNotAppliedProofRejectsForgedBindingAndAmbiguousZeroBytes(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	response, _ := NewSnapshotIdentity("github", "github-response-target-1", 1700000007000000000)
	responseBody := atomicRejectionResponseBody(t, 0)
	evidence := ledger.EvidenceRef{URI: "evidence/atomic-rejection", Kind: NotAppliedAtomicRejectionEvidenceKindV1, SHA256: digestBytes(responseBody)}
	targetSubmission := newTargetSubmission(t, f, "target-request-1")
	responseEnvelope := newTargetResponseEnvelope(t, f, targetSubmission, response.RequestID(), response.ObservedUnixNano(), 200, responseBody)
	proof, err := NewNotAppliedProofV1(NotAppliedProofV1Input{
		Kind: NotAppliedAtomicBaseRejected, RequestBytes: 200, ResponseEnvelope: &responseEnvelope, Response: &response, HTTPStatus: 200,
		ResponseBodySHA256: digestBytes(responseBody), ResponseBody: responseBody, EvidenceRef: evidence,
	}, f.sealed, targetSubmission, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCanonicalNotAppliedProofV1(proof.CanonicalJSON(), f.sealed, targetSubmission, f.limits); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCanonicalTargetSubmissionV1(targetSubmission.CanonicalJSON(), f.sealed, f.limits); err != nil {
		t.Fatal(err)
	}
	duplicateSubmission := newTargetSubmission(t, f, "target-request-duplicate")
	duplicateResponse, _ := NewSnapshotIdentity("github", "github-response-target-duplicate", response.ObservedUnixNano()+1)
	duplicateEnvelope := newTargetResponseEnvelope(t, f, duplicateSubmission, duplicateResponse.RequestID(), duplicateResponse.ObservedUnixNano(), 200, responseBody)
	duplicateProof, err := NewNotAppliedProofV1(NotAppliedProofV1Input{
		Kind: NotAppliedAtomicBaseRejected, RequestBytes: 200, ResponseEnvelope: &duplicateEnvelope, Response: &duplicateResponse, HTTPStatus: 200,
		ResponseBodySHA256: digestBytes(responseBody), ResponseBody: responseBody, EvidenceRef: evidence,
	}, f.sealed, duplicateSubmission, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateNotAppliedProofV1(f.sealed, targetSubmission, duplicateProof, f.limits); err == nil {
		t.Fatal("rejection from a duplicate request accepted for the original target submission")
	}
	for name, forged := range map[string][]byte{
		"repository":  bytes.Replace(proof.CanonicalJSON(), []byte("octo-org"), []byte("evil-org"), 1),
		"predicate":   bytes.Replace(proof.CanonicalJSON(), []byte("ref_updates[0].before_oid_mismatch"), []byte("ref_updates[1].before_oid_mismatch"), 1),
		"disposition": bytes.Replace(proof.CanonicalJSON(), []byte(NotAppliedAllOrNothingDispositionV1), []byte("collector_says_not_applied"), 1),
		"raw digest":  bytes.Replace(proof.CanonicalJSON(), []byte(evidence.SHA256), []byte(strings.Repeat("c", 64)), 1),
	} {
		if _, err := ParseCanonicalNotAppliedProofV1(forged, f.sealed, targetSubmission, f.limits); err == nil {
			t.Fatalf("forged NOT_APPLIED %s accepted", name)
		}
	}
	for name, mutate := range map[string]func(*NotAppliedProofV1Input){
		"request identity": func(input *NotAppliedProofV1Input) {
			other, _ := NewSnapshotIdentity("github", "unrelated-response", input.Response.ObservedUnixNano())
			input.Response = &other
		},
		"response evidence":  func(input *NotAppliedProofV1Input) { input.EvidenceRef.SHA256 = strings.Repeat("f", 64) },
		"rejected predicate": func(input *NotAppliedProofV1Input) { input.Kind = NotAppliedAtomicHeadRejected },
		"response body": func(input *NotAppliedProofV1Input) {
			input.ResponseBody = []byte(`{"data":{"updateRefs":null},"errors":[]}`)
			input.ResponseBodySHA256 = digestBytes(input.ResponseBody)
			input.EvidenceRef.SHA256 = input.ResponseBodySHA256
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := proof.Input()
			mutate(&input)
			if _, err := NewNotAppliedProofV1(input, f.sealed, targetSubmission, f.limits); err == nil {
				t.Fatal("NOT_APPLIED accepted an unrelated or self-certified rejection response")
			}
		})
	}

	zeroEvidence := ledger.EvidenceRef{URI: "evidence/zero-bytes", Kind: NotAppliedZeroByteEvidenceKindV1, SHA256: strings.Repeat("d", 64)}
	zeroSubmission := newTargetSubmission(t, f, "target-request-zero")
	if _, err := NewNotAppliedProofV1(NotAppliedProofV1Input{Kind: NotAppliedZeroRequestBytes, Response: &response, EvidenceRef: zeroEvidence}, f.sealed, zeroSubmission, f.limits); err == nil {
		t.Fatal("zero-byte proof accepted a provider response")
	}
	if _, err := NewNotAppliedProofV1(NotAppliedProofV1Input{Kind: NotAppliedZeroRequestBytes, EvidenceRef: zeroEvidence}, f.sealed, zeroSubmission, f.limits); err != nil {
		t.Fatalf("strict zero-byte proof rejected: %v", err)
	}
}

func atomicRejectionResponseBody(t *testing.T, refUpdateIndex int) []byte {
	t.Helper()
	response := atomicRejectionResponseV1{}
	response.Data.UpdateRefs = json.RawMessage("null")
	response.Errors = make([]struct {
		Type       string   `json:"type"`
		Path       []string `json:"path"`
		Extensions struct {
			Code           string `json:"code"`
			RefUpdateIndex int    `json:"ref_update_index"`
		} `json:"extensions"`
	}, 1)
	response.Errors[0].Type = "FAILED_PRECONDITION"
	response.Errors[0].Path = []string{"updateRefs", "refUpdates", "beforeOid"}
	response.Errors[0].Extensions.Code = NotAppliedBeforeOIDMismatchCodeV1
	response.Errors[0].Extensions.RefUpdateIndex = refUpdateIndex
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

type cancellationTestCase struct {
	input    CancellationAuthorityV1Input
	expected CancellationAuthorityExpectationV1
	durable  DurableCancellationAuthorityV1
}

func newCancellationTestCase(t *testing.T, f fixture, boundary CancellationBoundaryV1, proof CancellationSubmissionProofV1) cancellationTestCase {
	t.Helper()
	ready := f.authority.ReadyBinding()
	suffix := string(boundary) + "-" + string(proof.input.Kind)
	authRef := ledger.EvidenceRef{URI: "evidence/auth-" + suffix, Kind: "authentication", SHA256: strings.Repeat("1", 64)}
	policyRef := ledger.EvidenceRef{URI: "evidence/policy-" + suffix, Kind: "cancellation-policy", SHA256: strings.Repeat("2", 64)}
	requestRef := ledger.EvidenceRef{URI: "evidence/request-" + suffix, Kind: "cancellation-request", SHA256: strings.Repeat("3", 64)}
	evidence := []ledger.EvidenceRef{authRef, policyRef, requestRef, proof.input.EvidenceRef}
	if proof.input.NotAppliedProof != nil && proof.input.NotAppliedProof.input.ResponseEnvelope != nil {
		envelope := proof.input.NotAppliedProof.input.ResponseEnvelope
		evidence = append(evidence, envelope.input.BodyEvidence, envelope.EvidenceRef())
	}
	evidence = append(evidence, f.readyProof.input.EvidenceRefs...)
	input := CancellationAuthorityV1Input{
		ProjectID: ready.input.ProjectID, PlanID: ready.input.PlanID, RunID: ready.input.RunID,
		RepositoryBindingSHA256: ready.RepositoryBinding().SHA256(), Phase3AuthoritySHA256: ready.input.Phase3AuthoritySHA256,
		ReadyEventSHA256: ready.input.ReadyEventSHA256, ReadyEventID: ready.input.ReadyEventID, ReadyRunStateSequence: ready.input.ReadyRunStateSequence,
		ReadyBindingSHA256: ready.SHA256(), LedgerPrefixSHA256: ready.input.LedgerPrefixSHA256, LedgerPrefixLength: ready.input.LedgerPrefixLength,
		CurrentReadyProof: f.readyProof, Boundary: boundary, ReceiptUnixNano: f.readyProof.input.ObservedUnixNano + 1_000_000_000, IngressSequence: 12,
		AdmissionSHA256: proof.input.AdmissionSHA256, Attempt: proof.input.Attempt, SealSHA256: proof.input.SealSHA256,
		CommitmentSHA256: proof.input.CommitmentSHA256, SubmissionProof: proof,
		Requester: StablePrincipalV1{Kind: "user", Identity: StableIdentityV1{DatabaseID: 71, NodeID: "cancel-user"}}, AuthenticationEvidence: authRef,
		CancellationPolicyVersion: "cancel-v1", CancellationPolicySource: policyRef, CancellationPolicySHA256: strings.Repeat("6", 64),
		ScopedGrantSHA256: strings.Repeat("7", 64), AllowDecisionSHA256: strings.Repeat("8", 64), SourceRequestID: "request-" + suffix,
		SourceKind: "api", RequestEvidence: requestRef, IngressID: "ingress-" + suffix, EvidenceRefs: evidence,
	}
	authority, err := NewCancellationAuthorityV1(input, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	expected := CancellationAuthorityExpectationV1{
		ProjectID: input.ProjectID, PlanID: input.PlanID, RunID: input.RunID, ReadyBinding: ready, CurrentReadyProof: f.readyProof,
		AdmissionSHA256: input.AdmissionSHA256, Requester: input.Requester, AuthenticationEvidence: input.AuthenticationEvidence,
		CancellationPolicyVersion: input.CancellationPolicyVersion, CancellationPolicySource: input.CancellationPolicySource,
		CancellationPolicySHA256: input.CancellationPolicySHA256, ScopedGrantSHA256: input.ScopedGrantSHA256,
		AllowDecisionSHA256: input.AllowDecisionSHA256, Boundary: input.Boundary, Attempt: input.Attempt, SealSHA256: input.SealSHA256,
		CommitmentSHA256: input.CommitmentSHA256, SubmissionProof: proof, SourceRequestID: input.SourceRequestID, SourceKind: input.SourceKind,
		RequestEvidence: input.RequestEvidence, IngressID: input.IngressID, ReceiptUnixNano: input.ReceiptUnixNano,
		IngressSequence: input.IngressSequence, EvidenceRefs: input.EvidenceRefs,
	}
	if input.SealSHA256 != "" {
		expected.SealedAuthorization = f.sealed
	}
	replayRef := ledger.EvidenceRef{URI: "evidence/replay-" + suffix, Kind: "replay-index", SHA256: strings.Repeat("9", 64)}
	replay, err := NewCancellationReplayIdentityV1(authority, replayRef, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	channelRef := ledger.EvidenceRef{URI: "evidence/channel-" + suffix, Kind: "durable-channel", SHA256: strings.Repeat("a", 64)}
	durable, err := NewDurableCancellationAuthorityV1(authority, channelRef, replay, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	return cancellationTestCase{input, expected, durable}
}

func TestCancellationBoundaryMatrixAndSealedZeroByteProof(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	attempt := f.mergeWrite.Attempt()

	preEvidence := ledger.EvidenceRef{URI: "evidence/no-admission", Kind: "no-admission", SHA256: strings.Repeat("b", 64)}
	preProof, err := NewCancellationSubmissionProofV1(CancellationSubmissionProofV1Input{Kind: CancellationProofNoAdmission, SubmissionState: ReconciliationNotApplied, EvidenceRef: preEvidence}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	pre := newCancellationTestCase(t, f, CancellationPreAdmission, preProof)
	if err := AuthorizeCancelledV1(pre.durable, pre.expected, ReconciliationNotApplied, f.limits); err != nil {
		t.Fatal(err)
	}
	if err := AuthorizeCancelledV1(pre.durable, pre.expected, ReconciliationUnknown, f.limits); err == nil {
		t.Fatal("pre-admission cancellation accepted an UNKNOWN current disposition")
	}

	admittedEvidence := ledger.EvidenceRef{URI: "evidence/admitted-zero", Kind: "zero-request", SHA256: strings.Repeat("c", 64)}
	admittedProof, err := NewCancellationSubmissionProofV1(CancellationSubmissionProofV1Input{Kind: CancellationProofZeroRequestBytes, AdmissionSHA256: f.mergeWrite.SHA256(), Attempt: &attempt, SubmissionState: ReconciliationNotApplied, EvidenceRef: admittedEvidence}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	admitted := newCancellationTestCase(t, f, CancellationAdmittedPreTargetSubmission, admittedProof)
	if err := AuthorizeCancelledV1(admitted.durable, admitted.expected, ReconciliationNotApplied, f.limits); err != nil {
		t.Fatal(err)
	}
	forgedAttempt := attempt
	forgedAttempt.readyBindingSHA256 = strings.Repeat("0", 64)
	forgedAdmissionProof, err := NewCancellationSubmissionProofV1(CancellationSubmissionProofV1Input{Kind: CancellationProofZeroRequestBytes, AdmissionSHA256: f.mergeWrite.SHA256(), Attempt: &forgedAttempt, SubmissionState: ReconciliationNotApplied, EvidenceRef: admittedEvidence}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	forgedInput := admitted.input
	forgedInput.Attempt = &forgedAttempt
	forgedInput.SubmissionProof = forgedAdmissionProof
	if _, err := NewCancellationAuthorityV1(forgedInput, f.limits); err == nil {
		t.Fatal("cancellation accepted an admission attempt from a different READY authority")
	}

	unknownEvidence := ledger.EvidenceRef{URI: "evidence/unresolved", Kind: "unresolved-submission", SHA256: strings.Repeat("d", 64)}
	unknownSubmission := newTargetSubmission(t, f, "unknown-target-request")
	unknownProof, err := NewCancellationSubmissionProofV1(CancellationSubmissionProofV1Input{Kind: CancellationProofUnresolvedSubmission, AdmissionSHA256: f.mergeWrite.SHA256(), Attempt: &attempt, SealSHA256: f.sealed.Seal().SHA256(), CommitmentSHA256: f.sealed.Commitment().SHA256(), TargetSubmission: &unknownSubmission, RequestBytes: 100, SubmissionState: ReconciliationUnknown, EvidenceRef: unknownEvidence}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	unknown := newCancellationTestCase(t, f, CancellationTargetSubmissionUnknown, unknownProof)
	if err := AuthorizeCancelledV1(unknown.durable, unknown.expected, ReconciliationUnknown, f.limits); err == nil {
		t.Fatal("unresolved target submission selected CANCELLED before NOT_APPLIED proof")
	}
	responseBody := atomicRejectionResponseBody(t, 0)
	response, _ := NewSnapshotIdentity("github", "github-response-unknown-target", f.sealed.input.Seal.input.FinalRevalidation.input.CompletedUnixNano+1)
	atomicEvidence := ledger.EvidenceRef{URI: "evidence/unknown-reconciled", Kind: NotAppliedAtomicRejectionEvidenceKindV1, SHA256: digestBytes(responseBody)}
	responseEnvelope := newTargetResponseEnvelope(t, f, unknownSubmission, response.RequestID(), response.ObservedUnixNano(), 200, responseBody)
	atomicProof, err := NewNotAppliedProofV1(NotAppliedProofV1Input{Kind: NotAppliedAtomicBaseRejected, RequestBytes: 100, ResponseEnvelope: &responseEnvelope, Response: &response, HTTPStatus: 200, ResponseBodySHA256: digestBytes(responseBody), ResponseBody: responseBody, EvidenceRef: atomicEvidence}, f.sealed, unknownSubmission, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := AuthorizeCancelledV1(unknown.durable, unknown.expected, ReconciliationNotApplied, f.limits, atomicProof); err != nil {
		t.Fatal(err)
	}
	otherSubmission := newTargetSubmission(t, f, "other-target-request")
	otherResponse, _ := NewSnapshotIdentity("github", "github-response-other-target", response.ObservedUnixNano()+1)
	otherEnvelope := newTargetResponseEnvelope(t, f, otherSubmission, otherResponse.RequestID(), otherResponse.ObservedUnixNano(), 200, responseBody)
	otherProof, err := NewNotAppliedProofV1(NotAppliedProofV1Input{Kind: NotAppliedAtomicBaseRejected, RequestBytes: 100, ResponseEnvelope: &otherEnvelope, Response: &otherResponse, HTTPStatus: 200, ResponseBodySHA256: digestBytes(responseBody), ResponseBody: responseBody, EvidenceRef: atomicEvidence}, f.sealed, otherSubmission, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := AuthorizeCancelledV1(unknown.durable, unknown.expected, ReconciliationNotApplied, f.limits, otherProof); err == nil {
		t.Fatal("unknown-submission cancellation accepted NOT_APPLIED proof from a different request")
	}

	zeroEvidence := ledger.EvidenceRef{URI: "evidence/sealed-zero", Kind: NotAppliedZeroByteEvidenceKindV1, SHA256: strings.Repeat("e", 64)}
	zeroSubmission := newTargetSubmission(t, f, "sealed-zero-target")
	zeroProof, err := NewNotAppliedProofV1(NotAppliedProofV1Input{Kind: NotAppliedZeroRequestBytes, EvidenceRef: zeroEvidence}, f.sealed, zeroSubmission, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	sealedZeroProof, err := NewCancellationSubmissionProofV1(CancellationSubmissionProofV1Input{Kind: CancellationProofSealedZeroRequestBytes, AdmissionSHA256: f.mergeWrite.SHA256(), Attempt: &attempt, SealSHA256: f.sealed.Seal().SHA256(), CommitmentSHA256: f.sealed.Commitment().SHA256(), TargetSubmission: &zeroSubmission, SubmissionState: ReconciliationNotApplied, NotAppliedProof: &zeroProof, EvidenceRef: zeroEvidence}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	sealedZero := newCancellationTestCase(t, f, CancellationTargetNotApplied, sealedZeroProof)
	if err := AuthorizeCancelledV1(sealedZero.durable, sealedZero.expected, ReconciliationNotApplied, f.limits, zeroProof); err != nil {
		t.Fatal(err)
	}
	wrongLimitsProof := cloneNotAppliedProof(zeroProof)
	wrongLimitsProof.limitsSHA = strings.Repeat("0", 64)
	if _, err := NewCancellationSubmissionProofV1(CancellationSubmissionProofV1Input{Kind: CancellationProofSealedZeroRequestBytes, AdmissionSHA256: f.mergeWrite.SHA256(), Attempt: &attempt, SealSHA256: f.sealed.Seal().SHA256(), CommitmentSHA256: f.sealed.Commitment().SHA256(), TargetSubmission: &zeroSubmission, SubmissionState: ReconciliationNotApplied, NotAppliedProof: &wrongLimitsProof, EvidenceRef: zeroEvidence}, f.limits); err == nil {
		t.Fatal("sealed zero-byte cancellation accepted a nested proof from a different limits policy")
	}
}

func TestGenericSquashAndRebaseRemainRepresentableWithoutProductionAuthority(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	evidence := []ledger.EvidenceRef{{URI: "evidence/generic-strategy", Kind: "generic-strategy", SHA256: strings.Repeat("e", 64)}}
	resultSHA, _ := NewGitSHA(strings.Repeat("6", 40))

	for _, method := range []MergeMethod{MergeMethodSquash, MergeMethodRebase} {
		lineage := []CommitLineage{{SourceSHA: f.headSHA, SourceTree: f.headTree, ResultSHA: resultSHA, ResultTree: f.headTree, Parents: []GitSHA{f.baseSHA}}}
		result, err := NewGenericStrategyResultV1(GenericStrategyResultV1Input{
			Snapshot: f.snapshot, Repository: f.repository, PullRequest: f.pr, BaseBranch: f.base,
			AcceptedHeadSHA: f.headSHA, AcceptedHeadTree: f.headTree, BaseBeforeSHA: f.baseSHA, Method: method,
			ResultSHA: resultSHA, ResultTree: f.headTree, Parents: []GitSHA{f.baseSHA}, Lineage: lineage, EvidenceRefs: evidence,
		}, f.limits)
		if err != nil {
			t.Fatalf("generic %s result was not representable: %v", method, err)
		}
		containment, err := NewTargetContainmentProofV1(TargetContainmentProofV1Input{Snapshot: f.snapshot, Repository: f.repository, TargetRef: "refs/heads/" + f.base.String(), ResultSHA: resultSHA, ObservedTargetTipSHA: resultSHA, Mechanism: GitHubCompareProofV1, Status: TargetContainmentIdentical, MergeBaseSHA: resultSHA, EvidenceRefs: evidence}, f.limits)
		if err != nil {
			t.Fatal(err)
		}
		post, err := NewGenericStrategyPostMergeV1(GenericStrategyPostMergeV1Input{Result: result, Snapshot: f.snapshot, ObservedTargetTipSHA: resultSHA, ContainmentProof: containment, EvidenceRefs: evidence}, f.limits)
		if err != nil || post.SHA256() == "" {
			t.Fatalf("generic %s post-merge proof was not representable: %v", method, err)
		}
		otherSnapshot, _ := NewSnapshotIdentity("github", "unrelated-generic-observation", f.snapshot.ObservedUnixNano()+1)
		if _, err := NewGenericStrategyPostMergeV1(GenericStrategyPostMergeV1Input{Result: result, Snapshot: otherSnapshot, ObservedTargetTipSHA: resultSHA, ContainmentProof: containment, EvidenceRefs: evidence}, f.limits); err == nil {
			t.Fatalf("generic %s post-merge accepted containment from an unrelated snapshot", method)
		}
	}

	unsupported := f.authority.MergePolicy().Input()
	unsupported.Method = MergeMethodSquash
	if _, err := NewMergePolicyV1(unsupported, f.limits); err == nil {
		t.Fatal("generic squash representation widened production-v1 policy")
	}
}

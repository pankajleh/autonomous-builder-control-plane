package githublifecycle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

func TestProductionLimitsUseStageSpecificNumericBudgets(t *testing.T) {
	limits := DefaultLimits()
	values := []struct {
		name string
		got  int64
		want int64
	}{
		{"required trusted checks", int64(limits.MaxRequiredTrustedChecks), 64},
		{"eligible reviewers", int64(limits.MaxEligibleReviewers), 64},
		{"required reviewers", int64(limits.MaxRequiredReviewers), 64},
		{"minimum approvals", int64(limits.MaxMinimumApprovals), 64},
		{"observed checks", int64(limits.MaxObservedChecks), 500},
		{"observed reviews", int64(limits.MaxObservedReviews), 500},
		{"pagination pages", int64(limits.MaxPaginationPages), 10},
		{"pagination items per page", int64(limits.MaxPaginationItemsPerPage), 100},
		{"pagination items per source", int64(limits.MaxObservedItemsPerPaginationSource), 500},
		{"pagination sources", int64(limits.RequiredPaginationSources), 3},
		{"one pagination closure", int64(limits.MaxPaginationClosureBytes), 1 * 1024 * 1024},
		{"three pagination closures", int64(limits.MaxCumulativePaginationClosureBytes), 3 * 1024 * 1024},
		{"general evidence refs", int64(limits.MaxEvidenceRefs), 64},
		{"metadata items", int64(limits.MaxMetadataItems), 32},
		{"READY closure refs", int64(limits.MaxReadyEvidenceClosureRefs), 256},
		{"canonical object", int64(limits.MaxCanonicalObjectBytes), 256 * 1024},
		{"cancellation authority", int64(limits.MaxCancellationAuthorityBytes), 256 * 1024},
		{"READY ledger snapshot", int64(limits.MaxReadyLedgerSnapshotBytes), 64 * 1024 * 1024},
		{"ledger scan records", int64(limits.MaxLedgerScanRecords), 262144},
		{"request body", int64(limits.MaxRequestBodyBytes), 16 * 1024},
		{"decompressed response body", int64(limits.MaxDecompressedResponseBodyBytes), 4 * 1024 * 1024},
		{"target response envelope", int64(limits.MaxTargetResponseEnvelopeBytes), 4*1024*1024 + 64*1024},
	}
	for _, value := range values {
		t.Run(value.name, func(t *testing.T) {
			if value.got != value.want {
				t.Fatalf("got %d, want %d", value.got, value.want)
			}
		})
	}
	baseSHA, err := limits.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	changed := limits
	changed.MaxTargetResponseEnvelopeBytes++
	changedSHA, err := changed.SHA256()
	if err != nil || changedSHA == baseSHA {
		t.Fatalf("target response envelope limit is absent from canonical limits identity: %v", err)
	}
}

func TestPolicyAndObservationCardinalityBoundariesAreIndependent(t *testing.T) {
	f := newFixture(t, MergeMethodSquash)
	limits := f.limits
	cases := []struct {
		name  string
		limit int
		build func(int) error
	}{
		{
			name: "required checks", limit: limits.MaxRequiredTrustedChecks,
			build: func(count int) error {
				input := f.authority.MergePolicy().Input()
				input.RequiredChecks = makeTrustedCheckIdentities(count)
				_, err := NewMergePolicyV1(input, limits)
				return err
			},
		},
		{
			name: "eligible reviewers", limit: limits.MaxEligibleReviewers,
			build: func(count int) error {
				input := f.authority.MergePolicy().Input()
				input.EligibleReviewers = makeStableIdentities(count)
				_, err := NewMergePolicyV1(input, limits)
				return err
			},
		},
		{
			name: "required reviewers", limit: limits.MaxRequiredReviewers,
			build: func(count int) error {
				input := f.authority.MergePolicy().Input()
				input.EligibleReviewers = makeStableIdentities(count)
				input.RequiredReviewers = makeStableIdentities(count)
				input.MinimumApprovals = count
				_, err := NewMergePolicyV1(input, limits)
				return err
			},
		},
		{
			name: "observed checks", limit: limits.MaxObservedChecks,
			build: func(count int) error {
				_, err := NewCISnapshot(CISnapshotInput{
					Snapshot: f.snapshot, Repository: f.repository, HeadSHA: f.headSHA,
					Checks: makeObservedChecks(count, f.headSHA),
				}, limits)
				return err
			},
		},
		{
			name: "observed reviews", limit: limits.MaxObservedReviews,
			build: func(count int) error {
				input := f.prInput()
				input.Reviews = makeObservedReviews(count, f.headSHA)
				_, err := NewPullRequestSnapshot(input, limits)
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.build(tc.limit); err != nil {
				t.Fatalf("exact limit %d failed: %v", tc.limit, err)
			}
			if err := tc.build(tc.limit + 1); err == nil {
				t.Fatalf("limit+1 value %d passed", tc.limit+1)
			}
		})
	}

	policy := f.authority.MergePolicy().Input()
	policy.EligibleReviewers = makeStableIdentities(limits.MaxEligibleReviewers)
	policy.MinimumApprovals = limits.MaxMinimumApprovals
	if _, err := NewMergePolicyV1(policy, limits); err != nil {
		t.Fatalf("minimum approvals exact limit failed: %v", err)
	}
	policy.MinimumApprovals++
	if _, err := NewMergePolicyV1(policy, limits); err == nil {
		t.Fatal("minimum approvals limit+1 passed")
	}
	policy.EligibleReviewers = makeStableIdentities(1)
	policy.MinimumApprovals = 2
	if _, err := NewMergePolicyV1(policy, limits); err == nil {
		t.Fatal("minimum approvals greater than eligible reviewers passed")
	}
}

func TestEvidenceAndMetadataStageBoundaries(t *testing.T) {
	limits := DefaultLimits()
	cases := []struct {
		name  string
		codes func(int) error
		limit int
	}{
		{
			name: "general evidence refs", limit: limits.MaxEvidenceRefs,
			codes: func(count int) error {
				refs := makeEvidenceRefs(count, "general")
				return canonicalizeEvidence(&refs, limits)
			},
		},
		{
			name: "READY closure refs", limit: limits.MaxReadyEvidenceClosureRefs,
			codes: func(count int) error {
				refs := makeEvidenceRefs(count, "ready")
				return canonicalizeReadyEvidenceClosure(&refs, limits)
			},
		},
		{
			name: "metadata items", limit: limits.MaxMetadataItems,
			codes: func(count int) error {
				metadata := make(map[string]string, count)
				for index := 0; index < count; index++ {
					metadata[fmt.Sprintf("key-%03d", index)] = "value"
				}
				refs := []ledger.EvidenceRef{}
				return canonicalizeCommon(&refs, metadata, limits)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.codes(tc.limit); err != nil {
				t.Fatalf("exact limit failed: %v", err)
			}
			if err := tc.codes(tc.limit + 1); err == nil {
				t.Fatal("limit+1 passed")
			}
		})
	}

	f := newFixture(t, MergeMethodSquash)
	readyInput := f.authority.ReadyBinding().Input()
	for index := len(readyInput.EvidenceClosureRefs); index < limits.MaxReadyEvidenceClosureRefs; index++ {
		readyInput.EvidenceClosureRefs = append(readyInput.EvidenceClosureRefs, ledger.EvidenceRef{
			URI: fmt.Sprintf("evidence/ready-extra/%03d", index), Kind: "ready-extra", SHA256: fmt.Sprintf("%064x", index+1000),
		})
	}
	if _, err := NewReadyAuthorityBindingV1(readyInput, limits); err != nil {
		t.Fatalf("READY binding rejected 256 closure refs: %v", err)
	}
	readyInput.EvidenceClosureRefs = append(readyInput.EvidenceClosureRefs, ledger.EvidenceRef{
		URI: "evidence/ready-extra/over", Kind: "ready-extra", SHA256: strings.Repeat("c", 64),
	})
	if _, err := NewReadyAuthorityBindingV1(readyInput, limits); err == nil {
		t.Fatal("READY binding accepted 257 closure refs")
	}
}

func TestPaginationClosureAndBoundaryByteBudgets(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	pageItems := makePaginationItems(f.limits.MaxPaginationItemsPerPage)
	if _, err := makeSinglePaginationPage(f, f.limits, pageItems); err != nil {
		t.Fatalf("exact per-page item limit failed: %v", err)
	}
	if _, err := makeSinglePaginationPage(f, f.limits, makePaginationItems(f.limits.MaxPaginationItemsPerPage+1)); err == nil {
		t.Fatal("per-page item limit+1 passed")
	}
	if _, err := makePaginationClosure(f, PaginationCheckRuns, nil, f.limits, makePaginationItems(f.limits.MaxObservedItemsPerPaginationSource)); err != nil {
		t.Fatalf("exact observed-items-per-source limit failed: %v", err)
	}
	if _, err := makePaginationClosure(f, PaginationCheckRuns, nil, f.limits, makePaginationItems(f.limits.MaxObservedItemsPerPaginationSource+1)); err == nil {
		t.Fatal("observed-items-per-source limit+1 passed")
	}
	pageLimits := f.limits
	pageLimits.MaxPaginationItemsPerPage = 50
	pageLimits.MaxObservedItemsPerPaginationSource = 550
	pageLimits.MaxItemsPerPage = 50
	pageLimits.MaxTotalItems = 550
	if closure, err := makePaginationClosure(f, PaginationCheckRuns, nil, pageLimits, makePaginationItems(451)); err != nil {
		t.Fatalf("exact pagination page limit failed: %v", err)
	} else if len(closure.input.Pages) != pageLimits.MaxPaginationPages {
		t.Fatalf("pagination pages = %d", len(closure.input.Pages))
	}
	if _, err := makePaginationClosure(f, PaginationCheckRuns, nil, pageLimits, makePaginationItems(501)); err == nil {
		t.Fatal("pagination page limit+1 passed")
	}

	exact, err := makeSizedPaginationClosure(f, PaginationCheckRuns, nil, f.limits, f.limits.MaxPaginationClosureBytes)
	if err != nil {
		t.Fatalf("1 MiB pagination closure failed: %v", err)
	}
	if len(exact.CanonicalJSON()) != f.limits.MaxPaginationClosureBytes {
		t.Fatalf("closure bytes = %d", len(exact.CanonicalJSON()))
	}
	items := paginationItemsFromClosure(exact)
	for index := range items {
		if len(items[index].Key) < f.limits.MaxTextBytes {
			items[index].Key += "x"
			break
		}
	}
	if _, err := makePaginationClosure(f, PaginationCheckRuns, nil, f.limits, items); err == nil {
		t.Fatal("pagination closure accepted 1 MiB + 1 byte")
	}

	baseReview := emptyPaginationClosure(t, f, PaginationReviews, &f.pr, "cumulative-review", f.snapshot.ObservedUnixNano())
	baseChecks := emptyPaginationClosure(t, f, PaginationCheckRuns, nil, "cumulative-checks", f.snapshot.ObservedUnixNano())
	baseStatuses := emptyPaginationClosure(t, f, PaginationCommitStatuses, nil, "cumulative-statuses", f.snapshot.ObservedUnixNano())
	sum := len(baseReview.canonical) + len(baseChecks.canonical) + len(baseStatuses.canonical)
	maxOne := max(len(baseReview.canonical), len(baseChecks.canonical), len(baseStatuses.canonical))

	exactLimits := f.limits
	exactLimits.MaxPaginationClosureBytes = maxOne
	exactLimits.MaxCumulativePaginationClosureBytes = sum
	exactFixture := f
	exactFixture.limits = exactLimits
	review := emptyPaginationClosure(t, exactFixture, PaginationReviews, &f.pr, "cumulative-review", f.snapshot.ObservedUnixNano())
	checks := emptyPaginationClosure(t, exactFixture, PaginationCheckRuns, nil, "cumulative-checks", f.snapshot.ObservedUnixNano())
	statuses := emptyPaginationClosure(t, exactFixture, PaginationCommitStatuses, nil, "cumulative-statuses", f.snapshot.ObservedUnixNano())
	if _, err := validatePaginationBoundaryV1(review, checks, statuses, exactLimits); err != nil {
		t.Fatalf("exact cumulative three-closure limit failed: %v", err)
	}

	overLimits := exactLimits
	overLimits.MaxCumulativePaginationClosureBytes--
	overFixture := f
	overFixture.limits = overLimits
	review = emptyPaginationClosure(t, overFixture, PaginationReviews, &f.pr, "cumulative-review", f.snapshot.ObservedUnixNano())
	checks = emptyPaginationClosure(t, overFixture, PaginationCheckRuns, nil, "cumulative-checks", f.snapshot.ObservedUnixNano())
	statuses = emptyPaginationClosure(t, overFixture, PaginationCommitStatuses, nil, "cumulative-statuses", f.snapshot.ObservedUnixNano())
	if _, err := validatePaginationBoundaryV1(review, checks, statuses, overLimits); err == nil {
		t.Fatal("cumulative three-closure limit+1 passed")
	}
}

func TestReadyLedgerUsesIndependentSnapshotAndRecordBudgets(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	input := f.readyProof.Input()
	limits := f.limits
	limits.MaxPaginationClosureBytes = 1
	limits.MaxReadyLedgerSnapshotBytes = len(input.ObservedLedgerJSONL)
	limits.MaxLedgerLineBytes = longestJSONLLine(input.ObservedLedgerJSONL)
	limits.MaxLedgerScanRecords = bytes.Count(input.ObservedLedgerJSONL, []byte{'\n'})
	if _, err := NewCurrentReadyProofV1(input, limits); err != nil {
		t.Fatalf("exact READY ledger byte/record limits failed independently of pagination: %v", err)
	}

	oversized := input
	oversized.ObservedLedgerJSONL = append(oversized.ObservedLedgerJSONL, 'x')
	oversized.ObservedLedgerLength = int64(len(oversized.ObservedLedgerJSONL))
	oversized.ObservedLedgerSHA256 = digestBytes(oversized.ObservedLedgerJSONL)
	oversized.EvidenceRefs[0].SHA256 = oversized.ObservedLedgerSHA256
	if _, err := NewCurrentReadyProofV1(oversized, limits); err == nil {
		t.Fatal("READY ledger snapshot byte limit+1 passed")
	}

	extra := ledger.Event{
		SchemaVersion: 1, EventID: "unrelated-ledger-event", Timestamp: time.Unix(1700000002, 0).UTC(),
		RunID: "other-run", EventType: "OBSERVATION", Actor: "controller", Source: "fixture",
	}
	extraJSON, err := json.Marshal(extra)
	if err != nil {
		t.Fatal(err)
	}
	oneMoreRecord := input
	oneMoreRecord.ObservedLedgerJSONL = append(oneMoreRecord.ObservedLedgerJSONL, extraJSON...)
	oneMoreRecord.ObservedLedgerJSONL = append(oneMoreRecord.ObservedLedgerJSONL, '\n')
	oneMoreRecord.ObservedLedgerLength = int64(len(oneMoreRecord.ObservedLedgerJSONL))
	oneMoreRecord.ObservedLedgerSHA256 = digestBytes(oneMoreRecord.ObservedLedgerJSONL)
	oneMoreRecord.EvidenceRefs[0].SHA256 = oneMoreRecord.ObservedLedgerSHA256
	recordLimits := limits
	recordLimits.MaxReadyLedgerSnapshotBytes = len(oneMoreRecord.ObservedLedgerJSONL)
	if _, err := NewCurrentReadyProofV1(oneMoreRecord, recordLimits); err == nil {
		t.Fatal("READY ledger record scan limit+1 passed")
	}
	recordLimits.MaxLedgerScanRecords++
	if _, err := NewCurrentReadyProofV1(oneMoreRecord, recordLimits); err != nil {
		t.Fatalf("exact READY ledger record scan limit failed: %v", err)
	}
}

func TestMergeInputCanonicalObjectExactLimitAndLimitPlusOne(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	input := mergeAuthorizationInputFromFixture(f)
	input.EvidenceRefs = makeEvidenceRefs(f.limits.MaxEvidenceRefs, "merge")
	limitsSHA, err := f.limits.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	payload, _, err := canonicalJSON(mergeAuthorizationPayloadWire(input, input.Authority, limitsSHA))
	if err != nil {
		t.Fatal(err)
	}
	remaining := f.limits.MaxCanonicalObjectBytes - len(payload)
	if remaining <= 0 {
		t.Fatalf("minimal merge input unexpectedly uses %d bytes", len(payload))
	}
	for index := range input.EvidenceRefs {
		capacity := f.limits.MaxTextBytes - len(input.EvidenceRefs[index].URI)
		add := min(remaining, capacity)
		input.EvidenceRefs[index].URI += strings.Repeat("x", add)
		remaining -= add
	}
	if remaining != 0 {
		t.Fatalf("could not fill merge input to canonical limit; %d bytes remain", remaining)
	}
	exact, err := NewMergeInput(input, f.mergeWrite.attempt.WriteID(), f.limits)
	if err != nil {
		t.Fatalf("256 KiB merge input failed: %v", err)
	}
	if len(exact.CanonicalPayload()) != f.limits.MaxCanonicalObjectBytes {
		t.Fatalf("merge input bytes = %d", len(exact.CanonicalPayload()))
	}
	over := input
	over.EvidenceRefs = append([]ledger.EvidenceRef(nil), input.EvidenceRefs...)
	for index := range over.EvidenceRefs {
		if len(over.EvidenceRefs[index].URI) < f.limits.MaxTextBytes {
			over.EvidenceRefs[index].URI += "x"
			break
		}
	}
	if _, err := NewMergeInput(over, f.mergeWrite.attempt.WriteID(), f.limits); err == nil {
		t.Fatal("merge input accepted 256 KiB + 1 byte")
	}
}

func TestExactPaginationClosureComposesThroughMergeInputFinalRevalidationAndSeal(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	runChecks, runClosure := makeExactChecksClosure(t, f, PaginationCheckRuns, CheckSourceCheckRun, 250, f.limits.MaxPaginationClosureBytes, "admission-runs", f.snapshot.ObservedUnixNano())
	statusChecks, statusClosure := makeExactChecksClosure(t, f, PaginationCommitStatuses, CheckSourceCommitStatus, 250, f.limits.MaxPaginationClosureBytes, "admission-statuses", f.snapshot.ObservedUnixNano())
	reviews, reviewsClosure := makeExactReviewsClosure(t, f, f.pr, 250, f.limits.MaxPaginationClosureBytes, "admission-reviews", f.snapshot.ObservedUnixNano())
	initialPRInput := f.prAuth.Input()
	initialPRInput.Reviews = reviews
	initialPRInput.ReviewsClosure = reviewsClosure
	initialPR, err := NewAuthoritativePullRequestSnapshotV1(initialPRInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	checks := append(runChecks, statusChecks...)
	input := mergeAuthorizationInputFromFixture(f)
	input.InitialPullRequest = initialPR
	input.Checks = checks
	input.CheckRunsClosure = runClosure
	input.CommitStatusesClosure = statusClosure

	mergeInput, err := NewMergeInput(input, f.mergeWrite.Attempt().WriteID(), f.limits)
	if err != nil {
		t.Fatalf("exact-limit pagination closure did not compose through MergeInput: %v", err)
	}
	closureBytes := len(reviewsClosure.CanonicalJSON()) + len(runClosure.CanonicalJSON()) + len(statusClosure.CanonicalJSON())
	if closureBytes != f.limits.MaxCumulativePaginationClosureBytes || len(mergeInput.CanonicalPayload()) > f.limits.MaxCanonicalObjectBytes {
		t.Fatalf("closure/input bytes = %d/%d", closureBytes, len(mergeInput.CanonicalPayload()))
	}
	checkBytes, _, err := canonicalJSON(checkWires(checks))
	if err != nil {
		t.Fatal(err)
	}
	reviewBytes, err := canonicalAuthoritativePRReviews(reviews)
	if err != nil {
		t.Fatal(err)
	}
	records, err := NewCanonicalRecordSetV1(checkBytes, reviewBytes, reviewsClosure.CanonicalJSON(), runClosure.CanonicalJSON(), statusClosure.CanonicalJSON())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseCanonicalMergeInput(mergeInput.CanonicalPayload(), f.limits, records)
	if err != nil || parsed.SHA256() != mergeInput.SHA256() || parsed.Attempt() != mergeInput.Attempt() {
		t.Fatalf("exact-limit MergeInput strict recovery failed: %v", err)
	}
	if _, err := ParseCanonicalMergeInput(mergeInput.CanonicalPayload(), f.limits); err == nil {
		t.Fatal("MergeInput parser accepted missing retained records")
	}
	missing, err := NewCanonicalRecordSetV1(reviewsClosure.CanonicalJSON(), runClosure.CanonicalJSON(), statusClosure.CanonicalJSON())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCanonicalMergeInput(mergeInput.CanonicalPayload(), f.limits, missing); err == nil {
		t.Fatal("MergeInput parser accepted a missing referenced check record")
	}
	substituted, err := NewCanonicalRecordSetV1(checkBytes, reviewBytes, reviewsClosure.CanonicalJSON(), runClosure.CanonicalJSON(), statusClosure.CanonicalJSON())
	if err != nil {
		t.Fatal(err)
	}
	substituted.records[retainedCanonicalReference(checkBytes)] = []byte("[]")
	if _, err := ParseCanonicalMergeInput(mergeInput.CanonicalPayload(), f.limits, substituted); err == nil {
		t.Fatal("MergeInput parser accepted a substituted referenced check record")
	}
	var mismatched mergeInputWireV1
	if err := strictDecode(mergeInput.CanonicalPayload(), &mismatched); err != nil {
		t.Fatal(err)
	}
	mismatched.CheckRunsClosure.SHA256 = strings.Repeat("f", 64)
	mismatchJSON, err := json.Marshal(mismatched)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCanonicalMergeInput(mismatchJSON, f.limits, records); err == nil {
		t.Fatal("MergeInput parser accepted a digest-mismatched closure reference")
	}

	finalInput := f.sealed.Seal().input.FinalRevalidation.Input()
	finalInput.MergeInput = mergeInput
	finalRunChecks, finalRunClosure := makeExactChecksClosure(t, f, PaginationCheckRuns, CheckSourceCheckRun, 250, f.limits.MaxPaginationClosureBytes, "final-runs", finalInput.StartedUnixNano+1)
	finalStatusChecks, finalStatusClosure := makeExactChecksClosure(t, f, PaginationCommitStatuses, CheckSourceCommitStatus, 250, f.limits.MaxPaginationClosureBytes, "final-statuses", finalInput.StartedUnixNano+1)
	finalReviews, finalReviewsClosure := makeExactReviewsClosure(t, f, f.pr, 250, f.limits.MaxPaginationClosureBytes, "final-reviews", finalInput.StartedUnixNano+1)
	finalPRInput := finalInput.PullRequest.Input()
	finalPRInput.Reviews = finalReviews
	finalPRInput.ReviewsClosure = finalReviewsClosure
	finalPR, err := NewAuthoritativePullRequestSnapshotV1(finalPRInput, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	finalChecks := append(finalRunChecks, finalStatusChecks...)
	finalInput.PullRequest = finalPR
	finalInput.Checks = finalChecks
	finalInput.CheckRunsClosure = finalRunClosure
	finalInput.CommitStatusesClosure = finalStatusClosure
	finalInput.EvidenceRefs = append(finalInput.EvidenceRefs, finalReviewsClosure.input.EvidenceRefs...)
	finalInput.EvidenceRefs = append(finalInput.EvidenceRefs, finalRunClosure.input.EvidenceRefs...)
	finalInput.EvidenceRefs = append(finalInput.EvidenceRefs, finalStatusClosure.input.EvidenceRefs...)
	admission, err := validatePaginationBoundaryV1(reviewsClosure, runClosure, statusClosure, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	finalInput.Counters.AdmissionObservedChecks = len(checks)
	finalInput.Counters.AdmissionObservedReviews = len(mergeInput.initialPullRequest.input.Reviews)
	finalInput.Counters.AdmissionPaginationSources = admission.Sources
	finalInput.Counters.AdmissionPaginationPages = admission.Pages
	finalInput.Counters.AdmissionPaginationItems = admission.Items
	finalInput.Counters.AdmissionPaginationClosureBytes = admission.ClosureBytes
	finalInput.Counters.AdmissionHTTPCalls = admission.Pages + 1
	finalInput.Counters.PreSubmitHTTPCalls = finalInput.Counters.AdmissionHTTPCalls + finalInput.Counters.FinalRevalidationHTTPCalls
	finalInput.Counters.TotalHTTPCalls = finalInput.Counters.PreSubmitHTTPCalls
	finalStats, err := validatePaginationBoundaryV1(finalReviewsClosure, finalRunClosure, finalStatusClosure, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	finalInput.Counters.FinalObservedChecks = len(finalChecks)
	finalInput.Counters.FinalObservedReviews = len(finalReviews)
	finalInput.Counters.FinalPaginationSources = finalStats.Sources
	finalInput.Counters.FinalPaginationPages = finalStats.Pages
	finalInput.Counters.FinalPaginationItems = finalStats.Items
	finalInput.Counters.FinalPaginationClosureBytes = finalStats.ClosureBytes
	finalInput.Counters.FinalRevalidationHTTPCalls = finalStats.Pages + 1
	finalInput.Counters.PreSubmitHTTPCalls = finalInput.Counters.AdmissionHTTPCalls + finalInput.Counters.FinalRevalidationHTTPCalls
	finalInput.Counters.TotalHTTPCalls = finalInput.Counters.PreSubmitHTTPCalls
	final, err := NewFinalRevalidationV1(finalInput, f.limits)
	if err != nil {
		t.Fatalf("exact-limit pagination closure did not compose through final revalidation: %v", err)
	}
	seal, err := NewAuthorizationSealV1(AuthorizationSealV1Input{MergeInput: mergeInput, FinalRevalidation: final}, f.limits)
	if err != nil {
		t.Fatalf("exact-limit pagination closure did not compose through authorization seal: %v", err)
	}
	if len(final.CanonicalJSON()) > f.limits.MaxCanonicalObjectBytes || len(seal.CanonicalJSON()) > f.limits.MaxCanonicalObjectBytes {
		t.Fatalf("final/seal bytes = %d/%d", len(final.CanonicalJSON()), len(seal.CanonicalJSON()))
	}
	finalCheckBytes, _, err := canonicalJSON(checkWires(finalChecks))
	if err != nil {
		t.Fatal(err)
	}
	finalReviewBytes, err := canonicalAuthoritativePRReviews(finalReviews)
	if err != nil {
		t.Fatal(err)
	}
	allRecords, err := NewCanonicalRecordSetV1(
		checkBytes, reviewBytes, reviewsClosure.CanonicalJSON(), runClosure.CanonicalJSON(), statusClosure.CanonicalJSON(),
		finalCheckBytes, finalReviewBytes, finalReviewsClosure.CanonicalJSON(), finalRunClosure.CanonicalJSON(), finalStatusClosure.CanonicalJSON(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCanonicalFinalRevalidationV1(final.CanonicalJSON(), mergeInput, f.limits, allRecords); err != nil {
		t.Fatalf("exact-limit final pagination strict recovery failed: %v", err)
	}
	if _, err := ParseCanonicalAuthorizationSealV1(seal.CanonicalJSON(), mergeInput, f.limits, allRecords); err != nil {
		t.Fatalf("exact-limit pagination seal strict recovery failed: %v", err)
	}
	if _, err := ParseCanonicalFinalRevalidationV1(final.CanonicalJSON(), mergeInput, f.limits); err == nil {
		t.Fatal("final revalidation parser accepted missing retained pagination records")
	}
	if _, err := ParseCanonicalAuthorizationSealV1(seal.CanonicalJSON(), mergeInput, f.limits); err == nil {
		t.Fatal("authorization seal parser accepted missing retained pagination records")
	}
	commitment, err := NewTargetRefCommitmentV1(mergeInput, seal, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := NewSealedMergeAuthorizationV1(SealedMergeAuthorizationV1Input{MergeInput: mergeInput, Seal: seal, Commitment: commitment}, f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewTargetSubmissionV1("exact-pagination-target", sealed, f.limits); err != nil {
		t.Fatalf("exact-limit pagination authorization could not reach target submission: %v", err)
	}

	overClosure := clonePaginationClosure(runClosure)
	overClosure.canonical = append(overClosure.canonical, 'x')
	overClosure.digest = digestBytes(overClosure.canonical)
	overInput := input
	overInput.CheckRunsClosure = overClosure
	if _, err := NewMergeInput(overInput, f.mergeWrite.Attempt().WriteID(), f.limits); err == nil {
		t.Fatal("MergeInput accepted a pagination closure at limit+1")
	}
	forgedMerge := cloneLifecycleMergeInput(mergeInput)
	forgedMerge.checkRunsClosure = overClosure
	finalInput.MergeInput = forgedMerge
	if _, err := NewFinalRevalidationV1(finalInput, f.limits); err == nil {
		t.Fatal("final revalidation accepted a nested pagination closure at limit+1")
	}
	overFinalClosure := clonePaginationClosure(finalRunClosure)
	overFinalClosure.canonical = append(overFinalClosure.canonical, 'x')
	overFinalClosure.digest = digestBytes(overFinalClosure.canonical)
	overFinalInput := final.Input()
	overFinalInput.CheckRunsClosure = overFinalClosure
	if _, err := NewFinalRevalidationV1(overFinalInput, f.limits); err == nil {
		t.Fatal("final revalidation accepted a final pagination closure at limit+1")
	}
	forgedFinal := cloneFinalRevalidation(final)
	forgedFinal.input.CheckRunsClosure = overFinalClosure
	if _, err := NewAuthorizationSealV1(AuthorizationSealV1Input{MergeInput: mergeInput, FinalRevalidation: forgedFinal}, f.limits); err == nil {
		t.Fatal("authorization seal accepted a nested pagination closure at limit+1")
	}
}

func TestExactReadyLedgerComposesThroughFinalRevalidationAndSeal(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	ledgerJSONL := makeExactReadyLedgerJSONL(t, f.readyProof.input.ObservedLedgerJSONL, f.limits.MaxReadyLedgerSnapshotBytes, f.limits.MaxLedgerLineBytes)
	proofInput := f.readyProof.Input()
	proofInput.ObservedLedgerJSONL = ledgerJSONL
	proofInput.ObservedLedgerLength = int64(len(ledgerJSONL))
	proofInput.ObservedLedgerSHA256 = digestBytes(ledgerJSONL)
	proofInput.EvidenceRefs = []ledger.EvidenceRef{{
		URI: "evidence/current-ready-ledger-exact", Kind: CurrentReadyLedgerEvidenceKindV1, SHA256: proofInput.ObservedLedgerSHA256,
	}}
	proof, err := NewCurrentReadyProofV1(proofInput, f.limits)
	if err != nil {
		t.Fatalf("exact-limit READY ledger proof failed: %v", err)
	}
	if len(ledgerJSONL) != f.limits.MaxReadyLedgerSnapshotBytes || len(proof.CanonicalJSON()) > f.limits.MaxCanonicalObjectBytes {
		t.Fatalf("ledger/proof bytes = %d/%d", len(ledgerJSONL), len(proof.CanonicalJSON()))
	}

	finalInput := f.sealed.Seal().input.FinalRevalidation.Input()
	finalInput.CurrentReadyProof = proof
	finalInput.Counters.ReadyLedgerBytes = int64(len(ledgerJSONL))
	finalInput.Counters.ReadyLedgerRecords = bytes.Count(ledgerJSONL, []byte{'\n'})
	for index := range finalInput.EvidenceRefs {
		if finalInput.EvidenceRefs[index].Kind == CurrentReadyLedgerEvidenceKindV1 {
			finalInput.EvidenceRefs[index] = proofInput.EvidenceRefs[0]
		}
	}
	final, err := NewFinalRevalidationV1(finalInput, f.limits)
	if err != nil {
		t.Fatalf("exact-limit READY ledger did not compose through final revalidation: %v", err)
	}
	seal, err := NewAuthorizationSealV1(AuthorizationSealV1Input{MergeInput: f.mergeWrite, FinalRevalidation: final}, f.limits)
	if err != nil {
		t.Fatalf("exact-limit READY ledger did not compose through authorization seal: %v", err)
	}
	if len(final.CanonicalJSON()) > f.limits.MaxCanonicalObjectBytes || len(seal.CanonicalJSON()) > f.limits.MaxCanonicalObjectBytes {
		t.Fatalf("READY final/seal bytes = %d/%d", len(final.CanonicalJSON()), len(seal.CanonicalJSON()))
	}
	records, err := NewCanonicalRecordSetV1(ledgerJSONL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCanonicalFinalRevalidationV1(final.CanonicalJSON(), f.mergeWrite, f.limits, records); err != nil {
		t.Fatalf("exact-limit final revalidation strict recovery failed: %v", err)
	}
	if _, err := ParseCanonicalAuthorizationSealV1(seal.CanonicalJSON(), f.mergeWrite, f.limits, records); err != nil {
		t.Fatalf("exact-limit seal strict recovery failed: %v", err)
	}
	if _, err := ParseCanonicalFinalRevalidationV1(final.CanonicalJSON(), f.mergeWrite, f.limits); err == nil {
		t.Fatal("final revalidation parser accepted a missing READY ledger record")
	}
	substituted, err := NewCanonicalRecordSetV1(ledgerJSONL)
	if err != nil {
		t.Fatal(err)
	}
	substituted.records[retainedCanonicalReference(ledgerJSONL)] = []byte("{}\n")
	if _, err := ParseCanonicalAuthorizationSealV1(seal.CanonicalJSON(), f.mergeWrite, f.limits, substituted); err == nil {
		t.Fatal("authorization seal parser accepted a substituted READY ledger record")
	}

	overProof := cloneCurrentReadyProof(proof)
	overProof.input.ObservedLedgerJSONL = append(append([]byte(nil), ledgerJSONL...), 'x')
	overProof.input.ObservedLedgerLength++
	overProof.input.ObservedLedgerSHA256 = digestBytes(overProof.input.ObservedLedgerJSONL)
	overProof.input.EvidenceRefs[0].SHA256 = overProof.input.ObservedLedgerSHA256
	overFinalInput := finalInput
	overFinalInput.CurrentReadyProof = overProof
	overFinalInput.Counters.ReadyLedgerBytes++
	if _, err := NewFinalRevalidationV1(overFinalInput, f.limits); err == nil {
		t.Fatal("final revalidation accepted READY ledger bytes at limit+1")
	}
	forgedFinal := cloneFinalRevalidation(final)
	forgedFinal.input.CurrentReadyProof = overProof
	forgedFinal.input.Counters.ReadyLedgerBytes++
	if _, err := NewAuthorizationSealV1(AuthorizationSealV1Input{MergeInput: f.mergeWrite, FinalRevalidation: forgedFinal}, f.limits); err == nil {
		t.Fatal("authorization seal accepted READY ledger bytes at limit+1")
	}
}

func TestTargetResponseBodyAndEnvelopeIndependentExactLimits(t *testing.T) {
	f := newFixture(t, MergeMethodMerge)
	invocationID := strings.Repeat("<", f.limits.MaxTextBytes)
	submission := newTargetSubmission(t, f, invocationID)
	response, err := NewSnapshotIdentity("github", "response-exact-envelope", f.sealed.Seal().input.FinalRevalidation.input.CompletedUnixNano+1)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("\"" + strings.Repeat("x", f.limits.MaxDecompressedResponseBodyBytes-2) + "\"")
	bodyEvidence := ledger.EvidenceRef{
		URI: strings.Repeat("<", f.limits.MaxTextBytes), Kind: GitHubTargetResponseBodyEvidenceKindV1, SHA256: digestBytes(body),
	}
	input := TargetResponseEnvelopeV1Input{
		Response: response, HTTPStatus: 200, ResponseBody: body, BodyEvidence: bodyEvidence, EnvelopeURI: "e",
	}
	limitsSHA, err := f.limits.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	baseWire := targetResponseEnvelopeWireV1{
		Schema: TargetResponseEnvelopeSchemaV1, TargetSubmissionSHA256: submission.SHA256(), InvocationID: submission.invocationID,
		Response: snapshotWire(response), HTTPStatus: input.HTTPStatus, ResponseBody: append(json.RawMessage(nil), body...),
		ResponseBodySHA256: digestBytes(body), BodyEvidence: bodyEvidence, EnvelopeURI: input.EnvelopeURI, LimitsSHA256: limitsSHA,
	}
	baseJSON, err := json.Marshal(baseWire)
	if err != nil {
		t.Fatal(err)
	}
	remaining := f.limits.MaxTargetResponseEnvelopeBytes - len(baseJSON)
	if remaining <= 0 {
		t.Fatalf("bounded metadata already exceeds independent envelope limit by %d", -remaining)
	}
	available := f.limits.MaxTextBytes - len(input.EnvelopeURI)
	escaped := min(remaining/6, available)
	plain := remaining - escaped*6
	if escaped+plain > available {
		escaped--
		plain = remaining - escaped*6
	}
	if escaped < 0 || plain < 0 || escaped+plain > available {
		t.Fatalf("cannot fill independent envelope bound: remaining=%d capacity=%d", remaining, available)
	}
	input.EnvelopeURI += strings.Repeat("<", escaped) + strings.Repeat("x", plain)
	envelope, err := NewTargetResponseEnvelopeV1(input, submission, f.limits)
	if err != nil {
		t.Fatalf("exact 4-MiB body plus bounded metadata failed: %v", err)
	}
	if len(body) != f.limits.MaxDecompressedResponseBodyBytes || len(envelope.CanonicalJSON()) != f.limits.MaxTargetResponseEnvelopeBytes {
		t.Fatalf("body/envelope bytes = %d/%d", len(body), len(envelope.CanonicalJSON()))
	}
	if _, err := ParseCanonicalTargetResponseEnvelopeV1(envelope.CanonicalJSON(), submission, f.limits); err != nil {
		t.Fatalf("exact outer-limit envelope parse failed: %v", err)
	}

	overInput := input
	overInput.EnvelopeURI += "x"
	if _, err := NewTargetResponseEnvelopeV1(overInput, submission, f.limits); err == nil {
		t.Fatal("target response constructor accepted outer-limit+1")
	}
	overWire := baseWire
	overWire.EnvelopeURI = overInput.EnvelopeURI
	overJSON, err := json.Marshal(overWire)
	if err != nil {
		t.Fatal(err)
	}
	if len(overJSON) != f.limits.MaxTargetResponseEnvelopeBytes+1 {
		t.Fatalf("outer over-limit bytes = %d", len(overJSON))
	}
	if _, err := ParseCanonicalTargetResponseEnvelopeV1(overJSON, submission, f.limits); err == nil {
		t.Fatal("target response parser accepted outer-limit+1")
	}
	overBodyInput := input
	overBodyInput.EnvelopeURI = "evidence/over-body"
	overBodyInput.ResponseBody = []byte("\"" + strings.Repeat("x", f.limits.MaxDecompressedResponseBodyBytes-1) + "\"")
	overBodyInput.BodyEvidence = ledger.EvidenceRef{URI: "evidence/over-body", Kind: GitHubTargetResponseBodyEvidenceKindV1, SHA256: digestBytes(overBodyInput.ResponseBody)}
	if _, err := NewTargetResponseEnvelopeV1(overBodyInput, submission, f.limits); err == nil {
		t.Fatal("target response constructor accepted body-limit+1")
	}
}

func TestAuthorizationCountersExactLimitsAndRelationships(t *testing.T) {
	limits := DefaultLimits()
	base := func() AuthorizationCountersV1 {
		return AuthorizationCountersV1{
			AdmissionPaginationSources: limits.RequiredPaginationSources,
			FinalPaginationSources:     limits.RequiredPaginationSources,
		}
	}
	cases := []struct {
		name string
		set  func(*AuthorizationCountersV1, bool)
	}{
		{"admission observed checks", func(c *AuthorizationCountersV1, over bool) {
			c.AdmissionObservedChecks = limits.MaxObservedChecks + btoi(over)
		}},
		{"admission observed reviews", func(c *AuthorizationCountersV1, over bool) {
			c.AdmissionObservedReviews = limits.MaxObservedReviews + btoi(over)
		}},
		{"final observed checks", func(c *AuthorizationCountersV1, over bool) {
			c.FinalObservedChecks = limits.MaxObservedChecks + btoi(over)
		}},
		{"final observed reviews", func(c *AuthorizationCountersV1, over bool) {
			c.FinalObservedReviews = limits.MaxObservedReviews + btoi(over)
		}},
		{"admission pagination sources", func(c *AuthorizationCountersV1, over bool) {
			c.AdmissionPaginationSources = limits.RequiredPaginationSources + btoi(over)
		}},
		{"final pagination pages", func(c *AuthorizationCountersV1, over bool) {
			c.FinalPaginationPages = limits.RequiredPaginationSources*limits.MaxPaginationPages + btoi(over)
		}},
		{"admission pagination items", func(c *AuthorizationCountersV1, over bool) {
			c.AdmissionPaginationItems = limits.MaxObservedChecks + limits.MaxObservedReviews + btoi(over)
		}},
		{"admission closure bytes", func(c *AuthorizationCountersV1, over bool) {
			c.AdmissionPaginationClosureBytes = int64(limits.MaxCumulativePaginationClosureBytes + btoi(over))
		}},
		{"final closure bytes", func(c *AuthorizationCountersV1, over bool) {
			c.FinalPaginationClosureBytes = int64(limits.MaxCumulativePaginationClosureBytes + btoi(over))
		}},
		{"READY ledger bytes", func(c *AuthorizationCountersV1, over bool) {
			c.ReadyLedgerBytes = int64(limits.MaxReadyLedgerSnapshotBytes + btoi(over))
		}},
		{"READY ledger records", func(c *AuthorizationCountersV1, over bool) {
			c.ReadyLedgerRecords = limits.MaxLedgerScanRecords + btoi(over)
		}},
		{"pre-submit calls", func(c *AuthorizationCountersV1, over bool) {
			c.PreSubmitHTTPCalls = limits.MaxPreSubmitHTTPCalls + btoi(over)
			c.AdmissionHTTPCalls = c.PreSubmitHTTPCalls
			c.TotalHTTPCalls = c.PreSubmitHTTPCalls
		}},
		{"commit-object submissions", func(c *AuthorizationCountersV1, over bool) {
			c.CommitObjectCreationSubmissions = limits.MaxCommitObjectCreationSubmissions + btoi(over)
			c.TotalHTTPCalls = c.CommitObjectCreationSubmissions
		}},
		{"target-ref submissions", func(c *AuthorizationCountersV1, over bool) {
			c.TargetRefUpdateSubmissions = limits.MaxTargetRefUpdateSubmissions + btoi(over)
			c.TotalHTTPCalls = c.TargetRefUpdateSubmissions
		}},
		{"post-merge calls", func(c *AuthorizationCountersV1, over bool) {
			c.PostMergeHTTPCalls = limits.MaxPostMergeHTTPCalls + btoi(over)
			c.TotalHTTPCalls = c.PostMergeHTTPCalls
		}},
		{"reconciliation rounds", func(c *AuthorizationCountersV1, over bool) {
			c.ReconciliationRounds = limits.MaxReconciliationRounds + btoi(over)
		}},
		{"reconciliation calls", func(c *AuthorizationCountersV1, over bool) {
			c.ReconciliationRounds = limits.MaxReconciliationRounds
			c.ReconciliationHTTPCalls = limits.MaxReconciliationRounds*limits.MaxReconciliationCallsPerRound + btoi(over)
			c.TotalHTTPCalls = c.ReconciliationHTTPCalls
		}},
		{"principal validation calls", func(c *AuthorizationCountersV1, over bool) {
			c.PrincipalValidationHTTPCalls = limits.MaxPrincipalValidationCalls + btoi(over)
			c.TotalHTTPCalls = c.PrincipalValidationHTTPCalls
		}},
		{"total calls", func(c *AuthorizationCountersV1, over bool) {
			c.PreSubmitHTTPCalls = limits.MaxPreSubmitHTTPCalls
			c.AdmissionHTTPCalls = limits.MaxPreSubmitHTTPCalls
			c.CommitObjectCreationSubmissions = limits.MaxCommitObjectCreationSubmissions
			c.TargetRefUpdateSubmissions = limits.MaxTargetRefUpdateSubmissions
			c.PostMergeHTTPCalls = limits.MaxPostMergeHTTPCalls
			c.ReconciliationRounds = limits.MaxReconciliationRounds
			c.ReconciliationHTTPCalls = limits.MaxReconciliationRounds * limits.MaxReconciliationCallsPerRound
			c.PrincipalValidationHTTPCalls = limits.MaxPrincipalValidationCalls
			c.TotalHTTPCalls = limits.MaxHTTPCalls + btoi(over)
		}},
		{"cumulative request bytes", func(c *AuthorizationCountersV1, over bool) {
			c.CumulativeRequestBytes = limits.MaxCumulativeRequestBytes + int64(btoi(over))
		}},
		{"cumulative header bytes", func(c *AuthorizationCountersV1, over bool) {
			c.CumulativeResponseHeaderBytes = limits.MaxCumulativeResponseHeaderBytes + int64(btoi(over))
		}},
		{"cumulative compressed bytes", func(c *AuthorizationCountersV1, over bool) {
			c.CumulativeCompressedResponseBytes = limits.MaxCumulativeCompressedResponseBytes + int64(btoi(over))
		}},
		{"cumulative decompressed bytes", func(c *AuthorizationCountersV1, over bool) {
			c.CumulativeDecompressedResponseBytes = limits.MaxCumulativeDecompressedResponseBytes + int64(btoi(over))
		}},
		{"active provider time", func(c *AuthorizationCountersV1, over bool) {
			c.CumulativeActiveProviderCallNanos = int64(limits.MaxCumulativeActiveProviderCallTime) + int64(btoi(over))
			c.ControllerInvocationNanos = int64(limits.MaxControllerInvocationTime)
		}},
		{"controller invocation time", func(c *AuthorizationCountersV1, over bool) {
			c.ControllerInvocationNanos = int64(limits.MaxControllerInvocationTime) + int64(btoi(over))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exact := base()
			tc.set(&exact, false)
			if !exact.valid(limits) {
				t.Fatal("exact limit failed")
			}
			over := base()
			tc.set(&over, true)
			if over.valid(limits) {
				t.Fatal("limit+1 passed")
			}
		})
	}

	relationships := []struct {
		name    string
		counter AuthorizationCountersV1
	}{
		{"phase exceeds aggregate", func() AuthorizationCountersV1 {
			c := base()
			c.AdmissionHTTPCalls = 1
			return c
		}()},
		{"classified sum exceeds total", func() AuthorizationCountersV1 {
			c := base()
			c.PreSubmitHTTPCalls = 1
			return c
		}()},
		{"reconciliation calls exceed completed rounds", func() AuthorizationCountersV1 {
			c := base()
			c.ReconciliationHTTPCalls = 1
			c.TotalHTTPCalls = 1
			return c
		}()},
		{"active time exceeds invocation", func() AuthorizationCountersV1 {
			c := base()
			c.CumulativeActiveProviderCallNanos = 1
			return c
		}()},
	}
	for _, tc := range relationships {
		t.Run(tc.name, func(t *testing.T) {
			if tc.counter.valid(limits) {
				t.Fatal("invalid counter relationship passed")
			}
		})
	}
}

func makeTrustedCheckIdentities(count int) []TrustedCheckIdentityV1 {
	result := make([]TrustedCheckIdentityV1, count)
	for index := range result {
		context := fmt.Sprintf("check-%03d", index)
		result[index] = TrustedCheckIdentityV1{
			Context: context, Source: CheckSourceCheckRun,
			Producer: StableIdentityV1{DatabaseID: int64(index + 1), NodeID: fmt.Sprintf("producer-%03d", index)},
		}
	}
	return result
}

func makeStableIdentities(count int) []StableIdentityV1 {
	result := make([]StableIdentityV1, count)
	for index := range result {
		result[index] = StableIdentityV1{DatabaseID: int64(index + 1), NodeID: fmt.Sprintf("reviewer-%03d", index)}
	}
	return result
}

func makeObservedChecks(count int, head GitSHA) []Check {
	result := make([]Check, count)
	for index := range result {
		context := fmt.Sprintf("observed-check-%03d", index)
		result[index] = Check{
			NodeID: fmt.Sprintf("check-node-%03d", index), Name: context,
			Identity: TrustedCheckIdentityV1{
				Context: context, Source: CheckSourceCheckRun,
				Producer: StableIdentityV1{DatabaseID: int64(index + 1), NodeID: fmt.Sprintf("producer-%03d", index)},
			},
			Status: CheckCompleted, Conclusion: ConclusionSuccess, HeadSHA: head,
		}
	}
	return result
}

func makeObservedReviews(count int, head GitSHA) []Review {
	result := make([]Review, count)
	for index := range result {
		result[index] = Review{
			NodeID: fmt.Sprintf("review-node-%03d", index), DatabaseID: int64(index + 1),
			Reviewer: StableIdentityV1{DatabaseID: int64(index + 1), NodeID: fmt.Sprintf("reviewer-%03d", index)},
			State:    ReviewApproved, CommitSHA: head,
		}
	}
	return result
}

func makeEvidenceRefs(count int, prefix string) []ledger.EvidenceRef {
	result := make([]ledger.EvidenceRef, count)
	for index := range result {
		result[index] = ledger.EvidenceRef{
			URI: fmt.Sprintf("evidence/%s/%03d", prefix, index), Kind: prefix,
			SHA256: fmt.Sprintf("%064x", index+1),
		}
	}
	return result
}

func makeSizedPaginationClosure(f fixture, source PaginationSourceKind, pr *PullRequestIdentity, limits Limits, target int) (PaginationClosureV1, error) {
	items := makePaginationItems(300)
	closure, err := makePaginationClosure(f, source, pr, limits, items)
	if err != nil {
		return PaginationClosureV1{}, err
	}
	remaining := target - len(closure.canonical)
	if remaining < 0 {
		return PaginationClosureV1{}, fmt.Errorf("minimal closure is %d bytes", len(closure.canonical))
	}
	for index := range items {
		capacity := limits.MaxTextBytes - len(items[index].Key)
		add := min(remaining, capacity)
		items[index].Key += strings.Repeat("x", add)
		remaining -= add
	}
	if remaining != 0 {
		return PaginationClosureV1{}, fmt.Errorf("could not fill closure; %d bytes remain", remaining)
	}
	return makePaginationClosure(f, source, pr, limits, items)
}

func makeExactChecksClosure(t *testing.T, f fixture, paginationSource PaginationSourceKind, checkSource CheckSourceKind, count, target int, requestPrefix string, observedUnixNano int64) ([]Check, PaginationClosureV1) {
	t.Helper()
	checks := make([]Check, count)
	for index := range checks {
		name := fmt.Sprintf("composed-check-%03d", index)
		checks[index] = Check{
			NodeID: fmt.Sprintf("composed-check-node-%03d", index), Name: name,
			Identity: TrustedCheckIdentityV1{
				Context: name, Source: checkSource,
				Producer: StableIdentityV1{DatabaseID: int64(index + 1000), NodeID: fmt.Sprintf("composed-producer-%03d", index)},
			},
			Status: CheckCompleted, Conclusion: ConclusionSuccess, HeadSHA: f.headSHA,
		}
	}
	items := paginationItemsFromChecks(t, checks)
	closure, err := makePaginationClosureAt(f, paginationSource, nil, f.limits, items, requestPrefix, observedUnixNano)
	if err != nil {
		t.Fatal(err)
	}
	remaining := target - len(closure.CanonicalJSON())
	if remaining < 0 {
		t.Fatalf("base composed closure is %d bytes", len(closure.CanonicalJSON()))
	}
	for index := range checks {
		capacity := f.limits.MaxTextBytes - len(checks[index].NodeID)
		add := min(remaining, capacity)
		checks[index].NodeID += strings.Repeat("x", add)
		remaining -= add
	}
	if remaining != 0 {
		t.Fatalf("could not fill composed closure; %d bytes remain", remaining)
	}
	items = paginationItemsFromChecks(t, checks)
	closure, err = makePaginationClosureAt(f, paginationSource, nil, f.limits, items, requestPrefix, observedUnixNano)
	if err != nil {
		t.Fatal(err)
	}
	if len(closure.CanonicalJSON()) != target {
		t.Fatalf("composed closure bytes = %d, want %d", len(closure.CanonicalJSON()), target)
	}
	return checks, closure
}

func makeExactReviewsClosure(t *testing.T, f fixture, pr PullRequestIdentity, count, target int, requestPrefix string, observedUnixNano int64) ([]Review, PaginationClosureV1) {
	t.Helper()
	reviews := make([]Review, count)
	for index := range reviews {
		reviews[index] = Review{
			NodeID: fmt.Sprintf("composed-review-node-%03d", index), DatabaseID: int64(index + 1000),
			Reviewer: StableIdentityV1{DatabaseID: int64(index + 2000), NodeID: fmt.Sprintf("composed-reviewer-%03d", index)},
			State:    ReviewApproved, CommitSHA: f.headSHA,
		}
	}
	items := paginationItemsFromReviews(t, reviews)
	closure, err := makePaginationClosureAt(f, PaginationReviews, &pr, f.limits, items, requestPrefix, observedUnixNano)
	if err != nil {
		t.Fatal(err)
	}
	remaining := target - len(closure.CanonicalJSON())
	if remaining < 0 {
		t.Fatalf("base composed review closure is %d bytes", len(closure.CanonicalJSON()))
	}
	for index := range reviews {
		keyPrefix := fmt.Sprintf("%d/", reviews[index].DatabaseID)
		capacity := f.limits.MaxTextBytes - len(keyPrefix) - len(reviews[index].NodeID)
		add := min(remaining, capacity)
		reviews[index].NodeID += strings.Repeat("x", add)
		remaining -= add
	}
	if remaining != 0 {
		t.Fatalf("could not fill composed review closure; %d bytes remain", remaining)
	}
	items = paginationItemsFromReviews(t, reviews)
	closure, err = makePaginationClosureAt(f, PaginationReviews, &pr, f.limits, items, requestPrefix, observedUnixNano)
	if err != nil {
		t.Fatal(err)
	}
	if len(closure.CanonicalJSON()) != target {
		t.Fatalf("composed review closure bytes = %d, want %d", len(closure.CanonicalJSON()), target)
	}
	return reviews, closure
}

func paginationItemsFromChecks(t *testing.T, checks []Check) []CanonicalPaginationItemV1 {
	t.Helper()
	items := make([]CanonicalPaginationItemV1, len(checks))
	for index, check := range checks {
		raw, err := json.Marshal(checkWire{check.NodeID, check.Name, check.Identity, check.Status, check.Conclusion, check.HeadSHA.String(), check.EvidenceRefs})
		if err != nil {
			t.Fatal(err)
		}
		items[index] = CanonicalPaginationItemV1{Key: check.NodeID, SHA256: digestBytes(raw)}
	}
	return items
}

func paginationItemsFromReviews(t *testing.T, reviews []Review) []CanonicalPaginationItemV1 {
	t.Helper()
	items := make([]CanonicalPaginationItemV1, len(reviews))
	for index, review := range reviews {
		raw, err := json.Marshal(reviewWire{review.NodeID, review.DatabaseID, review.Reviewer, review.State, review.CommitSHA.String()})
		if err != nil {
			t.Fatal(err)
		}
		items[index] = CanonicalPaginationItemV1{Key: fmt.Sprintf("%d/%s", review.DatabaseID, review.NodeID), SHA256: digestBytes(raw)}
	}
	return items
}

func makeExactReadyLedgerJSONL(t *testing.T, prefix []byte, target, maxLine int) []byte {
	t.Helper()
	if len(prefix) >= target {
		t.Fatalf("READY prefix is %d bytes for target %d", len(prefix), target)
	}
	result := make([]byte, len(prefix), target)
	copy(result, prefix)
	remaining := target - len(result)
	records := (remaining + maxLine) / (maxLine + 1)
	for index := 0; index < records; index++ {
		recordsLeft := records - index
		recordBytes := remaining / recordsLeft
		lineBytes := recordBytes - 1
		event := ledger.Event{
			SchemaVersion: 1, EventID: fmt.Sprintf("unrelated-padding-%06d", index),
			Timestamp: time.Unix(1700000100+int64(index), 0).UTC(), RunID: fmt.Sprintf("unrelated-run-%06d", index),
			EventType: "OBSERVATION", Actor: "controller", Source: "resource-limit-fixture",
			Payload: map[string]any{"padding": ""},
		}
		base, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		padding := lineBytes - len(base)
		if padding < 0 || lineBytes > maxLine {
			t.Fatalf("cannot size READY ledger record to %d bytes", lineBytes)
		}
		event.Payload["padding"] = strings.Repeat("x", padding)
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if len(line) != lineBytes {
			t.Fatalf("READY ledger line bytes = %d, want %d", len(line), lineBytes)
		}
		result = append(result, line...)
		result = append(result, '\n')
		remaining -= recordBytes
	}
	if len(result) != target || remaining != 0 {
		t.Fatalf("READY ledger bytes = %d, want %d", len(result), target)
	}
	return result
}

func makePaginationClosure(f fixture, source PaginationSourceKind, pr *PullRequestIdentity, limits Limits, items []CanonicalPaginationItemV1) (PaginationClosureV1, error) {
	return makePaginationClosureAt(f, source, pr, limits, items, "sized", f.snapshot.ObservedUnixNano())
}

func makePaginationClosureAt(f fixture, source PaginationSourceKind, pr *PullRequestIdentity, limits Limits, items []CanonicalPaginationItemV1, requestPrefix string, observedUnixNano int64) (PaginationClosureV1, error) {
	query, err := DerivePaginationQueryV1(PaginationQueryScopeV1{
		Source: source, Repository: f.repository, RepositoryNodeID: "R_repo", PullRequest: pr, HeadSHA: f.headSHA,
	}, limits)
	if err != nil {
		return PaginationClosureV1{}, err
	}
	pageCount := (len(items) + query.PerPage - 1) / query.PerPage
	pages := make([]PaginationPageV1, pageCount)
	evidence := make([]ledger.EvidenceRef, 0, len(pages)*2)
	for index := range pages {
		start, end := index*query.PerPage, min((index+1)*query.PerPage, len(items))
		response, err := NewSnapshotIdentity("github", fmt.Sprintf("%s-%s-%02d", requestPrefix, source, index), observedUnixNano+int64(index))
		if err != nil {
			return PaginationClosureV1{}, err
		}
		bodyEvidence := ledger.EvidenceRef{
			URI: fmt.Sprintf("evidence/%s-%s-%02d", requestPrefix, source, index), Kind: GitHubPaginationBodyEvidenceKindV1,
			SHA256: fmt.Sprintf("%064x", index+1000),
		}
		pageInput := PaginationPageV1Input{
			Query: query, Ordinal: index, RequestedPage: index + 1, Response: response,
			RawBodySHA256: bodyEvidence.SHA256, ResponseEvidence: bodyEvidence,
			Items: append([]CanonicalPaginationItemV1(nil), items[start:end]...), RESTLinkObserved: true,
		}
		if index < len(pages)-1 {
			values := url.Values{"page": {fmt.Sprint(index + 2)}, "per_page": {fmt.Sprint(query.PerPage)}}
			for key, value := range query.Variables {
				values.Set(key, value)
			}
			pageInput.RESTLinkHeader = "<https://api.github.com" + query.PathOrDocumentSHA256 + "?" + values.Encode() + ">; rel=\"next\""
		}
		pageInput.EnvelopeEvidence, err = NewPaginationEnvelopeEvidenceV1(fmt.Sprintf("evidence/%s-envelope-%s-%02d", requestPrefix, source, index), pageInput, limits)
		if err != nil {
			return PaginationClosureV1{}, err
		}
		pages[index], err = NewPaginationPageV1(pageInput, limits)
		if err != nil {
			return PaginationClosureV1{}, err
		}
		evidence = append(evidence, bodyEvidence, pageInput.EnvelopeEvidence)
	}
	return NewPaginationClosureV1(PaginationClosureV1Input{Query: query, Pages: pages, EvidenceRefs: evidence}, limits)
}

func makeSinglePaginationPage(f fixture, limits Limits, items []CanonicalPaginationItemV1) (PaginationPageV1, error) {
	query, err := DerivePaginationQueryV1(PaginationQueryScopeV1{
		Source: PaginationCheckRuns, Repository: f.repository, RepositoryNodeID: "R_repo", HeadSHA: f.headSHA,
	}, limits)
	if err != nil {
		return PaginationPageV1{}, err
	}
	response, err := NewSnapshotIdentity("github", "single-page-boundary", f.snapshot.ObservedUnixNano())
	if err != nil {
		return PaginationPageV1{}, err
	}
	body := ledger.EvidenceRef{URI: "evidence/single-page-boundary", Kind: GitHubPaginationBodyEvidenceKindV1, SHA256: strings.Repeat("a", 64)}
	input := PaginationPageV1Input{
		Query: query, Ordinal: 0, RequestedPage: 1, Response: response, RawBodySHA256: body.SHA256,
		ResponseEvidence: body, Items: items, RESTLinkObserved: true,
	}
	input.EnvelopeEvidence, err = NewPaginationEnvelopeEvidenceV1("evidence/single-page-boundary-envelope", input, limits)
	if err != nil {
		return PaginationPageV1{}, err
	}
	return NewPaginationPageV1(input, limits)
}

func makePaginationItems(count int) []CanonicalPaginationItemV1 {
	items := make([]CanonicalPaginationItemV1, count)
	for index := range items {
		items[index] = CanonicalPaginationItemV1{Key: fmt.Sprintf("item-%03d/", index), SHA256: fmt.Sprintf("%064x", index+1)}
	}
	return items
}

func paginationItemsFromClosure(closure PaginationClosureV1) []CanonicalPaginationItemV1 {
	var result []CanonicalPaginationItemV1
	for _, page := range closure.input.Pages {
		result = append(result, page.input.Items...)
	}
	return result
}

func mergeAuthorizationInputFromFixture(f fixture) MergeAuthorizationInputV1 {
	return MergeAuthorizationInputV1{
		Authority: f.mergeWrite.authority, PolicyDecisionSHA256: f.mergeWrite.policyDecisionSHA256,
		InitialPullRequest: f.mergeWrite.initialPullRequest, Checks: f.mergeWrite.checks,
		CheckRunsClosure: f.mergeWrite.checkRunsClosure, CommitStatusesClosure: f.mergeWrite.commitStatusesClosure,
		Capability: f.mergeWrite.capability, Recipe: f.mergeWrite.recipe, EvidenceRefs: f.mergeWrite.approvalEvidence,
	}
}

func longestJSONLLine(data []byte) int {
	longest := 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		longest = max(longest, len(line))
	}
	return longest
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}

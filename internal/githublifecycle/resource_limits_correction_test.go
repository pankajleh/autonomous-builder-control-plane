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
	}
	for _, value := range values {
		t.Run(value.name, func(t *testing.T) {
			if value.got != value.want {
				t.Fatalf("got %d, want %d", value.got, value.want)
			}
		})
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

func makePaginationClosure(f fixture, source PaginationSourceKind, pr *PullRequestIdentity, limits Limits, items []CanonicalPaginationItemV1) (PaginationClosureV1, error) {
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
		response, err := NewSnapshotIdentity("github", fmt.Sprintf("sized-%s-%02d", source, index), f.snapshot.ObservedUnixNano()+int64(index))
		if err != nil {
			return PaginationClosureV1{}, err
		}
		bodyEvidence := ledger.EvidenceRef{
			URI: fmt.Sprintf("evidence/sized-%s-%02d", source, index), Kind: GitHubPaginationBodyEvidenceKindV1,
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
		pageInput.EnvelopeEvidence, err = NewPaginationEnvelopeEvidenceV1(fmt.Sprintf("evidence/sized-envelope-%s-%02d", source, index), pageInput, limits)
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

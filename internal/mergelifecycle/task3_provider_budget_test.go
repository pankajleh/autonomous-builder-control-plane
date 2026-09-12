//go:build linux

package mergelifecycle

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTask3ClosureM01DurableHTTPCallBudgets(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	limits := productionLimits()
	store, err := newDurableStore(root, limits)
	if err != nil {
		t.Fatal(err)
	}
	attemptID := strings.Repeat("a", 64)
	attempt, err := store.openAttempt(attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if budget, err := attempt.providerBudget(); err != nil || budget.HTTPCalls != MaxProviderCalls {
		t.Fatalf("initial actual-call budget = %+v, %v", budget, err)
	}
	if _, err := attempt.reserveCounter("commit-submission"); err != nil {
		t.Fatal(err)
	}
	if _, err := attempt.reserveCounter("target-submission"); err != nil {
		t.Fatal(err)
	}
	if counters, err := attempt.currentCounters(); err != nil || counters.TotalProviderCalls != 0 || counters.ProviderAccountingPending {
		t.Fatalf("operation markers charged an HTTP call: %+v, %v", counters, err)
	}
	complete := func(class ProviderCallClassV1) {
		t.Helper()
		if _, _, err := attempt.reserveHTTPCall(class); err != nil {
			t.Fatalf("reserve %s: %v", class, err)
		}
		if _, err := attempt.accountHTTPCall(class, ProviderCallAccountingV1{}); err != nil {
			t.Fatalf("account %s: %v", class, err)
		}
	}
	for range MaxPreSubmitCalls {
		complete(ProviderCallPreSubmitV1)
	}
	complete(ProviderCallCommitSubmissionV1)
	complete(ProviderCallTargetV1)
	for range MaxPostMergeCalls {
		complete(ProviderCallPostMergeV1)
	}
	now := time.Unix(1700000000, 0).UnixNano()
	for round := range MaxReconciliationRounds {
		if _, err := attempt.reserveReconciliation(now + int64(round)*int64(MinimumReconciliationInterval)); err != nil {
			t.Fatalf("reserve reconciliation round %d: %v", round+1, err)
		}
		for range 3 {
			complete(ProviderCallReconciliationV1)
		}
	}
	counters, err := attempt.currentCounters()
	if err != nil || counters.PreSubmitCalls != 28 || counters.CommitSubmissions != 1 ||
		counters.TargetSubmissions != 1 || counters.PostMergeCalls != 8 ||
		counters.ReconciliationRounds != 8 || counters.ReconciliationCalls != 24 || counters.TotalProviderCalls != 62 || counters.ProviderAccountingPending {
		t.Fatalf("exact 28+1+1+8+24 allocation was not durable: %+v, %v", counters, err)
	}
	if budget, err := attempt.providerBudget(); err != nil || budget.HTTPCalls != 2 {
		t.Fatalf("reserved-two aggregate allocation = %+v, %v", budget, err)
	}
	for _, class := range []ProviderCallClassV1{ProviderCallPreSubmitV1, ProviderCallCommitSubmissionV1, ProviderCallTargetV1, ProviderCallPostMergeV1, ProviderCallReconciliationV1} {
		if _, _, err := attempt.reserveHTTPCall(class); err == nil {
			t.Fatalf("reserved validation allowance was reassigned to %s", class)
		}
	}
	if err := attempt.close(); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}

	pendingRoot := t.TempDir()
	if err := os.Chmod(pendingRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	pendingStore, err := newDurableStore(pendingRoot, limits)
	if err != nil {
		t.Fatal(err)
	}
	pendingID := strings.Repeat("b", 64)
	pending, err := pendingStore.openAttempt(pendingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := pending.reserveHTTPCall(ProviderCallPreSubmitV1); err != nil {
		t.Fatal(err)
	}
	_ = pending.close()
	_ = pendingStore.close()
	pendingStore, err = newDurableStore(pendingRoot, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer pendingStore.close()
	pending, err = pendingStore.openAttempt(pendingID)
	if err != nil {
		t.Fatal(err)
	}
	defer pending.close()
	if counters, err := pending.currentCounters(); err != nil || !counters.ProviderAccountingPending || counters.TotalProviderCalls != 1 {
		t.Fatalf("restart lost the unclosed actual call: %+v, %v", counters, err)
	}
	if _, err := pending.providerBudget(); err == nil {
		t.Fatal("restart regained budget with an unclosed call")
	}
	if _, _, err := pending.reserveHTTPCall(ProviderCallPreSubmitV1); err == nil {
		t.Fatal("restart permitted a call after ambiguous accounting")
	}

	verifyRoot := t.TempDir()
	if err := os.Chmod(verifyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	verifyStore, err := newDurableStore(verifyRoot, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer verifyStore.close()
	verifyAttempt, err := verifyStore.openAttempt(strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer verifyAttempt.close()
	controller := &Controller{limits: limits}
	ctx, cancel, session, err := controller.providerCallContext(context.Background(), verifyAttempt, []ProviderCallClassV1{ProviderCallPreSubmitV1})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	handoff, ok := ProviderHTTPCallHandoffFromContext(ctx)
	if !ok {
		t.Fatal("controller did not bind the HTTP-call handle")
	}
	reservation, err := handoff.ReserveHTTPCall(ProviderCallPreSubmitV1)
	if err != nil {
		t.Fatal(err)
	}
	call := ProviderCallAccountingV1{RequestBytes: 11, HeaderBytes: 12, CompressedResponseBytes: 13, DecompressedResponseBytes: 14, ActiveNanos: 15}
	if err := reservation.Complete(call); err != nil {
		t.Fatal(err)
	}
	aggregate := ProviderAccountingV1{HTTPCalls: 1, RequestBytes: 11, HeaderBytes: 12, CompressedResponseBytes: 13,
		DecompressedResponseBytes: 14, ActiveNanos: 15, InvocationNanos: 16}
	if err := session.verify(aggregate); err != nil {
		t.Fatalf("matching aggregate cross-check failed: %v", err)
	}
	aggregate.RequestBytes++
	if err := session.verify(aggregate); err == nil {
		t.Fatal("mismatched method aggregate was accepted")
	}
	if counters, err := verifyAttempt.currentCounters(); err != nil || counters.CumulativeRequestBytes != 11 || counters.TotalProviderCalls != 1 {
		t.Fatalf("aggregate cross-check applied a second charge: %+v, %v", counters, err)
	}
}

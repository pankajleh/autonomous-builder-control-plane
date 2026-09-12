//go:build linux

package mergelifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
)

func TestTask3FinalClosureM01FirstHTTPReservationDurability(t *testing.T) {
	limits := productionLimits()

	t.Run("directory sync failure fails closed and survives restart", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		store, err := newDurableStore(root, limits)
		if err != nil {
			t.Fatal(err)
		}
		attemptID := strings.Repeat("d", 64)
		attempt, err := store.openAttempt(attemptID)
		if err != nil {
			t.Fatal(err)
		}
		controller := &Controller{limits: limits}
		ctx, cancel, _, err := controller.providerCallContext(context.Background(), attempt, []ProviderCallClassV1{ProviderCallPreSubmitV1})
		if err != nil {
			t.Fatal(err)
		}
		cancel()

		originalSyncFile, originalSyncDir := store.syncFile, store.syncDir
		fileSynced, directorySyncs := false, 0
		store.syncFile = func(file *os.File) error {
			err := originalSyncFile(file)
			if err == nil && filepath.Base(file.Name()) == "counters.jsonl" {
				fileSynced = true
			}
			return err
		}
		store.syncDir = func(directory *os.File) error {
			if filepath.Clean(directory.Name()) == filepath.Clean(attempt.root) {
				if !fileSynced {
					t.Fatal("attempt directory was synced before the reservation file")
				}
				directorySyncs++
				return errors.New("injected first-reservation directory sync failure")
			}
			return originalSyncDir(directory)
		}

		handoff, ok := ProviderHTTPCallHandoffFromContext(ctx)
		if !ok {
			t.Fatal("controller did not bind the HTTP-call handoff")
		}
		transportCalls := 0
		if _, err := handoff.ReserveHTTPCall(ProviderCallPreSubmitV1); err == nil {
			transportCalls++
		}
		if transportCalls != 0 || !fileSynced || directorySyncs != 1 {
			t.Fatalf("failed durability boundary: transport=%d file_synced=%v directory_syncs=%d", transportCalls, fileSynced, directorySyncs)
		}
		if err := attempt.close(); err != nil {
			t.Fatal(err)
		}
		if err := store.close(); err != nil {
			t.Fatal(err)
		}

		store, err = newDurableStore(root, limits)
		if err != nil {
			t.Fatal(err)
		}
		defer store.close()
		attempt, err = store.openAttempt(attemptID)
		if err != nil {
			t.Fatal(err)
		}
		defer attempt.close()
		counters, err := attempt.currentCounters()
		if err != nil || counters.TotalProviderCalls != 1 || !counters.ProviderAccountingPending {
			t.Fatalf("restart lost the failed-closed reservation: counters=%+v err=%v", counters, err)
		}
		if _, err := attempt.providerBudget(); err == nil {
			t.Fatal("restart regained budget while the first call remained pending")
		}
		if _, _, err := attempt.reserveHTTPCall(ProviderCallPreSubmitV1); err == nil {
			t.Fatal("restart authorized transport after the failed durability boundary")
		}
	})

	t.Run("completed first reservation preserves every cumulative dimension", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		store, err := newDurableStore(root, limits)
		if err != nil {
			t.Fatal(err)
		}
		attemptID := strings.Repeat("e", 64)
		attempt, err := store.openAttempt(attemptID)
		if err != nil {
			t.Fatal(err)
		}
		originalSyncFile, originalSyncDir := store.syncFile, store.syncDir
		fileSynced, directorySynced := false, false
		store.syncFile = func(file *os.File) error {
			err := originalSyncFile(file)
			if err == nil && filepath.Base(file.Name()) == "counters.jsonl" {
				fileSynced = true
			}
			return err
		}
		store.syncDir = func(directory *os.File) error {
			if filepath.Clean(directory.Name()) == filepath.Clean(attempt.root) {
				if !fileSynced {
					t.Fatal("attempt directory was synced before the first reservation file")
				}
				directorySynced = true
			}
			return originalSyncDir(directory)
		}
		reservation, _, err := attempt.reserveHTTPCall(ProviderCallPreSubmitV1)
		if err != nil || reservation.TotalProviderCalls != 1 || !fileSynced || !directorySynced {
			t.Fatalf("first reservation durability = %+v, %v, file=%v dir=%v", reservation, err, fileSynced, directorySynced)
		}
		accounting := ProviderCallAccountingV1{RequestBytes: 11, HeaderBytes: 12, CompressedResponseBytes: 13, DecompressedResponseBytes: 14, ActiveNanos: 15}
		if _, err := attempt.accountHTTPCall(ProviderCallPreSubmitV1, accounting); err != nil {
			t.Fatal(err)
		}
		if err := attempt.close(); err != nil {
			t.Fatal(err)
		}
		if err := store.close(); err != nil {
			t.Fatal(err)
		}

		store, err = newDurableStore(root, limits)
		if err != nil {
			t.Fatal(err)
		}
		defer store.close()
		attempt, err = store.openAttempt(attemptID)
		if err != nil {
			t.Fatal(err)
		}
		defer attempt.close()
		budget, err := attempt.providerBudget()
		if err != nil || budget.HTTPCalls != limits.providerCalls-1 || budget.RequestBytes != limits.cumulativeRequestBytes-accounting.RequestBytes ||
			budget.HeaderBytes != limits.cumulativeHeaderBytes-accounting.HeaderBytes ||
			budget.CompressedResponseBytes != limits.cumulativeCompressedBytes-accounting.CompressedResponseBytes ||
			budget.DecompressedResponseBytes != limits.cumulativeDecompressedBytes-accounting.DecompressedResponseBytes ||
			budget.ActiveNanos != int64(limits.cumulativeProviderTime)-accounting.ActiveNanos {
			t.Fatalf("restart regained consumed cumulative budget: budget=%+v err=%v", budget, err)
		}
	})
}

func TestTask3FinalClosureM02BudgetExhaustionDisposition(t *testing.T) {
	t.Run("pre-target exhaustion", func(t *testing.T) {
		fixture := newControllerFixture(t)
		provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
		controller := fixture.controller(t, provider)
		defer controller.Close()
		constrainProviderCalls(controller, 3)

		result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: fixture.runID})
		var lifecycleErr *Error
		if !errors.As(err, &lifecycleErr) || lifecycleErr.Code != CodeResourceLimitExhausted || result.State != domain.StateFailed ||
			result.ReasonCode != CodeResourceLimitExhausted || result.Unresolved {
			t.Fatalf("pre-target exhaustion = %+v, %v", result, err)
		}
		if provider.submitCalls != 0 || provider.postMergeCalls != 0 || provider.totalCalls() != 3 {
			t.Fatalf("pre-target exhaustion reached mutation: submit=%d post=%d total=%d", provider.submitCalls, provider.postMergeCalls, provider.totalCalls())
		}
		if _, active, barrierErr := fixture.ledger.ActiveTransitionBarrier(fixture.runID); barrierErr != nil || active {
			t.Fatalf("pre-target exhaustion left an unresolved barrier: active=%v err=%v", active, barrierErr)
		}
		assertOneTerminalEvent(t, fixture.ledger.Path(), domain.StateFailed, CodeResourceLimitExhausted)
	})

	t.Run("settle preserves applied result", func(t *testing.T) {
		fixture := newControllerFixture(t)
		base := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
		provider := &exhaustAfterTargetProvider{fakeProvider: base}
		controller := fixture.controller(t, provider)
		defer controller.Close()
		provider.afterTarget = func() { constrainProviderCalls(controller, 4) }

		result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: fixture.runID})
		if err != nil || result.State != domain.StateFailed || result.ReasonCode != CodePostMergeAcceptanceFailed || result.MergeResult.SHA256() == "" {
			t.Fatalf("post-APPLIED exhaustion = %+v, %v", result, err)
		}
		if base.submitCalls != 1 || base.postMergeCalls != 0 || base.totalCalls() != 4 {
			t.Fatalf("post-APPLIED exhaustion calls: submit=%d post=%d total=%d", base.submitCalls, base.postMergeCalls, base.totalCalls())
		}
		assertOneTerminalEvent(t, fixture.ledger.Path(), domain.StateFailed, CodePostMergeAcceptanceFailed)
	})

	t.Run("restart recovers applied result before cancellation or observation", func(t *testing.T) {
		fixture := newControllerFixture(t)
		base := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
		provider := &exhaustAfterTargetProvider{fakeProvider: base}
		controller := fixture.controller(t, provider)
		crashedAfterApplied := false
		provider.afterTarget = func() { constrainProviderCalls(controller, 4) }
		controller.beforeTerminal = func(attempt *attemptStore) error {
			if _, found, readErr := attempt.read("merge-result.json"); readErr != nil || !found {
				t.Fatalf("terminal selection preceded durable APPLIED result: found=%v err=%v", found, readErr)
			}
			crashedAfterApplied = true
			return errors.New("injected crash after durable APPLIED result")
		}
		first, err := controller.Execute(context.Background(), ExecuteRequest{RunID: fixture.runID})
		if err == nil || first.State != "" || !crashedAfterApplied || base.submitCalls != 1 || base.postMergeCalls != 0 {
			t.Fatalf("APPLIED crash boundary = %+v, %v, crashed=%v submit=%d post=%d", first, err, crashedAfterApplied, base.submitCalls, base.postMergeCalls)
		}
		calls := base.totalCalls()
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}

		recovery := fixture.controller(t, base)
		defer recovery.Close()
		constrainProviderCalls(recovery, 4)
		result, err := recovery.Execute(context.Background(), ExecuteRequest{RunID: fixture.runID})
		if err != nil || result.State != domain.StateFailed || result.ReasonCode != CodePostMergeAcceptanceFailed || result.MergeResult.SHA256() == "" {
			t.Fatalf("recovered post-APPLIED exhaustion = %+v, %v", result, err)
		}
		if base.totalCalls() != calls || base.submitCalls != 1 || base.postMergeCalls != 0 {
			t.Fatalf("recovery called provider: calls=%d->%d submit=%d post=%d", calls, base.totalCalls(), base.submitCalls, base.postMergeCalls)
		}
		assertOneTerminalEvent(t, fixture.ledger.Path(), domain.StateFailed, CodePostMergeAcceptanceFailed)
	})

	t.Run("applied result defeats pending cancellation", func(t *testing.T) {
		fixture := newControllerFixture(t)
		base := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationUnknown, reconcileDisposition: githublifecycle.ReconciliationApplied}
		provider := &exhaustAfterTargetProvider{fakeProvider: base}
		controller := controllerWithCancellation(t, fixture, provider, cancellationGrant())
		defer controller.Close()
		provider.afterReconcile = func() { constrainProviderCalls(controller, 5) }

		first, err := controller.Execute(context.Background(), ExecuteRequest{RunID: fixture.runID})
		if err == nil || first.State != domain.StateReadyForMerge || !first.Unresolved {
			t.Fatalf("initial unresolved target = %+v, %v", first, err)
		}
		pending, err := controller.Cancel(context.Background(), CancelRequest{RunID: fixture.runID, SourceRequestID: "budget-cancellation"})
		if err != nil || pending.State != domain.StateReadyForMerge || !pending.Unresolved {
			t.Fatalf("pending cancellation = %+v, %v", pending, err)
		}
		result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: fixture.runID})
		if err != nil || result.State != domain.StateFailed || result.ReasonCode != CodePostMergeAcceptanceFailed || result.MergeResult.SHA256() == "" {
			t.Fatalf("APPLIED/cancellation precedence = %+v, %v", result, err)
		}
		if base.submitCalls != 1 || base.reconcileCalls != 1 || base.postMergeCalls != 0 || base.totalCalls() != 5 {
			t.Fatalf("APPLIED/cancellation calls: submit=%d reconcile=%d post=%d total=%d", base.submitCalls, base.reconcileCalls, base.postMergeCalls, base.totalCalls())
		}
		assertOneTerminalEvent(t, fixture.ledger.Path(), domain.StateFailed, CodePostMergeAcceptanceFailed)
	})
}

func constrainProviderCalls(controller *Controller, calls int) {
	controller.limits.providerCalls = calls
	controller.store.limits.providerCalls = calls
}

type exhaustAfterTargetProvider struct {
	*fakeProvider
	afterTarget    func()
	afterReconcile func()
}

func (p *exhaustAfterTargetProvider) SubmitTarget(ctx context.Context, input githublifecycle.MergeExecutionInputV1) (TargetOutcome, error) {
	outcome, err := p.fakeProvider.SubmitTarget(ctx, input)
	if p.afterTarget != nil {
		p.afterTarget()
	}
	return outcome, err
}

func (p *exhaustAfterTargetProvider) ReconcileTarget(ctx context.Context, input githublifecycle.ReconcileWriteInput) (TargetOutcome, error) {
	outcome, err := p.fakeProvider.ReconcileTarget(ctx, input)
	if p.afterReconcile != nil {
		p.afterReconcile()
	}
	return outcome, err
}

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

//go:build linux

package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

// Parallel builds (design note docs/plans/parallel-builds.md): run slots, the
// exclusive run, and the handoff locks that keep a live run's plan from being
// recovered by another run.

func briefly() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 150*time.Millisecond)
}

func TestExecutionSlotsAdmitUpToTheParallelCount(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	repository := fixture.authority.Repository().Path
	held := make([]*executionSlots, 0, 3)
	for index := 0; index < 3; index++ {
		slots, err := acquireExecutionSlots(context.Background(), repository, 3, false)
		if err != nil {
			t.Fatalf("slot %d: %v", index, err)
		}
		held = append(held, slots)
	}
	ctx, cancel := briefly()
	defer cancel()
	if fourth, err := acquireExecutionSlots(ctx, repository, 3, false); !errors.Is(err, context.DeadlineExceeded) {
		_ = fourth.Close()
		t.Fatalf("a fourth run with three slots = %v, want it to wait", err)
	}
	if err := held[1].Close(); err != nil {
		t.Fatal(err)
	}
	fourth, err := acquireExecutionSlots(context.Background(), repository, 3, false)
	if err != nil {
		t.Fatalf("a freed slot was not taken: %v", err)
	}
	for _, slots := range []*executionSlots{held[0], held[2], fourth} {
		if err := slots.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOneSlotKeepsRunsOneAtATime(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	repository := fixture.authority.Repository().Path
	first, err := acquireExecutionSlots(context.Background(), repository, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := briefly()
	defer cancel()
	if second, err := acquireExecutionSlots(ctx, repository, 1, false); !errors.Is(err, context.DeadlineExceeded) {
		_ = second.Close()
		t.Fatalf("a second run with one slot = %v, want it to wait", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing slots twice: %v", err)
	}
}

func TestAnExclusiveRunHoldsEverySlot(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	repository := fixture.authority.Repository().Path
	shared, err := acquireExecutionSlots(context.Background(), repository, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := briefly()
	if exclusive, err := acquireExecutionSlots(ctx, repository, 2, true); !errors.Is(err, context.DeadlineExceeded) {
		_ = exclusive.Close()
		t.Fatalf("an exclusive run beside a running one = %v, want it to wait", err)
	}
	cancel()
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	exclusive, err := acquireExecutionSlots(context.Background(), repository, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = briefly()
	defer cancel()
	if other, err := acquireExecutionSlots(ctx, repository, 2, false); !errors.Is(err, context.DeadlineExceeded) {
		_ = other.Close()
		t.Fatalf("a run beside an exclusive one = %v, want it to wait", err)
	}
	if err := exclusive.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionSlotsRefuseCountsOutOfRange(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	repository := fixture.authority.Repository().Path
	for _, slots := range []int{0, -1, MaxParallelRuns + 1} {
		if held, err := acquireExecutionSlots(context.Background(), repository, slots, false); err == nil {
			_ = held.Close()
			t.Fatalf("%d slots were accepted", slots)
		}
	}
	runner := fixture.runner(t)
	for _, runs := range []int{0, MaxParallelRuns + 1} {
		if err := runner.SetParallelRuns(runs); err == nil {
			t.Fatalf("SetParallelRuns(%d) was accepted", runs)
		}
	}
	if err := runner.SetParallelRuns(3); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryLeavesALiveRunsHandoffAndRemovesAnAbandonedOne(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	repository := fixture.authority.Repository().Path
	lease, err := acquireRepositoryExecutionLease(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	handoff := func(runID string) (string, string, *os.File) {
		t.Helper()
		digest := sha256.Sum256([]byte(runID))
		relative := ralphex.ExecutionPlanHandoffPrefixV1 + hex.EncodeToString(digest[:]) + "/plan.md"
		plan := filepath.Join(repository, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(plan), 0o700); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, plan, []byte("plan of "+runID+"\n"), 0o600)
		owner, lock, err := writeExecutionPlanHandoffOwner(lease, executionPlanHandoffOwnerV1{
			Kind: executionPlanOwnerKind, SchemaVersion: 1, RunID: runID, RelativePath: relative, SHA256: testHash(t, plan),
		})
		if err != nil {
			t.Fatal(err)
		}
		return plan, owner, lock
	}
	livePlan, liveOwner, liveLock := handoff("run-live")
	defer liveLock.Close()
	gonePlan, goneOwner, goneLock := handoff("run-gone")
	if err := goneLock.Close(); err != nil { // the run that wrote it has ended
		t.Fatal(err)
	}
	if err := recoverExecutionPlanHandoffs(lease); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{livePlan, liveOwner} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("a live run's handoff was recovered: %s: %v", filepath.Base(path), err)
		}
	}
	for _, path := range []string{gonePlan, goneOwner, filepath.Join(filepath.Dir(goneOwner), filepath.Base(goneOwner[:len(goneOwner)-len(".json")])+".lock")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("an abandoned handoff was left: %s: %v", filepath.Base(path), err)
		}
	}
	if live, err := executionPlanHandoffLive(liveOwner[:len(liveOwner)-len(".json")] + ".lock"); err != nil || !live {
		t.Fatalf("live handoff lock = %v, %v", live, err)
	}
	if _, err := lockExecutionPlanHandoff(liveOwner[:len(liveOwner)-len(".json")] + ".lock"); err == nil {
		t.Fatal("a second run took a live run's handoff lock")
	}
	if live, err := executionPlanHandoffLive(filepath.Join(filepath.Dir(liveOwner), "missing.lock")); err != nil || live {
		t.Fatalf("a missing handoff lock = %v, %v, want not live", live, err)
	}
}

func TestAWorktreeRunReleasesTheSetupLeaseOnceItsBranchMoves(t *testing.T) {
	signals := t.TempDir()
	ready, release := filepath.Join(signals, "ready"), filepath.Join(signals, "release")
	// Stands in for Ralphex: commits the plan on the run's branch, then keeps executing until released.
	script := "#!/bin/sh\n" +
		"c=$(git -c user.name=t -c user.email=t@example.invalid commit-tree \"$(git rev-parse 'HEAD^{tree}')\" -p HEAD -m 'add plan') || exit 70\n" +
		"git branch abcp/early-release \"$c\" || exit 71\n" +
		"printf ready > " + ready + "\n" +
		"i=0; while [ ! -f " + release + " ] && [ $i -lt 600 ]; do sleep 0.05; i=$((i+1)); done\n" +
		"exit 17\n"
	fixture := newRunFixtureWithScript(t, script, authority.WorktreePolicy{Enabled: true, Branch: "abcp/early-release"}, commandPath(t, "true"))
	configureIgnoredAuthorityPlan(t, &fixture, []byte("### Task 1: early release\n\n- [ ] keep executing\n"))
	repository := fixture.authority.Repository().Path
	runner := fixture.runner(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runner.Run(context.Background())
	}()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			writeTestFile(t, release, []byte("x"), 0o600)
			<-done
			t.Fatal("the stand-in Ralphex did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	lease, err := acquireRepositoryExecutionLease(ctx, repository)
	cancel()
	if err != nil {
		writeTestFile(t, release, []byte("x"), 0o600)
		<-done
		t.Fatalf("the setup lease was kept while the run executed: %v", err)
	}
	// With one slot (the default) the run still keeps the next one waiting.
	waitCtx, waitCancel := briefly()
	if slots, err := acquireExecutionSlots(waitCtx, repository, 1, false); !errors.Is(err, context.DeadlineExceeded) {
		_ = slots.Close()
		t.Errorf("a second run took the only slot while the first executed: %v", err)
	}
	waitCancel()
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, release, []byte("x"), 0o600)
	<-done
	matches, _ := filepath.Glob(filepath.Join(repository, ralphex.ExecutionPlanHandoffPrefixV1+"*"))
	if len(matches) != 0 {
		t.Fatalf("the run left its handoff after it ended: %v", matches)
	}
}

func TestValidationIgnoresOnlyARunningRunsPlanCopy(t *testing.T) {
	fixture := newRunFixtureWithScript(t, "#!/bin/sh\nexit 0\n", authority.WorktreePolicy{Enabled: true, Branch: "abcp/beside-a-running-run"}, commandPath(t, "true"))
	configureIgnoredAuthorityPlan(t, &fixture, []byte("### Task 1: beside\n\n- [ ] start beside a running run\n"))
	repository := fixture.authority.Repository().Path
	lease, err := acquireRepositoryExecutionLease(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("run-still-executing"))
	relative := ralphex.ExecutionPlanHandoffPrefixV1 + hex.EncodeToString(digest[:]) + "/plan.md"
	plan := filepath.Join(repository, filepath.FromSlash(relative))
	if err := os.Mkdir(filepath.Dir(plan), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, plan, []byte("### Task 1: other\n\n- [ ] other run\n"), 0o600)
	_, liveLock, err := writeExecutionPlanHandoffOwner(lease, executionPlanHandoffOwnerV1{
		Kind: executionPlanOwnerKind, SchemaVersion: 1, RunID: "run-still-executing", RelativePath: relative, SHA256: testHash(t, plan),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := validatePinnedIdentity(context.Background(), fixture.authority); err != nil {
		t.Fatalf("a running run's plan copy made the checkout unclean: %v", err)
	}
	stray := filepath.Join(repository, "stray.txt")
	writeTestFile(t, stray, []byte("x"), 0o600)
	if _, err := validatePinnedIdentity(context.Background(), fixture.authority); err == nil || !strings.Contains(err.Error(), "not clean") {
		t.Fatalf("a stray file beside a running run's plan copy = %v, want not clean", err)
	}
	if err := os.Remove(stray); err != nil {
		t.Fatal(err)
	}
	if err := liveLock.Close(); err != nil { // the other run has ended without removing its copy
		t.Fatal(err)
	}
	if _, err := validatePinnedIdentity(context.Background(), fixture.authority); err == nil || !strings.Contains(err.Error(), "not clean") {
		t.Fatalf("an abandoned plan copy = %v, want not clean", err)
	}
}

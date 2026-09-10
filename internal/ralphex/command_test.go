package ralphex

import (
	"reflect"
	"strings"
	"testing"

	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
)

func TestInvocationArgv(t *testing.T) {
	inv := Invocation{
		BinaryPath:   "/opt/ralphex",
		PlanPath:     "docs/plans/feature.md",
		ConfigDir:    "/etc/abcp/ralphex",
		Mode:         ModeTasksOnly,
		Codex:        true,
		Worktree:     true,
		Branch:       "feature-branch",
		TaskModel:    "gpt-task",
		TaskEffort:   "high",
		ReviewModel:  "gpt-review",
		ReviewEffort: "medium",
		WaitOnLimit:  "30m0s",
	}
	got, err := inv.Argv()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/opt/ralphex",
		"--config-dir", "/etc/abcp/ralphex",
		"--codex",
		"--wait", "30m0s",
		"--task-model", "gpt-task:high",
		"--review-model", "gpt-review:medium",
		"--tasks-only",
		"--worktree", "--branch", "feature-branch",
		"docs/plans/feature.md",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv mismatch\nwant: %#v\n got: %#v", want, got)
	}
}

func TestV3InvocationEmitsEveryStructuralBound(t *testing.T) {
	bounds := testBounds()
	capability := testCapability(ModeTasksOnly)
	invocation := Invocation{
		BinaryPath: "/opt/ralphex", PlanPath: "plan.md", ConfigDir: "/controller/config", Mode: ModeTasksOnly,
		Codex: true, TaskModel: "gpt-task", TaskEffort: "xhigh", ReviewModel: "gpt-review", ReviewEffort: "xhigh",
		BaseRef: strings.Repeat("a", 40), Bounds: &bounds, Capability: &capability,
		BinarySHA256: capability.BinarySHA256, SourceSHA: capability.SourceSHA,
	}
	argv, err := invocation.Argv()
	if err != nil {
		t.Fatal(err)
	}
	wantSubsequence := []string{"--max-iterations", "3", "--session-timeout", "30m0s", "--idle-timeout", "10m0s", "--skip-finalize", "--base-ref", strings.Repeat("a", 40)}
	if !containsSubsequence(argv, wantSubsequence) {
		t.Fatalf("bounded argv = %#v; missing %#v", argv, wantSubsequence)
	}
}

func TestV3InvocationRejectsUnsupportedCapabilityAndNativeFix(t *testing.T) {
	bounds := testBounds()
	capability := testCapability(ModeFull)
	capability.GovernedHandoff = HandoffTasksOnly
	_, err := (Invocation{
		BinaryPath: "/opt/ralphex", PlanPath: "plan.md", Mode: ModeFull,
		TaskEffort: "xhigh", ReviewEffort: "xhigh", BaseRef: strings.Repeat("a", 40), Bounds: &bounds,
		Capability: &capability, BinarySHA256: capability.BinarySHA256, SourceSHA: capability.SourceSHA,
	}).Argv()
	if err == nil || !strings.Contains(err.Error(), "stop-before-fix") {
		t.Fatalf("expected native fix handoff rejection, got %v", err)
	}
	capability = testCapability(ModeTasksOnly)
	capability.IdleTimeoutFlag = false
	if err := ValidateCapabilityV1(capability, capability.BinarySHA256, capability.SourceSHA, ModeTasksOnly); err == nil || !strings.Contains(err.Error(), "EXECUTION_BOUNDS_INVALID") {
		t.Fatalf("expected unsupported flag rejection, got %v", err)
	}
}

func TestCumulativeBoundsDoNotReset(t *testing.T) {
	bounds := testBounds()
	state := ExecutionStateV1{RalphexInvocations: bounds.MaxRalphexInvocations, AggregateElapsed: "1m0s"}
	if err := ValidateExecutionStateV1(bounds, state); err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("expected exhausted counter rejection, got %v", err)
	}
	state = ExecutionStateV1{AggregateElapsed: bounds.AggregateWallClockTimeout}
	if err := ValidateExecutionStateV1(bounds, state); err == nil {
		t.Fatal("exact aggregate ceiling was reset or admitted")
	}
}

func TestSingleIncompleteTaskIgnoresFencedExamplesAndRejectsMalformedFence(t *testing.T) {
	plan := []byte("```md\n### Task 99: example\n- [ ] not executable\n```\n### Task 1: real\n- [ ] execute\n### Task 2: done\n- [x] done\n")
	if err := ValidateSingleIncompleteTaskV1(plan); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSingleIncompleteTaskV1([]byte("```\n### Task 1: hidden\n- [ ] hidden\n")); err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("expected malformed fence rejection, got %v", err)
	}
}

func testBounds() contextcapsule.ExecutionBoundsV1 {
	return contextcapsule.ExecutionBoundsV1{
		MaxIterations: 3, SessionTimeout: "30m0s", IdleTimeout: "10m0s", WallClockTimeout: "1h0m0s", AggregateWallClockTimeout: "2h0m0s",
		MaxIncompleteTasks: 1, MaxInitialActiveFindings: 3, MaxRalphexInvocations: 2, MaxReviewReports: 3,
		MaxMutationLeases: 2, MaxTotalFixBatches: 2, MaxChangedFiles: 10, MaxChangedBytes: 1000,
	}
}

func testCapability(mode Mode) CapabilityV1 {
	handoff := HandoffTasksOnly
	if mode == ModeFull {
		handoff = HandoffStopBeforeFixBatch
	}
	if mode == ModeReview {
		handoff = HandoffReviewOnly
	}
	return CapabilityV1{
		Kind: "RalphexCapabilityV1", BinarySHA256: strings.Repeat("a", 64), SourceSHA: "source-v1",
		MaxIterationsFlag: true, SessionTimeoutFlag: true, IdleTimeoutFlag: true, SkipFinalizeFlag: true,
		BaseRefFlag: true, ExecutorModelEffortFlags: true, IsolatedConfig: true, GovernedHandoff: handoff, LinuxContainment: true,
	}
}

func containsSubsequence(values, subsequence []string) bool {
	for index := 0; index+len(subsequence) <= len(values); index++ {
		if reflect.DeepEqual(values[index:index+len(subsequence)], subsequence) {
			return true
		}
	}
	return false
}

func TestInvocationRejectsBranchWithoutWorktree(t *testing.T) {
	_, err := (Invocation{
		BinaryPath: "/opt/ralphex",
		PlanPath:   "docs/plans/feature.md",
		Mode:       ModeFull,
		Branch:     "feature-branch",
	}).Argv()
	if err == nil {
		t.Fatal("expected branch override without worktree to be rejected")
	}
}

func TestInvocationBuildsEffortOnlyModelPolicy(t *testing.T) {
	got, err := (Invocation{
		BinaryPath: "/opt/ralphex",
		PlanPath:   "plan.md",
		Mode:       ModeFull,
		TaskEffort: "xhigh",
	}).Argv()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/ralphex", "--task-model", ":xhigh", "plan.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv mismatch\nwant: %#v\n got: %#v", want, got)
	}
}

func TestInvocationRejectsReviewWorktree(t *testing.T) {
	_, err := (Invocation{
		BinaryPath: "/opt/ralphex",
		PlanPath:   "docs/plans/completed/feature.md",
		Mode:       ModeReview,
		Worktree:   true,
	}).Argv()
	if err == nil {
		t.Fatal("expected review + worktree to be rejected")
	}
}

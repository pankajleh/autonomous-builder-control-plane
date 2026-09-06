package ralphex

import (
	"reflect"
	"testing"
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

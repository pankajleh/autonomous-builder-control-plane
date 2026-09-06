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
		TaskModel:    "gpt-task",
		TaskEffort:   "high",
		ReviewModel:  "gpt-review",
		ReviewEffort: "medium",
	}
	got, err := inv.Argv()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/opt/ralphex",
		"--config-dir", "/etc/abcp/ralphex",
		"--codex",
		"--task-model", "gpt-task:high",
		"--review-model", "gpt-review:medium",
		"--tasks-only",
		"--worktree",
		"docs/plans/feature.md",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv mismatch\nwant: %#v\n got: %#v", want, got)
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

package ralphex

import (
	"fmt"
	"strings"
)

type Mode string

const (
	ModeFull      Mode = "full"
	ModeTasksOnly Mode = "tasks-only"
	ModeReview    Mode = "review"
)

type Invocation struct {
	BinaryPath   string
	PlanPath     string
	ConfigDir    string
	Mode         Mode
	Codex        bool
	Worktree     bool
	Branch       string
	TaskModel    string
	TaskEffort   string
	ReviewModel  string
	ReviewEffort string
	WaitOnLimit  string
}

func (i Invocation) Argv() ([]string, error) {
	if i.BinaryPath == "" {
		return nil, fmt.Errorf("ralphex binary path is required")
	}
	if i.PlanPath == "" {
		return nil, fmt.Errorf("plan path is required")
	}
	if i.Mode == "" {
		i.Mode = ModeFull
	}
	if i.Mode != ModeFull && i.Mode != ModeTasksOnly && i.Mode != ModeReview {
		return nil, fmt.Errorf("unsupported ralphex mode %q", i.Mode)
	}
	if i.Mode == ModeReview && i.Worktree {
		return nil, fmt.Errorf("worktree is not valid for review-only invocation")
	}
	if i.Worktree && i.Branch == "" {
		return nil, fmt.Errorf("branch override is required for worktree invocation")
	}
	if i.Branch != "" && !i.Worktree {
		return nil, fmt.Errorf("branch override requires worktree invocation")
	}

	argv := []string{i.BinaryPath}
	if i.ConfigDir != "" {
		argv = append(argv, "--config-dir", i.ConfigDir)
	}
	if i.Codex {
		argv = append(argv, "--codex")
	}
	if i.WaitOnLimit != "" {
		argv = append(argv, "--wait", i.WaitOnLimit)
	}
	taskModel, err := modelSpec(i.TaskModel, i.TaskEffort)
	if err != nil {
		return nil, fmt.Errorf("task model policy: %w", err)
	}
	if taskModel != "" {
		argv = append(argv, "--task-model", taskModel)
	}
	reviewModel, err := modelSpec(i.ReviewModel, i.ReviewEffort)
	if err != nil {
		return nil, fmt.Errorf("review model policy: %w", err)
	}
	if reviewModel != "" {
		argv = append(argv, "--review-model", reviewModel)
	}
	switch i.Mode {
	case ModeTasksOnly:
		argv = append(argv, "--tasks-only")
	case ModeReview:
		argv = append(argv, "--review")
	}
	if i.Worktree {
		argv = append(argv, "--worktree")
		argv = append(argv, "--branch", i.Branch)
	}
	argv = append(argv, i.PlanPath)
	return argv, nil
}

func modelSpec(model, effort string) (string, error) {
	if strings.Contains(model, ":") || strings.Contains(effort, ":") {
		return "", fmt.Errorf("model and effort must not contain ':'")
	}
	if model == "" && effort == "" {
		return "", nil
	}
	if effort == "" {
		return model, nil
	}
	return model + ":" + effort, nil
}

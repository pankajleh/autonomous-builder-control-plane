package ralphex

import (
	"fmt"
)

type Mode string

const (
	ModeFull      Mode = "full"
	ModeTasksOnly Mode = "tasks-only"
	ModeReview    Mode = "review"
)

type Invocation struct {
	BinaryPath string
	PlanPath   string
	ConfigDir  string
	Mode       Mode
	Codex      bool
	Worktree   bool
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

	argv := []string{i.BinaryPath}
	if i.ConfigDir != "" {
		argv = append(argv, "--config-dir", i.ConfigDir)
	}
	if i.Codex {
		argv = append(argv, "--codex")
	}
	switch i.Mode {
	case ModeTasksOnly:
		argv = append(argv, "--tasks-only")
	case ModeReview:
		argv = append(argv, "--review")
	}
	if i.Worktree {
		argv = append(argv, "--worktree")
	}
	argv = append(argv, i.PlanPath)
	return argv, nil
}

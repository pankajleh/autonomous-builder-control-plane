//go:build linux

package accountlane

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// runClaude runs `claude -p` once for a question, in a fresh empty folder of its own (mode 0700, removed afterwards)
// and in its own process group, which is killed when ctx ends and again once Claude Code has exited. Its standard
// error is discarded, never kept or logged.
func (m *Manager) runClaude(ctx context.Context, lane LaneV1, settings map[string]string, ask serviceapi.AccountLaneAskV1) (serviceapi.AccountLaneAnswerV1, error) {
	none := serviceapi.AccountLaneAnswerV1{}
	claude, err := m.claudeExecutable()
	if err != nil {
		return none, err
	}
	input, err := askInput(ask)
	if err != nil {
		return none, err
	}
	work, err := os.MkdirTemp("", "abcp-ask-")
	if err != nil {
		return none, err
	}
	defer os.RemoveAll(work)
	if err := os.Chmod(work, 0o700); err != nil {
		return none, err
	}
	command := exec.CommandContext(ctx, claude, askArguments(ask.System, m.ask.Model)...)
	command.Dir = work
	command.Env = askEnvironment(claude, lane, settings)
	command.Stdin = bytes.NewReader(input)
	output := &boundedOutput{}
	command.Stdout = output
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = 2 * time.Second
	_ = command.Run()
	if command.Process != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	if output.over {
		return none, errors.New("claude printed too much")
	}
	return parseAnswer(output.buffer.Bytes())
}

//go:build !linux

package integrationworkspace

import (
	"os"
	"os/exec"
)

func configureGitProcess(_ *exec.Cmd) {}

func cancelGitProcess(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	return command.Process.Kill()
}

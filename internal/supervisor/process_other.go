//go:build !linux

package supervisor

import (
	"os"
	"os/exec"
)

func configureProcess(_ *exec.Cmd) {}

func processGroupID(_ *exec.Cmd) int {
	return 0
}

func cancelProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}

func terminatingSignal(_ *exec.ExitError) string {
	return ""
}

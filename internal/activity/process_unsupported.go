//go:build !linux

package activity

import (
	"os"
	"os/exec"
)

func pinExecutable(*exec.Cmd, *os.File) {}
func ownsListener(int, int) bool        { return false }

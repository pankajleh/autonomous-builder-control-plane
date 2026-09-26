//go:build !linux

package preview

import "os/exec"

func configureProcess(*exec.Cmd) error { return ErrUnavailable }
func cancelProcess(*exec.Cmd) error    { return ErrUnavailable }

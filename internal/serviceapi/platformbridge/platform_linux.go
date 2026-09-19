//go:build linux

// Package platformbridge keeps the command's pre-existing Linux-only
// containment entry points compilable behind an explicit fail-closed platform
// boundary. It contains no HTTP behavior.
package platformbridge

import runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"

func IsContainmentChildV1(args []string) bool { return runctl.IsContainmentChildV1(args) }

func RunContainmentChildV1(args []string) int { return runctl.RunContainmentChildV1(args) }

func NewContainedCommandRunner(inner runctl.CommandRunner, cgroupRoot string) (runctl.CommandRunner, error) {
	return runctl.NewLinuxContainedCommandRunner(inner, cgroupRoot)
}

//go:build !linux

package platformbridge

import (
	"errors"

	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
)

func IsContainmentChildV1([]string) bool { return false }

func RunContainmentChildV1([]string) int { return 125 }

func NewContainedCommandRunner(runctl.CommandRunner, string) (runctl.CommandRunner, error) {
	return nil, errors.New("EXECUTION_BOUNDS_INVALID: strong Linux containment is unsupported on this platform")
}

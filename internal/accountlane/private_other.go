//go:build !linux

package accountlane

import (
	"context"
	"errors"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

func privateDirectory(string) error { return errors.New("account lanes need Linux") }

// CodexDeviceLogin needs Linux.
func CodexDeviceLogin(context.Context, string) (DeviceLogin, error) {
	return DeviceLogin{}, errors.New("account lanes need Linux")
}

// ReadFile needs Linux.
func ReadFile(string) (FileV1, error) { return FileV1{}, errors.New("account lanes need Linux") }

func (m *Manager) runClaude(context.Context, LaneV1, map[string]string, serviceapi.AccountLaneAskV1) (serviceapi.AccountLaneAnswerV1, error) {
	return serviceapi.AccountLaneAnswerV1{}, errors.New("account lanes need Linux")
}

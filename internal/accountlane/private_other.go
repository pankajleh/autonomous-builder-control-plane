//go:build !linux

package accountlane

import (
	"context"
	"errors"
)

func privateDirectory(string) error { return errors.New("account lanes need Linux") }

// CodexDeviceLogin needs Linux.
func CodexDeviceLogin(context.Context, string) (DeviceLogin, error) {
	return DeviceLogin{}, errors.New("account lanes need Linux")
}

// ReadFile needs Linux.
func ReadFile(string) (FileV1, error) { return FileV1{}, errors.New("account lanes need Linux") }

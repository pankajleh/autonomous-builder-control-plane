//go:build !linux

package openmodel

import (
	"context"
	"errors"
	"time"
)

// ErrNoSlot says every build slot of the model stayed taken until the
// deadline.
var ErrNoSlot = errors.New("every build slot of the model is taken")

// WriteHome runs on Linux only.
func WriteHome(string, string, []byte) error {
	return errors.New("open-model engines run on Linux only")
}

// AcquireSlot runs on Linux only.
func AcquireSlot(context.Context, string, int, time.Time, time.Duration) (func() error, error) {
	return nil, errors.New("open-model engines run on Linux only")
}

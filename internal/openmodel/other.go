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

// WriteHomeFiles runs on Linux only.
func WriteHomeFiles(string, string, ...HomeFile) error {
	return errors.New("open-model engines run on Linux only")
}

// HomeFile is one file the controller writes into a model's Codex home.
type HomeFile struct {
	Name string
	Data []byte
}

// AcquireSlot runs on Linux only.
func AcquireSlot(context.Context, string, int, time.Time, time.Duration) (func() error, error) {
	return nil, errors.New("open-model engines run on Linux only")
}

// AcquireNumberedSlot runs on Linux only.
func AcquireNumberedSlot(context.Context, string, int, time.Time, time.Duration) (func() error, int, error) {
	return nil, 0, errors.New("open-model engines run on Linux only")
}

// RelayLogFile is the relay's log in a model's Codex home.
const RelayLogFile = "relay.log"

type relayLog struct{}

func openRelayLog(string) (*relayLog, error) {
	return nil, errors.New("open-model engines run on Linux only")
}

func (*relayLog) Write([]byte) (int, error) {
	return 0, errors.New("open-model engines run on Linux only")
}

func (*relayLog) Close() error { return nil }

// UseHome is unsupported off Linux.
func (*Start) UseHome(string) error {
	return errors.New("OPEN_MODEL_ENGINE_HOME: unsupported on this platform")
}

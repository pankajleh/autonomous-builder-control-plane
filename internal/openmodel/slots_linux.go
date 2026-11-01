//go:build linux

package openmodel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrNoSlot says every build slot of the model stayed taken until the
// deadline.
var ErrNoSlot = errors.New("every build slot of the model is taken")

// AcquireSlot takes one of capacity build slots in dir, slot-<n>.lock files
// held with an exclusive flock (like the run slots), so the cap holds across
// every `abcp run` process and every profile that shares the folder. It tries
// again every poll until the deadline, then answers ErrNoSlot. The answer
// releases the slot; a process that dies releases it with its descriptor.
func AcquireSlot(ctx context.Context, dir string, capacity int, deadline time.Time, poll time.Duration) (func() error, error) {
	if capacity < 1 || capacity > 8 {
		return nil, errors.New("a model has 1 to 8 build slots")
	}
	if poll <= 0 {
		poll = 2 * time.Second
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the model's build slots: %w", err)
	}
	for _, folder := range []string{filepath.Dir(dir), dir} {
		if err := privateDirectory(folder); err != nil {
			return nil, err
		}
	}
	for {
		for slot := 1; slot <= capacity; slot++ {
			path := filepath.Join(dir, fmt.Sprintf("slot-%d.lock", slot))
			fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
			if err != nil {
				return nil, fmt.Errorf("open a build slot: %w", err)
			}
			var stat syscall.Stat_t
			if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || int(stat.Uid) != os.Getuid() || stat.Mode&0o077 != 0 {
				syscall.Close(fd)
				return nil, errors.New("a build slot is not a private file of the controller's user")
			}
			if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
				released := false
				return func() error {
					if released {
						return nil
					}
					released = true
					return errors.Join(syscall.Flock(fd, syscall.LOCK_UN), syscall.Close(fd))
				}, nil
			}
			syscall.Close(fd)
		}
		wait := min(poll, time.Until(deadline))
		if wait <= 0 {
			return nil, ErrNoSlot
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, context.Cause(ctx)
		case <-timer.C:
		}
	}
}

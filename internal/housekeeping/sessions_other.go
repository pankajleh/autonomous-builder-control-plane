//go:build !linux

package housekeeping

import (
	"errors"
	"time"
)

// removeOldSessionLogs walks by directory descriptors, which only the Linux build does.
func removeOldSessionLogs(string, time.Time, func(string) bool) (int, error) {
	return 0, errors.New("housekeeping: session logs are aged on Linux only")
}

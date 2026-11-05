//go:build !linux

package housekeeping

import (
	"errors"
	"time"
)

// removeOldSessionLogs walks by directory descriptors, which only the Linux build does.
func removeOldSessionLogs(string, time.Time, SessionPolicy, func(string) bool) (SessionSweep, error) {
	return SessionSweep{}, errors.New("housekeeping: session logs are aged on Linux only")
}

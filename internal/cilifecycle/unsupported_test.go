//go:build !linux

package cilifecycle

import (
	"errors"
	"testing"
)

func TestUnsupportedAttemptLeasePoisonFailsClosed(t *testing.T) {
	if err := (&attemptLease{}).poison(); !errors.Is(err, errUnsupportedCIDurability) {
		t.Fatalf("unsupported attempt poison error = %v", err)
	}
}

//go:build !linux

package preview

import (
	"context"
	"testing"
)

func TestUnsupportedPlatformCannotEnableRuntime(t *testing.T) {
	if service, err := New(context.Background(), t.TempDir(), "", nil, nil); err == nil || service != nil {
		t.Fatal("unsupported filesystem enabled runtime")
	}
}

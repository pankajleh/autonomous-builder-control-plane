//go:build !linux

package actioncontrol

import "context"

type resourceGuard struct{}
type resourceLease struct{}

func openResourceGuard(string) (*resourceGuard, error) { return nil, ErrUnsupported }
func (*resourceGuard) acquire(context.Context, int, int) (*resourceLease, error) {
	return nil, ErrUnsupported
}
func (*resourceGuard) Close() error { return nil }
func (*resourceLease) Close() error { return nil }

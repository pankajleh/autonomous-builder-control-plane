//go:build !linux

package run

import (
	"context"
	"errors"
)

func repositoryExecutionLeasingSupported() bool { return false }

func (l *repositoryExecutionLease) Close() error { return nil }

func acquireRepositoryExecutionLease(context.Context, string) (*repositoryExecutionLease, error) {
	return nil, errors.New("repository execution leasing is supported only on Linux")
}

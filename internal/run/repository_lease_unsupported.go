//go:build !linux

package run

import (
	"context"
	"errors"
	"os"
)

func repositoryExecutionLeasingSupported() bool { return false }

func (l *repositoryExecutionLease) Close() error { return nil }

func (l *repositoryExecutionLease) held() bool { return false }

func acquireRepositoryExecutionLease(context.Context, string) (*repositoryExecutionLease, error) {
	return nil, errors.New("repository execution leasing is supported only on Linux")
}

func acquireExecutionSlots(context.Context, string, int, bool) (*executionSlots, error) {
	return nil, errors.New("run slots are supported only on Linux")
}

func (s *executionSlots) Close() error { return nil }

func lockExecutionPlanHandoff(string) (*os.File, error) {
	return nil, errors.New("Ralphex execution plan handoff locks are supported only on Linux")
}

func executionPlanHandoffLive(string) (bool, error) {
	return false, errors.New("Ralphex execution plan handoff locks are supported only on Linux")
}

func gitCommonDirectory(context.Context, string) (string, error) {
	return "", errors.New("repository execution leasing is supported only on Linux")
}

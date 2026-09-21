//go:build linux

package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func repositoryExecutionLeasingSupported() bool { return true }

func (l *repositoryExecutionLease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	return errors.Join(syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
}

func acquireRepositoryExecutionLease(ctx context.Context, repository string) (*repositoryExecutionLease, error) {
	common, err := gitOutput(context.WithoutCancel(ctx), repository, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("resolve Git common directory: %w", err)
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(repository, common)
	}
	common, err = filepath.EvalSymlinks(filepath.Clean(common))
	if err != nil {
		return nil, fmt.Errorf("canonicalize Git common directory: %w", err)
	}
	lockPath := filepath.Join(common, executionPlanLockName)
	fd, err := syscall.Open(lockPath, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open repository execution lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), lockPath)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		file.Close()
		return nil, errors.Join(errors.New("repository execution lock is not a protected regular file"), err)
	}
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			file.Close()
			return nil, fmt.Errorf("lock repository execution: %w", err)
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, context.Cause(ctx)
		case <-time.After(25 * time.Millisecond):
		}
	}
	return &repositoryExecutionLease{
		file: file, repository: repository,
		ownersDir: executionPlanOwnersDirectory(common, repository),
	}, nil
}

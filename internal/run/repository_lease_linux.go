//go:build linux

package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func repositoryExecutionLeasingSupported() bool { return true }

// Close releases the lease. It may be called more than once, and from the
// worktree-setup watcher and the run concurrently.
func (l *repositoryExecutionLease) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	return errors.Join(syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
}

func (l *repositoryExecutionLease) held() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file != nil
}

func gitCommonDirectory(ctx context.Context, repository string) (string, error) {
	common, err := gitOutput(context.WithoutCancel(ctx), repository, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("resolve Git common directory: %w", err)
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(repository, common)
	}
	common, err = filepath.EvalSymlinks(filepath.Clean(common))
	if err != nil {
		return "", fmt.Errorf("canonicalize Git common directory: %w", err)
	}
	return common, nil
}

// openProtectedLockFile opens, creating when absent, a 0600 regular lock file
// that is not a symbolic link.
func openProtectedLockFile(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		file.Close()
		return nil, errors.Join(fmt.Errorf("%s is not a protected regular file", filepath.Base(path)), err)
	}
	return file, nil
}

func tryLock(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

// waitForLock takes an exclusive flock, polling so that ctx can end the wait.
// A cancelled wait returns the context's cause unwrapped.
func waitForLock(ctx context.Context, file *os.File, every time.Duration) error {
	for {
		locked, err := tryLock(file)
		if err != nil {
			return fmt.Errorf("lock %s: %w", filepath.Base(file.Name()), err)
		}
		if locked {
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(every):
		}
	}
}

func acquireRepositoryExecutionLease(ctx context.Context, repository string) (*repositoryExecutionLease, error) {
	common, err := gitCommonDirectory(ctx, repository)
	if err != nil {
		return nil, err
	}
	file, err := openProtectedLockFile(filepath.Join(common, executionPlanLockName))
	if err != nil {
		return nil, errors.Join(errors.New("repository execution lock is not a protected regular file"), err)
	}
	if err := waitForLock(ctx, file, 25*time.Millisecond); err != nil {
		file.Close()
		return nil, err
	}
	return &repositoryExecutionLease{
		file: file, repository: repository,
		ownersDir: executionPlanOwnersDirectory(common, repository),
	}, nil
}

// acquireExecutionSlots takes the run slots a run executes under (design note
// docs/plans/parallel-builds.md): one free slot of slots, or every slot when
// exclusive. With one slot the runs of a repository are serialised.
func acquireExecutionSlots(ctx context.Context, repository string, slots int, exclusive bool) (*executionSlots, error) {
	if slots < 1 || slots > MaxParallelRuns {
		return nil, fmt.Errorf("parallel runs must be between 1 and %d", MaxParallelRuns)
	}
	common, err := gitCommonDirectory(ctx, repository)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(common, executionSlotDirectoryName)
	if err := ensureProtectedRunDirectory(directory); err != nil {
		return nil, fmt.Errorf("prepare run slots: %w", err)
	}
	files := make([]*os.File, 0, slots)
	closeAll := func() {
		for _, file := range files {
			_ = file.Close()
		}
	}
	for index := 0; index < slots; index++ {
		file, err := openProtectedLockFile(filepath.Join(directory, "slot-"+strconv.Itoa(index)+".lock"))
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("open run slot: %w", err)
		}
		files = append(files, file)
	}
	if exclusive {
		// In slot order, so two exclusive runs can never each hold part of the set.
		for _, file := range files {
			if err := waitForLock(ctx, file, 250*time.Millisecond); err != nil {
				closeAll()
				return nil, err
			}
		}
		return &executionSlots{files: files}, nil
	}
	for {
		for index, file := range files {
			locked, err := tryLock(file)
			if err != nil {
				closeAll()
				return nil, fmt.Errorf("lock run slot: %w", err)
			}
			if locked {
				for other, rest := range files {
					if other != index {
						_ = rest.Close()
					}
				}
				return &executionSlots{files: []*os.File{file}}, nil
			}
		}
		select {
		case <-ctx.Done():
			closeAll()
			return nil, context.Cause(ctx)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (s *executionSlots) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	for _, file := range s.files {
		err = errors.Join(err, syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
	}
	s.files = nil
	return err
}

// lockExecutionPlanHandoff takes the run's lock beside its handoff ownership
// record. Another run's recovery leaves the handoff alone while it is held.
func lockExecutionPlanHandoff(path string) (*os.File, error) {
	file, err := openProtectedLockFile(path)
	if err != nil {
		return nil, err
	}
	locked, err := tryLock(file)
	if err != nil || !locked {
		file.Close()
		return nil, errors.Join(errors.New("Ralphex execution plan handoff is held by another run"), err)
	}
	return file, nil
}

// executionPlanHandoffLive reports whether a live run holds the lock at path.
// A missing lock is a handoff from before handoff locks, or one abandoned
// before its lock was taken: not live.
func executionPlanHandoffLive(path string) (bool, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return false, errors.Join(errors.New("Ralphex execution plan handoff lock is not a protected regular file"), err)
	}
	locked, err := tryLock(file)
	if err != nil {
		return false, err
	}
	return !locked, nil
}

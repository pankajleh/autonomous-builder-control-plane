//go:build linux

package run

import (
	"errors"
	"fmt"
	"syscall"
)

// pointRunBranch points the branch refs/heads/<name> of the run's repository (<folder>/repo) at sha by writing its
// loose reference file. The run's repository is the build's to write, so every folder on the way is opened without
// following a link (a folder swapped for a link is refused), and the file is written beside its place and renamed over
// it, which replaces a link there rather than following it. No Git command runs in the run's repository.
func pointRunBranch(folder, name, sha string) error {
	if !fullCommitSHA.MatchString(sha) {
		return fmt.Errorf("%q is not a full commit id", sha)
	}
	if !guardedBranch.MatchString(name) {
		return fmt.Errorf("%q cannot name a branch here", name)
	}
	const dirFlags = syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	dir, err := syscall.Open(folder, dirFlags, 0)
	if err != nil {
		return fmt.Errorf("open the run's folder: %w", err)
	}
	for _, part := range []string{"repo", ".git", "refs", "heads"} {
		next, err := syscall.Openat(dir, part, dirFlags, 0)
		_ = syscall.Close(dir)
		if err != nil {
			return fmt.Errorf("open %s without following a link: %w", part, err)
		}
		dir = next
	}
	defer syscall.Close(dir)
	temp := "." + name + ".abcp"
	_ = syscall.Unlinkat(dir, temp)
	fd, err := syscall.Openat(dir, temp, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o644)
	if err != nil {
		return fmt.Errorf("write the branch beside its place: %w", err)
	}
	data := []byte(sha + "\n")
	n, writeErr := syscall.Write(fd, data)
	if writeErr == nil && n != len(data) {
		writeErr = errors.New("short write")
	}
	if err := errors.Join(writeErr, syscall.Fsync(fd), syscall.Close(fd)); err != nil {
		_ = syscall.Unlinkat(dir, temp)
		return fmt.Errorf("write the branch: %w", err)
	}
	if err := syscall.Renameat(dir, temp, dir, name); err != nil {
		_ = syscall.Unlinkat(dir, temp)
		return fmt.Errorf("put the branch in place: %w", err)
	}
	return nil
}

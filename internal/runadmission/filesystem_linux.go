//go:build linux

package runadmission

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func canonicalAbsolute(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator)
}

func canonicalRelativeDirectory(path string) bool {
	return path != "" && len(path) <= 1024 && !filepath.IsAbs(path) && filepath.VolumeName(path) == "" &&
		!strings.Contains(path, "\\") && filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) == path &&
		path != "." && path != ".." && !strings.HasPrefix(path, "../")
}

func pathsOverlap(first, second string) bool {
	within := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	return within(first, second) || within(second, first)
}

func openAbsoluteDirectory(path string, create bool) (int, error) {
	if !canonicalAbsolute(path) {
		return -1, errors.New("directory path is not canonical and absolute")
	}
	fd, err := syscall.Open(string(filepath.Separator), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		next, openErr := syscall.Openat(fd, component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if openErr == syscall.ENOENT && create {
			if mkdirErr := syscall.Mkdirat(fd, component, 0o700); mkdirErr != nil && mkdirErr != syscall.EEXIST {
				syscall.Close(fd)
				return -1, mkdirErr
			}
			next, openErr = syscall.Openat(fd, component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		}
		syscall.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func openRelativeDirectory(parent int, relative string, create bool) (int, error) {
	if !canonicalRelativeDirectory(relative) {
		return -1, errors.New("directory path is not canonical and relative")
	}
	fd, err := syscall.Openat(parent, ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range strings.Split(relative, "/") {
		next, openErr := syscall.Openat(fd, component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if openErr == syscall.ENOENT && create {
			if mkdirErr := syscall.Mkdirat(fd, component, 0o700); mkdirErr != nil && mkdirErr != syscall.EEXIST {
				syscall.Close(fd)
				return -1, mkdirErr
			}
			next, openErr = syscall.Openat(fd, component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		}
		syscall.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func openChildDirectory(parent int, name string, create bool) (int, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return -1, errors.New("invalid directory name")
	}
	fd, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err == syscall.ENOENT && create {
		if mkdirErr := syscall.Mkdirat(parent, name, 0o700); mkdirErr != nil && mkdirErr != syscall.EEXIST {
			return -1, mkdirErr
		}
		fd, err = syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	}
	return fd, err
}

func ensureLockFile(parent int, name string) error {
	fd, err := syscall.Openat(parent, name, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	return validateRegularFD(fd, true)
}

func readRegularAt(parent int, name string, maximum int) ([]byte, bool, error) {
	fd, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err == syscall.ENOENT {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	file := os.NewFile(uintptr(fd), "run-admission-record")
	defer file.Close()
	if err := validateRegularFD(fd, true); err != nil {
		return nil, false, err
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(data) > maximum {
		return nil, false, errors.New("record exceeds bounds")
	}
	return data, true, nil
}

func validateRegularFD(fd int, private bool) error {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Nlink != 1 {
		return errors.New("record is not a single regular file")
	}
	if private && (stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o7777 != 0o600) {
		return errors.New("record ownership or mode is unsafe")
	}
	return nil
}

func validatePrivateDirectoryFD(fd int) error {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFDIR ||
		stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o7777 != 0o700 {
		return errors.New("private directory ownership or mode is unsafe")
	}
	return nil
}

func validateRepositoryInputDirectoryFD(fd int) error {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFDIR ||
		stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o022 != 0 {
		return errors.New("repository input directory ownership or mode is unsafe")
	}
	return nil
}

func writeNewAtomicAt(parent int, name string, data []byte) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temporary := ".admission-" + hex.EncodeToString(nonce[:])
	fd, err := syscall.Openat(parent, temporary, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		if remove {
			_ = syscall.Unlinkat(parent, temporary)
		}
	}()
	for written := 0; written < len(data); {
		count, writeErr := syscall.Write(fd, data[written:])
		if writeErr != nil || count <= 0 {
			syscall.Close(fd)
			if writeErr != nil {
				return writeErr
			}
			return io.ErrShortWrite
		}
		written += count
	}
	if err := syscall.Fsync(fd); err != nil {
		syscall.Close(fd)
		return err
	}
	if err := syscall.Close(fd); err != nil {
		return err
	}
	if err := syscall.Renameat(parent, temporary, parent, name); err != nil {
		return err
	}
	remove = false
	return syscall.Fsync(parent)
}

func createOrVerifyAt(parent int, name string, expected []byte) error {
	data, found, err := readRegularAt(parent, name, MaxManifestTemplateBytes)
	if err != nil {
		return err
	}
	if found {
		if !bytes.Equal(data, expected) {
			return errors.New("existing admission material differs")
		}
		return nil
	}
	if err := writeNewAtomicAt(parent, name, expected); err != nil {
		return err
	}
	data, found, err = readRegularAt(parent, name, MaxManifestTemplateBytes)
	if err != nil || !found || !bytes.Equal(data, expected) {
		return errors.New("admission material verification failed")
	}
	return nil
}

func boundedFlock(ctx context.Context, fd int, maximum time.Duration) error {
	deadline := time.Now().Add(maximum)
	for {
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errors.New("admission lock timed out")
		}
		if remaining > 10*time.Millisecond {
			remaining = 10 * time.Millisecond
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func acquireLaunchLock(parent int, runID string) (*os.File, bool, error) {
	name := runID + ".lock"
	fd, err := syscall.Openat(parent, name, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := validateRegularFD(fd, true); err != nil {
		syscall.Close(fd)
		return nil, false, err
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		syscall.Close(fd)
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, false, nil
		}
		return nil, false, err
	}
	return os.NewFile(uintptr(fd), "run-admission-launch-lock"), true, nil
}

func readProtectedFile(path string, maximum int) ([]byte, error) {
	if !canonicalAbsolute(path) {
		return nil, errors.New("protected path is not canonical and absolute")
	}
	fd, err := syscall.Open(string(filepath.Separator), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	for index, component := range parts {
		flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
		if index != len(parts)-1 {
			flags |= syscall.O_DIRECTORY
		}
		next, openErr := syscall.Openat(fd, component, flags, 0)
		syscall.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	file := os.NewFile(uintptr(fd), "protected-admission-config")
	defer file.Close()
	if err := validateRegularFD(fd, true); err != nil {
		return nil, err
	}
	var initial syscall.Stat_t
	if err := syscall.Fstat(fd, &initial); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(data) > maximum {
		return nil, errors.New("protected file exceeds bounds")
	}
	var current syscall.Stat_t
	if err := syscall.Fstat(fd, &current); err != nil || current.Dev != initial.Dev || current.Ino != initial.Ino || current.Size != initial.Size {
		return nil, errors.New("protected file changed while reading")
	}
	return data, nil
}

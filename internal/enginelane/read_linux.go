//go:build linux

package enginelane

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// readPrivate opens path one component at a time without following links, and
// reads it only when it is a regular file owned by this process's user and
// readable by that user alone.
func readPrivate(path string) ([]byte, error) { return ReadPrivate(path, MaxBytes) }

// ReadPrivate is readPrivate for another bound: an open-model engine's key file
// (Repo C design note OPEN_MODEL_ENGINES) is read under the same rules. A file
// that is not there answers an error that wraps os.ErrNotExist.
func ReadPrivate(path string, maxBytes int64) ([]byte, error) {
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
			// A link as the last part answers ELOOP; a link (or a file) where a
			// folder should be answers ENOTDIR, since folders are opened with
			// O_DIRECTORY.
			if openErr == syscall.ELOOP || (openErr == syscall.ENOTDIR && index != len(parts)-1) {
				return nil, errors.New("file or a folder above it is a symbolic link or not a folder")
			}
			if openErr == syscall.ENOENT {
				return nil, fmt.Errorf("file is not there: %w", os.ErrNotExist)
			}
			return nil, errors.New("file cannot be opened")
		}
		fd = next
	}
	file := os.NewFile(uintptr(fd), "engine-lane")
	defer file.Close()
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return nil, errors.New("file is not a regular file")
	}
	if int(stat.Uid) != os.Getuid() {
		return nil, errors.New("file is not owned by the controller's user")
	}
	if stat.Mode&0o077 != 0 {
		return nil, errors.New("file is readable by others: it must be mode 0600")
	}
	if stat.Size > maxBytes {
		return nil, errors.New("file is too large")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("file is too large")
	}
	return data, nil
}

//go:build linux

package enginelane

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// readPrivate opens path one component at a time without following links, and
// reads it only when it is a regular file owned by this process's user and
// readable by that user alone.
func readPrivate(path string) ([]byte, error) {
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
	if stat.Size > MaxBytes {
		return nil, errors.New("file is too large")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxBytes {
		return nil, errors.New("file is too large")
	}
	return data, nil
}

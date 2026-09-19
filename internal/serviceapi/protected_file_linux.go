//go:build linux

package serviceapi

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func readProtectedFile(path string, maximum int) ([]byte, error) {
	absolute, err := filepath.Abs(path)
	if err != nil || filepath.Clean(absolute) != absolute {
		return nil, ErrUnsafeConfig
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrUnsafeConfig
	}
	parts := strings.Split(strings.TrimPrefix(absolute, "/"), "/")
	for index, part := range parts {
		last := index == len(parts)-1
		flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
		if !last {
			flags |= syscall.O_DIRECTORY
		}
		next, openErr := syscall.Openat(fd, part, flags, 0)
		syscall.Close(fd)
		if openErr != nil {
			return nil, ErrUnsafeConfig
		}
		fd = next
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || !validProtectedConfigStat(stat) {
		syscall.Close(fd)
		return nil, ErrUnsafeConfig
	}
	initial := stat
	file := os.NewFile(uintptr(fd), "protected-service-config")
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(data) > maximum {
		return nil, ErrUnsafeConfig
	}
	// Recheck the exact opened descriptor after the read. Replacement of the
	// path cannot redirect bytes, and link/mode/owner changes fail startup.
	if err := syscall.Fstat(fd, &stat); err != nil || !validReopenedConfigStat(stat, initial) {
		return nil, ErrUnsafeConfig
	}
	reopened, err := openProtectedPath(absolute)
	if err != nil {
		return nil, ErrUnsafeConfig
	}
	defer syscall.Close(reopened)
	if err := syscall.Fstat(reopened, &stat); err != nil || !validReopenedConfigStat(stat, initial) {
		return nil, ErrUnsafeConfig
	}
	return data, nil
}

func validProtectedConfigStat(stat syscall.Stat_t) bool {
	return stat.Mode&syscall.S_IFMT == syscall.S_IFREG && stat.Uid == uint32(os.Geteuid()) && stat.Mode&0o7777 == 0o600 && stat.Nlink == 1
}

func validReopenedConfigStat(current, initial syscall.Stat_t) bool {
	return validProtectedConfigStat(current) && current.Dev == initial.Dev && current.Ino == initial.Ino
}

func openProtectedPath(absolute string) (int, error) {
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	parts := strings.Split(strings.TrimPrefix(absolute, "/"), "/")
	for index, part := range parts {
		last := index == len(parts)-1
		flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
		if !last {
			flags |= syscall.O_DIRECTORY
		}
		next, openErr := syscall.Openat(fd, part, flags, 0)
		syscall.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

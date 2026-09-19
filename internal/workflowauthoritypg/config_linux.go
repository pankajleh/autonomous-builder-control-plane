//go:build linux

package workflowauthoritypg

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func readProtectedConfiguration(path string, maximum int) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return nil, errConfiguration
	}
	fd, err := syscall.Open(string(filepath.Separator), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errConfiguration
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
			return nil, errConfiguration
		}
		fd = next
	}
	file := os.NewFile(uintptr(fd), "workflow-authority-configuration")
	defer file.Close()
	var before syscall.Stat_t
	if err := syscall.Fstat(fd, &before); err != nil || before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Nlink != 1 ||
		before.Uid != uint32(os.Geteuid()) || before.Mode&0o7777 != 0o600 {
		return nil, errConfiguration
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(data) > maximum {
		return nil, errConfiguration
	}
	var after syscall.Stat_t
	if err := syscall.Fstat(fd, &after); err != nil || before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size {
		return nil, errConfiguration
	}
	return data, nil
}

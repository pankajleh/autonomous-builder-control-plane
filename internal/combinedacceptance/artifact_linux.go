//go:build linux

package combinedacceptance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// openArtifactNoSymlinks walks an absolute path through directory descriptors.
// O_NOFOLLOW rejects symlinks at every component, while O_NONBLOCK ensures a
// raced FIFO or device cannot block before the caller verifies the opened type.
func openArtifactNoSymlinks(path string) (*os.File, error) {
	separator := string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(path, separator), separator)
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return nil, errors.New("evidence artifact path must name a file")
	}

	directoryFD, err := syscall.Open(separator, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open filesystem root: %w", err)
	}
	for index, part := range parts {
		last := index == len(parts)-1
		flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
		if !last {
			flags |= syscall.O_DIRECTORY
		}
		nextFD, openErr := syscall.Openat(directoryFD, part, flags, 0)
		closeErr := syscall.Close(directoryFD)
		if openErr != nil {
			return nil, fmt.Errorf("open path component %q: %w", part, errors.Join(openErr, closeErr))
		}
		if closeErr != nil {
			syscall.Close(nextFD)
			return nil, fmt.Errorf("close parent path component: %w", closeErr)
		}
		if last {
			file := os.NewFile(uintptr(nextFD), path)
			if file == nil {
				syscall.Close(nextFD)
				return nil, errors.New("create evidence artifact file handle")
			}
			return file, nil
		}
		directoryFD = nextFD
	}
	return nil, errors.New("evidence artifact path must name a file")
}

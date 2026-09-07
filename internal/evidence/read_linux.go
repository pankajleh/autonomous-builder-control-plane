//go:build linux

package evidence

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func readBoundedLocal(root, path string, maximumBytes int64) ([]byte, error) {
	rootHandle, err := openNoSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("open controller evidence root: %w", err)
	}
	rootInfo, statErr := rootHandle.Stat()
	closeErr := rootHandle.Close()
	if statErr != nil || closeErr != nil {
		return nil, fmt.Errorf("inspect controller evidence root: %w", errors.Join(statErr, closeErr))
	}
	if !rootInfo.IsDir() {
		return nil, errors.New("controller evidence root is not a directory")
	}

	file, err := openNoSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("open evidence artifact: %w", err)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect evidence artifact: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("evidence artifact must be a regular file, not a symlink or special file")
	}
	if before.Size() < 0 || before.Size() > maximumBytes {
		return nil, fmt.Errorf("evidence artifact is %d bytes; maximum is %d", before.Size(), maximumBytes)
	}
	beforeIdentity, err := fileIdentity(before)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read evidence artifact: %w", err)
	}
	if int64(len(data)) > maximumBytes {
		return nil, fmt.Errorf("evidence artifact exceeds maximum %d bytes", maximumBytes)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("reinspect evidence artifact: %w", err)
	}
	afterIdentity, err := fileIdentity(after)
	if err != nil {
		return nil, err
	}
	if beforeIdentity != afterIdentity || int64(len(data)) != before.Size() {
		return nil, errors.New("evidence artifact changed while being read")
	}

	current, err := openNoSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("reopen evidence artifact: %w", err)
	}
	currentInfo, statErr := current.Stat()
	closeErr = current.Close()
	if statErr != nil || closeErr != nil {
		return nil, fmt.Errorf("reinspect evidence artifact path: %w", errors.Join(statErr, closeErr))
	}
	currentIdentity, err := fileIdentity(currentInfo)
	if err != nil {
		return nil, err
	}
	if beforeIdentity != currentIdentity {
		return nil, errors.New("evidence artifact path was replaced while being read")
	}
	return data, nil
}

type localFileIdentity struct {
	device, inode uint64
	size          int64
	mode          uint32
	mtimeSec      int64
	mtimeNsec     int64
	ctimeSec      int64
	ctimeNsec     int64
}

func fileIdentity(info os.FileInfo) (localFileIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return localFileIdentity{}, errors.New("evidence artifact identity is unavailable")
	}
	return localFileIdentity{
		device: uint64(stat.Dev), inode: stat.Ino, size: stat.Size, mode: stat.Mode,
		mtimeSec: stat.Mtim.Sec, mtimeNsec: stat.Mtim.Nsec,
		ctimeSec: stat.Ctim.Sec, ctimeNsec: stat.Ctim.Nsec,
	}, nil
}

// openNoSymlinks walks from the filesystem root using O_NOFOLLOW. O_NONBLOCK
// prevents a raced FIFO or device from blocking before its type is proved.
func openNoSymlinks(path string) (*os.File, error) {
	separator := string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(path, separator), separator)
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return nil, errors.New("path must name a filesystem object")
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
	return nil, errors.New("path must name a filesystem object")
}

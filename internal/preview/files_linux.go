//go:build linux

package preview

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Walk each component with openat/O_NOFOLLOW. Never establish trust through a
// provider-selected path or a symlink resolved just before an ordinary open.
func openDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrIntegrity
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		next, e := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		syscall.Close(fd)
		if e != nil {
			return nil, ErrIntegrity
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

func privateDirectory(path string, create bool) (*os.File, error) {
	if create {
		parent, err := openDirectory(filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		err = syscall.Mkdirat(int(parent.Fd()), filepath.Base(path), 0700)
		if err != nil && err != syscall.EEXIST {
			parent.Close()
			return nil, ErrIntegrity
		}
		if err = parent.Sync(); err != nil {
			parent.Close()
			return nil, err
		}
		parent.Close()
	}
	dir, err := openDirectory(path)
	if err != nil {
		return nil, err
	}
	var stat syscall.Stat_t
	if syscall.Fstat(int(dir.Fd()), &stat) != nil || stat.Uid != uint32(os.Geteuid()) || stat.Mode&077 != 0 {
		dir.Close()
		return nil, ErrIntegrity
	}
	return dir, nil
}

func openRegular(path string, flags int, private bool) (*os.File, error) {
	dir, err := openDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	fd, err := syscall.Openat(int(dir.Fd()), filepath.Base(path), (flags&^os.O_TRUNC)|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	var st syscall.Stat_t
	mask := uint32(022)
	if private {
		mask = 077
	}
	if syscall.Fstat(fd, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 || st.Uid != uint32(os.Geteuid()) || st.Mode&mask != 0 {
		f.Close()
		return nil, ErrIntegrity
	}
	if flags&os.O_TRUNC != 0 {
		if err = f.Truncate(0); err != nil {
			f.Close()
			return nil, ErrIntegrity
		}
	}
	return f, nil
}

func readFile(path string, limit int64, private bool) ([]byte, error) {
	f, err := openRegular(path, os.O_RDONLY, private)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > limit {
		return nil, ErrIntegrity
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, ErrIntegrity
	}
	return data, nil
}

func lockFile(f *os.File) error {
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return ErrUnavailable
	}
	return nil
}

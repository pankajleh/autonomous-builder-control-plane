//go:build linux

package remotetarget

import (
	"errors"
	"os"
	"syscall"
)

// privateDirectory accepts a folder that is no symbolic link, belongs to the
// controller's user and is open to that user alone.
func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("folder cannot be read")
	}
	if !info.IsDir() {
		return errors.New("not a folder, or a symbolic link")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return errors.New("folder is not owned by the controller's user")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("folder is open to others: it must be mode 0700")
	}
	return nil
}

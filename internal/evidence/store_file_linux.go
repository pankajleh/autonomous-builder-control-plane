//go:build linux

package evidence

import (
	"errors"
	"os"
	"syscall"
)

var errEvidenceMultipleLinks = errors.New("evidence artifact has multiple links")

func verifyEvidenceSingleLink(file *os.File, path string) error {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("evidence artifact has unsafe type or permissions")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("evidence artifact has unsafe ownership")
	}
	if stat.Nlink != 1 {
		return errEvidenceMultipleLinks
	}
	named, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, named) {
		return errors.New("evidence artifact path was replaced")
	}
	return nil
}

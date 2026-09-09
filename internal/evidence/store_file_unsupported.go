//go:build !linux

package evidence

import (
	"errors"
	"os"
)

var errEvidenceMultipleLinks = errors.New("evidence artifact has multiple links")

func verifyEvidenceSingleLink(file *os.File, path string) error {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("evidence artifact has unsafe type or permissions")
	}
	named, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, named) {
		return errors.New("evidence artifact path was replaced")
	}
	return nil
}

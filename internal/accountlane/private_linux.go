//go:build linux

package accountlane

import (
	"errors"
	"os"
	"syscall"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
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

// ReadFile reads the operator's account lanes file: a private regular file of
// the controller's user, strict JSON.
func ReadFile(path string) (FileV1, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return FileV1{}, errors.New("account lanes file is not a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return FileV1{}, errors.New("account lanes file must be the controller user's, mode 0600")
	}
	if info.Size() > 64<<10 {
		return FileV1{}, errors.New("account lanes file is too large")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return FileV1{}, errors.New("account lanes file cannot be read")
	}
	var file FileV1
	if err := strictjson.Decode(data, &file); err != nil {
		return FileV1{}, errors.New("account lanes file is not valid JSON of its schema")
	}
	return file, nil
}

//go:build !linux

package run

import "errors"

// pointRunBranch is Linux-only (walled builds run only on Linux); elsewhere the review keeps Ralphex's own base.
func pointRunBranch(folder, name, sha string) error {
	return errors.New("the review base is written only on Linux")
}

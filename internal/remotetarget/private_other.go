//go:build !linux

package remotetarget

import "errors"

func privateDirectory(string) error { return errors.New("remote targets need Linux") }

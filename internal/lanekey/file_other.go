//go:build !linux

package lanekey

import "errors"

// errUnsupported is the answer off Linux, where key files are neither written nor removed (builds read them on Linux
// only, enginelane.ReadPrivate).
var errUnsupported = errors.New("lane keys are written on Linux only")

func writeKeyFile(string, []byte) error { return errUnsupported }

func removeKeyFile(string) (bool, error) { return false, errUnsupported }

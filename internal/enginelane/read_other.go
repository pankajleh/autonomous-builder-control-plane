//go:build !linux

package enginelane

import "errors"

func readPrivate(string) ([]byte, error) {
	return nil, errors.New("engine lanes are read on Linux only")
}

// ReadPrivate reads private files on Linux only.
func ReadPrivate(string, int64) ([]byte, error) {
	return nil, errors.New("private files are read on Linux only")
}

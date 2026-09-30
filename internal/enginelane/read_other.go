//go:build !linux

package enginelane

import "errors"

func readPrivate(string) ([]byte, error) {
	return nil, errors.New("engine lanes are read on Linux only")
}

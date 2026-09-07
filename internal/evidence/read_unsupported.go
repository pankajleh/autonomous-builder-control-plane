//go:build !linux

package evidence

import "errors"

func readBoundedLocal(string, string, int64) ([]byte, error) {
	return nil, errors.New("secure local evidence artifact reads are unsupported on this platform")
}

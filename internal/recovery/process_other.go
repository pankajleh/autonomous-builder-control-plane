//go:build !linux

package recovery

import "errors"

var errProcessNotFound = errors.New("process not found")

type processObservation struct {
	identity ProcessIdentity
	alive    bool
}

func observeProcess(_ int) (processObservation, error) {
	return processObservation{}, ErrProcessIdentityUnsupported
}

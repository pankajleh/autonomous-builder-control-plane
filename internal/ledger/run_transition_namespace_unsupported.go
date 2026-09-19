//go:build !linux

package ledger

import "errors"

func closeRunTransitionNamespace(*runTransitionNamespace) error { return nil }

func (l *JSONLLedger) acquireRunTransitionLeaseLocked(string) (*RunTransitionLease, error) {
	return nil, errors.New("run-transition namespace authority is unsupported on this platform")
}

func (l *JSONLLedger) verifyRunTransitionLeaseLocked(*RunTransitionLease) error {
	return errors.New("run-transition namespace authority is unsupported on this platform")
}

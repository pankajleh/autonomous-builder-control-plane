//go:build !linux

package mergelifecycle

import "github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"

type durableStore struct{}
type attemptStore struct {
	id   string
	root string
}
type inventory struct{}

func newDurableStore(string, Limits) (*durableStore, error) { return nil, errUnsupportedDurability }
func (*durableStore) close() error                          { return nil }
func (*durableStore) openAttempt(string) (*attemptStore, error) {
	return nil, errUnsupportedDurability
}
func (*durableStore) appendChannel(string, string, string, []byte, int) (ledger.EvidenceRef, error) {
	return ledger.EvidenceRef{}, errUnsupportedDurability
}
func (*durableStore) readChannel(string, string, int) ([]byte, bool, error) {
	return nil, false, errUnsupportedDurability
}
func (*attemptStore) close() error { return nil }
func (*attemptStore) publish(string, []byte) ([]byte, string, error) {
	return nil, "", errUnsupportedDurability
}
func (*attemptStore) read(string) ([]byte, bool, error) { return nil, false, errUnsupportedDurability }
func (*attemptStore) cleanupTemporary() error           { return errUnsupportedDurability }
func (*attemptStore) reserveCounter(string) (Counters, error) {
	return Counters{}, errUnsupportedDurability
}
func (*attemptStore) currentCounters() (Counters, error) { return Counters{}, errUnsupportedDurability }

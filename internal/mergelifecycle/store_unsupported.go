//go:build !linux

package mergelifecycle

import "github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"

type durableStore struct{}
type repositoryBaseLease struct{}
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
func (*durableStore) acquireRepositoryBase(string) (*repositoryBaseLease, error) {
	return nil, errUnsupportedDurability
}
func (*repositoryBaseLease) close() error { return nil }
func (*durableStore) appendChannel(string, string, string, []byte, int) (ledger.EvidenceRef, error) {
	return ledger.EvidenceRef{}, errUnsupportedDurability
}
func (*durableStore) readChannel(string, string, int) ([]byte, bool, error) {
	return nil, false, errUnsupportedDurability
}
func (*durableStore) findTerminalCore(string) (TerminalCoreV1, []byte, string, bool, error) {
	return TerminalCoreV1{}, nil, "", false, errUnsupportedDurability
}
func (*durableStore) nextCleanupSequence(string) (int, bool, error) {
	return 0, false, errUnsupportedDurability
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
func (*attemptStore) accountProvider(ProviderAccountingV1) (Counters, error) {
	return Counters{}, errUnsupportedDurability
}
func (*attemptStore) reserveReconciliation(int64) (Counters, error) {
	return Counters{}, errUnsupportedDurability
}
func (*attemptStore) currentCounters() (Counters, error) { return Counters{}, errUnsupportedDurability }
func (*attemptStore) providerBudget() (ProviderBudgetV1, error) {
	return ProviderBudgetV1{}, errUnsupportedDurability
}

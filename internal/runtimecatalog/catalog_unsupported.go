//go:build !linux

package runtimecatalog

import (
	"github.com/pankajleh/autonomous-builder-control-plane/internal/recovery"
)

// Non-Linux builds retain the Track-A API but fail closed because no weaker
// filesystem or process-owner proof is permitted.
type Catalog struct{}
type OwnerLeaseGuard struct{}

func Open(string) (*Catalog, error)                          { return nil, ErrUnsupported }
func OpenServiceRoot(root string) (*Catalog, error)          { return Open(root) }
func (*Catalog) Root() string                                { return "" }
func (*Catalog) Close() error                                { return nil }
func (*Catalog) RegisterRun(RunRegistrationV1) error         { return ErrUnsupported }
func (*Catalog) RegisterAttempt(AttemptRegistrationV1) error { return ErrUnsupported }
func (*Catalog) ListRuns(string, int) ([]RunRegistrationV1, bool, error) {
	return nil, false, ErrUnsupported
}
func (*Catalog) ReadRun(string) (RunRegistrationV1, error) {
	return RunRegistrationV1{}, ErrUnsupported
}
func (*Catalog) ReadAttempt(string, string) (AttemptRegistrationV1, error) {
	return AttemptRegistrationV1{}, ErrUnsupported
}
func (*Catalog) AcquireOwnerLeaseGuard(string) (*OwnerLeaseGuard, error) { return nil, ErrUnsupported }
func (*Catalog) InstallOwnerLease(string, string, recovery.ProcessIdentity) (ActiveOwnerLeaseV1, error) {
	return ActiveOwnerLeaseV1{}, ErrUnsupported
}
func (*OwnerLeaseGuard) Close() error { return nil }
func (*OwnerLeaseGuard) Lease() (ActiveOwnerLeaseV1, error) {
	return ActiveOwnerLeaseV1{}, ErrUnsupported
}
func (*OwnerLeaseGuard) Install(string, recovery.ProcessIdentity) (ActiveOwnerLeaseV1, error) {
	return ActiveOwnerLeaseV1{}, ErrUnsupported
}
func (*OwnerLeaseGuard) MarkClosing(string, uint64) (ActiveOwnerLeaseV1, error) {
	return ActiveOwnerLeaseV1{}, ErrUnsupported
}
func (*OwnerLeaseGuard) Retire(string) (ActiveOwnerLeaseV1, error) {
	return ActiveOwnerLeaseV1{}, ErrUnsupported
}
func VerifyLiveOwner(ActiveOwnerLeaseV1) error                              { return ErrUnsupported }
func LeaseMatchesProcess(ActiveOwnerLeaseV1, recovery.ProcessIdentity) bool { return false }

func observeRegisteredLedgerGeneration(string) (LedgerGenerationV1, error) {
	return LedgerGenerationV1{}, ErrUnsupported
}

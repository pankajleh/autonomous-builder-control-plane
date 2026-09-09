//go:build !linux

package cilifecycle

import (
	"errors"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

var errUnsupportedCIDurability = errors.New("CI evidence ingestion requires verified Linux flock and no-follow semantics")

type attemptLimits struct {
	maxPerRun int
	maxGlobal int
	maxBytes  int64
}

func productionAttemptLimits() attemptLimits {
	return attemptLimits{MaxAttemptsPerRun, MaxAttemptsGlobal, MaxReservedEvidenceBytes}
}

type attemptStore struct{}
type attemptLease struct {
	created     bool
	needsRepair bool
}

func newAttemptStore(string, attemptLimits) (*attemptStore, error) {
	return nil, errUnsupportedCIDurability
}
func (*attemptStore) acquire(attemptReservationV1) (*attemptLease, error) {
	return nil, errUnsupportedCIDurability
}
func (*attemptStore) close() error  { return nil }
func (*attemptLease) repair() error { return errUnsupportedCIDurability }
func (*attemptLease) poison() error { return errUnsupportedCIDurability }
func (*attemptLease) close() error  { return nil }

type artifactBoundary struct{ store ArtifactStore }

func newArtifactBoundary(ArtifactStore) (*artifactBoundary, error) {
	return nil, errUnsupportedCIDurability
}
func (*artifactBoundary) close() error   { return nil }
func (*artifactBoundary) runID() string  { return "" }
func (*artifactBoundary) runDir() string { return "" }
func (*artifactBoundary) readVerified(string, ledger.EvidenceRef, int) ([]byte, error) {
	return nil, errUnsupportedCIDurability
}
func (*artifactBoundary) readExisting(string, string, int) ([]byte, ledger.EvidenceRef, bool, error) {
	return nil, ledger.EvidenceRef{}, false, errUnsupportedCIDurability
}
func (*artifactBoundary) stabilize(string) error { return errUnsupportedCIDurability }

type materialLedger struct{}

func newMaterialLedger(*ledger.JSONLLedger) (*materialLedger, error) {
	return nil, errUnsupportedCIDurability
}
func (*materialLedger) close() error { return nil }
func (*materialLedger) find(string) (materialEvent, bool, error) {
	return materialEvent{}, false, errUnsupportedCIDurability
}
func (*materialLedger) record(ledger.Event, []byte) error { return errUnsupportedCIDurability }

package authoritybackend

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

func TestFencedWorkflowAuthorityBackendDrainFenceCutoverAndTombstone(t *testing.T) {
	domain, controllerID, repositoryID := repeat("a"), repeat("b"), repeat("c")
	identity := governance.WorkflowBackendIdentityV1{
		AuthorityDomain: domain, ControllerIdentity: controllerID, ImplementationID: "memory-v1", CompositionSHA256: repeat("d"),
	}
	identitySHA, err := identity.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	directory := makeDirectory(t, identitySHA, domain, controllerID, repositoryID)
	probes := make(map[string]PredecessorDrainProbeV1)
	for _, binding := range directory.Bindings {
		digest := binding.BarrierStateSHA256
		probes[binding.BindingID] = func(governance.PredecessorStoreBindingV1) (string, error) { return digest, nil }
	}
	clock := time.Date(2026, 9, 17, 1, 2, 3, 0, time.UTC)
	fence, err := NewSharedPredecessorWriterFenceV1(directory, repeat("e"), 5*time.Minute, func() time.Time { return clock }, probes)
	if err != nil {
		t.Fatal(err)
	}
	state := initialV1State(controllerID, repositoryID, 7)
	stateBytes, _ := json.Marshal(state)
	backend := &memoryV1Backend{domain: domain, controllerID: controllerID, state: stateBytes, revision: 7}
	fenced, err := NewFencedWorkflowAuthorityBackendV1(backend, fence, "workflow", identity)
	if err != nil {
		t.Fatal(err)
	}

	// Retained writer compatibility: a pre-cutover CAS reaches the exact old
	// backend once and preserves its V1 wire.
	next := initialV1State(controllerID, repositoryID, 8)
	nextBytes, _ := json.Marshal(next)
	swapped, err := fenced.CompareAndSwapWorkflowStateV1(controllerID, 7, nextBytes)
	if err != nil || !swapped || backend.casCalls != 1 || !bytes.Equal(backend.state, nextBytes) {
		t.Fatalf("retained fenced CAS = swapped=%v calls=%d err=%v", swapped, backend.casCalls, err)
	}

	// An admitted shared writer makes the exclusive cutover wait. Its release
	// advances the directory, so the stale cutover revision fails and must be
	// freshly retried.
	lease, err := fence.AcquirePredecessorWriterV1([]string{"ledger-run-1"}, "maintenance")
	if err != nil {
		t.Fatal(err)
	}
	activeDirectory, _ := fence.DirectoryV1()
	transaction := &captureCutoverTransaction{}
	done := make(chan error, 1)
	go func() {
		_, cutoverErr := fenced.CutoverV1(PredecessorCutoverRequestV1{
			ExpectedDirectoryRevision: activeDirectory.DirectoryRevision,
			ExpectedStateRevision:     8, ActivationV2SHA256: repeat("f"),
		}, transaction)
		done <- cutoverErr
	}()
	select {
	case err := <-done:
		t.Fatalf("cutover did not wait for active writer: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || transaction.calls != 0 {
		t.Fatalf("stale cutover after writer release = err=%v calls=%d", err, transaction.calls)
	}

	stableDirectory, _ := fence.DirectoryV1()
	result, err := fenced.CutoverV1(PredecessorCutoverRequestV1{
		ExpectedDirectoryRevision: stableDirectory.DirectoryRevision,
		ExpectedStateRevision:     8, ActivationV2SHA256: repeat("f"),
	}, transaction)
	if err != nil {
		t.Fatal(err)
	}
	if transaction.calls != 1 || !bytes.Equal(transaction.input.PredecessorStateCanonical, nextBytes) ||
		result.SuccessorState.Revision != 9 || result.Cutover.V2Revision != 9 ||
		result.Cutover.PredecessorStateV1SHA256 != transaction.input.PredecessorStateSHA256 ||
		result.TombstonedDirectory.WriterState != governance.PredecessorWriterTombstoned {
		t.Fatalf("cutover did not preserve exact V1 and install V2 r+1: %+v", result)
	}
	if parsed, err := governance.ParsePredecessorCutoverV1(transaction.input.CutoverCanonical); err != nil || parsed.CutoverSHA256 != result.Cutover.CutoverSHA256 {
		t.Fatalf("durable cutover bytes = %+v, %v", parsed, err)
	}
	if parsed, err := governance.ParsePredecessorAuthorityDirectoryV1(transaction.input.TombstonedDirectoryCanonical); err != nil || parsed.WriterState != governance.PredecessorWriterTombstoned {
		t.Fatalf("durable tombstone bytes = %+v, %v", parsed, err)
	}

	// The wrapper checks the tombstone before touching the selected V1 backend.
	late := initialV1State(controllerID, repositoryID, 9)
	lateBytes, _ := json.Marshal(late)
	if swapped, err := fenced.CompareAndSwapWorkflowStateV1(controllerID, 8, lateBytes); err == nil || swapped {
		t.Fatalf("late V1 CAS = swapped=%v err=%v", swapped, err)
	}
	if backend.casCalls != 1 || !bytes.Equal(backend.state, nextBytes) {
		t.Fatalf("late V1 CAS touched predecessor: calls=%d", backend.casCalls)
	}
}

func TestSharedPredecessorFenceFailsClosedOnDrainProbeDrift(t *testing.T) {
	domain, controllerID, repositoryID := repeat("1"), repeat("2"), repeat("3")
	identity := governance.WorkflowBackendIdentityV1{AuthorityDomain: domain, ControllerIdentity: controllerID, ImplementationID: "memory-v1", CompositionSHA256: repeat("4")}
	identitySHA, _ := identity.SHA256()
	directory := makeDirectory(t, identitySHA, domain, controllerID, repositoryID)
	probes := make(map[string]PredecessorDrainProbeV1)
	for _, binding := range directory.Bindings {
		digest := binding.BarrierStateSHA256
		probes[binding.BindingID] = func(governance.PredecessorStoreBindingV1) (string, error) { return digest, nil }
	}
	probes["pr-root"] = func(governance.PredecessorStoreBindingV1) (string, error) { return repeat("a"), nil }
	fence, err := NewSharedPredecessorWriterFenceV1(directory, repeat("5"), time.Minute, func() time.Time {
		return time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	}, probes)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = fence.WithExclusivePredecessorCutoverV1(directory.DirectoryRevision, func(governance.PredecessorAuthorityDirectoryV1, governance.PredecessorAuthorityDirectoryV1, []string) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("drifted probe reached cutover operation: called=%v err=%v", called, err)
	}
	snapshot, _ := fence.DirectoryV1()
	if snapshot.WriterState != governance.PredecessorWriterOpen {
		t.Fatalf("failed drain changed directory state: %s", snapshot.WriterState)
	}
}

type memoryV1Backend struct {
	mu           sync.Mutex
	domain       string
	controllerID string
	state        []byte
	revision     uint64
	casCalls     int
}

func (b *memoryV1Backend) AuthorityDomainV1() (string, error) { return b.domain, nil }
func (b *memoryV1Backend) LoadWorkflowStateV1(controllerIdentity string) ([]byte, uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if controllerIdentity != b.controllerID {
		return nil, 0, errors.New("wrong controller")
	}
	return append([]byte(nil), b.state...), b.revision, nil
}
func (b *memoryV1Backend) CompareAndSwapWorkflowStateV1(controllerIdentity string, expectedRevision uint64, data []byte) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.casCalls++
	if controllerIdentity != b.controllerID {
		return false, errors.New("wrong controller")
	}
	if b.revision != expectedRevision {
		return false, nil
	}
	var state governance.ControllerStateV1
	if err := governance.ParseCanonical(data, &state); err != nil || state.Revision != expectedRevision+1 {
		return false, errors.Join(errors.New("invalid V1 successor"), err)
	}
	b.state, b.revision = append([]byte(nil), data...), state.Revision
	return true, nil
}

type captureCutoverTransaction struct {
	calls int
	input PredecessorCutoverTransactionInputV1
}

func (t *captureCutoverTransaction) CommitPredecessorCutoverV1(input PredecessorCutoverTransactionInputV1) (bool, error) {
	t.calls++
	t.input = input
	sum := sha256.Sum256(input.PredecessorStateCanonical)
	stateDigest := hex.EncodeToString(sum[:])
	if input.SuccessorState.Revision != input.PredecessorStateRevision+1 ||
		input.SuccessorState.PredecessorStateV1ArtifactSHA256 != input.PredecessorStateSHA256 ||
		input.Cutover.PredecessorStateV1SHA256 != input.PredecessorStateSHA256 ||
		stateDigest != input.PredecessorStateSHA256 {
		return false, errors.New("transaction input is inconsistent")
	}
	return true, nil
}

func makeDirectory(t *testing.T, workflowIdentitySHA, domain, controllerID, repositoryID string) governance.PredecessorAuthorityDirectoryV1 {
	t.Helper()
	binding := func(id string, kind governance.PredecessorBindingKind, run, marker string) governance.PredecessorStoreBindingV1 {
		value := governance.PredecessorStoreBindingV1{
			BindingID: id, BindingKind: kind, RunID: run, HostIdentity: repeat("6"),
			InitialScanArtifactSHA256: repeat(marker), BarrierStateSHA256: repeat(marker), RegisteredEpoch: 1,
		}
		if kind == governance.PredecessorBindingWorkflowState {
			value.WorkflowBackendIdentitySHA256 = workflowIdentitySHA
		} else {
			value.CanonicalPathSHA256, value.PhysicalIdentitySHA256 = repeat(marker), repeat(marker)
		}
		return value
	}
	directory, err := governance.SealPredecessorAuthorityDirectoryV1(governance.PredecessorAuthorityDirectoryV1{
		Kind: "PredecessorAuthorityDirectoryV1", SchemaVersion: governance.PredecessorAuthorityDirectorySchemaV1,
		AuthorityDomain: domain, ControllerIdentity: controllerID, RepositoryIdentity: repositoryID,
		WriterEpoch: 1, WriterState: governance.PredecessorWriterOpen,
		Bindings: []governance.PredecessorStoreBindingV1{
			binding("workflow", governance.PredecessorBindingWorkflowState, "", "7"),
			binding("ledger-run-1", governance.PredecessorBindingLedger, "run-1", "8"),
			binding("pr-root", governance.PredecessorBindingPRAdmission, "", "9"),
			binding("merge-root", governance.PredecessorBindingMergeStateStore, "", "0"),
		},
		ActiveWriterLeases: []string{}, DirectoryRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func initialV1State(controllerID, repositoryID string, revision uint64) governance.ControllerStateV1 {
	return governance.ControllerStateV1{
		Kind: "GovernanceControllerStateV1", ControllerIdentity: controllerID, RepositoryIdentity: repositoryID,
		Revision: revision, IssuedV2Authorities: []governance.IssuedAuthorityV1{}, FindingEvidence: []governance.FindingEvidenceV1{},
		ExecutionState: ralphex.ExecutionStateV1{AggregateElapsed: "0s"},
	}
}

func repeat(value string) string { return strings.Repeat(value, 64) }

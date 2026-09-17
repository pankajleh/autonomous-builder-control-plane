package ledger

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authoritybackend"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
)

func TestPredecessorFenceTombstoneBlocksLateLedgerWriter(t *testing.T) {
	path := t.TempDir() + "/events.jsonl"
	legacy, err := NewJSONLLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	event, err := NewEvent("run-1", "run.failed", "controller", "test")
	if err != nil {
		t.Fatal(err)
	}
	event.StateFrom, event.StateTo = domain.StateImplementing, domain.StateFailed
	if err := legacy.Append(event); err != nil {
		t.Fatal(err)
	}
	ledgerSnapshot, err := legacy.PredecessorDrainSnapshotV1("run-1")
	if err != nil {
		t.Fatal(err)
	}
	directory := ledgerTestDirectory(t, ledgerSnapshot)
	probes := map[string]authoritybackend.PredecessorDrainProbeV1{}
	for _, binding := range directory.Bindings {
		digest := binding.BarrierStateSHA256
		probes[binding.BindingID] = func(governance.PredecessorStoreBindingV1) (string, error) { return digest, nil }
	}
	probes["ledger-run-1"] = func(governance.PredecessorStoreBindingV1) (string, error) {
		return legacy.PredecessorDrainSnapshotV1("run-1")
	}
	fence, err := authoritybackend.NewSharedPredecessorWriterFenceV1(directory, strings.Repeat("e", 64), time.Minute, func() time.Time {
		return time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	}, probes)
	if err != nil {
		t.Fatal(err)
	}
	fenced, err := NewFencedJSONLLedger(path, fence, []string{"ledger-run-1"})
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := fenced.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := fence.WithExclusivePredecessorCutoverV1(directory.DirectoryRevision, func(
		governance.PredecessorAuthorityDirectoryV1, governance.PredecessorAuthorityDirectoryV1, []string,
	) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	late, err := NewEvent("run-2", "run.created", "controller", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := fenced.Append(late); err == nil || !strings.Contains(err.Error(), "TOMBSTONED") {
		t.Fatalf("late ledger writer error = %v", err)
	}
	after, _, err := fenced.Snapshot()
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("late ledger writer changed bytes: err=%v", err)
	}
}

func ledgerTestDirectory(t *testing.T, ledgerSnapshot string) governance.PredecessorAuthorityDirectoryV1 {
	t.Helper()
	repeat := func(value string) string { return strings.Repeat(value, 64) }
	binding := func(id string, kind governance.PredecessorBindingKind, run, marker string) governance.PredecessorStoreBindingV1 {
		value := governance.PredecessorStoreBindingV1{
			BindingID: id, BindingKind: kind, RunID: run, HostIdentity: repeat("1"),
			InitialScanArtifactSHA256: repeat(marker), BarrierStateSHA256: repeat(marker), RegisteredEpoch: 1,
		}
		if kind == governance.PredecessorBindingWorkflowState {
			value.WorkflowBackendIdentitySHA256 = repeat("2")
		} else {
			value.CanonicalPathSHA256, value.PhysicalIdentitySHA256 = repeat(marker), repeat(marker)
		}
		return value
	}
	bindings := []governance.PredecessorStoreBindingV1{
		binding("workflow", governance.PredecessorBindingWorkflowState, "", "2"),
		binding("ledger-run-1", governance.PredecessorBindingLedger, "run-1", "3"),
		binding("pr-root", governance.PredecessorBindingPRAdmission, "", "4"),
		binding("merge-root", governance.PredecessorBindingMergeStateStore, "", "5"),
	}
	bindings[1].BarrierStateSHA256 = ledgerSnapshot
	directory, err := governance.SealPredecessorAuthorityDirectoryV1(governance.PredecessorAuthorityDirectoryV1{
		Kind: "PredecessorAuthorityDirectoryV1", SchemaVersion: governance.PredecessorAuthorityDirectorySchemaV1,
		AuthorityDomain: repeat("a"), ControllerIdentity: repeat("b"), RepositoryIdentity: repeat("c"),
		WriterEpoch: 1, WriterState: governance.PredecessorWriterOpen, Bindings: bindings,
		ActiveWriterLeases: []string{}, DirectoryRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

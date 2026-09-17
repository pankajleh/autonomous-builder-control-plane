package governance

import (
	"encoding/json"
	"strings"
	"testing"

	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

func TestAssuranceWireStateUpgradeAndDrain(t *testing.T) {
	directory := testPredecessorDirectoryV1(t)
	data, err := directory.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePredecessorAuthorityDirectoryV1(data)
	if err != nil || parsed.DirectorySHA256 != directory.DirectorySHA256 {
		t.Fatalf("directory round trip = %+v, %v", parsed, err)
	}
	if _, err := ParsePredecessorAuthorityDirectoryV1(append(data, []byte("{}")...)); err == nil {
		t.Fatal("directory accepted trailing JSON")
	}

	state := ControllerStateV1{
		Kind: "GovernanceControllerStateV1", ControllerIdentity: directory.ControllerIdentity,
		RepositoryIdentity: directory.RepositoryIdentity, Revision: 7,
		IssuedV2Authorities: []IssuedAuthorityV1{}, FindingEvidence: []FindingEvidenceV1{},
		ExecutionState: ralphex.ExecutionStateV1{AggregateElapsed: "0s"},
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := V3Drained(stateBytes, 7, directory); err != nil {
		t.Fatalf("drained predecessor rejected: %v", err)
	}

	active := state
	active.ActiveInvocation = &InvocationReservationV1{Token: "active"}
	activeBytes, _ := json.Marshal(active)
	if err := V3Drained(activeBytes, 7, directory); ClassOf(err) != V3DrainRequired {
		t.Fatalf("active invocation class = %q, err=%v", ClassOf(err), err)
	}

	lease := MutationLeaseV1{
		Kind: "MutationLeaseV1", CapsuleSHA256: digestChar("a"), ReportSHA256: digestChar("b"),
		PreFixHEAD: strings.Repeat("c", 40), BlockerFindingIDs: []string{"finding"}, MutationRuleIDs: []string{"rule"},
		AllowedPaths: []string{"internal/governance/**"}, MaxChangedFiles: 1, MaxChangedBytes: 1,
	}
	lease.LeaseSHA256, err = leaseDigest(lease)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []LeaseStatus{LeaseIssued, LeaseConsuming} {
		live := state
		live.Lease = &lease
		live.MutationState = &MutationStateV1{LeaseTipSHA256: lease.LeaseSHA256, LeaseStatus: status}
		liveBytes, _ := json.Marshal(live)
		if err := V3Drained(liveBytes, 7, directory); ClassOf(err) != V3DrainRequired {
			t.Fatalf("live %s lease class = %q, err=%v", status, ClassOf(err), err)
		}
	}
	consumed := state
	consumed.Lease = &lease
	consumed.MutationState = &MutationStateV1{LeaseTipSHA256: lease.LeaseSHA256, LeaseStatus: LeaseConsumed, ReceiptTipSHA256: digestChar("d")}
	consumedBytes, _ := json.Marshal(consumed)
	if err := V3Drained(consumedBytes, 7, directory); err != nil {
		t.Fatalf("consumed predecessor lease rejected: %v", err)
	}

	writerLease := PredecessorWriterLeaseV1{
		Kind: "PredecessorWriterLeaseV1", SchemaVersion: PredecessorWriterLeaseSchemaV1,
		LeaseID: "lease-1", AuthorityDomain: directory.AuthorityDomain, ControllerIdentity: directory.ControllerIdentity,
		RepositoryIdentity: directory.RepositoryIdentity, WriterEpoch: directory.WriterEpoch,
		BindingIDs: []string{"ledger-run-1", "workflow"}, Operation: contextcapsule.OperationMaintenance,
		OwnerInstanceSHA256: digestChar("e"), OpenedAt: "2026-09-17T00:00:00Z", Deadline: "2026-09-17T00:05:00Z",
		LeaseState: PredecessorWriterLeaseActive,
	}
	if _, err := writerLease.CanonicalJSON(); err != nil {
		t.Fatalf("valid writer lease rejected: %v", err)
	}
	writerLease.BindingIDs = []string{"workflow", "ledger-run-1"}
	if err := writerLease.Validate(); err == nil {
		t.Fatal("unsorted writer binding IDs accepted")
	}

	cutover, err := SealPredecessorCutoverV1(PredecessorCutoverV1{
		Kind: "PredecessorCutoverV1", SchemaVersion: PredecessorCutoverSchemaV1,
		AuthorityDomain: directory.AuthorityDomain, ControllerIdentity: directory.ControllerIdentity,
		RepositoryIdentity: directory.RepositoryIdentity, DirectorySHA256: directory.DirectorySHA256,
		WriterEpoch: 1, LinearizationRevision: 2, PredecessorStateV1Revision: 7,
		PredecessorStateV1SHA256: digestChar("f"), PredecessorStateV1ArtifactSHA256: digestChar("f"),
		BarrierSnapshotSHA256s: []string{digestChar("4"), digestChar("3")}, V2Revision: 8,
		TombstoneState: string(PredecessorWriterTombstoned),
	})
	if err != nil || cutover.BarrierSnapshotSHA256s[0] != digestChar("3") {
		t.Fatalf("seal cutover = %+v, %v", cutover, err)
	}
	cutover.V2Revision = 9
	if err := cutover.Validate(); err == nil {
		t.Fatal("non-successor V2 revision accepted")
	}
}

func TestAssuranceDrainUpgradeAndSemanticRegistry(t *testing.T) {
	directory := testPredecessorDirectoryV1(t)
	tombstone := directory
	tombstone.WriterState = PredecessorWriterTombstoned
	tombstone.DirectoryRevision++
	tombstone.ActiveWriterLeases = []string{}
	tombstone, err := SealPredecessorAuthorityDirectoryV1(tombstone)
	if err != nil {
		t.Fatal(err)
	}
	if tombstone.DirectorySHA256 == directory.DirectorySHA256 {
		t.Fatal("tombstone did not change the directory digest")
	}
	late := tombstone
	late.ActiveWriterLeases = []string{digestChar("9")}
	if _, err := SealPredecessorAuthorityDirectoryV1(late); err == nil {
		t.Fatal("tombstoned directory accepted a late writer")
	}

	successor := ControllerStateV2{
		Kind: "ControllerStateV2", SchemaVersion: ControllerStateSchemaV2,
		AuthorityDomain: directory.AuthorityDomain, ControllerIdentity: directory.ControllerIdentity,
		RepositoryIdentity: directory.RepositoryIdentity, Revision: 8,
		PredecessorStateV1ArtifactSHA256: digestChar("a"), PredecessorStateV1Revision: 7,
		PredecessorCutoverSHA256: digestChar("b"), ActivationV2SHA256: digestChar("c"),
		CoordinationState: "NONE", ActiveResourceReservations: []string{}, EffectAttemptState: "NONE",
	}
	data, err := successor.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded ControllerStateV2
	if err := ParseCanonical(data, &decoded); err != nil || decoded.ValidateInitialCutoverV1() != nil {
		t.Fatalf("initial successor round trip failed: %v", err)
	}
	successor.ActiveResourceReservations = nil
	if err := successor.ValidateInitialCutoverV1(); err == nil {
		t.Fatal("nil successor reservation set accepted")
	}
}

func testPredecessorDirectoryV1(t *testing.T) PredecessorAuthorityDirectoryV1 {
	t.Helper()
	b := func(id string, kind PredecessorBindingKind, run string, char string) PredecessorStoreBindingV1 {
		binding := PredecessorStoreBindingV1{
			BindingID: id, BindingKind: kind, RunID: run, HostIdentity: digestChar("1"),
			InitialScanArtifactSHA256: digestChar(char), BarrierStateSHA256: digestChar(char), RegisteredEpoch: 1,
		}
		if kind == PredecessorBindingWorkflowState {
			binding.WorkflowBackendIdentitySHA256 = digestChar("2")
		} else {
			binding.CanonicalPathSHA256 = digestChar(char)
			binding.PhysicalIdentitySHA256 = digestChar(char)
		}
		return binding
	}
	directory, err := SealPredecessorAuthorityDirectoryV1(PredecessorAuthorityDirectoryV1{
		Kind: "PredecessorAuthorityDirectoryV1", SchemaVersion: PredecessorAuthorityDirectorySchemaV1,
		AuthorityDomain: digestChar("a"), ControllerIdentity: digestChar("b"), RepositoryIdentity: digestChar("c"),
		WriterEpoch: 1, WriterState: PredecessorWriterOpen,
		Bindings: []PredecessorStoreBindingV1{
			b("workflow", PredecessorBindingWorkflowState, "", "2"),
			b("ledger-run-1", PredecessorBindingLedger, "run-1", "3"),
			b("pr-root", PredecessorBindingPRAdmission, "", "4"),
			b("merge-root", PredecessorBindingMergeStateStore, "", "5"),
		},
		ActiveWriterLeases: []string{}, DirectoryRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func digestChar(value string) string { return strings.Repeat(value, 64) }

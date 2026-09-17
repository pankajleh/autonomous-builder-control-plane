package governance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
)

const (
	PredecessorAuthorityDirectorySchemaV1 = "predecessor-authority-directory-v1"
	PredecessorWriterLeaseSchemaV1        = "predecessor-writer-lease-v1"
	PredecessorCutoverSchemaV1            = "predecessor-cutover-v1"
	ControllerStateSchemaV2               = "governance-controller-state-v2"
	GovernancePolicyVersionV4             = "context-capsule-v4"
)

const V3DrainRequired FailureClass = "V3_DRAIN_REQUIRED"

type PredecessorWriterState string

const (
	PredecessorWriterOpen       PredecessorWriterState = "OPEN"
	PredecessorWriterTombstoned PredecessorWriterState = "TOMBSTONED"
)

type PredecessorBindingKind string

const (
	PredecessorBindingWorkflowState   PredecessorBindingKind = "WORKFLOW_STATE"
	PredecessorBindingLedger          PredecessorBindingKind = "LEDGER"
	PredecessorBindingPRAdmission     PredecessorBindingKind = "PR_ADMISSION_STORE"
	PredecessorBindingMergeStateStore PredecessorBindingKind = "MERGE_STATE_STORE"
)

type PredecessorWriterLeaseState string

const (
	PredecessorWriterLeaseActive   PredecessorWriterLeaseState = "ACTIVE"
	PredecessorWriterLeaseReleased PredecessorWriterLeaseState = "RELEASED"
)

// WorkflowBackendIdentityV1 binds the directory's workflow row to the exact
// predecessor implementation selected by trusted controller composition.
type WorkflowBackendIdentityV1 struct {
	AuthorityDomain    string `json:"authority_domain"`
	ControllerIdentity string `json:"controller_identity"`
	ImplementationID   string `json:"implementation_id"`
	CompositionSHA256  string `json:"composition_sha256"`
}

func (identity WorkflowBackendIdentityV1) Validate() error {
	if !validAuthorityDomainV1(identity.AuthorityDomain) || !validDBIdentityV1(identity.ControllerIdentity) ||
		!validID(identity.ImplementationID) || !validSHA256(identity.CompositionSHA256) {
		return errors.New("workflow backend identity is invalid")
	}
	return nil
}

func (identity WorkflowBackendIdentityV1) SHA256() (string, error) {
	if err := identity.Validate(); err != nil {
		return "", err
	}
	return digestJSON(identity)
}

// PredecessorStoreBindingV1 is one immutable authority-bearing physical
// identity discovered before V4 activation.
type PredecessorStoreBindingV1 struct {
	BindingID                     string                 `json:"binding_id"`
	BindingKind                   PredecessorBindingKind `json:"binding_kind"`
	RunID                         string                 `json:"run_id,omitempty"`
	HostIdentity                  string                 `json:"host_identity"`
	CanonicalPathSHA256           string                 `json:"canonical_path_sha256,omitempty"`
	PhysicalIdentitySHA256        string                 `json:"physical_identity_sha256,omitempty"`
	WorkflowBackendIdentitySHA256 string                 `json:"workflow_backend_identity_sha256,omitempty"`
	InitialScanArtifactSHA256     string                 `json:"initial_scan_artifact_sha256"`
	BarrierStateSHA256            string                 `json:"barrier_state_sha256"`
	RegisteredEpoch               uint64                 `json:"registered_epoch"`
}

func (binding PredecessorStoreBindingV1) validate(epoch uint64) error {
	if !validID(binding.BindingID) || !validDBIdentityV1(binding.HostIdentity) ||
		!validSHA256(binding.InitialScanArtifactSHA256) || !validSHA256(binding.BarrierStateSHA256) ||
		binding.RegisteredEpoch == 0 || binding.RegisteredEpoch != epoch {
		return fmt.Errorf("predecessor binding %q has invalid identity, scan, barrier, or epoch", binding.BindingID)
	}
	switch binding.BindingKind {
	case PredecessorBindingWorkflowState:
		if binding.RunID != "" || binding.CanonicalPathSHA256 != "" || binding.PhysicalIdentitySHA256 != "" ||
			!validSHA256(binding.WorkflowBackendIdentitySHA256) {
			return fmt.Errorf("workflow binding %q has an invalid identity arm", binding.BindingID)
		}
	case PredecessorBindingLedger:
		if !validID(binding.RunID) || !validSHA256(binding.CanonicalPathSHA256) ||
			!validSHA256(binding.PhysicalIdentitySHA256) || binding.WorkflowBackendIdentitySHA256 != "" {
			return fmt.Errorf("ledger binding %q has an invalid identity arm", binding.BindingID)
		}
	case PredecessorBindingPRAdmission, PredecessorBindingMergeStateStore:
		if binding.RunID != "" || !validSHA256(binding.CanonicalPathSHA256) ||
			!validSHA256(binding.PhysicalIdentitySHA256) || binding.WorkflowBackendIdentitySHA256 != "" {
			return fmt.Errorf("file-store binding %q has an invalid identity arm", binding.BindingID)
		}
	default:
		return fmt.Errorf("predecessor binding %q has invalid kind %q", binding.BindingID, binding.BindingKind)
	}
	return nil
}

// PredecessorAuthorityDirectoryV1 is the complete, global predecessor writer
// inventory. DirectorySHA256 is excluded from its own preimage.
type PredecessorAuthorityDirectoryV1 struct {
	Kind               string                      `json:"kind"`
	SchemaVersion      string                      `json:"schema_version"`
	AuthorityDomain    string                      `json:"authority_domain"`
	ControllerIdentity string                      `json:"controller_identity"`
	RepositoryIdentity string                      `json:"repository_identity"`
	WriterEpoch        uint64                      `json:"writer_epoch"`
	WriterState        PredecessorWriterState      `json:"writer_state"`
	Bindings           []PredecessorStoreBindingV1 `json:"bindings"`
	ActiveWriterLeases []string                    `json:"active_writer_leases"`
	DirectoryRevision  uint64                      `json:"directory_revision"`
	DirectorySHA256    string                      `json:"directory_sha256"`
}

func SealPredecessorAuthorityDirectoryV1(directory PredecessorAuthorityDirectoryV1) (PredecessorAuthorityDirectoryV1, error) {
	bindings := make([]PredecessorStoreBindingV1, len(directory.Bindings))
	copy(bindings, directory.Bindings)
	directory.Bindings = bindings
	leases := make([]string, len(directory.ActiveWriterLeases))
	copy(leases, directory.ActiveWriterLeases)
	directory.ActiveWriterLeases = leases
	sort.Slice(directory.Bindings, func(i, j int) bool { return directory.Bindings[i].BindingID < directory.Bindings[j].BindingID })
	sort.Strings(directory.ActiveWriterLeases)
	directory.DirectorySHA256 = ""
	if err := validatePredecessorDirectoryPayloadV1(directory); err != nil {
		return PredecessorAuthorityDirectoryV1{}, err
	}
	digest, err := predecessorDirectoryDigestV1(directory)
	if err != nil {
		return PredecessorAuthorityDirectoryV1{}, err
	}
	directory.DirectorySHA256 = digest
	return directory, nil
}

func (directory PredecessorAuthorityDirectoryV1) Validate() error {
	if err := validatePredecessorDirectoryPayloadV1(directory); err != nil {
		return err
	}
	digest, err := predecessorDirectoryDigestV1(directory)
	if err != nil || digest != directory.DirectorySHA256 {
		return errors.New("predecessor directory digest disagrees")
	}
	return nil
}

func (directory PredecessorAuthorityDirectoryV1) CanonicalJSON() ([]byte, error) {
	if err := directory.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(directory)
}

func ParsePredecessorAuthorityDirectoryV1(data []byte) (PredecessorAuthorityDirectoryV1, error) {
	var directory PredecessorAuthorityDirectoryV1
	if len(data) == 0 || len(data) > 1<<20 {
		return directory, errors.New("predecessor directory bytes are outside the authority bound")
	}
	if err := ParseCanonical(data, &directory); err != nil {
		return directory, err
	}
	return directory, directory.Validate()
}

func validatePredecessorDirectoryPayloadV1(directory PredecessorAuthorityDirectoryV1) error {
	if directory.Kind != "PredecessorAuthorityDirectoryV1" || directory.SchemaVersion != PredecessorAuthorityDirectorySchemaV1 ||
		!validAuthorityDomainV1(directory.AuthorityDomain) || !validDBIdentityV1(directory.ControllerIdentity) ||
		!validText(directory.RepositoryIdentity, 1024) || directory.WriterEpoch == 0 || directory.DirectoryRevision == 0 ||
		(directory.WriterState != PredecessorWriterOpen && directory.WriterState != PredecessorWriterTombstoned) ||
		len(directory.Bindings) < 1 || len(directory.Bindings) > 256 || directory.ActiveWriterLeases == nil ||
		len(directory.ActiveWriterLeases) > 256 {
		return errors.New("predecessor directory identity, state, revision, or cardinality is invalid")
	}
	counts := map[PredecessorBindingKind]int{}
	runs := map[string]struct{}{}
	paths := map[string]struct{}{}
	previous := ""
	for _, binding := range directory.Bindings {
		if binding.BindingID <= previous {
			return errors.New("predecessor bindings must be sorted and unique by binding_id")
		}
		previous = binding.BindingID
		if err := binding.validate(directory.WriterEpoch); err != nil {
			return err
		}
		counts[binding.BindingKind]++
		if binding.BindingKind == PredecessorBindingLedger {
			if _, duplicate := runs[binding.RunID]; duplicate {
				return fmt.Errorf("run %q has more than one ledger binding", binding.RunID)
			}
			runs[binding.RunID] = struct{}{}
		}
		if binding.CanonicalPathSHA256 != "" {
			key := string(binding.BindingKind) + "\x00" + binding.CanonicalPathSHA256 + "\x00" + binding.PhysicalIdentitySHA256
			if _, duplicate := paths[key]; duplicate {
				return fmt.Errorf("physical predecessor binding %q is duplicated", binding.BindingID)
			}
			paths[key] = struct{}{}
		}
	}
	if counts[PredecessorBindingWorkflowState] != 1 || counts[PredecessorBindingLedger] == 0 ||
		counts[PredecessorBindingPRAdmission] != 1 || counts[PredecessorBindingMergeStateStore] == 0 {
		return errors.New("predecessor directory is incomplete: workflow, ledger, PR root, and merge root bindings are required")
	}
	previous = ""
	for _, digest := range directory.ActiveWriterLeases {
		if digest <= previous || !validSHA256(digest) {
			return errors.New("active predecessor writer leases must be sorted unique digests")
		}
		previous = digest
	}
	if directory.WriterState == PredecessorWriterTombstoned && len(directory.ActiveWriterLeases) != 0 {
		return errors.New("a tombstoned predecessor directory cannot retain active writers")
	}
	return nil
}

func predecessorDirectoryDigestV1(directory PredecessorAuthorityDirectoryV1) (string, error) {
	directory.DirectorySHA256 = ""
	type preimage PredecessorAuthorityDirectoryV1
	return digestJSON(preimage(directory))
}

// PredecessorWriterLeaseV1 is the exact shared-fence admission record for one
// complete predecessor mutation/provider operation.
type PredecessorWriterLeaseV1 struct {
	Kind                string                       `json:"kind"`
	SchemaVersion       string                       `json:"schema_version"`
	LeaseID             string                       `json:"lease_id"`
	AuthorityDomain     string                       `json:"authority_domain"`
	ControllerIdentity  string                       `json:"controller_identity"`
	RepositoryIdentity  string                       `json:"repository_identity"`
	WriterEpoch         uint64                       `json:"writer_epoch"`
	BindingIDs          []string                     `json:"binding_ids"`
	Operation           contextcapsule.OperationKind `json:"operation"`
	OwnerInstanceSHA256 string                       `json:"owner_instance_sha256"`
	OpenedAt            string                       `json:"opened_at"`
	Deadline            string                       `json:"deadline"`
	LeaseState          PredecessorWriterLeaseState  `json:"lease_state"`
}

func (lease PredecessorWriterLeaseV1) Validate() error {
	if lease.Kind != "PredecessorWriterLeaseV1" || lease.SchemaVersion != PredecessorWriterLeaseSchemaV1 ||
		!validID(lease.LeaseID) || !validAuthorityDomainV1(lease.AuthorityDomain) ||
		!validDBIdentityV1(lease.ControllerIdentity) || !validText(lease.RepositoryIdentity, 1024) ||
		lease.WriterEpoch == 0 || len(lease.BindingIDs) == 0 || len(lease.BindingIDs) > 256 ||
		!validSHA256(lease.OwnerInstanceSHA256) ||
		(lease.LeaseState != PredecessorWriterLeaseActive && lease.LeaseState != PredecessorWriterLeaseReleased) {
		return errors.New("predecessor writer lease identity, state, or cardinality is invalid")
	}
	if !validV4OperationV1(lease.Operation) {
		return fmt.Errorf("predecessor writer lease operation %q is invalid", lease.Operation)
	}
	previous := ""
	for _, bindingID := range lease.BindingIDs {
		if bindingID <= previous || !validID(bindingID) {
			return errors.New("predecessor writer lease binding_ids must be sorted and unique")
		}
		previous = bindingID
	}
	opened, err := parseCanonicalSecondV1(lease.OpenedAt)
	if err != nil {
		return fmt.Errorf("predecessor writer lease opened_at: %w", err)
	}
	deadline, err := parseCanonicalSecondV1(lease.Deadline)
	if err != nil || !deadline.After(opened) {
		return errors.New("predecessor writer lease deadline must be canonical and after opened_at")
	}
	return nil
}

func validV4OperationV1(operation contextcapsule.OperationKind) bool {
	switch string(operation) {
	case "design-planning", "design-review", "implementation", "implementation-review", "acceptance", "final-review",
		"pr-publication", "merge-authorization", "post-merge-acceptance", "deployment", "recovery", "maintenance", "dogfood-delegation":
		return true
	default:
		return false
	}
}

func (lease PredecessorWriterLeaseV1) CanonicalJSON() ([]byte, error) {
	if err := lease.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(lease)
}

func (lease PredecessorWriterLeaseV1) SHA256() (string, error) {
	data, err := lease.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func ParsePredecessorWriterLeaseV1(data []byte) (PredecessorWriterLeaseV1, error) {
	var lease PredecessorWriterLeaseV1
	if len(data) == 0 || len(data) > 1<<20 {
		return lease, errors.New("predecessor writer lease bytes are outside the authority bound")
	}
	if err := ParseCanonical(data, &lease); err != nil {
		return lease, err
	}
	return lease, lease.Validate()
}

// PredecessorCutoverV1 seals the exact stable predecessor bytes and the
// tombstoned directory at the single V2 revision successor.
type PredecessorCutoverV1 struct {
	Kind                             string   `json:"kind"`
	SchemaVersion                    string   `json:"schema_version"`
	AuthorityDomain                  string   `json:"authority_domain"`
	ControllerIdentity               string   `json:"controller_identity"`
	RepositoryIdentity               string   `json:"repository_identity"`
	DirectorySHA256                  string   `json:"directory_sha256"`
	WriterEpoch                      uint64   `json:"writer_epoch"`
	LinearizationRevision            uint64   `json:"linearization_revision"`
	PredecessorStateV1Revision       uint64   `json:"predecessor_state_v1_revision"`
	PredecessorStateV1SHA256         string   `json:"predecessor_state_v1_sha256"`
	PredecessorStateV1ArtifactSHA256 string   `json:"predecessor_state_v1_artifact_sha256"`
	BarrierSnapshotSHA256s           []string `json:"barrier_snapshot_sha256s"`
	V2Revision                       uint64   `json:"v2_revision"`
	TombstoneState                   string   `json:"tombstone_state"`
	CutoverSHA256                    string   `json:"cutover_sha256"`
}

func SealPredecessorCutoverV1(cutover PredecessorCutoverV1) (PredecessorCutoverV1, error) {
	snapshots := make([]string, len(cutover.BarrierSnapshotSHA256s))
	copy(snapshots, cutover.BarrierSnapshotSHA256s)
	cutover.BarrierSnapshotSHA256s = snapshots
	sort.Strings(cutover.BarrierSnapshotSHA256s)
	cutover.CutoverSHA256 = ""
	if err := validatePredecessorCutoverPayloadV1(cutover); err != nil {
		return PredecessorCutoverV1{}, err
	}
	digest, err := predecessorCutoverDigestV1(cutover)
	if err != nil {
		return PredecessorCutoverV1{}, err
	}
	cutover.CutoverSHA256 = digest
	return cutover, nil
}

func (cutover PredecessorCutoverV1) Validate() error {
	if err := validatePredecessorCutoverPayloadV1(cutover); err != nil {
		return err
	}
	digest, err := predecessorCutoverDigestV1(cutover)
	if err != nil || digest != cutover.CutoverSHA256 {
		return errors.New("predecessor cutover digest disagrees")
	}
	return nil
}

func (cutover PredecessorCutoverV1) CanonicalJSON() ([]byte, error) {
	if err := cutover.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(cutover)
}

func ParsePredecessorCutoverV1(data []byte) (PredecessorCutoverV1, error) {
	var cutover PredecessorCutoverV1
	if len(data) == 0 || len(data) > 1<<20 {
		return cutover, errors.New("predecessor cutover bytes are outside the authority bound")
	}
	if err := ParseCanonical(data, &cutover); err != nil {
		return cutover, err
	}
	return cutover, cutover.Validate()
}

func validatePredecessorCutoverPayloadV1(cutover PredecessorCutoverV1) error {
	if cutover.Kind != "PredecessorCutoverV1" || cutover.SchemaVersion != PredecessorCutoverSchemaV1 ||
		!validAuthorityDomainV1(cutover.AuthorityDomain) || !validDBIdentityV1(cutover.ControllerIdentity) ||
		!validText(cutover.RepositoryIdentity, 1024) || !validSHA256(cutover.DirectorySHA256) ||
		cutover.WriterEpoch == 0 || cutover.LinearizationRevision == 0 || cutover.PredecessorStateV1Revision == 0 ||
		!validSHA256(cutover.PredecessorStateV1SHA256) || !validSHA256(cutover.PredecessorStateV1ArtifactSHA256) ||
		cutover.PredecessorStateV1SHA256 != cutover.PredecessorStateV1ArtifactSHA256 ||
		cutover.V2Revision != cutover.PredecessorStateV1Revision+1 || cutover.TombstoneState != string(PredecessorWriterTombstoned) ||
		cutover.BarrierSnapshotSHA256s == nil || len(cutover.BarrierSnapshotSHA256s) > 256 {
		return errors.New("predecessor cutover identity, revision, artifact, or tombstone is invalid")
	}
	previous := ""
	for _, digest := range cutover.BarrierSnapshotSHA256s {
		if digest <= previous || !validSHA256(digest) {
			return errors.New("cutover barrier snapshots must be sorted unique digests")
		}
		previous = digest
	}
	return nil
}

func predecessorCutoverDigestV1(cutover PredecessorCutoverV1) (string, error) {
	cutover.CutoverSHA256 = ""
	type preimage PredecessorCutoverV1
	return digestJSON(preimage(cutover))
}

// ResourceCountersV2 is embedded rather than hidden behind untyped JSON so a
// fresh process can validate the initial cutover state without reinterpretation.
type ResourceCountersV2 struct {
	ValidatorInvocations            uint64 `json:"validator_invocations"`
	RejectedAssuranceAttempts       uint64 `json:"rejected_assurance_attempts"`
	ExternalObservations            uint64 `json:"external_observations"`
	RetainedEvidenceRefs            uint64 `json:"retained_evidence_refs"`
	RetainedEvidenceBytes           uint64 `json:"retained_evidence_bytes"`
	TransientBytes                  uint64 `json:"transient_bytes"`
	ElapsedMS                       uint64 `json:"elapsed_ms"`
	Graphs                          uint64 `json:"graphs"`
	ActiveValidatorReservations     uint64 `json:"active_validator_reservations"`
	ProcessMemoryReservedBytes      uint64 `json:"process_memory_reserved_bytes"`
	ProcessPIDsReserved             uint64 `json:"process_pids_reserved"`
	ProcessCPUMicrosReserved        uint64 `json:"process_cpu_micros_reserved"`
	ProcessCPUPeriodMicros          uint64 `json:"process_cpu_period_micros"`
	WorkerNOFileReserved            uint64 `json:"worker_nofile_reserved"`
	CacheReservedBytes              uint64 `json:"cache_reserved_bytes"`
	ContainerMemoryReservedBytes    uint64 `json:"container_memory_reserved_bytes"`
	ContainerPIDsReserved           uint64 `json:"container_pids_reserved"`
	ContainerCPUMicrosReserved      uint64 `json:"container_cpu_micros_reserved"`
	ContainerCPUPeriodMicros        uint64 `json:"container_cpu_period_micros"`
	ContainerTmpfsReservedBytes     uint64 `json:"container_tmpfs_reserved_bytes"`
	ContainerShmReservedBytes       uint64 `json:"container_shm_reserved_bytes"`
	ContainerLogReservedBytes       uint64 `json:"container_log_reserved_bytes"`
	ContainerImageReservedBytes     uint64 `json:"container_image_reserved_bytes"`
	DockerDaemonMemoryReservedBytes uint64 `json:"docker_daemon_memory_reserved_bytes"`
	DockerDaemonPIDsReserved        uint64 `json:"docker_daemon_pids_reserved"`
	DockerDaemonCPUMicrosReserved   uint64 `json:"docker_daemon_cpu_micros_reserved"`
	DockerDaemonCPUPeriodMicros     uint64 `json:"docker_daemon_cpu_period_micros"`
	DockerDaemonCacheReservedBytes  uint64 `json:"docker_daemon_cache_reserved_bytes"`
	DockerDaemonLogReservedBytes    uint64 `json:"docker_daemon_log_reserved_bytes"`
}

// ControllerStateV2 is the exact additive successor row installed at r+1.
type ControllerStateV2 struct {
	Kind                             string             `json:"kind"`
	SchemaVersion                    string             `json:"schema_version"`
	AuthorityDomain                  string             `json:"authority_domain"`
	ControllerIdentity               string             `json:"controller_identity"`
	RepositoryIdentity               string             `json:"repository_identity"`
	Revision                         uint64             `json:"revision"`
	PredecessorStateV1ArtifactSHA256 string             `json:"predecessor_state_v1_artifact_sha256"`
	PredecessorStateV1Revision       uint64             `json:"predecessor_state_v1_revision"`
	PredecessorCutoverSHA256         string             `json:"predecessor_cutover_sha256"`
	ActivationV2SHA256               string             `json:"activation_v2_sha256"`
	ActiveLineageSHA256              string             `json:"active_lineage_sha256,omitempty"`
	StageGeneration                  uint64             `json:"stage_generation"`
	CoordinationState                string             `json:"coordination_state"`
	ActiveStageTipSHA256             string             `json:"active_stage_tip_sha256,omitempty"`
	InvalidatedStageTipSHA256        string             `json:"invalidated_stage_tip_sha256,omitempty"`
	LedgerEventTipSHA256             string             `json:"ledger_event_tip_sha256,omitempty"`
	ARequired                        bool               `json:"a_required"`
	CorrectionReentriesUsed          uint64             `json:"correction_reentries_used"`
	ActiveResourceReservations       []string           `json:"active_resource_reservations"`
	SelectedStageEvidenceSHA256      string             `json:"selected_stage_evidence_sha256,omitempty"`
	PREffectAttemptIndexSHA256       string             `json:"pr_effect_attempt_index_sha256,omitempty"`
	MergeEffectAttemptIndexSHA256    string             `json:"merge_effect_attempt_index_sha256,omitempty"`
	EffectAttemptState               string             `json:"effect_attempt_state"`
	CumulativeResourceCounters       ResourceCountersV2 `json:"cumulative_resource_counters"`
}

func (state ControllerStateV2) ValidateInitialCutoverV1() error {
	if state.Kind != "ControllerStateV2" || state.SchemaVersion != ControllerStateSchemaV2 ||
		!validAuthorityDomainV1(state.AuthorityDomain) || !validDBIdentityV1(state.ControllerIdentity) ||
		!validText(state.RepositoryIdentity, 1024) || state.Revision == 0 || state.PredecessorStateV1Revision == 0 ||
		state.Revision != state.PredecessorStateV1Revision+1 || !validSHA256(state.PredecessorStateV1ArtifactSHA256) ||
		!validSHA256(state.PredecessorCutoverSHA256) || !validSHA256(state.ActivationV2SHA256) ||
		state.ActiveLineageSHA256 != "" || state.StageGeneration != 0 || state.CoordinationState != "NONE" ||
		state.ActiveStageTipSHA256 != "" || state.InvalidatedStageTipSHA256 != "" || state.LedgerEventTipSHA256 != "" ||
		state.ARequired || state.CorrectionReentriesUsed != 0 || state.ActiveResourceReservations == nil ||
		len(state.ActiveResourceReservations) != 0 || state.SelectedStageEvidenceSHA256 != "" ||
		state.PREffectAttemptIndexSHA256 != "" || state.MergeEffectAttemptIndexSHA256 != "" || state.EffectAttemptState != "NONE" ||
		state.CumulativeResourceCounters != (ResourceCountersV2{}) {
		return errors.New("initial ControllerStateV2 cutover payload is invalid")
	}
	return nil
}

func (state ControllerStateV2) CanonicalJSON() ([]byte, error) {
	if err := state.ValidateInitialCutoverV1(); err != nil {
		return nil, err
	}
	return json.Marshal(state)
}

// V3Drained proves the exact predecessor ControllerStateV1 has no operational
// continuation. Binding-specific scans are checked by the exclusive fence.
func V3Drained(stateBytes []byte, backendRevision uint64, directory PredecessorAuthorityDirectoryV1) error {
	if err := directory.Validate(); err != nil {
		return fail(V3DrainRequired, "predecessor directory is invalid: %v", err)
	}
	if directory.WriterState != PredecessorWriterOpen || len(directory.ActiveWriterLeases) != 0 {
		return fail(V3DrainRequired, "predecessor directory is not OPEN and writer-drained")
	}
	var state ControllerStateV1
	if err := ParseCanonical(stateBytes, &state); err != nil {
		return fail(V3DrainRequired, "predecessor controller state is not strict canonical JSON: %v", err)
	}
	canonical, err := json.Marshal(state)
	if err != nil || !bytes.Equal(canonical, stateBytes) || state.Kind != "GovernanceControllerStateV1" ||
		state.ControllerIdentity != directory.ControllerIdentity || state.RepositoryIdentity != directory.RepositoryIdentity ||
		state.Revision == 0 || state.Revision != backendRevision || state.IssuedV2Authorities == nil ||
		state.FindingEvidence == nil || state.ExecutionState.AggregateElapsed == "" {
		return fail(V3DrainRequired, "predecessor controller state identity or revision is divergent")
	}
	if state.ActiveInvocation != nil || state.FixBatchLeaseSHA256 != "" {
		return fail(V3DrainRequired, "predecessor invocation or fix-batch reservation remains active")
	}
	if (state.Lease == nil) != (state.MutationState == nil) {
		return fail(V3DrainRequired, "predecessor mutation lease/state pair is partial")
	}
	if state.Lease != nil {
		digest, digestErr := leaseDigest(*state.Lease)
		if digestErr != nil || digest != state.Lease.LeaseSHA256 || state.MutationState.LeaseTipSHA256 != state.Lease.LeaseSHA256 ||
			state.MutationState.LeaseStatus != LeaseConsumed || !validSHA256(state.MutationState.ReceiptTipSHA256) {
			return fail(V3DrainRequired, "predecessor mutation lease is not validly CONSUMED")
		}
	}
	lineageNeverStarted := state.BCapsuleSHA256 == "" && state.CheckpointTip == nil && state.NextStageGrant == nil && state.Derivation == nil
	if !lineageNeverStarted {
		if state.CheckpointTip == nil || state.CheckpointTip.Kind != CheckpointPostMergeAccepted || ValidatePhaseCheckpointV1(*state.CheckpointTip) != nil {
			return fail(V3DrainRequired, "predecessor lineage is not at a valid POST_MERGE_ACCEPTED tip")
		}
	}
	return nil
}

func parseCanonicalSecondV1(value string) (time.Time, error) {
	if len(value) != len("2006-01-02T15:04:05Z") || !strings.HasSuffix(value, "Z") {
		return time.Time{}, errors.New("timestamp is not canonical UTC-second RFC3339")
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.Format("2006-01-02T15:04:05Z") != value {
		return time.Time{}, errors.New("timestamp is not canonical UTC-second RFC3339")
	}
	return parsed, nil
}

var authorityDomainV1Pattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func validAuthorityDomainV1(value string) bool { return authorityDomainV1Pattern.MatchString(value) }

func validDBIdentityV1(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

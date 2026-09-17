// Package authoritybackend provides the shared predecessor fence used by all
// retained V1 writers and the immutable wrapper around the selected workflow
// authority backend. The durable PostgreSQL implementation is supplied by the
// postgres subpackage; this file owns the backend-independent cutover rules.
package authoritybackend

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
)

const maxCanonicalCutoverFieldBytes = 1 << 20

// PredecessorWriterLeaseHandleV1 holds the shared cutover fence across one
// complete state/store/barrier/provider operation.
type PredecessorWriterLeaseHandleV1 interface {
	LeaseV1() governance.PredecessorWriterLeaseV1
	Release() error
}

// PredecessorWriterFenceV1 is the narrow interface consumed by retained
// workflow, ledger, PR, and merge writers.
type PredecessorWriterFenceV1 interface {
	AcquirePredecessorWriterV1(bindingIDs []string, operation string) (PredecessorWriterLeaseHandleV1, error)
}

// PredecessorDrainProbeV1 reopens one bound physical identity while the
// exclusive cutover fence is held. It returns the exact drained barrier-state
// artifact digest registered in the directory.
type PredecessorDrainProbeV1 func(binding governance.PredecessorStoreBindingV1) (string, error)

// PredecessorDirectoryFenceV1 adds the operations needed only by trusted
// cutover composition.
type PredecessorDirectoryFenceV1 interface {
	PredecessorWriterFenceV1
	DirectoryV1() (governance.PredecessorAuthorityDirectoryV1, error)
	WithExclusivePredecessorCutoverV1(expectedDirectoryRevision uint64, operation func(
		open governance.PredecessorAuthorityDirectoryV1,
		tombstoned governance.PredecessorAuthorityDirectoryV1,
		barrierSnapshotSHA256s []string,
	) error) error
}

// SharedPredecessorWriterFenceV1 is an executable shared/exclusive fence. Its
// directory update seam mirrors the PostgreSQL functions, while the RW lock
// gives focused and retained-writer tests the same no-late-writer semantics.
type SharedPredecessorWriterFenceV1 struct {
	cutover       sync.RWMutex
	mu            sync.Mutex
	directory     governance.PredecessorAuthorityDirectoryV1
	active        map[string]governance.PredecessorWriterLeaseV1
	released      map[string]governance.PredecessorWriterLeaseV1
	probes        map[string]PredecessorDrainProbeV1
	ownerSHA256   string
	now           func() time.Time
	leaseDuration time.Duration
}

func NewSharedPredecessorWriterFenceV1(
	directory governance.PredecessorAuthorityDirectoryV1,
	ownerInstanceSHA256 string,
	leaseDuration time.Duration,
	now func() time.Time,
	probes map[string]PredecessorDrainProbeV1,
) (*SharedPredecessorWriterFenceV1, error) {
	if err := directory.Validate(); err != nil {
		return nil, fmt.Errorf("predecessor directory: %w", err)
	}
	if directory.WriterState != governance.PredecessorWriterOpen || len(directory.ActiveWriterLeases) != 0 {
		return nil, errors.New("shared predecessor fence requires an OPEN directory with no pre-existing active leases")
	}
	if !validDigest(ownerInstanceSHA256) || leaseDuration <= 0 || leaseDuration > 24*time.Hour || now == nil {
		return nil, errors.New("shared predecessor fence owner, clock, or lease duration is invalid")
	}
	probeCopy := make(map[string]PredecessorDrainProbeV1, len(probes))
	for _, binding := range directory.Bindings {
		probe, ok := probes[binding.BindingID]
		if !ok || probe == nil {
			return nil, fmt.Errorf("predecessor binding %q has no trusted drain probe", binding.BindingID)
		}
		probeCopy[binding.BindingID] = probe
	}
	if len(probeCopy) != len(probes) {
		return nil, errors.New("predecessor drain probe set contains an unregistered binding")
	}
	return &SharedPredecessorWriterFenceV1{
		directory: directory, active: make(map[string]governance.PredecessorWriterLeaseV1),
		released: make(map[string]governance.PredecessorWriterLeaseV1), probes: probeCopy,
		ownerSHA256: ownerInstanceSHA256, now: now, leaseDuration: leaseDuration,
	}, nil
}

func (f *SharedPredecessorWriterFenceV1) DirectoryV1() (governance.PredecessorAuthorityDirectoryV1, error) {
	if f == nil {
		return governance.PredecessorAuthorityDirectoryV1{}, errors.New("predecessor directory fence is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneDirectory(f.directory)
}

func (f *SharedPredecessorWriterFenceV1) AcquirePredecessorWriterV1(bindingIDs []string, operation string) (PredecessorWriterLeaseHandleV1, error) {
	if f == nil {
		return nil, errors.New("predecessor writer fence is required")
	}
	f.cutover.RLock()
	releaseRead := true
	defer func() {
		if releaseRead {
			f.cutover.RUnlock()
		}
	}()

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.directory.WriterState != governance.PredecessorWriterOpen {
		return nil, errors.New("predecessor directory is TOMBSTONED")
	}
	canonicalIDs, err := canonicalBindingIDs(bindingIDs, f.directory.Bindings)
	if err != nil {
		return nil, err
	}
	opened := f.now().UTC().Truncate(time.Second)
	deadline := opened.Add(f.leaseDuration).UTC().Truncate(time.Second)
	lease := governance.PredecessorWriterLeaseV1{
		Kind: "PredecessorWriterLeaseV1", SchemaVersion: governance.PredecessorWriterLeaseSchemaV1,
		LeaseID:         fmt.Sprintf("predecessor-writer-%020d", f.directory.DirectoryRevision+1),
		AuthorityDomain: f.directory.AuthorityDomain, ControllerIdentity: f.directory.ControllerIdentity,
		RepositoryIdentity: f.directory.RepositoryIdentity, WriterEpoch: f.directory.WriterEpoch,
		BindingIDs: canonicalIDs, Operation: contextcapsule.OperationKind(operation), OwnerInstanceSHA256: f.ownerSHA256,
		OpenedAt: opened.Format("2006-01-02T15:04:05Z"), Deadline: deadline.Format("2006-01-02T15:04:05Z"),
		LeaseState: governance.PredecessorWriterLeaseActive,
	}
	if err := lease.Validate(); err != nil {
		return nil, err
	}
	digest, err := lease.SHA256()
	if err != nil {
		return nil, err
	}
	next := f.directory
	next.DirectoryRevision++
	next.ActiveWriterLeases = append(append([]string(nil), next.ActiveWriterLeases...), digest)
	next, err = governance.SealPredecessorAuthorityDirectoryV1(next)
	if err != nil {
		return nil, err
	}
	f.directory = next
	f.active[digest] = lease
	handle := &sharedWriterLeaseV1{fence: f, digest: digest, lease: lease}
	releaseRead = false
	return handle, nil
}

type sharedWriterLeaseV1 struct {
	mu       sync.Mutex
	fence    *SharedPredecessorWriterFenceV1
	digest   string
	lease    governance.PredecessorWriterLeaseV1
	released bool
}

func (h *sharedWriterLeaseV1) LeaseV1() governance.PredecessorWriterLeaseV1 {
	if h == nil {
		return governance.PredecessorWriterLeaseV1{}
	}
	result := h.lease
	result.BindingIDs = append([]string(nil), result.BindingIDs...)
	return result
}

func (h *sharedWriterLeaseV1) Release() error {
	if h == nil || h.fence == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.released {
		return nil
	}
	f := h.fence
	f.mu.Lock()
	lease, ok := f.active[h.digest]
	if !ok {
		f.mu.Unlock()
		return errors.New("active predecessor writer lease disappeared")
	}
	delete(f.active, h.digest)
	lease.LeaseState = governance.PredecessorWriterLeaseReleased
	f.released[h.digest] = lease
	next := f.directory
	next.DirectoryRevision++
	next.ActiveWriterLeases = removeDigest(next.ActiveWriterLeases, h.digest)
	sealed, err := governance.SealPredecessorAuthorityDirectoryV1(next)
	if err == nil {
		f.directory = sealed
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}
	h.lease = lease
	h.released = true
	f.cutover.RUnlock()
	return nil
}

func (f *SharedPredecessorWriterFenceV1) WithExclusivePredecessorCutoverV1(
	expectedDirectoryRevision uint64,
	operation func(governance.PredecessorAuthorityDirectoryV1, governance.PredecessorAuthorityDirectoryV1, []string) error,
) error {
	if f == nil || operation == nil || expectedDirectoryRevision == 0 {
		return errors.New("exclusive predecessor cutover requires a fence, revision, and operation")
	}
	f.cutover.Lock()
	defer f.cutover.Unlock()

	f.mu.Lock()
	if f.directory.WriterState != governance.PredecessorWriterOpen || f.directory.DirectoryRevision != expectedDirectoryRevision ||
		len(f.directory.ActiveWriterLeases) != 0 || len(f.active) != 0 {
		f.mu.Unlock()
		return errors.New("predecessor cutover conflicts with directory state, revision, or active writers")
	}
	open, err := cloneDirectory(f.directory)
	f.mu.Unlock()
	if err != nil {
		return err
	}

	barrierSnapshots := make([]string, 0, len(open.Bindings))
	for _, binding := range open.Bindings {
		probe := f.probes[binding.BindingID]
		digest, probeErr := probe(binding)
		if probeErr != nil {
			return fmt.Errorf("V3_DRAIN_REQUIRED: binding %s: %w", binding.BindingID, probeErr)
		}
		if digest != binding.BarrierStateSHA256 {
			return fmt.Errorf("V3_DRAIN_REQUIRED: binding %s barrier state changed", binding.BindingID)
		}
		barrierSnapshots = append(barrierSnapshots, digest)
	}
	sort.Strings(barrierSnapshots)
	barrierSnapshots = uniqueStrings(barrierSnapshots)

	tombstoned := open
	tombstoned.WriterState = governance.PredecessorWriterTombstoned
	tombstoned.ActiveWriterLeases = []string{}
	tombstoned.DirectoryRevision++
	tombstoned, err = governance.SealPredecessorAuthorityDirectoryV1(tombstoned)
	if err != nil {
		return err
	}
	if err := operation(open, tombstoned, barrierSnapshots); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.directory.DirectorySHA256 != open.DirectorySHA256 || f.directory.DirectoryRevision != open.DirectoryRevision || len(f.active) != 0 {
		return errors.New("predecessor directory changed inside exclusive cutover")
	}
	f.directory = tombstoned
	return nil
}

// FencedWorkflowAuthorityBackendV1 wraps the exact predecessor backend. It
// never substitutes storage and calls the underlying CAS exactly once after
// the shared lease has been installed.
type FencedWorkflowAuthorityBackendV1 struct {
	predecessor governance.WorkflowAuthorityBackendV1
	fence       PredecessorDirectoryFenceV1
	bindingID   string
	identity    governance.WorkflowBackendIdentityV1
}

func NewFencedWorkflowAuthorityBackendV1(
	predecessor governance.WorkflowAuthorityBackendV1,
	fence PredecessorDirectoryFenceV1,
	bindingID string,
	identity governance.WorkflowBackendIdentityV1,
) (*FencedWorkflowAuthorityBackendV1, error) {
	if predecessor == nil || fence == nil || bindingID == "" {
		return nil, errors.New("predecessor backend, directory fence, and workflow binding are required")
	}
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	domain, err := predecessor.AuthorityDomainV1()
	if err != nil || domain != identity.AuthorityDomain {
		return nil, errors.New("selected predecessor backend authority domain is unavailable or changed")
	}
	directory, err := fence.DirectoryV1()
	if err != nil {
		return nil, err
	}
	identityDigest, _ := identity.SHA256()
	if directory.AuthorityDomain != identity.AuthorityDomain || directory.ControllerIdentity != identity.ControllerIdentity {
		return nil, errors.New("workflow backend identity differs from predecessor directory")
	}
	found := false
	for _, binding := range directory.Bindings {
		if binding.BindingID == bindingID && binding.BindingKind == governance.PredecessorBindingWorkflowState &&
			binding.WorkflowBackendIdentitySHA256 == identityDigest {
			found = true
		}
	}
	if !found {
		return nil, errors.New("selected predecessor backend is not the immutable workflow binding")
	}
	return &FencedWorkflowAuthorityBackendV1{predecessor: predecessor, fence: fence, bindingID: bindingID, identity: identity}, nil
}

func (b *FencedWorkflowAuthorityBackendV1) AuthorityDomainV1() (string, error) {
	if b == nil || b.predecessor == nil {
		return "", errors.New("fenced predecessor backend is required")
	}
	domain, err := b.predecessor.AuthorityDomainV1()
	if err != nil || domain != b.identity.AuthorityDomain {
		return "", errors.New("selected predecessor backend authority domain is unavailable or changed")
	}
	return domain, nil
}

func (b *FencedWorkflowAuthorityBackendV1) LoadWorkflowStateV1(controllerIdentity string) ([]byte, uint64, error) {
	if _, err := b.AuthorityDomainV1(); err != nil || controllerIdentity != b.identity.ControllerIdentity {
		return nil, 0, errors.Join(errors.New("fenced predecessor load identity is invalid"), err)
	}
	return b.predecessor.LoadWorkflowStateV1(controllerIdentity)
}

func (b *FencedWorkflowAuthorityBackendV1) CompareAndSwapWorkflowStateV1(controllerIdentity string, expectedRevision uint64, canonicalState []byte) (swapped bool, resultErr error) {
	if _, err := b.AuthorityDomainV1(); err != nil || controllerIdentity != b.identity.ControllerIdentity {
		return false, errors.Join(errors.New("fenced predecessor CAS identity is invalid"), err)
	}
	lease, err := b.fence.AcquirePredecessorWriterV1([]string{b.bindingID}, string(contextcapsule.OperationMaintenance))
	if err != nil {
		return false, err
	}
	defer func() { resultErr = errors.Join(resultErr, lease.Release()) }()
	return b.predecessor.CompareAndSwapWorkflowStateV1(controllerIdentity, expectedRevision, canonicalState)
}

// PredecessorCutoverTransactionV1 is implemented by the successor backend.
// It must commit all supplied bytes atomically at SERIALIZABLE isolation and
// reconcile an ambiguous commit before returning Applied=true.
type PredecessorCutoverTransactionV1 interface {
	CommitPredecessorCutoverV1(PredecessorCutoverTransactionInputV1) (applied bool, err error)
}

type PredecessorCutoverTransactionInputV1 struct {
	OpenDirectoryCanonical       []byte
	TombstonedDirectoryCanonical []byte
	PredecessorStateCanonical    []byte
	PredecessorStateSHA256       string
	PredecessorStateRevision     uint64
	CutoverCanonical             []byte
	Cutover                      governance.PredecessorCutoverV1
	SuccessorStateCanonical      []byte
	SuccessorState               governance.ControllerStateV2
}

type PredecessorCutoverRequestV1 struct {
	ExpectedDirectoryRevision uint64
	ExpectedStateRevision     uint64
	ActivationV2SHA256        string
}

type PredecessorCutoverResultV1 struct {
	PredecessorStateCanonical []byte
	TombstonedDirectory       governance.PredecessorAuthorityDirectoryV1
	Cutover                   governance.PredecessorCutoverV1
	SuccessorState            governance.ControllerStateV2
}

// CutoverV1 holds the exclusive writer fence, reloads the selected backend
// exactly once, proves V3 drain, and hands the successor transaction exact V1
// bytes plus the V2 r+1 state. No predecessor CAS is performed here.
func (b *FencedWorkflowAuthorityBackendV1) CutoverV1(
	request PredecessorCutoverRequestV1,
	transaction PredecessorCutoverTransactionV1,
) (PredecessorCutoverResultV1, error) {
	var result PredecessorCutoverResultV1
	if b == nil || transaction == nil || request.ExpectedDirectoryRevision == 0 || request.ExpectedStateRevision == 0 ||
		!validDigest(request.ActivationV2SHA256) {
		return result, errors.New("predecessor cutover request or transaction is invalid")
	}
	err := b.fence.WithExclusivePredecessorCutoverV1(request.ExpectedDirectoryRevision, func(
		open governance.PredecessorAuthorityDirectoryV1,
		tombstoned governance.PredecessorAuthorityDirectoryV1,
		barrierSnapshots []string,
	) error {
		stateBytes, revision, err := b.predecessor.LoadWorkflowStateV1(b.identity.ControllerIdentity)
		if err != nil {
			return fmt.Errorf("load stable predecessor state: %w", err)
		}
		if revision != request.ExpectedStateRevision {
			return errors.New("predecessor workflow revision changed before cutover")
		}
		if len(stateBytes) == 0 || len(stateBytes) > maxCanonicalCutoverFieldBytes {
			return errors.New("predecessor workflow state is outside the cutover byte bound")
		}
		if err := governance.V3Drained(stateBytes, revision, open); err != nil {
			return err
		}
		stateDigest := sha256.Sum256(stateBytes)
		stateSHA := hex.EncodeToString(stateDigest[:])
		cutover, err := governance.SealPredecessorCutoverV1(governance.PredecessorCutoverV1{
			Kind: "PredecessorCutoverV1", SchemaVersion: governance.PredecessorCutoverSchemaV1,
			AuthorityDomain: open.AuthorityDomain, ControllerIdentity: open.ControllerIdentity,
			RepositoryIdentity: open.RepositoryIdentity, DirectorySHA256: tombstoned.DirectorySHA256,
			WriterEpoch: open.WriterEpoch, LinearizationRevision: tombstoned.DirectoryRevision,
			PredecessorStateV1Revision: revision, PredecessorStateV1SHA256: stateSHA,
			PredecessorStateV1ArtifactSHA256: stateSHA, BarrierSnapshotSHA256s: barrierSnapshots,
			V2Revision: revision + 1, TombstoneState: string(governance.PredecessorWriterTombstoned),
		})
		if err != nil {
			return err
		}
		successor := governance.ControllerStateV2{
			Kind: "ControllerStateV2", SchemaVersion: governance.ControllerStateSchemaV2,
			AuthorityDomain: open.AuthorityDomain, ControllerIdentity: open.ControllerIdentity,
			RepositoryIdentity: open.RepositoryIdentity, Revision: revision + 1,
			PredecessorStateV1ArtifactSHA256: stateSHA, PredecessorStateV1Revision: revision,
			PredecessorCutoverSHA256: cutover.CutoverSHA256, ActivationV2SHA256: request.ActivationV2SHA256,
			CoordinationState: "NONE", ActiveResourceReservations: []string{}, EffectAttemptState: "NONE",
		}
		if err := successor.ValidateInitialCutoverV1(); err != nil {
			return err
		}
		openBytes, _ := open.CanonicalJSON()
		tombstoneBytes, _ := tombstoned.CanonicalJSON()
		cutoverBytes, _ := cutover.CanonicalJSON()
		successorBytes, _ := successor.CanonicalJSON()
		for name, data := range map[string][]byte{
			"open directory": openBytes, "tombstoned directory": tombstoneBytes,
			"cutover": cutoverBytes, "successor state": successorBytes,
		} {
			if len(data) == 0 || len(data) > maxCanonicalCutoverFieldBytes {
				return fmt.Errorf("%s is outside the cutover byte bound", name)
			}
		}
		input := PredecessorCutoverTransactionInputV1{
			OpenDirectoryCanonical:       append([]byte(nil), openBytes...),
			TombstonedDirectoryCanonical: append([]byte(nil), tombstoneBytes...),
			PredecessorStateCanonical:    append([]byte(nil), stateBytes...), PredecessorStateSHA256: stateSHA,
			PredecessorStateRevision: revision, CutoverCanonical: append([]byte(nil), cutoverBytes...), Cutover: cutover,
			SuccessorStateCanonical: append([]byte(nil), successorBytes...), SuccessorState: successor,
		}
		applied, err := transaction.CommitPredecessorCutoverV1(input)
		if err != nil {
			return err
		}
		if !applied {
			return errors.New("successor cutover transaction did not apply")
		}
		result = PredecessorCutoverResultV1{
			PredecessorStateCanonical: append([]byte(nil), stateBytes...), TombstonedDirectory: tombstoned,
			Cutover: cutover, SuccessorState: successor,
		}
		return nil
	})
	return result, err
}

func cloneDirectory(directory governance.PredecessorAuthorityDirectoryV1) (governance.PredecessorAuthorityDirectoryV1, error) {
	data, err := directory.CanonicalJSON()
	if err != nil {
		return governance.PredecessorAuthorityDirectoryV1{}, err
	}
	return governance.ParsePredecessorAuthorityDirectoryV1(data)
}

func canonicalBindingIDs(values []string, bindings []governance.PredecessorStoreBindingV1) ([]string, error) {
	if len(values) == 0 || len(values) > 256 {
		return nil, errors.New("predecessor writer must bind between 1 and 256 stores")
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	registered := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		registered[binding.BindingID] = struct{}{}
	}
	previous := ""
	for _, bindingID := range result {
		if bindingID == previous {
			return nil, errors.New("predecessor writer binding IDs contain a duplicate")
		}
		if _, ok := registered[bindingID]; !ok {
			return nil, fmt.Errorf("predecessor writer binding %q is not registered", bindingID)
		}
		previous = bindingID
	}
	return result, nil
}

func removeDigest(values []string, target string) []string {
	result := make([]string, 0, len(values)-1)
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

func uniqueStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && bytes.Equal([]byte(hex.EncodeToString(decoded)), []byte(value))
}

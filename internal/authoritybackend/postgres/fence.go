package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authoritybackend"
	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
)

const predecessorFenceCASAttemptsV1 = 32

// PredecessorFenceConfigV1 selects the one durable directory protected by the
// PostgreSQL writer-open/release functions. Probes remain application-owned
// because the registered predecessor stores may be non-PostgreSQL.
type PredecessorFenceConfigV1 struct {
	ControllerIdentity  string
	OwnerInstanceSHA256 string
	LeaseDuration       time.Duration
	Now                 func() time.Time
	Probes              map[string]authoritybackend.PredecessorDrainProbeV1
}

// PostgresPredecessorDirectoryFenceV1 is the cross-host shared/exclusive
// predecessor fence. There is deliberately no process-local admission state:
// every open and release is a durable revision CAS in PostgreSQL, and cutover
// competes on that same revision in abcp_cutover_v1.
type PostgresPredecessorDirectoryFenceV1 struct {
	backend       *PostgresWorkflowAuthorityBackendV1
	controller    string
	ownerSHA256   string
	leaseDuration time.Duration
	now           func() time.Time
	probes        map[string]authoritybackend.PredecessorDrainProbeV1
}

var _ authoritybackend.PredecessorDirectoryFenceV1 = (*PostgresPredecessorDirectoryFenceV1)(nil)

func NewPostgresPredecessorDirectoryFenceV1(backend *PostgresWorkflowAuthorityBackendV1, config PredecessorFenceConfigV1) (*PostgresPredecessorDirectoryFenceV1, error) {
	if backend == nil || backend.pool == nil || validateDBIdentityV1(config.ControllerIdentity) != nil || validateDBIdentityV1(config.OwnerInstanceSHA256) != nil || config.LeaseDuration <= 0 || config.LeaseDuration > 24*time.Hour {
		return nil, errors.New("PostgreSQL predecessor fence backend, identities, or lease duration are invalid")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	fence := &PostgresPredecessorDirectoryFenceV1{
		backend: backend, controller: config.ControllerIdentity, ownerSHA256: config.OwnerInstanceSHA256,
		leaseDuration: config.LeaseDuration, now: config.Now, probes: make(map[string]authoritybackend.PredecessorDrainProbeV1, len(config.Probes)),
	}
	for bindingID, probe := range config.Probes {
		if bindingID == "" || probe == nil {
			return nil, errors.New("PostgreSQL predecessor fence contains an invalid drain probe")
		}
		fence.probes[bindingID] = probe
	}
	directory, err := fence.DirectoryV1()
	if err != nil {
		return nil, err
	}
	registered := make(map[string]struct{}, len(directory.Bindings))
	for _, binding := range directory.Bindings {
		registered[binding.BindingID] = struct{}{}
	}
	for bindingID := range fence.probes {
		if _, ok := registered[bindingID]; !ok {
			return nil, errors.New("predecessor drain probe set contains an unregistered binding")
		}
	}
	return fence, nil
}

// WorkflowBackendIdentityV1 returns the frozen production identity for this
// PostgreSQL backend/controller pair. Bootstrap and runtime composition use
// the same derivation so the directory cannot be rebound to another backend.
func (b *PostgresWorkflowAuthorityBackendV1) WorkflowBackendIdentityV1(controllerIdentity string) (governance.WorkflowBackendIdentityV1, error) {
	if b == nil || b.pool == nil || validateDBIdentityV1(controllerIdentity) != nil {
		return governance.WorkflowBackendIdentityV1{}, errors.New("PostgreSQL workflow backend identity input is invalid")
	}
	preimage := sha256.Sum256([]byte("ABCP-POSTGRES-WORKFLOW-COMPOSITION-V1\x00" + b.authorityDomain + "\x00" + controllerIdentity))
	identity := governance.WorkflowBackendIdentityV1{
		AuthorityDomain: b.authorityDomain, ControllerIdentity: controllerIdentity,
		ImplementationID: "postgres-v1", CompositionSHA256: hex.EncodeToString(preimage[:]),
	}
	return identity, identity.Validate()
}

func (f *PostgresPredecessorDirectoryFenceV1) DirectoryV1() (governance.PredecessorAuthorityDirectoryV1, error) {
	if f == nil || f.backend == nil || f.backend.pool == nil {
		return governance.PredecessorAuthorityDirectoryV1{}, errors.New("PostgreSQL predecessor fence is not open")
	}
	ctx, cancel := boundedContextV1(context.Background(), operationTimeoutV1)
	defer cancel()
	var canonical []byte
	var digest, state string
	var revision, epoch int64
	err := f.backend.pool.QueryRow(ctx, `
		SELECT canonical_directory,btrim(directory_sha256),writer_state,revision,writer_epoch
		FROM abcp_v4.abcp_predecessor_directory_v1
		WHERE authority_domain=$1 AND controller_identity=$2`, f.backend.authorityDomain, f.controller).
		Scan(&canonical, &digest, &state, &revision, &epoch)
	if err != nil {
		return governance.PredecessorAuthorityDirectoryV1{}, fmt.Errorf("load PostgreSQL predecessor directory: %w", err)
	}
	directory, err := governance.ParsePredecessorAuthorityDirectoryV1(canonical)
	if err != nil {
		return governance.PredecessorAuthorityDirectoryV1{}, fmt.Errorf("parse PostgreSQL predecessor directory: %w", err)
	}
	if directory.AuthorityDomain != f.backend.authorityDomain || directory.ControllerIdentity != f.controller || directory.DirectorySHA256 != digest || string(directory.WriterState) != state || directory.DirectoryRevision != uint64(revision) || directory.WriterEpoch != uint64(epoch) {
		return governance.PredecessorAuthorityDirectoryV1{}, errors.New("PostgreSQL predecessor directory columns disagree with canonical bytes")
	}
	return directory, nil
}

func (f *PostgresPredecessorDirectoryFenceV1) AcquirePredecessorWriterV1(bindingIDs []string, operation string) (authoritybackend.PredecessorWriterLeaseHandleV1, error) {
	if f == nil {
		return nil, errors.New("PostgreSQL predecessor fence is required")
	}
	leaseNonce := make([]byte, 16)
	if _, err := rand.Read(leaseNonce); err != nil {
		return nil, fmt.Errorf("mint predecessor writer lease: %w", err)
	}
	opened := f.now().UTC().Truncate(time.Second)
	var pendingDigest string
	var pendingLease governance.PredecessorWriterLeaseV1
	for attempt := 0; attempt < predecessorFenceCASAttemptsV1; attempt++ {
		directory, err := f.DirectoryV1()
		if err != nil {
			return nil, err
		}
		if directory.WriterState != governance.PredecessorWriterOpen {
			return nil, errors.New("predecessor directory is TOMBSTONED")
		}
		if pendingDigest != "" && containsPostgresLeaseDigestV1(directory.ActiveWriterLeases, pendingDigest) {
			return &postgresWriterLeaseHandleV1{fence: f, digest: pendingDigest, lease: pendingLease}, nil
		}
		canonicalIDs, err := postgresCanonicalBindingIDsV1(bindingIDs, directory.Bindings)
		if err != nil {
			return nil, err
		}
		lease := governance.PredecessorWriterLeaseV1{
			Kind: "PredecessorWriterLeaseV1", SchemaVersion: governance.PredecessorWriterLeaseSchemaV1,
			LeaseID: "predecessor-writer-" + hex.EncodeToString(leaseNonce), AuthorityDomain: directory.AuthorityDomain,
			ControllerIdentity: directory.ControllerIdentity, RepositoryIdentity: directory.RepositoryIdentity,
			WriterEpoch: directory.WriterEpoch, BindingIDs: canonicalIDs, Operation: contextcapsule.OperationKind(operation),
			OwnerInstanceSHA256: f.ownerSHA256, OpenedAt: opened.Format("2006-01-02T15:04:05Z"),
			Deadline:   opened.Add(f.leaseDuration).UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z"),
			LeaseState: governance.PredecessorWriterLeaseActive,
		}
		leaseDigest, err := lease.SHA256()
		if err != nil {
			return nil, err
		}
		pendingDigest = leaseDigest
		pendingLease = lease
		next := directory
		next.DirectoryRevision++
		next.ActiveWriterLeases = append(append([]string(nil), next.ActiveWriterLeases...), leaseDigest)
		next, err = governance.SealPredecessorAuthorityDirectoryV1(next)
		if err != nil {
			return nil, err
		}
		nextBytes, _ := next.CanonicalJSON()
		requestID := postgresFenceRequestIDV1("OPEN", leaseDigest, directory.DirectoryRevision, next.DirectoryRevision)
		request := PredecessorWriterOpenRequestV1{
			Kind: "PredecessorWriterOpenRequestV1", SchemaVersion: "predecessor-writer-open-request-v1",
			AuthorityDomain: directory.AuthorityDomain, RequestID: requestID, ControllerIdentity: directory.ControllerIdentity,
			ExpectedDirectoryRevision: directory.DirectoryRevision, NewDirectoryRevision: next.DirectoryRevision,
			NewCanonicalDirectoryB64: base64.StdEncoding.EncodeToString(nextBytes), NewDirectorySHA256: next.DirectorySHA256,
			WriterLeaseSHA256: leaseDigest,
		}
		var result PredecessorWriterOpenResultV1
		if err := f.backend.CallFrozenFunctionV1(context.Background(), "abcp_predecessor_writer_open_v1", request, &result); err != nil {
			if predecessorFenceConflictV1(err) {
				continue
			}
			return nil, err
		}
		if !result.Applied || result.RequestID != requestID || result.ControllerIdentity != f.controller || result.DirectoryRevision != next.DirectoryRevision || result.WriterLeaseSHA256 != leaseDigest {
			return nil, errors.New("PostgreSQL predecessor writer-open returned a divergent result")
		}
		return &postgresWriterLeaseHandleV1{fence: f, digest: leaseDigest, lease: lease}, nil
	}
	return nil, errors.New("PostgreSQL predecessor writer-open exhausted bounded CAS retries")
}

func containsPostgresLeaseDigestV1(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type postgresWriterLeaseHandleV1 struct {
	mu       sync.Mutex
	fence    *PostgresPredecessorDirectoryFenceV1
	digest   string
	lease    governance.PredecessorWriterLeaseV1
	released bool
}

func (h *postgresWriterLeaseHandleV1) LeaseV1() governance.PredecessorWriterLeaseV1 {
	if h == nil {
		return governance.PredecessorWriterLeaseV1{}
	}
	result := h.lease
	result.BindingIDs = append([]string(nil), result.BindingIDs...)
	return result
}

func (h *postgresWriterLeaseHandleV1) Release() error {
	if h == nil || h.fence == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.released {
		return nil
	}
	for attempt := 0; attempt < predecessorFenceCASAttemptsV1; attempt++ {
		directory, err := h.fence.DirectoryV1()
		if err != nil {
			return err
		}
		found := false
		for _, digest := range directory.ActiveWriterLeases {
			found = found || digest == h.digest
		}
		if !found {
			h.released = true
			h.lease.LeaseState = governance.PredecessorWriterLeaseReleased
			return nil
		}
		if directory.WriterState != governance.PredecessorWriterOpen {
			return errors.New("active predecessor lease survived a directory tombstone")
		}
		next := directory
		next.DirectoryRevision++
		next.ActiveWriterLeases = removePostgresLeaseDigestV1(next.ActiveWriterLeases, h.digest)
		next, err = governance.SealPredecessorAuthorityDirectoryV1(next)
		if err != nil {
			return err
		}
		nextBytes, _ := next.CanonicalJSON()
		requestID := postgresFenceRequestIDV1("RELEASE", h.digest, directory.DirectoryRevision, next.DirectoryRevision)
		request := PredecessorWriterReleaseRequestV1{
			Kind: "PredecessorWriterReleaseRequestV1", SchemaVersion: "predecessor-writer-release-request-v1",
			AuthorityDomain: directory.AuthorityDomain, RequestID: requestID, ControllerIdentity: directory.ControllerIdentity,
			ExpectedDirectoryRevision: directory.DirectoryRevision, NewDirectoryRevision: next.DirectoryRevision,
			NewCanonicalDirectoryB64: base64.StdEncoding.EncodeToString(nextBytes), NewDirectorySHA256: next.DirectorySHA256,
			ReleasedWriterLeaseSHA256: h.digest,
		}
		var result PredecessorWriterReleaseResultV1
		if err := h.fence.backend.CallFrozenFunctionV1(context.Background(), "abcp_predecessor_writer_release_v1", request, &result); err != nil {
			if predecessorFenceConflictV1(err) {
				continue
			}
			return err
		}
		if !result.Applied || result.RequestID != requestID || result.ControllerIdentity != h.fence.controller || result.DirectoryRevision != next.DirectoryRevision || result.ReleasedWriterLeaseSHA256 != h.digest {
			return errors.New("PostgreSQL predecessor writer-release returned a divergent result")
		}
		h.released = true
		h.lease.LeaseState = governance.PredecessorWriterLeaseReleased
		return nil
	}
	return errors.New("PostgreSQL predecessor writer-release exhausted bounded CAS retries")
}

func (f *PostgresPredecessorDirectoryFenceV1) WithExclusivePredecessorCutoverV1(expectedDirectoryRevision uint64, operation func(governance.PredecessorAuthorityDirectoryV1, governance.PredecessorAuthorityDirectoryV1, []string) error) error {
	if f == nil || expectedDirectoryRevision == 0 || operation == nil {
		return errors.New("exclusive PostgreSQL predecessor cutover requires a fence, revision, and operation")
	}
	open, err := f.DirectoryV1()
	if err != nil {
		return err
	}
	if open.WriterState != governance.PredecessorWriterOpen || open.DirectoryRevision != expectedDirectoryRevision || len(open.ActiveWriterLeases) != 0 {
		return errors.New("predecessor cutover conflicts with durable directory state, revision, or active writers")
	}
	barriers := make([]string, 0, len(open.Bindings))
	for _, binding := range open.Bindings {
		probe := f.probes[binding.BindingID]
		if probe == nil {
			return fmt.Errorf("predecessor binding %q has no trusted drain probe", binding.BindingID)
		}
		digest, err := probe(binding)
		if err != nil {
			return fmt.Errorf("V3_DRAIN_REQUIRED: binding %s: %w", binding.BindingID, err)
		}
		if digest != binding.BarrierStateSHA256 {
			return fmt.Errorf("V3_DRAIN_REQUIRED: binding %s barrier state changed", binding.BindingID)
		}
		barriers = append(barriers, digest)
	}
	sort.Strings(barriers)
	barriers = uniquePostgresStringsV1(barriers)
	tombstoned := open
	tombstoned.WriterState = governance.PredecessorWriterTombstoned
	tombstoned.ActiveWriterLeases = []string{}
	tombstoned.DirectoryRevision++
	tombstoned, err = governance.SealPredecessorAuthorityDirectoryV1(tombstoned)
	if err != nil {
		return err
	}
	// The callback must commit through abcp_cutover_v1. That function locks
	// and compares the same durable revision, so a remote writer-open either
	// linearizes first and makes cutover fail, or observes the tombstone.
	return operation(open, tombstoned, barriers)
}

func postgresCanonicalBindingIDsV1(values []string, bindings []governance.PredecessorStoreBindingV1) ([]string, error) {
	if len(values) == 0 || len(values) > 256 {
		return nil, errors.New("predecessor writer must bind between 1 and 256 stores")
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	registered := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		registered[binding.BindingID] = struct{}{}
	}
	for index, bindingID := range result {
		if index > 0 && result[index-1] == bindingID {
			return nil, errors.New("predecessor writer binding IDs contain a duplicate")
		}
		if _, ok := registered[bindingID]; !ok {
			return nil, fmt.Errorf("predecessor writer binding %q is not registered", bindingID)
		}
	}
	return result, nil
}

func postgresFenceRequestIDV1(action, digest string, before, after uint64) string {
	preimage := sha256.Sum256([]byte(fmt.Sprintf("ABCP-PREDECESSOR-%s-V1\x00%s\x00%d\x00%d", action, digest, before, after)))
	return hex.EncodeToString(preimage[:])
}

func predecessorFenceConflictV1(err error) bool {
	if err == nil {
		return false
	}
	value := err.Error()
	return strings.Contains(value, "PREDECESSOR_OPEN_CONFLICT") || strings.Contains(value, "PREDECESSOR_RELEASE_CONFLICT") || strings.Contains(value, "serialization") || strings.Contains(value, "serialize access") || strings.Contains(value, "SQLSTATE 40001")
}

func removePostgresLeaseDigestV1(values []string, target string) []string {
	result := make([]string, 0, len(values)-1)
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

func uniquePostgresStringsV1(values []string) []string {
	if len(values) == 0 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

//go:build linux

package runtimecatalog

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/recovery"
)

// Catalog pins the opened service-root directory. Every descendant operation
// is relative to pinned, no-follow directory descriptors, so renaming or
// replacing the path cannot redirect an operation outside the root.
type Catalog struct {
	root              string
	rootFD            int
	rootDev           uint64
	rootIno           uint64
	rootAuthorityData []byte
	catalog           catalogNamespacePin
	runs              catalogNamespacePin
	active            catalogNamespacePin
	config            catalogNamespacePin
	lock              catalogNamespacePin
	runGenerations    catalogNamespacePin
}

type catalogNamespacePin struct {
	name      string
	fd        int
	dev       uint64
	ino       uint64
	directory bool
}

// catalogRootGeneration uses a fixed object order (catalog, runs, active,
// config, lock, run-generation manifest). The compact encoding leaves the
// service root's shared xattr budget available to action-journal generations.
type catalogRootGeneration struct {
	state      byte
	rootDevice uint64
	rootInode  uint64
	objects    [6]catalogObjectIdentity
}

type catalogObjectIdentity struct {
	device uint64
	inode  uint64
}

type runNamespaceIdentityV1 struct {
	Kind           string `json:"kind"`
	SchemaVersion  int    `json:"schema_version"`
	RunComponent   string `json:"run_component"`
	RunDevice      uint64 `json:"run_device"`
	RunInode       uint64 `json:"run_inode"`
	AttemptsDevice uint64 `json:"attempts_device"`
	AttemptsInode  uint64 `json:"attempts_inode"`
}

// runNamespaceGenerationV2 is the durable issuance record for one physical
// run namespace. Unlike the legacy identity-only history record, it also binds
// the exact identity-file generation and chains the complete history prefix.
type runNamespaceGenerationV2 struct {
	Kind              string `json:"kind"`
	SchemaVersion     int    `json:"schema_version"`
	Sequence          int    `json:"s"`
	PriorRecordSHA256 string `json:"p,omitempty"`
	RunComponent      string `json:"c"`
	RunDevice         uint64 `json:"rd"`
	RunInode          uint64 `json:"ri"`
	AttemptsDevice    uint64 `json:"ad,omitempty"`
	AttemptsInode     uint64 `json:"ai,omitempty"`
	IdentityDevice    uint64 `json:"id,omitempty"`
	IdentityInode     uint64 `json:"ii,omitempty"`
	IdentitySHA256    string `json:"ih,omitempty"`
}

type catalogRunAuthorityV1 struct {
	Kind                      string                    `json:"kind"`
	SchemaVersion             int                       `json:"schema_version"`
	State                     string                    `json:"state"`
	RootDevice                uint64                    `json:"root_device"`
	RootInode                 uint64                    `json:"root_inode"`
	HistoryDevice             uint64                    `json:"history_device"`
	HistoryInode              uint64                    `json:"history_inode"`
	IssuanceCount             int                       `json:"issuance_count"`
	IssuanceFinalRecordSHA256 string                    `json:"issuance_final_record_sha256,omitempty"`
	PreparedRunGeneration     *runNamespaceGenerationV2 `json:"prepared_run_generation,omitempty"`
}

type runNamespaceGeneration struct {
	component     string
	runDev        uint64
	runIno        uint64
	attemptsDev   uint64
	attemptsIno   uint64
	identityDev   uint64
	identityIno   uint64
	identityBytes []byte
}

type catalogRunGenerationState struct {
	authority      catalogRunAuthorityV1
	authorityData  []byte
	records        map[string]runNamespaceGeneration
	committedBytes int64
	lastDigest     string
	tail           []byte
}

type runNamespace struct {
	catalog        *Catalog
	component      string
	runFD          int
	runDev         uint64
	runIno         uint64
	attemptsFD     int
	attemptsDev    uint64
	attemptsIno    uint64
	identityDev    uint64
	identityIno    uint64
	identityBytes  []byte
	authorityBytes []byte
}

const (
	catalogRootAuthorityXattr   = "user.abcp.rcat-v1"
	catalogRunAuthorityXattr    = "user.abcp.rcat-runs-v1"
	catalogGenerationInit       = byte(1)
	catalogGenerationReady      = byte(2)
	catalogGenerationReadyV2    = byte(3)
	catalogGenerationMaxBytes   = 117
	catalogXattrCreate          = 1
	catalogXattrReplace         = 2
	runNamespaceIdentityName    = ".namespace.identity.json"
	runNamespacePreparedName    = ".run-namespace.prepared"
	runGenerationsName          = ".run-namespaces.jsonl"
	maxRunGenerationsBytes      = MaxRuns * 512
	maxCatalogRunAuthority      = 1536
	maxRunGenerationRecord      = 512
	catalogRunAuthorityKind     = "RuntimeCatalogRunAuthorityV1"
	catalogRunStateEstablished  = "established"
	catalogRunStatePreparedRun  = "prepared-run"
	catalogRunStatePreparedFull = "prepared-full"
)

var catalogRunIssuanceBoundaryHook = func(string) {}

// Open creates a missing canonical service root, validates controller
// ownership/mode, pins it, and creates the bounded internal namespaces.
func Open(root string) (*Catalog, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) {
		return nil, errors.New("service root must be a canonical absolute directory")
	}
	fd, err := openAbsoluteDirectory(root, true)
	if err != nil {
		return nil, errors.New("open service root safely")
	}
	if err := validateDirectoryFD(fd, true); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	var rootStat syscall.Stat_t
	if err := syscall.Fstat(fd, &rootStat); err != nil {
		syscall.Close(fd)
		return nil, ErrIntegrity
	}
	catalog := &Catalog{
		root: root, rootFD: fd, rootDev: uint64(rootStat.Dev), rootIno: rootStat.Ino,
		catalog: catalogNamespacePin{fd: -1}, runs: catalogNamespacePin{fd: -1},
		active: catalogNamespacePin{fd: -1}, config: catalogNamespacePin{fd: -1}, lock: catalogNamespacePin{fd: -1},
		runGenerations: catalogNamespacePin{fd: -1},
	}
	fail := func(openErr error) (*Catalog, error) {
		_ = catalog.closeDescriptors()
		return nil, openErr
	}
	if err := lockCatalogRootGeneration(fd); err != nil {
		return fail(err)
	}
	rootLocked := true
	defer func() {
		if rootLocked {
			_ = syscall.Flock(fd, syscall.LOCK_UN)
		}
	}()
	authority, authorityData, found, err := readCatalogRootGeneration(fd, catalog.rootDev, catalog.rootIno)
	if err != nil {
		return fail(err)
	}
	if !found {
		if err := requireCatalogGenerationAbsent(fd); err != nil {
			return fail(err)
		}
		authority, authorityData, err = createCatalogRootGeneration(fd, catalog.rootDev, catalog.rootIno)
		if err != nil {
			return fail(err)
		}
	}
	allowCreate := authority.state == catalogGenerationInit
	catalog.rootAuthorityData = append([]byte(nil), authorityData...)
	if catalog.catalog, err = openCatalogDirectoryPin(fd, "catalog", allowCreate); err != nil {
		return fail(err)
	}
	if catalog.runs, err = openCatalogDirectoryPin(catalog.catalog.fd, "runs", allowCreate); err != nil {
		return fail(err)
	}
	if catalog.active, err = openCatalogDirectoryPin(fd, "active", allowCreate); err != nil {
		return fail(err)
	}
	if catalog.config, err = openCatalogDirectoryPin(fd, "config", allowCreate); err != nil {
		return fail(err)
	}
	if catalog.lock, err = openCatalogLockPin(catalog.catalog.fd, allowCreate); err != nil {
		return fail(err)
	}
	if catalog.runGenerations, err = openCatalogFilePin(catalog.catalog.fd, runGenerationsName, allowCreate); err != nil {
		return fail(err)
	}
	expected, expectedData, err := catalog.expectedRootGeneration(catalogGenerationReadyV2)
	if err != nil {
		return fail(err)
	}
	legacyAuthority, legacyData, legacyErr := catalog.expectedRootGeneration(catalogGenerationReady)
	if legacyErr != nil {
		return fail(legacyErr)
	}
	legacy := authority.state == catalogGenerationReady && bytes.Equal(authorityData, legacyData)
	if !allowCreate && !legacy && (authority.state != catalogGenerationReadyV2 || !bytes.Equal(authorityData, expectedData)) {
		return fail(ErrIntegrity)
	}
	if !allowCreate && legacy && authority != legacyAuthority {
		return fail(ErrIntegrity)
	}
	if err := catalog.verifyNamespaceAuthority(); err != nil {
		return fail(err)
	}
	lockFD, err := catalog.acquireLockRaw()
	if err != nil {
		return fail(err)
	}
	bootstrap := allowCreate || legacy
	if err := catalog.reconcileRunNamespaceIssuance(bootstrap); err != nil {
		_ = catalog.releaseLock(lockFD)
		return fail(fmt.Errorf("reconcile runtime catalog run namespaces: %w", err))
	}
	if bootstrap {
		authorityData, err = publishCatalogRootGeneration(fd, expected, expectedData)
		if err != nil {
			_ = catalog.releaseLock(lockFD)
			return fail(err)
		}
		catalog.rootAuthorityData = append([]byte(nil), authorityData...)
	}
	if err := catalog.releaseLock(lockFD); err != nil {
		return fail(err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_UN); err != nil {
		return fail(ErrIntegrity)
	}
	rootLocked = false
	return catalog, nil
}

func OpenServiceRoot(root string) (*Catalog, error) { return Open(root) }

func (c *Catalog) Root() string { return c.root }

func (c *Catalog) Close() error {
	if c == nil || c.rootFD < 0 {
		return nil
	}
	return c.closeDescriptors()
}

func (c *Catalog) closeDescriptors() error {
	if c == nil {
		return nil
	}
	var result error
	for _, pin := range []*catalogNamespacePin{&c.runGenerations, &c.lock, &c.config, &c.active, &c.runs, &c.catalog} {
		if pin.fd >= 0 {
			result = errors.Join(result, syscall.Close(pin.fd))
			pin.fd = -1
		}
	}
	if c.rootFD >= 0 {
		result = errors.Join(result, syscall.Close(c.rootFD))
		c.rootFD = -1
	}
	return result
}

func (c *Catalog) RegisterRun(registration RunRegistrationV1) error {
	if err := validateRunRegistration(registration); err != nil {
		return err
	}
	if err := verifyRegisteredLedgerGeneration(registration.CanonicalLedgerPath, registration.LedgerGeneration); err != nil {
		return fmt.Errorf("%w: registered ledger generation is unsafe or changed", ErrIntegrity)
	}
	if err := validateRegisteredPath(registration.CanonicalEvidenceRoot, true); err != nil {
		return fmt.Errorf("%w: registered evidence root is unsafe", ErrIntegrity)
	}
	data, err := canonicalJSON(registration)
	if err != nil {
		return err
	}
	return c.withLock(func() error {
		if err := verifyRegisteredLedgerGeneration(registration.CanonicalLedgerPath, registration.LedgerGeneration); err != nil {
			return fmt.Errorf("%w: registered ledger generation changed before publication", ErrIntegrity)
		}
		names, err := directoryNames(c.runs.fd)
		if err != nil {
			return ErrIntegrity
		}
		component := storageComponent(registration.RunID)
		found := false
		for _, name := range names {
			if !validStorageComponent(name) {
				return fmt.Errorf("%w: unsafe run directory", ErrIntegrity)
			}
			if name == component {
				found = true
			}
		}
		if !found && len(names) >= MaxRuns {
			return fmt.Errorf("%w: run ceiling reached", ErrIntegrity)
		}
		var namespace *runNamespace
		if found {
			namespace, err = c.openRunNamespaceComponent(component)
		} else {
			namespace, err = c.createRunNamespace(component)
		}
		if err != nil {
			return err
		}
		defer namespace.Close()
		// Hashed storage components cannot preserve lexical ordering. This
		// immutable, bounded sidecar lets listing read every long-ID key plus a
		// maximal page of registration bodies beneath MaxCatalogReadBytes.
		if component != registration.RunID {
			if err := createOrByteVerify(namespace.runFD, "run-id", []byte(registration.RunID+"\n")); err != nil {
				return err
			}
		}
		if err := createOrByteVerify(namespace.runFD, "run.json", data); err != nil {
			return err
		}
		if err := namespace.verify(); err != nil {
			return err
		}
		if err := verifyRegisteredLedgerGeneration(registration.CanonicalLedgerPath, registration.LedgerGeneration); err != nil {
			return fmt.Errorf("%w: registered ledger generation changed during publication", ErrIntegrity)
		}
		return nil
	})
}

func (c *Catalog) RegisterAttempt(registration AttemptRegistrationV1) error {
	if err := validateAttemptRegistration(registration); err != nil {
		return err
	}
	data, err := canonicalJSON(registration)
	if err != nil {
		return err
	}
	return c.withLock(func() error {
		namespace, err := c.openRunNamespace(registration.RunID)
		if err != nil {
			return err
		}
		defer namespace.Close()
		if _, err := readRunRecord(namespace.runFD, storageComponent(registration.RunID)); err != nil {
			return err
		}
		names, err := directoryNames(namespace.attemptsFD)
		if err != nil {
			return ErrIntegrity
		}
		target := storageComponent(registration.AttemptID) + ".json"
		found := false
		for _, name := range names {
			if !strings.HasSuffix(name, ".json") || !validStorageComponent(strings.TrimSuffix(name, ".json")) {
				return fmt.Errorf("%w: unsafe attempt record", ErrIntegrity)
			}
			if name == target {
				found = true
			}
		}
		if !found && len(names) >= MaxAttemptsPerRun {
			return fmt.Errorf("%w: attempt ceiling reached", ErrIntegrity)
		}
		if err := createOrByteVerify(namespace.attemptsFD, target, data); err != nil {
			return err
		}
		return namespace.verify()
	})
}

// ListRuns returns immutable registration facts in lexical run-ID order. It
// deliberately never opens attempts or active-owner namespaces.
func (c *Catalog) ListRuns(after string, limit int) (result []RunRegistrationV1, hasMore bool, resultErr error) {
	if after != "" && ValidateIdentifier(after) != nil {
		return nil, false, ErrInvalidIdentifier
	}
	if limit <= 0 || limit > MaxCatalogPageSize {
		return nil, false, errors.New("catalog page size is outside bounds")
	}
	lockFD, err := c.acquireLock()
	if err != nil {
		return nil, false, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, c.releaseLock(lockFD))
		if resultErr != nil {
			result = nil
			hasMore = false
		}
	}()
	names, err := directoryNames(c.runs.fd)
	if err != nil || len(names) > MaxRuns {
		return nil, false, ErrIntegrity
	}
	authorities, err := c.readRunNamespaceAuthorities()
	if err != nil || len(authorities) != len(names) {
		return nil, false, ErrIntegrity
	}
	type runKey struct {
		component string
		runID     string
	}
	keys := make([]runKey, 0, len(names))
	totalBytes := 0
	for _, name := range names {
		if !validStorageComponent(name) {
			return nil, false, fmt.Errorf("%w: unsafe run directory", ErrIntegrity)
		}
		authorityData, found := authorities[name]
		if !found {
			return nil, false, ErrIntegrity
		}
		runID := name
		if isEncodedStorageComponent(name) {
			namespace, openErr := c.openRunDirectoryAuthorized(name, authorityData)
			if openErr != nil {
				return nil, false, openErr
			}
			keyData, readErr := readProtectedAt(namespace.runFD, "run-id", MaxRunIDIndexSize)
			verifyErr := namespace.verifyRunDirectory()
			_ = namespace.Close()
			if readErr != nil {
				return nil, false, fmt.Errorf("%w: catalog key is unavailable", ErrIntegrity)
			}
			if verifyErr != nil {
				return nil, false, verifyErr
			}
			totalBytes += len(keyData)
			if totalBytes > MaxCatalogReadBytes || len(keyData) < 2 || keyData[len(keyData)-1] != '\n' {
				return nil, false, fmt.Errorf("%w: catalog key read ceiling or encoding invalid", ErrIntegrity)
			}
			runID = string(keyData[:len(keyData)-1])
			if ValidateIdentifier(runID) != nil || storageComponent(runID) != name {
				return nil, false, fmt.Errorf("%w: catalog key does not bind its registration", ErrIntegrity)
			}
		}
		keys = append(keys, runKey{component: name, runID: runID})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].runID < keys[j].runID })
	selected := make([]runKey, 0, limit)
	for _, key := range keys {
		if key.runID <= after {
			continue
		}
		if len(selected) == limit {
			break
		}
		selected = append(selected, key)
	}
	hasMore = false
	if len(selected) > 0 {
		last := selected[len(selected)-1].runID
		for _, key := range keys {
			if key.runID > last {
				hasMore = true
				break
			}
		}
	}
	result = make([]RunRegistrationV1, 0, len(selected))
	for _, key := range selected {
		namespace, openErr := c.openRunDirectoryAuthorized(key.component, authorities[key.component])
		if openErr != nil {
			return nil, false, openErr
		}
		record, count, readErr := readRunRecordBytes(namespace.runFD, key.component)
		verifyErr := namespace.verifyRunDirectory()
		_ = namespace.Close()
		if readErr != nil {
			return nil, false, readErr
		}
		if verifyErr != nil {
			return nil, false, verifyErr
		}
		totalBytes += count
		if totalBytes > MaxCatalogReadBytes {
			return nil, false, fmt.Errorf("%w: catalog read ceiling exceeded", ErrIntegrity)
		}
		if record.RunID != key.runID {
			return nil, false, fmt.Errorf("%w: catalog key identity mismatch", ErrIntegrity)
		}
		result = append(result, record)
	}
	return result, hasMore, nil
}

func (c *Catalog) ReadRun(runID string) (result RunRegistrationV1, resultErr error) {
	if err := ValidateIdentifier(runID); err != nil {
		return RunRegistrationV1{}, err
	}
	lockFD, err := c.acquireLock()
	if err != nil {
		return RunRegistrationV1{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, c.releaseLock(lockFD))
		if resultErr != nil {
			result = RunRegistrationV1{}
		}
	}()
	namespace, err := c.openRunDirectoryComponent(storageComponent(runID))
	if err != nil {
		return RunRegistrationV1{}, err
	}
	defer namespace.Close()
	record, err := readRunRecord(namespace.runFD, storageComponent(runID))
	if err == nil && record.RunID != runID {
		return RunRegistrationV1{}, ErrIntegrity
	}
	if err == nil {
		err = namespace.verifyRunDirectory()
	}
	return record, err
}

func (c *Catalog) ReadAttempt(runID, attemptID string) (result AttemptRegistrationV1, resultErr error) {
	if ValidateIdentifier(runID) != nil || ValidateIdentifier(attemptID) != nil {
		return AttemptRegistrationV1{}, ErrInvalidIdentifier
	}
	lockFD, err := c.acquireLock()
	if err != nil {
		return AttemptRegistrationV1{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, c.releaseLock(lockFD))
		if resultErr != nil {
			result = AttemptRegistrationV1{}
		}
	}()
	namespace, err := c.openRunNamespace(runID)
	if err != nil {
		return AttemptRegistrationV1{}, err
	}
	defer namespace.Close()
	data, err := readProtectedAt(namespace.attemptsFD, storageComponent(attemptID)+".json", MaxRegistrationSize)
	if err != nil {
		return AttemptRegistrationV1{}, err
	}
	var registration AttemptRegistrationV1
	if err := decodeCanonical(data, &registration); err != nil || validateAttemptRegistration(registration) != nil || registration.RunID != runID || registration.AttemptID != attemptID {
		return AttemptRegistrationV1{}, ErrIntegrity
	}
	if err := namespace.verify(); err != nil {
		return AttemptRegistrationV1{}, err
	}
	return registration, nil
}

// AcquireOwnerLeaseGuard serializes active-owner generation/state operations
// under the catalog lock for no longer than the frozen two-second bound.
func (c *Catalog) AcquireOwnerLeaseGuard(runID string) (*OwnerLeaseGuard, error) {
	if err := ValidateIdentifier(runID); err != nil {
		return nil, err
	}
	lockFD, err := c.acquireLock()
	if err != nil {
		return nil, err
	}
	return &OwnerLeaseGuard{catalog: c, runID: runID, lockFD: lockFD}, nil
}

func (c *Catalog) InstallOwnerLease(runID, attemptID string, process recovery.ProcessIdentity) (result ActiveOwnerLeaseV1, resultErr error) {
	guard, err := c.AcquireOwnerLeaseGuard(runID)
	if err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, guard.Close())
		if resultErr != nil {
			result = ActiveOwnerLeaseV1{}
		}
	}()
	return guard.Install(attemptID, process)
}

type OwnerLeaseGuard struct {
	catalog *Catalog
	runID   string
	lockFD  int
	closed  bool
}

func (g *OwnerLeaseGuard) Close() error {
	if g == nil || g.closed {
		return nil
	}
	verifyErr := g.verifyAuthority()
	g.closed = true
	return errors.Join(verifyErr, g.catalog.releaseLock(g.lockFD))
}

func (g *OwnerLeaseGuard) Lease() (ActiveOwnerLeaseV1, error) {
	if g == nil || g.closed {
		return ActiveOwnerLeaseV1{}, errors.New("owner lease guard is closed")
	}
	if err := g.verifyAuthority(); err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	activeFD, err := g.catalog.duplicatePinnedDirectory(&g.catalog.active)
	if err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	defer syscall.Close(activeFD)
	lease, err := readLease(activeFD, g.runID)
	if err == nil {
		err = g.verifyAuthority()
	}
	return lease, err
}

func (g *OwnerLeaseGuard) Install(attemptID string, process recovery.ProcessIdentity) (ActiveOwnerLeaseV1, error) {
	if g == nil || g.closed || ValidateIdentifier(attemptID) != nil {
		return ActiveOwnerLeaseV1{}, ErrInvalidIdentifier
	}
	if err := g.verifyAuthority(); err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	namespace, err := g.verifyRegisteredAttempt(attemptID)
	if err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	defer namespace.Close()
	observed, err := recovery.CaptureProcessIdentity(process.PID)
	if err != nil || observed != process {
		return ActiveOwnerLeaseV1{}, errors.New("exact live owner process identity is required")
	}
	activeFD, err := g.catalog.duplicatePinnedDirectory(&g.catalog.active)
	if err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	defer syscall.Close(activeFD)
	prior, err := readLease(activeFD, g.runID)
	generation := uint64(1)
	if err == nil {
		if prior.State != OwnerLeaseRetired {
			return ActiveOwnerLeaseV1{}, errors.New("prior owner lease generation is not retired")
		}
		if prior.LeaseGeneration == ^uint64(0) {
			return ActiveOwnerLeaseV1{}, ErrIntegrity
		}
		generation = prior.LeaseGeneration + 1
	} else if !errors.Is(err, os.ErrNotExist) {
		return ActiveOwnerLeaseV1{}, err
	}
	activeCount, err := countActiveOwners(activeFD)
	if err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	if activeCount >= MaxActiveOwners {
		return ActiveOwnerLeaseV1{}, fmt.Errorf("%w: active owner ceiling reached", ErrIntegrity)
	}
	lease := ActiveOwnerLeaseV1{Kind: "ActiveOwnerLeaseV1", RunID: g.runID, AttemptID: attemptID, LeaseGeneration: generation, Process: process, State: OwnerLeaseActive}
	lease.LeaseID = leaseIdentityDigest(lease.RunID, lease.AttemptID, lease.LeaseGeneration, lease.Process)
	if err := validateLease(lease); err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	data, _ := canonicalJSON(lease)
	if generation == 1 {
		if err := createOrByteVerify(activeFD, storageComponent(g.runID)+".json", data); err != nil {
			return ActiveOwnerLeaseV1{}, err
		}
	} else if err := atomicReplace(activeFD, storageComponent(g.runID)+".json", data); err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	if err := errors.Join(namespace.verify(), g.verifyAuthority()); err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	return lease, nil
}

func (g *OwnerLeaseGuard) MarkClosing(expectedLeaseID string, drainThrough uint64) (ActiveOwnerLeaseV1, error) {
	lease, activeFD, err := g.openCurrent()
	if err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	defer syscall.Close(activeFD)
	if lease.LeaseID != expectedLeaseID || lease.State != OwnerLeaseActive {
		return ActiveOwnerLeaseV1{}, ErrOwnerNotActive
	}
	lease.State = OwnerLeaseClosing
	lease.DrainThroughJournalSequence = &drainThrough
	data, _ := canonicalJSON(lease)
	if err := atomicReplace(activeFD, storageComponent(g.runID)+".json", data); err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	if err := g.verifyAuthority(); err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	return lease, nil
}

func (g *OwnerLeaseGuard) Retire(expectedLeaseID string) (ActiveOwnerLeaseV1, error) {
	lease, activeFD, err := g.openCurrent()
	if err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	defer syscall.Close(activeFD)
	if lease.LeaseID != expectedLeaseID || lease.State != OwnerLeaseClosing {
		return ActiveOwnerLeaseV1{}, ErrOwnerNotActive
	}
	lease.State = OwnerLeaseRetired
	data, _ := canonicalJSON(lease)
	if err := atomicReplace(activeFD, storageComponent(g.runID)+".json", data); err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	if err := g.verifyAuthority(); err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	return lease, nil
}

func (g *OwnerLeaseGuard) openCurrent() (ActiveOwnerLeaseV1, int, error) {
	if g == nil || g.closed {
		return ActiveOwnerLeaseV1{}, -1, errors.New("owner lease guard is closed")
	}
	if err := g.verifyAuthority(); err != nil {
		return ActiveOwnerLeaseV1{}, -1, err
	}
	activeFD, err := g.catalog.duplicatePinnedDirectory(&g.catalog.active)
	if err != nil {
		return ActiveOwnerLeaseV1{}, -1, err
	}
	lease, err := readLease(activeFD, g.runID)
	if err != nil {
		syscall.Close(activeFD)
		return ActiveOwnerLeaseV1{}, -1, err
	}
	return lease, activeFD, nil
}

func (g *OwnerLeaseGuard) verifyRegisteredAttempt(attemptID string) (*runNamespace, error) {
	namespace, err := g.catalog.openRunNamespace(g.runID)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*runNamespace, error) {
		_ = namespace.Close()
		return nil, err
	}
	if _, err := readRunRecord(namespace.runFD, storageComponent(g.runID)); err != nil {
		return fail(err)
	}
	data, err := readProtectedAt(namespace.attemptsFD, storageComponent(attemptID)+".json", MaxRegistrationSize)
	if err != nil {
		return fail(err)
	}
	var registration AttemptRegistrationV1
	if err := decodeCanonical(data, &registration); err != nil || validateAttemptRegistration(registration) != nil || registration.RunID != g.runID || registration.AttemptID != attemptID {
		return fail(ErrIntegrity)
	}
	if err := namespace.verify(); err != nil {
		return fail(err)
	}
	return namespace, nil
}

func (g *OwnerLeaseGuard) verifyAuthority() error {
	if g == nil || g.catalog == nil || g.closed || g.lockFD < 0 {
		return ErrIntegrity
	}
	if err := g.catalog.verifyLockFD(g.lockFD); err != nil {
		return err
	}
	return g.catalog.verifyNamespaceAuthority()
}

func VerifyLiveOwner(lease ActiveOwnerLeaseV1) error {
	if err := validateLease(lease); err != nil {
		return err
	}
	observed, err := recovery.CaptureProcessIdentity(lease.Process.PID)
	if err != nil || observed != lease.Process {
		return ErrOwnerNotActive
	}
	if lease.State != OwnerLeaseActive && lease.State != OwnerLeaseClosing {
		return ErrOwnerNotActive
	}
	return nil
}

func LeaseMatchesProcess(lease ActiveOwnerLeaseV1, observed recovery.ProcessIdentity) bool {
	return validateLease(lease) == nil && lease.Process == observed && (lease.State == OwnerLeaseActive || lease.State == OwnerLeaseClosing)
}

func (c *Catalog) openRunDirectory(runID string) (int, error) {
	namespace, err := c.openRunNamespace(runID)
	if err != nil {
		return -1, err
	}
	_ = syscall.Close(namespace.attemptsFD)
	namespace.attemptsFD = -1
	runFD := namespace.runFD
	namespace.runFD = -1
	return runFD, nil
}

func (c *Catalog) createRunNamespace(component string) (*runNamespace, error) {
	if c == nil || !validStorageComponent(component) {
		return nil, ErrIntegrity
	}
	state, history, historyStat, err := c.loadRunNamespaceGenerationState()
	if err != nil {
		return nil, err
	}
	defer history.Close()
	if state.authority.State != catalogRunStateEstablished || len(state.tail) != 0 || len(state.records) != state.authority.IssuanceCount ||
		state.authority.IssuanceCount >= MaxRuns {
		return nil, ErrIntegrity
	}
	if _, duplicate := state.records[component]; duplicate {
		return nil, ErrIntegrity
	}
	if found, probeErr := catalogDirectoryExists(c.runs.fd, component); probeErr != nil || found {
		return nil, ErrIntegrity
	}
	if found, probeErr := catalogDirectoryExists(c.runs.fd, runNamespacePreparedName); probeErr != nil || found {
		return nil, ErrIntegrity
	}
	if err := syscall.Mkdirat(c.runs.fd, runNamespacePreparedName, 0o700); err != nil {
		return nil, ErrIntegrity
	}
	catalogRunIssuanceBoundaryHook("stage-created")
	if syscall.Fsync(c.runs.fd) != nil {
		return nil, ErrIntegrity
	}
	runFD, err := openDirectoryAt(c.runs.fd, runNamespacePreparedName, false)
	if err != nil {
		return nil, ErrIntegrity
	}
	closeRun := true
	defer func() {
		if closeRun {
			_ = syscall.Close(runFD)
		}
	}()
	var runStat syscall.Stat_t
	if syscall.Fstat(runFD, &runStat) != nil || validateDirectoryFD(runFD, true) != nil {
		return nil, ErrIntegrity
	}
	catalogRunIssuanceBoundaryHook("unbound-stage")
	prepared := runNamespaceGenerationV2{
		Kind: "RuntimeCatalogRunNamespaceGenerationV2", SchemaVersion: 2,
		Sequence: state.authority.IssuanceCount + 1, PriorRecordSHA256: state.lastDigest,
		RunComponent: component, RunDevice: uint64(runStat.Dev), RunInode: runStat.Ino,
	}
	_, preparedRunData, err := makeCatalogRunAuthority(c, catalogRunStatePreparedRun,
		state.authority.IssuanceCount, state.lastDigest, &prepared)
	if err != nil || c.replaceCatalogRunAuthority(state.authorityData, preparedRunData, "prepare-run") != nil {
		return nil, ErrIntegrity
	}
	catalogRunIssuanceBoundaryHook("prepared-run")
	if err := syscall.Mkdirat(runFD, "attempts", 0o700); err != nil {
		return nil, ErrIntegrity
	}
	catalogRunIssuanceBoundaryHook("attempts-visible")
	if syscall.Fsync(runFD) != nil {
		return nil, ErrIntegrity
	}
	attemptsFD, err := openDirectoryAt(runFD, "attempts", false)
	if err != nil {
		return nil, ErrIntegrity
	}
	closeAttempts := true
	defer func() {
		if closeAttempts {
			_ = syscall.Close(attemptsFD)
		}
	}()
	var attemptsStat syscall.Stat_t
	if syscall.Fstat(attemptsFD, &attemptsStat) != nil || validateDirectoryFD(attemptsFD, true) != nil {
		return nil, ErrIntegrity
	}
	catalogRunIssuanceBoundaryHook("attempts-created")
	identity := runNamespaceIdentityV1{
		Kind: "RuntimeCatalogRunNamespaceIdentityV1", SchemaVersion: 1, RunComponent: component,
		RunDevice: uint64(runStat.Dev), RunInode: runStat.Ino,
		AttemptsDevice: uint64(attemptsStat.Dev), AttemptsInode: attemptsStat.Ino,
	}
	identityData, err := canonicalJSON(identity)
	if err != nil || len(identityData) > MaxRegistrationSize {
		return nil, ErrIntegrity
	}
	identityStat, err := writePreparedRunIdentity(runFD, identityData)
	if err != nil {
		return nil, err
	}
	catalogRunIssuanceBoundaryHook("identity-written")
	prepared.AttemptsDevice, prepared.AttemptsInode = uint64(attemptsStat.Dev), attemptsStat.Ino
	prepared.IdentityDevice, prepared.IdentityInode = uint64(identityStat.Dev), identityStat.Ino
	prepared.IdentitySHA256 = sha256Bytes(identityData)
	_, preparedFullData, err := makeCatalogRunAuthority(c, catalogRunStatePreparedFull,
		state.authority.IssuanceCount, state.lastDigest, &prepared)
	if err != nil || c.replaceCatalogRunAuthority(preparedRunData, preparedFullData, "prepare-full") != nil {
		return nil, ErrIntegrity
	}
	catalogRunIssuanceBoundaryHook("prepared-full")
	if err := syscall.Renameat(c.runs.fd, runNamespacePreparedName, c.runs.fd, component); err != nil {
		return nil, ErrIntegrity
	}
	catalogRunIssuanceBoundaryHook("directory-visible")
	if verifyNamedCatalogObject(c.runs.fd, component, runFD, uint64(runStat.Dev), runStat.Ino, true) != nil || syscall.Fsync(c.runs.fd) != nil {
		return nil, ErrIntegrity
	}
	catalogRunIssuanceBoundaryHook("published-directory")
	line, err := marshalRunNamespaceGeneration(prepared)
	if err != nil {
		return nil, err
	}
	line = append(line, '\n')
	if _, err := history.Seek(state.committedBytes, io.SeekStart); err != nil {
		return nil, ErrIntegrity
	}
	split := len(line) / 2
	if split == 0 {
		split = 1
	}
	if err := writeCatalogBytes(history, line[:split]); err != nil {
		return nil, err
	}
	catalogRunIssuanceBoundaryHook("partial-history")
	if err := writeCatalogBytes(history, line[split:]); err != nil {
		return nil, ErrIntegrity
	}
	catalogRunIssuanceBoundaryHook("history-complete-visible")
	if history.Sync() != nil {
		return nil, ErrIntegrity
	}
	catalogRunIssuanceBoundaryHook("complete-history")
	var appended syscall.Stat_t
	if syscall.Fstat(int(history.Fd()), &appended) != nil || appended.Size != state.committedBytes+int64(len(line)) ||
		appended.Dev != historyStat.Dev || appended.Ino != historyStat.Ino ||
		verifyNamedCatalogObject(c.catalog.fd, runGenerationsName, int(history.Fd()), c.runGenerations.dev, c.runGenerations.ino, false) != nil {
		return nil, ErrIntegrity
	}
	nextDigest := catalogRunRecordDigest(state.lastDigest, line[:len(line)-1])
	_, establishedData, err := makeCatalogRunAuthority(c, catalogRunStateEstablished, prepared.Sequence, nextDigest, nil)
	if err != nil || setCatalogRunAuthority(c.rootFD, establishedData, catalogXattrReplace) != nil {
		return nil, ErrIntegrity
	}
	catalogRunIssuanceBoundaryHook("checkpoint-visible")
	if syscall.Fsync(c.rootFD) != nil || verifyCatalogRunAuthorityData(c.rootFD, establishedData) != nil {
		return nil, ErrIntegrity
	}
	catalogRunIssuanceBoundaryHook("checkpoint-durable")
	namespace := &runNamespace{
		catalog: c, component: component, runFD: runFD, runDev: uint64(runStat.Dev), runIno: runStat.Ino,
		attemptsFD: attemptsFD, attemptsDev: uint64(attemptsStat.Dev), attemptsIno: attemptsStat.Ino,
		identityDev: uint64(identityStat.Dev), identityIno: identityStat.Ino,
		identityBytes: append([]byte(nil), identityData...), authorityBytes: append([]byte(nil), identityData...),
	}
	if err := namespace.verify(); err != nil {
		return nil, err
	}
	closeRun, closeAttempts = false, false
	return namespace, nil
}

func (c *Catalog) openRunNamespace(runID string) (*runNamespace, error) {
	if ValidateIdentifier(runID) != nil {
		return nil, ErrInvalidIdentifier
	}
	return c.openRunNamespaceComponent(storageComponent(runID))
}

func (c *Catalog) openRunNamespaceComponent(component string) (*runNamespace, error) {
	namespace, err := c.openRunDirectoryComponent(component)
	if err != nil {
		return nil, err
	}
	attemptsFD, err := openDirectoryAt(namespace.runFD, "attempts", false)
	if err != nil {
		_ = namespace.Close()
		return nil, err
	}
	var attemptsStat syscall.Stat_t
	if syscall.Fstat(attemptsFD, &attemptsStat) != nil || validateDirectoryFD(attemptsFD, true) != nil ||
		uint64(attemptsStat.Dev) != namespace.attemptsDev || attemptsStat.Ino != namespace.attemptsIno {
		syscall.Close(attemptsFD)
		_ = namespace.Close()
		return nil, ErrIntegrity
	}
	namespace.attemptsFD = attemptsFD
	if err := namespace.verify(); err != nil {
		_ = namespace.Close()
		return nil, err
	}
	return namespace, nil
}

func (c *Catalog) openRunDirectoryComponent(component string) (*runNamespace, error) {
	if c == nil || !validStorageComponent(component) {
		return nil, ErrIntegrity
	}
	authorities, err := c.readRunNamespaceAuthorities()
	if err != nil {
		return nil, err
	}
	authorityData, found := authorities[component]
	if !found {
		fd, openErr := syscall.Openat(c.runs.fd, component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if openErr == syscall.ENOENT {
			return nil, openErr
		}
		if openErr == nil {
			_ = syscall.Close(fd)
		}
		return nil, ErrIntegrity
	}
	return c.openRunDirectoryAuthorized(component, authorityData)
}

func (c *Catalog) openRunDirectoryAuthorized(component string, authority runNamespaceGeneration) (*runNamespace, error) {
	if c == nil || !validStorageComponent(component) || authority.component != component || len(authority.identityBytes) == 0 {
		return nil, ErrIntegrity
	}
	if err := c.verifyNamespaceAuthority(); err != nil {
		return nil, err
	}
	runFD, err := openDirectoryAt(c.runs.fd, component, false)
	if err != nil {
		return nil, err
	}
	var runStat syscall.Stat_t
	if syscall.Fstat(runFD, &runStat) != nil || validateDirectoryFD(runFD, true) != nil {
		syscall.Close(runFD)
		return nil, ErrIntegrity
	}
	data, identityStat, err := readProtectedAtGeneration(runFD, runNamespaceIdentityName, MaxRegistrationSize)
	if err != nil {
		syscall.Close(runFD)
		return nil, fmt.Errorf("%w: run namespace identity is unavailable", ErrIntegrity)
	}
	var identity runNamespaceIdentityV1
	if decodeCanonical(data, &identity) != nil || identity.Kind != "RuntimeCatalogRunNamespaceIdentityV1" || identity.SchemaVersion != 1 ||
		identity.RunComponent != component || identity.RunDevice != uint64(runStat.Dev) || identity.RunInode != runStat.Ino ||
		identity.AttemptsDevice == 0 || identity.AttemptsInode == 0 || uint64(runStat.Dev) != authority.runDev || runStat.Ino != authority.runIno ||
		identity.AttemptsDevice != authority.attemptsDev || identity.AttemptsInode != authority.attemptsIno ||
		!bytes.Equal(data, authority.identityBytes) || (authority.identityIno != 0 &&
		(uint64(identityStat.Dev) != authority.identityDev || identityStat.Ino != authority.identityIno)) {
		syscall.Close(runFD)
		return nil, ErrIntegrity
	}
	namespace := &runNamespace{catalog: c, component: component, runFD: runFD, runDev: uint64(runStat.Dev), runIno: runStat.Ino,
		attemptsFD: -1, attemptsDev: identity.AttemptsDevice, attemptsIno: identity.AttemptsInode,
		identityDev: authority.identityDev, identityIno: authority.identityIno,
		identityBytes: append([]byte(nil), data...), authorityBytes: append([]byte(nil), authority.identityBytes...)}
	if err := namespace.verifyRunDirectory(); err != nil {
		_ = namespace.Close()
		return nil, err
	}
	return namespace, nil
}

func (n *runNamespace) verify() error {
	if n == nil || n.catalog == nil || n.runFD < 0 || n.attemptsFD < 0 || len(n.identityBytes) == 0 {
		return ErrIntegrity
	}
	if err := n.verifyRunDirectory(); err != nil {
		return err
	}
	if err := verifyNamedCatalogObject(n.runFD, "attempts", n.attemptsFD, n.attemptsDev, n.attemptsIno, true); err != nil {
		return err
	}
	return nil
}

func (n *runNamespace) verifyRunDirectory() error {
	if n == nil || n.catalog == nil || n.runFD < 0 || len(n.identityBytes) == 0 || !bytes.Equal(n.identityBytes, n.authorityBytes) {
		return ErrIntegrity
	}
	if err := n.catalog.verifyNamespaceAuthority(); err != nil {
		return err
	}
	if err := verifyNamedCatalogObject(n.catalog.runs.fd, n.component, n.runFD, n.runDev, n.runIno, true); err != nil {
		return err
	}
	data, identityStat, err := readProtectedAtGeneration(n.runFD, runNamespaceIdentityName, MaxRegistrationSize)
	if err != nil || !bytes.Equal(data, n.identityBytes) || (n.identityIno != 0 &&
		(uint64(identityStat.Dev) != n.identityDev || identityStat.Ino != n.identityIno)) {
		return ErrIntegrity
	}
	return nil
}

func (n *runNamespace) Close() error {
	if n == nil {
		return nil
	}
	var result error
	if n.attemptsFD >= 0 {
		result = errors.Join(result, syscall.Close(n.attemptsFD))
		n.attemptsFD = -1
	}
	if n.runFD >= 0 {
		result = errors.Join(result, syscall.Close(n.runFD))
		n.runFD = -1
	}
	return result
}

func (c *Catalog) readRunNamespaceAuthorities() (map[string]runNamespaceGeneration, error) {
	state, history, _, err := c.loadRunNamespaceGenerationState()
	if err != nil {
		return nil, err
	}
	defer history.Close()
	if state.authority.State != catalogRunStateEstablished || len(state.tail) != 0 {
		return nil, ErrIntegrity
	}
	return state.records, nil
}

func (c *Catalog) reconcileRunNamespaceIssuance(allowBootstrap bool) error {
	if err := c.verifyNamespaceAuthority(); err != nil {
		return err
	}
	_, found, err := getCatalogRunAuthority(c.rootFD)
	if err != nil {
		return err
	}
	if !found {
		if !allowBootstrap {
			return ErrIntegrity
		}
		if err := c.bootstrapCatalogRunAuthority(); err != nil {
			return err
		}
	}
	state, history, historyStat, err := c.loadRunNamespaceGenerationState()
	if err != nil {
		return fmt.Errorf("load run namespace issuance: %w", err)
	}
	defer history.Close()
	switch state.authority.State {
	case catalogRunStateEstablished:
		if len(state.tail) != 0 {
			return fmt.Errorf("established run authority has a history tail: %w", ErrIntegrity)
		}
		if err := c.removeUnboundRunNamespaceStage(); err != nil {
			return fmt.Errorf("remove unbound run namespace stage: %w", err)
		}
	case catalogRunStatePreparedRun:
		state, err = c.rollbackPreparedRunNamespace(state)
		if err != nil {
			return err
		}
	case catalogRunStatePreparedFull:
		state, err = c.completePreparedRunNamespace(state, history, historyStat)
		if err != nil {
			return err
		}
	default:
		return ErrIntegrity
	}
	if state.authority.State != catalogRunStateEstablished || len(state.tail) != 0 {
		return ErrIntegrity
	}
	return nil
}

func (c *Catalog) bootstrapCatalogRunAuthority() error {
	if _, found, err := getCatalogRunAuthority(c.rootFD); err != nil || found {
		if err != nil {
			return err
		}
		return ErrIntegrity
	}
	file, stat, data, err := c.openRunGenerationHistory()
	if err != nil {
		return err
	}
	defer file.Close()
	records := make(map[string]runNamespaceGeneration)
	prior, offset, sequence := "", 0, 1
	for offset < len(data) {
		relativeEnd := bytes.IndexByte(data[offset:], '\n')
		if relativeEnd < 1 || relativeEnd > maxRunGenerationRecord || sequence > MaxRuns {
			return ErrIntegrity
		}
		line := data[offset : offset+relativeEnd]
		record, legacy, parseErr := parseRunNamespaceGeneration(line, sequence, prior)
		if parseErr != nil || !legacy {
			return ErrIntegrity
		}
		if _, duplicate := records[record.component]; duplicate {
			return ErrIntegrity
		}
		records[record.component] = record
		prior = catalogRunRecordDigest(prior, line)
		offset += relativeEnd + 1
		sequence++
	}
	if int64(len(data)) != stat.Size || len(records) > MaxRuns || c.validateRunNamespaceNames(records) != nil {
		return ErrIntegrity
	}
	_, authorityData, err := makeCatalogRunAuthority(c, catalogRunStateEstablished, len(records), prior, nil)
	if err != nil || setCatalogRunAuthority(c.rootFD, authorityData, catalogXattrCreate) != nil || syscall.Fsync(c.rootFD) != nil {
		return ErrIntegrity
	}
	return verifyCatalogRunAuthorityData(c.rootFD, authorityData)
}

func (c *Catalog) loadRunNamespaceGenerationState() (catalogRunGenerationState, *os.File, syscall.Stat_t, error) {
	if c == nil || c.verifyNamespaceAuthority() != nil {
		return catalogRunGenerationState{}, nil, syscall.Stat_t{}, ErrIntegrity
	}
	authorityData, found, err := getCatalogRunAuthority(c.rootFD)
	if err != nil || !found {
		return catalogRunGenerationState{}, nil, syscall.Stat_t{}, ErrIntegrity
	}
	authority, err := decodeCatalogRunAuthority(authorityData, c)
	if err != nil {
		return catalogRunGenerationState{}, nil, syscall.Stat_t{}, err
	}
	history, historyStat, data, err := c.openRunGenerationHistory()
	if err != nil {
		return catalogRunGenerationState{}, nil, syscall.Stat_t{}, err
	}
	state := catalogRunGenerationState{authority: authority, authorityData: append([]byte(nil), authorityData...),
		records: make(map[string]runNamespaceGeneration, authority.IssuanceCount)}
	offset, prior := 0, ""
	for sequence := 1; sequence <= authority.IssuanceCount; sequence++ {
		relativeEnd := bytes.IndexByte(data[offset:], '\n')
		if relativeEnd < 1 || relativeEnd > maxRunGenerationRecord {
			_ = history.Close()
			return catalogRunGenerationState{}, nil, syscall.Stat_t{}, ErrIntegrity
		}
		line := data[offset : offset+relativeEnd]
		record, _, parseErr := parseRunNamespaceGeneration(line, sequence, prior)
		if parseErr != nil {
			_ = history.Close()
			return catalogRunGenerationState{}, nil, syscall.Stat_t{}, parseErr
		}
		if _, duplicate := state.records[record.component]; duplicate {
			_ = history.Close()
			return catalogRunGenerationState{}, nil, syscall.Stat_t{}, ErrIntegrity
		}
		state.records[record.component] = record
		prior = catalogRunRecordDigest(prior, line)
		offset += relativeEnd + 1
	}
	if prior != authority.IssuanceFinalRecordSHA256 {
		_ = history.Close()
		return catalogRunGenerationState{}, nil, syscall.Stat_t{}, ErrIntegrity
	}
	state.committedBytes, state.lastDigest = int64(offset), prior
	state.tail = append([]byte(nil), data[offset:]...)
	return state, history, historyStat, nil
}

func (c *Catalog) openRunGenerationHistory() (*os.File, syscall.Stat_t, []byte, error) {
	fd, err := syscall.Openat(c.catalog.fd, runGenerationsName, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, syscall.Stat_t{}, nil, ErrIntegrity
	}
	file := os.NewFile(uintptr(fd), runGenerationsName)
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || !validProtectedFileStat(stat, 1) || stat.Size < 0 || stat.Size > int64(maxRunGenerationsBytes) ||
		uint64(stat.Dev) != c.runGenerations.dev || stat.Ino != c.runGenerations.ino ||
		verifyNamedCatalogObject(c.catalog.fd, runGenerationsName, fd, c.runGenerations.dev, c.runGenerations.ino, false) != nil {
		_ = file.Close()
		return nil, syscall.Stat_t{}, nil, ErrIntegrity
	}
	data, readErr := io.ReadAll(io.LimitReader(file, int64(maxRunGenerationsBytes)+1))
	if readErr != nil || len(data) > maxRunGenerationsBytes || int64(len(data)) != stat.Size {
		_ = file.Close()
		return nil, syscall.Stat_t{}, nil, ErrIntegrity
	}
	return file, stat, data, nil
}

func (c *Catalog) rollbackPreparedRunNamespace(state catalogRunGenerationState) (catalogRunGenerationState, error) {
	prepared := state.authority.PreparedRunGeneration
	if prepared == nil || state.authority.State != catalogRunStatePreparedRun || len(state.tail) != 0 {
		return catalogRunGenerationState{}, ErrIntegrity
	}
	if found, err := catalogDirectoryExists(c.runs.fd, prepared.RunComponent); err != nil || found {
		return catalogRunGenerationState{}, ErrIntegrity
	}
	stageFD, found, err := c.openExactRunNamespaceDirectory(runNamespacePreparedName, *prepared)
	if err != nil {
		return catalogRunGenerationState{}, err
	}
	if found {
		if err := cleanupPartiallyPreparedRunNamespace(stageFD); err != nil {
			_ = syscall.Close(stageFD)
			return catalogRunGenerationState{}, err
		}
		if err := syscall.Close(stageFD); err != nil || removeCatalogDirectoryAt(c.runs.fd, runNamespacePreparedName) != nil || syscall.Fsync(c.runs.fd) != nil {
			return catalogRunGenerationState{}, ErrIntegrity
		}
	}
	_, establishedData, err := makeCatalogRunAuthority(c, catalogRunStateEstablished,
		state.authority.IssuanceCount, state.lastDigest, nil)
	if err != nil || c.replaceCatalogRunAuthority(state.authorityData, establishedData, "") != nil {
		return catalogRunGenerationState{}, ErrIntegrity
	}
	established, err := decodeCatalogRunAuthority(establishedData, c)
	if err != nil {
		return catalogRunGenerationState{}, err
	}
	state.authority, state.authorityData, state.tail = established, establishedData, nil
	return state, nil
}

func (c *Catalog) completePreparedRunNamespace(state catalogRunGenerationState, history *os.File, historyStat syscall.Stat_t) (catalogRunGenerationState, error) {
	prepared := state.authority.PreparedRunGeneration
	if prepared == nil || state.authority.State != catalogRunStatePreparedFull || validateRunNamespaceGeneration(*prepared) != nil ||
		prepared.Sequence != state.authority.IssuanceCount+1 || prepared.PriorRecordSHA256 != state.lastDigest {
		return catalogRunGenerationState{}, ErrIntegrity
	}
	if _, duplicate := state.records[prepared.RunComponent]; duplicate {
		return catalogRunGenerationState{}, ErrIntegrity
	}
	stageFD, stageFound, err := c.openExactRunNamespaceDirectory(runNamespacePreparedName, *prepared)
	if err != nil {
		return catalogRunGenerationState{}, err
	}
	if stageFound {
		defer syscall.Close(stageFD)
	}
	finalFD, finalFound, err := c.openExactRunNamespaceDirectory(prepared.RunComponent, *prepared)
	if err != nil {
		return catalogRunGenerationState{}, err
	}
	if finalFound {
		defer syscall.Close(finalFD)
	}
	if stageFound == finalFound {
		return catalogRunGenerationState{}, ErrIntegrity
	}
	boundFD := finalFD
	if stageFound {
		if err := verifyPreparedRunNamespace(stageFD, *prepared); err != nil ||
			syscall.Renameat(c.runs.fd, runNamespacePreparedName, c.runs.fd, prepared.RunComponent) != nil ||
			verifyNamedCatalogObject(c.runs.fd, prepared.RunComponent, stageFD, prepared.RunDevice, prepared.RunInode, true) != nil ||
			syscall.Fsync(c.runs.fd) != nil {
			return catalogRunGenerationState{}, ErrIntegrity
		}
		boundFD = stageFD
	}
	if err := verifyPreparedRunNamespace(boundFD, *prepared); err != nil {
		return catalogRunGenerationState{}, err
	}
	line, err := marshalRunNamespaceGeneration(*prepared)
	if err != nil {
		return catalogRunGenerationState{}, err
	}
	line = append(line, '\n')
	if len(state.tail) > len(line) || !bytes.Equal(state.tail, line[:len(state.tail)]) {
		return catalogRunGenerationState{}, ErrIntegrity
	}
	if !bytes.Equal(state.tail, line) {
		if history.Truncate(state.committedBytes) != nil {
			return catalogRunGenerationState{}, ErrIntegrity
		}
		if _, err := history.Seek(state.committedBytes, io.SeekStart); err != nil || writeCatalogBytes(history, line) != nil {
			return catalogRunGenerationState{}, ErrIntegrity
		}
	}
	if history.Sync() != nil {
		return catalogRunGenerationState{}, ErrIntegrity
	}
	var appended syscall.Stat_t
	if syscall.Fstat(int(history.Fd()), &appended) != nil || appended.Size != state.committedBytes+int64(len(line)) ||
		appended.Dev != historyStat.Dev || appended.Ino != historyStat.Ino ||
		verifyNamedCatalogObject(c.catalog.fd, runGenerationsName, int(history.Fd()), c.runGenerations.dev, c.runGenerations.ino, false) != nil {
		return catalogRunGenerationState{}, ErrIntegrity
	}
	nextDigest := catalogRunRecordDigest(state.lastDigest, line[:len(line)-1])
	_, establishedData, err := makeCatalogRunAuthority(c, catalogRunStateEstablished, prepared.Sequence, nextDigest, nil)
	if err != nil || c.replaceCatalogRunAuthority(state.authorityData, establishedData, "") != nil {
		return catalogRunGenerationState{}, ErrIntegrity
	}
	record, _, err := parseRunNamespaceGeneration(line[:len(line)-1], prepared.Sequence, state.lastDigest)
	if err != nil {
		return catalogRunGenerationState{}, err
	}
	state.records[record.component] = record
	established, err := decodeCatalogRunAuthority(establishedData, c)
	if err != nil {
		return catalogRunGenerationState{}, err
	}
	state.authority, state.authorityData = established, establishedData
	state.committedBytes += int64(len(line))
	state.lastDigest, state.tail = nextDigest, nil
	return state, nil
}

func (c *Catalog) validateRunNamespaceNames(records map[string]runNamespaceGeneration) error {
	names, err := directoryNamesRaw(c.runs.fd)
	if err != nil || len(names) != len(records) {
		return ErrIntegrity
	}
	for _, name := range names {
		if !validStorageComponent(name) {
			return ErrIntegrity
		}
		if _, found := records[name]; !found {
			return ErrIntegrity
		}
	}
	return nil
}

func (c *Catalog) removeUnboundRunNamespaceStage() error {
	fd, found, err := openCatalogDirectoryIfPresent(c.runs.fd, runNamespacePreparedName)
	if err != nil || !found {
		return err
	}
	defer syscall.Close(fd)
	names, err := directoryNamesRaw(fd)
	if err != nil || len(names) != 0 {
		return ErrIntegrity
	}
	if err := removeCatalogDirectoryAt(c.runs.fd, runNamespacePreparedName); err != nil || syscall.Fsync(c.runs.fd) != nil {
		return ErrIntegrity
	}
	return nil
}

func (c *Catalog) openExactRunNamespaceDirectory(name string, generation runNamespaceGenerationV2) (int, bool, error) {
	fd, found, err := openCatalogDirectoryIfPresent(c.runs.fd, name)
	if err != nil || !found {
		return fd, found, err
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || uint64(stat.Dev) != generation.RunDevice || stat.Ino != generation.RunInode {
		_ = syscall.Close(fd)
		return -1, false, ErrIntegrity
	}
	return fd, true, nil
}

func openCatalogDirectoryIfPresent(parent int, name string) (int, bool, error) {
	fd, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err == syscall.ENOENT {
		return -1, false, nil
	}
	if err != nil || validateDirectoryFD(fd, true) != nil {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return -1, false, ErrIntegrity
	}
	return fd, true, nil
}

func catalogDirectoryExists(parent int, name string) (bool, error) {
	fd, found, err := openCatalogDirectoryIfPresent(parent, name)
	if found {
		_ = syscall.Close(fd)
	}
	return found, err
}

func cleanupPartiallyPreparedRunNamespace(runFD int) error {
	names, err := directoryNamesRaw(runFD)
	if err != nil || len(names) > 2 {
		return ErrIntegrity
	}
	for _, name := range names {
		switch name {
		case runNamespaceIdentityName:
			fd, openErr := syscall.Openat(runFD, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			var stat syscall.Stat_t
			if openErr != nil || syscall.Fstat(fd, &stat) != nil || !validProtectedFileStat(stat, 1) || stat.Size < 0 || stat.Size > MaxRegistrationSize {
				if fd >= 0 {
					_ = syscall.Close(fd)
				}
				return ErrIntegrity
			}
			_ = syscall.Close(fd)
			if syscall.Unlinkat(runFD, name) != nil {
				return ErrIntegrity
			}
		case "attempts":
			attemptsFD, found, openErr := openCatalogDirectoryIfPresent(runFD, name)
			if openErr != nil || !found {
				return ErrIntegrity
			}
			attemptNames, readErr := directoryNamesRaw(attemptsFD)
			closeErr := syscall.Close(attemptsFD)
			if readErr != nil || closeErr != nil || len(attemptNames) != 0 || removeCatalogDirectoryAt(runFD, name) != nil {
				return ErrIntegrity
			}
		default:
			return ErrIntegrity
		}
	}
	if syscall.Fsync(runFD) != nil {
		return ErrIntegrity
	}
	remaining, err := directoryNamesRaw(runFD)
	if err != nil || len(remaining) != 0 {
		return ErrIntegrity
	}
	return nil
}

func verifyPreparedRunNamespace(runFD int, generation runNamespaceGenerationV2) error {
	if err := validateRunNamespaceGeneration(generation); err != nil {
		return err
	}
	names, err := directoryNamesRaw(runFD)
	if err != nil || len(names) != 2 || !containsCatalogName(names, "attempts") || !containsCatalogName(names, runNamespaceIdentityName) {
		return ErrIntegrity
	}
	attemptsFD, err := openDirectoryAt(runFD, "attempts", false)
	if err != nil {
		return ErrIntegrity
	}
	if err := verifyNamedCatalogObject(runFD, "attempts", attemptsFD, generation.AttemptsDevice, generation.AttemptsInode, true); err != nil {
		_ = syscall.Close(attemptsFD)
		return err
	}
	attemptNames, readErr := directoryNamesRaw(attemptsFD)
	closeErr := syscall.Close(attemptsFD)
	if readErr != nil || closeErr != nil || len(attemptNames) != 0 {
		return ErrIntegrity
	}
	identityData, identityStat, err := readProtectedAtGeneration(runFD, runNamespaceIdentityName, MaxRegistrationSize)
	if err != nil || uint64(identityStat.Dev) != generation.IdentityDevice || identityStat.Ino != generation.IdentityInode ||
		sha256Bytes(identityData) != generation.IdentitySHA256 {
		return ErrIntegrity
	}
	expected, err := identityBytesForRunGeneration(generation)
	if err != nil || !bytes.Equal(identityData, expected) {
		return ErrIntegrity
	}
	return nil
}

func containsCatalogName(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}

func writePreparedRunIdentity(runFD int, data []byte) (syscall.Stat_t, error) {
	fd, err := syscall.Openat(runFD, runNamespaceIdentityName,
		syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return syscall.Stat_t{}, ErrIntegrity
	}
	file := os.NewFile(uintptr(fd), runNamespaceIdentityName)
	catalogRunIssuanceBoundaryHook("identity-created")
	split := len(data) / 2
	if split == 0 {
		split = 1
	}
	if err := writeCatalogBytes(file, data[:split]); err != nil {
		_ = file.Close()
		return syscall.Stat_t{}, err
	}
	catalogRunIssuanceBoundaryHook("identity-partial")
	if err := writeCatalogBytes(file, data[split:]); err != nil {
		_ = file.Close()
		return syscall.Stat_t{}, err
	}
	catalogRunIssuanceBoundaryHook("identity-complete-visible")
	if file.Sync() != nil {
		_ = file.Close()
		return syscall.Stat_t{}, ErrIntegrity
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || !validProtectedFileStat(stat, 1) || stat.Size != int64(len(data)) {
		_ = file.Close()
		return syscall.Stat_t{}, ErrIntegrity
	}
	if file.Close() != nil || syscall.Fsync(runFD) != nil {
		return syscall.Stat_t{}, ErrIntegrity
	}
	return stat, nil
}

func writeCatalogBytes(file *os.File, data []byte) error {
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil || written <= 0 {
			return ErrIntegrity
		}
		data = data[written:]
	}
	return nil
}

func parseRunNamespaceGeneration(line []byte, sequence int, prior string) (runNamespaceGeneration, bool, error) {
	if len(line) == 0 || len(line) > maxRunGenerationRecord {
		return runNamespaceGeneration{}, false, ErrIntegrity
	}
	var header struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(line, &header) != nil {
		return runNamespaceGeneration{}, false, ErrIntegrity
	}
	switch header.Kind {
	case "RuntimeCatalogRunNamespaceIdentityV1":
		var identity runNamespaceIdentityV1
		if json.Unmarshal(line, &identity) != nil {
			return runNamespaceGeneration{}, false, ErrIntegrity
		}
		canonical, err := json.Marshal(identity)
		if err != nil || !bytes.Equal(canonical, line) || validateRunNamespaceIdentity(identity) != nil {
			return runNamespaceGeneration{}, false, ErrIntegrity
		}
		return runNamespaceGeneration{component: identity.RunComponent, runDev: identity.RunDevice, runIno: identity.RunInode,
			attemptsDev: identity.AttemptsDevice, attemptsIno: identity.AttemptsInode,
			identityBytes: append(append([]byte(nil), line...), '\n')}, true, nil
	case "RuntimeCatalogRunNamespaceGenerationV2":
		var generation runNamespaceGenerationV2
		if json.Unmarshal(line, &generation) != nil || validateRunNamespaceGeneration(generation) != nil ||
			generation.Sequence != sequence || generation.PriorRecordSHA256 != prior {
			return runNamespaceGeneration{}, false, ErrIntegrity
		}
		canonical, err := json.Marshal(generation)
		if err != nil || !bytes.Equal(canonical, line) {
			return runNamespaceGeneration{}, false, ErrIntegrity
		}
		identityData, err := identityBytesForRunGeneration(generation)
		if err != nil || sha256Bytes(identityData) != generation.IdentitySHA256 {
			return runNamespaceGeneration{}, false, ErrIntegrity
		}
		return runNamespaceGeneration{component: generation.RunComponent, runDev: generation.RunDevice, runIno: generation.RunInode,
			attemptsDev: generation.AttemptsDevice, attemptsIno: generation.AttemptsInode,
			identityDev: generation.IdentityDevice, identityIno: generation.IdentityInode, identityBytes: identityData}, false, nil
	default:
		return runNamespaceGeneration{}, false, ErrIntegrity
	}
}

func validateRunNamespaceIdentity(identity runNamespaceIdentityV1) error {
	if identity.Kind != "RuntimeCatalogRunNamespaceIdentityV1" || identity.SchemaVersion != 1 ||
		!validStorageComponent(identity.RunComponent) || identity.RunInode == 0 || identity.AttemptsInode == 0 {
		return ErrIntegrity
	}
	return nil
}

func validateRunNamespaceGeneration(generation runNamespaceGenerationV2) error {
	if generation.Kind != "RuntimeCatalogRunNamespaceGenerationV2" || generation.SchemaVersion != 2 || generation.Sequence <= 0 ||
		!validStorageComponent(generation.RunComponent) || generation.RunInode == 0 || generation.AttemptsInode == 0 ||
		generation.IdentityInode == 0 || !isSHA256(generation.IdentitySHA256) ||
		(generation.Sequence == 1 && generation.PriorRecordSHA256 != "") ||
		(generation.Sequence > 1 && !isSHA256(generation.PriorRecordSHA256)) {
		return ErrIntegrity
	}
	return nil
}

func identityBytesForRunGeneration(generation runNamespaceGenerationV2) ([]byte, error) {
	identity := runNamespaceIdentityV1{
		Kind: "RuntimeCatalogRunNamespaceIdentityV1", SchemaVersion: 1, RunComponent: generation.RunComponent,
		RunDevice: generation.RunDevice, RunInode: generation.RunInode,
		AttemptsDevice: generation.AttemptsDevice, AttemptsInode: generation.AttemptsInode,
	}
	return canonicalJSON(identity)
}

func marshalRunNamespaceGeneration(generation runNamespaceGenerationV2) ([]byte, error) {
	if err := validateRunNamespaceGeneration(generation); err != nil {
		return nil, err
	}
	data, err := json.Marshal(generation)
	if err != nil || len(data) == 0 || len(data) > maxRunGenerationRecord {
		return nil, ErrIntegrity
	}
	return data, nil
}

func catalogRunRecordDigest(prior string, line []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(prior))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(line)
	return hex.EncodeToString(hash.Sum(nil))
}

func makeCatalogRunAuthority(c *Catalog, state string, count int, finalDigest string,
	prepared *runNamespaceGenerationV2) (catalogRunAuthorityV1, []byte, error) {
	if c == nil {
		return catalogRunAuthorityV1{}, nil, ErrIntegrity
	}
	authority := catalogRunAuthorityV1{
		Kind: catalogRunAuthorityKind, SchemaVersion: 1, State: state,
		RootDevice: c.rootDev, RootInode: c.rootIno,
		HistoryDevice: c.runGenerations.dev, HistoryInode: c.runGenerations.ino,
		IssuanceCount: count, IssuanceFinalRecordSHA256: finalDigest, PreparedRunGeneration: prepared,
	}
	data, err := json.Marshal(authority)
	if err != nil || len(data) == 0 || len(data) > maxCatalogRunAuthority {
		return catalogRunAuthorityV1{}, nil, ErrIntegrity
	}
	if _, err := decodeCatalogRunAuthority(data, c); err != nil {
		return catalogRunAuthorityV1{}, nil, err
	}
	return authority, data, nil
}

func decodeCatalogRunAuthority(data []byte, c *Catalog) (catalogRunAuthorityV1, error) {
	if len(data) == 0 || len(data) > maxCatalogRunAuthority || c == nil {
		return catalogRunAuthorityV1{}, ErrIntegrity
	}
	var authority catalogRunAuthorityV1
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&authority) != nil {
		return catalogRunAuthorityV1{}, ErrIntegrity
	}
	canonical, err := json.Marshal(authority)
	if err != nil || !bytes.Equal(canonical, data) || authority.Kind != catalogRunAuthorityKind || authority.SchemaVersion != 1 ||
		authority.RootDevice != c.rootDev || authority.RootInode != c.rootIno ||
		authority.HistoryDevice != c.runGenerations.dev || authority.HistoryInode != c.runGenerations.ino ||
		authority.IssuanceCount < 0 || authority.IssuanceCount > MaxRuns ||
		(authority.IssuanceCount == 0 && authority.IssuanceFinalRecordSHA256 != "") ||
		(authority.IssuanceCount > 0 && !isSHA256(authority.IssuanceFinalRecordSHA256)) {
		return catalogRunAuthorityV1{}, ErrIntegrity
	}
	switch authority.State {
	case catalogRunStateEstablished:
		if authority.PreparedRunGeneration != nil {
			return catalogRunAuthorityV1{}, ErrIntegrity
		}
	case catalogRunStatePreparedRun:
		prepared := authority.PreparedRunGeneration
		if prepared == nil || prepared.Kind != "RuntimeCatalogRunNamespaceGenerationV2" || prepared.SchemaVersion != 2 ||
			prepared.Sequence != authority.IssuanceCount+1 || prepared.PriorRecordSHA256 != authority.IssuanceFinalRecordSHA256 ||
			!validStorageComponent(prepared.RunComponent) || prepared.RunInode == 0 || prepared.AttemptsDevice != 0 || prepared.AttemptsInode != 0 ||
			prepared.IdentityDevice != 0 || prepared.IdentityInode != 0 || prepared.IdentitySHA256 != "" {
			return catalogRunAuthorityV1{}, ErrIntegrity
		}
	case catalogRunStatePreparedFull:
		prepared := authority.PreparedRunGeneration
		if prepared == nil || validateRunNamespaceGeneration(*prepared) != nil || prepared.Sequence != authority.IssuanceCount+1 ||
			prepared.PriorRecordSHA256 != authority.IssuanceFinalRecordSHA256 {
			return catalogRunAuthorityV1{}, ErrIntegrity
		}
	default:
		return catalogRunAuthorityV1{}, ErrIntegrity
	}
	return authority, nil
}

func (c *Catalog) replaceCatalogRunAuthority(expected, next []byte, boundary string) error {
	if err := c.verifyNamespaceAuthority(); err != nil || verifyCatalogRunAuthorityData(c.rootFD, expected) != nil {
		return ErrIntegrity
	}
	if setCatalogRunAuthority(c.rootFD, next, catalogXattrReplace) != nil {
		return ErrIntegrity
	}
	if boundary != "" {
		catalogRunIssuanceBoundaryHook(boundary + "-visible")
	}
	if syscall.Fsync(c.rootFD) != nil {
		return ErrIntegrity
	}
	return verifyCatalogRunAuthorityData(c.rootFD, next)
}

func getCatalogRunAuthority(fd int) ([]byte, bool, error) {
	name, err := syscall.BytePtrFromString(catalogRunAuthorityXattr)
	if err != nil {
		return nil, false, ErrIntegrity
	}
	buffer := make([]byte, maxCatalogRunAuthority+1)
	size, _, errno := syscall.Syscall6(syscall.SYS_FGETXATTR, uintptr(fd), uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, 0)
	if errno == syscall.ENODATA {
		return nil, false, nil
	}
	if errno != 0 || size == 0 || size > maxCatalogRunAuthority {
		return nil, false, ErrIntegrity
	}
	return append([]byte(nil), buffer[:int(size)]...), true, nil
}

func setCatalogRunAuthority(fd int, data []byte, flags int) error {
	if len(data) == 0 || len(data) > maxCatalogRunAuthority {
		return ErrIntegrity
	}
	name, err := syscall.BytePtrFromString(catalogRunAuthorityXattr)
	if err != nil {
		return ErrIntegrity
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_FSETXATTR, uintptr(fd), uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(flags), 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func verifyCatalogRunAuthorityData(fd int, expected []byte) error {
	observed, found, err := getCatalogRunAuthority(fd)
	if err != nil || !found || !bytes.Equal(observed, expected) {
		return ErrIntegrity
	}
	return nil
}

func removeCatalogDirectoryAt(parent int, name string) error {
	pointer, err := syscall.BytePtrFromString(name)
	if err != nil {
		return ErrIntegrity
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_UNLINKAT, uintptr(parent), uintptr(unsafe.Pointer(pointer)), uintptr(0x200), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func (c *Catalog) verifyRootPath() error {
	if c == nil || c.rootFD < 0 {
		return ErrIntegrity
	}
	fd, err := openAbsoluteDirectory(c.root, false)
	if err != nil {
		return fmt.Errorf("%w: service root was replaced", ErrIntegrity)
	}
	defer syscall.Close(fd)
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || uint64(stat.Dev) != c.rootDev || stat.Ino != c.rootIno {
		return fmt.Errorf("%w: service root was replaced", ErrIntegrity)
	}
	return validateDirectoryFD(fd, true)
}

func (c *Catalog) verifyNamespaceAuthority() error {
	if err := c.verifyRootPath(); err != nil {
		return err
	}
	if len(c.rootAuthorityData) == 0 || verifyCatalogRootGenerationData(c.rootFD, c.rootAuthorityData) != nil {
		return fmt.Errorf("%w: runtime catalog generation authority changed", ErrIntegrity)
	}
	rootAuthority, err := decodeCatalogRootGeneration(c.rootAuthorityData)
	if err != nil {
		return ErrIntegrity
	}
	for _, check := range []struct {
		parent int
		pin    *catalogNamespacePin
	}{
		{c.rootFD, &c.catalog}, {c.catalog.fd, &c.runs}, {c.rootFD, &c.active},
		{c.rootFD, &c.config}, {c.catalog.fd, &c.lock}, {c.catalog.fd, &c.runGenerations},
	} {
		if err := verifyNamedCatalogPin(check.parent, check.pin); err != nil {
			return err
		}
	}
	runAuthorityData, found, err := getCatalogRunAuthority(c.rootFD)
	if err != nil || (rootAuthority.state == catalogGenerationReadyV2 && !found) {
		return fmt.Errorf("%w: runtime catalog run authority is unavailable", ErrIntegrity)
	}
	if found {
		if _, err := decodeCatalogRunAuthority(runAuthorityData, c); err != nil {
			return err
		}
	}
	return nil
}

func (c *Catalog) duplicatePinnedDirectory(pin *catalogNamespacePin) (int, error) {
	if err := c.verifyNamespaceAuthority(); err != nil {
		return -1, err
	}
	if pin == nil || !pin.directory {
		return -1, ErrIntegrity
	}
	fd, err := syscall.Dup(pin.fd)
	if err != nil {
		return -1, ErrIntegrity
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || uint64(stat.Dev) != pin.dev || stat.Ino != pin.ino || validateDirectoryFD(fd, true) != nil {
		syscall.Close(fd)
		return -1, ErrIntegrity
	}
	return fd, nil
}

func (c *Catalog) withLock(fn func() error) (resultErr error) {
	fd, err := c.acquireLock()
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, c.releaseLock(fd))
	}()
	return fn()
}

func (c *Catalog) acquireLock() (int, error) {
	fd, err := c.acquireLockRaw()
	if err != nil {
		return -1, err
	}
	if err := c.reconcileRunNamespaceIssuance(false); err != nil {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = syscall.Close(fd)
		return -1, err
	}
	return fd, nil
}

func (c *Catalog) acquireLockRaw() (int, error) {
	if err := c.verifyNamespaceAuthority(); err != nil {
		return -1, err
	}
	fd, err := syscall.Openat(c.catalog.fd, ".lock", syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return -1, ErrIntegrity
	}
	if err := c.verifyLockFD(fd); err != nil {
		syscall.Close(fd)
		return -1, err
	}
	deadline := time.Now().Add(LockTimeout)
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			if err := errors.Join(c.verifyLockFD(fd), c.verifyNamespaceAuthority()); err != nil {
				_ = syscall.Flock(fd, syscall.LOCK_UN)
				_ = syscall.Close(fd)
				return -1, err
			}
			return fd, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			syscall.Close(fd)
			return -1, ErrIntegrity
		}
		if !time.Now().Before(deadline) {
			syscall.Close(fd)
			return -1, ErrBusy
		}
		remaining := time.Until(deadline)
		if remaining > 10*time.Millisecond {
			remaining = 10 * time.Millisecond
		}
		if remaining > 0 {
			time.Sleep(remaining)
		}
	}
}

func (c *Catalog) verifyLockFD(fd int) error {
	if c == nil || c.lock.fd < 0 || fd < 0 {
		return ErrIntegrity
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || !validProtectedFileStat(stat, 1) || uint64(stat.Dev) != c.lock.dev || stat.Ino != c.lock.ino {
		return ErrIntegrity
	}
	return nil
}

func (c *Catalog) releaseLock(fd int) error {
	if fd < 0 {
		return ErrIntegrity
	}
	verifyErr := errors.Join(c.verifyLockFD(fd), c.verifyNamespaceAuthority())
	return errors.Join(verifyErr, syscall.Flock(fd, syscall.LOCK_UN), syscall.Close(fd))
}

func lockCatalogRootGeneration(fd int) error {
	deadline := time.Now().Add(LockTimeout)
	for {
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return ErrIntegrity
		}
		if !time.Now().Before(deadline) {
			return ErrBusy
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func requireCatalogGenerationAbsent(rootFD int) error {
	for _, name := range []string{"catalog", "active"} {
		fd, err := syscall.Openat(rootFD, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err == syscall.ENOENT {
			continue
		}
		if err == nil {
			_ = syscall.Close(fd)
		}
		return ErrIntegrity
	}
	return nil
}

func openCatalogDirectoryPin(parent int, name string, create bool) (catalogNamespacePin, error) {
	fd, err := openDirectoryAt(parent, name, create)
	if err != nil {
		return catalogNamespacePin{fd: -1}, err
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || validateDirectoryFD(fd, true) != nil {
		syscall.Close(fd)
		return catalogNamespacePin{fd: -1}, ErrIntegrity
	}
	return catalogNamespacePin{name: name, fd: fd, dev: uint64(stat.Dev), ino: stat.Ino, directory: true}, nil
}

func openCatalogLockPin(parent int, create bool) (catalogNamespacePin, error) {
	return openCatalogFilePin(parent, ".lock", create)
}

func openCatalogFilePin(parent int, name string, create bool) (catalogNamespacePin, error) {
	flags := syscall.O_RDWR | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	if create {
		flags |= syscall.O_CREAT
	}
	fd, err := syscall.Openat(parent, name, flags, 0o600)
	if err != nil {
		return catalogNamespacePin{fd: -1}, ErrIntegrity
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || !validProtectedFileStat(stat, 1) {
		syscall.Close(fd)
		return catalogNamespacePin{fd: -1}, ErrIntegrity
	}
	if create && syscall.Fsync(parent) != nil {
		syscall.Close(fd)
		return catalogNamespacePin{fd: -1}, ErrIntegrity
	}
	return catalogNamespacePin{name: name, fd: fd, dev: uint64(stat.Dev), ino: stat.Ino}, nil
}

func (c *Catalog) expectedRootGeneration(state byte) (catalogRootGeneration, []byte, error) {
	if c == nil || (state != catalogGenerationReady && state != catalogGenerationReadyV2) {
		return catalogRootGeneration{}, nil, ErrIntegrity
	}
	objects := [6]catalogObjectIdentity{
		catalogGenerationObject(&c.catalog), catalogGenerationObject(&c.runs),
		catalogGenerationObject(&c.active), catalogGenerationObject(&c.config),
		catalogGenerationObject(&c.lock), catalogGenerationObject(&c.runGenerations),
	}
	authority := catalogRootGeneration{state: state, rootDevice: c.rootDev, rootInode: c.rootIno, objects: objects}
	data := encodeCatalogRootGeneration(authority)
	return authority, data, nil
}

func catalogGenerationObject(pin *catalogNamespacePin) catalogObjectIdentity {
	return catalogObjectIdentity{device: pin.dev, inode: pin.ino}
}

func createCatalogRootGeneration(fd int, rootDev, rootIno uint64) (catalogRootGeneration, []byte, error) {
	authority := catalogRootGeneration{state: catalogGenerationInit, rootDevice: rootDev, rootInode: rootIno}
	data := encodeCatalogRootGeneration(authority)
	if err := setCatalogRootXattr(fd, data, catalogXattrCreate); err != nil || syscall.Fsync(fd) != nil {
		return catalogRootGeneration{}, nil, ErrIntegrity
	}
	if err := verifyCatalogRootGenerationData(fd, data); err != nil {
		return catalogRootGeneration{}, nil, err
	}
	return authority, data, nil
}

func publishCatalogRootGeneration(fd int, authority catalogRootGeneration, data []byte) ([]byte, error) {
	canonical := encodeCatalogRootGeneration(authority)
	if (authority.state != catalogGenerationReady && authority.state != catalogGenerationReadyV2) || !bytes.Equal(canonical, data) {
		return nil, ErrIntegrity
	}
	if err := setCatalogRootXattr(fd, data, catalogXattrReplace); err != nil || syscall.Fsync(fd) != nil {
		return nil, ErrIntegrity
	}
	if err := verifyCatalogRootGenerationData(fd, data); err != nil {
		return nil, err
	}
	return append([]byte(nil), data...), nil
}

func readCatalogRootGeneration(fd int, rootDev, rootIno uint64) (catalogRootGeneration, []byte, bool, error) {
	data, found, err := getCatalogRootXattr(fd)
	if err != nil || !found {
		return catalogRootGeneration{}, nil, found, err
	}
	authority, decodeErr := decodeCatalogRootGeneration(data)
	if decodeErr != nil || authority.rootDevice != rootDev || authority.rootInode != rootIno {
		return catalogRootGeneration{}, nil, true, ErrIntegrity
	}
	return authority, append([]byte(nil), data...), true, nil
}

func encodeCatalogRootGeneration(authority catalogRootGeneration) []byte {
	size := 21
	if authority.state == catalogGenerationReady || authority.state == catalogGenerationReadyV2 {
		size = catalogGenerationMaxBytes
	}
	data := make([]byte, size)
	copy(data[:4], "RCG1")
	data[4] = authority.state
	binary.BigEndian.PutUint64(data[5:13], authority.rootDevice)
	binary.BigEndian.PutUint64(data[13:21], authority.rootInode)
	if authority.state == catalogGenerationReady || authority.state == catalogGenerationReadyV2 {
		offset := 21
		for _, object := range authority.objects {
			binary.BigEndian.PutUint64(data[offset:offset+8], object.device)
			binary.BigEndian.PutUint64(data[offset+8:offset+16], object.inode)
			offset += 16
		}
	}
	return data
}

func decodeCatalogRootGeneration(data []byte) (catalogRootGeneration, error) {
	if len(data) < 21 || string(data[:4]) != "RCG1" {
		return catalogRootGeneration{}, ErrIntegrity
	}
	authority := catalogRootGeneration{state: data[4], rootDevice: binary.BigEndian.Uint64(data[5:13]), rootInode: binary.BigEndian.Uint64(data[13:21])}
	switch authority.state {
	case catalogGenerationInit:
		if len(data) != 21 {
			return catalogRootGeneration{}, ErrIntegrity
		}
	case catalogGenerationReady, catalogGenerationReadyV2:
		if len(data) != catalogGenerationMaxBytes {
			return catalogRootGeneration{}, ErrIntegrity
		}
		offset := 21
		for index := range authority.objects {
			authority.objects[index] = catalogObjectIdentity{device: binary.BigEndian.Uint64(data[offset : offset+8]), inode: binary.BigEndian.Uint64(data[offset+8 : offset+16])}
			if authority.objects[index].inode == 0 {
				return catalogRootGeneration{}, ErrIntegrity
			}
			offset += 16
		}
	default:
		return catalogRootGeneration{}, ErrIntegrity
	}
	return authority, nil
}

func getCatalogRootXattr(fd int) ([]byte, bool, error) {
	name, err := syscall.BytePtrFromString(catalogRootAuthorityXattr)
	if err != nil {
		return nil, false, ErrIntegrity
	}
	buffer := make([]byte, catalogGenerationMaxBytes+1)
	size, _, errno := syscall.Syscall6(syscall.SYS_FGETXATTR, uintptr(fd), uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, 0)
	if errno == syscall.ENODATA {
		return nil, false, nil
	}
	if errno != 0 || size == 0 || size > catalogGenerationMaxBytes {
		return nil, false, ErrIntegrity
	}
	return append([]byte(nil), buffer[:int(size)]...), true, nil
}

func setCatalogRootXattr(fd int, data []byte, flags int) error {
	if len(data) == 0 || len(data) > catalogGenerationMaxBytes {
		return ErrIntegrity
	}
	name, err := syscall.BytePtrFromString(catalogRootAuthorityXattr)
	if err != nil {
		return ErrIntegrity
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_FSETXATTR, uintptr(fd), uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(flags), 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func verifyCatalogRootGenerationData(fd int, expected []byte) error {
	observed, found, err := getCatalogRootXattr(fd)
	if err != nil || !found || !bytes.Equal(observed, expected) {
		return ErrIntegrity
	}
	return nil
}

func verifyNamedCatalogPin(parent int, pin *catalogNamespacePin) error {
	if pin == nil {
		return ErrIntegrity
	}
	return verifyNamedCatalogObject(parent, pin.name, pin.fd, pin.dev, pin.ino, pin.directory)
}

func verifyNamedCatalogObject(parent int, name string, pinned int, dev, ino uint64, directory bool) error {
	if parent < 0 || pinned < 0 || name == "" || ino == 0 {
		return ErrIntegrity
	}
	var pinnedStat syscall.Stat_t
	if syscall.Fstat(pinned, &pinnedStat) != nil || uint64(pinnedStat.Dev) != dev || pinnedStat.Ino != ino {
		return ErrIntegrity
	}
	flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	if directory {
		flags |= syscall.O_DIRECTORY
		if validateDirectoryFD(pinned, true) != nil {
			return ErrIntegrity
		}
	} else {
		flags = syscall.O_RDWR | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
		if !validProtectedFileStat(pinnedStat, 1) {
			return ErrIntegrity
		}
	}
	opened, err := syscall.Openat(parent, name, flags, 0)
	if err != nil {
		return ErrIntegrity
	}
	defer syscall.Close(opened)
	var namedStat syscall.Stat_t
	if syscall.Fstat(opened, &namedStat) != nil || uint64(namedStat.Dev) != dev || namedStat.Ino != ino {
		return ErrIntegrity
	}
	if directory {
		return validateDirectoryFD(opened, true)
	}
	if !validProtectedFileStat(namedStat, 1) {
		return ErrIntegrity
	}
	return nil
}

func openAbsoluteDirectory(path string, create bool) (int, error) {
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, part := range parts {
		if part == "" {
			continue
		}
		next, openErr := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if openErr == syscall.ENOENT && create {
			if mkdirErr := syscall.Mkdirat(fd, part, 0o700); mkdirErr != nil && mkdirErr != syscall.EEXIST {
				syscall.Close(fd)
				return -1, mkdirErr
			}
			if syncErr := syscall.Fsync(fd); syncErr != nil {
				syscall.Close(fd)
				return -1, syncErr
			}
			next, openErr = syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		}
		syscall.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func validateRegisteredPath(path string, directory bool) error {
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, part := range parts {
		last := index == len(parts)-1
		flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
		if !last || directory {
			flags |= syscall.O_DIRECTORY
		}
		next, openErr := syscall.Openat(fd, part, flags, 0)
		syscall.Close(fd)
		if openErr != nil {
			return openErr
		}
		fd = next
	}
	defer syscall.Close(fd)
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return err
	}
	if directory && stat.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return ErrIntegrity
	}
	if !directory && stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return ErrIntegrity
	}
	return nil
}

func observeRegisteredLedgerGeneration(path string) (LedgerGenerationV1, error) {
	if !canonicalAbsolute(path) || path == string(filepath.Separator) {
		return LedgerGenerationV1{}, ErrIntegrity
	}
	parentPath, name := filepath.Dir(path), filepath.Base(path)
	parentFD, err := openAbsoluteDirectory(parentPath, false)
	if err != nil {
		return LedgerGenerationV1{}, err
	}
	defer syscall.Close(parentFD)
	var parentStat syscall.Stat_t
	if syscall.Fstat(parentFD, &parentStat) != nil || parentStat.Mode&syscall.S_IFMT != syscall.S_IFDIR || parentStat.Uid != uint32(os.Geteuid()) {
		return LedgerGenerationV1{}, ErrIntegrity
	}
	fileFD, err := syscall.Openat(parentFD, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return LedgerGenerationV1{}, err
	}
	defer syscall.Close(fileFD)
	var fileStat syscall.Stat_t
	if syscall.Fstat(fileFD, &fileStat) != nil || !validProtectedFileStat(fileStat, 1) {
		return LedgerGenerationV1{}, ErrIntegrity
	}
	generation := LedgerGenerationV1{
		Kind: "LedgerGenerationV1", ParentDevice: uint64(parentStat.Dev), ParentInode: parentStat.Ino,
		FileDevice: uint64(fileStat.Dev), FileInode: fileStat.Ino,
	}
	if err := verifyRegisteredLedgerGeneration(path, generation); err != nil {
		return LedgerGenerationV1{}, err
	}
	return generation, nil
}

func verifyRegisteredLedgerGeneration(path string, expected LedgerGenerationV1) error {
	if !expected.Valid() || !canonicalAbsolute(path) || path == string(filepath.Separator) {
		return ErrIntegrity
	}
	parentFD, err := openAbsoluteDirectory(filepath.Dir(path), false)
	if err != nil {
		return err
	}
	defer syscall.Close(parentFD)
	var parentStat syscall.Stat_t
	if syscall.Fstat(parentFD, &parentStat) != nil || parentStat.Mode&syscall.S_IFMT != syscall.S_IFDIR ||
		parentStat.Uid != uint32(os.Geteuid()) || uint64(parentStat.Dev) != expected.ParentDevice || parentStat.Ino != expected.ParentInode {
		return ErrIntegrity
	}
	fileFD, err := syscall.Openat(parentFD, filepath.Base(path), syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fileFD)
	var fileStat syscall.Stat_t
	if syscall.Fstat(fileFD, &fileStat) != nil || !validProtectedFileStat(fileStat, 1) ||
		uint64(fileStat.Dev) != expected.FileDevice || fileStat.Ino != expected.FileInode {
		return ErrIntegrity
	}
	return nil
}

func openDirectoryAt(parent int, name string, create bool) (int, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
		return -1, ErrIntegrity
	}
	fd, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err == syscall.ENOENT && create {
		if mkdirErr := syscall.Mkdirat(parent, name, 0o700); mkdirErr != nil && mkdirErr != syscall.EEXIST {
			return -1, ErrIntegrity
		}
		if syncErr := syscall.Fsync(parent); syncErr != nil {
			return -1, ErrIntegrity
		}
		fd, err = syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	}
	if err != nil {
		return -1, err
	}
	if err := validateDirectoryFD(fd, true); err != nil {
		syscall.Close(fd)
		return -1, err
	}
	return fd, nil
}

func validateDirectoryFD(fd int, exactMode bool) error {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFDIR || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("service directory ownership or type is unsafe")
	}
	if exactMode && stat.Mode&0o7777 != 0o700 {
		return errors.New("service directory mode must be 0700")
	}
	return nil
}

func validateFileFD(fd int) error {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || !validProtectedFileStat(stat, 1) {
		return fmt.Errorf("%w: unsafe catalog file", ErrIntegrity)
	}
	return nil
}

func validProtectedFileStat(stat syscall.Stat_t, links uint64) bool {
	return stat.Mode&syscall.S_IFMT == syscall.S_IFREG && stat.Uid == uint32(os.Geteuid()) && stat.Mode&0o7777 == 0o600 && stat.Nlink == links
}

func validReopenedProtectedFileStat(current, initial syscall.Stat_t) bool {
	return validProtectedFileStat(current, 1) && current.Dev == initial.Dev && current.Ino == initial.Ino
}

func directoryNamesRaw(fd int) ([]string, error) {
	duplicate, err := syscall.Dup(fd)
	if err != nil {
		return nil, err
	}
	if _, err := syscall.Seek(duplicate, 0, 0); err != nil {
		syscall.Close(duplicate)
		return nil, err
	}
	file := os.NewFile(uintptr(duplicate), "catalog-directory")
	defer file.Close()
	return file.Readdirnames(-1)
}

func directoryNames(fd int) ([]string, error) {
	if err := recoverTemporaryArtifacts(fd); err != nil {
		return nil, err
	}
	return directoryNamesRaw(fd)
}

func readProtectedAt(parent int, name string, max int) ([]byte, error) {
	data, _, err := readProtectedAtGeneration(parent, name, max)
	return data, err
}

func readProtectedAtGeneration(parent int, name string, max int) ([]byte, syscall.Stat_t, error) {
	if err := recoverTemporaryArtifacts(parent); err != nil {
		return nil, syscall.Stat_t{}, err
	}
	fd, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, syscall.Stat_t{}, err
	}
	if err := validateFileFD(fd); err != nil {
		syscall.Close(fd)
		return nil, syscall.Stat_t{}, err
	}
	var initial syscall.Stat_t
	if err := syscall.Fstat(fd, &initial); err != nil || !validProtectedFileStat(initial, 1) {
		syscall.Close(fd)
		return nil, syscall.Stat_t{}, ErrIntegrity
	}
	file := os.NewFile(uintptr(fd), "catalog-record")
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(max)+1))
	if err != nil || len(data) > max {
		return nil, syscall.Stat_t{}, ErrIntegrity
	}
	var afterRead syscall.Stat_t
	if err := syscall.Fstat(fd, &afterRead); err != nil || !validProtectedFileStat(afterRead, 1) || afterRead.Dev != initial.Dev || afterRead.Ino != initial.Ino {
		return nil, syscall.Stat_t{}, ErrIntegrity
	}
	reopened, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, syscall.Stat_t{}, ErrIntegrity
	}
	defer syscall.Close(reopened)
	var current syscall.Stat_t
	if err := syscall.Fstat(reopened, &current); err != nil || !validReopenedProtectedFileStat(current, initial) {
		return nil, syscall.Stat_t{}, ErrIntegrity
	}
	return data, initial, nil
}

func createOrByteVerify(parent int, name string, data []byte) error {
	existing, err := readProtectedAt(parent, name, MaxRegistrationSize)
	if err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := writeTemporary(parent, name, "create", data)
	if err != nil {
		return err
	}
	if err := linkAt(parent, temporary, parent, name); err != nil {
		if err == syscall.EEXIST {
			existing, readErr := readProtectedAt(parent, name, MaxRegistrationSize)
			cleanupErr := removeTemporary(parent, temporary)
			if readErr == nil && bytes.Equal(existing, data) && cleanupErr == nil {
				return nil
			}
			if cleanupErr != nil {
				return cleanupErr
			}
			return ErrConflict
		}
		_ = removeTemporary(parent, temporary)
		return ErrIntegrity
	}
	if err := syscall.Fsync(parent); err != nil {
		return ErrIntegrity
	}
	if err := syscall.Unlinkat(parent, temporary); err != nil {
		return ErrIntegrity
	}
	return syscall.Fsync(parent)
}

func atomicReplace(parent int, name string, data []byte) error {
	// The current record must still be a protected regular file before replace.
	if _, err := readProtectedAt(parent, name, MaxRegistrationSize); err != nil {
		return err
	}
	temporary, err := writeTemporary(parent, name, "replace", data)
	if err != nil {
		return err
	}
	if err := syscall.Renameat(parent, temporary, parent, name); err != nil {
		_ = removeTemporary(parent, temporary)
		return ErrIntegrity
	}
	return syscall.Fsync(parent)
}

func writeTemporary(parent int, target, operation string, data []byte) (string, error) {
	if operation != "create" && operation != "replace" {
		return "", ErrIntegrity
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", ErrIntegrity
	}
	targetDigest := sha256Bytes([]byte(target))
	name := ".tmp-v1-" + operation + "-" + targetDigest + "-" + hex.EncodeToString(random[:])
	fd, err := syscall.Openat(parent, name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", ErrIntegrity
	}
	if err := syscall.Fchmod(fd, 0o600); err != nil {
		syscall.Close(fd)
		syscall.Unlinkat(parent, name)
		return "", ErrIntegrity
	}
	file := os.NewFile(uintptr(fd), "catalog-temporary")
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		syscall.Unlinkat(parent, name)
		return "", ErrIntegrity
	}
	if err := syscall.Fsync(parent); err != nil {
		syscall.Unlinkat(parent, name)
		return "", ErrIntegrity
	}
	return name, nil
}

func sha256Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func parseTemporaryName(name string) (string, bool) {
	const prefix = ".tmp-v1-"
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	remainder := strings.TrimPrefix(name, prefix)
	operation := ""
	for _, candidate := range []string{"create", "replace"} {
		if strings.HasPrefix(remainder, candidate+"-") {
			operation = candidate
			remainder = strings.TrimPrefix(remainder, candidate+"-")
			break
		}
	}
	if operation == "" || len(remainder) != 64+1+24 || remainder[64] != '-' || !isSHA256(remainder[:64]) {
		return "", false
	}
	randomBytes, err := hex.DecodeString(remainder[65:])
	if err != nil || len(randomBytes) != 12 || hex.EncodeToString(randomBytes) != remainder[65:] {
		return "", false
	}
	return remainder[:64], true
}

func recoverTemporaryArtifacts(parent int) error {
	names, err := directoryNamesRaw(parent)
	if err != nil {
		return ErrIntegrity
	}
	ordinary := make(map[string][]string, len(names))
	for _, name := range names {
		if _, temporary := parseTemporaryName(name); !temporary {
			digest := sha256Bytes([]byte(name))
			ordinary[digest] = append(ordinary[digest], name)
		}
	}
	changed := false
	for _, name := range names {
		targetDigest, temporary := parseTemporaryName(name)
		if !temporary {
			continue
		}
		temporaryFD, openErr := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if openErr != nil {
			return ErrIntegrity
		}
		var temporaryStat syscall.Stat_t
		statErr := syscall.Fstat(temporaryFD, &temporaryStat)
		syscall.Close(temporaryFD)
		if statErr != nil || (temporaryStat.Nlink != 1 && temporaryStat.Nlink != 2) || !validProtectedFileStat(temporaryStat, temporaryStat.Nlink) {
			return ErrIntegrity
		}
		if temporaryStat.Nlink == 2 {
			targets := ordinary[targetDigest]
			if len(targets) != 1 {
				return ErrIntegrity
			}
			targetFD, targetErr := syscall.Openat(parent, targets[0], syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if targetErr != nil {
				return ErrIntegrity
			}
			var targetStat syscall.Stat_t
			targetErr = syscall.Fstat(targetFD, &targetStat)
			syscall.Close(targetFD)
			if targetErr != nil || !validProtectedFileStat(targetStat, 2) || targetStat.Dev != temporaryStat.Dev || targetStat.Ino != temporaryStat.Ino {
				return ErrIntegrity
			}
		}
		if err := syscall.Unlinkat(parent, name); err != nil {
			return ErrIntegrity
		}
		changed = true
	}
	if changed && syscall.Fsync(parent) != nil {
		return ErrIntegrity
	}
	return nil
}

func removeTemporary(parent int, name string) error {
	if _, valid := parseTemporaryName(name); !valid {
		return ErrIntegrity
	}
	if err := syscall.Unlinkat(parent, name); err != nil && err != syscall.ENOENT {
		return ErrIntegrity
	}
	if err := syscall.Fsync(parent); err != nil {
		return ErrIntegrity
	}
	return nil
}

func linkAt(oldDir int, oldName string, newDir int, newName string) error {
	oldPointer, err := syscall.BytePtrFromString(oldName)
	if err != nil {
		return err
	}
	newPointer, err := syscall.BytePtrFromString(newName)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_LINKAT, uintptr(oldDir), uintptr(unsafe.Pointer(oldPointer)), uintptr(newDir), uintptr(unsafe.Pointer(newPointer)), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func readRunRecord(runFD int, expectedID string) (RunRegistrationV1, error) {
	record, _, err := readRunRecordBytes(runFD, expectedID)
	return record, err
}

func readRunRecordBytes(runFD int, expectedComponent string) (RunRegistrationV1, int, error) {
	data, err := readProtectedAt(runFD, "run.json", MaxRegistrationSize)
	if err != nil {
		return RunRegistrationV1{}, 0, err
	}
	var record RunRegistrationV1
	if err := decodeCanonical(data, &record); err != nil || validateRunRegistration(record) != nil || storageComponent(record.RunID) != expectedComponent {
		return RunRegistrationV1{}, 0, ErrIntegrity
	}
	return record, len(data), nil
}

func validStorageComponent(value string) bool {
	if ValidateIdentifier(value) == nil && len(value) <= maxDirectStorageComponentBytes && !strings.HasPrefix(value, encodedStorageComponentPrefix) {
		return true
	}
	return isEncodedStorageComponent(value)
}

func isEncodedStorageComponent(value string) bool {
	return len(value) == len(encodedStorageComponentPrefix)+64 && strings.HasPrefix(value, encodedStorageComponentPrefix) && isSHA256(strings.TrimPrefix(value, encodedStorageComponentPrefix))
}

func readLease(activeFD int, runID string) (ActiveOwnerLeaseV1, error) {
	name := storageComponent(runID) + ".json"
	lease, err := readLeaseFile(activeFD, name)
	if err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	if lease.RunID != runID {
		return ActiveOwnerLeaseV1{}, ErrIntegrity
	}
	return lease, nil
}

func readLeaseFile(activeFD int, name string) (ActiveOwnerLeaseV1, error) {
	if !strings.HasSuffix(name, ".json") || !validStorageComponent(strings.TrimSuffix(name, ".json")) {
		return ActiveOwnerLeaseV1{}, ErrIntegrity
	}
	data, err := readProtectedAt(activeFD, name, MaxRegistrationSize)
	if err != nil {
		return ActiveOwnerLeaseV1{}, err
	}
	var lease ActiveOwnerLeaseV1
	if err := decodeCanonical(data, &lease); err != nil || validateLease(lease) != nil || storageComponent(lease.RunID)+".json" != name {
		return ActiveOwnerLeaseV1{}, ErrIntegrity
	}
	return lease, nil
}

func countActiveOwners(activeFD int) (int, error) {
	names, err := directoryNames(activeFD)
	if err != nil || len(names) > MaxRuns {
		return 0, ErrIntegrity
	}
	count := 0
	for _, name := range names {
		lease, err := readLeaseFile(activeFD, name)
		if err != nil {
			return 0, err
		}
		if lease.State == OwnerLeaseActive || lease.State == OwnerLeaseClosing {
			count++
		}
	}
	return count, nil
}

func decodeCanonical(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrIntegrity
	}
	canonical, err := canonicalJSON(target)
	if err != nil || !bytes.Equal(canonical, data) {
		return ErrIntegrity
	}
	return nil
}

//go:build linux

package actioncontrol

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var segmentNamePattern = regexp.MustCompile(`^([0-9]{8})\.jsonl$`)

type Journal struct {
	root    string
	rootFD  int
	rootDev uint64
	rootIno uint64
	// rootAuthorityData is stored as an extended attribute on the trusted
	// service-root inode. Unlike the child identity files, it cannot disappear
	// when a replaceable journal namespace and its adjacent anchor are removed
	// together.
	rootAuthorityData []byte
	// localGuard fairly serializes callers sharing one Journal before they
	// contend on the cross-process root/journal flocks. Without this gate, a
	// high-frequency watcher can repeatedly reacquire the nonblocking flocks
	// and starve an admission until the fixed two-second ceiling expires.
	localGuard chan struct{}
	// decisionEffectGuard fairly serializes decision-effect contenders sharing
	// one Journal before they contend on the cross-process actions-directory
	// flock. The actions inode is already pinned by the service-root journal
	// generation, so this authority cannot be reissued through a replacement
	// pathname or lock file.
	decisionEffectGuard chan struct{}
	// The service root is the cross-process coordination anchor. Every child
	// namespace that can split journal or idempotency authority is both pinned
	// by an open descriptor and recorded in an immutable identity file in its
	// pinned parent.
	actions      *namespacePin
	requestIndex *namespacePin
	lockFile     *namespacePin
	runHistory   *namespacePin
	shardMu      sync.Mutex
	shards       [RequestIndexShards]*namespacePin
	now          func() time.Time
	io           journalIO
	// failedClosed is latched when an ambiguous append cannot prove either a
	// durable rollback or a durable recovery marker. No later operation may
	// use this Journal instance after that point.
	failedClosed atomic.Bool
	// poisonLockFD stores a retained flock descriptor plus one. Retaining the
	// service-wide lock prevents other Journal instances and processes from
	// observing an unproven append while this failed-closed instance lives.
	poisonLockFD atomic.Int64
}

type journalIO struct {
	write    func(string, *os.File, []byte) error
	syncFile func(string, *os.File) error
	syncDir  func(string, int) error
	truncate func(string, *os.File, int64) error
	removeAt func(string, int, string) error
}

func defaultJournalIO() journalIO {
	return journalIO{
		write:    func(_ string, file *os.File, data []byte) error { return writeFull(file, data) },
		syncFile: func(_ string, file *os.File) error { return file.Sync() },
		syncDir:  func(_ string, fd int) error { return syscall.Fsync(fd) },
		truncate: func(_ string, file *os.File, size int64) error { return file.Truncate(size) },
		removeAt: func(_ string, parent int, name string) error { return syscall.Unlinkat(parent, name) },
	}
}

type journalGuard struct {
	journal      *Journal
	runID        string
	rootLockFD   int
	actionsFD    int
	requestsFD   int
	runHistoryFD int
	runFD        int
	runDev       uint64
	runIno       uint64
	// runAuthorityData freezes the exact committed run-generation prefix for
	// the lifetime of this guard. The root flock prevents a legitimate prefix
	// advance while it is held, so any change is an integrity failure.
	runAuthorityData []byte
	lockFD           int
	retainLock       bool
	localGuard       bool
}

// DecisionEffectLease is the single cross-process authority shared by every
// execution and terminal-reconciliation contender for a claimed human
// decision. The lock is deliberately distinct from the action-journal lock so
// journal transactions are never held while a run-transition lease is
// acquired.
type DecisionEffectLease struct {
	journal     *Journal
	runID       string
	operationID string
	actionsFD   int
	localGuard  bool
	closed      bool
}

type namespacePin struct {
	name       string
	anchorName string
	directory  bool
	fd         int
	dev        uint64
	ino        uint64
	anchorFile *os.File
	anchorDev  uint64
	anchorIno  uint64
	anchorData []byte
	// rootAuthorityName/rootAuthorityData are set for request-index shards.
	// Every shard generation has an independent authority record on the trusted
	// service-root inode. Its object identities never change; its bounded
	// issuance checkpoint advances only after a frozen identity is durable.
	rootAuthorityName string
	rootAuthorityData []byte
	// Request-index shard generations also bind the single bounded issuance
	// history file. The service-root authority checkpoints its committed prefix,
	// while these fields prevent loss or same-name inode replacement.
	historyName string
	historyDev  uint64
	historyIno  uint64
}

type rootGenerationAuthorityV1 struct {
	Kind                      string                           `json:"kind"`
	SchemaVersion             int                              `json:"schema_version"`
	State                     string                           `json:"state"`
	RootDevice                uint64                           `json:"root_device"`
	RootInode                 uint64                           `json:"root_inode"`
	Objects                   []rootGenerationObjectIdentityV1 `json:"objects,omitempty"`
	IssuanceCount             int                              `json:"issuance_count,omitempty"`
	IssuanceFinalRecordSHA256 string                           `json:"issuance_final_record_sha256,omitempty"`
	PreparedRunGeneration     *runGenerationV1                 `json:"prepared_run_generation,omitempty"`
	PreparedIdentity          *preparedIdentityPublicationV1   `json:"prepared_identity,omitempty"`
}

// preparedIdentityPublicationV1 makes an initializing root authority a
// transaction coordinator for one identity file. The final identity name is
// never created with an unbound inode: creation happens under StageName, the
// exact inode is checkpointed here, and only then is it renamed into place.
// Objects is the exact next authority prefix, including the identity file at
// IdentityIndex. Its identity device/inode are zero only before the staged
// file has been created and bound.
type preparedIdentityPublicationV1 struct {
	Parent         string                           `json:"parent"`
	IdentityName   string                           `json:"identity_name"`
	StageName      string                           `json:"stage_name"`
	IdentityIndex  int                              `json:"identity_index"`
	IdentitySHA256 string                           `json:"identity_sha256"`
	Objects        []rootGenerationObjectIdentityV1 `json:"objects"`
}

type rootGenerationObjectIdentityV1 struct {
	Parent     string `json:"parent"`
	Name       string `json:"name"`
	ObjectType string `json:"object_type"`
	Device     uint64 `json:"device"`
	Inode      uint64 `json:"inode"`
}

type namespaceIdentityV1 struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	Name          string `json:"name"`
	ObjectType    string `json:"object_type"`
	Device        uint64 `json:"device"`
	Inode         uint64 `json:"inode"`
}

type segmentState struct {
	epoch      uint64
	bytes      int64
	records    int
	lastDigest string
	empty      bool
}

type journalState struct {
	sequence   uint64
	total      int
	segments   []segmentState
	operations map[string]*Operation
	lookups    map[string]string
	decisions  map[string]string
}

type decodedRecord struct {
	kind      string
	epoch     uint64
	sequence  uint64
	prior     string
	operation string
	receipt   *ActionReceiptV1
	claim     *ActionClaimV1
	outcome   *ActionOutcomeV1
}

type requestIdentityV1 struct {
	Kind                      string            `json:"kind"`
	SchemaVersion             int               `json:"schema_version"`
	LookupKeySHA256           string            `json:"lookup_key_sha256"`
	OperationID               string            `json:"operation_id"`
	PrincipalID               string            `json:"principal_id"`
	PrincipalType             string            `json:"principal_type"`
	RequestID                 string            `json:"request_id"`
	RequestSHA256             string            `json:"request_sha256"`
	Action                    string            `json:"action"`
	RunID                     string            `json:"run_id"`
	AttemptID                 string            `json:"attempt_id"`
	ExpectedState             string            `json:"expected_state"`
	ExpectedRevision          string            `json:"expected_revision"`
	Reason                    string            `json:"reason"`
	DelegatedActor            *DelegatedActorV1 `json:"delegated_actor,omitempty"`
	Payload                   json.RawMessage   `json:"payload"`
	PolicyVersion             string            `json:"policy_version,omitempty"`
	AuthorityGrantSHA256      string            `json:"authority_grant_file_sha256,omitempty"`
	AdmittedStateTransitionID string            `json:"admitted_state_transition_event_id"`
	OwnerLeaseID              string            `json:"owner_lease_id,omitempty"`
	ReceivedAt                string            `json:"received_at"`
}

type requestIssuanceV1 struct {
	Kind              string `json:"kind"`
	SchemaVersion     int    `json:"schema_version"`
	Sequence          int    `json:"sequence"`
	PriorRecordSHA256 string `json:"prior_record_sha256,omitempty"`
	LookupKeySHA256   string `json:"lookup_key_sha256"`
	IdentitySHA256    string `json:"identity_sha256"`
	IdentityDevice    uint64 `json:"identity_device"`
	IdentityInode     uint64 `json:"identity_inode"`
}

type requestIssuanceState struct {
	authority      rootGenerationAuthorityV1
	authorityData  []byte
	records        map[string]requestIssuanceV1
	committedBytes int64
	lastDigest     string
	tail           []byte
}

type runGenerationV1 struct {
	Kind              string `json:"kind"`
	SchemaVersion     int    `json:"schema_version"`
	Sequence          int    `json:"sequence"`
	PriorRecordSHA256 string `json:"prior_record_sha256,omitempty"`
	RunID             string `json:"run_id"`
	StorageComponent  string `json:"storage_component"`
	DirectoryDevice   uint64 `json:"directory_device"`
	DirectoryInode    uint64 `json:"directory_inode"`
}

type runGenerationState struct {
	authority      rootGenerationAuthorityV1
	authorityData  []byte
	records        map[string]runGenerationV1
	committedBytes int64
	lastDigest     string
	tail           []byte
}

type appendPendingV1 struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	SegmentName   string `json:"segment_name"`
	PreAppendSize int64  `json:"pre_append_size"`
}

const (
	requestIndexDirectory          = "request-index"
	actionsIdentityName            = ".actions.identity.json"
	lockIdentityName               = ".lock.identity.json"
	requestIndexIdentityName       = ".request-index.identity.json"
	requestIssuanceHistoryName     = ".issuance-history.jsonl"
	runGenerationHistoryName       = ".run-generations.jsonl"
	runGenerationIdentityName      = ".run-generations.identity.json"
	runGenerationPreparedName      = ".run-generation.prepared"
	namespaceIdentityMaxBytes      = 512
	requestIssuanceMaxBytes        = 512
	requestIssuanceHistoryMaxBytes = int64(MaxRequestsPerShard * (requestIssuanceMaxBytes + 1))
	runGenerationMaxBytes          = 1024
	runGenerationHistoryMaxBytes   = int64(MaxJournalRuns * (runGenerationMaxBytes + 1))
	appendPendingName              = ".append-pending.json"
	appendPendingMaxBytes          = 512
	journalRootAuthorityXattr      = "user.abcp.actioncontrol.journal-generation-v1"
	journalRunAuthorityXattr       = "user.abcp.actioncontrol.journal-runs-generation-v1"
	journalRootAuthorityKind       = "ActionJournalRootGenerationV1"
	journalShardAuthorityKind      = "ActionJournalRequestShardGenerationV1"
	journalRunAuthorityKind        = "ActionJournalRunGenerationsV1"
	rootGenerationInitializing     = "initializing"
	rootGenerationEstablished      = "established"
	rootGenerationPrepared         = "prepared"
	rootGenerationMaxBytes         = 8192
	rootGenerationXattrCreate      = 1
	rootGenerationXattrReplace     = 2
)

// journalBootstrapBoundaryHook is overridden only by subprocess crash tests.
var journalBootstrapBoundaryHook = func(string, string) {}

func Open(root string) (*Journal, error)        { return OpenWithClock(root, time.Now) }
func OpenJournal(root string) (*Journal, error) { return Open(root) }

func OpenWithClock(root string, clock func() time.Time) (*Journal, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) || clock == nil {
		return nil, errors.New("canonical service root and clock are required")
	}
	fd, err := openAbsoluteDirectory(root)
	if err != nil {
		return nil, ErrIntegrity
	}
	if err := validateDirectoryFD(fd, true); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		syscall.Close(fd)
		return nil, ErrIntegrity
	}
	j := &Journal{root: root, rootFD: fd, rootDev: uint64(stat.Dev), rootIno: stat.Ino, now: clock, io: defaultJournalIO(),
		localGuard: make(chan struct{}, 1), decisionEffectGuard: make(chan struct{}, 1)}
	j.localGuard <- struct{}{}
	j.decisionEffectGuard <- struct{}{}
	authority, authorityData, found, err := readRootGenerationAuthority(fd, journalRootAuthorityXattr, journalRootAuthorityKind, j.rootDev, j.rootIno)
	if err != nil {
		j.Close()
		return nil, err
	}
	locked := false
	defer func() {
		if locked {
			_ = syscall.Flock(fd, syscall.LOCK_UN)
		}
	}()
	if !found || authority.State == rootGenerationInitializing {
		if err := lockRootGeneration(fd, JournalLockTimeout); err != nil {
			j.Close()
			return nil, err
		}
		locked = true
		authority, authorityData, found, err = readRootGenerationAuthority(fd, journalRootAuthorityXattr, journalRootAuthorityKind, j.rootDev, j.rootIno)
		if err != nil {
			j.Close()
			return nil, err
		}
	}
	if !found {
		if err := requireGenerationEntriesAbsent(fd,
			generationEntry{name: "actions", directory: true},
			generationEntry{name: actionsIdentityName}); err != nil {
			j.Close()
			return nil, err
		}
		authority, authorityData, err = createInitializingRootGeneration(fd, journalRootAuthorityXattr, journalRootAuthorityKind, j.rootDev, j.rootIno)
		if err != nil {
			j.Close()
			return nil, err
		}
	}
	runAuthority, runAuthorityData, runAuthorityFound, err := readRootGenerationAuthority(fd, journalRunAuthorityXattr, journalRunAuthorityKind, j.rootDev, j.rootIno)
	if err != nil {
		j.Close()
		return nil, err
	}
	if !runAuthorityFound {
		if authority.State != rootGenerationInitializing {
			j.Close()
			return nil, ErrIntegrity
		}
		runAuthority, runAuthorityData, err = createInitializingRootGeneration(fd, journalRunAuthorityXattr, journalRunAuthorityKind, j.rootDev, j.rootIno)
		if err != nil {
			j.Close()
			return nil, err
		}
	}
	if runAuthority.State == rootGenerationPrepared && !locked {
		if err := lockRootGeneration(fd, JournalLockTimeout); err != nil {
			j.Close()
			return nil, err
		}
		locked = true
		runAuthority, runAuthorityData, runAuthorityFound, err = readRootGenerationAuthority(fd, journalRunAuthorityXattr,
			journalRunAuthorityKind, j.rootDev, j.rootIno)
		if err != nil || !runAuthorityFound {
			j.Close()
			if err != nil {
				return nil, err
			}
			return nil, ErrIntegrity
		}
	}
	allowCreate := authority.State == rootGenerationInitializing
	actions, err := j.openDirectoryAt(fd, "actions", allowCreate)
	if err != nil {
		j.Close()
		if !allowCreate && errors.Is(err, ErrNotFound) {
			return nil, ErrIntegrity
		}
		return nil, err
	}
	j.actions, err = j.pinNamespace(fd, ".", "actions", actionsIdentityName, actions, true, allowCreate,
		journalRootAuthorityXattr, journalRootAuthorityKind)
	if err != nil {
		syscall.Close(actions)
		j.Close()
		return nil, err
	}
	if err := j.ensureLockFile(actions, allowCreate); err != nil {
		j.Close()
		return nil, err
	}
	lockFD, err := syscall.Openat(actions, ".lock", syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		j.Close()
		return nil, ErrIntegrity
	}
	j.lockFile, err = j.pinNamespace(actions, "actions", ".lock", lockIdentityName, lockFD, false, allowCreate,
		journalRootAuthorityXattr, journalRootAuthorityKind)
	if err != nil {
		syscall.Close(lockFD)
		j.Close()
		return nil, err
	}
	requests, err := j.openDirectoryAt(actions, requestIndexDirectory, allowCreate)
	if err != nil {
		j.Close()
		if !allowCreate && errors.Is(err, ErrNotFound) {
			return nil, ErrIntegrity
		}
		return nil, err
	}
	j.requestIndex, err = j.pinNamespace(actions, "actions", requestIndexDirectory, requestIndexIdentityName, requests, true, allowCreate,
		journalRootAuthorityXattr, journalRootAuthorityKind)
	if err != nil {
		syscall.Close(requests)
		j.Close()
		return nil, err
	}
	if err := j.ensureRunGenerationHistory(actions, allowCreate); err != nil {
		j.Close()
		return nil, err
	}
	runHistoryFD, err := syscall.Openat(actions, runGenerationHistoryName, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		j.Close()
		return nil, ErrIntegrity
	}
	j.runHistory, err = j.pinNamespace(actions, "actions", runGenerationHistoryName, runGenerationIdentityName, runHistoryFD, false, allowCreate,
		journalRootAuthorityXattr, journalRootAuthorityKind)
	if err != nil {
		syscall.Close(runHistoryFD)
		j.Close()
		return nil, err
	}
	expectedAuthority, expectedData, err := makeRootGenerationAuthority(journalRootAuthorityKind, j.rootDev, j.rootIno, journalRootGenerationObjects(j))
	if err != nil {
		j.Close()
		return nil, err
	}
	if allowCreate {
		authorityData, err = publishRootGeneration(fd, journalRootAuthorityXattr, expectedAuthority, expectedData)
	} else if !bytes.Equal(authorityData, expectedData) {
		err = ErrIntegrity
	}
	if err != nil {
		j.Close()
		return nil, err
	}
	j.rootAuthorityData = append([]byte(nil), authorityData...)
	runHistoryStat := syscall.Stat_t{}
	if syscall.Fstat(j.runHistory.fd, &runHistoryStat) != nil || validateRunGenerationHistoryStat(runHistoryStat) != nil {
		j.Close()
		return nil, ErrIntegrity
	}
	runObjects := rootGenerationObjectsForPin("actions", j.runHistory)
	switch runAuthority.State {
	case rootGenerationInitializing:
		if runHistoryStat.Size != 0 {
			j.Close()
			return nil, ErrIntegrity
		}
		expectedRunAuthority, expectedRunData, makeErr := makeRunGenerationAuthority(j.rootDev, j.rootIno, runObjects, 0, "")
		if makeErr != nil {
			j.Close()
			return nil, makeErr
		}
		runAuthorityData, err = publishRootGeneration(fd, journalRunAuthorityXattr, expectedRunAuthority, expectedRunData)
	case rootGenerationEstablished:
		_, expectedRunData, makeErr := makeRunGenerationAuthority(j.rootDev, j.rootIno, runObjects,
			runAuthority.IssuanceCount, runAuthority.IssuanceFinalRecordSHA256)
		if makeErr != nil || !bytes.Equal(runAuthorityData, expectedRunData) {
			err = ErrIntegrity
		}
	case rootGenerationPrepared:
		if runAuthority.PreparedRunGeneration == nil {
			err = ErrIntegrity
			break
		}
		_, expectedRunData, makeErr := makePreparedRunGenerationAuthority(j.rootDev, j.rootIno, runObjects,
			runAuthority.IssuanceCount, runAuthority.IssuanceFinalRecordSHA256, *runAuthority.PreparedRunGeneration)
		if makeErr != nil || !bytes.Equal(runAuthorityData, expectedRunData) {
			err = ErrIntegrity
		}
	default:
		err = ErrIntegrity
	}
	if err != nil {
		j.Close()
		return nil, err
	}
	// Validate every registered run on a fresh quiescent open. An open racing
	// an active journal transaction remains nonblocking here; its first
	// operation will acquire the root lock and validate the exact target.
	if !locked {
		flockErr := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if flockErr == nil {
			locked = true
		} else if flockErr != syscall.EWOULDBLOCK && flockErr != syscall.EAGAIN {
			j.Close()
			return nil, ErrIntegrity
		}
	}
	if locked {
		if _, err := j.reconcileRunGenerations(true); err != nil {
			j.Close()
			return nil, err
		}
	} else {
		// A concurrent registration may leave a legitimate uncommitted tail or
		// newly created directory, so a racing Open does not reconcile or reject
		// extras. The immutable committed prefix is still safe to parse and every
		// already-authorized run must retain its exact named inode.
		state, history, _, loadErr := j.loadRunGenerationState()
		if history != nil {
			loadErr = errors.Join(loadErr, history.Close())
		}
		if loadErr != nil || j.validateCommittedRunGenerationDirectories(state.records) != nil {
			j.Close()
			return nil, ErrIntegrity
		}
	}
	if err := j.loadEstablishedShards(locked); err != nil {
		j.Close()
		return nil, err
	}
	if err := j.verifyNamespaces(); err != nil {
		j.Close()
		return nil, err
	}
	if locked {
		if err := syscall.Flock(fd, syscall.LOCK_UN); err != nil {
			j.Close()
			return nil, ErrIntegrity
		}
		locked = false
	}
	return j, nil
}

func (j *Journal) Close() error {
	if j == nil || j.rootFD < 0 {
		return nil
	}
	var poisonErr error
	if encoded := j.poisonLockFD.Swap(0); encoded != 0 {
		fd := int(encoded - 1)
		poisonErr = errors.Join(syscall.Flock(fd, syscall.LOCK_UN), syscall.Close(fd))
	}
	var pinErr error
	j.shardMu.Lock()
	for index := range j.shards {
		pinErr = errors.Join(pinErr, closeNamespacePin(j.shards[index]))
		j.shards[index] = nil
	}
	j.shardMu.Unlock()
	pinErr = errors.Join(pinErr, closeNamespacePin(j.runHistory), closeNamespacePin(j.requestIndex), closeNamespacePin(j.lockFile), closeNamespacePin(j.actions))
	j.runHistory, j.requestIndex, j.lockFile, j.actions = nil, nil, nil, nil
	err := errors.Join(poisonErr, pinErr, syscall.Close(j.rootFD))
	j.rootFD = -1
	return err
}

func (j *Journal) CreateReceipt(ctx context.Context, input ReceiptInput, bind func(uint64) (AdmissionBinding, error)) (resultReceipt ActionReceiptV1, resultCreated bool, resultErr error) {
	if ctx == nil || input.RunID == "" {
		return ActionReceiptV1{}, false, ErrIntegrity
	}
	guard, err := j.acquireActions(ctx)
	if err != nil {
		return ActionReceiptV1{}, false, err
	}
	defer func() { resultErr = errors.Join(resultErr, guard.close()) }()
	lookup := LookupKey(input.PrincipalID, input.RequestID)
	identity, found, err := guard.readIdentity(lookup)
	if err != nil {
		return ActionReceiptV1{}, false, err
	}
	if found {
		if !identityMatchesInput(identity, input) {
			return ActionReceiptV1{}, false, ErrConflict
		}
		if err := guard.openRun(identity.RunID, true); err != nil {
			return ActionReceiptV1{}, false, err
		}
		state, err := guard.scan()
		if err != nil {
			return ActionReceiptV1{}, false, err
		}
		if existing := state.operations[identity.OperationID]; existing != nil {
			if err := reproveOperation(identity, existing); err != nil {
				return ActionReceiptV1{}, false, err
			}
			return cloneReceipt(existing.Receipt), false, nil
		}
		receipt := receiptFromIdentity(identity)
		if err := validateReceiptMaterialization(state, receipt); err != nil {
			return ActionReceiptV1{}, false, err
		}
		if err := guard.append(state, &receipt); err != nil {
			return ActionReceiptV1{}, false, err
		}
		return receipt, true, nil
	}
	if err := guard.openRun(input.RunID, true); err != nil {
		return ActionReceiptV1{}, false, err
	}
	state, err := guard.scan()
	if err != nil {
		return ActionReceiptV1{}, false, err
	}
	if _, duplicate := state.lookups[lookup]; duplicate {
		// A journal receipt without its immutable service-wide reservation is
		// not eligible for migration by inference.
		return ActionReceiptV1{}, false, ErrIntegrity
	}
	if input.Action == "decision" {
		decisionRequestID, err := journalDecisionRequestID(input.Payload)
		if err != nil {
			return ActionReceiptV1{}, false, err
		}
		if _, exists := state.decisions[decisionRequestID]; exists {
			return ActionReceiptV1{}, false, ErrDecisionExists
		}
	}
	operationID := operationIDForLookup(lookup)
	if _, exists := state.operations[operationID]; exists {
		return ActionReceiptV1{}, false, ErrIntegrity
	}
	next := state.sequence + 1
	binding := AdmissionBinding{}
	if bind != nil {
		binding, err = bind(next)
		if err != nil {
			if binding.Release != nil {
				err = errors.Join(err, binding.Release())
			}
			return ActionReceiptV1{}, false, err
		}
	}
	receipt := ActionReceiptV1{
		Kind: "ActionReceiptV1", SchemaVersion: JournalSchemaVersion,
		LookupKeySHA256: lookup, OperationID: operationID, PrincipalID: input.PrincipalID, PrincipalType: input.PrincipalType,
		RequestID: input.RequestID, RequestSHA256: input.RequestSHA256, Action: input.Action, RunID: input.RunID, AttemptID: input.AttemptID,
		ExpectedState: input.ExpectedState, ExpectedRevision: input.ExpectedRevision, Reason: input.Reason,
		DelegatedActor: cloneActor(input.DelegatedActor), Payload: append(json.RawMessage(nil), input.Payload...),
		PolicyVersion: input.PolicyVersion, AuthorityGrantSHA256: input.AuthorityGrantSHA256,
		AdmittedStateTransitionID: input.StateTransitionID, OwnerLeaseID: binding.OwnerLeaseID, ReceivedAt: canonicalTime(j.now()),
	}
	if _, _, _, err := prepareAppend(state, &receipt); err != nil {
		if binding.Release != nil {
			err = errors.Join(err, binding.Release())
		}
		return ActionReceiptV1{}, false, err
	}
	identity = identityFromReceipt(receipt)
	if err := validateRequestIdentity(identity); err != nil {
		if binding.Release != nil {
			err = errors.Join(err, binding.Release())
		}
		return ActionReceiptV1{}, false, err
	}
	indexErr := guard.createIdentity(identity)
	appendErr := indexErr
	if indexErr == nil {
		appendErr = guard.append(state, &receipt)
	}
	var releaseErr error
	if binding.Release != nil {
		releaseErr = binding.Release()
	}
	if err := errors.Join(appendErr, releaseErr); err != nil {
		return ActionReceiptV1{}, false, err
	}
	return receipt, true, nil
}

// ReplayReceipt performs the O(1) global identity lookup before touching a run
// journal. An exact durable reservation whose receipt was interrupted is
// materialized from the frozen identity; different intent never mutates state.
func (j *Journal) ReplayReceipt(ctx context.Context, replay ReceiptReplayInput) (result Operation, resultErr error) {
	if ctx == nil || runtimeIdentifier(replay.PrincipalID) != nil || runtimeIdentifier(replay.RequestID) != nil {
		return Operation{}, ErrIntegrity
	}
	guard, err := j.acquireActions(ctx)
	if err != nil {
		return Operation{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, guard.close()) }()
	identity, found, err := guard.readIdentity(LookupKey(replay.PrincipalID, replay.RequestID))
	if err != nil {
		return Operation{}, err
	}
	if !found {
		return Operation{}, ErrNotFound
	}
	if !identityMatchesReplay(identity, replay) {
		return Operation{}, ErrConflict
	}
	if err := guard.openRun(identity.RunID, true); err != nil {
		return Operation{}, err
	}
	state, err := guard.scan()
	if err != nil {
		return Operation{}, err
	}
	if operation := state.operations[identity.OperationID]; operation != nil {
		if err := reproveOperation(identity, operation); err != nil {
			return Operation{}, err
		}
		return cloneOperation(*operation), nil
	}
	receipt := receiptFromIdentity(identity)
	if err := validateReceiptMaterialization(state, receipt); err != nil {
		return Operation{}, err
	}
	if err := guard.append(state, &receipt); err != nil {
		return Operation{}, err
	}
	return Operation{Receipt: cloneReceipt(receipt), Status: StatusReceived}, nil
}

// AcquireDecisionEffect acquires the non-reissuable actions-generation flock
// and then re-reads the exact claimed decision while that exclusion is held.
// Holding this lease never holds the action-journal lock: callers may perform
// bounded journal transactions, release them, and only then acquire the
// run-transition lease used by the effect.
func (j *Journal) AcquireDecisionEffect(ctx context.Context, runID, operationID string) (*DecisionEffectLease, Operation, error) {
	if j == nil || j.rootFD < 0 || ctx == nil || runtimeIdentifier(runID) != nil || runtimeIdentifier(operationID) != nil || j.failedClosed.Load() {
		return nil, Operation{}, ErrIntegrity
	}
	deadline := time.Now().Add(JournalLockTimeout)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-j.decisionEffectGuard:
	case <-ctx.Done():
		return nil, Operation{}, errors.Join(ErrBusy, ctx.Err())
	case <-timer.C:
		return nil, Operation{}, ErrBusy
	}
	releaseLocal := func() { j.decisionEffectGuard <- struct{}{} }
	if j.failedClosed.Load() {
		releaseLocal()
		return nil, Operation{}, ErrIntegrity
	}
	if err := j.verifyNamespaces(); err != nil {
		releaseLocal()
		return nil, Operation{}, err
	}
	actionsFD, err := j.openPinnedNamespace(j.rootFD, j.actions)
	if err != nil {
		releaseLocal()
		return nil, Operation{}, err
	}
	if err := j.flockContext(ctx, actionsFD, deadline); err != nil {
		_ = syscall.Close(actionsFD)
		releaseLocal()
		return nil, Operation{}, err
	}
	lease := &DecisionEffectLease{journal: j, runID: runID, operationID: operationID, actionsFD: actionsFD, localGuard: true}
	if err := lease.Revalidate(); err != nil {
		_ = lease.Close()
		return nil, Operation{}, err
	}
	readCtx, cancel := context.WithDeadline(ctx, deadline)
	operation, err := lease.Operation(readCtx)
	cancel()
	if err != nil {
		closeErr := lease.Close()
		return nil, Operation{}, errors.Join(err, closeErr)
	}
	return lease, operation, nil
}

// Operation revalidates the pinned effect authority around an authoritative
// journal read. Only a durably claimed human-decision operation can be bound
// to this lease.
func (l *DecisionEffectLease) Operation(ctx context.Context) (Operation, error) {
	if l == nil || l.closed || ctx == nil || l.journal == nil {
		return Operation{}, ErrIntegrity
	}
	if err := l.Revalidate(); err != nil {
		return Operation{}, err
	}
	operation, err := l.journal.Read(ctx, l.runID, l.operationID)
	if err != nil {
		return Operation{}, err
	}
	if operation.Receipt.Action != "decision" || operation.Claim == nil || operation.Claim.ControllerEventID != DeterministicEventID(DecisionEventDomain, l.operationID) {
		return Operation{}, ErrIntegrity
	}
	if err := l.Revalidate(); err != nil {
		return Operation{}, err
	}
	return operation, nil
}

// Revalidate proves that the held flock and the service-root-authorized
// actions generation still name the exact pinned inode.
func (l *DecisionEffectLease) Revalidate() error {
	if l == nil || l.closed || l.journal == nil || l.actionsFD < 0 || l.journal.failedClosed.Load() {
		return ErrIntegrity
	}
	if err := l.journal.verifyNamespaces(); err != nil {
		return err
	}
	var stat syscall.Stat_t
	if syscall.Fstat(l.actionsFD, &stat) != nil || uint64(stat.Dev) != l.journal.actions.dev || stat.Ino != l.journal.actions.ino || validateDirectoryFD(l.actionsFD, false) != nil {
		return ErrIntegrity
	}
	return nil
}

// Close revalidates the exact authority before releasing it. Namespace loss
// permanently fails this Journal instance closed.
func (l *DecisionEffectLease) Close() error {
	if l == nil || l.closed {
		return nil
	}
	l.closed = true
	var authorityErr error
	if l.journal != nil && l.actionsFD >= 0 {
		// Revalidate inline because the public method rejects a closed lease.
		if err := l.journal.verifyNamespaces(); err != nil {
			authorityErr = err
		} else {
			var stat syscall.Stat_t
			if syscall.Fstat(l.actionsFD, &stat) != nil || uint64(stat.Dev) != l.journal.actions.dev || stat.Ino != l.journal.actions.ino || validateDirectoryFD(l.actionsFD, false) != nil {
				authorityErr = ErrIntegrity
			}
		}
		if authorityErr != nil {
			l.journal.failedClosed.Store(true)
		}
	}
	var lockErr error
	if l.actionsFD >= 0 {
		lockErr = errors.Join(syscall.Flock(l.actionsFD, syscall.LOCK_UN), syscall.Close(l.actionsFD))
		l.actionsFD = -1
	}
	if l.localGuard && l.journal != nil {
		l.journal.decisionEffectGuard <- struct{}{}
		l.localGuard = false
	}
	return errors.Join(authorityErr, lockErr)
}

func (j *Journal) CreateClaim(ctx context.Context, runID, operationID, ownerLeaseID, eventDomain string) (resultClaim ActionClaimV1, resultCreated bool, resultErr error) {
	guard, err := j.acquireRun(ctx, runID, false)
	if err != nil {
		return ActionClaimV1{}, false, err
	}
	defer func() { resultErr = errors.Join(resultErr, guard.close()) }()
	state, err := guard.scan()
	if err != nil {
		return ActionClaimV1{}, false, err
	}
	operation, ok := state.operations[operationID]
	if !ok || operation.Receipt.RunID != runID {
		return ActionClaimV1{}, false, ErrNotFound
	}
	if err := guard.reproveReceipt(operation); err != nil {
		return ActionClaimV1{}, false, err
	}
	if operation.Claim != nil {
		existing := *operation.Claim
		if existing.OwnerLeaseID != ownerLeaseID || existing.AdmittedStateTransitionID != operation.Receipt.AdmittedStateTransitionID || existing.ControllerEventID != DeterministicEventID(eventDomain, operationID) {
			return ActionClaimV1{}, false, ErrIntegrity
		}
		return existing, false, nil
	}
	if operation.Status != StatusReceived {
		return ActionClaimV1{}, false, ErrConflict
	}
	now := canonicalTime(j.now())
	claim := ActionClaimV1{Kind: "ActionClaimV1", SchemaVersion: JournalSchemaVersion, OperationID: operationID,
		AdmittedStateTransitionID: operation.Receipt.AdmittedStateTransitionID, OwnerLeaseID: ownerLeaseID,
		ClaimedAt: now, ControllerEventID: DeterministicEventID(eventDomain, operationID), ControllerEventTimestamp: now}
	if err := guard.append(state, &claim); err != nil {
		return ActionClaimV1{}, false, err
	}
	return claim, true, nil
}

func (j *Journal) AppendOutcome(ctx context.Context, runID, operationID string, status OutcomeStatus, eventIDs []string, reasonCode string) (resultOutcome ActionOutcomeV1, resultErr error) {
	guard, err := j.acquireRun(ctx, runID, false)
	if err != nil {
		return ActionOutcomeV1{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, guard.close()) }()
	state, err := guard.scan()
	if err != nil {
		return ActionOutcomeV1{}, err
	}
	operation, ok := state.operations[operationID]
	if !ok || operation.Receipt.RunID != runID {
		return ActionOutcomeV1{}, ErrNotFound
	}
	if err := guard.reproveReceipt(operation); err != nil {
		return ActionOutcomeV1{}, err
	}
	if isTerminal(operation.Status) {
		if len(operation.Outcomes) == 0 {
			return ActionOutcomeV1{}, ErrIntegrity
		}
		existing := operation.Outcomes[len(operation.Outcomes)-1]
		if existing.Status == status && slicesEqual(existing.AuthoritativeEventIDs, eventIDs) && existing.ReasonCode == reasonCode {
			return existing, nil
		}
		return ActionOutcomeV1{}, ErrConflict
	}
	outcome := ActionOutcomeV1{Kind: "ActionOutcomeV1", SchemaVersion: JournalSchemaVersion, OperationID: operationID,
		Status: status, RecordedAt: canonicalTime(j.now()), AuthoritativeEventIDs: append([]string(nil), eventIDs...), ReasonCode: reasonCode}
	trial := *operation
	trial.Outcomes = append(append([]ActionOutcomeV1(nil), operation.Outcomes...), outcome)
	if err := validateProgress(trial); err != nil {
		return ActionOutcomeV1{}, err
	}
	if err := guard.append(state, &outcome); err != nil {
		return ActionOutcomeV1{}, err
	}
	return outcome, nil
}

func (j *Journal) Read(ctx context.Context, runID, operationID string) (result Operation, resultErr error) {
	guard, err := j.acquireRun(ctx, runID, false)
	if err != nil {
		return Operation{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, guard.close()) }()
	state, err := guard.scan()
	if err != nil {
		return Operation{}, err
	}
	operation, ok := state.operations[operationID]
	if !ok || operation.Receipt.RunID != runID {
		return Operation{}, ErrNotFound
	}
	if err := guard.reproveReceipt(operation); err != nil {
		return Operation{}, err
	}
	return cloneOperation(*operation), nil
}

func (j *Journal) ReadByRequest(ctx context.Context, runID, principalID, requestID string) (result Operation, resultErr error) {
	guard, err := j.acquireActions(ctx)
	if err != nil {
		return Operation{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, guard.close()) }()
	identity, found, err := guard.readIdentity(LookupKey(principalID, requestID))
	if err != nil {
		return Operation{}, err
	}
	if !found {
		return Operation{}, ErrNotFound
	}
	if err := guard.openRun(identity.RunID, false); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Operation{}, ErrReceiptPending
		}
		return Operation{}, err
	}
	state, err := guard.scan()
	if err != nil {
		return Operation{}, err
	}
	operation := state.operations[identity.OperationID]
	if operation == nil {
		return Operation{}, ErrReceiptPending
	}
	if err := reproveOperation(identity, operation); err != nil {
		return Operation{}, err
	}
	_ = runID // The immutable identity selects the exact journal to re-prove.
	return cloneOperation(*operation), nil
}

func (j *Journal) ReadByDecisionRequest(ctx context.Context, runID, decisionRequestID string) (result Operation, resultErr error) {
	guard, err := j.acquireRun(ctx, runID, false)
	if err != nil {
		return Operation{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, guard.close()) }()
	state, err := guard.scan()
	if err != nil {
		return Operation{}, err
	}
	var found *Operation
	for _, operation := range state.operations {
		if operation.Receipt.Action != "decision" {
			continue
		}
		var payload struct {
			DecisionRequestID string `json:"decision_request_id"`
		}
		if json.Unmarshal(operation.Receipt.Payload, &payload) != nil {
			return Operation{}, ErrIntegrity
		}
		if payload.DecisionRequestID != decisionRequestID {
			continue
		}
		if found != nil {
			return Operation{}, ErrIntegrity
		}
		clone := cloneOperation(*operation)
		found = &clone
	}
	if found == nil {
		return Operation{}, ErrNotFound
	}
	return *found, nil
}

func (j *Journal) OperationsForLease(ctx context.Context, runID, leaseID string, through *uint64) (result []Operation, resultErr error) {
	guard, err := j.acquireRun(ctx, runID, false)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, guard.close()) }()
	state, err := guard.scan()
	if err != nil {
		return nil, err
	}
	result = make([]Operation, 0)
	for _, operation := range state.operations {
		if operation.Receipt.Action != "cancel" || operation.Receipt.OwnerLeaseID != leaseID || (through != nil && operation.Receipt.Sequence > *through) || isTerminal(operation.Status) {
			continue
		}
		if err := guard.reproveReceipt(operation); err != nil {
			return nil, err
		}
		result = append(result, cloneOperation(*operation))
	}
	sort.Slice(result, func(a, b int) bool { return result[a].Receipt.Sequence < result[b].Receipt.Sequence })
	return result, nil
}

func (j *Journal) acquire(ctx context.Context, runID string) (*journalGuard, error) {
	return j.acquireRun(ctx, runID, true)
}

func (j *Journal) acquireRun(ctx context.Context, runID string, create bool) (*journalGuard, error) {
	if j == nil || j.rootFD < 0 || ctx == nil || runtimeIdentifier(runID) != nil {
		return nil, ErrIntegrity
	}
	guard, err := j.acquireActions(ctx)
	if err != nil {
		return nil, err
	}
	if err := guard.openRun(runID, create); err != nil {
		guard.close()
		return nil, err
	}
	return guard, nil
}

func (j *Journal) acquireActions(ctx context.Context) (*journalGuard, error) {
	if j == nil || j.rootFD < 0 || ctx == nil || j.failedClosed.Load() {
		return nil, ErrIntegrity
	}
	deadline := time.Now().Add(JournalLockTimeout)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-j.localGuard:
	case <-ctx.Done():
		return nil, errors.Join(ErrBusy, ctx.Err())
	case <-timer.C:
		return nil, ErrBusy
	}
	releaseLocal := func() { j.localGuard <- struct{}{} }
	if j.failedClosed.Load() {
		releaseLocal()
		return nil, ErrIntegrity
	}
	if err := j.verifyRoot(); err != nil {
		releaseLocal()
		return nil, err
	}
	rootLockFD, err := j.openRootLock()
	if err != nil {
		releaseLocal()
		return nil, err
	}
	if err := j.flockContext(ctx, rootLockFD, deadline); err != nil {
		syscall.Close(rootLockFD)
		releaseLocal()
		return nil, err
	}
	cleanupRoot := func() {
		_ = syscall.Flock(rootLockFD, syscall.LOCK_UN)
		_ = syscall.Close(rootLockFD)
		releaseLocal()
	}
	if err := j.verifyNamespaces(); err != nil {
		cleanupRoot()
		return nil, err
	}
	actionsFD, err := j.openPinnedNamespace(j.rootFD, j.actions)
	if err != nil {
		cleanupRoot()
		return nil, err
	}
	requestsFD, err := j.openPinnedNamespace(actionsFD, j.requestIndex)
	if err != nil {
		syscall.Close(actionsFD)
		cleanupRoot()
		return nil, err
	}
	lockFD, err := j.openPinnedNamespace(actionsFD, j.lockFile)
	if err != nil {
		syscall.Close(requestsFD)
		syscall.Close(actionsFD)
		cleanupRoot()
		return nil, err
	}
	runHistoryFD, err := j.openPinnedNamespace(actionsFD, j.runHistory)
	if err != nil {
		syscall.Close(lockFD)
		syscall.Close(requestsFD)
		syscall.Close(actionsFD)
		cleanupRoot()
		return nil, err
	}
	if err := j.flockContext(ctx, lockFD, deadline); err != nil {
		syscall.Close(runHistoryFD)
		syscall.Close(lockFD)
		syscall.Close(requestsFD)
		syscall.Close(actionsFD)
		cleanupRoot()
		return nil, err
	}
	// An append holding this same service-wide lock may have latched the
	// instance while this caller was waiting for flock.
	if j.failedClosed.Load() {
		_ = syscall.Flock(lockFD, syscall.LOCK_UN)
		syscall.Close(runHistoryFD)
		syscall.Close(lockFD)
		syscall.Close(requestsFD)
		syscall.Close(actionsFD)
		cleanupRoot()
		return nil, ErrIntegrity
	}
	guard := &journalGuard{journal: j, rootLockFD: rootLockFD, actionsFD: actionsFD, requestsFD: requestsFD,
		runHistoryFD: runHistoryFD, runFD: -1, lockFD: lockFD, localGuard: true}
	if err := guard.verifyNamespace(); err != nil {
		_ = guard.close()
		return nil, err
	}
	return guard, nil
}

func (j *Journal) verifyRunGenerationAuthority() error {
	if j == nil || j.runHistory == nil {
		return ErrIntegrity
	}
	authority, authorityData, found, err := readRootGenerationAuthority(j.rootFD, journalRunAuthorityXattr,
		journalRunAuthorityKind, j.rootDev, j.rootIno)
	if err != nil || !found {
		return ErrIntegrity
	}
	objects := rootGenerationObjectsForPin("actions", j.runHistory)
	var expectedData []byte
	switch authority.State {
	case rootGenerationEstablished:
		_, expectedData, err = makeRunGenerationAuthority(j.rootDev, j.rootIno, objects,
			authority.IssuanceCount, authority.IssuanceFinalRecordSHA256)
	case rootGenerationPrepared:
		if authority.PreparedRunGeneration == nil {
			return ErrIntegrity
		}
		_, expectedData, err = makePreparedRunGenerationAuthority(j.rootDev, j.rootIno, objects,
			authority.IssuanceCount, authority.IssuanceFinalRecordSHA256, *authority.PreparedRunGeneration)
	default:
		return ErrIntegrity
	}
	if err != nil || !bytes.Equal(authorityData, expectedData) {
		return ErrIntegrity
	}
	return nil
}

func (j *Journal) loadRunGenerationState() (runGenerationState, *os.File, syscall.Stat_t, error) {
	if j == nil || j.runHistory == nil || j.verifyNamespacePin(j.actions.fd, j.runHistory) != nil || j.verifyRunGenerationAuthority() != nil {
		return runGenerationState{}, nil, syscall.Stat_t{}, ErrIntegrity
	}
	authority, authorityData, found, err := readRootGenerationAuthority(j.rootFD, journalRunAuthorityXattr,
		journalRunAuthorityKind, j.rootDev, j.rootIno)
	if err != nil || !found || (authority.State != rootGenerationEstablished && authority.State != rootGenerationPrepared) {
		return runGenerationState{}, nil, syscall.Stat_t{}, ErrIntegrity
	}
	fd, err := j.openPinnedNamespace(j.actions.fd, j.runHistory)
	if err != nil {
		return runGenerationState{}, nil, syscall.Stat_t{}, err
	}
	history := os.NewFile(uintptr(fd), runGenerationHistoryName)
	var historyStat syscall.Stat_t
	if syscall.Fstat(fd, &historyStat) != nil || validateRunGenerationHistoryStat(historyStat) != nil {
		_ = history.Close()
		return runGenerationState{}, nil, syscall.Stat_t{}, ErrIntegrity
	}
	data, readErr := io.ReadAll(io.LimitReader(history, runGenerationHistoryMaxBytes+1))
	if readErr != nil || int64(len(data)) != historyStat.Size || int64(len(data)) > runGenerationHistoryMaxBytes ||
		verifyOpenAndNamedFile(j.actions.fd, runGenerationHistoryName, fd, j.runHistory.dev, j.runHistory.ino) != nil {
		_ = history.Close()
		return runGenerationState{}, nil, syscall.Stat_t{}, ErrIntegrity
	}
	state := runGenerationState{authority: authority, authorityData: authorityData,
		records: make(map[string]runGenerationV1, authority.IssuanceCount)}
	components := make(map[string]struct{}, authority.IssuanceCount)
	offset, prior := 0, ""
	for sequence := 1; sequence <= authority.IssuanceCount; sequence++ {
		relativeEnd := bytes.IndexByte(data[offset:], '\n')
		if relativeEnd < 1 || relativeEnd > runGenerationMaxBytes {
			_ = history.Close()
			return runGenerationState{}, nil, syscall.Stat_t{}, ErrIntegrity
		}
		line := data[offset : offset+relativeEnd]
		var record runGenerationV1
		if json.Unmarshal(line, &record) != nil || validateRunGeneration(record) != nil ||
			record.Sequence != sequence || record.PriorRecordSHA256 != prior {
			_ = history.Close()
			return runGenerationState{}, nil, syscall.Stat_t{}, ErrIntegrity
		}
		canonical, marshalErr := json.Marshal(record)
		if marshalErr != nil || !bytes.Equal(canonical, line) {
			_ = history.Close()
			return runGenerationState{}, nil, syscall.Stat_t{}, ErrIntegrity
		}
		if _, duplicate := state.records[record.RunID]; duplicate {
			_ = history.Close()
			return runGenerationState{}, nil, syscall.Stat_t{}, ErrIntegrity
		}
		if _, duplicate := components[record.StorageComponent]; duplicate {
			_ = history.Close()
			return runGenerationState{}, nil, syscall.Stat_t{}, ErrIntegrity
		}
		state.records[record.RunID] = record
		components[record.StorageComponent] = struct{}{}
		prior = sha256Hex(line)
		offset += relativeEnd + 1
	}
	if prior != authority.IssuanceFinalRecordSHA256 {
		_ = history.Close()
		return runGenerationState{}, nil, syscall.Stat_t{}, ErrIntegrity
	}
	state.committedBytes, state.lastDigest = int64(offset), prior
	state.tail = append([]byte(nil), data[offset:]...)
	return state, history, historyStat, nil
}

// reconcileRunGenerations recovers the one root-authorized prepared run
// generation before exposing it. The prepared authority binds the exact inode
// while it still has a private staging name, so recovery never infers an inode
// from an unregistered actions/<run> pathname.
func (j *Journal) reconcileRunGenerations(validateAll bool) (runGenerationState, error) {
	state, history, historyStat, err := j.loadRunGenerationState()
	if err != nil {
		return runGenerationState{}, err
	}
	defer history.Close()
	if state.authority.State == rootGenerationPrepared {
		state, err = j.recoverPreparedRunGeneration(state, history, historyStat)
		if err != nil {
			return runGenerationState{}, err
		}
	} else {
		if err := j.removeUnboundRunGenerationStage(); err != nil {
			return runGenerationState{}, err
		}
	}
	// Accept the legacy complete-record crash boundary. New issuances retain a
	// prepared authority through the history write, so a partial tail is never
	// accepted without its exact prepared inode proof.
	if len(state.tail) != 0 {
		if state.authority.IssuanceCount >= MaxJournalRuns || state.tail[len(state.tail)-1] != '\n' || bytes.Count(state.tail, []byte{'\n'}) != 1 {
			return runGenerationState{}, ErrIntegrity
		}
		line := state.tail[:len(state.tail)-1]
		if len(line) == 0 || len(line) > runGenerationMaxBytes {
			return runGenerationState{}, ErrIntegrity
		}
		var record runGenerationV1
		if json.Unmarshal(line, &record) != nil || validateRunGeneration(record) != nil ||
			record.Sequence != state.authority.IssuanceCount+1 || record.PriorRecordSHA256 != state.lastDigest {
			return runGenerationState{}, ErrIntegrity
		}
		canonical, marshalErr := json.Marshal(record)
		if marshalErr != nil || !bytes.Equal(canonical, line) {
			return runGenerationState{}, ErrIntegrity
		}
		if _, duplicate := state.records[record.RunID]; duplicate {
			return runGenerationState{}, ErrIntegrity
		}
		for _, existing := range state.records {
			if existing.StorageComponent == record.StorageComponent {
				return runGenerationState{}, ErrIntegrity
			}
		}
		fd, openErr := j.openRunGenerationDirectory(record)
		if openErr != nil {
			return runGenerationState{}, openErr
		}
		_ = syscall.Close(fd)
		nextDigest := sha256Hex(line)
		nextAuthority, nextData, makeErr := makeRunGenerationAuthority(j.rootDev, j.rootIno,
			rootGenerationObjectsForPin("actions", j.runHistory), record.Sequence, nextDigest)
		if makeErr != nil {
			return runGenerationState{}, makeErr
		}
		setErr := fsetRootXattr(j.rootFD, journalRunAuthorityXattr, nextData, rootGenerationXattrReplace)
		var syncErr error
		if setErr == nil {
			syncErr = j.io.syncDir("run-generation-authority-recover", j.rootFD)
		}
		observed, found, readErr := fgetRootXattr(j.rootFD, journalRunAuthorityXattr)
		if readErr != nil || !found || !bytes.Equal(observed, nextData) || setErr != nil || syncErr != nil {
			return runGenerationState{}, errors.Join(ErrIntegrity, setErr, syncErr, readErr)
		}
		state.authority, state.authorityData = nextAuthority, append([]byte(nil), nextData...)
		state.records[record.RunID] = record
		state.committedBytes += int64(len(state.tail))
		state.lastDigest, state.tail = nextDigest, nil
	}
	if validateAll {
		if err := j.validateAllRunGenerationDirectories(state.records); err != nil {
			return runGenerationState{}, err
		}
	}
	return state, nil
}

func (j *Journal) recoverPreparedRunGeneration(state runGenerationState, history *os.File, historyStat syscall.Stat_t) (runGenerationState, error) {
	if state.authority.State != rootGenerationPrepared || state.authority.PreparedRunGeneration == nil ||
		state.authority.IssuanceCount >= MaxJournalRuns {
		return runGenerationState{}, ErrIntegrity
	}
	record := *state.authority.PreparedRunGeneration
	line, err := marshalRunGeneration(record)
	if err != nil || record.Sequence != state.authority.IssuanceCount+1 || record.PriorRecordSHA256 != state.lastDigest {
		return runGenerationState{}, ErrIntegrity
	}
	if _, duplicate := state.records[record.RunID]; duplicate {
		return runGenerationState{}, ErrIntegrity
	}
	for _, existing := range state.records {
		if existing.StorageComponent == record.StorageComponent {
			return runGenerationState{}, ErrIntegrity
		}
	}
	stageFD, stageFound, err := j.openExactRunGenerationDirectory(runGenerationPreparedName, record)
	if err != nil {
		return runGenerationState{}, err
	}
	if stageFound {
		defer syscall.Close(stageFD)
	}
	finalFD, finalFound, err := j.openExactRunGenerationDirectory(record.StorageComponent, record)
	if err != nil {
		return runGenerationState{}, err
	}
	if finalFound {
		defer syscall.Close(finalFD)
	}
	if stageFound == finalFound {
		return runGenerationState{}, ErrIntegrity
	}
	boundFD := finalFD
	if stageFound {
		if err := requireEmptyDirectory(stageFD); err != nil {
			return runGenerationState{}, err
		}
		if err := syscall.Renameat(j.actions.fd, runGenerationPreparedName, j.actions.fd, record.StorageComponent); err != nil ||
			verifyOpenAndNamedDirectory(j.actions.fd, record.StorageComponent, stageFD, record.DirectoryDevice, record.DirectoryInode) != nil {
			return runGenerationState{}, ErrIntegrity
		}
		if err := j.io.syncDir("run-generation-directory-recover", j.actions.fd); err != nil {
			return runGenerationState{}, err
		}
		boundFD = stageFD
	}
	if err := requireEmptyDirectory(boundFD); err != nil {
		return runGenerationState{}, err
	}
	line = append(line, '\n')
	if len(state.tail) > len(line) || !bytes.Equal(state.tail, line[:len(state.tail)]) {
		return runGenerationState{}, ErrIntegrity
	}
	if !bytes.Equal(state.tail, line) {
		if err := j.io.truncate("run-generation-history-recover", history, state.committedBytes); err != nil {
			return runGenerationState{}, err
		}
		if _, err := history.Seek(state.committedBytes, io.SeekStart); err != nil {
			return runGenerationState{}, ErrIntegrity
		}
		if err := j.io.write("run-generation-history-recover", history, line); err != nil {
			return runGenerationState{}, err
		}
	}
	if err := j.io.syncFile("run-generation-history-recover", history); err != nil {
		return runGenerationState{}, err
	}
	var appended syscall.Stat_t
	if syscall.Fstat(int(history.Fd()), &appended) != nil || appended.Size != state.committedBytes+int64(len(line)) ||
		appended.Dev != historyStat.Dev || appended.Ino != historyStat.Ino ||
		verifyOpenAndNamedFile(j.actions.fd, runGenerationHistoryName, int(history.Fd()), uint64(historyStat.Dev), historyStat.Ino) != nil ||
		verifyOpenAndNamedDirectory(j.actions.fd, record.StorageComponent, boundFD, record.DirectoryDevice, record.DirectoryInode) != nil {
		return runGenerationState{}, ErrIntegrity
	}
	nextDigest := sha256Hex(line[:len(line)-1])
	nextAuthority, nextData, err := makeRunGenerationAuthority(j.rootDev, j.rootIno,
		rootGenerationObjectsForPin("actions", j.runHistory), record.Sequence, nextDigest)
	if err != nil {
		return runGenerationState{}, err
	}
	setErr := fsetRootXattr(j.rootFD, journalRunAuthorityXattr, nextData, rootGenerationXattrReplace)
	var syncErr error
	if setErr == nil {
		syncErr = j.io.syncDir("run-generation-authority-recover", j.rootFD)
	}
	observed, found, readErr := fgetRootXattr(j.rootFD, journalRunAuthorityXattr)
	if readErr != nil || !found || !bytes.Equal(observed, nextData) || setErr != nil || syncErr != nil {
		return runGenerationState{}, errors.Join(ErrIntegrity, setErr, syncErr, readErr)
	}
	state.authority, state.authorityData = nextAuthority, append([]byte(nil), nextData...)
	state.records[record.RunID] = record
	state.committedBytes += int64(len(line))
	state.lastDigest, state.tail = nextDigest, nil
	return state, nil
}

func marshalRunGeneration(record runGenerationV1) ([]byte, error) {
	line, err := json.Marshal(record)
	if err != nil || len(line) == 0 || len(line) > runGenerationMaxBytes || validateRunGeneration(record) != nil {
		return nil, ErrIntegrity
	}
	return line, nil
}

func (j *Journal) openExactRunGenerationDirectory(name string, record runGenerationV1) (int, bool, error) {
	fd, err := syscall.Openat(j.actions.fd, name,
		syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err == syscall.ENOENT {
		return -1, false, nil
	}
	if err != nil || validateDirectoryFD(fd, false) != nil {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return -1, false, ErrIntegrity
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || uint64(stat.Dev) != record.DirectoryDevice || stat.Ino != record.DirectoryInode {
		_ = syscall.Close(fd)
		return -1, false, ErrIntegrity
	}
	return fd, true, nil
}

func (j *Journal) removeUnboundRunGenerationStage() error {
	fd, err := syscall.Openat(j.actions.fd, runGenerationPreparedName,
		syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err == syscall.ENOENT {
		return nil
	}
	if err != nil || validateDirectoryFD(fd, false) != nil {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return ErrIntegrity
	}
	defer syscall.Close(fd)
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || requireEmptyDirectory(fd) != nil ||
		verifyOpenAndNamedDirectory(j.actions.fd, runGenerationPreparedName, fd, uint64(stat.Dev), stat.Ino) != nil {
		return ErrIntegrity
	}
	if err := removeDirectoryAt(j.actions.fd, runGenerationPreparedName); err != nil {
		return ErrIntegrity
	}
	if err := j.io.syncDir("run-generation-stage-recover", j.actions.fd); err != nil {
		return err
	}
	return nil
}

func requireEmptyDirectory(fd int) error {
	dup, err := syscall.Dup(fd)
	if err != nil {
		return ErrIntegrity
	}
	directory := os.NewFile(uintptr(dup), "empty-run-generation")
	names, readErr := directory.Readdirnames(1)
	closeErr := directory.Close()
	if (readErr != nil && readErr != io.EOF) || closeErr != nil || len(names) != 0 {
		return ErrIntegrity
	}
	return nil
}

func validateRunGeneration(record runGenerationV1) error {
	if record.Kind != "ActionRunGenerationV1" || record.SchemaVersion != 1 || record.Sequence < 1 ||
		runtimeIdentifier(record.RunID) != nil || record.StorageComponent != storageComponent(record.RunID) ||
		!validStorageComponent(record.StorageComponent) || record.DirectoryDevice == 0 || record.DirectoryInode == 0 ||
		(record.Sequence == 1 && record.PriorRecordSHA256 != "") || (record.Sequence > 1 && !validDigest(record.PriorRecordSHA256)) {
		return ErrIntegrity
	}
	return nil
}

func validateRunGenerationHistoryStat(stat syscall.Stat_t) error {
	if validateProtectedFileStat(stat) != nil || stat.Size < 0 || stat.Size > runGenerationHistoryMaxBytes {
		return ErrIntegrity
	}
	return nil
}

func (j *Journal) openRunGenerationDirectory(record runGenerationV1) (int, error) {
	fd, err := syscall.Openat(j.actions.fd, record.StorageComponent,
		syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil || validateDirectoryFD(fd, false) != nil {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return -1, ErrIntegrity
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || uint64(stat.Dev) != record.DirectoryDevice || stat.Ino != record.DirectoryInode {
		_ = syscall.Close(fd)
		return -1, ErrIntegrity
	}
	return fd, nil
}

func (j *Journal) validateAllRunGenerationDirectories(records map[string]runGenerationV1) error {
	if err := j.validateCommittedRunGenerationDirectories(records); err != nil {
		return err
	}
	components := make(map[string]runGenerationV1, len(records))
	for _, record := range records {
		components[record.StorageComponent] = record
	}
	dup, err := syscall.Dup(j.actions.fd)
	if err != nil {
		return ErrIntegrity
	}
	directory := os.NewFile(uintptr(dup), "action-run-generation-directory")
	entries, readErr := directory.ReadDir(MaxJournalRuns + 7)
	closeErr := directory.Close()
	if (readErr != nil && readErr != io.EOF) || closeErr != nil || len(entries) > MaxJournalRuns+6 {
		return ErrIntegrity
	}
	allowed := map[string]struct{}{
		".lock": {}, lockIdentityName: {}, requestIndexDirectory: {}, requestIndexIdentityName: {},
		runGenerationHistoryName: {}, runGenerationIdentityName: {},
	}
	seen := make(map[string]struct{}, len(records))
	for _, entry := range entries {
		name := entry.Name()
		if _, ok := allowed[name]; ok {
			continue
		}
		record, ok := components[name]
		if !ok {
			return ErrIntegrity
		}
		if _, duplicate := seen[name]; duplicate {
			return ErrIntegrity
		}
		fd, openErr := j.openRunGenerationDirectory(record)
		if openErr != nil {
			return openErr
		}
		_ = syscall.Close(fd)
		seen[name] = struct{}{}
	}
	if len(seen) != len(records) {
		return ErrIntegrity
	}
	return nil
}

func (j *Journal) validateCommittedRunGenerationDirectories(records map[string]runGenerationV1) error {
	for _, record := range records {
		fd, err := j.openRunGenerationDirectory(record)
		if err != nil {
			return err
		}
		if err := syscall.Close(fd); err != nil {
			return ErrIntegrity
		}
	}
	return nil
}

func (g *journalGuard) openRun(runID string, create bool) error {
	if g == nil || g.runFD >= 0 || runtimeIdentifier(runID) != nil {
		return ErrIntegrity
	}
	if err := g.verifyNamespace(); err != nil {
		return err
	}
	state, err := g.journal.reconcileRunGenerations(false)
	if err != nil {
		return err
	}
	record, found := state.records[runID]
	var fd int
	if found {
		fd, err = g.openExactRunDirectory(record)
	} else if create {
		fd, record, state.authorityData, err = g.registerRunGeneration(runID, state)
	} else {
		component := storageComponent(runID)
		probe, probeErr := syscall.Openat(g.actionsFD, component,
			syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if probeErr == nil {
			_ = syscall.Close(probe)
			return ErrIntegrity
		}
		if probeErr != syscall.ENOENT {
			return ErrIntegrity
		}
		return ErrNotFound
	}
	if err != nil {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return err
	}
	g.runID, g.runFD, g.runDev, g.runIno = runID, fd, record.DirectoryDevice, record.DirectoryInode
	g.runAuthorityData = append([]byte(nil), state.authorityData...)
	if err := g.verifyNamespace(); err != nil {
		_ = syscall.Close(fd)
		g.runFD = -1
		return err
	}
	return nil
}

func (g *journalGuard) openExactRunDirectory(record runGenerationV1) (int, error) {
	fd, err := syscall.Openat(g.actionsFD, record.StorageComponent,
		syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil || validateDirectoryFD(fd, false) != nil {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return -1, ErrIntegrity
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || uint64(stat.Dev) != record.DirectoryDevice || stat.Ino != record.DirectoryInode {
		_ = syscall.Close(fd)
		return -1, ErrIntegrity
	}
	return fd, nil
}

func (g *journalGuard) registerRunGeneration(runID string, expected runGenerationState) (int, runGenerationV1, []byte, error) {
	state, history, historyStat, err := g.journal.loadRunGenerationState()
	if err != nil {
		return -1, runGenerationV1{}, nil, err
	}
	defer history.Close()
	if state.authority.State != rootGenerationEstablished || len(state.tail) != 0 || !bytes.Equal(state.authorityData, expected.authorityData) ||
		state.committedBytes != expected.committedBytes || len(state.records) != len(expected.records) {
		return -1, runGenerationV1{}, nil, ErrIntegrity
	}
	if state.authority.IssuanceCount >= MaxJournalRuns {
		return -1, runGenerationV1{}, nil, ErrExhausted
	}
	if _, duplicate := state.records[runID]; duplicate {
		return -1, runGenerationV1{}, nil, ErrIntegrity
	}
	component := storageComponent(runID)
	for _, record := range state.records {
		if record.StorageComponent == component {
			return -1, runGenerationV1{}, nil, ErrIntegrity
		}
	}
	probe, probeErr := syscall.Openat(g.actionsFD, component,
		syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if probeErr == nil {
		_ = syscall.Close(probe)
		return -1, runGenerationV1{}, nil, ErrIntegrity
	}
	if probeErr != syscall.ENOENT {
		return -1, runGenerationV1{}, nil, ErrIntegrity
	}
	probe, probeErr = syscall.Openat(g.actionsFD, runGenerationPreparedName,
		syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if probeErr == nil {
		_ = syscall.Close(probe)
		return -1, runGenerationV1{}, nil, ErrIntegrity
	}
	if probeErr != syscall.ENOENT {
		return -1, runGenerationV1{}, nil, ErrIntegrity
	}
	if err := syscall.Mkdirat(g.actionsFD, runGenerationPreparedName, 0o700); err != nil {
		return -1, runGenerationV1{}, nil, ErrIntegrity
	}
	fd, err := syscall.Openat(g.actionsFD, runGenerationPreparedName,
		syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil || validateDirectoryFD(fd, false) != nil {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return -1, runGenerationV1{}, nil, ErrIntegrity
	}
	var directoryStat syscall.Stat_t
	if syscall.Fstat(fd, &directoryStat) != nil {
		_ = syscall.Close(fd)
		return -1, runGenerationV1{}, nil, ErrIntegrity
	}
	cleanupUnboundStage := func(cause error) (int, runGenerationV1, []byte, error) {
		nameErr := verifyOpenAndNamedDirectory(g.actionsFD, runGenerationPreparedName, fd,
			uint64(directoryStat.Dev), directoryStat.Ino)
		emptyErr := requireEmptyDirectory(fd)
		removeErr := error(nil)
		if nameErr == nil && emptyErr == nil {
			removeErr = removeDirectoryAt(g.actionsFD, runGenerationPreparedName)
		}
		syncDirErr := g.journal.io.syncDir("run-generation-stage-rollback", g.actionsFD)
		closeErr := syscall.Close(fd)
		if nameErr != nil || emptyErr != nil || removeErr != nil || syncDirErr != nil || closeErr != nil {
			g.journal.failedClosed.Store(true)
			g.retainLock = true
			return -1, runGenerationV1{}, nil, errors.Join(ErrIntegrity, cause, nameErr, emptyErr, removeErr, syncDirErr, closeErr)
		}
		return -1, runGenerationV1{}, nil, cause
	}
	if err := g.journal.io.syncDir("directory", g.actionsFD); err != nil {
		return cleanupUnboundStage(err)
	}
	record := runGenerationV1{Kind: "ActionRunGenerationV1", SchemaVersion: 1,
		Sequence: state.authority.IssuanceCount + 1, PriorRecordSHA256: state.lastDigest,
		RunID: runID, StorageComponent: component, DirectoryDevice: uint64(directoryStat.Dev), DirectoryInode: directoryStat.Ino}
	line, marshalErr := marshalRunGeneration(record)
	if marshalErr != nil {
		return cleanupUnboundStage(marshalErr)
	}
	line = append(line, '\n')
	_, preparedData, err := makePreparedRunGenerationAuthority(g.journal.rootDev, g.journal.rootIno,
		rootGenerationObjectsForPin("actions", g.journal.runHistory), state.authority.IssuanceCount,
		state.authority.IssuanceFinalRecordSHA256, record)
	if err != nil {
		return cleanupUnboundStage(err)
	}
	setErr := fsetRootXattr(g.journal.rootFD, journalRunAuthorityXattr, preparedData, rootGenerationXattrReplace)
	var syncErr error
	if setErr == nil {
		syncErr = g.journal.io.syncDir("run-generation-prepare-authority", g.journal.rootFD)
	}
	observed, found, readErr := fgetRootXattr(g.journal.rootFD, journalRunAuthorityXattr)
	if readErr != nil || !found {
		g.journal.failedClosed.Store(true)
		g.retainLock = true
		_ = syscall.Close(fd)
		return -1, runGenerationV1{}, nil, errors.Join(ErrIntegrity, setErr, syncErr, readErr)
	}
	if bytes.Equal(observed, state.authorityData) {
		if setErr == nil && syncErr == nil {
			g.journal.failedClosed.Store(true)
			g.retainLock = true
			_ = syscall.Close(fd)
			return -1, runGenerationV1{}, nil, ErrIntegrity
		}
		return cleanupUnboundStage(errors.Join(setErr, syncErr))
	}
	if !bytes.Equal(observed, preparedData) {
		g.journal.failedClosed.Store(true)
		g.retainLock = true
		_ = syscall.Close(fd)
		return -1, runGenerationV1{}, nil, errors.Join(ErrIntegrity, setErr, syncErr)
	}
	if setErr != nil || syncErr != nil {
		_ = syscall.Close(fd)
		return -1, runGenerationV1{}, nil, errors.Join(setErr, syncErr)
	}
	if err := syscall.Renameat(g.actionsFD, runGenerationPreparedName, g.actionsFD, component); err != nil {
		_ = syscall.Close(fd)
		return -1, runGenerationV1{}, nil, err
	}
	if verifyOpenAndNamedDirectory(g.actionsFD, component, fd, uint64(directoryStat.Dev), directoryStat.Ino) != nil {
		_ = syscall.Close(fd)
		return -1, runGenerationV1{}, nil, ErrIntegrity
	}
	if err := g.journal.io.syncDir("run-generation-directory", g.actionsFD); err != nil {
		_ = syscall.Close(fd)
		return -1, runGenerationV1{}, nil, err
	}
	if _, err := history.Seek(state.committedBytes, io.SeekStart); err != nil {
		_ = syscall.Close(fd)
		return -1, runGenerationV1{}, nil, ErrIntegrity
	}
	if err := g.journal.io.write("run-generation-history", history, line); err != nil {
		_ = syscall.Close(fd)
		return -1, runGenerationV1{}, nil, err
	}
	if err := g.journal.io.syncFile("run-generation-history", history); err != nil {
		_ = syscall.Close(fd)
		return -1, runGenerationV1{}, nil, err
	}
	var appended syscall.Stat_t
	if syscall.Fstat(int(history.Fd()), &appended) != nil || appended.Size != state.committedBytes+int64(len(line)) ||
		appended.Dev != historyStat.Dev || appended.Ino != historyStat.Ino ||
		verifyOpenAndNamedFile(g.actionsFD, runGenerationHistoryName, int(history.Fd()), uint64(historyStat.Dev), historyStat.Ino) != nil ||
		verifyOpenAndNamedDirectory(g.actionsFD, component, fd, uint64(directoryStat.Dev), directoryStat.Ino) != nil {
		_ = syscall.Close(fd)
		return -1, runGenerationV1{}, nil, ErrIntegrity
	}
	nextDigest := sha256Hex(line[:len(line)-1])
	_, nextData, err := makeRunGenerationAuthority(g.journal.rootDev, g.journal.rootIno,
		rootGenerationObjectsForPin("actions", g.journal.runHistory), record.Sequence, nextDigest)
	if err != nil {
		_ = syscall.Close(fd)
		return -1, runGenerationV1{}, nil, err
	}
	setErr = fsetRootXattr(g.journal.rootFD, journalRunAuthorityXattr, nextData, rootGenerationXattrReplace)
	syncErr = nil
	if setErr == nil {
		syncErr = g.journal.io.syncDir("run-generation-authority", g.journal.rootFD)
	}
	observed, found, readErr = fgetRootXattr(g.journal.rootFD, journalRunAuthorityXattr)
	if readErr == nil && found && bytes.Equal(observed, nextData) {
		if setErr != nil || syncErr != nil {
			_ = syscall.Close(fd)
			return -1, runGenerationV1{}, nil, errors.Join(setErr, syncErr)
		}
		return fd, record, nextData, nil
	}
	if readErr == nil && found && bytes.Equal(observed, preparedData) && (setErr != nil || syncErr != nil) {
		_ = syscall.Close(fd)
		return -1, runGenerationV1{}, nil, errors.Join(setErr, syncErr)
	}
	g.journal.failedClosed.Store(true)
	g.retainLock = true
	_ = syscall.Close(fd)
	return -1, runGenerationV1{}, nil, errors.Join(ErrIntegrity, setErr, syncErr, readErr)
}

func (g *journalGuard) verifyRunNamespace() error {
	if g.runFD < 0 || g.runDev == 0 || g.runIno == 0 || runtimeIdentifier(g.runID) != nil || len(g.runAuthorityData) == 0 {
		return ErrIntegrity
	}
	if err := verifyRootGenerationData(g.journal.rootFD, journalRunAuthorityXattr, g.runAuthorityData); err != nil {
		return err
	}
	return verifyOpenAndNamedDirectory(g.actionsFD, storageComponent(g.runID), g.runFD, g.runDev, g.runIno)
}

func (g *journalGuard) close() error {
	if g == nil {
		return nil
	}
	var namespaceErr error
	if g.rootLockFD >= 0 {
		namespaceErr = g.verifyNamespace()
		if namespaceErr != nil && g.journal != nil {
			g.journal.failedClosed.Store(true)
		}
	}
	var runErr error
	if g.runFD >= 0 {
		runErr = syscall.Close(g.runFD)
		g.runFD = -1
	}
	var lockErr error
	if g.lockFD >= 0 {
		if g.retainLock {
			if !g.journal.poisonLockFD.CompareAndSwap(0, int64(g.lockFD)+1) {
				lockErr = errors.Join(ErrIntegrity, syscall.Close(g.lockFD))
			}
		} else {
			lockErr = errors.Join(syscall.Flock(g.lockFD, syscall.LOCK_UN), syscall.Close(g.lockFD))
		}
		g.lockFD = -1
	}
	var requestsErr error
	if g.requestsFD >= 0 {
		requestsErr = syscall.Close(g.requestsFD)
		g.requestsFD = -1
	}
	var runHistoryErr error
	if g.runHistoryFD >= 0 {
		runHistoryErr = syscall.Close(g.runHistoryFD)
		g.runHistoryFD = -1
	}
	var actionsErr error
	if g.actionsFD >= 0 {
		actionsErr = syscall.Close(g.actionsFD)
		g.actionsFD = -1
	}
	var rootLockErr error
	if g.rootLockFD >= 0 {
		rootLockErr = errors.Join(syscall.Flock(g.rootLockFD, syscall.LOCK_UN), syscall.Close(g.rootLockFD))
		g.rootLockFD = -1
	}
	if g.localGuard {
		g.journal.localGuard <- struct{}{}
		g.localGuard = false
	}
	return errors.Join(namespaceErr, lockErr, runErr, runHistoryErr, requestsErr, actionsErr, rootLockErr)
}

func (g *journalGuard) scan() (*journalState, error) {
	if g == nil || g.runFD < 0 {
		return nil, ErrIntegrity
	}
	if err := g.verifyNamespace(); err != nil {
		return nil, err
	}
	if err := g.recoverPendingAppend(); err != nil {
		return nil, err
	}
	state := &journalState{operations: map[string]*Operation{}, lookups: map[string]string{}, decisions: map[string]string{}}
	dup, err := syscall.Dup(g.runFD)
	if err != nil {
		return nil, ErrIntegrity
	}
	directory := os.NewFile(uintptr(dup), "action-run-directory")
	entries, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return nil, ErrIntegrity
	}
	var epochs []int
	for _, entry := range entries {
		name := entry.Name()
		if name == ".lock" {
			continue
		}
		match := segmentNamePattern.FindStringSubmatch(name)
		if match == nil || entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil, ErrIntegrity
		}
		var epoch int
		if _, err := fmt.Sscanf(match[1], "%d", &epoch); err != nil || epoch < 1 || epoch > MaxJournalEpochs {
			return nil, ErrIntegrity
		}
		epochs = append(epochs, epoch)
	}
	sort.Ints(epochs)
	for index, epoch := range epochs {
		if epoch != index+1 {
			return nil, ErrIntegrity
		}
		segment, records, err := g.readSegment(uint64(epoch))
		if err != nil {
			return nil, err
		}
		if len(records) == 0 && index != len(epochs)-1 {
			return nil, ErrIntegrity
		}
		if len(records) == 0 {
			segment.empty = true
			state.segments = append(state.segments, segment)
			continue
		}
		for recordIndex, record := range records {
			if record.epoch != uint64(epoch) || record.sequence != state.sequence+1 {
				return nil, ErrIntegrity
			}
			if recordIndex == 0 && epoch > 1 {
				if record.prior == "" || len(state.segments) == 0 || record.prior != state.segments[len(state.segments)-1].lastDigest {
					return nil, ErrIntegrity
				}
			} else if record.prior != "" {
				return nil, ErrIntegrity
			}
			state.sequence, state.total = record.sequence, state.total+1
			if state.total > MaxJournalRecords {
				return nil, ErrIntegrity
			}
			if err := applyDecoded(state, record); err != nil {
				return nil, err
			}
		}
		state.segments = append(state.segments, segment)
	}
	for _, operation := range state.operations {
		if err := validateProgress(*operation); err != nil {
			return nil, err
		}
		status := StatusReceived
		if operation.Claim != nil {
			status = StatusClaimed
		}
		for _, outcome := range operation.Outcomes {
			status = outcome.Status
		}
		operation.Status = status
	}
	if err := g.verifyNamespace(); err != nil {
		return nil, err
	}
	return state, nil
}

func (g *journalGuard) readSegment(epoch uint64) (segmentState, []decodedRecord, error) {
	name := segmentName(epoch)
	fd, err := syscall.Openat(g.runFD, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return segmentState{}, nil, ErrIntegrity
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || validateFileInfo(info) != nil || info.Size() > MaxSegmentBytes {
		return segmentState{}, nil, ErrIntegrity
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxSegmentBytes+1))
	if err != nil || len(data) > MaxSegmentBytes || (len(data) > 0 && data[len(data)-1] != '\n') {
		return segmentState{}, nil, ErrIntegrity
	}
	segment := segmentState{epoch: epoch, bytes: int64(len(data))}
	if len(data) == 0 {
		return segment, nil, nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 256<<10)
	var records []decodedRecord
	for scanner.Scan() {
		if len(records) == MaxSegmentRecords {
			return segmentState{}, nil, ErrIntegrity
		}
		line := append([]byte(nil), scanner.Bytes()...)
		record, err := decodeRecord(line)
		if err != nil {
			return segmentState{}, nil, err
		}
		records = append(records, record)
		sum := sha256.Sum256(line)
		segment.lastDigest = hex.EncodeToString(sum[:])
	}
	if scanner.Err() != nil {
		return segmentState{}, nil, ErrIntegrity
	}
	segment.records = len(records)
	return segment, records, nil
}

func decodeRecord(line []byte) (decodedRecord, error) {
	var header struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(line, &header); err != nil {
		return decodedRecord{}, ErrIntegrity
	}
	var record decodedRecord
	switch header.Kind {
	case "ActionReceiptV1":
		var value ActionReceiptV1
		if err := json.Unmarshal(line, &value); err != nil || validateReceipt(value) != nil {
			return record, ErrIntegrity
		}
		canonical, _ := json.Marshal(value)
		if !bytes.Equal(canonical, line) {
			return record, ErrIntegrity
		}
		record = decodedRecord{kind: value.Kind, epoch: value.Epoch, sequence: value.Sequence, prior: value.PriorSegmentFinalRecordSHA256, operation: value.OperationID, receipt: &value}
	case "ActionClaimV1":
		var value ActionClaimV1
		if err := json.Unmarshal(line, &value); err != nil || validateClaim(value) != nil {
			return record, ErrIntegrity
		}
		canonical, _ := json.Marshal(value)
		if !bytes.Equal(canonical, line) {
			return record, ErrIntegrity
		}
		record = decodedRecord{kind: value.Kind, epoch: value.Epoch, sequence: value.Sequence, prior: value.PriorSegmentFinalRecordSHA256, operation: value.OperationID, claim: &value}
	case "ActionOutcomeV1":
		var value ActionOutcomeV1
		if err := json.Unmarshal(line, &value); err != nil || validateOutcome(value) != nil {
			return record, ErrIntegrity
		}
		canonical, _ := json.Marshal(value)
		if !bytes.Equal(canonical, line) {
			return record, ErrIntegrity
		}
		record = decodedRecord{kind: value.Kind, epoch: value.Epoch, sequence: value.Sequence, prior: value.PriorSegmentFinalRecordSHA256, operation: value.OperationID, outcome: &value}
	default:
		return record, ErrIntegrity
	}
	return record, nil
}

func applyDecoded(state *journalState, record decodedRecord) error {
	switch record.kind {
	case "ActionReceiptV1":
		value := *record.receipt
		if _, exists := state.operations[value.OperationID]; exists {
			return ErrIntegrity
		}
		if _, exists := state.lookups[value.LookupKeySHA256]; exists {
			return ErrIntegrity
		}
		if value.LookupKeySHA256 != LookupKey(value.PrincipalID, value.RequestID) {
			return ErrIntegrity
		}
		state.operations[value.OperationID] = &Operation{Receipt: value, Status: StatusReceived}
		state.lookups[value.LookupKeySHA256] = value.OperationID
		if value.Action == "decision" {
			decisionRequestID, err := journalDecisionRequestID(value.Payload)
			if err != nil {
				return err
			}
			if _, duplicate := state.decisions[decisionRequestID]; duplicate {
				return ErrIntegrity
			}
			state.decisions[decisionRequestID] = value.OperationID
		}
	case "ActionClaimV1":
		operation := state.operations[record.operation]
		if operation == nil || operation.Claim != nil || operation.Status != StatusReceived || record.claim.AdmittedStateTransitionID != operation.Receipt.AdmittedStateTransitionID || record.claim.OwnerLeaseID != operation.Receipt.OwnerLeaseID {
			return ErrIntegrity
		}
		value := *record.claim
		operation.Claim, operation.Status = &value, StatusClaimed
	case "ActionOutcomeV1":
		operation := state.operations[record.operation]
		if operation == nil {
			return ErrIntegrity
		}
		operation.Outcomes = append(operation.Outcomes, *record.outcome)
		operation.Status = record.outcome.Status
	}
	return nil
}

func (g *journalGuard) append(state *journalState, value any) error {
	if err := g.verifyNamespace(); err != nil {
		return err
	}
	name, currentBytes, line, err := prepareAppend(state, value)
	if err != nil {
		return err
	}
	pending := appendPendingV1{Kind: "ActionAppendPendingV1", SchemaVersion: 1, SegmentName: name, PreAppendSize: currentBytes}
	if err := g.createAppendPending(name, currentBytes); err != nil {
		return err
	}
	flags := syscall.O_RDWR | syscall.O_APPEND | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	created := false
	fd, err := syscall.Openat(g.runFD, name, flags, 0)
	if err == syscall.ENOENT {
		fd, err = syscall.Openat(g.runFD, name, flags|syscall.O_CREAT|syscall.O_EXCL, 0o600)
		created = err == nil
	}
	if err != nil {
		return errors.Join(ErrIntegrity, g.recoverPendingAppend())
	}
	file := os.NewFile(uintptr(fd), name)
	var initial syscall.Stat_t
	if syscall.Fstat(fd, &initial) != nil || validateProtectedFileStat(initial) != nil || initial.Size != currentBytes {
		_ = file.Close()
		return errors.Join(ErrIntegrity, g.recoverPendingAppend())
	}
	if err := g.journal.io.write("journal-segment", file, line); err != nil {
		_ = file.Close()
		return errors.Join(err, g.recoverPendingAppend())
	}
	if err := g.journal.io.syncFile("journal-segment", file); err != nil {
		_ = file.Close()
		return errors.Join(err, g.recoverPendingAppend())
	}
	var appended syscall.Stat_t
	if syscall.Fstat(fd, &appended) != nil || appended.Size != currentBytes+int64(len(line)) {
		_ = file.Close()
		return errors.Join(ErrIntegrity, g.recoverPendingAppend())
	}
	if err := verifyOpenAndNamedFile(g.runFD, name, fd, initial.Dev, initial.Ino); err != nil {
		_ = file.Close()
		return errors.Join(err, g.recoverPendingAppend())
	}
	if created {
		if err := g.journal.io.syncDir("journal-segment", g.runFD); err != nil {
			_ = file.Close()
			return errors.Join(err, g.recoverPendingAppend())
		}
	}
	if err := file.Close(); err != nil {
		return errors.Join(err, g.recoverPendingAppend())
	}
	if err := g.journal.io.removeAt("append-commit", g.runFD, appendPendingName); err != nil {
		return g.resolveAmbiguousAppend(pending, err)
	}
	if err := g.journal.io.syncDir("append-commit", g.runFD); err != nil {
		return g.resolveAmbiguousAppend(pending, err)
	}
	return g.verifyNamespace()
}

// resolveAmbiguousAppend runs while the service-wide action lock is held. A
// failed marker removal or directory sync cannot be reported to the caller
// until the appended bytes have been durably rolled back or the rollback
// intent has again been made durable. If neither proof succeeds, this exact
// Journal instance is permanently failed closed.
func (g *journalGuard) resolveAmbiguousAppend(pending appendPendingV1, cause error) error {
	rollbackErr := g.rollbackPendingAppend(pending, false)
	if rollbackErr == nil {
		return cause
	}
	markerErr := g.restoreAppendPending(pending)
	if markerErr == nil {
		return errors.Join(cause, rollbackErr)
	}
	g.journal.failedClosed.Store(true)
	g.retainLock = true
	return errors.Join(ErrIntegrity, cause, rollbackErr, markerErr)
}

func prepareAppend(state *journalState, value any) (string, int64, []byte, error) {
	if state.total >= MaxJournalRecords {
		return "", 0, nil, ErrExhausted
	}
	epoch := uint64(1)
	prior := ""
	if len(state.segments) > 0 {
		epoch = state.segments[len(state.segments)-1].epoch
	}
	initialPrior := ""
	if len(state.segments) > 1 && state.segments[len(state.segments)-1].records == 0 {
		initialPrior = state.segments[len(state.segments)-2].lastDigest
	}
	setEnvelope(value, epoch, state.sequence+1, initialPrior)
	if err := validateAppendValue(value); err != nil {
		return "", 0, nil, err
	}
	line, err := json.Marshal(value)
	if err != nil {
		return "", 0, nil, err
	}
	line = append(line, '\n')
	currentBytes, currentRecords := int64(0), 0
	if len(state.segments) > 0 {
		currentBytes, currentRecords = state.segments[len(state.segments)-1].bytes, state.segments[len(state.segments)-1].records
	}
	if currentRecords >= MaxSegmentRecords || currentBytes+int64(len(line)) > MaxSegmentBytes {
		if epoch >= MaxJournalEpochs {
			return "", 0, nil, ErrExhausted
		}
		if len(state.segments) == 0 || state.segments[len(state.segments)-1].lastDigest == "" {
			return "", 0, nil, ErrIntegrity
		}
		epoch++
		prior = state.segments[len(state.segments)-1].lastDigest
		setEnvelope(value, epoch, state.sequence+1, prior)
		if err := validateAppendValue(value); err != nil {
			return "", 0, nil, err
		}
		line, err = json.Marshal(value)
		if err != nil {
			return "", 0, nil, err
		}
		line = append(line, '\n')
		currentBytes, currentRecords = 0, 0
	}
	if len(line) > MaxSegmentBytes {
		return "", 0, nil, ErrExhausted
	}
	name := segmentName(epoch)
	return name, currentBytes, line, nil
}

func (g *journalGuard) createAppendPending(segment string, size int64) error {
	value := appendPendingV1{Kind: "ActionAppendPendingV1", SchemaVersion: 1, SegmentName: segment, PreAppendSize: size}
	data, err := json.Marshal(value)
	if err != nil || len(data) > appendPendingMaxBytes {
		return ErrIntegrity
	}
	fd, err := syscall.Openat(g.runFD, appendPendingName, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err == syscall.EEXIST {
		if recoverErr := g.recoverPendingAppend(); recoverErr != nil {
			return recoverErr
		}
		fd, err = syscall.Openat(g.runFD, appendPendingName, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	}
	if err != nil || validateFileFD(fd) != nil {
		if fd >= 0 {
			syscall.Close(fd)
		}
		return ErrIntegrity
	}
	file := os.NewFile(uintptr(fd), appendPendingName)
	var initial syscall.Stat_t
	if syscall.Fstat(fd, &initial) != nil {
		_ = file.Close()
		return ErrIntegrity
	}
	fail := func(cause error) error {
		_ = file.Close()
		removeErr := g.journal.io.removeAt("append-pending-cleanup", g.runFD, appendPendingName)
		syncErr := g.journal.io.syncDir("append-pending-cleanup", g.runFD)
		return errors.Join(cause, removeErr, syncErr)
	}
	if err := g.journal.io.write("append-pending", file, data); err != nil {
		return fail(err)
	}
	if err := g.journal.io.syncFile("append-pending", file); err != nil {
		return fail(err)
	}
	var written syscall.Stat_t
	observed := make([]byte, len(data))
	if syscall.Fstat(fd, &written) != nil || written.Size != int64(len(data)) {
		return fail(ErrIntegrity)
	}
	if count, readErr := file.ReadAt(observed, 0); readErr != nil || count != len(data) || !bytes.Equal(observed, data) {
		return fail(ErrIntegrity)
	}
	if err := verifyOpenAndNamedFile(g.runFD, appendPendingName, fd, initial.Dev, initial.Ino); err != nil {
		return fail(err)
	}
	if err := g.journal.io.syncDir("append-pending", g.runFD); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := verifyOpenAndNamedFile(g.runFD, appendPendingName, fd, initial.Dev, initial.Ino); err != nil {
		return fail(err)
	}
	return file.Close()
}

// restoreAppendPending durably restores (or re-proves) the rollback marker
// after an ambiguous commit. It deliberately leaves any partially restored
// marker in place on error: a malformed marker makes scans fail closed, while
// its removal could expose the unproven appended bytes.
func (g *journalGuard) restoreAppendPending(pending appendPendingV1) error {
	data, err := json.Marshal(pending)
	if err != nil || len(data) > appendPendingMaxBytes || validateAppendPending(pending, data) != nil {
		return ErrIntegrity
	}
	flags := syscall.O_RDWR | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	created := false
	fd, err := syscall.Openat(g.runFD, appendPendingName, flags, 0)
	if err == syscall.ENOENT {
		fd, err = syscall.Openat(g.runFD, appendPendingName, flags|syscall.O_CREAT|syscall.O_EXCL, 0o600)
		created = err == nil
	}
	if err != nil || validateFileFD(fd) != nil {
		if fd >= 0 {
			syscall.Close(fd)
		}
		return ErrIntegrity
	}
	file := os.NewFile(uintptr(fd), appendPendingName)
	var initial syscall.Stat_t
	if syscall.Fstat(fd, &initial) != nil {
		_ = file.Close()
		return ErrIntegrity
	}
	if created {
		if err := g.journal.io.write("append-recovery-marker", file, data); err != nil {
			_ = file.Close()
			return err
		}
	} else {
		observed := make([]byte, len(data))
		if initial.Size != int64(len(data)) {
			_ = file.Close()
			return ErrIntegrity
		}
		if count, readErr := file.ReadAt(observed, 0); readErr != nil || count != len(data) || !bytes.Equal(observed, data) {
			_ = file.Close()
			return ErrIntegrity
		}
	}
	if err := g.journal.io.syncFile("append-recovery-marker", file); err != nil {
		_ = file.Close()
		return err
	}
	var written syscall.Stat_t
	observed := make([]byte, len(data))
	if syscall.Fstat(fd, &written) != nil || written.Size != int64(len(data)) {
		_ = file.Close()
		return ErrIntegrity
	}
	if count, readErr := file.ReadAt(observed, 0); readErr != nil || count != len(data) || !bytes.Equal(observed, data) {
		_ = file.Close()
		return ErrIntegrity
	}
	if err := verifyOpenAndNamedFile(g.runFD, appendPendingName, fd, initial.Dev, initial.Ino); err != nil {
		_ = file.Close()
		return err
	}
	if err := g.journal.io.syncDir("append-recovery-marker", g.runFD); err != nil {
		_ = file.Close()
		return err
	}
	if err := verifyOpenAndNamedFile(g.runFD, appendPendingName, fd, initial.Dev, initial.Ino); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (g *journalGuard) recoverPendingAppend() error {
	fd, err := syscall.Openat(g.runFD, appendPendingName, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err == syscall.ENOENT {
		return nil
	}
	if err != nil || validateFileFD(fd) != nil {
		if fd >= 0 {
			syscall.Close(fd)
		}
		return ErrIntegrity
	}
	file := os.NewFile(uintptr(fd), appendPendingName)
	data, readErr := io.ReadAll(io.LimitReader(file, appendPendingMaxBytes+1))
	var initial syscall.Stat_t
	statErr := syscall.Fstat(fd, &initial)
	identityErr := verifyOpenAndNamedFile(g.runFD, appendPendingName, fd, initial.Dev, initial.Ino)
	closeErr := file.Close()
	if readErr != nil || statErr != nil || identityErr != nil || closeErr != nil || len(data) > appendPendingMaxBytes {
		return ErrIntegrity
	}
	var pending appendPendingV1
	if json.Unmarshal(data, &pending) != nil {
		return ErrIntegrity
	}
	if validateAppendPending(pending, data) != nil {
		return ErrIntegrity
	}
	if err := g.rollbackPendingAppend(pending, true); err != nil {
		return err
	}
	if err := g.journal.io.removeAt("append-rollback", g.runFD, appendPendingName); err != nil {
		return err
	}
	if err := g.journal.io.syncDir("append-rollback", g.runFD); err != nil {
		return err
	}
	return nil
}

func validateAppendPending(pending appendPendingV1, data []byte) error {
	canonical, err := json.Marshal(pending)
	if err != nil || !bytes.Equal(canonical, data) || pending.Kind != "ActionAppendPendingV1" || pending.SchemaVersion != 1 ||
		segmentNamePattern.FindStringSubmatch(pending.SegmentName) == nil || pending.PreAppendSize < 0 || pending.PreAppendSize > MaxSegmentBytes {
		return ErrIntegrity
	}
	return nil
}

func (g *journalGuard) rollbackPendingAppend(pending appendPendingV1, allowMissingSegment bool) error {
	segmentFD, openErr := syscall.Openat(g.runFD, pending.SegmentName, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if openErr == syscall.ENOENT && pending.PreAppendSize == 0 && allowMissingSegment {
		return nil
	} else if openErr != nil || validateFileFD(segmentFD) != nil {
		if segmentFD >= 0 {
			syscall.Close(segmentFD)
		}
		return ErrIntegrity
	}
	segmentFile := os.NewFile(uintptr(segmentFD), pending.SegmentName)
	var segmentStat syscall.Stat_t
	if syscall.Fstat(segmentFD, &segmentStat) != nil || segmentStat.Size < pending.PreAppendSize {
		_ = segmentFile.Close()
		return ErrIntegrity
	}
	if err := g.journal.io.truncate("append-rollback", segmentFile, pending.PreAppendSize); err != nil {
		_ = segmentFile.Close()
		return err
	}
	if err := g.journal.io.syncFile("append-rollback", segmentFile); err != nil {
		_ = segmentFile.Close()
		return err
	}
	var rolledBack syscall.Stat_t
	if syscall.Fstat(segmentFD, &rolledBack) != nil || rolledBack.Size != pending.PreAppendSize {
		_ = segmentFile.Close()
		return ErrIntegrity
	}
	if err := verifyOpenAndNamedFile(g.runFD, pending.SegmentName, segmentFD, segmentStat.Dev, segmentStat.Ino); err != nil {
		_ = segmentFile.Close()
		return err
	}
	return segmentFile.Close()
}

func (g *journalGuard) readIdentity(lookup string) (requestIdentityV1, bool, error) {
	if !validDigest(lookup) {
		return requestIdentityV1{}, false, ErrIntegrity
	}
	if err := g.verifyNamespace(); err != nil {
		return requestIdentityV1{}, false, err
	}
	shardFD, err := g.openIndexShard(lookup[:2], false)
	if errors.Is(err, ErrNotFound) {
		return requestIdentityV1{}, false, nil
	}
	if err != nil {
		return requestIdentityV1{}, false, err
	}
	defer syscall.Close(shardFD)
	state, err := g.reconcileIdentityPublications(shardFD, lookup[:2])
	if err != nil {
		return requestIdentityV1{}, false, err
	}
	record, issued := state.records[lookup]
	if !issued {
		return requestIdentityV1{}, false, nil
	}
	identity, err := g.readIssuedIdentity(shardFD, lookup, record)
	if err != nil {
		return requestIdentityV1{}, false, err
	}
	if err := g.verifyNamespace(); err != nil {
		return requestIdentityV1{}, false, err
	}
	return identity, true, nil
}

func (g *journalGuard) createIdentity(identity requestIdentityV1) error {
	if err := validateRequestIdentity(identity); err != nil {
		return err
	}
	if err := g.verifyNamespace(); err != nil {
		return err
	}
	shardFD, err := g.openIndexShard(identity.LookupKeySHA256[:2], true)
	if err != nil {
		return err
	}
	defer syscall.Close(shardFD)
	state, err := g.reconcileIdentityPublications(shardFD, identity.LookupKeySHA256[:2])
	if err != nil {
		return err
	}
	if _, issued := state.records[identity.LookupKeySHA256]; issued {
		return ErrIntegrity
	}
	if len(state.records) >= MaxRequestsPerShard {
		return ErrExhausted
	}
	data, err := json.Marshal(identity)
	if err != nil || len(data) > MaxRequestIndexBytes {
		return ErrIntegrity
	}
	name := identity.LookupKeySHA256 + ".json"
	pendingName := "." + identity.LookupKeySHA256 + ".pending"
	fd, err := syscall.Openat(shardFD, pendingName, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil || validateFileFD(fd) != nil {
		if fd >= 0 {
			syscall.Close(fd)
		}
		return ErrIntegrity
	}
	file := os.NewFile(uintptr(fd), name)
	var initial syscall.Stat_t
	if syscall.Fstat(fd, &initial) != nil {
		_ = file.Close()
		return ErrIntegrity
	}
	cleanupPending := func(cause error) error {
		_ = file.Close()
		removeErr := g.journal.io.removeAt("request-index-cleanup", shardFD, pendingName)
		syncErr := g.journal.io.syncDir("request-index-cleanup", shardFD)
		return errors.Join(cause, removeErr, syncErr)
	}
	if err := g.journal.io.write("request-index", file, data); err != nil {
		return cleanupPending(err)
	}
	if err := g.journal.io.syncFile("request-index", file); err != nil {
		return cleanupPending(err)
	}
	var written syscall.Stat_t
	if syscall.Fstat(fd, &written) != nil || written.Size != int64(len(data)) {
		return cleanupPending(ErrIntegrity)
	}
	observed := make([]byte, len(data))
	if count, readErr := file.ReadAt(observed, 0); readErr != nil || count != len(data) || !bytes.Equal(observed, data) {
		return cleanupPending(ErrIntegrity)
	}
	if err := verifyOpenAndNamedFile(shardFD, pendingName, fd, initial.Dev, initial.Ino); err != nil {
		return cleanupPending(err)
	}
	// The staged name is the recovery source once issuance authority advances.
	// Make that directory entry durable before committing history so a crash can
	// never leave a committed record without the exact frozen identity bytes.
	if err := g.journal.io.syncDir("request-index-stage", shardFD); err != nil {
		return cleanupPending(err)
	}
	if err := file.Close(); err != nil {
		return cleanupPending(err)
	}
	committed, commitErr := g.commitRequestIssuance(shardFD, identity.LookupKeySHA256[:2], state, identity, data, written)
	if !committed {
		// An ambiguous history/authority outcome retains the staged identity and
		// the journal locks. A fresh process can then recover according to the
		// durable authority checkpoint without ever losing committed bytes.
		if g.retainLock {
			return commitErr
		}
		return cleanupPending(commitErr)
	}
	// A visible next authority with a failed durability operation is committed
	// for this live process but ambiguous across a crash. Leave only the already
	// durable staged name: recovery will either publish that exact inode if the
	// authority survived or discard it if the prior checkpoint survived.
	if commitErr != nil {
		return commitErr
	}
	if err := linkAt(shardFD, pendingName, shardFD, name); err != nil {
		return err
	}
	if err := g.journal.io.syncDir("request-index", shardFD); err != nil {
		return err
	}
	if err := g.journal.io.removeAt("request-index-publish", shardFD, pendingName); err != nil {
		return err
	}
	if err := g.journal.io.syncDir("request-index-publish", shardFD); err != nil {
		return err
	}
	return g.verifyNamespace()
}

type requestIndexEntry struct {
	name string
	stat syscall.Stat_t
}

func (g *journalGuard) reconcileIdentityPublications(shardFD int, shard string) (requestIssuanceState, error) {
	state, history, historyStat, err := g.loadRequestIssuanceState(shardFD, shard)
	if err != nil {
		return requestIssuanceState{}, err
	}
	defer history.Close()
	finals, pending, err := scanRequestIndexShard(shardFD, shard)
	if err != nil {
		return requestIssuanceState{}, err
	}
	for lookup := range finals {
		if _, tracked := state.records[lookup]; !tracked {
			return requestIssuanceState{}, ErrIntegrity
		}
	}
	if len(state.tail) != 0 {
		if err := g.journal.io.truncate("request-issuance-recover", history, state.committedBytes); err != nil {
			return requestIssuanceState{}, err
		}
		if err := g.journal.io.syncFile("request-issuance-recover", history); err != nil {
			return requestIssuanceState{}, err
		}
		var recovered syscall.Stat_t
		if syscall.Fstat(int(history.Fd()), &recovered) != nil || recovered.Size != state.committedBytes ||
			recovered.Dev != historyStat.Dev || recovered.Ino != historyStat.Ino ||
			verifyOpenAndNamedFile(shardFD, requestIssuanceHistoryName, int(history.Fd()), uint64(historyStat.Dev), historyStat.Ino) != nil {
			return requestIssuanceState{}, ErrIntegrity
		}
		state.tail = nil
	}
	removedPending := false
	for lookup, entry := range pending {
		if _, tracked := state.records[lookup]; tracked {
			continue
		}
		if _, exists := finals[lookup]; exists {
			return requestIssuanceState{}, ErrIntegrity
		}
		if err := g.journal.io.removeAt("request-index-invalid-pending", shardFD, entry.name); err != nil {
			return requestIssuanceState{}, err
		}
		delete(pending, lookup)
		removedPending = true
	}
	if removedPending {
		if err := g.journal.io.syncDir("request-index-invalid-pending", shardFD); err != nil {
			return requestIssuanceState{}, err
		}
	}
	for lookup, record := range state.records {
		final, hasFinal := finals[lookup]
		staged, hasPending := pending[lookup]
		if !hasFinal && !hasPending {
			return requestIssuanceState{}, ErrIntegrity
		}
		if hasFinal && !hasPending && final.stat.Nlink != 1 {
			return requestIssuanceState{}, ErrIntegrity
		}
		if hasFinal && (uint64(final.stat.Dev) != record.IdentityDevice || final.stat.Ino != record.IdentityInode) {
			return requestIssuanceState{}, ErrIntegrity
		}
		if hasPending {
			if uint64(staged.stat.Dev) != record.IdentityDevice || staged.stat.Ino != record.IdentityInode {
				return requestIssuanceState{}, ErrIntegrity
			}
			if _, err := g.readIssuedIdentityFile(shardFD, staged.name, lookup, record); err != nil {
				return requestIssuanceState{}, err
			}
			if hasFinal {
				if final.stat.Dev != staged.stat.Dev || final.stat.Ino != staged.stat.Ino || staged.stat.Nlink != 2 {
					return requestIssuanceState{}, ErrIntegrity
				}
				if err := g.journal.io.syncDir("request-index-recover", shardFD); err != nil {
					return requestIssuanceState{}, err
				}
			} else {
				if staged.stat.Nlink != 1 || linkAt(shardFD, staged.name, shardFD, lookup+".json") != nil {
					return requestIssuanceState{}, ErrIntegrity
				}
				if err := g.journal.io.syncDir("request-index-recover", shardFD); err != nil {
					return requestIssuanceState{}, err
				}
			}
			if err := g.journal.io.removeAt("request-index-recover", shardFD, staged.name); err != nil {
				return requestIssuanceState{}, err
			}
			if err := g.journal.io.syncDir("request-index-recover", shardFD); err != nil {
				return requestIssuanceState{}, err
			}
		}
	}
	return state, nil
}

func (g *journalGuard) loadRequestIssuanceState(shardFD int, shard string) (requestIssuanceState, *os.File, syscall.Stat_t, error) {
	pin, err := g.journal.requestIndexShardPin(shard)
	if err != nil || g.journal.verifyShardGenerationAuthority(pin) != nil {
		return requestIssuanceState{}, nil, syscall.Stat_t{}, ErrIntegrity
	}
	authority, authorityData, found, err := readRootGenerationAuthority(g.journal.rootFD, pin.rootAuthorityName,
		journalShardAuthorityKind, g.journal.rootDev, g.journal.rootIno)
	if err != nil || !found || authority.State != rootGenerationEstablished {
		return requestIssuanceState{}, nil, syscall.Stat_t{}, ErrIntegrity
	}
	fd, err := syscall.Openat(shardFD, requestIssuanceHistoryName, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return requestIssuanceState{}, nil, syscall.Stat_t{}, ErrIntegrity
	}
	history := os.NewFile(uintptr(fd), requestIssuanceHistoryName)
	var historyStat syscall.Stat_t
	if syscall.Fstat(fd, &historyStat) != nil || validateRequestIssuanceHistoryStat(historyStat) != nil ||
		uint64(historyStat.Dev) != pin.historyDev || historyStat.Ino != pin.historyIno {
		_ = history.Close()
		return requestIssuanceState{}, nil, syscall.Stat_t{}, ErrIntegrity
	}
	data, readErr := io.ReadAll(io.LimitReader(history, requestIssuanceHistoryMaxBytes+1))
	if readErr != nil || int64(len(data)) != historyStat.Size || int64(len(data)) > requestIssuanceHistoryMaxBytes ||
		verifyOpenAndNamedFile(shardFD, requestIssuanceHistoryName, fd, pin.historyDev, pin.historyIno) != nil {
		_ = history.Close()
		return requestIssuanceState{}, nil, syscall.Stat_t{}, ErrIntegrity
	}
	state := requestIssuanceState{authority: authority, authorityData: authorityData, records: make(map[string]requestIssuanceV1, authority.IssuanceCount)}
	offset, prior := 0, ""
	for sequence := 1; sequence <= authority.IssuanceCount; sequence++ {
		relativeEnd := bytes.IndexByte(data[offset:], '\n')
		if relativeEnd < 1 || relativeEnd > requestIssuanceMaxBytes {
			_ = history.Close()
			return requestIssuanceState{}, nil, syscall.Stat_t{}, ErrIntegrity
		}
		line := data[offset : offset+relativeEnd]
		var record requestIssuanceV1
		if json.Unmarshal(line, &record) != nil || validateRequestIssuance(record) != nil || record.Sequence != sequence || record.PriorRecordSHA256 != prior {
			_ = history.Close()
			return requestIssuanceState{}, nil, syscall.Stat_t{}, ErrIntegrity
		}
		canonical, marshalErr := json.Marshal(record)
		if marshalErr != nil || !bytes.Equal(canonical, line) {
			_ = history.Close()
			return requestIssuanceState{}, nil, syscall.Stat_t{}, ErrIntegrity
		}
		if _, duplicate := state.records[record.LookupKeySHA256]; duplicate {
			_ = history.Close()
			return requestIssuanceState{}, nil, syscall.Stat_t{}, ErrIntegrity
		}
		if !strings.HasPrefix(record.LookupKeySHA256, shard) {
			_ = history.Close()
			return requestIssuanceState{}, nil, syscall.Stat_t{}, ErrIntegrity
		}
		state.records[record.LookupKeySHA256] = record
		prior = sha256Hex(line)
		offset += relativeEnd + 1
	}
	if prior != authority.IssuanceFinalRecordSHA256 {
		_ = history.Close()
		return requestIssuanceState{}, nil, syscall.Stat_t{}, ErrIntegrity
	}
	state.committedBytes, state.lastDigest = int64(offset), prior
	state.tail = append([]byte(nil), data[offset:]...)
	return state, history, historyStat, nil
}

func (j *Journal) requestIndexShardPin(shard string) (*namespacePin, error) {
	decoded, err := hex.DecodeString(shard)
	if err != nil || len(decoded) != 1 || hex.EncodeToString(decoded) != shard {
		return nil, ErrIntegrity
	}
	j.shardMu.Lock()
	defer j.shardMu.Unlock()
	pin := j.shards[int(decoded[0])]
	if pin == nil {
		return nil, ErrIntegrity
	}
	return pin, nil
}

func validateRequestIssuance(record requestIssuanceV1) error {
	if record.Kind != "ActionRequestIssuanceV1" || record.SchemaVersion != 1 || record.Sequence < 1 ||
		!validDigest(record.LookupKeySHA256) || !validDigest(record.IdentitySHA256) || record.IdentityDevice == 0 || record.IdentityInode == 0 ||
		(record.Sequence == 1 && record.PriorRecordSHA256 != "") || (record.Sequence > 1 && !validDigest(record.PriorRecordSHA256)) {
		return ErrIntegrity
	}
	return nil
}

func validateRequestIssuanceHistoryStat(stat syscall.Stat_t) error {
	if validateProtectedFileStat(stat) != nil || stat.Size < 0 || stat.Size > requestIssuanceHistoryMaxBytes {
		return ErrIntegrity
	}
	return nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (g *journalGuard) readIssuedIdentity(shardFD int, lookup string, record requestIssuanceV1) (requestIdentityV1, error) {
	return g.readIssuedIdentityFile(shardFD, lookup+".json", lookup, record)
}

func (g *journalGuard) readIssuedIdentityFile(shardFD int, name, lookup string, record requestIssuanceV1) (requestIdentityV1, error) {
	fd, err := syscall.Openat(shardFD, name, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil || validateIndexPublicationFD(fd) != nil {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return requestIdentityV1{}, ErrIntegrity
	}
	file := os.NewFile(uintptr(fd), name)
	var initial syscall.Stat_t
	if syscall.Fstat(fd, &initial) != nil || initial.Size > MaxRequestIndexBytes || uint64(initial.Dev) != record.IdentityDevice || initial.Ino != record.IdentityInode {
		_ = file.Close()
		return requestIdentityV1{}, ErrIntegrity
	}
	data, readErr := io.ReadAll(io.LimitReader(file, MaxRequestIndexBytes+1))
	if readErr != nil || len(data) > MaxRequestIndexBytes || int64(len(data)) != initial.Size || sha256Hex(data) != record.IdentitySHA256 {
		_ = file.Close()
		return requestIdentityV1{}, ErrIntegrity
	}
	if err := g.journal.io.syncFile("request-index", file); err != nil {
		_ = file.Close()
		return requestIdentityV1{}, err
	}
	if err := verifyOpenAndNamedIndexPublication(shardFD, name, fd, initial.Dev, initial.Ino); err != nil {
		_ = file.Close()
		return requestIdentityV1{}, err
	}
	if err := file.Close(); err != nil {
		return requestIdentityV1{}, err
	}
	var identity requestIdentityV1
	if json.Unmarshal(data, &identity) != nil || validateRequestIdentity(identity) != nil {
		return requestIdentityV1{}, ErrIntegrity
	}
	canonical, err := json.Marshal(identity)
	if err != nil || !bytes.Equal(canonical, data) || identity.LookupKeySHA256 != lookup {
		return requestIdentityV1{}, ErrIntegrity
	}
	return identity, nil
}

func validateIndexPublicationFD(fd int) error {
	if fd < 0 {
		return ErrIntegrity
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil {
		return ErrIntegrity
	}
	return validateIndexPublicationStat(stat)
}

func scanRequestIndexShard(fd int, shard string) (map[string]requestIndexEntry, map[string]requestIndexEntry, error) {
	dup, err := syscall.Dup(fd)
	if err != nil {
		return nil, nil, ErrIntegrity
	}
	directory := os.NewFile(uintptr(dup), "action-request-index-shard")
	entries, readErr := directory.ReadDir(2*MaxRequestsPerShard + 2)
	closeErr := directory.Close()
	if (readErr != nil && readErr != io.EOF) || closeErr != nil || len(entries) >= 2*MaxRequestsPerShard+2 {
		return nil, nil, ErrIntegrity
	}
	finals := make(map[string]requestIndexEntry)
	pending := make(map[string]requestIndexEntry)
	historyFound := false
	keys := make(map[string]struct{})
	for _, entry := range entries {
		name := entry.Name()
		if name == requestIssuanceHistoryName {
			if historyFound || entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
				return nil, nil, ErrIntegrity
			}
			historyFound = true
			continue
		}
		lookup, final, staged := "", false, false
		if len(name) == sha256.Size*2+len(".json") && strings.HasSuffix(name, ".json") {
			lookup, final = strings.TrimSuffix(name, ".json"), true
		} else if len(name) == 1+sha256.Size*2+len(".pending") && strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".pending") {
			lookup, staged = strings.TrimSuffix(strings.TrimPrefix(name, "."), ".pending"), true
		}
		if !validDigest(lookup) || !strings.HasPrefix(lookup, shard) || (!final && !staged) || entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return nil, nil, ErrIntegrity
		}
		entryFD, openErr := syscall.Openat(fd, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		var stat syscall.Stat_t
		if openErr != nil || syscall.Fstat(entryFD, &stat) != nil || validateIndexPublicationStat(stat) != nil {
			if entryFD >= 0 {
				_ = syscall.Close(entryFD)
			}
			return nil, nil, ErrIntegrity
		}
		_ = syscall.Close(entryFD)
		value := requestIndexEntry{name: name, stat: stat}
		if final {
			finals[lookup] = value
		} else {
			pending[lookup] = value
		}
		keys[lookup] = struct{}{}
	}
	if !historyFound || len(keys) > MaxRequestsPerShard {
		return nil, nil, ErrIntegrity
	}
	return finals, pending, nil
}

func (g *journalGuard) commitRequestIssuance(shardFD int, shard string, expected requestIssuanceState, identity requestIdentityV1, identityData []byte, identityStat syscall.Stat_t) (bool, error) {
	state, history, historyStat, err := g.loadRequestIssuanceState(shardFD, shard)
	if err != nil {
		return false, err
	}
	defer history.Close()
	if len(state.tail) != 0 || !bytes.Equal(state.authorityData, expected.authorityData) || state.committedBytes != expected.committedBytes ||
		len(state.records) != len(expected.records) || state.authority.IssuanceCount >= MaxRequestsPerShard {
		return false, ErrIntegrity
	}
	if _, duplicate := state.records[identity.LookupKeySHA256]; duplicate {
		return false, ErrIntegrity
	}
	record := requestIssuanceV1{Kind: "ActionRequestIssuanceV1", SchemaVersion: 1,
		Sequence: state.authority.IssuanceCount + 1, PriorRecordSHA256: state.lastDigest,
		LookupKeySHA256: identity.LookupKeySHA256, IdentitySHA256: sha256Hex(identityData),
		IdentityDevice: uint64(identityStat.Dev), IdentityInode: identityStat.Ino}
	line, err := json.Marshal(record)
	if err != nil || len(line) == 0 || len(line) > requestIssuanceMaxBytes || validateRequestIssuance(record) != nil {
		return false, ErrIntegrity
	}
	line = append(line, '\n')
	if _, err := history.Seek(state.committedBytes, io.SeekStart); err != nil {
		return false, ErrIntegrity
	}
	rollback := func(cause error) (bool, error) {
		truncateErr := g.journal.io.truncate("request-issuance-rollback", history, state.committedBytes)
		syncErr := g.journal.io.syncFile("request-issuance-rollback", history)
		var rolledBack syscall.Stat_t
		statErr := syscall.Fstat(int(history.Fd()), &rolledBack)
		if truncateErr != nil || syncErr != nil || statErr != nil || rolledBack.Size != state.committedBytes ||
			rolledBack.Dev != historyStat.Dev || rolledBack.Ino != historyStat.Ino {
			g.journal.failedClosed.Store(true)
			g.retainLock = true
			return false, errors.Join(ErrIntegrity, cause, truncateErr, syncErr, statErr)
		}
		return false, cause
	}
	if err := g.journal.io.write("request-issuance-history", history, line); err != nil {
		return rollback(err)
	}
	if err := g.journal.io.syncFile("request-issuance-history", history); err != nil {
		return rollback(err)
	}
	var appended syscall.Stat_t
	if syscall.Fstat(int(history.Fd()), &appended) != nil || appended.Size != state.committedBytes+int64(len(line)) ||
		appended.Dev != historyStat.Dev || appended.Ino != historyStat.Ino ||
		verifyOpenAndNamedFile(shardFD, requestIssuanceHistoryName, int(history.Fd()), uint64(historyStat.Dev), historyStat.Ino) != nil {
		return rollback(ErrIntegrity)
	}
	pin, err := g.journal.requestIndexShardPin(shard)
	if err != nil {
		return rollback(err)
	}
	nextDigest := sha256Hex(line[:len(line)-1])
	nextAuthority, nextData, err := makeShardGenerationAuthority(g.journal.rootDev, g.journal.rootIno,
		rootGenerationObjectsForShardPin("actions/"+requestIndexDirectory, pin), record.Sequence, nextDigest)
	if err != nil {
		return rollback(err)
	}
	setErr := fsetRootXattr(g.journal.rootFD, pin.rootAuthorityName, nextData, rootGenerationXattrReplace)
	var syncErr error
	if setErr == nil {
		syncErr = g.journal.io.syncDir("request-issuance-authority", g.journal.rootFD)
	}
	observed, found, readErr := fgetRootXattr(g.journal.rootFD, pin.rootAuthorityName)
	if readErr == nil && found && bytes.Equal(observed, nextData) {
		if _, canonical, established, decodeErr := decodeRootGenerationAuthority(observed, journalShardAuthorityKind, g.journal.rootDev, g.journal.rootIno); decodeErr != nil || !established || !bytes.Equal(canonical, nextData) || nextAuthority.IssuanceCount != record.Sequence {
			g.journal.failedClosed.Store(true)
			g.retainLock = true
			return true, errors.Join(ErrIntegrity, setErr, syncErr, decodeErr)
		}
		g.journal.shardMu.Lock()
		pin.rootAuthorityData = append(pin.rootAuthorityData[:0], nextData...)
		g.journal.shardMu.Unlock()
		return true, errors.Join(setErr, syncErr)
	}
	if readErr == nil && found && bytes.Equal(observed, state.authorityData) {
		return rollback(errors.Join(setErr, syncErr))
	}
	g.journal.failedClosed.Store(true)
	g.retainLock = true
	return false, errors.Join(ErrIntegrity, setErr, syncErr, readErr)
}

func validateIndexShard(fd int) error {
	dup, err := syscall.Dup(fd)
	if err != nil {
		return ErrIntegrity
	}
	directory := os.NewFile(uintptr(dup), "action-request-index-shard")
	entries, readErr := directory.ReadDir(2*MaxRequestsPerShard + 2)
	closeErr := directory.Close()
	if readErr != nil && readErr != io.EOF {
		return ErrIntegrity
	}
	if closeErr != nil || len(entries) >= 2*MaxRequestsPerShard+2 {
		return ErrIntegrity
	}
	keys := make(map[string]struct{})
	for _, entry := range entries {
		name := entry.Name()
		if name == requestIssuanceHistoryName {
			if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
				return ErrIntegrity
			}
			continue
		}
		final := len(name) == sha256.Size*2+len(".json") && strings.HasSuffix(name, ".json") && validDigest(strings.TrimSuffix(name, ".json"))
		pending := len(name) == 1+sha256.Size*2+len(".pending") && strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".pending") &&
			validDigest(strings.TrimSuffix(strings.TrimPrefix(name, "."), ".pending"))
		if (!final && !pending) || entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return ErrIntegrity
		}
		entryFD, err := syscall.Openat(fd, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		var stat syscall.Stat_t
		if err != nil || syscall.Fstat(entryFD, &stat) != nil || validateIndexPublicationStat(stat) != nil || (final && stat.Nlink != 1 && stat.Nlink != 2) || (pending && stat.Nlink != 1 && stat.Nlink != 2) {
			if entryFD >= 0 {
				syscall.Close(entryFD)
			}
			return ErrIntegrity
		}
		syscall.Close(entryFD)
		lookup := strings.TrimSuffix(name, ".json")
		if pending {
			lookup = strings.TrimSuffix(strings.TrimPrefix(name, "."), ".pending")
		}
		keys[lookup] = struct{}{}
	}
	if len(keys) >= MaxRequestsPerShard {
		return ErrExhausted
	}
	return nil
}

func validateIndexPublicationStat(stat syscall.Stat_t) error {
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Mode&0o777 != 0o600 || stat.Uid != uint32(os.Geteuid()) || (stat.Nlink != 1 && stat.Nlink != 2) {
		return ErrIntegrity
	}
	return nil
}

func verifyOpenAndNamedIndexPublication(parent int, name string, fd int, dev, ino uint64) error {
	var current syscall.Stat_t
	if syscall.Fstat(fd, &current) != nil || validateIndexPublicationStat(current) != nil || current.Dev != dev || current.Ino != ino {
		return ErrIntegrity
	}
	reopened, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ErrIntegrity
	}
	defer syscall.Close(reopened)
	var named syscall.Stat_t
	if syscall.Fstat(reopened, &named) != nil || validateIndexPublicationStat(named) != nil || named.Dev != dev || named.Ino != ino {
		return ErrIntegrity
	}
	return nil
}

func linkAt(oldDir int, oldName string, newDir int, newName string) error {
	oldPointer, err := syscall.BytePtrFromString(oldName)
	if err != nil {
		return ErrIntegrity
	}
	newPointer, err := syscall.BytePtrFromString(newName)
	if err != nil {
		return ErrIntegrity
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_LINKAT, uintptr(oldDir), uintptr(unsafe.Pointer(oldPointer)), uintptr(newDir), uintptr(unsafe.Pointer(newPointer)), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func (g *journalGuard) reproveReceipt(operation *Operation) error {
	identity, found, err := g.readIdentity(operation.Receipt.LookupKeySHA256)
	if err != nil {
		return err
	}
	if !found {
		return ErrIntegrity
	}
	return reproveOperation(identity, operation)
}

func validateRequestIdentity(identity requestIdentityV1) error {
	if identity.Kind != "ActionRequestIdentityV1" || identity.SchemaVersion != 1 ||
		identity.LookupKeySHA256 != LookupKey(identity.PrincipalID, identity.RequestID) ||
		identity.OperationID != operationIDForLookup(identity.LookupKeySHA256) {
		return ErrIntegrity
	}
	receipt := receiptFromIdentity(identity)
	receipt.Epoch, receipt.Sequence = 1, 1
	return validateReceipt(receipt)
}

func identityFromReceipt(receipt ActionReceiptV1) requestIdentityV1 {
	return requestIdentityV1{Kind: "ActionRequestIdentityV1", SchemaVersion: 1,
		LookupKeySHA256: receipt.LookupKeySHA256, OperationID: receipt.OperationID,
		PrincipalID: receipt.PrincipalID, PrincipalType: receipt.PrincipalType, RequestID: receipt.RequestID,
		RequestSHA256: receipt.RequestSHA256, Action: receipt.Action, RunID: receipt.RunID, AttemptID: receipt.AttemptID,
		ExpectedState: receipt.ExpectedState, ExpectedRevision: receipt.ExpectedRevision, Reason: receipt.Reason,
		DelegatedActor: cloneActor(receipt.DelegatedActor), Payload: append(json.RawMessage(nil), receipt.Payload...),
		PolicyVersion: receipt.PolicyVersion, AuthorityGrantSHA256: receipt.AuthorityGrantSHA256,
		AdmittedStateTransitionID: receipt.AdmittedStateTransitionID, OwnerLeaseID: receipt.OwnerLeaseID, ReceivedAt: receipt.ReceivedAt}
}

func receiptFromIdentity(identity requestIdentityV1) ActionReceiptV1 {
	return ActionReceiptV1{Kind: "ActionReceiptV1", SchemaVersion: JournalSchemaVersion,
		LookupKeySHA256: identity.LookupKeySHA256, OperationID: identity.OperationID,
		PrincipalID: identity.PrincipalID, PrincipalType: identity.PrincipalType, RequestID: identity.RequestID,
		RequestSHA256: identity.RequestSHA256, Action: identity.Action, RunID: identity.RunID, AttemptID: identity.AttemptID,
		ExpectedState: identity.ExpectedState, ExpectedRevision: identity.ExpectedRevision, Reason: identity.Reason,
		DelegatedActor: cloneActor(identity.DelegatedActor), Payload: append(json.RawMessage(nil), identity.Payload...),
		PolicyVersion: identity.PolicyVersion, AuthorityGrantSHA256: identity.AuthorityGrantSHA256,
		AdmittedStateTransitionID: identity.AdmittedStateTransitionID, OwnerLeaseID: identity.OwnerLeaseID, ReceivedAt: identity.ReceivedAt}
}

func identityMatchesInput(identity requestIdentityV1, input ReceiptInput) bool {
	return identity.PrincipalID == input.PrincipalID && identity.PrincipalType == input.PrincipalType && identity.RequestID == input.RequestID &&
		identity.RequestSHA256 == input.RequestSHA256 && identity.Action == input.Action && identity.RunID == input.RunID && identity.AttemptID == input.AttemptID &&
		identity.ExpectedState == input.ExpectedState && identity.ExpectedRevision == input.ExpectedRevision && identity.Reason == input.Reason &&
		actorsEqual(identity.DelegatedActor, input.DelegatedActor) && bytes.Equal(identity.Payload, input.Payload) && identity.PolicyVersion == input.PolicyVersion &&
		identity.AuthorityGrantSHA256 == input.AuthorityGrantSHA256 && identity.AdmittedStateTransitionID == input.StateTransitionID
}

func identityMatchesReplay(identity requestIdentityV1, replay ReceiptReplayInput) bool {
	return identity.PrincipalID == replay.PrincipalID && identity.PrincipalType == replay.PrincipalType && identity.RequestID == replay.RequestID &&
		identity.RequestSHA256 == replay.RequestSHA256 && identity.Action == replay.Action && identity.RunID == replay.RunID && identity.AttemptID == replay.AttemptID
}

func actorsEqual(a, b *DelegatedActorV1) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func reproveOperation(identity requestIdentityV1, operation *Operation) error {
	if operation == nil || !reflect.DeepEqual(identityFromReceipt(operation.Receipt), identity) {
		return ErrIntegrity
	}
	return nil
}

// validateReceiptMaterialization reapplies every run-journal uniqueness
// constraint before a frozen identity with a missing receipt is appended.
// The immutable primary identity remains authoritative for exact replay, but
// it cannot displace a secondary identity that a different durable receipt
// acquired while this receipt was absent.
func validateReceiptMaterialization(state *journalState, receipt ActionReceiptV1) error {
	if state == nil {
		return ErrIntegrity
	}
	if _, exists := state.operations[receipt.OperationID]; exists {
		return ErrIntegrity
	}
	if _, exists := state.lookups[receipt.LookupKeySHA256]; exists {
		return ErrIntegrity
	}
	if receipt.Action != "decision" {
		return nil
	}
	decisionRequestID, err := journalDecisionRequestID(receipt.Payload)
	if err != nil {
		return err
	}
	if _, exists := state.decisions[decisionRequestID]; exists {
		return ErrDecisionExists
	}
	return nil
}

func validateAppendValue(value any) error {
	switch record := value.(type) {
	case *ActionReceiptV1:
		return validateReceipt(*record)
	case *ActionClaimV1:
		return validateClaim(*record)
	case *ActionOutcomeV1:
		return validateOutcome(*record)
	default:
		return ErrIntegrity
	}
}

func setEnvelope(value any, epoch, sequence uint64, prior string) {
	switch record := value.(type) {
	case *ActionReceiptV1:
		record.Epoch, record.Sequence, record.PriorSegmentFinalRecordSHA256 = epoch, sequence, prior
	case *ActionClaimV1:
		record.Epoch, record.Sequence, record.PriorSegmentFinalRecordSHA256 = epoch, sequence, prior
	case *ActionOutcomeV1:
		record.Epoch, record.Sequence, record.PriorSegmentFinalRecordSHA256 = epoch, sequence, prior
	}
}

func segmentName(epoch uint64) string { return fmt.Sprintf("%08d.jsonl", epoch) }

func storageComponent(identifier string) string {
	if len(identifier) <= 240 && !strings.HasPrefix(identifier, "sha256-") {
		return identifier
	}
	sum := sha256.Sum256([]byte(identifier))
	return "sha256-" + hex.EncodeToString(sum[:])
}

func validStorageComponent(value string) bool {
	if strings.HasPrefix(value, "sha256-") {
		return len(value) == len("sha256-")+sha256.Size*2 && validDigest(strings.TrimPrefix(value, "sha256-"))
	}
	return runtimeIdentifier(value) == nil && len(value) <= 240
}

func runtimeIdentifier(value string) error {
	if len(value) < 1 || len(value) > 256 {
		return ErrIntegrity
	}
	for index, b := range []byte(value) {
		if !((b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || (index > 0 && (b == '.' || b == '_' || b == '-'))) {
			return ErrIntegrity
		}
	}
	return nil
}

func cloneActor(value *DelegatedActorV1) *DelegatedActorV1 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneOperation(value Operation) Operation {
	value.Receipt = cloneReceipt(value.Receipt)
	if value.Claim != nil {
		clone := *value.Claim
		value.Claim = &clone
	}
	value.Outcomes = append([]ActionOutcomeV1(nil), value.Outcomes...)
	for index := range value.Outcomes {
		value.Outcomes[index].AuthoritativeEventIDs = append([]string(nil), value.Outcomes[index].AuthoritativeEventIDs...)
	}
	return value
}

func cloneReceipt(value ActionReceiptV1) ActionReceiptV1 {
	value.Payload = append(json.RawMessage(nil), value.Payload...)
	value.DelegatedActor = cloneActor(value.DelegatedActor)
	return value
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type generationEntry struct {
	name      string
	directory bool
}

func lockRootGeneration(fd int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
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

func requireGenerationEntriesAbsent(parent int, entries ...generationEntry) error {
	for _, entry := range entries {
		flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
		if entry.directory {
			flags |= syscall.O_DIRECTORY
		}
		fd, err := syscall.Openat(parent, entry.name, flags, 0)
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

func createInitializingRootGeneration(fd int, name, kind string, rootDev, rootIno uint64) (rootGenerationAuthorityV1, []byte, error) {
	authority := rootGenerationAuthorityV1{Kind: kind, SchemaVersion: 1, State: rootGenerationInitializing, RootDevice: rootDev, RootInode: rootIno}
	data, err := json.Marshal(authority)
	if err != nil || len(data) == 0 || len(data) > rootGenerationMaxBytes {
		return rootGenerationAuthorityV1{}, nil, ErrIntegrity
	}
	if err := fsetRootXattr(fd, name, data, rootGenerationXattrCreate); err != nil {
		return rootGenerationAuthorityV1{}, nil, ErrIntegrity
	}
	if err := syscall.Fsync(fd); err != nil {
		return rootGenerationAuthorityV1{}, nil, ErrIntegrity
	}
	if err := verifyRootGenerationData(fd, name, data); err != nil {
		return rootGenerationAuthorityV1{}, nil, err
	}
	return authority, data, nil
}

func makeRootGenerationAuthority(kind string, rootDev, rootIno uint64, objects []rootGenerationObjectIdentityV1) (rootGenerationAuthorityV1, []byte, error) {
	authority := rootGenerationAuthorityV1{Kind: kind, SchemaVersion: 1, State: rootGenerationEstablished,
		RootDevice: rootDev, RootInode: rootIno, Objects: append([]rootGenerationObjectIdentityV1(nil), objects...)}
	return encodeRootGenerationAuthority(authority, kind, rootDev, rootIno)
}

func makeShardGenerationAuthority(rootDev, rootIno uint64, objects []rootGenerationObjectIdentityV1, count int, finalDigest string) (rootGenerationAuthorityV1, []byte, error) {
	authority := rootGenerationAuthorityV1{Kind: journalShardAuthorityKind, SchemaVersion: 1, State: rootGenerationEstablished,
		RootDevice: rootDev, RootInode: rootIno, Objects: append([]rootGenerationObjectIdentityV1(nil), objects...),
		IssuanceCount: count, IssuanceFinalRecordSHA256: finalDigest}
	return encodeRootGenerationAuthority(authority, journalShardAuthorityKind, rootDev, rootIno)
}

func makeRunGenerationAuthority(rootDev, rootIno uint64, objects []rootGenerationObjectIdentityV1, count int, finalDigest string) (rootGenerationAuthorityV1, []byte, error) {
	authority := rootGenerationAuthorityV1{Kind: journalRunAuthorityKind, SchemaVersion: 1, State: rootGenerationEstablished,
		RootDevice: rootDev, RootInode: rootIno, Objects: append([]rootGenerationObjectIdentityV1(nil), objects...),
		IssuanceCount: count, IssuanceFinalRecordSHA256: finalDigest}
	return encodeRootGenerationAuthority(authority, journalRunAuthorityKind, rootDev, rootIno)
}

func makePreparedRunGenerationAuthority(rootDev, rootIno uint64, objects []rootGenerationObjectIdentityV1,
	count int, finalDigest string, prepared runGenerationV1) (rootGenerationAuthorityV1, []byte, error) {
	authority := rootGenerationAuthorityV1{Kind: journalRunAuthorityKind, SchemaVersion: 1, State: rootGenerationPrepared,
		RootDevice: rootDev, RootInode: rootIno, Objects: append([]rootGenerationObjectIdentityV1(nil), objects...),
		IssuanceCount: count, IssuanceFinalRecordSHA256: finalDigest, PreparedRunGeneration: &prepared}
	return encodeRootGenerationAuthority(authority, journalRunAuthorityKind, rootDev, rootIno)
}

func encodeRootGenerationAuthority(authority rootGenerationAuthorityV1, kind string, rootDev, rootIno uint64) (rootGenerationAuthorityV1, []byte, error) {
	data, err := json.Marshal(authority)
	if err != nil || len(data) == 0 || len(data) > rootGenerationMaxBytes {
		return rootGenerationAuthorityV1{}, nil, ErrIntegrity
	}
	if _, _, _, err := decodeRootGenerationAuthority(data, kind, rootDev, rootIno); err != nil {
		return rootGenerationAuthorityV1{}, nil, err
	}
	return authority, data, nil
}

func publishRootGeneration(fd int, name string, authority rootGenerationAuthorityV1, data []byte) ([]byte, error) {
	canonical, err := json.Marshal(authority)
	if err != nil || authority.State != rootGenerationEstablished || !bytes.Equal(canonical, data) {
		return nil, ErrIntegrity
	}
	if err := fsetRootXattr(fd, name, data, rootGenerationXattrReplace); err != nil {
		return nil, ErrIntegrity
	}
	if err := syscall.Fsync(fd); err != nil {
		return nil, ErrIntegrity
	}
	if err := verifyRootGenerationData(fd, name, data); err != nil {
		return nil, err
	}
	return append([]byte(nil), data...), nil
}

func readRootGenerationAuthority(fd int, name, kind string, rootDev, rootIno uint64) (rootGenerationAuthorityV1, []byte, bool, error) {
	data, found, err := fgetRootXattr(fd, name)
	if err != nil || !found {
		return rootGenerationAuthorityV1{}, nil, found, err
	}
	authority, canonical, _, err := decodeRootGenerationAuthority(data, kind, rootDev, rootIno)
	if err != nil {
		return rootGenerationAuthorityV1{}, nil, true, err
	}
	return authority, canonical, true, nil
}

func decodeRootGenerationAuthority(data []byte, kind string, rootDev, rootIno uint64) (rootGenerationAuthorityV1, []byte, bool, error) {
	if len(data) == 0 || len(data) > rootGenerationMaxBytes {
		return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
	}
	var authority rootGenerationAuthorityV1
	if json.Unmarshal(data, &authority) != nil || authority.Kind != kind || authority.SchemaVersion != 1 ||
		authority.RootDevice != rootDev || authority.RootInode != rootIno ||
		(authority.State != rootGenerationInitializing && authority.State != rootGenerationEstablished &&
			authority.State != rootGenerationPrepared) {
		return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
	}
	canonical, err := json.Marshal(authority)
	if err != nil || !bytes.Equal(canonical, data) {
		return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
	}
	if authority.State == rootGenerationInitializing {
		if authority.IssuanceCount != 0 || authority.IssuanceFinalRecordSHA256 != "" || authority.PreparedRunGeneration != nil {
			return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
		}
	} else if authority.PreparedIdentity != nil {
		return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
	}
	if authority.State == rootGenerationPrepared && kind != journalRunAuthorityKind {
		return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
	}
	if kind == journalShardAuthorityKind || kind == journalRunAuthorityKind {
		maximum := MaxRequestsPerShard
		if kind == journalRunAuthorityKind {
			maximum = MaxJournalRuns
		}
		if authority.IssuanceCount < 0 || authority.IssuanceCount > maximum ||
			(authority.IssuanceCount == 0 && authority.IssuanceFinalRecordSHA256 != "") ||
			(authority.IssuanceCount > 0 && !validDigest(authority.IssuanceFinalRecordSHA256)) {
			return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
		}
	} else if authority.IssuanceCount != 0 || authority.IssuanceFinalRecordSHA256 != "" {
		return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
	}
	if authority.State == rootGenerationEstablished && authority.PreparedRunGeneration != nil {
		return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
	}
	if authority.State == rootGenerationPrepared {
		if authority.IssuanceCount >= MaxJournalRuns || authority.PreparedRunGeneration == nil ||
			validateRunGeneration(*authority.PreparedRunGeneration) != nil ||
			authority.PreparedRunGeneration.Sequence != authority.IssuanceCount+1 ||
			authority.PreparedRunGeneration.PriorRecordSHA256 != authority.IssuanceFinalRecordSHA256 {
			return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
		}
	}
	if (authority.State != rootGenerationInitializing && len(authority.Objects) == 0) || len(authority.Objects) > 64 {
		return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
	}
	seen := make(map[string]struct{}, len(authority.Objects))
	for _, object := range authority.Objects {
		if object.Parent == "" || len(object.Parent) > 128 || object.Name == "" || len(object.Name) > 128 ||
			strings.Contains(object.Name, "/") || (object.ObjectType != "directory" && object.ObjectType != "file") || object.Inode == 0 {
			return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
		}
		key := object.Parent + "\x00" + object.Name
		if _, duplicate := seen[key]; duplicate {
			return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
		}
		seen[key] = struct{}{}
	}
	if authority.PreparedIdentity != nil {
		if validatePreparedIdentityPublication(*authority.PreparedIdentity) != nil ||
			identityPublicationObjectsOverlap(authority.Objects, authority.PreparedIdentity.Objects) {
			return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
		}
	}
	if authority.State == rootGenerationInitializing && validateJournalInitializingAuthority(authority, kind) != nil {
		return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
	}
	return authority, canonical, authority.State == rootGenerationEstablished, nil
}

func validateJournalInitializingAuthority(authority rootGenerationAuthorityV1, kind string) error {
	if kind == journalRunAuthorityKind {
		if len(authority.Objects) != 0 || authority.PreparedIdentity != nil {
			return ErrIntegrity
		}
		return nil
	}
	var schema []rootGenerationObjectIdentityV1
	switch kind {
	case journalRootAuthorityKind:
		schema = []rootGenerationObjectIdentityV1{
			{Parent: ".", Name: "actions", ObjectType: "directory"},
			{Parent: ".", Name: actionsIdentityName, ObjectType: "file"},
			{Parent: "actions", Name: ".lock", ObjectType: "file"},
			{Parent: "actions", Name: lockIdentityName, ObjectType: "file"},
			{Parent: "actions", Name: requestIndexDirectory, ObjectType: "directory"},
			{Parent: "actions", Name: requestIndexIdentityName, ObjectType: "file"},
			{Parent: "actions", Name: runGenerationHistoryName, ObjectType: "file"},
			{Parent: "actions", Name: runGenerationIdentityName, ObjectType: "file"},
		}
	case journalShardAuthorityKind:
		if len(authority.Objects) == 0 && authority.PreparedIdentity == nil {
			return nil
		}
		var target string
		if len(authority.Objects) != 0 {
			target = authority.Objects[0].Name
		} else if authority.PreparedIdentity != nil && len(authority.PreparedIdentity.Objects) != 0 {
			target = authority.PreparedIdentity.Objects[0].Name
		}
		decoded, err := hex.DecodeString(target)
		if err != nil || len(decoded) != 1 || hex.EncodeToString(decoded) != target {
			return ErrIntegrity
		}
		schema = []rootGenerationObjectIdentityV1{
			{Parent: "actions/" + requestIndexDirectory, Name: target, ObjectType: "directory"},
			{Parent: "actions/" + requestIndexDirectory, Name: "." + target + ".identity.json", ObjectType: "file"},
		}
	default:
		return ErrIntegrity
	}
	if len(authority.Objects) > len(schema) || len(authority.Objects)%2 != 0 {
		return ErrIntegrity
	}
	for index, object := range authority.Objects {
		expected := schema[index]
		if object.Parent != expected.Parent || object.Name != expected.Name || object.ObjectType != expected.ObjectType {
			return ErrIntegrity
		}
	}
	if authority.PreparedIdentity == nil {
		return nil
	}
	if len(authority.Objects)+len(authority.PreparedIdentity.Objects) > len(schema) || len(authority.PreparedIdentity.Objects) != 2 {
		return ErrIntegrity
	}
	for index, object := range authority.PreparedIdentity.Objects {
		expected := schema[len(authority.Objects)+index]
		if object.Parent != expected.Parent || object.Name != expected.Name || object.ObjectType != expected.ObjectType {
			return ErrIntegrity
		}
	}
	return nil
}

func validatePreparedIdentityPublication(prepared preparedIdentityPublicationV1) error {
	if prepared.Parent == "" || len(prepared.Parent) > 128 || prepared.IdentityName == "" || len(prepared.IdentityName) > 128 ||
		strings.Contains(prepared.IdentityName, "/") || prepared.StageName != prepared.IdentityName+".prepared" ||
		len(prepared.StageName) > 255 || !validDigest(prepared.IdentitySHA256) || len(prepared.Objects) < 2 ||
		len(prepared.Objects) > 16 || prepared.IdentityIndex < 0 || prepared.IdentityIndex >= len(prepared.Objects) {
		return ErrIntegrity
	}
	seen := make(map[string]struct{}, len(prepared.Objects))
	for index, object := range prepared.Objects {
		if object.Parent == "" || len(object.Parent) > 128 || object.Name == "" || len(object.Name) > 128 ||
			strings.Contains(object.Name, "/") || (object.ObjectType != "directory" && object.ObjectType != "file") {
			return ErrIntegrity
		}
		if index == prepared.IdentityIndex {
			if object.Parent != prepared.Parent || object.Name != prepared.IdentityName || object.ObjectType != "file" ||
				(object.Device == 0) != (object.Inode == 0) {
				return ErrIntegrity
			}
		} else if object.Inode == 0 {
			return ErrIntegrity
		}
		key := object.Parent + "\x00" + object.Name
		if _, duplicate := seen[key]; duplicate {
			return ErrIntegrity
		}
		seen[key] = struct{}{}
	}
	return nil
}

func verifyRootGenerationData(fd int, name string, expected []byte) error {
	observed, found, err := fgetRootXattr(fd, name)
	if err != nil || !found || !bytes.Equal(observed, expected) {
		return ErrIntegrity
	}
	return nil
}

func fgetRootXattr(fd int, name string) ([]byte, bool, error) {
	namePointer, err := syscall.BytePtrFromString(name)
	if err != nil {
		return nil, false, ErrIntegrity
	}
	buffer := make([]byte, rootGenerationMaxBytes+1)
	size, _, errno := syscall.Syscall6(syscall.SYS_FGETXATTR, uintptr(fd), uintptr(unsafe.Pointer(namePointer)),
		uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, 0)
	if errno == syscall.ENODATA {
		return nil, false, nil
	}
	if errno != 0 || size == 0 || size > rootGenerationMaxBytes {
		return nil, false, ErrIntegrity
	}
	return append([]byte(nil), buffer[:int(size)]...), true, nil
}

func fsetRootXattr(fd int, name string, data []byte, flags int) error {
	if len(data) == 0 || len(data) > rootGenerationMaxBytes {
		return ErrIntegrity
	}
	namePointer, err := syscall.BytePtrFromString(name)
	if err != nil {
		return ErrIntegrity
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_FSETXATTR, uintptr(fd), uintptr(unsafe.Pointer(namePointer)),
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(flags), 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func rootGenerationObjectsForPin(parent string, pin *namespacePin) []rootGenerationObjectIdentityV1 {
	if pin == nil {
		return nil
	}
	objectType := "file"
	if pin.directory {
		objectType = "directory"
	}
	return []rootGenerationObjectIdentityV1{
		{Parent: parent, Name: pin.name, ObjectType: objectType, Device: pin.dev, Inode: pin.ino},
		{Parent: parent, Name: pin.anchorName, ObjectType: "file", Device: pin.anchorDev, Inode: pin.anchorIno},
	}
}

func rootGenerationObjectsForShardPin(parent string, pin *namespacePin) []rootGenerationObjectIdentityV1 {
	objects := rootGenerationObjectsForPin(parent, pin)
	if pin == nil || pin.historyName == "" || pin.historyIno == 0 {
		return objects
	}
	return append(objects, rootGenerationObjectIdentityV1{
		Parent: parent + "/" + pin.name, Name: pin.historyName, ObjectType: "file", Device: pin.historyDev, Inode: pin.historyIno,
	})
}

func journalRootGenerationObjects(j *Journal) []rootGenerationObjectIdentityV1 {
	objects := make([]rootGenerationObjectIdentityV1, 0, 8)
	objects = append(objects, rootGenerationObjectsForPin(".", j.actions)...)
	objects = append(objects, rootGenerationObjectsForPin("actions", j.lockFile)...)
	objects = append(objects, rootGenerationObjectsForPin("actions", j.requestIndex)...)
	objects = append(objects, rootGenerationObjectsForPin("actions", j.runHistory)...)
	return objects
}

func (j *Journal) pinNamespace(parent int, authorityParent, name, anchorName string, fd int, directory, allowCreate bool,
	authorityName, authorityKind string) (*namespacePin, error) {
	if fd < 0 || authorityParent == "" || name == "" || anchorName == "" {
		return nil, ErrIntegrity
	}
	var objectStat syscall.Stat_t
	if syscall.Fstat(fd, &objectStat) != nil {
		return nil, ErrIntegrity
	}
	if directory {
		if validateDirectoryFD(fd, false) != nil {
			return nil, ErrIntegrity
		}
	} else if validateFileFD(fd) != nil {
		return nil, ErrIntegrity
	}
	objectType := "file"
	if directory {
		objectType = "directory"
	}
	identity := namespaceIdentityV1{Kind: "ActionNamespaceIdentityV1", SchemaVersion: 1, Name: name,
		ObjectType: objectType, Device: uint64(objectStat.Dev), Inode: objectStat.Ino}
	data, err := json.Marshal(identity)
	if err != nil || len(data) > namespaceIdentityMaxBytes {
		return nil, ErrIntegrity
	}
	anchorFD := -1
	var anchorFile *os.File
	var anchorStat syscall.Stat_t
	if allowCreate {
		anchorFile, anchorStat, err = j.publishPreparedNamespaceIdentity(parent, authorityParent, name, objectType,
			uint64(objectStat.Dev), objectStat.Ino, anchorName, data, authorityName, authorityKind)
	} else {
		anchorFD, err = syscall.Openat(parent, anchorName, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil || validateFileFD(anchorFD) != nil || syscall.Fstat(anchorFD, &anchorStat) != nil {
			if anchorFD >= 0 {
				_ = syscall.Close(anchorFD)
			}
			return nil, ErrIntegrity
		}
		anchorFile = os.NewFile(uintptr(anchorFD), anchorName)
		observed := make([]byte, len(data))
		if anchorStat.Size != int64(len(data)) {
			err = ErrIntegrity
		} else if count, readErr := anchorFile.ReadAt(observed, 0); readErr != nil || count != len(observed) || !bytes.Equal(observed, data) {
			err = ErrIntegrity
		}
	}
	if err != nil {
		if anchorFile != nil {
			_ = anchorFile.Close()
		}
		return nil, err
	}
	anchorFD = int(anchorFile.Fd())
	var durableStat syscall.Stat_t
	if syscall.Fstat(anchorFD, &durableStat) != nil || durableStat.Size != int64(len(data)) ||
		durableStat.Dev != anchorStat.Dev || durableStat.Ino != anchorStat.Ino {
		_ = anchorFile.Close()
		return nil, ErrIntegrity
	}
	if err := verifyOpenAndNamedFile(parent, anchorName, anchorFD, uint64(anchorStat.Dev), anchorStat.Ino); err != nil {
		_ = anchorFile.Close()
		return nil, err
	}
	pin := &namespacePin{name: name, anchorName: anchorName, directory: directory, fd: fd,
		dev: uint64(objectStat.Dev), ino: objectStat.Ino, anchorFile: anchorFile,
		anchorDev: uint64(anchorStat.Dev), anchorIno: anchorStat.Ino, anchorData: append([]byte(nil), data...)}
	if err := j.verifyNamespacePin(parent, pin); err != nil {
		_ = anchorFile.Close()
		return nil, err
	}
	return pin, nil
}

func (j *Journal) publishPreparedNamespaceIdentity(parent int, authorityParent, name, objectType string,
	objectDev, objectIno uint64, anchorName string, data []byte, authorityName, authorityKind string) (*os.File, syscall.Stat_t, error) {
	if j == nil || j.rootFD < 0 || parent < 0 || objectIno == 0 || len(data) == 0 {
		return nil, syscall.Stat_t{}, ErrIntegrity
	}
	target := rootGenerationObjectIdentityV1{Parent: authorityParent, Name: name, ObjectType: objectType, Device: objectDev, Inode: objectIno}
	identityObject := rootGenerationObjectIdentityV1{Parent: authorityParent, Name: anchorName, ObjectType: "file"}
	prepared := preparedIdentityPublicationV1{
		Parent: authorityParent, IdentityName: anchorName, StageName: anchorName + ".prepared", IdentityIndex: 1,
		IdentitySHA256: sha256Hex(data), Objects: []rootGenerationObjectIdentityV1{target, identityObject},
	}
	authority, authorityData, found, err := readRootGenerationAuthority(j.rootFD, authorityName, authorityKind, j.rootDev, j.rootIno)
	if err != nil || !found || authority.State != rootGenerationInitializing {
		return nil, syscall.Stat_t{}, ErrIntegrity
	}
	anchorObject, completed, err := completedIdentityPublication(authority.Objects, target, identityObject)
	if err != nil {
		return nil, syscall.Stat_t{}, err
	}
	if completed {
		anchorFD, openErr := syscall.Openat(parent, anchorName, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if openErr != nil || verifyOpenAndNamedFile(parent, anchorName, anchorFD, anchorObject.Device, anchorObject.Inode) != nil {
			if anchorFD >= 0 {
				_ = syscall.Close(anchorFD)
			}
			return nil, syscall.Stat_t{}, ErrIntegrity
		}
		file := os.NewFile(uintptr(anchorFD), anchorName)
		stat, verifyErr := verifyIdentityPublicationBytes(file, data)
		if verifyErr != nil {
			_ = file.Close()
			return nil, syscall.Stat_t{}, verifyErr
		}
		return file, stat, nil
	}
	if authority.PreparedIdentity == nil {
		if identityPublicationObjectsOverlap(authority.Objects, prepared.Objects) || identityNameExists(parent, anchorName) {
			return nil, syscall.Stat_t{}, ErrIntegrity
		}
		next := authority
		next.PreparedIdentity = &prepared
		authorityData, err = j.replaceInitializingRootGeneration(authorityName, authorityKind, authorityData, next, anchorName, "intent")
		if err != nil {
			return nil, syscall.Stat_t{}, err
		}
		authority = next
	} else {
		if !samePreparedIdentityIntent(*authority.PreparedIdentity, prepared) {
			return nil, syscall.Stat_t{}, ErrIntegrity
		}
		prepared = clonePreparedIdentity(*authority.PreparedIdentity)
	}

	identityObject = prepared.Objects[prepared.IdentityIndex]
	anchorFD := -1
	published := false
	if identityObject.Inode == 0 {
		if identityNameExists(parent, anchorName) {
			return nil, syscall.Stat_t{}, ErrIntegrity
		}
		if err := discardUnboundIdentityStage(parent, prepared.StageName); err != nil {
			return nil, syscall.Stat_t{}, err
		}
		anchorFD, err = syscall.Openat(parent, prepared.StageName,
			syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
		if err != nil || validateFileFD(anchorFD) != nil {
			if anchorFD >= 0 {
				_ = syscall.Close(anchorFD)
			}
			return nil, syscall.Stat_t{}, ErrIntegrity
		}
		journalBootstrapBoundaryHook(anchorName, "identity-created")
		var stat syscall.Stat_t
		if syscall.Fstat(anchorFD, &stat) != nil || stat.Size != 0 {
			_ = syscall.Close(anchorFD)
			return nil, syscall.Stat_t{}, ErrIntegrity
		}
		prepared.Objects[prepared.IdentityIndex].Device = uint64(stat.Dev)
		prepared.Objects[prepared.IdentityIndex].Inode = stat.Ino
		next := authority
		next.PreparedIdentity = &prepared
		authorityData, err = j.replaceInitializingRootGeneration(authorityName, authorityKind, authorityData, next, anchorName, "identity-bound")
		if err != nil {
			_ = syscall.Close(anchorFD)
			return nil, syscall.Stat_t{}, err
		}
		authority = next
	} else {
		anchorFD, published, err = openBoundIdentityStage(parent, prepared)
		if err != nil {
			return nil, syscall.Stat_t{}, err
		}
	}

	file := os.NewFile(uintptr(anchorFD), prepared.StageName)
	stat, err := resumeIdentityPublication(j, parent, file, prepared, data, anchorName, published)
	if err != nil {
		_ = file.Close()
		return nil, syscall.Stat_t{}, err
	}
	if authority.PreparedIdentity == nil || !reflect.DeepEqual(*authority.PreparedIdentity, prepared) {
		_ = file.Close()
		return nil, syscall.Stat_t{}, ErrIntegrity
	}
	next := authority
	next.Objects = append(append([]rootGenerationObjectIdentityV1(nil), authority.Objects...), prepared.Objects...)
	next.PreparedIdentity = nil
	if _, err := j.replaceInitializingRootGeneration(authorityName, authorityKind, authorityData, next, anchorName, "identity-committed"); err != nil {
		_ = file.Close()
		return nil, syscall.Stat_t{}, err
	}
	return file, stat, nil
}

func (j *Journal) replaceInitializingRootGeneration(name, kind string, prior []byte, next rootGenerationAuthorityV1,
	identityName, phase string) ([]byte, error) {
	_, data, err := encodeRootGenerationAuthority(next, kind, j.rootDev, j.rootIno)
	if err != nil || next.State != rootGenerationInitializing {
		return nil, ErrIntegrity
	}
	observed, found, err := fgetRootXattr(j.rootFD, name)
	if err != nil || !found || !bytes.Equal(observed, prior) || fsetRootXattr(j.rootFD, name, data, rootGenerationXattrReplace) != nil {
		return nil, ErrIntegrity
	}
	journalBootstrapBoundaryHook(identityName, phase+"-visible")
	if err := j.io.syncDir("namespace-identity-authority", j.rootFD); err != nil {
		return nil, err
	}
	if err := verifyRootGenerationData(j.rootFD, name, data); err != nil {
		return nil, err
	}
	journalBootstrapBoundaryHook(identityName, phase+"-durable")
	return data, nil
}

func completedIdentityPublication(objects []rootGenerationObjectIdentityV1, target, identity rootGenerationObjectIdentityV1) (rootGenerationObjectIdentityV1, bool, error) {
	var foundTarget, foundIdentity *rootGenerationObjectIdentityV1
	for index := range objects {
		object := &objects[index]
		if object.Parent == target.Parent && object.Name == target.Name {
			foundTarget = object
		}
		if object.Parent == identity.Parent && object.Name == identity.Name {
			foundIdentity = object
		}
	}
	if foundTarget == nil && foundIdentity == nil {
		return rootGenerationObjectIdentityV1{}, false, nil
	}
	if foundTarget == nil || foundIdentity == nil || !reflect.DeepEqual(*foundTarget, target) || foundIdentity.ObjectType != "file" || foundIdentity.Inode == 0 {
		return rootGenerationObjectIdentityV1{}, false, ErrIntegrity
	}
	return *foundIdentity, true, nil
}

func identityPublicationObjectsOverlap(existing, next []rootGenerationObjectIdentityV1) bool {
	for _, left := range existing {
		for _, right := range next {
			if left.Parent == right.Parent && left.Name == right.Name {
				return true
			}
		}
	}
	return false
}

func samePreparedIdentityIntent(observed, expected preparedIdentityPublicationV1) bool {
	if observed.IdentityIndex < 0 || observed.IdentityIndex >= len(observed.Objects) || expected.IdentityIndex != observed.IdentityIndex {
		return false
	}
	expected.Objects[expected.IdentityIndex].Device = observed.Objects[observed.IdentityIndex].Device
	expected.Objects[expected.IdentityIndex].Inode = observed.Objects[observed.IdentityIndex].Inode
	return reflect.DeepEqual(observed, expected)
}

func clonePreparedIdentity(value preparedIdentityPublicationV1) preparedIdentityPublicationV1 {
	value.Objects = append([]rootGenerationObjectIdentityV1(nil), value.Objects...)
	return value
}

func identityNameExists(parent int, name string) bool {
	fd, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err == nil {
		_ = syscall.Close(fd)
	}
	return err != syscall.ENOENT
}

func discardUnboundIdentityStage(parent int, name string) error {
	fd, err := syscall.Openat(parent, name, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err == syscall.ENOENT {
		return nil
	}
	var stat syscall.Stat_t
	if err != nil || syscall.Fstat(fd, &stat) != nil || validateFileFD(fd) != nil || stat.Size != 0 ||
		verifyOpenAndNamedFile(parent, name, fd, uint64(stat.Dev), stat.Ino) != nil {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return ErrIntegrity
	}
	if err := syscall.Close(fd); err != nil || syscall.Unlinkat(parent, name) != nil || syscall.Fsync(parent) != nil {
		return ErrIntegrity
	}
	return nil
}

func openBoundIdentityStage(parent int, prepared preparedIdentityPublicationV1) (int, bool, error) {
	identity := prepared.Objects[prepared.IdentityIndex]
	openExact := func(name string) (int, bool, error) {
		fd, err := syscall.Openat(parent, name, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err == syscall.ENOENT {
			return -1, false, nil
		}
		if err != nil || verifyOpenAndNamedFile(parent, name, fd, identity.Device, identity.Inode) != nil {
			if fd >= 0 {
				_ = syscall.Close(fd)
			}
			return -1, false, ErrIntegrity
		}
		return fd, true, nil
	}
	stageFD, stageFound, err := openExact(prepared.StageName)
	if err != nil {
		return -1, false, err
	}
	finalFD, finalFound, err := openExact(prepared.IdentityName)
	if err != nil {
		if stageFound {
			_ = syscall.Close(stageFD)
		}
		return -1, false, err
	}
	if stageFound == finalFound {
		if stageFound {
			_ = syscall.Close(stageFD)
			_ = syscall.Close(finalFD)
		}
		return -1, false, ErrIntegrity
	}
	if finalFound {
		return finalFD, true, nil
	}
	return stageFD, false, nil
}

func resumeIdentityPublication(j *Journal, parent int, file *os.File, prepared preparedIdentityPublicationV1, data []byte,
	identityName string, published bool) (syscall.Stat_t, error) {
	identity := prepared.Objects[prepared.IdentityIndex]
	var stat syscall.Stat_t
	if syscall.Fstat(int(file.Fd()), &stat) != nil || validateFileFD(int(file.Fd())) != nil || uint64(stat.Dev) != identity.Device ||
		stat.Ino != identity.Inode || stat.Size < 0 || stat.Size > int64(len(data)) {
		return syscall.Stat_t{}, ErrIntegrity
	}
	prefix := make([]byte, int(stat.Size))
	if len(prefix) > 0 {
		if count, err := file.ReadAt(prefix, 0); err != nil || count != len(prefix) || !bytes.Equal(prefix, data[:len(prefix)]) {
			return syscall.Stat_t{}, ErrIntegrity
		}
	}
	if published && stat.Size != int64(len(data)) {
		return syscall.Stat_t{}, ErrIntegrity
	}
	if !published && stat.Size < int64(len(data)) {
		if _, err := file.Seek(stat.Size, io.SeekStart); err != nil {
			return syscall.Stat_t{}, ErrIntegrity
		}
		split := len(data) / 2
		if split == 0 {
			split = 1
		}
		written := int(stat.Size)
		if written < split {
			if err := j.io.write("namespace-identity", file, data[written:split]); err != nil {
				return syscall.Stat_t{}, err
			}
			written = split
			journalBootstrapBoundaryHook(identityName, "identity-partial")
		}
		if written < len(data) {
			if err := j.io.write("namespace-identity", file, data[written:]); err != nil {
				return syscall.Stat_t{}, err
			}
		}
		journalBootstrapBoundaryHook(identityName, "identity-complete-visible")
	}
	if err := j.io.syncFile("namespace-identity", file); err != nil {
		return syscall.Stat_t{}, err
	}
	journalBootstrapBoundaryHook(identityName, "identity-file-durable")
	if err := j.io.syncDir("namespace-identity-stage", parent); err != nil {
		return syscall.Stat_t{}, err
	}
	journalBootstrapBoundaryHook(identityName, "identity-stage-durable")
	if !published {
		if syscall.Renameat(parent, prepared.StageName, parent, prepared.IdentityName) != nil ||
			verifyOpenAndNamedFile(parent, prepared.IdentityName, int(file.Fd()), identity.Device, identity.Inode) != nil {
			return syscall.Stat_t{}, ErrIntegrity
		}
		journalBootstrapBoundaryHook(identityName, "identity-published")
		if err := j.io.syncDir("namespace-identity-publish", parent); err != nil {
			return syscall.Stat_t{}, err
		}
		journalBootstrapBoundaryHook(identityName, "identity-publication-durable")
	}
	return verifyIdentityPublicationBytes(file, data)
}

func verifyIdentityPublicationBytes(file *os.File, data []byte) (syscall.Stat_t, error) {
	var stat syscall.Stat_t
	if file == nil || syscall.Fstat(int(file.Fd()), &stat) != nil || validateFileFD(int(file.Fd())) != nil || stat.Size != int64(len(data)) {
		return syscall.Stat_t{}, ErrIntegrity
	}
	observed := make([]byte, len(data))
	if count, err := file.ReadAt(observed, 0); err != nil || count != len(observed) || !bytes.Equal(observed, data) {
		return syscall.Stat_t{}, ErrIntegrity
	}
	return stat, nil
}

func closeNamespacePin(pin *namespacePin) error {
	if pin == nil {
		return nil
	}
	var anchorErr error
	if pin.anchorFile != nil {
		anchorErr = pin.anchorFile.Close()
		pin.anchorFile = nil
	}
	var objectErr error
	if pin.fd >= 0 {
		objectErr = syscall.Close(pin.fd)
		pin.fd = -1
	}
	return errors.Join(anchorErr, objectErr)
}

func (j *Journal) verifyNamespacePin(parent int, pin *namespacePin) error {
	if pin == nil || pin.fd < 0 || pin.anchorFile == nil {
		return ErrIntegrity
	}
	var objectStat syscall.Stat_t
	if syscall.Fstat(pin.fd, &objectStat) != nil || uint64(objectStat.Dev) != pin.dev || objectStat.Ino != pin.ino {
		return ErrIntegrity
	}
	if pin.directory {
		if validateDirectoryFD(pin.fd, false) != nil {
			return ErrIntegrity
		}
	} else if validateFileFD(pin.fd) != nil {
		return ErrIntegrity
	}
	opened, err := j.openNamespaceAt(parent, pin.name, pin.directory)
	if err != nil {
		return err
	}
	var namedStat syscall.Stat_t
	statErr := syscall.Fstat(opened, &namedStat)
	closeErr := syscall.Close(opened)
	if statErr != nil || closeErr != nil || uint64(namedStat.Dev) != pin.dev || namedStat.Ino != pin.ino {
		return ErrIntegrity
	}
	anchorFD := int(pin.anchorFile.Fd())
	var anchorStat syscall.Stat_t
	if syscall.Fstat(anchorFD, &anchorStat) != nil || validateFileFD(anchorFD) != nil ||
		uint64(anchorStat.Dev) != pin.anchorDev || anchorStat.Ino != pin.anchorIno || anchorStat.Size != int64(len(pin.anchorData)) {
		return ErrIntegrity
	}
	if err := verifyOpenAndNamedFile(parent, pin.anchorName, anchorFD, pin.anchorDev, pin.anchorIno); err != nil {
		return err
	}
	observed := make([]byte, len(pin.anchorData))
	if count, readErr := pin.anchorFile.ReadAt(observed, 0); readErr != nil || count != len(observed) || !bytes.Equal(observed, pin.anchorData) {
		return ErrIntegrity
	}
	if pin.rootAuthorityName != "" {
		if err := j.verifyShardGenerationAuthority(pin); err != nil {
			return err
		}
	}
	return nil
}

func (j *Journal) verifyShardGenerationAuthority(pin *namespacePin) error {
	if pin == nil || pin.rootAuthorityName == "" || pin.historyName != requestIssuanceHistoryName || pin.historyIno == 0 {
		return ErrIntegrity
	}
	historyFD, err := syscall.Openat(pin.fd, pin.historyName, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ErrIntegrity
	}
	defer syscall.Close(historyFD)
	var historyStat syscall.Stat_t
	if syscall.Fstat(historyFD, &historyStat) != nil || validateRequestIssuanceHistoryStat(historyStat) != nil ||
		uint64(historyStat.Dev) != pin.historyDev || historyStat.Ino != pin.historyIno {
		return ErrIntegrity
	}
	if err := verifyOpenAndNamedFile(pin.fd, pin.historyName, historyFD, pin.historyDev, pin.historyIno); err != nil {
		return err
	}
	authority, authorityData, found, err := readRootGenerationAuthority(j.rootFD, pin.rootAuthorityName, journalShardAuthorityKind, j.rootDev, j.rootIno)
	if err != nil || !found || authority.State != rootGenerationEstablished {
		return ErrIntegrity
	}
	_, expectedData, err := makeShardGenerationAuthority(j.rootDev, j.rootIno,
		rootGenerationObjectsForShardPin("actions/"+requestIndexDirectory, pin), authority.IssuanceCount, authority.IssuanceFinalRecordSHA256)
	if err != nil || !bytes.Equal(authorityData, expectedData) {
		return ErrIntegrity
	}
	return nil
}

func (j *Journal) openNamespaceAt(parent int, name string, directory bool) (int, error) {
	flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	if directory {
		flags |= syscall.O_DIRECTORY
	} else {
		flags = syscall.O_RDWR | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	}
	fd, err := syscall.Openat(parent, name, flags, 0)
	if err != nil {
		return -1, ErrIntegrity
	}
	if directory {
		err = validateDirectoryFD(fd, false)
	} else {
		err = validateFileFD(fd)
	}
	if err != nil {
		syscall.Close(fd)
		return -1, ErrIntegrity
	}
	return fd, nil
}

func (j *Journal) openPinnedNamespace(parent int, pin *namespacePin) (int, error) {
	if err := j.verifyNamespacePin(parent, pin); err != nil {
		return -1, err
	}
	fd, err := j.openNamespaceAt(parent, pin.name, pin.directory)
	if err != nil {
		return -1, err
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || uint64(stat.Dev) != pin.dev || stat.Ino != pin.ino {
		syscall.Close(fd)
		return -1, ErrIntegrity
	}
	return fd, nil
}

func (j *Journal) verifyNamespaces() error {
	if err := j.verifyRoot(); err != nil {
		return err
	}
	if err := j.verifyNamespacePin(j.rootFD, j.actions); err != nil {
		return err
	}
	if err := j.verifyNamespacePin(j.actions.fd, j.lockFile); err != nil {
		return err
	}
	if err := j.verifyNamespacePin(j.actions.fd, j.requestIndex); err != nil {
		return err
	}
	if err := j.verifyNamespacePin(j.actions.fd, j.runHistory); err != nil {
		return err
	}
	if err := j.verifyRunGenerationAuthority(); err != nil {
		return err
	}
	j.shardMu.Lock()
	defer j.shardMu.Unlock()
	for _, shard := range j.shards {
		if shard != nil {
			if err := j.verifyNamespacePin(j.requestIndex.fd, shard); err != nil {
				return err
			}
		}
	}
	return nil
}

func (j *Journal) openRootLock() (int, error) {
	fd, err := syscall.Openat(j.rootFD, ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil || validateDirectoryFD(fd, true) != nil {
		if fd >= 0 {
			syscall.Close(fd)
		}
		return -1, ErrIntegrity
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || uint64(stat.Dev) != j.rootDev || stat.Ino != j.rootIno {
		syscall.Close(fd)
		return -1, ErrIntegrity
	}
	return fd, nil
}

func (j *Journal) flockContext(ctx context.Context, fd int, deadline time.Time) error {
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
		if j.failedClosed.Load() {
			return ErrIntegrity
		}
		select {
		case <-ctx.Done():
			return errors.Join(ErrBusy, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (g *journalGuard) verifyNamespace() error {
	if g == nil || g.journal == nil || g.rootLockFD < 0 || g.actionsFD < 0 || g.requestsFD < 0 || g.runHistoryFD < 0 || g.lockFD < 0 {
		return ErrIntegrity
	}
	if err := g.journal.verifyNamespaces(); err != nil {
		return err
	}
	for _, identity := range []struct {
		fd  int
		dev uint64
		ino uint64
	}{
		{g.rootLockFD, g.journal.rootDev, g.journal.rootIno},
		{g.actionsFD, g.journal.actions.dev, g.journal.actions.ino},
		{g.requestsFD, g.journal.requestIndex.dev, g.journal.requestIndex.ino},
		{g.runHistoryFD, g.journal.runHistory.dev, g.journal.runHistory.ino},
		{g.lockFD, g.journal.lockFile.dev, g.journal.lockFile.ino},
	} {
		var stat syscall.Stat_t
		if syscall.Fstat(identity.fd, &stat) != nil || uint64(stat.Dev) != identity.dev || stat.Ino != identity.ino {
			return ErrIntegrity
		}
	}
	if g.runFD >= 0 {
		return g.verifyRunNamespace()
	}
	return nil
}

func (g *journalGuard) openIndexShard(name string, create bool) (int, error) {
	decoded, err := hex.DecodeString(name)
	if err != nil || len(decoded) != 1 || hex.EncodeToString(decoded) != name {
		return -1, ErrIntegrity
	}
	index := int(decoded[0])
	g.journal.shardMu.Lock()
	if pin := g.journal.shards[index]; pin != nil {
		fd, err := g.journal.openPinnedNamespace(g.requestsFD, pin)
		g.journal.shardMu.Unlock()
		return fd, err
	}
	pin, err := g.journal.openShardGeneration(g.requestsFD, name, create, true)
	if err != nil {
		g.journal.shardMu.Unlock()
		return -1, err
	}
	g.journal.shards[index] = pin
	g.journal.shardMu.Unlock()
	opened, err := g.journal.openPinnedNamespace(g.requestsFD, pin)
	if err != nil {
		return -1, err
	}
	if err := g.verifyNamespace(); err != nil {
		syscall.Close(opened)
		return -1, err
	}
	return opened, nil
}

func (j *Journal) loadEstablishedShards(rootLocked bool) error {
	if j == nil || j.requestIndex == nil {
		return ErrIntegrity
	}
	for index := 0; index < RequestIndexShards; index++ {
		name := fmt.Sprintf("%02x", index)
		pin, err := j.openShardGeneration(j.requestIndex.fd, name, false, rootLocked)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		j.shards[index] = pin
	}
	return nil
}

func (j *Journal) openShardGeneration(parent int, name string, create, rootLocked bool) (*namespacePin, error) {
	authorityName := journalShardAuthorityXattr(name)
	authority, authorityData, found, err := readRootGenerationAuthority(j.rootFD, authorityName, journalShardAuthorityKind, j.rootDev, j.rootIno)
	if err != nil {
		return nil, err
	}
	anchorName := "." + name + ".identity.json"
	ownedRootLock := false
	defer func() {
		if ownedRootLock {
			_ = syscall.Flock(j.rootFD, syscall.LOCK_UN)
		}
	}()
	if !rootLocked && found && authority.State == rootGenerationInitializing {
		if err := lockRootGeneration(j.rootFD, JournalLockTimeout); err != nil {
			return nil, err
		}
		ownedRootLock = true
		rootLocked = true
		authority, authorityData, found, err = readRootGenerationAuthority(j.rootFD, authorityName, journalShardAuthorityKind, j.rootDev, j.rootIno)
		if err != nil {
			return nil, err
		}
	}
	if !found {
		absentErr := requireGenerationEntriesAbsent(parent,
			generationEntry{name: name, directory: true},
			generationEntry{name: anchorName})
		if absentErr != nil && !rootLocked {
			if err := lockRootGeneration(j.rootFD, JournalLockTimeout); err != nil {
				return nil, err
			}
			ownedRootLock = true
			rootLocked = true
			authority, authorityData, found, err = readRootGenerationAuthority(j.rootFD, authorityName, journalShardAuthorityKind, j.rootDev, j.rootIno)
			if err != nil {
				return nil, err
			}
			if !found {
				absentErr = requireGenerationEntriesAbsent(parent,
					generationEntry{name: name, directory: true},
					generationEntry{name: anchorName})
			}
		}
		if !found && absentErr != nil {
			return nil, absentErr
		}
		if !found && !create {
			return nil, ErrNotFound
		}
		if !found {
			if !rootLocked {
				if err := lockRootGeneration(j.rootFD, JournalLockTimeout); err != nil {
					return nil, err
				}
				ownedRootLock = true
				rootLocked = true
			}
			authority, authorityData, err = createInitializingRootGeneration(j.rootFD, authorityName, journalShardAuthorityKind, j.rootDev, j.rootIno)
			if err != nil {
				return nil, err
			}
		}
	}
	allowCreate := authority.State == rootGenerationInitializing
	fd, err := j.openDirectoryAt(parent, name, allowCreate)
	if err != nil {
		if found && errors.Is(err, ErrNotFound) {
			return nil, ErrIntegrity
		}
		return nil, err
	}
	pin, err := j.pinNamespace(parent, "actions/"+requestIndexDirectory, name, anchorName, fd, true, allowCreate,
		authorityName, journalShardAuthorityKind)
	if err != nil {
		syscall.Close(fd)
		return nil, err
	}
	historyStat, err := j.openShardIssuanceHistory(pin.fd, allowCreate)
	if err != nil {
		_ = closeNamespacePin(pin)
		return nil, err
	}
	pin.historyName, pin.historyDev, pin.historyIno = requestIssuanceHistoryName, uint64(historyStat.Dev), historyStat.Ino
	objects := rootGenerationObjectsForShardPin("actions/"+requestIndexDirectory, pin)
	count, finalDigest := authority.IssuanceCount, authority.IssuanceFinalRecordSHA256
	if allowCreate {
		count, finalDigest = 0, ""
	}
	expectedAuthority, expectedData, err := makeShardGenerationAuthority(j.rootDev, j.rootIno, objects, count, finalDigest)
	if err != nil {
		_ = closeNamespacePin(pin)
		return nil, err
	}
	if allowCreate {
		authorityData, err = publishRootGeneration(j.rootFD, authorityName, expectedAuthority, expectedData)
	} else if !bytes.Equal(authorityData, expectedData) {
		err = ErrIntegrity
	}
	if err != nil {
		_ = closeNamespacePin(pin)
		return nil, err
	}
	pin.rootAuthorityName = authorityName
	pin.rootAuthorityData = append([]byte(nil), authorityData...)
	if err := j.verifyNamespacePin(parent, pin); err != nil {
		_ = closeNamespacePin(pin)
		return nil, err
	}
	return pin, nil
}

func (j *Journal) openShardIssuanceHistory(shardFD int, allowCreate bool) (syscall.Stat_t, error) {
	var stat syscall.Stat_t
	flags := syscall.O_RDWR | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	if allowCreate {
		flags |= syscall.O_CREAT | syscall.O_EXCL
	}
	fd, err := syscall.Openat(shardFD, requestIssuanceHistoryName, flags, 0o600)
	created := allowCreate && err == nil
	if allowCreate && err == syscall.EEXIST {
		fd, err = syscall.Openat(shardFD, requestIssuanceHistoryName, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	}
	if err != nil || syscall.Fstat(fd, &stat) != nil || validateRequestIssuanceHistoryStat(stat) != nil || (allowCreate && stat.Size != 0) {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return syscall.Stat_t{}, ErrIntegrity
	}
	file := os.NewFile(uintptr(fd), requestIssuanceHistoryName)
	if err := j.io.syncFile("request-issuance-generation", file); err != nil {
		_ = file.Close()
		return syscall.Stat_t{}, err
	}
	if err := verifyOpenAndNamedFile(shardFD, requestIssuanceHistoryName, fd, uint64(stat.Dev), stat.Ino); err != nil {
		_ = file.Close()
		return syscall.Stat_t{}, err
	}
	if err := file.Close(); err != nil {
		return syscall.Stat_t{}, err
	}
	if created {
		if err := j.io.syncDir("request-issuance-generation", shardFD); err != nil {
			return syscall.Stat_t{}, err
		}
	}
	return stat, nil
}

func journalShardAuthorityXattr(name string) string {
	return "user.abcp.actioncontrol.journal-request-shard-" + name + "-generation-v1"
}

func (j *Journal) verifyRoot() error {
	if j == nil || j.rootFD < 0 || len(j.rootAuthorityData) == 0 {
		return ErrIntegrity
	}
	fd, err := openAbsoluteDirectory(j.root)
	if err != nil {
		return ErrIntegrity
	}
	defer syscall.Close(fd)
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || uint64(stat.Dev) != j.rootDev || stat.Ino != j.rootIno {
		return ErrIntegrity
	}
	if err := validateDirectoryFD(fd, true); err != nil {
		return err
	}
	return verifyRootGenerationData(j.rootFD, journalRootAuthorityXattr, j.rootAuthorityData)
}

func openAbsoluteDirectory(path string) (int, error) {
	fd, err := syscall.Open(string(filepath.Separator), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, openErr := syscall.Openat(fd, component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		syscall.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func (j *Journal) openDirectoryAt(parent int, name string, create bool) (int, error) {
	fd, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err == syscall.ENOENT && create {
		if err := syscall.Mkdirat(parent, name, 0o700); err != nil && err != syscall.EEXIST {
			return -1, ErrIntegrity
		}
		fd, err = syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	}
	if err == syscall.ENOENT && !create {
		return -1, ErrNotFound
	}
	if err != nil || validateDirectoryFD(fd, false) != nil {
		if fd >= 0 {
			syscall.Close(fd)
		}
		return -1, ErrIntegrity
	}
	if create {
		// Sync on create-or-open so an earlier ambiguous mkdir can be safely
		// re-proven by the next attempt.
		if syncErr := j.io.syncDir("directory", parent); syncErr != nil {
			syscall.Close(fd)
			return -1, syncErr
		}
	}
	return fd, nil
}

func verifyOpenAndNamedDirectory(parent int, name string, fd int, dev, ino uint64) error {
	if validateDirectoryFD(fd, false) != nil {
		return ErrIntegrity
	}
	var current syscall.Stat_t
	if syscall.Fstat(fd, &current) != nil || uint64(current.Dev) != dev || current.Ino != ino {
		return ErrIntegrity
	}
	reopened, err := syscall.Openat(parent, name,
		syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ErrIntegrity
	}
	defer syscall.Close(reopened)
	var named syscall.Stat_t
	if syscall.Fstat(reopened, &named) != nil || validateDirectoryFD(reopened, false) != nil ||
		uint64(named.Dev) != dev || named.Ino != ino {
		return ErrIntegrity
	}
	return nil
}

func removeDirectoryAt(parent int, name string) error {
	pointer, err := syscall.BytePtrFromString(name)
	if err != nil {
		return ErrIntegrity
	}
	const atRemovedir = 0x200
	_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, uintptr(parent), uintptr(unsafe.Pointer(pointer)), uintptr(atRemovedir))
	if errno != 0 {
		return errno
	}
	return nil
}

func (j *Journal) ensureRunGenerationHistory(actionsFD int, allowCreate bool) error {
	flags := syscall.O_RDWR | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	if allowCreate {
		flags |= syscall.O_CREAT | syscall.O_EXCL
	}
	fd, err := syscall.Openat(actionsFD, runGenerationHistoryName, flags, 0o600)
	created := allowCreate && err == nil
	if allowCreate && err == syscall.EEXIST {
		fd, err = syscall.Openat(actionsFD, runGenerationHistoryName, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	}
	var stat syscall.Stat_t
	if err != nil || syscall.Fstat(fd, &stat) != nil || validateRunGenerationHistoryStat(stat) != nil || (created && stat.Size != 0) {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return ErrIntegrity
	}
	file := os.NewFile(uintptr(fd), runGenerationHistoryName)
	if err := j.io.syncFile("run-generation-history", file); err != nil {
		_ = file.Close()
		return err
	}
	if err := verifyOpenAndNamedFile(actionsFD, runGenerationHistoryName, fd, uint64(stat.Dev), stat.Ino); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if created {
		return j.io.syncDir("run-generation-history", actionsFD)
	}
	return nil
}

func (j *Journal) ensureLockFile(actionsFD int, allowCreate bool) error {
	flags := syscall.O_RDWR | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	if allowCreate {
		flags |= syscall.O_CREAT | syscall.O_EXCL
	}
	fd, err := syscall.Openat(actionsFD, ".lock", flags, 0o600)
	if allowCreate && err == syscall.EEXIST {
		fd, err = syscall.Openat(actionsFD, ".lock", syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	}
	if err != nil || validateFileFD(fd) != nil {
		if fd >= 0 {
			syscall.Close(fd)
		}
		return ErrIntegrity
	}
	file := os.NewFile(uintptr(fd), ".lock")
	if err := j.io.syncFile("action-lock", file); err != nil {
		_ = file.Close()
		return err
	}
	if err := j.io.syncDir("action-lock", actionsFD); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func validateDirectoryFD(fd int, root bool) error {
	if fd < 0 {
		return ErrIntegrity
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFDIR || stat.Mode&0o777 != 0o700 || stat.Uid != uint32(os.Geteuid()) {
		return ErrIntegrity
	}
	_ = root
	return nil
}

func validateFileFD(fd int) error {
	if fd < 0 {
		return ErrIntegrity
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Mode&0o777 != 0o600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return ErrIntegrity
	}
	return nil
}

func validateProtectedFileStat(stat syscall.Stat_t) error {
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Mode&0o777 != 0o600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return ErrIntegrity
	}
	return nil
}

func verifyOpenAndNamedFile(parent int, name string, fd int, dev uint64, ino uint64) error {
	var current syscall.Stat_t
	if syscall.Fstat(fd, &current) != nil || validateProtectedFileStat(current) != nil || current.Dev != dev || current.Ino != ino {
		return ErrIntegrity
	}
	reopened, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ErrIntegrity
	}
	defer syscall.Close(reopened)
	var named syscall.Stat_t
	if syscall.Fstat(reopened, &named) != nil || validateProtectedFileStat(named) != nil || named.Dev != dev || named.Ino != ino {
		return ErrIntegrity
	}
	return nil
}

func readProtectedFileAt(parent int, name string, limit int64) ([]byte, error) {
	fd, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil || validateFileFD(fd) != nil {
		if fd >= 0 {
			syscall.Close(fd)
		}
		return nil, ErrIntegrity
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, ErrIntegrity
	}
	return data, nil
}

func validateFileInfo(info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return ErrIntegrity
	}
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if value.IsValid() {
		if field := value.FieldByName("Nlink"); field.IsValid() && field.Uint() != 1 {
			return ErrIntegrity
		}
		if field := value.FieldByName("Uid"); field.IsValid() && field.Uint() != uint64(os.Geteuid()) {
			return ErrIntegrity
		}
	}
	return nil
}

func writeFull(file *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := file.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

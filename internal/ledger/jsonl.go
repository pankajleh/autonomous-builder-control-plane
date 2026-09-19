package ledger

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"
)

const (
	ledgerFileLockWait = 2 * time.Second
	maxLedgerLineBytes = 256 << 10
	maxLedgerBytes     = 64 << 20
	maxLedgerRecords   = 262144
)

// ErrRegisteredGenerationChanged identifies a service open whose immutable
// catalog generation no longer names the same parent and ledger objects.
var ErrRegisteredGenerationChanged = errors.New("registered ledger generation changed")

type JSONLLedger struct {
	path       string
	parentPath string
	file       *os.File
	parent     *os.File
	runLocks   *runTransitionNamespace
	fileInfo   os.FileInfo
	parentInfo os.FileInfo
	physicalID string
	expected   *PhysicalGeneration
	readOnly   bool
	// snapshotCoordinator is immutable after construction. Service-owned
	// writable ledgers use it to share the global authoritative-snapshot gate.
	snapshotCoordinator func(func() error) error
	closed              bool
	mu                  sync.Mutex
}

// PhysicalGeneration identifies the exact parent directory and ledger file
// objects pinned by a ledger. Registered service opens require this complete
// generation; standalone ledger constructors remain path based.
type PhysicalGeneration struct {
	ParentDevice uint64
	ParentInode  uint64
	FileDevice   uint64
	FileInode    uint64
}

// Valid reports whether the generation contains all four physical identity
// components.
func (g PhysicalGeneration) Valid() bool {
	return g.ParentDevice != 0 && g.ParentInode != 0 && g.FileDevice != 0 && g.FileInode != 0
}

func NewJSONLLedger(path string) (*JSONLLedger, error) {
	return openJSONLLedger(path, true, false, nil, nil)
}

// OpenExistingJSONLLedger opens an existing authoritative ledger for a
// governed writer. Unlike NewJSONLLedger, it never creates the parent or the
// ledger file and it pins both identities before returning.
func OpenExistingJSONLLedger(path string) (*JSONLLedger, error) {
	return openJSONLLedger(path, false, false, nil, nil)
}

// OpenRegisteredJSONLLedger opens an existing writable ledger only when its
// named parent and file still match immutable catalog authority.
func OpenRegisteredJSONLLedger(path string, generation PhysicalGeneration) (*JSONLLedger, error) {
	value, err := openJSONLLedger(path, false, false, nil, &generation)
	if err != nil {
		return nil, errors.Join(ErrRegisteredGenerationChanged, err)
	}
	return value, nil
}

// OpenExistingJSONLLedgerWithSnapshotCoordinator opens an existing writable
// ledger and routes each Snapshot call through coordinator. The coordinator
// may reject a snapshot without invoking it; otherwise it must invoke and
// return the supplied bounded snapshot operation exactly once.
func OpenExistingJSONLLedgerWithSnapshotCoordinator(path string, coordinator func(func() error) error) (*JSONLLedger, error) {
	if coordinator == nil {
		return nil, errors.New("snapshot coordinator is required")
	}
	return openJSONLLedger(path, false, false, coordinator, nil)
}

// OpenRegisteredJSONLLedgerWithSnapshotCoordinator is the generation-bound
// service writer. It cannot adopt a coherently replaced ledger namespace.
func OpenRegisteredJSONLLedgerWithSnapshotCoordinator(path string, generation PhysicalGeneration, coordinator func(func() error) error) (*JSONLLedger, error) {
	if coordinator == nil {
		return nil, errors.New("snapshot coordinator is required")
	}
	value, err := openJSONLLedger(path, false, false, coordinator, &generation)
	if err != nil {
		return nil, errors.Join(ErrRegisteredGenerationChanged, err)
	}
	return value, nil
}

func openJSONLLedger(path string, createParent, readOnly bool, snapshotCoordinator func(func() error) error, expected *PhysicalGeneration) (*JSONLLedger, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("canonical absolute ledger path is required")
	}
	if expected != nil && !expected.Valid() {
		return nil, errors.New("complete registered ledger generation is required")
	}
	parentPath := filepath.Dir(path)
	if createParent {
		if err := os.MkdirAll(parentPath, 0o700); err != nil {
			return nil, fmt.Errorf("create ledger directory: %w", err)
		}
	}
	resolvedParent, err := filepath.EvalSymlinks(parentPath)
	if err != nil || filepath.Clean(resolvedParent) != parentPath {
		return nil, errors.Join(errors.New("ledger parent traversal contains a symbolic link"), err)
	}
	parent, err := os.Open(parentPath)
	if err != nil {
		return nil, fmt.Errorf("open ledger parent: %w", err)
	}
	parentInfo, err := parent.Stat()
	if err != nil || !parentInfo.IsDir() {
		_ = parent.Close()
		return nil, errors.Join(errors.New("ledger parent is not a directory"), err)
	}
	parentNameInfo, err := os.Lstat(parentPath)
	if err != nil || parentNameInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(parentInfo, parentNameInfo) {
		_ = parent.Close()
		return nil, errors.Join(errors.New("ledger parent is unsafe or replaced"), err)
	}
	exists := false
	if existing, statErr := os.Lstat(path); statErr == nil {
		if existing.Mode()&os.ModeSymlink != 0 || verifyLedgerFileInfo(existing) != nil {
			_ = parent.Close()
			return nil, errors.New("existing ledger file is unsafe")
		}
		exists = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		_ = parent.Close()
		return nil, statErr
	}
	result := &JSONLLedger{
		path: path, parentPath: parentPath, parent: parent, parentInfo: parentInfo,
		readOnly: readOnly, snapshotCoordinator: snapshotCoordinator,
	}
	if expected != nil {
		copy := *expected
		result.expected = &copy
	}
	if exists || !createParent {
		result.mu.Lock()
		err = result.pinLedgerFileLocked(false)
		if err == nil {
			err = result.verifyPhysicalIdentityLocked()
		}
		result.mu.Unlock()
		if err != nil {
			return nil, errors.Join(err, result.Close())
		}
	}
	return result, nil
}

func (l *JSONLLedger) Append(event Event) error {
	return l.appendOrVerify(event, "", false, nil)
}

// AppendOrVerify durably appends event, or succeeds when the byte-identical
// event is already present. A repeated EventID with different bytes is an
// integrity error. State transitions remain subject to an unresolved barrier.
func (l *JSONLLedger) AppendOrVerify(event Event) error {
	return l.appendOrVerify(event, "", true, nil)
}

// AppendOrVerifyTransition is the only append path permitted through an
// unresolved transition barrier. The supplied barrier digest must identify
// the active barrier for the event's run. This lets recovery finish the one
// predetermined merge transition without opening a competing writer window.
func (l *JSONLLedger) AppendOrVerifyTransition(event Event, barrierSHA256 string) error {
	if barrierSHA256 == "" {
		return errors.New("transition barrier digest is required")
	}
	return l.appendOrVerify(event, barrierSHA256, true, nil)
}

// AppendOrVerifyLeased appends while the caller owns the exact run-transition
// lease. It is used for pre-submission terminal selection; barriers use the
// stricter AppendOrVerifyTransition path instead.
func (l *JSONLLedger) AppendOrVerifyLeased(event Event, lease *RunTransitionLease) error {
	if lease == nil {
		return errors.New("exact run-transition lease is required")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed || lease.ledger != l || lease.runID != event.RunID {
		return errors.New("exact run-transition lease is required")
	}
	return l.appendOrVerify(event, leasedAppendSentinel, true, lease)
}

func (l *JSONLLedger) appendOrVerify(event Event, barrierSHA256 string, verifyExisting bool, suppliedLease *RunTransitionLease) (resultErr error) {
	if l == nil {
		return errors.New("ledger is required")
	}
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate event: %w", err)
	}
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	var transitionLease *RunTransitionLease
	if event.StateFrom != "" && barrierSHA256 == "" {
		transitionLease, err = l.AcquireRunTransition(event.RunID)
		if err != nil {
			return err
		}
		defer func() {
			resultErr = errors.Join(resultErr, transitionLease.Close())
		}()
		suppliedLease = transitionLease
	}

	return l.withFileLock(nil, true, func(f *os.File) error {
		if suppliedLease != nil {
			if suppliedLease.closed || suppliedLease.ledger != l || suppliedLease.runID != event.RunID {
				return errors.New("exact run-transition lease is required")
			}
			if err := l.verifyRunTransitionLeaseLocked(suppliedLease); err != nil {
				return err
			}
		}
		if err := l.authorizeTransition(event, barrierSHA256); err != nil {
			return err
		}
		found := false
		if verifyExisting {
			found, err = scanExactEvent(f, event.EventID, line)
			if err != nil {
				return err
			}
		}
		if found {
			if err := f.Sync(); err != nil {
				return fmt.Errorf("fsync existing ledger event: %w", err)
			}
			return nil
		}
		info, err := f.Stat()
		if err != nil {
			return fmt.Errorf("stat ledger: %w", err)
		}
		encoded := append(append(make([]byte, 0, len(line)+1), line...), '\n')
		if len(encoded) > maxLedgerLineBytes || info.Size() > maxLedgerBytes-int64(len(encoded)) {
			return errors.New("ledger append exceeds bounded size")
		}
		if err := writeFull(f, encoded); err != nil {
			return fmt.Errorf("append ledger: %w", err)
		}
		if err := f.Sync(); err != nil {
			return fmt.Errorf("fsync ledger: %w", err)
		}
		if verifyExisting {
			found, err = scanExactEvent(f, event.EventID, line)
			if err != nil || !found {
				return errors.Join(errors.New("appended ledger event was not confirmed"), err)
			}
		}
		return nil
	})
}

func (l *JSONLLedger) Path() string { return l.path }

// PhysicalIdentity returns the pinned physical ledger identity after proving
// that both the parent and ledger names still resolve to the opened objects.
func (l *JSONLLedger) PhysicalIdentity() (string, error) {
	if l == nil {
		return "", errors.New("ledger is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return "", errors.New("authoritative ledger is closed")
	}
	if l.file == nil {
		if err := l.pinLedgerFileLocked(false); err != nil {
			return "", err
		}
	}
	if err := l.verifyPhysicalIdentityLocked(); err != nil {
		return "", err
	}
	return l.physicalID, nil
}

// PhysicalGeneration returns the exact verified parent/file generation. For
// a new writable standalone ledger it materializes the empty ledger file so
// a service registration can bind that object before the first event.
func (l *JSONLLedger) PhysicalGeneration() (PhysicalGeneration, error) {
	if l == nil {
		return PhysicalGeneration{}, errors.New("ledger is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return PhysicalGeneration{}, errors.New("authoritative ledger is closed")
	}
	if l.file == nil {
		if err := l.pinLedgerFileLocked(!l.readOnly); err != nil {
			return PhysicalGeneration{}, err
		}
	}
	if !l.readOnly {
		if err := l.file.Sync(); err != nil {
			return PhysicalGeneration{}, fmt.Errorf("sync ledger before generation binding: %w", err)
		}
		if err := l.parent.Sync(); err != nil {
			return PhysicalGeneration{}, fmt.Errorf("sync ledger parent before generation binding: %w", err)
		}
	}
	if err := l.verifyPhysicalIdentityLocked(); err != nil {
		return PhysicalGeneration{}, err
	}
	return physicalLedgerGeneration(l.parentInfo, l.fileInfo)
}

// Close releases the two persistent descriptors owned by the ledger. It is
// idempotent; once called, every operation on the object fails closed.
func (l *JSONLLedger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	file, parent, runLocks := l.file, l.parent, l.runLocks
	l.file, l.parent, l.runLocks = nil, nil, nil
	l.fileInfo, l.parentInfo = nil, nil
	l.physicalID = ""
	l.expected = nil
	var fileErr, parentErr error
	if file != nil {
		fileErr = file.Close()
	}
	if parent != nil {
		parentErr = parent.Close()
	}
	return errors.Join(closeRunTransitionNamespace(runLocks), fileErr, parentErr)
}

// Snapshot returns one bounded, complete ledger image while holding the same
// lock used by appenders. The returned identity is the cleaned physical path.
func (l *JSONLLedger) Snapshot() ([]byte, string, error) {
	if l == nil {
		return nil, "", errors.New("ledger is required")
	}
	if l.snapshotCoordinator != nil {
		var data []byte
		var physicalID string
		invocations := 0
		var snapshotErr error
		coordinatorErr := l.snapshotCoordinator(func() error {
			invocations++
			if invocations != 1 {
				return errors.New("snapshot coordinator invoked the operation more than once")
			}
			data, physicalID, snapshotErr = l.snapshot()
			return snapshotErr
		})
		if invocations == 0 && coordinatorErr != nil {
			return nil, "", coordinatorErr
		}
		if invocations != 1 {
			return nil, "", errors.Join(errors.New("snapshot coordinator must invoke the operation exactly once"), coordinatorErr)
		}
		if err := errors.Join(snapshotErr, coordinatorErr); err != nil {
			return nil, "", err
		}
		return data, physicalID, nil
	}
	return l.snapshot()
}

func (l *JSONLLedger) snapshot() ([]byte, string, error) {
	var data []byte
	var physicalID string
	err := l.withFileLock(nil, true, func(f *os.File) error {
		var readErr error
		data, readErr = readBoundedLedger(f)
		physicalID = l.physicalID
		return readErr
	})
	if err != nil {
		return nil, "", err
	}
	return data, physicalID, nil
}

// WithFileLock serializes one complete authoritative-ledger transaction with
// ordinary Append calls through both this object and other processes or
// ledger objects that refer to the same file. The operation must include all
// snapshot, bound-check, append, confirmation, and rollback work.
func (l *JSONLLedger) WithFileLock(file *os.File, operation func() error) (result error) {
	if l == nil || file == nil || operation == nil {
		return errors.New("authoritative ledger lock requires a ledger, file, and operation")
	}
	return l.withFileLock(file, false, func(*os.File) error { return operation() })
}

// withFileLock keeps every descriptor and physical-identity observation under
// the same in-process mutex as Close. A nil file selects the pinned ledger
// descriptor after it has been created or verified while holding that mutex.
func (l *JSONLLedger) withFileLock(file *os.File, create bool, operation func(*os.File) error) (result error) {
	if l == nil || operation == nil {
		return errors.New("authoritative ledger lock requires a ledger and operation")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("authoritative ledger is closed")
	}
	if l.file == nil {
		if err := l.pinLedgerFileLocked(create); err != nil {
			return err
		}
	}
	if err := l.verifyPhysicalIdentityLocked(); err != nil {
		return err
	}
	if file == nil {
		file = l.file
	}
	info, err := file.Stat()
	if err != nil || !os.SameFile(info, l.fileInfo) {
		return errors.Join(errors.New("authoritative ledger descriptor identity changed"), err)
	}
	if err := lockLedgerFile(file, ledgerFileLockWait); err != nil {
		return fmt.Errorf("lock authoritative ledger file: %w", err)
	}
	defer func() {
		if err := unlockLedgerFile(file); err != nil {
			result = errors.Join(result, err)
		}
	}()
	operationErr := operation(file)
	identityErr := l.verifyPhysicalIdentityLocked()
	return errors.Join(operationErr, identityErr)
}

func (l *JSONLLedger) verifyPhysicalIdentity() error {
	if l == nil {
		return errors.New("ledger is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.verifyPhysicalIdentityLocked()
}

func (l *JSONLLedger) ensurePhysicalIdentity(create bool) error {
	if l == nil {
		return errors.New("ledger is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("authoritative ledger is closed")
	}
	if l.file == nil {
		if err := l.pinLedgerFileLocked(create); err != nil {
			return err
		}
	}
	return l.verifyPhysicalIdentityLocked()
}

func (l *JSONLLedger) pinLedgerFileLocked(create bool) error {
	if l.closed {
		return errors.New("authoritative ledger is closed")
	}
	flags := os.O_RDONLY
	if !l.readOnly {
		flags = os.O_RDWR | os.O_APPEND
	}
	if create {
		flags |= os.O_CREATE
	}
	file, err := os.OpenFile(l.path, flags, 0o600)
	if err != nil {
		return fmt.Errorf("open ledger: %w", err)
	}
	fileInfo, err := file.Stat()
	nameInfo, nameErr := os.Lstat(l.path)
	if err != nil || nameErr != nil || nameInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(fileInfo, nameInfo) || verifyLedgerFileInfo(fileInfo) != nil {
		_ = file.Close()
		return errors.Join(errors.New("ledger file is unsafe or replaced"), err, nameErr)
	}
	l.file, l.fileInfo, l.physicalID = file, fileInfo, physicalLedgerIdentity(fileInfo)
	return nil
}

func (l *JSONLLedger) verifyPhysicalIdentityLocked() error {
	if l.closed {
		return errors.New("authoritative ledger is closed")
	}
	if l.parent == nil {
		return errors.New("authoritative ledger parent descriptor is not pinned")
	}
	pinnedParent, err := l.parent.Stat()
	if err != nil || !os.SameFile(pinnedParent, l.parentInfo) {
		return errors.Join(errors.New("pinned ledger parent identity changed"), err)
	}
	canonicalParent, resolveErr := filepath.EvalSymlinks(l.parentPath)
	parentNameInfo, nameErr := os.Lstat(l.parentPath)
	if resolveErr != nil || filepath.Clean(canonicalParent) != l.parentPath || nameErr != nil ||
		parentNameInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(parentNameInfo, l.parentInfo) {
		return errors.Join(errors.New("ledger parent path was replaced"), resolveErr, nameErr)
	}
	resolvedParent, err := os.Open(l.parentPath)
	if err != nil {
		return err
	}
	resolvedParentInfo, statErr := resolvedParent.Stat()
	closeErr := resolvedParent.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(resolvedParentInfo, l.parentInfo) {
		return errors.Join(errors.New("ledger parent path was replaced"), statErr, closeErr)
	}
	if l.file == nil {
		if _, err := os.Lstat(l.path); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errors.New("an unpinned ledger path appeared")
	}
	pinnedFile, err := l.file.Stat()
	if err != nil || !os.SameFile(pinnedFile, l.fileInfo) || verifyLedgerFileInfo(pinnedFile) != nil {
		return errors.Join(errors.New("pinned ledger file identity changed"), err)
	}
	resolved, err := os.Open(l.path)
	if err != nil {
		return err
	}
	resolvedInfo, statErr := resolved.Stat()
	closeErr = resolved.Close()
	nameInfo, nameErr := os.Lstat(l.path)
	if statErr != nil || closeErr != nil || nameErr != nil || nameInfo.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(resolvedInfo, l.fileInfo) || !os.SameFile(nameInfo, l.fileInfo) || verifyLedgerFileInfo(resolvedInfo) != nil {
		return errors.Join(errors.New("ledger path was replaced or became unsafe"), statErr, closeErr, nameErr)
	}
	if l.expected != nil {
		observed, generationErr := physicalLedgerGeneration(l.parentInfo, l.fileInfo)
		if generationErr != nil || observed != *l.expected {
			return errors.Join(ErrRegisteredGenerationChanged, generationErr)
		}
	}
	return nil
}

func verifyLedgerFileInfo(info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("ledger must be a mode-0600 regular file")
	}
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if value.IsValid() {
		if field := value.FieldByName("Nlink"); field.IsValid() && field.Uint() != 1 {
			return errors.New("ledger must not be hard linked")
		}
		if field := value.FieldByName("Uid"); field.IsValid() && field.Uint() != uint64(os.Geteuid()) {
			return errors.New("ledger is not controller-owned")
		}
	}
	return nil
}

func ledgerInfoOwnedByEffectiveUser(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if value.IsValid() {
		if field := value.FieldByName("Uid"); field.IsValid() {
			return field.Uint() == uint64(os.Geteuid())
		}
	}
	return true
}

func physicalLedgerIdentity(info os.FileInfo) string {
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if value.IsValid() {
		device, inode := value.FieldByName("Dev"), value.FieldByName("Ino")
		if device.IsValid() && inode.IsValid() {
			return fmt.Sprintf("ledger-dev-%d-inode-%d", device.Uint(), inode.Uint())
		}
	}
	return "ledger-physical-identity-unavailable"
}

func physicalLedgerGeneration(parent, file os.FileInfo) (PhysicalGeneration, error) {
	parentValue, fileValue := reflect.Indirect(reflect.ValueOf(parent.Sys())), reflect.Indirect(reflect.ValueOf(file.Sys()))
	if !parentValue.IsValid() || !fileValue.IsValid() {
		return PhysicalGeneration{}, errors.New("ledger physical generation is unavailable")
	}
	parentDevice, parentInode := parentValue.FieldByName("Dev"), parentValue.FieldByName("Ino")
	fileDevice, fileInode := fileValue.FieldByName("Dev"), fileValue.FieldByName("Ino")
	if !parentDevice.IsValid() || !parentInode.IsValid() || !fileDevice.IsValid() || !fileInode.IsValid() {
		return PhysicalGeneration{}, errors.New("ledger physical generation is unavailable")
	}
	generation := PhysicalGeneration{
		ParentDevice: parentDevice.Uint(), ParentInode: parentInode.Uint(),
		FileDevice: fileDevice.Uint(), FileInode: fileInode.Uint(),
	}
	if !generation.Valid() {
		return PhysicalGeneration{}, errors.New("ledger physical generation is incomplete")
	}
	return generation, nil
}

func scanExactEvent(file *os.File, eventID string, expected []byte) (bool, error) {
	data, err := readBoundedLedger(file)
	if err != nil {
		return false, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), maxLedgerLineBytes)
	found := false
	count := 0
	for scanner.Scan() {
		count++
		if count > maxLedgerRecords {
			return false, errors.New("ledger record bound exceeded")
		}
		var observed Event
		if err := json.Unmarshal(scanner.Bytes(), &observed); err != nil || observed.Validate() != nil {
			return false, errors.New("ledger contains an invalid event")
		}
		if observed.EventID == eventID {
			if found || !bytes.Equal(scanner.Bytes(), expected) {
				return false, fmt.Errorf("event ID %q conflicts with durable ledger bytes", eventID)
			}
			found = true
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("scan ledger: %w", err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		return false, errors.New("ledger ends with a partial event")
	}
	return found, nil
}

func readBoundedLedger(file *os.File) ([]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	reader := io.LimitReader(file, maxLedgerBytes+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if len(data) > maxLedgerBytes {
		return nil, errors.New("ledger byte bound exceeded")
	}
	return data, nil
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

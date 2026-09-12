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

type JSONLLedger struct {
	path       string
	parentPath string
	file       *os.File
	parent     *os.File
	fileInfo   os.FileInfo
	parentInfo os.FileInfo
	physicalID string
	mu         sync.Mutex
}

func NewJSONLLedger(path string) (*JSONLLedger, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("canonical absolute ledger path is required")
	}
	parentPath := filepath.Dir(path)
	if err := os.MkdirAll(parentPath, 0o700); err != nil {
		return nil, fmt.Errorf("create ledger directory: %w", err)
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
	result := &JSONLLedger{path: path, parentPath: parentPath, parent: parent, parentInfo: parentInfo}
	if exists {
		result.mu.Lock()
		err = result.pinLedgerFileLocked(false)
		result.mu.Unlock()
		if err != nil {
			_ = parent.Close()
			return nil, err
		}
	}
	return result, nil
}

func (l *JSONLLedger) Append(event Event) error {
	return l.appendOrVerify(event, "", false)
}

// AppendOrVerify durably appends event, or succeeds when the byte-identical
// event is already present. A repeated EventID with different bytes is an
// integrity error. State transitions remain subject to an unresolved barrier.
func (l *JSONLLedger) AppendOrVerify(event Event) error {
	return l.appendOrVerify(event, "", true)
}

// AppendOrVerifyTransition is the only append path permitted through an
// unresolved transition barrier. The supplied barrier digest must identify
// the active barrier for the event's run. This lets recovery finish the one
// predetermined merge transition without opening a competing writer window.
func (l *JSONLLedger) AppendOrVerifyTransition(event Event, barrierSHA256 string) error {
	if barrierSHA256 == "" {
		return errors.New("transition barrier digest is required")
	}
	return l.appendOrVerify(event, barrierSHA256, true)
}

// AppendOrVerifyLeased appends while the caller owns the exact run-transition
// lease. It is used for pre-submission terminal selection; barriers use the
// stricter AppendOrVerifyTransition path instead.
func (l *JSONLLedger) AppendOrVerifyLeased(event Event, lease *RunTransitionLease) error {
	if lease == nil || lease.closed || lease.ledger != l || lease.runID != event.RunID {
		return errors.New("exact run-transition lease is required")
	}
	return l.appendOrVerify(event, leasedAppendSentinel, true)
}

func (l *JSONLLedger) appendOrVerify(event Event, barrierSHA256 string, verifyExisting bool) error {
	if l == nil {
		return errors.New("ledger is required")
	}
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate event: %w", err)
	}
	if err := l.ensurePhysicalIdentity(true); err != nil {
		return err
	}
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	var transitionFile *os.File
	if event.StateFrom != "" && barrierSHA256 == "" {
		transitionFile, err = l.acquireRunTransitionFile(event.RunID)
		if err != nil {
			return err
		}
		defer func() {
			_ = unlockLedgerFile(transitionFile)
			_ = transitionFile.Close()
		}()
	}

	f := l.file

	return l.WithFileLock(f, func() error {
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

// Snapshot returns one bounded, complete ledger image while holding the same
// lock used by appenders. The returned identity is the cleaned physical path.
func (l *JSONLLedger) Snapshot() ([]byte, string, error) {
	if l == nil {
		return nil, "", errors.New("ledger is required")
	}
	if err := l.ensurePhysicalIdentity(true); err != nil {
		return nil, "", err
	}
	f := l.file
	var data []byte
	err := l.WithFileLock(f, func() error {
		var readErr error
		data, readErr = readBoundedLedger(f)
		return readErr
	})
	return data, l.physicalID, err
}

// WithFileLock serializes one complete authoritative-ledger transaction with
// ordinary Append calls through both this object and other processes or
// ledger objects that refer to the same file. The operation must include all
// snapshot, bound-check, append, confirmation, and rollback work.
func (l *JSONLLedger) WithFileLock(file *os.File, operation func() error) (result error) {
	if l == nil || file == nil || operation == nil {
		return errors.New("authoritative ledger lock requires a ledger, file, and operation")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		if err := l.pinLedgerFileLocked(false); err != nil {
			return err
		}
	}
	if err := l.verifyPhysicalIdentityLocked(); err != nil {
		return err
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
	return operation()
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
	if l.file == nil {
		if err := l.pinLedgerFileLocked(create); err != nil {
			return err
		}
	}
	return l.verifyPhysicalIdentityLocked()
}

func (l *JSONLLedger) pinLedgerFileLocked(create bool) error {
	flags := os.O_RDWR | os.O_APPEND
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
	if l.parent == nil {
		return errors.New("authoritative ledger parent descriptor is not pinned")
	}
	pinnedParent, err := l.parent.Stat()
	if err != nil || !os.SameFile(pinnedParent, l.parentInfo) {
		return errors.Join(errors.New("pinned ledger parent identity changed"), err)
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

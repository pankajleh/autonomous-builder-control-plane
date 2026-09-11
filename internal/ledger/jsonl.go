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
	path string
	mu   sync.Mutex
}

func NewJSONLLedger(path string) (*JSONLLedger, error) {
	if path == "" {
		return nil, fmt.Errorf("ledger path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create ledger directory: %w", err)
	}
	return &JSONLLedger{path: path}, nil
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

	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open ledger: %w", err)
	}
	defer f.Close()

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
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("open ledger snapshot: %w", err)
	}
	defer f.Close()
	var data []byte
	err = l.WithFileLock(f, func() error {
		var readErr error
		data, readErr = readBoundedLedger(f)
		return readErr
	})
	return data, filepath.Clean(l.path), err
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

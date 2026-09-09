package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const ledgerFileLockWait = 2 * time.Second

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
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate event: %w", err)
	}
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open ledger: %w", err)
	}
	defer f.Close()

	return l.WithFileLock(f, func() error {
		if _, err := f.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("append ledger: %w", err)
		}
		if err := f.Sync(); err != nil {
			return fmt.Errorf("fsync ledger: %w", err)
		}
		return nil
	})
}

func (l *JSONLLedger) Path() string { return l.path }

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

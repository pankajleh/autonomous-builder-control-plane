package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate event: %w", err)
	}
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open ledger: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append ledger: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync ledger: %w", err)
	}
	return nil
}

func (l *JSONLLedger) Path() string { return l.path }

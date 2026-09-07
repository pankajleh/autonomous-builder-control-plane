//go:build linux

package prlifecycle

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	maxLedgerBytes = 64 << 20
	maxLedgerLine  = 256 << 10
	maxLedgerLines = 262144
)

type MaterialLedgerRecorder struct {
	path     string
	parent   string
	base     string
	parentID fileIdentity
}

// NewMaterialLedgerRecorder binds the PR material recorder to the exact
// authoritative controller ledger object. There is deliberately no production
// path-only or nil-ledger construction mode.
func NewMaterialLedgerRecorder(supplied *ledger.JSONLLedger) (*MaterialLedgerRecorder, error) {
	if supplied == nil {
		return nil, errors.New("authoritative controller ledger is required")
	}
	return newMaterialLedgerRecorder(supplied.Path())
}

// newMaterialLedgerRecorder is available only to same-package tests.
func newMaterialLedgerRecorder(path string) (*MaterialLedgerRecorder, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("canonical absolute material ledger path is required")
	}
	parent := filepath.Dir(path)
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil || canonical != parent {
		return nil, errors.New("material ledger parent must exist without symlinks")
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return nil, fmt.Errorf("inspect material ledger parent: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("material ledger parent must be a safe owner-only directory (mode %s)", info.Mode())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return nil, errors.New("material ledger parent is not controller-owned")
	}
	parentFile, parentID, err := openVerified(parent, syscall.O_RDONLY|syscall.O_DIRECTORY, 0, true, info.Mode().Perm())
	if err != nil {
		return nil, fmt.Errorf("pin material ledger parent: %w", err)
	}
	_ = parentFile.Close()
	return &MaterialLedgerRecorder{path: path, parent: parent, base: filepath.Base(path), parentID: parentID}, nil
}

func (r *MaterialLedgerRecorder) Record(expected ledger.Event, canonical []byte) error {
	if r == nil || expected.Validate() != nil || len(canonical) == 0 || len(canonical) > maxLedgerLine {
		return &Error{Code: CodeLedgerIntegrity, Cause: errors.New("invalid deterministic material event")}
	}
	reencoded, err := json.Marshal(expected)
	if err != nil || !bytes.Equal(reencoded, canonical) {
		return &Error{Code: CodeLedgerIntegrity, Cause: errors.New("material event is not canonical")}
	}
	f, err := r.open()
	if err != nil {
		return &Error{Code: CodeLedgerUnavailable, Cause: err}
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.Size() > maxLedgerBytes {
		return &Error{Code: CodeLedgerUnavailable, Cause: errors.New("material ledger is unavailable or oversized")}
	}
	snapshotSize := stat.Size()
	section := io.NewSectionReader(f, 0, snapshotSize)
	reader := bufio.NewReaderSize(section, maxLedgerLine+1)
	lines := 0
	found := false
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lines++
			if lines > maxLedgerLines || len(line) > maxLedgerLine+1 {
				return &Error{Code: CodeLedgerIntegrity, Cause: errors.New("material ledger scan bound exceeded")}
			}
			if line[len(line)-1] != '\n' {
				return &Error{Code: CodeLedgerIntegrity, Cause: errors.New("material ledger snapshot is not newline terminated")}
			}
			line = line[:len(line)-1]
			var observed ledger.Event
			if err := json.Unmarshal(line, &observed); err != nil || observed.Validate() != nil {
				return &Error{Code: CodeLedgerIntegrity, Cause: errors.New("malformed material ledger record")}
			}
			if observed.EventID == expected.EventID {
				if !bytes.Equal(line, canonical) {
					return &Error{Code: CodeLedgerIntegrity, Cause: errors.New("deterministic event ID conflicts")}
				}
				found = true
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return &Error{Code: CodeLedgerUnavailable, Cause: readErr}
		}
	}
	if found {
		return nil
	}
	line := append(append(make([]byte, 0, len(canonical)+1), canonical...), '\n')
	n, err := f.Write(line) // exactly one O_APPEND write.
	if err != nil || n != len(line) {
		return &Error{Code: CodeLedgerUnavailable, Cause: fmt.Errorf("ambiguous material ledger append: wrote %d/%d: %w", n, len(line), err)}
	}
	if err := f.Sync(); err != nil {
		return &Error{Code: CodeLedgerUnavailable, Cause: fmt.Errorf("ambiguous material ledger fsync: %w", err)}
	}
	return nil
}

func (r *MaterialLedgerRecorder) open() (*os.File, error) {
	parent, parentID, err := openVerified(r.parent, syscall.O_RDONLY|syscall.O_DIRECTORY, 0, true, mustParentPerm(r.parent))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if parentID != r.parentID {
		return nil, errors.New("material ledger parent identity changed")
	}
	fd, err := syscall.Openat(int(parent.Fd()), r.base, syscall.O_RDWR|syscall.O_APPEND|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		fd, err = syscall.Openat(int(parent.Fd()), r.base, syscall.O_RDWR|syscall.O_APPEND|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	}
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), r.path)
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || os.FileMode(stat.Mode).Perm() != 0o600 {
		_ = f.Close()
		return nil, errors.New("material ledger must be a mode-0600 regular file")
	}
	return f, nil
}

func mustParentPerm(path string) os.FileMode {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return 0
	}
	return info.Mode().Perm()
}

//go:build linux

package cilifecycle

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
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

type materialLedger struct {
	path        string
	parentPath  string
	base        string
	parentDir   *os.File
	parentID    ciFileID
	parentPerm  os.FileMode
	syncFile    func(*os.File) error
	syncDir     func(*os.File) error
	writeLine   func(*os.File, []byte) (int, error)
	appendFault func(string) error
	maxBytes    int64
	maxLines    int
}

func newMaterialLedger(supplied *ledger.JSONLLedger) (*materialLedger, error) {
	if supplied == nil || !filepath.IsAbs(supplied.Path()) || filepath.Clean(supplied.Path()) != supplied.Path() {
		return nil, errors.New("authoritative ledger path must be absolute and clean")
	}
	path := supplied.Path()
	if len(path) > MaxEvidenceURIBytes {
		return nil, errors.New("authoritative ledger path exceeds its bound")
	}
	parentPath := filepath.Dir(path)
	canonical, err := filepath.EvalSymlinks(parentPath)
	if err != nil || canonical != parentPath {
		return nil, errors.New("authoritative ledger parent must pre-exist without symlinks")
	}
	info, err := os.Lstat(parentPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("authoritative ledger parent is unsafe")
	}
	parent, parentID, err := openCIPath(parentPath, syscall.O_RDONLY|syscall.O_DIRECTORY, true, info.Mode().Perm())
	if err != nil {
		return nil, err
	}
	result := &materialLedger{
		path: path, parentPath: parentPath, base: filepath.Base(path), parentDir: parent,
		parentID: parentID, parentPerm: info.Mode().Perm(),
		syncFile:  func(file *os.File) error { return file.Sync() },
		syncDir:   func(file *os.File) error { return file.Sync() },
		writeLine: func(file *os.File, data []byte) (int, error) { return file.Write(data) },
		maxBytes:  MaxLedgerScanBytes, maxLines: MaxLedgerLines,
	}
	file, _, openErr := result.open(false)
	if openErr != nil && !errors.Is(openErr, syscall.ENOENT) {
		_ = parent.Close()
		return nil, openErr
	}
	if file != nil {
		_ = file.Close()
	}
	return result, nil
}

func (r *materialLedger) close() error {
	if r == nil || r.parentDir == nil {
		return nil
	}
	err := r.parentDir.Close()
	r.parentDir = nil
	return err
}

func (r *materialLedger) find(eventID string) (materialEvent, bool, error) {
	if !validDigest(eventID) {
		return materialEvent{}, false, errors.New("invalid deterministic event ID")
	}
	file, id, err := r.open(false)
	if errors.Is(err, syscall.ENOENT) {
		return materialEvent{}, false, nil
	}
	if err != nil {
		return materialEvent{}, false, err
	}
	defer file.Close()
	if err := lockCIFile(file, 2*time.Second); err != nil {
		return materialEvent{}, false, fmt.Errorf("lock authoritative ledger for scan: %w", err)
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	observed, found, _, err := scanMaterialLedger(file, eventID, r.maxBytes, r.maxLines)
	if err != nil {
		return materialEvent{}, false, err
	}
	if found {
		if err := r.syncFile(file); err != nil {
			return materialEvent{}, false, fmt.Errorf("sync existing deterministic CI outcome event: %w", err)
		}
		if err := r.syncDir(r.parentDir); err != nil {
			return materialEvent{}, false, fmt.Errorf("sync authoritative ledger parent for replay: %w", err)
		}
		if err := r.confirm(file, id, eventID, observed.canonical); err != nil {
			return materialEvent{}, false, err
		}
		return observed, true, nil
	}
	if err := r.verifyLedgerPath(id); err != nil {
		return materialEvent{}, false, err
	}
	return materialEvent{}, false, nil
}

func (r *materialLedger) record(expected ledger.Event, canonical []byte) error {
	if r == nil || expected.Validate() != nil || !validDigest(expected.EventID) || len(canonical) == 0 ||
		len(canonical) > MaxLedgerEventBytes || len(canonical)+1 > MaxLedgerLineBytes {
		return errors.New("invalid deterministic CI outcome event")
	}
	reencoded, err := json.Marshal(expected)
	if err != nil || !bytes.Equal(reencoded, canonical) {
		return errors.New("CI outcome event is not canonical")
	}
	file, id, err := r.open(true)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := lockCIFile(file, 2*time.Second); err != nil {
		return fmt.Errorf("lock authoritative ledger for append: %w", err)
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	if err := r.syncDir(r.parentDir); err != nil {
		return fmt.Errorf("sync authoritative ledger parent before append: %w", err)
	}
	if err := r.verifyLedgerPath(id); err != nil {
		return err
	}
	observed, found, snapshot, err := scanMaterialLedger(file, expected.EventID, r.maxBytes, r.maxLines)
	if err != nil {
		return err
	}
	if found {
		if !bytes.Equal(observed.canonical, canonical) {
			return errors.New("deterministic CI outcome event conflicts")
		}
		if err := r.syncFile(file); err != nil {
			return fmt.Errorf("sync existing deterministic CI outcome event: %w", err)
		}
		return r.confirm(file, id, expected.EventID, canonical)
	}
	if r.appendFault != nil {
		if err := r.appendFault("before_append"); err != nil {
			return err
		}
	}
	line := append(append(make([]byte, 0, len(canonical)+1), canonical...), '\n')
	if snapshot.bytes > r.maxBytes-int64(len(line)) || snapshot.lines >= r.maxLines {
		return errors.New("authoritative ledger projected bound exceeded")
	}
	n, writeErr := r.writeLine(file, line)
	if writeErr == nil && n != len(line) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil && r.appendFault != nil {
		writeErr = r.appendFault("after_append")
	}
	if writeErr != nil {
		if n < 0 || n > len(line) {
			return fmt.Errorf("invalid CI outcome append count %d/%d: %w", n, len(line), writeErr)
		}
		if err := file.Truncate(snapshot.bytes); err != nil {
			return fmt.Errorf("rollback failed CI outcome append after %d/%d bytes: %w", n, len(line), errors.Join(writeErr, err))
		}
		if err := r.syncFile(file); err != nil {
			return fmt.Errorf("sync rolled-back CI outcome append after %d/%d bytes: %w", n, len(line), errors.Join(writeErr, err))
		}
		if stat, err := file.Stat(); err != nil || stat.Size() != snapshot.bytes {
			return errors.Join(errors.New("rolled-back CI outcome append did not verify"), err)
		}
		return fmt.Errorf("append deterministic CI outcome: wrote %d/%d: %w", n, len(line), writeErr)
	}
	syncErr := r.syncFile(file)
	if syncErr != nil {
		return fmt.Errorf("sync deterministic CI outcome: %w", syncErr)
	}
	if r.appendFault != nil {
		if err := r.appendFault("after_fsync"); err != nil {
			if confirmErr := r.confirm(file, id, expected.EventID, canonical); confirmErr != nil {
				return errors.Join(err, confirmErr)
			}
			return nil
		}
	}
	return r.confirm(file, id, expected.EventID, canonical)
}

func (r *materialLedger) confirm(file *os.File, id ciFileID, eventID string, canonical []byte) error {
	if err := r.verifyLedgerPath(id); err != nil {
		return err
	}
	observed, found, _, err := scanMaterialLedger(file, eventID, r.maxBytes, r.maxLines)
	if err != nil || !found || !bytes.Equal(observed.canonical, canonical) {
		return errors.Join(errors.New("deterministic CI outcome event was not confirmed"), err)
	}
	return nil
}

func (r *materialLedger) verifyLedgerPath(id ciFileID) error {
	if err := r.verifyParent(); err != nil {
		return err
	}
	resolved, resolvedID, err := openCIAt(r.parentDir, r.base, syscall.O_RDONLY|syscall.O_NONBLOCK, false, 0o600)
	if err != nil {
		return err
	}
	_ = resolved.Close()
	if resolvedID != id {
		return errors.New("authoritative ledger path was replaced")
	}
	return nil
}

func (r *materialLedger) open(create bool) (*os.File, ciFileID, error) {
	if err := r.verifyParent(); err != nil {
		return nil, ciFileID{}, err
	}
	flags := syscall.O_RDONLY | syscall.O_NONBLOCK
	if create {
		flags = syscall.O_RDWR | syscall.O_APPEND | syscall.O_NONBLOCK
	}
	file, id, err := openCIAt(r.parentDir, r.base, flags, false, 0o600)
	if !create || !errors.Is(err, syscall.ENOENT) {
		return file, id, err
	}
	fd, createErr := syscall.Openat(int(r.parentDir.Fd()), r.base,
		syscall.O_RDWR|syscall.O_APPEND|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0o600)
	if errors.Is(createErr, syscall.EEXIST) {
		return openCIAt(r.parentDir, r.base, flags, false, 0o600)
	}
	if createErr != nil {
		return nil, ciFileID{}, createErr
	}
	file = os.NewFile(uintptr(fd), r.path)
	id, err = verifyCIFile(file, false, 0o600)
	if err != nil {
		_ = file.Close()
		return nil, ciFileID{}, err
	}
	if err := r.syncDir(r.parentDir); err != nil {
		_ = file.Close()
		return nil, ciFileID{}, fmt.Errorf("sync authoritative ledger parent: %w", err)
	}
	return file, id, nil
}

func (r *materialLedger) verifyParent() error {
	if r == nil || r.parentDir == nil {
		return errors.New("authoritative ledger parent is not pinned")
	}
	current, id, err := openCIPath(r.parentPath, syscall.O_RDONLY|syscall.O_DIRECTORY, true, r.parentPerm)
	if err != nil {
		return err
	}
	_ = current.Close()
	if id != r.parentID {
		return errors.New("authoritative ledger parent identity changed")
	}
	return nil
}

type materialLedgerSnapshot struct {
	bytes int64
	lines int
}

func scanMaterialLedger(file *os.File, eventID string, maxBytes int64, maxLines int) (materialEvent, bool, materialLedgerSnapshot, error) {
	stat, err := file.Stat()
	if err != nil || maxBytes < 1 || maxBytes > MaxLedgerScanBytes || maxLines < 1 || maxLines > MaxLedgerLines ||
		stat.Size() < 0 || stat.Size() > maxBytes {
		return materialEvent{}, false, materialLedgerSnapshot{}, errors.New("authoritative ledger is unavailable or oversized")
	}
	snapshotSize := stat.Size()
	if snapshotSize > 0 {
		var final [1]byte
		if _, err := file.ReadAt(final[:], snapshotSize-1); err != nil || final[0] != '\n' {
			return materialEvent{}, false, materialLedgerSnapshot{}, errors.New("authoritative ledger snapshot is not newline terminated")
		}
	}
	reader := io.NewSectionReader(file, 0, snapshotSize)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), MaxLedgerLineBytes+1)
	lines := 0
	var found materialEvent
	foundCount := 0
	for scanner.Scan() {
		lines++
		line := append([]byte(nil), scanner.Bytes()...)
		if lines > maxLines || len(line)+1 > MaxLedgerLineBytes {
			return materialEvent{}, false, materialLedgerSnapshot{}, errors.New("authoritative ledger scan bound exceeded")
		}
		var event ledger.Event
		if err := strictDecode(line, &event); err != nil || event.Validate() != nil {
			return materialEvent{}, false, materialLedgerSnapshot{}, errors.New("authoritative ledger contains a malformed event")
		}
		canonical, err := json.Marshal(event)
		if err != nil || !bytes.Equal(line, canonical) {
			return materialEvent{}, false, materialLedgerSnapshot{}, errors.New("authoritative ledger contains a noncanonical event")
		}
		if event.EventID == eventID {
			foundCount++
			if foundCount > 1 {
				return materialEvent{}, false, materialLedgerSnapshot{}, errors.New("authoritative ledger contains duplicate deterministic events")
			}
			found = materialEvent{event: event, canonical: line}
		}
	}
	if err := scanner.Err(); err != nil {
		return materialEvent{}, false, materialLedgerSnapshot{}, errors.New("authoritative ledger line exceeds its bound")
	}
	return found, foundCount == 1, materialLedgerSnapshot{bytes: snapshotSize, lines: lines}, nil
}

package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const TransitionBarrierSchemaV1 = "run-transition-barrier-v1"

const leasedAppendSentinel = "leased-run-transition"

var safeBarrierRunID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$`)

// TransitionBarrier is an immutable durable exclusion protecting a READY run
// while one exact target-ref submission is unresolved.
type TransitionBarrier struct {
	SchemaVersion    string `json:"schema_version"`
	RunID            string `json:"run_id"`
	AttemptID        string `json:"attempt_id"`
	CommitmentSHA256 string `json:"commitment_sha256"`
	CreatedUnixNano  int64  `json:"created_unix_nano"`
	SHA256           string `json:"sha256"`
}

type barrierPayload struct {
	SchemaVersion    string `json:"schema_version"`
	RunID            string `json:"run_id"`
	AttemptID        string `json:"attempt_id"`
	CommitmentSHA256 string `json:"commitment_sha256"`
	CreatedUnixNano  int64  `json:"created_unix_nano"`
}

// RunTransitionLease holds the cross-process run lock across final READY
// reconstruction, authorization sealing, submission, and terminal selection.
type RunTransitionLease struct {
	ledger *JSONLLedger
	runID  string
	file   *os.File
	closed bool
}

func (l *JSONLLedger) AcquireRunTransition(runID string) (*RunTransitionLease, error) {
	if l == nil || !safeBarrierRunID.MatchString(runID) {
		return nil, errors.New("valid ledger and run ID are required")
	}
	if err := l.verifyPhysicalIdentity(); err != nil {
		return nil, err
	}
	file, err := l.acquireRunTransitionFile(runID)
	if err != nil {
		return nil, err
	}
	return &RunTransitionLease{ledger: l, runID: runID, file: file}, nil
}

func (l *RunTransitionLease) RunID() string {
	if l == nil {
		return ""
	}
	return l.runID
}

func (l *RunTransitionLease) Close() error {
	if l == nil || l.closed {
		return nil
	}
	l.closed = true
	err := unlockLedgerFile(l.file)
	return errors.Join(err, l.file.Close())
}

func (l *JSONLLedger) acquireRunTransitionFile(runID string) (*os.File, error) {
	if err := l.verifyPhysicalIdentity(); err != nil {
		return nil, err
	}
	directory := l.path + ".run-locks"
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	if err := verifyLedgerControlDirectory(directory); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(runID))
	path := filepath.Join(directory, hex.EncodeToString(sum[:])+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	info, statErr := file.Stat()
	nameInfo, nameErr := os.Lstat(path)
	if statErr != nil || nameErr != nil || nameInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, nameInfo) || verifyLedgerFileInfo(info) != nil {
		_ = file.Close()
		return nil, errors.Join(errors.New("run-transition lock is unsafe"), statErr, nameErr)
	}
	if err := lockLedgerFile(file, ledgerFileLockWait); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock run transition: %w", err)
	}
	return file, nil
}

func NewTransitionBarrier(runID, attemptID, commitmentSHA256 string, created time.Time) (TransitionBarrier, error) {
	b := TransitionBarrier{TransitionBarrierSchemaV1, runID, attemptID, commitmentSHA256, created.UTC().UnixNano(), ""}
	payload := barrierPayload{b.SchemaVersion, b.RunID, b.AttemptID, b.CommitmentSHA256, b.CreatedUnixNano}
	data, err := json.Marshal(payload)
	if err != nil {
		return TransitionBarrier{}, err
	}
	sum := sha256.Sum256(data)
	b.SHA256 = hex.EncodeToString(sum[:])
	if err := b.Validate(); err != nil {
		return TransitionBarrier{}, err
	}
	return b, nil
}

func (b TransitionBarrier) Validate() error {
	if b.SchemaVersion != TransitionBarrierSchemaV1 || !safeBarrierRunID.MatchString(b.RunID) ||
		!safeBarrierRunID.MatchString(b.AttemptID) || !validHexDigest(b.CommitmentSHA256) || b.CreatedUnixNano <= 0 || !validHexDigest(b.SHA256) {
		return errors.New("transition barrier is invalid")
	}
	payload, _ := json.Marshal(barrierPayload{b.SchemaVersion, b.RunID, b.AttemptID, b.CommitmentSHA256, b.CreatedUnixNano})
	sum := sha256.Sum256(payload)
	if b.SHA256 != hex.EncodeToString(sum[:]) {
		return errors.New("transition barrier digest disagrees")
	}
	return nil
}

func ParseTransitionBarrier(data []byte) (TransitionBarrier, error) {
	var b TransitionBarrier
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&b); err != nil {
		return b, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return b, errors.New("transition barrier contains trailing JSON")
	}
	canonical, err := json.Marshal(b)
	if err != nil || !bytes.Equal(canonical, data) {
		return b, errors.New("transition barrier is not canonical JSON")
	}
	return b, b.Validate()
}

// InstallTransitionBarrier immutable-creates the exact barrier. A byte-identical
// replay is accepted; a different barrier for the same run is rejected.
func (l *JSONLLedger) InstallTransitionBarrier(barrier TransitionBarrier) error {
	if l == nil || barrier.Validate() != nil {
		return errors.New("valid transition barrier and ledger are required")
	}
	if err := l.verifyPhysicalIdentity(); err != nil {
		return err
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	return l.WithFileLock(file, func() error {
		path := l.barrierPath(barrier.RunID)
		data, _ := json.Marshal(barrier)
		if err := os.MkdirAll(l.barrierDirectory(), 0o700); err != nil {
			return err
		}
		if err := verifyLedgerControlDirectory(l.barrierDirectory()); err != nil {
			return err
		}
		active, found, err := l.readBarrier(barrier.RunID)
		if err != nil {
			return err
		}
		if found {
			if active.SHA256 != barrier.SHA256 {
				return errors.New("a conflicting transition barrier is active")
			}
			return nil
		}
		temporary := path + ".new"
		if staged, err := readSafeBarrierFile(temporary); err == nil {
			if !bytes.Equal(staged, data) {
				return errors.New("a conflicting staged transition barrier exists")
			}
			if err := publishNoReplace(temporary, path); err != nil {
				return err
			}
			return syncDirectory(l.barrierDirectory())
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		f, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		writeErr := writeFull(f, data)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return err
		}
		if err := publishNoReplace(temporary, path); err != nil {
			return err
		}
		return syncDirectory(l.barrierDirectory())
	})
}

func (l *JSONLLedger) ActiveTransitionBarrier(runID string) (TransitionBarrier, bool, error) {
	if l == nil || !safeBarrierRunID.MatchString(runID) {
		return TransitionBarrier{}, false, errors.New("valid ledger and run ID are required")
	}
	if err := l.verifyPhysicalIdentity(); err != nil {
		return TransitionBarrier{}, false, err
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return TransitionBarrier{}, false, err
	}
	defer file.Close()
	var barrier TransitionBarrier
	var found bool
	err = l.WithFileLock(file, func() error {
		var readErr error
		barrier, found, readErr = l.readBarrier(runID)
		return readErr
	})
	return barrier, found, err
}

// ResolveTransitionBarrier removes the exact barrier only after the authorized
// terminal event has been durably append-or-verified by the caller.
func (l *JSONLLedger) ResolveTransitionBarrier(barrier TransitionBarrier) error {
	if l == nil || barrier.Validate() != nil {
		return errors.New("valid transition barrier and ledger are required")
	}
	if err := l.verifyPhysicalIdentity(); err != nil {
		return err
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	return l.WithFileLock(file, func() error {
		active, found, err := l.readBarrier(barrier.RunID)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if active.SHA256 != barrier.SHA256 {
			return errors.New("transition barrier resolution conflicts")
		}
		if err := os.Remove(l.barrierPath(barrier.RunID)); err != nil {
			return err
		}
		return syncDirectory(l.barrierDirectory())
	})
}

func (l *JSONLLedger) authorizeTransition(event Event, supplied string) error {
	if event.StateFrom == "" || event.StateTo == "" {
		return nil
	}
	barrier, found, err := l.readBarrier(event.RunID)
	if err != nil || !found {
		return err
	}
	if supplied == leasedAppendSentinel {
		return errors.New("leased append cannot bypass an unresolved transition barrier")
	}
	if supplied == "" || supplied != barrier.SHA256 || event.AttemptID != barrier.AttemptID {
		return fmt.Errorf("run %q has unresolved transition barrier", event.RunID)
	}
	return nil
}

func (l *JSONLLedger) readBarrier(runID string) (TransitionBarrier, bool, error) {
	path := l.barrierPath(runID)
	if err := verifyLedgerControlDirectory(l.barrierDirectory()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return TransitionBarrier{}, false, nil
		}
		return TransitionBarrier{}, false, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return TransitionBarrier{}, false, nil
	}
	if err != nil {
		return TransitionBarrier{}, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	nameInfo, nameErr := os.Lstat(path)
	if err != nil || nameErr != nil || !os.SameFile(info, nameInfo) || verifyLedgerFileInfo(info) != nil {
		return TransitionBarrier{}, false, errors.Join(errors.New("active transition barrier is unsafe or replaced"), err, nameErr)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxLedgerLineBytes+1))
	if err != nil || len(data) > maxLedgerLineBytes {
		return TransitionBarrier{}, false, errors.Join(errors.New("active transition barrier is unreadable or oversized"), err)
	}
	b, err := ParseTransitionBarrier(data)
	if err != nil || b.RunID != runID {
		return TransitionBarrier{}, false, errors.Join(errors.New("active transition barrier is corrupt"), err)
	}
	return b, true, nil
}

func (l *JSONLLedger) barrierDirectory() string { return l.path + ".transition-barriers" }
func (l *JSONLLedger) barrierPath(runID string) string {
	sum := sha256.Sum256([]byte(runID))
	return filepath.Join(l.barrierDirectory(), hex.EncodeToString(sum[:])+".json")
}

func validHexDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func syncDirectory(path string) error {
	if err := verifyLedgerControlDirectory(path); err != nil {
		return err
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func verifyLedgerControlDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !ledgerInfoOwnedByEffectiveUser(info) {
		return errors.New("ledger control directory is unsafe")
	}
	resolved, err := os.Open(path)
	if err != nil {
		return err
	}
	defer resolved.Close()
	resolvedInfo, err := resolved.Stat()
	if err != nil || !os.SameFile(info, resolvedInfo) {
		return errors.Join(errors.New("ledger control directory was replaced"), err)
	}
	return nil
}

func readSafeBarrierFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	nameInfo, nameErr := os.Lstat(path)
	if err != nil || nameErr != nil || !os.SameFile(info, nameInfo) || verifyLedgerFileInfo(info) != nil {
		return nil, errors.Join(errors.New("transition barrier staging file is unsafe or replaced"), err, nameErr)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxLedgerLineBytes+1))
	if err != nil || len(data) > maxLedgerLineBytes {
		return nil, errors.Join(errors.New("transition barrier staging file is unreadable or oversized"), err)
	}
	return data, nil
}

// publishNoReplace atomically adds the destination name without ever replacing
// an entry won by another process. Linking keeps this portable; removing the
// staging name after the link leaves the published barrier singly linked.
func publishNoReplace(temporary, destination string) error {
	if err := os.Link(temporary, destination); err != nil {
		return err
	}
	if err := os.Remove(temporary); err != nil {
		return err
	}
	return nil
}

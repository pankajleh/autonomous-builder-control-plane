//go:build linux

package mergelifecycle

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

var allowedAttemptRecords = map[string]int{
	"admission.json":              MaxTerminalRecordBytes,
	"commit-prepare-marker.json":  MaxTerminalRecordBytes,
	"commit-preparation.json":     MaxTerminalRecordBytes,
	"final-revalidation.json":     MaxTerminalRecordBytes,
	"authorization-seal.json":     MaxTerminalRecordBytes,
	"sealed-authorization.json":   MaxTerminalRecordBytes,
	"target-commitment.json":      MaxTerminalRecordBytes,
	"target-submission.json":      MaxTerminalRecordBytes,
	"target-outcome.json":         MaxTerminalRecordBytes,
	"target-reconciliation.json":  MaxTerminalRecordBytes,
	"merge-result.json":           MaxTerminalRecordBytes,
	"post-merge.json":             MaxTerminalRecordBytes,
	"cancellation-authority.json": MaxTerminalRecordBytes,
	"pending-cancellation.json":   MaxTerminalRecordBytes,
}

var allowedChannels = map[string]struct{}{
	"cancellation-observations": {},
	"cancellation-authorities":  {},
	"cancellation-replay":       {},
	"durable-cancellations":     {},
	"pending-cancellations":     {},
	"reconciliations":           {},
	"storage-reservations":      {},
	"terminal-intents":          {},
	"final-terminals":           {},
	"cleanup-incidents":         {},
}

type durableStore struct {
	root           string
	limits         Limits
	rootDir        *os.File
	rootInfo       os.FileInfo
	namespaceDirs  map[string]*os.File
	namespaceInfos map[string]os.FileInfo
	lock           *os.File
	syncFile       func(*os.File) error
	syncDir        func(*os.File) error
	remove         func(string) error
}

type attemptStore struct {
	store         *durableStore
	id            string
	root          string
	records       string
	temporary     string
	lock          *os.File
	rootInfo      os.FileInfo
	recordsInfo   os.FileInfo
	temporaryInfo os.FileInfo
	closed        bool
}

type repositoryBaseLease struct {
	file   *os.File
	closed bool
}

type inventory struct {
	publishedFiles int
	publishedBytes int64
	temporaryFiles int
	temporaryBytes int64
	names          []string
}

type counterReservation struct {
	AttemptID string   `json:"attempt_id"`
	Category  string   `json:"category"`
	Counters  Counters `json:"counters"`
}

type storageReservationV1 struct {
	Schema     string `json:"schema"`
	AttemptID  string `json:"attempt_id"`
	RecordName string `json:"record_name"`
	Bytes      int64  `json:"bytes"`
	SHA256     string `json:"sha256"`
}

func newDurableStore(root string, limits Limits) (*durableStore, error) {
	if !limits.valid() || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("merge state root and limits are invalid")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return nil, errors.New("merge state root must pre-exist without symlink components")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return nil, errors.New("merge state root must be an owner-only directory")
	}
	rootDir, err := openNoFollow(root, true, 0o700)
	if err != nil {
		return nil, err
	}
	rootInfo, err := rootDir.Stat()
	if err != nil {
		_ = rootDir.Close()
		return nil, err
	}
	store := &durableStore{root: root, limits: limits, rootDir: rootDir, rootInfo: rootInfo,
		namespaceDirs: make(map[string]*os.File), namespaceInfos: make(map[string]os.FileInfo),
		syncFile: func(f *os.File) error { return f.Sync() }, syncDir: func(f *os.File) error { return f.Sync() }, remove: os.Remove}
	for _, name := range []string{"attempts", "channels", "resource-locks"} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			store.close()
			return nil, err
		}
		if err := verifyDirectory(path, 0o700); err != nil {
			store.close()
			return nil, err
		}
		directory, err := openNoFollow(path, true, 0o700)
		if err != nil {
			store.close()
			return nil, err
		}
		directoryInfo, err := directory.Stat()
		if err != nil {
			_ = directory.Close()
			store.close()
			return nil, err
		}
		store.namespaceDirs[name], store.namespaceInfos[name] = directory, directoryInfo
	}
	lockPath := filepath.Join(root, "store.lock")
	lock, err := openOrCreateRegular(lockPath, 0o600)
	if err != nil {
		store.close()
		return nil, err
	}
	store.lock = lock
	if err := store.syncDir(rootDir); err != nil {
		store.close()
		return nil, err
	}
	if err := flock(store.lock, false); err != nil {
		store.close()
		return nil, err
	}
	inventoryErr := store.inventoryGlobal()
	unlockErr := funlock(store.lock)
	if err := errors.Join(inventoryErr, unlockErr); err != nil {
		store.close()
		return nil, err
	}
	return store, nil
}

func (s *durableStore) acquireRepositoryBase(key string) (*repositoryBaseLease, error) {
	if s == nil || !validDigest(key) {
		return nil, errors.New("valid repository/base lock identity is required")
	}
	if err := s.checkRoot(); err != nil {
		return nil, err
	}
	if err := flock(s.lock, false); err != nil {
		return nil, err
	}
	if err := s.checkRoot(); err != nil {
		_ = funlock(s.lock)
		return nil, err
	}
	path := filepath.Join(s.root, "resource-locks", key+".lock")
	file, err := openOrCreateRegular(path, 0o600)
	if err == nil {
		err = syncPathDirectory(filepath.Join(s.root, "resource-locks"), s.syncDir)
	}
	_ = funlock(s.lock)
	if err != nil {
		if file != nil {
			_ = file.Close()
		}
		return nil, err
	}
	if err := flock(file, false); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := s.checkRoot(); err != nil {
		_ = funlock(file)
		_ = file.Close()
		return nil, err
	}
	resolved, err := openNoFollow(path, false, 0o600)
	if err != nil {
		_ = funlock(file)
		_ = file.Close()
		return nil, err
	}
	lockedInfo, lockedErr := file.Stat()
	resolvedInfo, resolvedErr := resolved.Stat()
	_ = resolved.Close()
	if lockedErr != nil || resolvedErr != nil || !os.SameFile(lockedInfo, resolvedInfo) {
		_ = funlock(file)
		_ = file.Close()
		return nil, errors.New("repository/base lock was replaced during acquisition")
	}
	return &repositoryBaseLease{file: file}, nil
}

func (l *repositoryBaseLease) close() error {
	if l == nil || l.closed {
		return nil
	}
	l.closed = true
	return errors.Join(funlock(l.file), l.file.Close())
}

func (s *durableStore) close() error {
	if s == nil {
		return nil
	}
	var errs []error
	if s.lock != nil {
		errs = append(errs, s.lock.Close())
		s.lock = nil
	}
	if s.rootDir != nil {
		errs = append(errs, s.rootDir.Close())
		s.rootDir = nil
	}
	for name, directory := range s.namespaceDirs {
		errs = append(errs, directory.Close())
		delete(s.namespaceDirs, name)
	}
	return errors.Join(errs...)
}

func (s *durableStore) openAttempt(id string) (*attemptStore, error) {
	if s == nil || s.rootDir == nil || !validDigest(id) {
		return nil, errors.New("valid attempt identity and open store are required")
	}
	if err := flock(s.lock, true); err != nil {
		return nil, err
	}
	defer funlock(s.lock)
	if err := s.checkRoot(); err != nil {
		return nil, err
	}
	if err := s.inventoryGlobal(); err != nil {
		return nil, err
	}
	root := filepath.Join(s.root, "attempts", id)
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := verifyDirectory(root, 0o700); err != nil {
		return nil, err
	}
	if err := syncPathDirectory(filepath.Join(s.root, "attempts"), s.syncDir); err != nil {
		return nil, err
	}
	for _, name := range []string{"records", "tmp"} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if err := verifyDirectory(path, 0o700); err != nil {
			return nil, err
		}
	}
	if err := syncPathDirectory(root, s.syncDir); err != nil {
		return nil, err
	}
	lock, err := openOrCreateRegular(filepath.Join(root, "attempt.lock"), 0o600)
	if err != nil {
		return nil, err
	}
	if err := flock(lock, true); err != nil {
		lock.Close()
		return nil, errors.New("merge attempt is already active")
	}
	if err := syncPathDirectory(root, s.syncDir); err != nil {
		_ = funlock(lock)
		_ = lock.Close()
		return nil, err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		_ = funlock(lock)
		_ = lock.Close()
		return nil, err
	}
	recordsInfo, err := os.Lstat(filepath.Join(root, "records"))
	if err != nil {
		_ = funlock(lock)
		_ = lock.Close()
		return nil, err
	}
	temporaryInfo, err := os.Lstat(filepath.Join(root, "tmp"))
	if err != nil {
		_ = funlock(lock)
		_ = lock.Close()
		return nil, err
	}
	attempt := &attemptStore{store: s, id: id, root: root, records: filepath.Join(root, "records"), temporary: filepath.Join(root, "tmp"),
		lock: lock, rootInfo: rootInfo, recordsInfo: recordsInfo, temporaryInfo: temporaryInfo}
	if _, err := attempt.inventory(); err != nil {
		attempt.close()
		return nil, err
	}
	return attempt, nil
}

func (s *durableStore) checkRoot() error {
	if s == nil || s.rootDir == nil || s.rootInfo == nil {
		return errors.New("merge state root is not pinned")
	}
	pinned, err := s.rootDir.Stat()
	if err != nil || !os.SameFile(pinned, s.rootInfo) {
		return errors.Join(errors.New("pinned merge state root identity changed"), err)
	}
	resolved, err := openNoFollow(s.root, true, 0o700)
	if err != nil {
		return err
	}
	info, statErr := resolved.Stat()
	closeErr := resolved.Close()
	if statErr != nil || closeErr != nil || !os.SameFile(info, s.rootInfo) {
		return errors.Join(errors.New("merge state root path was replaced"), statErr, closeErr)
	}
	if len(s.namespaceDirs) != 3 || len(s.namespaceInfos) != 3 {
		return errors.New("merge state namespaces are not pinned")
	}
	for name, directory := range s.namespaceDirs {
		pinnedInfo, pinErr := directory.Stat()
		resolved, openErr := openNoFollow(filepath.Join(s.root, name), true, 0o700)
		if pinErr != nil || openErr != nil {
			if resolved != nil {
				_ = resolved.Close()
			}
			return errors.Join(errors.New("merge state namespace is unavailable"), pinErr, openErr)
		}
		resolvedInfo, statErr := resolved.Stat()
		closeErr := resolved.Close()
		if statErr != nil || closeErr != nil || !os.SameFile(pinnedInfo, s.namespaceInfos[name]) || !os.SameFile(resolvedInfo, s.namespaceInfos[name]) {
			return errors.Join(errors.New("merge state namespace path was replaced"), statErr, closeErr)
		}
	}
	return nil
}

func (a *attemptStore) checkIdentity() error {
	if a == nil || a.closed || a.store.checkRoot() != nil {
		return errors.New("merge attempt root is unavailable")
	}
	for _, item := range []struct {
		path string
		info os.FileInfo
		mode os.FileMode
	}{{a.root, a.rootInfo, 0o700}, {a.records, a.recordsInfo, 0o700}, {a.temporary, a.temporaryInfo, 0o700}} {
		resolved, err := openNoFollow(item.path, true, item.mode)
		if err != nil {
			return err
		}
		info, statErr := resolved.Stat()
		closeErr := resolved.Close()
		if statErr != nil || closeErr != nil || !os.SameFile(info, item.info) {
			return errors.Join(errors.New("merge attempt directory identity changed"), statErr, closeErr)
		}
	}
	return nil
}

func (a *attemptStore) close() error {
	if a == nil || a.closed {
		return nil
	}
	a.closed = true
	err := funlock(a.lock)
	return errors.Join(err, a.lock.Close())
}

func (a *attemptStore) publish(name string, data []byte) ([]byte, string, error) {
	bound, ok := allowedAttemptRecords[name]
	if a == nil || a.closed || !ok || len(data) == 0 || len(data) > bound || int64(len(data)) > a.store.limits.publishedBytes {
		return nil, "", errors.New("attempt record name or size is invalid")
	}
	if !json.Valid(data) {
		return nil, "", errors.New("attempt record must be canonical JSON")
	}
	if err := a.checkIdentity(); err != nil {
		return nil, "", err
	}
	if err := flock(a.store.lock, false); err != nil {
		return nil, "", err
	}
	defer funlock(a.store.lock)
	if err := a.checkIdentity(); err != nil {
		return nil, "", err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil || !bytes.Equal(compact.Bytes(), data) {
		return nil, "", errors.New("attempt record JSON is not compact")
	}
	inv, err := a.inventory()
	if err != nil {
		return nil, "", err
	}
	destination := filepath.Join(a.records, name)
	temporaryName := hex.EncodeToString([]byte(name)) + "-" + digest(data) + ".tmp"
	temporaryPath := filepath.Join(a.temporary, temporaryName)
	if existing, err := readSafeRegular(destination, bound); err == nil {
		if !bytes.Equal(existing, data) {
			return nil, "", errors.New("immutable attempt record conflicts")
		}
		if err := a.store.verifyStorageReservationLocked(a.id, name, data); err != nil {
			return nil, "", err
		}
		if staged, stagedErr := readSafeRegular(temporaryPath, len(data)); stagedErr == nil {
			if !bytes.Equal(staged, data) {
				return nil, "", errors.New("published record has a conflicting temporary")
			}
			if err := a.store.remove(temporaryPath); err != nil {
				return nil, "", err
			}
			if err := syncPathDirectory(a.temporary, a.store.syncDir); err != nil {
				return nil, "", err
			}
		} else if !errors.Is(stagedErr, os.ErrNotExist) {
			return nil, "", stagedErr
		}
		return existing, digest(existing), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	if err := a.store.reserveStorageLocked(a, name, data); err != nil {
		return nil, "", err
	}
	if inv.publishedFiles+1 > a.store.limits.publishedFiles || inv.publishedBytes+int64(len(data)) > a.store.limits.publishedBytes ||
		inv.publishedFiles+inv.temporaryFiles+1 > a.store.limits.liveFiles || inv.publishedBytes+inv.temporaryBytes+int64(len(data)) > a.store.limits.liveBytes {
		return nil, "", errors.New("attempt storage budget exhausted")
	}
	if staged, err := readSafeRegular(temporaryPath, len(data)); err == nil {
		if !bytes.Equal(staged, data) {
			return nil, "", errors.New("staged attempt publication conflicts")
		}
		file, err := openNoFollow(temporaryPath, false, 0o600)
		if err != nil {
			return nil, "", err
		}
		syncErr := a.store.syncFile(file)
		closeErr := file.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return nil, "", err
		}
		if err := renameNoReplace(temporaryPath, destination); err != nil {
			return nil, "", err
		}
		if err := syncPathDirectory(a.records, a.store.syncDir); err != nil {
			return nil, "", err
		}
		if err := syncPathDirectory(a.temporary, a.store.syncDir); err != nil {
			return nil, "", err
		}
		return append([]byte(nil), data...), digest(data), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	if inv.temporaryFiles >= a.store.limits.temporaryFiles || int64(len(data)) > a.store.limits.temporaryBytes {
		return nil, "", errors.New("attempt temporary storage budget exhausted")
	}
	f, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, "", err
	}
	writeErr := writeFullStore(f, data)
	syncErr := a.store.syncFile(f)
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return nil, "", err
	}
	confirmed, err := readSafeRegular(temporaryPath, len(data))
	if err != nil || !bytes.Equal(confirmed, data) {
		return nil, "", errors.Join(errors.New("temporary publication bytes did not verify"), err)
	}
	if err := syncPathDirectory(a.temporary, a.store.syncDir); err != nil {
		return nil, "", err
	}
	if _, err := os.Lstat(destination); err == nil {
		return nil, "", errors.New("attempt destination appeared during publication")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	if err := renameNoReplace(temporaryPath, destination); err != nil {
		return nil, "", err
	}
	if err := syncPathDirectory(a.records, a.store.syncDir); err != nil {
		return nil, "", err
	}
	if err := syncPathDirectory(a.temporary, a.store.syncDir); err != nil {
		return nil, "", err
	}
	stored, err := readSafeRegular(destination, bound)
	if err != nil || !bytes.Equal(stored, data) {
		return nil, "", errors.Join(errors.New("published attempt record did not verify"), err)
	}
	return stored, digest(stored), nil
}

func (s *durableStore) verifyStorageReservationLocked(attemptID, name string, data []byte) error {
	record := storageReservationV1{"merge-storage-reservation-v1", attemptID, name, int64(len(data)), digest(data)}
	encoded, _ := json.Marshal(record)
	reservations, err := s.storageReservationsLocked()
	if err != nil {
		return err
	}
	existing, found := reservations[digest(encoded)]
	if !found || !bytes.Equal(existing, encoded) {
		return errors.New("published attempt record has no exact durable storage reservation")
	}
	return nil
}

func (a *attemptStore) read(name string) ([]byte, bool, error) {
	bound, ok := allowedAttemptRecords[name]
	if a == nil || a.closed || !ok {
		return nil, false, errors.New("attempt record name is invalid")
	}
	if err := a.checkIdentity(); err != nil {
		return nil, false, err
	}
	data, err := readSafeRegular(filepath.Join(a.records, name), bound)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func (a *attemptStore) cleanupTemporary() error {
	if err := a.checkIdentity(); err != nil {
		return err
	}
	inv, err := a.inventory()
	if err != nil {
		return err
	}
	for _, name := range inv.names {
		if !strings.HasPrefix(name, "tmp/") {
			continue
		}
		if err := a.store.remove(filepath.Join(a.root, name)); err != nil {
			return err
		}
	}
	return syncPathDirectory(a.temporary, a.store.syncDir)
}

func (a *attemptStore) reserveCounter(category string) (Counters, error) {
	if a == nil || a.closed {
		return Counters{}, errors.New("attempt store is closed")
	}
	if err := a.checkIdentity(); err != nil {
		return Counters{}, err
	}
	path := filepath.Join(a.root, "counters.jsonl")
	_, beforeErr := os.Lstat(path)
	file, err := openOrCreateRegular(path, 0o600)
	if err != nil {
		return Counters{}, err
	}
	defer file.Close()
	if errors.Is(beforeErr, os.ErrNotExist) {
		if err := syncPathDirectory(a.root, a.store.syncDir); err != nil {
			return Counters{}, err
		}
	} else if beforeErr != nil {
		return Counters{}, beforeErr
	}
	last, err := readCounters(file, a.id, a.store.limits)
	if err != nil {
		return Counters{}, err
	}
	if last.ProviderAccountingPending {
		return Counters{}, errors.New("prior provider operation is missing durable accounting")
	}
	last, ok := nextCounters(last, category)
	if !ok {
		return Counters{}, errors.New("unknown provider counter category")
	}
	if err := last.validate(a.store.limits); err != nil {
		return Counters{}, err
	}
	record, err := json.Marshal(counterReservation{a.id, category, last})
	if err != nil {
		return Counters{}, err
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return Counters{}, err
	}
	if err := writeFullStore(file, append(record, '\n')); err != nil {
		return Counters{}, err
	}
	if err := a.store.syncFile(file); err != nil {
		return Counters{}, err
	}
	confirmed, err := readCounters(file, a.id, a.store.limits)
	if err != nil || confirmed != last {
		return Counters{}, errors.Join(errors.New("provider counter reservation did not verify"), err)
	}
	return last, nil
}

func providerBudgetAvailable(c Counters, limits Limits) bool {
	return c.TotalProviderCalls < limits.providerCalls && c.CumulativeRequestBytes < limits.cumulativeRequestBytes &&
		c.CumulativeHeaderBytes < limits.cumulativeHeaderBytes && c.CumulativeCompressedBytes < limits.cumulativeCompressedBytes &&
		c.CumulativeDecompressedBytes < limits.cumulativeDecompressedBytes && time.Duration(c.CumulativeCallNanos) < limits.cumulativeProviderTime
}

func httpCallCategory(class ProviderCallClassV1) string { return "http-" + string(class) }

type providerHTTPCallAudit struct {
	commit                int
	target                int
	reconciliationInRound int
	pending               ProviderCallClassV1
}

func readProviderHTTPCallAudit(file *os.File, attemptID string) (providerHTTPCallAudit, error) {
	var audit providerHTTPCallAudit
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return audit, err
	}
	scanner := bufio.NewScanner(io.LimitReader(file, MaxTerminalChannelBytes+1))
	scanner.Buffer(make([]byte, 4096), MaxTerminalRecordBytes)
	for scanner.Scan() {
		var record counterReservation
		if err := strictCanonical(scanner.Bytes(), &record); err != nil || record.AttemptID != attemptID {
			return audit, errors.New("provider HTTP-call audit is invalid")
		}
		switch record.Category {
		case httpCallCategory(ProviderCallCommitSubmissionV1):
			audit.commit++
			audit.pending = ProviderCallCommitSubmissionV1
		case httpCallCategory(ProviderCallTargetV1):
			audit.target++
			audit.pending = ProviderCallTargetV1
		case httpCallCategory(ProviderCallPreSubmitV1):
			audit.pending = ProviderCallPreSubmitV1
		case httpCallCategory(ProviderCallPostMergeV1):
			audit.pending = ProviderCallPostMergeV1
		case httpCallCategory(ProviderCallReconciliationV1):
			audit.reconciliationInRound++
			audit.pending = ProviderCallReconciliationV1
		case "reconciliation-round":
			audit.reconciliationInRound = 0
		case "http-accounting":
			audit.pending = ""
		}
	}
	return audit, scanner.Err()
}

// reserveHTTPCall increments the exact classified and aggregate call counters
// and makes the pending request durable before transport can begin.
func (a *attemptStore) reserveHTTPCall(class ProviderCallClassV1) (Counters, ProviderBudgetV1, error) {
	if a == nil || a.closed || !class.valid() {
		return Counters{}, ProviderBudgetV1{}, errors.New("valid provider call class and open attempt are required")
	}
	if err := a.checkIdentity(); err != nil {
		return Counters{}, ProviderBudgetV1{}, err
	}
	path := filepath.Join(a.root, "counters.jsonl")
	file, err := openOrCreateRegular(path, 0o600)
	if err != nil {
		return Counters{}, ProviderBudgetV1{}, err
	}
	defer file.Close()
	last, err := readCounters(file, a.id, a.store.limits)
	if err != nil {
		return Counters{}, ProviderBudgetV1{}, err
	}
	if last.ProviderAccountingPending {
		return Counters{}, ProviderBudgetV1{}, errors.New("prior provider HTTP call is missing durable accounting")
	}
	if !providerBudgetAvailable(last, a.store.limits) {
		return Counters{}, ProviderBudgetV1{}, errors.New("provider cumulative budget exhausted before HTTP call")
	}
	audit, err := readProviderHTTPCallAudit(file, a.id)
	if err != nil {
		return Counters{}, ProviderBudgetV1{}, err
	}
	if class == ProviderCallCommitSubmissionV1 && (audit.commit >= 1 || last.CommitSubmissions != audit.commit+1) ||
		class == ProviderCallTargetV1 && (audit.target >= 1 || last.TargetSubmissions != audit.target+1) ||
		class == ProviderCallReconciliationV1 && (last.ReconciliationRounds == 0 || audit.reconciliationInRound >= 3) {
		return Counters{}, ProviderBudgetV1{}, errors.New("provider HTTP call lacks its durable operation or round reservation")
	}
	next, ok := nextCounters(last, httpCallCategory(class))
	if !ok {
		return Counters{}, ProviderBudgetV1{}, errors.New("provider HTTP call class is not permitted")
	}
	if err := next.validate(a.store.limits); err != nil {
		return Counters{}, ProviderBudgetV1{}, err
	}
	record, err := json.Marshal(counterReservation{a.id, httpCallCategory(class), next})
	if err != nil {
		return Counters{}, ProviderBudgetV1{}, err
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return Counters{}, ProviderBudgetV1{}, err
	}
	if err := writeFullStore(file, append(record, '\n')); err != nil {
		return Counters{}, ProviderBudgetV1{}, err
	}
	if err := a.store.syncFile(file); err != nil {
		return Counters{}, ProviderBudgetV1{}, err
	}
	if last.TotalProviderCalls == 0 {
		if err := syncPathDirectory(a.root, a.store.syncDir); err != nil {
			return Counters{}, ProviderBudgetV1{}, err
		}
	}
	confirmed, err := readCounters(file, a.id, a.store.limits)
	if err != nil || confirmed != next {
		return Counters{}, ProviderBudgetV1{}, errors.Join(errors.New("provider HTTP call reservation did not verify"), err)
	}
	budget := providerBudgetAfterCounters(confirmed, a.store.limits)
	budget.HTTPCalls = 1
	return confirmed, budget, nil
}

// accountHTTPCall closes exactly one matching reservation and durably records
// all bytes and active time before another reservation can be made.
func (a *attemptStore) accountHTTPCall(class ProviderCallClassV1, accounting ProviderCallAccountingV1) (Counters, error) {
	if a == nil || a.closed || !class.valid() || accounting.validate() != nil {
		return Counters{}, errors.New("valid provider call accounting and open attempt are required")
	}
	if err := a.checkIdentity(); err != nil {
		return Counters{}, err
	}
	path := filepath.Join(a.root, "counters.jsonl")
	file, err := openOrCreateRegular(path, 0o600)
	if err != nil {
		return Counters{}, err
	}
	defer file.Close()
	last, err := readCounters(file, a.id, a.store.limits)
	if err != nil {
		return Counters{}, err
	}
	audit, auditErr := readProviderHTTPCallAudit(file, a.id)
	if auditErr != nil {
		return Counters{}, auditErr
	}
	if !last.ProviderAccountingPending || audit.pending != class {
		return Counters{}, errors.New("provider call accounting has no matching reservation")
	}
	next := last
	next.Sequence++
	next.ProviderAccountingPending = false
	next.CumulativeRequestBytes += accounting.RequestBytes
	next.CumulativeHeaderBytes += accounting.HeaderBytes
	next.CumulativeCompressedBytes += accounting.CompressedResponseBytes
	next.CumulativeDecompressedBytes += accounting.DecompressedResponseBytes
	next.CumulativeCallNanos += accounting.ActiveNanos
	if err := next.validate(a.store.limits); err != nil {
		return Counters{}, err
	}
	record, err := json.Marshal(counterReservation{a.id, "http-accounting", next})
	if err != nil {
		return Counters{}, err
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return Counters{}, err
	}
	if err := writeFullStore(file, append(record, '\n')); err != nil {
		return Counters{}, err
	}
	if err := a.store.syncFile(file); err != nil {
		return Counters{}, err
	}
	confirmed, err := readCounters(file, a.id, a.store.limits)
	if err != nil || confirmed != next {
		return Counters{}, errors.Join(errors.New("provider HTTP call accounting did not verify"), err)
	}
	return next, nil
}

func (a *attemptStore) reserveReconciliation(nowUnixNano int64) (Counters, error) {
	current, err := a.currentCounters()
	if err != nil {
		return Counters{}, err
	}
	if nowUnixNano <= 0 || current.LastReconciliationUnixNano > 0 &&
		nowUnixNano-current.LastReconciliationUnixNano < int64(a.store.limits.reconciliationInterval) {
		return Counters{}, errors.New("minimum reconciliation interval has not elapsed")
	}
	return a.reserveCounterValue("reconciliation-round", func(next *Counters) {
		next.ReconciliationRounds++
		next.LastReconciliationUnixNano = nowUnixNano
	})
}

func (a *attemptStore) reserveCounterValue(category string, mutate func(*Counters)) (Counters, error) {
	if a == nil || a.closed || mutate == nil {
		return Counters{}, errors.New("attempt store is closed")
	}
	path := filepath.Join(a.root, "counters.jsonl")
	file, err := openOrCreateRegular(path, 0o600)
	if err != nil {
		return Counters{}, err
	}
	defer file.Close()
	last, err := readCounters(file, a.id, a.store.limits)
	if err != nil {
		return Counters{}, err
	}
	if last.ProviderAccountingPending {
		return Counters{}, errors.New("prior provider operation is missing durable accounting")
	}
	next := last
	next.Sequence++
	mutate(&next)
	if err := next.validate(a.store.limits); err != nil {
		return Counters{}, err
	}
	record, err := json.Marshal(counterReservation{a.id, category, next})
	if err != nil {
		return Counters{}, err
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return Counters{}, err
	}
	if err := writeFullStore(file, append(record, '\n')); err != nil {
		return Counters{}, err
	}
	if err := a.store.syncFile(file); err != nil {
		return Counters{}, err
	}
	return next, nil
}

func (a *attemptStore) currentCounters() (Counters, error) {
	if a == nil || a.closed {
		return Counters{}, errors.New("attempt store is closed")
	}
	if err := a.checkIdentity(); err != nil {
		return Counters{}, err
	}
	path := filepath.Join(a.root, "counters.jsonl")
	file, err := openOrCreateRegular(path, 0o600)
	if err != nil {
		return Counters{}, err
	}
	defer file.Close()
	return readCounters(file, a.id, a.store.limits)
}

func (a *attemptStore) providerBudget() (ProviderBudgetV1, error) {
	counters, err := a.currentCounters()
	if err != nil {
		return ProviderBudgetV1{}, err
	}
	if counters.ProviderAccountingPending {
		return ProviderBudgetV1{}, errors.New("provider HTTP call accounting continuation is unresolved")
	}
	budget := providerBudgetAfterCounters(counters, a.store.limits)
	if budget.HTTPCalls <= 0 || budget.RequestBytes <= 0 || budget.HeaderBytes <= 0 || budget.CompressedResponseBytes <= 0 ||
		budget.DecompressedResponseBytes <= 0 || budget.ActiveNanos <= 0 {
		return ProviderBudgetV1{}, errProviderBudgetExhausted
	}
	return budget, nil
}

func providerBudgetAfterCounters(counters Counters, limits Limits) ProviderBudgetV1 {
	return ProviderBudgetV1{
		HTTPCalls:                 limits.providerCalls - counters.TotalProviderCalls,
		RequestBytes:              limits.cumulativeRequestBytes - counters.CumulativeRequestBytes,
		HeaderBytes:               limits.cumulativeHeaderBytes - counters.CumulativeHeaderBytes,
		CompressedResponseBytes:   limits.cumulativeCompressedBytes - counters.CumulativeCompressedBytes,
		DecompressedResponseBytes: limits.cumulativeDecompressedBytes - counters.CumulativeDecompressedBytes,
		ActiveNanos:               int64(limits.cumulativeProviderTime) - counters.CumulativeCallNanos,
	}
}

func readCounters(file *os.File, attemptID string, limits Limits) (Counters, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Counters{}, err
	}
	result := Counters{Schema: "merge-counters-v1"}
	scanner := bufio.NewScanner(io.LimitReader(file, MaxTerminalChannelBytes+1))
	scanner.Buffer(make([]byte, 4096), MaxTerminalRecordBytes)
	for scanner.Scan() {
		var record counterReservation
		if err := strictCanonical(scanner.Bytes(), &record); err != nil || record.AttemptID != attemptID || !validCounterStep(result, record.Category, record.Counters, limits) {
			return Counters{}, errors.New("provider counter channel is invalid or discontinuous")
		}
		if err := record.Counters.validate(limits); err != nil {
			return Counters{}, err
		}
		result = record.Counters
	}
	if err := scanner.Err(); err != nil {
		return Counters{}, err
	}
	return result, nil
}

func nextCounters(previous Counters, category string) (Counters, bool) {
	next := previous
	next.Sequence++
	switch category {
	case "commit-submission":
		next.CommitSubmissions++
	case "target-submission":
		next.TargetSubmissions++
	case httpCallCategory(ProviderCallPreSubmitV1):
		next.PreSubmitCalls++
		next.TotalProviderCalls++
		next.ProviderAccountingPending = true
	case httpCallCategory(ProviderCallCommitSubmissionV1):
		next.TotalProviderCalls++
		next.ProviderAccountingPending = true
	case httpCallCategory(ProviderCallTargetV1):
		next.TotalProviderCalls++
		next.ProviderAccountingPending = true
	case httpCallCategory(ProviderCallPostMergeV1):
		next.PostMergeCalls++
		next.TotalProviderCalls++
		next.ProviderAccountingPending = true
	case "reconciliation-round":
		next.ReconciliationRounds++
	case httpCallCategory(ProviderCallReconciliationV1):
		next.ReconciliationCalls++
		next.TotalProviderCalls++
		next.ProviderAccountingPending = true
	default:
		return Counters{}, false
	}
	return next, true
}

func validCounterStep(previous Counters, category string, observed Counters, limits Limits) bool {
	if category == "http-accounting" {
		return observed.Sequence == previous.Sequence+1 && observed.PreSubmitCalls == previous.PreSubmitCalls &&
			observed.CommitSubmissions == previous.CommitSubmissions && observed.TargetSubmissions == previous.TargetSubmissions &&
			observed.PostMergeCalls == previous.PostMergeCalls && observed.ReconciliationRounds == previous.ReconciliationRounds &&
			observed.ReconciliationCalls == previous.ReconciliationCalls && observed.TotalProviderCalls == previous.TotalProviderCalls &&
			previous.ProviderAccountingPending && !observed.ProviderAccountingPending &&
			observed.LastInvocationNanos == previous.LastInvocationNanos && observed.LastReconciliationUnixNano == previous.LastReconciliationUnixNano &&
			observed.CumulativeRequestBytes >= previous.CumulativeRequestBytes && observed.CumulativeHeaderBytes >= previous.CumulativeHeaderBytes &&
			observed.CumulativeCompressedBytes >= previous.CumulativeCompressedBytes && observed.CumulativeDecompressedBytes >= previous.CumulativeDecompressedBytes &&
			observed.CumulativeCallNanos >= previous.CumulativeCallNanos && observed.validate(limits) == nil
	}
	if category == "reconciliation-round" {
		if previous.ProviderAccountingPending {
			return false
		}
		expected := previous
		expected.Sequence++
		expected.ReconciliationRounds++
		expected.LastReconciliationUnixNano = observed.LastReconciliationUnixNano
		return observed.LastReconciliationUnixNano > previous.LastReconciliationUnixNano && expected == observed
	}
	for _, candidate := range []string{"commit-submission", "target-submission", httpCallCategory(ProviderCallPreSubmitV1),
		httpCallCategory(ProviderCallCommitSubmissionV1), httpCallCategory(ProviderCallTargetV1),
		httpCallCategory(ProviderCallPostMergeV1), httpCallCategory(ProviderCallReconciliationV1)} {
		if candidate == category {
			if previous.ProviderAccountingPending {
				return false
			}
			expected, _ := nextCounters(previous, candidate)
			if expected == observed {
				return true
			}
		}
	}
	return false
}

func (a *attemptStore) inventory() (inventory, error) {
	var inv inventory
	if err := a.checkIdentity(); err != nil {
		return inv, err
	}
	for _, child := range []struct {
		path string
		tmp  bool
	}{{a.records, false}, {a.temporary, true}} {
		entries, err := os.ReadDir(child.path)
		if err != nil {
			return inv, err
		}
		for _, entry := range entries {
			path := filepath.Join(child.path, entry.Name())
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || !ownedByEffectiveUser(info) {
				return inv, errors.Join(errors.New("attempt namespace contains an unsafe entry"), err)
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Nlink != 1 {
				return inv, errors.New("attempt namespace contains a hard-linked entry")
			}
			if child.tmp {
				trimmed := strings.TrimSuffix(entry.Name(), ".tmp")
				separator := strings.LastIndexByte(trimmed, '-')
				if !strings.HasSuffix(entry.Name(), ".tmp") || separator <= 0 {
					return inv, errors.New("attempt namespace contains an unclassified temporary")
				}
				destinationBytes, decodeErr := hex.DecodeString(trimmed[:separator])
				bound, known := allowedAttemptRecords[string(destinationBytes)]
				if decodeErr != nil || !known || !validDigest(trimmed[separator+1:]) || info.Size() > int64(bound) || info.Size() > a.store.limits.temporaryBytes {
					return inv, errors.New("attempt namespace contains an unclassified temporary")
				}
				inv.temporaryFiles++
				inv.temporaryBytes += info.Size()
				inv.names = append(inv.names, filepath.Join("tmp", entry.Name()))
			} else {
				bound, known := allowedAttemptRecords[entry.Name()]
				if !known || info.Size() <= 0 || info.Size() > int64(bound) {
					return inv, errors.New("attempt namespace contains an unknown or oversized record")
				}
				inv.publishedFiles++
				inv.publishedBytes += info.Size()
				inv.names = append(inv.names, filepath.Join("records", entry.Name()))
			}
		}
	}
	if inv.publishedFiles > a.store.limits.publishedFiles || inv.publishedBytes > a.store.limits.publishedBytes ||
		inv.temporaryFiles > a.store.limits.temporaryFiles || inv.temporaryBytes > a.store.limits.temporaryBytes ||
		inv.publishedFiles+inv.temporaryFiles > a.store.limits.liveFiles || inv.publishedBytes+inv.temporaryBytes > a.store.limits.liveBytes {
		return inv, errors.New("attempt namespace exceeds storage policy")
	}
	sort.Strings(inv.names)
	return inv, nil
}

func (s *durableStore) inventoryGlobal() error {
	if err := s.checkRoot(); err != nil {
		return err
	}
	rootEntries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	for _, entry := range rootEntries {
		if entry.Name() != "attempts" && entry.Name() != "channels" && entry.Name() != "resource-locks" && entry.Name() != "store.lock" {
			return errors.New("merge state root contains an unknown entry")
		}
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "attempts"))
	if err != nil {
		return err
	}
	if len(entries) > s.limits.publishedFilesTotal {
		return errors.New("global merge attempt capacity exhausted")
	}
	var files int
	var bytesTotal int64
	var temporary int
	published := make(map[string]storageReservationV1)
	temporaryFiles := make(map[string]int)
	temporaryBytes := make(map[string]int64)
	for _, entry := range entries {
		attemptRoot := filepath.Join(s.root, "attempts", entry.Name())
		if !entry.IsDir() || !validDigest(entry.Name()) || verifyDirectory(attemptRoot, 0o700) != nil {
			return errors.New("merge attempt root contains an unsafe entry")
		}
		rootChildren, err := os.ReadDir(attemptRoot)
		if err != nil {
			return err
		}
		for _, child := range rootChildren {
			if child.Name() != "records" && child.Name() != "tmp" && child.Name() != "attempt.lock" && child.Name() != "counters.jsonl" {
				return errors.New("merge attempt contains an unknown root entry")
			}
			path := filepath.Join(attemptRoot, child.Name())
			if child.Name() == "records" || child.Name() == "tmp" {
				if err := verifyDirectory(path, 0o700); err != nil {
					return errors.New("merge attempt contains an unsafe child directory")
				}
				continue
			}
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownedByEffectiveUser(info) {
				return errors.New("merge attempt contains an unsafe control file")
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Nlink != 1 {
				return errors.New("merge attempt control file is hard linked")
			}
		}
		for _, child := range []string{"records", "tmp"} {
			childEntries, err := os.ReadDir(filepath.Join(s.root, "attempts", entry.Name(), child))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			for _, item := range childEntries {
				path := filepath.Join(s.root, "attempts", entry.Name(), child, item.Name())
				info, err := os.Lstat(path)
				if err != nil {
					return errors.New("global merge inventory cannot inspect an entry")
				}
				stat, statOK := info.Sys().(*syscall.Stat_t)
				if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownedByEffectiveUser(info) || !statOK || stat.Nlink != 1 {
					return errors.New("global merge inventory contains an unsafe entry")
				}
				files++
				bytesTotal += info.Size()
				if child == "tmp" {
					temporary++
					temporaryFiles[entry.Name()]++
					temporaryBytes[entry.Name()] += info.Size()
					continue
				}
				bound, known := allowedAttemptRecords[item.Name()]
				if !known || info.Size() <= 0 || info.Size() > int64(bound) {
					return errors.New("global merge inventory contains an unknown or oversized published record")
				}
				data, readErr := readSafeRegular(path, bound)
				if readErr != nil {
					return readErr
				}
				published[entry.Name()+"\x00"+item.Name()] = storageReservationV1{
					Schema: "merge-storage-reservation-v1", AttemptID: entry.Name(), RecordName: item.Name(), Bytes: int64(len(data)), SHA256: digest(data),
				}
			}
		}
	}
	channelEntries, err := os.ReadDir(filepath.Join(s.root, "channels"))
	if err != nil {
		return err
	}
	lockEntries, err := os.ReadDir(filepath.Join(s.root, "resource-locks"))
	if err != nil {
		return err
	}
	for _, entry := range lockEntries {
		name := strings.TrimSuffix(entry.Name(), ".lock")
		info, infoErr := entry.Info()
		if infoErr != nil || !validDigest(name) || entry.Name() != name+".lock" || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownedByEffectiveUser(info) {
			return errors.New("repository/base lock namespace contains an unsafe entry")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 {
			return errors.New("repository/base lock is hard linked")
		}
	}
	for _, entry := range channelEntries {
		name := strings.TrimSuffix(entry.Name(), ".frames")
		if _, allowed := allowedChannels[name]; !allowed || name+".frames" != entry.Name() {
			return errors.New("merge channel namespace contains an unknown entry")
		}
		info, err := os.Lstat(filepath.Join(s.root, "channels", entry.Name()))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownedByEffectiveUser(info) {
			return errors.New("merge channel namespace contains an unsafe entry")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 || info.Size() > s.limits.terminalChannelBytes {
			return errors.New("merge channel namespace contains an invalid file")
		}
		files++
		bytesTotal += info.Size()
	}
	if err := s.validateStorageReservationsLocked(published, temporaryFiles, temporaryBytes); err != nil {
		return err
	}
	if files > s.limits.publishedFilesTotal+s.limits.activeTemporaryFiles || bytesTotal > s.limits.publishedBytesTotal+s.limits.terminalChannelBytes || temporary > s.limits.activeTemporaryFiles {
		return errors.New("global merge storage budget exhausted")
	}
	return nil
}

func (s *durableStore) validateStorageReservationsLocked(published map[string]storageReservationV1, temporaryFiles map[string]int, temporaryBytes map[string]int64) error {
	reservations, err := s.storageReservationsLocked()
	if err != nil {
		return err
	}
	bySlot := make(map[string]storageReservationV1, len(reservations))
	filesByAttempt := make(map[string]int)
	bytesByAttempt := make(map[string]int64)
	var globalFiles int
	var globalBytes int64
	for key, payload := range reservations {
		var reservation storageReservationV1
		bound, known := 0, false
		if err := strictCanonical(payload, &reservation); err == nil {
			bound, known = allowedAttemptRecords[reservation.RecordName]
		}
		if key != digest(payload) || reservation.Schema != "merge-storage-reservation-v1" || !validDigest(reservation.AttemptID) || !known ||
			reservation.Bytes <= 0 || reservation.Bytes > int64(bound) || !validDigest(reservation.SHA256) {
			return errors.New("durable storage reservation is invalid")
		}
		slot := reservation.AttemptID + "\x00" + reservation.RecordName
		if prior, duplicate := bySlot[slot]; duplicate && prior != reservation {
			return errors.New("durable storage reservation slot conflicts")
		}
		bySlot[slot] = reservation
		filesByAttempt[reservation.AttemptID]++
		bytesByAttempt[reservation.AttemptID] += reservation.Bytes
		globalFiles++
		globalBytes += reservation.Bytes
		if actual, exists := published[slot]; exists && actual != reservation {
			return errors.New("published record disagrees with its durable storage reservation")
		}
	}
	for slot := range published {
		if _, reserved := bySlot[slot]; !reserved {
			return errors.New("published record has no durable storage reservation")
		}
	}
	if globalFiles > s.limits.publishedFilesTotal || globalBytes > s.limits.publishedBytesTotal {
		return errors.New("durable global storage reservations exceed policy")
	}
	for attemptID, reservedFiles := range filesByAttempt {
		reservedBytes := bytesByAttempt[attemptID]
		if reservedFiles > s.limits.publishedFiles || reservedBytes > s.limits.publishedBytes ||
			reservedFiles+temporaryFiles[attemptID] > s.limits.liveFiles || reservedBytes+temporaryBytes[attemptID] > s.limits.liveBytes {
			return errors.New("durable attempt storage reservations exceed policy")
		}
	}
	return nil
}

func (s *durableStore) appendChannel(channel, kind, key string, data []byte, maxRecord int) (ledger.EvidenceRef, error) {
	_, allowed := allowedChannels[channel]
	if s == nil || !allowed || !validDigest(key) || len(data) == 0 || len(data) > maxRecord || len(data) > s.limits.terminalRecordBytes || !json.Valid(data) {
		return ledger.EvidenceRef{}, errors.New("channel record identity or size is invalid")
	}
	if err := s.checkRoot(); err != nil {
		return ledger.EvidenceRef{}, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil || !bytes.Equal(compact.Bytes(), data) {
		return ledger.EvidenceRef{}, errors.New("channel record JSON is not compact")
	}
	if err := flock(s.lock, false); err != nil {
		return ledger.EvidenceRef{}, err
	}
	defer funlock(s.lock)
	if err := s.checkRoot(); err != nil {
		return ledger.EvidenceRef{}, err
	}
	return s.appendChannelLocked(channel, kind, key, data, maxRecord)
}

func (s *durableStore) appendChannelLocked(channel, kind, key string, data []byte, maxRecord int) (ledger.EvidenceRef, error) {
	path := filepath.Join(s.root, "channels", channel+".frames")
	file, err := openOrCreateRegular(path, 0o600)
	if err != nil {
		return ledger.EvidenceRef{}, err
	}
	defer file.Close()
	if err := flock(file, false); err != nil {
		return ledger.EvidenceRef{}, err
	}
	defer funlock(file)
	found, offset, records, err := scanFrames(file, key, data, s.limits)
	if err != nil {
		return ledger.EvidenceRef{}, err
	}
	if !found {
		frame := encodeFrame(key, data)
		if offset > s.limits.terminalChannelBytes-int64(len(frame)) || records >= s.limits.terminalChannelRecords {
			return ledger.EvidenceRef{}, errors.New("durable channel capacity exhausted")
		}
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			return ledger.EvidenceRef{}, err
		}
		if err := writeFullStore(file, frame); err != nil {
			return ledger.EvidenceRef{}, err
		}
	}
	if err := s.syncFile(file); err != nil {
		return ledger.EvidenceRef{}, err
	}
	found, _, _, err = scanFrames(file, key, data, s.limits)
	if err != nil || !found {
		return ledger.EvidenceRef{}, errors.Join(errors.New("durable channel record did not verify"), err)
	}
	if err := syncPathDirectory(filepath.Join(s.root, "channels"), s.syncDir); err != nil {
		return ledger.EvidenceRef{}, err
	}
	return ledger.EvidenceRef{URI: path + "#" + key, SHA256: digest(data), Kind: kind}, nil
}

func (s *durableStore) reserveStorageLocked(attempt *attemptStore, name string, data []byte) error {
	reservations, err := s.storageReservationsLocked()
	if err != nil {
		return err
	}
	record := storageReservationV1{"merge-storage-reservation-v1", attempt.id, name, int64(len(data)), digest(data)}
	encoded, _ := json.Marshal(record)
	key := digest(encoded)
	if existing, ok := reservations[key]; ok {
		if !bytes.Equal(existing, encoded) {
			return errors.New("storage reservation identity conflicts")
		}
		return nil
	}
	var attemptFiles, globalFiles int
	var attemptBytes, globalBytes int64
	slots := make(map[string]storageReservationV1)
	for key, payload := range reservations {
		var observed storageReservationV1
		if err := strictCanonical(payload, &observed); err != nil {
			return errors.New("durable storage reservation is invalid")
		}
		bound, allowed := allowedAttemptRecords[observed.RecordName]
		if key != digest(payload) || observed.Schema != "merge-storage-reservation-v1" || !validDigest(observed.AttemptID) || !allowed ||
			observed.Bytes <= 0 || observed.Bytes > int64(bound) || !validDigest(observed.SHA256) {
			return errors.New("durable storage reservation is invalid")
		}
		slot := observed.AttemptID + "\x00" + observed.RecordName
		if prior, duplicate := slots[slot]; duplicate && prior != observed {
			return errors.New("durable storage reservation slot conflicts")
		}
		slots[slot] = observed
		globalFiles++
		globalBytes += observed.Bytes
		if observed.AttemptID == attempt.id {
			attemptFiles++
			attemptBytes += observed.Bytes
		}
	}
	if _, conflict := slots[attempt.id+"\x00"+name]; conflict {
		return errors.New("storage reservation conflicts with an earlier record identity")
	}
	inv, err := attempt.inventory()
	if err != nil {
		return err
	}
	if attemptFiles+1 > s.limits.publishedFiles || attemptBytes+int64(len(data)) > s.limits.publishedBytes ||
		attemptFiles+1+inv.temporaryFiles > s.limits.liveFiles || attemptBytes+int64(len(data))+inv.temporaryBytes > s.limits.liveBytes ||
		globalFiles+1 > s.limits.publishedFilesTotal || globalBytes+int64(len(data)) > s.limits.publishedBytesTotal {
		return errors.New("durable storage reservation capacity exhausted")
	}
	_, err = s.appendChannelLocked("storage-reservations", "merge-storage-reservation", key, encoded, MaxTerminalRecordBytes)
	return err
}

func (s *durableStore) storageReservationsLocked() (map[string][]byte, error) {
	result := make(map[string][]byte)
	path := filepath.Join(s.root, "channels", "storage-reservations.frames")
	file, err := openNoFollow(path, false, 0o600)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReader(io.LimitReader(file, s.limits.terminalChannelBytes+1))
	for records := 0; ; records++ {
		if records > s.limits.terminalChannelRecords {
			return nil, errors.New("storage reservation scan bound exceeded")
		}
		header := make([]byte, 4+sha256HexBytes+4)
		_, readErr := io.ReadFull(reader, header)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, errors.New("storage reservation channel contains a partial frame")
		}
		keyLen := binary.BigEndian.Uint32(header[:4])
		dataLen := binary.BigEndian.Uint32(header[4+sha256HexBytes:])
		key := string(header[4 : 4+sha256HexBytes])
		if keyLen != sha256HexBytes || !validDigest(key) || dataLen == 0 || int(dataLen) > s.limits.terminalRecordBytes {
			return nil, errors.New("storage reservation channel contains an invalid frame")
		}
		payload := make([]byte, int(dataLen))
		if _, err := io.ReadFull(reader, payload); err != nil {
			return nil, errors.New("storage reservation channel contains a partial payload")
		}
		if prior, exists := result[key]; exists && !bytes.Equal(prior, payload) {
			return nil, errors.New("storage reservation key conflicts")
		}
		result[key] = payload
	}
	return result, nil
}

func (s *durableStore) readChannel(channel, key string, maxRecord int) ([]byte, bool, error) {
	_, allowed := allowedChannels[channel]
	if s == nil || !allowed || !validDigest(key) || maxRecord <= 0 || maxRecord > s.limits.terminalRecordBytes {
		return nil, false, errors.New("channel read identity or bound is invalid")
	}
	if err := s.checkRoot(); err != nil {
		return nil, false, err
	}
	path := filepath.Join(s.root, "channels", channel+".frames")
	file, err := openNoFollow(path, false, 0o600)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	if err := flock(file, false); err != nil {
		return nil, false, err
	}
	defer funlock(file)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, false, err
	}
	reader := bufio.NewReader(io.LimitReader(file, s.limits.terminalChannelBytes+1))
	seen := make(map[string]string)
	var result []byte
	for records := 0; ; records++ {
		if records > s.limits.terminalChannelRecords {
			return nil, false, errors.New("durable channel record bound exceeded")
		}
		header := make([]byte, 4+sha256HexBytes+4)
		_, err := io.ReadFull(reader, header)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false, errors.New("durable channel contains a partial frame")
		}
		keyLen := binary.BigEndian.Uint32(header[:4])
		dataLen := binary.BigEndian.Uint32(header[4+sha256HexBytes:])
		frameKey := string(header[4 : 4+sha256HexBytes])
		if keyLen != sha256HexBytes || !validDigest(frameKey) || dataLen == 0 || int(dataLen) > s.limits.terminalRecordBytes {
			return nil, false, errors.New("durable channel contains an invalid frame")
		}
		payload := make([]byte, int(dataLen))
		if _, err := io.ReadFull(reader, payload); err != nil {
			return nil, false, errors.New("durable channel contains a partial payload")
		}
		payloadSHA := digest(payload)
		if prior, exists := seen[frameKey]; exists && prior != payloadSHA {
			return nil, false, errors.New("durable channel key conflicts")
		}
		seen[frameKey] = payloadSHA
		if frameKey == key {
			if result != nil && !bytes.Equal(result, payload) {
				return nil, false, errors.New("durable channel replay conflicts")
			}
			if len(payload) > maxRecord {
				return nil, false, errors.New("durable channel record exceeds read bound")
			}
			result = append([]byte(nil), payload...)
		}
	}
	return result, result != nil, nil
}

// findTerminalCore bounded-scans the independent terminal-intents channel by
// run identity. This is intentionally not dependent on the ledger event: the
// core is the terminal-selection linearization point and may be durable while
// the ledger still ends at READY.
func (s *durableStore) findTerminalCore(runID string) (TerminalCoreV1, []byte, string, bool, error) {
	var zero TerminalCoreV1
	if s == nil || runID == "" {
		return zero, nil, "", false, errors.New("store and run identity are required")
	}
	if err := s.checkRoot(); err != nil {
		return zero, nil, "", false, err
	}
	path := filepath.Join(s.root, "channels", "terminal-intents.frames")
	file, err := openNoFollow(path, false, 0o600)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
		return zero, nil, "", false, nil
	}
	if err != nil {
		return zero, nil, "", false, err
	}
	defer file.Close()
	if err := flock(file, false); err != nil {
		return zero, nil, "", false, err
	}
	defer funlock(file)
	reader := bufio.NewReader(io.LimitReader(file, s.limits.terminalChannelBytes+1))
	var selected TerminalCoreV1
	var selectedBytes []byte
	var selectedSHA string
	for records := 0; ; records++ {
		if records > s.limits.terminalChannelRecords {
			return zero, nil, "", false, errors.New("terminal intent scan bound exceeded")
		}
		header := make([]byte, 4+sha256HexBytes+4)
		_, readErr := io.ReadFull(reader, header)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return zero, nil, "", false, errors.New("terminal intent channel contains a partial frame")
		}
		keyLen := binary.BigEndian.Uint32(header[:4])
		dataLen := binary.BigEndian.Uint32(header[4+sha256HexBytes:])
		key := string(header[4 : 4+sha256HexBytes])
		if keyLen != sha256HexBytes || !validDigest(key) || dataLen == 0 || int(dataLen) > s.limits.terminalRecordBytes {
			return zero, nil, "", false, errors.New("terminal intent channel contains an invalid frame")
		}
		payload := make([]byte, int(dataLen))
		if _, err := io.ReadFull(reader, payload); err != nil {
			return zero, nil, "", false, errors.New("terminal intent channel contains a partial core")
		}
		core, err := ParseTerminalCoreV1(payload)
		if err != nil || key != digest(payload) {
			return zero, nil, "", false, errors.Join(errors.New("terminal intent channel contains an invalid core"), err)
		}
		if core.RunID != runID {
			continue
		}
		if selectedSHA != "" && (selectedSHA != key || !bytes.Equal(selectedBytes, payload)) {
			return zero, nil, "", false, errors.New("run has conflicting durable terminal selections")
		}
		selected, selectedBytes, selectedSHA = core, append([]byte(nil), payload...), key
	}
	return selected, selectedBytes, selectedSHA, selectedSHA != "", nil
}

func (s *durableStore) nextCleanupSequence(coreSHA string) (int, bool, error) {
	if s == nil || !validDigest(coreSHA) {
		return 0, false, errors.New("valid terminal core identity is required")
	}
	if err := s.checkRoot(); err != nil {
		return 0, false, err
	}
	path := filepath.Join(s.root, "channels", "cleanup-incidents.frames")
	file, err := openNoFollow(path, false, 0o600)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
		return 1, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer file.Close()
	if err := flock(file, false); err != nil {
		return 0, false, err
	}
	defer funlock(file)
	reader := bufio.NewReader(io.LimitReader(file, s.limits.terminalChannelBytes+1))
	sequences := make(map[int]struct{})
	for records := 0; ; records++ {
		if records > s.limits.terminalChannelRecords {
			return 0, false, errors.New("cleanup incident scan bound exceeded")
		}
		header := make([]byte, 4+sha256HexBytes+4)
		_, readErr := io.ReadFull(reader, header)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 0, false, errors.New("cleanup incident channel contains a partial frame")
		}
		dataLen := binary.BigEndian.Uint32(header[4+sha256HexBytes:])
		if dataLen == 0 || int(dataLen) > s.limits.cleanupIncidentBytes {
			return 0, false, errors.New("cleanup incident channel contains an invalid frame")
		}
		payload := make([]byte, int(dataLen))
		if _, err := io.ReadFull(reader, payload); err != nil {
			return 0, false, err
		}
		incident, err := ParseLocalCleanupIncidentV1(payload)
		if err != nil {
			return 0, false, err
		}
		if incident.TerminalCoreSHA256 == coreSHA {
			if _, duplicate := sequences[incident.Sequence]; duplicate {
				return 0, false, errors.New("cleanup incident sequence is duplicated")
			}
			sequences[incident.Sequence] = struct{}{}
		}
	}
	for sequence := 1; sequence <= len(sequences); sequence++ {
		if _, ok := sequences[sequence]; !ok {
			return 0, false, errors.New("cleanup incident sequence is discontinuous")
		}
	}
	if len(sequences) >= s.limits.cleanupAttempts {
		return len(sequences) + 1, true, nil
	}
	return len(sequences) + 1, false, nil
}

func encodeFrame(key string, data []byte) []byte {
	frame := make([]byte, 4+sha256HexBytes+4+len(data))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(key)))
	copy(frame[4:4+sha256HexBytes], key)
	binary.BigEndian.PutUint32(frame[4+sha256HexBytes:8+sha256HexBytes], uint32(len(data)))
	copy(frame[8+sha256HexBytes:], data)
	return frame
}

const sha256HexBytes = 64

func scanFrames(file *os.File, key string, expected []byte, limits Limits) (bool, int64, int, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false, 0, 0, err
	}
	reader := bufio.NewReader(io.LimitReader(file, limits.terminalChannelBytes+1))
	var offset int64
	var records int
	found := false
	seen := make(map[string]string)
	for {
		header := make([]byte, 4+sha256HexBytes+4)
		_, err := io.ReadFull(reader, header)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return false, offset, records, errors.New("durable channel contains a partial frame")
		}
		keyLen := binary.BigEndian.Uint32(header[:4])
		dataLen := binary.BigEndian.Uint32(header[4+sha256HexBytes:])
		frameKey := string(header[4 : 4+sha256HexBytes])
		if keyLen != sha256HexBytes || !validDigest(frameKey) || dataLen == 0 || int(dataLen) > limits.terminalRecordBytes {
			return false, offset, records, errors.New("durable channel contains an invalid frame")
		}
		payload := make([]byte, int(dataLen))
		if _, err := io.ReadFull(reader, payload); err != nil {
			return false, offset, records, errors.New("durable channel contains a partial payload")
		}
		records++
		if records > limits.terminalChannelRecords {
			return false, offset, records, errors.New("durable channel record bound exceeded")
		}
		payloadDigest := digest(payload)
		if prior, ok := seen[frameKey]; ok && prior != payloadDigest {
			return false, offset, records, errors.New("durable channel key conflicts")
		}
		seen[frameKey] = payloadDigest
		if frameKey == key {
			if !bytes.Equal(payload, expected) {
				return false, offset, records, errors.New("durable channel replay conflicts")
			}
			found = true
		}
		offset += int64(len(header) + len(payload))
	}
	if offset > limits.terminalChannelBytes {
		return false, offset, records, errors.New("durable channel byte bound exceeded")
	}
	return found, offset, records, nil
}

func openNoFollow(path string, directory bool, permission os.FileMode) (*os.File, error) {
	flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
	if directory {
		flags |= syscall.O_DIRECTORY
	}
	fd, err := syscall.Open(path, flags, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || directory != info.IsDir() || info.Mode().Perm() != permission || !ownedByEffectiveUser(info) || !directory && !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("opened merge path has unsafe type or permissions")
	}
	if !directory {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 {
			file.Close()
			return nil, errors.New("opened merge file must not be hard linked")
		}
	}
	return file, nil
}

func openOrCreateRegular(path string, permission os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, uint32(permission))
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != permission || !ownedByEffectiveUser(info) {
		file.Close()
		return nil, errors.New("merge file has unsafe type or permissions")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		file.Close()
		return nil, errors.New("merge file must not be hard linked")
	}
	return file, nil
}

func verifyDirectory(path string, permission os.FileMode) error {
	file, err := openNoFollow(path, true, permission)
	if err == nil {
		err = file.Close()
	}
	return err
}

func ownedByEffectiveUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func readSafeRegular(path string, max int) ([]byte, error) {
	file, err := openNoFollow(path, false, 0o600)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := io.LimitReader(file, int64(max)+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if len(data) > max {
		return nil, errors.New("merge record exceeds its bound")
	}
	return data, nil
}

func syncPathDirectory(path string, sync func(*os.File) error) error {
	dir, err := openNoFollow(path, true, 0o700)
	if err != nil {
		return err
	}
	defer dir.Close()
	return sync(dir)
}

func writeFullStore(file *os.File, data []byte) error {
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

func renameNoReplace(oldPath, newPath string) error {
	oldPointer, err := syscall.BytePtrFromString(oldPath)
	if err != nil {
		return err
	}
	newPointer, err := syscall.BytePtrFromString(newPath)
	if err != nil {
		return err
	}
	number := renameat2SyscallNumber()
	if number == 0 {
		return syscall.ENOSYS
	}
	_, _, errno := syscall.Syscall6(number, ^uintptr(99), uintptr(unsafe.Pointer(oldPointer)),
		^uintptr(99), uintptr(unsafe.Pointer(newPointer)), 1, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func renameat2SyscallNumber() uintptr {
	switch runtime.GOARCH {
	case "amd64":
		return 316
	case "386":
		return 353
	case "arm":
		return 382
	case "arm64", "loong64", "riscv64":
		return 276
	case "mips", "mipsle":
		return 4351
	case "mips64", "mips64le":
		return 5311
	case "ppc64", "ppc64le":
		return 357
	case "s390x":
		return 347
	default:
		return 0
	}
}

func flock(file *os.File, nonblocking bool) error {
	flags := syscall.LOCK_EX
	if nonblocking {
		flags |= syscall.LOCK_NB
	}
	return syscall.Flock(int(file.Fd()), flags)
}

func funlock(file *os.File) error { return syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }

func marshalCanonical(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return nil, err
	}
	return data, nil
}

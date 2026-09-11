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
	"sort"
	"strings"
	"syscall"

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
	"reconciliations":           {},
	"terminal-intents":          {},
	"final-terminals":           {},
	"cleanup-incidents":         {},
}

type durableStore struct {
	root     string
	limits   Limits
	rootDir  *os.File
	lock     *os.File
	syncFile func(*os.File) error
	syncDir  func(*os.File) error
	remove   func(string) error
}

type attemptStore struct {
	store     *durableStore
	id        string
	root      string
	records   string
	temporary string
	lock      *os.File
	closed    bool
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
	Counters  Counters `json:"counters"`
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
	store := &durableStore{root: root, limits: limits, rootDir: rootDir,
		syncFile: func(f *os.File) error { return f.Sync() }, syncDir: func(f *os.File) error { return f.Sync() }, remove: os.Remove}
	for _, name := range []string{"attempts", "channels"} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			store.close()
			return nil, err
		}
		if err := verifyDirectory(path, 0o700); err != nil {
			store.close()
			return nil, err
		}
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
	if err := store.inventoryGlobal(); err != nil {
		store.close()
		return nil, err
	}
	return store, nil
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
	attempt := &attemptStore{s, id, root, filepath.Join(root, "records"), filepath.Join(root, "tmp"), lock, false}
	if _, err := attempt.inventory(); err != nil {
		attempt.close()
		return nil, err
	}
	return attempt, nil
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
		if err := os.Rename(temporaryPath, destination); err != nil {
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
	if err := os.Rename(temporaryPath, destination); err != nil {
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

func (a *attemptStore) read(name string) ([]byte, bool, error) {
	bound, ok := allowedAttemptRecords[name]
	if a == nil || a.closed || !ok {
		return nil, false, errors.New("attempt record name is invalid")
	}
	data, err := readSafeRegular(filepath.Join(a.records, name), bound)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func (a *attemptStore) cleanupTemporary() error {
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
	last, ok := nextCounters(last, category)
	if !ok {
		return Counters{}, errors.New("unknown provider counter category")
	}
	if err := last.validate(a.store.limits); err != nil {
		return Counters{}, err
	}
	record, err := json.Marshal(counterReservation{a.id, last})
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

func (a *attemptStore) currentCounters() (Counters, error) {
	if a == nil || a.closed {
		return Counters{}, errors.New("attempt store is closed")
	}
	path := filepath.Join(a.root, "counters.jsonl")
	file, err := openOrCreateRegular(path, 0o600)
	if err != nil {
		return Counters{}, err
	}
	defer file.Close()
	return readCounters(file, a.id, a.store.limits)
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
		if err := strictCanonical(scanner.Bytes(), &record); err != nil || record.AttemptID != attemptID || !validCounterStep(result, record.Counters) {
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
	next.TotalProviderCalls++
	switch category {
	case "pre-submit":
		next.PreSubmitCalls++
	case "commit-submission":
		next.PreSubmitCalls++
		next.CommitSubmissions++
	case "target-submission":
		next.TargetSubmissions++
	case "post-merge":
		next.PostMergeCalls++
	case "reconciliation-round":
		next.ReconciliationRounds++
		next.TotalProviderCalls--
	case "reconciliation-call":
		next.ReconciliationCalls++
	default:
		return Counters{}, false
	}
	return next, true
}

func validCounterStep(previous, observed Counters) bool {
	for _, category := range []string{"pre-submit", "commit-submission", "target-submission", "post-merge", "reconciliation-round", "reconciliation-call"} {
		if expected, _ := nextCounters(previous, category); expected == observed {
			return true
		}
	}
	return false
}

func (a *attemptStore) inventory() (inventory, error) {
	var inv inventory
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
	rootEntries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	for _, entry := range rootEntries {
		if entry.Name() != "attempts" && entry.Name() != "channels" && entry.Name() != "store.lock" {
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
				info, err := os.Lstat(filepath.Join(s.root, "attempts", entry.Name(), child, item.Name()))
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
				}
			}
		}
	}
	channelEntries, err := os.ReadDir(filepath.Join(s.root, "channels"))
	if err != nil {
		return err
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
	if files > s.limits.publishedFilesTotal+s.limits.activeTemporaryFiles || bytesTotal > s.limits.publishedBytesTotal+s.limits.terminalChannelBytes || temporary > s.limits.activeTemporaryFiles {
		return errors.New("global merge storage budget exhausted")
	}
	return nil
}

func (s *durableStore) appendChannel(channel, kind, key string, data []byte, maxRecord int) (ledger.EvidenceRef, error) {
	_, allowed := allowedChannels[channel]
	if s == nil || !allowed || !validDigest(key) || len(data) == 0 || len(data) > maxRecord || len(data) > s.limits.terminalRecordBytes || !json.Valid(data) {
		return ledger.EvidenceRef{}, errors.New("channel record identity or size is invalid")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil || !bytes.Equal(compact.Bytes(), data) {
		return ledger.EvidenceRef{}, errors.New("channel record JSON is not compact")
	}
	if err := flock(s.lock, false); err != nil {
		return ledger.EvidenceRef{}, err
	}
	defer funlock(s.lock)
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

func (s *durableStore) readChannel(channel, key string, maxRecord int) ([]byte, bool, error) {
	_, allowed := allowedChannels[channel]
	if s == nil || !allowed || !validDigest(key) || maxRecord <= 0 || maxRecord > s.limits.terminalRecordBytes {
		return nil, false, errors.New("channel read identity or bound is invalid")
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
	flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	if directory {
		flags |= syscall.O_DIRECTORY
	}
	fd, err := syscall.Open(path, flags, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || directory != info.IsDir() || info.Mode().Perm() != permission || !ownedByEffectiveUser(info) {
		file.Close()
		return nil, errors.New("opened merge path has unsafe type or permissions")
	}
	return file, nil
}

func openOrCreateRegular(path string, permission os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, uint32(permission))
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

//go:build linux

package cilifecycle

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"
)

var attemptReservationName = regexp.MustCompile(`^ci-([0-9a-f]{64})\.reservation$`)

type attemptLimits struct {
	maxPerRun int
	maxGlobal int
	maxBytes  int64
}

func productionAttemptLimits() attemptLimits {
	return attemptLimits{MaxAttemptsPerRun, MaxAttemptsGlobal, MaxReservedEvidenceBytes}
}

func (l attemptLimits) valid() bool {
	return l.maxPerRun > 0 && l.maxGlobal > 0 && l.maxPerRun <= MaxAttemptsPerRun &&
		l.maxGlobal <= MaxAttemptsGlobal && l.maxBytes > 0 && l.maxBytes <= MaxReservedEvidenceBytes
}

type ciFileID struct {
	device uint64
	inode  uint64
}

type attemptStore struct {
	root       string
	rootDir    *os.File
	rootID     ciFileID
	capacityID ciFileID
	limits     attemptLimits
	syncFile   func(*os.File) error
	syncDir    func(*os.File) error
}

type attemptLease struct {
	store       *attemptStore
	file        *os.File
	fileID      ciFileID
	name        string
	expected    []byte
	local       *sync.Mutex
	capacity    *os.File
	created     bool
	needsRepair bool
	closed      bool
}

type attemptInventory struct {
	total      int
	incomplete int
	byRun      map[string]int
}

var processAttemptLocks sync.Map

func newAttemptStore(root string, limits attemptLimits) (*attemptStore, error) {
	if !limits.valid() || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("CI attempt root and limits are invalid")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return nil, errors.New("CI attempt root must pre-exist without symlink components")
	}
	rootDir, rootID, err := openCIPath(root, syscall.O_RDONLY|syscall.O_DIRECTORY, true, 0o700)
	if err != nil {
		return nil, fmt.Errorf("pin CI attempt root: %w", err)
	}
	capacity, capacityID, err := openCIAt(rootDir, "capacity.lock", syscall.O_RDWR|syscall.O_NONBLOCK, false, 0o600)
	if err != nil {
		_ = rootDir.Close()
		return nil, fmt.Errorf("open administrator-provisioned capacity lock: %w", err)
	}
	_ = capacity.Close()
	store := &attemptStore{
		root: root, rootDir: rootDir, rootID: rootID, capacityID: capacityID, limits: limits,
		syncFile: func(file *os.File) error { return file.Sync() },
		syncDir:  func(file *os.File) error { return file.Sync() },
	}
	if err := store.checkRoot(); err != nil {
		_ = rootDir.Close()
		return nil, err
	}
	return store, nil
}

func (s *attemptStore) close() error {
	if s == nil || s.rootDir == nil {
		return nil
	}
	err := s.rootDir.Close()
	s.rootDir = nil
	return err
}

func (s *attemptStore) acquire(reservation attemptReservationV1) (*attemptLease, error) {
	if s == nil || s.rootDir == nil {
		return nil, errors.New("CI attempt allocator is closed")
	}
	expected, err := reservation.canonicalJSON()
	if err != nil || len(expected) > MaxReservationBytes {
		return nil, errors.New("CI attempt reservation exceeds its bound")
	}
	name := attemptReservationFilename(reservation.AttemptKeySHA256)
	lockKey := fmt.Sprintf("%d:%d\x00%s", s.rootID.device, s.rootID.inode, name)
	value, _ := processAttemptLocks.LoadOrStore(lockKey, &sync.Mutex{})
	local := value.(*sync.Mutex)
	if !local.TryLock() {
		return nil, errors.New("CI attempt is busy in this process")
	}
	fail := func(err error) (*attemptLease, error) {
		local.Unlock()
		return nil, err
	}

	capacity, err := s.lockCapacity()
	if err != nil {
		return fail(err)
	}
	releaseCapacity := func() {
		unlockCIFile(capacity)
		capacity = nil
	}
	inv, err := s.inventory()
	if err != nil {
		releaseCapacity()
		return fail(err)
	}
	if inv.total > s.limits.maxGlobal || int64(inv.total)*MaxCompletedAttemptBytes > s.limits.maxBytes {
		releaseCapacity()
		return fail(errors.New("CI attempt capacity is already exceeded"))
	}

	file, fileID, openErr := openCIAt(s.rootDir, name, syscall.O_RDWR|syscall.O_NONBLOCK, false, 0o600)
	if openErr == nil {
		if err := lockCIFile(file, 0); err != nil {
			_ = file.Close()
			releaseCapacity()
			return fail(fmt.Errorf("acquire existing attempt lock: %w", err))
		}
		lease := &attemptLease{store: s, file: file, fileID: fileID, name: name, expected: expected, local: local, capacity: capacity}
		if err := lease.verifyNamedFile(); err != nil {
			lease.close()
			return nil, err
		}
		data, err := readBoundedFile(file, MaxReservationBytes)
		if err != nil {
			lease.close()
			return nil, err
		}
		switch {
		case bytes.Equal(data, expected):
			lease.releaseCapacity()
			return lease, nil
		case len(data) < len(expected) && bytes.Equal(data, expected[:len(data)]):
			lease.needsRepair = true
			return lease, nil
		default:
			lease.close()
			return nil, errors.New("existing CI attempt reservation conflicts")
		}
	}
	if !errors.Is(openErr, syscall.ENOENT) {
		releaseCapacity()
		return fail(openErr)
	}
	projectedRun := inv.byRun[reservation.RunID] + inv.incomplete + 1
	projectedGlobal := inv.total + 1
	projectedBytes := int64(projectedGlobal) * MaxCompletedAttemptBytes
	if projectedRun > s.limits.maxPerRun || projectedGlobal > s.limits.maxGlobal || projectedBytes > s.limits.maxBytes {
		releaseCapacity()
		return fail(errors.New("CI attempt capacity exhausted"))
	}
	var statfs syscall.Statfs_t
	if err := syscall.Fstatfs(int(s.rootDir.Fd()), &statfs); err != nil ||
		int64(statfs.Bavail)*int64(statfs.Bsize) < MaxCompletedAttemptBytes {
		releaseCapacity()
		return fail(errors.New("insufficient physical space for CI attempt reservation"))
	}

	fd, err := syscall.Openat(int(s.rootDir.Fd()), name,
		syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		releaseCapacity()
		return fail(err)
	}
	file = os.NewFile(uintptr(fd), filepath.Join(s.root, name))
	fileID, err = verifyCIFile(file, false, 0o600)
	if err != nil {
		_ = file.Close()
		releaseCapacity()
		return fail(err)
	}
	if err := lockCIFile(file, 0); err != nil {
		_ = file.Close()
		releaseCapacity()
		return fail(err)
	}
	lease := &attemptLease{store: s, file: file, fileID: fileID, name: name, expected: expected, local: local, capacity: capacity, created: true}
	if err := writeFullCI(file, expected); err != nil {
		lease.close()
		return nil, err
	}
	if err := s.syncFile(file); err != nil {
		lease.close()
		return nil, fmt.Errorf("sync CI attempt reservation: %w", err)
	}
	if err := s.syncDir(s.rootDir); err != nil {
		lease.close()
		return nil, fmt.Errorf("sync CI attempt root: %w", err)
	}
	if err := lease.verifyNamedFile(); err != nil {
		lease.close()
		return nil, err
	}
	lease.releaseCapacity()
	return lease, nil
}

func (l *attemptLease) repair() error {
	if l == nil || l.closed || !l.needsRepair || l.capacity == nil {
		return errors.New("CI attempt reservation is not recoverable")
	}
	if err := l.verifyNamedFile(); err != nil {
		return err
	}
	if err := l.file.Truncate(0); err != nil {
		return err
	}
	if _, err := l.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := writeFullCI(l.file, l.expected); err != nil {
		return err
	}
	if err := l.store.syncFile(l.file); err != nil {
		return err
	}
	if err := l.store.syncDir(l.store.rootDir); err != nil {
		return err
	}
	data, err := readBoundedFile(l.file, MaxReservationBytes)
	if err != nil || !bytes.Equal(data, l.expected) {
		return errors.New("recovered CI attempt reservation did not verify")
	}
	l.needsRepair = false
	l.releaseCapacity()
	return nil
}

func (l *attemptLease) verifyNamedFile() error {
	resolved, id, err := openCIAt(l.store.rootDir, l.name, syscall.O_RDWR|syscall.O_NONBLOCK, false, 0o600)
	if err != nil {
		return err
	}
	_ = resolved.Close()
	if id != l.fileID {
		return errors.New("CI attempt reservation was replaced")
	}
	return l.store.checkRoot()
}

func (l *attemptLease) releaseCapacity() {
	if l != nil && l.capacity != nil {
		unlockCIFile(l.capacity)
		l.capacity = nil
	}
}

func (l *attemptLease) close() error {
	if l == nil || l.closed {
		return nil
	}
	l.closed = true
	l.releaseCapacity()
	var unlockErr, closeErr error
	if l.file != nil {
		unlockErr = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		closeErr = l.file.Close()
	}
	if l.local != nil {
		l.local.Unlock()
	}
	return errors.Join(unlockErr, closeErr)
}

func (s *attemptStore) inventory() (attemptInventory, error) {
	result := attemptInventory{byRun: make(map[string]int)}
	directoryFD, err := syscall.Openat(int(s.rootDir.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return result, err
	}
	directory := os.NewFile(uintptr(directoryFD), s.root)
	entries, err := directory.ReadDir(-1)
	_ = directory.Close()
	if err != nil {
		return result, err
	}
	seenCapacity := false
	for _, entry := range entries {
		name := entry.Name()
		if name == "capacity.lock" {
			file, id, err := openCIAt(s.rootDir, name, syscall.O_RDWR|syscall.O_NONBLOCK, false, 0o600)
			if err != nil || id != s.capacityID {
				if file != nil {
					_ = file.Close()
				}
				return result, errors.New("unsafe CI capacity lock")
			}
			_ = file.Close()
			seenCapacity = true
			continue
		}
		match := attemptReservationName.FindStringSubmatch(name)
		if match == nil {
			return result, fmt.Errorf("unknown CI attempt-root entry %q", name)
		}
		file, _, err := openCIAt(s.rootDir, name, syscall.O_RDONLY|syscall.O_NONBLOCK, false, 0o600)
		if err != nil {
			return result, fmt.Errorf("inspect CI attempt reservation: %w", err)
		}
		data, readErr := readBoundedFile(file, MaxReservationBytes)
		_ = file.Close()
		if readErr != nil {
			return result, readErr
		}
		result.total++
		reservation, parseErr := parseAttemptReservation(data)
		if parseErr != nil || reservation.AttemptKeySHA256 != match[1] {
			result.incomplete++
			continue
		}
		result.byRun[reservation.RunID]++
	}
	if !seenCapacity {
		return result, errors.New("CI capacity lock disappeared")
	}
	return result, nil
}

func (s *attemptStore) lockCapacity() (*os.File, error) {
	if err := s.checkRoot(); err != nil {
		return nil, err
	}
	file, id, err := openCIAt(s.rootDir, "capacity.lock", syscall.O_RDWR|syscall.O_NONBLOCK, false, 0o600)
	if err != nil {
		return nil, err
	}
	if id != s.capacityID {
		_ = file.Close()
		return nil, errors.New("CI capacity lock identity changed")
	}
	if err := lockCIFile(file, 2*time.Second); err != nil {
		_ = file.Close()
		return nil, err
	}
	resolved, resolvedID, err := openCIAt(s.rootDir, "capacity.lock", syscall.O_RDWR|syscall.O_NONBLOCK, false, 0o600)
	if err != nil {
		unlockCIFile(file)
		return nil, err
	}
	_ = resolved.Close()
	if resolvedID != id {
		unlockCIFile(file)
		return nil, errors.New("CI capacity lock was replaced during acquisition")
	}
	return file, nil
}

func (s *attemptStore) checkRoot() error {
	if s == nil || s.rootDir == nil {
		return errors.New("CI attempt root is not pinned")
	}
	current, id, err := openCIPath(s.root, syscall.O_RDONLY|syscall.O_DIRECTORY, true, 0o700)
	if err != nil {
		return err
	}
	_ = current.Close()
	if id != s.rootID {
		return errors.New("CI attempt root identity changed")
	}
	return nil
}

func attemptReservationFilename(attemptKey string) string { return "ci-" + attemptKey + ".reservation" }

func openCIPath(path string, flags int, directory bool, permissions os.FileMode) (*os.File, ciFileID, error) {
	if !directory || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return nil, ciFileID{}, errors.New("CI directory path must be absolute, clean, and non-root")
	}
	parts := bytes.Split([]byte(path[1:]), []byte{filepath.Separator})
	fd, err := syscall.Open(string(filepath.Separator), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ciFileID{}, err
	}
	for index, raw := range parts {
		part := string(raw)
		if part == "" || part == "." || part == ".." {
			_ = syscall.Close(fd)
			return nil, ciFileID{}, errors.New("unsafe CI directory path component")
		}
		nextFlags := syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
		if index == len(parts)-1 {
			nextFlags = flags | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
		}
		next, openErr := syscall.Openat(fd, part, nextFlags, 0)
		closeErr := syscall.Close(fd)
		if openErr != nil {
			return nil, ciFileID{}, errors.Join(openErr, closeErr)
		}
		if closeErr != nil {
			_ = syscall.Close(next)
			return nil, ciFileID{}, closeErr
		}
		fd = next
	}
	file := os.NewFile(uintptr(fd), path)
	id, err := verifyCIFile(file, true, permissions)
	if err != nil {
		_ = file.Close()
		return nil, ciFileID{}, err
	}
	return file, id, nil
}

func openCIAt(parent *os.File, name string, flags int, directory bool, permissions os.FileMode) (*os.File, ciFileID, error) {
	if parent == nil || filepath.Base(name) != name || name == "." || name == ".." {
		return nil, ciFileID{}, errors.New("unsafe descriptor-relative CI filename")
	}
	fd, err := syscall.Openat(int(parent.Fd()), name, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ciFileID{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	id, err := verifyCIFile(file, directory, permissions)
	if err != nil {
		_ = file.Close()
		return nil, ciFileID{}, err
	}
	return file, id, nil
}

func verifyCIFile(file *os.File, directory bool, permissions os.FileMode) (ciFileID, error) {
	var stat syscall.Stat_t
	if file == nil || syscall.Fstat(int(file.Fd()), &stat) != nil {
		return ciFileID{}, errors.New("cannot inspect CI filesystem object")
	}
	wantType := uint32(syscall.S_IFREG)
	if directory {
		wantType = syscall.S_IFDIR
	}
	if stat.Mode&syscall.S_IFMT != wantType || os.FileMode(stat.Mode).Perm() != permissions || int(stat.Uid) != os.Geteuid() || (!directory && stat.Nlink != 1) {
		return ciFileID{}, errors.New("CI filesystem object has unsafe type, mode, owner, or links")
	}
	return ciFileID{uint64(stat.Dev), stat.Ino}, nil
}

func readBoundedFile(file *os.File, maximum int) ([]byte, error) {
	before, err := artifactFileSnapshot(file)
	if err != nil || before.size < 0 || before.size > int64(maximum) {
		return nil, errors.New("CI filesystem record has an invalid size")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maximum {
		return nil, errors.New("CI filesystem record exceeds its size bound")
	}
	after, err := artifactFileSnapshot(file)
	if err != nil || before != after || int64(len(data)) != before.size {
		return nil, errors.New("CI filesystem record changed while being read")
	}
	return data, nil
}

func writeFullCI(file *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := file.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func lockCIFile(file *os.File, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		if wait <= 0 || time.Now().After(deadline) {
			return errors.New("CI filesystem lock is busy")
		}
		time.Sleep(time.Millisecond)
	}
}

func unlockCIFile(file *os.File) {
	if file == nil {
		return
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

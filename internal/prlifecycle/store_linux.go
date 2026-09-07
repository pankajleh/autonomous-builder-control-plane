//go:build linux

package prlifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"syscall"
)

var admissionName = regexp.MustCompile(`^r-([0-9a-f]{64})(?:\.lock|-resource\.json|-rev-([1-9][0-9]*)-(?:revision|generation|submitted|terminal|superseded|prepare-[1-3]|resume-[1-4]|reconcile-[1-8]-(?:start|observation))\.json)$`)

type fileIdentity struct{ dev, ino uint64 }

type PRWriteAdmissionStore struct {
	root       string
	rootDir    *os.File
	rootID     fileIdentity
	capacityID fileIdentity
	policy     PRAdmissionPolicyV1
	identityMu sync.Mutex
	lockIDs    map[string]fileIdentity
	// afterResourceLockOpen is an unexported deterministic race hook used only
	// by same-package adversarial tests.
	afterResourceLockOpen func()
}

var processResourceLocks sync.Map // canonical-root + NUL + resource-key -> *sync.Mutex

// newPRWriteAdmissionStore is the package-private test boundary. Production
// construction is performed only by NewProductionController from the
// process-startup host binding in production_linux.go.
func newPRWriteAdmissionStore(root string) (*PRWriteAdmissionStore, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("admission root must be an absolute controller-configured path")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return nil, errors.New("admission root must pre-exist as a non-symlink mode-0700 directory")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != filepath.Clean(root) {
		return nil, errors.New("admission root must be canonical and contain no symlink component")
	}
	rootFile, rootID, err := openVerified(root, syscall.O_RDONLY|syscall.O_DIRECTORY, 0, true, 0o700)
	if err != nil {
		return nil, fmt.Errorf("open admission root: %w", err)
	}
	capacity, capacityID, err := openatVerified(rootFile, "capacity.lock", syscall.O_RDWR, 0, false, 0o600)
	if err != nil {
		_ = rootFile.Close()
		return nil, fmt.Errorf("open administrator-provisioned capacity lock: %w", err)
	}
	_ = capacity.Close()
	store := &PRWriteAdmissionStore{root: root, rootDir: rootFile, rootID: rootID, capacityID: capacityID, policy: DefaultAdmissionPolicy(), lockIDs: make(map[string]fileIdentity)}
	if err := store.checkRoot(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *PRWriteAdmissionStore) Root() string { return s.root }

func (s *PRWriteAdmissionStore) checkRoot() error {
	if s == nil || s.rootDir == nil {
		return errors.New(CodeIntegrityFailure + ": admission root is not pinned")
	}
	var pinned syscall.Stat_t
	if err := syscall.Fstat(int(s.rootDir.Fd()), &pinned); err != nil || (fileIdentity{uint64(pinned.Dev), pinned.Ino}) != s.rootID {
		return errors.New(CodeIntegrityFailure + ": pinned admission root identity changed")
	}
	f, id, err := openVerified(s.root, syscall.O_RDONLY|syscall.O_DIRECTORY, 0, true, 0o700)
	if err != nil {
		return err
	}
	defer f.Close()
	if id != s.rootID {
		return errors.New(CodeIntegrityFailure + ": admission root identity changed")
	}
	return nil
}

func (s *PRWriteAdmissionStore) ensureResourceLock(key PRResourceKeyV1) error {
	if !key.valid() {
		return errors.New("invalid resource key")
	}
	if err := s.checkRoot(); err != nil {
		return err
	}
	name := "r-" + key.String() + ".lock"
	if f, id, openErr := openatVerified(s.rootDir, name, syscall.O_RDWR, 0, false, 0o600); openErr == nil {
		if f != nil {
			_ = f.Close()
		}
		return s.rememberResourceLock(key, id)
	} else if !errors.Is(openErr, syscall.ENOENT) {
		return openErr
	}
	capacity, err := s.lockCapacity()
	if err != nil {
		return err
	}
	defer unlockClose(capacity)
	if f, id, openErr := openatVerified(s.rootDir, name, syscall.O_RDWR, 0, false, 0o600); openErr == nil {
		_ = f.Close()
		return s.rememberResourceLock(key, id)
	} else if !errors.Is(openErr, syscall.ENOENT) {
		return openErr
	}
	inv, err := s.inventory()
	if err != nil {
		return err
	}
	if inv.resources >= s.policy.MaxAdmissionResources {
		return &Error{Code: CodeCapacityExhausted, Cause: errors.New("admission resource capacity exceeded")}
	}
	if err := s.checkProjection(int64(1), 0, false, false); err != nil {
		return err
	}
	fd, err := syscall.Openat(int(s.rootDir.Fd()), name, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(s.root, name))
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := s.rootDir.Sync(); err != nil {
		return err
	}
	verified, id, err := openatVerified(s.rootDir, name, syscall.O_RDWR, 0, false, 0o600)
	if err != nil {
		return err
	}
	_ = verified.Close()
	return s.rememberResourceLock(key, id)
}

func (s *PRWriteAdmissionStore) rememberResourceLock(key PRResourceKeyV1, id fileIdentity) error {
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	if expected, ok := s.lockIDs[key.String()]; ok && expected != id {
		return errors.New(CodeIntegrityFailure + ": resource lock inode changed")
	}
	s.lockIDs[key.String()] = id
	return nil
}

type resourceTxn struct {
	store *PRWriteAdmissionStore
	key   PRResourceKeyV1
}

func (s *PRWriteAdmissionStore) withResource(key PRResourceKeyV1, operation func(*resourceTxn) error) error {
	if operation == nil {
		return errors.New("resource operation is required")
	}
	if err := s.ensureResourceLock(key); err != nil {
		return err
	}
	value, _ := processResourceLocks.LoadOrStore(fmt.Sprintf("%d:%d\x00%s", s.rootID.dev, s.rootID.ino, key.String()), &sync.Mutex{})
	local := value.(*sync.Mutex)
	local.Lock()
	defer local.Unlock()
	lockName := "r-" + key.String() + ".lock"
	lock, lockedID, err := openatVerified(s.rootDir, lockName, syscall.O_RDWR, 0, false, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if s.afterResourceLockOpen != nil {
		s.afterResourceLockOpen()
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return errors.New("PR resource is locked by another process")
		}
		return fmt.Errorf("acquire resource flock: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	s.identityMu.Lock()
	expectedID, remembered := s.lockIDs[key.String()]
	s.identityMu.Unlock()
	if !remembered || lockedID != expectedID {
		return errors.New(CodeIntegrityFailure + ": acquired resource lock differs from remembered inode")
	}
	// Re-resolve the name after flock. A replacement between discovery/open
	// and lock acquisition must never split serialization across two inodes.
	resolved, resolvedID, err := openatVerified(s.rootDir, lockName, syscall.O_RDWR, 0, false, 0o600)
	if err != nil {
		return err
	}
	_ = resolved.Close()
	if resolvedID != lockedID {
		return errors.New(CodeIntegrityFailure + ": resource lock was replaced during acquisition")
	}
	if err := s.checkRoot(); err != nil {
		return err
	}
	return operation(&resourceTxn{store: s, key: key})
}

func (s *PRWriteAdmissionStore) lockCapacity() (*os.File, error) {
	f, id, err := openatVerified(s.rootDir, "capacity.lock", syscall.O_RDWR, 0, false, 0o600)
	if err != nil {
		return nil, err
	}
	if id != s.capacityID {
		_ = f.Close()
		return nil, errors.New(CodeIntegrityFailure + ": capacity lock identity changed")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("acquire capacity flock: %w", err)
	}
	return f, nil
}

func unlockClose(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

func (t *resourceTxn) createJSON(name string, value any, terminalReservation, consumesReservation bool) ([]byte, string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	return t.create(name, data, terminalReservation, consumesReservation)
}

func (t *resourceTxn) create(name string, data []byte, terminalReservation, consumesReservation bool) ([]byte, string, error) {
	if !admissionName.MatchString(name) || !bytes.HasPrefix([]byte(name), []byte("r-"+t.key.String())) {
		return nil, "", errors.New("invalid admission record name")
	}
	capacity, err := t.store.lockCapacity() // resource -> capacity is the only nested order.
	if err != nil {
		return nil, "", err
	}
	defer unlockClose(capacity)
	if err := t.store.checkProjection(1, int64(len(data)), terminalReservation, consumesReservation); err != nil {
		return nil, "", err
	}
	fd, err := syscall.Openat(int(t.store.rootDir.Fd()), name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		if errors.Is(err, syscall.EEXIST) {
			existing, readErr := t.read(name, MaxTerminalBytes)
			if readErr == nil && bytes.Equal(existing, data) {
				return existing, digestBytes(existing), nil
			}
			return nil, "", errors.New(CodeIntegrityFailure + ": immutable record conflict")
		}
		return nil, "", err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(t.store.root, name))
	if err := writeFull(f, data); err != nil {
		_ = f.Close()
		return nil, "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, "", err
	}
	if err := f.Close(); err != nil {
		return nil, "", err
	}
	if err := t.store.rootDir.Sync(); err != nil {
		return nil, "", err
	}
	return append([]byte(nil), data...), digestBytes(data), nil
}

func (t *resourceTxn) read(name string, maximum int) ([]byte, error) {
	if !admissionName.MatchString(name) || !bytes.HasPrefix([]byte(name), []byte("r-"+t.key.String())) {
		return nil, errors.New("invalid admission record name")
	}
	f, _, err := openatVerified(t.store.rootDir, name, syscall.O_RDONLY, 0, false, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(maximum)+1))
	if err != nil || len(data) > maximum {
		return nil, errors.New(CodeIntegrityFailure + ": admission record oversized or unreadable")
	}
	return data, nil
}

func (t *resourceTxn) exists(name string) (bool, error) {
	if !admissionName.MatchString(name) || !bytes.HasPrefix([]byte(name), []byte("r-"+t.key.String())) {
		return false, errors.New("invalid admission record name")
	}
	f, _, err := openatVerified(t.store.rootDir, name, syscall.O_RDONLY, 0, false, 0o600)
	if errors.Is(err, syscall.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, errors.New(CodeIntegrityFailure + ": unsafe admission record")
	}
	_ = f.Close()
	return true, nil
}

type inventory struct {
	files, bytes, resources, reservations int64
}

func (s *PRWriteAdmissionStore) checkProjection(addFiles, addBytes int64, addReservation, consumeReservation bool) error {
	inv, err := s.inventory()
	if err != nil {
		return err
	}
	if addReservation {
		inv.reservations++
	}
	if consumeReservation {
		if inv.reservations <= 0 {
			return errors.New(CodeIntegrityFailure + ": terminal has no reservation")
		}
		inv.reservations--
	}
	projectedFiles := inv.files + addFiles + inv.reservations
	projectedBytes := inv.bytes + addBytes + inv.reservations*MaxTerminalBytes
	if inv.resources > s.policy.MaxAdmissionResources || projectedFiles > s.policy.MaxAdmissionFiles || projectedBytes > s.policy.MaxAdmissionBytes {
		return &Error{Code: CodeCapacityExhausted, Cause: errors.New("admission policy capacity exceeded")}
	}
	var stat syscall.Statfs_t
	if err := syscall.Fstatfs(int(s.rootDir.Fd()), &stat); err != nil {
		return &Error{Code: CodeCapacityExhausted, Cause: err}
	}
	free := int64(stat.Bavail) * int64(stat.Bsize)
	if free < addBytes+inv.reservations*MaxTerminalBytes {
		return &Error{Code: CodeCapacityExhausted, Cause: errors.New("insufficient physical free-space headroom")}
	}
	return nil
}

func (s *PRWriteAdmissionStore) inventory() (inventory, error) {
	entries, err := s.readDir()
	if err != nil {
		return inventory{}, err
	}
	var result inventory
	markers, terminals := map[string]bool{}, map[string]bool{}
	resources := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if name == "capacity.lock" {
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
				return result, errors.New(CodeIntegrityFailure + ": unsafe capacity lock")
			}
			result.files++
			result.bytes += info.Size()
			continue
		}
		match := admissionName.FindStringSubmatch(name)
		if match == nil {
			return result, errors.New(CodeIntegrityFailure + ": unknown admission-root entry " + name)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
			return result, errors.New(CodeIntegrityFailure + ": unsafe admission-root entry")
		}
		result.files++
		result.bytes += info.Size()
		resources[match[1]] = true
		prefix := name
		if len(name) > len("-submitted.json") && name[len(name)-len("-submitted.json"):] == "-submitted.json" {
			prefix = name[:len(name)-len("-submitted.json")]
			markers[prefix] = true
		}
		if len(name) > len("-terminal.json") && name[len(name)-len("-terminal.json"):] == "-terminal.json" {
			prefix = name[:len(name)-len("-terminal.json")]
			terminals[prefix] = true
		}
	}
	result.resources = int64(len(resources))
	for prefix := range markers {
		if !terminals[prefix] {
			result.reservations++
		}
	}
	return result, nil
}

func (s *PRWriteAdmissionStore) readDir() ([]os.DirEntry, error) {
	fd, err := syscall.Openat(int(s.rootDir.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), s.root)
	defer f.Close()
	return f.ReadDir(-1)
}

func openVerified(path string, flags int, perm uint32, directory bool, expectedPerm os.FileMode) (*os.File, fileIdentity, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, perm)
	if err != nil {
		return nil, fileIdentity{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		_ = f.Close()
		return nil, fileIdentity{}, err
	}
	if directory && stat.Mode&syscall.S_IFMT != syscall.S_IFDIR || !directory && stat.Mode&syscall.S_IFMT != syscall.S_IFREG || os.FileMode(stat.Mode).Perm() != expectedPerm {
		_ = f.Close()
		return nil, fileIdentity{}, errors.New("path type or permissions are unsafe")
	}
	return f, fileIdentity{uint64(stat.Dev), stat.Ino}, nil
}

func openatVerified(parent *os.File, name string, flags int, perm uint32, directory bool, expectedPerm os.FileMode) (*os.File, fileIdentity, error) {
	if parent == nil || filepath.Base(name) != name || name == "." || name == ".." {
		return nil, fileIdentity{}, errors.New("unsafe descriptor-relative name")
	}
	fd, err := syscall.Openat(int(parent.Fd()), name, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, perm)
	if err != nil {
		return nil, fileIdentity{}, err
	}
	f := os.NewFile(uintptr(fd), name)
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		_ = f.Close()
		return nil, fileIdentity{}, err
	}
	if directory && stat.Mode&syscall.S_IFMT != syscall.S_IFDIR || !directory && stat.Mode&syscall.S_IFMT != syscall.S_IFREG || os.FileMode(stat.Mode).Perm() != expectedPerm {
		_ = f.Close()
		return nil, fileIdentity{}, errors.New("path type or permissions are unsafe")
	}
	return f, fileIdentity{uint64(stat.Dev), stat.Ino}, nil
}

func writeFull(f *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := f.Write(data)
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

func recordPrefix(key PRResourceKeyV1, revision uint64) string {
	return "r-" + key.String() + "-rev-" + strconv.FormatUint(revision, 10) + "-"
}

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

var admissionName = regexp.MustCompile(`^r-([0-9a-f]{64})(?:\.lock|-resource\.json|-rev-([1-9][0-9]*)-(?:revision|generation|submitted|terminal|superseded|prepare-[1-3]|resume-[1-4]|reconcile-[1-8])\.json)$`)

type fileIdentity struct{ dev, ino uint64 }

type PRWriteAdmissionStore struct {
	root       string
	rootID     fileIdentity
	capacityID fileIdentity
	policy     PRAdmissionPolicyV1
	identityMu sync.Mutex
	lockIDs    map[string]fileIdentity
}

var processResourceLocks sync.Map // canonical-root + NUL + resource-key -> *sync.Mutex

func NewPRWriteAdmissionStore(root string) (*PRWriteAdmissionStore, error) {
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
	_ = rootFile.Close()
	capacityPath := filepath.Join(root, "capacity.lock")
	capacity, capacityID, err := openVerified(capacityPath, syscall.O_RDWR, 0, false, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open administrator-provisioned capacity lock: %w", err)
	}
	_ = capacity.Close()
	store := &PRWriteAdmissionStore{root: root, rootID: rootID, capacityID: capacityID, policy: DefaultAdmissionPolicy(), lockIDs: make(map[string]fileIdentity)}
	if err := store.checkRoot(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *PRWriteAdmissionStore) Root() string { return s.root }

func (s *PRWriteAdmissionStore) checkRoot() error {
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
	path := filepath.Join(s.root, name)
	if _, err := os.Lstat(path); err == nil {
		f, id, openErr := openVerified(path, syscall.O_RDWR, 0, false, 0o600)
		if f != nil {
			_ = f.Close()
		}
		if openErr == nil {
			openErr = s.rememberResourceLock(key, id)
		}
		return openErr
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	capacity, err := s.lockCapacity()
	if err != nil {
		return err
	}
	defer unlockClose(capacity)
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
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
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := syncDir(s.root); err != nil {
		return err
	}
	verified, id, err := openVerified(path, syscall.O_RDWR, 0, false, 0o600)
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
	value, _ := processResourceLocks.LoadOrStore(s.root+"\x00"+key.String(), &sync.Mutex{})
	local := value.(*sync.Mutex)
	local.Lock()
	defer local.Unlock()
	lockPath := filepath.Join(s.root, "r-"+key.String()+".lock")
	lock, _, err := openVerified(lockPath, syscall.O_RDWR, 0, false, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return errors.New("PR resource is locked by another process")
		}
		return fmt.Errorf("acquire resource flock: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := s.checkRoot(); err != nil {
		return err
	}
	return operation(&resourceTxn{store: s, key: key})
}

func (s *PRWriteAdmissionStore) lockCapacity() (*os.File, error) {
	path := filepath.Join(s.root, "capacity.lock")
	f, id, err := openVerified(path, syscall.O_RDWR, 0, false, 0o600)
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
	path := filepath.Join(t.store.root, name)
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
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
	f := os.NewFile(uintptr(fd), path)
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
	if err := syncDir(t.store.root); err != nil {
		return nil, "", err
	}
	return append([]byte(nil), data...), digestBytes(data), nil
}

func (t *resourceTxn) read(name string, maximum int) ([]byte, error) {
	if !admissionName.MatchString(name) || !bytes.HasPrefix([]byte(name), []byte("r-"+t.key.String())) {
		return nil, errors.New("invalid admission record name")
	}
	path := filepath.Join(t.store.root, name)
	f, _, err := openVerified(path, syscall.O_RDONLY, 0, false, 0o600)
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
	path := filepath.Join(t.store.root, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return false, errors.New(CodeIntegrityFailure + ": unsafe admission record")
	}
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
	if err := syscall.Statfs(s.root, &stat); err != nil {
		return &Error{Code: CodeCapacityExhausted, Cause: err}
	}
	free := int64(stat.Bavail) * int64(stat.Bsize)
	if free < addBytes+inv.reservations*MaxTerminalBytes {
		return &Error{Code: CodeCapacityExhausted, Cause: errors.New("insufficient physical free-space headroom")}
	}
	return nil
}

func (s *PRWriteAdmissionStore) inventory() (inventory, error) {
	entries, err := os.ReadDir(s.root)
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

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func recordPrefix(key PRResourceKeyV1, revision uint64) string {
	return "r-" + key.String() + "-rev-" + strconv.FormatUint(revision, 10) + "-"
}

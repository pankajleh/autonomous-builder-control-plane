//go:build linux

package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	runTransitionRegistryName = ".lock-identities-v1"
	runTransitionXattrCreate  = 1
	runTransitionXattrReplace = 2
	runTransitionRegistryHead = "RTR1"
	runTransitionLockHead     = "RLI1"
	runTransitionRecordSize   = sha256.Size + 16
	runTransitionLockSize     = len(runTransitionLockHead) + runTransitionRecordSize
)

type runTransitionAuthority struct {
	state        byte
	parentDev    uint64
	parentIno    uint64
	ledgerDev    uint64
	ledgerIno    uint64
	directoryDev uint64
	directoryIno uint64
	registryDev  uint64
	registryIno  uint64
}

type runTransitionIdentity struct {
	digest [sha256.Size]byte
	dev    uint64
	ino    uint64
}

func closeRunTransitionNamespace(namespace *runTransitionNamespace) error {
	if namespace == nil {
		return nil
	}
	var registryErr, directoryErr error
	if namespace.registry != nil {
		registryErr = namespace.registry.Close()
		namespace.registry = nil
	}
	if namespace.directory != nil {
		directoryErr = namespace.directory.Close()
		namespace.directory = nil
	}
	return errors.Join(registryErr, directoryErr)
}

func (l *JSONLLedger) acquireRunTransitionLeaseLocked(runID string) (*RunTransitionLease, error) {
	namespace, err := l.ensureRunTransitionNamespaceLocked()
	if err != nil {
		return nil, fmt.Errorf("prepare run-transition namespace: %w", err)
	}
	if err := lockLedgerFile(namespace.registry, ledgerFileLockWait); err != nil {
		return nil, fmt.Errorf("lock run-transition identity registry: %w", err)
	}
	registryLocked := true
	unlockRegistry := func() error {
		if !registryLocked {
			return nil
		}
		registryLocked = false
		return unlockLedgerFile(namespace.registry)
	}
	fail := func(file *os.File, cause error) (*RunTransitionLease, error) {
		if file != nil {
			cause = errors.Join(cause, file.Close())
		}
		return nil, errors.Join(cause, unlockRegistry())
	}
	if err := l.verifyRunTransitionNamespaceLocked(namespace); err != nil {
		return fail(nil, fmt.Errorf("verify run-transition namespace before identity selection: %w", err))
	}
	identities, err := readRunTransitionRegistry(namespace.registry)
	if err != nil {
		return fail(nil, fmt.Errorf("read run-transition identity registry: %w", err))
	}
	digest := sha256.Sum256([]byte(runID))
	identity, found := identities[digest]
	name := hex.EncodeToString(digest[:]) + ".lock"
	file, observed, err := openRunTransitionLock(namespace, name, digest, identity, found)
	if err != nil {
		return fail(file, err)
	}
	if !found {
		if err := appendRunTransitionIdentity(namespace.registry, observed); err != nil {
			return fail(file, err)
		}
		if err := namespace.directory.Sync(); err != nil {
			return fail(file, fmt.Errorf("%w: sync run-transition namespace", errRunTransitionIntegrity))
		}
		identity = observed
	}
	if err := verifyRunTransitionLock(namespace, name, file, identity); err != nil {
		return fail(file, err)
	}
	if err := l.verifyRunTransitionNamespaceLocked(namespace); err != nil {
		return fail(file, err)
	}
	if err := unlockRegistry(); err != nil {
		return fail(file, err)
	}
	lease := &RunTransitionLease{ledger: l, runID: runID, file: file, dev: identity.dev, ino: identity.ino, digest: digest}
	return lease, nil
}

// verifyRunTransitionLeaseLocked is called with the ledger mutex held and the
// exact per-run file flock still owned. The registry lock makes its complete
// append-only identity image stable while it is revalidated.
func (l *JSONLLedger) verifyRunTransitionLeaseLocked(lease *RunTransitionLease) (resultErr error) {
	if lease == nil || lease.file == nil || lease.ledger != l || l.runLocks == nil {
		return errRunTransitionIntegrity
	}
	namespace := l.runLocks
	if err := l.verifyRunTransitionNamespaceLocked(namespace); err != nil {
		return err
	}
	if err := lockLedgerFile(namespace.registry, ledgerFileLockWait); err != nil {
		return fmt.Errorf("revalidate run-transition identity registry: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, unlockLedgerFile(namespace.registry)) }()
	if err := l.verifyRunTransitionNamespaceLocked(namespace); err != nil {
		return err
	}
	identities, err := readRunTransitionRegistry(namespace.registry)
	if err != nil {
		return err
	}
	identity, found := identities[lease.digest]
	if !found || identity.dev != lease.dev || identity.ino != lease.ino {
		return fmt.Errorf("%w: run lock registry identity changed", errRunTransitionIntegrity)
	}
	name := hex.EncodeToString(lease.digest[:]) + ".lock"
	if err := verifyRunTransitionLock(namespace, name, lease.file, identity); err != nil {
		return err
	}
	return l.verifyRunTransitionNamespaceLocked(namespace)
}

func (l *JSONLLedger) ensureRunTransitionNamespaceLocked() (_ *runTransitionNamespace, resultErr error) {
	if l.runLocks != nil {
		if err := l.verifyRunTransitionNamespaceLocked(l.runLocks); err != nil {
			return nil, err
		}
		return l.runLocks, nil
	}
	if l.parent == nil || l.file == nil {
		return nil, fmt.Errorf("%w: ledger generation is not pinned", errRunTransitionIntegrity)
	}
	parentFD, ledgerFD := int(l.parent.Fd()), int(l.file.Fd())
	parentDev, parentIno, err := runTransitionParentIdentity(parentFD)
	if err != nil {
		return nil, fmt.Errorf("identify ledger parent: %w", err)
	}
	ledgerDev, ledgerIno, err := runTransitionFileIdentity(ledgerFD)
	if err != nil {
		return nil, fmt.Errorf("identify ledger file: %w", err)
	}
	authorityName := runTransitionAuthorityName(filepath.Base(l.path))
	if err := lockLedgerFile(l.parent, ledgerFileLockWait); err != nil {
		return nil, fmt.Errorf("lock ledger parent generation: %w", err)
	}
	parentLocked := true
	defer func() {
		if parentLocked {
			resultErr = errors.Join(resultErr, unlockLedgerFile(l.parent))
		}
	}()
	authority, authorityData, found, err := readRunTransitionAuthority(parentFD, authorityName)
	if err != nil {
		return nil, fmt.Errorf("read ledger-parent run-transition authority: %w", err)
	}
	if found && (authority.parentDev != parentDev || authority.parentIno != parentIno ||
		authority.ledgerDev != ledgerDev || authority.ledgerIno != ledgerIno) {
		return nil, fmt.Errorf("%w: ledger-parent generation binding changed", errRunTransitionIntegrity)
	}
	directoryName := filepath.Base(l.path) + ".run-locks"
	if !found {
		if fd, openErr := syscall.Openat(parentFD, directoryName, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0); openErr != syscall.ENOENT {
			if openErr == nil {
				_ = syscall.Close(fd)
			}
			return nil, fmt.Errorf("%w: unbound run-transition namespace exists", errRunTransitionIntegrity)
		}
		authority = runTransitionAuthority{state: 1, parentDev: parentDev, parentIno: parentIno, ledgerDev: ledgerDev, ledgerIno: ledgerIno}
		authorityData = encodeRunTransitionAuthority(authority)
		if err := setRunTransitionXattr(parentFD, authorityName, authorityData, runTransitionXattrCreate); err != nil || syscall.Fsync(parentFD) != nil {
			return nil, errors.Join(fmt.Errorf("%w: publish initializing run-transition generation", errRunTransitionIntegrity), err)
		}
		found = true
	}
	allowCreate := authority.state == 1
	directoryFD, err := openRunTransitionDirectory(parentFD, directoryName, allowCreate)
	if err != nil {
		return nil, err
	}
	namespace := &runTransitionNamespace{directory: os.NewFile(uintptr(directoryFD), directoryName), authorityName: authorityName}
	fail := func(cause error) (*runTransitionNamespace, error) {
		return nil, errors.Join(cause, closeRunTransitionNamespace(namespace))
	}
	namespace.directoryDev, namespace.directoryIno, err = runTransitionDirectoryIdentity(directoryFD)
	if err != nil {
		return fail(fmt.Errorf("identify run-transition directory: %w", err))
	}
	registryFD, err := openRunTransitionRegistry(directoryFD, allowCreate)
	if err != nil {
		return fail(err)
	}
	namespace.registry = os.NewFile(uintptr(registryFD), runTransitionRegistryName)
	namespace.registryDev, namespace.registryIno, err = runTransitionFileIdentity(registryFD)
	if err != nil {
		return fail(fmt.Errorf("identify run-transition registry: %w", err))
	}
	if _, err := readRunTransitionRegistry(namespace.registry); err != nil {
		return fail(fmt.Errorf("validate run-transition registry: %w", err))
	}
	established := runTransitionAuthority{state: 2, parentDev: parentDev, parentIno: parentIno, ledgerDev: ledgerDev, ledgerIno: ledgerIno,
		directoryDev: namespace.directoryDev, directoryIno: namespace.directoryIno,
		registryDev: namespace.registryDev, registryIno: namespace.registryIno}
	establishedData := encodeRunTransitionAuthority(established)
	if allowCreate {
		if err := setRunTransitionXattr(parentFD, authorityName, establishedData, runTransitionXattrReplace); err != nil || syscall.Fsync(parentFD) != nil {
			return fail(errors.Join(fmt.Errorf("%w: establish run-transition generation", errRunTransitionIntegrity), err))
		}
		authorityData = establishedData
	} else if authority.state != 2 || !bytes.Equal(authorityData, establishedData) {
		return fail(fmt.Errorf("%w: run-transition generation identity disagrees", errRunTransitionIntegrity))
	}
	namespace.authorityData = append([]byte(nil), authorityData...)
	l.runLocks = namespace
	if err := l.verifyRunTransitionNamespaceLocked(namespace); err != nil {
		l.runLocks = nil
		return fail(fmt.Errorf("verify established run-transition namespace: %w", err))
	}
	if err := unlockLedgerFile(l.parent); err != nil {
		parentLocked = false
		l.runLocks = nil
		return fail(err)
	}
	parentLocked = false
	return namespace, nil
}

func (l *JSONLLedger) verifyRunTransitionNamespaceLocked(namespace *runTransitionNamespace) error {
	if namespace == nil || namespace.directory == nil || namespace.registry == nil || l.parent == nil || l.file == nil {
		return errRunTransitionIntegrity
	}
	if err := l.verifyPhysicalIdentityLocked(); err != nil {
		return err
	}
	parentFD := int(l.parent.Fd())
	if err := verifyRunTransitionNamedDirectory(parentFD, filepath.Base(l.path)+".run-locks", int(namespace.directory.Fd()), namespace.directoryDev, namespace.directoryIno); err != nil {
		return err
	}
	if err := verifyRunTransitionNamedFile(int(namespace.directory.Fd()), runTransitionRegistryName, int(namespace.registry.Fd()), namespace.registryDev, namespace.registryIno); err != nil {
		return err
	}
	data, found, err := getRunTransitionXattr(parentFD, namespace.authorityName)
	if err != nil || !found || !bytes.Equal(data, namespace.authorityData) {
		return errors.Join(fmt.Errorf("%w: run-transition generation authority changed", errRunTransitionIntegrity), err)
	}
	return nil
}

func openRunTransitionDirectory(parent int, name string, allowCreate bool) (int, error) {
	fd, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err == syscall.ENOENT && allowCreate {
		mkdirErr := syscall.Mkdirat(parent, name, 0o700)
		if mkdirErr != nil && mkdirErr != syscall.EEXIST {
			return -1, fmt.Errorf("%w: create run-transition namespace", errRunTransitionIntegrity)
		}
		fd, err = syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err == nil && syscall.Fsync(parent) != nil {
			_ = syscall.Close(fd)
			return -1, fmt.Errorf("%w: sync run-transition namespace", errRunTransitionIntegrity)
		}
	}
	if err != nil || validateRunTransitionDirectory(fd) != nil {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return -1, errors.Join(fmt.Errorf("%w: open run-transition namespace", errRunTransitionIntegrity), err)
	}
	return fd, nil
}

func openRunTransitionRegistry(parent int, allowCreate bool) (int, error) {
	flags := syscall.O_RDWR | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	if allowCreate {
		flags |= syscall.O_CREAT | syscall.O_EXCL
	}
	fd, err := syscall.Openat(parent, runTransitionRegistryName, flags, 0o600)
	created := allowCreate && err == nil
	if allowCreate && err == syscall.EEXIST {
		fd, err = syscall.Openat(parent, runTransitionRegistryName, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	}
	if err != nil || validateRunTransitionFile(fd) != nil {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return -1, errors.Join(fmt.Errorf("%w: open run-transition registry", errRunTransitionIntegrity), err)
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil {
		_ = syscall.Close(fd)
		return -1, errRunTransitionIntegrity
	}
	if created || (allowCreate && stat.Size == 0) {
		if err := writeFullAt(fd, []byte(runTransitionRegistryHead), 0); err != nil || syscall.Fsync(fd) != nil || syscall.Fsync(parent) != nil {
			_ = syscall.Close(fd)
			return -1, errors.Join(fmt.Errorf("%w: initialize run-transition registry", errRunTransitionIntegrity), err)
		}
	}
	return fd, nil
}

func openRunTransitionLock(namespace *runTransitionNamespace, name string, digest [sha256.Size]byte, expected runTransitionIdentity, found bool) (*os.File, runTransitionIdentity, error) {
	parent := int(namespace.directory.Fd())
	flags := syscall.O_RDWR | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	fd, err := syscall.Openat(parent, name, flags, 0)
	created := false
	if err == syscall.ENOENT && !found {
		fd, err = syscall.Openat(parent, name, flags|syscall.O_CREAT|syscall.O_EXCL, 0o600)
		created = err == nil
	}
	if err != nil || validateRunTransitionFile(fd) != nil {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return nil, runTransitionIdentity{}, errors.Join(fmt.Errorf("%w: open exact run-transition lock", errRunTransitionIntegrity), err)
	}
	dev, ino, err := runTransitionFileIdentity(fd)
	if err != nil {
		_ = syscall.Close(fd)
		return nil, runTransitionIdentity{}, err
	}
	observed := runTransitionIdentity{digest: digest, dev: dev, ino: ino}
	file := os.NewFile(uintptr(fd), name)
	if created {
		if err := writeFullAt(fd, encodeRunTransitionIdentity(observed, true), 0); err != nil || file.Sync() != nil || namespace.directory.Sync() != nil {
			_ = file.Close()
			return nil, runTransitionIdentity{}, errors.Join(fmt.Errorf("%w: publish run-transition lock identity", errRunTransitionIntegrity), err)
		}
	}
	if found && (expected.digest != digest || expected.dev != dev || expected.ino != ino) {
		_ = file.Close()
		return nil, runTransitionIdentity{}, fmt.Errorf("%w: exact run-transition lock was replaced", errRunTransitionIntegrity)
	}
	if err := verifyRunTransitionLock(namespace, name, file, observed); err != nil {
		_ = file.Close()
		return nil, runTransitionIdentity{}, err
	}
	return file, observed, nil
}

func verifyRunTransitionLock(namespace *runTransitionNamespace, name string, file *os.File, identity runTransitionIdentity) error {
	if namespace == nil || namespace.directory == nil || file == nil || identity.ino == 0 {
		return errRunTransitionIntegrity
	}
	if err := verifyRunTransitionNamedFile(int(namespace.directory.Fd()), name, int(file.Fd()), identity.dev, identity.ino); err != nil {
		return err
	}
	data := make([]byte, runTransitionLockSize)
	if _, err := file.ReadAt(data, 0); err != nil || !bytes.Equal(data, encodeRunTransitionIdentity(identity, true)) {
		return errors.Join(fmt.Errorf("%w: run-transition lock identity content changed", errRunTransitionIntegrity), err)
	}
	var extra [1]byte
	if count, err := file.ReadAt(extra[:], int64(len(data))); count != 0 || err != io.EOF {
		return fmt.Errorf("%w: run-transition lock identity size changed", errRunTransitionIntegrity)
	}
	return nil
}

func readRunTransitionRegistry(file *os.File) (map[[sha256.Size]byte]runTransitionIdentity, error) {
	if file == nil {
		return nil, errRunTransitionIntegrity
	}
	stat, err := file.Stat()
	maximum := int64(len(runTransitionRegistryHead) + maxLedgerRecords*runTransitionRecordSize)
	if err != nil || stat.Size() < int64(len(runTransitionRegistryHead)) || stat.Size() > maximum ||
		(stat.Size()-int64(len(runTransitionRegistryHead)))%runTransitionRecordSize != 0 {
		return nil, errors.Join(fmt.Errorf("%w: run-transition registry has invalid bounds", errRunTransitionIntegrity), err)
	}
	data := make([]byte, stat.Size())
	if _, err := io.ReadFull(io.NewSectionReader(file, 0, stat.Size()), data); err != nil || string(data[:len(runTransitionRegistryHead)]) != runTransitionRegistryHead {
		return nil, errors.Join(fmt.Errorf("%w: run-transition registry is corrupt", errRunTransitionIntegrity), err)
	}
	result := make(map[[sha256.Size]byte]runTransitionIdentity, (len(data)-len(runTransitionRegistryHead))/runTransitionRecordSize)
	for offset := len(runTransitionRegistryHead); offset < len(data); offset += runTransitionRecordSize {
		identity, err := decodeRunTransitionIdentity(data[offset:offset+runTransitionRecordSize], false)
		if err != nil {
			return nil, err
		}
		if _, duplicate := result[identity.digest]; duplicate {
			return nil, fmt.Errorf("%w: duplicate run-transition identity", errRunTransitionIntegrity)
		}
		result[identity.digest] = identity
	}
	return result, nil
}

func appendRunTransitionIdentity(file *os.File, identity runTransitionIdentity) error {
	stat, err := file.Stat()
	if err != nil || stat.Size() < int64(len(runTransitionRegistryHead)) ||
		stat.Size() >= int64(len(runTransitionRegistryHead)+maxLedgerRecords*runTransitionRecordSize) {
		return errors.Join(fmt.Errorf("%w: run-transition registry append bound", errRunTransitionIntegrity), err)
	}
	if err := writeFullAt(int(file.Fd()), encodeRunTransitionIdentity(identity, false), stat.Size()); err != nil || file.Sync() != nil {
		return errors.Join(fmt.Errorf("%w: append run-transition identity", errRunTransitionIntegrity), err)
	}
	return nil
}

func encodeRunTransitionIdentity(identity runTransitionIdentity, lockFile bool) []byte {
	size := runTransitionRecordSize
	offset := 0
	if lockFile {
		size = runTransitionLockSize
		offset = len(runTransitionLockHead)
	}
	data := make([]byte, size)
	if lockFile {
		copy(data, runTransitionLockHead)
	}
	copy(data[offset:offset+sha256.Size], identity.digest[:])
	binary.BigEndian.PutUint64(data[offset+sha256.Size:offset+sha256.Size+8], identity.dev)
	binary.BigEndian.PutUint64(data[offset+sha256.Size+8:offset+sha256.Size+16], identity.ino)
	return data
}

func decodeRunTransitionIdentity(data []byte, lockFile bool) (runTransitionIdentity, error) {
	offset := 0
	if lockFile {
		if len(data) != runTransitionLockSize || string(data[:len(runTransitionLockHead)]) != runTransitionLockHead {
			return runTransitionIdentity{}, errRunTransitionIntegrity
		}
		offset = len(runTransitionLockHead)
	} else if len(data) != runTransitionRecordSize {
		return runTransitionIdentity{}, errRunTransitionIntegrity
	}
	var identity runTransitionIdentity
	copy(identity.digest[:], data[offset:offset+sha256.Size])
	identity.dev = binary.BigEndian.Uint64(data[offset+sha256.Size : offset+sha256.Size+8])
	identity.ino = binary.BigEndian.Uint64(data[offset+sha256.Size+8 : offset+sha256.Size+16])
	if identity.ino == 0 {
		return runTransitionIdentity{}, errRunTransitionIntegrity
	}
	return identity, nil
}

func encodeRunTransitionAuthority(authority runTransitionAuthority) []byte {
	count := 4
	if authority.state == 2 {
		count = 8
	}
	data := make([]byte, 5+count*8)
	copy(data, "RTG1")
	data[4] = authority.state
	values := []uint64{authority.parentDev, authority.parentIno, authority.ledgerDev, authority.ledgerIno,
		authority.directoryDev, authority.directoryIno, authority.registryDev, authority.registryIno}
	for index := 0; index < count; index++ {
		binary.BigEndian.PutUint64(data[5+index*8:5+(index+1)*8], values[index])
	}
	return data
}

func readRunTransitionAuthority(fd int, name string) (runTransitionAuthority, []byte, bool, error) {
	data, found, err := getRunTransitionXattr(fd, name)
	if err != nil || !found {
		return runTransitionAuthority{}, nil, found, err
	}
	if len(data) != 37 && len(data) != 69 || string(data[:4]) != "RTG1" || (data[4] != 1 && data[4] != 2) ||
		(data[4] == 1 && len(data) != 37) || (data[4] == 2 && len(data) != 69) {
		return runTransitionAuthority{}, nil, true, fmt.Errorf("%w: malformed run-transition generation", errRunTransitionIntegrity)
	}
	values := make([]uint64, (len(data)-5)/8)
	for index := range values {
		values[index] = binary.BigEndian.Uint64(data[5+index*8 : 5+(index+1)*8])
		if values[index] == 0 {
			return runTransitionAuthority{}, nil, true, errRunTransitionIntegrity
		}
	}
	authority := runTransitionAuthority{state: data[4], parentDev: values[0], parentIno: values[1], ledgerDev: values[2], ledgerIno: values[3]}
	if authority.state == 2 {
		authority.directoryDev, authority.directoryIno, authority.registryDev, authority.registryIno = values[4], values[5], values[6], values[7]
	}
	if !bytes.Equal(encodeRunTransitionAuthority(authority), data) {
		return runTransitionAuthority{}, nil, true, errRunTransitionIntegrity
	}
	return authority, append([]byte(nil), data...), true, nil
}

func runTransitionAuthorityName(ledgerName string) string {
	digest := sha256.Sum256([]byte(ledgerName))
	return "user.abcp.ledger.run-transition-v1." + hex.EncodeToString(digest[:])
}

func getRunTransitionXattr(fd int, name string) ([]byte, bool, error) {
	namePointer, err := syscall.BytePtrFromString(name)
	if err != nil {
		return nil, false, errRunTransitionIntegrity
	}
	buffer := make([]byte, 128)
	size, _, errno := syscall.Syscall6(syscall.SYS_FGETXATTR, uintptr(fd), uintptr(unsafe.Pointer(namePointer)),
		uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, 0)
	if errno == syscall.ENODATA {
		return nil, false, nil
	}
	if errno != 0 || size == 0 || size > uintptr(len(buffer)) {
		return nil, false, errors.Join(errRunTransitionIntegrity, errno)
	}
	return append([]byte(nil), buffer[:int(size)]...), true, nil
}

func setRunTransitionXattr(fd int, name string, data []byte, flags int) error {
	if len(data) == 0 || len(data) > 128 {
		return errRunTransitionIntegrity
	}
	namePointer, err := syscall.BytePtrFromString(name)
	if err != nil {
		return errRunTransitionIntegrity
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_FSETXATTR, uintptr(fd), uintptr(unsafe.Pointer(namePointer)),
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(flags), 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func runTransitionDirectoryIdentity(fd int) (uint64, uint64, error) {
	if err := validateRunTransitionDirectory(fd); err != nil {
		return 0, 0, err
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil {
		return 0, 0, errRunTransitionIntegrity
	}
	return uint64(stat.Dev), stat.Ino, nil
}

func runTransitionParentIdentity(fd int) (uint64, uint64, error) {
	if fd < 0 {
		return 0, 0, errRunTransitionIntegrity
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return 0, 0, errors.Join(errRunTransitionIntegrity, err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR || stat.Uid != uint32(os.Geteuid()) {
		return 0, 0, fmt.Errorf("%w: unsafe ledger parent", errRunTransitionIntegrity)
	}
	return uint64(stat.Dev), stat.Ino, nil
}

func runTransitionFileIdentity(fd int) (uint64, uint64, error) {
	if err := validateRunTransitionFile(fd); err != nil {
		return 0, 0, err
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil {
		return 0, 0, errRunTransitionIntegrity
	}
	return uint64(stat.Dev), stat.Ino, nil
}

func validateRunTransitionDirectory(fd int) error {
	if fd < 0 {
		return errRunTransitionIntegrity
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return errors.Join(errRunTransitionIntegrity, err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR || stat.Mode&0o777 != 0o700 || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%w: unsafe directory mode=%#o uid=%d", errRunTransitionIntegrity, stat.Mode, stat.Uid)
	}
	return nil
}

func validateRunTransitionFile(fd int) error {
	if fd < 0 {
		return errRunTransitionIntegrity
	}
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Mode&0o777 != 0o600 ||
		stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return errRunTransitionIntegrity
	}
	return nil
}

func verifyRunTransitionNamedDirectory(parent int, name string, pinned int, dev, ino uint64) error {
	currentDev, currentIno, err := runTransitionDirectoryIdentity(pinned)
	if err != nil || currentDev != dev || currentIno != ino {
		return errors.Join(errRunTransitionIntegrity, err)
	}
	reopened, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.Join(errRunTransitionIntegrity, err)
	}
	defer syscall.Close(reopened)
	namedDev, namedIno, err := runTransitionDirectoryIdentity(reopened)
	if err != nil || namedDev != dev || namedIno != ino {
		return errors.Join(errRunTransitionIntegrity, err)
	}
	return nil
}

func verifyRunTransitionNamedFile(parent int, name string, pinned int, dev, ino uint64) error {
	currentDev, currentIno, err := runTransitionFileIdentity(pinned)
	if err != nil || currentDev != dev || currentIno != ino {
		return errors.Join(errRunTransitionIntegrity, err)
	}
	reopened, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.Join(errRunTransitionIntegrity, err)
	}
	defer syscall.Close(reopened)
	namedDev, namedIno, err := runTransitionFileIdentity(reopened)
	if err != nil || namedDev != dev || namedIno != ino {
		return errors.Join(errRunTransitionIntegrity, err)
	}
	return nil
}

func writeFullAt(fd int, data []byte, offset int64) error {
	for len(data) > 0 {
		written, err := syscall.Pwrite(fd, data, offset)
		if written > 0 {
			data = data[written:]
			offset += int64(written)
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

//go:build linux

package actioncontrol

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"syscall"
	"time"
)

const resourceGuardDirectory = "read-model-resource-guard"

const (
	resourceSlotIdentityName                = ".slot-identities.json"
	resourceSlotIdentityMaxBytes            = 2048
	resourceDirectoryIdentityMaxBytes       = 512
	resourceDirectoryIdentityPublishTimeout = 2 * time.Second
	resourceRootAuthorityXattr              = "user.abcp.actioncontrol.resource-generation-v1"
	resourceRootAuthorityKind               = "ReadModelResourceRootGenerationV1"
)

var errResourceSlotsBusy = errors.New("cross-process read-model slots are busy")

// resourceBootstrapBoundaryHook is overridden only by subprocess crash tests.
var resourceBootstrapBoundaryHook = func(string, string) {}

type resourceGuard struct {
	root              string
	rootFD            int
	rootDev           uint64
	rootIno           uint64
	rootAuthorityData []byte
	guardFD           int
	guardDev          uint64
	guardIno          uint64
	guardAnchor       *resourceDirectoryAnchor
	ledgerFD          int
	ledgerDev         uint64
	ledgerIno         uint64
	ledgerAnchor      *resourceDirectoryAnchor
	snapshotFD        int
	snapshotDev       uint64
	snapshotIno       uint64
	snapshotAnchor    *resourceDirectoryAnchor
	ledgerSlots       *resourceSlotSet
	snapshotSlots     *resourceSlotSet
	mu                sync.Mutex
	closed            bool
	active            int
}

type resourceLease struct {
	guard *resourceGuard
	files []resourceLeaseFile
	once  sync.Once
	err   error
}

type resourceSlotSet struct {
	name         string
	slots        []*resourceSlotPin
	identityFile *os.File
	identityDev  uint64
	identityIno  uint64
	identityData []byte
}

type resourceSlotPin struct {
	name string
	file *os.File
	dev  uint64
	ino  uint64
}

type resourceLeaseFile struct {
	file   *os.File
	parent int
	pin    *resourceSlotPin
}

type resourceSlotIdentityV1 struct {
	Kind          string                        `json:"kind"`
	SchemaVersion int                           `json:"schema_version"`
	Resource      string                        `json:"resource"`
	Slots         []resourceSlotIdentityEntryV1 `json:"slots"`
}

type resourceSlotIdentityEntryV1 struct {
	Name   string `json:"name"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type resourceDirectoryAnchor struct {
	name string
	file *os.File
	dev  uint64
	ino  uint64
	data []byte
}

type resourceDirectoryIdentityV1 struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	Name          string `json:"name"`
	Device        uint64 `json:"device"`
	Inode         uint64 `json:"inode"`
}

func openResourceGuard(root string) (*resourceGuard, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) {
		return nil, errors.New("canonical service root is required for read-model resources")
	}
	rootFD, err := openAbsoluteDirectory(root)
	if err != nil || validateDirectoryFD(rootFD, true) != nil {
		if rootFD >= 0 {
			syscall.Close(rootFD)
		}
		return nil, ErrIntegrity
	}
	rootDev, rootIno, err := resourceDirectoryIdentity(rootFD)
	if err != nil {
		syscall.Close(rootFD)
		return nil, err
	}
	guard := &resourceGuard{root: root, rootFD: rootFD, rootDev: rootDev, rootIno: rootIno, guardFD: -1, ledgerFD: -1, snapshotFD: -1}
	fail := func(err error) (*resourceGuard, error) {
		_ = guard.Close()
		return nil, err
	}
	authority, authorityData, found, err := readResourceRootGenerationAuthority(rootFD, rootDev, rootIno)
	if err != nil {
		return fail(err)
	}
	locked := false
	defer func() {
		if locked {
			_ = syscall.Flock(rootFD, syscall.LOCK_UN)
		}
	}()
	if !found || authority.State == rootGenerationInitializing {
		if err := lockRootGeneration(rootFD, resourceDirectoryIdentityPublishTimeout); err != nil {
			return fail(err)
		}
		locked = true
		authority, authorityData, found, err = readResourceRootGenerationAuthority(rootFD, rootDev, rootIno)
		if err != nil {
			return fail(err)
		}
	}
	if !found {
		if err := requireGenerationEntriesAbsent(rootFD,
			generationEntry{name: resourceGuardDirectory, directory: true},
			generationEntry{name: resourceDirectoryAnchorName(resourceGuardDirectory)}); err != nil {
			return fail(err)
		}
		authority, authorityData, err = createInitializingResourceRootGeneration(rootFD, rootDev, rootIno)
		if err != nil {
			return fail(err)
		}
	}
	allowCreate := authority.State == rootGenerationInitializing
	guard.rootAuthorityData = append([]byte(nil), authorityData...)
	guard.guardFD, err = openResourceDirectory(rootFD, resourceGuardDirectory, allowCreate)
	if err != nil {
		return fail(fmt.Errorf("open read-model resource directory: %w", err))
	}
	guard.guardDev, guard.guardIno, err = resourceDirectoryIdentity(guard.guardFD)
	if err != nil {
		return fail(err)
	}
	guard.guardAnchor, err = openResourceDirectoryAnchor(guard, rootFD, ".", resourceGuardDirectory,
		guard.guardFD, guard.guardDev, guard.guardIno, allowCreate)
	if err != nil {
		return fail(fmt.Errorf("anchor read-model resource directory: %w", err))
	}
	guard.ledgerFD, err = openResourceDirectory(guard.guardFD, "ledger", allowCreate)
	if err != nil {
		return fail(fmt.Errorf("open ledger resource directory: %w", err))
	}
	guard.ledgerDev, guard.ledgerIno, err = resourceDirectoryIdentity(guard.ledgerFD)
	if err != nil {
		return fail(err)
	}
	guard.ledgerAnchor, err = openResourceDirectoryAnchor(guard, guard.guardFD, resourceGuardDirectory, "ledger",
		guard.ledgerFD, guard.ledgerDev, guard.ledgerIno, allowCreate)
	if err != nil {
		return fail(fmt.Errorf("anchor ledger resource directory: %w", err))
	}
	guard.snapshotFD, err = openResourceDirectory(guard.guardFD, "snapshot", allowCreate)
	if err != nil {
		return fail(fmt.Errorf("open snapshot resource directory: %w", err))
	}
	guard.snapshotDev, guard.snapshotIno, err = resourceDirectoryIdentity(guard.snapshotFD)
	if err != nil {
		return fail(err)
	}
	guard.snapshotAnchor, err = openResourceDirectoryAnchor(guard, guard.guardFD, resourceGuardDirectory, "snapshot",
		guard.snapshotFD, guard.snapshotDev, guard.snapshotIno, allowCreate)
	if err != nil {
		return fail(fmt.Errorf("anchor snapshot resource directory: %w", err))
	}
	if err := guard.initializeSlotSets(allowCreate); err != nil {
		return fail(err)
	}
	expectedAuthority, expectedData, err := makeResourceRootGenerationAuthority(rootDev, rootIno, resourceRootGenerationObjects(guard))
	if err != nil {
		return fail(err)
	}
	if allowCreate {
		authorityData, err = publishResourceRootGeneration(rootFD, expectedAuthority, expectedData)
	} else if !bytes.Equal(authorityData, expectedData) {
		err = ErrIntegrity
	}
	if err != nil {
		return fail(err)
	}
	guard.rootAuthorityData = append([]byte(nil), authorityData...)
	if err := guard.verify(); err != nil {
		return fail(err)
	}
	if locked {
		if err := syscall.Flock(rootFD, syscall.LOCK_UN); err != nil {
			return fail(ErrIntegrity)
		}
		locked = false
	}
	return guard, nil
}

func (g *resourceGuard) initializeSlotSets(allowCreate bool) (resultErr error) {
	if err := verifyAnchoredResourceDirectory(g.rootFD, resourceGuardDirectory, g.guardFD, g.guardDev, g.guardIno, g.guardAnchor); err != nil {
		return fmt.Errorf("guard-directory identity before slot initialization: %w", err)
	}
	if err := syscall.Flock(g.guardFD, syscall.LOCK_EX); err != nil {
		return ErrIntegrity
	}
	defer func() {
		if err := syscall.Flock(g.guardFD, syscall.LOCK_UN); err != nil {
			resultErr = errors.Join(resultErr, ErrIntegrity, err)
		}
	}()
	if err := g.verifyDirectories(); err != nil {
		return fmt.Errorf("verify resource directories after initialization lock: %w", err)
	}
	var err error
	g.ledgerSlots, err = openResourceSlotSet(g, g.ledgerFD, resourceGuardDirectory+"/ledger", "ledger", GlobalLedgerSlots, allowCreate)
	if err != nil {
		return fmt.Errorf("initialize ledger resource slots: %w", err)
	}
	g.snapshotSlots, err = openResourceSlotSet(g, g.snapshotFD, resourceGuardDirectory+"/snapshot", "snapshot", GlobalSnapshotSlots, allowCreate)
	if err != nil {
		return fmt.Errorf("initialize snapshot resource slots: %w", err)
	}
	if err := g.verify(); err != nil {
		return fmt.Errorf("verify read-model resource guard: %w", err)
	}
	return nil
}

func (g *resourceGuard) Close() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true
	if g.active != 0 {
		return g.verify()
	}
	return g.closeDescriptors()
}

func (g *resourceGuard) closeDescriptors() error {
	errs := []error{g.verify()}
	errs = append(errs, closeResourceSlotSet(g.snapshotSlots), closeResourceSlotSet(g.ledgerSlots))
	g.snapshotSlots, g.ledgerSlots = nil, nil
	errs = append(errs, closeResourceDirectoryAnchor(g.snapshotAnchor), closeResourceDirectoryAnchor(g.ledgerAnchor), closeResourceDirectoryAnchor(g.guardAnchor))
	g.snapshotAnchor, g.ledgerAnchor, g.guardAnchor = nil, nil, nil
	for _, fd := range []int{g.snapshotFD, g.ledgerFD, g.guardFD, g.rootFD} {
		if fd >= 0 {
			errs = append(errs, syscall.Close(fd))
		}
	}
	g.snapshotFD, g.ledgerFD, g.guardFD, g.rootFD = -1, -1, -1, -1
	return errors.Join(errs...)
}

func (g *resourceGuard) acquire(ctx context.Context, ledgers, snapshots int) (*resourceLease, error) {
	if g == nil || ctx == nil || ledgers < 0 || ledgers > GlobalLedgerSlots || snapshots < 0 || snapshots > GlobalSnapshotSlots || ledgers+snapshots == 0 {
		return nil, ErrIntegrity
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lease, err := g.tryAcquire(ledgers, snapshots)
		if err == nil {
			return lease, nil
		}
		if !errors.Is(err, errResourceSlotsBusy) {
			return nil, err
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (g *resourceGuard) tryAcquire(ledgers, snapshots int) (*resourceLease, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, ErrIntegrity
	}
	if err := g.verify(); err != nil {
		return nil, err
	}
	lease := &resourceLease{guard: g}
	for _, request := range []struct {
		fd, count int
		set       *resourceSlotSet
	}{{g.ledgerFD, ledgers, g.ledgerSlots}, {g.snapshotFD, snapshots, g.snapshotSlots}} {
		if request.count == 0 {
			continue
		}
		files, err := acquireResourceFiles(request.fd, request.set, request.count)
		lease.files = append(lease.files, files...)
		if err != nil {
			if closeErr := g.releaseFiles(lease.files); closeErr != nil {
				return nil, errors.Join(ErrIntegrity, err, closeErr)
			}
			return nil, err
		}
	}
	if err := g.verify(); err != nil {
		return nil, errors.Join(err, g.releaseFiles(lease.files))
	}
	g.active++
	return lease, nil
}

func (l *resourceLease) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.guard == nil {
			l.err = ErrIntegrity
		} else {
			l.guard.mu.Lock()
			l.err = l.guard.releaseFiles(l.files)
			if l.guard.active <= 0 {
				l.err = errors.Join(l.err, ErrIntegrity)
			} else {
				l.guard.active--
			}
			if l.guard.closed && l.guard.active == 0 {
				l.err = errors.Join(l.err, l.guard.closeDescriptors())
			}
			l.guard.mu.Unlock()
		}
		l.files = nil
		l.guard = nil
	})
	return l.err
}

func acquireResourceFiles(parent int, set *resourceSlotSet, count int) ([]resourceLeaseFile, error) {
	if set == nil || count < 0 || count > len(set.slots) {
		return nil, ErrIntegrity
	}
	files := make([]resourceLeaseFile, 0, count)
	for _, pin := range set.slots {
		if len(files) == count {
			break
		}
		if err := verifyResourceSlotPin(parent, pin); err != nil {
			return files, err
		}
		fd, err := syscall.Openat(parent, pin.name, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return files, ErrIntegrity
		}
		var before syscall.Stat_t
		if syscall.Fstat(fd, &before) != nil || validateResourceFile(before) != nil || uint64(before.Dev) != pin.dev || before.Ino != pin.ino {
			syscall.Close(fd)
			return files, ErrIntegrity
		}
		if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			syscall.Close(fd)
			if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
				continue
			}
			return files, ErrIntegrity
		}
		file := os.NewFile(uintptr(fd), pin.name)
		files = append(files, resourceLeaseFile{file: file, parent: parent, pin: pin})
		if err := verifyResourceLeaseFile(files[len(files)-1]); err != nil {
			return files, err
		}
	}
	if len(files) != count {
		return files, errResourceSlotsBusy
	}
	return files, nil
}

func (g *resourceGuard) releaseFiles(files []resourceLeaseFile) error {
	resultErr := g.verify()
	for index := len(files) - 1; index >= 0; index-- {
		entry := files[index]
		resultErr = errors.Join(resultErr, verifyResourceLeaseFile(entry))
		if entry.file == nil {
			resultErr = errors.Join(resultErr, ErrIntegrity)
			continue
		}
		resultErr = errors.Join(resultErr, syscall.Flock(int(entry.file.Fd()), syscall.LOCK_UN), entry.file.Close())
	}
	return errors.Join(resultErr, g.verify())
}

func verifyResourceLeaseFile(entry resourceLeaseFile) error {
	if entry.file == nil || entry.pin == nil {
		return ErrIntegrity
	}
	return verifyResourceFile(entry.parent, entry.pin.name, int(entry.file.Fd()), entry.pin.dev, entry.pin.ino)
}

func (g *resourceGuard) verify() error {
	if err := g.verifyDirectories(); err != nil {
		return err
	}
	if err := verifyResourceSlotSet(g.ledgerFD, g.ledgerSlots); err != nil {
		return fmt.Errorf("ledger slot set: %w", err)
	}
	if err := verifyResourceSlotSet(g.snapshotFD, g.snapshotSlots); err != nil {
		return fmt.Errorf("snapshot slot set: %w", err)
	}
	return nil
}

func (g *resourceGuard) verifyDirectories() error {
	rootFD, err := openAbsoluteDirectory(g.root)
	if err != nil {
		return ErrIntegrity
	}
	defer syscall.Close(rootFD)
	if err := verifyResourceDirectoryIdentity(rootFD, g.rootDev, g.rootIno); err != nil {
		return fmt.Errorf("service-root identity: %w", err)
	}
	if len(g.rootAuthorityData) == 0 || verifyRootGenerationData(g.rootFD, resourceRootAuthorityXattr, g.rootAuthorityData) != nil {
		return fmt.Errorf("service-root generation authority: %w", ErrIntegrity)
	}
	if err := verifyAnchoredResourceDirectory(g.rootFD, resourceGuardDirectory, g.guardFD, g.guardDev, g.guardIno, g.guardAnchor); err != nil {
		return fmt.Errorf("guard-directory identity: %w", err)
	}
	if err := verifyAnchoredResourceDirectory(g.guardFD, "ledger", g.ledgerFD, g.ledgerDev, g.ledgerIno, g.ledgerAnchor); err != nil {
		return fmt.Errorf("ledger-directory identity: %w", err)
	}
	if err := verifyAnchoredResourceDirectory(g.guardFD, "snapshot", g.snapshotFD, g.snapshotDev, g.snapshotIno, g.snapshotAnchor); err != nil {
		return fmt.Errorf("snapshot-directory identity: %w", err)
	}
	if err := verifyResourceGuardDirectory(g.guardFD); err != nil {
		return fmt.Errorf("guard-directory contents: %w", err)
	}
	return nil
}

func verifyResourceGuardDirectory(parent int) error {
	fd, err := syscall.Openat(parent, ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ErrIntegrity
	}
	directory := os.NewFile(uintptr(fd), "read-model-resource-guard")
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil || len(entries) != 4 {
		return ErrIntegrity
	}
	expected := map[string]bool{
		resourceDirectoryAnchorName("ledger"):   false,
		resourceDirectoryAnchorName("snapshot"): false,
		"ledger":                                true,
		"snapshot":                              true,
	}
	for _, entry := range entries {
		directoryEntry, ok := expected[entry.Name()]
		if !ok || entry.Type()&os.ModeSymlink != 0 || directoryEntry != entry.IsDir() || !directoryEntry && !entry.Type().IsRegular() {
			return ErrIntegrity
		}
	}
	return nil
}

func resourceDirectoryAnchorName(name string) string { return "." + name + ".identity.json" }

func openResourceDirectoryAnchor(guard *resourceGuard, parent int, authorityParent, name string, pinned int,
	dev uint64, ino uint64, allowCreate bool) (*resourceDirectoryAnchor, error) {
	if guard == nil || name == "" || authorityParent == "" || pinned < 0 || verifyNamedResourceDirectory(parent, name, pinned, dev, ino) != nil {
		return nil, ErrIntegrity
	}
	identity := resourceDirectoryIdentityV1{Kind: "ReadModelResourceDirectoryIdentityV1", SchemaVersion: 1, Name: name, Device: dev, Inode: ino}
	data, err := json.Marshal(identity)
	if err != nil || len(data) == 0 || len(data) > resourceDirectoryIdentityMaxBytes {
		return nil, ErrIntegrity
	}
	anchorName := resourceDirectoryAnchorName(name)
	anchorFD := -1
	var anchorStat syscall.Stat_t
	var anchorFile *os.File
	if allowCreate {
		objects := []rootGenerationObjectIdentityV1{
			{Parent: authorityParent, Name: name, ObjectType: "directory", Device: dev, Inode: ino},
			{Parent: authorityParent, Name: anchorName, ObjectType: "file"},
		}
		anchorFile, anchorStat, err = guard.publishPreparedResourceIdentity(parent, anchorName, objects, 1, data)
	} else {
		anchorFD, err = syscall.Openat(parent, anchorName, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err == nil {
			anchorFile = os.NewFile(uintptr(anchorFD), anchorName)
			anchorStat, err = verifyResourceIdentityPublicationBytes(anchorFile, data)
		}
	}
	if err != nil || anchorFile == nil {
		if anchorFile != nil {
			_ = anchorFile.Close()
		} else if anchorFD >= 0 {
			_ = syscall.Close(anchorFD)
		}
		return nil, ErrIntegrity
	}
	anchor := &resourceDirectoryAnchor{name: anchorName, file: anchorFile, dev: uint64(anchorStat.Dev), ino: anchorStat.Ino, data: append([]byte(nil), data...)}
	if err := verifyAnchoredResourceDirectory(parent, name, pinned, dev, ino, anchor); err != nil {
		_ = anchorFile.Close()
		return nil, err
	}
	return anchor, nil
}

func verifyAnchoredResourceDirectory(parent int, name string, pinned int, dev uint64, ino uint64, anchor *resourceDirectoryAnchor) error {
	if anchor == nil || anchor.file == nil || anchor.name != resourceDirectoryAnchorName(name) || len(anchor.data) == 0 || len(anchor.data) > resourceDirectoryIdentityMaxBytes {
		return ErrIntegrity
	}
	if err := verifyNamedResourceDirectory(parent, name, pinned, dev, ino); err != nil {
		return err
	}
	anchorFD := int(anchor.file.Fd())
	var anchorStat syscall.Stat_t
	if syscall.Fstat(anchorFD, &anchorStat) != nil || validateResourceFile(anchorStat) != nil ||
		uint64(anchorStat.Dev) != anchor.dev || anchorStat.Ino != anchor.ino || anchorStat.Size != int64(len(anchor.data)) {
		return ErrIntegrity
	}
	if err := verifyResourceFile(parent, anchor.name, anchorFD, anchor.dev, anchor.ino); err != nil {
		return err
	}
	observed := make([]byte, len(anchor.data))
	if count, err := anchor.file.ReadAt(observed, 0); err != nil || count != len(observed) || !bytes.Equal(observed, anchor.data) {
		return ErrIntegrity
	}
	return nil
}

func closeResourceDirectoryAnchor(anchor *resourceDirectoryAnchor) error {
	if anchor == nil || anchor.file == nil {
		return nil
	}
	err := anchor.file.Close()
	anchor.file = nil
	return err
}

func resourceRootGenerationObjects(g *resourceGuard) []rootGenerationObjectIdentityV1 {
	if g == nil || g.guardAnchor == nil || g.ledgerAnchor == nil || g.snapshotAnchor == nil ||
		g.ledgerSlots == nil || g.snapshotSlots == nil {
		return nil
	}
	objects := []rootGenerationObjectIdentityV1{
		{Parent: ".", Name: resourceGuardDirectory, ObjectType: "directory", Device: g.guardDev, Inode: g.guardIno},
		{Parent: ".", Name: g.guardAnchor.name, ObjectType: "file", Device: g.guardAnchor.dev, Inode: g.guardAnchor.ino},
		{Parent: resourceGuardDirectory, Name: "ledger", ObjectType: "directory", Device: g.ledgerDev, Inode: g.ledgerIno},
		{Parent: resourceGuardDirectory, Name: g.ledgerAnchor.name, ObjectType: "file", Device: g.ledgerAnchor.dev, Inode: g.ledgerAnchor.ino},
		{Parent: resourceGuardDirectory, Name: "snapshot", ObjectType: "directory", Device: g.snapshotDev, Inode: g.snapshotIno},
		{Parent: resourceGuardDirectory, Name: g.snapshotAnchor.name, ObjectType: "file", Device: g.snapshotAnchor.dev, Inode: g.snapshotAnchor.ino},
	}
	for _, set := range []*resourceSlotSet{g.ledgerSlots, g.snapshotSlots} {
		parent := resourceGuardDirectory + "/" + set.name
		objects = append(objects, rootGenerationObjectIdentityV1{Parent: parent, Name: resourceSlotIdentityName,
			ObjectType: "file", Device: set.identityDev, Inode: set.identityIno})
		for _, slot := range set.slots {
			if slot == nil {
				return nil
			}
			objects = append(objects, rootGenerationObjectIdentityV1{Parent: parent, Name: slot.name,
				ObjectType: "file", Device: slot.dev, Inode: slot.ino})
		}
	}
	return objects
}

// The resource generation still binds all 20 physical objects directly. Its
// fixed-order encoding avoids spending most of the shared service-root xattr
// block on repeated JSON field names.
const resourceRootGenerationBinarySize = 21 + 20*16

const resourceRootGenerationInitializingHeaderSize = 23

func resourceRootGenerationSchema() []rootGenerationObjectIdentityV1 {
	objects := []rootGenerationObjectIdentityV1{
		{Parent: ".", Name: resourceGuardDirectory, ObjectType: "directory"},
		{Parent: ".", Name: resourceDirectoryAnchorName(resourceGuardDirectory), ObjectType: "file"},
		{Parent: resourceGuardDirectory, Name: "ledger", ObjectType: "directory"},
		{Parent: resourceGuardDirectory, Name: resourceDirectoryAnchorName("ledger"), ObjectType: "file"},
		{Parent: resourceGuardDirectory, Name: "snapshot", ObjectType: "directory"},
		{Parent: resourceGuardDirectory, Name: resourceDirectoryAnchorName("snapshot"), ObjectType: "file"},
	}
	for _, resource := range []struct {
		name  string
		slots int
	}{{"ledger", GlobalLedgerSlots}, {"snapshot", GlobalSnapshotSlots}} {
		parent := resourceGuardDirectory + "/" + resource.name
		objects = append(objects, rootGenerationObjectIdentityV1{Parent: parent, Name: resourceSlotIdentityName, ObjectType: "file"})
		for slot := 0; slot < resource.slots; slot++ {
			objects = append(objects, rootGenerationObjectIdentityV1{Parent: parent, Name: resourceSlotName(slot), ObjectType: "file"})
		}
	}
	return objects
}

func makeResourceRootGenerationAuthority(rootDev, rootIno uint64, objects []rootGenerationObjectIdentityV1) (rootGenerationAuthorityV1, []byte, error) {
	authority := rootGenerationAuthorityV1{Kind: resourceRootAuthorityKind, SchemaVersion: 1, State: rootGenerationEstablished,
		RootDevice: rootDev, RootInode: rootIno, Objects: append([]rootGenerationObjectIdentityV1(nil), objects...)}
	data, err := encodeResourceRootGeneration(authority)
	return authority, data, err
}

func createInitializingResourceRootGeneration(fd int, rootDev, rootIno uint64) (rootGenerationAuthorityV1, []byte, error) {
	authority := rootGenerationAuthorityV1{Kind: resourceRootAuthorityKind, SchemaVersion: 1, State: rootGenerationInitializing,
		RootDevice: rootDev, RootInode: rootIno}
	data, err := encodeResourceRootGeneration(authority)
	if err != nil {
		return rootGenerationAuthorityV1{}, nil, err
	}
	if err := fsetRootXattr(fd, resourceRootAuthorityXattr, data, rootGenerationXattrCreate); err != nil || syscall.Fsync(fd) != nil {
		return rootGenerationAuthorityV1{}, nil, ErrIntegrity
	}
	if err := verifyRootGenerationData(fd, resourceRootAuthorityXattr, data); err != nil {
		return rootGenerationAuthorityV1{}, nil, err
	}
	return authority, data, nil
}

func publishResourceRootGeneration(fd int, authority rootGenerationAuthorityV1, data []byte) ([]byte, error) {
	canonical, err := encodeResourceRootGeneration(authority)
	if err != nil || authority.State != rootGenerationEstablished || !bytes.Equal(canonical, data) {
		return nil, ErrIntegrity
	}
	if err := fsetRootXattr(fd, resourceRootAuthorityXattr, data, rootGenerationXattrReplace); err != nil || syscall.Fsync(fd) != nil {
		return nil, ErrIntegrity
	}
	if err := verifyRootGenerationData(fd, resourceRootAuthorityXattr, data); err != nil {
		return nil, err
	}
	return append([]byte(nil), data...), nil
}

func readResourceRootGenerationAuthority(fd int, rootDev, rootIno uint64) (rootGenerationAuthorityV1, []byte, bool, error) {
	data, found, err := fgetRootXattr(fd, resourceRootAuthorityXattr)
	if err != nil || !found {
		return rootGenerationAuthorityV1{}, nil, found, err
	}
	authority, canonical, _, err := decodeResourceRootGeneration(data, rootDev, rootIno)
	if err != nil {
		return rootGenerationAuthorityV1{}, nil, true, err
	}
	return authority, canonical, true, nil
}

func encodeResourceRootGeneration(authority rootGenerationAuthorityV1) ([]byte, error) {
	if authority.Kind != resourceRootAuthorityKind || authority.SchemaVersion != 1 || authority.RootInode == 0 ||
		authority.IssuanceCount != 0 || authority.IssuanceFinalRecordSHA256 != "" || authority.PreparedRunGeneration != nil {
		return nil, ErrIntegrity
	}
	size := 21
	state := byte(1)
	if authority.State == rootGenerationEstablished {
		size = resourceRootGenerationBinarySize
		state = 2
	} else if authority.State != rootGenerationInitializing {
		return nil, ErrIntegrity
	} else if len(authority.Objects) != 0 || authority.PreparedIdentity != nil {
		if validateResourceInitializingAuthority(authority) != nil {
			return nil, ErrIntegrity
		}
		preparedCount := 0
		if authority.PreparedIdentity != nil {
			preparedCount = len(authority.PreparedIdentity.Objects)
		}
		size = resourceRootGenerationInitializingHeaderSize + 16*(len(authority.Objects)+preparedCount)
		state = 3
	}
	data := make([]byte, size)
	copy(data[:4], "RRG1")
	data[4] = state
	binary.BigEndian.PutUint64(data[5:13], authority.RootDevice)
	binary.BigEndian.PutUint64(data[13:21], authority.RootInode)
	if state == 1 {
		if len(authority.Objects) != 0 || authority.PreparedIdentity != nil {
			return nil, ErrIntegrity
		}
		return data, nil
	}
	schema := resourceRootGenerationSchema()
	if state == 2 && (len(authority.Objects) != len(schema) || len(schema) != 20 || authority.PreparedIdentity != nil) {
		return nil, ErrIntegrity
	}
	offset := 21
	objects := authority.Objects
	if state == 3 {
		data[21] = byte(len(authority.Objects))
		if authority.PreparedIdentity != nil {
			data[22] = byte(len(authority.PreparedIdentity.Objects))
			objects = append(append([]rootGenerationObjectIdentityV1(nil), authority.Objects...), authority.PreparedIdentity.Objects...)
		}
		offset = resourceRootGenerationInitializingHeaderSize
	}
	for index, object := range objects {
		expected := schema[index]
		identityIntent := state == 3 && authority.PreparedIdentity != nil && index == len(authority.Objects)+authority.PreparedIdentity.IdentityIndex
		if object.Parent != expected.Parent || object.Name != expected.Name || object.ObjectType != expected.ObjectType ||
			(object.Inode == 0 && !identityIntent) || (object.Device == 0) != (object.Inode == 0) {
			return nil, ErrIntegrity
		}
		binary.BigEndian.PutUint64(data[offset:offset+8], object.Device)
		binary.BigEndian.PutUint64(data[offset+8:offset+16], object.Inode)
		offset += 16
	}
	return data, nil
}

func decodeResourceRootGeneration(data []byte, rootDev, rootIno uint64) (rootGenerationAuthorityV1, []byte, bool, error) {
	if len(data) < 21 || string(data[:4]) != "RRG1" {
		return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
	}
	authority := rootGenerationAuthorityV1{Kind: resourceRootAuthorityKind, SchemaVersion: 1,
		RootDevice: binary.BigEndian.Uint64(data[5:13]), RootInode: binary.BigEndian.Uint64(data[13:21])}
	if authority.RootDevice != rootDev || authority.RootInode != rootIno {
		return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
	}
	switch data[4] {
	case 1:
		if len(data) != 21 {
			return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
		}
		authority.State = rootGenerationInitializing
		return authority, append([]byte(nil), data...), false, nil
	case 2:
		if len(data) != resourceRootGenerationBinarySize {
			return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
		}
		authority.State = rootGenerationEstablished
		authority.Objects = resourceRootGenerationSchema()
		offset := 21
		for index := range authority.Objects {
			authority.Objects[index].Device = binary.BigEndian.Uint64(data[offset : offset+8])
			authority.Objects[index].Inode = binary.BigEndian.Uint64(data[offset+8 : offset+16])
			if authority.Objects[index].Inode == 0 {
				return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
			}
			offset += 16
		}
		canonical, err := encodeResourceRootGeneration(authority)
		if err != nil || !bytes.Equal(canonical, data) {
			return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
		}
		return authority, canonical, true, nil
	case 3:
		if len(data) < resourceRootGenerationInitializingHeaderSize {
			return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
		}
		completedCount, preparedCount := int(data[21]), int(data[22])
		if completedCount > len(resourceRootGenerationSchema()) || preparedCount > 16 ||
			len(data) != resourceRootGenerationInitializingHeaderSize+16*(completedCount+preparedCount) {
			return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
		}
		authority.State = rootGenerationInitializing
		schema := resourceRootGenerationSchema()
		authority.Objects = append([]rootGenerationObjectIdentityV1(nil), schema[:completedCount]...)
		offset := resourceRootGenerationInitializingHeaderSize
		for index := range authority.Objects {
			authority.Objects[index].Device = binary.BigEndian.Uint64(data[offset : offset+8])
			authority.Objects[index].Inode = binary.BigEndian.Uint64(data[offset+8 : offset+16])
			offset += 16
		}
		if preparedCount != 0 {
			if completedCount+preparedCount > len(schema) {
				return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
			}
			objects := append([]rootGenerationObjectIdentityV1(nil), schema[completedCount:completedCount+preparedCount]...)
			for index := range objects {
				objects[index].Device = binary.BigEndian.Uint64(data[offset : offset+8])
				objects[index].Inode = binary.BigEndian.Uint64(data[offset+8 : offset+16])
				offset += 16
			}
			prepared, err := resourcePreparedIdentityForObjects(completedCount, objects)
			if err != nil {
				return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
			}
			authority.PreparedIdentity = &prepared
		}
		canonical, err := encodeResourceRootGeneration(authority)
		if err != nil || !bytes.Equal(canonical, data) {
			return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
		}
		return authority, canonical, false, nil
	default:
		return rootGenerationAuthorityV1{}, nil, false, ErrIntegrity
	}
}

func validateResourceInitializingAuthority(authority rootGenerationAuthorityV1) error {
	schema := resourceRootGenerationSchema()
	if len(authority.Objects) > len(schema) || !resourceBootstrapBoundary(len(authority.Objects)) {
		return ErrIntegrity
	}
	for index, object := range authority.Objects {
		expected := schema[index]
		if object.Parent != expected.Parent || object.Name != expected.Name || object.ObjectType != expected.ObjectType || object.Inode == 0 {
			return ErrIntegrity
		}
	}
	if authority.PreparedIdentity == nil {
		return nil
	}
	expected, err := resourcePreparedIdentityForObjects(len(authority.Objects), authority.PreparedIdentity.Objects)
	if err != nil || !reflect.DeepEqual(expected, *authority.PreparedIdentity) {
		return ErrIntegrity
	}
	return nil
}

func resourceBootstrapBoundary(count int) bool {
	return count == 0 || count == 2 || count == 4 || count == 6 || count == 15 || count == 20
}

func resourcePreparedIdentityForObjects(start int, objects []rootGenerationObjectIdentityV1) (preparedIdentityPublicationV1, error) {
	schema := resourceRootGenerationSchema()
	if !resourceBootstrapBoundary(start) || start == len(schema) {
		return preparedIdentityPublicationV1{}, ErrIntegrity
	}
	expectedCount, identityIndex := 2, 1
	if start == 6 {
		expectedCount, identityIndex = 1+GlobalLedgerSlots, 0
	} else if start == 15 {
		expectedCount, identityIndex = 1+GlobalSnapshotSlots, 0
	}
	if len(objects) != expectedCount || start+len(objects) > len(schema) {
		return preparedIdentityPublicationV1{}, ErrIntegrity
	}
	for index, object := range objects {
		expected := schema[start+index]
		if object.Parent != expected.Parent || object.Name != expected.Name || object.ObjectType != expected.ObjectType ||
			(index != identityIndex && object.Inode == 0) || (object.Device == 0) != (object.Inode == 0) {
			return preparedIdentityPublicationV1{}, ErrIntegrity
		}
	}
	var data []byte
	var err error
	if identityIndex == 1 {
		target := objects[0]
		data, err = json.Marshal(resourceDirectoryIdentityV1{Kind: "ReadModelResourceDirectoryIdentityV1", SchemaVersion: 1,
			Name: target.Name, Device: target.Device, Inode: target.Inode})
	} else {
		resource := "ledger"
		if start == 15 {
			resource = "snapshot"
		}
		identity := resourceSlotIdentityV1{Kind: "ReadModelResourceSlotIdentityV1", SchemaVersion: 1, Resource: resource,
			Slots: make([]resourceSlotIdentityEntryV1, 0, len(objects)-1)}
		for _, object := range objects[1:] {
			identity.Slots = append(identity.Slots, resourceSlotIdentityEntryV1{Name: object.Name, Device: object.Device, Inode: object.Inode})
		}
		data, err = json.Marshal(identity)
	}
	if err != nil || len(data) == 0 {
		return preparedIdentityPublicationV1{}, ErrIntegrity
	}
	identityObject := objects[identityIndex]
	return preparedIdentityPublicationV1{Parent: identityObject.Parent, IdentityName: identityObject.Name,
		StageName: identityObject.Name + ".prepared", IdentityIndex: identityIndex, IdentitySHA256: sha256Hex(data),
		Objects: append([]rootGenerationObjectIdentityV1(nil), objects...)}, nil
}

func openResourceDirectory(parent int, name string, allowCreate bool) (int, error) {
	fd, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	created := false
	if err == syscall.ENOENT && allowCreate {
		mkdirErr := syscall.Mkdirat(parent, name, 0o700)
		if mkdirErr != nil && mkdirErr != syscall.EEXIST {
			return -1, ErrIntegrity
		}
		created = mkdirErr == nil
		fd, err = syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	}
	if err != nil || validateDirectoryFD(fd, false) != nil {
		if fd >= 0 {
			syscall.Close(fd)
		}
		return -1, ErrIntegrity
	}
	if created && syscall.Fsync(parent) != nil {
		syscall.Close(fd)
		return -1, ErrIntegrity
	}
	return fd, nil
}

func openResourceSlotSet(guard *resourceGuard, parent int, authorityParent, resource string, total int, allowCreate bool) (*resourceSlotSet, error) {
	if guard == nil || authorityParent == "" || resource == "" || total <= 0 {
		return nil, ErrIntegrity
	}
	if err := ensureResourceSlots(parent, total, allowCreate); err != nil {
		return nil, err
	}
	set := &resourceSlotSet{name: resource, slots: make([]*resourceSlotPin, 0, total)}
	fail := func(err error) (*resourceSlotSet, error) {
		return nil, errors.Join(err, closeResourceSlotSet(set))
	}
	identity := resourceSlotIdentityV1{Kind: "ReadModelResourceSlotIdentityV1", SchemaVersion: 1, Resource: resource,
		Slots: make([]resourceSlotIdentityEntryV1, 0, total)}
	for slot := 0; slot < total; slot++ {
		name := resourceSlotName(slot)
		fd, err := syscall.Openat(parent, name, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return fail(ErrIntegrity)
		}
		var stat syscall.Stat_t
		if syscall.Fstat(fd, &stat) != nil || validateResourceFile(stat) != nil || verifyResourceFile(parent, name, fd, uint64(stat.Dev), stat.Ino) != nil {
			syscall.Close(fd)
			return fail(ErrIntegrity)
		}
		pin := &resourceSlotPin{name: name, file: os.NewFile(uintptr(fd), name), dev: uint64(stat.Dev), ino: stat.Ino}
		set.slots = append(set.slots, pin)
		identity.Slots = append(identity.Slots, resourceSlotIdentityEntryV1{Name: name, Device: pin.dev, Inode: pin.ino})
	}
	data, err := json.Marshal(identity)
	if err != nil || len(data) > resourceSlotIdentityMaxBytes {
		return fail(ErrIntegrity)
	}
	identityFD := -1
	var identityFile *os.File
	var identityStat syscall.Stat_t
	if allowCreate {
		objects := make([]rootGenerationObjectIdentityV1, 0, len(set.slots)+1)
		objects = append(objects, rootGenerationObjectIdentityV1{Parent: authorityParent, Name: resourceSlotIdentityName, ObjectType: "file"})
		for _, slot := range set.slots {
			objects = append(objects, rootGenerationObjectIdentityV1{Parent: authorityParent, Name: slot.name,
				ObjectType: "file", Device: slot.dev, Inode: slot.ino})
		}
		identityFile, identityStat, err = guard.publishPreparedResourceIdentity(parent, resourceSlotIdentityName, objects, 0, data)
	} else {
		identityFD, err = syscall.Openat(parent, resourceSlotIdentityName, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err == nil {
			identityFile = os.NewFile(uintptr(identityFD), resourceSlotIdentityName)
			identityStat, err = verifyResourceIdentityPublicationBytes(identityFile, data)
		}
	}
	if err != nil || identityFile == nil {
		if identityFile != nil {
			_ = identityFile.Close()
		} else if identityFD >= 0 {
			_ = syscall.Close(identityFD)
		}
		return fail(ErrIntegrity)
	}
	set.identityFile = identityFile
	set.identityDev = uint64(identityStat.Dev)
	set.identityIno = identityStat.Ino
	set.identityData = append([]byte(nil), data...)
	if err := verifyResourceSlotSet(parent, set); err != nil {
		return fail(err)
	}
	return set, nil
}

func (g *resourceGuard) publishPreparedResourceIdentity(parent int, identityName string,
	objects []rootGenerationObjectIdentityV1, identityIndex int, data []byte) (*os.File, syscall.Stat_t, error) {
	if g == nil || g.rootFD < 0 || parent < 0 || len(data) == 0 || identityIndex < 0 || identityIndex >= len(objects) {
		return nil, syscall.Stat_t{}, ErrIntegrity
	}
	start, err := resourceIdentityPublicationStart(objects)
	if err != nil || objects[identityIndex].Name != identityName {
		return nil, syscall.Stat_t{}, ErrIntegrity
	}
	prepared := preparedIdentityPublicationV1{
		Parent: objects[identityIndex].Parent, IdentityName: identityName, StageName: identityName + ".prepared",
		IdentityIndex: identityIndex, IdentitySHA256: sha256Hex(data), Objects: append([]rootGenerationObjectIdentityV1(nil), objects...),
	}
	hookName := prepared.Parent + "/" + identityName
	authority, authorityData, found, err := readResourceRootGenerationAuthority(g.rootFD, g.rootDev, g.rootIno)
	if err != nil || !found || authority.State != rootGenerationInitializing {
		return nil, syscall.Stat_t{}, ErrIntegrity
	}
	g.rootAuthorityData = append([]byte(nil), authorityData...)
	if len(authority.Objects) >= start+len(objects) {
		expected := append([]rootGenerationObjectIdentityV1(nil), objects...)
		expected[identityIndex] = authority.Objects[start+identityIndex]
		if !reflect.DeepEqual(authority.Objects[start:start+len(objects)], expected) {
			return nil, syscall.Stat_t{}, ErrIntegrity
		}
		anchor := authority.Objects[start+identityIndex]
		identityFD, openErr := syscall.Openat(parent, identityName, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if openErr != nil || verifyResourceFile(parent, identityName, identityFD, anchor.Device, anchor.Inode) != nil {
			if identityFD >= 0 {
				_ = syscall.Close(identityFD)
			}
			return nil, syscall.Stat_t{}, ErrIntegrity
		}
		file := os.NewFile(uintptr(identityFD), identityName)
		stat, verifyErr := verifyResourceIdentityPublicationBytes(file, data)
		if verifyErr != nil {
			_ = file.Close()
			return nil, syscall.Stat_t{}, verifyErr
		}
		return file, stat, nil
	}
	if len(authority.Objects) != start {
		return nil, syscall.Stat_t{}, ErrIntegrity
	}
	if authority.PreparedIdentity == nil {
		if identityNameExists(parent, identityName) {
			return nil, syscall.Stat_t{}, ErrIntegrity
		}
		next := authority
		next.PreparedIdentity = &prepared
		authorityData, err = g.replaceInitializingResourceRootGeneration(authorityData, next, hookName, "intent")
		if err != nil {
			return nil, syscall.Stat_t{}, err
		}
		authority = next
	} else {
		if !samePreparedIdentityIntent(*authority.PreparedIdentity, prepared) {
			return nil, syscall.Stat_t{}, ErrIntegrity
		}
		prepared = clonePreparedIdentity(*authority.PreparedIdentity)
	}

	identityObject := prepared.Objects[prepared.IdentityIndex]
	identityFD := -1
	published := false
	if identityObject.Inode == 0 {
		if identityNameExists(parent, identityName) || discardUnboundResourceIdentityStage(parent, prepared.StageName) != nil {
			return nil, syscall.Stat_t{}, ErrIntegrity
		}
		identityFD, err = syscall.Openat(parent, prepared.StageName,
			syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return nil, syscall.Stat_t{}, ErrIntegrity
		}
		resourceBootstrapBoundaryHook(hookName, "identity-created")
		var stat syscall.Stat_t
		if syscall.Fstat(identityFD, &stat) != nil || validateResourceFile(stat) != nil || stat.Size != 0 {
			_ = syscall.Close(identityFD)
			return nil, syscall.Stat_t{}, ErrIntegrity
		}
		prepared.Objects[prepared.IdentityIndex].Device = uint64(stat.Dev)
		prepared.Objects[prepared.IdentityIndex].Inode = stat.Ino
		next := authority
		next.PreparedIdentity = &prepared
		authorityData, err = g.replaceInitializingResourceRootGeneration(authorityData, next, hookName, "identity-bound")
		if err != nil {
			_ = syscall.Close(identityFD)
			return nil, syscall.Stat_t{}, err
		}
		authority = next
	} else {
		identityFD, published, err = openBoundResourceIdentityStage(parent, prepared)
		if err != nil {
			return nil, syscall.Stat_t{}, err
		}
	}

	file := os.NewFile(uintptr(identityFD), prepared.StageName)
	stat, err := resumeResourceIdentityPublication(parent, file, prepared, data, hookName, published)
	if err != nil {
		_ = file.Close()
		return nil, syscall.Stat_t{}, err
	}
	if authority.PreparedIdentity == nil || !reflect.DeepEqual(*authority.PreparedIdentity, prepared) {
		_ = file.Close()
		return nil, syscall.Stat_t{}, ErrIntegrity
	}
	next := authority
	next.Objects = append(append([]rootGenerationObjectIdentityV1(nil), authority.Objects...), prepared.Objects...)
	next.PreparedIdentity = nil
	if _, err := g.replaceInitializingResourceRootGeneration(authorityData, next, hookName, "identity-committed"); err != nil {
		_ = file.Close()
		return nil, syscall.Stat_t{}, err
	}
	return file, stat, nil
}

func resourceIdentityPublicationStart(objects []rootGenerationObjectIdentityV1) (int, error) {
	schema := resourceRootGenerationSchema()
	if len(objects) == 0 || len(objects) > len(schema) {
		return 0, ErrIntegrity
	}
	for start := 0; start+len(objects) <= len(schema); start++ {
		match := true
		for index, object := range objects {
			expected := schema[start+index]
			if object.Parent != expected.Parent || object.Name != expected.Name || object.ObjectType != expected.ObjectType {
				match = false
				break
			}
		}
		if match {
			return start, nil
		}
	}
	return 0, ErrIntegrity
}

func (g *resourceGuard) replaceInitializingResourceRootGeneration(prior []byte, next rootGenerationAuthorityV1,
	identityName, phase string) ([]byte, error) {
	data, err := encodeResourceRootGeneration(next)
	if err != nil || next.State != rootGenerationInitializing {
		return nil, ErrIntegrity
	}
	observed, found, err := fgetRootXattr(g.rootFD, resourceRootAuthorityXattr)
	if err != nil || !found || !bytes.Equal(observed, prior) ||
		fsetRootXattr(g.rootFD, resourceRootAuthorityXattr, data, rootGenerationXattrReplace) != nil {
		return nil, ErrIntegrity
	}
	resourceBootstrapBoundaryHook(identityName, phase+"-visible")
	if syscall.Fsync(g.rootFD) != nil || verifyRootGenerationData(g.rootFD, resourceRootAuthorityXattr, data) != nil {
		return nil, ErrIntegrity
	}
	resourceBootstrapBoundaryHook(identityName, phase+"-durable")
	g.rootAuthorityData = append([]byte(nil), data...)
	return data, nil
}

func discardUnboundResourceIdentityStage(parent int, name string) error {
	fd, err := syscall.Openat(parent, name, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err == syscall.ENOENT {
		return nil
	}
	var stat syscall.Stat_t
	if err != nil || syscall.Fstat(fd, &stat) != nil || validateResourceFile(stat) != nil || stat.Size != 0 ||
		verifyResourceFile(parent, name, fd, uint64(stat.Dev), stat.Ino) != nil {
		if fd >= 0 {
			_ = syscall.Close(fd)
		}
		return ErrIntegrity
	}
	if syscall.Close(fd) != nil || syscall.Unlinkat(parent, name) != nil || syscall.Fsync(parent) != nil {
		return ErrIntegrity
	}
	return nil
}

func openBoundResourceIdentityStage(parent int, prepared preparedIdentityPublicationV1) (int, bool, error) {
	identity := prepared.Objects[prepared.IdentityIndex]
	openExact := func(name string) (int, bool, error) {
		fd, err := syscall.Openat(parent, name, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err == syscall.ENOENT {
			return -1, false, nil
		}
		if err != nil || verifyResourceFile(parent, name, fd, identity.Device, identity.Inode) != nil {
			if fd >= 0 {
				_ = syscall.Close(fd)
			}
			return -1, false, ErrIntegrity
		}
		return fd, true, nil
	}
	stageFD, stageFound, err := openExact(prepared.StageName)
	if err != nil {
		return -1, false, err
	}
	finalFD, finalFound, err := openExact(prepared.IdentityName)
	if err != nil {
		if stageFound {
			_ = syscall.Close(stageFD)
		}
		return -1, false, err
	}
	if stageFound == finalFound {
		if stageFound {
			_ = syscall.Close(stageFD)
			_ = syscall.Close(finalFD)
		}
		return -1, false, ErrIntegrity
	}
	if finalFound {
		return finalFD, true, nil
	}
	return stageFD, false, nil
}

func resumeResourceIdentityPublication(parent int, file *os.File, prepared preparedIdentityPublicationV1, data []byte,
	identityName string, published bool) (syscall.Stat_t, error) {
	identity := prepared.Objects[prepared.IdentityIndex]
	var stat syscall.Stat_t
	if syscall.Fstat(int(file.Fd()), &stat) != nil || validateResourceFile(stat) != nil || uint64(stat.Dev) != identity.Device ||
		stat.Ino != identity.Inode || stat.Size < 0 || stat.Size > int64(len(data)) {
		return syscall.Stat_t{}, ErrIntegrity
	}
	prefix := make([]byte, int(stat.Size))
	if len(prefix) > 0 {
		if count, err := file.ReadAt(prefix, 0); err != nil || count != len(prefix) || !bytes.Equal(prefix, data[:len(prefix)]) {
			return syscall.Stat_t{}, ErrIntegrity
		}
	}
	if published && stat.Size != int64(len(data)) {
		return syscall.Stat_t{}, ErrIntegrity
	}
	if !published && stat.Size < int64(len(data)) {
		if _, err := file.Seek(stat.Size, 0); err != nil {
			return syscall.Stat_t{}, ErrIntegrity
		}
		split := len(data) / 2
		if split == 0 {
			split = 1
		}
		written := int(stat.Size)
		if written < split {
			if err := writeFull(file, data[written:split]); err != nil {
				return syscall.Stat_t{}, ErrIntegrity
			}
			written = split
			resourceBootstrapBoundaryHook(identityName, "identity-partial")
		}
		if written < len(data) {
			if err := writeFull(file, data[written:]); err != nil {
				return syscall.Stat_t{}, ErrIntegrity
			}
		}
		resourceBootstrapBoundaryHook(identityName, "identity-complete-visible")
	}
	if file.Sync() != nil {
		return syscall.Stat_t{}, ErrIntegrity
	}
	resourceBootstrapBoundaryHook(identityName, "identity-file-durable")
	if syscall.Fsync(parent) != nil {
		return syscall.Stat_t{}, ErrIntegrity
	}
	resourceBootstrapBoundaryHook(identityName, "identity-stage-durable")
	if !published {
		if syscall.Renameat(parent, prepared.StageName, parent, prepared.IdentityName) != nil ||
			verifyResourceFile(parent, prepared.IdentityName, int(file.Fd()), identity.Device, identity.Inode) != nil {
			return syscall.Stat_t{}, ErrIntegrity
		}
		resourceBootstrapBoundaryHook(identityName, "identity-published")
		if syscall.Fsync(parent) != nil {
			return syscall.Stat_t{}, ErrIntegrity
		}
		resourceBootstrapBoundaryHook(identityName, "identity-publication-durable")
	}
	return verifyResourceIdentityPublicationBytes(file, data)
}

func verifyResourceIdentityPublicationBytes(file *os.File, data []byte) (syscall.Stat_t, error) {
	var stat syscall.Stat_t
	if file == nil || syscall.Fstat(int(file.Fd()), &stat) != nil || validateResourceFile(stat) != nil || stat.Size != int64(len(data)) {
		return syscall.Stat_t{}, ErrIntegrity
	}
	observed := make([]byte, len(data))
	if count, err := file.ReadAt(observed, 0); err != nil || count != len(observed) || !bytes.Equal(observed, data) {
		return syscall.Stat_t{}, ErrIntegrity
	}
	return stat, nil
}

func closeResourceSlotSet(set *resourceSlotSet) error {
	if set == nil {
		return nil
	}
	var resultErr error
	if set.identityFile != nil {
		resultErr = set.identityFile.Close()
		set.identityFile = nil
	}
	for index := len(set.slots) - 1; index >= 0; index-- {
		if set.slots[index] != nil && set.slots[index].file != nil {
			resultErr = errors.Join(resultErr, set.slots[index].file.Close())
			set.slots[index].file = nil
		}
	}
	set.slots = nil
	return resultErr
}

func ensureResourceSlots(parent, total int, allowCreate bool) error {
	for slot := 0; slot < total; slot++ {
		name := resourceSlotName(slot)
		flags := syscall.O_RDWR | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
		if allowCreate {
			flags |= syscall.O_CREAT | syscall.O_EXCL
		}
		fd, err := syscall.Openat(parent, name, flags, 0o600)
		created := allowCreate && err == nil
		if allowCreate && err == syscall.EEXIST {
			fd, err = syscall.Openat(parent, name, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		}
		if err != nil {
			return ErrIntegrity
		}
		var stat syscall.Stat_t
		if syscall.Fstat(fd, &stat) != nil || validateResourceFile(stat) != nil || verifyResourceFile(parent, name, fd, stat.Dev, stat.Ino) != nil {
			syscall.Close(fd)
			return ErrIntegrity
		}
		if created && syscall.Fsync(fd) != nil {
			syscall.Close(fd)
			return ErrIntegrity
		}
		if err := syscall.Close(fd); err != nil {
			return ErrIntegrity
		}
	}
	if syscall.Fsync(parent) != nil {
		return ErrIntegrity
	}
	return nil
}

func verifyResourceSlotSet(parent int, set *resourceSlotSet) error {
	if set == nil || set.identityFile == nil || len(set.slots) == 0 || len(set.identityData) == 0 || len(set.identityData) > resourceSlotIdentityMaxBytes {
		return ErrIntegrity
	}
	dup, err := syscall.Openat(parent, ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ErrIntegrity
	}
	directory := os.NewFile(uintptr(dup), "read-model-resource-slots")
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil || len(entries) != len(set.slots)+1 {
		return ErrIntegrity
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return ErrIntegrity
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if names[0] != resourceSlotIdentityName {
		return ErrIntegrity
	}
	for slot, name := range names[1:] {
		if name != resourceSlotName(slot) || set.slots[slot] == nil || set.slots[slot].name != name {
			return ErrIntegrity
		}
		if err := verifyResourceSlotPin(parent, set.slots[slot]); err != nil {
			return ErrIntegrity
		}
	}
	identityFD := int(set.identityFile.Fd())
	var identityStat syscall.Stat_t
	if syscall.Fstat(identityFD, &identityStat) != nil || validateResourceFile(identityStat) != nil ||
		uint64(identityStat.Dev) != set.identityDev || identityStat.Ino != set.identityIno || identityStat.Size != int64(len(set.identityData)) {
		return ErrIntegrity
	}
	if err := verifyResourceFile(parent, resourceSlotIdentityName, identityFD, set.identityDev, set.identityIno); err != nil {
		return err
	}
	observed := make([]byte, len(set.identityData))
	if count, err := set.identityFile.ReadAt(observed, 0); err != nil || count != len(observed) || string(observed) != string(set.identityData) {
		return ErrIntegrity
	}
	return nil
}

func verifyResourceSlotPin(parent int, pin *resourceSlotPin) error {
	if pin == nil || pin.file == nil || pin.name == "" {
		return ErrIntegrity
	}
	return verifyResourceFile(parent, pin.name, int(pin.file.Fd()), pin.dev, pin.ino)
}

func resourceSlotName(slot int) string { return fmt.Sprintf("slot-%02d.lock", slot) }

func resourceDirectoryIdentity(fd int) (uint64, uint64, error) {
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || validateDirectoryFD(fd, false) != nil {
		return 0, 0, ErrIntegrity
	}
	return uint64(stat.Dev), stat.Ino, nil
}

func verifyResourceDirectoryIdentity(fd int, dev uint64, ino uint64) error {
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || validateDirectoryFD(fd, false) != nil || uint64(stat.Dev) != dev || stat.Ino != ino {
		return ErrIntegrity
	}
	return nil
}

func verifyNamedResourceDirectory(parent int, name string, pinned int, dev uint64, ino uint64) error {
	if err := verifyResourceDirectoryIdentity(pinned, dev, ino); err != nil {
		return err
	}
	fd, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ErrIntegrity
	}
	defer syscall.Close(fd)
	return verifyResourceDirectoryIdentity(fd, dev, ino)
}

func validateResourceFile(stat syscall.Stat_t) error {
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Mode&0o777 != 0o600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return ErrIntegrity
	}
	return nil
}

func verifyResourceFile(parent int, name string, fd int, dev uint64, ino uint64) error {
	var current syscall.Stat_t
	if syscall.Fstat(fd, &current) != nil || validateResourceFile(current) != nil || uint64(current.Dev) != dev || current.Ino != ino {
		return ErrIntegrity
	}
	reopened, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ErrIntegrity
	}
	defer syscall.Close(reopened)
	var named syscall.Stat_t
	if syscall.Fstat(reopened, &named) != nil || validateResourceFile(named) != nil || named.Dev != current.Dev || named.Ino != current.Ino {
		return ErrIntegrity
	}
	return nil
}

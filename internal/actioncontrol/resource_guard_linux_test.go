//go:build linux

package actioncontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

func TestResourceGuardCreatesExactProtectedSlotSets(t *testing.T) {
	root := newResourceGuardRoot(t)
	guard, err := openResourceGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()

	guardPath := filepath.Join(root, resourceGuardDirectory)
	assertProtectedDirectory(t, guardPath)
	assertResourceDirectoryAnchor(t, root, resourceGuardDirectory)
	guardEntries, err := os.ReadDir(guardPath)
	if err != nil || len(guardEntries) != 4 {
		t.Fatalf("guard entries = %d, err=%v", len(guardEntries), err)
	}
	for name, total := range map[string]int{"ledger": GlobalLedgerSlots, "snapshot": GlobalSnapshotSlots} {
		directory := filepath.Join(guardPath, name)
		assertProtectedDirectory(t, directory)
		assertResourceDirectoryAnchor(t, guardPath, name)
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != total+1 {
			t.Fatalf("%s slot entries = %d, err=%v", name, len(entries), err)
		}
		identityInfo, err := os.Lstat(filepath.Join(directory, resourceSlotIdentityName))
		if err != nil || !identityInfo.Mode().IsRegular() || identityInfo.Mode().Perm() != 0o600 {
			t.Fatalf("unsafe %s slot identity: info=%v err=%v", name, identityInfo, err)
		}
		for slot := 0; slot < total; slot++ {
			path := filepath.Join(directory, resourceSlotName(slot))
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
				t.Fatalf("unsafe %s slot %d: info=%v err=%v", name, slot, info, err)
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Nlink != 1 {
				t.Fatalf("%s slot %d link count = %v", name, slot, stat)
			}
		}
	}
}

func TestResourceGuardRootGenerationAuthorityIsDurableAndNonReissuable(t *testing.T) {
	root := newResourceGuardRoot(t)
	guard, err := openResourceGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	authorityData, found, err := fgetRootXattr(guard.rootFD, resourceRootAuthorityXattr)
	if err != nil || !found {
		_ = guard.Close()
		t.Fatalf("resource root authority found=%v err=%v", found, err)
	}
	authority, canonical, established, err := decodeResourceRootGeneration(authorityData, guard.rootDev, guard.rootIno)
	if err != nil || !established || authority.State != rootGenerationEstablished || len(authority.Objects) != 20 ||
		len(authorityData) != resourceRootGenerationBinarySize || !bytes.Equal(canonical, authorityData) {
		_ = guard.Close()
		t.Fatalf("resource root authority = %+v, established=%v err=%v", authority, established, err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := openResourceGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	observed, found, err := fgetRootXattr(reopened.rootFD, resourceRootAuthorityXattr)
	if err != nil || !found || !bytes.Equal(observed, authorityData) {
		_ = reopened.Close()
		t.Fatalf("resource root authority changed across reopen: found=%v err=%v", found, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	guardPath := filepath.Join(root, resourceGuardDirectory)
	if err := os.Rename(guardPath, guardPath+"-original"); err != nil {
		t.Fatal(err)
	}
	guardAnchor := filepath.Join(root, resourceDirectoryAnchorName(resourceGuardDirectory))
	if err := os.Rename(guardAnchor, guardAnchor+"-original"); err != nil {
		t.Fatal(err)
	}
	fresh, err := openResourceGuard(root)
	if fresh != nil {
		_ = fresh.Close()
		t.Fatal("paired resource namespace loss issued a new generation")
	}
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("paired resource namespace loss error = %v", err)
	}
	if _, err := os.Lstat(guardPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("paired resource namespace loss recreated guard: %v", err)
	}
}

func TestResourceGuardRootGenerationAuthorityConcurrentFirstOpen(t *testing.T) {
	root := newResourceGuardRoot(t)
	const contenders = 8
	start := make(chan struct{})
	results := make(chan error, contenders)
	for range contenders {
		go func() {
			<-start
			guard, err := openResourceGuard(root)
			if err == nil {
				err = guard.Close()
			}
			results <- err
		}()
	}
	close(start)
	for range contenders {
		if err := <-results; err != nil {
			t.Fatalf("concurrent first resource guard open failed: %v", err)
		}
	}
	guard, err := openResourceGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	authority, found, err := fgetRootXattr(guard.rootFD, resourceRootAuthorityXattr)
	if err != nil || !found || !bytes.Equal(authority, guard.rootAuthorityData) {
		t.Fatalf("concurrent resource authority found=%v err=%v", found, err)
	}
}

func TestResourceGuardBootstrapCrashHelper(t *testing.T) {
	root := os.Getenv("ABCP_RESOURCE_BOOTSTRAP_CRASH_ROOT")
	if root == "" {
		return
	}
	target := os.Getenv("ABCP_RESOURCE_BOOTSTRAP_CRASH_IDENTITY")
	boundary := os.Getenv("ABCP_RESOURCE_BOOTSTRAP_CRASH_BOUNDARY")
	resourceBootstrapBoundaryHook = func(identity, observed string) {
		if identity == target && observed == boundary {
			os.Exit(90)
		}
	}
	guard, err := openResourceGuard(root)
	if err != nil {
		os.Exit(91)
	}
	_ = guard.Close()
	os.Exit(92)
}

func TestResourceGuardInitialIdentityBootstrapRecoversEveryCrashBoundary(t *testing.T) {
	identities := []string{
		"./" + resourceDirectoryAnchorName(resourceGuardDirectory),
		resourceGuardDirectory + "/" + resourceDirectoryAnchorName("ledger"),
		resourceGuardDirectory + "/" + resourceDirectoryAnchorName("snapshot"),
		resourceGuardDirectory + "/ledger/" + resourceSlotIdentityName,
		resourceGuardDirectory + "/snapshot/" + resourceSlotIdentityName,
	}
	boundaries := []string{
		"intent-visible", "intent-durable", "identity-created", "identity-bound-visible", "identity-bound-durable",
		"identity-partial", "identity-complete-visible", "identity-file-durable", "identity-stage-durable",
		"identity-published", "identity-publication-durable", "identity-committed-visible", "identity-committed-durable",
	}
	for _, identity := range identities {
		for _, boundary := range boundaries {
			t.Run(identity+"/"+boundary, func(t *testing.T) {
				root := newResourceGuardRoot(t)
				runResourceBootstrapCrashHelper(t, root, identity, boundary)
				authorizedInode := resourceInitializingIdentityInode(t, root, identity)

				guard, err := openResourceGuard(root)
				if err != nil {
					t.Fatalf("recover initial resource identity %s at %s: %v", identity, boundary, err)
				}
				firstAuthority := append([]byte(nil), guard.rootAuthorityData...)
				if err := guard.Close(); err != nil {
					t.Fatal(err)
				}
				info, err := os.Lstat(resourceBootstrapIdentityPath(root, identity))
				if err != nil {
					t.Fatal(err)
				}
				stat, ok := info.Sys().(*syscall.Stat_t)
				if !ok || stat.Nlink != 1 || authorizedInode != 0 && stat.Ino != authorizedInode {
					t.Fatalf("recovered resource identity inode %s = %+v, authorized=%d", identity, stat, authorizedInode)
				}
				assertNoPreparedIdentityFiles(t, root)

				reopened, err := openResourceGuard(root)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(reopened.rootAuthorityData, firstAuthority) {
					_ = reopened.Close()
					t.Fatal("repeated startup changed the ready resource generation")
				}
				if err := reopened.Close(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestResourceGuardInitializingIdentityRejectsForeignReplacementAndMismatch(t *testing.T) {
	target := "./" + resourceDirectoryAnchorName(resourceGuardDirectory)
	for _, attack := range []struct {
		name     string
		boundary string
		mutate   func(*testing.T, string)
	}{
		{"foreign-final", "intent-durable", func(t *testing.T, root string) {
			if err := os.WriteFile(resourceBootstrapIdentityPath(root, target), []byte("foreign"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"replaced-stage", "identity-bound-durable", func(t *testing.T, root string) {
			stage := resourceBootstrapIdentityPath(root, target) + ".prepared"
			if err := os.Rename(stage, stage+".detached"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stage, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"mismatched-prefix", "identity-partial", func(t *testing.T, root string) {
			stage := resourceBootstrapIdentityPath(root, target) + ".prepared"
			file, err := os.OpenFile(stage, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteAt([]byte("x"), 0); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(attack.name, func(t *testing.T) {
			root := newResourceGuardRoot(t)
			runResourceBootstrapCrashHelper(t, root, target, attack.boundary)
			attack.mutate(t, root)
			guard, err := openResourceGuard(root)
			if guard != nil {
				_ = guard.Close()
				t.Fatal("tampered initializing resource generation opened")
			}
			if !errors.Is(err, ErrIntegrity) {
				t.Fatalf("tampered initializing resource error = %v", err)
			}
		})
	}
}

func runResourceBootstrapCrashHelper(t *testing.T, root, identity, boundary string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestResourceGuardBootstrapCrashHelper$")
	command.Env = append(os.Environ(),
		"ABCP_RESOURCE_BOOTSTRAP_CRASH_ROOT="+root,
		"ABCP_RESOURCE_BOOTSTRAP_CRASH_IDENTITY="+identity,
		"ABCP_RESOURCE_BOOTSTRAP_CRASH_BOUNDARY="+boundary,
	)
	output, err := command.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 90 {
		t.Fatalf("resource bootstrap crash helper %s/%s exit=%v output=%s", identity, boundary, err, output)
	}
}

func resourceInitializingIdentityInode(t *testing.T, root, identity string) uint64 {
	t.Helper()
	fd, err := openAbsoluteDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	dev, ino, err := resourceDirectoryIdentity(fd)
	if err != nil {
		t.Fatal(err)
	}
	authority, _, found, err := readResourceRootGenerationAuthority(fd, dev, ino)
	if err != nil || !found {
		t.Fatalf("read initializing resource authority: found=%v err=%v", found, err)
	}
	if authority.PreparedIdentity != nil && authority.PreparedIdentity.Parent+"/"+authority.PreparedIdentity.IdentityName == identity {
		return authority.PreparedIdentity.Objects[authority.PreparedIdentity.IdentityIndex].Inode
	}
	for _, object := range authority.Objects {
		if object.Parent+"/"+object.Name == identity {
			return object.Inode
		}
	}
	return 0
}

func resourceBootstrapIdentityPath(root, identity string) string {
	return filepath.Join(root, strings.TrimPrefix(identity, "./"))
}

func TestResourceGuardFreshOpenRejectsAnchoredDirectoryReplacement(t *testing.T) {
	for _, name := range []string{resourceGuardDirectory, "ledger", "snapshot"} {
		t.Run(name, func(t *testing.T) {
			root := newResourceGuardRoot(t)
			guard, err := openResourceGuard(root)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, resourceGuardDirectory, name)
			if name == resourceGuardDirectory {
				path = filepath.Join(root, name)
			}
			if err := os.Rename(path, path+"-original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}

			fresh, err := openResourceGuard(root)
			if fresh != nil {
				_ = fresh.Close()
				t.Fatal("fresh guard accepted a replacement directory generation")
			}
			if !errors.Is(err, ErrIntegrity) {
				t.Fatalf("fresh guard replacement error = %v", err)
			}
			entries, readErr := os.ReadDir(path)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("replacement directory gained resource capacity: entries=%d err=%v", len(entries), readErr)
			}
			if err := guard.Close(); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("original guard close after replacement = %v", err)
			}
		})
	}
}

func TestResourceGuardFreshOpenRejectsAnchoredDirectorySymlink(t *testing.T) {
	for _, name := range []string{resourceGuardDirectory, "ledger", "snapshot"} {
		t.Run(name, func(t *testing.T) {
			root := newResourceGuardRoot(t)
			guard, err := openResourceGuard(root)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, resourceGuardDirectory, name)
			if name == resourceGuardDirectory {
				path = filepath.Join(root, name)
			}
			original := path + "-original"
			if err := os.Rename(path, original); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Base(original), path); err != nil {
				t.Fatal(err)
			}

			fresh, err := openResourceGuard(root)
			if fresh != nil {
				_ = fresh.Close()
				t.Fatal("fresh guard followed a replacement directory symlink")
			}
			if !errors.Is(err, ErrIntegrity) {
				t.Fatalf("fresh guard symlink error = %v", err)
			}
			if err := guard.Close(); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("original guard close after symlink replacement = %v", err)
			}
		})
	}
}

func TestResourceGuardFreshOpenRejectsHardLinkedDirectoryAnchor(t *testing.T) {
	for _, name := range []string{resourceGuardDirectory, "ledger", "snapshot"} {
		t.Run(name, func(t *testing.T) {
			root := newResourceGuardRoot(t)
			guard, err := openResourceGuard(root)
			if err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(root, resourceGuardDirectory)
			if name == resourceGuardDirectory {
				parent = root
			}
			anchor := filepath.Join(parent, resourceDirectoryAnchorName(name))
			if err := os.Link(anchor, anchor+"-hardlink"); err != nil {
				t.Fatal(err)
			}

			fresh, err := openResourceGuard(root)
			if fresh != nil {
				_ = fresh.Close()
				t.Fatal("fresh guard accepted a hard-linked directory anchor")
			}
			if !errors.Is(err, ErrIntegrity) {
				t.Fatalf("fresh guard hard-linked anchor error = %v", err)
			}
			if err := guard.Close(); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("original guard close after anchor hardlink = %v", err)
			}
		})
	}
}

func TestResourceGuardRevalidatesAnchoredDirectoriesBeforeReleaseAndClose(t *testing.T) {
	root := newResourceGuardRoot(t)
	guard, err := openResourceGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := guard.acquire(context.Background(), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, resourceGuardDirectory, "ledger")
	if err := os.Rename(path, path+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("active guard close after namespace replacement = %v", err)
	}
	if err := lease.Close(); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("lease release after namespace replacement = %v", err)
	}
}

type resourcePairedGenerationAttack struct {
	name      string
	directory string
	resource  string
	slot      int
	ledgers   int
	snapshots int
}

func TestResourceGuardCrossProcessPairedGenerationReplacementIsNonReissuable(t *testing.T) {
	for _, attack := range []resourcePairedGenerationAttack{
		{name: "top-guard-and-anchor", directory: resourceGuardDirectory, slot: -1, ledgers: GlobalLedgerSlots, snapshots: GlobalSnapshotSlots},
		{name: "ledger-directory-and-anchor", directory: "ledger", slot: -1, ledgers: GlobalLedgerSlots},
		{name: "snapshot-directory-and-anchor", directory: "snapshot", slot: -1, snapshots: GlobalSnapshotSlots},
		{name: "held-ledger-slot-and-manifest", resource: "ledger", slot: 0, ledgers: GlobalLedgerSlots},
		{name: "free-ledger-slot-and-manifest", resource: "ledger", slot: GlobalLedgerSlots - 1, ledgers: GlobalLedgerSlots - 1},
		{name: "held-snapshot-slot-and-manifest", resource: "snapshot", slot: 0, snapshots: GlobalSnapshotSlots},
		{name: "free-snapshot-slot-and-manifest", resource: "snapshot", slot: GlobalSnapshotSlots - 1, snapshots: GlobalSnapshotSlots - 1},
	} {
		t.Run(attack.name, func(t *testing.T) {
			root := newResourceGuardRoot(t)
			guard, err := openResourceGuard(root)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := guard.acquire(context.Background(), attack.ledgers, attack.snapshots)
			if err != nil {
				_ = guard.Close()
				t.Fatal(err)
			}
			defer func() {
				if lease != nil {
					_ = lease.Close()
				}
				if guard != nil {
					_ = guard.Close()
				}
			}()

			authorityBefore := append([]byte(nil), guard.rootAuthorityData...)
			restore, verifyReplacement := installResourcePairedGenerationAttack(t, root, attack)
			verifyReplacement()

			// Process A keeps the original slot descriptors and locks while a
			// fresh process B repeatedly tries to adopt the locally coherent
			// replacement generation.
			if got, want := len(lease.files), attack.ledgers+attack.snapshots; got != want {
				t.Fatalf("original-generation held slots = %d, want %d", got, want)
			}
			runResourceOpenIntegrityHelper(t, root)
			verifyReplacement()
			observed, found, err := fgetRootXattr(guard.rootFD, resourceRootAuthorityXattr)
			if err != nil || !found || !bytes.Equal(observed, authorityBefore) {
				t.Fatalf("paired attack changed resource root authority: found=%v err=%v", found, err)
			}

			restore()
			if err := lease.Close(); err != nil {
				t.Fatalf("release restored original generation: %v", err)
			}
			lease = nil
			if err := guard.Close(); err != nil {
				t.Fatalf("close restored original generation: %v", err)
			}
			guard = nil

			// Failed process-B opens must leak neither descriptors nor locks:
			// after restoration a fresh guard can acquire the complete 8/4 set.
			proof, err := openResourceGuard(root)
			if err != nil {
				t.Fatal(err)
			}
			full, err := proof.acquire(context.Background(), GlobalLedgerSlots, GlobalSnapshotSlots)
			if err != nil {
				_ = proof.Close()
				t.Fatalf("paired replacement leaked original resource capacity: %v", err)
			}
			if err := full.Close(); err != nil {
				t.Fatal(err)
			}
			if err := proof.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResourceGuardCrossProcessDirectoryReplacementFailsClosed(t *testing.T) {
	for _, directory := range []string{resourceGuardDirectory, "ledger", "snapshot"} {
		for _, holdSlots := range []bool{false, true} {
			mode := "before-fresh-open"
			if holdSlots {
				mode = "while-slots-held"
			}
			t.Run(directory+"/"+mode, func(t *testing.T) {
				root := newResourceGuardRoot(t)
				guard, err := openResourceGuard(root)
				if err != nil {
					t.Fatal(err)
				}
				defer guard.Close()

				var lease *resourceLease
				if holdSlots {
					ledgers, snapshots := 0, 0
					switch directory {
					case resourceGuardDirectory:
						ledgers, snapshots = GlobalLedgerSlots, GlobalSnapshotSlots
					case "ledger":
						ledgers = GlobalLedgerSlots
					case "snapshot":
						snapshots = GlobalSnapshotSlots
					}
					lease, err = guard.acquire(context.Background(), ledgers, snapshots)
					if err != nil {
						t.Fatal(err)
					}
					defer lease.Close()
				} else if err := guard.Close(); err != nil {
					t.Fatal(err)
				}

				path := filepath.Join(root, resourceGuardDirectory, directory)
				if directory == resourceGuardDirectory {
					path = filepath.Join(root, directory)
				}
				original := path + "-original"
				if err := os.Rename(path, original); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}

				runResourceOpenIntegrityHelper(t, root)
				entries, err := os.ReadDir(path)
				if err != nil || len(entries) != 0 {
					t.Fatalf("replacement %s directory gained resource capacity: entries=%d err=%v", directory, len(entries), err)
				}

				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(original, path); err != nil {
					t.Fatal(err)
				}
				if lease != nil {
					if err := lease.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if err := guard.Close(); err != nil {
					t.Fatal(err)
				}

				proof, err := openResourceGuard(root)
				if err != nil {
					t.Fatalf("open after rejected replacement process: %v", err)
				}
				full, err := proof.acquire(context.Background(), GlobalLedgerSlots, GlobalSnapshotSlots)
				if err != nil {
					_ = proof.Close()
					t.Fatalf("failed fresh process leaked resource slots: %v", err)
				}
				if err := full.Close(); err != nil {
					t.Fatal(err)
				}
				if err := proof.Close(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestResourceGuardCrossProcessHeldAndFreeSlotReplacementFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name        string
		kind        string
		capacity    int
		replacement string
	}{
		{"ledger-held", "ledger", GlobalLedgerSlots, "held"},
		{"ledger-free", "ledger", GlobalLedgerSlots, "free"},
		{"snapshot-held", "snapshot", GlobalSnapshotSlots, "held"},
		{"snapshot-free", "snapshot", GlobalSnapshotSlots, "free"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := newResourceGuardRoot(t)
			guard, err := openResourceGuard(root)
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Close()

			heldCount := test.capacity
			replacedSlot := 0
			if test.replacement == "free" {
				heldCount--
				replacedSlot = test.capacity - 1
			}
			ledgers, snapshots := 0, 0
			if test.kind == "ledger" {
				ledgers = heldCount
			} else {
				snapshots = heldCount
			}
			lease, err := guard.acquire(context.Background(), ledgers, snapshots)
			if err != nil {
				t.Fatal(err)
			}

			set := guard.ledgerSlots
			if test.kind == "snapshot" {
				set = guard.snapshotSlots
			}
			path := filepath.Join(root, resourceGuardDirectory, test.kind, resourceSlotName(replacedSlot))
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || uint64(stat.Dev) == set.slots[replacedSlot].dev && stat.Ino == set.slots[replacedSlot].ino {
				t.Fatalf("%s slot replacement retained pinned identity: %+v", test.kind, stat)
			}

			runResourceIntegrityHelper(t, root, test.kind)

			requestLedgers, requestSnapshots := 0, 0
			if test.kind == "ledger" {
				requestLedgers = 1
			} else {
				requestSnapshots = 1
			}
			if _, err := guard.acquire(context.Background(), requestLedgers, requestSnapshots); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("local %s acquisition after %s-slot replacement = %v", test.kind, test.replacement, err)
			}
			if err := lease.Close(); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("%s lease release after %s-slot replacement = %v", test.kind, test.replacement, err)
			}
		})
	}
}

func TestResourceGuardFailsClosedOnSlotAndDirectoryReplacement(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(*testing.T, string)
	}{
		{"symlink", func(t *testing.T, root string) {
			directory := filepath.Join(root, resourceGuardDirectory, "ledger")
			if err := os.Remove(filepath.Join(directory, resourceSlotName(7))); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(resourceSlotName(0), filepath.Join(directory, resourceSlotName(7))); err != nil {
				t.Fatal(err)
			}
		}},
		{"hardlink", func(t *testing.T, root string) {
			directory := filepath.Join(root, resourceGuardDirectory, "snapshot")
			if err := os.Remove(filepath.Join(directory, resourceSlotName(3))); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(filepath.Join(directory, resourceSlotName(0)), filepath.Join(directory, resourceSlotName(3))); err != nil {
				t.Fatal(err)
			}
		}},
		{"mode", func(t *testing.T, root string) {
			path := filepath.Join(root, resourceGuardDirectory, "ledger", resourceSlotName(4))
			if err := os.Chmod(path, 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{"directory identity", func(t *testing.T, root string) {
			path := filepath.Join(root, resourceGuardDirectory, "snapshot")
			if err := os.Rename(path, path+"-replaced"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newResourceGuardRoot(t)
			guard, err := openResourceGuard(root)
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Close()
			test.tamper(t, root)
			if _, err := guard.acquire(context.Background(), 1, 1); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("tampered guard acquisition error = %v", err)
			}
		})
	}
}

func TestResourceGuardCancellationReleasesAtomicBundle(t *testing.T) {
	root := newResourceGuardRoot(t)
	first, err := openResourceGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := openResourceGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	full, err := first.acquire(context.Background(), GlobalLedgerSlots, GlobalSnapshotSlots)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := second.acquire(ctx, 1, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled acquisition error = %v", err)
	}
	if err := full.Close(); err != nil {
		t.Fatal(err)
	}
	reacquired, err := second.acquire(context.Background(), GlobalLedgerSlots, GlobalSnapshotSlots)
	if err != nil {
		t.Fatalf("slots leaked after release: %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGuardedReadSeamsReserveGlobalLedgerAndSnapshot(t *testing.T) {
	root := newResourceGuardRoot(t)
	blocker, err := openResourceGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	held, err := blocker.acquire(context.Background(), 0, GlobalSnapshotSlots)
	if err != nil {
		t.Fatal(err)
	}
	base := &blockingGuardedReadModel{entered: make(chan struct{}, 3), release: make(chan struct{})}
	close(base.release)
	model, err := NewGuardedReadModel(root, base)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	operations := []func(context.Context) error{
		func(ctx context.Context) error { _, err := model.Snapshot(ctx, "run"); return err },
		func(ctx context.Context) error { _, err := model.ReadRunProjection(ctx, "run"); return err },
		func(ctx context.Context) error {
			_, err := model.ReadEventProjection(ctx, "run", serviceapi.PageRequestV1{PageSize: 1})
			return err
		},
	}
	for index, operation := range operations {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		err := operation(ctx)
		cancel()
		if !errors.Is(err, serviceapi.ErrAuthoritativeReadBusy) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("guarded read seam %d error = %v", index, err)
		}
	}
	if base.calls.Load() != 0 {
		t.Fatalf("base read model entered without a global snapshot slot %d times", base.calls.Load())
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	for index, operation := range operations {
		if err := operation(context.Background()); err != nil {
			t.Fatalf("guarded read seam %d after release: %v", index, err)
		}
	}
	if base.calls.Load() != int32(len(operations)) {
		t.Fatalf("base read calls = %d", base.calls.Load())
	}
}

func TestGuardedWritableSnapshotBundleDoesNotDeadlockAtCeilings(t *testing.T) {
	root := newResourceGuardRoot(t)
	base := &blockingGuardedReadModel{entered: make(chan struct{}, GlobalSnapshotSlots), release: make(chan struct{})}
	models := make([]*GuardedReadModel, 0, GlobalSnapshotSlots+1)
	for index := 0; index < GlobalSnapshotSlots+1; index++ {
		model, err := NewGuardedReadModel(root, base)
		if err != nil {
			t.Fatal(err)
		}
		models = append(models, model)
		defer model.Close()
	}

	errorsOut := make(chan error, GlobalSnapshotSlots)
	for index := 0; index < GlobalSnapshotSlots; index++ {
		go func(model *GuardedReadModel) {
			errorsOut <- model.WithExistingWritableLedgerAndSnapshot(context.Background(), "run", func(_ *ledger.JSONLLedger, snapshot func() (readmodel.Snapshot, error)) error {
				_, err := snapshot()
				return err
			})
		}(models[index])
	}
	for index := 0; index < GlobalSnapshotSlots; index++ {
		select {
		case <-base.entered:
		case <-time.After(2 * time.Second):
			close(base.release)
			t.Fatal("combined writable/snapshot reservations deadlocked below the ceilings")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := models[GlobalSnapshotSlots].WithExistingWritableLedgerAndSnapshot(ctx, "run", func(_ *ledger.JSONLLedger, snapshot func() (readmodel.Snapshot, error)) error {
		_, snapshotErr := snapshot()
		return snapshotErr
	})
	if !errors.Is(err, serviceapi.ErrAuthoritativeReadBusy) || !errors.Is(err, context.DeadlineExceeded) {
		close(base.release)
		t.Fatalf("fifth combined operation error = %v", err)
	}
	close(base.release)
	for index := 0; index < GlobalSnapshotSlots; index++ {
		if err := <-errorsOut; err != nil {
			t.Fatal(err)
		}
	}
	if base.maximum.Load() > GlobalSnapshotSlots {
		t.Fatalf("snapshot maximum = %d", base.maximum.Load())
	}
}

func TestGuardedWritableSnapshotBundleReleasesAfterPanic(t *testing.T) {
	root := newResourceGuardRoot(t)
	base := &blockingGuardedReadModel{entered: make(chan struct{}, 1), release: make(chan struct{})}
	close(base.release)
	model, err := NewGuardedReadModel(root, base)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("combined operation did not panic")
			}
		}()
		_ = model.WithExistingWritableLedgerAndSnapshot(context.Background(), "run", func(*ledger.JSONLLedger, func() (readmodel.Snapshot, error)) error {
			panic("test panic")
		})
	}()
	proof, err := openResourceGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	defer proof.Close()
	lease, err := proof.acquire(context.Background(), GlobalLedgerSlots, GlobalSnapshotSlots)
	if err != nil {
		t.Fatalf("panic leaked combined resource slots: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerSnapshotCoordinatorCancellationReleasesCapacity(t *testing.T) {
	root := newResourceGuardRoot(t)
	coordinator, err := NewRunnerSnapshotCoordinator(root)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()

	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- coordinator.WithSnapshot(ctx, func() error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("runner snapshot did not acquire capacity")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled runner snapshot = %v", err)
	}

	proof, err := openResourceGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	defer proof.Close()
	lease, err := proof.acquire(context.Background(), 0, GlobalSnapshotSlots)
	if err != nil {
		t.Fatalf("cancelled runner snapshot leaked capacity: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerServiceAndWatcherSnapshotsShareExactCrossProcessCeiling(t *testing.T) {
	root := newResourceGuardRoot(t)
	initialize, err := openResourceGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := initialize.Close(); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	release := filepath.Join(state, "release")
	roles := []string{"runner", "service", "watcher", "writable"}
	children := make([]resourceHelper, 0, len(roles)+1)
	for index, role := range roles {
		marker := filepath.Join(state, "entered-"+strconv.Itoa(index))
		children = append(children, startSnapshotClientHelper(t, root, role, marker, release, ""))
	}
	for index := range children {
		waitForResourceMarker(t, children[index].marker)
	}
	overflowMarker := filepath.Join(state, "entered-overflow")
	children = append(children, startSnapshotClientHelper(t, root, "runner", overflowMarker, release, ""))
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(overflowMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fifth runner/service snapshot exceeded the aggregate ceiling: %v", err)
	}
	if err := os.WriteFile(release, []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		if err := child.command.Wait(); err != nil {
			t.Fatalf("%s snapshot helper failed: %v\n%s", child.role, err, child.output.String())
		}
	}
}

func TestCancelledRunnerSnapshotReleasesCrossProcessCapacity(t *testing.T) {
	root := newResourceGuardRoot(t)
	initialize, err := openResourceGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := initialize.Close(); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	release := filepath.Join(state, "release")
	roles := []string{"runner", "service", "watcher"}
	holders := make([]resourceHelper, 0, len(roles))
	for index, role := range roles {
		marker := filepath.Join(state, "holder-"+strconv.Itoa(index))
		holders = append(holders, startSnapshotClientHelper(t, root, role, marker, release, ""))
	}
	for index := range holders {
		waitForResourceMarker(t, holders[index].marker)
	}

	cancelPath := filepath.Join(state, "cancel")
	cancelledMarker := filepath.Join(state, "cancelled-runner-entered")
	cancelled := startSnapshotClientHelper(t, root, "runner", cancelledMarker, filepath.Join(state, "never-release"), cancelPath)
	waitForResourceMarker(t, cancelledMarker)
	overflowMarker := filepath.Join(state, "replacement-watcher-entered")
	overflow := startSnapshotClientHelper(t, root, "watcher", overflowMarker, release, "")
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(overflowMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overflow watcher entered before cancellation released capacity: %v", err)
	}
	if err := os.WriteFile(cancelPath, []byte("cancel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cancelled.command.Wait(); err != nil {
		t.Fatalf("cancelled runner helper failed: %v\n%s", err, cancelled.output.String())
	}
	waitForResourceMarker(t, overflowMarker)
	if err := os.WriteFile(release, []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, child := range holders {
		if err := child.command.Wait(); err != nil {
			t.Fatalf("%s snapshot holder failed: %v\n%s", child.role, err, child.output.String())
		}
	}
	if err := overflow.command.Wait(); err != nil {
		t.Fatalf("replacement watcher failed: %v\n%s", err, overflow.output.String())
	}
}

func TestResourceGuardAggregateCeilingsAcrossServeAndRunProcesses(t *testing.T) {
	for _, test := range []struct {
		name     string
		kind     string
		capacity int
	}{
		{"ledger", "ledger", GlobalLedgerSlots},
		{"snapshot", "snapshot", GlobalSnapshotSlots},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := newResourceGuardRoot(t)
			initialize, err := openResourceGuard(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := initialize.Close(); err != nil {
				t.Fatal(err)
			}
			state := t.TempDir()
			release := filepath.Join(state, "release")
			children := make([]resourceHelper, 0, test.capacity+1)
			for index := 0; index < test.capacity; index++ {
				role := "serve"
				if index%2 == 1 {
					role = "run-owner"
				}
				marker := filepath.Join(state, "entered-"+strconv.Itoa(index))
				children = append(children, startResourceHelper(t, root, test.kind, role, marker, release, false))
			}
			for index := range children {
				waitForResourceMarker(t, children[index].marker)
			}
			overflowMarker := filepath.Join(state, "entered-overflow")
			children = append(children, startResourceHelper(t, root, test.kind, "serve", overflowMarker, release, false))
			time.Sleep(150 * time.Millisecond)
			if _, err := os.Stat(overflowMarker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("process exceeded the aggregate %s ceiling: %v", test.kind, err)
			}
			if err := os.WriteFile(release, []byte("release\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, child := range children {
				if err := child.command.Wait(); err != nil {
					t.Fatalf("%s helper failed: %v\n%s", child.role, err, child.output.String())
				}
			}
		})
	}
}

func TestResourceGuardProcessExitCannotLeakSlots(t *testing.T) {
	root := newResourceGuardRoot(t)
	initialize, err := openResourceGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := initialize.Close(); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	marker := filepath.Join(state, "crash-entered")
	child := startResourceHelper(t, root, "all", "run-owner", marker, filepath.Join(state, "never-release"), true)
	waitForResourceMarker(t, marker)
	if err := child.command.Wait(); err != nil {
		t.Fatalf("crash helper exit = %v\n%s", err, child.output.String())
	}

	guard, err := openResourceGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	lease, err := guard.acquire(context.Background(), GlobalLedgerSlots, GlobalSnapshotSlots)
	if err != nil {
		t.Fatalf("process exit leaked resource slots: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestResourceGuardHelperProcess(t *testing.T) {
	if os.Getenv("ABCP_RESOURCE_GUARD_HELPER") != "1" {
		return
	}
	root := os.Getenv("ABCP_RESOURCE_GUARD_ROOT")
	role := os.Getenv("ABCP_RESOURCE_GUARD_ROLE")
	if client := os.Getenv("ABCP_RESOURCE_GUARD_SNAPSHOT_CLIENT"); client != "" {
		runSnapshotClientHelper(t, root, client)
		return
	}
	if role != "serve" && role != "run-owner" {
		t.Fatalf("invalid helper role %q", role)
	}
	if os.Getenv("ABCP_RESOURCE_GUARD_EXPECT_OPEN_INTEGRITY") == "1" {
		assertRejectedOpen := func() {
			t.Helper()
			guard, err := openResourceGuard(root)
			if guard != nil {
				_ = guard.Close()
				t.Fatal("replacement resource guard unexpectedly opened")
			}
			if !errors.Is(err, ErrIntegrity) {
				t.Fatalf("replacement guard open error = %v", err)
			}
		}
		assertRejectedOpen()
		before := countResourceHelperDescriptors(t)
		for attempt := 0; attempt < 8; attempt++ {
			assertRejectedOpen()
		}
		if after := countResourceHelperDescriptors(t); after != before {
			t.Fatalf("failed guard opens leaked descriptors: before=%d after=%d", before, after)
		}
		return
	}
	guard, err := openResourceGuard(root)
	if os.Getenv("ABCP_RESOURCE_GUARD_EXPECT_INTEGRITY") == "1" {
		if errors.Is(err, ErrIntegrity) {
			return
		}
		if err != nil {
			t.Fatalf("replacement guard open error = %v", err)
		}
		defer guard.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		lease, acquireErr := acquireResourceHelperKind(ctx, guard, os.Getenv("ABCP_RESOURCE_GUARD_KIND"))
		if lease != nil {
			_ = lease.Close()
		}
		if !errors.Is(acquireErr, ErrIntegrity) {
			t.Fatalf("replacement resource acquisition was not rejected: %v", acquireErr)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var lease *resourceLease
	lease, err = acquireResourceHelperKind(context.Background(), guard, os.Getenv("ABCP_RESOURCE_GUARD_KIND"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("ABCP_RESOURCE_GUARD_MARKER"), []byte(role+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("ABCP_RESOURCE_GUARD_CRASH") == "1" {
		os.Exit(0)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(os.Getenv("ABCP_RESOURCE_GUARD_RELEASE")); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for helper release")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
}

func acquireResourceHelperKind(ctx context.Context, guard *resourceGuard, kind string) (*resourceLease, error) {
	switch kind {
	case "ledger":
		return guard.acquire(ctx, 1, 0)
	case "snapshot":
		return guard.acquire(ctx, 0, 1)
	case "all":
		return guard.acquire(ctx, GlobalLedgerSlots, GlobalSnapshotSlots)
	default:
		return nil, ErrIntegrity
	}
}

type blockingGuardedReadModel struct {
	entered chan struct{}
	release chan struct{}
	active  atomic.Int32
	maximum atomic.Int32
	calls   atomic.Int32
}

func (m *blockingGuardedReadModel) Snapshot(context.Context, string) (readmodel.Snapshot, error) {
	m.calls.Add(1)
	current := m.active.Add(1)
	for {
		prior := m.maximum.Load()
		if current <= prior || m.maximum.CompareAndSwap(prior, current) {
			break
		}
	}
	m.entered <- struct{}{}
	<-m.release
	m.active.Add(-1)
	return readmodel.Snapshot{}, nil
}

func (m *blockingGuardedReadModel) ReadRunProjection(ctx context.Context, runID string) (json.RawMessage, error) {
	_, err := m.Snapshot(ctx, runID)
	return nil, err
}

func (m *blockingGuardedReadModel) ReadEventProjection(ctx context.Context, runID string, _ serviceapi.PageRequestV1) (json.RawMessage, error) {
	_, err := m.Snapshot(ctx, runID)
	return nil, err
}

func (*blockingGuardedReadModel) WithExistingWritableLedger(_ context.Context, _ string, operation func(*ledger.JSONLLedger) error) error {
	return operation(nil)
}

type resourceHelper struct {
	command *exec.Cmd
	output  *bytes.Buffer
	marker  string
	role    string
}

func startSnapshotClientHelper(t *testing.T, root, role, marker, release, cancel string) resourceHelper {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestResourceGuardHelperProcess$")
	command.Env = append(os.Environ(),
		"ABCP_RESOURCE_GUARD_HELPER=1",
		"ABCP_RESOURCE_GUARD_ROOT="+root,
		"ABCP_RESOURCE_GUARD_ROLE="+role,
		"ABCP_RESOURCE_GUARD_SNAPSHOT_CLIENT="+role,
		"ABCP_RESOURCE_GUARD_MARKER="+marker,
		"ABCP_RESOURCE_GUARD_RELEASE="+release,
		"ABCP_RESOURCE_GUARD_CANCEL="+cancel,
	)
	output := &bytes.Buffer{}
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	return resourceHelper{command: command, output: output, marker: marker, role: role}
}

func runSnapshotClientHelper(t *testing.T, root, role string) {
	t.Helper()
	if role != "runner" && role != "service" && role != "watcher" && role != "writable" {
		t.Fatalf("invalid snapshot client role %q", role)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelPath := os.Getenv("ABCP_RESOURCE_GUARD_CANCEL")
	if cancelPath != "" {
		go func() {
			for {
				if _, err := os.Stat(cancelPath); err == nil {
					cancel()
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	operation := func(operationCtx context.Context) error {
		if err := os.WriteFile(os.Getenv("ABCP_RESOURCE_GUARD_MARKER"), []byte(role+"\n"), 0o600); err != nil {
			return err
		}
		for {
			if _, err := os.Stat(os.Getenv("ABCP_RESOURCE_GUARD_RELEASE")); err == nil {
				return nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			select {
			case <-operationCtx.Done():
				return operationCtx.Err()
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	var err error
	if role == "runner" {
		coordinator, openErr := NewRunnerSnapshotCoordinator(root)
		if openErr != nil {
			t.Fatal(openErr)
		}
		err = coordinator.WithSnapshot(ctx, func() error { return operation(ctx) })
		err = errors.Join(err, coordinator.Close())
	} else {
		base := &resourceSnapshotHelperReadModel{snapshot: operation}
		model, openErr := NewGuardedReadModel(root, base)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if role == "writable" {
			err = model.WithExistingWritableLedgerAndSnapshot(ctx, "run", func(_ *ledger.JSONLLedger, snapshot func() (readmodel.Snapshot, error)) error {
				_, snapshotErr := snapshot()
				return snapshotErr
			})
		} else {
			_, err = model.Snapshot(ctx, "run")
		}
		err = errors.Join(err, model.Close())
	}
	if cancelPath != "" && errors.Is(err, context.Canceled) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
}

type resourceSnapshotHelperReadModel struct {
	snapshot func(context.Context) error
}

func (m *resourceSnapshotHelperReadModel) Snapshot(ctx context.Context, _ string) (readmodel.Snapshot, error) {
	return readmodel.Snapshot{}, m.snapshot(ctx)
}

func (m *resourceSnapshotHelperReadModel) ReadRunProjection(ctx context.Context, runID string) (json.RawMessage, error) {
	_, err := m.Snapshot(ctx, runID)
	return nil, err
}

func (m *resourceSnapshotHelperReadModel) ReadEventProjection(ctx context.Context, runID string, _ serviceapi.PageRequestV1) (json.RawMessage, error) {
	_, err := m.Snapshot(ctx, runID)
	return nil, err
}

func (*resourceSnapshotHelperReadModel) WithExistingWritableLedger(_ context.Context, _ string, operation func(*ledger.JSONLLedger) error) error {
	return operation(nil)
}

func startResourceHelper(t *testing.T, root, kind, role, marker, release string, crash bool) resourceHelper {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestResourceGuardHelperProcess$")
	command.Env = append(os.Environ(),
		"ABCP_RESOURCE_GUARD_HELPER=1",
		"ABCP_RESOURCE_GUARD_ROOT="+root,
		"ABCP_RESOURCE_GUARD_KIND="+kind,
		"ABCP_RESOURCE_GUARD_ROLE="+role,
		"ABCP_RESOURCE_GUARD_MARKER="+marker,
		"ABCP_RESOURCE_GUARD_RELEASE="+release,
	)
	if crash {
		command.Env = append(command.Env, "ABCP_RESOURCE_GUARD_CRASH=1")
	}
	output := new(bytes.Buffer)
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	})
	return resourceHelper{command: command, output: output, marker: marker, role: role}
}

func installResourcePairedGenerationAttack(t *testing.T, root string, attack resourcePairedGenerationAttack) (func(), func()) {
	t.Helper()
	backup := filepath.Join(root, ".resource-paired-generation-backup")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	if attack.directory != "" {
		parent := filepath.Join(root, resourceGuardDirectory)
		if attack.directory == resourceGuardDirectory {
			parent = root
		}
		object := filepath.Join(parent, attack.directory)
		anchor := filepath.Join(parent, resourceDirectoryAnchorName(attack.directory))
		backupObject := filepath.Join(backup, "directory")
		backupAnchor := filepath.Join(backup, "directory-anchor")
		if err := os.Rename(object, backupObject); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(anchor, backupAnchor); err != nil {
			t.Fatal(err)
		}
		if attack.directory == resourceGuardDirectory {
			createReplacementResourceGuard(t, root)
		} else {
			total := GlobalLedgerSlots
			if attack.directory == "snapshot" {
				total = GlobalSnapshotSlots
			}
			createReplacementResourceDirectory(t, parent, attack.directory, total)
		}
		verify := func() {
			if attack.directory == resourceGuardDirectory {
				assertReplacementResourceGuard(t, root)
				return
			}
			total := GlobalLedgerSlots
			if attack.directory == "snapshot" {
				total = GlobalSnapshotSlots
			}
			assertReplacementResourceDirectory(t, parent, attack.directory, total)
		}
		restore := func() {
			if err := os.RemoveAll(object); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(anchor); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(backupObject, object); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(backupAnchor, anchor); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(backup); err != nil {
				t.Fatal(err)
			}
		}
		return restore, verify
	}

	directory := filepath.Join(root, resourceGuardDirectory, attack.resource)
	slot := filepath.Join(directory, resourceSlotName(attack.slot))
	manifest := filepath.Join(directory, resourceSlotIdentityName)
	backupSlot := filepath.Join(backup, "slot")
	backupManifest := filepath.Join(backup, "slot-manifest")
	if err := os.Rename(slot, backupSlot); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(manifest, backupManifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(slot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	total := GlobalLedgerSlots
	if attack.resource == "snapshot" {
		total = GlobalSnapshotSlots
	}
	writeReplacementResourceSlotManifest(t, directory, attack.resource, total)
	verify := func() { assertReplacementResourceSlotSet(t, directory, attack.resource, total) }
	restore := func() {
		if err := os.Remove(slot); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(manifest); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(backupSlot, slot); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(backupManifest, manifest); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(backup); err != nil {
			t.Fatal(err)
		}
	}
	return restore, verify
}

func createReplacementResourceGuard(t *testing.T, root string) {
	t.Helper()
	guard := createReplacementResourceDirectoryOnly(t, root, resourceGuardDirectory)
	createReplacementResourceDirectory(t, guard, "ledger", GlobalLedgerSlots)
	createReplacementResourceDirectory(t, guard, "snapshot", GlobalSnapshotSlots)
}

func createReplacementResourceDirectory(t *testing.T, parent, name string, total int) string {
	t.Helper()
	directory := createReplacementResourceDirectoryOnly(t, parent, name)
	for slot := 0; slot < total; slot++ {
		if err := os.WriteFile(filepath.Join(directory, resourceSlotName(slot)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeReplacementResourceSlotManifest(t, directory, name, total)
	return directory
}

func createReplacementResourceDirectoryOnly(t *testing.T, parent, name string) string {
	t.Helper()
	directory := filepath.Join(parent, name)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("replacement resource directory has no stat identity")
	}
	identity := resourceDirectoryIdentityV1{Kind: "ReadModelResourceDirectoryIdentityV1", SchemaVersion: 1, Name: name,
		Device: uint64(stat.Dev), Inode: stat.Ino}
	data, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, resourceDirectoryAnchorName(name)), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func writeReplacementResourceSlotManifest(t *testing.T, directory, resource string, total int) {
	t.Helper()
	identity := resourceSlotIdentityV1{Kind: "ReadModelResourceSlotIdentityV1", SchemaVersion: 1, Resource: resource,
		Slots: make([]resourceSlotIdentityEntryV1, 0, total)}
	for slot := 0; slot < total; slot++ {
		name := resourceSlotName(slot)
		info, err := os.Lstat(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("replacement resource slot %s has no stat identity", name)
		}
		identity.Slots = append(identity.Slots, resourceSlotIdentityEntryV1{Name: name, Device: uint64(stat.Dev), Inode: stat.Ino})
	}
	data, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, resourceSlotIdentityName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertReplacementResourceGuard(t *testing.T, root string) {
	t.Helper()
	guard := filepath.Join(root, resourceGuardDirectory)
	assertProtectedDirectory(t, guard)
	assertResourceDirectoryAnchor(t, root, resourceGuardDirectory)
	entries, err := os.ReadDir(guard)
	if err != nil || len(entries) != 4 {
		t.Fatalf("replacement guard entries = %d, err=%v", len(entries), err)
	}
	assertReplacementResourceDirectory(t, guard, "ledger", GlobalLedgerSlots)
	assertReplacementResourceDirectory(t, guard, "snapshot", GlobalSnapshotSlots)
}

func assertReplacementResourceDirectory(t *testing.T, parent, resource string, total int) {
	t.Helper()
	directory := filepath.Join(parent, resource)
	assertProtectedDirectory(t, directory)
	assertResourceDirectoryAnchor(t, parent, resource)
	assertReplacementResourceSlotSet(t, directory, resource, total)
}

func assertReplacementResourceSlotSet(t *testing.T, directory, resource string, total int) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != total+1 {
		t.Fatalf("replacement %s entries = %d, err=%v", resource, len(entries), err)
	}
	data, err := os.ReadFile(filepath.Join(directory, resourceSlotIdentityName))
	if err != nil {
		t.Fatal(err)
	}
	manifestInfo, err := os.Lstat(filepath.Join(directory, resourceSlotIdentityName))
	if err != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Mode().Perm() != 0o600 {
		t.Fatalf("unsafe replacement %s slot manifest: info=%v err=%v", resource, manifestInfo, err)
	}
	manifestStat, ok := manifestInfo.Sys().(*syscall.Stat_t)
	if !ok || manifestStat.Nlink != 1 {
		t.Fatalf("unsafe replacement %s slot manifest identity: stat=%+v", resource, manifestStat)
	}
	var identity resourceSlotIdentityV1
	if json.Unmarshal(data, &identity) != nil {
		t.Fatalf("invalid replacement %s slot manifest", resource)
	}
	canonical, marshalErr := json.Marshal(identity)
	if marshalErr != nil || !bytes.Equal(canonical, data) || identity.Kind != "ReadModelResourceSlotIdentityV1" ||
		identity.SchemaVersion != 1 || identity.Resource != resource || len(identity.Slots) != total {
		t.Fatalf("replacement %s slot manifest = %+v, marshalErr=%v", resource, identity, marshalErr)
	}
	for slot, expected := range identity.Slots {
		name := resourceSlotName(slot)
		info, err := os.Lstat(filepath.Join(directory, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("unsafe replacement %s slot %s: info=%v err=%v", resource, name, info, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 || expected.Name != name || expected.Device != uint64(stat.Dev) || expected.Inode != stat.Ino {
			t.Fatalf("replacement %s slot identity %d = %+v, stat=%+v", resource, slot, expected, stat)
		}
	}
}

func runResourceIntegrityHelper(t *testing.T, root, kind string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestResourceGuardHelperProcess$")
	command.Env = append(os.Environ(),
		"ABCP_RESOURCE_GUARD_HELPER=1",
		"ABCP_RESOURCE_GUARD_EXPECT_INTEGRITY=1",
		"ABCP_RESOURCE_GUARD_ROOT="+root,
		"ABCP_RESOURCE_GUARD_KIND="+kind,
		"ABCP_RESOURCE_GUARD_ROLE=serve",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("replacement helper failed: %v\n%s", err, output)
	}
}

func runResourceOpenIntegrityHelper(t *testing.T, root string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestResourceGuardHelperProcess$")
	command.Env = append(os.Environ(),
		"ABCP_RESOURCE_GUARD_HELPER=1",
		"ABCP_RESOURCE_GUARD_EXPECT_OPEN_INTEGRITY=1",
		"ABCP_RESOURCE_GUARD_ROOT="+root,
		"ABCP_RESOURCE_GUARD_ROLE=serve",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("replacement open helper failed: %v\n%s", err, output)
	}
}

func countResourceHelperDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func waitForResourceMarker(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("resource helper did not enter: %s", marker)
}

func newResourceGuardRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "service")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func assertProtectedDirectory(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("unsafe resource directory %s: info=%v err=%v", path, info, err)
	}
}

func assertResourceDirectoryAnchor(t *testing.T, parent, name string) {
	t.Helper()
	anchorPath := filepath.Join(parent, resourceDirectoryAnchorName(name))
	anchorInfo, err := os.Lstat(anchorPath)
	if err != nil || !anchorInfo.Mode().IsRegular() || anchorInfo.Mode().Perm() != 0o600 {
		t.Fatalf("unsafe directory anchor %s: info=%v err=%v", anchorPath, anchorInfo, err)
	}
	anchorStat, ok := anchorInfo.Sys().(*syscall.Stat_t)
	if !ok || anchorStat.Nlink != 1 {
		t.Fatalf("directory anchor link count = %v", anchorStat)
	}
	data, err := os.ReadFile(anchorPath)
	if err != nil {
		t.Fatal(err)
	}
	var identity resourceDirectoryIdentityV1
	if err := json.Unmarshal(data, &identity); err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Lstat(filepath.Join(parent, name))
	if err != nil {
		t.Fatal(err)
	}
	directoryStat, ok := directoryInfo.Sys().(*syscall.Stat_t)
	if !ok || identity.Kind != "ReadModelResourceDirectoryIdentityV1" || identity.SchemaVersion != 1 || identity.Name != name ||
		identity.Device != uint64(directoryStat.Dev) || identity.Inode != directoryStat.Ino {
		t.Fatalf("directory identity = %+v, stat=%+v", identity, directoryStat)
	}
}

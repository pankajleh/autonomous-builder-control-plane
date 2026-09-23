//go:build linux

package runtimecatalog

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/recovery"
)

func TestCatalogRegistrationListingAndImmutableVerification(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	at := time.Date(2026, 9, 13, 1, 2, 3, 0, time.UTC)
	for _, id := range []string{"run-z", "run-a", strings.Repeat("r", 256)} {
		registration := testRunRegistration(t, id, at)
		if err := catalog.RegisterRun(registration); err != nil {
			t.Fatalf("register %q: %v", id, err)
		}
		if err := catalog.RegisterRun(registration); err != nil {
			t.Fatalf("byte-verify %q: %v", id, err)
		}
	}
	conflict := testRunRegistration(t, "run-a", at.Add(time.Second))
	if err := catalog.RegisterRun(conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting registration = %v", err)
	}
	first, more, err := catalog.ListRuns("", 2)
	if err != nil || !more || len(first) != 2 || first[0].RunID != strings.Repeat("r", 256) || first[1].RunID != "run-a" {
		t.Fatalf("first lexical page = %#v, more=%v, err=%v", first, more, err)
	}
	second, more, err := catalog.ListRuns(first[1].RunID, 2)
	if err != nil || more || len(second) != 1 || second[0].RunID != "run-z" {
		t.Fatalf("second lexical page = %#v, more=%v, err=%v", second, more, err)
	}
}

func TestRunRegistrationBindsImmutableLedgerGeneration(t *testing.T) {
	for _, attack := range []string{"file", "parent"} {
		t.Run(attack, func(t *testing.T) {
			root := t.TempDir()
			ledgerParent := filepath.Join(root, "ledger")
			ledgerPath := filepath.Join(ledgerParent, "events.jsonl")
			evidenceRoot := filepath.Join(root, "evidence")
			if err := os.MkdirAll(ledgerParent, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ledgerPath, []byte("original\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(evidenceRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			registration, err := NewRunRegistrationV1("bound-run", "example/repository", strings.Repeat("a", 64), ledgerPath, evidenceRoot, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := Open(filepath.Join(root, "service"))
			if err != nil {
				t.Fatal(err)
			}
			defer catalog.Close()
			if err := catalog.RegisterRun(registration); err != nil {
				t.Fatal(err)
			}

			if attack == "file" {
				if err := os.Rename(ledgerPath, ledgerPath+".original"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(ledgerParent, ledgerParent+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(ledgerParent, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(ledgerPath, []byte("replacement\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			if err := catalog.RegisterRun(registration); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("original physical generation was reissued: %v", err)
			}
			replacement, err := NewRunRegistrationV1("bound-run", "example/repository", strings.Repeat("a", 64), ledgerPath, evidenceRoot, mustParseCatalogTime(t, registration.InitialRegistrationTimestamp))
			if err != nil {
				t.Fatal(err)
			}
			if replacement.LedgerGeneration == registration.LedgerGeneration {
				t.Fatal("replacement reused the registered physical generation")
			}
			if err := catalog.RegisterRun(replacement); !errors.Is(err, ErrConflict) {
				t.Fatalf("replacement registration = %v, want immutable conflict", err)
			}
			stored, err := catalog.ReadRun(registration.RunID)
			if err != nil || stored != registration {
				t.Fatalf("stored registration changed: %#v, err=%v", stored, err)
			}
		})
	}
}

func mustParseCatalogTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestCatalogRejectsUnsafeRootIdentifiersAndRecords(t *testing.T) {
	parent := t.TempDir()
	if _, err := Open("relative"); err == nil {
		t.Fatal("relative service root accepted")
	}
	realRoot := filepath.Join(parent, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(parent, "link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(linkRoot); err == nil {
		t.Fatal("symlink service root accepted")
	}
	unsafeMode := filepath.Join(parent, "mode")
	if err := os.Mkdir(unsafeMode, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(unsafeMode); err == nil {
		t.Fatal("insecure service root mode accepted")
	}
	if os.Geteuid() == 0 {
		wrongOwner := filepath.Join(parent, "owner")
		if err := os.Mkdir(wrongOwner, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(wrongOwner, 1, -1); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(wrongOwner); err == nil {
			t.Fatal("foreign-owned service root accepted")
		}
	}
	catalog, err := Open(filepath.Join(parent, "service"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	registration := testRunRegistration(t, "good", time.Now().UTC())
	registration.RunID = "../escape"
	if err := catalog.RegisterRun(registration); !errors.Is(err, ErrIntegrity) && !errors.Is(err, ErrInvalidIdentifier) {
		t.Fatalf("traversal registration = %v", err)
	}
}

func TestCatalogFailsClosedOnSymlinkAndHardLinkedRecord(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	registration := testRunRegistration(t, "run-a", time.Now().UTC())
	if err := catalog.RegisterRun(registration); err != nil {
		t.Fatal(err)
	}
	runFile := filepath.Join(root, "catalog", "runs", "run-a", "run.json")
	hardlink := filepath.Join(t.TempDir(), "copy")
	if err := os.Link(runFile, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.ListRuns("", 50); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("hard-linked record list = %v", err)
	}
	if err := os.Remove(hardlink); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(runFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", runFile); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.ListRuns("", 50); err == nil {
		t.Fatal("symlinked registration listed")
	}
	if err := os.Remove(runFile); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(runFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.ListRuns("", 50); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("special-file registration list = %v", err)
	}
}

func TestCatalogFailsClosedAfterServiceRootReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "service")
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	if err := os.Rename(root, filepath.Join(parent, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.ListRuns("", 50); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("replaced service root list = %v", err)
	}
}

func TestCatalogRootGenerationAuthorityIsCompactAndDurable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	authority, found, err := getCatalogRootXattr(catalog.rootFD)
	if err != nil || !found || len(authority) != catalogGenerationMaxBytes || !bytes.Equal(authority, catalog.rootAuthorityData) {
		_ = catalog.Close()
		t.Fatalf("catalog root authority: bytes=%d found=%v err=%v", len(authority), found, err)
	}
	runAuthority, found, err := getCatalogRunAuthority(catalog.runs.fd)
	if err != nil || !found || len(runAuthority) > maxCatalogRunAuthority {
		_ = catalog.Close()
		t.Fatalf("catalog run authority: bytes=%d found=%v err=%v", len(runAuthority), found, err)
	}
	if _, legacyFound, legacyErr := getCatalogRunAuthority(catalog.rootFD); legacyErr != nil || legacyFound {
		_ = catalog.Close()
		t.Fatalf("catalog run authority remained on shared service root: found=%v err=%v", legacyFound, legacyErr)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	observed, found, err := getCatalogRootXattr(reopened.rootFD)
	if err != nil || !found || !bytes.Equal(observed, authority) {
		t.Fatalf("catalog root authority changed across reopen: found=%v err=%v", found, err)
	}
}

func TestCatalogRunAuthorityMigratesFromLegacyServiceRootXattr(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registration := testRunRegistration(t, "migrated-run", time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC))
	if err := catalog.RegisterRun(registration); err != nil {
		_ = catalog.Close()
		t.Fatal(err)
	}
	authority, found, err := getCatalogRunAuthority(catalog.runs.fd)
	if err != nil || !found {
		_ = catalog.Close()
		t.Fatalf("read current run authority: found=%v err=%v", found, err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}

	rootFD, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	runsFD, err := syscall.Open(filepath.Join(root, "catalog", "runs"), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		_ = syscall.Close(rootFD)
		t.Fatal(err)
	}
	if err := setCatalogRunAuthority(rootFD, authority, catalogXattrCreate); err != nil {
		_ = syscall.Close(runsFD)
		_ = syscall.Close(rootFD)
		t.Fatal(err)
	}
	if err := removeCatalogRunAuthorityForTest(runsFD); err != nil {
		_ = syscall.Close(runsFD)
		_ = syscall.Close(rootFD)
		t.Fatal(err)
	}
	if err := errors.Join(syscall.Fsync(rootFD), syscall.Fsync(runsFD), syscall.Close(runsFD), syscall.Close(rootFD)); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(root)
	if err != nil {
		t.Fatalf("migrate legacy run-authority storage: %v", err)
	}
	defer reopened.Close()
	migrated, found, err := getCatalogRunAuthority(reopened.runs.fd)
	if err != nil || !found || !bytes.Equal(migrated, authority) {
		t.Fatalf("migrated run authority: found=%v equal=%v err=%v", found, bytes.Equal(migrated, authority), err)
	}
	if _, legacyFound, legacyErr := getCatalogRunAuthority(reopened.rootFD); legacyErr != nil || legacyFound {
		t.Fatalf("legacy service-root authority after migration: found=%v err=%v", legacyFound, legacyErr)
	}
	if observed, err := reopened.ReadRun(registration.RunID); err != nil || observed != registration {
		t.Fatalf("migrated registration = %#v, err=%v", observed, err)
	}
}

func TestCatalogRunAuthorityMigrationAcceptsEqualDualCopy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registration := testRunRegistration(t, "dual-copy-run", time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC))
	if err := catalog.RegisterRun(registration); err != nil {
		_ = catalog.Close()
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	moveCatalogRunAuthorityToLegacyRoot(t, root, true)

	reopened, err := Open(root)
	if err != nil {
		t.Fatalf("recover equal dual-copy migration: %v", err)
	}
	defer reopened.Close()
	if _, found, err := getCatalogRunAuthority(reopened.rootFD); err != nil || found {
		t.Fatalf("legacy copy after equal dual-copy recovery: found=%v err=%v", found, err)
	}
	if _, found, err := getCatalogRunAuthority(reopened.runs.fd); err != nil || !found {
		t.Fatalf("destination copy after equal dual-copy recovery: found=%v err=%v", found, err)
	}
}

func TestCatalogRunAuthorityMigrationRejectsConflictingDualCopy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registration := testRunRegistration(t, "conflict-run", time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC))
	if err := catalog.RegisterRun(registration); err != nil {
		_ = catalog.Close()
		t.Fatal(err)
	}
	_, conflicting, err := makeCatalogRunAuthority(catalog, catalogRunStateEstablished, 0, "", nil)
	if err != nil {
		_ = catalog.Close()
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	rootFD, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := setCatalogRunAuthority(rootFD, conflicting, catalogXattrCreate); err != nil {
		_ = syscall.Close(rootFD)
		t.Fatal(err)
	}
	if err := errors.Join(syscall.Fsync(rootFD), syscall.Close(rootFD)); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(root)
	if reopened != nil {
		_ = reopened.Close()
	}
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("conflicting dual-copy migration accepted: %v", err)
	}
}

func TestCatalogRunAuthorityMigrationReconcilesPreparedStates(t *testing.T) {
	for _, boundary := range []string{"prepared-run", "prepared-full"} {
		t.Run(boundary, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "service")
			_, target, _ := prepareCatalogRunIssuanceCrashFixture(t, root)
			runCatalogIssuanceCrashHelper(t, root, target, boundary)
			moveCatalogRunAuthorityToLegacyRoot(t, root, false)

			catalog, err := Open(root)
			if err != nil {
				t.Fatalf("migrate/reconcile %s: %v", boundary, err)
			}
			defer catalog.Close()
			if _, found, err := getCatalogRunAuthority(catalog.rootFD); err != nil || found {
				t.Fatalf("%s legacy copy after migration: found=%v err=%v", boundary, found, err)
			}
			if _, found, err := getCatalogRunAuthority(catalog.runs.fd); err != nil || !found {
				t.Fatalf("%s destination after migration: found=%v err=%v", boundary, found, err)
			}
			if err := catalog.RegisterRun(target); err != nil {
				t.Fatalf("complete target after %s migration: %v", boundary, err)
			}
		})
	}
}

func TestCatalogRunAuthorityMigrationJoinsIssuanceLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	moveCatalogRunAuthorityToLegacyRoot(t, root, false)

	lockFD, err := syscall.Open(filepath.Join(root, "catalog", ".lock"), syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(lockFD, syscall.LOCK_EX); err != nil {
		_ = syscall.Close(lockFD)
		t.Fatal(err)
	}
	type openResult struct {
		catalog *Catalog
		err     error
	}
	result := make(chan openResult, 1)
	go func() {
		opened, openErr := Open(root)
		result <- openResult{catalog: opened, err: openErr}
	}()

	select {
	case early := <-result:
		if early.catalog != nil {
			_ = early.catalog.Close()
		}
		_ = syscall.Flock(lockFD, syscall.LOCK_UN)
		_ = syscall.Close(lockFD)
		t.Fatalf("migration bypassed issuance lock: %v", early.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := errors.Join(syscall.Flock(lockFD, syscall.LOCK_UN), syscall.Close(lockFD)); err != nil {
		t.Fatal(err)
	}
	opened := <-result
	if opened.err != nil {
		t.Fatalf("migration after issuance lock release: %v", opened.err)
	}
	if opened.catalog == nil {
		t.Fatal("migration returned nil catalog")
	}
	defer opened.catalog.Close()
}

func TestCatalogRunAuthorityMigrationRevalidatesSourceBeforeRemoval(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registration := testRunRegistration(t, "source-revalidation-run", time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC))
	if err := catalog.RegisterRun(registration); err != nil {
		_ = catalog.Close()
		t.Fatal(err)
	}
	_, conflicting, err := makeCatalogRunAuthority(catalog, catalogRunStateEstablished, 0, "", nil)
	if err != nil {
		_ = catalog.Close()
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	original := moveCatalogRunAuthorityToLegacyRoot(t, root, false)

	catalogRunAuthorityMigrationBoundaryHook = func(boundary string) {
		if boundary != "destination-durable" {
			return
		}
		rootFD, openErr := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if openErr != nil {
			t.Errorf("open source for mutation: %v", openErr)
			return
		}
		if setErr := setCatalogRunAuthority(rootFD, conflicting, catalogXattrReplace); setErr != nil {
			t.Errorf("mutate migration source: %v", setErr)
		}
		if syncErr := syscall.Fsync(rootFD); syncErr != nil {
			t.Errorf("sync mutated source: %v", syncErr)
		}
		_ = syscall.Close(rootFD)
	}
	defer func() { catalogRunAuthorityMigrationBoundaryHook = func(string) {} }()

	reopened, err := Open(root)
	if reopened != nil {
		_ = reopened.Close()
	}
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("mutated migration source accepted: %v", err)
	}
	rootFD, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(rootFD)
	observed, found, err := getCatalogRunAuthority(rootFD)
	if err != nil || !found || !bytes.Equal(observed, conflicting) {
		t.Fatalf("newer/conflicting source was removed: found=%v err=%v", found, err)
	}
	runsFD, err := syscall.Open(filepath.Join(root, "catalog", "runs"), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(runsFD)
	destination, found, err := getCatalogRunAuthority(runsFD)
	if err != nil || !found || !bytes.Equal(destination, original) {
		t.Fatalf("durable destination changed unexpectedly: found=%v err=%v", found, err)
	}
}

func TestCatalogLegacyRunAuthorityUpgradeAndRequiredCheckpoint(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	registration := testRunRegistration(t, "legacy-run", time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC))
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterRun(registration); err != nil {
		_ = catalog.Close()
		t.Fatal(err)
	}
	rootAuthority, err := decodeCatalogRootGeneration(catalog.rootAuthorityData)
	if err != nil {
		_ = catalog.Close()
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	identityData, err := os.ReadFile(filepath.Join(root, "catalog", "runs", storageComponent(registration.RunID), runNamespaceIdentityName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "catalog", runGenerationsName), identityData, 0o600); err != nil {
		t.Fatal(err)
	}
	rootFD, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	rootAuthority.state = catalogGenerationReady
	legacyData := encodeCatalogRootGeneration(rootAuthority)
	setErr := setCatalogRootXattr(rootFD, legacyData, catalogXattrReplace)
	runsFD, openRunsErr := syscall.Open(filepath.Join(root, "catalog", "runs"), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if openRunsErr != nil {
		_ = syscall.Close(rootFD)
		t.Fatal(openRunsErr)
	}
	removeErr := removeCatalogRunAuthorityForTest(runsFD)
	syncRunsErr := syscall.Fsync(runsFD)
	syncErr := syscall.Fsync(rootFD)
	closeRunsErr := syscall.Close(runsFD)
	if setErr != nil || removeErr != nil || syncRunsErr != nil || syncErr != nil || closeRunsErr != nil {
		_ = syscall.Close(rootFD)
		t.Fatalf("construct legacy catalog: set=%v remove=%v sync-runs=%v sync-root=%v close-runs=%v",
			setErr, removeErr, syncRunsErr, syncErr, closeRunsErr)
	}
	if err := syscall.Close(rootFD); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(root)
	if err != nil {
		t.Fatalf("upgrade legacy catalog: %v", err)
	}
	observedRoot, err := decodeCatalogRootGeneration(reopened.rootAuthorityData)
	if err != nil || observedRoot.state != catalogGenerationReadyV2 {
		_ = reopened.Close()
		t.Fatalf("upgraded root authority = %+v, err=%v", observedRoot, err)
	}
	runAuthorityData, found, err := getCatalogRunAuthority(reopened.runs.fd)
	runAuthority, decodeErr := decodeCatalogRunAuthority(runAuthorityData, reopened)
	if err != nil || !found || decodeErr != nil || runAuthority.State != catalogRunStateEstablished || runAuthority.IssuanceCount != 1 {
		_ = reopened.Close()
		t.Fatalf("upgraded run authority = %+v, found=%v err=%v decode=%v", runAuthority, found, err, decodeErr)
	}
	if observed, err := reopened.ReadRun(registration.RunID); err != nil || observed != registration {
		_ = reopened.Close()
		t.Fatalf("legacy registration after upgrade = %#v, err=%v", observed, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	runsFD, err = syscall.Open(filepath.Join(root, "catalog", "runs"), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	removeErr = removeCatalogRunAuthorityForTest(runsFD)
	syncErr = syscall.Fsync(runsFD)
	if removeErr != nil || syncErr != nil {
		_ = syscall.Close(runsFD)
		t.Fatalf("remove required run authority: remove=%v sync=%v", removeErr, syncErr)
	}
	_ = syscall.Close(runsFD)
	failed, err := Open(root)
	if failed != nil {
		_ = failed.Close()
	}
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("upgraded catalog recreated removed run authority: %v", err)
	}
}

func moveCatalogRunAuthorityToLegacyRoot(t *testing.T, root string, keepDestination bool) []byte {
	t.Helper()
	rootFD, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(rootFD)
	runsFD, err := syscall.Open(filepath.Join(root, "catalog", "runs"), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(runsFD)
	authority, found, err := getCatalogRunAuthority(runsFD)
	if err != nil || !found {
		t.Fatalf("read destination run authority: found=%v err=%v", found, err)
	}
	if err := setCatalogRunAuthority(rootFD, authority, catalogXattrCreate); err != nil {
		t.Fatal(err)
	}
	if !keepDestination {
		if err := removeCatalogRunAuthorityForTest(runsFD); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(syscall.Fsync(runsFD), syscall.Fsync(rootFD)); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), authority...)
}

func removeCatalogRunAuthorityForTest(fd int) error {
	name, err := syscall.BytePtrFromString(catalogRunAuthorityXattr)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_FREMOVEXATTR, uintptr(fd), uintptr(unsafe.Pointer(name)), 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func TestCatalogRunNamespaceIssuanceCrashHelper(t *testing.T) {
	root := os.Getenv("ABCP_RUNTIME_CATALOG_ISSUANCE_HELPER")
	if root == "" {
		return
	}
	boundary := os.Getenv("ABCP_RUNTIME_CATALOG_ISSUANCE_BOUNDARY")
	catalogRunIssuanceBoundaryHook = func(observed string) {
		if observed == boundary {
			os.Exit(91)
		}
	}
	catalog, err := Open(root)
	if err != nil {
		os.Exit(92)
	}
	registration, err := NewRunRegistrationV1(
		"run-crash", "example/repository", strings.Repeat("b", 64),
		os.Getenv("ABCP_RUNTIME_CATALOG_ISSUANCE_LEDGER"),
		os.Getenv("ABCP_RUNTIME_CATALOG_ISSUANCE_EVIDENCE"),
		time.Date(2026, 9, 15, 1, 2, 3, 0, time.UTC),
	)
	if err != nil || catalog.RegisterRun(registration) != nil {
		os.Exit(93)
	}
	_ = catalog.Close()
	os.Exit(94)
}

func TestCatalogRunNamespaceIssuanceRecoversEveryCrashBoundary(t *testing.T) {
	boundaries := []string{
		"stage-created", "unbound-stage", "prepare-run-visible", "prepared-run",
		"attempts-visible", "attempts-created", "identity-created", "identity-partial",
		"identity-complete-visible", "identity-written", "prepare-full-visible", "prepared-full",
		"directory-visible", "published-directory", "partial-history", "history-complete-visible", "complete-history",
		"checkpoint-visible", "checkpoint-durable",
	}
	for _, boundary := range boundaries {
		t.Run(boundary, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "service")
			prior, target, priorAttempt := prepareCatalogRunIssuanceCrashFixture(t, root)
			runCatalogIssuanceCrashHelper(t, root, target, boundary)

			var preparedInfo os.FileInfo
			preservesPreparedGeneration := boundary == "prepare-full-visible" || boundary == "prepared-full" ||
				boundary == "directory-visible" || boundary == "published-directory" || boundary == "partial-history" ||
				boundary == "history-complete-visible" || boundary == "complete-history" ||
				boundary == "checkpoint-visible" || boundary == "checkpoint-durable"
			if preservesPreparedGeneration {
				preparedName := runNamespacePreparedName
				if boundary != "prepare-full-visible" && boundary != "prepared-full" {
					preparedName = storageComponent(target.RunID)
				}
				var err error
				preparedInfo, err = os.Stat(filepath.Join(root, "catalog", "runs", preparedName))
				if err != nil {
					t.Fatalf("prepared generation at %s: %v", boundary, err)
				}
			}

			catalog, err := Open(root)
			if err != nil {
				t.Fatalf("reopen after %s: %v", boundary, err)
			}
			defer catalog.Close()
			if preservesPreparedGeneration {
				finalInfo, statErr := os.Stat(filepath.Join(root, "catalog", "runs", storageComponent(target.RunID)))
				if statErr != nil || !os.SameFile(preparedInfo, finalInfo) {
					t.Fatalf("%s recovery reissued prepared run generation: final=%v err=%v", boundary, finalInfo, statErr)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "catalog", "runs", runNamespacePreparedName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s left the prepared name: %v", boundary, err)
			}
			if err := catalog.RegisterRun(target); err != nil {
				t.Fatalf("complete registration after %s: %v", boundary, err)
			}
			if err := catalog.RegisterRun(target); err != nil {
				t.Fatalf("byte-verify registration after %s: %v", boundary, err)
			}
			conflict := target
			conflict.InitialRegistrationTimestamp = time.Date(2026, 9, 15, 1, 2, 4, 0, time.UTC).Format(time.RFC3339Nano)
			if err := catalog.RegisterRun(conflict); !errors.Is(err, ErrConflict) {
				t.Fatalf("create-only registration after %s = %v", boundary, err)
			}
			for _, expected := range []RunRegistrationV1{prior, target} {
				observed, readErr := catalog.ReadRun(expected.RunID)
				if readErr != nil || observed != expected {
					t.Fatalf("read %s after %s = %#v, err=%v", expected.RunID, boundary, observed, readErr)
				}
			}
			listed, more, err := catalog.ListRuns("", 2)
			if err != nil || more || len(listed) != 2 {
				t.Fatalf("list after %s = %#v, more=%v err=%v", boundary, listed, more, err)
			}
			if observed, err := catalog.ReadAttempt(prior.RunID, priorAttempt.AttemptID); err != nil || observed != priorAttempt {
				t.Fatalf("prior attempt after %s = %#v, err=%v", boundary, observed, err)
			}
			targetAttempt, err := NewAttemptRegistrationV1(target.RunID, "attempt-crash", target.AuthorityDigest,
				time.Date(2026, 9, 15, 1, 2, 5, 0, time.UTC))
			if err != nil {
				t.Fatalf("attempt registration after %s: %v", boundary, err)
			}
			if err := catalog.RegisterAttempt(targetAttempt); err != nil {
				t.Fatalf("attempt registration after %s: %v", boundary, err)
			}
			process, err := recovery.CaptureProcessIdentity(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			lease, err := catalog.InstallOwnerLease(target.RunID, targetAttempt.AttemptID, process)
			if err != nil || lease.State != OwnerLeaseActive {
				t.Fatalf("owner installation after %s = %#v, err=%v", boundary, lease, err)
			}
		})
	}
}

func TestCatalogPreparedRunNamespaceRejectsGenerationReplacement(t *testing.T) {
	for _, object := range []string{"run", "attempts", "identity"} {
		t.Run(object, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "service")
			_, target, _ := prepareCatalogRunIssuanceCrashFixture(t, root)
			runCatalogIssuanceCrashHelper(t, root, target, "prepared-full")
			stage := filepath.Join(root, "catalog", "runs", runNamespacePreparedName)
			replaced := stage
			switch object {
			case "attempts":
				replaced = filepath.Join(stage, "attempts")
			case "identity":
				replaced = filepath.Join(stage, runNamespaceIdentityName)
			}
			detached := replaced + ".detached"
			if err := os.Rename(replaced, detached); err != nil {
				t.Fatal(err)
			}
			switch object {
			case "run":
				if err := os.Mkdir(replaced, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(replaced, "attempts"), 0o700); err != nil {
					t.Fatal(err)
				}
				writeRunNamespaceIdentity(t, replaced, storageComponent(target.RunID))
			case "attempts":
				if err := os.Mkdir(replaced, 0o700); err != nil {
					t.Fatal(err)
				}
			case "identity":
				copyCatalogTestFile(t, detached, replaced)
			}
			catalog, err := Open(root)
			if catalog != nil {
				_ = catalog.Close()
			}
			if !errors.Is(err, ErrIntegrity) {
				t.Fatalf("prepared %s replacement was accepted: %v", object, err)
			}
			if _, err := os.Stat(detached); err != nil {
				t.Fatalf("detached exact %s generation was modified: %v", object, err)
			}
		})
	}
}

func TestCatalogPreparedRunNamespaceRejectsNonPrefixHistoryTail(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	_, target, _ := prepareCatalogRunIssuanceCrashFixture(t, root)
	runCatalogIssuanceCrashHelper(t, root, target, "partial-history")
	historyPath := filepath.Join(root, "catalog", runGenerationsName)
	history, err := os.OpenFile(historyPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	info, err := history.Stat()
	if err != nil || info.Size() == 0 {
		_ = history.Close()
		t.Fatalf("partial history stat: %v", err)
	}
	if _, err := history.WriteAt([]byte("!"), info.Size()-1); err != nil {
		_ = history.Close()
		t.Fatal(err)
	}
	if err := history.Sync(); err != nil {
		_ = history.Close()
		t.Fatal(err)
	}
	if err := history.Close(); err != nil {
		t.Fatal(err)
	}
	catalog, err := Open(root)
	if catalog != nil {
		_ = catalog.Close()
	}
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("non-prefix partial authority record was accepted: %v", err)
	}
}

func prepareCatalogRunIssuanceCrashFixture(t *testing.T, root string) (RunRegistrationV1, RunRegistrationV1, AttemptRegistrationV1) {
	t.Helper()
	fixtureRoot := filepath.Dir(root)
	ledgerPath := filepath.Join(fixtureRoot, "catalog-crash-ledger.jsonl")
	evidenceRoot := filepath.Join(fixtureRoot, "catalog-crash-evidence")
	if err := os.WriteFile(ledgerPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(evidenceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 15, 1, 2, 3, 0, time.UTC)
	prior, err := NewRunRegistrationV1("run-prior", "example/repository", strings.Repeat("a", 64), ledgerPath, evidenceRoot, at)
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewRunRegistrationV1("run-crash", "example/repository", strings.Repeat("b", 64), ledgerPath, evidenceRoot, at)
	if err != nil {
		t.Fatal(err)
	}
	priorAttempt, err := NewAttemptRegistrationV1(prior.RunID, "attempt-prior", prior.AuthorityDigest, at)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterRun(prior); err != nil {
		_ = catalog.Close()
		t.Fatal(err)
	}
	if err := catalog.RegisterAttempt(priorAttempt); err != nil {
		_ = catalog.Close()
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	return prior, target, priorAttempt
}

func runCatalogIssuanceCrashHelper(t *testing.T, root string, target RunRegistrationV1, boundary string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestCatalogRunNamespaceIssuanceCrashHelper$")
	command.Env = append(os.Environ(),
		"ABCP_RUNTIME_CATALOG_ISSUANCE_HELPER="+root,
		"ABCP_RUNTIME_CATALOG_ISSUANCE_BOUNDARY="+boundary,
		"ABCP_RUNTIME_CATALOG_ISSUANCE_LEDGER="+target.CanonicalLedgerPath,
		"ABCP_RUNTIME_CATALOG_ISSUANCE_EVIDENCE="+target.CanonicalEvidenceRoot,
	)
	output, err := command.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 91 {
		t.Fatalf("issuance crash boundary %s exit=%v output=%s", boundary, err, output)
	}
}

func TestOwnerLeaseGenerationsAndExactProcessProof(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	at := time.Now().UTC()
	run := testRunRegistration(t, "run-owner", at)
	if err := catalog.RegisterRun(run); err != nil {
		t.Fatal(err)
	}
	attempt, err := NewAttemptRegistrationV1(run.RunID, "attempt-1", run.AuthorityDigest, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterAttempt(attempt); err != nil {
		t.Fatalf("register attempt: %v", err)
	}
	process, err := recovery.CaptureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	stale := process
	stale.LinuxStartTicks++
	if _, err := catalog.InstallOwnerLease(run.RunID, attempt.AttemptID, stale); err == nil {
		t.Fatal("stale/PID-reused owner identity was installed")
	}
	lease, err := catalog.InstallOwnerLease(run.RunID, attempt.AttemptID, process)
	if err != nil || lease.LeaseGeneration != 1 || lease.State != OwnerLeaseActive || VerifyLiveOwner(lease) != nil {
		t.Fatalf("first owner = %+v, err=%v", lease, err)
	}
	if _, err := catalog.InstallOwnerLease(run.RunID, attempt.AttemptID, process); err == nil {
		t.Fatal("active generation was overwritten")
	}
	reused := process
	reused.LinuxStartTicks++
	if LeaseMatchesProcess(lease, reused) {
		t.Fatal("PID-reused identity matched lease")
	}
	guard, err := catalog.AcquireOwnerLeaseGuard(run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Retire(lease.LeaseID); err == nil {
		t.Fatal("ACTIVE -> RETIRED was accepted")
	}
	closing, err := guard.MarkClosing(lease.LeaseID, 0)
	if err != nil || closing.State != OwnerLeaseClosing || closing.DrainThroughJournalSequence == nil {
		t.Fatalf("closing = %+v, err=%v", closing, err)
	}
	retired, err := guard.Retire(lease.LeaseID)
	if err != nil || retired.State != OwnerLeaseRetired {
		t.Fatalf("retired = %+v, err=%v", retired, err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := catalog.InstallOwnerLease(run.RunID, attempt.AttemptID, process)
	if err != nil || next.LeaseGeneration != 2 || next.LeaseID == lease.LeaseID {
		t.Fatalf("next owner = %+v, err=%v", next, err)
	}
}

func TestAttemptRegistrationIsBoundedAndCreateVerified(t *testing.T) {
	catalog, err := Open(filepath.Join(t.TempDir(), "service"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	at := time.Now().UTC()
	run := testRunRegistration(t, "run-attempt", at)
	if err := catalog.RegisterRun(run); err != nil {
		t.Fatal(err)
	}
	attempt, err := NewAttemptRegistrationV1(run.RunID, strings.Repeat("a", 256), run.AuthorityDigest, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterAttempt(attempt); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterAttempt(attempt); err != nil {
		t.Fatal(err)
	}
	attempt.RegistrationTimestamp = at.Add(time.Second).Format(time.RFC3339Nano)
	if err := catalog.RegisterAttempt(attempt); !errors.Is(err, ErrConflict) {
		t.Fatalf("attempt conflict = %v", err)
	}
}

func TestCreateVerifyRecoversPrePublicationTemporary(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	run := testRunRegistration(t, "run-pre-publish", time.Now().UTC())
	if err := catalog.RegisterRun(run); err != nil {
		t.Fatal(err)
	}
	attempt, err := NewAttemptRegistrationV1(run.RunID, "attempt-pre-publish", run.AuthorityDigest, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	data, err := canonicalJSON(attempt)
	if err != nil {
		t.Fatal(err)
	}
	runFD, err := catalog.openRunDirectory(run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(runFD)
	attemptsFD, err := openDirectoryAt(runFD, "attempts", false)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(attemptsFD)
	temporary, err := writeTemporary(attemptsFD, storageComponent(attempt.AttemptID)+".json", "create", data)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterAttempt(attempt); err != nil {
		t.Fatalf("registration did not recover pre-publication residue: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "catalog", "runs", run.RunID, "attempts", temporary)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-publication temporary remains: %v", err)
	}
}

func TestCreateVerifyRecoversPostPublicationTemporaryLink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	run := testRunRegistration(t, "run-post-publish", time.Now().UTC())
	if err := catalog.RegisterRun(run); err != nil {
		t.Fatal(err)
	}
	runDirectory := filepath.Join(root, "catalog", "runs", run.RunID)
	temporary := ".tmp-v1-create-" + sha256Bytes([]byte("run.json")) + "-" + strings.Repeat("0", 24)
	if err := os.Link(filepath.Join(runDirectory, "run.json"), filepath.Join(runDirectory, temporary)); err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterRun(run); err != nil {
		t.Fatalf("registration did not recover published temporary link: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(runDirectory, temporary)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("post-publication temporary link remains: %v", err)
	}
	if _, err := catalog.ReadRun(run.RunID); err != nil {
		t.Fatalf("recovered final registration is invalid: %v", err)
	}
}

func TestAtomicReplaceRecoversPreReplaceTemporary(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "lease.json")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent, err := syscall.Open(directory, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(parent)
	temporary, err := writeTemporary(parent, "lease.json", "replace", []byte("interrupted\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicReplace(parent, "lease.json", []byte("new\n")); err != nil {
		t.Fatalf("replace did not recover pre-replace residue: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new\n" {
		t.Fatalf("replacement = %q, err=%v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(directory, temporary)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-replace temporary remains: %v", err)
	}
}

func TestTemporaryRecoveryRejectsUnrecognizedAndUnsafeArtifacts(t *testing.T) {
	directory := t.TempDir()
	parent, err := syscall.Open(directory, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(parent)
	unknown := filepath.Join(directory, ".tmp-user-controlled")
	if err := os.WriteFile(unknown, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverTemporaryArtifacts(parent); err != nil {
		t.Fatalf("unrecognized names should remain visible to namespace validation: %v", err)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatalf("unrecognized temporary was removed: %v", err)
	}
	unsafe := filepath.Join(directory, ".tmp-v1-create-"+sha256Bytes([]byte("target"))+"-"+strings.Repeat("0", 24))
	if err := os.WriteFile(unsafe, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recoverTemporaryArtifacts(parent); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("unsafe recognized temporary = %v", err)
	}
}

func TestOwnerLeaseGuardLockAcquisitionIsBounded(t *testing.T) {
	catalog, err := Open(filepath.Join(t.TempDir(), "service"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	first, err := catalog.AcquireOwnerLeaseGuard("run-lock")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	started := time.Now()
	if _, err := catalog.AcquireOwnerLeaseGuard("run-lock"); !errors.Is(err, ErrBusy) {
		t.Fatalf("contended guard = %v", err)
	}
	if elapsed := time.Since(started); elapsed > LockTimeout+100*time.Millisecond {
		t.Fatalf("lock wait exceeded bound: %s", elapsed)
	}
}

func TestCatalogOwnerGenerationReplacementHelper(t *testing.T) {
	root := os.Getenv("ABCP_RUNTIME_CATALOG_REPLACEMENT_HELPER")
	if root == "" {
		return
	}
	catalog, err := Open(root)
	if err != nil {
		os.Exit(30)
	}
	guard, err := catalog.AcquireOwnerLeaseGuard("run-replacement")
	if err != nil {
		os.Exit(31)
	}
	lease, err := guard.Lease()
	if err != nil || lease.LeaseGeneration != 1 || lease.State != OwnerLeaseRetired {
		os.Exit(37)
	}
	fmt.Println("held")
	var signal string
	if _, err := fmt.Fscanln(os.Stdin, &signal); err != nil || signal != "continue" {
		os.Exit(32)
	}
	process, err := recovery.CaptureProcessIdentity(os.Getpid())
	if err != nil {
		os.Exit(33)
	}
	if _, err := guard.Install("attempt-replacement", process); !errors.Is(err, ErrIntegrity) {
		os.Exit(34)
	}
	if err := guard.Close(); !errors.Is(err, ErrIntegrity) {
		os.Exit(35)
	}
	fmt.Println("integrity")
	if err := catalog.Close(); err != nil {
		os.Exit(36)
	}
}

func TestCatalogCrossProcessGenerationReplacementFailsClosed(t *testing.T) {
	for _, attack := range []string{"lock-unlink", "active-rename", "runs-rename", "run-authority-rename", "catalog-rename"} {
		t.Run(attack, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "service")
			catalog, retired, retiredData := prepareRetiredOwnerGeneration(t, root)
			defer catalog.Close()

			command := exec.Command(os.Args[0], "-test.run=^TestCatalogOwnerGenerationReplacementHelper$")
			command.Env = append(os.Environ(), "ABCP_RUNTIME_CATALOG_REPLACEMENT_HELPER="+root)
			stdin, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			finished := false
			defer func() {
				if !finished {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			}()
			output := bufio.NewReader(stdout)
			if line, err := output.ReadString('\n'); err != nil || line != "held\n" {
				t.Fatalf("owner helper readiness = %q, err=%v", line, err)
			}

			originalActive := filepath.Join(root, "active")
			switch attack {
			case "lock-unlink":
				lockPath := filepath.Join(root, "catalog", ".lock")
				if err := os.Remove(lockPath); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "active-rename":
				if err := os.Rename(originalActive, originalActive+".retired"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(originalActive, 0o700); err != nil {
					t.Fatal(err)
				}
			case "runs-rename":
				runs := filepath.Join(root, "catalog", "runs")
				if err := os.Rename(runs, runs+".retired"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(runs, 0o700); err != nil {
					t.Fatal(err)
				}
			case "run-authority-rename":
				authorityPath := filepath.Join(root, "catalog", runGenerationsName)
				if err := os.Rename(authorityPath, authorityPath+".retired"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(authorityPath, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "catalog-rename":
				catalogPath := filepath.Join(root, "catalog")
				if err := os.Rename(catalogPath, catalogPath+".retired"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(catalogPath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(catalogPath, "runs"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(catalogPath, ".lock"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := catalog.AcquireOwnerLeaseGuard(retired.RunID); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("replacement generation contender = %v", err)
			}
			fresh, err := Open(root)
			if fresh != nil {
				_ = fresh.Close()
			}
			if !errors.Is(err, ErrIntegrity) {
				t.Fatalf("fresh process adopted replacement generation: %v", err)
			}

			if _, err := fmt.Fprintln(stdin, "continue"); err != nil {
				t.Fatal(err)
			}
			if err := stdin.Close(); err != nil {
				t.Fatal(err)
			}
			if line, err := output.ReadString('\n'); err != nil || line != "integrity\n" {
				t.Fatalf("owner helper result = %q, err=%v", line, err)
			}
			if err := command.Wait(); err != nil {
				t.Fatalf("owner helper exit = %v", err)
			}
			finished = true

			activeRecord := filepath.Join(originalActive, storageComponent(retired.RunID)+".json")
			if attack == "active-rename" {
				activeRecord = filepath.Join(originalActive+".retired", storageComponent(retired.RunID)+".json")
				if _, err := os.Stat(filepath.Join(originalActive, storageComponent(retired.RunID)+".json")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("replacement active namespace received an owner: %v", err)
				}
			}
			data, err := os.ReadFile(activeRecord)
			if err != nil || string(data) != string(retiredData) {
				t.Fatalf("retired generation changed: data=%q err=%v", data, err)
			}
		})
	}
}

func TestCatalogRunAndAttemptsNamespaceReplacementFailsClosed(t *testing.T) {
	for _, attack := range []string{"run", "attempts"} {
		t.Run(attack, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "service")
			catalog, retired, _ := prepareRetiredOwnerGeneration(t, root)
			defer catalog.Close()
			runPath := filepath.Join(root, "catalog", "runs", storageComponent(retired.RunID))
			replaced := runPath
			if attack == "attempts" {
				replaced = filepath.Join(runPath, "attempts")
			}
			if err := os.Rename(replaced, replaced+".retired"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(replaced, 0o700); err != nil {
				t.Fatal(err)
			}
			if attack == "run" {
				if err := os.Mkdir(filepath.Join(replaced, "attempts"), 0o700); err != nil {
					t.Fatal(err)
				}
				copyCatalogTestFile(t, filepath.Join(runPath+".retired", "run.json"), filepath.Join(runPath, "run.json"))
				copyCatalogTestFile(t,
					filepath.Join(runPath+".retired", "attempts", storageComponent(retired.AttemptID)+".json"),
					filepath.Join(runPath, "attempts", storageComponent(retired.AttemptID)+".json"))
				writeRunNamespaceIdentity(t, runPath, storageComponent(retired.RunID))
			} else {
				copyCatalogTestFile(t,
					filepath.Join(replaced+".retired", storageComponent(retired.AttemptID)+".json"),
					filepath.Join(replaced, storageComponent(retired.AttemptID)+".json"))
				runs, _, err := catalog.ListRuns("", 1)
				if err != nil || len(runs) != 1 || runs[0].RunID != retired.RunID {
					t.Fatalf("catalog listing opened replaced attempts namespace: runs=%#v err=%v", runs, err)
				}
			}
			var readErr error
			if attack == "run" {
				_, readErr = catalog.ReadRun(retired.RunID)
			} else {
				_, readErr = catalog.ReadAttempt(retired.RunID, retired.AttemptID)
			}
			if !errors.Is(readErr, ErrIntegrity) {
				t.Fatalf("replaced run namespace read = %v", readErr)
			}
			fresh, err := Open(root)
			if err != nil {
				t.Fatalf("open fixed catalog generation after child replacement: %v", err)
			}
			defer fresh.Close()
			if attack == "run" {
				_, readErr = fresh.ReadRun(retired.RunID)
			} else {
				_, readErr = fresh.ReadAttempt(retired.RunID, retired.AttemptID)
			}
			if !errors.Is(readErr, ErrIntegrity) {
				t.Fatalf("fresh catalog adopted replaced run namespace: %v", readErr)
			}
			process, err := recovery.CaptureProcessIdentity(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := catalog.InstallOwnerLease(retired.RunID, retired.AttemptID, process); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("replaced run namespace owner install = %v", err)
			}
		})
	}
}

func copyCatalogTestFile(t *testing.T, source, destination string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeRunNamespaceIdentity(t *testing.T, runPath, component string) {
	t.Helper()
	runInfo, err := os.Stat(runPath)
	if err != nil {
		t.Fatal(err)
	}
	attemptsInfo, err := os.Stat(filepath.Join(runPath, "attempts"))
	if err != nil {
		t.Fatal(err)
	}
	runStat, runOK := runInfo.Sys().(*syscall.Stat_t)
	attemptsStat, attemptsOK := attemptsInfo.Sys().(*syscall.Stat_t)
	if !runOK || !attemptsOK {
		t.Fatal("replacement namespace lacks Linux identity")
	}
	identity := runNamespaceIdentityV1{
		Kind: "RuntimeCatalogRunNamespaceIdentityV1", SchemaVersion: 1, RunComponent: component,
		RunDevice: uint64(runStat.Dev), RunInode: runStat.Ino,
		AttemptsDevice: uint64(attemptsStat.Dev), AttemptsInode: attemptsStat.Ino,
	}
	data, err := canonicalJSON(identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runPath, runNamespaceIdentityName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func prepareRetiredOwnerGeneration(t *testing.T, root string) (*Catalog, ActiveOwnerLeaseV1, []byte) {
	t.Helper()
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	run := testRunRegistration(t, "run-replacement", at)
	if err := catalog.RegisterRun(run); err != nil {
		t.Fatal(err)
	}
	attempt, err := NewAttemptRegistrationV1(run.RunID, "attempt-replacement", run.AuthorityDigest, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RegisterAttempt(attempt); err != nil {
		t.Fatal(err)
	}
	process, err := recovery.CaptureProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := catalog.InstallOwnerLease(run.RunID, attempt.AttemptID, process)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := catalog.AcquireOwnerLeaseGuard(run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.MarkClosing(lease.LeaseID, 0); err != nil {
		_ = guard.Close()
		t.Fatal(err)
	}
	retired, err := guard.Retire(lease.LeaseID)
	if err != nil {
		_ = guard.Close()
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := canonicalJSON(retired)
	if err != nil {
		t.Fatal(err)
	}
	return catalog, retired, data
}

func TestFrozenCatalogResourceCeilings(t *testing.T) {
	if MaxRuns != 10_000 || MaxAttemptsPerRun != 1_000 || MaxActiveOwners != 256 {
		t.Fatalf("catalog count ceilings changed: runs=%d attempts=%d active=%d", MaxRuns, MaxAttemptsPerRun, MaxActiveOwners)
	}
	if MaxRegistrationSize != 64<<10 || MaxCatalogReadBytes != 16<<20 || LockTimeout != 2*time.Second {
		t.Fatalf("catalog byte/lock ceilings changed: record=%d read=%d lock=%s", MaxRegistrationSize, MaxCatalogReadBytes, LockTimeout)
	}
	if MaxCatalogPageSize != 200 || MaxRuns*MaxRunIDIndexSize+MaxCatalogPageSize*MaxRegistrationSize > MaxCatalogReadBytes {
		t.Fatal("long-ID key index plus maximal registration page exceeds the physical read ceiling")
	}
	worst := runNamespaceGenerationV2{
		Kind: "RuntimeCatalogRunNamespaceGenerationV2", SchemaVersion: 2, Sequence: MaxRuns,
		PriorRecordSHA256: strings.Repeat("a", 64), RunComponent: "sha256-" + strings.Repeat("b", 64),
		RunDevice: ^uint64(0), RunInode: ^uint64(0), AttemptsDevice: ^uint64(0), AttemptsInode: ^uint64(0),
		IdentityDevice: ^uint64(0), IdentityInode: ^uint64(0), IdentitySHA256: strings.Repeat("c", 64),
	}
	if data, err := marshalRunNamespaceGeneration(worst); err != nil || len(data) > maxRunGenerationRecord {
		t.Fatalf("maximal run-generation record exceeds its bound: bytes=%d err=%v", len(data), err)
	}
}

func TestCatalogListReadsOnlySelectedPageWithinAggregateByteCeiling(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	runsRoot := filepath.Join(root, "catalog", "runs")
	longInternalPath := "/" + strings.Repeat("p", 3900)
	total := 0
	for index := 0; total <= MaxCatalogReadBytes; index++ {
		if index >= MaxRuns {
			t.Fatal("fixture could not reach catalog byte ceiling")
		}
		runID := fmt.Sprintf("run-%04d", index)
		record := RunRegistrationV1{
			Kind: "RunRegistrationV1", RunID: runID,
			RepositoryIdentityDigest: strings.Repeat("a", 64), AuthorityDigest: strings.Repeat("b", 64),
			CanonicalLedgerPath: longInternalPath, LedgerGeneration: testLedgerGeneration(), CanonicalEvidenceRoot: longInternalPath,
			InitialRegistrationTimestamp: "2026-09-13T00:00:00Z",
		}
		data, err := canonicalJSON(record)
		if err != nil {
			t.Fatal(err)
		}
		runDirectory := filepath.Join(runsRoot, runID)
		namespace, err := catalog.createRunNamespace(runID)
		if err != nil {
			t.Fatal(err)
		}
		if err := namespace.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runDirectory, "run.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		total += len(data)
	}
	first, more, err := catalog.ListRuns("", 200)
	if err != nil || !more || len(first) != 200 || first[0].RunID != "run-0000" || first[199].RunID != "run-0199" {
		t.Fatalf("bounded first page = %d rows, more=%v, err=%v", len(first), more, err)
	}
	inserted := RunRegistrationV1{
		Kind: "RunRegistrationV1", RunID: "run-0199a",
		RepositoryIdentityDigest: strings.Repeat("a", 64), AuthorityDigest: strings.Repeat("b", 64),
		CanonicalLedgerPath: longInternalPath, LedgerGeneration: testLedgerGeneration(), CanonicalEvidenceRoot: longInternalPath,
		InitialRegistrationTimestamp: "2026-09-13T00:00:00Z",
	}
	data, err := canonicalJSON(inserted)
	if err != nil {
		t.Fatal(err)
	}
	insertedDirectory := filepath.Join(runsRoot, inserted.RunID)
	namespace, err := catalog.createRunNamespace(inserted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := namespace.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(insertedDirectory, "run.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	second, _, err := catalog.ListRuns("run-0199", 2)
	if err != nil || len(second) != 2 || second[0].RunID != "run-0199a" || second[1].RunID != "run-0200" {
		t.Fatalf("insertion continuation = %#v, err=%v", second, err)
	}
}

func TestCatalogLongIdentifierIndexKeepsLargeListingBoundedAndOrdered(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	at := time.Now().UTC()
	for index := 399; index >= 0; index-- {
		prefix := fmt.Sprintf("%04d-", index)
		runID := prefix + strings.Repeat("x", 256-len(prefix))
		if err := catalog.RegisterRun(testRunRegistration(t, runID, at)); err != nil {
			t.Fatalf("register long run %d: %v", index, err)
		}
	}
	page, more, err := catalog.ListRuns("", 200)
	if err != nil || !more || len(page) != 200 || !strings.HasPrefix(page[0].RunID, "0000-") || !strings.HasPrefix(page[199].RunID, "0199-") {
		t.Fatalf("long-ID first page = %d rows, more=%v, err=%v", len(page), more, err)
	}
}

func TestCatalogReservedSHA256RunIDsCoexistWithLongIdentifierComponent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service")
	catalog, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()

	longID := strings.Repeat("l", 256)
	exactHashShapedID := storageComponent(longID)
	ids := []string{"sha256-short", exactHashShapedID, longID, "run-middle"}
	components := make(map[string]string, len(ids))
	registrations := make(map[string]RunRegistrationV1, len(ids))
	at := time.Date(2026, 9, 13, 4, 5, 6, 0, time.UTC)
	for _, id := range ids {
		if !ValidIdentifier(id) {
			t.Fatalf("fixture ID is outside public grammar: %q", id)
		}
		component := storageComponent(id)
		if priorID, duplicate := components[component]; duplicate {
			t.Fatalf("IDs %q and %q share storage component %q", priorID, id, component)
		}
		components[component] = id
		if strings.HasPrefix(id, encodedStorageComponentPrefix) {
			if component == id || !isEncodedStorageComponent(component) {
				t.Fatalf("reserved-prefix ID %q mapped to %q", id, component)
			}
		}

		registration := testRunRegistration(t, id, at)
		registrations[id] = registration
		if err := catalog.RegisterRun(registration); err != nil {
			t.Fatalf("register %q: %v", id, err)
		}
		if err := catalog.RegisterRun(registration); err != nil {
			t.Fatalf("immutable byte-verify %q: %v", id, err)
		}
	}
	if components[exactHashShapedID] != longID {
		t.Fatalf("long ID did not map to its expected hash-shaped component %q", exactHashShapedID)
	}
	if storageComponent(exactHashShapedID) == exactHashShapedID {
		t.Fatal("exact hash-shaped valid ID aliases the long ID component")
	}

	runsRoot := filepath.Join(root, "catalog", "runs")
	for component, id := range components {
		if component == id {
			continue
		}
		data, err := os.ReadFile(filepath.Join(runsRoot, component, "run-id"))
		if err != nil || string(data) != id+"\n" {
			t.Fatalf("sidecar for %q = %q, err=%v", id, data, err)
		}
	}
	for _, id := range ids {
		got, err := catalog.ReadRun(id)
		if err != nil || got != registrations[id] {
			t.Fatalf("read %q = %#v, err=%v", id, got, err)
		}
	}

	sortedIDs := append([]string(nil), ids...)
	sort.Strings(sortedIDs)
	first, more, err := catalog.ListRuns("", 2)
	if err != nil || !more || len(first) != 2 || first[0].RunID != sortedIDs[0] || first[1].RunID != sortedIDs[1] {
		t.Fatalf("first page = %#v, more=%v, err=%v", first, more, err)
	}
	second, more, err := catalog.ListRuns(first[1].RunID, 2)
	if err != nil || more || len(second) != 2 || second[0].RunID != sortedIDs[2] || second[1].RunID != sortedIDs[3] {
		t.Fatalf("second page = %#v, more=%v, err=%v", second, more, err)
	}
}

func TestReopenedCatalogDescriptorRequiresAllProtectedAttributes(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "record")
	if err := os.WriteFile(path, []byte("record"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	var initial syscall.Stat_t
	if err := syscall.Fstat(fd, &initial); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*syscall.Stat_t){
		"type":  func(stat *syscall.Stat_t) { stat.Mode = stat.Mode&^syscall.S_IFMT | syscall.S_IFIFO },
		"owner": func(stat *syscall.Stat_t) { stat.Uid++ },
		"mode":  func(stat *syscall.Stat_t) { stat.Mode = stat.Mode&^0o7777 | 0o640 },
		"links": func(stat *syscall.Stat_t) { stat.Nlink = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			current := initial
			mutate(&current)
			if validReopenedProtectedFileStat(current, initial) {
				t.Fatal("unsafe final reopened descriptor accepted")
			}
		})
	}
}

func testRunRegistration(t *testing.T, id string, at time.Time) RunRegistrationV1 {
	t.Helper()
	root := t.TempDir()
	ledgerPath := filepath.Join(root, "ledger.jsonl")
	evidenceRoot := filepath.Join(root, "evidence")
	if err := os.WriteFile(ledgerPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(evidenceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	registration, err := NewRunRegistrationV1(id, "example/repository", strings.Repeat("a", 64), ledgerPath, evidenceRoot, at)
	if err != nil {
		t.Fatal(err)
	}
	return registration
}

func testLedgerGeneration() LedgerGenerationV1 {
	return LedgerGenerationV1{Kind: "LedgerGenerationV1", ParentDevice: 1, ParentInode: 1, FileDevice: 1, FileInode: 1}
}

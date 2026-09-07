//go:build linux

package prlifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
)

func provisionStore(t *testing.T) *PRWriteAdmissionStore {
	t.Helper()
	root := filepath.Join(t.TempDir(), "admission")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "capacity.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewPRWriteAdmissionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testResourceKey(t *testing.T, branch string) PRResourceKeyV1 {
	t.Helper()
	repository, _ := githublifecycle.NewRepository("octo", "control")
	base, _ := githublifecycle.NewBranch("main")
	head, _ := githublifecycle.NewBranch(branch)
	key, err := NewPRResourceKey(repository, base, head)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestAdmissionStoreRequiresProvisionedStableSafeRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-lock")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPRWriteAdmissionStore(root); err == nil {
		t.Fatal("missing administrator capacity lock accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "capacity.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPRWriteAdmissionStore(root); err == nil {
		t.Fatal("unsafe capacity-lock permissions accepted")
	}
}

func TestAdmissionStoreImmutableRecordsReservationAndUnknownEntry(t *testing.T) {
	store := provisionStore(t)
	key := testResourceKey(t, "feature")
	err := store.withResource(key, func(tx *resourceTxn) error {
		name := recordPrefix(key, 1) + "submitted.json"
		if _, _, err := tx.create(name, []byte(`{"marker":1}`), true, false); err != nil {
			return err
		}
		if _, _, err := tx.create(name, []byte(`{"marker":1}`), true, false); err != nil {
			return err
		}
		if _, _, err := tx.create(name, []byte(`{"marker":2}`), true, false); err == nil || !strings.Contains(err.Error(), CodeIntegrityFailure) {
			return errors.New("conflicting immutable record accepted")
		}
		if _, _, err := tx.create(recordPrefix(key, 1)+"terminal.json", []byte(`{"terminal":1}`), false, true); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Root(), "unknown"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	other := testResourceKey(t, "other")
	if err := store.withResource(other, func(*resourceTxn) error { return nil }); err == nil || !strings.Contains(err.Error(), CodeIntegrityFailure) {
		t.Fatalf("unknown root entry did not fail closed: %v", err)
	}
}

func TestAdmissionCapacityCountsNewPhysicalResource(t *testing.T) {
	store := provisionStore(t)
	store.policy.MaxAdmissionResources = 1
	first := testResourceKey(t, "one")
	if err := store.withResource(first, func(*resourceTxn) error { return nil }); err != nil {
		t.Fatal(err)
	}
	second := testResourceKey(t, "two")
	if err := store.withResource(second, func(*resourceTxn) error { return nil }); err == nil || !strings.Contains(err.Error(), CodeCapacityExhausted) {
		t.Fatalf("resource capacity was not enforced: %v", err)
	}
}

func TestAdmissionStoreDetectsResourceLockReplacement(t *testing.T) {
	store := provisionStore(t)
	key := testResourceKey(t, "stable-lock")
	if err := store.withResource(key, func(*resourceTxn) error { return nil }); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(store.Root(), "r-"+key.String()+".lock")
	oldPath := lockPath + ".old"
	if err := os.Rename(lockPath, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.withResource(key, func(*resourceTxn) error { return nil }); err == nil || !strings.Contains(err.Error(), CodeIntegrityFailure) {
		t.Fatalf("replacement resource lock accepted: %v", err)
	}
}

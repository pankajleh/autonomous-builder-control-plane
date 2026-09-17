//go:build linux

package prlifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPredecessorDrainSnapshotRejectsUnclassifiedPRState(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "capacity.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := newPRWriteAdmissionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.rootDir.Close()
	first, err := store.predecessorDrainSnapshotV1()
	if err != nil || len(first) != 64 {
		t.Fatalf("empty PR drain snapshot = %q, %v", first, err)
	}
	second, err := store.predecessorDrainSnapshotV1()
	if err != nil || second != first {
		t.Fatalf("PR drain snapshot is not deterministic: %q, %v", second, err)
	}
	if err := os.WriteFile(filepath.Join(root, "unclassified.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.predecessorDrainSnapshotV1(); err == nil || !strings.Contains(err.Error(), "V3_DRAIN_REQUIRED") {
		t.Fatalf("unclassified PR state drain error = %v", err)
	}
}

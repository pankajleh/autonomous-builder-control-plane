//go:build linux

package mergelifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPredecessorDrainSnapshotRejectsUnclassifiedMergeState(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := newDurableStore(root, productionLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	first, err := store.predecessorDrainSnapshotV1()
	if err != nil || len(first) != 64 {
		t.Fatalf("empty merge drain snapshot = %q, %v", first, err)
	}
	second, err := store.predecessorDrainSnapshotV1()
	if err != nil || second != first {
		t.Fatalf("merge drain snapshot is not deterministic: %q, %v", second, err)
	}
	if err := os.WriteFile(filepath.Join(root, "unclassified.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.predecessorDrainSnapshotV1(); err == nil || !strings.Contains(err.Error(), "V3_DRAIN_REQUIRED") {
		t.Fatalf("unclassified merge state drain error = %v", err)
	}
}

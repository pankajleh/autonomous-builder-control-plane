package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWriteBytesReturnsExactHashAndReference(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root, "run-1")
	if err != nil {
		t.Fatal(err)
	}

	data := []byte("exact bytes\x00\n")
	ref, err := store.WriteBytes("stdout.log", "process-stdout", data)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if want := hex.EncodeToString(digest[:]); ref.SHA256 != want {
		t.Fatalf("SHA256 = %q, want %q", ref.SHA256, want)
	}
	if ref.Kind != "process-stdout" {
		t.Fatalf("kind = %q, want process-stdout", ref.Kind)
	}
	if ref.URI != filepath.Join(store.RunDir(), "stdout.log") {
		t.Fatalf("URI = %q, want artifact in %q", ref.URI, store.RunDir())
	}
	written, err := os.ReadFile(ref.URI)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(written, data) {
		t.Fatalf("written bytes = %q, want %q", written, data)
	}
	if filepath.Dir(ref.URI) != store.RunDir() {
		t.Fatalf("artifact escaped run directory: %q", ref.URI)
	}
}

func TestWriteRejectsUnsafeNamesAndRunIDs(t *testing.T) {
	root := t.TempDir()
	for _, runID := range []string{"", ".", "..", "../escape", `..\\escape`, "/absolute"} {
		t.Run("runID_"+runID, func(t *testing.T) {
			if _, err := NewStore(root, runID); err == nil {
				t.Fatalf("NewStore accepted unsafe run ID %q", runID)
			}
		})
	}

	store, err := NewStore(root, "safe-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".", "..", "../escape", "subdir/file", `subdir\\file`, "/absolute"} {
		t.Run("artifact_"+name, func(t *testing.T) {
			if _, err := store.WriteBytes(name, "test", []byte("unsafe")); err == nil {
				t.Fatalf("WriteBytes accepted unsafe artifact name %q", name)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe path created outside run directory: %v", err)
	}
}

func TestWriteBytesDoesNotOverwriteArtifact(t *testing.T) {
	store, err := NewStore(t.TempDir(), "run-immutable")
	if err != nil {
		t.Fatal(err)
	}
	first := []byte("original evidence")
	ref, err := store.WriteBytes("result.txt", "test-result", first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteBytes("result.txt", "test-result", []byte("replacement")); !errors.Is(err, ErrArtifactExists) {
		t.Fatalf("second write error = %v, want ErrArtifactExists", err)
	}
	written, err := os.ReadFile(ref.URI)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(written, first) {
		t.Fatalf("artifact mutated to %q, want %q", written, first)
	}
}

func TestWriteJSONStoresExactMetadataWithoutMutation(t *testing.T) {
	store, err := NewStore(t.TempDir(), "run-json")
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{"exit_code": float64(0), "argv": []string{"go", "test", "./..."}}
	ref, err := store.WriteJSON("command.json", "command-metadata", metadata)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"argv":["go","test","./..."],"exit_code":0}`)
	written, err := os.ReadFile(ref.URI)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(written, want) {
		t.Fatalf("JSON bytes = %s, want %s", written, want)
	}
	if _, err := store.WriteJSON("command.json", "command-metadata", map[string]any{"exit_code": 1}); !errors.Is(err, ErrArtifactExists) {
		t.Fatalf("second JSON write error = %v, want ErrArtifactExists", err)
	}
}

func TestStoresUseIndependentRunDirectories(t *testing.T) {
	root := t.TempDir()
	first, err := NewStore(root, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(root, "run-b")
	if err != nil {
		t.Fatal(err)
	}
	firstRef, err := first.WriteBytes("result.txt", "result", []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	secondRef, err := second.WriteBytes("result.txt", "result", []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if first.RunDir() == second.RunDir() || firstRef.URI == secondRef.URI {
		t.Fatalf("run stores are not independent: %q and %q", firstRef.URI, secondRef.URI)
	}
	if filepath.Dir(first.RunDir()) != first.Root() || filepath.Dir(second.RunDir()) != second.Root() {
		t.Fatal("run directories are not contained directly beneath the evidence root")
	}
}

func TestNewStoreRejectsSymlinkRunDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "run-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(root, "run-link"); err == nil {
		t.Fatal("NewStore accepted a symlink run directory")
	}
}

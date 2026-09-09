package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"syscall"
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

func TestWriteBytesConcurrentPublishHasSingleWinner(t *testing.T) {
	store, err := NewStore(t.TempDir(), "run-concurrent")
	if err != nil {
		t.Fatal(err)
	}

	const writers = 32
	start := make(chan struct{})
	type result struct {
		payload []byte
		ref     string
		err     error
	}
	results := make(chan result, writers)
	var group sync.WaitGroup
	for index := 0; index < writers; index++ {
		payload := []byte("writer-" + strconv.Itoa(index))
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			ref, writeErr := store.WriteBytes("shared.txt", "concurrency-test", payload)
			results <- result{payload: payload, ref: ref.URI, err: writeErr}
		}()
	}
	close(start)
	group.Wait()
	close(results)

	var winner result
	winners := 0
	for got := range results {
		if got.err == nil {
			winner = got
			winners++
			continue
		}
		if !errors.Is(got.err, ErrArtifactExists) {
			t.Fatalf("concurrent writer returned %v, want ErrArtifactExists", got.err)
		}
	}
	if winners != 1 {
		t.Fatalf("successful writers = %d, want exactly one", winners)
	}
	written, err := os.ReadFile(winner.ref)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(written, winner.payload) {
		t.Fatalf("stored bytes = %q, winner supplied %q", written, winner.payload)
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

func TestWriteBytesDurabilityFaultsFailClosed(t *testing.T) {
	t.Run("link", func(t *testing.T) {
		store, err := NewStore(t.TempDir(), "run-link-fault")
		if err != nil {
			t.Fatal(err)
		}
		store.link = func(string, string) error { return errors.New("injected link failure") }
		if _, err := store.WriteBytes("result.txt", "test", []byte("payload")); err == nil {
			t.Fatal("link failure was ignored")
		}
		if _, err := os.Lstat(filepath.Join(store.RunDir(), "result.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("link failure published a destination: %v", err)
		}
	})

	t.Run("unlink", func(t *testing.T) {
		store, err := NewStore(t.TempDir(), "run-unlink-fault")
		if err != nil {
			t.Fatal(err)
		}
		store.remove = func(string) error { return errors.New("injected unlink failure") }
		if _, err := store.WriteBytes("result.txt", "test", []byte("payload")); err == nil {
			t.Fatal("temporary-link removal failure was ignored")
		}
		info, err := os.Lstat(filepath.Join(store.RunDir(), "result.txt"))
		if err != nil {
			t.Fatal(err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatal("evidence stat is not syscall.Stat_t")
		}
		if stat.Nlink != 2 {
			t.Fatalf("failed temporary-link removal left nlink=%d, want 2", stat.Nlink)
		}
	})

	for _, stage := range []string{"final-file-sync", "directory-sync"} {
		t.Run(stage, func(t *testing.T) {
			store, err := NewStore(t.TempDir(), "run-"+stage)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "final-file-sync" {
				calls := 0
				store.syncFile = func(file *os.File) error {
					calls++
					if calls == 2 {
						return errors.New("injected final file sync failure")
					}
					return file.Sync()
				}
			} else {
				store.syncDir = func(string) error { return errors.New("injected directory sync failure") }
			}
			if _, err := store.WriteBytes("result.txt", "test", []byte("payload")); err == nil {
				t.Fatalf("%s was ignored", stage)
			}
			store.syncFile = func(file *os.File) error { return file.Sync() }
			store.syncDir = syncDirectory
			if _, err := store.WriteBytes("result.txt", "test", []byte("payload")); !errors.Is(err, ErrArtifactExists) {
				t.Fatalf("%s retry did not durably verify the existing path: %v", stage, err)
			}
			info, err := os.Lstat(filepath.Join(store.RunDir(), "result.txt"))
			if err != nil {
				t.Fatal(err)
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				t.Fatal("evidence stat is not syscall.Stat_t")
			}
			if stat.Nlink != 1 {
				t.Fatalf("%s retry left nlink=%d, want 1", stage, stat.Nlink)
			}
		})
	}
}

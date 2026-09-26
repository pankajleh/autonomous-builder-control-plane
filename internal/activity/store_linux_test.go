//go:build linux

package activity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "activity"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStoreRestartDedupeAndGlobalResumeIdentity(t *testing.T) {
	s := newStore(t)
	a, err := s.Append("a", "registration", provider(t, "a", "1"))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Append("a", "registration", provider(t, "a", "1"))
	if err != nil || replay.Ordinal != a.Ordinal {
		t.Fatal("duplicate appended", err)
	}
	b, err := s.Append("b", "registration", provider(t, "b", "1"))
	if err != nil || b.Ordinal <= a.Ordinal {
		t.Fatal("ordinal reuse", err)
	}
	if _, _, _, err = s.read("b", "registration", a.Ordinal, 100); err == nil {
		t.Fatal("cross-run ordinal accepted")
	}
	if _, _, _, err = s.read("a", "other-registration", 0, 100); err == nil {
		t.Fatal("catalog generation changed")
	}
	if _, err = OpenStore(s.root); err == nil {
		t.Fatal("concurrent owner acquired store")
	}
	s.Close()
	recovered, err := OpenStore(s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	replay, err = recovered.Append("a", "registration", provider(t, "a", "1"))
	if err != nil || replay.Ordinal != a.Ordinal {
		t.Fatal("restart duplicate", err)
	}
	next, err := recovered.Append("a", "registration", provider(t, "a", "2"))
	if err != nil || next.Ordinal <= b.Ordinal {
		t.Fatal("restart ordinal reused", err)
	}
	events, _, more, err := recovered.read("a", "registration", a.Ordinal, 100)
	if err != nil || more || len(events) != 1 || events[0].Ordinal != next.Ordinal {
		t.Fatal("resume was not strictly after", err)
	}
}

func TestStoreConcurrentReplay(t *testing.T) {
	s := newStore(t)
	e := provider(t, "run", "1")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Append("run", "reg", e); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	events, _, _, err := s.read("run", "reg", 0, 100)
	if err != nil || len(events) != 1 {
		t.Fatal("concurrent duplicate", err)
	}
}

func TestStoreTamperTruncationReplacementAndSymlinks(t *testing.T) {
	for _, mode := range []string{"edit", "partial", "replace", "symlink", "hardlink", "permissions"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(t)
			if _, err := s.Append("run", "reg", provider(t, "run", "1")); err != nil {
				t.Fatal(err)
			}
			path := s.path("run")
			data, _ := os.ReadFile(path)
			switch mode {
			case "edit":
				data = []byte(strings.Replace(string(data), "finished", "tampered", 1))
				os.WriteFile(path, data, 0600)
			case "partial":
				os.WriteFile(path, data[:len(data)-1], 0600)
			case "replace":
				os.Remove(path)
				os.WriteFile(path, data, 0600)
			case "symlink":
				os.Rename(path, path+".old")
				os.Symlink(path+".old", path)
			case "hardlink":
				os.Link(path, path+".link")
			case "permissions":
				os.Chmod(path, 0644)
			}
			if _, _, _, err := s.read("run", "reg", 0, 100); err == nil {
				t.Fatal("live tampering accepted")
			}
			if mode == "edit" || mode == "partial" || mode == "symlink" || mode == "hardlink" || mode == "permissions" {
				s.Close()
				fresh, err := OpenStore(s.root)
				if err == nil {
					defer fresh.Close()
					if _, _, _, err = fresh.read("run", "reg", 0, 100); err == nil {
						t.Fatal("restart tampering accepted")
					}
				}
			}
		})
	}
}

func TestStoreBoundsFailClosed(t *testing.T) {
	s := newStore(t)
	if _, err := s.Append("run", "reg", provider(t, "run", "1")); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.cache["run"].events = make([]Event, MaxEvents)
	s.mu.Unlock()
	if _, err := s.Append("run", "reg", provider(t, "run", "2")); !errors.Is(err, ErrExhausted) {
		t.Fatal("event limit", err)
	}
	if _, _, _, err := s.read("run", "reg", 0, 100); !errors.Is(err, ErrExhausted) {
		t.Fatal("exhaustion did not close reads", err)
	}
	if _, err := s.Append("other", "reg", provider(t, "other", "1")); err != nil {
		t.Fatal("failure crossed run boundary", err)
	}
	log := &runLog{bytes: MaxBytes - 1}
	if err := s.write("unused", log, logRecord{Event: &Event{}}); !errors.Is(err, ErrExhausted) {
		t.Fatal("byte limit", err)
	}
	for _, run := range []string{"../run", "/etc/passwd", "", strings.Repeat("a", 257)} {
		if _, err := s.Append(run, "reg", provider(t, run, "1")); err == nil {
			t.Fatal("unsafe run", run)
		}
	}
}

func TestStoreCommittedAnchorSurvivesRestart(t *testing.T) {
	for _, mode := range []string{"whole-record-truncation", "deleted-log", "copied-log", "sequence-rollback", "unanchored-append"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(t)
			first, err := s.Append("run", "reg", provider(t, "run", "1"))
			if err != nil {
				t.Fatal(err)
			}
			path := s.path("run")
			prefix, _ := os.ReadFile(path)
			oldAnchor, _ := os.ReadFile(path + ".anchor.json")
			second, err := s.Append("run", "reg", provider(t, "run", "2"))
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			switch mode {
			case "whole-record-truncation":
				os.WriteFile(path, prefix, 0600)
			case "deleted-log":
				os.Remove(path)
			case "copied-log":
				data, _ := os.ReadFile(path)
				os.Rename(path, path+".old")
				os.WriteFile(path, data, 0600)
			case "sequence-rollback":
				seq := sequenceState{Next: first.Ordinal, Registry: s.registryHash}
				seq.Checksum = sequenceChecksum(seq)
				writeJSON(t, filepath.Join(s.root, "sequence"), seq)
			case "unanchored-append":
				os.WriteFile(path+".anchor.json", oldAnchor, 0600)
			}
			fresh, err := OpenStore(s.root)
			if mode == "sequence-rollback" {
				if err == nil {
					fresh.Close()
					t.Fatal("counter rollback accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			events, _, _, readErr := fresh.read("run", "reg", 0, 100)
			if mode == "unanchored-append" {
				if readErr != nil || len(events) != 2 || events[1].Ordinal != second.Ordinal {
					t.Fatal("fully durable append not recovered", readErr)
				}
				replay, err := fresh.Append("run", "reg", provider(t, "run", "2"))
				if err != nil || replay.Ordinal != second.Ordinal {
					t.Fatal("recovered duplicate", err)
				}
			} else if readErr == nil {
				t.Fatal("lost acknowledged activity was accepted")
			}
		})
	}
}

func TestUnsafeTemporaryHardlinkNeverTruncatesTarget(t *testing.T) {
	s := newStore(t)
	target := filepath.Join(t.TempDir(), "target")
	os.WriteFile(target, []byte("preserve this"), 0600)
	if err := os.Link(target, filepath.Join(s.root, "sequence.next")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append("run", "reg", provider(t, "run", "1")); err == nil {
		t.Fatal("hardlink accepted")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "preserve this" {
		t.Fatal("validation damaged hardlinked file")
	}
}

func TestStorePairedDeletionSurvivesRestartAndCacheEviction(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprint("restart=", restart), func(t *testing.T) {
			s := newStore(t)
			if _, err := s.Append("lost", "reg", provider(t, "lost", "1")); err != nil {
				t.Fatal(err)
			}
			path := s.path("lost")
			// Exercise real bounded cache eviction before removing both witnesses.
			for i := 0; s.cache["lost"] != nil && i < 100; i++ {
				run := fmt.Sprint("other-", i)
				if _, err := s.Append(run, "reg", provider(t, run, "1")); err != nil {
					t.Fatal(err)
				}
			}
			if s.cache["lost"] != nil {
				t.Fatal("fixture failed to evict lost run")
			}
			for _, file := range []string{path, path + ".anchor.json"} {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
			if restart {
				s.Close()
				var err error
				s, err = OpenStore(s.root)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
			}
			if _, _, _, err := s.read("lost", "reg", 0, 100); !errors.Is(err, ErrIntegrity) {
				t.Fatal("paired deletion silently initialized history", err)
			}
			if _, err := s.Append("lost", "reg", provider(t, "lost", "1")); !errors.Is(err, ErrIntegrity) {
				t.Fatal("replay erased lost history", err)
			}
			if _, err := s.Append("never-initialized", "reg", provider(t, "never-initialized", "1")); err != nil {
				t.Fatal("new run could not initialize", err)
			}
		})
	}
}

func TestStoreRegistryIntegrityAndNamespace(t *testing.T) {
	for _, mode := range []string{"deleted", "truncated", "rollback", "oversized", "symlink", "permissions", "namespace", "generation"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(t)
			path := filepath.Join(s.root, "initializations.json")
			empty, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Append("run", "reg", provider(t, "run", "1")); err != nil {
				t.Fatal(err)
			}
			s.Close()
			switch mode {
			case "deleted":
				os.Remove(path)
			case "truncated":
				os.WriteFile(path, []byte("{"), 0600)
			case "rollback":
				os.WriteFile(path, empty, 0600)
			case "oversized":
				os.Truncate(path, maxRegistryBytes+1)
			case "symlink":
				os.Rename(path, path+".old")
				os.Symlink(path+".old", path)
			case "permissions":
				os.Chmod(path, 0644)
			case "namespace", "generation":
				// Recommit a checksummed but semantically conflicting registry:
				// the namespace and per-run anchor must still be cross-checked.
				if mode == "namespace" {
					s.registry.Namespace = strings.Repeat("f", 64)
				} else {
					r := s.registry.Runs["run"]
					r.Generation = strings.Repeat("f", 64)
					s.registry.Runs["run"] = r
				}
				writeJSON(t, path, s.registry)
				data, _ := os.ReadFile(path)
				seq := sequenceState{Next: s.next, Registry: digest(data)}
				seq.Checksum = sequenceChecksum(seq)
				writeJSON(t, filepath.Join(s.root, "sequence"), seq)
			}
			fresh, err := OpenStore(s.root)
			if err == nil {
				defer fresh.Close()
				if _, _, _, err = fresh.read("run", "reg", 0, 100); err == nil {
					t.Fatal("registry corruption accepted")
				}
			}
		})
	}
}

func TestStoreRegistryPrecedesHistoryAndFailsClosedOnLiveDeletion(t *testing.T) {
	s := newStore(t)
	// Model death after registry publication, before log/anchor creation.
	s.registry.Runs["interrupted"] = initializedRun{Generation: strings.Repeat("a", 64), Registration: digest([]byte("reg"))}
	if err := s.saveRegistry(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	fresh, err := OpenStore(s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, _, _, err = fresh.read("interrupted", "reg", 0, 100); !errors.Is(err, ErrIntegrity) {
		t.Fatal("interrupted initialization reused a generation", err)
	}
	if _, err = fresh.Append("new", "reg", provider(t, "new", "1")); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(fresh.root, "initializations.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = fresh.read("new", "reg", 0, 100); !errors.Is(err, ErrIntegrity) {
		t.Fatal("cached history ignored registry loss", err)
	}
}

func TestStoreRegistryRunCeiling(t *testing.T) {
	s := newStore(t)
	for i := 0; i < runtimecatalog.MaxRuns; i++ {
		s.registry.Runs[fmt.Sprint("run-", i)] = initializedRun{Generation: strings.Repeat("a", 64), Registration: digest([]byte("reg"))}
	}
	if err := s.saveRegistry(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append("one-too-many", "reg", provider(t, "one-too-many", "1")); !errors.Is(err, ErrExhausted) {
		t.Fatal("registry exceeded run ceiling", err)
	}
	if _, err := os.Stat(s.path("one-too-many")); !os.IsNotExist(err) {
		t.Fatal("history created before registry admission", err)
	}
}

func TestStoreDoesNotAdoptLegacyNamespaceWithoutInitializationWitness(t *testing.T) {
	s := newStore(t)
	s.Close()
	if err := os.Remove(filepath.Join(s.root, "initializations.json")); err != nil {
		t.Fatal(err)
	}
	// An old ordinal allocator cannot prove that a missing log/anchor pair
	// belongs to a never-initialized run, even when no other histories remain.
	writeJSON(t, filepath.Join(s.root, "sequence"), struct {
		Next     uint64 `json:"next"`
		Checksum string `json:"checksum"`
	}{Next: 1, Checksum: jsonDigest(uint64(1))})
	fresh, err := OpenStore(s.root)
	if fresh != nil {
		fresh.Close()
	}
	if !errors.Is(err, ErrIntegrity) {
		t.Fatal("legacy namespace silently adopted", err)
	}
}

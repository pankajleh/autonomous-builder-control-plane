//go:build linux

package activity

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
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
				writeJSON(t, filepath.Join(s.root, "sequence"), sequenceState{Next: first.Ordinal, Checksum: jsonDigest(first.Ordinal)})
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

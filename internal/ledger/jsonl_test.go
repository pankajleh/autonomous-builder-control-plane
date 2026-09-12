package ledger

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestEventValidationRejectsMissingIdentity(t *testing.T) {
	e := Event{SchemaVersion: 1}
	if err := e.Validate(); err == nil {
		t.Fatal("expected missing required identity fields to fail validation")
	}
}

func TestCorrectionM07DescriptorRelativeDurability(t *testing.T) {
	newEvent := func(t *testing.T, id string) Event {
		t.Helper()
		event, err := NewEvent("m07-run", "MATERIAL", "controller", "m07")
		if err != nil {
			t.Fatal(err)
		}
		event.EventID = id
		return event
	}

	t.Run("byte-identical append-or-verify", func(t *testing.T) {
		value, err := NewJSONLLedger(filepath.Join(t.TempDir(), "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		event := newEvent(t, "m07-idempotent")
		if err := value.AppendOrVerify(event); err != nil {
			t.Fatal(err)
		}
		if err := value.AppendOrVerify(event); err != nil {
			t.Fatal(err)
		}
		data, identity, err := value.Snapshot()
		if err != nil || len(data) == 0 || identity == "" || identity == value.Path() {
			t.Fatalf("stable physical snapshot = %q, %q, %v", data, identity, err)
		}
		if got := len(bytesSplitLines(data)); got != 1 {
			t.Fatalf("idempotent append produced %d records", got)
		}
	})

	t.Run("ledger inode replacement", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "events.jsonl")
		value, err := NewJSONLLedger(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := value.Append(newEvent(t, "m07-original")); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path, path+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := value.Snapshot(); err == nil {
			t.Fatal("same-name ledger substitution was accepted")
		}
		if err := value.Append(newEvent(t, "m07-after-replacement")); err == nil {
			t.Fatal("replaced ledger received an append")
		}
	})

	t.Run("parent replacement", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "ledger")
		path := filepath.Join(parent, "events.jsonl")
		value, err := NewJSONLLedger(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(parent, parent+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := value.Snapshot(); err == nil {
			t.Fatal("ledger parent replacement was accepted")
		}
	})

	t.Run("parent symlink traversal", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "linked-parent")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := NewJSONLLedger(filepath.Join(link, "events.jsonl")); err == nil {
			t.Fatal("symbolic-link parent traversal was accepted")
		}
	})

	for _, test := range []struct {
		name string
		make func(string) error
	}{
		{"symlink", func(path string) error {
			target := path + ".target"
			if err := os.WriteFile(target, nil, 0o600); err != nil {
				return err
			}
			return os.Symlink(target, path)
		}},
		{"hard link", func(path string) error {
			target := path + ".target"
			if err := os.WriteFile(target, nil, 0o600); err != nil {
				return err
			}
			return os.Link(target, path)
		}},
		{"directory", func(path string) error { return os.Mkdir(path, 0o700) }},
		{"fifo", func(path string) error { return syscall.Mkfifo(path, 0o600) }},
	} {
		t.Run(test.name+" rejected", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			if err := test.make(path); err != nil {
				t.Fatal(err)
			}
			if _, err := NewJSONLLedger(path); err == nil {
				t.Fatalf("%s ledger was accepted", test.name)
			}
		})
	}
	t.Run("socket rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "events.jsonl")
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		if _, err := NewJSONLLedger(path); err == nil {
			t.Fatal("socket ledger was accepted")
		}
	})
	t.Run("device rejected", func(t *testing.T) {
		if _, err := os.Stat(os.DevNull); err != nil {
			t.Skip("platform has no null device")
		}
		if _, err := NewJSONLLedger(os.DevNull); err == nil {
			t.Fatal("device ledger was accepted")
		}
	})

	t.Run("barrier destination conflict", func(t *testing.T) {
		value, err := NewJSONLLedger(filepath.Join(t.TempDir(), "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		barrier, err := NewTransitionBarrier("m07-run", "m07-attempt", stringsRepeat("a", 64), time.Unix(1700000000, 0))
		if err != nil {
			t.Fatal(err)
		}
		if err := value.InstallTransitionBarrier(barrier); err != nil {
			t.Fatal(err)
		}
		other, err := NewTransitionBarrier("m07-run", "other-attempt", stringsRepeat("b", 64), time.Unix(1700000001, 0))
		if err != nil {
			t.Fatal(err)
		}
		if err := value.InstallTransitionBarrier(other); err == nil {
			t.Fatal("barrier destination race replaced immutable state")
		}
		active, found, err := value.ActiveTransitionBarrier("m07-run")
		if err != nil || !found || active.SHA256 != barrier.SHA256 {
			t.Fatalf("barrier changed after conflict: %+v, %v", active, err)
		}
	})

	t.Run("concurrent barrier destination race", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "events.jsonl")
		first, err := NewJSONLLedger(path)
		if err != nil {
			t.Fatal(err)
		}
		second, err := NewJSONLLedger(path)
		if err != nil {
			t.Fatal(err)
		}
		barriers := make([]TransitionBarrier, 2)
		barriers[0], err = NewTransitionBarrier("m07-race", "m07-first", stringsRepeat("c", 64), time.Unix(1700000002, 0))
		if err != nil {
			t.Fatal(err)
		}
		barriers[1], err = NewTransitionBarrier("m07-race", "m07-second", stringsRepeat("d", 64), time.Unix(1700000003, 0))
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		for index, value := range []*JSONLLedger{first, second} {
			go func(candidate *JSONLLedger, barrier TransitionBarrier) {
				<-start
				results <- candidate.InstallTransitionBarrier(barrier)
			}(value, barriers[index])
		}
		close(start)
		successes := 0
		for index := 0; index < 2; index++ {
			if <-results == nil {
				successes++
			}
		}
		if successes != 1 {
			t.Fatalf("concurrent no-replace barrier winners = %d, want 1", successes)
		}
	})
}

func bytesSplitLines(data []byte) [][]byte {
	var result [][]byte
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) > 0 {
			result = append(result, line)
		}
	}
	return result
}

func stringsRepeat(value string, count int) string {
	var result string
	for index := 0; index < count; index++ {
		result += value
	}
	return result
}

func TestJSONLLedgerAppendsAndPreservesPriorLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events", "run.jsonl")
	l, err := NewJSONLLedger(path)
	if err != nil {
		t.Fatal(err)
	}

	first, err := NewEvent("run-1", "RUN_CREATED", "test", "unit")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewEvent("run-1", "AUTHORITY_VALIDATED", "test", "unit")
	if err != nil {
		t.Fatal(err)
	}

	if err := l.Append(first); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(second); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	var got []Event
	for s.Scan() {
		var e Event
		if err := json.Unmarshal(s.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		got = append(got, e)
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 ledger lines, got %d", len(got))
	}
	if got[0].EventID != first.EventID || got[1].EventID != second.EventID {
		t.Fatalf("append order/history changed: %#v", got)
	}
}

package ledger

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
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

func TestRejectedRegisteredLedgerOpensReleasePersistentDescriptors(t *testing.T) {
	if attack := os.Getenv("ABCP_REJECTED_LEDGER_OPEN_HELPER"); attack != "" {
		runRejectedRegisteredLedgerOpenDescriptorHelper(t, attack)
		return
	}
	if runtime.GOOS != "linux" {
		t.Skip("process descriptor accounting requires Linux procfs")
	}
	for _, attack := range []string{"file", "parent"} {
		t.Run(attack, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestRejectedRegisteredLedgerOpensReleasePersistentDescriptors$")
			command.Env = append(os.Environ(), "ABCP_REJECTED_LEDGER_OPEN_HELPER="+attack)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("descriptor helper failed: %v\n%s", err, output)
			}
		})
	}
}

func runRejectedRegisteredLedgerOpenDescriptorHelper(t *testing.T, attack string) {
	t.Helper()
	const (
		serviceLedgerObjectLimit = 8
		rejectionWorkers         = 8
		rejectionsPerWorker      = 16
	)
	if attack != "file" && attack != "parent" {
		t.Fatalf("unknown replacement attack %q", attack)
	}
	parent := filepath.Join(t.TempDir(), "ledger")
	path := filepath.Join(parent, "events.jsonl")
	creator, err := NewJSONLLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	event, _ := NewEvent("registered-descriptor-limit", "RUN_CREATED", "controller", "test")
	event.Payload = map[string]any{"state": "RUN_CREATED"}
	if err := creator.Append(event); err != nil {
		_ = creator.Close()
		t.Fatal(err)
	}
	generation, err := creator.PhysicalGeneration()
	if err != nil {
		_ = creator.Close()
		t.Fatal(err)
	}
	if err := creator.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if attack == "file" {
		if err := os.Rename(path, path+".original"); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.Rename(parent, parent+".original"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	baseline := countProcessFileDescriptors(t)
	held := make([]*ReadOnlyJSONLLedger, 0, serviceLedgerObjectLimit)
	defer func() {
		for _, value := range held {
			_ = value.Close()
		}
	}()
	for range serviceLedgerObjectLimit {
		value, err := OpenExistingReadOnlyJSONLLedger(path)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, value)
	}
	if got, want := countProcessFileDescriptors(t), baseline+2*serviceLedgerObjectLimit; got != want {
		t.Fatalf("eight ledger objects hold %d descriptors above baseline, want %d", got-baseline, want-baseline)
	}

	openers := []struct {
		name string
		open func() error
	}{
		{name: "read-only", open: func() error {
			value, err := OpenRegisteredReadOnlyJSONLLedger(path, generation)
			if value != nil {
				_ = value.Close()
			}
			return err
		}},
		{name: "writable", open: func() error {
			value, err := OpenRegisteredJSONLLedger(path, generation)
			if value != nil {
				_ = value.Close()
			}
			return err
		}},
		{name: "coordinated-writable", open: func() error {
			value, err := OpenRegisteredJSONLLedgerWithSnapshotCoordinator(path, generation, func(operation func() error) error {
				return operation()
			})
			if value != nil {
				_ = value.Close()
			}
			return err
		}},
	}
	start := make(chan struct{})
	results := make(chan error, rejectionWorkers)
	for range rejectionWorkers {
		go func() {
			<-start
			for attempt := 0; attempt < rejectionsPerWorker; attempt++ {
				for _, opener := range openers {
					if err := opener.open(); !errors.Is(err, ErrRegisteredGenerationChanged) {
						results <- fmt.Errorf("%s rejection %d: %w", opener.name, attempt, err)
						return
					}
				}
			}
			results <- nil
		}()
	}
	close(start)
	for range rejectionWorkers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got, want := countProcessFileDescriptors(t), baseline+2*serviceLedgerObjectLimit; got != want {
		t.Fatalf("rejected opens bypassed the eight-ledger descriptor ceiling: got %d descriptors above baseline, want %d", got-baseline, want-baseline)
	}

	for _, value := range held {
		if err := value.Close(); err != nil {
			t.Fatal(err)
		}
	}
	held = nil
	if got := countProcessFileDescriptors(t); got != baseline {
		t.Fatalf("rejected registered opens leaked descriptors: before=%d after=%d", baseline, got)
	}
}

func countProcessFileDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
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

func TestJSONLLedgerConcurrentCloseSnapshotAndAppendFailClosed(t *testing.T) {
	for iteration := 0; iteration < 32; iteration++ {
		value, err := NewJSONLLedger(filepath.Join(t.TempDir(), "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		initial, err := NewEvent("close-race", "RUN_CREATED", "controller", "test")
		if err != nil {
			t.Fatal(err)
		}
		initial.Payload = map[string]any{"state": "RUN_CREATED"}
		if err := value.Append(initial); err != nil {
			t.Fatal(err)
		}
		observed, err := NewEvent("close-race", "OBSERVATION", "controller", "test")
		if err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		results := make(chan error, 2)
		closeResult := make(chan error, 1)
		var group sync.WaitGroup
		group.Add(3)
		go func() {
			defer group.Done()
			<-start
			_, _, snapshotErr := value.Snapshot()
			results <- snapshotErr
		}()
		go func() {
			defer group.Done()
			<-start
			results <- value.Append(observed)
		}()
		go func() {
			defer group.Done()
			<-start
			closeResult <- value.Close()
		}()
		close(start)
		group.Wait()
		close(results)
		for range results {
		}
		if err := <-closeResult; err != nil {
			t.Fatalf("concurrent close failed: %v", err)
		}
		if _, _, err := value.Snapshot(); err == nil {
			t.Fatal("snapshot succeeded after close")
		}
		if err := value.Append(observed); err == nil {
			t.Fatal("append succeeded after close")
		}
		if _, err := value.PhysicalIdentity(); err == nil {
			t.Fatal("physical identity succeeded after close")
		}
	}
}

func TestJSONLLedgerReplacementAfterPrecheckFailsSnapshotAndAppend(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux advisory file locking")
	}
	newFixture := func(t *testing.T) (*JSONLLedger, string) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "events.jsonl")
		value, err := NewJSONLLedger(path)
		if err != nil {
			t.Fatal(err)
		}
		initial, err := NewEvent("replacement-race", "RUN_CREATED", "controller", "test")
		if err != nil {
			t.Fatal(err)
		}
		initial.Payload = map[string]any{"state": "RUN_CREATED"}
		if err := value.Append(initial); err != nil {
			t.Fatal(err)
		}
		return value, path
	}
	blockOperation := func(t *testing.T, value *JSONLLedger, path string) *os.File {
		t.Helper()
		blocker, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := lockLedgerFile(blocker, ledgerFileLockWait); err != nil {
			_ = blocker.Close()
			t.Fatal(err)
		}
		return blocker
	}
	waitForPrecheck := func(t *testing.T, value *JSONLLedger) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if !value.mu.TryLock() {
				return
			}
			value.mu.Unlock()
			runtime.Gosched()
		}
		t.Fatal("ledger operation did not reach the locked precheck")
	}
	replacePath := func(t *testing.T, path string) {
		t.Helper()
		if err := os.Rename(path, path+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	releaseBlocker := func(t *testing.T, blocker *os.File) {
		t.Helper()
		if err := unlockLedgerFile(blocker); err != nil {
			t.Fatal(err)
		}
		if err := blocker.Close(); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("snapshot", func(t *testing.T) {
		value, path := newFixture(t)
		defer value.Close()
		blocker := blockOperation(t, value, path)
		result := make(chan error, 1)
		go func() {
			_, _, err := value.Snapshot()
			result <- err
		}()
		waitForPrecheck(t, value)
		replacePath(t, path)
		releaseBlocker(t, blocker)
		if err := <-result; err == nil {
			t.Fatal("snapshot of replaced pathname returned success")
		}
	})

	t.Run("append", func(t *testing.T) {
		value, path := newFixture(t)
		defer value.Close()
		observed, err := NewEvent("replacement-race", "OBSERVATION", "controller", "test")
		if err != nil {
			t.Fatal(err)
		}
		blocker := blockOperation(t, value, path)
		result := make(chan error, 1)
		go func() { result <- value.Append(observed) }()
		waitForPrecheck(t, value)
		replacePath(t, path)
		releaseBlocker(t, blocker)
		if err := <-result; err == nil {
			t.Fatal("append to replaced pathname returned success")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) != 0 {
			t.Fatal("replacement pathname received bytes intended for the old ledger")
		}
	})
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

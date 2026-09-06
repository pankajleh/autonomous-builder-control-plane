package ledger

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEventValidationRejectsMissingIdentity(t *testing.T) {
	e := Event{SchemaVersion: 1}
	if err := e.Validate(); err == nil {
		t.Fatal("expected missing required identity fields to fail validation")
	}
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

//go:build linux

package prlifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

func testEvent(t *testing.T, id string) (ledger.Event, []byte) {
	t.Helper()
	event := ledger.Event{SchemaVersion: 1, EventID: id, Timestamp: time.Unix(1, 0).UTC(), RunID: "run", EventType: "pr_lifecycle_terminal", Actor: "controller", Source: "prlifecycle"}
	b, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return event, b
}

func TestMaterialLedgerIdempotenceAndConflict(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "material.jsonl")
	recorder, err := NewMaterialLedgerRecorder(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	event, b := testEvent(t, strings.Repeat("a", 64))
	if err := recorder.Record(event, b); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(event, b); err != nil {
		t.Fatalf("identical replay failed: %v", err)
	}
	content, _ := os.ReadFile(path)
	if strings.Count(string(content), "\n") != 1 {
		t.Fatalf("duplicate deterministic event appended: %q", content)
	}
	conflict := event
	conflict.AttemptID = "different"
	conflictBytes, _ := json.Marshal(conflict)
	if err := recorder.Record(conflict, conflictBytes); err == nil || !strings.Contains(err.Error(), CodeLedgerIntegrity) {
		t.Fatalf("same ID/different bytes accepted: %v", err)
	}
}

func TestMaterialLedgerRejectsUnsafeParentAndMalformedSnapshot(t *testing.T) {
	unsafe := filepath.Join(t.TempDir(), "unsafe")
	if err := os.Mkdir(unsafe, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMaterialLedgerRecorder(filepath.Join(unsafe, "ledger"), nil); err == nil {
		t.Fatal("group/world-writable ledger parent accepted")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "ledger")
	if err := os.WriteFile(path, []byte(`{"broken":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder, err := NewMaterialLedgerRecorder(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	event, b := testEvent(t, strings.Repeat("b", 64))
	if err := recorder.Record(event, b); err == nil || !strings.Contains(err.Error(), CodeLedgerIntegrity) {
		t.Fatalf("non-newline snapshot accepted: %v", err)
	}
}

func TestMaterialLedgerParentReplacementCannotRedirectAppend(t *testing.T) {
	root := t.TempDir()
	_ = os.Chmod(root, 0o700)
	parent := filepath.Join(root, "ledger-parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	recorder, err := NewMaterialLedgerRecorder(filepath.Join(parent, "ledger"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parent, parent+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	event, b := testEvent(t, strings.Repeat("c", 64))
	if err := recorder.Record(event, b); err == nil || !strings.Contains(err.Error(), CodeLedgerUnavailable) {
		t.Fatalf("replaced parent redirected material append: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, "ledger")); !os.IsNotExist(err) {
		t.Fatalf("replacement directory received ledger write: %v", err)
	}
}

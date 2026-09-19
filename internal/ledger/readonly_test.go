package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExistingLedgerOpenersNeverMaterializeAndCloseFailClosed(t *testing.T) {
	root := t.TempDir()
	missingParentPath := filepath.Join(root, "missing", "events.jsonl")
	if _, err := OpenExistingReadOnlyJSONLLedger(missingParentPath); err == nil {
		t.Fatal("read-only opener created a missing parent or ledger")
	}
	if _, err := OpenExistingJSONLLedger(missingParentPath); err == nil {
		t.Fatal("writable opener created a missing parent or ledger")
	}
	if _, err := os.Stat(filepath.Dir(missingParentPath)); !os.IsNotExist(err) {
		t.Fatalf("missing parent was materialized: %v", err)
	}

	existingParent := filepath.Join(root, "existing")
	if err := os.Mkdir(existingParent, 0o700); err != nil {
		t.Fatal(err)
	}
	missingLedgerPath := filepath.Join(existingParent, "events.jsonl")
	if _, err := OpenExistingReadOnlyJSONLLedger(missingLedgerPath); err == nil {
		t.Fatal("read-only opener accepted a missing ledger")
	}
	if _, err := OpenExistingJSONLLedger(missingLedgerPath); err == nil {
		t.Fatal("writable opener accepted a missing ledger")
	}
	if _, err := os.Stat(missingLedgerPath); !os.IsNotExist(err) {
		t.Fatalf("missing ledger was materialized: %v", err)
	}

	creator, err := NewJSONLLedger(missingLedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	event, err := NewEvent("existing-run", "RUN_CREATED", "controller", "test")
	if err != nil {
		t.Fatal(err)
	}
	event.Payload = map[string]any{"state": "RUN_CREATED"}
	if err := creator.Append(event); err != nil {
		t.Fatal(err)
	}
	if err := creator.Close(); err != nil || creator.Close() != nil {
		t.Fatalf("creator close was not idempotent: %v", err)
	}
	if _, _, err := creator.Snapshot(); err == nil {
		t.Fatal("closed creator snapshot succeeded")
	}
	if err := creator.Append(event); err == nil {
		t.Fatal("closed creator append succeeded")
	}

	reader, err := OpenExistingReadOnlyJSONLLedger(missingLedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	data, identity, err := reader.Snapshot()
	if err != nil || len(data) == 0 || identity == "" {
		t.Fatalf("existing read-only snapshot = %d bytes, %q, %v", len(data), identity, err)
	}
	if verified, err := reader.PhysicalIdentity(); err != nil || verified != identity {
		t.Fatalf("physical identity = %q, %v, want %q", verified, err, identity)
	}
	if err := reader.Close(); err != nil || reader.Close() != nil {
		t.Fatalf("reader close was not idempotent: %v", err)
	}
	if _, _, err := reader.Snapshot(); err == nil {
		t.Fatal("closed reader snapshot succeeded")
	}

	writer, err := OpenExistingJSONLLedger(missingLedgerPath)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := NewEvent("existing-run", "OBSERVATION", "controller", "test")
	if err := writer.Append(second); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil || writer.Close() != nil {
		t.Fatalf("writer close was not idempotent: %v", err)
	}
}

func TestRegisteredLedgerOpenersRejectDifferentPhysicalGeneration(t *testing.T) {
	for _, attack := range []string{"file", "parent"} {
		t.Run(attack, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "ledger")
			path := filepath.Join(parent, "events.jsonl")
			creator, err := NewJSONLLedger(path)
			if err != nil {
				t.Fatal(err)
			}
			event, _ := NewEvent("registered-generation", "RUN_CREATED", "controller", "test")
			event.Payload = map[string]any{"state": "RUN_CREATED"}
			if err := creator.Append(event); err != nil {
				t.Fatal(err)
			}
			generation, err := creator.PhysicalGeneration()
			if err != nil {
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

			if reader, err := OpenRegisteredReadOnlyJSONLLedger(path, generation); !errors.Is(err, ErrRegisteredGenerationChanged) {
				if reader != nil {
					_ = reader.Close()
				}
				t.Fatalf("registered reader adopted replacement: %v", err)
			}
			if writer, err := OpenRegisteredJSONLLedger(path, generation); !errors.Is(err, ErrRegisteredGenerationChanged) {
				if writer != nil {
					_ = writer.Close()
				}
				t.Fatalf("registered writer adopted replacement: %v", err)
			}
			if _, err := os.Lstat(path + ".run-locks"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("replacement initialized a run-lock generation: %v", err)
			}
		})
	}
}

func TestExistingLedgerOpenersRejectReplacement(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "ledger")
	path := filepath.Join(parent, "events.jsonl")
	creator, err := NewJSONLLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	event, _ := NewEvent("replacement-run", "RUN_CREATED", "controller", "test")
	event.Payload = map[string]any{"state": "RUN_CREATED"}
	if err := creator.Append(event); err != nil {
		t.Fatal(err)
	}
	if err := creator.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenExistingReadOnlyJSONLLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.Snapshot(); err == nil {
		t.Fatal("reader followed a replacement ledger")
	}
	_ = reader.Close()

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".old", path); err != nil {
		t.Fatal(err)
	}
	reader, err = OpenExistingReadOnlyJSONLLedger(path)
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
	if _, _, err := reader.Snapshot(); err == nil {
		t.Fatal("reader followed a replacement parent")
	}
	_ = reader.Close()
}

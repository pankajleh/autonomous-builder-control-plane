package workflowauthoritypg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	governancev3 "github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

func TestPostgresWorkflowAuthorityRoundTrip(t *testing.T) {
	dsn := os.Getenv("ABCP_WORKFLOW_AUTHORITY_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("ABCP_WORKFLOW_AUTHORITY_PG_TEST_DSN is not set")
	}

	domain := strings.Repeat("2", 64)
	controller := strings.Repeat("1", 64)
	repository := "example/product"
	configPath := filepath.Join(t.TempDir(), "workflow-authority.json")
	configData, err := json.Marshal(configurationV1{
		SchemaVersion:         1,
		ConnectionString:      dsn,
		AuthorityDomainSHA256: domain,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	backend, err := Open(ctx, configPath)
	if err != nil {
		t.Fatalf("open backend: %v", err)
	}
	if err := backend.EnsureInitialized(ctx, controller, repository); err != nil {
		backend.Close()
		t.Fatalf("initialize backend: %v", err)
	}
	initialBytes, revision, err := backend.LoadWorkflowStateV1(controller)
	if err != nil || revision != 1 {
		backend.Close()
		t.Fatalf("initial load: revision=%d err=%v", revision, err)
	}
	var state governancev3.ControllerStateV1
	if err := governancev3.ParseCanonical(initialBytes, &state); err != nil {
		backend.Close()
		t.Fatalf("parse initial state: %v", err)
	}
	if state.ControllerIdentity != controller || state.RepositoryIdentity != repository ||
		state.ExecutionState.AggregateElapsed != "0s" {
		backend.Close()
		t.Fatalf("unexpected initial state: %+v", state)
	}

	state.Revision = 2
	state.ExecutionState = ralphex.ExecutionStateV1{AggregateElapsed: "1s"}
	nextBytes, err := json.Marshal(state)
	if err != nil {
		backend.Close()
		t.Fatal(err)
	}
	swapped, err := backend.CompareAndSwapWorkflowStateV1(controller, 1, nextBytes)
	if err != nil || !swapped {
		backend.Close()
		t.Fatalf("advance CAS: swapped=%t err=%v", swapped, err)
	}

	swapped, err = backend.CompareAndSwapWorkflowStateV1(controller, 1, nextBytes)
	if err != nil || !swapped {
		backend.Close()
		t.Fatalf("exact CAS replay: swapped=%t err=%v", swapped, err)
	}

	conflict := state
	conflict.ExecutionState = ralphex.ExecutionStateV1{AggregateElapsed: "2s"}
	conflictBytes, err := json.Marshal(conflict)
	if err != nil {
		backend.Close()
		t.Fatal(err)
	}
	swapped, err = backend.CompareAndSwapWorkflowStateV1(controller, 1, conflictBytes)
	if err != nil || swapped {
		backend.Close()
		t.Fatalf("stale divergent CAS: swapped=%t err=%v", swapped, err)
	}
	backend.Close()

	reopened, err := Open(ctx, configPath)
	if err != nil {
		t.Fatalf("reopen backend: %v", err)
	}
	defer reopened.Close()
	reopenedBytes, reopenedRevision, err := reopened.LoadWorkflowStateV1(controller)
	if err != nil || reopenedRevision != 2 || string(reopenedBytes) != string(nextBytes) {
		t.Fatalf("reopened state: revision=%d equal=%t err=%v", reopenedRevision, string(reopenedBytes) == string(nextBytes), err)
	}
	if err := reopened.EnsureInitialized(ctx, controller, repository); err != nil {
		t.Fatalf("idempotent initialize: %v", err)
	}
	if err := reopened.EnsureInitialized(ctx, controller, "different/repository"); err == nil {
		t.Fatal("divergent repository initialization was accepted")
	}

	otherConfig := filepath.Join(t.TempDir(), "workflow-authority.json")
	otherData, err := json.Marshal(configurationV1{
		SchemaVersion:         1,
		ConnectionString:      dsn,
		AuthorityDomainSHA256: strings.Repeat("3", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherConfig, otherData, 0o600); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, otherConfig)
	if err != nil {
		t.Fatalf("open other-domain backend: %v", err)
	}
	defer other.Close()
	if err := other.EnsureInitialized(ctx, controller, repository); err == nil {
		t.Fatal("divergent authority domain initialization was accepted")
	}
}

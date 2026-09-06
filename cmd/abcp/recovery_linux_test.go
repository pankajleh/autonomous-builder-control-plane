//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/blocker"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/recovery"
)

func TestRecoveryCLIInspectsAndExecutesExplicitAuthorizedRecovery(t *testing.T) {
	worktreeRoot := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	worktree := filepath.Join(worktreeRoot, "attempt-worktree")
	gitCommand(t, "", "init", "-b", "main", repository)
	gitCommand(t, repository, "config", "user.email", "recovery-cli@example.test")
	gitCommand(t, repository, "config", "user.name", "Recovery CLI Test")
	writeCLIFile(t, filepath.Join(repository, "README.md"), []byte("fixture\n"), 0o600)
	gitCommand(t, repository, "add", "README.md")
	gitCommand(t, repository, "commit", "-m", "fixture")
	branch := "attempt/cli-run"
	gitCommand(t, repository, "worktree", "add", "-b", branch, worktree)

	prior := recovery.AttemptIdentity{
		ProjectID: "project-1", PlanID: "plan-1", RunID: "cli-recovery-run",
		AttemptID: "attempt-1", TaskID: "task-1", AgentSessionID: "session-1",
	}
	ownership, err := recovery.CaptureOwnership(prior, recovery.WorktreeIdentity{
		RepositoryPath: repository, RootPath: worktreeRoot, Path: worktree, Branch: branch,
	}, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	ownershipPath := filepath.Join(t.TempDir(), "ownership.json")
	ownershipBytes, err := json.Marshal(ownership)
	if err != nil {
		t.Fatal(err)
	}
	writeCLIFile(t, ownershipPath, ownershipBytes, 0o600)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runCLI([]string{"recovery-inspect", "--ownership", ownershipPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("recovery-inspect exited %d: %s", code, stderr.String())
	}
	var inspection recovery.Inspection
	if err := json.Unmarshal(stdout.Bytes(), &inspection); err != nil {
		t.Fatal(err)
	}
	if inspection.Classification != recovery.ClassificationActive {
		t.Fatalf("CLI inspection = %#v", inspection)
	}

	writeCLIFile(t, filepath.Join(worktree, "README.md"), []byte("fixture\ndirty CLI state\n"), 0o600)
	ownership.Process.LinuxStartTicks++
	authorized := prior
	authorized.AttemptID = "attempt-2"
	authorized.AgentSessionID = "session-2"
	request := recovery.WorkflowRequest{
		SnapshotID: "cli-recovery", Ownership: ownership, StateFrom: domain.StateImplementing,
		Failure: blocker.FailureInput{Phase: blocker.PhaseExecution, Diagnostics: "unclassified controller failure"},
		Requirement: recovery.BlockerRequirement{Recovery: &recovery.RecoveryRequirement{
			Reason: "no deterministic classifier rule matched", RequiredAuthority: "recovery-controller",
		}},
		Authorization: &recovery.RecoveryAuthorization{
			AuthorizedAttempt: authorized, Actor: "repository-owner",
			Decision: "replace stale CLI attempt", Action: recovery.ResumeActionRestart,
			Timestamp:     time.Date(2026, 9, 6, 15, 0, 0, 0, time.UTC),
			PolicyVersion: "recovery-v1", Boundary: domain.StateExecutionStarting,
		},
	}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(t.TempDir(), "request.json")
	writeCLIFile(t, requestPath, requestBytes, 0o600)
	outputRoot := t.TempDir()
	ledgerPath := filepath.Join(outputRoot, "ledger", "events.jsonl")
	evidenceRoot := filepath.Join(outputRoot, "evidence")
	stdout.Reset()
	stderr.Reset()
	code := runCLI([]string{
		"recovery-resume", "--request", requestPath,
		"--ledger", ledgerPath, "--evidence-root", evidenceRoot,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("recovery-resume exited %d: %s", code, stderr.String())
	}
	var result recovery.WorkflowResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Boundary != domain.StateExecutionStarting || result.Authorized != authorized {
		t.Fatalf("CLI recovery result = %#v", result)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("CLI did not remove exact stale worktree: %v", err)
	}
	ledgerBytes, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ledgerBytes), recovery.EventRestartPrepared) ||
		strings.Contains(string(ledgerBytes), string(domain.StateIntegrationPending)) {
		t.Fatalf("unexpected recovery ledger: %s", ledgerBytes)
	}
}

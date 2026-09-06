package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

func TestRunCLIEndToEnd(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repository")
	gitCommand(t, "", "init", "-b", "main", repository)
	gitCommand(t, repository, "config", "user.email", "cli@example.test")
	gitCommand(t, repository, "config", "user.name", "CLI Test")
	gitCommand(t, repository, "remote", "add", "origin", "https://example.test/example/project.git")
	planPath := filepath.Join(repository, "plan.md")
	writeCLIFile(t, planPath, []byte("# CLI plan\n"), 0o600)
	gitCommand(t, repository, "add", "plan.md")
	gitCommand(t, repository, "commit", "-m", "initial plan")
	startSHA := gitCommand(t, repository, "rev-parse", "HEAD")

	binaryPath := filepath.Join(t.TempDir(), "fake-ralphex")
	writeCLIFile(t, binaryPath, []byte("#!/bin/sh\nprintf 'cli fake ralphex\\n'\n"), 0o700)
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	manifest := authority.Manifest{
		RunID: "cli-run",
		Repository: authority.RepositoryManifest{
			Path: repository, Identity: "example/project", Remotes: map[string]string{"origin": "https://example.test/example/project.git"}, DefaultBranch: "main", StartSHA: startSHA,
		},
		Plan:          authority.PlanManifest{Path: planPath, SHA256: cliFileHash(t, planPath)},
		Ralphex:       authority.RalphexManifest{BinaryPath: binaryPath, BinarySHA256: cliFileHash(t, binaryPath), Mode: ralphex.ModeFull, Timeout: "5s", WaitOnLimit: "0s"},
		Acceptance:    []authority.AcceptanceCommand{{Required: true, Timeout: "5s", Argv: []string{truePath}}},
		PolicyVersion: "cli-v1",
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	writeCLIFile(t, manifestPath, manifestBytes, 0o600)
	outputRoot := t.TempDir()
	ledgerPath := filepath.Join(outputRoot, "ledger", "events.jsonl")
	evidenceRoot := filepath.Join(outputRoot, "evidence")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI([]string{
		"run", "--manifest", manifestPath, "--ledger", ledgerPath, "--evidence-root", evidenceRoot,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run CLI exited %d: %s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "BRANCH_ACCEPTED" {
		t.Fatalf("unexpected CLI output %q", stdout.String())
	}
	if _, err := os.Stat(ledgerPath); err != nil {
		t.Fatalf("ledger was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(evidenceRoot, "cli-run", "authority.json")); err != nil {
		t.Fatalf("authority evidence was not created: %v", err)
	}
}

func TestRunCommandRequiresEveryExplicitPath(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI([]string{"run", "--manifest", "manifest.json", "--ledger", "events.jsonl"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected usage exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "--evidence-root") {
		t.Fatalf("usage does not name required evidence root: %q", stderr.String())
	}
}

func TestCanonicalLedgerDestinationRejectsEvidenceOverlap(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "evidence", "run-123")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalLedgerDestination(filepath.Join(runDir, "authority.json"), runDir); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("expected ledger/evidence overlap rejection, got %v", err)
	}

	outside := filepath.Join(t.TempDir(), "ledger", "events.jsonl")
	canonical, err := canonicalLedgerDestination(outside, runDir)
	if err != nil {
		t.Fatal(err)
	}
	if canonical != outside {
		t.Fatalf("canonical ledger path = %q, want %q", canonical, outside)
	}
}

func TestLoadManifestRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(`{"run_id":"test","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadManifest(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown-field rejection, got %v", err)
	}
}

func gitCommand(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	if directory != "" {
		command.Dir = directory
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeCLIFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func cliFileHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

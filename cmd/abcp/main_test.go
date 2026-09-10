package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	governancev3 "github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
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
	writeCLIFile(t, binaryPath, []byte("#!/bin/sh\nprintf 'cli fake ralphex\\n'\nprintf 'candidate\\n' > candidate.txt\ngit add candidate.txt || exit 20\ngit commit -qm 'candidate implementation' || exit 21\n"), 0o700)
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
	capsuleSpec := contextcapsule.Spec{
		PolicyVersion: contextcapsule.PolicyVersionV2,
		Project:       "ABCP", Plan: "CLI plan", RoadmapPhase: "test", ExecutionPack: "test",
		Task: "Task 1", Repository: "example/project", BaseSHA: startSHA,
		OperationContext: &contextcapsule.OperationContext{
			Kind: contextcapsule.OperationImplementation, OwnedScope: []string{"CLI plan"},
			BlockingCriteria: []string{"Current owned-scope Critical or Major findings."},
		},
		Invariants: []string{"Fail closed."}, NonGoals: []string{"No out-of-scope changes."},
		PredecessorOutcomes: []contextcapsule.Outcome{}, Sources: []string{"plan.md"},
	}
	_, capsuleBytes, err := contextcapsule.Build(repository, capsuleSpec)
	if err != nil {
		t.Fatal(err)
	}
	capsulePath := filepath.Join(t.TempDir(), "context-capsule.json")
	writeCLIFile(t, capsulePath, capsuleBytes, 0o600)
	manifest.ContextCapsule = &authority.ContextCapsuleManifest{Path: capsulePath, SHA256: cliFileHash(t, capsulePath)}
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

func TestContextBuildAndVerifyCLI(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repository")
	gitCommand(t, "", "init", "-b", "main", repository)
	gitCommand(t, repository, "config", "user.email", "context-cli@example.test")
	gitCommand(t, repository, "config", "user.name", "Context CLI Test")
	gitCommand(t, repository, "remote", "add", "origin", "https://example.test/example/project.git")
	sourcePath := filepath.Join(repository, "authority.md")
	writeCLIFile(t, sourcePath, []byte("durable authority"), 0o600)
	gitCommand(t, repository, "add", "authority.md")
	gitCommand(t, repository, "commit", "-m", "authority")
	head := gitCommand(t, repository, "rev-parse", "HEAD")
	spec := contextcapsule.Spec{
		PolicyVersion: contextcapsule.PolicyVersionV2,
		Project:       "ABCP", Plan: "EP-004", RoadmapPhase: "Phase 3", ExecutionPack: "EP-004",
		Task: "Task 1", Repository: "example/project", BaseSHA: head,
		OperationContext: &contextcapsule.OperationContext{
			Kind: contextcapsule.OperationImplementationReview, OwnedScope: []string{"authority.md"},
			BlockingCriteria: []string{"Current owned-scope Critical or Major findings."},
		},
		Invariants: []string{"Fail closed."}, NonGoals: []string{"No semantic retrieval."},
		PredecessorOutcomes: []contextcapsule.Outcome{}, Sources: []string{"authority.md"},
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(t.TempDir(), "spec.json")
	writeCLIFile(t, specPath, specJSON, 0o600)
	capsulePath := filepath.Join(t.TempDir(), "capsule.json")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runCLI([]string{"context-build", "--repository", repository, "--spec", specPath, "--output", capsulePath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("context-build exited %d: %s", code, stderr.String())
	}
	data, err := os.ReadFile(capsulePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contextcapsule.Parse(data); err != nil {
		t.Fatalf("context-build did not emit canonical capsule: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	code = runCLI([]string{"context-verify", "--repository", repository, "--capsule", capsulePath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("context-verify exited %d: %s", code, stderr.String())
	}
	var verified contextcapsule.Verification
	if err := json.Unmarshal(stdout.Bytes(), &verified); err != nil {
		t.Fatalf("decode verification output: %v", err)
	}
	if verified.BaseSHA != head || verified.SourcesVerified != 1 || verified.SHA256 != cliFileHash(t, capsulePath) {
		t.Fatalf("unexpected verification output: %+v", verified)
	}

	writeCLIFile(t, sourcePath, []byte("drift"), 0o600)
	stdout.Reset()
	stderr.Reset()
	if code := runCLI([]string{"context-verify", "--repository", repository, "--capsule", capsulePath}, &stdout, &stderr); code != 1 {
		t.Fatalf("context-verify accepted drifted source with exit %d", code)
	}
}

func TestContextCommandsRequireStructuredPathArguments(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runCLI([]string{"context-build", "--repository", ".", "--spec", "spec.json"}, &stdout, &stderr); code != 2 {
		t.Fatalf("context-build missing output exited %d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runCLI([]string{"context-verify", "capsule.json"}, &stdout, &stderr); code != 2 {
		t.Fatalf("context-verify accepted positional input with exit %d", code)
	}
}

func TestGovernanceActivationDiagnosticUsesProductValidator(t *testing.T) {
	activation, err := governancev3.SealGovernanceActivationV1(governancev3.GovernanceActivationV1{
		Kind: "GovernanceActivationV1", PolicyVersion: contextcapsule.PolicyVersionV3,
		PolicySHA256: strings.Repeat("a", 64), ActivationRepositoryCommit: strings.Repeat("b", 40),
		ActivationSequence: 12, ActivationTime: "2026-09-10T00:00:00Z", GrandfatheredV2Digests: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(activation)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "activation.json")
	writeCLIFile(t, path, data, 0o600)
	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"governance-activation-validate", "--input", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("activation diagnostic exited %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"valid": true`) {
		t.Fatalf("activation output = %q", stdout.String())
	}
	activation.PolicySHA256 = strings.Repeat("c", 64)
	data, err = json.Marshal(activation)
	if err != nil {
		t.Fatal(err)
	}
	writeCLIFile(t, path, data, 0o600)
	stdout.Reset()
	stderr.Reset()
	if code := runCLI([]string{"governance-activation-validate", "--input", path}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "CAPSULE_LINEAGE_INVALID") {
		t.Fatalf("tampered activation diagnostic exited %d: %s", code, stderr.String())
	}
}

func TestGovernanceDiagnosticRejectsNonCanonicalJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	writeCLIFile(t, path, []byte("{\n  \"kind\": \"DESIGN_ACCEPTED\"\n}\n"), 0o600)
	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"governance-checkpoint-validate", "--input", path}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "strict canonical") {
		t.Fatalf("noncanonical diagnostic exited %d: %s", code, stderr.String())
	}
}

func TestGovernanceCLIRejectsCallerSelectedStatePaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.json")
	writeCLIFile(t, path, []byte("{}"), 0o600)
	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"governance-review-advance", "--input", path, "--state", filepath.Join(t.TempDir(), "state.json")}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("caller-selected governance state exited %d: %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runCLI([]string{"run", "--manifest", path, "--ledger", "ledger", "--evidence-root", "evidence", "--governance-state", filepath.Join(t.TempDir(), "state.json")}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("caller-selected run state exited %d: %s", code, stderr.String())
	}
}

func TestCanonicalLedgerDestinationRejectsEvidenceOverlap(t *testing.T) {
	evidenceRoot := filepath.Join(t.TempDir(), "evidence")
	runDir := filepath.Join(evidenceRoot, "run-123")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalLedgerDestination(filepath.Join(runDir, "authority.json"), evidenceRoot); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("expected ledger/evidence overlap rejection, got %v", err)
	}
	otherRun, err := evidence.NewStore(evidenceRoot, "other-run")
	if err != nil {
		t.Fatal(err)
	}
	otherArtifact, err := otherRun.WriteBytes("authority.json", "validated-authority", []byte("immutable"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalLedgerDestination(otherArtifact.URI, evidenceRoot); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("expected cross-run ledger/evidence overlap rejection, got %v", err)
	}

	outside := filepath.Join(t.TempDir(), "ledger", "events.jsonl")
	canonical, err := canonicalLedgerDestination(outside, evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if canonical != outside {
		t.Fatalf("canonical ledger path = %q, want %q", canonical, outside)
	}
}

func TestCanonicalLedgerDestinationDoesNotCreateRejectedPath(t *testing.T) {
	root := t.TempDir()
	evidenceRoot := filepath.Join(root, "evidence")
	poisonedArtifact := filepath.Join(evidenceRoot, "run-123", "authority.json")
	ledgerPath := filepath.Join(poisonedArtifact, "events.jsonl")

	if _, err := canonicalLedgerDestination(ledgerPath, evidenceRoot); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("expected ledger/evidence overlap rejection, got %v", err)
	}
	if _, err := os.Lstat(evidenceRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected destination mutated evidence namespace: %v", err)
	}
}

func TestCanonicalLedgerDestinationRejectsDanglingSymlinkIntoEvidence(t *testing.T) {
	root := t.TempDir()
	evidenceRoot := filepath.Join(root, "evidence")
	if err := os.MkdirAll(filepath.Join(evidenceRoot, "run-123"), 0o700); err != nil {
		t.Fatal(err)
	}
	ledgerLink := filepath.Join(root, "ledger-link")
	if err := os.Symlink(filepath.Join(evidenceRoot, "run-123", "events.jsonl"), ledgerLink); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalLedgerDestination(ledgerLink, evidenceRoot); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("expected dangling ledger symlink overlap rejection, got %v", err)
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

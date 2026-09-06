package run

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

func TestRunnerSuccessReachesBranchAcceptedWithOrderedEvidence(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	result := fixture.execute(t)
	if !result.Accepted() {
		t.Fatalf("expected accepted result, got state %s, reason %q", result.State, result.FailureReason)
	}
	if result.Ralphex.Outcome != supervisor.OutcomeSucceeded {
		t.Fatalf("unexpected Ralphex outcome %s", result.Ralphex.Outcome)
	}
	wantArgv := []string{
		fixture.authority.Ralphex().BinaryPath,
		"--codex", "--task-model", "test-model:high", "--tasks-only", fixture.authority.Plan().Path,
	}
	if !reflect.DeepEqual(result.Ralphex.Argv, wantArgv) {
		t.Fatalf("Ralphex argv mismatch\nwant: %#v\n got: %#v", wantArgv, result.Ralphex.Argv)
	}

	events := readEvents(t, fixture.ledgerPath)
	wantStates := []domain.State{
		domain.StateRunCreated,
		domain.StateAuthorityValidated,
		domain.StateExecutionStarting,
		domain.StateImplementing,
		domain.StateImplementationCompleted,
		domain.StateBranchAcceptancePending,
		domain.StateBranchAccepted,
	}
	if got := eventStates(events); !reflect.DeepEqual(got, wantStates) {
		t.Fatalf("event state order mismatch\nwant: %#v\n got: %#v", wantStates, got)
	}
	assertEventEvidence(t, events)
	for _, event := range events {
		if event.StateTo == domain.StateIntegrationPending || event.StateTo == domain.StateReadyForMerge ||
			event.StateTo == domain.StateMerged || event.StateTo == domain.StateCompleted {
			t.Fatalf("EP-002 emitted prohibited state %s", event.StateTo)
		}
	}
	if len(events[len(events)-1].EvidenceRefs) < 8 {
		t.Fatalf("BRANCH_ACCEPTED lacks complete acceptance evidence: %#v", events[len(events)-1].EvidenceRefs)
	}
}

func TestRunnerRalphexFailureRecordsEvidenceWithoutImplementationCompleted(t *testing.T) {
	fixture := newRunFixture(t, 17, commandPath(t, "true"))
	result := fixture.execute(t)
	if result.State != domain.StateFailed || result.Ralphex.ExitCode != 17 {
		t.Fatalf("unexpected failure result: state=%s exit=%d", result.State, result.Ralphex.ExitCode)
	}
	if result.Accepted() {
		t.Fatal("Ralphex failure must not be accepted")
	}

	events := readEvents(t, fixture.ledgerPath)
	wantStates := []domain.State{
		domain.StateRunCreated,
		domain.StateAuthorityValidated,
		domain.StateExecutionStarting,
		domain.StateImplementing,
		domain.StateFailed,
	}
	if got := eventStates(events); !reflect.DeepEqual(got, wantStates) {
		t.Fatalf("event state order mismatch\nwant: %#v\n got: %#v", wantStates, got)
	}
	terminal := events[len(events)-1]
	if len(terminal.EvidenceRefs) != 3 {
		t.Fatalf("Ralphex failure should reference stdout, stderr, and metadata: %#v", terminal.EvidenceRefs)
	}
	assertEventEvidence(t, events)
}

func TestRunnerAcceptanceFailureStopsBeforeBranchAccepted(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "false"))
	result := fixture.execute(t)
	if result.State != domain.StateFailed {
		t.Fatalf("expected FAILED, got %s", result.State)
	}
	if result.Acceptance.Passed() || result.Accepted() {
		t.Fatal("failed controller acceptance must not yield BRANCH_ACCEPTED")
	}

	events := readEvents(t, fixture.ledgerPath)
	wantStates := []domain.State{
		domain.StateRunCreated,
		domain.StateAuthorityValidated,
		domain.StateExecutionStarting,
		domain.StateImplementing,
		domain.StateImplementationCompleted,
		domain.StateBranchAcceptancePending,
		domain.StateFailed,
	}
	if got := eventStates(events); !reflect.DeepEqual(got, wantStates) {
		t.Fatalf("event state order mismatch\nwant: %#v\n got: %#v", wantStates, got)
	}
	assertEventEvidence(t, events)
}

func TestRunnerRejectsIdentityChangedAfterValidation(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	if err := os.WriteFile(fixture.authority.Plan().Path, []byte("changed plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.runner(t).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "plan SHA256 changed") {
		t.Fatalf("expected pinned plan validation error, got %v", err)
	}
	if result.State != domain.StateFailed {
		t.Fatalf("expected FAILED, got %s", result.State)
	}
	if got := eventStates(readEvents(t, fixture.ledgerPath)); !reflect.DeepEqual(got, []domain.State{domain.StateRunCreated, domain.StateFailed}) {
		t.Fatalf("unexpected states after identity rejection: %#v", got)
	}
}

func TestEP002StateCapExcludesIntegrationAndCompletion(t *testing.T) {
	for _, state := range []domain.State{
		domain.StateIntegrationPending,
		domain.StateReadyForMerge,
		domain.StateMerged,
		domain.StateCompleted,
	} {
		if ep002State(state) {
			t.Fatalf("EP-002 runner unexpectedly permits %s", state)
		}
	}
}

type runFixture struct {
	authority  authority.Authority
	ledgerPath string
	evidence   string
}

func newRunFixture(t *testing.T, ralphexExit int, acceptanceArgv ...string) runFixture {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	runGit(t, "", "init", "-b", "main", repository)
	runGit(t, repository, "config", "user.email", "controller@example.test")
	runGit(t, repository, "config", "user.name", "Controller Test")
	runGit(t, repository, "remote", "add", "origin", "https://example.test/project.git")
	planPath := filepath.Join(repository, "plan.md")
	writeTestFile(t, planPath, []byte("# governed plan\n"), 0o600)
	runGit(t, repository, "add", "plan.md")
	runGit(t, repository, "commit", "-m", "initial plan")
	startSHA := runGit(t, repository, "rev-parse", "HEAD")

	binaryPath := filepath.Join(t.TempDir(), "fake-ralphex")
	script := fmt.Sprintf("#!/bin/sh\nprintf 'fake ralphex stdout\\n'\nprintf 'fake ralphex stderr\\n' >&2\nexit %s\n", strconv.Itoa(ralphexExit))
	writeTestFile(t, binaryPath, []byte(script), 0o700)

	manifest := authority.Manifest{
		RunID: "run-" + strconv.Itoa(ralphexExit),
		Repository: authority.RepositoryManifest{
			Path:          repository,
			Identity:      "example/project",
			Remotes:       map[string]string{"origin": "https://example.test/project.git"},
			DefaultBranch: "main",
			StartSHA:      startSHA,
		},
		Plan: authority.PlanManifest{Path: planPath, SHA256: testHash(t, planPath)},
		Ralphex: authority.RalphexManifest{
			BinaryPath: binaryPath, BinarySHA256: testHash(t, binaryPath), SourceSHA: "source-test", Mode: ralphex.ModeTasksOnly,
		},
		Executor: authority.ExecutorPolicy{Executor: "codex", TaskModel: "test-model", TaskEffort: "high"},
		Acceptance: []authority.AcceptanceCommand{{
			Name: "deterministic check", Class: "unit", Required: true, Argv: acceptanceArgv,
		}},
		PolicyVersion: "branch-test-v1",
	}
	governed, err := authority.New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	return runFixture{
		authority:  governed,
		ledgerPath: filepath.Join(root, "events", "run.jsonl"),
		evidence:   filepath.Join(root, "evidence"),
	}
}

func (f runFixture) runner(t *testing.T) *Runner {
	t.Helper()
	events, err := ledger.NewJSONLLedger(f.ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := evidence.NewStore(f.evidence, f.authority.RunID())
	if err != nil {
		t.Fatal(err)
	}
	runner, err := New(f.authority, events, artifacts, supervisor.New())
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func (f runFixture) execute(t *testing.T) Result {
	t.Helper()
	result, err := f.runner(t).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func readEvents(t *testing.T, path string) []ledger.Event {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var events []ledger.Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event ledger.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func eventStates(events []ledger.Event) []domain.State {
	states := make([]domain.State, 0, len(events))
	for _, event := range events {
		if event.EventType == string(domain.StateRunCreated) {
			states = append(states, domain.StateRunCreated)
			continue
		}
		states = append(states, event.StateTo)
	}
	return states
}

func assertEventEvidence(t *testing.T, events []ledger.Event) {
	t.Helper()
	for index, event := range events {
		if index > 0 && event.Timestamp.Before(events[index-1].Timestamp) {
			t.Fatalf("event %d timestamp precedes prior event", index)
		}
		for _, ref := range event.EvidenceRefs {
			data, err := os.ReadFile(ref.URI)
			if err != nil {
				t.Fatalf("read evidence %s: %v", ref.URI, err)
			}
			digest := sha256.Sum256(data)
			if hex.EncodeToString(digest[:]) != ref.SHA256 {
				t.Fatalf("evidence digest mismatch for %s", ref.URI)
			}
		}
	}
}

func runGit(t *testing.T, directory string, args ...string) string {
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

func commandPath(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTestFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func testHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

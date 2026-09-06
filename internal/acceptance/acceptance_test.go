package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/acceptance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

func TestExecutorPassesAllRequiredCommandsAndCapturesEvidence(t *testing.T) {
	repository := newGitRepository(t)
	dirtyPath := filepath.Join(repository, "untracked.txt")
	if err := os.WriteFile(dirtyPath, []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	commands := []authority.AcceptanceCommand{
		{
			Name:     "unit tests",
			Class:    "unit",
			Required: true,
			Argv:     helperArgv("pass", "unit output"),
		},
		{
			Name:     "smoke test",
			Class:    "smoke",
			Required: true,
			Argv:     helperArgv("pass", "smoke output"),
		},
	}
	governed := newAuthority(t, repository, commands)
	store := newEvidenceStore(t)
	t.Setenv("GO_WANT_ACCEPTANCE_HELPER", "1")

	result, err := acceptance.New(supervisor.New(), store).Run(context.Background(), governed)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status() != acceptance.StatusPass || !result.Passed() {
		t.Fatalf("acceptance result = %s (%s), want PASS", result.Status(), result.FailureReason())
	}
	state, ok := result.BranchAcceptedState()
	if !ok || state != domain.StateBranchAccepted {
		t.Fatalf("accepted state = %q, %t; want %q, true", state, ok, domain.StateBranchAccepted)
	}

	records := result.Commands()
	if len(records) != len(commands) {
		t.Fatalf("command records = %d, want %d", len(records), len(commands))
	}
	for index, record := range records {
		if record.Index != index+1 || record.Name != commands[index].Name || record.Class != commands[index].Class ||
			!record.Required || record.PolicyVersion != "acceptance-policy-v1" {
			t.Fatalf("command %d metadata = %#v", index+1, record)
		}
		if record.Process.Outcome != supervisor.OutcomeSucceeded || record.Process.ExitCode != 0 {
			t.Fatalf("command %d process = %#v, want success", index+1, record.Process)
		}
		if !reflect.DeepEqual(record.Process.Argv, commands[index].Argv) || record.Process.Cwd != repository {
			t.Fatalf("command %d identity = %#v in %q", index+1, record.Process.Argv, record.Process.Cwd)
		}
		assertEvidenceContains(t, record.Process.StdoutRef.URI, commands[index].Argv[len(commands[index].Argv)-1]+"\n")
		assertMetadata(t, record.MetadataRef.URI, commands[index].Class, "acceptance-policy-v1")
	}

	gitEvidence, ok := result.FinalGit()
	if !ok {
		t.Fatal("final Git evidence was not captured")
	}
	if gitEvidence.HeadSHA != gitOutput(t, repository, "rev-parse", "HEAD") {
		t.Fatalf("final HEAD = %q, want repository HEAD", gitEvidence.HeadSHA)
	}
	if !gitEvidence.Dirty {
		t.Fatal("final Git status did not record the untracked file")
	}
	if !reflect.DeepEqual(gitEvidence.HeadProcess.Argv, []string{"git", "rev-parse", "--verify", "HEAD"}) {
		t.Fatalf("HEAD argv = %#v", gitEvidence.HeadProcess.Argv)
	}
	if !reflect.DeepEqual(gitEvidence.StatusProcess.Argv, []string{"git", "status", "--porcelain=v1", "--untracked-files=normal"}) {
		t.Fatalf("status argv = %#v", gitEvidence.StatusProcess.Argv)
	}
	assertEvidenceContains(t, gitEvidence.HeadProcess.StdoutRef.URI, gitEvidence.HeadSHA+"\n")
	assertEvidenceContains(t, gitEvidence.StatusProcess.StdoutRef.URI, "?? untracked.txt\n")
	assertMetadata(t, gitEvidence.MetadataRef.URI, "\"dirty\":true", "acceptance-policy-v1")
}

func TestExecutorStopsAtFirstRequiredFailureAndCannotAcceptBranch(t *testing.T) {
	repository := newGitRepository(t)
	marker := filepath.Join(t.TempDir(), "second-command-ran")
	commands := []authority.AcceptanceCommand{
		{
			Name:     "failing test",
			Class:    "unit",
			Required: true,
			Argv:     helperArgv("fail"),
		},
		{
			Name:     "must not run",
			Class:    "smoke",
			Required: true,
			Argv:     helperArgv("mark", marker),
		},
	}
	governed := newAuthority(t, repository, commands)
	store := newEvidenceStore(t)
	t.Setenv("GO_WANT_ACCEPTANCE_HELPER", "1")

	result, err := acceptance.New(supervisor.New(), store).Run(context.Background(), governed)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status() != acceptance.StatusFail || result.Passed() {
		t.Fatalf("acceptance result = %s, passed=%t; want FAIL", result.Status(), result.Passed())
	}
	if records := result.Commands(); len(records) != 1 || records[0].Process.ExitCode != 17 {
		t.Fatalf("executed command records = %#v, want only exit 17", records)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("second command ran or marker inspection failed: %v", err)
	}
	if _, ok := result.FinalGit(); ok {
		t.Fatal("final Git capture ran after a required command failure")
	}
	if state, ok := result.BranchAcceptedState(); ok || state != "" {
		t.Fatalf("failed acceptance yielded state %q, %t", state, ok)
	}
	if _, err := os.Stat(filepath.Join(store.RunDir(), "acceptance-git-head-stdout.log")); !os.IsNotExist(err) {
		t.Fatalf("Git HEAD command ran after required failure: %v", err)
	}
}

func TestExecutorContinuesAfterOptionalFailure(t *testing.T) {
	repository := newGitRepository(t)
	marker := filepath.Join(t.TempDir(), "required-command-ran")
	commands := []authority.AcceptanceCommand{
		{Name: "advisory", Class: "static", Required: false, Argv: helperArgv("fail")},
		{Name: "required", Class: "unit", Required: true, Argv: helperArgv("mark", marker)},
	}
	governed := newAuthority(t, repository, commands)
	store := newEvidenceStore(t)
	t.Setenv("GO_WANT_ACCEPTANCE_HELPER", "1")

	result, err := acceptance.New(supervisor.New(), store).Run(context.Background(), governed)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed() || result.Status() != acceptance.StatusPass {
		t.Fatalf("optional failure prevented acceptance: %s (%s)", result.Status(), result.FailureReason())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("required command did not run after optional failure: %v", err)
	}
	if records := result.Commands(); len(records) != 2 || records[0].Process.ExitCode != 17 || records[1].Process.ExitCode != 0 {
		t.Fatalf("command outcomes = %#v", records)
	}
}

func TestExecutorRejectsMissingDependencies(t *testing.T) {
	repository := newGitRepository(t)
	governed := newAuthority(t, repository, []authority.AcceptanceCommand{{Required: true, Argv: helperArgv("pass", "ok")}})
	store := newEvidenceStore(t)

	if _, err := acceptance.New(nil, store).Run(context.Background(), governed); err == nil {
		t.Fatal("executor accepted a nil command runner")
	}
	if _, err := acceptance.New(supervisor.New(), nil).Run(context.Background(), governed); err == nil {
		t.Fatal("executor accepted a nil evidence writer")
	}
	if _, err := acceptance.New(supervisor.New(), store).Run(nil, governed); err == nil {
		t.Fatal("executor accepted a nil context")
	}
}

func TestAcceptanceHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_ACCEPTANCE_HELPER") != "1" {
		return
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		os.Exit(90)
	}

	switch os.Args[separator+1] {
	case "pass":
		if separator+2 >= len(os.Args) {
			os.Exit(91)
		}
		fmt.Fprintln(os.Stdout, os.Args[separator+2])
		fmt.Fprintln(os.Stderr, "acceptance helper stderr")
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stdout, "output before failure")
		fmt.Fprintln(os.Stderr, "acceptance helper failure")
		os.Exit(17)
	case "mark":
		if separator+2 >= len(os.Args) {
			os.Exit(92)
		}
		if err := os.WriteFile(os.Args[separator+2], []byte("ran\n"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(93)
		}
		os.Exit(0)
	default:
		os.Exit(94)
	}
}

func helperArgv(arguments ...string) []string {
	argv := []string{os.Args[0], "-test.run=^TestAcceptanceHelperProcess$", "--"}
	return append(argv, arguments...)
}

func newGitRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	planPath := filepath.Join(repository, "plan.md")
	if err := os.WriteFile(planPath, []byte("governed plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "-q")
	runGit(t, repository, "add", "plan.md")
	command := exec.Command("git", "-c", "user.name=Acceptance Test", "-c", "user.email=acceptance@example.invalid", "commit", "-qm", "initial")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v: %s", err, output)
	}
	return repository
}

func newAuthority(t *testing.T, repository string, commands []authority.AcceptanceCommand) authority.Authority {
	t.Helper()
	planPath := filepath.Join(repository, "plan.md")
	governed, err := authority.New(authority.Manifest{
		RunID: "acceptance-test",
		Repository: authority.RepositoryManifest{
			Path:     repository,
			StartSHA: gitOutput(t, repository, "rev-parse", "HEAD"),
		},
		Plan: authority.PlanManifest{
			Path:   planPath,
			SHA256: fileSHA256(t, planPath),
		},
		Ralphex: authority.RalphexManifest{
			BinaryPath:   os.Args[0],
			BinarySHA256: fileSHA256(t, os.Args[0]),
			Mode:         ralphex.ModeFull,
		},
		Acceptance:    commands,
		PolicyVersion: "acceptance-policy-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return governed
}

func newEvidenceStore(t *testing.T) *evidence.Store {
	t.Helper()
	store, err := evidence.NewStore(t.TempDir(), "acceptance-test")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func runGit(t *testing.T, repository string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v: %s", strings.Join(arguments, " "), err, output)
	}
}

func gitOutput(t *testing.T, repository string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = repository
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s failed: %v", strings.Join(arguments, " "), err)
	}
	return strings.TrimSpace(string(output))
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func assertEvidenceContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("evidence %q = %q, want %q", path, data, want)
	}
}

func assertMetadata(t *testing.T, path string, values ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("metadata %q is not valid JSON: %q", path, data)
	}
	for _, value := range values {
		if !strings.Contains(string(data), value) {
			t.Fatalf("metadata %q does not contain %q: %s", path, value, data)
		}
	}
}

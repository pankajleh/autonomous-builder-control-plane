//go:build linux

package recovery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
)

func TestInspectClassifiesLiveOwnerActive(t *testing.T) {
	fixture := newWorktreeFixture(t)
	owned := fixture.ownership(t, os.Getpid())

	result := Inspect(context.Background(), owned)

	if result.Classification != ClassificationActive || result.OwnerProof != OwnerProofMatching {
		t.Fatalf("inspection = (%q, %q, %q), want active matching owner", result.Classification, result.OwnerProof, result.Reason)
	}
	if result.ObservedProcess == nil || *result.ObservedProcess != owned.Process {
		t.Fatalf("observed process = %#v, want %#v", result.ObservedProcess, owned.Process)
	}
	if result.ObservedBranch != fixture.branch {
		t.Fatalf("observed branch = %q, want %q", result.ObservedBranch, fixture.branch)
	}
}

func TestInspectClassifiesDeadOwnerStale(t *testing.T) {
	fixture := newWorktreeFixture(t)
	process := startRecoveryHelper(t)
	owned := fixture.ownership(t, process.Process.Pid)
	if err := process.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err == nil {
		t.Fatal("killed helper exited successfully")
	}

	result := Inspect(context.Background(), owned)

	if result.Classification != ClassificationStaleOwnerDead || result.OwnerProof != OwnerProofProcessAbsent {
		t.Fatalf("inspection = (%q, %q, %q), want stale absent owner", result.Classification, result.OwnerProof, result.Reason)
	}
}

func TestInspectDetectsPIDReuseMismatch(t *testing.T) {
	fixture := newWorktreeFixture(t)
	owned := fixture.ownership(t, os.Getpid())
	owned.Process.LinuxStartTicks++

	result := Inspect(context.Background(), owned)

	if result.Classification != ClassificationStaleOwnerDead || result.OwnerProof != OwnerProofIdentityMismatch {
		t.Fatalf("inspection = (%q, %q, %q), want stale identity mismatch", result.Classification, result.OwnerProof, result.Reason)
	}
	if result.ObservedProcess == nil || result.ObservedProcess.PID != os.Getpid() {
		t.Fatalf("observed process = %#v, want current PID", result.ObservedProcess)
	}
}

func TestInspectClassifiesMissingWorktree(t *testing.T) {
	fixture := newWorktreeFixture(t)
	owned := fixture.ownership(t, os.Getpid())
	owned.Worktree.Path = filepath.Join(fixture.root, "missing-worktree")

	result := Inspect(context.Background(), owned)

	if result.Classification != ClassificationMissing || result.OwnerProof != OwnerProofUnavailable {
		t.Fatalf("inspection = (%q, %q, %q), want missing with no owner proof", result.Classification, result.OwnerProof, result.Reason)
	}
}

func TestInspectRejectsWorktreePathEscape(t *testing.T) {
	fixture := newWorktreeFixture(t)
	owned := fixture.ownership(t, os.Getpid())
	owned.Worktree.Path = t.TempDir()

	result := Inspect(context.Background(), owned)

	if result.Classification != ClassificationAmbiguous || !strings.Contains(result.Reason, "escapes") {
		t.Fatalf("inspection = (%q, %q), want ambiguous path escape", result.Classification, result.Reason)
	}
}

func TestInspectRejectsSymlinkWorktreePath(t *testing.T) {
	fixture := newWorktreeFixture(t)
	owned := fixture.ownership(t, os.Getpid())
	link := filepath.Join(fixture.root, "linked-worktree")
	if err := os.Symlink(fixture.worktree, link); err != nil {
		t.Fatal(err)
	}
	owned.Worktree.Path = link

	result := Inspect(context.Background(), owned)

	if result.Classification != ClassificationAmbiguous || !strings.Contains(result.Reason, "symlink") {
		t.Fatalf("inspection = (%q, %q), want ambiguous symlink", result.Classification, result.Reason)
	}
}

func TestInspectFailsClosedOnAmbiguousOwnerProof(t *testing.T) {
	fixture := newWorktreeFixture(t)
	owned := fixture.ownership(t, os.Getpid())
	owned.Process.BootID = ""

	result := Inspect(context.Background(), owned)

	if result.Classification != ClassificationAmbiguous || result.OwnerProof != OwnerProofUnavailable {
		t.Fatalf("inspection = (%q, %q, %q), want ambiguous unavailable proof", result.Classification, result.OwnerProof, result.Reason)
	}
}

func TestInspectFailsClosedOnBranchMismatch(t *testing.T) {
	fixture := newWorktreeFixture(t)
	owned := fixture.ownership(t, os.Getpid())
	owned.Worktree.Branch = "unexpected"

	result := Inspect(context.Background(), owned)

	if result.Classification != ClassificationAmbiguous || !strings.Contains(result.Reason, "branch mismatch") {
		t.Fatalf("inspection = (%q, %q), want ambiguous branch mismatch", result.Classification, result.Reason)
	}
}

func TestCaptureOwnershipRequiresGovernedAttemptAndLiveProcess(t *testing.T) {
	fixture := newWorktreeFixture(t)
	worktree := WorktreeIdentity{RepositoryPath: fixture.repository, RootPath: fixture.root, Path: fixture.worktree, Branch: fixture.branch}
	owned, err := CaptureOwnership(testAttempt(), worktree, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if owned.Attempt != testAttempt() || owned.Process.PID != os.Getpid() || owned.Process.LinuxStartTicks == 0 || owned.Process.BootID == "" {
		t.Fatalf("captured ownership = %#v", owned)
	}

	invalid := testAttempt()
	invalid.AttemptID = ""
	if _, err := CaptureOwnership(invalid, worktree, os.Getpid()); err == nil {
		t.Fatal("CaptureOwnership accepted an attempt without an attempt ID")
	}
}

func TestParseLinuxProcessStatHandlesParenthesesInCommand(t *testing.T) {
	fields := []string{"S"}
	for field := 4; field <= 21; field++ {
		fields = append(fields, strconv.Itoa(field))
	}
	fields = append(fields, "987654", "23")
	state, ticks, err := parseLinuxProcessStat([]byte("42 (helper ) name) " + strings.Join(fields, " ")))
	if err != nil {
		t.Fatal(err)
	}
	if state != "S" || ticks != 987654 {
		t.Fatalf("parsed stat = (%q, %d), want (S, 987654)", state, ticks)
	}
}

type worktreeFixture struct {
	root       string
	repository string
	worktree   string
	branch     string
}

func newWorktreeFixture(t *testing.T) worktreeFixture {
	t.Helper()
	root := t.TempDir()
	repository := filepath.Join(t.TempDir(), "repository")
	worktree := filepath.Join(root, "governed-worktree")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "README.md")
	runGit(t, repository, "commit", "-m", "fixture")
	branch := "attempt/run-1"
	runGit(t, repository, "worktree", "add", "-b", branch, worktree)
	return worktreeFixture{root: root, repository: repository, worktree: worktree, branch: branch}
}

func (fixture worktreeFixture) ownership(t *testing.T, pid int) Ownership {
	t.Helper()
	owned, err := CaptureOwnership(testAttempt(), WorktreeIdentity{
		RepositoryPath: fixture.repository,
		RootPath:       fixture.root,
		Path:           fixture.worktree,
		Branch:         fixture.branch,
	}, pid)
	if err != nil {
		t.Fatal(err)
	}
	return owned
}

func testAttempt() AttemptIdentity {
	return AttemptIdentity{
		ProjectID:      "project-1",
		PlanID:         "plan-1",
		RunID:          "run-1",
		AttemptID:      "attempt-1",
		TaskID:         "task-1",
		AgentSessionID: "session-1",
	}
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(gitexec.Environment(),
		"GIT_AUTHOR_NAME=Recovery Test",
		"GIT_AUTHOR_EMAIL=recovery@example.invalid",
		"GIT_COMMITTER_NAME=Recovery Test",
		"GIT_COMMITTER_EMAIL=recovery@example.invalid",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func startRecoveryHelper(t *testing.T) *exec.Cmd {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	command := exec.Command(os.Args[0], "-test.run=^TestRecoveryProcessHelper$")
	command.Env = append(os.Environ(), "GO_WANT_RECOVERY_HELPER=1", "RECOVERY_HELPER_READY="+ready)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return command
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for recovery helper")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRecoveryProcessHelper(t *testing.T) {
	if os.Getenv("GO_WANT_RECOVERY_HELPER") != "1" {
		return
	}
	if err := os.WriteFile(os.Getenv("RECOVERY_HELPER_READY"), []byte(fmt.Sprint(os.Getpid())), 0o600); err != nil {
		os.Exit(90)
	}
	for {
		time.Sleep(time.Hour)
	}
}

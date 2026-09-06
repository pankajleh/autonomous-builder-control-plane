//go:build linux

package integrationworkspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/scheduler"
)

func TestIntegrateCancellationIsBoundedKillsProcessGroupAndCleansWorkspace(t *testing.T) {
	repository := newRepository(t)
	baseline := commitFile(t, repository, "base.txt", "base\n", "baseline")
	head := branchCommit(t, repository, baseline, "candidate", "candidate.txt", "candidate\n")
	git(t, repository, "checkout", "--quiet", "main")
	candidate := acceptedCandidate(t, repository, "candidate", baseline, head, "run", time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC))
	report := riskReport(t, []scheduler.AcceptedCandidate{candidate})

	helperRoot := t.TempDir()
	readyPath := filepath.Join(helperRoot, "ready")
	ownedPIDPath := filepath.Join(helperRoot, "owned.pid")
	escapedPIDPath := filepath.Join(helperRoot, "escaped.pid")
	helper := filepath.Join(helperRoot, "git")
	script := `#!/bin/sh
/bin/sleep 60 &
owned_pid=$!
/usr/bin/setsid /bin/sleep 60 &
escaped_pid=$!
printf '%s\n' "$owned_pid" > "$TMPDIR/owned.pid"
printf '%s\n' "$escaped_pid" > "$TMPDIR/escaped.pid"
: > "$TMPDIR/ready"
wait "$owned_pid"
`
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", helperRoot+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("TMPDIR", helperRoot)

	temporaryRoot := t.TempDir()
	controller := newTestController(t, temporaryRoot, evidenceStore(t))
	controller.runner = cancellationGitRunner{
		repository: repository,
		exec: execGitRunner{
			stdoutLimitBytes: defaultStdoutLimit,
			stderrLimitBytes: defaultStderrLimit,
			waitDelay:        100 * time.Millisecond,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	type integrationOutcome struct {
		result Result
		err    error
	}
	done := make(chan integrationOutcome, 1)
	go func() {
		result, err := controller.Integrate(ctx, Request{BaselineSHA: baseline, RiskReport: report, EvidencePrefix: "cancelled"})
		done <- integrationOutcome{result: result, err: err}
	}()

	waitForIntegrationFile(t, readyPath)
	ownedPID := readIntegrationPID(t, ownedPIDPath)
	escapedPID := readIntegrationPID(t, escapedPIDPath)
	t.Cleanup(func() { _ = syscall.Kill(escapedPID, syscall.SIGKILL) })
	started := time.Now()
	cancel()
	var outcome integrationOutcome
	select {
	case outcome = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Integrate did not return within the cancellation deadline")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Integrate cancellation took %s, want less than one second", elapsed)
	}
	if !errors.Is(outcome.err, context.Canceled) || outcome.result.Status() != StatusUnavailable {
		t.Fatalf("cancelled integration = status %q failure %q err %v", outcome.result.Status(), outcome.result.Failure(), outcome.err)
	}
	commands := outcome.result.Commands()
	if len(commands) == 0 || strings.Join(commands[len(commands)-1].Argv, " ") != "git --no-replace-objects init --quiet --initial-branch=integration" {
		t.Fatalf("cancelled Git command evidence = %#v", commands)
	}
	assertEvidence(t, outcome.result.CaptureRef(), captureEvidenceKind)
	assertEvidence(t, outcome.result.CleanupRef(), cleanupEvidenceKind)
	if !outcome.result.Cleanup().WorkspaceRemoved {
		t.Fatal("cancelled integration did not report workspace removal")
	}
	if entries, err := os.ReadDir(temporaryRoot); err != nil || len(entries) != 0 {
		t.Fatalf("cancelled integration left a disposable workspace: entries=%v err=%v", entries, err)
	}
	waitForIntegrationProcessExit(t, ownedPID)
	if err := syscall.Kill(escapedPID, 0); err != nil {
		t.Fatalf("escaped helper unexpectedly exited before test cleanup: %v", err)
	}
}

type cancellationGitRunner struct {
	repository string
	exec       execGitRunner
}

func (runner cancellationGitRunner) Run(ctx context.Context, directory string, arguments ...string) (gitResult, error) {
	if directory != runner.repository {
		return runner.exec.Run(ctx, directory, arguments...)
	}
	result := gitResult{
		Argv: append([]string{"git"}, arguments...), ExitCode: 0,
		StdoutLimitBytes: defaultStdoutLimit, StderrLimitBytes: defaultStderrLimit,
	}
	if len(arguments) > 0 && arguments[len(arguments)-1] != "" && strings.HasSuffix(arguments[len(arguments)-1], "^{commit}") {
		objectID := strings.TrimSuffix(arguments[len(arguments)-1], "^{commit}")
		result.Stdout = []byte(objectID + "\n")
	}
	return result, nil
}

func waitForIntegrationFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for helper file %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readIntegrationPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func waitForIntegrationProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("inspect owned helper process %d: %v", pid, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("owned helper process %d survived process-group cancellation: %s", pid, fmt.Sprint(err))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

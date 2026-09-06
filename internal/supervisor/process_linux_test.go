//go:build linux

package supervisor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunCapturesExternalSignal(t *testing.T) {
	command, _ := helperCommand(t, "success")
	command.Argv = []string{os.Args[0], "-test.run=TestSupervisorSignalHelperProcess"}
	command.Env = append(os.Environ(), "GO_WANT_SUPERVISOR_SIGNAL_HELPER=1")
	result, err := New().Run(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeSignaled || result.ExitCode != -1 || result.TerminatingSignal != syscall.SIGTERM.String() {
		t.Fatalf("terminal result = (%q, %d, %q), want SIGTERM", result.Outcome, result.ExitCode, result.TerminatingSignal)
	}
}

func TestCancellationTerminatesDescendantProcess(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "descendant.pid")
	command, _ := helperCommand(t, "success")
	command.Argv = []string{os.Args[0], "-test.run=^TestSupervisorProcessTreeHelper$"}
	command.Env = append(os.Environ(),
		"GO_WANT_SUPERVISOR_TREE_PARENT=1",
		"GO_WANT_SUPERVISOR_TREE_PID_PATH="+pidPath,
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := New().Run(ctx, command)
		done <- err
	}()
	waitForFile(t, pidPath)
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	descendantPID, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not return after process-tree cancellation")
	}

	deadline := time.Now().Add(5 * time.Second)
	for processIsRunning(descendantPID) {
		if time.Now().After(deadline) {
			t.Fatalf("descendant process %d survived process-group cancellation", descendantPID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSupervisorProcessTreeHelper(t *testing.T) {
	if os.Getenv("GO_WANT_SUPERVISOR_TREE_CHILD") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if os.Getenv("GO_WANT_SUPERVISOR_TREE_PARENT") != "1" {
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestSupervisorProcessTreeHelper$")
	child.Env = append(os.Environ(), "GO_WANT_SUPERVISOR_TREE_PARENT=", "GO_WANT_SUPERVISOR_TREE_CHILD=1")
	if err := child.Start(); err != nil {
		os.Exit(96)
	}
	pidPath := os.Getenv("GO_WANT_SUPERVISOR_TREE_PID_PATH")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		os.Exit(97)
	}
	if err := child.Wait(); err != nil {
		os.Exit(98)
	}
}

func processIsRunning(pid int) bool {
	err := syscall.Kill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false
	}
	stat, readErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if readErr != nil {
		return !errors.Is(readErr, os.ErrNotExist)
	}
	closing := strings.LastIndexByte(string(stat), ')')
	return closing < 0 || closing+2 >= len(stat) || stat[closing+2] != 'Z'
}

func TestSupervisorSignalHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_SUPERVISOR_SIGNAL_HELPER") != "1" {
		return
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		os.Exit(94)
	}
	os.Exit(95)
}

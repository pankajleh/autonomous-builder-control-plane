//go:build linux

package supervisor

import (
	"context"
	"os"
	"syscall"
	"testing"
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

func TestSupervisorSignalHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_SUPERVISOR_SIGNAL_HELPER") != "1" {
		return
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		os.Exit(94)
	}
	os.Exit(95)
}

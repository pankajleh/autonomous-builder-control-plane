package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
)

func TestRunCapturesSuccessfulCommand(t *testing.T) {
	command, store := helperCommand(t, "success")
	result, err := New().Run(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}

	if result.Outcome != OutcomeSucceeded || result.ExitCode != 0 || result.TerminatingSignal != "" {
		t.Fatalf("terminal result = (%q, %d, %q), want successful exit", result.Outcome, result.ExitCode, result.TerminatingSignal)
	}
	if result.PID <= 0 {
		t.Fatalf("PID = %d, want positive", result.PID)
	}
	if runtime.GOOS == "linux" && result.ProcessGroupID != result.PID {
		t.Fatalf("process group = %d, want child PID %d", result.ProcessGroupID, result.PID)
	}
	if result.StartedAt.IsZero() || result.EndedAt.Before(result.StartedAt) {
		t.Fatalf("invalid timestamps: %s to %s", result.StartedAt, result.EndedAt)
	}
	if !reflect.DeepEqual(result.Argv, command.Argv) || result.Cwd != command.Cwd {
		t.Fatalf("recorded command = %#v in %q, want %#v in %q", result.Argv, result.Cwd, command.Argv, command.Cwd)
	}
	assertArtifact(t, result.StdoutRef.URI, "stdout from helper\n")
	assertArtifact(t, result.StderrRef.URI, "stderr from helper\n")
	if filepath.Dir(result.StdoutRef.URI) != store.RunDir() || filepath.Dir(result.StderrRef.URI) != store.RunDir() {
		t.Fatal("output evidence was not written to the run evidence directory")
	}
}

func TestRunCapturesNonzeroExit(t *testing.T) {
	command, _ := helperCommand(t, "failure")
	result, err := New().Run(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeExited || result.ExitCode != 23 || result.TerminatingSignal != "" {
		t.Fatalf("terminal result = (%q, %d, %q), want non-zero exit 23", result.Outcome, result.ExitCode, result.TerminatingSignal)
	}
	assertArtifact(t, result.StdoutRef.URI, "before failure\n")
	assertArtifact(t, result.StderrRef.URI, "failure detail\n")
}

func TestRunCapturesCancellation(t *testing.T) {
	readyPath := filepath.Join(t.TempDir(), "ready")
	command, _ := helperCommand(t, "wait", readyPath)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	type response struct {
		result Result
		err    error
	}
	done := make(chan response, 1)
	go func() {
		result, err := New().Run(ctx, command)
		done <- response{result: result, err: err}
	}()

	waitForFile(t, readyPath)
	cancel()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.result.Outcome != OutcomeCanceled || got.result.ExitCode != -1 {
			t.Fatalf("terminal result = (%q, %d), want canceled signal exit", got.result.Outcome, got.result.ExitCode)
		}
		if runtime.GOOS == "linux" && got.result.TerminatingSignal == "" {
			t.Fatal("cancellation did not record its terminating signal")
		}
		assertArtifact(t, got.result.StdoutRef.URI, "waiting\n")
	case <-time.After(5 * time.Second):
		t.Fatal("canceled command did not terminate")
	}
}

func TestRunCapturesTimeout(t *testing.T) {
	command, _ := helperCommand(t, "wait", filepath.Join(t.TempDir(), "ready"))
	command.Timeout = 100 * time.Millisecond
	result, err := New().Run(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeTimedOut || result.ExitCode != -1 {
		t.Fatalf("terminal result = (%q, %d), want timed-out signal exit", result.Outcome, result.ExitCode)
	}
}

func TestRunTerminatesAndBoundsExcessiveOutput(t *testing.T) {
	command, _ := helperCommand(t, "spam")
	command.OutputLimitBytes = 64
	result, err := New().Run(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeOutputLimit || !result.StdoutTruncated || result.OutputLimitBytes != 64 {
		t.Fatalf("output-limit result = %#v", result)
	}
	data, err := os.ReadFile(result.StdoutRef.URI)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 64 {
		t.Fatalf("captured stdout bytes = %d, want 64", len(data))
	}
}

func TestRunValidatesStructuredInput(t *testing.T) {
	valid, _ := helperCommand(t, "success")
	tests := map[string]func(*Command){
		"argv":          func(command *Command) { command.Argv = nil },
		"executable":    func(command *Command) { command.Argv[0] = "" },
		"cwd":           func(command *Command) { command.Cwd = "" },
		"timeout":       func(command *Command) { command.Timeout = -time.Second },
		"output limit":  func(command *Command) { command.OutputLimitBytes = -1 },
		"stdout writer": func(command *Command) { command.Stdout.Writer = nil },
		"stdout name":   func(command *Command) { command.Stdout.Name = "" },
		"stdout kind":   func(command *Command) { command.Stdout.Kind = "" },
		"stderr writer": func(command *Command) { command.Stderr.Writer = nil },
		"stderr name":   func(command *Command) { command.Stderr.Name = "" },
		"stderr kind":   func(command *Command) { command.Stderr.Kind = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			command := valid
			command.Argv = append([]string(nil), valid.Argv...)
			mutate(&command)
			if _, err := New().Run(context.Background(), command); err == nil {
				t.Fatal("Run accepted invalid command")
			}
		})
	}
	if _, err := New().Run(nil, valid); err == nil {
		t.Fatal("Run accepted a nil context")
	}
}

func helperCommand(t *testing.T, mode string, extra ...string) (Command, *evidence.Store) {
	t.Helper()
	store, err := evidence.NewStore(t.TempDir(), "supervisor-test")
	if err != nil {
		t.Fatal(err)
	}
	argv := []string{os.Args[0], "-test.run=TestSupervisorHelperProcess", "--", mode}
	argv = append(argv, extra...)
	return Command{
		Argv: argv,
		Cwd:  t.TempDir(),
		Env:  append(os.Environ(), "GO_WANT_SUPERVISOR_HELPER=1"),
		Stdout: EvidenceSink{
			Writer: store,
			Name:   "stdout.log",
			Kind:   "process-stdout",
		},
		Stderr: EvidenceSink{
			Writer: store,
			Name:   "stderr.log",
			Kind:   "process-stderr",
		},
	}, store
}

func TestSupervisorHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_SUPERVISOR_HELPER") != "1" {
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
	case "success":
		fmt.Fprintln(os.Stdout, "stdout from helper")
		fmt.Fprintln(os.Stderr, "stderr from helper")
		os.Exit(0)
	case "failure":
		fmt.Fprintln(os.Stdout, "before failure")
		fmt.Fprintln(os.Stderr, "failure detail")
		os.Exit(23)
	case "wait":
		if separator+2 >= len(os.Args) {
			os.Exit(91)
		}
		fmt.Fprintln(os.Stdout, "waiting")
		if err := os.WriteFile(os.Args[separator+2], []byte("ready"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(92)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "spam":
		for index := 0; index < 1024; index++ {
			fmt.Fprint(os.Stdout, "0123456789abcdef")
		}
		os.Exit(0)
	default:
		os.Exit(93)
	}
}

func assertArtifact(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("artifact %q = %q, want %q", path, data, want)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for helper readiness file %q", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

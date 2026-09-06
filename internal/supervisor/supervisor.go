// Package supervisor executes governed subprocesses without a shell boundary.
package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

// Outcome describes how a command reached its terminal state.
type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeExited    Outcome = "exited_nonzero"
	OutcomeSignaled  Outcome = "signaled"
	OutcomeCanceled  Outcome = "canceled"
	OutcomeTimedOut  Outcome = "timed_out"
)

// ArtifactWriter is the immutable evidence operation required by a command.
// evidence.Store satisfies this interface.
type ArtifactWriter interface {
	WriteBytes(name, kind string, data []byte) (ledger.EvidenceRef, error)
}

// EvidenceSink identifies where one output stream is published.
type EvidenceSink struct {
	Writer ArtifactWriter
	Name   string
	Kind   string
}

// Command is the complete structured input to one subprocess execution.
// Argv[0] is executed directly; it is never interpreted by a shell. A nil Env
// inherits the controller environment, while a non-nil Env is passed exactly.
type Command struct {
	Argv    []string
	Cwd     string
	Env     []string
	Timeout time.Duration
	Stdout  EvidenceSink
	Stderr  EvidenceSink
}

// Result records the governed process and its immutable output evidence.
type Result struct {
	Outcome           Outcome
	PID               int
	ProcessGroupID    int
	StartedAt         time.Time
	EndedAt           time.Time
	ExitCode          int
	TerminatingSignal string
	Argv              []string
	Cwd               string
	StdoutRef         ledger.EvidenceRef
	StderrRef         ledger.EvidenceRef
}

// Runner executes structured commands.
type Runner struct{}

// New returns a process supervisor.
func New() *Runner {
	return &Runner{}
}

// Run executes command under ctx and records its terminal state. Process exit
// failures are represented by Result. Errors are reserved for invalid input,
// launch failures, and failures to publish output evidence.
func (r *Runner) Run(ctx context.Context, command Command) (Result, error) {
	if err := validate(command); err != nil {
		return Result{}, err
	}
	if ctx == nil {
		return Result{}, errors.New("context is required")
	}

	result := Result{
		ExitCode: -1,
		Argv:     append([]string(nil), command.Argv...),
		Cwd:      command.Cwd,
	}

	runCtx := ctx
	cancel := func() {}
	if command.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, command.Timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, command.Argv[0], command.Argv[1:]...)
	cmd.Dir = command.Cwd
	if command.Env != nil {
		cmd.Env = append([]string(nil), command.Env...)
	}
	configureProcess(cmd)
	cmd.Cancel = func() error {
		return cancelProcess(cmd)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	result.StartedAt = time.Now().UTC()
	if err := cmd.Start(); err != nil {
		result.EndedAt = time.Now().UTC()
		return result, fmt.Errorf("start command: %w", err)
	}
	result.PID = cmd.Process.Pid
	result.ProcessGroupID = processGroupID(cmd)

	waitErr := cmd.Wait()
	result.EndedAt = time.Now().UTC()
	classify(&result, runCtx, cmd, waitErr)

	stdoutRef, err := command.Stdout.Writer.WriteBytes(command.Stdout.Name, command.Stdout.Kind, stdout.Bytes())
	if err != nil {
		return result, fmt.Errorf("publish stdout evidence: %w", err)
	}
	result.StdoutRef = stdoutRef
	stderrRef, err := command.Stderr.Writer.WriteBytes(command.Stderr.Name, command.Stderr.Kind, stderr.Bytes())
	if err != nil {
		return result, fmt.Errorf("publish stderr evidence: %w", err)
	}
	result.StderrRef = stderrRef

	return result, nil
}

func validate(command Command) error {
	if len(command.Argv) == 0 || command.Argv[0] == "" {
		return errors.New("command argv and executable are required")
	}
	if command.Cwd == "" {
		return errors.New("command cwd is required")
	}
	if command.Timeout < 0 {
		return errors.New("command timeout must not be negative")
	}
	if err := validateSink("stdout", command.Stdout); err != nil {
		return err
	}
	return validateSink("stderr", command.Stderr)
}

func validateSink(stream string, sink EvidenceSink) error {
	if sink.Writer == nil {
		return fmt.Errorf("%s evidence writer is required", stream)
	}
	if sink.Name == "" {
		return fmt.Errorf("%s evidence name is required", stream)
	}
	if sink.Kind == "" {
		return fmt.Errorf("%s evidence kind is required", stream)
	}
	return nil
}

func classify(result *Result, ctx context.Context, cmd *exec.Cmd, waitErr error) {
	result.ExitCode = cmd.ProcessState.ExitCode()
	if waitErr == nil {
		result.Outcome = OutcomeSucceeded
		return
	}

	if exitError, ok := waitErr.(*exec.ExitError); ok {
		result.TerminatingSignal = terminatingSignal(exitError)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Outcome = OutcomeTimedOut
		return
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		result.Outcome = OutcomeCanceled
		return
	}
	if result.TerminatingSignal != "" {
		result.Outcome = OutcomeSignaled
		return
	}
	result.Outcome = OutcomeExited
}

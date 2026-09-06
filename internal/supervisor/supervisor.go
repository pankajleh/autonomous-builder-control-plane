// Package supervisor executes governed subprocesses without a shell boundary.
package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

// Outcome describes how a command reached its terminal state.
type Outcome string

const (
	OutcomeSucceeded   Outcome = "succeeded"
	OutcomeExited      Outcome = "exited_nonzero"
	OutcomeSignaled    Outcome = "signaled"
	OutcomeCanceled    Outcome = "canceled"
	OutcomeTimedOut    Outcome = "timed_out"
	OutcomeOutputLimit Outcome = "output_limit_exceeded"
	OutcomeWaitDelay   Outcome = "pipe_wait_delay_exceeded"

	// DefaultOutputLimitBytes bounds each captured stream when a command does
	// not provide a smaller explicit limit.
	DefaultOutputLimitBytes int64 = 16 << 20
	// DefaultWaitDelay bounds waiting for output pipes retained by descendants
	// after the directly supervised process has exited.
	DefaultWaitDelay time.Duration = 2 * time.Second
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
	// WaitDelay bounds exec.Cmd pipe draining after process exit or context
	// cancellation. Zero selects DefaultWaitDelay.
	WaitDelay time.Duration
	// OutputLimitBytes bounds stdout and stderr independently. Zero selects
	// DefaultOutputLimitBytes.
	OutputLimitBytes int64
	Stdout           EvidenceSink
	Stderr           EvidenceSink
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
	OutputLimitBytes  int64
	Timeout           time.Duration
	WaitDelay         time.Duration
	StdoutTruncated   bool
	StderrTruncated   bool
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
		Timeout:  command.Timeout,
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
	waitDelay := command.WaitDelay
	if waitDelay == 0 {
		waitDelay = DefaultWaitDelay
	}
	cmd.WaitDelay = waitDelay
	result.WaitDelay = waitDelay

	limit := command.OutputLimitBytes
	if limit == 0 {
		limit = DefaultOutputLimitBytes
	}
	result.OutputLimitBytes = limit
	limitExceeded := make(chan struct{}, 1)
	stdout := newBoundedBuffer(limit, limitExceeded)
	stderr := newBoundedBuffer(limit, limitExceeded)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	result.StartedAt = time.Now().UTC()
	if err := cmd.Start(); err != nil {
		result.EndedAt = time.Now().UTC()
		return result, fmt.Errorf("start command: %w", err)
	}
	result.PID = cmd.Process.Pid
	result.ProcessGroupID = processGroupID(cmd)

	waited := make(chan error, 1)
	go func() {
		waited <- cmd.Wait()
	}()
	waitErr := error(nil)
	outputLimited := false
	select {
	case waitErr = <-waited:
		outputLimited = stdout.Truncated() || stderr.Truncated()
	case <-limitExceeded:
		outputLimited = true
		_ = cancelProcess(cmd)
		waitErr = <-waited
	}
	// The direct child can exit while descendants in its process group remain
	// alive. Terminate that governed group before returning control to the
	// caller so descendants cannot outlive terminal classification and mutate
	// state during later acceptance.
	_ = cancelProcess(cmd)
	result.EndedAt = time.Now().UTC()
	result.StdoutTruncated = stdout.Truncated()
	result.StderrTruncated = stderr.Truncated()
	classify(&result, runCtx, cmd, waitErr, outputLimited)

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
	if command.WaitDelay < 0 {
		return errors.New("command wait delay must not be negative")
	}
	if command.OutputLimitBytes < 0 {
		return errors.New("command output limit must not be negative")
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

func classify(result *Result, ctx context.Context, cmd *exec.Cmd, waitErr error, outputLimited bool) {
	result.ExitCode = cmd.ProcessState.ExitCode()
	if waitErr == nil {
		if outputLimited {
			result.Outcome = OutcomeOutputLimit
		} else {
			result.Outcome = OutcomeSucceeded
		}
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
	if outputLimited {
		result.Outcome = OutcomeOutputLimit
		return
	}
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		result.Outcome = OutcomeWaitDelay
		return
	}
	if result.TerminatingSignal != "" {
		result.Outcome = OutcomeSignaled
		return
	}
	result.Outcome = OutcomeExited
}

type boundedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	remaining int64
	truncated bool
	exceeded  chan<- struct{}
}

func newBoundedBuffer(limit int64, exceeded chan<- struct{}) boundedBuffer {
	return boundedBuffer{remaining: limit, exceeded: exceeded}
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	written := len(data)
	if int64(len(data)) <= b.remaining {
		_, _ = b.buffer.Write(data)
		b.remaining -= int64(len(data))
		return written, nil
	}
	if b.remaining > 0 {
		_, _ = b.buffer.Write(data[:b.remaining])
		b.remaining = 0
	}
	if !b.truncated {
		b.truncated = true
		select {
		case b.exceeded <- struct{}{}:
		default:
		}
	}
	return written, nil
}

func (b *boundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	return append([]byte(nil), b.buffer.Bytes()...)
}

func (b *boundedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.truncated
}

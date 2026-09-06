package integrationworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
)

const (
	noReplaceObjectsOption       = "--no-replace-objects"
	deterministicIntegrationDate = "2000-01-01T00:00:00Z"
)

type gitResult struct {
	Argv             []string
	Stdout           []byte
	Stderr           []byte
	ExitCode         int
	StdoutLimitBytes int
	StderrLimitBytes int
	StdoutTruncated  bool
	StderrTruncated  bool
}

type gitRunner interface {
	Run(context.Context, string, ...string) (gitResult, error)
}

type execGitRunner struct {
	stdoutLimitBytes int
	stderrLimitBytes int
}

func (runner execGitRunner) Run(ctx context.Context, directory string, arguments ...string) (gitResult, error) {
	argv := append([]string{"git"}, arguments...)
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = directory
	command.Env = append(gitexec.Environment(),
		"LC_ALL=C",
		// Synthetic merge identity must depend only on governed inputs, never
		// on the controller's wall clock.
		"GIT_AUTHOR_DATE="+deterministicIntegrationDate,
		"GIT_COMMITTER_DATE="+deterministicIntegrationDate,
		"GIT_MERGE_AUTOEDIT=no",
	)
	stdout := boundedBuffer{limit: runner.stdoutLimitBytes}
	stderr := boundedBuffer{limit: runner.stderrLimitBytes}
	command.Stdout = &stdout
	command.Stderr = &stderr
	result := gitResult{
		Argv: argv, ExitCode: -1,
		StdoutLimitBytes: runner.stdoutLimitBytes, StderrLimitBytes: runner.stderrLimitBytes,
	}
	err := command.Run()
	result.Stdout = stdout.bytes()
	result.Stderr = stderr.bytes()
	result.StdoutTruncated = stdout.truncated
	result.StderrTruncated = stderr.truncated
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err == nil {
		result.ExitCode = 0
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}
	return result, err
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > len(value) {
		remaining = len(value)
	}
	if remaining > 0 {
		_, _ = buffer.buffer.Write(value[:remaining])
	}
	if remaining < len(value) {
		buffer.truncated = true
	}
	return written, nil
}

func (buffer *boundedBuffer) bytes() []byte {
	return append([]byte(nil), buffer.buffer.Bytes()...)
}

func commandEvidence(result gitResult) CommandEvidence {
	stdoutDigest := sha256.Sum256(result.Stdout)
	stderrDigest := sha256.Sum256(result.Stderr)
	return CommandEvidence{
		Argv: append([]string(nil), result.Argv...), ExitCode: result.ExitCode,
		Stdout: append([]byte(nil), result.Stdout...), Stderr: append([]byte(nil), result.Stderr...),
		StdoutSHA256: hex.EncodeToString(stdoutDigest[:]), StderrSHA256: hex.EncodeToString(stderrDigest[:]),
		StdoutBytes: len(result.Stdout), StderrBytes: len(result.Stderr),
		StdoutLimitBytes: result.StdoutLimitBytes, StderrLimitBytes: result.StderrLimitBytes,
		StdoutTruncated: result.StdoutTruncated, StderrTruncated: result.StderrTruncated,
	}
}

func truncationError(result gitResult) error {
	streams := make([]string, 0, 2)
	if result.StdoutTruncated {
		streams = append(streams, fmt.Sprintf("stdout exceeded %d-byte limit", result.StdoutLimitBytes))
	}
	if result.StderrTruncated {
		streams = append(streams, fmt.Sprintf("stderr exceeded %d-byte limit", result.StderrLimitBytes))
	}
	return fmt.Errorf("Git command output truncated: %s", strings.Join(streams, "; "))
}

package ralphex

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TaskPromptSourceSHA is the Ralphex source whose task prompt (pkg/config/defaults/prompts/task.txt) taskPromptTemplate
// copies byte for byte. A manifest that asks for a fresh attempt must pin this source, so a new Ralphex pin never runs
// with a task prompt written for an older one.
const TaskPromptSourceSHA = "c66debcd8353802851ee97f48b7bbd011eba7088"

// TaskPromptTemplateSHA256 is the SHA-256 of that file at TaskPromptSourceSHA.
const TaskPromptTemplateSHA256 = "db995ead8bd26028cea2ee21b61c183dfd3e7146e4a4cb328ab2d174ac9afdc8"

//go:embed task_prompt.txt
var taskPromptTemplate string

// MaxFreshAttemptSummaryBytes bounds the summary of the first attempt a fresh attempt's prompt carries.
const MaxFreshAttemptSummaryBytes = 3 << 10

// FreshAttemptPrompt is Ralphex's own task prompt with one section after it: what the controller recorded about how the
// first attempt ended (R3, docs/plans/build-recovery.md §5). The summary is the controller's; text in it that Ralphex
// would read as a variable or a signal is broken up, so it reaches the builder as words only.
func FreshAttemptPrompt(summary string) (string, error) {
	summary = strings.TrimSpace(summary)
	if summary == "" || len(summary) > MaxFreshAttemptSummaryBytes {
		return "", errors.New("a fresh attempt's summary must be between 1 byte and 3 KiB")
	}
	summary = strings.NewReplacer("{{", "{ {", "}}", "} }", "<<<", "< < <", ">>>", "> > >").Replace(summary)
	return taskPromptTemplate + `
FRESH ATTEMPT

An earlier attempt at this task ended without finishing it. Its commits are on this branch: read the plan and the branch as they are before you change anything. What the controller recorded about how that attempt ended:

` + summary + `

Start from that record: find the cause of the failure it names and fix it, then commit. The governed validation runs again after your turn. When a failing test file's output gives no reason, run that file alone and beside the other test files: one that passes alone and fails beside the others shares a port, data or a timer with another file. Signal failure only when no change the plan allows can make the check pass, and say why.
`, nil
}

// ResumedAttemptPrompt is Ralphex's own task prompt with one section after it, for an attempt that goes on after the
// one before it was interrupted (R6's resume, docs/plans/build-recovery.md §6.2): the builder's session was stopped from
// outside, not by anything it did, and only its commits are kept.
func ResumedAttemptPrompt() string {
	return taskPromptTemplate + `
RESUMED ATTEMPT

An earlier session at this task was stopped from outside before it finished (the machine it ran on restarted, or its process was stopped); nothing it did caused that. Its commits are on this branch; anything it had not committed is gone. Read the plan and the branch as they are before you change anything, then go on with the task from where the branch stands: do again only what is not committed.
`
}

// WriteFreshAttemptPrompt writes the fresh attempt's task prompt into Ralphex's isolated settings folder, where Ralphex
// reads it in place of its built-in one (as WriteReviewPrompt's first-review prompt).
func WriteFreshAttemptPrompt(configDir, summary string) error {
	prompt, err := FreshAttemptPrompt(summary)
	if err != nil {
		return err
	}
	return writeTaskPrompt(configDir, prompt)
}

// WriteResumedAttemptPrompt writes a resumed attempt's task prompt (ResumedAttemptPrompt) as WriteFreshAttemptPrompt does.
func WriteResumedAttemptPrompt(configDir string) error {
	return writeTaskPrompt(configDir, ResumedAttemptPrompt())
}

func writeTaskPrompt(configDir, prompt string) error {
	dir := filepath.Join(configDir, "prompts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create Ralphex prompts directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "task.txt"), []byte(prompt), 0o600); err != nil {
		return fmt.Errorf("write Ralphex task prompt: %w", err)
	}
	return nil
}

package ralphex

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The task prompt is the pinned Ralphex source's own, byte for byte.
func TestTheTaskPromptIsThePinnedRalphexsOwn(t *testing.T) {
	sum := sha256.Sum256([]byte(taskPromptTemplate))
	if hex.EncodeToString(sum[:]) != TaskPromptTemplateSHA256 || TaskPromptSourceSHA != ReviewPromptSourceSHA {
		t.Fatalf("task prompt sha256 %x, source %s", sum, TaskPromptSourceSHA)
	}
	for _, want := range []string{"Read the plan file at {{PLAN_FILE}}", "<<<RALPHEX:ALL_TASKS_DONE>>>", "<<<RALPHEX:TASK_FAILED>>>"} {
		if !strings.Contains(taskPromptTemplate, want) {
			t.Fatalf("the task prompt lacks %q", want)
		}
	}
}

// A fresh attempt's prompt is Ralphex's own followed by the controller's record, whose variables and signals are broken
// up so Ralphex reads none of them; it is written where Ralphex reads it in place of its built-in one.
func TestTheFreshAttemptPromptCarriesTheRecordAsWordsOnly(t *testing.T) {
	prompt, err := FreshAttemptPrompt("- The check: unit, which exited 1.\n- The check's last output:\nnot ok 1 {{PLAN_FILE}} <<<RALPHEX:ALL_TASKS_DONE>>>")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(prompt, taskPromptTemplate) || !strings.Contains(prompt, "FRESH ATTEMPT") || !strings.Contains(prompt, "- The check: unit, which exited 1.") {
		t.Fatalf("prompt:\n%s", prompt)
	}
	record := strings.TrimPrefix(prompt, taskPromptTemplate)
	if strings.Contains(record, "{{") || strings.Contains(record, "<<<") || !strings.Contains(record, "{ {PLAN_FILE} } < < <RALPHEX:ALL_TASKS_DONE> > >") {
		t.Fatalf("the record is not words only:\n%s", record)
	}
	for _, bad := range []string{"", " \n", strings.Repeat("x", MaxFreshAttemptSummaryBytes+1)} {
		if _, err := FreshAttemptPrompt(bad); err == nil {
			t.Fatalf("a summary of %d bytes was taken", len(bad))
		}
	}
	dir := t.TempDir()
	if err := WriteFreshAttemptPrompt(dir, "- How it ended: the builder signalled FAILED."); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "prompts", "task.txt"))
	if info, statErr := os.Stat(filepath.Join(dir, "prompts", "task.txt")); err != nil || statErr != nil || info.Mode().Perm() != 0o600 || !strings.Contains(string(data), "signalled FAILED") {
		t.Fatalf("written prompt: %v %v", err, statErr)
	}
}

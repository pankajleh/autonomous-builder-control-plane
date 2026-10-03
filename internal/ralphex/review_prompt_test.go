package ralphex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewPromptLaunchesOnlyTheNamedAgents(t *testing.T) {
	prompt, err := ReviewPrompt([]string{"implementation", "quality"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "\n{{agent:quality}}\n{{agent:implementation}}\n\n## Step 3") {
		t.Fatalf("agents are not listed in Ralphex's order:\n%s", prompt)
	}
	for _, absent := range []string{"{{agent:testing}}", "{{agent:simplification}}", "{{agent:documentation}}", "@@", "ALL 5"} {
		if strings.Contains(prompt, absent) {
			t.Fatalf("prompt contains %q", absent)
		}
	}
	for _, line := range []string{"Launch ALL 2 Review Agents IN PARALLEL", "All 2 agent invocations", "ALL 2 agents have returned results", "<<<RALPHEX:REVIEW_DONE>>>"} {
		if !strings.Contains(prompt, line) {
			t.Fatalf("prompt lacks %q", line)
		}
	}
}

func TestReviewPromptWithEveryAgentIsRalphexsOwn(t *testing.T) {
	prompt, err := ReviewPrompt(ReviewAgents)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "{{agent:quality}}\n{{agent:implementation}}\n{{agent:testing}}\n{{agent:simplification}}\n{{agent:documentation}}\n") ||
		!strings.Contains(prompt, "Launch ALL 5 Review Agents IN PARALLEL") {
		t.Fatalf("five agents do not give Ralphex's own prompt:\n%s", prompt)
	}
	if strings.Count(reviewPromptTemplate, "@@AGENTS@@") != 1 || strings.Count(reviewPromptTemplate, "@@AGENT_COUNT@@") != 3 {
		t.Fatal("the prompt template's placeholders changed")
	}
}

func TestReviewAgentsAreValidated(t *testing.T) {
	for name, agents := range map[string][]string{
		"none":      nil,
		"one":       {"quality"},
		"unknown":   {"quality", "security"},
		"duplicate": {"quality", "quality"},
	} {
		if err := ValidateReviewAgents(agents); err == nil {
			t.Errorf("%s: accepted %v", name, agents)
		}
	}
	if err := ValidateReviewAgents([]string{"quality", "testing"}); err != nil {
		t.Fatal(err)
	}
}

func TestWriteReviewPromptPutsItWhereRalphexReadsIt(t *testing.T) {
	configDir := t.TempDir()
	if err := WriteReviewPrompt(configDir, []string{"quality", "implementation"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "prompts", "review_first.txt")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("prompt mode = %v", info.Mode().Perm())
	}
	if err := WriteReviewPrompt(t.TempDir(), []string{"quality"}); err == nil {
		t.Fatal("a one-agent review prompt was written")
	}
}

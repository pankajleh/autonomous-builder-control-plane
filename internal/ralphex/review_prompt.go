package ralphex

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// ReviewPromptSourceSHA is the Ralphex source whose first-review prompt reviewPromptTemplate copies. A manifest that
// names review agents must pin this source, so a new Ralphex pin cannot run with a prompt written for an older one.
const ReviewPromptSourceSHA = "c66debcd8353802851ee97f48b7bbd011eba7088"

// ReviewAgents are Ralphex's built-in review agents, in the order its own first-review prompt launches them.
var ReviewAgents = []string{"quality", "implementation", "testing", "simplification", "documentation"}

//go:embed review_first.txt
var reviewPromptTemplate string

// ExternalReviewTools are the outside review tools a manifest may name; "custom" is not offered because it needs a
// script ABCP does not govern.
var ExternalReviewTools = []string{"codex", "none"}

// ValidateReviewAgents accepts two or more of Ralphex's built-in review agents, each once.
func ValidateReviewAgents(agents []string) error {
	if len(agents) < 2 {
		return errors.New("ralphex.review_agents must name at least two agents")
	}
	seen := make(map[string]bool, len(agents))
	for _, agent := range agents {
		if !slices.Contains(ReviewAgents, agent) {
			return fmt.Errorf("ralphex.review_agents: unknown agent %q", agent)
		}
		if seen[agent] {
			return fmt.Errorf("ralphex.review_agents: %q is named twice", agent)
		}
		seen[agent] = true
	}
	return nil
}

// ReviewPrompt is the first-review prompt that launches only the given agents, in Ralphex's own order.
func ReviewPrompt(agents []string) (string, error) {
	if err := ValidateReviewAgents(agents); err != nil {
		return "", err
	}
	named := make(map[string]bool, len(agents))
	for _, agent := range agents {
		named[agent] = true
	}
	lines := make([]string, 0, len(agents))
	for _, agent := range ReviewAgents {
		if named[agent] {
			lines = append(lines, "{{agent:"+agent+"}}")
		}
	}
	prompt := strings.ReplaceAll(reviewPromptTemplate, "@@AGENT_COUNT@@", strconv.Itoa(len(lines)))
	return strings.Replace(prompt, "@@AGENTS@@", strings.Join(lines, "\n"), 1), nil
}

// WriteReviewPrompt writes the first-review prompt into Ralphex's isolated settings folder, where Ralphex reads it in
// place of its built-in one. Ralphex then installs none of its own prompts there and uses its built-in ones for the rest.
func WriteReviewPrompt(configDir string, agents []string) error {
	prompt, err := ReviewPrompt(agents)
	if err != nil {
		return err
	}
	dir := filepath.Join(configDir, "prompts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create Ralphex prompts directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "review_first.txt"), []byte(prompt), 0o600); err != nil {
		return fmt.Errorf("write Ralphex review prompt: %w", err)
	}
	return nil
}

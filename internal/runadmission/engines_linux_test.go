//go:build linux

package runadmission

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

const engineSecret = "sk-engine-value-never-shown-7QX"

func laneFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func environment(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestAnEngineOnTheControllersSignInIsReadyOnlyWithItsSignInFile(t *testing.T) {
	home := t.TempDir()
	env := environment(map[string]string{"HOME": home})
	codex := authority.ExecutorPolicy{Executor: "codex", TaskModel: "gpt-6-astra", TaskEffort: "xhigh", ReviewModel: "gpt-6-astra", ReviewEffort: "xhigh"}
	claude := authority.ExecutorPolicy{Executor: "claude", TaskEffort: "xhigh", ReviewEffort: "xhigh"}
	if got := engineOf("local-p02", codex, env); got.Ready || got.Access != "sign-in" || got.Reason != "not_signed_in" {
		t.Fatalf("codex without a sign-in = %+v", got)
	}
	if got := engineOf("local-p02-claude", claude, env); got.Ready || got.Reason != "not_signed_in" {
		t.Fatalf("claude without a sign-in = %+v", got)
	}
	touch(t, filepath.Join(home, ".codex", "auth.json"))
	touch(t, filepath.Join(home, ".claude", ".credentials.json"))
	got := engineOf("local-p02", codex, env)
	want := serviceapi.EngineV1{ProfileID: "local-p02", Executor: "codex", TaskModel: "gpt-6-astra", TaskEffort: "xhigh", ReviewModel: "gpt-6-astra",
		ReviewEffort: "xhigh", Access: "sign-in", Ready: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("codex signed in = %+v", got)
	}
	if got := engineOf("local-p02-claude", claude, env); !got.Ready || got.Reason != "" {
		t.Fatalf("claude signed in = %+v", got)
	}
	// The engine's own folder variable wins over the home, and a folder in place of the file is no sign-in.
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "auth.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := engineOf("local-p02", codex, environment(map[string]string{"HOME": home, "CODEX_HOME": other})); got.Ready {
		t.Fatalf("a folder named auth.json is not a sign-in: %+v", got)
	}
	if got := engineOf("local-p02-claude", claude, environment(map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": engineSecret})); !got.Ready {
		t.Fatalf("a token in the controller's environment signs Claude Code in: %+v", got)
	}
	if got := engineOf("x", authority.ExecutorPolicy{Executor: "other"}, env); got.Ready {
		t.Fatalf("an executor without a known sign-in is never ready: %+v", got)
	}
}

func TestAnEngineOnALaneIsReadyWithItsKeyAndShowsItsModelNeverAValue(t *testing.T) {
	claudeLane := laneFile(t, "claude-api", "CLAUDE_CONFIG_DIR=/home/devagent/.claude/lanes/claude-api\nANTHROPIC_API_KEY="+engineSecret+
		"\nANTHROPIC_MODEL=claude-sonnet-5-5\nANTHROPIC_SMALL_FAST_MODEL=claude-haiku-4-5\n")
	got := engineOf("local-p02-claude-api", authority.ExecutorPolicy{Executor: "claude", TaskEffort: "xhigh", ReviewModel: "haiku", Lane: "claude-api", LaneFile: claudeLane}, os.Getenv)
	want := serviceapi.EngineV1{ProfileID: "local-p02-claude-api", Executor: "claude", TaskEffort: "xhigh", ReviewModel: "haiku", Lane: "claude-api",
		LaneModel: "claude-sonnet-5-5", Access: "lane", Ready: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("claude lane = %+v", got)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), engineSecret) || strings.Contains(string(data), "/home/devagent") {
		t.Fatalf("no value but the model leaves the controller: %s", data)
	}

	// Codex on a lane: its key, or Codex signed in once in the lane's own home.
	codexHome := t.TempDir()
	codexLane := laneFile(t, "codex-api", "CODEX_HOME="+codexHome+"\n")
	policy := authority.ExecutorPolicy{Executor: "codex", TaskEffort: "xhigh", ReviewEffort: "xhigh", Lane: "codex-api", LaneFile: codexLane}
	if got := engineOf("local-p02-codex-api", policy, os.Getenv); got.Ready || got.Reason != "lane_no_key" {
		t.Fatalf("codex lane with no key and no sign-in = %+v", got)
	}
	touch(t, filepath.Join(codexHome, "auth.json"))
	if got := engineOf("local-p02-codex-api", policy, os.Getenv); !got.Ready {
		t.Fatalf("codex lane signed in in its home = %+v", got)
	}
	policy.LaneFile = laneFile(t, "codex-api", "OPENAI_API_KEY="+engineSecret+"\n")
	if got := engineOf("local-p02-codex-api", policy, os.Getenv); !got.Ready || got.LaneModel != "" {
		t.Fatalf("codex lane with a key = %+v", got)
	}

	// A lane file that is missing, or that its engine would refuse, is unreadable; a model name that looks wrong is left out.
	policy.LaneFile = filepath.Join(t.TempDir(), "missing.env")
	if got := engineOf("local-p02-codex-api", policy, os.Getenv); got.Ready || got.Reason != "lane_unreadable" {
		t.Fatalf("missing lane = %+v", got)
	}
	open := laneFile(t, "claude-api", "ANTHROPIC_API_KEY="+engineSecret+"\n")
	if err := os.Chmod(open, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := engineOf("p", authority.ExecutorPolicy{Executor: "claude", Lane: "claude-api", LaneFile: open}, os.Getenv); got.Reason != "lane_unreadable" {
		t.Fatalf("a lane others can read = %+v", got)
	}
	odd := laneFile(t, "claude-bedrock", "CLAUDE_CODE_USE_BEDROCK=1\nAWS_ACCESS_KEY_ID=AKIAEXAMPLE\nANTHROPIC_MODEL=a model with spaces\n")
	if got := engineOf("p", authority.ExecutorPolicy{Executor: "claude", Lane: "claude-bedrock", LaneFile: odd}, os.Getenv); got.Ready || got.LaneModel != "" || got.Reason != "lane_no_key" {
		t.Fatalf("bedrock without its secret, odd model = %+v", got)
	}
}

func TestTheControllerListsEveryProfilesEngineInProfileOrder(t *testing.T) {
	home := t.TempDir()
	touch(t, filepath.Join(home, ".codex", "auth.json"))
	controller := &Controller{getenv: environment(map[string]string{"HOME": home}), profiles: map[string]loadedProfile{
		"local-p02-claude": {template: authority.Manifest{Executor: authority.ExecutorPolicy{Executor: "claude", TaskEffort: "xhigh"}}},
		"local-p02":        {template: authority.Manifest{Executor: authority.ExecutorPolicy{Executor: "codex", TaskModel: "gpt-6-astra"}}},
	}}
	engines, err := controller.Engines(context.Background())
	if err != nil || engines.SchemaVersion != "EnginesV1" || len(engines.Engines) != 2 {
		t.Fatalf("engines = %+v, %v", engines, err)
	}
	if engines.Engines[0].ProfileID != "local-p02" || !engines.Engines[0].Ready || engines.Engines[1].ProfileID != "local-p02-claude" || engines.Engines[1].Ready {
		t.Fatalf("order and readiness = %+v", engines.Engines)
	}
	var none *Controller
	if empty, err := none.Engines(context.Background()); err != nil || empty.Engines == nil || len(empty.Engines) != 0 {
		t.Fatalf("no controller answers an empty list: %+v, %v", empty, err)
	}
}

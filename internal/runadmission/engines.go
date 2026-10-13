package runadmission

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/enginelane"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// EnginesSchemaV1 names the GET /v1/engines answer.
const EnginesSchemaV1 = "EnginesV1"

// laneModelPattern is what a lane's model name may look like to be reported;
// anything else is left out rather than shown.
var laneModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$`)

// Engines answers each admission profile's build engine (Repo C design note
// AI_KEYS_AND_MODELS §9.5): what its manifest runs, and whether its lane file
// or the controller's own sign-in holds credentials. Lane files are read again
// and sign-in files looked for on each call; no value leaves the controller.
func (c *Controller) Engines(context.Context) (serviceapi.EnginesV1, error) {
	if c == nil {
		return serviceapi.EnginesV1{SchemaVersion: EnginesSchemaV1, Engines: []serviceapi.EngineV1{}}, nil
	}
	ids := make([]string, 0, len(c.profiles))
	for id := range c.profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	getenv := c.getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	out := serviceapi.EnginesV1{SchemaVersion: EnginesSchemaV1, Engines: make([]serviceapi.EngineV1, 0, len(ids))}
	for _, id := range ids {
		engine := engineOf(id, c.profiles[id].template.Executor, getenv)
		if choices := c.profiles[id].configuration.ExecutorChoices; choices != nil {
			engine.Choices = &serviceapi.EngineChoicesV1{Models: slices.Clone(choices.Models), Efforts: slices.Clone(choices.Efforts)}
		}
		out.Engines = append(out.Engines, engine)
	}
	return out, nil
}

// engineOf describes one profile's engine. Ready means its credentials are in
// place, not that the vendor accepts them.
func engineOf(profileID string, policy authority.ExecutorPolicy, getenv func(string) string) serviceapi.EngineV1 {
	engine := serviceapi.EngineV1{
		ProfileID: profileID, Executor: policy.Executor,
		TaskModel: policy.TaskModel, TaskEffort: policy.TaskEffort, ReviewModel: policy.ReviewModel, ReviewEffort: policy.ReviewEffort,
	}
	if policy.Lane != "" {
		engine.Access, engine.Lane = "lane", policy.Lane
		lane, err := enginelane.Load(policy.Lane, policy.LaneFile, policy.Executor)
		if err != nil {
			engine.Reason = "lane_unreadable"
			return engine
		}
		if model := lane.Settings["ANTHROPIC_MODEL"]; laneModelPattern.MatchString(model) {
			engine.LaneModel = model
		}
		if laneHoldsKey(policy.Executor, lane.Settings) {
			engine.Ready = true
		} else {
			engine.Reason = "lane_no_key"
		}
		return engine
	}
	engine.Access = "sign-in"
	if signedIn(policy.Executor, getenv) {
		engine.Ready = true
	} else {
		engine.Reason = "not_signed_in"
	}
	return engine
}

// laneHoldsKey says whether a lane can sign its engine in: an API key, Codex
// signed in once in the lane's own home, a Claude setup token (an account
// lane, docs/plans/account-lanes.md), or Bedrock's credentials.
func laneHoldsKey(executor string, settings map[string]string) bool {
	switch executor {
	case "codex":
		if settings["OPENAI_API_KEY"] != "" {
			return true
		}
		home := settings["CODEX_HOME"]
		return filepath.IsAbs(home) && regularFile(filepath.Join(home, "auth.json"))
	case "claude":
		if settings["ANTHROPIC_API_KEY"] != "" || settings["CLAUDE_CODE_OAUTH_TOKEN"] != "" {
			return true
		}
		return settings["CLAUDE_CODE_USE_BEDROCK"] != "" && settings["AWS_ACCESS_KEY_ID"] != "" && settings["AWS_SECRET_ACCESS_KEY"] != ""
	}
	return false
}

// signedIn says whether the controller's own environment can sign the engine
// in: a key in its environment, or the engine's sign-in file in its home.
func signedIn(executor string, getenv func(string) string) bool {
	switch executor {
	case "codex":
		if getenv("OPENAI_API_KEY") != "" {
			return true
		}
		return regularFile(filepath.Join(configDirectory(getenv, "CODEX_HOME", ".codex"), "auth.json"))
	case "claude":
		if getenv("ANTHROPIC_API_KEY") != "" || getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" {
			return true
		}
		return regularFile(filepath.Join(configDirectory(getenv, "CLAUDE_CONFIG_DIR", ".claude"), ".credentials.json"))
	}
	return false
}

// configDirectory is the engine's folder: the one its variable names, else the
// named folder in the controller's home; empty when neither is absolute.
func configDirectory(getenv func(string) string, variable, inHome string) string {
	if dir := getenv(variable); filepath.IsAbs(dir) {
		return dir
	}
	if home := getenv("HOME"); filepath.IsAbs(home) {
		return filepath.Join(home, inHome)
	}
	return ""
}

func regularFile(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

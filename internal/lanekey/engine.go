package lanekey

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/enginelane"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// engineKey is where an executor's lane file holds an API key, and whose key it is.
type engineKey struct{ setting, provider string }

// engineKeys are the executors whose engine lanes pay with an API key in a lane file: Codex on OpenAI's API, Claude Code
// on Anthropic's (Repo C design note AI_KEYS_AND_MODELS §9.1: codex-api, claude-api).
var engineKeys = map[string]engineKey{
	"codex":  {setting: "OPENAI_API_KEY", provider: "openai"},
	"claude": {setting: "ANTHROPIC_API_KEY", provider: "anthropic"},
}

// EngineLaneNames reads abcp serve's --lane-key-engine-lanes: comma-separated lane names, none of them twice and none an
// account lane (an administrator's own sign-in is never written here). Empty is none, as before the flag.
func EngineLaneNames(list string, accountLanes []string) ([]string, error) {
	if strings.TrimSpace(list) == "" {
		return nil, nil
	}
	var names []string
	for _, name := range strings.Split(list, ",") {
		name = strings.TrimSpace(name)
		switch {
		case !enginelane.ValidName(name):
			return nil, fmt.Errorf("engine lane %q is not a lane name", name)
		case slices.Contains(names, name):
			return nil, fmt.Errorf("engine lane %q is named twice", name)
		case slices.Contains(accountLanes, name):
			return nil, fmt.Errorf("engine lane %q is an account lane: it takes no API key", name)
		}
		names = append(names, name)
	}
	return names, nil
}

// openAIBaseURLs are the addresses at which an OpenAI key is OpenAI's own.
var openAIBaseURLs = []string{"https://api.openai.com/v1", "https://api.openai.com/v1/"}

// paidOtherwise answers why a lane file's engine is not paid with its vendor's API key, or "": Claude Code on Amazon
// Bedrock or on an administrator's setup token, or Codex at another provider's address.
func paidOtherwise(executor string, settings map[string]string) string {
	switch executor {
	case "claude":
		if settings["CLAUDE_CODE_USE_BEDROCK"] != "" {
			return "bedrock"
		}
		if settings["CLAUDE_CODE_OAUTH_TOKEN"] != "" {
			return "setup-token"
		}
	case "codex":
		if url := settings["OPENAI_BASE_URL"]; url != "" && !slices.Contains(openAIBaseURLs, url) {
			return "another-address"
		}
	}
	return ""
}

// engineState reads an engine lane's file as a build reads it and answers its key's state; listed is false when the
// file is now paid another way, so the lane takes no key.
func engineState(lane EngineLane) (answer serviceapi.LaneV1, listed bool) {
	answer = serviceapi.LaneV1{Lane: lane.Name, Kind: serviceapi.LaneKindEngine, Provider: lane.Provider, Profiles: names(lane.Profiles), Uses: names(lane.Uses)}
	loaded, err := enginelane.Load(lane.Name, lane.LaneFile, lane.Executor)
	switch {
	case errors.Is(err, os.ErrNotExist):
		answer.State = serviceapi.LaneKeyMissing
	case err != nil:
		answer.State = serviceapi.LaneKeyUnreadable
	case paidOtherwise(lane.Executor, loaded.Settings) != "":
		return answer, false
	case loaded.Settings[lane.Setting] == "":
		answer.State = serviceapi.LaneKeyMissing
	default:
		answer.State, answer.Fingerprint = serviceapi.LaneKeySet, Fingerprint(loaded.Settings[lane.Setting])
	}
	return answer, true
}

// laneFile reads an engine lane's file as a build reads it (the controller's private file, no link in its path): its
// bytes and settings, or none when there is no file.
func laneFile(lane EngineLane) ([]byte, map[string]string, error) {
	data, err := enginelane.ReadPrivate(lane.LaneFile, enginelane.MaxBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, map[string]string{}, nil
	}
	if err != nil {
		return nil, nil, errors.New("the lane file cannot be read as a build reads it")
	}
	settings, err := enginelane.Parse(data, lane.Executor)
	if err != nil {
		return nil, nil, errors.New("the lane file is not one a build reads")
	}
	return data, settings, nil
}

// heldState is a lane's state from its settings, as engineState answers it.
func heldState(lane EngineLane, settings map[string]string) serviceapi.LaneV1 {
	answer := serviceapi.LaneV1{Lane: lane.Name, Kind: serviceapi.LaneKindEngine, Provider: lane.Provider, Profiles: names(lane.Profiles),
		Uses: names(lane.Uses), State: serviceapi.LaneKeyMissing}
	if value := settings[lane.Setting]; value != "" {
		answer.State, answer.Fingerprint = serviceapi.LaneKeySet, Fingerprint(value)
	}
	return answer
}

// setEngineKey replaces the key setting's line of an engine lane's file (or adds it), keeping every other line byte for
// byte, writes the file whole as a key lane's is written, and reads it back as the next build will.
func (m *Manager) setEngineKey(ctx context.Context, lane EngineLane, request serviceapi.LaneKeyRequestV1) (serviceapi.LaneV1, error) {
	if err := CheckShape(lane.Provider, request.Key); err != nil {
		m.audit("refused", lane.Name, "reason=shape")
		return serviceapi.LaneV1{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, settings, err := laneFile(lane)
	if err != nil {
		m.audit("not set", lane.Name, "reason=read")
		return serviceapi.LaneV1{}, fmt.Errorf("%w: %s", serviceapi.ErrLaneKeyUnavailable, err.Error())
	}
	if why := paidOtherwise(lane.Executor, settings); why != "" {
		m.audit("refused", lane.Name, "reason="+why)
		return serviceapi.LaneV1{}, serviceapi.ErrLaneNotKey
	}
	if err := expect(ctx, heldState(lane, settings)); err != nil {
		m.audit("refused", lane.Name, "reason=changed")
		return serviceapi.LaneV1{}, err
	}
	next := withSetting(data, lane.Setting, request.Key)
	if _, err := enginelane.Parse(next, lane.Executor); err != nil {
		m.audit("not set", lane.Name, "reason=compose")
		return serviceapi.LaneV1{}, fmt.Errorf("%w: the lane file would not be one a build reads", serviceapi.ErrLaneKeyUnavailable)
	}
	if err := writeKeyFile(lane.LaneFile, next); err != nil {
		m.audit("not set", lane.Name, "reason=write")
		return serviceapi.LaneV1{}, fmt.Errorf("%w: %s", serviceapi.ErrLaneKeyUnavailable, err.Error())
	}
	answer, listed := engineState(lane)
	if !listed || answer.State != serviceapi.LaneKeySet || answer.Fingerprint != Fingerprint(request.Key) {
		m.audit("not set", lane.Name, "reason=read-back")
		return serviceapi.LaneV1{}, fmt.Errorf("%w: the lane file does not read back with the key", serviceapi.ErrLaneKeyUnavailable)
	}
	m.audit("set", lane.Name, "fingerprint="+answer.Fingerprint)
	return answer, nil
}

// removeEngineKey removes the key setting's line from an engine lane's file, keeping every other line, unless nothing
// would be left: a lane file with no setting stops every profile from loading at the next start, so it keeps its key.
func (m *Manager) removeEngineKey(ctx context.Context, lane EngineLane) (serviceapi.LaneV1, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, settings, err := laneFile(lane)
	if err != nil {
		m.audit("not removed", lane.Name, "reason=read")
		return serviceapi.LaneV1{}, fmt.Errorf("%w: %s", serviceapi.ErrLaneKeyUnavailable, err.Error())
	}
	if why := paidOtherwise(lane.Executor, settings); why != "" {
		m.audit("refused", lane.Name, "reason="+why)
		return serviceapi.LaneV1{}, serviceapi.ErrLaneNotKey
	}
	before := heldState(lane, settings)
	if err := expect(ctx, before); err != nil {
		m.audit("refused", lane.Name, "reason=changed")
		return serviceapi.LaneV1{}, err
	}
	if before.State == serviceapi.LaneKeyMissing {
		m.audit("removed", lane.Name, "was=missing")
		return before, nil
	}
	next := withoutSetting(data, lane.Setting)
	if _, err := enginelane.Parse(next, lane.Executor); err != nil {
		m.audit("kept", lane.Name, "reason=required")
		return serviceapi.LaneV1{}, serviceapi.ErrLaneKeyRequired
	}
	if err := writeKeyFile(lane.LaneFile, next); err != nil {
		m.audit("not removed", lane.Name, "reason=write")
		return serviceapi.LaneV1{}, fmt.Errorf("%w: %s", serviceapi.ErrLaneKeyUnavailable, err.Error())
	}
	answer, listed := engineState(lane)
	if !listed || answer.State != serviceapi.LaneKeyMissing {
		return serviceapi.LaneV1{}, fmt.Errorf("%w: the lane file still holds a key", serviceapi.ErrLaneKeyUnavailable)
	}
	m.audit("removed", lane.Name, "fingerprint="+before.Fingerprint)
	return answer, nil
}

// settingName answers the setting a lane file's line gives, or "" for a blank line, a comment or no setting.
func settingName(line string) string {
	name, _, found := strings.Cut(strings.TrimRight(line, "\r"), "=")
	if !found {
		return ""
	}
	return name
}

// withSetting answers a lane file's data with setting's line given value (its line ending kept), or the line added at
// the end; every other byte is kept.
func withSetting(data []byte, setting, value string) []byte {
	lines := strings.Split(string(data), "\n")
	for index, line := range lines {
		if settingName(line) == setting {
			ending := ""
			if strings.HasSuffix(line, "\r") {
				ending = "\r"
			}
			lines[index] = setting + "=" + value + ending
			return []byte(strings.Join(lines, "\n"))
		}
	}
	text := string(data)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text + setting + "=" + value + "\n")
}

// withoutSetting answers a lane file's data without setting's line; every other byte is kept.
func withoutSetting(data []byte, setting string) []byte {
	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if settingName(line) != setting {
			kept = append(kept, line)
		}
	}
	return []byte(strings.Join(kept, "\n"))
}

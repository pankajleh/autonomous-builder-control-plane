package accountlane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// Asking a lane one question (Repo C design note AI_IN_EVERY_APP §10, AE3; docs/plans/account-lane-ask.md): an
// administrator's own app, while they try it themselves, answers through their own signed-in Claude account. ABCP runs
// `claude -p` once for each question:
//   - with every built-in tool off (--tools ""), no MCP server, no settings file, no skill or slash command, no saved
//     session, one turn, and the configured model;
//   - in a fresh empty folder (mode 0700, removed afterwards), in its own process group, killed when its time is up;
//   - with only the lane's settings (its home and its token), a minimal PATH and the lane's home as HOME, and two
//     switches that stop Claude Code's own updates and background traffic: none of the controller's keys.
//
// The conversation goes in on standard input as one stream-json user message, and the answer is the result event on
// standard output. A Codex lane does not answer: Codex's read-only sandbox still reads the whole disk.

const (
	// DefaultAskModel is the model a lane answers with unless abcp serve names another.
	DefaultAskModel = "sonnet"
	// DefaultAskTimeout is a question's time, its wait for the lane included: the relay's 30 s, less the start-up.
	DefaultAskTimeout = 25 * time.Second
	// DefaultAskBusyWait is how long a question waits for a lane answering another.
	DefaultAskBusyWait = 3 * time.Second
	// maxAskOutput bounds what Claude Code may print for one question.
	maxAskOutput = 1 << 20
)

// AskConfig sets how a Claude lane answers a question.
type AskConfig struct {
	// Claude is Claude Code's executable, an absolute path; empty finds `claude` on the controller's PATH at each
	// question, as a Codex device sign-in finds `codex`.
	Claude string
	// Model is the model asked; empty is DefaultAskModel.
	Model string
	// Timeout bounds a question, its wait for the lane included; zero is DefaultAskTimeout.
	Timeout time.Duration
	// BusyWait bounds the wait for a lane another question holds; zero is DefaultAskBusyWait.
	BusyWait time.Duration
}

var askModelPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:\[\]-]{0,99}$`)

// Option changes how the account lanes work.
type Option func(*Manager) error

// WithAsk sets how a Claude lane answers a question.
func WithAsk(config AskConfig) Option {
	return func(m *Manager) error {
		if config.Model == "" {
			config.Model = DefaultAskModel
		}
		if config.Timeout == 0 {
			config.Timeout = DefaultAskTimeout
		}
		if config.BusyWait == 0 {
			config.BusyWait = DefaultAskBusyWait
		}
		if !askModelPattern.MatchString(config.Model) || config.Timeout < 0 || config.Timeout > serviceapi.MaxAccountLaneAskTimeout ||
			config.BusyWait < 0 || config.BusyWait >= config.Timeout ||
			(config.Claude != "" && (!filepath.IsAbs(config.Claude) || filepath.Clean(config.Claude) != config.Claude)) {
			return errors.New("account lane questions need a model name, a timeout of at most a minute, a shorter busy wait and an absolute executable")
		}
		m.ask = config
		return nil
	}
}

// Ask asks a signed-in Claude lane one question. One question runs at a time per lane: another waits at most the busy
// wait, then is ErrAccountLaneBusy. A question's time, its wait included, is the timeout; past it Claude Code's process
// group is killed and the question is ErrAccountLaneTimeout. Nothing of the question, the answer or the token is
// logged or kept.
func (m *Manager) Ask(ctx context.Context, name string, ask serviceapi.AccountLaneAskV1) (serviceapi.AccountLaneAnswerV1, error) {
	none := serviceapi.AccountLaneAnswerV1{}
	lane, err := m.lane(name)
	if err != nil {
		return none, err
	}
	if lane.Executor != "claude" {
		return none, serviceapi.ErrAccountLaneAskUnsupported
	}
	if len(ask.Turns) == 0 || ask.Turns[len(ask.Turns)-1].Role != "user" {
		return none, serviceapi.ErrAccountLaneAskInvalid
	}
	if _, err := signedInSettings(lane); err != nil {
		return none, err
	}
	asking, cancel := context.WithTimeout(ctx, m.ask.Timeout)
	defer cancel()
	defer context.AfterFunc(m.ctx, cancel)()
	busy := time.NewTimer(m.ask.BusyWait)
	defer busy.Stop()
	select {
	case m.asking[lane.Lane] <- struct{}{}:
		defer func() { <-m.asking[lane.Lane] }()
	case <-busy.C:
		return none, serviceapi.ErrAccountLaneBusy
	case <-asking.Done():
		return none, askEnded(ctx, asking)
	}
	// Read again once the lane is this question's: it may have been signed out meanwhile.
	settings, err := signedInSettings(lane)
	if err != nil {
		return none, err
	}
	answer, err := m.runClaude(asking, lane, settings, ask)
	if asking.Err() != nil {
		return none, askEnded(ctx, asking)
	}
	if err != nil {
		return none, serviceapi.ErrAccountLaneUnavailable
	}
	return answer, nil
}

// askEnded says why a question's context ended: its own time, or the caller or the controller going away.
func askEnded(caller, asking context.Context) error {
	if errors.Is(asking.Err(), context.DeadlineExceeded) && caller.Err() == nil {
		return serviceapi.ErrAccountLaneTimeout
	}
	return serviceapi.ErrAccountLaneUnavailable
}

// signedInSettings reads a Claude lane's file: its home and its token, or ErrAccountLaneSignedOut without one.
func signedInSettings(lane LaneV1) (map[string]string, error) {
	settings, err := settingsOf(lane)
	if err != nil {
		return nil, serviceapi.ErrAccountLaneUnavailable
	}
	if settings[tokenSetting] == "" {
		return nil, serviceapi.ErrAccountLaneSignedOut
	}
	return settings, nil
}

// claudeExecutable answers the configured executable, or `claude` on the controller's PATH.
func (m *Manager) claudeExecutable() (string, error) {
	if m.ask.Claude != "" {
		return m.ask.Claude, nil
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		return "", errors.New("claude is not on the controller's PATH")
	}
	return filepath.Abs(path)
}

// askArguments is Claude Code's command line for one question. --tools takes a list, so only options follow it.
func askArguments(system, model string) []string {
	return []string{"-p",
		"--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--system-prompt", system,
		"--tools", "",
		"--strict-mcp-config",
		"--setting-sources", "",
		"--disable-slash-commands",
		"--no-session-persistence",
		"--max-turns", "1",
		"--model", model,
	}
}

// askPath follows Claude Code's own folder in its PATH, which holds the node an npm install of it runs with.
const askPath = "/usr/local/bin:/usr/bin:/bin"

// askSwitches stop Claude Code's own updates, telemetry and other background traffic; they carry nothing.
var askSwitches = []string{"DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"}

// askEnvironment is all Claude Code sees: a minimal PATH, the lane's home as HOME, the lane's settings and the
// switches. Nothing comes from the controller's own environment.
func askEnvironment(claude string, lane LaneV1, settings map[string]string) []string {
	environment := []string{"PATH=" + filepath.Dir(claude) + ":" + askPath, "HOME=" + lane.Home}
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		environment = append(environment, key+"="+settings[key])
	}
	return append(environment, askSwitches...)
}

type askTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type askImageBlock struct {
	Type   string         `json:"type"`
	Source askImageSource `json:"source"`
}

type askImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// askInput is the one user message Claude Code reads on its standard input (stream-json, one line):
//
//	{"type":"user","message":{"role":"user","content":[…]}}
//
// The earlier turns become a first text block, "Earlier in this conversation:" and a line per turn, "User: …" or
// "Assistant: …", a picture named "[a picture]" and not sent. The last user turn's texts and pictures follow in their
// order, a picture as a base64 image block.
func askInput(ask serviceapi.AccountLaneAskV1) ([]byte, error) {
	if len(ask.Turns) == 0 {
		return nil, errors.New("a question has a turn")
	}
	content := []any{}
	if len(ask.Turns) > 1 {
		content = append(content, askTextBlock{Type: "text", Text: earlierText(ask.Turns[:len(ask.Turns)-1])})
	}
	for _, part := range ask.Turns[len(ask.Turns)-1].Parts {
		if part.Picture() {
			content = append(content, askImageBlock{Type: "image", Source: askImageSource{Type: "base64", MediaType: part.MediaType, Data: part.Data}})
		} else {
			content = append(content, askTextBlock{Type: "text", Text: part.Text})
		}
	}
	line := struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []any  `json:"content"`
		} `json:"message"`
	}{Type: "user"}
	line.Message.Role, line.Message.Content = "user", content
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(line); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// earlierText is the earlier turns as one text: a line per turn, its texts and its pictures' names joined by spaces.
func earlierText(turns []serviceapi.AccountLaneTurnV1) string {
	var text strings.Builder
	text.WriteString("Earlier in this conversation:")
	for _, turn := range turns {
		if turn.Role == "assistant" {
			text.WriteString("\nAssistant: ")
		} else {
			text.WriteString("\nUser: ")
		}
		for index, part := range turn.Parts {
			if index > 0 {
				text.WriteString(" ")
			}
			if part.Picture() {
				text.WriteString("[a picture]")
			} else {
				text.WriteString(part.Text)
			}
		}
	}
	return text.String()
}

// askResult is the part of Claude Code's result event an answer keeps.
type askResult struct {
	Type    string  `json:"type"`
	Subtype string  `json:"subtype"`
	IsError bool    `json:"is_error"`
	Result  *string `json:"result"`
	Usage   *struct {
		InputTokens              *int64 `json:"input_tokens"`
		CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
		OutputTokens             *int64 `json:"output_tokens"`
	} `json:"usage"`
}

// parseAnswer reads Claude Code's stream-json output for its last result event. A result that is an error (is_error,
// or a subtype other than success), or no result at all, is no answer. The input tokens count the cached ones too, as
// the API's input_tokens leaves them out; a count Claude Code does not give is null.
func parseAnswer(output []byte) (serviceapi.AccountLaneAnswerV1, error) {
	var found *askResult
	for _, line := range bytes.Split(output, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var event askResult
		if json.Unmarshal(line, &event) == nil && event.Type == "result" {
			found = &event
		}
	}
	if found == nil {
		return serviceapi.AccountLaneAnswerV1{}, errors.New("claude gave no result")
	}
	if found.IsError || (found.Subtype != "" && found.Subtype != "success") || found.Result == nil {
		return serviceapi.AccountLaneAnswerV1{}, errors.New("claude's result is an error")
	}
	answer := serviceapi.AccountLaneAnswerV1{Text: *found.Result}
	if usage := found.Usage; usage != nil {
		if usage.InputTokens != nil {
			total := *usage.InputTokens
			for _, cached := range []*int64{usage.CacheCreationInputTokens, usage.CacheReadInputTokens} {
				if cached != nil {
					total += *cached
				}
			}
			answer.InputTokens = &total
		}
		answer.OutputTokens = usage.OutputTokens
	}
	return answer, nil
}

// boundedOutput keeps at most maxAskOutput bytes; past that it refuses, so Claude Code's output pipe closes.
type boundedOutput struct {
	buffer bytes.Buffer
	over   bool
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	if b.buffer.Len()+len(data) > maxAskOutput {
		b.over = true
		return 0, errors.New("claude printed too much")
	}
	return b.buffer.Write(data)
}

package stackjob

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/enginelane"
)

// EngineFileV1 is the protected file that names the change jobs' engine: Claude Code's binary, its engine lane
// (internal/enginelane, executor claude) and, optionally, its model.
type EngineFileV1 struct {
	SchemaVersion int    `json:"schema_version"`
	Binary        string `json:"binary"`
	LaneName      string `json:"lane_name"`
	LaneFile      string `json:"lane_file"`
	Model         string `json:"model,omitempty"`
}

var modelPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:\[\]-]{0,99}$`)

// LoadEngine reads the engine file (a private regular file, never a link) and checks its lane.
func LoadEngine(path string) (*ClaudeEngine, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 4<<10 {
		return nil, errors.New("change engine file must be a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f EngineFileV1
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if d.Decode(&f) != nil || f.SchemaVersion != 1 || !filepath.IsAbs(f.Binary) || filepath.Clean(f.Binary) != f.Binary ||
		(f.Model != "" && !modelPattern.MatchString(f.Model)) {
		return nil, errors.New("change engine file does not check")
	}
	if b, err := os.Stat(f.Binary); err != nil || !b.Mode().IsRegular() || b.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("change engine binary is not an executable file")
	}
	if _, err := enginelane.Load(f.LaneName, f.LaneFile, "claude"); err != nil {
		return nil, err
	}
	return &ClaudeEngine{Binary: f.Binary, LaneName: f.LaneName, LaneFile: f.LaneFile, Model: f.Model}, nil
}

// ClaudeEngine runs Claude Code headless in restricted mode (v2.1.248 or later): no tool that runs commands or code, no
// web, the file tools confined to the work folder, no user or project settings (so no hooks, plugins or MCP servers from
// the repository), only ABCP's MCP file; edits and the job's run tool allowed, anything else that would ask refused. An
// older Claude Code refuses the flag and the job ends ENGINE_UNAVAILABLE.
type ClaudeEngine struct {
	Binary, LaneName, LaneFile, Model string
}

// The built-in tools the agent has; the edits and the run tool it may use without asking; and those it never has,
// whatever a version of the tool adds.
var (
	agentTools   = []string{"Read", "Glob", "Grep", "Edit", "Write", "TodoWrite"}
	allowedRules = []string{"Edit(./**)", "mcp__abcp__run"}
	deniedTools  = []string{"Bash", "BashOutput", "KillShell", "WebFetch", "WebSearch", "Task", "Agent", "NotebookEdit", "Skill"}
)

// argv is the command line for one session.
func (e *ClaudeEngine) argv(run EngineRun) []string {
	args := []string{e.Binary, "--restricted", "-p", run.Prompt, "--output-format", "json", "--max-turns", strconv.Itoa(run.MaxTurns),
		"--tools", strings.Join(agentTools, ","), "--allowedTools", strings.Join(allowedRules, ","), "--disallowedTools", strings.Join(deniedTools, ","),
		"--mcp-config", run.MCPConfig, "--strict-mcp-config", "--permission-mode", "dontAsk"}
	if e.Model != "" {
		args = append(args, "--model", e.Model)
	}
	return args
}

// environment is the session's: the essentials, the controller's proxy when it has one, and the lane's settings; never
// the rest of the controller's environment.
func environment(home string, lane enginelane.Lane) []string {
	base := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "TMPDIR=" + filepath.Join(home, "tmp"), "LANG=C.UTF-8",
		"DISABLE_AUTOUPDATER=1", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"}
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy", "SSL_CERT_FILE"} {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			base = append(base, key+"="+v)
		}
	}
	return lane.Environment(base)
}

// SecretValues answers the lane's setting values long enough to be secrets, so a change or a summary that carries one is
// never handed back.
func (e *ClaudeEngine) SecretValues() []string {
	lane, err := enginelane.Load(e.LaneName, e.LaneFile, "claude")
	if err != nil {
		return nil
	}
	values := []string{}
	for _, v := range lane.Settings {
		if len(v) >= 12 {
			values = append(values, v)
		}
	}
	return values
}

// claudeResult is the part of Claude Code's JSON answer a job keeps.
type claudeResult struct {
	Type         string  `json:"type"`
	Subtype      string  `json:"subtype"`
	IsError      bool    `json:"is_error"`
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		OutputTokens int64 `json:"outputTokens"`
	} `json:"modelUsage"`
}

// Run runs one session in its own process group, ended with the context.
func (e *ClaudeEngine) Run(ctx context.Context, run EngineRun) (EngineResult, error) {
	lane, err := enginelane.Load(e.LaneName, e.LaneFile, "claude")
	if err != nil {
		return EngineResult{}, err
	}
	if os.MkdirAll(filepath.Join(run.Home, "tmp"), 0o700) != nil {
		return EngineResult{}, errors.New("the engine's home cannot be made")
	}
	args := e.argv(run)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = run.Work
	cmd.Env = environment(run.Home, lane)
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 10 * time.Second
	out := &limited{n: 4 << 20}
	cmd.Stdout, cmd.Stderr = out, io.Discard
	runErr := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if ctx.Err() != nil {
		return EngineResult{}, ctx.Err()
	}
	return parseClaude(out.buf.Bytes(), runErr)
}

// parseClaude reads Claude Code's answer: a session that ran out of turns still answers what it did.
func parseClaude(data []byte, runErr error) (EngineResult, error) {
	var r claudeResult
	if json.Unmarshal(data, &r) != nil || r.Type != "result" {
		if runErr != nil {
			return EngineResult{}, runErr
		}
		return EngineResult{}, errors.New("the engine's answer cannot be read")
	}
	result := EngineResult{Summary: r.Result, OutputTokens: r.Usage.OutputTokens, CostUSD: r.TotalCostUSD,
		InputTokens:     r.Usage.InputTokens + r.Usage.CacheReadInputTokens + r.Usage.CacheCreationInputTokens,
		CacheReadTokens: r.Usage.CacheReadInputTokens, CacheWriteTokens: r.Usage.CacheCreationInputTokens}
	// The model that wrote the most; the names are the engine's, checked before they are kept.
	var most int64 = -1
	for name, used := range r.ModelUsage {
		if modelPattern.MatchString(name) && (used.OutputTokens > most || (used.OutputTokens == most && name < result.Model)) {
			result.Model, most = name, used.OutputTokens
		}
	}
	if r.IsError && r.Subtype != "error_max_turns" {
		return result, errors.New("the engine's session failed")
	}
	return result, nil
}

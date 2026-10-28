package stackjob

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// The run tool (docs/plans/change-jobs.md): the agent's only way to run anything. `abcp stack-tool` is an MCP server
// over stdio that the agent's command-line tool starts; it hands each command to the job's broker, a Unix socket served
// by `abcp serve` in a folder only this controller's user can reach, and the broker runs it with Exec in the job's stack
// sandbox. The sandbox never sees the socket.

const (
	brokerCommands = 200
	commandTime    = 5 * time.Minute
	commandTimeMax = 10 * time.Minute
	commandChars   = 4000
	commandOutput  = 32 << 10
	commandLines   = 400
	brokerLine     = 16 << 10
	// answerLine bounds the broker's answer: the output escaped in JSON grows at most sixfold (\u0001 for a control byte).
	answerLine       = commandOutput*6 + 4<<10
	toolMessageBytes = 1 << 20
)

// runRequest is one command the agent asks for: a shell line, the folder under /work it runs in, and its time.
type runRequest struct {
	Command        string `json:"command"`
	Dir            string `json:"dir,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// runAnswer is its exit code and its last output, masked, or why it did not run.
type runAnswer struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	TimedOut bool   `json:"timed_out,omitempty"`
	Error    string `json:"error,omitempty"`
}

// broker serves one job's socket and runs its commands one at a time, in a container it reopens before the
// container's lifetime ends (the work and the cache are host folders, so nothing is lost).
type broker struct {
	ln      net.Listener
	ctx     context.Context
	cancel  context.CancelFunc
	open    func(ctx context.Context, n int) (Session, []string, error)
	life    time.Duration
	wg      sync.WaitGroup
	mu      sync.Mutex
	session Session
	env     []string
	secrets []string
	opened  time.Time
	opens   int
	count   int
	closed  bool
}

// openBroker listens on the job's socket for the agent's part.
func (m *ChangeManager) openBroker(ctx context.Context, r *changeRecord, p Part, n int, work, cache string, registries []serviceapi.RegistryV1) (*broker, error) {
	profile, ok := m.profiles.For(p.Stack, p.Version)
	if !ok || !m.probe.proved(ctx, profile) {
		return nil, errSource
	}
	open := func(ctx context.Context, opens int) (Session, []string, error) {
		spec := profile.Spec()
		spec.Hosts = registryHosts(registries)
		session, err := m.sandbox.Open(ctx, partID(r.Job.ID, n, "agent "+strconv.Itoa(opens)), spec, work, cache)
		if err != nil {
			return nil, nil, err
		}
		if javaSettings(p, cache, session.Proxy(), registries) != nil {
			_ = session.Close()
			return nil, nil, errSource
		}
		return session, Env(p, profile, session.Proxy()), nil
	}
	return listenBroker(ctx, m.socket(r.Job.ID), profile.Spec().Lifetime, open, registrySecrets(registries))
}

// listenBroker serves the agent's commands on socket; secrets are the job's registry tokens, masked in every answer so
// the model never reads one.
func listenBroker(ctx context.Context, socket string, life time.Duration, open func(context.Context, int) (Session, []string, error), secrets []string) (*broker, error) {
	_ = os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if os.Chmod(socket, 0o600) != nil {
		_ = ln.Close()
		return nil, errSource
	}
	b := &broker{ln: ln, open: open, life: life, secrets: secrets}
	b.ctx, b.cancel = context.WithCancel(ctx)
	b.wg.Add(1)
	go b.accept()
	return b, nil
}

func (b *broker) accept() {
	defer b.wg.Done()
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return
		}
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			defer conn.Close()
			go func() { <-b.ctx.Done(); _ = conn.Close() }()
			reader := bufio.NewReaderSize(conn, brokerLine)
			for {
				line, err := readLine(reader, brokerLine)
				if err != nil {
					return
				}
				var req runRequest
				answer := runAnswer{ExitCode: -1, Error: "the request is not a command"}
				if json.Unmarshal(line, &req) == nil {
					answer = b.run(req)
				}
				data, _ := json.Marshal(answer)
				if _, err := conn.Write(append(data, '\n')); err != nil {
					return
				}
			}
		}()
	}
}

// readLine reads one line of at most limit bytes.
func readLine(r *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		part, isPrefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		line = append(line, part...)
		if len(line) > limit {
			return nil, errors.New("line too long")
		}
		if !isPrefix {
			return line, nil
		}
	}
}

// workPath answers the container folder for dir, a relative folder under /work, or false for anything else.
func workPath(dir string) (string, bool) {
	if dir == "" || dir == "." {
		return "/work", true
	}
	if len(dir) > 512 || strings.HasPrefix(dir, "/") || strings.ContainsRune(dir, 0) || strings.Contains(dir, "\\") {
		return "", false
	}
	clean := path.Clean(dir)
	if clean == ".." || strings.HasPrefix(clean, "../") || clean == ".abcp" || strings.HasPrefix(clean, ".abcp/") {
		return "", false
	}
	return "/work/" + clean, true
}

func (b *broker) run(req runRequest) runAnswer {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.closed || b.ctx.Err() != nil:
		return runAnswer{ExitCode: -1, Error: "the job has ended"}
	case b.count >= brokerCommands:
		return runAnswer{ExitCode: -1, Error: "the job has run as many commands as it may"}
	case strings.TrimSpace(req.Command) == "" || utf8.RuneCountInString(req.Command) > commandChars || strings.ContainsRune(req.Command, 0) ||
		!utf8.ValidString(req.Command):
		return runAnswer{ExitCode: -1, Error: "the command is empty or too long"}
	case req.TimeoutSeconds < 0 || req.TimeoutSeconds > int(commandTimeMax/time.Second):
		return runAnswer{ExitCode: -1, Error: "timeout_seconds is from 1 to 600"}
	}
	dir, ok := workPath(req.Dir)
	if !ok {
		return runAnswer{ExitCode: -1, Error: "dir must be a folder inside the repository"}
	}
	limit := commandTime
	if req.TimeoutSeconds > 0 {
		limit = time.Duration(req.TimeoutSeconds) * time.Second
	}
	// A container is reopened when it could end before this command does.
	if b.session != nil && time.Since(b.opened)+limit+time.Minute > b.life {
		_ = b.session.Close()
		b.session = nil
	}
	if b.session == nil {
		session, env, err := b.open(b.ctx, b.opens)
		if err != nil {
			return runAnswer{ExitCode: -1, Error: "the sandbox could not be started"}
		}
		b.session, b.env, b.opened = session, env, time.Now()
		b.opens++
	}
	b.count++
	out := newTail(commandOutput)
	ctx, cancel := context.WithTimeout(b.ctx, limit)
	defer cancel()
	code, err := b.session.Exec(ctx, dir, b.env, []string{"/bin/sh", "-c", req.Command}, out)
	answer := runAnswer{ExitCode: code, Output: maskRegistries(out.Lines(commandLines), b.secrets)}
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		answer.ExitCode, answer.TimedOut = -1, true
		// A command that outlived its time may still be running: the next one gets a fresh container.
		_ = b.session.Close()
		b.session = nil
	case err != nil:
		answer.ExitCode, answer.Error = -1, "the sandbox did not run the command"
		_ = b.session.Close()
		b.session = nil
	}
	return answer
}

func (b *broker) commands() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

// close stops listening, ends the running command and the container, and waits for the connections; again is a no-op.
func (b *broker) close() {
	b.cancel()
	_ = b.ln.Close()
	b.wg.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed && b.session != nil {
		_ = b.session.Close()
	}
	b.session, b.closed = nil, true
}

// writeMCPConfig writes ABCP's own MCP file for the agent: the run tool and nothing else.
func writeMCPConfig(file, tool, socket string) error {
	data, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"abcp": map[string]any{
		"type": "stdio", "command": tool, "args": []string{"stack-tool", "--socket", socket}, "env": map[string]string{}}}})
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0o600)
}

// The MCP server.

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const runToolDescription = "Run one shell command in the repository's sandbox: a container with the project's own toolchain, " +
	"the repository at /work, and no network except the package registries. Use it for everything you need to run: installing, " +
	"building, the tests, scripts. Nothing runs on any other machine. Answers the exit code and the last lines printed."

var runToolSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"command":         map[string]any{"type": "string", "description": "the shell command, run by /bin/sh -c"},
		"dir":             map[string]any{"type": "string", "description": "a folder inside the repository to run it in; the top by default"},
		"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 600, "description": "at most 600; 300 by default"},
	},
	"required":             []string{"command"},
	"additionalProperties": false,
}

// ServeStackTool is `abcp stack-tool`: it answers MCP over in and out, offering the run tool, until in ends.
func ServeStackTool(in io.Reader, out io.Writer, socket string) error {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return fmt.Errorf("the job's broker cannot be reached")
	}
	defer conn.Close()
	brokerIn := bufio.NewReaderSize(conn, 64<<10)
	reader := bufio.NewReaderSize(in, 64<<10)
	writer := bufio.NewWriter(out)
	reply := func(r rpcReply) error {
		r.JSONRPC = "2.0"
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			return err
		}
		return writer.Flush()
	}
	for {
		line, err := readLine(reader, toolMessageBytes)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var msg rpcMessage
		if json.Unmarshal(line, &msg) != nil {
			if err := reply(rpcReply{ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}}); err != nil {
				return err
			}
			continue
		}
		if len(msg.ID) == 0 || string(msg.ID) == "null" {
			continue // a notification: nothing to answer
		}
		var result any
		var rerr *rpcError
		switch msg.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			version := params.ProtocolVersion
			if version == "" || len(version) > 32 {
				version = "2025-06-18"
			}
			result = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "abcp", "version": "1"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "run", "description": runToolDescription, "inputSchema": runToolSchema}}}
		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			var req runRequest
			if json.Unmarshal(msg.Params, &params) != nil || params.Name != "run" || decodeStrict(params.Arguments, &req) != nil {
				rerr = &rpcError{Code: -32602, Message: "the run tool takes command, and optionally dir and timeout_seconds"}
				break
			}
			answer, err := askBroker(conn, brokerIn, req)
			if err != nil {
				return fmt.Errorf("the job's broker went away")
			}
			result = toolResult(answer)
		default:
			rerr = &rpcError{Code: -32601, Message: "method not found"}
		}
		if err := reply(rpcReply{ID: msg.ID, Result: result, Error: rerr}); err != nil {
			return err
		}
	}
}

func decodeStrict(data []byte, v any) error {
	if len(data) == 0 {
		return errors.New("no arguments")
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

func askBroker(conn net.Conn, in *bufio.Reader, req runRequest) (runAnswer, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return runAnswer{}, err
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return runAnswer{}, err
	}
	line, err := readLine(in, answerLine)
	if err != nil {
		return runAnswer{}, err
	}
	var answer runAnswer
	if err := json.Unmarshal(line, &answer); err != nil {
		return runAnswer{}, err
	}
	return answer, nil
}

func toolResult(a runAnswer) map[string]any {
	var text string
	switch {
	case a.Error != "":
		text = "not run: " + a.Error
	case a.TimedOut:
		text = "timed out\n" + a.Output
	default:
		text = "exit code " + strconv.Itoa(a.ExitCode) + "\n" + a.Output
	}
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": a.Error != "" || a.TimedOut || a.ExitCode != 0}
}

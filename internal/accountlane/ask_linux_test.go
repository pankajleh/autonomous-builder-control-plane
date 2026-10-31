//go:build linux

package accountlane

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// fakeClaude is a `claude` that records its arguments, its environment, its working folder and its standard input in
// records, and prints records/output. A records/hold file keeps it waiting until removed; a records/sleep file makes
// it sleep 30 s in a child; records/exit is its exit status.
type fakeClaude struct {
	bin, path, records string
}

func newFakeClaude(t *testing.T, output string) fakeClaude {
	t.Helper()
	root := t.TempDir()
	f := fakeClaude{bin: filepath.Join(root, "bin"), records: filepath.Join(root, "records")}
	f.path = filepath.Join(f.bin, "claude")
	for _, dir := range []string{f.bin, f.records} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\n" +
		"r='" + f.records + "'\n" +
		"echo run >> \"$r/runs\"\n" +
		"for a in \"$@\"; do printf '%s\\0' \"$a\"; done > \"$r/args\"\n" +
		"cat /proc/$$/environ > \"$r/env\"\n" +
		"pwd -P > \"$r/cwd\"\n" +
		"ls -A > \"$r/listing\"\n" +
		"stat -c %a . > \"$r/mode\"\n" +
		"cat > \"$r/stdin\"\n" +
		": > \"$r/started\"\n" +
		"while [ -f \"$r/hold\" ]; do sleep 0.05; done\n" +
		"if [ -f \"$r/sleep\" ]; then sleep 30 & echo $! > \"$r/sleep.pid\"; wait; fi\n" +
		"cat \"$r/output\"\n" +
		"exit $(cat \"$r/exit\" 2>/dev/null || echo 0)\n"
	if err := os.WriteFile(f.path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	f.write(t, "output", output)
	return f
}

func (f fakeClaude) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.records, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f fakeClaude) read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.records, name))
	if err != nil {
		t.Fatalf("the fake recorded no %s: %v", name, err)
	}
	return string(data)
}

func (f fakeClaude) ran() bool {
	_, err := os.Stat(filepath.Join(f.records, "runs"))
	return err == nil
}

// waitFor waits for the fake to have read its question.
func (f fakeClaude) waitFor(t *testing.T, name string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(f.records, name)); err == nil {
			return
		}
	}
	t.Fatalf("the fake never wrote %s", name)
}

const answered = `{"type":"system","subtype":"init","cwd":"/x","session_id":"s","tools":[],"mcp_servers":[],"model":"claude-sonnet"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"About 700, with the rice."}]},"session_id":"s"}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}
{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"About 700, with the rice.","usage":{"input_tokens":1210,"cache_creation_input_tokens":20,"cache_read_input_tokens":5,"output_tokens":42}}
`

// signedIn gives the fixture's Claude lane its token.
func (f fixture) signedIn(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(f.claude.LaneFile, []byte("CLAUDE_CONFIG_DIR="+f.claude.Home+"\nCLAUDE_CODE_OAUTH_TOKEN="+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) asker(t *testing.T, config AskConfig) *Manager {
	t.Helper()
	m, err := New(FileV1{SchemaVersion: 1, Lanes: []LaneV1{f.codex, f.claude}}, (&fakeLogin{}).start, func() time.Time { return now }, WithAsk(config))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func question(text string) serviceapi.AccountLaneAskV1 {
	return serviceapi.AccountLaneAskV1{Turns: []serviceapi.AccountLaneTurnV1{{Role: "user", Parts: []serviceapi.AccountLanePartV1{{Text: text}}}}}
}

func TestAClaudeLaneAnswersOneQuestionWithEveryToolOffInAnEmptyFolder(t *testing.T) {
	f := newFixture(t)
	f.signedIn(t)
	claude := newFakeClaude(t, answered)
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	// The controller's own keys and settings, none of which Claude Code may see.
	t.Setenv("PATH", claude.bin+":/usr/bin:/bin")
	t.Setenv("ANTHROPIC_API_KEY", "the-controllers-own-anthropic-key")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "the-controllers-own-claude-token")
	t.Setenv("OPENAI_API_KEY", "sk-the-controllers-openai-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "the-controllers-aws-secret")
	t.Setenv("HTTPS_PROXY", "http://user:secret@proxy.invalid:3128")
	t.Setenv("ABCP_TOKEN", "the-controllers-bearer")
	m := f.asker(t, AskConfig{})

	jpeg := base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xff a plate of rice and dal"))
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n a second plate"))
	ask := serviceapi.AccountLaneAskV1{System: "-You are a calorie lens.", MaxTokens: 512, Turns: []serviceapi.AccountLaneTurnV1{
		{Role: "user", Parts: []serviceapi.AccountLanePartV1{{MediaType: "image/jpeg", Data: jpeg}, {Text: "What is on this plate?"}}},
		{Role: "assistant", Parts: []serviceapi.AccountLanePartV1{{Text: "A thali: rice, dal and two sabzis."}}},
		{Role: "user", Parts: []serviceapi.AccountLanePartV1{{Text: "How many calories?"}}},
		{Role: "assistant", Parts: []serviceapi.AccountLanePartV1{{Text: "About 650."}, {Text: "Less without the ghee."}}},
		{Role: "user", Parts: []serviceapi.AccountLanePartV1{{Text: "And this one?"}, {MediaType: "image/png", Data: png}, {Text: "It is my dinner."}}},
	}}
	answer, err := m.Ask(context.Background(), "claude-account", ask)
	if err != nil {
		t.Fatal(err)
	}
	if answer.Text != "About 700, with the rice." || answer.InputTokens == nil || *answer.InputTokens != 1235 || answer.OutputTokens == nil || *answer.OutputTokens != 42 {
		t.Fatalf("answer = %+v", answer)
	}

	args := strings.Split(strings.TrimSuffix(claude.read(t, "args"), "\x00"), "\x00")
	want := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--system-prompt", "-You are a calorie lens.", "--tools", "", "--strict-mcp-config", "--setting-sources", "",
		"--disable-slash-commands", "--no-session-persistence", "--max-turns", "1", "--model", "sonnet"}
	if strings.Join(args, "|") != strings.Join(want, "|") || len(args) != len(want) {
		t.Fatalf("arguments\n got %q\nwant %q", args, want)
	}

	environment := strings.Split(strings.TrimSuffix(claude.read(t, "env"), "\x00"), "\x00")
	sort.Strings(environment)
	wantEnvironment := []string{
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"CLAUDE_CODE_OAUTH_TOKEN=" + token,
		"CLAUDE_CONFIG_DIR=" + f.claude.Home,
		"DISABLE_AUTOUPDATER=1",
		"HOME=" + f.claude.Home,
		"PATH=" + claude.bin + ":/usr/local/bin:/usr/bin:/bin",
	}
	if strings.Join(environment, "\n") != strings.Join(wantEnvironment, "\n") {
		t.Fatalf("only the lane's settings, a minimal PATH and its HOME\n got %q\nwant %q", environment, wantEnvironment)
	}

	wantInput := `{"type":"user","message":{"role":"user","content":[` +
		`{"type":"text","text":"Earlier in this conversation:\nUser: [a picture] What is on this plate?\nAssistant: A thali: rice, dal and two sabzis.\nUser: How many calories?\nAssistant: About 650. Less without the ghee."},` +
		`{"type":"text","text":"And this one?"},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + png + `"}},` +
		`{"type":"text","text":"It is my dinner."}]}}` + "\n"
	if input := claude.read(t, "stdin"); input != wantInput {
		t.Fatalf("standard input\n got %s\nwant %s", input, wantInput)
	}

	work := strings.TrimSpace(claude.read(t, "cwd"))
	if claude.read(t, "listing") != "" || strings.TrimSpace(claude.read(t, "mode")) != "700" || strings.HasPrefix(work, f.root) {
		t.Fatalf("a fresh empty private folder: %q %q %q", work, claude.read(t, "listing"), claude.read(t, "mode"))
	}
	if _, err := os.Stat(work); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the folder is removed afterwards: %v", err)
	}
	if entries, _ := os.ReadDir(temporary); len(entries) != 0 {
		t.Fatalf("nothing temporary is left: %v", entries)
	}

	// A one-turn question is its texts alone; the configured model and executable are used.
	other := f.asker(t, AskConfig{Claude: claude.path, Model: "haiku", Timeout: 10 * time.Second, BusyWait: time.Second})
	t.Setenv("PATH", "/usr/bin:/bin")
	if _, err := other.Ask(context.Background(), "claude-account", question("Is it lunch?")); err != nil {
		t.Fatal(err)
	}
	if args := claude.read(t, "args"); !strings.HasSuffix(args, "--model\x00haiku\x00") || !strings.Contains(args, "--system-prompt\x00\x00--tools") {
		t.Fatalf("an absent system is an empty one, and the model is the configured one: %q", args)
	}
	if input := claude.read(t, "stdin"); input != `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Is it lunch?"}]}}`+"\n" {
		t.Fatalf("a one-turn question: %s", input)
	}
}

func TestAnAnswerIsTheResultEvent(t *testing.T) {
	answer, err := parseAnswer([]byte(answered))
	if err != nil || answer.Text != "About 700, with the rice." || *answer.InputTokens != 1235 || *answer.OutputTokens != 42 {
		t.Fatalf("answer = %+v %v", answer, err)
	}
	answer, err = parseAnswer([]byte(`{"type":"result","subtype":"success","is_error":false,"result":""}` + "\n"))
	if err != nil || answer.Text != "" || answer.InputTokens != nil || answer.OutputTokens != nil {
		t.Fatalf("no usage, no counts: %+v %v", answer, err)
	}
	answer, err = parseAnswer([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"ok","usage":{"output_tokens":3}}`))
	if err != nil || answer.InputTokens != nil || *answer.OutputTokens != 3 {
		t.Fatalf("only the counts given: %+v %v", answer, err)
	}
	for name, output := range map[string]string{
		"is_error":     `{"type":"result","subtype":"success","is_error":true,"result":"There's an issue with the selected model.","usage":{"input_tokens":0,"output_tokens":0}}`,
		"out of turns": `{"type":"result","subtype":"error_max_turns","is_error":false,"usage":{"input_tokens":9,"output_tokens":9}}`,
		"no result":    `{"type":"system","subtype":"init"}` + "\n" + `{"type":"assistant","message":{"role":"assistant","content":[]}}`,
		"no text":      `{"type":"result","subtype":"success","is_error":false}`,
		"nothing":      "",
		"not JSON":     "Error: Expected message role 'user', got 'undefined'\n",
	} {
		if _, err := parseAnswer([]byte(output)); err == nil {
			t.Errorf("%s is no answer", name)
		}
	}
}

func TestAQuestionIsRefusedBeforeClaudeCodeRuns(t *testing.T) {
	f := newFixture(t)
	claude := newFakeClaude(t, answered)
	m := f.asker(t, AskConfig{Claude: claude.path})
	for name, want := range map[string]struct {
		lane string
		ask  serviceapi.AccountLaneAskV1
		err  error
	}{
		"an unknown lane":         {"gemini-account", question("hi"), serviceapi.ErrAccountLaneUnknown},
		"a Codex lane":            {"codex-account", question("hi"), serviceapi.ErrAccountLaneAskUnsupported},
		"a signed-out Claude one": {"claude-account", question("hi"), serviceapi.ErrAccountLaneSignedOut},
	} {
		if _, err := m.Ask(context.Background(), want.lane, want.ask); !errors.Is(err, want.err) {
			t.Errorf("%s = %v", name, err)
		}
	}
	f.signedIn(t)
	if _, err := m.Ask(context.Background(), "claude-account", serviceapi.AccountLaneAskV1{}); !errors.Is(err, serviceapi.ErrAccountLaneAskInvalid) {
		t.Errorf("a question without turns = %v", err)
	}
	assistantLast := question("hi")
	assistantLast.Turns[0].Role = "assistant"
	if _, err := m.Ask(context.Background(), "claude-account", assistantLast); !errors.Is(err, serviceapi.ErrAccountLaneAskInvalid) {
		t.Errorf("a question the assistant ends = %v", err)
	}
	if claude.ran() {
		t.Fatal("no refused question runs Claude Code")
	}
}

func TestAFailingClaudeCodeIsUnavailable(t *testing.T) {
	f := newFixture(t)
	f.signedIn(t)
	claude := newFakeClaude(t, `{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login"}`+"\n")
	m := f.asker(t, AskConfig{Claude: claude.path})
	claude.write(t, "exit", "1")
	if _, err := m.Ask(context.Background(), "claude-account", question("hi")); !errors.Is(err, serviceapi.ErrAccountLaneUnavailable) {
		t.Fatalf("an error result = %v", err)
	}
	claude.write(t, "output", "")
	if _, err := m.Ask(context.Background(), "claude-account", question("hi")); !errors.Is(err, serviceapi.ErrAccountLaneUnavailable) {
		t.Fatalf("no result = %v", err)
	}
	claude.write(t, "exit", "0")
	claude.write(t, "output", strings.Repeat(`{"type":"assistant","message":{"content":"`+strings.Repeat("x", 1000)+`"}}`+"\n", 1100)+
		`{"type":"result","subtype":"success","is_error":false,"result":"late"}`+"\n")
	if _, err := m.Ask(context.Background(), "claude-account", question("hi")); !errors.Is(err, serviceapi.ErrAccountLaneUnavailable) {
		t.Fatalf("more than a megabyte of output = %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := f.asker(t, AskConfig{}).Ask(context.Background(), "claude-account", question("hi")); !errors.Is(err, serviceapi.ErrAccountLaneUnavailable) {
		t.Fatalf("no claude on the PATH = %v", err)
	}
}

func TestALaneAnswersOneQuestionAtATime(t *testing.T) {
	f := newFixture(t)
	f.signedIn(t)
	claude := newFakeClaude(t, answered)
	m := f.asker(t, AskConfig{Claude: claude.path, Timeout: 10 * time.Second, BusyWait: 300 * time.Millisecond})
	claude.write(t, "hold", "")
	first := make(chan error, 1)
	go func() {
		_, err := m.Ask(context.Background(), "claude-account", question("first"))
		first <- err
	}()
	claude.waitFor(t, "started")
	started := time.Now()
	if _, err := m.Ask(context.Background(), "claude-account", question("second")); !errors.Is(err, serviceapi.ErrAccountLaneBusy) {
		t.Fatalf("a second question while the first runs = %v", err)
	}
	if waited := time.Since(started); waited < 250*time.Millisecond || waited > 3*time.Second {
		t.Fatalf("it waited %v for the lane", waited)
	}
	os.Remove(filepath.Join(claude.records, "hold"))
	if err := <-first; err != nil {
		t.Fatalf("the first is answered: %v", err)
	}

	// A question that comes while another runs is answered once the lane is free within its wait.
	patient := f.asker(t, AskConfig{Claude: claude.path, Timeout: 10 * time.Second, BusyWait: 5 * time.Second})
	os.Remove(filepath.Join(claude.records, "started"))
	claude.write(t, "hold", "")
	go func() {
		_, err := patient.Ask(context.Background(), "claude-account", question("first"))
		first <- err
	}()
	claude.waitFor(t, "started")
	second := make(chan error, 1)
	go func() {
		_, err := patient.Ask(context.Background(), "claude-account", question("second"))
		second <- err
	}()
	time.Sleep(300 * time.Millisecond)
	os.Remove(filepath.Join(claude.records, "hold"))
	if err := <-first; err != nil {
		t.Fatalf("the first = %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("the second waits and is answered = %v", err)
	}
	if runs := strings.Count(claude.read(t, "runs"), "run"); runs != 3 {
		t.Fatalf("Claude Code ran %d times; the busy question never ran it", runs)
	}
}

func TestAQuestionPastItsTimeIsStoppedWithItsProcessGroup(t *testing.T) {
	f := newFixture(t)
	f.signedIn(t)
	claude := newFakeClaude(t, answered)
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	m := f.asker(t, AskConfig{Claude: claude.path, Timeout: 800 * time.Millisecond, BusyWait: 100 * time.Millisecond})
	claude.write(t, "sleep", "")
	started := time.Now()
	if _, err := m.Ask(context.Background(), "claude-account", question("hi")); !errors.Is(err, serviceapi.ErrAccountLaneTimeout) {
		t.Fatalf("past its time = %v", err)
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("it took %v", took)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(claude.read(t, "sleep.pid")))
	if err != nil {
		t.Fatal(err)
	}
	if !ended(pid, 5*time.Second) {
		t.Fatalf("Claude Code's child %d still runs", pid)
	}
	if entries, _ := os.ReadDir(temporary); len(entries) != 0 {
		t.Fatalf("its folder is removed: %v", entries)
	}

	// The lane is free again, and the controller stopping stops a question too.
	os.Remove(filepath.Join(claude.records, "started"))
	closing := f.asker(t, AskConfig{Claude: claude.path, Timeout: 20 * time.Second})
	done := make(chan error, 1)
	go func() {
		_, err := closing.Ask(context.Background(), "claude-account", question("hi"))
		done <- err
	}()
	claude.waitFor(t, "started")
	closing.Close()
	select {
	case err := <-done:
		if !errors.Is(err, serviceapi.ErrAccountLaneUnavailable) {
			t.Fatalf("stopped = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing stops a question")
	}
}

// ended reports whether a process is gone or a zombie within the wait.
func ended(pid int, wait time.Duration) bool {
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if err != nil {
			return true
		}
		if fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:])); len(fields) > 0 && fields[0] == "Z" {
			return true
		}
	}
	return false
}

func TestHowALaneAnswersIsChecked(t *testing.T) {
	f := newFixture(t)
	m := f.manager(t, &fakeLogin{}, func() time.Time { return now })
	if m.ask.Model != DefaultAskModel || m.ask.Timeout != DefaultAskTimeout || m.ask.BusyWait != DefaultAskBusyWait || m.ask.Claude != "" {
		t.Fatalf("defaults = %+v", m.ask)
	}
	if DefaultAskModel != "sonnet" || DefaultAskTimeout != 25*time.Second || DefaultAskBusyWait != 3*time.Second {
		t.Fatal("the defaults are sonnet, 25 s and 3 s")
	}
	for name, config := range map[string]AskConfig{
		"a model that is an option":  {Model: "--dangerously-skip-permissions"},
		"a model with a space":       {Model: "sonnet opus"},
		"two minutes":                {Timeout: 2 * time.Minute},
		"a negative time":            {Timeout: -time.Second},
		"a wait as long as the time": {Timeout: 3 * time.Second, BusyWait: 3 * time.Second},
		"a relative executable":      {Claude: "bin/claude"},
	} {
		if _, err := New(FileV1{SchemaVersion: 1, Lanes: []LaneV1{f.codex, f.claude}}, (&fakeLogin{}).start, func() time.Time { return now }, WithAsk(config)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

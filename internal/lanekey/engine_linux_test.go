//go:build linux

package lanekey

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/enginelane"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// Keys of Anthropic's and OpenAI's shapes that exist only in these tests: the operator's (in the lane files before any
// key is sent) and the ones Repo C sends.
const (
	operatorAnthropicKey = "sk-ant-api03-OperatorsOwnTestKeyThatIsNeverSentAnywhere-00"
	operatorOpenAIKey    = "sk-proj-OperatorsOwnTestKeyThatIsNeverSentAnywhere_0000"
	anthropicFirst       = "sk-ant-api03-TestOnlyKeyEnteredOnThePageForClaudeApi-01"
	anthropicSecond      = "sk-ant-api03-TestOnlyKeyEnteredOnThePageForClaudeApi-02"
	openAIKey            = "sk-proj-TestOnlyKeyEnteredOnThePageForCodexApi_000001"
	// The change engine's lane's own key, before the page's reaches it.
	operatorChangeKey = "sk-ant-api03-OperatorsOwnTestKeyInTheChangeEnginesLane-00"
)

var engineTestKeys = []string{operatorAnthropicKey, operatorOpenAIKey, anthropicFirst, anthropicSecond, openAIKey, firstKey, secondKey, operatorChangeKey}

// The claude-api lane file as the operator wrote it: a comment, its home, the key, its model; CRLF on one line.
var claudeLaneFile = "# Claude Code on Anthropic's API\nCLAUDE_CONFIG_DIR=/srv/claude-api-home\nANTHROPIC_API_KEY=" + operatorAnthropicKey +
	"\nANTHROPIC_MODEL=claude-sonnet-5-5\r\n"

// engineFixture is newFixture's host with Claude Code's API-key lane beside Codex's: two profiles on claude-api, one on
// codex-api (whose file holds the key alone), one on an account lane, and Together's two.
type engineFixture struct {
	*fixture
	lanes, claudeFile, codexFile string
}

func newEngineFixture(t *testing.T) *engineFixture {
	t.Helper()
	f := &engineFixture{fixture: newFixture(t)}
	f.lanes = privateFolder(t, filepath.Join(f.config, "lanes"))
	f.claudeFile, f.codexFile = filepath.Join(f.lanes, "claude-api.env"), filepath.Join(f.lanes, "codex-api.env")
	privateFile(t, f.claudeFile, []byte(claudeLaneFile))
	privateFile(t, f.codexFile, []byte("OPENAI_API_KEY="+operatorOpenAIKey+"\n"))
	privateFile(t, filepath.Join(f.lanes, "codex-account.env"), []byte("CODEX_HOME=/srv/codex-account-home\n"))
	claude := template(t, map[string]any{"executor": "claude", "task_effort": "xhigh", "lane": "claude-api", "lane_file": f.claudeFile})
	f.profiles = f.writeEngineProfiles(t, map[string][]byte{
		"local-p02-together":        template(t, map[string]any{"executor": "codex", "task_model": "gpt-oss-120b", "provider": f.together}),
		"local-p02-node-together":   template(t, map[string]any{"executor": "codex", "task_model": "gpt-oss-120b", "provider": f.together}),
		"local-p02-claude-api":      claude,
		"local-p02-node-claude-api": claude,
		"local-p02-codex-api":       template(t, map[string]any{"executor": "codex", "lane": "codex-api", "lane_file": f.codexFile}),
		"local-p02-codex-account":   template(t, map[string]any{"executor": "codex", "lane": "codex-account", "lane_file": filepath.Join(f.lanes, "codex-account.env")}),
		"local-p02":                 template(t, map[string]any{"executor": "codex", "task_effort": "xhigh", "review_effort": "xhigh"}),
	})
	return f
}

func (f *engineFixture) writeEngineProfiles(t *testing.T, templates map[string][]byte) string {
	t.Helper()
	var profiles []map[string]string
	for _, id := range []string{"local-p02", "local-p02-together", "local-p02-node-together", "local-p02-claude-api", "local-p02-node-claude-api",
		"local-p02-codex-api", "local-p02-codex-account", "local-p02-other"} {
		if data, ok := templates[id]; ok {
			path := filepath.Join(f.config, "manifest-template-"+id+".json")
			privateFile(t, path, data)
			profiles = append(profiles, map[string]string{"profile_id": id, "manifest_template_path": path})
		}
	}
	data, err := json.Marshal(map[string]any{"schema_version": 1, "profiles": profiles})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.config, "profiles.json")
	privateFile(t, path, data)
	return path
}

func (f *engineFixture) engineManager(t *testing.T, log *bytes.Buffer, engineLanes ...string) *Manager {
	t.Helper()
	lanes, err := Load(f.profiles, engineLanes...)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(lanes, log, func() time.Time { return testTime })
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// scanFiles fails the test when any file under root but the lane files allowed, or any of the texts, holds a test key.
func scanFiles(t *testing.T, root string, allowed []string, texts ...string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		for _, file := range allowed {
			if path == file {
				return nil
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		texts = append(texts, path+"\x00"+string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range texts {
		for _, key := range engineTestKeys {
			if strings.Contains(text, key) {
				name, _, _ := strings.Cut(text, "\x00")
				t.Errorf("a key is in %.200q", name)
			}
		}
	}
}

func TestAnEngineLaneTakesAKeyOnlyWhenTheOperatorNamesIt(t *testing.T) {
	f := newEngineFixture(t)
	lanes, err := Load(f.profiles)
	if err != nil || len(lanes.Engine) != 0 || strings.Join(lanes.Other, ",") != "claude-api,codex-account,codex-api" {
		t.Fatalf("without names = %+v, %v", lanes, err)
	}
	lanes, err = Load(f.profiles, "codex-api", "claude-api")
	if err != nil || len(lanes.Engine) != 2 || strings.Join(lanes.Other, ",") != "codex-account" || len(lanes.Key) != 1 {
		t.Fatalf("named = %+v, %v", lanes, err)
	}
	claude, codex := lanes.Engine[0], lanes.Engine[1]
	if claude.Name != "claude-api" || claude.LaneFile != f.claudeFile || claude.Executor != "claude" || claude.Setting != "ANTHROPIC_API_KEY" ||
		claude.Provider != "anthropic" || strings.Join(claude.Profiles, ",") != "local-p02-claude-api,local-p02-node-claude-api" {
		t.Fatalf("claude-api = %+v", claude)
	}
	if codex.Name != "codex-api" || codex.Setting != "OPENAI_API_KEY" || codex.Provider != "openai" || strings.Join(codex.Profiles, ",") != "local-p02-codex-api" {
		t.Fatalf("codex-api = %+v", codex)
	}

	// The flag: names, never twice, never an account lane.
	if names, err := EngineLaneNames(" claude-api, codex-api ", []string{"codex-account"}); err != nil || strings.Join(names, ",") != "claude-api,codex-api" {
		t.Fatalf("names = %v, %v", names, err)
	}
	if names, err := EngineLaneNames("", nil); err != nil || names != nil {
		t.Fatalf("no names = %v, %v", names, err)
	}
	for _, list := range []string{"claude-api,claude-api", "claude-api,codex-account", "Claude-API", "claude-api,", "claude api"} {
		if _, err := EngineLaneNames(list, []string{"codex-account"}); err == nil {
			t.Errorf("%q was taken", list)
		}
	}

	// Each of these refuses the whole configuration, so the routes say they are unsupported.
	for name, change := range map[string]func(g *engineFixture) []string{
		"a provider lane":          func(*engineFixture) []string { return []string{"together"} },
		"a lane no profile names":  func(*engineFixture) []string { return []string{"claude-bedrock"} },
		"the controller's sign-in": func(*engineFixture) []string { return []string{"local-p02"} },
		"a lane on Bedrock": func(g *engineFixture) []string {
			g.appendTo(t, g.claudeFile, "CLAUDE_CODE_USE_BEDROCK=1\n")
			return []string{"claude-api"}
		},
		"a lane on a setup token": func(g *engineFixture) []string {
			g.appendTo(t, g.claudeFile, "CLAUDE_CODE_OAUTH_TOKEN=x\n")
			return []string{"claude-api"}
		},
		"Codex at another address": func(g *engineFixture) []string {
			g.appendTo(t, g.codexFile, "OPENAI_BASE_URL=https://example.com/v1\n")
			return []string{"codex-api"}
		},
		"a lane file open to others": func(g *engineFixture) []string { _ = os.Chmod(g.claudeFile, 0o644); return []string{"claude-api"} },
		"two lane files": func(g *engineFixture) []string {
			other := filepath.Join(g.lanes, "claude-api-2.env")
			privateFile(t, other, []byte(claudeLaneFile))
			g.profiles = g.writeEngineProfiles(t, map[string][]byte{
				"local-p02-claude-api": template(t, map[string]any{"executor": "claude", "lane": "claude-api", "lane_file": g.claudeFile}),
				"local-p02-other":      template(t, map[string]any{"executor": "claude", "lane": "claude-api", "lane_file": other}),
			})
			return []string{"claude-api"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := newEngineFixture(t)
			names := change(g)
			if lanes, err := Load(g.profiles, names...); err == nil {
				t.Fatalf("loaded %+v", lanes)
			} else if strings.Contains(err.Error(), operatorAnthropicKey) || strings.Contains(err.Error(), operatorOpenAIKey) {
				t.Fatal("the error holds a key")
			}
		})
	}
	// Codex at OpenAI's own address takes OpenAI's key.
	g := newEngineFixture(t)
	g.appendTo(t, g.codexFile, "OPENAI_BASE_URL=https://api.openai.com/v1\n")
	if _, err := Load(g.profiles, "codex-api"); err != nil {
		t.Fatal(err)
	}
}

func (f *engineFixture) appendTo(t *testing.T, path, line string) {
	t.Helper()
	privateFile(t, path, []byte(read(t, path)+line))
}

func TestAnEngineLanesKeyLineIsReplacedEveryOtherLineKeptAndTheNextBuildReadsIt(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	f := newEngineFixture(t)
	log := &bytes.Buffer{}
	m := f.engineManager(t, log, "claude-api", "codex-api")
	ctx := context.Background()
	var answers []string
	record := func(value any, err error) {
		data, _ := json.Marshal(value)
		answers = append(answers, string(data), fmt.Sprint(err))
	}
	lanes, _ := m.Lanes(ctx)
	record(lanes, nil)
	if len(lanes.Lanes) != 3 || lanes.Lanes[0].Lane != "claude-api" || lanes.Lanes[0].Kind != "engine" || lanes.Lanes[0].Provider != "anthropic" ||
		lanes.Lanes[0].State != "set" || lanes.Lanes[0].Fingerprint != Fingerprint(operatorAnthropicKey) || lanes.Lanes[1].Lane != "codex-api" ||
		lanes.Lanes[1].Provider != "openai" || lanes.Lanes[2].Lane != "together" || lanes.Lanes[2].Provider != "together" {
		t.Fatalf("lanes = %+v", lanes)
	}

	// A build already running holds the settings it started with.
	running, err := enginelane.Load("claude-api", f.claudeFile, "claude")
	if err != nil || running.Settings["ANTHROPIC_API_KEY"] != operatorAnthropicKey {
		t.Fatal(err)
	}
	set, err := m.SetKey(ctx, "claude-api", serviceapi.LaneKeyRequestV1{Key: anthropicFirst})
	record(set, err)
	if err != nil || set.State != "set" || set.Fingerprint != Fingerprint(anthropicFirst) || set.Kind != "engine" || set.Provider != "anthropic" {
		t.Fatalf("set = %+v, %v", set, err)
	}
	want := strings.Replace(claudeLaneFile, operatorAnthropicKey, anthropicFirst, 1)
	if read(t, f.claudeFile) != want {
		t.Fatal("the lane file is not the operator's with the key line alone changed")
	}
	info, err := os.Lstat(f.claudeFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || int(info.Sys().(*syscall.Stat_t).Uid) != os.Getuid() {
		t.Fatalf("the lane file is %v, %v", info.Mode(), err)
	}
	if entries, _ := os.ReadDir(f.lanes); len(entries) != 3 {
		t.Fatalf("the folder holds more than the lane files: %v", entries)
	}
	// The next build's start reads the new key, the other settings as they were; the running one keeps its own.
	next, err := enginelane.Load("claude-api", f.claudeFile, "claude")
	if err != nil || next.Settings["ANTHROPIC_API_KEY"] != anthropicFirst || next.Settings["ANTHROPIC_MODEL"] != "claude-sonnet-5-5" ||
		next.Settings["CLAUDE_CONFIG_DIR"] != "/srv/claude-api-home" || running.Settings["ANTHROPIC_API_KEY"] != operatorAnthropicKey {
		t.Fatalf("the next build's lane = %v", err)
	}
	environment := strings.Join(next.Environment([]string{"PATH=/usr/bin", "ANTHROPIC_API_KEY=inherited"}), "\n")
	if !strings.Contains(environment, "ANTHROPIC_API_KEY="+anthropicFirst) || strings.Contains(environment, "inherited") {
		t.Fatal("the next build's engine does not get the key")
	}

	// If-Match: a key the lane does not hold refuses, and changes nothing; the one it holds goes ahead.
	stale := serviceapi.WithExpectedLaneKey(ctx, Fingerprint(operatorAnthropicKey))
	if answer, err := m.SetKey(stale, "claude-api", serviceapi.LaneKeyRequestV1{Key: anthropicSecond}); !errors.Is(err, serviceapi.ErrLaneKeyChanged) || answer.State != "" {
		t.Fatalf("a stale set = %+v, %v", answer, err)
	}
	if _, err := m.RemoveKey(stale, "claude-api"); !errors.Is(err, serviceapi.ErrLaneKeyChanged) || read(t, f.claudeFile) != want {
		t.Fatalf("a stale removal = %v", err)
	}
	current := serviceapi.WithExpectedLaneKey(ctx, Fingerprint(anthropicFirst))
	set, err = m.SetKey(current, "claude-api", serviceapi.LaneKeyRequestV1{Key: anthropicSecond})
	record(set, err)
	if err != nil || set.Fingerprint != Fingerprint(anthropicSecond) {
		t.Fatalf("set on the key held = %+v, %v", set, err)
	}

	// Removal takes the key's line alone away: the lane's engines then read lane_no_key, and the file still loads.
	removed, err := m.RemoveKey(serviceapi.WithExpectedLaneKey(ctx, Fingerprint(anthropicSecond)), "claude-api")
	record(removed, err)
	without := "# Claude Code on Anthropic's API\nCLAUDE_CONFIG_DIR=/srv/claude-api-home\nANTHROPIC_MODEL=claude-sonnet-5-5\r\n"
	if err != nil || removed.State != "missing" || removed.Fingerprint != "" || read(t, f.claudeFile) != without {
		t.Fatalf("remove = %+v, %v", removed, err)
	}
	if lane, err := enginelane.Load("claude-api", f.claudeFile, "claude"); err != nil || lane.Settings["ANTHROPIC_API_KEY"] != "" {
		t.Fatalf("the lane after removal = %v", err)
	}
	again, err := m.RemoveKey(ctx, "claude-api")
	if err != nil || again.State != "missing" || read(t, f.claudeFile) != without {
		t.Fatalf("remove again = %+v, %v", again, err)
	}
	if _, err := m.RemoveKey(current, "claude-api"); !errors.Is(err, serviceapi.ErrLaneKeyChanged) {
		t.Fatalf("If-Match on a lane with no key = %v", err)
	}
	// A key set again goes on a line of its own at the end.
	if _, err := m.SetKey(ctx, "claude-api", serviceapi.LaneKeyRequestV1{Key: anthropicFirst}); err != nil ||
		read(t, f.claudeFile) != without+"ANTHROPIC_API_KEY="+anthropicFirst+"\n" {
		t.Fatalf("set after removal: %v", err)
	}

	// codex-api's file holds its key alone: replaced, never removed (an empty lane file stops every admission).
	set, err = m.SetKey(ctx, "codex-api", serviceapi.LaneKeyRequestV1{Key: openAIKey})
	record(set, err)
	if err != nil || read(t, f.codexFile) != "OPENAI_API_KEY="+openAIKey+"\n" {
		t.Fatalf("codex-api set = %+v, %v", set, err)
	}
	if _, err := m.RemoveKey(ctx, "codex-api"); !errors.Is(err, serviceapi.ErrLaneKeyRequired) || read(t, f.codexFile) != "OPENAI_API_KEY="+openAIKey+"\n" {
		t.Fatalf("codex-api remove = %v", err)
	}

	// Keys of another shape, and a setup token, are refused before anything is read or written.
	for _, bad := range []string{"sk-ant-oat01-" + strings.Repeat("a", 40), "short", anthropicFirst + "\nCLAUDE_CODE_USE_BEDROCK=1", anthropicFirst + " x", "sk-ant-api03-" + strings.Repeat("a", 500)} {
		if _, err := m.SetKey(ctx, "claude-api", serviceapi.LaneKeyRequestV1{Key: bad}); !errors.Is(err, serviceapi.ErrLaneKeyInvalid) || strings.Contains(err.Error(), bad) {
			t.Errorf("%.20q… = %v", bad, err)
		}
	}
	if read(t, f.claudeFile) != without+"ANTHROPIC_API_KEY="+anthropicFirst+"\n" {
		t.Fatal("a refused key changed the lane file")
	}

	// A lane file the operator moved to Bedrock takes no key and is no longer listed.
	f.appendTo(t, f.claudeFile, "CLAUDE_CODE_USE_BEDROCK=1\n")
	before := read(t, f.claudeFile)
	if _, err := m.SetKey(ctx, "claude-api", serviceapi.LaneKeyRequestV1{Key: anthropicSecond}); !errors.Is(err, serviceapi.ErrLaneNotKey) {
		t.Fatalf("set on Bedrock = %v", err)
	}
	if _, err := m.RemoveKey(ctx, "claude-api"); !errors.Is(err, serviceapi.ErrLaneNotKey) || read(t, f.claudeFile) != before {
		t.Fatalf("remove on Bedrock = %v", err)
	}
	if lanes, _ := m.Lanes(ctx); len(lanes.Lanes) != 2 || lanes.Lanes[0].Lane != "codex-api" {
		t.Fatalf("lanes with claude-api on Bedrock = %+v", lanes)
	}
	// A lane file no build reads is not rewritten: its other lines can't be kept.
	privateFile(t, f.codexFile, []byte("OPENAI_API_KEY="+openAIKey+"\nNOT_A_SETTING=1\n"))
	if _, err := m.SetKey(ctx, "codex-api", serviceapi.LaneKeyRequestV1{Key: openAIKey}); !errors.Is(err, serviceapi.ErrLaneKeyUnavailable) {
		t.Fatalf("set on an unreadable lane = %v", err)
	}
	if lanes, _ := m.Lanes(ctx); lanes.Lanes[0].State != "unreadable" {
		t.Fatalf("an unreadable lane = %+v", lanes)
	}

	wantLog := []string{
		"abcp lane key set at=2026-11-07T18:00:00Z lane=claude-api fingerprint=" + Fingerprint(anthropicFirst),
		"abcp lane key refused at=2026-11-07T18:00:00Z lane=claude-api reason=changed",
		"abcp lane key refused at=2026-11-07T18:00:00Z lane=claude-api reason=changed",
		"abcp lane key set at=2026-11-07T18:00:00Z lane=claude-api fingerprint=" + Fingerprint(anthropicSecond),
		"abcp lane key removed at=2026-11-07T18:00:00Z lane=claude-api fingerprint=" + Fingerprint(anthropicSecond),
		"abcp lane key removed at=2026-11-07T18:00:00Z lane=claude-api was=missing",
		"abcp lane key refused at=2026-11-07T18:00:00Z lane=claude-api reason=changed",
		"abcp lane key set at=2026-11-07T18:00:00Z lane=claude-api fingerprint=" + Fingerprint(anthropicFirst),
		"abcp lane key set at=2026-11-07T18:00:00Z lane=codex-api fingerprint=" + Fingerprint(openAIKey),
		"abcp lane key kept at=2026-11-07T18:00:00Z lane=codex-api reason=required",
		"abcp lane key refused at=2026-11-07T18:00:00Z lane=claude-api reason=shape",
		"abcp lane key refused at=2026-11-07T18:00:00Z lane=claude-api reason=shape",
		"abcp lane key refused at=2026-11-07T18:00:00Z lane=claude-api reason=shape",
		"abcp lane key refused at=2026-11-07T18:00:00Z lane=claude-api reason=shape",
		"abcp lane key refused at=2026-11-07T18:00:00Z lane=claude-api reason=shape",
		"abcp lane key refused at=2026-11-07T18:00:00Z lane=claude-api reason=bedrock",
		"abcp lane key refused at=2026-11-07T18:00:00Z lane=claude-api reason=bedrock",
		"abcp lane key not set at=2026-11-07T18:00:00Z lane=codex-api reason=read",
	}
	if log.String() != strings.Join(wantLog, "\n")+"\n" {
		t.Fatalf("audit lines:\n%s", log.String())
	}
	// No file but the two lane files, no answer, error or log line holds any key.
	scanFiles(t, f.root, []string{f.claudeFile, f.codexFile}, append(answers, log.String())...)
}

func TestAnEngineLaneFileIsReplacedWholeOrNotAtAll(t *testing.T) {
	f := newEngineFixture(t)
	m := f.engineManager(t, &bytes.Buffer{}, "claude-api")
	ctx := context.Background()
	defer func() { syncFile, renameFile = syscall.Fsync, syscall.Renameat }()
	for name, fail := range map[string]func(){
		"sync":   func() { syncFile = func(int) error { return syscall.EIO } },
		"rename": func() { renameFile = func(int, string, int, string) error { return syscall.EIO } },
	} {
		fail()
		_, setErr := m.SetKey(ctx, "claude-api", serviceapi.LaneKeyRequestV1{Key: anthropicFirst})
		_, removeErr := m.RemoveKey(ctx, "claude-api")
		syncFile, renameFile = syscall.Fsync, syscall.Renameat
		if !errors.Is(setErr, serviceapi.ErrLaneKeyUnavailable) || !errors.Is(removeErr, serviceapi.ErrLaneKeyUnavailable) || strings.Contains(setErr.Error(), anthropicFirst) {
			t.Fatalf("%s failing = %v, %v", name, setErr, removeErr)
		}
		if read(t, f.claudeFile) != claudeLaneFile {
			t.Fatalf("%s failing changed the lane file", name)
		}
		if entries, _ := os.ReadDir(f.lanes); len(entries) != 3 {
			t.Fatalf("%s failing left a file: %v", name, entries)
		}
	}
	// A folder others may write in is refused, as for a key lane.
	if err := os.Chmod(f.lanes, 0o777); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(f.lanes, 0o700)
	if _, err := m.SetKey(ctx, "claude-api", serviceapi.LaneKeyRequestV1{Key: anthropicFirst}); !errors.Is(err, serviceapi.ErrLaneKeyUnavailable) || read(t, f.claudeFile) != claudeLaneFile {
		t.Fatalf("a folder others may write in = %v", err)
	}
}

func TestTheLaneRoutesTakeAnEngineLanesKeyAndIfMatch(t *testing.T) {
	f := newEngineFixture(t)
	log := &bytes.Buffer{}
	handler, token := liveServer(t, f.fixture, []string{"lanes.control"}, f.engineManager(t, log, "claude-api"), log)
	var answers []string
	send := func(method, path, ifMatch, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		if ifMatch != "" {
			request.Header.Set("If-Match", ifMatch)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		answers = append(answers, fmt.Sprint(response.Code, response.Header(), response.Body.String()))
		return response
	}
	operator := Fingerprint(operatorAnthropicKey)
	if r := send(http.MethodGet, "/v1/lanes", "", ""); r.Code != 200 || r.Body.String() != `{"schema_version":"LanesV1","lanes":[`+
		`{"lane":"claude-api","kind":"engine","provider":"anthropic","state":"set","fingerprint":"`+operator+`","profiles":["local-p02-claude-api","local-p02-node-claude-api"]},`+
		`{"lane":"together","kind":"key","provider":"together","state":"missing","profiles":["local-p02-node-together","local-p02-together"]}]}`+"\n" {
		t.Fatalf("lanes = %d %s", r.Code, r.Body.String())
	}
	body := `{"key":"` + anthropicFirst + `"}`
	for _, refused := range []struct {
		method, path, ifMatch, body string
		status                      int
		code                        string
	}{
		{http.MethodPut, "/v1/lanes/claude-api/key", `"00000000"`, body, 412, "lane_key_changed"},
		{http.MethodPut, "/v1/lanes/claude-api/key", operator, body, 400, "invalid_request"},
		{http.MethodPut, "/v1/lanes/claude-api/key", `W/"` + operator + `"`, body, 400, "invalid_request"},
		{http.MethodPut, "/v1/lanes/claude-api/key", `"` + strings.ToUpper(operator) + `"`, body, 400, "invalid_request"},
		{http.MethodPut, "/v1/lanes/claude-api/key", `*`, body, 400, "invalid_request"},
		{http.MethodGet, "/v1/lanes", `"` + operator + `"`, "", 400, "invalid_request"},
		{http.MethodPut, "/v1/lanes/codex-api/key", "", body, 409, "lane_not_key"},
		{http.MethodPut, "/v1/lanes/claude-api/key", "", `{"key":"sk-ant-oat01-` + strings.Repeat("a", 40) + `"}`, 400, "lane_key_invalid"},
	} {
		if r := send(refused.method, refused.path, refused.ifMatch, refused.body); r.Code != refused.status || !strings.Contains(r.Body.String(), `"code":"`+refused.code+`"`) {
			t.Errorf("%s %s If-Match %s = %d %s", refused.method, refused.path, refused.ifMatch, r.Code, r.Body.String())
		}
	}
	if read(t, f.claudeFile) != claudeLaneFile {
		t.Fatal("a refused request changed the lane file")
	}
	if r := send(http.MethodPut, "/v1/lanes/claude-api/key", `"`+operator+`"`, body); r.Code != 200 || !strings.Contains(r.Body.String(), `"fingerprint":"`+Fingerprint(anthropicFirst)+`"`) {
		t.Fatalf("set = %d %s", r.Code, r.Body.String())
	}
	if r := send(http.MethodDelete, "/v1/lanes/claude-api/key", `"`+operator+`"`, ""); r.Code != 412 {
		t.Fatalf("a stale removal = %d %s", r.Code, r.Body.String())
	}
	if r := send(http.MethodDelete, "/v1/lanes/claude-api/key", `"`+Fingerprint(anthropicFirst)+`"`, ""); r.Code != 200 || !strings.Contains(r.Body.String(), `"state":"missing"`) {
		t.Fatalf("remove = %d %s", r.Code, r.Body.String())
	}
	if strings.Contains(read(t, f.claudeFile), "ANTHROPIC_API_KEY") {
		t.Fatal("the key line is still there")
	}
	scanFiles(t, f.root, []string{f.claudeFile, f.codexFile}, append(answers, log.String())...)
}

// changeLaneText is the change engine's lane file as the host has it (change-engine.json's change-api): the key and the
// models, no configuration folder.
var changeLaneText = "ANTHROPIC_API_KEY=" + operatorChangeKey + "\nANTHROPIC_MODEL=claude-sonnet-5-5\nANTHROPIC_SMALL_FAST_MODEL=claude-haiku-4-5\n"

// changeLane writes the change engine's lane file beside the others and answers the lane as the change engine file
// names it.
func (f *engineFixture) changeLane(t *testing.T) ChangeLane {
	t.Helper()
	lane := ChangeLane{Name: "change-api", LaneFile: filepath.Join(f.lanes, "change-api.env")}
	privateFile(t, lane.LaneFile, []byte(changeLaneText))
	return lane
}

func TestTheChangeEnginesLaneTakesTheAnthropicKeyOnlyWhenTheOperatorNamesIt(t *testing.T) {
	f := newEngineFixture(t)
	change := f.changeLane(t)
	// Not named, it is left out entirely, as before: neither a lane that takes a key nor another lane.
	lanes, err := LoadWithChange(f.profiles, change, "claude-api", "codex-api")
	if err != nil || len(lanes.Engine) != 2 || strings.Join(lanes.Other, ",") != "codex-account" {
		t.Fatalf("not named = %+v, %v", lanes, err)
	}
	// Named without a change engine (change jobs not configured, or their engine file not checking), no lane is it.
	if _, err := Load(f.profiles, "claude-api", "change-api"); err == nil || !strings.Contains(err.Error(), "nor the change engine") {
		t.Fatalf("named without the change engine = %v", err)
	}
	if names, err := EngineLaneNames("claude-api,codex-api,change-api", []string{"codex-account"}); err != nil || len(names) != 3 {
		t.Fatalf("the flag = %v, %v", names, err)
	}
	lanes, err = LoadWithChange(f.profiles, change, "claude-api", "codex-api", "change-api")
	if err != nil || len(lanes.Engine) != 3 || strings.Join(lanes.Other, ",") != "codex-account" {
		t.Fatalf("named = %+v, %v", lanes, err)
	}
	lane := lanes.Engine[0]
	if lane.Name != "change-api" || lane.LaneFile != change.LaneFile || lane.Executor != "claude" || lane.Setting != "ANTHROPIC_API_KEY" ||
		lane.Provider != "anthropic" || lane.Profiles == nil || len(lane.Profiles) != 0 || strings.Join(lane.Uses, ",") != "change-jobs" ||
		lanes.Engine[1].Name != "claude-api" || lanes.Engine[1].Uses != nil {
		t.Fatalf("change-api = %+v", lanes.Engine)
	}
	// A profile may name the same lane on the same file and executor: its builds and the change jobs then read it.
	g := newEngineFixture(t)
	shared := g.changeLane(t)
	g.profiles = g.writeEngineProfiles(t, map[string][]byte{
		"local-p02-claude-api": template(t, map[string]any{"executor": "claude", "lane": "claude-api", "lane_file": g.claudeFile}),
		"local-p02-other":      template(t, map[string]any{"executor": "claude", "lane": "change-api", "lane_file": shared.LaneFile}),
	})
	if both, err := LoadWithChange(g.profiles, shared, "change-api"); err != nil || len(both.Engine) != 1 ||
		strings.Join(both.Engine[0].Profiles, ",") != "local-p02-other" || strings.Join(both.Engine[0].Uses, ",") != "change-jobs" {
		t.Fatalf("a profile on the change lane = %+v, %v", both, err)
	}

	// Each of these refuses the whole configuration, so the routes say they are unsupported, as for a profile's lane.
	for name, setup := range map[string]func(g *engineFixture, lane ChangeLane) ChangeLane{
		"on Bedrock": func(g *engineFixture, lane ChangeLane) ChangeLane {
			g.appendTo(t, lane.LaneFile, "CLAUDE_CODE_USE_BEDROCK=1\nAWS_REGION=us-east-1\n")
			return lane
		},
		"on a setup token": func(g *engineFixture, lane ChangeLane) ChangeLane {
			g.appendTo(t, lane.LaneFile, "CLAUDE_CODE_OAUTH_TOKEN=x\n")
			return lane
		},
		"a lane file open to others": func(g *engineFixture, lane ChangeLane) ChangeLane { _ = os.Chmod(lane.LaneFile, 0o644); return lane },
		"a lane file Claude Code does not read": func(g *engineFixture, lane ChangeLane) ChangeLane {
			g.appendTo(t, lane.LaneFile, "OPENAI_API_KEY="+operatorOpenAIKey+"\n")
			return lane
		},
		"no lane file": func(g *engineFixture, lane ChangeLane) ChangeLane {
			return ChangeLane{Name: lane.Name, LaneFile: lane.LaneFile + ".gone.env"}
		},
		"a lane path": func(g *engineFixture, lane ChangeLane) ChangeLane {
			return ChangeLane{Name: lane.Name, LaneFile: "change-api.env"}
		},
		"claude-api's file, though claude-api is not named": func(g *engineFixture, lane ChangeLane) ChangeLane {
			return ChangeLane{Name: lane.Name, LaneFile: g.claudeFile}
		},
		"an account lane's file": func(g *engineFixture, lane ChangeLane) ChangeLane {
			return ChangeLane{Name: lane.Name, LaneFile: filepath.Join(g.lanes, "codex-account.env")}
		},
		"a provider's name": func(g *engineFixture, lane ChangeLane) ChangeLane {
			return ChangeLane{Name: "together", LaneFile: lane.LaneFile}
		},
		"a profile naming it on another file": func(g *engineFixture, lane ChangeLane) ChangeLane {
			other := filepath.Join(g.lanes, "change-api-2.env")
			privateFile(t, other, []byte(changeLaneText))
			g.profiles = g.writeEngineProfiles(t, map[string][]byte{
				"local-p02-claude-api": template(t, map[string]any{"executor": "claude", "lane": "claude-api", "lane_file": g.claudeFile}),
				"local-p02-other":      template(t, map[string]any{"executor": "claude", "lane": "change-api", "lane_file": other}),
			})
			return lane
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := newEngineFixture(t)
			lane := setup(g, g.changeLane(t))
			if lanes, err := LoadWithChange(g.profiles, lane, lane.Name); err == nil {
				t.Fatalf("loaded %+v", lanes)
			} else if strings.Contains(err.Error(), operatorChangeKey) || strings.Contains(err.Error(), operatorAnthropicKey) {
				t.Fatal("the error holds a key")
			}
		})
	}
}

func TestTheChangeEnginesLaneKeyLineIsReplacedAndTheNextChangeJobReadsIt(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	f := newEngineFixture(t)
	change := f.changeLane(t)
	lanes, err := LoadWithChange(f.profiles, change, "claude-api", "change-api")
	if err != nil {
		t.Fatal(err)
	}
	log := &bytes.Buffer{}
	m, err := New(lanes, log, func() time.Time { return testTime })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var answers []string
	record := func(value any, err error) string {
		data, _ := json.Marshal(value)
		answers = append(answers, string(data), fmt.Sprint(err))
		return string(data)
	}
	listed, _ := m.Lanes(ctx)
	record(listed, nil)
	if len(listed.Lanes) != 3 || record(listed.Lanes[0], nil) != `{"lane":"change-api","kind":"engine","provider":"anthropic","state":"set","fingerprint":"`+
		Fingerprint(operatorChangeKey)+`","profiles":[],"uses":["change-jobs"]}` || strings.Contains(record(listed.Lanes[1], nil), "uses") {
		t.Fatalf("lanes = %+v", listed)
	}

	// A change job already running holds the settings it started with (stackjob.ClaudeEngine.Pin reads the lane so).
	running, err := enginelane.Load("change-api", change.LaneFile, "claude")
	if err != nil || running.Settings["ANTHROPIC_API_KEY"] != operatorChangeKey {
		t.Fatal(err)
	}
	set, err := m.SetKey(ctx, "change-api", serviceapi.LaneKeyRequestV1{Key: anthropicFirst})
	if record(set, err) != `{"lane":"change-api","kind":"engine","provider":"anthropic","state":"set","fingerprint":"`+Fingerprint(anthropicFirst)+
		`","profiles":[],"uses":["change-jobs"]}` || err != nil {
		t.Fatalf("set = %+v, %v", set, err)
	}
	want := strings.Replace(changeLaneText, operatorChangeKey, anthropicFirst, 1)
	if read(t, change.LaneFile) != want || read(t, f.claudeFile) != claudeLaneFile {
		t.Fatal("the change lane's file is not the operator's with the key line alone changed, or another lane's file changed")
	}
	info, err := os.Lstat(change.LaneFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || int(info.Sys().(*syscall.Stat_t).Uid) != os.Getuid() {
		t.Fatalf("the lane file is %v, %v", info.Mode(), err)
	}
	if entries, _ := os.ReadDir(f.lanes); len(entries) != 4 {
		t.Fatalf("the folder holds more than the lane files: %v", entries)
	}
	// The next change job's start reads the new key with the models as they were; the running one keeps its own.
	next, err := enginelane.Load("change-api", change.LaneFile, "claude")
	if err != nil || next.Settings["ANTHROPIC_API_KEY"] != anthropicFirst || next.Settings["ANTHROPIC_MODEL"] != "claude-sonnet-5-5" ||
		running.Settings["ANTHROPIC_API_KEY"] != operatorChangeKey {
		t.Fatalf("the next change job's lane = %v", err)
	}

	// If-Match, as on every lane: a key the lane does not hold refuses, and changes nothing.
	stale := serviceapi.WithExpectedLaneKey(ctx, Fingerprint(operatorChangeKey))
	if _, err := m.SetKey(stale, "change-api", serviceapi.LaneKeyRequestV1{Key: anthropicSecond}); !errors.Is(err, serviceapi.ErrLaneKeyChanged) {
		t.Fatalf("a stale set = %v", err)
	}
	if _, err := m.RemoveKey(stale, "change-api"); !errors.Is(err, serviceapi.ErrLaneKeyChanged) || read(t, change.LaneFile) != want {
		t.Fatalf("a stale removal = %v", err)
	}
	// Removal takes the key's line alone away; the file still loads, so the next change job starts and has no key.
	removed, err := m.RemoveKey(serviceapi.WithExpectedLaneKey(ctx, Fingerprint(anthropicFirst)), "change-api")
	record(removed, err)
	without := "ANTHROPIC_MODEL=claude-sonnet-5-5\nANTHROPIC_SMALL_FAST_MODEL=claude-haiku-4-5\n"
	if err != nil || removed.State != "missing" || removed.Fingerprint != "" || strings.Join(removed.Uses, ",") != "change-jobs" || read(t, change.LaneFile) != without {
		t.Fatalf("remove = %+v, %v", removed, err)
	}
	if lane, err := enginelane.Load("change-api", change.LaneFile, "claude"); err != nil || lane.Settings["ANTHROPIC_API_KEY"] != "" {
		t.Fatalf("the lane after removal = %v", err)
	}
	// A lane file holding the key alone keeps it: an empty one would stop change jobs from loading at the next start.
	privateFile(t, change.LaneFile, []byte("ANTHROPIC_API_KEY="+anthropicSecond+"\n"))
	if _, err := m.RemoveKey(ctx, "change-api"); !errors.Is(err, serviceapi.ErrLaneKeyRequired) || read(t, change.LaneFile) != "ANTHROPIC_API_KEY="+anthropicSecond+"\n" {
		t.Fatalf("removing the only setting = %v", err)
	}
	// A setup token is never an API key.
	if _, err := m.SetKey(ctx, "change-api", serviceapi.LaneKeyRequestV1{Key: "sk-ant-oat01-" + strings.Repeat("a", 40)}); !errors.Is(err, serviceapi.ErrLaneKeyInvalid) {
		t.Fatalf("a setup token = %v", err)
	}
	// A change lane the operator moved to Bedrock takes no key and is no longer listed.
	f.appendTo(t, change.LaneFile, "CLAUDE_CODE_USE_BEDROCK=1\n")
	if _, err := m.SetKey(ctx, "change-api", serviceapi.LaneKeyRequestV1{Key: anthropicFirst}); !errors.Is(err, serviceapi.ErrLaneNotKey) {
		t.Fatalf("set on Bedrock = %v", err)
	}
	if listed, _ := m.Lanes(ctx); len(listed.Lanes) != 2 || listed.Lanes[0].Lane != "claude-api" {
		t.Fatalf("lanes with change-api on Bedrock = %+v", listed)
	}

	wantLog := []string{
		"abcp lane key set at=2026-11-07T18:00:00Z lane=change-api fingerprint=" + Fingerprint(anthropicFirst),
		"abcp lane key refused at=2026-11-07T18:00:00Z lane=change-api reason=changed",
		"abcp lane key refused at=2026-11-07T18:00:00Z lane=change-api reason=changed",
		"abcp lane key removed at=2026-11-07T18:00:00Z lane=change-api fingerprint=" + Fingerprint(anthropicFirst),
		"abcp lane key kept at=2026-11-07T18:00:00Z lane=change-api reason=required",
		"abcp lane key refused at=2026-11-07T18:00:00Z lane=change-api reason=shape",
		"abcp lane key refused at=2026-11-07T18:00:00Z lane=change-api reason=bedrock",
	}
	if log.String() != strings.Join(wantLog, "\n")+"\n" {
		t.Fatalf("audit lines:\n%s", log.String())
	}
	scanFiles(t, f.root, []string{f.claudeFile, f.codexFile, change.LaneFile}, append(answers, log.String())...)
}

func TestTheLaneRoutesListAndTakeTheChangeEnginesLane(t *testing.T) {
	f := newEngineFixture(t)
	change := f.changeLane(t)
	lanes, err := LoadWithChange(f.profiles, change, "claude-api", "change-api")
	if err != nil {
		t.Fatal(err)
	}
	log := &bytes.Buffer{}
	manager, err := New(lanes, log, func() time.Time { return testTime })
	if err != nil {
		t.Fatal(err)
	}
	handler, token := liveServer(t, f.fixture, []string{"lanes.control"}, manager, log)
	var answers []string
	send := func(method, path, ifMatch, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		if ifMatch != "" {
			request.Header.Set("If-Match", ifMatch)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		answers = append(answers, fmt.Sprint(response.Code, response.Header(), response.Body.String()))
		return response
	}
	operator := Fingerprint(operatorChangeKey)
	if r := send(http.MethodGet, "/v1/lanes", "", ""); r.Code != 200 || r.Body.String() != `{"schema_version":"LanesV1","lanes":[`+
		`{"lane":"change-api","kind":"engine","provider":"anthropic","state":"set","fingerprint":"`+operator+`","profiles":[],"uses":["change-jobs"]},`+
		`{"lane":"claude-api","kind":"engine","provider":"anthropic","state":"set","fingerprint":"`+Fingerprint(operatorAnthropicKey)+`","profiles":["local-p02-claude-api","local-p02-node-claude-api"]},`+
		`{"lane":"together","kind":"key","provider":"together","state":"missing","profiles":["local-p02-node-together","local-p02-together"]}]}`+"\n" {
		t.Fatalf("lanes = %d %s", r.Code, r.Body.String())
	}
	body := `{"key":"` + anthropicFirst + `"}`
	if r := send(http.MethodPut, "/v1/lanes/change-api/key", `"00000000"`, body); r.Code != 412 || !strings.Contains(r.Body.String(), `"code":"lane_key_changed"`) {
		t.Fatalf("a stale set = %d %s", r.Code, r.Body.String())
	}
	if r := send(http.MethodPut, "/v1/lanes/change-api/key", `"`+operator+`"`, body); r.Code != 200 || r.Body.String() != `{"lane":"change-api","kind":"engine",`+
		`"provider":"anthropic","state":"set","fingerprint":"`+Fingerprint(anthropicFirst)+`","profiles":[],"uses":["change-jobs"]}`+"\n" {
		t.Fatalf("set = %d %s", r.Code, r.Body.String())
	}
	if read(t, change.LaneFile) != strings.Replace(changeLaneText, operatorChangeKey, anthropicFirst, 1) || read(t, f.claudeFile) != claudeLaneFile {
		t.Fatal("the set did not replace the change lane's key line alone")
	}
	if r := send(http.MethodDelete, "/v1/lanes/change-api/key", `"`+operator+`"`, ""); r.Code != 412 {
		t.Fatalf("a stale removal = %d %s", r.Code, r.Body.String())
	}
	if r := send(http.MethodDelete, "/v1/lanes/change-api/key", `"`+Fingerprint(anthropicFirst)+`"`, ""); r.Code != 200 || !strings.Contains(r.Body.String(), `"state":"missing"`) {
		t.Fatalf("remove = %d %s", r.Code, r.Body.String())
	}
	privateFile(t, change.LaneFile, []byte("ANTHROPIC_API_KEY="+anthropicSecond+"\n"))
	if r := send(http.MethodDelete, "/v1/lanes/change-api/key", "", ""); r.Code != 409 || !strings.Contains(r.Body.String(), `"code":"lane_key_required"`) {
		t.Fatalf("removing the only setting = %d %s", r.Code, r.Body.String())
	}
	scanFiles(t, f.root, []string{f.claudeFile, f.codexFile, change.LaneFile}, append(answers, log.String())...)
}

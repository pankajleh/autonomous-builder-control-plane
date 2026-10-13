//go:build linux

package accountlane

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/enginelane"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

const token = "sk-ant-oat01-abcdefghijklmnopqrstuvwxyz0123456789_-ABCDEF"

type fixture struct {
	root   string
	codex  LaneV1
	claude LaneV1
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{root: root,
		codex:  LaneV1{Lane: "codex-account", Executor: "codex", LaneFile: filepath.Join(root, "codex-account.env"), Home: filepath.Join(root, "codex-account-home")},
		claude: LaneV1{Lane: "claude-account", Executor: "claude", LaneFile: filepath.Join(root, "claude-account.env"), Home: filepath.Join(root, "claude-account-home")},
	}
	for _, lane := range []LaneV1{f.codex, f.claude} {
		if err := os.Mkdir(lane.Home, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lane.LaneFile, []byte(homeSetting[lane.Executor]+"="+lane.Home+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

type fakeLogin struct {
	starts int
	stops  int
	done   chan struct{}
	err    error
}

func (f *fakeLogin) start(context.Context, string) (DeviceLogin, error) {
	if f.err != nil {
		return DeviceLogin{}, f.err
	}
	f.starts++
	f.done = make(chan struct{})
	return DeviceLogin{VerificationURL: "https://auth.openai.com/codex/device", UserCode: "U2UU-AQ0TL", ExpiresAt: now.Add(15 * time.Minute),
		Done: f.done, Stop: func() { f.stops++ }}, nil
}

var now = time.Date(2026, 10, 13, 7, 0, 0, 0, time.UTC)

func (f fixture) manager(t *testing.T, login *fakeLogin, clock func() time.Time) *Manager {
	t.Helper()
	m, err := New(FileV1{SchemaVersion: 1, Lanes: []LaneV1{f.codex, f.claude}}, login.start, clock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func TestAClaudeLaneKeepsItsSetupTokenAndNeverShowsIt(t *testing.T) {
	f := newFixture(t)
	m := f.manager(t, &fakeLogin{}, func() time.Time { return now })
	for _, bad := range []string{"", "sk-ant-api03-not-a-setup-token-0123456789", "sk-ant-oat01-short", token + "\nANTHROPIC_API_KEY=x", token + " "} {
		if _, err := m.SignIn(context.Background(), "claude-account", serviceapi.AccountLaneSignInV1{Token: bad}); !errors.Is(err, serviceapi.ErrAccountLaneInvalid) {
			t.Fatalf("%q = %v", bad, err)
		}
	}
	state, err := m.SignIn(context.Background(), "claude-account", serviceapi.AccountLaneSignInV1{Token: token})
	if err != nil || state.State != "signed_in" || state.Since == "" || state.Device != nil {
		t.Fatalf("signed in = %+v %v", state, err)
	}
	info, err := os.Stat(f.claude.LaneFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the lane file stays private: %v %v", info.Mode(), err)
	}
	lane, err := enginelane.Load("claude-account", f.claude.LaneFile, "claude")
	if err != nil || lane.Settings["CLAUDE_CODE_OAUTH_TOKEN"] != token || lane.Settings["CLAUDE_CONFIG_DIR"] != f.claude.Home {
		t.Fatalf("the build reads the token from the lane: %v %v", lane.Keys(), err)
	}
	lanes, err := m.AccountLanes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(lanes)
	if strings.Contains(string(data), "oat01") || !strings.Contains(string(data), `"state":"signed_in"`) {
		t.Fatalf("no answer carries the token: %s", data)
	}
	out, err := m.SignOut(context.Background(), "claude-account")
	if err != nil || out.State != "signed_out" {
		t.Fatalf("signed out = %+v %v", out, err)
	}
	lane, err = enginelane.Load("claude-account", f.claude.LaneFile, "claude")
	if err != nil || len(lane.Settings) != 1 || lane.Settings["CLAUDE_CONFIG_DIR"] != f.claude.Home {
		t.Fatalf("only the home stays: %v %v", lane.Keys(), err)
	}
	entries, _ := os.ReadDir(f.root)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			t.Fatalf("no temporary file is left: %s", entry.Name())
		}
	}
}

func TestACodexLaneSignsInOnTheAdministratorsDeviceAndOut(t *testing.T) {
	f := newFixture(t)
	login := &fakeLogin{}
	clock := now
	m := f.manager(t, login, func() time.Time { return clock })
	if _, err := m.SignIn(context.Background(), "codex-account", serviceapi.AccountLaneSignInV1{Token: token}); !errors.Is(err, serviceapi.ErrAccountLaneInvalid) {
		t.Fatalf("a Codex lane takes no token: %v", err)
	}
	state, err := m.SignIn(context.Background(), "codex-account", serviceapi.AccountLaneSignInV1{})
	if err != nil || state.State != "signing_in" || state.Device == nil || state.Device.UserCode != "U2UU-AQ0TL" ||
		state.Device.VerificationURL != "https://auth.openai.com/codex/device" || state.Device.ExpiresAt != "2026-10-13T07:15:00Z" {
		t.Fatalf("device sign-in = %+v %v", state, err)
	}
	again, err := m.SignIn(context.Background(), "codex-account", serviceapi.AccountLaneSignInV1{})
	if err != nil || again.Device == nil || login.starts != 1 {
		t.Fatalf("asking again shows the same code: %+v %v starts=%d", again, err, login.starts)
	}
	if err := os.WriteFile(filepath.Join(f.codex.Home, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(login.done)
	lanes, err := m.AccountLanes(context.Background())
	if err != nil || lanes.Lanes[0].State != "signed_in" || lanes.Lanes[0].Device != nil || lanes.Lanes[1].State != "signed_out" {
		t.Fatalf("signed in once Codex keeps its sign-in: %+v %v", lanes, err)
	}
	if _, err := m.SignIn(context.Background(), "codex-account", serviceapi.AccountLaneSignInV1{}); !errors.Is(err, serviceapi.ErrAccountLaneSignedIn) {
		t.Fatalf("a signed-in lane signs out first: %v", err)
	}
	out, err := m.SignOut(context.Background(), "codex-account")
	if err != nil || out.State != "signed_out" {
		t.Fatalf("signed out = %+v %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(f.codex.Home, "auth.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the sign-in is gone: %v", err)
	}

	if _, err := m.SignIn(context.Background(), "codex-account", serviceapi.AccountLaneSignInV1{}); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(16 * time.Minute)
	expired, _ := m.AccountLanes(context.Background())
	if expired.Lanes[0].State != "signed_out" {
		t.Fatalf("an expired code is not shown: %+v", expired.Lanes[0])
	}
	if _, err := m.SignOut(context.Background(), "codex-account"); err != nil || login.stops == 0 {
		t.Fatalf("signing out stops a pending sign-in: %v stops=%d", err, login.stops)
	}
	login.err = errors.New("no codex")
	if _, err := m.SignIn(context.Background(), "codex-account", serviceapi.AccountLaneSignInV1{}); !errors.Is(err, serviceapi.ErrAccountLaneUnavailable) {
		t.Fatalf("a sign-in that cannot start: %v", err)
	}
	if _, err := m.SignIn(context.Background(), "gemini-account", serviceapi.AccountLaneSignInV1{}); !errors.Is(err, serviceapi.ErrAccountLaneUnknown) {
		t.Fatalf("an unknown lane: %v", err)
	}
	if _, err := m.SignOut(context.Background(), "gemini-account"); !errors.Is(err, serviceapi.ErrAccountLaneUnknown) {
		t.Fatalf("an unknown lane: %v", err)
	}
}

func TestEveryLaneIsCheckedWhenTheFileLoads(t *testing.T) {
	login := &fakeLogin{}
	clock := func() time.Time { return now }
	for name, edit := range map[string]func(f *fixture){
		"a home open to others": func(f *fixture) { os.Chmod(f.codex.Home, 0o755) },
		"no home":               func(f *fixture) { os.Remove(f.claude.Home) },
		"a lane file open to others": func(f *fixture) {
			os.Chmod(f.codex.LaneFile, 0o644)
		},
		"a lane file with a key": func(f *fixture) {
			os.WriteFile(f.codex.LaneFile, []byte("CODEX_HOME="+f.codex.Home+"\nOPENAI_API_KEY=sk-x\n"), 0o600)
		},
		"a lane file pointing elsewhere": func(f *fixture) {
			os.WriteFile(f.claude.LaneFile, []byte("CLAUDE_CONFIG_DIR=/home/devagent/.claude\n"), 0o600)
		},
		"an unknown engine":   func(f *fixture) { f.claude.Executor = "gemini" },
		"a relative home":     func(f *fixture) { f.codex.Home = "codex-home" },
		"the same lane twice": func(f *fixture) { f.claude.Lane = f.codex.Lane },
		"the same home twice": func(f *fixture) { f.claude.Home = f.codex.Home },
	} {
		f := newFixture(t)
		edit(&f)
		if _, err := New(FileV1{SchemaVersion: 1, Lanes: []LaneV1{f.codex, f.claude}}, login.start, clock); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	f := newFixture(t)
	if _, err := New(FileV1{SchemaVersion: 2, Lanes: []LaneV1{f.codex}}, login.start, clock); err == nil {
		t.Fatal("another schema is refused")
	}
	if _, err := New(FileV1{SchemaVersion: 1}, login.start, clock); err == nil {
		t.Fatal("a file without lanes is refused")
	}
	signedIn := newFixture(t)
	os.WriteFile(signedIn.claude.LaneFile, []byte("CLAUDE_CONFIG_DIR="+signedIn.claude.Home+"\nCLAUDE_CODE_OAUTH_TOKEN="+token+"\n"), 0o600)
	if _, err := New(FileV1{SchemaVersion: 1, Lanes: []LaneV1{signedIn.codex, signedIn.claude}}, login.start, clock); err != nil {
		t.Fatalf("a signed-in Claude lane loads: %v", err)
	}
}

func TestTheDeviceLinkAndCodeAreReadFromCodexsOwnWords(t *testing.T) {
	output := "WARNING: proceeding\n\nWelcome to Codex [v\x1b[90m0.162.0\x1b[0m]\n\nFollow these steps to sign in with ChatGPT using device code authorization:\n\n" +
		"1. Open this link in your browser and sign in to your account\n   \x1b[94mhttps://auth.openai.com/codex/device\x1b[0m\n\n" +
		"2. Enter this one-time code \x1b[90m(expires in 15 minutes)\x1b[0m\n   \x1b[94mU2UU-AQ0TL\x1b[0m\n\n"
	url, code := readDevice(strings.NewReader(output))
	if url != "https://auth.openai.com/codex/device" || code != "U2UU-AQ0TL" {
		t.Fatalf("read %q %q", url, code)
	}
	if url, code := readDevice(strings.NewReader("Error: login failed\n")); url != "" || code != "" {
		t.Fatalf("no link or code: %q %q", url, code)
	}
	if url, _ := readDevice(strings.NewReader("Open https://evil.example.com/codex/device\nABCD-EFGHI\n")); url != "" {
		t.Fatalf("only OpenAI's own page is shown: %q", url)
	}
}

func TestTheAccountLanesFileIsTheOperatorsPrivateFile(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, "account-lanes.json")
	data, _ := json.Marshal(FileV1{SchemaVersion: 1, Lanes: []LaneV1{f.codex, f.claude}})
	os.WriteFile(path, data, 0o600)
	if file, err := ReadFile(path); err != nil || len(file.Lanes) != 2 {
		t.Fatalf("read %+v %v", file, err)
	}
	os.Chmod(path, 0o644)
	if _, err := ReadFile(path); err == nil {
		t.Fatal("a file open to others is refused")
	}
	os.Chmod(path, 0o600)
	os.WriteFile(path, []byte(`{"schema_version":1,"lanes":[],"extra":1}`), 0o600)
	if _, err := ReadFile(path); err == nil {
		t.Fatal("an unknown field is refused")
	}
}

func TestACodexDeviceSignInRunsInItsOwnHomeAndStops(t *testing.T) {
	bin := t.TempDir()
	home := t.TempDir()
	script := "#!/bin/sh\n" +
		"[ \"$1 $2\" = \"login --device-auth\" ] || exit 3\n" +
		"[ -z \"$OPENAI_API_KEY\" ] || exit 4\n" +
		"echo \"$CODEX_HOME\" > \"$CODEX_HOME/seen\"\n" +
		"printf '1. Open this link\\n   \\033[94mhttps://auth.openai.com/codex/device\\033[0m\\n2. Enter this one-time code\\n   \\033[94mABCD-EFGHI\\033[0m\\n'\n" +
		"exec sleep 60\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("OPENAI_API_KEY", "sk-never-passed-on")
	login, err := CodexDeviceLogin(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if login.VerificationURL != "https://auth.openai.com/codex/device" || login.UserCode != "ABCD-EFGHI" || time.Until(login.ExpiresAt) < 14*time.Minute {
		t.Fatalf("login %+v", login)
	}
	seen, err := os.ReadFile(filepath.Join(home, "seen"))
	if err != nil || strings.TrimSpace(string(seen)) != home {
		t.Fatalf("Codex runs in the lane's home: %q %v", seen, err)
	}
	select {
	case <-login.Done:
		t.Fatal("the sign-in waits for the administrator")
	case <-time.After(200 * time.Millisecond):
	}
	login.Stop()
	select {
	case <-login.Done:
	case <-time.After(10 * time.Second):
		t.Fatal("stopping ends the sign-in")
	}

	os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\necho 'Error: device sign-in is not enabled'\nexit 1\n"), 0o700)
	if _, err := CodexDeviceLogin(context.Background(), home); err == nil {
		t.Fatal("a sign-in without a link and code fails")
	}
}

//go:build linux

package openmodel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeKey(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "groq-api-key")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// testProvider is groq() with a real key file and a private Codex home.
func testProvider(t *testing.T) Provider {
	t.Helper()
	base := groq()
	provider := *base.Clone()
	provider.KeyFile = writeKey(t, testKey+"\n", 0o600)
	provider.CodexHome = filepath.Join(t.TempDir(), "lanes-groq")
	provider.QueueTimeout = "1s"
	return provider
}

func TestAKeyFileIsReadUnderTheLanesRulesAndNeverShown(t *testing.T) {
	read, err := ReadKey(writeKey(t, testKey+"\r\n", 0o600))
	if err != nil || read.Value() != testKey || len(read.SHA256()) != 64 {
		t.Fatalf("key = %v, %v", read, err)
	}
	for _, shown := range []string{fmt.Sprint(read), fmt.Sprintf("%v %+v %#v %s", read, read, read, read)} {
		if strings.Contains(shown, testKey) {
			t.Fatalf("a printed key shows its value: %s", shown)
		}
	}
	if _, err := ReadKey(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("a missing key file = %v", err)
	}
	refused := map[string]string{}
	refused["open to others"] = writeKey(t, testKey+"\n", 0o644)
	refused["two lines"] = writeKey(t, testKey+"\nsecond-line-value\n", 0o600)
	refused["a space"] = writeKey(t, "gsk key with spaces in it\n", 0o600)
	refused["too short"] = writeKey(t, "short\n", 0o600)
	refused["empty"] = writeKey(t, "", 0o600)
	refused["NAME=value"] = writeKey(t, "GROQ_API_KEY=gsk_value_in_env_form_123\n", 0o600)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(writeKey(t, testKey+"\n", 0o600), link); err != nil {
		t.Fatal(err)
	}
	refused["a link"] = link
	for name, path := range refused {
		_, err := ReadKey(path)
		if err == nil || errors.Is(err, ErrKeyMissing) {
			t.Errorf("%s: %v", name, err)
		} else if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "second-line") {
			t.Errorf("%s: the error shows the file: %v", name, err)
		}
	}
}

func TestTheCodexHomeIsPrivateAndItsConfigurationExact(t *testing.T) {
	root := filepath.Join(t.TempDir(), "groq")
	home := filepath.Join(root, "gpt-oss-120b-abc")
	if err := WriteHome(root, home, []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{root: 0o700, home: 0o700, filepath.Join(home, "config.toml"): 0o600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("%s mode = %v, %v", path, info.Mode(), err)
		}
	}
	if err := WriteHome(root, home, []byte("two\n")); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(home, "config.toml")); string(data) != "two\n" {
		t.Fatalf("config.toml = %q", data)
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 1 {
		t.Fatalf("the home keeps a temporary file: %v", entries)
	}
	if err := WriteHome(root, filepath.Join(t.TempDir(), "elsewhere"), []byte("x")); err == nil {
		t.Fatal("a home outside the provider's folder was written")
	}
	open := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteHome(open, filepath.Join(open, "m"), []byte("x")); err == nil {
		t.Fatal("a provider folder open to others was used")
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(root, linked); err != nil {
		t.Fatal(err)
	}
	if err := WriteHome(linked, filepath.Join(linked, "gpt-oss-120b-abc"), []byte("x")); err == nil {
		t.Fatal("a provider folder that is a link was used")
	}
	if err := os.Remove(filepath.Join(home, "config.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(home, "config.toml")); err != nil {
		t.Fatal(err)
	}
	if err := WriteHome(root, home, []byte("x")); err == nil {
		t.Fatal("a config.toml that is a link was replaced")
	}
}

func TestBuildSlotsCapTheModelAcrossProcessesAndComeBack(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "slots", "gpt-oss-120b")
	first, err := AcquireSlot(context.Background(), dir, 1, time.Now().Add(time.Second), 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if _, err := AcquireSlot(context.Background(), dir, 1, time.Now().Add(150*time.Millisecond), 20*time.Millisecond); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("a second build took the only slot: %v", err)
	}
	if waited := time.Since(began); waited < 100*time.Millisecond {
		t.Fatalf("the second build did not wait: %s", waited)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		first()
	}()
	second, err := AcquireSlot(context.Background(), dir, 1, time.Now().Add(2*time.Second), 10*time.Millisecond)
	if err != nil {
		t.Fatalf("a released slot was not taken: %v", err)
	}
	if err := first(); err != nil {
		t.Fatalf("releasing twice: %v", err)
	}
	two, err := AcquireSlot(context.Background(), dir, 2, time.Now().Add(time.Second), 10*time.Millisecond)
	if err != nil {
		t.Fatalf("a second slot of two was not taken: %v", err)
	}
	second()
	two()
	ctx, cancel := context.WithCancel(context.Background())
	held, _ := AcquireSlot(context.Background(), dir, 1, time.Now().Add(time.Second), 10*time.Millisecond)
	defer held()
	cancel()
	if _, err := AcquireSlot(ctx, dir, 1, time.Now().Add(time.Second), 10*time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled wait = %v", err)
	}
	if _, err := AcquireSlot(context.Background(), dir, 9, time.Now(), 0); err == nil {
		t.Fatal("nine slots were offered")
	}
}

func TestABuildBeginsOnItsProviderWithTheKeyOnlyInItsEnvironment(t *testing.T) {
	provider := testProvider(t)
	start, err := Begin(context.Background(), provider, "gpt-oss-120b", StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer start.Release()
	config, err := os.ReadFile(filepath.Join(start.Home, "config.toml"))
	if err != nil || string(config) != string(CodexConfig(provider, provider.Models[0])) || filepath.Dir(start.Home) != provider.CodexHome {
		t.Fatalf("home %s config %q, %v", start.Home, config, err)
	}
	environment := strings.Join(start.Environment([]string{"OPENAI_API_KEY=sk-subscription", "CODEX_HOME=/home/devagent/.codex", "PATH=/usr/bin"}), "\n")
	if environment != "PATH=/usr/bin\nCODEX_HOME="+start.Home+"\nGROQ_API_KEY="+testKey {
		t.Fatalf("environment = %s", environment)
	}
	if printed := fmt.Sprintf("%v %+v %#v", start, start, start); strings.Contains(printed, testKey) || printed != "open-model start groq/gpt-oss-120b open-model start groq/gpt-oss-120b open-model start groq/gpt-oss-120b" {
		t.Fatalf("a printed start = %s", printed)
	}
	evidence := fmt.Sprint(start.Evidence())
	if strings.Contains(evidence, testKey) || strings.Contains(evidence, provider.KeyFile) || !strings.Contains(evidence, "openai/gpt-oss-120b") || !strings.Contains(evidence, "engine_used:provider") {
		t.Fatalf("evidence = %s", evidence)
	}
	filepath.Walk(provider.CodexHome, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			if data, _ := os.ReadFile(path); strings.Contains(string(data), testKey) {
				t.Errorf("%s holds the key", path)
			}
		}
		return nil
	})

	// The only slot is held: the next build waits, then is unavailable, before anything starts.
	_, err = Begin(context.Background(), provider, "gpt-oss-120b", StartOptions{SlotPoll: 50 * time.Millisecond})
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != ReasonBusy || !strings.HasPrefix(err.Error(), "OPEN_MODEL_ENGINE_UNAVAILABLE: groq/gpt-oss-120b: ") {
		t.Fatalf("a build past the cap = %v", err)
	}
	if err := start.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := Begin(context.Background(), provider, "gpt-oss-120b", StartOptions{})
	if err != nil {
		t.Fatalf("a released slot was not reused: %v", err)
	}
	again.Release()
}

func TestABuildThatCannotBeginReleasesItsSlotAndSaysWhy(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(status)
		fmt.Fprint(w, `{"data":[{"id":"openai/gpt-oss-120b"}]}`)
	}))
	defer server.Close()
	provider := testProvider(t)
	provider.BaseURL, provider.Preflight = server.URL, true
	options := StartOptions{Backoff: Backoff{Initial: 20 * time.Millisecond, Max: 40 * time.Millisecond}, SlotPoll: 10 * time.Millisecond}

	start, err := Begin(context.Background(), provider, "gpt-oss-120b", options)
	if err != nil || start.Preflight == nil || start.Preflight.Status != CheckOK {
		t.Fatalf("a preflight that answers = %+v, %v", start, err)
	}
	start.Release()

	status = http.StatusServiceUnavailable
	_, err = Begin(context.Background(), provider, "gpt-oss-120b", options)
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != ReasonUnavailable || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("a provider that keeps refusing = %v", err)
	}

	status = http.StatusOK
	provider.Models[1].ID = "mistralai/not-served"
	if _, err := Begin(context.Background(), provider, "devstral", options); err == nil || errors.As(err, &unavailable) || !strings.Contains(err.Error(), "OPEN_MODEL_ENGINE_MODEL_MISSING") {
		t.Fatalf("a model the provider does not list = %v", err)
	}
	refused := provider
	refused.KeyFile = writeKey(t, "gsk_another_key_the_provider_refuses\n", 0o600)
	if _, err := Begin(context.Background(), refused, "gpt-oss-120b", options); err == nil || errors.As(err, &unavailable) || !strings.Contains(err.Error(), "OPEN_MODEL_ENGINE_KEY_REFUSED") {
		t.Fatalf("a refused key = %v", err)
	}
	missing := provider
	missing.KeyFile = filepath.Join(t.TempDir(), "groq-api-key")
	if _, err := Begin(context.Background(), missing, "gpt-oss-120b", options); !errors.Is(err, ErrKeyMissing) || errors.As(err, &unavailable) {
		t.Fatalf("a missing key = %v", err)
	}
	if _, err := Begin(context.Background(), provider, "unknown", options); err == nil {
		t.Fatal("an unknown model began")
	}
	// Every failure above gave its slot back.
	last, err := Begin(context.Background(), provider, "gpt-oss-120b", options)
	if err != nil {
		t.Fatalf("a slot was kept by a build that never began: %v", err)
	}
	last.Release()
}

// relayProvider is testProvider on the relay to a stand-in for Together, with
// gpt-oss-120b's compat list and catalog entry.
func relayProvider(t *testing.T, baseURL string) Provider {
	t.Helper()
	provider := testProvider(t)
	provider.ID, provider.BaseURL, provider.Relay = "together", baseURL, true
	provider.Models[0].Compat = []string{CompatDropNullReasoningContent, CompatMessageBeforeCalls, CompatOutputAfterCall}
	provider.Models[0].CodexCatalog = &CodexCatalog{ApplyPatch: true}
	if err := provider.Validate(); err != nil {
		t.Fatal(err)
	}
	return provider
}

// TestARelayBuildRunsThroughItsOwnRelayFromItsSlotsHome: a build on the relay
// writes the model's catalog and a config.toml whose base_url is its relay,
// in its slot's home; the relay serves until the build gives its slot back;
// the next build in that slot keeps the home with its own relay's port.
func TestARelayBuildRunsThroughItsOwnRelayFromItsSlotsHome(t *testing.T) {
	fake := &fakeTogether{}
	server := httptest.NewServer(fake)
	defer server.Close()
	provider := relayProvider(t, server.URL+"/v1")
	start, err := Begin(context.Background(), provider, "gpt-oss-120b", StartOptions{Run: "run-7"})
	if err != nil {
		t.Fatal(err)
	}
	defer start.Release()
	model := provider.Models[0]
	home, catalog := homeOf(provider, model, 1)
	if start.Home != home || !strings.HasPrefix(filepath.Base(home), "gpt-oss-120b-") || !strings.HasSuffix(home, "-1") || len(filepath.Base(home)) != len("gpt-oss-120b-")+12+2 || start.Slot != 1 {
		t.Fatalf("home %s (want %s), slot %d", start.Home, home, start.Slot)
	}
	config, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	written, _ := os.ReadFile(filepath.Join(home, CatalogFile))
	if string(config) != string(configOf(provider, model, home, start.Relay.URL(), catalog)) || string(written) != string(catalog) {
		t.Fatalf("config.toml =\n%s", config)
	}
	for _, line := range []string{
		`base_url = "http://` + start.Relay.Address() + `/v1"`,
		`model_catalog_json = "` + filepath.Join(home, CatalogFile) + `"`,
		"# base_url is this build's ABCP relay on 127.0.0.1, to " + server.URL + "/v1 only; its fixes: drop_null_reasoning_content, message_before_calls, output_after_call.",
	} {
		if !strings.Contains(string(config), line+"\n") {
			t.Errorf("config.toml lacks %s", line)
		}
	}
	for _, name := range []string{"config.toml", CatalogFile, RelayLogFile} {
		if info, err := os.Lstat(filepath.Join(home, name)); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", name, info, err)
		}
	}

	// Codex's request reaches the provider through the relay, fixed.
	request := codexRequest("openai/gpt-oss-120b", withInput(codexOpening(), reasoningItem("rs_1"), call("call_1"), said("m", "x"), output("call_1")), nil)
	response, err := http.Post(start.Relay.URL()+"/responses", "application/json", strings.NewReader(string(request)))
	if err != nil || response.StatusCode != http.StatusOK || len(fake.requests) != 1 || togetherRefusal(fake.requests[0].body) != "" {
		t.Fatalf("through the relay: %v %v", response, err)
	}
	response.Body.Close()
	var logged []byte
	for deadline := time.Now().Add(2 * time.Second); !strings.Contains(string(logged), "\n") && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		logged, _ = os.ReadFile(filepath.Join(home, RelayLogFile))
	}
	if !strings.Contains(string(logged), " abcp open-model relay run=run-7 together/gpt-oss-120b POST /v1/responses model=openai/gpt-oss-120b status=200 fixes=drop_null_reasoning_content:1,message_before_calls:1 ") {
		t.Fatalf("relay.log = %s", logged)
	}
	evidence := start.Evidence()
	relay, _ := evidence["relay"].(map[string]any)
	codexCatalog, _ := evidence["codex_catalog"].(map[string]any)
	if relay["address"] != start.Relay.Address() || relay["upstream"] != server.URL+"/v1" || fmt.Sprint(relay["compat"]) != fmt.Sprint(model.Compat) ||
		relay["log"] != filepath.Join(home, RelayLogFile) || codexCatalog["apply_patch"] != true || codexCatalog["tool_output_tokens"] != 10000 ||
		len(fmt.Sprint(codexCatalog["sha256"])) != 64 || evidence["slot"] != 1 || evidence["codex_home"] != home {
		t.Fatalf("evidence = %v", evidence)
	}
	if strings.Contains(fmt.Sprint(evidence), testKey) || strings.Contains(fmt.Sprint(evidence), provider.KeyFile) {
		t.Fatal("the evidence names the key or its file")
	}

	address := start.Relay.Address()
	if err := start.Release(); err != nil {
		t.Fatal(err)
	}
	if connection, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		connection.Close()
		t.Fatal("the relay still answers after the build gave its slot back")
	}
	again, err := Begin(context.Background(), provider, "gpt-oss-120b", StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Release()
	rewritten, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	if again.Home != home || !strings.Contains(string(rewritten), `base_url = "http://`+again.Relay.Address()+`/v1"`) {
		t.Fatalf("the next build in the slot: home %s, config\n%s", again.Home, rewritten)
	}
	filepath.Walk(provider.CodexHome, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			if data, _ := os.ReadFile(path); strings.Contains(string(data), testKey) {
				t.Errorf("%s holds the key", path)
			}
		}
		return nil
	})
}

func TestEachSlotAndEachConfigurationOfARelayModelHasAHomeOfItsOwn(t *testing.T) {
	provider := relayProvider(t, "https://api.together.xyz/v1")
	model := provider.Models[0]
	one, _ := homeOf(provider, model, 1)
	two, _ := homeOf(provider, model, 2)
	if one == two || strings.TrimSuffix(one, "-1") != strings.TrimSuffix(two, "-2") {
		t.Fatalf("slots 1 and 2: %s, %s", one, two)
	}
	changed := model
	changed.Compat = []string{CompatDropNullReasoningContent}
	tokens := model
	tokens.CodexCatalog = &CodexCatalog{ApplyPatch: true, ToolOutputTokens: 20000}
	for name, other := range map[string]Model{"another compat list": changed, "another catalog entry": tokens} {
		if home, _ := homeOf(provider, other, 1); home == one {
			t.Errorf("%s shares the home", name)
		}
	}
	// Without the relay, a catalog entry still gets a home of its own
	// configuration and catalog, with no slot in its name; without either,
	// the home is #181's.
	direct := provider
	direct.Relay = false
	direct.Models = []Model{{Name: model.Name, ID: model.ID, Origin: model.Origin, ContextWindow: model.ContextWindow, CodexCatalog: model.CodexCatalog}}
	home, catalog := homeOf(direct, direct.Models[0], 1)
	config := string(configOf(direct, direct.Models[0], home, direct.BaseURL, catalog))
	if strings.HasSuffix(home, "-1") || catalog == nil || !strings.Contains(config, `base_url = "https://api.together.xyz/v1"`) ||
		!strings.Contains(config, `model_catalog_json = "`+home+`/model-catalog.json"`) || strings.Contains(config, "relay") {
		t.Fatalf("a direct build with a catalog: %s\n%s", home, config)
	}
	direct.Models[0].CodexCatalog = nil
	if home, catalog := homeOf(direct, direct.Models[0], 3); home != HomeFor(direct, direct.Models[0], CodexConfig(direct, direct.Models[0])) || catalog != nil {
		t.Fatalf("a plain build's home moved: %s", home)
	}
}

func TestTheRelaysLogMovesAsideWhenItGrows(t *testing.T) {
	home := t.TempDir()
	log, err := openRelayLog(home)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	line := []byte(strings.Repeat("x", 1023) + "\n")
	for written := 0; written <= MaxRelayLogBytes; written += len(line) {
		if _, err := log.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	old, errOld := os.Stat(filepath.Join(home, RelayLogFile+".1"))
	current, errCurrent := os.Stat(filepath.Join(home, RelayLogFile))
	if errOld != nil || errCurrent != nil || old.Size() > MaxRelayLogBytes || current.Size() == 0 || current.Mode().Perm() != 0o600 {
		t.Fatalf("relay.log.1 %v %v, relay.log %v %v", old, errOld, current, errCurrent)
	}
	link := filepath.Join(t.TempDir(), "linked")
	os.Symlink(filepath.Join(home, RelayLogFile), filepath.Join(filepath.Dir(link), RelayLogFile))
	if _, err := openRelayLog(filepath.Dir(link)); err == nil {
		t.Fatal("the relay wrote its log through a link")
	}
}

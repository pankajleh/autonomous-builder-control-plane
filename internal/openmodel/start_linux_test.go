//go:build linux

package openmodel

import (
	"context"
	"errors"
	"fmt"
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

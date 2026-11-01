//go:build linux

package run

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/openmodel"
)

const openModelKey = "gsk_open_model_key_never_recorded_8Kd2"

// openModelFixture is a run on an open-model engine: Groq's gpt-oss-120b and a
// model that takes no reasoning level, a key file and a private Codex home.
func openModelFixture(t *testing.T, script string, edit func(*openmodel.Provider)) (runFixture, *openmodel.Provider) {
	t.Helper()
	keyFile := filepath.Join(t.TempDir(), "groq-api-key")
	writeTestFile(t, keyFile, []byte(openModelKey+"\n"), 0o600)
	provider := &openmodel.Provider{
		ID: "groq", BaseURL: "https://api.groq.com/openai/v1", KeyFile: keyFile, WireAPI: "responses",
		CodexHome: filepath.Join(t.TempDir(), "lanes-groq"), QueueTimeout: "1s",
		Models: []openmodel.Model{
			{Name: "gpt-oss-120b", ID: "openai/gpt-oss-120b", Origin: "US", ContextWindow: 131072, Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium"},
			{Name: "devstral", ID: "mistralai/Devstral-Small-2", Origin: "FR", ContextWindow: 131072},
		},
	}
	if edit != nil {
		edit(provider)
	}
	fixture := newRunFixtureWithScript(t, script, authority.WorktreePolicy{}, commandPath(t, "true"))
	manifest := fixture.authority.Manifest()
	manifest.Executor = authority.ExecutorPolicy{Executor: "codex", TaskModel: "gpt-oss-120b", TaskEffort: "high", ReviewEffort: "high", Provider: provider}
	if provider.Models[0].Name != "gpt-oss-120b" {
		manifest.Executor.TaskModel, manifest.Executor.TaskEffort, manifest.Executor.ReviewEffort = provider.Models[0].Name, "", ""
	}
	fixture.authority = fixture.admit(t, manifest)
	return fixture, provider
}

const commitCandidate = `printf 'candidate\n' > candidate.txt
git add candidate.txt || exit 60
git commit -qm 'candidate implementation' || exit 61
`

// noKeyRecorded fails when any file under the roots holds the key.
func noKeyRecorded(t *testing.T, roots ...string) {
	t.Helper()
	for _, root := range roots {
		filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.Mode().IsRegular() {
				if data, _ := os.ReadFile(path); strings.Contains(string(data), openModelKey) {
					t.Errorf("%s records the provider's key", path)
				}
			}
			return nil
		})
	}
}

func TestRunnerRunsAnOpenModelEngineOnItsProviderWithTheKeyOnlyInCodexsEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "subscription-key")
	t.Setenv("CODEX_HOME", "/home/devagent/.codex")
	script := `#!/bin/sh
case " $* " in *" --codex "*) ;; *) exit 41 ;; esac
case " $* " in *" --task-model openai/gpt-oss-120b:high "*) ;; *) exit 42 ;; esac
case " $* " in *" --review-model openai/gpt-oss-120b:high "*) ;; *) exit 43 ;; esac
if [ "$GROQ_API_KEY" != "` + openModelKey + `" ]; then exit 44; fi
if [ -n "$OPENAI_API_KEY" ]; then exit 45; fi
grep -qx 'model_provider = "abcp-groq"' "$CODEX_HOME/config.toml" || exit 46
grep -qx 'env_key = "GROQ_API_KEY"' "$CODEX_HOME/config.toml" || exit 47
grep -qx 'model_context_window = 131072' "$CODEX_HOME/config.toml" || exit 48
` + commitCandidate
	fixture, provider := openModelFixture(t, script, nil)
	result := fixture.execute(t)
	if !result.Accepted() {
		t.Fatalf("a run on its open-model engine was not accepted: %#v", result)
	}
	ledgerData, err := os.ReadFile(fixture.ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"open_model":{`, `"engine_used":"provider"`, `"provider":"groq"`, `"model_id":"openai/gpt-oss-120b"`, `"origin":"US"`,
		`"key_env":"GROQ_API_KEY"`, `"codex_home":"` + provider.CodexHome + `/gpt-oss-120b-`, `"max_concurrent_builds":1`} {
		if !strings.Contains(string(ledgerData), want) {
			t.Errorf("the run's start does not record %s", want)
		}
	}
	if strings.Contains(string(ledgerData), provider.KeyFile) {
		t.Error("the run's events name the key file")
	}
	noKeyRecorded(t, filepath.Dir(fixture.ledgerPath), fixture.evidence, provider.CodexHome, fixture.authority.Repository().Path)
	// The slot came back when Ralphex exited.
	model, _ := provider.Model("gpt-oss-120b")
	release, err := openmodel.AcquireSlot(context.Background(), openmodel.SlotDir(*provider, model), 1, time.Now(), 0)
	if err != nil {
		t.Fatalf("the run kept its build slot: %v", err)
	}
	release()
}

func TestRunnerRunsAModelAtItsOwnDefaultLevelWhenTheManifestNamesNone(t *testing.T) {
	script := `#!/bin/sh
case " $* " in *" --task-model openai/gpt-oss-120b:medium "*) ;; *) exit 41 ;; esac
case " $* " in *" --review-model openai/gpt-oss-120b:medium "*) ;; *) exit 42 ;; esac
` + commitCandidate
	fixture, _ := openModelFixture(t, script, nil)
	manifest := fixture.authority.Manifest()
	manifest.Executor.TaskEffort, manifest.Executor.ReviewEffort = "", ""
	fixture.authority = fixture.admit(t, manifest)
	if result := fixture.execute(t); !result.Accepted() {
		t.Fatalf("a run at the model's default level was not accepted: %#v", result)
	}
}

func TestRunnerSendsNoReasoningLevelToAModelThatTakesNone(t *testing.T) {
	script := `#!/bin/sh
case " $* " in *" --task-model mistralai/Devstral-Small-2 "*) ;; *) exit 41 ;; esac
case " $* " in *"mistralai/Devstral-Small-2:"*) exit 42 ;; esac
config=""; previous=""
for argument in "$@"; do
  if [ "$previous" = "--config-dir" ]; then config="$argument"; fi
  previous="$argument"
done
grep -qx 'codex_reasoning_effort =' "$config/config" || exit 43
` + commitCandidate
	fixture, _ := openModelFixture(t, script, func(p *openmodel.Provider) { p.Models[0], p.Models[1] = p.Models[1], p.Models[0] })
	if result := fixture.execute(t); !result.Accepted() {
		t.Fatalf("a run on a model without levels was not accepted: %#v", result)
	}
}

func TestRunnerStartsOnTheFallbackOnlyWhenTheProviderCannotTakeTheBuildBeforeItStarts(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "subscription-key")
	t.Setenv("CODEX_HOME", "/home/devagent/.codex")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	script := `#!/bin/sh
case " $* " in *" --task-model gpt-6-astra:xhigh "*) ;; *) exit 41 ;; esac
case " $* " in *" --review-model gpt-6-astra:xhigh "*) ;; *) exit 42 ;; esac
if [ -n "$GROQ_API_KEY" ]; then exit 43; fi
if [ "$OPENAI_API_KEY" != "subscription-key" ] || [ "$CODEX_HOME" != "/home/devagent/.codex" ]; then exit 44; fi
` + commitCandidate
	fixture, provider := openModelFixture(t, script, func(p *openmodel.Provider) {
		p.BaseURL, p.Preflight = server.URL, true
		p.OnUnavailable, p.Fallback = openmodel.UnavailableFallback, &openmodel.Fallback{TaskModel: "gpt-6-astra", TaskEffort: "xhigh", ReviewEffort: "xhigh"}
	})
	runner := fixture.runner(t)
	runner.openModelOptions = openmodel.StartOptions{Backoff: openmodel.Backoff{Initial: 20 * time.Millisecond, Max: 50 * time.Millisecond}}
	result, err := runner.Run(context.Background())
	if err != nil || !result.Accepted() {
		t.Fatalf("a run on its fallback was not accepted: %#v, %v", result, err)
	}
	ledgerData, _ := os.ReadFile(fixture.ledgerPath)
	for _, want := range []string{`"engine_used":"fallback"`, `"fallback_reason":"unavailable"`, `"access":"sign-in"`, `"task_model":"gpt-6-astra"`} {
		if !strings.Contains(string(ledgerData), want) {
			t.Errorf("the run's start does not record %s", want)
		}
	}
	noKeyRecorded(t, filepath.Dir(fixture.ledgerPath), fixture.evidence, provider.CodexHome)

	// Without a fallback the same provider fails the build before anything starts.
	marker := filepath.Join(t.TempDir(), "engine-started")
	waiting, _ := openModelFixture(t, "#!/bin/sh\ntouch "+marker+"\nexit 0\n", func(p *openmodel.Provider) { p.BaseURL, p.Preflight = server.URL, true })
	runner = waiting.runner(t)
	runner.openModelOptions = openmodel.StartOptions{Backoff: openmodel.Backoff{Initial: 20 * time.Millisecond, Max: 50 * time.Millisecond}}
	result, err = runner.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "OPEN_MODEL_ENGINE_UNAVAILABLE: groq/gpt-oss-120b") || result.State != domain.StateFailed {
		t.Fatalf("a provider that keeps refusing = %s, %v", result.State, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the engine started")
	}
}

func TestRunnerNeverFallsBackFromAKeyThatIsNotThere(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "engine-started")
	fixture, _ := openModelFixture(t, "#!/bin/sh\ntouch "+marker+"\nexit 0\n", func(p *openmodel.Provider) {
		p.KeyFile = filepath.Join(filepath.Dir(p.KeyFile), "not-yet-placed")
		p.OnUnavailable, p.Fallback = openmodel.UnavailableFallback, &openmodel.Fallback{TaskModel: "gpt-6-astra", TaskEffort: "xhigh", ReviewEffort: "xhigh"}
	})
	result, err := fixture.runner(t).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "OPEN_MODEL_ENGINE_KEY") || result.State != domain.StateFailed {
		t.Fatalf("a missing key = %s, %v", result.State, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the engine started on the fallback")
	}
	if ledgerData, _ := os.ReadFile(fixture.ledgerPath); strings.Contains(string(ledgerData), `"engine_used"`) {
		t.Fatalf("a build that never started records an engine: %s", fmt.Sprint(len(ledgerData)))
	}
}

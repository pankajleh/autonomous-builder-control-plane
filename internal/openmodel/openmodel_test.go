package openmodel

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func groq() Provider {
	return Provider{
		ID: "groq", BaseURL: "https://api.groq.com/openai/v1", KeyFile: "/home/devagent/runtime/secrets/groq-api-key",
		WireAPI: WireResponses, CodexHome: "/home/devagent/.codex/lanes/groq",
		Models: []Model{
			{Name: "gpt-oss-120b", ID: "openai/gpt-oss-120b", Origin: "US", ContextWindow: 131072, Efforts: []string{"low", "medium", "high"}, DefaultEffort: "high"},
			{Name: "devstral", ID: "mistralai/Devstral-Small", Origin: "FR", ContextWindow: 131072, MaxConcurrentBuilds: 2},
		},
	}
}

func TestAProviderAsAManifestGivesItIsAccepted(t *testing.T) {
	if err := groq().Validate(); err != nil {
		t.Fatalf("a valid provider was refused: %v", err)
	}
	fallback := groq()
	fallback.OnUnavailable, fallback.Fallback = UnavailableFallback, &Fallback{TaskModel: "gpt-6-astra", TaskEffort: "xhigh", ReviewEffort: "xhigh"}
	fallback.QueueTimeout, fallback.Preflight, fallback.RequestMaxRetries = "5m", true, 12
	if err := fallback.Validate(); err != nil {
		t.Fatalf("a provider with a fallback was refused: %v", err)
	}
	loopback := groq()
	loopback.BaseURL = "http://127.0.0.1:4000/v1"
	if err := loopback.Validate(); err != nil {
		t.Fatalf("a loopback bridge was refused: %v", err)
	}
}

func TestAProviderCodexCannotUseOrThatCouldLeakIsRefused(t *testing.T) {
	cases := map[string]func(*Provider){
		"chat wire API":            func(p *Provider) { p.WireAPI = "chat" },
		"unknown wire API":         func(p *Provider) { p.WireAPI = "" },
		"plain http to another":    func(p *Provider) { p.BaseURL = "http://api.groq.com/openai/v1" },
		"credentials in the URL":   func(p *Provider) { p.BaseURL = "https://user:pass@api.groq.com/openai/v1" },
		"a query in the URL":       func(p *Provider) { p.BaseURL = "https://api.groq.com/openai/v1?key=x" },
		"a trailing slash":         func(p *Provider) { p.BaseURL = "https://api.groq.com/openai/v1/" },
		"a quote in the URL":       func(p *Provider) { p.BaseURL = `https://api.groq.com/"v1` },
		"an upper-case id":         func(p *Provider) { p.ID = "Groq" },
		"a relative key file":      func(p *Provider) { p.KeyFile = "secrets/groq-api-key" },
		"an unclean codex home":    func(p *Provider) { p.CodexHome = "/home/devagent/.codex/../groq" },
		"the root as codex home":   func(p *Provider) { p.CodexHome = "/" },
		"no models":                func(p *Provider) { p.Models = nil },
		"a model id with a colon":  func(p *Provider) { p.Models[0].ID = "openai/gpt-oss-120b:high" },
		"a model id with a quote":  func(p *Provider) { p.Models[0].ID = `openai/gpt"oss` },
		"an upper-case model name": func(p *Provider) { p.Models[0].Name = "GPT-OSS" },
		"a repeated model name":    func(p *Provider) { p.Models[1].Name = p.Models[0].Name },
		"a repeated model id":      func(p *Provider) { p.Models[1].ID = p.Models[0].ID },
		"no origin":                func(p *Provider) { p.Models[0].Origin = "" },
		"a tiny context":           func(p *Provider) { p.Models[0].ContextWindow = 4096 },
		"compaction past the end":  func(p *Provider) { p.Models[0].AutoCompactTokenLimit = 131072 },
		"an unknown level":         func(p *Provider) { p.Models[0].Efforts = []string{"max"} },
		"a repeated level":         func(p *Provider) { p.Models[0].Efforts = []string{"high", "high"} },
		"nine builds at once":      func(p *Provider) { p.Models[0].MaxConcurrentBuilds = 9 },
		"a bad queue timeout":      func(p *Provider) { p.QueueTimeout = "3h" },
		"a fallback without policy": func(p *Provider) {
			p.Fallback = &Fallback{TaskModel: "gpt-6-astra", TaskEffort: "xhigh", ReviewEffort: "xhigh"}
		},
		"fallback policy, no model": func(p *Provider) { p.OnUnavailable = UnavailableFallback },
		"a fallback below xhigh": func(p *Provider) {
			p.OnUnavailable, p.Fallback = UnavailableFallback, &Fallback{TaskModel: "gpt-6-astra", TaskEffort: "high", ReviewEffort: "xhigh"}
		},
		"an unknown policy":   func(p *Provider) { p.OnUnavailable = "switch" },
		"too many retries":    func(p *Provider) { p.StreamMaxRetries = 51 },
		"negative retries":    func(p *Provider) { p.RequestMaxRetries = -1 },
		"a model name in TOM": func(p *Provider) { p.Models[0].Name = "gpt\"oss" },
	}
	for name, mutate := range cases {
		base := groq()
		provider := base.Clone()
		mutate(provider)
		if err := provider.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	chat := groq()
	chat.WireAPI = "chat"
	if err := chat.Validate(); err == nil || !strings.Contains(err.Error(), "Responses") {
		t.Fatalf("the chat refusal does not say why: %v", err)
	}
}

func TestTheCodexConfigurationNamesTheProviderTheModelAndKeepsTheKeyFromCommands(t *testing.T) {
	provider := groq()
	model, _ := provider.Model("gpt-oss-120b")
	got := string(CodexConfig(provider, model))
	want := `# Written by ABCP for the open-model engine groq/gpt-oss-120b (Repo C design note OPEN_MODEL_ENGINES).
# ABCP rewrites this file before each build on it; edit the profile's manifest instead.
model = "openai/gpt-oss-120b"
model_provider = "abcp-groq"
model_context_window = 131072
model_auto_compact_token_limit = 104857
model_reasoning_summary = "none"
web_search = "disabled"

[model_providers.abcp-groq]
name = "groq (ABCP open-model engine)"
base_url = "https://api.groq.com/openai/v1"
env_key = "GROQ_API_KEY"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
request_max_retries = 8
stream_max_retries = 8

[model_providers.abcp-groq.capabilities]
remote_compaction = "unsupported"

[shell_environment_policy]
ignore_default_excludes = false
exclude = ["GROQ_API_KEY"]
`
	if got != want {
		t.Fatalf("config.toml =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(got, provider.KeyFile) {
		t.Fatal("the configuration names the key file")
	}
	provider.RequestMaxRetries, provider.StreamMaxRetries = 3, 4
	model.AutoCompactTokenLimit = 90000
	other := string(CodexConfig(provider, model))
	for _, line := range []string{"request_max_retries = 3\n", "stream_max_retries = 4\n", "model_auto_compact_token_limit = 90000\n"} {
		if !strings.Contains(other, line) {
			t.Errorf("the configuration lacks %q", line)
		}
	}
}

func TestEachConfigurationHasAHomeOfItsOwnBesideTheProvidersOthers(t *testing.T) {
	provider := groq()
	model, _ := provider.Model("gpt-oss-120b")
	config := CodexConfig(provider, model)
	home := HomeFor(provider, model, config)
	if !strings.HasPrefix(home, "/home/devagent/.codex/lanes/groq/gpt-oss-120b-") || len(home) != len("/home/devagent/.codex/lanes/groq/gpt-oss-120b-")+12 {
		t.Fatalf("home = %s", home)
	}
	if HomeFor(provider, model, config) != home {
		t.Fatal("the same configuration answers another home")
	}
	provider.StreamMaxRetries = 3
	if HomeFor(provider, model, CodexConfig(provider, model)) == home {
		t.Fatal("a changed configuration shares the old home")
	}
	if SlotDir(provider, model) != "/home/devagent/.codex/lanes/groq/slots/gpt-oss-120b" {
		t.Fatalf("slots = %s", SlotDir(provider, model))
	}
}

func TestTheBuildsEnvironmentLosesTheInheritedOpenAISettingsAndGainsTheProviders(t *testing.T) {
	base := []string{"HOME=/home/devagent", "PATH=/usr/bin", "CODEX_HOME=/home/devagent/.codex", "OPENAI_API_KEY=sk-subscription", "OPENAI_BASE_URL=https://api.openai.com/v1", "AZURE_OPENAI_API_KEY=a", "ABCP_CONTEXT_CAPSULE_PATH=/c"}
	got := Environment(base, map[string]string{"GROQ_API_KEY": "gsk-key", "CODEX_HOME": "/lanes/groq/gpt-oss-120b-abc"})
	want := []string{"HOME=/home/devagent", "PATH=/usr/bin", "ABCP_CONTEXT_CAPSULE_PATH=/c", "CODEX_HOME=/lanes/groq/gpt-oss-120b-abc", "GROQ_API_KEY=gsk-key"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("environment = %v", got)
	}
}

func TestNamesAndDefaults(t *testing.T) {
	provider := groq()
	if provider.EnvKey() != "GROQ_API_KEY" || (Provider{ID: "together-us"}).EnvKey() != "TOGETHER_US_API_KEY" || provider.CodexProviderID() != "abcp-groq" {
		t.Fatalf("names = %s %s", provider.EnvKey(), provider.CodexProviderID())
	}
	if provider.StartTimeout() != DefaultQueueTimeout {
		t.Fatalf("default start timeout = %s", provider.StartTimeout())
	}
	provider.QueueTimeout = "90s"
	if provider.StartTimeout() != 90*time.Second {
		t.Fatalf("start timeout = %s", provider.StartTimeout())
	}
	reasoning, plain := provider.Models[0], provider.Models[1]
	if reasoning.Slots() != 1 || plain.Slots() != 2 || reasoning.CompactAt() != 104857 {
		t.Fatalf("slots %d %d, compaction %d", reasoning.Slots(), plain.Slots(), reasoning.CompactAt())
	}
	if !reasoning.Accepts("high") || reasoning.Accepts("xhigh") || !reasoning.Accepts("") || !plain.Accepts("") || plain.Accepts("high") {
		t.Fatal("levels a model accepts are wrong")
	}
	if reasoning.Effort("") != "high" || reasoning.Effort("low") != "low" || plain.Effort("") != "" {
		t.Fatal("the level a build sends is wrong")
	}
	if _, ok := provider.Model("missing"); ok {
		t.Fatal("an unknown model was found")
	}
}

func TestACloneSharesNothing(t *testing.T) {
	provider := groq()
	provider.OnUnavailable, provider.Fallback = UnavailableFallback, &Fallback{TaskModel: "gpt-6-astra", TaskEffort: "xhigh", ReviewEffort: "xhigh"}
	clone := provider.Clone()
	clone.Models[0].Efforts[0] = "xhigh"
	clone.Models[1].ID = "changed"
	clone.Fallback.TaskModel = "changed"
	if provider.Models[0].Efforts[0] != "low" || provider.Models[1].ID == "changed" || provider.Fallback.TaskModel == "changed" {
		t.Fatal("a clone shares the original's lists")
	}
	var none *Provider
	if none.Clone() != nil {
		t.Fatal("no provider clones to one")
	}
}

func TestModelListsOfBothShapesAreRead(t *testing.T) {
	groqShape := `{"object":"list","data":[{"id":"openai/gpt-oss-120b","object":"model"},{"id":"openai/gpt-oss-20b"}]}`
	togetherShape := `[{"id":"zai-org/GLM-5.3","type":"chat"},{"id":"openai/gpt-oss-120b"}]`
	for shape, want := range map[string]string{groqShape: "[openai/gpt-oss-120b openai/gpt-oss-20b]", togetherShape: "[zai-org/GLM-5.3 openai/gpt-oss-120b]"} {
		ids, err := ModelIDs([]byte(shape))
		if err != nil || fmt.Sprint(ids) != want {
			t.Errorf("ids = %v, %v", ids, err)
		}
	}
	for _, odd := range []string{`{"models":[]}`, `not json`, `[1,2]`} {
		if _, err := ModelIDs([]byte(odd)); err == nil {
			t.Errorf("%s was read", odd)
		}
	}
}

func TestRetryAfterIsReadInSecondsOrAsADateAndBounded(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              0,
		"7":                             7 * time.Second,
		"0.5":                           500 * time.Millisecond,
		"-3":                            0,
		"86400":                         MaxRetryAfter,
		"Sun, 01 Nov 2026 12:00:30 GMT": 30 * time.Second,
		"Sun, 01 Nov 2026 11:00:00 GMT": 0,
		"soon":                          0,
	}
	for header, want := range cases {
		if got := retryAfter(header, now); got != want {
			t.Errorf("Retry-After %q = %s, want %s", header, got, want)
		}
	}
}

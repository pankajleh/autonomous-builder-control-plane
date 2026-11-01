//go:build linux

package runadmission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/openmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

const providerKey = "tgp_v1_provider_key_never_shown_3fZ"

// togetherProvider is Together with three trial models; its key file is not
// placed until a test writes it.
func togetherProvider(t *testing.T) *openmodel.Provider {
	t.Helper()
	return &openmodel.Provider{
		ID: "together", BaseURL: "https://api.together.xyz/v1", KeyFile: filepath.Join(t.TempDir(), "together-api-key"),
		WireAPI: "responses", CodexHome: filepath.Join(t.TempDir(), "lanes-together"),
		Models: []openmodel.Model{
			{Name: "gpt-oss-120b", ID: "openai/gpt-oss-120b", Origin: "US", ContextWindow: 131072, Efforts: []string{"low", "medium", "high"}},
			{Name: "glm-5.3", ID: "zai-org/GLM-5.3", Origin: "CN", ContextWindow: 202752, Efforts: []string{"high"}, MaxConcurrentBuilds: 2},
			{Name: "devstral", ID: "mistralai/Devstral-Small-2", Origin: "FR", ContextWindow: 131072},
		},
	}
}

// openModelTemplate writes the fixture's manifest template on an open-model
// engine and returns its path.
func openModelTemplate(t *testing.T, root string, provider *openmodel.Provider) string {
	return openModelTemplateAt(t, root, provider, "high")
}

// openModelTemplateAt is openModelTemplate with the manifest's level, empty
// for each model's own default.
func openModelTemplateAt(t *testing.T, root string, provider *openmodel.Provider, effort string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "manifest-template.json"))
	if err != nil {
		t.Fatal(err)
	}
	var template authority.Manifest
	if err := json.Unmarshal(data, &template); err != nil {
		t.Fatal(err)
	}
	template.Executor = authority.ExecutorPolicy{Executor: "codex", TaskModel: "gpt-oss-120b", TaskEffort: effort, ReviewEffort: effort, Provider: provider}
	path := filepath.Join(root, "manifest-template-together-"+effort+".json")
	writeProtectedJSON(t, path, template)
	return path
}

func TestAnOpenModelProfileOffersItsProvidersModelsAtTheLevelsTheyAccept(t *testing.T) {
	root, repository, head := makeRepository(t, true)
	config, _ := writeAdmissionConfiguration(t, root, repository, head)
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	var profiles ProfileFileV1
	if err := json.Unmarshal(data, &profiles); err != nil {
		t.Fatal(err)
	}
	open := profiles.Profiles[0]
	open.ManifestTemplatePath = openModelTemplate(t, root, togetherProvider(t))
	for name, test := range map[string]struct {
		choices *ExecutorChoicesV1
		valid   bool
	}{
		"no choices":                      {nil, true},
		"its reasoning models at high":    {&ExecutorChoicesV1{Models: []string{"gpt-oss-120b", "glm-5.3"}, Efforts: []string{"high"}}, true},
		"a level below xhigh alone":       {&ExecutorChoicesV1{Efforts: []string{"medium", "high"}}, true},
		"a model of another provider":     {&ExecutorChoicesV1{Models: []string{"gpt-6-astra"}}, false},
		"a level one model refuses":       {&ExecutorChoicesV1{Models: []string{"gpt-oss-120b", "glm-5.3"}, Efforts: []string{"medium"}}, false},
		"a model without the manifest's":  {&ExecutorChoicesV1{Models: []string{"gpt-oss-120b", "devstral"}}, false},
		"a level the model does not take": {&ExecutorChoicesV1{Efforts: []string{"xhigh"}}, false},
		"a level twice":                   {&ExecutorChoicesV1{Efforts: []string{"high", "high"}}, false},
	} {
		profile := open
		profile.ExecutorChoices = test.choices
		_, err := loadProfile(profile)
		if test.valid != (err == nil) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// With no level in the manifest, every model runs at its own default, so one profile offers them all.
	own := open
	own.ManifestTemplatePath = openModelTemplateAt(t, root, togetherProvider(t), "")
	own.ExecutorChoices = &ExecutorChoicesV1{Models: []string{"gpt-oss-120b", "glm-5.3", "devstral"}}
	if _, err := loadProfile(own); err != nil {
		t.Fatalf("every model at its own default was refused: %v", err)
	}
	// The key file is not needed to load: the engine reads not ready until it is placed.
	if _, err := loadProfile(open); err != nil {
		t.Fatalf("an open-model profile without its key yet was refused: %v", err)
	}
}

func TestAnAdministratorsPickOfAnOpenModelIsPinnedByNameAndTheEnginesSayWhatItIs(t *testing.T) {
	provider := togetherProvider(t)
	fixture := newEditedAdmissionFixture(t, true, func(root string, profiles *ProfileFileV1) {
		profiles.Profiles[0].ManifestTemplatePath = openModelTemplate(t, root, provider)
		profiles.Profiles[0].ExecutorChoices = &ExecutorChoicesV1{Models: []string{"gpt-oss-120b", "glm-5.3"}, Efforts: []string{"high"}}
	})
	refused := fixture.request
	refused.RequestID = "request-refused"
	refused.Executor = &serviceapi.RunExecutorV1{Model: "devstral"}
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, refused); !errors.Is(err, serviceapi.ErrExecutorChoiceNotAccepted) {
		t.Fatalf("a model the profile does not offer = %v", err)
	}
	request := fixture.request
	request.Executor = &serviceapi.RunExecutorV1{Model: "glm-5.3"}
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readRunManifest(t, fixture, response.RunID)
	if manifest.Executor.TaskModel != "glm-5.3" || manifest.Executor.TaskEffort != "high" || manifest.Executor.ReviewModel != "" ||
		manifest.Executor.Provider == nil || manifest.Executor.Provider.ID != "together" {
		t.Fatalf("the pinned executor = %+v", manifest.Executor)
	}

	engines, err := fixture.controller.Engines(context.Background())
	if err != nil || len(engines.Engines) == 0 || engines.Engines[0].ProfileID != "default" {
		t.Fatalf("engines = %+v, %v", engines, err)
	}
	engine := engines.Engines[0]
	if engine.Executor != "codex" || engine.Access != "lane" || engine.Lane != "together" || engine.LaneModel != "openai/gpt-oss-120b" ||
		engine.TaskModel != "gpt-oss-120b" || engine.Ready || engine.Reason != "lane_no_key" || engine.Choices == nil {
		t.Fatalf("an open-model engine without its key = %+v", engine)
	}
	if engine.Provider == nil || engine.Provider.ID != "together" || engine.Provider.WireAPI != "responses" || engine.Provider.OnUnavailable != "wait" ||
		len(engine.Provider.Models) != 3 || engine.Provider.Models[1].Origin != "CN" || engine.Provider.Models[1].MaxConcurrentBuilds != 2 || engine.Provider.Check != nil {
		t.Fatalf("the provider = %+v", engine.Provider)
	}
	if err := os.WriteFile(provider.KeyFile, []byte(providerKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if engines, _ := fixture.controller.Engines(context.Background()); !engines.Engines[0].Ready || engines.Engines[0].Reason != "" {
		t.Fatalf("an open-model engine with its key = %+v", engines.Engines[0])
	}
	if err := os.Chmod(provider.KeyFile, 0o644); err != nil {
		t.Fatal(err)
	}
	engines, _ = fixture.controller.Engines(context.Background())
	if engines.Engines[0].Ready || engines.Engines[0].Reason != "lane_unreadable" {
		t.Fatalf("a key others can read = %+v", engines.Engines[0])
	}
	data, _ := json.Marshal(engines)
	if strings.Contains(string(data), providerKey) || strings.Contains(string(data), provider.KeyFile) || strings.Contains(string(data), provider.CodexHome) {
		t.Fatalf("the engines answer shows a key or a path: %s", data)
	}
}

func TestTheEnginesAnswerCarriesTheProvidersOwnWordWhenAskedButNeverChangesReady(t *testing.T) {
	status := http.StatusUnauthorized
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, `{"data":[{"id":"openai/gpt-oss-120b"}]}`)
	}))
	defer server.Close()
	provider := togetherProvider(t)
	provider.ID, provider.BaseURL, provider.CheckModels = "together-check", server.URL, true
	if err := os.WriteFile(provider.KeyFile, []byte(providerKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := authority.ExecutorPolicy{Executor: "codex", TaskModel: "gpt-oss-120b", TaskEffort: "high", ReviewEffort: "high", Provider: provider}
	got := engineOf("local-p02-together", policy, os.Getenv)
	if !got.Ready || got.Provider == nil || got.Provider.Check == nil || got.Provider.Check.Status != "key_refused" || got.Provider.Check.HTTPStatus != 401 {
		t.Fatalf("a refused key = %+v %+v", got, got.Provider)
	}
	// The answer is reused for minutes, so Repo C's reads do not reach the provider each time.
	status = http.StatusOK
	if again := engineOf("local-p02-together", policy, os.Getenv); again.Provider.Check.Status != "key_refused" {
		t.Fatalf("the check was not reused: %+v", again.Provider.Check)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), providerKey) {
		t.Fatalf("the check shows the key: %s", data)
	}
}

func TestAnEngineWithoutAProviderKeepsItsAnswer(t *testing.T) {
	got := engineOf("local-p02", authority.ExecutorPolicy{Executor: "codex", TaskModel: "gpt-6-astra", TaskEffort: "xhigh"}, environment(map[string]string{}))
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), "provider") {
		t.Fatalf("an engine without a provider answers one: %s", data)
	}
}

//go:build linux

package runadmission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

func TestAProfileWithoutChoicesAdmitsNoExecutorChoice(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	request := fixture.request
	request.Executor = &serviceapi.RunExecutorV1{Model: "gpt-6-astra"}
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request); !errors.Is(err, serviceapi.ErrExecutorChoiceNotAccepted) {
		t.Fatalf("a choice the profile does not offer = %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.starts) != 0 {
		t.Fatal("a refused choice starts no build")
	}
}

func TestAnExecutorChoiceTheProfileListsIsPinnedInTheRunsManifest(t *testing.T) {
	fixture := newEditedAdmissionFixture(t, true, func(_ string, profiles *ProfileFileV1) {
		profiles.Profiles[0].ExecutorChoices = &ExecutorChoicesV1{Models: []string{"gpt-6-astra", "gpt-5.6-sol"}, Efforts: []string{"xhigh"}}
	})
	for i, executor := range []serviceapi.RunExecutorV1{{Model: "gpt-7"}, {Effort: "high"}, {Model: "gpt-5.6-sol", Effort: "medium"}} {
		refused := fixture.request
		refused.RequestID = fmt.Sprintf("request-refused-%d", i)
		refused.Executor = &executor
		if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, refused); !errors.Is(err, serviceapi.ErrExecutorChoiceNotAccepted) {
			t.Fatalf("%+v is not on the list = %v", executor, err)
		}
	}

	request := fixture.request
	request.Executor = &serviceapi.RunExecutorV1{Model: "gpt-5.6-sol", Effort: "xhigh"}
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readRunManifest(t, fixture, response.RunID)
	if manifest.Executor.Executor != "codex" || manifest.Executor.TaskModel != "gpt-5.6-sol" || manifest.Executor.TaskEffort != "xhigh" ||
		manifest.Executor.ReviewModel != "" || manifest.Executor.ReviewEffort != "" {
		t.Fatalf("the choice is the task's and the review keeps the profile's: %+v", manifest.Executor)
	}
	registerFixtureRun(t, fixture, response.RunID)
	if replayed, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request); err != nil || replayed != response {
		t.Fatalf("the same request replays: %+v %v", replayed, err)
	}
	changed := request
	changed.Executor = &serviceapi.RunExecutorV1{Model: "gpt-6-astra"}
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, changed); !errors.Is(err, serviceapi.ErrRequestIDConflict) {
		t.Fatalf("the same request id with another choice is another request: %v", err)
	}

	plain := fixture.request
	plain.RequestID = "request-plain"
	fresh, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, plain)
	if err != nil {
		t.Fatal(err)
	}
	if manifest := readRunManifest(t, fixture, fresh.RunID); manifest.Executor.TaskModel != "" || manifest.Executor.TaskEffort != "" {
		t.Fatalf("a build without a choice keeps the profile's: %+v", manifest.Executor)
	}

	engines, err := fixture.controller.Engines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range engines.Engines {
		switch engine.ProfileID {
		case "default":
			if engine.Choices == nil || !slices.Equal(engine.Choices.Models, []string{"gpt-6-astra", "gpt-5.6-sol"}) || !slices.Equal(engine.Choices.Efforts, []string{"xhigh"}) {
				t.Fatalf("the engines answer the profile's choices: %+v", engine.Choices)
			}
		default:
			if engine.Choices != nil {
				t.Fatalf("%s offers no choices: %+v", engine.ProfileID, engine.Choices)
			}
		}
	}
}

func TestAClaudeProfileMayOfferReasoningLevels(t *testing.T) {
	fixture := newEditedAdmissionFixture(t, true, func(root string, profiles *ProfileFileV1) {
		profiles.Profiles[0].ManifestTemplatePath = claudeTemplate(t, root)
		profiles.Profiles[0].ExecutorChoices = &ExecutorChoicesV1{Models: []string{"claude-sonnet-5-5", "claude-opus-5-5"}, Efforts: []string{"high", "xhigh"}}
	})
	request := fixture.request
	request.Executor = &serviceapi.RunExecutorV1{Effort: "high"}
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request)
	if err != nil {
		t.Fatal(err)
	}
	if manifest := readRunManifest(t, fixture, response.RunID); manifest.Executor.Executor != "claude" || manifest.Executor.TaskModel != "claude-sonnet-5-5" ||
		manifest.Executor.TaskEffort != "high" || manifest.Executor.ReviewModel != "haiku" {
		t.Fatalf("a level alone keeps the profile's model: %+v", manifest.Executor)
	}
}

func TestExecutorChoicesAreCheckedWhenTheProfileLoads(t *testing.T) {
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
	codex := profiles.Profiles[0]
	claude := codex
	claude.ManifestTemplatePath = claudeTemplate(t, root)
	many := make([]string, MaxExecutorChoices+1)
	for i := range many {
		many[i] = fmt.Sprintf("model-%d", i)
	}
	for name, test := range map[string]struct {
		profile ProfileV1
		choices ExecutorChoicesV1
		valid   bool
	}{
		"codex models":         {codex, ExecutorChoicesV1{Models: []string{"gpt-6-astra", "gpt-5.6-sol"}}, true},
		"codex xhigh":          {codex, ExecutorChoicesV1{Efforts: []string{"xhigh"}}, true},
		"codex another level":  {codex, ExecutorChoicesV1{Efforts: []string{"xhigh", "high"}}, false},
		"claude levels":        {claude, ExecutorChoicesV1{Efforts: []string{"low", "medium", "high", "xhigh", "max"}}, true},
		"nothing listed":       {codex, ExecutorChoicesV1{}, false},
		"a model twice":        {claude, ExecutorChoicesV1{Models: []string{"claude-opus-5-5", "claude-opus-5-5"}}, false},
		"an unknown level":     {claude, ExecutorChoicesV1{Efforts: []string{"extreme"}}, false},
		"an upper-case model":  {claude, ExecutorChoicesV1{Models: []string{"Claude-Opus"}}, false},
		"a model with a level": {claude, ExecutorChoicesV1{Models: []string{"claude-opus-5-5:high"}}, false},
		"too many models":      {claude, ExecutorChoicesV1{Models: many}, false},
	} {
		profile := test.profile
		profile.ExecutorChoices = &test.choices
		_, err := loadProfile(profile)
		if test.valid != (err == nil) {
			t.Errorf("%s: %v", name, err)
		}
	}
	plain, err := loadProfile(codex)
	if err != nil {
		t.Fatal(err)
	}
	listed := codex
	listed.ExecutorChoices = &ExecutorChoicesV1{Models: []string{"gpt-6-astra"}}
	chosen, err := loadProfile(listed)
	if err != nil {
		t.Fatal(err)
	}
	if plain.profileBindingSHA256 == chosen.profileBindingSHA256 {
		t.Fatal("a profile's choices are part of its binding")
	}
	data, err = json.Marshal(codex)
	if err != nil || strings.Contains(string(data), "executor_choices") {
		t.Fatalf("a profile without choices keeps its shape and binding: %s %v", data, err)
	}
}

// claudeTemplate writes the fixture's manifest template on Claude, with a
// model and a review model named, and returns its path.
func claudeTemplate(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "manifest-template.json"))
	if err != nil {
		t.Fatal(err)
	}
	var template authority.Manifest
	if err := json.Unmarshal(data, &template); err != nil {
		t.Fatal(err)
	}
	template.Executor = authority.ExecutorPolicy{Executor: "claude", TaskModel: "claude-sonnet-5-5", TaskEffort: "xhigh", ReviewModel: "haiku"}
	path := filepath.Join(root, "manifest-template-claude.json")
	writeProtectedJSON(t, path, template)
	return path
}

func readRunManifest(t *testing.T, fixture *admissionFixture, runID string) authority.Manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixture.input, runID, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest authority.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

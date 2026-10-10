//go:build linux

package runadmission

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// libraryMirror makes a template library with dist/booking at one commit and
// returns its bare mirror and the request naming it.
func libraryMirror(t *testing.T, root string) (string, serviceapi.RunTemplateV1) {
	t.Helper()
	library := filepath.Join(root, "library")
	git(t, "", "init", "-q", "-b", "main", library)
	git(t, library, "config", "user.email", "library@example.test")
	git(t, library, "config", "user.name", "Library Test")
	if err := os.MkdirAll(filepath.Join(library, "dist", "booking"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(library, "dist", "booking", "app.json"), []byte("{\"name\": \"booking\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, library, "add", "-A")
	git(t, library, "commit", "-q", "-m", "booking 1.0.0")
	mirror := filepath.Join(root, "library.git")
	git(t, "", "clone", "-q", "--bare", library, mirror)
	commit := git(t, library, "rev-parse", "HEAD")
	return mirror, serviceapi.RunTemplateV1{TemplateID: "booking", Version: "1.0.0", CommitSHA: commit, TreeSHA: git(t, library, "rev-parse", commit+":dist/booking")}
}

func TestAProfileWithoutAMirrorAdmitsNoTemplate(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	request := fixture.request
	request.Template = &serviceapi.RunTemplateV1{TemplateID: "booking", Version: "1.0.0", CommitSHA: strings.Repeat("c", 40), TreeSHA: strings.Repeat("d", 40)}
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request); !errors.Is(err, serviceapi.ErrTemplateNotAccepted) {
		t.Fatalf("template without a mirror = %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.starts) != 0 {
		t.Fatal("a refused template starts no build")
	}
}

func TestATemplateTheMirrorHoldsIsBoundIntoTheRunsManifest(t *testing.T) {
	var template serviceapi.RunTemplateV1
	fixture := newEditedAdmissionFixture(t, true, func(root string, profiles *ProfileFileV1) {
		var mirror string
		mirror, template = libraryMirror(t, root)
		profiles.Profiles[0].TemplateMirrorPath = mirror
	})
	unknown := fixture.request
	unknown.RequestID = "request-unknown-template"
	unknown.Template = &serviceapi.RunTemplateV1{TemplateID: "booking", Version: "1.0.0", CommitSHA: template.CommitSHA, TreeSHA: strings.Repeat("e", 40)}
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, unknown); !errors.Is(err, serviceapi.ErrTemplateNotAccepted) {
		t.Fatalf("a tree the mirror does not hold = %v", err)
	}

	request := fixture.request
	request.Template = &template
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(fixture.input, response.RunID, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest authority.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.TemplateCopy == nil || manifest.TemplateCopy.TemplateID != "booking" || manifest.TemplateCopy.TreeSHA != template.TreeSHA ||
		manifest.TemplateCopy.CommitSHA != template.CommitSHA || !strings.HasSuffix(manifest.TemplateCopy.MirrorPath, "library.git") {
		t.Fatalf("manifest template copy %+v", manifest.TemplateCopy)
	}
	registerFixtureRun(t, fixture, response.RunID)
	if replayed, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request); err != nil || replayed != response {
		t.Fatalf("the same request replays: %+v %v", replayed, err)
	}
	changed := request
	changed.Template = nil
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, changed); !errors.Is(err, serviceapi.ErrRequestIDConflict) {
		t.Fatalf("the same request id without its template is another request: %v", err)
	}

	plain := fixture.request
	plain.RequestID = "request-fresh"
	fresh, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, plain)
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(fixture.input, fresh.RunID, "manifest.json"))
	if err != nil || strings.Contains(string(data), "template_copy") {
		t.Fatalf("a fresh build names no template: %v", err)
	}
}

func TestAProfileMirrorMustBeABareRepository(t *testing.T) {
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
	profile := profiles.Profiles[0]
	profile.TemplateMirrorPath = repository
	if _, err := loadProfile(profile); err == nil || !strings.Contains(err.Error(), "bare") {
		t.Fatalf("a working repository is not a mirror: %v", err)
	}
	profile.TemplateMirrorPath = "library.git"
	if _, err := loadProfile(profile); err == nil {
		t.Fatal("a relative mirror path is refused")
	}
}

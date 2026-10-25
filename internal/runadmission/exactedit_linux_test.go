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

// earlierVersion commits an app on a run's branch, as an earlier build leaves
// its version, without moving the repository's HEAD, and returns the commit.
func earlierVersion(t *testing.T, repository string) string {
	t.Helper()
	worktree := filepath.Join(t.TempDir(), "version")
	git(t, repository, "worktree", "add", "-q", "-b", "abcp/earlier-run", worktree, "HEAD")
	if err := os.WriteFile(filepath.Join(worktree, "index.html"), []byte("<button>Book now</button>\n<p>Book now or call.</p>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, worktree, "add", "-A")
	git(t, worktree, "commit", "-q", "-m", "earlier build")
	commit := git(t, worktree, "rev-parse", "HEAD")
	git(t, repository, "worktree", "remove", "--force", worktree)
	return commit
}

func TestAnExactEditIsCheckedAgainstItsVersionAndBoundIntoTheRunsManifest(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	source := earlierVersion(t, fixture.repository)
	blob := git(t, fixture.repository, "rev-parse", source+":index.html")
	edit := func(find string) *serviceapi.RunExactEditV1 {
		return &serviceapi.RunExactEditV1{SourceSHA: source, Replacements: []serviceapi.RunReplacementV1{{Path: "index.html", BaseBlob: blob, Find: find, Replace: "Reserve a table"}}}
	}

	for name, refused := range map[string]*serviceapi.RunExactEditV1{
		"words found twice":    edit("Book now"),
		"words not found":      edit("Order now"),
		"a changed file":       {SourceSHA: source, Replacements: []serviceapi.RunReplacementV1{{Path: "index.html", BaseBlob: strings.Repeat("e", 40), Find: "<button>Book now", Replace: "<button>Reserve"}}},
		"a commit no run made": {SourceSHA: git(t, fixture.repository, "rev-parse", "HEAD"), Replacements: []serviceapi.RunReplacementV1{{Path: "index.html", BaseBlob: blob, Find: "<button>Book now", Replace: "<button>Reserve"}}},
	} {
		request := fixture.request
		request.RequestID = "request-" + strings.ReplaceAll(name, " ", "-")
		request.ExactEdit = refused
		if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request); !errors.Is(err, serviceapi.ErrExactEditNotAccepted) {
			t.Fatalf("%s: admission = %v, want exact_edit_not_accepted", name, err)
		}
	}
	fixture.mu.Lock()
	if len(fixture.starts) != 0 {
		fixture.mu.Unlock()
		t.Fatal("a refused exact edit started a run")
	}
	fixture.mu.Unlock()

	request := fixture.request
	request.ExactEdit = edit("<button>Book now</button>")
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
	if manifest.ExactEdit == nil || manifest.ExactEdit.SourceSHA != source || len(manifest.ExactEdit.Replacements) != 1 ||
		manifest.ExactEdit.Replacements[0] != (authority.ExactEditReplacement{Path: "index.html", BaseBlob: blob, Find: "<button>Book now</button>", Replace: "Reserve a table"}) {
		t.Fatalf("manifest exact edit %+v", manifest.ExactEdit)
	}
	if manifest.TemplateCopy != nil || manifest.Worktree.Branch != "abcp/"+response.RunID {
		t.Fatalf("an exact edit's manifest %+v", manifest)
	}
	registerFixtureRun(t, fixture, response.RunID)
	if replayed, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request); err != nil || replayed != response {
		t.Fatalf("the same request replays: %+v %v", replayed, err)
	}
	changed := request
	changed.ExactEdit = edit("<p>Book now")
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, changed); !errors.Is(err, serviceapi.ErrRequestIDConflict) {
		t.Fatalf("the same request id with other words is another request: %v", err)
	}
	plain := fixture.request
	plain.RequestID = "request-build"
	build, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, plain)
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(fixture.input, build.RunID, "manifest.json"))
	if err != nil || strings.Contains(string(data), "exact_edit") {
		t.Fatalf("a build names no exact edit: %v", err)
	}
}

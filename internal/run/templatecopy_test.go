package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
)

// templateFiles is the assembled app of a library template (dist/booking).
var templateFiles = map[string]string{
	"app.json":          "{\"name\": \"booking\"}\n",
	"server.js":         "console.log('booking');\n",
	"public/index.html": "<main data-screen=\"today\"></main>\n",
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, contents := range files {
		target := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func identify(t *testing.T, repository string) {
	t.Helper()
	runGit(t, repository, "config", "user.email", "template@example.test")
	runGit(t, repository, "config", "user.name", "Template Test")
	runGit(t, repository, "config", "commit.gpgsign", "false")
}

// templateMirror makes a library with dist/booking at a tagged commit and returns
// its bare mirror, the commit and the tree of dist/booking.
func templateMirror(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	library := filepath.Join(root, "library")
	runGit(t, "", "init", "-q", "-b", "main", library)
	identify(t, library)
	dist := map[string]string{}
	for path, contents := range templateFiles {
		dist["dist/booking/"+path] = contents
	}
	dist["templates/booking/blueprint.json"] = "{\"id\": \"booking\"}\n"
	writeFiles(t, library, dist)
	runGit(t, library, "add", "-A")
	runGit(t, library, "commit", "-q", "-m", "booking 1.0.0")
	runGit(t, library, "tag", "booking-1.0.0")
	mirror := filepath.Join(root, "mirror.git")
	runGit(t, "", "clone", "-q", "--bare", library, mirror)
	commit := runGit(t, library, "rev-parse", "HEAD")
	return mirror, commit, runGit(t, library, "rev-parse", commit+":dist/booking")
}

// buildRepository makes a product repository at its start commit and applies each
// step as one commit, the way a run's branch grows; it returns the start and the head.
func buildRepository(t *testing.T, steps ...map[string]string) (string, string, string) {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "product")
	runGit(t, "", "init", "-q", "-b", "main", repository)
	identify(t, repository)
	writeFiles(t, repository, map[string]string{"README.md": "product\n"})
	runGit(t, repository, "add", "-A")
	runGit(t, repository, "commit", "-q", "-m", "start")
	start := runGit(t, repository, "rev-parse", "HEAD")
	for i, step := range steps {
		writeFiles(t, repository, step)
		runGit(t, repository, "add", "-A")
		runGit(t, repository, "commit", "-q", "--allow-empty", "-m", "step "+string(rune('a'+i)))
	}
	return repository, start, runGit(t, repository, "rev-parse", "HEAD")
}

func TestTemplateCopyIsFoundInTheRunsFirstCommits(t *testing.T) {
	mirror, commit, tree := templateMirror(t)
	plan := map[string]string{"abcp-ralphex-plan-0/plan.md": "# Plan\n"}
	changed := map[string]string{"app.json": "{\"name\": \"PhysioFirst\"}\n", "public/clinic.html": "<main></main>\n"}
	repository, start, head := buildRepository(t, plan, templateFiles, changed)
	template := authority.TemplateCopyManifest{MirrorPath: mirror, TemplateID: "booking", Version: "1.0.0", CommitSHA: commit, TreeSHA: tree}

	copyCommit, err := requireTemplateCopy(context.Background(), repository, start, head, template)
	if err != nil {
		t.Fatalf("a copied template is accepted even when later commits change it: %v", err)
	}
	if want := runGit(t, repository, "rev-parse", "HEAD~1"); copyCommit != want {
		t.Fatalf("copy commit = %s, want the copy step %s", copyCommit, want)
	}
}

func TestTemplateCopyRefusesASkippedOrChangedCopy(t *testing.T) {
	mirror, commit, tree := templateMirror(t)
	template := authority.TemplateCopyManifest{MirrorPath: mirror, TemplateID: "booking", Version: "1.0.0", CommitSHA: commit, TreeSHA: tree}
	plan := map[string]string{"abcp-ralphex-plan-0/plan.md": "# Plan\n"}

	altered := map[string]string{}
	for path, contents := range templateFiles {
		altered[path] = contents
	}
	altered["server.js"] = "console.log('rewritten');\n"
	repository, start, head := buildRepository(t, plan, altered)
	_, err := requireTemplateCopy(context.Background(), repository, start, head, template)
	if err == nil || !strings.Contains(err.Error(), "server.js") || !strings.Contains(err.Error(), "1 of 3") {
		t.Fatalf("a file changed while copying is refused and named, got %v", err)
	}

	repository, start, head = buildRepository(t, plan, map[string]string{"index.html": "<h1>fresh</h1>\n"})
	if _, err := requireTemplateCopy(context.Background(), repository, start, head, template); err == nil {
		t.Fatal("a run that never copied the template is refused")
	}

	repository, start, _ = buildRepository(t, plan)
	if _, err := requireTemplateCopy(context.Background(), repository, start, start, template); err == nil ||
		!strings.Contains(err.Error(), "no commit") {
		t.Fatalf("a run with no commit is refused, got %v", err)
	}
}

func TestTemplateCopyIsLookedForOnlyEarlyAndOnlyAgainstTheMirror(t *testing.T) {
	mirror, commit, tree := templateMirror(t)
	template := authority.TemplateCopyManifest{MirrorPath: mirror, TemplateID: "booking", Version: "1.0.0", CommitSHA: commit, TreeSHA: tree}
	var steps []map[string]string
	for i := 0; i < maxTemplateCopyCommits; i++ {
		steps = append(steps, map[string]string{"notes.md": strings.Repeat("x", i+1) + "\n"})
	}
	steps = append(steps, templateFiles)
	repository, start, head := buildRepository(t, steps...)
	if _, err := requireTemplateCopy(context.Background(), repository, start, head, template); err == nil {
		t.Fatal("a copy made after the run's first commits does not count")
	}

	repository, start, head = buildRepository(t, templateFiles)
	wrong := template
	wrong.TreeSHA = strings.Repeat("a", 40)
	if _, err := requireTemplateCopy(context.Background(), repository, start, head, wrong); err == nil ||
		!strings.Contains(err.Error(), "mirror's tree") {
		t.Fatalf("a tree the mirror does not hold at that commit is refused, got %v", err)
	}
	before := runGit(t, "", "--git-dir="+mirror, "for-each-ref")
	if _, err := requireTemplateCopy(context.Background(), repository, start, head, template); err != nil {
		t.Fatal(err)
	}
	if after := runGit(t, "", "--git-dir="+mirror, "for-each-ref"); after != before {
		t.Fatal("the mirror is only read")
	}
}

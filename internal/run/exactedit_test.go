package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
)

// versionFiles is the app an earlier run built, the version an exact edit changes.
var versionFiles = map[string]string{
	"index.html":            "<main>\n  <h1>Our menu</h1>\n  <button>Book now</button>\n</main>\n",
	"tests/app.test.js":     "expect(page).toContain('Book now');\n",
	"migrations/001.sql":    "insert into labels values ('Book now');\n",
	"public/copy/notes.txt": "aaa\n",
}

// addVersion commits files on a run's branch from the repository's start, as an
// earlier build leaves its version, and returns the commit. The default checkout
// stays where it was.
func addVersion(t *testing.T, repository, branch string, files map[string]string) string {
	t.Helper()
	worktree := filepath.Join(t.TempDir(), "version")
	runGit(t, repository, "worktree", "add", "-q", "-b", branch, worktree, "HEAD")
	identify(t, worktree)
	writeFiles(t, worktree, files)
	runGit(t, worktree, "add", "-A")
	runGit(t, worktree, "commit", "-q", "-m", "earlier build")
	commit := runGit(t, worktree, "rev-parse", "HEAD")
	runGit(t, repository, "worktree", "remove", "--force", worktree)
	return commit
}

func blobAt(t *testing.T, repository, commit, path string) string {
	t.Helper()
	return runGit(t, repository, "rev-parse", commit+":"+path)
}

// exactEditFixture is a run whose authority changes "Book now" in the earlier
// version's index.html; change adjusts the edit before it is admitted.
func exactEditFixture(t *testing.T, files map[string]string, change func(repository string, edit *authority.ExactEditManifest), acceptanceArgv ...string) (runFixture, string) {
	t.Helper()
	var source string
	fixture := newRunFixtureEdited(t, "#!/bin/sh\nprintf 'the builder must not start\\n'\nexit 97\n",
		authority.WorktreePolicy{Enabled: true, Branch: "abcp/run-fixture"}, func(manifest *authority.Manifest) {
			repository := manifest.Repository.Path
			source = addVersion(t, repository, "abcp/earlier-run", files)
			edit := authority.ExactEditManifest{SourceSHA: source, Replacements: []authority.ExactEditReplacement{{
				Path: "index.html", BaseBlob: blobAt(t, repository, source, "index.html"), Find: "Book now", Replace: "Reserve a table",
			}}}
			if change != nil {
				change(repository, &edit)
			}
			manifest.ExactEdit = &edit
		}, acceptanceArgv...)
	return fixture, source
}

func TestExactEditRunAcceptsTheReplacementWithoutTheBuilder(t *testing.T) {
	fixture, source := exactEditFixture(t, versionFiles, nil, commandPath(t, "grep"), "-q", "Reserve a table", "index.html")
	result := fixture.execute(t)
	if !result.Accepted() || result.State != domain.StateBranchAccepted {
		t.Fatalf("exact edit was not accepted: %#v", result)
	}
	if result.Ralphex.StdoutRef.URI != "" || result.RalphexMetadataRef.URI != "" {
		t.Fatalf("the builder ran for an exact edit: %#v", result.Ralphex)
	}
	repository := fixture.authority.Repository().Path
	start := fixture.authority.Repository().StartSHA
	commits := strings.Fields(runGit(t, repository, "rev-list", "--reverse", start+"..abcp/run-fixture"))
	if len(commits) != 2 {
		t.Fatalf("the branch has %d commits after the start, want the copy and the edit", len(commits))
	}
	if runGit(t, repository, "rev-parse", commits[0]+"^{tree}") != runGit(t, repository, "rev-parse", source+"^{tree}") {
		t.Fatal("the first commit does not copy the version's files")
	}
	if changed := runGit(t, repository, "diff", "--name-only", commits[0], commits[1]); changed != "index.html" {
		t.Fatalf("the edit commit changed %q, want only index.html", changed)
	}
	if got := runGit(t, repository, "show", commits[1]+":index.html"); !strings.Contains(got, "<button>Reserve a table</button>") || strings.Contains(got, "Book now") {
		t.Fatalf("index.html after the edit = %q", got)
	}
	if got := runGit(t, repository, "show", commits[1]+":tests/app.test.js"); got != strings.TrimSpace(versionFiles["tests/app.test.js"]) {
		t.Fatalf("an undeclared file changed: %q", got)
	}
	// A worktree the authority does not retain is gone once the edit is made.
	if _, err := os.Stat(exactEditWorktreePath(repository, "abcp/run-fixture")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the edit's worktree was kept without retention: %v", err)
	}
	if head := runGit(t, repository, "rev-parse", "HEAD"); head != start {
		t.Fatalf("the default checkout moved to %s", head)
	}
	if records := result.Acceptance.Commands(); len(records) != 1 || records[0].Process.ExitCode != 0 {
		t.Fatalf("acceptance did not run on the edit: %#v", records)
	}
}

func TestExactEditRunIsStoppedByAFailingCheck(t *testing.T) {
	fixture, _ := exactEditFixture(t, versionFiles, nil, commandPath(t, "grep"), "-q", "Book now", "index.html")
	result := fixture.execute(t)
	if result.State != domain.StateFailed || result.Accepted() {
		t.Fatalf("a failing check accepted the edit: %#v", result)
	}
	if _, err := gitOutput(context.Background(), fixture.authority.Repository().Path, "rev-parse", "--verify", "abcp/run-fixture"); err != nil {
		t.Fatalf("the edit's branch is missing after its check failed: %v", err)
	}
}

func TestExactEditRunIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	twice := map[string]string{}
	for path, contents := range versionFiles {
		twice[path] = contents
	}
	twice["index.html"] = "<button>Book now</button>\n<a>Book now</a>\n"
	for _, test := range []struct {
		name   string
		files  map[string]string
		change func(string, *authority.ExactEditManifest)
		reason string
	}{
		{"a changed file", versionFiles, func(repository string, edit *authority.ExactEditManifest) {
			edit.Replacements[0].BaseBlob = blobAt(t, repository, edit.SourceSHA, "tests/app.test.js")
		}, "the file changed"},
		{"words found twice", twice, nil, "found more than once"},
		{"words not found", versionFiles, func(_ string, edit *authority.ExactEditManifest) {
			edit.Replacements[0].Find = "Order now"
		}, "the text is not found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, _ := exactEditFixture(t, test.files, test.change, commandPath(t, "grep"), "-q", "Reserve", "index.html")
			result, err := fixture.runner(t).Run(context.Background())
			if err == nil || !errors.Is(err, ErrExactEditDoesNotApply) || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("Run error = %v, want %q", err, test.reason)
			}
			if result.State != domain.StateFailed {
				t.Fatalf("state = %s, want FAILED", result.State)
			}
			if exists, _ := localBranchExists(context.Background(), fixture.authority.Repository().Path, "abcp/run-fixture"); exists {
				t.Fatal("a refused edit created its branch")
			}
		})
	}
}

func TestCheckExactEditReadsWithoutWriting(t *testing.T) {
	repository, start, _ := buildRepository(t)
	source := addVersion(t, repository, "abcp/earlier-run", versionFiles)
	stray := addVersion(t, repository, "feature/not-a-run", map[string]string{"index.html": versionFiles["index.html"], "stray.txt": "not a run's\n"})
	runGit(t, repository, "branch", "-D", "feature/not-a-run")
	base := func(path string) string { return blobAt(t, repository, source, path) }
	edit := func(path, find string) authority.ExactEditManifest {
		return authority.ExactEditManifest{SourceSHA: source, Replacements: []authority.ExactEditReplacement{{Path: path, BaseBlob: base(path), Find: find, Replace: "x"}}}
	}
	refs := runGit(t, repository, "for-each-ref")
	if err := CheckExactEdit(context.Background(), repository, edit("index.html", "Book now")); err != nil {
		t.Fatalf("a valid edit was refused: %v", err)
	}
	for _, test := range []struct {
		name   string
		edit   authority.ExactEditManifest
		reason string
	}{
		{"a commit no run made", authority.ExactEditManifest{SourceSHA: stray, Replacements: edit("index.html", "Book now").Replacements}, "no run's branch"},
		{"a commit not in the repository", authority.ExactEditManifest{SourceSHA: strings.Repeat("ab", 20), Replacements: edit("index.html", "Book now").Replacements}, "not in the repository"},
		{"the repository's start", authority.ExactEditManifest{SourceSHA: start, Replacements: edit("index.html", "Book now").Replacements}, "not in the source commit"},
		{"overlapping words", edit("public/copy/notes.txt", "aa"), "more than once"},
		{"a folder", authority.ExactEditManifest{SourceSHA: source, Replacements: []authority.ExactEditReplacement{{Path: "tests", BaseBlob: base("index.html"), Find: "a", Replace: "b"}}}, "not a regular file"},
		{"a missing file", authority.ExactEditManifest{SourceSHA: source, Replacements: []authority.ExactEditReplacement{{Path: "about.html", BaseBlob: base("index.html"), Find: "a", Replace: "b"}}}, "not in the source commit"},
		{"a migration", edit("migrations/001.sql", "Book now"), "the path is refused"},
		{"a glob", authority.ExactEditManifest{SourceSHA: source, Replacements: []authority.ExactEditReplacement{{Path: "*.html", BaseBlob: base("index.html"), Find: "Book now", Replace: "b"}}}, "not in the source commit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := CheckExactEdit(context.Background(), repository, test.edit)
			if !errors.Is(err, ErrExactEditDoesNotApply) || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("CheckExactEdit = %v, want %q", err, test.reason)
			}
		})
	}
	if after := runGit(t, repository, "for-each-ref"); after != refs {
		t.Fatalf("checking an edit changed the repository's refs:\n%s\n---\n%s", refs, after)
	}
}

func TestVerifyExactEditRefusesAnUndeclaredChange(t *testing.T) {
	repository, start, _ := buildRepository(t)
	source := addVersion(t, repository, "abcp/earlier-run", versionFiles)
	edit := authority.ExactEditManifest{SourceSHA: source, Replacements: []authority.ExactEditReplacement{{
		Path: "index.html", BaseBlob: blobAt(t, repository, source, "index.html"), Find: "Book now", Replace: "Reserve a table",
	}}}
	files, err := readExactEdit(context.Background(), repository, edit)
	if err != nil {
		t.Fatal(err)
	}
	copySHA, headSHA, err := makeExactEditBranch(context.Background(), repository, start, "abcp/edit", source, files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyExactEdit(context.Background(), repository, start, "abcp/edit", source, copySHA, headSHA, files); err != nil {
		t.Fatalf("the edit's own branch was refused: %v", err)
	}
	if _, _, err := makeExactEditBranch(context.Background(), repository, start, "abcp/edit", source, files); err == nil {
		t.Fatal("an edit overwrote an existing branch")
	}
	// The same edit plus a change to another file is not the declared edit.
	worktree := filepath.Join(t.TempDir(), "tampered")
	runGit(t, repository, "worktree", "add", "-q", "-b", "abcp/tampered", worktree, headSHA)
	identify(t, worktree)
	runGit(t, worktree, "reset", "-q", "--soft", copySHA)
	writeFiles(t, worktree, map[string]string{"tests/app.test.js": "expect(true);\n"})
	runGit(t, worktree, "add", "-A")
	runGit(t, worktree, "commit", "-q", "-m", "edit and more")
	tampered := runGit(t, worktree, "rev-parse", "HEAD")
	if _, err := verifyExactEdit(context.Background(), repository, start, "abcp/tampered", source, copySHA, tampered, files); err == nil {
		t.Fatal("an undeclared change was accepted as the exact edit")
	}
}

func TestCheckoutExactEditUsesTheBuildersWorktreePlace(t *testing.T) {
	repository, start, _ := buildRepository(t)
	source := addVersion(t, repository, "abcp/earlier-run", versionFiles)
	files, err := readExactEdit(context.Background(), repository, authority.ExactEditManifest{SourceSHA: source, Replacements: []authority.ExactEditReplacement{{
		Path: "index.html", BaseBlob: blobAt(t, repository, source, "index.html"), Find: "Book now", Replace: "Reserve a table",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := makeExactEditBranch(context.Background(), repository, start, "abcp/run-1", source, files); err != nil {
		t.Fatal(err)
	}
	path, err := checkoutExactEdit(context.Background(), repository, "abcp/run-1")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(repository, ".ralphex", "worktrees", "abcp", "run-1") {
		t.Fatalf("worktree at %s", path)
	}
	if branch := runGit(t, path, "symbolic-ref", "HEAD"); branch != "refs/heads/abcp/run-1" {
		t.Fatalf("worktree HEAD = %s", branch)
	}
	if status := runGit(t, path, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("the edit's worktree is not clean: %q", status)
	}
	if err := removeExactEditWorktree(context.Background(), repository, path); err != nil {
		t.Fatal(err)
	}
}

//go:build linux

package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
)

// runRepository clones the product repository the way a walled run's repository is made and returns the run folder.
func runRepository(t *testing.T, product string) string {
	t.Helper()
	folder := t.TempDir()
	runGit(t, "", "clone", "-q", "--no-local", "--single-branch", product, filepath.Join(folder, "repo"))
	return folder
}

func readBranch(t *testing.T, folder, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(folder, "repo", ".git", "refs", "heads", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// The review base (TEMPLATE_FIRST_BUILDS.md): it starts at the start commit and moves to the commit that holds the
// template copy once the brought-back branch has one, so `git diff abcp-review-base...HEAD` is the build's own work.
func TestReviewBaseMovesFromTheStartToTheTemplateCopy(t *testing.T) {
	mirror, commit, tree := templateMirror(t)
	plan := map[string]string{"abcp-ralphex-plan-0/plan.md": "# Plan\n"}
	changed := map[string]string{"app.json": "{\"name\": \"PhysioFirst\"}\n"}
	product, start, head := buildRepository(t, plan, templateFiles, changed)
	template := authority.TemplateCopyManifest{MirrorPath: mirror, TemplateID: "booking", Version: "1.0.0", CommitSHA: commit, TreeSHA: tree}
	folder := runRepository(t, product)
	w := &walledRun{folder: folder, repository: filepath.Join(folder, "repo"), product: product, startSHA: start,
		reviewBase: reviewBaseBranch, template: &template, evidence: map[string]any{}}

	if !w.startReviewBase() {
		t.Fatalf("the review base is written: %v", w.evidence)
	}
	if got := readBranch(t, folder, reviewBaseBranch); got != start {
		t.Fatalf("before the copy the base is the start commit: got %s, want %s", got, start)
	}
	if got := runGit(t, w.repository, "rev-parse", reviewBaseBranch); got != start {
		t.Fatalf("Git reads the branch at the start commit: %s", got)
	}

	w.fetched = head
	w.moveReviewBase(context.Background())
	copyCommit := runGit(t, product, "rev-parse", "HEAD~1")
	if got := readBranch(t, folder, reviewBaseBranch); got != copyCommit {
		t.Fatalf("after the copy the base is the copy commit: got %s, want %s", got, copyCommit)
	}
	diff := runGit(t, w.repository, "diff", "--name-only", reviewBaseBranch+"..."+head)
	if diff != "app.json" {
		t.Fatalf("the review reads only the build's own change, not the copy: %q", diff)
	}

	// A base already on the branch's history is left alone; a resumed attempt starts at it, not at the start commit.
	w.moveReviewBase(context.Background())
	if !w.startReviewBase() || readBranch(t, folder, reviewBaseBranch) != copyCommit {
		t.Fatal("a later attempt keeps the copy commit as its base")
	}
}

// A fresh build has no review base, and a branch without the copy keeps the start commit as its base.
func TestReviewBaseIsOnlyForACopyThatIsThere(t *testing.T) {
	mirror, commit, tree := templateMirror(t)
	product, start, head := buildRepository(t, map[string]string{"server.js": "console.log('fresh');\n"})
	folder := runRepository(t, product)
	fresh := &walledRun{folder: folder, repository: filepath.Join(folder, "repo"), product: product, startSHA: start, evidence: map[string]any{}}
	if fresh.startReviewBase() {
		t.Fatal("a fresh build keeps Ralphex's own base")
	}
	if _, err := os.Lstat(filepath.Join(folder, "repo", ".git", "refs", "heads", reviewBaseBranch)); !os.IsNotExist(err) {
		t.Fatalf("no branch is written for a fresh build: %v", err)
	}
	template := authority.TemplateCopyManifest{MirrorPath: mirror, TemplateID: "booking", Version: "1.0.0", CommitSHA: commit, TreeSHA: tree}
	skipped := &walledRun{folder: folder, repository: filepath.Join(folder, "repo"), product: product, startSHA: start,
		reviewBase: reviewBaseBranch, template: &template, evidence: map[string]any{}, fetched: head}
	if !skipped.startReviewBase() {
		t.Fatal("the base is written")
	}
	skipped.moveReviewBase(context.Background())
	if got := readBranch(t, folder, reviewBaseBranch); got != start {
		t.Fatalf("a branch without the copy keeps the start commit: %s", got)
	}
}

// The run's repository is the build's to write: a folder on the way swapped for a link is refused, and a link in the
// branch's own place is replaced, never followed.
func TestReviewBaseNeverFollowsALink(t *testing.T) {
	product, start, _ := buildRepository(t)
	folder := runRepository(t, product)
	outside := t.TempDir()

	heads := filepath.Join(folder, "repo", ".git", "refs", "heads")
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(heads, reviewBaseBranch)); err != nil {
		t.Fatal(err)
	}
	if err := pointRunBranch(folder, reviewBaseBranch, start); err != nil {
		t.Fatalf("a link in the branch's place is replaced: %v", err)
	}
	if data, _ := os.ReadFile(victim); string(data) != "keep\n" {
		t.Fatalf("the link's target is untouched: %q", data)
	}
	if info, err := os.Lstat(filepath.Join(heads, reviewBaseBranch)); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("the branch is a file of its own: %v", err)
	}

	if err := os.Rename(heads, filepath.Join(outside, "heads")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "heads"), heads); err != nil {
		t.Fatal(err)
	}
	if err := pointRunBranch(folder, "other", start); err == nil {
		t.Fatal("a folder swapped for a link is refused")
	}
	if _, err := os.Lstat(filepath.Join(outside, "heads", "other")); !os.IsNotExist(err) {
		t.Fatalf("nothing is written through the link: %v", err)
	}
	if err := pointRunBranch(folder, reviewBaseBranch, "main"); err == nil {
		t.Fatal("only a full commit id is written")
	}
}

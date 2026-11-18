package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A site-kit website as a build leaves it: content.json, the pages the export wrote from it, and a page it no longer makes.
var siteKitVersion = map[string]string{
	"app.json":                `{"id": "cafe", "kind": "static", "blocks": {"SITE": "0.3.1"}}` + "\n",
	"tools/export-static.mjs": "// the kit's export\n",
	"content.json":            siteContent,
	"index.html":              "<img src=\"public/assets/section-1.png\">\n<!-- made by the export -->\n",
	"gallery.html":            "<img src=\"public/assets/section-1.png\">\n<!-- made by the export -->\n",
	"old.html":                "<!-- made by the export -->\n",
	"public/snapshot.js":      "globalThis.__SITE_SNAPSHOT__ = {};\n",
	"public/styles.css":       "body { margin: 0 }\n",
	"sitemap.xml":             "<urlset/>\n",
}

// fakeExport stands in for the walled export: change edits the folder as an export would.
func fakeExport(t *testing.T, change func(dir string)) *int {
	t.Helper()
	calls := 0
	saved := siteExportCommand
	siteExportCommand = func(_ context.Context, _ *Wall, dir string) error {
		calls++
		change(dir)
		return nil
	}
	t.Cleanup(func() { siteExportCommand = saved })
	return &calls
}

func write(t *testing.T, dir, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestASiteKitWebsitesEditRegeneratesItsPages(t *testing.T) {
	repository, _, _ := buildRepository(t)
	head := addVersion(t, repository, "abcp/edit", siteKitVersion)
	fakeExport(t, func(dir string) {
		content, _ := os.ReadFile(filepath.Join(dir, "content.json"))
		if !strings.Contains(string(content), "section-1.png") {
			t.Error("the export did not get the website's files")
		}
		write(t, dir, "gallery.html", "<img src=\"public/assets/picked-7.png\">\n<!-- made by the export -->\n")
		write(t, dir, "public/snapshot.js", "globalThis.__SITE_SNAPSHOT__ = {\"picked\": true};\n")
		write(t, dir, "index.html", siteKitVersion["index.html"]) // written again, the same
		os.Remove(filepath.Join(dir, "old.html"))
	})
	regenerated, newHead, err := exportSitePages(context.Background(), nil, repository, "abcp/edit", head)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"gallery.html", "old.html", "public/snapshot.js"}; !reflect.DeepEqual(regenerated, want) {
		t.Fatalf("regenerated %v, want %v", regenerated, want)
	}
	if tip := runGit(t, repository, "rev-parse", "refs/heads/abcp/edit"); tip != newHead || newHead == head {
		t.Fatalf("the branch is at %s, want the pages' commit %s after %s", tip, newHead, head)
	}
	if parent := runGit(t, repository, "rev-parse", newHead+"^"); parent != head {
		t.Fatalf("the pages' commit's parent is %s, want %s", parent, head)
	}
	if got := runGit(t, repository, "show", newHead+":gallery.html"); !strings.Contains(got, "picked-7.png") {
		t.Fatalf("gallery.html: %q", got)
	}
	if _, err := gitBytes(context.Background(), repository, nil, nil, "cat-file", "-e", newHead+":old.html"); err == nil {
		t.Fatal("old.html should be gone")
	}
}

func TestTheExportMayChangeOnlyItsOwnFiles(t *testing.T) {
	for name, change := range map[string]func(dir string){
		"a style sheet":      func(dir string) { write(t, dir, "public/styles.css", "body { margin: 1px }\n") },
		"the content":        func(dir string) { write(t, dir, "content.json", "{}\n") },
		"a page in a folder": func(dir string) { write(t, dir, "pages/extra.html", "<p></p>\n") },
		"the export itself":  func(dir string) { os.Remove(filepath.Join(dir, "tools/export-static.mjs")) },
		"a link":             func(dir string) { os.Symlink("/etc/passwd", filepath.Join(dir, "link.html")) },
	} {
		t.Run(name, func(t *testing.T) {
			repository, _, _ := buildRepository(t)
			head := addVersion(t, repository, "abcp/edit", siteKitVersion)
			fakeExport(t, change)
			_, _, err := exportSitePages(context.Background(), nil, repository, "abcp/edit", head)
			if !errors.Is(err, ErrExactEditDoesNotApply) {
				t.Fatalf("got %v, want a refusal", err)
			}
			if tip := runGit(t, repository, "rev-parse", "refs/heads/abcp/edit"); tip != head {
				t.Fatal("a refused export moved the branch")
			}
		})
	}
}

func TestOnlyASiteKitWebsiteIsExported(t *testing.T) {
	notSites := map[string]map[string]string{
		"a node app":        {"app.json": `{"kind": "node", "blocks": {"SITE": "0.3.1"}}`, "tools/export-static.mjs": "//\n"},
		"no SITE block":     {"app.json": `{"kind": "static", "blocks": {}}`, "tools/export-static.mjs": "//\n"},
		"no export script":  {"app.json": `{"kind": "static", "blocks": {"SITE": "0.3.1"}}`},
		"no app.json":       {"index.html": "<p></p>\n"},
		"app.json not JSON": {"app.json": "{", "tools/export-static.mjs": "//\n"},
	}
	for name, files := range notSites {
		t.Run(name, func(t *testing.T) {
			repository, _, _ := buildRepository(t)
			head := addVersion(t, repository, "abcp/edit", files)
			calls := fakeExport(t, func(string) {})
			regenerated, newHead, err := exportSitePages(context.Background(), nil, repository, "abcp/edit", head)
			if err != nil || regenerated != nil || newHead != head || *calls != 0 {
				t.Fatalf("got %v %s %v, %d export(s)", regenerated, newHead, err, *calls)
			}
		})
	}
}

func TestAFailedExportRefusesTheEdit(t *testing.T) {
	repository, _, _ := buildRepository(t)
	head := addVersion(t, repository, "abcp/edit", siteKitVersion)
	saved := siteExportCommand
	siteExportCommand = func(context.Context, *Wall, string) error { return errors.New("exit status 1") }
	t.Cleanup(func() { siteExportCommand = saved })
	if _, _, err := exportSitePages(context.Background(), nil, repository, "abcp/edit", head); !errors.Is(err, ErrExactEditDoesNotApply) || !strings.Contains(err.Error(), "the pages") {
		t.Fatalf("got %v", err)
	}
	// With no wall, the real export refuses to run at all.
	if err := walledSiteExport(context.Background(), nil, t.TempDir()); err == nil {
		t.Fatal("the export ran without a wall")
	}
}

func TestSiteExportAllowedFiles(t *testing.T) {
	for path, want := range map[string]bool{
		"index.html": true, "gallery.html": true, "public/snapshot.js": true, "public/offline.js": true, "sitemap.xml": true,
		"content.json": false, "public/app.js": false, "pages/a.html": false, "public/index.html": false, "robots.txt": false,
	} {
		if siteExportAllowed(path) != want {
			t.Errorf("%s: want %v", path, want)
		}
	}
	if gitBlobID([]byte("hello\n"), 40) != "ce013625030ba8dba906f756967f9e9ca394464a" {
		t.Fatal("gitBlobID does not match Git's name for a blob")
	}
}

// TestWalledSiteExportOnTheHost runs the kit's real export inside the real wall, on a copy of a site-kit website:
// ABCP_PROOF_WALL_FILE names the host's wall file and ABCP_PROOF_SITE a website's folder. Skipped elsewhere.
func TestWalledSiteExportOnTheHost(t *testing.T) {
	wallFile, site := os.Getenv("ABCP_PROOF_WALL_FILE"), os.Getenv("ABCP_PROOF_SITE")
	if wallFile == "" || site == "" {
		t.Skip("set ABCP_PROOF_WALL_FILE and ABCP_PROOF_SITE to run the export in the host's wall")
	}
	wall, err := LoadBuildWall(wallFile)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "abcp-site-export-proof-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.CopyFS(dir, os.DirFS(site)); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dir, "sitemap.xml"))
	if err := walledSiteExport(context.Background(), wall, dir); err != nil {
		t.Fatal(err)
	}
	if sitemap, err := os.ReadFile(filepath.Join(dir, "sitemap.xml")); err != nil || !strings.Contains(string(sitemap), "<urlset") {
		t.Fatalf("the export wrote no sitemap: %v", err)
	}
}

package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

// A template's copy with exact writes (Repo C design note TEMPLATE_LIBRARY_ROUND_2.md §10.5): a first version made of the
// library template's dist/booking_lite and its settings, with no builder.

const (
	salonConfig = `{
  "cal.slotMinutes": 30,
  "cal.hours": {
    "mon": ["09:00", "17:00"],
    "sat": []
  },
  "site.brand": { "name": "Booking", "colour": "#224466" },
  "auth.mode": "email"
}
`
	salonPreset = `{
  "key": "salon",
  "default": true,
  "brand": { "name": "Salon" },
  "words": { "booking": "appointment" }
}
`
	salonApp = "{\n  \"name\": \"booking_lite\",\n  \"defaultPreset\": \"spa\"\n}\n"
)

// salonTemplate is the assembled app of the library's booking_lite.
var salonTemplate = map[string]string{
	"app.json":            salonApp,
	"app.config.json":     salonConfig,
	"presets/salon.json":  salonPreset,
	"presets/spa.json":    "{\"key\": \"spa\"}\n",
	"server.js":           "console.log('booking');\n",
	"public/index.html":   "<main><h1>Book now</h1></main>\n",
	"public/book.html":    "<p>Book now or call.</p>\n<p>Book now.</p>\n",
	"tests/check.test.js": "expect(config['cal.slotMinutes']).toBeGreaterThan(0);\n",
}

// writesMirror makes a library with two commits, dist/booking_lite (files) and another template beside it, and returns
// the bare mirror and the template as a run's authority names it.
func writesMirror(t *testing.T, files map[string]string) authority.TemplateCopyManifest {
	t.Helper()
	root := t.TempDir()
	library := filepath.Join(root, "library")
	runGit(t, "", "init", "-q", "-b", "main", library)
	identify(t, library)
	writeFiles(t, library, map[string]string{"dist/other/app.json": "{\"name\": \"other, an earlier library\"}\n"})
	runGit(t, library, "add", "-A")
	runGit(t, library, "commit", "-q", "-m", "other 1.0.0")
	dist := map[string]string{"templates/booking_lite/blueprint.json": "{\"id\": \"booking_lite\"}\n"}
	for path, contents := range files {
		dist["dist/booking_lite/"+path] = contents
	}
	writeFiles(t, library, dist)
	runGit(t, library, "add", "-A")
	runGit(t, library, "commit", "-q", "-m", "booking_lite 1.0.0")
	runGit(t, library, "tag", "booking_lite-1.0.0")
	mirror := filepath.Join(root, "library.git")
	runGit(t, "", "clone", "-q", "--bare", library, mirror)
	commit := runGit(t, library, "rev-parse", "HEAD")
	return authority.TemplateCopyManifest{MirrorPath: mirror, TemplateID: "booking_lite", Version: "1.0.0", CommitSHA: commit,
		TreeSHA: runGit(t, library, "rev-parse", commit+":dist/booking_lite")}
}

func mirrorBlob(t *testing.T, template authority.TemplateCopyManifest, path string) string {
	t.Helper()
	return runGit(t, "", "--git-dir="+template.MirrorPath, "rev-parse", template.TreeSHA+":"+path)
}

func compactJSON(t *testing.T, text string) string {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// salonWrites are Repo C's writes for a salon (SettingsOnly): the person's preset over presets/salon.json, whole; app.json's
// defaultPreset; two settings in app.config.json; and words in a page, alone in their file.
func salonWrites(t *testing.T, template authority.TemplateCopyManifest) authority.ExactEditManifest {
	t.Helper()
	config := mirrorBlob(t, template, "app.config.json")
	return authority.ExactEditManifest{Replacements: []authority.ExactEditReplacement{
		{Path: "presets/salon.json", BaseBlob: mirrorBlob(t, template, "presets/salon.json"), Pointer: "",
			From: compactJSON(t, salonPreset), To: `{"key":"salon","default":true,"brand":{"name":"Studio Nine"},"words":{"booking":"visit"}}`},
		{Path: "app.json", BaseBlob: mirrorBlob(t, template, "app.json"), Pointer: "/defaultPreset", From: `"spa"`, To: `"salon"`},
		{Path: "app.config.json", BaseBlob: config, Pointer: "/cal.slotMinutes", From: `30`, To: `45`},
		{Path: "app.config.json", BaseBlob: config, Pointer: "/site.brand", From: `{"name":"Booking","colour":"#224466"}`, To: `{"name":"Studio Nine","colour":"#aa3355"}`},
		{Path: "public/index.html", BaseBlob: mirrorBlob(t, template, "public/index.html"), Find: "Book now", Replace: "Book a visit"},
	}}
}

// templateWritesFixture is a run whose authority writes salonWrites on booking_lite's copy; change adjusts the writes
// before the run is admitted.
func templateWritesFixture(t *testing.T, change func(template *authority.TemplateCopyManifest, edit *authority.ExactEditManifest), acceptanceArgv ...string) (runFixture, authority.TemplateCopyManifest) {
	t.Helper()
	template := writesMirror(t, salonTemplate)
	edit := salonWrites(t, template)
	if change != nil {
		change(&template, &edit)
	}
	// The pinned binary answers the probe that a retained worktree needs (every product profile retains them), and
	// never builds.
	probe, err := json.Marshal(ralphex.CapabilityProbeV1{Kind: "RalphexCapabilityProbeV1", SourceSHA: "source-test",
		MaxIterationsFlag: true, SessionTimeoutFlag: true, IdleTimeoutFlag: true, IsolatedConfig: true,
		InternalReviewBudgetV1: true, OrchestratorSubprocessWaitV1: true, HumanSessionIdentityV1: true, WorktreeRetentionV1: true})
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1\" = \"--abcp-governance-capability-v1\" ]; then printf '%s\\n' '" + string(probe) + "'; exit 0; fi\n" +
		"printf 'the builder must not start\\n'\nexit 97\n"
	fixture := newRunFixtureEdited(t, script,
		authority.WorktreePolicy{Enabled: true, Branch: "abcp/run-fixture", Retain: true}, func(manifest *authority.Manifest) {
			copied := template
			manifest.TemplateCopy = &copied
			manifest.ExactEdit = &edit
		}, acceptanceArgv...)
	return fixture, template
}

// templateChecks is the template's own checks for the salon: its settings as written.
func templateChecks(t *testing.T, want ...string) string {
	t.Helper()
	script := "#!/bin/sh\nset -e\n"
	for _, check := range want {
		file, text, _ := strings.Cut(check, "|")
		script += fmt.Sprintf("%s -qF '%s' %s\n", commandPath(t, "grep"), text, file)
	}
	path := filepath.Join(t.TempDir(), "template-checks")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// snapshotTree records every entry under root: its kind, mode, size, time and, for a file, its bytes' digest.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	entries := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		record := fmt.Sprintf("%s %d %d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			record += " " + hex.EncodeToString(sum[:])
		}
		entries[path] = record
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func requireUnchanged(t *testing.T, before, after map[string]string) {
	t.Helper()
	for path, record := range before {
		if after[path] != record {
			t.Fatalf("the mirror changed at %s: %q, then %q", path, record, after[path])
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Fatalf("the mirror gained %s", path)
		}
	}
}

func TestATemplatesCopyWithExactWritesIsAcceptedWithoutTheBuilder(t *testing.T) {
	checks := templateChecks(t, `app.config.json|"cal.slotMinutes": 45,`, `presets/salon.json|"Studio Nine"`, `app.json|"defaultPreset": "salon"`)
	fixture, template := templateWritesFixture(t, nil, checks)
	repository := fixture.authority.Repository().Path
	start := fixture.authority.Repository().StartSHA
	mirrorBefore := snapshotTree(t, template.MirrorPath)

	result := fixture.execute(t)
	if !result.Accepted() || result.State != domain.StateBranchAccepted {
		t.Fatalf("the template's copy with its writes was not accepted: %s %q", result.State, result.FailureReason)
	}
	if result.Ralphex.StdoutRef.URI != "" || result.RalphexMetadataRef.URI != "" {
		t.Fatalf("the builder ran for a template's writes: %#v", result.Ralphex)
	}
	requireUnchanged(t, mirrorBefore, snapshotTree(t, template.MirrorPath))

	commits := strings.Fields(runGit(t, repository, "rev-list", "--reverse", start+"..abcp/run-fixture"))
	if len(commits) != 2 {
		t.Fatalf("the branch has %d commits after the start, want the copy and the writes", len(commits))
	}
	if got := runGit(t, repository, "rev-parse", commits[0]+"^{tree}"); got != template.TreeSHA {
		t.Fatalf("the first commit holds %s, not the mirror's tree %s", got, template.TreeSHA)
	}
	if parent := runGit(t, repository, "rev-parse", commits[0]+"^"); parent != start {
		t.Fatalf("the copy is not on the run's start: %s", parent)
	}
	if found, err := requireTemplateCopy(context.Background(), repository, start, commits[1], template); err != nil || found != commits[0] {
		t.Fatalf("the template-copy check finds the copy in %s, %v; want the first commit", found, err)
	}
	if changed := runGit(t, repository, "diff", "--name-only", commits[0], commits[1]); changed != "app.config.json\napp.json\npresets/salon.json\npublic/index.html" {
		t.Fatalf("the writes changed %q", changed)
	}
	wantConfig := strings.Replace(strings.Replace(salonConfig, `"cal.slotMinutes": 30`, `"cal.slotMinutes": 45`, 1),
		`{ "name": "Booking", "colour": "#224466" }`, `{"name":"Studio Nine","colour":"#aa3355"}`, 1)
	if got := runGit(t, repository, "cat-file", "blob", commits[1]+":app.config.json"); got+"\n" != wantConfig {
		t.Fatalf("app.config.json is not the copy with only its two values written:\n%s", got)
	}
	if got := runGit(t, repository, "cat-file", "blob", commits[1]+":app.json"); got+"\n" != strings.Replace(salonApp, `"spa"`, `"salon"`, 1) {
		t.Fatalf("app.json after the writes = %q", got)
	}
	// The whole document is written compact, as a pointer's value is, with the file's own line end kept.
	preset, err := gitBytes(context.Background(), repository, nil, nil, "cat-file", "blob", commits[1]+":presets/salon.json")
	if err != nil || string(preset) != `{"key":"salon","default":true,"brand":{"name":"Studio Nine"},"words":{"booking":"visit"}}`+"\n" {
		t.Fatalf("presets/salon.json after the writes = %q, %v", preset, err)
	}
	if got := runGit(t, repository, "cat-file", "blob", commits[1]+":public/index.html"); got != "<main><h1>Book a visit</h1></main>" {
		t.Fatalf("public/index.html after the writes = %q", got)
	}
	if head := runGit(t, repository, "rev-parse", "HEAD"); head != start {
		t.Fatalf("the default checkout moved to %s", head)
	}
	// Only the template's objects came: neither the library's commits nor another template's files.
	if _, err := gitOutput(context.Background(), repository, "cat-file", "-e", template.CommitSHA); err == nil {
		t.Fatal("the library's commit was copied into the repository")
	}
	other := runGit(t, "", "--git-dir="+template.MirrorPath, "rev-parse", template.CommitSHA+":dist/other/app.json")
	if _, err := gitOutput(context.Background(), repository, "cat-file", "-e", other); err == nil {
		t.Fatal("another template's file was copied into the repository")
	}
	// The worktree is where the builder keeps one, on the branch, clean.
	worktree := exactEditWorktreePath(repository, "abcp/run-fixture")
	if branch := runGit(t, worktree, "symbolic-ref", "HEAD"); branch != "refs/heads/abcp/run-fixture" {
		t.Fatalf("worktree HEAD = %s", branch)
	}
	if status := runGit(t, worktree, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("the worktree is not clean: %q", status)
	}
	if records := result.Acceptance.Commands(); len(records) != 1 || records[0].Process.ExitCode != 0 {
		t.Fatalf("the template's checks did not run on the writes: %#v", records)
	}

	data, err := os.ReadFile(filepath.Join(fixture.evidence, fixture.authority.RunID(), "exact-edit.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence ExactEditResultV1
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.SourceSHA != "" || strings.Contains(string(data), "source_sha") || evidence.Template == nil ||
		*evidence.Template != (ExactEditTemplateV1{TemplateID: "booking_lite", Version: "1.0.0", CommitSHA: template.CommitSHA, TreeSHA: template.TreeSHA}) ||
		evidence.CopySHA != commits[0] || evidence.HeadSHA != commits[1] || evidence.Worktree != worktree || len(evidence.Files) != 4 {
		t.Fatalf("exact-edit.json = %s", data)
	}
	for _, file := range evidence.Files {
		if file.BaseBlob != mirrorBlob(t, template, file.Path) || file.NewBlob != runGit(t, repository, "rev-parse", commits[1]+":"+file.Path) {
			t.Fatalf("exact-edit.json names %+v", file)
		}
	}

	ledger, err := os.ReadFile(fixture.ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ledger), `"exact_edit":true`) || !strings.Contains(string(ledger), `"template_id":"booking_lite"`) ||
		!strings.Contains(string(ledger), `"template_version":"1.0.0"`) || strings.Contains(string(ledger), `"source_sha"`) {
		t.Fatalf("the ledger does not mark the run as a template's writes:\n%s", ledger)
	}
	for _, word := range []string{"app.config.json", "presets/", "index.html", "Studio Nine", "Book a visit", "cal.slotMinutes"} {
		if strings.Contains(string(ledger), word) {
			t.Fatalf("the ledger carries %q", word)
		}
	}
}

func TestATemplatesCopyWithExactWritesIsStoppedByAFailingCheck(t *testing.T) {
	fixture, _ := templateWritesFixture(t, nil, templateChecks(t, `app.config.json|"cal.slotMinutes": 30,`))
	result, err := fixture.runner(t).Run(context.Background())
	if result.State != domain.StateFailed || result.Accepted() {
		t.Fatalf("a failing check accepted the writes: %s %v", result.State, err)
	}
	if exists, _ := localBranchExists(context.Background(), fixture.authority.Repository().Path, "abcp/run-fixture"); !exists {
		t.Fatal("the writes' branch is missing after its check failed")
	}
}

func TestATemplatesCopyWithExactWritesIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*authority.TemplateCopyManifest, *authority.ExactEditManifest)
		cause  error
		reason string
	}{
		{"a wrong blob", func(template *authority.TemplateCopyManifest, edit *authority.ExactEditManifest) {
			edit.Replacements[1].BaseBlob = mirrorBlob(t, *template, "server.js")
		}, ErrExactEditDoesNotApply, "app.json: the file changed"},
		{"a value that is not the one there", func(_ *authority.TemplateCopyManifest, edit *authority.ExactEditManifest) {
			edit.Replacements[2].From = `25`
		}, ErrExactEditDoesNotApply, `app.config.json: "/cal.slotMinutes": the value there is not the one expected`},
		{"a whole document that is not the file", func(_ *authority.TemplateCopyManifest, edit *authority.ExactEditManifest) {
			edit.Replacements[0].From = `{"key":"salon"}`
		}, ErrExactEditDoesNotApply, `presets/salon.json: "": the value there is not the one expected`},
		{"a pointer that names nothing", func(_ *authority.TemplateCopyManifest, edit *authority.ExactEditManifest) {
			edit.Replacements[2].Pointer = "/cal.slotLength"
		}, ErrExactEditDoesNotApply, "the pointer names no value"},
		{"a file the template does not have", func(template *authority.TemplateCopyManifest, edit *authority.ExactEditManifest) {
			edit.Replacements[1].Path = "presets/barber.json"
		}, ErrExactEditDoesNotApply, "presets/barber.json: the file is not in the template booking_lite 1.0.0"},
		{"words found twice", func(template *authority.TemplateCopyManifest, edit *authority.ExactEditManifest) {
			edit.Replacements = append(edit.Replacements, authority.ExactEditReplacement{Path: "public/book.html",
				BaseBlob: mirrorBlob(t, *template, "public/book.html"), Find: "Book now", Replace: "Reserve"})
		}, ErrExactEditDoesNotApply, "found more than once"},
		{"a tree the mirror does not hold", func(template *authority.TemplateCopyManifest, _ *authority.ExactEditManifest) {
			template.TreeSHA = strings.Repeat("a", 40)
		}, ErrTemplateNotHeld, "the mirror's tree does not match"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, template := templateWritesFixture(t, test.change, templateChecks(t, `app.json|salon`))
			repository := fixture.authority.Repository().Path
			mirrorBefore := snapshotTree(t, template.MirrorPath)
			result, err := fixture.runner(t).Run(context.Background())
			if !errors.Is(err, test.cause) || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("Run error = %v, want %q", err, test.reason)
			}
			if result.State != domain.StateFailed {
				t.Fatalf("state = %s, want FAILED", result.State)
			}
			if exists, _ := localBranchExists(context.Background(), repository, "abcp/run-fixture"); exists {
				t.Fatal("a refused template's writes created its branch")
			}
			if _, err := gitOutput(context.Background(), repository, "cat-file", "-e", writesMirrorTree(t, template)); err == nil {
				t.Fatal("a refused template's writes copied the template's objects")
			}
			requireUnchanged(t, mirrorBefore, snapshotTree(t, template.MirrorPath))
		})
	}
}

// writesMirrorTree is the mirror's own tree of dist/booking_lite, whatever the authority names.
func writesMirrorTree(t *testing.T, template authority.TemplateCopyManifest) string {
	t.Helper()
	return runGit(t, "", "--git-dir="+template.MirrorPath, "rev-parse", template.CommitSHA+":dist/booking_lite")
}

// CheckTemplateExactEdit is what admission asks before anything is written: the bounds Repo C keeps, the mirror's
// template and the writes against its files, each refused as its kind, with no ref and no object written anywhere.
func TestCheckTemplateExactEditRefusesEachCaseReadingOnly(t *testing.T) {
	template := writesMirror(t, salonTemplate)
	repository, _, _ := buildRepository(t)
	refs, mirrorBefore := runGit(t, repository, "for-each-ref"), snapshotTree(t, template.MirrorPath)
	objectsBefore := runGit(t, repository, "count-objects", "-v")
	if err := CheckTemplateExactEdit(context.Background(), repository, template, salonWrites(t, template)); err != nil {
		t.Fatalf("Repo C's writes for a salon were refused: %v", err)
	}
	config := mirrorBlob(t, template, "app.config.json")
	setting := func(pointer, from, to string) authority.ExactEditReplacement {
		return authority.ExactEditReplacement{Path: "app.config.json", BaseBlob: config, Pointer: pointer, From: from, To: to}
	}
	many := salonWrites(t, template)
	for len(many.Replacements) <= authority.MaxTemplateExactEditReplacements {
		many.Replacements = append(many.Replacements, setting(fmt.Sprintf("/x%d", len(many.Replacements)), `1`, `2`))
	}
	large := `"` + strings.Repeat("x", authority.MaxTemplateExactEditValueBytes) + `"`
	together := salonWrites(t, template)
	for i := 0; i < 5; i++ {
		together.Replacements = append(together.Replacements, setting(fmt.Sprintf("/y%d", i), `"a"`, `"`+strings.Repeat("y", authority.MaxTemplateExactEditValueBytes-2)+`"`))
	}
	for _, test := range []struct {
		name   string
		edit   func() authority.ExactEditManifest
		mirror string
		cause  error
		reason string
	}{
		{"nesting pointers", func() authority.ExactEditManifest {
			edit := salonWrites(t, template)
			edit.Replacements = append(edit.Replacements, setting("/site.brand/name", `"Booking"`, `"Nine"`))
			return edit
		}, "", ErrExactEditDoesNotApply, "two pointers in one file nest"},
		{"the same pointer twice", func() authority.ExactEditManifest {
			edit := salonWrites(t, template)
			edit.Replacements = append(edit.Replacements, setting("/cal.slotMinutes", `30`, `60`))
			return edit
		}, "", ErrExactEditDoesNotApply, "two pointers in one file nest"},
		{"the whole document beside another value", func() authority.ExactEditManifest {
			edit := salonWrites(t, template)
			edit.Replacements = append(edit.Replacements, authority.ExactEditReplacement{Path: "presets/salon.json",
				BaseBlob: mirrorBlob(t, template, "presets/salon.json"), Pointer: "/brand/name", From: `"Salon"`, To: `"Nine"`})
			return edit
		}, "", ErrExactEditDoesNotApply, "two pointers in one file nest"},
		{"words beside a value", func() authority.ExactEditManifest {
			edit := salonWrites(t, template)
			edit.Replacements = append(edit.Replacements, authority.ExactEditReplacement{Path: "app.config.json", BaseBlob: config, Find: "email", Replace: "phone"})
			return edit
		}, "", ErrExactEditDoesNotApply, "only replacement"},
		{"more than 40", func() authority.ExactEditManifest { return many }, "", ErrExactEditDoesNotApply, "one to 40 replacements"},
		{"a value over 64 KB", func() authority.ExactEditManifest {
			edit := salonWrites(t, template)
			edit.Replacements[2].To = large
			return edit
		}, "", ErrExactEditDoesNotApply, "at most 65536 bytes"},
		{"values over 256 KB together", func() authority.ExactEditManifest { return together }, "", ErrExactEditDoesNotApply, "larger than 262144 bytes together"},
		{"two blobs for one file", func() authority.ExactEditManifest {
			edit := salonWrites(t, template)
			edit.Replacements[3].BaseBlob = mirrorBlob(t, template, "server.js")
			return edit
		}, "", ErrExactEditDoesNotApply, "app.config.json: the file changed"},
		{"a source commit", func() authority.ExactEditManifest {
			edit := salonWrites(t, template)
			edit.SourceSHA = template.CommitSHA
			return edit
		}, "", ErrExactEditDoesNotApply, "names no source commit"},
		{"a mirror without the commit", func() authority.ExactEditManifest { return salonWrites(t, template) }, filepath.Join(t.TempDir(), "empty.git"), ErrTemplateNotHeld, "the commit is not in the mirror"},
	} {
		t.Run(test.name, func(t *testing.T) {
			named := template
			if test.mirror != "" {
				if _, err := os.Stat(test.mirror); errors.Is(err, os.ErrNotExist) {
					runGit(t, "", "init", "-q", "--bare", test.mirror)
				}
				named.MirrorPath = test.mirror
			}
			err := CheckTemplateExactEdit(context.Background(), repository, named, test.edit())
			if !errors.Is(err, test.cause) || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("CheckTemplateExactEdit = %v, want %v %q", err, test.cause, test.reason)
			}
		})
	}
	if after := runGit(t, repository, "for-each-ref"); after != refs {
		t.Fatalf("checking the writes changed the repository's refs:\n%s\n---\n%s", refs, after)
	}
	if after := runGit(t, repository, "count-objects", "-v"); after != objectsBefore {
		t.Fatalf("checking the writes wrote objects:\n%s\n---\n%s", objectsBefore, after)
	}
	requireUnchanged(t, mirrorBefore, snapshotTree(t, template.MirrorPath))
}

// A library whose objects are named by SHA-256 holds the template, but cannot give a SHA-1 repository its tree.
func TestATemplateNamedByAnotherObjectFormatIsNotHeld(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "library")
	runGit(t, "", "init", "-q", "-b", "main", "--object-format=sha256", library)
	identify(t, library)
	writeFiles(t, library, map[string]string{"dist/booking_lite/app.json": salonApp})
	runGit(t, library, "add", "-A")
	runGit(t, library, "commit", "-q", "-m", "booking_lite 1.0.0")
	mirror := filepath.Join(root, "library.git")
	runGit(t, "", "clone", "-q", "--bare", library, mirror)
	commit := runGit(t, library, "rev-parse", "HEAD")
	template := authority.TemplateCopyManifest{MirrorPath: mirror, TemplateID: "booking_lite", Version: "1.0.0", CommitSHA: commit,
		TreeSHA: runGit(t, library, "rev-parse", commit+":dist/booking_lite")}
	repository, _, _ := buildRepository(t)
	edit := authority.ExactEditManifest{Replacements: []authority.ExactEditReplacement{{Path: "app.json",
		BaseBlob: runGit(t, library, "rev-parse", commit+":dist/booking_lite/app.json"), Pointer: "/defaultPreset", From: `"spa"`, To: `"salon"`}}}
	if err := CheckTemplateExactEdit(context.Background(), repository, template, edit); !errors.Is(err, ErrTemplateNotHeld) || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("CheckTemplateExactEdit = %v, want the object formats named", err)
	}
}

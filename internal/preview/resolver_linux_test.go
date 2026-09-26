//go:build linux

package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/activity"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	capsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runadmission"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type catalogFixture struct {
	reg runtimecatalog.RunRegistrationV1
}

func (f *catalogFixture) ReadRun(run string) (runtimecatalog.RunRegistrationV1, error) {
	if f.reg.RunID != run {
		return runtimecatalog.RunRegistrationV1{}, os.ErrNotExist
	}
	return f.reg, nil
}

type eventFixture struct {
	page activity.Page
	err  error
}

func (f *eventFixture) ReadActivity(context.Context, string, serviceapi.PageRequestV1) (json.RawMessage, error) {
	b, _ := json.Marshal(f.page)
	return b, f.err
}

type bindingFixture struct {
	root, repo, worktree, run, bindingPath string
	binding                                runadmission.AdmissionBindingV1
	manifest                               authority.Manifest
	spec                                   capsule.Spec
	catalog                                *catalogFixture
	events                                 *eventFixture
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func command(t *testing.T, path string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func newBindingFixture(t *testing.T) *bindingFixture {
	t.Helper()
	base := t.TempDir()
	f := &bindingFixture{root: filepath.Join(base, "service"), repo: filepath.Join(base, "repo"), worktree: filepath.Join(base, "worktree"), run: runadmission.DeriveRunID("gateway", "admit-1")}
	for _, dir := range []string{f.root, filepath.Join(f.root, "admissions"), f.repo} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	command(t, f.repo, "init", "-b", "main")
	command(t, f.repo, "config", "user.email", "test@example.test")
	command(t, f.repo, "config", "user.name", "Preview test")
	command(t, f.repo, "remote", "add", "origin", "https://example.test/owner/repo.git")
	os.WriteFile(filepath.Join(f.repo, ".gitignore"), []byte(".abcp/\n"), 0600)
	os.WriteFile(filepath.Join(f.repo, "source"), []byte("base\n"), 0600)
	command(t, f.repo, "add", ".")
	command(t, f.repo, "commit", "-m", "base")
	baseSHA := command(t, f.repo, "rev-parse", "HEAD")
	command(t, f.repo, "worktree", "add", "-b", "abcp/"+f.run, f.worktree)
	admitted := filepath.Join(f.repo, ".abcp", f.run)
	os.MkdirAll(admitted, 0700)
	plan := []byte("# Work\nProject: auth-1\n\n### Task 1\n- [ ] implementation\n")
	planPath := filepath.Join(admitted, "plan.md")
	os.WriteFile(planPath, plan, 0600)
	f.spec = capsule.Spec{PolicyVersion: capsule.PolicyVersionV2, Project: "auth-1", Plan: "task-1", RoadmapPhase: "product-run-admission", ExecutionPack: "version-1", Task: "admit-1", Repository: "owner/repo", BaseSHA: baseSHA, OperationContext: &capsule.OperationContext{Kind: capsule.OperationImplementation, OwnedScope: []string{"Product task task-1"}, BlockingCriteria: []string{"Critical or Major findings"}}, Invariants: []string{"Preserve binding"}, NonGoals: []string{"No execution policy changes"}, PredecessorOutcomes: []capsule.Outcome{}, Sources: []string{filepath.ToSlash(filepath.Join(".abcp", f.run, "plan.md"))}}
	_, data, err := capsule.Build(f.repo, f.spec)
	if err != nil {
		t.Fatal(err)
	}
	capsulePath := filepath.Join(admitted, "context-capsule.json")
	os.WriteFile(capsulePath, data, 0600)
	f.manifest = authority.Manifest{RunID: f.run, Repository: authority.RepositoryManifest{Path: f.repo, Identity: "owner/repo", StartSHA: baseSHA}, Plan: authority.PlanManifest{Path: planPath, SHA256: digest(plan)}, ContextCapsule: &authority.ContextCapsuleManifest{Path: capsulePath, SHA256: digest(data)}, Worktree: authority.WorktreePolicy{Enabled: true, Branch: "abcp/" + f.run}, Ralphex: authority.RalphexManifest{BinaryPath: "/usr/bin/false", BinarySHA256: strings.Repeat("a", 64), SourceSHA: strings.Repeat("b", 40)}}
	f.binding = runadmission.AdmissionBindingV1{Kind: "AdmissionBindingV1", SchemaVersion: 1, PrincipalID: "gateway", RequestID: "admit-1", RunID: f.run, RepositoryPath: f.repo, RepositoryIdentity: "owner/repo", RepositoryIdentityDigest: runtimecatalog.RepositoryIdentityDigest("owner/repo"), InputDirectory: ".abcp", PlanPath: planPath, ContextCapsulePath: capsulePath, ManifestPath: filepath.Join(admitted, "manifest.json"), LedgerRoot: filepath.Join(base, "ledger"), CanonicalLedgerPath: filepath.Join(base, "ledger", f.run, "events.jsonl"), EvidenceRoot: filepath.Join(base, "evidence"), CanonicalEvidenceRoot: filepath.Join(base, "evidence", f.run)}
	f.bindingPath = filepath.Join(f.root, "admissions", digest([]byte("gateway\x00admit-1"))+".binding.json")
	f.catalog = &catalogFixture{runtimecatalog.RunRegistrationV1{Kind: "RunRegistrationV1", RunID: f.run, RepositoryIdentityDigest: f.binding.RepositoryIdentityDigest, CanonicalLedgerPath: f.binding.CanonicalLedgerPath, CanonicalEvidenceRoot: f.binding.CanonicalEvidenceRoot}}
	f.save(t)
	os.WriteFile(filepath.Join(f.worktree, "source"), []byte("candidate\n"), 0600)
	command(t, f.worktree, "add", "source")
	command(t, f.worktree, "commit", "-m", "candidate")
	sha := command(t, f.worktree, "rev-parse", "HEAD")
	f.events = &eventFixture{page: activity.Page{SchemaVersion: "ActivityPageV1", RunID: f.run, Events: []activity.Event{{SchemaVersion: "ActivityEventV1", Ordinal: 1, ActivityID: jsonDigest([]string{f.run, "checkpoint", sha}), RunID: f.run, AuthorityLevel: "ABCP_STATE", Category: "CHECKPOINT", Status: "AVAILABLE", SourceKind: "ABCP_CHECKPOINT_OBSERVER", SourceDigest: jsonDigest([]string{f.run, sha}), CheckpointSHA: sha, CheckpointClean: true}}}}
	return f
}
func (f *bindingFixture) save(t *testing.T) {
	writeJSON(t, f.binding.ManifestPath, f.manifest)
	data, _ := os.ReadFile(f.binding.ManifestPath)
	f.binding.AuthorityDigest = digest(data)
	f.catalog.reg.AuthorityDigest = f.binding.AuthorityDigest
	writeJSON(t, f.bindingPath, f.binding)
}
func (f *bindingFixture) resolver() Resolver { return Resolver{f.root, f.catalog, f.events} }
func (f *bindingFixture) resolve() (Source, error) {
	return f.resolver().Resolve(context.Background(), f.run, f.events.page.Events[0].ActivityID)
}
func TestResolverRejectsBindingAndCheckpointMismatches(t *testing.T) {
	cases := map[string]func(*testing.T, *bindingFixture){
		"wrong-run":   func(t *testing.T, f *bindingFixture) { f.events.page.Events[0].RunID = "another" },
		"wrong-page":  func(t *testing.T, f *bindingFixture) { f.events.page.RunID = "another" },
		"dirty":       func(t *testing.T, f *bindingFixture) { f.events.page.Events[0].CheckpointClean = false },
		"empty-sha":   func(t *testing.T, f *bindingFixture) { f.events.page.Events[0].CheckpointSHA = "" },
		"activity-id": func(t *testing.T, f *bindingFixture) { f.events.page.Events[0].ActivityID = strings.Repeat("0", 64) },
		"unreachable": func(t *testing.T, f *bindingFixture) {
			command(t, f.worktree, "reset", "--hard", f.manifest.Repository.StartSHA)
		},
		"not-descendant": func(t *testing.T, f *bindingFixture) {
			command(t, f.repo, "checkout", "--orphan", "unrelated")
			command(t, f.repo, "commit", "-m", "unrelated")
			f.manifest.Repository.StartSHA = command(t, f.repo, "rev-parse", "HEAD")
			f.spec.BaseSHA = f.manifest.Repository.StartSHA
			_, data, err := capsule.Build(f.repo, f.spec)
			if err != nil {
				t.Fatal(err)
			}
			os.WriteFile(f.binding.ContextCapsulePath, data, 0600)
			f.manifest.ContextCapsule.SHA256 = digest(data)
			f.save(t)
		},
		"ambiguous": func(t *testing.T, f *bindingFixture) {
			f.events.page.Events = append(f.events.page.Events, activity.Event{Ordinal: 2, RunID: f.run, Status: "UNKNOWN"})
		},
		"duplicate-binding": func(t *testing.T, f *bindingFixture) {
			writeJSON(t, filepath.Join(f.root, "admissions", "duplicate.binding.json"), f.binding)
		},
		"development":  func(t *testing.T, f *bindingFixture) { f.binding.AdmissionKind = "development"; f.save(t) },
		"registration": func(t *testing.T, f *bindingFixture) { f.catalog.reg.AuthorityDigest = strings.Repeat("f", 64) },
		"repo-digest": func(t *testing.T, f *bindingFixture) {
			f.binding.RepositoryIdentityDigest = strings.Repeat("f", 64)
			f.save(t)
		},
		"remote-identity": func(t *testing.T, f *bindingFixture) {
			command(t, f.repo, "remote", "set-url", "origin", "https://example.test/wrong/repo.git")
		},
		"capsule-path":        func(t *testing.T, f *bindingFixture) { f.binding.ContextCapsulePath += ".other"; f.save(t) },
		"capsule-file-digest": func(t *testing.T, f *bindingFixture) { os.WriteFile(f.binding.ContextCapsulePath, []byte("{}"), 0600) },
		"capsule-self-digest": func(t *testing.T, f *bindingFixture) {
			data, _ := os.ReadFile(f.binding.ContextCapsulePath)
			var c capsule.Capsule
			json.Unmarshal(data, &c)
			c.CapsuleSHA256 = strings.Repeat("f", 64)
			writeJSON(t, f.binding.ContextCapsulePath, c)
			data, _ = os.ReadFile(f.binding.ContextCapsulePath)
			f.manifest.ContextCapsule.SHA256 = digest(data)
			f.save(t)
		},
		"capsule-product-phase": func(t *testing.T, f *bindingFixture) {
			f.spec.RoadmapPhase = "repo-c-development"
			_, data, err := capsule.Build(f.repo, f.spec)
			if err != nil {
				t.Fatal(err)
			}
			os.WriteFile(f.binding.ContextCapsulePath, data, 0600)
			f.manifest.ContextCapsule.SHA256 = digest(data)
			f.save(t)
		},
		"binding-symlink": func(t *testing.T, f *bindingFixture) {
			os.Rename(f.bindingPath, f.bindingPath+".original")
			os.Symlink(f.bindingPath+".original", f.bindingPath)
		},
		"binding-permissions": func(t *testing.T, f *bindingFixture) { os.Chmod(f.bindingPath, 0644) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newBindingFixture(t)
			if _, err := f.resolve(); err != nil {
				t.Fatal("valid fixture", err)
			}
			change(t, f)
			if _, err := f.resolve(); err == nil {
				t.Fatal("unsafe source accepted")
			}
		})
	}
}
func TestExactDetachedSourceIgnoresMutableWorktree(t *testing.T) {
	f := newBindingFixture(t)
	source, err := f.resolve()
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(f.worktree, "source"), []byte("uncommitted mutation\n"), 0600)
	root := filepath.Join(t.TempDir(), "sources")
	checkout := Checkout{root}
	id := strings.Repeat("c", 64)
	path, err := checkout.Materialize(context.Background(), id, source)
	if err != nil {
		t.Fatal(err)
	}
	if path == f.worktree || command(t, path, "rev-parse", "HEAD") != source.SHA {
		t.Fatal("not exact detached source")
	}
	data, _ := os.ReadFile(filepath.Join(path, "source"))
	if string(data) != "candidate\n" {
		t.Fatal("mutable source was served")
	}
	if _, err = git(context.Background(), path, "symbolic-ref", "--quiet", "HEAD"); err == nil {
		t.Fatal("checkout not detached")
	}
	if strings.Contains(command(t, path, "config", "--list"), f.repo) {
		t.Fatal("source reveals origin host path")
	}
	if err := filepath.WalkDir(filepath.Join(path, ".git"), func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(name)
		if err == nil && bytes.Contains(data, []byte(f.repo)) {
			t.Errorf("source metadata retains controller path: %s", name)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"FETCH_HEAD", "logs"} {
		if _, err := os.Stat(filepath.Join(path, ".git", name)); !os.IsNotExist(err) {
			t.Fatalf("generated controller metadata retained: %s (%v)", name, err)
		}
	}
	if err = checkout.Remove(id); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("source not cleaned")
	}
	if err = checkout.Remove("../escape"); err == nil {
		t.Fatal("unsafe deletion accepted")
	}
}

func TestDetachedSourceReadableWithRestrictiveUmask(t *testing.T) {
	// Umask is process-wide; isolate it from other tests and their goroutines.
	if os.Getenv("ABCP_PREVIEW_UMASK_HELPER") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDetachedSourceReadableWithRestrictiveUmask$")
		cmd.Env = append(os.Environ(), "ABCP_PREVIEW_UMASK_HELPER=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("restrictive umask checkout: %v\n%s", err, out)
		}
		return
	}
	syscall.Umask(0077)
	f := newBindingFixture(t)
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("private\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.worktree, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.worktree, "nested", "executable"), []byte("#!/bin/true\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.worktree, "nested", "link")); err != nil {
		t.Fatal(err)
	}
	command(t, f.worktree, "add", "nested")
	command(t, f.worktree, "commit", "-m", "source permissions")
	source := Source{SHA: command(t, f.worktree, "rev-parse", "HEAD"), Repository: f.repo, Branch: f.manifest.Worktree.Branch}
	checkout := Checkout{Root: filepath.Join(t.TempDir(), "sources")}
	path, err := checkout.Materialize(context.Background(), strings.Repeat("d", 64), source)
	if err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]os.FileMode{
		checkout.Root: 0700, path: 0755, filepath.Join(path, "source"): 0644,
		filepath.Join(path, "nested"): 0755, filepath.Join(path, "nested", "executable"): 0755,
		outside: 0600, filepath.Join(f.worktree, "source"): 0600,
	} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s: mode %o, want %o", name, info.Mode().Perm(), mode)
		}
	}
	if info, err := os.Lstat(filepath.Join(path, "nested", "link")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("source symlink was changed", err)
	}
	// The container sees the bind root, not its private host parent. Other-user
	// bits must suffice even when its numeric UID/GID differ from the controller.
	if err := filepath.WalkDir(path, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		need := os.FileMode(4)
		if entry.IsDir() {
			need = 5
		}
		if info.Mode().Perm()&need != need {
			t.Errorf("different container UID cannot read %s: %o", name, info.Mode().Perm())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewLifecycleNeverMutatesRunAdmissionOrGovernedBranch(t *testing.T) {
	f := newBindingFixture(t)
	paths := []string{f.bindingPath, f.binding.ManifestPath, f.binding.ContextCapsulePath, f.binding.PlanPath, filepath.Join(f.worktree, "source")}
	before := map[string]string{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = digest(data)
	}
	registration := f.catalog.reg
	head := command(t, f.worktree, "rev-parse", "HEAD")
	profile := testProfile()
	profile.RepositoryIdentityDigest = registration.RepositoryIdentityDigest
	root := filepath.Join(t.TempDir(), "previews")
	s, err := newService(context.Background(), root, map[string]PreviewProfileV1{profile.ProfileID: profile}, f.resolver(), Checkout{filepath.Join(root, "sources")}, &memoryRuntime{available: true, healthy: true}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := createCommand("no-run-mutation")
	c.ExpectedRunID = f.run
	c.CheckpointActivityID = f.events.page.Events[0].ActivityID
	v, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, f.run, c)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, e := s.ReadPreview(context.Background(), principal(), f.run, v.PreviewID)
		if e != nil {
			t.Fatal(e)
		}
		if got.Status == "READY" {
			break
		}
		if terminal(got.Status) || time.Now().After(deadline) {
			t.Fatal("not ready", got.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil || digest(data) != before[path] {
			t.Fatal("run authority mutated", path)
		}
	}
	if registration != f.catalog.reg || command(t, f.worktree, "rev-parse", "HEAD") != head {
		t.Fatal("registration/branch mutated")
	}
}

type pagedActivity struct {
	pages   map[string]activity.Page
	cursors []string
}

func (p *pagedActivity) ReadActivity(_ context.Context, _ string, request serviceapi.PageRequestV1) (json.RawMessage, error) {
	p.cursors = append(p.cursors, request.Cursor)
	page, ok := p.pages[request.Cursor]
	if !ok {
		return nil, ErrIntegrity
	}
	return json.Marshal(page)
}
func TestResolverTraversesPagesAndDistinguishesProviderWarnings(t *testing.T) {
	for _, scenario := range []string{"warning-before", "warning-after", "integrity-after", "repeated-cursor", "nonprogressing-ordinal", "empty-page"} {
		t.Run(scenario, func(t *testing.T) {
			f := newBindingFixture(t)
			checkpoint := f.events.page.Events[0]
			warning := activity.Event{SchemaVersion: "ActivityEventV1", RunID: f.run, Ordinal: 2, Category: "WARNING", Status: "UNKNOWN", AuthorityLevel: "PROVIDER_DETAIL", SourceKind: "RALPHEX_PROGRESS", SourceSessionID: "sha256-" + strings.Repeat("a", 64), SourceEventID: "sha256-" + strings.Repeat("b", 64)}
			reader := &pagedActivity{pages: map[string]activity.Page{
				"":       {SchemaVersion: "ActivityPageV1", RunID: f.run, Events: []activity.Event{checkpoint}, NextCursor: "page-2"},
				"page-2": {SchemaVersion: "ActivityPageV1", RunID: f.run, Events: []activity.Event{warning}},
			}}
			first, second := reader.pages[""], reader.pages["page-2"]
			switch scenario {
			case "warning-before":
				warning.Ordinal, checkpoint.Ordinal = 1, 2
				first.Events, second.Events = []activity.Event{warning}, []activity.Event{checkpoint}
			case "integrity-after":
				second.Events[0].SourceSessionID, second.Events[0].SourceEventID = "", ""
			case "repeated-cursor":
				second.NextCursor = "page-2"
			case "nonprogressing-ordinal":
				second.Events[0].Ordinal = 1
			case "empty-page":
				first.Events = nil
			}
			reader.pages[""], reader.pages["page-2"] = first, second
			resolver := f.resolver()
			resolver.Activity = reader
			_, err := resolver.Resolve(context.Background(), f.run, checkpoint.ActivityID)
			valid := scenario == "warning-before" || scenario == "warning-after"
			if (err == nil) != valid {
				t.Fatalf("eligibility: %v", err)
			}
			wantPages := 2
			if scenario == "empty-page" {
				wantPages = 1
			}
			if len(reader.cursors) != wantPages || reader.cursors[0] != "" || wantPages == 2 && reader.cursors[1] != "page-2" {
				t.Fatal("incorrect traversal", reader.cursors)
			}
		})
	}
}

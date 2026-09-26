//go:build linux

package activity

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runadmission"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

type fakeCatalog struct {
	runs map[string]runtimecatalog.RunRegistrationV1
}

func (c *fakeCatalog) ReadRun(run string) (runtimecatalog.RunRegistrationV1, error) {
	if r, ok := c.runs[run]; ok {
		return r, nil
	}
	return runtimecatalog.RunRegistrationV1{}, os.ErrNotExist
}

type bindingFixture struct {
	root, repo, worktree, run, bindingPath string
	catalog                                *fakeCatalog
	binding                                runadmission.AdmissionBindingV1
	manifest                               authority.Manifest
	scope                                  Scope
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
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
	f := &bindingFixture{root: filepath.Join(base, "service"), repo: filepath.Join(base, "repository"), worktree: filepath.Join(base, "worktree"), run: "admission-test"}
	for _, path := range []string{f.root, filepath.Join(f.root, "admissions"), f.repo} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	command(t, f.repo, "init", "-b", "main")
	command(t, f.repo, "config", "user.email", "test@example.test")
	command(t, f.repo, "config", "user.name", "Activity test")
	os.WriteFile(filepath.Join(f.repo, ".gitignore"), []byte(".abcp/\n.ralphex/\n"), 0600)
	os.WriteFile(filepath.Join(f.repo, "source"), []byte("base\n"), 0600)
	command(t, f.repo, "add", ".")
	command(t, f.repo, "commit", "-m", "base")
	baseSHA := command(t, f.repo, "rev-parse", "HEAD")
	command(t, f.repo, "worktree", "add", "-b", "abcp/"+f.run, f.worktree)
	admitted := filepath.Join(f.repo, ".abcp", f.run)
	os.MkdirAll(admitted, 0700)
	plan := []byte("# Work\nDisplay-Title: Task\nProject: project-test\nRepository: owner/repo\nAdmission-ID: " + f.run + "\n\n### Task 1\n- [ ] implement\n")
	planPath := filepath.Join(admitted, "plan.md")
	os.WriteFile(planPath, plan, 0600)
	f.manifest = authority.Manifest{RunID: f.run, Repository: authority.RepositoryManifest{Path: f.repo, Identity: "owner/repo", StartSHA: baseSHA}, Plan: authority.PlanManifest{Path: planPath, SHA256: digest(plan)}, Worktree: authority.WorktreePolicy{Enabled: true, Branch: "abcp/" + f.run}, Ralphex: authority.RalphexManifest{BinaryPath: "/usr/bin/false", BinarySHA256: strings.Repeat("a", 64), SourceSHA: strings.Repeat("b", 40)}}
	f.binding = runadmission.AdmissionBindingV1{Kind: "AdmissionBindingV1", SchemaVersion: 1, RunID: f.run, RepositoryPath: f.repo, RepositoryIdentity: "owner/repo", RepositoryIdentityDigest: runtimecatalog.RepositoryIdentityDigest("owner/repo"), InputDirectory: ".abcp", PlanPath: planPath, ManifestPath: filepath.Join(admitted, "manifest.json"), LedgerRoot: filepath.Join(base, "ledger"), CanonicalLedgerPath: filepath.Join(base, "ledger", f.run, "events.jsonl"), EvidenceRoot: filepath.Join(base, "evidence"), CanonicalEvidenceRoot: filepath.Join(base, "evidence", f.run)}
	f.bindingPath = filepath.Join(f.root, "admissions", "fixture.binding.json")
	f.catalog = &fakeCatalog{runs: map[string]runtimecatalog.RunRegistrationV1{f.run: {Kind: "RunRegistrationV1", RunID: f.run, RepositoryIdentityDigest: f.binding.RepositoryIdentityDigest, CanonicalLedgerPath: f.binding.CanonicalLedgerPath, CanonicalEvidenceRoot: f.binding.CanonicalEvidenceRoot}}}
	f.saveManifest(t)
	var err error
	f.scope, err = (Resolver{f.root, f.catalog}).Resolve(context.Background(), f.run)
	if err != nil {
		t.Fatal("fixture scope", err)
	}
	return f
}
func (f *bindingFixture) saveManifest(t *testing.T) {
	writeJSON(t, f.binding.ManifestPath, f.manifest)
	data, _ := os.ReadFile(f.binding.ManifestPath)
	f.binding.AuthorityDigest = digest(data)
	reg := f.catalog.runs[f.run]
	reg.AuthorityDigest = f.binding.AuthorityDigest
	f.catalog.runs[f.run] = reg
	writeJSON(t, f.bindingPath, f.binding)
}

func TestResolverRequiresExactControllerBinding(t *testing.T) {
	for _, scenario := range []string{"absent", "duplicate", "corrupt", "symlink", "permissions", "authority", "repository-digest", "ledger", "evidence", "manifest-bytes", "manifest-run", "manifest-repository", "manifest-path", "branch", "worktree-disabled", "base", "pin", "plan", "input-escape"} {
		t.Run(scenario, func(t *testing.T) {
			f := newBindingFixture(t)
			switch scenario {
			case "absent":
				os.Remove(f.bindingPath)
			case "duplicate":
				writeJSON(t, filepath.Join(f.root, "admissions", "second.binding.json"), f.binding)
			case "corrupt":
				os.WriteFile(f.bindingPath, []byte("{"), 0600)
			case "symlink":
				os.Rename(f.bindingPath, f.bindingPath+".old")
				os.Symlink(f.bindingPath+".old", f.bindingPath)
			case "permissions":
				os.Chmod(f.bindingPath, 0644)
			case "authority":
				f.binding.AuthorityDigest = strings.Repeat("f", 64)
				writeJSON(t, f.bindingPath, f.binding)
			case "repository-digest":
				f.binding.RepositoryIdentityDigest = strings.Repeat("f", 64)
				writeJSON(t, f.bindingPath, f.binding)
			case "ledger":
				f.binding.CanonicalLedgerPath += "wrong"
				writeJSON(t, f.bindingPath, f.binding)
			case "evidence":
				f.binding.CanonicalEvidenceRoot += "wrong"
				writeJSON(t, f.bindingPath, f.binding)
			case "manifest-bytes":
				data, _ := os.ReadFile(f.binding.ManifestPath)
				os.WriteFile(f.binding.ManifestPath, append(data, ' '), 0600)
			case "manifest-run":
				f.manifest.RunID = "other"
				f.saveManifest(t)
			case "manifest-repository":
				f.manifest.Repository.Identity = "attacker/repo"
				f.saveManifest(t)
			case "manifest-path":
				f.manifest.Repository.Path = f.worktree
				f.saveManifest(t)
			case "branch":
				f.manifest.Worktree.Branch = "main"
				f.saveManifest(t)
			case "worktree-disabled":
				f.manifest.Worktree.Enabled = false
				f.saveManifest(t)
			case "base":
				f.manifest.Repository.StartSHA = "HEAD"
				f.saveManifest(t)
			case "pin":
				f.manifest.Ralphex.SourceSHA = ""
				f.saveManifest(t)
			case "plan":
				os.WriteFile(f.binding.PlanPath, []byte("changed project"), 0600)
			case "input-escape":
				f.binding.InputDirectory = "../outside"
				writeJSON(t, f.bindingPath, f.binding)
			}
			if _, err := (Resolver{f.root, f.catalog}).Resolve(context.Background(), f.run); err == nil {
				t.Fatal("unsafe binding accepted")
			}
		})
	}
}

func TestCheckpointNeedsCleanExactDescendant(t *testing.T) {
	f := newBindingFixture(t)
	if _, ok := checkpoint(context.Background(), f.scope, testTime); ok {
		t.Fatal("base emitted")
	}
	path := filepath.Join(f.worktree, "source")
	os.WriteFile(path, []byte("changed\n"), 0600)
	if _, ok := checkpoint(context.Background(), f.scope, testTime); ok {
		t.Fatal("dirty emitted")
	}
	command(t, f.worktree, "add", "source")
	command(t, f.worktree, "commit", "-m", "change")
	e, ok := checkpoint(context.Background(), f.scope, testTime)
	if !ok || !e.CheckpointClean || e.CheckpointSHA != command(t, f.worktree, "rev-parse", "HEAD") {
		t.Fatal("no valid checkpoint", e)
	}
	s := newStore(t)
	first, err := s.Append(f.run, "reg", e)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Append(f.run, "reg", e)
	if err != nil || first.Ordinal != second.Ordinal {
		t.Fatal("checkpoint duplicate")
	}
	os.WriteFile(filepath.Join(f.worktree, "untracked"), []byte("dirty"), 0600)
	if _, ok := checkpoint(context.Background(), f.scope, testTime); ok {
		t.Fatal("untracked emitted")
	}
	os.Remove(filepath.Join(f.worktree, "untracked"))
	command(t, f.worktree, "checkout", "--detach")
	if _, ok := checkpoint(context.Background(), f.scope, testTime); ok {
		t.Fatal("detached emitted")
	}
	command(t, f.worktree, "checkout", "abcp/"+f.run)
	command(t, f.worktree, "checkout", "-b", "different")
	if _, ok := checkpoint(context.Background(), f.scope, testTime); ok {
		t.Fatal("changed branch emitted")
	}
	command(t, f.worktree, "checkout", "abcp/"+f.run)
	unprovable := f.scope
	unprovable.Base = strings.Repeat("a", 40)
	if _, ok := checkpoint(context.Background(), unprovable, testTime); ok {
		t.Fatal("unprovable ancestry emitted")
	}
	command(t, f.worktree, "checkout", "--orphan", "unrelated")
	command(t, f.worktree, "add", ".")
	command(t, f.worktree, "commit", "-m", "unrelated")
	other := command(t, f.worktree, "rev-parse", "HEAD")
	command(t, f.worktree, "checkout", "abcp/"+f.run)
	command(t, f.worktree, "reset", "--hard", other)
	if _, ok := checkpoint(context.Background(), f.scope, testTime); ok {
		t.Fatal("unrelated history emitted")
	}
}

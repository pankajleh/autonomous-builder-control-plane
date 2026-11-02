//go:build linux

package activity

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
)

// walledBindingFixture is newBindingFixture's run walled (Repo C design note BUILD_ISOLATION.md, phase 1): its
// worktree is in its own repository in its own folder, and its manifest names that folder.
func walledBindingFixture(t *testing.T) (*bindingFixture, string) {
	t.Helper()
	f := newBindingFixture(t)
	command(t, f.repo, "worktree", "remove", "--force", f.worktree)
	command(t, f.repo, "branch", "-D", "abcp/"+f.run)
	folder := filepath.Join(filepath.Dir(f.repo), "builds", f.run)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	runRepository := filepath.Join(folder, "repo")
	clone := exec.Command("git", "clone", "-q", "--no-local", "--single-branch", "--branch", "main", f.repo, runRepository)
	clone.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	command(t, runRepository, "config", "user.email", "test@example.test")
	command(t, runRepository, "config", "user.name", "Activity test")
	f.worktree = filepath.Join(runRepository, ".ralphex", "worktrees", "abcp", f.run)
	command(t, runRepository, "worktree", "add", "-q", "-b", "abcp/"+f.run, f.worktree)
	f.manifest.Isolation = &authority.IsolationManifest{RunFolder: folder}
	f.saveManifest(t)
	return f, folder
}

// observerFor is a real bubblewrap wall for ABCP's reads, or a skip.
func observerFor(t *testing.T) *runctl.Wall {
	t.Helper()
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bubblewrap is not installed")
	}
	bwrap, _ = filepath.EvalSymlinks(bwrap)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(runctl.BuildWallFileV1{SchemaVersion: 1, Bwrap: bwrap, BuildsRoot: filepath.Join(root, "builds"), Home: home, System: []string{"/usr", "/etc"}})
	file := filepath.Join(root, "wall.json")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	wall, err := runctl.LoadBuildWall(file)
	if err != nil {
		t.Fatal(err)
	}
	args, err := wall.ObserverArgs(nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(args[0], append(args[1:], "/usr/bin/true")...).CombinedOutput(); err != nil {
		t.Skipf("bubblewrap cannot start a wall here: %v %s", err, out)
	}
	return wall
}

func TestAWalledRunsWorktreeIsReadInsideAWallAndItsCheckpointWaitsForTheProductBranch(t *testing.T) {
	f, folder := walledBindingFixture(t)
	resolver := Resolver{f.root, f.catalog}
	observerWall.Store(nil)
	t.Cleanup(func() { observerWall.Store(nil) })
	// Without the observer wall a walled run's worktree is never read: its detail is unavailable.
	if _, err := resolver.Resolve(context.Background(), f.run); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a walled run read without its observer wall = %v", err)
	}
	scope, err := resolver.ResolveBinding(context.Background(), f.run)
	if err != nil || scope.RunFolder != folder || scope.RunRepository != filepath.Join(folder, "repo") || scope.Repository != f.repo {
		t.Fatalf("walled binding = %+v, %v", scope, err)
	}
	UseObserverWall(observerFor(t))
	scope, err = resolver.Resolve(context.Background(), f.run)
	if err != nil || scope.Worktree != f.worktree {
		t.Fatalf("walled scope = %+v, %v", scope, err)
	}

	// The build commits, and its repository names a program for Git to run on its files: a clean filter that would
	// write outside the run's folder. ABCP's reads run inside the observer wall, where the run's folder is read-only and
	// nothing else is, so the program never runs on the host.
	escaped := filepath.Join(filepath.Dir(f.repo), "escaped")
	command(t, filepath.Join(folder, "repo"), "config", "filter.evil.clean", "sh -c 'touch "+escaped+"; cat'")
	os.WriteFile(filepath.Join(f.worktree, ".gitattributes"), []byte("* filter=evil\n"), 0o600)
	os.WriteFile(filepath.Join(f.worktree, "source"), []byte("changed\n"), 0o600)
	command(t, f.worktree, "-c", "filter.evil.clean=cat", "add", ".")
	command(t, f.worktree, "-c", "filter.evil.clean=cat", "commit", "-qm", "change")
	head := command(t, f.worktree, "rev-parse", "HEAD")
	if _, err := os.Stat(escaped); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the test's own commit ran the filter")
	}
	// Until the branch is back in the product repository, no checkpoint is offered (previews read it there).
	if _, ok := checkpoint(context.Background(), scope, testTime); ok {
		t.Fatal("a checkpoint was offered before its branch was brought back")
	}
	if _, err := os.Stat(escaped); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ABCP ran the build's Git filter on the host")
	}
	command(t, f.repo, "fetch", "-q", filepath.Join(folder, "repo", ".git"), "refs/heads/abcp/"+f.run+":refs/heads/abcp/"+f.run)
	event, ok := checkpoint(context.Background(), scope, testTime)
	if !ok || event.CheckpointSHA != head {
		t.Fatalf("checkpoint after the branch came back = %+v %t", event, ok)
	}
	if _, err := os.Stat(escaped); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ABCP ran the build's Git filter on the host")
	}
	if kept, err := historyKept(context.Background(), scope, []string{head}); err != nil || !kept {
		t.Fatalf("history kept = %t, %v", kept, err)
	}
	if path, err := WorktreePath(context.Background(), scope); err != nil || !strings.HasPrefix(path, folder+"/") {
		t.Fatalf("worktree path = %q, %v", path, err)
	}
	// The control: the same read outside a wall does run the build's program, which is why ABCP never makes it.
	unwalled := scope
	unwalled.RunFolder = ""
	os.Chtimes(filepath.Join(f.worktree, "source"), testTime, testTime)
	checkpointClean(context.Background(), unwalled, f.worktree, head)
	if _, err := os.Stat(escaped); err != nil {
		t.Fatalf("the control did not run the filter, so the test proves nothing: %v", err)
	}
}

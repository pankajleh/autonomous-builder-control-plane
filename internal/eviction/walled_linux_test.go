//go:build linux

package eviction

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/activity"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
)

func TestAWalledRunsFolderIsRemovedWholeNeverThroughALink(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "builds", "admission-1")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{filepath.Join(folder, "repo", ".git"), filepath.Join(folder, "home", ".npm", "locked"), outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	kept := filepath.Join(outside, "secret")
	if err := os.WriteFile(kept, []byte("kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "home", ".npm", "locked", "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The build left a folder it may not write in and a link to a folder outside its own.
	if err := os.Chmod(filepath.Join(folder, "home", ".npm", "locked"), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(folder, "home", "link")); err != nil {
		t.Fatal(err)
	}
	scope := activity.Scope{Repository: filepath.Join(root, "product"), RunFolder: folder, RunRepository: filepath.Join(folder, "repo")}
	if worktreeRepository(scope) != scope.RunRepository || retainedFolder(scope, filepath.Join(folder, "repo", "w")) != folder {
		t.Fatal("a walled run's worktree is looked for outside its own repository")
	}
	if err := removeRunFolder(folder); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(folder); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the run's folder is still there: %v", err)
	}
	if data, err := os.ReadFile(kept); err != nil || string(data) != "kept\n" {
		t.Fatalf("removal reached outside the run's folder: %q, %v", data, err)
	}
	if info, err := os.Stat(outside); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the folder outside changed: %v, %v", info, err)
	}
	// A run folder that is itself a link is never followed.
	link := filepath.Join(root, "builds", "admission-2")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := removeRunFolder(link); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("a linked run folder = %v", err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatal("a linked run folder's target was removed")
	}
}

// A walled run that failed before Ralphex made its worktree (its wall, its engine, an early exit) leaves its folder:
// the clone, the home and the caches. Once the run is finished the sweeper removes it, though no worktree is retained.
func TestAFinishedWalledRunsFolderWithoutAWorktreeIsRemoved(t *testing.T) {
	walledWithout := func(t *testing.T, clone bool) (*fixture, string) {
		f := newFixture(t)
		folder := filepath.Join(t.TempDir(), "builds", testRun)
		if err := os.MkdirAll(filepath.Join(folder, "home", ".npm", "_cacache"), 0o700); err != nil {
			t.Fatal(err)
		}
		if clone {
			command := exec.Command("git", "clone", "-q", "--no-local", "--single-branch", "--branch", "main", f.scope.Repository, filepath.Join(folder, "repo"))
			command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
			if out, err := command.CombinedOutput(); err != nil {
				t.Fatalf("clone: %v %s", err, out)
			}
		}
		f.scope.RunFolder, f.scope.RunRepository = folder, filepath.Join(folder, "repo")
		return f, folder
	}
	for name, clone := range map[string]bool{"no repository": false, "a repository without a worktree": true} {
		t.Run(name, func(t *testing.T) {
			if clone {
				activity.UseObserverWall(observerWall(t))
				t.Cleanup(func() { activity.UseObserverWall(nil) })
			}
			// Unfinished, it is kept: a resume needs it.
			f, folder := walledWithout(t, clone)
			f.state = string(domain.StateImplementing)
			if _, err := f.sweeper().Sweep(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(folder); err != nil {
				t.Fatalf("an unfinished run's folder was removed: %v", err)
			}
			// Finished (failed), it goes, whether its profile retains worktrees or not.
			for _, retain := range []bool{true, false} {
				f, folder := walledWithout(t, clone)
				f.state, f.scope.Retain = string(domain.StateFailed), retain
				var log bytes.Buffer
				sweeper := f.sweeper()
				sweeper.Log = &log
				if _, err := sweeper.Sweep(context.Background()); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(folder); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("retain=%t: a finished run's folder without a worktree is kept: %v\n%s", retain, err, log.String())
				}
				if !strings.Contains(log.String(), "abcp run folder removed run="+testRun+" reason=no-worktree") {
					t.Fatalf("the removal was not logged: %q", log.String())
				}
				// Gone, the run is nothing the sweep keeps or cannot verify.
				if _, summary, err := sweeper.sweep(context.Background()); err != nil || summary != (Summary{}) {
					t.Fatalf("retain=%t: a sweep after the removal = %+v, %v", retain, summary, err)
				}
			}
		})
	}
	// A finished walled run whose worktree is there is the eviction policy's, not removed here.
	f, folder := walledWithout(t, true)
	activity.UseObserverWall(observerWall(t))
	t.Cleanup(func() { activity.UseObserverWall(nil) })
	worktree := filepath.Join(folder, "repo", ".ralphex", "worktrees", "abcp", testRun)
	run(t, filepath.Join(folder, "repo"), "worktree", "add", "--quiet", "-b", "abcp/"+testRun, worktree)
	f.state, f.finishedAt = string(domain.StateFailed), now.Add(-time.Hour)
	sweeper := f.sweeper()
	sweeper.Started = now.Add(-time.Hour)
	if _, err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("a retained walled worktree was removed outside the policy: %v", err)
	}
}

// observerWall is a real bubblewrap wall for ABCP's reads of a walled run, or a skip.
func observerWall(t *testing.T) *runctl.Wall {
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
	data, _ := json.Marshal(runctl.BuildWallFileV1{SchemaVersion: 1, Bwrap: bwrap, BuildsRoot: filepath.Join(root, "walls"), Home: home, System: []string{"/usr", "/etc"}})
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

// Repo C's release of a finished walled run with no worktree removes its folder; with the folder gone, a release again
// answers there is no worktree, never an error.
func TestReleasingAWalledRunWithoutAWorktreeRemovesItsFolder(t *testing.T) {
	f := newFixture(t)
	folder := filepath.Join(t.TempDir(), "builds", testRun)
	if err := os.MkdirAll(filepath.Join(folder, "home", ".npm"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.scope.RunFolder, f.scope.RunRepository = folder, filepath.Join(folder, "repo")
	f.state = string(domain.StateFailed)
	for range 2 {
		result, err := f.sweeper().Release(context.Background(), testRun, ReasonTaskClosed)
		if err != nil || result.Status != NoWorktree {
			t.Fatalf("release = %+v, %v", result, err)
		}
		if _, err := os.Lstat(folder); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the run's folder is still there: %v", err)
		}
	}
}

//go:build linux

package housekeeping

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
)

func git(t *testing.T, dir string, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.test"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out.String())
	}
	return strings.TrimSpace(out.String())
}

func objectPresent(dir, sha string) bool {
	cmd := exec.Command("git", "-C", dir, "cat-file", "-e", sha)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	return cmd.Run() == nil
}

func repository(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "product-repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "", "init", "--quiet", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "", "add", "README.md")
	git(t, repo, "", "commit", "--quiet", "-m", "base")
	return repo
}

// The nightly gc prunes an unreachable object older than the 14-day grace (a forgotten run's, once its refs are gone)
// and keeps a fresh one (a running build's) and every reachable one.
func TestGCPrunesAnOldUnreachableObjectAndKeepsAFreshOne(t *testing.T) {
	repo := repository(t)
	head := git(t, repo, "", "rev-parse", "HEAD")
	old := git(t, repo, "a forgotten app's code\n", "hash-object", "-w", "--stdin")
	fresh := git(t, repo, "a running build's code\n", "hash-object", "-w", "--stdin")
	longAgo := time.Now().Add(-20 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(repo, ".git", "objects", old[:2], old[2:]), longAgo, longAgo); err != nil {
		t.Fatal(err)
	}
	locked := 0
	lock := func(ctx context.Context, repository string) (io.Closer, error) {
		locked++
		if repository != repo {
			t.Fatalf("locked %s", repository)
		}
		return runctl.LockRepository(ctx, repository)
	}
	if err := GC(context.Background(), repo, lock); err != nil {
		t.Fatal(err)
	}
	if locked != 1 {
		t.Fatal("gc ran without the repository's lock")
	}
	if objectPresent(repo, old) {
		t.Fatal("an old unreachable object was kept")
	}
	if !objectPresent(repo, fresh) || !objectPresent(repo, head) {
		t.Fatal("a fresh unreachable object or a reachable one was pruned")
	}
}

// While a run holds the repository's lock (setting up its worktree), gc waits and never runs past its wait.
func TestGCWaitsForTheRepositorysLock(t *testing.T) {
	repo := repository(t)
	held, err := runctl.LockRepository(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	old := git(t, repo, "old\n", "hash-object", "-w", "--stdin")
	longAgo := time.Now().Add(-20 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(repo, ".git", "objects", old[:2], old[2:]), longAgo, longAgo); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := GC(ctx, repo, runctl.LockRepository); err == nil {
		t.Fatal("gc ran while a run held the repository's lock")
	}
	if !objectPresent(repo, old) {
		t.Fatal("gc pruned without the lock")
	}
	if err := GC(context.Background(), "relative/repo", runctl.LockRepository); err == nil {
		t.Fatal("a relative repository was accepted")
	}
}

// Session logs older than 30 days go, newer ones and the folders holding them stay; a link is removed, never followed;
// only a folder named sessions or projects is swept.
func TestRemoveOldSessionLogs(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, ".codex", "sessions")
	now := time.Date(2026, 11, 5, 23, 17, 0, 0, time.UTC)
	longAgo, recent := now.Add(-31*24*time.Hour), now.Add(-29*24*time.Hour)
	files := map[string]time.Time{
		"2026/09/01/rollout-a.jsonl":       longAgo,
		"2026/10/10/rollout-b.jsonl":       recent,
		"runs/admission-1/rollout-c.jsonl": longAgo,
	}
	for name, at := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("prompts\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(base, "auth.json")
	if err := os.WriteFile(outside, []byte("sign-in\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "2026", "09", "01", "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(root, "2026", "09", "01"), filepath.Join(root, "2026", "09"), filepath.Join(root, "runs", "admission-1")} {
		if err := os.Chtimes(dir, longAgo, longAgo); err != nil {
			t.Fatal(err)
		}
	}
	// The link's own time is now: it is new, and stays this time.
	removed, err := RemoveOldSessionLogs(root, now, SessionLogsMaxAge)
	if err != nil || removed != 2 {
		t.Fatalf("removed %d, %v", removed, err)
	}
	for name, at := range files {
		if _, err := os.Lstat(filepath.Join(root, name)); errors.Is(err, os.ErrNotExist) != at.Equal(longAgo) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("a link was followed")
	}
	if _, err := os.Lstat(filepath.Join(root, "2026", "10", "10")); err != nil {
		t.Fatal("a folder holding a new log was removed")
	}
	if _, err := os.Lstat(root); err != nil {
		t.Fatal("the session folder itself was removed")
	}
	for _, bad := range []string{filepath.Join(base, ".codex"), "relative/sessions", filepath.Join(base, ".claude")} {
		if _, err := RemoveOldSessionLogs(bad, now, SessionLogsMaxAge); err == nil {
			t.Fatalf("%s was swept", bad)
		}
	}
	if removed, err := RemoveOldSessionLogs(filepath.Join(base, "none", "projects"), now, SessionLogsMaxAge); err != nil || removed != 0 {
		t.Fatalf("a missing folder: %d, %v", removed, err)
	}
}

func TestNightlyRunsAtItsTimeOfDay(t *testing.T) {
	at, err := ParseAt(DefaultAt)
	if err != nil || at != 23*time.Hour+17*time.Minute {
		t.Fatal(at, err)
	}
	for _, bad := range []string{"", "23", "24:00", "7:05", "07:60", "07:05:00", "ab:cd"} {
		if _, err := ParseAt(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if got := untilNext(time.Date(2026, 11, 5, 20, 0, 0, 0, time.UTC), at); got != 3*time.Hour+17*time.Minute {
		t.Fatal(got)
	}
	if got := untilNext(time.Date(2026, 11, 5, 23, 17, 0, 0, time.UTC), at); got != 24*time.Hour {
		t.Fatal(got)
	}
	var log bytes.Buffer
	repo := repository(t)
	n := &Nightly{At: at, Repositories: []string{repo}, Lock: runctl.LockRepository, Now: time.Now, Log: &log}
	if !n.Enabled() || (&Nightly{}).Enabled() {
		t.Fatal("enabled wrongly")
	}
	n.Once(context.Background())
	if !strings.Contains(log.String(), "abcp nightly git gc repository=") || !strings.Contains(log.String(), "ok=true") {
		t.Fatalf("log = %q", log.String())
	}
}

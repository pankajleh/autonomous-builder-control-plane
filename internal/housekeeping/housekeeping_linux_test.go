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
	"strconv"
	"strings"
	"sync/atomic"
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

const (
	finishedRun = "admission-1111111111111111111111111111111111111111111111111111111111111111"
	runningRun  = "admission-2222222222222222222222222222222222222222222222222222222222222222"
)

func finishedOnly(run string) bool { return run == finishedRun }

// writeAged writes each file (made with its folders) last changed at its time.
func writeAged(t *testing.T, root string, files map[string]time.Time, data func(name string) string) {
	t.Helper()
	for name, at := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data(name)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
}

func codexLog(cwd string) string {
	return `{"timestamp":"2026-09-01T10:00:00Z","type":"session_meta","payload":{"id":"S1","cwd":"` + cwd + `","originator":"codex_exec"}}` + "\n" +
		`{"type":"response_item","payload":{"text":"the prompts"}}` + "\n"
}

// Only finished builds' session logs older than 30 days go: a walled build's runs/<run>/ (its memory/ kept), a build's
// Claude Code project folder from before builds were walled, and a Codex log whose first record names a build's working
// folder. A running build's folder, newer logs, a person's own sessions and memory/, and anything a link names stay.
func TestRemoveOldSessionLogs(t *testing.T) {
	base := t.TempDir()
	now := time.Date(2026, 11, 5, 23, 17, 0, 0, time.UTC)
	longAgo, recent := now.Add(-31*24*time.Hour), now.Add(-29*24*time.Hour)
	buildFolder := "/home/devagent/product-repo/.ralphex/worktrees/abcp/"
	slug := func(run string) string { return "-home-devagent-product-repo--ralphex-worktrees-abcp-" + run }

	codex := filepath.Join(base, ".codex", "sessions")
	codexFiles := map[string]time.Time{
		"runs/" + finishedRun + "/2026/09/01/rollout-a.jsonl": longAgo, // gone
		"runs/" + finishedRun + "/2026/10/10/rollout-b.jsonl": recent,
		"runs/" + runningRun + "/2026/09/01/rollout-c.jsonl":  longAgo, // a running build's: stays
		"2026/09/01/rollout-build.jsonl":                      longAgo, // a finished build's, from before walls: gone
		"2026/09/01/rollout-build-src.jsonl":                  longAgo, // the same, in a folder of its working folder: gone
		"2026/09/01/rollout-running.jsonl":                    longAgo, // a running build's: stays
		"2026/09/01/rollout-own.jsonl":                        longAgo, // a person's own: stays
		"2026/09/01/notes.txt":                                longAgo,
		"archived/rollout-x.jsonl":                            longAgo,
	}
	writeAged(t, codex, codexFiles, func(name string) string {
		switch filepath.Base(name) {
		case "rollout-build.jsonl":
			return codexLog(buildFolder + finishedRun)
		case "rollout-build-src.jsonl":
			return codexLog(buildFolder + finishedRun + "/src")
		case "rollout-running.jsonl":
			return codexLog(buildFolder + runningRun)
		case "rollout-own.jsonl":
			return codexLog("/home/devagent/notes")
		}
		return "the prompts\n"
	})
	claude := filepath.Join(base, ".claude", "projects")
	claudeFiles := map[string]time.Time{
		"runs/" + finishedRun + "/" + slug(finishedRun) + "/s1.jsonl":         longAgo, // gone
		"runs/" + finishedRun + "/" + slug(finishedRun) + "/memory/MEMORY.md": longAgo,
		slug(finishedRun) + "/s2.jsonl":                                       longAgo, // gone
		slug(finishedRun) + "-src/s3.jsonl":                                   longAgo, // gone
		slug(finishedRun) + "/memory/MEMORY.md":                               longAgo,
		slug(runningRun) + "/s4.jsonl":                                        longAgo,
		"-home-devagent-autonomous-development-platform/s5.jsonl":             longAgo, // a person's own: stays
		"-home-devagent-autonomous-development-platform/memory/MEMORY.md":     longAgo,
	}
	writeAged(t, claude, claudeFiles, func(string) string { return "the prompts\n" })
	// A link in a finished build's folder to a file outside: removed itself, never followed.
	outside := filepath.Join(base, "auth.json")
	if err := os.WriteFile(outside, []byte("sign-in\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(claude, slug(finishedRun), "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(outside, longAgo, longAgo); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("touch", "-h", "-d", "@"+strconv.FormatInt(longAgo.Unix(), 10), link).CombinedOutput(); err != nil {
		t.Fatalf("touch -h: %v %s", err, out)
	}
	// Folders unchanged for 30 days.
	for _, dir := range []string{filepath.Join(codex, "runs", finishedRun, "2026", "09", "01"), filepath.Join(codex, "runs", finishedRun, "2026", "09"),
		filepath.Join(claude, slug(finishedRun)+"-src"), filepath.Join(codex, "2026", "09", "01")} {
		if err := os.Chtimes(dir, longAgo, longAgo); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := RemoveOldSessionLogs(codex, now, SessionLogsMaxAge, finishedOnly)
	if err != nil || removed != 3 {
		t.Fatalf("codex: removed %d, %v", removed, err)
	}
	for name := range codexFiles {
		gone := map[string]bool{"runs/" + finishedRun + "/2026/09/01/rollout-a.jsonl": true, "2026/09/01/rollout-build.jsonl": true, "2026/09/01/rollout-build-src.jsonl": true}[name]
		if _, err := os.Lstat(filepath.Join(codex, name)); errors.Is(err, os.ErrNotExist) != gone {
			t.Fatalf("codex %s: %v", name, err)
		}
	}
	// The emptied folders of a finished build unchanged for 30 days go; a dated folder of the engine's own stays.
	if _, err := os.Lstat(filepath.Join(codex, "runs", finishedRun, "2026", "09")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an emptied old folder of a finished build stays: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(codex, "runs", finishedRun, "2026", "10", "10")); err != nil {
		t.Fatal("a folder holding a new log was removed")
	}

	removed, err = RemoveOldSessionLogs(claude, now, SessionLogsMaxAge, finishedOnly)
	if err != nil || removed != 4 {
		t.Fatalf("claude: removed %d, %v", removed, err)
	}
	for name := range claudeFiles {
		gone := strings.HasSuffix(name, "/s1.jsonl") || strings.HasSuffix(name, "/s2.jsonl") || strings.HasSuffix(name, "/s3.jsonl")
		if _, err := os.Lstat(filepath.Join(claude, name)); errors.Is(err, os.ErrNotExist) != gone {
			t.Fatalf("claude %s: %v", name, err)
		}
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an old link in a finished build's folder stays")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("a link was followed")
	}
	if _, err := os.Lstat(filepath.Join(claude, slug(finishedRun)+"-src")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an emptied old build project folder stays")
	}
	for _, root := range []string{codex, claude} {
		if _, err := os.Lstat(root); err != nil {
			t.Fatal("the session folder itself was removed")
		}
	}
	// Without a way to tell a finished run, nothing is aged.
	writeAged(t, claude, map[string]time.Time{slug(finishedRun) + "/s6.jsonl": longAgo}, func(string) string { return "x\n" })
	if removed, err := RemoveOldSessionLogs(claude, now, SessionLogsMaxAge, nil); err != nil || removed != 0 {
		t.Fatalf("no finished runs: removed %d, %v", removed, err)
	}
	for _, bad := range []string{filepath.Join(base, ".codex"), "relative/sessions", filepath.Join(base, ".claude")} {
		if _, err := RemoveOldSessionLogs(bad, now, SessionLogsMaxAge, finishedOnly); err == nil {
			t.Fatalf("%s was swept", bad)
		}
	}
	if removed, err := RemoveOldSessionLogs(filepath.Join(base, "none", "projects"), now, SessionLogsMaxAge, finishedOnly); err != nil || removed != 0 {
		t.Fatalf("a missing folder: %d, %v", removed, err)
	}
}

// A build that can still write its session folder (runs/<run>, bound read-write into its wall) swaps a folder for a link
// to a folder outside while the sweep walks it: the walk holds each folder open and removes only names in it, so it
// never reaches outside. (The sweep does not walk a running build's folder at all; here the build is taken for finished,
// as one whose process outlived it would be.)
func TestSessionSweepNeverFollowsASwappedFolder(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "codex", "sessions")
	runDir := filepath.Join(root, "runs", finishedRun)
	outside := filepath.Join(base, "product-repo", ".git")
	for _, dir := range []string{runDir, outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-400 * 24 * time.Hour)
	victim := filepath.Join(outside, "HEAD")
	if err := os.WriteFile(victim, []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(victim, old, old); err != nil {
		t.Fatal(err)
	}
	x, real := filepath.Join(runDir, "x"), filepath.Join(runDir, "x.real")
	var stop atomic.Bool
	done := make(chan struct{})
	go func() { // the build
		defer close(done)
		for !stop.Load() {
			os.MkdirAll(x, 0o700)
			f := filepath.Join(x, "HEAD")
			os.WriteFile(f, []byte("bait\n"), 0o600)
			os.Chtimes(f, old, old)
			os.Rename(x, real)
			os.Symlink(outside, x)
			time.Sleep(50 * time.Microsecond)
			os.Remove(x)
			os.Rename(real, x)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	sweeps := 0
	for ; time.Now().Before(deadline); sweeps++ {
		RemoveOldSessionLogs(root, time.Now(), SessionLogsMaxAge, finishedOnly)
		if _, err := os.Lstat(victim); err != nil {
			stop.Store(true)
			<-done
			t.Fatalf("after %d sweeps the sweep removed %s, outside the session folder: %v", sweeps+1, victim, err)
		}
	}
	stop.Store(true)
	<-done
	if _, err := os.Stat(outside); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d sweeps raced the build", sweeps)
}

// A running build's session folder is never walked: nothing in it is removed, however old.
func TestSessionSweepLeavesARunningBuildsFolder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "codex", "sessions")
	longAgo := time.Now().Add(-90 * 24 * time.Hour)
	writeAged(t, root, map[string]time.Time{"runs/" + runningRun + "/2026/08/01/rollout-a.jsonl": longAgo}, func(string) string { return "x\n" })
	if removed, err := RemoveOldSessionLogs(root, time.Now(), SessionLogsMaxAge, finishedOnly); err != nil || removed != 0 {
		t.Fatalf("removed %d, %v", removed, err)
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

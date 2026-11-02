//go:build linux

package run

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
)

// requireBwrap answers a bubblewrap that starts a wall here, or skips: the integration tests need the real one.
func requireBwrap(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bubblewrap is not installed")
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	args := []string{"--ro-bind", "/usr", "/usr", "--unshare-pid", "--dev", "/dev", "--proc", "/proc"}
	for _, link := range []string{"/bin", "/lib", "/lib64"} {
		if target, err := os.Readlink(link); err == nil {
			args = append(args, "--symlink", target, link)
		}
	}
	if output, err := exec.Command(path, append(args, "--", "/usr/bin/true")...).CombinedOutput(); err != nil {
		t.Skipf("bubblewrap cannot start a wall here: %v %s", err, output)
	}
	return path
}

// inheritFile opens path read-write as a file a child would inherit (not close-on-exec), at a number no other file of
// this test process has, as abcp serve's launch lock is in abcp run; it is closed when the test ends.
func inheritFile(t *testing.T, path string) int {
	t.Helper()
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	const inherited = 77
	if _, err := os.Lstat("/proc/self/fd/" + strconv.Itoa(inherited)); err == nil {
		t.Fatalf("file %d is in use", inherited)
	}
	if err := syscall.Dup3(fd, inherited, 0); err != nil {
		t.Fatal(err)
	}
	syscall.Close(fd)
	t.Cleanup(func() { syscall.Close(inherited) })
	if closeOnExec(t, inherited) {
		t.Fatal("the inherited file is close-on-exec already")
	}
	return inherited
}

func closeOnExec(t *testing.T, fd int) bool {
	t.Helper()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	return flags&syscall.FD_CLOEXEC != 0
}

// wallCanaries are files and folders no build may see, made for one test.
type wallCanaries struct {
	otherRun, secret, sshKey, homeSignIn, otherPlan, productFile, productGit, socket string
	branchCommit, branchBlob                                                         string
}

func makeWallCanaries(t *testing.T, dirs wallDirs, product string) wallCanaries {
	t.Helper()
	c := wallCanaries{
		otherRun: filepath.Join(dirs.builds, "admission-other", "repo", "canary.txt"),
		secret:   filepath.Join(dirs.root, "runtime", "secrets", "isolation-canary"),
		sshKey:   filepath.Join(dirs.home, ".ssh", "id_ed25519"), homeSignIn: filepath.Join(dirs.home, ".codex", "auth.json"),
		otherPlan: filepath.Join(product, ".abcp-input", "admission-other", "plan.md"), productFile: filepath.Join(product, "context.md"),
		productGit: filepath.Join(product, ".git", "HEAD"),
	}
	// A socket like Docker's (a short path: socket paths are bounded).
	short, err := os.MkdirTemp("", "dock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	c.socket = filepath.Join(short, "docker.sock")
	for path, mode := range map[string]os.FileMode{c.otherRun: 0o600, c.secret: 0o600, c.sshKey: 0o600, c.homeSignIn: 0o600, c.otherPlan: 0o600} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, path, []byte("CANARY-"+filepath.Base(filepath.Dir(path))+"\n"), mode)
	}
	listener, err := net.Listen("unix", c.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	// Another build's branch in the product repository, holding a canary of its own.
	runGit(t, product, "checkout", "-q", "-b", "abcp/admission-canary")
	writeTestFile(t, filepath.Join(product, "other-customer.txt"), []byte("CANARY-OTHER-CUSTOMERS-APP\n"), 0o600)
	runGit(t, product, "add", "other-customer.txt")
	runGit(t, product, "commit", "-qm", "another customer's app")
	c.branchCommit = runGit(t, product, "rev-parse", "HEAD")
	c.branchBlob = runGit(t, product, "rev-parse", "HEAD:other-customer.txt")
	runGit(t, product, "checkout", "-q", "main")
	return c
}

// walledFixture is a product run whose Ralphex is the given script, walled by a real bubblewrap, with an app folder it
// may read (version/index.html).
func walledFixture(t *testing.T, bwrap string, script func(wallDirs, string) string) (runFixture, *Wall, wallDirs, string) {
	t.Helper()
	wall, dirs, err := writeTestWall(t, bwrap, nil)
	if err != nil {
		t.Fatal(err)
	}
	version := filepath.Join(dirs.reads, testApp, "v1", "files")
	if err := os.MkdirAll(version, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(version, "index.html"), []byte("<p>the app's earlier version</p>\n"), 0o600)
	fixture := newRunFixtureWithScript(t, script(dirs, version), authority.WorktreePolicy{Enabled: true, Branch: "abcp/run-fixture", Retain: false}, commandPath(t, "true"))
	configureIgnoredAuthorityPlan(t, &fixture, []byte("### Task 1: walled work\n\n- [ ] implement it\n"))
	manifest := fixture.authority.Manifest()
	manifest.Isolation = &authority.IsolationManifest{RunFolder: wall.RunFolder(manifest.RunID), ReadPaths: []string{version}, ProductTaskID: testApp}
	fixture.authority = fixture.admit(t, manifest)
	return fixture, wall, dirs, version
}

// fakeWalledRalphex makes the worktree and commits the plan copy and a candidate, as Ralphex does, in the repository
// it starts in, after body.
func fakeWalledRalphex(body string) string {
	return `#!/bin/sh
branch=
plan=
while [ "$#" -gt 0 ]; do
	case "$1" in
		--branch) branch=$2; shift 2 ;;
		--*) shift ;;
		*) plan=$1; shift ;;
	esac
done
` + body + `
[ -n "$branch" ] || exit 20
[ -r "$plan" ] || exit 21
root=$(git rev-parse --show-toplevel) || exit 24
rel=${plan#"$root"/}
worktree="$root/.ralphex/worktrees/$branch"
git worktree add -q -b "$branch" "$worktree" HEAD || exit 26
mkdir -p "$worktree/$(dirname "$rel")" || exit 27
cp "$plan" "$worktree/$rel" || exit 28
cd "$worktree" || exit 29
git add "$rel" || exit 31
git commit -qm 'add execution plan' || exit 32
printf 'candidate\n' > candidate.txt
git add candidate.txt || exit 33
git commit -qm 'candidate implementation' || exit 34
`
}

func TestTheWallShowsABuildItsOwnFolderAndNoOtherBuildSecretRepositoryOrSocket(t *testing.T) {
	bwrap := requireBwrap(t)
	var canaries wallCanaries
	var report string
	fixture, wall, dirs, version := walledFixture(t, bwrap, func(dirs wallDirs, version string) string {
		report = filepath.Join(dirs.builds, "run-fixture", "report.txt")
		return fakeWalledRalphex(fmt.Sprintf(`
report=%q
check() { if [ -e "$1" ] || [ -L "$1" ]; then echo "OPEN $1"; else echo "BLOCKED $1"; fi; }
{
for p in %s; do check "$p"; done
if git rev-parse --verify -q abcp/admission-canary >/dev/null 2>&1; then echo "OPEN branch"; else echo "BLOCKED branch"; fi
if git cat-file -e "$CANARY_BLOB" 2>/dev/null; then echo "OPEN object"; else echo "BLOCKED object"; fi
if kill -0 %d 2>/dev/null; then echo "OPEN abcp-process"; else echo "BLOCKED abcp-process"; fi
if (echo x > %q/write-attempt) 2>/dev/null; then echo "OPEN read-path-write"; else echo "BLOCKED read-path-write"; fi
if touch %q/written 2>/dev/null; then echo "OPEN tools-write"; else echo "BLOCKED tools-write"; fi
fds=$(ls /proc/self/fd | tr '\n' ' ')
if [ "$fds" = "0 1 2 3 " ]; then echo "BLOCKED inherited-files"; else echo "OPEN inherited-files $fds"; fi
[ "$HOME" = %q ] && touch "$HOME/own-home" && echo "OWN home"
touch /tmp/own-tmp && echo "OWN tmp"
touch "$PWD/own-repository" && echo "OWN repository"
grep -q 'earlier version' %q/index.html && echo "OWN read-path"
grep -q 'sign-in' "$CODEX_HOME/auth.json" && echo "OWN sign-in"
mkdir -p "$CODEX_HOME/sessions/2026/11/02" && echo '{}' > "$CODEX_HOME/sessions/2026/11/02/rollout-wall.jsonl" && echo "OWN sessions"
[ "$(ls /proc | grep -c '^[0-9]')" -lt 10 ] && echo "OWN pid-namespace"
} > "$report" 2>&1
rm -f "$PWD/own-repository"
`, report, "$CANARIES", os.Getpid(), version, dirs.tools, dirs.home, version))
	})
	canaries = makeWallCanaries(t, dirs, fixture.authority.Repository().Path)
	writeTestFile(t, canaries.homeSignIn, []byte(`{"auth_mode":"sign-in"}`+"\n"), 0o600)
	// The canaries' paths and the other branch's object reach the fake agent in its script, the only way in.
	list := strings.Join([]string{canaries.otherRun, filepath.Dir(canaries.otherRun), canaries.secret, filepath.Dir(canaries.secret), canaries.sshKey,
		filepath.Join(dirs.home, ".codex"), canaries.otherPlan, canaries.productFile, canaries.productGit, canaries.socket,
		"/var/run/docker.sock", "/run/docker.sock", "/run/user/" + strconv.Itoa(os.Getuid()), dirs.file, fixture.ledgerPath, fixture.evidence}, " ")
	manifest := fixture.authority.Manifest()
	script, err := os.ReadFile(manifest.Ralphex.BinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.Replace(strings.Replace(string(script), `$CANARIES`, list, 1), `"$CANARY_BLOB"`, canaries.branchBlob, 1))
	if err := os.Chmod(manifest.Ralphex.BinaryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, manifest.Ralphex.BinaryPath, script, 0o700)
	manifest.Ralphex.BinarySHA256 = testHash(t, manifest.Ralphex.BinaryPath)
	fixture.authority = fixture.admit(t, manifest)

	// abcp serve hands abcp run its launch lock as a file it inherits; abcp run marks every such file close-on-exec
	// before it starts anything, so the build inside its wall has none.
	lock := inheritFile(t, filepath.Join(dirs.root, "launch.lock"))
	if err := CloseInheritedFilesOnExec(); err != nil {
		t.Fatal(err)
	}
	if !closeOnExec(t, lock) {
		t.Fatal("the inherited file is not close-on-exec")
	}
	runner := fixture.runner(t)
	if err := runner.SetBuildWall(wall); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background())
	if err != nil || !result.Accepted() {
		stderr, _ := os.ReadFile(result.Ralphex.StderrRef.URI)
		stdout, _ := os.ReadFile(result.Ralphex.StdoutRef.URI)
		reported, _ := os.ReadFile(report)
		t.Fatalf("walled run = %s %q exit %d, %v\nstdout:\n%s\nstderr:\n%s\nreport:\n%s", result.State, result.FailureReason, result.Ralphex.ExitCode, err, stdout, stderr, reported)
	}
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	blocked, own := 0, map[string]bool{}
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "OPEN "):
			t.Errorf("the build saw what it must not: %s", line)
		case strings.HasPrefix(line, "BLOCKED "):
			blocked++
		case strings.HasPrefix(line, "OWN "):
			own[strings.TrimPrefix(line, "OWN ")] = true
		default:
			t.Errorf("unexpected report line %q", line)
		}
	}
	if blocked != 16+6 {
		t.Errorf("blocked = %d, want %d:\n%s", blocked, 16+6, data)
	}
	for _, want := range []string{"home", "tmp", "repository", "read-path", "sign-in", "sessions", "pid-namespace"} {
		if !own[want] {
			t.Errorf("the build could not use its own %s:\n%s", want, data)
		}
	}

	product := fixture.authority.Repository().Path
	runFolder := wall.RunFolder(manifest.RunID)
	runRepository := filepath.Join(runFolder, "repo")
	// The branch came back into the product repository; the run's worktree and progress stayed in its own folder.
	if head := runGit(t, product, "show", "refs/heads/abcp/run-fixture:candidate.txt"); head != "candidate" {
		t.Fatalf("the branch was not brought back: %q", head)
	}
	if listed := runGit(t, product, "worktree", "list", "--porcelain"); strings.Contains(listed, "abcp/run-fixture") {
		t.Fatalf("the product repository lists the run's worktree:\n%s", listed)
	}
	if _, err := os.Stat(filepath.Join(runRepository, ".ralphex", "worktrees", "abcp", "run-fixture", "candidate.txt")); err != nil {
		t.Fatalf("the run's worktree is not in its own repository: %v", err)
	}
	// The run's repository held the start commit's history only: no other build's branch or object.
	if all := runGit(t, runRepository, "rev-list", "--all"); strings.Contains(all, canaries.branchCommit) {
		t.Fatal("the run's repository holds another build's commit")
	}
	if err := exec.Command("git", "-C", runRepository, "cat-file", "-e", canaries.branchBlob).Run(); err == nil {
		t.Fatal("the run's repository holds another build's object")
	}
	if remotes := runGit(t, runRepository, "remote"); remotes != "" {
		t.Fatalf("the run's repository has remotes: %q", remotes)
	}
	if _, err := os.Lstat(filepath.Join(runRepository, ".git", "objects", "info", "alternates")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the run's repository borrows objects")
	}
	// The session the engine wrote is where the meter reads, six levels down at most.
	if _, err := os.Stat(filepath.Join(dirs.home, ".codex", "sessions", "runs", manifest.RunID, "2026", "11", "02", "rollout-wall.jsonl")); err != nil {
		t.Fatalf("the engine's session is not in its run's session folder: %v", err)
	}
	// The read path was not written and the plan copy is gone.
	if _, err := os.Stat(filepath.Join(version, "write-attempt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the build wrote in its app's earlier version")
	}
	if matches, _ := filepath.Glob(filepath.Join(runRepository, "abcp-ralphex-plan-*")); len(matches) != 0 {
		t.Fatalf("the plan copy was left: %v", matches)
	}
	// The start's evidence names the wall, never a value.
	var isolation map[string]any
	for _, event := range readEvents(t, fixture.ledgerPath) {
		if event.StateTo == domain.StateExecutionStarting {
			isolation, _ = event.Payload["isolation"].(map[string]any)
		}
	}
	if isolation == nil || isolation["kind"] != BuildIsolationKindV1 || isolation["run_folder"] != runFolder || isolation["sign_in"] != "auth.json" ||
		isolation["read_paths"] != float64(1) || len(fmt.Sprint(isolation["binds_sha256"])) != 64 {
		t.Fatalf("EXECUTION_STARTING isolation = %v", isolation)
	}
	if strings.Contains(fmt.Sprint(isolation), "sign-in\"") || strings.Contains(fmt.Sprint(isolation), "CANARY") {
		t.Fatalf("the evidence holds a value: %v", isolation)
	}
}

func TestAWalledRunFailsClosedWhenItsWallCannotStart(t *testing.T) {
	started := filepath.Join(t.TempDir(), "ralphex-started")
	fixture, _, dirs, _ := walledFixture(t, "", func(wallDirs, string) string {
		return fakeWalledRalphex(": > " + started)
	})
	// The fake bubblewrap answers its version and refuses every wall, as one without its AppArmor profile does.
	wall, err := LoadBuildWall(dirs.file)
	if err != nil {
		t.Fatal(err)
	}
	runner := fixture.runner(t)
	if err := runner.SetBuildWall(wall); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background())
	if err == nil || result.State != domain.StateFailed || !strings.Contains(result.FailureReason, "build_wall_unavailable") {
		t.Fatalf("run with a wall that cannot start = %s %q, %v", result.State, result.FailureReason, err)
	}
	if _, err := os.Stat(started); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Ralphex started without its wall")
	}
	if exists := exec.Command("git", "-C", fixture.authority.Repository().Path, "rev-parse", "--verify", "-q", "refs/heads/abcp/run-fixture").Run(); exists == nil {
		t.Fatal("a branch came back from a run that never started")
	}
	// A wall file that does not load fails the run the same way, before anything starts.
	other, _, _, _ := walledFixture(t, "", func(wallDirs, string) string { return fakeWalledRalphex(": > " + started) })
	runner = other.runner(t)
	if err := runner.SetBuildWallFile(filepath.Join(t.TempDir(), "missing-wall.json"), strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if result, _ := runner.Run(context.Background()); result.State != domain.StateFailed || !strings.Contains(result.FailureReason, "build_wall_unavailable") {
		t.Fatalf("run with a missing wall file = %s %q", result.State, result.FailureReason)
	}
	// A wall file that is not the one abcp serve probed (it changed since) fails the run the same way.
	changed, _, changedDirs, _ := walledFixture(t, "", func(wallDirs, string) string { return fakeWalledRalphex(": > " + started) })
	probed, err := BuildWallFileSHA256(changedDirs.file)
	if err != nil {
		t.Fatal(err)
	}
	runner = changed.runner(t)
	if err := runner.SetBuildWallFile(changedDirs.file, strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if result, _ := runner.Run(context.Background()); result.State != domain.StateFailed || !strings.Contains(result.FailureReason, "not the one abcp serve probed") {
		t.Fatalf("run with a wall file other than the one probed = %s %q", result.State, result.FailureReason)
	}
	if err := changed.runner(t).SetBuildWallFile(changedDirs.file, "not-a-digest"); err == nil {
		t.Fatal("a wall file without its probed digest was accepted")
	}
	if len(probed) != 64 {
		t.Fatalf("the wall file's digest = %q", probed)
	}
	// A run whose authority names no run folder is never walled into a folder of its own choosing.
	legacy, wall2, _, _ := walledFixture(t, "", func(wallDirs, string) string { return fakeWalledRalphex(": > " + started) })
	manifest := legacy.authority.Manifest()
	manifest.Isolation = nil
	legacy.authority = legacy.admit(t, manifest)
	runner = legacy.runner(t)
	if err := runner.SetBuildWall(wall2); err != nil {
		t.Fatal(err)
	}
	if result, _ := runner.Run(context.Background()); result.State != domain.StateFailed || !strings.Contains(result.FailureReason, "names no run folder") {
		t.Fatalf("run admitted before the wall = %s %q", result.State, result.FailureReason)
	}
	if _, err := os.Stat(started); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Ralphex started without its wall")
	}
}

func TestTheProbeFindsNoCanaryInARealWallAndRefusesOneThatShowsOne(t *testing.T) {
	bwrap := requireBwrap(t)
	wall, dirs, err := writeTestWall(t, bwrap, nil)
	if err != nil {
		t.Fatal(err)
	}
	canaries := makeWallCanaries(t, dirs, func() string {
		product := filepath.Join(dirs.root, "product")
		runGit(t, "", "init", "-q", "-b", "main", product)
		runGit(t, product, "config", "user.email", "p@example.test")
		runGit(t, product, "config", "user.name", "P")
		writeTestFile(t, filepath.Join(product, "context.md"), []byte("x\n"), 0o600)
		runGit(t, product, "add", "context.md")
		runGit(t, product, "commit", "-qm", "start")
		return product
	}())
	extra := []string{canaries.secret, canaries.otherRun, canaries.productGit, canaries.socket}
	if err := wall.Probe(context.Background(), extra); err != nil {
		t.Fatalf("probe of a real wall = %v", err)
	}
	// A canary the wall shows (here a system folder every build is shown) fails the probe, and names it.
	if err := wall.Probe(context.Background(), append(extra, "/usr/bin")); !errors.Is(err, ErrBuildWallUnavailable) || !strings.Contains(err.Error(), "/usr/bin") {
		t.Fatalf("probe of a wall that shows a canary = %v", err)
	}
	if entries, _ := os.ReadDir(dirs.builds); len(entries) != 1 || entries[0].Name() != "admission-other" {
		t.Fatalf("the probe left folders in the builds root: %v", entries)
	}
}

// walledRunFor is a walled run of product's run id, its folder made, without a Runner.
func walledRunFor(t *testing.T, wall *Wall, product, runID string) *walledRun {
	t.Helper()
	folder := wall.RunFolder(runID)
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	return &walledRun{wall: wall, runID: runID, folder: folder, repository: filepath.Join(folder, "repo"), product: product,
		branch: "abcp/" + runID, startSHA: runGit(t, product, "rev-parse", "HEAD"), env: map[string]string{}}
}

func TestTheRunsRepositoryIsAFreshCloneAndItsBranchComesBackFastForwardOnly(t *testing.T) {
	wall, dirs, err := writeTestWall(t, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	product := filepath.Join(dirs.root, "product")
	runGit(t, "", "init", "-q", "-b", "main", product)
	runGit(t, product, "config", "user.email", "product@example.test")
	runGit(t, product, "config", "user.name", "Product")
	writeTestFile(t, filepath.Join(product, "README.md"), []byte("start\n"), 0o600)
	runGit(t, product, "add", "README.md")
	runGit(t, product, "commit", "-qm", "start")
	canaries := makeWallCanaries(t, dirs, product)
	w := walledRunFor(t, wall, product, "admission-fetch")
	if err := w.cloneRepository(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Only the start commit's history, no remote, no borrowed objects, the product repository's identity.
	if all, start := runGit(t, w.repository, "rev-list", "--all"), runGit(t, product, "rev-list", "main"); all != start {
		t.Fatalf("the run's repository holds %q, want %q", all, start)
	}
	if err := exec.Command("git", "-C", w.repository, "cat-file", "-e", canaries.branchBlob).Run(); err == nil {
		t.Fatal("the run's repository holds another build's object")
	}
	if remotes := runGit(t, w.repository, "remote"); remotes != "" {
		t.Fatalf("remotes = %q", remotes)
	}
	if name := runGit(t, w.repository, "config", "--local", "user.name"); name != "Product" {
		t.Fatalf("the run's commits would not carry the product repository's identity: %q", name)
	}
	if count := runGit(t, w.repository, "count-objects", "-v"); !strings.Contains(count, "count: ") {
		t.Fatalf("count-objects = %q", count)
	}

	// No branch yet: nothing to bring back.
	if err := w.bringBack(context.Background()); err != nil {
		t.Fatal(err)
	}
	runGit(t, w.repository, "checkout", "-q", "-b", w.branch)
	writeTestFile(t, filepath.Join(w.repository, "app.txt"), []byte("one\n"), 0o600)
	runGit(t, w.repository, "add", "app.txt")
	runGit(t, w.repository, "commit", "-qm", "one")
	first := runGit(t, w.repository, "rev-parse", "HEAD")
	if err := w.bringBack(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, product, "rev-parse", "refs/heads/"+w.branch); got != first {
		t.Fatalf("product branch = %s, want %s", got, first)
	}
	writeTestFile(t, filepath.Join(w.repository, "app.txt"), []byte("two\n"), 0o600)
	runGit(t, w.repository, "commit", "-qam", "two")
	second := runGit(t, w.repository, "rev-parse", "HEAD")
	if err := w.bringBack(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, product, "rev-parse", "refs/heads/"+w.branch); got != second {
		t.Fatalf("product branch = %s, want the fast-forward %s", got, second)
	}
	// A rewritten branch is never brought back; the product repository keeps what it had.
	runGit(t, w.repository, "reset", "-q", "--hard", first)
	writeTestFile(t, filepath.Join(w.repository, "app.txt"), []byte("rewritten\n"), 0o600)
	runGit(t, w.repository, "commit", "-qam", "rewritten")
	if err := w.bringBack(context.Background()); !errors.Is(err, ErrBranchRewritten) {
		t.Fatalf("a rewritten branch = %v, want BRANCH_REWRITTEN", err)
	}
	if got := runGit(t, product, "rev-parse", "refs/heads/"+w.branch); got != second {
		t.Fatalf("the rewrite reached the product repository: %s", got)
	}
	// A repository the build replaced with a link is not read.
	if err := os.Rename(filepath.Join(w.repository, ".git"), filepath.Join(w.folder, "moved.git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(product, ".git"), filepath.Join(w.repository, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := w.bringBack(context.Background()); err == nil {
		t.Fatal("a linked repository was read")
	}
}

func TestAResumedWalledRunFindsItsFolderAndAFreshRunNeverReusesOne(t *testing.T) {
	fixture, wall, _, _ := walledFixture(t, "", func(wallDirs, string) string { return fakeWalledRalphex("") })
	fresh := fixture.runner(t)
	fresh.wall = wall
	w, err := fresh.prepareWalledRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(w.repository, "kept-by-the-first-attempt")
	writeTestFile(t, marker, []byte("x\n"), 0o600)
	again := fixture.runner(t)
	again.wall = wall
	if _, err := again.prepareWalledRun(context.Background()); !errors.Is(err, ErrBuildWallUnavailable) || !strings.Contains(err.Error(), "there already") {
		t.Fatalf("a fresh run over an existing folder = %v", err)
	}
	resumed := fixture.runner(t)
	resumed.wall, resumed.resume = wall, true
	found, err := resumed.prepareWalledRun(context.Background())
	if err != nil || found.repository != w.repository {
		t.Fatalf("resumed run = %+v, %v", found, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the resumed run's repository was made again: %v", err)
	}
	if err := os.RemoveAll(w.folder); err != nil {
		t.Fatal(err)
	}
	lost := fixture.runner(t)
	lost.wall, lost.resume = wall, true
	if _, err := lost.prepareWalledRun(context.Background()); !errors.Is(err, ErrBuildWallUnavailable) || !strings.Contains(err.Error(), "not there") {
		t.Fatalf("a resumed run without its folder = %v", err)
	}
}

func TestAWalledRunWhoseBranchIsRewrittenFails(t *testing.T) {
	bwrap := requireBwrap(t)
	previous := fetchBackInterval
	fetchBackInterval = 20 * time.Millisecond
	t.Cleanup(func() { fetchBackInterval = previous })
	fixture, wall, _, _ := walledFixture(t, bwrap, func(wallDirs, string) string {
		// After Ralphex's commits the branch is brought back; then the build rewrites it.
		return strings.TrimSuffix(fakeWalledRalphex(""), "\n") + `
sleep 1
git reset -q --hard HEAD~1 || exit 40
printf 'rewritten\n' > candidate.txt
git add candidate.txt && git commit -qm 'rewritten candidate' || exit 41
sleep 1
`
	})
	runner := fixture.runner(t)
	if err := runner.SetBuildWall(wall); err != nil {
		t.Fatal(err)
	}
	result, _ := runner.Run(context.Background())
	if result.State != domain.StateFailed || !strings.Contains(result.FailureReason, "BRANCH_REWRITTEN") {
		t.Fatalf("a rewritten branch's run = %s %q", result.State, result.FailureReason)
	}
}

func TestAWalledRunWhoseBranchIsRewrittenAndPutBackStillFails(t *testing.T) {
	bwrap := requireBwrap(t)
	previous := fetchBackInterval
	fetchBackInterval = 20 * time.Millisecond
	t.Cleanup(func() { fetchBackInterval = previous })
	fixture, wall, _, _ := walledFixture(t, bwrap, func(wallDirs, string) string {
		// The candidate is brought back; the build rewrites it (refused), then puts it back and builds on it, so the
		// last fetch back is a fast-forward again.
		return strings.TrimSuffix(fakeWalledRalphex(""), "\n") + `
sleep 1
kept=$(git rev-parse HEAD)
git reset -q --hard HEAD~1 || exit 40
printf 'rewritten\n' > candidate.txt
git add candidate.txt && git commit -qm 'rewritten candidate' || exit 41
sleep 1
git reset -q --hard "$kept" || exit 42
printf 'more\n' > more.txt
git add more.txt && git commit -qm 'more on the kept candidate' || exit 43
`
	})
	runner := fixture.runner(t)
	if err := runner.SetBuildWall(wall); err != nil {
		t.Fatal(err)
	}
	result, _ := runner.Run(context.Background())
	if result.State != domain.StateFailed || !strings.Contains(result.FailureReason, "BRANCH_REWRITTEN") {
		t.Fatalf("a branch rewritten and put back = %s %q", result.State, result.FailureReason)
	}
}

func TestAWalledRunsBranchStartsAtItsStartCommitSoAnUnrelatedOneIsNeverTaken(t *testing.T) {
	bwrap := requireBwrap(t)
	fixture, wall, _, _ := walledFixture(t, bwrap, func(wallDirs, string) string {
		// The build's branch holds a history of its own, unrelated to the start commit.
		return `#!/bin/sh
branch=
while [ "$#" -gt 0 ]; do
	case "$1" in
		--branch) branch=$2; shift 2 ;;
		--*) shift ;;
		*) shift ;;
	esac
done
[ -n "$branch" ] || exit 20
root=$(git rev-parse --show-toplevel) || exit 24
worktree="$root/.ralphex/worktrees/$branch"
git worktree add -q --detach "$worktree" HEAD || exit 26
cd "$worktree" || exit 29
git checkout -q --orphan "$branch" || exit 30
git rm -rqf --cached . >/dev/null 2>&1
printf 'unrelated\n' > candidate.txt
git add candidate.txt && git commit -qm 'an unrelated history' || exit 34
`
	})
	product := fixture.authority.Repository().Path
	start := fixture.authority.Repository().StartSHA
	runner := fixture.runner(t)
	if err := runner.SetBuildWall(wall); err != nil {
		t.Fatal(err)
	}
	result, _ := runner.Run(context.Background())
	if result.State != domain.StateFailed || !strings.Contains(result.FailureReason, "BRANCH_REWRITTEN") {
		t.Fatalf("an unrelated branch's run = %s %q", result.State, result.FailureReason)
	}
	// The product repository's branch was made at the start commit before Ralphex started, and kept it.
	if head := runGit(t, product, "rev-parse", "refs/heads/abcp/run-fixture"); head != start {
		t.Fatalf("the product repository's branch = %s, want the start commit %s", head, start)
	}
	if reflog := runGit(t, product, "reflog", "show", "--format=%gs", "refs/heads/abcp/run-fixture"); !strings.Contains(reflog, "starts at its start commit") {
		t.Fatalf("the branch was not started by ABCP: %q", reflog)
	}
}

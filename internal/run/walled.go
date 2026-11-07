package run

// A walled run (Repo C design note BUILD_ISOLATION.md §5.1, plan docs/plans/build-isolation.md): the run gets a folder
// of its own, <builds root>/<run id>, with a repository of its own made through Git's transport from the product
// repository (never --local, --shared or --reference, which would hand it every other build's objects), its own home,
// /tmp and engine home. Ralphex starts in that repository inside the wall; the run's branch is brought back into the
// product repository, fast-forward only, as it moves and when Ralphex exits, so previews, activity, eviction, resume
// and Repo C read it there as before.
//
// Once Ralphex has run, the run's repository is the build's to write. ABCP then only fetches from it (upload-pack,
// which Git makes safe to run on a repository it does not trust) and never runs other Git commands in it outside a wall.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/enginelane"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/openmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

// ErrBranchRewritten says the run's branch cannot be brought back fast-forward: its history was rewritten in the
// run's repository. The run fails rather than accept a branch the product repository never saw.
var ErrBranchRewritten = errors.New("BRANCH_REWRITTEN: the run's branch cannot be brought back into the product repository fast-forward")

// fetchBackInterval is how often a running build's branch is looked at, to be brought back as it moves.
var fetchBackInterval = 2 * time.Second

const maxEngineConfigBytes = 256 << 10

type walledRun struct {
	wall       *Wall
	runID      string
	folder     string
	repository string
	home, tmp  string
	engineHome string
	product    string
	branch     string
	startSHA   string
	readPaths  []string
	planCopy   string
	readOnly   []string
	mounts     []WallMount
	env        map[string]string
	evidence   map[string]any

	mu      sync.Mutex
	fetched string
	// rewrites counts the times the branch could not be brought back fast-forward; one fails the run, even when the
	// build puts the branch back afterwards.
	rewrites int
}

// SetBuildWall walls every Ralphex run of this runner (Repo C design note BUILD_ISOLATION.md, phase 1). abcp run sets
// it from --build-wall-file and refuses to run without one; a runner without it runs as before, which only tests do.
// It must be configured before Run starts.
func (r *Runner) SetBuildWall(wall *Wall) error {
	if r == nil || wall == nil {
		return errors.New("build wall is required")
	}
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.started {
		return errors.New("runner has already started")
	}
	r.wall = wall
	return nil
}

// SetBuildWallFile names the wall file Run loads (abcp run --build-wall-file) and the SHA-256 abcp serve probed it at
// (--build-wall-sha256): a file that does not load or check, or whose digest is another, fails the run before Ralphex
// starts, with build_wall_unavailable. It must be configured before Run starts.
func (r *Runner) SetBuildWallFile(path, sha256 string) error {
	if r == nil || path == "" {
		return errors.New("build wall file is required")
	}
	if len(sha256) != 64 || strings.Trim(sha256, "0123456789abcdef") != "" {
		return errors.New("the build wall file's probed SHA-256 is required")
	}
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.started {
		return errors.New("runner has already started")
	}
	r.wallFile, r.wallSHA256 = path, sha256
	return nil
}

// prepareWalledRun makes (or, for a resumed run, finds) the run's folder and repository. It runs under the setup lease.
func (r *Runner) prepareWalledRun(ctx context.Context) (*walledRun, error) {
	isolation, ok := r.governed.Isolation()
	if !ok {
		return nil, fmt.Errorf("%w: the run's authority names no run folder (it was admitted before builds were walled)", ErrBuildWallUnavailable)
	}
	if !r.governed.Worktree().Enabled {
		return nil, fmt.Errorf("%w: a walled build runs in a worktree of its own repository", ErrBuildWallUnavailable)
	}
	if r.parsedCapsule.PolicyVersion == contextcapsule.PolicyVersionV3 {
		return nil, fmt.Errorf("%w: a V3 governed invocation is not walled in phase 1", ErrBuildWallUnavailable)
	}
	folder := r.wall.RunFolder(r.governed.RunID())
	if folder != isolation.RunFolder {
		return nil, fmt.Errorf("%w: the run folder %s is not the wall file's %s", ErrBuildWallUnavailable, isolation.RunFolder, folder)
	}
	if err := r.wall.ValidateReadPaths(isolation.ReadPaths, isolation.ProductTaskID); err != nil {
		return nil, err
	}
	w := &walledRun{
		wall: r.wall, runID: r.governed.RunID(), folder: folder, repository: filepath.Join(folder, "repo"),
		home: filepath.Join(folder, "home"), tmp: filepath.Join(folder, "tmp"), engineHome: filepath.Join(folder, "engine-home"),
		product: r.governed.Repository().Path, branch: r.governed.Worktree().Branch, startSHA: r.governed.Repository().StartSHA,
		readPaths: isolation.ReadPaths, env: map[string]string{},
	}
	if _, err := os.Lstat(folder); errors.Is(err, os.ErrNotExist) {
		if r.resume {
			return nil, fmt.Errorf("%w: the resumed run's folder %s is not there", ErrBuildWallUnavailable, folder)
		}
		if err := os.Mkdir(folder, 0o700); err != nil {
			return nil, fmt.Errorf("%w: make the run's folder: %v", ErrBuildWallUnavailable, err)
		}
		if err := w.cloneRepository(ctx); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("%w: inspect the run's folder: %v", ErrBuildWallUnavailable, err)
	} else if !r.resume {
		return nil, fmt.Errorf("%w: the run's folder %s is there already; a run starts in a folder of its own", ErrBuildWallUnavailable, folder)
	}
	if err := privateWallDirectory(folder); err != nil {
		return nil, fmt.Errorf("%w: run folder: %v", ErrBuildWallUnavailable, err)
	}
	if err := realDirectory(filepath.Join(w.repository, ".git")); err != nil {
		return nil, fmt.Errorf("%w: the run's repository: %v", ErrBuildWallUnavailable, err)
	}
	for _, dir := range []string{w.home, w.tmp, w.engineHome} {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%w: make %s: %v", ErrBuildWallUnavailable, dir, err)
		}
		if err := privateWallDirectory(dir); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrBuildWallUnavailable, dir, err)
		}
	}
	w.evidence = map[string]any{"run_folder": folder, "repository": "own clone (git clone --no-local)", "read_paths": len(isolation.ReadPaths)}
	return w, nil
}

// cloneRepository makes the run's repository from the product repository's checked-out branch through Git's
// transport, checks it starts at the start commit and holds that commit's history only, and leaves it no remote.
func (w *walledRun) cloneRepository(ctx context.Context) error {
	branch, err := gitOutput(ctx, w.product, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch == "" {
		return fmt.Errorf("%w: the product repository has no checked-out branch to start from", ErrBuildWallUnavailable)
	}
	clone := exec.CommandContext(ctx, "git", "-c", "protocol.file.allow=always", "clone", "--quiet", "--no-local", "--no-hardlinks", "--no-tags",
		"--single-branch", "--branch", branch, "--", w.product, w.repository)
	clone.Env = gitexec.Environment()
	if output, err := clone.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: make the run's repository: %v: %s", ErrBuildWallUnavailable, err, bounded(output))
	}
	head, err := gitOutput(ctx, w.repository, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || head != w.startSHA {
		return fmt.Errorf("%w: the run's repository starts at %q, not the start commit %s", ErrBuildWallUnavailable, head, w.startSHA)
	}
	if _, err := gitOutput(ctx, w.repository, "remote", "remove", "origin"); err != nil {
		return fmt.Errorf("%w: leave the run's repository without a remote: %v", ErrBuildWallUnavailable, err)
	}
	if _, err := os.Lstat(filepath.Join(w.repository, ".git", "objects", "info", "alternates")); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: the run's repository borrows objects", ErrBuildWallUnavailable)
	}
	own, err := gitOutput(ctx, w.repository, "rev-list", "--all")
	if err != nil {
		return fmt.Errorf("%w: list the run's repository: %v", ErrBuildWallUnavailable, err)
	}
	start, err := gitOutput(ctx, w.product, "rev-list", w.startSHA)
	if err != nil || own != start {
		return fmt.Errorf("%w: the run's repository holds more than the start commit's history", ErrBuildWallUnavailable)
	}
	// Commits are made under the product repository's identity, as before: the build has no ~/.gitconfig of its own.
	for _, key := range []string{"user.name", "user.email"} {
		value, err := userGitConfig(ctx, w.product, w.wall.Home(), key)
		if err != nil || value == "" {
			continue
		}
		if _, err := gitOutput(ctx, w.repository, "config", "--local", key, value); err != nil {
			return fmt.Errorf("%w: set the run's %s: %v", ErrBuildWallUnavailable, key, err)
		}
	}
	return nil
}

// userGitConfig reads a setting as Git resolves it for the controller's user in repository (its own and the user's
// global settings), the way Ralphex's commits resolved it before builds had a home of their own.
func userGitConfig(ctx context.Context, repository, home, key string) (string, error) {
	command := exec.CommandContext(ctx, "git", "config", "--get", key)
	command.Dir = repository
	command.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// executionPlan answers the plan Ralphex is given in the run's repository: the governed plan itself when the start
// commit tracks it, else an untracked copy Ralphex copies into its worktree and commits on the run's branch. The copy
// is removed after Ralphex exits.
func (w *walledRun) executionPlan(ctx context.Context, plan authority.PlanManifest) (string, error) {
	return w.executionPlanFor(ctx, plan, 1)
}

// executionPlanFor is executionPlan for one attempt of the run. A fresh attempt's copy (R3) has a folder of its own,
// abcp-ralphex-plan-<run digest>-fresh-<attempt>: the first attempt's copy is committed on the run's branch already, and
// Ralphex, finding the same file there again, would have nothing to commit and refuse to start.
func (w *walledRun) executionPlanFor(ctx context.Context, plan authority.PlanManifest, attempt int) (string, error) {
	relative, err := filepath.Rel(w.product, plan.Path)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("prepare Ralphex execution plan: governed plan is outside the repository")
	}
	tracked := exec.CommandContext(ctx, "git", "cat-file", "blob", w.startSHA+":"+filepath.ToSlash(relative))
	tracked.Dir = w.product
	tracked.Env = gitexec.Environment()
	if contents, err := tracked.Output(); err == nil {
		sum := sha256.Sum256(contents)
		if hex.EncodeToString(sum[:]) != plan.SHA256 {
			return "", errors.New("governed plan blob in start commit differs from governed plan")
		}
		return filepath.Join(w.repository, relative), nil
	}
	contents, err := os.ReadFile(plan.Path)
	if err != nil {
		return "", fmt.Errorf("read governed plan for Ralphex execution copy: %w", err)
	}
	sum := sha256.Sum256(contents)
	if hex.EncodeToString(sum[:]) != plan.SHA256 {
		return "", errors.New("governed plan differs from its authority")
	}
	runSum := sha256.Sum256([]byte(w.runID))
	name := ralphex.ExecutionPlanHandoffPrefixV1 + hex.EncodeToString(runSum[:])
	if attempt > 1 {
		name = fmt.Sprintf("%s-fresh-%d", name, attempt)
	}
	dir := filepath.Join(w.repository, name)
	if err := w.removePlanCopyAt(dir); err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("create Ralphex execution plan directory: %w", err)
	}
	path := filepath.Join(dir, "plan.md")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", fmt.Errorf("create Ralphex execution plan copy: %w", err)
	}
	_, writeErr := file.Write(contents)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return "", fmt.Errorf("write Ralphex execution plan copy: %w", err)
	}
	w.planCopy = dir
	return path, nil
}

// removePlanCopy removes the execution plan copy, never through a link the build may have put in its place.
func (w *walledRun) removePlanCopy() error {
	if w == nil || w.planCopy == "" {
		return nil
	}
	err := w.removePlanCopyAt(w.planCopy)
	w.planCopy = ""
	return err
}

func (w *walledRun) removePlanCopyAt(dir string) error {
	if err := realDirectory(w.repository); err != nil {
		return fmt.Errorf("the run's repository is not a real folder: %w", err)
	}
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return os.RemoveAll(dir)
}

// prepareEngine gives the run's engine a home in its folder: the engine's settings copied (or, for an open-model
// engine, written there), its one sign-in file bound read-write so a refresh is kept, and its session folder bound from
// <the folder the meter reads>/runs/<run>, within the six levels Repo C's meter walks.
func (w *walledRun) prepareEngine(executor string, lane *enginelane.Lane, start *openmodel.Start) error {
	settings := map[string]string{}
	if lane != nil {
		settings = lane.Settings
	}
	evidence := map[string]any{"engine_home": w.engineHome}
	switch strings.ToLower(strings.TrimSpace(executor)) {
	case "codex":
		var sessionsRoot string
		if start != nil {
			if err := start.UseHome(w.engineHome); err != nil {
				return err
			}
			sessionsRoot = start.Provider.CodexHome
		} else {
			source := settings["CODEX_HOME"]
			if source == "" {
				source = os.Getenv("CODEX_HOME")
			}
			if !filepath.IsAbs(source) {
				source = filepath.Join(w.wall.Home(), ".codex")
			}
			config, err := codexConfigFor(filepath.Join(source, "config.toml"), w.repository)
			if err != nil {
				return err
			}
			if err := writePrivateFile(filepath.Join(w.engineHome, "config.toml"), config); err != nil {
				return err
			}
			if signIn := filepath.Join(source, "auth.json"); realFile(signIn) == nil {
				w.mounts = append(w.mounts, WallMount{Source: signIn, Target: filepath.Join(w.engineHome, "auth.json"), Writable: true})
				evidence["sign_in"] = "auth.json"
			}
			sessionsRoot = filepath.Join(source, "sessions")
		}
		sessions, err := runSessions(sessionsRoot, w.runID)
		if err != nil {
			return err
		}
		w.mounts = append(w.mounts, WallMount{Source: sessions, Target: filepath.Join(w.engineHome, "sessions"), Writable: true})
		w.env["CODEX_HOME"] = w.engineHome
		evidence["sessions"] = sessions
	default:
		source := settings["CLAUDE_CONFIG_DIR"]
		if source == "" {
			source = os.Getenv("CLAUDE_CONFIG_DIR")
		}
		if !filepath.IsAbs(source) {
			source = filepath.Join(w.wall.Home(), ".claude")
		}
		if data, err := readEngineFile(filepath.Join(source, "settings.json")); err == nil {
			if err := writePrivateFile(filepath.Join(w.engineHome, "settings.json"), data); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if signIn := filepath.Join(source, ".credentials.json"); realFile(signIn) == nil {
			w.mounts = append(w.mounts, WallMount{Source: signIn, Target: filepath.Join(w.engineHome, ".credentials.json"), Writable: true})
			evidence["sign_in"] = ".credentials.json"
		}
		sessions, err := runSessions(filepath.Join(source, "projects"), w.runID)
		if err != nil {
			return err
		}
		w.mounts = append(w.mounts, WallMount{Source: sessions, Target: filepath.Join(w.engineHome, "projects"), Writable: true})
		w.env["CLAUDE_CONFIG_DIR"] = w.engineHome
		evidence["sessions"] = sessions
	}
	for key, value := range evidence {
		w.evidence[key] = value
	}
	return nil
}

// runSessions makes <root>/runs/<run> (0700), root being a real folder of the engine's (made when its parent is).
func runSessions(root, runID string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", fmt.Errorf("%w: the engine's session folder %q", ErrBuildWallPath, root)
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return "", fmt.Errorf("%w: make the engine's session folder %s: %v", ErrBuildWallPath, root, err)
		}
	}
	if err := realDirectory(root); err != nil {
		return "", fmt.Errorf("%w: the engine's session folder %s: %v", ErrBuildWallPath, root, err)
	}
	path := root
	for _, part := range []string{"runs", runID} {
		path = filepath.Join(path, part)
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		if err := realDirectory(path); err != nil {
			return "", fmt.Errorf("%w: %s: %v", ErrBuildWallPath, path, err)
		}
	}
	return path, nil
}

// codexConfigFor is the engine's config.toml without its [projects.*] tables, which name other builds' folders, and
// with the run's repository trusted.
func codexConfigFor(path, repository string) ([]byte, error) {
	data, err := readEngineFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var out bytes.Buffer
	skipping := false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), maxEngineConfigBytes)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			header := strings.TrimSpace(strings.Trim(trimmed, "[]"))
			skipping = strings.HasPrefix(header, "projects.") || header == "projects"
		}
		if !skipping {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read the engine's settings: %w", err)
	}
	out.WriteString("\n# Added by ABCP for this walled build (Repo C design note BUILD_ISOLATION.md): its own repository is trusted.\n")
	out.WriteString("[projects." + tomlQuote(repository) + "]\ntrust_level = \"trusted\"\n")
	return out.Bytes(), nil
}

func tomlQuote(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

// readEngineFile reads one of the engine's own settings files: a regular file, no link, at most 256 KiB.
func readEngineFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxEngineConfigBytes {
		return nil, fmt.Errorf("%w: %s is not a regular file of at most 256 KiB", ErrBuildWallPath, path)
	}
	if err := realFile(path); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrBuildWallPath, path, err)
	}
	return os.ReadFile(path)
}

func writePrivateFile(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrBuildWallPath, path)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Sync(), file.Close())
}

// environment is the build's environment inside the wall: the filtered one, its home and /tmp the run's own, the
// engine's home its own, and no setting that names a folder of the host's.
func (w *walledRun) environment(base []string) []string {
	overrides := map[string]string{"HOME": w.wall.Home(), "TMPDIR": "/tmp"}
	for key, value := range w.env {
		overrides[key] = value
	}
	dropped := map[string]bool{"XDG_CACHE_HOME": true, "XDG_CONFIG_HOME": true}
	out := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; replaced || dropped[key] {
			continue
		}
		out = append(out, entry)
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+overrides[key])
	}
	return out
}

// args answers the build's wall: its own folders, the plan and capsule files, Ralphex, the app's own folders, the
// engine's file and folder, and ABCP's Ralphex settings for the run (the folder writable, since Ralphex adds its
// defaults there at start; every file ABCP wrote in it read-only).
func (w *walledRun) args(governed authority.Authority, configDir string) ([]string, error) {
	readOnly := []string{governed.Plan().Path, governed.Ralphex().BinaryPath}
	if capsule, ok := governed.ContextCapsule(); ok {
		readOnly = append(readOnly, capsule.Path)
	}
	readOnly = append(readOnly, w.readPaths...)
	mounts := append([]WallMount{}, w.mounts...)
	if err := realDirectory(configDir); err != nil {
		return nil, fmt.Errorf("%w: Ralphex settings folder: %v", ErrBuildWallPath, err)
	}
	mounts = append(mounts, WallMount{Source: configDir, Target: configDir, Writable: true})
	var written []string
	err := filepath.WalkDir(configDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			written = append(written, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(written)
	for _, path := range written {
		mounts = append(mounts, WallMount{Source: path, Target: path})
	}
	return w.wall.Args(WallSpec{RunFolder: w.folder, Home: w.home, Tmp: w.tmp, Chdir: w.repository, ReadOnly: readOnly, Mounts: mounts})
}

// startBranch makes the run's branch in the product repository at the start commit, under the setup lease, before
// Ralphex starts, so the first fetch back is already a fast-forward of something (Git takes a branch the product
// repository does not have as it is). A resumed run's branch is there already, where its last fetch left it; one that
// is gone is made again at the start commit.
func (w *walledRun) startBranch(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	lease, err := acquireRepositoryExecutionLease(ctx, w.product)
	if err != nil {
		return fmt.Errorf("acquire repository execution lease to start the run's branch: %w", err)
	}
	defer lease.Close()
	ref := "refs/heads/" + w.branch
	if current, err := gitOutput(ctx, w.product, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil && current != "" {
		w.fetched = current
		return nil
	}
	// An empty old value makes the branch only where there is none.
	if _, err := gitOutput(ctx, w.product, "update-ref", "-m", "abcp: the walled run's branch starts at its start commit", ref, w.startSHA, ""); err != nil {
		return fmt.Errorf("start the run's branch in the product repository: %w", err)
	}
	w.fetched = w.startSHA
	return nil
}

// bringBackFinally brings the branch back once more after Ralphex exits; a branch that could not be brought back
// fast-forward at any time during the run fails it.
func (w *walledRun) bringBackFinally(ctx context.Context) error {
	err := w.bringBack(ctx)
	w.mu.Lock()
	rewritten := w.rewrites > 0
	w.mu.Unlock()
	if rewritten && !errors.Is(err, ErrBranchRewritten) {
		err = errors.Join(err, ErrBranchRewritten)
	}
	return err
}

// bringBack fetches the run's branch from the run's repository into the product repository, fast-forward only,
// under the setup lease, when it has moved. A branch the run's repository does not have yet is not an error.
func (w *walledRun) bringBack(ctx context.Context) error {
	if err := realDirectory(filepath.Join(w.repository, ".git")); err != nil {
		return fmt.Errorf("%w: the run's repository is not a real folder: %v", ErrBranchRewritten, err)
	}
	remote := filepath.Join(w.repository, ".git")
	ref := "refs/heads/" + w.branch
	listed, err := gitOutput(ctx, w.product, "-c", "protocol.file.allow=always", "ls-remote", "--heads", "--", remote, ref)
	if err != nil {
		return fmt.Errorf("read the run's branch: %w", err)
	}
	head := ""
	for _, line := range strings.Split(listed, "\n") {
		if sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok && name == ref {
			head = sha
		}
	}
	if head == "" {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if head == w.fetched {
		return nil
	}
	lease, err := acquireRepositoryExecutionLease(ctx, w.product)
	if err != nil {
		return fmt.Errorf("acquire repository execution lease to bring the run's branch back: %w", err)
	}
	defer lease.Close()
	fetch := exec.CommandContext(ctx, "git", "-c", "protocol.file.allow=always", "fetch", "--no-progress", "--no-tags", "--no-write-fetch-head",
		"--no-recurse-submodules", "--", remote, ref+":"+ref)
	fetch.Dir = w.product
	fetch.Env = gitexec.Environment()
	if output, err := fetch.CombinedOutput(); err != nil {
		// Git refuses a branch that does not fast-forward ("! [rejected] ... (non-fast-forward)").
		if bytes.Contains(output, []byte("rejected")) || bytes.Contains(output, []byte("non-fast-forward")) {
			w.rewrites++
			return ErrBranchRewritten
		}
		return fmt.Errorf("bring the run's branch back: %v: %s", err, bounded(output))
	}
	fetched, err := gitOutput(ctx, w.product, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return fmt.Errorf("read the brought-back branch: %w", err)
	}
	w.fetched = fetched
	return nil
}

// bringBackWhileRunning brings the branch back each fetchBackInterval until stopped, so checkpoints and previews come
// as the build commits. The stop waits for the watcher; it may be called more than once.
func (w *walledRun) bringBackWhileRunning(ctx context.Context) func() {
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(fetchBackInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = w.bringBack(ctx)
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done); <-finished }) }
}

func bounded(output []byte) string {
	text := strings.TrimSpace(string(output))
	if len(text) > 1024 {
		text = text[:1024]
	}
	return text
}

package run

// The build wall (Repo C design note BUILD_ISOLATION.md, phase 1; plan docs/plans/build-isolation.md): every build's
// Ralphex runs inside bubblewrap, which shows it a read-only system, the tools, Autobuild's own libraries, its own run
// folder (its repository, home and /tmp), its plan, its app's own folders and its engine's one sign-in file and session
// folder, and nothing else: no secrets, no other build, no product repository, no Docker socket, no user systemd, no
// other process. The network is still the host's (phase 2 takes it).

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/enginelane"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

const (
	// BuildIsolationKindV1 is what ABCP's capabilities and engines answer say while every build is walled.
	BuildIsolationKindV1 = "bwrap-v1"
	// MaxReadPaths bounds the app's own folders one build may read.
	MaxReadPaths = 16
	// MaxWallFileBytes bounds the wall file.
	MaxWallFileBytes = 64 << 10
	maxWallEntries   = 64
	wallProbeTimeout = 15 * time.Second
)

// ErrBuildWallUnavailable says the wall cannot start: no build starts, and admission answers 503
// build_wall_unavailable. A build never runs unwalled instead.
var ErrBuildWallUnavailable = errors.New("build_wall_unavailable")

// ErrBuildWallPath says a path a build would be shown is not a real folder or file under the wall's rules.
var ErrBuildWallPath = errors.New("build wall path refused")

// BuildWallFileV1 is the protected file (0600) `abcp serve --build-wall-file` and `abcp run --build-wall-file` name.
// Every path is absolute, clean and real: no link anywhere in it. Each is shown inside the wall at its own path.
type BuildWallFileV1 struct {
	SchemaVersion int `json:"schema_version"`
	// Bwrap is the bubblewrap binary.
	Bwrap string `json:"bwrap"`
	// BuildsRoot holds every run's own folder, <builds_root>/<run id> (0700).
	BuildsRoot string `json:"builds_root"`
	// Home is the controller user's home. Inside the wall it is the run's own home, empty at the start.
	Home string `json:"home"`
	// System is the read-only system (/usr, /etc and what name resolution needs). The host's /bin, /lib and the like
	// are links into it inside too.
	System []string `json:"system"`
	// Tools are read-only: Node and the engines (~/.nvm), Ralphex, the browsers the kits' checks use.
	Tools []string `json:"tools,omitempty"`
	// Libraries are Autobuild's own, read-only: the kits, the templates, the template mirror, the decoration library.
	Libraries []string `json:"libraries,omitempty"`
	// ReadRoots are the roots an app's own folders (read_paths) may come from: earlier versions, brands, chat files,
	// imports, site reads, standard pages.
	ReadRoots []ReadRootV1 `json:"read_roots,omitempty"`
	// Canaries are places the start probe must not find inside a wall: the secrets, ABCP's settings, its ledger and
	// evidence. The probe adds the home's engine folders and keys, another run's folder, the Docker sockets, the
	// user's systemd and ABCP's own process.
	Canaries []string `json:"canaries,omitempty"`
}

// ReadRootV1 is one root an app's own folders may come from. A root kept per app (the default) holds each app's
// folders under its product task ID (versions/<task>/…, imports/tasks/<task>/…): a build is shown a folder under it
// only when the folder's path names the build's own task ID as a whole component, so no other app's folder, no parent
// of every app's (imports/tasks) and never the root. A shared root holds folders kept by something ABCP is not told
// (brands, kept per owner, and their shared phone fonts): any real folder strictly inside it is accepted, and which is
// the app's is Repo C's to decide.
type ReadRootV1 struct {
	Path   string `json:"path"`
	Shared bool   `json:"shared,omitempty"`
}

// Wall is a loaded, checked wall file.
type Wall struct {
	path        string
	file        BuildWallFileV1
	sha256      string
	bwrapSHA256 string
	version     string
	links       [][2]string
}

// WallMount is one more thing a build is shown: a file or folder at Target, read-only unless Writable.
type WallMount struct {
	Source, Target string
	Writable       bool
}

// WallSpec is one build's own part of the wall.
type WallSpec struct {
	// RunFolder is <builds_root>/<run id>, read-write at its own path.
	RunFolder string
	// Home is the folder shown as the wall file's home; Tmp the one shown as /tmp. Both are in RunFolder.
	Home, Tmp string
	// Chdir is where the build starts, in RunFolder.
	Chdir string
	// ReadOnly are more paths shown read-only at their own path: the plan, Ralphex, the app's own folders.
	ReadOnly []string
	// Mounts follow, in order: the engine's sign-in file and session folder, ABCP's settings for the run.
	Mounts []WallMount
}

// LoadBuildWall reads and checks the wall file: a private regular file of this user, every path in it real, the
// builds root private, and the bubblewrap binary answering its version.
func LoadBuildWall(path string) (*Wall, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("%w: the wall file path must be absolute and clean", ErrBuildWallUnavailable)
	}
	data, err := enginelane.ReadPrivate(path, MaxWallFileBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: wall file: %v", ErrBuildWallUnavailable, err)
	}
	var file BuildWallFileV1
	if err := strictjson.Decode(data, &file); err != nil {
		return nil, fmt.Errorf("%w: wall file does not decode", ErrBuildWallUnavailable)
	}
	if err := checkWallFile(&file); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBuildWallUnavailable, err)
	}
	sum := sha256.Sum256(data)
	wall := &Wall{path: path, file: file, sha256: hex.EncodeToString(sum[:])}
	if wall.bwrapSHA256, err = fileSHA256(file.Bwrap); err != nil {
		return nil, fmt.Errorf("%w: hash bubblewrap: %v", ErrBuildWallUnavailable, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, file.Bwrap, "--version").Output()
	if err != nil || len(version) == 0 || len(version) > 256 {
		return nil, fmt.Errorf("%w: bubblewrap does not answer its version", ErrBuildWallUnavailable)
	}
	wall.version = strings.TrimSpace(string(version))
	for _, link := range []string{"/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32"} {
		info, err := os.Lstat(link)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(link)
		if err != nil || filepath.IsAbs(target) {
			continue
		}
		resolved := filepath.Join(filepath.Dir(link), target)
		for _, system := range file.System {
			if within(system, resolved) {
				wall.links = append(wall.links, [2]string{target, link})
				break
			}
		}
	}
	return wall, nil
}

func checkWallFile(file *BuildWallFileV1) error {
	if file.SchemaVersion != 1 {
		return errors.New("wall file schema_version must be 1")
	}
	if err := realFile(file.Bwrap); err != nil {
		return fmt.Errorf("bwrap: %v", err)
	}
	if info, err := os.Stat(file.Bwrap); err != nil || info.Mode()&0o111 == 0 {
		return errors.New("bwrap is not executable")
	}
	if err := realDirectory(file.Home); err != nil {
		return fmt.Errorf("home: %v", err)
	}
	if file.Home == "/" || file.Home == "/tmp" {
		return errors.New("home must be the controller user's own folder")
	}
	if !canonicalWallPath(file.BuildsRoot) {
		return errors.New("builds_root must be absolute and clean")
	}
	if err := os.Mkdir(file.BuildsRoot, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("builds_root: %v", err)
	}
	if err := privateWallDirectory(file.BuildsRoot); err != nil {
		return fmt.Errorf("builds_root: %v", err)
	}
	if len(file.System) == 0 {
		return errors.New("system must name the read-only system")
	}
	groups := []struct {
		name      string
		paths     []string
		directory bool
	}{{"system", file.System, false}, {"tools", file.Tools, false}, {"libraries", file.Libraries, false}, {"read_roots", readRootPaths(file.ReadRoots), true}}
	var bound []string
	for _, group := range groups {
		if len(group.paths) > maxWallEntries {
			return fmt.Errorf("%s names more than %d paths", group.name, maxWallEntries)
		}
		for _, path := range group.paths {
			var err error
			if group.directory {
				err = realDirectory(path)
			} else {
				err = realPathOf(path)
			}
			if err != nil {
				return fmt.Errorf("%s %s: %v", group.name, path, err)
			}
			if path == "/" || within(path, file.BuildsRoot) || within(file.BuildsRoot, path) || path == file.Home || within(path, file.Home) && group.name == "system" {
				return fmt.Errorf("%s %s overlaps the builds root or the home", group.name, path)
			}
			if group.name != "read_roots" {
				bound = append(bound, path)
			}
		}
	}
	roots := readRootPaths(file.ReadRoots)
	for index, root := range roots {
		for _, other := range bound {
			if within(other, root) || within(root, other) {
				return fmt.Errorf("read root %s overlaps %s, which every build is shown", root, other)
			}
		}
		for _, other := range roots[:index] {
			if within(other, root) || within(root, other) {
				return fmt.Errorf("read root %s overlaps read root %s", root, other)
			}
		}
	}
	if len(file.Canaries) > maxWallEntries {
		return fmt.Errorf("canaries names more than %d paths", maxWallEntries)
	}
	for _, canary := range file.Canaries {
		if !canonicalWallPath(canary) || canary == "/" {
			return fmt.Errorf("canary %q must be absolute and clean", canary)
		}
		for _, other := range append(append([]string{}, bound...), roots...) {
			if within(other, canary) {
				return fmt.Errorf("canary %s is inside %s, which a build may be shown", canary, other)
			}
		}
	}
	return nil
}

// BuildWallFileSHA256 answers the SHA-256 of the wall file at path as it is now, read as LoadBuildWall reads it.
func BuildWallFileSHA256(path string) (string, error) {
	data, err := enginelane.ReadPrivate(path, MaxWallFileBytes)
	if err != nil {
		return "", fmt.Errorf("%w: wall file: %v", ErrBuildWallUnavailable, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Path is the wall file's path; SHA256 its digest; Version bubblewrap's own version line.
func (w *Wall) Path() string    { return w.path }
func (w *Wall) SHA256() string  { return w.sha256 }
func (w *Wall) Version() string { return w.version }

// BuildsRoot is the folder every run's own folder is in.
func (w *Wall) BuildsRoot() string { return w.file.BuildsRoot }

// Home is the controller user's home, the path a build's own home is shown at.
func (w *Wall) Home() string { return w.file.Home }

// RunFolder is the run's own folder.
func (w *Wall) RunFolder(runID string) string { return filepath.Join(w.file.BuildsRoot, runID) }

// ValidateReadPath accepts one of the app's own folders for the app whose product task ID is app, a canonical UUID: a
// real folder (no link anywhere in its path) strictly inside one of the wall file's read roots and, unless that root is shared, naming
// app as a whole component of its path where a root keeps an app's folder: <root>/<app>/…, or
// <root>/<kind>/<app>/… under a plain lowercase folder such as tasks, changes or imports (never inside another app's).
func (w *Wall) ValidateReadPath(path, app string) error {
	// Read paths belong to an app named by its canonical UUID (Repo C's task IDs): never a plain word such as
	// "tasks", which a root uses for the folder holding every app's.
	if !ValidReadPathApp(app) {
		return fmt.Errorf("%w: read paths name their app by its UUID, and %q is none", ErrBuildWallPath, app)
	}
	if err := realDirectory(path); err != nil {
		return fmt.Errorf("%w: %v", ErrBuildWallPath, err)
	}
	for _, root := range w.file.ReadRoots {
		if path == root.Path || !within(root.Path, path) {
			continue
		}
		if root.Shared {
			return nil
		}
		rel, err := filepath.Rel(root.Path, path)
		if err == nil {
			parts := strings.Split(rel, string(filepath.Separator))
			if parts[0] == app || len(parts) > 1 && plainKindFolder(parts[0]) && parts[1] == app {
				return nil
			}
		}
		return fmt.Errorf("%w: %s is not the app's own folder in %s", ErrBuildWallPath, path, root.Path)
	}
	return fmt.Errorf("%w: %s is under no read root", ErrBuildWallPath, path)
}

// ValidReadPathApp reports whether app can name an app's folders: a product task ID in canonical UUID form
// (lowercase hexadecimal, 8-4-4-4-12), as Repo C sends every one. A word, a path or an upper-case form is none.
func ValidReadPathApp(app string) bool {
	return authority.CanonicalUUID(app)
}

// plainKindFolder reports whether name is a folder a root keeps its apps' folders in by kind (tasks, changes,
// imports): lowercase letters only, so never an app's or a person's ID.
func plainKindFolder(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, c := range name {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

func readRootPaths(roots []ReadRootV1) []string {
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		paths = append(paths, root.Path)
	}
	return paths
}

// ValidateReadPaths checks a build's read paths for the app app: at most MaxReadPaths, each once, each
// ValidateReadPath.
func (w *Wall) ValidateReadPaths(paths []string, app string) error {
	if len(paths) > MaxReadPaths {
		return fmt.Errorf("%w: more than %d read paths", ErrBuildWallPath, MaxReadPaths)
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			return fmt.Errorf("%w: %s is named twice", ErrBuildWallPath, path)
		}
		seen[path] = true
		if err := w.ValidateReadPath(path, app); err != nil {
			return err
		}
	}
	return nil
}

// Args answers bubblewrap's arguments for one build, up to and with "--": the namespaces, the read-only system and
// links, its own /proc and /dev, its /tmp and home, the tools and libraries, its run folder, then the spec's read-only
// paths and mounts. Every source is checked real (no link in its path) first.
func (w *Wall) Args(spec WallSpec) ([]string, error) {
	if filepath.Dir(spec.RunFolder) != w.file.BuildsRoot || filepath.Base(spec.RunFolder) == "." {
		return nil, fmt.Errorf("%w: the run folder %s is not directly in the builds root", ErrBuildWallPath, spec.RunFolder)
	}
	if err := privateWallDirectory(spec.RunFolder); err != nil {
		return nil, fmt.Errorf("%w: run folder: %v", ErrBuildWallPath, err)
	}
	for _, own := range []string{spec.Home, spec.Tmp, spec.Chdir} {
		if !within(spec.RunFolder, own) {
			return nil, fmt.Errorf("%w: %s is not in the run folder", ErrBuildWallPath, own)
		}
		if err := realDirectory(own); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBuildWallPath, err)
		}
	}
	args := []string{"--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try", "--new-session", "--die-with-parent", "--cap-drop", "ALL"}
	args = append(args, w.systemArgs()...)
	args = append(args, "--proc", "/proc", "--dev", "/dev", "--bind", spec.Tmp, "/tmp", "--bind", spec.Home, w.file.Home)
	for _, path := range append(append([]string{}, w.file.Tools...), w.file.Libraries...) {
		args = append(args, "--ro-bind", path, path)
	}
	args = append(args, "--bind", spec.RunFolder, spec.RunFolder)
	seen := map[string]bool{}
	for _, path := range spec.ReadOnly {
		if seen[path] {
			continue
		}
		seen[path] = true
		if err := realPathOf(path); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrBuildWallPath, path, err)
		}
		args = append(args, "--ro-bind", path, path)
	}
	for _, mount := range spec.Mounts {
		if err := realPathOf(mount.Source); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrBuildWallPath, mount.Source, err)
		}
		if !canonicalWallPath(mount.Target) || mount.Target == "/" {
			return nil, fmt.Errorf("%w: mount target %q", ErrBuildWallPath, mount.Target)
		}
		// A build writes only in its own folder and its own /tmp.
		if mount.Writable && !within(spec.RunFolder, mount.Target) && !within("/tmp", mount.Target) {
			return nil, fmt.Errorf("%w: a writable mount at %s is outside the run's own folders", ErrBuildWallPath, mount.Target)
		}
		kind := "--ro-bind"
		if mount.Writable {
			kind = "--bind"
		}
		args = append(args, kind, mount.Source, mount.Target)
	}
	args = append(args, "--chdir", spec.Chdir, "--")
	return append([]string{w.file.Bwrap}, args...), nil
}

func (w *Wall) systemArgs() []string {
	var args []string
	for _, path := range w.file.System {
		args = append(args, "--ro-bind", path, path)
	}
	for _, link := range w.links {
		args = append(args, "--symlink", link[0], link[1])
	}
	return args
}

// ObserverArgs answers bubblewrap's arguments for one of ABCP's own reads of a build's folder (its Git reads and
// its progress view): the read-only system, readOnly at their own paths, writable at theirs, no network
// unless network, and nothing else. A build's repository is the build's to write, so its configuration and
// attributes could name programs; ABCP never runs Git in it outside such a wall.
func (w *Wall) ObserverArgs(readOnly, writable []string, network bool) ([]string, error) {
	args := []string{w.file.Bwrap, "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try", "--new-session", "--die-with-parent", "--cap-drop", "ALL"}
	if !network {
		args = append(args, "--unshare-net")
	}
	args = append(args, w.systemArgs()...)
	args = append(args, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp")
	for _, path := range readOnly {
		if err := realPathOf(path); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrBuildWallPath, path, err)
		}
		args = append(args, "--ro-bind", path, path)
	}
	for _, path := range writable {
		if err := realDirectory(path); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrBuildWallPath, path, err)
		}
		args = append(args, "--bind", path, path)
	}
	return append(args, "--chdir", "/", "--"), nil
}

// Check starts the build's wall once with /bin/true in it, so a wall that cannot start stops the build before
// anything runs, with bubblewrap's own words (bounded) in the error.
func (w *Wall) Check(ctx context.Context, wallArgs []string) error {
	ctx, cancel := context.WithTimeout(ctx, wallProbeTimeout)
	defer cancel()
	argv := append(append([]string{}, wallArgs...), "/bin/true")
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	var stderr bytes.Buffer
	command.Stderr = &limitWriter{w: &stderr, left: 2048}
	if err := command.Run(); err != nil {
		return fmt.Errorf("%w: the build's wall does not start: %v: %s", ErrBuildWallUnavailable, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Probe proves a wall like a build's hides every canary and shows its own folder: a probe run folder and another run's
// folder are made in the builds root, a fixed script in the wall reports BLOCKED or OPEN for each canary (the wall file's,
// extra, the other run's folder, the home's engine folders and keys, the Docker sockets, the user's systemd) and for
// ABCP's own process, and must write in its own folder. Any OPEN, or no write, is an error.
func (w *Wall) Probe(ctx context.Context, extra []string) error {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	own := filepath.Join(w.file.BuildsRoot, ".wall-probe-"+hex.EncodeToString(suffix))
	other := own + "-other"
	defer os.RemoveAll(own)
	defer os.RemoveAll(other)
	for _, dir := range []string{own, filepath.Join(own, "home"), filepath.Join(own, "tmp"), other} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return fmt.Errorf("%w: make the probe's folders: %v", ErrBuildWallUnavailable, err)
		}
	}
	if err := os.WriteFile(filepath.Join(other, "canary"), []byte("wall probe canary\n"), 0o600); err != nil {
		return fmt.Errorf("%w: make the probe's canary: %v", ErrBuildWallUnavailable, err)
	}
	canaries := []string{filepath.Join(other, "canary"), other}
	for _, name := range []string{".codex", ".claude", ".claude.json", ".ssh", ".aws", ".config/gh", ".gitconfig"} {
		canaries = append(canaries, filepath.Join(w.file.Home, name))
	}
	canaries = append(canaries, "/var/run/docker.sock", "/run/docker.sock", "/run/user/"+strconv.Itoa(os.Getuid()))
	canaries = append(canaries, w.file.Canaries...)
	canaries = append(canaries, extra...)
	args, err := w.Args(WallSpec{RunFolder: own, Home: filepath.Join(own, "home"), Tmp: filepath.Join(own, "tmp"), Chdir: own})
	if err != nil {
		return err
	}
	script := `own=$1; pid=$2; shift 2; status=0
while [ $# -gt 0 ]; do
  if [ -e "$1" ] || [ -L "$1" ]; then echo "OPEN $1"; status=1; else echo "BLOCKED $1"; fi
  shift
done
if kill -0 "$pid" 2>/dev/null; then echo "OPEN abcp-process"; status=1; else echo "BLOCKED abcp-process"; fi
if touch "$own/probe" 2>/dev/null && [ -f "$own/probe" ]; then echo "OWN ok"; else echo "OWN refused"; status=1; fi
exit $status`
	argv := append(args, "/bin/sh", "-c", script, "wall-probe", own, strconv.Itoa(os.Getpid()))
	argv = append(argv, canaries...)
	ctx, cancel := context.WithTimeout(ctx, wallProbeTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	var stdout, stderr bytes.Buffer
	command.Stdout = &limitWriter{w: &stdout, left: 64 << 10}
	command.Stderr = &limitWriter{w: &stderr, left: 2048}
	runErr := command.Run()
	blocked := 0
	var open []string
	ownOK := false
	for _, line := range strings.Split(stdout.String(), "\n") {
		switch {
		case strings.HasPrefix(line, "BLOCKED "):
			blocked++
		case strings.HasPrefix(line, "OPEN "):
			open = append(open, strings.TrimPrefix(line, "OPEN "))
		case line == "OWN ok":
			ownOK = true
		}
	}
	if len(open) > 0 {
		sort.Strings(open)
		return fmt.Errorf("%w: the probe's wall shows %s", ErrBuildWallUnavailable, strings.Join(open, ", "))
	}
	if runErr != nil || !ownOK || blocked != len(canaries)+1 {
		return fmt.Errorf("%w: the probe's wall did not start or check (%d of %d blocked, own folder %t): %v: %s",
			ErrBuildWallUnavailable, blocked, len(canaries)+1, ownOK, runErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Evidence answers what the run's start records of its wall: the kind, bubblewrap's path, version and digest, the wall
// file's digest, the arguments' digest and count, the namespaces and the network; never a setting's value.
func (w *Wall) Evidence(wallArgs []string) map[string]any {
	data, _ := json.Marshal(wallArgs)
	sum := sha256.Sum256(data)
	binds := 0
	for _, arg := range wallArgs {
		if arg == "--bind" || arg == "--ro-bind" {
			binds++
		}
	}
	return map[string]any{
		"kind": BuildIsolationKindV1, "bwrap": w.file.Bwrap, "bwrap_version": w.version, "bwrap_sha256": w.bwrapSHA256,
		"wall_file_sha256": w.sha256, "binds_sha256": hex.EncodeToString(sum[:]), "binds": binds,
		"namespaces": []string{"pid", "ipc", "uts", "cgroup", "mount", "user"}, "network": "host",
		"capabilities": "none",
	}
}

type limitWriter struct {
	w    io.Writer
	left int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	n := len(p)
	if l.left > 0 {
		if len(p) > l.left {
			p = p[:l.left]
		}
		written, _ := l.w.Write(p)
		l.left -= written
	}
	return n, nil
}

func canonicalWallPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, 0) && len(path) <= 4096
}

// realPathOf accepts an existing absolute clean path with no link anywhere in it.
func realPathOf(path string) error {
	if !canonicalWallPath(path) {
		return errors.New("not an absolute clean path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != path {
		return errors.New("a link is in its path")
	}
	return nil
}

func realDirectory(path string) error {
	if err := realPathOf(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("not a folder")
	}
	return nil
}

func realFile(path string) error {
	if err := realPathOf(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	return nil
}

// privateWallDirectory requires a real folder of this user that no one else may open.
func privateWallDirectory(path string) error {
	if err := realDirectory(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("not a private folder of the controller's user (mode 0700)")
	}
	return nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, 1<<30)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

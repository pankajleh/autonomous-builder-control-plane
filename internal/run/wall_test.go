package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// wallDirs are a test wall file's folders.
type wallDirs struct {
	root, bwrap, builds, home, reads, shared, library, tools, file string
}

// fakeBwrap answers its version and fails anything else, as a bubblewrap that cannot make its namespaces does.
const fakeBwrap = "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'bubblewrap 0.9.0'; exit 0; fi\necho 'bwrap: setting up uid map: Permission denied' >&2\nexit 1\n"

// writeTestWall writes a wall file of real folders; bwrap is the binary named, or a fake one when empty.
func writeTestWall(t *testing.T, bwrap string, edit func(*BuildWallFileV1)) (*Wall, wallDirs, error) {
	t.Helper()
	root := t.TempDir()
	dirs := wallDirs{root: root, builds: filepath.Join(root, "runtime", "builds"), home: filepath.Join(root, "home"),
		reads: filepath.Join(root, "vendor", "versions"), shared: filepath.Join(root, "vendor", "brands"), library: filepath.Join(root, "vendor", "node"),
		tools: filepath.Join(root, "home", ".nvm"), file: filepath.Join(root, "build-wall.json")}
	for _, dir := range []string{filepath.Join(root, "runtime"), dirs.home, dirs.reads, dirs.shared, dirs.library, dirs.tools} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if bwrap == "" {
		bwrap = filepath.Join(root, "bwrap")
		writeTestFile(t, bwrap, []byte(fakeBwrap), 0o700)
	}
	dirs.bwrap = bwrap
	file := BuildWallFileV1{SchemaVersion: 1, Bwrap: bwrap, BuildsRoot: dirs.builds, Home: dirs.home, System: []string{"/usr", "/etc"},
		Tools: []string{dirs.tools}, Libraries: []string{dirs.library}, ReadRoots: []ReadRootV1{{Path: dirs.reads}, {Path: dirs.shared, Shared: true}},
		Canaries: []string{filepath.Join(root, "runtime", "secrets")}}
	if edit != nil {
		edit(&file)
	}
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, dirs.file, data, 0o600)
	wall, err := LoadBuildWall(dirs.file)
	return wall, dirs, err
}

func testWallSpec(t *testing.T, wall *Wall, run string) WallSpec {
	t.Helper()
	folder := wall.RunFolder(run)
	for _, dir := range []string{folder, filepath.Join(folder, "home"), filepath.Join(folder, "tmp"), filepath.Join(folder, "repo")} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return WallSpec{RunFolder: folder, Home: filepath.Join(folder, "home"), Tmp: filepath.Join(folder, "tmp"), Chdir: filepath.Join(folder, "repo")}
}

func TestTheWallsArgumentsShowABuildItsOwnFoldersAndTheSharedOnesReadOnly(t *testing.T) {
	wall, dirs, err := writeTestWall(t, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := testWallSpec(t, wall, "admission-1")
	plan := filepath.Join(dirs.root, "product", ".abcp-input", "admission-1", "plan.md")
	if err := os.MkdirAll(filepath.Dir(plan), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, plan, []byte("plan\n"), 0o600)
	version := filepath.Join(dirs.reads, "task", "v1", "files")
	if err := os.MkdirAll(version, 0o700); err != nil {
		t.Fatal(err)
	}
	signIn := filepath.Join(dirs.home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(signIn), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, signIn, []byte("{}"), 0o600)
	spec.ReadOnly = []string{plan, version, plan}
	spec.Mounts = []WallMount{{Source: signIn, Target: filepath.Join(spec.RunFolder, "engine-home", "auth.json"), Writable: true}}
	args, err := wall.Args(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{dirs.bwrap,
		"--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try", "--new-session", "--die-with-parent", "--cap-drop", "ALL",
		"--ro-bind", "/usr", "/usr", "--ro-bind", "/etc", "/etc"}
	for _, link := range wall.links {
		want = append(want, "--symlink", link[0], link[1])
	}
	want = append(want, "--proc", "/proc", "--dev", "/dev",
		"--bind", spec.Tmp, "/tmp", "--bind", spec.Home, dirs.home,
		"--ro-bind", dirs.tools, dirs.tools, "--ro-bind", dirs.library, dirs.library,
		"--bind", spec.RunFolder, spec.RunFolder,
		"--ro-bind", plan, plan, "--ro-bind", version, version,
		"--bind", signIn, filepath.Join(spec.RunFolder, "engine-home", "auth.json"),
		"--chdir", spec.Chdir, "--")
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("wall arguments\nwant %q\n got %q", want, args)
	}
	for _, forbidden := range []string{"--share-net", "--unshare-user-try", "/var", "/run", "secrets", "--clearenv", "--setenv"} {
		for _, arg := range args {
			if arg == forbidden || strings.Contains(arg, "secrets") {
				t.Fatalf("wall arguments hold %q: %q", forbidden, args)
			}
		}
	}
	evidence := wall.Evidence(args)
	if evidence["kind"] != BuildIsolationKindV1 || evidence["bwrap_version"] != "bubblewrap 0.9.0" || evidence["network"] != "host" ||
		len(evidence["binds_sha256"].(string)) != 64 || evidence["binds"] != 10 {
		t.Fatalf("wall evidence = %v", evidence)
	}
	observer, err := wall.ObserverArgs([]string{spec.RunFolder}, []string{spec.Tmp}, false)
	if err != nil || !containsArg(observer, "--unshare-net") || containsArg(observer, dirs.home) || observer[len(observer)-1] != "--" {
		t.Fatalf("observer arguments = %q, %v", observer, err)
	}
	if networked, _ := wall.ObserverArgs([]string{spec.RunFolder}, nil, true); containsArg(networked, "--unshare-net") {
		t.Fatalf("a networked observer has no network: %q", networked)
	}
}

func TestTheWallRefusesLinksForeignFoldersAndWritesOutsideTheRun(t *testing.T) {
	wall, dirs, err := writeTestWall(t, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := testWallSpec(t, wall, "admission-2")
	outside := t.TempDir()
	link := filepath.Join(dirs.root, "plan-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*WallSpec){
		"a read-only link":            func(s *WallSpec) { s.ReadOnly = []string{link} },
		"a missing path":              func(s *WallSpec) { s.ReadOnly = []string{filepath.Join(outside, "nothing")} },
		"a mount through a link":      func(s *WallSpec) { s.Mounts = []WallMount{{Source: link, Target: filepath.Join(s.RunFolder, "x")}} },
		"a writable mount outside":    func(s *WallSpec) { s.Mounts = []WallMount{{Source: outside, Target: "/opt/elsewhere", Writable: true}} },
		"a run folder not in builds":  func(s *WallSpec) { s.RunFolder = outside },
		"a home outside the run":      func(s *WallSpec) { s.Home = outside },
		"a start folder outside":      func(s *WallSpec) { s.Chdir = outside },
		"a relative mount target":     func(s *WallSpec) { s.Mounts = []WallMount{{Source: outside, Target: "relative"}} },
		"a mount over the whole root": func(s *WallSpec) { s.Mounts = []WallMount{{Source: outside, Target: "/"}} },
	} {
		changed := spec
		edit(&changed)
		if _, err := wall.Args(changed); !errors.Is(err, ErrBuildWallPath) {
			t.Fatalf("%s: wall arguments = %v, want a refusal", name, err)
		}
	}
	// A root kept per app: versions/<task>/<version>, and imports' tasks/<task> and pending/<person>/<import>.
	version := filepath.Join(dirs.reads, testApp, "v1")
	nested := filepath.Join(dirs.reads, "tasks", testApp, "files")
	otherApp := filepath.Join(dirs.reads, "other-task", "3", "files")
	pending := filepath.Join(dirs.reads, "pending", "user-7", "import-3")
	brand := filepath.Join(dirs.shared, "user-7", "v3", "brand")
	// Another customer may name a folder in their own app after this app's ID: it is still theirs.
	nestedInOther := filepath.Join(dirs.reads, "other-task", testApp)
	deepInOther := filepath.Join(otherApp, testApp)
	for _, dir := range []string{version, nested, otherApp, pending, brand, nestedInOther, deepInOther} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	versionLink := filepath.Join(dirs.reads, "link")
	if err := os.Symlink(version, versionLink); err != nil {
		t.Fatal(err)
	}
	if err := wall.ValidateReadPaths([]string{version, filepath.Join(dirs.reads, testApp), nested, filepath.Dir(nested), brand}, testApp); err != nil {
		t.Fatal(err)
	}
	for name, paths := range map[string][]string{
		"outside the roots": {outside}, "a link": {versionLink}, "the root itself": {dirs.reads}, "twice": {version, version},
		"missing": {filepath.Join(dirs.reads, "none")}, "too many": make([]string, MaxReadPaths+1),
		// The attack: a spec naming another app's folder, or a folder holding every app's.
		"another app's folder":           {otherApp},
		"another app's whole folder":     {filepath.Join(dirs.reads, "other-task")},
		"every app's tasks":              {filepath.Join(dirs.reads, "tasks")},
		"every person's pending imports": {filepath.Join(dirs.reads, "pending")},
		"a person's import":              {pending},
		"the app's ID in another app's":  {nestedInOther},
		"the app's ID deep in its files": {deepInOther},
		"the shared root itself":         {dirs.shared},
	} {
		if err := wall.ValidateReadPaths(paths, testApp); !errors.Is(err, ErrBuildWallPath) {
			t.Fatalf("read paths %s = %v", name, err)
		}
	}
	// The app is named by its canonical UUID: no prefix of one, no path, no upper case, none at all, and no word such as
	// "tasks", which would make the folder holding every app's (imports/tasks) look like one app's.
	for _, app := range []string{testApp[:35], testApp + "/v1", strings.ToUpper(testApp), "..", "", "tasks", "task"} {
		if err := wall.ValidateReadPaths([]string{version}, app); !errors.Is(err, ErrBuildWallPath) {
			t.Fatalf("read path for app %q = %v", app, err)
		}
	}
	for name, path := range map[string]string{"every app's tasks": filepath.Join(dirs.reads, "tasks"), "a shared root's folder": brand} {
		if err := wall.ValidateReadPaths([]string{path}, "tasks"); !errors.Is(err, ErrBuildWallPath) {
			t.Fatalf("%s for the app %q = %v", name, "tasks", err)
		}
	}
}

// testApp is a product task ID as Repo C sends one.
const testApp = "3b8f2c1e-5d4a-4e7b-9c2f-0a1b2c3d4e5f"

func TestTheWallFileIsCheckedWhenItLoads(t *testing.T) {
	for name, edit := range map[string]func(*BuildWallFileV1){
		"another schema":                     func(f *BuildWallFileV1) { f.SchemaVersion = 2 },
		"no system":                          func(f *BuildWallFileV1) { f.System = nil },
		"a missing bwrap":                    func(f *BuildWallFileV1) { f.Bwrap = filepath.Join(f.Home, "no-bwrap") },
		"the builds root inside a library":   func(f *BuildWallFileV1) { f.BuildsRoot = filepath.Join(f.Libraries[0], "builds") },
		"a library inside the builds root":   func(f *BuildWallFileV1) { f.Libraries = append(f.Libraries, filepath.Dir(f.BuildsRoot)) },
		"the whole home bound":               func(f *BuildWallFileV1) { f.Tools = append(f.Tools, f.Home) },
		"a read root inside a library":       func(f *BuildWallFileV1) { f.ReadRoots[0].Path = f.Libraries[0] },
		"a read root named twice":            func(f *BuildWallFileV1) { f.ReadRoots = append(f.ReadRoots, f.ReadRoots[0]) },
		"a canary a build is shown":          func(f *BuildWallFileV1) { f.Canaries = append(f.Canaries, filepath.Join(f.Tools[0], "secret")) },
		"a relative canary":                  func(f *BuildWallFileV1) { f.Canaries = append(f.Canaries, "secrets") },
		"a system path through a link (/..)": func(f *BuildWallFileV1) { f.System = append(f.System, "/usr/../etc") },
	} {
		if _, _, err := writeTestWall(t, "", edit); !errors.Is(err, ErrBuildWallUnavailable) {
			t.Fatalf("%s: load = %v, want build_wall_unavailable", name, err)
		}
	}
	// A wall file others may read is refused.
	_, dirs, err := writeTestWall(t, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dirs.file, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBuildWall(dirs.file); !errors.Is(err, ErrBuildWallUnavailable) {
		t.Fatalf("a readable wall file = %v", err)
	}
}

func TestAWallThatCannotStartFailsItsProbeAndItsCheck(t *testing.T) {
	wall, _, err := writeTestWall(t, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := wall.Probe(context.Background(), nil); !errors.Is(err, ErrBuildWallUnavailable) || !strings.Contains(err.Error(), "did not start") {
		t.Fatalf("probe of a wall that cannot start = %v", err)
	}
	spec := testWallSpec(t, wall, "admission-3")
	args, err := wall.Args(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := wall.Check(context.Background(), args); !errors.Is(err, ErrBuildWallUnavailable) || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("check of a wall that cannot start = %v", err)
	}
}

func TestTheEnginesSettingsAreCopiedWithoutOtherBuildsFolders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, path, []byte("model = \"gpt-6-astra\"\n[projects.\"/srv/product-repo\"]\ntrust_level = \"trusted\"\n[shell_environment_policy]\ninherit = \"core\"\n[projects.\"/srv/other\"]\ntrust_level = \"trusted\"\n"), 0o600)
	config, err := codexConfigFor(path, "/builds/admission-1/repo")
	if err != nil {
		t.Fatal(err)
	}
	text := string(config)
	if strings.Contains(text, "/srv/") || !strings.Contains(text, "model = \"gpt-6-astra\"") || !strings.Contains(text, "[shell_environment_policy]\ninherit = \"core\"") ||
		!strings.Contains(text, "[projects.\"/builds/admission-1/repo\"]\ntrust_level = \"trusted\"") {
		t.Fatalf("engine settings = %q", text)
	}
	if missing, err := codexConfigFor(filepath.Join(t.TempDir(), "none.toml"), "/r"); err != nil || !strings.Contains(string(missing), "[projects.\"/r\"]") {
		t.Fatalf("settings without a file = %q, %v", missing, err)
	}
	w := &walledRun{wall: &Wall{file: BuildWallFileV1{Home: "/home/devagent"}}, env: map[string]string{"CODEX_HOME": "/builds/run/engine-home"}}
	env := w.environment([]string{"HOME=/elsewhere", "PATH=/usr/bin", "XDG_CACHE_HOME=/home/devagent/.cache", "TMPDIR=/var/tmp", "CODEX_HOME=/home/devagent/.codex", "OPENAI_API_KEY=kept"})
	want := []string{"PATH=/usr/bin", "OPENAI_API_KEY=kept", "CODEX_HOME=/builds/run/engine-home", "HOME=/home/devagent", "TMPDIR=/tmp"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("walled environment = %q, want %q", env, want)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

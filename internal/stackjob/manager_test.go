package stackjob

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/preview"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

func testProfiles() ProfilesV1 {
	profile := func(id, stack, path string, egress []string, versions ...string) ProfileV1 {
		return ProfileV1{ProfileID: id, Stack: stack, Versions: versions, Image: "ghcr.io/pankajleh/autobuild-stack-" + stack + "@sha256:" + strings.Repeat("c", 64),
			Path: path, CPUQuota: 200000, MemoryBytes: 4 << 30, PidsLimit: 1024, TmpfsBytes: 256 << 20, WorkBytes: 4 << 30, TimeoutSeconds: 1800, Egress: egress}
	}
	java := profile("java", StackJava, "/opt/java/openjdk/bin:/usr/bin:/bin", []string{preview.EgressMavenCentral, preview.EgressGradle}, "17", "21")
	java.Environment = map[string]string{"JAVA_HOME": "/opt/java/openjdk"}
	return ProfilesV1{SchemaVersion: 1, Profiles: []ProfileV1{
		profile("python", StackPython, "/usr/local/bin:/usr/bin:/bin", []string{preview.EgressPyPI}, "3.10", "3.11", "3.12", "3.13"),
		java,
		profile("node", StackNode, "/usr/local/bin:/usr/bin:/bin", []string{preview.EgressNpm}, "20", "22", "24"),
		profile("go", StackGo, "/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", []string{preview.EgressGo}, "1.26", "1.27"),
		profile("ruby", StackRuby, "/usr/local/bin:/usr/bin:/bin", []string{preview.EgressRubyGems}, "3.3", "3.4", "4.0"),
	}}
}

type execCall struct {
	dir  string
	argv []string
	env  []string
}

type fakeSandbox struct {
	mu      sync.Mutex
	proveOK bool
	proves  int
	opens   []string
	specs   []preview.StackSpec
	calls   []execCall
	exec    func(ctx context.Context, work, dir string, argv []string, out io.Writer) (int, error)
}

type fakeSession struct {
	s    *fakeSandbox
	work string
}

func (f *fakeSandbox) Open(_ context.Context, id string, spec preview.StackSpec, work, cache string) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !spec.Valid() || filepath.Base(cache) == "" {
		return nil, errors.New("bad spec")
	}
	f.opens = append(f.opens, spec.Image)
	f.specs = append(f.specs, spec)
	return &fakeSession{s: f, work: work}, nil
}
func (f *fakeSandbox) Prove(context.Context, string, preview.StackSpec, string, string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.proves++
	return f.proveOK
}
func (f *fakeSession) Proxy() netip.AddrPort { return netip.MustParseAddrPort("10.213.0.1:3129") }
func (f *fakeSession) Exec(ctx context.Context, dir string, env, argv []string, out io.Writer) (int, error) {
	f.s.mu.Lock()
	f.s.calls = append(f.s.calls, execCall{dir, argv, env})
	run := f.s.exec
	f.s.mu.Unlock()
	if run == nil {
		return 0, nil
	}
	return run(ctx, f.work, dir, argv, out)
}
func (f *fakeSession) Close() error { return nil }

type fixture struct {
	m       *Manager
	box     *fakeSandbox
	root    string
	sources string
}

func newFixture(t *testing.T, box *fakeSandbox) *fixture {
	t.Helper()
	root, sources := filepath.Join(t.TempDir(), "stack"), t.TempDir()
	m, err := New(root, sources, testProfiles(), box, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	m.freeDisk = func(string) (uint64, bool) { return 100 << 30, true }
	t.Cleanup(m.Close)
	return &fixture{m: m, box: box, root: root, sources: sources}
}

var key = strings.Repeat("d", 64)

func (f *fixture) start(t *testing.T, request, source string, files map[string]string) serviceapi.StackJobV1 {
	t.Helper()
	if files != nil {
		write(t, filepath.Join(f.sources, source), files)
	}
	job, err := f.m.StartStackJob(context.Background(), serviceapi.StackJobCreateV1{RequestID: request, Source: source, CacheKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func (f *fixture) wait(t *testing.T, id string) serviceapi.StackJobV1 {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, err := f.m.StackJob(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State != Queued && job.State != Running {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s did not end", id)
	return serviceapi.StackJobV1{}
}

func TestAPythonProjectIsInstalledBuiltAndTestedWithItsCounts(t *testing.T) {
	box := &fakeSandbox{proveOK: true}
	box.exec = func(_ context.Context, work, dir string, argv []string, out io.Writer) (int, error) {
		io.WriteString(out, "collected 3 items\nDATABASE_URL=postgres://app:pa55@db/app\n")
		if strings.Contains(strings.Join(argv, " "), "pytest") {
			os.WriteFile(filepath.Join(work, ".abcp", "pytest-0.xml"), []byte(`<testsuites><testsuite tests="3" failures="0" errors="0" skipped="1"/></testsuites>`), 0o644)
		}
		return 0, nil
	}
	f := newFixture(t, box)
	queued := f.start(t, "req-1", "proj-1", map[string]string{"requirements.txt": "pytest\n", "app.py": "print(1)\n", ".git/config": "x"})
	if queued.State != Queued || queued.SchemaVersion != serviceapi.StackJobSchemaV1 || len(queued.ID) != 64 {
		t.Fatalf("queued = %+v", queued)
	}
	job := f.wait(t, queued.ID)
	if job.State != Passed || job.Failure != "" || job.StartedAt == "" || job.EndedAt == "" || len(job.Parts) != 1 {
		t.Fatalf("job = %+v", job)
	}
	part := job.Parts[0]
	if part.Stack != StackPython || part.Version != "3.12" || part.Tool != "pip" || len(part.Steps) != 3 {
		t.Fatalf("part = %+v", part)
	}
	for _, s := range part.Steps {
		if s.State != Passed || s.ExitCode == nil || *s.ExitCode != 0 || !strings.Contains(s.OutputTail, "collected 3 items") || strings.Contains(s.OutputTail, "pa55") {
			t.Errorf("step %+v", s)
		}
	}
	if tests := part.Steps[2].Tests; tests == nil || *tests != (serviceapi.StackTestsV1{Passed: 2, Failed: 0, Skipped: 1}) {
		t.Fatalf("tests = %+v", tests)
	}
	if len(box.calls) != 3 || box.calls[0].dir != "/work" || !strings.Contains(strings.Join(box.calls[0].env, " "), "HTTPS_PROXY=http://10.213.0.1:3129") ||
		box.proves != 1 || len(box.opens) != 1 || !strings.Contains(box.opens[0], "stack-python@") {
		t.Fatalf("sandbox = %+v %d %v", box.calls, box.proves, box.opens)
	}
	// The work is gone, the cache stays, and the record is on disk as answered.
	if _, err := os.Stat(filepath.Join(f.root, "work", job.ID)); !os.IsNotExist(err) {
		t.Fatal("work kept")
	}
	if _, err := os.Stat(filepath.Join(f.root, "caches", key, "home")); err != nil {
		t.Fatal("cache missing")
	}
	var kept record
	data, _ := os.ReadFile(filepath.Join(f.root, "jobs", job.ID+".json"))
	if json.Unmarshal(data, &kept) != nil || kept.Job.State != Passed || kept.Source != "proj-1" {
		t.Fatalf("record = %s", data)
	}
	// The same request answers the same job; another body under it is a conflict.
	if again, err := f.m.StartStackJob(context.Background(), serviceapi.StackJobCreateV1{RequestID: "req-1", Source: "proj-1", CacheKey: key}); err != nil || again.ID != job.ID {
		t.Fatalf("replay = %v %v", again.ID, err)
	}
	if _, err := f.m.StartStackJob(context.Background(), serviceapi.StackJobCreateV1{RequestID: "req-1", Source: "proj-2", CacheKey: key}); !errors.Is(err, serviceapi.ErrStackJobConflict) {
		t.Fatalf("conflict = %v", err)
	}
	// The probe ran once for the profile, not for every job.
	second := f.wait(t, f.start(t, "req-2", "proj-1", nil).ID)
	if second.State != Passed || box.proves != 1 {
		t.Fatalf("second = %s, proves %d", second.State, box.proves)
	}
}

func TestAFailedInstallEndsThePartAndNamesWhereItFailed(t *testing.T) {
	box := &fakeSandbox{proveOK: true}
	box.exec = func(_ context.Context, _, _ string, argv []string, out io.Writer) (int, error) {
		io.WriteString(out, "npm ERR! 404 Not Found - left-padd\n")
		return 1, nil
	}
	f := newFixture(t, box)
	job := f.wait(t, f.start(t, "req-1", "web", map[string]string{"package.json": `{"scripts":{"build":"vite build","test":"vitest"}}`, "package-lock.json": "{}"}).ID)
	if job.State != Failed || job.Failure != FailInstall {
		t.Fatalf("job = %+v", job)
	}
	steps := job.Parts[0].Steps
	if steps[0].State != Failed || *steps[0].ExitCode != 1 || !strings.Contains(steps[0].OutputTail, "404") || steps[1].State != StepNotRun || steps[2].State != StepNotRun {
		t.Fatalf("steps = %+v", steps)
	}
	if len(box.calls) != 1 || strings.Join(box.calls[0].argv, " ") != "npm ci --no-audit --no-fund" {
		t.Fatalf("calls = %+v", box.calls)
	}
}

func TestJavaGetsItsProxyFilesAndNoTestsFoundIsNotAFailure(t *testing.T) {
	box := &fakeSandbox{proveOK: true}
	f := newFixture(t, box)
	job := f.wait(t, f.start(t, "req-1", "api", map[string]string{"pom.xml": "<project/>"}).ID)
	if job.State != Passed || job.Parts[0].Steps[0].State != StepSkipped || job.Parts[0].Steps[0].Note == "" {
		t.Fatalf("java job = %+v", job)
	}
	settings, _ := os.ReadFile(filepath.Join(f.root, "caches", key, "home", ".m2", "settings.xml"))
	gradle, _ := os.ReadFile(filepath.Join(f.root, "caches", key, "home", ".gradle", "gradle.properties"))
	if !strings.Contains(string(settings), "<host>10.213.0.1</host>") || !strings.Contains(string(gradle), "systemProp.https.proxyHost=10.213.0.1") {
		t.Fatalf("proxy files:\n%s\n%s", settings, gradle)
	}
	box.exec = func(_ context.Context, _, _ string, argv []string, _ io.Writer) (int, error) {
		if strings.Contains(strings.Join(argv, " "), "pytest") {
			return 5, nil
		}
		return 0, nil
	}
	py := f.wait(t, f.start(t, "req-2", "lib", map[string]string{"pyproject.toml": "[project]\nname='lib'\n"}).ID)
	if py.State != Passed || py.Parts[0].Steps[2].State != StepSkipped || py.Parts[0].Steps[2].Note != "no tests were found" {
		t.Fatalf("no tests = %+v", py.Parts[0].Steps[2])
	}
}

func TestJobsWithoutAStackOrASandboxEndWithTheirCodes(t *testing.T) {
	box := &fakeSandbox{proveOK: false}
	f := newFixture(t, box)
	if job := f.wait(t, f.start(t, "r1", "empty", map[string]string{"README.md": "hi"}).ID); job.State != Failed || job.Failure != FailNoStack {
		t.Fatalf("no stack = %+v", job)
	}
	if job := f.wait(t, f.start(t, "r2", "shop", map[string]string{"composer.json": "{}"}).ID); job.Failure != FailUnsupported || job.Parts[0].Found != "php" {
		t.Fatalf("php = %+v", job)
	}
	job := f.wait(t, f.start(t, "r3", "py", map[string]string{"requirements.txt": ""}).ID)
	if job.Failure != FailSandbox || len(box.opens) != 0 || job.Parts[0].Steps[0].State != StepNotRun {
		t.Fatalf("probe failed = %+v, opens %v", job, box.opens)
	}
	f.m.freeDisk = func(string) (uint64, bool) { return 1 << 30, true }
	if job := f.wait(t, f.start(t, "r4", "py", nil).ID); job.Failure != FailSandbox {
		t.Fatalf("low disk = %+v", job)
	}
}

func TestRequestsAreCheckedAndJobsStopQueuedOrRunning(t *testing.T) {
	release := make(chan struct{})
	box := &fakeSandbox{proveOK: true}
	box.exec = func(ctx context.Context, _, _ string, _ []string, _ io.Writer) (int, error) {
		select {
		case <-ctx.Done():
			return 0, errors.New("ended")
		case <-release:
			return 0, nil
		}
	}
	f := newFixture(t, box)
	ctx := context.Background()
	for _, bad := range []serviceapi.StackJobCreateV1{
		{RequestID: "r", Source: "missing", CacheKey: key},
		{RequestID: "r", Source: "../x", CacheKey: key},
		{RequestID: "r", Source: "x", CacheKey: "short"},
		{RequestID: "", Source: "x", CacheKey: key},
	} {
		if _, err := f.m.StartStackJob(ctx, bad); !errors.Is(err, serviceapi.ErrStackJobNotAccepted) {
			t.Errorf("%+v = %v", bad, err)
		}
	}
	os.Symlink(t.TempDir(), filepath.Join(f.sources, "linked"))
	if _, err := f.m.StartStackJob(ctx, serviceapi.StackJobCreateV1{RequestID: "r", Source: "linked", CacheKey: key}); !errors.Is(err, serviceapi.ErrStackJobNotAccepted) {
		t.Fatalf("linked source = %v", err)
	}
	running := f.start(t, "run", "app", map[string]string{"requirements.txt": ""})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j, _ := f.m.StackJob(ctx, running.ID); j.State == Running && len(j.Parts) == 1 && j.Parts[0].Steps[0].State == Running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	queued := []serviceapi.StackJobV1{}
	for i := 0; i < maxQueued; i++ {
		queued = append(queued, f.start(t, "q"+string(rune('a'+i)), "app", nil))
	}
	if _, err := f.m.StartStackJob(ctx, serviceapi.StackJobCreateV1{RequestID: "one-too-many", Source: "app", CacheKey: key}); !errors.Is(err, serviceapi.ErrStackJobsBusy) {
		t.Fatalf("queue full = %v", err)
	}
	if j, err := f.m.StopStackJob(ctx, queued[0].ID); err != nil || j.State != Stopped || j.Failure != FailStopped {
		t.Fatalf("stop queued = %+v %v", j, err)
	}
	if _, err := f.m.StopStackJob(ctx, running.ID); err != nil {
		t.Fatal(err)
	}
	if j := f.wait(t, running.ID); j.State != Stopped || j.Parts[0].Steps[0].State != Stopped || j.Parts[0].Steps[1].State != StepNotRun {
		t.Fatalf("stopped running = %+v", j)
	}
	if _, err := f.m.StackJob(ctx, strings.Repeat("0", 64)); !errors.Is(err, serviceapi.ErrStackJobNotFound) {
		t.Fatalf("unknown = %v", err)
	}
	close(release)
}

func TestARestartMarksWhatWasRunningUnknownAndClearsItsWork(t *testing.T) {
	root, sources := filepath.Join(t.TempDir(), "stack"), t.TempDir()
	for _, dir := range []string{"jobs", "work/" + strings.Repeat("e", 64)} {
		os.MkdirAll(filepath.Join(root, dir), 0o700)
	}
	left := record{Source: "app", CacheKey: key, BodyDigest: bodyDigest(serviceapi.StackJobCreateV1{Source: "app", CacheKey: key}), Job: serviceapi.StackJobV1{SchemaVersion: serviceapi.StackJobSchemaV1, ID: strings.Repeat("e", 64),
		RequestID: "left", State: Running, CreatedAt: "2026-10-28T03:00:00Z", Parts: []serviceapi.StackPartV1{}}}
	data, _ := json.Marshal(left)
	os.WriteFile(filepath.Join(root, "jobs", left.Job.ID+".json"), data, 0o600)
	m, err := New(root, sources, testProfiles(), &fakeSandbox{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	job, err := m.StackJob(context.Background(), left.Job.ID)
	if err != nil || job.State != Unknown || job.EndedAt == "" {
		t.Fatalf("left job = %+v %v", job, err)
	}
	if _, err := os.Stat(filepath.Join(root, "work", left.Job.ID)); !os.IsNotExist(err) {
		t.Fatal("left work kept")
	}
	if again, err := m.StartStackJob(context.Background(), serviceapi.StackJobCreateV1{RequestID: "left", Source: "app", CacheKey: key}); err != nil || again.ID != left.Job.ID {
		t.Fatalf("replay after restart = %+v %v", again, err)
	}
}

func TestProfilesAreCheckedWhenLoaded(t *testing.T) {
	good := testProfiles()
	if good.Validate() != nil {
		t.Fatal("good profiles refused")
	}
	for name, change := range map[string]func(*ProfilesV1){
		"schema":           func(f *ProfilesV1) { f.SchemaVersion = 2 },
		"image not pinned": func(f *ProfilesV1) { f.Profiles[0].Image = "python:3.12" },
		"no egress":        func(f *ProfilesV1) { f.Profiles[0].Egress = nil },
		"shopify egress":   func(f *ProfilesV1) { f.Profiles[0].Egress = []string{preview.EgressShopifyStores} },
		"version twice":    func(f *ProfilesV1) { f.Profiles[2].Versions = []string{"22", "22"} },
		"unknown stack":    func(f *ProfilesV1) { f.Profiles[0].Stack = "php" },
		"setting":          func(f *ProfilesV1) { f.Profiles[1].Environment = map[string]string{"LD_PRELOAD": "/x.so"} },
		"timeout":          func(f *ProfilesV1) { f.Profiles[0].TimeoutSeconds = 99999 },
		"memory":           func(f *ProfilesV1) { f.Profiles[0].MemoryBytes = 1 << 20 },
		"same id":          func(f *ProfilesV1) { f.Profiles[1].ProfileID = "python" },
	} {
		bad := testProfiles()
		change(&bad)
		if bad.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	path := filepath.Join(t.TempDir(), "profiles.json")
	data, _ := json.Marshal(good)
	os.WriteFile(path, data, 0o600)
	if loaded, err := LoadProfiles(path); err != nil || len(loaded.Profiles) != 5 {
		t.Fatalf("load = %v", err)
	}
	os.WriteFile(path, []byte(`{"schema_version":1,"profiles":[],"extra":1}`), 0o600)
	if _, err := LoadProfiles(path); err == nil {
		t.Fatal("unknown field accepted")
	}
	if p, ok := good.For(StackJava, "21"); !ok || p.ProfileID != "java" || p.Spec().Lifetime != 32*time.Minute {
		t.Fatalf("for = %+v", p)
	}
}

func TestLinksAJobLeftInItsCacheAreNeverFollowed(t *testing.T) {
	box := &fakeSandbox{proveOK: true}
	f := newFixture(t, box)
	outside := t.TempDir()
	token := filepath.Join(outside, "token")
	os.WriteFile(token, []byte("controller secret"), 0o600)
	os.MkdirAll(filepath.Join(outside, "ssh"), 0o700)
	home := filepath.Join(f.root, "caches", key, "home")
	os.MkdirAll(filepath.Join(home, ".gradle"), 0o755)
	// What a hostile earlier job could leave: .m2 a link to a folder outside, gradle.properties a link to a file outside.
	os.Symlink(filepath.Join(outside, "ssh"), filepath.Join(home, ".m2"))
	os.Symlink(token, filepath.Join(home, ".gradle", "gradle.properties"))
	job := f.wait(t, f.start(t, "req-1", "api", map[string]string{"pom.xml": "<project/>"}).ID)
	if job.State != Passed {
		t.Fatalf("job = %+v", job)
	}
	if data, _ := os.ReadFile(token); string(data) != "controller secret" {
		t.Fatalf("the file outside was written: %q", data)
	}
	if entries, _ := os.ReadDir(filepath.Join(outside, "ssh")); len(entries) != 0 {
		t.Fatalf("the folder outside was written: %v", entries)
	}
	if info, err := os.Lstat(filepath.Join(home, ".m2")); err != nil || !info.IsDir() {
		t.Fatal(".m2 is not a real folder again")
	}
	if !realFile(home, ".gradle", "gradle.properties") || !realFile(home, ".m2", "settings.xml") {
		t.Fatal("the proxy files were not written in real folders")
	}
	// The whole home a link: made a real folder again before the next job.
	os.RemoveAll(home)
	os.Symlink(outside, home)
	if job := f.wait(t, f.start(t, "req-2", "api", nil).ID); job.State != Passed {
		t.Fatalf("second job = %+v", job)
	}
	if info, err := os.Lstat(home); err != nil || !info.IsDir() {
		t.Fatal("home is not a real folder again")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 2 {
		t.Fatalf("outside changed: %v", entries)
	}
	// A pytest report reached through a link is not read.
	box.exec = func(_ context.Context, work, _ string, argv []string, _ io.Writer) (int, error) {
		if strings.Contains(strings.Join(argv, " "), "pytest") {
			report := filepath.Join(outside, "pytest-0.xml")
			os.WriteFile(report, []byte(`<testsuite tests="7"/>`), 0o644)
			os.RemoveAll(filepath.Join(work, ".abcp"))
			os.Symlink(outside, filepath.Join(work, ".abcp"))
		}
		return 0, nil
	}
	py := f.wait(t, f.start(t, "req-3", "lib", map[string]string{"requirements.txt": ""}).ID)
	if py.State != Passed || py.Parts[0].Steps[2].Tests != nil {
		t.Fatalf("a linked report was counted: %+v", py.Parts[0].Steps[2])
	}
}

package stackjob

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/enginelane"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// fakeEngine plays the agent: each session calls act.
type fakeEngine struct {
	mu   sync.Mutex
	runs []EngineRun
	act  func(run EngineRun, n int) (EngineResult, error)
}

func (e *fakeEngine) Run(_ context.Context, run EngineRun) (EngineResult, error) {
	e.mu.Lock()
	e.runs = append(e.runs, run)
	n := len(e.runs)
	act := e.act
	e.mu.Unlock()
	if act == nil {
		return EngineResult{Summary: "nothing"}, nil
	}
	return act(run, n)
}

// socketOf reads the broker's socket from ABCP's MCP file, as the agent's tool would.
func socketOf(t *testing.T, mcp string) string {
	t.Helper()
	data, err := os.ReadFile(mcp)
	var file struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err != nil || json.Unmarshal(data, &file) != nil || len(file.MCPServers) != 1 {
		t.Fatalf("mcp file = %s %v", data, err)
	}
	server := file.MCPServers["abcp"]
	if server.Command != "/usr/local/bin/abcp" || len(server.Args) != 3 || server.Args[0] != "stack-tool" || server.Args[1] != "--socket" {
		t.Fatalf("mcp server = %+v", server)
	}
	return server.Args[2]
}

func ask(t *testing.T, socket string, req runRequest) runAnswer {
	t.Helper()
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	answer, err := askBroker(conn, bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	return answer
}

type changeFixture struct {
	m       *ChangeManager
	box     *fakeSandbox
	engine  *fakeEngine
	root    string
	sources string
}

func newChangeFixture(t *testing.T, box *fakeSandbox, engine *fakeEngine) *changeFixture {
	t.Helper()
	// A short root: a Unix socket's path is bounded.
	base, err := os.MkdirTemp("", "cj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	root, sources := filepath.Join(base, "c"), filepath.Join(base, "src")
	if err := os.Mkdir(sources, 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := NewChangeManager(root, sources, "/usr/local/bin/abcp", testProfiles(), box, engine, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	m.freeDisk = func(string) (uint64, bool) { return 100 << 30, true }
	t.Cleanup(m.Close)
	return &changeFixture{m: m, box: box, engine: engine, root: root, sources: sources}
}

func (f *changeFixture) start(t *testing.T, request, kind string, files map[string]string) serviceapi.ChangeJobV1 {
	t.Helper()
	write(t, filepath.Join(f.sources, request), files)
	job, err := f.m.StartChangeJob(context.Background(), serviceapi.ChangeJobCreateV1{RequestID: request, Source: request, CacheKey: key, Kind: kind,
		Words: "The total is wrong when the list is empty."})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func (f *changeFixture) wait(t *testing.T, id string) serviceapi.ChangeJobV1 {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		job, err := f.m.ChangeJob(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State != Queued && job.State != Running {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s did not end", id)
	return serviceapi.ChangeJobV1{}
}

var pythonProject = map[string]string{
	"requirements.txt":  "pytest\n",
	"sample.py":         "def total(xs):\n    return sum(xs) if xs else None\n",
	"tests/test_one.py": "from sample import total\n\ndef test_one():\n    assert total([1]) == 1\n",
	"build/kept.txt":    "a build folder the code already had\n",
	".github/ci.yml":    "on: push\n",
	"node_modules/x.js": "never copied back\n",
	"docs/notes.md":     "notes\n",
}

// passingSteps answers every step, writing pytest's report with passed tests; after marks the checks after the change.
func passingSteps(passed *int, after *bool, testCode *int) func(context.Context, string, string, []string, io.Writer) (int, error) {
	return func(_ context.Context, work, dir string, argv []string, out io.Writer) (int, error) {
		line := strings.Join(argv, " ")
		if strings.Contains(line, "pytest") && strings.Contains(line, "junitxml") {
			n, code := *passed, 0
			if *after {
				n++
				code = *testCode
			}
			report := `<testsuite tests="` + itoa(n) + `" failures="0" errors="0" skipped="0"/>`
			if code != 0 {
				report = `<testsuite tests="` + itoa(n) + `" failures="1" errors="0" skipped="0"/>`
			}
			os.WriteFile(filepath.Join(work, ".abcp", "pytest-0.xml"), []byte(report), 0o644)
			return code, nil
		}
		if strings.Contains(line, "pip install") {
			// What installing writes stays out of the change.
			os.MkdirAll(filepath.Join(work, ".venv", "bin"), 0o755)
			os.WriteFile(filepath.Join(work, ".venv", "bin", "python"), []byte("x"), 0o755)
			os.MkdirAll(filepath.Join(work, "target"), 0o755)
			os.WriteFile(filepath.Join(work, "target", "out.class"), []byte("x"), 0o644)
			os.WriteFile(filepath.Join(work, "build", "made.txt"), []byte("x"), 0o644)
		}
		io.WriteString(out, "ok "+dir+"\n")
		return 0, nil
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func enginelaneFor(settings map[string]string) enginelane.Lane {
	return enginelane.Lane{Name: "test", Settings: settings}
}

func TestAFixIsMadeWithCommandsInTheSandboxReviewedCheckedAndHandedBackAsAPatch(t *testing.T) {
	passed, after, testCode := 1, false, 0
	box := &fakeSandbox{proveOK: true}
	box.exec = passingSteps(&passed, &after, &testCode)
	engine := &fakeEngine{}
	var seen []runAnswer
	var gitInside bool
	engine.act = func(run EngineRun, n int) (EngineResult, error) {
		if _, err := os.Stat(filepath.Join(run.Work, ".git")); err == nil {
			gitInside = true
		}
		socket := socketOf(t, run.MCPConfig)
		if n == 1 {
			seen = append(seen, ask(t, socket, runRequest{Command: "python -m pytest -q", Dir: "tests"}))
			seen = append(seen, ask(t, socket, runRequest{Command: "cat /etc/passwd", Dir: "../.."}))
			os.WriteFile(filepath.Join(run.Work, "sample.py"), []byte("def total(xs):\n    return sum(xs)\n"), 0o644)
			os.WriteFile(filepath.Join(run.Work, "tests", "test_empty.py"), []byte("from sample import total\n\ndef test_empty():\n    assert total([]) == 0\n"), 0o644)
			return EngineResult{Summary: "total() returns 0 for an empty list; added test_empty. db_password=hunter22", InputTokens: 1000,
				OutputTokens: 200, CostUSD: 0.25}, nil
		}
		diff, err := os.ReadFile(filepath.Join(run.Work, ".abcp", "change.diff"))
		if err != nil || !strings.Contains(string(diff), "return sum(xs)") || !strings.Contains(run.Prompt, "change.diff") {
			t.Errorf("review saw %q %v", diff, err)
		}
		after = true
		return EngineResult{Summary: "Checked the fix and ran the tests.", InputTokens: 300, OutputTokens: 50, CostUSD: 0.05}, nil
	}
	f := newChangeFixture(t, box, engine)
	queued := f.start(t, "fix-1", "fix", pythonProject)
	if queued.State != Queued || queued.SchemaVersion != serviceapi.ChangeJobSchemaV1 || queued.Kind != "fix" {
		t.Fatalf("queued = %+v", queued)
	}
	job := f.wait(t, queued.ID)
	if job.State != ChangeHandedBack || job.Reason != "" || job.Result == nil || job.Agent == nil {
		t.Fatalf("job = %+v", job)
	}
	if gitInside {
		t.Fatal("the Git directory is inside the work folder")
	}
	// The agent's command ran in the sandbox, in /work/tests, by the shell; a folder outside the repository was refused.
	if len(seen) != 2 || seen[0].ExitCode != 0 || seen[1].Error == "" {
		t.Fatalf("answers = %+v", seen)
	}
	var agentCall *execCall
	for i, c := range box.calls {
		if strings.Join(c.argv, " ") == "/bin/sh -c python -m pytest -q" {
			agentCall = &box.calls[i]
		}
	}
	if agentCall == nil || agentCall.dir != "/work/tests" || !strings.Contains(strings.Join(agentCall.env, " "), "HTTPS_PROXY=http://10.213.0.1:3129") {
		t.Fatalf("calls = %+v", box.calls)
	}
	// The prompt carries the words, the rules and the baseline.
	first := engine.runs[0]
	if !strings.Contains(first.Prompt, "The total is wrong when the list is empty.") || !strings.Contains(first.Prompt, "You have no shell") ||
		!strings.Contains(first.Prompt, "tests 1 passed") || first.MaxTurns != agentTurns || engine.runs[1].MaxTurns != reviewTurns {
		t.Fatalf("prompt = %s", first.Prompt)
	}
	// The change: the fix and the test, nothing the steps wrote, the build folder the code had kept.
	paths := []string{}
	for _, file := range job.Result.Files {
		paths = append(paths, file.Status+" "+file.Path)
	}
	if strings.Join(paths, ", ") != "modified sample.py, added tests/test_empty.py" || job.Result.Insertions != 5 || job.Result.Deletions != 1 {
		t.Fatalf("files = %v +%d -%d", paths, job.Result.Insertions, job.Result.Deletions)
	}
	patch, err := f.m.ChangeJobPatch(context.Background(), job.ID)
	if err != nil || !strings.Contains(string(patch), "+    return sum(xs)") || strings.Contains(string(patch), ".venv") || strings.Contains(string(patch), "target/") ||
		strings.Contains(string(patch), "made.txt") || strings.Contains(string(patch), "change.diff") {
		t.Fatalf("patch = %s %v", patch, err)
	}
	// Before and after, the agent's words masked, its use summed.
	if len(job.Before) != 1 || len(job.After) != 1 || job.Before[0].Steps[2].Tests.Passed != 1 || job.After[0].Steps[2].Tests.Passed != 2 {
		t.Fatalf("checks = %+v %+v", job.Before, job.After)
	}
	if strings.Contains(job.Agent.Summary, "hunter22") || !strings.Contains(job.Agent.Summary, "[hidden]") || job.Agent.Review != "Checked the fix and ran the tests." || job.Agent.Commands != 1 ||
		job.Agent.InputTokens != 1300 || job.Agent.OutputTokens != 250 || job.Agent.CostUSD < 0.29 || job.Agent.CostUSD > 0.31 {
		t.Fatalf("agent = %+v", job.Agent)
	}
	// Nothing of the job is left but its record and its patch.
	for _, gone := range []string{filepath.Join(f.root, "work", job.ID), f.m.socket(job.ID)} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s kept", gone)
		}
	}
	// The same request answers the same job; other words under it are a conflict.
	again, err := f.m.StartChangeJob(context.Background(), serviceapi.ChangeJobCreateV1{RequestID: "fix-1", Source: "fix-1", CacheKey: key, Kind: "fix",
		Words: "The total is wrong when the list is empty."})
	if err != nil || again.ID != job.ID {
		t.Fatalf("replay = %v %v", again.ID, err)
	}
	if _, err := f.m.StartChangeJob(context.Background(), serviceapi.ChangeJobCreateV1{RequestID: "fix-1", Source: "fix-1", CacheKey: key, Kind: "fix",
		Words: "something else"}); !errors.Is(err, serviceapi.ErrChangeJobConflict) {
		t.Fatalf("conflict = %v", err)
	}
}

func TestAChangeThatLeavesTheCodeWorseIsNotHandedBack(t *testing.T) {
	passed, after, testCode := 1, false, 1
	box := &fakeSandbox{proveOK: true}
	box.exec = passingSteps(&passed, &after, &testCode)
	engine := &fakeEngine{act: func(run EngineRun, n int) (EngineResult, error) {
		if n == 1 {
			os.WriteFile(filepath.Join(run.Work, "sample.py"), []byte("def total(xs):\n    return 1\n"), 0o644)
		}
		after = true
		return EngineResult{Summary: "done"}, nil
	}}
	f := newChangeFixture(t, box, engine)
	job := f.wait(t, f.start(t, "fix-2", "fix", pythonProject).ID)
	if job.State != ChangeNotHandedBack || job.Reason != ReasonWorse || job.Result != nil {
		t.Fatalf("job = %+v", job)
	}
	if _, err := f.m.ChangeJobPatch(context.Background(), job.ID); !errors.Is(err, serviceapi.ErrChangeJobNotFound) {
		t.Fatalf("patch = %v", err)
	}
}

func TestNothingChangedACIFileAndAFailedInstallAreNotHandedBack(t *testing.T) {
	passed, after, testCode := 1, false, 0
	box := &fakeSandbox{proveOK: true}
	box.exec = passingSteps(&passed, &after, &testCode)
	engine := &fakeEngine{}
	f := newChangeFixture(t, box, engine)
	if job := f.wait(t, f.start(t, "none", "fix", pythonProject).ID); job.State != ChangeNotHandedBack || job.Reason != ReasonNoChange || len(engine.runs) != 1 {
		t.Fatalf("no change = %+v (%d sessions)", job, len(engine.runs))
	}
	engine.act = func(run EngineRun, n int) (EngineResult, error) {
		os.MkdirAll(filepath.Join(run.Work, ".github", "workflows"), 0o755)
		os.WriteFile(filepath.Join(run.Work, ".github", "workflows", "deploy.yml"), []byte("on: push\n"), 0o644)
		return EngineResult{}, nil
	}
	if job := f.wait(t, f.start(t, "ci", "fix", pythonProject).ID); job.State != ChangeNotHandedBack || job.Reason != ReasonForbidden {
		t.Fatalf("ci = %+v", job)
	}
	sessions := len(engine.runs)
	box.exec = func(_ context.Context, _, _ string, argv []string, out io.Writer) (int, error) {
		if strings.Contains(strings.Join(argv, " "), "pip install") {
			return 1, nil
		}
		return 0, nil
	}
	if job := f.wait(t, f.start(t, "broken", "fix", pythonProject).ID); job.State != ChangeNotHandedBack || job.Reason != ReasonBaseline || len(engine.runs) != sessions {
		t.Fatalf("baseline = %+v", job)
	}
	engine.act = func(EngineRun, int) (EngineResult, error) { return EngineResult{}, errors.New("no model") }
	box.exec = passingSteps(&passed, &after, &testCode)
	if job := f.wait(t, f.start(t, "engine", "fix", pythonProject).ID); job.State != ChangeFailed || job.Reason != ReasonEngine {
		t.Fatalf("engine = %+v", job)
	}
	if job := f.wait(t, f.start(t, "nothing", "fix", map[string]string{"README.md": "hi\n"}).ID); job.State != ChangeFailed || job.Reason != FailNoStack {
		t.Fatalf("no stack = %+v", job)
	}
}

func TestATestJobMustAddPassingTests(t *testing.T) {
	passed, after, testCode := 1, false, 0
	box := &fakeSandbox{proveOK: true}
	box.exec = passingSteps(&passed, &after, &testCode)
	engine := &fakeEngine{act: func(run EngineRun, n int) (EngineResult, error) {
		if n == 1 {
			os.WriteFile(filepath.Join(run.Work, "tests", "test_more.py"), []byte("def test_more():\n    assert True\n"), 0o644)
		}
		after = true
		return EngineResult{Summary: "added a test"}, nil
	}}
	f := newChangeFixture(t, box, engine)
	job := f.wait(t, f.start(t, "tests-1", "tests", pythonProject).ID)
	if job.State != ChangeHandedBack || !strings.Contains(engine.runs[0].Prompt, "Write tests") {
		t.Fatalf("tests job = %+v", job)
	}
}

func TestNoWorseCountsInstallBuildAndTests(t *testing.T) {
	part := func(install, build, test string, tests *serviceapi.StackTestsV1) []serviceapi.StackPartV1 {
		return []serviceapi.StackPartV1{{Stack: StackPython, Steps: []serviceapi.StackStepV1{{Name: "install", State: install}, {Name: "build", State: build},
			{Name: "test", State: test, Tests: tests}}}}
	}
	t3 := &serviceapi.StackTestsV1{Passed: 3}
	cases := []struct {
		name          string
		kind          string
		before, after []serviceapi.StackPartV1
		want          bool
	}{
		{"same", "fix", part(Passed, Passed, Passed, t3), part(Passed, Passed, Passed, t3), true},
		{"build broke", "fix", part(Passed, Passed, Passed, t3), part(Passed, Failed, StepNotRun, nil), false},
		{"a test fails now", "fix", part(Passed, Passed, Passed, t3), part(Passed, Passed, Failed, &serviceapi.StackTestsV1{Passed: 2, Failed: 1}), false},
		{"a failing test fixed", "fix", part(Passed, Passed, Failed, &serviceapi.StackTestsV1{Passed: 2, Failed: 1}), part(Passed, Passed, Passed, t3), true},
		{"tests added", "tests", part(Passed, Passed, Passed, t3), part(Passed, Passed, Passed, &serviceapi.StackTestsV1{Passed: 5}), true},
		{"no tests added", "tests", part(Passed, Passed, Passed, t3), part(Passed, Passed, Passed, t3), false},
		{"first tests", "tests", part(Passed, Passed, StepSkipped, nil), part(Passed, Passed, Passed, &serviceapi.StackTestsV1{Passed: 2}), true},
	}
	for _, c := range cases {
		if got := noWorse(c.kind, c.before, c.after, 0); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	if !baselineReady("fix", part(Passed, Failed, StepNotRun, nil)[0]) || baselineReady("tests", part(Passed, Failed, StepNotRun, nil)[0]) ||
		baselineReady("fix", part(Failed, StepNotRun, StepNotRun, nil)[0]) {
		t.Fatal("baseline")
	}
}

func TestForbiddenPaths(t *testing.T) {
	for p, want := range map[string]bool{
		".github/workflows/ci.yml": true, ".gitlab-ci.yml": true, "deploy/Jenkinsfile": true, ".env": true, "api/.env.production": true,
		".env.example": false, "certs/server.pem": true, "config/app.key": true, ".gitmodules": true, ".npmrc": true,
		"src/app.py": false, "tests/test_env.py": false, "docs/github.md": false, "keys.md": false,
		"packages/x/.github/workflows/deploy.yml": true, "sub/.husky/pre-commit": true, "sub/.claude/settings.json": true, ".mcp.json": true,
		"docs/my.github/notes.md": false,
	} {
		if forbidden(p) != want {
			t.Errorf("%s: %v", p, !want)
		}
	}
}

func TestTheRunToolSpeaksMCPAndOnlyRunsInTheSandbox(t *testing.T) {
	base, _ := os.MkdirTemp("", "st")
	defer os.RemoveAll(base)
	socket := filepath.Join(base, "b.sock")
	box := &fakeSandbox{proveOK: true}
	box.exec = func(_ context.Context, _, dir string, argv []string, out io.Writer) (int, error) {
		if argv[2] == "flood" {
			io.WriteString(out, strings.Repeat(strings.Repeat("\x01", 79)+"\n", 600))
			return 0, nil
		}
		io.WriteString(out, "ran "+dir+" "+argv[2]+"\npassword=hunter2\nfetched with "+npmToken+"\n")
		if argv[2] == "false" {
			return 1, nil
		}
		return 0, nil
	}
	opens := 0
	b, err := listenBroker(context.Background(), socket, time.Hour, func(ctx context.Context, n int) (Session, []string, error) {
		opens++
		s, err := box.Open(ctx, "x", testProfiles().Profiles[0].Spec(), base, base)
		return s, []string{"PATH=/usr/bin"}, err
	}, registrySecrets(testRegistries()))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if info, err := os.Stat(socket); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v %v", info, err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeStackTool(inR, outW, socket); outW.Close() }()
	replies := bufio.NewReader(outR)
	call := func(msg string) map[string]any {
		t.Helper()
		if _, err := io.WriteString(inW, msg+"\n"); err != nil {
			t.Fatal(err)
		}
		line, err := replies.ReadBytes('\n')
		var reply map[string]any
		if err != nil || json.Unmarshal(line, &reply) != nil {
			t.Fatalf("reply %q %v", line, err)
		}
		return reply
	}
	init := call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code"}}}`)
	if r := init["result"].(map[string]any); r["protocolVersion"] != "2025-06-18" || r["capabilities"].(map[string]any)["tools"] == nil {
		t.Fatalf("initialize = %v", init)
	}
	io.WriteString(inW, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
	list := call(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := list["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "run" {
		t.Fatalf("tools = %v", list)
	}
	text := func(reply map[string]any) (string, bool) {
		r := reply["result"].(map[string]any)
		return r["content"].([]any)[0].(map[string]any)["text"].(string), r["isError"].(bool)
	}
	if out, isErr := text(call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"run","arguments":{"command":"npm test","dir":"web"}}}`)); isErr ||
		!strings.Contains(out, "exit code 0") || !strings.Contains(out, "ran /work/web npm test") || strings.Contains(out, "hunter2") ||
		strings.Contains(out, npmToken) || !strings.Contains(out, RegistryMask) {
		t.Fatalf("run = %q %v", out, isErr)
	}
	if out, isErr := text(call(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"run","arguments":{"command":"false"}}}`)); !isErr || !strings.Contains(out, "exit code 1") {
		t.Fatalf("false = %q", out)
	}
	if out, isErr := text(call(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"run","arguments":{"command":"ls","dir":"/etc"}}}`)); !isErr || !strings.HasPrefix(out, "not run") {
		t.Fatalf("outside = %q", out)
	}
	// Output that grows sixfold when escaped still comes back, and the tool goes on working.
	if out, _ := text(call(`{"jsonrpc":"2.0","id":51,"method":"tools/call","params":{"name":"run","arguments":{"command":"flood"}}}`)); !strings.Contains(out, "exit code 0") {
		t.Fatalf("flood = %.80q", out)
	}
	if out, isErr := text(call(`{"jsonrpc":"2.0","id":52,"method":"tools/call","params":{"name":"run","arguments":{"command":"ls","dir":".abcpx"}}}`)); isErr || !strings.Contains(out, "ran /work/.abcpx") {
		t.Fatalf("after the flood = %q", out)
	}
	if out, isErr := text(call(`{"jsonrpc":"2.0","id":53,"method":"tools/call","params":{"name":"run","arguments":{"command":"ls","dir":".abcp/x"}}}`)); !isErr || !strings.HasPrefix(out, "not run") {
		t.Fatalf(".abcp = %q", out)
	}
	if reply := call(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"shell","arguments":{"command":"ls"}}}`); reply["error"] == nil {
		t.Fatalf("other tool = %v", reply)
	}
	if reply := call(`{"jsonrpc":"2.0","id":7,"method":"resources/list"}`); reply["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Fatalf("unknown = %v", reply)
	}
	inW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if opens != 1 || len(box.calls) != 4 || b.commands() != 4 {
		t.Fatalf("opens %d calls %d commands %d", opens, len(box.calls), b.commands())
	}
	// Once the job ends nothing more runs.
	b.close()
	if _, err := net.Dial("unix", socket); err == nil {
		t.Fatal("the socket still answers")
	}
}

func TestClaudeIsRunWithItsToolsListedAndNothingFromTheRepository(t *testing.T) {
	e := &ClaudeEngine{Binary: "/usr/local/bin/claude", Model: "claude-sonnet-4-6"}
	argv := strings.Join(e.argv(EngineRun{Prompt: "p", MCPConfig: "/c/mcp.json", MaxTurns: 80}), " ")
	for _, want := range []string{"/usr/local/bin/claude --restricted -p p --output-format json --max-turns 80", "--strict-mcp-config",
		"--mcp-config /c/mcp.json", "--permission-mode dontAsk", "--tools Read,Glob,Grep,Edit,Write,TodoWrite", "--allowedTools Edit(./**),mcp__abcp__run",
		"--disallowedTools Bash,", "WebFetch", "--model claude-sonnet-4-6"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv lacks %q: %s", want, argv)
		}
	}
	for _, flag := range []string{"--tools ", "--allowedTools "} {
		if strings.Contains(strings.SplitN(strings.SplitN(argv, flag, 2)[1], " ", 2)[0], "Bash") {
			t.Fatalf("the shell is offered by %s", flag)
		}
	}
	t.Setenv("ABCP_SECRET_OF_THE_CONTROLLER", "x")
	t.Setenv("HTTPS_PROXY", "http://proxy:3128")
	env := strings.Join(environment("/c/home", enginelaneFor(map[string]string{"ANTHROPIC_API_KEY": "k"})), " ")
	if strings.Contains(env, "ABCP_SECRET") || !strings.Contains(env, "HOME=/c/home") || !strings.Contains(env, "ANTHROPIC_API_KEY=k") ||
		!strings.Contains(env, "HTTPS_PROXY=http://proxy:3128") {
		t.Fatalf("env = %s", env)
	}
	r, err := parseClaude([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"Fixed it.","total_cost_usd":0.4,"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":100,"cache_creation_input_tokens":5},"modelUsage":{"claude-haiku-4-5":{"outputTokens":3},"claude-sonnet-4-6":{"outputTokens":17},"bad name!":{"outputTokens":99}}}`), nil)
	if err != nil || r.Summary != "Fixed it." || r.InputTokens != 115 || r.CacheReadTokens != 100 || r.CacheWriteTokens != 5 || r.OutputTokens != 20 ||
		r.CostUSD != 0.4 || r.Model != "claude-sonnet-4-6" {
		t.Fatalf("parse = %+v %v", r, err)
	}
	if _, err := parseClaude([]byte(`{"type":"result","subtype":"error_max_turns","is_error":true,"result":""}`), errors.New("exit 1")); err != nil {
		t.Fatalf("out of turns = %v", err)
	}
	if _, err := parseClaude([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true}`), nil); err == nil {
		t.Fatal("a failed session passed")
	}
	if _, err := parseClaude([]byte("not json"), nil); err == nil {
		t.Fatal("no answer passed")
	}
}

// keepingEngine is a fake engine that also names its secret values.
type keepingEngine struct {
	*fakeEngine
	values []string
}

func (k keepingEngine) SecretValues() []string { return k.values }

func TestAChangeOrASummaryCarryingASecretIsNotHandedBack(t *testing.T) {
	passed, after, testCode := 1, false, 0
	box := &fakeSandbox{proveOK: true}
	box.exec = passingSteps(&passed, &after, &testCode)
	inner := &fakeEngine{}
	engine := keepingEngine{fakeEngine: inner, values: []string{"sk-ant-oat01-the-engines-own-token-value"}}
	base, _ := os.MkdirTemp("", "cj")
	defer os.RemoveAll(base)
	os.Mkdir(filepath.Join(base, "src"), 0o755)
	m, err := NewChangeManager(filepath.Join(base, "c"), filepath.Join(base, "src"), "/usr/local/bin/abcp", testProfiles(), box, engine, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	m.freeDisk = func(string) (uint64, bool) { return 100 << 30, true }
	defer m.Close()
	f := &changeFixture{m: m, box: box, engine: inner, root: filepath.Join(base, "c"), sources: filepath.Join(base, "src")}
	for name, content := range map[string]string{
		"token":  "GITHUB = 'ghp_abcdefghijklmnopqrstuvwxyz0123456789'\n",
		"engine": "KEY = 'sk-ant-oat01-the-engines-own-token-value'\n",
	} {
		content := content
		inner.act = func(run EngineRun, n int) (EngineResult, error) {
			if n%2 == 1 {
				os.WriteFile(filepath.Join(run.Work, "settings.py"), []byte(content), 0o644)
			}
			return EngineResult{Summary: "done"}, nil
		}
		job := f.wait(t, f.start(t, "leak-"+name, "fix", pythonProject).ID)
		if job.State != ChangeNotHandedBack || job.Reason != ReasonSecret {
			t.Fatalf("%s = %+v", name, job)
		}
	}
	// A summary naming the engine's own token is not handed back either, and is not kept.
	inner.act = func(run EngineRun, n int) (EngineResult, error) {
		if n%2 == 1 {
			os.WriteFile(filepath.Join(run.Work, "sample.py"), []byte("def total(xs):\n    return sum(xs)\n"), 0o644)
			return EngineResult{Summary: "my key is sk-ant-oat01-the-engines-own-token-value"}, nil
		}
		return EngineResult{Summary: "ok"}, nil
	}
	job := f.wait(t, f.start(t, "leak-summary", "fix", pythonProject).ID)
	if job.State != ChangeNotHandedBack || job.Reason != ReasonSecret || job.Agent == nil || job.Agent.Summary != "" {
		t.Fatalf("summary = %+v", job)
	}
	// Test code with a password literal is not a secret.
	if leaks([]byte(`+    assert login(user="a", password="test1234")`), nil) {
		t.Fatal("a test password literal counted as a secret")
	}
}

func TestAChangeJobChangesOnlyTheStacksItNames(t *testing.T) {
	box := &fakeSandbox{proveOK: true}
	engine := &fakeEngine{}
	f := newChangeFixture(t, box, engine)
	write(t, filepath.Join(f.sources, "gosvc"), map[string]string{"go.mod": "module x\n\ngo 1.22\n", "total.go": "package x\n"})
	job, err := f.m.StartChangeJob(context.Background(), serviceapi.ChangeJobCreateV1{RequestID: "gosvc", Source: "gosvc", CacheKey: key, Kind: "fix",
		Words: "The total is wrong when the list is empty.", Stacks: []string{"python", "java", "node"}})
	if err != nil {
		t.Fatal(err)
	}
	if job = f.wait(t, job.ID); job.State != ChangeFailed || job.Reason != FailUnsupported || len(engine.runs) != 0 || len(box.opens) != 0 {
		t.Fatalf("go not named = %+v (%d sessions, %d sandboxes)", job, len(engine.runs), len(box.opens))
	}
	if _, err := f.m.StartChangeJob(context.Background(), serviceapi.ChangeJobCreateV1{RequestID: "gosvc", Source: "gosvc", CacheKey: key, Kind: "fix",
		Words: "The total is wrong when the list is empty."}); err != serviceapi.ErrChangeJobConflict {
		t.Fatalf("the same request without its stacks = %v", err)
	}
}

func TestAnAssessmentChecksTheTargetVersionsAndHandsBackAPlanAndNoPatch(t *testing.T) {
	// Repo C LEGACY_MIGRATION §4.
	passed, after, testCode := 1, false, 0
	box := &fakeSandbox{proveOK: true}
	box.exec = passingSteps(&passed, &after, &testCode)
	plan := `{"summary": "Python 3.12 is supported; pytest is unpinned.", "paths": [{"what": "Python", "from": "3.12", "to": "3.13"}],
"steps": [{"title": "Pin pytest", "words": "Pin pytest to a version that supports Python 3.13.", "risk": "low", "size": "small",
"files": ["requirements.txt"], "tests": "tests/test_one.py", "tests_first": false}]}`
	answer := "```json\n" + plan + "\n```"
	var sawWork bool
	engine := &fakeEngine{act: func(run EngineRun, n int) (EngineResult, error) {
		// The work folder is as the baseline left it, not as the target's checks would have.
		_, err := os.Stat(filepath.Join(run.Work, ".venv", "bin", "python"))
		sawWork = err == nil
		os.WriteFile(filepath.Join(run.Work, "sample.py"), []byte("changed\n"), 0o644)
		return EngineResult{Summary: answer, InputTokens: 900, OutputTokens: 300, CostUSD: 0.2}, nil
	}}
	f := newChangeFixture(t, box, engine)
	write(t, filepath.Join(f.sources, "old"), pythonProject)
	start := func(request string, targets map[string]string) serviceapi.ChangeJobV1 {
		t.Helper()
		job, err := f.m.StartChangeJob(context.Background(), serviceapi.ChangeJobCreateV1{RequestID: request, Source: "old", CacheKey: key, Kind: "assess",
			Words: "What would it take to move to supported versions?", Targets: targets})
		if err != nil {
			t.Fatal(err)
		}
		return f.wait(t, job.ID)
	}
	job := start("assess-1", map[string]string{"python": "3.13", "java": "21"})
	if job.State != ChangeAssessed || job.Reason != "" || job.Result != nil || job.Plan == nil || len(engine.runs) != 1 || !sawWork {
		t.Fatalf("job = %+v (%d sessions)", job, len(engine.runs))
	}
	if len(job.Before) != 1 || job.Before[0].Version != "3.12" || len(job.After) != 1 || job.After[0].Version != "3.13" ||
		job.After[0].VersionNote != "asked to run on 3.13 instead of 3.12" || job.After[0].Steps[2].State != Passed {
		t.Fatalf("checks = %+v %+v", job.Before, job.After)
	}
	if job.Plan.Summary != "Python 3.12 is supported; pytest is unpinned." || len(job.Plan.Steps) != 1 || job.Plan.Steps[0].Files[0] != "requirements.txt" ||
		job.Agent.Summary != job.Plan.Summary || job.Agent.InputTokens != 900 {
		t.Fatalf("plan = %+v agent = %+v", job.Plan, job.Agent)
	}
	prompt := engine.runs[0].Prompt
	if !strings.Contains(prompt, "Change no file") || !strings.Contains(prompt, "python 3.13 (pip) at the top of the repository, asked to run on 3.13") ||
		!strings.Contains(prompt, "What would it take") || !strings.Contains(prompt, `"tests_first"`) {
		t.Fatalf("prompt = %s", prompt)
	}
	if _, err := f.m.ChangeJobPatch(context.Background(), job.ID); !errors.Is(err, serviceapi.ErrChangeJobNotFound) {
		t.Fatalf("an assessment's patch = %v", err)
	}
	// Without targets, no checks after; an answer that is not a plan is not handed back.
	answer = "I looked at it. It is fine."
	if job := start("assess-2", nil); job.State != ChangeNotHandedBack || job.Reason != ReasonNoPlan || job.Plan != nil || len(job.After) != 0 ||
		job.Agent.Summary != answer {
		t.Fatalf("no plan = %+v", job)
	}
	if _, err := f.m.StartChangeJob(context.Background(), serviceapi.ChangeJobCreateV1{RequestID: "assess-1", Source: "old", CacheKey: key, Kind: "assess",
		Words: "What would it take to move to supported versions?"}); !errors.Is(err, serviceapi.ErrChangeJobConflict) {
		t.Fatalf("the same request without its targets = %v", err)
	}
}

func TestAPlanIsReadOnlyInItsShape(t *testing.T) {
	step := `{"title": "Move to Java 21", "words": "Set the release to 21.", "risk": "medium", "size": "small", "files": ["pom.xml"], "tests": "", "tests_first": true}`
	ok := `{"summary": "Old.", "paths": [], "steps": [` + step + `]}`
	if plan, err := parsePlan("  " + ok + "\n"); err != nil || plan.Steps[0].Title != "Move to Java 21" || !plan.Steps[0].TestsFirst {
		t.Fatalf("plan = %+v %v", plan, err)
	}
	if plan, err := parsePlan(`{"summary": "token ghp_` + strings.Repeat("a", 36) + `", "paths": null, "steps": [` + step + `]}`); err != nil ||
		strings.Contains(plan.Summary, "ghp_") || plan.Paths == nil {
		t.Fatalf("masked = %+v %v", plan, err)
	}
	bad := []string{
		"", "Here is the plan: " + ok, ok + " and more", ok + ok, `{"summary": "Old.", "steps": []}`,
		strings.Replace(ok, `"medium"`, `"huge"`, 1), strings.Replace(ok, `"small"`, `"tiny"`, 1),
		strings.Replace(ok, `"pom.xml"`, `"../pom.xml"`, 1), strings.Replace(ok, `"pom.xml"`, `"/etc/passwd"`, 1),
		strings.Replace(ok, `"pom.xml"`, `"a/../b"`, 1), strings.Replace(ok, `"Old."`, `""`, 1),
		strings.Replace(ok, `"Move to Java 21"`, `"Move\nto"`, 1), strings.Replace(ok, `"summary"`, `"summary": "x", "extra"`, 1),
		strings.Replace(ok, `"Old."`, `"`+strings.Repeat("a", maxPlanSummary+1)+`"`, 1),
		`{"summary": "Old.", "paths": [], "steps": [` + strings.TrimSuffix(strings.Repeat(step+",", maxPlanSteps+1), ",") + `]}`,
	}
	for _, text := range bad {
		if _, err := parsePlan(text); err == nil {
			t.Errorf("accepted %.120q", text)
		}
	}
}

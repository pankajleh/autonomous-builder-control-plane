//go:build linux

package stackjob

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Keys of Anthropic's shape that exist only in these tests: the one the change engine's lane holds first, and the one
// written to it afterwards (PUT /v1/lanes/change-api/key, docs/plans/lane-keys.md).
const (
	changeLaneFirstKey  = "sk-ant-api03-TestOnlyKeyInTheChangeEnginesLaneFirst-0001"
	changeLaneSecondKey = "sk-ant-api03-TestOnlyKeyInTheChangeEnginesLaneSecond-002"
)

// changeLaneFile is the change engine's lane file as the host has it: the key and the models, no configuration folder.
func changeLaneFile(key string) string {
	return "ANTHROPIC_API_KEY=" + key + "\nANTHROPIC_MODEL=claude-sonnet-5-5\nANTHROPIC_SMALL_FAST_MODEL=claude-haiku-4-5\n"
}

// writePrivate writes a file the controller's user alone reads, replacing it whole as a lane key write does (a new file
// renamed over it).
func writePrivate(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	next := path + ".next"
	if err := os.WriteFile(next, data, mode); err != nil || os.Chmod(next, mode) != nil || os.Rename(next, path) != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The change engine reads its lane when each job starts (Pin), as a build's run does: a key written to the lane reaches
// the next job without a restart, and a job already running keeps the key it started with for every session.
func TestTheChangeEngineReadsItsLaneWhenEachJobStartsAndAJobKeepsItsKey(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	// A Claude Code that answers with the key its session was given, so the test sees which key each session ran on.
	claude := filepath.Join(root, "claude")
	writePrivate(t, claude, []byte("#!/bin/sh\nprintf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"%s\"}' \"$ANTHROPIC_API_KEY\"\n"), 0o700)
	lane := filepath.Join(root, "lanes", "change-api.env")
	if err := os.Mkdir(filepath.Dir(lane), 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivate(t, lane, []byte(changeLaneFile(changeLaneFirstKey)), 0o600)
	file, _ := json.Marshal(EngineFileV1{SchemaVersion: 1, Binary: claude, LaneName: "change-api", LaneFile: lane})
	engineFile := filepath.Join(root, "change-engine.json")
	writePrivate(t, engineFile, file, 0o600)
	engine, err := LoadEngine(engineFile)
	if err != nil {
		t.Fatal(err)
	}
	session := func(e Engine) string {
		t.Helper()
		work, home := t.TempDir(), t.TempDir()
		said, err := e.Run(context.Background(), EngineRun{Work: work, Home: home, MCPConfig: filepath.Join(root, "mcp.json"), Prompt: "p", MaxTurns: 1})
		if err != nil {
			t.Fatal(err)
		}
		return said.Summary
	}

	running, err := engine.Pin()
	if err != nil {
		t.Fatal(err)
	}
	if got := session(running); got != changeLaneFirstKey {
		t.Fatalf("the first job's session ran on %.20q…", got)
	}
	// A new key is written to the lane while the first job runs.
	writePrivate(t, lane, []byte(changeLaneFile(changeLaneSecondKey)), 0o600)
	next, err := engine.Pin()
	if err != nil {
		t.Fatal(err)
	}
	if session(running) != changeLaneFirstKey || session(next) != changeLaneSecondKey {
		t.Fatal("the running job did not keep its key, or the next job did not get the new one")
	}
	// Each job's secret checks are on its own key.
	values := func(e Engine) []string { return e.(secretKeeper).SecretValues() }
	if !slices.Contains(values(running), changeLaneFirstKey) || slices.Contains(values(running), changeLaneSecondKey) ||
		!slices.Contains(values(next), changeLaneSecondKey) || slices.Contains(values(next), changeLaneFirstKey) {
		t.Fatal("a job's secret values are not its own key's")
	}
	// The engine itself, unpinned, reads the file each time, as before.
	if session(engine) != changeLaneSecondKey {
		t.Fatal("the engine did not read its lane file")
	}
	// A lane file no build would read fails the next job's start, never the running one's.
	writePrivate(t, lane, []byte(changeLaneFile(changeLaneSecondKey)), 0o644)
	if _, err := engine.Pin(); err == nil {
		t.Fatal("a lane file open to others was read")
	}
	if session(running) != changeLaneFirstKey {
		t.Fatal("the running job lost its key")
	}
}

// laneEngine plays an engine on a lane file: each job pins the key the lane holds when the job starts, and each of the
// job's sessions records the key it ran on.
type laneEngine struct {
	t   *testing.T
	mu  sync.Mutex
	key string
	ran []string
	act func(run EngineRun, n int, key string) (EngineResult, error)
}

func (e *laneEngine) Run(context.Context, EngineRun) (EngineResult, error) {
	e.t.Error("a session ran on the lane file now, not on the settings its job started with")
	return EngineResult{}, errors.New("not pinned")
}

func (e *laneEngine) Pin() (Engine, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.key == "" {
		return nil, errors.New("the lane file cannot be read")
	}
	return &pinnedLaneEngine{lane: e, key: e.key}, nil
}

func (e *laneEngine) setKey(key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.key = key
}

func (e *laneEngine) sessions() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.ran)
}

type pinnedLaneEngine struct {
	lane *laneEngine
	key  string
}

func (p *pinnedLaneEngine) Run(_ context.Context, run EngineRun) (EngineResult, error) {
	p.lane.mu.Lock()
	p.lane.ran = append(p.lane.ran, p.key)
	n, act := len(p.lane.ran), p.lane.act
	p.lane.mu.Unlock()
	return act(run, n, p.key)
}

func (p *pinnedLaneEngine) SecretValues() []string { return []string{p.key} }

func TestAChangeJobRunsOnTheLaneAsItWasWhenTheJobStarted(t *testing.T) {
	passed, after, testCode := 1, false, 0
	box := &fakeSandbox{proveOK: true}
	box.exec = passingSteps(&passed, &after, &testCode)
	// Values no pattern finds on its own: only the job's own settings tell them from code.
	const first, second, third, fourth = "first-lane-setting-value-0001", "second-lane-setting-value-0002", "third-lane-setting-value-0003", "fourth-lane-setting-value-0004"
	engine := &laneEngine{t: t, key: first}
	var leak atomic.Bool
	engine.act = func(run EngineRun, n int, key string) (EngineResult, error) {
		if n%2 == 1 {
			// While each job's agent works, another key is written to the lane.
			next := map[string]string{first: second, second: third, third: fourth}[key]
			engine.setKey(next)
			code := "def total(xs):\n    return sum(xs)\n"
			if leak.Load() {
				code += "KEY = '" + key + "'\n"
			}
			os.WriteFile(filepath.Join(run.Work, "sample.py"), []byte(code), 0o644)
		}
		return EngineResult{Summary: "done"}, nil
	}
	base, err := os.MkdirTemp("", "cj")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	os.Mkdir(filepath.Join(base, "src"), 0o755)
	m, err := NewChangeManager(filepath.Join(base, "c"), filepath.Join(base, "src"), "/usr/local/bin/abcp", testProfiles(), box, engine, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	m.freeDisk = func(string) (uint64, bool) { return 100 << 30, true }
	defer m.Close()
	f := &changeFixture{m: m, box: box, root: filepath.Join(base, "c"), sources: filepath.Join(base, "src")}

	// The first job's agent and review both run on the key the lane held when it started; the next job on the new one.
	if job := f.wait(t, f.start(t, "lane-1", "fix", pythonProject).ID); job.State != ChangeHandedBack {
		t.Fatalf("the first job = %+v", job)
	}
	if job := f.wait(t, f.start(t, "lane-2", "fix", pythonProject).ID); job.State != ChangeHandedBack {
		t.Fatalf("the second job = %+v", job)
	}
	if got := strings.Join(engine.sessions(), ","); got != strings.Join([]string{first, first, second, second}, ",") {
		t.Fatalf("sessions ran on %s", got)
	}
	// A change carrying the job's own key is not handed back, though the lane holds another key by then.
	leak.Store(true)
	if job := f.wait(t, f.start(t, "lane-3", "fix", pythonProject).ID); job.State != ChangeNotHandedBack || job.Reason != ReasonSecret {
		t.Fatalf("a change with the job's key = %+v", job)
	}
	// A lane that can't be read when a job starts ends it before anything runs.
	engine.setKey("")
	calls := len(box.calls)
	if job := f.wait(t, f.start(t, "lane-4", "fix", pythonProject).ID); job.State != ChangeFailed || job.Reason != ReasonEngine || len(job.Before) != 0 {
		t.Fatalf("a job on an unreadable lane = %+v", job)
	}
	if len(box.calls) != calls || len(engine.sessions()) != 6 {
		t.Fatalf("a job on an unreadable lane ran %d commands and %d sessions", len(box.calls)-calls, len(engine.sessions())-6)
	}
}

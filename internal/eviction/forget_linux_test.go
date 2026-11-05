//go:build linux

package eviction

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

const (
	otherRun        = "admission-0000000000000000000000000000000000000000000000000000000000000002"
	forgetRequest   = "0f8fad5b-d9cb-469f-a165-70867728950e"
	anotherRequest  = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	sessionsSegment = "runs"
)

// forgetFixture is a product repository holding this run's and another run's branches, worktrees, pinned checkpoints,
// inputs, progress, evidence, engine sessions and a shared base, with this run's ledger, eviction record and activity.
type forgetFixture struct {
	*fixture
	root, ledgerPath           string
	evidence, otherEvidence    string
	sessions, otherSessions    string
	otherInputs, otherProgress string
	otherBranch, otherPinned   string
	base                       string
	forgets                    *ForgetStore
	previewsForgotten          []string
	activityForgotten          []string
	events                     []ledger.Event
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func progressLog(run string) string {
	return "# Ralphex Progress Log\nPlan: plan.md\nBranch: abcp/" + run + "\nMode: full\n------------------------------------------------------------\nthe person's words and file names\n"
}

func newForgetFixture(t *testing.T, walled bool) *forgetFixture {
	t.Helper()
	base := newFixture(t)
	root := filepath.Dir(base.scope.Repository)
	repo := base.scope.Repository
	f := &forgetFixture{fixture: base, root: root, base: base.scope.Base}
	// Another run of another app in the same repository: its branch, worktree and pinned checkpoint stay.
	otherWorktree := filepath.Join(repo, ".ralphex", "worktrees", "abcp", otherRun)
	run(t, repo, "worktree", "add", "--quiet", "-b", "abcp/"+otherRun, otherWorktree, f.base)
	write(t, filepath.Join(otherWorktree, "other.md"), "other app\n")
	run(t, otherWorktree, "add", "other.md")
	run(t, otherWorktree, "commit", "--quiet", "-m", "other")
	f.otherBranch = run(t, otherWorktree, "rev-parse", "HEAD")
	f.otherPinned = CheckpointRef(otherRun, f.otherBranch)
	run(t, repo, "update-ref", f.otherPinned, f.otherBranch)
	// This run's pinned checkpoint.
	run(t, repo, "update-ref", CheckpointRef(testRun, f.checkpoint), f.checkpoint)
	// Inputs, the plan copy, progress (this run's, its archive, and another run's).
	f.scope.Inputs = filepath.Join(repo, ".abcp-input", testRun)
	write(t, filepath.Join(f.scope.Inputs, "plan.md"), "# the person's plan\n")
	write(t, filepath.Join(f.scope.Inputs, "manifest.json"), "{}\n")
	f.otherInputs = filepath.Join(repo, ".abcp-input", otherRun, "plan.md")
	write(t, f.otherInputs, "# another person's plan\n")
	sum := sha256.Sum256([]byte(testRun))
	write(t, filepath.Join(repo, ralphex.ExecutionPlanHandoffPrefixV1+hex.EncodeToString(sum[:]), "plan.md"), "# the person's plan\n")
	if !walled {
		write(t, filepath.Join(repo, ".ralphex", "progress", "progress-plan.txt"), progressLog(testRun))
		write(t, filepath.Join(repo, ".ralphex", "progress", "history", "plan", "archive-20261101-000000-a.txt"), progressLog(testRun))
	}
	f.otherProgress = filepath.Join(repo, ".ralphex", "progress", "progress-plan-review.txt")
	write(t, f.otherProgress, progressLog(otherRun))
	// Evidence of both runs.
	f.evidence = filepath.Join(root, "evidence", testRun)
	write(t, filepath.Join(f.evidence, "authority.json"), `{"authority":1}`)
	write(t, filepath.Join(f.evidence, "ralphex-stdout.log"), "the build's output\n")
	f.otherEvidence = filepath.Join(root, "evidence", otherRun, "authority.json")
	write(t, f.otherEvidence, `{"authority":2}`)
	// Engine sessions, tied to the run by its ledger.
	f.sessions = filepath.Join(root, "codex", "sessions", sessionsSegment, testRun)
	write(t, filepath.Join(f.sessions, "rollout.jsonl"), "the prompts\n")
	f.otherSessions = filepath.Join(root, "codex", "sessions", sessionsSegment, otherRun, "rollout.jsonl")
	write(t, f.otherSessions, "other prompts\n")
	// A walled run: its own folder holds its repository (with its worktree and progress), home and engine home; its
	// branch is in the product repository, but no worktree of it.
	if walled {
		run(t, repo, "worktree", "remove", "--force", f.worktree)
		folder := filepath.Join(root, "builds", testRun)
		write(t, filepath.Join(folder, "repo", ".ralphex", "progress", "progress-plan.txt"), progressLog(testRun))
		write(t, filepath.Join(folder, "home", ".npm", "cache"), "cache\n")
		write(t, filepath.Join(folder, "engine-home", "config.toml"), "model = \"x\"\n")
		f.scope.RunFolder, f.scope.RunRepository = folder, filepath.Join(folder, "repo")
		f.worktree = ""
	}
	// The ledger: the run's start records its isolation with its session folder.
	f.ledgerPath = filepath.Join(root, "ledger", testRun, "events.jsonl")
	writer, err := ledger.NewJSONLLedger(f.ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	created, _ := ledger.NewEvent(testRun, string(domain.StateRunCreated), "controller", "test")
	created.Payload = map[string]any{"state": string(domain.StateRunCreated)}
	if err := writer.Append(created); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	f.events = []ledger.Event{
		{StateTo: domain.StateExecutionStarting, Timestamp: now.Add(-26 * time.Hour), Payload: map[string]any{"isolation": map[string]any{"sessions": f.sessions, "run_folder": "x"}}},
		// Not the run's own session folder: left alone.
		{StateTo: domain.StateImplementing, Timestamp: now.Add(-26 * time.Hour), Payload: map[string]any{"isolation": map[string]any{"sessions": filepath.Join(root, "codex", "sessions")}}},
		{StateTo: domain.State(f.state), Timestamp: f.finishedAt},
	}
	// An earlier eviction's record.
	if err := f.store.Write(RecordV1{Kind: "WorktreeEvictionV1", SchemaVersion: 1, RunID: testRun, AuthorityDigest: f.scope.AuthorityDigest,
		Repository: repo, Branch: f.scope.Branch, Base: f.base, Worktree: filepath.Join(repo, ".ralphex", "worktrees", "abcp", testRun), Reason: ReasonIdle,
		EvictedAt: now.Format(time.RFC3339Nano), Checkpoints: []PinnedCheckpoint{{SHA: f.checkpoint, Ref: CheckpointRef(testRun, f.checkpoint)}}}); err != nil {
		t.Fatal(err)
	}
	if f.forgets, err = OpenForgetStore(root); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *forgetFixture) ReadRun(run string) (runtimecatalog.RunRegistrationV1, error) {
	return runtimecatalog.RunRegistrationV1{RunID: run, CanonicalEvidenceRoot: filepath.Join(f.root, "evidence", run)}, nil
}
func (f *forgetFixture) Snapshot(context.Context, string) (readmodel.Snapshot, error) {
	s := readmodel.Snapshot{Events: append([]ledger.Event{}, f.events...)}
	s.Events[len(s.Events)-1].StateTo, s.Events[len(s.Events)-1].Timestamp = domain.State(f.state), f.finishedAt
	s.Projection.CurrentState = f.state
	return s, nil
}
func (f *forgetFixture) ForgetPreviews(_ context.Context, run string) error {
	f.previewsForgotten = append(f.previewsForgotten, run)
	return nil
}
func (f *forgetFixture) ForgetActivity(run string) error {
	f.activityForgotten = append(f.activityForgotten, run)
	return nil
}
func (f *forgetFixture) WithExistingWritableLedgerAndSnapshot(_ context.Context, run string, operation func(*ledger.JSONLLedger, func() (readmodel.Snapshot, error)) error) error {
	writer, err := ledger.OpenExistingJSONLLedger(filepath.Join(f.root, "ledger", run, "events.jsonl"))
	if err != nil {
		return err
	}
	defer writer.Close()
	return operation(writer, func() (readmodel.Snapshot, error) { return f.Snapshot(context.Background(), run) })
}
func (f *forgetFixture) sweeper() *Sweeper {
	s := f.fixture.sweeper()
	s.Runs, s.States, s.Activity, s.Previews, s.Forgets, s.Ledgers = f, f, f, f, f.forgets, f
	return s
}

func (f *forgetFixture) ledgerEvents(t *testing.T, eventType string) []ledger.Event {
	t.Helper()
	file, err := os.Open(f.ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var found []ledger.Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var e ledger.Event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if e.EventType == eventType {
			found = append(found, e)
			if strings.Contains(scanner.Text(), "/") {
				t.Fatalf("RUN_FORGOTTEN carries a path: %s", scanner.Text())
			}
		}
	}
	return found
}

func digestOf(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// Forgetting removes only this run's refs, at their SHAs, and its worktree, inputs, progress, evidence bodies, engine
// sessions, eviction record, previews' sources and activity; another run's things and the shared base stay; the ledger
// keeps RUN_FORGOTTEN and the record keeps the artifact hash manifest.
func TestForgetRemovesOnlyTheRunsThingsAndKeepsItsRecord(t *testing.T) {
	f := newForgetFixture(t, false)
	repo := f.scope.Repository
	branchSHA := run(t, repo, "rev-parse", "refs/heads/"+f.scope.Branch)
	if !exists(filepath.Join(repo, ".git", "logs", "refs", "heads", "abcp", testRun)) {
		t.Fatal("the fixture's branch has no reflog to remove")
	}
	result, err := f.sweeper().Forget(context.Background(), testRun, "app-deleted", forgetRequest)
	if err != nil {
		t.Fatal(err)
	}
	record := result.Record
	wantRefs := []serviceapi.ForgottenRefV1{{Ref: CheckpointRef(testRun, f.checkpoint), SHA: f.checkpoint}, {Ref: "refs/heads/abcp/" + testRun, SHA: branchSHA}}
	if result.Status != Forgotten || len(record.Refs) != 2 || record.Refs[0] != wantRefs[0] || record.Refs[1] != wantRefs[1] {
		t.Fatalf("forget = %+v", result)
	}
	// Refs: this run's gone with the branch's reflog; another run's and the shared base at their SHAs.
	if out := run(t, repo, "for-each-ref", "--format=%(refname)", "refs/heads/abcp/"+testRun, "refs/abcp/checkpoints/"+testRun); out != "" {
		t.Fatalf("the run's refs are still there: %s", out)
	}
	if exists(filepath.Join(repo, ".git", "logs", "refs", "heads", "abcp", testRun)) {
		t.Fatal("the branch's reflog is still there")
	}
	if run(t, repo, "rev-parse", "refs/heads/abcp/"+otherRun) != f.otherBranch || run(t, repo, "rev-parse", f.otherPinned) != f.otherBranch || run(t, repo, "rev-parse", "refs/heads/main") != f.base {
		t.Fatal("another run's refs or the shared base changed")
	}
	// Files: this run's gone, another run's kept.
	for _, gone := range []string{f.worktree, f.scope.Inputs, filepath.Join(repo, ".ralphex", "progress", "progress-plan.txt"),
		filepath.Join(repo, ".ralphex", "progress", "history", "plan", "archive-20261101-000000-a.txt"), f.sessions,
		filepath.Join(f.evidence, "authority.json"), filepath.Join(f.evidence, "ralphex-stdout.log"), filepath.Join(f.root, "evictions", testRun+".json")} {
		if exists(gone) {
			t.Fatalf("%s is still there", gone)
		}
	}
	sum := sha256.Sum256([]byte(testRun))
	if exists(filepath.Join(repo, ralphex.ExecutionPlanHandoffPrefixV1+hex.EncodeToString(sum[:]))) {
		t.Fatal("the plan copy is still there")
	}
	if strings.Contains(run(t, repo, "worktree", "list", "--porcelain"), testRun) {
		t.Fatal("Git still lists the run's worktree")
	}
	for _, kept := range []string{f.evidence, f.otherInputs, f.otherProgress, f.otherEvidence, f.otherSessions, filepath.Join(repo, ".ralphex", "worktrees", "abcp", otherRun, "other.md"), filepath.Join(f.root, "codex", "sessions")} {
		if !exists(kept) {
			t.Fatalf("%s was removed", kept)
		}
	}
	if len(f.previewsForgotten) != 1 || len(f.activityForgotten) != 1 {
		t.Fatalf("previews forgotten %v, activity forgotten %v", f.previewsForgotten, f.activityForgotten)
	}
	// Kept: the ledger with RUN_FORGOTTEN, and the record with the artifact hash manifest.
	forgotten := f.ledgerEvents(t, RunForgottenEvent)
	if len(forgotten) != 1 || forgotten[0].Payload["reason"] != "app-deleted" || forgotten[0].Payload["request_id"] != forgetRequest ||
		forgotten[0].Payload["refs_removed"] != float64(2) || forgotten[0].Payload["artifacts_removed"] != float64(2) {
		t.Fatalf("RUN_FORGOTTEN = %+v", forgotten)
	}
	if len(f.ledgerEvents(t, string(domain.StateRunCreated))) != 1 {
		t.Fatal("the ledger lost its events")
	}
	sealed, ok, err := f.forgets.Read(testRun)
	if err != nil || !ok || sealed.ForgottenAt != record.ForgottenAt || len(sealed.Sessions) != 1 || sealed.Sessions[0] != f.sessions {
		t.Fatalf("record = %+v, %v, %v", sealed, ok, err)
	}
	manifest := map[string]ForgottenArtifact{}
	for _, a := range sealed.Artifacts {
		manifest[a.Name] = a
	}
	if len(manifest) != 2 || manifest["authority.json"].SHA256 != digestOf(`{"authority":1}`) || manifest["ralphex-stdout.log"].Bytes != int64(len("the build's output\n")) {
		t.Fatalf("artifact manifest = %+v", sealed.Artifacts)
	}
	info, err := os.Stat(filepath.Join(f.root, "forgotten", testRun+".json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record file = %v, %v", info, err)
	}
}

func TestForgetReplaysAndLaterCallsAnswerTheSameRecord(t *testing.T) {
	f := newForgetFixture(t, false)
	first, err := f.sweeper().Forget(context.Background(), testRun, "app-deleted", forgetRequest)
	if err != nil {
		t.Fatal(err)
	}
	later := f.sweeper()
	later.Now = func() time.Time { return now.Add(time.Hour) }
	replay, err := later.Forget(context.Background(), testRun, "app-deleted", forgetRequest)
	if err != nil || replay.Status != Forgotten || replay.Record.ForgottenAt != first.Record.ForgottenAt || len(replay.Record.Refs) != len(first.Record.Refs) {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	other, err := later.Forget(context.Background(), testRun, "account-deleted", anotherRequest)
	if err != nil || other.Status != AlreadyForgotten || other.Record.RequestID != forgetRequest || other.Record.Reason != "app-deleted" || other.Record.ForgottenAt != first.Record.ForgottenAt {
		t.Fatalf("later call = %+v, %v", other, err)
	}
	if v := other.Record.V1(other.Status); serviceapi.ValidateRunForgetV1(v, testRun) != nil || v.RefsRemoved[0] != first.Record.Refs[0] {
		t.Fatalf("answer = %+v", v)
	}
	if n := len(f.ledgerEvents(t, RunForgottenEvent)); n != 1 {
		t.Fatalf("RUN_FORGOTTEN appended %d times", n)
	}
	for _, bad := range [][2]string{{"task-closed", forgetRequest}, {"app-deleted", "request-1"}, {"app-deleted", strings.ToUpper(forgetRequest)}} {
		if _, err := f.sweeper().Forget(context.Background(), testRun, bad[0], bad[1]); !errors.Is(err, ErrInvalidForget) {
			t.Fatalf("%v accepted: %v", bad, err)
		}
	}
}

func TestForgetRefusesAnUnfinishedOrHeldRun(t *testing.T) {
	for _, state := range []domain.State{domain.StateImplementing, domain.StateHumanDecisionRequired, domain.StateExecutionStarting} {
		f := newForgetFixture(t, false)
		f.state = string(state)
		if _, err := f.sweeper().Forget(context.Background(), testRun, "app-deleted", forgetRequest); !errors.Is(err, ErrRunNotFinished) {
			t.Fatalf("%s: forget = %v", state, err)
		}
		f.assertUntouched(t)
	}
	f := newForgetFixture(t, false)
	write(t, filepath.Join(f.root, "holds", testRun), "incident 42\n")
	if _, err := f.sweeper().Forget(context.Background(), testRun, "app-deleted", forgetRequest); !errors.Is(err, ErrRunOnHold) {
		t.Fatalf("held: forget = %v", err)
	}
	f.assertUntouched(t)
	if err := os.Remove(filepath.Join(f.root, "holds", testRun)); err != nil {
		t.Fatal(err)
	}
	if result, err := f.sweeper().Forget(context.Background(), testRun, "app-deleted", forgetRequest); err != nil || result.Status != Forgotten {
		t.Fatalf("hold lifted: %+v, %v", result, err)
	}
}

func (f *forgetFixture) assertUntouched(t *testing.T) {
	t.Helper()
	if sealed, err := f.forgets.Sealed(testRun); err != nil || sealed {
		t.Fatalf("a record was sealed: %v, %v", sealed, err)
	}
	for _, path := range []string{f.worktree, f.scope.Inputs, f.sessions, filepath.Join(f.evidence, "authority.json")} {
		if !exists(path) {
			t.Fatalf("%s was removed", path)
		}
	}
	run(t, f.scope.Repository, "rev-parse", "--verify", "refs/heads/abcp/"+testRun)
	if len(f.previewsForgotten) != 0 || len(f.activityForgotten) != 0 || len(f.ledgerEvents(t, RunForgottenEvent)) != 0 {
		t.Fatal("a refused forget removed something")
	}
}

// A forget interrupted after its record was sealed is finished by the next call; meanwhile the sweep and releases
// leave the run alone (an eviction would pin refs the record does not name).
func TestAnInterruptedForgetIsFinished(t *testing.T) {
	f := newForgetFixture(t, false)
	s := f.sweeper()
	s.mu.Lock()
	sealed, err := s.sealForget(context.Background(), testRun, "app-deleted", forgetRequest)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// Part done: the evidence and the branch went before the interruption.
	if err := removeEvidenceBodies(f.evidence); err != nil {
		t.Fatal(err)
	}
	run(t, f.scope.Repository, "update-ref", "-d", "refs/heads/abcp/"+testRun)
	if err := f.store.Remove(testRun); err != nil {
		t.Fatal(err)
	}
	f.finishedAt = now.Add(-30 * 24 * time.Hour)
	if evicted, err := s.Sweep(context.Background()); err != nil || len(evicted) != 0 {
		t.Fatalf("sweep of a forgotten run = %+v, %v", evicted, err)
	}
	if _, err := s.Release(context.Background(), testRun, ReasonTaskClosed); !errors.Is(err, ErrRunForgotten) {
		t.Fatalf("release of a forgotten run = %v", err)
	}
	if exists(filepath.Join(f.root, "evictions", testRun+".json")) || !exists(f.worktree) {
		t.Fatal("the sweep or a release touched a forgotten run")
	}
	result, err := f.sweeper().Forget(context.Background(), testRun, "account-deleted", anotherRequest)
	if err != nil || result.Status != AlreadyForgotten || result.Record.ForgottenAt != sealed.ForgottenAt || len(result.Record.Artifacts) != 2 {
		t.Fatalf("finish = %+v, %v", result, err)
	}
	for _, gone := range []string{f.worktree, f.scope.Inputs, f.sessions, filepath.Join(f.scope.Repository, ".ralphex", "progress", "progress-plan.txt")} {
		if exists(gone) {
			t.Fatalf("%s is still there", gone)
		}
	}
	if out := run(t, f.scope.Repository, "for-each-ref", "--format=%(refname)", "refs/abcp/checkpoints/"+testRun); out != "" {
		t.Fatalf("pinned refs are still there: %s", out)
	}
	if len(f.ledgerEvents(t, RunForgottenEvent)) != 1 {
		t.Fatal("RUN_FORGOTTEN missing")
	}
}

// A ref is removed only at the SHA the record names: one moved since the record was sealed stops the forget.
func TestForgetRemovesARefOnlyAtTheRecordsSHA(t *testing.T) {
	f := newForgetFixture(t, false)
	s := f.sweeper()
	s.mu.Lock()
	_, err := s.sealForget(context.Background(), testRun, "app-deleted", forgetRequest)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	run(t, f.scope.Repository, "update-ref", "refs/heads/abcp/"+testRun, f.base)
	if _, err := f.sweeper().Forget(context.Background(), testRun, "app-deleted", forgetRequest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("forget of a moved ref = %v", err)
	}
	if run(t, f.scope.Repository, "rev-parse", "refs/heads/abcp/"+testRun) != f.base {
		t.Fatal("a ref at another SHA was removed")
	}
	if len(f.ledgerEvents(t, RunForgottenEvent)) != 0 {
		t.Fatal("RUN_FORGOTTEN was appended to an unfinished forget")
	}
}

// A walled run's whole folder goes (its repository, worktree, progress, home and engine home); its branch in the product
// repository goes; the product repository's progress, which is other runs', stays.
func TestForgetRemovesAWalledRunsFolder(t *testing.T) {
	f := newForgetFixture(t, true)
	result, err := f.sweeper().Forget(context.Background(), testRun, "app-deleted", forgetRequest)
	if err != nil || result.Status != Forgotten || result.Record.RunFolder != f.scope.RunFolder || result.Record.Worktree != "" {
		t.Fatalf("forget = %+v, %v", result, err)
	}
	if exists(f.scope.RunFolder) || exists(f.scope.Inputs) || exists(f.sessions) {
		t.Fatal("a walled run's folder, inputs or sessions are still there")
	}
	if out := run(t, f.scope.Repository, "for-each-ref", "--format=%(refname)", "refs/heads/abcp/"+testRun, "refs/abcp/checkpoints/"+testRun); out != "" {
		t.Fatalf("the run's refs are still there: %s", out)
	}
	if !exists(f.otherProgress) || !exists(filepath.Join(f.scope.Repository, "README.md")) {
		t.Fatal("a walled run's forget reached the product repository's own files")
	}
}

// A record names only the run's own paths and refs.
func TestForgetRecordRejectsForeignPathsAndRefs(t *testing.T) {
	f := newForgetFixture(t, false)
	good := ForgetRecordV1{Kind: "RunForgetV1", SchemaVersion: 1, RunID: testRun, Reason: "app-deleted", RequestID: forgetRequest,
		ForgottenAt: now.Format(time.RFC3339Nano), Repository: f.scope.Repository, Refs: []serviceapi.ForgottenRefV1{{Ref: "refs/heads/abcp/" + testRun, SHA: f.checkpoint}},
		Worktree: f.worktree, Inputs: f.scope.Inputs, Evidence: f.evidence, Sessions: []string{f.sessions}, Artifacts: []ForgottenArtifact{}}
	if err := good.validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ForgetRecordV1){
		"another run's ref": func(r *ForgetRecordV1) { r.Refs[0].Ref = "refs/heads/abcp/" + otherRun },
		"main":              func(r *ForgetRecordV1) { r.Refs[0].Ref = "refs/heads/main" },
		"another run's worktree": func(r *ForgetRecordV1) {
			r.Worktree = filepath.Join(f.scope.Repository, ".ralphex", "worktrees", "abcp", otherRun)
		},
		"inputs outside":   func(r *ForgetRecordV1) { r.Inputs = filepath.Join(f.root, testRun) },
		"a sessions root":  func(r *ForgetRecordV1) { r.Sessions = []string{filepath.Join(f.root, "codex", "sessions")} },
		"another evidence": func(r *ForgetRecordV1) { r.Evidence = filepath.Join(f.root, "evidence", otherRun) },
		"an artifact path": func(r *ForgetRecordV1) { r.Artifacts = []ForgottenArtifact{{Name: "../x", SHA256: digestOf("x")}} },
	} {
		r := good
		r.Refs = append([]serviceapi.ForgottenRefV1{}, good.Refs...)
		mutate(&r)
		if err := f.forgets.write(r); err == nil {
			t.Fatalf("%s: written", name)
		}
	}
}

// blobGoneAfterGC reports whether the fixture's feature.md blob, which only this run's refs and worktree reached, is gone
// once the repository is collected with no grace.
func (f *forgetFixture) blobGoneAfterGC(t *testing.T) bool {
	t.Helper()
	repo := f.scope.Repository
	blob := run(t, repo, "rev-parse", f.checkpoint+":feature.md")
	run(t, repo, "-c", "gc.reflogExpire=now", "-c", "gc.reflogExpireUnreachable=now", "gc", "--quiet", "--prune=now")
	_, err := git(context.Background(), repo, "cat-file", "-e", blob)
	return err != nil
}

// An exact edit never runs in the wall: its worktree is in the product repository at the builder's place, though its
// admission named a run folder (never made). The forget finds it, records both, removes both, and its blob goes at gc:
// with the binding as it was (the run folder in the scope) and as it is now (the run folder ignored for an exact edit).
func TestForgetRemovesAnExactEditsWorktreeInTheProductRepository(t *testing.T) {
	for _, binding := range []string{"run folder named", "run folder ignored"} {
		t.Run(binding, func(t *testing.T) {
			f := newForgetFixture(t, false)
			f.scope.ExactEdit = true
			folder := filepath.Join(f.root, "builds", testRun)
			if binding == "run folder named" {
				f.scope.RunFolder, f.scope.RunRepository = folder, filepath.Join(folder, "repo")
			}
			result, err := f.sweeper().Forget(context.Background(), testRun, "app-deleted", forgetRequest)
			if err != nil || result.Status != Forgotten {
				t.Fatalf("forget = %+v, %v", result, err)
			}
			if result.Record.Worktree != f.worktree || result.Record.RunFolder != f.scope.RunFolder {
				t.Fatalf("record worktree=%q run_folder=%q, want %q and %q", result.Record.Worktree, result.Record.RunFolder, f.worktree, f.scope.RunFolder)
			}
			if exists(f.worktree) {
				t.Fatalf("the exact edit's worktree, with the app's files, is still there: %s", f.worktree)
			}
			if list := run(t, f.scope.Repository, "worktree", "list", "--porcelain"); strings.Contains(list, testRun) {
				t.Fatalf("Git still lists the worktree:\n%s", list)
			}
			if !f.blobGoneAfterGC(t) {
				t.Fatal("the app's blob survives gc --prune=now")
			}
			if !exists(filepath.Join(f.scope.Repository, ".ralphex", "worktrees", "abcp", otherRun, "other.md")) {
				t.Fatal("another run's worktree was removed")
			}
		})
	}
}

// Git does not prune a locked worktree whose folder is gone, and its index keeps the run's objects through gc: the
// forget unlocks and prunes it.
func TestForgetPrunesALockedWorktreeWhoseFolderIsGone(t *testing.T) {
	f := newForgetFixture(t, false)
	s := f.sweeper()
	s.mu.Lock()
	sealed, err := s.sealForget(context.Background(), testRun, "app-deleted", forgetRequest)
	s.mu.Unlock()
	if err != nil || sealed.Worktree != f.worktree {
		t.Fatalf("seal = %+v, %v", sealed, err)
	}
	run(t, f.scope.Repository, "worktree", "lock", "--reason", "kept", f.worktree)
	if err := os.RemoveAll(f.worktree); err != nil {
		t.Fatal(err)
	}
	if result, err := f.sweeper().Forget(context.Background(), testRun, "app-deleted", forgetRequest); err != nil || result.Status != Forgotten {
		t.Fatalf("forget = %+v, %v", result, err)
	}
	if list := run(t, f.scope.Repository, "worktree", "list", "--porcelain"); strings.Contains(list, testRun) {
		t.Fatalf("Git still lists the locked worktree:\n%s", list)
	}
	if !f.blobGoneAfterGC(t) {
		t.Fatal("the app's blob survives gc --prune=now")
	}
}

// A worktree of the run's branch Git still lists after the removal (here made at another place after the record was
// sealed) stops the forget: it is not FORGOTTEN, and its branch is not deleted under it.
func TestForgetFailsWhileGitStillListsAWorktreeOfTheRun(t *testing.T) {
	f := newForgetFixture(t, false)
	s := f.sweeper()
	s.mu.Lock()
	_, err := s.sealForget(context.Background(), testRun, "app-deleted", forgetRequest)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	run(t, f.scope.Repository, "worktree", "remove", "--force", f.worktree)
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	run(t, f.scope.Repository, "worktree", "add", "--quiet", elsewhere, f.scope.Branch)
	if _, err := f.sweeper().Forget(context.Background(), testRun, "app-deleted", forgetRequest); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("forget with the run's branch checked out elsewhere = %v", err)
	}
	run(t, f.scope.Repository, "rev-parse", "--verify", "refs/heads/"+f.scope.Branch)
	if len(f.ledgerEvents(t, RunForgottenEvent)) != 0 {
		t.Fatal("RUN_FORGOTTEN was appended to an unfinished forget")
	}
}

// Once sealed, the removal runs apart from the request: a request that has gone away (its context cancelled) does not
// interrupt it.
func TestASealedForgetIsFinishedWhenTheRequestHasGone(t *testing.T) {
	f := newForgetFixture(t, false)
	s := f.sweeper()
	s.mu.Lock()
	_, err := s.sealForget(context.Background(), testRun, "app-deleted", forgetRequest)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := f.sweeper().Forget(gone, testRun, "app-deleted", forgetRequest)
	if err != nil || result.Status != Forgotten {
		t.Fatalf("forget = %+v, %v", result, err)
	}
	if exists(f.worktree) || len(f.ledgerEvents(t, RunForgottenEvent)) != 1 {
		t.Fatal("the forget was not finished")
	}
	if out := run(t, f.scope.Repository, "for-each-ref", "--format=%(refname)", "refs/heads/abcp/"+testRun); out != "" {
		t.Fatalf("the run's branch is still there: %s", out)
	}
}

// An interrupted Git is asked to stop with SIGTERM, so it removes its lock files, and not killed outright.
func TestAnInterruptedGitIsAskedToStop(t *testing.T) {
	bin := t.TempDir()
	mark := filepath.Join(bin, "stopped")
	script := "#!/bin/sh\ntrap 'kill $pid; echo stopped > " + mark + "; exit 143' TERM\nsleep 30 &\npid=$!\nwait $pid\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := git(ctx, t.TempDir(), "update-ref", "-d", "refs/heads/x"); err == nil {
		t.Fatal("an interrupted git succeeded")
	}
	if took := time.Since(started); took > gitWaitDelay {
		t.Fatalf("git took %s to stop", took)
	}
	if data, err := os.ReadFile(mark); err != nil || string(data) != "stopped\n" {
		t.Fatalf("git was not sent SIGTERM: %q, %v", data, err)
	}
}

// The same request id with another reason is a conflicting replay, refused without changing anything.
func TestForgetRefusesTheSameRequestWithAnotherReason(t *testing.T) {
	f := newForgetFixture(t, false)
	if _, err := f.sweeper().Forget(context.Background(), testRun, "app-deleted", forgetRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sweeper().Forget(context.Background(), testRun, "account-deleted", forgetRequest); !errors.Is(err, ErrForgetConflict) {
		t.Fatalf("a conflicting replay = %v", err)
	}
	api := API{Sweeper: f.sweeper()}
	if _, err := api.ForgetRun(context.Background(), serviceapi.Principal{}, testRun, serviceapi.RunForgetRequestV1{SchemaVersion: 1, Reason: "account-deleted", RequestID: forgetRequest}); !errors.Is(err, serviceapi.ErrRequestIDConflict) {
		t.Fatalf("the API answers a conflicting replay with %v", err)
	}
	if replay, err := api.ForgetRun(context.Background(), serviceapi.Principal{}, testRun, serviceapi.RunForgetRequestV1{SchemaVersion: 1, Reason: "app-deleted", RequestID: forgetRequest}); err != nil || replay.Status != Forgotten {
		t.Fatalf("the replay = %+v, %v", replay, err)
	}
}

// A forget record that cannot be read is an error to a release (500), never 410 for a run that may not be forgotten;
// the sweep leaves the run alone.
func TestAnUnreadableForgetRecordIsAnErrorToARelease(t *testing.T) {
	f := newForgetFixture(t, false)
	if err := os.Mkdir(filepath.Join(f.root, "forgotten", testRun+".json"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := f.sweeper()
	if _, err := s.Release(context.Background(), testRun, ReasonTaskClosed); err == nil || errors.Is(err, ErrRunForgotten) {
		t.Fatalf("release with an unreadable record = %v", err)
	}
	if _, err := (API{Sweeper: s}).ReleaseRunWorktree(context.Background(), serviceapi.Principal{}, testRun, serviceapi.WorktreeReleaseRequestV1{SchemaVersion: 1, Reason: ReasonTaskClosed}); !errors.Is(err, serviceapi.ErrInternalDurableSubstrate) {
		t.Fatalf("the API answers %v", err)
	}
	f.finishedAt = now.Add(-30 * 24 * time.Hour)
	if evicted, err := s.Sweep(context.Background()); err != nil || len(evicted) != 0 || !exists(f.worktree) {
		t.Fatalf("sweep with an unreadable record = %+v, %v", evicted, err)
	}
}

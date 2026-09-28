//go:build linux

package eviction

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/activity"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

const testRun = "admission-0000000000000000000000000000000000000000000000000000000000000001"

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.test"}, args...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out.String())
	}
	return strings.TrimSpace(out.String())
}

type fixture struct {
	scope      activity.Scope
	worktree   string
	checkpoint string
	store      *Store
	state      string
	finishedAt time.Time
	live       bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "init", "--quiet", "--initial-branch=main")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o600)
	run(t, repo, "add", "README.md")
	run(t, repo, "commit", "--quiet", "-m", "base")
	base := run(t, repo, "rev-parse", "HEAD")
	worktree := filepath.Join(repo, ".ralphex", "worktrees", "abcp", testRun)
	run(t, repo, "worktree", "add", "--quiet", "-b", "abcp/"+testRun, worktree)
	os.WriteFile(filepath.Join(worktree, "feature.md"), []byte("feature\n"), 0o600)
	run(t, worktree, "add", "feature.md")
	run(t, worktree, "commit", "--quiet", "-m", "feature")
	store, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		scope:    activity.Scope{RunID: testRun, Repository: repo, Branch: "abcp/" + testRun, Base: base, AuthorityDigest: strings.Repeat("a", 64), Retain: true},
		worktree: worktree, checkpoint: run(t, worktree, "rev-parse", "HEAD"), store: store,
		state: string(domain.StateBranchAccepted), finishedAt: now.Add(-25 * time.Hour),
	}
}

func (f *fixture) ListRuns(after string, _ int) ([]runtimecatalog.RunRegistrationV1, bool, error) {
	if after != "" {
		return nil, false, nil
	}
	return []runtimecatalog.RunRegistrationV1{{RunID: testRun}}, false, nil
}
func (f *fixture) ResolveBinding(context.Context, string) (activity.Scope, error) {
	return f.scope, nil
}
func (f *fixture) Snapshot(context.Context, string) (readmodel.Snapshot, error) {
	s := readmodel.Snapshot{Events: []ledger.Event{{StateTo: domain.State(f.state), Timestamp: f.finishedAt}}}
	s.Projection.CurrentState = f.state
	return s, nil
}
func (f *fixture) Engagement(string) (bool, time.Time) { return false, time.Time{} }
func (f *fixture) Checkpoints(context.Context, string) ([]string, error) {
	return []string{f.checkpoint}, nil
}
func (f *fixture) Live(string) bool { return f.live }
func (f *fixture) sweeper() *Sweeper {
	return &Sweeper{Policy: DefaultPolicy(), Store: f.store, Runs: f, Binder: f, States: f, Activity: f, Previews: f,
		Now: func() time.Time { return now }, Started: now.Add(-48 * time.Hour)}
}

func TestSweepEvictsIdleWorktreeAfterPinningCheckpoints(t *testing.T) {
	f := newFixture(t)
	evicted, err := f.sweeper().Sweep(context.Background())
	if err != nil || len(evicted) != 1 || evicted[0].Reason != ReasonIdle {
		t.Fatalf("sweep = %+v, %v", evicted, err)
	}
	if _, err := os.Stat(f.worktree); !os.IsNotExist(err) {
		t.Fatal("evicted worktree still exists", err)
	}
	ref := CheckpointRef(testRun, f.checkpoint)
	if got := run(t, f.scope.Repository, "rev-parse", ref); got != f.checkpoint {
		t.Fatalf("pinned ref = %s, want %s", got, f.checkpoint)
	}
	if got := run(t, f.scope.Repository, "rev-parse", "refs/heads/"+f.scope.Branch); got != f.checkpoint {
		t.Fatal("eviction must keep the run's branch")
	}
	record, ok, err := f.store.Read(testRun)
	if err != nil || !ok || !record.Pinned(f.checkpoint) || record.Base != f.scope.Base || record.Worktree != f.worktree || record.WorktreeBytes <= 0 {
		t.Fatalf("record = %+v, %v, %v", record, ok, err)
	}
	// A second sweep has nothing left to do.
	if again, err := f.sweeper().Sweep(context.Background()); err != nil || len(again) != 0 {
		t.Fatalf("second sweep = %+v, %v", again, err)
	}
}

func TestSweepNeverEvictsProtectedUnfinishedOrUnretainedWorktrees(t *testing.T) {
	for name, mutate := range map[string]func(*fixture){
		"live preview":     func(f *fixture) { f.live = true },
		"unfinished run":   func(f *fixture) { f.state = string(domain.StateImplementing) },
		"paused for human": func(f *fixture) { f.state = string(domain.StateHumanDecisionRequired) },
		"not retained":     func(f *fixture) { f.scope.Retain = false },
		"recently finished": func(f *fixture) {
			f.finishedAt = now.Add(-time.Hour)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			mutate(f)
			sweeper := f.sweeper()
			sweeper.Started = now.Add(-time.Hour)
			if evicted, err := sweeper.Sweep(context.Background()); err != nil || len(evicted) != 0 {
				t.Fatalf("sweep evicted %+v, %v", evicted, err)
			}
			if _, err := os.Stat(f.worktree); err != nil {
				t.Fatal("protected worktree was removed", err)
			}
		})
	}
}

func TestSweepCompletesAnInterruptedEviction(t *testing.T) {
	f := newFixture(t)
	record := RecordV1{Kind: "WorktreeEvictionV1", SchemaVersion: 1, RunID: testRun, AuthorityDigest: f.scope.AuthorityDigest,
		Repository: f.scope.Repository, Branch: f.scope.Branch, Base: f.scope.Base, Worktree: f.worktree, Reason: ReasonMaxAge,
		EvictedAt: now.Format(time.RFC3339Nano), Checkpoints: []PinnedCheckpoint{}}
	if err := f.store.Write(record); err != nil {
		t.Fatal(err)
	}
	f.finishedAt = now // recent: only the sealed record can cause the removal
	if evicted, err := f.sweeper().Sweep(context.Background()); err != nil || len(evicted) != 0 {
		t.Fatalf("resume sweep = %+v, %v", evicted, err)
	}
	if _, err := os.Stat(f.worktree); !os.IsNotExist(err) {
		t.Fatal("sealed eviction was not completed", err)
	}
}

func TestSweepRefusesAConflictingPinnedRef(t *testing.T) {
	f := newFixture(t)
	run(t, f.scope.Repository, "update-ref", CheckpointRef(testRun, f.checkpoint), f.scope.Base)
	if evicted, err := f.sweeper().Sweep(context.Background()); err != nil || len(evicted) != 0 {
		t.Fatalf("sweep with a conflicting ref = %+v, %v", evicted, err)
	}
	if _, ok, _ := f.store.Read(testRun); ok {
		t.Fatal("a record was sealed although pinning failed")
	}
	if _, err := os.Stat(f.worktree); err != nil {
		t.Fatal("worktree removed although pinning failed", err)
	}
}

func TestStoreRejectsInvalidOrForeignRecords(t *testing.T) {
	f := newFixture(t)
	good := RecordV1{Kind: "WorktreeEvictionV1", SchemaVersion: 1, RunID: testRun, AuthorityDigest: f.scope.AuthorityDigest,
		Repository: f.scope.Repository, Branch: f.scope.Branch, Base: f.scope.Base, Worktree: f.worktree, Reason: ReasonIdle,
		EvictedAt: now.Format(time.RFC3339Nano), Checkpoints: []PinnedCheckpoint{{SHA: f.checkpoint, Ref: CheckpointRef(testRun, f.checkpoint)}}}
	for name, bad := range map[string]func(r *RecordV1){
		"wrong branch":   func(r *RecordV1) { r.Branch = "main" },
		"unknown reason": func(r *RecordV1) { r.Reason = "manual" },
		"foreign ref":    func(r *RecordV1) { r.Checkpoints[0].Ref = "refs/heads/main" },
		"relative worktree": func(r *RecordV1) {
			r.Worktree = "wt"
		},
	} {
		r := good
		r.Checkpoints = append([]PinnedCheckpoint{}, good.Checkpoints...)
		bad(&r)
		if err := f.store.Write(r); err == nil {
			t.Fatalf("%s: invalid record was written", name)
		}
	}
	if err := f.store.Write(good); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(f.scope.Repository), "evictions", testRun+".json")
	os.Chmod(path, 0o644)
	if _, _, err := f.store.Read(testRun); err == nil {
		t.Fatal("a group/world-readable record was trusted")
	}
}

//go:build linux

package activity

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

// R6's resume (Repo B docs/plans/build-recovery.md §6.2): a run interrupted while it implements stops at
// RECOVERY_REQUIRED; its resume removes the worktree and Ralphex adds it again at the same path once the resumed attempt
// implements. As after RETRYING, the worktree is not there yet until then, however long the resume waits (abcp serve
// looks every 5 minutes): no marker, so the run stays eligible for preview. Once it implements again, a worktree still
// missing gets the usual bounded window.
func TestAResumedRunsMissingWorktreeIsPendingUntilItImplementsAgain(t *testing.T) {
	implementing := ledger.Event{EventID: "implementing", StateTo: domain.StateImplementing, Timestamp: testTime}
	interrupted := ledger.Event{EventID: "interrupted", StateFrom: domain.StateImplementing, StateTo: domain.StateRecoveryRequired, Timestamp: testTime.Add(time.Minute)}
	starting := ledger.Event{EventID: "starting", StateFrom: domain.StateRecoveryRequired, StateTo: domain.StateExecutionStarting, Timestamp: testTime.Add(6 * time.Minute)}
	again := ledger.Event{EventID: "implementing-again", StateFrom: domain.StateExecutionStarting, StateTo: domain.StateImplementing, Timestamp: testTime.Add(7 * time.Minute)}
	for _, scenario := range []struct {
		name        string
		events      []ledger.Event
		looks       int
		wantPending bool
	}{
		{"waiting for its resume, for longer than the window", []ledger.Event{implementing, interrupted}, 3, true},
		{"starting again, for longer than the window", []ledger.Event{implementing, interrupted, starting}, 3, true},
		{"implementing again, just now", []ledger.Event{implementing, interrupted, starting, again}, 1, true},
		{"implementing again, past the window", []ledger.Event{implementing, interrupted, starting, again}, 2, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newBindingFixture(t)
			command(t, f.repo, "worktree", "remove", "--force", f.worktree)
			registration := jsonDigest(f.catalog.runs[f.run])
			now := testTime.Add(time.Hour)
			s := &Service{store: newStore(t), resolver: Resolver{f.root, f.catalog}, snapshots: &fakeSnapshots{events: scenario.events}, ctx: context.Background(), now: func() time.Time { return now }}
			if err := s.store.saveProof(f.run, registration, providerProof{Session: "sha256-session", Generation: "generation", FileIdentity: "file", Size: 1, PrefixDigest: "prefix"}); err != nil {
				t.Fatal(err)
			}
			for look := 0; look < scenario.looks; look++ {
				if scope, err := s.refresh(context.Background(), f.run, registration); err != nil || scope.RunID != "" {
					t.Fatal("unresolved binding produced a scope or error", scope, err)
				}
				now = now.Add(worktreeTransitionWindow)
			}
			if pending := len(unknownEvents(t, s, f.run, registration)) == 0; pending != scenario.wantPending {
				t.Fatalf("pending = %v, want %v", pending, scenario.wantPending)
			}
		})
	}
}

// The whole gap as a resume makes it, what preview needs of the run: the interrupted attempt's commit is a checkpoint
// (its uncommitted work is lost with its worktree), the worktree goes while the run is RECOVERY_REQUIRED, the resumed
// attempt adds it again at the same path from the branch and commits, and that commit is a checkpoint too, with no
// marker anywhere.
func TestAResumedRunsCommitIsACheckpointAfterItsWorktreeComesBack(t *testing.T) {
	f := newBindingFixture(t)
	registration := jsonDigest(f.catalog.runs[f.run])
	snapshots := &fakeSnapshots{events: []ledger.Event{{EventID: "implementing", StateTo: domain.StateImplementing, Timestamp: testTime}}}
	now := testTime
	s := &Service{store: newStore(t), resolver: Resolver{f.root, f.catalog}, snapshots: snapshots, ctx: context.Background(), now: func() time.Time { return now }}
	commit := func(name string) string {
		if err := os.WriteFile(filepath.Join(f.worktree, name), []byte(name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		command(t, f.worktree, "add", name)
		command(t, f.worktree, "commit", "-qm", name)
		return command(t, f.worktree, "rev-parse", "HEAD")
	}
	refresh := func() Scope {
		scope, err := s.refresh(context.Background(), f.run, registration)
		if err != nil {
			t.Fatal(err)
		}
		return scope
	}
	first := commit("attempt-1.txt")
	if scope := refresh(); scope.RunID == "" {
		t.Fatal("the interrupted attempt's binding did not resolve")
	}
	if err := os.WriteFile(filepath.Join(f.worktree, "uncommitted.txt"), []byte("lost\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.store.saveProof(f.run, registration, providerProof{Session: "sha256-session", Generation: "generation", FileIdentity: "file", Size: 1, PrefixDigest: "prefix"}); err != nil {
		t.Fatal(err)
	}
	snapshots.mu.Lock()
	snapshots.events = append(snapshots.events, ledger.Event{EventID: "interrupted", StateFrom: domain.StateImplementing, StateTo: domain.StateRecoveryRequired, Timestamp: testTime.Add(time.Minute)})
	snapshots.mu.Unlock()
	command(t, f.repo, "worktree", "remove", "--force", f.worktree)
	for i := 0; i < 3; i++ {
		now = now.Add(worktreeTransitionWindow)
		if scope := refresh(); scope.RunID != "" {
			t.Fatal("a removed worktree resolved")
		}
	}
	snapshots.mu.Lock()
	snapshots.events = append(snapshots.events,
		ledger.Event{EventID: "starting", StateFrom: domain.StateRecoveryRequired, StateTo: domain.StateExecutionStarting, Timestamp: testTime.Add(6 * time.Minute)},
		ledger.Event{EventID: "implementing-again", StateFrom: domain.StateExecutionStarting, StateTo: domain.StateImplementing, Timestamp: testTime.Add(7 * time.Minute)})
	snapshots.mu.Unlock()
	command(t, f.repo, "worktree", "add", "-q", f.worktree, "abcp/"+f.run)
	second := commit("attempt-2.txt")
	if scope := refresh(); scope.RunID == "" || scope.Worktree != f.scope.Worktree {
		t.Fatalf("the resumed attempt's binding = %+v", scope)
	}
	if unknown := unknownEvents(t, s, f.run, registration); len(unknown) != 0 {
		t.Fatalf("markers: %+v", unknown)
	}
	checkpoints, err := s.recordedCheckpoints(f.run, registration)
	if err != nil || len(checkpoints) != 2 || checkpoints[0] != first || checkpoints[1] != second {
		t.Fatalf("checkpoints = %v, %v; want %s then %s", checkpoints, err, first, second)
	}
}

// A builder killed outright (SIGKILL, the host reset) writes no footer; Ralphex keeps such a progress file and writes a
// "restarted at" section on the resumed start, as it does after a "Failed:" footer (a SIGTERM's). The collector keeps
// one provider session across it with no marker.
func TestAResumedRunsProgressWithoutAFooterIsTheSameSession(t *testing.T) {
	f := newBindingFixture(t)
	selected := providerSession(t, f)
	path := filepath.Join(f.repo, ".ralphex", "progress", "progress-plan.txt")
	s := &Service{store: newStore(t), resolver: Resolver{f.root, f.catalog}, ctx: context.Background(), now: func() time.Time { return testTime }}
	registration := jsonDigest(f.catalog.runs[f.run])
	provider := &ralphexSidecar{history: []string{"validation started", "implementing"}}
	server := provider.server(t, &selected, nil)
	defer server.Close()
	sc := testSidecar(server)
	last := uint64(math.MaxUint64)
	if err := s.collectBatch(f.scope, registration, sc, &last); err != nil || last != 2 {
		t.Fatal("the interrupted attempt's detail", err, last)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString("\n--- restarted at 2026-11-07 10:31:00 ---\n\nthe resumed attempt\n")
	file.Close()
	provider.mu.Lock()
	provider.history = append(provider.history, "restarted", "the resumed attempt commits")
	provider.mu.Unlock()
	if err := s.collectBatch(f.scope, registration, sc, &last); err != nil || last != 4 {
		t.Fatal("the resumed attempt's detail", err, last)
	}
	if unknown := unknownEvents(t, s, f.run, registration); len(unknown) != 0 {
		t.Fatalf("markers: %+v", unknown)
	}
}

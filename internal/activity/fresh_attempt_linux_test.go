//go:build linux

package activity

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

// A fresh attempt (Repo B docs/plans/build-recovery.md §5) removes the first attempt's worktree while the run is
// RETRYING, and Ralphex adds it again at the same path once the attempt implements. Until then the worktree is not
// there yet, as before a first attempt, however long the attempt waits for its run slot: no marker, so the run stays
// eligible for preview. Once it implements again, a worktree still missing gets the usual bounded window.
func TestAFreshAttemptsMissingWorktreeIsPendingUntilItImplementsAgain(t *testing.T) {
	implementing := ledger.Event{EventID: "implementing", StateTo: domain.StateImplementing, Timestamp: testTime}
	retrying := ledger.Event{EventID: "retrying", StateFrom: domain.StateImplementing, StateTo: domain.StateRetrying, Timestamp: testTime.Add(time.Minute)}
	starting := ledger.Event{EventID: "starting", StateFrom: domain.StateRetrying, StateTo: domain.StateExecutionStarting, Timestamp: testTime.Add(2 * time.Minute)}
	again := ledger.Event{EventID: "implementing-again", StateFrom: domain.StateExecutionStarting, StateTo: domain.StateImplementing, Timestamp: testTime.Add(3 * time.Minute)}
	for _, scenario := range []struct {
		name        string
		events      []ledger.Event
		looks       int
		wantPending bool
	}{
		{"retrying, for longer than the window", []ledger.Event{implementing, retrying}, 3, true},
		{"starting again, for longer than the window", []ledger.Event{implementing, retrying, starting}, 3, true},
		{"implementing again, just now", []ledger.Event{implementing, retrying, starting, again}, 1, true},
		{"implementing again, past the window", []ledger.Event{implementing, retrying, starting, again}, 2, false},
		{"a first attempt's worktree lost past the window", []ledger.Event{implementing}, 2, false},
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

// The whole gap as a fresh attempt makes it: the first attempt's commit is a checkpoint, its worktree goes while the run
// is RETRYING (for longer than the window), the fresh attempt adds it again at the same path from the branch and
// commits, and that commit is a checkpoint too, with no marker anywhere: what preview needs of the run.
func TestAFreshAttemptsCommitIsACheckpointAfterItsWorktreeComesBack(t *testing.T) {
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
		t.Fatal("the first attempt's binding did not resolve")
	}
	if err := s.store.saveProof(f.run, registration, providerProof{Session: "sha256-session", Generation: "generation", FileIdentity: "file", Size: 1, PrefixDigest: "prefix"}); err != nil {
		t.Fatal(err)
	}
	snapshots.mu.Lock()
	snapshots.events = append(snapshots.events, ledger.Event{EventID: "retrying", StateFrom: domain.StateImplementing, StateTo: domain.StateRetrying, Timestamp: testTime.Add(time.Minute)})
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
		ledger.Event{EventID: "starting", StateFrom: domain.StateRetrying, StateTo: domain.StateExecutionStarting, Timestamp: testTime.Add(2 * time.Minute)},
		ledger.Event{EventID: "implementing-again", StateFrom: domain.StateExecutionStarting, StateTo: domain.StateImplementing, Timestamp: testTime.Add(3 * time.Minute)})
	snapshots.mu.Unlock()
	command(t, f.repo, "worktree", "add", "-q", f.worktree, "abcp/"+f.run)
	second := commit("attempt-2.txt")
	if scope := refresh(); scope.RunID == "" || scope.Worktree != f.scope.Worktree {
		t.Fatalf("the fresh attempt's binding = %+v", scope)
	}
	if unknown := unknownEvents(t, s, f.run, registration); len(unknown) != 0 {
		t.Fatalf("markers: %+v", unknown)
	}
	checkpoints, err := s.recordedCheckpoints(f.run, registration)
	if err != nil || len(checkpoints) != 2 || checkpoints[0] != first || checkpoints[1] != second {
		t.Fatalf("checkpoints = %v, %v; want %s then %s", checkpoints, err, first, second)
	}
}

// Ralphex appends a fresh attempt's start to the same progress file: the first attempt ended with a "Failed:" footer,
// which Ralphex never archives (its progressCompletedAt), so it writes a "restarted at" section and goes on. The
// collector keeps one provider session across it with no marker. A file truncated instead (what a "Completed:" footer
// gets) is refused, which is why the fresh attempt runs where Ralphex appends.
func TestAFreshAttemptsProgressIsTheSameSession(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		truncate  bool
		wantClean bool
	}{{"appended after a Failed footer", false, true}, {"truncated", true, false}} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newBindingFixture(t)
			selected := providerSession(t, f)
			path := filepath.Join(f.repo, ".ralphex", "progress", "progress-plan.txt")
			s := &Service{store: newStore(t), resolver: Resolver{f.root, f.catalog}, ctx: context.Background(), now: func() time.Time { return testTime }}
			registration := jsonDigest(f.catalog.runs[f.run])
			provider := &ralphexSidecar{history: []string{"validation failed", "task failed, retrying...", "task execution failed after retry"}}
			server := provider.server(t, &selected, nil)
			defer server.Close()
			sc := testSidecar(server)
			last := uint64(math.MaxUint64)
			if err := s.collectBatch(f.scope, registration, sc, &last); err != nil || last != 3 {
				t.Fatal("the first attempt's detail", err, last)
			}
			fresh := "\n" + "------------------------------------------------------------\nFailed: 2026-11-07 10:30:00\n\n\n--- restarted at 2026-11-07 10:31:00 ---\n\nthe fresh attempt\n"
			if scenario.truncate {
				if err := os.WriteFile(path, []byte("# Ralphex Progress Log\nthe fresh attempt\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Fatal(err)
				}
				file.WriteString(fresh)
				file.Close()
			}
			provider.mu.Lock()
			provider.history = append(provider.history, "restarted", "the fresh attempt commits")
			provider.mu.Unlock()
			err := s.collectBatch(f.scope, registration, sc, &last)
			if scenario.wantClean {
				if err != nil || last != 5 {
					t.Fatal("the fresh attempt's detail", err, last)
				}
				if unknown := unknownEvents(t, s, f.run, registration); len(unknown) != 0 {
					t.Fatalf("markers: %+v", unknown)
				}
				return
			}
			if !errors.Is(err, ErrIntegrity) {
				t.Fatalf("a truncated progress file was taken: %v", err)
			}
		})
	}
}

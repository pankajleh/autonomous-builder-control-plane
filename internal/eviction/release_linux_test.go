//go:build linux

package eviction

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
)

func TestReleaseEvictsAFinishedRunRegardlessOfPolicyAndProtection(t *testing.T) {
	f := newFixture(t)
	// Just finished, streamed and previewed: the policy would keep it.
	f.finishedAt, f.streaming, f.live = now, true, true
	result, err := f.sweeper().Release(context.Background(), testRun, ReasonTaskClosed)
	if err != nil || result.Status != Released || result.Record.Reason != ReasonTaskClosed || !result.Record.Pinned(f.checkpoint) {
		t.Fatalf("release = %+v, %v", result, err)
	}
	if _, err := os.Stat(f.worktree); !os.IsNotExist(err) {
		t.Fatal("released worktree still exists", err)
	}
	if got := run(t, f.scope.Repository, "rev-parse", CheckpointRef(testRun, f.checkpoint)); got != f.checkpoint {
		t.Fatal("checkpoint was not pinned before release")
	}
	again, err := f.sweeper().Release(context.Background(), testRun, ReasonTaskClosed)
	if err != nil || again.Status != AlreadyReleased || again.Record.EvictedAt != result.Record.EvictedAt {
		t.Fatalf("repeated release = %+v, %v", again, err)
	}
}

func TestReleaseRefusesUnfinishedRunsAndPolicyReasons(t *testing.T) {
	for _, state := range []domain.State{domain.StateImplementing, domain.StateHumanDecisionRequired} {
		f := newFixture(t)
		f.state = string(state)
		if _, err := f.sweeper().Release(context.Background(), testRun, ReasonTaskClosed); !errors.Is(err, ErrRunNotFinished) {
			t.Fatalf("%s: release = %v", state, err)
		}
		if _, ok, _ := f.store.Read(testRun); ok {
			t.Fatalf("%s: a record was sealed for an unfinished run", state)
		}
		if _, err := os.Stat(f.worktree); err != nil {
			t.Fatalf("%s: an unfinished run's worktree was removed: %v", state, err)
		}
	}
	// Before its worktree exists, or when it retains none, a running build is still refused.
	for name, mutate := range map[string]func(*testing.T, *fixture){
		"no worktree yet": func(t *testing.T, f *fixture) {
			run(t, f.scope.Repository, "worktree", "remove", "--force", f.worktree)
		},
		"not retained": func(_ *testing.T, f *fixture) { f.scope.Retain = false },
	} {
		f := newFixture(t)
		f.state = string(domain.StateImplementing)
		mutate(t, f)
		if _, err := f.sweeper().Release(context.Background(), testRun, ReasonTaskClosed); !errors.Is(err, ErrRunNotFinished) {
			t.Fatalf("%s: running build release = %v", name, err)
		}
	}
	f := newFixture(t)
	for _, reason := range []string{ReasonIdle, ReasonQuota, "manual", ""} {
		if _, err := f.sweeper().Release(context.Background(), testRun, reason); !errors.Is(err, ErrInvalidReason) {
			t.Fatalf("reason %q accepted: %v", reason, err)
		}
	}
	f.scope.Retain = false
	if result, err := f.sweeper().Release(context.Background(), testRun, ReasonTaskClosed); err != nil || result.Status != NotRetained {
		t.Fatalf("not retained = %+v, %v", result, err)
	}
}

func TestReleaseRepositoryReleasesOnlyThatRepositoriesFinishedRuns(t *testing.T) {
	f := newFixture(t)
	if result, err := f.sweeper().ReleaseRepository(context.Background(), "other/repo", ReasonTenantDeleted); err != nil || result != (RepositoryReleaseResult{}) {
		t.Fatalf("other repository = %+v, %v", result, err)
	}
	f.state = string(domain.StateImplementing)
	if result, err := f.sweeper().ReleaseRepository(context.Background(), "owner/repo", ReasonTenantDeleted); err != nil || result != (RepositoryReleaseResult{Unfinished: 1}) {
		t.Fatalf("unfinished = %+v, %v", result, err)
	}
	f.state, f.finishedAt = string(domain.StateBranchAccepted), now.Add(-time.Minute)
	if result, err := f.sweeper().ReleaseRepository(context.Background(), "owner/repo", ReasonTenantDeleted); err != nil || result != (RepositoryReleaseResult{Released: 1}) {
		t.Fatalf("finished = %+v, %v", result, err)
	}
	record, ok, err := f.store.Read(testRun)
	if err != nil || !ok || record.Reason != ReasonTenantDeleted {
		t.Fatalf("record = %+v, %v, %v", record, ok, err)
	}
	if result, err := f.sweeper().ReleaseRepository(context.Background(), "owner/repo", ReasonTenantDeleted); err != nil || result != (RepositoryReleaseResult{AlreadyReleased: 1}) {
		t.Fatalf("repeated = %+v, %v", result, err)
	}
}

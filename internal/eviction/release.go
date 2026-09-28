package eviction

import (
	"context"
	"errors"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/activity"
)

var (
	// ErrRunNotFinished refuses to release the worktree of a run that can still use it.
	ErrRunNotFinished = errors.New("run is not finished")
	// ErrInvalidReason rejects a release reason the product may not request.
	ErrInvalidReason = errors.New("invalid worktree release reason")
)

// Release outcomes.
const (
	Released        = "RELEASED"         // evicted now
	AlreadyReleased = "ALREADY_RELEASED" // an earlier eviction sealed it
	NotRetained     = "NOT_RETAINED"     // the manifest never retained a worktree
	NoWorktree      = "NO_WORKTREE"      // retained, but no worktree exists
)

// ReleaseResult is one run's release outcome. Record is set for Released and
// AlreadyReleased.
type ReleaseResult struct {
	Status string
	Record RecordV1
}

// RepositoryReleaseResult counts the outcome of releasing every retained
// worktree of one governed repository.
type RepositoryReleaseResult struct {
	Released, AlreadyReleased, Unfinished, Failed int
}

func productReason(reason string) bool {
	return reason == ReasonTaskClosed || reason == ReasonTenantDeleted
}

// Release evicts one finished run's retained worktree now, because the product
// closed its task or deleted its tenant. The idle, age and quota rules and the
// stream and preview protection do not apply: preview sources are separate
// checkouts, and pinned checkpoints keep a preview of the run available. A run
// that has not finished is refused. Releasing an evicted run is idempotent.
func (s *Sweeper) Release(ctx context.Context, run, reason string) (ReleaseResult, error) {
	if !productReason(reason) {
		return ReleaseResult{}, ErrInvalidReason
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.release(ctx, run, reason)
}

// ReleaseRepository releases every finished retained worktree whose run belongs
// to the governed repository identity (tenant deletion). Unfinished runs are
// counted and kept; runs whose binding cannot be verified are skipped.
func (s *Sweeper) ReleaseRepository(ctx context.Context, identity, reason string) (RepositoryReleaseResult, error) {
	var result RepositoryReleaseResult
	if !productReason(reason) || identity == "" || len(identity) > 512 {
		return result, ErrInvalidReason
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	after := ""
	for count := 0; count < maxRuns; {
		runs, more, err := s.Runs.ListRuns(after, 100)
		if err != nil {
			return result, err
		}
		for _, reg := range runs {
			count++
			after = reg.RunID
			scope, err := s.Binder.ResolveBinding(ctx, reg.RunID)
			if err != nil || scope.RepositoryIdentity != identity {
				continue
			}
			outcome, err := s.release(ctx, reg.RunID, reason)
			switch {
			case errors.Is(err, ErrRunNotFinished):
				result.Unfinished++
			case err != nil:
				result.Failed++
				s.logf("abcp worktree release failed run=%s reason=%s class=%s", reg.RunID, reason, class(err))
			case outcome.Status == Released:
				result.Released++
			case outcome.Status == AlreadyReleased:
				result.AlreadyReleased++
			}
		}
		if !more || len(runs) == 0 {
			break
		}
	}
	return result, nil
}

func (s *Sweeper) release(ctx context.Context, run, reason string) (ReleaseResult, error) {
	scope, err := s.Binder.ResolveBinding(ctx, run)
	if err != nil {
		return ReleaseResult{}, err
	}
	// A run that can still use its worktree is refused first, whether or not
	// the worktree exists yet or is retained: a product must not treat a
	// running build as released.
	terminal, terminalAt, err := s.finished(ctx, run)
	if err != nil {
		return ReleaseResult{}, err
	}
	if !terminal {
		return ReleaseResult{}, ErrRunNotFinished
	}
	if !scope.Retain {
		return ReleaseResult{Status: NotRetained}, nil
	}
	path, err := activity.WorktreePath(ctx, scope)
	if err != nil {
		return ReleaseResult{}, err
	}
	if record, ok, err := s.Store.Read(run); err != nil {
		return ReleaseResult{}, err
	} else if ok {
		// Complete an eviction sealed before an interruption.
		if path != "" {
			if err := removeWorktree(ctx, scope, path); err != nil {
				return ReleaseResult{}, err
			}
		}
		return ReleaseResult{Status: AlreadyReleased, Record: record}, nil
	}
	if path == "" {
		return ReleaseResult{Status: NoWorktree}, nil
	}
	if !inside(scope.Repository, path) {
		return ReleaseResult{}, ErrIntegrity
	}
	bytes, err := size(path)
	if err != nil {
		return ReleaseResult{}, err
	}
	c := candidate{scope: scope, Candidate: Candidate{Run: run, Repository: scope.Repository, Worktree: path, Terminal: true, TerminalAt: terminalAt, Bytes: bytes}}
	record, err := s.evict(ctx, c, reason, s.Now())
	if err != nil {
		return ReleaseResult{}, err
	}
	s.logf("abcp worktree evicted at=%s run=%s reason=%s bytes=%d checkpoints=%d", record.EvictedAt, record.RunID, record.Reason, record.WorktreeBytes, len(record.Checkpoints))
	return ReleaseResult{Status: Released, Record: record}, nil
}

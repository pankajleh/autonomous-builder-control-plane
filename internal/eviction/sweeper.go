package eviction

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/activity"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

type Runs interface {
	ListRuns(after string, limit int) ([]runtimecatalog.RunRegistrationV1, bool, error)
}
type Binder interface {
	ResolveBinding(ctx context.Context, run string) (activity.Scope, error)
}
type States interface {
	Snapshot(ctx context.Context, run string) (readmodel.Snapshot, error)
}
type Activity interface {
	Engagement(run string) (streaming bool, last time.Time)
	Checkpoints(ctx context.Context, run string) ([]string, error)
}
type Previews interface {
	Live(run string) bool
}

// Finished states whose governed worktree is no longer used by the run. A run
// paused for a human decision can resume, so it is never finished here.
var finished = map[domain.State]bool{
	domain.StateBranchAccepted: true, domain.StateFailed: true, domain.StateCancelled: true,
	domain.StateMerged: true, domain.StateCompleted: true,
}

const maxRuns = 20000

type Sweeper struct {
	// mu serialises sweeps and product-requested releases.
	mu       sync.Mutex
	Policy   Policy
	Store    *Store
	Runs     Runs
	Binder   Binder
	States   States
	Activity Activity
	Previews Previews
	Now      func() time.Time
	// Started is the idle baseline for runs not used since this process
	// started: a restart never shortens a worktree's idle window.
	Started time.Time
	Log     io.Writer
}

type candidate struct {
	Candidate
	scope activity.Scope
	kept  string // why a protected worktree is kept: unfinished, streaming or preview
}

// Summary counts one sweep's retained worktrees by outcome, so an operator can
// see why a worktree is still kept.
type Summary struct {
	Retained, Unfinished, Streaming, Preview, Unverified, Evicted, Failed int
}

// Run sweeps every interval until ctx ends.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.Sweep(ctx); err != nil {
				s.logf("abcp worktree eviction sweep failed class=%s", class(err))
			}
		}
	}
}

// Sweep evaluates every retained worktree once and evicts those the policy
// selects. Runs whose binding, state or worktree cannot be verified are skipped,
// never evicted.
func (s *Sweeper) Sweep(ctx context.Context) ([]RecordV1, error) {
	evicted, _, err := s.sweep(ctx)
	return evicted, err
}

func (s *Sweeper) sweep(ctx context.Context) ([]RecordV1, Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	var summary Summary
	var candidates []candidate
	after := ""
	for count := 0; count < maxRuns; {
		runs, more, err := s.Runs.ListRuns(after, 100)
		if err != nil {
			return nil, summary, err
		}
		for _, reg := range runs {
			count++
			after = reg.RunID
			c, outcome := s.inspect(ctx, reg.RunID)
			switch outcome {
			case inspected:
				summary.Retained++
				candidates = append(candidates, c)
				switch c.kept {
				case "unfinished":
					summary.Unfinished++
				case "streaming":
					summary.Streaming++
				case "preview":
					summary.Preview++
				}
			case unverified:
				summary.Retained++
				summary.Unverified++
			}
		}
		if !more || len(runs) == 0 {
			break
		}
	}
	plain := make([]Candidate, len(candidates))
	byRun := map[string]candidate{}
	for i, c := range candidates {
		plain[i] = c.Candidate
		byRun[c.Run] = c
	}
	var evicted []RecordV1
	for _, d := range s.Policy.Decide(plain, now) {
		record, err := s.evict(ctx, byRun[d.Run], d.Reason, now)
		if err != nil {
			summary.Failed++
			s.logf("abcp worktree eviction failed run=%s reason=%s class=%s", d.Run, d.Reason, class(err))
			continue
		}
		summary.Evicted++
		evicted = append(evicted, record)
		s.logf("abcp worktree evicted at=%s run=%s reason=%s bytes=%d checkpoints=%d", record.EvictedAt, record.RunID, record.Reason, record.WorktreeBytes, len(record.Checkpoints))
	}
	if summary.Retained > 0 {
		s.logf("abcp worktree eviction sweep at=%s retained=%d kept-unfinished=%d kept-streaming=%d kept-preview=%d unverified=%d evicted=%d failed=%d",
			now.UTC().Format(time.RFC3339), summary.Retained, summary.Unfinished, summary.Streaming, summary.Preview, summary.Unverified, summary.Evicted, summary.Failed)
	}
	return evicted, summary, nil
}

type outcome int

const (
	notRetained outcome = iota // not a retained worktree: not the sweeper's concern
	inspected
	unverified // retained, but its state or size could not be verified: kept
)

func (s *Sweeper) inspect(ctx context.Context, run string) (candidate, outcome) {
	scope, err := s.Binder.ResolveBinding(ctx, run)
	if err != nil || !scope.Retain {
		return candidate{}, notRetained
	}
	path, err := activity.WorktreePath(ctx, scope)
	if err == nil && path == "" {
		return candidate{}, notRetained
	}
	if err != nil || !inside(scope.Repository, path) {
		return candidate{}, unverified
	}
	if record, ok, err := s.Store.Read(run); err != nil {
		return candidate{}, unverified
	} else if ok {
		// An eviction sealed before an interruption is finished, not re-decided.
		if err := removeWorktree(ctx, scope, path); err != nil {
			s.logf("abcp worktree eviction resume failed run=%s class=%s", run, class(err))
		} else {
			s.logf("abcp worktree eviction resumed run=%s reason=%s", run, record.Reason)
		}
		return candidate{}, notRetained
	}
	c := candidate{scope: scope, Candidate: Candidate{Run: run, Repository: scope.Repository, Worktree: path}}
	if c.Terminal, c.TerminalAt, err = s.finished(ctx, run); err != nil {
		return candidate{}, unverified
	}
	streaming, last := s.Activity.Engagement(run)
	if last.Before(s.Started) {
		last = s.Started
	}
	c.LastAccess = last
	switch {
	case !c.Terminal:
		c.kept = "unfinished"
	case streaming:
		c.kept = "streaming"
	case s.Previews.Live(run):
		c.kept = "preview"
	}
	c.Protected = c.kept != ""
	if c.Bytes, err = size(path); err != nil {
		return candidate{}, unverified
	}
	return c, inspected
}

// finished reports whether the run reached a finished state, and when.
func (s *Sweeper) finished(ctx context.Context, run string) (bool, time.Time, error) {
	snapshot, err := s.States.Snapshot(ctx, run)
	if err != nil {
		return false, time.Time{}, err
	}
	state := snapshot.Projection.CurrentState
	var at time.Time
	for i := len(snapshot.Events) - 1; i >= 0; i-- {
		if e := snapshot.Events[i]; e.StateTo != "" {
			if state == "" {
				state = string(e.StateTo)
			}
			if string(e.StateTo) == state {
				at = e.Timestamp
				break
			}
		}
	}
	return finished[domain.State(state)] && !at.IsZero(), at, nil
}

func (s *Sweeper) evict(ctx context.Context, c candidate, reason string, now time.Time) (RecordV1, error) {
	shas, err := s.Activity.Checkpoints(ctx, c.Run)
	if err != nil {
		return RecordV1{}, err
	}
	record := RecordV1{Kind: "WorktreeEvictionV1", SchemaVersion: 1, RunID: c.Run, AuthorityDigest: c.scope.AuthorityDigest,
		Repository: c.scope.Repository, Branch: c.scope.Branch, Base: c.scope.Base, Worktree: c.Worktree, Reason: reason,
		EvictedAt: now.UTC().Format(time.RFC3339Nano), WorktreeBytes: c.Bytes, Checkpoints: []PinnedCheckpoint{}}
	for _, sha := range shas {
		ref := CheckpointRef(c.Run, sha)
		if err := pin(ctx, c.scope.Repository, ref, sha); err != nil {
			return RecordV1{}, err
		}
		record.Checkpoints = append(record.Checkpoints, PinnedCheckpoint{SHA: sha, Ref: ref})
	}
	// Seal first: preview trusts the record only once every ref is pinned, and
	// a crash after this point resumes the removal on the next sweep.
	if err := s.Store.Write(record); err != nil {
		return RecordV1{}, err
	}
	return record, removeWorktree(ctx, c.scope, c.Worktree)
}

func pin(ctx context.Context, repository, ref, sha string) error {
	if !gitSHA.MatchString(sha) {
		return ErrIntegrity
	}
	if _, err := git(ctx, repository, "cat-file", "-e", sha+"^{commit}"); err != nil {
		return err
	}
	if current, err := git(ctx, repository, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
		if current != sha {
			return ErrIntegrity
		}
		return nil
	}
	// An empty old value creates the ref only if it does not exist yet.
	_, err := git(ctx, repository, "update-ref", "--no-deref", ref, sha, "")
	return err
}

// removeWorktree removes the run's worktree only while it still holds the
// run's branch at the expected path inside the governed repository.
func removeWorktree(ctx context.Context, scope activity.Scope, path string) error {
	current, err := activity.WorktreePath(ctx, scope)
	if err != nil {
		return err
	}
	if current == "" {
		return nil
	}
	if current != path || !inside(scope.Repository, path) {
		return ErrIntegrity
	}
	_, err = git(ctx, scope.Repository, "worktree", "remove", "--force", "--", path)
	return err
}

func inside(repository, path string) bool {
	rel, err := filepath.Rel(repository, path)
	return err == nil && rel != "." && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, "../")
}

// size sums regular file sizes without following symlinks, bounded in entries.
func size(root string) (int64, error) {
	var total int64
	entries := 0
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entries++; entries > 5_000_000 {
			return ErrIntegrity
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func git(ctx context.Context, repository string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repository}, args...)...)
	cmd.Env = gitexec.Environment()
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(out.String()), nil
}

func marshal(record RecordV1) ([]byte, error) {
	data, err := json.MarshalIndent(record, "", "  ")
	return append(data, '\n'), err
}

func class(err error) string {
	switch {
	case errors.Is(err, ErrIntegrity):
		return "integrity"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "interrupted"
	}
	return "unavailable"
}

func (s *Sweeper) logf(format string, args ...any) {
	if s.Log != nil {
		fmt.Fprintf(s.Log, format+"\n", args...)
	}
}

// Package eviction removes controller-retained governed worktrees once they are
// no longer needed. The worktree is a disposable cache: before removal every
// recorded checkpoint is pinned under refs/abcp/checkpoints/<run>/<sha> and a
// durable eviction record seals the verified binding, so activity history and
// exact-source preview survive the eviction.
package eviction

import (
	"sort"
	"time"
)

const (
	DefaultIdle       = 24 * time.Hour
	DefaultMaxAge     = 7 * 24 * time.Hour
	DefaultQuotaBytes = int64(5) << 30
	DefaultInterval   = 10 * time.Minute
)

// Policy bounds how long a finished run's worktree is kept. A zero duration or
// quota disables that rule.
type Policy struct {
	Idle       time.Duration // since the run finished or was last used, whichever is later
	MaxAge     time.Duration // since the run finished, regardless of use
	QuotaBytes int64         // per governed repository, across all its retained worktrees
}

// DefaultPolicy is 24 hours idle, 7 days maximum and 5 GiB per repository.
func DefaultPolicy() Policy {
	return Policy{Idle: DefaultIdle, MaxAge: DefaultMaxAge, QuotaBytes: DefaultQuotaBytes}
}

// Candidate is one run whose governed worktree still exists.
type Candidate struct {
	Run, Repository, Worktree string
	Terminal                  bool      // the run reached a finished state
	TerminalAt                time.Time // when it did
	LastAccess                time.Time // latest activity read, stream or preview resolution
	Bytes                     int64
	// Protected worktrees are never evicted: the run is not finished, a client
	// is streaming its activity, or one of its previews is live.
	Protected bool
}

const (
	ReasonIdle   = "idle"
	ReasonMaxAge = "max-age"
	ReasonQuota  = "quota"
)

type Decision struct {
	Candidate
	Reason string
}

func (c Candidate) evictable() bool { return c.Terminal && !c.Protected }

func (c Candidate) lastUse() time.Time {
	if c.LastAccess.After(c.TerminalAt) {
		return c.LastAccess
	}
	return c.TerminalAt
}

// Decide returns the worktrees to evict now, ordered by run. Age and idle rules
// apply first; then, per repository, the least recently used evictable
// worktrees are removed until usage fits the quota. Protected and unfinished
// worktrees still count toward the quota but are never chosen.
func (p Policy) Decide(candidates []Candidate, now time.Time) []Decision {
	chosen := map[string]string{}
	for _, c := range candidates {
		if !c.evictable() {
			continue
		}
		switch {
		case p.MaxAge > 0 && now.Sub(c.TerminalAt) >= p.MaxAge:
			chosen[c.Run] = ReasonMaxAge
		case p.Idle > 0 && now.Sub(c.lastUse()) >= p.Idle:
			chosen[c.Run] = ReasonIdle
		}
	}
	if p.QuotaBytes > 0 {
		byRepository := map[string][]Candidate{}
		for _, c := range candidates {
			byRepository[c.Repository] = append(byRepository[c.Repository], c)
		}
		for _, group := range byRepository {
			var usage int64
			var lru []Candidate
			for _, c := range group {
				if _, gone := chosen[c.Run]; gone {
					continue
				}
				usage += c.Bytes
				if c.evictable() {
					lru = append(lru, c)
				}
			}
			sort.SliceStable(lru, func(i, j int) bool {
				if !lru[i].lastUse().Equal(lru[j].lastUse()) {
					return lru[i].lastUse().Before(lru[j].lastUse())
				}
				return lru[i].Run < lru[j].Run
			})
			for _, c := range lru {
				if usage <= p.QuotaBytes {
					break
				}
				chosen[c.Run] = ReasonQuota
				usage -= c.Bytes
			}
		}
	}
	var out []Decision
	for _, c := range candidates {
		if reason, ok := chosen[c.Run]; ok {
			out = append(out, Decision{Candidate: c, Reason: reason})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Run < out[j].Run })
	return out
}

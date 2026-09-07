package scheduler

import (
	"errors"
	"sort"
	"strings"
	"sync"
)

var (
	// ErrDuplicateCandidate means the same project/plan/run/attempt acceptance
	// identity was already appended.
	ErrDuplicateCandidate = errors.New("accepted candidate identity already queued")
	// ErrReplayCandidate means accepted head or acceptance evidence was reused
	// under a different identity.
	ErrReplayCandidate = errors.New("accepted candidate provenance replay")
)

// Queue is an in-process append-only accepted-candidate queue. It deliberately
// exposes no remove or mutation operation. Snapshot ordering is derived solely
// from immutable candidate data, so it is independent of enqueue timing.
type Queue struct {
	mu         sync.RWMutex
	candidates []AcceptedCandidate
	identities map[string]struct{}
	heads      map[string]struct{}
	evidence   map[string]struct{}
}

// NewQueue returns an empty append-only queue.
func NewQueue() *Queue {
	return &Queue{
		identities: make(map[string]struct{}),
		heads:      make(map[string]struct{}),
		evidence:   make(map[string]struct{}),
	}
}

// Append validates and appends one controller-accepted candidate. Failed
// appends leave the queue unchanged.
func (q *Queue) Append(candidate AcceptedCandidate) error {
	if q == nil {
		return errors.New("queue is required")
	}
	if err := candidate.validate(); err != nil {
		return err
	}
	copyCandidate := cloneCandidate(candidate)
	input := copyCandidate.data
	identity := copyCandidate.Key()
	head := strings.Join([]string{input.Repository, input.HeadSHA}, "\x00")

	q.mu.Lock()
	defer q.mu.Unlock()
	if _, exists := q.identities[identity]; exists {
		return ErrDuplicateCandidate
	}
	if _, exists := q.heads[head]; exists {
		return ErrReplayCandidate
	}
	for _, ref := range input.AcceptanceEvidence {
		if _, exists := q.evidence[evidenceKey(ref)]; exists {
			return ErrReplayCandidate
		}
	}

	q.candidates = append(q.candidates, copyCandidate)
	q.identities[identity] = struct{}{}
	q.heads[head] = struct{}{}
	for _, ref := range input.AcceptanceEvidence {
		q.evidence[evidenceKey(ref)] = struct{}{}
	}
	return nil
}

// Snapshot returns deep copies in a stable total order: acceptance timestamp,
// then exact project/plan/run/attempt/repository/branch/start/head identity.
func (q *Queue) Snapshot() []AcceptedCandidate {
	if q == nil {
		return nil
	}
	q.mu.RLock()
	candidates := make([]AcceptedCandidate, len(q.candidates))
	for index, candidate := range q.candidates {
		candidates[index] = cloneCandidate(candidate)
	}
	q.mu.RUnlock()
	sort.Slice(candidates, func(i, j int) bool {
		return candidateOrderKey(candidates[i]) < candidateOrderKey(candidates[j])
	})
	return candidates
}

// Len returns the number of successfully appended candidates.
func (q *Queue) Len() int {
	if q == nil {
		return 0
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.candidates)
}

func candidateOrderKey(candidate AcceptedCandidate) string {
	input := candidate.data
	return strings.Join([]string{
		input.AcceptedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
		input.ProjectID, input.PlanID, input.RunID, input.AttemptID, input.Repository,
		input.Branch, input.StartSHA, input.HeadSHA,
	}, "\x00")
}

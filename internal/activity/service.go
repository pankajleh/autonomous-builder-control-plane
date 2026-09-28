package activity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type SnapshotReader interface {
	Snapshot(context.Context, string) (readmodel.Snapshot, error)
}

type Service struct {
	store     *Store
	resolver  Resolver
	snapshots SnapshotReader
	cursors   *serviceapi.CursorSigner
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	workers   map[string]*worker
	faults    map[string]error
	sidecars  map[string]*sharedSidecar
	wg        sync.WaitGroup
	closed    bool
	now       func() time.Time
	// diagnostics receives one bounded operator line per controller marker.
	diagnostics io.Writer
	// worktreeMissing records when a run's governed worktree was first seen
	// missing after a provider proof, bounding the finish transition grace.
	worktreeMissing map[string]time.Time
	// lastAccess records, per run, when a client last opened or closed a live
	// activity stream since this process started (worktree eviction idle).
	// Paged reads do not count: background readers, such as Repo C's
	// notification poller, page every run's activity every few seconds.
	lastAccess map[string]time.Time
	// evictions reports a controller-sealed worktree eviction (see SetEvictions).
	evictions func(run string) (reason string, at time.Time, ok bool)
	// evictionRecorded marks runs whose eviction event is already appended.
	evictionRecorded map[string]bool
	// gaps holds, per run, a provider replay gap seen once and not yet re-read.
	gaps map[string]string
}
type worker struct {
	touched       time.Time
	subscriptions int
}
type sharedSidecar struct {
	ready   chan struct{}
	sc      *sidecar
	err     error
	refs    int
	closing chan struct{}
}

const initialProviderUnavailableRetryBudget = 6

// deferProviderUnavailable reports whether an unavailable provider read is
// retried before it is marked. The provider session can briefly be missing
// from a sidecar in two cases: a newly admitted run has no provider proof yet,
// and a freshly started sidecar (after a controller restart or sidecar respawn)
// has not yet listed an existing session. Both get the same bounded budget.
// Otherwise, once a provider proof exists and this sidecar has served the run,
// any loss remains immediately fail-closed.
func deferProviderUnavailable(err error, previous *providerProof, freshSidecar bool, misses int) bool {
	return errors.Is(err, ErrUnavailable) && (previous == nil || freshSidecar) && misses < initialProviderUnavailableRetryBudget
}

// A failed activity substrate disables only this extension. Existing run reads
// and execution remain available through their unchanged dependencies.
func New(parent context.Context, root string, catalog Catalog, snapshots SnapshotReader, cursors *serviceapi.CursorSigner) (*Service, error) {
	if parent == nil || catalog == nil || snapshots == nil || cursors == nil {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithCancel(parent)
	// Runtime cleanup is provider-only: failure cannot disable ledger reads.
	_ = reconcileSidecarRuntime(ctx, root)
	store, _ := OpenStore(root + "/activity")
	return &Service{store: store, resolver: Resolver{root, catalog}, snapshots: snapshots, cursors: cursors, ctx: ctx, cancel: cancel, workers: map[string]*worker{}, faults: map[string]error{}, sidecars: map[string]*sharedSidecar{}, now: time.Now, diagnostics: os.Stderr, worktreeMissing: map[string]time.Time{}, lastAccess: map[string]time.Time{}}, nil
}
func (s *Service) Close() error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
	if s.store != nil {
		return s.store.Close()
	}
	return nil
}
func apiError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, serviceapi.ErrInvalidCursor) || errors.Is(err, serviceapi.ErrCursorEpochChanged) || errors.Is(err, serviceapi.ErrProjectionLineageChanged) {
		return err
	}
	return serviceapi.ErrActivityUnavailable
}
func (s *Service) registration(run string) (string, error) {
	reg, err := s.resolver.Catalog.ReadRun(run)
	if err != nil || reg.RunID != run {
		return "", ErrIntegrity
	}
	return jsonDigest(reg), nil
}

// appendUnknown records a controller marker. A stopping service records none:
// shutdown interrupts provider reads and git checks, which is not missing
// detail, and the next process resumes from the durable provider proof.
func (s *Service) appendUnknown(run, registration, key, title string) error {
	if s.stopping() {
		return context.Canceled
	}
	_, err := s.store.Append(run, registration, unknown(run, key, title, s.now()))
	return err
}

func (s *Service) stopping() bool { return s.ctx != nil && s.ctx.Err() != nil }

func (s *Service) refresh(ctx context.Context, run, registration string) (Scope, error) {
	snapshot, err := s.snapshots.Snapshot(ctx, run)
	if err != nil {
		return Scope{}, err
	}
	for _, fact := range snapshot.Events {
		if fact.RunID != run {
			return Scope{}, ErrIntegrity
		}
		if e, ok := normalizeLedger(fact, s.now()); ok {
			if _, err = s.store.Append(run, registration, e); err != nil {
				return Scope{}, err
			}
		}
	}
	if err = s.recordEviction(run, registration); err != nil {
		return Scope{}, err
	}
	scope, err := s.resolver.Resolve(ctx, run)
	if err != nil {
		// Unless the manifest retains it (worktree.retain), the governed worktree
		// is removed once the run reaches an authoritative terminal branch
		// outcome. Provider detail can no longer be refreshed at that point, but
		// that expected cleanup is not an integrity ambiguity and must not poison
		// an otherwise clean accepted checkpoint with a controller UNKNOWN marker.
		// Any provider warning or integrity marker observed before the terminal
		// transition remains durable. A retained worktree keeps resolving here,
		// so trailing provider detail is still collected after acceptance.
		if terminalActivityState(snapshot) {
			return Scope{}, nil
		}
		// Without retention the provider removes its governed worktree when it
		// finishes, before the controller records acceptance. Once implementation
		// has completed that removal is the same expected cleanup, not missing detail.
		if errors.Is(err, errWorktreeMissing) && implementationFinished(snapshot) {
			return Scope{}, nil
		}
		// A controller eviction seals its record before removing the retained
		// worktree, whatever finished state the run has since reached.
		if errors.Is(err, errWorktreeMissing) && s.wasEvicted(run) {
			return Scope{}, nil
		}
		if s.worktreePending(run, registration, snapshot, err) {
			return Scope{}, nil
		}
		// A cancelled request or a stopping service interrupted the binding
		// check; that is not an unresolved binding.
		if ctx.Err() != nil || s.stopping() {
			return Scope{}, context.Canceled
		}
		s.diagnose(run, "binding-unavailable", err)
		if err = s.appendUnknown(run, registration, "binding-unavailable", "Implementation detail unavailable"); err != nil {
			return Scope{}, err
		}
		return Scope{}, nil
	}
	s.worktreePresent(run)
	if e, ok := checkpoint(ctx, scope, s.now()); ok {
		if _, err = s.store.Append(run, registration, e); err != nil {
			return Scope{}, err
		}
	}
	// A retained worktree keeps resolving after the run finishes, but its
	// provider session has ended. Trailing detail is read only for a bounded
	// window after the first finished transition; afterwards no provider read
	// can turn a complete history into a false marker, for example when a
	// restarted sidecar has not yet listed the old session.
	if at, ok := finishedAt(snapshot); ok && s.now().Sub(at) > trailingDetailWindow {
		return Scope{}, nil
	}
	return scope, nil
}

// trailingDetailWindow bounds provider reads after a run first finishes.
// Ralphex writes its final detail within seconds of finishing.
const trailingDetailWindow = 15 * time.Minute

// finishedAt returns when the run first reached an authoritative terminal
// branch outcome. Later lifecycle states, such as MERGED, follow it.
func finishedAt(snapshot readmodel.Snapshot) (time.Time, bool) {
	for _, fact := range snapshot.Events {
		switch fact.StateTo {
		case "BRANCH_ACCEPTED", "FAILED", "CANCELLED":
			return fact.Timestamp, !fact.Timestamp.IsZero()
		}
	}
	return time.Time{}, false
}

// The governed worktree is absent at both ends of a healthy run: the provider
// creates it shortly after admission and removes it when it finishes, shortly
// before the controller records completion. Treat an absent worktree as pending
// only within a bounded window: after admission while no provider proof exists,
// or from its first observation once a proof exists. Afterwards an unresolved
// binding remains a fail-closed controller marker.
const worktreeTransitionWindow = 30 * time.Second

func (s *Service) worktreePending(run, registration string, snapshot readmodel.Snapshot, err error) bool {
	if !errors.Is(err, errWorktreeMissing) {
		return false
	}
	proof, proofErr := s.store.proof(run, registration)
	if proofErr != nil {
		return false
	}
	if proof != nil {
		return s.worktreeMissingFor(run) < worktreeTransitionWindow
	}
	var admitted time.Time
	for _, fact := range snapshot.Events {
		if !fact.Timestamp.IsZero() && (admitted.IsZero() || fact.Timestamp.Before(admitted)) {
			admitted = fact.Timestamp
		}
	}
	return !admitted.IsZero() && s.now().Sub(admitted) < worktreeTransitionWindow
}

// worktreeMissingFor returns how long the run's worktree has been observed
// missing, recording the first observation.
func (s *Service) worktreeMissingFor(run string) time.Duration {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.worktreeMissing == nil {
		s.worktreeMissing = map[string]time.Time{}
	}
	first, seen := s.worktreeMissing[run]
	if !seen {
		s.worktreeMissing[run] = now
		return 0
	}
	return now.Sub(first)
}

func (s *Service) worktreePresent(run string) {
	s.mu.Lock()
	delete(s.worktreeMissing, run)
	s.mu.Unlock()
}

func implementationFinished(snapshot readmodel.Snapshot) bool {
	switch activityState(snapshot) {
	case "IMPLEMENTATION_COMPLETED", "BRANCH_ACCEPTANCE_PENDING":
		return true
	}
	return false
}

func activityState(snapshot readmodel.Snapshot) string {
	state := snapshot.Projection.CurrentState
	if state == "" {
		for i := len(snapshot.Events) - 1; i >= 0; i-- {
			if snapshot.Events[i].StateTo != "" {
				state = string(snapshot.Events[i].StateTo)
				break
			}
		}
	}
	return state
}

func terminalActivityState(snapshot readmodel.Snapshot) bool {
	switch activityState(snapshot) {
	case "BRANCH_ACCEPTED", "FAILED", "CANCELLED":
		return true
	default:
		return false
	}
}

func (s *Service) prepare(ctx context.Context, run string, subscribe bool) (string, *worker, error) {
	if s == nil || s.store == nil {
		return "", nil, ErrUnavailable
	}
	registration, err := s.registration(run)
	if err != nil {
		return "", nil, err
	}
	if _, err = s.refresh(ctx, run, registration); err != nil {
		return "", nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if subscribe {
		s.noteEngagement(run)
	}
	if s.closed || s.faults[run] != nil {
		return "", nil, ErrUnavailable
	}
	if w := s.workers[run]; w != nil {
		w.touched = s.now()
		if subscribe {
			w.subscriptions++
		}
		return registration, w, nil
	}
	if len(s.workers) >= MaxStreams {
		return "", nil, ErrExhausted
	}
	w := &worker{touched: s.now()}
	if subscribe {
		w.subscriptions = 1
	}
	s.workers[run] = w
	s.wg.Add(1)
	go s.collect(run, registration, w)
	return registration, w, nil
}

// confirmGap reports whether the run's provider replay gap was already seen
// once at the same position. A first sighting is remembered, not confirmed.
func (s *Service) confirmGap(run, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gaps == nil {
		s.gaps = map[string]string{}
	}
	if s.gaps[run] == key {
		delete(s.gaps, run)
		return true
	}
	s.gaps[run] = key
	return false
}

// SetEvictions installs the reader of sealed worktree evictions. It is set once
// before serving. An evicted run's missing worktree is expected cleanup, and
// the eviction itself is recorded as an ABCP state activity event.
func (s *Service) SetEvictions(evictions func(run string) (reason string, at time.Time, ok bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictions = evictions
}

func (s *Service) eviction(run string) (string, time.Time, bool) {
	s.mu.Lock()
	evictions := s.evictions
	s.mu.Unlock()
	if evictions == nil {
		return "", time.Time{}, false
	}
	return evictions(run)
}

func (s *Service) wasEvicted(run string) bool {
	_, _, ok := s.eviction(run)
	return ok
}

// recordEviction appends the run's eviction event once it is sealed. The
// event is derived from the durable record, so an eviction interrupted before
// any read, or sealed before this process started, is still reported.
func (s *Service) recordEviction(run, registration string) error {
	s.mu.Lock()
	done := s.evictionRecorded[run]
	s.mu.Unlock()
	if done {
		return nil
	}
	reason, at, ok := s.eviction(run)
	if !ok {
		return nil
	}
	e, ok := evictionEvent(run, reason, at, s.now())
	if !ok {
		return nil
	}
	if _, err := s.store.Append(run, registration, e); err != nil {
		return err
	}
	s.mu.Lock()
	if s.evictionRecorded == nil {
		s.evictionRecorded = map[string]bool{}
	}
	s.evictionRecorded[run] = true
	s.mu.Unlock()
	return nil
}

// Engagement reports whether a client is streaming the run's activity and when
// a live stream of it was last opened or closed by this process. A zero time
// means no stream since startup. Paged reads are not engagement.
func (s *Service) Engagement(run string) (streaming bool, last time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w := s.workers[run]; w != nil && w.subscriptions > 0 {
		streaming = true
	}
	return streaming, s.lastAccess[run]
}

// Checkpoints records the run's current clean worktree head, then returns every
// clean checkpoint SHA recorded for the run, in activity order. It starts no
// collection worker and does not count as use of the run.
func (s *Service) Checkpoints(ctx context.Context, run string) ([]string, error) {
	if s == nil || s.store == nil {
		return nil, ErrUnavailable
	}
	registration, err := s.registration(run)
	if err != nil {
		return nil, err
	}
	// Eviction pins what is recorded here, so observe the final head first.
	if _, err = s.refresh(ctx, run, registration); err != nil {
		return nil, err
	}
	var shas []string
	seen := map[string]bool{}
	after := uint64(0)
	for {
		events, _, more, err := s.store.read(run, registration, after, MaxPageSize)
		if err != nil {
			return nil, err
		}
		for _, e := range events {
			if e.SourceKind == "ABCP_CHECKPOINT_OBSERVER" && e.Category == "CHECKPOINT" && e.Status == "AVAILABLE" && e.CheckpointClean && gitSHA.MatchString(e.CheckpointSHA) && !seen[e.CheckpointSHA] {
				seen[e.CheckpointSHA] = true
				shas = append(shas, e.CheckpointSHA)
			}
			after = e.Ordinal
		}
		if !more || len(events) == 0 {
			return shas, nil
		}
	}
}

func (s *Service) releaseSubscription(w *worker, run string) {
	s.mu.Lock()
	w.subscriptions--
	w.touched = s.now()
	s.noteEngagement(run)
	s.mu.Unlock()
}

// noteEngagement records live-stream use of the run. Callers hold s.mu.
func (s *Service) noteEngagement(run string) {
	if s.lastAccess == nil {
		s.lastAccess = map[string]time.Time{}
	}
	s.lastAccess[run] = s.now()
}

func (s *Service) acquireSidecar(scope Scope) (*sharedSidecar, string) {
	key := identity(scope.Repository, scope.Ralphex.BinaryPath, scope.Ralphex.BinarySHA256, scope.Ralphex.SourceSHA)
	s.mu.Lock()
	if shared := s.sidecars[key]; shared != nil {
		if shared.closing != nil {
			closed := shared.closing
			s.mu.Unlock()
			<-closed
			return s.acquireSidecar(scope)
		}
		shared.refs++
		s.mu.Unlock()
		<-shared.ready
		return shared, key
	}
	shared := &sharedSidecar{ready: make(chan struct{}), refs: 1}
	s.sidecars[key] = shared
	s.mu.Unlock()
	shared.sc, shared.err = startSidecar(s.ctx, scope, s.resolver.Root)
	close(shared.ready)
	return shared, key
}
func (s *Service) releaseSidecar(key string) {
	s.mu.Lock()
	shared := s.sidecars[key]
	shared.refs--
	last := shared.refs == 0
	if last {
		shared.closing = make(chan struct{})
	}
	s.mu.Unlock()
	if last {
		if shared.sc != nil {
			shared.sc.close()
		}
		s.mu.Lock()
		delete(s.sidecars, key)
		close(shared.closing)
		s.mu.Unlock()
	}
}

func transientReadError(err error) bool {
	return errors.Is(err, serviceapi.ErrAuthoritativeReadBusy) || errors.Is(err, runtimecatalog.ErrBusy) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

func (s *Service) collect(run, registration string, w *worker) {
	defer s.wg.Done()
	idleExit := false
	defer func() {
		s.mu.Lock()
		if s.workers[run] == w {
			delete(s.workers, run)
		}
		if !idleExit && s.ctx.Err() == nil {
			if s.faults == nil {
				s.faults = map[string]error{}
			}
			s.faults[run] = ErrUnavailable
		}
		s.mu.Unlock()
	}()
	var shared *sharedSidecar
	key := ""
	last := uint64(math.MaxUint64)
	defer func() {
		if shared != nil {
			s.releaseSidecar(key)
		}
	}()
	backoff := 250 * time.Millisecond
	providerFailed := false
	initialUnavailableMisses := 0
	freshSidecar := false
	for {
		if s.ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		idle := w.subscriptions == 0 && s.now().Sub(w.touched) > time.Minute
		if idle {
			// Retire under the admission lock so a new subscriber cannot attach
			// to a collector that has already decided to stop.
			delete(s.workers, run)
		}
		s.mu.Unlock()
		if idle {
			idleExit = true
			return
		}
		scope, err := s.refresh(s.ctx, run, registration)
		if err != nil && !transientReadError(err) {
			return
		}
		if err == nil && scope.RunID != "" && !providerFailed {
			if shared == nil {
				shared, key = s.acquireSidecar(scope)
				freshSidecar, initialUnavailableMisses = true, 0
			}
			if shared.err != nil {
				if s.stopping() {
					return
				}
				s.diagnose(run, "sidecar-unavailable", shared.err)
				if s.appendUnknown(run, registration, "sidecar-unavailable", "Implementation detail unavailable") != nil {
					return
				}
				s.releaseSidecar(key)
				shared = nil
			} else {
				err = s.collectBatch(scope, registration, shared.sc, &last)
				if err != nil && s.stopping() {
					// Shutdown stops the sidecar and cancels in-flight reads.
					return
				}
				if errors.Is(err, errWorktreeMissing) {
					// Refresh classifies an absent worktree on its next pass.
				} else if errors.Is(err, ErrIntegrity) {
					providerFailed = true
					s.diagnose(run, "provider-integrity", err)
					if s.appendUnknown(run, registration, "provider-integrity", "Implementation detail integrity failure") != nil {
						return
					}
				} else if err != nil {
					previous, proofErr := s.store.proof(run, registration)
					if proofErr != nil {
						return
					}
					if deferProviderUnavailable(err, previous, freshSidecar, initialUnavailableMisses) {
						initialUnavailableMisses++
					} else {
						s.diagnose(run, "provider-unavailable", err)
						if s.appendUnknown(run, registration, "provider-unavailable", "Implementation detail unavailable") != nil {
							return
						}
					}
				} else {
					initialUnavailableMisses, freshSidecar = 0, false
					backoff = 250 * time.Millisecond
				}
				select {
				case <-shared.sc.done:
					s.releaseSidecar(key)
					shared = nil
					last = math.MaxUint64
				default:
				}
			}
		}
		delay := backoff
		if err == nil && (scope.RunID == "" || providerFailed) {
			delay = 2 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > 5*time.Second {
			backoff = 5 * time.Second
		}
	}
}

func (s *Service) collectBatch(scope Scope, registration string, sc *sidecar, last *uint64) error {
	list, err := sc.sessions(s.ctx)
	if err != nil {
		return atStep("sessions", err)
	}
	selected, err := correlate(scope, list)
	if err != nil {
		return atStep("correlate", err)
	}
	previous, err := s.store.proof(scope.RunID, registration)
	if err != nil {
		return atStep("proof-read", err)
	}
	before, err := progressProof(scope, selected, previous)
	if err != nil {
		return atStep("progress-before", err)
	}
	messages, err := sc.batch(s.ctx, selected.ID, *last)
	if err != nil {
		return atStep("provider-read", err)
	}
	// Revalidate controller binding and provider metadata before persisting any
	// detail from this bounded batch.
	current, err := s.resolver.Resolve(s.ctx, scope.RunID)
	if err != nil {
		if errors.Is(err, errWorktreeMissing) {
			// The provider removes its worktree when it finishes. Discard this
			// batch; refresh decides between expected cleanup, a bounded
			// transition and a fail-closed marker.
			return atStep("revalidate-binding", err)
		}
		return annotate("revalidate-binding", err, ErrIntegrity)
	}
	if jsonDigest(current) != jsonDigest(scope) {
		return atStep("revalidate-scope", ErrIntegrity)
	}
	list, err = sc.sessions(s.ctx)
	if err != nil {
		return atStep("revalidate-sessions", err)
	}
	again, err := correlate(scope, list)
	if err != nil {
		return atStep("revalidate-correlate", err)
	}
	proof, err := progressProof(scope, again, &before)
	if err != nil {
		return atStep("progress-after", err)
	}
	// Source IDs must strictly increase within a batch. Events at or before the
	// committed position are the provider re-sending history from the overlapping
	// resume; they must match what was committed and are not appended again.
	committed := *last
	events := make([]Event, 0, len(messages))
	fresh := make([]providerMessage, 0, len(messages))
	var resent []Event
	for i, message := range messages {
		if i > 0 && message.id <= messages[i-1].id {
			return atStep("ordering", ErrIntegrity)
		}
		event, err := normalizeProvider(scope.RunID, selected.ID, strconv.FormatUint(message.id, 10), message.payload, s.now())
		if err != nil {
			return atStep("normalize", err)
		}
		if committed != math.MaxUint64 && message.id <= committed {
			resent = append(resent, event)
			continue
		}
		events = append(events, event)
		fresh = append(fresh, message)
	}
	if err = s.store.checkCommittedProviderReplay(scope.RunID, registration, resent); err != nil {
		return atStep("resume-overlap", err)
	}
	if err = s.store.checkProviderReplay(scope.RunID, registration, events); err != nil {
		return atStep("replay", err)
	}
	if err = sc.verifyOwner(); err != nil {
		return atStep("owner", err)
	}
	if err = s.store.saveProof(scope.RunID, registration, proof); err != nil {
		return atStep("proof-save", err)
	}
	// A grown progress source with no resumable events does not prove
	// continuity. Retry its available replay and make the uncertainty explicit.
	// Only growth observed before this read counts: bytes appended after the
	// bounded read ended are delivered by the next batch, not missing.
	if len(fresh) == 0 && previous != nil && before.Size > previous.Size {
		s.diagnose(scope.RunID, "provider-silent-gap", nil)
		if err = s.appendUnknown(scope.RunID, registration, identity("silent-gap", proof.Generation, proof.PrefixDigest), "Implementation detail replay gap"); err != nil {
			return err
		}
		*last = math.MaxUint64
	}
	for i, message := range fresh {
		expected := *last + 1
		if *last == math.MaxUint64 {
			// Ralphex's finite SSE replayer assigns the first source event ID as 1.
			// MaxUint64 is our local sentinel for "no prior source event", so treating
			// the wrapped value 0 as the expected first ID creates a false replay-gap
			// marker on every healthy run and makes the checkpoint ineligible for
			// Preview Runtime. Preserve gap detection while honoring the provider's
			// documented first-event identity.
			expected = 1
		}
		if message.id != expected {
			gapKey := identity("gap", proof.Generation, strconv.FormatUint(*last, 10), strconv.FormatUint(message.id, 10))
			if !s.confirmGap(scope.RunID, gapKey) {
				// The live sidecar can deliver an event before an earlier one it is
				// still persisting. Stop at the gap; the next batch resumes from the
				// last contiguous event and re-reads the session. Only the same gap
				// seen again is a real one.
				s.noteGap(scope.RunID, *last, message.id, false)
				return nil
			}
			s.noteGap(scope.RunID, *last, message.id, true)
			s.diagnose(scope.RunID, "provider-replay-gap", nil)
			if err = s.appendUnknown(scope.RunID, registration, gapKey, "Implementation detail replay gap"); err != nil {
				return err
			}
		}
		if _, err = s.store.Append(scope.RunID, registration, events[i]); err != nil {
			return err
		}
		*last = message.id
	}
	return nil
}

type activityCursor struct {
	Run, Generation string
	After           uint64
}

func (s *Service) ReadActivity(ctx context.Context, run string, request serviceapi.PageRequestV1) (json.RawMessage, error) {
	if request.PageSize < 1 || request.PageSize > MaxPageSize {
		return nil, serviceapi.ErrInvalidCursor
	}
	var cursor activityCursor
	if request.Cursor != "" {
		if err := s.cursors.Verify(request.Cursor, serviceapi.CursorKindLedger, "activity-v1:"+run, &cursor); err != nil {
			return nil, err
		}
		if cursor.Run != run || cursor.After == 0 {
			return nil, serviceapi.ErrInvalidCursor
		}
	}
	registration, _, err := s.prepare(ctx, run, false)
	if err != nil {
		return nil, apiError(err)
	}
	events, generation, more, err := s.store.read(run, registration, cursor.After, request.PageSize)
	if err != nil {
		if request.Cursor != "" {
			return nil, serviceapi.ErrProjectionLineageChanged
		}
		return nil, apiError(err)
	}
	if request.Cursor != "" && cursor.Generation != generation {
		return nil, serviceapi.ErrProjectionLineageChanged
	}
	page := Page{SchemaVersion: "ActivityPageV1", RunID: run, Events: events}
	if more {
		now := s.now()
		page.NextCursor, err = s.cursors.Sign(serviceapi.CursorKindLedger, "activity-v1:"+run, activityCursor{run, generation, events[len(events)-1].Ordinal}, now, now.Add(serviceapi.MaxCursorLifetime))
		if err != nil {
			return nil, err
		}
	}
	data, err := json.Marshal(page)
	return data, apiError(err)
}

func (s *Service) OpenActivityStream(ctx context.Context, run, last string) (serviceapi.ActivitySubscription, error) {
	var after uint64
	if last != "" {
		var err error
		after, err = strconv.ParseUint(last, 10, 64)
		if err != nil || after == 0 || strconv.FormatUint(after, 10) != last {
			return nil, serviceapi.ErrInvalidCursor
		}
	}
	registration, w, err := s.prepare(ctx, run, true)
	if err != nil {
		return nil, apiError(err)
	}
	_, generation, _, err := s.store.read(run, registration, after, 1)
	if err != nil {
		s.releaseSubscription(w, run)
		if last != "" {
			return nil, serviceapi.ErrInvalidCursor
		}
		return nil, apiError(err)
	}
	return &subscription{service: s, worker: w, run: run, registration: registration, generation: generation, after: after}, nil
}

type subscription struct {
	service                       *Service
	worker                        *worker
	closeOnce                     sync.Once
	run, registration, generation string
	after                         uint64
	pending                       []Event
}

func (s *subscription) Close() error {
	s.closeOnce.Do(func() { s.service.releaseSubscription(s.worker, s.run) })
	return nil
}
func (s *subscription) Next(ctx context.Context) (serviceapi.ActivityFrame, error) {
	for {
		if err := ctx.Err(); err != nil {
			return serviceapi.ActivityFrame{}, err
		}
		if err := s.service.ctx.Err(); err != nil {
			return serviceapi.ActivityFrame{}, err
		}
		s.service.mu.Lock()
		fault := s.service.faults[s.run]
		s.service.mu.Unlock()
		if fault != nil {
			return serviceapi.ActivityFrame{}, apiError(fault)
		}
		if len(s.pending) > 0 {
			e := s.pending[0]
			s.pending = s.pending[1:]
			s.after = e.Ordinal
			data, _ := json.Marshal(e)
			return serviceapi.ActivityFrame{Ordinal: e.Ordinal, Data: data}, nil
		}
		events, generation, _, err := s.service.store.read(s.run, s.registration, s.after, MaxPageSize)
		if err != nil {
			return serviceapi.ActivityFrame{}, apiError(err)
		}
		if generation != s.generation {
			return serviceapi.ActivityFrame{}, serviceapi.ErrProjectionLineageChanged
		}
		s.pending = events
		if len(events) > 0 {
			continue
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return serviceapi.ActivityFrame{}, ctx.Err()
		case <-s.service.ctx.Done():
			timer.Stop()
			return serviceapi.ActivityFrame{}, ErrUnavailable
		case <-timer.C:
		}
	}
}

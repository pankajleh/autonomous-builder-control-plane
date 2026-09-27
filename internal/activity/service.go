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

func deferInitialProviderUnavailable(err error, previous *providerProof, misses int) bool {
	return errors.Is(err, ErrUnavailable) && previous == nil && misses < initialProviderUnavailableRetryBudget
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
	return &Service{store: store, resolver: Resolver{root, catalog}, snapshots: snapshots, cursors: cursors, ctx: ctx, cancel: cancel, workers: map[string]*worker{}, faults: map[string]error{}, sidecars: map[string]*sharedSidecar{}, now: time.Now, diagnostics: os.Stderr}, nil
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
func (s *Service) appendUnknown(run, registration, key, title string) error {
	_, err := s.store.Append(run, registration, unknown(run, key, title, s.now()))
	return err
}

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
	scope, err := s.resolver.Resolve(ctx, run)
	if err != nil {
		// Governed execution worktrees are intentionally removed after the run
		// reaches an authoritative terminal branch outcome. Provider detail can
		// no longer be refreshed at that point, but that expected cleanup is not
		// an integrity ambiguity and must not poison an otherwise clean accepted
		// checkpoint with a controller UNKNOWN marker. Any provider warning or
		// integrity marker observed before the terminal transition remains durable.
		if terminalActivityState(snapshot) {
			return Scope{}, nil
		}
		s.diagnose(run, "binding-unavailable", err)
		if err = s.appendUnknown(run, registration, "binding-unavailable", "Implementation detail unavailable"); err != nil {
			return Scope{}, err
		}
		return Scope{}, nil
	}
	if e, ok := checkpoint(ctx, scope, s.now()); ok {
		if _, err = s.store.Append(run, registration, e); err != nil {
			return Scope{}, err
		}
	}
	return scope, nil
}

func terminalActivityState(snapshot readmodel.Snapshot) bool {
	state := snapshot.Projection.CurrentState
	if state == "" {
		for i := len(snapshot.Events) - 1; i >= 0; i-- {
			if snapshot.Events[i].StateTo != "" {
				state = string(snapshot.Events[i].StateTo)
				break
			}
		}
	}
	switch state {
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
func (s *Service) releaseSubscription(w *worker) {
	s.mu.Lock()
	w.subscriptions--
	w.touched = s.now()
	s.mu.Unlock()
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
			}
			if shared.err != nil {
				s.diagnose(run, "sidecar-unavailable", shared.err)
				if s.appendUnknown(run, registration, "sidecar-unavailable", "Implementation detail unavailable") != nil {
					return
				}
				s.releaseSidecar(key)
				shared = nil
			} else {
				err = s.collectBatch(scope, registration, shared.sc, &last)
				if errors.Is(err, ErrIntegrity) {
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
					if deferInitialProviderUnavailable(err, previous, initialUnavailableMisses) {
						// A newly admitted run can briefly precede the provider session's
						// appearance in the sidecar. Retry a bounded number of times before
						// declaring provider detail unavailable; after the first verified
						// provider proof, any loss remains immediately fail-closed.
						initialUnavailableMisses++
					} else {
						s.diagnose(run, "provider-unavailable", err)
						if s.appendUnknown(run, registration, "provider-unavailable", "Implementation detail unavailable") != nil {
							return
						}
					}
				} else {
					initialUnavailableMisses = 0
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
	events := make([]Event, len(messages))
	position := *last
	for i, message := range messages {
		if position != math.MaxUint64 && message.id <= position {
			return atStep("ordering", ErrIntegrity)
		}
		events[i], err = normalizeProvider(scope.RunID, selected.ID, strconv.FormatUint(message.id, 10), message.payload, s.now())
		if err != nil {
			return atStep("normalize", err)
		}
		position = message.id
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
	if len(messages) == 0 && previous != nil && proof.Size > previous.Size {
		s.diagnose(scope.RunID, "provider-silent-gap", nil)
		if err = s.appendUnknown(scope.RunID, registration, identity("silent-gap", proof.Generation, proof.PrefixDigest), "Implementation detail replay gap"); err != nil {
			return err
		}
		*last = math.MaxUint64
	}
	for i, message := range messages {
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
			s.diagnose(scope.RunID, "provider-replay-gap", nil)
			gapKey := identity("gap", proof.Generation, strconv.FormatUint(*last, 10), strconv.FormatUint(message.id, 10))
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
		s.releaseSubscription(w)
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
	s.closeOnce.Do(func() { s.service.releaseSubscription(s.worker) })
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

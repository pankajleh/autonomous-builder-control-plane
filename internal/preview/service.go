package preview

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/activity"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type Runtime interface {
	Available(PreviewProfileV1) bool
	Start(context.Context, string, string, PreviewProfileV1) (string, error)
	Healthy(context.Context, string, PreviewProfileV1) (bool, error)
	// ResolveRoute looks up the exact preview ID and current handle without I/O.
	ResolveRoute(context.Context, string, string) (string, error)
	Stop(context.Context, string) error
	Reconcile(context.Context) error
}

// keptRuntime starts a preview with its app's kept data and forgets that data (Repo C design note TRY_IT_KEEPS_DATA).
type keptRuntime interface {
	StartKept(ctx context.Context, id, path string, p PreviewProfileV1, keyDigest string) (string, error)
	RemoveKept(ctx context.Context, keyDigest string) error
	// KeptKeys lists the kept-data keys whose volumes exist, each by its first 24 hex characters (the volume's name
	// carries no more).
	KeptKeys(ctx context.Context) (map[string]bool, error)
}

// KeptDataIdle is how long an app's kept Try it data stays without a Try it of the app (the owner, 2026-10-26: "90
// days is fine, go ahead"; Repo C design note TRY_IT_KEEPS_DATA §4). Start fresh and archiving the app clear it sooner.
const KeptDataIdle = 90 * 24 * time.Hour

// keptKey is the digest that names an app's kept Try it data; only it reaches the runtime.
func keptKey(task string) string { return jsonDigest([]string{"try-it-data", task}) }

type Service struct {
	mu                  sync.Mutex
	store               *store
	profiles            map[string]PreviewProfileV1
	resolver            SourceResolver
	checkout            Materializer
	runtime             Runtime
	ctx                 context.Context
	cancel              context.CancelFunc
	workers             map[string]context.CancelFunc
	keptLocks           map[string]*sync.Mutex
	wg                  sync.WaitGroup
	now                 func() time.Time
	closed, unavailable bool
}

// New wires only read interfaces to existing controller substrates. Missing
// host isolation leaves history/stop usable while capability remains false.
// Option changes how New builds the preview runtime.
type Option func(*dockerRuntime)

// WithAppRelay sends previews' relay requests to relay (BF-08b). A profile naming EgressAutobuildRelay is unavailable
// without it.
func WithAppRelay(relay AppRelay) Option {
	return func(d *dockerRuntime) { d.relay = &relay }
}

func New(parent context.Context, root, profileFile string, catalog activity.Catalog, events ActivityReader, evictions EvictionReader, options ...Option) (*Service, error) {
	profiles, err := LoadProfiles(profileFile)
	if err != nil {
		return nil, ErrUnavailable
	}
	private, err := privateDirectory(filepath.Join(root, "previews"), true)
	if err != nil {
		return nil, ErrUnavailable
	}
	private.Close()
	previewRoot := filepath.Join(root, "previews")
	runtime := newDocker(previewRoot)
	for _, option := range options {
		option(runtime)
	}
	s, err := newService(parent, previewRoot, profiles, Resolver{root, catalog, events, evictions}, Checkout{filepath.Join(previewRoot, "sources")}, runtime, time.Now)
	if err == nil && !s.unavailable {
		runtime.proveProfiles(parent, profiles)
	}
	return s, err
}
func newService(parent context.Context, root string, profiles map[string]PreviewProfileV1, resolver SourceResolver, checkout Materializer, runtime Runtime, now func() time.Time) (*Service, error) {
	if parent == nil || resolver == nil || checkout == nil || runtime == nil || now == nil {
		return nil, ErrUnavailable
	}
	store, err := openStore(root)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Service{store: store, profiles: profiles, resolver: resolver, checkout: checkout, runtime: runtime, ctx: ctx, cancel: cancel, workers: map[string]context.CancelFunc{}, keptLocks: map[string]*sync.Mutex{}, now: now}
	// Restart never resumes candidate execution. Reconcile all objects carrying
	// this private root's namespace, including objects created before a crash.
	cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 30*time.Second)
	runtimeErr := runtime.Reconcile(cleanCtx)
	cleanCancel()
	if runtimeErr != nil {
		s.unavailable = true
	} else if checkout.Reconcile() != nil {
		s.unavailable = true
	}
	if store.load() != nil {
		// Retain the lock and damaged files, but keep legacy service startup
		// independent of preview history recovery. No receipt may be replayed.
		s.unavailable = true
		return s, nil
	}
	for _, v := range store.records {
		if !terminal(v.Status) {
			v = ended(v, "FAILED", now())
			if !now().Before(parseTime(v.ExpiresAt)) {
				v.Status = "EXPIRED"
			}
			if store.append(v, nil) != nil {
				s.unavailable = true
			}
		}
	}
	return s, nil
}
func (s *Service) Available() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unavailable || s.store.broken || s.ctx.Err() != nil {
		return false
	}
	for _, p := range s.profiles {
		if s.runtime.Available(p) {
			return true
		}
	}
	return false
}

// ProfileAvailability tells, for each loaded preview profile, whether previews (or hosted instances) can start on it now.
// The runtime approves a profile once, at startup, by proving it from its pinned images, so a profile it could not prove
// (one of its images pruned from the host, say) stays unavailable until a restart. All are false while the service
// itself is unavailable.
func (s *Service) ProfileAvailability() map[string]bool {
	out := map[string]bool{}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	down := s.closed || s.unavailable || s.store.broken || s.ctx.Err() != nil
	for id, p := range s.profiles {
		out[id] = !down && s.runtime.Available(p)
	}
	return out
}
func (s *Service) replay(key, d string) (serviceapi.PreviewV1, bool, error) {
	if r, ok := s.store.receipts[key]; ok {
		if r.Digest != d {
			return serviceapi.PreviewV1{}, true, serviceapi.ErrRequestIDConflict
		}
		return r.Result, true, nil
	}
	return serviceapi.PreviewV1{}, false, nil
}
func (s *Service) CreatePreview(ctx context.Context, p serviceapi.Principal, authorityDigest, run string, c serviceapi.PreviewRequestV1) (serviceapi.PreviewV1, error) {
	empty := serviceapi.PreviewV1{}
	if serviceapi.ValidatePreviewRequestV1(c, run) != nil || !owner(p).valid() || !sha256Pattern.MatchString(authorityDigest) {
		return empty, serviceapi.ErrPreviewIneligible
	}
	receipt := createReceipt(p, authorityDigest, run, c)
	key, d := receipt.Key, receipt.Digest
	s.mu.Lock()
	if s.closed || s.store.broken {
		s.mu.Unlock()
		return empty, serviceapi.ErrPreviewUnavailable
	}
	if v, ok, err := s.replay(key, d); ok {
		s.mu.Unlock()
		return v, err
	}
	profile, ok := s.profiles[c.ProfileID]
	if !ok || profile.Hosted {
		s.mu.Unlock()
		return empty, serviceapi.ErrPreviewProfile
	}
	if s.unavailable || s.ctx.Err() != nil || !s.runtime.Available(profile) {
		s.mu.Unlock()
		return empty, serviceapi.ErrPreviewUnavailable
	}
	if len(s.workers) >= 8 {
		s.mu.Unlock()
		return empty, serviceapi.ErrPreviewUnavailable
	}
	s.mu.Unlock()
	source, err := s.resolver.Resolve(ctx, run, c.CheckpointActivityID)
	if err != nil || source.RunID != run || source.CheckpointActivityID != c.CheckpointActivityID || source.RepositoryIdentityDigest != profile.RepositoryIdentityDigest {
		return empty, serviceapi.ErrPreviewIneligible
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok, err := s.replay(key, d); ok {
		return v, err
	}
	if s.closed || s.unavailable || s.ctx.Err() != nil || len(s.workers) >= 8 {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	now := s.now().UTC()
	id := jsonDigest([]string{key, d})
	v := serviceapi.PreviewV1{SchemaVersion: "PreviewV1", PreviewID: id, Revision: s.store.revisions[run] + 1, RunID: run, CheckpointActivityID: c.CheckpointActivityID, SourceSHA: source.SHA, ProductAuthorizationID: source.ProductAuthorizationID, ProductTaskID: source.ProductTaskID, ProductVersionID: source.ProductVersionID, ProfileID: c.ProfileID, ProfileDigest: profile.Digest(), Status: "REQUESTED", Health: "UNKNOWN", CreatedAt: stamp(now), ExpiresAt: stamp(now.Add(time.Duration(profile.TTLSeconds) * time.Second)), ValidationID: source.ValidationID, EvidenceID: jsonDigest([]string{id, "REQUESTED"}), RequestID: c.RequestID, RequestDigest: d}
	receipt.Result = v
	if err = s.store.append(v, &receipt); err != nil {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	workerCtx, cancel := context.WithDeadline(s.ctx, parseTime(v.ExpiresAt))
	s.workers[id] = cancel
	s.wg.Add(1)
	go s.work(workerCtx, id, source, profile)
	return v, nil
}
func stamp(t time.Time) string     { return t.UTC().Format(time.RFC3339Nano) }
func parseTime(v string) time.Time { t, _ := time.Parse(time.RFC3339Nano, v); return t }
func ended(v serviceapi.PreviewV1, status string, now time.Time) serviceapi.PreviewV1 {
	v.Status = status
	v.Health = "UNKNOWN"
	v.RouteHandle = ""
	v.StoppedAt = stamp(now)
	v.EvidenceID = jsonDigest([]string{v.PreviewID, status, v.StoppedAt})
	return v
}
func (s *Service) transition(id, status, health, route string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.store.records[id]
	if terminal(v.Status) || s.closed || s.store.broken {
		return false
	}
	if !s.now().Before(parseTime(v.ExpiresAt)) {
		v = ended(v, "EXPIRED", s.now())
	} else if terminal(status) {
		v = ended(v, status, s.now())
	} else {
		v.Status = status
		v.Health = health
		v.RouteHandle = route
		v.EvidenceID = jsonDigest([]string{id, status, health, route})
	}
	if s.store.append(v, nil) != nil {
		s.unavailable = true
		return false
	}
	return !terminal(v.Status)
}
func (s *Service) work(ctx context.Context, id string, source Source, p PreviewProfileV1) {
	defer s.wg.Done()
	defer func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := s.runtime.Stop(cleanCtx, id)
		cancel()
		if err == nil {
			err = s.checkout.Remove(id)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			s.unavailable = true
		}
		if cancel := s.workers[id]; cancel != nil {
			cancel()
		}
		delete(s.workers, id)
	}()
	fail := func() {
		status := "FAILED"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = "EXPIRED"
		}
		s.transition(id, status, "UNKNOWN", "")
	}
	if !s.transition(id, "VALIDATING", "UNKNOWN", "") {
		return
	}
	exact, err := s.resolver.Resolve(ctx, source.RunID, source.CheckpointActivityID)
	if err != nil || exact != source {
		fail()
		return
	}
	path, err := s.checkout.Materialize(ctx, id, source)
	if err != nil {
		fail()
		return
	}
	exact, err = s.resolver.Resolve(ctx, source.RunID, source.CheckpointActivityID)
	if err != nil || exact != source {
		fail()
		return
	}
	if ctx.Err() != nil || !s.transition(id, "STARTING", "UNKNOWN", "") {
		fail()
		return
	}
	var route string
	if p.KeepData {
		// One preview of an app holds its kept data: the others end, and their containers are gone, first.
		kept, ok := s.runtime.(keptRuntime)
		if !ok || source.ProductTaskID == "" {
			fail()
			return
		}
		lock := s.keptLock(source.ProductTaskID)
		lock.Lock()
		if !s.releaseApp(id, source.ProductTaskID, false) || ctx.Err() != nil {
			lock.Unlock()
			fail()
			return
		}
		route, err = kept.StartKept(ctx, id, path, p, keptKey(source.ProductTaskID))
		lock.Unlock()
	} else {
		route, err = s.runtime.Start(ctx, id, path, p)
	}
	if err != nil || !sha256Pattern.MatchString(route) {
		fail()
		return
	}
	healthCtx, cancel := context.WithTimeout(ctx, time.Duration(p.HealthTimeoutSeconds)*time.Second)
	healthy := false
	for healthCtx.Err() == nil {
		ok, checkErr := s.runtime.Healthy(healthCtx, id, p)
		if checkErr != nil {
			cancel()
			fail()
			return
		}
		if ok {
			healthy = true
			break
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-healthCtx.Done():
		case <-timer.C:
		}
		timer.Stop()
	}
	cancel()
	if !healthy || !s.transition(id, "READY", "HEALTHY", route) {
		fail()
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fail()
			return
		case <-ticker.C:
			health := "DEGRADED"
			ok, checkErr := s.runtime.Healthy(ctx, id, p)
			if checkErr != nil {
				fail()
				return
			}
			if ok {
				health = "HEALTHY"
			}
			s.mu.Lock()
			current := s.store.records[id]
			s.mu.Unlock()
			if terminal(current.Status) {
				return
			}
			if !s.now().Before(parseTime(current.ExpiresAt)) {
				s.transition(id, "EXPIRED", "UNKNOWN", "")
				return
			}
			if health != current.Health && !s.transition(id, "READY", health, route) {
				return
			}
		}
	}
}

// Live reports whether any preview of the run is still running, so worktree
// eviction leaves its source in place until the preview ends or expires.
func (s *Service) Live(run string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store == nil {
		return false
	}
	now := s.now()
	for _, v := range s.store.records {
		if v.RunID != run || terminal(v.Status) {
			continue
		}
		if expires, err := time.Parse(time.RFC3339Nano, v.ExpiresAt); err == nil && !now.Before(expires) {
			continue
		}
		return true
	}
	return false
}

func (s *Service) ListPreviews(ctx context.Context, p serviceapi.Principal, run string) (serviceapi.PreviewListV1, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := serviceapi.PreviewListV1{SchemaVersion: "PreviewListV1", RunID: run, Previews: []serviceapi.PreviewV1{}}
	if s.closed || s.store.broken || ctx.Err() != nil {
		return out, serviceapi.ErrPreviewUnavailable
	}
	for _, v := range s.store.records {
		if v.RunID == run && owner(p).valid() && s.store.owners[v.PreviewID] == owner(p) {
			v = s.expire(v)
			out.Previews = append(out.Previews, v)
		}
	}
	if s.store.broken {
		return serviceapi.PreviewListV1{}, serviceapi.ErrPreviewUnavailable
	}
	sort.Slice(out.Previews, func(i, j int) bool { return out.Previews[i].Revision < out.Previews[j].Revision })
	return out, nil
}
func (s *Service) ReadPreview(ctx context.Context, p serviceapi.Principal, run, id string) (serviceapi.PreviewV1, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.store.broken || ctx.Err() != nil {
		return serviceapi.PreviewV1{}, serviceapi.ErrPreviewUnavailable
	}
	v, ok := s.store.records[id]
	if !ok || v.RunID != run || !owner(p).valid() || s.store.owners[id] != owner(p) {
		return serviceapi.PreviewV1{}, serviceapi.ErrDependencyNotFound
	}
	v = s.expire(v)
	if s.store.broken {
		return serviceapi.PreviewV1{}, serviceapi.ErrPreviewUnavailable
	}
	return v, nil
}
func (s *Service) ResolvePreviewRoute(ctx context.Context, p serviceapi.Principal, run, id string) (serviceapi.PreviewRouteV1, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	empty := serviceapi.PreviewRouteV1{}
	if s.closed || s.unavailable || s.store.broken || s.ctx.Err() != nil || ctx.Err() != nil {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	v, ok := s.store.records[id]
	if !ok || v.PreviewID != id || v.RunID != run || !owner(p).valid() || s.store.owners[id] != owner(p) {
		return empty, serviceapi.ErrDependencyNotFound
	}
	v = s.expire(v)
	if s.store.broken || v.Status != "READY" || serviceapi.ValidatePreviewV1(v) != nil {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	profile, ok := s.profiles[v.ProfileID]
	if !ok || profile.Digest() != v.ProfileDigest || !s.runtime.Available(profile) {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	// Keep ownership, lifecycle and handle stable until the runtime lookup is
	// complete. Stop and health transitions use this same lock.
	target, err := s.runtime.ResolveRoute(ctx, id, v.RouteHandle)
	if err != nil || ctx.Err() != nil || s.ctx.Err() != nil {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	v = s.expire(v)
	if s.store.broken || v.Status != "READY" {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	out := serviceapi.PreviewRouteV1{SchemaVersion: "PreviewRouteV1", RunID: v.RunID, PreviewID: v.PreviewID, RouteHandle: v.RouteHandle, ExpiresAt: v.ExpiresAt, TargetURL: target}
	if serviceapi.ValidatePreviewRouteV1(out) != nil {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	return out, nil
}

func (s *Service) StopPreview(ctx context.Context, p serviceapi.Principal, authorityDigest, run, id string, c serviceapi.PreviewStopRequestV1) (serviceapi.PreviewV1, error) {
	empty := serviceapi.PreviewV1{}
	if serviceapi.ValidatePreviewStopRequestV1(c, run, id) != nil || !owner(p).valid() || !sha256Pattern.MatchString(authorityDigest) {
		return empty, serviceapi.ErrPreviewIneligible
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.store.broken || ctx.Err() != nil {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	receipt := stopReceipt(p, authorityDigest, run, id, c)
	if v, ok, err := s.replay(receipt.Key, receipt.Digest); ok {
		return v, err
	}
	v, ok := s.store.records[id]
	if !ok || v.RunID != run || s.store.owners[id] != owner(p) {
		return empty, serviceapi.ErrDependencyNotFound
	}
	if !terminal(v.Status) {
		v = ended(v, "STOPPED", s.now())
	}
	receipt.Result = v
	if s.store.append(v, &receipt) != nil {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	if cancel := s.workers[id]; cancel != nil {
		cancel()
	}
	return v, nil
}

// ForgetPreviewData ends every live preview of the app that preview id belongs to and removes the app's kept Try it data
// (Repo C design note TRY_IT_KEEPS_DATA), so its next kept-data preview starts from an empty database. It answers that
// preview as it then is, always ended, and replays by request ID as a stop does.
func (s *Service) ForgetPreviewData(ctx context.Context, p serviceapi.Principal, authorityDigest, run, id string, c serviceapi.PreviewStopRequestV1) (serviceapi.PreviewV1, error) {
	empty := serviceapi.PreviewV1{}
	if serviceapi.ValidatePreviewStopRequestV1(c, run, id) != nil || !owner(p).valid() || !sha256Pattern.MatchString(authorityDigest) {
		return empty, serviceapi.ErrPreviewIneligible
	}
	kept, keeps := s.runtime.(keptRuntime)
	receipt := forgetReceipt(p, authorityDigest, run, id, c)
	s.mu.Lock()
	if s.closed || s.store.broken || ctx.Err() != nil {
		s.mu.Unlock()
		return empty, serviceapi.ErrPreviewUnavailable
	}
	if v, ok, err := s.replay(receipt.Key, receipt.Digest); ok {
		s.mu.Unlock()
		return v, err
	}
	v, found := s.store.records[id]
	if !found || v.RunID != run || s.store.owners[id] != owner(p) {
		s.mu.Unlock()
		return empty, serviceapi.ErrDependencyNotFound
	}
	task := v.ProductTaskID
	s.mu.Unlock()
	if !keeps || task == "" {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	lock := s.keptLock(task)
	lock.Lock()
	defer lock.Unlock()
	if !s.releaseApp("", task, true) {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	removeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := kept.RemoveKept(removeCtx, keptKey(task))
	cancel()
	if err != nil {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok, err := s.replay(receipt.Key, receipt.Digest); ok {
		return v, err
	}
	if s.closed || s.store.broken {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	v = s.store.records[id]
	if !terminal(v.Status) {
		v = ended(v, "STOPPED", s.now())
	}
	receipt.Result = v
	if s.store.append(v, &receipt) != nil {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	return v, nil
}

// keptLock serializes, for one app, starting a kept-data preview and forgetting its data.
func (s *Service) keptLock(task string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.keptLocks[task]
	if l == nil {
		l = &sync.Mutex{}
		s.keptLocks[task] = l
	}
	return l
}

// releaseApp ends the app's live previews but except (only kept-data ones unless all) and waits until each one's
// worker has stopped its containers, so the app's data volume is free. It reports false when one did not stop in time.
func (s *Service) releaseApp(except, task string, all bool) bool {
	s.mu.Lock()
	var held []string
	for id, v := range s.store.records {
		if id == except || terminal(v.Status) || v.ProductTaskID != task || !all && !s.profiles[v.ProfileID].KeepData {
			continue
		}
		if s.store.append(ended(v, "STOPPED", s.now()), nil) != nil {
			s.unavailable = true
		}
		if cancel := s.workers[id]; cancel != nil {
			cancel()
		}
		held = append(held, id)
	}
	s.mu.Unlock()
	deadline := time.Now().Add(60 * time.Second)
	for _, id := range held {
		for {
			s.mu.Lock()
			_, running := s.workers[id]
			s.mu.Unlock()
			if !running {
				break
			}
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(20 * time.Millisecond)
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := s.runtime.Stop(stopCtx, id)
		cancel()
		if err != nil {
			return false
		}
	}
	return true
}

// SweepKeptData removes the kept Try it data of each app whose newest kept-data preview started at least idle ago and
// that has no preview running, and returns how many apps' data it removed. A volume whose app it cannot name is left as
// it is; a volume in use is never removed.
func (s *Service) SweepKeptData(ctx context.Context, idle time.Duration) (int, error) {
	kept, keeps := s.runtime.(keptRuntime)
	if s == nil || !keeps || idle <= 0 {
		return 0, nil
	}
	newest, running, err := s.keptUse()
	if err != nil {
		return 0, err
	}
	present, err := kept.KeptKeys(ctx)
	if err != nil {
		return 0, ErrUnavailable
	}
	removed := 0
	for task, at := range newest {
		key := keptKey(task)
		if running[task] || s.now().Sub(at) < idle || !present[key[:24]] {
			continue
		}
		lock := s.keptLock(task)
		lock.Lock()
		// A preview of the app may have started while the volumes were listed.
		if _, now, err := s.keptUse(); err == nil && !now[task] && kept.RemoveKept(ctx, key) == nil {
			removed++
		}
		lock.Unlock()
	}
	return removed, nil
}

// keptUse is, for each app with kept-data previews, when its newest one started and whether one is running.
func (s *Service) keptUse() (map[string]time.Time, map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.store.broken || s.ctx.Err() != nil {
		return nil, nil, ErrUnavailable
	}
	newest, running := map[string]time.Time{}, map[string]bool{}
	for _, v := range s.store.records {
		p, ok := s.profiles[v.ProfileID]
		if !ok || !p.KeepData || v.ProductTaskID == "" {
			continue
		}
		if at := parseTime(v.CreatedAt); at.After(newest[v.ProductTaskID]) {
			newest[v.ProductTaskID] = at
		}
		if !terminal(v.Status) {
			running[v.ProductTaskID] = true
		}
	}
	return newest, running, nil
}

// SweepKeptDataEvery sweeps kept Try it data (SweepKeptData) a first time after first and then every every, until the
// service closes, telling each sweep to logf.
func (s *Service) SweepKeptDataEvery(idle, first, every time.Duration, logf func(format string, args ...any)) {
	if s == nil || every <= 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		timer := time.NewTimer(first)
		defer timer.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-timer.C:
			}
			removed, err := s.SweepKeptData(s.ctx, idle)
			if err != nil {
				logf("abcp kept try-it data sweep failed at=%s", s.now().UTC().Format(time.RFC3339))
			} else {
				logf("abcp kept try-it data sweep at=%s idle-days=%d removed=%d", s.now().UTC().Format(time.RFC3339), int(idle.Hours()/24), removed)
			}
			timer.Reset(every)
		}
	}()
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	var closeErr error
	for id, v := range s.store.records {
		if !s.store.broken && !terminal(v.Status) {
			closeErr = errors.Join(closeErr, s.store.append(ended(v, "STOPPED", s.now()), nil))
			if cancel := s.workers[id]; cancel != nil {
				cancel()
			}
		}
	}
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
	return errors.Join(closeErr, s.store.close())
}

// Call under mu so a read never presents a route after its deadline, even if
// a Docker health command is still returning from a canceled context.
func (s *Service) expire(v serviceapi.PreviewV1) serviceapi.PreviewV1 {
	if !terminal(v.Status) && !s.now().Before(parseTime(v.ExpiresAt)) {
		v = ended(v, "EXPIRED", s.now())
		if s.store.append(v, nil) != nil {
			s.unavailable = true
		}
		if cancel := s.workers[v.PreviewID]; cancel != nil {
			cancel()
		}
	}
	return v
}

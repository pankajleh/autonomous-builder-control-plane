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
	Stop(context.Context, string) error
	Reconcile(context.Context) error
}
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
	wg                  sync.WaitGroup
	now                 func() time.Time
	closed, unavailable bool
}

// New wires only read interfaces to existing controller substrates. Missing
// host isolation leaves history/stop usable while capability remains false.
func New(parent context.Context, root, profileFile string, catalog activity.Catalog, events ActivityReader) (*Service, error) {
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
	s, err := newService(parent, previewRoot, profiles, Resolver{root, catalog, events}, Checkout{filepath.Join(previewRoot, "sources")}, runtime, time.Now)
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
	s := &Service{store: store, profiles: profiles, resolver: resolver, checkout: checkout, runtime: runtime, ctx: ctx, cancel: cancel, workers: map[string]context.CancelFunc{}, now: now}
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
	if !ok {
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
	route, err := s.runtime.Start(ctx, id, path, p)
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

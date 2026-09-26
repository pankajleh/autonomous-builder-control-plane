//go:build linux

package preview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type staticResolver struct {
	source Source
	err    error
}

func (r staticResolver) Resolve(context.Context, string, string) (Source, error) {
	return r.source, r.err
}

type memoryCheckout struct {
	mu                      sync.Mutex
	made, removed           []string
	err                     error
	removeErr, reconcileErr error
	reconciled              bool
}

func (m *memoryCheckout) Materialize(_ context.Context, id string, _ Source) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.made = append(m.made, id)
	return "/private/" + id, m.err
}
func (m *memoryCheckout) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removed = append(m.removed, id)
	return m.removeErr
}
func (m *memoryCheckout) Reconcile() error { m.reconciled = true; return m.reconcileErr }

type memoryRuntime struct {
	mu                                         sync.Mutex
	available, healthy                         bool
	started, stopped                           []string
	startErr, reconcileErr, stopErr, healthErr error
	reconciled                                 bool
}

func (r *memoryRuntime) Available(PreviewProfileV1) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.available
}
func (r *memoryRuntime) Start(ctx context.Context, id, path string, p PreviewProfileV1) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, id)
	return strings.Repeat("d", 64), r.startErr
}
func (r *memoryRuntime) Healthy(context.Context, string, PreviewProfileV1) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.healthy, r.healthErr
}
func (r *memoryRuntime) Stop(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = append(r.stopped, id)
	return r.stopErr
}
func (r *memoryRuntime) Reconcile(context.Context) error { r.reconciled = true; return r.reconcileErr }

const testAuthorityDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func principal() serviceapi.Principal {
	return serviceapi.Principal{PrincipalID: "gateway", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "bearer-token-v1"}
}
func createCommand(id string) serviceapi.PreviewRequestV1 {
	return serviceapi.PreviewRequestV1{SchemaVersion: 1, RequestID: id, ExpectedRunID: "run-1", CheckpointActivityID: strings.Repeat("b", 64), ProfileID: "web-v1", DelegatedActor: serviceapi.DelegatedActorV1{SubjectID: "alice", SubjectType: serviceapi.PrincipalUser}}
}
func fixtureService(t *testing.T, rt *memoryRuntime) (*Service, *memoryCheckout) {
	t.Helper()
	return serviceAt(t, filepath.Join(t.TempDir(), "previews"), rt, time.Now)
}
func serviceAt(t *testing.T, root string, rt *memoryRuntime, now func() time.Time) (*Service, *memoryCheckout) {
	t.Helper()
	p := testProfile()
	m := &memoryCheckout{}
	source := Source{RunID: "run-1", CheckpointActivityID: strings.Repeat("b", 64), SHA: strings.Repeat("c", 40), Repository: "/private/repo", RepositoryIdentityDigest: p.RepositoryIdentityDigest, ProductAuthorizationID: "auth-1", ProductTaskID: "task-1", ProductVersionID: "version-1", ValidationID: strings.Repeat("e", 64)}
	s, err := newService(context.Background(), root, map[string]PreviewProfileV1{p.ProfileID: p}, staticResolver{source: source}, m, rt, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, m
}
func awaitPreview(t *testing.T, s *Service, id, status, health string) serviceapi.PreviewV1 {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		v, err := s.ReadPreview(context.Background(), principal(), "run-1", id)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status == status && (health == "" || v.Health == health) {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("preview %s %s; wanted %s %s", v.Status, v.Health, status, health)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func awaitCleanup(t *testing.T, s *Service) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		n := len(s.workers)
		s.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not clean up")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func TestLifecycleReplayHealthStopAndTerminalRetry(t *testing.T) {
	rt := &memoryRuntime{available: true, healthy: true}
	s, m := fixtureService(t, rt)
	c := createCommand("create-1")
	first, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", c)
	if err != nil {
		t.Fatal(err)
	}
	ready := awaitPreview(t, s, first.PreviewID, "READY", "HEALTHY")
	if ready.RouteHandle == "" {
		t.Fatal("ready has no opaque route")
	}
	replay, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", c)
	if err != nil || replay != first {
		t.Fatal("receipt changed", err)
	}
	changed := c
	changed.ProfileID = "another"
	if _, err = s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", changed); !errors.Is(err, serviceapi.ErrRequestIDConflict) {
		t.Fatal("body conflict", err)
	}
	rt.mu.Lock()
	rt.healthy = false
	rt.mu.Unlock()
	awaitPreview(t, s, first.PreviewID, "READY", "DEGRADED")
	stop := serviceapi.PreviewStopRequestV1{SchemaVersion: 1, RequestID: "stop-1", ExpectedRunID: "run-1", ExpectedPreviewID: first.PreviewID, DelegatedActor: c.DelegatedActor}
	stopped, err := s.StopPreview(context.Background(), principal(), testAuthorityDigest, "run-1", first.PreviewID, stop)
	if err != nil || stopped.Status != "STOPPED" || stopped.RouteHandle != "" {
		t.Fatal(stopped, err)
	}
	again, err := s.StopPreview(context.Background(), principal(), testAuthorityDigest, "run-1", first.PreviewID, stop)
	if err != nil || again != stopped {
		t.Fatal("stop replay changed", err)
	}
	stop.RequestID = c.RequestID
	if _, err = s.StopPreview(context.Background(), principal(), testAuthorityDigest, "run-1", first.PreviewID, stop); !errors.Is(err, serviceapi.ErrRequestIDConflict) {
		t.Fatal("cross-command conflict", err)
	}
	awaitCleanup(t, s)
	rt.mu.Lock()
	rt.healthy = true
	rt.mu.Unlock()
	c.RequestID = "retry-2"
	second, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", c)
	if err != nil || second.PreviewID == first.PreviewID || second.Revision != first.Revision+1 || second.SourceSHA != first.SourceSHA {
		t.Fatal("terminal retry identity", second, err)
	}
	awaitPreview(t, s, second.PreviewID, "READY", "")
	list, err := s.ListPreviews(context.Background(), principal(), "run-1")
	if err != nil || len(list.Previews) != 2 || list.Previews[0] != stopped {
		t.Fatal("history changed", list, err)
	}
	if _, err = s.ReadPreview(context.Background(), principal(), "other", first.PreviewID); !errors.Is(err, serviceapi.ErrDependencyNotFound) {
		t.Fatal("cross-run read", err)
	}
	s.Close()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.made) != 2 || len(m.removed) != 2 {
		t.Fatal("checkout cleanup", m)
	}
}
func TestLifecycleFailureExpiryAndUnavailable(t *testing.T) {
	for _, scenario := range []string{"failed", "expired", "unavailable", "wrong-repo", "unknown-profile"} {
		t.Run(scenario, func(t *testing.T) {
			rt := &memoryRuntime{available: true, healthy: true}
			if scenario == "failed" {
				rt.startErr = ErrUnavailable
			}
			if scenario == "unavailable" {
				rt.available = false
			}
			s, _ := fixtureService(t, rt)
			c := createCommand("request-1")
			if scenario == "expired" {
				p := s.profiles["web-v1"]
				p.TTLSeconds = 1
				s.profiles[p.ProfileID] = p
			}
			if scenario == "wrong-repo" {
				p := s.profiles["web-v1"]
				p.RepositoryIdentityDigest = strings.Repeat("f", 64)
				s.profiles[p.ProfileID] = p
			}
			if scenario == "unknown-profile" {
				c.ProfileID = "unknown"
			}
			v, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", c)
			if scenario == "unavailable" || scenario == "wrong-repo" || scenario == "unknown-profile" {
				if err == nil {
					t.Fatal("unsafe create accepted")
				}
				if len(s.store.records) != 0 || len(rt.started) != 0 {
					t.Fatal("unavailable create had effects")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			status := "FAILED"
			if scenario == "expired" {
				status = "EXPIRED"
			}
			end := awaitPreview(t, s, v.PreviewID, status, "")
			if end.StoppedAt == "" || end.RouteHandle != "" {
				t.Fatal("terminal route remains")
			}
			awaitCleanup(t, s)
		})
	}
}
func TestConcurrentRequestReceiptIsUnique(t *testing.T) {
	rt := &memoryRuntime{available: true, healthy: true}
	s, _ := fixtureService(t, rt)
	c := createCommand("concurrent")
	var wg sync.WaitGroup
	results := make(chan serviceapi.PreviewV1, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", c)
			if err != nil {
				t.Error(err)
			}
			results <- v
		}()
	}
	wg.Wait()
	close(results)
	var first serviceapi.PreviewV1
	for v := range results {
		if first.PreviewID == "" {
			first = v
		} else if first != v {
			t.Fatal("different replay receipt")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.store.records) != 1 || len(s.store.receipts) != 1 {
		t.Fatal("duplicate durable admission")
	}
}
func TestDurableRecoveryPreservesReceiptAndEndsInterruptedRuntime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "previews")
	rt := &memoryRuntime{available: true, healthy: true}
	s, _ := serviceAt(t, root, rt, time.Now)
	c := createCommand("persisted")
	v, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", c)
	if err != nil {
		t.Fatal(err)
	}
	awaitPreview(t, s, v.PreviewID, "READY", "")
	// Simulate process death after durable READY without writing a graceful
	// terminal event. Cancellation drains fake runtime resources before reopen.
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
	s.store.close()
	recovered, _ := serviceAt(t, root, &memoryRuntime{available: true, healthy: true}, time.Now)
	got, err := recovered.ReadPreview(context.Background(), principal(), "run-1", v.PreviewID)
	if err != nil || got.Status != "FAILED" || got.RouteHandle != "" {
		t.Fatal("restart resumed runtime", got, err)
	}
	replay, err := recovered.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", c)
	if err != nil || replay != v {
		t.Fatal("restart changed receipt", err)
	}
	c.RequestID = "after-restart"
	next, err := recovered.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", c)
	if err != nil || next.Revision != v.Revision+1 {
		t.Fatal("revision reused", err)
	}
}
func TestReadsExpireBeforeReturningRoute(t *testing.T) {
	var offset atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(offset.Load()) * time.Second) }
	s, _ := serviceAt(t, filepath.Join(t.TempDir(), "previews"), &memoryRuntime{available: true, healthy: true}, now)
	v, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", createCommand("expire-read"))
	if err != nil {
		t.Fatal(err)
	}
	awaitPreview(t, s, v.PreviewID, "READY", "")
	offset.Store(120)
	got, err := s.ReadPreview(context.Background(), principal(), "run-1", v.PreviewID)
	if err != nil || got.Status != "EXPIRED" || got.RouteHandle != "" {
		t.Fatal("expired route exposed", got, err)
	}
}
func TestProtectedProfilesAndDurableStorageFailClosed(t *testing.T) {
	p := testProfile()
	root := t.TempDir()
	file := filepath.Join(root, "profiles.json")
	writeJSON(t, file, ProfileFileV1{1, []PreviewProfileV1{p}})
	loaded, err := LoadProfiles(file)
	if err != nil || loaded[p.ProfileID].Digest() != p.Digest() {
		t.Fatal(err)
	}
	os.Chmod(file, 0644)
	if _, err = LoadProfiles(file); err == nil {
		t.Fatal("public profile accepted")
	}
	os.Chmod(file, 0600)
	link := filepath.Join(root, "link")
	os.Symlink(file, link)
	if _, err = LoadProfiles(link); err == nil {
		t.Fatal("symlink profile accepted")
	}
	data, _ := os.ReadFile(file)
	data = append(data[:len(data)-1], []byte(`,"unexpected":true}`)...)
	os.WriteFile(file, data, 0600)
	if _, err = LoadProfiles(file); err == nil {
		t.Fatal("unknown profile authority accepted")
	}
	storeRoot := filepath.Join(root, "store")
	s, err := openLoadedStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = openLoadedStore(storeRoot); err == nil {
		t.Fatal("second owner acquired journal")
	}
	s.close()
	os.WriteFile(filepath.Join(storeRoot, "history.jsonl"), []byte("{\n"), 0600)
	if _, err = openLoadedStore(storeRoot); err == nil {
		t.Fatal("torn journal accepted")
	}
}
func TestJournalRejectsTruncationAndTerminalRebinding(t *testing.T) {
	s, _ := fixtureService(t, &memoryRuntime{available: true, healthy: true})
	v, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", createCommand("journal"))
	if err != nil {
		t.Fatal(err)
	}
	awaitPreview(t, s, v.PreviewID, "READY", "")
	s.Close()
	data, _ := os.ReadFile(filepath.Join(s.store.root, "history.jsonl"))
	parts := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(parts) < 4 {
		t.Fatal("history not append-only")
	}
	reopened, err := openLoadedStore(s.store.root)
	if err != nil {
		t.Fatal(err)
	}
	old := reopened.records[v.PreviewID]
	changed := old
	changed.SourceSHA = strings.Repeat("f", 40)
	if err = reopened.append(changed, nil); err == nil {
		t.Fatal("terminal identity rebound")
	}
	reopened.close()
	truncated := strings.Join(parts[:len(parts)-1], "\n") + "\n"
	os.WriteFile(filepath.Join(s.store.root, "history.jsonl"), []byte(truncated), 0600)
	if _, err = openLoadedStore(s.store.root); err == nil {
		t.Fatal("tail deletion not detected")
	}
}

func TestNewCheckpointGetsNextRevisionAndRequestIDsBindTargets(t *testing.T) {
	s, _ := fixtureService(t, &memoryRuntime{available: true, healthy: true})
	c := createCommand("checkpoint-1")
	first, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", c)
	if err != nil {
		t.Fatal(err)
	}
	awaitPreview(t, s, first.PreviewID, "READY", "")
	other := c
	other.ExpectedRunID = "run-2"
	if _, err = s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-2", other); !errors.Is(err, serviceapi.ErrRequestIDConflict) {
		t.Fatal("request rebound to another run", err)
	}
	nextSource := s.resolver.(staticResolver).source
	nextSource.SHA = strings.Repeat("f", 40)
	nextSource.CheckpointActivityID = strings.Repeat("a", 64)
	s.resolver = staticResolver{source: nextSource}
	c.RequestID = "checkpoint-2"
	c.CheckpointActivityID = nextSource.CheckpointActivityID
	second, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", c)
	if err != nil || second.Revision != first.Revision+1 || second.PreviewID == first.PreviewID {
		t.Fatal("revision not advanced", second, err)
	}
	awaitPreview(t, s, second.PreviewID, "READY", "")
	historical, err := s.ReadPreview(context.Background(), principal(), "run-1", first.PreviewID)
	if err != nil || historical.SourceSHA != first.SourceSHA {
		t.Fatal("previous source identity rebound")
	}
}

// Tests that inspect journal recovery need both ownership and validated history.
func openLoadedStore(root string) (*store, error) {
	s, err := openStore(root)
	if err != nil {
		return nil, err
	}
	if err = s.load(); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

type delayedRuntime struct {
	*memoryRuntime
	entered chan context.Context
	release chan struct{}
}

func (r *delayedRuntime) Start(ctx context.Context, id, path string, p PreviewProfileV1) (string, error) {
	r.entered <- ctx
	<-r.release
	// Model a runtime that completes just as cancellation races its return.
	return r.memoryRuntime.Start(context.Background(), id, path, p)
}
func TestStopAndExpiryDuringStartupCannotPublishLateRoute(t *testing.T) {
	for _, scenario := range []string{"stop", "expiry"} {
		t.Run(scenario, func(t *testing.T) {
			rt := &memoryRuntime{available: true, healthy: true}
			s, _ := fixtureService(t, rt)
			delayed := &delayedRuntime{rt, make(chan context.Context, 1), make(chan struct{})}
			s.runtime = delayed
			var release sync.Once
			defer release.Do(func() { close(delayed.release) })
			if scenario == "expiry" {
				p := s.profiles["web-v1"]
				p.TTLSeconds = 1
				s.profiles[p.ProfileID] = p
			}
			v, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", createCommand("startup-race"))
			if err != nil {
				t.Fatal(err)
			}
			var ctx context.Context
			select {
			case ctx = <-delayed.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("runtime did not enter Start")
			}
			status := "EXPIRED"
			if scenario == "stop" {
				status = "STOPPED"
				stop := serviceapi.PreviewStopRequestV1{SchemaVersion: 1, RequestID: "stop-race", ExpectedRunID: "run-1", ExpectedPreviewID: v.PreviewID, DelegatedActor: createCommand("").DelegatedActor}
				if _, err = s.StopPreview(context.Background(), principal(), testAuthorityDigest, "run-1", v.PreviewID, stop); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("worker was not canceled")
			}
			release.Do(func() { close(delayed.release) })
			terminal := awaitPreview(t, s, v.PreviewID, status, "")
			awaitCleanup(t, s)
			got, err := s.ReadPreview(context.Background(), principal(), "run-1", v.PreviewID)
			if err != nil || got != terminal || got.RouteHandle != "" || !s.Available() {
				t.Fatal("late runtime result changed terminal identity", got, err)
			}
		})
	}
}
func TestCleanupAndRecoveryFailuresDisableAdmission(t *testing.T) {
	for _, scenario := range []string{"runtime-stop", "source-remove", "runtime-reconcile", "source-reconcile", "health-integrity"} {
		t.Run(scenario, func(t *testing.T) {
			rt := &memoryRuntime{available: true, healthy: true}
			if scenario == "runtime-reconcile" {
				rt.reconcileErr = ErrUnavailable
			}
			if scenario == "runtime-stop" {
				rt.stopErr = ErrUnavailable
			}
			s, m := fixtureService(t, rt)
			if scenario == "source-remove" {
				m.removeErr = ErrUnavailable
			}
			if scenario == "source-reconcile" {
				s.Close()
				m = &memoryCheckout{reconcileErr: ErrUnavailable}
				var err error
				s, err = newService(context.Background(), filepath.Join(t.TempDir(), "previews"), s.profiles, s.resolver, m, rt, time.Now)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
			}
			if !strings.HasSuffix(scenario, "reconcile") {
				v, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", createCommand("cleanup"))
				if err != nil {
					t.Fatal(err)
				}
				awaitPreview(t, s, v.PreviewID, "READY", "")
				if scenario == "health-integrity" {
					rt.mu.Lock()
					rt.healthErr = ErrIntegrity
					rt.available = false
					rt.mu.Unlock()
					awaitPreview(t, s, v.PreviewID, "FAILED", "")
				} else {
					stop := serviceapi.PreviewStopRequestV1{SchemaVersion: 1, RequestID: "stop-cleanup", ExpectedRunID: "run-1", ExpectedPreviewID: v.PreviewID, DelegatedActor: createCommand("").DelegatedActor}
					if _, err = s.StopPreview(context.Background(), principal(), testAuthorityDigest, "run-1", v.PreviewID, stop); err != nil {
						t.Fatal(err)
					}
				}
				awaitCleanup(t, s)
			}
			if s.Available() {
				t.Fatal("failed cleanup remains available")
			}
			if _, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", createCommand("after-failure")); !errors.Is(err, serviceapi.ErrPreviewUnavailable) {
				t.Fatal("admitted after failure", err)
			}
			if scenario == "runtime-stop" && len(m.removed) != 0 {
				t.Fatal("removed source while runtime cleanup failed")
			}
			if scenario == "runtime-reconcile" && m.reconciled {
				t.Fatal("removed orphan source while runtime reconciliation failed")
			}
		})
	}
}
func TestDamagedJournalStillReconcilesExclusivelyOwnedObjects(t *testing.T) {
	for _, damage := range []string{"torn-frame", "wrong-anchor"} {
		t.Run(damage, func(t *testing.T) {
			s, _ := fixtureService(t, &memoryRuntime{available: true, healthy: true})
			v, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", createCommand("before-crash"))
			if err != nil {
				t.Fatal(err)
			}
			awaitPreview(t, s, v.PreviewID, "READY", "")
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			root := s.store.root
			file := filepath.Join(root, "history.jsonl")
			if damage == "wrong-anchor" {
				file = filepath.Join(root, "head")
			}
			original, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			damaged := append(original, []byte("torn")...)
			if err = os.WriteFile(file, damaged, 0600); err != nil {
				t.Fatal(err)
			}
			sourceRoot := filepath.Join(root, "sources")
			if err = os.Mkdir(sourceRoot, 0700); err != nil {
				t.Fatal(err)
			}
			orphan := filepath.Join(sourceRoot, v.PreviewID)
			if err = os.Mkdir(orphan, 0755); err != nil {
				t.Fatal(err)
			}
			f := newDockerFixture(t, testProfile())
			f.d.root = root
			f.containers["orphan"] = strings.Repeat("a", 64)
			f.network = true
			recovered, err := newService(context.Background(), root, s.profiles, s.resolver, Checkout{sourceRoot}, f.d, time.Now)
			if err != nil {
				t.Fatal("damaged preview prevented service startup", err)
			}
			defer recovered.Close()
			if recovered.Available() || len(f.containers) != 0 || f.network {
				t.Fatal("damaged journal bypassed cleanup or enabled admission")
			}
			if _, err = os.Stat(orphan); !os.IsNotExist(err) {
				t.Fatal("orphan source remains", err)
			}
			if _, err = recovered.ReadPreview(context.Background(), principal(), "run-1", v.PreviewID); !errors.Is(err, serviceapi.ErrPreviewUnavailable) {
				t.Fatal("untrusted partial history readable", err)
			}
			if _, err = recovered.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", createCommand("before-crash")); !errors.Is(err, serviceapi.ErrPreviewUnavailable) {
				t.Fatal("damaged receipt replayed", err)
			}
			after, err := os.ReadFile(file)
			if err != nil || string(after) != string(damaged) {
				t.Fatal("damaged history changed", err)
			}
			// A second process must not reconcile resources belonging to the live owner.
			other := &memoryRuntime{available: true}
			checkout := &memoryCheckout{}
			if second, err := newService(context.Background(), root, s.profiles, s.resolver, checkout, other, time.Now); err == nil {
				second.Close()
				t.Fatal("second owner admitted")
			}
			if other.reconciled || checkout.reconciled {
				t.Fatal("cleanup ran without namespace ownership")
			}
		})
	}
}

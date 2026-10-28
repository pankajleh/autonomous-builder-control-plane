package stackjob

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/preview"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// Sandbox runs a part's steps: the preview runtime's stack sandbox on the build host, a fake in tests.
type Sandbox interface {
	Open(ctx context.Context, id string, spec preview.StackSpec, work, cache string) (Session, error)
	Prove(ctx context.Context, id string, spec preview.StackSpec, work, cache string) bool
}

// Session is one part's container.
type Session interface {
	Proxy() netip.AddrPort
	Exec(ctx context.Context, dir string, env, argv []string, out io.Writer) (int, error)
	Close() error
}

// PreviewSandbox is the preview runtime's stack sandbox.
type PreviewSandbox struct{ Service *preview.Service }

func (p PreviewSandbox) Open(ctx context.Context, id string, spec preview.StackSpec, work, cache string) (Session, error) {
	x, err := p.Service.OpenStack(ctx, id, spec, work, cache)
	if err != nil {
		return nil, err
	}
	return x, nil
}

func (p PreviewSandbox) Prove(ctx context.Context, id string, spec preview.StackSpec, work, cache string) bool {
	return p.Service.ProveStack(ctx, id, spec, work, cache)
}

// Job and step states, and the codes a job that did not pass ends with.
const (
	Queued, Running, Passed, Failed, Stopped, Unknown = "QUEUED", "RUNNING", "PASSED", "FAILED", "STOPPED", "UNKNOWN"
	StepWaiting, StepSkipped, StepNotRun              = "WAITING", "SKIPPED", "NOT_RUN"

	FailNoStack     = "NO_STACK"
	FailUnsupported = "UNSUPPORTED_STACK"
	FailSource      = "SOURCE_REFUSED"
	FailInstall     = "INSTALL_FAILED"
	FailBuild       = "BUILD_FAILED"
	FailTests       = "TESTS_FAILED"
	FailTimedOut    = "TIMED_OUT"
	FailTooLarge    = "TOO_LARGE"
	FailStopped     = "STOPPED"
	FailSandbox     = "SANDBOX_UNAVAILABLE"
)

const (
	maxQueued     = 20
	keptJobs      = 1000
	outputBytes   = 64 << 10
	outputLines   = 200
	cacheBytes    = 2 << 30
	cachesBytes   = 10 << 30
	freeDiskBytes = 10 << 30
	probeRetry    = time.Hour
)

var (
	keyPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// record is one job as kept on disk: the job as answered, and what it was asked.
type record struct {
	Job        serviceapi.StackJobV1 `json:"job"`
	BodyDigest string                `json:"body_digest"`
	Source     string                `json:"source"`
	CacheKey   string                `json:"cache_key"`
}

type probe struct {
	ok bool
	at time.Time
}

// Manager runs stack jobs one at a time.
type Manager struct {
	root, sources string
	profiles      ProfilesV1
	sandbox       Sandbox
	now           func() time.Time
	// freeDisk answers the bytes free where jobs work; statfs on the build host.
	freeDisk func(string) (uint64, bool)

	mu        sync.Mutex
	records   map[string]*record
	byRequest map[string]string
	queue     []string
	running   string
	cancel    context.CancelFunc
	probes    map[string]probe
	wake      chan struct{}
	ctx       context.Context
	stop      context.CancelFunc
	wg        sync.WaitGroup
}

// New opens the jobs kept under root, marks any left running UNKNOWN, removes what they left, and starts the worker.
func New(root, sources string, profiles ProfilesV1, sandbox Sandbox, now func() time.Time) (*Manager, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(sources) || filepath.Clean(root) != root || filepath.Clean(sources) != sources || profiles.Validate() != nil ||
		sandbox == nil || now == nil {
		return nil, serviceapi.ErrStackJobsUnavailable
	}
	if info, err := os.Lstat(sources); err != nil || !info.IsDir() {
		return nil, serviceapi.ErrStackJobsUnavailable
	}
	for _, dir := range []string{root, filepath.Join(root, "jobs"), filepath.Join(root, "work"), filepath.Join(root, "caches"), filepath.Join(root, "probe")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, serviceapi.ErrStackJobsUnavailable
		}
	}
	m := &Manager{root: root, sources: sources, profiles: profiles, sandbox: sandbox, now: now, freeDisk: statfsFree,
		records: map[string]*record{}, byRequest: map[string]string{}, probes: map[string]probe{}, wake: make(chan struct{}, 1)}
	if err := m.load(); err != nil {
		return nil, err
	}
	m.ctx, m.stop = context.WithCancel(context.Background())
	m.wg.Add(1)
	go m.work()
	return m, nil
}

func statfsFree(path string) (uint64, bool) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}

// Close stops the worker, ending a running job as STOPPED.
func (m *Manager) Close() {
	m.stop()
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) load() error {
	entries, err := os.ReadDir(filepath.Join(m.root, "jobs"))
	if err != nil {
		return serviceapi.ErrStackJobsUnavailable
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(m.root, "jobs", e.Name()))
		var r record
		if err != nil || json.Unmarshal(data, &r) != nil || !keyPattern.MatchString(r.Job.ID) || e.Name() != r.Job.ID+".json" {
			continue
		}
		if r.Job.State == Queued || r.Job.State == Running {
			r.Job.State, r.Job.EndedAt = Unknown, m.stamp()
		}
		m.records[r.Job.ID] = &r
		m.byRequest[r.Job.RequestID] = r.Job.ID
		if err := m.save(&r); err != nil {
			return err
		}
	}
	work, _ := os.ReadDir(filepath.Join(m.root, "work"))
	for _, e := range work {
		_ = os.RemoveAll(filepath.Join(m.root, "work", e.Name()))
	}
	m.prune()
	return nil
}

func (m *Manager) stamp() string { return m.now().UTC().Format(time.RFC3339Nano) }

// save writes a record whole, then renames it into place.
func (m *Manager) save(r *record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return serviceapi.ErrStackJobsUnavailable
	}
	path := filepath.Join(m.root, "jobs", r.Job.ID+".json")
	if os.WriteFile(path+".tmp", data, 0o600) != nil || os.Rename(path+".tmp", path) != nil {
		return serviceapi.ErrStackJobsUnavailable
	}
	return nil
}

// prune keeps the newest keptJobs ended jobs.
func (m *Manager) prune() {
	ended := []*record{}
	for _, r := range m.records {
		if r.Job.State != Queued && r.Job.State != Running {
			ended = append(ended, r)
		}
	}
	if len(ended) <= keptJobs {
		return
	}
	sort.Slice(ended, func(i, j int) bool { return ended[i].Job.CreatedAt < ended[j].Job.CreatedAt })
	for _, r := range ended[:len(ended)-keptJobs] {
		delete(m.records, r.Job.ID)
		if m.byRequest[r.Job.RequestID] == r.Job.ID {
			delete(m.byRequest, r.Job.RequestID)
		}
		_ = os.Remove(filepath.Join(m.root, "jobs", r.Job.ID+".json"))
	}
}

func copyJob(j serviceapi.StackJobV1) serviceapi.StackJobV1 {
	data, _ := json.Marshal(j)
	var out serviceapi.StackJobV1
	_ = json.Unmarshal(data, &out)
	return out
}

func bodyDigest(r serviceapi.StackJobCreateV1) string {
	data, _ := json.Marshal([]string{r.Source, r.CacheKey})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// StartStackJob queues a job, or answers the job its request ID already started.
func (m *Manager) StartStackJob(_ context.Context, req serviceapi.StackJobCreateV1) (serviceapi.StackJobV1, error) {
	if !req.Valid() {
		return serviceapi.StackJobV1{}, serviceapi.ErrStackJobNotAccepted
	}
	digest := bodyDigest(req)
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.byRequest[req.RequestID]; ok {
		r := m.records[id]
		if r.BodyDigest != digest {
			return serviceapi.StackJobV1{}, serviceapi.ErrStackJobConflict
		}
		return copyJob(r.Job), nil
	}
	if info, err := os.Lstat(filepath.Join(m.sources, req.Source)); err != nil || !info.IsDir() {
		return serviceapi.StackJobV1{}, serviceapi.ErrStackJobNotAccepted
	}
	if len(m.queue) >= maxQueued {
		return serviceapi.StackJobV1{}, serviceapi.ErrStackJobsBusy
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return serviceapi.StackJobV1{}, serviceapi.ErrStackJobsUnavailable
	}
	r := &record{BodyDigest: digest, Source: req.Source, CacheKey: req.CacheKey, Job: serviceapi.StackJobV1{
		SchemaVersion: serviceapi.StackJobSchemaV1, ID: hex.EncodeToString(raw), RequestID: req.RequestID, State: Queued,
		CreatedAt: m.stamp(), Parts: []serviceapi.StackPartV1{}}}
	if err := m.save(r); err != nil {
		return serviceapi.StackJobV1{}, err
	}
	m.records[r.Job.ID] = r
	m.byRequest[req.RequestID] = r.Job.ID
	m.queue = append(m.queue, r.Job.ID)
	m.prune()
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return copyJob(r.Job), nil
}

// StackJob answers one job.
func (m *Manager) StackJob(_ context.Context, id string) (serviceapi.StackJobV1, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return serviceapi.StackJobV1{}, serviceapi.ErrStackJobNotFound
	}
	return copyJob(r.Job), nil
}

// StopStackJob stops a queued job at once, and a running one at its next moment; an ended job is answered as it is.
func (m *Manager) StopStackJob(_ context.Context, id string) (serviceapi.StackJobV1, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return serviceapi.StackJobV1{}, serviceapi.ErrStackJobNotFound
	}
	switch {
	case r.Job.State == Queued:
		for i, q := range m.queue {
			if q == id {
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				break
			}
		}
		r.Job.State, r.Job.Failure, r.Job.EndedAt = Stopped, FailStopped, m.stamp()
		if err := m.save(r); err != nil {
			return serviceapi.StackJobV1{}, err
		}
	case r.Job.State == Running && m.running == id && m.cancel != nil:
		m.cancel()
	}
	return copyJob(r.Job), nil
}

func (m *Manager) work() {
	defer m.wg.Done()
	for {
		m.mu.Lock()
		var id string
		if len(m.queue) > 0 && m.ctx.Err() == nil {
			id, m.queue = m.queue[0], m.queue[1:]
		}
		m.mu.Unlock()
		if id == "" {
			select {
			case <-m.ctx.Done():
				return
			case <-m.wake:
			}
			continue
		}
		m.run(id)
	}
}

// update changes the running job under the lock and keeps it.
func (m *Manager) update(r *record, change func(*serviceapi.StackJobV1)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	change(&r.Job)
	_ = m.save(r)
}

func partID(job string, n int, salt string) string {
	sum := sha256.Sum256([]byte(job + "\x00" + salt + "\x00" + strconv.Itoa(n)))
	return hex.EncodeToString(sum[:])
}

func (m *Manager) run(id string) {
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	m.mu.Lock()
	r := m.records[id]
	m.running, m.cancel = id, cancel
	r.Job.State, r.Job.StartedAt = Running, m.stamp()
	_ = m.save(r)
	m.mu.Unlock()
	work := filepath.Join(m.root, "work", id)
	failure := m.runJob(ctx, r, work)
	_ = os.RemoveAll(work)
	m.update(r, func(j *serviceapi.StackJobV1) {
		switch {
		case failure == "":
			j.State = Passed
		case failure == FailStopped:
			j.State, j.Failure = Stopped, failure
		default:
			j.State, j.Failure = Failed, failure
		}
		j.EndedAt = m.stamp()
		// A step the job never reached says so.
		for p := range j.Parts {
			for s := range j.Parts[p].Steps {
				if st := &j.Parts[p].Steps[s]; st.State == StepWaiting || st.State == Running {
					st.State = StepNotRun
				}
			}
		}
	})
	m.tidyCaches(r.CacheKey)
	m.mu.Lock()
	m.running, m.cancel = "", nil
	m.mu.Unlock()
}

// runJob copies the code, finds its parts and runs each, answering the job's failure code or "".
func (m *Manager) runJob(ctx context.Context, r *record, work string) string {
	if free, ok := m.freeDisk(m.root); !ok || free < freeDiskBytes {
		return FailSandbox
	}
	if copySource(filepath.Join(m.sources, r.Source), work) != nil {
		return FailSource
	}
	if os.Mkdir(filepath.Join(work, ".abcp"), 0o755) != nil {
		return FailSandbox
	}
	parts := Detect(work, m.profiles.Available())
	if len(parts) == 0 {
		return FailNoStack
	}
	m.update(r, func(j *serviceapi.StackJobV1) {
		j.Parts = []serviceapi.StackPartV1{}
		for n, p := range parts {
			part := serviceapi.StackPartV1{Path: p.Dir, Stack: p.Stack, Found: p.Found, Version: p.Version, VersionNote: p.VersionNote, Tool: p.Tool,
				Steps: []serviceapi.StackStepV1{}}
			for _, s := range Steps(p, n) {
				part.Steps = append(part.Steps, serviceapi.StackStepV1{Name: s.Name, State: StepWaiting, Command: s.Show})
			}
			j.Parts = append(j.Parts, part)
		}
	})
	cache := filepath.Join(m.root, "caches", r.CacheKey)
	if os.MkdirAll(filepath.Join(cache, "home"), 0o755) != nil {
		return FailSandbox
	}
	now := m.now()
	_ = os.Chtimes(cache, now, now)
	failure, ran := "", false
	for n, p := range parts {
		if p.Stack == StackUnsupported {
			continue
		}
		ran = true
		if f := m.runPart(ctx, r, n, p, work, cache); f != "" && failure == "" {
			failure = f
		}
		if failure == FailStopped || failure == FailSandbox {
			break
		}
	}
	if !ran {
		return FailUnsupported
	}
	return failure
}

func (m *Manager) proved(ctx context.Context, profile ProfileV1) bool {
	m.mu.Lock()
	p, ok := m.probes[profile.ProfileID]
	m.mu.Unlock()
	if ok && (p.ok || m.now().Sub(p.at) < probeRetry) {
		return p.ok
	}
	dir := filepath.Join(m.root, "probe", profile.ProfileID)
	_ = os.RemoveAll(dir)
	result := os.MkdirAll(filepath.Join(dir, "work"), 0o755) == nil && os.MkdirAll(filepath.Join(dir, "cache"), 0o755) == nil &&
		m.sandbox.Prove(ctx, partID(profile.ProfileID, 0, "probe "+m.stamp()), profile.Spec(), filepath.Join(dir, "work"), filepath.Join(dir, "cache"))
	_ = os.RemoveAll(dir)
	m.mu.Lock()
	m.probes[profile.ProfileID] = probe{ok: result, at: m.now()}
	m.mu.Unlock()
	return result
}

// failureOf is the job's code for a step that failed.
var failureOf = map[string]string{"install": FailInstall, "build": FailBuild, "test": FailTests}

func (m *Manager) runPart(ctx context.Context, r *record, n int, p Part, work, cache string) string {
	profile, ok := m.profiles.For(p.Stack, p.Version)
	if !ok || !m.proved(ctx, profile) {
		return FailSandbox
	}
	session, err := m.sandbox.Open(ctx, partID(r.Job.ID, n, "part"), profile.Spec(), work, cache)
	if err != nil {
		return FailSandbox
	}
	defer session.Close()
	if p.Stack == StackJava {
		home := filepath.Join(cache, "home")
		if os.MkdirAll(filepath.Join(home, ".m2"), 0o755) != nil || os.MkdirAll(filepath.Join(home, ".gradle"), 0o755) != nil ||
			os.WriteFile(filepath.Join(home, ".m2", "settings.xml"), []byte(mavenSettings(session.Proxy())), 0o644) != nil ||
			os.WriteFile(filepath.Join(home, ".gradle", "gradle.properties"), []byte(gradleProperties(session.Proxy())), 0o644) != nil {
			return FailSandbox
		}
	}
	env := Env(p, profile, session.Proxy())
	partCtx, cancel := context.WithTimeout(ctx, time.Duration(profile.TimeoutSeconds)*time.Second)
	defer cancel()
	failure := ""
	for i, s := range Steps(p, n) {
		set := func(change func(*serviceapi.StackStepV1)) {
			m.update(r, func(j *serviceapi.StackJobV1) { change(&j.Parts[n].Steps[i]) })
		}
		switch {
		case s.Skip != "":
			set(func(st *serviceapi.StackStepV1) { st.State, st.Note = StepSkipped, s.Skip })
			continue
		case failure != "":
			set(func(st *serviceapi.StackStepV1) { st.State = StepNotRun })
			continue
		}
		set(func(st *serviceapi.StackStepV1) { st.State = Running })
		out := newTail(outputBytes)
		stepCtx, stepCancel := context.WithTimeout(partCtx, s.Timeout)
		started := time.Now()
		code, err := session.Exec(stepCtx, s.Dir, env, s.Argv, out)
		timedOut := stepCtx.Err() == context.DeadlineExceeded
		stepCancel()
		seconds := time.Since(started).Seconds()
		state, note := Passed, ""
		switch {
		case ctx.Err() != nil:
			state, failure = Stopped, FailStopped
		case timedOut || partCtx.Err() != nil:
			state, failure, note = Failed, FailTimedOut, "took longer than allowed"
		case err != nil:
			state, failure = Failed, FailSandbox
		case code == 0:
		case s.NoTests != 0 && code == s.NoTests:
			state, note = StepSkipped, "no tests were found"
		default:
			state, failure = Failed, failureOf[s.Name]
		}
		var tests *serviceapi.StackTestsV1
		if s.JUnit && state != Stopped {
			extra := []string{}
			if p.Stack == StackPython {
				extra = append(extra, filepath.Join(work, ".abcp", filepath.Base(pytestReport(n))))
			}
			if passed, failed, skipped, ok := Counts(filepath.Join(work, filepath.FromSlash(p.Dir)), extra...); ok {
				tests = &serviceapi.StackTestsV1{Passed: passed, Failed: failed, Skipped: skipped}
			}
		}
		if failure == "" && folderSize(work, profile.WorkBytes) > profile.WorkBytes {
			state, failure, note = Failed, FailTooLarge, "the code grew past what a check may write"
		}
		exit := code
		set(func(st *serviceapi.StackStepV1) {
			st.State, st.Note, st.Seconds, st.OutputTail, st.Tests = state, note, float64(int(seconds*10))/10, out.Lines(outputLines), tests
			if err == nil {
				st.ExitCode = &exit
			}
		})
		if failure == FailStopped {
			break
		}
	}
	return failure
}

// tidyCaches empties a cache past its limit, and removes the least recently used caches while all of them are past
// theirs, never the one just used.
func (m *Manager) tidyCaches(current string) {
	dir := filepath.Join(m.root, "caches")
	if folderSize(filepath.Join(dir, current), cacheBytes) > cacheBytes {
		_ = os.RemoveAll(filepath.Join(dir, current))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type cache struct {
		name string
		used time.Time
		size int64
	}
	caches, total := []cache{}, int64(0)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !e.IsDir() || !keyPattern.MatchString(e.Name()) {
			continue
		}
		size := folderSize(filepath.Join(dir, e.Name()), cachesBytes)
		caches = append(caches, cache{e.Name(), info.ModTime(), size})
		total += size
	}
	sort.Slice(caches, func(i, j int) bool { return caches[i].used.Before(caches[j].used) })
	for _, c := range caches {
		if total <= cachesBytes {
			break
		}
		if c.name == current {
			continue
		}
		if os.RemoveAll(filepath.Join(dir, c.name)) == nil {
			total -= c.size
		}
	}
}

var _ serviceapi.StackJobController = (*Manager)(nil)

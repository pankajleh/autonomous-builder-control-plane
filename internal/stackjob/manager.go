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
	// FailScan: a scan's scanner failed, or its result is missing or does not check (SECURITY_AND_COMPLIANCE §11.1).
	FailScan = "SCAN_FAILED"
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
	Scan       bool                  `json:"scan,omitempty"`
	Stacks     []string              `json:"stacks,omitempty"`
	Versions   map[string]string     `json:"versions,omitempty"`
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

	probe     *prober
	mu        sync.Mutex
	records   map[string]*record
	byRequest map[string]string
	queue     []string
	running   string
	cancel    context.CancelFunc
	wake      chan struct{}
	ctx       context.Context
	stop      context.CancelFunc
	wg        sync.WaitGroup

	// registries are each job's private registries (§15.9), kept here only, never in its record.
	registries map[string][]serviceapi.RegistryV1
	// osv is the vulnerability data's folder scans bind read-only at /osv; "" refuses scans.
	osv string
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
		records: map[string]*record{}, byRequest: map[string]string{}, registries: map[string][]serviceapi.RegistryV1{}, wake: make(chan struct{}, 1),
		probe: &prober{root: filepath.Join(root, "probe"), sandbox: sandbox, now: now, probes: map[string]probe{}}}
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

// EnableScans lets the manager run scan jobs, binding dir (an absolute, clean, real folder: the operator's vulnerability
// data, --stack-osv-root) read-only at /osv in their parts.
func (m *Manager) EnableScans(dir string) error {
	info, err := os.Lstat(dir)
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || err != nil || !info.IsDir() {
		return serviceapi.ErrStackScansUnavailable
	}
	m.mu.Lock()
	m.osv = dir
	m.mu.Unlock()
	return nil
}

func bodyDigest(r serviceapi.StackJobCreateV1) string {
	fields := append([]string{r.Source, r.CacheKey}, serviceapi.RegistriesDigestPart(r.Registries)...)
	if r.Scan {
		// Only a scan says so, so a check's digest, kept with jobs from before scans, is what it was.
		fields = append(fields, "scan")
	}
	fields = append(fields, serviceapi.StacksDigestPart(r.Stacks)...)
	fields = append(fields, serviceapi.VersionsDigestPart(r.Versions)...)
	data, _ := json.Marshal(fields)
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
	if req.Scan && m.osv == "" {
		return serviceapi.StackJobV1{}, serviceapi.ErrStackScansUnavailable
	}
	if len(m.queue) >= maxQueued {
		return serviceapi.StackJobV1{}, serviceapi.ErrStackJobsBusy
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return serviceapi.StackJobV1{}, serviceapi.ErrStackJobsUnavailable
	}
	r := &record{BodyDigest: digest, Source: req.Source, CacheKey: req.CacheKey, Scan: req.Scan, Stacks: append([]string(nil), req.Stacks...), Versions: req.Versions, Job: serviceapi.StackJobV1{
		SchemaVersion: serviceapi.StackJobSchemaV1, ID: hex.EncodeToString(raw), RequestID: req.RequestID, State: Queued,
		CreatedAt: m.stamp(), Parts: []serviceapi.StackPartV1{}, Scan: req.Scan}}
	if err := m.save(r); err != nil {
		return serviceapi.StackJobV1{}, err
	}
	m.records[r.Job.ID] = r
	m.byRequest[req.RequestID] = r.Job.ID
	if len(req.Registries) > 0 {
		m.registries[r.Job.ID] = append([]serviceapi.RegistryV1(nil), req.Registries...)
	}
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
	tidyCaches(filepath.Join(m.root, "caches"), r.CacheKey)
	m.mu.Lock()
	m.running, m.cancel = "", nil
	delete(m.registries, id)
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
	parts := Pin(Only(Detect(work, m.profiles.Available()), r.Stacks), r.Versions, m.profiles.Available())
	if len(parts) == 0 {
		return FailNoStack
	}
	m.update(r, func(j *serviceapi.StackJobV1) {
		j.Parts = []serviceapi.StackPartV1{}
		for n, p := range parts {
			part := serviceapi.StackPartV1{Path: p.Dir, Stack: p.Stack, Found: p.Found, Version: p.Version, VersionNote: p.VersionNote, Tool: p.Tool,
				Steps: []serviceapi.StackStepV1{}}
			for _, s := range partSteps(p, n, r.Scan, false) {
				part.Steps = append(part.Steps, serviceapi.StackStepV1{Name: s.Name, State: StepWaiting, Command: s.Show})
			}
			j.Parts = append(j.Parts, part)
		}
	})
	cache := filepath.Join(m.root, "caches", r.CacheKey)
	if os.MkdirAll(cache, 0o755) != nil {
		return FailSandbox
	}
	// The cache is the project's own and a job may leave links in it: its home is made a real folder again first.
	if _, err := realDirs(cache, "home"); err != nil {
		return FailSandbox
	}
	now := m.now()
	_ = os.Chtimes(cache, now, now)
	m.mu.Lock()
	registries := m.registries[r.Job.ID]
	m.mu.Unlock()
	forget, err := writeRegistrySettings(cache, registries)
	if err != nil {
		return FailSandbox
	}
	defer forget()
	failure, ran := "", false
	for n, p := range parts {
		if p.Stack == StackUnsupported {
			continue
		}
		ran = true
		if f := m.runPart(ctx, r, n, p, work, cache, registries); f != "" && failure == "" {
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

// prober proves each stack profile's isolation before it is used, again after probeRetry when it failed.
type prober struct {
	root    string
	sandbox Sandbox
	now     func() time.Time
	mu      sync.Mutex
	probes  map[string]probe
}

func (p *prober) proved(ctx context.Context, profile ProfileV1) bool {
	p.mu.Lock()
	last, ok := p.probes[profile.ProfileID]
	p.mu.Unlock()
	if ok && (last.ok || p.now().Sub(last.at) < probeRetry) {
		return last.ok
	}
	dir := filepath.Join(p.root, profile.ProfileID)
	_ = os.RemoveAll(dir)
	stamp := p.now().UTC().Format(time.RFC3339Nano)
	result := os.MkdirAll(filepath.Join(dir, "work"), 0o755) == nil && os.MkdirAll(filepath.Join(dir, "cache"), 0o755) == nil &&
		p.sandbox.Prove(ctx, partID(profile.ProfileID, 0, "probe "+stamp), profile.Spec(), filepath.Join(dir, "work"), filepath.Join(dir, "cache"))
	_ = os.RemoveAll(dir)
	p.mu.Lock()
	p.probes[profile.ProfileID] = probe{ok: result, at: p.now()}
	p.mu.Unlock()
	return result
}

// failureOf is the job's code for a step that failed.
var failureOf = map[string]string{"install": FailInstall, "build": FailBuild, "test": FailTests, "scan": FailScan}

// partSteps are a part's steps: a check's, or a scan's.
func partSteps(p Part, n int, scan, measure bool) []Step {
	if scan {
		return ScanSteps(p, n)
	}
	s := Steps(p, n)
	if measure {
		for i := range s {
			s[i] = Measured(p, n, s[i])
		}
	}
	return s
}

func (m *Manager) runPart(ctx context.Context, r *record, n int, p Part, work, cache string, registries []serviceapi.RegistryV1) string {
	x := steps{sandbox: m.sandbox, profiles: m.profiles, probe: m.probe, session: partID(r.Job.ID, n, "part"), registries: registries,
		set: func(i int, change func(*serviceapi.StackStepV1)) {
			m.update(r, func(j *serviceapi.StackJobV1) { change(&j.Parts[n].Steps[i]) })
		}}
	if r.Scan {
		m.mu.Lock()
		x.scan, x.osv = true, m.osv
		m.mu.Unlock()
		x.part = func(change func(*serviceapi.StackPartV1)) {
			m.update(r, func(j *serviceapi.StackJobV1) { change(&j.Parts[n]) })
		}
	}
	return runSteps(ctx, x, n, p, work, cache)
}

// steps is what runs one part's steps needs: the sandbox, the profiles and their probe, the container's id, and where
// each step's state is kept.
type steps struct {
	sandbox  Sandbox
	profiles ProfilesV1
	probe    *prober
	session  string
	set      func(i int, change func(*serviceapi.StackStepV1))
	// registries are the job's private registries (§15.9): their names join the part's egress, Maven's settings carry
	// them, and their tokens are masked in every output kept.
	registries []serviceapi.RegistryV1
	// scan runs a scan's steps with the vulnerability data (osv) bound at /osv, and keeps the part's findings with part.
	scan bool
	osv  string
	part func(change func(*serviceapi.StackPartV1))
	// measure runs the tests with the stack's coverage tool and keeps the project's line coverage on the test step (Repo C
	// design note FIX_AND_TEST §4), for a change job's tests.
	measure bool
}

// runSteps runs one part's steps in a container of its own and answers the part's failure code or "".
func runSteps(ctx context.Context, x steps, n int, p Part, work, cache string) string {
	profile, ok := x.profiles.For(p.Stack, p.Version)
	if !ok || !x.probe.proved(ctx, profile) {
		return FailSandbox
	}
	spec := profile.Spec()
	spec.Hosts = registryHosts(x.registries)
	if x.scan {
		if x.osv == "" || x.part == nil {
			return FailSandbox
		}
		spec.OSVData = x.osv
	}
	session, err := x.sandbox.Open(ctx, x.session, spec, work, cache)
	if err != nil {
		return FailSandbox
	}
	secrets := registrySecrets(x.registries)
	ended := false
	end := func() {
		if !ended {
			ended = true
			_ = session.Close()
		}
	}
	defer end()
	if javaSettings(p, cache, session.Proxy(), x.registries) != nil {
		return FailSandbox
	}
	env := Env(p, profile, session.Proxy())
	partCtx, cancel := context.WithTimeout(ctx, time.Duration(profile.TimeoutSeconds)*time.Second)
	defer cancel()
	failure, counted, scanned, countedOutput := "", -1, -1, ""
	var countedGo *goTally
	for i, s := range partSteps(p, n, x.scan, x.measure) {
		set := func(change func(*serviceapi.StackStepV1)) { x.set(i, change) }
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
		var w io.Writer = out
		var tally *goTally
		if s.JUnit && p.Stack == StackGo {
			tally = &goTally{}
			w = io.MultiWriter(out, tally)
		}
		stepCtx, stepCancel := context.WithTimeout(partCtx, s.Timeout)
		started := time.Now()
		code, err := session.Exec(stepCtx, s.Dir, env, s.Argv, w)
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
			if s.Name == "scan" {
				scanned = i
			}
		case s.NoTests != 0 && code == s.NoTests:
			state, note = StepSkipped, "no tests were found"
		case x.scan && (s.Name == "install" || s.Name == "build"):
			// A scan goes on without a whole install: lockfiles, the code and what was installed are still read.
			state, note = Failed, "the scan goes on with what was installed"
		default:
			state, failure = Failed, failureOf[s.Name]
		}
		if s.JUnit && state != Stopped {
			counted, countedOutput, countedGo = i, out.Lines(outputLines), tally
		}
		if failure == "" && folderSize(work, profile.WorkBytes) > profile.WorkBytes {
			state, failure, note = Failed, FailTooLarge, "the code grew past what a check may write"
		}
		exit := code
		set(func(st *serviceapi.StackStepV1) {
			st.State, st.Note, st.Seconds, st.OutputTail = state, note, float64(int(seconds*10))/10, maskRegistries(out.Lines(outputLines), secrets)
			if err == nil {
				st.ExitCode = &exit
			}
		})
		if failure == FailStopped {
			break
		}
	}
	// The tests' reports and a scan's result are read once the part's container has ended, so nothing can change them
	// while they are read.
	end()
	if scanned >= 0 && failure == "" {
		result, err := ReadScan(work, n, secrets)
		if err != nil {
			failure = FailScan
			x.set(scanned, func(st *serviceapi.StackStepV1) { st.State, st.Note = Failed, "its result could not be read" })
		} else {
			x.part(func(part *serviceapi.StackPartV1) {
				part.Findings, part.FindingsMore, part.Licences, part.Checks = result.Findings, result.More, result.Licences, result.Checks
				part.ScanAbout = result.About
			})
		}
	}
	if counted >= 0 {
		extra := []string{}
		if p.Stack == StackPython && realFile(work, ".abcp", filepath.Base(pytestReport(n))) {
			extra = append(extra, filepath.Join(work, ".abcp", filepath.Base(pytestReport(n))))
		}
		if p.Stack == StackPHP && realFile(work, ".abcp", filepath.Base(phpunitReport(n))) {
			extra = append(extra, filepath.Join(work, ".abcp", filepath.Base(phpunitReport(n))))
		}
		passed, failed, skipped, ok := Counts(filepath.Join(work, filepath.FromSlash(p.Dir)), extra...)
		switch {
		case ok:
		case p.Stack == StackNode || p.Stack == StackRuby:
			// A Node or Ruby project seldom writes JUnit; its runner's own lines say the same (FIX_AND_TEST §5, MORE_STACKS §4).
			passed, failed, skipped, ok = RunnerCounts(countedOutput)
		case countedGo != nil:
			// Nor does a Go project; each test's own line does.
			passed, failed, skipped, ok = countedGo.Counts()
		case p.Stack == StackDotnet:
			// .NET's are its TRX logs, outside the part.
			passed, failed, skipped, ok = TRXCounts(filepath.Join(work, ".abcp", filepath.Base(dotnetResults(n))))
		}
		if ok {
			tests := &serviceapi.StackTestsV1{Passed: passed, Failed: failed, Skipped: skipped}
			x.set(counted, func(st *serviceapi.StackStepV1) { st.Tests = tests })
		}
		if x.measure {
			if coverage := ReadCoverage(work, n); coverage != nil {
				x.set(counted, func(st *serviceapi.StackStepV1) { st.Coverage = coverage })
			}
		}
	}
	return failure
}

// javaSettings tells a Java part's Maven and Gradle where the proxy is. Written from the host into a cache a job may have
// left links in: through real folders only, never a link.
func javaSettings(p Part, cache string, proxy netip.AddrPort, registries []serviceapi.RegistryV1) error {
	if p.Stack != StackJava {
		return nil
	}
	m2, err := realDirs(cache, "home", ".m2")
	if err != nil {
		return err
	}
	if err := writeReal(m2, "settings.xml", []byte(mavenSettings(proxy, registries))); err != nil {
		return err
	}
	gradle, err := realDirs(cache, "home", ".gradle")
	if err != nil {
		return err
	}
	return writeReal(gradle, "gradle.properties", []byte(gradleProperties(proxy)))
}

// tidyCaches empties a cache past its limit, and removes the least recently used caches while all of them are past
// theirs, never the one just used.
func tidyCaches(dir, current string) {
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

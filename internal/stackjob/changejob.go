package stackjob

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// Change jobs (docs/plans/change-jobs.md; Repo C design note IMPROVE_EXISTING_PRODUCTS §14): a customer's code changed
// by the agent on the host, which has no shell of its own: every command it runs goes through the job's broker to the
// job's stack sandbox. The checks before and after are a stack job's steps.

// Change job states, phases and the reasons a job ends without being handed back.
const (
	ChangeHandedBack, ChangeNotHandedBack, ChangeFailed = "HANDED_BACK", "NOT_HANDED_BACK", "FAILED"

	PhaseBaseline, PhaseAgent, PhaseReview, PhaseChecking, PhaseResult = "BASELINE", "AGENT", "REVIEW", "CHECKING", "RESULT"

	ReasonBaseline  = "BASELINE_FAILED"
	ReasonNoChange  = "NO_CHANGE"
	ReasonWorse     = "WORSE_AFTER"
	ReasonTooLarge  = "TOO_LARGE"
	ReasonForbidden = "FORBIDDEN_PATH"
	ReasonEngine    = "ENGINE_UNAVAILABLE"
	ReasonSecret    = "SECRET_IN_CHANGE"
)

// secretKeeper is an engine that can say which values must never leave a job.
type secretKeeper interface{ SecretValues() []string }

const (
	changeWorkers    = 2
	changeQueued     = 10
	changeKept       = 500
	changeJobTime    = 90 * time.Minute
	agentTime        = 40 * time.Minute
	reviewTime       = 15 * time.Minute
	agentTurns       = 80
	reviewTurns      = 20
	changeSummaryMax = 4 << 10
)

// Engine runs the agent: Claude Code, headless on the host, a fake in tests.
type Engine interface {
	Run(ctx context.Context, run EngineRun) (EngineResult, error)
}

// EngineRun is one session: the work folder it changes, its own empty home, ABCP's MCP file naming the run tool, the
// prompt and the turns it may take.
type EngineRun struct {
	Work, Home, MCPConfig, Prompt string
	MaxTurns                      int
}

// EngineResult is what a session said and used: the model that did most of the work and its tokens (input counts the
// cache's reads and writes too).
type EngineResult struct {
	Summary, Model                                               string
	InputTokens, CacheReadTokens, CacheWriteTokens, OutputTokens int64
	CostUSD                                                      float64
}

type changeRecord struct {
	Job        serviceapi.ChangeJobV1 `json:"job"`
	BodyDigest string                 `json:"body_digest"`
	Source     string                 `json:"source"`
	CacheKey   string                 `json:"cache_key"`
	Words      string                 `json:"words"`
	Stacks     []string               `json:"stacks,omitempty"`
}

// ChangeManager runs change jobs, at most two at once and one per project.
type ChangeManager struct {
	root, sources string
	profiles      ProfilesV1
	sandbox       Sandbox
	engine        Engine
	// tool is this binary, which the agent's MCP file runs as `abcp stack-tool`.
	tool     string
	now      func() time.Time
	freeDisk func(string) (uint64, bool)
	probe    *prober

	mu        sync.Mutex
	records   map[string]*changeRecord
	byRequest map[string]string
	queue     []string
	busy      map[string]bool
	cancels   map[string]context.CancelFunc
	wake      chan struct{}
	ctx       context.Context
	stop      context.CancelFunc
	wg        sync.WaitGroup

	// registries are each job's private registries (§15.9), kept here only, never in its record.
	registries map[string][]serviceapi.RegistryV1
}

// NewChangeManager opens the jobs kept under root, marks any left running UNKNOWN, removes what they left, and starts
// the workers.
func NewChangeManager(root, sources, tool string, profiles ProfilesV1, sandbox Sandbox, engine Engine, now func() time.Time) (*ChangeManager, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(sources) || !filepath.IsAbs(tool) || filepath.Clean(root) != root || filepath.Clean(sources) != sources ||
		profiles.Validate() != nil || sandbox == nil || engine == nil || now == nil {
		return nil, serviceapi.ErrChangeJobsUnavailable
	}
	if info, err := os.Lstat(sources); err != nil || !info.IsDir() {
		return nil, serviceapi.ErrChangeJobsUnavailable
	}
	for _, dir := range []string{root, filepath.Join(root, "jobs"), filepath.Join(root, "patches"), filepath.Join(root, "work"), filepath.Join(root, "s"),
		filepath.Join(root, "caches"), filepath.Join(root, "probe")} {
		if err := os.MkdirAll(dir, 0o700); err != nil || os.Chmod(dir, 0o700) != nil {
			return nil, serviceapi.ErrChangeJobsUnavailable
		}
	}
	m := &ChangeManager{root: root, sources: sources, tool: tool, profiles: profiles, sandbox: sandbox, engine: engine, now: now, freeDisk: statfsFree,
		probe:   &prober{root: filepath.Join(root, "probe"), sandbox: sandbox, now: now, probes: map[string]probe{}},
		records: map[string]*changeRecord{}, byRequest: map[string]string{}, busy: map[string]bool{}, cancels: map[string]context.CancelFunc{},
		wake: make(chan struct{}, changeWorkers), registries: map[string][]serviceapi.RegistryV1{}}
	if err := m.load(); err != nil {
		return nil, err
	}
	m.ctx, m.stop = context.WithCancel(context.Background())
	for i := 0; i < changeWorkers; i++ {
		m.wg.Add(1)
		go m.work()
	}
	return m, nil
}

// Close stops the workers, ending running jobs as STOPPED.
func (m *ChangeManager) Close() {
	m.stop()
	m.mu.Lock()
	for _, cancel := range m.cancels {
		cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *ChangeManager) stamp() string { return m.now().UTC().Format(time.RFC3339Nano) }

func (m *ChangeManager) load() error {
	entries, err := os.ReadDir(filepath.Join(m.root, "jobs"))
	if err != nil {
		return serviceapi.ErrChangeJobsUnavailable
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(m.root, "jobs", e.Name()))
		var r changeRecord
		if err != nil || json.Unmarshal(data, &r) != nil || !keyPattern.MatchString(r.Job.ID) || e.Name() != r.Job.ID+".json" {
			continue
		}
		if r.Job.State == Queued || r.Job.State == Running {
			r.Job.State, r.Job.Phase, r.Job.EndedAt = Unknown, "", m.stamp()
		}
		m.records[r.Job.ID] = &r
		m.byRequest[r.Job.RequestID] = r.Job.ID
		if err := m.save(&r); err != nil {
			return err
		}
	}
	for _, dir := range []string{"work", "s"} {
		left, _ := os.ReadDir(filepath.Join(m.root, dir))
		for _, e := range left {
			_ = os.RemoveAll(filepath.Join(m.root, dir, e.Name()))
		}
	}
	m.prune()
	return nil
}

func (m *ChangeManager) save(r *changeRecord) error {
	data, err := json.Marshal(r)
	if err != nil {
		return serviceapi.ErrChangeJobsUnavailable
	}
	path := filepath.Join(m.root, "jobs", r.Job.ID+".json")
	if os.WriteFile(path+".tmp", data, 0o600) != nil || os.Rename(path+".tmp", path) != nil {
		return serviceapi.ErrChangeJobsUnavailable
	}
	return nil
}

// prune keeps the newest changeKept ended jobs and their patches.
func (m *ChangeManager) prune() {
	ended := []*changeRecord{}
	for _, r := range m.records {
		if r.Job.State != Queued && r.Job.State != Running {
			ended = append(ended, r)
		}
	}
	if len(ended) <= changeKept {
		return
	}
	sort.Slice(ended, func(i, j int) bool { return ended[i].Job.CreatedAt < ended[j].Job.CreatedAt })
	for _, r := range ended[:len(ended)-changeKept] {
		delete(m.records, r.Job.ID)
		if m.byRequest[r.Job.RequestID] == r.Job.ID {
			delete(m.byRequest, r.Job.RequestID)
		}
		_ = os.Remove(filepath.Join(m.root, "jobs", r.Job.ID+".json"))
		_ = os.Remove(filepath.Join(m.root, "patches", r.Job.ID+".diff"))
	}
}

func copyChangeJob(j serviceapi.ChangeJobV1) serviceapi.ChangeJobV1 {
	data, _ := json.Marshal(j)
	var out serviceapi.ChangeJobV1
	_ = json.Unmarshal(data, &out)
	return out
}

func changeDigest(r serviceapi.ChangeJobCreateV1) string {
	fields := append([]string{r.Source, r.CacheKey, r.Kind, r.Words}, serviceapi.RegistriesDigestPart(r.Registries)...)
	data, _ := json.Marshal(append(fields, serviceapi.StacksDigestPart(r.Stacks)...))
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// StartChangeJob queues a job, or answers the job its request ID already started.
func (m *ChangeManager) StartChangeJob(_ context.Context, req serviceapi.ChangeJobCreateV1) (serviceapi.ChangeJobV1, error) {
	if !req.Valid() {
		return serviceapi.ChangeJobV1{}, serviceapi.ErrChangeJobNotAccepted
	}
	digest := changeDigest(req)
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.byRequest[req.RequestID]; ok {
		r := m.records[id]
		if r.BodyDigest != digest {
			return serviceapi.ChangeJobV1{}, serviceapi.ErrChangeJobConflict
		}
		return copyChangeJob(r.Job), nil
	}
	if info, err := os.Lstat(filepath.Join(m.sources, req.Source)); err != nil || !info.IsDir() {
		return serviceapi.ChangeJobV1{}, serviceapi.ErrChangeJobNotAccepted
	}
	if len(m.queue) >= changeQueued {
		return serviceapi.ChangeJobV1{}, serviceapi.ErrChangeJobsBusy
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return serviceapi.ChangeJobV1{}, serviceapi.ErrChangeJobsUnavailable
	}
	r := &changeRecord{BodyDigest: digest, Source: req.Source, CacheKey: req.CacheKey, Words: req.Words, Stacks: append([]string(nil), req.Stacks...), Job: serviceapi.ChangeJobV1{
		SchemaVersion: serviceapi.ChangeJobSchemaV1, ID: hex.EncodeToString(raw), RequestID: req.RequestID, Kind: req.Kind, State: Queued,
		CreatedAt: m.stamp(), Before: []serviceapi.StackPartV1{}, After: []serviceapi.StackPartV1{}}}
	if err := m.save(r); err != nil {
		return serviceapi.ChangeJobV1{}, err
	}
	m.records[r.Job.ID] = r
	m.byRequest[req.RequestID] = r.Job.ID
	if len(req.Registries) > 0 {
		m.registries[r.Job.ID] = append([]serviceapi.RegistryV1(nil), req.Registries...)
	}
	m.queue = append(m.queue, r.Job.ID)
	m.prune()
	m.nudge()
	return copyChangeJob(r.Job), nil
}

func (m *ChangeManager) nudge() {
	for i := 0; i < changeWorkers; i++ {
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
}

// ChangeJob answers one job.
func (m *ChangeManager) ChangeJob(_ context.Context, id string) (serviceapi.ChangeJobV1, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return serviceapi.ChangeJobV1{}, serviceapi.ErrChangeJobNotFound
	}
	return copyChangeJob(r.Job), nil
}

// ChangeJobPatch answers a handed-back job's patch, checked against its digest.
func (m *ChangeManager) ChangeJobPatch(_ context.Context, id string) ([]byte, error) {
	m.mu.Lock()
	r, ok := m.records[id]
	var want string
	if ok && r.Job.State == ChangeHandedBack && r.Job.Result != nil {
		want = r.Job.Result.PatchSHA256
	}
	m.mu.Unlock()
	if want == "" {
		return nil, serviceapi.ErrChangeJobNotFound
	}
	data, err := os.ReadFile(filepath.Join(m.root, "patches", id+".diff"))
	sum := sha256.Sum256(data)
	if err != nil || hex.EncodeToString(sum[:]) != want {
		return nil, serviceapi.ErrChangeJobsUnavailable
	}
	return data, nil
}

// StopChangeJob stops a queued job at once and a running one at its next moment; an ended job is answered as it is.
func (m *ChangeManager) StopChangeJob(_ context.Context, id string) (serviceapi.ChangeJobV1, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	if !ok {
		return serviceapi.ChangeJobV1{}, serviceapi.ErrChangeJobNotFound
	}
	switch {
	case r.Job.State == Queued:
		for i, q := range m.queue {
			if q == id {
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				break
			}
		}
		r.Job.State, r.Job.Reason, r.Job.EndedAt = Stopped, FailStopped, m.stamp()
		if err := m.save(r); err != nil {
			return serviceapi.ChangeJobV1{}, err
		}
	case r.Job.State == Running && m.cancels[id] != nil:
		m.cancels[id]()
	}
	return copyChangeJob(r.Job), nil
}

// next takes the first queued job whose project has none running.
func (m *ChangeManager) next() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil {
		return ""
	}
	for i, id := range m.queue {
		if r := m.records[id]; !m.busy[r.CacheKey] {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			m.busy[r.CacheKey] = true
			return id
		}
	}
	return ""
}

func (m *ChangeManager) work() {
	defer m.wg.Done()
	for {
		id := m.next()
		if id == "" {
			select {
			case <-m.ctx.Done():
				return
			case <-m.wake:
			}
			continue
		}
		m.run(id)
		m.nudge()
	}
}

func (m *ChangeManager) update(r *changeRecord, change func(*serviceapi.ChangeJobV1)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	change(&r.Job)
	_ = m.save(r)
}

// changeEnd is how a job ended: its state and its reason.
type changeEnd struct{ state, reason string }

var errStopped = errors.New("change job stopped")

func (m *ChangeManager) run(id string) {
	ctx, cancel := context.WithTimeout(m.ctx, changeJobTime)
	defer cancel()
	m.mu.Lock()
	r := m.records[id]
	m.cancels[id] = cancel
	r.Job.State, r.Job.StartedAt = Running, m.stamp()
	_ = m.save(r)
	m.mu.Unlock()
	dir := filepath.Join(m.root, "work", id)
	end := m.runChange(ctx, r, dir)
	if ctx.Err() == context.DeadlineExceeded && end.state != ChangeHandedBack && end.state != ChangeNotHandedBack {
		end = changeEnd{ChangeFailed, FailTimedOut}
	}
	_ = os.RemoveAll(dir)
	_ = os.Remove(m.socket(id))
	m.update(r, func(j *serviceapi.ChangeJobV1) {
		j.State, j.Phase, j.EndedAt = end.state, "", m.stamp()
		if end.state != ChangeHandedBack {
			j.Reason = end.reason
		}
		for _, parts := range [][]serviceapi.StackPartV1{j.Before, j.After} {
			for p := range parts {
				for s := range parts[p].Steps {
					if st := &parts[p].Steps[s]; st.State == StepWaiting || st.State == Running {
						st.State = StepNotRun
					}
				}
			}
		}
	})
	tidyCaches(filepath.Join(m.root, "caches"), r.CacheKey)
	m.mu.Lock()
	delete(m.cancels, id)
	delete(m.busy, r.CacheKey)
	delete(m.registries, id)
	m.mu.Unlock()
}

// socket is the job's broker socket: 96 bits of the job's id, short since a Unix socket's path is bounded.
func (m *ChangeManager) socket(id string) string { return filepath.Join(m.root, "s", id[:24]+".sock") }

// stopped answers the end for a job whose context ended.
func stopped(ctx context.Context) changeEnd {
	if ctx.Err() == context.DeadlineExceeded {
		return changeEnd{ChangeFailed, FailTimedOut}
	}
	return changeEnd{Stopped, FailStopped}
}

func (m *ChangeManager) runChange(ctx context.Context, r *changeRecord, dir string) changeEnd {
	if free, ok := m.freeDisk(m.root); !ok || free < freeDiskBytes {
		return changeEnd{ChangeFailed, FailSandbox}
	}
	work, gitDir, home := filepath.Join(dir, "files"), filepath.Join(dir, "git"), filepath.Join(dir, "home")
	if os.MkdirAll(dir, 0o700) != nil || os.Mkdir(home, 0o700) != nil {
		return changeEnd{ChangeFailed, FailSandbox}
	}
	if copySource(filepath.Join(m.sources, r.Source), work) != nil {
		return changeEnd{ChangeFailed, FailSource}
	}
	kept := keptFolders(work)
	if os.Mkdir(filepath.Join(work, ".abcp"), 0o755) != nil || gitStart(gitDir, work, home) != nil {
		return changeEnd{ChangeFailed, FailSandbox}
	}
	tracked, err := trackedFiles(gitDir, work, home)
	if err != nil {
		return changeEnd{ChangeFailed, FailSandbox}
	}
	parts := Only(Detect(work, m.profiles.Available()), r.Stacks)
	agentPart := -1
	for n, p := range parts {
		if p.Stack != StackUnsupported {
			agentPart = n
			break
		}
	}
	switch {
	case len(parts) == 0:
		return changeEnd{ChangeFailed, FailNoStack}
	case agentPart < 0:
		return changeEnd{ChangeFailed, FailUnsupported}
	}
	cache := filepath.Join(m.root, "caches", r.CacheKey)
	if os.MkdirAll(cache, 0o755) != nil {
		return changeEnd{ChangeFailed, FailSandbox}
	}
	if _, err := realDirs(cache, "home"); err != nil {
		return changeEnd{ChangeFailed, FailSandbox}
	}
	now := m.now()
	_ = os.Chtimes(cache, now, now)
	m.mu.Lock()
	registries := m.registries[r.Job.ID]
	m.mu.Unlock()
	forget, err := writeRegistrySettings(cache, registries)
	if err != nil {
		return changeEnd{ChangeFailed, FailSandbox}
	}
	defer forget()

	// The baseline.
	m.update(r, func(j *serviceapi.ChangeJobV1) { j.Phase, j.Before = PhaseBaseline, planParts(parts) })
	if f := m.check(ctx, r, parts, work, cache, "before", registries); f != "" {
		if f == FailStopped || ctx.Err() != nil {
			return stopped(ctx)
		}
		return changeEnd{ChangeFailed, FailSandbox}
	}
	before := m.snapshot(r).Before
	if !baselineReady(r.Job.Kind, before[agentPart]) {
		return changeEnd{ChangeNotHandedBack, ReasonBaseline}
	}

	// The agent, then its review.
	m.update(r, func(j *serviceapi.ChangeJobV1) { j.Phase, j.Agent = PhaseAgent, &serviceapi.ChangeAgentV1{} })
	b, err := m.openBroker(ctx, r, parts[agentPart], agentPart, work, cache, registries)
	if err != nil {
		if ctx.Err() != nil {
			return stopped(ctx)
		}
		return changeEnd{ChangeFailed, FailSandbox}
	}
	defer b.close()
	mcp := filepath.Join(dir, "mcp.json")
	if writeMCPConfig(mcp, m.tool, m.socket(r.Job.ID)) != nil {
		return changeEnd{ChangeFailed, FailSandbox}
	}
	started := time.Now()
	agentCtx, agentCancel := context.WithTimeout(ctx, agentTime)
	said, err := m.engine.Run(agentCtx, EngineRun{Work: work, Home: home, MCPConfig: mcp, MaxTurns: agentTurns,
		Prompt: changePrompt(r.Job.Kind, r.Words, parts[agentPart], before[agentPart])})
	agentCancel()
	values := append(m.secretValues(), registrySecrets(registries)...)
	leaked := m.recordAgent(r, said, false, b.commands(), time.Since(started), values)
	if err != nil {
		if ctx.Err() != nil {
			return stopped(ctx)
		}
		return changeEnd{ChangeFailed, ReasonEngine}
	}
	diff, err := workingDiff(gitDir, work, home, kept, tracked)
	if err != nil {
		return changeEnd{ChangeFailed, FailSandbox}
	}
	if len(diff) == 0 {
		return changeEnd{ChangeNotHandedBack, ReasonNoChange}
	}
	m.update(r, func(j *serviceapi.ChangeJobV1) { j.Phase = PhaseReview })
	if writeReal(filepath.Join(work, ".abcp"), "change.diff", diff) != nil {
		return changeEnd{ChangeFailed, FailSandbox}
	}
	reviewCtx, reviewCancel := context.WithTimeout(ctx, reviewTime)
	reviewed, err := m.engine.Run(reviewCtx, EngineRun{Work: work, Home: home, MCPConfig: mcp, MaxTurns: reviewTurns,
		Prompt: reviewPrompt(r.Job.Kind, r.Words)})
	reviewCancel()
	leaked = m.recordAgent(r, reviewed, true, b.commands(), time.Since(started), values) || leaked
	b.close()
	if err != nil {
		if ctx.Err() != nil {
			return stopped(ctx)
		}
		return changeEnd{ChangeFailed, ReasonEngine}
	}

	// The result, made before the checks after write anything.
	m.update(r, func(j *serviceapi.ChangeJobV1) { j.Phase = PhaseResult })
	result, patch, reason, err := makeResult(gitDir, work, filepath.Join(dir, "result"), home, kept, tracked)
	switch {
	case err != nil:
		return changeEnd{ChangeFailed, FailSandbox}
	case reason != "":
		return changeEnd{ChangeNotHandedBack, reason}
	}

	if leaked || leaks(patch, values) {
		m.update(r, func(j *serviceapi.ChangeJobV1) {
			if j.Agent != nil {
				j.Agent.Summary, j.Agent.Review = "", ""
			}
		})
		return changeEnd{ChangeNotHandedBack, ReasonSecret}
	}

	// A fix's new tests on the code as it was before the change (FIX_AND_TEST §3): shown, never required. It is a check, so
	// the phase says so; the product reads no other phase yet.
	if r.Job.Kind == "fix" {
		m.update(r, func(j *serviceapi.ChangeJobV1) { j.Phase = PhaseChecking })
		proof, f := m.prove(ctx, r, parts[agentPart], agentPart, before[agentPart], result, work, filepath.Join(dir, "proof"), cache, registries)
		if f == FailStopped || ctx.Err() != nil {
			return stopped(ctx)
		}
		m.update(r, func(j *serviceapi.ChangeJobV1) { j.Proof = proof })
	}

	// The checks after, on the changed files.
	m.update(r, func(j *serviceapi.ChangeJobV1) { j.Phase, j.After = PhaseChecking, planParts(parts) })
	if f := m.check(ctx, r, parts, work, cache, "after", registries); f == FailStopped || f == FailSandbox {
		if f == FailStopped || ctx.Err() != nil {
			return stopped(ctx)
		}
		return changeEnd{ChangeFailed, FailSandbox}
	}
	if !noWorse(r.Job.Kind, before, m.snapshot(r).After, agentPart) {
		return changeEnd{ChangeNotHandedBack, ReasonWorse}
	}
	if os.WriteFile(filepath.Join(m.root, "patches", r.Job.ID+".diff"), patch, 0o600) != nil {
		return changeEnd{ChangeFailed, FailSandbox}
	}
	m.update(r, func(j *serviceapi.ChangeJobV1) { j.Result = result })
	return changeEnd{ChangeHandedBack, ""}
}

func (m *ChangeManager) snapshot(r *changeRecord) serviceapi.ChangeJobV1 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return copyChangeJob(r.Job)
}

// secretValues answers the engine's values that must never leave a job.
func (m *ChangeManager) secretValues() []string {
	if k, ok := m.engine.(secretKeeper); ok {
		return k.SecretValues()
	}
	return nil
}

// recordAgent keeps what a session said, masked and without the engine's own values, and what it used; it answers
// whether what it said carried a secret.
func (m *ChangeManager) recordAgent(r *changeRecord, said EngineResult, review bool, commands int, took time.Duration, values []string) bool {
	leaked := leaks([]byte(said.Summary), values)
	m.update(r, func(j *serviceapi.ChangeJobV1) {
		if j.Agent == nil {
			j.Agent = &serviceapi.ChangeAgentV1{}
		}
		text := cutText(said.Summary, changeSummaryMax)
		for _, v := range values {
			if v != "" {
				text = strings.ReplaceAll(text, v, "[hidden]")
			}
		}
		text = Mask(text)
		if review {
			j.Agent.Review = text
		} else {
			j.Agent.Summary = text
		}
		j.Agent.InputTokens += said.InputTokens
		j.Agent.CacheReadTokens += said.CacheReadTokens
		j.Agent.CacheWriteTokens += said.CacheWriteTokens
		j.Agent.OutputTokens += said.OutputTokens
		if said.Model != "" && (j.Agent.Model == "" || !review) {
			j.Agent.Model = said.Model
		}
		j.Agent.CostUSD += said.CostUSD
		j.Agent.Commands = commands
		j.Agent.Seconds = float64(int(took.Seconds()*10)) / 10
	})
	return leaked
}

func cutText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	for len(cut) > 0 && cut[len(cut)-1]&0xc0 == 0x80 {
		cut = cut[:len(cut)-1]
	}
	if len(cut) > 0 && cut[len(cut)-1] >= 0xc0 {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// planParts is the parts as found, each step waiting.
func planParts(parts []Part) []serviceapi.StackPartV1 {
	out := []serviceapi.StackPartV1{}
	for n, p := range parts {
		part := serviceapi.StackPartV1{Path: p.Dir, Stack: p.Stack, Found: p.Found, Version: p.Version, VersionNote: p.VersionNote, Tool: p.Tool,
			Steps: []serviceapi.StackStepV1{}}
		for _, s := range Steps(p, n) {
			part.Steps = append(part.Steps, serviceapi.StackStepV1{Name: s.Name, State: StepWaiting, Command: s.Show})
		}
		out = append(out, part)
	}
	return out
}

// check runs every supported part's steps into the job's before or after; it answers FailStopped or FailSandbox when the
// job cannot go on, and "" otherwise, whatever the steps' own results.
func (m *ChangeManager) check(ctx context.Context, r *changeRecord, parts []Part, work, cache, which string, registries []serviceapi.RegistryV1) string {
	for n, p := range parts {
		if p.Stack == StackUnsupported {
			continue
		}
		n := n
		f := runSteps(ctx, steps{sandbox: m.sandbox, profiles: m.profiles, probe: m.probe, session: partID(r.Job.ID, n, which), registries: registries,
			measure: r.Job.Kind == "tests",
			set: func(i int, change func(*serviceapi.StackStepV1)) {
				m.update(r, func(j *serviceapi.ChangeJobV1) {
					if which == "before" {
						change(&j.Before[n].Steps[i])
					} else {
						change(&j.After[n].Steps[i])
					}
				})
			}}, n, p, work, cache)
		if f == FailStopped || f == FailSandbox {
			return f
		}
	}
	return ""
}

var _ serviceapi.ChangeJobController = (*ChangeManager)(nil)

// prove runs a fix's added or changed test files on the code as it was before the change (FIX_AND_TEST §3): the job's
// source again, with only those files from the change, installed from the project's cache and tested in a fresh sandbox
// as the baseline was. It answers what that showed, and FailStopped when the job was stopped meanwhile.
func (m *ChangeManager) prove(ctx context.Context, r *changeRecord, p Part, n int, base serviceapi.StackPartV1, result *serviceapi.ChangeResultV1,
	work, folder, cache string, registries []serviceapi.RegistryV1) (*serviceapi.ChangeProofV1, string) {
	files, deleted := []string{}, map[string]bool{}
	for _, f := range result.Files {
		if testFile(p, f.Path) {
			files = append(files, f.Path)
			deleted[f.Path] = f.Status == "deleted"
		}
	}
	if len(files) == 0 {
		return &serviceapi.ChangeProofV1{State: ProofNoTest, Files: files}, ""
	}
	notRun := func(note string) (*serviceapi.ChangeProofV1, string) {
		return &serviceapi.ChangeProofV1{State: ProofNotRun, Files: files, Note: note}, ""
	}
	plan := Steps(p, n)
	if len(plan) == 0 || plan[len(plan)-1].Name != "test" || plan[len(plan)-1].Skip != "" {
		return notRun("the project has no tests to run")
	}
	if bt, ok := step(base, "test"); !ok || (bt.State != Passed && bt.State != Failed && bt.State != StepSkipped) {
		return notRun("the tests did not run before the change")
	}
	defer os.RemoveAll(folder)
	if copySource(filepath.Join(m.sources, r.Source), folder) != nil {
		return notRun("the code before the change could not be read")
	}
	for _, f := range files {
		target := filepath.Join(folder, filepath.FromSlash(f))
		if deleted[f] {
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return notRun("the code before the change could not be prepared")
			}
			continue
		}
		names := strings.Split(f, "/")
		data, readable := readReport(filepath.Join(work, filepath.FromSlash(f)), maxProofFile)
		dir, err := realDirs(folder, names[:len(names)-1]...)
		if !realFile(work, names...) || !readable || err != nil || writeReal(dir, names[len(names)-1], data) != nil {
			return notRun("the code before the change could not be prepared")
		}
	}
	if os.MkdirAll(filepath.Join(folder, ".abcp"), 0o755) != nil {
		return notRun("the code before the change could not be prepared")
	}
	part := serviceapi.StackPartV1{Steps: []serviceapi.StackStepV1{}}
	for _, s := range plan {
		part.Steps = append(part.Steps, serviceapi.StackStepV1{Name: s.Name, State: StepWaiting, Command: s.Show})
	}
	f := runSteps(ctx, steps{sandbox: m.sandbox, profiles: m.profiles, probe: m.probe, session: partID(r.Job.ID, n, "proof"), registries: registries,
		set: func(i int, change func(*serviceapi.StackStepV1)) { change(&part.Steps[i]) }}, n, p, folder, cache)
	if f == FailStopped || ctx.Err() != nil {
		return nil, FailStopped
	}
	pt, _ := step(part, "test")
	bt, _ := step(base, "test")
	state, note := proofState(bt, pt)
	return &serviceapi.ChangeProofV1{State: state, Files: files, Tests: pt.Tests, Note: note}, ""
}

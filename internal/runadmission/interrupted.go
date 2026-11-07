package runadmission

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

// R6's resume (Repo C BUILD_RELIABILITY.md §5.6; docs/plans/build-recovery.md §6.2). abcp serve, at start and every
// ResumeEvery, looks at the registered runs and launches abcp run --resume-interrupted, once per run and at most
// ResumesPerPass a pass, for each one whose builder was interrupted (its ledger stops at RECOVERY_REQUIRED with recovery
// RESUME, or in EXECUTION_STARTING or IMPLEMENTING) and whose owner process is gone: never for a run resumed before,
// one asked to stop, or one whose profile allows no fresh attempt (runctl.ResumesAfterInterruption). The launch is
// admission's: the run's durable binding, its launch lock (held by the launched run for its life), the wall and the
// workflow database checked first; while either cannot be reached the pass stops, writes nothing and the next pass
// tries again. A durable record, <service-root>/admissions/resumes/<run>.json (InterruptedResumeV1), is written before
// the launch and never removed, so no run is launched for a resume twice.

const (
	// ResumeEvery is how often abcp serve's resume pass runs after the one at its start.
	ResumeEvery = 5 * time.Minute
	// ResumesPerPass bounds the resumes one pass launches.
	ResumesPerPass = 4
	// ResumeWithin bounds how long after its ledger last moved an interrupted run is resumed: one older (interrupted
	// while abcp serve itself was down for a day, or long before this binary) is left as it is.
	ResumeWithin = 24 * time.Hour
	// InterruptedResumeKind is the kind of a resume record.
	InterruptedResumeKind = "InterruptedResumeV1"
	// MaxInterruptedResumeBytes bounds a resume record.
	MaxInterruptedResumeBytes = 4 << 10
	// maxResumeRuns bounds the registered runs one pass looks at.
	maxResumeRuns = 20000
	// resumesDirectory is where the resume records are kept, in the admissions folder.
	resumesDirectory = "resumes"
)

// InterruptedRun is what the resume pass knows of one registered run: what its ledger says about a resume, and whether
// the process that owns it is alive.
type InterruptedRun struct {
	Interruption runctl.Interruption
	OwnerLive    bool
}

// InterruptedRuns lists the registered runs and reads one (abcp serve's, from the runtime catalog, the authoritative
// read model and the run's owner lease).
type InterruptedRuns interface {
	ListRuns(after string, limit int) ([]runtimecatalog.RunRegistrationV1, bool, error)
	InterruptedRun(ctx context.Context, runID string) (InterruptedRun, error)
}

// InterruptedResumeV1 records that abcp serve launched a run's one resume: the state and the attempts its ledger showed.
type InterruptedResumeV1 struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	State         string `json:"state"`
	Attempts      int    `json:"attempts"`
	LaunchedAt    string `json:"launched_at"`
}

// InterruptedResumer is abcp serve's resume pass.
type InterruptedResumer struct {
	admissions *Controller
	runs       InterruptedRuns
	log        io.Writer
	mu         sync.Mutex
	// settled holds the runs no later pass needs to read: ended, resumed, refused or not admitted here.
	settled map[string]bool
}

// Resumer answers the resume pass over runs, logging one line per launch and per wait to log.
func (c *Controller) Resumer(runs InterruptedRuns, log io.Writer) *InterruptedResumer {
	if log == nil {
		log = io.Discard
	}
	return &InterruptedResumer{admissions: c, runs: runs, log: log, settled: map[string]bool{}}
}

// Run passes over the registered runs at once and then every ResumeEvery until ctx ends.
func (r *InterruptedResumer) Run(ctx context.Context) {
	if r == nil || r.admissions == nil || r.runs == nil {
		return
	}
	ticker := time.NewTicker(ResumeEvery)
	defer ticker.Stop()
	r.logf("abcp interrupted runs resume ready=true every=%s per-pass=%d within=%s", ResumeEvery, ResumesPerPass, ResumeWithin)
	for {
		r.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *InterruptedResumer) logf(format string, args ...any) {
	fmt.Fprintf(r.log, format+"\n", args...)
}

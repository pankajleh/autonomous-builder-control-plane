package serviceapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Change jobs (docs/plans/change-jobs.md; Repo C design note IMPROVE_EXISTING_PRODUCTS §14): a customer's code, left by
// Repo C in a folder under the change source root, changed by the agent while every command it runs runs in the stack
// sandbox, and handed back as a patch. ABCP owns the job's lifecycle and evidence; Repo C opens the pull request.

// ChangeJobCreateV1 starts a job. Source and CacheKey are as a stack job's; Kind is fix, tests, assess or migrate; Words
// are the person's own, or a migration's step. The same RequestID with the same body replays the same job.
type ChangeJobCreateV1 struct {
	RequestID string `json:"request_id"`
	Source    string `json:"source"`
	CacheKey  string `json:"cache_key"`
	Kind      string `json:"kind"`
	Words     string `json:"words"`
	// Registries are the project's private registries for this job alone (§15.9); never kept on disk.
	Registries []RegistryV1 `json:"registries,omitempty"`
	// Stacks, when given, are the stacks the job may change and check, as a stack job's.
	Stacks []string `json:"stacks,omitempty"`
	// Targets, for an assessment or a migration's step only, are the versions the project moves to, "java": "21" (Repo C
	// design note LEGACY_MIGRATION §4 and §5): an assessment's checks run on them too, a step's agent and checks after on
	// them alone, as a stack job's pinned versions.
	Targets map[string]string `json:"targets,omitempty"`
}

// ChangeJobV1 is one job. State is QUEUED, RUNNING (with its Phase), HANDED_BACK, ASSESSED (an assessment with its Plan),
// NOT_HANDED_BACK, FAILED, STOPPED or UNKNOWN; Reason a stable code once it ended without being handed back. Before and
// After are the checks' parts: an assessment's After are its parts on their target versions.
type ChangeJobV1 struct {
	SchemaVersion string          `json:"schema_version"`
	ID            string          `json:"id"`
	RequestID     string          `json:"request_id"`
	Kind          string          `json:"kind"`
	State         string          `json:"state"`
	Phase         string          `json:"phase,omitempty"`
	CreatedAt     string          `json:"created_at"`
	StartedAt     string          `json:"started_at,omitempty"`
	EndedAt       string          `json:"ended_at,omitempty"`
	Before        []StackPartV1   `json:"before"`
	After         []StackPartV1   `json:"after"`
	Agent         *ChangeAgentV1  `json:"agent,omitempty"`
	Result        *ChangeResultV1 `json:"result,omitempty"`
	Proof         *ChangeProofV1  `json:"proof,omitempty"`
	Plan          *ChangePlanV1   `json:"plan,omitempty"`
	Reason        string          `json:"reason,omitempty"`
}

// ChangePlanV1 is an assessment's plan (Repo C design note LEGACY_MIGRATION §4): what is old in the project and what
// moving it takes, the moves (Paths), and the migration as ordered Steps, each one pull request small enough to review.
type ChangePlanV1 struct {
	Summary string             `json:"summary"`
	Paths   []ChangePlanPathV1 `json:"paths"`
	Steps   []ChangePlanStepV1 `json:"steps"`
}

// ChangePlanPathV1 is one move: a runtime, framework or library, from the version in use to the one to move to.
type ChangePlanPathV1 struct {
	What string `json:"what"`
	From string `json:"from"`
	To   string `json:"to"`
}

// ChangePlanStepV1 is one step: its title; its words, what to change, fit to start a change job with; its risk and size
// (low, medium or high; small, medium or large); the files it expects to touch; the tests it relies on; and whether the
// code needs tests written before it is safe (Repo C design note FIX_AND_TEST).
type ChangePlanStepV1 struct {
	Title      string   `json:"title"`
	Words      string   `json:"words"`
	Risk       string   `json:"risk"`
	Size       string   `json:"size"`
	Files      []string `json:"files"`
	Tests      string   `json:"tests"`
	TestsFirst bool     `json:"tests_first"`
}

// ChangeProofV1 is a fix's new tests run on the code as it was before the change (Repo C design note FIX_AND_TEST §3):
// FAILED_BEFORE (they fail there, so they show what was fixed), PASSED_BEFORE (they pass there too), NO_TEST (the change
// touched no test file) or NOT_RUN (the project's tests do not run, with why); the test files and the counts on the
// code before. It is shown, never required.
type ChangeProofV1 struct {
	State string        `json:"state"`
	Files []string      `json:"files"`
	Tests *StackTestsV1 `json:"tests,omitempty"`
	Note  string        `json:"note,omitempty"`
}

// ChangeAgentV1 is what the agent said and used: its summary and its review's, the commands it ran in the sandbox, the
// model and the tokens and cost its engine reported (input counts the cache's reads and writes too), and its time.
type ChangeAgentV1 struct {
	Summary          string  `json:"summary,omitempty"`
	Review           string  `json:"review,omitempty"`
	Commands         int     `json:"commands"`
	Model            string  `json:"model,omitempty"`
	InputTokens      int64   `json:"input_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	Seconds          float64 `json:"seconds"`
}

// ChangeResultV1 is the change: the patch's SHA-256, the files it touches (added, modified or deleted) and its lines.
type ChangeResultV1 struct {
	PatchSHA256 string         `json:"patch_sha256"`
	Files       []ChangeFileV1 `json:"files"`
	Insertions  int            `json:"insertions"`
	Deletions   int            `json:"deletions"`
}

type ChangeFileV1 struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

const ChangeJobSchemaV1 = "ChangeJobV1"

// ChangeJobController runs change jobs, for a service principal granted change.run.
type ChangeJobController interface {
	StartChangeJob(ctx context.Context, request ChangeJobCreateV1) (ChangeJobV1, error)
	ChangeJob(ctx context.Context, id string) (ChangeJobV1, error)
	ChangeJobPatch(ctx context.Context, id string) ([]byte, error)
	StopChangeJob(ctx context.Context, id string) (ChangeJobV1, error)
}

var (
	ErrChangeJobNotAccepted  = errors.New("change job not accepted")
	ErrChangeJobNotFound     = errors.New("change job not found")
	ErrChangeJobConflict     = errors.New("change job request conflicts")
	ErrChangeJobsBusy        = errors.New("change jobs busy")
	ErrChangeJobsUnavailable = errors.New("change jobs unavailable")
)

const (
	maxChangeJobBody = 40 << 10
	// MaxChangeWords bounds the person's words, in characters.
	MaxChangeWords = 4000
)

// Valid reports a request of the right shape: a request ID, a folder name, a cache digest, a kind and the words; targets
// only for an assessment or a migration's step.
func (r ChangeJobCreateV1) Valid() bool {
	return stackRequestID.MatchString(r.RequestID) && stackSourceName.MatchString(r.Source) && stackJobID.MatchString(r.CacheKey) &&
		(r.Kind == "fix" || r.Kind == "tests" || r.Kind == "assess" || r.Kind == "migrate") && utf8.ValidString(r.Words) && !strings.ContainsRune(r.Words, 0) &&
		strings.TrimSpace(r.Words) != "" && utf8.RuneCountInString(r.Words) <= MaxChangeWords && validRegistries(r.Registries) &&
		validStacks(r.Stacks) && validVersions(r.Targets) && (r.Kind == "assess" || r.Kind == "migrate" || len(r.Targets) == 0)
}

func (s *Server) mayRunChangeJobs(principal Principal) bool {
	return principal.PrincipalType == PrincipalService && s.authority.Match(principal, "change.run") == nil
}

// /v1/change-jobs, /v1/change-jobs/{id}, /v1/change-jobs/{id}/patch and /v1/change-jobs/{id}/stop
func (s *Server) changeJobs(w http.ResponseWriter, r *http.Request, principal Principal, id string) {
	bad := func() {
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid change job request", RequestID: id})
	}
	if s.reserved.ChangeJobs == nil {
		s.writeError(w, http.StatusServiceUnavailable, ErrorV1{Code: "change_jobs_unavailable", Message: "change jobs are not configured", RequestID: id})
		return
	}
	if !s.mayRunChangeJobs(principal) {
		s.writeDependencyError(w, id, ErrAuthorityDenied)
		return
	}
	if r.URL.RawPath != "" || r.URL.RawQuery != "" {
		bad()
		return
	}
	answer := func(job ChangeJobV1, err error, status int) {
		if err != nil {
			s.writeChangeJobError(w, id, err)
			return
		}
		s.writeJSON(w, status, job)
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/change-jobs")
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	switch {
	case rest == "" && r.Method == http.MethodPost:
		data, err := io.ReadAll(io.LimitReader(r.Body, maxChangeJobBody+1))
		var create ChangeJobCreateV1
		if err != nil || len(data) == 0 || len(data) > maxChangeJobBody || decodeStrictJSON(data, &create) != nil || !create.Valid() {
			bad()
			return
		}
		job, err := s.reserved.ChangeJobs.StartChangeJob(r.Context(), create)
		answer(job, err, http.StatusAccepted)
	case rest == "" || len(parts) > 2 || !stackJobID.MatchString(parts[0]):
		bad()
	case len(parts) == 1 && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		job, err := s.reserved.ChangeJobs.ChangeJob(r.Context(), parts[0])
		answer(job, err, http.StatusOK)
	case len(parts) == 2 && parts[1] == "patch" && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		patch, err := s.reserved.ChangeJobs.ChangeJobPatch(r.Context(), parts[0])
		if err != nil {
			s.writeChangeJobError(w, id, err)
			return
		}
		w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(patch)
	case len(parts) == 2 && parts[1] == "stop" && r.Method == http.MethodPost:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		job, err := s.reserved.ChangeJobs.StopChangeJob(r.Context(), parts[0])
		answer(job, err, http.StatusOK)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, ErrorV1{Code: "method_not_allowed", Message: "method not allowed", RequestID: id})
	}
}

func (s *Server) writeChangeJobError(w http.ResponseWriter, id string, err error) {
	switch {
	case errors.Is(err, ErrChangeJobNotAccepted):
		s.writeError(w, http.StatusUnprocessableEntity, ErrorV1{Code: "change_job_not_accepted", Message: "the source cannot be changed", RequestID: id})
	case errors.Is(err, ErrChangeJobNotFound):
		s.writeError(w, http.StatusNotFound, ErrorV1{Code: "change_job_not_found", Message: "change job not found", RequestID: id})
	case errors.Is(err, ErrChangeJobConflict):
		s.writeError(w, http.StatusConflict, ErrorV1{Code: "change_job_conflict", Message: "the request ID names another change job", RequestID: id})
	case errors.Is(err, ErrChangeJobsBusy):
		s.writeError(w, http.StatusTooManyRequests, ErrorV1{Code: "change_jobs_busy", Message: "too many change jobs are waiting", RequestID: id, Retryable: true})
	default:
		s.writeError(w, http.StatusServiceUnavailable, ErrorV1{Code: "change_jobs_unavailable", Message: "change jobs are unavailable", RequestID: id, Retryable: true})
	}
}

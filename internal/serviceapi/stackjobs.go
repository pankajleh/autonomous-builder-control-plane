package serviceapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Stack jobs (docs/plans/stack-jobs.md; Repo C design note IMPROVE_EXISTING_PRODUCTS §13): a customer's code, left by
// Repo C in a folder under the stack source root, installed, built and tested in the stack sandbox. ABCP owns the job's
// lifecycle and evidence; Repo C shows what this API answers.

// StackJobCreateV1 starts a job. Source names a folder directly under the stack source root; CacheKey the project's
// dependency cache, a digest Repo C makes (never a name). The same RequestID with the same body replays the same job.
type StackJobCreateV1 struct {
	RequestID string `json:"request_id"`
	Source    string `json:"source"`
	CacheKey  string `json:"cache_key"`
	// Registries are the project's private registries for this job alone (§15.9); never kept on disk.
	Registries []RegistryV1 `json:"registries,omitempty"`
	// Scan makes the job a security scan (Repo C design note SECURITY_AND_COMPLIANCE §11.1): each part is installed (and
	// built, for Java), then scanned by the image's own scanner; no tests run.
	Scan bool `json:"scan,omitempty"`
	// Stacks, when given, are the stacks the job may run (Repo C design note MORE_STACKS §10: a stack for administrators
	// first): a part of any other stack is found and not run, as a stack ABCP does not run yet. Without them, every stack
	// the profiles serve.
	Stacks []string `json:"stacks,omitempty"`
}

// StackJobV1 is one job. State is QUEUED, RUNNING, PASSED, FAILED, STOPPED or UNKNOWN (running when the controller
// stopped). Failure is a stable code once it ended without passing.
type StackJobV1 struct {
	SchemaVersion string        `json:"schema_version"`
	ID            string        `json:"id"`
	RequestID     string        `json:"request_id"`
	State         string        `json:"state"`
	CreatedAt     string        `json:"created_at"`
	StartedAt     string        `json:"started_at,omitempty"`
	EndedAt       string        `json:"ended_at,omitempty"`
	Parts         []StackPartV1 `json:"parts"`
	Failure       string        `json:"failure,omitempty"`
	// Scan says the job is a security scan.
	Scan bool `json:"scan,omitempty"`
}

// StackPartV1 is one part of the code: its folder ("" for the top), its stack (python, java, node, or unsupported with
// what was found), the version and tool chosen, and its steps.
type StackPartV1 struct {
	Path        string        `json:"path"`
	Stack       string        `json:"stack"`
	Found       string        `json:"found,omitempty"`
	Version     string        `json:"version,omitempty"`
	VersionNote string        `json:"version_note,omitempty"`
	Tool        string        `json:"tool,omitempty"`
	Steps       []StackStepV1 `json:"steps"`
	// Findings are a scan's findings for the part, checked and masked; FindingsMore counts those past the limit,
	// Licences how many installed packages each licence covers, and Checks which of the scan's four checks
	// (dependencies, secrets, code, licences) were done, failed or skipped. A check has none of them.
	Findings     []StackFindingV1  `json:"findings,omitempty"`
	FindingsMore int               `json:"findings_more,omitempty"`
	Licences     map[string]int    `json:"licences,omitempty"`
	Checks       map[string]string `json:"checks,omitempty"`
	// ScanAbout is what the scan used, for the person's report: each tool's version, and each data source's date.
	ScanAbout *StackScanAboutV1 `json:"scan_about,omitempty"`
}

// StackScanAboutV1 is what a scan used: each tool's version, and the date of each source of vulnerability data.
type StackScanAboutV1 struct {
	Tools map[string]string `json:"tools,omitempty"`
	Data  map[string]string `json:"data,omitempty"`
}

// StackFindingV1 is one finding of a scan: its kind (dependency, secret, code or licence), the tool and its rule, the
// severity, where it is, and for a dependency the package, its version, the version that fixes it, its licence and the
// advisories' IDs. A secret's value is never part of it.
type StackFindingV1 struct {
	Kind     string   `json:"kind"`
	Tool     string   `json:"tool"`
	Rule     string   `json:"rule,omitempty"`
	Severity string   `json:"severity"`
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
	Package  string   `json:"package,omitempty"`
	Version  string   `json:"version,omitempty"`
	Fixed    string   `json:"fixed,omitempty"`
	Licence  string   `json:"licence,omitempty"`
	IDs      []string `json:"ids,omitempty"`
	Title    string   `json:"title,omitempty"`
}

// StackStepV1 is one step (install, build or test): PASSED, FAILED, SKIPPED, STOPPED or NOT_RUN, the command shown,
// its time, its exit code, the last lines it printed (masked), the tests' counts when the tools reported them, and the
// project's line coverage when a change job measured it.
type StackStepV1 struct {
	Name       string           `json:"name"`
	State      string           `json:"state"`
	Command    string           `json:"command,omitempty"`
	Note       string           `json:"note,omitempty"`
	Seconds    float64          `json:"seconds"`
	ExitCode   *int             `json:"exit_code,omitempty"`
	OutputTail string           `json:"output_tail,omitempty"`
	Tests      *StackTestsV1    `json:"tests,omitempty"`
	Coverage   *StackCoverageV1 `json:"coverage,omitempty"`
}

// StackTestsV1 counts the tests the tools reported in JUnit form, or a Node test runner in its own summary.
type StackTestsV1 struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// StackCoverageV1 is a measured test step's line coverage (Repo C design note FIX_AND_TEST §4): the project's own lines
// the tests ran, its lines in all, and the tool that measured them with its version.
type StackCoverageV1 struct {
	Covered int    `json:"covered"`
	Total   int    `json:"total"`
	Tool    string `json:"tool"`
}

const StackJobSchemaV1 = "StackJobV1"

// StackJobController runs stack jobs, for a service principal granted stack.run.
type StackJobController interface {
	StartStackJob(ctx context.Context, request StackJobCreateV1) (StackJobV1, error)
	StackJob(ctx context.Context, id string) (StackJobV1, error)
	StopStackJob(ctx context.Context, id string) (StackJobV1, error)
}

var (
	ErrStackJobNotAccepted  = errors.New("stack job not accepted")
	ErrStackJobNotFound     = errors.New("stack job not found")
	ErrStackJobConflict     = errors.New("stack job request conflicts")
	ErrStackJobsBusy        = errors.New("stack jobs busy")
	ErrStackJobsUnavailable = errors.New("stack jobs unavailable")
	// ErrStackScansUnavailable: scans need the vulnerability data's folder (--stack-osv-root), which is not set.
	ErrStackScansUnavailable = errors.New("stack scans unavailable")
)

// maxStackJobBody leaves room for three private registries with their tokens (§15.9).
const maxStackJobBody = 16 << 10

var (
	stackJobID      = regexp.MustCompile(`^[a-f0-9]{64}$`)
	stackRequestID  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	stackSourceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
)

// Valid reports a request of the right shape: a request ID, a folder name and a cache digest.
func (r StackJobCreateV1) Valid() bool {
	return stackRequestID.MatchString(r.RequestID) && stackSourceName.MatchString(r.Source) && stackJobID.MatchString(r.CacheKey) &&
		validRegistries(r.Registries) && validStacks(r.Stacks)
}

func (s *Server) mayRunStackJobs(principal Principal) bool {
	return principal.PrincipalType == PrincipalService && s.authority.Match(principal, "stack.run") == nil
}

// /v1/stack-jobs, /v1/stack-jobs/{id} and /v1/stack-jobs/{id}/stop
func (s *Server) stackJobs(w http.ResponseWriter, r *http.Request, principal Principal, id string) {
	bad := func() {
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid stack job request", RequestID: id})
	}
	if s.reserved.StackJobs == nil {
		s.writeError(w, http.StatusServiceUnavailable, ErrorV1{Code: "stack_jobs_unavailable", Message: "stack jobs are not configured", RequestID: id})
		return
	}
	if !s.mayRunStackJobs(principal) {
		s.writeDependencyError(w, id, ErrAuthorityDenied)
		return
	}
	if r.URL.RawPath != "" || r.URL.RawQuery != "" {
		bad()
		return
	}
	answer := func(job StackJobV1, err error, status int) {
		if err != nil {
			s.writeStackJobError(w, id, err)
			return
		}
		s.writeJSON(w, status, job)
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/stack-jobs")
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	switch {
	case rest == "" && r.Method == http.MethodPost:
		data, err := io.ReadAll(io.LimitReader(r.Body, maxStackJobBody+1))
		var create StackJobCreateV1
		if err != nil || len(data) == 0 || len(data) > maxStackJobBody || decodeStrictJSON(data, &create) != nil || !create.Valid() {
			bad()
			return
		}
		job, err := s.reserved.StackJobs.StartStackJob(r.Context(), create)
		answer(job, err, http.StatusAccepted)
	case rest == "" || len(parts) > 2 || !stackJobID.MatchString(parts[0]):
		bad()
	case len(parts) == 1 && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		job, err := s.reserved.StackJobs.StackJob(r.Context(), parts[0])
		answer(job, err, http.StatusOK)
	case len(parts) == 2 && parts[1] == "stop" && r.Method == http.MethodPost:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		job, err := s.reserved.StackJobs.StopStackJob(r.Context(), parts[0])
		answer(job, err, http.StatusOK)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, ErrorV1{Code: "method_not_allowed", Message: "method not allowed", RequestID: id})
	}
}

func (s *Server) writeStackJobError(w http.ResponseWriter, id string, err error) {
	switch {
	case errors.Is(err, ErrStackJobNotAccepted):
		s.writeError(w, http.StatusUnprocessableEntity, ErrorV1{Code: "stack_job_not_accepted", Message: "the source cannot be checked", RequestID: id})
	case errors.Is(err, ErrStackJobNotFound):
		s.writeError(w, http.StatusNotFound, ErrorV1{Code: "stack_job_not_found", Message: "stack job not found", RequestID: id})
	case errors.Is(err, ErrStackJobConflict):
		s.writeError(w, http.StatusConflict, ErrorV1{Code: "stack_job_conflict", Message: "the request ID names another stack job", RequestID: id})
	case errors.Is(err, ErrStackScansUnavailable):
		s.writeError(w, http.StatusServiceUnavailable, ErrorV1{Code: "stack_scans_unavailable", Message: "scans are not configured", RequestID: id})
	case errors.Is(err, ErrStackJobsBusy):
		s.writeError(w, http.StatusTooManyRequests, ErrorV1{Code: "stack_jobs_busy", Message: "too many stack jobs are waiting", RequestID: id, Retryable: true})
	default:
		s.writeError(w, http.StatusServiceUnavailable, ErrorV1{Code: "stack_jobs_unavailable", Message: "stack jobs are unavailable", RequestID: id, Retryable: true})
	}
}

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
}

// StackStepV1 is one step (install, build or test): PASSED, FAILED, SKIPPED, STOPPED or NOT_RUN, the command shown,
// its time, its exit code, the last lines it printed (masked) and the tests' counts when the tools reported them.
type StackStepV1 struct {
	Name       string        `json:"name"`
	State      string        `json:"state"`
	Command    string        `json:"command,omitempty"`
	Note       string        `json:"note,omitempty"`
	Seconds    float64       `json:"seconds"`
	ExitCode   *int          `json:"exit_code,omitempty"`
	OutputTail string        `json:"output_tail,omitempty"`
	Tests      *StackTestsV1 `json:"tests,omitempty"`
}

// StackTestsV1 counts the tests the tools reported in JUnit form.
type StackTestsV1 struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
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
)

const maxStackJobBody = 2 << 10

var (
	stackJobID      = regexp.MustCompile(`^[a-f0-9]{64}$`)
	stackRequestID  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	stackSourceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
)

// Valid reports a request of the right shape: a request ID, a folder name and a cache digest.
func (r StackJobCreateV1) Valid() bool {
	return stackRequestID.MatchString(r.RequestID) && stackSourceName.MatchString(r.Source) && stackJobID.MatchString(r.CacheKey)
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
	case errors.Is(err, ErrStackJobsBusy):
		s.writeError(w, http.StatusTooManyRequests, ErrorV1{Code: "stack_jobs_busy", Message: "too many stack jobs are waiting", RequestID: id, Retryable: true})
	default:
		s.writeError(w, http.StatusServiceUnavailable, ErrorV1{Code: "stack_jobs_unavailable", Message: "stack jobs are unavailable", RequestID: id, Retryable: true})
	}
}

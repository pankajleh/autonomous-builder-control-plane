package serviceapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

// ErrRunNotFinished refuses to release the worktree of a run that is still executing.
var ErrRunNotFinished = errors.New("run is not finished")

// WorktreeReleaser releases controller-retained governed worktrees on product
// request: a closed task (one run) or a deleted tenant (one repository). A
// release pins every clean checkpoint and seals an eviction record first, so
// activity and exact-source preview survive it.
type WorktreeReleaser interface {
	ReleaseRunWorktree(ctx context.Context, principal Principal, run string, request WorktreeReleaseRequestV1) (WorktreeReleaseV1, error)
	ReleaseRepositoryWorktrees(ctx context.Context, principal Principal, request RepositoryWorktreeReleaseRequestV1) (RepositoryWorktreeReleaseV1, error)
}

type WorktreeReleaseRequestV1 struct {
	SchemaVersion int    `json:"schema_version"`
	Reason        string `json:"reason"`
}

type WorktreeReleaseV1 struct {
	SchemaVersion string `json:"schema_version"`
	RunID         string `json:"run_id"`
	// Status is RELEASED, ALREADY_RELEASED, NOT_RETAINED or NO_WORKTREE.
	Status string `json:"status"`
	// Reason and ReleasedAt describe the sealed eviction; empty without one.
	Reason     string `json:"reason"`
	ReleasedAt string `json:"released_at"`
}

type RepositoryWorktreeReleaseRequestV1 struct {
	SchemaVersion      int    `json:"schema_version"`
	RepositoryIdentity string `json:"repository_identity"`
	Reason             string `json:"reason"`
}

type RepositoryWorktreeReleaseV1 struct {
	SchemaVersion      string `json:"schema_version"`
	RepositoryIdentity string `json:"repository_identity"`
	Reason             string `json:"reason"`
	Released           int    `json:"released"`
	AlreadyReleased    int    `json:"already_released"`
	Unfinished         int    `json:"unfinished"`
	Failed             int    `json:"failed"`
}

func ValidateWorktreeReleaseRequestV1(r WorktreeReleaseRequestV1) error {
	if r.SchemaVersion != 1 || r.Reason != "task-closed" {
		return errors.New("invalid worktree release request")
	}
	return nil
}

func ValidateRepositoryWorktreeReleaseRequestV1(r RepositoryWorktreeReleaseRequestV1) error {
	if r.SchemaVersion != 1 || r.Reason != "tenant-deleted" || r.RepositoryIdentity == "" || len(r.RepositoryIdentity) > 512 {
		return errors.New("invalid repository worktree release request")
	}
	return nil
}

func ValidateWorktreeReleaseV1(v WorktreeReleaseV1, run string) error {
	sealed := v.Status == "RELEASED" || v.Status == "ALREADY_RELEASED"
	if v.SchemaVersion != "WorktreeReleaseV1" || v.RunID != run || runtimecatalog.ValidateIdentifier(run) != nil {
		return errors.New("invalid worktree release")
	}
	switch v.Status {
	case "RELEASED", "ALREADY_RELEASED", "NOT_RETAINED", "NO_WORKTREE":
	default:
		return errors.New("invalid worktree release status")
	}
	if _, err := time.Parse(time.RFC3339Nano, v.ReleasedAt); sealed != (err == nil) || sealed != (v.Reason != "") {
		return errors.New("invalid worktree release record")
	}
	return nil
}

func (s *Server) mayReleaseWorktrees(principal Principal) bool {
	return principal.PrincipalType == PrincipalService && s.authority.Match(principal, "worktree.release") == nil
}

func (s *Server) readReleaseBody(w http.ResponseWriter, r *http.Request, id string, into any) bool {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1025))
	if r.Method != http.MethodPost || r.URL.RawQuery != "" || err != nil || len(data) == 0 || len(data) > 1024 || decodeStrictJSON(data, into) != nil {
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid worktree release request", RequestID: id})
		return false
	}
	return true
}

// POST /v1/runs/{run}/worktree/release
func (s *Server) runWorktreeRelease(w http.ResponseWriter, r *http.Request, principal Principal, id, run string) {
	if !s.mayReleaseWorktrees(principal) {
		s.writeDependencyError(w, id, ErrAuthorityDenied)
		return
	}
	var request WorktreeReleaseRequestV1
	if !s.readReleaseBody(w, r, id, &request) {
		return
	}
	if ValidateWorktreeReleaseRequestV1(request) != nil {
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid worktree release request", RequestID: id})
		return
	}
	if !s.requireRegisteredRun(w, id, run) {
		return
	}
	if s.reserved.Worktrees == nil {
		s.writeDependencyError(w, id, ErrUnsupportedCapability)
		return
	}
	result, err := s.reserved.Worktrees.ReleaseRunWorktree(r.Context(), principal, run, request)
	if err != nil {
		s.writeDependencyError(w, id, err)
		return
	}
	if ValidateWorktreeReleaseV1(result, run) != nil {
		s.writeDependencyError(w, id, ErrInternalDurableSubstrate)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// POST /v1/worktrees/release
func (s *Server) repositoryWorktreeRelease(w http.ResponseWriter, r *http.Request, principal Principal, id string) {
	if !s.mayReleaseWorktrees(principal) {
		s.writeDependencyError(w, id, ErrAuthorityDenied)
		return
	}
	var request RepositoryWorktreeReleaseRequestV1
	if !s.readReleaseBody(w, r, id, &request) {
		return
	}
	if ValidateRepositoryWorktreeReleaseRequestV1(request) != nil {
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid repository worktree release request", RequestID: id})
		return
	}
	if s.reserved.Worktrees == nil {
		s.writeDependencyError(w, id, ErrUnsupportedCapability)
		return
	}
	result, err := s.reserved.Worktrees.ReleaseRepositoryWorktrees(r.Context(), principal, request)
	if err != nil {
		s.writeDependencyError(w, id, err)
		return
	}
	if result.SchemaVersion != "RepositoryWorktreeReleaseV1" || result.RepositoryIdentity != request.RepositoryIdentity || result.Reason != request.Reason ||
		result.Released < 0 || result.AlreadyReleased < 0 || result.Unfinished < 0 || result.Failed < 0 {
		s.writeDependencyError(w, id, ErrInternalDurableSubstrate)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

package serviceapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

var (
	// ErrRunOnHold refuses to forget a run an operator holds (docs/architecture/RUN_EVIDENCE_RETENTION_POLICY.md).
	ErrRunOnHold = errors.New("run is on hold")
	// ErrRunForgotten answers a read of a forgotten run: its activity, timeline, evidence and previews are gone.
	ErrRunForgotten = errors.New("run is forgotten")
)

// RunForgetter forgets a finished run on product request (Repo C design note APP_DELETION.md §4): an app or an
// account was deleted. A forget is sealed in a record before anything is removed, so an interrupted one is finished by
// the next call, and a replay or any later call answers that record.
type RunForgetter interface {
	ForgetRun(ctx context.Context, principal Principal, run string, request RunForgetRequestV1) (RunForgetV1, error)
	// RunForgotten reports whether a forget of the run is sealed. An unreadable record is an error, never false.
	RunForgotten(run string) (bool, error)
}

type RunForgetRequestV1 struct {
	SchemaVersion int    `json:"schema_version"`
	Reason        string `json:"reason"`
	RequestID     string `json:"request_id"`
}

// ForgottenRefV1 is one ref the forget removes, only while it still names SHA.
type ForgottenRefV1 struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type RunForgetV1 struct {
	SchemaVersion string `json:"schema_version"`
	RunID         string `json:"run_id"`
	// Status is FORGOTTEN for the request that sealed the forget (and its replays), ALREADY_FORGOTTEN for any other.
	Status string `json:"status"`
	// Reason, RequestID and ForgottenAt are the sealing request's, whichever request asks.
	Reason      string           `json:"reason"`
	RequestID   string           `json:"request_id"`
	ForgottenAt string           `json:"forgotten_at"`
	RefsRemoved []ForgottenRefV1 `json:"refs_removed"`
}

var (
	canonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	forgottenSHA  = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

// ValidForgetReason reports whether the product may ask to forget a run for reason.
func ValidForgetReason(reason string) bool {
	return reason == "app-deleted" || reason == "account-deleted"
}

func ValidateRunForgetRequestV1(r RunForgetRequestV1) error {
	if r.SchemaVersion != 1 || !ValidForgetReason(r.Reason) || !canonicalUUID.MatchString(r.RequestID) {
		return errors.New("invalid run forget request")
	}
	return nil
}

// ForgottenRefOf reports whether ref is one of run's refs a forget removes: its branch refs/heads/abcp/<run>, or a ref
// in refs/abcp/<kind>/<run>/ (its pinned checkpoints among them).
func ForgottenRefOf(run, ref string) bool {
	if ref == "refs/heads/abcp/"+run {
		return true
	}
	parts := strings.Split(ref, "/")
	return len(parts) >= 4 && parts[0] == "refs" && parts[1] == "abcp" && parts[2] != "" && parts[3] == run && !strings.Contains(ref, "//")
}

func ValidateRunForgetV1(v RunForgetV1, run string) error {
	if v.SchemaVersion != "RunForgetV1" || v.RunID != run || runtimecatalog.ValidateIdentifier(run) != nil ||
		(v.Status != "FORGOTTEN" && v.Status != "ALREADY_FORGOTTEN") || !ValidForgetReason(v.Reason) || !canonicalUUID.MatchString(v.RequestID) || v.RefsRemoved == nil {
		return errors.New("invalid run forget")
	}
	if _, err := time.Parse(time.RFC3339Nano, v.ForgottenAt); err != nil {
		return errors.New("invalid run forget time")
	}
	for _, r := range v.RefsRemoved {
		if !ForgottenRefOf(run, r.Ref) || !forgottenSHA.MatchString(r.SHA) {
			return errors.New("invalid forgotten ref")
		}
	}
	return nil
}

// POST /v1/runs/{run}/forget
func (s *Server) runForget(w http.ResponseWriter, r *http.Request, principal Principal, id, run string) {
	if principal.PrincipalType != PrincipalService || s.authority.Match(principal, "run.forget") != nil {
		s.writeDependencyError(w, id, ErrAuthorityDenied)
		return
	}
	var request RunForgetRequestV1
	if !s.readSmallCommand(w, r, id, "invalid run forget request", &request) {
		return
	}
	if ValidateRunForgetRequestV1(request) != nil {
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid run forget request", RequestID: id})
		return
	}
	if !s.requireRegisteredRun(w, id, run) {
		return
	}
	if s.reserved.Forget == nil {
		s.writeDependencyError(w, id, ErrUnsupportedCapability)
		return
	}
	result, err := s.reserved.Forget.ForgetRun(r.Context(), principal, run, request)
	if err != nil {
		s.writeDependencyError(w, id, err)
		return
	}
	if ValidateRunForgetV1(result, run) != nil {
		s.writeDependencyError(w, id, ErrInternalDurableSubstrate)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// refuseForgotten answers 410 run_forgotten, and false, for a run whose forget is sealed. Without a forgetter no run is
// forgotten.
func (s *Server) refuseForgotten(w http.ResponseWriter, id, run string) bool {
	if s.reserved.Forget == nil {
		return true
	}
	forgotten, err := s.reserved.Forget.RunForgotten(run)
	if err != nil {
		s.writeDependencyError(w, id, ErrInternalDurableSubstrate)
		return false
	}
	if forgotten {
		s.writeDependencyError(w, id, ErrRunForgotten)
		return false
	}
	return true
}

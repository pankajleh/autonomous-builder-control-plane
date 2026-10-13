package serviceapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Account lanes (Repo C design note AI_KEYS_AND_MODELS §9.5, step 4c) are
// engine lanes the operator set up for an administrator's own Codex or Claude
// account. ABCP signs them in and out: a Codex lane by Codex's device sign-in,
// whose link and one-time code the administrator opens on their own device; a
// Claude lane by a token made with `claude setup-token`, which only ABCP keeps.
// No credential ever leaves ABCP.

// AccountLaneV1 is one account lane as Repo C's page shows it.
type AccountLaneV1 struct {
	Lane     string `json:"lane"`
	Executor string `json:"executor"`
	// State is "signed_out", "signing_in" (a Codex device sign-in waits for the
	// administrator) or "signed_in".
	State string `json:"state"`
	// Since is when the lane was signed in, RFC 3339 UTC; omitted otherwise.
	Since  string               `json:"since,omitempty"`
	Device *AccountLaneDeviceV1 `json:"device,omitempty"`
}

// AccountLaneDeviceV1 is a pending Codex device sign-in: the page the
// administrator opens and the code they enter there.
type AccountLaneDeviceV1 struct {
	VerificationURL string `json:"verification_url"`
	UserCode        string `json:"user_code"`
	ExpiresAt       string `json:"expires_at"`
}

// AccountLanesV1 lists every account lane, in the order the operator gave.
type AccountLanesV1 struct {
	SchemaVersion string          `json:"schema_version"`
	Lanes         []AccountLaneV1 `json:"lanes"`
}

// AccountLaneSignInV1 is a sign-in request: a Claude lane's setup token, or
// nothing for a Codex lane's device sign-in.
type AccountLaneSignInV1 struct {
	Token string `json:"token,omitempty"`
}

// AccountLaneController keeps the account lanes.
type AccountLaneController interface {
	AccountLanes(context.Context) (AccountLanesV1, error)
	SignIn(context.Context, string, AccountLaneSignInV1) (AccountLaneV1, error)
	SignOut(context.Context, string) (AccountLaneV1, error)
}

var (
	ErrAccountLaneUnknown     = errors.New("account lane unknown")
	ErrAccountLaneInvalid     = errors.New("account lane request invalid")
	ErrAccountLaneSignedIn    = errors.New("account lane signed in")
	ErrAccountLaneUnavailable = errors.New("account lane unavailable")
)

// AccountLanesSchemaV1 names the GET /v1/account-lanes answer.
const AccountLanesSchemaV1 = "AccountLanesV1"

// maxAccountLaneBody bounds a sign-in request.
const maxAccountLaneBody = 4 << 10

var accountLaneNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func (s *Server) mayControlAccounts(principal Principal) bool {
	return principal.PrincipalType == PrincipalService && s.authority.Match(principal, "accounts.control") == nil
}

// /v1/account-lanes, /v1/account-lanes/{lane} and /v1/account-lanes/{lane}/sign-in
func (s *Server) accountLanes(w http.ResponseWriter, r *http.Request, principal Principal, id string) {
	bad := func() {
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid account lane request", RequestID: id})
	}
	if s.reserved.AccountLanes == nil {
		s.writeError(w, http.StatusNotImplemented, ErrorV1{Code: "unsupported_capability", Message: "account lanes are not available", RequestID: id})
		return
	}
	if !s.mayControlAccounts(principal) {
		s.writeDependencyError(w, id, ErrAuthorityDenied)
		return
	}
	if r.URL.RawPath != "" || r.URL.RawQuery != "" {
		bad()
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/account-lanes")
	switch {
	case rest == "" && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		lanes, err := s.reserved.AccountLanes.AccountLanes(r.Context())
		if err != nil {
			s.writeAccountLaneError(w, id, err)
			return
		}
		s.writeJSON(w, http.StatusOK, lanes)
	case strings.HasSuffix(rest, "/sign-in") && r.Method == http.MethodPost:
		lane := strings.TrimSuffix(strings.TrimPrefix(rest, "/"), "/sign-in")
		if !accountLaneNamePattern.MatchString(lane) {
			bad()
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, maxAccountLaneBody+1))
		var signIn AccountLaneSignInV1
		if err != nil || len(data) == 0 || len(data) > maxAccountLaneBody || decodeStrictJSON(data, &signIn) != nil {
			bad()
			return
		}
		answer, err := s.reserved.AccountLanes.SignIn(r.Context(), lane, signIn)
		if err != nil {
			s.writeAccountLaneError(w, id, err)
			return
		}
		s.writeJSON(w, http.StatusOK, answer)
	case strings.Count(rest, "/") == 1 && r.Method == http.MethodDelete:
		lane := strings.TrimPrefix(rest, "/")
		if !accountLaneNamePattern.MatchString(lane) || !requestBodyEmpty(r) {
			bad()
			return
		}
		answer, err := s.reserved.AccountLanes.SignOut(r.Context(), lane)
		if err != nil {
			s.writeAccountLaneError(w, id, err)
			return
		}
		s.writeJSON(w, http.StatusOK, answer)
	default:
		bad()
	}
}

func (s *Server) writeAccountLaneError(w http.ResponseWriter, id string, err error) {
	switch {
	case errors.Is(err, ErrAccountLaneUnknown):
		s.writeError(w, http.StatusNotFound, ErrorV1{Code: "account_lane_unknown", Message: "account lane is not configured", RequestID: id})
	case errors.Is(err, ErrAccountLaneInvalid):
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid account lane request", RequestID: id})
	case errors.Is(err, ErrAccountLaneSignedIn):
		s.writeError(w, http.StatusConflict, ErrorV1{Code: "account_lane_signed_in", Message: "account lane is already signed in", RequestID: id})
	default:
		s.writeError(w, http.StatusServiceUnavailable, ErrorV1{Code: "account_lane_unavailable", Message: "account lane is unavailable", RequestID: id, Retryable: true})
	}
}

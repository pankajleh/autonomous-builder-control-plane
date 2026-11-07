package serviceapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Lane keys (Repo C design note BUILD_ENGINE_RULES.md §9, slice 5; docs/plans/lane-keys.md): a key entered on Repo C's
// AI and API keys page reaches the builds of the lanes that pay with a provider's key. A key lane is an open-model
// provider a build profile names (executor.provider): its key file is the one each build of those profiles reads when
// it starts. ABCP writes that file when Repo C sends the key, and removes it when Repo C takes the key away. No answer,
// log line, ledger event or evidence item carries the key: only its fingerprint.

// LaneKindKey is the one kind of lane that takes a key here: an open-model provider's.
const LaneKindKey = "key"

// Lane key states.
const (
	// LaneKeySet: the lane's key file holds a key the next build start reads.
	LaneKeySet = "set"
	// LaneKeyMissing: there is no key file; the lane's engines read lane_no_key.
	LaneKeyMissing = "missing"
	// LaneKeyUnreadable: a file is there but no build would read it (not the controller's private file, or not one
	// key); the lane's engines read lane_unreadable. Setting a key replaces it.
	LaneKeyUnreadable = "unreadable"
)

// LaneV1 is one key lane as Repo C's page shows it.
type LaneV1 struct {
	Lane string `json:"lane"`
	Kind string `json:"kind"`
	// State is LaneKeySet, LaneKeyMissing or LaneKeyUnreadable.
	State string `json:"state"`
	// Fingerprint is the first 8 hex of the SHA-256 of the key, so Repo C can show which key is set; only when set.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Profiles are the build profiles whose builds read this lane's key, sorted.
	Profiles []string `json:"profiles"`
}

// LanesV1 lists every key lane the build profiles name, by lane name.
type LanesV1 struct {
	SchemaVersion string   `json:"schema_version"`
	Lanes         []LaneV1 `json:"lanes"`
}

// LanesSchemaV1 names the GET /v1/lanes answer.
const LanesSchemaV1 = "LanesV1"

// LaneKeyRequestV1 is PUT /v1/lanes/{lane}/key's body. It prints without the key.
type LaneKeyRequestV1 struct {
	Key string `json:"key"`
}

// String hides the key.
func (LaneKeyRequestV1) String() string { return "LaneKeyRequestV1{[lane key]}" }

// GoString hides the key from %#v.
func (r LaneKeyRequestV1) GoString() string { return r.String() }

// LaneKeyController keeps the key lanes' key files.
type LaneKeyController interface {
	Lanes(context.Context) (LanesV1, error)
	SetKey(context.Context, string, LaneKeyRequestV1) (LaneV1, error)
	RemoveKey(context.Context, string) (LaneV1, error)
}

var (
	// ErrLaneUnknown: no build profile names the lane.
	ErrLaneUnknown = errors.New("lane unknown")
	// ErrLaneNotKey: a profile names the lane, but it is an engine lane file or an account lane, never a key lane.
	ErrLaneNotKey = errors.New("lane takes no key")
	// ErrLaneKeyInvalid: the key does not have the provider's shape.
	ErrLaneKeyInvalid = errors.New("lane key invalid")
	// ErrLaneKeyUnavailable: the key file could not be written, removed or read.
	ErrLaneKeyUnavailable = errors.New("lane key unavailable")
)

// MaxLaneKeyBody bounds PUT /v1/lanes/{lane}/key's body: a key is at most 512 bytes.
const MaxLaneKeyBody = 4 << 10

var laneNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func (s *Server) mayControlLanes(principal Principal) bool {
	return principal.PrincipalType == PrincipalService && s.authority.Match(principal, "lanes.control") == nil
}

// /v1/lanes and /v1/lanes/{lane}/key
func (s *Server) lanes(w http.ResponseWriter, r *http.Request, principal Principal, id string) {
	bad := func() {
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid lane request", RequestID: id})
	}
	if s.reserved.LaneKeys == nil {
		s.writeError(w, http.StatusNotImplemented, ErrorV1{Code: "unsupported_capability", Message: "lane keys are not available", RequestID: id})
		return
	}
	if !s.mayControlLanes(principal) {
		s.writeDependencyError(w, id, ErrAuthorityDenied)
		return
	}
	if r.URL.RawPath != "" || r.URL.RawQuery != "" {
		bad()
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/lanes")
	if rest == "" {
		if r.Method != http.MethodGet || !requestBodyEmpty(r) {
			bad()
			return
		}
		lanes, err := s.reserved.LaneKeys.Lanes(r.Context())
		if err != nil {
			s.writeLaneError(w, id, err)
			return
		}
		s.writeJSON(w, http.StatusOK, lanes)
		return
	}
	lane, found := strings.CutSuffix(strings.TrimPrefix(rest, "/"), "/key")
	if !found || !strings.HasPrefix(rest, "/") || !laneNamePattern.MatchString(lane) {
		bad()
		return
	}
	switch r.Method {
	case http.MethodPut:
		if r.ContentLength > MaxLaneKeyBody {
			s.writeError(w, http.StatusRequestEntityTooLarge, ErrorV1{Code: "request_too_large", Message: "a lane key request is at most 4 KiB", RequestID: id})
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, MaxLaneKeyBody+1))
		if len(data) > MaxLaneKeyBody {
			s.writeError(w, http.StatusRequestEntityTooLarge, ErrorV1{Code: "request_too_large", Message: "a lane key request is at most 4 KiB", RequestID: id})
			return
		}
		var request LaneKeyRequestV1
		if err != nil || len(data) == 0 || decodeStrictJSON(data, &request) != nil {
			bad()
			return
		}
		answer, err := s.reserved.LaneKeys.SetKey(r.Context(), lane, request)
		if err != nil {
			s.writeLaneError(w, id, err)
			return
		}
		s.writeJSON(w, http.StatusOK, answer)
	case http.MethodDelete:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		answer, err := s.reserved.LaneKeys.RemoveKey(r.Context(), lane)
		if err != nil {
			s.writeLaneError(w, id, err)
			return
		}
		s.writeJSON(w, http.StatusOK, answer)
	default:
		bad()
	}
}

// writeLaneError answers a lane controller's error with its stable code. Messages are fixed: none carries a key.
func (s *Server) writeLaneError(w http.ResponseWriter, id string, err error) {
	switch {
	case errors.Is(err, ErrLaneUnknown):
		s.writeError(w, http.StatusNotFound, ErrorV1{Code: "lane_unknown", Message: "no build profile names this lane", RequestID: id})
	case errors.Is(err, ErrLaneNotKey):
		s.writeError(w, http.StatusConflict, ErrorV1{Code: "lane_not_key", Message: "this lane takes no key: it is an account or an engine lane file", RequestID: id})
	case errors.Is(err, ErrLaneKeyInvalid):
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "lane_key_invalid", Message: "the key does not have this provider's shape", RequestID: id})
	default:
		s.writeError(w, http.StatusServiceUnavailable, ErrorV1{Code: "lane_key_unavailable", Message: "the lane's key file is unavailable", RequestID: id, Retryable: true})
	}
}

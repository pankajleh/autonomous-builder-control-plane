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
//
// An API-key engine lane takes a key too once the operator names it (abcp serve --lane-key-engine-lanes; the slice's
// second half): claude-api's ANTHROPIC_API_KEY line, codex-api's OPENAI_API_KEY line. Its lane file is rewritten whole
// with that one line changed, every other line kept.

// LaneKindKey is an open-model provider's key lane: its key file holds the key alone.
const LaneKindKey = "key"

// LaneKindEngine is an API-key engine lane the operator named: one line of its lane file holds the key.
const LaneKindEngine = "engine"

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
	// Provider is the vendor whose key the lane takes: an open-model provider's id (together, groq), or anthropic or
	// openai for an engine lane.
	Provider string `json:"provider"`
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
	// ErrLaneNotKey: a profile names the lane, but it is an account lane, an engine lane the operator did not name, or
	// one paid another way (Bedrock, a setup token, another provider's address), never a key lane.
	ErrLaneNotKey = errors.New("lane takes no key")
	// ErrLaneKeyInvalid: the key does not have the provider's shape.
	ErrLaneKeyInvalid = errors.New("lane key invalid")
	// ErrLaneKeyUnavailable: the key file could not be written, removed or read.
	ErrLaneKeyUnavailable = errors.New("lane key unavailable")
	// ErrLaneKeyRequired: removing the key would leave an engine lane's file with no setting, which no profile loads.
	ErrLaneKeyRequired = errors.New("lane key required")
	// ErrLaneKeyChanged: the request named the key it expects the lane to hold (If-Match), and the lane holds another
	// key or none.
	ErrLaneKeyChanged = errors.New("lane key changed")
)

// laneKeyExpected carries an If-Match fingerprint from the route to the lane controller.
type laneKeyExpected struct{}

// WithExpectedLaneKey answers ctx carrying the fingerprint of the key a lane must hold for a set or a removal to go
// ahead (If-Match).
func WithExpectedLaneKey(ctx context.Context, fingerprint string) context.Context {
	return context.WithValue(ctx, laneKeyExpected{}, fingerprint)
}

// ExpectedLaneKey answers the fingerprint a request expects its lane to hold, if it names one.
func ExpectedLaneKey(ctx context.Context) (string, bool) {
	fingerprint, ok := ctx.Value(laneKeyExpected{}).(string)
	return fingerprint, ok
}

// laneFingerprintPattern is a key's fingerprint as the lanes answer it: the first 8 hex of its SHA-256.
var laneFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{8}$`)

// expectedLaneKey reads If-Match: absent, or exactly one quoted fingerprint.
func expectedLaneKey(r *http.Request) (string, bool, bool) {
	values := r.Header.Values("If-Match")
	if len(values) == 0 {
		return "", false, true
	}
	if len(values) != 1 {
		return "", false, false
	}
	tag, ok := strings.CutPrefix(values[0], `"`)
	if !ok {
		return "", false, false
	}
	tag, ok = strings.CutSuffix(tag, `"`)
	if !ok || !laneFingerprintPattern.MatchString(tag) {
		return "", false, false
	}
	return tag, true, true
}

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
	expected, expects, wellFormed := expectedLaneKey(r)
	rest := strings.TrimPrefix(r.URL.Path, "/v1/lanes")
	if !wellFormed {
		bad()
		return
	}
	if rest == "" {
		if r.Method != http.MethodGet || !requestBodyEmpty(r) || expects {
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
	// If-Match names the key the lane must hold now: the controller checks it under its write lock.
	ctx := r.Context()
	if expects {
		ctx = WithExpectedLaneKey(ctx, expected)
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
		answer, err := s.reserved.LaneKeys.SetKey(ctx, lane, request)
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
		answer, err := s.reserved.LaneKeys.RemoveKey(ctx, lane)
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
	case errors.Is(err, ErrLaneKeyRequired):
		s.writeError(w, http.StatusConflict, ErrorV1{Code: "lane_key_required", Message: "this lane's file holds nothing but its key, so it keeps it", RequestID: id})
	case errors.Is(err, ErrLaneKeyChanged):
		s.writeError(w, http.StatusPreconditionFailed, ErrorV1{Code: "lane_key_changed", Message: "the lane does not hold the key If-Match names", RequestID: id})
	default:
		s.writeError(w, http.StatusServiceUnavailable, ErrorV1{Code: "lane_key_unavailable", Message: "the lane's key file is unavailable", RequestID: id, Retryable: true})
	}
}

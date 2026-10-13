package serviceapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// Remote targets (Repo C design note BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md §5,
// §11 BF-14.1) are Linux servers ABCP reaches over SSH: our own servers for
// DeployFlix hosting and, later, customers' own servers. ABCP makes each
// target's key pair and keeps its private key; the server's owner puts the
// public key on the server. ABCP pins the server's host key once the owner
// has confirmed its fingerprint, and checks the server with fixed, read-only
// commands. Nothing on the server is changed, and no private key ever leaves
// ABCP.

// TargetV1 is one target as Repo C's Servers page shows it.
type TargetV1 struct {
	Key     string `json:"key"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	User    string `json:"user"`
	// PublicKey is the line to add to the user's ~/.ssh/authorized_keys.
	PublicKey string `json:"public_key"`
	// State is "new" (the server's host key not yet seen),
	// "fingerprint_pending" (seen; waiting for the owner's confirmation) or
	// "confirmed" (pinned; preflight runs).
	State string `json:"state"`
	// HostKeyFingerprint is the SHA256 fingerprint seen (pending) or pinned (confirmed).
	HostKeyFingerprint string             `json:"host_key_fingerprint,omitempty"`
	CreatedAt          string             `json:"created_at"`
	Preflight          *TargetPreflightV1 `json:"preflight,omitempty"`
}

// TargetsV1 lists every target, oldest first.
type TargetsV1 struct {
	SchemaVersion string     `json:"schema_version"`
	Targets       []TargetV1 `json:"targets"`
}

// TargetCreateV1 registers a target. Port defaults to 22 and User to "autobuild".
type TargetCreateV1 struct {
	Address string `json:"address"`
	Port    int    `json:"port,omitempty"`
	User    string `json:"user,omitempty"`
}

// TargetConfirmV1 is the fingerprint the owner checked on the server.
type TargetConfirmV1 struct {
	Fingerprint string `json:"fingerprint"`
}

// TargetPreflightV1 is the last read-only check of a target.
type TargetPreflightV1 struct {
	CheckedAt string `json:"checked_at"`
	// Outcome is "checked", or why the server could not be checked:
	// "unreachable", "refused_address", "auth_refused", "host_key_changed",
	// "timeout" or "failed".
	Outcome string `json:"outcome"`
	// Ready is true when every required check passed.
	Ready  bool            `json:"ready"`
	Facts  *TargetFactsV1  `json:"facts,omitempty"`
	Checks []TargetCheckV1 `json:"checks,omitempty"`
}

// TargetFactsV1 is what the server reported about itself.
type TargetFactsV1 struct {
	OS               string `json:"os"`
	OSID             string `json:"os_id"`
	OSVersion        string `json:"os_version"`
	Arch             string `json:"arch"`
	CPUs             int    `json:"cpus"`
	MemoryMB         int    `json:"memory_mb"`
	DiskFreeMB       int    `json:"disk_free_mb"`
	ContainerRuntime string `json:"container_runtime,omitempty"`
	Rootless         bool   `json:"rootless"`
	GVisor           bool   `json:"gvisor"`
}

// TargetCheckV1 is one named check. Name is one of: os_supported, arch,
// memory, disk, not_root, container_runtime, rootless, gvisor, port_80_free,
// port_443_free, clock_synced, registry_reachable.
type TargetCheckV1 struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Required bool   `json:"required"`
	Value    string `json:"value"`
}

// TargetController keeps the remote targets.
type TargetController interface {
	Targets(context.Context) (TargetsV1, error)
	Target(context.Context, string) (TargetV1, error)
	CreateTarget(context.Context, string, TargetCreateV1) (TargetV1, error)
	ConfirmTarget(context.Context, string, TargetConfirmV1) (TargetV1, error)
	PreflightTarget(context.Context, string) (TargetV1, error)
	DeleteTarget(context.Context, string) error
}

var (
	ErrTargetUnknown             = errors.New("target unknown")
	ErrTargetInvalid             = errors.New("target request invalid")
	ErrTargetExists              = errors.New("target exists")
	ErrTargetAddressRefused      = errors.New("target address refused")
	ErrTargetFingerprintMismatch = errors.New("target fingerprint mismatch")
	ErrTargetLimit               = errors.New("target limit reached")
	ErrTargetUnavailable         = errors.New("target unavailable")
)

// TargetsSchemaV1 names the GET /v1/targets answer.
const TargetsSchemaV1 = "TargetsV1"

const maxTargetBody = 4 << 10

var targetKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func (s *Server) mayControlTargets(principal Principal) bool {
	return principal.PrincipalType == PrincipalService && s.authority.Match(principal, "targets.control") == nil
}

// /v1/targets, /v1/targets/{key}, /v1/targets/{key}/confirm and /v1/targets/{key}/preflight
func (s *Server) targets(w http.ResponseWriter, r *http.Request, principal Principal, id string) {
	bad := func() {
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid target request", RequestID: id})
	}
	if s.reserved.Targets == nil {
		s.writeError(w, http.StatusNotImplemented, ErrorV1{Code: "unsupported_capability", Message: "remote targets are not available", RequestID: id})
		return
	}
	if !s.mayControlTargets(principal) {
		s.writeDependencyError(w, id, ErrAuthorityDenied)
		return
	}
	if r.URL.RawPath != "" || r.URL.RawQuery != "" {
		bad()
		return
	}
	body := func(into any) bool {
		data, err := io.ReadAll(io.LimitReader(r.Body, maxTargetBody+1))
		return err == nil && len(data) > 0 && len(data) <= maxTargetBody && decodeStrictJSON(data, into) == nil
	}
	answer := func(target TargetV1, err error, status int) {
		if err != nil {
			s.writeTargetError(w, id, err)
			return
		}
		s.writeJSON(w, status, target)
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/targets")
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	switch {
	case rest == "" && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		list, err := s.reserved.Targets.Targets(r.Context())
		if err != nil {
			s.writeTargetError(w, id, err)
			return
		}
		s.writeJSON(w, http.StatusOK, list)
	case rest == "" || !targetKeyPattern.MatchString(parts[0]) || len(parts) > 6:
		bad()
	case len(parts) >= 2 && (parts[1] == "plans" || parts[1] == "apply" || parts[1] == "operations" || parts[1] == "sites" ||
		parts[1] == "databases" || parts[1] == "clean" || parts[1] == "drift"):
		s.targetDeployments(w, r, id, parts, body)
	case len(parts) > 2:
		bad()
	case len(parts) == 1 && r.Method == http.MethodPut:
		var create TargetCreateV1
		if !body(&create) {
			bad()
			return
		}
		target, err := s.reserved.Targets.CreateTarget(r.Context(), parts[0], create)
		answer(target, err, http.StatusCreated)
	case len(parts) == 1 && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		target, err := s.reserved.Targets.Target(r.Context(), parts[0])
		answer(target, err, http.StatusOK)
	case len(parts) == 1 && r.Method == http.MethodDelete:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		if err := s.reserved.Targets.DeleteTarget(r.Context(), parts[0]); err != nil {
			s.writeTargetError(w, id, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 2 && parts[1] == "confirm" && r.Method == http.MethodPost:
		var confirm TargetConfirmV1
		if !body(&confirm) {
			bad()
			return
		}
		target, err := s.reserved.Targets.ConfirmTarget(r.Context(), parts[0], confirm)
		answer(target, err, http.StatusOK)
	case len(parts) == 2 && parts[1] == "preflight" && r.Method == http.MethodPost:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		target, err := s.reserved.Targets.PreflightTarget(r.Context(), parts[0])
		answer(target, err, http.StatusOK)
	default:
		bad()
	}
}

func (s *Server) writeTargetError(w http.ResponseWriter, id string, err error) {
	switch {
	case errors.Is(err, ErrTargetUnknown):
		s.writeError(w, http.StatusNotFound, ErrorV1{Code: "target_unknown", Message: "target is not registered", RequestID: id})
	case errors.Is(err, ErrTargetInvalid):
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid target request", RequestID: id})
	case errors.Is(err, ErrTargetAddressRefused):
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "target_address_refused", Message: "target address must be a public internet address", RequestID: id})
	case errors.Is(err, ErrTargetExists):
		s.writeError(w, http.StatusConflict, ErrorV1{Code: "target_exists", Message: "target is already registered", RequestID: id})
	case errors.Is(err, ErrTargetFingerprintMismatch):
		s.writeError(w, http.StatusConflict, ErrorV1{Code: "target_fingerprint_mismatch", Message: "fingerprint does not match the host key the server presented", RequestID: id})
	case errors.Is(err, ErrTargetLimit):
		s.writeError(w, http.StatusConflict, ErrorV1{Code: "target_limit", Message: "the target limit is reached", RequestID: id})
	default:
		s.writeError(w, http.StatusServiceUnavailable, ErrorV1{Code: "target_unavailable", Message: "targets are unavailable", RequestID: id, Retryable: true})
	}
}

package serviceapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

var (
	// ErrHostedConflict refuses a hosted command in the key's current state: a start while it runs, a backup or
	// restore while it is not ready, a purge while it is not stopped.
	ErrHostedConflict = errors.New("hosted state conflict")
	// ErrHostedCapacity refuses a start beyond the controller's limit of running hosted instances.
	ErrHostedCapacity = errors.New("hosted capacity reached")
)

// HostingController runs hosted instances (B9.2.1): one app, named by a hosting key the product chooses, running one
// version at a time on the governed preview runtime, with its own data volume, no time limit and controller restarts.
type HostingController interface {
	ReadHosted(ctx context.Context, principal Principal, key string) (HostedV1, error)
	StartHosted(ctx context.Context, principal Principal, authorityDigest, key string, request HostedStartRequestV1) (HostedV1, error)
	StopHosted(ctx context.Context, principal Principal, authorityDigest, key string, request HostedCommandRequestV1) (HostedV1, error)
	ResolveHostedRoute(ctx context.Context, principal Principal, key string) (HostedRouteV1, error)
	BackupHosted(ctx context.Context, principal Principal, authorityDigest, key string, request HostedBackupRequestV1) (HostedBackupV1, error)
	ListHostedBackups(ctx context.Context, principal Principal, key string) (HostedBackupListV1, error)
	RestoreHosted(ctx context.Context, principal Principal, authorityDigest, key string, request HostedRestoreRequestV1) (HostedV1, error)
	// OpenHostedBackup opens one listed backup's file for reading, with its size, for the product to hand to the app's
	// owner (Repo C BF-10c); the caller closes it.
	OpenHostedBackup(ctx context.Context, principal Principal, key, backupID string) (io.ReadCloser, int64, error)
	PurgeHosted(ctx context.Context, principal Principal, authorityDigest, key string, request HostedCommandRequestV1) (HostedV1, error)
	// ReadHostedSetup answers the app's setup token, made at its first start since the controller keeps them, or
	// ErrSetupPending before that start.
	ReadHostedSetup(ctx context.Context, principal Principal, key string) (HostedSetupV1, error)
	// HostedErrors answers the app's recent errors, kept from its own output as it ran (Repo C BF-07c2).
	HostedErrors(ctx context.Context, principal Principal, key string) (HostedErrorsV1, error)
}

// HostedErrorsV1 is a hosted app's recent errors (Repo C design note BF07_WHEN_A_RUNNING_APP_GOES_WRONG §13, 7c2): at
// most the last SiteErrorLines lines of its own server's output from the last day that read as errors, SiteErrorBytes
// in all, with what looks like a secret, a sign-in token, an email address or a phone number replaced by fixed words as
// the controller kept them; how many times the controller started it again in the last day, and the exit code its
// server last ended with on its own. The controller keeps them in memory from Since, the app's first start after the
// controller's own (absent when it has not run since). Running is true while the app is ready.
type HostedErrorsV1 struct {
	SchemaVersion string   `json:"schema_version"`
	HostingKey    string   `json:"hosting_key"`
	ReadAt        string   `json:"read_at"`
	Since         string   `json:"since,omitempty"`
	Running       bool     `json:"running"`
	Restarts      int      `json:"restarts"`
	ExitCode      *int     `json:"exit_code,omitempty"`
	Lines         []string `json:"lines"`
	Truncated     bool     `json:"truncated"`
}

// HostedErrorLineBytes bounds one kept line of a hosted app's output.
const HostedErrorLineBytes = 400

func ValidateHostedErrorsV1(v HostedErrorsV1, key string) error {
	if v.SchemaVersion != "HostedErrorsV1" || v.HostingKey != key || ValidateHostingKey(key) != nil || !validStamp(v.ReadAt) || (v.Since != "" && !validStamp(v.Since)) || v.Restarts < 0 || v.Lines == nil || len(v.Lines) > SiteErrorLines {
		return errors.New("invalid hosted errors")
	}
	total := 0
	for _, line := range v.Lines {
		total += len(line) + 1
		if len(line) > HostedErrorLineBytes || strings.ContainsAny(line, "\r\n") {
			return errors.New("invalid hosted error line")
		}
	}
	if total > SiteErrorBytes {
		return errors.New("invalid hosted errors")
	}
	return nil
}

type HostedStartRequestV1 struct {
	SchemaVersion        int              `json:"schema_version"`
	RequestID            string           `json:"request_id"`
	RunID                string           `json:"run_id"`
	CheckpointActivityID string           `json:"checkpoint_activity_id"`
	ProfileID            string           `json:"profile_id"`
	DelegatedActor       DelegatedActorV1 `json:"delegated_actor"`
	// Settings are the values the profile's settings service takes (Repo C design note A5 §8): exactly the names it
	// lists, no more. They are kept in a 0600 file for the key and never returned, logged or digested.
	Settings map[string]string `json:"settings,omitempty"`
}

// HostedCommandRequestV1 is the body of stop and purge.
type HostedCommandRequestV1 struct {
	SchemaVersion  int              `json:"schema_version"`
	RequestID      string           `json:"request_id"`
	DelegatedActor DelegatedActorV1 `json:"delegated_actor"`
}

type HostedBackupRequestV1 struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	// Reason is manual or pre-update. Nightly backups are the controller's own.
	Reason         string           `json:"reason"`
	DelegatedActor DelegatedActorV1 `json:"delegated_actor"`
}

type HostedRestoreRequestV1 struct {
	SchemaVersion  int              `json:"schema_version"`
	RequestID      string           `json:"request_id"`
	BackupID       string           `json:"backup_id"`
	DelegatedActor DelegatedActorV1 `json:"delegated_actor"`
}

type HostedV1 struct {
	SchemaVersion string `json:"schema_version"`
	HostingKey    string `json:"hosting_key"`
	// Desired is RUNNING or STOPPED; Status is STARTING, READY, FAILED or STOPPED.
	Desired    string `json:"desired"`
	Status     string `json:"status"`
	Health     string `json:"health"`
	Generation uint64 `json:"generation"`
	// The version: one run's checkpoint on one hosted profile.
	RunID                  string `json:"run_id"`
	CheckpointActivityID   string `json:"checkpoint_activity_id"`
	SourceSHA              string `json:"source_sha"`
	ProfileID              string `json:"profile_id"`
	ProfileDigest          string `json:"profile_digest"`
	ProductAuthorizationID string `json:"product_authorization_id"`
	ProductTaskID          string `json:"product_task_id"`
	ProductVersionID       string `json:"product_version_id"`
	RouteHandle            string `json:"route_handle,omitempty"`
	// Data is PRESENT or PURGED.
	Data       string          `json:"data"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
	LastBackup *HostedBackupV1 `json:"last_backup,omitempty"`
}

type HostedBackupV1 struct {
	SchemaVersion string `json:"schema_version"`
	HostingKey    string `json:"hosting_key"`
	BackupID      string `json:"backup_id"`
	// Reason is manual, pre-update or nightly.
	Reason     string `json:"reason"`
	TakenAt    string `json:"taken_at"`
	Bytes      int64  `json:"bytes"`
	Generation uint64 `json:"generation"`
	SourceSHA  string `json:"source_sha"`
	// Offsite is the backup's copy off the host, when the controller copies them (Repo C BF-10b): absent before a
	// copy was tried.
	Offsite *HostedBackupOffsiteV1 `json:"offsite,omitempty"`
}

// HostedBackupOffsiteV1 says whether a hosted backup was copied off the host: when, and where to (the s3:// address)
// or why not.
type HostedBackupOffsiteV1 struct {
	At     string `json:"at"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type HostedBackupListV1 struct {
	SchemaVersion string           `json:"schema_version"`
	HostingKey    string           `json:"hosting_key"`
	Backups       []HostedBackupV1 `json:"backups"`
}

// HostedRouteV1 is a server-only presentation target, like PreviewRouteV1.
type HostedRouteV1 struct {
	SchemaVersion string `json:"schema_version"`
	HostingKey    string `json:"hosting_key"`
	RouteHandle   string `json:"route_handle"`
	TargetURL     string `json:"target_url"`
}

// HostedSetupV1 is a hosted app's setup token (Repo C design note H6_FIRST_ADMIN_ON_HOSTING.md): the token with which
// the app's first administrator is made, which the controller keeps for the key and gives the app as BOOTSTRAP_TOKEN on
// every start. Only the service principal that controls hosting reads it; it is in no other answer, receipt or log.
type HostedSetupV1 struct {
	SchemaVersion string `json:"schema_version"`
	HostingKey    string `json:"hosting_key"`
	Token         string `json:"token"`
}

// hostedSetupToken is a setup token's shape: 32 lowercase hex characters, as on a server of its own (BF-14.2c).
var hostedSetupToken = regexp.MustCompile(`^[a-f0-9]{32}$`)

func ValidateHostedSetupV1(v HostedSetupV1, key string) error {
	if v.SchemaVersion != "HostedSetupV1" || v.HostingKey != key || ValidateHostingKey(key) != nil || !hostedSetupToken.MatchString(v.Token) {
		return errors.New("invalid hosted setup")
	}
	return nil
}

// ValidateHostingKey accepts the product's identifier for one hosted app.
func ValidateHostingKey(key string) error { return ValidatePrincipalID(key) }

func ValidateHostedStartRequestV1(c HostedStartRequestV1) error {
	if c.SchemaVersion != 1 || ValidatePrincipalID(c.RequestID) != nil || runtimecatalog.ValidateIdentifier(c.RunID) != nil || !isLowerSHA256(c.CheckpointActivityID) || ValidatePrincipalID(c.ProfileID) != nil || !validPreviewActor(c.DelegatedActor) {
		return errors.New("invalid hosted start command")
	}
	if len(c.Settings) > 8 {
		return errors.New("invalid hosted start command")
	}
	for name, value := range c.Settings {
		if !settingName.MatchString(name) || !validSettingValue(value) {
			return errors.New("invalid hosted start command")
		}
	}
	return nil
}

var settingName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// validSettingValue is one line of printable UTF-8, up to 1024 bytes, not starting with a space: what Docker's
// --env-file reads back unchanged.
func validSettingValue(value string) bool {
	if value == "" || len(value) > 1024 || !utf8.ValidString(value) || unicode.IsSpace(rune(value[0])) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func ValidateHostedCommandRequestV1(c HostedCommandRequestV1) error {
	if c.SchemaVersion != 1 || ValidatePrincipalID(c.RequestID) != nil || !validPreviewActor(c.DelegatedActor) {
		return errors.New("invalid hosted command")
	}
	return nil
}
func ValidateHostedBackupRequestV1(c HostedBackupRequestV1) error {
	if c.SchemaVersion != 1 || ValidatePrincipalID(c.RequestID) != nil || (c.Reason != "manual" && c.Reason != "pre-update") || !validPreviewActor(c.DelegatedActor) {
		return errors.New("invalid hosted backup command")
	}
	return nil
}
func ValidateHostedRestoreRequestV1(c HostedRestoreRequestV1) error {
	if c.SchemaVersion != 1 || ValidatePrincipalID(c.RequestID) != nil || !isLowerSHA256(c.BackupID) || !validPreviewActor(c.DelegatedActor) {
		return errors.New("invalid hosted restore command")
	}
	return nil
}

func validStamp(v string) bool {
	_, err := time.Parse(time.RFC3339Nano, v)
	return err == nil
}

func ValidateHostedBackupV1(v HostedBackupV1, key string) error {
	if v.SchemaVersion != "HostedBackupV1" || v.HostingKey != key || ValidateHostingKey(key) != nil || !isLowerSHA256(v.BackupID) || (v.Reason != "manual" && v.Reason != "pre-update" && v.Reason != "nightly" && v.Reason != "pre-restore") || !validStamp(v.TakenAt) || v.Bytes < 0 || v.Generation == 0 || !isLowerGitObjectID(v.SourceSHA) {
		return errors.New("invalid hosted backup")
	}
	if v.Offsite != nil && (!validStamp(v.Offsite.At) || v.Offsite.Detail == "" || len(v.Offsite.Detail) > 512) {
		return errors.New("invalid hosted backup copy")
	}
	return nil
}

func ValidateHostedV1(v HostedV1, key string) error {
	if v.SchemaVersion != "HostedV1" || v.HostingKey != key || ValidateHostingKey(key) != nil || (v.Desired != "RUNNING" && v.Desired != "STOPPED") || (v.Data != "PRESENT" && v.Data != "PURGED") || !validStamp(v.CreatedAt) || !validStamp(v.UpdatedAt) {
		return errors.New("invalid hosted instance")
	}
	if v.Generation == 0 || runtimecatalog.ValidateIdentifier(v.RunID) != nil || !isLowerSHA256(v.CheckpointActivityID) || !isLowerGitObjectID(v.SourceSHA) || ValidatePrincipalID(v.ProfileID) != nil || !isLowerSHA256(v.ProfileDigest) || ValidatePrincipalID(v.ProductAuthorizationID) != nil || ValidatePrincipalID(v.ProductTaskID) != nil || ValidatePrincipalID(v.ProductVersionID) != nil {
		return errors.New("invalid hosted version")
	}
	switch v.Status {
	case "READY":
		if v.Desired != "RUNNING" || (v.Health != "HEALTHY" && v.Health != "DEGRADED") || !isLowerSHA256(v.RouteHandle) {
			return errors.New("invalid ready hosted instance")
		}
	case "STARTING", "FAILED", "STOPPED":
		if v.Health != "UNKNOWN" || v.RouteHandle != "" || (v.Status == "STOPPED") != (v.Desired == "STOPPED") {
			return errors.New("invalid hosted instance state")
		}
	default:
		return errors.New("invalid hosted status")
	}
	if v.Data == "PURGED" && v.Status != "STOPPED" {
		return errors.New("invalid purged hosted instance")
	}
	if v.LastBackup != nil && ValidateHostedBackupV1(*v.LastBackup, key) != nil {
		return errors.New("invalid hosted last backup")
	}
	return nil
}

func ValidateHostedRouteV1(v HostedRouteV1, key string) error {
	if v.SchemaVersion != "HostedRouteV1" || v.HostingKey != key || ValidateHostingKey(key) != nil || !isLowerSHA256(v.RouteHandle) {
		return errors.New("invalid hosted route")
	}
	return ValidatePreviewTargetURL(v.TargetURL)
}

func (s *Server) mayControlHosting(principal Principal) bool {
	return principal.PrincipalType == PrincipalService && s.authority.Match(principal, "hosting.control") == nil
}

// /v1/hosted/{key} and /v1/hosted/{key}/{start|stop|route|setup|backups|restore|purge|errors}
func (s *Server) hostedRoute(w http.ResponseWriter, r *http.Request, principal Principal, id string) {
	bad := func() {
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid hosted request", RequestID: id})
	}
	if r.URL.RawPath != "" || r.URL.RawQuery != "" {
		bad()
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/hosted/"), "/")
	if len(parts) < 1 || len(parts) > 3 || ValidateHostingKey(parts[0]) != nil || (len(parts) == 3 && (parts[1] != "backups" || !isLowerSHA256(parts[2]))) {
		bad()
		return
	}
	key, op, backupID := parts[0], "", ""
	if len(parts) >= 2 {
		op = parts[1]
	}
	if len(parts) == 3 {
		op, backupID = "backup", parts[2]
	}
	if !s.mayControlHosting(principal) {
		s.writeDependencyError(w, id, ErrAuthorityDenied)
		return
	}
	if s.reserved.Hosting == nil {
		s.writeDependencyError(w, id, ErrUnsupportedCapability)
		return
	}
	h := s.reserved.Hosting
	read := r.Method == http.MethodGet && (op == "" || op == "route" || op == "setup" || op == "backups" || op == "backup" || op == "errors")
	write := r.Method == http.MethodPost && (op == "start" || op == "stop" || op == "backups" || op == "restore" || op == "purge")
	if !read && !write {
		if op != "" && op != "start" && op != "stop" && op != "route" && op != "setup" && op != "backups" && op != "backup" && op != "restore" && op != "purge" && op != "errors" {
			s.writeDependencyError(w, id, ErrDependencyNotFound)
			return
		}
		bad()
		return
	}
	if read {
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		switch op {
		case "":
			v, err := h.ReadHosted(r.Context(), principal, key)
			s.writeHosted(w, id, key, v, err, http.StatusOK)
		case "route":
			v, err := h.ResolveHostedRoute(r.Context(), principal, key)
			if err != nil {
				s.writeDependencyError(w, id, err)
				return
			}
			if ValidateHostedRouteV1(v, key) != nil {
				s.writeDependencyError(w, id, ErrInternalDurableSubstrate)
				return
			}
			s.writeJSON(w, http.StatusOK, v)
		case "setup":
			v, err := h.ReadHostedSetup(r.Context(), principal, key)
			if err != nil {
				s.writeDependencyError(w, id, err)
				return
			}
			if ValidateHostedSetupV1(v, key) != nil {
				s.writeDependencyError(w, id, ErrInternalDurableSubstrate)
				return
			}
			s.writeJSON(w, http.StatusOK, v)
		case "backup":
			s.writeHostedBackupFile(w, r, principal, id, key, backupID)
		case "errors":
			v, err := h.HostedErrors(r.Context(), principal, key)
			if err != nil {
				s.writeDependencyError(w, id, err)
				return
			}
			if ValidateHostedErrorsV1(v, key) != nil {
				s.writeDependencyError(w, id, ErrInternalDurableSubstrate)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			s.writeJSON(w, http.StatusOK, v)
		case "backups":
			v, err := h.ListHostedBackups(r.Context(), principal, key)
			if err != nil {
				s.writeDependencyError(w, id, err)
				return
			}
			if v.SchemaVersion != "HostedBackupListV1" || v.HostingKey != key || len(v.Backups) > 64 {
				s.writeDependencyError(w, id, ErrInternalDurableSubstrate)
				return
			}
			for _, b := range v.Backups {
				if ValidateHostedBackupV1(b, key) != nil {
					s.writeDependencyError(w, id, ErrInternalDurableSubstrate)
					return
				}
			}
			s.writeJSON(w, http.StatusOK, v)
		}
		return
	}
	if principal.PrincipalType != PrincipalService || !s.authority.MayAssertDelegatedActor(principal) {
		s.writeDependencyError(w, id, ErrAuthorityDenied)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(data) == 0 || len(data) > 4096 {
		bad()
		return
	}
	digest := s.authority.Digest()
	switch op {
	case "start":
		var c HostedStartRequestV1
		if decodeStrictJSON(data, &c) != nil || ValidateHostedStartRequestV1(c) != nil {
			bad()
			return
		}
		if !s.requireRegisteredRun(w, id, c.RunID) {
			return
		}
		v, err := h.StartHosted(r.Context(), principal, digest, key, c)
		s.writeHosted(w, id, key, v, err, http.StatusAccepted)
	case "stop", "purge":
		var c HostedCommandRequestV1
		if decodeStrictJSON(data, &c) != nil || ValidateHostedCommandRequestV1(c) != nil {
			bad()
			return
		}
		var v HostedV1
		if op == "stop" {
			v, err = h.StopHosted(r.Context(), principal, digest, key, c)
		} else {
			v, err = h.PurgeHosted(r.Context(), principal, digest, key, c)
		}
		s.writeHosted(w, id, key, v, err, http.StatusOK)
	case "backups":
		var c HostedBackupRequestV1
		if decodeStrictJSON(data, &c) != nil || ValidateHostedBackupRequestV1(c) != nil {
			bad()
			return
		}
		v, err := h.BackupHosted(r.Context(), principal, digest, key, c)
		if err != nil {
			s.writeDependencyError(w, id, err)
			return
		}
		if ValidateHostedBackupV1(v, key) != nil {
			s.writeDependencyError(w, id, ErrInternalDurableSubstrate)
			return
		}
		s.writeJSON(w, http.StatusOK, v)
	case "restore":
		var c HostedRestoreRequestV1
		if decodeStrictJSON(data, &c) != nil || ValidateHostedRestoreRequestV1(c) != nil {
			bad()
			return
		}
		v, err := h.RestoreHosted(r.Context(), principal, digest, key, c)
		s.writeHosted(w, id, key, v, err, http.StatusOK)
	}
}

// writeHostedBackupFile answers one backup's file as it is, never parsed or logged: the product hands it to the app's
// owner (Repo C BF-10c). A short copy after the headers ends the response early, so the receiver sees fewer bytes than
// its Content-Length.
func (s *Server) writeHostedBackupFile(w http.ResponseWriter, r *http.Request, principal Principal, id, key, backupID string) {
	f, size, err := s.reserved.Hosting.OpenHostedBackup(r.Context(), principal, key, backupID)
	if err != nil {
		s.writeDependencyError(w, id, err)
		return
	}
	defer f.Close()
	if size <= 0 {
		s.writeDependencyError(w, id, ErrInternalDurableSubstrate)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.CopyN(w, f, size)
}

func (s *Server) writeHosted(w http.ResponseWriter, id, key string, v HostedV1, err error, status int) {
	if err != nil {
		s.writeDependencyError(w, id, err)
		return
	}
	if ValidateHostedV1(v, key) != nil {
		s.writeDependencyError(w, id, ErrInternalDurableSubstrate)
		return
	}
	s.writeJSON(w, status, v)
}

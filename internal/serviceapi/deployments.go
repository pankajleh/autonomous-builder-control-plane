package serviceapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Deployments to remote targets (Repo C design note
// BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md §11, BF-14.2a): Repo C sends a release
// (an exact app version's files as a tar named by its digest), ABCP makes a
// plan sealed by a hash, Repo C applies the plan an administrator approved,
// and ABCP carries it out on the target over its pinned SSH key as an
// operation. On the server ABCP keeps releases, a link per site and one
// rootless front door; nothing outside the deploy login's ~/autobuild, and the
// autobuild-* containers, networks and volumes, is touched. A Node release
// (BF-14.2b) also gets the shared Postgres, a database and login of its own,
// and an app container on a network of its own, with a backup before every
// update. A Node app's backup can be restored (an approved plan) and checked
// by restoring it into a separate database; every night ABCP backs up each
// Node app and checks the backup (BF-14.2b-1, second part).

// ReleaseV1 describes a release ABCP keeps.
type ReleaseV1 struct {
	SchemaVersion string `json:"schema_version"`
	Digest        string `json:"digest"`
	App           string `json:"app"`
	Version       string `json:"version"`
	Kind          string `json:"kind"`
	Files         int    `json:"files"`
	Bytes         int64  `json:"bytes"`
	ReceivedAt    string `json:"received_at"`
}

// PlanSiteV1 names a site and the host name it answers on.
type PlanSiteV1 struct {
	Name string `json:"name"`
	Host string `json:"host"`
}

// PlanCreateV1 asks for a plan to put a release on a target as a site, or,
// with Restore and nothing else, to restore one of a Node app's backups.
type PlanCreateV1 struct {
	Release string         `json:"release,omitempty"`
	Site    PlanSiteV1     `json:"site"`
	Restore *PlanRestoreV1 `json:"restore,omitempty"`
}

// PlanRestoreV1 names the Node app and the backup a restore plan puts back.
type PlanRestoreV1 struct {
	Site   string `json:"site"`
	Backup string `json:"backup"`
}

// PlanV1 is what applying would do, sealed by Hash: the SHA-256 of the plan's
// canonical JSON without the hash. Nothing on the target changes until a plan
// is applied, and a plan is applied at most once, before ExpiresAt.
type PlanV1 struct {
	SchemaVersion string `json:"schema_version"`
	PlanID        string `json:"plan_id"`
	Target        string `json:"target"`
	// Kind is "restore" for a restore plan and empty for putting a release up.
	Kind    string     `json:"kind,omitempty"`
	Release ReleaseV1  `json:"release"`
	Site    PlanSiteV1 `json:"site"`
	// Replaces is the release the site serves now, if any.
	Replaces string `json:"replaces,omitempty"`
	// Steps, in order. A static site: send_files (or reuse_files),
	// switch_site, write_site, start_frontdoor (or reload_frontdoor), check.
	// A Node app (BF-14.2b): send_files (or reuse_files), get_images,
	// start_database, make_database, back_up (when it replaces a release),
	// switch_site, start_app, wait_health, write_site, start_frontdoor (or
	// reload_frontdoor), check. A restore: back_up, restore_copy, stop_app,
	// switch_database, start_app, wait_health, check, clean_up.
	Steps []string `json:"steps"`
	// Images are what a Node app runs, by digest; Database is its database and
	// login, made on the server the first time; Sandbox is "gvisor" or
	// "standard". All three are empty for a static site.
	Images   []string `json:"images,omitempty"`
	Database string   `json:"database,omitempty"`
	Sandbox  string   `json:"sandbox,omitempty"`
	// Backup is the backup a restore plan puts back; Release is then the
	// release the app keeps running.
	Backup    *BackupV1 `json:"backup,omitempty"`
	CreatedAt string    `json:"created_at"`
	ExpiresAt string    `json:"expires_at"`
	Hash      string    `json:"hash"`
}

// ApplyV1 applies a plan an administrator approved. RequestID makes a
// repeated request answer the same operation.
type ApplyV1 struct {
	PlanID    string `json:"plan_id"`
	PlanHash  string `json:"plan_hash"`
	RequestID string `json:"request_id"`
}

// SiteRemoveV1 removes a site. RequestID makes a repeated request answer the
// same operation.
type SiteRemoveV1 struct {
	RequestID string `json:"request_id"`
}

// OperationStepV1 is one step of an operation. Site names the app a step of
// the nightly backups is for; other steps are for the operation's site.
type OperationStepV1 struct {
	Name   string `json:"name"`
	Site   string `json:"site,omitempty"`
	State  string `json:"state"`
	At     string `json:"at,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// OperationV1 is a change ABCP carries out on a target. Kind is "apply",
// "remove", "backup", "restore", "check_backup" or "nightly" (every Node app
// on the target, with no site of its own). State is "queued", "running",
// "succeeded", "failed" or "unknown" (interrupted: unknown until reconciled,
// never safe to repeat blindly).
type OperationV1 struct {
	SchemaVersion string            `json:"schema_version"`
	OperationID   string            `json:"operation_id"`
	Target        string            `json:"target"`
	Kind          string            `json:"kind"`
	Site          string            `json:"site"`
	PlanID        string            `json:"plan_id,omitempty"`
	RequestID     string            `json:"request_id"`
	State         string            `json:"state"`
	Steps         []OperationStepV1 `json:"steps"`
	StartedAt     string            `json:"started_at"`
	EndedAt       string            `json:"ended_at,omitempty"`
}

// SiteCheckV1 is the last check that a site answers with its release.
type SiteCheckV1 struct {
	At     string `json:"at"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// SiteV1 is a site ABCP put on a target. State is "deploying", "live",
// "failed", "removing" or "unknown".
type SiteV1 struct {
	Name string `json:"name"`
	Host string `json:"host"`
	// Kind is "node" for a Node app and empty for a static site; a Node app
	// also names its database and sandbox.
	Kind      string       `json:"kind,omitempty"`
	Database  string       `json:"database,omitempty"`
	Sandbox   string       `json:"sandbox,omitempty"`
	Release   string       `json:"release"`
	State     string       `json:"state"`
	UpdatedAt string       `json:"updated_at"`
	LastCheck *SiteCheckV1 `json:"last_check,omitempty"`
	Operation string       `json:"operation,omitempty"`
}

// SitesV1 lists a target's sites.
type SitesV1 struct {
	SchemaVersion string   `json:"schema_version"`
	Target        string   `json:"target"`
	Sites         []SiteV1 `json:"sites"`
}

// BackupV1 is one backup of a Node app's database, kept on its server.
// Reason is "before_update", "before_restore", "nightly" or "manual".
type BackupV1 struct {
	BackupID string `json:"backup_id"`
	Reason   string `json:"reason"`
	TakenAt  string `json:"taken_at"`
	Bytes    int64  `json:"bytes"`
	Release  string `json:"release,omitempty"`
	// Tables and Rows are what the database held when the backup was taken,
	// counted in the same snapshot as the dump.
	Tables *int   `json:"tables,omitempty"`
	Rows   *int64 `json:"rows,omitempty"`
	// Check is the last time the backup was restored into a separate database
	// and every table's rows compared with what it held when taken.
	Check *BackupCheckV1 `json:"check,omitempty"`
}

// BackupCheckV1 is whether a backup restored with the rows it was taken with.
type BackupCheckV1 struct {
	At     string `json:"at"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// BackupsV1 lists a Node app's backups, newest last. They are kept after the
// app is removed, with its database.
type BackupsV1 struct {
	SchemaVersion string     `json:"schema_version"`
	Target        string     `json:"target"`
	Site          string     `json:"site"`
	Database      string     `json:"database"`
	Backups       []BackupV1 `json:"backups"`
}

// BackupCreateV1 takes a backup now, or checks one. RequestID makes a
// repeated request answer the same operation.
type BackupCreateV1 struct {
	RequestID string `json:"request_id"`
}

const (
	BackupsSchemaV1   = "BackupsV1"
	ReleaseSchemaV1   = "ReleaseV1"
	PlanSchemaV1      = "PlanV1"
	OperationSchemaV1 = "OperationV1"
	SitesSchemaV1     = "SitesV1"
	// MaxReleaseBytes bounds a release's tar: 128 MB of files and the tar's own headers.
	MaxReleaseBytes = 160 << 20
	// releaseReadTimeout bounds receiving one release.
	releaseReadTimeout = 5 * time.Minute
)

// DeploymentController keeps releases and carries plans out on targets.
type DeploymentController interface {
	PutRelease(ctx context.Context, digest string, body io.Reader) (ReleaseV1, bool, error)
	Release(ctx context.Context, digest string) (ReleaseV1, error)
	CreatePlan(ctx context.Context, target string, create PlanCreateV1) (PlanV1, error)
	Apply(ctx context.Context, target string, apply ApplyV1) (OperationV1, error)
	Operation(ctx context.Context, target, operationID string) (OperationV1, error)
	Sites(ctx context.Context, target string) (SitesV1, error)
	RemoveSite(ctx context.Context, target, site string, remove SiteRemoveV1) (OperationV1, error)
	Backups(ctx context.Context, target, site string) (BackupsV1, error)
	BackUp(ctx context.Context, target, site string, create BackupCreateV1) (OperationV1, error)
	CheckBackup(ctx context.Context, target, site, backup string, create BackupCreateV1) (OperationV1, error)
}

var (
	ErrReleaseInvalid   = errors.New("release invalid")
	ErrReleaseUnknown   = errors.New("release unknown")
	ErrReleaseLimit     = errors.New("release limit reached")
	ErrPlanStale        = errors.New("plan expired, changed or applied")
	ErrTargetBusy       = errors.New("target busy")
	ErrTargetNotReady   = errors.New("target not ready")
	ErrSiteUnknown      = errors.New("site unknown")
	ErrOperationUnknown = errors.New("operation unknown")
	ErrBackupUnknown    = errors.New("backup unknown")
)

var (
	releaseDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	operationIDPattern   = regexp.MustCompile(`^op-[0-9a-f]{16}$`)
	siteNamePattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	backupIDPattern      = regexp.MustCompile(`^bk-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4}$`)
)

// isReleaseUpload is a PUT of one release, the one request allowed a body
// larger than MaxRequestBodyBytes and a longer read.
func isReleaseUpload(r *http.Request) bool {
	return r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/releases/")
}

// /v1/releases/{digest}
func (s *Server) releases(w http.ResponseWriter, r *http.Request, principal Principal, id string) {
	bad := func() {
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid release request", RequestID: id})
	}
	if s.reserved.Deployments == nil {
		s.writeError(w, http.StatusNotImplemented, ErrorV1{Code: "unsupported_capability", Message: "deployments are not available", RequestID: id})
		return
	}
	if !s.mayControlTargets(principal) {
		s.writeDependencyError(w, id, ErrAuthorityDenied)
		return
	}
	digest := strings.TrimPrefix(r.URL.Path, "/v1/releases/")
	if r.URL.RawPath != "" || r.URL.RawQuery != "" || !releaseDigestPattern.MatchString(digest) {
		bad()
		return
	}
	switch r.Method {
	case http.MethodPut:
		if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/x-tar" {
			bad()
			return
		}
		release, created, err := s.reserved.Deployments.PutRelease(r.Context(), digest, r.Body)
		if err != nil {
			s.writeDeploymentError(w, id, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		s.writeJSON(w, status, release)
	case http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		release, err := s.reserved.Deployments.Release(r.Context(), digest)
		if err != nil {
			s.writeDeploymentError(w, id, err)
			return
		}
		s.writeJSON(w, http.StatusOK, release)
	default:
		bad()
	}
}

// /v1/targets/{key}/plans, /apply, /operations/{id}, /sites, /sites/{name}/remove, /sites/{name}/backups and
// /sites/{name}/backups/{backup}/check
func (s *Server) targetDeployments(w http.ResponseWriter, r *http.Request, id string, parts []string, body func(any) bool) {
	bad := func() {
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid deployment request", RequestID: id})
	}
	if s.reserved.Deployments == nil {
		s.writeError(w, http.StatusNotImplemented, ErrorV1{Code: "unsupported_capability", Message: "deployments are not available", RequestID: id})
		return
	}
	operation := func(op OperationV1, err error, status int) {
		if err != nil {
			s.writeDeploymentError(w, id, err)
			return
		}
		s.writeJSON(w, status, op)
	}
	key := parts[0]
	switch {
	case len(parts) == 2 && parts[1] == "plans" && r.Method == http.MethodPost:
		var create PlanCreateV1
		if !body(&create) {
			bad()
			return
		}
		plan, err := s.reserved.Deployments.CreatePlan(r.Context(), key, create)
		if err != nil {
			s.writeDeploymentError(w, id, err)
			return
		}
		s.writeJSON(w, http.StatusCreated, plan)
	case len(parts) == 2 && parts[1] == "apply" && r.Method == http.MethodPost:
		var apply ApplyV1
		if !body(&apply) {
			bad()
			return
		}
		op, err := s.reserved.Deployments.Apply(r.Context(), key, apply)
		operation(op, err, http.StatusAccepted)
	case len(parts) == 3 && parts[1] == "operations" && operationIDPattern.MatchString(parts[2]) && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		op, err := s.reserved.Deployments.Operation(r.Context(), key, parts[2])
		operation(op, err, http.StatusOK)
	case len(parts) == 2 && parts[1] == "sites" && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		sites, err := s.reserved.Deployments.Sites(r.Context(), key)
		if err != nil {
			s.writeDeploymentError(w, id, err)
			return
		}
		s.writeJSON(w, http.StatusOK, sites)
	case len(parts) == 4 && parts[1] == "sites" && siteNamePattern.MatchString(parts[2]) && parts[3] == "remove" && r.Method == http.MethodPost:
		var remove SiteRemoveV1
		if !body(&remove) {
			bad()
			return
		}
		op, err := s.reserved.Deployments.RemoveSite(r.Context(), key, parts[2], remove)
		operation(op, err, http.StatusAccepted)
	case len(parts) == 4 && parts[1] == "sites" && siteNamePattern.MatchString(parts[2]) && parts[3] == "backups" && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		backups, err := s.reserved.Deployments.Backups(r.Context(), key, parts[2])
		if err != nil {
			s.writeDeploymentError(w, id, err)
			return
		}
		s.writeJSON(w, http.StatusOK, backups)
	case len(parts) == 4 && parts[1] == "sites" && siteNamePattern.MatchString(parts[2]) && parts[3] == "backups" && r.Method == http.MethodPost:
		var create BackupCreateV1
		if !body(&create) {
			bad()
			return
		}
		op, err := s.reserved.Deployments.BackUp(r.Context(), key, parts[2], create)
		operation(op, err, http.StatusAccepted)
	case len(parts) == 6 && parts[1] == "sites" && siteNamePattern.MatchString(parts[2]) && parts[3] == "backups" &&
		backupIDPattern.MatchString(parts[4]) && parts[5] == "check" && r.Method == http.MethodPost:
		var create BackupCreateV1
		if !body(&create) {
			bad()
			return
		}
		op, err := s.reserved.Deployments.CheckBackup(r.Context(), key, parts[2], parts[4], create)
		operation(op, err, http.StatusAccepted)
	default:
		bad()
	}
}

func (s *Server) writeDeploymentError(w http.ResponseWriter, id string, err error) {
	switch {
	case errors.Is(err, ErrReleaseInvalid):
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "release_invalid", Message: "release is not a valid release", RequestID: id})
	case errors.Is(err, ErrReleaseUnknown):
		s.writeError(w, http.StatusNotFound, ErrorV1{Code: "release_unknown", Message: "release is not kept", RequestID: id})
	case errors.Is(err, ErrReleaseLimit):
		s.writeError(w, http.StatusConflict, ErrorV1{Code: "release_limit", Message: "the release limit is reached", RequestID: id})
	case errors.Is(err, ErrPlanStale):
		s.writeError(w, http.StatusConflict, ErrorV1{Code: "plan_stale", Message: "plan expired, changed or was already applied", RequestID: id})
	case errors.Is(err, ErrTargetBusy):
		s.writeError(w, http.StatusConflict, ErrorV1{Code: "target_busy", Message: "another operation is running on this target", RequestID: id, Retryable: true})
	case errors.Is(err, ErrTargetNotReady):
		s.writeError(w, http.StatusConflict, ErrorV1{Code: "target_not_ready", Message: "target's host key is not confirmed", RequestID: id})
	case errors.Is(err, ErrSiteUnknown):
		s.writeError(w, http.StatusNotFound, ErrorV1{Code: "site_unknown", Message: "site is not on this target", RequestID: id})
	case errors.Is(err, ErrBackupUnknown):
		s.writeError(w, http.StatusNotFound, ErrorV1{Code: "backup_unknown", Message: "backup is not kept or cannot be restored", RequestID: id})
	case errors.Is(err, ErrOperationUnknown):
		s.writeError(w, http.StatusNotFound, ErrorV1{Code: "operation_unknown", Message: "operation is not known", RequestID: id})
	default:
		s.writeTargetError(w, id, err)
	}
}

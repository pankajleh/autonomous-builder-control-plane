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
// Node app and checks the backup (BF-14.2b-1, second part). A Node app can be
// stopped and started, an app's data removed once its site is off, a target
// cleaned up once nothing is left, and a target checked against ABCP's record
// (BF-14.5a).

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
	// Conversion is SQL that converts a Node app's data for the release that
	// replaces it, when that release is another app's (Repo C design note
	// BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md, an app replaced by another): run
	// once, as the app's own login, in one transaction, after the app is
	// stopped and backed up. At most MaxConversionBytes of UTF-8 text with no
	// backslash, so psql runs no command of its own.
	Conversion string `json:"conversion,omitempty"`
	// StartFrom is one of the app's backups, by id, for the release that
	// replaces the one it runs to start on, instead of today's data (Repo C
	// design note BF10_GOING_BACK_SAFELY.md §10.2, BF-10d2b: going back to an
	// earlier version with its data as it was). The backup's rows were counted
	// and it was taken while that same release ran; today's data is backed up
	// first. Not with a conversion.
	StartFrom string `json:"start_from,omitempty"`
}

// MaxConversionBytes bounds a plan's conversion.
const MaxConversionBytes = 64 << 10

// maxPlanBody bounds a plan request: the other target requests' bound plus a
// conversion, each of whose bytes JSON writes as at most six (\u00XX).
const maxPlanBody = maxTargetBody + 6*MaxConversionBytes

// PlanConversionV1 is the conversion a plan runs, by its SHA-256 and size; the
// plan's hash covers it, so an approval covers the exact SQL.
type PlanConversionV1 struct {
	Digest string `json:"digest"`
	Bytes  int    `json:"bytes"`
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
	// reload_frontdoor), check. A Node app replaced with a conversion: the same
	// with stop_app, back_up, convert before switch_site. A Node app updated
	// onto a backup: the same with stop_app, back_up, restore_data before
	// switch_site, and clean_up after check. A restore: back_up, restore_copy,
	// stop_app, switch_database, start_app, wait_health, check, clean_up.
	Steps []string `json:"steps"`
	// Conversion is the SQL a plan runs on the app's data before the release
	// starts, when it has one.
	Conversion *PlanConversionV1 `json:"conversion,omitempty"`
	// StartFrom is the backup the release starts on, when it starts on one.
	StartFrom *BackupV1 `json:"start_from,omitempty"`
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
// "remove", "backup", "restore", "check_backup", "nightly" (every Node app on
// the target, with no site of its own), "stop", "start", "remove_data",
// "clean" or "drift" (the last two with no site), or "isolation" (P1b: Site
// and another app kept apart). State is "queued", "running",
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
// "failed", "removing", "stopped" (a Node app paused, BF-14.5a) or "unknown".
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
	// Offsite is the backup's copy off the server (BF-14.2b-2), when the target
	// has a bucket: where it went, or why it did not.
	Offsite *BackupOffsiteV1 `json:"offsite,omitempty"`
	// Counts names the tables with the most rows when it was taken, at most
	// BackupCountLimit, in name order (Repo C BF-06a); a listing gives them.
	// Tables and Rows count every table.
	Counts []TableRowsV1 `json:"counts,omitempty"`
}

// TableRowsV1 is one table's rows when a backup was taken (Repo C design note
// BF06_YOUR_APPS_DATA §4.1): the table by schema and name as the database
// quotes them, such as public.orders.
type TableRowsV1 struct {
	Table string `json:"table"`
	Rows  int64  `json:"rows"`
}

// BackupCountLimit bounds the tables a backup's answer names: the ones with
// the most rows, among those whose name is at most BackupCountNameBytes long,
// so a listing of every backup stays a few tens of kilobytes.
const (
	BackupCountLimit     = 20
	BackupCountNameBytes = 64
)

// ValidateTableRows checks a backup's counts: at most BackupCountLimit tables,
// each named once in order, none empty, longer than BackupCountNameBytes or
// negative.
func ValidateTableRows(counts []TableRowsV1) error {
	if len(counts) > BackupCountLimit {
		return errors.New("too many table counts")
	}
	for i, c := range counts {
		if c.Table == "" || len(c.Table) > BackupCountNameBytes || c.Rows < 0 || (i > 0 && counts[i-1].Table >= c.Table) {
			return errors.New("invalid table count")
		}
	}
	return nil
}

// BackupOffsiteV1 is whether a backup was copied off its server: Detail is the
// copy's s3:// address, or why it was not copied.
type BackupOffsiteV1 struct {
	At     string `json:"at"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
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

// SiteErrorsV1 is a Node app's recent errors on its target, read when asked
// (Repo C design note BF07_WHEN_A_RUNNING_APP_GOES_WRONG §4.3, 7c): at most the
// last SiteErrorLines error lines of the app's own output from the last day,
// SiteErrorBytes in all, with what looks like a secret, a sign-in token, an
// email address or a phone number replaced by fixed words before it leaves
// ABCP; how many times the app restarted, and how it last exited. Running is
// false when the app's container is not running (stopped or gone).
type SiteErrorsV1 struct {
	SchemaVersion string   `json:"schema_version"`
	Target        string   `json:"target"`
	Site          string   `json:"site"`
	ReadAt        string   `json:"read_at"`
	Running       bool     `json:"running"`
	Restarts      int      `json:"restarts"`
	ExitCode      *int     `json:"exit_code,omitempty"`
	OutOfMemory   bool     `json:"out_of_memory"`
	Lines         []string `json:"lines"`
	Truncated     bool     `json:"truncated"`
}

// BackupCreateV1 takes a backup now, or checks one. RequestID makes a
// repeated request answer the same operation.
type BackupCreateV1 struct {
	RequestID string `json:"request_id"`
}

// OperationRequestV1 starts an operation with nothing more than its request
// id: a Node app's stop or start, or a check of the target (BF-14.5a).
type OperationRequestV1 struct {
	RequestID string `json:"request_id"`
}

// IsolationRequestV1 asks whether two Node apps on a target are kept apart
// (BF-14 proof P1b): each app's database login is tried on the other's
// database and must be refused, and neither app's container may reach the
// other's. With Crash, Site's container is killed outright, Other must still
// answer, and Site is then started again.
type IsolationRequestV1 struct {
	RequestID string `json:"request_id"`
	Site      string `json:"site"`
	Other     string `json:"other"`
	Crash     bool   `json:"crash,omitempty"`
}

// ConfirmedRequestV1 starts an operation that removes something for good;
// Confirm repeats its name: an app's database, or the target's key.
type ConfirmedRequestV1 struct {
	RequestID string `json:"request_id"`
	Confirm   string `json:"confirm"`
}

// DatabaseV1 is a Node app's database on a target; OnServer is whether its
// site is still on the target.
type DatabaseV1 struct {
	Site      string `json:"site"`
	Database  string `json:"database"`
	CreatedAt string `json:"created_at"`
	Backups   int    `json:"backups"`
	OnServer  bool   `json:"on_server"`
}

// DatabasesV1 lists a target's app databases.
type DatabasesV1 struct {
	SchemaVersion string       `json:"schema_version"`
	Target        string       `json:"target"`
	Databases     []DatabaseV1 `json:"databases"`
}

// DriftFindingV1 is one way a target differs from ABCP's record. Kind is one
// of container_missing, container_stopped, container_unknown,
// network_missing, network_unknown, volume_missing, volume_unknown,
// link_missing, link_wrong, link_unknown, release_missing, release_unknown,
// database_missing, database_unknown, copy_left, backup_missing,
// backup_unknown, frontdoor_config or site_unknown.
type DriftFindingV1 struct {
	Kind   string `json:"kind"`
	Site   string `json:"site,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// DriftV1 is the last check of a target against ABCP's record. Reconciled
// names the sites an interrupted operation had left unknown that the check
// found unambiguous and made live again.
type DriftV1 struct {
	SchemaVersion string           `json:"schema_version"`
	Target        string           `json:"target"`
	Checked       bool             `json:"checked"`
	CheckedAt     string           `json:"checked_at,omitempty"`
	OK            bool             `json:"ok"`
	Findings      []DriftFindingV1 `json:"findings"`
	Reconciled    []string         `json:"reconciled"`
}

// NameV1 answers whether a name points at a target (BF-14.3a): the target's
// address, the addresses the name has now (A and AAAA, at most eight, IPv4
// first) and whether one of them is the target's.
type NameV1 struct {
	SchemaVersion string   `json:"schema_version"`
	Target        string   `json:"target"`
	Host          string   `json:"host"`
	Address       string   `json:"address"`
	Addresses     []string `json:"addresses"`
	Points        bool     `json:"points"`
	CheckedAt     string   `json:"checked_at"`
}

// SiteRelayV1 gives a site the relay credential its front-door block sends
// with Autobuild's paths (BF-08a): 32 random bytes, base64url without padding.
// ABCP keeps it with the site and never answers it.
type SiteRelayV1 struct {
	Credential string `json:"credential"`
}

// SiteSetupV1 is a Node app's setup token and the host its first-admin form
// is on (BF-14.2c). Only the service principal reads it.
type SiteSetupV1 struct {
	SchemaVersion string `json:"schema_version"`
	Target        string `json:"target"`
	Site          string `json:"site"`
	Host          string `json:"host"`
	Token         string `json:"token"`
}

const (
	SiteSetupSchemaV1  = "SiteSetupV1"
	NameSchemaV1       = "NameV1"
	DatabasesSchemaV1  = "DatabasesV1"
	DriftSchemaV1      = "DriftV1"
	BackupsSchemaV1    = "BackupsV1"
	SiteErrorsSchemaV1 = "SiteErrorsV1"
	// SiteErrorLines and SiteErrorBytes bound a site's recent errors.
	SiteErrorLines    = 50
	SiteErrorBytes    = 16 * 1024
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
	// OpenBackup opens one listed backup's file as it is on the target, with
	// its size, for the app's owner to download (BF-10c2).
	OpenBackup(ctx context.Context, target, site, backup string) (io.ReadCloser, int64, error)
	// OpenSpreadsheets opens one listed backup as a zip of one CSV file per
	// table, read as it is made (BF-06b); an error after it opens leaves the
	// zip unfinished.
	OpenSpreadsheets(ctx context.Context, target, site, backup string) (io.ReadCloser, error)
	// SiteErrors reads a Node app's recent errors on its target, when asked
	// (BF-07c); it changes nothing.
	SiteErrors(ctx context.Context, target, site string) (SiteErrorsV1, error)
	StopSite(ctx context.Context, target, site string, request OperationRequestV1) (OperationV1, error)
	StartSite(ctx context.Context, target, site string, request OperationRequestV1) (OperationV1, error)
	Databases(ctx context.Context, target string) (DatabasesV1, error)
	RemoveData(ctx context.Context, target, site string, request ConfirmedRequestV1) (OperationV1, error)
	CleanTarget(ctx context.Context, target string, request ConfirmedRequestV1) (OperationV1, error)
	CheckDrift(ctx context.Context, target string, request OperationRequestV1) (OperationV1, error)
	// CheckIsolation proves two Node apps on a target are kept apart (P1b).
	CheckIsolation(ctx context.Context, target string, request IsolationRequestV1) (OperationV1, error)
	Drift(ctx context.Context, target string) (DriftV1, error)
	// Name looks a host up once and says whether it points at the target;
	// CheckSite runs a site's check once more (BF-14.3a).
	Name(ctx context.Context, target, host string) (NameV1, error)
	CheckSite(ctx context.Context, target, site string, request OperationRequestV1) (OperationV1, error)
	// SiteSetup answers a Node app's setup token, with which its first
	// administrator is made (BF-14.2c).
	SiteSetup(ctx context.Context, target, site string) (SiteSetupV1, error)
	// SetSiteRelay keeps a site's relay credential and rewrites its target's
	// front door as an operation, so the site's block sends Autobuild's paths
	// on to the relay (BF-08a).
	SetSiteRelay(ctx context.Context, target, site string, relay SiteRelayV1) (OperationV1, error)
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
	ErrSiteStopped      = errors.New("site stopped")
	ErrDataInUse        = errors.New("data in use")
	ErrTargetInUse      = errors.New("target in use")
	ErrSetupPending     = errors.New("setup pending")
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

// /v1/targets/{key}/plans, /apply, /operations/{id}, /sites, /sites/{name}/remove, /sites/{name}/backups,
// /sites/{name}/backups/{backup} (its file), /sites/{name}/backups/{backup}/spreadsheets, /sites/{name}/backups/{backup}/check, /sites/{name}/errors, /sites/{name}/stop and /start,
// /databases, /databases/{name}/remove, /clean, /drift and /isolation
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
		// A plan may carry a conversion, so it has a bound of its own.
		var create PlanCreateV1
		data, err := io.ReadAll(io.LimitReader(r.Body, maxPlanBody+1))
		if err != nil || len(data) == 0 || len(data) > maxPlanBody || decodeStrictJSON(data, &create) != nil {
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
	case len(parts) == 4 && parts[1] == "sites" && siteNamePattern.MatchString(parts[2]) && parts[3] == "errors" && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		errs, err := s.reserved.Deployments.SiteErrors(r.Context(), key, parts[2])
		if err != nil {
			s.writeDeploymentError(w, id, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		s.writeJSON(w, http.StatusOK, errs)
	case len(parts) == 4 && parts[1] == "sites" && siteNamePattern.MatchString(parts[2]) && parts[3] == "backups" && r.Method == http.MethodPost:
		var create BackupCreateV1
		if !body(&create) {
			bad()
			return
		}
		op, err := s.reserved.Deployments.BackUp(r.Context(), key, parts[2], create)
		operation(op, err, http.StatusAccepted)
	case len(parts) == 5 && parts[1] == "sites" && siteNamePattern.MatchString(parts[2]) && parts[3] == "backups" &&
		backupIDPattern.MatchString(parts[4]) && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		s.writeSiteBackupFile(w, r, id, key, parts[2], parts[4])
	case len(parts) == 6 && parts[1] == "sites" && siteNamePattern.MatchString(parts[2]) && parts[3] == "backups" &&
		backupIDPattern.MatchString(parts[4]) && parts[5] == "spreadsheets" && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		f, err := s.reserved.Deployments.OpenSpreadsheets(r.Context(), key, parts[2], parts[4])
		if err != nil {
			s.writeDeploymentError(w, id, err)
			return
		}
		s.writeSpreadsheets(w, f, func() {
			s.writeError(w, http.StatusServiceUnavailable, ErrorV1{Code: "target_unavailable", Message: "targets are unavailable", RequestID: id, Retryable: true})
		})
	case len(parts) == 6 && parts[1] == "sites" && siteNamePattern.MatchString(parts[2]) && parts[3] == "backups" &&
		backupIDPattern.MatchString(parts[4]) && parts[5] == "check" && r.Method == http.MethodPost:
		var create BackupCreateV1
		if !body(&create) {
			bad()
			return
		}
		op, err := s.reserved.Deployments.CheckBackup(r.Context(), key, parts[2], parts[4], create)
		operation(op, err, http.StatusAccepted)
	case len(parts) == 4 && parts[1] == "sites" && siteNamePattern.MatchString(parts[2]) && parts[3] == "check" && r.Method == http.MethodPost:
		var request OperationRequestV1
		if !body(&request) {
			bad()
			return
		}
		op, err := s.reserved.Deployments.CheckSite(r.Context(), key, parts[2], request)
		operation(op, err, http.StatusAccepted)
	case len(parts) == 4 && parts[1] == "sites" && siteNamePattern.MatchString(parts[2]) && parts[3] == "setup" && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		setup, err := s.reserved.Deployments.SiteSetup(r.Context(), key, parts[2])
		if err != nil {
			s.writeDeploymentError(w, id, err)
			return
		}
		s.writeJSON(w, http.StatusOK, setup)
	case len(parts) == 4 && parts[1] == "sites" && siteNamePattern.MatchString(parts[2]) && parts[3] == "relay" && r.Method == http.MethodPut:
		var relay SiteRelayV1
		if !body(&relay) {
			bad()
			return
		}
		op, err := s.reserved.Deployments.SetSiteRelay(r.Context(), key, parts[2], relay)
		operation(op, err, http.StatusAccepted)
	case len(parts) == 3 && parts[1] == "names" && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		name, err := s.reserved.Deployments.Name(r.Context(), key, parts[2])
		if err != nil {
			s.writeDeploymentError(w, id, err)
			return
		}
		s.writeJSON(w, http.StatusOK, name)
	case len(parts) == 4 && parts[1] == "sites" && siteNamePattern.MatchString(parts[2]) && (parts[3] == "stop" || parts[3] == "start") &&
		r.Method == http.MethodPost:
		var request OperationRequestV1
		if !body(&request) {
			bad()
			return
		}
		var op OperationV1
		var err error
		if parts[3] == "stop" {
			op, err = s.reserved.Deployments.StopSite(r.Context(), key, parts[2], request)
		} else {
			op, err = s.reserved.Deployments.StartSite(r.Context(), key, parts[2], request)
		}
		operation(op, err, http.StatusAccepted)
	case len(parts) == 2 && parts[1] == "databases" && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		databases, err := s.reserved.Deployments.Databases(r.Context(), key)
		if err != nil {
			s.writeDeploymentError(w, id, err)
			return
		}
		s.writeJSON(w, http.StatusOK, databases)
	case len(parts) == 4 && parts[1] == "databases" && siteNamePattern.MatchString(parts[2]) && parts[3] == "remove" && r.Method == http.MethodPost:
		var request ConfirmedRequestV1
		if !body(&request) {
			bad()
			return
		}
		op, err := s.reserved.Deployments.RemoveData(r.Context(), key, parts[2], request)
		operation(op, err, http.StatusAccepted)
	case len(parts) == 2 && parts[1] == "clean" && r.Method == http.MethodPost:
		var request ConfirmedRequestV1
		if !body(&request) {
			bad()
			return
		}
		op, err := s.reserved.Deployments.CleanTarget(r.Context(), key, request)
		operation(op, err, http.StatusAccepted)
	case len(parts) == 2 && parts[1] == "drift" && r.Method == http.MethodPost:
		var request OperationRequestV1
		if !body(&request) {
			bad()
			return
		}
		op, err := s.reserved.Deployments.CheckDrift(r.Context(), key, request)
		operation(op, err, http.StatusAccepted)
	case len(parts) == 2 && parts[1] == "isolation" && r.Method == http.MethodPost:
		var request IsolationRequestV1
		if !body(&request) {
			bad()
			return
		}
		op, err := s.reserved.Deployments.CheckIsolation(r.Context(), key, request)
		operation(op, err, http.StatusAccepted)
	case len(parts) == 2 && parts[1] == "drift" && r.Method == http.MethodGet:
		if !requestBodyEmpty(r) {
			bad()
			return
		}
		drift, err := s.reserved.Deployments.Drift(r.Context(), key)
		if err != nil {
			s.writeDeploymentError(w, id, err)
			return
		}
		s.writeJSON(w, http.StatusOK, drift)
	default:
		bad()
	}
}

// writeSpreadsheets answers a backup's spreadsheets as they are made (Repo C BF-06b). Nothing is sent until the zip's first
// bytes are, so a backup whose data cannot be read is an error answer; a failure after that aborts the response, so the
// receiver never takes an unfinished zip for a whole one.
func (s *Server) writeSpreadsheets(w http.ResponseWriter, f io.ReadCloser, unavailable func()) {
	defer f.Close()
	first := make([]byte, 32<<10)
	n, _ := io.ReadAtLeast(f, first, 1)
	if n == 0 {
		unavailable()
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(first[:n]); err != nil {
		panic(http.ErrAbortHandler)
	}
	if _, err := io.Copy(w, f); err != nil {
		panic(http.ErrAbortHandler)
	}
}

// writeSiteBackupFile answers one backup of an app on a server as it is, never parsed or logged: the product hands it
// to the app's owner (Repo C BF-10c2). A short copy after the headers ends the response early, so the receiver sees
// fewer bytes than its Content-Length.
func (s *Server) writeSiteBackupFile(w http.ResponseWriter, r *http.Request, id, key, site, backup string) {
	f, size, err := s.reserved.Deployments.OpenBackup(r.Context(), key, site, backup)
	if err != nil {
		s.writeDeploymentError(w, id, err)
		return
	}
	defer f.Close()
	if size <= 0 {
		s.writeError(w, http.StatusServiceUnavailable, ErrorV1{Code: "target_unavailable", Message: "targets are unavailable", RequestID: id, Retryable: true})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.CopyN(w, f, size)
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
	case errors.Is(err, ErrSiteStopped):
		s.writeError(w, http.StatusConflict, ErrorV1{Code: "site_stopped", Message: "the app is stopped: start it first", RequestID: id})
	case errors.Is(err, ErrDataInUse):
		s.writeError(w, http.StatusConflict, ErrorV1{Code: "data_in_use", Message: "the app is still on this target: take it off first", RequestID: id})
	case errors.Is(err, ErrTargetInUse):
		s.writeError(w, http.StatusConflict, ErrorV1{Code: "target_in_use", Message: "sites or app data are still on this target", RequestID: id})
	case errors.Is(err, ErrBackupUnknown):
		s.writeError(w, http.StatusNotFound, ErrorV1{Code: "backup_unknown", Message: "backup is not kept or cannot be restored", RequestID: id})
	case errors.Is(err, ErrSetupPending):
		s.writeError(w, http.StatusConflict, ErrorV1{Code: "setup_pending", Message: "the app has no setup token yet: start it again", RequestID: id})
	case errors.Is(err, ErrOperationUnknown):
		s.writeError(w, http.StatusNotFound, ErrorV1{Code: "operation_unknown", Message: "operation is not known", RequestID: id})
	default:
		s.writeTargetError(w, id, err)
	}
}

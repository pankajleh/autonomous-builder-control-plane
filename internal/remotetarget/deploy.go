package remotetarget

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

// Deployments (Repo C design note BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md §11,
// BF-14.2a). A plan says what applying would do and is sealed by a hash; an
// applied plan becomes an operation that runs, step by step, fixed scripts on
// the target over its pinned key. Every value in a script is one ABCP checked
// against a narrow pattern first. On the server everything lives in the deploy
// login's ~/autobuild:
//
//	releases/<digest>/   a release's files, checked against its manifest
//	sites/<name>         a link to the release a site serves, switched in one step
//	frontdoor/           the front door's Caddyfile, certificates and state
//
// The front door is one rootless Caddy container, pinned by digest, that serves
// every site read-only and gets and renews its own certificates.

// FrontDoorImage is the front door's image, pinned by digest (Caddy 2.11).
const FrontDoorImage = "caddy@sha256:f2a1290d0463aad60660d4ec134943f183ee2a5f6c3eb7bf32dd984f2f020772"

const (
	planSchema        = 1
	operationSchema   = 1
	planLifetime      = 30 * time.Minute
	maxSites          = 50
	operationLimit    = 15 * time.Minute
	stepTimeout       = 5 * time.Minute
	checkWindow       = 3 * time.Minute
	defaultCheckEvery = 5 * time.Second
	maxCheckBody      = 64 << 20

	opQueued    = "queued"
	opRunning   = "running"
	opSucceeded = "succeeded"
	opFailed    = "failed"
	opUnknown   = "unknown"

	siteDeploying = "deploying"
	siteLive      = "live"
	siteFailed    = "failed"
	siteRemoving  = "removing"
	siteUnknown   = "unknown"
)

var (
	siteNamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	sitePart         = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
	planIDPattern    = regexp.MustCompile(`^pln-[0-9a-f]{16}$`)
	operationPattern = regexp.MustCompile(`^op-[0-9a-f]{16}$`)
)

// siteRecord is a site as ABCP keeps it in its target's record.
type siteRecord struct {
	Name string `json:"name"`
	Host string `json:"host"`
	// Kind is "node" for a Node app (BF-14.2b), empty for a static site.
	Kind      string                  `json:"kind,omitempty"`
	Database  string                  `json:"database,omitempty"`
	Sandbox   string                  `json:"sandbox,omitempty"`
	Release   string                  `json:"release"`
	State     string                  `json:"state"`
	UpdatedAt string                  `json:"updated_at"`
	LastCheck *serviceapi.SiteCheckV1 `json:"last_check,omitempty"`
	Operation string                  `json:"operation,omitempty"`
	// Relay is the credential the site's front-door block sends with
	// Autobuild's paths (BF-08a); never answered, never logged.
	Relay string `json:"relay,omitempty"`
}

func (s siteRecord) view() serviceapi.SiteV1 {
	return serviceapi.SiteV1{Name: s.Name, Host: s.Host, Kind: s.Kind, Database: s.Database, Sandbox: s.Sandbox, Release: s.Release,
		State: s.State, UpdatedAt: s.UpdatedAt, LastCheck: s.LastCheck, Operation: s.Operation}
}

type planRecord struct {
	SchemaVersion int               `json:"schema_version"`
	Plan          serviceapi.PlanV1 `json:"plan"`
	Applied       string            `json:"applied,omitempty"`
	// Conversion is the SQL of the plan's conversion, which Plan names by digest.
	Conversion string `json:"conversion,omitempty"`
}

type operationRecord struct {
	SchemaVersion int                    `json:"schema_version"`
	Operation     serviceapi.OperationV1 `json:"operation"`
	Host          string                 `json:"host,omitempty"`
	Release       string                 `json:"release,omitempty"`
	// Replaces and Sandbox are a Node app's (BF-14.2b): the release a failed
	// update switches back to, and the runtime its app runs on. Backup is the
	// backup a restore puts back or a check restores.
	Replaces string `json:"replaces,omitempty"`
	Sandbox  string `json:"sandbox,omitempty"`
	Backup   string `json:"backup,omitempty"`
	// Conversion is the SQL an apply runs on the app's data; Backup is then the
	// backup taken just before it, which puts the data back when the new
	// release never answers.
	Conversion string `json:"conversion,omitempty"`
	// Paused is a start whose app never answered and was stopped again (BF-14.5a).
	Paused bool `json:"paused,omitempty"`
	// Other and Crash are an isolation check's (P1b): the app the site is
	// checked against, and whether the site is crashed and started again.
	Other string `json:"other,omitempty"`
	Crash bool   `json:"crash,omitempty"`
}

// remote runs scripts on a target.
type remote interface {
	Run(ctx context.Context, script string, stdin io.Reader) (string, error)
	Close() error
}

// validHost is a public DNS name: lowercase, dotted, no address.
func validHost(host string) bool {
	if len(host) < 4 || len(host) > 253 || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return false
	}
	for _, part := range strings.Split(host, ".") {
		if !sitePart.MatchString(part) {
			return false
		}
	}
	return true
}

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func (m *Manager) stamp() string { return m.now().UTC().Format(time.RFC3339) }

// planHash is the SHA-256 of a plan's JSON without its hash.
func planHash(p serviceapi.PlanV1) string {
	p.Hash = ""
	data, _ := json.Marshal(p)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// CreatePlan says what putting a release on a target as a site would do, or
// restoring one of a Node app's backups (restore.go). Nothing on the target
// changes.
func (m *Manager) CreatePlan(_ context.Context, key string, create serviceapi.PlanCreateV1) (serviceapi.PlanV1, error) {
	if create.Restore != nil {
		return m.createRestorePlan(key, create)
	}
	name, host := create.Site.Name, strings.TrimSuffix(strings.ToLower(strings.TrimSpace(create.Site.Host)), ".")
	if !siteNamePattern.MatchString(name) || !validHost(host) || !digestPattern.MatchString(create.Release) {
		return serviceapi.PlanV1{}, serviceapi.ErrTargetInvalid
	}
	release, err := m.readRelease(create.Release)
	if err != nil {
		return serviceapi.PlanV1{}, err
	}
	m.mu.Lock()
	r, err := m.read(key)
	m.mu.Unlock()
	if err != nil {
		return serviceapi.PlanV1{}, err
	}
	if r.State != stateConfirmed {
		return serviceapi.PlanV1{}, serviceapi.ErrTargetNotReady
	}
	replaces := ""
	running := false
	for _, s := range r.Sites {
		if s.Host == host && s.Name != name {
			return serviceapi.PlanV1{}, serviceapi.ErrTargetInvalid
		}
		if s.Name == name {
			// A site keeps its kind: a static site becomes a Node app, or back,
			// only by removing it first.
			if s.Release != "" && siteKind(s) != release.Release.Kind {
				return serviceapi.PlanV1{}, serviceapi.ErrTargetInvalid
			}
			replaces = s.Release
		}
		running = running || s.State == siteLive
	}
	if replaces == "" && len(r.Sites) >= maxSites {
		return serviceapi.PlanV1{}, serviceapi.ErrTargetLimit
	}
	steps := []string{"send_files"}
	for _, d := range r.Releases {
		if d == release.Release.Digest {
			steps[0] = "reuse_files"
		}
	}
	frontdoor := "start_frontdoor"
	if running {
		frontdoor = "reload_frontdoor"
	}
	if replaces == release.Release.Digest {
		replaces = ""
	}
	if release.Release.Kind == kindNode {
		steps = nodeSteps(steps[0], frontdoor, replaces != "")
	} else {
		steps = append(steps, "switch_site", "write_site", frontdoor, "check")
	}
	if create.Conversion != "" {
		// A conversion is for a Node app that runs, with its data, and is
		// replaced by another release.
		site, _ := findSite(r, name)
		_, kept := findDatabase(r, name)
		if release.Release.Kind != kindNode || replaces == "" || !kept || !validConversion(create.Conversion) {
			return serviceapi.PlanV1{}, serviceapi.ErrTargetInvalid
		}
		if site.State == siteStopped {
			return serviceapi.PlanV1{}, serviceapi.ErrSiteStopped
		}
		steps = convertSteps(steps[0], frontdoor)
	}
	now := m.now().UTC()
	plan := serviceapi.PlanV1{
		SchemaVersion: serviceapi.PlanSchemaV1, PlanID: newID("pln-"), Target: key, Release: release.Release,
		Site: serviceapi.PlanSiteV1{Name: name, Host: host}, Replaces: replaces, Steps: steps,
		CreatedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(planLifetime).Format(time.RFC3339),
	}
	if release.Release.Kind == kindNode {
		plan.Images, plan.Database, plan.Sandbox = []string{NodeRuntimeImage, NodePostgresImage}, databaseName(name), sandbox(r)
	}
	if create.Conversion != "" {
		plan.Conversion = &serviceapi.PlanConversionV1{Digest: conversionDigest(create.Conversion), Bytes: len(create.Conversion)}
	}
	plan.Hash = planHash(plan)
	if err := m.writeJSON(filepath.Join(m.dir, "plans"), plan.PlanID, planRecord{SchemaVersion: planSchema, Plan: plan, Conversion: create.Conversion}); err != nil {
		return serviceapi.PlanV1{}, err
	}
	m.prunePlans(now)
	return plan, nil
}

// Apply carries out a plan an administrator approved, once, before it
// expires, and answers its operation. The same request answers the same
// operation.
func (m *Manager) Apply(_ context.Context, key string, apply serviceapi.ApplyV1) (serviceapi.OperationV1, error) {
	if !planIDPattern.MatchString(apply.PlanID) || !digestPattern.MatchString(apply.PlanHash) || !requestIDPattern.MatchString(apply.RequestID) {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return serviceapi.OperationV1{}, err
	}
	if existing, ok := m.operationByRequest(key, apply.RequestID); ok {
		if existing.Operation.PlanID != apply.PlanID {
			return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
		}
		return existing.Operation, nil
	}
	var pr planRecord
	if err := m.readJSON(filepath.Join(m.dir, "plans"), apply.PlanID, &pr); err != nil || pr.SchemaVersion != planSchema {
		return serviceapi.OperationV1{}, serviceapi.ErrPlanStale
	}
	plan := pr.Plan
	expires, _ := time.Parse(time.RFC3339, plan.ExpiresAt)
	if plan.Target != key || plan.Hash != apply.PlanHash || planHash(plan) != plan.Hash || pr.Applied != "" || !m.now().Before(expires) {
		return serviceapi.OperationV1{}, serviceapi.ErrPlanStale
	}
	if r.State != stateConfirmed {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetNotReady
	}
	if _, err := m.readRelease(plan.Release.Digest); err != nil {
		return serviceapi.OperationV1{}, err
	}
	if m.busy[key] {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetBusy
	}
	for _, s := range r.Sites {
		if s.Name == plan.Site.Name && s.Release != plan.Replaces && !(plan.Replaces == "" && s.Release == plan.Release.Digest) {
			// The site changed since the plan was made.
			return serviceapi.OperationV1{}, serviceapi.ErrPlanStale
		}
	}
	kind, backup := "apply", ""
	if plan.Kind == planRestore {
		if restoreStale(r, plan) {
			return serviceapi.OperationV1{}, serviceapi.ErrPlanStale
		}
		kind, backup = "restore", plan.Backup.BackupID
	}
	conversion := ""
	if plan.Conversion != nil {
		// The SQL is the one the approval covers, and the app still runs.
		site, _ := findSite(r, plan.Site.Name)
		if pr.Conversion == "" || conversionDigest(pr.Conversion) != plan.Conversion.Digest || len(pr.Conversion) != plan.Conversion.Bytes ||
			site.State == siteStopped {
			return serviceapi.OperationV1{}, serviceapi.ErrPlanStale
		}
		conversion = pr.Conversion
	}
	op := serviceapi.OperationV1{
		SchemaVersion: serviceapi.OperationSchemaV1, OperationID: newID("op-"), Target: key, Kind: kind,
		Site: plan.Site.Name, PlanID: plan.PlanID, RequestID: apply.RequestID, State: opQueued, StartedAt: m.stamp(),
	}
	for _, step := range plan.Steps {
		op.Steps = append(op.Steps, serviceapi.OperationStepV1{Name: step, State: "pending"})
	}
	rec := operationRecord{SchemaVersion: operationSchema, Operation: op, Host: plan.Site.Host, Release: plan.Release.Digest,
		Replaces: plan.Replaces, Sandbox: plan.Sandbox, Backup: backup, Conversion: conversion}
	if err := m.writeJSON(filepath.Join(m.dir, "operations"), op.OperationID, rec); err != nil {
		return serviceapi.OperationV1{}, err
	}
	pr.Applied = op.OperationID
	if err := m.writeJSON(filepath.Join(m.dir, "plans"), plan.PlanID, pr); err != nil {
		return serviceapi.OperationV1{}, err
	}
	// The site keeps the release it serves until its link is switched.
	site, _ := findSite(r, plan.Site.Name)
	site.Name, site.Host, site.State, site.UpdatedAt, site.Operation = plan.Site.Name, plan.Site.Host, siteDeploying, m.stamp(), op.OperationID
	if plan.Release.Kind == kindNode {
		site.Kind, site.Database, site.Sandbox = kindNode, plan.Database, plan.Sandbox
	}
	m.setSite(&r, site)
	if err := m.write(r); err != nil {
		return serviceapi.OperationV1{}, err
	}
	m.busy[key] = true
	m.wg.Add(1)
	go m.run(rec)
	return op, nil
}

// RemoveSite takes a site off its target: its block in the front door, its
// link and the releases only it used. The front door stops with the last site.
func (m *Manager) RemoveSite(_ context.Context, key, name string, remove serviceapi.SiteRemoveV1) (serviceapi.OperationV1, error) {
	if !siteNamePattern.MatchString(name) || !requestIDPattern.MatchString(remove.RequestID) {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return serviceapi.OperationV1{}, err
	}
	if existing, ok := m.operationByRequest(key, remove.RequestID); ok {
		if existing.Operation.Kind != "remove" || existing.Operation.Site != name {
			return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
		}
		return existing.Operation, nil
	}
	site, ok := findSite(r, name)
	if !ok {
		return serviceapi.OperationV1{}, serviceapi.ErrSiteUnknown
	}
	if r.State != stateConfirmed {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetNotReady
	}
	if m.busy[key] {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetBusy
	}
	op := serviceapi.OperationV1{
		SchemaVersion: serviceapi.OperationSchemaV1, OperationID: newID("op-"), Target: key, Kind: "remove", Site: name,
		RequestID: remove.RequestID, State: opQueued, StartedAt: m.stamp(),
		Steps: []serviceapi.OperationStepV1{{Name: "unlink_site", State: "pending"}, {Name: "write_site", State: "pending"},
			{Name: "reload_frontdoor", State: "pending"}, {Name: "remove_files", State: "pending"}},
	}
	if siteKind(site) == kindNode {
		// A Node app stops first; its database and backups are kept.
		op.Steps = append([]serviceapi.OperationStepV1{{Name: "stop_app", State: "pending"}}, op.Steps...)
	}
	rec := operationRecord{SchemaVersion: operationSchema, Operation: op, Host: site.Host, Release: site.Release}
	if err := m.writeJSON(filepath.Join(m.dir, "operations"), op.OperationID, rec); err != nil {
		return serviceapi.OperationV1{}, err
	}
	site.State, site.UpdatedAt, site.Operation = siteRemoving, m.stamp(), op.OperationID
	m.setSite(&r, site)
	if err := m.write(r); err != nil {
		return serviceapi.OperationV1{}, err
	}
	m.busy[key] = true
	m.wg.Add(1)
	go m.run(rec)
	return op, nil
}

// Operation answers an operation on a target.
func (m *Manager) Operation(_ context.Context, key, id string) (serviceapi.OperationV1, error) {
	if !operationPattern.MatchString(id) {
		return serviceapi.OperationV1{}, serviceapi.ErrOperationUnknown
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.read(key); err != nil {
		return serviceapi.OperationV1{}, err
	}
	var rec operationRecord
	if err := m.readJSON(filepath.Join(m.dir, "operations"), id, &rec); err != nil || rec.Operation.Target != key {
		return serviceapi.OperationV1{}, serviceapi.ErrOperationUnknown
	}
	return rec.Operation, nil
}

// Sites lists a target's sites.
func (m *Manager) Sites(_ context.Context, key string) (serviceapi.SitesV1, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return serviceapi.SitesV1{}, err
	}
	sites := serviceapi.SitesV1{SchemaVersion: serviceapi.SitesSchemaV1, Target: key, Sites: []serviceapi.SiteV1{}}
	for _, s := range r.Sites {
		sites.Sites = append(sites.Sites, s.view())
	}
	return sites, nil
}

// Wait returns once no operation is running (for tests and shutdown).
func (m *Manager) Wait() { m.wg.Wait() }

// run carries out one operation and records each step as it goes.
func (m *Manager) run(rec operationRecord) {
	defer m.wg.Done()
	ctx, cancel := context.WithTimeout(context.Background(), operationLimit)
	defer cancel()
	op := &rec.Operation
	key := op.Target
	failed := ""
	defer func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.busy, key)
		op.EndedAt = m.stamp()
		if failed == "" {
			op.State = opSucceeded
		} else {
			op.State = opFailed
		}
		_ = m.writeJSON(filepath.Join(m.dir, "operations"), op.OperationID, rec)
		r, err := m.read(key)
		if err != nil {
			return
		}
		site, ok := findSite(r, op.Site)
		switch {
		case op.Kind == "remove" && failed == "":
			m.dropSite(&r, op.Site)
		case ok && site.Operation == op.OperationID && failed == "":
			site.State, site.UpdatedAt = siteLive, m.stamp()
			if op.Kind == "stop" {
				site.State = siteStopped
			}
			m.setSite(&r, site)
		case ok && site.Operation == op.OperationID:
			site.State, site.UpdatedAt = siteFailed, m.stamp()
			if rec.Paused {
				site.State = siteStopped
			}
			m.setSite(&r, site)
		}
		_ = m.write(r)
	}()
	m.mu.Lock()
	op.State = opRunning
	_ = m.writeJSON(filepath.Join(m.dir, "operations"), op.OperationID, rec)
	r, err := m.read(key)
	m.mu.Unlock()
	if err != nil {
		failed = "target_unreadable"
		m.markFirstPending(&rec, "failed", failed)
		return
	}
	if op.Kind == "check_site" {
		failed = m.runCheckSite(ctx, &rec, r)
		return
	}
	session, outcome := m.open(ctx, r)
	if session == nil {
		failed = outcome
		m.markFirstPending(&rec, "failed", outcome)
		return
	}
	defer session.Close()
	switch op.Kind {
	case "remove":
		failed = m.runRemove(ctx, &rec, r, session)
	case "backup":
		failed = m.runBackup(ctx, &rec, r, session)
	case "restore":
		failed = m.runRestore(ctx, &rec, r, session)
	case "check_backup":
		failed = m.runCheckBackup(ctx, &rec, r, session)
	case "nightly":
		failed = m.runNightly(ctx, &rec, r, session)
	case "stop":
		failed = m.runStop(ctx, &rec, r, session)
	case "start":
		failed = m.runStart(ctx, &rec, r, session)
	case "remove_data":
		failed = m.runRemoveData(ctx, &rec, r, session)
	case "clean":
		failed = m.runClean(ctx, &rec, r, session)
	case "drift":
		failed = m.runDrift(ctx, &rec, r, session)
	case "isolation":
		failed = m.runIsolation(ctx, &rec, r, session)
	case "relay":
		failed = m.runRelay(ctx, &rec, r, session)
	default:
		failed = m.runApply(ctx, &rec, r, session)
	}
}

// step runs one named step, recording it before and after.
func (m *Manager) step(rec *operationRecord, name string, work func() (string, error)) error {
	return m.siteStep(rec, name, "", work)
}

// siteStep runs the named step for one app of the nightly backups.
func (m *Manager) siteStep(rec *operationRecord, name, site string, work func() (string, error)) error {
	m.setStep(rec, name, site, "running", "")
	detail, err := work()
	if err != nil {
		m.setStep(rec, name, site, "failed", stepDetail(err))
		return err
	}
	m.setStep(rec, name, site, "done", detail)
	return nil
}

func (m *Manager) setStep(rec *operationRecord, name, site, state, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range rec.Operation.Steps {
		if rec.Operation.Steps[i].Name == name && rec.Operation.Steps[i].Site == site {
			rec.Operation.Steps[i].State, rec.Operation.Steps[i].At, rec.Operation.Steps[i].Detail = state, m.stamp(), detail
		}
	}
	_ = m.writeJSON(filepath.Join(m.dir, "operations"), rec.Operation.OperationID, *rec)
}

func (m *Manager) markFirstPending(rec *operationRecord, state, detail string) {
	for _, s := range rec.Operation.Steps {
		if s.State == "pending" {
			m.setStep(rec, s.Name, s.Site, state, detail)
			return
		}
	}
}

func (m *Manager) runApply(ctx context.Context, rec *operationRecord, r record, session remote) string {
	op := &rec.Operation
	release, err := m.readRelease(rec.Release)
	if err != nil {
		m.markFirstPending(rec, "failed", "release_missing")
		return "release_missing"
	}
	site := op.Site
	converting := rec.Conversion != ""
	// again starts the app that ran before on the data it had: a conversion
	// stopped before it changed anything.
	again := func(err error) error {
		if _, startErr := m.startApp(ctx, session, r.Key, site, rec.Sandbox); startErr != nil {
			return errStep(stepDetail(err) + "; nothing changed but the app did not start again")
		}
		return errStep(stepDetail(err) + "; nothing changed and the app is started again")
	}
	// undo puts back the data a conversion changed and the release it replaced.
	undo := func(err error) error {
		return errStep(stepDetail(err) + "; " + m.unConvert(ctx, rec, r.Key, session) + "; " + m.rollBack(ctx, rec, r.Key, session))
	}
	for _, s := range op.Steps {
		var err error
		switch s.Name {
		case "stop_app":
			err = m.step(rec, s.Name, func() (string, error) {
				out, err := run(ctx, session, fill(stopAppOnlyScript, "N", site), nil)
				if err != nil {
					return "", again(err)
				}
				return out, nil
			})
		case "convert":
			err = m.step(rec, s.Name, func() (string, error) {
				out, err := m.convert(ctx, session, site, rec.Conversion)
				if err != nil {
					return "", again(err)
				}
				return out, nil
			})
		case "send_files", "reuse_files":
			err = m.step(rec, s.Name, func() (string, error) { return m.sendRelease(ctx, session, release) })
			if err == nil {
				m.mu.Lock()
				if cur, readErr := m.read(r.Key); readErr == nil {
					if !contains(cur.Releases, release.Release.Digest) {
						cur.Releases = append(cur.Releases, release.Release.Digest)
						sort.Strings(cur.Releases)
					}
					_ = m.write(cur)
				}
				m.mu.Unlock()
			}
		case "switch_site":
			err = m.step(rec, s.Name, func() (string, error) {
				out, err := run(ctx, session, fill(switchScript, "N", site, "D", release.Release.Digest), nil)
				if err != nil && converting {
					return "", undo(err)
				}
				return out, err
			})
			if err == nil {
				m.mu.Lock()
				if cur, readErr := m.read(r.Key); readErr == nil {
					if linked, ok := findSite(cur, site); ok {
						linked.Release = release.Release.Digest
						m.setSite(&cur, linked)
						_ = m.write(cur)
					}
				}
				m.mu.Unlock()
			}
		case "write_site":
			err = m.step(rec, s.Name, func() (string, error) {
				return run(ctx, session, fill(writeSiteScript, "NETS", m.nodeNetworks(r.Key)), strings.NewReader(m.caddyfile(r.Key, site, rec.Host)))
			})
		case "start_frontdoor", "reload_frontdoor":
			err = m.step(rec, s.Name, func() (string, error) {
				return run(ctx, session, fill(frontDoorScript, "NETS", m.nodeNetworks(r.Key)), nil)
			})
		case "check":
			err = m.step(rec, s.Name, func() (string, error) { return m.checkSite(ctx, r, rec.Host, release) })
		case "get_images":
			err = m.step(rec, s.Name, func() (string, error) { return run(ctx, session, getImagesScript, nil) })
		case "start_database":
			err = m.step(rec, s.Name, func() (string, error) { return run(ctx, session, startDatabaseScript, nil) })
		case "make_database":
			err = m.step(rec, s.Name, func() (string, error) {
				return run(ctx, session, fill(makeDatabaseScript, "N", site, "DB", databaseName(site)), nil)
			})
			if err == nil {
				m.mu.Lock()
				if cur, readErr := m.read(r.Key); readErr == nil {
					if _, ok := findDatabase(cur, site); !ok {
						setDatabase(&cur, databaseRecord{Site: site, Name: databaseName(site), CreatedAt: m.stamp()})
						_ = m.write(cur)
					}
				}
				m.mu.Unlock()
			}
		case "back_up":
			err = m.step(rec, s.Name, func() (string, error) {
				id, detail, err := m.backUp(ctx, session, r.Key, site, reasonBeforeUpdate, rec.Replaces, "")
				if converting {
					if err != nil {
						return "", again(err)
					}
					// The data a conversion changes comes back from this backup.
					rec.Backup = id
				}
				return detail, err
			})
		case "start_app":
			err = m.step(rec, s.Name, func() (string, error) {
				out, err := m.startApp(ctx, session, r.Key, site, rec.Sandbox)
				if err != nil && converting {
					return "", undo(err)
				}
				return out, err
			})
		case "wait_health":
			err = m.step(rec, s.Name, func() (string, error) {
				out, err := run(ctx, session, fill(waitHealthScript, "N", site, "WAIT", fmt.Sprint(m.healthWait)), nil)
				if err != nil && converting {
					return "", undo(err)
				}
				if err != nil {
					return "", errStep(stepDetail(err) + "; " + m.rollBack(ctx, rec, r.Key, session))
				}
				return out, nil
			})
		}
		if err != nil {
			return stepDetail(err)
		}
	}
	m.pruneReleases(ctx, session, r.Key)
	return ""
}

func (m *Manager) runRemove(ctx context.Context, rec *operationRecord, r record, session remote) string {
	site := rec.Operation.Site
	steps := []struct {
		name string
		work func() (string, error)
	}{
		{"stop_app", func() (string, error) { return run(ctx, session, fill(stopAppScript, "N", site), nil) }},
		{"unlink_site", func() (string, error) { return run(ctx, session, fill(unlinkScript, "N", site), nil) }},
		{"write_site", func() (string, error) {
			return run(ctx, session, fill(writeSiteScript, "NETS", m.nodeNetworks(r.Key)), strings.NewReader(m.caddyfile(r.Key, site, "")))
		}},
		{"reload_frontdoor", func() (string, error) {
			if m.liveSitesExcept(r.Key, site) == 0 {
				return run(ctx, session, stopFrontDoorScript, nil)
			}
			return run(ctx, session, fill(frontDoorScript, "NETS", m.nodeNetworks(r.Key)), nil)
		}},
		{"remove_files", func() (string, error) { return m.pruneReleases(ctx, session, r.Key, site), nil }},
	}
	node := false
	for _, s := range rec.Operation.Steps {
		node = node || s.Name == "stop_app"
	}
	for _, s := range steps {
		if s.name == "stop_app" && !node {
			continue
		}
		if err := m.step(rec, s.name, s.work); err != nil {
			return stepDetail(err)
		}
	}
	return ""
}

// sendRelease puts a release's files on the target and checks them there,
// unless they are there already.
func (m *Manager) sendRelease(ctx context.Context, session remote, release releaseRecord) (string, error) {
	digest := release.Release.Digest
	present, err := run(ctx, session, fill(prepareScript, "D", digest), nil)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(present) == "present" {
		return "already there", nil
	}
	file, err := os.Open(filepath.Join(m.releaseDir(), digest+".tar"))
	if err != nil {
		return "", errStep("release_missing")
	}
	defer file.Close()
	if _, err := run(ctx, session, fill(receiveScript, "D", digest), file); err != nil {
		return "", err
	}
	count := fmt.Sprint(len(release.Files))
	if _, err := run(ctx, session, fill(verifyScript, "D", digest, "COUNT", count), bytes.NewReader(checksumLines(release.Files))); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d files", len(release.Files)), nil
}

// checkSite fetches the site over HTTPS from the target's address and compares
// it with the release's index.html, while the front door gets its certificate.
func (m *Manager) checkSite(ctx context.Context, r record, host string, release releaseRecord) (string, error) {
	ips, err := m.addresses(ctx, r.Address)
	if err != nil {
		return "", errStep("refused_address")
	}
	// A static site must answer / with its index.html; a Node app must answer
	// /health (any answer with status 200).
	path, want, node := "/", "", release.Release.Kind == kindNode
	if node {
		path = "/health"
	}
	for _, f := range release.Files {
		if f.Path == "index.html" {
			want = f.SHA256
		}
	}
	resolved, _ := m.resolve(ctx, host)
	pointed := false
	for _, ip := range resolved {
		pointed = pointed || ip.Equal(ips[0])
	}
	if !pointed {
		m.recordCheck(r.Key, host, false, "dns")
		return "", errStep("dns")
	}
	deadline := time.Now().Add(m.checkFor)
	last := "unreachable"
	for {
		got, err := m.fetch(ctx, ips[0], host, path)
		switch {
		case err == nil && node:
			m.recordCheck(r.Key, host, true, "")
			return "answers /health", nil
		case err == nil && got == want:
			m.recordCheck(r.Key, host, true, "")
			return "answers with this release", nil
		case err == nil:
			last = "content"
		default:
			last = stepDetail(err)
		}
		if time.Now().After(deadline) {
			m.recordCheck(r.Key, host, false, last)
			return "", errStep(last)
		}
		select {
		case <-ctx.Done():
			return "", errStep("timeout")
		case <-time.After(m.checkEvery):
		}
	}
}

func (m *Manager) recordCheck(key, host string, ok bool, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return
	}
	for i := range r.Sites {
		if r.Sites[i].Host == host {
			r.Sites[i].LastCheck = &serviceapi.SiteCheckV1{At: m.stamp(), OK: ok, Detail: detail}
		}
	}
	_ = m.write(r)
}

// httpsFetch reads https://host<path> from ip, checking the certificate for
// host, and answers the SHA-256 of what came back.
func httpsFetch(ctx context.Context, ip net.IP, host, path string) (string, error) {
	dialer := &net.Dialer{Timeout: dialTimeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), "443"))
		},
		TLSClientConfig:       &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: 15 * time.Second,
		DisableKeepAlives:     true,
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+path, nil)
	if err != nil {
		return "", errStep("failed")
	}
	resp, err := client.Do(req)
	if err != nil {
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			return "", errStep("certificate")
		}
		return "", errStep("unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errStep("status")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(resp.Body, maxCheckBody)); err != nil {
		return "", errStep("unreachable")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// served is true for a site whose link is on the server and that is not being
// removed: a site whose last update failed keeps serving the release it has.
func served(s siteRecord) bool { return s.Release != "" && s.State != siteRemoving }

// caddyfile is the front door's configuration for every site the target
// serves, with site answering on host, or left out when host is empty.
func (m *Manager) caddyfile(key, site, host string) string {
	m.mu.Lock()
	r, _ := m.read(key)
	relay := m.relayHost
	m.mu.Unlock()
	return caddyfileFor(r, site, host, relay)
}

// A Node app refuses a write whose Origin is not its own address, which it
// sees as plain http because the front door ends TLS. The front door hands it
// the browser's Origin with only that scheme changed, so a write from the
// app's own page is its own and one from any other site is still refused. An
// app that took its scheme from X-Forwarded-Proto would need this gone.
const (
	originFrom = "^https://"
	originTo   = "http://"
)

// caddyfileFor is caddyfile for a record already read. A stopped Node app's
// host answers a plain 503 saying it is paused (BF-14.5a). With relayHost set,
// a site that has a relay credential sends Autobuild's paths (/__autobuild/*)
// to that host instead of serving them (BF-08a): with the credential, the host
// the visitor used and the visitor's address, and without the visitor's
// cookies or credentials; the same headers sent by anyone else are removed.
// Such a running Node app's own server reaches the same host on the inside
// listener (BF-08b2), known by its network's range in inside/<site>.caddy,
// for a connection's status read alone, marked as the server asking.
func caddyfileFor(r record, site, host, relayHost string) string {
	hosts, kinds, paused, relays := map[string]string{}, map[string]string{}, map[string]bool{}, map[string]string{}
	inside := map[string]bool{}
	for _, s := range r.Sites {
		kinds[s.Name] = siteKind(s)
		paused[s.Name] = s.State == siteStopped
		if served(s) {
			hosts[s.Name] = s.Host
			// Exactly the sites whose matcher insideScript writes (nodeNetworks).
			inside[s.Name] = siteKind(s) == kindNode && siteNamePattern.MatchString(s.Name)
		}
		if relayHost != "" && relayCredentialPattern.MatchString(s.Relay) {
			relays[s.Name] = s.Relay
		}
	}
	delete(hosts, site)
	if host != "" {
		hosts[site] = host
	}
	names := make([]string, 0, len(hosts))
	for name := range hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# Written by ABCP (BF-14.2a, BF-14.2b, BF-14.2c). A static site serves the release its link points to; a Node app is its container.\n")
	for _, name := range names {
		serve := "\troot * /srv/sites/" + name + "\n\tfile_server\n"
		switch {
		case kinds[name] == kindNode && paused[name]:
			serve = "\trespond \"This app is paused.\" 503\n"
		case kinds[name] == kindNode:
			serve = "\treverse_proxy autobuild-app-" + name + ":8080 {\n\t\theader_up Origin " + originFrom + " " + originTo + "\n\t}\n"
		}
		if credential, ok := relays[name]; ok && !paused[name] {
			serve = "\trequest_header -X-Autobuild-Site\n\trequest_header -X-Autobuild-Host\n\trequest_header -X-Autobuild-Client\n\trequest_header -X-Autobuild-Caller\n" +
				"\thandle /__autobuild/* {\n\t\treverse_proxy https://" + relayHost + " {\n\t\t\theader_up Host " + relayHost +
				"\n\t\t\theader_up X-Autobuild-Site " + credential + "\n\t\t\theader_up X-Autobuild-Host {host}\n" +
				"\t\t\theader_up X-Autobuild-Client {remote_host}\n\t\t\theader_up -Cookie\n\t\t\theader_up -Authorization\n\t\t}\n\t}\n" +
				"\thandle {\n" + indent(serve) + "\t}\n"
		}
		fmt.Fprintf(&b, "\n%s {\n%s\tencode zstd gzip\n\theader {\n\t\tX-Content-Type-Options nosniff\n\t\tReferrer-Policy strict-origin-when-cross-origin\n\t\t-Server\n\t}\n}\n", hosts[name], serve)
	}
	var servers strings.Builder
	for _, name := range names {
		credential, ok := relays[name]
		if !ok || paused[name] || !inside[name] {
			continue
		}
		fmt.Fprintf(&servers, "\timport inside/%s.caddy\n\thandle @from_%s {\n\t\treverse_proxy https://%s {\n\t\t\theader_up Host %s\n"+
			"\t\t\theader_up X-Autobuild-Site %s\n\t\t\theader_up X-Autobuild-Host %s\n\t\t\theader_up X-Autobuild-Client {remote_host}\n"+
			"\t\t\theader_up X-Autobuild-Caller server\n\t\t\theader_up -Cookie\n\t\t\theader_up -Authorization\n\t\t}\n\t}\n",
			name, name, relayHost, relayHost, credential, hosts[name])
	}
	if servers.Len() > 0 {
		fmt.Fprintf(&b, "\n# The inside listener (BF-08b2): an app's own server asks Autobuild here, known by its network's range.\nhttp://:%d {\n"+
			"\trequest_header -X-Autobuild-Site\n\trequest_header -X-Autobuild-Host\n\trequest_header -X-Autobuild-Client\n"+
			"\trequest_header -X-Autobuild-Caller\n\trequest_header -X-Autobuild-Relay-Token\n%s\trespond 404\n}\n", insidePort, servers.String())
	}
	return b.String()
}

// The front door's inside listener (BF-08b2): plain http on a port never
// published, which a Node app reaches on its own network.
const (
	insidePort  = 8089
	insideRelay = "http://autobuild-frontdoor:8089"
)

// indent puts one more tab before each line of a site block's body.
func indent(block string) string {
	lines := strings.SplitAfter(block, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = "\t" + line
		}
	}
	return strings.Join(lines, "")
}

func (m *Manager) liveSitesExcept(key, site string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return 0
	}
	n := 0
	for _, s := range r.Sites {
		if s.Name != site && served(s) {
			n++
		}
	}
	return n
}

// pruneReleases removes from the target the releases no site uses (apart
// from the site being removed) and answers what it removed.
func (m *Manager) pruneReleases(ctx context.Context, session remote, key string, removing ...string) string {
	m.mu.Lock()
	r, err := m.read(key)
	m.mu.Unlock()
	if err != nil {
		return ""
	}
	used := map[string]bool{}
	for _, s := range r.Sites {
		if len(removing) == 0 || s.Name != removing[0] {
			used[s.Release] = true
		}
	}
	var keep, drop []string
	for _, d := range r.Releases {
		if used[d] {
			keep = append(keep, d)
		} else if digestPattern.MatchString(d) {
			drop = append(drop, d)
		}
	}
	if len(drop) == 0 {
		return "nothing to remove"
	}
	if _, err := run(ctx, session, fill(removeReleasesScript, "LIST", strings.Join(drop, " ")), nil); err != nil {
		return "left in place"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, err := m.read(key); err == nil {
		cur.Releases = keep
		_ = m.write(cur)
	}
	return fmt.Sprintf("%d removed", len(drop))
}

// open signs in to a target with its key and the pinned host key only.
func (m *Manager) open(ctx context.Context, r record) (remote, string) {
	if m.remote != nil {
		return m.remote(ctx, r)
	}
	client, outcome := m.pinnedClient(ctx, r, operationLimit)
	if client == nil {
		return nil, outcome
	}
	return sshRemote{client: client}, ""
}

// pinnedClient signs in with the target's key, accepting only the pinned host key.
func (m *Manager) pinnedClient(ctx context.Context, r record, deadline time.Duration) (*ssh.Client, string) {
	pinned, _, _, _, err := ssh.ParseAuthorizedKey([]byte(r.HostKey))
	if err != nil {
		return nil, "failed"
	}
	data, err := os.ReadFile(filepath.Join(m.dir, r.Key+".key"))
	if err != nil {
		return nil, "failed"
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, "failed"
	}
	config := &ssh.ClientConfig{
		User: r.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if !bytes.Equal(key.Marshal(), pinned.Marshal()) {
				return errKeyChanged
			}
			return nil
		},
		HostKeyAlgorithms: hostKeyAlgorithms(pinned),
	}
	return m.connectFor(ctx, r, config, deadline)
}

type sshRemote struct{ client *ssh.Client }

func (s sshRemote) Close() error { return s.client.Close() }

// Run runs a script through `sh -c` with stdin, answering its output; a
// failure carries the last line it printed on stderr.
func (s sshRemote) Run(ctx context.Context, script string, stdin io.Reader) (string, error) {
	session, err := s.client.NewSession()
	if err != nil {
		return "", errStep("failed")
	}
	defer session.Close()
	var out, errOut limitedBuffer
	if stdin != nil {
		session.Stdin = stdin
	}
	session.Stdout, session.Stderr = &out, &errOut
	done := make(chan error, 1)
	go func() { done <- session.Run("sh -c " + shellQuote(script)) }()
	select {
	case err = <-done:
	case <-ctx.Done():
		return out.String(), errStep("timeout")
	case <-time.After(stepTimeout):
		return out.String(), errStep("timeout")
	}
	if err != nil {
		return out.String(), errStep(lastLine(errOut.String()))
	}
	return out.String(), nil
}

// run runs one script on a target.
func run(ctx context.Context, session remote, script string, stdin io.Reader) (string, error) {
	return session.Run(ctx, script, stdin)
}

type stepError string

func (e stepError) Error() string { return string(e) }

func errStep(detail string) error {
	if detail == "" {
		detail = "failed"
	}
	return stepError(detail)
}

// stepDetail is a step's failure in words fit to show: bounded and printable.
func stepDetail(err error) string {
	var se stepError
	detail := "failed"
	if errors.As(err, &se) {
		detail = string(se)
	}
	detail = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, detail)
	if len(detail) > 200 {
		detail = detail[:200]
	}
	return detail
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// fill puts checked values into a script: each placeholder is @NAME@.
func fill(script string, pairs ...string) string {
	for i := 0; i+1 < len(pairs); i += 2 {
		script = strings.ReplaceAll(script, "@"+pairs[i]+"@", pairs[i+1])
	}
	return script
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func findSite(r record, name string) (siteRecord, bool) {
	for _, s := range r.Sites {
		if s.Name == name {
			return s, true
		}
	}
	return siteRecord{}, false
}

func (m *Manager) setSite(r *record, site siteRecord) {
	for i := range r.Sites {
		if r.Sites[i].Name == site.Name {
			r.Sites[i] = site
			return
		}
	}
	r.Sites = append(r.Sites, site)
	sort.Slice(r.Sites, func(i, j int) bool { return r.Sites[i].Name < r.Sites[j].Name })
}

func (m *Manager) dropSite(r *record, name string) {
	kept := r.Sites[:0]
	for _, s := range r.Sites {
		if s.Name != name {
			kept = append(kept, s)
		}
	}
	r.Sites = kept
}

func (m *Manager) operationByRequest(key, requestID string) (operationRecord, bool) {
	entries, err := os.ReadDir(filepath.Join(m.dir, "operations"))
	if err != nil {
		return operationRecord{}, false
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		if !operationPattern.MatchString(name) {
			continue
		}
		var rec operationRecord
		if m.readJSON(filepath.Join(m.dir, "operations"), name, &rec) == nil && rec.Operation.Target == key && rec.Operation.RequestID == requestID {
			return rec, true
		}
	}
	return operationRecord{}, false
}

// interrupted marks the operations a previous ABCP left running as unknown,
// and their sites too: unknown until reconciled, never repeated blindly.
func (m *Manager) interrupted() {
	dir := filepath.Join(m.dir, "operations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		var rec operationRecord
		if !operationPattern.MatchString(name) || m.readJSON(dir, name, &rec) != nil {
			continue
		}
		if rec.Operation.State != opQueued && rec.Operation.State != opRunning {
			continue
		}
		rec.Operation.State, rec.Operation.EndedAt = opUnknown, m.stamp()
		_ = m.writeJSON(dir, name, rec)
		if r, err := m.read(rec.Operation.Target); err == nil {
			if site, ok := findSite(r, rec.Operation.Site); ok && site.Operation == name {
				site.State, site.UpdatedAt = siteUnknown, m.stamp()
				m.setSite(&r, site)
				_ = m.write(r)
			}
		}
	}
}

func (m *Manager) prunePlans(now time.Time) {
	dir := filepath.Join(m.dir, "plans")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		var pr planRecord
		if !planIDPattern.MatchString(name) || m.readJSON(dir, name, &pr) != nil {
			continue
		}
		expires, err := time.Parse(time.RFC3339, pr.Plan.ExpiresAt)
		if err == nil && now.After(expires.Add(24*time.Hour)) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func (m *Manager) writeJSON(dir, name string, v any) error {
	if err := privateDirectoryCreate(dir); err != nil {
		return serviceapi.ErrTargetUnavailable
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return serviceapi.ErrTargetUnavailable
	}
	if err := writePrivate(filepath.Join(dir, name+".json"), append(data, '\n')); err != nil {
		return serviceapi.ErrTargetUnavailable
	}
	return nil
}

func (m *Manager) readJSON(dir, name string, v any) error {
	data, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if err != nil {
		return err
	}
	return strictjson.Decode(data, v)
}

// The scripts. Each value in them (@D@ a release digest, @N@ a site name,
// @COUNT@ a number, @LIST@ digests, @NETS@ site names) is checked by ABCP
// against a narrow pattern before it is put in; nothing else from a request
// reaches a server.
const (
	dockerHost = `[ -S "/run/user/$(id -u)/docker.sock" ] && export DOCKER_HOST="unix:///run/user/$(id -u)/docker.sock"`

	prepareScript = `set -eu
umask 022
A="$HOME/autobuild"
mkdir -p "$A/releases" "$A/sites" "$A/frontdoor/config" "$A/frontdoor/data" "$A/frontdoor/state"
if [ -d "$A/releases/@D@" ]; then echo present; else echo absent; fi
`
	receiveScript = `set -eu
umask 022
R="$HOME/autobuild/releases"
rm -rf "$R/.@D@.part"
mkdir "$R/.@D@.part"
tar -x -f - -C "$R/.@D@.part" --no-same-owner --no-same-permissions --exclude=autobuild-release.json
`
	verifyScript = `set -eu
cd "$HOME/autobuild/releases/.@D@.part"
sha256sum --quiet --strict -c -
if [ -n "$(find . ! -type f ! -type d)" ]; then echo "release holds something that is not a file" >&2; exit 3; fi
if [ "$(find . -type f | wc -l)" -ne @COUNT@ ]; then echo "release holds other files" >&2; exit 3; fi
cd ..
mv ".@D@.part" "@D@"
`
	switchScript = `set -eu
S="$HOME/autobuild/sites"
test -d "$HOME/autobuild/releases/@D@"
ln -sfn "../releases/@D@" "$S/.@N@.next"
mv -T "$S/.@N@.next" "$S/@N@"
`
	unlinkScript = `set -eu
rm -f "$HOME/autobuild/sites/@N@"
`
	writeSiteScript = `set -eu
` + dockerHost + `
C="$HOME/autobuild/frontdoor/config"
mkdir -p "$C"
` + insideScript + `cat > "$C/Caddyfile.next"
mv "$C/Caddyfile.next" "$C/Caddyfile"
`
	// insideScript writes, for each Node site (@NETS@), the matcher the front
	// door's inside listener knows its app's server by (BF-08b2): its
	// network's range, and only a connection's status read. A site whose
	// network is not found gets a range nothing comes from. Both front-door
	// scripts run it before Caddy reads the Caddyfile that imports these.
	insideScript = `I="$HOME/autobuild/frontdoor/config/inside"
mkdir -p "$I"
for n in @NETS@; do
  range=$(docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' "autobuild-net-$n" </dev/null 2>/dev/null | tr -s ' ' '\n' |
    grep -E '^[0-9]{1,3}(\.[0-9]{1,3}){3}/[0-9]{1,2}$' | head -n 1 || true)
  [ -n "$range" ] || range=255.255.255.255/32
  printf '@from_%s {\n\tremote_ip %s\n\tmethod GET\n\tpath_regexp ^/__autobuild/connect/[a-z0-9-]+/[a-z0-9-]+/[A-Za-z0-9_-]+$\n}\n' "$n" "$range" > "$I/.$n.caddy.next"
  mv "$I/.$n.caddy.next" "$I/$n.caddy"
done
`
	frontDoorScript = `set -eu
` + dockerHost + `
A="$HOME/autobuild"
` + insideScript + `state=started
if [ "$(docker inspect -f '{{.State.Running}}' autobuild-frontdoor 2>/dev/null || true)" = "true" ]; then
  state=reloaded
else
  docker rm -f autobuild-frontdoor >/dev/null 2>&1 || true
  docker pull -q ` + FrontDoorImage + ` >/dev/null
  docker run -d --name autobuild-frontdoor --restart unless-stopped --read-only --cap-drop ALL --cap-add NET_BIND_SERVICE \
    --security-opt no-new-privileges --tmpfs /tmp -p 80:80 -p 443:443 \
    -v "$A/frontdoor/config:/etc/caddy:ro" -v "$A/frontdoor/data:/data" -v "$A/frontdoor/state:/config" \
    -v "$A/sites:/srv/sites:ro" -v "$A/releases:/srv/releases:ro" \
    ` + FrontDoorImage + ` caddy run --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null
fi
# The front door joins each Node app's network, @NETS@ being their sites.
for n in @NETS@; do
  docker inspect -f '{{json .NetworkSettings.Networks}}' autobuild-frontdoor | grep -q "\"autobuild-net-$n\"" ||
    docker network connect "autobuild-net-$n" autobuild-frontdoor
done
if [ "$state" = reloaded ]; then
  docker exec autobuild-frontdoor caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
fi
echo "$state"
`
	stopFrontDoorScript = `set -eu
` + dockerHost + `
docker rm -f autobuild-frontdoor >/dev/null 2>&1 || true
echo stopped
`
	removeReleasesScript = `set -eu
cd "$HOME/autobuild/releases"
for d in @LIST@; do rm -rf "./$d"; done
`
)

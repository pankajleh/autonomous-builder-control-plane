package remotetarget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// Lifecycle on a target (Repo C design note BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md
// §11, BF-14.5a): a Node app or a WordPress website stopped and started
// without losing anything, an
// app's data removed once its site is off, a target cleaned up once nothing is
// left (each removal confirmed by repeating its name), and a read-only check
// of the target against ABCP's record. The check repairs nothing but one
// thing: a site an interrupted operation left unknown becomes live again when
// the target is unambiguous about it (its link points at a release ABCP keeps
// and, for a Node app, its container runs).

const (
	siteStopped = "stopped"
	// maxFindings bounds a drift report.
	maxFindings = 100
)

var (
	objectPattern = regexp.MustCompile(`^autobuild-[a-z0-9-]{1,60}$`)
	copyPattern   = regexp.MustCompile(`^(check|restore|previous)_(app|wp)_[a-z0-9_]{1,40}$`)
	// wpNamePattern is a WordPress website's database or a copy of it: the
	// shared MariaDB's, not the Postgres's.
	wpNamePattern = regexp.MustCompile(`^((check|restore|previous)_)?wp_[a-z0-9_]{1,40}$`)
)

// startLifecycle records an operation on a target's site and marks the target
// busy; the caller holds m.mu and runs it.
func (m *Manager) startLifecycle(r *record, kind, site, requestID string, steps []string, siteState string) (operationRecord, error) {
	op := serviceapi.OperationV1{
		SchemaVersion: serviceapi.OperationSchemaV1, OperationID: newID("op-"), Target: r.Key, Kind: kind, Site: site,
		RequestID: requestID, State: opQueued, StartedAt: m.stamp(),
	}
	for _, name := range steps {
		op.Steps = append(op.Steps, serviceapi.OperationStepV1{Name: name, State: "pending"})
	}
	rec := operationRecord{SchemaVersion: operationSchema, Operation: op}
	if s, ok := findSite(*r, site); ok {
		rec.Host, rec.Release, rec.Sandbox = s.Host, s.Release, s.Sandbox
		if siteState != "" {
			s.State, s.UpdatedAt, s.Operation = siteState, m.stamp(), op.OperationID
			m.setSite(r, s)
		}
	}
	if err := m.writeJSON(filepath.Join(m.dir, "operations"), op.OperationID, rec); err != nil {
		return operationRecord{}, err
	}
	if siteState != "" {
		if err := m.write(*r); err != nil {
			return operationRecord{}, err
		}
	}
	m.busy[r.Key] = true
	m.wg.Add(1)
	return rec, nil
}

// lifecycleTarget reads a confirmed, idle target for an operation, or answers
// the operation an earlier identical request started.
func (m *Manager) lifecycleTarget(key, kind, site, requestID string) (record, *serviceapi.OperationV1, error) {
	r, err := m.read(key)
	if err != nil {
		return record{}, nil, err
	}
	if existing, ok := m.operationByRequest(key, requestID); ok {
		if existing.Operation.Kind != kind || existing.Operation.Site != site {
			return record{}, nil, serviceapi.ErrTargetInvalid
		}
		return r, &existing.Operation, nil
	}
	if r.State != stateConfirmed {
		return record{}, nil, serviceapi.ErrTargetNotReady
	}
	if m.busy[key] {
		return record{}, nil, serviceapi.ErrTargetBusy
	}
	return r, nil, nil
}

// StopSite stops a Node app: its container goes, the front door answers its
// host with a paused 503, and its data, network and release stay.
func (m *Manager) StopSite(_ context.Context, key, site string, request serviceapi.OperationRequestV1) (serviceapi.OperationV1, error) {
	return m.startStop(key, site, request, "stop")
}

// StartSite starts a stopped Node app again, waits for /health and puts its
// proxy back; an app that never answers is stopped again.
func (m *Manager) StartSite(_ context.Context, key, site string, request serviceapi.OperationRequestV1) (serviceapi.OperationV1, error) {
	return m.startStop(key, site, request, "start")
}

func (m *Manager) startStop(key, site string, request serviceapi.OperationRequestV1, kind string) (serviceapi.OperationV1, error) {
	if !siteNamePattern.MatchString(site) || !requestIDPattern.MatchString(request.RequestID) {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, existing, err := m.lifecycleTarget(key, kind, site, request.RequestID)
	if err != nil || existing != nil {
		return deref(existing), err
	}
	s, ok := findSite(r, site)
	if !ok || !appKind(siteKind(s)) || !served(s) {
		return serviceapi.OperationV1{}, serviceapi.ErrSiteUnknown
	}
	steps, state := []string{"stop_app", "write_site", "reload_frontdoor"}, siteStopped
	if kind == "start" {
		if s.State != siteStopped {
			return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
		}
		steps, state = []string{"start_app", "wait_health", "write_site", "reload_frontdoor", "check"}, siteDeploying
	} else if s.State == siteStopped {
		return serviceapi.OperationV1{}, serviceapi.ErrSiteStopped
	}
	rec, err := m.startLifecycle(&r, kind, site, request.RequestID, steps, state)
	if err != nil {
		return serviceapi.OperationV1{}, err
	}
	go m.run(rec)
	return rec.Operation, nil
}

func deref(op *serviceapi.OperationV1) serviceapi.OperationV1 {
	if op == nil {
		return serviceapi.OperationV1{}
	}
	return *op
}

func (m *Manager) runStop(ctx context.Context, rec *operationRecord, r record, session remote) string {
	site := rec.Operation.Site
	steps := []struct {
		name string
		work func() (string, error)
	}{
		{"stop_app", func() (string, error) { return run(ctx, session, fill(stopAppOnlyScript, "N", site), nil) }},
		{"write_site", func() (string, error) {
			return run(ctx, session, m.withNetworks(writeSiteScript, r.Key), strings.NewReader(m.caddyfile(r.Key, site, rec.Host)))
		}},
		{"reload_frontdoor", func() (string, error) {
			return run(ctx, session, m.withNetworks(frontDoorScript, r.Key), nil)
		}},
	}
	for _, s := range steps {
		if err := m.step(rec, s.name, s.work); err != nil {
			return stepDetail(err)
		}
	}
	return ""
}

func (m *Manager) runStart(ctx context.Context, rec *operationRecord, r record, session remote) string {
	site := rec.Operation.Site
	pauseAgain := func() string {
		_, _ = run(ctx, session, fill(stopAppOnlyScript, "N", site), nil)
		m.mu.Lock()
		if cur, err := m.read(r.Key); err == nil {
			if s, ok := findSite(cur, site); ok {
				s.State = siteStopped
				m.setSite(&cur, s)
				_ = m.write(cur)
			}
		}
		m.mu.Unlock()
		if _, err := run(ctx, session, m.withNetworks(writeSiteScript, r.Key), strings.NewReader(m.caddyfile(r.Key, site, rec.Host))); err != nil {
			return "the app is stopped again"
		}
		_, _ = run(ctx, session, m.withNetworks(frontDoorScript, r.Key), nil)
		rec.Paused = true
		return "the app is stopped again"
	}
	kind := kindNode
	if wpData(r, site) {
		kind = kindWP
	}
	steps := []struct {
		name string
		work func() (string, error)
	}{
		{"start_app", func() (string, error) { return m.startApp(ctx, session, r.Key, site, rec.Sandbox) }},
		{"wait_health", func() (string, error) {
			out, err := run(ctx, session, fill(waitHealthScript, "N", site, "WAIT", m.waitFor(kind)), nil)
			if err != nil {
				return "", errStep(stepDetail(err) + "; " + pauseAgain())
			}
			return out, nil
		}},
		{"write_site", func() (string, error) {
			return run(ctx, session, m.withNetworks(writeSiteScript, r.Key), strings.NewReader(m.caddyfile(r.Key, site, rec.Host)))
		}},
		{"reload_frontdoor", func() (string, error) {
			return run(ctx, session, m.withNetworks(frontDoorScript, r.Key), nil)
		}},
		{"check", func() (string, error) {
			return m.checkSite(ctx, r, rec.Host, releaseRecord{Release: serviceapi.ReleaseV1{Kind: kind}})
		}},
	}
	for _, s := range steps {
		if err := m.step(rec, s.name, s.work); err != nil {
			return stepDetail(err)
		}
	}
	return ""
}

// Databases lists a target's app databases and whether each one's site is
// still on the target.
func (m *Manager) Databases(_ context.Context, key string) (serviceapi.DatabasesV1, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return serviceapi.DatabasesV1{}, err
	}
	out := serviceapi.DatabasesV1{SchemaVersion: serviceapi.DatabasesSchemaV1, Target: key, Databases: []serviceapi.DatabaseV1{}}
	for _, d := range r.Databases {
		_, on := findSite(r, d.Site)
		out.Databases = append(out.Databases, serviceapi.DatabaseV1{Site: d.Site, Database: d.Name, CreatedAt: d.CreatedAt, Backups: len(d.Backups), OnServer: on})
	}
	return out, nil
}

// RemoveData removes an app's database, its login, database.env and its
// backups for good, once its site is off the target and Confirm repeats the
// database's name; a WordPress website's volume, its uploads, goes too.
func (m *Manager) RemoveData(_ context.Context, key, site string, request serviceapi.ConfirmedRequestV1) (serviceapi.OperationV1, error) {
	if !siteNamePattern.MatchString(site) || !requestIDPattern.MatchString(request.RequestID) {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, existing, err := m.lifecycleTarget(key, "remove_data", site, request.RequestID)
	if err != nil || existing != nil {
		return deref(existing), err
	}
	d, ok := findDatabase(r, site)
	if !ok {
		return serviceapi.OperationV1{}, serviceapi.ErrSiteUnknown
	}
	wp := wpDatabasePattern.MatchString(d.Name) && d.Name == wpDatabaseName(site)
	if request.Confirm != d.Name || !(databasePattern.MatchString(d.Name) || wp) {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	if _, on := findSite(r, site); on {
		return serviceapi.OperationV1{}, serviceapi.ErrDataInUse
	}
	steps := []string{"start_database", "drop_database", "remove_backups"}
	if wp {
		steps = []string{"start_database", "drop_database", "remove_volume", "remove_backups"}
	}
	rec, err := m.startLifecycle(&r, "remove_data", site, request.RequestID, steps, "")
	if err != nil {
		return serviceapi.OperationV1{}, err
	}
	go m.run(rec)
	return rec.Operation, nil
}

func (m *Manager) runRemoveData(ctx context.Context, rec *operationRecord, r record, session remote) string {
	site := rec.Operation.Site
	db := databaseName(site)
	start, drop := startDatabaseScript, dropDataScript
	if wpData(r, site) {
		db, start, drop = wpDatabaseName(site), m.withNetworks(wpStartDatabaseScript, r.Key), wpDropDataScript
	}
	steps := map[string]func() (string, error){
		"start_database": func() (string, error) { return run(ctx, session, start, nil) },
		"drop_database":  func() (string, error) { return run(ctx, session, fill(drop, "N", site, "DB", db), nil) },
		"remove_volume":  func() (string, error) { return run(ctx, session, fill(wpRemoveVolumeScript, "N", site), nil) },
		"remove_backups": func() (string, error) { return run(ctx, session, fill(removeSiteBackupsScript, "N", site), nil) },
	}
	for _, s := range rec.Operation.Steps {
		work, ok := steps[s.Name]
		if !ok {
			continue
		}
		if err := m.step(rec, s.Name, work); err != nil {
			return stepDetail(err)
		}
	}
	m.mu.Lock()
	if cur, err := m.read(r.Key); err == nil {
		kept := cur.Databases[:0]
		for _, d := range cur.Databases {
			if d.Site != site {
				kept = append(kept, d)
			}
		}
		cur.Databases = kept
		_ = m.write(cur)
	}
	m.mu.Unlock()
	return ""
}

// CleanTarget removes everything ABCP put on a target once no site and no
// app's data is left and Confirm repeats the target's key: the front door,
// the shared Postgres and MariaDB and their volumes, the networks, the pinned
// images and ~/autobuild. Nothing else on the target is touched.
func (m *Manager) CleanTarget(_ context.Context, key string, request serviceapi.ConfirmedRequestV1) (serviceapi.OperationV1, error) {
	if !requestIDPattern.MatchString(request.RequestID) {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, existing, err := m.lifecycleTarget(key, "clean", "", request.RequestID)
	if err != nil || existing != nil {
		return deref(existing), err
	}
	if request.Confirm != key {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	if len(r.Sites) > 0 || len(r.Databases) > 0 {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInUse
	}
	rec, err := m.startLifecycle(&r, "clean", "", request.RequestID,
		[]string{"stop_frontdoor", "stop_database", "remove_volume", "remove_networks", "remove_images", "remove_files"}, "")
	if err != nil {
		return serviceapi.OperationV1{}, err
	}
	go m.run(rec)
	return rec.Operation, nil
}

func (m *Manager) runClean(ctx context.Context, rec *operationRecord, r record, session remote) string {
	scripts := map[string]string{"stop_frontdoor": stopFrontDoorScript, "stop_database": stopDatabaseScript, "remove_volume": removeVolumeScript,
		"remove_networks": removeNetworksScript, "remove_images": removeImagesScript, "remove_files": removeFilesScript}
	for _, s := range rec.Operation.Steps {
		script := scripts[s.Name]
		if err := m.step(rec, s.Name, func() (string, error) { return run(ctx, session, script, nil) }); err != nil {
			return stepDetail(err)
		}
	}
	m.mu.Lock()
	if cur, err := m.read(r.Key); err == nil {
		cur.Releases = nil
		_ = m.write(cur)
	}
	m.mu.Unlock()
	return ""
}

// CheckDrift reads a target, read-only, and compares it with ABCP's record,
// as an operation; the report is kept for Drift.
func (m *Manager) CheckDrift(_ context.Context, key string, request serviceapi.OperationRequestV1) (serviceapi.OperationV1, error) {
	if !requestIDPattern.MatchString(request.RequestID) {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, existing, err := m.lifecycleTarget(key, "drift", "", request.RequestID)
	if err != nil || existing != nil {
		return deref(existing), err
	}
	rec, err := m.startLifecycle(&r, "drift", "", request.RequestID, []string{"read_server"}, "")
	if err != nil {
		return serviceapi.OperationV1{}, err
	}
	go m.run(rec)
	return rec.Operation, nil
}

// Drift answers a target's last check.
func (m *Manager) Drift(_ context.Context, key string) (serviceapi.DriftV1, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return serviceapi.DriftV1{}, err
	}
	if r.Drift == nil {
		return serviceapi.DriftV1{SchemaVersion: serviceapi.DriftSchemaV1, Target: key, Findings: []serviceapi.DriftFindingV1{}, Reconciled: []string{}}, nil
	}
	return *r.Drift, nil
}

func (m *Manager) runDrift(ctx context.Context, rec *operationRecord, r record, session remote) string {
	if err := m.step(rec, "read_server", func() (string, error) { return m.readServer(ctx, session, r.Key) }); err != nil {
		return stepDetail(err)
	}
	return ""
}

// readServer reads what a target holds, compares it with ABCP's record,
// reconciles the sites it can and keeps the report. It answers the step's
// detail.
func (m *Manager) readServer(ctx context.Context, session remote, key string) (string, error) {
	out, err := run(ctx, session, readServerScript, nil)
	if err != nil {
		return "", err
	}
	seen := parseServer(out)
	if !seen.complete {
		return "", errStep("the server's state could not be read")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return "", errStep("target_unreadable")
	}
	report := m.compare(&r, seen)
	r.Drift = &report
	if err := m.write(r); err != nil {
		return "", errStep("target_unreadable")
	}
	switch {
	case report.OK && len(report.Reconciled) == 0:
		return "as recorded", nil
	case report.OK:
		return fmt.Sprintf("as recorded; %d reconciled", len(report.Reconciled)), nil
	default:
		return fmt.Sprintf("%d differences", len(report.Findings)), nil
	}
}

// serverState is what the read-only script found on a target.
type serverState struct {
	complete   bool
	containers map[string]bool // name: running
	networks   map[string]bool
	volumes    map[string]bool
	links      map[string]string // site: target
	releases   map[string]bool
	backups    map[string]map[string]bool // site: backup ids
	databases  map[string]bool
	postgres   bool
	mariadb    bool
	caddyfile  string
}

func parseServer(out string) serverState {
	s := serverState{containers: map[string]bool{}, networks: map[string]bool{}, volumes: map[string]bool{}, links: map[string]string{},
		releases: map[string]bool{}, backups: map[string]map[string]bool{}, databases: map[string]bool{}}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 3 && f[0] == "container" && objectPattern.MatchString(f[1]):
			s.containers[f[1]] = f[2] == "running"
		case len(f) == 2 && f[0] == "network" && objectPattern.MatchString(f[1]):
			s.networks[f[1]] = true
		case len(f) == 2 && f[0] == "volume" && objectPattern.MatchString(f[1]):
			s.volumes[f[1]] = true
		case len(f) == 3 && f[0] == "link" && siteNamePattern.MatchString(f[1]) && len(f[2]) <= 200:
			s.links[f[1]] = f[2]
		case len(f) == 2 && f[0] == "release" && digestPattern.MatchString(f[1]):
			s.releases[f[1]] = true
		case len(f) == 3 && f[0] == "backup" && siteNamePattern.MatchString(f[1]) && backupIDPattern.MatchString(f[2]):
			if s.backups[f[1]] == nil {
				s.backups[f[1]] = map[string]bool{}
			}
			s.backups[f[1]][f[2]] = true
		case len(f) == 2 && f[0] == "database" && (databasePattern.MatchString(f[1]) || wpDatabasePattern.MatchString(f[1]) || copyPattern.MatchString(f[1])):
			s.databases[f[1]] = true
		case len(f) == 1 && f[0] == "postgres":
			s.postgres = true
		case len(f) == 1 && f[0] == "mariadb":
			s.mariadb = true
		case len(f) == 2 && f[0] == "caddyfile" && digestPattern.MatchString(f[1]):
			s.caddyfile = f[1]
		case len(f) == 1 && f[0] == "done":
			s.complete = true
		}
	}
	return s
}

// compare finds how a target differs from its record and reconciles the
// sites left unknown that the target is unambiguous about; the caller holds
// m.mu and writes r.
func (m *Manager) compare(r *record, seen serverState) serviceapi.DriftV1 {
	report := serviceapi.DriftV1{SchemaVersion: serviceapi.DriftSchemaV1, Target: r.Key, Checked: true, CheckedAt: m.stamp(),
		Findings: []serviceapi.DriftFindingV1{}, Reconciled: []string{}}
	add := func(kind, site, detail string) {
		if len(report.Findings) < maxFindings {
			report.Findings = append(report.Findings, serviceapi.DriftFindingV1{Kind: kind, Site: site, Detail: detail})
		}
	}
	kept := map[string]bool{}
	for _, d := range r.Releases {
		kept[d] = true
	}
	// Reconcile first: a site an interrupted operation left unknown takes the
	// release its link points at when ABCP keeps that release and, for a Node
	// app or a WordPress website, its container runs.
	for _, s := range r.Sites {
		if s.State != siteUnknown {
			continue
		}
		digest, ok := strings.CutPrefix(seen.links[s.Name], "../releases/")
		running := seen.containers["autobuild-app-"+s.Name]
		if ok && digestPattern.MatchString(digest) && kept[digest] && seen.releases[digest] && (!appKind(siteKind(s)) || running) {
			s.Release, s.State, s.UpdatedAt = digest, siteLive, m.stamp()
			m.setSite(r, s)
			report.Reconciled = append(report.Reconciled, s.Name)
			continue
		}
		add("site_unknown", s.Name, "left unknown by an interrupted operation")
	}
	expected := map[string]bool{} // containers that should run
	nets, vols := map[string]bool{}, map[string]bool{}
	postgres, mariadb := false, false
	for _, d := range r.Databases {
		if wpDatabasePattern.MatchString(d.Name) {
			// A WordPress website's volume, its uploads, is kept with its data.
			mariadb = true
			vols["autobuild-wp-"+d.Site] = true
		} else {
			postgres = true
		}
	}
	anyServed := false
	for _, s := range r.Sites {
		if !served(s) {
			continue
		}
		anyServed = true
		if appKind(siteKind(s)) {
			if siteKind(s) == kindWP {
				mariadb = true
				vols["autobuild-wp-"+s.Name] = true
			} else {
				postgres = true
			}
			nets["autobuild-net-"+s.Name] = true
			if s.State != siteStopped {
				expected["autobuild-app-"+s.Name] = true
			}
		}
		if s.State == siteUnknown {
			continue
		}
		want := "../releases/" + s.Release
		switch got, ok := seen.links[s.Name]; {
		case !ok:
			add("link_missing", s.Name, "")
		case got != want:
			add("link_wrong", s.Name, "")
		}
	}
	if anyServed {
		expected["autobuild-frontdoor"] = true
	}
	if postgres {
		expected["autobuild-postgres"] = true
		nets["autobuild-db"] = true
		vols["autobuild-pgdata"] = true
	}
	if mariadb {
		expected["autobuild-mariadb"] = true
		nets["autobuild-db"] = true
		vols["autobuild-mariadb-data"] = true
	}
	names := func(set map[string]bool) []string {
		out := make([]string, 0, len(set))
		for n := range set {
			out = append(out, n)
		}
		sort.Strings(out)
		return out
	}
	siteOf := func(name string) string {
		for _, prefix := range []string{"autobuild-app-", "autobuild-net-", "autobuild-wp-"} {
			if s, ok := strings.CutPrefix(name, prefix); ok {
				return s
			}
		}
		return ""
	}
	for _, c := range names(expected) {
		running, there := seen.containers[c]
		switch {
		case !there:
			add("container_missing", siteOf(c), c)
		case !running:
			add("container_stopped", siteOf(c), c)
		}
	}
	for _, c := range names(seen.containers) {
		if !expected[c] {
			add("container_unknown", siteOf(c), c)
		}
	}
	for _, n := range names(nets) {
		if !seen.networks[n] {
			add("network_missing", siteOf(n), n)
		}
	}
	for _, n := range names(seen.networks) {
		if !nets[n] {
			add("network_unknown", siteOf(n), n)
		}
	}
	for _, v := range names(vols) {
		if !seen.volumes[v] {
			add("volume_missing", siteOf(v), v)
		}
	}
	for _, v := range names(seen.volumes) {
		if !vols[v] {
			add("volume_unknown", siteOf(v), v)
		}
	}
	for _, site := range sortedKeys(seen.links) {
		if s, ok := findSite(*r, site); !ok || !served(s) {
			add("link_unknown", site, "")
		}
	}
	for _, d := range r.Releases {
		if !seen.releases[d] {
			add("release_missing", "", d[:12])
		}
	}
	for _, d := range names(seen.releases) {
		if !kept[d] {
			add("release_unknown", "", d[:12])
		}
	}
	// Each database server's databases are compared when it was read: the
	// Postgres's app_ ones, the MariaDB's wp_ ones.
	listed := func(name string) bool {
		if wpNamePattern.MatchString(name) {
			return seen.mariadb
		}
		return seen.postgres
	}
	recorded := map[string]bool{}
	for _, d := range r.Databases {
		recorded[d.Name] = true
		if listed(d.Name) && !seen.databases[d.Name] {
			add("database_missing", d.Site, d.Name)
		}
	}
	for _, d := range names(seen.databases) {
		switch {
		case copyPattern.MatchString(d):
			add("copy_left", "", d)
		case !recorded[d]:
			add("database_unknown", "", d)
		}
	}
	for _, d := range r.Databases {
		for _, b := range d.Backups {
			if !seen.backups[d.Site][b.BackupID] {
				add("backup_missing", d.Site, b.BackupID)
			}
		}
	}
	for _, site := range sortedKeys(seen.backups) {
		d, _ := findDatabase(*r, site)
		for _, id := range names(seen.backups[site]) {
			if _, ok := findBackup(d, id); !ok {
				add("backup_unknown", site, id)
			}
		}
	}
	// The front door's Caddyfile is the one ABCP would write now (a target
	// whose last site went keeps an empty one, or none).
	if anyServed {
		sum := sha256.Sum256([]byte(caddyfileFor(*r, "", "", m.relayHost)))
		if seen.caddyfile != hex.EncodeToString(sum[:]) {
			add("frontdoor_config", "", "")
		}
	}
	report.OK = len(report.Findings) == 0
	return report
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The lifecycle scripts. Each value in them (@N@ a site name, @DB@ its
// database) is checked by ABCP before it is put in; the rest is fixed.
const (
	// dropDataScript drops an app's database (and any copy a restore or check
	// left), its login, database.env and its setup token.
	dropDataScript = `set -eu
` + dockerHost + `
docker exec -i autobuild-postgres ` + psql + ` -v ON_ERROR_STOP=1 -qAtX >/dev/null <<'SQL'
DROP DATABASE IF EXISTS @DB@ WITH (FORCE);
DROP DATABASE IF EXISTS check_@DB@ WITH (FORCE);
DROP DATABASE IF EXISTS restore_@DB@ WITH (FORCE);
DROP DATABASE IF EXISTS previous_@DB@ WITH (FORCE);
DROP ROLE IF EXISTS @DB@;
SQL
rm -f "$HOME/autobuild/apps/@N@/database.env" "$HOME/autobuild/apps/@N@/setup.env"
rmdir "$HOME/autobuild/apps/@N@" 2>/dev/null || true
echo "its database and login are dropped"
`
	removeSiteBackupsScript = `set -eu
B="$HOME/autobuild/backups"
if [ -d "$B/@N@" ]; then n=$(find "$B/@N@" -maxdepth 1 -name '*.dump' | wc -l); rm -rf "$B/@N@"; echo "$n backups removed"; else echo "no backups"; fi
`
	stopDatabaseScript = `set -eu
` + dockerHost + `
docker rm -f autobuild-postgres >/dev/null 2>&1 || true
docker rm -f autobuild-mariadb >/dev/null 2>&1 || true
echo stopped
`
	// removeVolumeScript removes the shared databases' volumes: the Postgres's
	// and the MariaDB's.
	removeVolumeScript = `set -eu
` + dockerHost + `
said="not there"
for v in autobuild-pgdata autobuild-mariadb-data; do
  if docker volume inspect "$v" >/dev/null 2>&1; then docker volume rm "$v" >/dev/null; said=removed; fi
done
echo "$said"
`
	removeNetworksScript = `set -eu
` + dockerHost + `
n=0
for net in $(docker network ls --filter label=autobuild.site --format '{{.Name}}') autobuild-db; do
  case "$net" in autobuild-*) ;; *) continue ;; esac
  if docker network inspect "$net" >/dev/null 2>&1; then docker network rm "$net" >/dev/null; n=$((n+1)); fi
done
echo "$n removed"
`
	removeImagesScript = `set -eu
` + dockerHost + `
for image in ` + FrontDoorImage + ` ` + NodeRuntimeImage + ` ` + NodePostgresImage + ` ` + WordPressServerImage + ` ` + WordPressMariaDBImage + `; do
  docker image rm "$image" >/dev/null 2>&1 || true
done
echo removed
`
	removeFilesScript = `set -eu
case "$HOME" in /*/*) ;; *) echo "the deploy login's home is not a folder of its own" >&2; exit 3 ;; esac
rm -rf "$HOME/autobuild"
echo removed
`
	// readServerScript lists, read-only, what ABCP may have put on a target.
	readServerScript = `set -eu
` + dockerHost + `
A="$HOME/autobuild"
docker ps -a --filter name=autobuild- --format 'container {{.Names}} {{.State}}'
docker network ls --filter name=autobuild- --format 'network {{.Name}}'
docker volume ls --filter name=autobuild- --format 'volume {{.Name}}'
if [ -d "$A/sites" ]; then for l in "$A/sites"/*; do if [ -L "$l" ]; then echo "link ${l##*/} $(readlink "$l")"; fi; done; fi
if [ -d "$A/releases" ]; then for d in "$A/releases"/*; do if [ -d "$d" ]; then echo "release ${d##*/}"; fi; done; fi
if [ -d "$A/backups" ]; then for f in "$A/backups"/*/*.dump; do if [ -f "$f" ]; then s=${f%/*}; b=${f##*/}; echo "backup ${s##*/} ${b%.dump}"; fi; done; fi
if [ "$(docker inspect -f '{{.State.Running}}' autobuild-postgres 2>/dev/null || true)" = "true" ]; then
  echo postgres
  docker exec autobuild-postgres ` + psql + ` -qAtX -c "SELECT datname FROM pg_database WHERE datname ~ '^(check_|restore_|previous_)?app_'" | sed 's/^/database /'
fi
if [ "$(docker inspect -f '{{.State.Running}}' autobuild-mariadb 2>/dev/null || true)" = "true" ]; then
  echo mariadb
  printf 'SHOW DATABASES;\n' | ` + mariadbAdmin + ` --skip-column-names | grep -E '^((check|restore|previous)_)?wp_[a-z0-9_]+$' | sed 's/^/database /'
fi
if [ -f "$A/frontdoor/config/Caddyfile" ]; then echo "caddyfile $(sha256sum < "$A/frontdoor/config/Caddyfile" | cut -d' ' -f1)"; fi
echo done
`
)

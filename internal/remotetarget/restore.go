package remotetarget

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// Restoring a Node app's backup, checking that a backup restores, and the
// nightly backups (Repo C design note BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md §11,
// BF-14.2b-1, second part).
//
// A backup is checked by restoring it, as the app's login, into a database of
// its own (check_app_<site>) and comparing every table's rows with what the
// database held when the backup was taken, counted in the dump's snapshot;
// the copy is then dropped. A restore is a plan an administrator approves: it
// backs the app up first, restores the backup into restore_app_<site> and
// checks it the same way while the app keeps running, then stops the app,
// swaps the two databases in one transaction (the one it replaces is kept as
// previous_app_<site>), starts the app and waits for /health. When the app
// never answers, the databases are swapped back and the app started again;
// once it answers, previous_app_<site> is dropped. None of these names starts
// with app_, so none can be another app's database. Every night at 02:30
// India time ABCP backs up and checks each Node app a target serves, in one
// SSH session per target; a target that does not answer is tried again the
// next night, not in a loop (Repo C NOTIFICATION_POLLING_BUDGET.md).

const (
	planRestore = "restore"

	nightlyHour   = 2
	nightlyMinute = 30
	// nightlyRetry is how long a target busy at 02:30 waits for its one more
	// try that night.
	nightlyRetry = 15 * time.Minute
)

var india = time.FixedZone("IST", 5*3600+30*60)

func restoreSteps() []string {
	return []string{"back_up", "restore_copy", "stop_app", "switch_database", "start_app", "wait_health", "check", "clean_up"}
}

// createRestorePlan says what restoring one of a Node app's backups, or a
// WordPress website's, would do. Nothing on the target changes.
func (m *Manager) createRestorePlan(key string, create serviceapi.PlanCreateV1) (serviceapi.PlanV1, error) {
	want := *create.Restore
	if create.Release != "" || create.Site != (serviceapi.PlanSiteV1{}) || create.AdminEmail != "" || !siteNamePattern.MatchString(want.Site) || !backupIDPattern.MatchString(want.Backup) {
		return serviceapi.PlanV1{}, serviceapi.ErrTargetInvalid
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
	site, ok := findSite(r, want.Site)
	d, kept := findDatabase(r, want.Site)
	if !ok || !kept || !appKind(siteKind(site)) || !served(site) || d.Name != dataName(siteKind(site), site.Name) {
		return serviceapi.PlanV1{}, serviceapi.ErrSiteUnknown
	}
	if site.State == siteStopped {
		// A restore starts the app; a stopped app is started first (BF-14.5a).
		return serviceapi.PlanV1{}, serviceapi.ErrSiteStopped
	}
	backup, ok := findBackup(d, want.Backup)
	if !ok || backup.Tables == nil {
		// A backup restores only when its rows were counted, to be compared.
		return serviceapi.PlanV1{}, serviceapi.ErrBackupUnknown
	}
	release, err := m.readRelease(site.Release)
	if err != nil {
		return serviceapi.PlanV1{}, err
	}
	box := site.Sandbox
	if box == "" {
		box = sandbox(r)
	}
	now := m.now().UTC()
	shown := backup.BackupV1
	plan := serviceapi.PlanV1{
		SchemaVersion: serviceapi.PlanSchemaV1, PlanID: newID("pln-"), Target: key, Kind: planRestore, Release: release.Release,
		Site: serviceapi.PlanSiteV1{Name: site.Name, Host: site.Host}, Steps: restoreSteps(),
		Images: []string{NodeRuntimeImage, NodePostgresImage}, Database: d.Name, Sandbox: box, Backup: &shown,
		CreatedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(planLifetime).Format(time.RFC3339),
	}
	if siteKind(site) == kindWP {
		// A WordPress website's uploads go back with its database.
		plan.Steps, plan.Images = wpRestoreSteps(), []string{WordPressServerImage, WordPressMariaDBImage}
	}
	plan.Hash = planHash(plan)
	if err := m.writeJSON(filepath.Join(m.dir, "plans"), plan.PlanID, planRecord{SchemaVersion: planSchema, Plan: plan}); err != nil {
		return serviceapi.PlanV1{}, err
	}
	m.prunePlans(now)
	return plan, nil
}

// restoreStale is true when a restore plan no longer fits its target: the app
// is gone or serves another release, or the backup is no longer kept.
func restoreStale(r record, plan serviceapi.PlanV1) bool {
	site, ok := findSite(r, plan.Site.Name)
	d, kept := findDatabase(r, plan.Site.Name)
	if !ok || !kept || !appKind(siteKind(site)) || !served(site) || site.State == siteStopped || site.Release != plan.Release.Digest || plan.Backup == nil {
		return true
	}
	_, ok = findBackup(d, plan.Backup.BackupID)
	return !ok
}

// CheckBackup restores a backup into a database of its own and compares its
// rows, as an operation. The same request answers the same operation.
func (m *Manager) CheckBackup(_ context.Context, key, site, backup string, create serviceapi.BackupCreateV1) (serviceapi.OperationV1, error) {
	if !siteNamePattern.MatchString(site) || !backupIDPattern.MatchString(backup) || !requestIDPattern.MatchString(create.RequestID) {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return serviceapi.OperationV1{}, err
	}
	if existing, ok := m.operationByRequest(key, create.RequestID); ok {
		if existing.Operation.Kind != "check_backup" || existing.Operation.Site != site || existing.Backup != backup {
			return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
		}
		return existing.Operation, nil
	}
	d, ok := findDatabase(r, site)
	if !ok {
		return serviceapi.OperationV1{}, serviceapi.ErrSiteUnknown
	}
	if b, ok := findBackup(d, backup); !ok || b.Tables == nil {
		return serviceapi.OperationV1{}, serviceapi.ErrBackupUnknown
	}
	if r.State != stateConfirmed {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetNotReady
	}
	if m.busy[key] {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetBusy
	}
	op := serviceapi.OperationV1{
		SchemaVersion: serviceapi.OperationSchemaV1, OperationID: newID("op-"), Target: key, Kind: "check_backup", Site: site,
		RequestID: create.RequestID, State: opQueued, StartedAt: m.stamp(),
		Steps: []serviceapi.OperationStepV1{{Name: "check_restore", State: "pending"}},
	}
	rec := operationRecord{SchemaVersion: operationSchema, Operation: op, Backup: backup}
	if err := m.writeJSON(filepath.Join(m.dir, "operations"), op.OperationID, rec); err != nil {
		return serviceapi.OperationV1{}, err
	}
	m.busy[key] = true
	m.wg.Add(1)
	go m.run(rec)
	return op, nil
}

func (m *Manager) runCheckBackup(ctx context.Context, rec *operationRecord, r record, session remote) string {
	site := rec.Operation.Site
	d, _ := findDatabase(r, site)
	b, ok := findBackup(d, rec.Backup)
	if !ok {
		m.markFirstPending(rec, "failed", "the backup is no longer kept")
		return "the backup is no longer kept"
	}
	if err := m.step(rec, "check_restore", func() (string, error) {
		return m.restoreCopy(ctx, session, r.Key, site, b, "check_", false)
	}); err != nil {
		return stepDetail(err)
	}
	return ""
}

// restoreCopy restores a backup into a copy of the app's database named with
// prefix (check_ or restore_), as the app's login, and compares every table's
// rows with what the database held when the backup was taken; it records the
// check on the backup. The copy is dropped unless keep, and always when the
// rows differ. A WordPress website's copy is restored into the shared MariaDB,
// where no login reaches it, once its uploads' tar is found readable.
func (m *Manager) restoreCopy(ctx context.Context, session remote, key, site string, b backupRecord, prefix string, keep bool) (string, error) {
	m.mu.Lock()
	r, err := m.read(key)
	m.mu.Unlock()
	wp := err == nil && wpData(r, site)
	db, script, drop := databaseName(site), restoreCopyScript, dropDatabaseScript
	if wp {
		db, script, drop = wpDatabaseName(site), wpRestoreCopyScript, wpDropCopyScript
	}
	copy := prefix + db
	if !(databasePattern.MatchString(db) || wpDatabasePattern.MatchString(db)) || (prefix != "check_" && prefix != "restore_") ||
		!backupIDPattern.MatchString(b.BackupID) || b.Tables == nil {
		return "", errStep("the backup's rows were not counted")
	}
	mode := "drop"
	if keep {
		mode = "keep"
	}
	out, err := run(ctx, session, fill(script, "N", site, "DB", db, "ID", b.BackupID, "COPY", copy, "KEEP", mode), nil)
	if err != nil {
		m.recordBackupCheck(key, site, b.BackupID, false, stepDetail(err))
		if keep {
			_, _ = run(ctx, session, fill(drop, "COPY", copy), nil)
		}
		return "", err
	}
	got, err := parseCounts(out)
	if err != nil {
		m.recordBackupCheck(key, site, b.BackupID, false, stepDetail(err))
		if keep {
			_, _ = run(ctx, session, fill(drop, "COPY", copy), nil)
		}
		return "", err
	}
	ok, detail := compareCounts(b.Counts, got)
	m.recordBackupCheck(key, site, b.BackupID, ok, detail)
	if !ok {
		if keep {
			_, _ = run(ctx, session, fill(drop, "COPY", copy), nil)
		}
		return "", errStep(detail)
	}
	return detail, nil
}

// compareCounts says whether a restore holds the rows the backup was taken
// with, naming up to three tables that differ.
func compareCounts(want, got map[string]int64) (bool, string) {
	names := map[string]bool{}
	for name := range want {
		names[name] = true
	}
	for name := range got {
		names[name] = true
	}
	var differ []string
	for name := range names {
		w, inWant := want[name]
		g, inGot := got[name]
		if inWant != inGot || w != g {
			differ = append(differ, name)
		}
	}
	sort.Strings(differ)
	tables, rows := countTotals(want)
	if len(differ) == 0 {
		return true, fmt.Sprintf("%d tables, %d rows, as when it was taken", tables, rows)
	}
	var parts []string
	for i, name := range differ {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("%d more", len(differ)-3))
			break
		}
		w, inWant := want[name]
		g, inGot := got[name]
		switch {
		case !inWant:
			parts = append(parts, fmt.Sprintf("%s: not in the backup, %d restored", name, g))
		case !inGot:
			parts = append(parts, fmt.Sprintf("%s: %d taken, not restored", name, w))
		default:
			parts = append(parts, fmt.Sprintf("%s: %d taken, %d restored", name, w, g))
		}
	}
	return false, "the rows differ: " + strings.Join(parts, "; ")
}

func (m *Manager) recordBackupCheck(key, site, id string, ok bool, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return
	}
	d, found := findDatabase(r, site)
	if !found {
		return
	}
	for i := range d.Backups {
		if d.Backups[i].BackupID == id {
			d.Backups[i].Check = &serviceapi.BackupCheckV1{At: m.stamp(), OK: ok, Detail: stepDetail(errStep(detail))}
		}
	}
	setDatabase(&r, d)
	_ = m.write(r)
}

func (m *Manager) runRestore(ctx context.Context, rec *operationRecord, r record, session remote) string {
	site := rec.Operation.Site
	d, _ := findDatabase(r, site)
	b, ok := findBackup(d, rec.Backup)
	if !ok {
		m.markFirstPending(rec, "failed", "the backup is no longer kept")
		return "the backup is no longer kept"
	}
	if wpData(r, site) {
		return m.runRestoreWebsite(ctx, rec, r, session, b)
	}
	db := databaseName(site)
	start := func() (string, error) { return m.startApp(ctx, session, r.Key, site, rec.Sandbox) }
	for _, s := range rec.Operation.Steps {
		var err error
		switch s.Name {
		case "back_up":
			err = m.step(rec, s.Name, func() (string, error) {
				_, detail, err := m.backUp(ctx, session, r.Key, site, reasonBeforeRestore, rec.Release, b.BackupID)
				return detail, err
			})
		case "restore_copy":
			err = m.step(rec, s.Name, func() (string, error) { return m.restoreCopy(ctx, session, r.Key, site, b, "restore_", true) })
		case "stop_app":
			err = m.step(rec, s.Name, func() (string, error) { return run(ctx, session, fill(stopAppOnlyScript, "N", site), nil) })
		case "switch_database":
			err = m.step(rec, s.Name, func() (string, error) {
				out, err := run(ctx, session, fill(switchDatabaseScript, "DB", db), nil)
				if err != nil {
					// The swap is one transaction: the database is as it was.
					if _, startErr := start(); startErr != nil {
						return "", errStep(stepDetail(err) + "; the app did not start again")
					}
					return "", errStep(stepDetail(err) + "; the database is as it was and the app is started again")
				}
				return out, nil
			})
		case "start_app", "wait_health":
			err = m.step(rec, s.Name, func() (string, error) {
				var out string
				var err error
				if s.Name == "start_app" {
					out, err = start()
				} else {
					out, err = run(ctx, session, fill(waitHealthScript, "N", site, "WAIT", fmt.Sprint(m.healthWait)), nil)
				}
				if err != nil {
					return "", errStep(stepDetail(err) + "; " + m.unRestore(ctx, session, site, start))
				}
				return out, nil
			})
		case "check":
			err = m.step(rec, s.Name, func() (string, error) {
				return m.checkSite(ctx, r, rec.Host, releaseRecord{Release: serviceapi.ReleaseV1{Kind: kindNode}})
			})
		case "clean_up":
			err = m.step(rec, s.Name, func() (string, error) {
				return run(ctx, session, fill(dropDatabaseScript, "COPY", "previous_"+db), nil)
			})
		}
		if err != nil {
			if s.Name == "stop_app" {
				_, _ = start()
			}
			return stepDetail(err)
		}
	}
	return ""
}

// unRestore swaps the databases back after a restored app never answered,
// and starts the app again. It answers what it did.
func (m *Manager) unRestore(ctx context.Context, session remote, site string, start func() (string, error)) string {
	if _, err := run(ctx, session, fill(unRestoreScript, "N", site, "DB", databaseName(site)), nil); err != nil {
		return "the database it replaced could not be put back"
	}
	if _, err := start(); err != nil {
		return "the database is put back as it was but the app did not start"
	}
	return "the database is put back as it was and the app started again"
}

// nextNightly is the next 02:30 in India after now.
func nextNightly(now time.Time) time.Time {
	n := now.In(india)
	at := time.Date(n.Year(), n.Month(), n.Day(), nightlyHour, nightlyMinute, 0, 0, india)
	if !at.After(n) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}

// Nightly backs up and checks every Node app and WordPress website each
// confirmed target serves,
// every night at 02:30 India time, until ctx ends. A target busy then is
// tried once more 15 minutes later.
func (m *Manager) Nightly(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(nextNightly(m.now()))):
		}
		busy := m.NightlyPass(ctx, nil)
		if len(busy) == 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(nightlyRetry):
		}
		m.NightlyPass(ctx, busy)
	}
}

// NightlyPass runs tonight's backups on each confirmed target with a site
// (only the targets named, when some are), one target after another, and
// answers the targets that were busy; each night ends with a check of the
// target against its record (BF-14.5a). A target's night runs at most once.
func (m *Manager) NightlyPass(ctx context.Context, only []string) []string {
	m.mu.Lock()
	records, err := m.all()
	m.mu.Unlock()
	if err != nil {
		return nil
	}
	night := "nightly-" + m.now().In(india).Format("2006-01-02")
	var busy []string
	for _, r := range records {
		if ctx.Err() != nil {
			return busy
		}
		if len(only) > 0 && !contains(only, r.Key) {
			continue
		}
		rec, outcome := m.startNightly(r.Key, night)
		switch outcome {
		case "busy":
			busy = append(busy, r.Key)
		case "started":
			m.run(rec)
		}
	}
	return busy
}

// startNightly records tonight's operation on a target and marks it busy, or
// answers why there is none.
func (m *Manager) startNightly(key, night string) (operationRecord, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil || r.State != stateConfirmed {
		return operationRecord{}, "none"
	}
	var steps []serviceapi.OperationStepV1
	anyServed := false
	for _, s := range r.Sites {
		anyServed = anyServed || served(s)
		// A stopped app's data cannot change, so it is not backed up again.
		if _, kept := findDatabase(r, s.Name); kept && appKind(siteKind(s)) && served(s) && s.State != siteStopped {
			steps = append(steps, serviceapi.OperationStepV1{Name: "back_up", Site: s.Name, State: "pending"},
				serviceapi.OperationStepV1{Name: "check_restore", Site: s.Name, State: "pending"})
		}
	}
	if !anyServed {
		return operationRecord{}, "none"
	}
	steps = append(steps, serviceapi.OperationStepV1{Name: "read_server", State: "pending"})
	if _, done := m.operationByRequest(key, night); done {
		return operationRecord{}, "done"
	}
	if m.busy[key] {
		return operationRecord{}, "busy"
	}
	op := serviceapi.OperationV1{
		SchemaVersion: serviceapi.OperationSchemaV1, OperationID: newID("op-"), Target: key, Kind: "nightly",
		RequestID: night, State: opQueued, StartedAt: m.stamp(), Steps: steps,
	}
	rec := operationRecord{SchemaVersion: operationSchema, Operation: op}
	if err := m.writeJSON(filepath.Join(m.dir, "operations"), op.OperationID, rec); err != nil {
		return operationRecord{}, "none"
	}
	m.busy[key] = true
	m.wg.Add(1)
	return rec, "started"
}

// runNightly backs up each app and checks the backup; one app's failure does
// not stop the next. It answers the first failure.
func (m *Manager) runNightly(ctx context.Context, rec *operationRecord, r record, session remote) string {
	failed := ""
	note := func(site string, err error) {
		if failed == "" {
			failed = site + ": " + stepDetail(err)
		}
	}
	for _, s := range rec.Operation.Steps {
		if s.Name != "back_up" {
			continue
		}
		site := s.Site
		current := ""
		if linked, ok := findSite(r, site); ok {
			current = linked.Release
		}
		id := ""
		if err := m.siteStep(rec, "back_up", site, func() (string, error) {
			taken, detail, err := m.backUp(ctx, session, r.Key, site, reasonNightly, current, "")
			id = taken
			return detail, err
		}); err != nil {
			note(site, err)
			m.setStep(rec, "check_restore", site, "failed", "no backup to check")
			continue
		}
		m.mu.Lock()
		cur, err := m.read(r.Key)
		m.mu.Unlock()
		d, _ := findDatabase(cur, site)
		b, ok := findBackup(d, id)
		if err != nil || !ok {
			note(site, errStep("the backup is not recorded"))
			m.setStep(rec, "check_restore", site, "failed", "the backup is not recorded")
			continue
		}
		if err := m.siteStep(rec, "check_restore", site, func() (string, error) {
			return m.restoreCopy(ctx, session, r.Key, site, b, "check_", false)
		}); err != nil {
			note(site, err)
		}
	}
	if err := m.step(rec, "read_server", func() (string, error) { return m.readServer(ctx, session, r.Key) }); err != nil && failed == "" {
		failed = stepDetail(err)
	}
	return failed
}

// The restore scripts. Each value in them (@N@ a site name, @DB@ its
// database, @ID@ a backup id, @COPY@ a database named from @DB@ with a fixed
// prefix, @KEEP@ keep or drop) is checked or fixed by ABCP before it is put in.
const (
	// restoreCopyScript restores a backup into @COPY@, owned by the app's
	// login and reachable by no one, as that login in one transaction, and
	// prints each table's rows there.
	restoreCopyScript = `set -eu
` + dockerHost + `
D="$HOME/autobuild/backups/@N@/@ID@.dump"
if [ ! -s "$D" ]; then echo "the backup's file is not on the server" >&2; exit 4; fi
q() { docker exec autobuild-postgres ` + psql + ` -v ON_ERROR_STOP=1 -qAtX -c "$1" >/dev/null; }
if [ "@KEEP@" != keep ]; then trap 'q "DROP DATABASE IF EXISTS @COPY@ WITH (FORCE)" || true' EXIT; fi
q "DROP DATABASE IF EXISTS @COPY@ WITH (FORCE)"
q "CREATE DATABASE @COPY@ OWNER @DB@"
q "REVOKE CONNECT, TEMPORARY ON DATABASE @COPY@ FROM PUBLIC"
if ! docker exec -i autobuild-postgres /usr/lib/postgresql/17/bin/pg_restore -h /scratch -U postgres --role=@DB@ --no-owner --no-privileges \
  --single-transaction --exit-on-error -d @COPY@ < "$D" >/dev/null 2>&1; then
  q "DROP DATABASE IF EXISTS @COPY@ WITH (FORCE)" || true
  echo "the backup did not restore" >&2
  exit 4
fi
docker exec -i autobuild-postgres ` + psqlBin + ` -d @COPY@ -v ON_ERROR_STOP=1 -qAtX <<'SQL'
` + countRows + `;
SQL
`
	// switchDatabaseScript swaps the restored copy in, in one transaction,
	// keeping the database it replaces as previous_@DB@.
	switchDatabaseScript = `set -eu
` + dockerHost + `
docker exec -i autobuild-postgres ` + psql + ` -v ON_ERROR_STOP=1 -qAtX >/dev/null <<'SQL'
SELECT pg_terminate_backend(pid, 5000) FROM pg_stat_activity WHERE datname IN ('@DB@', 'restore_@DB@') AND pid <> pg_backend_pid();
DROP DATABASE IF EXISTS previous_@DB@ WITH (FORCE);
BEGIN;
ALTER DATABASE @DB@ RENAME TO previous_@DB@;
ALTER DATABASE restore_@DB@ RENAME TO @DB@;
COMMIT;
SQL
echo "switched; the database it replaced is kept until the app answers"
`
	// unRestoreScript stops the app and swaps the database it replaced back.
	unRestoreScript = `set -eu
` + dockerHost + `
docker rm -f autobuild-app-@N@ >/dev/null 2>&1 || true
docker exec -i autobuild-postgres ` + psql + ` -v ON_ERROR_STOP=1 -qAtX >/dev/null <<'SQL'
SELECT pg_terminate_backend(pid, 5000) FROM pg_stat_activity WHERE datname = '@DB@' AND pid <> pg_backend_pid();
DROP DATABASE IF EXISTS restore_@DB@ WITH (FORCE);
BEGIN;
ALTER DATABASE @DB@ RENAME TO restore_@DB@;
ALTER DATABASE previous_@DB@ RENAME TO @DB@;
COMMIT;
DROP DATABASE restore_@DB@ WITH (FORCE);
SQL
echo "put back"
`
	dropDatabaseScript = `set -eu
` + dockerHost + `
docker exec autobuild-postgres ` + psql + ` -v ON_ERROR_STOP=1 -qc "DROP DATABASE IF EXISTS @COPY@ WITH (FORCE)"
echo dropped
`
)

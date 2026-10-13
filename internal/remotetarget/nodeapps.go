package remotetarget

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// Node apps on a remote target (Repo C design note
// BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md §11, BF-14.2b). A Node release runs in
// the kit's runtime image with its files read-only, on an internal network of
// its own that only the shared Postgres and the front door join. Every Node app
// on a target shares one Postgres (the kit's image as `autobuild-postgres
// server`), with a database and a login of the same name per app whose
// password is made on the server and kept only there, in
// ~/autobuild/apps/<site>/database.env. Before an update replaces a release,
// the app's database is backed up into ~/autobuild/backups/<site>/; a new
// version that never answers /health is switched back to the release it
// replaced, and data is never restored on its own. Every backup counts each
// table's rows in the same snapshot as its dump, so restoring it can be checked
// (restore.go).

// The kit's images, by digest: Node format 1's runtime, and the Postgres with
// its server mode (Repo C tools/node-kit, published by node-runtime.yml: the
// runtime hosted apps run, and the Postgres of Repo C ea389f4, #494).
const (
	NodeRuntimeImage  = "ghcr.io/pankajleh/autobuild-node-app-runtime@sha256:57899c524d200601f752cd059e8cfbd5ac93b16cda0aa40df020dba7301f7957"
	NodePostgresImage = "ghcr.io/pankajleh/autobuild-node-app-postgres@sha256:1059b7d7f68e8ce1af825107c373b6a5dc2acb7f44bc15c5d6bfbe82456a62c9"
)

const (
	kindStatic = "static"
	kindNode   = "node"

	sandboxGVisor   = "gvisor"
	sandboxStandard = "standard"

	reasonBeforeUpdate  = "before_update"
	reasonBeforeRestore = "before_restore"
	reasonNightly       = "nightly"
	reasonManual        = "manual"

	// Backups are kept 7 days and at most 14, and the newest always.
	backupKeep  = 7 * 24 * time.Hour
	backupLimit = 14
	// A database's tables, as a backup counts them, and the length of a
	// table's name as quote_ident gives it with its schema.
	maxTables    = 2000
	maxTableName = 260
	// defaultHealthWait bounds how long a new version may take to answer
	// /health, migrations first.
	defaultHealthWait = 120
)

var (
	backupIDPattern  = regexp.MustCompile(`^bk-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4}$`)
	migrationPattern = regexp.MustCompile(`^migrations/[0-9]{4}_[a-z0-9_]+\.sql$`)
	databasePattern  = regexp.MustCompile(`^app_[a-z0-9_]{1,40}$`)
)

// databaseRecord is a Node app's database on its target. It is kept after the
// app is removed, with its backups, until it is removed in its own right.
type databaseRecord struct {
	Site      string         `json:"site"`
	Name      string         `json:"name"`
	CreatedAt string         `json:"created_at"`
	Backups   []backupRecord `json:"backups,omitempty"`
}

// backupRecord is a backup as ABCP keeps it: what it shows, and each table's
// rows when it was taken (when Tables is set), which a check compares a
// restore with.
type backupRecord struct {
	serviceapi.BackupV1
	Counts map[string]int64 `json:"counts,omitempty"`
}

func findBackup(d databaseRecord, id string) (backupRecord, bool) {
	for _, b := range d.Backups {
		if b.BackupID == id {
			return b, true
		}
	}
	return backupRecord{}, false
}

// parseCounts reads the rows of each table as a backup or a restore counted
// them: a JSON object of table names and counts.
func parseCounts(text string) (map[string]int64, error) {
	counts := map[string]int64{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &counts); err != nil || len(counts) > maxTables {
		return nil, errStep("the rows could not be counted")
	}
	for name, n := range counts {
		if name == "" || len(name) > maxTableName || n < 0 {
			return nil, errStep("the rows could not be counted")
		}
	}
	return counts, nil
}

// countTotals is a database's tables and rows.
func countTotals(counts map[string]int64) (int, int64) {
	var rows int64
	for _, n := range counts {
		rows += n
	}
	return len(counts), rows
}

// databaseName is a site's database and login: app_ and its name, hyphens as
// underscores.
func databaseName(site string) string { return "app_" + strings.ReplaceAll(site, "-", "_") }

// siteKind is "node" or "static" (a site kept before BF-14.2b has none).
func siteKind(s siteRecord) string {
	if s.Kind == kindNode {
		return kindNode
	}
	return kindStatic
}

// sandbox is gVisor when the target's last check found it, else the standard
// runtime. Different customers' apps share a target only from BF-14.6, which
// will require gVisor there.
func sandbox(r record) string {
	if r.Preflight != nil && r.Preflight.Facts != nil && r.Preflight.Facts.GVisor {
		return sandboxGVisor
	}
	return sandboxStandard
}

// nodeSteps are what applying a Node release does, in order.
func nodeSteps(files, frontdoor string, replacing bool) []string {
	steps := []string{files, "get_images", "start_database", "make_database"}
	if replacing {
		steps = append(steps, "back_up")
	}
	return append(steps, "switch_site", "start_app", "wait_health", "write_site", frontdoor, "check")
}

// validNodeFiles is Node format 1's layout: server.js, package.json and
// public/index.html, no installed packages, and migrations named in order.
func validNodeFiles(files []manifestFile) bool {
	need := map[string]bool{"server.js": false, "package.json": false, "public/index.html": false}
	for _, f := range files {
		if _, ok := need[f.Path]; ok {
			need[f.Path] = true
		}
		if f.Path == "node_modules" || strings.HasPrefix(f.Path, "node_modules/") || strings.Contains(f.Path, "/node_modules/") {
			return false
		}
		if strings.HasPrefix(f.Path, "migrations/") && !migrationPattern.MatchString(f.Path) {
			return false
		}
	}
	for _, ok := range need {
		if !ok {
			return false
		}
	}
	return true
}

func findDatabase(r record, site string) (databaseRecord, bool) {
	for _, d := range r.Databases {
		if d.Site == site {
			return d, true
		}
	}
	return databaseRecord{}, false
}

func setDatabase(r *record, d databaseRecord) {
	for i := range r.Databases {
		if r.Databases[i].Site == d.Site {
			r.Databases[i] = d
			return
		}
	}
	r.Databases = append(r.Databases, d)
}

// nodeNetworks are the sites whose networks the front door joins: every Node
// app it serves.
func (m *Manager) nodeNetworks(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return ""
	}
	var names []string
	for _, s := range r.Sites {
		if siteKind(s) == kindNode && served(s) && siteNamePattern.MatchString(s.Name) {
			names = append(names, s.Name)
		}
	}
	return strings.Join(names, " ")
}

func newBackupID(now time.Time) string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return "bk-" + now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b)
}

// backUp dumps a Node app's database on its target, with each table's rows
// counted in the dump's own snapshot, records the backup and prunes the ones
// no longer kept, never the one named keep (a restore's). It answers the new
// backup's id and the step's detail.
func (m *Manager) backUp(ctx context.Context, session remote, key, site, reason, release, keep string) (string, string, error) {
	db := databaseName(site)
	if !siteNamePattern.MatchString(site) || !databasePattern.MatchString(db) {
		return "", "", errStep("failed")
	}
	id := newBackupID(m.now())
	out, err := run(ctx, session, fill(backupScript, "N", site, "DB", db, "ID", id), nil)
	if err != nil {
		return "", "", err
	}
	size, counts, err := parseBackup(out)
	if err != nil {
		_, _ = run(ctx, session, fill(removeBackupsScript, "N", site, "LIST", id), nil)
		return "", "", err
	}
	tables, rows := countTotals(counts)
	backup := backupRecord{BackupV1: serviceapi.BackupV1{BackupID: id, Reason: reason, TakenAt: m.stamp(), Bytes: size, Release: release,
		Tables: &tables, Rows: &rows}, Counts: counts}
	m.mu.Lock()
	var drop []string
	if r, err := m.read(key); err == nil {
		d, ok := findDatabase(r, site)
		if !ok {
			d = databaseRecord{Site: site, Name: db, CreatedAt: m.stamp()}
		}
		d.Backups = append(d.Backups, backup)
		d.Backups, drop = pruneBackupList(d.Backups, m.now(), keep)
		setDatabase(&r, d)
		_ = m.write(r)
	}
	m.mu.Unlock()
	if len(drop) > 0 {
		_, _ = run(ctx, session, fill(removeBackupsScript, "N", site, "LIST", strings.Join(drop, " ")), nil)
	}
	return id, fmt.Sprintf("%d bytes, %d tables, %d rows", size, tables, rows), nil
}

// parseBackup reads what the backup script printed: the dump's size, then each
// table's rows.
func parseBackup(out string) (int64, map[string]int64, error) {
	size, rest, _ := strings.Cut(strings.TrimSpace(out), "\n")
	n, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64)
	if err != nil || n <= 0 {
		return 0, nil, errStep("the backup is empty")
	}
	counts, err := parseCounts(rest)
	if err != nil {
		return 0, nil, err
	}
	return n, counts, nil
}

// pruneBackupList keeps the backups of the last 7 days, at most 14, the newest
// always and the one named keep; it answers what it keeps and the ids it drops.
func pruneBackupList(backups []backupRecord, now time.Time, keep string) ([]backupRecord, []string) {
	var kept []backupRecord
	var drop []string
	for i, b := range backups {
		taken, err := time.Parse(time.RFC3339, b.TakenAt)
		if i == len(backups)-1 || b.BackupID == keep || (err == nil && now.Sub(taken) < backupKeep) {
			kept = append(kept, b)
		} else if backupIDPattern.MatchString(b.BackupID) {
			drop = append(drop, b.BackupID)
		}
	}
	for i := 0; len(kept) > backupLimit && i < len(kept); {
		if kept[i].BackupID == keep {
			i++
			continue
		}
		if backupIDPattern.MatchString(kept[i].BackupID) {
			drop = append(drop, kept[i].BackupID)
		}
		kept = append(kept[:i], kept[i+1:]...)
	}
	return kept, drop
}

// Backups lists a Node app's backups, kept after the app is removed.
func (m *Manager) Backups(_ context.Context, key, site string) (serviceapi.BackupsV1, error) {
	if !siteNamePattern.MatchString(site) {
		return serviceapi.BackupsV1{}, serviceapi.ErrSiteUnknown
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return serviceapi.BackupsV1{}, err
	}
	d, ok := findDatabase(r, site)
	if !ok {
		return serviceapi.BackupsV1{}, serviceapi.ErrSiteUnknown
	}
	out := serviceapi.BackupsV1{SchemaVersion: serviceapi.BackupsSchemaV1, Target: key, Site: site, Database: d.Name, Backups: []serviceapi.BackupV1{}}
	for _, b := range d.Backups {
		out.Backups = append(out.Backups, b.BackupV1)
	}
	return out, nil
}

// BackUp takes a backup of a Node app's database now, as an operation. The
// same request answers the same operation.
func (m *Manager) BackUp(_ context.Context, key, site string, create serviceapi.BackupCreateV1) (serviceapi.OperationV1, error) {
	if !siteNamePattern.MatchString(site) || !requestIDPattern.MatchString(create.RequestID) {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return serviceapi.OperationV1{}, err
	}
	if existing, ok := m.operationByRequest(key, create.RequestID); ok {
		if existing.Operation.Kind != "backup" || existing.Operation.Site != site {
			return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
		}
		return existing.Operation, nil
	}
	if _, ok := findDatabase(r, site); !ok {
		return serviceapi.OperationV1{}, serviceapi.ErrSiteUnknown
	}
	if r.State != stateConfirmed {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetNotReady
	}
	if m.busy[key] {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetBusy
	}
	op := serviceapi.OperationV1{
		SchemaVersion: serviceapi.OperationSchemaV1, OperationID: newID("op-"), Target: key, Kind: "backup", Site: site,
		RequestID: create.RequestID, State: opQueued, StartedAt: m.stamp(),
		Steps: []serviceapi.OperationStepV1{{Name: "back_up", State: "pending"}},
	}
	rec := operationRecord{SchemaVersion: operationSchema, Operation: op}
	if err := m.writeJSON(filepath.Join(m.dir, "operations"), op.OperationID, rec); err != nil {
		return serviceapi.OperationV1{}, err
	}
	m.busy[key] = true
	m.wg.Add(1)
	go m.run(rec)
	return op, nil
}

func (m *Manager) runBackup(ctx context.Context, rec *operationRecord, r record, session remote) string {
	site := rec.Operation.Site
	current := ""
	if s, ok := findSite(r, site); ok {
		current = s.Release
	}
	if err := m.step(rec, "back_up", func() (string, error) {
		_, detail, err := m.backUp(ctx, session, r.Key, site, reasonManual, current, "")
		return detail, err
	}); err != nil {
		return stepDetail(err)
	}
	return ""
}

// rollBack puts back the release a failed update replaced and starts it
// again, or stops the new app when there was none. It answers what it did.
func (m *Manager) rollBack(ctx context.Context, rec *operationRecord, key string, session remote) string {
	site := rec.Operation.Site
	if rec.Replaces == "" || !digestPattern.MatchString(rec.Replaces) {
		if _, err := run(ctx, session, fill(stopAppOnlyScript, "N", site), nil); err != nil {
			return "the new app is left stopped or failed"
		}
		return "the new app is stopped"
	}
	if _, err := run(ctx, session, fill(switchScript, "N", site, "D", rec.Replaces), nil); err != nil {
		return "the release it replaced could not be put back"
	}
	m.mu.Lock()
	if cur, err := m.read(key); err == nil {
		if s, ok := findSite(cur, site); ok {
			s.Release = rec.Replaces
			m.setSite(&cur, s)
			_ = m.write(cur)
		}
	}
	m.mu.Unlock()
	if _, err := run(ctx, session, fill(startAppScript, "N", site, "RUNTIME", runtimeFlag(rec.Sandbox)), nil); err != nil {
		return "the release it replaced is linked but did not start"
	}
	return "switched back to the release it replaced"
}

func runtimeFlag(sandbox string) string {
	if sandbox == sandboxGVisor {
		return "--runtime=runsc"
	}
	return ""
}

// The Node scripts. Each value in them (@N@ a site name, @DB@ its database,
// @ID@ a backup id, @LIST@ backup ids, @RUNTIME@ the sandbox flag, @WAIT@ a
// number of seconds) is checked by ABCP against a narrow pattern or fixed
// before it is put in.
const (
	psqlBin = `/usr/lib/postgresql/17/bin/psql -h /scratch -U postgres`
	psql    = psqlBin + ` -d postgres`

	// countRows answers each ordinary table's rows, by schema and name, as one
	// JSON object (query_to_xml counts each table in the same transaction).
	countRows = `SELECT coalesce(json_object_agg(t.name, t.n ORDER BY t.name), '{}') FROM (SELECT quote_ident(s.nspname) || '.' || ` +
		`quote_ident(c.relname) AS name, (xpath('/row/n/text()', query_to_xml(format('SELECT count(*) AS n FROM %I.%I', s.nspname, ` +
		`c.relname), false, true, '')))[1]::text::bigint AS n FROM pg_class c JOIN pg_namespace s ON s.oid = c.relnamespace ` +
		`WHERE c.relkind = 'r' AND s.nspname <> 'information_schema' AND s.nspname NOT LIKE 'pg\_%') t`

	getImagesScript = `set -eu
` + dockerHost + `
got=""
for image in ` + NodeRuntimeImage + ` ` + NodePostgresImage + `; do
  if ! docker image inspect "$image" >/dev/null 2>&1; then docker pull -q "$image" >/dev/null; got=pulled; fi
done
echo "${got:-already there}"
`
	startDatabaseScript = `set -eu
` + dockerHost + `
if [ "$(docker inspect -f '{{.State.Running}}' autobuild-postgres 2>/dev/null || true)" = "true" ]; then echo running; exit 0; fi
docker rm -f autobuild-postgres >/dev/null 2>&1 || true
docker network inspect autobuild-db >/dev/null 2>&1 || docker network create --internal autobuild-db >/dev/null
docker volume inspect autobuild-pgdata >/dev/null 2>&1 || docker volume create autobuild-pgdata >/dev/null
docker run -d --name autobuild-postgres --restart unless-stopped --network autobuild-db --user 999:999 --read-only --cap-drop ALL \
  --security-opt no-new-privileges --memory 512m --memory-swap 512m --pids-limit 256 --ulimit nofile=4096:4096 \
  --tmpfs /scratch:rw,nosuid,nodev,noexec,size=268435456,mode=700,uid=999,gid=999 --workdir /scratch \
  --mount type=volume,src=autobuild-pgdata,dst=/data --entrypoint /usr/bin/env \
  ` + NodePostgresImage + ` -i PATH=/usr/local/bin:/usr/bin:/bin HOME=/scratch TMPDIR=/scratch /usr/local/bin/autobuild-postgres server >/dev/null
i=0
until docker exec autobuild-postgres /usr/lib/postgresql/17/bin/pg_isready -h /scratch -q; do
  i=$((i+1))
  if [ "$i" -ge 60 ]; then echo "the database did not start" >&2; exit 4; fi
  sleep 1
done
for n in $(docker network ls --filter label=autobuild.site --format '{{.Name}}'); do
  docker network connect --alias autobuild-postgres "$n" autobuild-postgres
done
echo started
`
	makeDatabaseScript = `set -eu
` + dockerHost + `
A="$HOME/autobuild"
umask 077
mkdir -p "$A/apps/@N@"
docker network inspect autobuild-net-@N@ >/dev/null 2>&1 || docker network create --internal --label autobuild.site=@N@ autobuild-net-@N@ >/dev/null
docker inspect -f '{{json .NetworkSettings.Networks}}' autobuild-postgres | grep -q '"autobuild-net-@N@"' ||
  docker network connect --alias autobuild-postgres autobuild-net-@N@ autobuild-postgres
if [ -s "$A/apps/@N@/database.env" ]; then echo kept; exit 0; fi
pw=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
if [ "${#pw}" -ne 48 ]; then echo "no password could be made" >&2; exit 4; fi
verb=CREATE
if [ "$(docker exec autobuild-postgres ` + psql + ` -tAc "SELECT 1 FROM pg_roles WHERE rolname = '@DB@'")" = 1 ]; then verb=ALTER; fi
printf "%s ROLE @DB@ LOGIN PASSWORD '%s';\n" "$verb" "$pw" | docker exec -i autobuild-postgres ` + psql + ` -v ON_ERROR_STOP=1 -q
if [ "$(docker exec autobuild-postgres ` + psql + ` -tAc "SELECT 1 FROM pg_database WHERE datname = '@DB@'")" != 1 ]; then
  docker exec autobuild-postgres ` + psql + ` -v ON_ERROR_STOP=1 -qc "CREATE DATABASE @DB@ OWNER @DB@"
  docker exec autobuild-postgres ` + psql + ` -v ON_ERROR_STOP=1 -qc "REVOKE CONNECT, TEMPORARY ON DATABASE @DB@ FROM PUBLIC"
fi
printf 'DATABASE_URL=postgres://@DB@:%s@autobuild-postgres:5432/@DB@\n' "$pw" > "$A/apps/@N@/.database.env.next"
mv "$A/apps/@N@/.database.env.next" "$A/apps/@N@/database.env"
echo made
`
	// backupScript dumps the app's database inside a transaction whose
	// snapshot it exports, so the rows it counts there are the rows the dump
	// holds; the counts go through the Postgres's own /scratch.
	backupScript = `set -eu
` + dockerHost + `
B="$HOME/autobuild/backups/@N@"
umask 077
mkdir -p "$B"
trap 'rm -f "$B/.@ID@.part"' EXIT
docker exec -i autobuild-postgres ` + psqlBin + ` -d @DB@ -qAtX > "$B/.@ID@.part" <<'SQL'
\set ON_ERROR_STOP 1
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SELECT pg_export_snapshot() AS snap \gset
\setenv SNAP :snap
\o /scratch/.@ID@.counts
` + countRows + `;
\o
\! /usr/lib/postgresql/17/bin/pg_dump -Fc -h /scratch -U postgres --snapshot="$SNAP" @DB@ || echo failed > /scratch/.@ID@.failed
COMMIT;
SQL
counts=$(docker exec autobuild-postgres /bin/sh -c 'if [ -e /scratch/.@ID@.failed ]; then echo failed; else cat /scratch/.@ID@.counts; fi; rm -f /scratch/.@ID@.failed /scratch/.@ID@.counts')
if [ "$counts" = failed ] || [ -z "$counts" ] || [ ! -s "$B/.@ID@.part" ]; then echo "the backup failed" >&2; exit 4; fi
mv "$B/.@ID@.part" "$B/@ID@.dump"
wc -c < "$B/@ID@.dump"
printf '%s\n' "$counts"
`
	removeBackupsScript = `set -eu
cd "$HOME/autobuild/backups/@N@"
for b in @LIST@; do rm -f "./$b.dump"; done
`
	startAppScript = `set -eu
` + dockerHost + `
A="$HOME/autobuild"
test -s "$A/apps/@N@/database.env"
R=$(readlink -f "$A/releases")
src=$(readlink -f "$A/sites/@N@")
case "$src" in "$R"/*) ;; *) echo "the site's link is not a release" >&2; exit 3 ;; esac
docker rm -f autobuild-app-@N@ >/dev/null 2>&1 || true
docker run -d --name autobuild-app-@N@ --restart unless-stopped --network autobuild-net-@N@ --network-alias app @RUNTIME@ \
  --user 65534:65534 --read-only --cap-drop ALL --security-opt no-new-privileges --memory 256m --memory-swap 256m \
  --pids-limit 256 --ulimit nofile=1024:1024 --tmpfs /scratch:rw,nosuid,nodev,noexec,size=67108864,mode=700,uid=65534,gid=65534 \
  --workdir /scratch --mount type=bind,src="$src",dst=/source,readonly --env-file "$A/apps/@N@/database.env" \
  -e HOME=/scratch -e TMPDIR=/scratch --label autobuild.site=@N@ --entrypoint /usr/local/bin/node \
  ` + NodeRuntimeImage + ` /opt/autobuild/start.mjs >/dev/null
echo started
`
	waitHealthScript = `set -eu
` + dockerHost + `
i=0
while [ "$i" -lt @WAIT@ ]; do
  if docker exec autobuild-app-@N@ /bin/busybox wget -T 2 -q -O /dev/null http://127.0.0.1:8080/health >/dev/null 2>&1; then echo answers; exit 0; fi
  i=$((i+1))
  sleep 1
done
echo "the app did not answer /health" >&2
exit 4
`
	stopAppOnlyScript = `set -eu
` + dockerHost + `
docker rm -f autobuild-app-@N@ >/dev/null 2>&1 || true
echo stopped
`
	stopAppScript = `set -eu
` + dockerHost + `
docker rm -f autobuild-app-@N@ >/dev/null 2>&1 || true
docker network disconnect -f autobuild-net-@N@ autobuild-frontdoor >/dev/null 2>&1 || true
docker network disconnect -f autobuild-net-@N@ autobuild-postgres >/dev/null 2>&1 || true
docker network rm autobuild-net-@N@ >/dev/null 2>&1 || true
echo "stopped; its database and backups are kept"
`
)

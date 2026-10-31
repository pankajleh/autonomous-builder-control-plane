package remotetarget

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// WordPress websites on a remote target (Repo C design note
// WORDPRESS_WEBSITES.md §13, W5b; the images' contract is Repo C
// tools/wp-site-kit/SERVER.md, whose docker runs ci/server-helpers.sh holds).
// A WordPress website is a third release kind, wp, and goes on a target as a
// Node app does (nodeapps.go): its release read-only at /source, a container
// of its own (autobuild-app-<site>, the kit's autobuild-wp-server image) on an
// internal network of its own that only the shared database and the front
// door join, and a backup before every update, nightly and on request, copied
// off the server as a Node app's is. Every WordPress website on a target
// shares one MariaDB (autobuild-mariadb, the kit's autobuild-wp-mariadb image,
// its data in the volume autobuild-mariadb-data), with a database and a login
// of the same name per site (wp_<site>) whose password is made on the server
// and kept only there, in ~/autobuild/apps/<site>/database.env. The site's own
// volume (autobuild-wp-<site>) holds its uploads; each start writes site.env
// (its public address and its administrator's) beside database.env. The
// site's first start makes its administrator, the owner's address with a long
// random password, which ABCP reads from the site once and never keeps
// (SiteAdminPassword). A backup is the database's dump and the uploads' tar;
// a restore swaps the tables in one RENAME TABLE (MariaDB cannot rename a
// database) and puts the uploads back. The site's contact form hands its
// messages to the front door's inside listener (deploy.go), which passes them
// to Autobuild's relay.

// The kit's server images, by digest (Repo C tools/wp-site-kit, built and run
// by wp-site-kit.yml's server job exactly as these scripts run them, and
// published from main: the images of Repo C 168dbff, #918, W5a).
const (
	WordPressServerImage  = "ghcr.io/pankajleh/autobuild-wp-server@sha256:e43cf36bff1be641b053e98856f1c3612924992a00f465c5eda2696cafa1d778"
	WordPressMariaDBImage = "ghcr.io/pankajleh/autobuild-wp-mariadb@sha256:ad91266b3362c723d2bc7386a052e41ef0bb5e60231e977b2f540da89901e99e"
)

var (
	wpDatabasePattern = regexp.MustCompile(`^wp_[a-z0-9_]{1,40}$`)
	// adminEmailPattern is an address PHP's FILTER_VALIDATE_EMAIL takes (the
	// site's start checks it so), in lower case, with nothing a shell, a
	// printf format or an env file could read differently.
	adminEmailPattern = regexp.MustCompile(`^[a-z0-9_%+-]+(\.[a-z0-9_%+-]+)*@([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// adminPasswordPattern is what the site's first start makes: 48 hex digits
	// (SERVER.md), within what its installer takes.
	adminPasswordPattern = regexp.MustCompile(`^[A-Za-z0-9]{32,128}$`)
	// wpPHPPattern is a file PHP could run.
	wpPHPPattern = regexp.MustCompile(`(?i)\.(php[0-9]?|phtml|phar|phps)$`)
)

// wpDatabaseName is a WordPress website's database and login: wp_ and its
// name, hyphens as underscores.
func wpDatabaseName(site string) string { return "wp_" + strings.ReplaceAll(site, "-", "_") }

// validWPFiles is Repo C's WordPress website format (tools/wp-site-kit/
// WP_SITE_FORMAT.md): site/site.json, site/pages.json, theme/style.css and
// README.md, and PHP nowhere but in theme/, where the server runs only the
// starting theme's own PHP whatever the release holds (SERVER.md, "What a
// start does").
func validWPFiles(files []manifestFile) bool {
	need := map[string]bool{"site/site.json": false, "site/pages.json": false, "theme/style.css": false, "README.md": false}
	for _, f := range files {
		if _, ok := need[f.Path]; ok {
			need[f.Path] = true
		}
		if wpPHPPattern.MatchString(f.Path) && !strings.HasPrefix(f.Path, "theme/") {
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

// checkAdminEmail answers an administrator's address in lower case, or false
// when it is not one the site takes.
func checkAdminEmail(raw string) (string, bool) {
	email := strings.ToLower(strings.TrimSpace(raw))
	local, _, _ := strings.Cut(email, "@")
	if len(email) > 254 || len(local) > 64 || !adminEmailPattern.MatchString(email) {
		return "", false
	}
	return email, true
}

// adminUser is the user name the site's first start gives its administrator:
// the address, or admin for one longer than WordPress's 60 characters.
func adminUser(email string) string {
	if len(email) <= 60 {
		return email
	}
	return "admin"
}

// waitFor is how long a new version may take to answer /health: a WordPress
// website's first start installs WordPress and imports its pages, and every
// start imports them again, so it has half as long again as a Node app (180
// seconds, SERVER.md "Waiting for it").
func (m *Manager) waitFor(kind string) string {
	if kind == kindWP {
		return fmt.Sprint(m.healthWait * 3 / 2)
	}
	return fmt.Sprint(m.healthWait)
}

// startWebsite starts a WordPress website with its database, its public
// address and its administrator's, which the start script reads on its input
// and keeps in site.env, and, while relays are on, the address of the front
// door's inside listener, where its contact form hands its messages.
func (m *Manager) startWebsite(ctx context.Context, session remote, key, site, sandbox string) (string, error) {
	m.mu.Lock()
	r, err := m.read(key)
	relay := ""
	if m.relayHost != "" {
		relay = "-e AUTOBUILD_RELAY=" + insideRelay + " "
	}
	m.mu.Unlock()
	if err != nil {
		return "", errStep("target_unreadable")
	}
	s, ok := findSite(r, site)
	d, kept := findDatabase(r, site)
	if !ok || !validHost(s.Host) || !kept || !adminEmailPattern.MatchString(d.AdminEmail) {
		return "", errStep("the site's address or its administrator's is not known")
	}
	settings := "https://" + s.Host + "\n" + d.AdminEmail + "\n"
	return run(ctx, session, fill(wpStartAppScript, "N", site, "RUNTIME", runtimeFlag(sandbox), "RELAY", relay), strings.NewReader(settings))
}

// parseWPBackup reads what a WordPress website's backup script printed: the
// dump's size, the uploads' size, then each table's rows.
func parseWPBackup(out string) (int64, int64, map[string]int64, error) {
	lines := strings.SplitN(strings.TrimSpace(out), "\n", 3)
	if len(lines) != 3 {
		return 0, 0, nil, errStep("the backup is empty")
	}
	dump, err := strconv.ParseInt(strings.TrimSpace(lines[0]), 10, 64)
	if err != nil || dump <= 0 {
		return 0, 0, nil, errStep("the backup is empty")
	}
	uploads, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
	if err != nil || uploads <= 0 {
		return 0, 0, nil, errStep("the backup's uploads are empty")
	}
	counts, err := parseCounts(lines[2])
	if err != nil {
		return 0, 0, nil, err
	}
	return dump, uploads, counts, nil
}

// wpRestoreSteps are a WordPress website's restore: a Node app's, with its
// uploads put back while it is stopped.
func wpRestoreSteps() []string {
	return []string{"back_up", "restore_copy", "stop_app", "switch_database", "restore_files", "start_app", "wait_health", "check", "clean_up"}
}

// runRestoreWebsite restores a WordPress website's backup: a backup first, the
// dump restored into restore_wp_<site> and its rows compared while the site
// runs, then, with the site stopped, its tables swapped in (the ones they
// replace kept in previous_wp_<site>) and its uploads put back, and the site
// started. When the site never answers, its tables and uploads are put back
// as the first backup took them and it is started again.
func (m *Manager) runRestoreWebsite(ctx context.Context, rec *operationRecord, r record, session remote, b backupRecord) string {
	site := rec.Operation.Site
	db := wpDatabaseName(site)
	start := func() (string, error) { return m.startApp(ctx, session, r.Key, site, rec.Sandbox) }
	before := ""
	for _, s := range rec.Operation.Steps {
		var err error
		switch s.Name {
		case "back_up":
			err = m.step(rec, s.Name, func() (string, error) {
				id, detail, err := m.backUp(ctx, session, r.Key, site, reasonBeforeRestore, rec.Release, b.BackupID)
				before = id
				return detail, err
			})
		case "restore_copy":
			err = m.step(rec, s.Name, func() (string, error) { return m.restoreCopy(ctx, session, r.Key, site, b, "restore_", true) })
		case "stop_app":
			err = m.step(rec, s.Name, func() (string, error) { return run(ctx, session, fill(stopAppOnlyScript, "N", site), nil) })
		case "switch_database":
			err = m.step(rec, s.Name, func() (string, error) {
				out, err := run(ctx, session, fill(wpSwitchDatabaseScript, "DB", db), nil)
				if err != nil {
					// The swap is one RENAME TABLE: the database is as it was.
					if _, startErr := start(); startErr != nil {
						return "", errStep(stepDetail(err) + "; the site did not start again")
					}
					return "", errStep(stepDetail(err) + "; the database is as it was and the site is started again")
				}
				return out, nil
			})
		case "restore_files":
			err = m.step(rec, s.Name, func() (string, error) {
				out, err := run(ctx, session, fill(wpRestoreFilesScript, "N", site, "ID", b.BackupID), nil)
				if err != nil {
					// files restore changes nothing when it fails.
					return "", errStep(stepDetail(err) + "; " + m.unRestoreWebsite(ctx, session, site, "", start))
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
					out, err = run(ctx, session, fill(waitHealthScript, "N", site, "WAIT", m.waitFor(kindWP)), nil)
				}
				if err != nil {
					return "", errStep(stepDetail(err) + "; " + m.unRestoreWebsite(ctx, session, site, before, start))
				}
				return out, nil
			})
		case "check":
			err = m.step(rec, s.Name, func() (string, error) {
				return m.checkSite(ctx, r, rec.Host, releaseRecord{Release: serviceapi.ReleaseV1{Kind: kindWP}})
			})
		case "clean_up":
			err = m.step(rec, s.Name, func() (string, error) { return run(ctx, session, fill(wpDropCopiesScript, "DB", db), nil) })
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

// unRestoreWebsite stops a restored WordPress website that never answered,
// swaps its tables back, puts back the uploads of the backup named before (the
// one the restore took first) when it names one, and starts the site again.
// It answers what it did.
func (m *Manager) unRestoreWebsite(ctx context.Context, session remote, site, before string, start func() (string, error)) string {
	if _, err := run(ctx, session, fill(wpUnRestoreScript, "N", site, "DB", wpDatabaseName(site)), nil); err != nil {
		return "the database it replaced could not be put back"
	}
	said := "the database is put back as it was"
	if before != "" {
		if !backupIDPattern.MatchString(before) {
			return said + " but its uploads could not be"
		}
		if _, err := run(ctx, session, fill(wpRestoreFilesScript, "N", site, "ID", before), nil); err != nil {
			return said + " but its uploads could not be"
		}
		said = "the database and uploads are put back as they were"
	}
	if _, err := start(); err != nil {
		return said + " but the site did not start"
	}
	return said + " and the site started again"
}

// liveWebsite is a confirmed target and a WordPress website it serves, with
// its administrator's address, for a request that asks the running site.
func (m *Manager) liveWebsite(key, site string) (record, siteRecord, databaseRecord, error) {
	if !siteNamePattern.MatchString(site) {
		return record{}, siteRecord{}, databaseRecord{}, serviceapi.ErrTargetInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return record{}, siteRecord{}, databaseRecord{}, err
	}
	s, ok := findSite(r, site)
	if !ok || !served(s) {
		return record{}, siteRecord{}, databaseRecord{}, serviceapi.ErrSiteUnknown
	}
	if siteKind(s) != kindWP {
		return record{}, siteRecord{}, databaseRecord{}, serviceapi.ErrTargetInvalid
	}
	if s.State == siteStopped {
		return record{}, siteRecord{}, databaseRecord{}, serviceapi.ErrSiteStopped
	}
	d, kept := findDatabase(r, site)
	if !kept || !adminEmailPattern.MatchString(d.AdminEmail) {
		return record{}, siteRecord{}, databaseRecord{}, serviceapi.ErrSiteUnknown
	}
	if r.State != stateConfirmed {
		return record{}, siteRecord{}, databaseRecord{}, serviceapi.ErrTargetNotReady
	}
	return r, s, d, nil
}

// SiteAdminPassword reads a WordPress website's administrator's password from
// the site, once (Repo C design note WORDPRESS_WEBSITES.md §13.3): the site
// deletes it as it is read, and every read after answers it as taken. The
// password passes through ABCP to the answer alone: no record, operation, step
// or log holds it. It runs beside operations, as it changes nothing of ABCP's.
func (m *Manager) SiteAdminPassword(ctx context.Context, key, site string) (serviceapi.SiteAdminPasswordV1, error) {
	r, s, d, err := m.liveWebsite(key, site)
	if err != nil {
		return serviceapi.SiteAdminPasswordV1{}, err
	}
	session, outcome := m.open(ctx, r)
	if session == nil {
		return serviceapi.SiteAdminPasswordV1{}, errStep(outcome)
	}
	defer session.Close()
	out, err := run(ctx, session, fill(wpAdminPasswordScript, "N", site), nil)
	if err != nil {
		return serviceapi.SiteAdminPasswordV1{}, err
	}
	answer := serviceapi.SiteAdminPasswordV1{SchemaVersion: serviceapi.SiteAdminPasswordSchemaV1, Target: key, Site: site, Host: s.Host,
		User: adminUser(d.AdminEmail), Email: d.AdminEmail}
	password := strings.TrimSpace(out)
	switch {
	case password == "":
		answer.Taken = true
	case adminPasswordPattern.MatchString(password):
		answer.Password = password
	default:
		return serviceapi.SiteAdminPasswordV1{}, errStep("the site's answer is not a password")
	}
	return answer, nil
}

// SiteResetLink has a WordPress website send its administrator WordPress's
// own password-reset email, the way the site sends its mail (the server's
// relay); the link is never printed. A site that could not send it answers
// ErrNotSent.
func (m *Manager) SiteResetLink(ctx context.Context, key, site string) (serviceapi.SiteResetLinkV1, error) {
	r, _, d, err := m.liveWebsite(key, site)
	if err != nil {
		return serviceapi.SiteResetLinkV1{}, err
	}
	session, outcome := m.open(ctx, r)
	if session == nil {
		return serviceapi.SiteResetLinkV1{}, errStep(outcome)
	}
	defer session.Close()
	out, err := run(ctx, session, fill(wpResetLinkScript, "N", site), strings.NewReader(d.AdminEmail+"\n"))
	if err != nil {
		if strings.HasPrefix(stepDetail(err), "not sent") {
			return serviceapi.SiteResetLinkV1{}, serviceapi.ErrNotSent
		}
		return serviceapi.SiteResetLinkV1{}, err
	}
	if lastLine(out) != "sent" {
		return serviceapi.SiteResetLinkV1{}, serviceapi.ErrNotSent
	}
	return serviceapi.SiteResetLinkV1{SchemaVersion: serviceapi.SiteResetLinkSchemaV1, Target: key, Site: site, Email: d.AdminEmail, Sent: true}, nil
}

// mariadbSwap is SQL that moves, in one RENAME TABLE (which MariaDB does all
// at once or not at all), every table of db into aside, made empty first, and
// every table of from into db: MariaDB cannot rename a database (SERVER.md,
// "Backups and restore"). When from has no table the statement is NULL, which
// MariaDB refuses to prepare, so nothing moves and the client stops there. The
// site's login keeps its grant on db, and has none on aside. Each name is
// checked or fixed before it is put in.
func mariadbSwap(db, aside, from string) string {
	moves := func(order int, schema, to string) string {
		return fmt.Sprintf("  SELECT %d AS o, table_name AS t, CONCAT('%s.', CHAR(96 USING utf8mb4), table_name, CHAR(96 USING utf8mb4), ' TO %s.', "+
			"CHAR(96 USING utf8mb4), table_name, CHAR(96 USING utf8mb4)) AS m\n"+
			"    FROM information_schema.tables WHERE table_schema = '%s' AND table_type = 'BASE TABLE'\n", order, schema, to, schema)
	}
	return "DROP DATABASE IF EXISTS " + aside + ";\n" +
		"CREATE DATABASE " + aside + " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n" +
		"SET SESSION group_concat_max_len = 1048576;\n" +
		"SELECT IF(SUM(o = 2) > 0, CONCAT('RENAME TABLE ', GROUP_CONCAT(m ORDER BY o, t SEPARATOR ', ')), NULL) INTO @swap FROM (\n" +
		moves(1, db, aside) + "  UNION ALL\n" + moves(2, from, db) + ") AS moves;\n" +
		"PREPARE swap FROM @swap;\nEXECUTE swap;\nDEALLOCATE PREPARE swap;\n"
}

// mariadbAdmin is the shared MariaDB's administrator, mysql, on the server's
// own socket, which only docker exec reaches; it reads SQL on its input.
const mariadbAdmin = `docker exec -i autobuild-mariadb mariadb --no-defaults --socket=/scratch/mysqld.sock --user=mysql --batch`

// The WordPress website scripts, following SERVER.md and its
// ci/server-helpers.sh. Each value in them (@N@ a site name, @DB@ its
// database, @ID@ a backup id, @COPY@ a database named from @DB@ with a fixed
// prefix, @KEEP@ keep or drop, @RUNTIME@ the sandbox flag, @RELAY@ the inside
// listener's address or nothing, @WPNETS@ site names) is checked or fixed by
// ABCP before it is put in; the site's address, its administrator's and every
// password reach the server on a script's input or are made there.
const (
	wpGetImagesScript = `set -eu
` + dockerHost + `
got=""
for image in ` + WordPressServerImage + ` ` + WordPressMariaDBImage + `; do
  if ! docker image inspect "$image" >/dev/null 2>&1; then docker pull -q "$image" >/dev/null; got=pulled; fi
done
echo "${got:-already there}"
`
	// wpStartDatabaseScript starts the shared MariaDB unless it runs; started
	// again, it joins each WordPress website's network again (@WPNETS@), and no
	// other site's.
	wpStartDatabaseScript = `set -eu
` + dockerHost + `
if [ "$(docker inspect -f '{{.State.Running}}' autobuild-mariadb 2>/dev/null || true)" = "true" ]; then echo running; exit 0; fi
docker rm -f autobuild-mariadb >/dev/null 2>&1 || true
docker network inspect autobuild-db >/dev/null 2>&1 || docker network create --internal autobuild-db >/dev/null
docker volume inspect autobuild-mariadb-data >/dev/null 2>&1 || docker volume create autobuild-mariadb-data >/dev/null
docker run -d --name autobuild-mariadb --restart unless-stopped --network autobuild-db --user 999:999 --read-only --cap-drop ALL \
  --security-opt no-new-privileges --memory 512m --memory-swap 512m --pids-limit 256 --ulimit nofile=4096:4096 \
  --tmpfs /scratch:rw,nosuid,nodev,noexec,size=268435456,mode=700,uid=999,gid=999 --workdir /scratch \
  --mount type=volume,src=autobuild-mariadb-data,dst=/data --entrypoint /usr/local/bin/autobuild-mariadb \
  ` + WordPressMariaDBImage + ` server >/dev/null
i=0
until docker exec autobuild-mariadb mariadb-admin --no-defaults --socket=/scratch/mysqld.sock --user=mysql --silent ping >/dev/null 2>&1; do
  i=$((i+1))
  if [ "$i" -ge 60 ]; then echo "the database did not start" >&2; exit 4; fi
  sleep 1
done
for n in @WPNETS@; do
  docker network inspect "autobuild-net-$n" >/dev/null 2>&1 || continue
  docker network connect --alias autobuild-mariadb "autobuild-net-$n" autobuild-mariadb
done
echo started
`
	// wpMakeDatabaseScript makes the site's network, and its database and
	// login with a password made here that goes to MariaDB on its input and
	// into database.env, and nowhere else. A kept database.env gives its
	// password again, so a new database server (a restore onto a new server)
	// takes the one the site already has.
	wpMakeDatabaseScript = `set -eu
` + dockerHost + `
A="$HOME/autobuild"
umask 077
mkdir -p "$A/apps/@N@"
docker network inspect autobuild-net-@N@ >/dev/null 2>&1 || docker network create --internal --label autobuild.site=@N@ autobuild-net-@N@ >/dev/null
docker inspect -f '{{json .NetworkSettings.Networks}}' autobuild-mariadb | grep -q '"autobuild-net-@N@"' ||
  docker network connect --alias autobuild-mariadb autobuild-net-@N@ autobuild-mariadb
said=kept
pw=$(sed -n 's/^DB_PASSWORD=//p' "$A/apps/@N@/database.env" 2>/dev/null || true)
if [ -z "$pw" ]; then
  said=made
  pw=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
  if [ "${#pw}" -ne 48 ]; then echo "no password could be made" >&2; exit 4; fi
fi
printf '%s\n' "$pw" | docker exec -i autobuild-mariadb autobuild-mariadb create-site @DB@ >/dev/null
if [ "$said" = made ]; then
  printf 'DB_HOST=autobuild-mariadb:3306\nDB_NAME=@DB@\nDB_USER=@DB@\nDB_PASSWORD=%s\n' "$pw" > "$A/apps/@N@/.database.env.next"
  mv "$A/apps/@N@/.database.env.next" "$A/apps/@N@/database.env"
fi
echo "$said"
`
	// wpStartAppScript reads the site's public address and its
	// administrator's on its input and keeps them in site.env, written before
	// every start since the address changes when the person's domain replaces
	// the first one; then it starts the site on its volume. Its own output is
	// kept by Docker in at most two files of 1 MB.
	wpStartAppScript = `set -eu
` + dockerHost + `
A="$HOME/autobuild"
umask 077
test -s "$A/apps/@N@/database.env"
IFS= read -r url
IFS= read -r email
host=${url#https://}
case "$url" in https://*) ;; *) echo "no site address" >&2; exit 3 ;; esac
case "$host" in *[!a-z0-9.-]*|'') echo "no site address" >&2; exit 3 ;; esac
case "$email" in *[!a-z0-9._%+@-]*|'') echo "no administrator's address" >&2; exit 3 ;; esac
case "$email" in ?*@?*) ;; *) echo "no administrator's address" >&2; exit 3 ;; esac
printf 'SITE_URL=%s\nADMIN_EMAIL=%s\n' "$url" "$email" > "$A/apps/@N@/.site.env.next"
mv "$A/apps/@N@/.site.env.next" "$A/apps/@N@/site.env"
R=$(readlink -f "$A/releases")
src=$(readlink -f "$A/sites/@N@")
case "$src" in "$R"/*) ;; *) echo "the site's link is not a release" >&2; exit 3 ;; esac
docker volume inspect autobuild-wp-@N@ >/dev/null 2>&1 || docker volume create autobuild-wp-@N@ >/dev/null
docker rm -f autobuild-app-@N@ >/dev/null 2>&1 || true
docker run -d --name autobuild-app-@N@ --restart unless-stopped --network autobuild-net-@N@ --network-alias app @RUNTIME@ \
  --user 33:33 --read-only --cap-drop ALL --security-opt no-new-privileges --memory 512m --memory-swap 512m \
  --pids-limit 256 --ulimit nofile=1024:1024 --log-driver json-file --log-opt max-size=1m --log-opt max-file=2 \
  --tmpfs /scratch:rw,nosuid,nodev,noexec,size=134217728,mode=700,uid=33,gid=33 --workdir /scratch \
  --mount type=bind,src="$src",dst=/source,readonly --mount type=volume,src=autobuild-wp-@N@,dst=/data \
  --env-file "$A/apps/@N@/database.env" --env-file "$A/apps/@N@/site.env" \
  -e HOME=/scratch -e TMPDIR=/scratch @RELAY@--label autobuild.site=@N@ --entrypoint /usr/local/bin/autobuild-wp-server \
  ` + WordPressServerImage + ` >/dev/null
echo started
`
	// wpBackupScript dumps the site's database and tars its uploads, in a
	// container of its own on the site's volume, so the site need not run. The
	// rows are counted in a copy restored from the dump, so they are the rows
	// it holds: MariaDB's counts cannot share the dump's snapshot (SERVER.md,
	// "Backups and restore").
	wpBackupScript = `set -eu
` + dockerHost + `
B="$HOME/autobuild/backups/@N@"
umask 077
mkdir -p "$B"
q() { printf '%s;\n' "$1" | ` + mariadbAdmin + ` >/dev/null; }
trap 'rm -f "$B/.@ID@.part" "$B/.@ID@.uploads.part"; q "DROP DATABASE IF EXISTS check_@DB@" || true' EXIT
docker exec autobuild-mariadb autobuild-mariadb dump @DB@ > "$B/.@ID@.part"
docker run --rm --user 33:33 --read-only --cap-drop ALL --security-opt no-new-privileges --network none \
  --mount type=volume,src=autobuild-wp-@N@,dst=/data --entrypoint /usr/local/bin/files ` + WordPressServerImage + ` backup > "$B/.@ID@.uploads.part"
q "DROP DATABASE IF EXISTS check_@DB@"
docker exec -i autobuild-mariadb autobuild-mariadb restore check_@DB@ < "$B/.@ID@.part" >/dev/null
counts=$(docker exec autobuild-mariadb autobuild-mariadb counts check_@DB@)
if [ -z "$counts" ] || [ ! -s "$B/.@ID@.part" ] || [ ! -s "$B/.@ID@.uploads.part" ]; then echo "the backup failed" >&2; exit 4; fi
mv "$B/.@ID@.part" "$B/@ID@.dump"
mv "$B/.@ID@.uploads.part" "$B/@ID@.uploads.tar"
wc -c < "$B/@ID@.dump"
wc -c < "$B/@ID@.uploads.tar"
printf '%s\n' "$counts"
`
	// wpRestoreCopyScript checks a backup's uploads can be read, restores its
	// dump into @COPY@, which no login reaches, and prints each table's rows
	// there.
	wpRestoreCopyScript = `set -eu
` + dockerHost + `
B="$HOME/autobuild/backups/@N@"
if [ ! -s "$B/@ID@.dump" ] || [ ! -s "$B/@ID@.uploads.tar" ]; then echo "the backup's files are not on the server" >&2; exit 4; fi
if ! tar -tf "$B/@ID@.uploads.tar" 2>/dev/null | grep -q '^uploads/'; then echo "the backup's uploads cannot be read" >&2; exit 4; fi
q() { printf '%s;\n' "$1" | ` + mariadbAdmin + ` >/dev/null; }
if [ "@KEEP@" != keep ]; then trap 'q "DROP DATABASE IF EXISTS @COPY@" || true' EXIT; fi
q "DROP DATABASE IF EXISTS @COPY@"
if ! docker exec -i autobuild-mariadb autobuild-mariadb restore @COPY@ < "$B/@ID@.dump" >/dev/null 2>&1; then
  q "DROP DATABASE IF EXISTS @COPY@" || true
  echo "the backup did not restore" >&2
  exit 4
fi
docker exec autobuild-mariadb autobuild-mariadb counts @COPY@
`
	// wpRestoreFilesScript puts a backup's uploads in place on the site's
	// volume, all at once, in a container of its own, while the site is
	// stopped; a tar that cannot be read changes nothing.
	wpRestoreFilesScript = `set -eu
` + dockerHost + `
U="$HOME/autobuild/backups/@N@/@ID@.uploads.tar"
if [ ! -s "$U" ]; then echo "the backup's uploads are not on the server" >&2; exit 4; fi
docker run --rm -i --user 33:33 --read-only --cap-drop ALL --security-opt no-new-privileges --network none \
  --mount type=volume,src=autobuild-wp-@N@,dst=/data --entrypoint /usr/local/bin/files ` + WordPressServerImage + ` restore < "$U"
`
	// wpDropCopyScript drops a copy a restore or a check made.
	wpDropCopyScript = `set -eu
` + dockerHost + `
printf 'DROP DATABASE IF EXISTS @COPY@;\n' | ` + mariadbAdmin + ` >/dev/null
echo dropped
`
	// wpDropCopiesScript drops what a restore set aside once the site answers.
	wpDropCopiesScript = `set -eu
` + dockerHost + `
` + mariadbAdmin + ` >/dev/null <<'SQL'
DROP DATABASE IF EXISTS previous_@DB@;
DROP DATABASE IF EXISTS restore_@DB@;
SQL
echo dropped
`
	// wpStopAppScript stops a WordPress website being taken off its target:
	// its container and network go; its database, volume and backups stay.
	wpStopAppScript = `set -eu
` + dockerHost + `
docker rm -f autobuild-app-@N@ >/dev/null 2>&1 || true
docker network disconnect -f autobuild-net-@N@ autobuild-frontdoor >/dev/null 2>&1 || true
docker network disconnect -f autobuild-net-@N@ autobuild-mariadb >/dev/null 2>&1 || true
docker network rm autobuild-net-@N@ >/dev/null 2>&1 || true
echo "stopped; its database, uploads and backups are kept"
`
	// wpDropDataScript drops a WordPress website's database (and any copy a
	// restore or check left), its login, database.env and site.env.
	wpDropDataScript = `set -eu
` + dockerHost + `
` + mariadbAdmin + ` >/dev/null <<'SQL'
DROP DATABASE IF EXISTS @DB@;
DROP DATABASE IF EXISTS check_@DB@;
DROP DATABASE IF EXISTS restore_@DB@;
DROP DATABASE IF EXISTS previous_@DB@;
DROP USER IF EXISTS '@DB@'@'%';
FLUSH PRIVILEGES;
SQL
rm -f "$HOME/autobuild/apps/@N@/database.env" "$HOME/autobuild/apps/@N@/site.env"
rmdir "$HOME/autobuild/apps/@N@" 2>/dev/null || true
echo "its database and login are dropped"
`
	// wpRemoveVolumeScript removes a WordPress website's volume: its uploads,
	// and the keys and salts its sign-ins are made with.
	wpRemoveVolumeScript = `set -eu
` + dockerHost + `
if docker volume inspect autobuild-wp-@N@ >/dev/null 2>&1; then docker volume rm autobuild-wp-@N@ >/dev/null; echo removed; else echo "not there"; fi
`
	// wpAdminPasswordScript prints the administrator's password once, as the
	// container's user; the site renames it before it reads it.
	wpAdminPasswordScript = `set -eu
` + dockerHost + `
exec docker exec autobuild-app-@N@ admin-password take
`
	// wpResetLinkScript reads the administrator's address on its input and has
	// the site send WordPress's own reset email to it.
	wpResetLinkScript = `set -eu
` + dockerHost + `
IFS= read -r email
case "$email" in *[!a-z0-9._%+@-]*|'') echo "no administrator's address" >&2; exit 3 ;; esac
case "$email" in ?*@?*) ;; *) echo "no administrator's address" >&2; exit 3 ;; esac
exec docker exec autobuild-app-@N@ reset-link "$email"
`
)

var (
	// wpSwitchDatabaseScript swaps the restored copy's tables in, keeping the
	// ones they replace in previous_@DB@.
	wpSwitchDatabaseScript = `set -eu
` + dockerHost + `
` + mariadbAdmin + ` >/dev/null <<'SQL'
` + mariadbSwap("@DB@", "previous_@DB@", "restore_@DB@") + `SQL
echo "switched; the tables it replaced are kept until the site answers"
`
	// wpUnRestoreScript stops the site and swaps the tables it had back.
	wpUnRestoreScript = `set -eu
` + dockerHost + `
docker rm -f autobuild-app-@N@ >/dev/null 2>&1 || true
` + mariadbAdmin + ` >/dev/null <<'SQL'
` + mariadbSwap("@DB@", "restore_@DB@", "previous_@DB@") + `DROP DATABASE restore_@DB@;
DROP DATABASE previous_@DB@;
SQL
echo "put back"
`
)

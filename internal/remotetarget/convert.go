package remotetarget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// A Node app replaced by another app's release, with its data converted for it
// (Repo C design note BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md, an app replaced by
// another). The plan carries the conversion, SQL its hash covers; applying it
// stops the app first, so nothing written after the backup is lost, backs the
// data up, and runs the SQL once on the app's own database as the app's own
// login (which can reach no other database), in one transaction: a failing
// conversion changes nothing and the app that ran is started again. Then the
// new release starts as an update does. When it never answers, the data comes
// back from the backup taken just before (restored into a copy, its rows
// checked, swapped in) and the release it replaced is started again.

// convertSteps are what replacing a Node app with a conversion does.
func convertSteps(files, frontdoor string) []string {
	return []string{files, "get_images", "start_database", "make_database", "stop_app", "back_up", "convert",
		"switch_site", "start_app", "wait_health", "write_site", frontdoor, "check"}
}

// validConversion is SQL a plan may run on an app's data: not empty, at most
// MaxConversionBytes of UTF-8 text, with no NUL and no backslash, so psql reads
// it as SQL only and runs no command of its own.
func validConversion(sql string) bool {
	return strings.TrimSpace(sql) != "" && len(sql) <= serviceapi.MaxConversionBytes && utf8.ValidString(sql) &&
		!strings.ContainsAny(sql, "\x00\\")
}

func conversionDigest(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:])
}

// convert runs a conversion on the app's database as its own login and answers
// the tables and rows it holds after.
func (m *Manager) convert(ctx context.Context, session remote, site, sql string) (string, error) {
	db := databaseName(site)
	if !validConversion(sql) || !siteNamePattern.MatchString(site) || !databasePattern.MatchString(db) {
		return "", errStep("the conversion is not valid")
	}
	out, err := run(ctx, session, fill(convertScript, "N", site, "DB", db), strings.NewReader(sql))
	if err != nil {
		return "", err
	}
	counts, err := parseCounts(out)
	if err != nil {
		return "", err
	}
	tables, rows := countTotals(counts)
	return fmt.Sprintf("converted; %d tables, %d rows", tables, rows), nil
}

// unConvert stops the app and puts back the data a conversion changed, from the
// backup taken just before it. It answers what it did.
func (m *Manager) unConvert(ctx context.Context, rec *operationRecord, key string, session remote) string {
	site := rec.Operation.Site
	db := databaseName(site)
	if _, err := run(ctx, session, fill(stopAppOnlyScript, "N", site), nil); err != nil {
		return "the app could not be stopped to put its data back"
	}
	m.mu.Lock()
	r, err := m.read(key)
	m.mu.Unlock()
	if err != nil {
		return "the data could not be put back"
	}
	d, _ := findDatabase(r, site)
	b, ok := findBackup(d, rec.Backup)
	if !ok {
		return "the data could not be put back: its backup is not kept"
	}
	if _, err := m.restoreCopy(ctx, session, key, site, b, "restore_"+db, true); err != nil {
		return "the data could not be put back: " + stepDetail(err)
	}
	if _, err := run(ctx, session, fill(switchDatabaseScript, "DB", db), nil); err != nil {
		return "the data could not be put back"
	}
	_, _ = run(ctx, session, fill(dropDatabaseScript, "COPY", "previous_"+db), nil)
	return "the data is put back as it was before"
}

// convertScript runs the conversion on its input as the app's own login over
// the Postgres's own loopback, in one transaction that stops at the first
// error, then counts every table's rows. The login's password goes from
// database.env through a file only the Postgres's user reads, removed after;
// no command line holds it. (@N@ a site name, @DB@ its database, both checked.)
const convertScript = `set -eu
` + dockerHost + `
A="$HOME/autobuild"
pw=$(sed -n 's|^DATABASE_URL=postgres://@DB@:\([0-9a-f]\{48\}\)@autobuild-postgres:5432/@DB@$|\1|p' "$A/apps/@N@/database.env")
if [ "${#pw}" -ne 48 ]; then echo "the app's login could not be read" >&2; exit 4; fi
P=/scratch/.convert-@DB@.pgpass
trap 'docker exec autobuild-postgres /bin/sh -c "rm -f $P" >/dev/null 2>&1 || true' EXIT
printf '127.0.0.1:5432:@DB@:@DB@:%s\n' "$pw" | docker exec -i autobuild-postgres /bin/sh -c "umask 077; cat > $P"
if ! err=$(docker exec -i -e PGPASSFILE=$P autobuild-postgres /usr/lib/postgresql/17/bin/psql -h 127.0.0.1 -U @DB@ -d @DB@ -X -q \
  -v ON_ERROR_STOP=1 --single-transaction -f - 2>&1 >/dev/null); then
  printf 'the conversion failed: %s\n' "$(printf '%s' "$err" | head -n 1 | cut -c1-160)" >&2
  exit 4
fi
docker exec -i autobuild-postgres ` + psqlBin + ` -d @DB@ -v ON_ERROR_STOP=1 -qAtX <<'SQL'
` + countRows + `;
SQL
`

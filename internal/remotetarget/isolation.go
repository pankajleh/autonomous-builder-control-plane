package remotetarget

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// CheckIsolation proves that two Node apps on a target are kept apart (BF-14
// proof P1b), as an operation: each app's own database login connects to its
// own database and is refused on the other's, neither may connect there at
// all, and neither app's container reaches the other's by name or by
// address. With Crash, the site's container is killed outright, the other app
// must still answer, and the site is started again and waits for /health.
// Nothing else is changed; the logins' passwords stay on the server.
func (m *Manager) CheckIsolation(_ context.Context, key string, request serviceapi.IsolationRequestV1) (serviceapi.OperationV1, error) {
	if !siteNamePattern.MatchString(request.Site) || !siteNamePattern.MatchString(request.Other) || request.Site == request.Other ||
		!requestIDPattern.MatchString(request.RequestID) {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.operationByRequest(key, request.RequestID); ok &&
		(existing.Operation.Kind != "isolation" || existing.Other != request.Other || existing.Crash != request.Crash) {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	r, existing, err := m.lifecycleTarget(key, "isolation", request.Site, request.RequestID)
	if err != nil || existing != nil {
		return deref(existing), err
	}
	for _, name := range []string{request.Site, request.Other} {
		s, ok := findSite(r, name)
		if !ok || siteKind(s) != kindNode || !served(s) {
			return serviceapi.OperationV1{}, serviceapi.ErrSiteUnknown
		}
		if s.State != siteLive {
			return serviceapi.OperationV1{}, serviceapi.ErrSiteStopped
		}
	}
	steps := []string{"own_login", "other_login", "login_back", "network"}
	if request.Crash {
		steps = append(steps, "crash", "other_answers", "start_again")
	}
	rec, err := m.startLifecycle(&r, "isolation", request.Site, request.RequestID, steps, "")
	if err != nil {
		return serviceapi.OperationV1{}, err
	}
	rec.Other, rec.Crash = request.Other, request.Crash
	if err := m.writeJSON(filepath.Join(m.dir, "operations"), rec.Operation.OperationID, rec); err != nil {
		delete(m.busy, r.Key)
		m.wg.Done()
		return serviceapi.OperationV1{}, err
	}
	go m.run(rec)
	return rec.Operation, nil
}

func (m *Manager) runIsolation(ctx context.Context, rec *operationRecord, _ record, session remote) string {
	site, other := rec.Operation.Site, rec.Other
	a, b := databaseName(site), databaseName(other)
	if !databasePattern.MatchString(a) || !databasePattern.MatchString(b) {
		m.markFirstPending(rec, "failed", "invalid site")
		return "invalid site"
	}
	// login tries from's login on to's database: what the server answered,
	// and whether the role may connect to that database at all.
	login := func(from, fromDB, toDB string) (string, bool, error) {
		out, err := run(ctx, session, fill(isolationLoginScript, "FROM", from, "FROMDB", fromDB, "TODB", toDB), nil)
		if err != nil {
			return "", false, err
		}
		said, may, _ := strings.Cut(lastLine(out), "|")
		return said, may == "t", nil
	}
	if err := m.step(rec, "own_login", func() (string, error) {
		said, may, err := login(site, a, a)
		if err != nil {
			return "", err
		}
		if said != "connects" || !may {
			return "", errStep(fmt.Sprintf("%s on its own database: %s", a, said))
		}
		return a + " connects to its own database", nil
	}); err != nil {
		return stepDetail(err)
	}
	refused := func(from, fromDB, toDB string) func() (string, error) {
		return func() (string, error) {
			said, may, err := login(from, fromDB, toDB)
			if err != nil {
				return "", err
			}
			if !strings.HasPrefix(said, "refused") || may {
				return "", errStep(fmt.Sprintf("%s on %s: %s; may connect: %t", fromDB, toDB, said, may))
			}
			return fmt.Sprintf("%s on %s: %s; %s may not connect to %s", fromDB, toDB, said, fromDB, toDB), nil
		}
	}
	if err := m.step(rec, "other_login", refused(site, a, b)); err != nil {
		return stepDetail(err)
	}
	if err := m.step(rec, "login_back", refused(other, b, a)); err != nil {
		return stepDetail(err)
	}
	if err := m.step(rec, "network", func() (string, error) {
		for _, pair := range [][2]string{{site, other}, {other, site}} {
			out, err := run(ctx, session, fill(isolationNetworkScript, "FROM", pair[0], "TO", pair[1]), nil)
			if err != nil {
				return "", err
			}
			if said := lastLine(out); said != "unreachable" {
				return "", errStep(fmt.Sprintf("%s reaches %s (%s)", pair[0], pair[1], strings.TrimPrefix(said, "reachable ")))
			}
		}
		return fmt.Sprintf("%s and %s cannot reach each other, by name or by address", site, other), nil
	}); err != nil {
		return stepDetail(err)
	}
	if !rec.Crash {
		return ""
	}
	if err := m.step(rec, "crash", func() (string, error) {
		out, err := run(ctx, session, fill(isolationCrashScript, "N", site), nil)
		return lastLine(out), err
	}); err != nil {
		// Whatever the kill did, the site is started again.
		m.restartAfterCrash(ctx, rec, session, site)
		return stepDetail(err)
	}
	failed := ""
	if err := m.step(rec, "other_answers", func() (string, error) {
		out, err := run(ctx, session, fill(waitHealthScript, "N", other, "WAIT", "10"), nil)
		if err != nil {
			return "", err
		}
		return other + " " + lastLine(out) + " /health while " + site + " is down", nil
	}); err != nil {
		failed = stepDetail(err)
	}
	if detail := m.restartAfterCrash(ctx, rec, session, site); failed == "" {
		failed = detail
	}
	return failed
}

// restartAfterCrash starts a killed app again and waits for its /health; it
// answers the step's failure, or "".
func (m *Manager) restartAfterCrash(ctx context.Context, rec *operationRecord, session remote, site string) string {
	if err := m.step(rec, "start_again", func() (string, error) {
		if _, err := run(ctx, session, fill(isolationStartScript, "N", site), nil); err != nil {
			return "", err
		}
		out, err := run(ctx, session, fill(waitHealthScript, "N", site, "WAIT", fmt.Sprint(m.healthWait)), nil)
		if err != nil {
			return "", err
		}
		return "started again; " + lastLine(out), nil
	}); err != nil {
		return stepDetail(err)
	}
	return ""
}

const (
	// isolationLoginScript reads one app's login from its database.env and
	// tries it, over TCP as an app connects, on a database; the password goes
	// on the Postgres container's input, never on a command line. It prints
	// what the server answered and whether the role may connect there.
	isolationLoginScript = `set -eu
` + dockerHost + `
f="$HOME/autobuild/apps/@FROM@/database.env"
may=$(docker exec autobuild-postgres ` + psql + ` -tAXc "SELECT has_database_privilege('@FROMDB@', '@TODB@', 'CONNECT')" 2>/dev/null || true)
if [ ! -s "$f" ]; then echo "no login|$may"; exit 0; fi
url=$(sed -n 's/^DATABASE_URL=//p' "$f")
pw=${url#postgres://@FROMDB@:}
pw=${pw%@autobuild-postgres:5432/@FROMDB@}
case "$pw" in ''|*[!a-f0-9]*) echo "no login|$may"; exit 0 ;; esac
out=$(printf '%s\n' "$pw" | docker exec -i autobuild-postgres /bin/sh -c 'IFS= read -r PGPASSWORD; export PGPASSWORD; exec /usr/lib/postgresql/17/bin/psql -h 127.0.0.1 -U @FROMDB@ -d @TODB@ -w -tAXqc "SELECT 1" 2>&1' || true)
case "$out" in
  1) said=connects ;;
  *"no pg_hba.conf entry"*) said="refused by the server's access rules" ;;
  *"permission denied for database"*) said="refused: no permission on that database" ;;
  *"password authentication failed"*) said="refused: its password" ;;
  *) said="no answer" ;;
esac
echo "$said|$may"
`
	// isolationNetworkScript tries, from one app's container, the other's by
	// its name and by each of its addresses.
	isolationNetworkScript = `set -eu
` + dockerHost + `
try() { if docker exec autobuild-app-@FROM@ /bin/busybox wget -T 3 -q -O /dev/null "http://$1:8080/health" >/dev/null 2>&1; then echo "reachable $1"; exit 0; fi; }
try autobuild-app-@TO@
for ip in $(docker inspect -f '{{json .NetworkSettings.Networks}}' autobuild-app-@TO@ | tr ',' '\n' | sed -n 's/.*"IPAddress":"\([0-9a-f.:]*\)".*/\1/p'); do
  try "$ip"
done
echo unreachable
`
	// isolationCrashScript kills an app's container outright, as a crash would.
	isolationCrashScript = `set -eu
` + dockerHost + `
docker kill --signal KILL autobuild-app-@N@ >/dev/null
sleep 2
if [ "$(docker inspect -f '{{.State.Running}}' autobuild-app-@N@ 2>/dev/null || true)" = true ]; then echo "killed; it came back by itself"; else echo "killed; it is down"; fi
`
	isolationStartScript = `set -eu
` + dockerHost + `
docker start autobuild-app-@N@ >/dev/null
echo started
`
)

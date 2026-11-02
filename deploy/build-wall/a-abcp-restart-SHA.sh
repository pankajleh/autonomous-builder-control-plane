#!/usr/bin/env bash
# Build isolation, phase 1 (Repo B #184; Repo C BUILD_ISOLATION.md §5.1): moves the live ABCP from 2cac026 to @SHA@, which
# starts every build inside bubblewrap in a folder and repository of its own (runtime/builds/<run>) and refuses every
# build while that wall cannot start. The same refusals as every restart, and two more: the wall file and the builds
# folder must be in place, and the new binary's probe of the wall must pass first. If @SHA@ does not listen, or its wall is
# not ready, 2cac026 starts again (decision 1A: everyone's apps keep building meanwhile). On success the staged boot
# entry replaces abcp-live-start.sh (kept as .pre-@SHA@).
# Usage: setsid nohup ./a-abcp-restart-@SHA@.sh > a-abcp-restart-@SHA@.out 2>&1 < /dev/null &
set -uo pipefail
R=/home/devagent/local-integration/repo-c-abcp/runtime
C=$R/abcp-config
N=/home/devagent/abcp-worktrees/live-b-@SHA@/abcp
TS=$(date -u +%Y%m%dT%H%M%SZ)
cd "$R"
say() { echo "$(date -u +%H:%M:%S) $*"; }
test -x "$N" || { say "REFUSED: the new binary is not built"; exit 1; }
[ "$(stat -c '%a %U' "$C/build-wall.json" 2>/dev/null)" = "600 devagent" ] || { say "REFUSED: $C/build-wall.json is not a 0600 file of devagent"; exit 1; }
[ "$(stat -c '%a %U' "$R/builds" 2>/dev/null)" = "700 devagent" ] || { say "REFUSED: $R/builds is not a 0700 folder of devagent"; exit 1; }
probe=$("$N" build-wall-probe --build-wall-file "$C/build-wall.json" --canary "$R/product-repo" --canary "$R/development-repo" \
  --canary "$R/service" --canary "$R/ledger" --canary "$R/evidence" --canary "$R/development-ledger" --canary "$R/development-evidence" 2>&1)
case "$probe" in "build wall ready "*) say "probe: $probe" ;; *) say "REFUSED: the wall's probe failed: $probe"; exit 1 ;; esac
[ -f abcp-live-start.sh.new ] && grep -q 'a-abcp-start-@SHA@.sh' abcp-live-start.sh.new || { say "REFUSED: the staged boot entry does not name @SHA@"; exit 1; }
if ps -eo args | grep "[r]alphex-v1.7.0-abcp" | grep -v -- --serve | grep -q .; then say "REFUSED: a build is running"; exit 1; fi
busy=$(python3 - <<'PY'
import json, pathlib
n = 0
for root in ('stack-jobs/jobs', 'change-jobs/jobs'):
    for f in pathlib.Path(root).glob('*.json'):
        try: state = json.loads(f.read_text())['job']['state']
        except Exception: continue
        n += state in ('QUEUED', 'RUNNING')
print(n)
PY
)
[ "$busy" = "0" ] || { say "REFUSED: $busy stack or change jobs are queued or running"; exit 1; }
recent=$(python3 - <<'PY'
import json, os, subprocess
from pathlib import Path
from urllib.parse import urlparse
live = json.loads(Path('secrets/live-repo-c.env.json').read_text())['api']
u = urlparse(live['SPRING_DATASOURCE_URL'].removeprefix('jdbc:'))
env = {**os.environ, 'PGPASSWORD': live['SPRING_DATASOURCE_PASSWORD']}
q = ("select (select count(*) from s01_abcp_admission_correlation where created_at > now() - interval '5 minutes')"
     " + (select count(*) from px_bought_server where state in ('starting', 'checking', 'removing'))")
r = subprocess.run(['docker', 'exec', '-i', '-e', 'PGPASSWORD', 'tier1-pg', 'psql', '-h', '127.0.0.1', '-U', live['SPRING_DATASOURCE_USERNAME'], '-d', u.path.lstrip('/'), '-qAt', '-c', q], env=env, capture_output=True, text=True)
print(r.stdout.strip() or 'unknown')
PY
)
[ "$recent" = "0" ] || { say "REFUSED: $recent build requests in the last 5 minutes or bought servers being made or removed (or unknown)"; exit 1; }
old=""
for p in $(pgrep -u devagent -f "live-b-2cac026/abcp serve"); do
  cmd=$(tr "\0" " " < /proc/$p/cmdline); [[ $cmd == *"--service-root $R/service "* ]] && old=$p
done
if [ -z "$old" ]; then say "REFUSED: the live ABCP was not found"; exit 1; fi
if [ -n "$(ps --ppid "$old" -o comm= | grep -x ssh)" ]; then say "REFUSED: ABCP $old has a server session open"; exit 1; fi
say "stopping ABCP $old (2cac026)"
kill -TERM "$old"
for i in $(seq 1 60); do kill -0 "$old" 2>/dev/null || break; sleep 1; done
if kill -0 "$old" 2>/dev/null; then say "FAILED: $old did not stop in 60 s"; exit 1; fi
say "stopped"
log="abcp-@SHA@-$TS.log"
ABCP_LOG="$log" setsid nohup ./a-abcp-start-@SHA@.sh > /dev/null 2>&1 < /dev/null &
say "started @SHA@ with log $log"
for i in $(seq 1 360); do ss -ltn | grep -q ':18888 ' && break; sleep 0.5; done
if ss -ltn | grep -q ':18888 ' && grep -q 'abcp build wall ready=true' "$log"; then
  cp -p abcp-live-start.sh abcp-live-start.sh.pre-@SHA@ && mv abcp-live-start.sh.new abcp-live-start.sh && chmod +x abcp-live-start.sh
  say "listening, every build walled; the boot script now starts @SHA@"
  grep -h "abcp build wall\|hosted instances limit\|server backups areas" "$log" | tail -3
  grep -h "preview profile id=" "$log" | grep -c "available=true" | sed 's/^/profiles available: /'
  pgrep -u devagent -f "live-b-@SHA@/abcp serve" | head -1 | sed 's/^/new PID /'
  say "DONE"
else
  say "FAILED: @SHA@ did not listen in 180 s or its wall is not ready ($(grep -h 'abcp build wall' "$log" | tail -1)); 2cac026 starts again"
  for p in $(pgrep -u devagent -f "live-b-@SHA@/abcp serve"); do kill -TERM "$p"; done
  for i in $(seq 1 60); do pgrep -u devagent -f "live-b-@SHA@/abcp serve" > /dev/null || break; sleep 1; done
  ABCP_LOG="abcp-2cac026-back-$TS.log" setsid nohup ./a-abcp-start-2cac026.sh > /dev/null 2>&1 < /dev/null &
  exit 1
fi

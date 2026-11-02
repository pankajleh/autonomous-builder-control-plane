# The build wall's own check (Repo C design note BUILD_ISOLATION.md §8.1, step 1), run by Ralphex as a validation step
# inside the wall of the proof build's profile (make-isolation-probe-profile.py puts it in the manifest template, with
# @CANARY@ set). Each line says BLOCKED or OPEN; any OPEN fails the step and so the build.
set -u
R=/home/devagent/local-integration/repo-c-abcp/runtime
canary=@CANARY@
status=0
blocked() { echo "BLOCKED $1"; }
open() { echo "OPEN $1"; status=1; }
see() { if [ -e "$2" ] || [ -L "$2" ]; then open "$1 ($2)"; else blocked "$1"; fi; }
see "another run's worktree" "$R/builds/admission-$canary/repo/canary.txt"
see "another run's folder" "$R/builds/admission-$canary"
see "the product repository" "$R/product-repo/.git"
see "the development repository" "$R/development-repo/.git"
# The build's own plan file is shown under product-repo/.abcp-input/<its run>, so that much of the path exists inside.
see "another run's plan" "$R/product-repo/.abcp-input/admission-$canary/plan.md"
see "the product repository's files" "$R/product-repo/README.md"
if git rev-parse --verify -q "abcp/admission-$canary" >/dev/null 2>&1; then open "the canary branch"; else blocked "the canary branch"; fi
if git cat-file -e "@CANARYBLOB@" 2>/dev/null; then open "the canary branch's object"; else blocked "the canary branch's object"; fi
see "the secrets" "$R/secrets"
see "the secret canary" "$R/secrets/isolation-canary"
see "ABCP's settings" "$R/abcp-config"
see "ABCP's ledger" "$R/ledger"
see "ABCP's evidence" "$R/evidence"
see "another engine home's sign-in" "/home/devagent/.codex/lanes/codex-api/auth.json"
see "the lane canary" "/home/devagent/.codex/lanes/codex-api/isolation-canary"
see "other runs' sessions" "/home/devagent/.codex/sessions/2026"
see "the session canary" "/home/devagent/.codex/sessions/isolation-canary"
see "the owner's Claude sign-in" "/home/devagent/.claude/.credentials.json"
see "the Docker socket" "/var/run/docker.sock"
see "the user's systemd" "/run/user/1000/systemd"
see "~/.ssh" "/home/devagent/.ssh"
see "~/.aws" "/home/devagent/.aws"
see "the apps' code histories" "/home/devagent/autobuild-code"
see "connected code" "/home/devagent/autobuild-vendor/change-sources"
if ps -eo comm= | grep -qx abcp; then open "ABCP's process"; else blocked "ABCP's process"; fi
if [ "$(ls /proc | grep -c '^[0-9]')" -gt 200 ]; then open "the host's processes"; else blocked "the host's processes"; fi
if crontab -l >/dev/null 2>&1; then open "crontab"; else blocked "crontab"; fi
if sudo -n true >/dev/null 2>&1; then open "sudo"; else blocked "sudo"; fi
if (touch /home/devagent/autobuild-vendor/templates/.isolation-probe) 2>/dev/null; then open "writing the templates"; rm -f /home/devagent/autobuild-vendor/templates/.isolation-probe; else blocked "writing the templates"; fi
if (touch /home/devagent/.nvm/.isolation-probe) 2>/dev/null; then open "writing ~/.nvm"; rm -f /home/devagent/.nvm/.isolation-probe; else blocked "writing ~/.nvm"; fi
exit $status

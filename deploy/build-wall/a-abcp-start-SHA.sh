#!/usr/bin/env bash
# Starts the live ABCP on the @SHA@ binary: Repo B #184 (every build walled by bubblewrap in a folder and repository of its
# own; Repo C BUILD_ISOLATION.md, phase 1) on top of 2cac026, with 2cac026's flags and --build-wall-file.
# A separate script, so the caller's command line never names the service root that start-abcp.sh looks for.
# ABCP_LOG names the log (start-abcp.sh truncates it): give each restart its own name so no earlier log is lost.
set -euo pipefail
cd /home/devagent/local-integration/repo-c-abcp/runtime
C=/home/devagent/local-integration/repo-c-abcp/runtime/abcp-config
exec ./start-abcp.sh /home/devagent/abcp-worktrees/live-b-@SHA@/abcp "${ABCP_LOG:-abcp-@SHA@.log}" --parallel-runs 3 \
  --build-wall-file $C/build-wall.json \
  --account-lanes-file $C/account-lanes.json \
  --targets-dir $C/targets \
  --site-relay-host siterelays.deployflix.app \
  --app-relay-url http://127.0.0.1:18093 \
  --app-relay-token-file $C/app-relay.token \
  --hosted-offsite-file $C/hosted-offsite.json \
  --server-backups-file $C/server-backups.json \
  --stack-profiles-file $C/stack-profiles.json \
  --stack-source-root /home/devagent/autobuild-vendor/stack-sources \
  --stack-root /home/devagent/local-integration/repo-c-abcp/runtime/stack-jobs \
  --stack-osv-root /home/devagent/autobuild-vendor/osv \
  --change-root /home/devagent/local-integration/repo-c-abcp/runtime/change-jobs \
  --change-source-root /home/devagent/autobuild-vendor/change-sources \
  --change-engine-file $C/change-engine.json \
  --hosted-max-running 20

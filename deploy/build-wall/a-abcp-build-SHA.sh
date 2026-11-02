#!/usr/bin/env bash
# Build isolation, phase 1 (Repo B #184; Repo C BUILD_ISOLATION.md §5.1): builds ABCP @SHA@ in a worktree of its own and
# runs its tests; the tests that start real walls (bubblewrap as devagent) and the real-Codex checks run in a second
# worktree, wall-test-@SHA@, never in the live one. Restarts nothing.
# Make it with: sed 's/@SHA@/<12-hex merge sha>/g; s/@FULLSHA@/<full merge sha>/g' a-abcp-build-SHA.sh > $R/a-abcp-build-<sha>.sh
# Usage: setsid nohup ./a-abcp-build-@SHA@.sh > a-abcp-build-@SHA@.out 2>&1 < /dev/null &
set -uo pipefail
B=/home/devagent/autonomous-builder-control-plane
W=/home/devagent/abcp-worktrees/live-b-@SHA@
T=/home/devagent/abcp-worktrees/wall-test-@SHA@
SHA=@FULLSHA@
CODEX=/home/devagent/.nvm/versions/node/v24.19.0/lib/node_modules/@openai/codex/node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex
export PATH=$PATH:/usr/local/go/bin
say() { echo "$(date -u +%H:%M:%S) $*"; }
git -C "$B" fetch -q origin main || { say "FAILED: fetch"; exit 1; }
git -C "$B" merge-base --is-ancestor "$SHA" origin/main || { say "FAILED: $SHA is not on main"; exit 1; }
say "origin/main is $(git -C "$B" rev-parse --short origin/main); building $SHA"
for dir in "$W" "$T"; do
  if [ ! -d "$dir" ]; then git -C "$B" worktree add --detach "$dir" "$SHA" || { say "FAILED: worktree $dir"; exit 1; }; fi
  [ "$(git -C "$dir" rev-parse HEAD)" = "$SHA" ] || { say "FAILED: $dir is not at $SHA"; exit 1; }
done
(cd "$W" && go build -trimpath -o abcp ./cmd/abcp > build.out 2>&1; echo "build exit=$?" >> build.out)
tail -1 "$W/build.out"
grep -q 'build exit=0' "$W/build.out" || { say "FAILED: build"; exit 1; }
(cd "$T" && go test ./internal/run/ ./internal/runadmission/ ./internal/activity/ ./internal/eviction/ ./internal/serviceapi/ ./internal/authority/ \
  ./internal/openmodel/ ./internal/preview/ ./internal/remotetarget/ ./internal/accountlane/ ./cmd/abcp/ > test.out 2>&1; echo "test exit=$?" >> test.out)
tail -12 "$T/test.out"
grep -q 'test exit=0' "$T/test.out" || { say "FAILED: tests"; exit 1; }
# The walls the tests start must really start here, as devagent under the host's AppArmor profile: none may skip.
(cd "$T" && go test -count=1 -v -run 'TestTheWallShows|TestTheProbe|TestAWalledRun|TestAResumedWalledRun|TestTheRunsRepository|TestBuildWallProbe|TestAFinishedWalledRun|TestRunMarks' \
  ./internal/run/ ./internal/activity/ ./internal/eviction/ ./cmd/abcp/ > wall.out 2>&1; echo "wall exit=$?" >> wall.out)
grep -E "^(--- (PASS|FAIL|SKIP))" "$T/wall.out"; tail -1 "$T/wall.out"
grep -q 'wall exit=0' "$T/wall.out" && ! grep -q -- "--- SKIP" "$T/wall.out" || { say "FAILED: the wall tests"; exit 1; }
# The real Codex through the relay, outside the wall as before and inside it now; it calls no provider.
(cd "$T" && ABCP_TEST_CODEX=$CODEX go test -count=1 -v -run TestCodexBuildsThrough ./internal/openmodel/ ./internal/run/ > codex-e2e.out 2>&1; echo "codex e2e exit=$?" >> codex-e2e.out)
grep -E "^(=== RUN|--- (PASS|FAIL|SKIP))" "$T/codex-e2e.out"; tail -1 "$T/codex-e2e.out"
grep -q 'codex e2e exit=0' "$T/codex-e2e.out" && ! grep -q -- "--- SKIP" "$T/codex-e2e.out" || { say "FAILED: the Codex end-to-end tests"; exit 1; }
say "done"

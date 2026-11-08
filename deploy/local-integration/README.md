# Local-integration operator configuration

This directory versions the non-secret ABCP operator configuration used by the live Repo C ↔ ABCP local-integration environment. Before 2026-09-28 it existed only on the integration host, with manual `.pre-*`/`.bak-*` copies as its only history.

The live copy remains what `abcp serve` reads, at `/home/devagent/local-integration/repo-c-abcp/runtime/abcp-config/`. Change it in two steps:

1. Change this directory in a PR.
2. After merge, copy the file to the host (mode `0600`, one hard link) and restart `abcp serve`, because profiles are read at startup.

Then run the drift check on the host:

```text
python3 tools/operator/check_local_integration_config.py [--live <dir>]
```

## Versioned files (`abcp-config/`)

| File | Purpose |
|---|---|
| `profiles.json` | Admission profiles, all 32 the live host reads: `repo-c-development-v1` (Repo C) and the product builds' `local-p02*`, one per platform and engine. The web-app and node-app ones (`local-p02`, `-claude`, `-codex-api`, `-claude-api`, `-lean`, `-codex-api-lean`, `-claude-api-lean`, `-node`, `-node-claude-api`, `-node-lean`, `-node-claude-api-lean`) name the template library's bare mirror, `template_mirror_path` (`docs/plans/template-copy.md`) |
| `manifest-template.json` | Demo-product governed manifest template; pins the Ralphex binary, executor policy, worktree retention and acceptance |
| `manifest-template-development.json` | Repo C development manifest template |
| `preview-profiles.json` | BP-02 preview profiles: `demo-v1` (demo product) and `web-v1` (Repo C) |
| `grants.json` | Authority grants for the `repo-c-service` principal: `preview.control`, `worktree.release` for product-requested worktree release (#53), `run.forget` for forgetting a deleted app's runs (Repo C `APP_DELETION.md` D1), `hosting.control` for hosted instances (#63, B9.2.1), and `lanes.control` for the key lanes' keys Repo C sends (`docs/plans/lane-keys.md`) |

Both preview profiles are static busybox `httpd` servers. `/bin/httpd` is the busybox path (`/usr/sbin/httpd` is the Alpine path and exits 127). The health path is `/README.md` because busybox `httpd` has no directory index. `web-v1` serves Repo C sources as static files only; a real web/API preview of Repo C needs an approved offline image and its own profile.

## Secret files (never versioned)

`token`, `cursor.json`, `preview-session-signing.hex`, `preview-gateway-service.token` and `workflow.json`, which holds the workflow-authority database connection. The drift check verifies only that they exist with owner-only permissions; it never reads them.

## Pinned Ralphex governance binary

Source: the private repository [`pankajleh/ralphex-governance`](https://github.com/pankajleh/ralphex-governance). Each pin is a short ABCP patch series on top of an upstream `umputun/ralphex` **release tag**. Its branches are named `abcp/<upstream-release>`, and the default branch is the current pin.

| Binary | Upstream base | Source | SHA-256 |
|---|---|---|---|
| `ralphex-v1.7.0-abcp2` (current) | release `v1.7.0` (`24c19b1`) | `abcp/v1.7.0` @ `c66debcd8353802851ee97f48b7bbd011eba7088` | `4b62e1d6b74f8ea6d5331f165b335185e33598e98eedc90503d89ed3750d26fe` |
| `ralphex-v1.7.0-abcp` (previous) | release `v1.7.0` (`24c19b1`) | `abcp/v1.7.0` @ `2275e23adba99bb22c23679ae3e8152c0323aba1` | `fe5a7c4651aa73af755f0aed10a82d9652a763a000f72b0646eaba6ae4894058` |
| `ralphex-governance-v2` (older) | master `319e306` (v1.6.1+34) | `abcp/governance-bundle-v2-20260928` @ `055dfbdb4d92120a8249ae58923d7608d583e82f` | `54300a18cb9a5ae5dc295f0f2b7f2e9441165e6b0aa56b4be52b18c225de94bc` |
| `ralphex-governance-v1` (earlier) | master `319e306` | `abcp/governance-bundle-v1-20260922` @ `66e8868173ffc982dbeab903663839d2276372c4` | `9698c1621fddf76038c38dec19c424c815b334d392d2211c74b556c1a8b48bcd` |

The patch series has four commits:

1. **ABCP governed execution budgets** (26 files, about +1,350/−220). It adds:
   - bounded iterations, session and idle timeouts, and internal review passes;
   - the validation spec;
   - session identity;
   - the orchestrator subprocess wait;
   - the `--abcp-governance-capability-v1` probe.
2. **`--keep-worktree`** (2 files, +55/−7). The finished run leaves its worktree for ABCP to remove, and the probe advertises `worktree_retention_v1`.
3. **Lint clean-up** (ralphex-governance #5; 4 files, no behaviour change). golangci-lint v2.13.0 passes on the branch, so CI is green.
4. **A review pass that commits a fix and says `REVIEW_DONE` is a fix, not a failed run** (ralphex-governance #4; 2 files). In bounded internal review, such a pass is validated like any fix and the runner's budget decides the next pass: with ABCP's budget of 2, a first-pass fix is reviewed by the final pass, and no review 3+ is launched (`docs/plans/review-pass-fixes.md`).

## Upgrading Ralphex

1. In the host clone `~/ralphex-behavior-lab/tools/ralphex`, run `git fetch origin --tags`. Create `abcp/<new-tag>` from the new release tag, then `git cherry-pick -x` the commits of the current branch, oldest first.
2. Resolve any conflicts. Then run `gofmt -l cmd pkg` (expect no output), `go vet ./...` and `go test ./...`.
3. Build with `cd cmd/ralphex && go build -trimpath -ldflags "-X main.revision=<commit> -s -w" -o ../../.bin/ralphex-<new-tag>-abcp .`, and check `--abcp-governance-capability-v1`. From a `git worktree` outside the clone, add `-buildvcs=false`: the revision comes from `-ldflags`.
4. Push the branch to `pankajleh/ralphex-governance` and make it the default branch.
5. Update the binary path, SHA-256 and source in both templates here, and in **every** `manifest-template*.json` on the host (`runtime/pin-ralphex.py`). Besides the two versioned here, the host holds one template per engine lane and kind of app (14 on 2026-10-02), and ABCP reads them all when it starts. Restart `abcp serve` (`runtime/start-abcp.sh`) with no run executing, run one governed proof run, and record it.

If `--keep-worktree` is accepted upstream, adjust the patch series once, at the first upgrade to a release that contains it:

- Drop commit 2.
- Move its one probe line, `WorktreeRetentionV1` / `worktree_retention_v1: true`, into commit 1, which owns the ABCP capability probe.
- If upstream names the flag differently, change the flag name that ABCP passes in `internal/ralphex/command.go`.

Nothing changes when the upstream PR merges: the live binary is pinned by hash until the next deliberate upgrade. Commit 1 is ABCP-specific, so this repository remains necessary unless upstream also accepts the governance budgets.

## Worktree eviction

`abcp serve` evicts finished runs' retained worktrees. The rules are 24 hours without a live activity stream (paged reads, such as Repo C's notification poller, do not count), 7 days after the run finished, or least recently used first when a repository exceeds 5 GiB. Unfinished runs, live previews and streaming clients protect a worktree (PR #46, `docs/plans/completed/bp02-worktree-eviction.md`). The defaults apply without flags. To override them:

```text
--worktree-eviction-interval 10m  --worktree-eviction-idle 24h
--worktree-eviction-max-age 168h  --worktree-eviction-quota-bytes 5368709120
```

A zero value disables a rule, and an interval of `0` disables eviction.

Each eviction does three things:

- keeps the `abcp/<run>` branch;
- pins every clean checkpoint at `refs/abcp/checkpoints/<run>/<sha>`;
- writes `<service-root>/evictions/<run>.json` (mode `0600`) and logs `abcp worktree evicted …` to the service log. Every sweep that finds retained worktrees also logs `abcp worktree eviction sweep …`, which counts why each worktree is kept.

A preview of an evicted run is still created from the pinned ref. Do not delete `refs/abcp/checkpoints/*` or the eviction records: without them, previews of evicted runs are refused.

### Product-requested release (#53)

Repo C releases a finished run's worktree immediately when a task is closed: `POST /v1/runs/{run}/worktree/release` with reason `task-closed`. A future tenant deletion can release a whole repository: `POST /v1/worktrees/release` with reason `tenant-deleted`. Both need the `worktree.release` grant. A run that is still executing is refused with `409 run_not_finished`.

Every eviction, whether by policy or by release, appears in the run's activity as a `WORKSPACE`/`RELEASED` event. Deploy the Repo C version that accepts this event before this ABCP version.

### Forgetting a run (Repo C `APP_DELETION.md` §4, D1)

When an app or an account is deleted, Repo C forgets each of the app's finished runs: `POST /v1/runs/{run}/forget` with `{"schema_version":1,"reason":"app-deleted"|"account-deleted","request_id":"<lowercase UUID>"}` (strict JSON, at most 1 KiB, no query). It needs the `run.forget` grant (anyone else `403 authority_denied`). An unknown run is `404 not_found`; a run not finished (one paused for a human decision included) is `409 run_not_finished`; a run on hold is `409 run_on_hold`. The answer is `200` `RunForgetV1`: `run_id`, `status` (`FORGOTTEN` for the request that sealed the forget and its replays, `ALREADY_FORGOTTEN` for any other), `reason`, `request_id` and `forgotten_at` of the sealing request, and `refs_removed` (each `ref` with its `sha`). The same `request_id` with another reason is `409 request_id_conflict`.

ABCP first writes `<service-root>/forgotten/<run>.json` (mode `0600`): the run, reason, request id, time, every ref to remove with its SHA, the run's paths, its engine session folders and the hash manifest of its evidence bodies. Then, apart from the request (a request that goes away does not stop it, and Git is asked to stop with SIGTERM, never killed half way, so it leaves no lock), it ends the run's previews and removes their sources (a preview asked for meanwhile is refused); removes its run folder and its worktree in the product repository (an exact edit keeps one there, though its manifest names a run folder), prunes the worktree list (unlocking a locked one whose folder is gone) and stops while Git still lists a worktree of the run; deletes `refs/heads/abcp/<run>` and every `refs/abcp/<kind>/<run>/…` with their reflogs, each only while it names the recorded SHA (a moved ref stops the forget); removes `.abcp-input/<run>/` and any plan copy, the progress logs whose header names the run's branch, the eviction record, every file under `evidence/<run>/` (the folder stays), the engine session folders the run's ledger names (`…/runs/<run>`) and its activity log; and appends `RUN_FORGOTTEN` (reason, request id and counts only) to its ledger. An interrupted forget is finished by the next call; the sweep and releases leave a forgotten run alone. Kept: the ledger, the registration, the admission receipts and the record. A forgotten run's activity, timeline, evidence, previews and worktree release answer `410 run_forgotten`; its detail and events still answer.

**A hold.** An operator holds a run by creating `<service-root>/holds/<run>` (any file; write the reason in it). A held run is refused with `409 run_on_hold`; removing the file lifts the hold. A forget already sealed is finished whatever is held after.

**The nightly clean-up.** `abcp serve` runs it at `--nightly-at` (UTC, default `23:17`), only for what is named: the product repository, and the engines' session folders (a Codex home's `sessions`, a Claude config folder's `projects`; nothing else can be named), each with the flag for who uses it.

```text
--git-gc-repository /home/devagent/local-integration/repo-c-abcp/runtime/product-repo
--session-logs-dir /home/devagent/.codex/sessions
--session-logs-dir /home/devagent/.claude/projects
--lane-session-logs-dir /home/devagent/local-integration/repo-c-abcp/runtime/abcp-config/lanes/codex-account-home/sessions
--lane-session-logs-dir /home/devagent/local-integration/repo-c-abcp/runtime/abcp-config/lanes/claude-account-home/projects
```

- **`--lane-session-logs-dir`, a build lane's own folder** (the account lanes' homes above, and any other lane whose file names its own `CODEX_HOME` or `CLAUDE_CONFIG_DIR`): no person uses it, so everything in it older than 30 days goes, a change job's sessions (which keep the person's change request) included, but a build's that has not finished (its `runs/<run>/`, its project folder, its `codex exec` logs) and `memory/`.
- **`--session-logs-dir`, a folder people use too**: the server's own `~/.codex/sessions` and the `projects` of the Claude config folder that builds on the server's own sign-in use (the wall file's home's `.claude`, `/home/devagent/.claude/projects`), where interactive sessions keep their transcripts and `memory/`. A lane whose file names no home of its own uses these. Only finished builds' logs older than 30 days go: `runs/<run>/` (a walled build's sessions), a Claude Code project folder named after a build's working folder (`…-worktrees-abcp-admission-<id>…`, from before builds were walled) and a Codex log in a dated folder that `codex exec` wrote in a build's working folder (`originator` `codex_exec`, `cwd` `…/worktrees/abcp/admission-<id>`). A person's own sessions (one started in a build's folder included) and `memory/` stay.

A folder named twice refuses the start. An open-model provider's home (`~/.codex/lanes/<provider>`, whose `runs/<run>/` a forget removes) cannot be named. Each repository gets `nice -n 19 ionice -c 3 -t git gc --quiet --prune=14.days.ago` under its execution lock (`abcp-ralphex-execution.lock`, waiting up to 30 minutes for a run's setup), so a forgotten run's objects go within 14 days of its refs while a running build's new objects are kept; a gc ABCP stops is asked to with SIGTERM, so it leaves no lock. In what a session folder's rule reaches, files older than 30 days go, and folders left empty that were unchanged for 30 days. A running build's folder is never walked; the walk opens each folder from its parent without following links, so a build cannot lead it outside its folder; a folder a finished build left unreadable in its `runs/<run>/` is made readable (0700) first; an entry it still cannot read or remove is skipped and counted, and the rest is swept. One line each in the service log: `abcp nightly git gc …` and `abcp nightly session logs folder=… policy=lane|builds ok=… removed=<n> skipped=<n>` (`class=failed` when an entry was skipped).

### A run that cannot be previewed after a moment's catalog wait (#190)

Before #190, when the runtime catalog could not give a run's record for a moment while a collector batch was checking the run's binding (its lock held past 2 s, as while Repo C restarts and reads every run at once), ABCP recorded the run's `Implementation detail integrity failure` marker, and every preview of the run was refused with `409 preview_ineligible` for ever. ABCP now leaves such a moment to the activity refresh, which waits up to a minute before it records anything, so no new marker of this kind is written. A marker recorded before stays in the run's activity; an operator who has checked its cause lets the run be previewed again with a review file. Nothing is restarted.

Write a review only when ABCP's own line for the marker names this step (the first command prints it; without it, stop), and only for that run. Run as `devagent`:

```sh
R=/home/devagent/local-integration/repo-c-abcp/runtime
RUN=admission-<the full run id>
grep -h "run=$RUN marker=provider-integrity step=revalidate-binding/registration " $R/abcp-*.log
ORDINAL=$(python3 -I - "$R/service/activity/$(printf %s "$RUN" | sha256sum | cut -c1-64).jsonl" <<'PY'
import json, sys
found = [r["event"]["ordinal"] for r in map(json.loads, open(sys.argv[1])) if "event" in r and r["event"]["title"] == "Implementation detail integrity failure" and r["event"]["source_session_id"] == ""]
assert len(found) == 1, found
print(found[0])
PY
)
(umask 077 && mkdir -p $R/service/marker-reviews && printf '{"kind":"ActivityMarkerReviewV1","schema_version":1,"run_id":"%s","marker":"provider-integrity","ordinal":%s,"step":"revalidate-binding/registration","reason":"%s"}\n' "$RUN" "$ORDINAL" "<why, e.g. catalog lock busy during the Repo C deploy of 2026-11-06>" > $R/service/marker-reviews/$RUN.json)
```

The preview resolver then reads `<service-root>/marker-reviews/<run>.json` (strict JSON; the folder `0700` and the file `0600`, both owned by the service user) and passes over that one marker, for checkpoints before and after it alike. It still checks the run's record, binding, manifest, capsule and plan, and the checkpoint in Git, as for any preview. A review naming another ordinal, run, marker or step, a review with no reason, and any other marker of the run still refuse the run. Removing the file refuses the run again.

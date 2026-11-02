# Build isolation, phase 1: every build in a wall, in a repository of its own

Date: 2026-11-02. Exact base: `2cac026` (#183). Status: **built (#184); not on the host until the switch-over in
`deploy/build-wall/README.md`** (one announced restart with no build running). Repo C design note
`BUILD_ISOLATION.md` (§5.1 the plan, §7 the decisions, §8.1 the proof).

The owner, 2026-11-02: "Isolation phase 1: go ahead", with every §7 decision as recommended: **1A** everyone's apps keep
building meanwhile; **2A** bubblewrap around Ralphex and a fresh repository per build; **3A** no build starts when the wall
cannot (`build_wall_unavailable`), and Repo C's guard holds builds. Decision 4 (the owner's Ralphex dashboard) is the
owner's and nothing here touches it.

## Why

Round 1 of the open-model trial found a build's agent reading other builds' worktrees and history in the shared product
repository and sending it to its provider (Repo C `OPEN_MODEL_ENGINES.md` §9.1). Ralphex runs every engine with its
sandbox off, as the user `devagent`, so every build could read and write everything that user can: other customers'
apps, plans and sessions, every secret, ABCP's records, the Docker socket. Repo C §2 has what was checked.

## What a build sees

`abcp run` starts Ralphex as `bwrap … -- ralphex …` (`internal/run/wall.go`, `walled.go`), for every profile, engine and
kind; the argv is built by `Wall.Args` and nothing in a manifest turns it off. Every folder keeps its own path inside.

| Inside the wall | From | How |
|---|---|---|
| `/usr`, `/etc`, `/run/systemd/resolve`; `/bin`, `/lib`, `/lib64`, `/sbin` as links into `/usr` | the host | read-only (`system`) |
| `/proc`, `/dev` | the wall's own | its own PID namespace; a minimal `/dev` |
| `/tmp` | `<run>/tmp` | read-write |
| `/home/devagent` | `<run>/home`, empty at the start | read-write |
| `~/.nvm` (Node, Codex, Claude Code), the browsers, corepack, pnpm's store | the host | read-only (`tools`) |
| Autobuild's templates, mirror, decoration library, forms kit, kits | the host | read-only (`libraries`) |
| `<builds root>/<run>/`: the run's repository, worktree, home, `/tmp`, engine home | new for the run | read-write |
| the plan file and the context capsule file (not their folders) | `product-repo/.abcp-input/<run>/` | read-only |
| the pinned Ralphex binary | the manifest's `binary_path` | read-only |
| the app's own folders its admission names (`read_paths`) | under the wall file's `read_roots`, each naming the app's task ID unless its root is shared | read-only |
| ABCP's Ralphex settings for the run (`/tmp/abcp-ralphex-config-…`) | the host | the folder read-write (Ralphex adds its defaults there at start), every file ABCP wrote in it read-only |
| the engine's one sign-in file | the server's or the lane's `auth.json` / `.credentials.json`, when there is one | read-write, so a refresh is kept |
| the engine's session folder | `<the folder the meter reads>/runs/<run>/` | read-write |
| the network | the host's | unchanged until phase 2 |

Namespaces: `--unshare-pid --unshare-ipc --unshare-uts --unshare-cgroup-try --new-session --die-with-parent --cap-drop
ALL` (and the user and mount namespaces bubblewrap makes). Nothing else exists inside: no `runtime/secrets`, no
`abcp-config`, no ledger or evidence, no other run's folder, no product or development repository (only the skeleton of
folders above the plan file), no `~/.codex`, `~/.claude`, `~/.ssh`, `~/.aws`, `~/.gitconfig`, no Docker socket, no
`/run/user/1000`, no other process. The environment is today's filtered one (`ralphexEnvironment`) with `HOME`, `TMPDIR`
and the engine's home the run's own and `XDG_CACHE_HOME`/`XDG_CONFIG_HOME` left out; it is bubblewrap's own environment,
so no value is ever in an argument or the evidence. A V3 governed invocation and a run without a worktree are refused
under a wall in phase 1 (no admitted profile is either).

## The run's folder and repository

Under the setup lease, after the authority is validated against the product repository as before
(`prepareWalledRun`): `<builds root>/<run>/` (0700) is made, and in it `repo` with `git -c protocol.file.allow=always
clone --no-local --no-hardlinks --no-tags --single-branch --branch <the product repository's branch>` (never `--local`,
`--shared` or `--reference`, each of which hands the new repository every object of the old one). ABCP checks `HEAD` is
the start commit, that `rev-list --all` is the start commit's history and nothing else, that no `alternates` file
exists, removes the `origin` remote, and sets `user.name`/`user.email` from the product repository (the build has no
`~/.gitconfig`). Then `home`, `tmp` and `engine-home`. The setup lease is let go before Ralphex starts: the product
repository is needed again only to bring the branch back. A fresh run never finds its folder there already; a resumed run
(`--resume`) must find it, and runs no Git command in it.

The plan copy Ralphex commits on its branch (product plans are Git-ignored) is written into the run's repository
(`abcp-ralphex-plan-<run digest>/plan.md`) and removed when Ralphex exits, never through a link. Ralphex makes its
worktree at `<run>/repo/.ralphex/worktrees/abcp/admission-<id>`, which still ends as the meter's pattern expects, and its
progress in `<run>/repo/.ralphex/progress/`.

**The branch brought back** (`bringBack`): once the run's wall has started, and before Ralphex does, ABCP makes
`refs/heads/abcp/<run>` in the product repository at the start commit, under the setup lease (`startBranch`; a resumed
run's is there already), so every fetch back is a fast-forward of something: Git takes a branch the destination does not
have as it is. Then every 2 seconds while Ralphex runs and once after it exits, ABCP asks the run's repository for
`refs/heads/abcp/<run>` (`git ls-remote`) and, when it moved, fetches it into the product repository under the setup
lease, `refs/heads/abcp/<run>:refs/heads/abcp/<run>`, which Git does fast-forward only. A branch that cannot
fast-forward was rewritten: any refused fetch, even one the build later "puts right", fails the run with
`BRANCH_REWRITTEN` once Ralphex exits (`bringBackFinally`), and the product repository keeps what it had. Acceptance,
previews, Repo C's versions and saves to GitHub read the product repository as before.

Once Ralphex has run, the run's repository is the build's to write. ABCP only fetches from it (upload-pack, which Git
makes safe on a repository it does not trust); it never runs another Git command there outside a wall (below).

## Engine homes

Each engine gets a home of its own, `<run>/engine-home`, named by `CODEX_HOME` or `CLAUDE_CONFIG_DIR`
(`prepareEngine`). Its sessions are bound from a `runs/<run>` folder inside the folder Repo C's meter already reads, so
`UsageCollector.scan` (six levels) and `SessionLogs.RUN` (`/worktrees/abcp/admission-<id>` in the session's working
folder) read them as before.

| Engine | Settings in the run's home | Sign-in bound | Sessions bound from (the meter's folder, levels to a log) |
|---|---|---|---|
| the server's Codex | `~/.codex/config.toml` without its `[projects.*]` tables (they name other builds' folders), the run's repository trusted | `~/.codex/auth.json` | `~/.codex/sessions/runs/<run>` (`~/.codex/sessions`: runs/run/YYYY/MM/DD/file, 6) |
| a Codex lane (`codex-api`, `codex-account`) | the lane's `config.toml`, the same way | the lane's `auth.json` | `<lane CODEX_HOME>/sessions/runs/<run>` (6) |
| an open model (`together`) | `config.toml` and the model's catalog, written by ABCP (`Start.UseHome`) | none (the key is in the environment, as before) | `<provider codex_home>/runs/<run>` (`~/.codex/lanes/together`: 6) |
| Claude Code (server, `claude-api`, `claude-account`) | `settings.json` | `.credentials.json` when there is one | `<config dir>/projects/runs/<run>` (4 or 5) |

## The open-model relay

The relay stays where #183 put it: in `abcp run`, on the host's 127.0.0.1, started before Ralphex. Phase 1 keeps the
host's network namespace, so 127.0.0.1 inside the wall is the host's and Codex reaches the relay at the address its
`config.toml` names, which is now written in the run's own home. The relay's log stays in the model's slot home, which
the build is never shown. Proved with the real Codex (`TestCodexBuildsThroughTheRelayInsideTheWall`, with
`ABCP_TEST_CODEX`): Codex in a wall, through the relay to a fake provider, three turns, `apply_patch` writing the file,
its command seeing only the run's home and no Docker socket, its session in `<provider folder>/runs/<run>`. Phase 2
serves the relay into the wall's own network at the same address.

## ABCP's own reads of a walled run

The run's repository and progress are the build's to write, so ABCP reading them on the host would let a build run a
program there: Git runs a repository's clean filters (named in its `.git/config` and `.gitattributes`) when it hashes
worktree files, which `git status` and `update-index --really-refresh` do, and the activity checkpoint observer runs
both. `TestAWalledRunsWorktreeIsReadInsideAWallAndItsCheckpointWaitsForTheProductBranch` shows a filter planted by the
build running on the host when read unwalled. So:

- **Git reads** of a walled run's repository and worktree (activity's worktree resolution, checkpoint, history check;
  eviction's worktree lookup) run inside an **observer wall** (`Wall.ObserverArgs`): the read-only system, the run's
  folder read-only, the private index folder read-write, no network, nothing else; `GIT_OPTIONAL_LOCKS=0`. Without the
  wall (`activity.UseObserverWall`, set by `abcp serve` from the wall file) a walled run's detail is unavailable, never
  read unwalled.
- **The progress view** (the Ralphex `--serve --watch` sidecar) of a walled run runs in an observer wall with the
  host's loopback (ABCP reads it there), the run's folder and the pinned binary read-only, so a link in the progress
  folder leads nowhere. Its listener is proved on the started process or its descendants.
- A walled run's **checkpoint** is offered once its commit is on the run's branch in the product repository (brought
  back), where previews read it.
- **Eviction** pins the checkpoints in the product repository as before and removes the run's folder whole
  (`removeRunFolder`: never through a link; a folder the build left unwritable is opened without following links and
  made writable first). A finished walled run whose folder holds no worktree (it failed before Ralphex made one, at its
  wall, its engine or an early exit, or its profile keeps none) has its folder removed by the sweep the same way
  (`removeFolderWithoutWorktree`, logged `abcp run folder removed run=… reason=no-worktree`), and so does Repo C's release
  of it; an unfinished one is kept for its resume. A walled run whose folder is gone (evicted) is nothing to sweep, not
  "unverified".
- **Files abcp run is given**: `abcp serve` hands each `abcp run` its launch lock as file 3. `abcp run` marks every file
  it was given above stderr close-on-exec before it starts anything (`CloseInheritedFilesOnExec`), and keeps its own
  copy, so the lock is held while it runs and nothing inside the wall has a file it did not open itself.

## Settings and the start probe

`abcp serve --build-wall-file <0600 file>` and `abcp run --build-wall-file <file> --build-wall-sha256 <hex>` (which `abcp
serve` passes to every run it launches or resumes, with the SHA-256 of the file it loaded and probes). `abcp run` refuses
to start without both, and a run whose wall file is not the one probed (it changed on disk since) fails before Ralphex
starts with `build_wall_unavailable`. `abcp serve` checks the file on disk against what it probed at every admission and
resume and in every probe: a changed file holds every build (503 `build_wall_unavailable`, no `build_isolation`) until
`abcp serve` is restarted. The file (`BuildWallFileV1`; the host's is `deploy/build-wall/build-wall.json`):

| Field | |
|---|---|
| `bwrap` | the bubblewrap binary |
| `builds_root` | where every run's folder is (0700, made when missing) |
| `home` | the controller user's home: a build's own home is shown there |
| `system`, `tools`, `libraries` | shown read-only to every build |
| `read_roots` | the roots an app's own folders may come from, each `{"path": …}` (kept per app) or `{"path": …, "shared": true}` |
| `canaries` | places the probe must not find inside a wall |

Every path is absolute, clean and real (no link anywhere in it); the builds root overlaps nothing shown; a read root is
inside nothing shown and overlaps no other read root; a canary is inside nothing shown. A file that does not check is
refused with its reason.

**The start probe** (`Wall.Probe`): a probe run folder and another run's canary folder are made in the builds root, a
wall like a build's is started with a fixed script, and every canary must be absent (`BLOCKED`), ABCP's own process
unseen, and the probe's own folder writable. Canaries: the wall file's; the home's `.codex`, `.claude`, `.claude.json`,
`.ssh`, `.aws`, `.config/gh`, `.gitconfig`; the Docker sockets; `/run/user/<uid>`; the other run's folder; and from
`abcp serve`, the service root, the profile file and each profile's repository, ledger, evidence, template, workflow
settings, lane file and open-model key and homes. It runs at start, again before the first build after it, then
whenever a passed probe is ten minutes old (a failed one is retried after a minute). While it fails, or with no wall
file, **every admission and resume answers 503 `build_wall_unavailable`**, `GET /v1/engines` says every profile `ready:
false` with `reason: "build_wall_unavailable"` and no `build_isolation`, and `GET /v1/capabilities` omits
`build_isolation`; while it passes both say `"build_isolation": "bwrap-v1"`. A run whose own wall does not start
(`Wall.Check`, `/bin/true` in the run's wall) fails before Ralphex starts, with bubblewrap's words. Nothing builds
unwalled instead. `abcp build-wall-probe --build-wall-file <file> [--canary <path>]…` runs the probe alone, for the
switch-over. `abcp serve` logs `abcp build wall ready=true kind=bwrap-v1 …` or `ready=false reason=…`.

## Admission

`POST /v1/runs` may carry `read_paths`: at most 16, each a distinct absolute clean path; the controller accepts each only
as a real folder (no link anywhere in its path) strictly inside one of the wall file's `read_roots` and, unless that root
is `shared`, naming the request's `product_task_id` (which must then be a canonical UUID, as Repo C sends every one:
a word such as `tasks` would make `imports/tasks` look like one app's folder; a request whose task ID is none is shown
no read path at all) as a whole component where a root keeps an app's folder:
`<root>/<task>/…`, or `<root>/<kind>/<task>/…` under a plain lowercase folder (`tasks`, `changes`, `imports`). So never
another app's folder, a folder holding every app's (`imports/tasks`, `imports/pending`), a root, or a folder named after
the app inside another app's; else 422 `read_path_not_accepted`. The task markdown carries the customer's own words, and
Repo C finds the paths in it, so this is the check that holds even if Repo C's were wrong. The host's shared root is
`brands`, which Autobuild keeps per person (and their shared phone fonts), a key ABCP is not told: there Repo C alone
sends only the owner's own folder. Part of the request's digest only when given, so every earlier request replays as it
did. Every admitted manifest gets `isolation: {run_folder: <builds root>/<run>, read_paths: […], product_task_id: …}`
(read paths and the task only when given; omitted from manifests admitted before, whose digests stay); the binding pins
the manifest by its digest. The run checks both again against its wall file before its wall starts; a run whose manifest
names no run folder (admitted before the wall) is refused, never walled into a folder of its own choosing.

## Evidence

`EXECUTION_STARTING` carries `isolation`: `kind` (`bwrap-v1`), `bwrap`, `bwrap_version`, `bwrap_sha256`,
`wall_file_sha256`, `binds_sha256` (of the wall's arguments), `binds`, `namespaces`, `network: "host"`,
`capabilities: "none"`, `run_folder`, `repository`, `read_paths` (a count), `engine_home`, `sessions`, `sign_in` (the
bound file's name). Never a value. `argv` stays Ralphex's own; the process evidence's argv is the whole `bwrap … --
ralphex …`.

## What changes for what already works

| What | With the wall |
|---|---|
| **Previews, Try it** | read the product repository as before; a build's checkpoint is offered once its commit is brought back (within about 2 seconds of the commit) |
| **Activity** | the run's worktree and progress are in its own repository, read inside an observer wall; one progress sidecar per walled run while it is streamed (before, one per repository) |
| **The meter** | sessions land in `<the meter's folder>/runs/<run>/`, within its six levels; the working folder still ends in `/worktrees/abcp/admission-<id>`. Repo C unchanged |
| **Resume** | `--resume` finds the run's folder and repository on disk; a run admitted before the wall cannot be resumed under it (refused, fail closed) |
| **Eviction** | the run's folder goes whole; its quota counts the folder (repository, home, caches) |
| **The relay** | unchanged, in `abcp run` on the host's loopback; Codex's home, with the relay's address, is the run's own |
| **Caches** | `~/.npm` and the like are each build's own, in its folder, gone with it; corepack and pnpm's store read-only |
| **Change builds** | see their earlier version only when Repo C names it in `read_paths` |
| **The owner's Ralphex dashboard** | watches `product-repo/.ralphex/progress`, so it no longer sees new builds' progress (each is in its run's folder); decision 4 untouched |
| **Exact edits, stack, change and scan jobs** | unchanged (no agent, or walled already) |
| **Rollback to 2cac026** | builds unwalled again; 2cac026 cannot read walled runs' manifests (`isolation`), so their activity and checkpoint previews are unavailable under it; their branches are in the product repository |

## Left open (phase 2 and later)

- **The network**: the internet, every loopback port, the tailnet, the owner's dashboard on 18096.
- **The engine's own sign-in or key**: the bound `auth.json` (the codex-api lane's holds its key), `.credentials.json`,
  the key in the environment.
- **Acceptance runs on the host, unwalled**: ABCP's acceptance commands run in a checkout of the build's branch as
  `devagent`, so a build's code that acceptance runs (a `package.json` script, a test) runs outside the wall. Repo C §5.2
  step 3 walls it.
- **The Ralphex settings folder** is writable by the build (ABCP's files in it are not).
- **The shared kernel**; `devagent` in `sudo` (no setuid program works in the wall: `no_new_privs`).
- **A Claude sign-in refresh** that replaces `.credentials.json` by renaming fails on the bound file (`EBUSY`); Codex
  writes `auth.json` in place. To be seen in the proof build on the server's Claude.
- **Brands' folders** are checked by ABCP only as under the shared `brands` root: which person's is the app's is Repo C's
  to decide (from the database, never the spec).

## Tests

- `internal/run/wall_test.go`: the wall's exact arguments; links, missing paths, foreign run folders, writable mounts
  outside the run, relative or root targets refused; the wall file's checks (schema, missing bwrap, overlaps, canaries
  inside what is shown, a readable file); a wall that cannot start fails its probe and its check; the engine settings
  without `[projects.*]`; the walled environment.
- `internal/run/wall_linux_test.go` (real bubblewrap; skipped where it cannot start a wall): a walled run whose fake
  agent tries another run's folder and canary, a canary secret and its folder, `~/.ssh`, the home's `.codex`, another
  run's plan, the product repository's files and `.git`, a Unix socket like Docker's and Docker's own path, the user's
  systemd, the wall file, ABCP's ledger and evidence, the canary branch and its object, ABCP's process, writing in its
  app's folder and in the tools: every one `BLOCKED`; and its own home, `/tmp`, repository, read path, sign-in, session
  folder and PID namespace work; the branch is brought back, the product repository lists no worktree, the run's
  repository holds the start commit's history only, no remote, no alternates; the session is under `runs/<run>`; the
  start's evidence names the wall. The fail-closed path (a wall that cannot start, a missing wall file, a manifest
  without a run folder: FAILED `build_wall_unavailable`, Ralphex never started, no branch). The probe finds no canary in
  a real wall and refuses one that shows a canary. The fresh clone and the fetch-back (fast-forward, a rewrite refused, a
  linked repository not read); resume finds the folder, a fresh run never reuses one; a run whose branch is rewritten
  fails `BRANCH_REWRITTEN`, and so does one whose branch is rewritten and put back, and one whose branch holds a
  history unrelated to the start commit (the product repository's branch, made at the start commit before Ralphex,
  stays there). No file above stderr is open inside the wall (`ls /proc/self/fd` in the agent: `0 1 2 3`, the last its
  own), with a file given to the test process as abcp serve gives the launch lock, marked close-on-exec as `abcp run`
  does (without the mark the same test sees it: `0 1 2 3 77`). A wall file whose digest is not the probed one fails the
  run before Ralphex starts.
- `internal/run/codex_wall_linux_test.go`: the real Codex in a wall through the relay (with `ABCP_TEST_CODEX`).
- `internal/activity/walled_linux_test.go`: a walled run's worktree is never read without the observer wall; inside it a
  clean filter the build planted does not run on the host (the same read unwalled does run it); a checkpoint waits for
  the product branch.
- `internal/eviction/walled_linux_test.go`: a run's folder removed whole, never through a link; a finished walled run's
  folder with no worktree (no repository, or a repository without one, read in a real observer wall) removed by the
  sweep whether its profile retains worktrees or not, and a sweep after it counts nothing; an unfinished one kept; one
  with its worktree left to the policy; Repo C's release of one removes its folder, and a release again answers no
  worktree.
- `internal/runadmission/wall_linux_test.go`: no admission or resume while the wall fails or without a wall file; every
  engine `build_wall_unavailable`; the first build after the start probes again; `read_paths` outside the roots, a link,
  missing, the root, another app's folder or its whole folder, every app's `tasks`, one's own with another's refused;
  accepted ones pinned in the manifest with the app's task ID; the digest unchanged without them. A wall file changed
  since it was probed holds every build and fails the probe; put back, builds are admitted again; the run is passed
  the probed digest.
- `internal/serviceapi`: `read_paths` validation, `build_wall_unavailable` (503) and `read_path_not_accepted` (422),
  `build_isolation` in the capabilities. `cmd/abcp`: `build-wall-probe`; `abcp run` names the wall file and its digest;
  `abcp run` marks a file it was given close-on-exec first, even when it then refuses its arguments.
- `internal/run/wall_test.go`, read paths: kept per app, the app's own (`<task>`, `tasks/<task>`) accepted and a shared
  root's folder accepted; another app's folder, its whole folder, every app's `tasks`, every person's `pending`, a
  person's import, the app's ID inside another app's folder or deep in its files, a shared root itself, an app ID that is
  a prefix, a path or none refused.
- `go test ./...`: every package passes (in a container as root, `internal/mergelifecycle`'s M02/M03 fail as they do on
  `main` there).
- **Run on the server** (2026-11-02, as `devagent`, in `abcp-worktrees/wall-test-d11e474`, this change on `2cac026`, the
  live ABCP untouched), for the first version and again for the version after review (its Go code checked identical to
  this commit's by digest): `go build ./...`, `go vet ./...` and `go test -count=1 ./...` pass, all 44 packages (Go
  1.26.8); every real-wall test above passes with bubblewrap 0.9.0 under `/etc/apparmor.d/usr.bin.bwrap`, none
  skipped; with `ABCP_TEST_CODEX` (Codex 0.163.0), `internal/openmodel`'s two tests and
  `TestCodexBuildsThroughTheRelayInsideTheWall` pass. `abcp build-wall-probe` with `deploy/build-wall/build-wall.json`
  (only its `builds_root` moved into the test worktree) and the product repository, development repository, service,
  ledger and evidence as canaries answers `build wall ready kind=bwrap-v1`; a copy naming a missing bwrap answers
  `build_wall_unavailable`.

## After review (2026-11-02)

An independent review of the first version found five problems, each fixed here and tested above:
1. **Another customer's folders through `read_paths`** (an escape): Repo C sent any folder its spec named under a per-app
   root, and the spec carries the customer's own words; ABCP accepted any real folder under a root. Now Repo C sends
   only folders cut at the app's own task ID (or, for brands, the owner's user ID from the database, and the shared
   phone fonts), and ABCP accepts only folders naming the request's `product_task_id` where a root keeps an app's
   folder, unless the root is marked `shared` in the wall file (Admission).
2. **The launch lock reached inside the wall** (file 3, read-write, through `/proc/self/fd/3`): `abcp run` marks every
   file it was given close-on-exec before it starts anything.
3. **A run folder with no worktree was never removed** (a run failing before Ralphex made one kept its clone and caches
   for ever): the sweep removes a finished walled run's folder that holds no worktree.
4. **The fast-forward rule had no starting point, and a recorded rewrite was ignored**: the branch is made in the
   product repository at the start commit before Ralphex starts, and any refused fetch fails the run.
5. **The probe did not test the file each run uses**: `abcp run` is passed the probed file's SHA-256 and refuses another;
   `abcp serve` holds builds while the file on disk is not the one it probed.

The re-review confirmed the five and asked for one hardening: a task ID made of lowercase letters (`tasks`) would have
matched the folder holding every app's. Read paths now need the request's `product_task_id` in canonical UUID form, at
admission, in the manifest's `isolation` and in the run's own check (`TestIsolationReadPathsNameTheirAppByItsCanonicalUUID`,
and `tasks` as the task ID in the wall's and the admission's read-path tests).

## Rollout

`deploy/build-wall/README.md`: build and test (the wall tests and the real Codex in a separate worktree), the wall file,
the proof build's profiles and canaries, an announced restart with no build running (rolled back to 2cac026 if the wall
is not ready), the proof builds (Repo C §8.1), then Repo C's guard on.

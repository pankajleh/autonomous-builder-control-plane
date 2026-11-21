# Lane keys: a key entered on the page reaches builds

Status: **#198 live since 2026-11-08 02:16 UTC** (provider key lanes); **engine lanes live since 2026-11-19 03:15 UTC**
(#205, ABCP `04a8f6f`, below); **the change engine's lane built, not live** (below). One quiet restart takes it live, with `lanes.control` added to `grants.json`; no template
changes. Repo C design note `BUILD_ENGINE_RULES.md` §9, slice 5 (Repo B's half). Repo C wires the page's "Add key" to it
afterwards.

## Why

The owner, 2026-11-07: "the engine and 'If it can't take the build' fields should be driven by what we have in the 'API
keys' section … admin can add keys/sign ins for the ones he would like, right now the rule section still seems it is
being driven from a mix of UI and server keys both." Then "Yes, build it", which covers this work's Repo B merges and
quiet restarts.

Until now an open-model lane (Together; Groq once #192 is live) read its provider key from a file on the build host that
the operator wrote by hand (`docs/plans/open-model-engines.md`, "What a manifest names": `key_file`). A key entered on
Repo C's AI and API keys page never reached a build.

## Where each lane's key is today

A **key lane** is an open-model provider a build profile's manifest template names in `executor.provider`. Its key file is
that template's `executor.provider.key_file`. On the host (read 2026-11-07, names and modes only):

| Lane | Key file | Profiles that read it |
|---|---|---|
| `together` | `runtime/secrets/together-api-key` (0600, ABCP's user) | `local-p02-together`, `local-p02-node-together` |
| `groq` | `runtime/secrets/groq-api-key` (0600, ABCP's user) | none yet: its two templates are on the host, but `profiles.json` names them only once #192 goes live |

`runtime/secrets/` is 0700, ABCP's user. Each build on the lane reads the file when it starts (`openmodel.Begin` →
`ReadKey`: a regular file of the controller's user, 0600 or 0400, no link anywhere in its path, one line of 16 to 512
printable characters) and hands the key to Codex as `<ID>_API_KEY` only. `GET /v1/engines` reads it on each call for the
lane's `ready` (`lane_no_key`, `lane_unreadable`).

The other lanes are not key lanes and never take a key here: the API-key lane files (`codex-api`, `claude-api`: `.env`
files under `abcp-config/lanes/`) and the administrators' account lanes (`codex-account`, `claude-account`; signed in
through `/v1/account-lanes`).

## What changes

- **`internal/lanekey`.** At `abcp serve` start, the key lanes are read from the admission profile file and the templates
  it names, as run admission reads them (private files, no links, strict JSON). Two profiles naming one provider must
  name the same key file, a key file belongs to one lane, and no name is both a provider and an engine lane; otherwise
  the routes answer `501` and the log says `abcp lane keys ready=false reason=…`. It logs
  `abcp lane keys ready=true lanes=together other-lanes=4`. No key file is read at start.
- **Setting a key** writes the lane's key file whole: a new file beside it (`.<name>.abcp-<16 hex>.tmp`, created
  exclusively, 0600, ABCP's user), the key and a newline, synced, renamed over the key file, then the folder synced. A
  build starting meanwhile reads the old key or the new one, never part of either; a build already running keeps the
  key it started with (it holds it in memory). The folder must be ABCP's user's and writable by it alone, with no link
  in its path; a link at the key file's name is replaced (no build reads a link), a folder there is refused. The file is
  then read back exactly as a build reads it. On any failure the new file is removed and the old key stays.
- **The key's shape** is checked, never the provider called: Together 32 to 128 letters, digits, `_` or `-` (its keys
  are 64 hex, or `tgp_v1_` and 43 more); Groq 40 to 128 of the same (`gsk_` and 52 more); any other provider the builds'
  own rule. Repo C checks the key with the vendor before it sends it (design note §9).
- **Removing a key** removes the file and syncs the folder; the lane's engines then read `lane_no_key`.
- **The key is never logged, answered, or put in a ledger event or an evidence item.** The request log has the path only
  (as for every route). The audit line names the lane and the key's fingerprint, the first 8 hex of its SHA-256:
  `abcp lane key set at=… lane=together fingerprint=1a2b3c4d`, `abcp lane key removed at=… lane=together
  fingerprint=…` (or `was=missing`), `abcp lane key refused at=… lane=together reason=shape`. A build's start records
  the key file's SHA-256 as before.

## The routes

For a service principal granted **`lanes.control`** (new, in `grants.json`); `401` without the bearer token, `403
authority_denied` without the grant, `501 unsupported_capability` without key lanes.

| Call | Body | Answer |
|---|---|---|
| `GET /v1/lanes` | none | `200 {"schema_version":"LanesV1","lanes":[{"lane":"together","kind":"key","state":"set","fingerprint":"1a2b3c4d","profiles":["local-p02-node-together","local-p02-together"]}]}` |
| `PUT /v1/lanes/{lane}/key` | `{"key":"…"}`, strict JSON, at most 4 KiB | `200`, the lane as `GET` lists it, `"state":"set"` and the new key's fingerprint |
| `DELETE /v1/lanes/{lane}/key` | none | `200`, the lane with `"state":"missing"` (also when there was no key) |

`state` is `set`, `missing` or `unreadable` (a file no build would read; setting a key replaces it). `fingerprint`
appears only with `set`. Errors, `{"error":{"code":…}}`, never with the key:

| Status | Code | When |
|---|---|---|
| 400 | `invalid_request` | a body that is not `{"key": string}` exactly, a query, an escaped or malformed lane, another method or path |
| 400 | `lane_key_invalid` | the key does not have the provider's shape |
| 404 | `lane_unknown` | no build profile names the lane |
| 409 | `lane_not_key` | the lane is an API-key lane file or an account lane |
| 413 | `request_too_large` | a body over 4 KiB |
| 503 | `lane_key_unavailable` (retryable) | the key file could not be written, read back or removed |

Readiness stays `GET /v1/engines`' (`ready`, `lane_no_key`, `lane_unreadable`, and the provider's own `check` where the
manifest asks for it); `GET /v1/lanes` adds which key is set.

## What stays the same

Every template, profile, digest, lane file and account lane. The builds read their key file as before; ABCP never calls a
provider here. `GET /v1/capabilities` is unchanged: Repo C learns the routes exist from the routes themselves.

## Checks

- `internal/lanekey`: the lanes the profiles name (a provider's two profiles one lane, the API-key and account lanes
  others, Groq a lane of its own) and each refused file; the write (0600 under umask 0, ABCP's user, the key alone on one
  line, nothing else left in the folder); the next build's start (`openmodel.Begin`) reading the new key while a running
  build keeps its own; a removed key failing the next start with `OPEN_MODEL_ENGINE_KEY`; 200 replacements while a reader
  reads the file, always one key whole; a failed sync or rename leaving the old key and no new file; an unknown lane, an
  API-key lane and an account lane refused and the account lane's file untouched; a folder others may write in, a linked
  folder, no folder and a folder at the key file's name refused; a file open to others and a link replaced (the link's
  target untouched); the shapes; the fingerprint. The routes end to end through `serviceapi`'s server with a real bearer
  token and grant file: set, refused (no token, a wrong one, unknown, account, API-key lane, shape, a strict body, over 4
  KiB), removed, and without `lanes.control`. **No key anywhere:** every file the tests write (key files aside: the Codex
  homes and slots, a ledger with each start's evidence as `abcp run` records it, an evidence store), every answer and
  every log line is scanned for both test keys.
- `internal/serviceapi`: the routes' paths, strict bodies, the 4 KiB bound with and without a stated length, the error
  codes, the grant, a user principal refused, `401` anonymous, `501` without lanes, a request that prints without its key.

## Rollout

1. Merge; build the binary from main (`a-lane-keys-build.sh <merge sha>`: the tests, the wall tests and the Codex
   end-to-end tests, as the batch builds did).
2. One quiet restart (`a-lane-keys-restart.sh <merge sha>`): waits for quiet, proves the binary and the wall, adds
   `lanes.control` to `grants.json` (kept as `grants.json.pre-lane-keys-<time>`), starts the new binary, checks both
   Together engines ready with their check ok and every engine ready before still ready, `GET /v1/lanes` answering
   `together` `set` with its profiles, and moves the boot entry; on any failure everything goes back and the old binary
   starts again.
3. Repo C: "Add key" on a vendor with a lane sends `PUT /v1/lanes/{lane}/key` after the vendor's check, shows the lane's
   state and fingerprint from `GET /v1/lanes`, and "Remove" sends `DELETE`. Then one build on a key entered on the page
   (design note §9's check).

## Engine lanes (the slice's second half, #205; live since 2026-11-19)

Repo C's page keeps an Anthropic and an OpenAI key, and the builds that pay with those keys run on the API-key engine
lanes `claude-api` (22 profiles) and `codex-api` (2), whose `.env` lane file holds the key in one line
(`ANTHROPIC_API_KEY=`, `OPENAI_API_KEY=`). #198 refused them (`lane_not_key`); this takes them, once the operator names them.

- **`abcp serve --lane-key-engine-lanes claude-api,codex-api`** (and the same flag on `abcp lane-keys`). Empty, the
  default, is #198 exactly. Each lane named must be one a profile names in `executor.lane`, on `codex` (`OPENAI_API_KEY`,
  vendor `openai`) or `claude` (`ANTHROPIC_API_KEY`, `anthropic`); every profile naming it names the same lane file and
  executor; its file is no other lane's; it is not in the account lanes file; and its file, read at start as run
  admission reads it, is not paid another way (`CLAUDE_CODE_USE_BEDROCK`, `CLAUDE_CODE_OAUTH_TOKEN`, or an
  `OPENAI_BASE_URL` other than OpenAI's). Otherwise `abcp lane keys ready=false reason=…` and the routes answer `501`.
  Start line: `abcp lane keys ready=true lanes=claude-api,codex-api,together engine-lanes=2 other-lanes=2`.
- **`GET /v1/lanes`**: every lane gains `provider`, the vendor whose key it takes (`together`, `groq`, `anthropic`,
  `openai`); an engine lane is `"kind":"engine"`, its state and fingerprint those of its key setting. An engine lane whose
  file is now paid another way is left out.
- **`PUT`** on an engine lane reads its file as a build does, replaces the key setting's line (or adds it at the end),
  keeps every other line byte for byte (comments, other settings, line endings), checks the result as a build reads it,
  and writes it whole exactly as a key file is written (`.claude-api.env.abcp-<16 hex>.tmp`, 0600, fsync, rename, folder
  fsync), then reads it back with `enginelane.Load`. A file no build reads is not rewritten (`503`). Shapes: 20 to 512
  letters, digits, `_` or `-` for both; an Anthropic setup token (`sk-ant-oat…`) is refused. Neither vendor is called.
- **`DELETE`** on an engine lane removes the key setting's line. When nothing else would be left it answers **`409
  lane_key_required`** and changes nothing: a lane file that gives no setting fails profile load, and with it every
  admission, at the next start. `codex-api`'s file may be that file.
- **`If-Match: "<fingerprint>"`** (new, on `PUT` and `DELETE` of every lane): the lane must hold exactly that key now,
  checked under the write lock, or **`412 lane_key_changed`** and nothing changes. One quoted 8-hex tag only; anything
  else is `400 invalid_request` (and on `GET`). Repo C removes a key with the fingerprint of the key it entered, so it
  never removes a key the operator or a newer entry put there.
- **A build running** keeps the settings it started with (`run.go` reads the lane file once at the run's start); the
  next start, a run resumed after a restart, and `GET /v1/engines` read the new file.
- **Audit lines** as before, plus `abcp lane key kept at=… lane=codex-api reason=required` and `abcp lane key refused
  at=… lane=… reason=changed|bedrock|setup-token|another-address`.
- **The change jobs' lane** (`change-engine.json`, `change-api`, Anthropic): the owner said yes (2026-11-19); built in
  the section below.

Checks: `internal/lanekey` (`engine_linux_test.go`: the flag and every refused configuration; a key line replaced with
every other byte kept under umask 0, 0600, read back, the next build's `enginelane.Load` and environment on the new key
while a loaded lane keeps its own; `If-Match` met and unmet on set and removal; the line removed, `lane_key_required`;
shapes; Bedrock after start; an unreadable file not rewritten; a failed sync or rename keeping the file; every file but the
lane files, every answer, error and log line scanned for six test keys; the routes end to end with `If-Match`),
`internal/serviceapi` (`If-Match` passed on or refused, the two new codes), `cmd/abcp` (`abcp lane-keys` with an engine lane
and an account lane refused).

**Rollout:** merge; build the binary as before; one quiet restart (the owner's word) with `--lane-key-engine-lanes
claude-api,codex-api` added to the start script. Without the flag the new binary behaves as #198 (`provider` aside).
Check with `abcp lane-keys --admission-profile-file … --lane-key-engine-lanes claude-api,codex-api --account-lanes-file …`
before the restart: both lanes `state=set`. No grant or template change.

## The change engine's lane (branch `feature/change-lane-key`; built, not live)

The owner, 2026-11-19 (Repo C `BUILD_ENGINE_RULES.md` §14, answer 1): the change jobs' lane (`change-engine.json`'s
`change-api`, Claude Code on Anthropic's API; `docs/plans/change-jobs.md`) takes the page's Anthropic key too, "named like
the engine lanes, its `ANTHROPIC_API_KEY` line written the same way, listed by `GET /v1/lanes` with no profiles but the
change jobs".

- **Named in the same flag:** `--lane-key-engine-lanes claude-api,codex-api,change-api`. The lane is the change engine
  file's `lane_name` and `lane_file` (`abcp serve` takes them from `--change-engine-file` whenever that file checks,
  whether or not change jobs are otherwise ready); executor `claude`, setting `ANTHROPIC_API_KEY`, vendor `anthropic`.
  The same checks as a profile's engine lane: not an account lane; its file read at start as a build reads it and not paid
  another way (Bedrock, a setup token); a file of its own (no profile's lane file, named or not, and no key file); a
  profile that names the same lane names the same file on `claude`. Named while the change engine file is missing or does
  not check: `abcp lane keys ready=false reason="engine lane \"change-api\": no profile names it, nor the change engine"`
  and the routes answer `501`, as for any named lane that does not check. Not named, the lane is left out entirely (not
  in `other-lanes` either), as before.
- **`GET /v1/lanes`:** `{"lane":"change-api","kind":"engine","provider":"anthropic","state":"set","fingerprint":"…",
  "profiles":[],"uses":["change-jobs"]}`. `uses` is new and appears only on that lane, so every other lane answers byte for
  byte as before; `profiles` is an empty list, never `null`. Repo C's `BuilderLanes` already leaves fields it does not read
  alone and takes an empty `profiles`, and already sends an Anthropic key to every lane whose `provider` is `anthropic`.
- **`PUT`, `DELETE`, `If-Match`** exactly as on `claude-api`: the key line replaced with every other line kept, 0600, read
  back; the line removed, or `409 lane_key_required` when nothing else would be left (the host's file also holds its model
  settings, so a removal goes through and the next change job has no key: `ENGINE_UNAVAILABLE`); `412 lane_key_changed`;
  audit lines with `lane=change-api`.
- **Each change job reads the lane once, when it starts** (`stackjob.ClaudeEngine.Pin`, called by the job's worker before
  its baseline), as a build's run reads its lane: its agent session, its review session and its secret checks use those
  settings and never read the file again. Until now each session read the file itself, so a key written mid-job reached
  its review, and its secret check read the lane after the agent had run. A key written by `PUT` reaches the next job
  without a restart; a job already running keeps the key it started with. A lane file no build would read when a job
  starts ends it `FAILED` with `ENGINE_UNAVAILABLE` before its baseline (until now, after it). `abcp serve`'s check of
  the engine file at start is unchanged.
- **`abcp lane-keys`** takes `--change-engine-file`, as `abcp serve` does, and prints `lane change-api kind=engine
  provider=anthropic state=set profiles= uses=change-jobs`.

Checks: `internal/lanekey` (the lane named and not named, and named without the change engine; each refused
configuration: Bedrock, a setup token, a file open to others, a file Claude Code does not read, no file, a relative path,
`claude-api`'s file though `claude-api` is not named, an account lane's file, a provider's name, a profile naming it on
another file; a profile on the same file allowed; the key line replaced with every other byte kept under umask 0, 0600,
read back, the next change job's `enginelane.Load` on the new key while a loaded one keeps its own; `If-Match`; the line
removed; `lane_key_required`; a setup token; Bedrock after start; every file but the lane files, every answer and log line
scanned for the test keys; the routes end to end), `internal/stackjob` (a job's agent and review on the key the lane held
when the job started though a new key was written meanwhile, the next job on the new key; a change carrying the job's own
key not handed back though the lane holds another by then; an unreadable lane ending a job before anything runs;
`ClaudeEngine.Pin` through `LoadEngine` with a fake Claude Code that answers the key its session got), `internal/serviceapi`
(the listing with `uses`, and without it as before), `cmd/abcp` (`abcp lane-keys` with the change engine file, and the
lane named without it refused).

**Rollout:** merge; build the binary as before; check on the host with `abcp lane-keys --admission-profile-file …
--lane-key-engine-lanes claude-api,codex-api,change-api --account-lanes-file … --change-engine-file …` (run with this
branch against the host's files on 2026-11-21: `change-api` `state=set profiles= uses=change-jobs`, `lanes=4`). Then the
start script's flag gains the change lane, `--lane-key-engine-lanes claude-api,codex-api,change-api`, and one quiet
restart, on the owner's word. The start line then reads `abcp lane keys ready=true
lanes=change-api,claude-api,codex-api,together engine-lanes=3 other-lanes=2`. Without `change-api` in the flag the new
binary behaves as today, but for each change job reading its lane once at its start. No grant, template or lane file
change. Repo C then sends the Anthropic key to it as to `claude-api` (Send to builds on Anthropic's card).

## Not here

A Groq lane appears in `GET /v1/lanes` once its profiles are live (#192) and ABCP restarts on them.

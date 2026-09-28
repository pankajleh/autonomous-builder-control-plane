# Current Project State

Date: 2026-09-28

Operational role: current checkpoint and authority projection for Repo B / ABCP. Immutable execution-pack/evidence artifacts and Git/GitHub objects remain authoritative if this projection conflicts with them.

## Repository checkpoint

- Repository: `pankajleh/autonomous-builder-control-plane`.
- Executable baseline: PR #54 merge `109e15b45ae1f75316f3b3ba75b93e6ae6a6d128`. It carries worktree eviction, product-requested worktree release, and their corrections (PRs #46–#54). Later documentation-only commits may advance `main`; Git/GitHub are authoritative for the latest head.
- Live controller: `abcp serve` built from exactly `109e15b` (binary SHA-256 `403d104aa741fbfc93f4716ef6defdc916434940c7b1a96bcc78a7cc58b78b78`) on the local-integration host, serving Repo C. See [live runtime](#live-runtime-2026-09-28).
- Platform boundary: **the EP-006 service/API baseline, plus these extensions:**
  - Repo C product run admission (P01);
  - the controller-owned Ralphex execution profile;
  - governed development-run admission (G0);
  - human-decision pause/resume;
  - BP-01 provider-neutral activity;
  - BP-02 governed preview runtime;
  - controller-owned worktree retention and eviction.
- The post-EP-006 A/B/C assurance expansion remains discarded and is not part of current runtime authority.
- Repo C is the product/UX authority. ABCP owns execution admission, the authoritative run lifecycle, projections, evidence and actions, provider coordination after admission, activity normalization and preview runtime.
- Repo A / Dev-Agent and Ralphex remain execution/provider mechanisms beneath ABCP; Repo C does not call them directly.

## Rebaseline and P01 chain

| Checkpoint | Exact identity | Result |
|---|---|---|
| EP-006 retained baseline | retained EP-006 commit `f1f4af3c8783d870dcab85a1f86756e6ed4efcee` | service/API projections/actions retained; later assurance expansion removed |
| Repo B rebaseline publication | PR #21 head `4040744d9db924ce4e174f7281fdc9f58db7818f`; merge `821e0476600c9299652b88a016c24ba471bf8a6b` | `main` re-established on the retained EP-006 tree |
| P01 authority base | `4d45202f5b411d9c91caf5fef906d1eff9b26e4b` | frozen Repo C↔ABCP admission architecture, execution pack, review contract and evidence discipline |
| P01 final tested executable | `38eda0c3ce489dbe5e51ad297ac3374d98fadcd7` | product-facing admission + replay/reconciliation hardening; all required gates passed, PostgreSQL integration skipped only because the authorized DSN was unavailable |
| P01 publication | PR #22 head `f1ae22fc3132513bbe5c411058e506676d9fb069`; merge `3d6a841e722c9c67c83d835a52178b09ca16776d` | product run admission is integrated on `main` |

Everything after P01, PRs #23–#57, is indexed in `docs/AUDIT_INDEX.md` and summarized in `docs/PROGRESS.md`.

## Current product-facing service boundary

ABCP provides the retained EP-006 read/action surfaces, bounded product run admission and the PDLC Experience extensions:

- authenticated capability discovery;
- run list/detail, events, timeline and evidence, and evidence download;
- cancel, human-decision recording and action status;
- `POST /v1/runs` when admission is configured;
- `GET /v1/extensions/pdlc-experience` (`PdlcExperienceCapabilitiesV1`, with `activity_stream` and `preview_runtime`);
- BP-01 provider-neutral activity: paged `ActivityPageV1` reads and an SSE stream. Ralphex progress is normalized into provider-neutral events, and ABCP state and checkpoint facts are included. Every unverifiable gap becomes an explicit controller `UNKNOWN` marker, and marker diagnostics name the failing step without provider text;
- BP-02 governed preview runtime: create, list, detail, stop and route for an exact clean checkpoint of a product-admitted run. Operator-owned, digest-pinned preview profiles apply, the `preview.control` authority is required, and preview never changes run state.

The admission contract accepts only the frozen schema-v1 product-safe request. It resolves controller-owned private execution configuration, binds one stable request identity to one durable ABCP run identity, and fails closed on conflicting replay, repository-base mismatch or unresolved reconciliation.

A run whose Ralphex failure classifies as a human decision pauses at `HUMAN_DECISION_REQUIRED`, and resumes after a recorded proceed decision (PR #31). The advertised `retry`, `resume` and `recovery` capability flags remain false. There is no general retry or recovery.

## Governed worktree retention

Manifest templates may set `worktree.retain`. ABCP then passes `--keep-worktree` to a pinned Ralphex whose capability probe proves `worktree_retention_v1`, so the governed worktree survives the run and its removal is controller-owned (PR #42). This keeps the activity binding resolvable after acceptance. Trailing provider detail, such as the final "Review completed" signal, is collected for 15 minutes after the run finishes (PR #47), and a checkpoint stays preview-eligible after `BRANCH_ACCEPTED`. Retention is bounded by controller-owned eviction (PR #46). A finished run's worktree is evicted after 24 hours without a live activity stream (paged reads do not count) or 7 days after the run finished, and least-recently-used worktrees go first when a repository exceeds 5 GiB. Unfinished runs, live previews and streaming clients protect a worktree. Before removal, ABCP pins every clean checkpoint at `refs/abcp/checkpoints/<run>/<sha>` and seals a `WorktreeEvictionV1` record. A preview of an evicted run then resolves from the sealed record and the pinned ref, even if the branch is deleted, and activity reads stay free of markers. The design, flags and tests are in `docs/plans/completed/bp02-worktree-eviction.md`.

## Live runtime (2026-09-28)

| Component | Identity |
|---|---|
| ABCP | `109e15b` since 07:36Z, binary SHA-256 `403d104a…8b78`, listener `127.0.0.1:18888`; worktree eviction on the default policy (24 h without a live stream, 7 days, 5 GiB per repository); worktree release enabled by the `worktree.release` grant |
| Pinned Ralphex | `ralphex-v1.7.0-abcp`: upstream release `v1.7.0` plus the ABCP patch series, source `pankajleh/ralphex-governance` `abcp/v1.7.0` @ `2275e23adba99bb22c23679ae3e8152c0323aba1`, SHA-256 `fe5a7c46…4058` |
| Manifest templates | both `worktree.retain: true`; versioned in `deploy/local-integration/abcp-config/` |
| Preview profiles | `demo-v1` and `web-v1` (busybox `/bin/httpd`, health `/README.md`); versioned in the same directory |

Retention proof run `admission-2b69b2cd89c1c0cba2ab77c840587345a2512dc6d7944f00762cb77cb2a15c57` showed the following:

- the worktree was retained after `BRANCH_ACCEPTED`;
- activity grew from 79 events at acceptance to 86, including "Review completed", with 0 `UNKNOWN` markers;
- a preview created only after acceptance was `READY/HEALTHY` and served the marker file (`200`);
- the run state was unchanged.

Worktree eviction proof run `admission-3ce5345c4792fc37113251cfb9ca1a1fb03291838ee41f0eaa5461c75f0d4280` (task `317c9266-0ae6-496e-99f1-ec7d6cc99b89`) ran on `e8ab2c9` with a short proof policy (sweep every minute, 2-minute idle):

- `BRANCH_ACCEPTED` at 05:27:41Z. At acceptance the run had 83 activity events and 0 `UNKNOWN` markers.
- ABCP evicted the worktree on its own at 05:29:58Z (`reason=idle`).
- The `abcp/<run>` branch was kept, and all 3 clean checkpoints were pinned under `refs/abcp/checkpoints/<run>/`.
- The record `evictions/<run>.json` was written with mode `0600`.
- After eviction, activity had 96 events (trailing provider detail collected before eviction) and still 0 `UNKNOWN` markers.
- Two previews were created only after eviction (`aebb78eb…`, `5c747a06…`). Both were `READY/HEALTHY` from the final pinned checkpoint `e4eaacd678d8d5be3a80fed62d95296d377aa647`, fetched by pinned ref with no origin remote. Both served `docs/EVICTION_PROOF_MARKER.md` through the Repo C preview gateway (`200`, line `P1-CLOSURE-VERIFICATION-OK`).
- ABCP was then restarted on `e8ab2c9` with the default policy.

The earlier proof runs `admission-2b69b2cd…` and `admission-f7e2967f…` were also evicted (05:18:02Z, `idle`, 3 pinned checkpoints each). Previews of them are refused as ineligible because each history carries an `UNKNOWN` marker:

- `2b69b2cd…` ordinal 807 was written by provider polling after acceptance (fixed in #47);
- `f7e2967f…` ordinal 882 was written by a controller shutdown (fixed in #50).

Two observations came out of the proof:

- **Repo C error mapping.** Repo C reported those refusals as `PREVIEW_IDENTITY_CONFLICT`, because it read ABCP's error `code` at the top level instead of `error.code`. It is corrected in Repo C P0057.
- **Restart race.** A restart attempt at 05:35:27Z failed with `construct preview extension`. The old process still held the preview store lock while removing its live preview containers, and the start script waited only for the port. The service was down about 25 seconds. The host `runtime/start-abcp.sh` now waits for the old process itself to exit.

Task-closure proof (2026-09-28, ABCP `7ac0cff` with Repo C P0060 `27748d8`):

- **Already evicted run.** Closing task `317c9266…` asked ABCP to release `admission-3ce5345c…`, which had been evicted for inactivity. The answer was `ALREADY_RELEASED` with the original eviction time. Its activity already showed the eviction event (Repo C text "Build workspace removed after inactivity").
- **Fresh run.** Task `259dc838-14ee-4e1b-85cb-852a113ea764` ran `admission-6f80645d…` to `BRANCH_ACCEPTED` with 3 checkpoints.
  - Closing it took 0.27 s, and the release answered `RELEASED`.
  - ABCP sealed a `task-closed` record (mode `0600`), removed the worktree and pinned all 3 checkpoints.
  - Activity gained one `WORKSPACE`/`RELEASED` event (Repo C text "Task closed; build workspace removed").
  - Repo C then refused preview create and build submission with `409 TASK_CLOSED`.
- **Defects found.** The proof found two defects, both fixed in #54:
  - A close attempt during the build returned `503` instead of `409 TASK_HAS_ACTIVE_BUILD`. Release looked at the worktree before checking whether the run had finished.
  - A false `provider-replay-gap` marker (ordinal 983) was recorded about 3 s into the run, although a later full replay was contiguous.
- **Cutover.** ABCP was cut over to `109e15b` at 07:36Z with no run executing. It was down about a minute: the operator's wrapper command contained the text that `start-abcp.sh` looks for, so the script waited for a process that had already exited. Starting the script by itself worked.

Operator configuration drift is checked with `tools/operator/check_local_integration_config.py`.

## Deliberate limits

Current `main` does **not** claim:

- general retry/recovery, or advertised `retry`/`resume`/`recovery` capabilities;
- a customer-visible "worktree evicted" activity entry (needs a Repo B and Repo C contract change), or eviction on task closure or tenant deletion;
- provider-selection redesign;
- live production/AWS deployment;
- restored Assurance Capsule / post-EP-006 A-B-C methodology.

## Known issues

- `internal/mergelifecycle`: two recovery subtests of `TestTask3FinalClosureM02BudgetExhaustionDisposition` fail ("leased append cannot bypass an unresolved transition barrier"). So does `TestTask3FinalClosureM03PreTargetBudgetCrashRecovery`. The failures are identical at every merge back to PR #17 (`ed74fad`), whose post-merge acceptance passed at the time, so they depend on the environment rather than being a regression from any later PR.
- `internal/actionapi` has an intermittent failure.
- `internal/run/resume.go` is not `gofmt`-clean on `main`.
- The pinned Ralphex governance patch series is maintained in the private `pankajleh/ralphex-governance` repository, on upstream release tags. The upgrade procedure is in `deploy/local-integration/README.md`.

## Next bounded integration boundary

Persistent checkpoint state and eviction are in place (PR #46). Candidate follow-ups are a customer-visible eviction entry and eviction on task closure or tenant deletion. Each is added only when a concrete Repo C product workflow needs it. Review/discovery alone does not create new scope.

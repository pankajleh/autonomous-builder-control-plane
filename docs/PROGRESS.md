# Implementation Progress

Updated: 2026-09-28

Operational role: this file projects roadmap progress and the next eligible planning boundary. Immutable controller, review, Git, and durable evidence remain authoritative.

## Roadmap

| Phase | Status | Evidence and current boundary |
|---|---|---|
| Phase 0 — Foundation | COMPLETE | EP-001 merged |
| Phase 1 — Governed single-plan execution | COMPLETE | PR #4 merged |
| Phase 2 — Recovery and blocker control | COMPLETE | PR #5 merged |
| Phase 3 — Cross-plan scheduler and integration | COMPLETE | PR #6 merge `94e14ca749d31ac214e979aab03fbde37502dd7f` |
| Phase 4 — GitHub lifecycle | **COMPLETE** | CI Task 4 merged in PR #15; governance hardening PR #16; merge authorization Task 1/2/3 closed; exact convergence PR #17 merged as `ed74fad…`; durable post-merge acceptance/reconciliation PASS |
| Phase 5 — Service/API and dashboard | **IN PROGRESS** | EP-006 service/API baseline retained (PR #21). Product-integration extensions merged through PR #47 (bounded trailing provider detail) (see the post-rebaseline ledger below). The customer dashboard is owned by Repo C. |
| Phase 6 — Production hardening | NOT STARTED | Roadmap only |

## Phase-4 completion ledger

| Deliverable / subtrack | Status | Exact checkpoint |
|---|---|---|
| GitHub lifecycle foundation | COMPLETE / MERGED | PR #7 merge `bf5f923f1743b541fac8ad75fa173557fe68ba0f` |
| Exact-head PR lifecycle | COMPLETE / MERGED | accepted/reviewed head `6db075240ce87b751f8db98abb540c96410c515b`; PR #8 merge `ccf75d093625119cc39944fe7a47c3a03b30ad3b` |
| CI evidence ingestion Tasks 1–3 | COMPLETE | final Task-3 head `3e0f295e8f98c348d684b2c026bacd9ceeeea911`, 0C/0M review; PR #13 merge `bf2482f756a7c5f75906825b8f3e6e454c72f94f` |
| CI Task 3 projection reconciliation | COMPLETE / MERGED | PR #14 merge `6fd7fc2d59a1467f4e85aad18a35dc83d0bc7c9e` |
| CI Task 4 acceptance / scope audit / exact-head review | COMPLETE / MERGED | corrected head `9ea0c67a0ab5fdcc1c26ed6eeae4e09732b5aaa2`; 15/15 acceptance PASS; final 0C/0M; PR #15 merge `66da45760c7923bd244f3ac6dd3aa699d89bc2e8` |
| Three-capsule governance hardening | COMPLETE / MERGED | corrected head `aa888f26652f88040ca8faa3499251ba6281a995`; return-to-B closure 0C/0M; PR #16 merge `d145b9f69418fd579650e5b1afca267f6ee59e72` |
| Merge authorization Task 1 — lifecycle contracts | COMPLETE | independently accepted/reviewed 0C/0M at `bf5c851e8bce19f61648eb499ad146f25a1ee86c` |
| Merge authorization Task 2 — controller/runtime | COMPLETE | `CLOSURE_CLEAN` 0C/0M at `c6716a800f1b317eb1cae43c9e50fa5e81bf67c2` |
| Merge authorization Task 3 — live GitHub merge-only provider | COMPLETE | final exact-head `15a7585ca25d4fee036afc6d19324c1b528b9381`; `CLOSURE_CLEAN` 0C/0M |
| Final base convergence | COMPLETE | `8dd860286590044888052f2f53e856c3c8c1f1cb`; deterministic acceptance PASS; exact-head 0C/0M |
| Publication and merge | COMPLETE | PR #17; expected-head-bound normal merge; result `ed74fad66b9dd564ac09dcedc1274b8fefffb983` |
| Post-merge acceptance/reconciliation | COMPLETE | full deterministic merged-state suite PASS; exact remote/result reconciliation PASS; fsync-sealed durable evidence |
| Operational projections through Phase-4 cutoff | COMPLETE AT THIS PROJECTION | `CURRENT_STATE.md`, `PROGRESS.md`, and `AUDIT_INDEX.md` materialize the accepted cutoff |

## Terminal Phase-4 evidence

The final reviewed convergence head `8dd860286590044888052f2f53e856c3c8c1f1cb` has tree `c055f686f20634e0e69550cc08e019cf4be7a0e5`. PR #17 merged it against exact base `d145b9f69418fd579650e5b1afca267f6ee59e72` as `ed74fad66b9dd564ac09dcedc1274b8fefffb983`; the merge result has the same tree and an empty candidate-to-result content diff.

Post-merge deterministic acceptance passed the governed run/governance, GitHub lifecycle, live provider, merge lifecycle, integration gate, full test, full race, vet, smoke, metadata-free smoke, Darwin compile, diff-check, and clean-worktree gates. Reconciliation independently proved the exact PR/head/base/result/ref/tree/parent facts.

Durability identities: manifest `ba742fb2ed92eeb03094b34e7f128bb0c46881abbe9300f97ababb52adce7533`; acceptance record `da1a94c11f6803dec15d44b6b79b3c16f88ac8dae6f3e8971666f11a69489609`; durability seal `ca35273ae946fa3cbc91fe5bf099302e1bc33a4e42b9fcdfc25adcb3a987a57b`. Manifest verification and final recheck both had zero failures.

## Cross-cutting governance status

- Context-bound autonomous-operation policy remains merged and authoritative from PR #11.
- Operational projection policy remains merged and authoritative from PR #12.
- PR #16 merged the hardened three-capsule and semantic-convergence implementation/contracts. It does not, by merge alone, prove a production `GovernanceActivationV1` installation.
- The accepted C-stage boundary is read-only for the exact bound candidate; review findings requiring content changes return through valid B authority, or A when design/scope must change.

## Supported / unsupported at the Phase-4 cutoff

Supported production merge execution is the controller-authorized same-repository-head `merge` path bound to exact base/head authority and the frozen atomic base-update plus head-no-op-CAS provider contract. Squash/rebase and fork-head merge execution remain unsupported. CI evidence remains neutral/read-only; `STABLE` is not a merge-approval verdict.

## Post-rebaseline product-integration ledger (Phase 5)

After the 2026-09-19 EP-006 rebaseline (PR #21), Repo B work followed concrete Repo C product-integration needs. Exact head and merge identities are in `docs/AUDIT_INDEX.md`.

| PR | Merge | Scope | Evidence |
|---|---|---|---|
| #22 | `3d6a841` | ABCP-RC-P01 product run admission | `docs/execution-packs/ABCP-RC-P01/` |
| #23, #24 | `f39eeb7`, `ff22028` | P01 current-state and audit projections | this file's history |
| #25 | `12e292a` | Controller-owned Ralphex execution profile (ABCP-RALPHEX-P01) | `docs/execution-packs/ABCP-RALPHEX-P01/evidence.md`, `docs/architecture/RALPHEX_ADAPTER_CONTRACT.md` |
| #26 | `baf1b37` | G0 governed development-run admission | `docs/execution-packs/G0-DEVELOPMENT-TOOLING-CUTOVER/` |
| #27 | `5ae6fa6` | Governed product defaults and bounded Ralphex validation | PR #27 |
| #28 | `fd1a17b` | Runtime catalog run-authority xattr relocation | PR #28 |
| #29 | — | closed without merge (branch-accepted terminal report) | — |
| #30 | `fec0c08` | Reject acceptance commands that cannot fail | PR #30 |
| #31 | `6fc11cd` | Human-decision pause and resume after a proceed decision | PR #31 |
| #32 | `935114d` | Preserve full admission run identity (BP-01 prerequisite) | PR #32 |
| #33 | `f21174d` | BP-01 provider-neutral activity capability | `docs/plans/completed/bp-01-provider-neutral-activity.md` |
| #34 | `ad8d19a` | BP-02 governed preview runtime | `docs/plans/completed/bp-02-governed-preview-runtime.md` |
| #35 | `5cd8ee5` | BP-02 Docker 29 bind propagation correction | `docs/plans/completed/bp-02-docker29-bind-propagation-correction.md` |
| #36 | `d510f0c` | PX-07 prerequisite: bounded preview route resolution | `docs/plans/completed/px07-preview-route-resolution.md` |
| #37 | `34dec9e` | BP-01 activity replay continuity | `docs/plans/completed/bp01-activity-replay-continuity-correction.md` |
| #38 | `dfc3b93` | BP-01 provider replay semantics | `docs/plans/completed/bp01-provider-replay-semantic-correction.md` |
| #39 | `fd39b1a` | BP-01 step-named marker diagnostics | `docs/plans/completed/bp01-activity-marker-diagnostics.md` |
| #40 | `20f2319` | BP-01 sidecar resume overlap; startup worktree deferral | `docs/plans/completed/bp01-sidecar-resume-overlap.md` |
| #41 | `0d99f7d` | BP-01 provider worktree cleanup transition | `docs/plans/completed/bp01-worktree-cleanup-transition.md` |
| #42 | `a5383a6` | Controller-owned governed worktree retention (`worktree.retain`, Ralphex `--keep-worktree`) | `docs/plans/completed/bp02-controller-owned-worktree-retention.md` |
| #43 | `f364222` | Ledger reconciliation; versioned local-integration operator configuration and drift check | `deploy/local-integration/README.md` |
| #44 | `65992f7` | Ralphex governance patch series rebuilt on upstream release `v1.7.0` (`ralphex-v1.7.0-abcp`), source moved to private `pankajleh/ralphex-governance`, upgrade procedure. Proof run `admission-f7e2967f…`: retained worktree, 61→74 events, 0 `UNKNOWN`, preview after acceptance `READY`/`200` | `deploy/local-integration/README.md` |
| #45 | `fbf1ab8` | Upgrade note corrected: an upstream `--keep-worktree` needs a one-time patch-series adjustment | `deploy/local-integration/README.md` |
| #46 | `7ae1a5f` | Controller-owned worktree eviction: 24 h idle, 7 days max age, 5 GiB per repository. Checkpoints are pinned and the eviction record is sealed before removal, so preview and activity survive eviction | `docs/plans/completed/bp02-worktree-eviction.md` |
| #47 | this PR | A finished run with a retained worktree reads provider detail for 15 minutes only, so a restarted sidecar can no longer add a false `UNKNOWN` marker | `docs/plans/completed/bp01-bounded-trailing-detail.md` |

Repo C closed its PX-07 integrated journey on live Repo B `0d99f7d` (Repo C P0052, DEC-0021). BP-01/BP-02 therefore carry the Repo C PDLC Experience foundation. PR #42 was then proven live on `a5383a6` by run `admission-2b69b2cd…`:

- the worktree was retained after acceptance;
- 7 trailing provider events, including "Review completed", were collected after acceptance, with 0 `UNKNOWN` markers;
- a preview created after acceptance served the exact checkpoint.

Known issues (not regressions of this ledger's PRs):

- `internal/mergelifecycle` has environment-dependent failures, identical back to PR #17;
- `internal/actionapi` fails intermittently;
- `internal/run/resume.go` is not `gofmt`-clean;
- the Ralphex governance patch series now lives in private `pankajleh/ralphex-governance` on upstream release tags (#44).

## Next authorized action

Phase 5 continues only where Repo C needs a capability. Persistent checkpoint state and controller-owned eviction are merged (#46). The candidates are a customer-visible eviction entry and eviction on task closure or tenant deletion. No EP-005 authority may be carried forward as Phase-5 implementation authority.

# Assurance / Proof-Obligation Governance Enforcement Plan

Status: **A_DESIGN correction candidate — implementation forbidden until fresh exact-head 0C/0M review and `DESIGN_ACCEPTED`**

## Goal

Make the assurance/proof-obligation policy fail-closed in ABCP before any new external code-bearing build. Exactly three capsules remain: A designs/freezes functional + assurance authority, B implements it, C performs branch acceptance/final review and owns the governed publication/merge sequence.

The controlling A model is `docs/plans/assurance-proof-obligation-governance-enforcement-assurance.md`. B mutation is forbidden until that exact model + this plan pass fresh independent 0 Critical / 0 Major A review.

## Task 1 — V4 contracts, drained activation, durable backend

Frozen obligations: `AG-PO-001..007`, `AG-PO-020..024`, `AG-PO-031`, `AG-PO-034`, `AG-PO-038`, `AG-PO-042`, `AG-PO-046..050`.

Implement the exhaustive strict-canonical schema/type/reference catalog and generated positive/per-constraint rejection vectors. Implement the shared `PredecessorAuthorityDirectoryV1` for the exact workflow row, every bound ledger/run, process-startup PR admission root and merge state root. `internal/ledger/**` is explicitly in B authority because the shared writer lease/tombstone and effect transition barrier must be enforced at the ledger mutation boundary, not only at callers. Every V1 CAS/ledger/provider writer takes the registered shared cutover lease. `FencedWorkflowAuthorityBackendV1` wraps the exact selected predecessor `WorkflowAuthorityBackendV1`, so the exclusive fence quiesces even a non-PostgreSQL predecessor backend before loading its stable canonical V1 bytes. The SERIALIZABLE successor transaction proves exact lease states `{ISSUED,CONSUMING,CONSUMED}` and all barriers drained, preserves those exact V1 bytes, tombstones all later wrapped V1 CAS/provider admission, and installs V2 at `v1.revision+1`. Grandfather set is empty. Implement exact five-file manifest and forced-RLS PostgreSQL authority with pgx v5.7.6. No live V3 migration is permitted.

## Task 2 — A gate, capsule/grant/checkpoint authority, semantic corrections

Frozen obligations: `AG-PO-006`, `AG-PO-008..013`, `AG-PO-018..019`, `AG-PO-023..027`, `AG-PO-031`, `AG-PO-040`, `AG-PO-047..049`.

Implement `ContextCapsuleV4`, `PhaseAuthorityV4`, `PhaseCheckpointV2` and `StageGrantV2` as additive successors retaining every V3 capsule/source/outcome/parent field and every V1 grant/checkpoint blocker/invariant/operation/final-review floor. Use the frozen acyclic root bootstrap: A capsule without lineage, current checkpoint-core→A→B grant→full checkpoint sealing without lineage, then `AcceptedALineageV1`; every descendant carries it. Bind exact full parent checkpoint **and** grant. Preserve policy/model/registry/review plus independent proof-set, evidence-matrix, resource-profile-set and canonical-vector-catalog digests through A→B→C and failed-C→correction-grant→correction-B→replacement-C. Correction paths remain only `SemanticAuthorityRegistryV2 ∩ original B scope ∩ ControllerAffectedPathSetV1` from controller-recomputed accepted-B base→failed-candidate Git diff; reviewer bytes never supply paths.

## Task 3 — evidence, resource, artifact and secret execution

Frozen obligations: `AG-PO-013..016`, `AG-PO-020..025`, `AG-PO-027..036`, `AG-PO-039..041`, `AG-PO-050..052`.

Implement worker-scoped shared PostgreSQL admission/release across all domains/controllers/workflows, binding worker/domain/controller/repository/lineage/owner/parent/lifecycle and complete process/container/tmpfs/shm/cache/log/image counters plus the separately preflighted Docker-daemon cgroup/cache/log footprint. Separate immutable blobs from stage occurrences. Stage close must exclude publishers and fix a commit-consistent ordinal cutoff; inventory is ≤16 Merkle-linked pages ×128 entries, with closure artifacts outside the sealed occurrence set. Bind controller-owned complete secret-set commitment, quarantined exact candidate diff, exact stage subject and sealed inventory into scan authority/evidence. `CODEX_BROKER_V1` uses only the frozen base-URL/routes/token/request/byte/timeout/zero-retry profile and exact isolation probes; no credential-bearing stage passes without full zero-match evidence.

## Task 4 — C, integration, PR/merge effects and ledger handoff

Frozen obligations: `AG-PO-010..023`, `AG-PO-025..029`, `AG-PO-031..037`, `AG-PO-043`, `AG-PO-045`, `AG-PO-054..055`.

C executes only C cells against unchanged candidate and fresh exact-head review. Enforce integration before PR. PR intent binds the current exact `INTEGRATION_ACCEPTED` ledger event/run/candidate; merge intent binds exact `READY_FOR_MERGE`. Under `AcquireRunTransition`, with the corresponding `internal/ledger/**` enforcement in scope, install the exact V1 transition barrier before the PG claim race. Implement one-winner claims, separate durable `CALL_POSSIBLE` admission before transport, typed result/reconciliation/settlement records, exact request-digest equality, pre-call lost-winner `NOT_APPLIED`, UNKNOWN recovery hold, and the frozen resolve/read-back/`EffectBarrierResolutionV1` chain. `EffectReauthorizationV1` permits ordinal `n+1` only after exact settled NOT_APPLIED plus that resolution record. Integrate minimally into `internal/prlifecycle/**` and `internal/mergelifecycle/**`; do not change `internal/githublifecycle/**`.

The append-only ledger remains run-state truth and PostgreSQL remains coordination authority. PR barriers prevent INTEGRATION_ACCEPTED invalidation after a winning claim; PR APPLIED append-or-verifies `INTEGRATION_ACCEPTED→READY_FOR_MERGE` before PG `PR_PUBLISHED`, and merge APPLIED append-or-verifies `READY_FOR_MERGE→MERGED` before PG `MERGE_APPLIED`. NOT_APPLIED resolves and records its exact barrier; UNKNOWN retains it. Post-merge failure emits `PostMergeFailureV1`/`POST_MERGE_FAILED` without rewriting factual MERGED bytes.

## Task 5 — assurance escapes and learning evidence

Frozen obligations: `AG-PO-013`, `AG-PO-026`, `AG-PO-040`, `AG-PO-048`.

Persist `AssuranceEscapeV1` and mark A-required for foreseeable Critical/Major model escapes. Learning records never mutate live authority. No score/threshold bypass exists.

## Task 6 — deterministic dogfood and post-merge acceptance

Frozen obligations: `AG-PO-023..055` plus cumulative verification of every earlier obligation.

Execute J-01..J-05 exactly. J-05 uses the frozen four-file toy repository, exact plan, baseline commit `eb301906130c0d7ac13b7d90942d469753ab7708`, tree `6b50353385765d0809844bc6ef2415ce112f7e2b`, concrete floor/budget/broker/tool/profile vectors and digests, and the complete parent workflow+worker reservation including container/image/daemon charges. Delegation permits child A design/review only; child A creates its repository-bound model before B/C. PR/merge/deployment/nested dogfood remain forbidden.

Negative acceptance proves: unregistered/non-drained V3 store or exact `ISSUED`/`CONSUMING` lease blocks V4; tombstone blocks every later V1 CAS; cross-domain DB access fails; blob reuse cannot omit an occurrence; active/late publisher blocks stage close; 2,048 occurrences inventory completely; secret subset/diff/subject substitution fails; independent controllers cannot oversubscribe one worker and Docker daemon stays bounded; reviewer paths cannot mint correction authority; two correction grants only; PR/merge claim has one winner; pre-call loss makes no provider call, post-admission loss cannot retry; request/ledger/PG crash gaps reconcile without false state; wrong/missing digest/floor/evidence/profile/subject blocks its stage.

## Frozen non-goals

- no fourth capsule;
- no live V3 migration/grandfather continuation during V4 activation;
- no universal evidence class for every task beyond the frozen matrix;
- no reviewer-created mutation authority;
- no retroactive reinterpretation of historical V1/V2/V3 bytes;
- no replacement of deterministic acceptance with model review;
- no modification of `internal/githublifecycle/**` unless a future A scope expansion explicitly proves it necessary;
- no external-build readiness claim until controller enforcement + dogfood + post-merge acceptance are complete.

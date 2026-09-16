# Assurance / Proof-Obligation Governance Enforcement Plan

Status: **A_DESIGN correction candidate — implementation forbidden until fresh exact-head 0C/0M review and `DESIGN_ACCEPTED`**

## Goal

Make the assurance/proof-obligation policy fail-closed in ABCP before any new external code-bearing build. Exactly three capsules remain: A designs/freezes functional + assurance authority, B implements it, C performs branch acceptance/final review and owns the governed publication/merge sequence.

The controlling A model is `docs/plans/assurance-proof-obligation-governance-enforcement-assurance.md`. B mutation is forbidden until that exact model + this plan pass fresh independent 0 Critical / 0 Major A review.

## Task 1 — V4 contracts, drained activation, durable backend

Frozen obligations: `AG-PO-001..007`, `AG-PO-020..024`, `AG-PO-031`, `AG-PO-034`, `AG-PO-038`, `AG-PO-042`, `AG-PO-046..050`, `AG-PO-060`, `AG-PO-064`.

Implement the exhaustive strict-canonical field/digest/bound/predicate descriptor catalog and generated positive/per-constraint rejection vectors; extraction fails on any bare array, unresolved digest, unbounded field or unenumerated predicate. Implement the shared `PredecessorAuthorityDirectoryV1` for the exact workflow row, every bound ledger/run, process-startup PR admission root and merge state root. `internal/ledger/**` is explicitly in B authority because shared writer fencing and typed V4 effect barriers must be enforced at the ledger mutation boundary. Every V1 CAS/ledger/provider writer takes the registered shared cutover lease. `FencedWorkflowAuthorityBackendV1` wraps the exact selected predecessor backend, so the exclusive fence quiesces even a non-PostgreSQL predecessor before loading stable canonical V1 bytes. The SERIALIZABLE successor transaction proves exact lease states `{ISSUED,CONSUMING,CONSUMED}` and all barriers drained, preserves exact V1 bytes, tombstones later wrapped V1 CAS/provider admission, and installs V2 at `v1.revision+1`; grandfather set is empty. Implement the exact five-file manifest and executable forced-RLS role/function/policy model: schema/table owner, domain-function owner and worker-function owner are distinct NOLOGIN/NOBYPASSRLS roles, policies use validated `session_user`, and bootstrap verifies every owner/grant/policy/FORCE flag. No live V3 migration is permitted.

## Task 2 — A gate, capsule/grant/checkpoint authority, semantic corrections

Frozen obligations: `AG-PO-006`, `AG-PO-008..013`, `AG-PO-018..019`, `AG-PO-023..027`, `AG-PO-031`, `AG-PO-040`, `AG-PO-047..049`.

Implement `ContextCapsuleV4`, `PhaseAuthorityV4`, `PhaseCheckpointV2` and `StageGrantV2` as additive successors retaining every V3 capsule/source/outcome/parent field and every V1 grant/checkpoint blocker/invariant/operation/final-review floor. Use the frozen acyclic root bootstrap: A capsule without lineage, current checkpoint-core→A→B grant→full checkpoint sealing without lineage, then `AcceptedALineageV1`; every descendant carries it. Bind exact full parent checkpoint **and** grant. Preserve policy/model/registry/review plus independent proof-set, evidence-matrix, resource-profile-set and canonical-vector-catalog digests through A→B→C and failed-C→correction-grant→correction-B→replacement-C. Correction paths remain only `SemanticAuthorityRegistryV2 ∩ original B scope ∩ ControllerAffectedPathSetV1` from controller-recomputed accepted-B base→failed-candidate Git diff; reviewer bytes never supply paths.

## Task 3 — evidence, resource, artifact and secret execution

Frozen obligations: `AG-PO-013..016`, `AG-PO-020..025`, `AG-PO-027..036`, `AG-PO-039..041`, `AG-PO-050..052`, `AG-PO-061..063`.

Implement worker-scoped shared PostgreSQL admission/release across all domains/controllers/workflows. Every validator binds one flattened `MaterializedResourceProfileV1`; aggregate admission checks combined process+container+unique-daemon memory/PID/CPU/cache/log/image ceilings. J-05 children are inclusive suballocations: roots charge the worker once, own-plus-child and sibling sums cannot exceed the parent in any dimension. Separate immutable blobs from stage occurrences. Implement exact publisher open/append/finalize/crash-abandon operations under stage→publisher lock order; close returns bounded recovery work and fixes a cutoff only after zero ACTIVE publishers. Inventory remains ≤16 Merkle-linked pages ×128 entries, with closure artifacts outside the occurrence set. Derive `StageDiffLineageV1` by stage and require exact subject/base/candidate equality through candidate artifact, inventory, scan authority/evidence and rejection vectors. `CODEX_BROKER_V1` uses only the frozen profile/probes; no credential-bearing stage passes without full exact-pair zero-match evidence.

## Task 4 — C, integration, PR/merge effects and ledger handoff

Frozen obligations: `AG-PO-010..023`, `AG-PO-025..029`, `AG-PO-031..037`, `AG-PO-043`, `AG-PO-045`, `AG-PO-054..059`.

C executes only C cells against unchanged candidate and fresh exact-head review. `internal/integrationgate/**` must durably stop at `INTEGRATION_ACCEPTED`. PR intent binds that event/run/candidate and a deterministically derived `PREffectRequestV1`; merge intent binds exact PR-derived `READY_FOR_MERGE` and `MergeEffectRequestV1` containing the existing `TargetSubmissionV1`. Each request binds authenticated principal/application, repository, refs/OIDs, PR identity/document and exact origin/route/body; APPLIED observations must repeat them. Under `AcquireRunTransition`, install the typed embedded `EffectLedgerBarrierV4`; ledger append/removal accepts only exact `EffectLedgerTransitionAuthorityV1` or typed settlement/unclaimed proof. Provider admission revalidates exact source event under that lease. Implement one-winner claims and durable `CALL_POSSIBLE`; a crash before result creates only `EffectReconciliationAnchorV1`, then ordinal-1 read-only reconciliation. `EffectReauthorizationV1` permits `n+1` only after settled NOT_APPLIED plus exact resolution.

The append-only ledger remains run-state truth and PostgreSQL remains coordination authority. Exact PR APPLIED permits `MergeReadinessAuthorityV1` to create `ReadyForMergeEvidenceV1` and the sole authorized `INTEGRATION_ACCEPTED→READY_FOR_MERGE` event before PG `PR_PUBLISHED`; merge APPLIED creates only exact `READY_FOR_MERGE→MERGED` before PG `MERGE_APPLIED`. `internal/githublifecycle/**` receives only the additive bridge parser/authority needed to accept that evidence under retained EP-005 head/baseline/tree semantics; `internal/integrationgate/**` removes only its premature READY step. NOT_APPLIED resolves typed barrier proof; UNKNOWN retains it. Post-merge failure preserves factual MERGED bytes.

## Task 5 — assurance escapes and learning evidence

Frozen obligations: `AG-PO-013`, `AG-PO-026`, `AG-PO-040`, `AG-PO-048`.

Persist `AssuranceEscapeV1` and mark A-required for foreseeable Critical/Major model escapes. Learning records never mutate live authority. No score/threshold bypass exists.

## Task 6 — deterministic dogfood and post-merge acceptance

Frozen obligations: `AG-PO-023..064` plus cumulative verification of every earlier obligation.

Execute J-01..J-05 exactly. J-05 uses the frozen four-file toy repository, exact plan, baseline commit `eb301906130c0d7ac13b7d90942d469753ab7708`, tree `6b50353385765d0809844bc6ef2415ce112f7e2b`, concrete floor/budget/broker/tool/materialized-profile vectors and the complete inclusive parent workflow+worker reservation. Child reservations consume that parent and add zero worker-root charge; sibling overflow rejects. Delegation permits child A design/review only; child A creates its repository-bound model before B/C. PR/merge/deployment/nested dogfood remain forbidden.

Negative acceptance proves: unregistered/non-drained V3 blocks V4; tombstone blocks later V1 CAS; direct and SECURITY DEFINER cross-domain DB access fails; blob reuse cannot omit an occurrence; active/late publisher blocks close until exact recovery-abandon, and append cannot race close; 2,048 occurrences inventory completely; empty/unrelated/reversed/stale diff pair fails; split/weaker profile, cross-category overflow, nested double-charge and sibling overflow fail; reviewer paths cannot mint authority; two correction grants only; PR/merge has one winner; wrong principal/repository/body/PR/ref/route cannot settle APPLIED; pre-call loss makes no call; resultless CALL_POSSIBLE anchors without invented response; competing FAILED/CANCELLED cannot pass the typed barrier; integration cannot emit READY and non-PR-derived READY fails EP-005; every untyped/unbounded/unpredicated wire descriptor fails.

## Frozen non-goals

- no fourth capsule;
- no live V3 migration/grandfather continuation during V4 activation;
- no universal evidence class for every task beyond the frozen matrix;
- no reviewer-created mutation authority;
- no retroactive reinterpretation of historical V1/V2/V3 bytes;
- no replacement of deterministic acceptance with model review;
- no `internal/githublifecycle/**` change beyond additive `ReadyForMergeEvidenceV1` parsing/validation that preserves retained EP-005 semantics, and no `internal/integrationgate/**` change beyond stopping clean integration at `INTEGRATION_ACCEPTED`;
- no external-build readiness claim until controller enforcement + dogfood + post-merge acceptance are complete.

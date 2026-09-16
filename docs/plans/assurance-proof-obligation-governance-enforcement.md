# Assurance / Proof-Obligation Governance Enforcement Plan

Status: **A_DESIGN correction candidate — implementation forbidden until fresh exact-head design/assurance review and `DESIGN_ACCEPTED`**

## Goal

Turn `ASSURANCE_MODEL_AND_PROOF_OBLIGATION_POLICY.md` into fail-closed ABCP controller authority before ABCP governs new external code-bearing builds. Preserve exactly A_DESIGN → B_IMPLEMENTATION → C_ACCEPTANCE_MERGE; assurance is frozen A authority, not a fourth capsule or reviewer-authored context.

The complete A authority is `docs/plans/assurance-proof-obligation-governance-enforcement-assurance.md`. No B mutation may begin until this plan + that model receive a fresh exact-head 0 Critical / 0 Major A review and controller `DESIGN_ACCEPTED` authority.

## Task 1 — successor schemas, activation, and durable backend

Frozen obligations: `AG-PO-001` through `AG-PO-007`, `AG-PO-020` through `AG-PO-024`, `AG-PO-031`, `AG-PO-034`, `AG-PO-038`, `AG-PO-042`.

Implement the exact strict-canonical V4 records/digest preimages, immutable V1 predecessor-state preservation + one-time `ControllerStateV2` upgrade, controller-derived V3 grandfather rules, `GovernanceActivationV2`, and `PolicyUpgradeAuthorityV1`. Implement `PostgresWorkflowAuthorityBackendV1` exactly as frozen: pgx v5.7.6, exact schema/role/domain/bootstrap/CAS/hash/TLS/deadline contracts, and common durable CLI composition. Historical V1/V2/V3 bytes keep their original meaning.

Required proof includes canonical vectors, conflicting/replayed activation and bootstrap, concurrent state upgrade, V3 grandfather positive/negative transitions, and fresh-process PostgreSQL visibility/ambiguity reconciliation.

## Task 2 — A gate, graph completeness, and operational validity

Frozen obligations: `AG-PO-006`, `AG-PO-008` through `AG-PO-013`, `AG-PO-023`, `AG-PO-024`, `AG-PO-027`, `AG-PO-031`, `AG-PO-040`, `AG-PO-042`.

Default every lineage to code-bearing. Permit `documentation_only` only through controller-owned A classification and exact-diff revalidation. Implement the semantic assurance-graph validator so scenario → invariant → proof → cell → validator → stage/subject is closed and every J-01..J-05 is explicitly evidenced. Implement `HistoricalValid` and `OperationallyUsable` exactly as frozen; unrelated resource/evidence CAS revisions must not invalidate active stage authority.

A→B binds exact activated manifest/model/registry/resource-profile digests, allowed paths, evidence floors, final-review profile, execution ceilings, and the two-reentry correction bound. Missing/malformed authority blocks Ralphex before mutation.

## Task 3 — B evidence execution, resources, and secret containment

Frozen obligations: `AG-PO-013` through `AG-PO-016`, `AG-PO-020` through `AG-PO-025`, `AG-PO-027`, `AG-PO-028`, `AG-PO-032` through `AG-PO-036`, `AG-PO-039` through `AG-PO-041`.

Implement controller-issued resource reservations/ordinals, direct cgroup-v2 + prlimit/bwrap containment, cumulative elapsed/evidence/transient budgets, Docker PostgreSQL reservation/reconciliation, and the exact runner preflight. All Go tests use the non-vacuity wrapper with explicit uncached `--count`; cached/zero-match evidence is invalid.

Implement the secret-egress partition: secret-capable child raw output is not retained, credential material is inaccessible to Codex tool subprocesses under the required capability probe, and exact credential-value scanning of candidate diff + retained evidence must be zero-match before PASS. B convergence requires every B cell against one exact candidate and cannot alter the frozen A model.

## Task 4 — C acceptance, failed-stage correction, and merge-effect authority

Frozen obligations: `AG-PO-010` through `AG-PO-023`, `AG-PO-025` through `AG-PO-029`, `AG-PO-031` through `AG-PO-043`.

C independently executes only frozen C cells against one unchanged exact candidate and performs read-only exact-head final review. `ASSURANCE_MODEL_GAP` atomically returns A and grants zero correction authority. A sufficient-model `IMPLEMENTATION_FINDING` publishes `FailedStageV1`; a controller CAS may derive only the mapped `CorrectionBGrantV1`, sharing the cumulative maximum of two reentries across C-review and integration failures.

Enforce the exact V4 order: `BRANCH_ACCEPTED → FINAL_REVIEW_CLEAN → INTEGRATION_ACCEPTED → PR_PUBLISHED → MERGE_AUTHORIZED → MERGE_SUBMITTING`. PR publication before integration is invalid. The last pre-provider authority is one atomic one-use `MergeEffectLeaseV1`; invalidation and lease issuance race on one predecessor tip. `internal/mergelifecycle` may call the provider only with that lease. Ambiguous submission is reconciliation-only and cannot silently retry.

## Task 5 — assurance escapes and learning ratchet

Frozen obligations: `AG-PO-013`, `AG-PO-026`, `AG-PO-040`, `AG-PO-041`.

Persist assurance escapes with missed failure class/scenario, affected invariants/proofs, discovery stage, exact candidate, and required A/policy action. Every foreseeable Critical/Major assurance-model escape marks A-required; no review score or numeric threshold bypasses that return. Learning evidence never mutates active-lineage authority.

## Task 6 — full dogfood and lifecycle acceptance

Frozen obligations: `AG-PO-023` through `AG-PO-044` plus cumulative verification of every earlier obligation.

Execute J-01..J-05 exactly. J-05 uses the frozen toy-repository fixture/task/digests, exact parent-reserved `DOGFOOD_V1` budget, parent-issued `DOGFOOD_DELEGATED` V4 child activation, official Ralphex `319e306…` / binary digest `9ad47b…`, and Codex CLI 0.149.0 digest `134063…`, model `gpt-5.6-sol`, effort `xhigh`. Child publication/merge/nested dogfood are forbidden. Integration PASS is mandatory before PR publication and merge authorization.

Negative acceptance must prove wrong/missing evidence blocks its owning stage; cached/zero-match tests fail; wrong-stage/subject/attempt evidence fails; false docs-only classification fails; secret egress fails; a deliberately insufficient assurance model returns A; two correction grants are allowed and a third is denied after restart; lineage budgets cannot reset; merge invalidation wins before effect-lease issuance or, once a lease wins, ambiguous external effect becomes mandatory read-only reconciliation rather than retroactive correction/retry.

## Frozen non-goals

- no fourth capsule or mutable reviewer authority;
- no universal requirement to run every evidence class when A marks it inapplicable with reviewed rationale;
- no retroactive reinterpretation of historical V1/V2/V3 bytes;
- no replacement of deterministic smoke/integration/acceptance with model review;
- no external GitHub merge in J-05 and no production-service deployment claim;
- no claim of external-build readiness until controller enforcement, exact-head closure, dogfood, integration, merge, and post-merge acceptance are complete.

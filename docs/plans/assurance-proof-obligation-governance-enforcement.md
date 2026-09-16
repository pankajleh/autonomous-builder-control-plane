# Assurance / Proof-Obligation Governance Enforcement Plan

Status: **A_DESIGN correction candidate — implementation forbidden until fresh exact-head 0C/0M review and `DESIGN_ACCEPTED`**

## Goal

Make the assurance/proof-obligation policy fail-closed in ABCP before any new external code-bearing build. Exactly three capsules remain: A designs/freezes functional + assurance authority, B implements it, C performs branch acceptance/final review and owns the governed publication/merge sequence.

The controlling A model is `docs/plans/assurance-proof-obligation-governance-enforcement-assurance.md`. B mutation is forbidden until that exact model + this plan pass fresh independent 0 Critical / 0 Major A review.

## Task 1 — V4 contracts, drained activation, durable backend

Frozen obligations: `AG-PO-001..007`, `AG-PO-020..024`, `AG-PO-031`, `AG-PO-034`, `AG-PO-038`, `AG-PO-042`, `AG-PO-046..050`.

Implement all strict-canonical V4 records/digest vectors, `V3Drained`, immutable predecessor V1 artifact preservation, V2 revision=`v1+1`, empty grandfather set, exact five-file policy manifest, final-review/semantic registry authority, and `PostgresWorkflowAuthorityBackendV1` with forced-RLS domain isolation + immutable artifact store. pgx is exactly v5.7.6. No live V3 migration is permitted.

## Task 2 — A gate, capsule/grant/checkpoint authority, semantic corrections

Frozen obligations: `AG-PO-006`, `AG-PO-008..013`, `AG-PO-018..019`, `AG-PO-023..027`, `AG-PO-031`, `AG-PO-040`, `AG-PO-047..049`.

Implement `ContextCapsuleV4`, `PhaseAuthorityV4`, `PhaseCheckpointV2`, `StageGrantV2`, work classification and exact graph validation. A→B/B→C preserve policy/model/registry/review bindings plus the independent proof-obligation-set, evidence-matrix and resource-profile-set digests. Correction-B paths must be derived only from frozen `SemanticAuthorityRegistryV2 ∩ original B scope ∩ ControllerAffectedPathSetV1`, where the controller recomputes changed paths from the exact accepted-B base→failed-candidate Git diff; failed-stage/reviewer bytes never supply path authority.

## Task 3 — evidence, resource, artifact and secret execution

Frozen obligations: `AG-PO-013..016`, `AG-PO-020..025`, `AG-PO-027..036`, `AG-PO-039..041`, `AG-PO-050..052`.

Implement validator-bound resource/secret profiles, lineage+worker aggregate reservation, full process/container accounting, immutable artifact inventory, non-vacuous uncached Go wrapper, complete stage-local secret scans, and the exact PostgreSQL/Docker/Codex credential trust partition. Codex provider access uses only `CODEX_BROKER_V1`: real upstream credentials stay in the controller-owned loopback broker, Codex receives a short-lived broker session capability, and harmless preflight probes must prove shell-env, parent-process and loopback isolation before any real provider session. Credential-bearing B/C/integration/post-merge stages cannot pass without complete zero-match inventory evidence.

## Task 4 — C, integration, PR/merge effects and ledger handoff

Frozen obligations: `AG-PO-010..023`, `AG-PO-025..029`, `AG-PO-031..037`, `AG-PO-043`, `AG-PO-045`, `AG-PO-054..055`.

C executes only C cells against unchanged candidate and fresh exact-head review. Enforce integration before PR. Implement one-winner ephemeral-token `PREffectClaimV1` and `MergeEffectClaimV1`; minimally integrate the claims into `internal/prlifecycle/**` and `internal/mergelifecycle/**` without changing `internal/githublifecycle/**`. Provider ambiguity is reconciliation-only.

The append-only ledger remains run-state truth. PostgreSQL is coordination authority and every effect/stage binds an exact ledger event. Merge requires exact READY event + PG claim; terminal ledger event is durable before PG settlement; post-merge failure emits `PostMergeFailureV1` without rewriting factual merge state.

## Task 5 — assurance escapes and learning evidence

Frozen obligations: `AG-PO-013`, `AG-PO-026`, `AG-PO-040`, `AG-PO-048`.

Persist `AssuranceEscapeV1` and mark A-required for foreseeable Critical/Major model escapes. Learning records never mutate live authority. No score/threshold bypass exists.

## Task 6 — deterministic dogfood and post-merge acceptance

Frozen obligations: `AG-PO-023..055` plus cumulative verification of every earlier obligation.

Execute J-01..J-05 exactly. J-05 uses the frozen four-file toy repository including exact `docs/plans/unique-sorted.md`, baseline commit `eb301906130c0d7ac13b7d90942d469753ab7708`, tree `6b50353385765d0809844bc6ef2415ce112f7e2b`, parent `DogfoodPolicyFloorV1`, child A-only delegation, child-created repository-bound model, exact `CODEX_BROKER_V1` isolation profile/probes, pinned Ralphex/Codex identities and exact child budget. PR/merge/nested dogfood remain forbidden inside child.

Negative acceptance proves: non-drained V3 blocks V4; cross-domain DB credential cannot read/update another domain; missing immutable artifact blocks fresh host; caller cannot weaken resource/secret profile; failed/unselected/orphan secret artifact cannot evade scan; reviewer paths cannot mint correction authority; two correction grants only; PR/merge claim has one winner; lost claim cannot retry; ledger↔PG crash gaps reconcile without false state; wrong/missing evidence/profile/subject blocks its stage.

## Frozen non-goals

- no fourth capsule;
- no live V3 migration/grandfather continuation during V4 activation;
- no universal evidence class for every task beyond the frozen matrix;
- no reviewer-created mutation authority;
- no retroactive reinterpretation of historical V1/V2/V3 bytes;
- no replacement of deterministic acceptance with model review;
- no modification of `internal/githublifecycle/**` unless a future A scope expansion explicitly proves it necessary;
- no external-build readiness claim until controller enforcement + dogfood + post-merge acceptance are complete.

# EP-005 Foundation Controller Major Corrections

## Overview
Correct three controller-review Majors on exact accepted foundation head `6d8f4e7760e0213c9febf36b648ff4d4054eed24`. Scope remains the Phase 4 network-free GitHub lifecycle foundation only.

## Review authority
- ABCP accepted exact head `6d8f4e7760e0213c9febf36b648ff4d4054eed24`.
- Claude exact-head review returned `CLEAN_CRITICAL_MAJOR`.
- Controller exact-head review found three substantive Majors; these findings are not provider failure and must be corrected.
- Controller review artifact: `/home/devagent/abcp-runtime/ep005-foundation/controller-final-review.json`, SHA256 `54a7b765ed37c82deaa3defe56139635c2cba91a1b32aa1075a623c0ae73e5a9`.
- Pre-implementation Claude design review found three residual Major design gaps in the first correction draft (operation scoping, expected-tree derivation authority, and validator limit re-scan); the revised design closed them.
- Final independent design re-review verdict: `DESIGN_CLEAN_CRITICAL_MAJOR`.

## Major 1 — Bind write-attempt identity end to end
A remote write must have one immutable `WriteAttempt` created before submission and bound into the exact write request, provider result, execution error, and reconciliation request/result. The attempt must include a typed operation kind (`pr_upsert` or `merge`), write ID, authority digest, and canonical operation-payload digest (computed over the write payload excluding the attempt itself, avoiding circular hashing). `UpsertPullRequestInput` and `MergeInput` may not omit it, and successful write results may not invent or replace it.

Required invariant:
`authority + repository + actor + operation_kind + write_id + canonical_payload_sha256 -> provider submission -> result/error -> reconciliation` uses the same identity throughout. Reconciliation and `CanRetry` must compare all of these bound fields, so evidence for one operation/payload can never authorize replay of another.

## Major 2 — Controller-owned expected merge content
Provider-supplied SHA/tree/lineage assertions are observations, not authority. Before any merge write, controller authority must bind an independently derived expected final content identity for the exact accepted head and expected base tip.

Freeze a copy-safe `ExpectedMergeContent` (or equivalent) containing the exact expected result tree SHA plus its deterministic derivation identity. The authoritative source is the already accepted Phase 3 integration target behind `READY_FOR_MERGE`, not GitHub/provider data: the controller takes the exact Phase 3 integrated-head SHA and its immutable integration evidence, resolves that exact commit to its tree with pinned local Git under replacement-ref-resistant settings, and records a versioned derivation policy identity such as `phase3-ready-tree-v1`, the source integrated-head SHA, source integration evidence digest/ref, pinned Git identity, and resulting tree SHA.

`Authority`, `MergeInput`, `MergeResult`, and `PostMergeObservation` must bind this expected-content object. For `merge`, `squash`, and `rebase`, the observed/result tree must equal the Phase-3-accepted expected tree; provider lineage remains useful provenance but can never substitute for that equality. A missing/unverifiable Phase 3 derivation blocks merge authority rather than accepting a caller/provider assertion.

## Major 3 — Bind resource-limit policy across provider boundary
The exact validated `Limits` policy used by the controller must have a deterministic identity and be bound to provider operations and returned snapshots/results. A provider must not be able to construct a result under looser limits and have it accepted under stricter controller intent.

Required invariant:
`controller limit policy -> provider input -> bounded result -> controller revalidation` uses one exact limits-policy identity. `Limits` must expose deterministic canonical JSON/SHA256 over every governed field. Provider inputs and all returned snapshots/results bind that SHA256. `ValidatePullRequest`, `ValidateCI`, `ValidateMergeResult`, `VerifyPostMerge`, `ValidatePullRequestWriteResult`, and PR selection must accept the controller's expected `Limits`, require the embedded policy identity to match, and independently re-scan actual collection/text/evidence/metadata/parent/lineage cardinalities under those expected limits rather than only trusting constructors.

## Pre-implementation failure/threat matrix
- wrong/missing write ID, operation kind, authority digest, or payload digest in request/result/reconciliation -> fail closed;
- ambiguous write reconciled under another write ID/actor/repository -> no retry authority;
- Phase 3 expected-tree derivation/evidence missing, mismatched, or unverifiable -> fail closed;
- provider reports valid-looking result tree different from Phase-3-derived expected controller tree -> fail closed;
- provider reports matching lineage but wrong expected tree -> fail closed;
- limits identity missing/mismatched -> fail closed;
- object created under looser limits and validated under stricter limits -> fail closed;
- mutation of caller-owned evidence/lineage/limits-related data -> immutable result unchanged;
- no network calls, state transitions, PR writes, merges, or Phase 5+ behavior in this correction.

### Task 1: Correct the frozen GitHub lifecycle foundation
- [x] Add one immutable write-attempt identity to every PR/merge write request and successful result. It must bind typed operation kind, write ID, authority SHA256, and canonical operation-payload SHA256; bind execution errors and reconciliation to the exact same tuple and require `CanRetry` to compare it field-for-field.
- [x] Add authority-owned expected merge content derived only from exact Phase 3 READY_FOR_MERGE/integration provenance: versioned `phase3-ready-tree-v1` derivation identity, source integrated-head SHA, source integration evidence identity/ref, pinned replacement-ref-resistant Git identity, and exact expected result tree. Bind it into Authority/write/result/observation and require all merge methods to match that expected tree independently of provider lineage claims.
- [x] Give validated resource limits deterministic canonical JSON/SHA256 and bind that identity through provider inputs/snapshots/results. Change all controller validators (`ValidatePullRequest`, `ValidateCI`, `ValidateMergeResult`, `VerifyPostMerge`, `ValidatePullRequestWriteResult`, and PR selection) to accept expected `Limits`, require identity equality, and independently revalidate actual collections/text/evidence/metadata/parents/lineage under those limits.
- [x] Update provider interfaces/constructors without adding network implementation.
- [x] Update `GITHUB_LIFECYCLE_FOUNDATION_CONTRACT.md` to freeze the corrected contract.
- [x] Add adversarial regressions for cross-operation/same-write-ID reconciliation, wrong payload/authority digest, forged/missing write identity, mismatched reconciliation, missing/forged Phase 3 tree derivation, provider-self-certified wrong tree, limits-policy mismatch, looser-provider/stricter-controller validation, and defensive-copy behavior.
- [x] Preserve prior exact-head/base-tip/acting-principal/cancellation/retry/strategy-aware tests.
- [x] Run `gofmt`, focused tests, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`.

## Non-goals
No GitHub network provider, PR creation/update execution, CI polling, merge execution, ledger state transition, Phase 5 API/dashboard, deployment, or production acceptance.

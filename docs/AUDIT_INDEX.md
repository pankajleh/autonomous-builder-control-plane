# Audit Index

Operational role: this file indexes accepted, reviewed, and merged evidence identities. It must be consulted before planning or starting governed work. It is a human-readable projection and pointer set, not a substitute for controller ledgers, immutable evidence artifacts, verified capsules/authorities, or Git object/ref evidence.

## Accepted, reviewed, and merged implementation identities

| Scope | PR | Accepted/reviewed head | Merge SHA | Review evidence |
|---|---:|---|---|---|
| EP-002 | #4 | `0668397491394964d06ddf7ad00ae8032b2ac49d` | `f7ca8e6e59f04e7bece0582f3febcd37ec2cb4f7` | Claude cross-model clean after correction |
| EP-003 | #5 | `f345a910ff14c15be3fdc872eab89c13c5b89caa` | `db56b1f8cf32561be6b707db4bbf046f4c24e067` | Controller fallback after Claude capacity failure; prior Claude Majors corrected |
| EP-004 | #6 | `d3193cf5615c5ea33e2f74398519106863ed4b06` | `94e14ca749d31ac214e979aab03fbde37502dd7f` | Controller fallback after Claude session failure; prior Claude/controller Majors corrected |
| EP-005 exact-head PR lifecycle | #8 | `6db075240ce87b751f8db98abb540c96410c515b` | `ccf75d093625119cc39944fe7a47c3a03b30ad3b` | Controller fallback 0C/0M artifact SHA-256 `e34a632e290930bbfcccfbd4326d5aeab1b1d8012285d67a122a87b55d8196c5` |
| Context-bound autonomous operations | #11 | `84c6a8ee4b6d6a315eeaa1e7de17fbf5f94a1dec` | `454ea4dce3c674e0d8e55273319cfb4ea4a077a5` | Exact-head 0C/0M artifact SHA-256 `85405bc64c0703319cad881d4b4aa3f07ba7aa91fb97ef16b8172dcb56beebe5` |
| Operational state projections | #12 | `2ab636f2b118e7e77bcb6656a0afb163f2082461` | `e11afb7d7356a0df36566d98c34adbd07a0097ae` | Exact-head 0C/0M artifact SHA-256 `ab16a9b1711d54391dbe317078879da765aca6de36626f1a95e7ad274546dd78` |

EP-005 foundation merged in PR #7 at `bf5f923f1743b541fac8ad75fa173557fe68ba0f` before the exact-head PR lifecycle subtrack.

## Current-policy evidence

- Context-bound operation policy candidate: `84c6a8ee4b6d6a315eeaa1e7de17fbf5f94a1dec`.
- Policy controller result: `BRANCH_ACCEPTED`, with final-Git evidence SHA-256 `1de3bb8bf42f8a9e231d0d9e4ef0b699c8ac3534fd66778c03b316b28a48ee5c`.
- Policy final review: `CLEAN_CRITICAL_MAJOR`, 0 Critical and 0 Major, artifact SHA-256 `85405bc64c0703319cad881d4b4aa3f07ba7aa91fb97ef16b8172dcb56beebe5`.
- Policy merge: PR #11 at `454ea4dce3c674e0d8e55273319cfb4ea4a077a5`.
- Projection correction candidate: `2ab636f2b118e7e77bcb6656a0afb163f2082461`.
- Projection controller result: `BRANCH_ACCEPTED`, with final-Git evidence SHA-256 `c28140c17923fe3f68ea0f5d16488752c7a22779784c0ffcbbd7845264ed64d4`.
- Projection final review: `IMPLEMENTATION_CLEAN_CRITICAL_MAJOR`, 0 Critical and 0 Major, artifact SHA-256 `ab16a9b1711d54391dbe317078879da765aca6de36626f1a95e7ad274546dd78`.
- Projection merge: PR #12 at `e11afb7d7356a0df36566d98c34adbd07a0097ae`.

These identities establish the current universal operation rule: each new governed operation requires a fresh verified `context-capsule-v2` and immutable authority bound to its exact base and sources.

## EP-005 CI evidence-ingestion evidence

The initial CI evidence-ingestion design SHA-256 `d90099eeab3af74ea5dd25f50b7e87f920f7cb47eb523e4e7112882dc0524276` was rejected after review found 1 Critical and 10 Major findings; review artifact SHA-256 `659174158aecc5a690663f79e8a2b567a459b77af0cf08042d37cdfb6db265f3`. The corrected neutral, read-only design is frozen at SHA-256 `47a6d7b1d4c93a853a26e4da3753894dedacfce930345c610cf24a4236b2b409`; final bounded review returned 0 Critical and 0 Major, artifact SHA-256 `aa5d86471ffcfe1d68eff9c49ef5b2df718355c0b4c825716d35d68217b3fa45`.

| CI checkpoint | Commit | Evidence and lifecycle status |
|---|---|---|
| Task 1 — v1 evidence contract and bounds | `e1740f4be8df06571a3299c3fb31ecc31b0a1aea` | Implemented predecessor checkpoint |
| Task 2 — bounded GitHub reads and stabilization | `da8ffea4582539067724b363b3144d9601dee086` | Deterministic acceptance PASS; result SHA-256 `03d6f291f7606854d214b718892434a9f42eaab809e6da4bca0c431a047356d3`; final-Git evidence SHA-256 `06a404b5ed0d300b7a643f1df75929cf75f8d41f869d8a10313970a9773021f0` |
| Task 2 current-policy reconciliation and correction | `1e3e84bb30a60ed3e47c2ca765c1ef2be8042b56` | Exact clean head reached `BRANCH_ACCEPTED` after all 14 correction acceptance gates passed; final-Git evidence SHA-256 `3e424d9fc1d18682b8c410a0cb5288988cddb3ef3552f153a879b5f5ade1300c`; events ledger SHA-256 `51764b8e0a038b42ab2cd99a85822015de6ac643bf9ca8f3f2d9860c6a8b241a` |
| Task 3 predecessor projection | resulting accepted exact head | Resolve this projection candidate's accepted exact head from Git/controller evidence after commit; these frozen bytes cannot self-record that later acceptance |
| Task 3 — immutable evidence, ledger, replay | none | Not started; the resulting accepted exact projection head is the predecessor and requires fresh Task-3-only v2 authority |

At accepted correction head `1e3e84bb30a60ed3e47c2ca765c1ef2be8042b56`, current-policy-owned `internal/run`, `internal/context`, `internal/authority`, and `cmd/abcp` are byte-identical to merged main `e11afb7d7356a0df36566d98c34adbd07a0097ae`, and accepted Task 2 `internal/cilifecycle` remains byte-identical to `da8ffea4582539067724b363b3144d9601dee086`.

The accepted Task 1/Task 2 implementation remains neutral evidence collection and bounded observational stability only. No evidence here authorizes merge, defines approval policy, protects an expected head, executes a merge, or performs post-merge acceptance. Task 3 is the sole next executable operation after its fresh authority is bound to the resulting accepted projection head. Task 4 requirements remain deferred and non-executable until Task 3 completes under its own authority.

## Projection cutoff and reconciliation rule

The explicit immutable-evidence cutoff for this projection candidate is the accepted CI Task 2/current-policy correction at exact clean head `1e3e84bb30a60ed3e47c2ca765c1ef2be8042b56`. All 14 correction acceptance gates passed; final-Git evidence SHA-256 is `3e424d9fc1d18682b8c410a0cb5288988cddb3ef3552f153a879b5f5ade1300c`, and events ledger SHA-256 is `51764b8e0a038b42ab2cd99a85822015de6ac643bf9ca8f3f2d9860c6a8b241a`. The candidate materializes all required evidence through Task 2 deterministic acceptance and the accepted, reviewed, and merged PR #12 boundary at `e11afb7d7356a0df36566d98c34adbd07a0097ae` while preserving both byte-identity invariants above.

It cannot self-record its later commit, acceptance, review, or merge identities. Those events become authoritative immediately in immutable evidence and are materialized in the next separately governed reconciliation. Resolve this candidate's resulting accepted exact head from Git/controller evidence after commit; that head is the Task 3 predecessor. Before any non-reconciliation governed operation, cross-check this index with `CURRENT_STATE.md`, `PROGRESS.md`, immutable controller evidence, and Git; stale-through-boundary or contradictory projections block planning.

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
| EP-005 CI Task 3 — immutable evidence, ledger, replay | #13 | `3e0f295e8f98c348d684b2c026bacd9ceeeea911` | `bf2482f756a7c5f75906825b8f3e6e454c72f94f` | Exact-head 0C/0M artifact SHA-256 `94cd9d4cf4173a2ecf59743aa7892c8b6e091d4eaf70b67cfcc8d18d7c5caebb` |

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
| Task 3 predecessor projection | `81c0597f287026e501f5f80a4b26837c50696f7e` | Exact accepted predecessor used by the fresh Task-3-only v2 capsule and immutable authority |
| Task 3 — immutable evidence, ledger, replay | `b5c7e2cd0ea3cc223f481b1d73a78c6276846639` | Exact clean head reached `BRANCH_ACCEPTED` after all 16 deterministic gates passed; final-Git evidence SHA-256 `24f3ceebf733c596c9638f9d9693b2fa49375d3721222a45dc4cc565e00cd1b7`; exact-head review `IMPLEMENTATION_FINDINGS`, 0 Critical / 5 Major, artifact SHA-256 `d0a3f692f19d9dc5b42a13abf8c71bbd18469a57d8920183ca86345953ccf6f3`; review-blocked |
| Task 3 first correction | `39db34bd4f04dd6b85b9e5444eee86a2f1ea1b05` | Exact clean head reached `BRANCH_ACCEPTED` after all 16 required gates passed; final-Git evidence SHA-256 `14e6500bce762de792cefd2c509188e83dde799d438dd053cb306659d63214ef`; correction-run ledger SHA-256 `84cc4756ea8cf6f27d498977a6211da32c32ae68f3a72e5c0b6d534ff193b027`; fresh exact-head review `IMPLEMENTATION_FINDINGS`, 0 Critical / 3 Major, artifact SHA-256 `be18740a9c60d110d9acd4487d70ba0c21fafd4485b7d046e39693f5312e6364`; superseded by final correction |
| Task 3 final correction | `3e0f295e8f98c348d684b2c026bacd9ceeeea911` | Exact clean head reached `BRANCH_ACCEPTED`; all 17 required acceptance entries recorded successful; final-Git evidence SHA-256 `d809846a7f6133adca513e46b531101dac428c8c0707035dd1ec298e4634726b`; controller ledger SHA-256 `9f7d41498c984e8dfce7f762fc9d616e50ccc2b70f50b24b322339e751c51870`; fresh exact-head review `CLEAN_CRITICAL_MAJOR`, 0 Critical / 0 Major, artifact SHA-256 `94cd9d4cf4173a2ecf59743aa7892c8b6e091d4eaf70b67cfcc8d18d7c5caebb`; PR #13 merge `bf2482f756a7c5f75906825b8f3e6e454c72f94f` |

At accepted correction head `1e3e84bb30a60ed3e47c2ca765c1ef2be8042b56`, current-policy-owned `internal/run`, `internal/context`, `internal/authority`, and `cmd/abcp` are byte-identical to merged main `e11afb7d7356a0df36566d98c34adbd07a0097ae`, and accepted Task 2 `internal/cilifecycle` remains byte-identical to `da8ffea4582539067724b363b3144d9601dee086`.

Acceptance commands 14 and 15 for the final correction used `! rg ...` while `rg` was unavailable. Their shell exit statuses alone are not substantive proof of forbidden-semantics absence; the independent exact-head reviewer rechecked the production diff and verified that no forbidden merge-policy, write, or transition semantics were introduced.

PR #13 merged exact accepted/reviewed head `3e0f295e8f98c348d684b2c026bacd9ceeeea911` into `main` as `bf2482f756a7c5f75906825b8f3e6e454c72f94f`. The merge parents are previous main `e11afb7d7356a0df36566d98c34adbd07a0097ae` and that exact reviewed head.

The accepted Task 1–3 implementation remains neutral evidence collection, immutable evidence/replay, and bounded observational stability only. CI Task 3 is accepted, reviewed, merged, and complete. Deferred Task 4 — acceptance, scope audit, and exact-head review handoff — is the sole next eligible CI operation; no Task 4 implementation or checkpoint exists yet. No evidence here authorizes publication or later Phase-4 work, defines merge approval policy, protects an expected head, executes a merge, performs post-merge acceptance, or implements automatic operation handoff.

## Final Task 3 review dispositions

The final exact-head review artifact at SHA-256 `94cd9d4cf4173a2ecf59743aa7892c8b6e091d4eaf70b67cfcc8d18d7c5caebb` returned `CLEAN_CRITICAL_MAJOR`, 0 Critical and 0 Major, with final marker `IMPLEMENTATION_CLEAN_CRITICAL_MAJOR`. Its dispositions are:

| ID | Review status | Exact reviewed scope |
|---|---|---|
| M-001 | CLOSED | New reservations remain durably provisional until ledger and bundle absence checks succeed; scan failures retain provisional state, while discovered material durably poisons the reservation. |
| M-002 | PRESERVED | Immutable publication still verifies temporary-link removal, final single-link identity, file durability, directory durability, and exact readback. |
| M-003 | CLOSED | Ledger lookup locks, scans, successfully fsyncs the ledger and parent directory, and reconfirms the exact event before replay. |
| M-004 | PRESERVED | Existing canonical reservations still require successful file and pinned-directory sync, named-inode revalidation, and exact-byte reread. |
| M-005 | PRESERVED | Inventory remains bounded to the configured limit plus sentinel, and process-local keyed locks remain reference-counted and removed on release. |
| M-006 | CLOSED | The non-Linux `attemptLease.poison` stub fails closed with `errUnsupportedCIDurability`; exact acceptance metadata records the dedicated Darwin/amd64 compilation gate succeeding. |

## Projection cutoff and reconciliation rule

The explicit immutable-evidence cutoff for this projection candidate is final Task 3 acceptance and exact-head review at exact clean head `3e0f295e8f98c348d684b2c026bacd9ceeeea911`, followed by PR #13 merge `bf2482f756a7c5f75906825b8f3e6e454c72f94f`. All 17 required acceptance entries were recorded successful; final-Git evidence SHA-256 is `d809846a7f6133adca513e46b531101dac428c8c0707035dd1ec298e4634726b`, and controller ledger SHA-256 is `9f7d41498c984e8dfce7f762fc9d616e50ccc2b70f50b24b322339e751c51870`. The review artifact SHA-256 is `94cd9d4cf4173a2ecf59743aa7892c8b6e091d4eaf70b67cfcc8d18d7c5caebb`, with verdict `CLEAN_CRITICAL_MAJOR`, 0 Critical and 0 Major. The merge parents are previous main `e11afb7d7356a0df36566d98c34adbd07a0097ae` and the exact reviewed head. The candidate also preserves the original Task 3, earlier accepted Task 2/current-policy correction, and accepted, reviewed, and merged PR #12 boundary.

This reconciliation cannot self-record its later commit, acceptance, review, or merge identities. Those events become authoritative immediately in immutable evidence and are materialized in the next separately governed reconciliation. Resolve this candidate's resulting accepted exact head from Git/controller evidence after commit before issuing Task-4-only authority. Before any non-reconciliation governed operation, cross-check this index with `CURRENT_STATE.md`, `PROGRESS.md`, immutable controller evidence, and Git; stale-through-boundary or contradictory projections block planning.

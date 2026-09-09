# Current Project State

Date: 2026-09-09

Operational role: this file is the present checkpoint and authority projection for governed work. It must be consulted before planning or starting an operation. It summarizes controller and repository evidence; it does not replace immutable authority, capsule, ledger, acceptance, review, or Git evidence.

## Repository checkpoint and current authority

- Repository: `pankajleh/autonomous-builder-control-plane`.
- Active implementation branch: `ep-005-ci-ingestion`.
- Current-policy merge boundary: exact `origin/main` commit `e11afb7d7356a0df36566d98c34adbd07a0097ae`, the merge commit for PR #12 and descendant of PR #11 merge `454ea4dce3c674e0d8e55273319cfb4ea4a077a5`.
- This Task 3 correction-review materialization operation began from exact clean base `91585261fcde05874a8750f5840904ada69fbb82`.
- Its fresh `context-capsule-v2` at `/home/devagent/abcp-runtime/ep005-ci-task3-correction-review-materialization/context.json` has exact byte SHA-256 `05b16a3a62b92f2afd808b435de2c5007095a5b2d49853d1d1df42cfa0f8f779`; independent verification confirmed the base, implementation operation kind, internal capsule SHA-256 `87cd459e080189f6ad5dfb4a0b03f2c4b51862b07ce22e7e9d0e16a7797b5341`, and all 7 bound source hashes before editing.
- Current roadmap phase: Phase 4 — GitHub lifecycle.
- Current execution pack: EP-005 — GitHub Lifecycle.
- Canonical roadmap: `docs/roadmap/IMPLEMENTATION_ROADMAP.md`.
- Execution-pack authority: `docs/execution-packs/EP-005-github-lifecycle.md`.

## Accepted and merged governance checkpoints

The context-bound operation policy candidate `84c6a8ee4b6d6a315eeaa1e7de17fbf5f94a1dec` reached controller `BRANCH_ACCEPTED`. Its final exact-head implementation review returned `CLEAN_CRITICAL_MAJOR` with 0 Critical and 0 Major; review artifact SHA-256 `85405bc64c0703319cad881d4b4aa3f07ba7aa91fb97ef16b8172dcb56beebe5`. PR #11 merged that candidate as `454ea4dce3c674e0d8e55273319cfb4ea4a077a5`.

The operational-state projection correction candidate `2ab636f2b118e7e77bcb6656a0afb163f2082461` reached controller `BRANCH_ACCEPTED`, with final-Git evidence SHA-256 `c28140c17923fe3f68ea0f5d16488752c7a22779784c0ffcbbd7845264ed64d4`. Its exact-head review returned `IMPLEMENTATION_CLEAN_CRITICAL_MAJOR` with 0 Critical and 0 Major; review artifact SHA-256 `ab16a9b1711d54391dbe317078879da765aca6de36626f1a95e7ad274546dd78`. PR #12 merged that candidate as `e11afb7d7356a0df36566d98c34adbd07a0097ae`.

These merged policies require every new governed operation to use a purpose-specific verified v2 capsule and immutable authority at its exact operation base. A plan/source/base or commit/HEAD change requires a fresh operation capsule and authority; prior authority cannot cross that boundary.

## Active roadmap track: EP-005 CI evidence ingestion

The corrected read-only CI evidence-ingestion design is frozen at SHA-256 `47a6d7b1d4c93a853a26e4da3753894dedacfce930345c610cf24a4236b2b409`. Its bounded design review returned 0 Critical and 0 Major; review artifact SHA-256 `aa5d86471ffcfe1d68eff9c49ef5b2df718355c0b4c825716d35d68217b3fa45`.

- Task 1, the v1 evidence contract and bounds, is implemented at `e1740f4be8df06571a3299c3fb31ecc31b0a1aea`.
- Task 2, bounded GitHub reads and two-sweep stabilization, is implemented at `da8ffea4582539067724b363b3144d9601dee086` and passed deterministic acceptance at that exact clean head. The acceptance result SHA-256 is `03d6f291f7606854d214b718892434a9f42eaab809e6da4bca0c431a047356d3`; final-Git evidence SHA-256 is `06a404b5ed0d300b7a643f1df75929cf75f8d41f869d8a10313970a9773021f0`.
- The Task 2/current-policy acceptance correction reached controller `BRANCH_ACCEPTED` at exact clean head `1e3e84bb30a60ed3e47c2ca765c1ef2be8042b56` after all 14 correction acceptance gates passed. Its final-Git evidence SHA-256 is `3e424d9fc1d18682b8c410a0cb5288988cddb3ef3552f153a879b5f5ade1300c`; its events ledger SHA-256 is `51764b8e0a038b42ab2cd99a85822015de6ac643bf9ca8f3f2d9860c6a8b241a`.
- At that accepted correction head, current-policy-owned `internal/run`, `internal/context`, `internal/authority`, and `cmd/abcp` are byte-identical to merged main `e11afb7d7356a0df36566d98c34adbd07a0097ae`, while accepted Task 2 `internal/cilifecycle` remains byte-identical to `da8ffea4582539067724b363b3144d9601dee086`.
- Task 3, immutable evidence, ledger outcome, and replay, reached controller `BRANCH_ACCEPTED` at exact clean technical head `b5c7e2cd0ea3cc223f481b1d73a78c6276846639` after all 16 deterministic gates passed. Its final-Git evidence SHA-256 is `24f3ceebf733c596c9638f9d9693b2fa49375d3721222a45dc4cc565e00cd1b7`.
- The fresh exact-head implementation review of `b5c7e2cd0ea3cc223f481b1d73a78c6276846639` returned exactly `IMPLEMENTATION_FINDINGS`, 0 Critical and 5 Major. The review artifact SHA-256 is `d0a3f692f19d9dc5b42a13abf8c71bbd18469a57d8920183ca86345953ccf6f3`.
- The Task 3 correction reached controller `BRANCH_ACCEPTED` at exact clean head `39db34bd4f04dd6b85b9e5444eee86a2f1ea1b05` after all 16 required gates passed. Its final-Git evidence SHA-256 is `14e6500bce762de792cefd2c509188e83dde799d438dd053cb306659d63214ef`; its correction-run ledger SHA-256 is `84cc4756ea8cf6f27d498977a6211da32c32ae68f3a72e5c0b6d534ff193b027`.
- The fresh exact-head implementation review of `39db34bd4f04dd6b85b9e5444eee86a2f1ea1b05` returned exactly `IMPLEMENTATION_FINDINGS`, 0 Critical and 3 Major. The review artifact SHA-256 is `be18740a9c60d110d9acd4487d70ba0c21fafd4485b7d046e39693f5312e6364`.
- Task 3's correction is accepted but review-blocked. The sole next eligible CI operation is a freshly authorized correction-only operation for M-001, M-003, and M-006; Task 4 and publication are ineligible until a corrected exact head passes deterministic acceptance and a fresh exact-head implementation review reaches 0 Critical and 0 Major.

CI evidence ingestion remains neutral, read-only collection and bounded observational stability only. It does not define merge policy, approval, required-check acceptance, expected-head merge protection, merge execution, or post-merge acceptance. The CI subtrack remains unpublished and cannot advance to publication or later Phase-4 work without final deterministic acceptance and an exact-head 0 Critical/0 Major implementation review.

## Current blocking correction set

The exact 0C/3M correction review artifact records these dispositions; its verdict and severity counts are authoritative and are not reinterpreted here:

- M-001 — OPEN: a transient error while stabilizing pre-positioned bundle material can leave a newly created reservation unpoisoned, allowing the next retry to use that material as historical proof.
- M-002 — CLOSED: publication now checks temporary-link removal, verifies the single-link final inode, and completes the required file and directory durability checks without masking failures.
- M-003 — OPEN: after ledger fsync failure leaves a readable appended line, the controller retry path can replay it without successfully stabilizing the ledger.
- M-004 — CLOSED: retry now requires reservation file and directory sync followed by named-inode and exact-byte revalidation.
- M-005 — CLOSED: attempt inventory is bounded before allocation, and keyed process locks use lifecycle-safe reference counting and removal.
- M-006 — OPEN, NEW: `attemptLease.poison` has no non-Linux implementation, so `internal/cilifecycle` fails to compile on non-Linux targets instead of returning its fail-closed unsupported error.

## Projection reconciliation boundary

The immutable-evidence cutoff for this projection candidate is Task 3 correction acceptance and exact-head review at exact clean head `39db34bd4f04dd6b85b9e5444eee86a2f1ea1b05`. All 16 required acceptance gates passed; final-Git evidence SHA-256 is `14e6500bce762de792cefd2c509188e83dde799d438dd053cb306659d63214ef`, and the correction-run ledger SHA-256 is `84cc4756ea8cf6f27d498977a6211da32c32ae68f3a72e5c0b6d534ff193b027`. The exact review artifact SHA-256 is `be18740a9c60d110d9acd4487d70ba0c21fafd4485b7d046e39693f5312e6364`, with exact verdict `IMPLEMENTATION_FINDINGS`, 0 Critical and 3 Major. These bytes materialize all required evidence through that cutoff, including the original Task 3, accepted Task 2/current-policy, and merged PR #12 boundaries described above.

This materialization candidate cannot self-record lifecycle events that occur after its bytes are frozen. Its resulting commit identity and any later acceptance or review are authoritative immediately in immutable evidence and belong in the next separately governed reconciliation. Once this candidate commits and reaches controller acceptance, resolve its exact accepted head from Git/controller evidence before issuing fresh correction-only authority. That bounded lag is expected and is not itself a contradiction.

## Separate cross-cutting design track

Automatic operation handoff remains a separate controller-workflow design track. Its drafts do not authorize the remaining Task 3 correction, do not change CI deliverable semantics, and are not a substitute for the required fresh correction-only capsule and authority.

Before any non-reconciliation governed operation, reconcile all projections through the latest relevant predecessor boundary. A projection stale through that boundary or contradicting immutable evidence is a planning blocker; immutable controller and Git evidence governs.

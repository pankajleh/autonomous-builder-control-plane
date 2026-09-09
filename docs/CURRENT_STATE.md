# Current Project State

Date: 2026-09-09

Operational role: this file is the present checkpoint and authority projection for governed work. It must be consulted before planning or starting an operation. It summarizes controller and repository evidence; it does not replace immutable authority, capsule, ledger, acceptance, review, or Git evidence.

## Repository checkpoint and current authority

- Repository: `pankajleh/autonomous-builder-control-plane`.
- Active implementation branch: `ep-005-ci-ingestion`.
- Current-policy merge boundary: exact `origin/main` commit `e11afb7d7356a0df36566d98c34adbd07a0097ae`, the merge commit for PR #12 and descendant of PR #11 merge `454ea4dce3c674e0d8e55273319cfb4ea4a077a5`.
- This Task 3 review-materialization operation began from exact clean base `b0080793683a0a8f4aa145b7840b41eb529a4478`.
- Its fresh `context-capsule-v2` at `/home/devagent/abcp-runtime/ep005-ci-task3-review-materialization/context.json` has exact byte SHA-256 `58981771da6a66effd68566951851e62c5d1f30218ca6c9ce26f2af626426c97`; independent verification confirmed the base, implementation operation kind, internal capsule SHA-256 `e787239116f8d98ae726366457b5e96582f951877e9c04039629dca812680501`, and all 7 bound source hashes before editing.
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
- Task 3 is accepted but review-blocked. The sole next eligible CI operation is a freshly authorized correction of M-001 through M-005; Task 4 and publication are ineligible until a corrected exact head passes deterministic acceptance and a fresh exact-head implementation review reaches 0 Critical and 0 Major.

CI evidence ingestion remains neutral, read-only collection and bounded observational stability only. It does not define merge policy, approval, required-check acceptance, expected-head merge protection, merge execution, or post-merge acceptance. The CI subtrack remains unpublished and cannot advance to publication or later Phase-4 work without final deterministic acceptance and an exact-head 0 Critical/0 Major implementation review.

## Current blocking correction set

The exact 0C/5M review artifact records this blocking set; its verdict and severity counts are authoritative and are not reinterpreted here:

- M-001: a newly created normal reservation can survive the detection of pre-existing event or bundle material, so a retry can treat pre-positioned material as historical proof rather than remaining fail-closed.
- M-002: artifact publication does not durably remove its temporary hard link or prove the final single-link pathname durable, and immediate read-back can mask a directory-fsync failure.
- M-003: ledger append/read-back can mask append or fsync failure; the append lacks serialized projected bounds and safe partial-tail rollback, permitting a non-durable completed outcome or a permanently poisoned ledger.
- M-004: an exact readable reservation encountered after reservation file or directory sync failure is accepted on retry without successful resync and named-inode/byte revalidation.
- M-005: allocator inventory reads an unbounded directory before enforcing the 64-attempt limit, and keyed process locks are never removed, leaving direct memory-exhaustion paths.

## Projection reconciliation boundary

The immutable-evidence cutoff for this projection candidate is Task 3 deterministic acceptance and exact-head review at exact clean technical head `b5c7e2cd0ea3cc223f481b1d73a78c6276846639`. The final-Git evidence SHA-256 is `24f3ceebf733c596c9638f9d9693b2fa49375d3721222a45dc4cc565e00cd1b7`; the exact review artifact SHA-256 is `d0a3f692f19d9dc5b42a13abf8c71bbd18469a57d8920183ca86345953ccf6f3`, with exact verdict `IMPLEMENTATION_FINDINGS`, 0 Critical and 5 Major. These bytes materialize all required evidence through that cutoff, including the accepted Task 2/current-policy and merged PR #12 boundaries described above.

This materialization candidate cannot self-record lifecycle events that occur after its bytes are frozen. Its resulting commit identity and any later acceptance or review are authoritative immediately in immutable evidence and belong in the next separately governed reconciliation. Once this candidate commits and reaches controller acceptance, resolve its exact accepted head from Git/controller evidence before issuing fresh correction-only authority. That bounded lag is expected and is not itself a contradiction.

## Separate cross-cutting design track

Automatic operation handoff remains a separate controller-workflow design track. Its drafts do not authorize the Task 3 correction, do not change CI deliverable semantics, and are not a substitute for the required fresh correction-only capsule and authority.

Before any non-reconciliation governed operation, reconcile all projections through the latest relevant predecessor boundary. A projection stale through that boundary or contradicting immutable evidence is a planning blocker; immutable controller and Git evidence governs.

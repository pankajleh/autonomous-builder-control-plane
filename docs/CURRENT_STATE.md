# Current Project State

Date: 2026-09-09

Operational role: this file is the present checkpoint and authority projection for governed work. It must be consulted before planning or starting an operation. It summarizes controller and repository evidence; it does not replace immutable authority, capsule, ledger, acceptance, review, or Git evidence.

## Repository checkpoint and current authority

- Repository: `pankajleh/autonomous-builder-control-plane`.
- Active governed operation: docs-only CI Task 3 post-merge projection reconciliation on `ci-task3-post-merge-projection-20260909`; no CI runtime implementation is active in this operation.
- Exact repository merge base: `origin/main` commit `bf2482f756a7c5f75906825b8f3e6e454c72f94f`, the merge commit for PR #13. Its parents are previous main `e11afb7d7356a0df36566d98c34adbd07a0097ae` and exact accepted/reviewed CI Task 3 head `3e0f295e8f98c348d684b2c026bacd9ceeeea911`.
- The reconciliation's plan-only kickoff commit is `4921a20f286bc8d9d87539b82d3fdce38824b224`, whose sole parent is that exact PR #13 merge base. The three projection files at the kickoff remain byte-identical to the merge base.
- Its fresh `context-capsule-v2` at `/home/devagent/abcp-runtime/ci-task3-post-merge-projection/context.json` has exact byte SHA-256 `0429ab44d7067d297a1057aeb15a1261d3eaac634cd49497ac9c92bd7058126f`; independent verification confirmed kickoff base `4921a20f286bc8d9d87539b82d3fdce38824b224`, implementation operation kind, internal capsule SHA-256 `71a9e19ae9cd0618d45e89bef3b70cf3e950b73e3915d8d4c490705280321b4d`, and all 8 bound source hashes before editing.
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
- Task 3, immutable evidence, ledger outcome, and replay, first reached controller `BRANCH_ACCEPTED` at exact clean technical head `b5c7e2cd0ea3cc223f481b1d73a78c6276846639` after all 16 deterministic gates passed. Its final-Git evidence SHA-256 is `24f3ceebf733c596c9638f9d9693b2fa49375d3721222a45dc4cc565e00cd1b7`; its 0 Critical / 5 Major exact-head review artifact SHA-256 is `d0a3f692f19d9dc5b42a13abf8c71bbd18469a57d8920183ca86345953ccf6f3`.
- The first Task 3 correction reached `BRANCH_ACCEPTED` at exact clean head `39db34bd4f04dd6b85b9e5444eee86a2f1ea1b05` after all 16 required gates passed. Its final-Git evidence SHA-256 is `14e6500bce762de792cefd2c509188e83dde799d438dd053cb306659d63214ef`; its controller ledger SHA-256 is `84cc4756ea8cf6f27d498977a6211da32c32ae68f3a72e5c0b6d534ff193b027`; its 0 Critical / 3 Major exact-head review artifact SHA-256 is `be18740a9c60d110d9acd4487d70ba0c21fafd4485b7d046e39693f5312e6364`.
- The final Task 3 correction reached controller `BRANCH_ACCEPTED` at exact clean head `3e0f295e8f98c348d684b2c026bacd9ceeeea911`. All 17 required acceptance entries were recorded successful. Its final-Git evidence SHA-256 is `d809846a7f6133adca513e46b531101dac428c8c0707035dd1ec298e4634726b`; its controller ledger SHA-256 is `9f7d41498c984e8dfce7f762fc9d616e50ccc2b70f50b24b322339e751c51870`.
- Acceptance commands 14 and 15 used `! rg ...` while `rg` was unavailable. Their shell exit statuses alone are not substantive proof of forbidden-semantics absence; the independent exact-head reviewer rechecked the production diff and verified that no forbidden merge-policy, write, or transition semantics were introduced.
- The fresh exact-head implementation review of `3e0f295e8f98c348d684b2c026bacd9ceeeea911` returned `CLEAN_CRITICAL_MAJOR`, 0 Critical and 0 Major, with final marker `IMPLEMENTATION_CLEAN_CRITICAL_MAJOR`. Review artifact SHA-256 is `94cd9d4cf4173a2ecf59743aa7892c8b6e091d4eaf70b67cfcc8d18d7c5caebb`.
- PR #13 merged that exact accepted/reviewed head into `main` as `bf2482f756a7c5f75906825b8f3e6e454c72f94f`, with parents `e11afb7d7356a0df36566d98c34adbd07a0097ae` and `3e0f295e8f98c348d684b2c026bacd9ceeeea911`.
- CI Task 3 is merged and complete. Deferred Task 4 — acceptance, scope audit, and exact-head review handoff — is the sole next eligible CI operation and requires its own plan, fresh v2 capsule, and immutable authority at the exact accepted reconciliation predecessor.

CI evidence ingestion remains neutral, read-only collection and bounded observational stability only. It does not define merge policy, approval, required-check acceptance, expected-head merge protection, merge execution, or post-merge acceptance. The CI subtrack remains unpublished and cannot advance beyond Deferred Task 4 to publication or later Phase-4 work unless Task 4 produces deterministic acceptance for its exact implementation SHA and the same exact SHA receives a fresh 0 Critical / 0 Major review.

## Final Task 3 review dispositions

The final exact-head 0C/0M review artifact records these dispositions; its verdict and severity counts are authoritative and are not reinterpreted here:

- M-001 — CLOSED: new reservations remain durably provisional until ledger and bundle absence checks succeed; scan failures retain provisional state, while discovered material durably poisons the reservation.
- M-002 — PRESERVED: immutable publication still verifies temporary-link removal, final single-link identity, file durability, directory durability, and exact readback.
- M-003 — CLOSED: ledger lookup locks, scans, successfully fsyncs the ledger and parent directory, and reconfirms the exact event before replay.
- M-004 — PRESERVED: existing canonical reservations still require successful file and pinned-directory sync, named-inode revalidation, and exact-byte reread.
- M-005 — PRESERVED: inventory remains bounded to the configured limit plus sentinel, and process-local keyed locks remain reference-counted and removed on release.
- M-006 — CLOSED: the non-Linux `attemptLease.poison` stub fails closed with `errUnsupportedCIDurability`, and the dedicated Darwin/amd64 compilation gate succeeded.

## Projection reconciliation boundary

The immutable-evidence cutoff for this projection candidate is final Task 3 acceptance and exact-head review at exact clean head `3e0f295e8f98c348d684b2c026bacd9ceeeea911`, followed by its PR #13 merge `bf2482f756a7c5f75906825b8f3e6e454c72f94f`. All 17 required acceptance entries were recorded successful; final-Git evidence SHA-256 is `d809846a7f6133adca513e46b531101dac428c8c0707035dd1ec298e4634726b`, and controller ledger SHA-256 is `9f7d41498c984e8dfce7f762fc9d616e50ccc2b70f50b24b322339e751c51870`. The exact review artifact SHA-256 is `94cd9d4cf4173a2ecf59743aa7892c8b6e091d4eaf70b67cfcc8d18d7c5caebb`, with verdict `CLEAN_CRITICAL_MAJOR`, 0 Critical and 0 Major. The merge parents are previous main `e11afb7d7356a0df36566d98c34adbd07a0097ae` and the exact reviewed head. These bytes also preserve the earlier Task 3, accepted Task 2/current-policy, and merged PR #12 boundaries described above.

This reconciliation cannot self-record lifecycle events that occur after its bytes are frozen. Its resulting commit identity and any later acceptance, review, or merge are authoritative immediately in immutable evidence and belong in the next separately governed reconciliation. Once this reconciliation commits and reaches controller acceptance, resolve its exact accepted head from Git/controller evidence before issuing fresh Task-4-only authority. That bounded lag is expected and is not itself a contradiction.

This reconciliation records predecessor facts only. Merge approval policy, expected-head protection, merge execution, post-merge acceptance, and automatic operation handoff remain outside its implementation scope.

## Separate cross-cutting design track

Automatic operation handoff remains a separate controller-workflow design track. Its drafts do not authorize Deferred Task 4, do not change CI deliverable semantics, and are not a substitute for the required fresh Task-4-only capsule and authority.

Before any non-reconciliation governed operation, reconcile all projections through the latest relevant predecessor boundary. A projection stale through that boundary or contradicting immutable evidence is a planning blocker; immutable controller and Git evidence governs.

# Current Project State

Date: 2026-09-09

Operational role: this file is the present checkpoint and authority projection for governed work. It must be consulted before planning or starting an operation. It summarizes controller and repository evidence; it does not replace immutable authority, capsule, ledger, acceptance, review, or Git evidence.

## Repository checkpoint and current authority

- Repository: `pankajleh/autonomous-builder-control-plane`.
- Governed target checkpoint: `origin/main` at `454ea4dce3c674e0d8e55273319cfb4ea4a077a5`, the merge commit for PR #11.
- The current projection-correction operation began from authority-bound base `5ad4304eee996697efedf909a61bbb2cdcdc2f2a`; its parent is the accepted and reviewed state-reconciliation candidate `51a2e84c4ff412aaedb7d352bd2b33781992f4ab`.
- Current roadmap phase: Phase 4 — GitHub lifecycle.
- Current execution pack: EP-005 — GitHub Lifecycle.
- Canonical roadmap: `docs/roadmap/IMPLEMENTATION_ROADMAP.md`.
- Execution-pack authority: `docs/execution-packs/EP-005-github-lifecycle.md`.

The exact authority-bound `context-capsule-v2` for this correction was verified against its authority-bound byte SHA-256 `cceea0c4b4297bddc7a8d0027676081ddeb8311c12ab640e76454bfdf644442b`, exact base, repository, and seven source hashes before editing.

## Accepted and merged governance checkpoint

The context-bound operation policy candidate `84c6a8ee4b6d6a315eeaa1e7de17fbf5f94a1dec` reached controller `BRANCH_ACCEPTED`. Its final exact-head implementation review returned `CLEAN_CRITICAL_MAJOR` with 0 Critical and 0 Major; review artifact SHA-256 `85405bc64c0703319cad881d4b4aa3f07ba7aa91fb97ef16b8172dcb56beebe5`. PR #11 merged that candidate to `main` as `454ea4dce3c674e0d8e55273319cfb4ea4a077a5`.

The resulting operating rule applies to every new governed operation: use a purpose-specific verified v2 capsule and immutable authority at the exact operation base. A plan/source/base or commit/HEAD change requires a fresh operation capsule and authority; a prior capsule cannot be carried across that boundary.

## Active roadmap track: EP-005 CI evidence ingestion

The corrected read-only CI evidence-ingestion design is frozen at SHA-256 `47a6d7b1d4c93a853a26e4da3753894dedacfce930345c610cf24a4236b2b409`. Its bounded design review returned 0 Critical and 0 Major; review artifact SHA-256 `aa5d86471ffcfe1d68eff9c49ef5b2df718355c0b4c825716d35d68217b3fa45`.

The implementation branch `ep-005-ci-ingestion` is at `da8ffea4582539067724b363b3144d9601dee086`:

- Task 1, the v1 evidence contract and bounds, is implemented at `e1740f4be8df06571a3299c3fb31ecc31b0a1aea`.
- Task 2, bounded GitHub reads and two-sweep stabilization, is implemented at `da8ffea4582539067724b363b3144d9601dee086`, but that commit is only an implementation checkpoint. It has no controller acceptance and predates the merged v2 context policy in PR #11.
- Task 3, immutable evidence, ledger outcome, and replay, has not started. The attempted handoff stopped before task work because the Task 2 capsule was bound to predecessor HEAD `e1740f4be8df06571a3299c3fb31ecc31b0a1aea` and cannot authorize a new operation at `da8ffea4582539067724b363b3144d9601dee086`. That unaccepted, policy-stale checkpoint is not an authorized Task 3 base.
- Task 4, acceptance, scope audit, and exact-head review handoff, remains pending after Task 3.

Tasks 1 and 2 are implementation checkpoints, not accepted/reviewed/merged lifecycle claims. The CI subtrack remains unpublished and must not advance to publication or later Phase-4 work without deterministic acceptance and an exact-head 0 Critical/0 Major implementation review.

Immediate next CI action: run deterministic Task 2 acceptance and governedly reconcile the CI branch and its plan with current merged policy. Only the resulting checkpoint, after its acceptance and current-policy reconciliation evidence proves it eligible, may become the predecessor for Task 3. Do not assert that identity in advance. Task 3 then requires a separate fresh operation with a Task-3-only executable plan and a newly verified v2 capsule and immutable authority bound to that exact eligible base.

## State-projection reconciliation boundary

The preceding state-projection reconciliation candidate `51a2e84c4ff412aaedb7d352bd2b33781992f4ab` passed deterministic acceptance. Its exact-head review returned `IMPLEMENTATION_FINDINGS`, with 0 Critical and 2 Major; review artifact SHA-256 `ce7c7088a0468104392bd040ee5835e24cba1d35285b0c05606d512e93904638`. This correction operation starts at plan commit `5ad4304eee996697efedf909a61bbb2cdcdc2f2a` and addresses those two findings only.

The immutable-evidence cutoff for this projection candidate is the correction operation authority at base `5ad4304eee996697efedf909a61bbb2cdcdc2f2a`, including the preceding candidate's acceptance and exact-head review evidence above. These bytes materialize all required evidence through that cutoff. They do not and must not claim acceptance, review, or merge events for this correction candidate that occur only after these exact bytes are frozen.

## Separate cross-cutting design track

Automatic operation handoff is a separate controller-workflow design track based at exact `main` checkpoint `454ea4dce3c674e0d8e55273319cfb4ea4a077a5`. Draft `AUTOMATIC_OPERATION_HANDOFF.md` and its execution plan exist only as uncommitted design outputs in the `abcp-automatic-operation-handoff` worktree. They have not been frozen in Git, accepted by the design gate, reviewed to a clean exact identity, or authorized for implementation.

That design track may automate future transitions between separately authorized operations. It must not rewrite EP-005 CI semantics, and it is not authority to continue CI Task 3. Its next step is to freeze the draft and obtain an exact-artifact design review/gate decision.

## Projection lifecycle

Every projection-reconciliation candidate declares an immutable-evidence cutoff before its bytes are frozen and must materialize all required controller, review, and Git evidence through that cutoff. Never mutate an accepted or reviewed candidate merely to add its own later acceptance, review, or merge result: those events are authoritative immediately in immutable evidence and are materialized by the next separately governed reconciliation. That expected bounded lag is not itself a contradiction.

Before any non-reconciliation governed operation, reconcile the projections through the latest relevant predecessor boundary. A projection that is stale through that boundary or contradicts immutable evidence remains a planning blocker. A purpose-specific projection-reconciliation operation is the explicit exception that may start to repair this bounded lag; it still requires fresh exact-base authority and may change only its authorized projection scope. If a projection disagrees with immutable controller or Git evidence, the immutable evidence governs.

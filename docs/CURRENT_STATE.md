# Current Project State

Date: 2026-09-09

Operational role: this file is the present checkpoint and authority projection for governed work. It must be consulted before planning or starting an operation. It summarizes controller and repository evidence; it does not replace immutable authority, capsule, ledger, acceptance, review, or Git evidence.

## Repository checkpoint and current authority

- Repository: `pankajleh/autonomous-builder-control-plane`.
- Governed target checkpoint: `origin/main` at `454ea4dce3c674e0d8e55273319cfb4ea4a077a5`, the merge commit for PR #11.
- This reconciliation operation began from authority-bound base `d12679967d9a0a14936cafc05fc22070bfc08fc4`, whose parent is that exact `origin/main` checkpoint.
- Current roadmap phase: Phase 4 — GitHub lifecycle.
- Current execution pack: EP-005 — GitHub Lifecycle.
- Canonical roadmap: `docs/roadmap/IMPLEMENTATION_ROADMAP.md`.
- Execution-pack authority: `docs/execution-packs/EP-005-github-lifecycle.md`.

The exact authority-bound `context-capsule-v2` for this reconciliation was verified against its supplied byte SHA-256 `64d9eb25b87c9da8427689eb2fb307da84f6d669f020c435f611436dfb75a23f`, exact base, repository, and eight source hashes before editing.

## Accepted and merged governance checkpoint

The context-bound operation policy candidate `84c6a8ee4b6d6a315eeaa1e7de17fbf5f94a1dec` reached controller `BRANCH_ACCEPTED`. Its final exact-head implementation review returned `CLEAN_CRITICAL_MAJOR` with 0 Critical and 0 Major; review artifact SHA-256 `85405bc64c0703319cad881d4b4aa3f07ba7aa91fb97ef16b8172dcb56beebe5`. PR #11 merged that candidate to `main` as `454ea4dce3c674e0d8e55273319cfb4ea4a077a5`.

The resulting operating rule applies to every new governed operation: use a purpose-specific verified v2 capsule and immutable authority at the exact operation base. A plan/source/base or commit/HEAD change requires a fresh operation capsule and authority; a prior capsule cannot be carried across that boundary.

## Active roadmap track: EP-005 CI evidence ingestion

The corrected read-only CI evidence-ingestion design is frozen at SHA-256 `47a6d7b1d4c93a853a26e4da3753894dedacfce930345c610cf24a4236b2b409`. Its bounded design review returned 0 Critical and 0 Major; review artifact SHA-256 `aa5d86471ffcfe1d68eff9c49ef5b2df718355c0b4c825716d35d68217b3fa45`.

The implementation branch `ep-005-ci-ingestion` is at `da8ffea4582539067724b363b3144d9601dee086`:

- Task 1, the v1 evidence contract and bounds, is implemented at `e1740f4be8df06571a3299c3fb31ecc31b0a1aea`.
- Task 2, bounded GitHub reads and two-sweep stabilization, is implemented at `da8ffea4582539067724b363b3144d9601dee086`.
- Task 3, immutable evidence, ledger outcome, and replay, has not started. The attempted handoff stopped before task work because the Task 2 capsule was bound to predecessor HEAD `e1740f4be8df06571a3299c3fb31ecc31b0a1aea` and cannot authorize a new operation at `da8ffea4582539067724b363b3144d9601dee086`.
- Task 4, acceptance, scope audit, and exact-head review handoff, remains pending after Task 3.

Tasks 1 and 2 are implementation checkpoints, not accepted/reviewed/merged lifecycle claims. The CI subtrack remains unpublished and must not advance to publication or later Phase-4 work without deterministic acceptance and an exact-head 0 Critical/0 Major implementation review.

Immediate next authority: make Task 3 the sole incomplete executable task, then issue and verify a fresh implementation v2 capsule and immutable run authority at exact base `da8ffea4582539067724b363b3144d9601dee086` before any Task 3 work.

## Separate cross-cutting design track

Automatic operation handoff is a separate controller-workflow design track based at exact `main` checkpoint `454ea4dce3c674e0d8e55273319cfb4ea4a077a5`. Draft `AUTOMATIC_OPERATION_HANDOFF.md` and its execution plan exist only as uncommitted design outputs in the `abcp-automatic-operation-handoff` worktree. They have not been frozen in Git, accepted by the design gate, reviewed to a clean exact identity, or authorized for implementation.

That design track may automate future transitions between separately authorized operations. It must not rewrite EP-005 CI semantics, and it is not authority to continue CI Task 3. Its next step is to freeze the draft and obtain an exact-artifact design review/gate decision.

## Projection lifecycle

Consult this file at operation planning and startup. Reconcile it whenever an operation reaches controller acceptance, exact-head review, or merge. If this projection disagrees with immutable controller or Git evidence, stop, use the immutable evidence as authority, and repair the projection before planning further work.

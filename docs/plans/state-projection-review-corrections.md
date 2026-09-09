# State Projection Review Corrections

## Authority

This plan corrects only the two Major findings from the exact-head review of `51a2e84c4ff412aaedb7d352bd2b33781992f4ab`.

### Task 1: Correct predecessor eligibility and projection materialization semantics

- [ ] Before edits, verify the fresh authority-bound `context-capsule-v2`, exact repository base, and review artifact SHA-256 `ce7c7088a0468104392bd040ee5835e24cba1d35285b0c05606d512e93904638`.
- [ ] Correct M01: never present `da8ffea4582539067724b363b3144d9601dee086` as an authorized Task 3 base. State that Task 2 is only an implementation checkpoint until deterministic acceptance completes and the CI branch is governedly reconciled with current merged policy; only the resulting verified eligible checkpoint may become a Task 3 predecessor.
- [ ] Correct M01: state that the next CI action is Task 2 acceptance/reconciliation, not Task 3 implementation, and that Task 3 requires a fresh Task-3-only executable plan/capsule at the resulting eligible base.
- [ ] Correct M02: define projection materialization by an explicit immutable-evidence cutoff. A reconciliation candidate must materialize all required evidence through that cutoff but must not self-record acceptance/review/merge events that occur after its own bytes are frozen.
- [ ] Correct M02: state that acceptance/review/merge evidence for a reconciliation candidate is authoritative immediately in immutable evidence and is materialized in the next reconciliation; this expected bounded lag is not itself a contradiction.
- [ ] Correct M02: before any non-reconciliation governed operation, projections must be reconciled through the latest relevant predecessor boundary; a stale or contradictory projection remains a blocker. Projection-reconciliation operations are the explicit exception needed to repair that lag.
- [ ] Preserve exact-head immutability: never mutate an accepted/reviewed candidate merely to record its own acceptance or review.
- [ ] Update only `docs/CURRENT_STATE.md`, `docs/PROGRESS.md`, `docs/AUDIT_INDEX.md`, and this correction plan.
- [ ] Run exact identity/content assertions plus `git diff --check`, then commit this single correction task only.

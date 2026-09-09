# CI Task 2 Current-Policy Reconciliation

## Authority

This operation reconciles the deterministically accepted Task 2 checkpoint with exact merged main `e11afb7d7356a0df36566d98c34adbd07a0097ae` (PR #11 context-bound policy plus PR #12 state projections). It does not implement Task 3.

### Task 1: Reconcile accepted Task 2 with current policy and establish an eligible Task 3 predecessor

- [ ] Before edits, verify the fresh authority-bound `context-capsule-v2`, exact repository base, Task 2 acceptance evidence, and exact merge target `e11afb7d7356a0df36566d98c34adbd07a0097ae`.
- [ ] Merge exact `origin/main` commit `e11afb7d7356a0df36566d98c34adbd07a0097ae` into `ep-005-ci-ingestion`; do not rebase or rewrite accepted Task 1/Task 2 commits.
- [ ] Resolve overlaps by preserving current merged v2 context-bound policy while retaining the accepted CI branch's authority-bound capsule environment propagation behavior; do not weaken either invariant.
- [ ] Preserve Task 1/Task 2 `internal/cilifecycle` implementation semantics byte-for-byte unless a compile/test conflict with current main makes a minimal compatibility edit necessary; any such edit must be explicitly documented.
- [ ] Reconcile `docs/CURRENT_STATE.md`, `docs/PROGRESS.md`, and `docs/AUDIT_INDEX.md` through the Task 2 acceptance and PR #12 merge boundary using the projection cutoff rule.
- [ ] Restructure CI execution planning so Task 3 is the sole incomplete executable `### Task N:`/`### Iteration N:` section; keep Task 4 requirements deferred without making them executable in the Task 3 operation.
- [ ] Preserve CI evidence-ingestion scope: neutral read-only collection/stability only; no merge policy, approval, expected-head merge protection, merge execution, or post-merge acceptance semantics.
- [ ] Run `gofmt`, focused package tests, `go test -race ./internal/cilifecycle`, `go test ./...`, `go vet ./...`, `make smoke`, forbidden-change/semantics checks adjusted only for merged-main policy ownership, and `git diff --check`.
- [ ] Commit this single reconciliation task. Do not start Task 3 in this operation.

# CI Task 2 Reconciliation Acceptance Correction

## Authority

Correct only the failed `current-policy-exact` acceptance gate from reconciliation candidate `e762bc55d1d0dcf8cf2f1564abfb47b6790884e2`. Preserve all accepted CI Task 2 bytes and the reconciled projections/Task-3-only plan.

### Task 1: Restore exact current-policy bytes and requalify the predecessor

- [ ] Verify the fresh v2 capsule, exact base, failed acceptance command-008 evidence, and merged-main policy SHA `e11afb7d7356a0df36566d98c34adbd07a0097ae` before edits.
- [ ] Restore `internal/run/run.go` and `internal/run/run_test.go` byte-for-byte from `e11afb7d7356a0df36566d98c34adbd07a0097ae`; do not alter other current-policy-owned files.
- [ ] Preserve `internal/cilifecycle` byte-for-byte from accepted Task 2 SHA `da8ffea4582539067724b363b3144d9601dee086`.
- [ ] Preserve the reconciled CI plan with Task 3 as the sole incomplete executable task and Task 4 deferred/non-executable.
- [ ] Preserve CURRENT_STATE/PROGRESS/AUDIT_INDEX reconciliation through the Task 2 acceptance + PR #12 merge cutoff.
- [ ] Run full format/tests/race/vet/smoke plus exact policy/CI byte-identity, scope, forbidden-semantics, and diff checks.
- [ ] Commit this correction only and leave a clean worktree.

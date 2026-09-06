# EP-003 Claude Major Corrections

## Authority

Roadmap phase: Phase 2 — Recovery and blocker control.
Parent goal: close only the three Major findings from the independent Claude review of exact head `6eec2909095b6e70f20d2dd1519c1c2cc56fc4f0`.
Base SHA for this correction: `6eec2909095b6e70f20d2dd1519c1c2cc56fc4f0`.

## Context Authority

Read before implementation:
1. `docs/roadmap/IMPLEMENTATION_ROADMAP.md`
2. `docs/execution-packs/EP-003-recovery-blocker-control.md`
3. `docs/plans/completed/ep-003-recovery-blocker-control.md`
4. `docs/architecture/STATE_MACHINE.md`
5. `docs/architecture/DECISION_AND_ACCEPTANCE_POLICY.md`
6. `docs/architecture/CONTEXT_AUTHORITY_AND_CAPSULE_POLICY.md`

Do not add Phase 3+ capability. Do not change context-capsule behavior except where tests need compatibility. Preserve all EP-002 and EP-003 state/authority invariants.

### Task 1: Correct recovery evidence and cleanup safety

- [ ] Preserve untracked, non-ignored file contents before any destructive cleanup. Use repository/worktree-contained, symlink-rejecting, bounded capture with immutable evidence metadata and re-verification before deletion.
- [ ] If complete preservation cannot be proven, fail closed and do not remove the worktree.
- [ ] Prevent destructive cleanup when any required snapshot artifact is truncated. Truncation may remain recorded as evidence but must not authorize cleanup.
- [ ] Bind progress capture/deletion to controller-owned governed attempt metadata rather than arbitrary request paths. Reject progress roots outside the authorized runtime/worktree boundary and reject symlink/path escape/system-home targets.
- [ ] Ensure every deletable progress target is explicitly covered by recorded recovery authority; no request-only destructive target may be acted on.
- [ ] Add regression tests for untracked file preservation, untracked symlink rejection, truncation refusal, arbitrary progress-root refusal, secret-like home path refusal, and exact authorized progress deletion.
- [ ] Keep cleanup branch-history preservation and owner-dead proof unchanged.
- [ ] Run `gofmt -w cmd internal`, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`.
- [ ] Mark this task complete and commit only the bounded correction.

## Acceptance

The correction is acceptable only if all three Claude Majors are closed, destructive cleanup remains fail-closed, no new integration/merge capability appears, all required validation passes, and the worktree is clean.

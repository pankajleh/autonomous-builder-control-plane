# Plan: EP-003 — Recovery and Blocker Control

## Authority

Execution pack: `docs/execution-packs/EP-003-recovery-blocker-control.md`
Base commit: `f7ca8e6e59f04e7bece0582f3febcd37ec2cb4f7`

Architecture authority:
- `docs/architecture/AUTONOMOUS_BUILDER_ARCHITECTURE.md`
- `docs/architecture/STATE_MACHINE.md`
- `docs/architecture/EVENT_AND_PROVENANCE_MODEL.md`
- `docs/architecture/DECISION_AND_ACCEPTANCE_POLICY.md`
- `docs/roadmap/IMPLEMENTATION_ROADMAP.md`

This plan implements Phase 2 recovery and blocker control only. Do not add cross-plan scheduling, integration/merge authority, GitHub lifecycle automation, service APIs, dashboard features, deployment, or production completion.

## Global invariants

1. Ralphex remains a subprocess boundary.
2. No destructive recovery action before positive owner-dead proof and immutable snapshot publication.
3. PID alone is never sufficient Linux owner identity.
4. Unknown/ambiguous recovery state fails closed.
5. Secrets are never persisted in events/evidence/Git.
6. Recovery/resume authority must be explicit and ledgered; never infer permission from a prior failed run.
7. EP-003 may use existing states but must not create a path to integration/merge/completion.
8. Tests are deterministic and use disposable repos/processes.
9. Preserve EP-002 acceptance/state invariants and existing tests.
10. Go standard library only unless separately justified.

## Validation baseline

After every task:

```bash
gofmt -w cmd internal
go test ./...
go test -race ./...
go vet ./...
make smoke
git diff --check
```

### Task 1: Implement recovery ownership and stale-state inspection

- [ ] Add a focused recovery package with typed process/worktree ownership metadata for a governed attempt.
- [ ] On Linux capture process identity strong enough to detect PID reuse (for example PID plus `/proc` start identity) without relying on command text alone.
- [ ] Inspect governed Ralphex worktree/branch/process state without mutating it and classify active, stale-owner-dead, missing, or ambiguous state.
- [ ] Keep platform-specific process identity behind small build-tagged files; unsupported proof must fail closed.
- [ ] Add tests for live owner, dead owner, PID-reuse mismatch, missing worktree, path escape/symlink rejection, and ambiguous proof.
- [ ] Run validation, mark Task 1 complete, and commit.
### Task 2: Preserve terminal-failure evidence before cleanup

- [ ] Add immutable recovery snapshot metadata tying run/attempt identity to repository, worktree, branch, HEAD, status, and owner proof.
- [ ] Capture uncommitted diff/status and relevant Ralphex progress metadata before any authorized cleanup.
- [ ] Bound snapshot sizes and preserve truncation metadata; never silently omit oversized state.
- [ ] Ensure snapshot publication failure prevents cleanup/restart.
- [ ] Add tests proving dirty state preservation, clean-state snapshot, immutable hashes, size bounds, and no cleanup on evidence failure.
- [ ] Run validation, mark Task 2 complete, and commit.

### Task 3: Implement deterministic failure/blocker classification

- [ ] Add typed classifier output with class, confidence/evidence basis, retryability, and recommended controller state/action.
- [ ] Distinguish transient execution failure, capacity/rate-limit wait, hard quota/usage exhaustion, auth/authorization, billing/account failure, validator unavailable, policy block, external dependency, secret requirement, human decision, and unrecoverable failure.
- [ ] Keep secret values out of classifier payloads/evidence; redact credential-like material in captured diagnostics.
- [ ] Unknown/ambiguous input must not be auto-retried destructively.
- [ ] Add table-driven tests from harmless representative stderr/exit metadata including EXP-03/05/06-style cases.
- [ ] Run validation, mark Task 3 complete, and commit.
### Task 4: Add explicit blocker and resume authority records

- [ ] Add typed controller records/events for recovery/blocker decisions using existing domain states and durable evidence refs.
- [ ] Represent the exact question/required authority for `HUMAN_DECISION_REQUIRED`, required secret identity (never secret value), external dependency, validation unavailable, and policy block.
- [ ] Add explicit resume/restart authority containing prior run/attempt reference, actor, decision/action, timestamp, policy version, and evidence refs.
- [ ] Reject resume authority that targets the wrong run/attempt, lacks required actor/action, or attempts a forbidden state transition.
- [ ] Add tests proving no implicit resume, no secret persistence, actor attribution, and state-machine validation.
- [ ] Run validation, mark Task 4 complete, and commit.

### Task 5: Implement bounded cleanup/restart workflow and CLI

- [ ] Add orchestration that performs: inspect → owner-dead proof → snapshot → classify/authorize → cleanup → new attempt authority → restart preparation.
- [ ] Cleanup only the exact governed stale worktree/runtime state proven by Task 1; preserve committed branch history.
- [ ] Add explicit CLI surface to inspect recovery and execute an authorized recovery/resume without hidden destructive defaults.
- [ ] Record every recovery transition/action in the append-only ledger with immutable evidence refs.
- [ ] Add deterministic E2E tests for successful stale recovery, live-owner refusal, ambiguous-owner refusal, dirty-state preservation, missing authority, and cleanup failure.
- [ ] Ensure the workflow ends at a restart-ready/EP-002 execution boundary and cannot reach integration/merge/completion.
- [ ] Run validation, mark Task 5 complete, and commit.

## Final verification

All checkboxes complete; all validation passes; worktree clean; no Phase 3+ capability added.

# EP-005 Task 2 — Exact Two-Finding Closure Correction

Base candidate: `722b12b3adf4c46af4c6bd7b42cbff1dbf00c087`
Frozen closure verdict SHA-256: `d666e4aa9a5ecacb1e7c011ae4ea5736ecc664709b87eab67feba5d2a689a9d6`
Review seal SHA-256: `de8244fcef7730649d646ad19eaefc9b70c2ddeadef24eb07d877084e4844525`

## Scope
Close exactly the two remaining in-scope blockers from the fresh exact-head Claude closure review. The seven other original findings remain closed and MUST NOT be reopened. No Task-3/live-provider work.

### Task 1: Close Critical-01 and Major-01 only

#### Critical-01 — unresolved target-submission must not be terminalized pre-authority
- Before `terminalizeIdentifiedAuthorityFailure` can durably append a terminal core, check the active transition barrier and durable target-submission boundary under the held run lease.
- If submission is unresolved/active, do not append a terminal core; preserve READY with `TARGET_REF_UPDATE_UNKNOWN` and `Unresolved: true` so reconciliation owns settlement.
- Preserve repository/base serialization required by M-08 for any pre-authority settlement path.
- Add an exact regression covering crash/restart after UNKNOWN (and equivalent post-submit unresolved state), then authority evidence becomes unreadable: no FAILED core may be selected, no alternate terminal may be selected, and reconciliation remains possible.

#### Major-01 — M-06 negative storage proofs must exercise capacity logic
- Replace invalid `authority.json` negative-boundary publishes in `TestCorrectionM06StorageReservationsCleanupLimits` with allowlisted record names on open attempts.
- Assert the rejection is the capacity/reservation error, not invalid record-name/closed-attempt validation.
- Preserve genuine exact-limit pass and limit+1 fail coverage for per-attempt files and bytes.

#### Mandatory evidence
- `TestClosureCritical01UnresolvedSubmissionAuthorityFailure` passes and proves no terminal core is durably selected across an active unresolved submission barrier.
- `TestCorrectionM06StorageReservationsCleanupLimits` passes with explicit assertions that both per-attempt limit+1 cases reach capacity logic.
- Existing nine correction entrypoints remain green.
- Focused race/restart tests, full repository tests/vet, metadata-free smoke, portability compile, network-free/dependency checks, Task-1 regression, and diff/scope checks all pass.
- Exactly one implementation commit is created from this plan head; plan is moved to `docs/plans/completed/` in that commit.

#### Mutation ceiling
Only:
- `internal/mergelifecycle/**`
- this plan move from `docs/plans/` to `docs/plans/completed/`

Forbidden:
- `internal/githublifecycle/**`, `internal/run/**`, `internal/integrationgate/**`, `internal/ledger/**`
- Task-3 live provider/network/credentials/HTTP/GraphQL
- status-doc reconciliation or Phase-5 work
- any new blocker family or design reopening

- [x] Critical-01 corrected with exact regression.
- [x] Major-01 test evidence corrected to exercise real capacity logic.
- [x] All frozen acceptance evidence passes.
- [x] Exactly one correction commit created and worktree clean.

# EP-005 Task 3 — Final Crash-Recovery M03 Correction

## Overview
Close only the final sealed Task-3 exact-head Major from review seal `73f8ac9547800009e6bc4af64dd679a22f07c80238075ead306d93ee9f2fb818` at candidate `58164a6a7902bf0c8562af80982ad93c4281bcc6`.

The defect is narrow: after `target-submission.json` is durable, cumulative provider budget may be detected exhausted before any target HTTP reservation is authorized. If terminal-intent durability then crashes/fails, restart currently treats the durable submission record as possibly submitted and enters reconciliation, which can only return `TARGET_UPDATE_UNKNOWN` because the budget is exhausted. T3-14/Sections 7–8 require zero target HTTP reservation to remain provably unsubmitted.

## Authority and scope
Owned paths only:
- `internal/mergelifecycle/controller.go`
- `internal/mergelifecycle/store_linux.go`
- `internal/mergelifecycle/store_unsupported.go` only if required for the same helper contract
- `internal/mergelifecycle/task3_provider_budget_test.go`
- this plan, moved to `docs/plans/completed/` on completion

Frozen: GitHub provider, githublifecycle contracts, ledger/run/integrationgate/cmd, module files, status docs, Task 4/Phase 5+. No live GitHub mutation or credential use.

## Required semantics
1. Recovery must not infer possible target submission from `target-submission.json` alone.
2. A strict durable read of the existing provider HTTP-call journal must distinguish: no target HTTP reservation ever existed; one target reservation existed/pending/accounted; malformed/ambiguous audit state.
3. If the target submission record exists, no target HTTP reservation ever existed, and cumulative budget is exhausted, recovery must replay the existing terminal protocol as `READY_FOR_MERGE -> FAILED/RESOURCE_LIMIT_EXHAUSTED`.
4. This recovery path makes zero provider/reconciliation calls and never emits `TARGET_UPDATE_UNKNOWN`.
5. If any target HTTP reservation existed, retain the existing possibly-submitted UNKNOWN/reconciliation behavior; never downgrade it to provably unsubmitted.
6. Malformed/ambiguous local audit state remains storage-integrity failure, not resource exhaustion or UNKNOWN.
7. Existing valid cancellation precedence remains unchanged; do not broaden/remint cancellation authority.
8. Existing post-APPLIED `POST_MERGE_ACCEPTANCE_FAILED` behavior and all prior Task-3 corrections remain unchanged.

## Mandatory regression
`TestTask3FinalClosureM03PreTargetBudgetCrashRecovery`

It must inject failure after pre-target budget exhaustion is known but before terminal-intent durability; restart; prove zero target HTTP reservations/requests and zero reconciliation calls; require exactly `FAILED/RESOURCE_LIMIT_EXHAUSTED`; require barrier resolution; prove a prior target reservation preserves UNKNOWN/reconciliation; and prove malformed audit fails closed without provider mutation.

## Acceptance floor
- mandatory regression above, repeated
- `TestTask3FinalClosureM01FirstHTTPReservationDurability`
- `TestTask3FinalClosureM02BudgetExhaustionDisposition`
- all prior five Task-3 closure regressions
- legacy Task-3 provider suite
- mergelifecycle + Task-1/Task-2 regressions
- package/full repository tests and race, vet, smoke, Darwin compile-only
- forbidden mutation/static scope checks
- controlled live conformance remains default-off
- exactly one implementation commit after this plan head
- clean final worktree and `git diff --check`

### Task 1: Close final crash-recovery finding
- [ ] Add strict durable target-reservation audit/recovery classification.
- [ ] Add the mandatory M03 regression and negative controls.
- [ ] Run the complete acceptance floor.
- [ ] Move this plan to `docs/plans/completed/` and commit exactly one implementation commit.

## Completion boundary
Implementation acceptance does not close Task 3. A fresh independent exact-head Critical/Major closure review must return 0C/0M afterward.

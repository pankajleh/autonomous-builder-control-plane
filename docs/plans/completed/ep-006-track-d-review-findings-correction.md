# EP-006 Track D — Exact-Head Review Findings Correction

Blocked exact head: `d6ce11694bba9d80ef2c6b0300ed856983ef03c0`.
Blocked tree: `35c519970d5dfc92a3a5ea37245980f778bd6cd1`.
Accepted Track-C predecessor: `e1b5367ea82e774d81b10c989d506efe16b7d5bc`.
Accepted EP-006 design: `908710e1606b0da761e612c36b85c866e182c7f7`.
Sealed review: `ff224466dbbde4a2390971665c1e67f8b6ff05a447a0ec398a92a2dfae395e04`.
Review verdict: `0 Critical / 6 Major / 2 Minor`, gate NOT_VERIFIED.

This is a bounded Track-D correction only. A/B/C semantics and files remain frozen.
No run admission, retry, resume, recovery, UI, or new authority type may be added.

## Maximum correction ownership

```text
internal/actionapi/**
internal/actioncontrol/**
internal/run/run.go
internal/run/run_test.go
cmd/abcp/main.go
cmd/abcp/main_test.go
this plan and its completed-plan move
```

No new `internal/run/*.go` file may survive.
### Task 1: close the six Major findings

- [x] Make `(principal_id, request_id)` idempotency global across the complete service action journal, not merely per run. Same semantic request returns the existing operation; different intent anywhere returns `request_id_conflict`. Preserve bounded, mode-0600, no-symlink, cross-process durability and ordering; do not create an unbounded or mutable side database.
- [x] Make cancellation-vs-run-transition selection atomic. Once the watcher wins the run-transition lease, appends the exact `API_CANCEL_REQUESTED` event, and delivers the typed cause, no later competing runner transition selected from the prior state may append `IMPLEMENTATION_COMPLETED`, `BRANCH_ACCEPTED`, or another competing state edge. Preserve ordinary non-API cancellation behavior.
- [x] Make cancel application/reconciliation proof exact and order-safe. Require one exact deterministic request event before the matching `CANCELLED` transition; reject duplicate/conflicting/reordered histories and bind complete expected request semantics before returning APPLIED or RECONCILED_APPLIED.
- [x] Make human-decision reconciliation compare the complete deterministic event, including record schema, required authority, principal ID/type, delegated actor ID/type, policy versions, grant digest, request digest, operation ID, timestamp, run/attempt, event type/source/actor and non-transition shape.
- [x] Accept the contract-valid optional delegated actor on cancel, preserving it in durable receipt/idempotency semantics without granting it decision-style authority semantics.
- [x] Add the missing adversarial proof coverage: API provenance across every Runner.Run cancellation exit; watcher-close/admission race; owner-generation replacement; closing-watermark contention/final drain; watcher-wins runner-transition race; reordered/conflicting cancel history; cross-run request-ID reuse; exact decision reconciliation mismatch fields.

All six findings must be closed together; partial closure is not completion.
### Task 2: close reviewed Minors within already-touched surfaces

- [x] Enforce durable receipt bounds consistent with the frozen command contract: request ID <=128 bytes, bounded valid state name, reason <=1 KiB valid UTF-8 with no NUL.
- [x] Return action-appropriate invalid-request classification for malformed cancel payloads rather than `decision_request_invalid`.
- [x] Do not broaden public API semantics while closing these Minors.

### Task 3: validation and completion

- [x] Run focused normal/race tests for `internal/actionapi`, `internal/actioncontrol`, `internal/run`, and `cmd/abcp`.
- [x] Stress all newly-added adversarial race tests at least 10 repetitions under `-race` where practical.
- [x] Rerun predecessor tripwires for merge lifecycle and Track-C evidence concurrency.
- [x] Run `go test -count=1 ./...`, `go test -count=1 -race ./...`, `go vet ./...`, and Darwin/Windows production builds for touched production packages where supported.
- [x] Prove cumulative Track-D scope from accepted Track C contains only authorized D paths and completed correction plans, with zero A/B/C mutation and no surviving extra `internal/run/*.go` files.
- [x] Complete every checkbox, move this plan under `docs/plans/completed/`, create one correction implementation commit, and leave the worktree clean.

## Completion boundary

Ralphex completion is implementation evidence only. Fresh deterministic Track-D acceptance and a fresh independent exact-head Critical/Major closure review remain mandatory. Required closure is 0 Critical / 0 Major before combined A–D acceptance.

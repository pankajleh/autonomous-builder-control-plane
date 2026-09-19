# EP-006 Track D Final Major-Findings Correction

## Authority

Exact blocked candidate: `e99d918e5f0be2e9e2a5727641b2d85311825946`.
Accepted Track-C predecessor remains `e1b5367ea82e774d81b10c989d506efe16b7d5bc`; A/B/C semantic proofs remain closed.
Governing accepted EP-006 design is `908710e1606b0da761e612c36b85c866e182c7f7`.
The sealed exact-head review of `e99d918…` is under `/home/devagent/abcp-runtime/ep006-track-d-final-closure-review-e99d918`, seal SHA-256 `3413c2c030f2083df03ee38b349dfde290bcbab65f024331cb383b022de9665b`, verdict 0 Critical / 5 Major / 2 Minor.

This plan authorizes correction of all five Majors together. The HTTP cancel-classification Minor may remain because closing it would require reopening frozen `internal/serviceapi/**`; do not mutate that surface. The no-mutation-on-failed-precondition Minor must close as a consequence of the bounded global request-index design below.

## Maximum mutation authority

```text
internal/actionapi/**
internal/actioncontrol/**
internal/run/run.go
internal/run/run_test.go
cmd/abcp/main.go
cmd/abcp/main_test.go
this plan and its completed-plan move
```

Frozen: `internal/readmodel/**`, `internal/ledger/**`, `internal/runtimecatalog/**`, `internal/serviceapi/**`, `internal/timeline/**`, `internal/evidence/**`, `internal/recovery/**`, `internal/supervisor/**`, all earlier Track-A/B/C paths, and all accepted design docs.
No new `internal/run/*.go` file may survive.

### Task 1: close durable receipt and bounded global idempotency Majors

- [x] Replace service-wide all-run journal scanning with an O(1)/bounded immutable request-identity index keyed by canonical `LookupKey(principal_id, request_id)`. The index must live under the canonical service root, be sharded/bounded, mode-0600, no-symlink/no-hardlink, descriptor-identity rechecked, create-once/immutable, and contain enough frozen semantic identity to recover the exact operation without scanning unrelated run journals. It must not be an unbounded mutable side database.
- [x] Make global identity reservation crash-safe: the immutable index freezes operation ID/received-at/run/action/request digest before materializing the per-run receipt. If a crash occurs after index durability but before receipt durability, an exact semantic replay may materialize the exact frozen receipt; different intent conflicts. No effect/watcher may act until the per-run receipt itself is durably materialized.
- [x] Make receipt append fail closed on write/fsync/parent-directory-fsync ambiguity. Preserve exact pre-append size; on append/sync failure prove rollback/truncate+sync before releasing locks, otherwise poison/fail closed so a visible but uncommitted receipt can never be executed. Do not discard directory-fsync failures for newly created durable objects.
- [x] Existing idempotent receipts/index entries must be re-proven from immutable index + exact run journal without broad scans or mutation on stale/precondition failure paths.
- [x] Add crash/fault-injection tests for write, file-sync and directory-sync failures, index-created/receipt-missing recovery, conflicting replay, no-effect-before-durable-receipt, and bounded no-global-scan lookup.

### Task 2: enforce true cross-process read-model ceilings without modifying Track B/C

- [x] Add a D-owned Linux cross-process resource guard under the canonical service root with exactly eight ledger-object slots and four snapshot slots, acquired by safe bounded file locks and released on every path. Slot files/directories must be mode-safe, no-symlink/no-hardlink, identity-checked, context-cancellable, and fail closed.
- [x] Add a D-owned read-model wrapper that implements the frozen read seams used by `serve`, timeline, action controller, and owner watcher. Ordinary snapshot/projection operations reserve one global ledger + one snapshot slot. Writable-ledger operations must account for the writable object and any nested authoritative snapshot without deadlock or oversubscription; provide a D-owned combined writable-ledger-plus-snapshot operation if necessary and update only D-owned action/watcher interfaces to use it.
- [x] Wire the same cross-process guard in both `abcp serve` and every `abcp run --service-root` owner watcher so separate OS processes participate in the same eight/four ceilings. Leave `internal/readmodel/**` unchanged.
- [x] Add multi-process tests proving aggregate ceilings across independent service/run processes, cancellation/context release, no slot leak after crash/process exit, and no deadlock for writable-ledger + in-lease snapshot revalidation.

### Task 3: close owner-finalization/admission race

- [x] Split owner shutdown into an idempotent nonblocking BeginClose/freeze phase and bounded drain/retire completion. BeginClose must atomically bind the exact action-journal watermark under journal->catalog order and change the exact owner generation to CLOSING so no later cancel admission can succeed.
- [x] Wire a D-owned Runner finalization hook so BeginClose occurs before a successful final runner transition from which `Runner.Run` can return (`IMPLEMENTATION_COMPLETED`, `BRANCH_ACCEPTED`, or any equivalent successful return edge). Do not block the runner waiting for drain while it may still need to append `CANCELLED`.
- [x] Preserve transition-lease atomicity: pre-watermark cancel versus final runner transition has exactly one winner. If cancel wins, typed cause leads to authoritative `CANCELLED`; if final transition wins, the admitted cancel deterministically becomes not-applied/rejected and drain can complete. Post-BeginClose admissions fail closed.
- [x] After `Runner.Run` returns, Close may only finish exact-watermark drain and retire the same generation. Replacement generations must remain untouched.
- [x] Add deterministic races for admission-vs-BeginClose, watcher-vs-final transition, final transition wins, cancel wins, close after runner return, replacement generation, and exact watermark retirement.

### Task 4: add real Runner.Run API-cancellation proof coverage

- [x] Add tests that drive actual `Runner.Run`, not direct `transition` calls, and prove typed API-cancel provenance on every required cancellation exit: validation/identity failure path after API cause becomes relevant, supervisor/process error, supervised process cancellation, pre-acceptance cancellation, acceptance-stage cancellation, and successful-transition race points.
- [x] For each applicable exit, prove exactly one authoritative `CANCELLED` state transition carries the exact `operation_id`, `owner_lease_id`, and deterministic request-event ID; prove no later competing successful/failed edge is appended from the prior state.
- [x] Remove/supplement synthetic-ledger tests that could mask the real finalization race; integration tests must exercise watcher + runner + journal/catalog composition where practical.

### Task 5: validation and completion

- [x] Run focused normal/race tests for `internal/actionapi`, `internal/actioncontrol`, `internal/run`, and `cmd/abcp`.
- [x] Stress every new Major-specific adversarial race test at least 10 repetitions under `-race` where practical.
- [x] Rerun predecessor tripwires for merge lifecycle, Track-C evidence-buffer concurrency, and the frozen supervisor PID-file test.
- [x] Run `go test -count=1 ./...`, `go test -count=1 -race ./...`, `go vet ./...`, and Darwin/Windows production builds for touched production packages where supported.
- [x] Prove cumulative Track-D scope from accepted Track C contains only authorized D paths/completed D correction plans, with zero frozen A/B/C mutation and no extra `internal/run/*.go` files.
- [x] Complete every checkbox, move this plan under `docs/plans/completed/`, create exactly one correction implementation commit above this authority commit, and leave the worktree clean.

## Completion boundary

Ralphex completion is implementation evidence only. Fresh deterministic Track-D acceptance and a fresh exact-head independent Critical/Major closure review remain mandatory. Required closure is 0 Critical / 0 Major before combined A-D acceptance. Do not publish or merge EP-006 from this task.

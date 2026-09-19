# EP-006 Combined Exact-Head Major Correction

## Authority

Exact blocked cumulative candidate: `98c57d1dcc82cc5529929815f5da90893cde55b7` (tree `e249a25231ad6c984efc7f6bc467f43e73337800`).
Phase-5 base remains `c4f899072e31364f81453b2a5d6d90147c774107`; accepted checkpoints remain A=`f082cae1677d002825b7f4176d9783e199c9a125`, B=`0097effa0e94340980a9ab5483abb5359ebfc2a1`, C=`e1b5367ea82e774d81b10c989d506efe16b7d5bc`, D=`98c57d1dcc82cc5529929815f5da90893cde55b7`.
Governing accepted EP-006 design is `908710e1606b0da761e612c36b85c866e182c7f7`.
Combined deterministic A-D acceptance is VERIFIED: summary SHA-256 `95773b8b71682acea148deeec498fe2485d2d161a3e12725970c9dcf81118288`, manifest SHA-256 `021f85e17c095e132766c6d66028ebb88e6b55b5d0966a68b8d3261162b83461`, final seal `99e7ca05a784a2c6831cb7b39408b2412b960a0aca1e659be168db827caef44d`.
The mandatory combined exact-head review is frozen at `/home/devagent/abcp-runtime/ep006-combined-ad-final-review-98c57d1/review.out`, SHA-256 `d39f7512b61bd3b60e575e627a7ffa028c80b501fabc46a3b896daf54b2030a7`, packet-manifest SHA-256 `e32c3e5b883b82144e9dc2ddd231be3832ce56cca0957b0c302537b94cc572e7`.
Its terminal verdict is exactly 0 Critical / 4 Major / 1 Minor and `EP006_COMBINED_EXACT_HEAD_GATE: NOT_VERIFIED`.

This B-correction authorizes exactly the four Majors below. No new blocker may enter this mutation pass. The known malformed-cancel HTTP-classification Minor remains deferred and must not reopen `internal/serviceapi/**`.

## Maximum mutation authority

```text
internal/actionapi/**
internal/actioncontrol/**
internal/runtimecatalog/**
internal/ledger/**
internal/run/run.go
internal/run/run_test.go
cmd/abcp/main.go
cmd/abcp/main_test.go
this plan and its completed-plan move
```

Frozen: `internal/serviceapi/**`, `internal/readmodel/**`, `internal/timeline/**`, `internal/evidence/**`, `internal/recovery/**`, `internal/supervisor/**`, `internal/githublifecycle/**`, `internal/mergelifecycle/**`, all accepted architecture/design/execution-pack documents, operational projections, and all other paths.

### Task 1: serialize human-decision effect execution and reconciliation

- [x] M1: after a durable decision claim exists, all execution/reconciliation contenders for that exact operation must share one cross-process, non-reissuable effect-execution authority before either applying the effect or selecting a terminal reconciliation outcome.
- [x] A contender must never emit `RECONCILED_NOT_APPLIED` while another process can still append the claimed decision event. Recovery may decide applied/not-applied only while owning the same exact exclusion and after authoritative revalidation.
- [x] Preserve one-effect semantics, existing action-journal idempotency, transition-lease ordering, bounded lock acquisition, crash recovery, and deterministic event identity. Add deterministic two-process/worker races for claim-created-before-effect, effect-in-progress, crash/recovery, and conflicting replay.

### Task 2: make runtime-catalog serialization and child namespace authority non-reissuable

- [x] M2: runtime-catalog `.lock`, `active`, `catalog/runs`, and every authority-bearing child namespace used by owner generation operations must be rooted in the already-pinned service-root generation and must not be recreated/rebound by pathname after catalog open.
- [x] Lock acquisition must use a pinned/root-authorized physical lock identity; loss/replacement of the lock or an anchored child directory must fail closed for existing and fresh processes. Revalidate namespace/lock identity while authority is held and before returning success/releasing it.
- [x] Add true cross-process unlink/rename/replacement tests proving two owner generations cannot concurrently install from one retired generation and that replacement cannot redirect reads/writes into a new namespace.

### Task 3: make run-transition lease namespace non-reissuable

- [x] M3: `.run-locks` must have one durable ledger-parent-bound generation. A fresh process must reject loss/replacement instead of accepting a new directory generation.
- [x] Each acquired run-transition lease must remain protected from lock-file replacement for its full lifetime; another process must be unable to acquire a different inode for the same run while the first lease is live. Revalidate exact namespace/lease identity before append authority is used and before release.
- [x] Preserve existing barrier semantics and bounded acquisition. Add cross-process directory and per-run-lock replacement races covering runner-vs-watcher/decision selection and proving exactly one transition authority can exist.

### Task 4: put runner snapshots under the same global four-snapshot ceiling

- [x] M4: every authoritative `JSONLLedger.Snapshot()` reachable from `abcp run --service-root`, including transition-state reconstruction and API-cancel proof, must reserve the same service-root-wide snapshot capacity used by service reads/watchers.
- [x] Reuse the existing D-owned resource-generation/slot authority; do not create a second accounting namespace. Avoid import cycles and preserve non-service `abcp run` behavior.
- [x] Add cross-process tests with runner and service/watcher snapshots together proving the aggregate snapshot ceiling is exactly four, cancellation releases capacity, and no nested writable/snapshot path deadlocks or double-counts.

### Task 5: cumulative validation and completion

- [x] Run focused normal and race tests for `internal/actionapi`, `internal/actioncontrol`, `internal/runtimecatalog`, `internal/ledger`, `internal/run`, and `cmd/abcp`; stress each new Major-specific adversarial race at least 10 repetitions where practical.
- [x] Rerun the EP-006 combined race regressions, full `go test -count=1 -race ./...`, full uncached `go test -count=1 ./...`, `go vet ./...`, Darwin/Windows builds, and `git diff --check`.
- [x] Prove the cumulative `c4f8990...HEAD` diff still preserves accepted A/B/C semantics and that the correction diff from `98c57d1...HEAD` contains only the maximum-authority paths above.
- [x] Complete all checkboxes, move this plan to `docs/plans/completed/`, leave a clean worktree, and commit the bounded correction.

## Completion boundary

Ralphex completion is implementation evidence only. The corrected exact head must undergo fresh cumulative deterministic A-D acceptance and then a fresh independent combined exact-head Critical/Major review. Publication/PR/merge remains forbidden until that combined review is 0 Critical / 0 Major.

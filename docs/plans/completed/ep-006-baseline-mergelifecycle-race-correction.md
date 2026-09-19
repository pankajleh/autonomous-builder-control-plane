# EP-006 Baseline Merge-Lifecycle Race Correction

## Authority

- Accepted Track-B predecessor: `0097effa0e94340980a9ab5483abb5359ebfc2a1`.
- Blocked Track-C candidate: `f67a3a3199c390b499bcd6736c2d71c9e3c97cb4`.
- Track-C authority: `6d61b2c6a526ec464c8bd7b673963a3a4246f9c3`.
- Track-C blocked-acceptance summary SHA-256: `a62b9974d2f087d1f2f225f471674e050996e07cb31bf551d2c8d43180c1ed0c`.
- Track-C blocked-acceptance manifest SHA-256: `08060a77701831e620ac36d45ab6819e3b0ce7a7f3b0004051f95d9c9876308e`.
- The same full-suite race failure was reproduced on exact accepted Track B; Track C did not mutate `internal/mergelifecycle/**`.

## Finding

`TestConcurrentControllersSubmitExactAttemptOnce` assumes both concurrent callers must return success. Production run-transition acquisition is intentionally bounded to two seconds and may fail closed with `lock run transition: authoritative ledger file lock is busy`. Under race instrumentation that legitimate bounded-busy result reaches `Fatalf` before the sibling goroutine is joined; deferred controller `Close()` then runs concurrently with the sibling store operation, producing a teardown data-race report.

## Maximum scope

```text
internal/mergelifecycle/controller_linux_test.go
this plan and its completed-plan move
```

Production merge lifecycle, ledger lock semantics, lock timeout, provider behavior, Track-B readmodel/ledger code, Track-C code, and every other path are read-only.

### Task 1: Correct the concurrency test contract

- [x] Drain both concurrent Execute results before any fatal assertion or deferred controller teardown can run.
- [x] Require at least one caller to reach the governed `MERGED` result; permit only the exact bounded run-transition-busy error for the other caller, or a second `MERGED` result after serialized observation.
- [x] Preserve the core invariant: prepare/submit/post-merge external side effects occur exactly once.
- [x] Do not increase lock wait, suppress the race detector, add sleeps, skip tests, or mutate production code.
- [x] Add/retain assertions proving no unexpected error class is accepted.
- [x] Run the exact concurrency test under `-race -count=20`.
- [x] Run `go test -race ./internal/mergelifecycle -count=2` and ordinary package tests.
- [x] Run uncached full `go test ./...`, one clean uncached full `go test -race ./...`, and `go vet ./...`.
- [x] Run `git diff --check`, exact scope checks, and prove Track-B readmodel/ledger plus Track-C-owned paths are untouched.
- [x] Mark all tasks complete, move this plan to `docs/plans/completed/`, make exactly one correction implementation commit, and leave the worktree clean.

## Completion boundary

This repair invalidates only the merge-lifecycle baseline proof. It does not reopen Track B's accepted ledger/read-model semantic proof. After fresh acceptance and exact-head 0C/0M review of this correction, replay the already-reviewed Track-C implementation delta onto this corrected base and revalidate Track C.

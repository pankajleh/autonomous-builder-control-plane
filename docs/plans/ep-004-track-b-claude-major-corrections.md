# EP-004 Track B — Claude Major Corrections

## Context Authority

- Roadmap: Phase 3 — Cross-plan scheduler and integration.
- Execution pack: `docs/execution-packs/EP-004-cross-plan-scheduler-integration.md`.
- Track B reviewed head: `982840835f4ee0d38583d0a69dbf1be05b800402`.
- Frozen Track A scheduler contract remains read-only; Track C remains out of scope.
- Fresh task must verify the bound context capsule before implementation.
- Global invariants: deterministic/reproducible governed evidence; bounded cancellation; ambiguity fails closed; no state transition or `READY_FOR_MERGE` authority here.

### Task 1: Correct Track B reproducibility and cancellation

- [ ] Remove random disposable-workspace path/ID material from canonical integration result and deterministic pre-cleanup capture identity; preserve operational workspace identity only in separate cleanup evidence where needed.
- [ ] Make identical baseline + identical risk report + identical candidates produce identical canonical result SHA256 and deterministic capture SHA256 across separate runs.
- [ ] Bound Git subprocess cancellation: isolate the Git process group on supported platforms, terminate the owned group on context cancellation, and set a bounded wait/pipe-drain delay so descendants cannot hold `Integrate` indefinitely.
- [ ] Preserve exact command evidence, truncation failure, source/candidate immutability, textual-conflict proof, and cleanup-before-destruction evidence semantics.
- [ ] Add regression coverage for deterministic result/capture identity and cancellation that returns within a deadline and leaves no disposable workspace behind.
- [ ] Keep `internal/scheduler/`, Track C, canonical state machine, GitHub lifecycle, and merge authority untouched.- [ ] Run the targeted determinism/cancellation regressions repeatedly, then `gofmt -w cmd internal`, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check` with the pinned Go toolchain available on PATH.
- [ ] Mark only this task complete and commit the bounded correction.

## Review findings being corrected

1. Claude Major: random `os.MkdirTemp` workspace path/ID is embedded in canonical result/capture bytes, so identical governed inputs produce different result/capture digests.
2. Claude Major: `execGitRunner` uses `exec.CommandContext` without owned process-group cancellation or `WaitDelay`, so descendants retaining output pipes can make cancellation unbounded.

## Track boundary

This correction fixes the two substantive independent-review findings only. It must not implement Track D gate assembly or broaden Phase 3 scope.
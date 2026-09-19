# EP-006 Track D — namespace and durability closure correction

Authority parent: `bede4ff6b7379110026f32309ce0afd575a1996d`.
Accepted Track-C predecessor remains `e1b5367ea82e774d81b10c989d506efe16b7d5bc` and is frozen.
This plan closes only the four Majors from the sealed exact-head review under `/home/devagent/abcp-runtime/ep006-track-d-final-closure-review-bede4ff-r2`.

Mutation ceiling:
- `internal/actioncontrol/journal.go`
- `internal/actioncontrol/journal_linux.go`
- `internal/actioncontrol/journal_linux_test.go`
- `internal/actioncontrol/journal_unsupported.go`
- `internal/actioncontrol/resource_guard_linux.go`
- `internal/actioncontrol/resource_guard_linux_test.go`
- `internal/actioncontrol/resource_guard_unsupported.go`
- `internal/actioncontrol/readmodel_guard.go`
- bounded `internal/actionapi/controller.go` / `controller_linux_test.go` only if required for exact replay semantics
- this plan/completed-plan move.

Frozen: `internal/readmodel/**`, `internal/ledger/**`, `internal/runtimecatalog/**`, `internal/serviceapi/**`, `internal/timeline/**`, `internal/evidence/**`, `internal/recovery/**`, `internal/run/**`, `cmd/abcp/**`, and all A/B/C surfaces.
The malformed-cancel HTTP classification Minor remains deferred because fixing it would require reopening frozen `serviceapi`; it must not be promoted solely for remaining Minor.

### Task 1: make receipt commit durability fail closed
- [x] Correct the append protocol so failure of the final commit-marker removal directory fsync cannot expose an admission-failed receipt to readers/watchers.
- [x] While the global action lock remains held, every ambiguous final-sync path must either restore a durable recovery/poison marker and prove it, or roll back/truncate+sync the appended bytes; if neither can be proven, the journal instance must fail closed and no effect path may proceed.
- [x] Recovery after crash must deterministically decide committed versus rollback state without allowing the same receipt to be both executable and later truncated.
- [x] Add fault-injection coverage specifically for the final append-commit marker-removal directory sync, including watcher/read visibility before restart and recovery after restart.

### Task 2: preserve secondary decision uniqueness during immutable-index replay
- [x] Exact replay of a reserved identity must reapply all current run-journal secondary semantic constraints before materializing a missing receipt, including `decision_request_id` uniqueness.
- [x] If another durable decision receipt now owns the same decision request, replay must fail deterministically without appending/poisoning the run journal and without corrupting the immutable global request identity.
- [x] Add the two-request crash/replay adversarial sequence from the review and prove subsequent scans remain healthy.

### Task 3: pin action journal and request-index namespace identities across processes
- [x] Pin and continuously reverify the exact `actions` directory, request-index namespace, and service-wide lock-file identities; safe-mode same-owner unlink/recreate must fail closed rather than split flock/global-idempotency authority.
- [x] Protect request-index shard identity across processes with a durable parent-anchored identity mechanism or equivalent bounded design so shard replacement cannot hide identities, reset ceilings, or admit duplicate `(principal_id, request_id)` reservations.
- [x] Preserve no-symlink/no-hardlink/mode/owner/descriptor identity checks and journal→catalog lock order.
- [x] Add multi-process replacement tests for actions directory, `.lock`, request-index root, and active shard replacement while another process holds/uses the original namespace.

### Task 4: pin every cross-process resource-slot inode
- [x] Pin the exact identities of all eight ledger slot files and four snapshot slot files for the lifetime of each resource guard and reverify the named inode before/after acquisition and release.
- [x] Unlink/recreate of a held or free slot must fail closed across processes; no process may lock a replacement inode while another process retains a lock on the old inode.
- [x] Preserve crash/process-exit release and context-cancellable acquisition without leaking descriptors or weakening the exact 8/4 ceilings.
- [x] Add multi-process held-slot replacement and free-slot replacement adversarial tests proving the aggregate ceiling cannot be exceeded.

### Task 5: validation and completion
- [x] Run focused normal/race tests for `internal/actioncontrol` and any bounded `internal/actionapi` mutation.
- [x] Stress each new Major-specific adversarial test at least 10 repetitions under `-race` where practical.
- [x] Rerun predecessor tripwires for merge lifecycle, Track-C evidence concurrency, frozen supervisor cancellation, owner-finalization races, and real Runner.Run cancellation proof.
- [x] Run `go test -count=1 ./...`, `go test -count=1 -race ./...`, `go vet ./...`, and Darwin/Windows production builds for touched packages where supported.
- [x] Prove cumulative Track-D scope from accepted Track C remains authorized with zero frozen A/B/C mutation and no extra `internal/run/*.go` production files.
- [x] Complete every checkbox, move this plan to `docs/plans/completed/`, consolidate to exactly one correction implementation commit above this authority commit, and leave the worktree clean.

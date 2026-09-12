# EP-005 — Merge Authorization Task 2: Controller Policy, Admission, Recovery

## Authority

This bounded implementation task is an exact subset of `docs/plans/ep-005-merge-authorization-and-post-merge.md` Task 2. Task 1 contracts are independently accepted and exact-head reviewed 0 Critical / 0 Major at `bf5c851e8bce19f61648eb499ad146f25a1ee86c`.

The accepted master design remains authoritative. This task implements only controller/runtime semantics against network-free lifecycle contracts and a fake exact-base/exact-head provider. Task 3 live GitHub HTTPS/GraphQL behavior is forbidden.

## Fresh-operation startup

Before implementation begins, read the bound context capsule, verify its exact SHA256 and base SHA, and run `abcp context-verify` against this repository. Missing binding, source drift, base drift, or failed verification stops execution.

## Owned paths

Implementation may change only:

- `internal/mergelifecycle/**`
- `internal/ledger/jsonl.go`
- `internal/ledger/jsonl_test.go`
- `internal/ledger/transition_barrier.go`
- `internal/ledger/transition_barrier_test.go`
- `internal/run/run.go`
- `internal/run/run_test.go`
- `internal/integrationgate/gate.go`
- `internal/integrationgate/gate_test.go`
- this plan file, only to mark completion and move it to `docs/plans/completed/`

`internal/githublifecycle/**` is frozen in Task 2. If a Task-1 contract amendment appears necessary, stop with a design/scope blocker instead of editing it.

## Invariants

- Controller derives repository binding, READY authority, merge policy, actor and cancellation policy from controller-owned durable inputs; callers cannot select them.
- Production semantics are merge-only. Squash/rebase and fork-head execution remain unsupported.
- Shared transition/barrier semantics prevent competing state transitions while target submission is unresolved.
- Authorization seal durability is the controller authorization linearization point.
- At most one commit-preparation submission and one target-ref submission exist per exact attempt; ambiguous submission is reconciled read-only and never blindly retried.
- `APPLIED` requires the identical validated `MergeResult`; `NOT_APPLIED` requires typed proof; `UNKNOWN` preserves READY behind the durable barrier.
- Valid applied result plus durable post-merge proof selects irreversible `MERGED` before cleanup. Cleanup failures are auxiliary local incidents and never select another state.
- `CANCELLED` requires prior durable validated cancellation authority/replay evidence and the exact allowed submission boundary.
- Linux durable filesystem semantics fail closed; non-Linux entry points fail before admission/mutation.
- Task 2 remains network-free and contains no live GitHub provider implementation.

## Explicit non-goals

- No `net/http`, live GitHub credentials, URL construction, redirects, compression, request-byte instrumentation, or authenticated provider transport.
- No `api.github.com` or `/graphql` implementation, ordinary PR merge endpoint, REST ref mutation, or live `updateRefs`.
- No live provider capability probing/conformance test, publication, PR creation, merge orchestration, deployment, Phase 5+, scheduler/outer-loop, or dashboard/API work.
- No `CURRENT_STATE.md`, `PROGRESS.md`, or `AUDIT_INDEX.md` reconciliation in this task.

### Task 1: Implement Task 2 controller policy, admission, execution and recovery

- [x] Add ledger transition-barrier and exact append-or-verify semantics shared by state writers; preserve existing transition behavior when no barrier is active.
- [x] Add only the narrow `run` and `integrationgate` provenance needed for exact project/plan/run/attempt READY reconstruction and controller/integration-gate provenance.
- [x] Create network-free `internal/mergelifecycle` controller schemas, immutable limits/configuration, strict canonical recovery, and provider dependency interfaces.
- [x] Implement Linux authority derivation, safe durable attempt store, counters/reservations, bounded `records/` and `tmp/`, cancellation replay/channel, terminal-intents channel, cleanup incidents, recovery inventory, and fail-closed non-Linux stubs.
- [x] Implement controller-only initial authorization/admission, deterministic merge recipe/result-commit preparation, fresh final PR/pagination/policy revalidation under shared locks, authorization-seal fsync linearization, target commitment/barrier, and exactly one fake target submission.
- [x] Implement full-input reconciliation, typed APPLIED/NOT_APPLIED/UNKNOWN handling, cancellation precedence, descendant-aware post-merge acceptance, and exact READY_FOR_MERGE -> MERGED/FAILED/CANCELLED terminal/event append-or-verify recovery.
- [x] Ensure valid applied result plus durable post-merge proof immediately persists irreversible MERGED intent/event before cleanup; every later cleanup failure is local-only with zero provider retries and no second transition.
- [x] Add deterministic fake-provider, concurrency, authority/policy, cumulative-budget, cancellation, storage-integrity, crash-boundary, reconciliation, ledger-ambiguity, post-merge and terminal-order adversarial tests. Use deterministic synchronization rather than sleeps.
- [x] Prove no Task-3 dependency: Task-2 production files contain no `net/http`, `api.github.com`, `/graphql`, ordinary PR merge endpoint, REST ref-update implementation, credentials, `httptest`, or live conformance logic; `go list -deps ./internal/mergelifecycle` must not contain `net/http`.
- [x] Run gofmt, Task-2 package tests, repeated concurrency/crash tests, race tests, full repository tests/vet, non-Linux compile-only validation, scope checks and `git diff --check`.
- [x] Commit implementation and move this plan to `docs/plans/completed/` only after all implementation-owned checks pass. Do not claim deterministic ABCP acceptance or exact-head review inside the implementation task.

## Required deterministic gates

At minimum run:

- `go test -count=1 ./internal/githublifecycle`
- `go test -count=1 ./internal/ledger ./internal/run ./internal/integrationgate ./internal/mergelifecycle`
- `go test -count=1 -race ./internal/ledger ./internal/run ./internal/integrationgate ./internal/mergelifecycle`
- bounded repeated deterministic concurrency/crash tests with `-count=25`
- `go test -count=1 ./...`
- `go vet ./...`
- non-Linux compile-only checks for Task-2 packages/dependants
- `git diff --check`
- exact changed-path allowlist from this plan

## Completion boundary

Ralphex completion is implementation completion only. Task 2 is not independently accepted until fresh deterministic acceptance and a fresh exact-head Critical/Major review pass. Task 3 remains blocked until that Task-2 closure.

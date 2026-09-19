# EP-006 Track B — Projection / Read-Model and Existing-Ledger Access

## Authority

Predecessor Track-A exact head: `f082cae1677d002825b7f4176d9783e199c9a125`, independently accepted 0C/0M/0m.
Accepted EP-006 design: `908710e1606b0da761e612c36b85c866e182c7f7`.
Track A API routing, DTO seams, auth, catalog cursor/signing and runtime-catalog semantics are frozen and read-only.

## Owned maximum

- `internal/readmodel/**`
- `internal/ledger/jsonl.go`
- `internal/ledger/jsonl_test.go`
- `internal/ledger/readonly.go` and `internal/ledger/readonly_test.go` if needed
- this plan, moved to `docs/plans/completed/` only after validation
- no `internal/serviceapi/**`, runtimecatalog, cmd, timeline/evidence/action/run/recovery/scheduler/integration/GitHub mutation

### Task 1: Implement Track-B authoritative projections

- [x] Add existing-file-only ledger openers: restricted read-only `OpenExistingReadOnlyJSONLLedger`, writable `OpenExistingJSONLLedger`, idempotent `Close`, pinned parent+ledger identity, no creation/materialization, fail closed after close or replacement.
- [x] Implement one Track-B read-model service with context-cancellable global limits of 8 open service ledger objects and 4 concurrent snapshots; no cache; one snapshot maximum per read.
- [x] Decode one complete validated snapshot in ledger order; reject partial/malformed/oversized/conflicting history. Only typed `RUN_CREATED` with no state edge and payload state `RUN_CREATED` may establish initial state; later state comes only from valid state edges.
- [x] Preserve full attempt/task/session history without overwrite; retain first/last event identity/time, current durable state, event/evidence counts, terminal status and recognized blocker/decision/lifecycle summaries without allowing payload to override core identity/state.
- [x] Compute deterministic `projection_revision` from ledger physical identity digest, exact snapshot digest/length, last ordinal/event ID and projection schema version.
- [x] Implement the Track-B `ledger` cursor payload using the frozen Track-A `CursorEnvelopeV1` signer/verifier: bind run, physical identity, prior record lineage, ordinal/event/line digest and page position; allow append-only extension only when every bound prior record is byte-identical; replacement/truncation/partial-line mismatch is lineage conflict.
- [x] Implement structural dependencies satisfying frozen run-detail and event-page reader seams without modifying `internal/serviceapi`; never expose registered ledger/evidence filesystem paths.
- [x] Add adversarial tests for missing/replaced ledgers, close semantics, semaphore cancellation/ceilings, explicit initial state, invalid chronology, repeated attempts/tasks/sessions, deterministic revision, event ordering, cursor tamper/epoch/kind/filter/lineage, append extension, truncation/replacement/partial line and path non-disclosure.
- [x] Run gofmt; focused tests/race; full `go test ./...`; full `go test -race ./...`; `go vet ./...`; Darwin/Windows compile-only for affected packages; `git diff --check`; exact owned-path and forbidden-semantic checks.
- [x] Mark every item complete, move this plan to `docs/plans/completed/`, create exactly one Track-B implementation commit, and leave the exact worktree clean.

## Completion boundary

Ralphex completion is implementation only. Fresh deterministic acceptance and fresh exact-head independent Critical/Major review at 0C/0M are required before Track C.

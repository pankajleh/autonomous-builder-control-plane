# EP-006 Track B exact-head review corrections

Exact reviewed candidate: `1d639186ac54f1484dcd7eb7c4ff32b9ef15fecc`.
Accepted EP-006 design: `908710e1606b0da761e612c36b85c866e182c7f7`.
Accepted Track-A predecessor: `f082cae1677d002825b7f4176d9783e199c9a125`.
Deterministic acceptance summary SHA-256: `05ff91afcf64137973f8a0f6f2404b2ca09e9fddbe553fda2dcc5c686170ec14`.
Exact-head review summary SHA-256: `1e64433141a7c70dc57fb6b7e292a929d7898084f556da09a77f43a2487bc657`.
Review verdict: `IMPLEMENTATION_FINDINGS`, Critical 0 / Major 6 / Minor 0.

This correction is limited to the six exact Track-B Majors below. Track A remains frozen; Track C/D remain blocked. Maximum implementation scope is `internal/ledger/jsonl.go`, `internal/ledger/jsonl_test.go`, `internal/ledger/readonly.go`, `internal/ledger/readonly_test.go`, `internal/readmodel/**`, and this plan/completed-plan move.

### Task 1: Close the six Track-B exact-head Majors

- [x] M1: make `JSONLLedger.Close`, snapshot and append synchronization race-free; no descriptor/identity field may be observed or cleared outside the same mutex discipline, and use-after-close must fail closed.
- [x] M2: make replacement races fail closed by re-proving exact pathname physical identity after the locked operation before returning success; old unlinked/replaced objects must never produce a successful snapshot or append.
- [x] M3: prevent writable-ledger callers from bypassing the service-wide four-authoritative-snapshot ceiling; retain the shared eight-ledger-object ceiling and no cache.
- [x] M4: classify a newly appended partial/malformed record as `projection_integrity_failure` when every cursor-bound prior byte/record remains identical; reserve `projection_lineage_changed` for mutation/replacement/truncation/mismatch of bound prior lineage.
- [x] M5: validate every service-visible `attempt_id` against the frozen service identifier grammar before projection/exposure; malformed attempt identity must fail closed as projection integrity.
- [x] M6: apply raw-path non-disclosure protection consistently to recognized-summary fields including lifecycle `event_type`; no registered ledger/evidence path may escape through any DTO field.
- [x] Add targeted adversarial tests for concurrent Close/Snapshot/append, replace-after-precheck races, writable snapshot ceiling, partial-new-record classification, invalid attempt IDs, and lifecycle-summary path disclosure.
- [x] Run gofmt; focused tests/race; uncached full tests/race; `go vet ./...`; production Darwin/Windows compile for affected packages; exact scope/forbidden-path checks; `git diff --check`.
- [x] Mark every item complete, move this plan to `docs/plans/completed/`, create exactly one correction implementation commit, and leave the worktree clean.

Ralphex completion is correction implementation only. The resulting exact head must separately pass fresh deterministic acceptance and a fresh independent exact-head Critical/Major closure review at 0C/0M before Track C may start.

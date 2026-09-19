# EP-006 Track A — Closure Correction 02

## Authority

Exact reviewed head: `fbe98f394f3ef15bd3fc013c59202cd1fb93cf52`.
Accepted design: `908710e1606b0da761e612c36b85c866e182c7f7`.
Deterministic acceptance: PASS, SHA-256 `53f5f5ffb901342787946d8ee75671a2f70a86f0085f0ba38c56d5b7baa9d472`.
Fresh exact-head closure review: `7651cc212c61f9efd7581b6762cbc3c6b8b9f4f4d47e53517ad7d92416780d93`.
Frozen finding set is EXACTLY one Major and no other blocker may enter this correction pass.

## Correction-owned maximum

- `internal/runtimecatalog/**`
- this plan, moved to `docs/plans/completed/` after validation
- no serviceapi/cmd/B/C/D/ledger/evidence/run/recovery/scheduler/integration mutation

### Task 1: Close the sha256 storage-component ambiguity

- [x] Reserve the encoded storage prefix without changing public run-ID grammar: every identifier that exceeds the direct-name limit OR begins with the reserved `sha256-` encoding prefix must map to an unambiguous encoded component with its sidecar.
- [x] Ensure a valid short `sha256-*` run ID cannot be mistaken for an encoded long-ID component and an exact hash-shaped valid ID cannot alias a long identifier's component.
- [x] Preserve immutable registration, lexical run-ID ordering, signed cursor semantics, <=16 MiB catalog physical-read ceiling, and all accepted Track-A public behavior.
- [x] Add focused regression tests covering short `sha256-*`, exact hash-shaped valid IDs, long IDs, coexistence, registration/read/list pagination, and no collision/integrity failure.
- [x] Run gofmt; focused runtimecatalog tests/race; full tests/race; vet; Darwin/Windows compile-only for affected package; git diff --check; exact path-scope/forbidden-semantic checks.
- [x] Mark all items complete, move this plan to `docs/plans/completed/`, create exactly one implementation commit, and leave the exact worktree clean.

## Completion boundary

Ralphex completion is implementation only. Fresh deterministic acceptance and fresh exact-head Critical/Major closure review are required. Track B remains blocked until 0C/0M.

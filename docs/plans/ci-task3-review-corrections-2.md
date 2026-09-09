# CI Task 3 review corrections — second pass

## Scope

Close only the three blocking findings from the fresh exact-head review of `39db34bd4f04dd6b85b9e5444eee86a2f1ea1b05`: M-001, M-003, and M-006. Preserve the already-closed M-002, M-004, and M-005 behavior. Task 4 and publication remain out of scope.

### Task 1: Close M-001, M-003, and M-006

- [ ] M-001: ensure a newly created reservation can never turn pre-positioned bundle/event material into historical proof after any transient scan/stabilization failure; poison/fail closed before such material can become replay-eligible.
- [ ] Add controller-level regression coverage for transient pre-existing-material inspection failures followed by retry.
- [ ] M-003: require successful authoritative-ledger stabilization/fsync before replaying an existing deterministic event; a readable line after failed fsync is not sufficient proof of durability.
- [ ] Add controller-level regression coverage for ledger fsync failure followed by retry through the normal `Collect` path.
- [ ] M-006: add the missing non-Linux `attemptLease.poison` fail-closed stub and a non-Linux cross-compilation gate.
- [ ] Preserve M-002/M-004/M-005 fixes, current policy bytes, frozen lifecycle packages, and neutral read-only CI semantics.
- [ ] Run focused tests/race tests, full tests/vet/smoke, non-Linux compile, scope/policy/forbidden-semantics checks, and `git diff --check`; commit exactly once.

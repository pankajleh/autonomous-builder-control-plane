# EP-005 CI Task 4 fresh-review corrections

## Scope

Close only M-001 and M-002 from the fresh post-Task-4 review of exact CI implementation `3e0f295e8f98c348d684b2c026bacd9ceeeea911`, whose Task-4 audit candidate `00cf3d5204a40f240301d453f9444b8a9a002719` reached `BRANCH_ACCEPTED`. Preserve all prior Task-3 fixes, Task-4 audit evidence, neutral read-only CI semantics, and the PR #14 predecessor boundary. Publication and later Phase-4 work remain blocked until this correction is accepted and freshly reviewed 0C/0M.

### Task 1: Close M-001 and M-002

- [x] Verify the fresh v2 capsule path/hash and run independent `context-verify` before any edit; stop on mismatch or drift.
- [x] M-001: treat repeated GitHub `Link` header fields as one bounded semantic input, enforce the aggregate Link-data cap, and detect `rel=next` across every field value for every paginated endpoint. Require `X-GitHub-Request-Id` to be unambiguous (exactly zero or one value) and bounded; repeated/ambiguous request IDs must fail closed.
- [x] Add regression coverage for a later repeated `Link` value carrying `rel=next`, aggregate repeated-Link exact-limit/limit+1 behavior, and repeated request-ID rejection.
- [x] M-002: make ordinary authoritative `JSONLLedger.Append` and CI material-ledger snapshot/check/append/rollback participate in the same file-level serialization so an unrelated append cannot be truncated or invalidate projected bounds while CI owns the transaction.
- [x] Add regression coverage that overlaps an ordinary authoritative append with an injected partial/ambiguous CI append and proves the unrelated event remains intact and the ledger remains canonical/bounded.
- [x] Preserve M-001/M-003/M-006 Task-3 closures, M-002/M-004/M-005 preservation, immutable evidence behavior, replay durability, bounded-resource rules, non-Linux fail-closed compilation, and zero merge-policy/write/transition semantics.
- [x] Run focused tests, race tests, ledger tests, full repository tests/vet, non-Linux compile, forbidden-semantics/scope checks, and `git diff --check`; mark this task complete and commit only this correction.

## Gate

No publication or later Phase-4 work is authorized by implementation success alone. The exact corrected candidate must reach deterministic ABCP acceptance and then receive a fresh exact-head Critical/Major review with 0 Critical / 0 Major.

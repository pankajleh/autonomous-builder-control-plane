# EP-005 CI Task 3 — Exact-head review corrections

Status: executable correction-only plan.
Base authority: accepted materialization head `0314fe49f38c99e90fb7fb0df771c9225bd5ca02`.
Blocking review: exact Task-3 head `b5c7e2cd0ea3cc223f481b1d73a78c6276846639`, review artifact SHA-256 `d0a3f692f19d9dc5b42a13abf8c71bbd18469a57d8920183ca86345953ccf6f3`, verdict 0 Critical / 5 Major.

## Scope

Correct only M-001 through M-005 at their root causes while preserving Task 3's neutral read-only CI-evidence semantics. Changes may touch `internal/cilifecycle`, `internal/evidence`, and `internal/ledger` only where required for the five durability/boundedness corrections, plus this plan. Do not implement Task 4, required-check policy, merge approval, GitHub writes, expected-head merge protection, merge execution, post-merge acceptance, or automatic operation handoff.

### Task 1: Close M-001 through M-005

- [ ] Before code changes, independently verify the launch-authority v2 capsule and exact base SHA; stop on any mismatch.
- [ ] M-001: make pre-positioned event/bundle material remain fail-closed when discovered around a newly created reservation; a retry must never convert that failed first encounter into historical proof. Add regression tests proving repeated retries cannot promote pre-positioned material.
- [ ] M-002: make deterministic evidence publication durably end with exactly one verified final link/path; temporary-link removal must be checked and directory-synced, and read-back must not convert failed durability into success. Add crash/fault tests around link, unlink, final-file sync, and directory sync.
- [ ] M-003: make authoritative outcome append a serialized bounded transaction: check projected bytes/lines before append, handle owned partial-tail writes safely, and require successful fsync before success. Readability/page-cache confirmation must not mask append/fsync failure. Add boundary, partial-write, append-error, fsync-error, and concurrency tests.
- [ ] M-004: before accepting an existing exact reservation after a prior persistence failure, successfully sync the reservation and pinned directory and revalidate named inode + canonical bytes. Add file-sync and directory-sync retry tests.
- [ ] M-005: make allocator inventory bounded before allocation (configured limit plus sentinel only), and lifecycle/refcount process-local keyed locks so inactive keys are removed safely. Add contaminated-directory and repeated-unique-attempt memory/lifecycle tests.
- [ ] Preserve exact Task-3 evidence semantics: immutable/create-or-verify artifacts, deterministic attempt/bundle identity, same-attempt recovery, exact zero-network replay, empty event transition fields, bounded local state, and no merge-policy semantics.
- [ ] Run focused unit/race tests for `internal/cilifecycle`, supporting `internal/evidence`/`internal/ledger` tests, full repository tests/vet/smoke, frozen-policy checks, forbidden-semantics checks, and `git diff --check`.
- [ ] Commit one correction feature commit with all five findings closed; leave Task 4 non-executable.

## Required correction evidence

The correction must include tests that would fail on `b5c7e2cd0ea3cc223f481b1d73a78c6276846639` for each M-001 through M-005 root cause. Acceptance of this correction does not authorize publication. After deterministic acceptance, freeze a fresh `implementation-review` capsule at the exact corrected head and require 0 Critical / 0 Major before Task 4 can be authorized.

## Non-goals

- No Task 4 implementation or publication work.
- No required-check/merge-approval/branch-protection/ruleset policy.
- No GitHub write methods or domain state transitions.
- No automatic-operation-handoff implementation.
- No weakening of context authority, exact-head acceptance, or state-projection rules.

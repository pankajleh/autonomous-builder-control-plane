# EP-005 CI Evidence Ingestion — Deferred Task 4 Acceptance Audit

## Authority boundary

- Accepted and merged projection predecessor: `6fd7b12c41b05e158df19b698028fb217a41eb23` (PR #14 merge).
- Exact accepted/reviewed CI implementation under audit: `3e0f295e8f98c348d684b2c026bacd9ceeeea911`.
- PR #13 merge containing that implementation: `bf2482f756a7c5f75906825b8f3e6e454c72f94f`.
- This operation is audit/documentation only. It must not change `internal/`, `cmd/`, architecture contracts, roadmap semantics, execution-pack behavior, or the three operational projections.

### Task 1: Execute Deferred Task 4 acceptance, scope audit, and review handoff

- [ ] Verify the supplied fresh v2 capsule hash and `context-verify` at the exact Task-4 plan checkpoint before any audit work.
- [ ] Prove `internal/cilifecycle` and `docs/architecture/CI_EVIDENCE_INGESTION_CONTRACT.md` are unchanged from exact implementation head `3e0f295e8f98c348d684b2c026bacd9ceeeea911`.
- [ ] Run the design validation matrix: gofmt, vet, package tests, race tests, regression packages, and full repository tests.
- [ ] Re-run forbidden-package checks and forbidden-semantics checks without relying on `rg` availability.
- [ ] Verify exact-limit/limit+1 behavior for request, response/text, encoded-size, attempt/capacity, ledger-scan, and duration boundaries using the existing adversarial tests and production-limit identity tests.
- [ ] Verify every durable completed non-success outcome exercised by tests is ledger-reachable/replayable; verify `STABLE` remains neutral and has no acceptance/approval field.
- [ ] Verify app-installation/malformed-principal rejection occurs before allocator, evidence, ledger, and network hooks.
- [ ] Reconcile the implementation against the Phase-4 CI-ingestion scope and record that merge policy, required-check policy, expected-head protection, merge execution, post-merge acceptance, and automatic handoff remain out of scope.
- [ ] Write `docs/audits/EP005_CI_TASK4_ACCEPTANCE_AUDIT.md` recording exact target SHA, merged predecessor, commands/check classes, results, and the fresh-review handoff requirement. Do not claim review success before the separate reviewer runs.
- [ ] Run `git diff --check`, mark this task complete, and commit only this Task-4 audit operation.

## Publication gate

Task 4 does not itself authorize later Phase-4 implementation. The exact CI implementation SHA `3e0f295e8f98c348d684b2c026bacd9ceeeea911` must receive deterministic acceptance evidence from this fresh PR-#14-based operation and then a fresh exact-head Critical/Major review with 0 Critical / 0 Major before the CI-ingestion subtrack may be treated as publication-complete.

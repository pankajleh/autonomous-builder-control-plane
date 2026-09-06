# EP-004 Foundation — Controller Review Corrections

## Context Authority

- Roadmap: Phase 3 — Cross-plan scheduler and integration.
- Execution pack: `docs/execution-packs/EP-004-cross-plan-scheduler-integration.md`.
- This is a bounded correction of the already controller-accepted EP-004 foundation; do not add later Phase-3 deliverables.
- Governed base identity: use the exact `base_sha` in the verified capsule/run authority.
- Fresh tasks must verify the bound context capsule before reading implementation files or making changes.
- Frozen-boundary intent remains: later parallel tracks consume `internal/scheduler/` read-only after this correction is accepted.

### Task 1: Correct final-diff provenance and Git safety

- [ ] Before implementation, verify the bound context capsule against the governed repository; stop on any drift.
- [ ] Bind the full canonical `RiskPolicy` contents into `RiskReport` evidence, not only the human-readable policy identity, so two different root configurations cannot produce indistinguishable policy provenance.
- [ ] Ensure report canonical JSON/digest changes when canonical risk-policy contents change, even when the resulting risk class and candidate diffs happen to be identical.
- [ ] Disable Git replacement-object/graft semantics for every scheduler-owned Git command so `refs/replace/*` cannot change ancestry or final-diff results for exact SHAs.
- [ ] Add regression evidence proving a repository replacement ref cannot suppress or alter the committed final diff observed by the analyzer.
- [ ] Bound scheduler Git stdout/stderr capture and fail closed on truncation instead of buffering unbounded output in controller memory.
- [ ] Preserve structured argv evidence and make truncation/limit behavior explicit enough for later audit.
- [ ] Keep Git execution cancellable; do not silently convert cancellation into a successful or ordinary risk classification.
- [ ] Harden branch-name validation so names unsafe under `git check-ref-format --branch` (including a leading dash) are rejected before entering the frozen candidate contract.
- [ ] Add tests for policy-content binding, replacement-ref immunity, bounded/truncated Git output, cancellation behavior where practical, and unsafe branch rejection.
- [ ] Do not implement disposable integration workspaces, textual conflicts, combined acceptance, semantic conflicts, or `READY_FOR_MERGE` in this correction.
- [ ] Run `gofmt -w cmd internal`, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`.
- [ ] Mark only this correction task complete and commit the bounded changes.

## Controller review findings being corrected

1. **Major — risk-policy provenance is not content-bound.** `RiskReport` records `PolicyIdentity` but omits the canonical protected-root configuration, so materially different policies can share the same apparent policy provenance and, for unaffected candidates, the same report bytes.
2. **Major — exact-SHA analysis is replace-ref sensitive.** Scheduler Git commands currently permit repository `refs/replace/*`; Git can therefore substitute different commit/tree content while commands still name the accepted exact SHA.
3. **Major — Git output capture is unbounded.** `execGitRunner` buffers complete stdout/stderr in memory; a very large accepted diff can exhaust the controller rather than producing a bounded fail-closed result.
4. **Hardening — branch validation is weaker than the claimed Git branch contract.** A leading-dash branch passes the local validator although `git check-ref-format --branch` rejects it.

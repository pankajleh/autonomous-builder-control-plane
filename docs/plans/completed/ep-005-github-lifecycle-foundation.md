# EP-005 GitHub Lifecycle Foundation

## Overview

Implement only the serial Phase 4 foundation required before PR/CI/merge work can proceed. No GitHub network calls or remote writes are authorized by this plan.

## Design-gate outcome

Controller design review was followed by independent Claude design review. The first independent pass found one Critical plus two Major design gaps before implementation: strategy-aware post-merge identity, timeout/cancellation semantics for ambiguous writes, and authenticated acting-principal provenance. Those gaps were corrected in the design. The exact corrected design was independently re-reviewed and returned `DESIGN_CLEAN_CRITICAL_MAJOR`. Implementation is authorized only from the committed corrected design and its verified context capsule.

## Authority and trust boundaries

- Base: the exact Phase 3 merge SHA bound by the context capsule.
- The authority binds a non-secret authenticated acting principal/app-installation identity for remote writes; secret credentials/tokens never enter authority, requests persisted as evidence, or ledgers.
- The authority binds the allowed merge method and the exact pre-merge base-tip SHA independently of the accepted PR head SHA.
- New package: `internal/githublifecycle/`.
- Remote/provider input is untrusted until structurally bounded and tied to the exact repository/base/head authority.
- This task defines contracts only; it must not execute network requests, shell GitHub commands, PR writes, merges, or state transitions.
- No existing `internal/scheduler/`, `internal/integrationworkspace/`, `internal/combinedacceptance/`, or `internal/integrationgate/` contract may be modified.

### Task 1: Freeze GitHub lifecycle contracts

- [x] Define immutable/copy-safe identities for governed repository, base branch, head branch, exact head SHA, expected pre-merge base-tip SHA, optional PR identity, remote snapshot identity, allowed merge method, and non-secret authenticated acting principal/app-installation identity.
- [x] Define bounded provider result types for PR snapshot, CI/check snapshot, merge result, and post-merge observation without embedding unbounded remote bodies/logs. Merge/post-merge types must carry enough structured identity for strategy-aware proof: accepted head SHA/tree, base-before SHA, merge method, result/base-after SHA, result tree, and parent/lineage data where applicable; never assume post-merge SHA equals accepted head SHA.
- [x] Define a provider interface whose read/write methods use structured typed inputs and context/deadline control; no method may accept free-form shell/URL fragments as authority. Every write input/result must bind the authenticated acting identity without exposing credentials.
- [x] Define explicit outcome/error classes that distinguish unavailable/ambiguous provider execution from substantive policy/CI/review failure. Cancellation or deadline after a write may have been submitted is always an ambiguous write, not a clean retryable failure.
- [x] Define foundation resource limits for remote page/item counts, text fields, evidence-reference counts, per-call timeout/deadline, and retries; retry authority defaults to zero for ambiguous writes until an explicit reconciliation result proves the prior outcome.
- [x] Canonicalize ordering and JSON/digest identity for snapshots that will later become immutable evidence.
- [x] Validate exact Git SHA syntax and safe branch/repository identifiers without silently normalizing different identities into equality.
- [x] Add adversarial tests for moved/mismatched head, moved pre-merge base tip, stale CI SHA, duplicate/ambiguous PR identity, unsupported/changed merge method, post-merge SHA divergence with correct/incorrect tree-lineage proof, wrong acting identity, cancellation/deadline during a write, forbidden retry after ambiguous write, oversized collections/text, unsafe identifiers, mutation of caller-owned slices/maps, and nondeterministic input ordering.
- [x] Document the frozen foundation contract for later PR/CI/merge tracks.
- [x] Run `gofmt`, focused tests, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`; commit only this bounded foundation.

## Success criteria

Later Phase 4 tracks can consume one frozen, deterministic, fail-closed GitHub lifecycle contract without inventing DTO fields, retry semantics, remote identity rules, or merge authority.

## Non-goals

No network provider implementation, PR creation/update, CI polling, merge execution, post-merge transition, Phase 5 API/dashboard, or production work.

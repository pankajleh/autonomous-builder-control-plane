# EP-004 Track D — Serial Gate Assembly

## Overview
Implement the final Phase 3 serial authority that binds the reviewed Track B textual-integration result to Track C combined acceptance and is the only EP-004 component allowed to assert `READY_FOR_MERGE`.

## Context
- Roadmap authority: Phase 3 / EP-004 `READY_FOR_MERGE` gate.
- Reviewed Track B head: `d8d4299e040f3dbca8a920b8d338c8e06bc04295` (`CLEAN_CRITICAL_MAJOR`).
- Reviewed Track C head: `a35b2d23e2367e793af0fb7c1cc9c872555f5b65` (`CLEAN_CRITICAL_MAJOR`).
- Exact serial assembly merge: `5ae83c622e2415e19b1dde4930413bd9d8c19a80`, parents B then C.
- Track A `internal/scheduler/` remains frozen unless an explicitly necessary serial contract revision is proved by tests.
- Track B and Track C APIs must not be weakened; additive lifecycle support is allowed only where required to run combined acceptance against the exact integrated result.

### Task 1: Implement the serial integration/merge gate
- [x] Add a controller-owned Track D package (prefer `internal/integrationgate/`) that accepts validated authority, exact accepted candidates, the immutable risk report, governed combined-acceptance policy, review attestations, blocker/prerequisite state, an event appender, evidence writer, and required runtime roots.
- [x] The gate must obtain textual integration from `internal/integrationworkspace` and construct Track C `IntegrationProvenance` exclusively from that exact result; callers must not be able to inject an arbitrary integrated head, target path, candidate order, risk digest, textual status, or precomputed Track C result.
- [x] Solve the Track B cleanup lifecycle safely: Track D must controller-materialize/reproduce the exact clean integrated target, prove it matches the bound Track B result/digest/head, keep it isolated only through combined acceptance, and always perform bounded cleanup afterward. Do not leave a caller-selected checkout as the acceptance target.
- [x] Run `internal/combinedacceptance` from the gate itself against that exact materialized target and bind its result SHA/evidence back to the exact Track B result, candidate set, risk report, repository identity, and policy identities.
- [x] Re-verify candidate source heads are unchanged at the merge-readiness boundary; replacement refs, malformed/ambiguous evidence, truncation, unavailable validation, target mutation, incomplete cleanup, or provenance mismatch must fail closed.
- [x] Map outcomes deterministically: textual conflict → `INTEGRATION_CONFLICT`; semantic conflict → `SEMANTIC_CONFLICT`; unavailable/ambiguous validation → `VALIDATION_UNAVAILABLE`; only clean combined acceptance may produce `INTEGRATION_ACCEPTED`.
- [x] `READY_FOR_MERGE` requires, in addition to `INTEGRATION_ACCEPTED`: exact branch-acceptance provenance for every candidate, required review policy satisfied against exact reviewed SHAs, no unresolved blocker, complete verified evidence/provenance, and unchanged expected heads. A substantive non-clean review verdict can never be treated as provider failure or bypassed.
- [x] Emit only valid ledger transitions in order (`BRANCH_ACCEPTED → INTEGRATION_PENDING → INTEGRATING → ...`; success continues `INTEGRATION_ACCEPTED → READY_FOR_MERGE`). Every transition must carry bounded immutable evidence refs sufficient to reproduce the decision. Never emit `READY_FOR_MERGE` directly from implementation completion, Track B, or Track C.
- [x] Add deterministic regressions for caller-forged Track B/C provenance, changed candidate heads, review SHA/verdict mismatch, unresolved blocker, missing/mutated evidence, textual conflict, semantic conflict, validation unavailable, cleanup failure, and the complete clean path through `READY_FOR_MERGE`.
- [x] Preserve immutable-source boundaries: source/candidate branches and primary worktrees are read-only; no GitHub PR/merge, deployment, production acceptance, dashboard/API, or Phase 4+ work.
- [x] Run `gofmt` on changed Go files, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`.
- [x] Confirm final diff is bounded to Track D plus only explicitly necessary serial integration lifecycle/wiring changes; commit the completed task.

## Success criteria
- [x] Exact Track B → Track C provenance is controller-constructed and cryptographically/evidentially bound, not caller-asserted.
- [x] The exact integrated target exists only inside controller-owned disposable scope for combined acceptance and is cleaned on every terminal path.
- [x] Conflict/unavailable paths cannot reach `INTEGRATION_ACCEPTED` or `READY_FOR_MERGE`.
- [x] Clean path reaches `READY_FOR_MERGE` only after every architecture prerequisite is independently proven.
- [x] Existing Track A/B/C deterministic tests remain green and the canonical state-machine invariant remains intact.

## Non-goals
GitHub lifecycle/merge automation, service/API/dashboard, deployment/production acceptance, worker leasing, or any Phase 4–6 feature.

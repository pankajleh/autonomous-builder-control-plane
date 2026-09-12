# EP-005 — Task 3 Five-Finding Closure Correction

## Authority

This correction is authorized solely by the sealed exact-head Task-3 closure review of candidate `d84191e48f935afa16c3e315f7e20d710f92830c` (tree `87edb595935359a24144e8367e1450d26b13c58f`), verdict SHA `9cbd127f72b11a422e8a5b0f87d65797aa6dc87e04edd4db4e44ed264a00da8a`, review seal `ee24e82196a67ee591f9008aa1284867206d3298c88bcd760e996c97383def9b`.

The five blockers are all mapped to already-accepted Task-3 requirements. Four are provider-local. Major-01 exposes the executable Task-3 plan's explicit interface/scope blocker: the current controller/provider handoff cannot durably reserve each actual HTTP request. This correction therefore authorizes only the smallest Task-2 seam amendment required to implement the already-accepted T3-14 semantics; it does not reopen any other Task-2 behavior.

## Exact five findings

1. `C-01` / T3-12 — reconciliation may report `APPLIED` without fresh PR/repository/principal identity and equal-or-descendant containment proof.
2. `M-01` / T3-14 — actual HTTP-call and active-time budgets are not compositionally/durably enforced across provider operations and restart.
3. `M-02` / T3-02,T3-05 — repeated `Link` / `Content-Encoding` headers are not rejected or losslessly represented.
4. `M-03` / T3-04 — app-installation credentials are accepted without independently proving the installation identity.
5. `M-04` / T3-18 — live wrong-head evidence is vacuous because the positive update runs first.

No sixth blocker family may be introduced by this correction. Independent observations outside these five are deferred unless they expose a direct regression of an existing frozen requirement.

## Owned paths

Provider correction paths:
- `internal/githubmergeprovider/**`

Narrow controller/provider budget seam only:
- `internal/mergelifecycle/types.go`
- `internal/mergelifecycle/controller.go`
- `internal/mergelifecycle/store_linux.go`
- `internal/mergelifecycle/store_unsupported.go`
- `internal/mergelifecycle/task3_provider_budget_test.go` (new, if useful)
- existing `internal/mergelifecycle/*_test.go` only where required to prove the same budget seam without changing unrelated semantics.

Correction plan path:
- this file, later moved byte-identically except checked boxes to `docs/plans/completed/`.

Everything else is frozen, including `internal/githublifecycle/**`, `internal/ledger/**`, `internal/run/**`, `internal/integrationgate/**`, `cmd/**`, `go.mod`, `go.sum`, status/progress docs, Phase 5+, and Task-4/post-merge work.

## Narrow budget-seam amendment

The provider method signatures remain unchanged. The runtime context handoff may be narrowly extended so each actual outbound HTTP request must obtain a controller-owned durable reservation immediately before any network write.

Required semantics:
- the controller binds an exact call class (`pre-submit`, `target`, `reconciliation`, or `post-merge`) into each provider invocation context;
- the provider must synchronously reserve one actual HTTP request through the controller-owned handoff before transport invocation; reservation failure prevents that request;
- the durable reservation increments the correct phase counter and the attempt-wide `TotalProviderCalls` before network I/O and survives crash/restart;
- `ProviderBudgetV1` exposes remaining actual HTTP-call allowance in addition to existing byte/time allowance;
- `ProviderAccountingV1` reports actual HTTP calls for independent cross-checking but never creates retry authority or replaces the durable pre-call reservation;
- existing operation/submission reservations continue to own commit-submission, target-submission, reconciliation-round, cancellation, and terminal semantics; do not reinterpret those identities;
- exact production ceilings remain 28 pre-submit HTTP calls, 3 reconciliation calls per round, 8 post-merge HTTP calls, 64 total HTTP calls, and the existing byte/time limits;
- active time is measured through complete bounded body read/decompression and body close, not merely through receipt of response headers;
- each request deadline is capped by the lesser of the per-call timeout and remaining cumulative active-time budget before transport;
- crash/restart cannot reset call or time budget and a missing/ambiguous accounting continuation fails closed.

If these semantics cannot be implemented through this narrow seam without changing any other frozen package/contract, stop as `SCOPE_EXPANSION_REQUIRED`; do not widen further.

## Provider corrections

### C-01 reconciliation
`ReconcileTarget` remains read-only. `APPLIED` requires fresh exact PR/repository/principal identity, exact result-object reconstruction, current target ref, and `github-compare-v1` proof that the target is identical to or a bounded descendant of the result. Absent/open/mismatched PR, unproved principal/repository identity, missing object, unrelated/behind/diverged target, or unavailable/truncated compare evidence remains `UNKNOWN`. No reconciliation path may mutate a ref or synthesize success from `merged=true` alone.

### M-02 repeated headers
All singleton headers used by the sealed response contract must reject repeated values. `Content-Encoding` must be exactly zero/one allowed value; multiple field-values or comma-composed multiple encodings fail closed. Pagination must either reject multiple `Link` header fields or combine all values losslessly before the frozen Link parser/evidence digest; no value may be silently discarded.

### M-03 app-installation principal
Until an independently authenticated remote installation identity is available inside the frozen route set, production `NewAppInstallationAuthenticator`/provider admission must fail closed before any mutation. User-principal authentication behavior remains unchanged. Do not add a JWT/app-management credential path in this correction.

### M-04 live conformance
The controlled disposable-ref live harness must execute both wrong-base and wrong-head negative cases while the base ref is still at `BaseBefore`. In the wrong-head case, the valid base entry must propose a real `BaseBefore -> BaseAfter` change so a sequential/partial implementation would be observable. Verify both refs remain unchanged after each negative case, then run the accepted positive two-ref update and verify exact final refs. Cleanup remains mandatory and protected/product refs remain untouched.

## Frozen correction acceptance matrix

| ID | Required proof | Mandatory entrypoint/evidence |
| --- | --- | --- |
| F1 | C-01 fresh identity + exact object + equal/descendant containment before reconciliation `APPLIED` | `TestTask3ClosureC01ReconciliationIdentityContainment` |
| F2 | M-01 every real HTTP request is durably controller-reserved before network; phase/global call ceilings, restart, deadline, and full-body/close active-time accounting are exact | `TestTask3ClosureM01DurableHTTPCallBudgets` plus controller/store restart proof |
| F3 | M-02 repeated Link/encoding fields cannot be collapsed or silently accepted | `TestTask3ClosureM02RepeatedHeaders` |
| F4 | M-03 app-installation credential cannot reach provider mutation without independent installation proof | `TestTask3ClosureM03AppInstallationIdentity` |
| F5 | M-04 negative live cases are materially state-changing if partially applied and run before positive mutation | `TestTask3ClosureM04LiveAtomicityOrdering` plus a new separately sealed live run |

The five entrypoints above must exist exactly. Passing legacy tests without exercising these predicates is insufficient.

## Regression floor

Before the correction commit:
- all original 14 Task-3 named entrypoints pass;
- Task-1/Task-2 deterministic regressions pass, including the prior cumulative-budget and restart corrections;
- `go test -count=1 ./internal/githubmergeprovider ./internal/mergelifecycle` passes;
- focused and full repository `-race` pass;
- `go test -count=1 ./...`, `go vet ./...`, metadata-free smoke, and non-Linux compile pass;
- production code still contains no ordinary PR-merge endpoint, REST ref-update mutation, force update, or second target mutation path;
- exact changed-path allowlist and `git diff --check` pass.

Implementation phase remains live-mutation-free. The corrected live harness is executed only afterward under separate acceptance-only disposable-ref authority.

## Task 1: Close the exact five Task-3 review findings

- [ ] Implement only F1-F5 and the narrow controller/provider budget seam described above.
- [ ] Add the five exact regression entrypoints and any bounded controller/store tests necessary for durable HTTP-call reservation/restart proof.
- [ ] Run the full regression floor and prove no scope escape.
- [ ] Move this plan to `docs/plans/completed/` and create exactly one correction implementation commit.

## Closure boundary

Correction implementation alone does not close Task 3. Closure requires, against the exact correction head: deterministic acceptance, a fresh separately sealed controlled live-conformance run with the corrected ordering, and one fresh independent exact-head Critical/Major review returning 0 Critical / 0 Major.

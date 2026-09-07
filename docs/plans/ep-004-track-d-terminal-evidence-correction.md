# EP-004 Track D Terminal Evidence Correction

## Overview
Correct the remaining substantive Major found by final independent review of exact head `f7c3c1f0029e857a98c7b45cb9514af2f51baa78`. Keep scope inside Phase 3 / EP-004 Track D.

## Context
- Track B reviewed head: `d8d4299e040f3dbca8a920b8d338c8e06bc04295`.
- Track C reviewed head: `a35b2d23e2367e793af0fb7c1cc9c872555f5b65`.
- Track D correction head `f7c3c1f...` is ABCP `BRANCH_ACCEPTED` and exact-head deterministic validation passed.
- Final Claude re-review confirmed the prior four Major classes are fixed except one terminal-evidence failure path.
- The defect: an unverified materialization evidence ref can escape from `UseMaterialized`; then `Gate.finish` re-verification can return while the durable state remains `INTEGRATING`.

## Pre-implementation design gate
- Trust boundary: no evidence ref becomes part of a returned Track B materialization result until its bytes, root containment, type, bound, identity and digest have verified.
- State invariant: after `INTEGRATING` is durably emitted, every evidence-validation/resource-bound failure that can still write controller evidence must durably leave `INTEGRATING`.
- Evidence invariant: terminal fallback transitions contain only freshly verified evidence refs; rejected refs may be described/hashes recorded as failure metadata but are never attached as transition evidence.
- Post-acceptance invariant: if evidence becomes unverifiable after `INTEGRATION_ACCEPTED` but before `READY_FOR_MERGE`, fail closed to an allowed state rather than leaving an unrecoverable accepted-but-not-ready run.
- Unavoidable ledger/evidence-store write failure remains a controller infrastructure failure; do not fabricate a transition when durable append itself is unavailable.
## Task 1: Eliminate terminal evidence stranding
- [ ] In `internal/integrationworkspace.UseMaterialized`, hold newly published capture refs in a local variable; assign `outcome.CaptureRef` and expose it to target/cleanup evidence only after secure verification succeeds. Mirror the already-safe `Integrate` publication pattern.
- [ ] In `internal/integrationgate`, make terminalization resilient to evidence-verification failure after `INTEGRATING`: individually verify candidate refs, exclude failed refs, create a fresh bounded fallback decision artifact that records authority/risk/result digests, failure class, and rejected-ref identities/digest metadata, and emit `VALIDATION_UNAVAILABLE` with only verified refs.
- [ ] Do not reuse an unverified or missing `inputRef`, decision ref, Track B/C ref, review ref, or source-head ref in fallback transition evidence. The freshly written/verified fallback decision is the minimum durable evidence anchor.
- [ ] If evidence becomes unverifiable after `INTEGRATION_ACCEPTED` but before `READY_FOR_MERGE`, emit the allowed fail-closed `FAILED` transition using fresh verified failure evidence; never silently strand the run at `INTEGRATION_ACCEPTED`.
- [ ] Keep normal clean-path evidence complete. Do not weaken Track B/C provenance, review authority, source-head checks, cleanup guarantees, or existing classifications.
- [ ] Add adversarial regressions for: materialization publication that returns an oversized/mismatched ref; a bad ref reaching terminal `finish`; mutation immediately after `INTEGRATION_ACCEPTED`; and assertions that every emitted terminal evidence ref independently passes `ReadVerifiedLocal`.
- [ ] Assert no failure path under test ends durably at `INTEGRATING` or `INTEGRATION_ACCEPTED` unless the event ledger itself is the injected unavailable dependency.
- [ ] Run `gofmt`, focused tests, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`; commit only this bounded correction.

## Success criteria
- [ ] No unverified materialization ref escapes Track B lifecycle code.
- [ ] Evidence validation/resource-bound failures after `INTEGRATING` terminalize with a reduced, fully verified evidence set.
- [ ] Evidence loss between `INTEGRATION_ACCEPTED` and `READY_FOR_MERGE` cannot strand state.
- [ ] Exact-head deterministic acceptance remains green.

## Non-goals
No new review architecture, blocker registry, GitHub lifecycle, dashboard/API, deployment, production acceptance, or Phase 4+ work.

## Independent design-review closure — normative
The following requirements close the pre-implementation Claude design review and supersede any conflicting wording above.

- Track the **last durably emitted state** explicitly inside `Run`, beginning at `BRANCH_ACCEPTED`; update it only after `EventAppender.Append` succeeds. Every normal/fallback transition must use that exact state as `state_from`. Thread it into terminalization helpers rather than hardcoding `INTEGRATING`.
- Split transition handling so evidence verification failure is distinguishable from ledger-append failure. Evidence is verified exactly once immediately before append; a typed/classified verification failure may invoke one fallback attempt. An `Append` failure is never retried/re-emitted because append success is unknowable.
- Fallback is one-shot and non-recursive. A failure while creating/verifying/appending the fallback cannot trigger another fallback.
- Verify all candidate terminal refs **before** publishing the normal decision artifact. If verification fails, do not publish a normal decision artifact asserting the superseded state; publish only the fallback decision artifact.
- A pre-acceptance verification failure terminalizes from the tracked `INTEGRATING` state to `VALIDATION_UNAVAILABLE`. A post-acceptance verification failure terminalizes from tracked `INTEGRATION_ACCEPTED` to `FAILED`.
- The fresh fallback decision artifact records authority/risk/textual/combined digests where available, original intended state, original failure, evidence-verification failure, and bounded identities/digests of rejected refs. Only refs that independently verify may accompany it in the transition.
- Returned `Result.State` must equal the last successfully appended ledger state on every return. `FailureReason` must distinguish original cause, fallback classification, and infrastructure failure.
- The only allowed non-terminal stranding cases are: ledger append unavailable/ambiguous, or fresh fallback decision artifact write/verification unavailable. Never fabricate evidence-less state in either case.
- Fault-injection tests must be scoped by artifact name/kind so a deliberately bad materialization/normal artifact can coexist with a healthy fallback writer. Also test fallback-writer failure separately and assert no fabricated transition.
- Add a full ledger-continuity assertion: every event's `state_from` equals the previous durably emitted `state_to`; specifically test `INTEGRATING → VALIDATION_UNAVAILABLE` and `INTEGRATION_ACCEPTED → FAILED`.
- Do not auto-redrive `Run` after a terminalized fallback; no duplicate `BRANCH_ACCEPTED → ...` sequence may be appended by this correction.

### Design-review amendment 2 — fallback target by durable state
This mapping is exhaustive for evidence-verification failures once `Gate.Run` begins from its trusted `BRANCH_ACCEPTED` input:
- last durable `BRANCH_ACCEPTED` → fallback `FAILED`;
- last durable `INTEGRATION_PENDING` → fallback `FAILED`;
- last durable `INTEGRATING` → fallback `VALIDATION_UNAVAILABLE`;
- last durable `INTEGRATION_ACCEPTED` → fallback `FAILED`.
The fallback helper must validate the selected edge with `domain.ValidateTransition` before writing/appending it. Add continuity regressions for verification failure before the first integration transition and between `INTEGRATION_PENDING` and `INTEGRATING`, in addition to the two later-state cases above. The allowed-strand set remains only ledger append unavailable/ambiguous or fresh fallback artifact write/verification unavailable.

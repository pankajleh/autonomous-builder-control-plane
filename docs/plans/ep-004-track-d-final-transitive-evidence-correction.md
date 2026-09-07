# EP-004 Track D Final Transitive Evidence Correction

## Overview
Correct one controller-review Major on exact Track D head `44311f1c718960060cbcfdc50cb05716c24cfffa`. Keep scope strictly inside Phase 3 / EP-004 Track D.

## Review status
- ABCP independently accepted exact head `44311f1c718960060cbcfdc50cb05716c24cfffa` after retrying acceptance with the pinned absolute Go executable.
- Claude final exact-head review was attempted first but failed before any verdict because the provider session limit was exhausted; under the governed provider-failure rule, controller review is the fallback authority.
- Controller review confirms the prior Major classes remain fixed, but found one remaining post-acceptance evidence-integrity gap.

## Major being corrected
`INTEGRATION_ACCEPTED` is emitted only after all terminal refs verify, but the current `READY_FOR_MERGE` transition re-verifies only the gate input, integration-accepted decision, materialization cleanup, and final source-head evidence. A transitive Track B/Track C/review/acceptance evidence artifact can therefore be mutated after `INTEGRATION_ACCEPTED`; the immutable decision still contains the old ref/digest, yet the reduced READY transition never re-reads that referenced artifact.

This violates the normative post-acceptance invariant: any evidence that becomes unverifiable after `INTEGRATION_ACCEPTED` but before `READY_FOR_MERGE` must fail closed rather than allow readiness.

## Governed context
- Context capsule: `/home/devagent/abcp-runtime/ep004-track-d-final-transitive-evidence-correction/context.json`.
- Before any edit, run `abcp context-verify` against this repository and stop on mismatch.
- The exact capsule SHA256 is bound by the ABCP run authority.
## Pre-implementation design gate
- The last durable state entering the final readiness edge is `INTEGRATION_ACCEPTED`.
- Immediately before appending `INTEGRATION_ACCEPTED -> READY_FOR_MERGE`, the gate must re-verify the complete bounded terminal evidence closure already used to justify `INTEGRATION_ACCEPTED`, plus the ready-for-merge source-head proof.
- Do not rely on the integration-accepted decision artifact as a substitute for re-reading its referenced evidence bytes; the artifact binds metadata/digests but does not prove those referenced files remain locally verifiable at the later boundary.
- Reuse the existing `terminalRefs(accepted, inputRef)` closure so the final readiness edge covers Track B capture/cleanup, Track C/combined acceptance evidence, materialization evidence, prior source-head proofs, the integration-accepted decision, and the final source-head proof.
- Preserve existing cardinality/resource bounds. The legitimate closure is already bounded by accepted candidate/review/acceptance/integration limits; do not introduce a new magic low cap.
- If any member of that closure fails `ReadVerifiedLocal` at the READY boundary, use the existing typed verification-failure path and one-shot fallback: `INTEGRATION_ACCEPTED -> FAILED` with only verified reduced refs plus the fresh fallback decision.
- Ledger append ambiguity and fallback-artifact write/verification failure remain the only permitted non-terminal infrastructure stranding cases.

### Task 1: Re-verify full evidence closure at READY_FOR_MERGE
- [ ] Replace the reduced final READY evidence set with the complete `terminalRefs(accepted, inputRef)` closure after the ready-for-merge source proof has been appended to `accepted.SourceVerificationEvidence`.
- [ ] Keep `g.transition` as the immediate pre-append verifier so every final transition ref is independently verified at the readiness boundary.
- [ ] Add an adversarial regression that mutates a transitive combined/acceptance evidence artifact immediately after durable `INTEGRATION_ACCEPTED` while leaving the integration-accepted decision artifact itself untouched.
- [ ] Assert the run then ends durably at `FAILED`, never emits `READY_FOR_MERGE`, ledger continuity is preserved, rejected evidence is absent from terminal transition refs, and all emitted fallback refs independently verify.
- [ ] Keep the existing direct mutation test for the integration-accepted decision artifact.
- [ ] Do not alter Track B/C semantics, review-policy authority, source-head/replacement-ref checks, state mappings, scheduler contracts, or any Phase 4+ surface.
- [ ] Run `gofmt`, focused Track D tests, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`; commit only this bounded correction.

# Assurance / Proof-Obligation Governance Enforcement Plan

Status: **planning candidate — implementation forbidden until design/assurance review and `DESIGN_ACCEPTED`**

## Goal

Turn `ASSURANCE_MODEL_AND_PROOF_OBLIGATION_POLICY.md` from normative architecture into fail-closed controller authority before ABCP governs new external code-bearing builds.

The implementation must preserve the three-capsule A/B/C model. Assurance authority is part of A and is inherited through grants/checkpoints; it is not a fourth capsule and not reviewer-authored mutable context.

## Task 1 — immutable assurance contracts

Design strict-canonical records for the assurance policy/model, failure-dimension classifications, invariants, proof obligations, evidence requirements, and critical journeys. Define canonical digests and compatibility/version rules without retroactively reinterpreting existing V3 bytes.

Required proof: reordered/duplicated/unknown/ambiguous records fail deterministically; canonical equivalents hash identically.

## Task 2 — A gate and A-to-B authority

Add controller validation that every code-bearing A lineage has complete invariant → proof obligation → evidence traceability, reviewed applicability decisions, and named smoke/integration journeys before `DESIGN_ACCEPTED`.

A-to-B must bind exact assurance digests and mandatory floors. Missing or malformed assurance authority fails before Ralphex admission.

## Task 3 — B implementation traceability

Bind tasks/tests/evidence to frozen proof-obligation IDs. `IMPLEMENTATION_CONVERGED` must fail when a required evidence cell is missing, failed, unavailable, or satisfied only by a weaker class.

B must never modify the assurance model. A material newly discovered threat class returns `ASSURANCE_MODEL_GAP` to A.

## Task 4 — C acceptance and final review

Make C independently execute the frozen matrix against one unchanged exact candidate. Verify smoke/integration journeys through production composition boundaries required by A, then perform exact-head source review.

Final review blockers must classify as `IMPLEMENTATION_FINDING` or `ASSURANCE_MODEL_GAP`. The latter invalidates C and cannot directly authorize mutation.

## Task 5 — assurance escapes and learning evidence

Persist assurance escapes with missed failure dimension, affected proof obligations, discovery stage, exact candidate, and corrective A/policy action. Define the threshold that forces an A-level completeness re-review for repeated escapes in one lineage.

No learning record may silently change an active lineage's authority.

## Task 6 — dogfood acceptance

Run one non-trivial ABCP feature through A → B → C using the new contracts. Required evidence must include unit/static plus applicable adversarial classes, a controller-run smoke journey, an integration journey, exact-head acceptance, and source review.

Negative acceptance must prove missing evidence blocks convergence and a deliberately omitted material failure class returns to A rather than creating another B review/fix loop.

## Frozen non-goals

- no fourth capsule;
- no universal requirement to run every evidence class for every task;
- no reviewer ability to mint proof obligations during B/C;
- no retroactive mutation of historical V1/V2/V3 evidence;
- no replacement of deterministic acceptance with model review;
- no claim of external-build readiness until controller enforcement and dogfood acceptance are complete.

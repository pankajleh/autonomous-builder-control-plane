# Assurance / Proof-Obligation Governance Enforcement Plan

Status: **planning candidate — implementation forbidden until design/assurance review and `DESIGN_ACCEPTED`**

## Goal

Turn `ASSURANCE_MODEL_AND_PROOF_OBLIGATION_POLICY.md` from normative architecture into fail-closed controller authority before ABCP governs new external code-bearing builds.

The implementation must preserve the three-capsule A/B/C model. Assurance authority is part of A and is inherited through grants/checkpoints; it is not a fourth capsule and not reviewer-authored mutable context.

A-stage assurance authority for this implementation is defined in `docs/plans/assurance-proof-obligation-governance-enforcement-assurance.md`. No code-bearing task may begin until that artifact and this plan receive a fresh exact-head 0C/0M design/assurance review.

## Task 1 — immutable assurance contracts

Frozen obligations: `AG-PO-001` through `AG-PO-007`, `AG-PO-020` through `AG-PO-025`, `AG-PO-031` through `AG-PO-038`.

Design strict-canonical records for the assurance policy/model, failure-dimension/scenario classifications, invariants, proof obligations, evidence requirements with lifecycle-stage/subject bindings, critical journeys, code-bearing/documentation-only classification, and cumulative final-review correction-reentry ceiling. Every applicable scenario must map into invariant → proof obligation → evidence. Define canonical digests and compatibility/version rules without retroactively reinterpreting existing V3 bytes.

Also design the V4 successor activation/policy-manifest authority and PostgreSQL workflow-authority backend exactly as frozen by the A assurance model: existing controller activation operation, exact predecessor activation, exact repository commit, controller-recomputed canonical policy-file digests/schema, expected-revision CAS, and the exact controller-derived eligible predecessor-V3 lineage set. It must reject unactivated versions, caller-selected grandfather scope, and raced issuance.

Required proof: reordered/duplicated/unknown/ambiguous records fail deterministically; canonical equivalents hash identically; unactivated policy/schema digests fail; a competing or over-broad grandfather set fails.

## Task 2 — A gate and A-to-B authority

Frozen obligations: `AG-PO-006`, `AG-PO-008` through `AG-PO-013`, `AG-PO-023`, `AG-PO-024`, `AG-PO-027`, `AG-PO-031`, `AG-PO-034`, `AG-PO-038`.

Default every lineage to code-bearing. Permit `documentation_only` only by controller-owned A classification for non-authoritative documentation paths, and revalidate the exact B/C diff before honoring the exemption.

Add controller validation that every applicable failure dimension/scenario maps through invariant → proof obligation → evidence requirement, with reviewed N/A decisions, lifecycle-stage/subject bindings, and named smoke/integration production boundaries before `DESIGN_ACCEPTED`.

A-to-B must bind exact activated assurance digests, mandatory floors, and cumulative correction-reentry ceiling. Missing or malformed assurance authority fails before Ralphex admission.

## Task 3 — B implementation traceability

Frozen obligations: `AG-PO-013`, `AG-PO-014`, `AG-PO-020` through `AG-PO-025`, `AG-PO-027`, `AG-PO-028`, `AG-PO-032`, `AG-PO-033`, `AG-PO-034`, `AG-PO-036`.

Bind tasks/tests/evidence to frozen proof-obligation IDs and lifecycle stages. `IMPLEMENTATION_CONVERGED` must fail when a B-stage required evidence cell is missing, unavailable, executed against the wrong subject, or satisfied only by a weaker class; a deterministic test that disproves a valid obligation is an implementation failure.

B must never modify the assurance model. Implement broad `ASSURANCE_MODEL_GAP` precedence for misclassification/N/A, inadequate obligations, wrong evidence/validator/stage/binding, missing production boundary/journey, or broken traceability.

## Task 4 — C acceptance and final review

Frozen obligations: `AG-PO-010` through `AG-PO-019`, `AG-PO-023` through `AG-PO-029`, `AG-PO-031` through `AG-PO-037`.

Make C independently execute only the frozen `C_BRANCH_ACCEPTANCE` cells against one unchanged exact candidate and verify required B evidence. Later integration/post-merge/production controllers execute only their assigned cells against their exact result/deployment subjects.

Final review blockers must classify as `IMPLEMENTATION_FINDING` or broad-precedence `ASSURANCE_MODEL_GAP`. The latter invalidates C and returns to A. For an implementation finding, persist an exact failed-C record and implement a controller-derived correction-B reentry bound to frozen A authority, exact finding IDs/paths, prior failed candidate/evidence, and the non-resetting cumulative reentry ceiling. C/reviewer prose never mints mutation authority.

## Task 5 — assurance escapes and learning evidence

Frozen obligations: `AG-PO-013`, `AG-PO-026`.

Persist assurance escapes with missed failure dimension/scenario, affected invariants/proof obligations, discovery stage, exact candidate, and corrective A/policy action. Every Critical/Major assurance escape forces an A-level assurance-completeness re-review; no numeric threshold or reviewer discretion bypasses that return.

No learning record may silently change an active lineage's authority.

## Task 6 — dogfood acceptance

Frozen obligations: `AG-PO-023` through `AG-PO-038` plus cumulative verification of every prior obligation.\n
Run one non-trivial ABCP feature through A → B → C → integration and the applicable post-merge stage using the new contracts, with integration PASS required before merge authorization. Required evidence must include unit/static plus applicable adversarial classes, a controller-run smoke journey, an integration journey, exact-head branch acceptance/review, and correctly staged later evidence where applicable.

Negative acceptance must prove: missing evidence blocks the owning stage; wrong-stage/wrong-subject evidence is rejected; a deliberately omitted/misclassified material failure model returns to A; a C implementation finding can create only controller-derived correction-B authority; exactly two correction-B grant publications may consume the lineage counter, exact replay consumes none, and a third post-restart/post-C attempt is denied and returns to A; documentation-only misclassification is rejected; and no candidate/process/restart can reset lineage ceilings.

## Frozen non-goals

- no fourth capsule;
- no universal requirement to run every evidence class for every task;
- no reviewer ability to mint proof obligations during B/C;
- no retroactive mutation of historical V1/V2/V3 evidence;
- no replacement of deterministic acceptance with model review;
- no claim of external-build readiness until controller enforcement and dogfood acceptance are complete.

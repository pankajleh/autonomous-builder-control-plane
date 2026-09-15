# Assurance Model and Proof Obligation Policy

## 1. Purpose and scope

ABCP must not use final implementation review as the primary mechanism for discovering foreseeable classes of failure. Every code-bearing software build governed by ABCP must define what has to remain true, how the system can fail, and what evidence proves those claims before implementation authority is granted.

This policy is platform-level. It applies to ABCP itself and to every repository later built under ABCP governance. It strengthens the existing A/B/C workflow; it does not create a fourth capsule.

Documentation-only work may declare the `documentation_only` assurance profile. Any change that can alter executable behavior, generated runtime configuration, schema/migration behavior, deployment behavior, security posture, or operational state is code-bearing and cannot use that profile.

## 2. Governing rule

The A capsule owns both **functional design** and **assurance design**. `DESIGN_ACCEPTED` is forbidden until the controller has accepted one immutable assurance model bound to the same design lineage.

The assurance model must contain:

1. named invariants;
2. an explicit failure/threat model;
3. numbered, falsifiable proof obligations;
4. required evidence classes for each proof obligation;
5. critical smoke/integration journeys and component boundaries;
6. a traceability matrix from invariant → proof obligation → evidence class → expected artifact;
7. explicit `not_applicable` decisions with rationale for evidence classes that are not required.

A reviewer may challenge completeness, but may not silently enlarge implementation authority. A newly discovered material failure class is an assurance-model gap and returns to A.

## 3. Minimum failure-model dimensions

Every A design must classify each dimension as `applicable` or `not_applicable` with rationale:

- crash consistency / process termination at durable boundaries;
- concurrency, races, locking, and competing valid actors;
- retry, replay, duplicate delivery, and idempotency;
- authority/identity replacement, reissuance, expiry, and stale authority;
- partial failure between ordered side effects;
- cancellation, timeout, and ambiguous completion;
- physical resource exhaustion and boundedness, not only logical counters;
- restart, recovery, rollback, and fail-closed behavior;
- malformed, forged, oversized, or adversarial inputs;
- filesystem/path/link/object identity where local state is authoritative;
- external dependency delay, duplication, inconsistency, or unavailability;
- compatibility, migration, version skew, and old/new state where applicable;
- tenant/security/privacy boundaries where applicable;
- integration composition and critical end-to-end user/controller journeys.

Risk-specific designs may add dimensions. They may not remove this classification requirement.

## 4. Proof obligations

Each proof obligation has a stable ID such as `PO-001`. It must state one falsifiable claim and identify the invariant(s) it protects.

Examples:

- `PO-017`: a committed authority generation cannot be reissued after namespace replacement.
- `PO-018`: termination at every publication boundary converges to the same prepared generation, provable rollback, or fail-closed state without corrupting prior authority.
- `PO-019`: repeated rejected opens cannot exceed the declared physical descriptor ceiling.

Proof obligations are frozen by A. B may implement and gather evidence against them but cannot weaken, delete, reinterpret, or mark them `not_applicable`. A missing material obligation discovered during B or C is `ASSURANCE_MODEL_GAP` and requires new A authority.

## 5. Evidence classes and traceability

The supported evidence vocabulary includes at minimum:

- `unit` — local semantics and boundary conditions;
- `static` — compile/type/lint/vet/static analysis as applicable;
- `contract` — serialization/API/protocol/schema compatibility;
- `fault_injection` — failures between internal operations;
- `race_concurrency` — competing operations under race/concurrency instrumentation;
- `crash_restart` — real process termination and fresh-process recovery at named boundaries;
- `resource` — physical descriptor/process/memory/slot/fan-out ceilings;
- `replay_idempotency` — duplicate/retry/restart semantics;
- `security_negative` — forged identity/path/authority/input attacks;
- `smoke` — the assembled artifact starts and performs its critical minimal journey;
- `integration` — real production composition boundaries cooperate under the frozen contract;
- `migration` — forward/backward/rollback behavior where state or schema changes;
- `e2e` — critical externally observable journey across the intended system boundary;
- `runtime` / `production` — deployed-runtime checks where the execution pack requires them.

Every proof obligation must map to at least one evidence class. Every required matrix cell must bind an expected deterministic validator or a precisely defined evidence artifact. A required cell cannot be blank, waived by reviewer prose, or substituted with a weaker class.

Smoke and integration evidence are mandatory for code-bearing work unless A explicitly proves them not applicable. Unit tests do not substitute for integration evidence; mocks do not prove the production boundary whose contract is under test. External providers may use contract-faithful fakes only when the frozen assurance model says what the fake proves and what remains for runtime/production acceptance.

## 6. A/B/C gate semantics

### A — design and assurance acceptance

Before `DESIGN_ACCEPTED`:

- functional design is complete;
- the assurance model and traceability matrix are complete;
- every invariant maps to one or more proof obligations;
- every proof obligation maps to required evidence classes;
- critical smoke/integration journeys are named;
- design/threat/assurance review has zero unresolved Critical or Major findings.

The A-to-B grant binds the assurance-policy digest, assurance-model digest, proof-obligation set digest, required-evidence matrix digest, and required final-review profile.

### B — implementation convergence

B tasks must reference the proof obligations they implement or validate. New tests must state which obligations they cover.

`IMPLEMENTATION_CONVERGED` requires all B-required deterministic evidence to pass for the exact candidate, including every required smoke/integration/adversarial class. `VALIDATION_UNAVAILABLE` is blocking. A passing narrower class cannot replace a required broader class.

A reviewer finding that violates an existing proof obligation is an implementation finding. A finding that exposes a material missing failure class or missing proof obligation is `ASSURANCE_MODEL_GAP` and returns to A instead of entering an unbounded B patch loop.

### C — acceptance, final review, publication, merge

C reruns the frozen acceptance matrix independently against one unchanged exact candidate. It verifies evidence-to-obligation traceability before exact-head source review.

Final review is a verification gate, not the primary discovery engine. It must classify every blocking issue as either:

- `IMPLEMENTATION_FINDING`: violation of frozen design/invariant/proof obligation; or
- `ASSURANCE_MODEL_GAP`: the frozen assurance model omitted a material failure class or proof obligation.

An assurance-model gap invalidates C and returns to A. Repository mutation remains forbidden in C.

## 7. Assurance escapes and learning loop

A Critical/Major issue first discovered in final review that was reasonably foreseeable from the minimum failure-model dimensions is an **assurance escape**. The event/evidence record must capture the missed dimension, affected proof obligations, discovery stage, and corrective policy/design action.

An assurance escape is not permission to widen B automatically. The controller first classifies whether the frozen model already covered the issue. If not, it returns to A.

Repeated assurance escapes in the same lineage require an explicit A-level assurance-completeness re-review before further implementation authority. ABCP should use these records to improve future execution-pack templates and deterministic validators rather than merely increasing reviewer effort.

## 8. Execution-pack requirements

Every new code-bearing execution pack governed by ABCP must contain or reference:

- assurance profile and policy version/digest;
- invariant registry;
- failure-model classification;
- numbered proof-obligation registry;
- evidence-class traceability matrix;
- smoke/integration journey matrix;
- deterministic acceptance commands/validators or the authority that derives them;
- final-review scope and assurance-gap classification rule.

Plans may narrow implementation scope but cannot weaken the parent execution pack's assurance floor.

## 9. Controller enforcement and activation

This document is normative architecture immediately after merge, but it must not pretend that the current V3 wire/schema already enforces fields it does not encode.

Before ABCP is declared ready to govern new external software builds under this policy, controller/schema implementation must fail closed on missing assurance authority. The next compatible governance activation must bind the assurance policy/model/proof/evidence digests into durable capsule/checkpoint/grant authority and validate them on fresh sessions.

Until that enforcement lands, new ABCP development execution packs and A design reviews must apply this policy explicitly in repository evidence. Existing in-flight lineages are not retroactively rewritten; any new A lineage created after policy activation must comply.

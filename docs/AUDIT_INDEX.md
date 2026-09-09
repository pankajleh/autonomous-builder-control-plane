# Audit Index

Operational role: this file indexes accepted, reviewed, and merged evidence identities. It must be consulted before planning or starting governed work and reconciled whenever one of those lifecycle boundaries is reached. It is a human-readable projection and pointer set, not a substitute for controller ledgers, immutable evidence artifacts, verified capsules/authorities, or Git object/ref evidence.

## Accepted, reviewed, and merged implementation identities

| Scope | PR | Accepted/reviewed head | Merge SHA | Review evidence |
|---|---:|---|---|---|
| EP-002 | #4 | `0668397491394964d06ddf7ad00ae8032b2ac49d` | `f7ca8e6e59f04e7bece0582f3febcd37ec2cb4f7` | Claude cross-model clean after correction |
| EP-003 | #5 | `f345a910ff14c15be3fdc872eab89c13c5b89caa` | `db56b1f8cf32561be6b707db4bbf046f4c24e067` | Controller fallback after Claude capacity failure; prior Claude Majors corrected |
| EP-004 | #6 | `d3193cf5615c5ea33e2f74398519106863ed4b06` | `94e14ca749d31ac214e979aab03fbde37502dd7f` | Controller fallback after Claude session failure; prior Claude/controller Majors corrected |
| EP-005 exact-head PR lifecycle | #8 | `6db075240ce87b751f8db98abb540c96410c515b` | `ccf75d093625119cc39944fe7a47c3a03b30ad3b` | Controller fallback 0C/0M artifact SHA-256 `e34a632e290930bbfcccfbd4326d5aeab1b1d8012285d67a122a87b55d8196c5` |
| Context-bound autonomous operations | #11 | `84c6a8ee4b6d6a315eeaa1e7de17fbf5f94a1dec` | `454ea4dce3c674e0d8e55273319cfb4ea4a077a5` | Exact-head 0C/0M artifact SHA-256 `85405bc64c0703319cad881d4b4aa3f07ba7aa91fb97ef16b8172dcb56beebe5` |

EP-005 foundation merged in PR #7 at `bf5f923f1743b541fac8ad75fa173557fe68ba0f` before the exact-head PR lifecycle subtrack.

## Context-bound operation policy evidence

- Final policy/correction candidate: `84c6a8ee4b6d6a315eeaa1e7de17fbf5f94a1dec`.
- Controller result: `BRANCH_ACCEPTED`, with final-Git evidence SHA-256 `1de3bb8bf42f8a9e231d0d9e4ef0b699c8ac3534fd66778c03b316b28a48ee5c` proving the clean exact candidate branch/head.
- Final review: `CLEAN_CRITICAL_MAJOR`, 0 Critical and 0 Major, artifact SHA-256 `85405bc64c0703319cad881d4b4aa3f07ba7aa91fb97ef16b8172dcb56beebe5`.
- Merge: PR #11 at `454ea4dce3c674e0d8e55273319cfb4ea4a077a5`.

These identities establish the current universal operation rule: each new governed operation requires a fresh verified `context-capsule-v2` and immutable authority bound to its exact base and sources.

## EP-005 active evidence

The PR-lifecycle subtrack ultimately reached controller `BRANCH_ACCEPTED` at `6db075240ce87b751f8db98abb540c96410c515b`. Its final policy-authorized controller fallback review returned `CLEAN_CRITICAL_MAJOR` with 0 Critical and 0 Major; artifact SHA-256 `e34a632e290930bbfcccfbd4326d5aeab1b1d8012285d67a122a87b55d8196c5`. PR #8 merged that exact reviewed head at `ccf75d093625119cc39944fe7a47c3a03b30ad3b`.

The initial CI evidence-ingestion design SHA-256 `d90099eeab3af74ea5dd25f50b7e87f920f7cb47eb523e4e7112882dc0524276` was rejected after review found 1 Critical and 10 Major findings; review artifact SHA-256 `659174158aecc5a690663f79e8a2b567a459b77af0cf08042d37cdfb6db265f3`. The corrected, read-only design is frozen at SHA-256 `47a6d7b1d4c93a853a26e4da3753894dedacfce930345c610cf24a4236b2b409`; final bounded Codex review returned 0 Critical and 0 Major, artifact SHA-256 `aa5d86471ffcfe1d68eff9c49ef5b2df718355c0b4c825716d35d68217b3fa45`. The frozen design entered Git at `3ee547796a5bc240f62d4ce2247c826921c87cd5`.

### Non-terminal implementation checkpoints

| CI task | Commit | Lifecycle status |
|---|---|---|
| Task 1 — v1 evidence contract and bounds | `e1740f4be8df06571a3299c3fb31ecc31b0a1aea` | Implemented; no controller acceptance, exact-head implementation review, or merge identity recorded |
| Task 2 — bounded GitHub reads and stabilization | `da8ffea4582539067724b363b3144d9601dee086` | Implemented; no controller acceptance, exact-head implementation review, or merge identity recorded |
| Task 3 — immutable evidence, ledger, replay | none | Not started; prior capsule cannot cross from `e1740f4be8df06571a3299c3fb31ecc31b0a1aea` to Task 2 HEAD `da8ffea4582539067724b363b3144d9601dee086` |

These checkpoints must not be promoted into the accepted/reviewed/merged table until the corresponding immutable controller and review evidence exists. EP-005 remains active; CI evidence ingestion, merge approval/expected-head protection, and serial post-merge acceptance are not merged.

## Automatic operation handoff design status

The separate automatic-handoff design operation is based on `454ea4dce3c674e0d8e55273319cfb4ea4a077a5` and uses a design-planning v2 capsule. Its architecture and execution-plan drafts are currently uncommitted worktree outputs. There is no frozen Git identity, accepted design-gate artifact, clean exact-artifact review, implementation acceptance, or merge evidence to index yet.

Do not treat those drafts as EP-005 authority or evidence. Once the design is frozen and gated, record its exact artifact and review identities here; only later accepted/reviewed/merged implementation heads belong in the implementation table above.

## Reconciliation rule

At operation planning/start, verify identities here against controller/Git evidence and cross-check the present authority in `CURRENT_STATE.md` and the next action in `PROGRESS.md`. At each accepted, reviewed, or merged boundary, update all three projections. A contradiction or missing identity is a planning blocker until reconciled from immutable evidence.

# Implementation Progress

Updated: 2026-09-09

Operational role: this file projects roadmap and subtrack progress plus the next authorized action. It must be consulted before planning or starting an operation. It is not completion authority; immutable controller and Git evidence remain authoritative.

## Roadmap

| Phase | Status | Evidence and current boundary |
|---|---|---|
| Phase 0 — Foundation | COMPLETE | EP-001 merged |
| Phase 1 — Governed single-plan execution | COMPLETE | PR #4 merged |
| Phase 2 — Recovery and blocker control | COMPLETE | PR #5 merged |
| Phase 3 — Cross-plan scheduler and integration | COMPLETE | PR #6 merged at `94e14ca749d31ac214e979aab03fbde37502dd7f` |
| Phase 4 — GitHub lifecycle | IN PROGRESS | Foundation PR #7 and exact-head PR lifecycle PR #8 merged; CI evidence ingestion Tasks 1–2 are implemented, and the accepted Task 2/current-policy correction is materialized here before the Task 3 operation |
| Phase 5 — Service/API/dashboard | NOT STARTED | Roadmap only |
| Phase 6 — Production hardening | NOT STARTED | Roadmap only |

## Phase 4 subtracks

| Subtrack | Status | Exact checkpoint | Next action |
|---|---|---|---|
| GitHub lifecycle foundation | MERGED | PR #7 at `bf5f923f1743b541fac8ad75fa173557fe68ba0f` | Preserve frozen behavior |
| Exact-head PR lifecycle | MERGED | Accepted/reviewed head `6db075240ce87b751f8db98abb540c96410c515b`; PR #8 merge `ccf75d093625119cc39944fe7a47c3a03b30ad3b` | Preserve frozen behavior |
| CI evidence-ingestion design | ACCEPTED | Design SHA-256 `47a6d7b1d4c93a853a26e4da3753894dedacfce930345c610cf24a4236b2b409`; 0C/0M review `aa5d86471ffcfe1d68eff9c49ef5b2df718355c0b4c825716d35d68217b3fa45` | Preserve neutral read-only collection semantics |
| CI Task 1 — contract and bounds | IMPLEMENTED | `e1740f4be8df06571a3299c3fb31ecc31b0a1aea` | Preserve byte-for-byte |
| CI Task 2 — bounded reads and stabilization | ACCEPTED; CURRENT-POLICY RECONCILED AND CORRECTED | Accepted implementation `da8ffea4582539067724b363b3144d9601dee086`; accepted clean correction head `1e3e84bb30a60ed3e47c2ca765c1ef2be8042b56` passed all 14 gates; final-Git SHA-256 `3e424d9fc1d18682b8c410a0cb5288988cddb3ef3552f153a879b5f5ade1300c`; events ledger SHA-256 `51764b8e0a038b42ab2cd99a85822015de6ac643bf9ca8f3f2d9860c6a8b241a` | Preserve both accepted boundaries byte-for-byte |
| CI Task 3 — immutable evidence, ledger, replay | SOLE NEXT EXECUTABLE OPERATION; FRESH AUTHORITY REQUIRED | No Task 3 implementation commit; its predecessor will be this projection candidate's resulting accepted exact head, resolved from Git/controller evidence after commit | Issue a Task-3-only v2 capsule and immutable authority at that exact accepted SHA, then execute Task 3 only |
| CI Task 4 — acceptance and review handoff | DEFERRED, NOT EXECUTABLE | No implementation checkpoint | Establish a separate fresh operation only after Task 3 completes |
| Merge approval and expected-head protection | NOT STARTED | Roadmap only | Wait for accepted/reviewed/merged CI subtrack |
| Serial post-merge acceptance | NOT STARTED | Roadmap only | Follow merge protection in roadmap order |

The Task 2 acceptance proves the exact clean `da8ffea…` implementation passed all 10 deterministic gates; it does not itself authorize Task 3. The later exact clean correction head `1e3e84bb30a60ed3e47c2ca765c1ef2be8042b56` reached `BRANCH_ACCEPTED` after all 14 correction gates passed, with final-Git evidence SHA-256 `3e424d9fc1d18682b8c410a0cb5288988cddb3ef3552f153a879b5f5ade1300c` and events ledger SHA-256 `51764b8e0a038b42ab2cd99a85822015de6ac643bf9ca8f3f2d9860c6a8b241a`. At that head, current-policy-owned `internal/run`, `internal/context`, `internal/authority`, and `cmd/abcp` match merged main `e11afb7d7356a0df36566d98c34adbd07a0097ae`, and accepted Task 2 `internal/cilifecycle` matches `da8ffea4582539067724b363b3144d9601dee086` byte-for-byte.

This projection materializes the accepted correction boundary. Its resulting accepted exact head must be resolved from Git/controller evidence after commit and becomes the Task 3 predecessor; these frozen bytes do not self-record that later acceptance. Task 3 must begin as a distinct, freshly authorized operation at that exact accepted head.

## Cross-cutting operational governance

| Track | Status | Exact checkpoint | Next action |
|---|---|---|---|
| Context-bound autonomous operations | MERGED | Candidate `84c6a8ee4b6d6a315eeaa1e7de17fbf5f94a1dec` accepted/reviewed 0C/0M; PR #11 merge `454ea4dce3c674e0d8e55273319cfb4ea4a077a5` | Enforce v2 capsule/authority boundaries |
| Operational state projections | MERGED | Candidate `2ab636f2b118e7e77bcb6656a0afb163f2082461` accepted/reviewed 0C/0M; PR #12 merge `e11afb7d7356a0df36566d98c34adbd07a0097ae` | Apply the immutable-evidence cutoff rule |
| Automatic operation handoff | SEPARATE DESIGN TRACK | No authority applicable to CI Task 3 | Do not substitute it for fresh Task 3 authority |

## Next authorized actions

1. After this projection candidate commits and reaches controller acceptance, resolve its resulting accepted exact head from Git/controller evidence. Do not write that later lifecycle event into these frozen candidate bytes.
2. Build and independently verify a fresh `context-capsule-v2` for Task 3 only, then bind immutable run authority to that exact accepted base and the restructured plan.
3. Execute only Task 3: immutable evidence, ledger outcome, and replay.

Task 4 remains deferred and non-executable until Task 3 completes. It requires a separate fresh operation for final scope audit, deterministic acceptance, and exact-head review; neither task may implement merge policy, merge execution, or post-merge acceptance.

## Projection materialization rule

This candidate's immutable-evidence cutoff is the accepted Task 2/current-policy correction at exact clean head `1e3e84bb30a60ed3e47c2ca765c1ef2be8042b56`, with final-Git evidence SHA-256 `3e424d9fc1d18682b8c410a0cb5288988cddb3ef3552f153a879b5f5ade1300c` and events ledger SHA-256 `51764b8e0a038b42ab2cd99a85822015de6ac643bf9ca8f3f2d9860c6a8b241a`. It materializes Task 2 acceptance at `da8ffea4582539067724b363b3144d9601dee086` and accepted/reviewed/merged operational projections through PR #12 merge `e11afb7d7356a0df36566d98c34adbd07a0097ae`. Current-policy-owned files match that merge, and accepted Task 2 bytes remain identical to `da8ffea4582539067724b363b3144d9601dee086`.

Acceptance, review, and merge evidence is authoritative immediately in immutable evidence. Events after these bytes freeze, including this candidate's exact commit identity and controller acceptance, belong in the next separately governed projection reconciliation. The resulting accepted exact head, resolved from Git/controller evidence after commit, is the Task 3 predecessor. Before that non-reconciliation operation begins, all three projections must be current through this boundary.

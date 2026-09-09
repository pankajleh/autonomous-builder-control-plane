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
| Phase 4 — GitHub lifecycle | IN PROGRESS; CI TASK 3 REVIEW-BLOCKED | Foundation PR #7 and exact-head PR lifecycle PR #8 merged; CI Task 3 is deterministically accepted at `b5c7e2c…` but its exact-head review found 0 Critical / 5 Major |
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
| CI Task 3 — immutable evidence, ledger, replay | ACCEPTED; REVIEW-BLOCKED (0C/5M) | Exact clean technical head `b5c7e2cd0ea3cc223f481b1d73a78c6276846639`; 16/16 deterministic gates passed; final-Git SHA-256 `24f3ceebf733c596c9638f9d9693b2fa49375d3721222a45dc4cc565e00cd1b7`; exact-head review `IMPLEMENTATION_FINDINGS`, artifact SHA-256 `d0a3f692f19d9dc5b42a13abf8c71bbd18469a57d8920183ca86345953ccf6f3` | Correct M-001 through M-005 in a fresh correction-only operation, re-run deterministic acceptance, and obtain a fresh exact-head 0C/0M review |
| CI Task 4 — acceptance and review handoff | BLOCKED, NOT EXECUTABLE | No eligible Task 4 checkpoint; Task 3 exact-head review has 5 Major findings | Remain ineligible until the corrected Task 3 exact head passes deterministic acceptance and a fresh exact-head review reaches 0C/0M |
| Merge approval and expected-head protection | NOT STARTED | Roadmap only | Wait for accepted/reviewed/merged CI subtrack |
| Serial post-merge acceptance | NOT STARTED | Roadmap only | Follow merge protection in roadmap order |

The Task 2 acceptance proves the exact clean `da8ffea…` implementation passed all 10 deterministic gates; it does not itself authorize Task 3. The later exact clean correction head `1e3e84bb30a60ed3e47c2ca765c1ef2be8042b56` reached `BRANCH_ACCEPTED` after all 14 correction gates passed, with final-Git evidence SHA-256 `3e424d9fc1d18682b8c410a0cb5288988cddb3ef3552f153a879b5f5ade1300c` and events ledger SHA-256 `51764b8e0a038b42ab2cd99a85822015de6ac643bf9ca8f3f2d9860c6a8b241a`. At that head, current-policy-owned `internal/run`, `internal/context`, `internal/authority`, and `cmd/abcp` match merged main `e11afb7d7356a0df36566d98c34adbd07a0097ae`, and accepted Task 2 `internal/cilifecycle` matches `da8ffea4582539067724b363b3144d9601dee086` byte-for-byte.

Task 3 reached `BRANCH_ACCEPTED` at exact clean technical head `b5c7e2cd0ea3cc223f481b1d73a78c6276846639` after all 16 deterministic gates passed. Its final-Git evidence SHA-256 is `24f3ceebf733c596c9638f9d9693b2fa49375d3721222a45dc4cc565e00cd1b7`. Its fresh exact-head review returned exactly `IMPLEMENTATION_FINDINGS`, 0 Critical and 5 Major, with artifact SHA-256 `d0a3f692f19d9dc5b42a13abf8c71bbd18469a57d8920183ca86345953ccf6f3`. Deterministic acceptance and review are distinct gates: the former does not override the latter.

The current blocking correction set is M-001 pre-existing material becoming recoverable through a newly left reservation; M-002 incomplete hard-link cleanup/durability and masked directory-fsync failure; M-003 non-durable, unbounded, unserialized ledger append/recovery; M-004 retry acceptance of a reservation that never crossed a successful sync boundary; and M-005 unbounded attempt-directory allocation plus leaked keyed process locks. Task 4 and publication remain ineligible until a corrected exact head passes deterministic acceptance and a fresh exact-head implementation review reaches 0 Critical and 0 Major.

## Cross-cutting operational governance

| Track | Status | Exact checkpoint | Next action |
|---|---|---|---|
| Context-bound autonomous operations | MERGED | Candidate `84c6a8ee4b6d6a315eeaa1e7de17fbf5f94a1dec` accepted/reviewed 0C/0M; PR #11 merge `454ea4dce3c674e0d8e55273319cfb4ea4a077a5` | Enforce v2 capsule/authority boundaries |
| Operational state projections | MERGED | Candidate `2ab636f2b118e7e77bcb6656a0afb163f2082461` accepted/reviewed 0C/0M; PR #12 merge `e11afb7d7356a0df36566d98c34adbd07a0097ae` | Apply the immutable-evidence cutoff rule |
| Automatic operation handoff | SEPARATE DESIGN TRACK | No authority applicable to the Task 3 correction | Do not substitute it for fresh correction-only authority |

## Next authorized actions

1. After this materialization candidate commits and reaches controller acceptance, resolve its resulting accepted exact head from Git/controller evidence. Do not write that later lifecycle event into these frozen candidate bytes.
2. Freeze a correction-only plan for M-001 through M-005, build and independently verify a fresh `context-capsule-v2`, and bind immutable run authority to the exact accepted materialization base and correction sources.
3. Execute only the Task 3 correction set, then require fresh deterministic acceptance and an exact-head implementation review.

Task 4 and publication are blocked and non-executable until the corrected Task 3 exact head passes deterministic acceptance and a fresh exact-head review reaches 0 Critical and 0 Major. Neither the correction nor Task 4 may implement merge policy, merge execution, or post-merge acceptance.

## Projection materialization rule

This candidate's immutable-evidence cutoff is Task 3 deterministic acceptance and exact-head review at exact clean technical head `b5c7e2cd0ea3cc223f481b1d73a78c6276846639`. The final-Git evidence SHA-256 is `24f3ceebf733c596c9638f9d9693b2fa49375d3721222a45dc4cc565e00cd1b7`; the review artifact SHA-256 is `d0a3f692f19d9dc5b42a13abf8c71bbd18469a57d8920183ca86345953ccf6f3`, with exact verdict `IMPLEMENTATION_FINDINGS`, 0 Critical and 5 Major. It also preserves the earlier Task 2/current-policy and accepted/reviewed/merged PR #12 boundaries.

Acceptance, review, and merge evidence is authoritative immediately in immutable evidence. Events after these bytes freeze, including this candidate's exact commit identity and any controller acceptance or review, belong in the next separately governed projection reconciliation. Resolve this materialization's exact accepted head from Git/controller evidence after commit before authorizing the correction. Before that non-reconciliation operation begins, all three projections must be current through this boundary.

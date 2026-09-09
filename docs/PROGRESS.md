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
| Phase 4 — GitHub lifecycle | IN PROGRESS; CI TASK 3 MERGED/COMPLETE | Foundation PR #7, exact-head PR lifecycle PR #8, and accepted/reviewed CI Task 3 PR #13 merged; Deferred Task 4 is the sole next eligible CI operation |
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
| CI Task 3 — immutable evidence, ledger, replay | ACCEPTED/REVIEWED/MERGED; COMPLETE | Final exact clean accepted/reviewed head `3e0f295e8f98c348d684b2c026bacd9ceeeea911`; all 17 acceptance entries recorded successful; final-Git SHA-256 `d809846a7f6133adca513e46b531101dac428c8c0707035dd1ec298e4634726b`; controller ledger SHA-256 `9f7d41498c984e8dfce7f762fc9d616e50ccc2b70f50b24b322339e751c51870`; exact-head 0C/0M review artifact SHA-256 `94cd9d4cf4173a2ecf59743aa7892c8b6e091d4eaf70b67cfcc8d18d7c5caebb`; PR #13 merge `bf2482f756a7c5f75906825b8f3e6e454c72f94f` | Preserve accepted/reviewed/merged behavior and evidence |
| Deferred CI Task 4 — acceptance, scope audit, and exact-head review handoff | NEXT ELIGIBLE; NOT STARTED | Task 3 predecessor is accepted/reviewed/merged; no Task 4 checkpoint exists yet | Freeze a Task-4-only plan, build and verify a fresh v2 capsule, bind immutable authority to the exact accepted reconciliation predecessor, then execute only Deferred Task 4 |
| Merge approval and expected-head protection | NOT STARTED | Roadmap only | Remain out of scope until Deferred Task 4 completes and later work is separately authorized |
| Serial post-merge acceptance | NOT STARTED | Roadmap only | Remain out of scope until its roadmap predecessor is separately authorized and complete |

The Task 2 acceptance proves the exact clean `da8ffea…` implementation passed all 10 deterministic gates; it does not itself authorize Task 3. The later exact clean correction head `1e3e84bb30a60ed3e47c2ca765c1ef2be8042b56` reached `BRANCH_ACCEPTED` after all 14 correction gates passed, with final-Git evidence SHA-256 `3e424d9fc1d18682b8c410a0cb5288988cddb3ef3552f153a879b5f5ade1300c` and events ledger SHA-256 `51764b8e0a038b42ab2cd99a85822015de6ac643bf9ca8f3f2d9860c6a8b241a`. At that head, current-policy-owned `internal/run`, `internal/context`, `internal/authority`, and `cmd/abcp` match merged main `e11afb7d7356a0df36566d98c34adbd07a0097ae`, and accepted Task 2 `internal/cilifecycle` matches `da8ffea4582539067724b363b3144d9601dee086` byte-for-byte.

Task 3 first reached `BRANCH_ACCEPTED` at exact clean technical head `b5c7e2cd0ea3cc223f481b1d73a78c6276846639` after all 16 deterministic gates passed. Its final-Git evidence SHA-256 is `24f3ceebf733c596c9638f9d9693b2fa49375d3721222a45dc4cc565e00cd1b7`. Its exact-head review returned exactly `IMPLEMENTATION_FINDINGS`, 0 Critical and 5 Major, with artifact SHA-256 `d0a3f692f19d9dc5b42a13abf8c71bbd18469a57d8920183ca86345953ccf6f3`.

The first correction reached `BRANCH_ACCEPTED` at exact clean head `39db34bd4f04dd6b85b9e5444eee86a2f1ea1b05` after all 16 required gates passed. Its final-Git evidence SHA-256 is `14e6500bce762de792cefd2c509188e83dde799d438dd053cb306659d63214ef`, and its correction-run ledger SHA-256 is `84cc4756ea8cf6f27d498977a6211da32c32ae68f3a72e5c0b6d534ff193b027`. Its fresh exact-head review returned exactly `IMPLEMENTATION_FINDINGS`, 0 Critical and 3 Major, with artifact SHA-256 `be18740a9c60d110d9acd4487d70ba0c21fafd4485b7d046e39693f5312e6364`. Deterministic acceptance and review remained distinct gates: the former did not override the latter.

The final correction reached controller `BRANCH_ACCEPTED` at exact clean head `3e0f295e8f98c348d684b2c026bacd9ceeeea911`. All 17 required acceptance entries were recorded successful. Its final-Git evidence SHA-256 is `d809846a7f6133adca513e46b531101dac428c8c0707035dd1ec298e4634726b`, and its controller ledger SHA-256 is `9f7d41498c984e8dfce7f762fc9d616e50ccc2b70f50b24b322339e751c51870`.

Acceptance commands 14 and 15 used `! rg ...` while `rg` was unavailable. Their shell exit statuses alone are not substantive proof of forbidden-semantics absence; the independent exact-head reviewer rechecked the production diff and verified that no forbidden merge-policy, write, or transition semantics were introduced.

The final exact-head review returned `CLEAN_CRITICAL_MAJOR`, 0 Critical and 0 Major, with final marker `IMPLEMENTATION_CLEAN_CRITICAL_MAJOR` and artifact SHA-256 `94cd9d4cf4173a2ecf59743aa7892c8b6e091d4eaf70b67cfcc8d18d7c5caebb`. It closed M-001, M-003, and M-006 while preserving M-002, M-004, and M-005. PR #13 merged that exact accepted/reviewed head as `bf2482f756a7c5f75906825b8f3e6e454c72f94f`, with parents previous main `e11afb7d7356a0df36566d98c34adbd07a0097ae` and exact reviewed head `3e0f295e8f98c348d684b2c026bacd9ceeeea911`.

CI Task 3 is merged and complete. Deferred Task 4 — acceptance, scope audit, and exact-head review handoff — is now the sole next eligible CI operation. Publication and later Phase-4 work remain ineligible until Task 4 produces deterministic acceptance for its exact implementation SHA and the same exact SHA receives a fresh 0 Critical / 0 Major review.

## Cross-cutting operational governance

| Track | Status | Exact checkpoint | Next action |
|---|---|---|---|
| Context-bound autonomous operations | MERGED | Candidate `84c6a8ee4b6d6a315eeaa1e7de17fbf5f94a1dec` accepted/reviewed 0C/0M; PR #11 merge `454ea4dce3c674e0d8e55273319cfb4ea4a077a5` | Enforce v2 capsule/authority boundaries |
| Operational state projections | MERGED | Candidate `2ab636f2b118e7e77bcb6656a0afb163f2082461` accepted/reviewed 0C/0M; PR #12 merge `e11afb7d7356a0df36566d98c34adbd07a0097ae` | Apply the immutable-evidence cutoff rule |
| Automatic operation handoff | SEPARATE DESIGN TRACK | No authority applicable to Deferred Task 4 | Do not substitute it for fresh Task-4-only authority |

## Next authorized actions

1. After this reconciliation commits and reaches controller acceptance, resolve its resulting accepted exact head from Git/controller evidence. Do not write that later lifecycle event into these frozen candidate bytes.
2. Freeze a Task-4-only plan for Deferred Task 4 — acceptance, scope audit, and exact-head review handoff — build and independently verify a fresh `context-capsule-v2`, and bind immutable run authority to the exact accepted reconciliation predecessor and Task 4 sources.
3. Execute only Deferred Task 4, produce deterministic ABCP acceptance evidence for its exact implementation SHA, and submit that same exact SHA to a fresh Critical/Major review.

No other CI operation is eligible. Merge approval policy, expected-head protection, merge execution, post-merge acceptance, and automatic operation handoff remain out of scope for this reconciliation and Deferred Task 4.

## Projection materialization rule

This candidate's immutable-evidence cutoff is final Task 3 acceptance and exact-head review at exact clean head `3e0f295e8f98c348d684b2c026bacd9ceeeea911`, followed by PR #13 merge `bf2482f756a7c5f75906825b8f3e6e454c72f94f`. All 17 required acceptance entries were recorded successful; final-Git evidence SHA-256 is `d809846a7f6133adca513e46b531101dac428c8c0707035dd1ec298e4634726b`, and controller ledger SHA-256 is `9f7d41498c984e8dfce7f762fc9d616e50ccc2b70f50b24b322339e751c51870`. The review artifact SHA-256 is `94cd9d4cf4173a2ecf59743aa7892c8b6e091d4eaf70b67cfcc8d18d7c5caebb`, with verdict `CLEAN_CRITICAL_MAJOR`, 0 Critical and 0 Major. The merge parents are previous main `e11afb7d7356a0df36566d98c34adbd07a0097ae` and the exact reviewed head. This cutoff also preserves the original Task 3, earlier Task 2/current-policy, and accepted/reviewed/merged PR #12 boundaries.

Acceptance, review, and merge evidence is authoritative immediately in immutable evidence. This reconciliation cannot self-record events after these bytes freeze, including its own exact commit identity or any later controller acceptance, review, or merge; those belong in the next separately governed projection reconciliation. Resolve this reconciliation's exact accepted head from Git/controller evidence after commit before authorizing Deferred Task 4. Before that non-reconciliation operation begins, all three projections must be current through this boundary.

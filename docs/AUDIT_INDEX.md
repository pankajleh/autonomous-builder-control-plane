# Audit Index

Operational role: this file indexes accepted, reviewed, merged, and durable evidence identities through the current legally recordable cutoff. It is a human-readable projection, not a substitute for immutable controller ledgers, review artifacts, provider evidence, or Git objects/refs.

## Accepted, reviewed, and merged identities

| Scope | PR | Accepted/reviewed head | Merge SHA | Terminal review / evidence |
|---|---:|---|---|---|
| EP-002 | #4 | `0668397491394964d06ddf7ad00ae8032b2ac49d` | `f7ca8e6e59f04e7bece0582f3febcd37ec2cb4f7` | Claude cross-model clean after correction |
| EP-003 | #5 | `f345a910ff14c15be3fdc872eab89c13c5b89caa` | `db56b1f8cf32561be6b707db4bbf046f4c24e067` | Controller fallback after provider failure; prior Majors corrected |
| EP-004 | #6 | `d3193cf5615c5ea33e2f74398519106863ed4b06` | `94e14ca749d31ac214e979aab03fbde37502dd7f` | Controller fallback after provider failure; prior findings corrected |
| EP-005 exact-head PR lifecycle | #8 | `6db075240ce87b751f8db98abb540c96410c515b` | `ccf75d093625119cc39944fe7a47c3a03b30ad3b` | 0C/0M review artifact SHA-256 `e34a632e290930bbfcccfbd4326d5aeab1b1d8012285d67a122a87b55d8196c5` |
| Context-bound autonomous operations | #11 | `84c6a8ee4b6d6a315eeaa1e7de17fbf5f94a1dec` | `454ea4dce3c674e0d8e55273319cfb4ea4a077a5` | 0C/0M artifact SHA-256 `85405bc64c0703319cad881d4b4aa3f07ba7aa91fb97ef16b8172dcb56beebe5` |
| Operational state projections | #12 | `2ab636f2b118e7e77bcb6656a0afb163f2082461` | `e11afb7d7356a0df36566d98c34adbd07a0097ae` | 0C/0M artifact SHA-256 `ab16a9b1711d54391dbe317078879da765aca6de36626f1a95e7ad274546dd78` |
| EP-005 CI Task 3 | #13 | `3e0f295e8f98c348d684b2c026bacd9ceeeea911` | `bf2482f756a7c5f75906825b8f3e6e454c72f94f` | 0C/0M artifact SHA-256 `94cd9d4cf4173a2ecf59743aa7892c8b6e091d4eaf70b67cfcc8d18d7c5caebb` |
| CI Task-3 post-merge projection | #14 | `eb257fab925daf785bdcb924f88ddced6a0b861c` | `6fd7fc2d59a1467f4e85aad18a35dc83d0bc7c9e` | Projection reconciliation predecessor for Task 4 |
| EP-005 CI Task 4 | #15 | `9ea0c67a0ab5fdcc1c26ed6eeae4e09732b5aaa2` | `66da45760c7923bd244f3ac6dd3aa699d89bc2e8` | 15/15 acceptance PASS; `CLEAN_CRITICAL_MAJOR`, 0C/0M; review SHA-256 `0b2a56c940d035e860ae21cfdb6cfd73fbd52f35294177257fe1b08429c357f5` |
| Three-capsule governance hardening | #16 | `aa888f26652f88040ca8faa3499251ba6281a995` | `d145b9f69418fd579650e5b1afca267f6ee59e72` | `RETURN_TO_B_CLOSURE_CLEAN`, 0C/0M; review artifact SHA-256 `200c755f6591e7d1a7fe99f922973bfa46eb35c5e1aea44e20b2b26392f9a779` |
| EP-005 merge lifecycle convergence | #17 | `8dd860286590044888052f2f53e856c3c8c1f1cb` | `ed74fad66b9dd564ac09dcedc1274b8fefffb983` | deterministic acceptance PASS; independent convergence `CLOSURE_CLEAN`, 0C/0M; post-merge acceptance/reconciliation PASS |
| EP-006 rebaseline after assurance removal | #21 | `4040744d9db924ce4e174f7281fdc9f58db7818f` | `821e0476600c9299652b88a016c24ba471bf8a6b` | retained EP-006 tree restored as current platform baseline; post-EP-006 assurance expansion removed |
| Repo C product run admission (ABCP-RC-P01) | #22 | `f1ae22fc3132513bbe5c411058e506676d9fb069` | `3d6a841e722c9c67c83d835a52178b09ca16776d` | final tested executable `38eda0c3ce489dbe5e51ad297ac3374d98fadcd7`; required focused/broad/race/vet/cross-platform gates PASS; exactly two Ralphex review passes; bounded product admission integrated |

EP-005 foundation merged earlier in PR #7 at `bf5f923f1743b541fac8ad75fa173557fe68ba0f`.

## CI evidence-ingestion terminal evidence

The corrected CI design remains the neutral/read-only collection design at SHA-256 `47a6d7b1d4c93a853a26e4da3753894dedacfce930345c610cf24a4236b2b409`, whose bounded design review returned 0 Critical / 0 Major.

Task 3 closed at exact head `3e0f295e8f98c348d684b2c026bacd9ceeeea911`: all 17 required acceptance entries were successful; final-Git evidence SHA-256 `d809846a7f6133adca513e46b531101dac428c8c0707035dd1ec298e4634726b`; controller ledger SHA-256 `9f7d41498c984e8dfce7f762fc9d616e50ccc2b70f50b24b322339e751c51870`; exact review 0C/0M artifact SHA-256 `94cd9d4cf4173a2ecf59743aa7892c8b6e091d4eaf70b67cfcc8d18d7c5caebb`.

Task 4 then audited the exact CI implementation and required corrections. Its corrected exact head `9ea0c67a0ab5fdcc1c26ed6eeae4e09732b5aaa2` passed all 15/15 required acceptance commands. Acceptance result SHA-256 is `2dd220a37f3d52b927d1a89952165f02f0e8434fa6caead60790fb74ce87cf28`; final-Git evidence SHA-256 is `59511978c327683629b9823b4cffe72fc117755b44847a58431ae527e1e502a5`; the final independent review returned `CLEAN_CRITICAL_MAJOR`, 0 Critical / 0 Major, review artifact SHA-256 `0b2a56c940d035e860ae21cfdb6cfd73fbd52f35294177257fe1b08429c357f5`. PR #15 merged that exact head.

CI `STABLE` remains observational only. This evidence does not by itself define required-check/approval policy or grant merge authority.

## Three-capsule governance evidence

PR #16 merged exact corrected head `aa888f26652f88040ca8faa3499251ba6281a995`. Its initial C final review at pre-fix head `80b65675d292b3565d0abce3fbcd7af60fce8851` found seven Major issues; the bounded return-to-B correction closed exactly `C-FINAL-M01` through `C-FINAL-M07` without scope expansion or new findings. Final closure report records `RETURN_TO_B_CLOSURE_CLEAN Critical 0 Major 0`, review artifact SHA-256 `200c755f6591e7d1a7fe99f922973bfa46eb35c5e1aea44e20b2b26392f9a779`, deterministic verification result SHA-256 `f1c08580f42f7e540ada1ced6263645f589877ab04950125168018803c8768f7`, mutation receipt SHA-256 `5400ff636e55b6303bad2fab91a1541c33579343520906e27e65205ba271e6ca`, and source-packet manifest SHA-256 `44063006b187a383e9f4c26384b560f5f86bae9292113d2f4b7d1d8e23d335f8`.

This merge records the hardened three-capsule governance implementation/contracts. It is not evidence, by itself, of a production `GovernanceActivationV1` installation.

## Merge authorization Task 1 / Task 2 / Task 3 evidence

- Task 1 lifecycle contracts were independently accepted/reviewed 0C/0M at `bf5c851e8bce19f61648eb499ad146f25a1ee86c`.
- Task 2 controller/runtime was independently accepted/reviewed `CLOSURE_CLEAN`, 0C/0M at `c6716a800f1b317eb1cae43c9e50fa5e81bf67c2`, closure seal `7e15a03fcc106044e2e8dff12281919427fcb7845800f116cd864b0a53e516ae`.
- Task 3 live GitHub merge-only provider final exact head is `15a7585ca25d4fee036afc6d19324c1b528b9381`, tree `1073baab52a6de97503d50d899fad628d086304f`; independent review `CLOSURE_CLEAN`, Critical 0 / Major 0, verdict SHA-256 `3eca5d815e0f2306e090c6cb543b00a69a1e58259d6988f3f3543259d0cd3fde`, packet-manifest SHA-256 `bb91dba2668c5f158568c96e172eb554ab9260e22649abd0afa12a46378816d0`.

Because `main` advanced through PR #16, Task 3 was not published directly. A bounded convergence merge produced `8dd860286590044888052f2f53e856c3c8c1f1cb` from first parent `d145b9f69418fd579650e5b1afca267f6ee59e72` and second parent `15a7585ca25d4fee036afc6d19324c1b528b9381`. Candidate tree is `c055f686f20634e0e69550cc08e019cf4be7a0e5`. Deterministic convergence acceptance passed; the fresh independent exact-head convergence review returned `CLOSURE_CLEAN`, Critical 0 / Major 0, `BASE_CONVERGENCE_EXACT_HEAD_CLOSURE: VERIFIED`. Its immutable review packet contained 304 manifest entries and independently reverified with zero hash failures.

## Publication, merge, and durable post-merge evidence

PR #17 published exact reviewed head `8dd860286590044888052f2f53e856c3c8c1f1cb` against exact base `d145b9f69418fd579650e5b1afca267f6ee59e72`. GitHub computed the PR clean/mergeable. The normal merge mutation was issued with expected-head binding; result `ed74fad66b9dd564ac09dcedc1274b8fefffb983` has ordered parents `d145b9f…` and `8dd8602…` and result tree `c055f686f20634e0e69550cc08e019cf4be7a0e5`. Candidate-to-result diff is empty.

Post-merge deterministic acceptance on the actual merge commit passed all focused lifecycle/provider/integration checks plus full tests, full race, vet, smoke, metadata-free smoke, Darwin compile, diff-check, and clean-worktree checks. Remote/result reconciliation independently proved the exact PR/base/head/result/ref/tree/parent identities.

Durable evidence identities:

- manifest SHA-256 `ba742fb2ed92eeb03094b34e7f128bb0c46881abbe9300f97ababb52adce7533`;
- acceptance-record SHA-256 `da1a94c11f6803dec15d44b6b79b3c16f88ac8dae6f3e8971666f11a69489609`;
- durability-seal SHA-256 `ca35273ae946fa3cbc91fe5bf099302e1bc33a4e42b9fcdfc25adcb3a987a57b`;
- final manifest recheck failures: zero.

## Historical Phase-4 cutoff

The former Phase-4 cutoff through PR #17 and its fsync-sealed post-merge evidence remains historical audit evidence. It no longer describes the current product-integration boundary after the 2026-09-19 EP-006 rebaseline.

## EP-006 rebaseline and P01 admission evidence

PR #21 re-established the retained EP-006 service/runtime tree on `main` at merge `821e0476600c9299652b88a016c24ba471bf8a6b`. This intentionally removed the later post-EP-006 assurance expansion from current authority while preserving historical Git evidence.

ABCP-RC-P01 then froze the product-facing Repo C→ABCP admission boundary. Exact implementation base was `4d45202f5b411d9c91caf5fef906d1eff9b26e4b`. The final tested executable was `38eda0c3ce489dbe5e51ad297ac3374d98fadcd7`.

P01 validation included focused package tests, full Go tests, focused/full race, `go vet ./...`, Darwin/Windows builds and `git diff --check`; all passed. The PostgreSQL integration test skipped exactly as authorized because `ABCP_WORKFLOW_AUTHORITY_PG_TEST_DSN` was unavailable and no live infrastructure was provisioned.

The actual Ralphex review loop had exactly two passes. Review 1 corrected frozen-profile/replay and repository-identity issues; Review 2 corrected duplicate-launch risk after an ambiguous successful process start. No third Ralphex review was performed or authorized.

PR #22 published exact head `f1ae22fc3132513bbe5c411058e506676d9fb069` and merged as `3d6a841e722c9c67c83d835a52178b09ca16776d`. Git/GitHub therefore supersede the pre-merge evidence document's historical "merge not done" statement.

## Current cutoff and non-claims

The current legally recordable source cutoff is Repo B `main@3d6a841e722c9c67c83d835a52178b09ca16776d`: retained EP-006 plus bounded product run admission.

This cutoff does not claim Repo C UI completion, general retry/resume/recovery, human-decision continuation, provider redesign, live deployment/AWS mutation, or restoration of the discarded post-EP-006 assurance framework.

Further Repo B work requires fresh bounded authority from a concrete product integration need; review/discovery alone is not authority to expand scope.

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
| P01 current-state and audit projection | #23 | `c2d235a07215a80b0fe5c8fd66acd55490b64728` | `f39eeb7d313a1deac630a0d51c2ebdd9c8415150` | documentation only |
| P01 projection merge-stability follow-up | #24 | `055b54e72351ec31010884b5ab4b4df2767eae4d` | `ff2202884cad24ac1b4ced70c8100d0ebc392827` | documentation only |
| Controller-owned Ralphex execution profile (ABCP-RALPHEX-P01) | #25 | `bfd18fcea5d58351abd15728dd98066bfc266813` | `12e292afb93fd201485cedc734397c006140c202` | `docs/execution-packs/ABCP-RALPHEX-P01/evidence.md` |
| G0 governed development-run admission | #26 | `c023870bb02b5305352e884ebc339a84b3b3a895` | `baf1b37f565d1054c1c96fcc3d46ed386c5a0d5c` | `docs/execution-packs/G0-DEVELOPMENT-TOOLING-CUTOVER/` |
| Product defaults and bounded Ralphex validation | #27 | `2e1a20a9ca157879c28849963252736876045f28` | `5ae6fa6c8d06431a998cd4be08b4939b6972e838` | PR #27 |
| Runtime catalog run-authority xattr relocation | #28 | `59d538febf0c7ad1789974fc71087304539510de` | `fd1a17bb80a66a73fc373d31e5790112c07b39fc` | PR #28 |
| Branch-accepted terminal report (F-1) | #29 | `4eab423f9bb051933511307e69ca64ec3586c2fb` | not merged | closed without merge |
| Reject acceptance commands that cannot fail | #30 | `cd99895d037fcda9833e7fe33d20b66e6dab1a2c` | `fec0c088337553a74e5d7423db41a400e96b2a42` | PR #30 |
| Human-decision pause and resume loop | #31 | `1f48b1a7df8ea492a22906c42f76cb0d673f7356` | `6fc11cd96959869802be37dc03724d2b7f747d13` | PR #31; `retry`/`resume`/`recovery` capability flags remain false |
| Full admission run identity | #32 | `69b6261dbf7af7764a953fd63c94398eeab88ff8` | `935114d21cdd79cf444bdea89c6052e27309a5bf` | PR #32 |
| BP-01 provider-neutral activity | #33 | `cf04357d842174a1db877230edc2873be0e72d42` | `f21174dbaf47f4e71185f58dff5a338d0befa861` | `docs/plans/completed/bp-01-provider-neutral-activity.md` |
| BP-02 governed preview runtime | #34 | `1705eba52346760edb58f5ba380fcf94d483d7f6` | `ad8d19ad1fcefb775a9c4ae6457bab772ccc96a9` | `docs/plans/completed/bp-02-governed-preview-runtime.md` |
| BP-02 Docker 29 bind propagation | #35 | `3c4cd6eaec6798fb60aa575dfe642fb2df275cf1` | `5cd8ee5d3cc60370f9e607e2fda267eec514eee2` | `docs/plans/completed/bp-02-docker29-bind-propagation-correction.md` |
| PX-07 preview route resolution | #36 | `b2b57c565c68dbd85a522a2b273897e4fa427363` | `d510f0c743bed33cdf2a53ffe9b46604ac85c31a` | `docs/plans/completed/px07-preview-route-resolution.md` |
| BP-01 activity replay continuity | #37 | `b8c3a13572260a62699087d0db65dfb85bae34de` | `34dec9ee8d725304c0c56aa7c53118113759711c` | `docs/plans/completed/bp01-activity-replay-continuity-correction.md` |
| BP-01 provider replay semantics | #38 | `d573ab93430f48e403b5b0429d8e66fa057f147d` | `dfc3b93858b157c93cd074d5a1abf521e50c62cf` | `docs/plans/completed/bp01-provider-replay-semantic-correction.md` |
| BP-01 marker diagnostics | #39 | `bf96b34d4a8c1ecf5e10e7720c395b8d99adfc85` | `fd39b1a32e0f741e35d48013cf7a337aa7628e17` | `docs/plans/completed/bp01-activity-marker-diagnostics.md` |
| BP-01 sidecar resume overlap | #40 | `60efc66c7f704726a7ec527e5f4d6ad461eea901` | `20f2319d9291252e2c641971235faac1405d1bec` | `docs/plans/completed/bp01-sidecar-resume-overlap.md` |
| BP-01 worktree cleanup transition | #41 | `512725720f6ffd400dc676c995a4406ce2097b8a` | `0d99f7d340cec40336728c6c59dcb66a41d2a77c` | `docs/plans/completed/bp01-worktree-cleanup-transition.md`; Repo C PX-07 closure ran on this merge |
| Controller-owned worktree retention | #42 | `a5ee44ae30fc98a5676695347a6694fb8a82ba47` | `a5383a672f99b68413cb946a6d93f89116a2769b` | `docs/plans/completed/bp02-controller-owned-worktree-retention.md`; live proof run `admission-2b69b2cd…` |
| Ledger reconciliation and versioned operator configuration | #43 | `d723b01b7fbb3ef08492aeb117feb509ba6244f8` | `f36422201591e81a336d4e43690cb68efa64d17e` | `deploy/local-integration/README.md`; host drift check clean |
| Ralphex governance on upstream release v1.7.0 | #44 | `9b8b03e860b44929b87a7c9ac87c841b0f0e6c17` | `65992f746541f2794710f75d670e3b1dc297c084` | `pankajleh/ralphex-governance` `abcp/v1.7.0` @ `2275e23`; binary `fe5a7c46…4058`. Proof run `admission-f7e2967f1e7e3085a03f4d16ec5f68e0799e70ddeaca90090051754e945d6dcb`: `BRANCH_ACCEPTED`, worktree retained, 61→74 events, 0 `UNKNOWN`, preview after acceptance `READY` with marker `200` |
| Ralphex upstream-acceptance upgrade note | #45 | `303bb916e339eefdaf62c772b440ea1543c5f97f` | `fbf1ab8a1cb6a8f44286472de5d6d9a9491f9cfd` | documentation only |
| Controller-owned worktree eviction | #46 | `0fb3f0f82caefd8a3535de3acfdcc85db7dd3c32` | `7ae1a5f2d520a2d46a09620544dc0ddeb9ddfa09` | `docs/plans/completed/bp02-worktree-eviction.md` |
| Bounded trailing provider detail for retained worktrees | #47 | `c63423065f8d1c7e63f6ffd3dc42d55c3c853df5` | `bef3cd5729f7753ab1800d0292d95b1d7a701230` | `docs/plans/completed/bp01-bounded-trailing-detail.md` |
| Eviction sweep summary | #48 | `64ea62bcf75f8ec49f2c2a7c1c691822b2598b8a` | `880d9c66c79c45f16822a66b90b0a6f6c9efa8f9` | `docs/plans/completed/bp02-worktree-eviction.md` |
| Eviction idle counts only live streams | #49 | `ff78fbfed51aebd6961a85e60c6de03fb2ae3a48` | `b187536aa194ff667b17102efb54d1ecac7d94fe` | `docs/plans/completed/bp02-worktree-eviction.md` |
| No activity marker on shutdown or cancelled reads | #50 | `ff860580991025b7a03adaf43a401c5f60a3c639` | `e8ab2c97285e8767d8b9e4ad80d3b47f6a7f4a1b` | `docs/plans/completed/bp01-no-marker-on-shutdown.md` |
| Worktree eviction live proof | #51 | `f7d7c44f91739735feda4873b55afae47d332166` | `ab4a007d745b532a2d564d691c3fff3e9fd99fca` | `docs/CURRENT_STATE.md`; run `admission-3ce5345c4792fc37113251cfb9ca1a1fb03291838ee41f0eaa5461c75f0d4280`: evicted `idle` 05:29:58Z, 3 pinned checkpoints, record `0600`, 83→96 events, 0 `UNKNOWN`; previews `aebb78eb…` and `5c747a06…` after eviction `READY/HEALTHY` from `e4eaacd…`, marker `200` |
| Fresh provider sidecar grace | #52 | `0013073eccfd66b392f6fa27da6e8855260d961e` | `928f94acf48f197ff73a33e798bc27c4d20de1e7` | `docs/plans/completed/bp01-fresh-sidecar-grace.md` |
| Worktree release API and eviction activity | #53 | `98d1f301371eb00b6e681673b7d0a99c0372becf` | `7ac0cffef3887ba8d46bd1390b39c2033d048940` | `docs/plans/completed/bp02-worktree-release.md` |
| Confirmed replay gaps; running-build release refusal | #54 | `6d5c7304156a93f2d6f2834cbcbf0e26b0330eb9` | `109e15b45ae1f75316f3b3ba75b93e6ae6a6d128` | `docs/plans/completed/bp01-confirmed-replay-gaps.md` |
| Task-closure live proof | #55 | `96a326205ce8f2e885bf84e42b6eb553a1b40e23` | `bd4008332162e4bf0e047598745df9226ff463e3` | `docs/CURRENT_STATE.md`; runs `admission-3ce5345c…` (`ALREADY_RELEASED`) and `admission-6f80645d…` (`RELEASED`) |
| Queued run never marked | #56 | `190e7b39d5fe1ae7b2e15987c123511140b03936` | `2bd85f7c308f7a37546e68037c76855977b03a24` | `docs/plans/completed/bp01-queued-run-no-marker.md` |
| Servable-app product acceptance; hour-long previews | #57 | this PR | this PR | `docs/plans/completed/bp02-product-acceptance-servable-app.md` |

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

The current recordable source cutoff is Repo B `main@e8ab2c97285e8767d8b9e4ad80d3b47f6a7f4a1b` (PR #50) plus this documentation PR #51. It covers retained EP-006, bounded product and development admission, the Ralphex execution profile, human-decision pause/resume, BP-01 activity, BP-02 preview runtime, and controller-owned worktree retention and eviction (PRs #42, #46–#50). That same executable is live on the local-integration host, proven by the eviction proof run above.

This cutoff does not claim general retry/recovery or the advertised `retry`/`resume`/`recovery` capabilities. Nor does it claim a customer-visible eviction entry, eviction on task closure or tenant deletion, provider redesign, live production/AWS deployment, or restoration of the discarded post-EP-006 assurance framework.

Earlier entries (PRs #23–#41) were recorded retrospectively on 2026-09-28; they merged without updating these projections.

Further Repo B work requires fresh bounded authority from a concrete product integration need; review/discovery alone is not authority to expand scope.

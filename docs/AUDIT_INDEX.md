# Audit Index

This index links each accepted execution pack to its reviewed Git identity and merge evidence. Runtime run/evidence references are added as governed execution progresses.

| EP | PR | Reviewed head | Merge SHA | Review mode |
|---|---:|---|---|---|
| EP-002 | #4 | `0668397491394964d06ddf7ad00ae8032b2ac49d` | `f7ca8e6e59f04e7bece0582f3febcd37ec2cb4f7` | Claude cross-model clean after correction |
| EP-003 | #5 | `f345a910ff14c15be3fdc872eab89c13c5b89caa` | `db56b1f8cf32561be6b707db4bbf046f4c24e067` | controller fallback after Claude capacity failure; prior Claude Majors corrected |
| EP-004 | #6 | `d3193cf5615c5ea33e2f74398519106863ed4b06` | `94e14ca749d31ac214e979aab03fbde37502dd7f` | controller fallback after Claude session failure; all prior Claude/controller Majors corrected |

## EP-005 active

Roadmap authority: Phase 4 GitHub lifecycle. Foundation merged in PR #7 at `bf5f923f1743b541fac8ad75fa173557fe68ba0f`.

Exact-head lifecycle implementation `f986008ba11c69c3864f0b8977af440024d12048` passed ABCP acceptance but post-implementation review found 1 Critical and 9 Major defects. First governed correction round then passed ABCP acceptance at `d4e7e1d8fc6af9545f9f67e3c99fa94c3420a7ce`; fresh Claude Code / Opus exact-head review found 2 Critical and 4 Major defects, artifact SHA-256 `d07f9124d771a38caff86b5c3283a70c35d5fe71379b329b1513416392c99421`.

Second correction plan `docs/plans/ep-005-pr-lifecycle-review-corrections-2.md` is design-clean at SHA-256 `86b469cb4ced969394854aa88ad91418721b64d27a83aed5a4a2c0eeda6c8527`. First Claude design review found 1 Critical + 3 Major gaps; revised-plan Claude re-review hit provider session limits, after which policy-authorized controller fallback returned `DESIGN_CLEAN_CRITICAL_MAJOR`, artifact SHA-256 `6851fe6a742e6bdb5cf49ed7587ec690936719d9510f6de243b1982041ebcbde`.

Second correction implementation then reached ABCP `BRANCH_ACCEPTED` at `35faeec8b8314519ee6b9d36eba32a0c25ac6050`. Fresh Claude exact-head review verified the ten prior findings corrected and found one remaining Critical, artifact SHA-256 `9fa76414ce73fc9168de70d967e28539cf662a54802e09678d1a5de829266974`. Third correction design is clean at SHA-256 `cb7349da368084eb607e2e9d93f284e4801be831ddc08a1959c572eb3912ce60`; after Claude refinement hit a provider session limit, policy-authorized controller fallback returned `DESIGN_CLEAN_CRITICAL_MAJOR`, artifact SHA-256 `b82f3b3a84c31137c35fc7461acc9de152a916f5fd075d1521235a9f4bf4a984`. The first implementation launch failed pre-task because the plan lacked Ralphex task syntax; a syntax-only Task 1 amendment was reviewed clean at artifact SHA-256 `9874944142e54377f005f50cda1fa73af4b6e8290f136c73afdb8d6c0ee178e6` and changed no architecture semantics.

The exact-head PR lifecycle subtrack ultimately reached final ABCP `BRANCH_ACCEPTED` at `6db075240ce87b751f8db98abb540c96410c515b`. Final controller fallback review, permitted after Claude provider session-limit failure, returned `CLEAN_CRITICAL_MAJOR` with 0 Critical + 0 Major; review artifact SHA-256 `e34a632e290930bbfcccfbd4326d5aeab1b1d8012285d67a122a87b55d8196c5`. PR #8 merged that exact reviewed head into `main` at merge SHA `ccf75d093625119cc39944fe7a47c3a03b30ad3b`.

EP-005 remains active because Phase 4 also requires CI evidence ingestion, merge approval policy, expected-head merge protection, and serial post-merge acceptance. The next governed track is CI evidence ingestion. Claude's first CI-ingestion design was frozen at SHA-256 `d90099eeab3af74ea5dd25f50b7e87f920f7cb47eb523e4e7112882dc0524276`; independent Codex review returned `DESIGN_FINDINGS` with 1 Critical and 10 enumerated Major findings, review artifact SHA-256 `659174158aecc5a690663f79e8a2b567a459b77af0cf08042d37cdfb6db265f3`. CI implementation remains unauthorized while the design is re-scoped and corrected.

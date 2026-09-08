# Implementation Progress

Updated: 2026-09-08

| Phase | Status | Evidence |
|---|---|---|
| Phase 0 — Foundation | COMPLETE | EP-001 merged |
| Phase 1 — Governed single-plan execution | COMPLETE | PR #4 merged |
| Phase 2 — Recovery and blocker control | COMPLETE | PR #5 merged |
| Phase 3 — Cross-plan scheduler and integration | COMPLETE | PR #6 merged at `94e14ca749d31ac214e979aab03fbde37502dd7f` |
| Phase 4 — GitHub lifecycle | IN PROGRESS | Foundation merged in PR #7 at `bf5f923f1743b541fac8ad75fa173557fe68ba0f` |
| Phase 5 — Service/API/dashboard | NOT STARTED | roadmap only |
| Phase 6 — Production hardening | NOT STARTED | roadmap only |

## Current

EP-005 PR lifecycle is merged in PR #8. Final reviewed/accepted head: `6db075240ce87b751f8db98abb540c96410c515b`; merge SHA: `ccf75d093625119cc39944fe7a47c3a03b30ad3b`. Final post-implementation controller fallback review (Claude provider session-limit fallback) returned `CLEAN_CRITICAL_MAJOR` with 0 Critical + 0 Major; artifact SHA-256 `e34a632e290930bbfcccfbd4326d5aeab1b1d8012285d67a122a87b55d8196c5`.

The next Phase-4 track is **CI evidence ingestion** on branch `ep-005-ci-ingestion`, based exactly on merged `main` SHA `ccf75d093625119cc39944fe7a47c3a03b30ad3b`. Claude's initial design was frozen at SHA-256 `d90099eeab3af74ea5dd25f50b7e87f920f7cb47eb523e4e7112882dc0524276`; independent Codex review returned `DESIGN_FINDINGS` with 1 Critical and 10 enumerated Major findings, artifact SHA-256 `659174158aecc5a690663f79e8a2b567a459b77af0cf08042d37cdfb6db265f3`. No implementation has started.

## Next

Re-scope the CI-ingestion design to the exact roadmap bullet: bounded, stable, immutable CI/check evidence collection tied to the exact candidate head SHA. Keep merge approval policy and merge authorization in the following Phase-4 track; do not import PR-write-style replay/admission semantics unless read-only evidence integrity demonstrably requires them. Correct all in-scope Critical/Major findings, then run a fresh independent Critical/Major design gate before creating implementation authority/context. Only after a 0 Critical + 0 Major design verdict may ABCP/Ralphex implementation begin.

## Remaining after Phase 4

After exact-head PR lifecycle acceptance/review/merge, continue Phase 4 in roadmap order: CI evidence ingestion, merge approval/expected-head protection, then serial post-merge acceptance. Service/API/dashboard and production hardening remain deferred to Phases 5–6.

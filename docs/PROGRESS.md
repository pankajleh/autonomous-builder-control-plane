# Implementation Progress

Updated: 2026-09-09

Operational role: this file projects roadmap and subtrack progress plus the next authorized action. It must be consulted before planning or starting an operation and reconciled at accepted, reviewed, and merged lifecycle boundaries. It is not completion authority; immutable controller and Git evidence remain authoritative.

## Roadmap

| Phase | Status | Evidence and current boundary |
|---|---|---|
| Phase 0 — Foundation | COMPLETE | EP-001 merged |
| Phase 1 — Governed single-plan execution | COMPLETE | PR #4 merged |
| Phase 2 — Recovery and blocker control | COMPLETE | PR #5 merged |
| Phase 3 — Cross-plan scheduler and integration | COMPLETE | PR #6 merged at `94e14ca749d31ac214e979aab03fbde37502dd7f` |
| Phase 4 — GitHub lifecycle | IN PROGRESS | Foundation PR #7 and exact-head PR lifecycle PR #8 merged; CI evidence ingestion has Tasks 1–2 implemented but not accepted/reviewed/merged |
| Phase 5 — Service/API/dashboard | NOT STARTED | Roadmap only |
| Phase 6 — Production hardening | NOT STARTED | Roadmap only |

## Phase 4 subtracks

| Subtrack | Status | Exact checkpoint | Next action |
|---|---|---|---|
| GitHub lifecycle foundation | MERGED | PR #7 at `bf5f923f1743b541fac8ad75fa173557fe68ba0f` | Preserve frozen behavior |
| Exact-head PR lifecycle | MERGED | Accepted/reviewed head `6db075240ce87b751f8db98abb540c96410c515b`; PR #8 merge `ccf75d093625119cc39944fe7a47c3a03b30ad3b` | Preserve frozen behavior |
| CI evidence-ingestion design | ACCEPTED | Design SHA-256 `47a6d7b1d4c93a853a26e4da3753894dedacfce930345c610cf24a4236b2b409`; 0C/0M review `aa5d86471ffcfe1d68eff9c49ef5b2df718355c0b4c825716d35d68217b3fa45` | Keep implementation within neutral read-only collection semantics |
| CI Task 1 — contract and bounds | IMPLEMENTED, NOT ACCEPTED | `e1740f4be8df06571a3299c3fb31ecc31b0a1aea` | Retain as predecessor checkpoint |
| CI Task 2 — bounded reads and stabilization | IMPLEMENTED, NOT ACCEPTED | `da8ffea4582539067724b363b3144d9601dee086` | Retain as exact Task 3 base |
| CI Task 3 — immutable evidence, ledger, replay | BLOCKED BEFORE START | No Task 3 implementation commit | Make Task 3 the sole incomplete executable section; issue a fresh v2 capsule and authority at `da8ffea4582539067724b363b3144d9601dee086` |
| CI Task 4 — acceptance and review handoff | WAITING | No implementation checkpoint | Begin only after Task 3 completes under a separate fresh operation |
| Merge approval and expected-head protection | NOT STARTED | Roadmap only | Wait for accepted/reviewed/merged CI subtrack |
| Serial post-merge acceptance | NOT STARTED | Roadmap only | Follow merge protection in roadmap order |

The prior CI launch correctly failed closed at the Task 3 handoff: its capsule was bound to Task 2's start SHA `e1740f4be8df06571a3299c3fb31ecc31b0a1aea`, while Task 2 committed a new HEAD `da8ffea4582539067724b363b3144d9601dee086`. Task completion cannot silently extend the old capsule across that commit boundary.

## Cross-cutting operational governance

| Track | Status | Exact checkpoint | Next action |
|---|---|---|---|
| Context-bound autonomous operations | MERGED | Candidate `84c6a8ee4b6d6a315eeaa1e7de17fbf5f94a1dec` accepted and reviewed 0C/0M; PR #11 merge `454ea4dce3c674e0d8e55273319cfb4ea4a077a5` | Enforce the v2 capsule/authority boundary for every new operation |
| Automatic operation handoff | DESIGN DRAFT, NOT GATED | Separate worktree at base `454ea4dce3c674e0d8e55273319cfb4ea4a077a5`; draft files are uncommitted | Freeze exact design artifacts, run the design gate, then create implementation authority only after a clean decision |

Automatic handoff is independent of EP-005 deliverable semantics. It may automate future controller transitions, but it does not authorize CI Task 3 and must not import or redefine CI collection, merge-policy, or post-merge behavior.

## Next authorized actions

1. For EP-005 CI, establish a fresh Task 3 operation at exact predecessor `da8ffea4582539067724b363b3144d9601dee086`, with only one incomplete executable task and a newly verified v2 capsule/authority.
2. Complete CI Task 3 without carrying the previous capsule across HEAD; then repeat the fresh-operation boundary for Task 4.
3. After the final CI implementation head, produce deterministic controller acceptance and obtain a fresh exact-head 0 Critical/0 Major implementation review before publication.
4. Independently, freeze and review the automatic-handoff design. Do not make its draft status a substitute for the manual fresh-authority steps above.

At each `BRANCH_ACCEPTED`, exact-head review, or merge boundary, update this roadmap projection, `CURRENT_STATE.md`, and the accepted/reviewed/merged identities in `AUDIT_INDEX.md` from the immutable evidence.

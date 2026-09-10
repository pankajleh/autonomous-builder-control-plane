# EP-005 — Task 1 exact-head review corrections

Exact reviewed candidate: `51582db8b75af2dc5a51fde6c5c0b1e10cac1b71`.
Fresh exact-head xhigh review artifact SHA-256: `16d3a4353ff7a9d69e9e5605cedb010e1dacd1a910876d80f0764e955f024fc1`.
Verdict: `IMPLEMENTATION_FINDINGS`, Critical 1 / Major 7.

This correction is limited to the network-free `internal/githublifecycle` Task-1 contracts/tests and `docs/architecture/GITHUB_LIFECYCLE_FOUNDATION_CONTRACT.md`. The accepted master design remains authoritative. Do not implement Task 2 controller/runtime, Task 3 live GitHub transport, projections, Phase 5, or any remote side effect.

### Task 1: Close C-01 and M-01 through M-07 at their root causes

- [x] **C-01 final revalidation seal:** make `AuthorizationSealV1` bind a distinct controller-ordered final-revalidation record, fresh PR/review/check/status request identities, current READY proof, and an independently canonical final decision digest; reject reuse of admission observations.
- [x] **M-01 READY authority completeness:** require exact controller actor/source, complete READY-event and accepted-source evidence closure, and locally checkable bounded ledger offset/sequence/prefix coherence.
- [x] **M-02 pagination authority:** validate each source against independently derived frozen query identity (repository/PR/head/API/filter/method/path/document), retain unambiguous terminal response evidence, reject self-certified/truncated closure, and canonicalize unordered review/check sets only after closure validation.
- [x] **M-03 merge recipe derivation:** deterministically enforce policy message/trailer/timestamp derivation and object-format-consistent OID widths before result hashing; caller input must not alter policy-derived recipe bytes.
- [x] **M-04 one seal/one mutation identity:** derive `clientMutationID` only from the bound `WriteAttempt.WriteID`; eliminate unrelated caller mutation IDs and make commitment deterministic for one seal.
- [x] **M-05 typed NOT_APPLIED proof:** add strict-canonical independently validated zero-byte / atomic base-head rejection proof contracts binding repository, both exact ref updates/OIDs, rejected predicate, capability, request/response identity, raw evidence digest, and all-or-nothing disposition.
- [x] **M-06 cancellation completeness:** expand independent expectations to every authority-bearing cancellation field, bind the exact canonical typed submission proof, and independently validate replay identity keyed by source kind/request ID before CANCELLED selection.
- [x] **M-07 generic strategy representation:** preserve network-free generic squash/rebase result/post-merge representation while keeping production merge policy/support merge-only; stage-qualify sealed merge-v1 requirements or separate production-v1 contracts without making squash/rebase executable.
- [x] Add adversarial regression tests for every finding, including initial-evidence-as-final rejection, incomplete READY closure, wrong pagination source/empty-terminal ambiguity, policy-recipe tampering, duplicate mutation identity, forged NOT_APPLIED, cancellation expectation/replay forgery, and generic squash/rebase representation.
- [x] Run gofmt, vet, githublifecycle tests/race, full repository tests/vet, Darwin compile-only validation, production network-free guard, scope checks, and git diff --check.
- [x] Commit the corrected exact candidate and move this plan to `docs/plans/completed/` only after all correction work/tests pass. Do not claim ABCP acceptance or exact-head review inside Ralphex.

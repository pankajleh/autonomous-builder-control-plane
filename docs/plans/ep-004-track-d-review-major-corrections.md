# EP-004 Track D Independent/Controller Review Major Corrections

## Overview
Correct the substantive Critical/Major review findings on exact Track D head `f8ac7f8175d0155eddeb47bb5cff4f910e0a114e` without expanding into Phase 4+ work.

## Context
- Roadmap authority: Phase 3 / EP-004 serial `READY_FOR_MERGE` gate.
- Reviewed Track B head remains `d8d4299e040f3dbca8a920b8d338c8e06bc04295` and must not be weakened.
- Reviewed Track C head remains `a35b2d23e2367e793af0fb7c1cc9c872555f5b65` and must not be weakened.
- Track D base implementation passed ABCP deterministic acceptance at `f8ac7f8175d0155eddeb47bb5cff4f910e0a114e`, but independent Claude review returned four Majors; controller review confirmed them and additionally requires durable source-head re-verification evidence.
- Verify the governed context capsule before any edit; stop on any mismatch.

### Task 1: Correct merge-authority trust and evidence boundaries
- [ ] Bind the required review policy to immutable controller authority rather than allowing caller-selected `ReviewPolicy` to self-certify. Preserve backward compatibility for earlier authority manifests when the merge gate is not used.
- [ ] Require each review requirement/attestation to match the authority-bound component identity and exact reviewed SHA; reviewed SHAs must resolve as exact commits in the governed repository. Review evidence must be inside the controller-owned evidence root and byte/digest verified.
- [ ] Replace Track D `readVerified` with bounded, race-resistant local evidence reads: absolute clean path, controller evidence-root containment, no symlink traversal, nonblocking open, regular-file proof, explicit per-artifact size bound, bounded read, identity/replacement recheck, SHA256 verification, and fail-closed behavior on unsupported platforms.
- [ ] Harden Track B `readExistingEvidence` so file type, evidence-root containment, symlink/path ambiguity and size bounds are proven before bytes are read; no FIFO/device/oversized artifact may block or exhaust the gate.
- [ ] Correct evidence fan-out governance: the gate must support the maximum legitimate Track C acceptance evidence set without a magic cap that wedges a run. Every source contribution must remain explicitly bounded; exceeding a bound must still emit a terminal `VALIDATION_UNAVAILABLE` transition with a reduced verified evidence set instead of returning while ledger state remains `INTEGRATING`.
- [ ] Capture immutable evidence for every source-head/replacement-ref re-verification used by the final merge-readiness decision and include those refs in the relevant transition/decision evidence; the ledger must prove the boundary check actually happened.
- [ ] Preserve deterministic Track B → Track C provenance, cleanup-on-every-path, exact target identity, conflict classification, and the state-machine invariant that only clean combined acceptance can reach `INTEGRATION_ACCEPTED` then `READY_FOR_MERGE`.
- [ ] Add regressions for fabricated/self-selected review policy, nonexistent/wrong reviewed commits, review evidence outside the evidence root, FIFO/device/symlink/oversized evidence, Track B pre-read special-file attacks, large legitimate acceptance-evidence fan-out, fan-out overflow terminalization, and durable source-head verification evidence.
- [ ] Keep `internal/scheduler/` unchanged unless an unavoidable serial contract revision is explicitly proved; do not implement GitHub lifecycle, dashboard/API, deployment, production acceptance, or Phase 4+ work.
- [ ] Run `gofmt`, focused regressions, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`; confirm the final diff is bounded and commit the correction.

## Success criteria
- [ ] A caller cannot manufacture a clean review policy/attestation pair and thereby satisfy merge review authority.
- [ ] No caller-controlled or reused evidence path can cause unbounded allocation, special-file blocking, symlink traversal, or read-before-validation.
- [ ] Evidence cardinality bounds cannot strand a run in `INTEGRATING`; every bound failure is durably fail-closed.
- [ ] `READY_FOR_MERGE` evidence contains reproducible proof of exact source-head verification at the boundary.
- [ ] Exact-head deterministic acceptance remains green after the correction.

## Non-goals
Do not create a new global blocker registry or redesign Phase 2 blocker semantics in this correction. Do not add PR/merge automation, service/API/dashboard work, deployment, or production acceptance.
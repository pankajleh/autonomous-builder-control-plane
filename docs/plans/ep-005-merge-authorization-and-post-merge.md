# EP-005 — Merge authorization, expected-head protection, and post-merge acceptance

## Roadmap authority
This plan implements only the remaining serial Phase-4 bullets after PR #15: merge approval policy, expected-head/base protection plus merge execution, and post-merge acceptance. The frozen `internal/githublifecycle` contracts remain the authority boundary.

## Design gate

### Authority binding
- Consume exact `githublifecycle.Authority`: repository/base/head, accepted head SHA, expected pre-merge base tip SHA, PR identity, allowed merge method, acting principal, and controller-derived expected merge content.
- Bind a new immutable merge-policy object: required check names, minimum current-head approvals, optional required reviewer identities, exact policy version/hash.
- Merge execution requires exact PR and CI snapshots independently revalidated immediately before the write.

### Provenance and state
READY_FOR_MERGE evidence -> lifecycle authority -> PR/CI observations -> policy decision -> durable merge-write admission -> provider merge result -> post-merge observation -> strategy-aware verification -> MERGED transition. No provider success can skip policy or post-merge proof.

### Merge policy
- Required checks are an exact bounded unique set. Each required check must appear exactly once for the accepted head, be completed, and conclude success.
- Current-head reviews are reduced deterministically by reviewer identity. `changes_requested` blocks. Approvals from another head do not count. Required reviewers must each currently approve. `minimum_approvals` is bounded and may be zero only when no required reviewers are configured.
- Canonical policy JSON/SHA-256 is included in approval evidence and merge admission identity; policy cannot change between decision and write.

### Remote write and ambiguity
- Add controller-owned `internal/mergelifecycle`; do not mutate Phase-3 scheduler/integration semantics.
- Re-observe and validate PR/head/base and CI immediately before submission; re-evaluate policy immediately before submission.
- Persist create-or-verify merge admission before provider write, binding authority SHA, policy SHA, PR/CI digests, approval decision digest, write ID, payload digest.
- One submitted merge attempt has zero implicit retry. Cancellation/deadline after possible submission is ambiguous. Reconciliation may prove applied/not-applied/unknown; only proven not-applied can consume explicit bounded retry authority.
- Concurrent attempts for the same exact authority serialize and replay terminal evidence rather than duplicate writes.

### Expected-head/base protection
The final pre-write PR snapshot must still bind exact accepted head and expected pre-merge base tip. Any head/base/PR/method/actor/policy drift fails closed before `Provider.Merge`. The request uses the existing immutable `MergeInput` and exact write attempt.

### Post-merge acceptance
After confirmed merge result, call `ObservePostMerge` and require `githublifecycle.VerifyPostMerge` to prove repository/base/PR/actor/write identity, accepted head/tree, base-before, method, result/base-after SHA, expected tree, ordered parents, and squash/rebase lineage as applicable. Persist result and verification evidence before emitting MERGED. A merge that may have happened but lacks valid post-merge proof is not MERGED.

### GitHub provider
- Implement live GitHub merge/post-merge/reconciliation with structured HTTP only, bounded headers/body/text, authenticated-principal verification, exact REST identities, request IDs, no secret persistence, no shell/gh CLI.
- Merge request includes exact accepted head SHA and authority-bound method. Responses are reconstructed into frozen types and revalidated.
- Reconciliation uses exact PR/write authority and never infers success from merged state without matching content/identity proof.

### Bounds / filesystem / durability
Bound policy entries, calls, retries, bytes, evidence refs, ledger scans, admission files, and operation duration. Linux durable admission/evidence uses descriptor-relative symlink/special-file-resistant paths, file+directory fsync, file-level serialization, immutable create-or-verify, bounded scans. Unsupported platforms fail closed where required.

### Failure matrix
- Stale head/base, missing/failed/pending required check, blocking review, policy mismatch: substantive blocker, zero write.
- Provider unavailable before submission: bounded retry only by authority.
- Possible submission: ambiguous write; no blind retry.
- Confirmed merge + invalid post-merge evidence: fail closed and preserve result; never emit MERGED.
- Ledger/evidence durability ambiguity: fail closed; no later transition.

### Adversarial tests
Moved head/base immediately before write; duplicate/missing required checks; stale-head approval; latest changes-requested; forged policy hash; mutated approval evidence; duplicate concurrent merge; cancellation after submission; ambiguous reconciliation applied/not-applied/unknown; wrong merge method/tree/parents/lineage; merge/squash/rebase positive proofs; post-merge base mismatch; symlink/path replacement; partial append/fsync failure; exact limit/limit+1; non-Linux compile/fail-closed.

## Task 1: Implement merge policy and controller-owned authorization
- [ ] Add immutable bounded merge-policy and deterministic policy evaluation over exact PR/CI snapshots.
- [ ] Add Linux durable merge admission/terminal material and fail-closed unsupported-platform stubs.
- [ ] Add merge controller through pre-write validation, one-write execution, ambiguity reconciliation, result validation, and terminal evidence; do not emit MERGED yet.
- [ ] Add focused adversarial tests for policy, stale authority, concurrency, durability, and ambiguous-write behavior.
- [ ] Run package/full tests, race, vet, non-Linux compile, scope checks, and diff hygiene; commit only Task 1.

## Task 2: Implement live GitHub merge provider and post-merge acceptance
- [ ] Add bounded authenticated GitHub merge, post-merge observation, and exact-write reconciliation implementation using frozen provider contracts.
- [ ] Add post-merge controller verification and emit `READY_FOR_MERGE -> MERGED` only after durable strategy-aware verification evidence.
- [ ] Add merge/squash/rebase, moved-base/head, ambiguous-write, wrong-tree/lineage, and provider-boundary adversarial tests.
- [ ] Reconcile EP-005 architecture/status documentation without claiming later review/merge evidence beyond the immutable cutoff.
- [ ] Run complete deterministic acceptance and exact-head review handoff; commit only Task 2.

## Completion gate
EP-005 is complete only after both tasks are independently accepted, exact final implementation head receives fresh 0 Critical / 0 Major review, implementation PR is merged at that exact reviewed head, post-merge acceptance/reconciliation evidence is materialized, and `CURRENT_STATE.md`, `PROGRESS.md`, `AUDIT_INDEX.md` are reconciled through the legally recordable cutoff.

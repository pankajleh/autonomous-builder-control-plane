# EP-005 — Merge Authorization Task 1: Lifecycle Contract Corrections

## Authority

This bounded implementation task is an exact subset of `docs/plans/ep-005-merge-authorization-and-post-merge.md` at design head `44a6aa1d976962d363ccc88214b2fa8653dd3274`. The final independent xhigh design re-review returned `DESIGN_ACCEPTED`, 0 Critical / 0 Major; review artifact SHA-256 is `e6053debdf70b24c69fa41664910fd0919ce9b9b6e482ced0a4b091f46f346a1`.

Task 1 may amend only the network-free lifecycle contracts/tests and the foundation contract document needed by the accepted design. It must not implement controller execution, live GitHub HTTP/GraphQL behavior, projection reconciliation, or Task 2/3 semantics.

## Invariants

- Preserve the full accepted design at `44a6aa1d976962d363ccc88214b2fa8653dd3274`; do not weaken any authority, exact-head/base, policy, pagination, PR eligibility, ambiguous-write, cancellation, post-merge, cleanup, or resource-bound invariant.
- `internal/githublifecycle` remains network-free in Task 1: no live HTTP client/provider implementation and no ordinary PR merge or REST ref-update mutation.
- Generic squash/rebase representation may remain in foundation contracts, but production support must not be claimed.
- Task 2 controller/runtime and Task 3 live provider remain out of scope until this exact Task-1 candidate is independently accepted and reviewed.
- `CURRENT_STATE.md`, `PROGRESS.md`, and `AUDIT_INDEX.md` are not updated in this operation.

## Implementation

- [ ] Add full READY/repository/policy authority bindings and strict canonical recovery; bind them through `Authority`, `MergeInput`, attempt identity, results, and validation.
- [ ] Add authoritative open/non-draft/unmerged PR snapshots, trusted check producer/app/context, stable reviewer identities, conservative exact-head dismissal handling, and independent per-source pagination-closure contracts while preserving unordered review semantics.
- [ ] Add deterministic merge-commit recipe/expected result OID and full-input reconciliation contracts; require applied reconciliation to materialize a validated `MergeResult`.
- [ ] Add immutable `AuthorizationSealV1` / `SealedMergeAuthorizationV1` contracts and frozen `github-update-refs-atomic-base-head-v1` capability binding without claiming live provider support.
- [ ] Add immutable strict-canonical `CancellationAuthorityV1`, independent validation, exact READY/attempt/write/request-boundary/principal/policy/source-evidence binding, and require its prior durable validated form for `CANCELLED` authority.
- [ ] Amend post-merge observation/verification for exact result-object proof plus equal-or-descendant target containment.
- [ ] Update `docs/architecture/GITHUB_LIFECYCLE_FOUNDATION_CONTRACT.md` and contract tests only for these narrow amendments.
- [ ] Add adversarial and boundary tests required by the accepted master design for all Task-1-owned contract behavior.
- [ ] Run gofmt, vet, package tests, race tests, full repository tests, non-Linux compile-only validation, scope/forbidden-semantic checks, and `git diff --check`.
- [ ] Commit the implementation and move this plan to `docs/plans/completed/` only after all Task-1 work/tests are complete. Do not claim deterministic ABCP acceptance or independent exact-head review inside the implementation task.

## Completion boundary

Ralphex completion is only implementation completion. Publication, Task 2, Task 3, merge, post-merge lifecycle execution, and EP-005 completion remain blocked until ABCP deterministic acceptance and a fresh exact-head Critical/Major review independently accept this Task-1 candidate.

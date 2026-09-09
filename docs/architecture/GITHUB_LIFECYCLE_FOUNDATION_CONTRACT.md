# GitHub Lifecycle Foundation Contract

## Scope

`internal/githublifecycle` is the frozen, network-free contract boundary for
EP-005. It defines what PR, CI, merge-authorization, reconciliation, and
post-merge tracks may exchange with a provider. It does not implement a
provider, make a network request, hold a credential, execute a GitHub command,
perform a merge, or emit a state transition.

The frozen `github-update-refs-atomic-base-head-v1` value is a capability
contract, not a claim that a live GitHub deployment supports it. Live
capability proof and transport remain Task 3. The ordinary PR merge endpoint
and REST ref-update mutation are outside this foundation.

## Authority and Phase-3 READY provenance

An immutable `Authority` binds exact case-sensitive repository, base/head
branches, accepted head, pre-merge base tip, optional stable PR identity,
acting identity, `ExpectedMergeContent`, `ReadyAuthorityBindingV1`, and
controller-owned `MergePolicyV1` values.

`ExpectedMergeContent` is content proof, not merge authority. Its controller
derivation verifies exact immutable Phase-3 `READY_FOR_MERGE` evidence and
resolves the integrated commit tree with a pinned local Git executable under
replacement-ref-resistant and no-lazy-fetch settings.

`ReadyAuthorityBindingV1` binds:

- the exact canonical Phase-3 authority bytes/digest, including run,
  repository, plan, and policy identities;
- every accepted source project/plan/run/attempt, repository/branch/start/head,
  acceptance policy, and evidence closure;
- the stable `RepositoryBindingV1`;
- exact canonical `INTEGRATION_ACCEPTED -> READY_FOR_MERGE` ledger event bytes,
  digest, IDs, time, physical ledger identity/offset, run-state sequence, and
  transition ordinal;
- the bounded ledger-prefix identity and complete READY evidence closure; and
- the integrated head, baseline, expected tree, and exact READY decision ref
  used by `ExpectedMergeContent`.

`RepositoryBindingV1` binds Phase-3 repository identity/path/canonical remote/
start SHA to exact GitHub owner/name and stable repository node/database IDs,
under immutable mapping-configuration evidence. Fork or mutable display text
is never an equivalent repository identity.

`PolicyAuthorityBindingV1` binds immutable configuration evidence, repository,
Phase-3 authority, READY binding, and exact stable acting principal.
`MergePolicyV1` additionally binds method, trusted required checks, stable
eligible/required reviewers, minimum unique approvals, and deterministic
commit-recipe policy. Authority method and actor must equal policy-derived
values; request values cannot select them.

Compatibility authorities retained for the already-frozen PR/CI observation
tracks cannot create a `MergeInput`. Merge admission requires the complete
READY/repository/policy chain.

Strict parsers re-run ordinary constructors for repository, READY, policy,
authority, expected content, and every nested durable merge value. They accept
only byte-identical canonical JSON. Unknown or duplicate fields, whitespace,
key reordering, omitted/defaulted values, altered nested bytes, digest
disagreement, and invalid constructor inputs fail closed.

## Stable PR, check, review, and pagination evidence

`AuthoritativePullRequestSnapshotV1` requires concrete `OPEN`,
`isDraft=false`, `merged=false`, and absent `mergedAt` observations together
with stable repository and PR node/database identities, exact same-repository
full base/head refs and OIDs, API version, authenticated acting principal,
provider request identity, response digest, evidence, limits, and independently
validated reviews closure. Closed, draft, merged, contradictory, missing,
synthetic/deleted-ref, or fork-head observations are invalid.

Every `Check` has a `TrustedCheckIdentityV1`: exact context, source kind
(`check_run` or `commit_status`), stable producer identity, and stable GitHub
App identity when applicable. Name-only, mutable-login, wrong-context/App,
missing-producer, stale-head, duplicate, pending, or non-success observations
cannot satisfy a required check.

Every `Review` has stable review and reviewer node/database identities, state,
and exact reviewed commit. Sorting is deterministic set ordering only and
never chronology. At most one exact-head approval counts per eligible
reviewer. Any eligible exact-head `changes_requested` blocks, including beside
an approval. Every eligible exact-head `dismissed` record blocks as unproved
history. This foundation exposes no positive GitHub-v1 authenticated review
history capability. Stale-head, commented, and ineligible reviews grant no
approval.

`PaginationClosureV1` is independent for reviews, check runs, and commit
statuses. Each closure binds its exact source/query/filters, ordered page or
cursor requests, provider request/body identities, canonical response
envelopes, stable item keys/digests, closure-wide set digest, evidence, and
limits. REST termination comes only from a valid final `Link` relation set
without `next`; GraphQL termination comes only from final
`hasNextPage=false`. Missing, skipped, repeated, reordered, duplicate,
conflicting, altered, invented-terminal, or truncated chains fail.
`ValidatePaginationClosureV1` reconstructs the chain and exact canonical item
set rather than trusting collector summaries.

All immutable values deep-copy slices, maps, nested bytes, and optional values;
accessors return defensive copies. Canonical set ordering never changes the
semantic ordering of Git parents, lineage, or pagination chains.

## Deterministic merge input

Production-v1 merge admission is method `merge` only.
`MergeCommitRecipeV1` binds exact repository/full target ref, expected tree,
ordered `[base, head]` parents, deterministic message and attempt trailer,
author/committer identity and timestamps, object format, write/authority/
policy/READY/limits identities, canonical Git commit bytes, and locally
computed result OID. Changing any recipe field cannot retain the old OID.

`MergeInput` owns the full authority, initial authoritative PR and policy
decision, canonical checks, all three independently validated source closures,
frozen provider capability, exact recipe/result OID, evidence, limits, and
attempt. A merge `WriteAttempt` explicitly binds READY and policy digests in
addition to repository, principal, operation, write ID, authority digest,
payload digest, and limits. `ParseCanonicalMergeInput` must reconstruct
byte-identical input and attempt identities.

Generic squash/rebase lineage remains representable by foundation strategy
contracts. That representation and its tests do not claim production support.

## Authorization seal and exact target commitment

`AuthorizationSealV1` binds one unchanged `MergeInput` to freshly validated
final PR/review/check observations, exact authorized verdict and PR
eligibility, base/head refs and OIDs, recipe/result, capability, cumulative
counters, evidence, limits, and the fact that no target request was attempted.
It is immutable and one-use.

`ProviderCapabilityV1` accepts only
`github-update-refs-atomic-base-head-v1` with immutable evidence for atomic,
all-or-nothing, no-op, base-then-head ordering, and `force=false` behavior.
`TargetRefCommitmentV1` contains exactly two ordered entries:

1. base: `before=ExpectedBaseTip`, `after=ExpectedResult`, `force=false`;
2. head: `before=AcceptedHead`, `after=AcceptedHead`, `force=false`.

`SealedMergeAuthorizationV1` owns the original input, seal, and exact
commitment. Merge execution accepts this sealed value, never an unsealed
input. Strict parsers recover the seal, commitment, and sealed chain by all
nested canonical bytes and digests.

## Exact reconciliation

Merge reconciliation owns the full `SealedMergeAuthorizationV1`, not an
attempt alone. `APPLIED` requires the identical materialized and validated
`MergeResult`. `NOT_APPLIED` requires exact typed zero-request-byte or
authenticated all-or-nothing base/head before-OID rejection proof. `UNKNOWN`
contains no result or non-application claim. Absent/open PR, old/unrelated
target, missing expected object, truncation, or generic provider assertions
remain `UNKNOWN`.

`ValidateReconciliationResult` checks the full input, seal, commitment,
attempt, result/proof, evidence, and limits. Merge mutations have no implicit
retry right even after `NOT_APPLIED`; future retry authority requires a
separately versioned controller contract. Existing non-merge retries retain
their independently bounded rules.

## Cancellation authority

`CancellationAuthorityV1` is immutable strict-canonical controller authority.
It binds project/plan/run, repository and Phase-3 authority, exact READY event/
digest/sequence and ledger prefix, current-READY proof, a closed requested-at
boundary and controller receipt ordering, every reached admission/attempt/
write/seal/commitment identity, stable authenticated requester, controller
cancellation policy/source/grant/allow decision, unique source request,
ingress identity, exact submission proof, evidence closure, and limits.

Boundary construction enforces exact absence/presence for `PRE_ADMISSION`,
`ADMITTED_PRE_TARGET_SUBMISSION`, `TARGET_SUBMISSION_UNKNOWN`, and
`TARGET_NOT_APPLIED`. Independent validation receives expected READY, policy,
principal, boundary, attempt/write/seal/commitment, source/ingress ordering,
and proof identities.

Raw context cancellation, deadline, signal, disconnect, mutable login, caller
boolean, or merge credential is never cancellation authority.
`DurableCancellationAuthorityV1` additionally binds validated fsynced channel
and single-use replay-index evidence. Only this prior durable form may
authorize `CANCELLED`; submitted boundaries also need exact typed
`NOT_APPLIED` proof. `APPLIED` always defeats cancellation.

## Post-merge result and containment

The exact result object is independent of the moving target ref.
`ResultCommitObservationV1` proves result OID/tree/ordered parents/message/
author/committer/timestamps and recipe digest. `TargetContainmentProofV1`
separately binds repository/full target ref, result and observed-tip OIDs,
provider request identity, proof mechanism, merge base, bounded descendant
distance, evidence, and limits.

`github-compare-v1` accepts only `identical`, or `ahead` with merge base equal
to the exact result and distance within the configured bound.
`VerifyPostMerge` validates the original sealed authorization, merge result,
exact result object, and equal-or-descendant containment. Wrong tree/OID/
parents/recipe, sibling, rewind, force move, wrong repository/ref, excessive
distance, unavailable ancestry, or truncation fails closed.

Foundation strategy proof still distinguishes accepted head and synthesized
result identities: merge requires ordered base/head parents; squash binds one
base parent and exact content lineage; rebase binds a continuous ordered
first-parent lineage. No strategy may substitute provider claims for the
controller-derived expected tree.

## Limits and failure semantics

`Limits` deterministically binds numeric page, per-page, total-item, text,
evidence, metadata, parent, lineage, pagination-closure-byte, descendant-
distance, call-time, and retry bounds. Constructor and independent-validator
rescans use the controller's exact limits digest. Exact `limit` is accepted;
`limit+1` fails.

Provider unavailability, ambiguous possible writes, policy/CI/review failure,
and invalid evidence remain distinct. Cancellation or deadline after possible
request bytes is ambiguous, not `CANCELLED`. No substantive failure retries.
No caller URL, shell fragment, credential, or free-form provider authority is
part of these network-free contracts.

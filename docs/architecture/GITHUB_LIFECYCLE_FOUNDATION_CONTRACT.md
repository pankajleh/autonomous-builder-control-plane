# GitHub Lifecycle Foundation Contract

## Scope

`internal/githublifecycle` is the frozen, network-free contract boundary for
EP-005. It defines what later PR, CI, merge-authorization, and post-merge tracks
may exchange with a provider. It does not implement a provider, make network
requests, execute GitHub CLI commands, hold credentials, perform a merge, or
emit a state transition.

## Authority

An immutable `Authority` binds these exact, case-sensitive values:

- repository owner and name;
- base and head branch;
- accepted head SHA;
- expected pre-merge base-tip SHA;
- optional pull-request number plus node ID;
- the one allowed merge method (`merge`, `squash`, or `rebase`); and
- the authenticated user or app-installation identity that may perform a
  remote write; and
- controller-owned `ExpectedMergeContent` for the accepted head/base pair.

`ExpectedMergeContent` can only be produced by the controller derivation
boundary. `DeriveExpectedMergeContent` verifies an immutable
`serial-integration-gate-decision` whose state is `READY_FOR_MERGE` and whose
combined-acceptance target binds the exact Phase 3 integrated head and
baseline. It then resolves that exact local commit to its tree using an
explicit, pinned Git executable under `--no-replace-objects` and the governed
Git environment. The frozen object records `phase3-ready-tree-v1`, the source
integrated-head and baseline SHAs, the complete source evidence reference, the
canonical Git executable path/version/binary SHA-256, and the expected result
tree SHA. Missing, altered, wrong-kind, wrong-state, wrong-head, wrong-baseline,
or unverifiable evidence blocks authority construction.

All constructors preserve spelling and case. They do not trim, fold case, or
turn distinct inputs into equal identities. Git object IDs must be complete
lowercase 40- or 64-hex values. Repository and branch constructors reject
path, ref, revision-expression, control-character, and traversal ambiguity.

The acting identity is deliberately non-secret. It can contain an authenticated
subject and app installation number, but the contract has no credential,
password, private key, or token field. `Authority.CanonicalJSON` is therefore
safe to bind into evidence, subject to the caller's normal identity-data policy.

## Provider boundary

The `Provider` interface accepts `context.Context` on every operation and only
structured inputs. No operation accepts a shell command, API URL, query string,
or free-form authority fragment. The interface freezes reads for PR and CI
observations, writes for PR upsert and merge, post-merge observation, and
explicit write reconciliation.

Every PR or merge write input contains one immutable `WriteAttempt`, created
while constructing the exact request and before submission. The attempt binds
the repository, acting identity, typed operation kind (`pr_upsert` or `merge`),
write ID, authority SHA-256, canonical operation-payload SHA-256, and limits
policy SHA-256. The payload digest covers the complete operation payload but
excludes the attempt itself, avoiding circular hashing. Successful results,
execution errors, reconciliation inputs, and reconciliation results carry that
same value. Result validators compare it with the original request; providers
cannot invent or replace it.

A provider implementation must authenticate the bound principal out of band.
Deadlines are bounded by `Limits.CallTimeout`; provider implementations must
return no collection or text beyond the supplied limits.

## Immutable remote evidence

`PullRequestSnapshot`, `CISnapshot`, `PullRequestPage`, `MergeResult`, and
`PostMergeObservation` bind the exact limits-policy SHA-256 and are built only
through validating constructors. They:

- deep-copy caller slices and maps;
- expose defensive copies;
- reject duplicate identities and oversized text, item, evidence, metadata,
  parent, and lineage collections;
- sort set-like reviews, checks, and evidence references;
- preserve ordered Git parent and rewritten-commit lineage sequences; and
- expose stable canonical JSON and its lowercase SHA-256 digest.

Remote bodies, logs, and arbitrary response payloads are never embedded. Large
material belongs in immutable bounded artifacts referenced by
`ledger.EvidenceRef`.

Before policy evaluation, consumers pass the controller's expected `Limits` to
`ValidatePullRequest`, `ValidateCI`, `ValidatePullRequestWriteResult`,
`ValidateMergeResult`, `VerifyPostMerge`, or `SelectPullRequest`. Each requires
the embedded limits identity to match and independently reconstructs/rescans
the actual text, nested evidence, metadata, collection, parent, and lineage
data under those limits. A result constructed under a looser provider policy
cannot be accepted by a stricter controller merely by presenting a plausible
snapshot digest. PR discovery uses `PullRequestPage` and `SelectPullRequest`;
absence, repeated identity, or multiple matching PRs is an error rather than a
provider-dependent choice.

## Strategy-aware merge proof

The accepted head commit and provider-created result commit are separate
identities. `MergeInput`, `MergeResult`, and `PostMergeObservation` all bind the
authority-owned expected-content object. For every merge method, both the
accepted-head tree observation and final result tree must equal the exact
Phase-3-derived expected tree; provider parent/lineage claims are provenance,
never content authority. `VerifyPostMerge` also requires agreement among the
original merge request, result, and target-branch observation for repository,
branch, PR, actor, write attempt, accepted head and tree, base-before SHA,
method, result/base-after SHA, result tree, ordered parents, and rewritten
lineage.

- `merge` requires ordered base-before and accepted-head parents.
- `squash` requires the base-before parent and one entry binding the accepted
  head/tree to the synthesized result SHA/tree.
- `rebase` requires an ordered, continuous first-parent chain whose final entry
  binds the accepted head/tree to the observed result SHA/tree.

Consequently, a correct synthesized SHA is accepted only with its exact
method-specific content/lineage proof. Equality between post-merge SHA and the
accepted head SHA is never assumed.

## Failure and retry rules

`OperationError` keeps provider unavailability and ambiguous remote execution
separate from substantive policy, CI, review, and invalid-remote-evidence
failures. A write error after possible submission is always
`FailureAmbiguousWrite`, including cancellation and deadline expiry.

`CanRetry` grants no implicit replay:

- substantive failures are never retried;
- unavailable reads and known-not-submitted writes are limited separately;
- ambiguous writes have zero retry authority by default; and
- an ambiguous write can enter the normal bounded write-retry allowance only
  when the error still contains the request's exact attempt and reconciliation
  proves that exact repository/actor/operation/write-ID/authority/payload tuple
  was not applied.

An applied, unknown, missing, or identity-mismatched reconciliation never
authorizes retry.

## Foundation limits

`DefaultLimits` bounds remote pages, items per page, total items, UTF-8 text,
evidence references, metadata, parents, rewritten lineage, call duration, and
read/write retries. `Limits.CanonicalJSON` is a versioned deterministic encoding
of every governed field (duration is nanoseconds), and `Limits.SHA256` is its
policy identity. Read/write inputs and all provider-returned snapshots/results
carry that identity. `MaxAmbiguousRetries` is invariantly zero. Later tracks may
choose stricter validated limits but must not bypass constructors, validator
rescans, or interpret truncation as successful evidence.

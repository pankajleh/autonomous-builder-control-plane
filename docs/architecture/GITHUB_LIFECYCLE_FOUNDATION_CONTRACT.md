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
  transition ordinal, with actor exactly `controller` and source exactly
  `integration-gate`;
- the bounded ledger-prefix identity and complete READY evidence closure; and
- the integrated head, baseline, expected tree, and exact READY decision ref
  used by `ExpectedMergeContent`.

The READY event must end exactly at the bound JSONL prefix boundary (including
its newline), and its run-state sequence must equal its transition ordinal.
The closure must contain the event evidence, repository-mapping evidence, and
every accepted source's acceptance evidence. A source for the integrated head
must be present. `CurrentReadyProofV1` independently re-parses the READY
binding, retains the exact bounded ledger JSONL bytes and matching evidence,
recomputes the bound-prefix and full-observation digests, verifies the READY
event at its exact byte offset, and rejects any later transition for the same
run. A caller-provided no-later-transition summary is never sufficient.

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
provider request identity, response digest, body evidence, and retained response-
envelope evidence binding that request/body pair to the authenticated acting
principal and decoded PR fields, limits,
and independently validated reviews closure. Closed, draft, merged, contradictory, missing,
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
statuses. Consumers pass only `PaginationQueryScopeV1`; the source-specific
GET method, owner/name path, fixed API version, exact-head/PR identities,
filters, and page size are independently produced by
`DerivePaginationQueryV1`. Collectors cannot supply these authority fields.
Each closure binds its exact derived source/query/filters, ordered page or
cursor requests, unique provider request/body identities, canonical response
envelopes, stable item keys/digests, closure-wide set digest, evidence, and
limits. Every page requires a GitHub response identity, binds a retained body
evidence ref whose digest equals the raw-body digest, and binds a second
retained response-envelope evidence ref covering the response identity,
decoded items, and exact REST/GraphQL pagination fields. The closure evidence
contains both refs for every page. REST
termination comes only from an explicitly observed valid final `Link`
relation set (including an explicitly observed absent header) without
`next`; an empty default string is not observation. GraphQL termination comes
only from final `hasNextPage=false`. Missing, skipped, repeated, reordered, duplicate,
conflicting, altered, invented-terminal, or truncated chains fail. Nonterminal
pages must contain the frozen page size, GraphQL cursors must advance without
reuse, and every observed REST `last` relation must remain consistent with the
actual terminal page.
`ValidatePaginationClosureV1` reconstructs the chain and exact canonical item
set against the authority-derived query rather than trusting collector
summaries. Review/check sets are canonicalized only after this closure
validation; sorting never supplies completeness or chronology.

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

`NewMergeCommitRecipeV1` accepts only the authority and write ID. Repository,
ref, tree, parents, message, trailer value, author, committer, READY-derived
timestamp, object format, and authority/policy/READY digests are all derived;
there are no caller recipe bytes to adopt. Every tree/parent/result OID must
have the width selected by the object format. Production-v1 admission
additionally requires the proved SHA-1 format; SHA-256 remains contract-only.

`MergeInput` owns the full authority, initial authoritative PR and policy
decision, canonical checks, all three independently validated source closures,
frozen provider capability, exact recipe/result OID, evidence, limits, and
attempt. A merge `WriteAttempt` explicitly binds READY and policy digests in
addition to repository, principal, operation, write ID, authority digest,
payload digest, and limits. `ParseCanonicalMergeInput` must reconstruct
byte-identical input and attempt identities.

`GenericStrategyResultV1` and `GenericStrategyPostMergeV1` preserve
network-free squash/rebase result, lineage, and containment representation
without a production seal or recipe. Production policy, `MergeInput`, and
sealed execution remain merge-only. Generic representation and its tests do
not claim executable production support.

## Authorization seal and exact target commitment

`FinalRevalidationV1` is a distinct controller-ordered record created after
a typed current-READY proof. It owns fresh PR, review, check-run, and
commit-status request identities, rejects every admission request identity and
cross-source duplicate, bounds all response times to its start/completion
interval, independently evaluates policy, closes all authority-bearing
response evidence, and recomputes `final-authorization-decision-v1` canonical
bytes and digest. A caller decision digest is never accepted.

`AuthorizationSealV1` binds one unchanged `MergeInput` to the full canonical
`FinalRevalidationV1` bytes/digest and independently derived final decision
digest, exact authorized verdict and PR eligibility, base/head refs and OIDs,
recipe/result, capability, cumulative counters, evidence, limits, and the fact
that no target request was attempted. Reusing admission observations or
mutating an in-memory nested record while retaining its old canonical bytes
fails independent reconstruction. The seal is immutable and one-use.

`ProviderCapabilityV1` accepts only
`github-update-refs-atomic-base-head-v1` with immutable evidence for atomic,
all-or-nothing, no-op, base-then-head ordering, and `force=false` behavior.
`TargetRefCommitmentV1` contains exactly two ordered entries:

1. base: `before=ExpectedBaseTip`, `after=ExpectedResult`, `force=false`;
2. head: `before=AcceptedHead`, `after=AcceptedHead`, `force=false`.

Its `clientMutationID` is exactly the bound `WriteAttempt.WriteID`; the
constructor has no independent caller mutation-ID argument. Reconstructing one
seal therefore produces byte-identical commitment and mutation identity.

`SealedMergeAuthorizationV1` owns the original input, seal, and exact
commitment. Merge execution accepts this sealed value, never an unsealed
input. Strict parsers recover the seal, commitment, and sealed chain by all
nested canonical bytes and digests.

## Exact reconciliation

Merge reconciliation owns the full `SealedMergeAuthorizationV1`, not an
attempt alone. `APPLIED` requires the identical materialized and validated
`MergeResult`. Every disposition also binds the strict-canonical
`TargetSubmissionV1` published for the one transport invocation: the exact
sealed authorization, seal, commitment, write/mutation identity, request ID,
canonical request body/digest/length, and limits. The reconciliation or
cancellation submission proof separately binds the transport-observed request-
byte count to that exact submission record.
`NOT_APPLIED` requires strict-canonical
`NotAppliedProofV1`: either exact zero-request-byte evidence or authenticated
all-or-nothing base/head before-OID rejection. The proof derives and binds the
repository/node identity, both ordered ref updates and OIDs, rejected
predicate, capability, seal/commitment/write/mutation identities,
request/response identity and bodies, raw evidence digest/ref, and the fixed
all-or-nothing disposition. Atomic rejection proof construction requires the
response request ID to match the submitted request, retains and strict-parses
the bounded canonical response body, derives the rejected ref-update index
from its frozen machine-readable error code, and requires the raw evidence
digest to equal the response-body digest. Its evidence must also be in reconciliation
closure. `UNKNOWN`
contains no result or non-application claim. Absent/open PR, old/unrelated
target, missing expected object, truncation, or generic provider assertions
remain `UNKNOWN`.

`ValidateReconciliationResult` checks the full input, seal, commitment,
target submission, attempt, result/proof, evidence, and limits. Merge mutations have no implicit
retry right even after `NOT_APPLIED`; future retry authority requires a
separately versioned controller contract. Existing non-merge retries retain
their independently bounded rules.

## Cancellation authority

`CancellationAuthorityV1` is immutable strict-canonical controller authority.
It binds project/plan/run, repository and Phase-3 authority, exact READY event/
digest/sequence and ledger prefix, the full independently revalidated canonical
current-READY proof rather than a caller digest, a closed requested-at
boundary and controller receipt ordering, every reached admission/attempt/
write/seal/commitment identity, stable authenticated requester, controller
cancellation policy/source/grant/allow decision, unique source request,
ingress identity, exact submission proof, evidence closure, and limits.

The submission proof is the full canonical typed
`CancellationSubmissionProofV1`, not a caller kind/digest pair. Independent
expectations cover every authority-bearing field: current READY and admission,
principal/authentication, policy version/source/digest, grant/allow decision,
boundary/attempt/seal/commitment, the exact `TargetSubmissionV1` for submitted
boundaries, exact submission proof, source kind/request
and evidence, ingress ordering, and evidence closure.

Boundary construction enforces exact absence/presence for `PRE_ADMISSION`,
`ADMITTED_PRE_TARGET_SUBMISSION`, `TARGET_SUBMISSION_UNKNOWN`, and
`TARGET_NOT_APPLIED`, including a sealed zero-request-byte form for a published
seal/commitment whose transport provably wrote no request bytes. Independent validation receives expected READY, policy,
principal, boundary, attempt/write/seal/commitment, source/ingress ordering,
and proof identities.

Raw context cancellation, deadline, signal, disconnect, mutable login, caller
boolean, or merge credential is never cancellation authority.
`CancellationReplayIdentityV1` derives the replay key exclusively from
`(source_kind, source_request_id)` and binds it to the exact authority ID and
fsynced index evidence. `DurableCancellationAuthorityV1` owns its canonical
bytes/digest. Replay identity is independently reconstructed before
`CANCELLED` selection. Only this prior durable form may
authorize `CANCELLED`; submitted boundaries also need exact typed
`NOT_APPLIED` proof; `TARGET_NOT_APPLIED` must contain that byte-identical
proof in its submission record. Pre-submit selection requires its bound
no-admission/zero-byte `NOT_APPLIED` disposition; `UNKNOWN` never selects a
terminal state. `APPLIED` always defeats cancellation.

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

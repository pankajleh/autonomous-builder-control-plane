# EP-005 — Merge authorization, atomic exact-base/exact-head commitment, and post-merge acceptance

## Roadmap authority and scope

This plan implements only the remaining Phase-4 work after PR #15: controller-owned merge policy, exact-head/exact-base merge execution, and post-merge acceptance. It may make the narrow, explicitly listed amendments to the otherwise frozen `internal/githublifecycle` contracts that are required to close the design findings below. It does not change Phase-3 scheduling or integration semantics.

Production v1 supports only the `merge` method. `squash` and `rebase` remain representable by the network-free foundation contracts, but the production controller/provider must reject them before admission. Contract representation and contract tests are not evidence that either method is implemented.

Phase 5 service/API/dashboard work, deployment and production acceptance, multi-host workers, secret brokering, retention automation, and every Phase 5+ concern remain out of scope.

## Previously corrected properties that remain normative

| Finding | Root-cause correction in this plan |
| --- | --- |
| Prior C-01 — READY authority replayable across repositories/runs | Keep the immutable `ReadyAuthorityBindingV1` covering the exact Phase-3 authority, repository mapping, run/plan/source-attempt identities, exact durable `INTEGRATION_ACCEPTED -> READY_FOR_MERGE` event and sequence, complete verified evidence closure, and proof that READY is still the run's current durable state. Expected tree content alone grants no merge authority. |
| Prior C-02 — preconditions not atomic with remote mutation | Keep the ban on the ordinary pull-request merge endpoint and REST ref-update simulation. Tighten the exact-ref primitive to the two-ref, all-or-nothing base update plus head no-op CAS defined in Section 3. |
| Prior M-01 — policy identity has no governing authority | Keep policy controller-owned, bind its source evidence and digest into lifecycle `Authority`, and reject caller-selected policy or method values. |
| Prior M-02 — check/review provenance and ordering insufficient | Keep trusted check producer/app/context and stable reviewer identities and the ban on invented chronology. Tighten dismissed-review handling and independently verifiable pagination as defined below. |
| Prior M-03 — exact merge cannot be reconstructed for reconciliation | Keep canonical, strictly rehydratable `MergeInput` by exact attempt. Reconciliation consumes the full input and an applied result must materialize the identical validated `MergeResult`; open/absent PR or absent result in a bounded observation remains `UNKNOWN`, not `NOT_APPLIED`. |
| Prior M-04 — durable crash behavior missing | Keep immutable admission, observation, prepare, commitment, result, verification, terminal, and deterministic-ledger-event records, including append-succeeded-but-returned-error recovery. Extend the same protocol to every negative terminal outcome. |
| Prior M-05 — target advancement prevents valid post-merge proof | Keep descendant-aware post-merge proof: the target may equal the result commit or be a proved descendant, while the exact result object must still match tree and ordered parents and any non-descendant/force move fails closed. |
| Prior M-06 — bounds are non-numeric | Keep numeric production limits, durable cumulative counters, and exact `limit`/`limit+1` tests; add numeric pagination and temporary-storage limits. |
| Prior M-07 — transport/submission semantics incomplete | Keep the pinned HTTPS origin/endpoints, disabled redirects and implicit mutation retries, sealed credentials, bounded headers and compressed/decompressed bodies, deterministic response mapping, body closure, safe request IDs, and zero-request-byte proof requirement for `submitted=false`. |

## Fresh design re-review closure summary

| Finding | Root-cause correction in this plan |
| --- | --- |
| C-01 — base-only CAS leaves head and other predicates revocable | Use one documented all-or-nothing GitHub `updateRefs` mutation containing a base update CAS and a same-repository head no-op CAS, both with exact `beforeOid`/`afterOid` and `force=false`. Final PR eligibility, reviews, checks, policy, principal, and READY facts become immutable controller authorization only at the durable `AuthorizationSealV1` linearization point. Missing two-ref/no-op/all-or-nothing provider capability fails closed before admission. |
| M-01 — dismissed exact-head review can hide former changes requested | Every eligible exact-head `dismissed` review is a blocking `REVIEW_HISTORY_UNPROVEN` observation unless a complete authenticated history proof passes independent validation. The frozen GitHub v1 provider has no accepted proof source, so every such dismissal blocks. |
| M-02 — pagination completion is not independently verifiable | Require one canonical bounded `PaginationClosureV1` per reviews, check-runs, and commit-statuses source, binding the exact query and page/cursor chain, response identities/digests, item uniqueness, and source-specific terminal proof. Independent validators reconstruct and reject incomplete, inconsistent, duplicate, or truncated closures. |
| M-03 — pull-request eligibility is implicit | Add `AuthoritativePullRequestSnapshotV1` and require the exact bound PR to be open, non-draft, and unmerged during authorization and again in final revalidation immediately before the authorization seal. |
| M-04 — negative terminal paths lack exact legal outcomes | Map every classified outcome from a proved current `READY_FOR_MERGE` state to exactly `MERGED`, `FAILED`, or `CANCELLED`, with a reason code and the same immutable terminal/event fsync append-or-verify protocol. Only a genuinely `UNKNOWN` target-ref submission may remain stably `READY_FOR_MERGE`. |
| M-05 — temporary-storage cleanup is unspecified | Use a bounded controller-owned per-attempt `tmp/` namespace, reserve and count temporary plus published files/bytes before creation, and apply the cleanup/recovery matrix for every completion/failure boundary. Unsafe or unclassifiable leftovers fail closed. |

## 1. Full Phase-3 READY authority

### `ReadyAuthorityBindingV1`

`ExpectedMergeContent` proves only accepted content. It is necessary but insufficient authorization for a remote merge. Before constructing lifecycle `Authority`, the controller must create an immutable `ReadyAuthorityBindingV1` with canonical JSON and SHA-256 over all of the following:

- the exact Phase-3 `authority.Authority` canonical bytes and SHA-256, including its `RunID`, canonical repository manifest identity/path/remotes/start SHA, exact plan path and plan SHA-256, and policy version;
- every accepted source candidate identity carried by the verified gate input: project ID, plan ID, run ID, attempt ID, repository, branch, start SHA, accepted head SHA, acceptance-policy identity, and acceptance-evidence refs; an empty optional identity is allowed only when the upstream schema proves it is inapplicable, never when parsing lost it;
- a controller-owned `RepositoryBindingV1`: Phase-3 repository identity and canonical remote, exact GitHub owner/name plus stable repository node/database identity, and the immutable configuration ref/digest that authorizes that mapping;
- the exact READY transition's canonical `ledger.Event` bytes and SHA-256, `EventID`, timestamp, physical ledger identity, byte offset, and run-state sequence number; the event must have the same run/plan/attempt identities wherever those fields are applicable, `StateFrom=INTEGRATION_ACCEPTED`, `StateTo=READY_FOR_MERGE`, `EventType=STATE_TRANSITION`, and the expected controller actor/source;
- the exact ordered run-state sequence through that event, represented by its bounded ledger-prefix byte length/SHA-256 and the READY event's ordinal among that run's state transitions;
- the READY event's exact evidence refs, the READY decision ref, the verified gate-input/combined-acceptance/review/source-head/transitive evidence closure, and a canonical closure digest; and
- the derived integrated head, baseline, and tree identities, which must agree with `ExpectedMergeContent` byte-for-byte and by digest.

Authority assembly must bounded-read the controller-selected canonical ledger through one no-follow regular-file descriptor, validate every event and per-run transition in physical order, reject duplicate/conflicting event IDs, verify the full READY evidence closure by bytes and digest, and require the exact bound READY event to be the latest durable state transition for that run. The caller may identify a run to process but may not provide the event, repository binding, state, method, or authority digest as trusted values.

Immediately before creating `AuthorizationSealV1`, the controller must reacquire/retain the shared run-transition and repository/base locks, re-read a bounded stable ledger snapshot, require the previously bound prefix to be unchanged, and prove there is no later state transition for the run. On every synchronous settled path it holds the run-transition lock through result/failure/cancellation evidence, cleanup, terminal persistence, and the exact `MERGED`, `FAILED`, or `CANCELLED` append. Before releasing that lock on an ambiguous target-submission path, it persists an unresolved-submission barrier that every state-transition writer must honor; no competing failure/cancellation transition may make an applied merge permanently unrecordable. Recovery reacquires the same lock, resolves or preserves the barrier, and again requires READY as the current durable state before the predetermined terminal append. Thus a cancellation/failure transition cannot race between the final READY-state proof and the governed merge outcome. A missing, truncated, rewritten, ambiguous, superseded, or non-READY ledger state is `STALE_READY_FOR_MERGE_AUTHORITY` and permits no target-ref write.

### Narrow lifecycle authority amendments

Amend `internal/githublifecycle` rather than weakening the consumer around it:

- Add immutable, copy-safe `ReadyAuthorityBindingV1`, `RepositoryBindingV1`, and their validating strict canonical parsers.
- Add the READY binding and its digest to `AuthorityInput`, `Authority.CanonicalJSON`, `Authority.SHA256`, and all equality/revalidation paths. Require repository, head, base, expected content, and Phase-3 authority identities to agree.
- Require `ExpectedMergeContent.SourceIntegrationEvidence()` to be the exact READY decision ref included by the bound READY transition; it remains content derivation, not a substitute for the READY binding.
- Add a controller-only authority assembly path. Any compatibility constructor must require a complete validated READY binding; it must not accept caller-chosen method/policy fields as authority.
- Add strict canonical decode/revalidation for the opaque nested values needed to recover a durable merge input. Parsing must reject unknown fields, non-canonical encodings, digest disagreement, and fields that fail the ordinary constructors.

These are the only authorized Phase-4 changes to the frozen authority boundary; they add missing provenance and recovery invariants and do not broaden provider power.

## 2. Controller-governed merge policy

### Policy authority and binding

The controller loads one `MergePolicyV1` from its immutable repository policy configuration after resolving `RepositoryBindingV1`. Request DTOs do not contain a selectable merge method, required-check list, reviewer list, threshold, or policy digest. If a compatibility DTO supplies any of them, it must equal the controller-derived value byte-for-byte or be rejected as caller policy injection.

The canonical policy binds:

- schema/policy version, source configuration evidence ref and SHA-256, repository binding digest, Phase-3 authority digest, READY binding digest, and acting-principal requirement;
- production method, fixed to `merge` in v1;
- a unique bounded set of required `TrustedCheckIdentityV1` values;
- a unique bounded set of eligible reviewer stable identities, a required-reviewer subset, and `minimum_approvals`; and
- the merge-commit recipe policy: deterministic message/trailer template, exact author/committer stable identity, timestamp derivation, Git object-format policy, and ordered-parent rule.

The policy canonical bytes/SHA-256 are part of lifecycle `Authority`, policy-decision evidence, canonical `MergeInput`, `WriteAttempt` payload digest, durable admission, `MergeResult`, post-merge proof, and `MERGED` evidence. A policy reload or digest change requires new authority and can never be adopted by an admitted attempt.

Lifecycle `AuthorityInput` must therefore gain the immutable policy and policy-authority binding, and its existing `AllowedMergeMethod` must be derived from and equal that policy. The acting identity is likewise resolved from the sealed credential, verified against GitHub, and matched to the policy's allowed stable principal; caller text never selects the actor.

### Authoritative pull-request eligibility

Add a copy-safe, strict-canonical `AuthoritativePullRequestSnapshotV1`. It binds the stable repository node/database identity, PR node/database identity and number, exact base repository/full ref/OID, exact head repository/full ref/OID, API version, authenticated acting principal, provider request identity, bounded response digest, observation time, and the independently decoded eligibility fields. It is eligible only when all of these are proved together:

- the PR state is exactly `OPEN`;
- `isDraft` is exactly `false`;
- `merged` is exactly `false` and `mergedAt` is absent;
- the base and head repository/ref/OID identities exactly equal `Authority`, the READY binding, and the eventual two-ref mutation input; and
- the base and head refs are distinct, both are `refs/heads/...` in the one authority-bound repository, and neither identity was synthesized from display text or a mutable login.

Production v1 therefore rejects fork-head PRs before admission: `UpdateRefsInput` has one `repositoryId`, so a cross-repository head cannot participate in the same atomic ref transaction. It also rejects a deleted/synthetic pull ref, a closed PR, a draft PR, a PR converted back to draft, and an already-merged PR. An absent field, null where a concrete value is required, schema mismatch, contradictory `state`/`merged` fields, or unavailable/truncated observation is invalid remote evidence, never eligibility.

The controller obtains and validates this snapshot during authorization and obtains a new authoritative snapshot during final revalidation. The final snapshot must still prove the same PR identity, exact base/head refs and OIDs, and all three eligibility predicates. Neither an earlier PR-lifecycle observation nor `merged=false` by itself suffices.

### Trusted checks

Amend `githublifecycle.Check`/`CISnapshot` with a typed producer identity. A required check key contains the exact check context/name, source kind (`check_run` or `commit_status`), stable producer identity, and stable GitHub App database/node identity where the source is app-produced. Mutable login, display name, or name alone is never trusted. Missing provenance fails closed.

Policy evaluation requires complete bounded pagination and exactly one matching observation for every required check key on `Authority.HeadSHA`. Each must be completed with `success`. Missing, duplicate, pending, failed, stale-head, untrusted-producer, wrong-app, or wrong-context observations fail. Unrequired observations cannot satisfy a required key.

### Reviews without invented chronology

Amend `githublifecycle.Review` with a stable reviewer identity (GitHub numeric/database identity plus node identity) and preserve its exact reviewed commit SHA. Canonical sorting exists only for deterministic bytes; node IDs, array position, lexical sort, and response order never imply chronology.

After complete bounded pagination:

- consider only policy-eligible reviewers for approval/blocking authority;
- any `changes_requested` by an eligible reviewer for the exact current accepted head blocks the attempt, even if another record from that reviewer says `approved`;
- treat every `dismissed` review by an eligible reviewer for the exact current accepted head as blocking with reason `REVIEW_HISTORY_UNPROVEN` unless the exact record has a valid `AuthenticatedReviewHistoryProofV1`;
- count at most one approval per eligible reviewer, only when an exact-current-head `approved` record exists and that reviewer has no blocking exact-current-head record;
- require every required reviewer to qualify and the unique approval count to meet `minimum_approvals`; and
- ignore `commented`, stale-head observations, and ineligible identities for satisfying policy; a dismissed record is never silently ignored when it is eligible and exact-head.

`AuthenticatedReviewHistoryProofV1`, if a future frozen provider capability supplies it, must bind the repository and PR stable identities, original review node/database ID, exact reviewed head, original state, every intervening state, dismissal state/time, stable authenticated dismissing principal, policy authorization for that principal, provider event identities, complete history-query pagination closure, and immutable response evidence/digests. An independent validator must reconstruct the state history and reject gaps, duplicate/conflicting events, mutable-login-only identity, or an unauthorized dismissal. A current `dismissed` value, a timestamp, audit-log prose, or an actor login alone is not proof. The frozen GitHub production-v1 contract in this plan exposes no accepted complete authenticated history source, so it produces no such proof and every eligible exact-head `dismissed` review blocks. No “latest review” reduction is permitted.

### Independently verifiable pagination closure

Complete pagination is evidence, not a trusted collector boolean. Each authorization and final revalidation persists one strict-canonical `PaginationClosureV1` for each source actually required by the frozen provider contract: exact-head check runs, exact-head commit statuses, and PR reviews. Absence of any required source closure fails policy even if another endpoint appears to contain equivalent names. Each closure binds:

- source kind, fixed HTTPS method/path or GraphQL document digest, API version, authority-bound repository/PR/head identities, every normalized query variable/filter, requested `per_page`/page number or `first`/cursor, and closure schema version;
- an ordered bounded page record for every request: zero-based ordinal, exact requested page/cursor, validated provider request identity, bounded raw response-body SHA-256, canonical response-closure envelope bytes/SHA-256, canonical decoded-item digest/count, and the exact validated `Link` relation set or GraphQL `pageInfo { hasNextPage endCursor }` returned by that response; the retained envelope contains the exact pagination fields and stable item-key/item-digest list needed for independent closure validation rather than a collector conclusion;
- the source-defined stable item key and canonical item digest for every decoded item, plus a closure-wide set digest; stable keys must be unique across all pages, and a repeated key is accepted only as an exact duplicate if the source contract explicitly documents duplication—production v1 documents none, so every cross-page duplicate fails;
- a chain proof that page zero used the endpoint's initial page/cursor, each nonterminal response advertised exactly the next request recorded, no page/cursor was skipped, repeated, reordered, or invented, and the configured page/item/call budgets were not exceeded; and
- a terminal proof derived from the final retained response: for REST, a syntactically valid bounded `Link` set with no `rel="next"`; for GraphQL, `hasNextPage=false` with a schema-valid terminal `endCursor`. A short page, empty page, collector EOF, budget exhaustion, or a caller-provided `complete=true` flag is not terminal proof.

`ValidatePaginationClosureV1` receives the expected source/query identities plus the canonical check/review items, recomputes every response-envelope and item digest, reconstructs the page chain, uniqueness set, totals, and terminal condition from the canonical records, and never trusts collector summaries. It rejects a missing first/intermediate/terminal page, inconsistent query or cursor, changed response identity/digest, item absent from or inconsistent with the bound snapshot, duplicate/conflicting item, `hasNextPage=true` at the boundary, malformed/ambiguous `Link`, unknown fields, non-canonical ordering, or truncation. `CISnapshot`, the review snapshot, the policy decision, and `MergeInput` bind the three closure digests and cannot be constructed until independent validation succeeds. Canonical item sorting occurs only after closure validation and still conveys no review chronology.

### Durable authorization linearization

After result-commit preparation, the controller performs final revalidation under the shared run-transition and repository/base locks. It reads a fresh authoritative PR snapshot; independently validates fresh complete check-run, commit-status, and review pagination closures; re-evaluates policy; re-verifies the acting principal, repository binding, exact READY ledger prefix/current state, exact base OID, and exact same-repository head OID; and then immutable-create-or-verifies `AuthorizationSealV1`.

The seal binds the full canonical bytes and digests of those observations and closures, the deterministic verdict, PR eligibility, exact base/head refs and OIDs, result recipe/OID, policy/READY/authority/attempt/write identities, provider-capability digest, limits/counters, and the fact that no target-ref request has yet been attempted. Completion of the seal file fsync followed by its parent-directory fsync is the controller authorization linearization point. A PR close/draft conversion/merge, review change/dismissal, or check change reflected in any final response blocks the seal; a policy/principal/READY mismatch blocks locally. If publication does not reach that durable point, recovery discards no evidence but collects entirely new final PR/review/check observations and cannot reuse the incomplete seal.

At that durable point, the exact retained PR/review/check responses—not a claim that GitHub offers a multi-endpoint snapshot—plus policy, principal, and READY evidence deliberately become immutable controller authorization facts for this one exact attempt. A provider change after its final response, including one concurrent with seal fsync, does not revoke or rewrite the decision if and only if that exact seal becomes durable; if the seal does not become durable, recovery must re-observe. The seal is one-use, cannot be refreshed, and cannot adopt later observations. Base and head remain server-linearized predicates regardless of observation timing: the immediately following target-ref request must use the seal's exact OIDs in the one atomic two-ref mutation from Section 3. No other provider read, wait, callback, caller decision, or mutable local-policy reload is allowed between seal/commitment publication and that single transport invocation. A crash after the seal follows the durable submission matrix and never creates a second authorization or mutation right.

The earlier authorization decision remains a canonical record containing policy source/digest, exact READY binding, complete PR/CI snapshot and pagination-closure bytes/digests/request IDs, qualifying and blocking stable identities, PR eligibility, and the deterministic verdict. Only the fresh, independently validated final versions bound into `AuthorizationSealV1` authorize publication of the target-ref commitment marker.

## 3. Production v1 atomic exact-base/exact-head merge

### Forbidden primitives

Production must not call GitHub's ordinary pull-request merge endpoint. Supplying the accepted head SHA to that endpoint does not atomically compare the target branch to `ExpectedBaseTipSHA`.

Production also must not claim exact-base safety by doing a GET followed by REST `PATCH /repos/{owner}/{repo}/git/refs/{ref}` with `force=false`; that endpoint enforces fast-forward ancestry but has no expected-old-OID compare-and-swap field. The preflight GET remains evidence only.

### Authority-approved result commit

For method `merge`, the controller derives a canonical `MergeCommitRecipeV1` only after full authority/policy validation. It binds:

- repository and full target ref `refs/heads/<exact base branch>`;
- exact expected result tree, equal to both the Phase-3-derived tree and freshly observed accepted-head tree;
- exactly two ordered parents: `[ExpectedBaseTipSHA, AcceptedHeadSHA]`;
- deterministic controller-derived message and non-secret attempt trailer;
- exact controller-policy-derived author/committer name, email, timezone, and timestamp; and
- object format, recipe version, exact expected result commit OID, write ID, authority/policy/READY digests, and limits digest. GitHub production v1 accepts only its proved 40-hex SHA-1 repository object format; other object formats remain contract-only and fail before admission.

The controller constructs the canonical Git commit bytes and expected OID locally under the pinned object-format implementation. The provider creates that exact object with `POST /repos/{owner}/{repo}/git/commits`; a `201` response is accepted only if a subsequent bounded object observation proves the returned OID, tree, ordered parents, message, author, and committer exactly match the recipe and locally computed OID. Creating an unattached content-addressed commit is preparation, not proof that the base ref changed.

The canonical recipe and expected OID must be part of `MergeInput` before any provider request. This avoids accepting a provider-selected result commit and makes the exact merge reconstructible after a lost response.

### Frozen provider capability contract

The frozen GitHub capability is `github-update-refs-atomic-base-head-v1`. Its normative upstream basis is GitHub's official [GraphQL Git `updateRefs` reference](https://docs.github.com/en/graphql/reference/git#updaterefs), which specifies that `UpdateRefsInput` contains one repository ID and a list of `RefUpdate` values, that `beforeOid` is the required current value, that `afterOid` is the value after the operation, and—critically—that every update in the list is performed atomically so rejection of one leaves every ref unmodified. That is the required all-or-nothing guarantee; generic GraphQL mutation behavior, response field ordering, observed current refs, or a test double is not a substitute.

Task 3 must freeze a reviewed canonical capability record containing that documentation URL and retained contract/schema digest, the exact `UpdateRefsInput`/`RefUpdate` fields and types, supported GitHub deployment/API identity, implementation version, and conformance-fixture digest. Startup and admission independently validate the compiled capability against that record. A controlled-repository conformance test must prove that an accepted two-ref request updates only the base as specified while leaving the head at its identical OID, and that a wrong base or wrong head `beforeOid` rejects the request with neither ref modified. The live test corroborates behavior; the published all-or-nothing promise is the authority for relying on it.

If the deployed provider lacks `updateRefs`, rejects a same-OID head update as unsupported, cannot place both full refs under the same `repositoryId`, or the frozen contract no longer supplies the explicit all-or-nothing guarantee, production merge execution is disabled and admission returns `UNSUPPORTED_ATOMIC_BASE_HEAD_CAS`. It may not degrade to a base-only mutation. A fork-head PR is therefore unsupported in production v1. Updating the frozen capability requires independent design review; runtime schema introspection or a successful one-ref call cannot silently widen it.

### One atomic base-and-head commitment

After commit preparation and completion of the durable `AuthorizationSealV1` linearization point, the controller publishes/fsyncs the target-ref commitment marker and sends exactly one GitHub GraphQL `updateRefs` mutation. `repositoryId` is the one authority-bound repository node ID, `clientMutationId` is the bound write ID, and `refUpdates` contains exactly these two distinct entries in canonical base-then-head order:

```text
refUpdates[0].name      = authority-bound full base ref
refUpdates[0].beforeOid = ExpectedBaseTipSHA
refUpdates[0].afterOid  = MergeCommitRecipeV1.ExpectedResultSHA
refUpdates[0].force     = false

refUpdates[1].name      = authority-bound full head ref
refUpdates[1].beforeOid = AcceptedHeadSHA
refUpdates[1].afterOid  = AcceptedHeadSHA
refUpdates[1].force     = false
```

The second entry is an intentional no-op CAS, not a head rewrite. The documented `afterOid` contract permits the final value to equal the required `beforeOid`; an identical OID is not a non-fast-forward, and `force=false` remains mandatory. GitHub must observe both exact `beforeOid` predicates in the same server operation. `force=false` independently requires that the base update be fast-forward and forbids any non-fast-forward interpretation; the head remains identical. Under the frozen provider guarantee, movement or deletion of either ref makes one predicate reject and therefore leaves both refs untouched. This closes the observation-to-write race for both exact code-bearing refs. An ABA that returns a ref to the sealed exact OID does not change authorized content; all non-ref predicates have already become immutable attempt authorization at the seal.

The provider must serialize exactly two updates and reject missing, extra, duplicate, reordered, cross-repository, abbreviated, or equal base/head ref names and any omitted/null `beforeOid`, wrong `afterOid`, or `force=true`. A provider that cannot prove and exercise this exact primitive fails closed before admission; it may not fall back to the PR merge endpoint, REST ref update, force update, sequential mutations, base-only CAS, or a check-then-write sequence.

A successful response is not yet `MERGED`. It is converted to a bounded `MergeResult` only after exact response/ref/commit validation and durable persistence. `squash` and `rebase` return `UNSUPPORTED_MERGE_METHOD` before admission and consume zero mutation budget.

## 4. Durable admission and exact reconciliation

### Recoverable canonical input

The admission key is the exact `{repository stable ID, base ref, Phase-3 run ID, READY event ID/digest, authority SHA-256, operation=merge, write ID}`. Under the physical resource and run locks, immutable create-or-verify records persist the canonical `MergeInput` bytes, SHA-256, a primitive recovery wire for every nested value, `WriteAttempt`, expected result OID/recipe, policy/decision, READY binding, limits identity, and evidence refs. Files use descriptor-relative no-follow regular-file checks, owner-only safe directories, file and directory fsync, and bounded atomic publication. Unsupported platforms fail closed.

The lifecycle contract must be amended as follows:

- `MergeInput` gains the complete READY binding, policy authority/decision digest, initial authoritative PR snapshot, initial independently validated pagination closures, frozen provider-capability digest, trusted authorization snapshot digests, exact base/head ref identities, and exact commit recipe/result OID, all covered by its canonical payload and attempt digest.
- Add `SealedMergeAuthorizationV1`, constructed only after commit preparation and final revalidation, which owns the unchanged reconstructed `MergeInput` plus `AuthorizationSealV1`, the fresh final PR/pagination snapshot digests, and the target-ref commitment identity. The commitment marker, mutation input, `MergeResult`, reconciliation input/result, post-merge proof, and terminal records all bind this sealed value; neither the admitted `MergeInput` nor its attempt digest is mutated in place.
- Add `ParseCanonicalMergeInput` (or an equivalent public strict rehydration constructor) so the exact original value can be reconstructed after restart. Recovery must produce identical canonical bytes, digest, attempt, and payload digest; a track-owned mirror without contract revalidation is insufficient.
- Replace attempt-only `NewReconcileWriteInput(attempt, ...)` with an input that owns the complete reconstructed `SealedMergeAuthorizationV1` and exact durable observation/commitment identities.
- Amend `ReconciliationResult` so `applied` requires a materialized `MergeResult` (or a complete result wire accepted by `NewMergeResult` and `ValidateMergeResult`) for that exact input/attempt. `not_applied` requires typed authenticated proof described below. `unknown` carries bounded observations but no result.
- Add independent `ValidateReconciliationResult(SealedMergeAuthorizationV1, result, limits)`; attempt equality without full input/seal/result equality is never enough.

On restart, the controller validates the entire admission chain before any remote call. An applied reconciliation fetches the exact expected result object, validates recipe/tree/parents, observes the target ref and bounded containment proof, validates PR/repository/actor identities, constructs the identical `MergeResult`, and persists it before post-merge verification. It never synthesizes success from `merged=true` alone.

### Submission boundary and dispositions

There are separate durable `commit-prepare-submitted` and `target-ref-update-commitment` markers. Both are published before invoking their mutating transports. The latter is the merge application boundary.

For either mutation, `submitted=false` is legal only when local validation/encoding failed before transport invocation or connection-level instrumentation proves that zero HTTP request header/body bytes could have reached the server. DNS, dial, or TLS failure qualifies only when the instrumented connection proves zero HTTP request bytes. Once any request byte may have been written, or the transport cannot prove the count, the operation is submitted/ambiguous regardless of timeout, cancellation, status, missing request ID, or returned Go error. Standard `http.Client.Do`/`RoundTrip` errors alone never prove non-submission.

For the target-ref mutation:

- `APPLIED` requires the exact successful atomic mutation result or reconciliation that materializes and validates the identical `MergeResult`.
- `NOT_APPLIED` requires either persisted zero-request-byte instrumentation or an authenticated, well-formed `updateRefs` response that explicitly proves a base or head `beforeOid` condition rejected and, under the frozen all-or-nothing contract, that neither ref update committed. A stale-ref rejection terminalizes the authority; it does not authorize retry against the new base or head.
- `UNKNOWN` is everything else, including an absent or open PR, result commit not found in a bounded read, target still at the old base, target at an unrelated commit, truncated history, lost/malformed response, or generic provider assertion without commit proof. These observations cannot rule out a commit followed by force movement or delayed visibility.

Production v1 grants zero blind or automatic mutation retries and at most one commit-object creation submission plus one target-ref update submission per exact attempt. Reconciliation is read-only. A future retry can occur only under a separately versioned, explicitly bounded controller retry authority after `NOT_APPLIED`; returning an error never creates retry authority.

## 5. Post-merge acceptance with descendant target tips

Narrowly amend the frozen post-merge contract:

- Replace the invariant `BaseAfterSHA == ResultSHA` with distinct `ResultSHA` and `ObservedTargetTipSHA` fields.
- Add a bounded `TargetContainmentProofV1` binding repository, full target ref, result SHA, observed tip SHA, provider request identity, proof mechanism/version, descendant distance, and immutable evidence. It must prove `tip == result` or that `tip` is a descendant of `result`; a truncated/indeterminate comparison is not proof.
- Keep an exact result-commit observation independent of the moving target tip: result tree must equal expected tree; method must be `merge`; parents must be exactly `[base-before, accepted-head]`; recipe/OID/attempt/authority/policy/READY identities must agree.
- Update `NewPostMergeObservation`, canonical JSON, limits validation, copy behavior, and `VerifyPostMerge` accordingly. `VerifyPostMerge` validates the original `MergeInput`, exact `SealedMergeAuthorizationV1`, `MergeResult`, exact result object, and containment proof under the controller's limits.

The live GitHub proof reads the exact result commit and the current target ref, then calls the fixed GitHub compare operation with base=`ResultSHA` and head=`ObservedTargetTipSHA`. `github-compare-v1` accepts only `identical`, or `ahead` with both the reported base and merge-base equal to `ResultSHA` and `ahead_by <= 500`; it does not infer containment from a returned commit-array prefix. A target that advanced normally from the exact result is accepted. A force move, rewind, sibling/non-descendant tip, missing result object, changed tree/parents, wrong repository/ref, excessive descendant distance, or unavailable/truncated proof fails closed, preserves the known applied result, and emits no `MERGED`; after the bounded post-merge proof budget is exhausted it terminalizes exactly `FAILED/POST_MERGE_ACCEPTANCE_FAILED` through Section 8 rather than leaving a settled negative outcome at READY.

The controller persists immutable result evidence first, then post-merge verification evidence, then a self-contained terminal core that deterministically defines the one `READY_FOR_MERGE -> MERGED` ledger event. Provider success, PR `merged` state, or target containment alone can never skip these steps.

## 6. Exact GitHub HTTPS transport

The production provider owns a sealed client and accepts no caller URL, host, transport, query, or headers. Its origin is exactly `https://api.github.com` with no userinfo, alternate port, fragment, or origin-relative override. REST paths are fixed templates with validated components escaped segment-by-segment; the GraphQL endpoint is exactly `/graphql`. TLS verifies `api.github.com`; the production transport has no environment/caller proxy, and redirect behavior cannot change the authenticated origin.

- Disable redirects (`CheckRedirect` returns `http.ErrUseLastResponse`) and reject every 3xx. Never forward credentials to a redirect or any non-pinned origin.
- A sealed signing round-tripper injects `Authorization` only after scheme/host/port/path validation. Caller-supplied authorization, proxy-authorization, cookie, host, forwarding, and conditional/cache headers are rejected. Credentials and secret-bearing headers never enter errors, evidence, admissions, or logs.
- Set fixed `Accept: application/vnd.github+json`, `X-GitHub-Api-Version: 2026-03-10`, and JSON `Content-Type`. Disable transparent transport retries for both mutation requests.
- Set `http.Transport.MaxResponseHeaderBytes=32 KiB` and independently count headers as `sum(len(canonical-name)+2+len(value)+2)+2`, across every repeated value, to the same cumulative per-response cap. A `Link` value is capped at `8 KiB`.
- Cap every encoded request body at `16 KiB`. Set `DisableCompression=true`, request only `identity` or `gzip`, count raw compressed response bytes with a `4 MiB + 1` sentinel before decode, and count decompressed bytes separately with another `4 MiB + 1` sentinel. Allow only one `identity` or `gzip` encoding; unsupported/multiple encodings fail closed. Decompression ratios cannot bypass either cap.
- Always close every non-nil response body on success, error, redirect, over-limit, decode failure, cancellation, and unexpected status. Read exactly one bounded JSON value and require EOF. A body read/close error after target-ref submission is ambiguous.
- Accept exactly one `X-GitHub-Request-Id` matching `^[A-Za-z0-9][A-Za-z0-9:._-]{0,255}$`. Duplicate/oversized/unsafe IDs make a success response invalid. Missing/invalid IDs on errors are recorded only as “unavailable” alongside the local write ID and never imply non-submission; raw values are neither returned nor persisted.

Mutation calls use a dedicated single-use HTTPS connection with keep-alives and HTTP/2 disabled. Instrumentation is above the TLS record layer and records whether the transport ever invoked a plaintext HTTP request write; once such a write is attempted, even if it reports zero bytes, submission is conservatively possible. Only validation failure before transport or a dial/TLS failure with no attempted plaintext request write can produce the persisted zero-request-byte proof.

Deterministic response mapping is operation-specific:

| Operation | Accepted response | Other response classes |
| --- | --- | --- |
| Read/observation | exact documented `200`, valid headers/body/identity | `401/403` authentication/authorization failure; `404` unavailable identity; `408/429/5xx` provider unavailable; `3xx`, `304`, other `2xx`, malformed/oversized bodies, and schema/identity mismatch invalid remote evidence. Read retries remain within the numeric read/call budgets. |
| Create exact commit object | exact documented `201` plus exact recipe/OID object proof | Proven zero bytes is not submitted. Well-formed `4xx` is a rejected preparation and permits no ref write. `3xx`, `408/429/5xx`, unexpected `2xx`, EOF, timeout/cancel, or malformed/over-limit response after possible bytes is unknown preparation; perform only exact-object reconciliation. |
| GraphQL two-ref `updateRefs` CAS | HTTP `200`, no errors, exact echoed `clientMutationId`, and a request proven to contain exactly the sealed base update plus head no-op entries; the provider then observes both refs/result object rather than pretending this payload echoes them | An authenticated response with a frozen, documented stable machine-readable before-OID rejection code is `NOT_APPLIED`; the all-or-nothing capability proves neither ref changed and the authority is stale. Error-message text is never parsed as proof. If the frozen contract supplies no such code, the result is `UNKNOWN`. Every other status, GraphQL error shape, partial/null data, malformed/over-limit body, timeout/cancel, body-close failure, or transport error after possible bytes is also `UNKNOWN`; only read-only reconciliation follows. |

## 7. Numeric production budgets

All counters are controller-owned, included in the limits-policy canonical JSON/SHA-256, reserved durably before the associated read/call/write, and cumulative across crash recovery for one exact attempt. A crash cannot reset a counter.

| Resource | Production v1 limit |
| --- | ---: |
| required trusted checks | 64 |
| eligible reviewers / required reviewers | 64 / 64 |
| minimum approvals | 0..64 and no greater than eligible reviewers |
| checks or reviews observed | 500 each |
| pages / items per page | 10 / 100 |
| pagination sources / one closure / cumulative closure bytes | exactly 3 / 1 MiB / 3 MiB |
| text or opaque identity | 4,096 bytes |
| evidence refs / metadata items | 64 / 32 per contract object |
| READY evidence-closure refs | 256 |
| one READY evidence artifact / cumulative READY closure bytes | 16 MiB / 64 MiB |
| result parents / lineage entries in production merge-only profile | exactly 2 / 0 |
| proved descendant distance after result commit | 500 commits |
| policy, input, observation, result, or decision canonical object | 256 KiB each |
| terminal core/final terminal | 512 KiB each |
| published durable files / bytes per merge attempt | 32 / 8 MiB |
| temporary files / bytes per attempt | 1 / 1 MiB |
| live temporary + published files / bytes per attempt | 33 / 9 MiB |
| concurrently active temporary files in the store | 64 |
| published admission-store files / bytes total | 4,096 / 64 MiB |
| terminal-intents record / channel bytes / records scanned | 512 KiB / 64 MiB / 4,096 |
| live temporary + published store/channel files / bytes total | 4,161 / 192 MiB |
| ledger line / snapshotted ledger / ledger records scanned | 256 KiB / 64 MiB / 262,144 |
| per HTTP call timeout | 30 seconds |
| pre-submit HTTP calls, including read retries | 28 |
| commit-object creation submissions | 1 |
| target-ref update submissions | 1 |
| normal post-merge HTTP calls | 8 |
| reconciliation rounds / calls per round | 8 / 3 |
| all HTTP calls for the attempt | 64 |
| request body / response headers / Link / request ID | 16 KiB / 32 KiB / 8 KiB / 256 bytes |
| compressed / decompressed response body per call | 4 MiB / 4 MiB |
| cumulative request / header bytes | 1 MiB / 2 MiB |
| cumulative compressed / decompressed response bytes | 256 MiB / 256 MiB |
| cumulative active provider-call time / one controller invocation | 10 minutes / 15 minutes |
| minimum interval between reconciliation rounds | 30 seconds |

The phase budgets sum to at most 62 classified calls and the hard aggregate cap is 64; the remaining two are reserved only for body-safe principal/request-identity validation and cannot be reassigned to mutations. Reaching a phase or aggregate limit stops before the next operation even when another limit has room.

Every number above requires a table-driven exact-limit success test and a `limit+1` fail-closed test. Aggregate tests must include many individually valid values that exceed cumulative call, byte, time, live temporary-plus-published file, pagination-closure, evidence-closure, or ledger-scan limits. Mutation and storage counters are tested across restart, not only in one process.

### Bounded controller-owned temporary namespace and cleanup

Each admitted attempt owns exactly one descriptor-opened directory selected by the admission-key digest, with fixed `records/` and `tmp/` children. The controller creates both beneath its configured state root using descriptor-relative no-follow operations, owner-only permissions, and file/directory fsync; caller paths, symlinks, hard links, devices, sockets, inherited files, and alternate roots are forbidden. Published names are a closed schema keyed by record kind. A temporary name is deterministically bound to `{attempt digest, write ID, record kind, destination name, declared length, content SHA-256}` and may exist only in that attempt's `tmp/`. At most one publication temporary file may exist per attempt.

Before creating any file, the controller computes its exact encoded length, bounded-scans both child directories, and durably reserves the file and byte counts in the attempt and store counters. The admission-time storage manifest and every reservation bind both the current and prospective counts for temporary and published files. Capacity checks count a temporary file and an already published destination separately whenever both exist, count declared reserved bytes before creation and actual bytes during recovery, and enforce all per-attempt and global live/published limits above. Rename does not refund a cumulative reservation; unlink refunds only live temporary capacity after directory fsync. A missing reservation, counter/file disagreement, overflow, or inability to complete the bounded inventory fails before creation.

Publication is write-full-and-fsync temporary, strict reread/digest verification, no-replace rename into `records/`, then fsync of both directories and create-or-verify of the final record. Recovery inventories the namespace before any provider call. A complete classified temporary with an exact publication intent is finished; if the byte-identical destination already exists the temporary is removed and `tmp/` is fsynced; a classified partial temporary whose destination is absent is removed and regenerated only when no mutation boundary has been crossed. Conflicting destination bytes, a temporary not named and reserved by the exact attempt, an unknown published name, unsafe type/ownership/link count, excessive size/count, or an entry that cannot be classified without following a link is never deleted or adopted and causes `LOCAL_STORAGE_INTEGRITY_FAILURE`.

The terminal/event protocol in Section 8 has a separately reserved bounded append-only `terminal-intents` channel under the canonical controller evidence/ledger roots, so an unsafe attempt `tmp/` cannot suppress the required `READY_FOR_MERGE -> FAILED` cleanup-failure event when target submission is proved absent or not applied. The channel is initialized through a safe controller-owned descriptor before attempts run, is not caller-selectable, creates no per-outcome temporary file, and is reserved against both its own record/byte/scan counters and the global live file/byte counters before append. It uses framed canonical records, full-write verification, append-or-verify, fsync, and bounded scan. If target submission is genuinely `UNKNOWN`, the unsafe namespace is preserved with the unresolved barrier and no terminal state is fabricated.

| Cleanup boundary | Required cleanup/recovery | State consequence |
| --- | --- | --- |
| Successful merge and all required publications | Before the `MERGED` event, verify every prerequisite published record, remove every classified temporary, fsync `tmp/`, and prove the live counters/inventory agree. After the event, append-or-verify `FinalTerminalV1` in the reserved terminal channel and recheck the empty `tmp/` plus counters before caller return. A crash repeats only the applicable local phase. | Pre-event cleanup failure selects `FAILED/LOCAL_CLEANUP_FAILED`. Once the exact `MERGED` event is durable, a later final-terminal/inventory failure cannot reverse it; failure is a durable auxiliary incident and recovery remains local-only. |
| Authorization/validation/policy/staleness failure before target submission | Finish or discard only classified local publications needed for the failure evidence, remove all other classified temporaries, fsync, and append-or-verify the deterministic failure terminal/event. | `READY_FOR_MERGE -> FAILED` with the exact Section-8 reason; zero target writes. |
| Cancellation before target submission, or after `NOT_APPLIED` proof | Preserve audit/zero-byte or rejection proof, remove classified temporaries, fsync, and append-or-verify the cancellation terminal/event. | `READY_FOR_MERGE -> CANCELLED`; zero/reconciled-not-applied target writes. |
| Timeout/cancellation after possible target-ref bytes with outcome `UNKNOWN` | Preserve admission, seal, commitment, counters, observations, and every reconciliation-required record. Remove only a classified expendable temporary whose publication intent proves it is not needed; otherwise leave it for bounded recovery. | Remain `READY_FOR_MERGE` behind the unresolved barrier; cleanup never converts ambiguity to failure/cancellation. |
| Publication failure or crash before rename/fsync | On restart, compare the exact temp intent, bytes/digest, destination, reservation, and boundary marker. Complete-or-verify the publication when safe; discard and regenerate only a classified pre-mutation partial; never overwrite a conflict. | Pre-submit exhaustion/conflict becomes `FAILED`; an unknown target submission remains `READY_FOR_MERGE`; a known applied result continues to `MERGED` only after all required records validate. |
| Cleanup syscall/fsync failure or repeated crash during cleanup | Persist the cleanup intent/observation, retry only the same bounded local cleanup during recovery, and re-inventory after every crash. Never spend a provider mutation or delete an unsafe/unclassifiable entry. | If no target update occurred or `NOT_APPLIED` is proved, append-or-verify `FAILED/LOCAL_CLEANUP_FAILED` unless an authorized cancellation already selected `CANCELLED`. If target outcome is `UNKNOWN`, keep the barrier/READY. If a terminal event is already durable, keep that state and attach the cleanup incident without a second transition. |
| Unsafe or unclassifiable leftover at admission/recovery | Preserve it byte-for-byte, stop all provider calls, record bounded path/type/ownership metadata without reading through it, and use the independent terminal channel where terminalization is safe. Separately authorized operator repair is required. | `FAILED/LOCAL_STORAGE_INTEGRITY_FAILURE` when no target update or `NOT_APPLIED` is proved; otherwise unresolved `READY_FOR_MERGE`. |

## 8. Durable failure and crash-boundary matrix

### Exact legal dispositions

Once the controller has safely identified the controller-selected run and proved its exact current durable state is `READY_FOR_MERGE`, every settled attempt outcome has exactly one legal transition and immutable reason. There is no successful no-transition return and no generic “leave READY” fallback:

| Classified disposition | Exact legal state/event |
| --- | --- |
| Exact result is `APPLIED` and all result/post-merge acceptance evidence validates | `READY_FOR_MERGE -> MERGED`, reason `MERGE_APPLIED_ACCEPTED`. |
| Authorized cancellation is observed before any target-ref transport invocation | `READY_FOR_MERGE -> CANCELLED`, reason `CANCELLED_BEFORE_TARGET_SUBMISSION`. |
| Authorized cancellation is pending and read-only reconciliation proves `NOT_APPLIED` | `READY_FOR_MERGE -> CANCELLED`, reason `CANCELLED_AFTER_NOT_APPLIED`. |
| Authority/ledger/policy/principal changed or stale; PR closed, draft/converted, or already merged; review/check policy rejected; pagination/remote evidence invalid or unavailable before target submission; provider capability unsupported; preparation rejected/unproved; target request proved not submitted without an authorized cancellation; atomic CAS proved `NOT_APPLIED`; local validation/publication/budget/storage/cleanup failure; or known-applied post-merge acceptance exhausts its bounded proof/rejects | `READY_FOR_MERGE -> FAILED` with exactly one canonical reason from `AUTHORITY_INVALID`, `AUTHORITY_STALE`, `PR_INELIGIBLE`, `POLICY_REJECTED`, `PAGINATION_INVALID`, `PRE_SUBMIT_VALIDATION_UNAVAILABLE`, `UNSUPPORTED_ATOMIC_BASE_HEAD_CAS`, `COMMIT_PREPARATION_FAILED`, `TARGET_UPDATE_NOT_SUBMITTED`, `TARGET_UPDATE_NOT_APPLIED_STALE_BASE`, `TARGET_UPDATE_NOT_APPLIED_STALE_HEAD`, `TARGET_UPDATE_NOT_APPLIED_STALE_REFS`, `LOCAL_PUBLICATION_FAILED`, `RESOURCE_LIMIT_EXHAUSTED`, `LOCAL_CLEANUP_FAILED`, `LOCAL_STORAGE_INTEGRITY_FAILURE`, or `POST_MERGE_ACCEPTANCE_FAILED`. |
| Target-ref request bytes may have reached GitHub and bounded reconciliation cannot prove `APPLIED` or `NOT_APPLIED` | No terminal event; the run remains `READY_FOR_MERGE` only with the exact durable `TARGET_UPDATE_UNKNOWN` barrier and read-only reconciliation authority. |

A cancellation received after `APPLIED` is known cannot relabel the remote outcome: the controller completes `MERGED` if acceptance validates, otherwise `FAILED/POST_MERGE_ACCEPTANCE_FAILED`. An object-creation request may be ambiguous, but before the target-ref commitment it cannot have moved either governed ref; bounded exact-object reconciliation either permits continuation after a new final revalidation or settles `FAILED/COMMIT_PREPARATION_FAILED`. `POLICY_BLOCKED`, `VALIDATION_UNAVAILABLE`, and `RECOVERY_REQUIRED` are not legal transitions from `READY_FOR_MERGE` in the canonical state machine and therefore are reason classifications only, never emitted states here.

Input rejected before the controller can safely identify a unique current READY run is not an admitted merge-attempt outcome and cannot authorize any ledger mutation. Once that exact READY state is proved, failure to assemble the remaining authority is `READY_FOR_MERGE -> FAILED/AUTHORITY_INVALID`. If the canonical ledger/evidence roots themselves cannot be safely read or appended, the controller reports `INTEGRITY_RECOVERY_UNAVAILABLE`, performs no provider call, and requires repair of that already-corrupt control plane; it does not invent a state transition without ledger authority.

### One terminal publication protocol for every settled outcome

`TerminalCoreV1` is generic, not MERGED-only. It always binds the exact controller-selected run and READY event/sequence plus a deterministic terminal-attempt identity derived from them and the classified boundary. Authority/policy/input/write-attempt/seal/commitment fields are stage-qualified: each is exact when its boundary was reached and is absent only with durable proof that the boundary was not reached, never because parsing or publication lost it. The core also binds submission disposition and proof, selected destination state and canonical reason, cancellation authority when applicable, result/post-merge evidence when applicable, cleanup/storage observations, and the complete bounded evidence-ref set. The controller immutable append-or-verifies and fsyncs the framed core in the reserved `terminal-intents` channel before deriving `terminal_core_digest -> canonical STATE_TRANSITION event bytes/EventID -> FinalTerminalV1`.

Under the run/ledger locks, recovery bounded-scans the same safely opened ledger descriptor and append-or-verifies exactly those event bytes. Exact EventID plus byte-identical bytes is success; the same ID with different bytes is integrity failure; clean absence permits one full `O_APPEND` write of the same event followed by ledger fsync; partial append, scan exhaustion, or ambiguous bytes never permits a new EventID. It then immutable append-or-verifies and fsyncs framed `FinalTerminalV1` in the terminal channel; that record binds the event identity/bytes and terminal core. Append-succeeded-but-returned-error, event fsync uncertainty, evidence-publication failure, cleanup interruption, and caller-return failure all re-enter this same protocol and never repeat a remote write.

This protocol is identical for `MERGED`, `FAILED`, and `CANCELLED`. A settled terminal result is not returned until the exact event is append-or-verified and fsynced. During a crash between durable terminal core and event fsync, the ledger may transiently still show READY, but the terminal barrier forces recovery to finish the predetermined event; it is not exposed as a stable READY outcome. Only `TARGET_UPDATE_UNKNOWN` may remain stably READY. No row below permits `MERGED` unless result, post-merge verification, terminal core, required pre-event cleanup, and exact event validate; final-terminal publication and post-event temporary cleanup must then finish before the normal caller return. Immutable records are create-or-verify; conflict/corruption is never a reason to overwrite.

| Boundary / failure | Last trustworthy durable state | Recovery and permitted action | Remote re-write / transition |
| --- | --- | --- | --- |
| Current READY is proved; authority/admission validation fails | exact READY event plus fresh failure proof | Build `TerminalCoreV1` with `AUTHORITY_INVALID` or `AUTHORITY_STALE`; cleanup and append-or-verify. | Zero provider mutations; exact transition `READY_FOR_MERGE -> FAILED`. |
| Admission publication/fsync remains invalid after create-or-verify recovery | exact READY; target provably unsubmitted | Preserve the conflicting/partial artifact, publish `LOCAL_PUBLICATION_FAILED` through the independent terminal channel, and append-or-verify. | Zero target writes; `READY_FOR_MERGE -> FAILED`. |
| Admission durable; authorization observation absent, invalid, unavailable, or policy/PR ineligible | exact recoverable `MergeInput`; target provably unsubmitted | Re-observe only within budget. If it does not validate, select the one exact authority/PR/policy/pagination/validation reason and run the terminal protocol. | Zero target writes; `READY_FOR_MERGE -> FAILED`, except an authorized cancellation gives `CANCELLED_BEFORE_TARGET_SUBMISSION`. |
| Pre-submit observation/closure/seal publication or fsync fails | admission plus prior immutable records; target provably unsubmitted | Never infer freshness from memory/partial bytes. Bounded create-or-verify recovery either completes the exact record or terminalizes `LOCAL_PUBLICATION_FAILED`. | Zero target writes; `READY_FOR_MERGE -> FAILED` or authorized pre-submit `CANCELLED`. |
| Commit-prepare marker uncertain or crash/error during object creation | conservatively prepare-submitted, but target provably unsubmitted | Validate marker/counters and query only the exact expected OID. Exact object permits continuation only after fresh final revalidation/new seal; absent/indeterminate at budget end terminalizes `COMMIT_PREPARATION_FAILED`. | Never repeat object creation unless persisted zero-byte proof and separately authorized; zero target writes; settled state `FAILED` or pre-submit `CANCELLED`. |
| Exact result object prepared; final READY/policy/PR/check/review/base/head/principal proof fails | prepared unattached object; no target commitment | Persist the exact stale/ineligible/policy reason and run cleanup plus terminal protocol. The unattached object grants nothing. | Zero target writes; `READY_FOR_MERGE -> FAILED` or authorized pre-submit `CANCELLED`. |
| Authorization seal and target commitment marker are valid; crash before transport with durable zero-byte proof | exact sealed attempt, `NOT_SUBMITTED` | The attempt has no implicit retry right. Terminalize `TARGET_UPDATE_NOT_SUBMITTED`, or honor an already authorized cancellation. | No target retry; `READY_FOR_MERGE -> FAILED` or `CANCELLED`. |
| Target commitment marker is partial/uncertain and zero bytes cannot be proved | submission conservatively possible | Recover `SealedMergeAuthorizationV1`; perform only exact read-only reconciliation and preserve the barrier. | No target retry; only `APPLIED -> MERGED/FAILED`, `NOT_APPLIED -> FAILED/CANCELLED`, or `UNKNOWN -> READY_FOR_MERGE`. |
| Atomic mutation proves base `beforeOid` rejection | authenticated all-or-nothing `NOT_APPLIED` | Persist rejection/current-ref evidence, cleanup, and append-or-verify `TARGET_UPDATE_NOT_APPLIED_STALE_BASE`. | No retry; `READY_FOR_MERGE -> FAILED`, or `CANCELLED_AFTER_NOT_APPLIED` if cancellation was pending. |
| Atomic mutation proves head `beforeOid` rejection | authenticated all-or-nothing `NOT_APPLIED` | Persist rejection/current-ref evidence, cleanup, and append-or-verify `TARGET_UPDATE_NOT_APPLIED_STALE_HEAD`. | No retry and no base update; `READY_FOR_MERGE -> FAILED`, or `CANCELLED_AFTER_NOT_APPLIED` if cancellation was pending. |
| Mutation may have received bytes; timeout/cancel/error/malformed response | submitted, `UNKNOWN` | Perform only bounded read-only reconciliation from the recovered full input/seal. Cancellation stops optional reads but cannot select a false terminal. | No retry; `UNKNOWN` alone remains barrier-protected `READY_FOR_MERGE`. |
| Mutation success observed; `MergeResult` validation/publication initially fails | submitted; no trustworthy settled result yet | Reconcile exact recipe/result and both refs; materialize/persist only the identical valid result. At budget end, `UNKNOWN` remains READY; known applied but irrecoverably invalid acceptance terminalizes `POST_MERGE_ACCEPTANCE_FAILED`. | No write retry; eventually `MERGED`, `FAILED`, or unresolved READY as classified. |
| Valid result durable; crash before/during post-merge observation | durable `APPLIED` result | Resume bounded post-merge proof; target may advance. Cancellation cannot relabel it. Valid acceptance selects MERGED; rejection/unavailability at the terminal budget selects `POST_MERGE_ACCEPTANCE_FAILED`. | No write retry; `READY_FOR_MERGE -> MERGED` or `FAILED`. |
| Post-merge proof is unavailable, truncated, excessive, non-descendant, or force-moved at budget end | durable applied result; verification not accepted | Persist exact failure evidence and run the generic terminal protocol. | No remote write; `READY_FOR_MERGE -> FAILED/POST_MERGE_ACCEPTANCE_FAILED`. |
| Terminal core durable; crash before event append/fsync | exact destination state/reason and event bytes/ID recoverable | Under locks, append-or-verify only that event, fsync, publish-or-verify final terminal, and cleanup. | No remote write; finish predetermined `MERGED`, `FAILED`, or `CANCELLED`. |
| Event append reports error, fsync is uncertain, or process crashes after append | event presence initially unknown | Reopen the same canonical ledger and bounded-scan. Exact ID/bytes is success; clean absence permits the same append; conflict/partial bytes is integrity failure. | Never create a new EventID or repeat remote write; finish only the predetermined state. |
| Event durable; final-terminal/evidence publication, cleanup, or caller return fails | durable state is exactly the event's `MERGED`, `FAILED`, or `CANCELLED` destination | Replay exact core/event, publish-or-verify missing derived evidence, execute bounded local cleanup, and return identical disposition. | No duplicate event, state change, or remote write. |
| Attempt-storage unsafe/unclassifiable or cleanup repeatedly fails | target disposition determines authority | Preserve unsafe bytes. If no target write/`NOT_APPLIED`, use independent terminal publication and select storage/cleanup failure; if `UNKNOWN`, preserve barrier; if event already durable, keep it and attach incident. | Zero new remote writes; exact state follows the cleanup matrix. |

While a target-ref submission is unresolved, its durable barrier leaves the run at `READY_FOR_MERGE` and blocks competing terminal transitions under the shared run lock. Cancellation may stop further network calls, but it cannot erase the barrier or assert `CANCELLED`/`FAILED` until reconciliation proves `NOT_APPLIED`; if the outcome remains unknown, governance remains explicitly unresolved rather than recording a false state.

Every deterministic material event is derived non-circularly as `TerminalCoreV1 -> terminal_core_digest -> canonical event bytes/EventID -> FinalTerminalV1`. The event binds the original READY event/sequence and all disposition-relevant authority/policy/input/seal/attempt/result/verification/cancellation/failure digests, always uses `StateFrom=READY_FOR_MERGE`, and uses exactly the destination selected by the disposition table. Same-descriptor bounded scan/append, `O_APPEND`, full-write verification, and fsync follow the already accepted PR-lifecycle durability pattern.

## 9. Required adversarial tests

- Cross-repository, cross-run, cross-plan, cross-attempt, wrong repository-node-ID, wrong authority digest, copied READY decision, omitted evidence closure, changed ledger prefix, duplicate READY event, later FAILED/CANCELLED event, and append-returned-error READY cases all fail closed.
- PR eligibility tests cover closed, draft at first observation, converted to draft before final seal, merged/already-merged, contradictory state/merged fields, changed PR identity, deleted/synthetic head, and fork-head PR. Every case makes zero target calls and emits the exact failure terminal once a current READY run is proved.
- Caller policy/method injection, changed policy source, forged hash, untrusted check producer/app, same-name wrong context, duplicate required check, stale-head check/review, ineligible approval, duplicate reviewer identity, and eligible current-head `changes_requested` plus approval all fail.
- Review regressions include an eligible exact-head `dismissed` record that formerly represented `changes_requested`, a dismissed record plus approval by the same reviewer, forged/partial history, wrong review/head/actor history binding, and missing history-page closure. All block as `REVIEW_HISTORY_UNPROVEN`; no GitHub production-v1 positive test may bypass dismissal because that provider has no accepted authenticated history source.
- For check-runs, commit statuses, and reviews independently, pagination tests cover missing first/middle/final pages, changed query/head/PR identity, page/cursor skip/repeat/reorder, cross-page duplicate/conflict, altered response/item digest, malformed or ambiguous REST `Link`, GraphQL `hasNextPage=true` at the bound, invented terminal flag, short/empty nonterminal pages, and page/item/closure `limit+1`. A canonical complete terminal chain at the exact limit passes independent validation.
- Constructed merge commit has the exact tree, two ordered parents, message/trailer, author/committer, object format, expected OID, and write identity. Any mismatch fails before the ref update.
- Assert zero calls to the ordinary PR merge endpoint and REST ref-update endpoint. The exact GraphQL request contains only the base update and same-repository head no-op CAS with their sealed full refs/OIDs and `force=false`. Wrong base or wrong head `beforeOid`, deleted/moved head, forward/backward/sideways movement, extra/missing/reordered entries, `force=true`, cross-repository head, unsupported no-op, and missing/mismatched frozen all-or-nothing capability all fail with no partial base update. A controlled live conformance test proves both-ref acceptance and each-ref rejection boundaries.
- Deterministic race tests pause before each final response, after each final response, during both seal fsyncs, after the seal, and inside the server mutation. Base or head movement before/during the mutation rejects the whole atomic update. PR close/draft conversion/merge and review/check changes reflected in a final response prevent the seal; changes after the bound response cannot mutate or revoke an eventually durable seal. A crash before seal durability forces entirely new observations, while a crash after it reuses only the identical seal and cannot create a second seal/mutation right.
- Cancellation and connection faults at every byte boundary of commit creation and ref update; `submitted=false` is accepted only for proved zero HTTP request bytes. No implicit transport or controller mutation retry occurs.
- Reconciliation restarts from canonical `MergeInput`, rejects any nested/attempt/payload mutation, materializes a valid exact `MergeResult` for applied, and returns `UNKNOWN` for absent/open PR, old/unrelated target, absent expected object, truncation, or generic not-applied claims.
- Post-merge accepts target `== result` and a bounded proved descendant; rejects wrong result tree/OID/parents, sibling, rewind, force-move, wrong ref/repository, and truncated/unknown ancestry.
- Every negative outcome table row asserts its exact `READY_FOR_MERGE -> FAILED` or `READY_FOR_MERGE -> CANCELLED` reason/event. Crash/fault injection covers admission, pre-submit evidence, prepare marker/result, authorization seal, target commitment, zero-byte proof, base/head CAS rejection, result fsync, verification fsync, all three destination terminal cores, ledger write/fsync, append-succeeded-but-error, final-terminal/evidence publication, cleanup, and caller return. Only target-submission `UNKNOWN` may finish an invocation still READY, always with the barrier.
- Storage tests fill temporary, published, live-combined, and global counters to `limit`/`limit+1`; crash before/during/after temp fsync, rename, each directory fsync, destination verify, unlink, and cleanup fsync; repeat those crashes across multiple recoveries; inject unlink/fsync/permission failures; and introduce symlink, hard-link, device, unknown-name, over-size, unreserved, partial, and conflicting leftovers. They prove deterministic finish-or-remove behavior, preservation of unsafe entries, zero remote retries, exact cleanup-failure terminal mapping, and an empty classified `tmp/` on completed success.
- Transport tests cover exact origin/path/method/headers/status, escaped malicious identities, redirect and credential non-forwarding, duplicate/missing/unsafe request ID, unsupported content encoding, body closure on every path, per-response caps, and cumulative caps.
- Every numeric bound receives exact `limit` and `limit+1` coverage, including cumulative counts/bytes/time, pagination closures, temporary-plus-published storage, and persisted counters across restart. Run race, non-Linux compile/fail-closed, path replacement, malformed JSON, partial file, duplicate identity, and concurrent same-authority attempts.
- `squash` and `rebase` production requests fail `UNSUPPORTED_MERGE_METHOD` before admission with zero provider mutation calls. No positive contract test may be described as production support.

## Task 1: Narrow `githublifecycle` contract corrections

- [ ] Add full READY/repository/policy authority bindings and strict canonical recovery; bind them through `Authority`, `MergeInput`, attempt identity, results, and validation.
- [ ] Add authoritative open/non-draft/unmerged PR snapshots, trusted check producer/app/context, stable reviewer identities, conservative exact-head dismissal handling, and independent per-source pagination-closure contracts while preserving unordered review semantics.
- [ ] Add the deterministic merge-commit recipe/expected result OID and full-input reconciliation contracts; require applied reconciliation to materialize a validated `MergeResult`.
- [ ] Add immutable `AuthorizationSealV1`/`SealedMergeAuthorizationV1` contracts and the frozen `github-update-refs-atomic-base-head-v1` capability binding without widening the foundation to claim live provider support.
- [ ] Amend post-merge observation/verification for exact result-object proof plus equal-or-descendant target containment.
- [ ] Update the foundation contract document and contract tests only for these narrow amendments. Preserve generic squash/rebase representation but make no production-support claim.
- [ ] Run package/full tests, race, vet, non-Linux compile, scope checks, and diff hygiene; commit only after independent Task-1 acceptance.

## Task 2: Controller policy, authority, admission, and recovery

- [ ] Implement controller-only policy/repository/READY derivation, initial and final PR/pagination/policy revalidation under shared locks, the durable authorization-seal linearization point, merge-only policy evaluation, and deterministic result-commit construction.
- [ ] Implement Linux durable records/counters, the bounded controller-owned temporary namespace and cleanup matrix, and fail-closed unsupported-platform stubs for every matrix boundary, including the generic `MERGED`/`FAILED`/`CANCELLED` terminal/event append-or-verify recovery protocol.
- [ ] Implement one-attempt execution against a fake exact-base/exact-head provider, full-input reconciliation, result persistence, descendant-aware post-merge acceptance, and the serial `READY_FOR_MERGE -> MERGED` transition.
- [ ] Add the authority, policy, concurrency, cumulative-budget, reconciliation, crash-boundary, and ledger-ambiguity adversarial tests above.
- [ ] Run package/full tests, race, vet, non-Linux compile, scope checks, and diff hygiene; commit only after independent Task-2 acceptance.

## Task 3: Live GitHub merge-only provider

- [ ] Implement the sealed bounded GitHub HTTPS transport, stable principal/repository/PR eligibility checks, exact commit-object creation/observation, independently verifiable review/check pagination, the frozen provider-capability record, and exactly one GraphQL atomic two-entry `updateRefs` request: base update CAS plus same-repository head no-op CAS, with exact before/after OIDs and `force=false`. Do not implement or call the ordinary PR merge endpoint or REST ref-update mutation.
- [ ] Implement merge-only live reconciliation and post-merge result-object/target-containment observations using the amended contracts and exact attempt budgets.
- [ ] Fail closed for squash/rebase, fork-head PRs, and any GitHub deployment whose frozen contract cannot guarantee and exercise all-or-nothing base-and-head CAS including the head no-op.
- [ ] Add exact wire/status/submission, moved-base/moved-head and non-ref seal races, closed/draft/converted/merged PR, dismissed review, pagination closure, ambiguity, descendant-tip, request-ID, body-closure, compressed/decompressed/cumulative-limit, and zero-forbidden-endpoint tests.
- [ ] Reconcile only Phase-4 architecture/status documentation through the immutable accepted cutoff; do not claim squash/rebase, Phase 5, deployment, or later evidence complete.
- [ ] Run complete deterministic acceptance and exact-head independent review; commit only after independent Task-3 acceptance.

## Completion gate

This plan is complete only when all three tasks are independently accepted; production supports one controller-authorized same-repository-head `merge` path using the frozen all-or-nothing exact-base update plus exact-head no-op CAS; every finding above has regression coverage; the exact final implementation head receives fresh 0 Critical / 0 Major review; that exact head is merged; post-merge acceptance/reconciliation evidence is durable; and `CURRENT_STATE.md`, `PROGRESS.md`, and `AUDIT_INDEX.md` are reconciled through the legally recordable Phase-4 cutoff. Squash/rebase, fork-head merge execution, and every Phase 5+ feature remain incomplete and out of scope.

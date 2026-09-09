# EP-005 — Merge authorization, atomic exact-base update, and post-merge acceptance

## Roadmap authority and scope

This plan implements only the remaining Phase-4 work after PR #15: controller-owned merge policy, exact-head/exact-base merge execution, and post-merge acceptance. It may make the narrow, explicitly listed amendments to the otherwise frozen `internal/githublifecycle` contracts that are required to close the design findings below. It does not change Phase-3 scheduling or integration semantics.

Production v1 supports only the `merge` method. `squash` and `rebase` remain representable by the network-free foundation contracts, but the production controller/provider must reject them before admission. Contract representation and contract tests are not evidence that either method is implemented.

Phase 5 service/API/dashboard work, deployment and production acceptance, multi-host workers, secret brokering, retention automation, and every Phase 5+ concern remain out of scope.

## Independent-review closure summary

| Finding | Root-cause correction in this plan |
| --- | --- |
| C-01 READY authority replayable across repositories/runs | Add an immutable `ReadyAuthorityBindingV1` covering the exact Phase-3 authority, repository mapping, run/plan/source-attempt identities, the exact durable `INTEGRATION_ACCEPTED -> READY_FOR_MERGE` event and sequence, its complete verified evidence closure, and a proof that it is still the run's current durable state. Expected tree content alone grants no merge authority. |
| C-02 preconditions not atomic with remote mutation | Do not call GitHub's ordinary pull-request merge endpoint and do not use REST `PATCH /git/refs` as a simulated compare-and-swap. Build the exact authority-approved merge commit first, then use one GitHub GraphQL `updateRefs` mutation with `beforeOid=expected base`, `afterOid=approved result`, and `force=false`. The server atomically rejects any changed target tip. |
| M-01 policy identity has no governing authority | Load policy only from controller-owned repository configuration, bind its source evidence and digest into lifecycle `Authority`, and reject caller-selected policy or method values. |
| M-02 check/review provenance and ordering insufficient | Bind required check context and trusted producer/app identities plus eligible reviewer stable identities. Do not derive chronology from canonical/node sorting. Any eligible current-head `changes_requested` blocks; only eligible exact-current-head approvals count; v1 ignores `dismissed` as an unblock signal because no authenticated dismissal link exists. |
| M-03 exact merge cannot be reconstructed for reconciliation | Persist canonical, strictly rehydratable `MergeInput` by exact attempt. Make reconciliation consume that full input and require an applied reconciliation to return enough material to construct and validate the identical `MergeResult`. Open/absent PR or absent result in a bounded observation is `UNKNOWN`, not `NOT_APPLIED`. |
| M-04 durable crash behavior missing | Define immutable admission, observation, prepare, commitment, result, verification, and deterministic-ledger-event records, with the complete crash/failure matrix below, including append-succeeded-but-returned-error. |
| M-05 target advancement prevents valid post-merge proof | Amend `PostMergeObservation`/`VerifyPostMerge` so the observed target may equal the result commit or be a proved descendant. The exact result object still must match tree and ordered parents; a non-descendant/force move fails closed. |
| M-06 bounds are non-numeric | Adopt the production limits table below, durable cumulative counters, and exact `limit`/`limit+1` tests for every scalar and aggregate limit. |
| M-07 transport/submission semantics incomplete | Pin the HTTPS origin and endpoints, disable redirects and implicit retries, seal credentials, bound headers and both compressed/decompressed bodies, map every response class deterministically, always close bodies, handle request IDs safely, and report `submitted=false` only with zero-request-byte instrumentation. |

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

Immediately before committing the target-ref update, the controller must reacquire/retain the shared run-transition and repository/base locks, re-read a bounded stable ledger snapshot, require the previously bound prefix to be unchanged, and prove there is no later state transition for the run. On the synchronous confirmed path it holds the run-transition lock through result persistence, post-merge verification, and the `MERGED` append. Before releasing that lock on an ambiguous path, it persists an unresolved-submission barrier that every state-transition writer must honor; no competing failure/cancellation transition may make an applied merge permanently unrecordable. Recovery reacquires the same lock, resolves or preserves the barrier, and again requires READY as the current durable state before `MERGED`. Thus a cancellation/failure transition cannot race between the final READY-state proof and the governed merge outcome. A missing, truncated, rewritten, ambiguous, superseded, or non-READY ledger state is `STALE_READY_FOR_MERGE_AUTHORITY` and permits no target-ref write.

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

### Trusted checks

Amend `githublifecycle.Check`/`CISnapshot` with a typed producer identity. A required check key contains the exact check context/name, source kind (`check_run` or `commit_status`), stable producer identity, and stable GitHub App database/node identity where the source is app-produced. Mutable login, display name, or name alone is never trusted. Missing provenance fails closed.

Policy evaluation requires complete bounded pagination and exactly one matching observation for every required check key on `Authority.HeadSHA`. Each must be completed with `success`. Missing, duplicate, pending, failed, stale-head, untrusted-producer, wrong-app, or wrong-context observations fail. Unrequired observations cannot satisfy a required key.

### Reviews without invented chronology

Amend `githublifecycle.Review` with a stable reviewer identity (GitHub numeric/database identity plus node identity) and preserve its exact reviewed commit SHA. Canonical sorting exists only for deterministic bytes; node IDs, array position, lexical sort, and response order never imply chronology.

After complete bounded pagination:

- consider only policy-eligible reviewers for approval/blocking authority;
- any `changes_requested` by an eligible reviewer for the exact current accepted head blocks the attempt, even if another record from that reviewer says `approved`;
- count at most one approval per eligible reviewer, only when an exact-current-head `approved` record exists and that reviewer has no blocking exact-current-head record;
- require every required reviewer to qualify and the unique approval count to meet `minimum_approvals`; and
- ignore `commented`, stale-head approvals, ineligible identities, and `dismissed` for satisfying or clearing policy.

The current contract has no authenticated link from `dismissed` to an original review and no trustworthy dismissal actor/provenance, so production v1 cannot use dismissal to clear `changes_requested`. A later implementation may add a separately reviewed `AuthenticatedDismissalProof` binding original review node ID, exact head, authorized dismissing identity, provider event identity, and cryptographically/authentically verified evidence. Until then, dismissal never unblocks. No “latest review” reduction is permitted.

The controller persists a canonical decision containing policy source/digest, exact READY binding, complete PR/CI snapshot bytes/digests/request IDs, qualifying and blocking stable identities, and the deterministic verdict. It re-observes and re-evaluates repository/PR/head/base, checks, reviews, and acting principal after result-commit preparation and immediately before the target-ref commitment marker.

## 3. Production v1 atomic exact-base merge

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

### One atomic target-ref commitment

After commit preparation, the controller performs its final READY/policy/PR/head/base/principal revalidation, persists and fsyncs the exact pre-submit observations, and publishes/fsyncs the target-ref commitment marker. The provider then sends exactly one GitHub GraphQL `updateRefs` mutation containing one `RefUpdate`:

```text
repositoryId = authority-bound repository node ID
name         = authority-bound full base ref
beforeOid    = ExpectedBaseTipSHA
afterOid     = MergeCommitRecipeV1.ExpectedResultSHA
force        = false
```

`clientMutationId` is the bound write ID. `updateRefs` is selected because `beforeOid` gives the server the exact old OID and the mutation is atomic; `force=false` independently requires a fast-forward. If the ref changed forward, backward, or sideways, the before-OID comparison rejects the entire mutation and no ref is updated. A provider that cannot prove this exact primitive is supported must fail closed before admission; it may not fall back to the PR merge endpoint, REST ref update, force update, or check-then-write sequence.

A successful response is not yet `MERGED`. It is converted to a bounded `MergeResult` only after exact response/ref/commit validation and durable persistence. `squash` and `rebase` return `UNSUPPORTED_MERGE_METHOD` before admission and consume zero mutation budget.

## 4. Durable admission and exact reconciliation

### Recoverable canonical input

The admission key is the exact `{repository stable ID, base ref, Phase-3 run ID, READY event ID/digest, authority SHA-256, operation=merge, write ID}`. Under the physical resource and run locks, immutable create-or-verify records persist the canonical `MergeInput` bytes, SHA-256, a primitive recovery wire for every nested value, `WriteAttempt`, expected result OID/recipe, policy/decision, READY binding, limits identity, and evidence refs. Files use descriptor-relative no-follow regular-file checks, owner-only safe directories, file and directory fsync, and bounded atomic publication. Unsupported platforms fail closed.

The lifecycle contract must be amended as follows:

- `MergeInput` gains the complete READY binding, policy authority/decision digest, trusted pre-submit snapshot digests, and exact commit recipe/result OID, all covered by its canonical payload and attempt digest.
- Add `ParseCanonicalMergeInput` (or an equivalent public strict rehydration constructor) so the exact original value can be reconstructed after restart. Recovery must produce identical canonical bytes, digest, attempt, and payload digest; a track-owned mirror without contract revalidation is insufficient.
- Replace attempt-only `NewReconcileWriteInput(attempt, ...)` with an input that owns the complete reconstructed `MergeInput` and exact durable observation/commitment identities.
- Amend `ReconciliationResult` so `applied` requires a materialized `MergeResult` (or a complete result wire accepted by `NewMergeResult` and `ValidateMergeResult`) for that exact input/attempt. `not_applied` requires typed authenticated proof described below. `unknown` carries bounded observations but no result.
- Add independent `ValidateReconciliationResult(MergeInput, result, limits)`; attempt equality without full input/result equality is never enough.

On restart, the controller validates the entire admission chain before any remote call. An applied reconciliation fetches the exact expected result object, validates recipe/tree/parents, observes the target ref and bounded containment proof, validates PR/repository/actor identities, constructs the identical `MergeResult`, and persists it before post-merge verification. It never synthesizes success from `merged=true` alone.

### Submission boundary and dispositions

There are separate durable `commit-prepare-submitted` and `target-ref-update-commitment` markers. Both are published before invoking their mutating transports. The latter is the merge application boundary.

For either mutation, `submitted=false` is legal only when local validation/encoding failed before transport invocation or connection-level instrumentation proves that zero HTTP request header/body bytes could have reached the server. DNS, dial, or TLS failure qualifies only when the instrumented connection proves zero HTTP request bytes. Once any request byte may have been written, or the transport cannot prove the count, the operation is submitted/ambiguous regardless of timeout, cancellation, status, missing request ID, or returned Go error. Standard `http.Client.Do`/`RoundTrip` errors alone never prove non-submission.

For the target-ref mutation:

- `APPLIED` requires the exact successful atomic mutation result or reconciliation that materializes and validates the identical `MergeResult`.
- `NOT_APPLIED` requires either persisted zero-request-byte instrumentation or an authenticated, well-formed `updateRefs` response that explicitly proves the atomic `beforeOid` condition rejected and that no ref update committed. A stale-base rejection terminalizes the authority; it does not authorize retry against the new base.
- `UNKNOWN` is everything else, including an absent or open PR, result commit not found in a bounded read, target still at the old base, target at an unrelated commit, truncated history, lost/malformed response, or generic provider assertion without commit proof. These observations cannot rule out a commit followed by force movement or delayed visibility.

Production v1 grants zero blind or automatic mutation retries and at most one commit-object creation submission plus one target-ref update submission per exact attempt. Reconciliation is read-only. A future retry can occur only under a separately versioned, explicitly bounded controller retry authority after `NOT_APPLIED`; returning an error never creates retry authority.

## 5. Post-merge acceptance with descendant target tips

Narrowly amend the frozen post-merge contract:

- Replace the invariant `BaseAfterSHA == ResultSHA` with distinct `ResultSHA` and `ObservedTargetTipSHA` fields.
- Add a bounded `TargetContainmentProofV1` binding repository, full target ref, result SHA, observed tip SHA, provider request identity, proof mechanism/version, descendant distance, and immutable evidence. It must prove `tip == result` or that `tip` is a descendant of `result`; a truncated/indeterminate comparison is not proof.
- Keep an exact result-commit observation independent of the moving target tip: result tree must equal expected tree; method must be `merge`; parents must be exactly `[base-before, accepted-head]`; recipe/OID/attempt/authority/policy/READY identities must agree.
- Update `NewPostMergeObservation`, canonical JSON, limits validation, copy behavior, and `VerifyPostMerge` accordingly. `VerifyPostMerge` validates the original `MergeInput`, `MergeResult`, exact result object, and containment proof under the controller's limits.

The live GitHub proof reads the exact result commit and the current target ref, then calls the fixed GitHub compare operation with base=`ResultSHA` and head=`ObservedTargetTipSHA`. `github-compare-v1` accepts only `identical`, or `ahead` with both the reported base and merge-base equal to `ResultSHA` and `ahead_by <= 500`; it does not infer containment from a returned commit-array prefix. A target that advanced normally from the exact result is accepted. A force move, rewind, sibling/non-descendant tip, missing result object, changed tree/parents, wrong repository/ref, excessive descendant distance, or unavailable/truncated proof fails closed and preserves the known result without emitting `MERGED`.

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
| GraphQL `updateRefs` CAS | HTTP `200`, no errors, and exact echoed `clientMutationId`; the provider then observes the ref/result object rather than pretending this payload echoes them | An authenticated response with a documented stable machine-readable before-OID rejection code is `NOT_APPLIED` and stale authority; error-message text is never parsed as proof. If GitHub supplies no such code, the result is `UNKNOWN`. Every other status, GraphQL error shape, partial/null data, malformed/over-limit body, timeout/cancel, body-close failure, or transport error after possible bytes is also `UNKNOWN`; only read-only reconciliation follows. |

## 7. Numeric production budgets

All counters are controller-owned, included in the limits-policy canonical JSON/SHA-256, reserved durably before the associated read/call/write, and cumulative across crash recovery for one exact attempt. A crash cannot reset a counter.

| Resource | Production v1 limit |
| --- | ---: |
| required trusted checks | 64 |
| eligible reviewers / required reviewers | 64 / 64 |
| minimum approvals | 0..64 and no greater than eligible reviewers |
| checks or reviews observed | 500 each |
| pages / items per page | 10 / 100 |
| text or opaque identity | 4,096 bytes |
| evidence refs / metadata items | 64 / 32 per contract object |
| READY evidence-closure refs | 256 |
| one READY evidence artifact / cumulative READY closure bytes | 16 MiB / 64 MiB |
| result parents / lineage entries in production merge-only profile | exactly 2 / 0 |
| proved descendant distance after result commit | 500 commits |
| policy, input, observation, result, or decision canonical object | 256 KiB each |
| terminal core/final terminal | 512 KiB each |
| durable files / bytes per merge attempt | 32 / 8 MiB |
| admission store files / bytes total | 4,096 / 64 MiB |
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

Every number above requires a table-driven exact-limit success test and a `limit+1` fail-closed test. Aggregate tests must include many individually valid values that exceed cumulative call, byte, time, file, evidence-closure, or ledger-scan limits. Mutation counters are tested across restart, not only in one process.

## 8. Durable failure and crash-boundary matrix

No row below permits `MERGED` unless result, post-merge verification, terminal core, and exact ledger event all validate. Immutable records are create-or-verify; conflict/corruption is an integrity failure, never a reason to overwrite.

| Boundary / failure | Last trustworthy durable state | Recovery and permitted action | Remote re-write / transition |
| --- | --- | --- | --- |
| READY admission validation fails | no merge admission | Preserve failure evidence if safely publishable; obtain new governed Phase-3 authority when stale. | No mutation; no `MERGED`. |
| Admission publication or fsync uncertain | absent/partial admission is untrusted | Reopen under locks and create-or-verify exact canonical record; conflict/corruption blocks. | No mutation until exact admission is durable. |
| Admission durable; pre-submit observation absent | exact `MergeInput` recoverable, zero target submission | Re-read current READY state and all remote policy/preconditions within budgets, then persist observations. | No target write yet. |
| Pre-submit observation write/fsync fails | admission plus prior observations only | Re-observe; never infer freshness from memory or a partial file. | No target write. |
| Commit-prepare marker uncertain or crash during object creation | conservatively prepare-submitted | Validate marker/counters; query only the exact expected OID. Exact object permits continuation after full revalidation; absence/indeterminate is unknown preparation. | Never repeat object creation unless persisted zero-byte proof; no `MERGED`. |
| Exact result object prepared; final policy/READY/base proof fails | prepared unattached object, no target commitment | Terminalize stale/policy failure. Unreachable object is harmless evidence, not an applied merge. | No target update. |
| Target-ref commitment marker write/fsync uncertain before transport | marker present/partial/uncertain, transport may or may not have run | Valid/uncertain marker is treated submitted unless durable zero-byte proof exists; reconcile exact input. Corruption blocks. | No blind target retry. |
| Marker durable; instrumentation proves zero request bytes | exact attempt, `NOT_SUBMITTED` proof | Revalidate everything; v1 still has no automatic retry. A separately explicit retry authority would be required. | No implicit rewrite; no transition. |
| Atomic CAS returns proved before-OID rejection | durable authenticated `NOT_APPLIED`; authority stale if base moved | Persist rejection/current-ref evidence and terminalize this authority. | No retry against changed base; no `MERGED`. |
| CAS may have received bytes; timeout/cancel/error/malformed response | submitted, outcome `UNKNOWN` | Perform only bounded read-only reconciliation using recovered full `MergeInput`. | No blind retry; no transition. |
| CAS success observed; `MergeResult` validation/persistence fails | submitted/no trustworthy result | Reconcile exact recipe/result/ref; materialize and persist only a fully validated identical result. | No write retry; no `MERGED`. |
| Valid result durably persisted; crash before post-merge observation | durable applied result | Resume at bounded post-merge observation; target may advance. | No write retry; no transition yet. |
| Post-merge proof unavailable or non-descendant | result preserved, verification not accepted | Retry bounded reads only; persist force-move/divergence evidence. | No remote write; no `MERGED`. |
| Post-merge verification persistence/fsync fails | valid result, no durable verification | Re-observe and reconstruct exact verification; partial/conflicting record blocks. | No remote write; no `MERGED`. |
| Verification + terminal core durable; crash before ledger append | exact deterministic event bytes/ID recoverable | Under run/ledger locks, bounded-scan then append-or-verify that exact event. | Local event append only; never remote rewrite. |
| Ledger append fails before any byte with proved clean absence | terminal remains authoritative; event absent | A later recovery may bounded-rescan and append the same deterministic event once. | No remote write; `MERGED` only after durable event. |
| Ledger append succeeds but returns error, fsync is uncertain, or process crashes after append | event presence initially unknown | Reopen the same canonical ledger safely and bounded-scan. Exact EventID plus byte-identical canonical event is success; same ID/different bytes is integrity failure; clean absence permits only the same event append. | Never create a new EventID or repeat remote write. |
| Ledger event durable; evidence publication/caller return fails | durable state is `MERGED` | Replay exact terminal/event, publish-or-verify missing derived evidence, and return the identical result. | No duplicate event and no remote write. |
| Any admission/result/verification/terminal/ledger conflict, malformed file, unsafe path, or exhausted scan | last earlier validated state only | Integrity/recovery-unavailable failure; preserve records for audit and require separately authorized repair. | No write and no fabricated transition. |

While a target-ref submission is unresolved, its durable barrier leaves the run at `READY_FOR_MERGE` and blocks competing terminal transitions under the shared run lock. Cancellation may stop further network calls, but it cannot erase the barrier or assert `CANCELLED`/`FAILED` until reconciliation proves `NOT_APPLIED`; if the outcome remains unknown, governance remains explicitly unresolved rather than recording a false state.

The deterministic material event is derived non-circularly as `TerminalCoreV1 -> terminal_core_digest -> canonical MERGED event bytes/EventID -> FinalTerminalV1`. The event binds the original READY event/sequence, full authority/policy/attempt/result/verification digests, and uses `StateFrom=READY_FOR_MERGE`, `StateTo=MERGED`. Same-descriptor bounded scan/append, `O_APPEND`, full-write verification, and fsync follow the already accepted PR-lifecycle durability pattern.

## 9. Required adversarial tests

- Cross-repository, cross-run, cross-plan, cross-attempt, wrong repository-node-ID, wrong authority digest, copied READY decision, omitted evidence closure, changed ledger prefix, duplicate READY event, later FAILED/CANCELLED event, and append-returned-error READY cases all fail closed.
- Caller policy/method injection, changed policy source, forged hash, untrusted check producer/app, same-name wrong context, duplicate required check, incomplete pagination, stale-head check/review, ineligible approval, duplicate reviewer identity, eligible current-head `changes_requested` plus approval, and unauthenticated dismissal all fail.
- Constructed merge commit has the exact tree, two ordered parents, message/trailer, author/committer, object format, expected OID, and write identity. Any mismatch fails before the ref update.
- Assert zero calls to the ordinary PR merge endpoint and REST ref-update endpoint. Exercise GraphQL `updateRefs` with exact before/after OIDs and `force=false`; forward/backward/sideways target movement atomically rejects with no update.
- Cancellation and connection faults at every byte boundary of commit creation and ref update; `submitted=false` is accepted only for proved zero HTTP request bytes. No implicit transport or controller mutation retry occurs.
- Reconciliation restarts from canonical `MergeInput`, rejects any nested/attempt/payload mutation, materializes a valid exact `MergeResult` for applied, and returns `UNKNOWN` for absent/open PR, old/unrelated target, absent expected object, truncation, or generic not-applied claims.
- Post-merge accepts target `== result` and a bounded proved descendant; rejects wrong result tree/OID/parents, sibling, rewind, force-move, wrong ref/repository, and truncated/unknown ancestry.
- Crash/fault injection covers every matrix row: admission, pre-submit evidence, prepare marker/result, target commitment, result fsync, verification fsync, terminal fsync, ledger write/fsync, append-succeeded-but-error, evidence publication, and caller return.
- Transport tests cover exact origin/path/method/headers/status, escaped malicious identities, redirect and credential non-forwarding, duplicate/missing/unsafe request ID, unsupported content encoding, body closure on every path, per-response caps, and cumulative caps.
- Every numeric bound receives exact `limit` and `limit+1` coverage, including cumulative counts/bytes/time and persisted counters across restart. Run race, non-Linux compile/fail-closed, symlink/path replacement, malformed JSON, partial file, duplicate identity, and concurrent same-authority attempts.
- `squash` and `rebase` production requests fail `UNSUPPORTED_MERGE_METHOD` before admission with zero provider mutation calls. No positive contract test may be described as production support.

## Task 1: Narrow `githublifecycle` contract corrections

- [ ] Add full READY/repository/policy authority bindings and strict canonical recovery; bind them through `Authority`, `MergeInput`, attempt identity, results, and validation.
- [ ] Add trusted check producer/app/context and stable reviewer identities while preserving conservative unordered review semantics.
- [ ] Add the deterministic merge-commit recipe/expected result OID and full-input reconciliation contracts; require applied reconciliation to materialize a validated `MergeResult`.
- [ ] Amend post-merge observation/verification for exact result-object proof plus equal-or-descendant target containment.
- [ ] Update the foundation contract document and contract tests only for these narrow amendments. Preserve generic squash/rebase representation but make no production-support claim.
- [ ] Run package/full tests, race, vet, non-Linux compile, scope checks, and diff hygiene; commit only after independent Task-1 acceptance.

## Task 2: Controller policy, authority, admission, and recovery

- [ ] Implement controller-only policy/repository/READY derivation, exact current-state revalidation under shared locks, merge-only policy evaluation, and deterministic result-commit construction.
- [ ] Implement Linux durable records/counters and fail-closed unsupported-platform stubs for every matrix boundary, including deterministic material-event append recovery.
- [ ] Implement one-attempt execution against a fake exact-base provider, full-input reconciliation, result persistence, descendant-aware post-merge acceptance, and the serial `READY_FOR_MERGE -> MERGED` transition.
- [ ] Add the authority, policy, concurrency, cumulative-budget, reconciliation, crash-boundary, and ledger-ambiguity adversarial tests above.
- [ ] Run package/full tests, race, vet, non-Linux compile, scope checks, and diff hygiene; commit only after independent Task-2 acceptance.

## Task 3: Live GitHub merge-only provider

- [ ] Implement the sealed bounded GitHub HTTPS transport, stable principal/repository identity checks, exact commit-object creation/observation, and the GraphQL atomic `updateRefs(beforeOid, afterOid, force=false)` primitive. Do not implement or call the ordinary PR merge endpoint or REST ref-update mutation.
- [ ] Implement merge-only live reconciliation and post-merge result-object/target-containment observations using the amended contracts and exact attempt budgets.
- [ ] Fail closed for squash/rebase and for any GitHub deployment that cannot provide the exact atomic before-OID primitive.
- [ ] Add exact wire/status/submission, moved-target, ambiguity, descendant-tip, request-ID, body-closure, compressed/decompressed/cumulative-limit, and zero-forbidden-endpoint tests.
- [ ] Reconcile only Phase-4 architecture/status documentation through the immutable accepted cutoff; do not claim squash/rebase, Phase 5, deployment, or later evidence complete.
- [ ] Run complete deterministic acceptance and exact-head independent review; commit only after independent Task-3 acceptance.

## Completion gate

This plan is complete only when all three tasks are independently accepted; production supports one controller-authorized `merge` path using exact-base atomic compare-and-swap; every finding above has regression coverage; the exact final implementation head receives fresh 0 Critical / 0 Major review; that exact head is merged; post-merge acceptance/reconciliation evidence is durable; and `CURRENT_STATE.md`, `PROGRESS.md`, and `AUDIT_INDEX.md` are reconciled through the legally recordable Phase-4 cutoff. Squash/rebase and every Phase 5+ feature remain incomplete and out of scope.

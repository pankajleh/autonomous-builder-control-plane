# IMPLEMENTABLE_PLAN

Execution checkpoint: Tasks 1 and 2 are complete on `ep-005-ci-ingestion`. Task 2 at exact clean commit `da8ffea4582539067724b363b3144d9601dee086` passed all 10 deterministic acceptance gates; acceptance result SHA-256 is `03d6f291f7606854d214b718892434a9f42eaab809e6da4bca0c431a047356d3` and final-Git evidence SHA-256 is `06a404b5ed0d300b7a643f1df75929cf75f8d41f869d8a10313970a9773021f0`. The containing reconciliation commit preserves those accepted implementation bytes and merges exact current policy through `e11afb7d7356a0df36566d98c34adbd07a0097ae`. Before Task 3 begins, resolve that commit's exact SHA and issue a fresh Task-3-only `context-capsule-v2` and immutable run authority bound to it.

This replacement follows the boundaries in [IMPLEMENTATION_ROADMAP.md](/home/devagent/autonomous-builder-control-plane/docs/roadmap/IMPLEMENTATION_ROADMAP.md), [EP-005-github-lifecycle.md](/home/devagent/autonomous-builder-control-plane/docs/execution-packs/EP-005-github-lifecycle.md), and the repository architecture contracts. It deliberately removes the rejected design’s CI-policy evaluator and PR-write state machine.

## 1. Exact scope

This subtrack owns only:

- authenticating one supported GitHub user principal;
- reading the exact governed head ref;
- bounded collection of check suites, check runs, and legacy commit statuses for `Authority.HeadSHA`;
- detecting pagination instability and same-SHA mutation with two complete semantic sweeps;
- producing one immutable, canonical `CIEvidenceBundleV1`;
- recording one authoritative ledger outcome event for every durably completed attempt;
- verified replay of a completed attempt;
- a minimal local attempt allocator that prevents artifact-name collisions and bounds CI-ingestion storage.

A `STABLE` outcome means only:

> Two complete normalized observations matched while three sampled head-ref reads remained equal to the governed SHA during the recorded interval.

It does not mean CI passed. Empty, pending, failed, successful, neutral, stale, or mixed observations can all be part of a `STABLE` collection.

## 2. Non-goals

The implementation must not:

- define or evaluate required checks;
- define accepted statuses or conclusions;
- return `ci_satisfied`, `merge_approved`, or equivalent;
- add CI policy to `authority.Manifest`;
- discover branch protection, rulesets, or required-status-check configuration;
- authorize freshness at merge time;
- select a merge method or protect an expected merge base;
- perform any GitHub write;
- emit `MERGED` or any other domain transition;
- perform post-merge acceptance;
- select “latest” legacy status semantics or collapse contexts;
- case-fold check names or legacy status contexts;
- alter `githublifecycle.CheckStatus`, `CheckConclusion`, `CISnapshot`, or their historical canonical bytes;
- implement `githublifecycle.Provider.GetCI` by coercing raw GitHub states into the frozen snapshot;
- import or reproduce PR revision/generation/submission/reconciliation semantics;
- modify `internal/prlifecycle`, `internal/integrationgate`, `internal/scheduler`, `internal/domain`, merge execution, or post-merge code;
- delete, compact, prune, or overwrite evidence.

Policy interpretation belongs to the later merge-approval/authorization track.

## 3. Input and public result

```go
type CollectRequest struct {
    RunID      string
    AttemptID  string
    Authority  githublifecycle.Authority
}
```

Production collection limits are controller-owned constants. They are not caller-supplied. Tests may inject component-wise stricter limits through an unexported constructor.

`RunID` must match the configured evidence store. `AttemptID` is a caller-provided stable retry identity, bounded to 128 bytes and restricted to safe opaque text.

Before allocator, evidence, ledger, or network access:

1. validate the complete `githublifecycle.Authority`;
2. require `Actor.Kind() == ActingKindUser`;
3. require zero installation ID;
4. parse the subject exactly as `github-user-id:<canonical-positive-int64>`;
5. reject app-installation authority as `UNSUPPORTED_PRINCIPAL`.

An unsupported app request is rejected pre-attempt. It therefore creates no reservation, artifact, ledger event, or network request. Installation proof is explicitly deferred.

```go
func (c *Controller) Collect(
    ctx context.Context,
    request CollectRequest,
) (CIEvidenceBundleV1, error)
```

For a durably completed non-`STABLE` attempt, `Collect` returns the authoritative bundle plus a typed `CollectionError` naming its evidence outcome. An empty bundle is returned only when no authoritative completed attempt could be published or recovered.

No outcome in this API authorizes merge or CI acceptance.

## 4. Versioned evidence schemas

All new types live in `internal/cilifecycle`. Canonical persisted structures use fixed field order through dedicated wire structs and `encoding/json`. Constructors deep-copy slices and expose defensive copies. Readers use strict JSON decoding, reject trailing values and unknown fields, re-encode, and require byte equality.

### Check-suite evidence

```go
type CheckSuiteObservationV1 struct {
    ProviderID     int64   `json:"provider_id"`       // required > 0
    ProviderNodeID string  `json:"provider_node_id"`  // optional, exact, <=256 bytes
    AppID          int64   `json:"app_id"`            // required > 0
    HeadSHA        string  `json:"head_sha"`          // exact governed SHA
    Status         string  `json:"status"`            // raw provider value
    Conclusion     *string `json:"conclusion"`        // raw provider value/null
    CreatedAt      string  `json:"created_at"`        // canonical UTC RFC3339Nano
    UpdatedAt      string  `json:"updated_at"`        // canonical UTC RFC3339Nano
}
```

Supported suite statuses are `queued`, `in_progress`, and `completed`.

Supported suite conclusions are `success`, `failure`, `neutral`, `cancelled`, `skipped`, `timed_out`, `action_required`, `stale`, and `startup_failure`.

Non-completed suites require a null conclusion. Completed suites require a supported non-null conclusion. These are representability checks, not acceptance rules.

### Check-run evidence

```go
type CheckRunObservationV1 struct {
    ProviderID     int64   `json:"provider_id"`       // required > 0
    ProviderNodeID string  `json:"provider_node_id"`  // optional, exact, <=256 bytes
    CheckSuiteID   int64   `json:"check_suite_id"`    // required > 0
    AppID          int64   `json:"app_id"`            // required > 0
    Name           string  `json:"name"`              // exact case, 1..256 bytes
    HeadSHA        string  `json:"head_sha"`          // exact governed SHA
    Status         string  `json:"status"`            // raw provider value
    Conclusion     *string `json:"conclusion"`        // raw provider value/null
    StartedAt      *string `json:"started_at"`        // canonical UTC RFC3339Nano
    CompletedAt    *string `json:"completed_at"`      // canonical UTC RFC3339Nano
}
```

Supported run statuses are `queued`, `in_progress`, `completed`, `waiting`, `requested`, and `pending`.

Supported run conclusions are `success`, `failure`, `neutral`, `cancelled`, `skipped`, `timed_out`, `action_required`, and `stale`. `startup_failure` is deliberately suite-only.

Every run’s `CheckSuiteID` must resolve to a collected suite in the same sweep, and the run/suite `AppID` values must agree.

### Legacy commit-status evidence

```go
type CommitStatusObservationV1 struct {
    ProviderID      int64   `json:"provider_id"`       // required > 0
    ProviderNodeID  string  `json:"provider_node_id"`  // optional
    State           string  `json:"state"`             // raw provider value
    Context         string  `json:"context"`           // exact case preserved
    Description     *string `json:"description"`       // optional, <=256 bytes
    HeadSHA         string  `json:"head_sha"`           // exact governed SHA
    CreatedAt       string  `json:"created_at"`
    UpdatedAt       string  `json:"updated_at"`
    CreatorID       int64   `json:"creator_id"`         // required > 0
    CreatorNodeID   string  `json:"creator_node_id"`    // optional
    CreatorLogin    string  `json:"creator_login"`      // exact case
    CreatorType     string  `json:"creator_type"`       // exact case
}
```

Representable raw states are `error`, `failure`, `pending`, and `success`.

All returned history is retained. The collector does not choose a latest record, group by context, compare contexts case-insensitively, or interpret success/failure. Provider numeric IDs are used only as identities and deterministic sort keys—not chronological signals.

### Semantic sweep

```go
type CISemanticSweepV1 struct {
    SchemaVersion        int                         `json:"schema_version"`
    Kind                 string                      `json:"kind"`
    RepositoryOwner      string                      `json:"repository_owner"`
    RepositoryName       string                      `json:"repository_name"`
    HeadSHA              string                      `json:"head_sha"`
    CheckSuiteTotalCount int                         `json:"check_suite_total_count"`
    CheckRunTotalCount   int                         `json:"check_run_total_count"`
    CheckSuites          []CheckSuiteObservationV1   `json:"check_suites"`
    CheckRuns            []CheckRunObservationV1     `json:"check_runs"`
    CommitStatuses       []CommitStatusObservationV1 `json:"commit_statuses"`
}
```

Canonical ordering is numeric `ProviderID` ascending within each collection. Numeric ordering does not assert provider chronology. Duplicate numeric IDs are forbidden within a type. A non-empty node ID must also be unique within its type.

The semantic digest covers every field above, including raw statuses, conclusions, exact case, relationships, and provider timestamps. It excludes HTTP request IDs, response-arrival times, and transport metadata.

### Request provenance

Each performed request records:

- local sequence number;
- phase and locally generated page number;
- method, path template, escaped path, and canonical query;
- API origin/version and Accept header;
- canonical request digest;
- HTTP status when available;
- exact bounded response-body digest when a complete body was read;
- response-envelope digest over status and body digest;
- captured-prefix digest and truncation flag when the body exceeded its cap;
- bounded `X-GitHub-Request-Id`;
- request-start and response-observed timestamps;
- response byte count;
- controller-owned failure code, never raw secret-bearing error text.

The request/response chain is the canonical ordered array of `{sequence, request_sha256, response_envelope_sha256}`. Missing responses on transport failure are represented by a typed failure-envelope digest.

### Bundle

```go
type CIEvidenceBundleV1 struct {
    SchemaVersion int    `json:"schema_version"`
    Kind          string `json:"kind"` // "ci_evidence_bundle_v1"

    RunID             string `json:"run_id"`
    AttemptID         string `json:"attempt_id"`
    AttemptKeySHA256  string `json:"attempt_key_sha256"`
    AuthoritySHA256   string `json:"authority_sha256"`
    LimitsSHA256      string `json:"limits_sha256"`
    RepositoryOwner   string `json:"repository_owner"`
    RepositoryName    string `json:"repository_name"`
    HeadBranch        string `json:"head_branch"`
    HeadSHA           string `json:"head_sha"`
    ActingKind        string `json:"acting_kind"`
    ActingSubject     string `json:"acting_subject"`
    AuthenticatedID   int64  `json:"authenticated_user_id,omitempty"`
    AuthenticatedNode string `json:"authenticated_user_node_id,omitempty"`
    AuthenticatedLogin string `json:"authenticated_user_login,omitempty"`

    Outcome     CollectionOutcome `json:"outcome"`
    FailureCode string            `json:"failure_code,omitempty"`
    FailedPhase string            `json:"failed_phase,omitempty"`
    FailedPage  int               `json:"failed_page,omitempty"`

    AttemptStartedUnixNano    int64 `json:"attempt_started_unix_nano"`
    AttemptEndedUnixNano      int64 `json:"attempt_ended_unix_nano"`
    CollectionStartedUnixNano int64 `json:"collection_started_unix_nano,omitempty"`
    CollectionEndedUnixNano   int64 `json:"collection_ended_unix_nano,omitempty"`

    FirstResponseObservedUnixNano int64 `json:"first_response_observed_unix_nano,omitempty"`
    LastResponseObservedUnixNano  int64 `json:"last_response_observed_unix_nano,omitempty"`
    EarliestProviderStateAt       string `json:"earliest_provider_state_at,omitempty"`
    LatestProviderStateAt         string `json:"latest_provider_state_at,omitempty"`

    HeadObservations []HeadObservationV1 `json:"head_observations"`
    SweepA           *CISemanticSweepV1  `json:"sweep_a,omitempty"`
    SweepB           *CISemanticSweepV1  `json:"sweep_b,omitempty"`
    SemanticDigestA  string               `json:"semantic_digest_a,omitempty"`
    SemanticDigestB  string               `json:"semantic_digest_b,omitempty"`

    RequestProvenance          []RequestProvenanceV1 `json:"request_provenance"`
    RequestResponseChainSHA256 string                `json:"request_response_chain_sha256"`
    CollectionIdentitySHA256   string                `json:"collection_identity_sha256,omitempty"`
}
```

`CollectionIdentitySHA256` is populated only for `STABLE`:

```text
SHA256(canonical JSON {
  schema_version,
  repository_owner,
  repository_name,
  head_sha,
  normalized_semantic_sweep_sha256,
  ordered_request_response_digests
})
```

`normalized_semantic_sweep_sha256` is `SemanticDigestA`, which must equal `SemanticDigestB`.

Transport timestamps and request IDs remain in provenance but are excluded from sweep equality and from request/response digest construction. Collection start/end, first/last response observations, and earliest/latest provider state timestamps are separate concepts.

## 5. Endpoint/read matrix

All requests are `GET`, carry no body, reject redirects, and use exact structured construction.

| Phase | Endpoint | Query | Maximum calls |
|---|---|---|---:|
| Principal proof | `/user` | none | 1 |
| Head H0 | `/repos/{owner}/{repo}/git/ref/heads/{branch}` | none | 1 |
| Sweep A suites | `/repos/{owner}/{repo}/commits/{sha}/check-suites` | `filter=all&page=N&per_page=64` | 4 |
| Sweep A runs | `/repos/{owner}/{repo}/commits/{sha}/check-runs` | `filter=all&page=N&per_page=64` | 4 |
| Sweep A statuses | `/repos/{owner}/{repo}/commits/{sha}/statuses` | `page=N&per_page=64` | 5 |
| Head H1 | exact head-ref endpoint | none | 1 |
| Sweep B suites | same exact-SHA suite endpoint | same | 4 |
| Sweep B runs | same exact-SHA run endpoint | same | 4 |
| Sweep B statuses | same exact-SHA status endpoint | same | 5 |
| Head H2 | exact head-ref endpoint | none | 1 |

Maximum: `1 + 3 + 2 × (4 + 4 + 5) = 30` requests.

`{sha}` is always `Authority.HeadSHA().String()`, never the branch, `HEAD`, a short SHA, or a revision expression.

The adapter never reads branch protection, rulesets, combined-status rollups, Actions workflow runs, check-suite-specific run rollups, or write endpoints.

## 6. Pagination and completeness

Production limits:

- `MaxSuites = 256`
- `MaxRuns = 256`
- `MaxStatuses = 256`
- `ItemsPerPage = 64`
- four data pages for total-count endpoints;
- five status pages, where page five is the required empty sentinel when the first four are full;
- `MaxCollectionRequests = 30`;
- no automatic transport retries.

Rules:

1. Generate page numbers locally. Never dereference or copy a URL from `Link`.
2. Bound `Link` to 8 KiB and inspect only whether a `rel=next` token exists.
3. Check-suite and check-run `total_count` must:
   - be present and non-negative;
   - be identical on every page in the sweep;
   - not exceed 256;
   - equal the final number of unique collected IDs.
4. A short suite/run page before the declared count is reached is unstable/incomplete.
5. Reaching 256 items while `rel=next` remains present is truncated.
6. Statuses have no trusted total count:
   - continue after every full page even if `Link` is absent;
   - stop only at a short page without `rel=next`;
   - when four full pages are returned, page five must be empty and have no next relation;
   - any item or next relation on page five is truncated.
7. Duplicate numeric IDs in one response are malformed.
8. Duplicate numeric IDs across local pages, a repeated non-empty page digest, or a repeated page identity is pagination instability/cycle detection.
9. A node ID reused by different numeric IDs is conflicting provider identity.
10. No monotonic-ID or timestamp ordering assumption is used.
11. Suite/run/status collections are independently complete.
12. Every run’s suite relationship is resolved and app-consistent.
13. Every item head SHA must equal the exact authority SHA.

## 7. Stable two-sweep algorithm

1. Perform all local request validation, including the user-only acting-authority restriction.
2. Reserve the exact attempt under the attempt lock.
3. If the deterministic outcome event already exists, run verified replay and make no network calls.
4. Authenticate with `GET /user`.
5. Require the returned positive user ID to equal the canonical numeric ID in the acting subject. A mismatch is `INTEGRITY_FAILURE`.
6. Record `CollectionStartedUnixNano`.
7. Read head ref H0. Require exact ref echo, object type `commit`, and SHA equal to `Authority.HeadSHA`.
8. Collect complete Sweep A in the fixed order suites → runs → statuses.
9. Read H1 and require the same exact SHA.
10. Collect complete Sweep B in the same fixed order.
11. Read H2 and require the same exact SHA.
12. Canonically normalize both sweeps and calculate their semantic digests.
13. Require `digest(A) == digest(B)`.
14. Record `CollectionEndedUnixNano`, request chain, provider timestamp ranges, and collection identity.
15. Construct and size-check the canonical bundle.
16. Publish or verify the immutable bundle.
17. Read it back through `evidence.ReadVerifiedLocal`.
18. Reconstruct and revalidate it through the public v1 constructors.
19. append-or-verify the deterministic authoritative ledger event.
20. Confirm the event by a bounded ledger scan before returning.

Race reasoning:

- H0 rejects an already-moved or unpublished governed head before Sweep A.
- H1 prevents treating Sweep A as current if the branch changed during it.
- H2 detects movement during or immediately after Sweep B.
- Suite evidence closes the rerequest gap where an old successful run remains unchanged while its suite becomes queued.
- Complete A/B equality detects persistent same-ID, same-count in-place changes, reordered pagination, inserted or removed records, suite rerequests, and status-history changes between sweeps.
- Transient mutation that changes and reverts between observations cannot be disproved against a remote provider. The bundle therefore claims repeated observational stability across the recorded bounded interval—not an atomic provider snapshot or continuous immutability.
- A mutation after H2 is outside the evidence interval. The later merge-authorization track must decide acceptable age and recollect when required.

## 8. Failure taxonomy

| Outcome | Meaning |
|---|---|
| `STABLE` | Complete A and B match and all three heads equal authority. No policy meaning. |
| `UNSTABLE` | A/B mismatch, total-count mutation, cross-page duplicate, repeated-page cycle, or other pagination/state mutation. |
| `STALE_HEAD` | H0/H1/H2 or any item SHA differs from authority, or the exact governed head ref is absent. |
| `TRUNCATED` | Page/item/body/header/text cap exceeded, next page exists beyond the cap, or enumeration cannot be proved complete. |
| `MALFORMED` | Invalid JSON, missing required identity, ID ≤0, invalid SHA/timestamp, unsupported state, invalid status/conclusion relationship, unresolved suite, or conflicting provider identity. |
| `PROVIDER_UNAVAILABLE` | Timeout, cancellation, DNS/transport failure, redirect, throttling, server failure, or unusable non-success response. |
| `UNSUPPORTED_PRINCIPAL` | Pre-attempt local rejection of non-user authority or noncanonical user subject. No artifact/event/network activity. |
| `INTEGRITY_FAILURE` | Authenticated user mismatch, clock inversion, allocator/evidence conflict, digest failure, unsafe filesystem object, conflicting ledger event, or failed reconstruction. |

Pending, failure, success, neutral, cancellation, empty observations, or missing expected names do not select an outcome other than their collection-integrity outcome. They are evidence for a future policy consumer.

## 9. Resource bounds and exact encoding arithmetic

### Transport and time

- response body: 2 MiB per request, read with `LimitReader(max+1)`;
- total response headers: 32 KiB;
- `Link`: 8 KiB;
- request ID: 256 bytes;
- provider names, contexts, descriptions, node IDs, logins, and comparable text: 256 bytes;
- state values: 64 bytes and closed enumerations;
- RFC3339Nano normalized timestamp: at most 30 bytes;
- call timeout: 10 seconds;
- collection deadline: 6 minutes;
- requests: at most 30;
- retries: zero;
- response bodies are digested and discarded after bounded decoding; arbitrary bodies/logs are not persisted.

### JSON worst-case rule

With Go `encoding/json` defaults and admitted valid UTF-8/control-free text:

```text
quoted(n) <= 6n + 2
```

The factor six covers `<`, `>`, and `&` becoming `\u003c`, `\u003e`, and `\u0026`. Quotes and backslashes expand only twofold; U+2028/U+2029 expand from three input bytes to six output bytes.

All canonical fields are emitted explicitly; optional provider values encode as either `null` or their bounded string.

Using 19 digits for every positive `int64`, 64 bytes for SHA-256/Git SHA fields where applicable, 30-byte timestamps, longest admitted state strings, and 256 ampersands in every free text field, the fixed v1 wire profile yields:

- maximum suite object: 1,846 bytes;
- maximum run object: 3,431 bytes;
- maximum legacy-status object: 9,728 bytes;
- one maximum semantic sweep with 256 of each type: 3,842,529 bytes.

The legacy-status cap is deliberately conservative: a maximum object with every admitted 256-byte free-text field filled with `&` must marshal below 9,728 bytes with the production Go encoder. The maximum-profile test computes the actual encoded length; this numeric cap is an upper bound, not an estimate.

Therefore:

```text
2 semantic sweeps                       7,685,058
30 provenance entries × 8 KiB            245,760
principal + 3 heads + fixed envelope       81,920
--------------------------------------------------
maximum derived bundle profile          8,012,738
MaxBundleBytes                          8,388,608  (8 MiB)
remaining envelope margin                 375,870
```

Every individual provenance entry is capped at 8 KiB after canonical encoding. The complete non-sweep portion is capped at 320 KiB. The final bundle has an independent actual-length check at 8 MiB.

Additional bounds:

- ledger event: 64 KiB;
- evidence URI: 1,024 bytes;
- attempt reservation: 8 KiB;
- evidence refs per event: exactly one bundle ref;
- maximum completed-attempt footprint: 9 MiB;
- attempts per run: 16;
- attempts globally in the CI allocator: 64;
- maximum CI-ingestion evidence reservation: `64 × 9 MiB = 576 MiB`;
- ledger scan snapshot: 64 MiB;
- ledger line: 256 KiB;
- ledger lines: 262,144.

Maximum-profile tests must construct real `<`, `>`, `&`, `"`, and `\` inputs, marshal with the production encoder, and assert the stated object/sweep/bundle caps. Constructors must reject any aggregate profile that cannot fit; late publication is not the first size check.

## 10. Evidence, ledger, and minimal attempt allocation

### Attempt identity

```text
request_key =
  SHA256(canonical {
    schema_version,
    run_id,
    authority_sha256,
    repository owner/name,
    head branch,
    head SHA,
    acting kind/subject,
    limits_sha256
  })

attempt_key =
  SHA256(canonical {
    request_key,
    attempt_id
  })
```

The artifact name is deterministic and attempt-bound:

```text
ci-<attempt_key>.json
```

The authoritative ledger evidence reference carries the verified `bundle_sha256`; the filename does not need the content hash. This gives an incomplete attempt exactly one possible bundle pathname, which makes crash recovery bounded and prevents orphan-bundle accumulation.

### Minimal allocator

A controller-configured, pre-existing mode-0700 directory contains:

- one administrator-provisioned mode-0600 `capacity.lock`;
- exactly one mode-0600 reservation file per attempt key. That reservation file is also the attempt's `flock` target; there is no separate per-attempt lock file.

The store uses Linux `O_NOFOLLOW`, descriptor-relative opens, inode rechecks, same-process mutexes, nonblocking `flock`, `O_EXCL`, file `fsync`, and directory `fsync`.

Reservation protocol:

1. Hold `capacity.lock` while inventorying and creating/verifying the named reservation. Every recognized reservation filename counts toward the 64-attempt/global-byte limits even if its body is incomplete, so a crash cannot create unaccounted lock-file growth.
2. Create the final reservation file with `O_CREAT|O_EXCL`, immediately acquire `flock` on that same descriptor, write the bounded canonical reservation, `fsync` the file, then `fsync` the directory. Retain the reservation-file lock for the entire same-attempt collect/publish/event operation; release `capacity.lock` after inventory/reservation so different attempt IDs may proceed concurrently.
3. If a crash leaves a zero-length or partial reservation, a later invocation for the **same attempt key** may recover it only while holding both `capacity.lock` and the reservation-file lock, only when the file is a safe regular mode-0600 object of at most 8 KiB, and only when no authoritative completed ledger event exists for that attempt. It truncates and rewrites the exact expected canonical reservation, fsyncs file+directory, then verifies bytes. A mismatched complete reservation, oversize/foreign object, or completed-event conflict is `INTEGRITY_FAILURE`.
4. A complete reservation with no authoritative event is also a recoverable **same attempt**, not a reason to consume a fresh slot. After taking the reservation-file lock, recovery checks the sole deterministic bundle path `ci-<attempt_key>.json`: if a valid bundle exists, reconstruct it and finish append-or-verify of the event without network access; if no bundle exists, resume the collection using the same `AttemptID`; if conflicting/tampered bundle material exists, fail `INTEGRITY_FAILURE`. Repeated crashes therefore reuse one reservation and one bundle pathname.
5. No cleanup of completed reservations is required for correctness. Partial or complete-but-unfinished reservations are recoverable rather than permanently poisoning additional slots. Capacity remains deterministically bounded at 64 reservation files / 576 MiB reserved footprint.

Under the capacity lock the allocator enforces the per-run/global/byte limits. Unknown entries, symlinks, special files, unsafe permissions, and capacity excess fail closed.

There is no revision, generation, submission, reconciliation, per-resource external-write admission, or mutable counter. Different attempt IDs may collect concurrently after reservation. Same-attempt executions serialize on the reservation-file lock: one collector or recovery owner proceeds, and later contenders replay the completed event or observe busy according to the bounded lock policy. The limits exist solely for collision avoidance and bounded local growth.

### Publication and ledger event

After collection, build the complete bundle in memory and calculate its digest. Publication is create-or-verify:

- create atomically without overwrite;
- if the deterministic name exists, securely read it;
- require exact kind, digest, size, and byte equality;
- reject a conflicting existing artifact as `INTEGRITY_FAILURE`.

The deterministic ledger event is:

```text
event_type: ci_evidence_collection_outcome
actor: controller
source: cilifecycle
attempt_id: request AttemptID
state_from/state_to: empty
evidence_refs: exactly the verified bundle ref
payload:
  schema_version
  attempt_key_sha256
  authority_sha256
  limits_sha256
  outcome
  collection_identity_sha256, when STABLE
  bundle_sha256
```

`EventID = SHA256("ci-evidence-outcome-v1\0" + attempt_key)` and the event timestamp equals `AttemptEndedUnixNano`.

Append-or-verify scans the bounded authoritative JSONL ledger. An existing event ID must have byte-identical canonical content. A new event is appended in one `O_APPEND` write and fsynced. An ambiguous append/fsync result is rescanned; only an exact event is success.

The attempt is completed only after the event is found and verified.

## 11. Replay and recovery

When an existing reservation is encountered:

1. acquire and revalidate its exact attempt lock;
2. bounded-scan the authoritative ledger for the deterministic event ID;
3. reject duplicate or conflicting matching events;
4. require exactly one evidence reference;
5. verify root containment, clean absolute path, regular file, no symlink/special file, size, stable inode, and SHA-256 through `evidence.ReadVerifiedLocal`;
6. strict-decode and canonical-reencode the bundle;
7. reconstruct all suite/run/status/provenance/head objects through v1 constructors;
8. recompute both semantic digests;
9. recheck outcome-specific invariants;
10. recompute the request/response chain and collection identity;
11. rebind run, attempt, authority, actor, repository, branch, head, and limits;
12. reconstruct the deterministic event and require exact bytes;
13. return without network access.

Because the event references one self-contained artifact, “every referenced artifact” is the bundle itself; all nested evidence is reconstructed rather than trusted.

If the reservation exists but no authoritative event exists, the attempt is incomplete **but recoverable under the same `AttemptID`**. While holding the reservation-file lock:

- if `ci-<attempt_key>.json` exists, securely verify, strict-decode, reconstruct, and rebind it; if valid, append-or-verify the deterministic event and complete without network access;
- if that path does not exist, resume/restart the read-only collection using the same reserved attempt;
- if the path exists but conflicts, is malformed, or fails integrity checks, return `INTEGRITY_FAILURE`.

No incomplete attempt requires a fresh slot merely because the process crashed. A completed attempt always replays its original evidence. A **new** `AttemptID` is used only when the caller intentionally wants a fresh observation after a prior attempt has completed; that fresh attempt can observe different state at the same SHA.

## 12. Durable-state and cleanup matrix

| Failure point | Last durable material | Result |
|---|---|---|
| Invalid/app authority | none | Pre-attempt `UNSUPPORTED_PRINCIPAL`; no work performed |
| Reservation failure | none or verified reservation | No authoritative bundle |
| Crash during network/sweeps | reservation | Incomplete; same attempt reacquires its reservation lock and recollects |
| Completed remote failure | reservation | Publish non-success bundle and outcome event |
| Bundle publication failure | reservation, possibly orphan temporary material | No authoritative result |
| Crash after bundle, before event | reservation plus deterministic attempt bundle | Same attempt verifies bundle and finishes the event without network |
| Ambiguous ledger append | reservation plus verified bundle | Rescan; exact event completes, absence remains same-attempt recoverable |
| Replay artifact missing/tampered | reservation plus ledger event | `INTEGRITY_FAILURE`; never return cached fields |
| Cancellation/timeout | reservation | Finalize `PROVIDER_UNAVAILABLE` when local evidence substrate remains usable |
| Cleanup failure | not applicable | No cleanup, deletion, or overwrite is attempted |

No domain state is changed. Evidence/ledger unavailability is the only condition allowed to prevent durable terminalization of an otherwise completed collection operation.

## 13. File ownership

New files only:

- `internal/cilifecycle/doc.go`
- `internal/cilifecycle/types.go`
- `internal/cilifecycle/limits.go`
- `internal/cilifecycle/canonical.go`
- `internal/cilifecycle/github.go`
- `internal/cilifecycle/collector.go`
- `internal/cilifecycle/evidence_linux.go`
- `internal/cilifecycle/attempt_store_linux.go`
- `internal/cilifecycle/material_ledger_linux.go`
- `internal/cilifecycle/unsupported.go`
- corresponding `internal/cilifecycle/*_test.go`
- `docs/architecture/CI_EVIDENCE_INGESTION_CONTRACT.md`
- the governed implementation plan/capsule specifications selected by launch authority.

Consumed but unchanged:

- `internal/githublifecycle`
- `internal/evidence`
- `internal/ledger`
- `internal/authority`

Must remain unchanged:

- `internal/prlifecycle`
- `internal/integrationgate`
- `internal/scheduler`
- `internal/domain`
- merge execution and post-merge packages.

No CLI or service endpoint is required for this subtrack.

## 14. Adversarial test inventory

Tests must cover:

- app-installation and malformed user subjects rejected before allocator/evidence/ledger/network calls;
- `/user` positive match, mismatch, zero/missing ID, malformed node/login;
- wrong ref echo, non-commit object, missing ref, moved H0/H1/H2;
- suite queued after rerequest while an old run remains completed/successful;
- same-count/same-ID state mutation between pages;
- mutation between final CI page and H2;
- A/B insertions, removals, status changes, conclusion changes, timestamp changes, and relationship changes;
- mutation followed by a stable second state producing `UNSTABLE`, not a mixed snapshot;
- stable pending, stable failure, stable empty, and stable success all returning neutral `STABLE`;
- suite/run/status IDs omitted, zero, negative, duplicated, or conflicting;
- duplicate node ID with different numeric IDs;
- run referencing absent suite or mismatched app;
- real provider node IDs containing `/`, `+`, and `=` preserved without substitution;
- case-distinct run names and status contexts preserved;
- old-failure→success and success→pending legacy histories retained without selection;
- unsupported suite/run/status states and suite-only `startup_failure`;
- invalid conclusion/status combinations;
- invalid/mismatched item head SHA;
- changed `total_count`, short page before total, count mismatch, total above 256;
- four full status pages followed by nonempty page five;
- oversized/next `Link`, repeated-page cycles, duplicate identities across pages;
- response body at cap and cap+1, headers at cap and cap+1;
- redirects and attempted non-GET construction;
- timeout/cancellation and zero retries;
- sealed authenticator changing method, URL, host, headers other than authorization, or content length;
- request-ID/timestamp variation leaving semantic equality unchanged;
- semantic-field mutation changing the digest;
- ordered request/response chain reconstruction and omitted/reordered/duplicated provenance;
- collection deadline and clock inversion;
- exact collection-identity golden vector;
- maximum `<>&"\` JSON escaping profile;
- artifact symlink, FIFO, device, directory, path escape, inode replacement, hash mismatch, and content conflict;
- concurrent same-attempt processes serialize on the reservation-file lock: one collector/recovery owner, others replay/busy, never duplicate network collection;
- concurrent different attempts without external hazard;
- per-run/global/byte allocator exhaustion;
- reservation, artifact, append, and fsync fault injection;
- repeated same-attempt crashes after reservation, during network, after bundle publication, append, and fsync; prove they reuse one reservation and one deterministic bundle pathname without consuming fresh slots;
- replay with missing/tampered bundle or conflicting event;
- fresh attempt at the same SHA observing evolved CI;
- no event state transition fields;
- no `ci_satisfied`/approval field;
- source-tree assertion that forbidden packages are unchanged.

## 15. Compatibility and rollback

- The frozen `githublifecycle` enums, snapshot wire, validators, and historical digests remain byte-for-byte unchanged.
- `CIEvidenceBundleV1` has its own explicit schema and strict retained reader.
- Existing binaries can continue reading the generic ledger envelope and safely ignore the new event type.
- No authority-manifest migration is introduced.
- Rollback means stopping invocation of `internal/cilifecycle`; artifacts, reservations, and ledger events remain immutable.
- Rollback never deletes or rewrites already-published evidence.
- Future schema versions must add new readers rather than reinterpret v1.
- A future merge-authorization track may consume verified v1 bundles under separately governed policy and freshness authority.

## 16. Prior finding disposition

| Finding | Disposition | Justification |
|---|---|---|
| C01 suite rerequests/same-SHA mutation | `IN-SCOPE FIXED` | Suites are collected separately; complete A/B semantic sweeps and H0/H1/H2 prevent the rejected false-satisfaction path. No satisfaction verdict exists. |
| M01 caller-selected CI policy | `FUTURE-TRACK/NOT-A-BLOCKER` | This track has no policy input or evaluator and does not modify `authority.Manifest`. Policy authority belongs to merge approval/authorization. |
| M02 incoherent legacy-status evaluation | `IN-SCOPE FIXED` | Complete bounded history is preserved neutrally with case and producer identity. Nothing selects latest or satisfies/blocks policy. |
| M03 unversioned frozen-enum amendment | `IN-SCOPE FIXED` | No frozen type changes. Raw states live solely in explicit `cilifecycle` v1 schemas. |
| M04 synthetic NodeID/invalid numeric identity | `IN-SCOPE FIXED` | Numeric IDs, provider node IDs, suite IDs, and app IDs are distinct fields; numeric IDs must be positive; no synthetic node ID exists. |
| M05 unreachable failure artifacts/partial replay | `IN-SCOPE FIXED` | Every durable completed outcome has one authoritative event referencing the self-contained bundle. Replay rereads and reconstructs it entirely. Orphans are never authoritative. |
| M06 false size arithmetic | `IN-SCOPE FIXED` | Bounds derive from Go JSON’s sixfold worst-case escaping, exact cardinalities, aggregate encoded caps, and maximum-profile tests. |
| M07 unsafe scan/reservation | `IN-SCOPE FIXED` | A minimal locked, bounded allocator serializes only the same attempt and reserves bounded evidence capacity. No PR write-admission state machine is copied. |
| M08 missing composite identity/freshness provenance | `IN-SCOPE FIXED` | Ordered request/response digests, semantic digest, collection interval, response-observation interval, and provider-state timestamp range are distinct and reconstructible. Freshness authorization remains future scope. |
| M09 unsupported app authority | `IN-SCOPE FIXED` | App installations and malformed user subjects are rejected locally before allocator, evidence, ledger, or network activity. |
| M10 missing task-capsule startup | `IN-SCOPE FIXED` | Every task below begins with independent verification using the path/hash supplied by launch authority; no circular hash is embedded. |

## 17. Validation commands

Implementation acceptance must run without external services:

```bash
test -z "$(gofmt -l internal/cilifecycle)"
go vet ./internal/cilifecycle
go test ./internal/cilifecycle
go test -race ./internal/cilifecycle
go test ./internal/githublifecycle ./internal/evidence ./internal/ledger ./internal/authority
go test ./internal/prlifecycle ./internal/integrationgate ./internal/scheduler ./internal/domain
go test ./...
```

Forbidden-change check:

```bash
test -z "$(git diff --name-only fd9ed5492b326f02833d68408ed415baacb89e01 -- \
  internal/githublifecycle \
  internal/prlifecycle \
  internal/integrationgate \
  internal/scheduler \
  internal/domain)"
test -z "$(git diff --name-only e11afb7d7356a0df36566d98c34adbd07a0097ae -- \
  internal/authority)"
```

The split baseline preserves the CI scope barrier while recognizing that `internal/authority` is owned by merged-main v2 operation policy.

Forbidden-semantics checks:

```bash
! rg -n 'ci_satisfied|merge_approved|required[_-]?check|branch[_-]?protection|ruleset' internal/cilifecycle
! rg -n 'Method(Post|Put|Patch|Delete)|StateFrom|StateTo' internal/cilifecycle
```

All HTTP tests use local `httptest` servers and injected clocks/transports.

## 18. Completion gate

Implementation may begin only after this design receives `DESIGN_ACCEPTED` through [IMPLEMENTATION_DESIGN_GATE.md](/home/devagent/autonomous-builder-control-plane/docs/architecture/IMPLEMENTATION_DESIGN_GATE.md).

The subtrack is complete only when:

- every task below passes its authority-bound capsule verification;
- all schemas, bounds, algorithms, and recovery invariants above are implemented;
- maximum-profile arithmetic tests pass;
- deterministic acceptance commands pass at the exact implementation head;
- the forbidden-package diff is empty;
- a fresh exact-head Critical/Major review reports zero in-scope findings;
- the accepted evidence proves only collection integrity and bounded observational stability;
- no merge-policy, authorization, transition, or post-merge behavior appears in the diff.

Roadmap self-review: this implements only Phase 4’s “CI evidence ingestion” bullet. Merge approval policy, expected-head/base protection, merge execution, and post-merge acceptance remain later serial Phase-4 work.

### Task 1: Freeze the v1 evidence contract and bounds

- [x] Before any task work, obtain `ABCP_CONTEXT_CAPSULE_PATH` and `ABCP_CONTEXT_CAPSULE_SHA256` from launch authority; require both nonempty, compare `sha256sum -- "$ABCP_CONTEXT_CAPSULE_PATH"` with the supplied hash, then run `go run ./cmd/abcp context-verify --repository "$PWD" --capsule "$ABCP_CONTEXT_CAPSULE_PATH"`. Stop on missing binding, mismatch, source drift, or repository/base mismatch.
- [x] Confirm the verified capsule identifies EP-005 CI evidence ingestion, the exact task base, this accepted design, predecessor outputs, and explicit non-goals.
- [x] Add `internal/cilifecycle` v1 observation, sweep, provenance, bundle, outcome, failure, and immutable-constructor types.
- [x] Implement fixed canonical wires, strict readers, defensive copying, exact digest helpers, relationship validation, and raw-state representability tables.
- [x] Implement controller-owned production limits and their canonical SHA-256 identity.
- [x] Add exact maximum-profile size tests, including `<`, `>`, `&`, `"`, `\`, U+2028, U+2029, and boundary cardinalities.
- [x] Add `CI_EVIDENCE_INGESTION_CONTRACT.md` recording neutral semantics and later-policy ownership.
- [x] Verify no frozen lifecycle or run-authority file changed.

### Task 2: Implement bounded GitHub reads and two-sweep stabilization

- [x] Before any task work, obtain the capsule path/hash from launch authority, independently verify its exact bytes and run `go run ./cmd/abcp context-verify --repository "$PWD" --capsule "$ABCP_CONTEXT_CAPSULE_PATH"`; stop on any mismatch or drift.
- [x] Implement the sealed authenticated GET-only transport with body/header/text/time/request caps and no retries.
- [x] Implement `/user`, exact head-ref, check-suite, check-run, and legacy-status readers using only the endpoint matrix above.
- [x] Implement locally numbered pagination, `filter=all` for both check-suite and check-run queries, total-count validation, status sentinel-page enumeration, duplicate/conflict/cycle detection, and complete-enumeration proof.
- [x] Implement suite/run relationship validation and exact per-item SHA binding.
- [x] Implement H0 → Sweep A → H1 → Sweep B → H2 and deterministic semantic normalization.
- [x] Implement A/B equality, request/response chain construction, collection identity, and separate time ranges.
- [x] Add adversarial local-server tests for rerequests, in-place mutation, pagination races, truncation, malformed state, cancellation, and authenticator mutation.
- [x] Assert stable pending/failed/empty evidence remains neutral and never becomes a policy result.

### Task 3: Implement immutable evidence, ledger outcome, and replay

- [ ] Before any task work, obtain the capsule path/hash from launch authority, independently verify the supplied SHA-256 and execute `go run ./cmd/abcp context-verify --repository "$PWD" --capsule "$ABCP_CONTEXT_CAPSULE_PATH"`; stop before filesystem or network work if verification fails.
- [ ] Implement the minimal Linux attempt allocator with pinned roots, verified locks, immutable reservations, and per-run/global/byte limits.
- [ ] Implement one deterministic bundle pathname per attempt (`ci-<attempt_key>.json`) and create-or-verify publication using the existing evidence store and verified reader; the authoritative evidence ref carries the content SHA-256.
- [ ] Implement the deterministic `ci_evidence_collection_outcome` event with empty transition fields.
- [ ] Implement bounded append-or-verify ledger handling and ambiguous-append rescan.
- [ ] Implement exact replay with no network calls and full bundle reconstruction.
- [ ] Treat reservation-without-event material as same-attempt recoverable: finalize a valid deterministic bundle if present, otherwise recollect under the same reservation/AttemptID; never consume a fresh slot solely because of a crash.
- [ ] Add non-Linux fail-closed stubs.
- [ ] Add multiprocess/concurrency, capacity, reservation-file-as-lock, recoverable zero/partial-reservation crash-boundary, symlink/special-file, tamper, missing-artifact, conflicting-event, append, and fsync tests.
- [ ] Confirm no PR revision/generation/submission/reconciliation types or semantics were introduced.

## Deferred Task 4 requirements: Acceptance, scope audit, and exact-head review handoff

These requirements are intentionally non-executable during the Task 3 operation. After Task 3 commits, freeze a separate Task-4-only plan and issue a fresh v2 capsule and immutable authority at that exact predecessor.

- Verify the final-task capsule's supplied SHA-256 and run `go run ./cmd/abcp context-verify --repository "$PWD" --capsule "$ABCP_CONTEXT_CAPSULE_PATH"`; stop on a missing binding, drift, or incorrect predecessor head.
- Run formatting, vet, package, race, regression, and complete repository tests from the validation section.
- Run the forbidden-package and forbidden-semantics checks.
- Verify maximum requests, artifacts, encoded bytes, attempts, ledger scans, and duration at their exact limits and at limit+1.
- Verify every durable completed non-success outcome is ledger-reachable and replayable.
- Verify every `STABLE` result states bounded observational stability only and contains no acceptance or approval field.
- Verify app-installation rejection occurs before allocator, evidence, ledger, and network hooks.
- Reconcile implementation documentation against the roadmap and EP-005 boundaries.
- Produce deterministic ABCP acceptance evidence for the exact implementation SHA.
- Submit that same exact SHA to a fresh Critical/Major review under the repository fallback policy.
- Do not authorize publication or later Phase-4 work unless the exact-head verdict is zero Critical and zero Major.

DESIGN_READY_FOR_GATE

# EP-006 Service/API Contract

Status: **A_DESIGN candidate — implementation forbidden until DESIGN_ACCEPTED**
Base: `c4f899072e31364f81453b2a5d6d90147c774107`

## 1. Architectural role

The service is an adapter around ABCP controller truth:

```text
HTTP request
 -> authenticate principal
 -> bounded canonical decode
 -> service contract
 -> read projection OR governed action admission
 -> authoritative ledger/evidence/controller operation
 -> canonical response
```

HTTP handlers do not call GitHub, Ralphex, Git, cleanup, process signals, or state transitions directly.

## 2. Package ownership and durable substrates

Shared contract ownership is frozen before implementation:

```text
internal/serviceapi/       external v1 DTOs/errors/authn/HTTP transport + shared cursor envelope/HMAC signer + catalog cursor
internal/runtimecatalog/   service-root safety, immutable run/attempt registration, monotonic active-owner lease/guard
internal/readmodel/        deterministic ledger projection + ledger cursor payload
internal/ledger/           B-only existing-file reader/writer constructors + close extension named in the execution plan
internal/timeline/         normalized timeline/evidence index and safe evidence resolution
internal/evidence/         C-only Linux hard-link verification extension named in the execution plan
internal/actionapi/        external action DTO/admission/controller adapters + typed human-decision decoder
internal/actioncontrol/    durable request journal, decision recording and cross-process cancel mailbox
internal/recovery/         existing blocker/resume authority remains unchanged/read-only
internal/run/              bounded cancellation-provenance threading only
cmd/abcp/                  `serve` and opt-in `run --service-root` wiring
```

A owns service DTO/interfaces, service root/catalog and authentication. B/C/D consume frozen A contracts. D may add only the named action packages and the bounded `internal/run` cancellation-provenance extension; existing `internal/recovery.ResumeAuthority` and recovery workflow semantics are read-only and are not duplicated or weakened. D does not change scheduler, GitHub, integration, Ralphex, merge, or recovery authority semantics.

The API action journal is operational idempotency/provenance, not lifecycle authority. Every applied action outcome points to the authoritative ledger event(s) that prove the controller effect.

## 3. Service root and run registration

`service_root` is a canonical absolute directory, controller-owned, mode `0700`, created/opened without symbolic-link traversal. All catalog/action paths are derived from validated run/attempt identifiers and remain beneath this root.

Conceptual layout:

```text
<service_root>/
  config/cursor-key.json
  config/authority-grants.json
  catalog/runs/<run_id>/run.json
  catalog/runs/<run_id>/attempts/<attempt_id>.json
  active/<run_id>.json
  actions/<run_id>/<epoch>.jsonl
```

`RunRegistrationV1` is create-only/byte-verify and contains internal controller data needed for safe service operation: run identity, repository identity, authority digest, canonical ledger path, canonical evidence root, and initial registration timestamp. `AttemptRegistrationV1` is separately create-only/byte-verify for each attempt. These internal paths are never returned through v1 DTOs. `GET /v1/runs` reads only `run.json` records; it never scans attempt directories or active leases to synthesize list state.

Service-visible `run_id` and `attempt_id` values MUST match `^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$`; the same grammar is used before deriving any service-root path component. Other caller-controlled identifiers are never used as filesystem names.

`abcp run --service-root <root>` registers the exact run/attempt before execution and installs `ActiveOwnerLeaseV1`. The lease binds run ID, attempt ID, a monotonically increasing per-run `lease_generation`, and the exact Linux `recovery.ProcessIdentity` tuple returned by `recovery.CaptureProcessIdentity` (PID, boot ID, Linux start ticks). `lease_id` is the SHA-256 of the strict-canonical lease identity. PID reuse is rejected by exact tuple comparison; executable identity is not claimed. Service/runtime-catalog code does not call `recovery.Inspect`, which is stale-worktree recovery logic.

`active/<run_id>.json` is a durable single-record state machine `ACTIVE -> CLOSING -> RETIRED`. Install, state change and next-generation allocation use the same bounded catalog lock, atomic replace/create-verify semantics and directory fsync. A new owner generation cannot be installed until the prior record is durably `RETIRED`; generation always advances by exactly one. A crash leaves the exact prior generation stale and never makes a different process its owner by inference.

Cancel admission and owner shutdown share an explicit ordering. Admission acquires the action-journal lock and then the catalog lock, verifies one exact live `ACTIVE` lease, and persists the receipt with that `lease_id` before releasing either lock. Normal shutdown uses the same action-journal -> catalog order to atomically change that lease to `CLOSING` and freeze a `drain_through_journal_sequence`; from that point no new cancel receipt can bind the lease. The single owner watcher drains every already-admitted receipt for that `lease_id` through the watermark to a terminal or `RECONCILIATION_REQUIRED` outcome before the lease becomes `RETIRED`. A `CLOSING` lease is delivery-eligible only for a receipt at or before its frozen drain watermark. This makes watcher shutdown and admission non-racy and prevents a replacement owner from consuming an earlier owner's cancellation.

Runs executed without `--service-root` remain valid legacy ABCP runs but are invisible to EP-006 APIs. Filesystem scanning/import of arbitrary historical ledgers is explicitly out of scope.

Catalog listing is bounded, lexically deterministic, rejects symlinks/special files/duplicate identities/conflicting records, and fails closed on unsafe entries. Track A owns the shared `CursorEnvelopeV1` signer/verifier plus the `catalog` cursor variant because `GET /v1/runs` is an A endpoint. Track B may add only the `ledger` payload encoder/decoder behind that already-frozen envelope and signer; it may not change catalog pagination semantics or key handling.

Track A also freezes the runtime-catalog lease guard consumed later by D: `AcquireOwnerLeaseGuard(run_id)` obtains the <=2-second catalog lock and returns a guard that can read the exact `ActiveOwnerLeaseV1` and perform only validated `ACTIVE -> CLOSING -> RETIRED`/next-generation operations before `Close`. D's journal guard is acquired first whenever both are needed; no inverse catalog -> journal nesting is allowed.

## 4. Authentication and authority matching

Every `/v1/*` route requires authentication, including capabilities, run reads and evidence bytes. A nil/missing authenticator is server startup failure.

`Authenticator` returns only non-secret identity:

```text
PrincipalID
PrincipalType = service|user|operator|test
AuthnMethod
```

The EP-006 concrete `abcp serve` wiring is loopback-only and machine-to-machine. `--token-file` names an owner-controlled, no-symlink, non-hard-linked mode-`0600` file no larger than 4 KiB containing exactly one opaque token after one optional trailing newline; the token is 32..512 non-whitespace/non-NUL bytes. `--principal-id` supplies one stable service-principal identifier matching `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`. Token comparison is constant-time; token bytes are never logged/persisted. Missing/unsafe token material or principal ID is startup failure. Non-loopback direct listening and direct end-user principals are not supported in EP-006; Repo C authenticates its users and proxies only authorized product requests to ABCP.

Fine-grained tenant/RBAC policy is Phase 6 / Repo C. EP-006 still has an explicit, bounded grant surface for human decisions. `--authority-grants-file` names one owner-controlled, no-symlink, non-hard-linked mode-`0600` `AuthorityGrantFileV1`, loaded and frozen at server startup. The file is at most 64 KiB; it may contain at most 16 principals, at most 256 exact `RequiredAuthority` strings per principal, and every principal/authority string is at most 256 bytes. It maps an exact authenticated principal ID to a finite exact authority set and a boolean `may_assert_delegated_actor`; duplicate principals/authorities, wildcards, empty authorities, unknown fields, or an absent file are startup failure. An explicitly configured principal with an empty authority array is the default-deny grant. `AuthorityMatcher` is constructed only from this validated file and denies unknown authority.

Because the concrete EP-006 server is a trusted machine-to-machine proxy, a decision command additionally carries a bounded `delegated_actor` (`subject_id`, `subject_type=user|operator`), with subject ID using the same 128-byte principal grammar. It is accepted only when the authenticated service principal's grant has `may_assert_delegated_actor=true`. The durable decision event records both the authenticated service principal and delegated actor. The delegated actor is attribution supplied by the trusted proxy, not independent ABCP end-user authentication; Repo C remains responsible for authenticating/authorizing that user.

## 5. Read consistency and snapshot coordination

Each projection is built from one complete validated `JSONLLedger.Snapshot()` image and its physical identity. The service refuses the sentinel `ledger-physical-identity-unavailable`.

Because `Snapshot()` is bounded but uncancellable and contends with appenders, EP-006 uses:

- at most 64 in-flight HTTP requests;
- at most 4 concurrent authoritative snapshots globally;
- context-cancellable waiting for a snapshot slot;
- **zero** ledger snapshots for `GET /v1/runs`; that route is catalog-only;
- at most **one** authoritative ledger snapshot for every other read request;
- at most 8 simultaneously open service-owned ledger objects across read-only snapshots and service-side action writers; each owns exactly two persistent OS descriptors (ledger + parent), so this is at most 16 persistent ledger descriptors; transient verification descriptors are immediately closed;
- at most 4 concurrent evidence downloads and therefore at most 64 MiB of evidence artifact bytes buffered by `ReadVerifiedLocal` at once;
- no long-poll loops; clients use cursor polling.

`GET /v1/runs` returns only immutable run-registration facts: `run_id`, repository identity digest, initial registration timestamp, and a detail URL. It consumes at most 16 MiB of bounded catalog bytes per request and does not inspect attempt directories or active leases. It MUST NOT claim current/latest attempt, active status, lifecycle state, event counts, blocker status, or terminal status. Those fields require `GET /v1/runs/{run_id}` and one authoritative snapshot.

The bounded ledger extension freezes two existing-file constructors. `ledger.OpenExistingReadOnlyJSONLLedger(path)` returns a restricted read-only type exposing only bounded `Snapshot()`/identity access and idempotent `Close()`; it opens the existing parent and ledger without creating any ledger, parent, lock/control directory or other filesystem object. `ledger.OpenExistingJSONLLedger(path)` returns the existing writable `JSONLLedger` type but likewise requires the parent+ledger to already exist; it is used only by governed service-side action effects such as decision recording. `JSONLLedger.Close()` is idempotent, releases its pinned ledger+parent descriptors, and makes later operations fail closed. Missing/replaced registered ledgers are integrity failures for both constructors and are never materialized by observation or action recovery.

Service composition has one context-cancellable global ledger-object semaphore of eight covering both read-only readers and service-side writable action objects. It does not cache either across requests/operations. A read request acquires both the global ledger-object slot and (for snapshots) the stricter four-snapshot slot, opens one existing read-only ledger, snapshots it, closes it, then releases both slots. A service-side decision effect acquires one global ledger-object slot, opens the existing writable ledger, performs only the claimed bounded action, closes it, and releases the slot. Each object pins exactly two persistent descriptors (parent + ledger), so eight objects mean at most sixteen persistent service-owned ledger descriptors.

Once a snapshot begins it may run to the existing 64-MiB/262,144-record and 2-second ledger-lock-acquisition bounds. Client disconnect does not spawn replacement work. Lock timeout maps to `503 authoritative_read_busy`, not data absence.

## 6. Projection revision and cursor

`projection_revision` is the SHA-256 of canonical context containing ledger physical-identity digest, exact snapshot SHA-256, byte length, last record ordinal/event ID, and projection schema version.

Pagination uses one stateless HMAC-SHA256 `CursorEnvelopeV1` for all deployments. `--cursor-key-file` names one owner-controlled, no-symlink, non-hard-linked mode-`0600` file no larger than 8 KiB containing `CursorKeyFileV1{key_id,key_base64}`. Startup requires a key ID matching the 128-byte principal grammar and 32..64 decoded random key bytes; duplicate JSON fields, unknown fields, insecure permissions, symlinks/hard links, malformed base64, or out-of-range keys fail startup. Key bytes are never stored in ledger/evidence/action journal or responses. Cursor lifetime is at most 15 minutes from issuance.

The signed envelope contains cursor schema version, public key ID, cursor `kind`, canonical filter identity, issued/expiry time and a kind-specific canonical payload. Two v1 kinds are frozen:

- `ledger`: run ID, ledger physical-identity digest, prior snapshot/record lineage, prior ordinal and event ID/line digest, and page position. New events may extend the view only when every bound prior record remains byte-identical; replacement, truncation, partial line, identity change or prior-record mismatch returns `409 projection_lineage_changed`.
- `catalog`: last returned lexical `run_id` (or empty for the first page), catalog schema version, and filters. It contains **no ledger identity/lineage fields**. `GET /v1/runs` performs HMAC-protected keyset pagination over the current validated immutable registrations where `run_id > last_run_id`. A registration inserted after page N with an ID `<= last_run_id` is intentionally not visible in that continuation and appears on a new traversal; an insertion with a greater ID may appear on a later page. Existing immutable registrations cannot reorder or duplicate. There is no claim of snapshot isolation for a catalog traversal.

The key is deployment-stable across restarts if cursor continuity is desired. A presented cursor whose public key ID differs returns `409 cursor_epoch_changed`; a cursor kind inappropriate for the route returns `400 invalid_cursor`. A cursor with matching key ID but invalid MAC or malformed canonical payload returns `400 invalid_cursor`.

Cursor bytes are hard-bounded to 4 KiB. No server-side cursor table exists.

## 7. Projection rules and history

Every decoded event is validated and preserved in ledger order. Core envelope fields (`RunID`, attempt/task/session IDs, state edge, actor/source, correlation, evidence refs) are authoritative projection inputs. One explicit initial-state decoder recognizes only `EventType == "RUN_CREATED"` with no state edge and requires `Payload.state == "RUN_CREATED"`; it establishes initial durable state `RUN_CREATED`. No other payload field may create or override lifecycle state. Untyped `Payload` is exposed only as bounded opaque metadata or via a registered typed decoder and cannot override core identity/state.

For each run retain:

- run ID and observed project/plan IDs;
- first/last event identity and timestamps;
- current durable state established by the one typed `RUN_CREATED` initial-state decoder and thereafter changed only by valid transition events;
- complete attempt/task/session identities and ordered summaries;
- blocker/decision indicators only from recognized authoritative events;
- event/evidence counts and terminal state;
- integration/PR/merge summary only from recognized lifecycle evidence.

A new attempt/task/session appends a distinct historical child. It never overwrites an earlier child. Conflicting identity history or impossible state chronology is `409 projection_integrity_failure`, not best-effort display data.

## 8. Evidence contract

`EvidenceRef.URI` is internal provenance and is never accepted from callers.

The evidence index is built only from refs in the authoritative run snapshot. `evidence_id` binds run ID + source event ID + evidence-ref ordinal + ref digest. API metadata includes kind, SHA-256, source event, byte size when safely known, and `downloadable`; raw controller filesystem paths are never returned.

A ref is downloadable only when all are true:

1. URI is a canonical absolute local path; no other URI/scheme is served;
2. a complete lowercase SHA-256 is present;
3. the path is beneath the exact `evidence_root` in the run's immutable catalog registration;
4. a fresh snapshot proves the same run/event/ref binding;
5. the bounded EP-006 extension to `evidence.ReadVerifiedLocal(registeredEvidenceRoot, ref, maxBytes)` succeeds. On Linux that reader requires `Nlink == 1` on the exact opened descriptor before reading, after reading, and on the reopened path descriptor used for replacement verification, in addition to its existing regular-file, containment, identity and digest checks.

Relative/remote/abstract refs remain metadata-only with `downloadable=false`. Symlink/hard-link/special-file/path replacement/digest mismatch returns `409 evidence_integrity_changed` and no bytes.

## 9. Durable action journal

Action idempotency uses dedicated mode-`0600`, no-symlink, cross-process-locked JSONL segments under `service_root/actions/<run_id>/<epoch>.jsonl`; it does not reuse `ledger.Event.AppendOrVerify`. Action-journal lock acquisition is bounded to 2 seconds. One segment is bounded to 8 MiB / 32,768 records; a run may have at most 8 monotonically linked epochs and 262,144 total records. Rollover create-verifies the next epoch with the exact digest of the prior segment's final durable record before accepting another receipt. At the total ceiling, actions fail closed with `action_journal_exhausted`; there is no deletion/compaction that could erase idempotency history.

Lock ordering is strict and finite. The only permitted nested service-filesystem order is **action-journal -> catalog**, used solely for cancel admission and owner-closing watermark creation; each acquisition has the existing <=2-second service-lock ceiling and neither lock may be held while acquiring a run-transition lease or ledger flock. Action claim/outcome transactions release the journal lock before authoritative ledger operations. For final action effect, the owner acquires the existing run-transition lease (<=2-second acquisition), then may briefly acquire/release the catalog lock only to re-prove the exact owner `lease_id`; no ledger flock is held during that catalog check. While the run-transition lease is held, the action performs at most one bounded snapshot/revalidation plus one deterministic ledger append and, for cancel only, one in-process nonblocking context-cancel invocation. No network call, sleep, journal lock, catalog lock, process wait or provider call occurs in that critical section. The lease is released before the runner attempts its resulting `CANCELLED` transition. These operation-count/byte/lock-wait bounds are the hold bound; EP-006 does not promise an unenforceable independent wall-clock lease expiry.

`ActionReceiptV1` is created under one cross-process file lock before any side effect. The deterministic lookup key is SHA-256 over canonical `(principal_id, request_id)`. Under the lock:

- if no receipt exists, the store generates `operation_id` and `received_at` once, records canonical request digest/action/target/expected state+revision plus the admitted latest state-transition event ID; cancel additionally records the exact admitted `owner_lease_id` and journal sequence under the atomic admission locks. It fsyncs before returning the stored receipt;
- if the key exists with byte-equivalent semantic identity, the existing receipt is returned without reconstructing timestamp/ID;
- if the key exists with a different digest/action/target, return `409 request_id_conflict`.

Before any operation may attempt its controller effect, the action owner create-verifies `ActionClaimV1`, keyed by `operation_id`, which freezes `claimed_at`, admitted state-transition event ID, the cancel `owner_lease_id` when applicable, and every deterministic controller event ID/timestamp that the operation may append. A receipt with no claim may still be safely admitted/claimed after preconditions are revalidated. Once a claim exists, any crash, timeout, cancellation, or ambiguous append failure before a terminal outcome requires read-only reconciliation; the controller effect is never automatically replayed.

Outcomes are separate append-only `ActionOutcomeV1` records keyed to `operation_id`. Allowed progress is:

```text
RECEIVED -> REJECTED
RECEIVED -> CLAIMED -> APPLIED
RECEIVED -> CLAIMED -> RECONCILIATION_REQUIRED -> RECONCILED_APPLIED | RECONCILED_NOT_APPLIED
```

`REJECTED`, `APPLIED`, `RECONCILED_APPLIED`, and `RECONCILED_NOT_APPLIED` are terminal. `RECONCILIATION_REQUIRED` permits only read-only reconciliation; it never authorizes automatic action replay. Outcome references exact authoritative ledger event IDs/evidence when an effect occurred.

## 10. Canonical command envelope

Every command includes:

```json
{
  "schema_version": 1,
  "request_id": "client-stable-id",
  "attempt_id": "exact-target-attempt",
  "expected_state": "required-state",
  "expected_revision": "required-projection-revision",
  "reason": "bounded human-readable reason",
  "delegated_actor": {"subject_id": "repo-c-user-id", "subject_type": "user"},
  "payload": {}
}
```

`delegated_actor` is required for `decision` and optional/omitted for `cancel`; it participates in the semantic digest. The server validates and canonicalizes action-specific payload, computes its semantic digest, then creates/loads the durable receipt. Caller-supplied filesystem paths, PIDs, authority manifests, Ralphex args, recovery cleanup paths, and arbitrary evidence references are forbidden.

Action POSTs are asynchronous. After the durable receipt exists they return `202 Accepted` with `{operation_id,status,status_url}`; an idempotent repeat returns the same operation and its latest journal status. `GET /v1/runs/{run_id}/actions/{operation_id}` requires authentication and returns only that operation's bounded receipt/status/outcome metadata plus authoritative event IDs, never internal paths. No POST request waits for the cross-process effect.

## 11. Cancel action — exact cross-process actuation

`POST /v1/runs/{run_id}/actions/cancel` is supported only when the catalog has one exact live active-owner lease for the requested run/attempt.

Actuation mechanism is a durable mailbox implemented by the action journal:

1. service validates the caller's full `expected_revision`, exact current state and attempt, then performs the atomic action-journal -> catalog admission described in §3. The receipt freezes the latest authoritative **state-transition event ID**, exact `owner_lease_id`, and journal sequence observed at admission;
2. service persists/fsyncs `ActionReceiptV1` under those locks and returns `202` only after both the receipt and exact owner binding are durable;
3. the exact `abcp run --service-root` process has one watcher for its own lease generation, polling only its journal at an interval no greater than 250 ms while that lease is `ACTIVE` or completing its bounded `CLOSING` final drain; EP-006 permits at most 256 simultaneously active registered runs/watchers;
4. watcher create-verifies `ActionClaimV1`, including the exact receipt `owner_lease_id`, deterministic `API_CANCEL_REQUESTED` event ID and timestamp; the journal lock is then released;
5. watcher acquires the run-transition lease. It briefly checks the catalog record and requires the receipt/claim `owner_lease_id` to equal its own exact lease generation and to be delivery-eligible (`ACTIVE`, or `CLOSING` with receipt sequence <= `drain_through_journal_sequence`). A changed/retired/replaced lease produces no effect and closes the claimed operation as not applied/reconciliation as appropriate;
6. still holding the run-transition lease, watcher takes one fresh bounded snapshot and requires the latest authoritative state-transition event ID and current state to equal the admitted values. Unrelated non-transition events do not make cancellation stale; any intervening state transition does. It then uses `AppendOrVerifyLeased` to append the deterministic non-transition `API_CANCEL_REQUESTED` event binding operation/principal/request digest **and owner_lease_id**, and immediately invokes its in-process context cancel function with a typed cause carrying `operation_id` and `owner_lease_id`;
7. watcher releases the run-transition lease. Only then may the runner append its resulting state transition. A bounded `internal/run` extension centralizes API-cancel provenance and applies it to **every current runner cancellation exit**, including validation cancellation, supervisor/process cancellation, pre-acceptance cancellation, acceptance cancellation, and the process-error branch that would otherwise emit `FAILED` while the typed API cancellation cause is active. Non-API failures retain existing semantics; an API-caused `CANCELLED` payload binds both `operation_id` and `owner_lease_id` and is allowed only after the durable exact request event;
8. watcher/journal reconciliation records `APPLIED` only after the `CANCELLED` transition carrying the same `operation_id` and `owner_lease_id` is observed, referencing both ledger events. No later owner generation may claim, deliver or reconcile-as-applied a request bound to an earlier generation.

The cancel POST is therefore admission, not completion. Clients follow the returned action-status URL. Under a live run the watcher discovery interval is bounded by 250 ms, but end-to-end transition time is not falsely promised; status remains `CLAIMED`/reconciliation state until authoritative ledger proof exists.

If the watcher claims/delivers a command and crashes before proof, the journal records/reconstructs `RECONCILIATION_REQUIRED`. Reconciliation inspects ledger + exact owner proof. It may conclude applied or not-applied but may not resend the cancellation automatically.

If no exact live lease exists, return `409 action_target_not_active`. Stale/PID-reused owner state never receives cancellation.

## 12. Human-decision action — durable answer recording without duplicate resume authority

`POST /v1/runs/{run_id}/actions/decision` is supported only for current state `HUMAN_DECISION_REQUIRED`. It records one authorized human answer but deliberately **does not perform a state transition**. `recovery.ResumeAuthority` remains the sole shipped recovery/resume authority type for later continuation.

The decision request identity is the exact originating `BLOCKER_DECISION_RECORDED` event ID. A frozen typed decoder in `internal/actionapi` accepts only that event type/schema and extracts `requirement.human_decision` while reusing the existing recovery requirement semantics. It validates:

- same run/attempt;
- event's transition target is `HUMAN_DECISION_REQUIRED`;
- question and `RequiredAuthority` are valid;
- `AcceptedAnswers` is non-empty and unique;
- no later state transition has superseded the blocker;
- no prior `API_HUMAN_DECISION_RECORDED` event already binds that decision-request event ID.

Action payload is only:

```json
{
  "decision_request_id": "originating-event-id",
  "answer": "one exact accepted answer"
}
```

Non-enumerable/unknown payload shapes fail closed; EP-006 does not invent free-form decision schemas. A different request ID cannot record a second answer for the same decision request; it returns `409 decision_already_recorded`. An idempotent repeat of the original `(principal_id, request_id)` returns the existing operation.

After durable action receipt, `AuthorityMatcher` proves the authenticated principal satisfies `RequiredAuthority` from the frozen grant file and that `may_assert_delegated_actor=true`. The handler create-verifies `ActionClaimV1`; the claim freezes deterministic event ID `SHA256("ep006-human-decision-record-v1\0" || operation_id)` and its exact timestamp, then releases the journal lock. It acquires the run-transition lease and, while holding it, takes one fresh bounded snapshot, revalidates the admitted state-transition event/current `HUMAN_DECISION_REQUIRED` state, unresolved blocker event, absence of any prior decision record, accepted answer and grant, and uses `AppendOrVerifyLeased` to append one non-transition ledger event:

```text
EventType = API_HUMAN_DECISION_RECORDED
StateFrom = ""
StateTo   = ""
```

The payload binds decision-request event ID, accepted answer, required-authority identity, authenticated service principal ID, delegated actor ID/type, policy version, authority-grant-file digest, request digest, and action operation ID. The run-transition lease serializes this final revalidation+append against competing transitions and against every other decision recorder; the second contender therefore observes the first deterministic decision record and cannot append a different answer. An unresolved transition barrier is neither consumed nor bypassed because the appended event has no state edge. Current lifecycle state remains `HUMAN_DECISION_REQUIRED`. The lease is released immediately after the append; only then does the action journal record `APPLIED` after the exact deterministic event is durably observable.

The service-side decision handler opens the registered ledger only through `OpenExistingJSONLLedger`; a missing ledger after claim is never recreated and therefore cannot manufacture a new authority history. If the process crashes or the append result is ambiguous after `ActionClaimV1`, retry does **not** append again. Read-only reconciliation searches the bounded authoritative snapshot for the deterministic event ID. Exact matching event bytes/semantics produce `RECONCILED_APPLIED`; a conflicting same ID is projection/integrity failure. If the deterministic event is absent and the ledger proof establishes no effect, record `RECONCILED_NOT_APPLIED`; otherwise remain `RECONCILIATION_REQUIRED`.

The future deferred resume/recovery seam MUST consume the exact recorded decision event as evidence and then construct/validate the existing `recovery.ResumeAuthority` under the appropriate same-attempt or new-attempt safety proof. EP-006 D neither constructs `ResumeAuthority`, changes state to `AUTHORITY_VALIDATED`, starts a process, cleans a worktree, nor creates a new attempt. There is therefore no second immutable authority type governing the same state edge.

## 13. Deferred action semantics

There is no `/actions/retry`, `/actions/resume`, or `/actions/recover` in EP-006 v1. The later bounded `PRODUCT_RUN_ADMISSION.md` extension conditionally adds `POST /v1/runs` when a concrete controller-owned admission profile is configured; without it, admission remains unsupported and `run_admission=false`.

- `FAILED` remains terminal unless a new run/attempt is created through an explicitly implemented admission/recovery path; general retry/recovery remains deferred.
- `RETRYING`, `CAPACITY_WAIT`, and `RECOVERY_REQUIRED` remain observable through projections but not remotely actuated by EP-006.
- capability discovery continues to report retry, resume, and recovery false.

The admission extension reuses the existing `abcp run` execution path and does not imply retry/resume/recovery capability.

## 14. Error model

Canonical errors contain stable machine code, safe message, request ID, retryable flag, reconciliation-required flag, and bounded safe details. They never include controller paths, secrets, raw provider bodies, tokens or stack traces.

Required classes include:

- invalid request / unauthenticated / authority denied;
- unsupported capability / not found;
- stale expected state / stale expected revision;
- request ID conflict;
- catalog integrity failure / action journal exhausted / action status not found;
- invalid cursor / cursor epoch changed / projection lineage changed / projection integrity failure;
- authoritative read busy;
- evidence integrity changed;
- action target not active / action state advanced;
- decision request invalid/already resolved/already recorded;
- reconciliation required;
- internal durable-substrate failure.

## 15. Failure-state matrix

- decode/validation/authentication failure: no receipt, no mutation;
- catalog/ledger snapshot acquisition busy: no receipt, no mutation;
- receipt persistence/fsync failure: no downstream mutation;
- post-receipt, pre-claim precondition failure: durable `REJECTED`;
- action-journal/catalog/run-transition lock acquisition exceeds 2 seconds before an effect starts: fail closed with no effect; a post-claim operation records not-applied/reconciliation status rather than replaying blindly;
- owner lease changes generation, retires, or is outside its closing watermark before cancel effect: no delivery to the different owner; the claimed operation is proven not applied or remains reconciliation-required;
- cancel claim/delivery with lost outcome: `RECONCILIATION_REQUIRED`, no resend;
- decision-record append success with lost journal outcome: deterministic-event reconciliation yields `RECONCILED_APPLIED`; no second append;
- ambiguous decision-record append: `RECONCILIATION_REQUIRED`, no automatic replay;
- an existing ledger transition barrier does not block the decision record because it is non-transition; the API cannot use the decision route to bypass or resolve that barrier;
- response write failure after durable action outcome: retry same request ID returns stored/reconciled outcome;
- projection parse/identity/truncation failure: read fails; authority unmodified;
- ledger flock timeout: `authoritative_read_busy`;
- evidence-download semaphore wait is context-cancellable; at most four verified-local artifact buffers exist concurrently;
- evidence read/digest/identity failure: no bytes returned;
- client disconnect while waiting for global snapshot slot cancels wait; after uncancellable bounded snapshot begins it completes under the four-reader ceiling and its response may be discarded.

## 16. Adversarial acceptance

Tests cover at least:

- unsafe service root/catalog file/symlink and duplicate registration;
- stale/PID-reused active lease, owner generation replacement, cancel admission vs watcher close race, and exact closing-watermark final drain;
- forged/modified cursor, changed key ID, wrong cursor kind, and catalog keyset pagination with insertions before/after the last-run key;
- missing registered ledger causes no filesystem mutation, read-only open/close and descriptor release across many runs, plus ledger replacement/truncation/partial final line;
- duplicate event ID with conflicting bytes;
- payload attempting to override run/state identity;
- excessive body/page/cursor/catalog/action-journal values, segment rollover and total journal exhaustion;
- arbitrary evidence path/URI injection;
- symlink/hard-link/special-file evidence replacement and SHA mismatch;
- anonymous read and command request;
- missing authenticator/cursor key/authority-grant file startup failure, insecure config permissions, wildcard authority grants;
- reused request ID with different semantic body;
- stale expected state/revision;
- cancel against inactive/wrong attempt/owner generation, unrelated non-transition append, transition racing final revalidation, watcher-latency/status behavior, shutdown final-drain ordering, and crash-after-delivery reconciliation;
- decision wrong event/run/attempt/state/answer/required-authority, unauthorized delegated actor, concurrent second-answer/transition race under run-transition exclusion, deterministic non-transition event replay/reconciliation, and already-resolved request;
- client disconnect after durable receipt;
- repeated attempts/tasks/sessions proving no overwrite.


## 17. Concrete service/configuration bounds

The concrete EP-006 server freezes these implementation values rather than leaving them to code review:

- bearer token file: <=4 KiB; token 32..512 bytes;
- cursor key file: <=8 KiB; key ID <=128 bytes; decoded key 32..64 bytes;
- authority grant file: <=64 KiB, <=16 principals, <=256 authorities/principal, <=256 bytes/string;
- delegated actor/principal IDs: <=128 bytes and identifier grammar defined above;
- command `request_id`: <=128 bytes using the service identifier grammar; `reason`: <=1 KiB UTF-8 with no NUL;
- action-specific JSON payload after decode: <=16 KiB and depth <=8 even though the outer HTTP body ceiling is 1 MiB;
- cursor validity: <=15 minutes;
- service filesystem lock acquisition (catalog/action journal): <=2 seconds;
- HTTP server: `ReadHeaderTimeout=5s`, `ReadTimeout=10s`, `WriteTimeout=30s`, `IdleTimeout=60s`;
- catalog list: <=200 rows and <=16 MiB registration bytes read;
- evidence downloads: <=4 concurrent, <=16 MiB each;
- active registered runs/watchers: <=256.

Any implementation need to widen one of these ceilings requires a separately bounded design change.

## 18. UI and admission deferral

ABCP ships no dashboard in EP-006. Repo C may combine ABCP projections with its own product/workspace/PDLC data.

The EP-006 core deliberately does not expose controller-local `authority.Manifest` fields. The bounded `PRODUCT_RUN_ADMISSION.md` extension supplies the safe higher-level Repo-C↔ABCP submission contract: callers provide only product identities/digests, task text, profile selection, repository base SHA, and delegated actor attribution; private manifest, paths, Ralphex/worktree settings, and workflow-authority storage remain controller-owned.

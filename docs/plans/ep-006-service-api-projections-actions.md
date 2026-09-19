# EP-006 A–D — Service/API, Projections, Timeline/Evidence, Governed Actions

Status: **A_DESIGN — corrected candidate for design review**
Exact original base: `c4f899072e31364f81453b2a5d6d90147c774107`

## 1. Authority and intent

User direction authorizes EP-006 Phase-5 capabilities A, B, C and D while leaving ABCP dashboard/UI deferred because `autonomous-development-platform` owns the stronger final product UI and consumes ABCP APIs. That product also integrates `dev_agent_automation_platform`; ABCP remains the common execution-governance core.

This plan does not grant implementation authority. It must pass `IMPLEMENTATION_DESIGN_GATE.md` and independent design review to produce `DESIGN_ACCEPTED` before B-stage code mutation.

## 2. Accepted design boundary

- Repo C owns product/user/workspace/project/PDLC/Governed-Task semantics and unified UX.
- ABCP owns execution governance, lifecycle truth, durable events/evidence, projections and the bounded API defined by EP-006.
- Repo A/Dev-Agent and Ralphex remain execution mechanisms/providers, not API contract owners.
- No public ABCP endpoint exposes controller paths, context capsules/checkpoints, Ralphex argv/config, worktrees or provider-private state.
- New-run admission/provider selection is deferred rather than leaking `authority.Manifest` upward.

## 3. Design-review correction closure

The first independent A-design review returned 0 Critical / 9 Major. This correction remains inside A–D and closes them by design:

- F1: exact actuation seam is the controller-owned service root + immutable runtime catalog + durable action journal; `abcp run --service-root` owns cross-process cancel observation. D path ownership includes `internal/actioncontrol`, the bounded `internal/run` cancellation-provenance extension and command wiring; `internal/recovery` is read-only.
- F2: generic `authority_ref` was removed and v1 exposes no retry/resume/recovery route. The intermediate proposal for a new human-decision authority was later removed by the third-review correction; final design records a non-transition decision event only.
- F3: idempotency substrate is a separate bounded action journal with create-once receipt and separate outcome records, not ledger byte-equality replay.
- F4: retry/resume routes were removed. No EP-006 action creates a new run/attempt or launches execution.
- F5: cursor mechanism is unconditionally stateless HMAC-SHA256 with explicit key ID/epoch and 4-KiB bound.
- F6: long-poll was removed; service uses at most four concurrent one-shot ledger snapshots and maps lock contention explicitly.
- F7: evidence download is limited to digest-bearing canonical local paths beneath the registered evidence root through `ReadVerifiedLocal`; all other refs are metadata-only.
- F8: human decision request ID is the originating `BLOCKER_DECISION_RECORDED` event ID; accepted-answer decoding is a frozen recovery decoder and non-enumerable forms fail closed.
- F9: every `/v1/*` route is authenticated; missing authenticator is startup failure.
- N1: raw paths are never returned.
- N2: unavailable physical ledger identity fails closed.
- N3: document precedence is explicit.

An additional design gap found during correction is also closed: multi-run discovery is an explicit safe runtime catalog; arbitrary ledger filesystem scanning is forbidden.

The second independent review of exact corrected head `e7750fd88003fdb381fdd76926aaf1f20842394d` closed all prior findings but returned 0 Critical / 5 Major plus 6 Minor. This correction closes them without scope expansion:

- M1: `GET /v1/runs` is catalog-only with a frozen non-lifecycle payload and consumes zero ledger snapshots; every other read uses at most one snapshot.
- M2: command admission still validates full projection revision, but cross-process cancel delivery binds/rechecks the admitted latest **state-transition event ID**, so unrelated non-transition events cannot make cancellation systematically stale.
- M3: action POST is explicitly asynchronous `202` and returns a stable operation/status URL; authenticated action-status GET plus a <=250-ms live watcher poll bound defines completion observation.
- M4: every effect has a durable `ActionClaimV1`; the decision-record event ID/timestamp are deterministic from the claim, and post-claim ambiguity is read-only reconciled instead of re-appended.
- M5: `AuthorityGrantFileV1` is a protected startup-frozen exact-grant surface; decision records both authenticated service principal and trusted-proxy delegated human attribution.
- m1: D ownership explicitly includes the bounded `internal/run` extension.
- m2: service-visible run/attempt filesystem identifier grammar is frozen.
- m3: cursor-key file provisioning/permissions/schema are frozen.
- m4: action journal uses bounded linked rollover epochs before its total hard ceiling.
- m5: active-owner proof uses captured Linux process-identity comparison, not stale-worktree `recovery.Inspect`.
- m6: the second-review transition-lease concern was initially removed from decision authority by making the answer event non-transition. The later final-gate correction reuses the existing run-transition lease only as bounded serialization for final revalidation+append; it does not create transition authority or consume a barrier.

The third independent review of exact head `ed8159d7ca11496fdada93e43ff30b9492edd0a4` closed all five prior Majors and six prior Minors but returned 0 Critical / 1 Major plus six new Minors. This correction closes them by simplifying D rather than adding authority:

- F1: the proposed `HumanDecisionAuthorityV1` is removed. `decision` now appends one deterministic **non-transition** `API_HUMAN_DECISION_RECORDED` event and leaves state `HUMAN_DECISION_REQUIRED`; the future deferred seam must consume that event and construct/validate the existing `recovery.ResumeAuthority`.
- m1: service lock acquisition is <=2 seconds and no journal/catalog lock is held across ledger flock/snapshot/append. The later final-gate correction adds the existing run-transition lease as the outer serialization primitive for final action effect, with journal/catalog locks released before ledger work.
- m2: a single typed decoder recognizes the existing non-transition `RUN_CREATED` event/payload as initial state; no other payload may supply lifecycle state.
- m3: the D runner extension covers every current cancellation exit, including the process-error path that could otherwise classify typed API cancellation as `FAILED`.
- m4: decision recording remains non-transition; using `AppendOrVerifyLeased` under the run-transition lease serializes it with competing transitions without consuming, bypassing, or resolving an existing transition barrier.
- m5: verified evidence download concurrency is capped at 4 (<=64 MiB artifact buffers) in addition to the snapshot ceiling.
- m6: no new `internal/recovery` authority is created, so its evidence-ref contract is not bypassed; the decision ledger event itself is durable evidence for the later existing authority seam.

The correction also freezes previously implicit implementation bounds for token/key/grant files, IDs/reason/payload, cursor lifetime, service locks, HTTP timeouts, catalog bytes, and download concurrency so Track A/D do not invent them during coding.

The final independent gate of exact head `9be031d8d5ce0206c85765b371ab4e532fd9e7c7` verified every prior Critical/Major closure but returned 1 Critical / 4 Major / 3 Minor. This bounded A-design correction closes them:

- C1: cancel receipts/claims/request events and resulting `CANCELLED` provenance bind one exact monotonic `ActiveOwnerLeaseV1.lease_id`; admission and owner closing are serialized by action-journal -> catalog locking with a durable closing watermark, so replacement generations cannot consume prior cancellations.
- M1: final cancel/decision revalidation and deterministic event append run under the existing run-transition lease; cancel retains it through the nonblocking typed context-cancel invocation, then releases it before the runner emits `CANCELLED`. Journal/catalog locks are never held across ledger operations.
- M2: Track B is authorized to add a restricted `OpenExistingReadOnlyJSONLLedger` plus an existing-file writable `OpenExistingJSONLLedger` for later governed action effects, and idempotent `JSONLLedger.Close`; neither path may create a missing ledger. Service composition open/snapshot-or-effect/closes without caching, under one global object semaphore. The object/persistent-FD ceilings are explicit.
- M3: `CursorEnvelopeV1` has distinct signed `ledger` and `catalog` payloads. Track A owns the signer and catalog payload; B only adds ledger lineage payloads. Catalog pagination is lexical HMAC-protected keyset pagination with explicit weak-consistency insertion behavior and no ledger dependency.
- M4: Track C may narrowly amend `internal/evidence/read_linux.go` plus its dedicated test so the exact opened/rechecked descriptor must have `Nlink == 1`; the hard-link guarantee is therefore enforced by the same verified reader.
- m1: active-owner process identity text now matches `CaptureProcessIdentity` exactly: PID + boot ID + Linux start ticks.
- m2: projection current state is explicitly `RUN_CREATED` initial decoding followed only by valid transitions.
- m3: stale plan text granting D an `internal/recovery` extension is removed; recovery remains read-only.

## 4. Implementation sequence

Default sequence is serial:

```text
A service foundation + catalog/security contract
 -> A deterministic acceptance + bounded review
B read-model/cursor engine
 -> B deterministic acceptance + bounded review
C timeline/evidence
 -> C deterministic acceptance + bounded review
D action journal + cancel + human decision recording
 -> D deterministic acceptance + bounded review
combined EP-006 deterministic acceptance
 -> exact-head C-stage review
 -> publication/merge/post-merge proof
```

B and C may overlap only after A is frozen and an explicit disjoint-file proof exists. D begins after A contracts and B revision semantics are frozen.

## 5. Track A — path authority and acceptance

Owned maximum:

```text
internal/serviceapi/**
internal/runtimecatalog/**
cmd/abcp/main.go
cmd/abcp/main_test.go
docs/architecture/EP006_SERVICE_API_CONTRACT.md
```

A implements service-root safety, strict run/attempt identifier grammar, run/attempt catalog, monotonic active-owner lease generations and the bounded owner-lease guard, DTO/contracts, authentication, protected cursor-key/grant-file loading, shared HMAC `CursorEnvelopeV1` signer/verifier plus the signed `catalog` keyset-cursor variant, loopback server, capability discovery, canonical errors, bounded decode/encode, catalog-only paginated run listing, concurrency/timeouts, and dependency interfaces. `abcp run --service-root` registers exact runtime metadata but A does not implement action observation yet.

Acceptance includes unsafe path/symlink/ownership tests, duplicate/conflicting registration, monotonic owner-generation/install/closing/retire guards, stale/PID-reused owner proof, authenticated-all-routes proof, nil-authenticator/insecure-key/insecure-grant startup rejection, signed catalog keyset cursor/tamper/epoch/insertion behavior, zero-snapshot run-list proof, hard limits, deterministic capability response, and full repository regression tests.

## 6. Track B — path authority and acceptance

Owned maximum:

```text
internal/readmodel/**
internal/ledger/jsonl.go
internal/ledger/jsonl_test.go
internal/ledger/readonly.go
internal/ledger/readonly_test.go
```

B consumes catalog-resolved existing ledger images. It implements deterministic projections, revision identities and only the `ledger` cursor payload behind A's frozen HMAC envelope/signer. Its narrowly authorized ledger substrate extension adds `OpenExistingReadOnlyJSONLLedger` (restricted Snapshot/Close only), `OpenExistingJSONLLedger` (existing-file writable object for later governed service action effects), and idempotent `JSONLLedger.Close`; neither constructor may create the parent, ledger or control paths when absent. Read-model service reads use only the restricted read-only type and never mutate ledger/controller state.

Acceptance includes rebuild determinism, current/historical preservation, impossible-history rejection, unavailable physical identity rejection, signed `ledger` cursor lineage/epoch behavior while preserving A's catalog cursor bytes, bounded pagination/four-reader snapshot coordination, read-only and writable existing-file constructors causing no missing-ledger mutation, replacement races, idempotent close, and persistent-descriptor counts across more than eight registered runs.

## 7. Track C — path authority and acceptance

Owned maximum:

```text
internal/timeline/**
internal/evidence/read_linux.go
internal/evidence/read_linux_test.go
```

C normalizes timeline entries and builds the ledger-backed evidence index. It invokes the verified local evidence reader only with the exact registered evidence root; caller paths are impossible. Its only evidence-package mutation is the Linux `Nlink == 1` check on the same opened/rechecked descriptors already used by `ReadVerifiedLocal`, with a dedicated regression test.

Acceptance includes ledger order, opaque evidence-ID binding, metadata-only nonlocal/nondigest refs, path containment, symlink/hardlink/special-file replacement rejection, digest verification, download bound, and history preservation.

## 8. Track D — path authority and acceptance

Owned maximum:

```text
internal/actionapi/**
internal/actioncontrol/**
internal/run/run.go
internal/run/run_test.go
cmd/abcp/main.go
cmd/abcp/main_test.go
```

D implements the bounded segmented action journal, asynchronous operation-status surface, durable receipt/claim/outcome protocol, active-run cancel observer/watcher wiring, API cancel adapter, state-transition-event epoch revalidation, typed cancellation provenance across every runner cancellation exit, and a typed human-decision decoder plus deterministic **non-transition** decision-record event with exact grant matching/delegated-human attribution. Existing recovery/resume authority is read-only. D does not implement retry/resume/recovery/new-run/provider selection.

Acceptance includes:

- receipt-before-effect and separate-outcome durability;
- `(principal, request_id)` idempotency and conflicting-body detection;
- journal segment/epoch/total bounds, linked rollover and cross-process locking;
- asynchronous `202` + authenticated action-status read semantics;
- cancel exact run/attempt/admitted owner-lease generation, closing-watermark, admission-revision and state-transition-event checks;
- no OS-signalled cancellation from the service;
- authoritative `CANCELLED` transition carries the exact action `operation_id`;
- <=250-ms live cancel watcher polling plus crash-after-delivery read-only reconciliation and no automatic resend;
- decision request decoder bound to exact blocker event;
- protected exact-grant-file, delegated actor and accepted-answer/required-authority enforcement;
- durable action claim plus run-transition exclusion spanning final state/request revalidation through deterministic non-transition event append (and through typed cancel invocation for cancel);
- already-resolved/stale/second-answer rejection and ambiguous-append deterministic reconciliation without second append;
- lifecycle state remains `HUMAN_DECISION_REQUIRED`; no duplicate ResumeAuthority or transition barrier interaction;
- outcome references authoritative ledger event IDs;
- no direct GitHub/Ralphex/Git/cleanup authority in API packages.

## 9. Combined deterministic acceptance

At minimum:

```bash
go test ./internal/serviceapi ./internal/runtimecatalog ./internal/readmodel ./internal/timeline ./internal/actionapi ./internal/actioncontrol
go test ./internal/run ./internal/recovery ./internal/ledger ./internal/evidence
go test ./internal/governance ./internal/integrationgate ./internal/githublifecycle ./internal/mergelifecycle
go test -race ./...
go vet ./...
go test ./...
```

Black-box acceptance starts a loopback server with disposable service-root/ledger/evidence fixtures and proves authenticated signed catalog-keyset/list/detail reads, read-only ledger open/close with no missing-path mutation, cursor polling, explicit RUN_CREATED projection, timeline/evidence with hard-link/download-concurrency checks, async action status, exact owner-generation-bound cross-process cancel across unrelated non-transition appends/every runner cancellation exit/shutdown races, atomic transition-vs-cancel and second-decision exclusion, attributed non-transition decision recording with state unchanged, receipt/claim idempotency, segmented-journal rollover, deterministic crash reconciliation and restart behavior without external services.

## 10. Stop conditions

Return to A design authority if implementation discovers any need to:

- expose raw filesystem/controller paths;
- add retry/resume/recovery/new-run/provider routes;
- create another lifecycle authority sequence;
- weaken existing recovery/merge/governance semantics;
- add product workspace/user/fine-grained RBAC ownership to ABCP;
- add an ABCP dashboard/UI;
- expand to PostgreSQL/multi-host/secret broker;
- copy Dev-Agent executor internals;
- change external v1 semantics incompatibly;
- widen hard resource ceilings.

# EP-005 — Exact-Head PR Lifecycle Post-Implementation Review Corrections

## Authority and scope

This correction track starts from ABCP-accepted implementation head `f986008ba11c69c3864f0b8977af440024d12048`. It corrects the exact-head PR lifecycle implementation only; it adds no CI ingestion, merge execution, domain state transition, service/API/dashboard, Phase 5+ behavior, or broader GitHub functionality.

The accepted implementation passed ABCP deterministic acceptance, but the required risk-triggered post-implementation review could not use Claude Code because the configured provider hit a session/quota limit. Under `docs/architecture/DECISION_AND_ACCEPTANCE_POLICY.md`, controller fallback reviewed the same exact accepted SHA and found one Critical and nine Major defects. Those findings are substantive and must be corrected before push or PR creation.

Controller review artifact: `/home/devagent/abcp-runtime/ep006-pr-lifecycle/post-implementation-review/controller-review.json`
Artifact SHA-256: `5919918bb38fee0ced9c858cd8109a63f48f600c0dfcad39ff1f2408eb8e3e4d`

## Correction invariants

### 1. Production construction is controller-owned and cannot re-key the physical PR resource — CRITICAL

The production lifecycle must have exactly one controller-authorized admission-store identity for the host and exactly one authoritative controller ledger identity. A plan, run, request, caller, plugin, or arbitrary package consumer must not select an alternative admission root or material-ledger path.

Production construction must therefore be separated from test injection:

- the production entry point obtains the stable admission root from controller-owned host configuration loaded at process startup, not from a request/run/plan field;
- arbitrary admission roots are available only behind an unexported or explicit test-only boundary;
- the production material recorder is constructed from the controller's exact canonical `*ledger.JSONLLedger` (or an equivalently unforgeable controller binding) and derives its path from that object; a caller may not pair `nil` with an arbitrary production path;
- the store pins the canonical root directory identity/descriptor for its lifetime and all resource/admission operations are relative to that pinned root;
- a second root or ledger identity for the same production controller is a policy/integrity failure, never a new interlock.

### 2. Admission locking is descriptor-relative and replacement-safe — MAJOR

The store must use the pinned root descriptor plus `openat`/`fstat`-style no-follow operations for resource locks, records, inventory, and directory fsync. The exact resource-lock descriptor on which `flock` is acquired must have the same device/inode identity recorded for that resource. A replacement between initial discovery and lock acquisition must fail closed. The capacity lock identity remains pinned and validated.

### 3. Reconciliation authority is consumed before any remote read — MAJOR

For a submitted/no-terminal generation, determine the next reconciliation round under the physical-resource lock before `GET /user`, ref reads, PR discovery, or exact-PR reads. If a prior round exists, enforce the 30-second minimum interval before any network call.

A reconciliation invocation that is admitted to perform remote reads owns one durable round even if principal/ref/discovery/postflight reads fail. Use an immutable two-phase namespace per ordinal: create `reconcile-<n>-start.json` under resource+capacity authority **before any network call**, binding attempt/write ID, previous-round digest, admitted timestamp, limits/policy identities and round ordinal; after bounded reads, create at most one `reconcile-<n>-observation.json` binding the start digest plus sanitized outcome/proof. Neither file is rewritten. A start record without observation is still a consumed round after crash. At most eight start records and the configured aggregate byte/rate bounds apply to all reconciliation attempts, not only successful ones.

### 4. Divergence terminal is disposition-specific and does not require a successful snapshot — MAJOR

`remote_diverged_after_write` is a durable unresolved terminal, not a successful PR result. It must be constructible when a known/possibly-applied mutation is followed by:

- moved head or base;
- closed or merged PR;
- PR identity/repository/ref/head-label mismatch;
- exact desired document mismatch;
- CREATE author/principal mismatch;
- another bounded exact-postflight validation failure after enough remote evidence exists to classify divergence.

Confirmed/reconciled success still requires the fully validated frozen snapshot and `ValidatePullRequestWriteResult`. Divergence terminalization instead stores bounded raw normalized PR/head/base/principal observations, exact error classification, attempt/generation/marker identities, and an optional snapshot only when one was valid. Recovery of a divergence terminal can only reconstruct the same unresolved/no-retry result. It can never report success or authorize another revision.

### 5. TerminalBudgetV1 is a real pre-submit upper bound — MAJOR

Before publishing the submitted marker, the controller must mathematically prove that every terminal disposition reachable from that write fits the 256-KiB reservation. The bound must include every actual persisted component, including both source and derived authority forms when distinct, WriteAttempt, title/body, primitive recovery material, principal/PR/ref observations, reconciliation material, result core, reason/failure fields, event material, and any self-contained evidence bytes retained for recovery.

Do not double-store large evidence merely because it is convenient. Prefer terminal-owned normalized canonical observation wires plus artifact names/kinds/digests where those wires are already sufficient for restart reconstruction. If self-contained artifact bytes are required, their exact count and maximum aggregate size must be included in the pre-submit budget. Maximal valid test values must either provably produce a terminal `<= 256 KiB` or be rejected before marker publication and before `http.Client.Do`.

### 6. Same logical revision recovers across governed runs — MAJOR

The terminal's originating run ID remains immutable provenance, but it is not an idempotency key. A later governed run presenting the same canonical request/authority/document must be able to acquire the same physical-resource lock, validate the terminal and admission chain, and recover without GitHub calls.

Recovery republishes the terminal's self-contained evidence into the current run's evidence store using current-run artifact URIs while proving content/name/kind/digest equivalence. It returns a current-run `PRLifecycleResultV1` referencing those current immutable artifacts. Absolute URIs from the originating run are provenance, not an equality requirement. The original terminal/event identity remains unchanged.

### 7. Transport-level response-header bounds are enforced through the sealed authenticated transport — MAJOR

Production GitHub construction must guarantee `MaxResponseHeaderBytes = 32 KiB` at the underlying network transport even when authentication is implemented by a wrapper `RoundTripper`. Post-parse `boundHeaders` remains defense in depth but is not the primary allocation bound.

Use a controller-owned sealed transport construction/factory or an interface whose invariant can be verified, rather than accepting an opaque wrapper whose underlying transport policy cannot be proven. Credentials remain transport-only and are never exposed to lifecycle methods.

### 8. CREATE postflight binds list and full-PR identities — MAJOR

After CREATE, filtered discovery must return exactly one raw candidate. Preserve both candidate number and node ID. The subsequent full-PR GET must match both values before deriving PR-bound postflight authority. A mismatch is fail-closed and follows the disposition-appropriate divergence/ambiguity path; it is never silently rebound to the full response.

### 9. Evidence capture/recovery uses the repository's safe reader — MAJOR

Every read or re-read of a controller evidence artifact must use `evidence.ReadVerifiedLocal` (or an equivalent no-symlink, bounded, identity-stable primitive), not `os.ReadFile`. The caller must bind the exact controller-owned evidence root/run directory and expected `EvidenceRef` digest. Path replacement, symlink substitution, size growth, or digest mismatch is an integrity failure.

### 10. Persisted errors are sanitized classes, never arbitrary transport strings — MAJOR

Admission records, reconciliation records, terminal material, run evidence, and ledger payloads must never serialize raw `error.Error()` text from the authenticated transport/provider. Persist stable controller-owned error class/code plus explicitly safe bounded fields. Raw provider bodies and headers remain excluded. Diagnostic strings that originate only from controller-owned constants may be recorded, but arbitrary transport errors remain memory-only.

## Required adversarial regression matrix

- [x] Production factory cannot select two admission roots for one host/controller; test-only root injection cannot escape into production API.
- [x] Production material recorder derives exact controller ledger path; nil/arbitrary production ledger binding is impossible.
- [x] Root directory replacement and resource-lock replacement between discovery/open/flock fail closed; the locked fd must equal the remembered inode.
- [x] Two concurrent processes for one physical PR cannot acquire different lock inodes or roots and cannot submit twice.
- [x] Submitted/no-terminal immediate retry performs zero GitHub calls before the 30-second interval.
- [x] Reconciliation creates immutable `round-start` before its first GitHub call and optional `round-observation` afterward; crash/failure after start still consumes the round, repeated failures stop at eight starts, and byte/rate caps include both records.
- [x] Moved head, moved base, closed PR, merged PR, repository/ref/head-label mismatch, document mismatch, and CREATE author mismatch each create a durable `remote_diverged_after_write` terminal and restart returns the same unresolved result with zero GitHub writes.
- [x] Divergence terminal does not require a valid frozen snapshot; confirmed/reconciled success still does.
- [x] Terminal budget maximal-value tests cover all stored components and prove final canonical terminal cannot exceed its pre-submit reservation.
- [x] A terminal-budget overflow is rejected before submitted marker and before `Do`.
- [x] A second controller/evidence store with a different run ID recovers the same terminal with zero GitHub calls and current-run evidence refs.
- [x] Wrapped authenticated transport still enforces the 32-KiB network response-header cap before application parsing.
- [x] CREATE discovery candidate node-ID/full-GET node-ID mismatch fails closed.
- [x] Evidence symlink/path/inode replacement during terminal capture or publish-or-verify is rejected through the safe evidence reader.
- [x] A transport error containing a fake secret/token string never appears in admission files, evidence, terminal material, ledger material, or returned persisted diagnostics.
- [x] Existing exact CREATE/UPDATE, stable numeric actor, one-submission ambiguity, no-replay, capacity, restart, material-ledger, and unsupported-platform tests remain green.

### Task 1: Correct the exact-head PR lifecycle implementation

- [x] Implement the ten correction invariants above without modifying frozen `internal/githublifecycle` semantics unless a test proves an unavoidable foundation defect.
- [x] Keep the package fail-closed and preserve exactly one remote PR submission per revision.
- [x] Add all adversarial regressions above.
- [x] Update the completed lifecycle contract/plan only where necessary to record corrected implementation semantics; do not erase prior design/review history.
- [x] Run `gofmt -w` on changed Go files.
- [x] Run focused `go test ./internal/prlifecycle`.
- [x] Run `go test ./...`.
- [x] Run `go test -race ./...`.
- [x] Run `go vet ./...`.
- [x] Run `make smoke`.
- [x] Run `git diff --check f986008ba11c69c3864f0b8977af440024d12048 HEAD`.
- [x] Commit only after every required validation passes.

## Completion gate

The correction is not push-ready merely because Ralphex completes. The exact correction head must pass ABCP deterministic acceptance and then a fresh Critical/Major post-implementation review. Claude/cross-model review is preferred when available; documented controller fallback is permitted only for genuine provider failure. Any substantive finding requires another correction cycle.

No branch push, PR #8 creation, merge, CI lifecycle work, or later Phase-4 task is authorized until that exact-head review is clean.

## Round-2 exact-head correction addendum (2026-09-07)

The later exact-head review correction narrows only `internal/prlifecycle`; frozen `internal/githublifecycle` limits and semantics remain unchanged.

- PR-lifecycle title and body admission is now explicitly capped at 1024 bytes per field, no greater than the controller's observable remote-text limit. The exact canonical CREATE/PATCH JSON and HTTP request are built and checked against the 16-KiB request cap before generation or submitted-marker publication.
- A reconciliation principal read failure remains ambiguous and consumes only its already-started reconciliation round. An observed numeric principal mismatch is durable divergence evidence, while `PRLifecycleResultCoreV1` remains bound to the authority actor ID and cannot combine that ID with the mismatching node ID/login.
- Every failure propagated after `http.Client.Do` may have run is normalized to a controller-owned submitted error containing a stable code and write-attempt identity. A locally proven never-submitted post-marker failure remains non-replayable.
- A new revision after `applied_confirmed` directly reads and re-proves the prior terminal-bound PR number/node ID, repository, open state, refs, head/base authority, and prior document before UPDATE authority can be admitted. Filtered discovery absence can never switch that resource back to CREATE.
- Prepare provenance is run-scoped and named `r-<resource>-rev-<ordinal>-prepare-run-<sha256(raw-run-id)>-<round>.json`. Its strict canonical body binds the raw run ID and digest, resource, pending revision, and round. Each run retains three rounds, while each physical resource plus pending revision is capped at 24 immutable prepare records; the next revision gets an independent allowance.
- `TerminalBudgetV1` now itemizes the exact component caps and uses canonical JSON length for the direct title/body copy, including HTML escaping. Principal, PR, ref, reconciliation, snapshot, retained artifact, authority, attempt, and result-core material are checked against the same caps used by the reservation, and the final canonical terminal is checked against both the computed upper bound and 256 KiB before publication.

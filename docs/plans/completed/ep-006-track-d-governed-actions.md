# EP-006 Track D — Governed Action Journal, Cancel, and Human Decision

Exact predecessor / accepted Track-C head: `e1b5367ea82e774d81b10c989d506efe16b7d5bc`.
Accepted EP-006 design: `908710e1606b0da761e612c36b85c866e182c7f7`.
Accepted Track-A semantic head: `f082cae1677d002825b7f4176d9783e199c9a125`.
Accepted Track-B semantic head: `0097effa0e94340980a9ab5483abb5359ebfc2a1`.
Track-C final closure gate: 0 Critical / 0 Major / 0 Minor, exact-head VERIFIED.

This is Track D only. A/B/C are frozen and no EP-006 publication/merge occurs at this boundary.

## Maximum ownership

```text
internal/actionapi/**
internal/actioncontrol/**
internal/run/run.go
internal/run/run_test.go
cmd/abcp/main.go
cmd/abcp/main_test.go
this plan and its completed-plan move
```

Everything else is read-only, including `internal/serviceapi/**`, `internal/runtimecatalog/**`, `internal/readmodel/**`, `internal/ledger/**`, `internal/timeline/**`, `internal/evidence/**`, `internal/recovery/**`, scheduler/integration/GitHub/merge lifecycle, accepted design docs, and tracking docs.

### Task 1: implement the accepted governed action capability

- [x] Implement a dedicated bounded segmented action journal under the configured service root with owner-only safe directories/files, no symlink/hard-link traversal, cross-process lock acquisition <=2 seconds, append-only strict canonical records, fsync durability, create-or-verify recovery, and no reuse of ledger event storage.
- [x] Freeze action-journal bounds exactly: <=8 MiB / 32,768 records per segment, <=8 monotonically linked epochs / 262,144 records per run, prior-segment final-record digest linkage, fail closed with `action_journal_exhausted`, and no compaction/deletion that erases idempotency history.
- [x] Implement `ActionReceiptV1`, `ActionClaimV1`, and separate append-only `ActionOutcomeV1` progression exactly as accepted: `RECEIVED -> REJECTED` or `RECEIVED -> CLAIMED -> APPLIED`, with the only ambiguity branch `CLAIMED -> RECONCILIATION_REQUIRED -> RECONCILED_APPLIED|RECONCILED_NOT_APPLIED` and no automatic effect replay after claim.
- [x] Bind idempotency to canonical `(principal_id, request_id)` plus semantic request digest; same intent returns the same operation/timestamps, conflicting intent returns `request_id_conflict`. Receipt must be durable before any controller effect.
- [x] Keep all command inputs bounded and caller-safe. Implement only cancel and decision payloads; caller paths, PIDs, authority manifests, Ralphex args, cleanup paths, arbitrary evidence refs, retry/resume/recover/new-run/provider-selection inputs are forbidden.
- [x] Implement a concrete `serviceapi.ActionController` without mutating `serviceapi`: authenticated `202` admission/status semantics, bounded operation status, stable authoritative event IDs, canonical service errors, and no internal path disclosure.
- [x] Preserve strict lock order. The only nested service-filesystem order is action-journal -> catalog for cancel admission / closing watermark. Release journal locks before ledger/run-transition operations. Never hold journal or catalog locks while acquiring ledger flock/run-transition lease except the accepted brief catalog re-proof while the run-transition lease is held.
- [x] Use only frozen public predecessor seams: `runtimecatalog.Catalog`/`OwnerLeaseGuard`, `readmodel.Service.Snapshot`, `readmodel.Service.WithExistingWritableLedger`, `ledger.AcquireRunTransition`, `ledger.AppendOrVerifyLeased`, and frozen service interfaces. Do not modify those packages.
- [x] Cancel admission must validate exact registered run/attempt, expected state, full expected projection revision, latest authoritative state-transition event ID, and exact live owner lease generation; persist the exact `owner_lease_id` and admitted journal sequence atomically under action-journal -> catalog ordering.
- [x] Implement one bounded watcher for the exact `abcp run --service-root` owner generation, polling only its own action journal at <=250 ms while ACTIVE and through bounded CLOSING final drain. No OS signal is used for service cancellation; no later owner generation may inherit an earlier request.
- [x] Watcher claim freezes exact admitted owner lease plus deterministic `API_CANCEL_REQUESTED` event ID/timestamp. After releasing the journal lock, acquire the run-transition lease, briefly re-prove the exact delivery-eligible owner lease, take one fresh bounded authoritative snapshot, and require unchanged admitted latest state-transition event/current state before any effect.
- [x] While holding the run-transition lease, append-or-verify exactly one non-transition `API_CANCEL_REQUESTED` event binding operation/principal/request digest/owner lease, then invoke only an in-process nonblocking context cancellation with typed cause `{operation_id, owner_lease_id}`. Release the transition lease before the runner emits its terminal state transition.
- [x] Extend `internal/run` only enough to centralize typed API-cancel provenance and apply it to every current runner cancellation exit, including validation, supervisor/process, pre-acceptance, acceptance, and process-error branches. API-caused `CANCELLED` must bind exact `operation_id` + `owner_lease_id` and require the durable request-event provenance; non-API failures/cancellation remain byte/behavior compatible.
- [x] Record cancel `APPLIED` only after observing an authoritative `CANCELLED` transition with the same operation and owner lease, referencing both request and transition events. Crash/lost outcome after delivery becomes reconciliation-required; read-only reconciliation may classify applied/not-applied but never resend automatically.
- [x] On owner shutdown, bind a final journal sequence watermark, transition the exact lease ACTIVE -> CLOSING with `drain_through_journal_sequence`, allow only eligible <=watermark work to drain, then retire that exact lease. Preserve generation isolation and stale/PID-reuse rejection.
- [x] Implement the frozen human-decision decoder in `internal/actionapi`: accept only the exact originating blocker/decision requirement event semantics, same run/attempt, current `HUMAN_DECISION_REQUIRED`, valid question/RequiredAuthority, unique nonempty accepted answers, no superseding state transition, and no prior decision record for the same originating event.
- [x] Decision payload is only `{decision_request_id, answer}` and requires delegated actor. Validate exact accepted answer, exact authority grant, and delegated-actor permission through the frozen `AuthorityMatcher`; a different request ID cannot record a second answer for the same decision request.
- [x] Decision claim freezes deterministic `API_HUMAN_DECISION_RECORDED` event ID/timestamp. Under the run-transition lease, take one fresh bounded snapshot, revalidate admitted transition/current blocker/no-prior-answer/answer/grant, and append-or-verify one non-transition decision event with exact principal, delegated actor, policy/grant digest, request digest, required authority, answer, originating event, and operation ID.
- [x] Decision must leave lifecycle state `HUMAN_DECISION_REQUIRED`, must not consume/bypass a transition barrier, must not construct or duplicate `recovery.ResumeAuthority`, and must not start/restart a process, clean a worktree, create an attempt, or perform recovery. Ambiguous post-claim append is reconciliation-only; exact deterministic event proof may yield `RECONCILED_APPLIED` without a second append.
- [x] Compose the frozen B/C read services plus D action controller in `abcp serve` using existing service-root/catalog/cursor/auth/grant inputs. Preserve all existing routes/capabilities and keep retry/resume/recovery/new-run capabilities false.
- [x] Wire `abcp run --service-root` to the exact owner-generation watcher and typed cancellation context without changing runs that omit `--service-root`; preserve existing signal cancellation behavior independently of API-cancel provenance.
- [x] Add adversarial tests for receipt-before-effect durability, cross-process journal locking, segment rollover/exhaustion, idempotent replay/conflicting digest, exact operation status, owner replacement/closing watermark/watcher-close races, stale state/revision/attempt/lease, unrelated non-transition append, state-transition races, crash-after-delivery reconciliation/no resend, and exact cancellation provenance on every runner cancellation exit.
- [x] Add decision tests for wrong originating event/run/attempt/state/answer/authority, duplicate/second answers, unauthorized delegated actor, transition races, existing transition barriers, deterministic append replay/ambiguity reconciliation, missing ledger no-create behavior, lifecycle remaining `HUMAN_DECISION_REQUIRED`, and zero `ResumeAuthority` construction.
- [x] Preserve all Track-C evidence-buffer/resource corrections byte-for-byte and keep service action work within the global ledger-object/snapshot limits exposed by the frozen readmodel service. No new cache, unbounded goroutine-per-action scheme, long poll, network call, process wait, or provider call is allowed in transition critical sections.
- [x] Run gofmt; focused actionapi/actioncontrol/run/cmd tests and race; predecessor critical concurrency stress; full uncached `go test ./...`; full uncached `go test -race ./...`; `go vet ./...`; Darwin/Windows production builds where supported; `git diff --check`; exact owned-path/frozen-path audits; and forbidden retry/resume/recover/new-run checks.
- [x] Mark every item complete, move this plan to `docs/plans/completed/`, create a bounded Track-D implementation commit, and leave the exact worktree clean.

## Completion boundary

Ralphex completion is implementation only. Fresh deterministic Track-D acceptance and a fresh independent exact-head Critical/Major review at 0C/0M are mandatory before combined EP-006 A–D acceptance/publication. No tracking-doc reconciliation occurs here.

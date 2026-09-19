# EP-006 — Service API, Projections, Timeline/Evidence, and Governed Actions

**Roadmap phase:** Phase 5 — Service/API and dashboard
**Base authority:** `c4f899072e31364f81453b2a5d6d90147c774107`
**Scope decision:** implement Phase-5 service/API capabilities A–D; defer ABCP-owned dashboard/UI.

## 1. Objective

Expose ABCP's governed execution core through a stable, versioned service contract so `pankajleh/autonomous-development-platform` can consume authoritative run state, history, evidence, and bounded governed actions without importing ABCP internals, scraping controller files, or copying governance algorithms.

ABCP remains the common execution-governance authority. The API is transport and projection over existing controller truth; it is never a second authority system.

## 2. Cross-repository boundary

```text
Repo C — autonomous-development-platform
  product/users/workspaces/projects/PDLC/unified UX
                  |
          versioned ABCP API
                  v
Repo B — autonomous-builder-control-plane
  authority/scope/scheduler/recovery/review/acceptance/
  integration/GitHub lifecycle/event+evidence truth
                  |
          execution provider seam
             /          \
Repo A — Dev-Agent      Ralphex
```

EP-006 MUST NOT copy Repo C product concepts into ABCP or copy Repo A executor mechanics into ABCP.

## 3. Authorized tracks

### A — Service/API contracts, runtime catalog, and security boundary

Implement a versioned HTTP/JSON boundary, authenticated principal abstraction, capability discovery, canonical request/error envelopes, bounded pagination, a controller-owned service root, a safe multi-run catalog, active-run ownership registration, server lifecycle, and exact dependency interfaces.

Only runs started with explicit EP-006 service registration are visible/actionable. Legacy arbitrary ledger/evidence paths are not discovered by filesystem scan and are not silently imported.

### B — Projection/read-model engine

Build deterministic query projections from registered authoritative ledger snapshots. Preserve current and historical runs, attempts, tasks and agent sessions without overwriting prior history. Projection state is derived/read-only and rebuildable from authoritative events.

### C — Timeline and evidence model

Expose normalized ordered timeline entries and ledger-backed evidence descriptors. Evidence access uses opaque API identifiers derived from authoritative references. Callers never supply filesystem paths or evidence URIs. Only digest-bearing local evidence proven inside the registered controller evidence root is downloadable.

### D — Governed action API

Implement two bounded v1 actions for already-admitted, service-registered runs:

1. `cancel` — cross-process cancellation of one currently active exact run/attempt through a durable controller action journal observed by the registered run process; no OS signal guessing.
2. `decision` — record one exact authorized answer for an outstanding `HUMAN_DECISION_REQUIRED` blocker as deterministic non-transition controller evidence. It enforces the finite accepted-answer set and required authority but does not transition/resume the run or duplicate `recovery.ResumeAuthority`.

The v1 API does NOT expose generic retry, resume, recovery, new-attempt creation, or new-run admission. Those require the later Repo-C↔ABCP admission/provider seam and capability discovery reports them unavailable.

## 4. Explicitly deferred

The following are NOT part of EP-006:

- ABCP dashboard or customer UI;
- product users/workspaces/projects/repository-management semantics;
- product-level Governed Task or PDLC UX;
- new-run/public manifest admission;
- generic retry/resume/recovery execution APIs;
- production RBAC/SSO/tenant policy;
- PostgreSQL event-store migration;
- multi-host worker leasing;
- secret-broker integration;
- metrics/alerts/retention/backup;
- provider-selection implementation;
- WebSocket/SSE infrastructure.

Repo C may build its stronger UI independently against the accepted API.

## 5. Normative document precedence

1. `docs/architecture/EP006_SERVICE_API_CONTRACT.md` is authoritative for API semantics, security, durable service substrates, action semantics, cursor/evidence behavior, and failure handling.
2. This execution pack is authoritative for EP-006 scope, hard resource ceilings, completion criteria, and explicit deferrals.
3. `docs/plans/ep-006-service-api-projections-actions.md` is authoritative for implementation sequence and path ownership only and cannot weaken either document above.

When two limits differ, the stricter limit applies.

## 6. Core invariants

1. Event/evidence/controller state remains authoritative; catalog, action journal, projections, and HTTP responses are not lifecycle authority.
2. No endpoint may fabricate acceptance, merge readiness, merge success, blocker resolution, recovery/resume authority, or execution restart. A recorded human answer leaves lifecycle state `HUMAN_DECISION_REQUIRED`.
3. Every `/v1/*` route requires an authenticated principal. Missing authenticator is server startup failure.
4. Every command binds principal, client request ID, canonical semantic-body digest, exact run/attempt, expected state, expected projection revision, and controller result evidence.
5. Repeating `(principal_id, request_id)` with the same semantic digest reconciles the existing action; reuse with different intent is an integrity conflict.
6. Action receipt is durable before side effect; action outcome is a separate durable record. Ambiguous effect is reconciled read-only and never blindly replayed.
7. Cursor integrity is unconditional HMAC-SHA256 with one configured key ID/key; signed `ledger` and `catalog` cursor kinds are distinct and route-bound, and unsigned/caller-editable offsets are forbidden.
8. Evidence bytes are served only from refs already associated with the registered run and only through `evidence.ReadVerifiedLocal`-equivalent containment and digest verification.
9. Authentication secrets never enter catalog, ledger, evidence, action journal, request logs, or API responses.
10. Phase-6 fine-grained RBAC is deferred, but human-decision actions enforce exact `RequiredAuthority` grants from a protected startup-frozen grant file and preserve both authenticated proxy principal and delegated human attribution.
11. `GET /v1/runs` is catalog-only, uses signed lexical keyset pagination and may not imply current lifecycle state; lifecycle truth requires an authoritative run snapshot.
12. Every cancel operation is bound to one exact durable owner-lease generation from admission through request event and resulting `CANCELLED`; replacement owners cannot inherit it.
13. Service ledger reads are open-existing/read-only/no-create and explicitly closable; missing registered ledgers never become empty ledgers by observation.
14. UI/product aggregate semantics remain outside ABCP.
15. Existing EP-005 merge-only/same-repository-head restrictions remain unchanged.

## 7. External API surface v1

Minimum read endpoints:

```text
GET /v1/capabilities
GET /v1/runs
GET /v1/runs/{run_id}
GET /v1/runs/{run_id}/events
GET /v1/runs/{run_id}/timeline
GET /v1/runs/{run_id}/evidence
GET /v1/runs/{run_id}/evidence/{evidence_id}
GET /v1/runs/{run_id}/actions/{operation_id}
```

Event-stream semantics in EP-006 are bounded cursor polling. Long-poll, SSE and WebSocket are deferred.

Command endpoints:

```text
POST /v1/runs/{run_id}/actions/cancel
POST /v1/runs/{run_id}/actions/decision
```

Capability discovery MUST report at least `run_admission=false`, `retry=false`, `resume=false`, `recovery=false`, and the actual availability of cancel/decision/evidence download. Unsupported routes fail closed rather than emulate success.

## 8. Service root and catalog hard bounds

The service uses one explicitly configured canonical absolute `service_root`, controller-owned mode `0700`, with no symbolic-link traversal. The catalog contains immutable run/attempt registrations plus bounded active-owner and action-journal state. Internal ledger/evidence paths never appear in API responses.

Hard bounds:

- catalog runs: 10,000;
- simultaneously active registered runs/watchers: 256;
- attempts per run: 1,000;
- registration record: 64 KiB;
- action journal segment: 8 MiB / 32,768 records; maximum 8 linked segments / 262,144 records per run; service lock acquisition max 2 seconds;
- max request body: 1 MiB;
- max JSON nesting/field lengths: explicit validators, no unbounded strings/maps;
- events per page: default 100, hard max 500;
- runs per page: default 50, hard max 200; list rows are catalog-only and consume zero ledger snapshots;
- evidence descriptors per page: max 200;
- evidence download: hard max 16 MiB and never above the registered evidence-reader bound; max 4 concurrent downloads / 64 MiB buffered artifact bytes;
- cursor token: max 4 KiB;
- concurrent HTTP requests: hard max 64;
- concurrent authoritative ledger snapshots: hard max 4 globally and at most 1 per non-list read request;
- simultaneously open service-owned ledger objects across read-only snapshots and service-side action writers: hard max 8, each with exactly two persistent descriptors (ledger + parent), for <=16 persistent ledger descriptors; snapshot concurrency remains <=4;
- cancel watcher poll interval: maximum 250 ms while a registered run is live;
- bearer/cursor/grant config files and identifiers obey the exact byte/count ceilings in the service contract;
- HTTP server timeouts: read-header 5s, read 10s, write 30s, idle 60s;
- no goroutine per event/subscription and no unbounded subscriber state.

Implementation may tighten these bounds but may not silently widen them.

## 9. Completion gate

EP-006 A–D is complete only when:

- all four tracks have accepted task designs;
- implementation passes deterministic package and full-repository acceptance;
- API contract tests prove catalog containment, signed catalog keyset listing, authenticated reads, read-only and writable existing-file/no-create/closable ledger access, deterministic projections including explicit RUN_CREATED initial state, ledger/catalog cursor integrity and epoch handling, historical-session preservation, evidence containment/digest/hard-link/concurrency checks, durable segmented idempotent actions and status reads, owner-generation-bound cross-process cancel with atomic final state-transition revalidation/provenance on every runner cancellation exit and watcher-close races, exact attributed non-transition human-decision recording under transition exclusion, stale-state rejection, and deterministic ambiguous-effect reconciliation;
- independent exact-head Critical/Major review closes at 0C/0M;
- the exact reviewed candidate is published and merged under EP-005 lifecycle rules;
- post-merge acceptance/reconciliation passes on the actual merge result.

Dashboard/UI completion is not an EP-006 completion criterion.

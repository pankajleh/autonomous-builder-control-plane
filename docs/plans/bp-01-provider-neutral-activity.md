# BP-01 — Provider-Neutral Activity Capability

Date: 2026-09-26

Status: **IMPLEMENTED — LOCAL CANDIDATE; OPERATOR PUBLICATION/RELEASE GATES STILL APPLY**

Exact Repo B base:
`935114d21cdd79cf444bdea89c6052e27309a5bf`

Parent product/technical authority:
- Repo C PX-00 D7 DOC-FROZEN;
- D6 §6 BP-01 activity contract;
- D6 §16 compatibility;
- D6 §17 BP-01 path ownership;
- D6 §18 forbidden behavior;
- Repo B PR #32 prerequisite is integrated, so fresh provider metadata now carries full `Admission-ID == ABCP run_id`.

Risk class: **HIGH**

## Objective

Add an **additive provider-neutral activity capability** to ABCP so Repo C can later consume truthful live implementation progress without exposing or depending on the Ralphex dashboard.

ABCP remains the authoritative execution source. Ralphex remains hidden provider telemetry. Provider completion never becomes ABCP acceptance.

## Frozen HTTP contract

Add only:

- `GET /v1/extensions/pdlc-experience`

```json
{
  "schema_version": "PdlcExperienceCapabilitiesV1",
  "activity_stream": true,
  "preview_runtime": false
}
```

- `GET /v1/runs/{runId}/activity?page_size=&cursor=`
- `GET /v1/runs/{runId}/activity/stream`

Do **not** add fields to existing `/v1/capabilities` and do not change current run/events/timeline/evidence/action/admission semantics.

Paged response:

```json
{
  "schema_version": "ActivityPageV1",
  "run_id": "admission-...",
  "events": [],
  "next_cursor": "..."
}
```

SSE event IDs are durable activity ordinals. `Last-Event-ID` resumes strictly after that ordinal. Invalid, stale, run-mismatched or generation-conflicting resume is rejected before SSE headers are committed.

## ActivityEventV1

Implement the D6 §6.4 fields exactly:

- schema_version
- ordinal
- activity_id
- run_id
- occurred_at
- observed_at
- authority_level
- category
- phase
- status
- title
- detail
- task_number
- iteration_number
- source_kind
- source_session_id
- source_event_id
- source_digest
- optional checkpoint_sha
- checkpoint_clean

Provider detail is never a run-state transition.

## Controller-owned run binding

Provider metadata SHALL NOT establish repository/worktree authority.

For each requested registered run, `internal/activity` must derive its trusted run scope from controller-owned durable material:

1. read the exact runtime-catalog registration for the run;
2. locate exactly one protected admission binding under the configured ABCP service root whose `RunID` equals the requested run;
3. parse the existing exported `runadmission.AdmissionBindingV1`;
4. cross-check binding authority digest against the runtime-catalog registration;
5. cross-check repository identity digest, canonical ledger/evidence paths and exact manifest digest;
6. parse the admitted manifest and require exact run ID, repository identity/path, admitted base SHA, governed branch `abcp/<runId>`, worktree enabled, and Ralphex binary/hash/source pin;
7. resolve the governed Git worktree independently using `git worktree list --porcelain` and exact branch identity; do not trust a provider-reported worktree path.

If the binding is absent/ambiguous/corrupt, provider detail and checkpoint observation are unavailable/UNKNOWN for that run; ABCP ledger reads continue normally.

No customer response exposes absolute repository/worktree/binding/manifest paths.

## Hidden Ralphex sidecar

For an eligible admitted run, BP-01 starts or attaches one watch-only pinned Ralphex sidecar for the exact trusted repository scope:

```text
<admitted governed Ralphex binary>
  --serve
  --host 127.0.0.1
  --port <controller-allocated loopback port>
  --watch <trusted repository root>
```

Before trusting it, verify the exact binary path, SHA-256 and source SHA from the admitted manifest via the existing Ralphex capability verifier.

Provider-native machine contract is limited to:

- `GET /api/sessions`
- `GET /events?session=<provider-session-id>`
- standard `Last-Event-ID`

The adapter accepts exactly one session whose metadata satisfies:
- full `Admission-ID == ABCP run_id`;
- repository equals admitted repository identity;
- project, when present, is compatible with the admitted run;
- `dirPath` resolves exactly to `<trusted repository>/.ralphex/progress`;
- branch equals the admitted governed branch.

Zero match => provider detail UNKNOWN.
Multiple matches, path escape, repository/project/admission mismatch, changed progress generation => provider-detail integrity failure only; no run-state mutation.

The sidecar is loopback-only and never proxied/exposed to Repo C or browser clients.

## Durable activity store

Create `internal/activity/**`.

The store is:
- append-only;
- run-scoped;
- restart-recoverable;
- deterministic-deduplicating;
- separate from the authoritative run ledger.

Provider source identity:
`run ID + provider session ID + provider SSE event ID + normalized payload digest`.

Authoritative activity records are derived only from existing durable ABCP ledger facts and remain visibly `ABCP_STATE` / `ABCP_ACCEPTANCE`.

Recovery:
- replay provider detail through the sidecar;
- replay existing ABCP durable ledger facts;
- append only missing deterministic identities;
- if the provider replay window has a gap, append an explicit GAP/UNKNOWN provider-detail record and continue only from verifiable available detail;
- never reconstruct missing provider events by guess.

## Source-checkpoint observer

Never attach a sampled SHA to Ralphex `task_end`.

Checkpoint observation is separate and uses only the trusted controller-owned repository/worktree binding above.

Emit `CHECKPOINT / AVAILABLE` only when all are true:
- exact governed worktree/branch belongs to the run;
- `git status --porcelain` is empty;
- HEAD resolves to an immutable commit;
- HEAD is a strict descendant of the admitted base;
- HEAD is reachable from the governed branch;
- SHA has not already been emitted.

Identity: deterministic from `run ID + checkpoint SHA`.

Dirty, ambiguous, missing, changed-branch or unprovable state => no checkpoint.

## Provider normalization

Map provider signals without authority inflation:

- task_start → IMPLEMENTATION / STARTED
- task_end → IMPLEMENTATION / COMPLETED
- review iteration_start → REVIEW / STARTED
- REVIEW_DONE → REVIEW / COMPLETED
- Codex review phase → REVIEW / PROGRESS
- provider COMPLETED → LIFECYCLE / COMPLETED with title equivalent to “Implementation workflow completed”
- provider FAILED/error → ERROR / FAILED
- replay gap → WARNING / UNKNOWN

Provider `COMPLETED` must never emit ACCEPTED.

Selected durable ABCP facts map separately:
- HUMAN_DECISION_REQUIRED → HUMAN_REQUIRED / REQUIRED
- BRANCH_ACCEPTANCE_PENDING → ACCEPTANCE / PROGRESS
- BRANCH_ACCEPTED → ACCEPTANCE / ACCEPTED
- FAILED/CANCELLED → LIFECYCLE / FAILED where applicable.

## Redaction and bounds

Before persistence/customer projection:
- UTF-8 validate and escape;
- redact credential/token-looking values;
- redact absolute host paths;
- redact URI-like sensitive values;
- title max 256 bytes;
- detail max 4096 bytes;
- source IDs max 512 bytes.

Resource ceilings:
- default page size 100, max 500;
- max 100,000 activity events per run;
- max 64 MiB durable activity bytes per run;
- max 16 concurrent activity SSE streams service-wide;
- sidecar startup deadline 5s;
- provider HTTP request timeout 5s;
- provider reconnect backoff bounded 250ms..5s;
- activity corruption or resource exhaustion fails the activity extension closed without affecting the run ledger.

## SSE / service rules

- use existing service authentication and registered-run authority;
- activity streams use a dedicated semaphore and do not hold the generic 64-request semaphore for stream lifetime;
- authenticate, verify run registration and acquire stream capacity before committing SSE headers;
- saturation returns bounded retryable HTTP failure;
- disconnect/reconnect must not lose durable activity already stored.

## Allowed source paths

Implementation may change only:

- `internal/activity/**`
- `internal/serviceapi/server.go`
- `internal/serviceapi/server_test.go`
- `internal/serviceapi/dto.go`
- `internal/serviceapi/dto_test.go`
- `internal/serviceapi/config.go` and focused config tests only if genuinely required
- `cmd/abcp/main.go`
- `cmd/abcp/main_test.go`
- this plan file / its completed location

No other source path is authorized.

## Forbidden

Do not modify:
- `internal/runadmission/**`
- `internal/runtimecatalog/**`
- `internal/run/**`
- `internal/ledger/**`
- `internal/readmodel/**`
- `internal/timeline/**`
- acceptance/state-transition/scheduler/integration/GitHub/merge lifecycle packages;
- existing public API semantics;
- Repo C;
- Preview/BP-02;
- notification persistence;
- production/AWS/deployment;
- Ralphex source/binary.

Do not expose provider-native paths or dashboard UI.
Do not let activity events drive run transitions.
Do not widen scope if the trusted binding/checkpoint design cannot be implemented within the allowed paths; report ROADBLOCK instead.

## Required implementation tests

At minimum:
- deterministic provider event identity and replay dedupe;
- provider replay gap => explicit UNKNOWN, no guessed events;
- full run/repository/project/dirPath/branch correlation;
- zero/multiple/mismatched sidecar sessions fail provider detail closed;
- binding/catalog/manifest mismatch fails provider detail/checkpoint closed;
- provider COMPLETED is not ACCEPTED;
- authoritative BRANCH_ACCEPTED is ACCEPTED only from ABCP ledger facts;
- source checkpoint requires clean exact governed branch/worktree and descendant/reachability proof;
- task_end never inherits sampled SHA;
- redaction/bounds;
- activity store restart and duplicate replay;
- cursor run/freshness/tamper checks;
- Last-Event-ID resume;
- stream capacity isolation and pre-header rejection;
- existing `/v1/capabilities`, run, events, timeline, evidence, action and admission tests remain compatible.

## Required validation before PR

- `go test ./internal/activity ./internal/serviceapi ./cmd/abcp`
- `go test -race ./internal/activity ./internal/serviceapi ./cmd/abcp`
- `go vet ./internal/activity ./internal/serviceapi ./cmd/abcp`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- non-Linux compile-only validation for affected packages;
- `git diff --check`;
- exact base-to-head changed-path proof;
- fresh independent exact-head review at 0 Critical / 0 Major before PR.

### Task 1: Implement BP-01 provider-neutral activity

- [x] Verify exact base `935114d21cdd79cf444bdea89c6052e27309a5bf` and this plan.
- [x] Implement the trusted run-binding resolver and hidden loopback Ralphex sidecar adapter inside `internal/activity/**`.
- [x] Implement durable activity store, provider/ABCP normalization, replay/gap handling and source-checkpoint observer.
- [x] Add the additive extension/activity page/SSE HTTP contracts.
- [x] Wire BP-01 into `cmd/abcp` without changing existing endpoint semantics.
- [x] Add adversarial tests for all frozen trust, replay, resource, redaction and compatibility invariants.
- [x] Run focused validation and leave one clean candidate commit within the path ceiling.

## Completion boundary

Ralphex completion/review is implementation evidence only. Repo B publication still follows the operator runbook:
focused tests → full suite → independent exact-head review → PR/repository checks → merge → build from merged SHA → dry-start → confirm no active governed runs → live cutover → release verification.

No BP-02, PX-04, publication, merge or activation authority is granted by this plan.

## Independent pre-implementation plan review

A broad read-only review attempt reached its 100-second timeout without a verdict and is not acceptance evidence.

The bounded exact-scope re-review returned:

```text
CRITICAL: 0
MAJOR: 0
PLAN_READY: YES
```

Confirmed invariants:
- trust derives from exported admission binding, runtime catalog and admitted manifest rather than provider metadata;
- Git independently resolves the exact governed branch/worktree;
- sidecar trust requires admitted Ralphex pin verification and exact session correlation;
- durable activity reads canonical facts without changing authoritative run/ledger packages;
- additive routes, dedicated SSE capacity and non-Linux fail-closed behavior fit the allowed paths.

This review authorizes implementation only within this plan's path ceiling. Repo B publication and release remain governed by the operator runbook.


## Implementation validation — 2026-09-26

Task 1 is complete in the isolated Repo B worktree. The required base
`935114d21cdd79cf444bdea89c6052e27309a5bf` was verified; the initial worktree
contained only the subsequent frozen-plan commit.

Implemented controller-bound scope resolution, pinned shared loopback sidecars,
provider generation/replay checks, redacted deterministic activity records,
ledger milestones, independent clean-source checkpoints, and additive authenticated
page/SSE routes. Durable global ordinals disambiguate cross-run resume; committed
log anchors detect deletion, replacement, and complete-record truncation after
restart. Activity failures remain isolated from the authoritative run ledger.
Linux filesystem generation evidence is required for activity storage and provider
observation; unavailable evidence fails the extension or provider detail closed.

Validation passed:
- `go test ./internal/activity ./internal/serviceapi ./cmd/abcp`
- `go test -race ./internal/activity ./internal/serviceapi ./cmd/abcp`
- `go vet ./internal/activity ./internal/serviceapi ./cmd/abcp`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `GOOS=darwin GOARCH=amd64 go test -c` for each affected package
- `git diff --check` and the exact-base changed-path allowlist check

Publication/release remain subject to the completion-boundary operator runbook,
including a fresh independent review of the exact candidate commit before a PR.
The pre-implementation plan review above is not an implementation review verdict.

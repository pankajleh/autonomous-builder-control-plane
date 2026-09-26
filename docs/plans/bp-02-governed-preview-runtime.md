# BP-02 — Governed Preview Runtime

Date: 2026-09-26
Scope track: `PRODUCT_FOUNDATION:PDLC_EXPERIENCE`
Status: **DOC-FROZEN — AUTHORITY READY FOR THE CURRENT PRE-ACTIVATION REPO B UPGRADE PROCEDURE**
Risk class: **HIGH**

Repo B integrated parent base:
`f21174dbaf47f4e71185f58dff5a338d0befa861`

External frozen product/architecture authority:
- Repo C PX-00 D7 DOC-FROZEN;
- current Repo C integrated state `b0878087cd95d325ec27d3ab1e2b02a018c91bc1`;
- `REQ-PX-PREV-001..012`;
- primary `TC/EV-PX-PREV-001..012` plus CP-PREV cross-cutting C01-C19;
- D6 section 9 and sections 13-18;
- BP-01 provider-neutral activity integrated in Repo B PR #33 and live.

Governance activation check at preparation time:
`GovernanceActivationV1 = absent`.
This Repo B upgrade therefore follows the current operator runbook §24 isolated-worktree/direct-governed-Ralphex procedure. Do not invent a self-admission path. If V3 activation appears before launch, stop and re-derive the required V3 authority.

## Objective

Implement an additive **Governed Preview Runtime** in ABCP.

### Task 1: Implement the frozen BP-02 governed preview runtime

- [x] Implement every requirement, invariant, endpoint, isolation rule and deterministic verification obligation in this frozen BP-02 plan without widening its allowed paths or authority.

Preview is a development presentation of exact governed candidate bytes. It is never deployment, acceptance, merge authority or a new run state.

BP-02 owns:
- preview eligibility from a BP-01 clean source checkpoint;
- immutable preview identity/history;
- controller-owned PreviewProfileV1 configuration;
- exact detached source materialization;
- bounded Docker runtime/isolation;
- health/TTL/stop/recovery;
- additive preview HTTP API;
- request-id/digest idempotency;
- `preview_runtime` capability truth.

Repo C Preview UI/gateway/access grants/feedback remain PX-06 and are forbidden here.

## Trusted source and product binding

A preview request identifies only:
- run ID;
- BP-01 checkpoint activity ID;
- server-configured preview profile ID;
- request ID;
- delegated actor.

The caller may not supply arbitrary SHA, argv, image, route, filesystem path or shell text.

ABCP must independently prove:
- exact registered run;
- checkpoint activity belongs to that run;
- checkpoint has non-empty SHA and `checkpoint_clean=true`;
- SHA is descended from admitted base and remains reachable from the governed branch;
- activity/binding integrity is not failed/ambiguous;
- admission is a **product admission**, not a development admission.

Product task/version/ProductAuthorization identities are read only from the existing controller-created, digest-bound product admission context capsule:
- `project = ProductAuthorizationID`;
- `plan = ProductTaskID`;
- `execution_pack = ProductVersionID`;
- `roadmap_phase = product-run-admission`.

The preview resolver must cross-check the existing immutable run registration, AdmissionBindingV1, manifest digest/path, context-capsule digest/path and repository identity exactly. It may reuse/import existing types and read seams but MUST NOT mutate `runadmission`, `runtimecatalog`, ledger or run state.

## PreviewProfileV1

Profiles are operator-controlled protected configuration, never request-body authority.

Each profile freezes:
- profile ID;
- repository identity digest;
- 1..4 service definitions;
- digest-pinned image reference per service; mutable tags alone are invalid;
- fixed prepare/start argv arrays; shell command strings are forbidden;
- bounded non-secret environment allowlist;
- source mount flag;
- internal service name/port;
- exactly one presented service;
- health path and timeout;
- TTL;
- CPU quota;
- memory bytes;
- pids limit;
- bounded tmpfs/write budget;
- `network_policy = INTERNAL_ONLY`.

Unknown profile, repository mismatch, image without digest, arbitrary shell, unbounded resource, privileged/root-only profile where non-root is required, Docker socket, host path injection or secret-bearing environment fails closed.

No registry/internet access is granted for package installation. Profiles are eligible only when local/digest-pinned images plus controller-approved local/offline inputs suffice.

## Exact source materialization

For every preview revision:
- create a fresh detached checkout of exact checkpoint SHA under a private controller-owned preview root;
- never serve the mutable run worktree;
- verify exact SHA after checkout;
- mount source read-only at a fixed container path;
- all writes go only to bounded scratch/tmpfs areas;
- terminal cleanup removes containers/network/detached checkout without deleting historical preview records.

## Preview identity and durable state

Persist append-only/history-preserving records containing:
- preview ID;
- run-local monotonically increasing preview revision;
- ProductAuthorization ID;
- ProductTask ID;
- ProductVersion ID;
- run ID;
- checkpoint activity ID;
- source SHA;
- profile ID + canonical profile digest;
- status;
- health;
- created/expires/stopped timestamps;
- validation/evidence identities;
- request receipt/body digest.

A different eligible SHA creates N+1.
A deliberate retry after FAILED/EXPIRED/STOPPED uses a new request ID and new preview ID/revision even if SHA is unchanged.
Terminal identities are never rebound.

State:
`REQUESTED → VALIDATING → STARTING → READY`
with exits/overlay:
`FAILED | EXPIRED | STOPPED`, health `DEGRADED`.

Preview state never mutates ABCP run state or acceptance state.

## Docker isolation

Foundation uses host Docker through structured controller argv only.

Every preview group:
- digest-pinned images only;
- non-root user where supported; fail closed otherwise;
- `--cap-drop=ALL`;
- `no-new-privileges`;
- read-only root filesystem;
- bounded tmpfs/write area;
- source mounted read-only;
- CPU/memory/pids bounds;
- dedicated Docker `--internal` network;
- no Docker socket;
- no production secrets;
- no ordinary external egress;
- only the presented service may be reachable through a controller-owned host-loopback route;
- sibling services stay internal;
- TTL/restart cleanup removes orphaned runtime objects.

### Host capability rule

Prefreeze host probes confirmed Docker is present and external egress is blocked on an internal network, but direct Docker port publication from that internal network was not reachable and Docker did not expose a usable container IP during the initial manual probes.

Implementation is authorized to provide a **controller-owned loopback-only proxy/presentation adapter** only if it can prove:
1. containers remain exclusively on the dedicated internal network;
2. ordinary container external egress remains blocked;
3. the controller can reach only the configured presented internal service endpoint;
4. the proxy binds an explicit loopback IP only;
5. no container ID/host path/credential reaches service DTOs.

If acceptance cannot prove a host-loopback presentation path while retaining INTERNAL_ONLY isolation, `preview_runtime` MUST remain false and Preview Serve is `NOT_AVAILABLE`; do not weaken network policy.

## HTTP contract

Add only:
- `POST /v1/runs/{runId}/previews`
- `GET /v1/runs/{runId}/previews`
- `GET /v1/runs/{runId}/previews/{previewId}`
- `POST /v1/runs/{runId}/previews/{previewId}/stop`

State-changing requests use bounded command envelope data:
- request ID;
- delegated actor;
- expected run/preview identity;
- profile/checkpoint identifiers.

Persist canonical request-body digest:
- exact same request ID + same body → same receipt/result;
- same request ID + different body/target → conflict;
- ambiguous transport → caller reads receipt/status, no blind replay.

Status may return a server-only internal route handle while READY/DEGRADED. It must never contain container IDs, Docker socket paths, host filesystem paths, credentials or production secrets.

After a fully supported BP-02 runtime is wired, the existing `PdlcExperienceCapabilitiesV1` schema remains unchanged and only `preview_runtime` changes from false to true.
If host/profile isolation is unsupported/unavailable, it truthfully remains false.

## Allowed implementation paths

Only:
- `internal/preview/**` (new package);
- `internal/serviceapi/server.go`;
- `internal/serviceapi/server_test.go`;
- `internal/serviceapi/dto.go`;
- `internal/serviceapi/dto_test.go`;
- `internal/serviceapi/config.go` and focused config tests if required for protected PreviewProfileV1 loading;
- `cmd/abcp/main.go`;
- `cmd/abcp/main_test.go`;
- this plan file only for completion/move to `docs/plans/completed/`.

Imports/read-only calls into existing `activity`, `runadmission`, `runtimecatalog`, `context`, `authority`, `strictjson` and Git helpers are allowed. Those packages are **read-only dependencies** for this slice.

If implementation requires modifying any of those existing packages, or run/ledger/readmodel/timeline/action/scheduler/acceptance/integration/GitHub/merge/governance packages, report ROADBLOCK.

## Explicit non-goals

- Repo C Product API/UI;
- preview gateway, public domain, browser access grant, feedback;
- changing run states/transitions/acceptance;
- arbitrary user shell or image;
- external package-registry/internet egress;
- Docker socket exposure;
- production credentials/data;
- P3/P4 public-sharing/account semantics;
- BP-01 activity semantics changes;
- V3 governance activation;
- deployment/AWS;
- automatic publication/merge.

## Required deterministic verification

At minimum:
- focused `internal/preview`, serviceapi and cmd tests;
- `go test ./...`;
- `go test -race ./...`;
- `go vet ./...`;
- non-Linux compile-only checks for affected packages with fail-closed runtime behavior;
- `git diff --check`;
- exact base-to-head path subset proof;
- existing `/v1/capabilities` byte/semantic compatibility;
- existing activity APIs remain compatible;
- checkpoint run/activity/clean/reachability mismatch negatives;
- product-context-capsule binding negatives and development-run rejection;
- profile digest/tag/shell/resource/repo mismatch negatives;
- request replay/conflict/terminal-retry tests;
- READY/DEGRADED/FAILED/EXPIRED/STOPPED state tests proving zero run-state mutation;
- source checkout read-only/exact SHA proof;
- Docker command injection/path/secret tests;
- TTL/restart/orphan cleanup;
- host isolation proof: external egress blocked + host-loopback route reachable, or fail-closed `preview_runtime=false`;
- no raw container/host/credential fields in service DTOs.

## Ralphex execution contract

Run from an isolated Repo B worktree rooted at the exact parent base above.
Use the canonical governed Ralphex binary:
- source `66e8868173ffc982dbeab903663839d2276372c4`;
- task/review model `gpt-6-astra:xhigh`;
- max task iterations 8;
- max internal review passes 2;
- session timeout 2h;
- idle timeout 15m.

Ralphex completion is implementation evidence only.
After Ralphex exits, the operator independently reruns deterministic acceptance and an exact-head 0C/0M publication review.

## Live-runtime impact after publication

BP-02 changes the ABCP engine/configuration surface.
After any human-authorized merge it is `CODE_INTEGRATED_RUNTIME_STALE` until §24/§40:
- merged-SHA build;
- private-root/alternate-port dry-start with protected preview profile configuration;
- no active governed run;
- live ABCP cutover;
- capability/API/log checks;
- host preview isolation smoke;
- previous binary/config retained;
- first post-cutover preview-capable run behavior verified where applicable.

## Completion boundary

No BP-02 implementation grants PX-06, deployment, integration or merge authority.
If host isolation cannot satisfy the frozen network+loopback contract, implementation may still preserve fail-closed `NOT_AVAILABLE`, but it may not claim live Preview Serve capability.

## Independent pre-implementation authority review

Accepted bounded review:

CRITICAL: 0
MAJOR: 0
AUTHORITY_READY: YES

Accepted invariants:
1. Product identities require digest-bound product-admission capsule verification; development admissions are rejected.
2. Checkpoints require exact run binding, clean SHA, admitted-base ancestry and governed-branch reachability.
3. Exact detached source stays read-only; protected profiles enforce bounded isolated execution.
4. `preview_runtime` remains false unless acceptance proves INTERNAL_ONLY isolation, blocked external egress and safe host-loopback presentation.
5. Preview history/replay identities remain durable; run and acceptance state remain unchanged.
6. Implementation stays within allowed paths; V3 activation requires re-derived authority; publication still requires independent acceptance and review.

The earlier broad review attempt timed out without a verdict and is not acceptance evidence.


## Implementation completion — 2026-09-26

Task 1 is implemented within the frozen allowed paths. Protected profiles load
through `abcp serve --preview-profile-file`; absent or unsupported host/profile
isolation preserves `preview_runtime=false` and `NOT_AVAILABLE`. Preview history
and receipts are independent of run/acceptance state.

Validation passed:
- focused `internal/preview`, `internal/serviceapi`, and `cmd/abcp` tests;
- `go test ./...`, `go test -race ./...`, and `go vet ./...`;
- Darwin/amd64 and Windows/amd64 compile-only checks for all three affected packages;
- strict legacy capability compatibility, activity regression tests, binding and
  profile negatives, exact detached source, request replay/conflicts, lifecycle,
  concurrency, restart/orphan cleanup, isolation command/proxy and DTO checks;
- `git diff --check` and the exact frozen-parent allowed-path subset proof.

The opt-in local Docker smoke used the already-present digest-pinned Alpine image
`alpine@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6`.
The complete host/profile isolation and presentation proof was unavailable, so
capability remained false; probe cleanup passed. This is the authorized fail-closed
outcome, not live Preview Serve acceptance. Publication, runtime cutover, and
independent exact-head acceptance/review remain outside this implementation iteration.

### Review corrections — 2026-09-26

The five review scopes completed; their confirmed findings were addressed within
the frozen allowed paths:
- health-check cancellation no longer revokes a profile's isolation approval;
- exclusive namespace ownership is acquired before orphan cleanup, separately
  from journal loading, so damaged history disables preview while preserving
  history and allowing the existing service to start;
- controller subprocesses use process-group cancellation and bounded pipe waits;
- candidate-visible Git metadata no longer retains generated origin paths or
  controller reflogs;
- ordinary provider warnings remain eligible while controller integrity markers
  still fail closed, including across activity pages;
- host/profile probes exercise the actual read-only source mount and require
  successful cleanup before recording approval;
- deterministic tests cover successful Docker execution/health, partial startup,
  isolation drift, cancellation/expiry, cleanup failures and damaged recovery;
- unused runtime bookkeeping and redundant environment conversion were removed;
- protected configuration, host prerequisites, smoke testing and HTTP receipt
  usage are documented in `internal/preview/README.md`.

The updated local Docker smoke includes the source mount. It again completed
with `preview_runtime=false` and successful cleanup because the full host/profile
isolation and presentation proof was unavailable. This remains the authorized
fail-closed outcome and does not claim live Preview Serve acceptance.

Review validation passed: `go test ./...`, `go test -race ./...`, `go vet ./...`,
Darwin/amd64 and Windows/amd64 compile-only checks for the three affected packages,
Go formatting, `git diff --check`, and the frozen-parent allowed-path subset audit.

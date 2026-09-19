# ABCP-RC-P01 — Repo C Product Run Admission

Status: **BOUNDED DELIVERY AUTHORITY**
Date: 2026-09-19

## 1. Identity

Pack ID: `ABCP-RC-P01`

Objective: finish and harden the existing Repo B product-facing run-admission candidate so one exact Repo C product authorization snapshot can become one durable ABCP run reference without exposing controller/provider internals.

Repository: `pankajleh/autonomous-builder-control-plane`

Integration target: `main`

Governing architecture: `docs/architecture/ABCP-EP006-RC-INTEGRATION-ARCHITECTURE.md`

Existing implementation candidate: `551eeade43261ac3c24ae393419b880bc0e5827a`

Repo C authority snapshot: `b5de18b79765be089317d86786bf64956452ae10`

Exact implementation base for the bounded Ralphex run is supplied by `runtime-plan.md` after this authority pack is committed. It MUST be a clean exact 40-character SHA containing this pack and architecture authority.

## 2. Single bounded delivery outcome

P01 is complete only when Repo B exposes the frozen product-facing admission contract defined by the governing architecture, with durable idempotent admission, safe ambiguous-outcome reconciliation, delegated actor attribution, exact repository-base binding, strict request decoding, private controller-owned materialization and compatibility with retained EP-006 surfaces.

P01 does not authorize any other Repo B/Repo C capability.
## 3. In-scope production areas

Only changes required to satisfy P01 are allowed in these code areas:
- `cmd/abcp/**` for bounded serve/run wiring required by admission;
- `internal/serviceapi/**` for the public v1 admission DTO/route/capability/error mapping;
- `internal/runadmission/**` for admission receipt, reconciliation, profile loading, safe materialization and launch ownership;
- `internal/workflowauthoritypg/**` only where required by the existing admission execution path;
- `internal/governance/**` only for a concrete admission-path correctness/security defect required by the frozen contract;
- `internal/strictjson/**` if strict case-sensitive duplicate-safe JSON decoding is required to satisfy the public/private admission contract;
- `go.mod` / `go.sum` only if already required by the admitted implementation path;
- this pack's architecture/pack/review/evidence documentation.

No other package becomes in scope merely because review notices it.

## 4. Product-facing acceptance contract

With admission configured and authorized:
1. `GET /v1/capabilities` reports `run_admission=true`.
2. `POST /v1/runs` accepts only the schema-v1 fields frozen by the architecture document.
3. A valid request returns `202 Accepted` with exactly one stable `run_id` and `run_url=/v1/runs/{run_id}`.
4. Exact semantic replay by the same authenticated principal and `request_id` returns the same logical run identity and MUST NOT duplicate execution.
5. Conflicting reuse of the same request identity fails closed.
6. Repository HEAD/base mismatch fails closed; admission does not fetch/reset/merge to satisfy the request.
7. Unknown, duplicate, case-alias, malformed, non-UTF-8 or oversized public/private JSON fails closed.
8. The public request cannot inject paths, manifests, argv, PIDs, evidence locations, workflow-store details or provider-private configuration.
9. Admission persists enough controller-owned binding to reconcile receipt/run registration and reject conflicting or tampered durable state.
10. Delegated actor attribution is preserved while authorization remains the authenticated trusted service principal plus its configured grant to assert delegated actors.
11. Success is never fabricated from an unresolved ambiguous durable/launch outcome; exact replay/reconciliation preserves one durable identity.
12. Existing EP-006 run detail/events/timeline/evidence/cancel/decision/action-status behavior remains compatible.
13. Without an admission profile/controller, `run_admission=false` and `POST /v1/runs` remains unsupported.
14. Admission does not turn on retry, resume or recovery.

## 5. Expected-red / defect-first coverage

Because an implementation candidate already exists, P01 does not require deleting working code to manufacture a red test. Before production correction, the agent MUST establish at least one failing regression for every concrete in-scope gap it intends to fix when practical.

The known candidate-hardening cases that are valid expected-red targets if not already passing at the exact base are:
- case-insensitive JSON field aliases accepted by Go's default decoder;
- invalid UTF-8 accepted in controller/public configuration records;
- first initialization of durable workflow authority poisoned by a caller/manifest repository identity not derived from the trusted repository;
- replay/catalog reconciliation accepting a same `run_id` whose registered repository/authority/ledger/evidence binding does not match the original admission;
- unsafe or foreign-owned/mode-widened private ledger/evidence roots;
- ambiguous/existing receipt/launch state causing duplicate launch or false success.

If any listed case already passes at the exact base, it is not a mandate to change code. Record it as already satisfied.
## 6. Required failure behavior

At minimum the bounded implementation/tests must cover:
- admission unavailable/not configured;
- unauthenticated request;
- malformed/oversized/unknown-field/duplicate-field/case-alias/non-UTF-8 request;
- invalid product IDs/digests/delegated actor;
- repository base mismatch;
- request-ID semantic conflict;
- unsafe profile/private configuration;
- unsafe materialization/binding corruption;
- durable read/write/reconciliation failure;
- exact replay after receipt exists;
- run already registered with exact binding;
- run registered with conflicting binding;
- launch failure after durable receipt without duplicate logical run creation.

Errors must remain typed through the existing EP-006 error envelope. No failure may be converted into an invented success or an implicit new request identity.

## 7. Explicit exclusions

DO NOT implement or redesign:
- Repo C ABCP client/adapter or Repo C persistence;
- Repo C UI;
- human-decision continuation/resume;
- general retry/recovery;
- provider switching/selection;
- Repo A, MCP Direct or NUC Bridge;
- Ralphex configuration or orchestration behavior;
- integration/publication/merge authority;
- post-EP-006 Assurance Capsule/A-B-C/V4 validator work;
- broad scheduler/governance refactors;
- live infrastructure or AWS changes.

A real required fix outside this pack stops as an authority gap. It is not silently added to P01.
## 8. Required focused gates

Run from the exact candidate worktree after implementation:
- `go test -count=1 ./internal/runadmission`
- `go test -count=1 ./internal/serviceapi`
- `go test -count=1 ./internal/workflowauthoritypg`
- `go test -count=1 ./internal/governance`
- `go test -count=1 ./cmd/abcp`
- `go test -count=1 -race ./internal/runadmission ./internal/serviceapi ./internal/workflowauthoritypg`

If `ABCP_WORKFLOW_AUTHORITY_PG_TEST_DSN` is available in the authorized environment, the PostgreSQL integration test MUST run against that DSN. If it is unavailable, the test may skip exactly as coded and evidence must state that fact; the autonomous pack must not create live database infrastructure.

## 9. Required broad gates

Before executable SHA freeze:
- `go test -count=1 ./...`
- `go test -count=1 -race ./...`
- `go vet ./...`
- `GOOS=darwin GOARCH=amd64 go build ./cmd/abcp`
- `GOOS=windows GOARCH=amd64 go build ./cmd/abcp`
- `git diff --check`

Any unrelated/pre-existing flaky failure must be reproduced and classified; P01 does not authorize unrelated changes merely to make a broad gate green.

## 10. Evidence and delivery

Production/test changes are committed first as the tested executable SHA. Evidence is then recorded against that exact SHA in an evidence-only successor.

The evidence MUST record:
- exact base SHA;
- exact Repo C authority commit and hashes;
- executable SHA;
- changed-file list;
- focused and broad gate commands/results/counts where available;
- admission contract/schema hashes where relevant;
- Review-1 accepted findings and correction SHA if any;
- final review result;
- explicit statement that no merge occurred.

Delivery is a pushed **Draft PR** targeting `main`. Autonomous merge is forbidden.
## 11. Review authority

The binding review rules are in `review-contract.md`.

In summary:
- Review 1 is one comprehensive base-to-head review against this pack and the frozen architecture;
- only concrete in-scope authority/correctness/security/compatibility/replay/durability defects or out-of-scope mutations are actionable;
- later-pack redesign, alternative architecture, cleanup/style and unrelated defects are `OUT_OF_SCOPE_REVIEW`;
- if Review 1 causes accepted corrections, one final Review 2 verifies those corrections and remaining critical/major violations;
- there is no third review.

## 12. Exact completion claim

The only authorized completion claim is:

`ABCP_RC_P01_RUN_ADMISSION_COMPLETE`

It may be emitted only when all acceptance criteria and required gates are satisfied or explicitly classified under this pack, the executable SHA and evidence successor are frozen, the worktree is clean, the branch is pushed, a Draft PR targets `main`, and the authorized review sequence has converged with no unresolved in-scope finding.

Merge is not part of this completion claim.

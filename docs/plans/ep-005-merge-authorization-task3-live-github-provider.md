# EP-005 — Merge Authorization Task 3: Live GitHub Merge-Only Provider

## Authority

This bounded implementation task is an exact subset of `docs/plans/ep-005-merge-authorization-and-post-merge.md` Task 3. Task 2 is independently accepted and exact-head reviewed `CLOSURE_CLEAN`, Critical 0 / Major 0 at `c6716a800f1b317eb1cae43c9e50fa5e81bf67c2` with closure seal `7e15a03fcc106044e2e8dff12281919427fcb7845800f116cd864b0a53e516ae`.

The accepted master design remains authoritative. Task 3 implements only the production merge-only GitHub provider that satisfies the existing `internal/mergelifecycle.Provider` interface and the already-frozen `internal/githublifecycle` contracts. It must not redesign Task 1/2 contracts or widen Phase 4.

## Fresh-operation startup

Before implementation begins, read the bound context capsule, verify its exact SHA256/base SHA, and run `abcp context-verify`. Missing binding, source/base drift, failed verification, or loss of the Task-2 closure evidence stops execution.

## Owned paths

Implementation may change only:

- `internal/githubmergeprovider/**` — new production provider, frozen capability record, local deterministic tests, and opt-in live conformance test.
- `docs/architecture/GITHUB_LIFECYCLE_FOUNDATION_CONTRACT.md` — only provider/capability semantics already accepted by the master design; do not claim Task-3 acceptance/merge.
- this plan file, only to mark completion and move it to `docs/plans/completed/`.

Frozen in Task 3:

- `internal/githublifecycle/**`
- `internal/mergelifecycle/**`
- `internal/ledger/**`
- `internal/run/**`
- `internal/integrationgate/**`
- `cmd/**`
- `go.mod` / `go.sum`
- `docs/CURRENT_STATE.md`, `docs/PROGRESS.md`, `docs/AUDIT_INDEX.md`

If the provider cannot implement the frozen interfaces/contracts without changing a frozen path, stop with a design/scope blocker instead of editing it.

## Provider implementation invariants

- Production supports `merge` only. Squash/rebase and fork-head PRs fail closed before mutation.
- Production origin is exactly `https://api.github.com`; GraphQL endpoint exactly `/graphql`; REST paths are closed templates with escaped components.
- The provider owns the HTTP client, origin, API version, headers, redirect policy, connection policy, limits, and request construction. Callers cannot select URLs, transports, headers, queries, methods, policy, merge method, refs, or credentials.
- Authentication is injected by a sealed authenticator only after request identity is fixed; credentials never enter logs/evidence/errors.
- `github-update-refs-atomic-base-head-v1` is frozen in a reviewed embedded canonical capability record and validated both at provider construction/startup and before target admission/execution.
- Target mutation is exactly one GraphQL `updateRefs` request containing exactly two distinct same-repository refs in base-then-head order: base CAS to expected result OID plus head no-op CAS, exact `beforeOid`/`afterOid`, `force=false` for both.
- The provider never calls GitHub's ordinary PR merge endpoint and never uses REST ref-update mutation as a fallback.
- Commit preparation creates only the exact deterministic commit object and then independently observes/reconstructs exact object bytes/fields before reporting success.
- Authorization observation independently obtains the exact PR/repository/principal facts plus complete check-run, commit-status, and review pagination closures required by the frozen contracts.
- Every eligible exact-head dismissed review blocks as `REVIEW_HISTORY_UNPROVEN`; production v1 supplies no authenticated review-history bypass.
- Submission classification is byte-boundary conservative: only local pre-transport failure or proved zero plaintext HTTP request bytes may be `submitted=false`; otherwise ambiguity is `UNKNOWN` and only read-only reconciliation follows.
- Mutation transport has no automatic retry, keep-alive, HTTP/2, proxy, redirect, caller-selected origin, or hidden second target write.
- Response header, Link, request-ID, request-body, compressed/decompressed body, cumulative byte/call/time budgets are enforced from controller-provided remaining budgets without exceeding production maxima.
- Every non-nil response body is closed on every success/error/limit/decode/cancel path; one bounded JSON value plus EOF is required.
- Reconciliation is merge-only and read-only. `APPLIED` materializes the exact validated result, `NOT_APPLIED` requires the frozen typed proof, and all unproved cases remain `UNKNOWN`.
- Post-merge observation proves the exact result object plus target equal-or-descendant containment under `github-compare-v1`; it never infers success from PR `merged=true` alone.
- Provider methods return accurate `ProviderAccountingV1`; the provider may not reset or reinterpret controller-owned cumulative counters.
- Production implementation contains no deployment/service/API/dashboard/Phase-5 behavior.

## Live-effect boundary

The implementation/Ralphex phase is **network mutation free**:

- no live GitHub write calls;
- no use of host GitHub credentials by tests;
- no remote branch/ref creation/deletion;
- no live commit-object creation;
- no live GraphQL mutation.

An opt-in controlled-repository conformance test may be implemented, but it must skip unless an explicit acceptance-only environment/fixture is supplied. Running that live test is a later acceptance operation with separate bounded authority and exact disposable refs; implementation completion alone does not authorize it.

## Explicit non-goals

- No ordinary PR merge endpoint.
- No REST ref-update mutation.
- No base-only CAS, sequential base/head writes, force update, or check-then-write fallback.
- No runtime GraphQL introspection as authority and no silent capability widening.
- No support for squash/rebase or fork-head execution.
- No Task-1/Task-2 contract amendments.
- No PR publication, merge of this development branch, deployment, service activation, Phase 5+, scheduler/dashboard/API work.
- No status-doc claim that Task 3 or EP-005 is accepted/complete before independent acceptance/review/merge evidence.

## Frozen Acceptance Matrix

Every row below is mandatory. A passing test that does not exercise the named predicate is not evidence for that row.

| ID | Required proof | Deterministic evidence |
| --- | --- | --- |
| T3-01 | sealed origin/auth/request identity; redirect/proxy/credential non-forwarding | local transport tests with malicious origins/paths/headers/authenticator mutation |
| T3-02 | exact header/body/request-ID/compression/body-close limits | exact-limit and limit+1 local HTTP tests; body-close assertions on every response class |
| T3-03 | canonical frozen capability record matches compiled provider | startup + pre-target validation tests; digest mismatch/unsupported no-op/all-or-nothing fail closed |
| T3-04 | exact PR/repository/principal eligibility and same-repository head | open/non-draft/unmerged positive; closed/draft/converted/merged/fork/identity mismatch negatives |
| T3-05 | complete independent reviews/check-runs/status pagination | page/cursor chain, duplicate, truncation, malformed Link/pageInfo, exact-limit/limit+1 tests |
| T3-06 | dismissed current-head eligible review blocks | explicit `REVIEW_HISTORY_UNPROVEN` regression; no production bypass |
| T3-07 | exact deterministic commit preparation/observation | exact recipe/OID/bytes/tree/parents/message/author/committer positive and mismatch/ambiguous negatives |
| T3-08 | exactly one two-entry GraphQL `updateRefs` mutation | wire test proves base-then-head, exact before/after, same-repo head no-op, both `force=false` |
| T3-09 | forbidden mutation endpoints are unreachable | tests assert zero ordinary PR-merge and REST ref-update calls/strings in production request routing |
| T3-10 | byte-boundary submission classification | pre-transport zero-byte positive plus possible-byte timeout/error/cancel/body-close => UNKNOWN; no retry |
| T3-11 | typed NOT_APPLIED vs UNKNOWN mapping | stable machine code proof only; generic errors/message text/open PR/old target remain UNKNOWN |
| T3-12 | exact read-only target reconciliation | APPLIED reconstructs identical MergeResult; NOT_APPLIED typed proof; mutation count remains zero |
| T3-13 | post-merge exact result + equal/descendant containment | identical/ahead<=500 positives; sibling/behind/diverged/wrong merge-base/truncated/excessive negatives |
| T3-14 | cumulative provider budgets compose across calls | individually valid calls exceed cumulative request/header/body/time/call budgets and fail before next operation |
| T3-15 | unsupported modes/deployments fail before mutation | squash/rebase/fork/missing capability/no-op unsupported/contract digest mismatch => zero mutation |
| T3-16 | concurrency and cancellation do not duplicate writes | deterministic race/fault tests prove <=1 commit creation and <=1 target submission per exact attempt/provider instance |
| T3-17 | interface compatibility without Task-1/2 changes | provider satisfies `mergelifecycle.Provider`; frozen package diffs empty; Task-1/2 regressions pass |
| T3-18 | opt-in controlled live conformance harness is bounded | test code requires explicit fixture and verifies accepted two-ref update plus wrong-base/wrong-head all-or-nothing boundaries; skipped by default |

## Required named regression entrypoints

The implementation must provide these exact test entrypoints under `internal/githubmergeprovider`:

- `TestTask3SealedTransportAndCredentialBoundary`
- `TestTask3ResponseAndBodyResourceLimits`
- `TestTask3FrozenCapabilityValidation`
- `TestTask3PullRequestEligibilityAndForkRejection`
- `TestTask3PaginationClosureAndDismissedReview`
- `TestTask3ExactCommitPreparationObservation`
- `TestTask3AtomicUpdateRefsWireAndForbiddenEndpoints`
- `TestTask3SubmissionByteBoundaryNoRetry`
- `TestTask3ReconciliationDispositions`
- `TestTask3PostMergeContainment`
- `TestTask3CumulativeProviderBudgets`
- `TestTask3UnsupportedModesAndCapabilities`
- `TestTask3ConcurrentMutationCeilings`
- `TestTask3ControlledLiveConformance`

### Task 1: Implement Task 3 live GitHub merge-only provider

- [ ] Create `internal/githubmergeprovider` implementing the frozen `mergelifecycle.Provider` interface with no Task-1/2 contract changes.
- [ ] Freeze and embed the canonical `github-update-refs-atomic-base-head-v1` capability record, including official documentation identity, retained contract/schema digest, exact field/type semantics, API/deployment identity, provider implementation version, and conformance-fixture digest; independently validate it at startup and pre-target execution.
- [ ] Implement sealed bounded GitHub HTTPS transport with fixed origin/endpoints/API version, strict auth injection, no redirects/proxy/retries/HTTP2/keep-alives, exact limits/accounting, body closure, request-ID validation, and conservative request-byte submission instrumentation.
- [ ] Implement stable principal/repository/PR eligibility observation plus exact complete check-run/status/review pagination closures and dismissed-review blocking.
- [ ] Implement exact deterministic commit-object creation plus independent exact object observation/reconciliation.
- [ ] Implement exactly one GraphQL two-ref atomic target request and strict response/disposition mapping; forbid PR merge and REST ref mutation.
- [ ] Implement read-only merge reconciliation and post-merge exact-result/target-containment observation.
- [ ] Add all frozen matrix tests, exact named entrypoints, deterministic concurrency/fault tests, and opt-in controlled live-conformance harness; default test suite performs zero live provider mutations.
- [ ] Update only the authorized Phase-4 provider section of `GITHUB_LIFECYCLE_FOUNDATION_CONTRACT.md`, without claiming independent acceptance/merge/EP-005 completion.
- [ ] Run all implementation-owned deterministic gates; move this plan to `docs/plans/completed/` and create exactly one implementation commit only after they pass.

## Required deterministic implementation gates

At minimum:

- all 14 named Task-3 test entrypoints individually;
- `go test -count=1 ./internal/githubmergeprovider`;
- repeated deterministic mutation/concurrency/fault subset with `-count=25`;
- `go test -count=1 -race ./internal/githubmergeprovider`;
- `go test -count=1 ./internal/githublifecycle ./internal/ledger ./internal/mergelifecycle`;
- `go test -count=1 ./...`;
- `go test -count=1 -race ./...`;
- `go vet ./...`;
- non-Linux compile/fail-closed validation for the provider package;
- static production-code check proving no ordinary PR merge endpoint or REST ref-update mutation implementation;
- exact changed-path allowlist and frozen-path proof;
- `git diff --check`.

The live conformance entrypoint must be present but skipped in these implementation gates unless separately authorized acceptance fixtures are supplied.

## Completion boundary

Ralphex completion is implementation completion only. Task 3 is not independently accepted until:

1. deterministic acceptance passes against the exact implementation head;
2. separately bounded controlled-repository live conformance proves accepted two-ref mutation and both wrong-base/wrong-head all-or-nothing rejection boundaries without touching protected/product refs;
3. a fresh independent exact-head Critical/Major review returns 0 Critical / 0 Major.

EP-005 is not complete merely because Task 3 closes; final exact-head plan review/publication/merge/post-merge acceptance and status reconciliation remain governed by the master-plan completion gate.

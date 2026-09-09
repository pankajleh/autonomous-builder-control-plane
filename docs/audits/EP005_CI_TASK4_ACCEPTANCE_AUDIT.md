# EP-005 CI Task 4 Acceptance Audit

Status: deterministic acceptance passed; fresh exact-head Critical/Major review pending.

## Audit identity

- Exact CI implementation under audit: `3e0f295e8f98c348d684b2c026bacd9ceeeea911`.
- PR #13 merge containing that implementation: `bf2482f756a7c5f75906825b8f3e6e454c72f94f`, whose parents are merged-main predecessor `e11afb7d7356a0df36566d98c34adbd07a0097ae` and the exact implementation SHA.
- Accepted and merged projection predecessor: `6fd7b12c41b05e158df19b698028fb217a41eb23` (PR #14 merge), whose parents are the PR #13 merge and projection head `eb257fab925daf785bdcb924f88ddced6a0b861c`.
- Task-4 plan checkpoint and capsule base: `291f5134b461338f0e5ccddf66a22170019ff0fa`, whose parent is the PR #14 merge.
- Validation environment: `go version go1.26.8 linux/amd64`; all tests use local or injected transports and require no external service.

The authority-supplied `context-capsule-v2` at `/home/devagent/abcp-runtime/ep005-ci-task4-acceptance-pr14/context.json` had supplied and independently calculated byte SHA-256 `a14bd68095cfd244e2cfe5ebb1f6f5de8867f4c2f9773fa430ec6344ccad86e0`. `go run ./cmd/abcp context-verify --repository "$PWD" --capsule "$ABCP_CONTEXT_CAPSULE_PATH"` passed before the implementation audit. It reported internal capsule SHA-256 `1559d4eb2ee0d8b89e5b57e51115908d8a233c8df5dad6680626bdeb7570b40c`, base `291f5134b461338f0e5ccddf66a22170019ff0fa`, operation kind `implementation`, policy `context-capsule-v2`, and all 9 source hashes verified.

## Exact implementation scope proof

`git diff --exit-code 3e0f295e8f98c348d684b2c026bacd9ceeeea911 -- internal/cilifecycle docs/architecture/CI_EVIDENCE_INGESTION_CONTRACT.md` passed with an empty diff.

- `internal/cilifecycle` is tree object `6b87256a906e593cb087ff755a311075411a8135` at both the exact implementation SHA and this audit checkpoint.
- `docs/architecture/CI_EVIDENCE_INGESTION_CONTRACT.md` is blob object `44cac46ee492f0d02685bcdf2f7219c069746d2d` at both points; its file SHA-256 is `1788e6d165d6ed123db4b11bc7e11b77761306cc6bc196e21f2576794c293416` in both trees.
- No file in `internal/`, `cmd/`, the architecture contracts, roadmap, execution pack, or the operational projections was changed by this Task-4 operation.

## Design validation matrix

All commands passed:

| Check class | Command | Result |
|---|---|---|
| Formatting | `test -z "$(gofmt -l internal/cilifecycle)"` | PASS |
| Vet | `go vet ./internal/cilifecycle` | PASS |
| Package tests | `go test ./internal/cilifecycle` | PASS |
| Race tests | `go test -race ./internal/cilifecycle` | PASS |
| Regression packages | `go test ./internal/githublifecycle ./internal/evidence ./internal/ledger ./internal/authority` | PASS |
| Regression packages | `go test ./internal/prlifecycle ./internal/integrationgate ./internal/scheduler ./internal/domain` | PASS |
| Full repository | `go test ./...` | PASS |

The focused acceptance rerun used `go test -count=1 -v ./internal/cilifecycle` with the named adversarial, boundary, replay, neutrality, and principal-ordering tests below. All selected tests and subtests passed.

## Forbidden changes and semantics

The design's split-baseline forbidden-package checks passed:

- No changes relative to `fd9ed5492b326f02833d68408ed415baacb89e01` in `internal/githublifecycle`, `internal/prlifecycle`, `internal/integrationgate`, `internal/scheduler`, or `internal/domain`.
- No changes relative to `e11afb7d7356a0df36566d98c34adbd07a0097ae` in `internal/authority`.

The forbidden-semantics checks were rerun with `find` and `grep -E`, without relying on `rg`. No `ci_satisfied`, `merge_approved`, required-check, branch-protection, ruleset, non-GET method-token, or transition-field semantic was found in `internal/cilifecycle` production or test Go files. The `accept` member in request provenance is the sealed HTTP `Accept` header, not an acceptance-policy field.

## Boundary evidence

The production policy identity test `TestProductionLimitsIdentityIsCompleteAndStable` passed and confirmed that the canonical identity includes request, response, encoded aggregate, attempt/global capacity, ledger-scan, duration, and retry limits. It also confirmed a 64-hex-character digest and defensive copying.

| Boundary class | Production boundary | Existing test evidence |
|---|---|---|
| Requests | 30 collection requests, zero retries | `TestTextLinkRequestAndClockCaps` accepted the exact stricter request cap and rejected request cap+1 as `TRUNCATED/request_limit_exceeded`; `TestTransportCancellationRedirectAndZeroRetries` proved one call and no retry on timeout or redirect. |
| Response and text | 2 MiB body, 32 KiB headers, 8 KiB Link, 256-byte request ID/text | `TestTransportBodyAndHeaderExactBounds` accepted exact body/header caps and rejected each cap+1. `TestTextLinkRequestAndClockCaps` rejected over-limit principal text and Link data. The maximum profile admitted exact 256-byte escaped text. |
| Encoded size and counts | 256 suites/runs/statuses, 8 KiB provenance, 3,842,529-byte sweep, 8,012,738-byte derived maximum profile, 8 MiB hard bundle cap | `TestExactMaximumEscapingAndAggregateProfiles` constructed and round-tripped the maximum production escaping/cardinality profile and rejected cardinality limit+1. It covered `<`, `>`, `&`, quote, backslash, U+2028, and U+2029 production escaping. |
| Attempt and capacity | 16 attempts/run, 64 global attempts, 9 MiB/reservation, 576 MiB total | `TestAttemptAllocatorCapacityAndReservationLock` admitted exact stricter per-run/global/byte capacities, then rejected each next reservation. It also proved the reservation file is the same-attempt lock. |
| Ledger scan/append | 64 MiB scan, 256 KiB line, 262,144 lines | `TestMaterialLedgerRejectsProjectedBoundsBeforeAppend` admitted the exact stricter line count, rejected line limit+1, and rejected a projected byte limit+1 before append without mutating the ledger. The full persistence suite exercised bounded scans and replay; the production identity includes all three ledger bounds. |
| Duration | 10-second call timeout, 6-minute collection timeout | `TestTransportCancellationRedirectAndZeroRetries` and `TestCollectionDeadlineBoundsTheWholeObservation` used stricter injected exact deadlines, classified the first overrun as `PROVIDER_UNAVAILABLE/request_timeout`, retained provenance, and made no retry. |

## Durability, replay, and neutrality

`TestControllerPublishesLedgerOutcomeAndReplaysWithoutNetwork` proved that a completed bundle is published at its deterministic path, referenced by one deterministic `ci_evidence_collection_outcome` ledger event, and replayed byte-identically without another network call. It also verified that the event contains neither `state_from` nor `state_to`.

`TestControllerReplaysCompletedNonStableOutcome` exercised the durable completed non-success path with `UNSTABLE`: the first result produced a canonical bundle and typed error, and the second call recovered the same bundle SHA-256 and typed outcome from the ledger without a network call. This is the only non-`STABLE` outcome directly exercised through the durable controller path; the shared publication/replay path does not branch on the completed outcome. Collector adversarial tests separately produced `UNSTABLE`, `STALE_HEAD`, `TRUNCATED`, `MALFORMED`, `PROVIDER_UNAVAILABLE`, and `INTEGRITY_FAILURE` evidence. `UNSUPPORTED_PRINCIPAL` is intentionally rejected before any durable work and therefore is not a completed ledger outcome.

`TestStableEvidenceIsNeutralAcrossProviderStates` passed for empty, pending, failed, and successful provider states. Every case returned only bounded observational `STABLE`, with matching semantic sweeps and exact-head observations. The v1 bundle schema has no required-check, satisfaction, acceptance-policy, approval, merge-authorization, or transition field. `STABLE` therefore remains neutral and cannot itself authorize a merge.

## Principal rejection order

`TestUnsupportedPrincipalIsRejectedBeforeDurableOrNetworkWork` passed for an app-installation identity and a malformed/noncanonical user subject. Both returned `UNSUPPORTED_PRINCIPAL` with no bundle. The test observed:

- zero GitHub requests;
- no attempt reservation beyond the pre-existing allocator capacity lock;
- no evidence artifact; and
- no authoritative ledger file.

This proves rejection occurs before allocator reservation, evidence publication, ledger append/scan, and network hooks. `TestPreNetworkValidationAndRequestIdentity` independently confirmed invalid request identity cannot reach the network.

## Phase-4 scope reconciliation

The implementation matches only the Phase-4 `CI evidence ingestion` deliverable: read-only bounded collection, exact-head observations, immutable evidence, deterministic outcome recording, and replay. Its `STABLE` state is observational rather than policy-bearing.

The following remain outside this implementation and this audit:

- merge approval and required-check policy;
- accepted-conclusion or freshness policy;
- expected-head/base merge protection;
- merge method authorization and merge execution;
- post-merge acceptance or a `MERGED` transition; and
- automatic controller operation handoff.

These are separately governed later Phase-4 or controller-workflow concerns. This audit neither implements nor authorizes them.

## Fresh-review handoff

The deterministic Task-4 acceptance result is PASS for exact CI implementation SHA `3e0f295e8f98c348d684b2c026bacd9ceeeea911`. The audit documentation commit does not change those implementation bytes.

Publication remains blocked. A separate fresh reviewer must review that same exact implementation SHA under the repository Critical/Major policy and report 0 Critical / 0 Major before CI ingestion can be treated as publication-complete or later Phase-4 work can be authorized. This audit records no fresh-review verdict and makes no claim that the review gate has passed.

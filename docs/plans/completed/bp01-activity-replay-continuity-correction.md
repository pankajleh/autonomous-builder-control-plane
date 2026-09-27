# BP-01 activity replay continuity correction

Date: 2026-09-27

Exact base: `d510f0c743bed33cdf2a53ffe9b46604ac85c31a`

## Problem

The BP-01 provider-neutral activity collector treated the first Ralphex SSE event as if provider event IDs started at 0. Ralphex's finite replayer starts at ID 1, so healthy runs received a false controller replay-gap marker. Separately, authoritative terminal run cleanup could remove the admitted worktree before a later refresh, producing a false provider-unavailable UNKNOWN marker after `BRANCH_ACCEPTED`. BP-02 correctly rejects controller integrity/ambiguity UNKNOWN markers, so otherwise valid checkpoints became ineligible for Preview Runtime.

## Bounded correction

- Treat Ralphex event ID 1 as the expected first event when the collector has no prior provider event.
- Preserve gap detection for any later discontinuity.
- Do not append `Implementation detail unavailable` when resolver failure occurs only after authoritative terminal state (`BRANCH_ACCEPTED`, `FAILED`, or `CANCELLED`).
- Preserve fail-closed UNKNOWN behavior for all non-terminal binding/provider failures.
- Change no run-state, acceptance, preview, merge, admission, authority, or provider contract semantics.

## Allowed implementation paths

- `internal/activity/service.go`
- `internal/activity/provider_linux_test.go`
- `internal/activity/service_linux_test.go`
- this evidence document.

## Acceptance

```text
go test ./internal/activity ./internal/preview ./internal/serviceapi ./cmd/abcp
go test -race ./internal/activity ./internal/preview ./internal/serviceapi ./cmd/abcp
go vet ./internal/activity ./internal/preview ./internal/serviceapi ./cmd/abcp
go test ./...
git diff --check
```

Observed result: `BP01_REPLAY_FIX_VALIDATION_PASS`.

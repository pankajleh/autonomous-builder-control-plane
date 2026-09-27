# BP-01 activity marker diagnostics

Date: 2026-09-28

Exact base: `dfc3b93858b157c93cd074d5a1abf521e50c62cf`

## Problem

Two consecutive live PX-07 proof runs on `dfc3b93` (`admission-079b926e…` and `admission-a6185b35…`) each reached `BRANCH_ACCEPTED` with a clean checkpoint, but preview creation was rejected as ineligible. In both runs the BP-01 collector appended `PROVIDER_DETAIL / WARNING / UNKNOWN — Implementation detail integrity failure` 10–20 seconds after the provider session started, which permanently stops provider collection for the run and makes every later checkpoint ineligible for Preview Runtime.

Offline replay of both runs' complete provider streams through the unchanged `parseSSE` and `normalizeProvider` reproduced every persisted provider digest exactly, and the progress-file prefix, file identity, session generation, `Last-Event-ID` resume and branch correlation were all verified intact. The rejection therefore came from another collector step, most likely per-batch binding or worktree revalidation, but ABCP records no reason: roughly one hundred sites return the same `ErrIntegrity` sentinel and the service emits no diagnostics.

## Bounded correction

Diagnostics only; no behavioural change.

- Annotate each `collectBatch` rejection with the step that produced it (`sessions`, `correlate`, `progress-before`, `provider-read`, `revalidate-binding/<reason>`, `revalidate-scope`, `revalidate-sessions`, `revalidate-correlate`, `progress-after`, `ordering`, `normalize`, `replay`, `owner`, `proof-save`).
- Name the binding-resolution and worktree sub-step inside `Resolver.Resolve` and `resolveWorktree`, and distinguish Git subprocesses stopped by their deadline or cancellation (`git-deadline`, `git-canceled`) from Git failures (`git-exit`, `git-output-limit`).
- Emit one operator line to the service's stderr whenever a controller marker is appended: `abcp activity marker at=… run=… marker=… step=… class=…`. Every field is a controller-owned token; provider text, paths, credentials and raw error strings are never emitted, and an invalid run identifier is printed as `invalid`.
- The annotation wraps the existing sentinel, so `errors.Is` classification, fail-closed decisions, marker contents, activity schema, preview eligibility, run state, acceptance, admission and merge semantics are unchanged.

## Acceptance

```text
go test ./internal/activity ./internal/preview ./internal/serviceapi ./cmd/abcp
go test -race ./internal/activity ./internal/preview ./internal/serviceapi ./cmd/abcp
go vet ./internal/activity ./internal/preview ./internal/serviceapi ./cmd/abcp
go test ./...
git diff --check
```

`go test ./...` in the authoring container also fails `internal/mergelifecycle` (`TestTask3FinalClosureM02BudgetExhaustionDisposition`, `TestTask3FinalClosureM03PreTargetBudgetCrashRecovery`) and intermittently `internal/actionapi` (`TestDecisionConflictingReplayWorkersShareEffectAuthority`) on the unmodified base `dfc3b93`; neither package depends on `internal/activity`.

## Follow-up

Deploy, rerun the PX-07 proof, read the marker line, then make a separately reviewed correction for the specific step that fires.

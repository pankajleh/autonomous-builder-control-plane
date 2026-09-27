# BP-01 provider worktree cleanup transition

Date: 2026-09-28

Exact base: `20f2319d9291252e2c641971235faac1405d1bec`

## Problem

With the resume and startup corrections live, PX-07 proof run 4 (`admission-ab72ad8c…`) collected provider detail cleanly through implementation and review, including the caught-up quiet periods that failed before. At the end of the run two controller markers appeared:

```text
20:14:36.245 IMPLEMENTATION_COMPLETED
20:14:36.267 BRANCH_ACCEPTANCE_PENDING
20:14:36.321 marker=binding-unavailable step=worktree/branch-missing
20:14:36.340 BRANCH_ACCEPTED
20:14:38.887 marker=provider-integrity  step=revalidate-binding/worktree/branch-missing
```

The provider removes its governed worktree when it finishes, shortly before the controller records completion; the branch remains. #37 treated that cleanup as expected only once the run is terminal, so an activity read during acceptance appended `binding-unavailable`, and a collector batch in flight when the worktree disappeared turned its revalidation failure into a permanent integrity failure. Customers therefore saw an integrity failure after "Build accepted".

## Bounded correction

- A missing governed worktree is expected cleanup once implementation has completed (`IMPLEMENTATION_COMPLETED`, `BRANCH_ACCEPTANCE_PENDING`), extending the existing terminal-state rule; no marker is appended.
- After a provider proof exists, a missing worktree is pending for at most 30 seconds from its first observation. This covers the gap between the provider's cleanup and the controller recording completion. If it is still missing after the window outside those states, the existing fail-closed marker is appended. A resolved worktree resets the observation.
- A batch whose revalidation finds the worktree missing is discarded, with nothing committed, and the next refresh classifies it. Every other revalidation failure remains an integrity failure.
- Startup handling from #40 is unchanged, and so are marker contents, activity schema, preview trust rules, run state, acceptance and admission.

## Acceptance

```text
go test ./internal/activity ./internal/preview ./internal/serviceapi ./cmd/abcp
go test -race ./internal/activity ./internal/preview ./internal/serviceapi ./cmd/abcp
go vet ./internal/activity ./internal/preview ./internal/serviceapi ./cmd/abcp
go test ./...
git diff --check
```

Each new test fails when its rule is removed. `go test ./...` reports only the `internal/mergelifecycle` and intermittent `internal/actionapi` failures present on the unmodified base.

## Open item (not changed here)

Preview eligibility resolves the activity binding, which requires the governed worktree. After the provider's cleanup, an accepted run's checkpoints can no longer be previewed. The PX-07 proof follows the documented order: preview during the build, then acceptance. Allowing preview after acceptance needs a separate BP-02 trust-model decision.

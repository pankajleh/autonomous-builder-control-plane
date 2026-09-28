# BP-01 bounded trailing provider detail for retained worktrees

Date: 2026-09-28

Exact base: `7ae1a5f2d520a2d46a09620544dc0ddeb9ddfa09`

## Problem

With `worktree.retain` (PR #42), a finished run's binding keeps resolving, so each activity read of that run kept reading its Ralphex session with no end. The intent was to collect only the trailing detail written as the provider finishes. The live evidence came from the #46 eviction proof, before cutover:

- Retention proof run `admission-2b69b2cd…` was `BRANCH_ACCEPTED` at 02:45 with 0 `UNKNOWN` markers.
- ABCP was restarted at 03:27 for the Ralphex v1.7.0 cutover.
- The next read started a fresh sidecar. The sidecar had not yet listed the old session, so correlation found none (`marker=provider-unavailable step=correlate`).
- The run already had a verified provider proof, so the miss was recorded at once as a durable `UNKNOWN` marker. That marker makes the run's checkpoints ineligible for preview.

Without retention this could not happen: the worktree was gone once the run finished, and refresh stopped before any provider read.

## Correction

Refresh still records checkpoints from a retained worktree at any time. Provider reads, however, now continue only for **15 minutes after the run first finished**, meaning its first `BRANCH_ACCEPTED`, `FAILED` or `CANCELLED` transition. After the window, refresh returns no provider scope, so no sidecar is read and no provider marker can be written. Later lifecycle states such as `MERGED` count from the same first finish.

Ralphex writes its final detail within seconds of finishing. In the #42 proof, 7 trailing events, including "Review completed", arrived within 90 seconds of acceptance.

## Limits

- A marker already recorded is durable and stays: `admission-2b69b2cd…` remains ineligible for preview. This is correct fail-closed behaviour for a history with a gap.
- If ABCP restarts *inside* the 15-minute window, the fresh sidecar can still race. The window makes that rare but does not close it.

## Acceptance

```text
go test ./internal/activity ./internal/eviction ./internal/preview
go vet ./internal/activity
```

- `TestRetainedWorktreeCollectsTrailingDetailAfterAcceptance` now reads trailing detail two minutes after acceptance, inside the window.
- New: `TestRetainedWorktreeStopsProviderReadsAfterTheTrailingWindow` covers `BRANCH_ACCEPTED` and a later `MERGED`. Provider reads continue inside the window and stop after it, checkpoints are still observed, and no marker is written.

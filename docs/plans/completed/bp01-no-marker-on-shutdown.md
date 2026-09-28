# BP-01: no activity marker on controller shutdown or cancelled reads

Date: 2026-09-28

Exact base: `b187536aa194ff667b17102efb54d1ecac7d94fe`

## Problem

The #46 eviction proof restarted `abcp serve` at 04:57:35. At that moment, the stopping `a5383a6` process wrote a durable `UNKNOWN` marker into both runs it was collecting:

```text
abcp activity marker at=2026-09-28T04:57:35.783Z run=admission-f7e2967f… marker=provider-unavailable step=provider-read
abcp activity marker at=2026-09-28T04:57:35.791Z run=admission-2b69b2cd… marker=provider-unavailable step=provider-read
```

Shutdown cancels the service context and stops the Ralphex sidecars, so the in-flight provider read failed. The collect loop classified that failure as lost provider detail. Such a marker makes every checkpoint of the run ineligible for preview, so the preview after eviction was then correctly refused. Any restart during an active run could do the same.

The same exposure existed in refresh. A client disconnect cancels the request context in the middle of the binding check. Past the transition window, that cancelled check was recorded as `binding-unavailable`.

## Correction

- `appendUnknown` records no marker while the service is stopping. The next process resumes from the durable provider proof.
- The collect loop returns without a marker or diagnostic when a provider read or sidecar start fails because the service is stopping.
- Refresh returns `context.Canceled` instead of recording `binding-unavailable` when its request was cancelled or the service is stopping.

Replay-gap markers are unchanged. They come from events the provider has already delivered, not from a failed read.

Markers already recorded stay. `admission-f7e2967f…` (ordinal 882) and `admission-2b69b2cd…` (ordinal 807; its shutdown marker has the same key and was deduplicated into it) remain ineligible for preview, which is correct fail-closed behaviour for a history with a gap.

## Acceptance

```text
go test ./internal/activity ./internal/eviction ./internal/preview ./cmd/abcp
go test -race ./internal/activity
```

New test: `TestShutdownOrCancelledReadRecordsNoMarker`. A worktree absent beyond the transition window is not marked when the request is cancelled or the service is stopping, and a stopping service refuses a provider marker. The same condition is marked exactly once when the service is live.

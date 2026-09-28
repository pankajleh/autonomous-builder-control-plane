# BP-01: bounded grace for a freshly started provider sidecar

Date: 2026-09-28

Exact base: `ab4a007d745b532a2d564d691c3fff3e9fd99fca`

## Problem

A provider read that finds no session is retried a bounded number of times (6 retries, about 13 seconds with backoff) only while the run has no provider proof yet. After the first verified proof, any miss was recorded at once as a durable `UNKNOWN` marker.

A controller restart, or a sidecar respawn, starts a fresh Ralphex sidecar that can briefly list no sessions while it scans the progress directory. The first read of a run that already has a proof then marks it. This is how `admission-2b69b2cd…` got ordinal 807 (`provider-unavailable step=correlate`) after the 03:27 restart. #47 bounds provider reads to 15 minutes after a run finishes, but a restart during a run or inside that window still hit this path.

## Correction

A collect loop now treats its sidecar as fresh from the moment it acquires one until the first successful batch. While the sidecar is fresh, an unavailable read gets the same bounded budget as a run without a proof. The budget restarts whenever a sidecar is acquired again. Once this sidecar has served the run, a miss is again immediately fail-closed. Integrity failures are never deferred.

## Acceptance

```text
go test ./internal/activity
go vet ./internal/activity
```

`TestInitialProviderUnavailableRetryBudgetIsBounded` now also covers these cases:
- a fresh sidecar after a verified proof is deferred up to the budget and not past it;
- a sidecar that already served the run is not deferred;
- an integrity failure is not deferred.

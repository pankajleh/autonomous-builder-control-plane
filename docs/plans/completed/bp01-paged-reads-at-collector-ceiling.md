# BP-01: paged activity reads are served at the collector ceiling

Date: 2026-09-29

Exact base: `d01b64c` (PR #60)

Found live by Repo C: "versions unavailable" on an app's page, and previews that could not start, on apps whose builds were long finished.

## Observation

Every activity read, paged or streamed, went through `prepare`. It refreshed the run's durable store, then made sure the run had a collector goroutine. A paged read's collector retires only after a minute without reads.

At most `MaxStreams` (16) collectors run at once. With 16 alive, a read for any other run failed with `ErrExhausted`, which the API answers as `503 activity_unavailable` (retryable).

Repo C pages the activity of every build of an app to list its versions and find the newest checkpoint of each. One visit to a changed app, or a few app pages open at once, therefore touched more than 16 runs within a minute. On 2026-09-29 at 23:0xZ, 8 of 12 live apps answered `503` for their versions, and one run's activity was `503` until nothing had read any run for about 100 seconds. Nothing was logged: a refused read records no marker.

## Change

In `prepare`, a paged read that finds the ceiling reached is served from the durable store without starting a collector. The refresh that `prepare` has just run keeps the store current for the ledger facts, eviction records and checkpoints.

A live stream still needs a collector of its own, and beyond the ceiling it is still refused (`ErrExhausted`). Nothing else changes: markers, eligibility, eviction engagement (a paged read is not engagement) and the ceiling itself.

Provider detail (Ralphex progress) is collected only by a collector. A paged read served at the ceiling therefore sees the provider detail that earlier collection stored, and the next read with a free slot starts collection again.

## Acceptance

```text
go test ./internal/activity ./internal/preview ./internal/eviction ./cmd/abcp ./internal/serviceapi
go test -race ./internal/activity
go vet ./internal/activity
```

New test `TestPagedReadsAreServedAtTheCollectorCeiling`:

- with 16 collectors alive, a paged read answers from the refreshed store;
- that read starts no collector;
- a live stream beyond the ceiling is still refused;
- a live stream within the ceiling opens.

The test fails without the change ("a paged read was refused at the collector ceiling").

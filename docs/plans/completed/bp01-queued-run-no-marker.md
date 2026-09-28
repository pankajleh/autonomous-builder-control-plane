# BP-01: a queued run is never marked for its missing worktree

Date: 2026-09-28

Exact base: `bd4008332162e4bf0e047598745df9226ff463e3`

Found while building real products end to end, which the user asked for on 2026-09-28 ("finish the products until they are served").

## Observation

Two product runs were admitted to `example/product` while a third was still building.

Runs of one repository build one at a time: `abcp run` holds the repository execution lease, an exclusive lock on the Git common directory, for the whole Ralphex process. A queued run therefore stays in `RUN_CREATED` until the build ahead finishes, and it has no governed worktree until it reaches implementation.

Activity collection gave a missing worktree only the 30 s start-up grace (`worktreeTransitionWindow`). After that it recorded `binding-unavailable` (`step=worktree/branch-missing`) for both queued runs. The log repeated the diagnosis about once a second. The recorded `UNKNOWN` marker would make every checkpoint of those runs ineligible for preview, so the finished apps could never have been served.

## Change

`worktreePending` treats a missing worktree as pending, without a time limit, while the run has not begun implementing. A run has begun implementing once its history holds a transition to a state other than `RUN_CREATED`, `AUTHORITY_VALIDATED`, `EXECUTION_STARTING` or `CAPACITY_WAIT`.

Once implementation has begun, the existing bounded grace applies unchanged.

The state machine is unchanged. `CAPACITY_WAIT` is not reachable from `RUN_CREATED`, and a queued run stays in `RUN_CREATED`. Repo C tells the customer that a build still getting ready after about 90 seconds is waiting its turn.

## Acceptance

```text
go test ./internal/activity ./internal/preview ./internal/eviction ./cmd/abcp
go test -race ./internal/activity
go vet ./internal/activity
```

New test: `TestQueuedRunWaitingForTheRepositoryIsNeverMarked`.
- A queued run is not marked after 5 s, 30 s or 45 min.
- Once the run reaches implementation, a still-missing worktree is marked after the bounded window.
- The test fails without the change: the run is marked after 30 s.

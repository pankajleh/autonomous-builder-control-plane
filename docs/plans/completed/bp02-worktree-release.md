# BP-02: product-requested worktree release and eviction activity

Date: 2026-09-28

Exact base: `928f94acf48f197ff73a33e798bc27c4d20de1e7`

Requested by the user on 2026-09-28:
- delete worktrees when a task closes or a tenant is deleted;
- show the deletion in the activity UI.

Repo C has no task closure and no tenant model. The user chose to add a final "Close task" action in Repo C and, for tenants, to build only the ABCP side.

## Release API

Both endpoints require the service principal to hold the new authority grant `worktree.release`.

| Endpoint | Body | Result |
|---|---|---|
| `POST /v1/runs/{run}/worktree/release` | `{"schema_version":1,"reason":"task-closed"}` | `WorktreeReleaseV1`: `status` is `RELEASED`, `ALREADY_RELEASED`, `NOT_RETAINED` or `NO_WORKTREE`, with `reason` and `released_at` for a sealed eviction |
| `POST /v1/worktrees/release` | `{"schema_version":1,"repository_identity":"owner/repo","reason":"tenant-deleted"}` | `RepositoryWorktreeReleaseV1`: counts of `released`, `already_released`, `unfinished` and `failed` |

A release uses the same pin → seal → remove order as a policy eviction, so activity and exact-source preview survive it.

The policy rules and protection do not apply. A release happens even if the run was recently used, is being streamed, or has a live preview. This is safe because preview sources are separate checkouts, and pinned checkpoints keep preview available.

A run that is not finished, including one paused for a human decision, is refused with `409 run_not_finished`, and its worktree is kept. For a repository release, such runs are counted as `unfinished`. Releasing an evicted run is idempotent, and it completes an eviction that was interrupted after sealing. Runs whose binding cannot be verified are skipped in a repository release.

Sweeps and releases are serialized by one mutex. The sweeper is built even when periodic eviction is disabled (`--worktree-eviction-interval 0`), so releases still work.

The eviction record accepts the reasons `task-closed` and `tenant-deleted`, and `evicted_at` must now parse as RFC 3339.

## Eviction activity event

Every sealed eviction becomes one ABCP state activity event, whatever its reason.

| Field | Value |
|---|---|
| `authority_level` | `ABCP_STATE` |
| `category` / `status` | `WORKSPACE` / `RELEASED` |
| `source_kind` | `ABCP_WORKTREE_EVICTION` |
| `activity_id` | `identity(run, "worktree-eviction")`, so at most one per run |
| `occurred_at` | the sealed eviction time |
| `title` | one of five fixed titles: `Worktree evicted after inactivity`, `Worktree evicted at maximum age`, `Worktree evicted to free repository space`, `Worktree released: task closed`, `Worktree released: tenant deleted` |

The event is derived from the durable record on activity refresh, rather than written by the sweeper. This reports an eviction sealed before a crash, as well as evictions sealed before this change.

**Deploy order.** Repo C's activity contract rejects unknown categories, statuses and source kinds, which would fail the whole activity page of an evicted run. Repo C must therefore accept this event (Repo C P0059) before this ABCP version is deployed.

## Acceptance

```text
go test ./internal/eviction ./internal/activity ./internal/serviceapi ./internal/preview ./internal/runadmission ./cmd/abcp
go test -race ./internal/eviction ./internal/activity ./internal/serviceapi
go vet ./...
```

New tests:
- `TestReleaseEvictsAFinishedRunRegardlessOfPolicyAndProtection`
- `TestReleaseRefusesUnfinishedRunsAndPolicyReasons`
- `TestReleaseRepositoryReleasesOnlyThatRepositoriesFinishedRuns`
- `TestSealedEvictionIsRecordedOnceAsAnABCPStateEvent`
- `TestWorktreeReleaseRequiresTheServiceGrant`
- `TestWorktreeReleaseValidatesRequestsAndMapsOutcomes`

## Cutover

1. Deploy Repo C P0059: it accepts the eviction event and adds Close task.
2. Add `worktree.release` to the host `grants.json`, which is versioned in `deploy/local-integration/abcp-config/`.
3. Build `abcp` from the merge SHA and restart it with no run executing.

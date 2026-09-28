# BP-02 controller-owned worktree eviction

Date: 2026-09-28

Exact base: `fbf1ab8a1cb6a8f44286472de5d6d9a9491f9cfd`

Requested by the user on 2026-09-28: a SaaS deployment cannot keep governed worktrees forever. A worktree must be deleted once an eviction policy applies, and a preview of an evicted run must still work.

## Problem

PR #42 made worktree removal controller-owned (`worktree.retain`, Ralphex `--keep-worktree`), but nothing removed a worktree afterwards. Retention was indefinite, and disk was the only bound. The next step recorded in `bp02-controller-owned-worktree-retention.md` (pin checkpoints, seal the binding, allow eligibility from sealed state, then prune) is implemented here.

## Policy

Only worktrees that ABCP retains (manifest `worktree.retain`) are considered. A retained worktree is evicted when its run is finished and any one of these rules applies:

| Rule | Default | Measured from |
|---|---|---|
| Idle | 24 hours | the later of the run finishing and the last activity read, stream or preview resolution |
| Maximum age | 7 days | the run finishing, regardless of use |
| Quota | 5 GiB per governed repository | across all retained worktrees in that repository; least recently used first |

The quota evicts least-recently-used evictable worktrees until usage fits. Protected worktrees still count toward usage, but they are never chosen.

Finished states are `BRANCH_ACCEPTED`, `FAILED`, `CANCELLED`, `MERGED` and `COMPLETED`. `HUMAN_DECISION_REQUIRED` is not finished, because the run can resume.

A worktree is **protected**, and never evicted, in any of these cases:

- the run is unfinished;
- a client is streaming its activity;
- one of its previews is live, meaning not terminal and not expired.

Runs are skipped, never evicted, in any of these cases:

- the binding, state or worktree path cannot be verified;
- the worktree is outside the governed repository.

Last access is tracked in memory and floored at process start, so a restart never shortens an idle window.

`abcp serve` flags (a zero value disables that rule; negative values are rejected):

```text
--worktree-eviction-interval    10m     sweep period (0 disables eviction)
--worktree-eviction-idle        24h
--worktree-eviction-max-age     168h
--worktree-eviction-quota-bytes 5368709120
```

## Eviction order

A sweep evicts in this order:

1. **Pin.** The activity service first records the worktree's current clean head, so a final commit nobody has read yet is not lost. Then every clean `CHECKPOINT/AVAILABLE` SHA recorded for the run is pinned at `refs/abcp/checkpoints/<run>/<sha>`. Pinning first verifies the commit exists, then creates the ref only if it is absent (`update-ref <ref> <sha> ""`). An existing ref that points elsewhere is an integrity failure, and the eviction stops before any record is written.
2. **Seal.** A `WorktreeEvictionV1` record is written to `<service-root>/evictions/<run>.json`. The directory has mode `0700` and must be owned by the service user; the file has mode `0600` and is written atomically (temp file, fsync, rename, directory fsync). The record holds the authority digest, repository, branch, base, worktree path, reason, time, size and every pinned checkpoint. Reads refuse symlinks, loose permissions, unknown fields and invalid values.
3. **Remove.** `git worktree remove --force` runs only while the path still holds `abcp/<run>` and lies inside the repository. The branch is kept.

If a sweep is interrupted after sealing, the next sweep finds the record and completes the removal instead of deciding again. Each eviction is logged as `abcp worktree evicted at=… run=… reason=… bytes=… checkpoints=N`. Each sweep that finds retained worktrees also logs why each one is kept (PR #48): `abcp worktree eviction sweep at=… retained=N kept-unfinished=… kept-streaming=… kept-preview=… unverified=… evicted=… failed=…`. A retained worktree that is none of these is simply not yet due.

## Preview after eviction (sealed state)

BP-02 eligibility normally resolves the live worktree. For an evicted run it falls back to sealed state, and only when all of these hold:

- a valid eviction record exists;
- the binding still verifies without the worktree (`activity.Resolver.ResolveBinding`: registration, admission binding, manifest, plan and repository);
- the record's authority digest, repository, branch and base equal the binding's.

The rest of eligibility is unchanged: capsule, plan, a clean `CHECKPOINT/AVAILABLE` event with no integrity marker, and the base as an ancestor. One check is replaced: the checkpoint must be pinned in the record and its ref must still resolve to that SHA, instead of being an ancestor of the branch. The source is then fetched by the pinned ref into an empty repository (`git init`, `fetch --depth=1 origin <ref>`). This works even if the branch is later deleted and garbage collected. The ref must be under `refs/abcp/checkpoints/<run>/`. The origin, `FETCH_HEAD` and reflogs are removed as before, so the source carries no host path.

## Activity after eviction

An evicted run's missing worktree is expected cleanup in every later state. Refresh checks the sealed record (`Service.SetEvicted`) before any other missing-worktree rule, so reads of an evicted run never add a false `UNKNOWN` marker. This also covers `MERGED` and `COMPLETED`, which are not activity-terminal states. An invalid or unreadable record is not an eviction, and its missing worktree is still marked.

## Not in this slice

- A customer-visible "worktree evicted" activity entry. The Repo C activity contract only accepts fixed categories, statuses and source kinds, so an entry would need a matching Repo B and Repo C contract change. Eviction is recorded in the ABCP store and the diagnostic log.
- Eviction triggered by the product task closing or the tenant being deleted. Both need a Repo C and tenant integration.
- Last-access tracking that survives a restart.

## Acceptance

```text
go test ./internal/eviction ./internal/preview ./internal/activity ./internal/serviceapi ./internal/runadmission ./cmd/abcp
go test -race ./internal/eviction ./internal/preview ./internal/activity
go vet ./...
gofmt -l internal cmd   (only the pre-existing internal/run/resume.go)
git diff --check
```

New tests:

- `TestDecideAppliesAgeIdleAndProtection`, `TestDecideQuotaEvictsLeastRecentlyUsedPerRepository`, `TestDecideZeroDisablesEachRule`
- `TestSweepEvictsIdleWorktreeAfterPinningCheckpoints`, `TestSweepNeverEvictsProtectedUnfinishedOrUnretainedWorktrees`, `TestSweepCompletesAnInterruptedEviction`, `TestSweepRefusesAConflictingPinnedRef`, `TestStoreRejectsInvalidOrForeignRecords`
- `TestEvictedRunPreviewsThroughPinnedCheckpointWithoutBranch`, `TestEvictedRunRejectsUnsealedOrMismatchedPins`
- `TestControllerEvictedWorktreeIsExpectedCleanupInAnyLaterState`, `TestCheckpointsRecordTheFinalHeadBeforeEviction`

The full `go test ./...` fails only in `internal/mergelifecycle` (`TestTask3FinalClosureM03PreTargetBudgetCrashRecovery`), and it fails identically on the base `fbf1ab8`. This is the known environment-dependent failure.

## Cutover

1. Merge, then build `abcp` from the merge SHA and restart `abcp serve` with no run executing. The defaults apply without new flags.
2. Live proof:
   - evict a finished retained run with a short idle window;
   - verify that the worktree is gone, the branch and pinned refs remain, and the record is valid;
   - verify that activity reads add no `UNKNOWN` marker;
   - verify that a preview created after eviction is `READY` and serves the checkpoint;
   - restart with the default policy.

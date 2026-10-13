# Remote restore: restoring a Node app's backup, checking backups, and nightly backups (BF-14.2b-1, second part)

Status: **live since 2026-10-13 23:04 UTC on `fbff5c3`** (#97, recorded in #98); no Node app on a target yet, so no nightly
backup has run. Repo C's side follows. Repo C design note
`BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md` §11 (BF-14.2b, P0480), under the owner's approval of the design ("agree with
1,2,3") and "go with your recommendation options" (2026-10-13). Builds on `docs/plans/remote-node-apps.md` (#95).

## What changes

- **Every backup counts its rows.** The backup script opens a `REPEATABLE READ` transaction on the app's database,
  exports its snapshot, counts each ordinary table's rows there (`query_to_xml`, one JSON object through the Postgres's
  own `/scratch`) and runs `pg_dump -Fc --snapshot=…` from inside that transaction (psql's `\!`), so the counts are the
  rows the dump holds. A backup says `tables` and `rows`; ABCP keeps each table's count in its own record.
- **`POST /v1/targets/{key}/sites/{name}/backups/{backup}/check`** `{request_id}`: an operation (kind `check_backup`,
  step `check_restore`) that restores the backup, as the app's login (`pg_restore --role=<login> --no-owner
  --no-privileges --single-transaction --exit-on-error`), into `check_app_<site>` (owned by the app's login, `CONNECT`
  revoked from everyone), compares every table's rows with what the backup was taken with, records the outcome on the
  backup (`check`: time, whether it matched, and up to three tables that differ) and drops the copy. The same request
  answers the same operation; the app keeps running throughout.
- **Restore plans:** `POST /v1/targets/{key}/plans` with only `{restore: {site, backup}}` answers a `PlanV1` of
  `kind: "restore"` naming the backup, the release the app keeps running, its database, images and sandbox, sealed by
  its hash like any plan, and applied the same way (operation kind `restore`). Its steps: `back_up` (reason
  `before_restore`; it never prunes the backup being restored), `restore_copy` (the check above into
  `restore_app_<site>`, kept when the rows match, dropped when not, the app still running), `stop_app`,
  `switch_database` (one transaction renames the app's database to `previous_app_<site>` and the copy to `app_<site>`;
  the login, its password and `database.env` do not change), `start_app`, `wait_health`, `check`, `clean_up` (drops
  `previous_app_<site>`). When the app never answers `/health`, ABCP stops it, swaps the databases back and starts it
  again; when the swap fails, the database is as it was and the app is started again. A plan no longer applies once
  the app serves another release, is gone, or the backup is no longer kept. Only a backup whose rows were counted can
  be restored. `404 backup_unknown` for a backup that is not kept.
- **Nightly backups:** `abcp serve --targets-dir` starts a scheduler that, at 02:30 India time every night, runs one
  operation per confirmed target that serves a Node app (kind `nightly`, no site; steps `back_up` and `check_restore`
  for each app, named by `site`), one target after another, each in one SSH session. One app's failure does not stop
  the next. A target busy at 02:30 is tried once more 15 minutes later; a target that does not answer is tried again
  the next night, not in a loop (Repo C `NOTIFICATION_POLLING_BUDGET.md`). A night runs at most once per target (its
  request id is `nightly-<date>`).
- Reasons are now `before_update`, `before_restore`, `nightly` and `manual`. Operation steps may name a `site`.

None of `check_`, `restore_` and `previous_` starts with `app_`, so no copy can be another app's database, and the
`pg_hba` rule `sameuser` lets no login reach one.

## Tests

`internal/remotetarget/restore_linux_test.go`, with the fake `docker` now keeping each database's rows and carrying
them through dumps, restores and renames: a backup records its rows; a check restores into a database of its own,
leaves the app's untouched, records the outcome, and fails naming the tables when rows are lost; a restore plan's
steps, hash and fields, applied once, the copy checked before the swap, the swap one transaction, a backup first; an
app that never answers gets its database back as it was; a copy that loses rows is never swapped in; a stale plan is
refused; nightly backups for each Node app (not the static site), once a night, a busy target answered for its retry,
an unreachable one failed and not retried; the night at 02:30 India time; counts compared table by table. The scripts
themselves were run once against a real Postgres (16) through a `docker exec` stand-in: the snapshot-counted dump,
the check, the copy, the swap and the swap back, and a missing or cut dump. `internal/serviceapi/deployments_test.go`
gains the restore plan and check routes.

## Going live

Restart ABCP on the merged commit with no build running (same flags), and check that the check route answers `404`
for an unknown target and a restore plan for an unknown target `404`. With no Node app on any target, the nightly
scheduler has nothing to do until host 1 is enrolled. Repo C's side (Restore and Check on the Servers page) follows.

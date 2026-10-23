# An app on a server goes back with its data as it was (Repo C BF-10d2b)

Status: **built.** Repo C's design note `BF10_GOING_BACK_SAFELY.md` §10.2 (the owner, 2026-10-23: "all as recommended";
the queue: "please continue building as per queue"). The same for a share link needed no ABCP change: a hosted restore of
a backup from another version already starts that version, then puts its data back (BF-10d2a, Repo C P0709).

## Why

Going back to an earlier version updates the app's server to that version's release, on today's data. When the version
the app runs changed its data in a way the earlier one cannot use (a table or column dropped or renamed, a required
column added), the earlier release would start on data it does not understand. Its own data, as it was when it was
replaced, is in the backup every update takes first (`before_update`, `remote-node-apps.md`), taken while it still ran.
A restore plan (`remote-restore.md`) puts a backup back under the release the app runs now, never under another.

## The plan

`POST /v1/targets/{key}/plans` takes an optional `start_from`: one of the app's backups, by id. It is accepted only for a
Node release that replaces the release a running Node app serves, on a site whose database ABCP keeps, and not with a
`conversion` (`invalid_request`); a stopped app is started first (`site_stopped`). The backup must be listed, with its
rows counted (`backup_unknown`), and taken while that same release ran (its `release` is the plan's release;
`invalid_request` otherwise), so the release starts on data it wrote itself. Releases are named by their content, so an
earlier version built again is the same release (seen live: Crew Access Desk's second version, `9896be5d`, deployed on
2026-10-22 and again on 2026-10-23). The plan shows the backup as `start_from`, which its hash covers.

Its steps: `send_files` (or `reuse_files`), `get_images`, `start_database`, `make_database`, `stop_app`, `back_up`,
`restore_data`, `switch_site`, `start_app`, `wait_health`, `write_site`, `start_frontdoor` (or `reload_frontdoor`),
`check`, `clean_up`. Applying it refuses (`plan_stale`) when the app was stopped or its data removed since, or the backup
is no longer kept.

## Applying it

- **`stop_app`** before the backup, so nothing written after it is lost.
- **`back_up`** as before an update (`before_update`, rows counted, under the release it replaces): today's data, kept.
- **`restore_data`** restores the chosen backup into `restore_app_<site>` as the app's login and compares every table's
  rows with the backup's (as a restore does), then swaps it in in one transaction, keeping today's database as
  `previous_app_<site>` ("1 tables, 3 rows, as when it was taken; switched; the database it replaced is kept until the
  app answers").
- **When the copy differs or the swap fails**, the app's database is as it was and the app that ran is started again
  ("…; nothing changed and the app is started again").
- **When the release never answers** (or does not start, or its link cannot be switched), today's database is swapped
  back and the release it replaced is linked and started again ("…; the data it replaced is put back; switched back to
  the release it replaced").
- **`clean_up`** drops `previous_app_<site>` once the site checks.

## Tests

`internal/remotetarget/startfrom_linux_test.go`: an update starts its release on a backup of that release, in order
(stopped, backed up, restored, started), today's data kept as a backup, and forward again on that backup; a copy that
loses rows and a release that never answers both leave today's data and release running; a backup of another release, the
release it runs, an unknown or malformed backup, a conversion as well, a first version and a stopped app are refused, and a
plan made before the app stopped no longer applies. `internal/serviceapi/deployments_test.go`: a plan carries
`start_from` to the backend.

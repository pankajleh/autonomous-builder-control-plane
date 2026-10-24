# A backup says what the app's database held, table by table (Repo C BF-06a)

Status: **live** on ABCP since 2026-10-24 17:11 UTC (`cb7bd74`, #141; recorded in #142). Repo C's design note
`BF06_YOUR_APPS_DATA.md` §4.1 (the owner, 2026-10-24: "all as recommended, go ahead", every §7 answer A). Repo C lists the
tables and their records on the app's card and the Servers page from the newest backup, and leaves out empty tables and the
tables the app's platform keeps for itself (`autobuild_*`).

## What changes

**`internal/pgcount`** holds what both kinds of backup share: the fixed query that counts every ordinary table's rows in
one JSON object (moved unchanged from `internal/remotetarget`), its bounded reader (at most 2000 tables, names of at most
260 bytes, no negative counts), the totals, and `Top`: the `serviceapi.BackupCountLimit` (20) tables with the most rows,
in name order, among names of at most 64 bytes (a longer one is counted in the totals but not named), so a listing of
every backup stays a few tens of kilobytes.

**An app on a server.** Its backups already counted each table in the dump's own snapshot and kept the counts with the
backup (for checks). `GET /v1/targets/{key}/sites/{site}/backups` now names them: each `BackupV1` gains `counts`, a list of
`{table, rows}` from `Top`. Nothing new is kept, so backups taken before this name their tables too. Plans that carry a
backup (restore, start from a backup) do not.

**A share link's app.** `HostedBackupV1` gains `tables`, `rows` and `counts`. Just after the dump, ABCP runs the psql beside
the profile's `pg_dump` backup command, as the same user in the same database, with the fixed query in one read-only
`REPEATABLE READ` transaction, inside the app's database container. The counts are taken just after the dump, not in its
snapshot: a share link's backup is the profile's plain `pg_dump`, and changing how it is taken is not worth the risk to
backups. Rows written in the second between them can make the counts differ from the file by those rows. A count that
fails leaves the backup as it was, without counts; a profile whose backup command is not `pg_dump` has none.

Every answer is checked before it is sent: counts are whole or absent, at most 20, in name order, none unnamed or negative,
and never more than the tables counted.

## Tests

`internal/pgcount`: bounds, totals, the biggest 20 in name order, a long name counted but not named. `internal/remotetarget`: a backup's listing names each
table with its rows. `internal/preview`: a hosted backup carries what its database held; a failed count leaves the backup
without counts; the count's command is the psql beside `pg_dump` with its host, user and database, and nothing else is
counted. `internal/serviceapi`: a hosted backup's counts are whole, bounded and ordered.

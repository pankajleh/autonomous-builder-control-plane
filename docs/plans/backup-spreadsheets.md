# A backup taken out as spreadsheets (Repo C BF-06b)

Status: **built**, not live yet. Repo C's design note `BF06_YOUR_APPS_DATA.md` §4.2 (the owner, 2026-10-24: "all as
recommended, go ahead", every §7 answer A). Repo C offers **Download as spreadsheets** beside **Download** on each backup,
for the app's owner and administrators, after the same personal-data warning.

## What changes

**`internal/pgcsv`** turns a backup's data into a zip with one CSV file per table. It reads what `pg_restore --data-only`
writes for the backup, as text: each `COPY schema.table (columns) FROM stdin;` block and its rows, up to `\.`. So no
database is restored and nothing from the app or a person becomes part of a command: the tables and their columns come
from the backup itself. Settings, sequence values and anything else are passed over.

- **Files:** one per table, named by the table (with its schema unless that is `public`), as letters, digits, `_`, `-` and
  `.`, at most 100 bytes, made unique. Each starts with a UTF-8 byte order mark and a header row of its columns, so
  spreadsheet programs read it as UTF-8. Lines end in CRLF. An empty table is its header row only. Each file's time is the
  backup's.
- **Values:** COPY's escapes are decoded and `\N` (null) is an empty field. A value that starts with `=`, `+`, `-`, `@`, a
  tab or a carriage return, and is not a number, gets a leading apostrophe, so a spreadsheet never runs it as a formula.
- **Left out:** the tables the app's platform keeps for itself (`autobuild_*`), as on Repo C's card.
- **Bounds:** at most 2000 tables, rows of at most 64 MiB as pg_restore writes them, and a zip of at most 1 GiB. Data that
  does not read as pg_restore writes it (a block without its end, a row with the wrong number of fields, a lone
  backslash) is an error.

The decision against the design note's "restore into a separate database, then write each table out": reading the dump's
own data needs no copy database, no disk on the server and no `zip` there, and gives the same rows.

**An app on a server.** `GET /v1/targets/{key}/sites/{site}/backups/{backup}/spreadsheets`. Only a listed backup whose file
still has its kept size is read. The server runs `docker exec -i autobuild-postgres pg_restore --data-only --no-owner
--no-privileges -f - < <its file>` and ABCP makes the zip as the text arrives. It runs beside operations, as the backup
file download does, since it changes nothing.

**A share link's app.** `GET /v1/hosted/{key}/backups/{id}/spreadsheets`, through the hosting grant. The pg_restore beside
the profile's `pg_dump` backup command, in the ready instance's data container, reads the backup's file on its standard
input. A profile whose backup command is not `pg_dump`, or an instance that is not ready, is a conflict (409).

**The answer.** `application/zip`, `no-store`, read as it is made, so no `Content-Length`. Nothing is sent until the zip's
first bytes are, so a backup whose data cannot be read at all is an error answer (`503`). A failure after that aborts the
response (`http.ErrAbortHandler`), so the receiver sees the connection end and never takes an unfinished zip for a whole
one.

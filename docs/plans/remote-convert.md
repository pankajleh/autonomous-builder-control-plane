# Remote conversion: an app replaced by another, its data converted (BF-14)

Repo C's design note `BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md` (§11, "An app replaced by another, its data converted"). The
owner, 2026-10-14, about two separate Garment Production apps: "The newer app, with migration and publish it on wages". A
site's release can already be replaced by another app's release, and the new one starts on the site's database; but when
the two apps lay their data out differently, the new app's own migrations fail on the old tables, the update switches
back, and nothing converts the data. ABCP alone holds a target's SSH key and runs only fixed scripts on it, so the
conversion is part of an approved plan, carried out by ABCP like every other step.

## The plan

`POST /v1/targets/{key}/plans` takes an optional `conversion`: SQL, at most 64 KiB of UTF-8 text with no NUL and no
backslash (so psql reads it as SQL only and runs no command of its own). It is accepted only for a Node release that
replaces the release a running Node app serves, on a site whose database ABCP keeps; a stopped app is started first
(`site_stopped`). The plan names it as `conversion: {digest, bytes}` (SHA-256 and size), so its hash, and an approval of
it, covers the exact SQL; ABCP keeps the SQL with the plan, and applying it refuses (`plan_stale`) unless the kept SQL
still has that digest and size.

A plan request has a bound of its own, 4 KiB plus six bytes for every byte of the largest conversion (JSON writes a byte
as at most six, `\u00XX`), 388 KiB in all; every other target request keeps 4 KiB. (#110: the first live plan with a
conversion, 4,595 bytes of SQL, was refused as `invalid_request` by the 4 KiB bound before anything ran.)

Its steps: `send_files` (or `reuse_files`), `get_images`, `start_database`, `make_database`, `stop_app`, `back_up`,
`convert`, `switch_site`, `start_app`, `wait_health`, `write_site`, `start_frontdoor` (or `reload_frontdoor`), `check`.

## Applying it

- **`stop_app`** before the backup, so nothing written after the backup is lost.
- **`back_up`** as before an update (`before_update`, rows counted); the operation keeps its id.
- **`convert`** runs the SQL once on the app's own database as the app's own login (which can reach no other database),
  over the Postgres's own loopback, in one transaction that stops at the first error (`psql --single-transaction -v
  ON_ERROR_STOP=1 -f -`, the SQL on its input), then counts every table's rows ("converted; N tables, M rows"). The
  login's password goes from `database.env` through a file in the Postgres's `/scratch` only its user reads, removed
  after; no command line holds it.
- **When the conversion fails**, nothing changed (one transaction) and the app that ran is started again: "the
  conversion failed: <psql's first line>; nothing changed and the app is started again".
- **When the new release never answers** (or does not start, or its link cannot be switched), the data comes back from
  the backup taken just before: restored into `restore_app_<site>` as the app's login, its rows checked against the
  backup's, swapped in in one transaction, the converted database dropped; then the release it replaced is linked and
  started again ("…; the data is put back as it was before; switched back to the release it replaced").

## Tests

`convert_linux_test.go`, against the fake Docker (which now accepts `exec -e`): the plan's steps, digest and hash; the
order (stopped, backed up, converted, then the new release started); the SQL exactly as given, as `app_crm` with the
psql flags above; the password only in the removed file, on no command line and in no script; a failing conversion
leaving the data and starting the old app; a converted app that never answers getting its data and release back; and
the refusals (a psql command, a backslash, NUL, nothing, too long, not UTF-8, a first version, the release it runs, a
stopped app, and SQL changed in the plan's record after the plan was made).

`deployments_test.go`: a plan with a conversion of the largest size, written as JSON escapes throughout, reaches the
backend as given, a plan past its own bound is refused before the backend sees it, and an apply keeps 4 KiB.

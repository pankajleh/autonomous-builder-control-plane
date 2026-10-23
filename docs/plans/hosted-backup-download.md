# A hosted app's own data: restores back up first, and backups are handed out (Repo C BF-10c1)

Status: **built.** Repo C's design note `BF10_GOING_BACK_SAFELY.md` §4 and §9 (approved by the owner on 2026-10-23: "all
as recommended", decisions 2 A and 4 A: a hosted app's owner restores with the same confirmation as on a server, and
the owner and administrators download a backup after a confirmation that it holds the app's users' personal data).

## Why

A server's restore backs up the data as it is first (`before_restore`, `remote-restore.md`); a hosted restore did not,
so the owner's data as it was before a restore was lost once replaced. Repo C §5 rule: nothing overwrites data without a
backup first. And a backup was only ever restored, never handed to the owner.

## Restore

`POST /v1/hosted/{key}/restore`, under the key's command lock, now:

1. checks the backup is listed and opens its file;
2. takes a backup of the instance as it is now, reason `pre-restore`, copies it off the host like any other
   (`hosted-offsite.md`), and saves it in the key's state, so it is kept even if the restore then fails;
3. only then loads the chosen backup.

Without that backup (the dump fails or is empty) nothing is restored, and the restore answers `NOT_AVAILABLE`. A
replayed restore answers its first result and backs up nothing again. Older backups are pruned by the next backup, never
during a restore, so the one being read is never removed. `HostedBackupV1.reason` may now be `pre-restore`; the backup
command itself still takes `manual` or `pre-update`.

## Download

`GET /v1/hosted/{key}/backups/{backup id}` (the hosting grant, as the other hosted reads) answers one listed backup's
file as it is: `application/octet-stream`, its `Content-Length`, `Cache-Control: no-store`, `nosniff`. Never parsed,
logged or kept by ABCP beyond the file it already holds. An unknown backup, another key or another principal's key is
`not_found`; a malformed id or path is `invalid_request`. Repo C checks who may download (the app's owner or an
administrator, after a confirmation) and names the file.

## Tests

`internal/preview/hosted_restore_linux_test.go`: a restore backs up the data as it is now first, restores the chosen
backup, keeps its first result on a replay, and the backup taken first can itself be restored; without that backup
nothing is restored; the backup taken first is kept when the restore fails; a backup file is handed out as it is and
only for its key and principal. `internal/serviceapi/hosting_test.go`: the file route's body and headers, its refusals,
its errors and the hosting grant.

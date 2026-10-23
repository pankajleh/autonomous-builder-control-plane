# An app on a server: its backups handed out (Repo C BF-10c2)

Status: **built.** Repo C's design note `BF10_GOING_BACK_SAFELY.md` §9 (approved by the owner on 2026-10-23: "all as
recommended", decision 4 A: the owner and administrators download a backup after a confirmation that it holds the app's
users' personal data). The same for hosted apps is `hosted-backup-download.md`.

## Why

A Node app's backups on its own server (`remote-nodeapps.md`) were only ever restored or checked, never handed to the
owner. A server session's output is bounded (`limitedBuffer`), so a dump could not be read through it.

## Streaming read

The server session gains `Stream(ctx, script, out)`: the script's output goes to `out` as it comes, neither held nor
bounded; its error output stays bounded and gives the failure's last line. It ends when the request ends or after
30 minutes.

## Download

`GET /v1/targets/{key}/sites/{site}/backups/{backup id}` (the targets grant, as the other target routes):

1. the backup must be listed for the site, with a size kept for it, on a confirmed target; otherwise `backup_unknown`,
   `site_unknown`, `target_unknown` or `target_not_ready`;
2. ABCP signs in to the server and reads the file's size there; a file that is gone or no longer has that size is
   `backup_unknown`, a server that cannot be reached is `target_unavailable`;
3. then the file is read with `cat` and answered as it is: `application/octet-stream`, the kept size as its
   `Content-Length`, `Cache-Control: no-store`, `nosniff`. Never parsed, logged or kept by ABCP. A read cut short ends
   the response early, so the receiver sees fewer bytes than its length.

It changes nothing on the server, so it runs beside operations without taking the target. Repo C checks who may download
(the app's owner or an administrator, after a confirmation) and names the file.

## Tests

`internal/remotetarget/download_linux_test.go`: a backup is read as it is with its kept size; closing early ends the
read; unknown, malformed or other sites' backups, an unknown target, a file changed or gone, and an unconfirmed target are
refused. `internal/serviceapi/deployments_test.go`: the file route's body and headers, its refusals, its errors and the
targets grant.

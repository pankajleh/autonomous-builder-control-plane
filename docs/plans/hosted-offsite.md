# Hosted backups off the host (Repo C BF-10b)

Status: **built, off until its settings file exists.** Repo C's design note `BF10_GOING_BACK_SAFELY.md` §8 (approved by
the owner on 2026-10-23: "all as recommended", decision 3 A: hosted backups go to the same S3 bucket as servers'
backups, kept 30 days).

## Why

A hosted app's backups (`before an update`, `manual`, `nightly`, kept 7 days and at most 32) sit on the same machine as the
app. A lost disk or host loses both. Servers' backups are already copied off the server (`remote-offsite.md`); hosted
apps run on Autobuild's own server, which is not in AWS, so they cannot use host 1's instance role.

## The copy

`abcp serve --hosted-offsite-file <path>` names a private file (mode 0600, owned by ABCP's user):

```json
{"bucket": "<name>", "region": "<region>", "access_key_id": "<id>", "secret_access_key": "<secret>"}
```

The key belongs to the IAM user `autobuild-hosted-backups`, which may put and get objects under `hosted/` in that bucket
and list that prefix: no delete, nothing else (Repo C `deploy/bf14/hosted-backups.sh`). The file is read afresh at each
backup, so writing it, changing it or removing it needs no restart; one that is missing, readable by others or not
valid copies nothing.

After each hosted backup, ABCP puts the dump at `s3://<bucket>/hosted/<hosting key>/<backup>.dump`:

- signed with Signature Version 4, the dump's SHA-256 as its payload hash (no SDK);
- read back (`HEAD`) and its size compared;
- recorded on the backup as `offsite: {at, ok, detail}` (`detail`: the `s3://` address, or why it was not copied, never
  the key or a response body), which the hosted backup routes answer as part of `HostedBackupV1`.

A copy that fails never fails the backup or the update it guards: the backup stays on the host, and the next backup of the
same key tries up to three earlier ones not copied yet. A replayed backup command answers its first result and copies
nothing again. The bucket's own rule removes copies after 30 days; the host keeps its 7 days as before.

## Tests

`internal/preview/hosted_offsite_linux_test.go`: a fake S3 that checks every request's signature: a backup copied and
read back, the key never answered; a refused copy kept on the host and copied with the next backup; a settings file
readable by others, or none, copies nothing, and one written later is read at the next backup.

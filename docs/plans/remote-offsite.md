# Remote backups off the server (BF-14.2b-2)

Status: **live since 08:28 UTC on 2026-10-15** (ABCP `b8f7b97`, recorded in #115); host 1's backups are copied to the owner's
bucket, proved on the QA account's Team Kudos at 08:29 UTC.

Repo C's design note `BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md` (§12 decision 5, approved by the owner on 2026-10-14: "ok for
S3 bucket too"). The owner's CloudShell paste (Repo C `deploy/bf14/backups-bucket.sh`) made the bucket
`autobuild-bf14-backups-<account>` in `ap-south-1` (private, encrypted, objects kept 30 days) and attached the instance
profile `autobuild-bf14-host` to host 1: it may put, get and list objects in that bucket only, with no delete. ABCP
alone holds a target's SSH key and runs only fixed scripts on it, so the copy is one of them.

## The copy

When the targets folder's `offsite.json` names a bucket for a target —
`{"targets": {"<key>": {"bucket": "<name>", "region": "<region>"}}}`, read afresh at each backup so a change needs no
restart — every backup ABCP takes there (`before_update`, `before_restore`, `nightly`, `manual`) is also copied to
`s3://<bucket>/<target>/<site>/<backup>.dump`:

- by the server itself, with `python3` (every Ubuntu cloud image has it, cloud-init needs it), using its own short-lived
  credentials from its instance role through EC2's metadata (IMDSv2): no key is kept on the server or in ABCP, and no
  credential is printed or put on a command line;
- signed with Signature Version 4, the dump's SHA-256 as its payload hash, then read back (`HEAD`) and its size compared;
- recorded on the backup as `offsite: {at, ok, detail}` (`detail`: the `s3://` address, or why it was not copied), and
  said in the step's detail ("; copied off the server" or "; kept on the server only: <why>").

A copy that fails never fails the backup or the operation: the backup stays on the server, and the next backup of the
same app tries up to three earlier ones not yet copied again. The bucket's own rule removes copies after 30 days; the
server keeps its 7 days as before.

## Servers bought for people (Repo C BF-14.4d d1)

Live since 15:17:23 UTC on 2026-10-25 (ABCP `aa6d10e`, recorded in #151); proved on a DigitalOcean server bought for a QA
app, its backup copied and read back with its size.

Repo C's design note `BF14_4_YOUR_OWN_CLOUD_ACCOUNT.md` §17.1 (the owner, 2026-10-25: "go ahead for BF-14.4"). A server
Autobuild bought for a person, at AWS, DigitalOcean or Hostinger, is created with `PUT /v1/targets/{key}`
`{"address": …, "backups_off_server": true}`; the target's record and its answers keep `backups_off_server`. A person's
own server never asks for it, and host 1 keeps `offsite.json` and its role. Each backup on such a target is also copied
to `s3://<bucket>/hosted/servers/<target>/<site>/<backup>.dump`:

- in the hosted backups' bucket, with the key of their settings file (`--hosted-offsite-file`, BF-10b): it may put and
  get under `hosted/` and nothing else, and the bucket keeps every copy 30 days, so nothing new is asked of the owner;
- ABCP signs an upload address for that one object (Signature Version 4 in the query, the host header alone signed, the
  payload unsigned, 15 minutes) and the server puts the backup to it itself with `python3`; ABCP then reads the copy's
  size back (`HEAD`, signed with the key) and compares it;
- no key reaches the server, the address can put that one object until it expires, and the backup does not pass through
  Autobuild's own server. A bought server never gets the instance role, which may read every backup in the bucket;
- recorded and said as above, and tried again as above.

The settings file is read afresh at each backup and used only while it is private (mode 0600) and valid.

## Also

`MaxTargets` goes from 20 to 200: with BF-14 offered to everyone, each person may add two servers of their own while
hosting is invite-only (Repo C decision 8).

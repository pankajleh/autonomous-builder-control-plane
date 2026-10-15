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

## Also

`MaxTargets` goes from 20 to 200: with BF-14 offered to everyone, each person may add two servers of their own while
hosting is invite-only (Repo C decision 8).

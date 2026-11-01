# Bought servers' backups kept in their own legal area (Repo C R2)

Status: **built, off until its settings file is given** (`--server-backups-file`). Repo C's design note
`HOSTING_REGIONS_AND_DATA_LAWS.md` §9, decision 3 A (the owner, 2026-10-31: "agree with Hosting regions and data laws"):
a server's backups are kept in a bucket in the same legal area and never pass through India. Until R2 every bought
server's copy went to the one hosted backups' bucket in `ap-south-1` (`remote-offsite.md`).

## The settings file

`abcp serve --server-backups-file <path>` names a private file (mode 0600, at most 32 KiB, strict JSON). It holds no
key: the hosted backups' key (`--hosted-offsite-file`) still signs every upload address. The example, valid and tested
(`TestTheExampleSettingsKeepEachRegionsBackupsInItsArea`), is `server-backups.example.json` beside this page, with the
AWS documentation's account id `123456789012` in place of Autobuild's:

```json
{
  "areas": {
    "IN": {"bucket": "autobuild-bf14-backups-<account>", "region": "ap-south-1", "prefix": "hosted/servers/"},
    "UK": {"bucket": "autobuild-bf14-backups-uk-<account>", "region": "eu-west-2", "prefix": "hosted/servers/"}
  },
  "regions": {
    "aws": {"ap-south-1": "IN", "eu-west-2": "UK", "eu-central-1": "EEA"},
    "digitalocean": {"blr1": "IN", "lon1": "UK", "fra1": "EEA"},
    "hostinger": {"mum2": "IN"}
  },
  "unplaced": "IN"
}
```

- **`areas`**: each legal area that has a bucket, with its bucket, the bucket's AWS region and the prefix copies go under.
  India's is the hosted backups' bucket and `hosted/servers/`, as before, so its servers' earlier copies stay where they
  are, readable with the same key.
- **`regions`**: every provider's region and its legal area, so ABCP needs nothing of Repo C's (whose
  `hosting-regions.json` the example follows). A region whose area has no bucket yet stays in the map.
- **`unplaced`**: the area of a bought server created before R2, which names no region. Every one was sold in India
  (R1 sold only `ap-south-1` and `blr1`), so `IN`. Without it such a server's backups stay on it.

The file is used only while every name is well formed and **each area's bucket is in an AWS region of that same area**
(`regions.aws[bucket region]` is the area); otherwise none of it is used. A prefix must be under `hosted/`, the only
prefix the key may write. The file is read afresh at each backup and at each new bought server, so adding an area needs
no restart; ABCP prints `abcp server backups areas=IN,UK` (or `none`) when it starts.

Why a new file, not more fields in `hosted-offsite.json`: that file holds the key and was written straight from AWS so
the key never passed through a chat or a log; this one holds no secret, changes each time an area opens, and can be
written and shown in full. Hosted apps on Autobuild's own server keep the bucket of `hosted-offsite.json` unchanged.

## A bought server names where it is

`PUT /v1/targets/{key}` with `backups_off_server: true` now needs `provider` and `region` (`"digitalocean"`, `"lon1"`),
kept on the target's record and answered with it. It is refused before anything is made:

- `409 backups_area_unready` when the settings file is missing or not valid, or names no legal area for that region, or
  no bucket for its area. Repo C buys the server first, so its purchase fails and its credits come back at once; Repo C
  sells a region only when its own setting says its area has a bucket, so this is the backstop.
- `400 invalid_request` for a bought server that names no provider or region, or a person's own server that names one.

## Its backups

Each backup on a bought server (`before_update`, `before_restore`, `nightly`, `manual`) is copied as before (one upload
address for one object, put by the server, read back with a signed `HEAD`), but **only** to its area's bucket:
`s3://<area's bucket>/<prefix><target>/<site>/<backup>.dump`, signed for that bucket's region. A bought server never uses
`offsite.json` (host 1's role). When its area has no bucket (the file changed, or is gone), nothing is asked of any
bucket and the backup records `offsite: {ok: false, detail}` with one of these words, said in the step as
"kept on the server only: …":

- `no bucket in its legal area (UK)`;
- `its region has no legal area`;
- `its region is not known` (bought before R2, no `unplaced`);
- `the backups' areas are not set` (no valid file).

Without the hosted backups' key nothing is tried and nothing is recorded, as before.

- **Listing:** the target's backups answer `offsite.detail` with the area bucket's `s3://` address, as before.
- **Restore, check and download** read the backup on the server, as before; a restore's first backup is copied to the
  area's bucket.
- **Pruning:** the server keeps 7 days, at most 14, as before; the copies are removed by each bucket's own 30-day rule
  (set by the owner's paste, as on India's bucket). ABCP deletes nothing in any bucket.
- **Requests:** as before, each backup copies itself and at most three earlier ones not yet copied, each one presigned
  `PUT` (made by the server) and one `HEAD` per file; a backup whose area has no bucket asks nothing.

## Tests

`internal/remotetarget/areas_linux_test.go`, with one fake S3 per bucket that checks every signature: a London server's
copy goes to the UK's bucket signed for `eu-west-2` and none to India's; with the UK's bucket gone nothing is asked and
each backup says why, then the next backup copies itself and three earlier ones (8 requests); an Indian server and one
bought before R2 keep India's bucket and place; a bought server is refused without the file, for a region whose area
has no bucket, for a region or provider in no area, and when it names nowhere, and an own server naming a region is
refused; the file is not used when others may read it, too large, or with a bucket in another area's region, a prefix
outside `hosted/`, an unknown or repeated field; the example file; a restore's first backup copied to the area; pruning
asks no bucket to delete. `offsite_linux_test.go` and `wpsites_linux_test.go` keep their bought-server copies (a
target bought before R2, `unplaced: IN`). `internal/serviceapi/targets_test.go`: the route's fields and
`backups_area_unready`.

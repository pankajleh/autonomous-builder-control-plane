# The Data screen's runtime where apps run (Repo C BF-06c)

Status: **merged, not live yet**. Repo C's design note `BF06_YOUR_APPS_DATA.md` §11 (the owner, 2026-10-24: "all as
recommended, go ahead", every §7 answer A). An app with administrators gets a read-only Data screen of its tables from the
Node kit's `autobuild-db/data` (Repo C P0740, `b0200c6`, #753), so the runtime that starts a Node app must carry it.

## What changes

**An app on a server.** `NodeRuntimeImage` (`internal/remotetarget/nodeapps.go`) names the runtime published from Repo C
`b0200c6`: `ghcr.io/pankajleh/autobuild-node-app-runtime@sha256:85179e3b84ace667ad4e0ceadee32ff70c782b0c7e6d287ec4ffe28cdd609b1d`.
It is the runtime of `ebd8e11` (the timetable, #128) with `autobuild-db/data` added and nothing else changed: the
launcher, the setup token and `AUTOBUILD_JOBS` are as they were. An app already on a server moves at its next start,
update or restore, as with every runtime before. `NodePostgresImage` is unchanged.

**Previews and share links** are the host's profiles, not this repository: the same restart moves `node-pg-v1`'s app to
the new runtime and adds `node-pg-hosted-v4` (`node-pg-hosted-v3` with the new runtime). v3 stays for the apps running on
it until their next update.

## Proof before anything moves

Repo C's workflow builds the image and runs the kit's example in it as a preview, a share link and a server run it: a
member and someone not signed in are refused the Data screen's records; in hosted mode the member who joined is made the
app's administrator and reads the tables, a search and no password hash. On the host (`a-bf06c-proof.py`, 2026-10-24
18:35 UTC) the same example in the published image beside its Postgres passed the same checks and logged no error.

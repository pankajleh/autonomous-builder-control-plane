# Remote sites: a static site on a remote target (BF-14.2a)

Status: **live since 2026-10-13 20:47 UTC** (#93, `15f994b`; ABCP restarted on it with no build running, recorded in #94).
No site exists yet: Repo C's side follows, then P0 on host 1. Repo C
design note `BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md` §11 (BF-14.2a, P0477), under the owner's approval of the design
("agree with 1,2,3") and "go with your recommendation options" (2026-10-13). Builds on `docs/plans/remote-targets.md`.

## What changes

All routes need `targets.control`, as the targets' do, and exist only with `--targets-dir`, whose folder now also keeps
`releases/`, `plans/` and `operations/` (each 0700).

- **`PUT /v1/releases/{sha256}`** (`Content-Type: application/x-tar`): a release, one app version's files as a tar
  named by its SHA-256, with `autobuild-release.json` (manifest v1: app, version commit, kind `static`, every other
  file's path, SHA-256 and size). The one request allowed more than a megabyte (160 MB) and a longer read (5 minutes).
  ABCP keeps it only if the digest matches, every entry is a regular file or a folder with a safe relative name (no
  `..`, no absolute name, no link, nothing a shell or a checksum line could read differently), the manifest lists
  exactly those files with their sizes and hashes, `index.html` is there, at most 2,000 files and 128 MB. The same
  release again is answered as it is (`200`; `201` the first time). At most 30 releases.
- **`POST /v1/targets/{key}/plans`** `{release, site: {name, host}}`: what applying would do, sealed by the SHA-256 of
  the plan's JSON without the hash: `send_files` (or `reuse_files` when the files are on the server already),
  `switch_site`, `write_site`, `start_frontdoor` (or `reload_frontdoor`), `check`; the release the site serves now
  (`replaces`); an expiry 30 minutes on. The host is a lowercase dotted name, not an address, and no other site on the
  target has it. Nothing on the server changes. A confirmed target only (`409 target_not_ready`).
- **`POST /v1/targets/{key}/apply`** `{plan_id, plan_hash, request_id}`: `202` and the operation. Refused with
  `409 plan_stale` when the plan expired, its hash differs, it was applied, or the site changed since; `409 target_busy`
  while another operation runs on the target. The same request answers the same operation.
- **`GET /v1/targets/{key}/operations/{id}`**: the operation, each step with its state, time and words. An operation
  a previous ABCP left running is `unknown` (and its site too): unknown until reconciled, never repeated blindly.
- **`GET /v1/targets/{key}/sites`** and **`POST /v1/targets/{key}/sites/{name}/remove`** `{request_id}`.

## On the server

One SSH connection per operation, signed in with the target's key against the pinned host key; each step a fixed
script through `sh -c`, every value in it (a digest, a site name, a count) checked against a narrow pattern first.
Everything lives in the login's `~/autobuild`:

- `releases/<digest>/`: the tar goes through the session's input into `tar -x` in a new folder (the manifest left
  out), then `sha256sum -c` against the manifest, no other file and nothing that is not a file, then renamed.
- `sites/<name>`: a link to its release, switched with one rename.
- `frontdoor/config/Caddyfile`: one block per site (`root * /srv/sites/<name>`, `file_server`), written by ABCP.
- The front door: `autobuild-frontdoor`, Caddy 2.11 pinned by digest (`FrontDoorImage`), one rootless container,
  read-only root, every capability dropped but binding ports 80 and 443, no new privileges, the sites and releases
  mounted read-only; it gets and renews its certificates itself. Started with the first site, reloaded after, stopped
  with the last.
- The check: the host must resolve to the target's address; then ABCP reads `https://<host>/` from that address,
  checking the certificate for the host, and compares it with the release's `index.html`, for up to 3 minutes while
  the certificate is issued.
- After a release replaces another, the release no site uses any more is removed from the server; removing a site
  removes its link, its block and the releases only it used.

## Tests

`internal/remotetarget/deploy_linux_test.go` runs every script with `sh` in a folder standing for the login's home,
with a fake `docker` that records what it was asked, and a fake front door that serves `index.html` from the link the
Caddyfile names: releases refused for a wrong digest, a link, a name climbing out or absolute, a file the manifest
leaves out, a wrong hash, a newline in a name, no `index.html` or another kind; a site sent, checked, switched, served
and replaced, the replaced release removed, the same release reused; a plan applied once, before it expires and only
as sealed, names and hosts with a shell in them refused; one operation at a time; a name that does not point at the
server failing the check and the site; removing the last site stopping the front door and leaving nothing; an
operation left running by a previous ABCP read as unknown. `internal/serviceapi/deployments_test.go`: routes, bodies,
the release's larger bound only on its own route, the grant, stable codes, `501` without the folder.

## Going live

Restart ABCP on the merged commit with no build running (same flags), and check `GET /v1/targets/{key}/sites` answers
for an unknown target `404` and a release with a wrong digest `400 release_invalid`. P0 runs on host 1 once the owner
launches it and Repo C's side is live.

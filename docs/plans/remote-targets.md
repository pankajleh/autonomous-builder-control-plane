# Remote targets: Linux servers ABCP reaches over SSH (BF-14.1)

Status: **live since 2026-10-13 19:22 UTC** (#90, `dc8b8b9`; grant #91; ABCP restarted on `5ab7575` with `--targets-dir`, recorded
in #92). Repo C design note `BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md` §5 and §11 (BF-14.1), approved by the owner
on 2026-10-13 ("agree with 1,2,3").

## Why

The owner put BF-14 first on 2026-10-13: apps on servers we don't run ourselves, starting with DeployFlix hosting on the
owner's AWS account, then customers' own servers. The owner approved that ABCP runs those deployments, as a remote-target
mode of its hosted instances, so the product never holds a server's credentials. The first step registers a server and
checks it, reading only.

## What changes

- **`abcp serve --targets-dir <folder>`** keeps the targets in a folder of the controller's user, mode 0700. Without it
  the routes answer `501`.
- **For a service principal granted `targets.control`:**
  - `GET /v1/targets` lists them (`TargetsV1`, oldest first);
  - `PUT /v1/targets/{key}` `{address, port?, user?}` registers one (`201`): the address must be a host name or address
    whose every resolved address is public (never loopback, private, link-local, shared, documentation, multicast or
    unspecified); the login is not `root` (default `autobuild`), the port 22 unless given; at most 20 targets. ABCP
    makes an Ed25519 key pair, keeps the private key in `<key>.key` (0600) and answers the public key line for the
    server's owner to add to the login's `authorized_keys`;
  - `POST /v1/targets/{key}/preflight`: on a new target it reads the server's host key and stops before signing in
    (state `fingerprint_pending`, the fingerprint answered); on a confirmed one it signs in with the target's key,
    accepts only the pinned host key, runs the fixed `PreflightScript` through `sh -s` and answers what it found;
  - `POST /v1/targets/{key}/confirm` `{fingerprint}` pins the host key whose fingerprint the owner checked on the server;
    another fingerprint answers `409 target_fingerprint_mismatch`;
  - `GET /v1/targets/{key}` and `DELETE /v1/targets/{key}` (which deletes the private key; `204`).
- **The preflight** reads the login's user id, the CPU architecture, the OS (`/etc/os-release`), cores, memory, free disk
  on `/`, Docker's version, whether it is rootless and its runtimes, `runsc` (gVisor), the listening ports, clock sync
  and whether GitHub's registry answers. Required checks: Ubuntu 24.04 or 26.04, or Debian 12 or 13 (by os-release's
  `ID` and `VERSION_ID`, Repo C BF-14.4c), x86-64, at least 1.8 GB of memory and 10 GB
  free, not root, Docker present and rootless, ports 80 and 443 free, clock in sync, the registry reachable. gVisor is
  reported but not required here; the product requires it on servers shared by different customers.
- **Outcomes** besides `checked`: `unreachable`, `timeout`, `refused_address` (the name now points somewhere not
  public; resolution is checked at every connection and the address dialled is the one checked), `auth_refused` (the
  key is not on the server), `host_key_changed` (nothing signs in or runs) and `failed`.

No request text becomes part of a command; the script is a constant. Nothing on a server is changed.

## Going live

1. In `abcp-config`, make the targets folder (mode 0700) and add `targets.control` to Repo C's principal in
   `grants.json` (keep the previous file beside it).
2. Restart ABCP with no build running, adding `--targets-dir …/targets`, and run the drift check.
3. Check `GET /v1/targets` answers an empty list, a private address answers `400 target_address_refused`, and without
   the grant `403`.

## Tests

`internal/remotetarget` against an SSH server in the test: the key pair and its files' modes; first contact reads the
host key without signing in or running anything; a wrong fingerprint refused, the right one pinned; the preflight runs
exactly `PreflightScript` and reads it; a server with another host key refused before signing in; a key not on the
server, nobody listening and a poor server reported; private, loopback, link-local, shared and unresolvable addresses
refused at registration and a name that moves inside refused at connection; bad keys, logins and ports; the limit; an
open folder refused. `internal/serviceapi`: the routes, bodies, codes, the grant and `501`.

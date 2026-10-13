# Remote Node apps: Node apps and the shared Postgres on a remote target (BF-14.2b-1)

Status: **live since 2026-10-13 22:01 UTC on `a4d0506`** (#95, recorded in #96); no Node app on a target yet. Repo C's side follows. Repo C
design note `BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md` §11 (BF-14.2b, P0480), under the owner's approval of the design
("agree with 1,2,3") and "go with your recommendation options" (2026-10-13). Builds on `docs/plans/remote-sites.md`;
the kit's Postgres `server` mode is Repo C P0481 (#494).

## What changes

- **Releases of kind `node`** (`PUT /v1/releases/{sha256}`, unchanged otherwise): Node format 1's layout, so
  `server.js`, `package.json` and `public/index.html`; no `node_modules`; every file under `migrations/` named
  `NNNN_words.sql`. A static release is checked as before.
- **Plans for a Node release** (`POST /v1/targets/{key}/plans`, the same body) also name the images by digest
  (`NodeRuntimeImage`, `NodePostgresImage`), the app's database (`app_<site>`, hyphens as underscores) and the sandbox
  (`gvisor` when the target's last check found it, else `standard`). Their steps: `send_files` (or `reuse_files`),
  `get_images`, `start_database`, `make_database`, `back_up` (only when the plan replaces a release), `switch_site`,
  `start_app`, `wait_health`, `write_site`, `start_frontdoor` (or `reload_frontdoor`), `check`. A site keeps its kind:
  a static site becomes a Node app only by removing it first. Static plans are unchanged, byte for byte.
- **Sites** say `kind`, `database` and `sandbox` for a Node app.
- **`GET /v1/targets/{key}/sites/{name}/backups`**: the app's database and its backups (time, reason `before_update`
  or `manual`, size, the release it was taken from), kept after the app is removed.
- **`POST /v1/targets/{key}/sites/{name}/backups`** `{request_id}`: a backup now, as an operation (kind `backup`); the
  same request answers the same operation.
- Removing a Node app stops it first (`stop_app`) and keeps its database and backups.

## On the server

The same one SSH connection per operation and fixed scripts, every value checked first. Besides BF-14.2a's folders:

- **The shared Postgres:** `autobuild-postgres`, the kit's image by digest started as `autobuild-postgres server`
  (SCRAM passwords, each login only to the database of its own name, `postgres` only on the local socket), on an
  internal network of its own (`autobuild-db`), its data in the volume `autobuild-pgdata`, 512 MB, restarted unless
  stopped, never published. Started by the first Node app's operation; when it is started again it joins every app's
  network again.
- **Per app:** an internal network `autobuild-net-<site>` (labelled `autobuild.site`) that Postgres (as
  `autobuild-postgres`) and the front door join; a login and database `app_<site>`, `CONNECT` revoked from everyone
  else, made with a 48-hex-digit password from `/dev/urandom` that goes to `psql` on its input and into
  `~/autobuild/apps/<site>/database.env` (0600) as `DATABASE_URL`, and nowhere else: ABCP never sees it. An update
  keeps it.
- **The app:** `autobuild-app-<site>`, the runtime image by digest, the release's folder read-only at `/source`, user
  65534, read-only root, every capability dropped, no new privileges, 256 MB, 256 processes, memory-backed `/scratch`,
  `DATABASE_URL` from `database.env`, restarted unless stopped, and `--runtime=runsc` under gVisor.
- **`wait_health`** asks the app for `/health` inside its container for up to 2 minutes (migrations run first). When
  it never answers, ABCP switches the site's link back to the release the plan replaced and starts it again, or stops
  the new app when there was none; the step fails saying which. Data is never restored on its own.
- **The front door** proxies the app's host to `autobuild-app-<site>:8080`, and joins each app's network whenever it is
  started or reloaded. **The check** asks `https://<host>/health` from the target's address for an answer with status
  200.
- **Backups:** `pg_dump -Fc` of the app's database through the local socket into
  `~/autobuild/backups/<site>/<id>.dump` (0600), before each update that replaces a release and when asked. Kept 7 days
  and at most 14, always the newest; the rest are deleted from the server.
- **Removing an app** removes its container and network and its block; the Postgres, the app's database,
  `database.env` and backups stay, so putting it back under the same name finds its data.

## Tests

`internal/remotetarget/nodeapps_linux_test.go`, with the fake `docker` of `deploy_linux_test.go` now keeping
containers, networks, volumes, images and the Postgres's roles and databases as files: Node releases refused without
`server.js`, `package.json` or `public/index.html`, with packages, or with a misnamed migration; a Node app's plan,
images, Postgres, network, login and app container, the password only in `database.env` and `psql`'s input (no
command line and no script ABCP sent holds it), the front door's proxy block, a backup before the second version and
the same password after it, a static site beside it; a version that never answers switched back (and a first version
stopped) without touching data; removing keeps the database and backups, a backup on request, putting it back finds
its login; gVisor when the server has it; backups kept 7 days and at most 14. `internal/serviceapi/deployments_test.go`
gains the backups routes.

## Going live

Restart ABCP on the merged commit with no build running (same flags), and check that `GET /v1/targets/{key}/sites/x/backups`
answers `404` for an unknown target and a Node release missing `server.js` is refused `400 release_invalid`. P1, P1b
and P2 on the server run on host 1 once it is enrolled and P0 has passed. Restore, the restore check and nightly
backups are `docs/plans/remote-restore.md` (#97).

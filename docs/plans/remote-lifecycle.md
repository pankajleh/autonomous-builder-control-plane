# Remote lifecycle: stopping and starting a Node app, removing its data, cleaning up a target, checking for drift (BF-14.5a)

Status: **live since 2026-10-13 23:51 UTC on `b5a5a5e`** (#99, recorded in #100); no target is enrolled yet. Repo C's side follows. Repo C design note
`BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md` §11 (BF-14.5a, P0486), under the owner's "go with your recommendation options"
(2026-10-13). Builds on `docs/plans/remote-sites.md`, `remote-node-apps.md` and `remote-restore.md`.

## What changes

- **Stop and start** (`POST /v1/targets/{key}/sites/{name}/stop` and `…/start`, `{request_id}`, operations `stop` and
  `start`), Node apps only. Stop: `stop_app`, `write_site` (the host's block becomes `respond "This app is paused." 503`),
  `reload_frontdoor`; the site's state is `stopped` (new), its data, network and release stay. Start: `start_app`,
  `wait_health`, `write_site`, `reload_frontdoor`, `check`; an app that never answers is stopped again and the site stays
  `stopped`. Stopping a stopped app is `409 site_stopped`, starting a running one `400`. A new version for a stopped app
  starts it. A stopped app is left out of the nightly backups, and a restore plan for it is `409 site_stopped`.
- **Remove an app's data** (`GET /v1/targets/{key}/databases`: each app's database, when it was made, its backups and
  whether its site is on the target; `POST /v1/targets/{key}/databases/{name}/remove` `{request_id, confirm}`, operation
  `remove_data`): only when `confirm` is the database's name and the site is off the target (`409 data_in_use`). Steps:
  `start_database`, `drop_database` (the database and any copy a restore or check left, with `WITH (FORCE)`, its login
  and `database.env`), `remove_backups`. The record forgets the database; putting the app back makes a new one.
- **Clean up a target** (`POST /v1/targets/{key}/clean` `{request_id, confirm}`, operation `clean`): only when `confirm`
  is the target's key and no site or app data is left (`409 target_in_use`). Steps: `stop_frontdoor`, `stop_database`,
  `remove_volume`, `remove_networks` (`autobuild-db` and the networks labelled `autobuild.site`), `remove_images` (the
  three pinned images), `remove_files` (`~/autobuild`, refused unless the login's home is a folder of its own).
- **Check for drift** (`POST /v1/targets/{key}/drift` `{request_id}`, operation `drift`, step `read_server`;
  `GET /v1/targets/{key}/drift`: the last report, `checked: false` before the first). One read-only script lists the
  `autobuild-*` containers (and their state), networks and volumes, the site links, release folders, backup files, the
  app databases and any `check_`, `restore_` or `previous_` copy, and the Caddyfile's hash. Findings: `container_missing`,
  `container_stopped`, `container_unknown`, `network_missing`, `network_unknown`, `volume_missing`, `volume_unknown`,
  `link_missing`, `link_wrong`, `link_unknown`, `release_missing`, `release_unknown`, `database_missing`,
  `database_unknown`, `copy_left`, `backup_missing`, `backup_unknown`, `frontdoor_config` and `site_unknown`, at most 100.
  Nothing is repaired, except that a site left `unknown` by an interrupted operation becomes `live` with the release its
  link points at when ABCP keeps that release and, for a Node app, its container runs (`reconciled`).
- **Nightly:** every confirmed target with a site gets its night (not only those with a Node app), ending with
  `read_server`, so each report is at most a day old in the same SSH session.
- New errors: `409 site_stopped`, `409 data_in_use`, `409 target_in_use`.

## Tests

`internal/remotetarget/lifecycle_linux_test.go`, the fake `docker` now listing containers, networks and volumes and
removing volumes and images: a stop that pauses the host and keeps the data, network and release, the same request one
stop, a stopped app refused a second stop and a restore and skipped by the night (which still checks the server), a
start that puts the proxy back, a start that never answers stopped again, a static site refused, a new version starting
a stopped app; data refused while the site is on the target or under another name, then dropped with its login,
`database.env` and backups, and a new empty database when the app comes back; a clean-up refused with a site, with data
and under another key, then removing the front door, Postgres, volume, networks, images and `~/autobuild`; a drift check
that finds nothing as deployed, then each kind of difference after the server is changed by hand without repairing any,
and an unknown site reconciled while an unknown Node app without its container stays unknown. The data removal and the
drift read's database list were run once against a real Postgres (16). `internal/serviceapi/deployments_test.go` gains
the routes and the three errors.

## Going live

Restart ABCP on the merged commit with no build running (same flags), and check the new routes answer `404` for an
unknown target. Repo C's side (Stop, Start, Data kept, Check the server, Clean up) follows.

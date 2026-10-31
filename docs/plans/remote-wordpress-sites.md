# Remote WordPress websites: WordPress websites and the shared MariaDB on a remote target (W5b)

Status: **in review, not live.** Repo C design note `WORDPRESS_WEBSITES.md` §13 (W5b), designed on 2026-10-31 under
the owner's decisions 2 A, 3 A and 5 A (§9). The images and their contract are Repo C's W5a (`tools/wp-site-kit/SERVER.md`
and `ci/server-helpers.sh` at Repo C `168dbff`, #918, whose `wp-site-kit.yml` runs both images exactly as the scripts
here do and published them from main). Builds on `docs/plans/remote-node-apps.md`, `remote-restore.md`,
`remote-offsite.md`, `remote-setup.md`, `remote-lifecycle.md` and `remote-names.md`: a WordPress website is a third
release kind, `wp`, and goes on a target as a Node app does. Repo C's side (W5c) follows.

## What changes

- **Releases of kind `wp`** (`PUT /v1/releases/{sha256}`, unchanged otherwise): Repo C's WordPress website format
  (`tools/wp-site-kit/WP_SITE_FORMAT.md`), so `site/site.json`, `site/pages.json`, `theme/style.css` and `README.md`,
  and no file PHP could run (`.php`, `.phtml`, `.phar`, any case) outside `theme/`, where the server runs only the
  starting theme's own PHP whatever the release holds. A `wp` release folder keeps `autobuild-release.json` (checked on
  the server like every other file, its SHA-256 kept with the release), so the site marks its pages with the release's
  version; static sites and Node apps leave it out as before.
- **Plans for a `wp` release** (`POST /v1/targets/{key}/plans`) take **`admin_email`**, the owner's sign-in address:
  required for `wp`, refused for any other kind and for a restore, an address PHP's `FILTER_VALIDATE_EMAIL` takes, kept
  in lower case. The plan names the images by digest (`WordPressServerImage`, `WordPressMariaDBImage`), the site's
  database (`wp_<site>`, hyphens as underscores), the sandbox and **`admin_email`**: a site keeps the address its data
  was made with, whose administrator it is, so a later plan shows that one whatever it was asked. Its steps are a Node
  app's: `send_files` (or `reuse_files`), `get_images`, `start_database`, `make_database`, `back_up` (only when the plan
  replaces a release), `switch_site`, `start_app`, `wait_health`, `write_site`, `start_frontdoor` (or
  `reload_frontdoor`), `check`. A site keeps its kind, and a name whose kept data is another kind's is refused until
  that data is removed. Conversions and starting on a backup stay Node apps' alone.
- **Sites** say `kind: "wp"`, `database` and `sandbox`. **Backups** of a `wp` site say `uploads_bytes` beside `bytes`
  (the dump).
- **`POST /v1/targets/{key}/sites/{name}/admin-password`** (empty body): `SiteAdminPasswordV1` (`target`, `site`,
  `host`, `user`, `email`, `password`, `taken`). It runs `docker exec autobuild-app-<site> admin-password take` over SSH
  and answers the password once; every call after answers `taken: true` and no password. A POST because taking it deletes
  it on the site; answered `Cache-Control: no-store`, and never logged or kept by ABCP (no record, operation or step
  holds it). `user` is the address, or `admin` past WordPress's 60 characters; WordPress signs in by `email` either way,
  at `https://<host>/wp-admin/`.
- **`POST /v1/targets/{key}/sites/{name}/reset-link`** (empty body): `SiteResetLinkV1` (`email`, `sent`). It runs
  `reset-link <address>`, the address on the script's input: WordPress's own reset email, sent the way the site sends its
  mail (the relay). A site that could not send it is `502 not_sent` (new). Both routes: a static site or a Node app
  `400`, a site not on the target `404`, a stopped site `409 site_stopped`.
- Restore plans, checks, nightly backups, off-server copies, downloads, stop and start, removing a site, removing its
  data, cleaning a target and the drift check all take `wp` sites (below). `GET …/backups/{backup}/spreadsheets` is
  `400` for a `wp` backup (MariaDB's SQL is not Postgres's).

## On the server

The same one SSH connection per operation and fixed scripts, every value checked first; the site's address, its
administrator's and every password reach the server on a script's input or are made there. Following SERVER.md:

- **The shared MariaDB:** `autobuild-mariadb`, the kit's image by digest started as `autobuild-mariadb server`, on
  `autobuild-db` (shared with the Postgres), its data in the volume `autobuild-mariadb-data`, 512 MB, restarted unless
  stopped, never published. Started by the first website's operation; started again, it joins every **WordPress
  website's** network again and no Node app's (SERVER.md's loop takes every site's network).
- **Per site:** the internal network `autobuild-net-<site>` that the MariaDB (as `autobuild-mariadb`) and the front door
  join; a database and login `wp_<site>` made by `create-site` with a 48-hex-digit password from `/dev/urandom` that goes
  to it on its input and into `~/autobuild/apps/<site>/database.env` (0600), and nowhere else. A kept `database.env`
  gives its password again, so a new database server takes the one the site has (`kept`).
- **The site:** `autobuild-app-<site>`, the server image by digest, the release folder read-only at `/source`, the
  volume `autobuild-wp-<site>` at `/data`, user 33, read-only root, every capability dropped, no new privileges, 512 MB,
  memory-backed `/scratch`, `database.env` and `site.env` (`SITE_URL=https://<host>`, `ADMIN_EMAIL=<address>`, 0600,
  written before every start since the host may change), `AUTOBUILD_RELAY` while relays are on, and `--runtime=runsc`
  under gVisor. The first start installs WordPress with the administrator; `wait_health` waits up to 180 seconds (half as
  long again as a Node app) for `/health`; a new version that never answers is switched back, data untouched.
- **The front door:** `<host> { reverse_proxy autobuild-app-<site>:8080 … }` with a Node app's encoding and headers and
  no `Origin` rewrite (SERVER.md asks for no header); a stopped website answers `respond "This website is paused." 503`.
  Node apps' and static sites' blocks are byte for byte as before. With a relay credential, the inside listener passes a
  `POST /__autobuild/contact` from the website's network (matcher `@contact_<site>` in `inside/<site>.caddy`, its
  network's range) to the relay host with `X-Autobuild-Site`, `X-Autobuild-Host` and `X-Autobuild-Client`, its body
  bounded at 16 KB, and **without** `X-Autobuild-Caller`, so the relay takes it as a visitor's message, whose `Origin`
  (`https://<host>`, which the site sends) it already checks.
- **Backups:** `autobuild-mariadb dump wp_<site>` into `~/autobuild/backups/<site>/<id>.dump` and `files backup`, in a
  container of its own on the site's volume (`--network none`, read-only root, user 33), into `<id>.uploads.tar`, both
  0600. The rows are counted (`counts`) in `check_wp_<site>` restored from the dump and then dropped, so they are the
  rows the dump holds (MariaDB's counts cannot share the dump's snapshot). Before each update, before each restore,
  nightly and on request, kept 7 days and at most 14 with the newest, exactly as a Node app's.
- **Checks and restores:** a check restores the dump into `check_wp_<site>` (once the uploads' tar lists `uploads/`),
  compares every table's rows and drops it. A restore plan's steps: `back_up`, `restore_copy` (into `restore_wp_<site>`),
  `stop_app`, `switch_database`, `restore_files`, `start_app`, `wait_health`, `check`, `clean_up`. The switch is one
  `RENAME TABLE` built from `information_schema` (all at once or not at all): the site's tables into `previous_wp_<site>`,
  the copy's into `wp_<site>`; a copy with no table makes the statement NULL, so nothing moves. `restore_files` is
  `files restore` in a container of its own on the volume. When the site never answers, its tables are swapped back and
  the uploads of the backup the restore took first are put back, then it is started again. No copy can be another site's:
  none of `check_`, `restore_`, `previous_` starts with `wp_`, and only `wp_<site>`'s login has a grant.
- **Off the server:** both files, `s3://…/<target>/<site>/<id>.dump` and `<id>.uploads.tar` (or under
  `hosted/servers/` for a bought server), the backup's `offsite` saying both addresses, or why not.
- **Download** (`GET …/backups/{backup}`) answers one **tar** (ustar, `application/octet-stream` as before) of
  `database.sql` and `uploads.tar`, each as it is on the server: both sizes are checked first, so its `Content-Length`
  is known before anything is read, and each file streams into it as it comes. A tar rather than a zip because its size
  follows from the two sizes alone, as the download's answer needs.
- **Lifecycle:** stop and start as a Node app; taking a website off removes its container and network and keeps its
  database, volume, env files and backups; removing its data (`confirm: "wp_<site>"`) runs `start_database`,
  `drop_database` (the database, every copy, the login, `database.env`, `site.env`), `remove_volume` and
  `remove_backups`. Cleaning a target also removes the MariaDB, its volume and both images. The drift check lists the
  MariaDB's `wp_` databases and copies and expects each website's container, network and volume (the volume while its
  data is kept).

## Tests

`internal/remotetarget/wpsites_linux_test.go`, with the fake `docker` now keeping the MariaDB's databases and logins,
each website's volume as a folder whose uploads the kit's `files` program tars and puts back for real, and a first
start that installs WordPress with its password on the volume: releases refused without each required file, with PHP
outside `theme/` or as another kind; a website's plan, images, MariaDB, network, login, volume and container exactly as
SERVER.md runs them, the password only in `database.env` and `create-site`'s input, `site.env`, the manifest at
`/source`, the front door's block, a backup of both parts before the second version with the same password and
administrator; an address required, checked and refused for other kinds; a version that never answers switched back
with its data untouched; the administrator's password read once, held by nothing ABCP keeps or ran, a reset link sent
to the address on the script's input and `not_sent`; a backup's rows, a check and a check that loses rows, a restore
that swaps the tables in one step and puts the uploads back, and one that never answers put back as it was; the night,
copies of both files off the server, a download of one tar of both; stop, start, the MariaDB started again joining
websites' networks alone, a drift check as recorded, taking it off, removing its data and a clean-up. `wpsites_test.go`:
the Node and static Caddyfile byte for byte against `testdata/caddyfile-node-static.golden` (written before this
change), the website's block and its inside entry, the contact matcher, addresses, the scripts against SERVER.md's
`docker run`s, the tar's size. `internal/serviceapi/deployments_test.go`: the POST, a GET refused, the reset link,
`not_sent`, `admin_email` passed on. The MariaDB scripts (make database, backup and its count copy, check and restore
copies, the swap and the swap back, the empty-copy guard, the drift list, removing data) and `files backup|restore`
were run once against a real MariaDB 10.11 with the kit's own `autobuild-mariadb`, through a `docker exec` stand-in.

## Going live

The images are private until the owner makes them public: until then `get_images` cannot pull them. Restart ABCP on the
merged commit with no build running (same flags), and check that `POST /v1/targets/{key}/sites/x/admin-password`
answers `404` for an unknown target and a `wp` release missing `site/site.json` is refused `400 release_invalid`. Repo
C's side (W5c: `wp-site` on Put it online, the live card, the relay taking a website's contact messages) follows.

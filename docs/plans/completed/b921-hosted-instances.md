# B9.2.1: hosted instances in the governed preview runtime

Date: 2026-09-30

Exact base: `fa8e02a` (PR #62)

Approved by the owner on 2026-09-30, all five decisions as recommended: Repo C's design note
`docs/product/design-notes/B9_2_HOSTING_APPS_WITH_A_SERVER.md` (answer recorded in Repo C P0166). This is its first
step, B9.2.1: the hosted mode in ABCP. Repo C's product side (B9.2.2) and the live acceptance (B9.2.3) follow.

## Why

A web app with accounts (Repo C's `node-app`, profile `node-pg-v1`) runs only as a preview: an hour at most, its
Postgres data in memory. To share such an app, it has to run all the time and keep its data. The owner chose to do that
inside ABCP's preview runtime, so hosted apps keep exactly the preview's isolation rules.

## What a hosted instance is

A hosted instance is one app, named by a **hosting key** chosen by the product (Repo C uses its app's identifier). At
any time the key runs at most one version: one run's checkpoint, on one hosted profile.

It runs on the **same runtime as a preview**, with the same checks on every health read:

- internal network, no internet;
- all capabilities dropped, no new privileges, a read-only root and a read-only source;
- CPU, memory, process and file limits from the profile;
- the controller's own loopback presentation proxy, the only way in.

It differs in four things:

1. **No time limit.** It runs until it is stopped.
2. **A named data volume.** A service marked `data_volume` gets the key's own Docker volume at `/data`, read-write.
   The volume is labelled with this controller's namespace and the key's digest, survives restarts and stops, and is
   removed only by an explicit purge. It is the only writable mount besides `/scratch`, and every health read checks
   that it is exactly that volume.
3. **Restarted by the controller.** After an ABCP restart (which removes every runtime object, as today), every key
   whose desired state is running is started again, on a new generation, with the same volume. A health failure that
   lasts 2 minutes restarts it too, at most 3 times in an hour; after that it is `FAILED` until started again.
4. **Counted separately.** Hosted instances do not use preview slots. At most 5 keys may be running at once (the
   owner's pilot limit).

## Profiles

`PreviewProfileV1` gains, all omitted when unset so existing profiles keep their digests:

- `hosted` (bool): the profile is for hosted instances only. Preview requests refuse it, and hosted requests refuse
  any other profile.
- per service, `data_volume` (bool), `backup_argv` and `restore_argv` (argv, checked like `start_argv`). Only a hosted
  profile may set them. At most one service has a data volume, and only that service may have backup and restore
  commands, both or neither. The backup command writes the dump to standard output; the restore command reads it from
  standard input.

The isolation probe of a hosted profile also mounts a throwaway volume, proves that the service's user can write to
`/data`, and removes that volume.

## The API

A new authority, `hosting.control`, for the service principal (with a delegated actor on every change, as for previews).
All routes are under `/v1/hosted/{key}`:

| Route | What it does |
|---|---|
| `GET /v1/hosted/{key}` | The key's state (`HostedV1`) |
| `POST …/start` | Starts a version (run, checkpoint, profile). Refused while the key runs; stop it first |
| `POST …/stop` | Stops it. The data volume and backups stay |
| `GET …/route` | The loopback target of its presentation proxy, like a preview route |
| `POST …/backups` | Takes a backup now (reason `manual` or `pre-update`); the key must be ready |
| `GET …/backups` | The key's backups |
| `POST …/restore` | Restores one backup into the running instance |
| `POST …/purge` | Removes the data volume and every backup; the key must be stopped |

Every change carries a request ID and is idempotent by it. A repeated request with a different body is a conflict.

**Backups** are written by the profile's backup command, run by the controller in the data service, to
`<service-root>/previews/hosted/<key digest>/backups/`, private to the controller. Every ready key gets a `nightly`
backup once a day. Backups older than 7 days are removed, except the newest.

**Update** (a new version with the data kept) is the product's sequence of these primitives: a `pre-update` backup,
stop, start the new version, and if it does not become ready, stop it, start the previous version and restore the
backup.

## State

One private state file per key, `<service-root>/previews/hosted/<key digest>/state.json`, written whole and synced
before it is renamed into place. It holds the key, the desired state, the current generation and version, the status
and health, the restart times, the backups and the last 64 request receipts.

## Out of scope

Off-host backups, more than one instance per key, gVisor (the pilot is for administrators' apps only, decided in Repo
C), and starting ABCP after a host reboot.

## Acceptance

```text
go test ./internal/preview ./internal/serviceapi ./cmd/abcp
go test -race ./internal/preview
go vet ./...
```

Tests cover: profile validation of the new fields; docker arguments and inspection checks for the data volume;
start, ready, stop, restart after a controller restart, health-failure restart and its limit; the capacity limit;
idempotent requests; backup, list, restore, nightly backups and pruning; purge only when stopped; and the API's
authority, validation and error mapping.

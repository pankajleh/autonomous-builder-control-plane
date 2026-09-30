# A4 general access: a gVisor runtime for preview profiles

Date: 2026-09-30

Exact base: `fc550be` (PR #63)

Repo C's design note `docs/product/design-notes/A4_NODE_POSTGRES.md`, decision 4, approved by the owner on 2026-09-29:
standard containers for the administrator pilot, and **gVisor before general access**. On 2026-09-30 the owner installed
gVisor on the live host (`runsc`, registered with Docker as the `runsc` runtime). This change lets a preview profile run
its services under it.

## What changes

- **A profile may name a runtime.** `runtime: "runsc"` is the only accepted value; empty is Docker's default runtime.
  The field is omitted when empty, so every existing profile keeps its digest. A profile that names it is part of the
  profile's digest, so switching a profile to gVisor is a new profile version (a hosted key pinned to the old digest
  must be started again, as for any profile change).
- **Every service of such a profile runs with `--runtime=runsc`.**
- **Services find each other by hosts entries.** gVisor has its own network stack, which does not reach Docker's
  embedded DNS (127.0.0.11). So the services that are not presented start first, and the presented one last, with a
  `--add-host name:ip` entry for each service already running (for Repo C's node profiles: the app gets `db:<its IP>`).
  Profiles without a runtime keep their order and get no entries.
- **Every inspection checks the runtime and the entries.** A profile with a runtime must run on exactly that runtime;
  one without must run on Docker's default. Hosts entries exist only under gVisor, and each must name another service of
  the profile at a private IPv4 address, once. Anything else fails the isolation check, as any drift does.
- **A gVisor profile is proved only where Docker has the runtime** (`docker info` lists `runsc`). Otherwise the probe
  starts nothing and the profile stays unavailable.

## Checked on the live host before this change

The node-kit example and its hosted Postgres, run with exactly this runtime's container arguments and `--runtime=runsc`
(`a-runsc-trial.sh`, pinned images, cleaned up afterwards):
- healthy in 13 s; both services report the `4.19.0-gvisor` kernel;
- the data in the named volume at `/data/pgdata`, the sample data loaded;
- the probe's `/proc/net/route` readable, with no default route;
- no internet from the app;
- Docker's embedded DNS refused inside gVisor, while the database's IP was reachable, which is why the hosts entries are
  needed.

## Tests

`internal/preview/gvisor_linux_test.go`:
- a gVisor profile starts the database first, the app gets `--add-host db:<ip>`, both run `--runtime=runsc`, and the
  started preview passes its isolation inspection;
- a profile without a runtime keeps its order and gets neither argument;
- the inspection refuses the wrong runtime, an entry for an unknown name, for itself, to loopback, to a public address,
  or twice, and any entry on a profile without a runtime;
- the probe proves a gVisor profile only where Docker lists `runsc`, and starts nothing otherwise;
- `runtime` accepts only `runsc`, and an empty one leaves the profile's JSON and digest unchanged.

The Docker fixture now reports each container's runtime and hosts entries from its create arguments, as Docker does.
Without the change in `docker.go`, three of the new tests fail.

## Rollout (Repo C, host)

Repo C's `node-pg-v1` and `node-pg-hosted-v1` get `"runtime": "runsc"`, validated with `LoadProfiles`, then an ABCP
restart announced on board #93. Then Repo C opens web apps with a server to everyone (A4 general access).

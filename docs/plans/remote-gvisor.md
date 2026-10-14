# Remote targets under gVisor (BF-14, decision 3)

Repo C's design note `BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md` §7 puts different customers' apps on our own servers only
under gVisor. ABCP already runs a Node app with `--runtime=runsc` when a target's last check found gVisor (#95). This plan
records what a rootless target needs for that to work, found on a throwaway server on 2026-10-14, and the two changes
ABCP makes for it.

## On the server (Repo C's operator script, not ABCP)

A rootless Docker cannot use runsc as installed:

- **cgroups:** runsc's systemd cgroup driver asks the system's systemd for a scope ("Interactive authentication
  required"); pointed at the login's own systemd it then writes to the root cgroup and is refused. Docker's `cgroupfs`
  driver is no way out: rootless, it falls back to no cgroups at all, for runc too. The server's runtime is a small
  wrapper: runsc with `--ignore-cgroups`, and `create` run inside a transient scope of the login's own systemd that carries
  the container's memory and pids limits (`MemoryMax`, `TasksMax`), so the whole sandbox stays within them. Measured: the
  scope holds 256 MB and 256 tasks, and a 400 MB allocation inside the app is stopped at the limit.
- **Names:** gVisor's own network stack cannot reach Docker's name service on a user-defined network (gVisor's FAQ), so
  an app cannot find `autobuild-postgres`. The runtime runs with gVisor's network passthrough (`--network=host`): the
  sandbox uses the network namespace Docker made for the container, so the app's internal network, its names and its
  isolation stay as they are (the instance metadata address and the internet remain unreachable from the app). The
  app's system calls still go to gVisor; only its sockets use the host kernel's network stack. Using gVisor's own stack
  would need fixed addresses on each app's network instead of names (a later hardening).
- **Memory as the app sees it:** gVisor shows the app the server's memory, not the container's limit.

## ABCP's changes

- **Ports held by our own front door are free.** A target's check reports `port_80_free` and `port_443_free` as passed
  with the value `front door` when ABCP's running `autobuild-frontdoor` holds them; anything else listening there still
  fails them. Without this, checking a target that already has sites made it look unready (found on a QA own server
  after its first app), and the product refuses a second app on a server that is not ready.
- **The app's heap stays under its 256 MB:** the app's start passes `NODE_OPTIONS=--max-old-space-size=160`, whichever
  sandbox runs it, so Node does not size its heap from the server's memory under gVisor.

Tests: `TestEvaluatePortsHeldByTheFrontDoor` (held by the front door: passed, `front door`; held by something else:
failed; free: passed, `yes`) and `TestStartAppCapsTheHeap`.

## Going live

Restart ABCP on the merged commit with no build and no server operation running (same flags). A target's next check
reports its ports this way; an app gets the heap cap at its next start (an update, a restore, a start after a pause).

**Live (#108).** ABCP runs `6564f4e` since 12:32 UTC on 2026-10-14. Host 1 runs gVisor since 12:35 UTC: its check passes
every item (`gvisor: yes`, ports `front door`), and a new plan there uses `sandbox: gvisor` (Team Kudos, a QA app). Apps
already running there keep runc until their next start from a plan.

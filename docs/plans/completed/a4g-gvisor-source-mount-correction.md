# A4 general access: gVisor source mount correction

Date: 2026-09-30

Exact base: `e3c079b` (PR #64)

## What went wrong

After PR #64, Repo C's `node-pg-v1` and `node-pg-hosted-v1` were switched to `"runtime": "runsc"` and the controller
restarted. Neither profile was proved: the probe's `docker create` failed for the app service, which mounts the checkout
at `/source`. Docker refused the mount itself:

```
docker: Error response from daemon: rro is not supported by runtime "runsc"
```

The source mount is recursively read-only (`bind-recursive=readonly`, with the `rprivate` propagation Docker 29 requires
for it, see `bp-02-docker29-bind-propagation-correction.md`). gVisor does not support recursive read-only binds. The
trial before PR #64 (`a-runsc-trial.sh`) had mounted the example's source without that option, so it did not catch this.
PR #64's fixture accepted any create arguments. As designed, the refused mount left the profiles unavailable, with
no retry using weaker options. Both profiles went back to the standard runtime and the controller was restarted.

## What changes

- **A gVisor profile's source mount is plain read-only:** `type=bind,src=<checkout>,dst=/source,readonly,bind-propagation=rprivate`.
  The recursive option exists to make mounts *inside* the bound directory read-only too. The checkout is a directory
  this controller creates, with nothing mounted inside it. Under gVisor, gVisor's own kernel serves the whole tree
  through that one read-only mount in any case.
- **Profiles without a runtime keep the recursive read-only mount exactly as before.** This is a choice made from the
  profile, not a retry after a refusal. A refused mount still leaves the profile unavailable.
- The inspection is unchanged: `/source` must be a bind of this preview's checkout, not writable (`RW` false).

## Checked on the live host

Pinned images under `--runtime=runsc`, cleaned up afterwards:
- with `readonly,bind-recursive=readonly,bind-propagation=rprivate`, `docker create` fails with the error above;
- with `readonly,bind-propagation=rprivate`, the container starts, `/source` is readable by the service's user, and a
  write to it is refused (`Permission denied`).

## Tests

- The Docker fixture refuses a create with `--runtime=runsc` and `bind-recursive=readonly`, as Docker does.
- `TestGVisorSourceMountIsReadOnlyWithoutTheRecursiveOptionGVisorRefuses`: the exact mount for a gVisor profile, and the
  unchanged recursive mount for a profile without a runtime.
- Without the change in `docker.go`, three tests fail: this one, and PR #64's start and probe tests for a gVisor
  profile, whose creates the fixture now refuses.

## Rollout (Repo C, host)

Build the controller from the merge commit, switch the two node profiles to `runsc` again, validate them with
`LoadProfiles`, announce the restart on board #93, restart, and check that both profiles are proved.

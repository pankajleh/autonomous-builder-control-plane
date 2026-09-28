# Local-integration operator configuration

This directory versions the non-secret ABCP operator configuration used by the live Repo C ↔ ABCP local-integration environment. Before 2026-09-28 it existed only on the integration host, with manual `.pre-*`/`.bak-*` copies as its only history.

The live copy remains what `abcp serve` reads, at `/home/devagent/local-integration/repo-c-abcp/runtime/abcp-config/`. Change it in two steps:

1. Change this directory in a PR.
2. After merge, copy the file to the host (mode `0600`, one hard link) and restart `abcp serve`, because profiles are read at startup.

Then run the drift check on the host:

```text
python3 tools/operator/check_local_integration_config.py [--live <dir>]
```

## Versioned files (`abcp-config/`)

| File | Purpose |
|---|---|
| `profiles.json` | Admission profiles: `local-p02` (demo product `example/product`) and `repo-c-development-v1` (Repo C) |
| `manifest-template.json` | Demo-product governed manifest template; pins the Ralphex binary, executor policy, worktree retention and acceptance |
| `manifest-template-development.json` | Repo C development manifest template |
| `preview-profiles.json` | BP-02 preview profiles: `demo-v1` (demo product) and `web-v1` (Repo C) |
| `grants.json` | Authority grants for the `repo-c-service` principal |

Both preview profiles are static busybox `httpd` servers. `/bin/httpd` is the busybox path (`/usr/sbin/httpd` is the Alpine path and exits 127). The health path is `/README.md` because busybox `httpd` has no directory index. `web-v1` serves Repo C sources as static files only; a real web/API preview of Repo C needs an approved offline image and its own profile.

## Secret files (never versioned)

`token`, `cursor.json`, `preview-session-signing.hex`, `preview-gateway-service.token` and `workflow.json`, which holds the workflow-authority database connection. The drift check verifies only that they exist with owner-only permissions; it never reads them.

## Pinned Ralphex governance binary

Source: the private repository [`pankajleh/ralphex-governance`](https://github.com/pankajleh/ralphex-governance). Each pin is a short ABCP patch series on top of an upstream `umputun/ralphex` **release tag**. Its branches are named `abcp/<upstream-release>`, and the default branch is the current pin.

| Binary | Upstream base | Source | SHA-256 |
|---|---|---|---|
| `ralphex-v1.7.0-abcp` (current) | release `v1.7.0` (`24c19b1`) | `abcp/v1.7.0` @ `2275e23adba99bb22c23679ae3e8152c0323aba1` | `fe5a7c4651aa73af755f0aed10a82d9652a763a000f72b0646eaba6ae4894058` |
| `ralphex-governance-v2` (previous) | master `319e306` (v1.6.1+34) | `abcp/governance-bundle-v2-20260928` @ `055dfbdb4d92120a8249ae58923d7608d583e82f` | `54300a18cb9a5ae5dc295f0f2b7f2e9441165e6b0aa56b4be52b18c225de94bc` |
| `ralphex-governance-v1` (earlier) | master `319e306` | `abcp/governance-bundle-v1-20260922` @ `66e8868173ffc982dbeab903663839d2276372c4` | `9698c1621fddf76038c38dec19c424c815b334d392d2211c74b556c1a8b48bcd` |

The patch series has two commits:

1. **ABCP governed execution budgets** (26 files, about +1,350/−220). It adds:
   - bounded iterations, session and idle timeouts, and internal review passes;
   - the validation spec;
   - session identity;
   - the orchestrator subprocess wait;
   - the `--abcp-governance-capability-v1` probe.
2. **`--keep-worktree`** (2 files, +55/−7). The finished run leaves its worktree for ABCP to remove, and the probe advertises `worktree_retention_v1`.

## Upgrading Ralphex

1. In the host clone `~/ralphex-behavior-lab/tools/ralphex`, run `git fetch origin --tags`. Create `abcp/<new-tag>` from the new release tag, then `git cherry-pick -x` the two commits of the current branch.
2. Resolve any conflicts. Then run `gofmt -l cmd pkg` (expect no output), `go vet ./...` and `go test ./...`.
3. Build with `cd cmd/ralphex && go build -trimpath -ldflags "-X main.revision=<commit> -s -w" -o ../../.bin/ralphex-<new-tag>-abcp .`, and check `--abcp-governance-capability-v1`.
4. Push the branch to `pankajleh/ralphex-governance` and make it the default branch.
5. Update the binary path, SHA-256 and source in both templates here and on the host (`runtime/pin-ralphex.py`). Restart `abcp serve` (`runtime/start-abcp.sh`) with no run executing, run one governed proof run, and record it.

If `--keep-worktree` is accepted upstream, drop commit 2 at the next upgrade. The capability-probe bit stays in commit 1, because the probe is ABCP's.

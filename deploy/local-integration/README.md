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

| Binary | Source | SHA-256 |
|---|---|---|
| `ralphex-governance-v2` (current) | `abcp/governance-bundle-v2-20260928` @ `055dfbdb4d92120a8249ae58923d7608d583e82f` (v1 + `--keep-worktree`) | `54300a18cb9a5ae5dc295f0f2b7f2e9441165e6b0aa56b4be52b18c225de94bc` |
| `ralphex-governance-v1` (previous) | `abcp/governance-bundle-v1-20260922` @ `66e8868173ffc982dbeab903663839d2276372c4` | `9698c1621fddf76038c38dec19c424c815b334d392d2211c74b556c1a8b48bcd` |

Both fork branches exist only in the host clone at `~/ralphex-behavior-lab/tools/ralphex`, which is based on `umputun/ralphex` `319e306`. They have no remote. Pushing them to a repository you control would remove the last host-only source dependency.

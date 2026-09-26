# Governed preview runtime

Preview presents an exact clean checkpoint from a product-admitted run. It does
not change run state, acceptance, deployment, or merge authority. Development
admissions and checkpoints with broken or ambiguous bindings are ineligible.
Ordinary provider warnings do not invalidate otherwise eligible checkpoints.

## Configuration and host requirements

Add `--preview-profile-file /absolute/path/preview-profiles.json` to `abcp serve`
alongside its required service-root, listener, token, principal, cursor-key, and
authority-grants options. The profile file must be a regular file owned by the
controller's effective user, with one hard link and no group/other permissions
(for example, mode 0600). Its absolute, clean path must have no symlink components.
The controller reads this configuration at startup; restart to change profiles.

The file envelope is `ProfileFileV1`, with 1–32 uniquely named profiles. Example
(replace the example repository and image digests before use):

```json
{
  "schema_version": 1,
  "profiles": [{
    "schema_version": 1,
    "profile_id": "web-v1",
    "repository_identity_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "services": [{
      "name": "web",
      "image": "local/web@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      "user": "1000:1000",
      "prepare_argv": [],
      "start_argv": ["/usr/bin/node", "/source/server.js"],
      "environment": {"NODE_ENV": "development", "TZ": "UTC"},
      "mount_source": true,
      "port": 8080,
      "presented": true
    }],
    "health_path": "/health",
    "health_timeout_seconds": 10,
    "ttl_seconds": 300,
    "cpu_quota": 10000,
    "memory_bytes": 67108864,
    "pids_limit": 32,
    "tmpfs_bytes": 8388608,
    "network_policy": "INTERNAL_ONLY"
  }]
}
```

`repository_identity_digest` is the lowercase SHA-256 of the admitted repository
identity's UTF-8 bytes, such as `owner/repo`, with no trailing newline. It must
match the immutable run registration. Image references must match a locally
installed image's `RepoDigests`; mutable tags alone are rejected. Images are
never pulled, and package installation must use approved offline inputs.

Validation limits:

| Field | Requirement |
| --- | --- |
| Profile ID | 1–128 ASCII letters/digits/dot/underscore/hyphen, starting with a letter or digit |
| Services | 1–4 unique names; exactly one `presented` service |
| Service name | Lowercase letter followed by up to 31 lowercase letters/digits/hyphens |
| User | Numeric nonzero UID:GID, each at most six digits |
| Port | 1024–65535 |
| TTL | 1–3600 seconds |
| Health timeout | 1–30 seconds, no greater than TTL |
| CPU quota | 1000–200000 microseconds per fixed 100000-microsecond period, per service |
| Memory | 16 MiB–2 GiB per service; swap disabled |
| PIDs | 1–256 per service |
| Scratch tmpfs | 1–256 MiB per service, at most half its memory limit |
| Health path | Single-leading-slash path, at most 256 bytes; no query, fragment, backslash, NUL, CR or LF |

Prepare/start arrays contain at most 32 arguments, 1024 bytes per argument and
8192 bytes in total. Start is required; prepare is optional. The executable must
be an absolute clean container path. Shells, BusyBox entrypoints, command wrappers,
inline `-c`/`-e`/`--eval`, shell metacharacters, and Docker socket references are
rejected. No request can override argv, images, environment, paths, or policy.

The only configurable environment keys are `NODE_ENV` (`development`, `production`,
`test`), `LANG`/`LC_ALL` (`C`, `C.UTF-8`), and `TZ` (`UTC`). Candidate commands run
with a cleared environment and fixed PATH, HOME and TMPDIR. Image environment
metadata is also checked: only these values plus recognized PATH/HOME defaults
are accepted. Images declaring volumes are rejected.

The supported runtime is Linux, with `/usr/bin/docker` and the local
`/var/run/docker.sock`, accessible to the controller. Docker must report memory,
swap, CPU quota, PID and seccomp support. Ambient Docker contexts, credentials,
and proxy settings are ignored. Every image must contain `/usr/bin/env`,
`/bin/sleep`, and `/bin/busybox` with `httpd`, `wget` and `cat` applets for the
fixed offline isolation probe, as well as its configured application executables.
The configured non-root user must be able to run these tools.

Each group uses a dedicated internal bridge with no published container ports,
a read-only root, dropped capabilities and no-new-privileges. `/scratch` is its
bounded, non-executable write area. Source, when requested, is mounted at
`/source` using `readonly,bind-recursive=readonly`; the daemon/kernel must support
these options. Startup probes exercise that same mount with a private temporary
source directory. Host-loopback presentation binds an explicit 127.0.0.1 address
and targets only the presented service; siblings remain internal.

Missing profile configuration or an unsupported host/profile leaves
`preview_runtime=false` at `GET /v1/extensions/pdlc-experience`. At least one
profile must pass the full isolation, blocked-egress, mount and loopback-access
probe before capability becomes true. Unreadable/invalid protected configuration
or inability to acquire namespace ownership fails service startup. A corrupt
preview journal keeps preview unavailable, preserves the damaged history, and
still permits namespace-owned runtime cleanup and the legacy service to start.
Source cleanup follows successful runtime cleanup so live containers never lose
their mounted checkout. No live Preview Serve claim follows from a fail-closed
result.

Run the optional host smoke using an already installed digest-pinned image with
the required probe tools:

```sh
ABCP_PREVIEW_DOCKER_SMOKE_IMAGE='local/probe@sha256:<actual-local-digest>' \
  go test ./internal/preview -run '^TestLocalDockerHostIsolation$' -count=1 -v
```

The smoke accepts either a proved host or a fail-closed unavailable host, and
checks cleanup. Read its log to distinguish these outcomes. The ordinary suite
uses deterministic Docker-command fixtures and does not require Docker or pull
images. Validation commands are `go test ./...`, `go test -race ./...` and
`go vet ./...`; unsupported platforms have compile-only/fail-closed coverage.

## HTTP API and receipts

All preview routes require the service's bearer authentication. POST additionally
requires a service principal whose protected authority grant has
`may_assert_delegated_actor: true`; the delegated subject must be `user` or
`operator`. Requests cannot use query parameters. POST JSON must be at most
8192 bytes and rejects unknown/duplicate fields. GET requests have no body.

Create: `POST /v1/runs/{runId}/previews`

```json
{
  "schema_version": 1,
  "request_id": "preview-create-1",
  "expected_run_id": "run-1",
  "checkpoint_activity_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "profile_id": "web-v1",
  "delegated_actor": {"subject_id": "alice", "subject_type": "user"}
}
```

Use the actual registered run ID and a clean checkpoint activity ID from its
activity API. Product authorization/task/version identities come from the
controller-created admission capsule, not the caller.

List: `GET /v1/runs/{runId}/previews` returns `PreviewListV1` with `run_id` and a
`previews` array ordered by revision. Detail:
`GET /v1/runs/{runId}/previews/{previewId}` returns the current `PreviewV1`.
Both return HTTP 200.

Stop: `POST /v1/runs/{runId}/previews/{previewId}/stop`

```json
{
  "schema_version": 1,
  "request_id": "preview-stop-1",
  "expected_run_id": "run-1",
  "expected_preview_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "delegated_actor": {"subject_id": "alice", "subject_type": "user"}
}
```

Replace the preview ID with the ID returned by create/list. Successful POSTs
return HTTP 202 and an immutable `PreviewV1` receipt. Create's receipt records
`REQUESTED`; current state advances through `VALIDATING`, `STARTING`, `READY`,
then `FAILED`, `EXPIRED` or `STOPPED`. A ready preview can have `DEGRADED` health.
The response includes its immutable source, profile, product, validation and
request identities, revision and timestamps. Only current READY state carries
an opaque server-only `route_handle`; this is not a browser URL or access grant.

Request IDs are scoped to the authenticated principal across both commands and
all runs. The same ID with the same canonical body/target returns the original
receipt, even if current state has changed. Reusing it with a different command,
body or target conflicts. Read detail/list for current state. After ambiguous
transport, read list/detail and match `request_id` before deciding to retry; an
exact replay retrieves the same receipt. A deliberate retry after terminal state
needs a new request ID and creates a new preview ID/revision, even for the same SHA.

| HTTP status | Error code / meaning |
| --- | --- |
| 400 | `invalid_request`: malformed or out-of-contract request |
| 401 | Authentication required/invalid |
| 403 | `authority_denied`: delegated actor authority missing |
| 404 | `not_found` or `unknown_preview_profile` |
| 409 | `preview_ineligible` or `request_id_conflict` |
| 503 | `NOT_AVAILABLE`: runtime, history or capacity unavailable |

History and receipts remain after terminal cleanup. The foundation bounds each
service store to 1000 preview identities, 100000 frames and 64 MiB of journal
storage, and at most eight concurrent preview workers. Do not delete history to
reset those limits or damaged receipts; preserve it for operator recovery.

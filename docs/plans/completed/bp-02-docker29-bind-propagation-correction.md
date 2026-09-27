# BP-02 Docker 29 bind-propagation compatibility correction

## Authority and problem

Base: `ad8d19ad1fcefb775a9c4ae6457bab772ccc96a9` (merged BP-02).

PX-07 live-preview preparation reproduced a concrete host compatibility failure on Docker 29.7.2:

`docker create --mount type=bind,...,readonly,bind-recursive=readonly`

is rejected with:

`option 'bind-recursive=readonly' requires 'bind-propagation=rprivate' to be specified in conjunction`.

This prevents the frozen BP-02 host/profile isolation probe from creating the preview container, so `preview_runtime` truthfully remains false even though the host otherwise reports the required Linux resource/security capabilities.

### Task 1: Apply Docker 29 mount compatibility correction

Implement only the minimum provider-neutral BP-02 correction needed for current Docker compatibility:

- [x] add explicit private bind propagation to the existing read-only source bind mount;
- [x] preserve recursive read-only semantics and every existing isolation/resource invariant;
- [x] do not weaken INTERNAL_ONLY networking, non-root execution, read-only rootfs, cap-drop, no-new-privileges, quotas, loopback-only presentation, route opacity, source provenance, or acceptance authority;
- [x] do not change preview API shape, run state, integration/merge semantics, or Repo C;
- [x] add deterministic regression coverage proving the exact mount options and failure-closed behavior;
- [x] update BP-02 documentation only where needed to record the Docker 29 requirement.

## Allowed paths

- `internal/preview/docker.go`
- focused `internal/preview/*_test.go`
- `internal/preview/README.md`
- this plan file only.

Any need outside those paths is a ROADBLOCK.

## Acceptance

Run from the exact candidate head:

1. `gofmt -w` only on changed Go files, then prove no formatting drift.
2. `go test ./internal/preview`
3. `go test -race ./internal/preview`
4. `go test ./internal/serviceapi ./cmd/abcp`
5. `go vet ./internal/preview ./internal/serviceapi ./cmd/abcp`
6. `git diff --check ad8d19ad1fcefb775a9c4ae6457bab772ccc96a9...HEAD`
7. opt-in host smoke with the already installed digest-pinned BusyBox image:
   `ABCP_PREVIEW_DOCKER_SMOKE_IMAGE=busybox@sha256:5cec3fc171c87218698e85a52af7087de727372aae264a787b8112901a5b0092 go test -v ./internal/preview -run TestLocalDockerHostIsolation -count=1`

The host smoke must report the isolation/loopback proof as true, or the correction is not sufficient for PX-07 and publication remains blocked.

## Resolved host-smoke blocker (2026-09-27)

The correction adds `bind-propagation=rprivate` without changing
other production behavior. The new regression checks fail before that change
and pass after it. Formatting, `go test ./internal/preview`,
`go test -race ./internal/preview`, `go test ./internal/serviceapi ./cmd/abcp`,
and the required `go vet` command pass on the working tree.

The initial Alpine host smoke exited successfully but reported `preview_runtime=false`,
so it did not satisfy acceptance. Temporary diagnostic tracing on Docker 29.7.2
confirmed successful container creation, read-only source inspection with
`Propagation: rprivate` and `ReadOnlyForceRecursive: true`, and source-file reads
as the configured non-root user. External HTTP remains blocked. However, the
specified Alpine digest lacks the required BusyBox `httpd` applet:
`/bin/busybox httpd --help` reports `httpd: applet not found` (exit status 127).
The container endpoint refuses connections and the loopback proxy returns 503.
Probe resources were cleaned up, and the temporary tracing was removed.

Resolution: the operator installed official BusyBox 1.37.0-musl and pinned the
resolved local RepoDigest `busybox@sha256:5cec3fc171c87218698e85a52af7087de727372aae264a787b8112901a5b0092`.
That image satisfies the already-frozen offline probe-tool contract (`httpd`,
`wget`, `cat`) without changing production probe behavior. With the Docker 29
mount correction present, the exact opt-in smoke reports `INTERNAL_ONLY egress
block and explicit host-loopback presentation proved`. The acceptance command
above is therefore re-bound to this exact locally installed digest; no mutable
tag is accepted by the runtime.

## Task 1 validation (2026-09-27)

All required Go tests and vet checks passed with the completed correction,
including the preview race tests and service/CLI tests. `gofmt -w` was limited
to the three changed Go files, and `gofmt -l` reported no formatting drift.
The BusyBox opt-in host smoke passed and reported
`INTERNAL_ONLY egress block and explicit host-loopback presentation proved`.
The production change is limited to the source mount argument; regression
coverage verifies its exact options, omission when source mounting is disabled,
cleanup after creation failure, rejection without weaker retries, and revocation
of prior probe approval. All changes remain within the allowed paths.

## Review

Independent exact-head review must report zero Critical and zero Major findings before PR publication.

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

- add explicit private bind propagation to the existing read-only source bind mount;
- preserve recursive read-only semantics and every existing isolation/resource invariant;
- do not weaken INTERNAL_ONLY networking, non-root execution, read-only rootfs, cap-drop, no-new-privileges, quotas, loopback-only presentation, route opacity, source provenance, or acceptance authority;
- do not change preview API shape, run state, integration/merge semantics, or Repo C;
- add deterministic regression coverage proving the exact mount options and failure-closed behavior;
- update BP-02 documentation only where needed to record the Docker 29 requirement.

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
7. opt-in host smoke with the already installed digest-pinned Alpine image:
   `ABCP_PREVIEW_DOCKER_SMOKE_IMAGE=alpine@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 go test -v ./internal/preview -run TestLocalDockerHostIsolation -count=1`

The host smoke must report the isolation/loopback proof as true, or the correction is not sufficient for PX-07 and publication remains blocked.

## Review

Independent exact-head review must report zero Critical and zero Major findings before PR publication.

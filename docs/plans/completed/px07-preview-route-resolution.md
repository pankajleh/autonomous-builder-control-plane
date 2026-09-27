# PX-07 prerequisite — preview route resolution

Base: `5cd8ee5d3cc60370f9e607e2fda267eec514eee2`

Purpose: close only the missing service-only presentation seam required by PX-07. Existing BP-02 preview authority, source eligibility, run state, acceptance, integration and merge semantics remain unchanged.

Frozen contract:
- add `GET /v1/runs/{runId}/previews/{previewId}/route`;
- response schema `PreviewRouteV1`;
- caller must be the authenticated service principal with existing `preview.control`;
- resolve only an owned, unexpired READY/DEGRADED preview with its exact current route handle;
- response contains exact run ID, preview ID, route handle, preview expiry, and one server-only loopback HTTP target;
- unknown/cross-run/cross-owner/pre-ready/terminal/expired/stale-handle/unroutable/non-loopback cases fail closed;
- no browser-facing API, no public sharing, no new preview lifecycle state.

Allowed paths:
- `internal/preview/service.go`
- `internal/preview/docker.go`
- focused `internal/preview/*_test.go`
- `internal/serviceapi/dto.go`
- `internal/serviceapi/server.go`
- focused `internal/serviceapi/*_test.go`
- `cmd/abcp/main.go` and `cmd/abcp/main_test.go` only if interface wiring requires it
- this plan file.

### Task 1: Implement bounded preview route resolution

- [x] Add exact `PreviewRouteV1` DTO validation/encoding.
- [x] Add preview-service route resolution that rechecks owner, run, lifecycle, expiry and current route handle.
- [x] Add runtime lookup from opaque route handle to the currently owned loopback presentation target without exposing container or filesystem internals.
- [x] Add the single authenticated service API route under the existing preview resource.
- [x] Preserve all existing create/list/detail/stop behavior and capability truth.
- [x] Add deterministic denial tests for cross-owner, cross-run, terminal, expired, stale handle, unavailable runtime and non-loopback target.
- [x] Add success test proving exact READY preview resolves only its current explicit loopback HTTP target.
- [x] Update this task checklist and commit the completed task.

Acceptance on exact head:

```text
go test ./internal/preview ./internal/serviceapi ./cmd/abcp
go test -race ./internal/preview ./internal/serviceapi ./cmd/abcp
go vet ./internal/preview ./internal/serviceapi ./cmd/abcp
go test ./...
go test -race ./...
go vet ./...
git diff --check 5cd8ee5d3cc60370f9e607e2fda267eec514eee2...HEAD
```

If implementation needs any path or semantic expansion beyond this plan, stop with ROADBLOCK rather than broadening scope.

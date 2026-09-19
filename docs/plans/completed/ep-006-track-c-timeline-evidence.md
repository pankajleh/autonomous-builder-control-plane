# EP-006 Track C — Timeline and Evidence

Exact predecessor authority: `0097effa0e94340980a9ab5483abb5359ebfc2a1` (Track B IMPLEMENTATION_CONVERGED, fresh deterministic acceptance PASS, fresh exact-head review 0C/0M/0m).
Accepted EP-006 design: `908710e1606b0da761e612c36b85c866e182c7f7`.
Accepted Track-A predecessor: `f082cae1677d002825b7f4176d9783e199c9a125`.

Track A and Track B are frozen. Track D is blocked. This plan authorizes Track C only.

## Maximum ownership

```text
internal/timeline/**
internal/evidence/read_linux.go
internal/evidence/read_linux_test.go
this plan and its completed-plan move
```

`internal/serviceapi/**`, `internal/runtimecatalog/**`, `internal/readmodel/**`, `internal/ledger/**`, `cmd/**`, action/recovery/run/scheduler/integration/GitHub-lifecycle paths are read-only.

### Task 1: Implement normalized timeline and evidence model

- [x] Implement `internal/timeline/**` against the frozen Track-A `TimelineReader` / `EvidenceReader` seams and accepted Track-B projection/snapshot contracts; do not mutate those contracts.
- [x] Produce deterministic ordered timeline entries from authoritative ledger order only; preserve historical attempts/tasks/sessions and never fabricate lifecycle transitions or authority.
- [x] Build the evidence index only from evidence refs present in the authoritative run snapshot. `evidence_id` must be opaque, deterministic, identifier-safe, and bind run ID + source event identity + evidence-ref ordinal + evidence-ref digest.
- [x] Evidence metadata may expose kind, digest, source event identity, safe byte size when known, and `downloadable`; raw ledger/evidence/controller paths or URIs must never appear in DTOs, cursors, errors, or IDs.
- [x] Relative, remote, abstract, missing-digest, malformed-digest, or otherwise non-downloadable refs remain metadata-only with `downloadable=false`.
- [x] Download only a digest-bearing canonical absolute local ref beneath the exact immutable registered evidence root, and only after a fresh authoritative snapshot proves the same run/event/ref binding.
- [x] Extend Linux `evidence.ReadVerifiedLocal` so the exact opened artifact descriptor before read, after read, and the reopened path descriptor each require `Nlink == 1`, while preserving existing no-symlink, regular-file, containment, physical-identity, replacement and digest checks.
- [x] Map symlink/hard-link/special-file/path-replacement/digest mismatch to the frozen evidence-integrity failure seam and return no bytes.
- [x] Enforce evidence list page hard max 200, download hard max 16 MiB, max 4 concurrent downloads and <=64 MiB buffered artifact bytes; semaphore waits must be context-cancellable and no artifact/cache state may outlive a request.
- [x] Reuse only the accepted frozen cursor envelope/kinds and Track-B lineage semantics. Do not invent a third cursor kind, server cursor table, or weaken append-only lineage. If frozen predecessor seams cannot satisfy a required cursor invariant within Track-C ownership, stop fail-closed rather than mutate A/B.
- [x] Add adversarial tests for ledger ordering/history, opaque evidence IDs, path/URI non-disclosure, metadata-only refs, symlink/hard-link/special-file and replacement races, SHA mismatch, stale snapshot/ref binding, size/concurrency/cancellation ceilings, cursor tamper/lineage, and missing registered artifacts.
- [x] Run gofmt; focused timeline/evidence tests and race; full uncached `go test ./...`; full uncached `go test -race ./...`; `go vet ./...`; Darwin/Windows production builds for Track-C packages where supported; `git diff --check`; exact owned-path and forbidden-semantic checks.
- [x] Mark every item complete, move this plan to `docs/plans/completed/`, create exactly one Track-C implementation commit, and leave the exact worktree clean.

## Completion boundary

Ralphex completion is implementation only. Fresh deterministic acceptance and fresh exact-head independent Critical/Major review at 0C/0M are mandatory before Track D.

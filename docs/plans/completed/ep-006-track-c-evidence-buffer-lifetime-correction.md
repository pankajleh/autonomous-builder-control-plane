# EP-006 Track C — Evidence Buffer Lifetime Correction

## Authority

- Exact blocked Track-C candidate: `37c6fb700b71ab5e26cf4b453d1bd604b47a1e60`.
- Exact Track-C review seal SHA-256: `c5f80e3e177194411307ecff020f1a67533bd3f8952be33182052a3a4e1137b5`.
- Review verdict: `IMPLEMENTATION_FINDINGS`, Critical 0 / Major 1 / Minor 0.
- Accepted EP-006 design: `908710e1606b0da761e612c36b85c866e182c7f7`.
- Accepted corrected baseline: `74c00f998ca5e452a151ff9c30abe777ca23331a`.

## Exact finding

The existing Track-C read semaphore is released when `ReadEvidence` returns, while the frozen HTTP handler retains and writes the returned `[]byte` afterward. Slow responses can therefore retain more than four verified artifact buffers and exceed the accepted 64-MiB aggregate buffer ceiling.

## Maximum correction scope

```text
internal/serviceapi/server.go
internal/serviceapi/server_test.go
this plan and its completed-plan move
```

All Track-B ledger/readmodel code, Track-C timeline/evidence implementation, runtime catalog, command wiring, actions/recovery/run/scheduler/integration/GitHub lifecycle, and every other path are read-only.
### Task 1: Enforce evidence buffer lifetime through HTTP response completion

- [x] Add one server-owned evidence-response guard with capacity exactly 4; acquisition must be context-cancellable and must occur before invoking `EvidenceReader.ReadEvidence`.
- [x] Hold that guard until the evidence response write returns on every success path; release it on all error/early-return paths after acquisition.
- [x] Keep the accepted `EvidenceReader` interface and Track-C `ReadEvidence` implementation unchanged; do not widen limits or add caches/queues/background retention.
- [x] Preserve the hard 16-MiB per-artifact limit; four guarded responses therefore cap verified artifact buffers at <=64 MiB.
- [x] Add deterministic service-level tests proving four slow/held response writes consume all four guards, a fifth acquisition cannot proceed, cancellation is honored, and release occurs only after response write completion.
- [x] Preserve existing authentication, error mapping, route semantics, response headers, and global 64-request ceiling.
- [x] Run focused serviceapi tests and race, focused timeline/evidence tests and race, full uncached tests, full uncached race, vet, and `git diff --check`.
- [x] Prove only the authorized paths changed and the exact prior Track-C implementation blobs remain unchanged.
- [x] Mark all items complete, move this plan to `docs/plans/completed/`, create exactly one correction implementation commit, and leave a clean worktree.

## Completion boundary

This correction addresses only the sealed Track-C Major. Ralphex completion is implementation evidence only. Fresh deterministic acceptance and fresh exact-head independent Critical/Major review at 0C/0M remain mandatory before Track D.

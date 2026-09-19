# EP-006 Track C shared evidence-buffer guard correction

## Authority

Exact blocked candidate: `e1205e69f13eb6601435fdab9980b6c07022c204`.
Accepted EP-006 design: `908710e1606b0da761e612c36b85c866e182c7f7`.
Prior accepted baseline: `74c00f998ca5e452a151ff9c30abe777ca23331a`.
Prior Track-C correction authority: `9d776d274f97a832588e58c5803faa939f624b16`.
Fresh exact-head review: 0 Critical / 1 Major, sealed under `ep006-track-c-buffer-lifetime-review-v2-e1205e6`.

The sole open finding is that the server-owned four-slot evidence buffer guard covers download responses but not evidence-list requests. `ListEvidence` may invoke the verified local evidence reader while four download response buffers remain live, permitting the accepted four-buffer / 64-MiB ceiling to be exceeded.

## Maximum ownership

```text
internal/serviceapi/server.go
internal/serviceapi/server_test.go
this plan and its completed-plan move
```

Everything else is frozen, including `EvidenceReader`, `internal/timeline/**`, `internal/evidence/**`, Track-B readmodel/ledger, runtime catalog, cmd, action/recovery/run/scheduler/integration/GitHub-lifecycle code.
### Task 1: close the mixed list/download buffer ceiling

- [x] Reuse one server-owned evidence-buffer permit pool of exact capacity four for both evidence-list and evidence-download routes.
- [x] Acquire the permit context-cancellably after route/input/registration validation but before either `ListEvidence` or `ReadEvidence` can invoke a verified local artifact read.
- [x] For downloads, retain the permit through `ResponseWriter.Write` completion exactly as the prior correction requires.
- [x] For evidence lists, retain the permit through the entire `ListEvidence` call and JSON response completion; conservative serialization is acceptable, widening the four-buffer ceiling is not.
- [x] Release every acquired permit on dependency error, invalid/integrity result, canceled wait, JSON response completion, and download write return.
- [x] Add deterministic mixed-route tests: hold four download writes, prove a fifth evidence-list request cannot call `ListEvidence`; cancellation must return the existing busy mapping without consuming a permit; after one/all writes release, list proceeds and all permits drain to zero.
- [x] Preserve the `EvidenceReader` interface, all auth/routes/headers/error mappings/body/page limits, and every Track-C timeline/evidence blob exactly.
- [x] Run focused service tests repeatedly and under race; unchanged Track-C tests/race; predecessor merge-lifecycle stress; full uncached tests/race; vet; Darwin/Windows service build; `git diff --check`; scope/frozen-blob checks.
- [x] Mark all items complete, move this plan to `docs/plans/completed/`, create exactly one correction implementation commit, and leave the worktree clean.

## Completion boundary

Ralphex completion is implementation only. Fresh deterministic acceptance and a fresh independent exact-head Critical/Major review at 0C/0M are mandatory before Track D. No EP-006 publication/merge occurs at this Track-C boundary.

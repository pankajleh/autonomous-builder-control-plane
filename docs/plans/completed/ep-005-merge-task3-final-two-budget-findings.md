# EP-005 — Task 3 Final Two Budget Findings Correction

## Authority

This correction is authorized solely by the sealed exact-head Task-3 closure review of candidate `52a891773c9e9870878861ea452342a11b65d19a` (tree `cb3dfb795af94f112ab9f8fa1189b4e4a3d01fab`), verdict SHA `506d073d774d110d227882721390d3063eb1f4c5cf1451cce78583d8995e2081`, review seal `36a011df491db4c3085f1b4838032677082a005d115f908ace2ca8dffeb2481a`.

Exactly two Major findings are authorized. Both map only to the already-accepted T3-14 controller-owned provider-budget seam. No third blocker family, provider redesign, Task 4, Phase 5, publication, merge, or status-document work is authorized.

## Exact findings

1. `M-01` / T3-14 — the first `counters.jsonl` HTTP-call reservation can become visible to transport before the new directory entry is crash-durable.
2. `M-02` / T3-14 + master Sections 7–8 — provider-budget exhaustion before target transport is incorrectly returned as `TARGET_UPDATE_UNKNOWN`, and budget exhaustion after a durable `APPLIED` result can return raw while leaving READY instead of selecting `FAILED/POST_MERGE_ACCEPTANCE_FAILED`.

## Owned paths

- `internal/mergelifecycle/store_linux.go`
- `internal/mergelifecycle/controller.go`
- `internal/mergelifecycle/task3_provider_budget_test.go`
- existing `internal/mergelifecycle/*_test.go` only where strictly required for these two predicates
- this plan, later moved to `docs/plans/completed/`

Everything else is frozen, including `internal/githubmergeprovider/**`, `internal/githublifecycle/**`, `internal/ledger/**`, `internal/run/**`, `internal/integrationgate/**`, `cmd/**`, `go.mod`, `go.sum`, architecture/status/progress docs, and all live-conformance code/evidence.

## Required semantics

### M-01 — first HTTP reservation durability

Before any HTTP-call reservation can authorize network I/O, existence of `counters.jsonl` itself must be crash-durable. On first creation, the store must establish the file and its directory entry durably before the reserved HTTP call is exposed to the provider: file contents/reservation are fsynced and the attempt directory is fsynced at the creation boundary. A directory-sync failure must fail closed before network authorization. Restart after every covered crash boundary must preserve the consumed/pending call and must never regain call, byte, or active-time budget.

Do not broaden this into unrelated store publication semantics. Reuse the existing store `syncFile`/`syncDir` fault-injection mechanism and safe path rules.

### M-02 — exact lifecycle dispositions on budget exhaustion

Budget exhaustion is never remote ambiguity when transport was not authorized.

- If `providerCallContext` fails before `SubmitTarget` because the cumulative provider budget is exhausted, no target HTTP request is permitted. The controller must terminalize the current READY attempt as `FAILED/RESOURCE_LIMIT_EXHAUSTED` through the existing terminal protocol, with no target provider call and no unresolved `TARGET_UPDATE_UNKNOWN` barrier result.
- If an exact valid `MergeResult` is already durably `APPLIED` (whether recovered in `executeAttempt` or produced in `settle`) and `providerCallContext` cannot authorize `ObservePostMerge` because the bounded provider budget is exhausted, the controller must preserve the durable applied result and terminalize exactly `FAILED/POST_MERGE_ACCEPTANCE_FAILED`. It must never return a raw budget error while leaving READY, and cancellation cannot replace this disposition.
- Storage-integrity errors and an actually unresolved/possibly-submitted target remain governed by their existing distinct paths; this correction must not relabel them as budget exhaustion.

## Frozen acceptance matrix

| ID | Required proof | Mandatory entrypoint |
| --- | --- | --- |
| F1 | First creation/reservation is file+directory durable before transport; directory-sync failure authorizes zero network calls; restart cannot regain a pending/consumed budget | `TestTask3FinalClosureM01FirstHTTPReservationDurability` |
| F2 | Pre-target budget exhaustion produces exactly `FAILED/RESOURCE_LIMIT_EXHAUSTED` with zero target HTTP calls; durable-APPLIED post-merge budget exhaustion produces exactly `FAILED/POST_MERGE_ACCEPTANCE_FAILED`, including restart/recovery | `TestTask3FinalClosureM02BudgetExhaustionDisposition` |

Both named entrypoints must exist exactly and assert terminal state + reason + mutation/provider-call counts, not only returned errors.

## Regression floor

Before the correction commit:
- F1 and F2 pass individually and together;
- all five prior Task-3 closure regressions pass;
- `internal/mergelifecycle` package tests and race pass;
- Task-1/Task-2 regression packages pass;
- `internal/githubmergeprovider` package tests pass unchanged;
- full `go test ./...`, `go test -race ./...`, `go vet ./...`, metadata-free smoke, and Darwin compile-only pass;
- no forbidden endpoint/live-GitHub effect occurs;
- changed-path audit contains only the owned paths above;
- exactly one implementation commit exists after the executable plan head;
- worktree is clean and `git diff --check` passes.

### Task 1: Close the final two Task-3 budget findings

- [x] Implement only F1/F2 and the exact T3-14 semantics above.
- [x] Add the two exact regression entrypoints with crash/restart and exact terminal-disposition assertions.
- [x] Run the complete regression floor and prove no scope escape or live GitHub mutation.
- [x] Move this plan to `docs/plans/completed/` and create exactly one correction implementation commit.

## Closure boundary

Correction implementation and deterministic acceptance do not by themselves close Task 3. A fresh independent exact-head Critical/Major closure review is still required against the exact correction head. The previously sealed corrected live-conformance boundary proof remains valid because this correction cannot edit provider/live-harness code; no new live mutation run is required unless the final reviewer identifies a concrete dependency of T3-18 on these controller-only changes.

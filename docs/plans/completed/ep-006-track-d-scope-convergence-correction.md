# EP-006 Track D — Scope Convergence Correction

Exact blocked Track-D implementation head: `cf7a5eb047f373e856738c0ecd011721226a52b8`.
Accepted Track-C predecessor: `e1b5367ea82e774d81b10c989d506efe16b7d5bc`.
Accepted EP-006 design: `908710e1606b0da761e612c36b85c866e182c7f7`.

Fresh deterministic acceptance found one governance defect: Track D created `internal/run/api_cancel.go`, while the accepted Track-D ownership ceiling permits runner mutations only in `internal/run/run.go` and `internal/run/run_test.go`.

This correction changes no accepted action semantics. It restores exact path authority by folding the helper implementation into the already-authorized `internal/run/run.go` and deleting the unauthorized file.

## Maximum correction ownership

```text
internal/run/run.go
internal/run/api_cancel.go   # removal only; no surviving file permitted
this plan and its completed-plan move
```

Everything else is frozen, including actionapi/actioncontrol behavior, cmd composition, A/B/C surfaces, recovery, ledger, readmodel, runtimecatalog, serviceapi, timeline/evidence, and accepted design docs.

### Task 1: converge Track D to the accepted runner path ceiling

- [x] Move the complete typed API-cancel provenance implementation from `internal/run/api_cancel.go` into `internal/run/run.go` without changing exported names, validation, deterministic event-ID derivation, durable request-event proof, cancellation payload fields, or error semantics.
- [x] Delete `internal/run/api_cancel.go`; no replacement runner file may be created.
- [x] Preserve all Track-D behavior byte/semantic-equivalent outside file placement; do not change actionapi/actioncontrol/cmd or tests except gofmt effects inside `run.go` if required.
- [x] Run focused `internal/run`, actionapi/actioncontrol integration, race, predecessor concurrency stress, full uncached tests/race, vet, Darwin/Windows production builds, diff check, and exact cumulative Track-D scope audit proving zero surviving unauthorized paths.
- [x] Mark all items complete, move this plan to `docs/plans/completed/`, create exactly one correction implementation commit, and leave the worktree clean.

## Completion boundary

Ralphex completion is implementation only. Fresh deterministic Track-D acceptance and fresh exact-head independent Critical/Major review at 0C/0M remain mandatory before combined EP-006 acceptance.

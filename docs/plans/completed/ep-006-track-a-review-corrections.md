# EP-006 Track A — Exact-Head Review Corrections

## Authority

Exact reviewed implementation head: `c60e0f1be41146665f2cf7186c609ecfdb0f4c75`.
Accepted design ancestor: `908710e1606b0da761e612c36b85c866e182c7f7`.
Deterministic acceptance for the reviewed head: PASS, evidence SHA-256 `bef1f168bfc0893d35f7e8fba7e4fcace5c113863c9da339efcdc20dd4d0e11a`.
Independent-provider attempt failed before a complete verdict under `DECISION_AND_ACCEPTANCE_POLICY.md` provider-fallback rules. Controller fallback exact-head review is authoritative for this correction and is frozen at SHA-256 `4e524e5faabaddd540d92f4778646ec776d56cee4d50d04702027e5943fe3bb9`.

Active finding set is EXACTLY: M1, M2, M3, m1 from that review. No new blocker may be added in this correction pass without returning for new authority.

Accepted EP-006 design documents are read-only and public-v1 semantics, hard ceilings, deferrals, and A/B/C/D ownership may not change.

## Correction-owned production maximum

- `internal/runtimecatalog/**`
- `internal/serviceapi/**`
- `cmd/abcp/main.go` and `cmd/abcp/main_test.go` only if strictly required to prove frozen dependency composition; avoid otherwise
- this correction plan, moved to `docs/plans/completed/` after all checks pass

Tracks B/C/D business logic remains forbidden. No ledger/evidence/run/recovery/scheduler/integration/GitHub/merge mutation.

### Task 1: Close the frozen Track-A review findings

- [x] M1: make catalog create-verify and replace crash-recoverable. Interrupted temporary publication must never leave a valid final record rejected solely by a surviving temp link/name, and stale controller temp artifacts must be safely recoverable without accepting arbitrary names or weakening mode/owner/link/fsync checks. Add regression tests for the exact pre-publication, post-publication/pre-cleanup, and pre-replace crash residue cases.
- [x] M2: complete the frozen future-track transport seam without implementing B/C/D behavior. Add authenticated fail-closed parsing/dispatch for every accepted future `/v1/runs/...` read/action/status route, sufficient typed pagination/action inputs (including action kind), bounded strict request decoding, canonical dependency-error mapping, and capability flags derived from installed accepted dependencies. Nil dependencies remain `unsupported_capability`; no fabricated success. Later B/C/D must be able to implement their owned packages and plug into these interfaces without mutating `internal/serviceapi` public routing semantics.
- [x] M3: make catalog keyset pagination obey the <=16 MiB physical registration-read ceiling per request and remain usable as the valid catalog grows within accepted stored/count bounds. Do not scan all registration bodies before selecting a <=200-row page. Preserve lexical run-ID ordering, insertion semantics, immutable validation, and signed cursor bytes. Add large-catalog/order/insertion/resource-bound tests.
- [x] m1: revalidate type, effective-user ownership, exact 0600 mode and Nlink==1 on the final reopened protected config/catalog descriptor as well as device/inode identity. Add focused regression coverage.
- [x] Keep every correction inside the accepted Track-A architecture and exact frozen finding semantics. Do not widen any hard ceiling or add a new endpoint/action semantic.
- [x] Run gofmt; focused `go test` and `go test -race` for runtimecatalog/serviceapi/cmd as touched; `go test ./...`; `go test -race ./...`; `go vet ./...`; Darwin and Windows compile-only for affected packages/command; `git diff --check`; exact path-scope and forbidden-semantic checks.
- [x] Mark all items complete, move this plan to `docs/plans/completed/`, create exactly one correction implementation commit after this authority commit, and leave the exact worktree clean.

## Completion boundary

Ralphex completion is correction implementation only. The resulting exact head must rerun deterministic acceptance and a fresh exact-head Critical/Major closure review. Track B remains blocked until 0 Critical / 0 Major.

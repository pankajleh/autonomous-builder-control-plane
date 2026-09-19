# EP-006 Track B cursor disclosure closure correction

Exact reviewed candidate: `be98be6fb0a5dd8db338bdcfa761d07dbdeb59e4`.
Fresh exact-head closure verdict: `IMPLEMENTATION_FINDINGS`, Critical 0 / Major 1 / Minor 0.
All six prior Track-B Majors are explicitly source-closed by that review. This correction is limited to the one new Major below.
Accepted EP-006 design: `908710e1606b0da761e612c36b85c866e182c7f7`.
Accepted Track-A predecessor: `f082cae1677d002825b7f4176d9783e199c9a125`.
Fresh deterministic acceptance summary for the reviewed candidate: `c41080bee39d468b03c89f7dcc8ba67f75151157ac98656e6cfd2230afbe1891`.

### Task 1: Close the single cursor path-disclosure Major

- [x] Prevent `PriorLastEventID` in the signed client-visible ledger cursor from disclosing the registered ledger path or evidence root. The cursor is signed/base64url encoded, not encrypted, so raw internal paths must never be embedded in its payload.
- [x] Preserve the accepted stateless lineage contract: the cursor must still bind the prior terminal event identity deterministically and continuation must fail closed for replacement/truncation/prior-byte or prior-event mismatch. Do not weaken byte-prefix, ordinal, line-digest, physical-identity, route/filter/run, or key-epoch checks.
- [x] Do not globally tighten legacy ledger EventID semantics outside Track B unless required by the accepted contract. Prefer a Track-B projection/cursor boundary solution that is deterministic and consistent on cursor creation and verification.
- [x] Add adversarial regression coverage proving a valid event whose raw EventID equals the registered ledger path and one whose raw EventID equals the evidence root cannot leak either path through `next_cursor`, while append-only continuation still works and tampered/mismatched lineage still fails closed.
- [x] Maximum production scope: `internal/readmodel/**`. Tests may use `internal/readmodel/**`. `internal/ledger/**`, `internal/serviceapi/**`, `internal/runtimecatalog/**`, `cmd/**`, timeline/evidence/action/recovery/run packages and Track-A surfaces are frozen and read-only.
- [x] Run gofmt; focused readmodel tests/race; full uncached tests/race; `go vet ./...`; Darwin/Windows production builds for readmodel; exact scope and `git diff --check`.
- [x] Mark every item complete, move this plan to `docs/plans/completed/`, create exactly one correction implementation commit, and leave the worktree clean.

Ralphex completion is correction implementation only. Fresh deterministic acceptance and a fresh exact-head Critical/Major closure review at 0C/0M are still required before Track C may start.

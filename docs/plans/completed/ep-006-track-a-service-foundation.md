# EP-006 Track A — Service Foundation, Runtime Catalog, Authentication

## Authority

This bounded implementation task is an exact Track-A subset of accepted EP-006 A-design head `908710e1606b0da761e612c36b85c866e182c7f7` (tree `4f3b77ca94a3ef0864b638218b347bad0d7237fc`). The fresh independent final design gate returned `DESIGN_ACCEPTED`, Critical 0 / Major 0 / Minor 0, with review artifact SHA-256 `f3b0e9dd5cad72c19c4a42385de9a5530b4b1b20c199a4d2c7faf34524fc0053` and sealed review-packet manifest SHA-256 `0bb091ecdbd1e681ec501fee5780f5b8142691696baf5213ab498e9d6bdbcad9`.

The plan-only authority commit containing this file MUST be the sole child of the accepted design head and contain no source-code change. Track A only. Tracks B, C and D are forbidden until Track A independently passes deterministic acceptance and exact-head review.

Normative design remains read-only during B implementation:
- `docs/architecture/EP006_SERVICE_API_CONTRACT.md`
- `docs/execution-packs/EP-006-service-api-projections-actions.md`
- `docs/plans/ep-006-service-api-projections-actions.md`

## Owned maximum

- `internal/serviceapi/**`
- `internal/runtimecatalog/**`
- `cmd/abcp/main.go`
- `cmd/abcp/main_test.go`
- this task plan, moved to `docs/plans/completed/` only after all checks pass

Do not modify accepted design documents, `internal/ledger`, `internal/evidence`, `internal/readmodel`, `internal/timeline`, `internal/actionapi`, `internal/actioncontrol`, `internal/run`, `internal/recovery`, scheduler/integration/GitHub/merge packages, or roadmap/projection docs.

### Task 1: Implement Track-A service foundation

- [x] Verify clean start, authority-only parentage, accepted design/review hashes, and exact allowed path set before source mutation.
- [x] Implement canonical controller-owned mode-0700 `service_root` handling with no symlink traversal/replacement and bounded <=2s service filesystem locks.
- [x] Implement exact run/attempt identifier grammar and immutable create-or-byte-verify `RunRegistrationV1` / `AttemptRegistrationV1` with accepted 10,000-run, 1,000-attempt/run, 64-KiB record and <=16-MiB catalog-read ceilings.
- [x] Implement `ActiveOwnerLeaseV1`: exact run/attempt, monotonic per-run `lease_generation`, exact PID+boot-ID+Linux-start-ticks process identity, strict-canonical `lease_id`, and durable `ACTIVE -> CLOSING -> RETIRED` state. Expose the <=2s `AcquireOwnerLeaseGuard` contract needed later by D. Generation install/replace is fail-closed and cannot overwrite a live/stale prior generation by inference.
- [x] Add optional `abcp run --service-root`; register exact run/attempt and install the active owner before governed execution. Because Track-A capabilities expose no action admission yet, normal Track-A-only shutdown may close the lease through a zero-action `CLOSING -> RETIRED` path; D later owns the journal-watermark final drain. Runs without the flag preserve legacy behavior and remain API-invisible.
- [x] Implement safe startup readers for bearer token, principal ID, cursor key and authority grants under the accepted byte/count/ownership/mode/no-symlink/no-hardlink constraints. Token/key bytes never enter logs, ledger, catalog, evidence, errors or responses.
- [x] Implement shared `CursorEnvelopeV1` HMAC-SHA256 signer/verifier and the Track-A-owned `catalog` cursor variant only: route-bound kind, key ID, filters, lexical last-run key, <=15-minute expiry, <=4-KiB token, keyset insertion semantics, invalid-MAC/kind/epoch failures. Do not implement B's ledger-lineage cursor payload.
- [x] Implement service DTOs, canonical safe errors/request IDs, Authenticator/AuthorityMatcher foundations, loopback-only HTTP server, <=64 in-flight requests, <=1-MiB body, and exact 5s/10s/30s/60s HTTP timeouts.
- [x] Implement authenticated `GET /v1/capabilities` deterministically. Track A reports `run_admission=false`, `retry=false`, `resume=false`, `recovery=false`, `cancel=false`, `decision=false`, `evidence_download=false`; later tracks may turn only their accepted capabilities on.
- [x] Implement authenticated signed-keyset `GET /v1/runs` from immutable `run.json` records only. Return only `run_id`, repository identity digest, initial registration timestamp and detail URL; consume zero ledger snapshots and do not inspect attempt/active directories or claim lifecycle/current-attempt state.
- [x] Reserve/freeze dependency interfaces and authenticated fail-closed routing needed by later B/C/D without implementing their projections/evidence/actions or fabricating success.
- [x] Add Linux process-identity integration and non-Linux fail-closed/compile behavior without inventing a weaker owner identity.
- [x] Add adversarial tests covering unsafe service roots/config files, symlink/hardlink/ownership/mode/replacement, traversal IDs, duplicate/conflicting registration, bounds, monotonic owner generations, stale/PID-reused owner proof, owner guard/state transitions, anonymous `/v1`, missing config startup, non-loopback listener, request/concurrency/body/timeouts, secret/path non-disclosure, catalog ordering/byte ceiling, signed cursor tamper/epoch/kind/expiry/insertion behavior, zero-snapshot list, and deterministic capabilities.
- [x] Run gofmt; focused tests and race for `internal/serviceapi`, `internal/runtimecatalog`, `cmd/abcp`; `go test ./...`; `go test -race ./...`; `go vet ./...`; non-Linux compile-only for affected packages; `git diff --check`; and explicit forbidden-scope/forbidden-semantic scans.
- [x] Prove only the owned maximum plus this plan changed from the authority-only plan commit; mark every checkbox complete, move this plan to `docs/plans/completed/`, create the implementation commit, and verify clean exact committed HEAD.

## Completion boundary

Ralphex completion is implementation completion only. It creates no deterministic acceptance, independent review, publication, merge, or Track-B authority. After Ralphex completes, the exact implementation head must separately pass deterministic acceptance and a fresh independent exact-head Critical/Major review at 0C/0M.

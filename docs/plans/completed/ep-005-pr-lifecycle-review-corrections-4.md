# EP-005 — Exact-Head PR Lifecycle Post-Implementation Review Corrections Round 4

## Authority and scope

- Exact accepted head: `03caec5b822eb038658ac7556500b1decd3f3365` (ABCP `BRANCH_ACCEPTED`, clean).
- Controller fallback review artifact: `/home/devagent/abcp-runtime/ep006-pr-lifecycle-review-corrections-3-run2/post-implementation-controller/controller-review.json`.
- Review artifact SHA-256: `20421b265b4fcbcdb8f08ddfb7d77882ada7a3a6a980cb0bece57b73aeb700d8`.
- Verdict consumed: **0 Critical + 1 Major** (`REVISION_REQUEST_IDENTITY_NOT_SELF_AUTHENTICATING`).
- Exact-SHA isolated reproduction proved a different request can falsely recover an older terminal after tampering only `revision.RequestSHA256`.

This correction addresses only that Major. No GitHub API behavior, PR mutation semantics, admission schema version, state-machine ownership, CI/merge behavior, Phase-5+ work, or `internal/githublifecycle` behavior changes.

## Root cause

`Upsert` computes `requestSHA = SHA256(canonical JSON {source authority, title, body})`. `makeRevision` persists that digest plus canonical source/derived authority bytes and their digests and the document digest. But `readRevision` currently proves only canonical JSON plus schema/resource/ordinal.

Replay dispatch then trusts `revision.RequestSHA256` before terminal recovery:

- current-max exact replay compares `revision.RequestSHA256 == requestSHA`;
- lower-barrier exact replay reads the barrier revision and compares the same field.

`recoverTerminal` authenticates the terminal, source/derived authority, document, generation and marker chains, but does not re-derive `revision.RequestSHA256`. Therefore a canonical grammar-valid revision file with only that field altered can select the wrong terminal for a different request.
## Correction invariant

**A persisted `revisionRecord` is not trusted until every identity duplicated inside it is re-derived from its canonical payload.** `readRevision` is the single authentication boundary and must fail `ADMISSION_INTEGRITY_FAILURE` before returning a record when any of these checks fail:

1. strict canonical JSON, schema version, resource key and ordinal (existing checks);
2. `SourceAuthority` is strict canonical authority and `SourceSHA256 == SHA256(SourceAuthority)`;
3. `Authority` is strict canonical authority and `AuthoritySHA256 == SHA256(Authority)`;
4. `DocumentSHA256 == documentDigest(Title, Body)`;
5. `RequestSHA256 == canonicalDigest({Authority: SourceAuthority, Title, Body})` using the exact same field names/order/types as `Upsert`.

No replay comparison may observe an unauthenticated `RequestSHA256`. Because all replay/re-entry/resume paths already call `readRevision`, placing the proof there closes current-max and lower-barrier replay without adding a parallel dispatch-specific validator.

Existing correctly-written records remain byte-compatible. There is no schema bump, migration, rewrite, repair, deletion, or compaction. Tampered historical records fail closed rather than being normalized or healed.

## Exact implementation

### `internal/prlifecycle/terminal.go`

Add a package-private helper (name may be `validateRevisionIdentity`) used only by `readRevision`. It accepts the decoded `revisionRecord` and performs the five checks above. Reuse existing `validateCanonicalAuthority`, `documentDigest`, and `canonicalDigest`; do not create a second authority parser or alternate request-hash format.

`readRevision` order must be:

1. bounded descriptor-relative read;
2. `strictJSON` + schema/resource/ordinal check;
3. canonical re-marshal equality check;
4. revision identity validation;
5. return the record.
`validateCanonicalAuthority` already verifies strict canonical bytes and the supplied digest. `canonicalDigest` must be invoked over the same anonymous wire shape used by `Upsert`:

```go
struct {
    Authority json.RawMessage `json:"authority"`
    Title     string          `json:"title"`
    Body      string          `json:"body"`
}{revision.SourceAuthority, revision.Title, revision.Body}
```

Any marshal/validation/digest mismatch returns `&Error{Code: CodeIntegrityFailure, ...}`. Do not silently recompute and overwrite fields.

### `internal/prlifecycle/review4_linux_test.go` (new)

Add focused adversarial regressions against immutable revision files. Use isolated admission roots and existing fixtures/helpers; do not alter production files outside the test root.

Required cases:

- current-max terminal: tamper only `RequestSHA256` to the hash of a different request; that different request must return `ADMISSION_INTEGRITY_FAILURE`, make zero GitHub requests/writes, publish no new terminal, and not return the old terminal as success;
- lower-barrier terminal with a later zero-write ordinal: same tamper and same fail-closed result;
- for both current-max and lower-barrier shapes, independently tamper `SourceSHA256`, `AuthoritySHA256`, and `DocumentSHA256`; each must fail integrity before any GitHub call;
- independently alter canonical `SourceAuthority` bytes while leaving its digest stale, and canonical `Authority` bytes while leaving its digest stale; each must fail integrity;
- positive controls: untampered current-max exact replay and untampered lower-barrier exact replay remain zero-network and return the identical terminal SHA;
- create-or-verify/re-entry/resume regressions from round 3 remain green.

### `internal/prlifecycle/review3_linux_test.go` compatibility fixture correction

Round 3 contains one adversarial helper (`writeAbandonedRevision`) that deliberately writes an arbitrary `RequestSHA256` unrelated to its stored `SourceAuthority`/`Title`/`Body`. That fixture predates this invariant and would now fail for the right production reason before reaching the barrier behavior it is intended to test. Update only that synthetic fixture so the abandoned revision is internally self-consistent: choose a different title/body (or equivalent different request), recompute `DocumentSHA256`, and recompute `RequestSHA256` with the exact `Upsert` wire shape. Preserve the test's intended non-confirmed-barrier `REVISION_CONFLICT` assertion and zero-network behavior. Do not weaken `readRevision` to accommodate the old invalid fixture.
## Verification

Run in this order after `gofmt`:

1. `go test ./internal/prlifecycle`
2. `go test ./...`
3. `go test -race ./...`
4. `go vet ./...`
5. `make smoke`
6. `git diff --check 03caec5b822eb038658ac7556500b1decd3f3365 HEAD`

Commit only after every validation passes. Then move this plan to `docs/plans/completed/` in the same governed implementation commit.

## Completion and review gate

Ralphex completion does not authorize publication. The exact correction commit must pass ABCP deterministic acceptance and then a fresh exact-head Critical/Major review. Claude/cross-model review is preferred; controller fallback is permitted only on genuine provider failure under the existing decision policy. Any Critical or Major finding starts another governed correction cycle. No push, PR #8 creation/update, merge, or later Phase-4 work is authorized until the exact reviewed head is clean.

# IMPLEMENTABLE_PLAN

### Task 1: Authenticate persisted revision request identity before replay

- [x] Make `readRevision` reject any persisted revision whose source authority, derived authority, document digest, or request digest is not self-consistent.
- [x] Add all current-max and lower-barrier adversarial tamper regressions plus untampered positive replay controls.
- [x] Preserve round-3 barrier, resume, one-submission/no-replay, evidence, locking, capacity, transport, reconciliation, and terminal-chain invariants.
- [x] Run every verification command above successfully.
- [x] Move this plan to `docs/plans/completed/` and commit only after validation.

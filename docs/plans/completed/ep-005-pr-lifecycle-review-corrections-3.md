# EP-005 — Exact-Head PR Lifecycle Post-Implementation Review Corrections Round 3

## Authority and scope

- Accepted correction head: `35faeec8b8314519ee6b9d36eba32a0c25ac6050` (verified clean at design time).
- Claude exact-head review artifact: `/home/devagent/abcp-runtime/ep006-pr-lifecycle-review-corrections-2/post-implementation-claude/review.txt`
- Artifact SHA-256: `9fa76414ce73fc9168de70d967e28539cf662a54802e09678d1a5de829266974` (verified).
- Verdict consumed: the ten prior findings are confirmed corrected. **One Critical remains.**

This round corrects **only** that single Critical. It adds no CI ingestion, no merge execution, no domain-state transition, no service/API/dashboard, no Phase-5+ behavior, no new GitHub endpoint, and no change to frozen `internal/githublifecycle`. No admission record is ever deleted, compacted, or rewritten.

## Context

`upsertLocked` derives the prior-`applied_confirmed` proof (`previous`) from **one** revision ordinal — the highest ordinal that owns `revision.json` (`latestRevision`, `terminal.go:503`) — and only when that same ordinal also owns `generation.json` (`controller.go:289-321`). Three states therefore admit new mutation authority with `previous == nil`:

1. **Abandoned intermediate revision.** `revision.json` is fsynced at `controller.go:356`; `submit` can then fail at `NewUpsertPullRequestInput`, attempt sizing, `NewTerminalBudget`, `prepareWrite` (`:515`), or `tx.createJSON(genName, …)` (`:536`) — or the process can be killed in that window. That ordinal becomes the permanent `latest` while owning no generation, so the last confirmed terminal at a lower ordinal is invisible. A later differing request writes `superseded.json` (`:311`), advances to `latest+1` with `previous == nil`, takes the discovery fallback (`:432`), sees zero open candidates once the confirmed PR was closed remotely, resolves `mode = "CREATE"`, and `POST`s a **second** pull request for a resource whose confirmed terminal binds a different PR number. The resource then holds two `applied_confirmed` terminals with different `PRNumber`/`PRNodeID`, which nothing can reconcile.
2. **Same-request re-entry at an existing ordinal.** Fall-through past `controller.go:322` keeps `ordinal = latest` with `previous == nil`, so the prior title/body/identity proof at `:466` is skipped and a remotely edited confirmed PR is overwritten instead of failing closed; if the confirmed PR was closed, the same fall-through resolves CREATE.
3. **Resume.** `resumeGeneration` calls `c.prepare(…, nil)` at `controller.go:593`, so an in-place resume re-preps without the prior document proof.

The intended outcome: the prior-confirmed proof becomes **resource-scoped** rather than `latest`-scoped, and every path that can admit new mutation authority receives it.

### Stated interpretation

The review's regression (a) requires "zero writes and never issues a `POST /pulls`". This design reads that as **zero remote PR submissions**: no `POST`/`PATCH`, no `generation.json`, no `submitted.json`, no `terminal.json`. The zero-remote-write settlement record `superseded.json` and the bounded prepare record for the refused ordinal are still created, because both are authorized by the existing differing-request and prepare-history contracts and both precede any network mutation. Regressions assert this exact record set.

## Correction invariant (single, Critical)

**The prior-confirmed barrier is a property of the physical PR resource, not of the numerically latest revision ordinal.** Under the resource lock, before any new mutation authority is admitted, the controller scans the admission root for the highest revision ordinal that owns a `terminal.json`, authenticates that terminal through the existing terminal/generation/marker/revision chain, requires `applied_confirmed`, and passes it into `prepare` on **every** admitting path — new ordinal, same-request re-entry at an existing ordinal, and `resumeGeneration`. Absence, closure, merge, or identity/document proof failure of the terminal-bound PR is fail-closed with zero new submission.

Preserved unchanged: one submission / no replay per generation, exact-head authority, descriptor-relative locking and lock order (`resource → capacity`), immutable evidence and publish-or-verify, bounded reads, cross-run terminal recovery, and the terminal/generation/marker/event chain. No schema version bump; no stored record becomes invalid.

## 1. Exact state / scan algorithm

`latestRevision` is **replaced** by `scanResource`, so there is one source of truth for resource state. It performs the single `readDir()` that `latestRevision` already performed — no additional directory listing, no file reads.

```go
const (
    kindRevision   uint8 = 1 << 0
    kindGeneration uint8 = 1 << 1
    kindSubmitted  uint8 = 1 << 2
    kindTerminal   uint8 = 1 << 3
    kindSuperseded uint8 = 1 << 4
)

type resourceStateV1 struct {
    maxOrdinal     uint64            // highest ordinal owning revision.json (0 if none)
    barrierOrdinal uint64            // highest ordinal owning terminal.json (0 if none)
    present        map[uint64]uint8
}
```

**Pass 1 — classification (names only, zero file reads).** For each entry of `tx.store.readDir()`:

1. Skip unless the name has the exact prefix `"r-" + tx.key.String() + "-rev-"`. This excludes `capacity.lock`, `r-<key>.lock`, `r-<key>-resource.json`, and every other resource.
2. `m := admissionName.FindStringSubmatch(name)`. If `m == nil` or `m[1] != tx.key.String()` or `m[2] == ""`, **skip** (see §2 for why this is not a hard failure).
3. `ordinal, err := strconv.ParseUint(m[2], 10, 64)`; `err != nil || ordinal == 0` ⇒ `ADMISSION_INTEGRITY_FAILURE`.
4. Map `m[3]` (new capture group, §2) to a flag: `revision|generation|submitted|terminal|superseded`. Any other kind (`prepare-run-*`, `resume-*`, `reconcile-*`) is **skipped without recording an ordinal** — prepare records legitimately exist for an ordinal that has no `revision.json` yet (`TestPrepareHistoryCeilingAndRevisionIndependence` depends on this).
5. Duplicate flag for the same ordinal ⇒ `ADMISSION_INTEGRITY_FAILURE` (defensive; the grammar makes it unreachable).
6. Track `maxOrdinal` over `kindRevision` and `barrierOrdinal` over `kindTerminal`.

**Pass 2 — structural validation (fail-closed, zero file reads).**

| Rule | Violation |
| --- | --- |
| every recorded ordinal owns `revision.json` | `ADMISSION_INTEGRITY_FAILURE` |
| `submitted` or `terminal` ⇒ `generation` | `ADMISSION_INTEGRITY_FAILURE` |
| `terminal` ⇒ `submitted` | `ADMISSION_INTEGRITY_FAILURE` |
| `superseded` and `generation` are mutually exclusive at one ordinal | `ADMISSION_INTEGRITY_FAILURE` |
| revision ordinals are contiguous `1..maxOrdinal` (no gap) | `ADMISSION_INTEGRITY_FAILURE` |
| **at most one unsettled ordinal, and it must be `maxOrdinal`** (settled ≡ owns `terminal` or `superseded`) | `ADMISSION_INTEGRITY_FAILURE` |
| `barrierOrdinal <= maxOrdinal` | `ADMISSION_INTEGRITY_FAILURE` |

`unresolved ≡ present[maxOrdinal] & kindGeneration != 0 && present[maxOrdinal] & kindTerminal == 0`. Because Pass 2 forbids an unsettled ordinal below the maximum, "a generation with a marker and no terminal" is detectable resource-wide from `maxOrdinal` alone.

**Order independence and bounds.** The algorithm depends only on set membership and maxima, so `readDir` ordering is irrelevant. `present` holds at most one 9-byte entry per matching root entry, and root entries are bounded by `PRAdmissionPolicyV1.MaxAdmissionFiles = 65536`, enforced by `inventory()` under `capacity.lock` before every file creation. `zeroWriteAfterBarrier()` is a names-only scan of `present` and returns true only when every ordinal above `barrierOrdinal` lacks `generation`, `submitted`, and `terminal`. Exact replay reads at most one additional bounded `revision.json` (the barrier revision) before `recoverTerminal`; admitting mutation paths still read at most one terminal (§4).

**Dispatch in `upsertLocked`** (replaces `controller.go:265-325`):

```
ensureResourceRecord(tx, authority)
state := scanResource(tx)

if state.maxOrdinal > 0 {
    revision := readRevision(tx, state.maxOrdinal)
    // Exact replay of the current terminal remains first and unchanged.
    if revision.RequestSHA256 == requestSHA && state.has(maxOrdinal, kindTerminal) {
        return c.recoverTerminal(tx, terminalName(maxOrdinal), request.RunID)
    }

    // A lower barrier terminal may also be exact-replayed when every later ordinal
    // is provably zero-write: no generation, submitted marker, or terminal exists
    // above barrierOrdinal. This covers abandoned/superseded intermediate revisions
    // without allowing replay to bypass an active or ambiguous newer generation.
    if state.barrierOrdinal > 0 && state.barrierOrdinal < state.maxOrdinal &&
       state.zeroWriteAfterBarrier() {
        barrierRevision := readRevision(tx, state.barrierOrdinal)
        if barrierRevision.RequestSHA256 == requestSHA {
            return c.recoverTerminal(tx, terminalName(state.barrierOrdinal), request.RunID)
        }
    }
}

ordinal := uint64(1)
var previous *terminalV1
if state.maxOrdinal > 0 {
    ordinal = state.maxOrdinal
    switch {
    case revision.RequestSHA256 != requestSHA:
        if state.unresolved() { return REVISION_CONFLICT }              // active generation, no terminal
        previous = c.barrierTerminal(tx, state, request.Authority)      // load + gate  (§4)
        if !state.has(maxOrdinal, kindTerminal|kindSuperseded) {
            tx.createJSON(recordPrefix(key,max)+"superseded.json", …)   // unchanged zero-write settlement
        }
        ordinal = state.maxOrdinal + 1
    case state.unresolved():
        return c.resumeGeneration(ctx, tx, request, revision, state)    // §6
    case state.has(maxOrdinal, kindSuperseded):
        return REVISION_CONFLICT                                        // §7
    default:                                                            // same-request re-entry, ordinal = maxOrdinal
        previous = c.barrierTerminal(tx, state, request.Authority)      // §5
        if previous != nil && (revision.Mode != "UPDATE" ||
            revision.PRNumber != previous.Core.ResultCore.PRNumber ||
            revision.PRNodeID != previous.Core.ResultCore.PRNodeID) {
            return ADMISSION_INTEGRITY_FAILURE                          // pre-network, zero remote reads
        }
    }
}
assert state.barrierOrdinal < ordinal          // else ADMISSION_INTEGRITY_FAILURE
prepareRound := nextPrepareRound(tx, request.RunID, ordinal, …)          // unchanged
prep := c.prepare(ctx, key, ordinal, prepareRound, "prepare", request, previous)
…                                                                        // unchanged from controller.go:332
```

The barrier is loaded **lazily, only on paths that can admit mutation authority**. The exact-replay path and the marker-present reconcile-only branch of `resumeGeneration` (`controller.go:570-588`) never load it, so an unrelated corrupt or non-confirmed lower terminal can never block read-only reconciliation of an ambiguous submitted write — the safety-critical liveness path is untouched.

## 2. Admissible record grammar

`admissionName` (`store_linux.go:19`) gains **one capture group** around the existing kind alternation, so kind classification is grammar-driven instead of suffix-guessed. The set of matching names is unchanged, and capture indices 1 and 2 are unchanged, so `nextPrepareRound` (`terminal.go:569`) and `inventory` (`store_linux.go:369`) are unaffected and no stored record becomes invalid:

```
^r-([0-9a-f]{64})(?:\.lock|-resource\.json|-rev-([1-9][0-9]*)-((?:revision|generation|submitted|terminal|superseded|prepare-run-[0-9a-f]{64}-[1-3]|resume-[1-4]|reconcile-[1-8]-(?:start|observation)))\.json)$
```

`scanResource` structurally interprets only `revision|generation|submitted|terminal|superseded`. `prepare-run-…`, `resume-…`, and `reconcile-…` remain governed by their existing dedicated parsers and are not part of resource-state structure.

**Why non-matching entries are skipped rather than rejected.** Root-wide unknown-entry rejection already exists in `inventory()` and runs under `capacity.lock` before **every** `create` (`store_linux.go:253`, `369-372`). Every admitting path in this design creates at least one file (prepare record, `superseded.json`, `revision.json`), so a hostile or leftover non-matching entry still fails the request closed at the first creation. The read-only paths that skip `inventory` — exact replay and reconcile-budget-exhausted resume — perform no mutation at all. Making `scanResource` itself reject non-matching prefixed names would additionally reclassify the deliberate `terminal.json.saved` leftover in `TestReconciliationStartPrecedesRemoteAndImmediateRetryMakesNoCalls` from `RECONCILIATION_BUDGET_EXHAUSTED` to `ADMISSION_INTEGRITY_FAILURE`, breaking a green regression, without adding real authority: barrier authority requires a fully validated `terminal.json` plus its chain, which a non-matching name can never supply.

## 3. When direct PR reads occur

No new endpoint, no new call class. Inside `prepare` only, in this fixed order:

1. `GET /user` (`controller.go:388`)
2. `GET /repos/{owner}/{repo}/git/ref/heads/{head}` — must equal `Authority.HeadSHA()` exactly
3. `GET /repos/{owner}/{repo}/git/ref/heads/{base}` — must equal `Authority.ExpectedBaseTipSHA()` exactly
4. exactly one of:
   - authority-bound PR ⇒ `GET /repos/{owner}/{repo}/pulls/{bound.Number}` (`:408`)
   - **`previous != nil` ⇒ `GET /repos/{owner}/{repo}/pulls/{previous.Core.ResultCore.PRNumber}` (`:416`) and no discovery call at all**
   - otherwise ⇒ `GET /repos/{owner}/{repo}/pulls?…` discovery, then the full PR read for a single candidate
5. `validateExactPR` (`:461`), then the prior-document/identity proof (`:466`)

Per prepare round with a barrier present: exactly 4 bounded GETs — the same count as today's UPDATE path. The resume path drops from `list + full-PR` to `full-PR`, i.e. one call fewer. `previous != nil` **never** falls back to discovery, which is what `listReads` assertions pin.

## 4. How the previous terminal is selected and authenticated

**Selection.** `previous` = the terminal at `state.barrierOrdinal` = the highest revision ordinal owning `terminal.json`. This is well-defined and complete because Pass 2 guarantees every lower ordinal is settled (terminalized or zero-write superseded) and no ordinal below the maximum is unresolved; superseded ordinals performed no remote write, so the remote document still belongs to the barrier terminal. Reading only the maximum terminal is inductively sufficient: a non-confirmed terminal can never be followed by a later admitted ordinal, because this gate runs before every admission.

**Authentication** — new helper `(*Controller).barrierTerminal`, side-effect free. It must **not** call `recoverTerminal`, which republishes evidence and records a ledger event.

1. `readTerminal(tx, recordPrefix(key, ordinal)+"terminal.json")` — canonical-bytes equality, `terminal_core_sha256`, and deterministic material-event chain (`terminal.go:438-456`).
2. `readGenerationAndMarker(tx, ordinal)` — canonical generation, marker↔generation binding, `Reservation == MaxTerminalBytes` (`terminal.go:470-484`); require `prior.Core.GenerationSHA256 == genSHA`, `prior.Core.SubmittedSHA256 == markerSHA`, `prior.Core.ResultCore.WriteID == generation.WriteID`. This is exactly the chain check at `controller.go:302-305`, relocated to the barrier ordinal.
3. `readRevision(tx, ordinal)` — require `revision.SourceSHA256 == Core.SourceAuthoritySHA`, `revision.AuthoritySHA256 == Core.DerivedAuthoritySHA`, `revision.DocumentSHA256 == Core.DocumentSHA256`, `generation.AuthoritySHA256 == Core.DerivedAuthoritySHA`, `generation.AttemptSHA256 == Core.AttemptSHA256`.
4. Policy/limits identity: `Core.PolicySHA256`/`Core.LimitsSHA256` equal the live store policy and adapter limits digests ⇒ else `ADMISSION_POLICY_MISMATCH`.
5. Resource/revision identity: `Core.ResourceKey == key`, `Core.Revision == ordinal`, `ResultCore.ResourceKey == key`, `ResultCore.Revision == ordinal`, `ResultCore.Generation == 1`, `documentDigest(Core.Title, Core.Body) == Core.DocumentSHA256`.
6. **Disposition gate:** `ResultCore.Disposition != AppliedConfirmed` ⇒ `REVISION_CONFLICT` ("ambiguous or divergent history barriers later revisions"). Evaluated before any PR-identity requirement, because a divergence terminal may legitimately carry `PRNumber == 0`.
7. Confirmed-only bindings: `ResultCore.PRNumber > 0`, `ResultCore.PRNodeID != ""`, equal to `Core.PullRequest.Number`/`NodeID`, and `ResultCore.Repository`/`BaseBranch`/`HeadBranch` equal the live `request.Authority` repository/base/head. This binds the barrier to the live authority's physical resource beyond the key digest alone.

Bounded reads: terminal ≤ `MaxTerminalBytes` (256 KiB), generation ≤ 128 KiB, marker ≤ 32 KiB, revision ≤ 128 KiB — constant, independent of history length, at most one terminal read per `Upsert`.

The remote half of the proof is unchanged (`controller.go:414-431`, `:466`): direct full-PR read at the barrier number, node-ID equality, `validateExactPR` (state open, not merged, exact repository/base/head/label/head-SHA), then `previous.Core.Title`/`Body`/`PRNumber`/`PRNodeID` equality against the observation.

## 5. How same-request re-entry receives it

Re-entry is the `default` arm: `maxOrdinal` owns `revision.json` with the same `RequestSHA256`, no `generation.json`, no `terminal.json`, no `superseded.json`. It now:

1. loads and gates the barrier exactly as a new ordinal does;
2. requires the stored immutable revision to agree with the barrier before any network call — `revision.Mode == "UPDATE"`, `revision.PRNumber == previous.Core.ResultCore.PRNumber`, `revision.PRNodeID == previous.Core.ResultCore.PRNodeID` — else `ADMISSION_INTEGRITY_FAILURE` with zero remote reads. This is precisely the state a pre-fix binary could have persisted (a CREATE revision above a confirmed barrier); it is refused rather than resurrected;
3. passes `previous` into `prepare`, so the direct PR read and the prior title/body proof run before `makeRevision`/`submit`.

When the barrier is absent (`barrierOrdinal == 0`, e.g. the first revision of a resource), behaviour is exactly today's: `previous == nil`, discovery fallback.

## 6. How resume receives it

`resumeGeneration` takes the scan state and threads the barrier into its prepare:

- **Marker present** (`controller.go:570-588`): reconcile-only, read-only, no mutation authority ⇒ no barrier load, no gate, unchanged.
- **Marker securely absent**: the generation was allocated but `http.Client.Do` was never reached for this ordinal, so the remote document is still the barrier's. Therefore:
  1. `previous = c.barrierTerminal(tx, state, request.Authority)` — load and gate;
  2. before `nextLegacyRound`/`prepare`, require `previous == nil || (revision.Mode == "UPDATE" && revision.PRNumber == previous…PRNumber && revision.PRNodeID == previous…PRNodeID)`, else `ADMISSION_INTEGRITY_FAILURE` with zero remote reads;
  3. `c.prepare(ctx, key, revision.Ordinal, resumeRound, "resume", request, previous)` — replaces the literal `nil` at `controller.go:593`.

The immutable-generation guard in `submit(resumed=true)` (`controller.go:524-534`) is untouched and still requires the re-derived attempt bytes, authority digest, mode, and PR identity to equal the stored allocation, so resume can never switch CREATE↔UPDATE or retarget a PR.

## 7. How abandoned revisions are handled

An ordinal owning `revision.json` with no `generation.json` performed **no** remote write and holds no mutation outcome. It is:

- **never** a barrier — `barrierOrdinal` is computed over `terminal.json` only, so it cannot hide a lower confirmed terminal;
- **the only ordinal permitted to be unsettled**, and only when it is `maxOrdinal` (Pass 2);
- **settleable** by a differing request through the existing zero-write `superseded.json` (`controller.go:311-319`), now written only after the barrier has been locally loaded and gated;
- **re-enterable** only by the identical request, and only under §5's barrier proof;
- **permanently closed** once `superseded.json` exists: re-entry by the superseded request returns `REVISION_CONFLICT` rather than resurrecting it into a generation. This closes the residual window where a supersede succeeded, the successor `revision.json` creation failed, and the original request could re-enter `maxOrdinal` and submit a write that a differing request had already displaced. Differing requests still make progress — the `superseded.json` create is byte-identical and therefore idempotent (`store_linux.go:258-263`), and the ordinal advances to `maxOrdinal + 1`.

## 8. Failure classes

All are zero-remote-write and `Submitted=false` unless noted. None deletes or rewrites a durable record.

| Condition | Code | Where |
| --- | --- | --- |
| malformed ordinal; duplicate structural record; missing `revision` for a structural kind; `submitted`/`terminal` without `generation`; `terminal` without `submitted`; `superseded`+`generation`; ordinal gap; unsettled ordinal below `maxOrdinal`; `barrierOrdinal > maxOrdinal` | `ADMISSION_INTEGRITY_FAILURE` | `scanResource` |
| barrier terminal unreadable, non-canonical, broken core/event digest, or chain/revision/generation binding mismatch | `ADMISSION_INTEGRITY_FAILURE` | `barrierTerminal` 1-3,5 |
| barrier policy or limits identity differs from live | `ADMISSION_POLICY_MISMATCH` | `barrierTerminal` 4 |
| barrier disposition is `applied_reconciled` or `remote_diverged_after_write` | `REVISION_CONFLICT` | `barrierTerminal` 6 |
| confirmed barrier lacks PR identity or its repository/base/head differ from live authority | `ADMISSION_INTEGRITY_FAILURE` | `barrierTerminal` 7 |
| `maxOrdinal` has a generation and no terminal, and the request differs | `REVISION_CONFLICT` | dispatch |
| re-entry at a superseded ordinal | `REVISION_CONFLICT` | dispatch |
| stored revision at a re-entered/resumed ordinal disagrees with the barrier's mode/PR identity | `ADMISSION_INTEGRITY_FAILURE` | §5 / §6 (pre-network) |
| barrier-bound PR read fails (403/404/5xx/transport), number/node mismatch, or `priorNumber <= 0` | `REVISION_CONFLICT` | `controller.go:418` |
| barrier-bound PR fails the exact-open predicate (closed, merged, repository/ref/label/head-SHA divergence) | `EXISTING_INELIGIBLE_OPEN_PR` | `controller.go:463` |
| barrier document (title/body) or PR identity changed remotely | `REVISION_CONFLICT` | `controller.go:467` |
| authority-bound PR present but different from the confirmed barrier's PR | `REVISION_CONFLICT` | `controller.go:466` |
| `state.barrierOrdinal >= ordinal` | `ADMISSION_INTEGRITY_FAILURE` | dispatch assertion |

A transient remote failure on the barrier PR read consumes one run-scoped prepare round (or one resume round) and returns fail-closed; it is not a permanent block, because prepare-round budget is run-scoped per the round-2 correction and a later governed run receives a fresh budget.

## 9. Adversarial regression matrix

New tests live in `internal/prlifecycle/review3_linux_test.go` and reuse `lifecycleAuthority`, `provisionStore`, `makeCorrectionController`, `makeRawRunController`, `review2Fixture`, and `countPrepareRecords`. The abandoned-revision window is produced deterministically with the existing `controller.github.writeConstructionHook` (fails inside `prepareWrite`, i.e. after `revision.json` at `:356` and before `generation.json` at `:536`). The generation-without-marker window uses a new unexported `Controller.beforeMarker func() error` fault boundary, mirroring `postSubmitFailure`, checked immediately before `tx.createJSON(markerName, …)` and nil in production.

**Primary — the reported Critical**

- [x] rev1 `applied_confirmed` on PR #7 → rev2 abandoned between `revision.json` and `generation.json` → PR #7 closed remotely → third differing request: fails closed (`EXISTING_INELIGIBLE_OPEN_PR`), fixture `writes == 1`, **no `POST /repos/{owner}/{repo}/pulls` ever observed**, `listReads` unchanged, no `rev-3-generation.json`/`submitted.json`/`terminal.json`; store gains exactly `rev-2-superseded.json` plus one `rev-3` prepare record.
- [x] Same setup, PR #7 left open and unmodified: third differing request succeeds as **UPDATE on #7** at ordinal 3, `writes == 2`, `listReads` unchanged, and every `*-terminal.json` for the resource binds the same `PRNumber`/`PRNodeID`.
- [x] Same setup, PR #7 title edited remotely: `REVISION_CONFLICT`, `writes == 1`, zero `POST`.
- [x] Explicit `superseded.json` assertions (the review noted no test greps for it): the abandoned ordinal owns exactly `revision.json` + `superseded.json`, never a generation, and re-supersede by another differing request is byte-identical/idempotent.

**Same-request re-entry**

- [x] rev1 confirmed → differing request creates `rev-2-revision.json` then fails at `prepareWrite` → hook cleared → the **same** second request re-enters: with PR #7 remotely edited ⇒ `REVISION_CONFLICT`; with PR #7 closed ⇒ `EXISTING_INELIGIBLE_OPEN_PR` and no `POST /pulls`; unmodified ⇒ succeeds as UPDATE at ordinal 2 with `writes == 2` and `listReads` unchanged.
- [x] A pre-fix-shaped `rev-2-revision.json` with `Mode == "CREATE"` above a confirmed barrier is refused with `ADMISSION_INTEGRITY_FAILURE` and **zero GitHub requests**.
- [x] Re-entry with no barrier at all (first revision of a resource) still takes the discovery fallback — no regression for the no-prior-terminal case.

**Resume**

- [x] rev1 confirmed → rev2 generation allocated with `beforeMarker` failing → PR #7 title edited remotely → same request resumes: `REVISION_CONFLICT`, no `submitted.json` for rev2, `writes == 1`.
- [x] Same construction, PR #7 untouched: resume completes as UPDATE, `writes == 2`, exactly one `submitted.json` for rev2, `listReads` unchanged (resume now issues no discovery call).
- [x] Marker-present resume (`corrections_linux_test.go:275` shape) still reconciles read-only with **no barrier load**: with the barrier terminal made non-confirmed or corrupt, reconciliation still returns `RECONCILIATION_BUDGET_EXHAUSTED`, not an integrity/conflict error.

**Barrier selection, authentication, gating**

- [x] Non-confirmed barrier is not bypassable by an abandoned ordinal: rev1 `remote_diverged_after_write` (principal-mismatch fixture) → rev2 abandoned → third differing request ⇒ `REVISION_CONFLICT`, zero GitHub writes; and the exact replay of rev1's request still returns the identical `TerminalSHA256()` with zero GitHub requests because rev2 has no generation/submitted/terminal.
- [x] Lower-barrier replay is forbidden once any later ordinal owns `generation.json`: rev1 terminal → rev2 generation allocated (with or without marker) → request matching rev1 must not recover rev1; it follows the active-generation conflict/reconciliation path with zero replay of the older terminal.
- [x] Barrier authentication at an ordinal **below** `maxOrdinal`: independently tamper the barrier's `generation.json`, `submitted.json`, `revision.json`, and `terminal.json` bytes (names left grammar-valid) ⇒ `ADMISSION_INTEGRITY_FAILURE` with zero GitHub requests in each case.
- [x] Barrier `PolicySHA256`/`LimitsSHA256` mismatch ⇒ `ADMISSION_POLICY_MISMATCH`, zero GitHub requests.
- [x] Barrier whose `ResultCore.Repository`/`BaseBranch`/`HeadBranch` do not match the live authority ⇒ `ADMISSION_INTEGRITY_FAILURE`.
- [x] Exactly one `terminal.json` read per `Upsert` on the barrier path, and a barrier-driven prepare issues no `GET …/pulls?` (assert `listReads` and per-path request counts).

**Structural scan**

- [x] Each of: generation-without-terminal at an ordinal below `maxOrdinal`; `submitted` without `generation`; `terminal` without `submitted`; revision-ordinal gap (`rev-1`, `rev-3`); `superseded` + `generation` at one ordinal ⇒ `ADMISSION_INTEGRITY_FAILURE` with zero GitHub requests.
- [x] Prepare-only ordinals (24 seeded records, no `revision.json`) do not enter resource state — `PREFLIGHT_HISTORY_EXHAUSTED` behaviour is unchanged.
- [x] A non-matching leftover (`…-terminal.json.saved`) does not fail the read-only paths, and the first path that creates a file still fails closed via `inventory`.

**Superseded resurrection**

- [x] rev1 confirmed → rev2 abandoned → differing request supersedes rev2 and then fails at prepare (no `rev-3-revision.json`) → re-issuing the **rev2** request ⇒ `REVISION_CONFLICT`, zero writes, no `rev-2-generation.json`; a further differing request still advances to ordinal 3.

**Unchanged regressions must stay green**

- [x] One-submission/no-replay, descriptor-relative locking and inode recheck, two-phase reconciliation with 8-round/interval/aggregate bounds, divergence terminals, cross-run terminal recovery and evidence republish, sealed authenticated transport and header/body caps, CREATE list-vs-full identity, persisted-diagnostic sanitization, capacity/reservation accounting, run-scoped prepare history and the 24-record ceiling, `TerminalBudgetV1`, material-ledger idempotence, and the non-Linux fail-closed build.

## 10. Verification

- `gofmt -w` on changed Go files
- `go test ./internal/prlifecycle` (focused)
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `make smoke`
- `git diff --check 35faeec8b8314519ee6b9d36eba32a0c25ac6050 HEAD`
- Commit only after every validation passes.

## Completion gate

Ralphex completion is not push-readiness. The exact correction head must pass ABCP deterministic acceptance and then a fresh exact-head Critical/Major post-implementation review (Claude/cross-model preferred; controller fallback only for genuine provider failure per `docs/architecture/DECISION_AND_ACCEPTANCE_POLICY.md`). Any substantive Critical or Major finding requires another governed correction cycle. No branch push, PR #8 creation, merge, CI lifecycle work, or later Phase-4 task is authorized until the exact reviewed correction head is clean.

---

# IMPLEMENTABLE_PLAN

Land this document as `docs/plans/ep-005-pr-lifecycle-review-corrections-3.md` first (it is the governing plan), then implement.

### Task 1: Correct the resource-scoped prior-confirmed PR barrier

- [x] Implement Steps 1–5 below exactly as designed, including bounded lower-barrier exact replay only across provably zero-write later ordinals.
- [x] Implement every adversarial regression in §9, including abandoned revision, same-request re-entry, resume, barrier authentication, structural scan, lower-barrier replay blocking, and superseded resurrection.
- [x] Preserve every listed non-goal and all previously accepted one-submission/no-replay, exact-head, locking, recovery, evidence, transport, budget, and ledger invariants.
- [x] Run every validation in §10 and require all to pass.
- [x] Mark satisfied plan checkboxes, commit the implementation only after validation, and move this plan to `docs/plans/completed/`.

### Step 1 — `internal/prlifecycle/store_linux.go`

1. Wrap the kind alternation of `admissionName` (line 19) in a third capture group, exactly as printed in §2. Do not change which names match. Verify capture indices 1 and 2 are unchanged so `nextPrepareRound` (`terminal.go:569`) and `inventory` (`store_linux.go:369`) need no edit.

### Step 2 — `internal/prlifecycle/terminal.go`

2. Add the `kind*` flag constants and `resourceStateV1` from §1, with helpers `has(ordinal, mask) bool`, `unresolved() bool`, and `zeroWriteAfterBarrier() bool`. The last helper returns false if any ordinal greater than `barrierOrdinal` owns `generation`, `submitted`, or `terminal`.
3. Add `scanResource(tx *resourceTxn) (resourceStateV1, error)` implementing Pass 1 + Pass 2 exactly as specified. Skip non-matching names and non-structural kinds; return `&Error{Code: CodeIntegrityFailure, …}` on every Pass-2 violation.
4. **Delete `latestRevision`** (lines 503-523) — `scanResource` is now the single source of resource state.
5. Add `func (c *Controller) barrierTerminal(tx *resourceTxn, state resourceStateV1, authority githublifecycle.Authority) (*terminalV1, error)` implementing §4 steps 1-7. Return `(nil, nil)` when `state.barrierOrdinal == 0`. Do **not** call `recoverTerminal`; perform no publish and no ledger record.

### Step 3 — `internal/prlifecycle/controller.go`

6. Add the unexported fault boundary `beforeMarker func() error` to `Controller` beside `postSubmitFailure` (line 40), with the same doc comment style. In `submit`, immediately before `tx.createJSON(markerName, marker, true, false)` (line 544), call it and return its error unchanged if non-nil. It is never set by `newController`.
7. Replace `controller.go:265-331` with the §1 dispatch. Preserve the current-max exact replay unchanged, then add the bounded lower-barrier exact-replay check exactly as specified: only when `barrierOrdinal < maxOrdinal` and `zeroWriteAfterBarrier()` is true, read the barrier `revision.json`; if its `RequestSHA256` matches, call `recoverTerminal` for `barrierOrdinal`. Never lower-replay when any later generation exists. Preserve verbatim the `superseded.json` payload/reason, `nextPrepareRound`, prepare-record construction/size/create, `makeRevision`, `revision.json` creation, and `submit` call.
8. Remove the now-duplicated inline barrier logic at `:294-309` (`readTerminal`, `readGenerationAndMarker`, disposition check) — `barrierTerminal` owns it.
9. Add the pre-network revision/barrier agreement guard on the same-request re-entry arm (§5.2).
10. Add `assert state.barrierOrdinal < ordinal` before `nextPrepareRound`.
11. Change `resumeGeneration` to `(ctx, tx, request, revision revisionRecord, state resourceStateV1)`. Leave the marker-present branch untouched. In the marker-absent branch, load the barrier, apply the §6.2 guard **before** `nextLegacyRound`, and pass `previous` to `c.prepare(…)` in place of the literal `nil` at line 593.
12. Leave `prepare` (`:386-479`), `submit`, `reconcile`, `terminalize`, `makeRevision`, and every remote call site otherwise unchanged.

### Step 4 — `internal/prlifecycle/review3_linux_test.go` (new)

13. Implement every unchecked box in §9. Extend `review2Fixture` additively with per-path counters (`prCreates`, `prReads`) so `POST /repos/octo/control/pulls` can be asserted as never issued; keep existing fields and behaviour so `review2_linux_test.go` stays green.
14. Build the abandoned-revision state with `writeConstructionHook`, the generation-without-marker state with `beforeMarker`, and tamper states with `os.WriteFile` on grammar-valid names inside `store.Root()`.

### Step 5 — documentation

15. Additively record the resource-scoped barrier semantics in `docs/plans/completed/ep-005-pr-lifecycle.md`'s post-implementation correction note and the state/failure matrix row for "different request meets active revision". Do not erase prior review history.

### Step 6 — validation

16. Run §10 in order. Commit only after all pass, then move the plan to `docs/plans/completed/`.

### Non-goals (must not appear in the diff)

CI ingestion, merge execution or authorization, domain-state transitions, Phase-5+ behaviour, new GitHub endpoints or query parameters, changes to `internal/githublifecycle`, schema-version bumps, terminal/result-core field additions, new admission record kinds, and any deletion, compaction, or rewrite of admission records.

DESIGN_READY_FOR_GATE

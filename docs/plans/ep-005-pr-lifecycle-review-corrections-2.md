# EP-005 — Exact-Head PR Lifecycle Post-Implementation Review Corrections Round 2

## Authority and scope

This correction track starts from ABCP-accepted correction head `d4e7e1d8fc6af9545f9f67e3c99fa94c3420a7ce`. The preceding correction implementation passed deterministic ABCP acceptance, but the required fresh Claude Code / Opus exact-head Critical/Major review found two Critical and four Major defects. Those findings are substantive and block branch publication and PR #8.

Claude review artifact: `/home/devagent/abcp-runtime/ep006-pr-lifecycle-review-corrections/post-implementation-claude/review.txt`
Artifact SHA-256: `d07f9124d771a38caff86b5c3283a70c35d5fe71379b329b1513416392c99421`
Reviewed exact SHA: `d4e7e1d8fc6af9545f9f67e3c99fa94c3420a7ce`

This round corrects only the six review findings below. It adds no CI ingestion, merge execution, domain transition, service/API/dashboard, Phase 5+ behavior, or broader GitHub capability.

## Correction invariants

### 1. Request construction and observable document bounds are complete before submitted authority is consumed — CRITICAL

All deterministic request construction that can fail must complete before `submitted.json` publication, terminal reservation consumption, or any call that may invoke `http.Client.Do`. The PR-lifecycle layer defines `PRLifecycleDocumentMaxBytes = 1024` and uses that stricter controller-owned bound per title and per body, equal to `maxLifecycleRemoteText` / `lifecycleRemoteTextLimit`. This narrows only `internal/prlifecycle` admission and does not change frozen `githublifecycle.DefaultLimits().MaxTextBytes = 4096`.

- Reject a title or body over 1024 bytes as a zero-write preflight failure before generation/marker creation. Title remains non-empty under existing semantics.
- Structurally assert the admitted document bound is `<= lifecycleRemoteTextLimit`, so every admitted CREATE/UPDATE document can be decoded from the provider response.
- Build and JSON-marshal the exact canonical CREATE/UPDATE request body before marker publication.
- Enforce `MaxRequestBytes` against the encoded bytes, including JSON escaping expansion; prove an escape-maximal 1024-byte title plus 1024-byte body remains within the 16-KiB request cap.
- Build the exact HTTP request before marker publication when request construction itself can fail.
- A pre-submit construction/size failure is a zero-write preflight failure and must leave no submitted marker.
- After marker publication, failures must distinguish a request proven never submitted from a request whose transport outcome is submitted/ambiguous. A proven-never-submitted post-marker local failure does **not** authorize replay in this correction track; it returns a fail-closed controller error and requires a later governed correction/recovery path.
- `applied_reconciled` may never be selected solely because a locally constructed request failed before network submission.

### 2. Principal read failures remain ambiguous; observed identity divergence is recoverable — CRITICAL

A reconciliation-round failure to read/parse the authenticated principal is not evidence of remote divergence.

- `principalErr` must participate in ambiguity guards before any divergence classification.
- A failed principal read consumes the already-started reconciliation round but does not terminalize; later bounded reconciliation rounds remain available.
- An observed authenticated-principal mismatch is a legitimate `remote_diverged_after_write` condition.
- Terminal authority identity remains bound to the authority-derived actor ID, not the mismatching observed principal ID. `ResultCore.PrincipalID` is the authority-derived numeric actor ID. `ResultCore.PrincipalNodeID` / `PrincipalLogin` may be populated only from a principal observation whose numeric ID has already matched that authority; for a principal-mismatch divergence they remain empty rather than forming a mixed identity tuple.
- The complete mismatching observed principal tuple is retained only in the bounded terminal `Principal` observation evidence.
- A divergence terminal caused by principal mismatch must recover across governed runs to the same unresolved/no-retry result with zero GitHub writes.

### 3. Every post-submit failure preserves submitted provenance — MAJOR

Once `http.Client.Do` may have been invoked, every propagated failure must use a controller-owned error class with `Submitted=true` and the generation/write-attempt identity.

This includes terminal observation sizing, reconciliation material sizing, result-core construction, evidence capture, admission record creation, terminal publication, and material-ledger recording. Raw transport/provider text remains excluded.

No post-submit path may return a bare error that a caller can misclassify as a local zero-write failure.

### 4. Prior confirmed PR identity must be re-proven before a later revision — MAJOR

When a previous terminal is `applied_confirmed`, a later revision must prove the same terminal-bound PR number/node ID and repository/base/head identity before any new mutation authority is admitted.

- Do not skip the proof merely because filtered open-PR discovery returns zero candidates.
- Drive a direct full-PR read using the previous terminal's exact PR number, then verify node ID, repository, base/head labels/branches, prior title/body, and other frozen authority fields required by the completed plan.
- If the terminal-bound PR is absent, closed/merged, or identity/document proof fails, fail closed with zero new PR submission.
- A later revision must never silently switch from UPDATE to CREATE because the previously confirmed PR is no longer discoverable as open.

### 5. Prepare-round exhaustion is run-scoped but globally bounded per pending revision — MAJOR

Pre-submit prepare/read failures consume bounded read authority for the current governed run, not permanent write authority for the physical PR resource, while immutable history must not grow without bound.

- Persist prepare records as `r-<resource>-rev-<ordinal>-prepare-run-<run_sha256>-<round>.json`, where `<run_sha256>` is the fixed 64-lower-hex SHA-256 of the raw governed `RunID`; never place raw caller-controlled RunID text in a filename.
- Bind the exact raw `RunID`, run digest, resource key, revision ordinal and round inside the canonical record body.
- Amend `admissionName` and every exact parser to admit only that fixed grammar. Replace `nextLegacyRound` for prepare with a dedicated parser that fails closed on malformed or inconsistent records; it must never silently coerce parse failure to round 1.
- Restart of the same governed run preserves its monotonic three-round budget. A different governed run gets a fresh three-round budget only when no generation/submitted marker exists.
- Define `MaxPrepareHistoryRecordsPerPendingRevision = 24` (eight fully exhausted governed runs × three rounds) for the same physical resource + candidate revision ordinal. Enforce this resource-local ceiling **before** root-wide capacity projection. When reached, return controller code `PREFLIGHT_HISTORY_EXHAUSTED` for that resource/revision and create no additional file.
- Once a revision is successfully created, the next revision ordinal has its own bounded prepare-history allowance; prior records remain immutable provenance and are never deleted or overwritten.
- This 24-record ceiling ensures one persistently failing resource/revision has finite admission-root growth and cannot by itself consume the 65,536-file global policy. Root-wide `MaxAdmissionFiles`, `MaxAdmissionBytes`, filesystem headroom and resource-lock rules remain additional fail-closed bounds.

### 6. TerminalBudgetV1 is an encoded upper bound, not a heuristic — MAJOR

The pre-submit terminal reservation must conservatively upper-bound the actual canonical persisted terminal for every reachable disposition. The effective PR-lifecycle document contract is the 1024-byte-per-field bound from invariant 1; the 4096-byte foundation limit is not an admitted PR-lifecycle maximum.

- Compute the document budget from canonical JSON encoded length, including worst-case HTML escaping, not raw `len(title)+len(body)`. Escape-maximal 1024-byte title and 1024-byte body are the maximal admitted document case.
- Enumerate and budget every persisted document copy: `terminalCoreV1.Title`/`Body`, the normalized `PullRequest` observation, reconciliation terminal material when it carries the PR observation, and retained `EvidenceArtifacts[].Bytes` when it carries the same normalized observation. No copy may be hidden inside unexplained slack.
- Itemize source authority (16 KiB), derived authority (16 KiB), attempt (8 KiB), snapshot (16 KiB), principal observation (8 KiB), PR observation (32 KiB), head+base ref observations (16 KiB total), reconciliation (32 KiB), retained terminal artifact (32 KiB), result core (16 KiB), deterministic event/reason/digests/framing, plus the exact encoded document term. If implementation de-duplicates a copy, the budget and tests must bind the resulting schema explicitly.
- Enforce principal observation `<= 8 KiB` before terminal construction; every other component must be checked against the exact cap used by the budget.
- `WorstCaseBytes` must be a provable upper bound and `WorstCaseBytes <= MaxTerminalBytes (256 KiB)` before marker publication.
- Escape-maximal strings exactly at the 1024-byte admitted bound must fit the computed reservation; any title/body above 1024 bytes or any other input profile that cannot fit must be rejected before marker publication and before `http.Client.Do`.

## Required adversarial regression matrix

- [x] Title/body at exactly 1024 bytes each round-trip through CREATE and UPDATE; 1025 bytes in either field is rejected before generation/marker creation and before any HTTP call.
- [x] Structural regression proves PR-lifecycle admitted document bound `<= lifecycleRemoteTextLimit` and escape-maximal admitted request bytes `<= MaxRequestBytes`.
- [x] Pre-submit request-construction failure leaves no submitted marker and cannot select `applied_reconciled`.
- [x] Principal read 5xx/403/malformed response after a submitted write consumes one reconciliation round, returns ambiguous, writes no divergence terminal, and allows later bounded reconciliation.
- [x] Observed principal-ID mismatch creates a durable divergence terminal that recovers on a later governed run with zero GitHub writes.
- [x] Every injected post-`Do` terminal/evidence/admission/ledger failure returns a controller-owned `*Error` with `Submitted=true`, non-empty attempt identity, **non-empty controller-owned `Code`**, and no raw provider text.
- [x] Previous `applied_confirmed` PR closed before a later revision causes zero-write fail-closed behavior; no replacement CREATE is submitted.
- [x] Previous confirmed PR direct-read node-ID/repository/ref/document mismatch fails closed before mutation.
- [x] Three prepare failures exhaust run A only; run B gets a fresh three-round budget when no submitted generation exists.
- [x] Prepare filenames contain only the fixed run SHA-256; hostile/long RunID values cannot alter grammar, escape the root, or create unknown inventory entries, and raw RunID is bound inside the record.
- [x] Restart of run A does not reset its prepare-round budget; malformed prepare history fails closed rather than resetting to round 1.
- [x] After 24 prepare records for one resource + pending revision ordinal, the next run returns `PREFLIGHT_HISTORY_EXHAUSTED` without creating a file; an unrelated resource can still create records under the same global policy.
- [x] After a successful revision, the next revision ordinal receives an independent bounded prepare-history allowance while prior records remain immutable.
- [x] Escape-maximal title/body at the 1024-byte admitted bound plus maximal principal/PR/ref/reconciliation/artifact material prove canonical terminal bytes `<= WorstCaseBytes <= 256 KiB`; 1025-byte title/body are rejected pre-submit.
- [x] Principal observation above its configured bound is rejected before terminal publication with submitted provenance preserved when post-write.
- [x] Existing one-submission/no-replay, descriptor locking, two-phase reconciliation, divergence, cross-run recovery, evidence-reader, sealed-transport, CREATE identity, sanitization, capacity, material-ledger, and unsupported-platform regressions remain green.

### Task 1: Correct the second exact-head PR lifecycle review findings

- [x] Implement only the six correction invariants above without changing frozen `internal/githublifecycle` semantics unless a regression proves an unavoidable foundation defect.
- [x] Add every adversarial regression above.
- [x] Preserve exactly one remote PR submission per revision and all existing fail-closed barriers.
- [x] Update completed lifecycle documentation only additively where corrected semantics require it; preserve prior review history.
- [x] Run `gofmt -w` on changed Go files.
- [x] Run focused `go test ./internal/prlifecycle`.
- [x] Run `go test ./...`.
- [x] Run `go test -race ./...`.
- [x] Run `go vet ./...`.
- [x] Run `make smoke`.
- [x] Run `git diff --check d4e7e1d8fc6af9545f9f67e3c99fa94c3420a7ce HEAD`.
- [x] Commit only after every required validation passes.

## Completion gate

This round is not push-ready merely because Ralphex completes. The exact correction head must pass ABCP deterministic acceptance and then a fresh exact-head Critical/Major post-implementation review. Claude/cross-model review is preferred when available; controller fallback is permitted only for genuine provider failure under `docs/architecture/DECISION_AND_ACCEPTANCE_POLICY.md`.

Any substantive Critical or Major finding requires another governed correction cycle. No branch push, PR #8 creation, merge, CI lifecycle work, or later Phase-4 task is authorized until the exact reviewed correction head is clean.

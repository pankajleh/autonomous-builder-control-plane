# EP-006 Combined Crash-Consistency Five-Major Correction

## Authority

Fresh cumulative candidate `c42712fab55af985b6655039ad1cacb90deafbb0` (tree `5227415e0ae98f0978614e5d4e22ceb636cf45fb`) passed fresh cumulative deterministic A-D acceptance but failed the mandatory independent exact-head source review with exactly 0 Critical / 5 Major / 2 Minor.
Phase-5 base remains `c4f899072e31364f81453b2a5d6d90147c774107`; governing accepted EP-006 design remains `908710e1606b0da761e612c36b85c866e182c7f7`; accepted A/B/C checkpoints remain ancestors. Review artifact SHA-256: `caa2d82cc8cc64b1e6303bd309b424f36ab00477acc883d774db52c6770f0589`.
The verdict is `IMPLEMENTATION_FINDINGS`, not a design gap. This B-correction authorizes exactly the five Majors below. The known malformed-cancel HTTP-classification Minor remains deferred. The review-packet manifest Minor is evidence-pack construction only and must be corrected in the next runtime evidence packet, not by widening repository product scope.

## Maximum mutation authority

```text
internal/actioncontrol/journal_linux.go
internal/actioncontrol/journal_linux_test.go
internal/actioncontrol/resource_guard_linux.go
internal/actioncontrol/resource_guard_linux_test.go
internal/actioncontrol/watcher.go
internal/actioncontrol/watcher_linux_test.go
internal/ledger/jsonl.go
internal/ledger/jsonl_test.go
internal/runtimecatalog/catalog_linux.go
internal/runtimecatalog/catalog_linux_test.go
this plan and its completed-plan move
```

Frozen: `internal/serviceapi/**`, `internal/actionapi/**`, `internal/readmodel/**`, `internal/evidence/**`, `internal/timeline/**`, `internal/recovery/**`, `internal/supervisor/**`, `internal/githublifecycle/**`, `internal/mergelifecycle/**`, `cmd/abcp/**`, accepted architecture/design/execution-pack documents, operational projections, and every path not listed above.

### Task 1: make action-run generation issuance crash-consistent

- [x] Replace the directory-before-history crash window with one root-authorized prepared/two-phase issuance protocol that can deterministically complete or roll back the exact prepared `actions/<run>` physical generation after process termination without adopting an unproven inode.
- [x] Fresh-process recovery must safely handle termination before directory publication, after durable directory publication but before history append, during a short/partial history write, after a complete uncheckpointed record, and around the root-authority checkpoint; previously committed run generations and decision-request/outcome uniqueness must remain readable.
- [x] Add deterministic cross-process crash-injection/reopen tests for every issuance boundary and prove no orphan/partial record can permanently brick `Journal.Open` or reissue a run namespace.

### Task 2: close descriptors on rejected registered-ledger opens

- [x] Every `openJSONLLedger` constructor failure after pinning a ledger file must close both the pinned ledger descriptor and parent descriptor exactly once; a failed constructor must leave no unreachable OS file descriptor.
- [x] Preserve registered-generation rejection, standalone constructor behavior, snapshot/ledger resource accounting, and existing close semantics.
- [x] Add repeated rejected-open tests, including coherently replaced registered generations and race-enabled loops, proving process descriptor count returns to baseline and the logical 8-ledger ceiling cannot be bypassed by failed opens.

### Task 3: preserve live watcher liveness after durable reconciliation-required

- [x] Once an ambiguous delivery has durably recorded `RECONCILIATION_REQUIRED` and the one bounded reconciliation attempt has completed without an integrity/authority failure, treat the operation as handled for watcher-loop liveness instead of propagating the original append/fsync error as a fatal watcher error.
- [x] Keep true journal/catalog/integrity/authority failures fatal; do not synthesize terminal success, do not resend cancellation, do not add recovery/resume authority, and preserve owner-generation/watermark isolation.
- [x] Add an end-to-end test using the same started production watcher: ambiguous append -> durable `RECONCILIATION_REQUIRED` -> watcher remains live -> shutdown drain -> exact `CLOSING -> RETIRED`; retain negative tests showing `RECEIVED`/`CLAIMED` and real integrity failures still block/stop appropriately.

### Task 4: make runtime-catalog run-namespace issuance crash-consistent

- [x] Replace durable run/attempt/identity publication before `.run-generations.jsonl` authority with a catalog-root-authorized prepared/two-phase issuance protocol that binds the exact intended run/attempt/identity physical generation before it can become an unrecoverable orphan.
- [x] Fresh-process recovery must safely complete or roll back exact prepared state after termination at every boundary, including partial authority records, without accepting a coherent replacement or changing create-only/byte-verify registration semantics.
- [x] Add deterministic crash/reopen races proving listing, `ReadRun`, `RegisterRun`, attempts, and owner installation remain available after recoverable interruption while replacement/tamper still fails closed.

### Task 5: make initial action/resource generation bootstrap crash-recoverable

- [x] Make the initial journal namespace identity-anchor bootstrap recoverable when termination occurs between `O_EXCL` creation and complete durable identity publication; the root `INITIALIZING` authority must bind enough prepared identity to finish or safely roll back the exact inode without reissuance.
- [x] Apply the same recoverable root-authorized transaction discipline to the D-owned resource guard directory anchor and ledger/snapshot slot identity files; a fresh process must recover zero/partial prepared identity files without weakening the immutable ready-generation guarantee.
- [x] Add cross-process crash-injection tests for every initial identity publication boundary and prove repeated startup converges to exactly one generation, while foreign/replaced/mismatched identity files remain integrity failures.

### Task 6: cumulative validation and completion

- [x] Run focused normal/race tests for every touched package and stress each of the five Major-specific crash/leak/liveness regressions at least 10 repetitions where practical.
- [x] Rerun all previously sealed EP-006 combined and correction race groups, full `go test -count=1 -race ./...`, full uncached `go test -count=1 ./...`, `go vet ./...`, Darwin/Windows production builds, and `git diff --check`.
- [x] Prove accepted design docs are byte-unchanged; A/B/C, blocked D, `49d418d`, and `c42712f` remain ancestors; correction diff from `c42712f...HEAD` contains only maximum-authority paths; cumulative Phase-5 semantics remain unchanged except these five implementation closures.
- [x] Complete all checkboxes, move this plan to `docs/plans/completed/`, leave a clean worktree, and commit the bounded correction.

## Completion boundary

Ralphex completion is implementation evidence only. The new exact head must undergo a fresh cumulative deterministic A-D acceptance with a corrected non-self-referential evidence manifest, followed by a fresh independent combined exact-head Critical/Major review. Publication/PR/merge remains forbidden until that review is exactly 0 Critical / 0 Major.

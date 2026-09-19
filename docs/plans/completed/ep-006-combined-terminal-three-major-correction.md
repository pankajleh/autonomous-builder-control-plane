# EP-006 Combined Terminal Three-Major Correction

## Authority

Fresh corrected cumulative candidate `49d418d06f17eb14c108a24b303c532d2b2b09d2` (tree `9c36870508ab181b7384aae57a35c030588636ff`) passed fresh cumulative deterministic A-D acceptance but failed the mandatory independent exact-head source review with exactly 0 Critical / 3 Major / 1 known Minor.
Phase-5 base remains `c4f899072e31364f81453b2a5d6d90147c774107`; governing accepted EP-006 design remains `908710e1606b0da761e612c36b85c866e182c7f7`; accepted A/B/C checkpoints remain ancestors.
The review verdict is `IMPLEMENTATION_FINDINGS`, not a design gap. This B-correction authorizes exactly the three Majors below. The known malformed-cancel HTTP-classification Minor remains deferred and must not reopen `internal/serviceapi/**`.

## Maximum mutation authority

```text
internal/actioncontrol/journal_linux.go
internal/actioncontrol/journal_linux_test.go
internal/actioncontrol/journal_unsupported.go
internal/actioncontrol/watcher.go
internal/actioncontrol/*_test.go only when directly required by Task 3
internal/runtimecatalog/types.go
internal/runtimecatalog/catalog_linux.go
internal/runtimecatalog/catalog_linux_test.go
internal/runtimecatalog/catalog_unsupported.go only if required for API parity
internal/ledger/jsonl.go
internal/ledger/jsonl_test.go
internal/ledger/readonly.go
internal/ledger/readonly_test.go
internal/ledger/run_transition_namespace_linux.go only if required to consume the same immutable registered generation
internal/ledger/run_transition_namespace_linux_test.go only if directly required by Task 2
internal/readmodel/types.go
internal/readmodel/service.go
internal/readmodel/readmodel_test.go
cmd/abcp/main.go
cmd/abcp/main_test.go
this plan and its completed-plan move
```

Frozen: `internal/serviceapi/**`, `internal/evidence/**`, `internal/timeline/**`, `internal/recovery/**`, `internal/supervisor/**`, `internal/githublifecycle/**`, `internal/mergelifecycle/**`, accepted architecture/design/execution-pack documents, operational projections, and every path not listed above.

### Task 1: make each action-journal run namespace non-reissuable

- [x] Bind every `actions/<run>` directory to one durable service-root/actions-generation-authorized physical identity; a fresh process must reject loss, rename, replacement, or coherent pathname rebinding instead of accepting a new run namespace.
- [x] A live `journalGuard` must retain and revalidate the exact named run-directory identity throughout its lifetime and before/after authoritative reads/writes and close; detached writes must never become silently authoritative or disappear from later status/decision-request uniqueness reads.
- [x] Add deterministic cross-process rename/replacement races proving operation history, outcomes, and per-run decision-request uniqueness cannot be reissued or redirected.

### Task 2: bind immutable run registration to the exact ledger physical generation

- [x] Registration must durably bind the exact ledger parent and ledger file physical generation using controller-owned immutable authority without changing the accepted service-visible API contract or leaking physical/path data into v1 DTOs.
- [x] Every fresh read-only and writable service open derived from a run registration must atomically fail closed unless the named parent/file still match the registered generation. A replaced parent/file must never become a new authoritative projection source and must never initialize a second `.run-locks` generation.
- [x] Preserve create-only/byte-verify registration semantics, existing cursor lineage, bounded resource accounting, standalone non-service runner behavior, and lock ordering. Add replacement races covering fresh GET/projection and writable watcher/decision paths while an original transition lease is live.

### Task 3: allow bounded owner retirement after durable `RECONCILIATION_REQUIRED`

- [x] Align watcher drain semantics with the accepted contract: once a bounded reconciliation attempt leaves an admitted cancel durably at `RECONCILIATION_REQUIRED`, that operation satisfies the frozen closing watermark for retirement while remaining visible for later read-only reconciliation.
- [x] Do not treat `RECEIVED` or `CLAIMED` as drained; do not synthesize terminal success; preserve owner-generation isolation, watermark ordering, and no recovery/resume authority.
- [x] Add tests for ambiguous append -> durable `RECONCILIATION_REQUIRED` -> watcher drain -> `CLOSING -> RETIRED`, plus negative cases proving unresolved non-durable states still block retirement.

### Task 4: cumulative validation and completion

- [x] Run focused normal/race tests for all touched packages and stress each new Major-specific replacement/retirement race at least 10 repetitions where practical.
- [x] Rerun the previously sealed combined EP-006 race regressions, full `go test -count=1 -race ./...`, full uncached `go test -count=1 ./...`, `go vet ./...`, Darwin/Windows production builds, and `git diff --check`.
- [x] Prove accepted design docs are byte-unchanged; A/B/C checkpoints and prior candidate remain ancestors; correction diff from `49d418d...HEAD` contains only maximum-authority paths; cumulative Phase-5 diff preserves accepted semantics.
- [x] Complete all checkboxes, move this plan to `docs/plans/completed/`, leave a clean worktree, and commit the bounded correction.

## Completion boundary

Ralphex completion is implementation evidence only. The new exact head must undergo a fresh cumulative deterministic A-D acceptance and then a fresh independent combined exact-head Critical/Major review. Publication/PR/merge remains forbidden until that review is exactly 0 Critical / 0 Major.

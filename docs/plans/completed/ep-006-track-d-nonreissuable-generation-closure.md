# EP-006 Track D — Non-Reissuable Generation Closure

## Authority

Blocked exact head: `425b099d1844b82ff169de4098f995d9626eaf02`.
Blocked tree: `cfc92b0fda13e8f482568ca666ca6310fa37114d`.
Accepted Track-C predecessor: `e1b5367ea82e774d81b10c989d506efe16b7d5bc`.
Accepted EP-006 design: `908710e1606b0da761e612c36b85c866e182c7f7`.
Sealed review: `0072c6632e3dc1d4515c49abccf40f6da60c8a4f7dc9b387278960091ced826d`.
Review verdict: `0 Critical / 2 Major / 1 Minor`; exact-head gate NOT_VERIFIED.

Both Majors are inside accepted cumulative Track-D ownership under `internal/actioncontrol/**`.
A/B/C remain frozen. The known malformed-cancel HTTP classification Minor remains deferred.

## Maximum mutation authority

```text
internal/actioncontrol/journal_linux.go
internal/actioncontrol/journal_linux_test.go
internal/actioncontrol/resource_guard_linux.go
internal/actioncontrol/resource_guard_linux_test.go
this plan and its completed-plan move
```

No other source file may change. No new production file may survive.

### Task 1: make generation authority non-reissuable

- [x] Bind resource-guard and action-journal/request-index generation authority to the already trusted canonical service-root inode, or an equivalent durable root mechanism outside every replaceable child namespace.
- [x] A missing/replaced child object plus missing/replaced child anchor/manifest must never authorize automatic creation of a new generation after that subsystem generation was established.
- [x] Initial establishment must be race-safe, bounded, durable, and bound to the exact service-root physical identity; reopening must verify exact existing generation state and fail closed on loss/mismatch rather than reissue it.
- [x] The durable root authority must preserve the exact resource 8-ledger/4-snapshot namespace and the journal global request-id/idempotency namespace across fresh processes.
- [x] Preserve all existing no-symlink/no-hardlink/mode/owner/descriptor checks, bounded locking, cancellation, crash release, journal lock ordering, and prior accepted Track-D semantics.

### Task 2: adversarial proof

- [x] Add cross-process resource tests that remove/replace a protected directory or slot generation together with its directory anchor and/or `.slot-identities.json`; fresh process B must fail closed while process A retains original-generation capacity.
- [x] Cover top guard directory, ledger directory, snapshot directory, held/free slot identity generation, and paired anchor/manifest loss; prove no second 8/4 generation and no descriptor/slot leak.
- [x] Add cross-process journal tests that remove/replace `actions`, request-index root/shard, lock identity, or protected object together with its corresponding anchor while preserving the trusted service root; fresh `Open` must fail closed and must not establish empty global request-id authority.
- [x] Prove existing durable global `(principal_id, request_id)` identity/history cannot disappear or be reset by paired object+anchor replacement.
- [x] Stress both Major-specific paired-replacement proofs at least 10 repetitions under `-race` where practical.

### Task 3: validation and completion

- [x] Run focused `go test -count=1 ./internal/actioncontrol` and focused race.
- [x] Rerun all prior Track-D Major-specific actioncontrol stresses, owner-finalization/real-runner proof, and predecessor merge/evidence/supervisor tripwires.
- [x] Run `go test -count=1 ./...`, one clean first-run `go test -count=1 -race ./...`, `go vet ./...`, Darwin/Windows production builds where supported, and `git diff --check`.
- [x] Prove exact correction scope contains only the four authorized actioncontrol files plus completed-plan move; cumulative Track-D authority and frozen A/B/C paths remain unchanged.
- [x] Mark all items complete, move this plan to `docs/plans/completed/`, consolidate to exactly one implementation commit above the plan-only authority, and leave the worktree clean.

## Completion boundary

Ralphex completion is implementation evidence only. Fresh deterministic Track-D acceptance and a fresh immutable exact-head Critical/Major review are mandatory. Required closure remains `0 Critical / 0 Major` before combined EP-006 A-D acceptance. Do not publish or merge from this correction.

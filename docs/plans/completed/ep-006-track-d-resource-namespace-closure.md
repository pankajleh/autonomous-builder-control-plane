# EP-006 Track D — Resource Namespace Closure Correction

## Authority

Exact blocked candidate: `b0c37a7a07795df01a527b04d994c2f7d7fb014b`.
Blocked tree: `0cd42944e3e7329c33e7090d2b9302a03f1b7080`.
Accepted Track-C predecessor: `e1b5367ea82e774d81b10c989d506efe16b7d5bc`.
Accepted EP-006 design: `908710e1606b0da761e612c36b85c866e182c7f7`.
Fresh exact-head review SHA-256: `82773c8570f921a1f4e5bfb5ea9f23e7cc99de34e495dfc4e233d489f7f6cf79`.
Review verdict: `0 Critical / 1 Major / 1 Minor`; exact-head gate NOT_VERIFIED.

This correction closes only the remaining Major: cross-process replacement of the resource-guard directory namespace can split the global 8-ledger / 4-snapshot ceiling even though individual slot files are inode-pinned.
The known malformed-cancel HTTP classification Minor remains outside this bounded correction because closing it would reopen frozen `internal/serviceapi/**`.

## Maximum mutation authority

```text
internal/actioncontrol/resource_guard_linux.go
internal/actioncontrol/resource_guard_linux_test.go
this plan and its completed-plan move
```

Everything else is frozen, including all Track-A/B/C surfaces and all other Track-D action journal, action API, runner, and command composition files.

### Task 1: close the cross-process namespace-split Major

- [x] Durably parent-anchor `read-model-resource-guard`, `ledger`, and `snapshot` namespaces for the full guard lifetime, using exact directory identities reachable from already trusted parent descriptors rather than trusting whichever path is currently named.
- [x] Make fresh guard open fail closed if any anchored guard/ledger/snapshot namespace was renamed, replaced, rebound, symlinked, hard-linked where prohibited, or otherwise no longer resolves to the exact durable identity established for that service-root namespace.
- [x] Preserve the exact eight ledger slots and four snapshot slots as one service-root-wide namespace across processes; no replacement directory may allow a second valid slot generation while holders of the original generation remain active.
- [x] Revalidate anchored directory identities before acquisition, after successful slot locking, before unlock/release, and during guard close; namespace-integrity loss must fail closed without granting replacement capacity.
- [x] Preserve context-cancellable bounded acquisition, existing lock ordering, slot identity manifests, crash/process-exit release behavior, and all previously accepted D semantics.

### Task 2: adversarial proof

- [x] Add a true cross-process directory-replacement test: process A holds enough original-generation slots to constrain capacity, the guard/ledger/snapshot directory namespace is renamed/replaced, then fresh process B must fail closed rather than create/acquire a replacement slot generation.
- [x] Cover replacement of the top guard directory and each child slot directory, including replacement before fresh open and replacement while another process holds slots.
- [x] Prove existing individual held/free slot replacement tests continue to pass and no slot/descriptor leak remains after failed fresh-process attempts.
- [x] Stress the new cross-process namespace-replacement test at least 10 repetitions under `-race` where practical.

### Task 3: validation and completion

- [x] Run focused `go test -count=1 ./internal/actioncontrol` and focused race.
- [x] Rerun all four prior Major-specific actioncontrol race stresses and the previously closed Track-D finalization/real-runner race proof.
- [x] Rerun predecessor merge/evidence/supervisor tripwires.
- [x] Run `go test -count=1 ./...`, one clean first-run `go test -count=1 -race ./...`, `go vet ./...`, Darwin/Windows production builds where supported, and `git diff --check`.
- [x] Prove the exact correction diff contains only the two authorized resource-guard files plus this completed-plan move; prove cumulative Track-D authority and frozen A/B/C paths remain unchanged.
- [x] Mark every item complete, move this plan to `docs/plans/completed/`, create exactly one implementation commit above this plan-only authority commit, and leave the worktree clean.

## Completion boundary

Ralphex completion is implementation evidence only. Fresh deterministic Track-D acceptance and a new immutable exact-head Critical/Major review are mandatory. Required closure remains `0 Critical / 0 Major` before combined EP-006 A-D acceptance. Do not publish or merge EP-006 from this correction.

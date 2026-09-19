# EP-006 Track D — Request Identity Issuance Closure

## Authority

Blocked exact head: `0cf5fb45f1d8f89efb5f81a160468c3c40cf9f70`.
Blocked tree: `c4e6a74137dd2cfdc1bef96f6a4b9f22f5caa0e5`.
Accepted Track-C predecessor: `e1b5367ea82e774d81b10c989d506efe16b7d5bc`.
Accepted EP-006 design: `908710e1606b0da761e612c36b85c866e182c7f7`.
Sealed review verdict: `0 Critical / 1 Major / 1 Minor`; gate NOT_VERIFIED.
Review final seal SHA-256: `05e1de40dcfd6bfcd7ec380ae9be63666b6c2da1646819c73b9bad9bf58bc329`.

This correction closes only the remaining Major: an issued global `(principal_id, request_id)` identity can be deleted from an otherwise valid root-authorized shard and then reissued with conflicting intent.
The known malformed-cancel HTTP classification Minor remains deferred.

## Maximum mutation authority

- `internal/actioncontrol/journal_linux.go`
- `internal/actioncontrol/journal_linux_test.go`
- this plan and its completed-plan move

Everything else is frozen, including resource guard, action API, runner/cmd, all A/B/C surfaces, accepted design docs, and all previous correction plans.
### Task 1: make issued request identities non-reissuable

- [x] Add bounded per-shard issuance authority/history, rooted in the already trusted service-root/shard generation authority, that remembers every committed lookup key after its identity is first issued.
- [x] A previously issued key whose `<lookup>.json` is missing, replaced, semantically different, or no longer descriptor-identical must fail closed with integrity error; it must never be treated as unused or admitted again.
- [x] A present identity with no matching issuance authority/history must also fail closed; do not silently adopt orphan/untracked identities.
- [x] Preserve exact global semantic replay/conflict behavior: exact same request returns the frozen operation; conflicting intent for the same key returns conflict only when the original durable identity is intact and re-proven, otherwise integrity failure.
- [x] Keep lookup bounded to the canonical shard. Do not restore service-wide/all-run scans, do not add one root xattr per request, and do not create unbounded mutable authority.
- [x] Per-shard authority/history must itself be non-reissuable: its exact inode/namespace must be bound by the existing root-authorized shard generation, and loss/replacement must fail closed.

### Task 2: crash-safe issuance transaction and recovery

- [x] Integrate issuance history with the existing `.pending` identity publication protocol so crash at every ordering point deterministically yields either no committed issuance or the exact frozen issued identity—never a reusable key.
- [x] Bound history by `MaxRequestsPerShard`; validate mode/owner/no-symlink/no-hardlink, byte/count ceilings, exact canonical records, duplicate keys, chronology/hash-chain or equivalent integrity, and descriptor/path identity.
- [x] Preserve the 2-second journal lock ceiling and existing root/journal lock ordering. No network/process/provider work may enter the critical section.
- [x] Preserve existing immutable-index recovery, receipt replay, decision uniqueness, journal segmentation, owner cancellation/finalization, and all previously accepted D semantics.
### Task 3: adversarial proof and completion

- [x] Add direct deletion/replacement tests for an already issued `<lookup>.json` while shard directory, shard anchor and root authority remain intact; fresh process/open must fail closed and must preserve original request-id authority.
- [x] Prove conflicting cross-run reuse after identity deletion cannot create a second operation, and exact replay cannot reconstruct from attacker/replacement bytes.
- [x] Add loss/replacement tests for the issuance-history authority itself and orphan identity files; all must fail closed without leaking descriptors or mutating history.
- [x] Add fault-injection/crash tests around issuance-history commit versus pending/final identity publication; stress the new Major-specific tests at least 10 repetitions under `-race` where practical.
- [x] Rerun prior journal/root-generation/paired-generation Major stresses, owner-finalization/real-runner proof, predecessor merge/evidence/supervisor tripwires, full uncached tests, one fresh full repository race, `go vet`, Darwin/Windows production builds, and `git diff --check`.
- [x] Prove exact correction scope, cumulative Track-D ownership, clean worktree, zero A/B/C mutation, no extra runner production files, and no unchecked plan items.
- [x] Move this plan to `docs/plans/completed/` and consolidate to exactly one implementation commit above the plan-only authority.

## Completion boundary

Ralphex completion is implementation evidence only. Fresh deterministic Track-D acceptance and a fresh read-only exact-head Critical/Major review remain mandatory. Required closure is `0 Critical / 0 Major` before combined EP-006 A-D acceptance. Do not publish or merge from this correction.

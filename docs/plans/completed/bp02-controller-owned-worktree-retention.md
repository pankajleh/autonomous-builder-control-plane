# BP-01/BP-02 controller-owned worktree retention

Date: 2026-09-28

Exact base: `0d99f7d340cec40336728c6c59dcb66a41d2a77c`

## Problem

Ralphex always removes its governed worktree when a run ends. Upstream Ralphex has no configuration to keep it: `runWithWorktree` removes the worktree unless a failed plan archive left state behind. The branch survives, but ABCP's activity binding and BP-02 preview eligibility both resolve the governed worktree. That removal causes two known gaps, both recorded as carry-forward items at the PX-07 closure (Repo C P0052):

- **Trailing provider detail.** Provider detail emitted in the final seconds of a run, for example review-done signals, is not collected once the worktree is gone. Repo B PRs #37 and #41 made the removal expected cleanup, so no marker appears, but the provider statuses shown to the customer can stop short of the provider's real final state.
- **Preview after acceptance.** Preview materializes a fresh clone of the checkpoint SHA, so it does not need the worktree for content. It is ineligible after `BRANCH_ACCEPTED` only because the binding can no longer be resolved.

## Correction

Worktree removal moves out of Ralphex and becomes controller-owned.

- **Governance fork.** `abcp/governance-bundle-v2-20260928`, commit `055dfbdb4d92120a8249ae58923d7608d583e82f`, on top of v1 `66e8868`, adds `--keep-worktree`, which is valid only with `--worktree`. The run restores its working directory but leaves the worktree and branch in place. A worktree preserved after a failed plan archive keeps its existing recovery message. The capability probe adds `worktree_retention_v1: true`. Binary: `ralphex-governance-v2`, SHA-256 `54300a18cb9a5ae5dc295f0f2b7f2e9441165e6b0aa56b4be52b18c225de94bc`.
- **Manifest policy.** `worktree.retain`, optional and omitted when false, so existing authority digests are unchanged, requires `worktree.enabled`. `authority.New` accepts it only when the pinned binary's strict capability probe proves `worktree_retention_v1` (`ralphex.VerifyWorktreeRetentionCapabilityV1`).
- **Invocation.** A retaining manifest passes `--keep-worktree` after `--worktree --branch`.
- **Probe decoding.** `CapabilityProbeV1` accepts the new field, which is omitted when false, so v1 and v2 probes both remain strict canonical JSON.
- **Activity.** No rule changes. A retained worktree keeps resolving, so refresh returns a scope after acceptance and trailing detail is collected with verified overlap. The #37/#41 expected-cleanup rules still cover runs without retention and worktrees an operator removes later.
- **Unchanged.** Run state, acceptance (which uses its own detached checkout), admission, preview trust rules and activity schema.

## How long worktrees can be kept

Git places no time limit on a worktree whose directory exists: `git worktree prune` and `gc.worktreePruneExpire` only clear metadata for directories that are already gone. Retention is bounded by disk, not time. Measured on the integration host on 2026-09-28:

| Repository | Worktree size | Notes |
|---|---|---|
| Demo product (`example/product`) | well under 1 MB | 37 `abcp/*` branches, 5 MB `.git` |
| Repo C development repository | median about 50 MB, largest 662 MB (validation installs `node_modules`) | 26 `abcp/*` branches |

The disk had 222 GB free, so hundreds of Repo C development runs, or far more demo runs, fit before retention becomes a constraint. Two operational effects apply:

- a retained branch cannot be checked out in a second worktree, although ABCP acceptance and publication do not need that;
- each retained worktree remains visible to `git worktree list`.

An operator removes one with `git -C <repository> worktree remove <path>`. The `abcp/<run>` branch stays, and activity already recorded stays durable.

## Next: persistent state that outlives the worktree (not implemented here)

Keeping every worktree is the interim policy. The durable design lets ABCP remove worktrees on a retention schedule without losing preview or audit:

1. **Pin checkpoints.** When the checkpoint observer records `CHECKPOINT/AVAILABLE`, ABCP writes `refs/abcp/checkpoints/<run>/<sha>` in the governed repository. The commit then survives branch deletion and garbage collection.
2. **Seal the binding.** ABCP stores the verified binding facts in its own store at that moment: registration, manifest, plan and capsule digests, the branch, the start SHA and the checkpoint SHA. These are the facts preview eligibility re-derives today from the live worktree.
3. **Eligibility from sealed state.** Once the run is terminal and the worktree is absent, BP-02 eligibility verifies the sealed binding against the immutable registration and the pinned ref instead of the worktree. This changes the BP-02 trust model and needs its own review.
4. **Controller-owned pruning.** With 1–3 in place, ABCP can remove worktrees for terminal runs after a configured window, for example 30 days after acceptance or when the product task closes. Refs and the activity log keep the run's source and history.

## Acceptance

```text
go test ./internal/ralphex ./internal/authority ./internal/run ./internal/activity ./internal/preview ./internal/runadmission ./internal/serviceapi ./cmd/abcp
go vet ./internal/ralphex ./internal/authority ./internal/run ./internal/activity
git diff --check
Ralphex fork: go test ./... (all packages pass)
```

New tests:

- `TestInvocationKeepsWorktreeOnlyInWorktreeMode`
- `TestWorktreeRetentionCapabilityIsProvenByThePinnedProbe`
- `TestNewWorktreeRetentionRequiresWorktreeAndProvenCapability`
- `TestRetainedWorktreeCollectsTrailingDetailAfterAcceptance`
- in the fork: `TestKeepWorktreeFlag`, `TestABCPGovernanceCapabilityAdvertisesWorktreeRetention` and `TestArchivePlanWorktree/keep_worktree_hands_removal_to_caller`

## Cutover

1. Merge this change, then build and cut over `abcp` from the merge SHA with no executing run.
2. In each manifest template, pin `ralphex-governance-v2` (binary path, SHA-256 and source SHA) and set `"worktree": {"enabled": true, "retain": true}`. The templates are versioned in `deploy/local-integration/abcp-config/`; `tools/operator/check_local_integration_config.py` checks the host copy for drift.
3. Restart `abcp serve` so admission profiles reload, then run one governed run. The run should show three things: the worktree is retained after `BRANCH_ACCEPTED`, trailing provider detail is collected, and a preview can be created after acceptance.

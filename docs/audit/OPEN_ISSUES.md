# Open Issues After Behavior Audit

These items are intentionally **not blockers** to adopting Ralphex as the inner orchestrator. They are either upstream investigations or control-plane implementation details.

1. Determine why a successful first-review fix can still emit `warning: first review pass did not complete cleanly, continuing...`.
2. Decide whether strict production policies should fail immediately when the required validator is unavailable rather than permit equivalent/best-effort checks.
3. Define managed terminal-failure preservation policy for valuable uncommitted work.
4. Finalize durable run/attempt/task/session identity format.
5. Ensure notifications link durable evidence rather than ephemeral worktree paths.
6. Enforce scope control when review fixes expand the actual changed-file set.
7. Implement deterministic controller-owned branch acceptance.
8. Finalize cross-model review risk triggers.
9. Implement rich blocker/escalation states and authority provenance.
10. Replace foundation JSONL ledger with a transactional durable store when service deployment requires it.
11. Define exact stale-worktree owner-dead proof before automated cleanup/restart.
12. Investigate Ralphex dashboard parser/history behavior for FAILED/COMPLETED/REVIEW inconsistencies.
13. Decide whether to contribute dashboard fixes upstream or permanently treat it as best-effort visibility.
14. Implement capacity-vs-hard-quota-vs-auth/billing failure classification.
15. Finalize cross-plan state machine between branch acceptance and merge.
16. Define semantic-conflict auto-resolution boundaries versus `HUMAN_DECISION_REQUIRED`.
17. Implement parallel scheduling risk heuristics using **actual final diffs**, not only planned ownership.
18. Choose integration queue strategy: incremental baseline, serialized queue, or compatible batches.
19. Define project-specific combined acceptance required before `READY_FOR_MERGE`.

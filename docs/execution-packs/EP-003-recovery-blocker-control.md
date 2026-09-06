# EP-003 — Recovery and Blocker Control

**Status:** Ready for implementation
**Base:** `f7ca8e6e59f04e7bece0582f3febcd37ec2cb4f7`
**Goal:** Add deterministic recovery ownership, failure preservation, blocker classification, and explicit resume authority without adding scheduling or integration.

## Architecture boundary

EP-003 extends the control plane above the already-accepted EP-002 single-plan lifecycle. Ralphex remains the inner coding/orchestration subprocess. Recovery decisions, durable evidence, blocker states, and resume authority remain controller-owned.

Do not add cross-plan scheduling, integration/merge authority, GitHub lifecycle automation, service APIs, dashboard work, deployment, or production completion.

## Required outcomes

1. Stale execution/worktree state can be detected and classified without destructive cleanup.
2. Automated cleanup requires positive owner-dead proof and durable pre-cleanup evidence.
3. Valuable uncommitted failure state is snapshotted before cleanup/restart.
4. Capacity/rate/quota/auth/billing and decision/secret/external/validation/policy blockers have explicit controller classifications.
5. Resume/restart requires explicit, recorded authority and cannot silently reuse stale execution state.

## Recovery invariant

The recovery sequence is:

```text
detect stale state
→ prove prior owner is dead
→ snapshot provenance + uncommitted state
→ classify blocker/recovery reason
→ perform policy-authorized cleanup
→ create new attempt/resume authority
→ restart
```

No cleanup may occur before owner-dead proof and snapshot publication succeed. A missing or ambiguous proof must become `RECOVERY_REQUIRED` or `HUMAN_DECISION_REQUIRED`, not an inferred safe cleanup.

## Durable identity

Preserve the existing hierarchy:

```text
project_id → plan_id → run_id → attempt_id → task_id → agent_session_id
```

EP-003 only needs the identities required for recovery/resume provenance. Do not build a scheduler or service database.

## Failure and blocker taxonomy

At minimum distinguish:

- transient retryable execution failure;
- capacity/rate-limit wait;
- hard quota/usage exhaustion;
- authentication/authorization failure;
- billing/account failure;
- `HUMAN_DECISION_REQUIRED`;
- `SECRET_REQUIRED`;
- `EXTERNAL_DEPENDENCY`;
- `VALIDATION_UNAVAILABLE`;
- `POLICY_BLOCKED`;
- unrecoverable `FAILED`.

Classifiers must be evidence-backed and deterministic where possible. Unknown/ambiguous cases fail closed to recovery/human authority rather than guessing.

## Safety and evidence

- Structured argv only; no shell strings from governed/user input.
- No secret values in ledger, Git, snapshots, or classifier payloads.
- Snapshot artifacts are immutable and SHA256-addressed through the existing evidence store.
- Cleanup must be repository/worktree scoped and path-contained.
- PID alone is not sufficient owner-dead proof; use process identity data sufficient to avoid PID-reuse ambiguity on Linux.
- Tests must use disposable repositories and harmless local fake processes/Ralphex executables.
- Go standard library only unless separately justified.

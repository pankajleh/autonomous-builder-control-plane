# Autonomous Builder Control Plane Architecture

## 1. Mission

Build a durable autonomous software-development control plane that can accept governed work, launch Ralphex safely, recover from failures, verify results independently, integrate concurrent branches, and advance work to READY_FOR_MERGE with a complete evidence trail.

The system should minimize human intervention without removing human authority where policy genuinely requires it.

## 2. Design principles

1. **Evidence over narration.** Agent claims never substitute for controller-run tests, Git state, process state, or recorded artifacts.
2. **Git is development memory, not the operational ledger.** Commits preserve code history; the control plane preserves run history.
3. **Ralphex is an inner orchestrator.** Reuse its plan/task/review/worktree machinery.
4. **Committed state is the resume boundary.** Uncommitted edits are volatile evidence and must be snapshotted when recovery needs them.
5. **Every state transition has authority.** No transition to READY_FOR_MERGE without deterministic evidence.
6. **Parallelism is provisional until integration.** Branch-local success is not global success.
7. **Human intervention is classified, not improvised.** Blockers become explicit machine states.
8. **Cross-model review is risk-triggered.** Deterministic acceptance has higher priority than model diversity.
9. **The dashboard is a view.** The event ledger is the source of truth.
10. **Do not duplicate proven inner capabilities.** Keep the outer layer small and governance-focused.

## 3. Logical components

### 3.1 Control API / Operator Interface

Accepts work requests, policy overrides, human decisions, resume/cancel commands, and evidence queries.

Initial implementation may be CLI-first. A service API can follow once the state and evidence contracts stabilize.

### 3.2 Authority Registry

Stores immutable references for each run:

- project/repository identity;
- default branch;
- plan path and plan hash;
- start SHA;
- Ralphex binary/source identity;
- model and effort policy;
- acceptance policy version;
- integration policy version;
- actor/correlation identifiers.

### 3.3 Scheduler

Decides when a governed plan may execute.

Responsibilities:

- dependency checks;
- concurrency limits;
- risk-based serialization;
- retry timing;
- quota/capacity resume;
- integration queue ordering.

It does not create worktrees itself; it asks the Ralphex adapter to execute plans with the required isolation.

### 3.4 Ralphex Adapter

The only component allowed to construct and launch governed Ralphex processes.

It validates:

- exact binary path;
- expected source/binary hash when pinned;
- mode and flags;
- config directory;
- plan identity;
- worktree choice;
- executor/model/effort policy;
- timeout/wait-on-limit policy.

It captures stdout/stderr, exit status, process identity, start/end timestamps, and relevant progress paths.

### 3.5 Process Supervisor

Tracks process groups, child ownership, heartbeats/output activity, kill/cancel semantics, and stale-process proof.

This component is critical for safe EXP-04-style recovery.

### 3.6 Durable Event Ledger

Append-only operational truth. Every material event is written with a run ID and correlation identifiers.

Examples:

- RUN_CREATED
- AUTHORITY_VALIDATED
- RALPHEX_STARTED
- TASK_ATTEMPT_STARTED
- CAPACITY_WAIT
- RETRYING
- IMPLEMENTATION_COMPLETED
- BRANCH_ACCEPTANCE_PASSED
- INTEGRATION_CONFLICT
- HUMAN_DECISION_REQUIRED
- READY_FOR_MERGE
- MERGED
- PRODUCTION_ACCEPTED
- FAILED

### 3.7 Acceptance Controller

Runs required commands independently from the coding agent.

It owns:

- command execution;
- timeout;
- exit code;
- stdout/stderr capture;
- artifact hashing;
- test counts where parsable;
- environment metadata;
- policy decision.

A branch is not accepted because Ralphex says COMPLETED; it is accepted because the acceptance controller records required evidence.

### 3.8 Recovery Controller

Handles:

- failed Ralphex process;
- stale worktree;
- incomplete run after host restart;
- quota/capacity waits;
- recoverable external dependencies;
- safe restart from committed authority.

### 3.9 Decision / Escalation Controller

Turns ambiguous blockers into durable states and requests narrowly scoped human authority.

Example:

```text
HUMAN_DECISION_REQUIRED
  question: "Choose rollout ring: blue or green"
  allowed_answers: [blue, green]
  requested_by_run: ...
  evidence: ...
```

When authority arrives, it is written as an immutable decision event and the run resumes.

### 3.10 Integration Controller

Takes one or more BRANCH_ACCEPTED candidates and attempts controlled integration.

Pipeline:

```text
candidate branches
→ select integration baseline
→ merge/rebase in disposable integration workspace
→ detect textual conflicts
→ resolve only under policy
→ run combined acceptance
→ classify semantic conflicts
→ INTEGRATION_ACCEPTED or escalation/failure
```

### 3.11 Merge Authority

The only component allowed to assert READY_FOR_MERGE.

Required inputs:

- branch acceptance passed;
- integration acceptance passed;
- required review policy satisfied;
- no unresolved blockers;
- provenance complete;
- expected head SHAs still match.

Actual merge may remain human-approved initially.

### 3.12 Risk / Assurance Policy

Determines whether native Ralphex review is sufficient or whether independent cross-model review is required.

Likely triggers:

- auth/security;
- destructive migration;
- billing/financial logic;
- concurrency/locking;
- cryptography;
- data retention/deletion;
- large cross-cutting diff;
- weak deterministic acceptance;
- prior failed acceptance on same task.

## 4. Runtime topology

Initial deployment target:

```text
Ubuntu execution host
├── control-plane process
├── event/evidence storage
├── Ralphex binary (pinned)
├── Codex CLI
├── Claude Code (optional/risk-triggered)
├── target repositories
└── Ralphex worktrees
```

Later, the control API may be remote while execution workers remain on Ubuntu nodes.

## 5. Durable identity model

Every run gets a globally unique `run_id` generated by the control plane.

Nested identities:

```text
project_id
  plan_id
    run_id
      attempt_id
        task_id
          agent_session_id
```

Separate identifiers:

- `request_id`: one transport attempt;
- `correlation_id`: one logical user/control-plane action;
- `run_id`: one governed execution lifecycle.

Do not derive run identity from Ralphex progress filenames or plan names.

## 6. Authority manifest

Before starting Ralphex, create an immutable run manifest containing at minimum:

```yaml
run_id: ...
project:
  repo: ...
  default_branch: main
plan:
  path: docs/plans/feature.md
  sha256: ...
git:
  start_sha: ...
ralphex:
  source_sha: ...
  binary_sha256: ...
  mode: full
  worktree: true
executor:
  primary: codex
  task_model: gpt-5.6-sol
  task_effort: high
review:
  native: true
  cross_model: risk-triggered
acceptance_policy: branch-v1
integration_policy: integration-v1
```

The manifest should be content-addressed or otherwise immutable after RUNNING starts. Amendments become new authority events; do not silently mutate history.

## 7. Run lifecycle

Simplified happy path:

```text
RUN_CREATED
→ AUTHORITY_VALIDATED
→ EXECUTION_STARTING
→ IMPLEMENTING
→ IMPLEMENTATION_COMPLETED
→ BRANCH_ACCEPTANCE_PENDING
→ BRANCH_ACCEPTED
→ INTEGRATION_PENDING
→ INTEGRATION_ACCEPTED
→ READY_FOR_MERGE
→ MERGED
→ PRODUCTION_ACCEPTANCE_PENDING
→ PRODUCTION_ACCEPTED
→ COMPLETED
```

Blockers and failures branch from this path and are explicit in the state machine document.

## 8. Acceptance hierarchy

### Level 1: agent-local validation

Useful but non-authoritative. Performed by Codex/Claude inside Ralphex.

### Level 2: Ralphex branch lifecycle completion

Implementation + native review complete.

### Level 3: controller branch acceptance

Required deterministic commands rerun independently. Evidence captured.

### Level 4: integration acceptance

Combined state passes tests and policy.

### Level 5: merge acceptance

Expected heads/provenance/risk policy satisfied.

### Level 6: production acceptance

Deployment/runtime checks pass where applicable.

Only Level 6 can produce final project completion when production verification is required.

## 9. Recovery model

The control plane records enough evidence to answer:

- What was the last committed SHA?
- Was Ralphex still running?
- Was the worktree owned by a live process?
- Were there uncommitted edits?
- What failed?
- Is the failure retryable?
- What authority is required to continue?

No destructive cleanup is allowed without a recorded recovery decision.

## 10. Security model

Key rules:

- secrets never stored in Git plans or event payloads in plaintext;
- child-agent environment is allowlisted;
- Ralphex/Codex/Claude binary identities recorded;
- repository path and worktree path canonicalized before execution;
- shell command templates are structured arguments, not concatenated untrusted strings;
- destructive operations require policy checks;
- event ledger is append-only to the application role;
- human decisions are actor-attributed and timestamped;
- merge uses exact expected head SHAs.

## 11. Technology choice

Initial control plane: **Go**.

Reasons:

- first-class process supervision and signal handling;
- single static-ish deployment artifact for Ubuntu;
- strong concurrency primitives;
- easy Git/CLI orchestration without a shell-heavy control layer;
- aligns operationally with Ralphex without importing or forking it;
- low dependency footprint for the foundation.

The first implementation uses only the Go standard library. Durable database storage can move from the foundation JSONL ledger to SQLite/PostgreSQL behind the same ledger interface without changing domain semantics.

## 12. Implementation boundaries

### Build now

- domain state machine;
- immutable run IDs and event schema;
- append-only event ledger interface;
- Ralphex invocation contract;
- process supervisor;
- authority manifest;
- deterministic acceptance executor;
- recovery controller;
- integration controller;
- operator CLI/API.

### Do not build now

- new coding agent;
- new review-agent framework;
- new worktree engine;
- duplicate Git branch/commit abstraction over normal Git unless needed for evidence capture;
- elaborate UI before state/evidence contracts stabilize;
- automatic cross-model review on every task.

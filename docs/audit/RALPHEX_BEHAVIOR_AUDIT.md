# Ralphex Behavior Audit and Architecture Decision

**Status:** Final behavior-audit conclusion  
**Audit date:** 2026-09-06  
**Canonical Ralphex repository:** `umputun/ralphex`  
**Audited master SHA:** `319e30618352a1b43e4be1b8a894c6c05e6d5fa8`  
**Audited binary SHA256:** `9ad47b083aaaf34eed4b35ca82f3c92d1a3b7641921be0ea606cbd1cd1c3adac`  
**Host:** Ubuntu 24.04.4 LTS  
**Codex CLI:** 0.149.0  
**Claude Code:** 2.1.263 for EXP-09  

## 1. Executive decision

**GO.** Adopt Ralphex as the inner autonomous coding/orchestration layer.

Do **not** use Ralphex as the global workflow authority, durable operational system of record, integration/merge controller, production acceptance authority, or final project-completion authority.

The audit crossed the threshold from source inspection to live behavior evidence. We observed successful operation, failures, retries, crashes, human-decision gaps, quota/capacity handling, parallel worktrees, cross-plan conflicts, and cross-model review behavior on a real Ubuntu host.

The central architectural rule is:

> **Ralphex COMPLETED means the local Ralphex lifecycle completed. It does not mean globally integratable, ready to merge, production accepted, or project complete.**

## 2. Audited model of Ralphex

Ralphex is a Git-native, plan-driven, disposable-session coding-agent orchestrator. Its durable continuity model is primarily:

```text
Markdown plan checkbox state + committed Git branch state
```

Each task execution is a fresh coding-agent session. The system provides bounded task loops, retries, review phases, Git/worktree operations, notifications, and a dashboard. This is useful and should be reused.

Ralphex is not a persistent cognitive agent, deployment system, global milestone engine, cross-plan scheduler, merge queue, production validator, or permanent audit ledger.

## 3. Experiment scorecard

| Experiment | Question | Result | Settled conclusion |
|---|---|---:|---|
| EXP-00 | Can Ralphex autonomously implement, validate, review and commit a real toy feature? | PASS | Baseline autonomous inner executor is viable. |
| EXP-01 | Does success notification work? | PASS | SMTP success notification works; reviewer concurrency ceiling observed. |
| EXP-02 | What happens on terminal implementation/validation failure? | PASS | One retry means two attempts; managed terminal cleanup removes worktree and can discard uncommitted work. |
| EXP-03 | Can a fresh retry recover a transient failure using the same worktree? | PASS | Yes; uncommitted edits survive within managed retry; one final success notification; dashboard may be wrong. |
| EXP-04 | What happens after SIGKILL? | PASS | Worktree and uncommitted edits survive; blind restart safely refuses; outer recovery must prove owner dead, snapshot evidence, clean stale worktree, then resume from committed authority. |
| EXP-05 | What happens when a task needs unavailable human authority? | PASS | Native task mode fails safely but lacks rich human-decision state; outer decision/escalation controller required. |
| EXP-06 | Can quota/capacity interruption recover? | PASS | `wait_on_limit` can recover capacity-class interruption; this is executor-policy retry, not task retry. |
| EXP-07 | Can two independent plans run concurrently in worktrees? | PASS | Yes, with real overlap and clean branch isolation; no global scheduler/merge authority. |
| EXP-08 | Can two individually successful plans fail integration? | PASS | Yes; both can be COMPLETED yet textually and semantically incompatible. |
| EXP-09B | Codex implementation: Codex review vs Claude review? | PASS | Both fixed 3/3 seeded defects; no defect-count uplift from cross-model review. |
| EXP-09A | Claude implementation: Claude review vs Codex review? | PASS | Both fixed 3/3 seeded defects; reciprocal result also tied. |

## 4. What is proven to work and should be reused

### 4.1 Plan-driven task execution

Ralphex reparses the plan and selects the first unfinished task/iteration. It rejects `ALL_TASKS_DONE` while actionable unchecked boxes remain.

### 4.2 Fresh coding-agent sessions

Fresh sessions reduce stale conversational context and force the agent to re-read repository-resident authority. This is a strength, not a limitation, provided the outer system writes durable authority into the repository or its own immutable context manifest.

### 4.3 Git-native continuity

Committed Git state is the reliable durability boundary. Uncommitted work has different behavior depending on failure mode:

- managed terminal failure: cleanup may remove the worktree and lose uncommitted edits;
- hard process death: cleanup does not run, so the stale worktree and uncommitted edits can survive.

### 4.4 Worktree isolation and parallel plans

Independent plans can run concurrently in distinct worktrees with real execution overlap and clean branch separation.

This is a strong reason not to rebuild worktree management in the outer system.

### 4.5 Native multi-agent review

Ralphex provides a first broad review with multiple specialist perspectives, followed by critical/major review. In both EXP-09 directions, strong same-model review fixed all controlled seeded defects.

### 4.6 Retry and capacity primitives

Task retry works, and limit/capacity waiting can recover when `wait_on_limit` is configured.

The outer system should configure and govern these primitives, not duplicate the inner mechanics.

### 4.7 Notifications and operational visibility

Notifications work and are useful. The dashboard is useful for current operational visibility.

Neither should be treated as authoritative workflow state.

## 5. What does not work as a global authority

### 5.1 Ralphex completion is branch-local

EXP-08 proved two plans can both:

- complete implementation;
- pass branch-local tests;
- pass native reviews;
- archive their plans;
- emit COMPLETED;
- display COMPLETED;

and still fail to integrate into one state satisfying both plans.

Therefore the control plane must introduce states between branch completion and merge.

### 5.2 No global cross-plan scheduler

Ralphex can run plans concurrently, but it does not own a project-wide DAG, dependency graph, merge queue, semantic conflict graph, or global acceptance policy.

### 5.3 No production-grade deterministic acceptance authority

Task agents are instructed to run validation, but the agent remains part of that execution path. EXP-05 showed review can catch a validation/integration mistake after the task agent claimed success.

Required acceptance commands must be run and recorded by the outer controller independently of the coding-agent narrative.

### 5.4 No rich task-level human decision workflow

Plan creation can ask questions. Task execution does not expose a robust human-decision state machine for blockers such as:

- HUMAN_DECISION_REQUIRED
- SECRET_REQUIRED
- EXTERNAL_DEPENDENCY
- VALIDATION_UNAVAILABLE
- POLICY_BLOCKED

The outer control plane must classify, pause, notify, collect authority, and resume.

### 5.5 No permanent forensic ledger

Ralphex progress logs are operational logs, not a permanent immutable event store. Reuse/archive behavior and dashboard representation are not sufficient for forensic run identity.

### 5.6 Dashboard is non-authoritative

Observed behavior includes:

- success after retry while UI remained FAILED;
- successful reruns with differing final UI states;
- same plan names reusing/overwriting prior UI session representation;
- review-only sessions remaining at REVIEW after successful completion;
- no representation of EXP-08 global integration failure.

The dashboard should be treated as a view, never as the source of truth.

## 6. Failure and recovery semantics

### 6.1 Managed terminal failure

Observed:

```text
agent attempt fails
→ task retry if configured
→ if terminal: Ralphex cleanup executes
→ worktree removed
→ uncommitted work may be lost
→ branch remains at last commit
```

Outer implication: if uncommitted work may be valuable, the controller should capture a diff/evidence snapshot before destructive managed cleanup when feasible.

### 6.2 Hard crash / SIGKILL

Observed:

```text
process dies abruptly
→ cleanup does not execute
→ stale worktree survives
→ uncommitted edits survive
→ blind restart refuses because worktree already exists
```

Required outer recovery protocol:

```text
detect stale worktree
→ prove owning process is dead
→ snapshot uncommitted diff/files
→ record recovery decision
→ remove stale worktree
→ prune metadata
→ restart from committed branch + plan authority
```

Never auto-remove a stale worktree merely because it exists.

### 6.3 Capacity / quota

Observed capacity-style failure can recover through `wait_on_limit` when configured.

The outer policy still needs to distinguish:

- transient service/model capacity;
- rate/usage limit with expected recovery;
- hard quota exhaustion;
- authentication failure;
- billing/account failure.

## 7. Review conclusion

EXP-09 produced a symmetric result:

```text
Codex-authored candidate:
  Codex review  3/3
  Claude review 3/3

Claude-authored candidate:
  Claude review 3/3
  Codex review  3/3
```

No defect-count improvement was demonstrated by always adding a second model.

Claude showed a useful qualitative tendency to reason explicitly about commit provenance and test weakening, but this did not improve the controlled defect score.

Architecture decision:

```text
DEFAULT
  native same-model Ralphex multi-agent review
  + controller-run deterministic acceptance

OPTIONAL / RISK-TRIGGERED
  independent cross-model review
```

Triggers can include high-risk migrations, auth/security changes, billing, data loss risk, concurrency, schema changes, or low-confidence acceptance evidence.

## 8. Required outer responsibilities

The control plane must own:

1. global goal/milestone and plan authority;
2. immutable context manifest and provenance;
3. durable run/attempt/task/session event ledger;
4. deterministic acceptance execution and evidence capture;
5. blocker classification and human-decision workflow;
6. stale-worktree/crash recovery policy;
7. capacity/quota/backoff/scheduled resume policy;
8. cross-plan scheduling and actual-final-diff analysis;
9. integration/merge controller;
10. combined acceptance after integration;
11. semantic-conflict classification;
12. READY_FOR_MERGE authority;
13. production acceptance and final completion;
14. optional risk-triggered cross-model assurance.

## 9. Capabilities explicitly not to rebuild

Unless later evidence proves a material gap, do not reimplement:

- task selection from plan checkboxes;
- coding-agent session orchestration;
- Ralphex task retry loop;
- Ralphex capacity wait primitive;
- worktree creation and branch isolation;
- normal Git commit mechanics;
- Ralphex native specialist review;
- plan archival;
- basic Ralphex notifications;
- Ralphex dashboard current-session visualization.

## 10. Final architecture boundary

```text
ChatGPT = Architect / human interface
Control Plane = authority / policy / scheduler / evidence / recovery / integration
Ralphex = autonomous development orchestrator
Codex CLI / Claude Code = coding and review agents
Ubuntu = durable execution environment
Git = durable development memory
GitHub = remote collaboration and merge surface
```

## 11. Remaining issues that do not block architecture

These are follow-up engineering or upstream-investigation items, not reasons to continue the behavior audit:

- exact reason for `warning: first review pass did not complete cleanly, continuing...` after a valid fix;
- exact dashboard parser/history bugs;
- managed terminal-failure preservation policy for valuable uncommitted work;
- exact production-grade capacity backoff matrix;
- exact risk score for cross-model review;
- upstream dashboard fixes versus treating dashboard as best-effort only.

## 12. Final go/no-go

**GO:** use Ralphex as the inner autonomous development executor/orchestrator.

**NO-GO:** do not make Ralphex the global source of truth or final completion authority.

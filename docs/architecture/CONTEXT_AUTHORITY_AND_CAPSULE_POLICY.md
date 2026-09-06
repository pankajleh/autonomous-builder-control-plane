# Context Authority and Task Capsule Policy

## Purpose

Fresh coding/review sessions must be able to execute correctly without ChatGPT history or a long-lived agent conversation. Project context is durable in the repository; model context is disposable.

This policy is effective for EP-004 and later. EP-003 is not retroactively represented as having used generated capsules.

## Authority hierarchy

Context is selected from canonical sources in this order:

1. architecture and ADRs;
2. implementation roadmap;
3. current progress/state;
4. the active execution pack;
5. the active Ralphex task plan;
6. exact repository/base SHA and prior accepted task outputs.

A task must not invent missing architecture. Missing required authority becomes a blocker or `HUMAN_DECISION_REQUIRED`.

## Context layers

Every fresh task receives a compact context capsule made of three layers:

- **Global guardrails** — project purpose, authority hierarchy, state/security invariants, completion rules, and non-negotiable boundaries.
- **Execution-pack context** — roadmap phase, parent goal, in-scope deliverables, explicit non-goals, relevant components, and expected end state.
- **Task context** — exact objective, relevant files/contracts, predecessor outputs, edge cases, and acceptance criteria.

The default is relevant context, not the entire architecture corpus.

## Capsule requirements

Each capsule must record:

- project/plan/task identity;
- exact base SHA;
- roadmap phase and execution-pack ID;
- selected source paths plus SHA256 hashes;
- relevant prior task outcome summaries and commit SHAs;
- explicit non-goals;
- context policy version;
- capsule SHA256.

Run authority may bind `context_capsule.path` and `context_capsule.sha256`. When present, the controller verifies the exact capsule bytes, its internal canonical hash, repository/base SHA, and every source hash before launching Ralphex. Manifests from before this policy may omit the binding.

## Token-efficiency rules

- Do not replay prior agent transcripts as context.
- Prefer compact task-outcome records over conversation history.
- Read deeper canonical documents on demand only when the task requires them.
- Use a deterministic component/dependency map as the primary selector; semantic retrieval may supplement but cannot override canonical authority.
- Keep repeated global guardrails small and stable so provider prompt caching can help where available, but correctness must not depend on caching.

## Fresh-task startup contract

Before implementation, every fresh task must read its capsule, verify the referenced repository/base identity, and inspect the task's relevant source files. If a referenced source hash no longer matches, execution must stop for re-authorization rather than silently using newer context.

The intended invariant is: **durable external project memory + small fresh agent sessions**, not one indefinitely growing model conversation.

## Immediate operating rule

Starting with EP-004, every executable Ralphex plan must point fresh tasks to the context capsule path and SHA256 bound by governed run authority. Before any task work begins, each fresh task must read that capsule and run independent capsule verification against the governed repository. A missing binding, failed verification, or drift is a blocker; the task must not continue with unverified context.

Every task section must include that startup instruction before its implementation steps. The capsule supplies compact invariants, non-goals, predecessor outcomes, and hashed source references; the agent may open those referenced documents on demand, but entire documents are not pasted into the capsule.

Capsules are built from structured specs with `abcp context-build --repository <path> --spec <path> --output <path>` and independently checked with `abcp context-verify --repository <path> --capsule <path>`. Source selection remains explicit and deterministic; no semantic retrieval is performed.

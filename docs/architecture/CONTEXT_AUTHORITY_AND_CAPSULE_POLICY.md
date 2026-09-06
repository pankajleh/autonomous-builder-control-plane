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

The default is relevant context, not the entire architecture corpus.## Capsule requirements

Each capsule must record:

- project/plan/task identity;
- exact base SHA;
- roadmap phase and execution-pack ID;
- selected source paths plus SHA256 hashes;
- relevant prior task outcome summaries and commit SHAs;
- explicit non-goals;
- context policy version;
- capsule SHA256.

The run authority should eventually bind `context_capsule_sha256`; until that field exists, the capsule remains a governed input recorded with the run.

## Token-efficiency rules

- Do not replay prior agent transcripts as context.
- Prefer compact task-outcome records over conversation history.
- Read deeper canonical documents on demand only when the task requires them.
- Use a deterministic component/dependency map as the primary selector; semantic retrieval may supplement but cannot override canonical authority.
- Keep repeated global guardrails small and stable so provider prompt caching can help where available, but correctness must not depend on caching.

## Fresh-task startup contract

Before implementation, every fresh task must read its capsule, verify the referenced repository/base identity, and inspect the task's relevant source files. If a referenced source hash no longer matches, execution must stop for re-authorization rather than silently using newer context.

The intended invariant is: **durable external project memory + small fresh agent sessions**, not one indefinitely growing model conversation.## Immediate operating rule (before a context compiler exists)

Starting with EP-004, every Ralphex plan must contain a compact `Context Authority` section near the top. Because Ralphex starts fresh task sessions from the plan, that section is the mandatory context capsule until ABCP generates capsules automatically.

It must name the roadmap phase, execution pack, base SHA, canonical architecture documents relevant to that EP, global invariants, explicit non-goals, and the predecessor task-output/commit information needed by later tasks.

Every task section must instruct the fresh agent to read and obey `Context Authority` before changing files. The agent may open deeper referenced documents on demand; they are not pasted wholesale into every prompt.

A future context compiler may generate the same contract and hashes mechanically, but automation must preserve this behavior rather than change its authority semantics.
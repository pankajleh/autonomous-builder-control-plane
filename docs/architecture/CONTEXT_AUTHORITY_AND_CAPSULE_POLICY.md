# Context Authority and Task Capsule Policy

## Purpose

Fresh coding/review sessions must be able to execute correctly without ChatGPT history or a long-lived agent conversation. Project context is durable in the repository; model context is disposable.

The original capsule policy became effective for EP-004. Historical artifacts are not retroactively represented as having stronger authority than they had when created.

The universal rule for new work is: no governed autonomous operation may start without a purpose-specific, verified `context-capsule-v2`. Verification binds the exact capsule bytes, repository, operation base SHA, and source bytes. A base or source change invalidates that authority and requires a newly authorized capsule; an executor must never carry a capsule across a commit/HEAD boundary into a fresh operation.

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

Every fresh operation receives a compact context capsule made of three layers:

- **Global guardrails** — project purpose, authority hierarchy, state/security invariants, completion rules, and non-negotiable boundaries.
- **Execution-pack context** — roadmap phase, parent goal, in-scope deliverables, explicit non-goals, relevant components, and expected end state.
- **Operation context** — exact objective and operation kind, owned scope, blocking criteria, relevant files/contracts, predecessor outputs, edge cases, and acceptance criteria.

The default is relevant context, not the entire architecture corpus.

## Capsule requirements

Every v2 capsule must record:

- project/plan/task identity;
- one recognized operation kind: `design-planning`, `design-review`, `implementation`, `implementation-review`, `acceptance`, `merge-authorization`, `deployment`, `recovery`, or `maintenance`;
- explicit owned scope and blocking criteria;
- exact base SHA;
- roadmap phase and execution-pack ID;
- selected source paths plus SHA256 hashes;
- relevant prior task outcome summaries and commit SHAs;
- explicit non-goals;
- context policy version;
- capsule SHA256.

`context-capsule-v2` adds operation context without changing the canonical representation or verification rules for `context-capsule-v1`. Historical v1 capsules and authorities remain parseable and evidence-readable, but a v1 capsule cannot authorize a new governed execution.

New run authority must bind `context_capsule.path` and `context_capsule.sha256`. The controller verifies the bound exact bytes, internal canonical hash, repository/base SHA, and every source hash both when authority is constructed and immediately before execution. The capsule base must equal the governed start SHA. A missing binding, wrong byte hash, wrong version, base drift, or source drift fails closed before subprocess launch. Optional bindings remain parseable only so pre-policy authority evidence retains its historical shape.

For Codex-governed execution, both `task_effort` and `review_effort` must be exactly `xhigh`. Missing or lower effort fails closed before launch.

## Token-efficiency rules

- Do not replay prior agent transcripts as context.
- Prefer compact task-outcome records over conversation history.
- Read deeper canonical documents on demand only when the task requires them.
- Use a deterministic component/dependency map as the primary selector; semantic retrieval may supplement but cannot override canonical authority.
- Keep repeated global guardrails small and stable so provider prompt caching can help where available, but correctness must not depend on caching.

## Fresh-task startup contract

Before any governed autonomous operation, every fresh executor must read its capsule, verify the environment-provided capsule path and SHA256 against exact bytes, independently run `abcp context-verify` against the governed repository/base, and inspect the operation's relevant source files. If the binding is missing or any base/source/hash check fails, execution stops for re-authorization rather than silently using newer context.

The controller supplies `ABCP_CONTEXT_CAPSULE_PATH` and `ABCP_CONTEXT_CAPSULE_SHA256` to Ralphex/Codex only from its validated immutable authority. Ambient variables with those names cannot create a binding and cannot override the governed values.

The intended invariant is: **durable external project memory + small fresh agent sessions**, not one indefinitely growing model conversation.

## Immediate operating rule

Every executable Ralphex plan must point fresh operations to the context capsule path and SHA256 bound by governed run authority. Before any operation work begins, each fresh executor must read that capsule and run independent capsule verification against the governed repository. A missing binding, failed verification, or drift is a blocker; work must not continue with unverified context.

Every task section must include that startup instruction before its implementation steps. Ralphex `full` and `tasks-only` modes are implementation-capable and require an `implementation` operation capsule; `review` mode requires a `design-review` or `implementation-review` capsule. All other operation-kind/mode combinations fail closed before launch. Every implementation-capable plan may contain at most one incomplete executable `### Task N:` or `### Iteration N:` section. Completing that section changes the commit/HEAD boundary, so any next section is a fresh operation requiring fresh authority and a capsule built at the accepted predecessor HEAD. Fenced-code exclusions use Markdown fence indentation, marker, and length rules; malformed or unterminated fence structure cannot hide executable sections from this restriction.

For `design-review` and `implementation-review`, only Critical or Major findings that apply to the capsule's current owned scope and blocking criteria block the current operation. Valid concerns for future work or outside the owned scope are recorded as deferred observations. They cannot be promoted into current blockers without new authority that brings them into scope.

The capsule supplies compact invariants, non-goals, predecessor outcomes, and hashed source references; the executor may open those referenced documents on demand, but entire documents are not pasted into the capsule.

Capsules are built from structured specs with `abcp context-build --repository <path> --spec <path> --output <path>` and independently checked with `abcp context-verify --repository <path> --capsule <path>`. Source selection remains explicit and deterministic; no semantic retrieval is performed.

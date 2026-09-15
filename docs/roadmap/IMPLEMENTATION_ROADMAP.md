# Implementation Roadmap

## Phase 0 — Foundation (started)

Goal: establish durable domain contracts before launching real Ralphex processes.

Deliverables:

- Go module and CLI;
- state machine and transition validator;
- event schema;
- append-only JSONL ledger;
- Ralphex command builder contract;
- unit tests;
- architecture/audit documentation.

Exit criteria:

```bash
go test ./...
```

must pass with no external services.

## Phase 1 — Governed single-plan execution

Deliverables:

- run authority manifest;
- process supervisor;
- Ralphex launcher;
- stdout/stderr evidence capture;
- branch/head discovery;
- implementation-complete mapping;
- deterministic post-Ralphex branch acceptance;
- CLI to launch and inspect a governed run.

Acceptance:

- real toy repo;
- exact run ID;
- process crash evidence;
- branch acceptance independent of Ralphex narration.

## Phase 2 — Recovery and blocker control

Deliverables:

- stale-worktree detection;
- owner-dead proof;
- uncommitted diff snapshot;
- retry/capacity classification;
- human-decision state and resume authority;
- restart/resume workflow.

## Phase 3 — Cross-plan scheduler and integration

Deliverables:

- accepted-candidate queue;
- final-diff risk analysis;
- disposable integration workspaces;
- textual conflict capture;
- combined acceptance;
- semantic conflict classification;
- READY_FOR_MERGE gate.

## Phase 4 — GitHub lifecycle

Deliverables:

- PR creation/update with exact head SHA;
- CI evidence ingestion;
- merge approval policy;
- expected-head merge protection;
- post-merge acceptance.

## Cross-cutting gate — Assurance governance enforcement

This gate applies before ABCP is declared ready to govern new external code-bearing software builds under the assurance policy. It does not renumber the roadmap phases.

Deliverables:

- successor activation/policy-manifest authority that pins the approved assurance policy/schema and narrowly grandfathers eligible in-flight V3 lineages;
- durable assurance-policy/model and proof-obligation identity;
- controller-owned code-bearing/documentation-only classification with exact-diff revalidation;
- A-stage failure-scenario → invariant → proof-obligation → staged-evidence completeness validation and A-to-B digest binding;
- B-stage proof-obligation/test traceability and evidence completeness checks;
- lifecycle-stage and exact candidate/result/deployment binding for every evidence requirement;
- C-stage independent execution of only the frozen branch-acceptance matrix plus bounded controller-derived correction-B reentry for implementation findings;
- mandatory smoke/integration journey enforcement for applicable builds;
- broad-precedence `ASSURANCE_MODEL_GAP` return-to-A semantics;
- assurance-escape recording and mandatory A completeness re-review;
- fresh-session validation that fails closed when required assurance authority is missing.

Exit criteria:

- one ABCP dogfood execution pack is accepted from A through C using the new policy;
- every applicable failure scenario maps to invariant/proof/evidence and every required cell has a lifecycle stage plus exact subject binding;
- at least one real smoke journey and one integration journey are controller-run independently at their assigned stages;
- injected missing/wrong-stage evidence produces `ASSURANCE_EVIDENCE_INCOMPLETE`;
- an injected missing/misclassified threat, unjustified N/A, inadequate proof, or missing boundary produces `ASSURANCE_MODEL_GAP` and cannot mint B mutation authority;
- an injected C implementation finding can only enter a finite controller-derived correction B without resetting cumulative ceilings;
- an attempted documentation-only bypass and an unactivated assurance policy/schema both fail closed;
- exact-head final review verifies the frozen model rather than defining a new one.

## Phase 5 — Service/API and dashboard

Deliverables:

- API around event stream and run actions;
- read model/projections;
- run/task/attempt/session timeline;
- evidence links;
- blocker/decision UI;
- current and historical runs without session overwrites.

## Phase 6 — Production hardening

Deliverables:

- PostgreSQL event store;
- worker leasing;
- multi-host execution;
- secret broker integration;
- RBAC;
- signed/hashed evidence manifests;
- retention/backup;
- metrics/alerts;
- policy versioning;
- risk-triggered cross-model review.

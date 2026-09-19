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

# Autonomous Builder Control Plane

A governance and control-plane layer for autonomous software development using **Ralphex** as the inner coding/orchestration engine.

## Decision

Ralphex is adopted as the **inner autonomous development orchestrator**, not as the global control plane, integration authority, production acceptance authority, or permanent system of record.

The control plane owns what live behavior testing proved must exist outside Ralphex:

- immutable run and event history;
- authority and provenance;
- deterministic branch/integration/production acceptance;
- crash recovery and stale-worktree recovery policy;
- rich blockers and human-decision escalation;
- capacity/quota/backoff policy;
- cross-plan scheduling and integration;
- semantic-conflict handling;
- READY_FOR_MERGE and production-complete state;
- optional risk-triggered independent cross-model review.

## Proven stack boundary

```text
ChatGPT / Human
        |
        v
Autonomous Builder Control Plane
  - authority + policy
  - durable event ledger
  - scheduler
  - acceptance
  - recovery
  - integration
  - escalation
        |
        v
Ralphex
  - task orchestration
  - fresh coding-agent sessions
  - retries / wait-on-limit
  - worktrees / branches / commits
  - native multi-agent review
        |
        v
Codex CLI / Claude Code
        |
        v
Ubuntu + Git + GitHub
```

## Repository status

This repository starts implementation with a deliberately small foundation slice:

1. an explicit control-plane state machine;
2. an append-only JSONL event ledger;
3. a Ralphex invocation contract and command builder;
4. tests for state-transition authority and durable event append semantics.

The initial slice has no dependency on external packages so it can compile and test on a clean Ubuntu host with Go alone.

## Canonical documentation

- [Ralphex Behavior Audit](docs/audit/RALPHEX_BEHAVIOR_AUDIT.md)
- [Architecture](docs/architecture/AUTONOMOUS_BUILDER_ARCHITECTURE.md)
- [State Machine](docs/architecture/STATE_MACHINE.md)
- [Event & Provenance Model](docs/architecture/EVENT_AND_PROVENANCE_MODEL.md)
- [Decision & Acceptance Policy](docs/architecture/DECISION_AND_ACCEPTANCE_POLICY.md)
- [Ralphex Adapter Contract](docs/architecture/RALPHEX_ADAPTER_CONTRACT.md)
- [Integration Controller](docs/architecture/INTEGRATION_CONTROLLER.md)
- [Implementation Roadmap](docs/roadmap/IMPLEMENTATION_ROADMAP.md)
- [Execution Pack EP-001](docs/execution-packs/EP-001-foundation.md)

## Build and test

```bash
go test ./...
go run ./cmd/abcp version
```

## Non-goals

This project must not rebuild capabilities already demonstrated to work reliably in Ralphex, Codex/Claude, Git, or Linux. In particular, it does not implement its own coding agent, its own worktree manager, its own branch commit engine, or another native review framework unless future evidence shows a material gap.

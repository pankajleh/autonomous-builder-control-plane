# ADR-0001: Use Ralphex as the Inner Autonomous Orchestrator

**Status:** Accepted

## Context

Live EXP-00 through EXP-09 behavior testing showed Ralphex reliably provides plan-driven task execution, fresh sessions, Git/worktree isolation, retries, native multi-agent review, and operational notifications/visibility.

The same testing showed it does not provide durable global run authority, integration acceptance, rich blocker states, or production completion authority.

## Decision

Use Ralphex as an inner orchestrator behind a control-plane adapter.

Do not fork or embed Ralphex unless future integration requirements make subprocess isolation impossible.

## Consequences

Positive:

- avoids rebuilding proven behavior;
- keeps upstream upgrade path;
- preserves clear failure boundary;
- allows Codex/Claude executor choice through Ralphex.

Negative:

- control plane must normalize Ralphex logs/process results;
- some dashboard/status quirks remain upstream concerns;
- process supervision and recovery must be implemented externally.

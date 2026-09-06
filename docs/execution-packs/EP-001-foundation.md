# EP-001 — Control-Plane Foundation

**Status:** In implementation  
**Scope:** Domain state machine, append-only event ledger, Ralphex invocation contract  
**No external dependencies**

## Objective

Create the smallest production-shaped foundation that establishes the outer control plane as an authority distinct from Ralphex.

## Required implementation

### 1. State machine

Create typed control-plane states and a conservative transition validator.

Must reject at least:

- `IMPLEMENTATION_COMPLETED → READY_FOR_MERGE`;
- `RUN_CREATED → COMPLETED`;
- `BRANCH_ACCEPTED → MERGED` without integration/merge readiness.

### 2. Event model

Create an event envelope with run identity, timestamps, state transitions, actor/source, payload, and evidence refs.

### 3. Append-only JSONL ledger

Requirements:

- create parent directories when needed;
- append exactly one JSON object per line;
- reject events missing required identity fields;
- serialize concurrent appends inside the process;
- `fsync` before returning success;
- never rewrite prior lines.

### 4. Ralphex command builder

Build structured argv for governed invocation.

Requirements:

- explicit binary path;
- explicit plan path;
- optional `--config-dir`;
- optional `--codex`;
- optional `--worktree`;
- optional mode (`--tasks-only`, `--review`);
- reject unsupported modes and empty plan path.

### 5. CLI

Initial commands:

```text
abcp version
abcp validate-transition <from> <to>
```

The CLI is only a thin interface over domain packages.

## Tests

Required:

```bash
go test ./...
```

Unit tests must cover:

- valid happy-path transitions;
- invalid direct READY_FOR_MERGE shortcut;
- event validation;
- ledger append persistence;
- ledger preserves prior lines;
- Ralphex argv construction.

## Deferred

Do not implement in EP-001:

- real Ralphex process execution;
- SQLite/PostgreSQL;
- API server;
- GitHub integration;
- worktree cleanup;
- acceptance command execution;
- cross-model review.

Those belong to later execution packs after the foundation contracts are reviewed.

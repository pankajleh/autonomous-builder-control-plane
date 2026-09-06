# Event and Provenance Model

## 1. Why an outer ledger exists

Git preserves code history. Ralphex progress logs preserve useful operational text. Neither is a permanent, immutable, queryable record of every governed run and recovery decision.

The control plane therefore owns an append-only event ledger.

## 2. Event envelope

Every event contains:

```json
{
  "schema_version": 1,
  "event_id": "...",
  "timestamp": "2026-09-06T12:00:00Z",
  "project_id": "...",
  "plan_id": "...",
  "run_id": "...",
  "attempt_id": "...",
  "task_id": "...",
  "agent_session_id": "...",
  "correlation_id": "...",
  "event_type": "STATE_TRANSITION",
  "state_from": "IMPLEMENTING",
  "state_to": "IMPLEMENTATION_COMPLETED",
  "actor": "control-plane",
  "source": "ralphex-adapter",
  "payload": {},
  "evidence_refs": []
}
```

Optional identifiers may be empty when not applicable, but `event_id`, `timestamp`, `run_id`, `event_type`, `actor`, and `source` are always required.

## 3. Evidence references

Large outputs should not be embedded directly in events. Store them as immutable artifacts and reference them by URI/path plus content hash.

Examples:

- Ralphex stdout/stderr log;
- acceptance stdout/stderr;
- Git diff snapshot;
- stale-worktree patch;
- environment manifest;
- model/tool version output;
- integration conflict report;
- test report;
- human decision artifact.

## 4. Append-only rule

Events are never edited in place.

Corrections are new events referencing the earlier event ID.

This is necessary for trustworthy crash recovery and postmortem reconstruction.

## 5. Foundation storage

The first implementation uses newline-delimited JSON with:

- append-only file open mode;
- one JSON object per line;
- mutex serialization inside the process;
- `fsync` after each appended event.

This is intentionally simple and dependency-free.

The storage interface is designed so SQLite/PostgreSQL can replace JSONL without changing domain semantics.

## 6. Run summary is derived data

Current run status, elapsed time, last fault and similar dashboard fields are projections built from the event stream.

They are disposable and rebuildable.

The event ledger is authoritative; dashboard state is not.

# Ralphex Adapter Contract

## 1. Purpose

The adapter isolates all assumptions about Ralphex invocation and observed result mapping from the rest of the control plane.

## 2. Inputs

A governed invocation includes:

- binary path;
- expected binary hash (optional but recommended for pinned production runs);
- expected source SHA metadata;
- repository path;
- plan path;
- mode;
- executor (`codex` or default Claude);
- worktree flag;
- config directory;
- task/review model + effort policy;
- timeout;
- `wait_on_limit` policy;
- correlation/run identifiers.

## 3. Command construction

Arguments are built as structured `[]string`. Never concatenate user-controlled plan paths into a shell command string.

Examples:

```text
ralphex --config-dir <dir> --codex --worktree docs/plans/feature.md
ralphex --config-dir <dir> --review docs/plans/completed/feature.md
```

## 4. Result mapping

The adapter records:

- process PID/process-group identity;
- start/end timestamps;
- exit code/signal;
- stdout/stderr artifact refs;
- progress log path;
- detected branch/worktree/plan paths when available;
- final Git SHA;
- coarse Ralphex outcome.

The adapter does **not** map Ralphex success directly to `READY_FOR_MERGE` or `COMPLETED`.

Successful full/tasks execution maps at most to `IMPLEMENTATION_COMPLETED` pending controller acceptance.

## 5. Recovery-safe ownership

Before deleting a stale worktree, the supervisor must establish that the owning execution is no longer alive and must snapshot uncommitted changes.

## 6. Notifications and dashboard

Ralphex notifications and dashboard are supplemental. Their status never overrides control-plane ledger state.

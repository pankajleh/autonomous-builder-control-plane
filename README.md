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

EP-002 governed single-plan execution is code-complete and under review. The
repository now includes:

1. an explicit control-plane state machine;
2. an append-only JSONL event ledger;
3. validated immutable run authority and immutable evidence artifacts;
4. supervised, bounded-output Ralphex subprocess execution;
5. deterministic acceptance of the exact candidate branch produced by Ralphex;
6. an explicit governed-run CLI.

The implementation has no dependency on external Go packages. Real pinned
Ralphex acceptance on the Ubuntu behavior-lab host remains pending after code
review.

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
- [Execution Pack EP-002](docs/execution-packs/EP-002-governed-single-plan.md)
- [EP-002 Implementation Plan](docs/plans/ep-002-governed-single-plan.md)

## Build and test

```bash
go test ./...
go run ./cmd/abcp version
```

## Governed single-plan run

Launch a governed run with explicit authority, ledger, and evidence locations:

```bash
go run ./cmd/abcp run \
  --manifest /path/to/authority.json \
  --ledger /path/to/events.jsonl \
  --evidence-root /path/to/evidence
```

Example authority manifest:

```json
{
  "run_id": "feature-123",
  "repository": {
    "path": "/srv/project",
    "identity": "example/project",
    "remotes": {"origin": "https://example.invalid/example/project.git"},
    "default_branch": "main",
    "start_sha": "0123456789abcdef0123456789abcdef01234567"
  },
  "plan": {
    "path": "docs/plans/feature.md",
    "sha256": "0000000000000000000000000000000000000000000000000000000000000000"
  },
  "ralphex": {
    "binary_path": "/opt/ralphex/bin/ralphex",
    "binary_sha256": "0000000000000000000000000000000000000000000000000000000000000000",
    "source_sha": "pinned-source-revision",
    "mode": "full",
    "timeout": "2h",
    "wait_on_limit": "30m"
  },
  "executor": {
    "executor": "codex",
    "task_model": "gpt-5.6-sol",
    "task_effort": "high",
    "review_model": "gpt-5.6-sol",
    "review_effort": "high"
  },
  "worktree": {
    "enabled": true,
    "branch": "feature-123"
  },
  "acceptance": [
    {"name": "tests", "class": "unit", "required": true, "timeout": "10m", "argv": ["go", "test", "./..."]}
  ],
  "policy_version": "branch-v1"
}
```

Repository identity, complete remotes, and the default branch are required;
the identity must match the repository path encoded by at least one remote.
Timeouts use Go duration syntax and must be positive; `wait_on_limit` may be
`0s` to disable retries explicitly. At least one acceptance command must be
required. Supported Ralphex modes are `full`,
`tasks-only`, and `review`. Worktree mode
requires an explicit new branch name and is unavailable with review mode. The
controller verifies the Git repository root, complete remote set, repository,
plan, binary, and branch identities; runs acceptance against the actual clean
candidate checkout with an allowlisted isolated environment; and limits each captured
stdout/stderr stream to 16 MiB. Success prints `BRANCH_ACCEPTED`. Failures return
nonzero and preserve the JSONL ledger plus immutable artifacts under
`<evidence-root>/<run_id>/`.

The ledger path must be outside `<evidence-root>/<run_id>/` so append-only
events cannot overlap immutable evidence artifacts.

## Non-goals

This project must not rebuild capabilities already demonstrated to work reliably in Ralphex, Codex/Claude, Git, or Linux. In particular, it does not implement its own coding agent, its own worktree manager, its own branch commit engine, or another native review framework unless future evidence shows a material gap.

Ralphex still owns implementation worktrees and candidate commits. The
controller only materializes a temporary detached candidate checkout after
Ralphex cleanup so independent acceptance runs against the correct branch.

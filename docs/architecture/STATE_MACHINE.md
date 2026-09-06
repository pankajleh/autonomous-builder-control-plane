# Control-Plane State Machine

## 1. Purpose

Ralphex status is an input signal, not the control-plane state machine.

The control plane must distinguish implementation completion, branch acceptance, integration acceptance, merge readiness, and production completion.

## 2. Primary states

| State | Meaning |
|---|---|
| `RUN_CREATED` | Run identity exists; execution has not started. |
| `AUTHORITY_VALIDATED` | Repository, plan, policy and tool authority are pinned and valid. |
| `EXECUTION_STARTING` | Process launch is being prepared. |
| `IMPLEMENTING` | Ralphex task/full execution is active. |
| `IMPLEMENTATION_COMPLETED` | Ralphex local implementation/review lifecycle completed successfully. |
| `BRANCH_ACCEPTANCE_PENDING` | Controller is executing independent branch acceptance. |
| `BRANCH_ACCEPTED` | Required branch acceptance passed. |
| `INTEGRATION_PENDING` | Candidate awaits integration evaluation. |
| `INTEGRATING` | Disposable integration workspace is active. |
| `INTEGRATION_CONFLICT` | Textual/structural Git conflict requires policy resolution or escalation. |
| `SEMANTIC_CONFLICT` | Merge can be made textual-clean but combined requirements cannot pass together. |
| `INTEGRATION_ACCEPTED` | Combined candidate passed required acceptance. |
| `READY_FOR_MERGE` | All merge preconditions are satisfied. |
| `MERGED` | Candidate merged to governed target branch. |
| `PRODUCTION_ACCEPTANCE_PENDING` | Runtime/deployment acceptance is being evaluated. |
| `PRODUCTION_ACCEPTED` | Required production/runtime acceptance passed. |
| `COMPLETED` | Governed run reached its final completion policy. |
| `RETRYING` | A retryable failure is waiting or restarting. |
| `CAPACITY_WAIT` | Model/service capacity policy is waiting. |
| `RECOVERY_REQUIRED` | Crash/stale-worktree/manual recovery classification is required. |
| `HUMAN_DECISION_REQUIRED` | A bounded human authority decision is required. |
| `SECRET_REQUIRED` | Execution needs a secret not available under current policy. |
| `EXTERNAL_DEPENDENCY` | External system state blocks progress. |
| `VALIDATION_UNAVAILABLE` | Required validator cannot currently execute. |
| `POLICY_BLOCKED` | Policy explicitly forbids continuation. |
| `FAILED` | Terminal failure. |
| `CANCELLED` | Authorized cancellation. |

## 3. Key invariants

1. `IMPLEMENTATION_COMPLETED` cannot transition directly to `READY_FOR_MERGE`.
2. `BRANCH_ACCEPTED` requires controller-generated acceptance evidence.
3. A multi-branch change cannot become `READY_FOR_MERGE` without `INTEGRATION_ACCEPTED`.
4. `INTEGRATION_CONFLICT` and `SEMANTIC_CONFLICT` are not equivalent.
5. `HUMAN_DECISION_REQUIRED` must identify an explicit question and accepted answer space where possible.
6. `FAILED` is terminal unless a new run/attempt is created under explicit recovery authority.
7. `COMPLETED` is a control-plane conclusion, never copied blindly from the Ralphex dashboard.

## 4. Allowed-transition baseline

The implementation encodes an initial conservative transition set. Future additions require an ADR because permissive transitions weaken governance.

Happy path:

```text
RUN_CREATED
→ AUTHORITY_VALIDATED
→ EXECUTION_STARTING
→ IMPLEMENTING
→ IMPLEMENTATION_COMPLETED
→ BRANCH_ACCEPTANCE_PENDING
→ BRANCH_ACCEPTED
→ INTEGRATION_PENDING
→ INTEGRATING
→ INTEGRATION_ACCEPTED
→ READY_FOR_MERGE
→ MERGED
→ PRODUCTION_ACCEPTANCE_PENDING
→ PRODUCTION_ACCEPTED
→ COMPLETED
```

Single-branch work may still pass through `INTEGRATION_PENDING` / `INTEGRATING`; the integration controller can treat the operation as a trivial one-candidate integration so the state model remains uniform.

## 5. Retry/blocker semantics

A retryable execution failure may enter `RETRYING` and then return to `EXECUTION_STARTING`.

Capacity-specific flow:

```text
IMPLEMENTING
→ CAPACITY_WAIT
→ EXECUTION_STARTING
```

Crash flow:

```text
IMPLEMENTING
→ RECOVERY_REQUIRED
→ EXECUTION_STARTING
```

Decision flow:

```text
IMPLEMENTING
→ HUMAN_DECISION_REQUIRED
→ AUTHORITY_VALIDATED
→ EXECUTION_STARTING
```

The new authority must be recorded before resume.

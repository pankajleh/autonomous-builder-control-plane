# Decision, Escalation and Acceptance Policy

## 1. Decision taxonomy

The control plane classifies unresolved blockers before asking a human.

### `HUMAN_DECISION_REQUIRED`

A bounded product/architecture/business decision is missing.

Required event payload:

- exact question;
- why execution cannot decide safely;
- allowed options if enumerable;
- affected run/plan/task;
- evidence supporting the request.

### `SECRET_REQUIRED`

A required credential or secret is unavailable.

The secret value itself must never be written to the event ledger or Git.

### `EXTERNAL_DEPENDENCY`

A non-secret external condition is unresolved: service availability, approval, deployment, DNS, vendor state, etc.

### `VALIDATION_UNAVAILABLE`

A required validator cannot run. The system must not silently substitute weaker checks unless policy explicitly allows it.

### `POLICY_BLOCKED`

Continuation violates a configured policy.

## 2. Progressive autonomy

The system should resolve everything covered by policy without human interruption.

Human intervention is reserved for genuinely missing authority, not routine agent uncertainty.

## 3. Branch acceptance

A branch is accepted only when all required acceptance commands run under controller authority and pass.

Each command record includes:

- exact argv;
- working directory;
- environment policy version;
- timeout;
- start/end timestamp;
- exit code;
- stdout/stderr artifact refs;
- resulting Git SHA and clean/dirty state.

## 4. Acceptance classes

Recommended classes:

- `unit`
- `static`
- `integration`
- `smoke`
- `security`
- `migration`
- `contract`
- `runtime`
- `production`

A plan or project policy selects required classes.

## 5. Review policy

Default:

```text
Ralphex native multi-agent review
+ deterministic controller acceptance
```

Risk-triggered addition:

```text
independent cross-model review
```

Cross-model review is never a replacement for deterministic acceptance.

## 6. Failure semantics

A required command failure produces failed acceptance even if Ralphex previously emitted COMPLETED.

A required command being unavailable produces `VALIDATION_UNAVAILABLE`, not PASS.

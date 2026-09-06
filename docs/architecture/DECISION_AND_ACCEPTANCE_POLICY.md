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

### Independent-review provider fallback

When a risk-triggered independent cross-model review is required, the configured independent provider is attempted first. If that provider fails before delivering a complete review because of capacity/session limits, quota/auth/provider outage, timeout, or tool failure, the failure is recorded and the review defaults to a controller-authority architecture/security review against the same exact Git SHA. The current controller authority is ChatGPT unless project policy names another reviewer.

The fallback reviewer must use the same Critical/Major scope and may return a clean gate only when deterministic acceptance for that exact code state has passed. A substantive Critical/Major finding from an independent provider is not a provider failure and cannot be bypassed by fallback; it must be corrected and reviewed again.

The merge audit must record the exact reviewed SHA, reviewer mode (`cross_model` or `controller_fallback`), provider-failure reason when applicable, deterministic acceptance result, and final Critical/Major verdict.

## 6. Failure semantics

A required command failure produces failed acceptance even if Ralphex previously emitted COMPLETED.

A required command being unavailable produces `VALIDATION_UNAVAILABLE`, not PASS.

# Integration Controller

## 1. Problem proved by EXP-08

Two branches can independently pass implementation, tests and review yet fail to merge or fail combined semantics.

Therefore integration is an explicit governed phase.

## 2. Inputs

Only `BRANCH_ACCEPTED` candidates can enter integration.

Each candidate provides:

- exact branch name;
- exact accepted head SHA;
- base SHA;
- final changed paths;
- acceptance evidence;
- plan authority;
- risk metadata.

## 3. Pipeline

```text
select candidates
→ verify accepted heads unchanged
→ create disposable integration workspace
→ merge/rebase candidate set in deterministic order
→ capture textual conflicts
→ apply policy-safe resolutions only
→ run combined acceptance
→ classify semantic conflict if combined requirements fail
→ record INTEGRATION_ACCEPTED or blocker/failure
```

## 4. Conflict taxonomy

### Textual/structural conflict

Git cannot produce a clean tree automatically.

State: `INTEGRATION_CONFLICT`.

### Semantic conflict

A clean tree can be produced, but requirements/acceptance cannot simultaneously pass.

State: `SEMANTIC_CONFLICT`.

This distinction is essential because semantic conflict cannot be solved by blindly choosing one side of a Git hunk.

## 5. Parallel scheduling implication

Do not decide conflict risk only from planned file ownership. EXP-08 showed review/fix activity can expand the changed-file set.

Scheduler and integration policy must examine **actual final diffs**.

## 6. READY_FOR_MERGE gate

`READY_FOR_MERGE` requires:

- branch acceptance;
- integration acceptance;
- expected candidate SHAs unchanged;
- required review policy satisfied;
- no open blocker;
- complete provenance.

# Context-Bound Operation Policy — Implementation Plan

Purpose: make context completeness a fail-closed platform invariant for governed autonomous execution without breaking historical evidence readability.

Non-goals: no EP-005 CI semantics, no GitHub writes/merge changes, no scheduler redesign, no Desktop Commander/Ralphex/Codex product dependency.

### Task 1: Enforce context-bound autonomous operations

- [ ] Before any implementation work, require `ABCP_CONTEXT_CAPSULE_PATH` and `ABCP_CONTEXT_CAPSULE_SHA256`, verify exact bytes, and run `abcp context-verify` against the exact task base. Stop on any mismatch or source drift.
- [ ] Extend the capsule format additively with a v2 operation context while preserving canonical parsing/verification of historical v1 capsules. V2 must bind operation kind, owned scope, blocking criteria, explicit non-goals, exact base SHA, predecessor outcomes, and hashed sources.
- [ ] Define recognized operation kinds covering design/planning, design review, implementation, implementation review, acceptance, merge authorization, deployment, recovery, and maintenance.
- [ ] Make `run.New` fail closed unless execution is bound to a verified v2 operation capsule. Historical v1 authorities remain parseable/evidence-readable but cannot start new governed execution.
- [ ] For Codex-governed execution, require `task_effort=xhigh` and `review_effort=xhigh`; lower or missing effort must be rejected before subprocess launch.
- [ ] Propagate capsule path/hash to Ralphex/Codex only from validated immutable authority; ambient `ABCP_CONTEXT_CAPSULE_*` values must never create or override a binding.
- [ ] For implementation operations, reject a task plan containing more than one incomplete executable `### Task N:` / `### Iteration N:` section so one capsule cannot cross a commit/HEAD boundary. A fresh task requires fresh authority and a fresh capsule at the predecessor HEAD.
- [ ] Document review semantics: only current owned-scope Critical/Major findings block; valid future/out-of-scope concerns are recorded as deferred observations and cannot be promoted into current blockers.
- [ ] Update the existing context-authority policy so the universal rule is explicit: no governed autonomous operation without a purpose-specific verified capsule; source/base drift requires re-authorization.
- [ ] Add regression tests for v1 readability, v2 required fields, missing/wrong capsule, source/base drift, hostile ambient capsule variables, Codex high/empty effort rejection, xhigh acceptance, and multi-incomplete-task rejection.
- [ ] Run `gofmt`, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`.
- [ ] Commit only this policy/enforcement task; do not modify EP-005 CI-ingestion implementation files.

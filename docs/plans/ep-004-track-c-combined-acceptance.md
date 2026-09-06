# EP-004 Track C — Combined Acceptance and Semantic Conflict Classification

## Context Authority

- Roadmap: Phase 3 — Cross-plan scheduler and integration.
- Execution pack: `docs/execution-packs/EP-004-cross-plan-scheduler-integration.md`.
- Accepted Track A foundation SHA: `615a06abccf58605cb76917d6df4f4ad2fd609ea`.
- Frozen shared contract: `internal/scheduler/` exactly as accepted at Track A; this track must not edit it.
- Owned implementation area: new `internal/combinedacceptance/` package and its tests/docs only.
- Fresh tasks must verify the bound context capsule before implementation and read capsule-referenced authorities on demand.
- Global invariants: controller-owned deterministic acceptance outranks agent claims; exact provenance is mandatory; ambiguous state fails closed; this track cannot grant `READY_FOR_MERGE`.
- Non-goals: disposable workspace implementation, textual conflict resolution, state-machine wiring, GitHub lifecycle, dashboard/API, deployment, production acceptance.

### Task 1: Implement combined acceptance and semantic-conflict classification

- [ ] Verify the governed context capsule before reading implementation files or making changes.
- [ ] Consume `internal/scheduler` candidate/risk contracts read-only and reuse the existing controller-owned `internal/acceptance` executor rather than duplicating branch-acceptance mechanics.
- [ ] Add a new `internal/combinedacceptance/` package that evaluates a caller-supplied exact integrated target and preserves exact candidate/integration provenance.- [ ] Run combined deterministic acceptance against that exact integrated target with immutable command/Git evidence and fail closed if target identity changes during evaluation.
- [ ] Define deterministic semantic-conflict classification from combined acceptance evidence and governed policy signals only; no model narrative may substitute for acceptance evidence.
- [ ] At minimum distinguish clean combined acceptance, deterministic semantic-conflict evidence, and validation-unavailable/fail-closed outcomes suitable for later serial gate assembly.
- [ ] Preserve enough evidence to explain which required checks or policy signals caused the classification while excluding secrets and unbounded output.
- [ ] Reject incomplete candidate provenance, incomplete integration provenance, stale/moved integrated heads, ambiguous policy input, or unavailable required validation.
- [ ] Return immutable evidence/results only; do not perform integration workspace creation, textual conflict resolution, state transitions, or merge authorization.
- [ ] Add tests for combined PASS, required-check failure, semantic-conflict classification, moved-head rejection, incomplete provenance, deterministic evidence, and immutable returned values.
- [ ] Keep `internal/scheduler/`, `internal/integrationworkspace/`, canonical state machine, branch acceptance contracts, and merge authority untouched.
- [ ] Run `gofmt -w cmd internal`, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`.
- [ ] Mark only this task complete and commit the bounded Track C implementation.

## Track boundary

This plan consumes a later integration target supplied by serial assembly. It must not create that workspace and must stop before `READY_FOR_MERGE`.
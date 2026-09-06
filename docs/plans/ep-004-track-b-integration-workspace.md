# EP-004 Track B — Disposable Integration Workspace and Textual Conflict Capture

## Context Authority

- Roadmap: Phase 3 — Cross-plan scheduler and integration.
- Execution pack: `docs/execution-packs/EP-004-cross-plan-scheduler-integration.md`.
- Accepted Track A foundation SHA: `615a06abccf58605cb76917d6df4f4ad2fd609ea`.
- Frozen shared contract: `internal/scheduler/` exactly as accepted at Track A; this track must not edit it.
- Owned implementation area: new `internal/integrationworkspace/` package and its tests/docs only.
- Fresh tasks must verify the bound context capsule before implementation and read capsule-referenced authorities on demand.
- Global invariants: candidate branches are immutable inputs; Git operations use structured argv; ambiguous state fails closed; this track cannot grant `READY_FOR_MERGE`.
- Non-goals: combined acceptance, semantic-conflict classification, state-machine wiring, GitHub lifecycle, dashboard/API, deployment, production acceptance.

### Task 1: Implement disposable integration workspace and textual conflict evidence

- [ ] Verify the governed context capsule before reading implementation files or making changes.
- [ ] Reuse `internal/scheduler` accepted-candidate/risk contracts as read-only inputs; do not revise the frozen Track A contract.
- [ ] Add a controller-owned disposable integration workspace abstraction under `internal/integrationworkspace/`.- [ ] Materialize integration only from exact committed baseline/candidate SHAs in a controller-owned temporary root; never modify candidate branches or the primary working tree.
- [ ] Apply candidates in deterministic governed order and capture exact Git argv/outcome/evidence for each integration step.
- [ ] Detect and capture textual conflicts deterministically, including conflicting paths and immutable evidence sufficient for later serial gate assembly.
- [ ] Fail closed on missing/non-commit objects, invalid ancestry, ambiguous Git output, unexpected repository state, path escape/symlink hazards, evidence truncation, or cleanup uncertainty.
- [ ] Cleanup must be bounded and scoped only to the controller-created disposable workspace; preserve evidence before destructive cleanup.
- [ ] Return an immutable result distinguishing clean textual integration from textual conflict/unavailable states; do not classify semantic conflicts.
- [ ] Add tests for clean multi-candidate integration, deterministic order, textual conflict capture, immutable candidate branches, malformed/missing object rejection, cleanup scope, and evidence preservation.
- [ ] Keep `internal/scheduler/`, `internal/combinedacceptance/`, canonical state machine, branch acceptance, and merge authority untouched.
- [ ] Run `gofmt -w cmd internal`, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`.
- [ ] Mark only this task complete and commit the bounded Track B implementation.

## Track boundary

This plan produces evidence for later serial integration authority. It must stop before combined acceptance, semantic-conflict classification, or `READY_FOR_MERGE`.
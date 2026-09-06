# EP-004 Foundation — Accepted Candidate Queue and Final-Diff Risk

## Context Authority

- Roadmap: Phase 3 — Cross-plan scheduler and integration.
- Execution pack: `docs/execution-packs/EP-004-cross-plan-scheduler-integration.md`.
- Governed base identity: use the exact `base_sha` in the verified capsule/run authority; never infer from chat or dashboard state.
- Capsule: `/home/devagent/abcp-runtime/ep004-foundation/context.json`.
- Fresh tasks must verify that capsule against `/home/devagent/autonomous-builder-control-plane` before reading implementation files or making changes.
- Global invariants: controller owns acceptance/integration state; Ralphex is inner implementation orchestration; exact Git/evidence provenance is mandatory; unknown state fails closed.
- Non-goals: integration workspaces, textual merge resolution, semantic conflict resolution, GitHub lifecycle, dashboard/service APIs, deployment, production acceptance.
- Predecessor: EP-003 merged as `db56b1f8cf32561be6b707db4bbf046f4c24e067`.

### Task 1: Implement accepted-candidate queue and final-diff risk foundation

- [x] Before any implementation, run `/home/devagent/autonomous-builder-control-plane/bin/abcp context-verify --repository /home/devagent/autonomous-builder-control-plane --capsule /home/devagent/abcp-runtime/ep004-foundation/context.json`; stop immediately if verification fails.
- [x] Read the capsule-referenced roadmap, state-machine, event/provenance, decision/acceptance, context-policy, current-progress, and EP-004 sources on demand.
- [x] Add a controller-owned accepted-candidate model carrying exact project/plan/run/attempt identity, repository, branch, start/head SHA, acceptance evidence references, and acceptance timestamp/policy identity.
- [x] Implement an append-only deterministic accepted-candidate queue with stable ordering, duplicate/replay rejection, and immutable returned values.
- [x] Implement deterministic final-diff risk analysis from committed Git SHAs only; do not inspect or trust uncommitted worktree state.
- [x] At minimum classify disjoint path sets, overlapping path sets, and contract-sensitive/shared-authority overlap, with evidence explaining the classification.
- [x] Reject missing objects, non-descendant/invalid candidate history, malformed Git output, ambiguous rename/path state, or acceptance provenance that is incomplete.
- [x] Keep the foundation's code ownership under a new scheduler/integration-contract package wherever practical; avoid changing canonical state-machine or acceptance contracts unless an existing contract makes the roadmap deliverable impossible.
- [x] Expose a frozen, minimal shared contract for the later integration-workspace and combined-acceptance tracks; document package/file ownership so those tracks can run in parallel without editing the same authoritative contract.
- [x] Emit/return risk evidence sufficient for a later integration controller to decide whether candidates may enter `INTEGRATION_PENDING`; this task must not itself grant `READY_FOR_MERGE`.
- [x] Add tests for deterministic ordering, duplicate rejection, exact-SHA provenance, disjoint changes, overlapping changes, contract-sensitive overlap, rename/path ambiguity, invalid ancestry, and dirty-worktree irrelevance.
- [x] Run `gofmt -w cmd internal`, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`.
- [x] Mark only this task complete and commit the bounded implementation.

## Parallel fan-out after acceptance

Do not implement these tracks in this plan. After this foundation is independently accepted, the controller may launch two fresh capsule-bound plans in parallel from the exact accepted foundation SHA:

- Track B owns disposable integration workspace creation and textual conflict capture.
- Track C owns combined-acceptance orchestration and semantic-conflict classification.

Both tracks must treat the shared foundation contract as read-only. A later serial gate-assembly task integrates their accepted heads and implements the `READY_FOR_MERGE` gate.

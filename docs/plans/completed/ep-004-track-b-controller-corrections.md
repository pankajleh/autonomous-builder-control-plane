# EP-004 Track B — Controller Review Corrections

## Context Authority

- Roadmap: Phase 3 — Cross-plan scheduler and integration.
- Parent execution pack: `docs/execution-packs/EP-004-cross-plan-scheduler-integration.md`.
- Parallel fan-out base: `6ea59c6c1c8dcf2f56387caa8b9c5051105c10ef`.
- Track B implementation head under review: `e3b687e0e81457ff76bb3422befb0caee3794781`.
- Frozen Track A scheduler contract remains read-only.
- Track C has completed independently; this correction must not edit Track C files.
- This is a serial post-track correction after parallel execution has stopped.
- No READY_FOR_MERGE, state-machine, GitHub lifecycle, dashboard/API, or deployment authority is added.

### Task 1: Correct Track B determinism and acceptance reliability

- [x] Verify the bound context capsule before any implementation change.
- [x] Make synthetic integration commit identity reproducible across wall-clock time for the same exact baseline, accepted candidate order, candidate commits, and policy inputs; do not rely on two integrations occurring within the same second.
- [x] Add a regression that repeats an otherwise identical integration after crossing a wall-clock second boundary and proves identical final integrated commit/result identity where the contract requires determinism.
- [x] Remove the inherited flake in `TestRunCapturesExternalSignal`: the test must synchronize process readiness and external SIGTERM delivery so deterministic acceptance cannot randomly observe helper exit 95. Prefer a test-only fix; production supervisor semantics must not change unless evidence proves they are wrong.
- [x] Preserve bounded Git output, replacement-object suppression, exact merge-parent verification, source-branch immutability, conflict capture, cleanup/evidence ordering, and Track B scope.
- [x] Add/adjust targeted tests for the two controller findings and prove the previous failure mode cannot recur by repeated targeted execution.
- [x] Run `gofmt -w cmd internal`, `go test ./...`, repeated targeted supervisor/integration determinism tests, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`.
- [x] Mark only this correction task complete and commit the bounded correction.

## Controller findings being corrected

1. **Major — synthetic integration identity depends on wall clock.** Track B uses ordinary `git merge` commits without deterministic author/committer dates. A controlled reproduction using the same baseline and candidates with a two-second delay produced different final integration SHAs. The implementation test currently passes only when both integrations occur inside the same Git timestamp second.
2. **Acceptance reliability — inherited supervisor signal test is racy.** ABCP branch acceptance for Track B failed because `TestRunCapturesExternalSignal` returned helper exit 95 instead of SIGTERM. Ten immediate targeted reruns plus the complete validation suite passed, demonstrating a nondeterministic test race rather than a Track B feature regression. The acceptance gate must be deterministic before Track B can be accepted.

## Required end state

Track B must finish on one clean exact SHA with deterministic synthetic integration identity, a non-flaky signal test, complete controller acceptance evidence, and no edits to Track C or frozen scheduler contracts.

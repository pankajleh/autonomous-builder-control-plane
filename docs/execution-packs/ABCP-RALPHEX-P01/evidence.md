# ABCP Ralphex Execution Integration P01 Evidence

## Runtime identity

- Repo B base: `ff2202884cad24ac1b4ced70c8100d0ebc392827`
- Ralphex executable: `/home/devagent/ralphex-behavior-lab/tools/ralphex/.bin/ralphex`
- Ralphex executable SHA-256: `9ad47b083aaaf34eed4b35ca82f3c92d1a3b7641921be0ea606cbd1cd1c3adac`
- Ralphex detached source HEAD: `319e30618352a1b43e4be1b8a894c6c05e6d5fa8`

## Profile change

The private Repo B admission manifest template now pins the approved Ralphex executable, executable digest, and source revision. The public admission profile identifier, Repo C contract, repository settings, executor policy, worktree policy, acceptance policy, and `internal/run` to `internal/ralphex` path are unchanged. The independent local smoke acceptance command remains `/usr/bin/true`; it is not the Ralphex execution command.

## Regression coverage

`TestAdmissionMaterializesV2InputsAndReplaysOrConflicts` uses a distinct executable as the controller-owned Ralphex runtime and verifies that admission materialization preserves its path, digest, source revision, mode, and time bounds exactly instead of substituting the ABCP launcher or acceptance executable.

`TestRunnerCopiesGitIgnoredAuthorityPlanForRalphexWorktree` verifies that the controller supplies a hash-identical, Git-visible execution copy to worktree mode, keeps the admitted authority plan immutable, and removes the source-checkout copy. `TestRunnerRejectsTrackedPlanWhenStartCommitBlobDiffers` proves worktree execution fails closed when a hidden index flag masks source-checkout plan bytes that differ from the governed start-commit blob. `TestRunnerPinnedRalphexCopiesGitIgnoredAuthorityPlanIntoWorktree` runs the approved pinned Ralphex binary with a deterministic fake Codex executor and proves the real worktree implementation can read the copy and produce a candidate branch. `TestRunnerCleansGitIgnoredExecutionPlanAfterFailureAndCancellation` covers nonzero execution and pre-launch cancellation cleanup. `TestRunnerRecoversOwnedExecutionPlanAfterInterruptedCleanup` proves the next repository lease holder removes a hash-matching stale copy before validation, `TestRunnerRejectsTamperedInterruptedExecutionPlan` proves recovery fails closed without deleting changed bytes, and `TestRepositoryExecutionLeaseSerializesOverlappingRuns` proves distinct runs cannot expose concurrent handoffs in one checkout.

`TestAdmissionPlanHasExactlyOneExecutableTaskAndPreservesAuthorizedMarkdown` verifies the single executable Task 1 contract and collision-safe preservation of authorized Markdown. Admission tests also cover rejection of repositories that hide the reserved handoff namespace, and service API tests enforce the pinned parser's per-line bound.

## Gates

- `sha256sum /home/devagent/ralphex-behavior-lab/tools/ralphex/.bin/ralphex`: PASS, exact approved executable digest.
- `git -C /home/devagent/ralphex-behavior-lab/tools/ralphex rev-parse HEAD`: PASS, exact approved detached source revision.
- Bounded `abcp serve` startup with the real private profile and a fresh service root: PASS; the controller remained healthy until the expected timeout shutdown and emitted no error output.
- `go test -count=1 ./internal/runadmission -run 'TestAdmissionMaterializesV2InputsAndReplaysOrConflicts' -v`: PASS.
- `ABCP_TEST_PINNED_RALPHEX=/home/devagent/ralphex-behavior-lab/tools/ralphex/.bin/ralphex go test -count=1 ./internal/run -run TestRunnerPinnedRalphexCopiesGitIgnoredAuthorityPlanIntoWorktree -v`: PASS.
- `ABCP_TEST_PINNED_RALPHEX=/home/devagent/ralphex-behavior-lab/tools/ralphex/.bin/ralphex go test -count=1 ./internal/serviceapi ./internal/runadmission ./internal/run ./internal/ralphex`: PASS.
- `ABCP_TEST_PINNED_RALPHEX=/home/devagent/ralphex-behavior-lab/tools/ralphex/.bin/ralphex go test -count=1 ./...`: PASS.
- `go test -count=1 -race ./...`: PASS.
- `go test -count=1 -race ./internal/serviceapi ./internal/runadmission ./internal/run ./internal/ralphex`: PASS after the final run-specific ignore defense.
- `go vet ./...`: PASS.

The bounded correction leaves Repo C source and public DTO shape unchanged. Repo B now enforces the parser-safe admission contract, rejects profiles whose ignore rules hide the handoff namespace, generates one executable Task 1 wrapper, and supplies real Ralphex a verified temporary plan copy without mutating the admitted authority plan.

## Final live C → B → Ralphex proof

- Final executable Repo B HEAD before this evidence successor: `ad736b1`.
- Fresh Repo C integration run: `admission-4c44adab5d686d0c2a5b6467dd8c92257d4eff86ca93ff625c7946d6f641ec54`.
- Materialized Ralphex timeout: `10m0s`; real Ralphex process outcome: `succeeded`, exit code `0`.
- Candidate branch: `abcp/admission-4c44adab5d686d0c2a5b6467dd8c92257d4eff86ca93ff625c7946d6f641ec54`.
- Candidate HEAD: `c4920e6f281dad32218b7e1df0705463f40e2016`.
- Existing ABCP acceptance command: PASS.
- Final candidate checkout: clean.
- Ledger terminal acceptance transition: `BRANCH_ACCEPTED`.
- Existing C → B replay and ambiguous-outcome reconciliation harness: `LOCAL_INTEGRATION_EXERCISE_PASS`.

The live proof therefore exercised the required chain on the final executable tree:
Repo C authorization → Repo B admission → `abcp run` → `internal/run` → `internal/ralphex` → approved real Ralphex/Codex → candidate branch/evidence → existing ABCP acceptance.

## Review closure

Review 1 produced accepted corrections. Review 2 corrections are included through `ad736b1` and were followed by focused, broad, race, vet, pinned-runtime, and final live integration verification. A subsequent automatic review iteration was stopped without applying changes in order to honor the frozen `No Review 3` contract. The branch is now at the human merge gate.

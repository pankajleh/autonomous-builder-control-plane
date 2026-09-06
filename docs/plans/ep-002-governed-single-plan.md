# Plan: EP-002 — Governed Single-Plan Execution

## Authority

Execution pack: `docs/execution-packs/EP-002-governed-single-plan.md`
Architecture authority:
- `docs/architecture/AUTONOMOUS_BUILDER_ARCHITECTURE.md`
- `docs/architecture/STATE_MACHINE.md`
- `docs/architecture/EVENT_AND_PROVENANCE_MODEL.md`
- `docs/architecture/RALPHEX_ADAPTER_CONTRACT.md`
- `docs/architecture/DECISION_AND_ACCEPTANCE_POLICY.md`

This plan implements one governed Ralphex lifecycle only. Do not implement recovery, multi-plan scheduling, integration/merge authority, GitHub PR automation, service APIs, or production deployment in this plan.

## Global invariants

1. Ralphex remains a subprocess boundary; do not import/fork Ralphex internals.
2. Never construct shell command strings from user-controlled inputs. Use structured argv and `exec.CommandContext`.
3. `IMPLEMENTATION_COMPLETED` must never imply `BRANCH_ACCEPTED`, `READY_FOR_MERGE`, or `COMPLETED`.
4. Controller acceptance runs independently after Ralphex returns success.
5. Evidence is immutable per artifact and referenced by SHA256.
6. Exact repository/plan/binary identities must be validated before execution.
7. Keep the implementation Go-standard-library-only unless a dependency is strictly required and separately justified.
8. Tests must not depend on a live Codex/Claude/Ralphex service; use harmless local fake executables for deterministic unit/integration tests.
9. Linux process-group behavior must be explicit and testable; keep platform-specific process configuration behind small files/build tags when necessary.
10. Do not weaken existing state-transition or ledger tests.

## Validation baseline

Run after every task:

```bash
gofmt -w cmd internal
go test ./...
make smoke
```

### Task 1: Implement validated immutable run authority

- [x] Add an `internal/authority` package with typed manifest input for run ID, canonical repository path, repository identity/remotes, default branch, start SHA, plan path + SHA256, Ralphex binary path + SHA256, expected Ralphex source SHA metadata, execution mode, executor/model/effort policy, worktree policy, acceptance commands, and policy version.
- [x] Add validation that rejects missing run ID, repository path, start SHA, plan path/hash, Ralphex binary path/hash, unsupported mode, and empty acceptance argv.
- [x] Canonicalize repository, plan and Ralphex binary paths before producing validated authority; ensure the plan resolves within the governed repository.
- [x] Produce a validated immutable/value authority representation only through a constructor/validator; do not expose mutating setters.
- [x] Add deterministic canonical serialization and SHA256 authority hash; same semantic manifest must hash identically.
- [x] Add unit tests for required-field rejection, path-boundary rejection, supported modes, and stable hashing.
- [x] Run the validation baseline, mark Task 1 complete, and commit.

### Task 2: Implement immutable evidence artifact store

- [x] Add `internal/evidence` with a run-scoped store rooted under a caller-supplied evidence directory, never implicitly inside a target repository.
- [x] Write artifacts atomically into the run directory, compute SHA256 over exact bytes, and return `ledger.EvidenceRef` with URI/path, SHA256 and kind.
- [x] Reject unsafe artifact names/path traversal and prevent escape from the run evidence directory.
- [x] Provide helpers for byte artifacts and JSON metadata without mutating previously written artifacts.
- [x] Add tests proving exact hashing, path containment, immutability/no overwrite, and independent run directories.
- [x] Run the validation baseline, mark Task 2 complete, and commit.

### Task 3: Implement supervised subprocess execution

- [x] Add `internal/supervisor` with structured command input: argv, cwd, environment, timeout/context and evidence sinks.
- [x] Launch using `exec.CommandContext`; do not invoke `/bin/sh -c` or equivalent.
- [x] Capture PID, Linux process-group identity, start/end timestamps, exit code, terminating signal when applicable, exact argv/cwd, stdout and stderr artifact refs.
- [x] Put the child into its own Linux process group so cancellation/timeout can target the governed group rather than only the direct process.
- [x] Preserve clear result semantics for exit 0, non-zero exit, context cancellation and signal termination.
- [x] Add deterministic tests using temporary harmless helper scripts/processes for success, non-zero exit, stdout/stderr capture and cancellation.
- [x] Run the validation baseline, mark Task 3 complete, and commit.

### Task 4: Implement deterministic branch acceptance executor

- [x] Add `internal/acceptance` that executes configured acceptance commands independently of the Ralphex subprocess result.
- [x] For every command capture exact argv, cwd, timestamps, exit/signal semantics, stdout/stderr evidence refs, and policy/class metadata.
- [x] Stop and fail acceptance on the first required command failure; never claim branch acceptance from Ralphex narration or dashboard state.
- [x] Record final Git `HEAD` SHA and clean/dirty status through structured Git argv execution, not shell parsing.
- [x] Define an acceptance result that can only be PASS when all required commands pass and final Git evidence is captured.
- [x] Add tests showing all-pass, first-failure stop, evidence capture, and no transition to `BRANCH_ACCEPTED` on failure.
- [x] Run the validation baseline, mark Task 4 complete, and commit.

### Task 5: Implement governed single-plan runner and CLI entry point

- [x] Add `internal/run` (or equivalently focused orchestration package) that accepts only validated authority plus ledger/evidence/supervisor dependencies.
- [x] Persist state/event sequence under controller authority: `RUN_CREATED → AUTHORITY_VALIDATED → EXECUTION_STARTING → IMPLEMENTING` before the Ralphex process and, only on exit 0, `IMPLEMENTATION_COMPLETED → BRANCH_ACCEPTANCE_PENDING`.
- [x] Invoke Ralphex through the existing `internal/ralphex.Invocation` structured argv contract; extend that contract only where EP-002 authority requires it.
- [x] On Ralphex non-zero/cancelled outcome, record failure evidence and do not emit `IMPLEMENTATION_COMPLETED`.
- [x] Run controller acceptance after Ralphex success; emit `BRANCH_ACCEPTED` only when acceptance PASS evidence exists.
- [x] Ensure EP-002 cannot transition to `INTEGRATION_PENDING`, `READY_FOR_MERGE`, `MERGED` or `COMPLETED` from this runner.
- [x] Add CLI command `abcp run --manifest <path> --ledger <path> --evidence-root <path>` (or an equally explicit structured interface) that loads the manifest, validates authority and runs the governed lifecycle without hidden defaults for repository/plan/Ralphex identity.
- [x] Add end-to-end deterministic tests with a disposable Git repository and fake Ralphex executable proving success to `BRANCH_ACCEPTED`, Ralphex failure, acceptance failure, and preserved evidence/event ordering.
- [x] Run the validation baseline, mark Task 5 complete, and commit.

## Final verification

After all tasks are complete:

```bash
gofmt -w cmd internal
go test ./...
make smoke
git diff --check
git status --short
```

Required outcome:
- all tests pass;
- worktree clean after commits;
- plan checkboxes all complete;
- implementation reaches at most `BRANCH_ACCEPTED`;
- no integration/merge/production-completion capability is added in EP-002.

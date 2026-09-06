# Plan: EP-002 PR #4 Corrections

## Authority

PR: `#4`
Exact reviewed head: `242963a77dda8d3d46e222d04163f1d61ef57da9`
Base: `4c89938dd03afd74cfffea4c9ebd8172acbcac5d`

This is a bounded correction pass. Do not add new EP-002 capabilities.

## Required invariants

1. Preserve the isolated controller-owned Ralphex `--config-dir` boundary.
2. Preserve rejection of real repository-local Ralphex configuration overrides.
3. Normal canonical Ralphex runtime artifacts under `.ralphex/` must not block a governed run.
4. Every controller state transition must be validated by the canonical domain state machine before ledger append.
5. Do not change integration, merge, recovery, scheduling, deployment, or production-completion scope.

### Task 1: Correct PR #4 architecture findings

- [ ] Replace the blanket rejection of any repository `.ralphex/` directory with a precise audited-local-override check.
- [ ] Reject an unsafe/symlinked `.ralphex` local configuration boundary and reject actual local override surfaces used by audited Ralphex: `.ralphex/config`, `.ralphex/prompts`, and `.ralphex/agents`.
- [ ] Allow benign canonical runtime state such as `.ralphex/.gitignore`, `.ralphex/progress/`, and `.ralphex/worktrees/` when no local override surface is present.
- [ ] Add regression tests proving benign runtime state is accepted and true local configuration remains rejected.- [ ] Make the governed runner transition helper call `domain.ValidateTransition(from, to)` before appending the event, in addition to the EP-002 destination-state cap.
- [ ] Add a focused regression proving an invalid transition whose destination is still inside the EP-002 state subset is rejected and not appended.
- [ ] Keep all existing success/failure/cancellation behavior and evidence contracts intact.
- [ ] Run the full validation baseline and commit only these corrections plus this plan update.

## Validation baseline

```bash
gofmt -w cmd internal
go test ./...
go test -race ./...
go vet ./...
make smoke
git diff --check
git status --short
```

Required outcome:

- all validation passes;
- worktree clean after commit;
- no new product scope;
- current repository-style benign `.ralphex` runtime state is compatible with governed execution;
- actual Ralphex local overrides remain blocked;
- invalid domain transitions cannot be appended by the EP-002 runner.
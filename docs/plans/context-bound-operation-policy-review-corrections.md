# Context-Bound Operation Policy — Review Corrections

Purpose: correct only CBO-001 and CBO-002 from the exact-head implementation review of `0f0c3c05d8abc3a186dd7428a6ca90bf7828560c`.

Non-goals: no scheduler/handoff automation, no EP-005 CI changes, no new capsule architecture, no publication/merge behavior changes.

### Task 1: Correct reviewed operation-authority blockers

- [ ] Before edits, verify the authority-bound v2 implementation capsule and exact repository base.
- [ ] Fix CBO-001 by enforcing a closed mapping between operation kind and Ralphex execution mode before launch: implementation-capable modes require `implementation`; review mode requires a supported review operation kind. Apply the one-incomplete-section restriction to every implementation-capable launch.
- [ ] Add regression tests proving review capsules cannot launch implementation-capable modes and implementation capsules cannot launch review mode.
- [ ] Fix CBO-002 by parsing Markdown fences from the original line with valid Markdown indentation/open/close rules, failing closed on ambiguous or unterminated fence syntax where needed so executable sections cannot be hidden.
- [ ] Add regression coverage for a four-space-indented backtick line followed by two incomplete executable task sections, plus representative valid fenced-code cases.
- [ ] Preserve v1 historical compatibility, v2 exact authority binding, xhigh enforcement, ambient-variable isolation, and all prior policy behavior.
- [ ] Run `gofmt`, focused tests, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`.
- [ ] Commit only these two root-cause corrections and their tests/documentation if needed.

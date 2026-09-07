# EP-004 Track C — Final Claude Major Correction

## Context Authority

- Roadmap: Phase 3 — Cross-plan scheduler and integration.
- Execution pack: `docs/execution-packs/EP-004-cross-plan-scheduler-integration.md`.
- Current Track C reviewed head: `4170bc0a7fffc5c0a9d33ebe8a9cdd54b204a286`.
- Frozen shared contract: `internal/scheduler/` remains read-only.
- Owned area: `internal/combinedacceptance/` and tests/docs required for this correction only.
- Fresh task must verify the bound context capsule before edits.
- This correction cannot create integration workspaces, transition lifecycle state, or grant `READY_FOR_MERGE`.

### Task 1: Bound and validate caller-supplied integration evidence reads

- [ ] Verify the governed context capsule and exact base/source hashes before editing.
- [ ] Correct the independent-review Major: caller-controlled `Integration.Evidence` URIs must never cause unbounded, blocking, or special-device reads.
- [ ] Require evidence paths used as local artifacts to be absolute, clean, existing regular non-symlink files; reject ambiguous/special paths deterministically.
- [ ] Enforce an explicit maximum artifact size and maximum integration-evidence count before reads.
- [ ] Use a bounded read path so a changed/oversized artifact cannot exceed the declared controller limit; digest verification remains mandatory.
- [ ] Preserve the prior reserved-kind collision checks and clean-path acceptance-evidence re-verification.
- [ ] Add regressions for FIFO/device/non-regular paths where safely testable, symlink, relative/non-canonical path, oversized artifact, excessive evidence count, and valid bounded evidence.
- [ ] Keep Track A scheduler contracts and all Track B packages untouched.
- [ ] Run `gofmt -w cmd internal`, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`.
- [ ] Mark only this task complete and commit the bounded correction.

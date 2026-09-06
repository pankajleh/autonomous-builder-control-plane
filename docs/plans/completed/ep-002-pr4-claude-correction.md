# Plan: EP-002 PR #4 Claude Review Correction

## Authority

PR: #4 — `EP-002 implementation: governed single-plan execution`
Base reviewed head: `964b9c0d73e84ad61189e4e5b11e5981084cc292`

This is a bounded correction plan for one independently verified Claude Code major finding. Do not expand EP-002 scope and do not invent new tasks.

### Task 1: Synchronize bounded subprocess output capture

- [x] Fix the `internal/supervisor` bounded stdout/stderr capture so `Write`, truncation reads, and evidence byte reads are race-free when `exec.Cmd.WaitDelay` returns before copy goroutines have fully quiesced.
- [x] Use the smallest synchronization mechanism appropriate for `boundedBuffer`; `Bytes()` must return a stable copy safe for hashing/publishing.
- [x] Preserve the existing non-blocking output-limit signal behavior and all existing outcome semantics.
- [x] Add a deterministic regression test that exercises the WaitDelay/retained-pipe path with a descendant writing concurrently or otherwise proves the prior race is closed.
- [x] Preserve process-group cleanup, output-limit, cancellation, timeout, signal, and evidence behavior.
- [x] Run `gofmt -w cmd internal`, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`.
- [x] Mark this task complete and commit only this bounded correction.

## Non-goals

No authority, acceptance, runner, Git, CLI, state-machine, documentation architecture, integration, merge, CI/CD, or production behavior changes unless strictly required by this defect.

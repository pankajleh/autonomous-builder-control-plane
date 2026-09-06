# EP-004 Track C — Claude Major Correction

## Context Authority

- Roadmap: Phase 3 — Cross-plan scheduler and integration.
- Execution pack: `docs/execution-packs/EP-004-cross-plan-scheduler-integration.md`.
- Track C reviewed head: `2b4dd5c084767330362f4e2a59de574b9f70a711`.
- Frozen Track A scheduler contract remains read-only; Track B remains out of scope.
- Fresh task must verify the bound context capsule before implementation.
- Global invariants: controller evidence must be byte/digest verified before trusted reuse; caller evidence cannot impersonate controller-produced evidence; ambiguity fails closed; no state transition or `READY_FOR_MERGE` authority here.

### Task 1: Correct Track C evidence trust boundary

- [x] Verify every caller-supplied `Integration.Evidence` reference by reading the exact bytes and matching SHA256 before evaluation can continue; reject unreadable, missing, mutated, or digest-mismatched evidence.
- [x] Prevent caller-supplied integration evidence kinds from colliding with controller-produced combined-target or acceptance evidence namespaces, or keep caller integration refs structurally separated so provenance tiers cannot be confused.
- [x] On `CLEAN_COMBINED_ACCEPTANCE`, verify the acceptance command/Git evidence bytes and digests rather than trusting structurally complete references only.
- [x] Preserve existing fail-closed semantic-conflict/validation-unavailable classification, exact target checkpoints, frozen scheduler contract, and Track D responsibility to bind the exact Track B result.
- [x] Add regressions for forged/mutated integration evidence, kind collision, and clean-path acceptance evidence verification.
- [x] Run `gofmt -w cmd internal`, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check` with the pinned Go toolchain available on PATH.
- [x] Mark only this task complete and commit the bounded correction.

## Review finding being corrected

Claude Major: Track C structurally accepts caller-supplied integration evidence references without reading/hash-verifying them, then merges those refs into the controller evidence set; clean combined acceptance also does not independently byte-verify all acceptance artifacts before emitting a clean result.

## Track boundary

This correction hardens Track C's evidence trust boundary only. Track D still must bind the exact Track B result and remains out of scope.

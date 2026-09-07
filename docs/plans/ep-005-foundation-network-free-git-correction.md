# EP-005 Foundation Network-Free Git Correction

## Overview
Correct the remaining controller-review Major on exact accepted head `097449bf30602d4f291bb2a528d8d4a5b93af2a8`. Scope remains the Phase 4 network-free GitHub lifecycle foundation only.

## Review authority
- ABCP accepted exact head `097449bf30602d4f291bb2a528d8d4a5b93af2a8`.
- Claude exact-head review returned `CLEAN_CRITICAL_MAJOR`.
- Controller exact-head review found one substantive Major: controller-owned Git derivation can lazy-fetch missing promisor objects because the governed Git environment does not set `GIT_NO_LAZY_FETCH=1`.
- Controller review SHA256: `a70768b4e1f90e8db9b50c21977f133a699e7bf4cac0acf3528e8d2cf09a168d`.
- Reproduction SHA256: `18b0ee67599654d3a34034b79d15ac29bd152a9a5b876fd71ce829df90856dcf`.

## Required invariant
Every controller-owned Git subprocess used by the network-free foundation must be unable to obtain missing objects from any promisor remote. Required objects must already exist locally; otherwise the operation fails closed.

The canonical governed Git environment must set `GIT_NO_LAZY_FETCH=1` in addition to the existing replacement-ref, graft, hook, fsmonitor, credential, and host-config protections. `runPinnedGit` must continue using that environment unchanged.
## Threat/failure matrix
- missing promisor object with remote available -> no fetch; fail closed;
- missing promisor object with remote unavailable -> same fail-closed result;
- replacement refs remain ignored;
- ambient Git config/credentials remain excluded;
- local complete repository behavior remains unchanged;
- no provider/network implementation, PR write, CI poll, merge write, state transition, or Phase 5+ work.

### Task 1: Disable Git lazy-fetch in the network-free foundation
- [ ] Add `GIT_NO_LAZY_FETCH=1` to `internal/gitexec.Environment()` so every governed Git subprocess inherits the prohibition.
- [ ] Preserve all existing Git environment protections and `runPinnedGit` behavior.
- [ ] Add a regression using a promisor/partial-clone setup with a deliberately missing object. Prove the governed derivation cannot fetch it, returns an error, and does not increase local object/pack materialization.
- [ ] Preserve and rerun the existing replacement-ref expected-content regression.
- [ ] Update `GITHUB_LIFECYCLE_FOUNDATION_CONTRACT.md` to state that lazy promisor fetching is disabled and required objects must already be local.
- [ ] Run `gofmt`, focused tests, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`.

## Non-goals
No change to write-attempt identity, expected-tree authority semantics, resource-limit policy, GitHub provider implementation, PR/CI execution, merge execution, ledger state transitions, Phase 5 API/dashboard, deployment, or production acceptance.
# Task 2 exact nine-finding correction evidence

Candidate corrected: `d0673f6165acb0b27c52518417e99153ff8c33b1`

Every command below completed with exit status 0 on the final implementation before the correction commit. Each matrix entrypoint contains its row-specific positive, negative, crash, restart, concurrency, and provider-call assertions; none is skipped or assertion-free.

## Matrix: 9/9 PASS

| ID | Deterministic entrypoint | Exact command | Result |
|---|---|---|---|
| T2-CORR-C01 | `TestCorrectionC01TerminalCoreRecovery` | `GOFLAGS=-buildvcs=false go test ./internal/mergelifecycle -run '^TestCorrectionC01TerminalCoreRecovery$' -count=1` | PASS (`ok`, 1.016s): core-only MERGED/FAILED/CANCELLED and event-before-final recovery select the exact durable terminal with zero provider calls. |
| T2-CORR-M01 | `TestCorrectionM01AuthorityEvidenceBytes` | `GOFLAGS=-buildvcs=false go test ./internal/mergelifecycle -run '^TestCorrectionM01AuthorityEvidenceBytes$' -count=1` | PASS (`ok`, 0.113s): missing/mismatched/unsafe/over-limit/wrong/incomplete evidence fails closed; exact bytes pass. |
| T2-CORR-M02 | `TestCorrectionM02ExactCommitPreparationProof` | `GOFLAGS=-buildvcs=false go test ./internal/mergelifecycle -run '^TestCorrectionM02ExactCommitPreparationProof$' -count=1` | PASS (`ok`, 0.332s): every exact-object field is independently checked and ambiguous recovery does not repeat object creation. |
| T2-CORR-M03 | `TestCorrectionM03CanonicalTerminalReasons` | `GOFLAGS=-buildvcs=false go test ./internal/mergelifecycle -run '^TestCorrectionM03CanonicalTerminalReasons$' -count=1` | PASS (`ok`, 0.319s): all Section-8 negative reasons route through exactly one legal terminal transition. |
| T2-CORR-M04 | `TestCorrectionM04CancellationRecoveryBinding` | `GOFLAGS=-buildvcs=false go test ./internal/mergelifecycle -run '^TestCorrectionM04CancellationRecoveryBinding$' -count=1` | PASS (`ok`, 1.939s): indexed cancellation is recovered and independently bound; APPLIED wins and exact NOT_APPLIED may cancel. |
| T2-CORR-M05 | `TestCorrectionM05CumulativeProviderBudgets` | `GOFLAGS=-buildvcs=false go test ./internal/mergelifecycle -run '^TestCorrectionM05CumulativeProviderBudgets$' -count=1` | PASS (`ok`, 0.107s): exact-limit and limit+1 call/byte/time/invocation cases, restart, and reconciliation interval are enforced. |
| T2-CORR-M06 | `TestCorrectionM06StorageReservationsCleanupLimits` | `GOFLAGS=-buildvcs=false go test ./internal/mergelifecycle -run '^TestCorrectionM06StorageReservationsCleanupLimits$' -count=1` | PASS (`ok`, 0.314s): serialized reservations prevent oversubscription and cleanup recovery exhausts monotonically at the required boundary. |
| T2-CORR-M07 | `TestCorrectionM07DescriptorRelativeDurability` | `GOFLAGS=-buildvcs=false go test ./internal/ledger -run '^TestCorrectionM07DescriptorRelativeDurability$' -count=1` | PASS (`ok`, 0.055s): path/inode replacement and unsafe entry types fail closed while exact create-or-verify stays idempotent. |
| T2-CORR-M08 | `TestCorrectionM08RepositoryBaseLock` | `GOFLAGS=-buildvcs=false go test ./internal/mergelifecycle -run '^TestCorrectionM08RepositoryBaseLock$' -count=1` | PASS (`ok`, 0.121s): same repository/base serializes, distinct bases do not alias, and lock order does not invert. |

## Repository gates: PASS

- Task-2 focused: `GOFLAGS=-buildvcs=false go test ./internal/mergelifecycle ./internal/ledger -count=1` — PASS.
- Repeated crash/concurrency: `GOFLAGS=-buildvcs=false go test -count=10 ./internal/mergelifecycle ./internal/ledger -run 'TestCorrection(C01TerminalCoreRecovery|M04CancellationRecoveryBinding|M06StorageReservationsCleanupLimits|M07DescriptorRelativeDurability|M08RepositoryBaseLock)'` — PASS; M08 also passed independently with `-count=25`.
- Focused race: `GOFLAGS=-buildvcs=false go test -race ./internal/mergelifecycle ./internal/ledger -count=1` — PASS.
- Full race: `go test -race ./... -count=1` — PASS.
- Frozen Task-1 regression: `GOFLAGS=-buildvcs=false go test ./internal/githublifecycle -count=1` — PASS.
- Full repository: `GOFLAGS=-buildvcs=false go test ./... -count=1` — PASS.
- Vet: `GOFLAGS=-buildvcs=false go vet ./...` — PASS.
- Smoke: `GOFLAGS=-buildvcs=false make smoke` — PASS.
- Non-Linux compile-only: `GOFLAGS=-buildvcs=false GOOS=darwin GOARCH=amd64 go test -exec=/bin/true ./internal/ledger ./internal/mergelifecycle` — PASS.
- Frozen contracts: `git diff --exit-code -- internal/githublifecycle internal/run internal/integrationgate go.mod go.sum` — PASS.
- Network-free source: `if rg -n '"net/http"|http\\.(Client|NewRequest|NewRequestWithContext)|api\\.github\\.com|/graphql' internal/mergelifecycle internal/ledger; then exit 1; fi` — PASS.
- Network-free dependency: `if go list -deps ./internal/mergelifecycle | rg -q '^net/http$'; then exit 1; fi` — PASS.
- Mutation-ceiling path audit — PASS; only paths authorized by the correction plan are changed.
- Diff integrity: `git diff --check` — PASS.

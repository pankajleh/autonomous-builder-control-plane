# EP-004 Foundation Contract and Ownership

## Frozen shared contract

The accepted-candidate and final-diff risk contract is owned by the
`internal/scheduler/` package. Later EP-004 tracks consume these validated
values and must treat the entire package as read-only. Candidate identity is
defined in `contract.go`; queue and risk behavior and evidence types are
defined in `queue.go` and `risk.go`:

- `CandidateInput` is the controller input containing exact project, plan, run,
  attempt, repository, branch, start SHA, head SHA, acceptance evidence,
  acceptance timestamp, and acceptance policy identity.
- `AcceptedCandidate` is the validated immutable value. It can only be created
  by `NewAcceptedCandidate`; accessors return defensive copies.
- `RiskPolicy`, `RiskClass`, `CandidateDiffEvidence`, `PairRiskEvidence`, and
  `RiskReport` are the minimal evidence types returned by final-diff analysis.
- A risk report describes selection evidence only. It does not transition
  control-plane state and cannot grant `READY_FOR_MERGE`.

Changes to the frozen shared package require serial gate-assembly ownership or
a separately authorized contract revision. Parallel tracks must not edit it.

## Parallel file ownership

| Track | Owned implementation area | Read-only dependency |
|---|---|---|
| Foundation | `internal/scheduler/` package and tests | none |
| Integration workspace / textual conflicts | new `internal/integrationworkspace/` package | read-only `internal/scheduler/` package |
| Combined acceptance / semantic conflicts | new `internal/combinedacceptance/` package | read-only `internal/scheduler/` package |
| Serial gate assembly | state-transition wiring and any explicitly authorized contract revision | all accepted track packages |

The parallel tracks must not edit each other's package, the canonical state
machine, or the branch-acceptance implementation. Both start from the exact
accepted foundation SHA and return evidence to later serial gate assembly.

## Foundation behavior

`Queue` is append-only and returns candidates in a stable order derived from
their immutable acceptance timestamp and exact identity. It rejects duplicate
run attempts, accepted-head replay, and acceptance-evidence replay.

`Analyzer` verifies each start/head object as an exact commit, proves ancestry,
and reads `git diff --name-status -z` between those committed SHAs. It never
reads worktree status. Rename/copy output, malformed paths, case-fold ambiguity,
missing objects, and invalid ancestry fail closed. Pair evidence distinguishes
disjoint paths, ordinary overlap, and candidates that both touch the same
contract-sensitive or shared-authority root.

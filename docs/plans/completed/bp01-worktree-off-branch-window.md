# BP-01: a run's worktree briefly off its branch

Date: 2026-09-30

Exact base: `11f4636` (PR #61)

Approved by the owner on 2026-09-30, with the 10-minute window. Repo C's proposal, with the evidence:
`docs/product/design-notes/ABCP_WORKTREE_OFF_BRANCH.md` (Repo C P0142, answer recorded in P0166).

## Observation

Team Kudos (run `admission-9effa5fd…`, 2026-09-29): the builder's own review asked for the work in more commits, and
the fixing agent rebuilt the branch as four commits. Its worktree was on a detached `HEAD` for about 4.5 minutes.

`resolveWorktree` finds no worktree holding the run's branch both when the worktree is gone and when it is there on a
detached `HEAD` or another branch. After the 30-second transition window, every read appended a `binding-unavailable`
marker (142 in all), and one marker makes every checkpoint of the run ineligible for preview. The build finished with
every step passing, and none of its versions could be previewed.

## Change

- **The service remembers the worktree path it last bound for each run** (`worktreeBound`), in memory.
- **Off its branch** means: that path is still listed by `git worktree list`, no worktree holds the run's branch, and
  the branch ref still exists (`worktreeOffBranch`, `binding.go`).
- **While off its branch, for at most 10 minutes** from the first observation (`worktreeOffBranchWindow`):
  - no marker;
  - nothing is read or observed, so no checkpoint can come from off the branch;
  - one note line: `abcp activity note … what=worktree-off-branch`.
- **Back on its branch:** before anything is observed, every checkpoint already recorded for the run must be an
  ancestor of the branch head (`git merge-base --is-ancestor`, `historyKept`).
  - Kept: the run carries on, with one note line (`what=worktree-back-on-branch`).
  - Not kept: one integrity marker, `history-rewritten` ("Implementation history rewritten"), recorded like any
    other controller marker, so the preview resolver refuses the run's checkpoints exactly as before.
  - An interrupted check decides nothing and observes nothing; the next read tries again.
- **Past 10 minutes** off the branch, every read records today's `binding-unavailable` marker, without the 30-second
  window on top.
- **Anything else is unchanged:** a worktree that is gone, a deleted branch ref, a run the service has not bound since
  it started, eviction and release (which still treat only "no worktree holds the branch" as absent), retention,
  eligibility and the 30-second transition window.

After a controller restart the remembered paths are empty, so an off-branch worktree then gets today's rule until the
worktree is seen on its branch again. Restarts are made only with no build running.

## Acceptance

```text
go test ./internal/activity ./internal/preview ./internal/eviction ./cmd/abcp ./internal/serviceapi
go test -race ./internal/activity
go vet ./...
```

New tests in `internal/activity/offbranch_linux_test.go`:

- a detached `HEAD` for 2.5 minutes, with a commit made there, then back: no marker, no checkpoint from off the
  branch, the next checkpoint observed, and exactly one note line each way;
- the branch rebuilt from its base while detached, then back: one `history-rewritten` marker, and no second one
  on later reads;
- 11 minutes off: a marker (none just inside 10 minutes);
- the branch ref deleted while detached: the 30-second rule, then a marker;
- the worktree removed after binding: the 30-second rule, then a marker;
- the worktree on another branch for 5 minutes, then back: no marker.

The first, second, third and last tests fail without the change.

`go test ./...` also fails in `internal/mergelifecycle` (`TestTask3FinalClosureM02…`, `TestTask3FinalClosureM03…`:
"leased append cannot bypass an unresolved transition barrier"). It fails the same way on `11f4636` without this
change, and that package does not import `internal/activity`. `internal/run/resume.go` is not `gofmt`-clean on
`11f4636` either. Neither is touched here.

# Parallel builds: hold the repository only while a build is set up

Date: 2026-10-02

Exact base: `71b1995` (PR #72)

Owner, 2026-10-02, on a build "waiting for its turn": "how is this organised when multiple people request. this is gonna
happen very quickly. people can not and should not wait. they will just go away."

## Why builds wait today

Every product build runs in one Git repository (`runtime/product-repo` on the live host). `run.Runner.Run` takes the
repository execution lease (`abcp-ralphex-execution.lock`, an exclusive `flock` in the Git common directory) before it
validates the authority and holds it until Ralphex exits. A build takes 15–45 minutes, so the host builds one app at a
time and everyone else waits in arrival order. On 2026-10-02 a build waited 24 minutes behind one change.

The lease exists for three things, all of them at the start of a build:

1. **Handoff recovery.** At acquisition the controller removes every execution-plan handoff left by an interrupted run
   (`recoverExecutionPlanHandoffs`). If two runs held the lease at once, one would remove the other's live handoff.
2. **The untracked handoff.** A product plan is Git-ignored, so the controller writes an untracked copy
   (`abcp-ralphex-plan-<run digest>/plan.md`) that Ralphex copies into the new worktree and commits on the run's branch.
3. **One clean start.** Validation reads `HEAD`, the branch and the plan from the shared checkout, and refuses a checkout
   with any untracked file, which another run's handoff is while that run executes.

Nothing after Ralphex has created the run's worktree needs the shared checkout to itself:

- Ralphex's worktree mode was built for "multiple ralphex instances on the same repo simultaneously" (upstream plan
  `20260224-worktree-isolation`). Other runs' untracked files are logged, not refused (`CreateWorktreeForPlan`).
- Each run works in `.ralphex/worktrees/<branch>` on its own branch `abcp/<run id>`; its progress file is named after
  that branch; `HEAD` of the shared checkout never moves (every admission starts from the same `HEAD`).
- The behaviour lab ran two plans at once from one repository (EXP-07, 2026-09-06):
  `EXPECTED_PARALLEL_WORKTREE_ISOLATION_OBSERVED`, both branches isolated, the shared checkout unchanged and clean.

## What changes

- **Setup lease.** The existing lease is kept, with the same file, but a worktree run holds it only while it is being set
  up: recovery, validation, writing its handoff, starting Ralphex, and until its branch carries a commit past the start
  (Ralphex has created the worktree and committed the plan copy there). It is then released, or when Ralphex exits,
  whichever comes first; after three minutes without that commit it is released anyway.
- **Run slots.** How many runs may execute at once is a count of slot files, `abcp-ralphex-slots/slot-<n>.lock` (0600, in
  a 0700 directory in the Git common directory). A worktree run takes one free slot before the setup lease and keeps it
  until Ralphex exits. With one slot (the default) runs are serialised exactly as today.
- **Exclusive runs.** A run without a worktree (read-only review, or a profile that disables worktrees) works in the
  shared checkout itself and needs it clean, so it takes every slot and holds the setup lease until Ralphex exits.
- **Live handoffs.** A run holds an exclusive `flock` on `<run digest>.lock` beside its handoff ownership record from the
  moment it writes the record until the handoff is removed. Recovery removes only handoffs whose lock it can take (the
  owner process is gone) and removes that lock file with them; a live run's handoff is left alone. Records and lock files
  are created, recovered and removed only under the setup lease.
- **Removing the handoff** after Ralphex exits takes the setup lease again for that moment.
- **A clean start beside running runs.** Validation's clean-checkout check leaves out exactly the handoffs of runs that are
  still executing (a valid ownership record whose lock is held); any other change, an abandoned handoff included, still
  makes the checkout unclean.
- **Configuration.** `abcp serve --parallel-runs N` (1–8, default 1) passes `--parallel-runs N` to every `abcp run` it
  launches or resumes (only when N > 1, so default launches are unchanged). `abcp run --parallel-runs` takes the same
  range. Every run of a repository must use the same N; the live host sets 3.

## Not changed

- Admission, receipts, bindings, manifests and their digests; the ledger's states and events.
- Post-run steps (acceptance, snapshots, finalization) already run after the lease is released.
- One build per app at a time is Repo C's rule and stays there.

## Capacity

The live host has 8 cores and 31 GB, about half of the memory free with one build running. Three runs at once is the
starting point; more needs more builder hosts (Repo C roadmap PX-12b), an owner decision.

## Tests

- Slots: with 2 slots, two runs take one each and a third waits until one is released; an exclusive run waits for every
  slot and blocks new runs while it holds them; cancellation while waiting returns the context's cause; slot files are
  protected.
- Recovery: a handoff whose owner holds its lock survives another run's recovery; a handoff whose owner is gone is
  removed with its lock file; unknown names still fail closed.
- Early release: a worktree run releases the setup lease once its branch has moved past the start commit, so a second run
  can set up while the first is still executing; an exclusive run keeps it to the end.
- Default (one slot): two overlapping runs are serialised as before (the existing serialisation test stays).
- Through the real Ralphex: the existing pinned-runtime contract test (`ABCP_TEST_PINNED_RALPHEX`) still passes, and the
  rollout below starts two real builds at once on the live host: both execute together, their branches stay isolated and
  the shared checkout is clean afterwards (the behaviour lab's EXP-07 showed the same for Ralphex alone).

## Rollout

Merge, build the controller, restart `abcp serve` with `--parallel-runs 3` at a moment no build is executing (announced
on Repo C #93), then start two builds and watch both execute at once.

# Build recovery: say why a build failed, and give it more chances before it does

Date: 2026-11-07

Exact base: `add6f4e` (PR #191)

Status: **R1 and R2 built in PR #193 (`1d69c32`, live since 2026-11-07 15:24 UTC); R3 built in PR #194 (`c0aeb06`,
live since 2026-11-07 16:38 UTC); R5 and R6's 503 built in PRs #195 and #196 (`3010d56`, live since 2026-11-07
17:46 UTC); R7 built in PR #197 (`1305e35`), to go live after Repo C reads a queued run; R6's resume built in PR #199
(§6.2); R2b and R4 designed below for the next PRs.** Repo C
design note
`BUILD_RELIABILITY.md` §5.6 is the proposal (Repo C P1122, PR #1137). The owner, 2026-11-07, on "Recovery in ABCP":
"Yes, build it (Recommended)": each Repo B merge and each quiet ABCP restart for R1 to R7 is covered, at about US$30 a
month of extra model spend on builds that fail.

## 1. Why

Repo C measured every admitted build of the 30 days from 2026-10-08 (`BUILD_RELIABILITY.md` §3, read-only):

- 417 admitted builds, 374 ended with a working version (89.7%). People's apps: 82 admitted, 71 working (86.6%); as Repo C
  counts its 99.9% target, 68 of 80 requests (85.0%) ended working without anyone pressing again; 2–7 November, 79.3%.
- Of the 43 that did not end working, the people's 11 were: our own check contradicting our kit or crashing (4), the
  builder's own new tests failing with no reason in the check's report (2, Thali Calorie Lens), a request past a platform
  limit (1), credits (1) and Stop build (3). The rest were the operator's template factory (15) and open-model trials (17).
- **Every one reached the person as one generic line.** ABCP gives Repo C only the classifier's `failure_class`
  (`TASK_NOT_COMPLETED` for most) and the timeline's categories: not which check failed, with which reason, or what was
  already tried.

The case (§3.5, run `admission-455e7963…`, 2026-11-07 10:07–10:30 UTC, Codex): `node-app-check` failed on two new tests;
one repair turn fixed them; it then failed on `tests/browser.test.js` with only "not ok 3 - tests/browser.test.js"; the
builder ran the file itself (28 of 28 passed), found no reason and signalled FAILED; Ralphex's one retry did the same; the
run ended `TASK_NOT_COMPLETED` and the page said "Your change didn't finish."

## 2. What the pinned Ralphex already does

`ralphex-v1.7.0-abcp2` (`pankajleh/ralphex-governance` `abcp/v1.7.0` @ `c66debc`), with the product profiles'
`long_running_subprocess_mode: orchestrator` and validation commands:

- **Task phase** (`pkg/processor/phase/task.go`). Each iteration is a fresh builder session. After a turn that did not
  signal FAILED, Ralphex runs the governed validation itself; if it fails, the next turn's prompt is the task plus
  "DETERMINISTIC VALIDATION REPAIR OVERRIDE" and the check's evidence ("deterministic validation failed; returning
  evidence to model for one repair turn: validation <id> exit N: <output>"). This repeats with the new evidence after
  every turn that changed something, bounded only by `max_iterations` (12 for product builds, "max iterations (12)
  reached without completion").
- **What ends a task attempt** is the builder signalling FAILED: Ralphex retries `task_retry_count` times ("task
  failed, retrying...", its default 1), each in a fresh session with the same prompt (after a failed check, the last
  evidence), without running the check again; then "task execution failed after retry (FAILED signal received)".
- **Review phase** (`pkg/processor/phase/review.go`, `validateWithOneRepair`): after a review pass that changed `HEAD`,
  validation runs; a failure gets exactly one repair turn ("validation still failing after one repair turn: …").
- The run's error is Ralphex's last standard-error line, `error: runner: <phase>: <error>`.

So "one repair turn" is the review's rule; in the task phase what runs out is the builder's patience, once.

## 3. R1: a structured failure (built)

Every transition that ends a run without an accepted branch (`FAILED`, `CANCELLED`, `VALIDATION_UNAVAILABLE`) records
`failure_detail` in its payload (`internal/runfailure`), next to the existing `reason`, `failure_class` and
`retryability`. It holds fixed codes and counts only, because the ledger is kept when a run is forgotten:

| Field | Value |
|---|---|
| `record_schema_version` | 1 |
| `cause` | one of the list below |
| `phase` | `ADMISSION`, `SETUP`, `IMPLEMENTATION`, `REVIEW`, `ACCEPTANCE` |
| `check`, `check_state`, `check_exit_code` | the failing validation command's id (or acceptance command's name, `acceptance-<n>` when its name is not an identifier); `EXITED`, `TIMED_OUT`, `STALLED`; the exit code when `EXITED` |
| `repair_turns`, `task_retries` | Ralphex's "deterministic validation failed" turns and "task failed, retrying..." retries in the run |
| `task_retry_limit`, `attempts` | the profile's `task_retry_count` (else 1); builder attempts (2 once a fresh attempt ran, R3); `repair_turns` and `task_retries` are the last attempt's |
| `engine`, `provider` | the executor (`codex`, `claude`); the open-model provider's id |

Causes: `VALIDATION_FAILED` (a check still failed when the attempt ended, the builder having given up on it or its review
repair used), `MODEL_SIGNALLED_FAILED` (FAILED with no failing check pending), `MAX_ITERATIONS`, `REVIEW_FAILED`,
`ACCEPTANCE_FAILED`, `ACCEPTANCE_UNAVAILABLE`, `PROVIDER_REFUSED`, `PROVIDER_LIMIT`, `TIMEOUT`, `SANDBOX` (the build wall
did not start, or the build rewrote its branch), `INTERRUPTED` (a signal, not a request: a restart), `CANCELLED` (a cancel
request: Stop build, or Repo C's stop for credits), `ADMISSION_FAILED`, `EXACT_EDIT_DOES_NOT_APPLY`, `CONTROLLER_ERROR`,
`UNCLASSIFIED`. Ralphex's endings are read from its own lines (§2), never the builder's; a provider class comes from the
existing classifier.

When there is a failing check's output or Ralphex's last error to keep, the stop's evidence gains `failure-detail.json`
(kind `run-failure-detail`, `RunFailureDetailV1`): the check's own output and the last error, redacted with the
classifier's redaction, control characters and terminal escapes dropped, at most 4 KB (the first three quarters and the
last quarter around a `…[truncated]…` line when longer). A forget removes it with the other evidence bodies.

**Read:** `GET /v1/runs/{run}/failure` (`RunFailureV1`), from one ledger snapshot and the verified body; it writes
nothing, needs an empty GET of a registered run, and answers `410 run_forgotten` for a forgotten run, as its evidence
does. `failure` is absent while the run goes on and once its branch is accepted:

```json
{
  "schema_version": "RunFailureV1",
  "run_id": "admission-…",
  "projection_revision": "…",
  "current_state": "FAILED",
  "failure": {
    "event": {"ordinal": 61, "event_id": "…", "timestamp": "2026-11-07T10:30:02Z"},
    "state": "FAILED", "state_from": "IMPLEMENTING",
    "recorded": true,
    "cause": "VALIDATION_FAILED", "phase": "IMPLEMENTATION",
    "failure_class": "TASK_NOT_COMPLETED", "retryability": "RETRYABLE_AFTER_CONDITION_CHANGE",
    "check": "node-app-check", "check_state": "EXITED", "check_exit_code": 1,
    "check_output": "NODE_APP_CHECK_FAILED: tests\nnot ok 3 - tests/browser.test.js",
    "repair_turns": 2, "task_retries": 1, "task_retry_limit": 1, "attempts": 1,
    "engine": "codex",
    "evidence": [{"evidence_id": "…", "kind": "ralphex-stdout"}, {"evidence_id": "…", "kind": "run-failure-detail"}]
  }
}
```

`reason`, `failure_class`, `retryability`, `check*`, `check_output_truncated`, `task_retry_limit`, `attempts`, `engine`
and `provider` are left out when empty; `repair_turns`, `task_retries` and `evidence` are always there. A stop recorded
before R1 (or a transition an API cancellation took over, whose payload the runner replaces) answers `recorded: false`
and a cause told from its fixed codes only (`BUILD_WALL_FAILED` → `SANDBOX`, an `operation_id` → `CANCELLED`, an older
Ralphex failure → `UNCLASSIFIED`, and so on). Each evidence item downloads through `GET /v1/runs/{run}/evidence/{id}`.

**Unchanged:** `RunProjectionV1` (Repo C parses it with unknown fields refused), the timeline, the evidence list and
`/v1/capabilities`. The route is new, so an ABCP without it answers `404 not_found`.

**How Repo C reads it (its slice 7):** once, when the run projection first says the run stopped (no polling of a
finished run, `NOTIFICATION_POLLING_BUDGET.md`); strict, with an unknown `cause` read as `UNCLASSIFIED` and a 404 read as
"no detail" (it then words the stop from `failure_class` as today). What happened from `cause` and `phase`; which check
and its criteria from `check` and the kit's own lines in `check_output` (Repo C's format, `BUILD_RELIABILITY.md` §5.1);
what was tried from `repair_turns`, `task_retries` and `attempts`.

## 4. R2: more chances on a failing check (built)

`ralphex.task_retry_count` (1 to 3, `authority.MaxTaskRetryCount`), named after the Ralphex setting as `max_iterations`
and `max_internal_review_passes` are. A set count is one line, `task_retry_count = N`, of Ralphex's isolated
configuration (`<config-dir>/config`, which Ralphex merges over its defaults key by key); the start evidence records it.
Omitted, nothing is written, Ralphex keeps its own 1 and every existing manifest keeps its digest. The versioned product
template (`deploy/local-integration/abcp-config/manifest-template.json`) sets 3.

With 3, a builder that gives up on a failing check is sent back three times, each a fresh session with the check's last
evidence, before the attempt fails; a turn that changes something still gets the check run again and its new evidence,
as before. Each retry is one more session on a failing build only (the owner's spend).

**Not in R2** (Ralphex's code, so a governance patch and a re-pin, R2b): the review phase's single repair turn; running
the check again (verbose when its output has no reason) before a FAILED retry, so the retry gets new evidence and a test
that fails only with the others is caught as flaky (Repo C slice 2 makes the node kit say so itself).

## 5. R3: a fresh second attempt (built, PR #194)

`ralphex.fresh_attempts: 1` (`authority.MaxFreshAttempts`; omitted, none, and every manifest keeps its digest). It needs a
worktree run that implements and the Ralphex source `ralphex.TaskPromptSourceSHA` (`c66debc`, the live pin). When the
first attempt's builder ends without finishing, R1's cause `VALIDATION_FAILED` or `MODEL_SIGNALLED_FAILED` (Ralphex's own
repair turns and retries used), the run starts Ralphex once more on its branch before it fails:

- **Ledger:** `IMPLEMENTING → RETRYING` with the first attempt's ending (`outcome`, `failure_class`, its `failure_detail`
  with `attempts: 1`, `recovery: FRESH_ATTEMPT`, `next_attempt: 2`), then `RETRYING → EXECUTION_STARTING` (its start
  evidence names `attempt: 2`, `fresh_attempt_after` and `retained_worktree_removed`) `→ IMPLEMENTING`, all edges the
  state machine already allows. A second failure ends `FAILED` with R1's `attempts: 2`; a fresh attempt that cannot start
  fails from `RETRYING`. The fresh attempt's Ralphex evidence is named `-fresh-2`. Any other ending (max iterations, the
  review, a provider, a timeout, a cancel) ends the run as before, and so does an accepted first attempt: one attempt.
- **The retained worktree** (Ralphex refuses to start while it is there) is removed first with Git's own `worktree remove
  --force` and `worktree prune`: inside the wall for a walled run (the run's repository is the build's to write), with
  Git's controller environment (no hooks, no fsmonitor). Ralphex then adds the worktree again from the existing branch,
  at the same path, so every commit of the first attempt is kept and the activity binding resolves as before.
- **One progress session:** Ralphex appends the new start to the same progress file (the failed attempt's `Failed:`
  footer is never archived, its `progressCompletedAt`), so the sidecar keeps one session and the collector's append-only
  proof holds. A start in place, without a worktree, is not used: its second progress file would be refused.
- **Activity:** from `RETRYING` until the attempt implements again the missing worktree is "not there yet", as before a
  first attempt, however long the attempt waits for its run slot (`executionStarted`); then the usual bounded window.
  Checkpoints of both attempts resolve for preview.
- **The plan:** a walled run's untracked plan copy has a folder of its own for the attempt,
  `abcp-ralphex-plan-<run digest>-fresh-2/` (the kits skip every `abcp-ralphex-plan-*` folder): the first attempt's copy
  is committed on the branch, and Ralphex, finding the same file again, would have nothing to commit and refuse to start.
  An unwalled run with an untracked plan copy gets no fresh attempt (only tests run unwalled).
- **The prompt:** Ralphex's own task prompt (`internal/ralphex/task_prompt.txt`, the pinned source's file byte for byte)
  with a "FRESH ATTEMPT" section after it, written to the isolated settings folder's `prompts/task.txt` for the second
  attempt only: how the first ended, in fixed words, the check and its exit, the repair turns and retries used, and at
  most 2 KiB of the check's own output, with Ralphex's variable and signal markers broken up.
- **The runner:** one loop over at most two attempts. Each takes its run slot, its open-model build slot and (unwalled)
  the repository lease again, a fresh settings folder, its engine home written again from the same base, the wall's
  check, the bring-back of the branch; a walled run needs no lease past its setup. An attempt that is not the first holds
  an unwalled run's lease until Ralphex exits.

Each fresh attempt is one more session on a failing build only (the owner's spend).

**R2b, the review's repair turns and a fresh check before a retry, is its own PR next:** both are Ralphex's code
(`validateWithOneRepair`, the task loop's FAILED retry), so a governance patch in `pankajleh/ralphex-governance`, a new
pin in every manifest template and a quiet restart.

## 6. R4 to R7 (next PRs)

- **R4, a second engine.** `PROVIDER_REFUSED` or `PROVIDER_LIMIT` (R1's cause, never the words) moves the remaining
  attempt to the profile's declared fallback (`executor.provider.fallback` exists for an open-model engine that cannot
  start; a fallback for a running build is R3's attempt on another invocation). Which engine customers fall back to is
  Repo C's A2 build rules.
- **R5, acceptance once more: built, PR #195** (§6.1).
- **R6, restarts:** admission's `503` built, PR #196; the resume built, PR #199 (§6.2).
- **R7, a queue at admission:** built, PR #197 (§6.3); it goes live only after Repo C reads a queued run.

### 6.1 R5: the final acceptance once more (built, PR #195)

The case (Repo C `BUILD_RELIABILITY.md` §3.3 row 2, `e51a8b70`, 2026-11-06): the run's own check passed, ABCP's final
acceptance then failed the same kit check on one test file (one that fails beside the others and passes alone), and the
same app built 12 minutes later.

`acceptance_reruns: 1` (top level of the manifest; needs `ralphex.validation`; omitted keeps every digest). When:

- the run's own governed validation passed on its last run: Ralphex's `validation completed successfully: <id>` for each
  of the manifest's `ralphex.validation` ids, none failing after (`runfailure.Transcript.ValidationPassed`, read from the
  attempt's own Ralphex output), and
- ABCP's final acceptance then fails a required command by its exit (`exited_nonzero`; not a timeout, a signal, a dirty
  checkout or an acceptance that could not run),

the acceptance runs once more, all its commands, on a fresh checkout of the same candidate (the same branch at the same
commit, else the second run is unavailable: `VALIDATION_UNAVAILABLE`). Its evidence is named beside the first run's
(`acceptance-command-001-stdout-again.log`, `acceptance-final-git-again.json`, the same kinds), so the last
`acceptance-command-stdout` is the second run's, which is what Repo C's `StoppedChecks` and `TriedApp` read.

- **Passing:** `BRANCH_ACCEPTED`, its payload `acceptance_runs: 2` and `acceptance_flaky: <check>` (R1's name for the
  command), and `acceptance-flaky.json` (kind `acceptance-flaky`, `AcceptanceFlakyV1`: the words `FLAKY: failed, then
  passed when run again`, the check, the first run's outcome and exit code, its output redacted and bounded as R1's). No
  `failure_detail`: the run did not fail.
- **Failing again:** `FAILED` as before (`ACCEPTANCE_FAILED`, R1's detail and body from the second run), with
  `acceptance_runs: 2`.

**Not built: a fresh attempt (R3) after the second failure.** A run that reached acceptance had a Ralphex that
succeeded, so its progress file ends with a `Completed:` footer; Ralphex archives and truncates such a file when it starts
again (`archiveAndTruncateCompleted`), and the activity collector refuses a truncated progress file (append-only proof),
so the recovered build could not be previewed (the problem R3 avoids after a `Failed:` footer). It needs either the
governance fork to append under ABCP after a `Completed:` footer too (with R2b's patch and re-pin), or the collector to
accept a truncation whose archive holds the proven prefix byte for byte. Until then a second failure ends the run.

**Spend:** one more acceptance run (minutes, no model) on a failing build only.

### 6.2 R6: never 202 and nothing (built, PR #196); the resume (built)

**The case** (Repo C `BUILD_RELIABILITY.md` §3.3 row 11, `admission-58cbcfac…`, 2026-10-17 02:27 UTC): admission launched
`abcp run` and answered `202`; `abcp run` could not open its workflow database and exited before it registered, so the
run never appeared (`GET /v1/runs/<run>` still answers `404`) and a replay of the request is refused for reconciliation
(its launch intent is kept).

**Built:**

- Before the launch intent is written, admission reaches the profile's workflow database as `abcp run` will
  (`workflowauthoritypg.Open`, at most 5 s, then closed; nothing read or written). Unreachable: `503
  admission_unavailable` (retryable), nothing launched, so the same request admits the run once the database is back.
- After the launch, admission waits at most 10 s (`defaultLaunchWait`, inside Repo C's 30-second request timeout) for
  the run to register or exit. `abcp run` holds the launch lock for its whole life, so a free lock means it exited. A
  run that exits before it registers (the database lost in between, any early failure) has its launch intent cleared and
  is answered `503 admission_unavailable`: the same request launches it again. A run that registers is answered `202`
  as soon as it does (usually within a second or two); one still starting when the wait ends is answered `202` as before.

Repo C already words `ADMISSION_UNAVAILABLE` ("the builder is restarting or busy: start the build again in a minute");
R7 is for that press to need no one.

**The resume (built).** An ABCP restart does not interrupt a build: each `abcp run` is its own process, and `abcp
serve` can stop and start beside it (the quiet restarts wait for no build running anyway). What interrupts one is the
host's restart (`4a40c064`, 2026-10-16), an out-of-memory kill or a `kill`. Until now, on SIGTERM `abcp run` recorded
`CANCELLED` with R1's `INTERRUPTED`, a state with no way out; on SIGKILL it recorded nothing and its catalog owner lease
went stale, the run left `IMPLEMENTING` for ever. Now a run whose profile allows a fresh attempt (`ralphex.fresh_attempts:
1`, R3; no template changes, every product template has it since R3 went live) goes on, once
(`internal/run/resume_interrupted.go`, `internal/runadmission/interrupted*.go`, `cmd/abcp/resume_interrupted.go`):

1. **SIGTERM records `RECOVERY_REQUIRED`.** `abcp run` cancels its run with a cause of its own for each signal
   (`runctl.NewSignalCause`). When the builder's run ends and the cause is SIGTERM (not an API cancellation, which has its
   own), the run is a worktree run that implements outside V3 bounds whose plan can be handed to Ralphex again (R3's
   `attemptCanStartAgain`), its ledger shows no resume yet and no `API_CANCEL_REQUESTED`, the attempt records `IMPLEMENTING
   → RECOVERY_REQUIRED` in place of `CANCELLED`: `outcome`, `exit_code`, `signal`, R1's `failure_detail` (cause
   `INTERRUPTED`, `attempts` the attempt's), `interruption: SIGTERM`, `recovery: RESUME`, `next_attempt`. Ralphex ending
   by itself on the same SIGTERM (it reaches the whole host at a shutdown) counts too. SIGINT (a person at the terminal)
   and every other case end as before. `/failure` answers no `failure` for it (it is not a stop state) and activity resets
   on it as on `RETRYING` (the worktree "not there yet" until the resumed attempt implements).
2. **`abcp serve` relaunches the run once.** At start and every 5 minutes (`ResumeEvery`), the resume pass reads each
   registered run not yet settled: its ledger (`runctl.InterruptionOf`) and its owner lease. A run is launched when its
   ledger stops at that `RECOVERY_REQUIRED` (recovery `RESUME`) or in `EXECUTION_STARTING`/`IMPLEMENTING`, never resumed
   before and never asked to stop, its owner's process is gone (the lease retired or its process identity dead), its last
   ledger event is at most 24 hours old (`ResumeWithin`: one interrupted while ABCP itself was down longer is left), its
   own frozen manifest allows it (`runctl.ResumesAfterInterruption`: `fresh_attempts`, a worktree run, not a review or an
   exact edit), and its launch lock is free (a live owner or a launch of it holds it). Then, as admission's launch: the
   wall and the workflow database checked (either down: the pass stops, writes nothing, the next tries again), a durable
   record written (`<service-root>/admissions/resumes/<run>.json`, `InterruptedResumeV1`, never removed but when `abcp
   run` could not even start), and `abcp run --resume-interrupted` launched from the run's admission binding with the
   launch lock as file 3. At most 4 a pass (`ResumesPerPass`). Ended, resumed, refused or unadmitted runs are settled and
   not read again by that `abcp serve`. One line each: `abcp interrupted runs resume ready=true …` at start, `abcp resume
   launched run=… state=… attempt=…`, `abcp resume waits run=… cause=…`, `abcp resume skipped run=… reason=…`.
3. **`abcp run --resume-interrupted` takes the run over and goes on as R3's fresh attempt.** It needs `--service-root`.
   It retires the owner lease the last `abcp run` left (only once that process is gone; refused while a cancel still waits
   for that owner in the action journal: a run its owner stopped is not resumed), under the journal's lock then the
   catalog's, as its watcher would have; then registers as every `abcp run` does (the next owner generation, its cancel
   watcher, the action journal; the same attempt id, so Repo C's Stop build reaches it). The runner reads the ledger again:
   not resumable, it records nothing and exits (`RESUME_NOT_ALLOWED`); from `EXECUTION_STARTING`/`IMPLEMENTING` (a SIGKILL)
   it first records `→ RECOVERY_REQUIRED` (`resume-controller`, `interruption: OWNER_GONE`, R1's `INTERRUPTED`). Then,
   under its run slot and (until a walled run's setup is done) the repository lease again, `RECOVERY_REQUIRED →
   EXECUTION_STARTING` (start evidence `attempt`, `resume_after: INTERRUPTED`, `retained_worktree_removed`) `→
   IMPLEMENTING`: the attempt numbered from the ledger (its `EXECUTION_STARTING` edges plus one), the retained worktree
   removed first with Git's own commands, inside the wall for a walled run (its uncommitted work goes with it; its commits
   stay on the branch), a walled plan copy and Ralphex evidence named `-fresh-<attempt>`, Ralphex's own task prompt with a
   "RESUMED ATTEMPT" section (the session before was stopped from outside; only its commits are kept). Ralphex appends to
   the progress file, which ended `Failed:` (SIGTERM) or with no footer (SIGKILL), so one session. R3's fresh attempt stays
   available after a resume when the run had not used it (its ledger's `RETRYING`): at most three builder sessions.
4. **Bounds.** One resume per run, ever: a run whose ledger shows a resume ends `CANCELLED` (`INTERRUPTED`, R1's
   attempts) at its next SIGTERM, and serve's record keeps it from a second launch whatever happens; none for a run asked
   to stop; at most 4 a pass and none older than a day.

**Not resumed (as before):** a run interrupted before its builder started (`RUN_CREATED`, `AUTHORITY_VALIDATED`, waiting
for its run slot), between attempts (`RETRYING`), or in acceptance (`IMPLEMENTATION_COMPLETED`, `BRANCH_ACCEPTANCE_PENDING`:
a Ralphex that succeeded leaves a `Completed:` footer, §6.1); a resumed run killed again by SIGKILL stays as it was. And
R7's queue never collides with it: the queue launches only runs that never registered, the resume only registered ones,
each under the run's launch lock.

**Repo C** reads `RECOVERY_REQUIRED` as still preparing ("picking up where it stopped") while ABCP resumes it
(`journey.ts` counted it `PAUSED`).

### 6.3 R7: a queue at admission (built, PR #197; live after Repo C)

**What is passing today.** After R6, ABCP refuses an approved build for a passing reason with `503`: `admission_unavailable`
(the workflow database unreachable, the run exiting before it registers, a launch lock or start failing),
`build_wall_unavailable` (the wall's probe failing), and, before the controller, `service_busy` and
`authoritative_read_busy` (the HTTP layer's own guards). Repo C maps all four to `ADMISSION_UNAVAILABLE`. A *restarting*
ABCP answers nothing (connection refused, or Repo C's 30-second timeout): Repo C's `TRANSPORT_AMBIGUOUS`. A *capacity
wait* (an open-model engine full) is already inside the run (`on_unavailable: wait`, `CAPACITY_WAIT`), not a refusal.

**ABCP's side, as built.** A profile with `queue_admissions: true` (profiles.json; not part of the profile's binding
digest; omitted, refusals as before) answers an admission whose request is valid and whose binding is frozen (profile,
base, template, executor, exact edit and read paths all accepted) but whose launch meets a passing cause with `202` and
the same body as today, and queues it instead of refusing it. The causes (`internal/runadmission/queue_linux.go`):
`build_wall_unavailable` (a wall is configured but its probe fails), `workflow_database_unavailable` (R6's probe),
`run_exited_before_registering` (R6's wait) and `launch_failed` (`abcp run` could not be started).

- A durable entry, `<service-root>/admissions/queued/<run>.json` (`AdmissionQueueEntryV1`: the run, its receipt's name,
  the cause, `queued_since`, attempts, `next_try_at`, state `QUEUED` or `EXPIRED`), written before the `202`.
- `abcp serve` passes over the queue every 30 s, oldest first, at most 4 launches a pass, a due entry only: the run's
  launch lock free (else a launch of it is starting), the wall and the database checked again, then the launch exactly
  as admission's (launch intent, `abcp run`, R6's wait). Registered: the entry goes. The same cause: it stays, its next
  try 30 s later, doubling to at most 5 minutes. Queued 30 minutes: `EXPIRED`, kept 7 days, then removed.
- While queued, every route that checks a run's registration (`GET /v1/runs/{run}`, `/activity`, `/events`, `/timeline`,
  `/failure`, `/evidence` and the rest) answers `404` with `{"error": {"code": "run_queued", "retryable": true,
  "details": {"queued_since": "<RFC 3339>", "cause": "<cause>"}, ...}}`; once expired, `404` with code
  `admission_expired`, `retryable: true` and the same details. Any other unknown run answers `404 not_found` as before.
- A replay of the request while queued answers `202` and launches nothing; after expiry it is admitted again (queued again
  while the cause holds).
- `service_busy` and `authoritative_read_busy` stay refusals (they are seconds long and come before admission reads the
  request); a down ABCP cannot queue what it never received.

**Why it goes live after Repo C:** without Repo C reading `run_queued`, a queued build looks to today's Repo C exactly like
`58cbcfac` (a `202`, then `404` for ever), the very failure R6 removes.

**What Repo C must change (exact):**

1. `HttpAbcpRuntimeClient`: `404 run_queued` maps to a new kind `RUN_QUEUED` (not an error; its `queued_since` and cause
   kept), `404 admission_expired` to `ADMISSION_EXPIRED` (an ending: the build could not start, nothing charged); today an
   unknown code is `REMOTE_ERROR`.
2. The build's state (`lib/journey.ts` `buildPhase`, `BuildNotices.BEFORE_WORK`): a queued run is *preparing*, worded
   as waiting for the builder ("It starts by itself; nothing to press"); an expired one ends with the existing
   `ADMISSION_UNAVAILABLE` words and **Start the build again** (the same request id, so never two builds).
3. Polling (`NOTIFICATION_POLLING_BUDGET.md`): a queued run is read every 60 s and no longer than the queue's 30 minutes
   plus one read.
4. A down ABCP (`TRANSPORT_AMBIGUOUS` from a refused connection, and `ADMISSION_UNAVAILABLE` from `service_busy` /
   `authoritative_read_busy`): Repo C itself sends the same request again (the same `X-Request-ID` and body; ABCP's
   admission is idempotent) every 30 s for up to 10 minutes before it shows the refusal, since nothing on ABCP's side can
   hold a request it never received.
5. The admission response is unchanged (`202`, `run_id`, `run_url`): no change to `parseAccepted`.

The restart that takes R7 live turns `queue_admissions` on in the product profiles; until then the queue is off and
refusals are as today, so the order of the two releases cannot strand a build.

## 7. Going live (R1, R2)

1. **Merge** (covered by the owner's answer of 2026-11-07).
2. **Build** the binary on the host from the merge, as `$R/a-abcp-build-b191.sh` did.
3. **Stage** the product templates as `.new` (a binary before this PR refuses a template with `task_retry_count`, so
   they go in with the new binary only), in `$C=$R/abcp-config`:

   ```text
   python3 -I - "$C" <<'PY'
   import json, os, sys, glob
   for path in sorted(glob.glob(os.path.join(sys.argv[1], "manifest-template*.json"))):
       data = json.load(open(path))
       if data.get("policy_version") != "abcp-product-default-v1":
           continue
       new = json.loads(json.dumps(data)); new["ralphex"]["task_retry_count"] = 3
       assert {**data["ralphex"], "task_retry_count": 3} == new["ralphex"] and {k: v for k, v in new.items() if k != "ralphex"} == {k: v for k, v in data.items() if k != "ralphex"}
       fd = os.open(path + ".new", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
       os.write(fd, (json.dumps(new, indent=2, ensure_ascii=False) + "\n").encode()); os.close(fd)
       print("staged", path + ".new")
   PY
   ```

4. **One quiet restart** with no build running, as `$R/a-abcp-restart-b191-tr7.sh` did: copy each live template to
   `.pre-build-recovery`, move the `.new` files into place, start the new binary, check `GET /v1/engines` answers every
   profile `ready` as before.
5. **Check it live:** `GET /v1/runs/<a failed run of the last days>/failure` answers `RunFailureV1` with `recorded:
   false`; a running build's answers no `failure`; the next build's `EXECUTION_STARTING` evidence says
   `task_retry_count: 3`; the next failed build's answer says `recorded: true` with its cause, check and output.
6. **Back:** the `.pre-build-recovery` templates, and the previous binary (`add6f4e`), with no build running. A run
   admitted with `task_retry_count` names it in its authority, which the previous binary's preview reads strictly, so
   such runs cannot be previewed by it: go back only before the first such run is wanted in Try it.

## 8. Going live (R3)

1. **Merge.** Covered by the owner's answer of 2026-11-07.
2. **Build** on the host from the merge: `$R/a-abcp-build-r3.sh <merge sha>`, as `a-abcp-build-b193.sh` built `1d69c32`. It
   runs the walled test of a fresh attempt (`TestAWalledRunGetsAFreshAttemptAfterItsWorktreeIsRemovedInsideItsWall`) with
   the other wall tests, none skipped, and stages `a-abcp-start-<sha7>.sh`.
3. **Templates:** each product template in `profiles.json` (49) staged as `<name>.r3-new` with `"fresh_attempts": 1`
   (`a-r3-stage.py`); the binary before this PR refuses the field, so they go in only while ABCP is down.
   `a-r3-validate.sh` reads them as both binaries read a template at start. Two more changes ride on the same restart:
   - **the store kit's check** (Repo C `BUILD_RELIABILITY.md` §5.1 left it for the kit's next install): `store-kit-1`
     still held the check of 2026-09-29, which refuses a variant's own picture (`variants[].image`, what a catalogue of
     several colours gives each colour). `a-store-install.sh <repo-c sha>` installs main's `check_store.py` and
     `STORE_FORMAT.md` (still format `autobuild-store/1`: it takes every store it took before), and the four Shopify
     `.r3-new` copies carry its pin in their validation and acceptance, so the kit and its pin move together;
   - **the two Groq templates** (not in `profiles.json` until Repo B #192 goes live): made again from their Together twins,
     so they carry the node kit's new check pin, `task_retry_count` and `fresh_attempts`, and `a-groq-stage.py check`
     holds again.
4. **One quiet restart:** `$R/a-abcp-restart-r3.sh <merge sha> <repo-c sha>` stops `1d69c32`, swaps the store kit, keeps
   each template as `.pre-r3-<time>`, moves the copies in, starts the new binary, checks the engines and `/failure`, and
   moves the boot entry; on any failure it puts everything back and starts `1d69c32` again. Its `DRY=` mode runs every
   step on copies.
5. **Check it live:** the next build's `EXECUTION_STARTING` evidence; a failing build's `RETRYING` and second attempt.

## 9. Going live (R5, R6)

1. **Merge**, covered by the owner's answer of 2026-11-07; **build** on the host from the merge
   (`$R/a-abcp-build-batch.sh <merge sha>`): the packages' tests, the wall tests (with
   `TestAWalledRunsFlakyAcceptanceIsRunAgainOnTheProductBranch`, none skipped) and the Codex end-to-end tests.
2. **Templates:** each product template in `profiles.json` (49) and the two Groq ones beside it staged as
   `<name>.batch-new` with `"acceptance_reruns": 1` before its `"acceptance"` (`a-batch-stage.py`); `c0aeb06` refuses the
   field, so they go in only while ABCP is down. `a-batch-validate.sh` reads them as both binaries read a template.
3. **One quiet restart** (`a-abcp-restart-batch.sh`, the same waits, refusals and way back as R3's; `DRY=` on copies), with
   whichever Repo B PRs of this batch are merged by then. R6 (PR #196) changes no template: its binary refuses a launch
   while the database is down and waits for the run to register.
4. **Check it live:** the next accepted build's `BRANCH_ACCEPTED` payload; a flaky acceptance's `acceptance_flaky`.

## 10. Going live (R6's resume, with R7 and lane keys)

One build and one quiet restart take whichever of R7 (#197), R6's resume (this PR) and lane keys (Repo B #198) `main`
holds, each by its own files: `$R/a-abcp-build-resume-batch.sh <main sha>` builds the binary (each one's tests, the wall
tests with R3's and `TestAResumedWalledRun…`, none skipped, the Codex end-to-end tests, `abcp lane-keys` on the live
profiles when lane keys are in) and stages `a-abcp-start-<sha7>.sh`; `$R/a-abcp-restart-resume-batch.sh <main sha>`
waits as every quiet restart does, stops the live ABCP, moves in R7's `profiles.json.r7-new` (kept as
`profiles.json.pre-r7-<time>`) and lane keys' `lanes.control` in `grants.json` (kept as `grants.json.pre-lane-keys-<time>`)
for what the commit holds, starts the new binary and checks the engines, `/failure`, the resume pass's ready line and
`GET /v1/lanes` as each is in; on any failure everything goes back and the binary before starts again. Its `DRY=` mode
does every step on copies. R6's resume changes no file: its pass starts with the binary.

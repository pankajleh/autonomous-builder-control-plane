# Build recovery: say why a build failed, and give it more chances before it does

Date: 2026-11-07

Exact base: `add6f4e` (PR #191)

Status: **R1 and R2 built in PR #193 (`1d69c32`, live since 2026-11-07 15:24 UTC); R3 built in PR #194; R2b and R4 to
R7 designed below for the next PRs.** Repo C design note
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
- **R5, acceptance once more.** When final acceptance fails a check the run's own validation passed, run that acceptance
  once more; passing, accept the branch with `FLAKY` in its evidence (and R1 records nothing); failing, R3's attempt.
- **R6, restarts.** A run that a restart interrupted (no terminal transition, its `abcp run` gone; or `INTERRUPTED`)
  resumes from its last commit as a fresh attempt (R3's machinery); admission answers `503` while the workflow database
  cannot be reached, never `202` and nothing (2026-10-17, `58cbcfac`).
- **R7, a queue at admission.** A passing refusal (unavailable, busy) is accepted and queued, not refused, and starts
  when the cause clears; Repo C words the wait.

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
3. **Templates:** each product template in `profiles.json` (and the two Groq ones beside it) staged as `<name>.r3-new`
   with `"fresh_attempts": 1` (`a-r3-stage.py`); the binary before this PR refuses the field, so they go in only while
   ABCP is down. `a-r3-validate.sh` reads them as both binaries read a template at start.
4. **One quiet restart:** `$R/a-abcp-restart-r3.sh <merge sha> <repo-c sha>` stops `1d69c32`, keeps each template as
   `.pre-r3-<time>`, moves the copies in, starts the new binary, checks the engines and `/failure`, and moves the boot
   entry; on any failure it puts everything back and starts `1d69c32` again. Its `DRY=` mode runs every step on copies.
5. **Check it live:** the next build's `EXECUTION_STARTING` evidence; a failing build's `RETRYING` and second attempt.

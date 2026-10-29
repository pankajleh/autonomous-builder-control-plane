# Change jobs: a customer's code changed by the agent, its commands run only in the stack sandbox

Status: **built, dormant** (F3a). Repo C design note `IMPROVE_EXISTING_PRODUCTS.md` §14 (F3). A person asks Autobuild to
**fix or change** something in a repository Autobuild did not write, or to **write tests** for it. The change is handed
back to Repo C as a patch, which Repo C opens as a pull request with its own GitHub token.

## Why not a run

A run drives the Codex or Claude Code tool through Ralphex as a plain process on the host, with the host's network and
the engine's credentials, and its checks run on the host (`internal/run`). Those tools run their own shell, so a run on a
customer's repository would run the customer's code (its install scripts, its tests) on the host. A change job keeps the
agent on the host, because it needs the model and its credentials, but takes its shell away: every command it runs goes
to the job's stack sandbox (`docs/plans/stack-jobs.md`), and nothing from the repository is loaded by the agent's tool.

## The API

Under a new grant, `change.run`:

- `POST /v1/change-jobs` with `{"request_id", "source", "cache_key", "kind", "words"}`: `source` a folder directly under
  `--change-source-root`, as stack jobs name theirs; `cache_key` the project's cache digest; `kind` `fix`, `tests` or
  `assess` (below);
  `words` the person's own (at most 4,000 characters, no NUL). `202` with the job, or the same job for the same
  `request_id` and body; `409` for the same `request_id` with another body; `422` when the folder is missing or a link;
  `429` when 10 are waiting; `503` when change jobs are not configured. An optional `"stacks"` names the stacks the job
  may change and check, as a stack job's (`stack-jobs.md`, The API): with none of them in the code, the job ends `FAILED`
  with `UNSUPPORTED_STACK` before any agent or sandbox runs.
- `"targets"`, for `assess` only: the versions the project would move to, `{"java": "21"}`, shaped as a stack job's pinned
  `versions` (`stack-jobs.md`, The API); part of the request.
- `GET /v1/change-jobs/{id}`: the job. `GET /v1/change-jobs/{id}/patch`: the patch once handed back (`text/x-diff`).
  `POST /v1/change-jobs/{id}/stop`.

A job: `{"id", "kind", "state", "phase", "created_at", "started_at", "ended_at", "before": [parts], "after": [parts],
"agent": {"summary", "review", "commands", "input_tokens", "output_tokens", "cost_usd", "seconds"}, "result": {"patch_sha256",
"files": [{"path", "status"}], "insertions", "deletions"}, "proof": {"state", "files", "tests", "note"}, "reason"}`; a test
step may carry `"coverage": {"covered", "total", "tool"}`. Parts are stack jobs' parts. `state` is `QUEUED`,
`RUNNING` (with `phase` `BASELINE`, `AGENT`, `REVIEW`, `CHECKING`, `RESULT`), `HANDED_BACK`, `ASSESSED` (an assessment, with
its `plan`), `NOT_HANDED_BACK`, `FAILED`, `STOPPED` or `UNKNOWN`. `reason`:

- not handed back: `NO_PLAN` (an assessment's agent answered no plan of the right shape), `SECRET_IN_CHANGE` (the patch or what the agent said carries a private key, a provider's token, or one of the engine's own settings), `BASELINE_FAILED` (a fix needs the code to install, a test job needs it to build), `NO_CHANGE`,
  `WORSE_AFTER` (it no longer builds, fewer tests pass or more fail, or a test job's tests do not pass), `TOO_LARGE` (over
  200 files or 2 MB), `FORBIDDEN_PATH` (CI settings, workflows, `.claude/` or `.mcp.json`, a submodule, or a file that looks like a secret);
- failed: `NO_STACK`, `UNSUPPORTED_STACK`, `SOURCE_REFUSED`, `SANDBOX_UNAVAILABLE`, `ENGINE_UNAVAILABLE`, `TIMED_OUT`;
  stopped: `STOPPED`.

## The job

1. **The work folder:** the source copied as stack jobs copy it (regular files and folders, never a link or `.git`,
   within the limits), and its files committed as `start` in a Git directory kept beside it, never inside it, so the
   sandbox mounts files only. Git runs with no system or global configuration, no hooks and no file monitor.
2. **The baseline:** each part's steps (`Steps`), as a stack job runs them, recorded in `before`.
3. **The agent** (`Engine`): Claude Code, headless, in **restricted mode** (`--restricted`, v2.1.248 or later; an older
   one refuses the flag and the job ends `ENGINE_UNAVAILABLE`), in the work folder, as this controller's user:
   - restricted mode removes every tool that runs commands or code and the web, confines the file tools to the working
     folder, and loads no user or project settings, so no hooks, plugins or MCP servers from the repository; only ABCP's
     MCP file (`--strict-mcp-config`);
   - its built-in tools are read, search, edit and write (`--tools`); edits in the folder and the job's **run** tool are
     allowed (`Edit(./**)`, `mcp__abcp__run`), and anything else that would ask is refused (`--permission-mode dontAsk`);
     the shell, the web and sub-agents are also named as never (`--disallowedTools`); turns and time are limited;
   - an empty home of its own for the job, and the engine lane's settings only (`internal/enginelane`, executor
     `claude`), never the controller's environment;
   - **run** is `abcp stack-tool --socket <path>`, an MCP server over stdio that hands each command to the job's broker:
     a Unix socket (0600) in a folder only this controller's user reaches, served by `abcp serve`, which runs it with
     `Exec` in the job's sandbox (in `/work` or a folder under it, the part's environment, at most 10 minutes, at most
     200 commands, a fresh container when one could end first) and answers its exit code and its last output, masked;
   - its prompt: the kind's rules, the person's words, the baseline, the limits. A second, shorter session reviews the
     diff against the words and may correct it.
4. **A fix's tests on the code before it** (Repo C design note `FIX_AND_TEST.md` §3): the change's added or changed test
   files, by the stack's own conventions (`testFile`: Python's `test_*.py`, `*_test.py`, `conftest.py` and `tests/`;
   Java's `src/test/`; Node's `*.test.*`, `*.spec.*`, `__tests__/`, `test/`), copied onto the source again in a folder of
   their own, installed from the cache and tested in a fresh sandbox, as the baseline was. `proof` says `FAILED_BEFORE`,
   `PASSED_BEFORE`, `NO_TEST` or `NOT_RUN` (with why), with the files and the counts. Shown, never required; the phase
   stays `CHECKING`, which Repo C reads.
5. **After:** each part's steps again, recorded in `after`. Handed back only when no worse than before.
6. **Coverage, for a test job** (`FIX_AND_TEST.md` §4): its baseline and its checks after run the tests with the stack's
   coverage tool from the image (`/opt/autobuild/coverage`: coverage.py, JaCoCo's agent through `JAVA_TOOL_OPTIONS`, V8's
   own coverage read by c8) and keep the image report's answer on the test step as `coverage`: the project's lines run,
   its lines in all, the tool. What a run before left is removed first; the tests' exit code is kept; an image without the
   tools runs the tests as they are, and a run that left nothing to read has no `coverage`.
7. **Node's counts** (`FIX_AND_TEST.md` §5): when a Node part writes no JUnit report, its counts come from the summary
   its runner printed (Jest, Vitest, Node's own, Mocha); none when the output matches none of them, or more than one.
8. **The result:** the changed files copied out as the source was (regular files only, dependency and build folders left
   out), committed with Git objects only, and diffed against `start`: the patch, its digest and its files.
9. **Limits:** at most two jobs at once and one per cache key; 90 minutes a job; the folder, the socket and the agent's
   home deleted when it ends. Caches are the change root's own, apart from stack jobs'.

## Assessments (Repo C `LEGACY_MIGRATION.md` §4)

A job of the kind `assess` answers what it would take to move a project to supported versions, and changes nothing:

1. **The baseline**, as a change's, on the versions the project uses now; the agent starts whatever it shows (an old
   project may not install today, which is what an assessment is for). Test steps carry their coverage, as a test job's.
2. **The checks on the targets** (phase `CHECKING`): each part that `targets` moves to a version a profile serves, on a
   fresh copy of the source, so the work folder stays as the baseline left it; recorded in `after`, each part with
   `version_note` "asked to run on 21 instead of 17". Without targets, or with none served, `after` stays empty.
3. **The agent** (phase `AGENT`), with the run tool on the project's own versions and its rules: read the code, run what it
   needs, change no file. Its prompt carries the words, both checks and the plan's shape; its answer is the plan alone, one
   JSON object (a fenced code block is read too). No review session.
4. **The plan**, read strictly: `{"summary", "paths": [{"what", "from", "to"}], "steps": [{"title", "words", "risk", "size",
   "files", "tests", "tests_first"}]}`, no other field and nothing after it; a summary of at most 2,000 characters; at most 8
   paths; 1 to 20 steps, each titled in at most 200 characters, its words at most 4,000 (so a step can start a change job),
   `risk` `low`, `medium` or `high`, `size` `small`, `medium` or `large`, at most 50 files as clean paths inside the
   repository, its tests in at most 1,000 characters; no control characters but line breaks and tabs where text runs over
   lines. Every text masked as a step's output is. An answer that is not a plan ends `NOT_HANDED_BACK` with `NO_PLAN`; one
   carrying a secret, `SECRET_IN_CHANGE`. Otherwise the job ends `ASSESSED` with `plan`; the agent's summary is the
   plan's. There is no patch: `GET …/patch` is `404`.

## Settings

`abcp serve --change-root <private folder> --change-source-root <folder> --change-engine-file <protected file>` with the
stack profiles file. The engine file: `{"schema_version": 1, "binary": "/abs/path/claude", "lane_name": "…",
"lane_file": "/abs/path/….env", "model": "…"}`. Without all of them, or with one that does not check, the route says
change jobs are not configured and the rest of the service runs.

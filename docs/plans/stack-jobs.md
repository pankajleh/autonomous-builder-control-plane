# Stack jobs: a customer's code installed, built and tested in a sandbox

Status: **planned** (F2a). Repo C design note `IMPROVE_EXISTING_PRODUCTS.md` §13 (F2; the owner, 2026-10-26: "All as
recommended", decision 3: JavaScript and TypeScript, Python and Java first). A person connects code Autobuild did not
write and asks **Check it builds**: install what it needs, build it, run its own tests, and say what passed. Nothing is
changed or committed and no model runs. The result is the baseline a change job (F3) starts from.

## Why not a run

A run is Ralphex in a worktree of the profile's repository, on the host, as this controller's user, with the host's
network and tools. That is fine for code built from Autobuild's own kits and never for a customer's existing code, which
may run anything at install. A stack job is therefore not a run: no manifest, no Ralphex, no acceptance, no worktree. It
uses what previews already have (`internal/preview`): gVisor, a private network per group in `10.213.0.0/16` that the
host rule lets reach only the egress proxy's port 3129, digest-pinned images checked before use, a read-only root, no
capabilities, limits, and the CONNECT egress proxy.

## The API

Under a new grant, `stack.run`:

- `POST /v1/stack-jobs` with `{"request_id", "source", "cache_key"}`: `source` is a folder name directly under
  `--stack-source-root` (`[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}`), where Repo C has left a clone or an upload; `cache_key`
  names the project's dependency cache (`[a-f0-9]{64}`, a digest Repo C makes from its project; never a name). Answers
  `202` with the job, or replays the same job for the same `request_id`. Refused `422 stack_job_not_accepted` when the
  folder is missing, is a link, holds a link out of itself, or is over 300 MB or 20,000 files; `503` when stack jobs are
  not configured.
- `GET /v1/stack-jobs/{id}`: the job (below). `POST /v1/stack-jobs/{id}/stop`: stops a queued or running job.
- One job runs at a time; others wait (`QUEUED`), at most 20 waiting.

A job: `{"id", "state", "created_at", "started_at", "ended_at", "parts": [...], "failure"}`. `state` is `QUEUED`,
`RUNNING`, `PASSED`, `FAILED`, `STOPPED` or `UNKNOWN` (a job running when the controller stopped). Each part:
`{"path", "stack", "version", "version_note", "tool", "steps": [{"name", "state", "command", "seconds", "exit_code",
"output_tail", "tests": {"passed", "failed", "skipped"}}]}`. `stack` is `python`, `java`, `node` or `unsupported` (with
what was found, for example `go`). A step's `state` is `WAITING`, `RUNNING`, `PASSED`, `FAILED`, `SKIPPED` (with a
`note`), `STOPPED` or `NOT_RUN`. `failure` is a stable code: `NO_STACK`, `UNSUPPORTED_STACK`, `SOURCE_REFUSED` (the
copy met a limit), `INSTALL_FAILED`, `BUILD_FAILED`, `TESTS_FAILED`, `TIMED_OUT`, `TOO_LARGE` (the work grew past the
profile's `work_bytes`), `STOPPED`, `SANDBOX_UNAVAILABLE` (no profile, the probe failed, the container would not start, or
less than 10 GB free).

## The job

1. **Copy** the source folder into `{stack-root}/jobs/{id}/work`, skipping links and special files; the source is never
   written. The cache is `{stack-root}/caches/{cache_key}` (created at most 2 GB; all caches at most 10 GB, the least
   recently used removed first, never one in use).
2. **Find the parts and their stacks** from the files at the top of `work`: `pyproject.toml`, `requirements*.txt`,
   `setup.py` (Python); `pom.xml`, `build.gradle`, `build.gradle.kts` (Java); `package.json` (Node). With none at the top,
   each folder one level down that has one is a part, at most three. Several manifests in one folder: Java, then Python,
   then Node.
3. **Pick the version and tool**: Python from `.python-version`, else 3.12 when `requires-python` allows it, else the
   lowest version it allows among 3.10–3.13; tool `uv` with `uv.lock`, `poetry` with `poetry.lock`, else `pip`. Java from `.java-version`,
   `maven.compiler.release`, `maven.compiler.source`, `java.version`, or Gradle's `languageVersion`, else 21, then 17 or
   21 (the nearest not older); tool Maven or Gradle, the project's wrapper when it has one. Node from `.nvmrc`,
   `.node-version` or `engines.node`'s first major, else 22, then 20, 22 or 24; tool from the lockfile (`npm`, `pnpm`,
   `yarn`). A version the images lack is the nearest newer one, said in `version_note`.
4. **The container**, one per part, from the stack profile for that stack and version: `docker create --pull=never
   --runtime=runsc --network <private> --user <profile user> --cap-drop=ALL --security-opt=no-new-privileges --read-only
   --cpu-quota --memory --memory-swap --pids-limit --ipc=none --log-driver=none`, as this controller's own uid and gid
   (which own the folders; never root), `/work` and `/cache` bound read-write,
   `/tmp` a small tmpfs (with `exec`: the code already runs from `/work`, and native builds run what they compile there), the image checked by `imageSafe` (its digest among its RepoDigests, its environment from the
   short list). The environment is built from nothing: `PATH`, `HOME=/cache/home`, `CI=1`, the proxy for each tool
   (`HTTPS_PROXY`/`HTTP_PROXY`; `JAVA_TOOL_OPTIONS` with `https.proxyHost`/`https.proxyPort` and `user.home=/cache/home`, since Java takes its home from the user database, never from `HOME`, and the runner's uid is not in the image's or is another user's there; Maven's `settings.xml` and
   Gradle's `gradle.properties` written into the cache's home through real folders only: the cache is the job's to write,
   so anything at those names, a link above all, is replaced, never followed), and the caches' places (`PIP_CACHE_DIR`,
   `UV_CACHE_DIR`, `POETRY_CACHE_DIR`, `npm_config_cache`, `GRADLE_USER_HOME`, Maven's local repository).
5. **The registries, and nothing else**: the group's egress proxy with the profile's rules, new entries beside
   `*.myshopify.com:443`, each an exact list of names on port 443:
   - `pypi`: `pypi.org`, `files.pythonhosted.org`;
   - `maven-central`: `repo.maven.apache.org`, `repo1.maven.org`;
   - `gradle`: `services.gradle.org`, `downloads.gradle.org`, `plugins.gradle.org`, `plugins-artifacts.gradle.org`;
   - `npm`: `registry.npmjs.org`, `registry.yarnpkg.com`, `repo.yarnpkg.com`.
6. **The steps**, each a `docker exec` with its own time limit inside the job's 30 minutes, its output kept to the last
   200 lines (64 KiB), with anything that looks like a key or token masked:

   | Stack | install | build | test |
   |---|---|---|---|
   | python | `uv sync --frozen`; `poetry install --no-interaction`; or `python -m venv /work/.venv` then `pip install -r requirements.txt` (or `pip install .`) | `python -m compileall -q .` | `python -m pytest -q --junitxml=…` when pytest is installed in the environment, else `python -m unittest discover` |
   | java | skipped: Maven and Gradle fetch what the code needs while they build | `sh mvnw` or `mvn -B -ntp -DskipTests package`; `sh gradlew` or `gradle --no-daemon assemble` | `… test` |
   | node | `npm ci` (`npm install` without a lockfile); `corepack pnpm install --frozen-lockfile`; `corepack yarn install --immutable` (Yarn 1: `--frozen-lockfile`) | the tool's `run build` when the script exists | its `run test` when the script exists (not npm's placeholder) |

   A step with nothing to do is `SKIPPED`. A failed install or build ends the part. pytest's and unittest's exit code 5,
   no tests found, is `SKIPPED` with that note, not a failure.
7. **Tests' counts**, once the part's container has ended, from JUnit XML the tools wrote under `work` (`target/surefire-reports`, `build/test-results`, the
   pytest report): at most 200 files and 8 MiB read, parsed with limits, or none.
8. **End**: the evidence is written to `{stack-root}/jobs/{id}/job.json`, the containers and network removed, `work`
   deleted. The job's JSON stays (at most 1,000 kept, oldest removed first).

## The stack profiles

`--stack-profiles-file`, operator configuration, checked when `abcp serve` starts: schema version 1, and for each
profile its `stack`, the `versions` it serves, `image` (`name@sha256:…`), `user` (`uid:gid`, not root), `cpu_quota`,
`memory_bytes`, `pids_limit`, `tmpfs_bytes`, `work_bytes`, `timeout_seconds`, `runtime` (`runsc` only) and `egress` (the
rules above). With `--stack-source-root` and `--stack-root` they turn stack jobs on; without all three the API answers
`503`.

## The probe

Before a profile's first job after start, ABCP runs its image once in the sandbox with the image's `/bin/busybox` (the
stack images carry it, as the preview images do): no default route, `1.1.1.1` and the host unreachable, the proxy
refusing `example.com` and opening the profile's first registry. A profile that fails is not used for an hour, and its
jobs fail `SANDBOX_UNAVAILABLE`.

## Tests

Finding the stack, version and tool for each manifest and lockfile; the steps' commands; the egress rules (each name in,
others and numeric addresses out); the profile checks; the copy refusing links and sizes; the masking; the JUnit counts
with hostile XML; the API (grant, replay, refusals, states, stop); the docker arguments, as `docker_test.go` checks the
previews'. The containers themselves are tried on the build host (F2c), where `runsc` is.

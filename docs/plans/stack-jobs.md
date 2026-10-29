# Stack jobs: a customer's code installed, built and tested in a sandbox

Status: **planned** (F2a); scan jobs added for IX-3 (below). Repo C design note `IMPROVE_EXISTING_PRODUCTS.md` §13 (F2; the owner, 2026-10-26: "All as
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
- `"stacks"`, optional: the stacks the job may run, at most eight names (`[a-z]{1,16}`) each once (Repo C `MORE_STACKS.md`
  §10: a stack for administrators first). A part of any other stack is `unsupported`, with its stack as what was found,
  and is not run, as a stack ABCP does not run yet. Without it, every stack the profiles serve. It is part of the
  request: the same `request_id` with other stacks is `409`, in another order the same request.
- `GET /v1/stack-jobs/{id}`: the job (below). `POST /v1/stack-jobs/{id}/stop`: stops a queued or running job.
- One job runs at a time; others wait (`QUEUED`), at most 20 waiting.

A job: `{"id", "state", "created_at", "started_at", "ended_at", "parts": [...], "failure"}`. `state` is `QUEUED`,
`RUNNING`, `PASSED`, `FAILED`, `STOPPED` or `UNKNOWN` (a job running when the controller stopped). Each part:
`{"path", "stack", "version", "version_note", "tool", "steps": [{"name", "state", "command", "seconds", "exit_code",
"output_tail", "tests": {"passed", "failed", "skipped"}}]}`. `stack` is `python`, `java`, `node`, `go`, `ruby` or
`unsupported` (with what was found, for example `php`, or a stack the request did not name). A step's `state` is `WAITING`, `RUNNING`, `PASSED`, `FAILED`, `SKIPPED` (with a
`note`), `STOPPED` or `NOT_RUN`. `failure` is a stable code: `NO_STACK`, `UNSUPPORTED_STACK`, `SOURCE_REFUSED` (the
copy met a limit), `INSTALL_FAILED`, `BUILD_FAILED`, `TESTS_FAILED`, `TIMED_OUT`, `TOO_LARGE` (the work grew past the
profile's `work_bytes`), `STOPPED`, `SANDBOX_UNAVAILABLE` (no profile, the probe failed, the container would not start, or
less than 10 GB free).

## The job

1. **Copy** the source folder into `{stack-root}/jobs/{id}/work`, skipping links and special files; the source is never
   written. The cache is `{stack-root}/caches/{cache_key}` (created at most 2 GB; all caches at most 10 GB, the least
   recently used removed first, never one in use).
2. **Find the parts and their stacks** from the files at the top of `work`: `pyproject.toml`, `requirements*.txt`,
   `setup.py` (Python); `pom.xml`, `build.gradle`, `build.gradle.kts` (Java); a `Gemfile` asking for a gem beyond a mobile
   app's build tools (`fastlane`, `cocoapods` and the like) (Ruby); `go.mod` (Go); `package.json` (Node). With none at the
   top, each folder one level down that has one is a part, at most three. Several manifests in one folder: Java, then
   Python, then Ruby, then Go, then Node (a Rails project or a Go service has a `package.json` for its front end beside its
   own manifest; Repo C `MORE_STACKS.md` §3). A stack no profile serves (its images are not on this host yet) is
   `unsupported` with what was found, and is not run.
3. **Pick the version and tool**: Python from `.python-version`, else 3.12 when `requires-python` allows it, else the
   lowest version it allows among 3.10–3.13; tool `uv` with `uv.lock`, `poetry` with `poetry.lock`, else `pip`. Java from `.java-version`,
   `maven.compiler.release`, `maven.compiler.source`, `java.version`, or Gradle's `languageVersion`, else 21, then 17 or
   21 (the nearest not older); tool Maven or Gradle, the project's wrapper when it has one. Node from `.nvmrc`,
   `.node-version` or `engines.node`'s first major, else 22, then 20, 22 or 24; tool from the lockfile (`npm`, `pnpm`,
   `yarn`). Go from `go.mod`'s `go` line (the oldest it builds with), else 1.27, among the two releases served; its
   `toolchain` line is never downloaded. Ruby from `.ruby-version`, the `Gemfile`'s `ruby` line or `Gemfile.lock`'s `RUBY
   VERSION`, else 3.4, then 3.3, 3.4 or 4.0; tool Bundler, tests with RSpec when there is a `spec` folder and RSpec is in
   the lockfile or `Gemfile`, else Rake's `test` task when there is a `Rakefile` and a `test` folder. A version the images
   lack is the nearest newer one, said in `version_note`.
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
   - `npm`: `registry.npmjs.org`, `registry.yarnpkg.com`, `repo.yarnpkg.com`;
   - `go`: `proxy.golang.org`, `sum.golang.org` (`GOPROXY` is the module proxy alone, `GOTOOLCHAIN=local`);
   - `rubygems`: `rubygems.org`, `index.rubygems.org`.
6. **The steps**, each a `docker exec` with its own time limit inside the job's 30 minutes, its output kept to the last
   200 lines (64 KiB), with anything that looks like a key or token masked:

   | Stack | install | build | test |
   |---|---|---|---|
   | python | `uv sync --frozen`; `poetry install --no-interaction`; or `python -m venv /work/.venv` then `pip install -r requirements.txt` (or `pip install .`) | `python -m compileall -q .` | `python -m pytest -q --junitxml=…` when pytest is installed in the environment, else `python -m unittest discover` |
   | java | skipped: Maven and Gradle fetch what the code needs while they build | `sh mvnw` or `mvn -B -ntp -DskipTests package`; `sh gradlew` or `gradle --no-daemon assemble` | `… test` |
   | node | `npm ci` (`npm install` without a lockfile); `corepack pnpm install --frozen-lockfile`; `corepack yarn install --immutable` (Yarn 1: `--frozen-lockfile`) | the tool's `run build` when the script exists | its `run test` when the script exists (not npm's placeholder) |
   | go | `go mod download` | `go build ./...` | `go test -v ./...` when a `_test.go` file is within four levels |
   | ruby | `bundle install` (into the cache: `BUNDLE_PATH=/cache/home/bundle`) | skipped: nothing to build | `bundle exec rspec`, or `bundle exec rake test` |

   A step with nothing to do is `SKIPPED`. A failed install or build ends the part. pytest's and unittest's exit code 5,
   no tests found, is `SKIPPED` with that note, not a failure.
7. **Tests' counts**, once the part's container has ended, from JUnit XML the tools wrote under `work` (`target/surefire-reports`, `build/test-results`, the
   pytest report): at most 200 files and 8 MiB read, parsed with limits, or none. A Node or Ruby part without JUnit takes
   them from its runner's summary in the step's last lines (Jest, Vitest, Node's, Mocha; RSpec, Minitest); a Go part from
   each test's own `--- PASS`, `--- FAIL` or `--- SKIP` line, counted in all the step printed as it printed it (subtests
   too), not only the lines kept.
8. **End**: the evidence is written to `{stack-root}/jobs/{id}/job.json`, the containers and network removed, `work`
   deleted. The job's JSON stays (at most 1,000 kept, oldest removed first).

## The stack profiles

`--stack-profiles-file`, operator configuration, checked when `abcp serve` starts: schema version 1, and for each
profile its `stack`, the `versions` it serves, `image` (`name@sha256:…`), `user` (`uid:gid`, not root), `cpu_quota`,
`memory_bytes`, `pids_limit`, `tmpfs_bytes`, `work_bytes`, `timeout_seconds`, `runtime` (`runsc` only) and `egress` (the
rules above). With `--stack-source-root` and `--stack-root` they turn stack jobs on; without all three the API answers
`503`.

## Private registries (F5b)

Repo C design note `IMPROVE_EXISTING_PRODUCTS.md` §15.9. A stack or change job's request may carry `registries`: up to
three of `{kind, use, scope, url, username, token}`. The kind is `npm`, `maven` or `python`. The use is `all` (it mirrors
the public registry) or `own` (the company's packages; npm names the scope). The address is `https` on 443, a name and
never an IP address, a plain path, no user, query or fragment, at most 300 characters. Maven and Python take a user name;
the token is 8 to 4,096 printable characters with no quotes, `\`, `<`, `>`, `&`, `$` or backquote. A request with any
other is refused whole. A stack job's request may now be 16 KB and a change job's 40 KB, room for three registries with
their longest tokens.

- **Kept in memory only:** with the job, never in its record, evidence, answers or logs. `body_digest` covers everything
  but the tokens. A job that a restart leaves `UNKNOWN` loses them.
- **Egress:** each registry's name joins that part's own proxy (`StackSpec.Hosts`), on 443 and to public addresses only,
  as the registry rules' names do.
- **Settings:** written into the cache's home before the job's parts, through real folders only, and removed when the
  job ends, again through real folders only, since the job's code may leave links where they were. They are
  `.npmrc` and `.yarnrc.yml` for npm, `.config/pip/pip.conf` and `.config/uv/uv.toml` for Python, and
  `.gradle/init.d/abcp-registries.gradle` for Maven. Maven's `settings.xml` carries a server, a mirror of `*` for `all`
  and an active profile with the repository for `own`; it is written with the proxy before each Java part and removed
  when the job ends.
- **Masking:** every kept step output, and every answer the change job's run tool gives the agent, has each token
  replaced with `[registry token]`: as given, URL-encoded, fully percent-encoded, base64, and in basic authentication's
  forms. The tokens join the values a change job's leak check looks for (`SECRET_IN_CHANGE`).

## Scan jobs (IX-3)

Repo C design note `SECURITY_AND_COMPLIANCE.md` §11.1 (the owner, 2026-10-28: all four checks together, open-source tools,
no credits). A scan is a stack job with `"scan": true` in its request; `body_digest` then carries it, so a check's digest
is what it was and a request ID names either a check or a scan, never both. The job answers `"scan": true`.

- **Configured by** `--stack-osv-root`: a real folder (absolute, clean, not a link) of OSV's vulnerability data, which the
  build host refreshes. Without it a scan is refused `503 stack_scans_unavailable`; checks are unaffected.
- **The data:** bound read-only at `/osv` (`--mount type=bind,src=…,dst=/osv,readonly`) in a scan's parts only, never in
  a check's or a change job's, and never in the probe.
- **The steps:** **install** as a check's; **build** only for Java, whose classes SpotBugs reads (the others:
  `SKIPPED`, "not needed to scan"); **test** `SKIPPED` ("a scan runs no tests"); then **scan**: the image's own
  `/opt/autobuild/scan --part <the part's folder> --index <n> --stack <python|java|node|go|ruby> --out /work/.abcp/scan-<n>.json`,
  within 10 minutes. A failed install or build does not end a scan's part (the step says "the scan goes on with what
  was installed"): lockfiles, the code and what was installed are still read. A scanner that exits non-zero fails the
  part `SCAN_FAILED`; timeouts, the work's size and the sandbox fail it as a check's do.
- **The result,** read once the part's container has ended, from the part's `scan-<n>.json` under the job's `.abcp`,
  reached through real folders only and opened without following a link: a regular file of at most 1 MiB, one strict
  JSON value (no unknown, duplicate or case-aliased fields), schema 1:

  ```json
  {"schema": 1,
   "findings": [{"kind": "dependency|secret|code|licence", "tool": "[a-z0-9-]{1,40}", "rule": "…", "severity":
     "critical|high|medium|low|info|unknown", "file": "relative/path", "line": 12, "package": "…", "version": "…",
     "fixed": "…", "licence": "…", "ids": ["GHSA-…", "CVE-…"], "title": "…"}],
   "more": 0,
   "licences": {"MIT": 40, "UNKNOWN": 2},
   "checks": {"dependencies": "done", "secrets": "skipped", "code": "failed", "licences": "done"},
   "about": {"tools": {"osv-scanner": "2.6.0"}, "data": {"osv-PyPI": "Wed, 28 Oct 2026 16:48:24 GMT"}}}
  ```

  At most 2,000 findings (`more` counts the rest, at most 1,000,000); `rule` at most 120 characters, `file` 300
  (relative, clean, never above the code), `package` 200, `version`, `fixed` and `licence` 100, `title` 300, at most 10
  `ids` of `[A-Za-z0-9._:-]{1,60}`, `line` 0 to 10,000,000; no control characters. `licences` maps at most 200 licence
  names (at most 100 characters; `UNKNOWN` for packages without one) to how many installed packages each covers, 0 to
  1,000,000; findings of kind `licence` are only the packages to review. `checks` says, for each of the four, `done`,
  `failed` or `skipped` (secrets are looked for in part 0 only). A secret finding has no field for its value. Every text
  is masked as a step's output is, with the job's registry tokens. `about` (optional) says what the scan used, for the
  person's report: at most 12 tools (`[a-z0-9-]{1,40}`) with their versions (`[A-Za-z0-9._+-]{1,40}`), and at most 6
  sources of vulnerability data (`[A-Za-z0-9._-]{1,40}`) with the date each was fetched (printable, at most 60). A missing file, or one that breaks any of this, fails
  the part `SCAN_FAILED` and keeps nothing of it.
- **The evidence:** the part's `findings`, `findings_more`, `licences`, `checks` and `scan_about` (each left out when empty, so a
  check's parts are unchanged). The file comes from tools run in the customer's own container after their install, so
  it is their data about their code: kept and shown to them, never executed or followed as instructions.

## The probe

Before a profile's first job after start, ABCP runs its image once in the sandbox with the image's `/bin/busybox` (the
stack images carry it, as the preview images do): no default route, `1.1.1.1` and the host unreachable, the proxy
refusing `example.com` and opening the profile's first registry. A profile that fails is not used for an hour, and its
jobs fail `SANDBOX_UNAVAILABLE`.

## Tests

Finding the stack, version and tool for each manifest and lockfile; the steps' commands; the egress rules (each name in,
others and numeric addresses out); the profile checks; the copy refusing links and sizes; the masking; the registries' settings, their removal through real folders, their masking and their absence from every
file ABCP keeps; the JUnit counts
with hostile XML; the API (grant, replay, refusals, states, stop); the docker arguments, as `docker_test.go` checks the
previews'. The containers themselves are tried on the build host (F2c), where `runsc` is.

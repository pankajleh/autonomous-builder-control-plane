# Open-model engines: Codex on Together AI and Groq

Status: **built, not on.** No profile names a provider, so nothing changes until the operator adds the trial profiles
(`deploy/open-models/README.md`) in an announced restart with no build running. Repo C design note
`OPEN_MODEL_ENGINES.md`.

## Why

The owner, 2026-11-01: "i want to add APIs of open source like Together AI … or Groq …, for autobuild because claude
and codex are too costly. can ralphex handle that?" Then: "ok go ahead with the trial, i'll add the keys."

Ralphex drives the Codex CLI. Codex can talk to any provider that serves OpenAI's **Responses API**, through a custom
`model_providers` entry in its `config.toml` (`base_url`, `env_key`, `wire_api`). It no longer speaks Chat Completions
to a provider: its source refuses `wire_api = "chat"` ("no longer supported … set `wire_api = "responses"`",
openai/codex discussion 7782). So an open-model engine is Codex as today, with a provider in place of OpenAI. Nothing
in Ralphex changes.

## What a manifest names

`executor.provider`, beside `executor: "codex"` (never with `executor.lane`):

| Field | Meaning |
|---|---|
| `id` | `together`, `groq`: the engine's lane in `GET /v1/engines`, Codex's provider `abcp-<id>`, the key variable `<ID>_API_KEY` |
| `base_url` | `https://api.together.xyz/v1`, `https://api.groq.com/openai/v1`; https only, or http on a loopback address |
| `key_file` | the owner's key file: one line, owner-only (0600/0400), no link in its path; read when each build starts |
| `wire_api` | `responses` (the only one Codex speaks; `chat` is refused with the reason) |
| `codex_home` | the provider's Codex homes, `~/.codex/lanes/<id>` on the host (the usage collector reads it) |
| `models[]` | `name` (lower-case, what manifests, choices and Repo C use), `id` (the provider's), `origin` (country of the maker), `context_window`, optional `auto_compact_token_limit` (80% of the window), `efforts` a build may pick, `default_effort` (empty sends none), `max_concurrent_builds` (1–8, default 1) |
| `preflight` | ask `GET /models` before each build (free) whether the key and the model answer |
| `check_models` | let `GET /v1/engines` ask it too, cached 5 minutes; never changes `ready` |
| `queue_timeout` | how long a build may wait to start (slot and provider), default `20m`, 1s–2h |
| `on_unavailable` | `wait` (default): fail before anything started; `fallback`: start on `fallback` instead |
| `fallback` | the controller's own Codex sign-in: `task_model`, `review_model`, both levels `xhigh` |
| `request_max_retries`, `stream_max_retries` | Codex's own retries (default 8 each), which honour Retry-After with exponential backoff |

`task_model` names a model by `name`. `review_model` is left out: the review runs on the task's model. The levels are
the model's (`efforts`), or empty for its `default_effort`; the "Codex runs at xhigh only" rule stays for the
controller's own Codex. A profile's `executor_choices.models` must be the provider's models, each accepting the
manifest's levels and every level offered.

## What a build does

Before Ralphex starts (`internal/run`, `startOpenModel`; `internal/openmodel`, `Begin`):

1. Reads the key file under the engine lanes' rules. Missing: the build fails, `OPEN_MODEL_ENGINE_KEY`.
2. Takes one of the model's build slots: `slot-<n>.lock` files under `<codex_home>/slots/<model>/`, held with `flock`
   across every `abcp run` and every profile that shares the folder, until Ralphex exits. It waits up to
   `queue_timeout`.
3. With `preflight`, asks `GET <base_url>/models`: 401/403 is a refused key, a missing id a missing model (both fail
   the build); 429, 5xx and no answer are retried with backoff from 2 s doubling to 60 s, or the provider's
   Retry-After when longer (at most 5 minutes), until the deadline.
4. Writes `<codex_home>/<model>-<12 hex of the config's SHA-256>/config.toml` (folders 0700, file 0600, atomic).
5. Starts Ralphex with `CODEX_HOME` set to that home and `<ID>_API_KEY` set to the key. The inherited `CODEX_HOME`,
   `OPENAI_*` and `AZURE_OPENAI_*` are removed. `--task-model` and `--review-model` carry the provider's id and the
   level; a part with no level gets a Ralphex `config` with `codex_reasoning_effort =`, so none is sent.

A provider that cannot take the build by its deadline (every slot taken, or it kept refusing) fails it before anything
started (`OPEN_MODEL_ENGINE_UNAVAILABLE: <provider>/<model>: …`), or, with `on_unavailable: fallback`, starts it on
the fallback. A missing or refused key and an unknown model never fall back. Once Ralphex runs, the engine never
changes; a provider that fails mid-build fails the build, after Codex's own retries.

**Evidence.** The run's start (the `EXECUTION_STARTING` transition) records `open_model`: `engine_used` (`provider` or
`fallback`), the provider, base URL, wire API, model name and id, origin, context window, default level, Codex home,
the configuration's SHA-256, the key variable's name, the key file's SHA-256, the slots, and the preflight's status
and tries; for a fallback, why, and what it ran. Never the key, never the key file's path.

## The Codex configuration

For Groq's gpt-oss-120b (exactly what `CodexConfig` writes; tested):

```toml
model = "openai/gpt-oss-120b"
model_provider = "abcp-groq"
model_context_window = 131072
model_auto_compact_token_limit = 104857
model_reasoning_summary = "none"
web_search = "disabled"

[model_providers.abcp-groq]
name = "groq (ABCP open-model engine)"
base_url = "https://api.groq.com/openai/v1"
env_key = "GROQ_API_KEY"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
request_max_retries = 8
stream_max_retries = 8

[model_providers.abcp-groq.capabilities]
remote_compaction = "unsupported"

[shell_environment_policy]
ignore_default_excludes = false
exclude = ["GROQ_API_KEY"]
```

Codex treats a model it does not know with fallback metadata (a 272k context), so the window is stated; compaction is
local; the key's name holds `KEY`, which Codex's default shell policy already keeps from the commands the model runs,
and it is excluded by name too. Ralphex adds its own `-c` flags on top: `codex exec -c features.multi_agent=true -c
agents.reviewer.description="…" --dangerously-bypass-approvals-and-sandbox --sandbox danger-full-access -c
model="openai/gpt-oss-120b" -c model_reasoning_effort=high -c stream_idle_timeout_ms=3600000`, the prompt on stdin.

## The engines answer

`GET /v1/engines` describes an open-model profile in the shape Repo C reads today: `access: "lane"`, `lane` the
provider's id, `lane_model` the provider's id of the task's model, `ready` from the key file with the lanes' reasons
(`lane_no_key` while the file is not there, `lane_unreadable` when it cannot be used). A new `provider` object (Repo C
ignores fields it does not know) gives the id, base URL, wire API, `on_unavailable`, each model's name, id, origin,
context, levels and slots, and, with `check_models`, the provider's own `check` (`ok`, `key_refused`,
`model_missing`, `unavailable`). No key and no path leave the controller.

## What stays the same

- Every existing manifest and profile keeps its digest: `executor.provider` is omitted when absent.
- The controller's own Codex, the lanes, the account lanes, admission and the request's shape.
- V3 governance runs on the controller's own Codex only; it refuses a provider.

## Checks

- `internal/openmodel`: provider validation (the chat wire API refused with its reason, https, ids, origins,
  windows, levels, fallback); the exact `config.toml`; homes per configuration; the environment; the key file's rules
  and that a key never prints; the slots' cap, wait, release and cancellation; `/models` in both answer shapes, with
  the key as a bearer token and no redirect followed; the preflight's backoff and Retry-After; the engines check's
  cache; `Begin` writing the home, and releasing its slot and saying why on every failure.
- `internal/authority`: an open-model executor validated, part of the digest, absent from the canonical form when not
  named, copied deep.
- `internal/run`: a run on its provider (flags, `CODEX_HOME`, the key only in the environment, no inherited OpenAI
  key, the evidence, the slot released, no key in any ledger, evidence, home or repository file); a model at its own
  default level; no level for a model that takes none; the fallback before start, and the failure without one; no
  fallback from a missing key.
- `internal/runadmission`: an open-model profile's choices; a pick pinned by name; the engines answer, its readiness,
  the provider's check, and no key or path in it.

## Rollout

1. Merge; build the binary on the host.
2. The owner places the keys (`runtime/secrets/together-api-key`, `groq-api-key`, 0600, the controller's user).
3. `deploy/open-models/check-open-model-providers.py`, then `make-open-model-profiles.py` (README there).
4. One announced restart with no build running, `profiles.json.open-models` in place. `GET /v1/engines` must show the
   new profiles ready.
5. Repo C registers the engines for administrators' own builds only (design note §8).

# Open-model engines: Codex on Together AI and Groq

Status: **built; on the host for the trial since 2026-11-01** (`local-p02-together`, round 1, through a Python relay on
127.0.0.1:18996). **#183 (2026-11-02) moves the relay into ABCP** with per-model `compat` lists and gives each model a
Codex catalog entry (gpt-oss-120b gets `apply_patch`); not on until the switch-over below (one announced restart with
no build running). **2026-11-07 adds `stop_at_first_call` and `custom_tool_as_function`** for gpt-oss-120b on
Together (below); live since 2026-11-07 (`add6f4e`). **2026-11-07 also adds `drop_client_metadata` and `drop_include`**
for gpt-oss-120b on Groq (below); not live until a Groq profile names them. A manifest without `relay`, `compat` or
`codex_catalog` runs exactly as before. Repo C design note `OPEN_MODEL_ENGINES.md` (§9.1, §9.2).

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
| `relay` | `true`: each build runs through its own loopback relay to `base_url` (below, 2026-11-02) |
| `models[].compat` | the relay's fixes for that model, by name; needs `relay` |
| `models[].codex_catalog` | `{}` or `{"apply_patch": true, "tool_output_tokens": 10000}`: Codex's catalog entry for the model |

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

## 2026-11-02: the relay and Codex's model catalog, in ABCP (#183)

Round 1 (Repo C `OPEN_MODEL_ENGINES.md` §7.5, §9.1) ran through a Python relay between Codex and Together that the
trial added by hand (`runtime/open-models/together-responses-shim-v6.py`, `chat_bridge.py`), and found why gpt-oss-120b
wrote no file. Both fixes are now ABCP's.

### The relay: one per build, inside its `abcp run`

For a provider with `"relay": true`, `openmodel.Begin` starts an HTTP listener on `127.0.0.1:<free port>` in the
build's own `abcp run` process, after the slot and the preflight and before Ralphex starts, and writes that address as
Codex's `base_url` (with the provider's path, `http://127.0.0.1:<port>/v1`). It stops when the build gives its slot
back. Why this and not a long-lived relay in `abcp serve` or a supervised `abcp relay` subcommand:

- **Nothing to supervise or restart.** The relay exists exactly while its build does; a reboot, an `abcp serve`
  restart or a crash leaves nothing behind and nothing to bring back. It is ABCP's own code in a process ABCP already
  runs, so it is as reboot-safe as the build.
- **The fixes are the build's own authority.** They come from the manifest the build runs (part of its digest), not
  from whatever profiles a server loaded; a fixed port shared across builds would need a second source of truth.
- **No port to choose or collide on.** The kernel picks a free one; only loopback is bound.

The relay forwards to the provider's `base_url` and nowhere else: a path outside it (or one that climbs out with
`..`), a method other than GET and POST, a `POST …/responses` for any model but the build's, a body that is not a JSON
object, and a redirect (never followed) are refused with a JSON error of its own (`abcp_relay_*`). It never reads or
adds a key: Codex's `Authorization` goes on as it came. Answers stream back as they arrive, byte for byte, except a
chat-bridged one. It asks for no compression and drops hop-by-hop headers, as the Python relay did.

**Its log.** One line per request to `<home>/relay.log` (0600, never through a link; moved to `relay.log.1` at 4 MiB):
the time, the run, the provider and model name, the method and path (no query), the model id (or `-`), the status,
the fixes it applied with counts, the time it took, and for a request it refused or could not carry a fixed word
(`error=model_refused`, `upstream_unreachable`, `redirect_refused`, `client_gone`…). Never a header, a key or a body,
not even a provider's error text. For example:

```text
2026-11-02T09:00:00Z abcp open-model relay run=<run> together/nemotron-3.5-super POST /v1/responses model=nvidia/NVIDIA-Nemotron-3.5-Super-VL status=200 fixes=drop_null_reasoning_content:1,message_before_calls:1,output_after_call:2 duration=1.204s
```

**Its home.** The `config.toml` names the build's port, so a relay build's Codex home is its slot's:
`<codex_home>/<model>-<12 hex>-<slot>`, the hex naming the configuration with the relay's address and the catalog's
path left out (plus the catalog itself). Builds that overlap hold different slots and never rewrite each other's file;
the next build in a slot keeps the home (Codex's homes hold about 130 MB each, so a home per build was not an option).
A provider without `relay` and without `codex_catalog` keeps #181's home names exactly.

**Validation.** `relay` needs `wire_api` `responses`; `compat` needs `relay`; an unknown or repeated fix is refused,
naming the known ones; the chat bridge is refused with either reordering fix (it keeps a turn's calls together
itself), and with `apply_patch` (it carries function tools only). There is no upstream field: the relay's one outbound
host is the provider's own.

### The fixes (`compat`), from round 1's probes

Each was proved against Together with the exact request Codex sent (HTTP 400) and the fixed one (HTTP 200); the unit
tests rebuild those requests and a stand-in that refuses what Together refused, with the probe logs' words.

| Fix | What it changes | Round 1's evidence |
|---|---|---|
| `drop_null_reasoning_content` | removes `"content": null` from reasoning items in `input` | Together refuses its own item back: "data did not match any variant of untagged enum ResponseInput" (gpt-oss-120b's first build) |
| `message_before_calls` | in one assistant turn: reasoning, then the message(s), then the call(s) | nemotron, muse: "invalid_tool_messages: incomplete parallel tool-call group" as sent, 200 reordered; `parallel_tool_calls: false` still 400 (probe-nemotron.log, probe-muse.log) |
| `output_after_call` | each call's output straight after its call (left alone when it cannot be placed exactly) | nemotron's two parallel calls: reordered still 400, with each output after its call 200 (probe-nemotron2.log) |
| `drop_namespace_tools` | leaves out `namespace` tools (Codex's multi_agent_v1) | Bonsai: "tools[4].type: unknown variant `namespace`" (probe-bonsai.log) |
| `chat_completions_bridge` | sends the request to `/chat/completions` (not streamed) with each turn's reasoning as `reasoning_content`, and answers Codex with the Responses events it reads (created, in progress, each item added and done, the message's text as one delta, completed with the usage and its cached tokens); a refusal goes back as the provider gave it | Beam: "The assistant turn at messages[5] is missing its reasoning" on /responses; Chat Completions with `reasoning_content` 200 (probe-beam*.log); the accepted Beam build ran on this bridge |
| `drop_medium_effort` | leaves out an empty `reasoning: {}` or a `"medium"` effort | DeepSeek: `reasoning_effort must be low, high, xhigh, max …; got "medium"` for `{}` (Together fills in medium) and for medium (probe-deepseek*.log) |
| `custom_tool_as_function` | offers each `custom` tool (`apply_patch`) as a function taking one string `input`, replays earlier custom items as function items; on the streamed answer, the model's call of that function goes back to Codex as a `custom_tool_call` | (2026-11-07, below) Together drops custom tools from what gpt-oss sees and answers 200; the model called a function `apply_patch` with arguments of its own making |
| `stop_at_first_call` | on the streamed answer: ends it at its first call (at the next item, an error or the stream's end), with a `response.completed` of the relay's, and closes the provider's stream | (2026-11-07, below) Together does not stop gpt-oss at `<\|call\|>`: "Parse error: Failed to process token …", or invented calls and answers |

Codex's freeform `apply_patch` replays as `custom_tool_call` items; the two reordering fixes treat them as calls.
A fix rewrites only what it names: the other keys, their order and every number go on byte for byte, and a request no
fix changes is sent exactly as it came. They apply in the order of the table above (the bridge last).

`deploy/open-models/make-open-model-profiles.py` writes the round-1 lists: nemotron-3.5-super, muse-spark-open and
glm-5.3 the first three; gpt-oss-120b those and `stop_at_first_call`, `custom_tool_as_function` (2026-11-07); bonsai-27b
the first three and `drop_namespace_tools`; beam-1.0 the bridge alone; deepseek-v4.1-flash the first three and
`drop_medium_effort`.

### Codex's model catalog (`codex_catalog`)

Codex 0.163.0 has no metadata for these ids. On its fallback metadata its base prompt tells the model to edit with an
`apply_patch` tool it does not offer (gpt-oss-120b answered "unsupported call" for 38 minutes), tool output is cut at
10,000 bytes (the plan was cut), and `spawn_agent` lists OpenAI's models. `model_catalog_json`, a top-level
`config.toml` key, names a JSON file `{"models": [entry]}` that takes the place of Codex's bundled catalog. ABCP writes
`<home>/model-catalog.json` (0600) before `config.toml`, and the config names it.

What an entry needs, found by running Codex 0.163.0 (the npm package `@openai/codex@0.163.0-linux-x64`, the same
binary as the host's by SHA-256) against a loopback stand-in: it refuses an entry without `supported_reasoning_levels`,
`shell_type`, `visibility`, `supported_in_api`, `priority`, `support_verbosity`, `truncation_policy`,
`experimental_supported_tools`, or without `base_instructions` (or `model_messages.instructions_template`); it ignores
keys it does not know; `apply_patch_tool_type` takes only `freeform`; `truncation_policy.mode` is `bytes` or `tokens`;
`shell_type` one of `default`, `local`, `shell_command`, `unified_exec`, `disabled`. ABCP's entry:

| Key | Value |
|---|---|
| `slug`, `display_name` | the provider's model id |
| `base_instructions` | the prompt Codex sends on fallback metadata, byte for byte (`internal/openmodel/codex_base_instructions.md`, captured from its request; Apache-2.0, openai/codex) |
| `supported_reasoning_levels`, `default_reasoning_level` | the model's `efforts` and `default_effort` (null for none, so a model without levels still sends `reasoning: {}`) |
| `apply_patch_tool_type` | `freeform`, only with `"apply_patch": true` |
| `truncation_policy` | `{"mode": "tokens", "limit": 10000}` (Codex's own models' limit; `tool_output_tokens` 1,000–100,000, at most a quarter of the window) |
| `supports_search_tool`, `support_verbosity`, `supports_reasoning_summaries` | `false`: a search tool would add `tool_search`, which providers refuse, and fold the multi-agent tools behind it; verbosity would add `text.verbosity` |
| `context_window`, `max_context_window`, `auto_compact_token_limit` | the model's window and compaction point, as `config.toml` states them |
| `shell_type`, `visibility`, `supported_in_api`, `priority`, `experimental_supported_tools`, `default_reasoning_summary`, `description` | `default`, `list`, `true`, `0`, `[]`, `none`, `<provider>/<name> (ABCP open-model engine)` |

**How it was checked.** With Ralphex's own flags (`features.multi_agent`, the reviewer, the effort, the stream timeout),
the first request on the entry differs from the fallback's only in the added `custom` `apply_patch` tool (with its lark
grammar) and in `spawn_agent` naming the build's model alone: same prompt, tools, reasoning, `include` and parallel
calls. A `seq 1 40000` reached the model at 40,213 bytes instead of 10,213. A scripted `apply_patch` call wrote its file
("Success. Updated the following files: A hello.txt"); on fallback metadata the same call answers "unsupported custom
tool call: apply_patch". `TestCodexBuildsThroughTheRelayWithItsCatalogEntry` and `TestCodexBuildsThroughTheChatBridge`
repeat this end to end, real Codex through the relay to a stand-in provider, when `ABCP_TEST_CODEX` names a Codex
binary (skipped otherwise; run them on the host after any Codex upgrade).

**`apply_patch` only where it is known to work.** Together took Codex's `custom` tool type for gpt-oss-120b
(probe-custom-tool.log: 200 with and without the grammar), so the script sets it for gpt-oss-120b alone; Bonsai's
endpoint lists no custom type and the chat bridge carries function tools only. Every round-1 model gets an entry for
the tool-output limit and the sub-agent list. **Answered 2026-11-07 (below):** Together takes the tool but leaves it out
of what gpt-oss sees, so the model calls a function `apply_patch`; `custom_tool_as_function` is that relay fix.

### Evidence

`EXECUTION_STARTING`'s `open_model` adds `slot`, `codex_catalog` (`sha256`, `apply_patch`, `tool_output_tokens`) and
`relay` (`address`, `upstream`, `compat`, `log`). Never the key.

### Checks (#183)

- `internal/openmodel`: each fix from the probes' request shapes, 400 from Together's stand-in as sent and 200 fixed;
  the nemotron2 case (reordered alone still refused); fixes that change nothing leave the bytes exact, and one that
  changes something keeps every other key, its order and every number; the effort fix's cases; outputs that cannot be
  placed exactly are left alone; `apply_patch` turns count as calls; compat validation. The chat bridge: Beam's probe
  request to the exact Chat Completions request; how items fold into turns; the Responses events, their order,
  sequence numbers, items and usage. The relay end to end against a stand-in: a refused turn reaches the provider
  fixed with Codex's key and headers, the stream comes back byte for byte and before the provider finishes, GET and
  refusals (with Retry-After) pass through, every refusal of its own, no redirect followed, the bridge end to end, a
  closed relay gone, and a log that holds no key, header, body or query. The catalog entry. A relay build's home, files
  and modes, its relay serving until the slot comes back, the next build in the slot, homes per slot and per
  configuration, #181's homes unchanged, the log's rotation and refusal of a link.
- `internal/authority`: `relay`, `compat` and `codex_catalog` in the digest when named, absent from the canonical form
  otherwise (every existing profile keeps its digest), copied deep, and refused when invalid.
- `internal/run`: a build through its own relay (Ralphex's `config.toml` names it and the catalog; a request through it
  reaches the provider fixed with the key; the start records the relay; the relay is gone after the run).
- `go build`, `go vet`; `go test ./...` 43 packages pass, the known M02/M03 in `mergelifecycle` as on `main`.

## 2026-11-07: gpt-oss-120b on Together (`stop_at_first_call`, `custom_tool_as_function`)

Rounds 1 and 2 of the trial: gpt-oss-120b on Together wrote no file (Repo C `OPEN_MODEL_ENGINES.md` §7.5, §7.6). The
Stream A diagnosis of 2026-11-07 (scratch probes under `/tmp/gptoss-diag/` on the host) found two causes, both
Together's, not Codex's or the relay's:

- **Together does not stop gpt-oss at harmony's `<|call|>`.** The model keeps generating after its call with nothing to
  read. Either Together's own parser breaks on the next token (`Parse error: Failed to process token N: Unexpected
  token N while expecting start token 200006`, an `error` event after HTTP 200 with no `response.completed`; HTTP 500
  `harmony_parsing_failed` without streaming and on Chat Completions), or the answer "completes" with what the model
  wrote without the tool's result: more calls (up to 5 in one answer, `parallel_tool_calls: false` ignored), an early
  "done", `<<<RALPHEX:TASK_FAILED>>>`, "Could you paste the contents of plan.md?" (one answer 11,925 output tokens).
  Round 2: 11 of 12 sessions made no call at all.
- **Together drops non-function tools** from the prompt gpt-oss sees (the `custom` `apply_patch`, the `namespace`
  `multi_agent_v1`) and answers 200. The model, told by Codex's base prompt to use `apply_patch`, calls a *function*
  `apply_patch` with `{"patch": …}`, `{"command": …}` or `{"command": ["apply_patch", …]}`; Codex applies only a
  `custom_tool_call`.

How Together streams such an answer: an `output_item.added` for each item as the model starts it, a call's arguments
as `function_call_arguments.delta` events, and every later `.done` held to the very end, where the items no longer
match the deltas (names, arguments and call ids shifted between calls). A stand-in relay that ended each answer at its
first call and offered `apply_patch` as a function made Codex write `hello.txt`, then make two real `apply_patch`
edits (exit 0 both times; 2 of 3 answers had hit the parse error and were saved).

### `stop_at_first_call` (the streamed answer of `POST …/responses`)

- Every event passes to Codex byte for byte as it comes until an item of type `function_call` (or `custom_tool_call`)
  has started. Its `output_item.added` and argument deltas pass too.
- Then the next `output_item.added`, an `error` (or `response.failed`, `response.incomplete`) or the stream's end
  ends the answer at that call. Codex gets the call's `function_call_arguments.done` and `output_item.done`, built
  from the added item and its deltas (complete JSON by then), never from Together's `.done`; an item before the call
  that Together had not ended (a preamble message: its streamed text); then a `response.completed` of the relay's own
  whose output is those items, with the response's id, `created_at` and model, and its usage only when the provider
  gave one. The relay then closes the provider's stream. Nothing after the call reaches Codex.
- An error before a call is complete (no call, or arguments not yet one JSON value) goes to Codex as it came, and so
  does the rest of that stream, so Codex's own retries apply (`stream_max_retries`).
- A provider that does stop at the call is not cut: its `.done` is taken as the call's end (built the same way) and
  its own `response.completed`, with its usage, goes on with the items as Codex got them as its output.
- Relay events are numbered after the provider's last `sequence_number`. Only a `200` `text/event-stream` answer is
  read; any other passes as before.

### `custom_tool_as_function`

- **The request:** each `custom` tool is offered as a `function` with one string parameter `input` (apply_patch: "Edit
  files with a patch. Pass the whole patch, from *** Begin Patch to *** End Patch, as the `input` string.", the words
  with which the stand-in got `{"input": "*** Begin Patch…"}` every time), unless a function of that name is offered
  too; a `tool_choice` naming it names the function. Codex's earlier `custom_tool_call` is replayed as a
  `function_call` with `arguments` `{"input": <its input>}`, and its `custom_tool_call_output` as a
  `function_call_output` (their `id` and `status` left out, as the stand-in sent them). The reorder fixes count them
  as calls as before.
- **The answer:** a `function_call` of such a tool is held (none of its events reach Codex) until it ends, then goes to
  Codex as `output_item.added` and `output_item.done` of a `custom_tool_call` (the call's `id` and `call_id`, its
  name) whose `input` is the arguments' `input`. For the shapes gpt-oss improvised: a `patch` or `command` string, or
  the item of a `command` list that holds the patch (else its last string); for `apply_patch`, the text from `*** Begin
  Patch` to `*** End Patch`. Arguments of no such shape go as the input as they came, so `apply_patch` answers the model
  with its own error. With `stop_at_first_call` the call ends at the cut; alone, at the provider's own `.done`.
- **Codex's catalog entry is unchanged:** `apply_patch_tool_type` stays `freeform` (Codex must offer the tool and
  accept the `custom_tool_call`; `freeform` is the only value Codex takes), and the base prompt stays Codex's own.

The chat bridge makes its own answer from function tools, so it is refused with either fix.

### The log

When a fixed stream's answer was cut, the line says `cut=next_item|error|end`; when it ended without the provider's
usage (Together sends usage only in its last `response.completed`, which a cut or a broken stream never reaches), it
says `usage=lost` with an estimate (about four bytes a token) of the request sent and of the text, arguments and input
the model streamed. Together does not stream gpt-oss's reasoning, so the output estimate is a floor; whether Together
stops billing when the stream closes is not known. For example:

```text
2026-11-07T09:00:00Z abcp open-model relay run=<run> together/gpt-oss-120b POST /v1/responses model=openai/gpt-oss-120b status=200 fixes=drop_null_reasoning_content:1,custom_tool_as_function:4,stop_at_first_call:1 duration=6.412s cut=error usage=lost est_input_tokens=7150 est_output_tokens=38
```

`custom_tool_as_function` counts the tools, tool choice and items it changed in the request and the calls it turned
back in the answer.

### What stays open

- **Usage is lost** on every cut answer: metering undercounts unless it adds the `usage=lost` lines' estimates.
- **The model never gets its reasoning back**: Together streams none, so between calls gpt-oss sees an empty analysis;
  expect weaker multi-step coherence.
- **`spawn_agent` and the other multi-agent tools stay invisible** to gpt-oss (Together drops namespace tools). If
  Ralphex's review needs sub-agents it may fail as Bonsai's did; flattening the namespace would be a third fix.
- Not yet tried: a real Ralphex build. Groq's own serving of gpt-oss-120b may stop at `<|call|>`; no Groq key is on the
  host yet (`runtime/secrets/groq-api-key`), so it is unprobed.

### Checks (2026-11-07)

- `internal/openmodel` (`stream_test.go`, on streams shaped as the diagnosis recorded them): a parse error after a
  complete call ends the answer at the call (events before it byte for byte, the call's end from its deltas, a
  completed without usage, no error); the runaway answer (five calls, a "done", shifted `.done` items, a completed with
  usage) reaches Codex as its first call alone; a preamble message ends with its text; an error before a call is
  complete, and all after it, goes as it came; a provider that stops keeps its usage; a function `apply_patch` in each
  shape (`input`, `patch`, `command`, `command` list, pretty-printed) reaches Codex as its `custom_tool_call`, another
  function as it came; the input from each shape; the custom fix alone ends at the provider's `.done`; answers with
  nothing to fix pass byte for byte (events split over reads, CRLF, a last event without its blank line).
  `compat_test.go`: the request (the tool in its place, the replay, `tool_choice`, a clashing function, Together's
  stand-in takes it); other models' fixes leave custom tools and items exactly as they came; validation.
  `relay_test.go`: end to end, the relay closes the provider's stream at the invented item, the provider gets
  `apply_patch` as a function and the replay, the log line; nemotron's identical answer passes byte for byte.
- `TestCodexBuildsThroughTheRelayWithGptOssOnTogethersFixes` (real Codex, `ABCP_TEST_CODEX`): Codex runs a call cut
  at Together's parse error, applies a function `apply_patch {"command": ["apply_patch", …]}` cut at an invented
  "done" (the file is written), and its replay reaches the provider with no custom tool or item.

### Going live

1. Merge; build the binary on the host; the real-Codex checks there (§4b step 1's command now runs three tests).
2. Stage the live gpt-oss template with the two names (`make-open-model-profiles.py … --providers together`, or edit
   the template's gpt-oss `compat` list), as `.new` beside the live file, with the live one kept.
3. One announced ABCP restart with no build running, the `.new` in place.
4. A one-file check through ABCP's real relay (about US$0.01), then one real build.

## 2026-11-07: gpt-oss-120b on Groq (`drop_client_metadata`, `drop_include`)

The owner said yes (2026-11-07) to one gpt-oss-120b build on Groq, on Groq's paid Developer plan, after a probe showed
Groq stopping gpt-oss cleanly at its call (Repo C `OPEN_MODEL_ENGINES.md` §9.2). Groq's Responses API was then probed
with Codex 0.163.0's own requests, as ABCP's relay sends them (real Codex through the relay to a loopback stand-in that
streams as Groq does, the captured bodies cut to a few tools and short prompts so that each fitted the free plan's 8,000
tokens a minute; `/tmp/groq-stage` on the host), one request a minute:

| Request shape | Groq's answer | Fix |
|---|---|---|
| Codex's tools with the custom `apply_patch` | 400 `invalid JSON body` (no field named) | `custom_tool_as_function` |
| Codex's tools with the `multi_agent_v1` namespace | 400 `invalid JSON body` | `drop_namespace_tools` |
| `client_metadata` (Codex's turn metadata, on every request) | 400 ``unknown field `client_metadata` in request body`` | **`drop_client_metadata`** (new) |
| `include: ["reasoning.encrypted_content"]` | 400 `Field 'include' is not supported` | **`drop_include`** (new) |
| `store: false`, `prompt_cache_key`, `parallel_tool_calls: true`, `reasoning.effort` | 200 | none |
| the replay: a reasoning item with its `reasoning_text` content and `encrypted_content: null`, calls with `id` and `status`, outputs, `apply_patch` as a function call | 200 | none |
| a reasoning item with `content: null`; an assistant message between a call and its output; two calls before both outputs | 200 | none (no turn fix) |

Each 200 answered as harmony expects: the reasoning streamed as `reasoning_text` deltas, one call, `response.completed`
with the usage (cached tokens counted once the prefix repeats), so neither stream fix is needed. Groq validates a call
against the tools it was given: a call of a tool not in the request ends the stream with an `error` and
`response.failed` (`tool_use_failed`), which Codex retries.

- **`drop_client_metadata`** and **`drop_include`** each leave out one top-level key of `POST …/responses`; every other
  key, its order and every number go on byte for byte, and a request without the key is sent exactly as it came. They
  sit after `drop_namespace_tools` in the relay's order and go with any other fix.
- **gpt-oss-120b on Groq** gets `drop_namespace_tools`, `drop_client_metadata`, `drop_include`,
  `custom_tool_as_function` and Codex's catalog entry with `apply_patch` (`make-open-model-profiles.py`). As on
  Together the multi-agent tools stay invisible to it. Codex gives the model its reasoning back between calls, which
  Together never could.
- **Checks:** `compat_test.go` walks Codex's request through Groq's refusals one fix at a time (a stand-in with Groq's
  words, `fakegroq_test.go`) and keeps every other byte; `TestCodexBuildsThroughTheRelayWithGptOssOnGroqsFixes` runs
  the real Codex through the relay to that stand-in, which streams as Groq does: Codex applies `apply_patch` (the file is
  written), and its replay reaches the provider with the reasoning's text and none of what Groq refuses.
- **Going live:** merge; build (the real-Codex run now has five tests); stage the Groq templates and profiles as new
  files; on the Developer plan, the one-file check through the real relay (about US$0.01); one announced restart with no
  build running; one real build.

## Rollout

1. Merge; build the binary on the host.
2. The owner places the keys (`runtime/secrets/together-api-key`, `groq-api-key`, 0600, the controller's user).
3. `deploy/open-models/check-open-model-providers.py`, then `make-open-model-profiles.py` (README there).
4. One announced restart with no build running, `profiles.json.open-models` in place. `GET /v1/engines` must show the
   new profiles ready.
5. Repo C registers the engines for administrators' own builds only (design note §8).
6. (#183) From the trial's Python relay to ABCP's: `deploy/open-models/README.md` §4b (build, the script's new template,
   one announced restart with no build running, then the Python relay on 127.0.0.1:18996 retired).

# Open-model trial engines on the host

Repo C design note `OPEN_MODEL_ENGINES.md`; how ABCP runs them: `docs/plans/open-model-engines.md`. Nothing here is
on until step 4.

## 1. The owner

- **Keys:** `runtime/secrets/together-api-key` and `runtime/secrets/groq-api-key` (under
  `/home/devagent/local-integration/repo-c-abcp/`), each the key alone on one line, owned by `devagent`, mode `0600`.
- **Together, Organization Settings → Privacy:** turn prompt storage **off** (it is on by default and used for
  product improvement); that gives zero data retention and also switches off "passthrough" models, which must stay
  off. Leave training opt-in off (its default). Cap the account at the trial's budget (US$5 for round 1), by its
  spending limit or by loading only that much credit.
- **Groq:** the base limits (8K tokens a minute per model) cannot carry one build. A Groq build waits for the paid
  Developer plan; its limits are on the account's limits page. Turn on zero data retention in Data Controls.

## 2. Check the providers (read-only)

```text
python3 deploy/open-models/check-open-model-providers.py
python3 deploy/open-models/check-open-model-providers.py --all-models            # every id, names only
python3 deploy/open-models/check-open-model-providers.py --together-base https://us-api.together.ai/v1   # US-only processing
python3 deploy/open-models/check-open-model-providers.py --probe-responses --emit ~/open-models/trial-models.json
```

It never prints a key. `--probe-responses` spends a few hundred tokens (well under a cent) to confirm that each
candidate answers the **Responses API** with a function tool, with and without a reasoning level. A provider that
answers 404 there cannot build through Codex (it speaks only Responses to a provider): record it and stop that
provider. Edit the emitted file: keep the ids it confirmed, set `efforts` and `default_effort` to what answered 200
(empty for none), and drop models the trial does not use.

## 3. Make the profiles (dry run first)

```text
python3 deploy/open-models/make-open-model-profiles.py --models ~/open-models/trial-models.json --providers together
python3 deploy/open-models/make-open-model-profiles.py --models ~/open-models/trial-models.json --providers together --write
```

It derives `manifest-template-open-<provider>.json` from the host's own `manifest-template.json` (the pinned Ralphex,
budgets and acceptance stay exactly the host's), with this executor (Together shown, ids as the check confirmed them):

```json
"executor": {
  "executor": "codex",
  "task_model": "gpt-oss-120b",
  "provider": {
    "id": "together",
    "base_url": "https://api.together.xyz/v1",
    "key_file": "/home/devagent/local-integration/repo-c-abcp/runtime/secrets/together-api-key",
    "wire_api": "responses",
    "codex_home": "/home/devagent/.codex/lanes/together",
    "models": [
      {"name": "gpt-oss-120b", "id": "openai/gpt-oss-120b", "origin": "US", "context_window": 131072,
       "efforts": ["low", "medium", "high"], "default_effort": "high", "max_concurrent_builds": 1},
      {"name": "nemotron-3.5-super", "id": "<as listed>", "origin": "US", "context_window": 131072, "max_concurrent_builds": 1},
      {"name": "devstral", "id": "<as listed, if served>", "origin": "FR", "context_window": 131072, "max_concurrent_builds": 1},
      {"name": "glm-5.3", "id": "<as listed>", "origin": "CN", "context_window": 202752, "max_concurrent_builds": 1},
      {"name": "deepseek-v4.1-flash", "id": "<as listed>", "origin": "CN", "context_window": 163840, "max_concurrent_builds": 1}
    ],
    "preflight": true,
    "check_models": true,
    "queue_timeout": "20m",
    "on_unavailable": "wait"
  }
}
```

Groq's is the same with `"id": "groq"`, `"base_url": "https://api.groq.com/openai/v1"`, `groq-api-key`,
`/home/devagent/.codex/lanes/groq` and `gpt-oss-120b` alone. The profile entry added to `profiles.json.open-models`
is a copy of `local-p02` with `"profile_id": "local-p02-together"`, the new template, and
`"executor_choices": {"models": ["gpt-oss-120b", "nemotron-3.5-super", "devstral", "glm-5.3", "deepseek-v4.1-flash"]}`.
`--kinds web,node` adds `local-p02-node-<provider>` from `manifest-template-node.json`.

## 4. Turn it on (an announced restart with no build running)

1. Build the merged ABCP binary.
2. `cp -p profiles.json profiles.json.pre-open-models`, then move `profiles.json.open-models` to `profiles.json`
   (`0600`, one hard link).
3. Restart `abcp serve` (`runtime/start-abcp.sh`). A refused profile stops the start: put the previous file back.
4. `GET /v1/engines`: `local-p02-together` with `access: lane`, `lane: together`, `ready: true` and
   `provider.check.status: ok`.

**Off again:** put `profiles.json.pre-open-models` back and restart with no build running. The templates, homes and
keys can stay; nothing reads them.

## 5. Repo C's live settings (`start-live-repo-c.py`)

```text
abcp.admission.profiles.codex-together=local-p02-together
abcp.admission.profiles.codex-groq=local-p02-groq
product-api.usage.lanes[N].engine=codex-together     # codex-api until Repo C's engine check takes codex-together
product-api.usage.lanes[N].kind=codex
product-api.usage.lanes[N].dir=/home/devagent/.codex/lanes/together
product-api.usage.prices[M].model=openai/gpt-oss-120b
product-api.usage.prices[M].input=0.15
product-api.usage.prices[M].cached-input=0.15        # until the provider lists a cached price
product-api.usage.prices[M].output=0.60
```

One price row per model id the check confirmed, from the provider's list (the US endpoint is 1.25×). Then the trial
build itself: design note §7.

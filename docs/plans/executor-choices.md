# Model and reasoning per build

Status: **live since 2026-10-12 20:47 UTC** (#86, `813bdfe`; ABCP restarted with no build running and the versioned profiles copied, recorded in #87). Repo C design note `AI_KEYS_AND_MODELS.md` §9.5, step 4b.

## Why

Repo C's AI and API keys page shows each engine's model and reasoning since step 4a (`engines-read.md`), but an
administrator can only read them: they are fixed in each profile's manifest template. The owner asked on 2026-10-12:
"if i select claude-api and click "Use engine" what happens? does it let choose model and reasoning?". This lets a
build carry its own model and reasoning level, from a list the operator keeps per profile, and records what it ran
on in the run's pinned manifest.

## What changes

- **The request.** `RunAdmissionRequestV1` may carry `executor { model, effort }`: a model, a reasoning level, or
  both, for the build's task. A model is a lower-case name (letters, digits, `.`, `-`, `_`, at most 64 bytes); a
  level is one of `low`, `medium`, `high`, `xhigh`, `max`. Neither, an unknown field or any other value is
  `400 invalid_request`. Omitted, the request keeps its shape and digest.
- **The profile.** `ProfileV1` may carry `executor_choices { models, efforts }`, the list a request may pick from.
  It is checked when the profile loads: known levels and valid model names, each once, at most 16 of each, not both
  empty, and only `xhigh` for a Codex profile (Codex runs refuse any other level, V3 and the Ralphex command alike).
  A profile without it admits no request that names an executor; omitted, an existing profile keeps its binding
  digest. Adding it to a profile changes that profile's binding digest, as `template_mirror_path` did.
- **Admission.** Before anything is written, a request whose model or level is not on its profile's list is refused,
  `422 executor_choice_not_accepted`, and nothing starts. An accepted choice replaces `executor.task_model` and/or
  `executor.task_effort` in the run's manifest. The review model and level stay the profile's. The manifest is then
  bound and registered as before, so the authority digest covers the choice, and the replay check refuses a manifest
  whose task model or level differs from the request. The same `request_id` with another choice is
  `409 request_id_conflict`.
- **The engines.** `GET /v1/engines` gives each profile's `choices { models, efforts }`, omitted when it offers none,
  so Repo C offers only what the builder admits.
- **The live profiles** (`deploy/local-integration/abcp-config/profiles.json`): every product profile gets a list;
  the development profile none.
  - Codex, on the controller's sign-in or the `codex-api` lane: `gpt-6-astra`, `gpt-5.6-sol`; reasoning `xhigh`
    only. Both have built here: `gpt-5.6-sol` was Codex's default before every profile named `gpt-6-astra`
    (owner, 2026-10-07: "move to astra").
  - Claude, on the controller's sign-in or the `claude-api` lane: `claude-sonnet-5-5`, `claude-opus-5-5`; reasoning
    `medium`, `high`, `xhigh` (owner, wave 1 checkpoint: Sonnet 5.5 by default, Opus 5.5 for complex builds).

  A model the vendor refuses for that account fails the build that chose it, so the operator lists only models that
  have run on that lane.

## What stays the same

- The development admission has no executor; its shape and digest are unchanged.
- Runs read the model and level from the manifest as before (`internal/run` → `--task-model model:effort`); nothing
  in Ralphex changes.
- `GET /v1/capabilities` is unchanged.

## Checks

- `internal/serviceapi`: the request without an executor keeps its shape; known values are accepted and anything
  else refused; strict decoding refuses an empty executor, an unknown field, an unknown level or a bad model; the
  choice reaches admission; `422 executor_choice_not_accepted`.
- `internal/runadmission`: a profile without choices refuses one and starts nothing; a model or level not listed is
  refused; a listed one is pinned as the task's and the review keeps the profile's; the same request replays and the
  same id with another choice conflicts; a build without a choice keeps the profile's; the engines answer each
  profile's choices; a Claude profile may offer levels and a level alone keeps its model; the load checks each rule;
  the choices are part of the profile's binding.

## Rollout

1. Merge, build the binary on the host, and restart ABCP with no build running (the hard rule). Copy the versioned
   `profiles.json` in the same restart and run the drift check.
2. Repo C reads `choices` from `/v1/engines`, lets the administrator pick a model and level per engine, and sends
   `executor` only when the build's profile lists the pick.

## Next

- Repo C: the page's choice, stored per engine, sent with each build.
- 4c: personal Codex and Claude sign-ins as lanes of their own.

## Live check (2026-10-12)

- **The restart:** ABCP started on `813bdfe` at 20:47:45 UTC, with no build running, and listens with its 8 preview
  profiles available. The versioned `profiles.json` was copied in the same restart; the host's previous file, equal to
  `a17fdee`'s, is kept beside it. The drift check says OK for it.
- **The engines:** `GET /v1/engines` answers the 32 profiles, all ready. 31 give `choices`: the 16 Codex ones
  `gpt-6-astra` and `gpt-5.6-sol` at `xhigh`, the 15 Claude ones `claude-sonnet-5-5` and `claude-opus-5-5` at
  `medium`, `high` or `xhigh`. The development profile gives none.

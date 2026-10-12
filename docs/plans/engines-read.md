# The engines, read by Repo C

Status: **live since 2026-10-12 19:59 UTC** (#84, `34b31b7`; ABCP restarted with no build running, recorded in #85). Repo C design note
`AI_KEYS_AND_MODELS.md` §9.5, step 4a.

## Why

Repo C's AI and API keys page lets an administrator choose the engine for new builds. Before this, the page could not
say what each engine runs, nor whether its key or sign-in is in place. Choosing an engine whose lane had no key made
every new build fail at its start. The owner asked on 2026-10-12: "if i select claude-api and click "Use engine" what
happens? does it let choose model and reasoning? also, does it expect to have API key to be present before we could
click "Use engine"."

## What ABCP answers

`GET /v1/engines`, for any authenticated principal: no body, no query. It answers `EnginesV1`, one entry per admission
profile, in profile order:

| Field | What it is |
|---|---|
| `profile_id` | the admission profile |
| `executor` | `codex` or `claude` |
| `task_model`, `task_effort`, `review_model`, `review_effort` | as the profile's manifest names them; omitted when it names none |
| `lane`, `lane_model` | the profile's engine lane, and the model its file names in `ANTHROPIC_MODEL` (only if it looks like a model name); omitted on the controller's own sign-in |
| `access` | `sign-in` (the controller's own Codex or Claude sign-in) or `lane` |
| `ready`, `reason` | whether the credentials are in place; if not, `lane_unreadable`, `lane_no_key` or `not_signed_in` |

**What "ready" means:**
- For a lane, the file loads under the same rules as at admission (owner-only, no symbolic link, only its engine's
  settings). It also holds a key:
  - Codex: `OPENAI_API_KEY`, or a Codex sign-in in the lane's `CODEX_HOME`;
  - Claude: `ANTHROPIC_API_KEY`, or Bedrock's three settings.
- For the controller's own sign-in, the engine's sign-in file is in its folder:
  - Codex: `CODEX_HOME`, else `~/.codex`, holding `auth.json`;
  - Claude: `CLAUDE_CONFIG_DIR`, else `~/.claude`, holding `.credentials.json`.

  A key in the controller's environment counts too.
- "Ready" never means the vendor accepts the key: nothing is sent to a vendor.

Lane files are read again, and sign-in files looked for, on each request, so a changed key shows at once. No value leaves
the controller except a lane's model name.

## What stays the same

- Admission, profiles, manifests and lanes are unchanged; no profile's binding digest moves.
- `GET /v1/capabilities` is unchanged, since Repo C reads it strictly. Repo C learns that ABCP answers engines from the
  route itself: an older ABCP answers it 404.
- Without an admission profile file, the route answers `501 unsupported_capability`.

## Next (Repo C, then 4b and 4c)

- Repo C's page shows each engine's model, reasoning and readiness, and refuses to choose an engine that is not ready.
- 4b: model and reasoning per build, from an allowlist, recorded in the pinned manifest.
- 4c: personal sign-ins as lanes of their own.

## Live check (2026-10-12)

- **The restart:** ABCP started on `34b31b7` at 19:58:51 UTC, with no build running, and listens with its 8 preview
  profiles available.
- **The engines:** `GET /v1/engines` answers the 32 profiles, and every one is ready.
  - The Codex sign-in profiles run `gpt-6-astra` at `xhigh`, for the task and the review.
  - The Claude sign-in profiles run `xhigh`, with no model named.
  - The `claude-api` lane names `claude-sonnet-5-5`, with reviews on `haiku`.
  - The `codex-api` lane names no model, so Codex's own default applies.
- **The rest:** `/v1/capabilities` answers as before.

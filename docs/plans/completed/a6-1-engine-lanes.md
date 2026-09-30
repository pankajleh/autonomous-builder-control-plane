# Engine lanes: one way of paying for a build engine, per profile

Date: 2026-09-30

Exact base: `3242cba` (PR #67)

Repo C design note `docs/product/design-notes/A6_COMMERCIAL_MODEL_ACCESS.md` (A6.1): paid builds run on commercial access,
Claude Code through Amazon Bedrock and Codex through the OpenAI API. Until now the engine got an allowlist of the
controller's own environment (`ralphexEnvironment`), which has none of Claude Code's Bedrock settings and is the same for
every profile, so a subscription build and an API build could not run side by side.

## What changes

- **The manifest.** `executor.lane` names a lane and `executor.lane_file` its file, both or neither: a short lower-case
  name, and an absolute, clean path ending in `.env`. Both are omitted when unset, so every existing template keeps its
  canonical form and digest. The lane is part of the authority, so a run's lane is known from its authority's SHA-256.
- **The lane file** (`internal/enginelane`). `KEY=VALUE` lines; blank lines and `#` comments are skipped.
  - Read only when it is the operator's private file: opened one path part at a time without following links, a
    regular file owned by the controller's user, mode 0600 or 0400, at most 16 KiB.
  - Each key must be on its engine's list and given once, with a value:
    - Codex: `CODEX_HOME`, `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_ORG_ID`, `OPENAI_PROJECT_ID`;
    - Claude: `CLAUDE_CONFIG_DIR`, `CLAUDE_CODE_USE_BEDROCK`, `AWS_REGION`, `AWS_ACCESS_KEY_ID`,
      `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `ANTHROPIC_MODEL`, `ANTHROPIC_SMALL_FAST_MODEL`,
      `ANTHROPIC_DEFAULT_HAIKU_MODEL`.
  - Anything else refuses the lane. A refusal names the line and the key, never a value.
- **At load.** A profile whose template names a lane is refused when its lane file cannot be read as above.
- **At each build's start.** The file is read again, before the engine starts. A changed, removed or opened-up file stops
  the build (`FAILED` from authority validation). The lane's settings replace the inherited ones of the same names for
  that build only; the rest of the allowlisted environment is unchanged.
- **Evidence.** The start transition records `engine_lane`, `engine_lane_sha256` (of the file's bytes) and
  `engine_lane_keys` (names only). No value is written to the ledger, the evidence or an error.

## What does not change

- A template without a lane: the same environment, allowlist and `ralphex-env-v2` policy as before.
- V3 governance's own rule that its executor is Codex at `xhigh`.
- Nothing is loaded live by this change. Adding a lane is: the operator writes the lane file (0600), a template names it,
  and one announced restart loads the profile. Repo C A6.3 does that with the owner's yes, since API builds cost money.

## Tests

- `internal/enginelane`:
  - a Codex lane and a Claude (Bedrock) lane read with their SHA-256;
  - the environment merge;
  - refusals: format, unknown and cross-engine keys, duplicates, empty values, NUL, size, group- or world-readable
    files, a linked file, a linked folder, a folder, a missing file, bad names, paths and executors;
  - no refusal names a value.
- `internal/authority`: lane and file together or not at all; the lane changes the authority's digest, and a manifest
  without one keeps its canonical form.
- `internal/run`:
  - the engine sees the lane's key and home in place of the inherited ones, and the ledger records the lane's name,
    SHA-256 and key names but not the key;
  - a lane file opened up after admission stops the run before the engine starts.
- `internal/runadmission`: a profile loads with a private lane file, and is refused with a readable one or a key its
  engine does not take.
- `go test ./...`: all pass but `internal/mergelifecycle`'s two known failures, which fail the same on main.

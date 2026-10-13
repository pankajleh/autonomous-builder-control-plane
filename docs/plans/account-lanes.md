# Account lanes: an administrator's own Codex or Claude account

Status: **live since 2026-10-13 07:02 UTC** (#88, `ab633e7`; host lanes, templates and files in place, ABCP restarted with no build running; recorded in #89). Repo C design note `AI_KEYS_AND_MODELS.md` §9.5, step 4c.

## Why

The owner asked on 2026-10-11 for the AI page to "allow personal codex/cluade accounts", and on 2026-10-12 how Codex and
Claude Code sign in when the builder is not on the VPS. On 2026-10-13 they said "u can do 4c", after being told that a
personal plan is meant for its holder's own use: an account lane serves administrators' own builds, and customers' builds
stay on the server's sign-in or an API key (Repo C decides which builds use it).

## What changes

- **Account lanes.** An account lane is an engine lane (A6) the operator makes once for an administrator's own account:
  its lane file, its own private home and the build profiles that name it. `abcp serve --account-lanes-file` lists them
  (`account-lanes.json`: lane, executor, lane file, home; at most 8). Each is checked at start:
  - the home is a folder of the controller's user, mode 0700, no symbolic link;
  - the lane file is the operator's private file and gives only `CODEX_HOME` (Codex) or `CLAUDE_CONFIG_DIR` (Claude)
    pointing at that home, and for Claude perhaps its token;
  - no name, file or home twice.
- **Signing in.**
  - **Codex:** ABCP runs `codex login --device-auth` (from its own `PATH`) in its own process group, with `CODEX_HOME`
    set to the lane's home and no key or other sign-in in its environment. It reads Codex's link and one-time code
    (only an `openai.com` or `chatgpt.com` link) and answers them; the administrator opens the link on their own device
    and enters the code. Codex keeps the sign-in in the home's `auth.json`. The code lasts 15 minutes; the process is
    stopped then, on sign-out or when ABCP stops.
  - **Claude:** the administrator makes a token with `claude setup-token` on their own computer. ABCP takes it only in
    the shape `sk-ant-oatNN-…` and writes it into the lane file as `CLAUDE_CODE_OAUTH_TOKEN` (mode 0600, written beside
    it and renamed over it, so a build starting meanwhile reads the old file or the new one).
  - **Signing out** removes Codex's `auth.json`, or leaves the Claude file with only its home.
- **The routes**, for a service principal granted `accounts.control` (`grants.json`); `501` without the file:
  - `GET /v1/account-lanes`: each lane's `state` (`signed_out`, `signing_in`, `signed_in`), `since`, and a pending
    device sign-in's `verification_url`, `user_code` and `expires_at`;
  - `POST /v1/account-lanes/{lane}/sign-in` `{"token": …}` (Claude) or `{}` (Codex); asking again while a Codex
    sign-in waits shows the same code; a signed-in Codex lane answers `409 account_lane_signed_in`;
  - `DELETE /v1/account-lanes/{lane}`;
  - unknown lane `404 account_lane_unknown`, a wrong body `400`, trouble `503 account_lane_unavailable`.
  No answer carries a token, and request logs carry only the path.
- **Lanes and readiness.** A Claude lane may give `CLAUDE_CODE_OAUTH_TOKEN`; Ralphex passes it to Claude Code (it
  strips only `ANTHROPIC_API_KEY`). `GET /v1/engines` counts it as the lane's key, as it counts a Codex `auth.json` in
  the lane's home.
- **Profiles.** `MaxProfiles` goes from 32 to 64. The live profiles gain four: `local-p02-codex-account`,
  `local-p02-node-codex-account`, `local-p02-claude-account`, `local-p02-node-claude-account`, each as its sign-in or
  lane twin was (mirror, choices), on a manifest template naming the account lane.

## What stays the same

- Every other profile, lane and route. A build reads its lane when it starts, as before.
- `GET /v1/capabilities` is unchanged; Repo C learns the routes exist from the routes themselves.

## Checks

- `internal/accountlane`: a Claude token is kept, refused when it is not one, never answered, and removed on sign-out
  with nothing temporary left; a Codex device sign-in shows its link and code, the same again, signed in once Codex keeps
  its sign-in, refused while signed in, signed out, expired, unavailable; every rule of the file; Codex's own words read
  (and another site's link refused); the real process runs in the lane's home without the controller's key and stops.
- `internal/serviceapi`: the routes, their strict bodies, the error codes, the grant, `501` without the file, `401`
  anonymous.
- `internal/enginelane`, `internal/runadmission`: the token setting, and an account lane ready once it holds one.

## Rollout

1. On the host, in `abcp-config/lanes`: the two homes (0700) and lane files (0600) giving only their home; the four
   manifest templates, copies of their twins naming the lane; the versioned `account-lanes.json`, `grants.json` and
   `profiles.json`.
2. Restart ABCP with no build running, adding `--account-lanes-file …/account-lanes.json`, and run the drift check.
3. Repo C shows the lanes on the AI page, signs them in and out, and offers their engines for administrators' own builds.

## Next

- Repo C's page and routing.

## Live check (2026-10-13)

- **The host:** in `abcp-config/lanes`, the two homes (0700) and lane files (0600, giving only their home); the four
  manifest templates; the versioned `account-lanes.json`, `grants.json` and `profiles.json`. The drift check says OK
  for the three.
- **The restart:** ABCP started on `ab633e7` at 07:02:24 UTC, with no build running, and listens with its 8 preview
  profiles available.
- **The answers:** `GET /v1/engines` answers 36 profiles; the 32 before are ready and the four account ones say
  `lane_no_key` until signed in. `GET /v1/account-lanes` answers both lanes `signed_out`. A token that is not one
  answers 400, an unknown lane 404. Nothing was signed in.

# Account lanes answer one question: an administrator's own app (AE3)

Status: **built, not yet live** (#177). Repo C design note `AI_IN_EVERY_APP.md` §3.4 and §10 (AE3, P0917), on top of
the account lanes (`docs/plans/account-lanes.md`, live since 2026-10-13).

## Why

The owner asked on 2026-10-31 for AI in every app that wants it, and: "for admins, personal codex/claude should be
allowed like we currently do for building apps." Repo C's decision 3 A (its owner's standing answer): an administrator's
app may answer with their own signed-in account **for their own use only**, so only in Try it, a preview opened by the
app's owner from Autobuild's page. An account of one's own is for that person; visitors get Autobuild's key or the
owner's own key. Repo C's relay asks ABCP, which holds the lanes and their tokens.

## What changes

- **The route** `POST /v1/account-lanes/{lane}/ask`, for a service principal granted `accounts.control` (the grant
  Repo C's principal already has); `501` without `--account-lanes-file`, `403` without the grant.
- **The request** is the relay's `{system?, messages, max_tokens?}` with no tools, checked as the relay checks it
  (Repo C `AppAiService.problem`), characters counted as Java counts them (UTF-16 units):
  - strict JSON (no other field, no field twice, no case alias, UTF-8);
  - `system` a string of at most 8,000 characters, no NUL (it is passed as an argument);
  - `messages` 1 to 40 turns of exactly `{role, content}`, `user` or `assistant`, alternating, the first and the last
    the user's;
  - `content` a string of 1 to 20,000 characters, or 1 to 20 parts: `{"type":"text","text"}` (1 to 20,000 characters)
    or, in a user's turn, `{"type":"image","media_type","data"}`: JPEG, PNG or WebP, plain padded base64 with no line
    breaks, at most 300 KB decoded and starting as its type does; at most 3 pictures in the request;
  - `max_tokens` an integer from 1 to 1,024;
  - the body at most 1 MB + 16 KB (this route's own bound; every other route keeps 1 MB). A body whose length is
    declared over it gets the server's `413`; anything else wrong, tools included, `400 account_lane_ask_invalid`.
- **Which lane:** a Claude lane only, signed in (its file holds `CLAUDE_CODE_OAUTH_TOKEN`). A Codex lane answers
  `400 account_lane_ask_unsupported`: Codex's read-only sandbox still reads the whole disk, where the lanes and secrets
  are.
- **What ABCP runs:** `claude` found on the controller's `PATH` at each question (as `codex` is for a device sign-in),
  once:

  ```text
  claude -p --input-format stream-json --output-format stream-json --verbose
         --system-prompt <system, or "" when absent> --tools "" --strict-mcp-config --setting-sources ""
         --disable-slash-commands --no-session-persistence --max-turns 1 --model <model>
  ```

  - in a fresh empty folder made for it (mode 0700, under the controller's temporary folder), removed afterwards;
  - in its own process group, killed when the question's time is up and again once it has exited;
  - with this environment and nothing else: `PATH=<claude's own folder>:/usr/local/bin:/usr/bin:/bin` (its folder
    first, which holds the `node` an npm install runs with), `HOME` the lane's home, the lane file's
    `CLAUDE_CONFIG_DIR` and `CLAUDE_CODE_OAUTH_TOKEN`, and two switches that carry nothing,
    `DISABLE_AUTOUPDATER=1` and `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`, so a question never updates the host's
    Claude Code or sends its telemetry. None of the controller's keys, proxies or other settings;
  - its standard error discarded; its standard output kept up to 1 MB (more is no answer).
- **What it reads:** one stream-json line on standard input, one user message:

  ```json
  {"type":"user","message":{"role":"user","content":[
    {"type":"text","text":"Earlier in this conversation:\nUser: [a picture] What is on this plate?\nAssistant: A thali."},
    {"type":"text","text":"And this one?"},
    {"type":"image","source":{"type":"base64","media_type":"image/png","data":"…"}}]}}
  ```

  The earlier turns become the first text block, a line per turn (`User: …`, `Assistant: …`, a turn's parts joined by
  a space), a picture from an earlier turn written `[a picture]` and not sent. The last user turn's texts and pictures
  follow as content blocks, in their order. A one-turn question has no first block.
- **The answer:** the last `{"type":"result"}` line of its output: `200 {"text", "input_tokens", "output_tokens"}`, the
  text from `result` and the counts from `usage` (the input counting `cache_creation_input_tokens` and
  `cache_read_input_tokens` too, which the API's `input_tokens` leaves out), `null` when absent. A result with
  `is_error`, or a subtype other than `success`, or no result: `503 account_lane_unavailable`.
- **One question at a time per lane:** another waits at most 3 s, then `429 account_lane_busy` (`Retry-After: 1`).
  Each question gets 25 s, its wait included (the relay's 30 s, less the start-up); past it, `504
  account_lane_timeout`. The response may be written past the server's 30 s write timeout (its deadline is moved for
  this route, at most a minute and 15 s).
- **Refusals:** `404 account_lane_unknown`, `409 account_lane_signed_out`, `400 account_lane_ask_unsupported`,
  `400 account_lane_ask_invalid`, `429 account_lane_busy`, `504 account_lane_timeout`, `503 account_lane_unavailable`
  (no `claude` on the `PATH`, a failing run, too much output, ABCP stopping).
- **Settings:** `abcp serve --account-lane-ask-model` (default `sonnet`, a model name or alias) and
  `--account-lane-ask-timeout` (default `25s`, at most `1m`), read only with `--account-lanes-file`.
- **Nothing logged:** nothing of the question, the answer or the token is logged or kept; the request log keeps only
  the method, path, status and time, as for the other account-lane routes.

## Checked against Claude Code

Against Claude Code 2.1.316 here (the host runs 2.1.263), with a cleared environment and an empty home:

- every flag above is accepted; `--setting-sources ""` loads none; `--tools ""` leaves `tools: []` and
  `--strict-mcp-config` `mcp_servers: []`;
- `--system-prompt ""` is used as given, not Claude Code's own prompt (a short question took 425 input tokens), and a
  system prompt starting with `-` is taken as the value;
- the user message above, with a PNG in its `source` block, was answered ("RED", "Red.");
- an error is `{"type":"result","subtype":"success","is_error":true,…}` and exit 1 (an unknown model; a token that is
  not valid, `401 OAuth access token is invalid`); a line that is not a user message prints nothing and exits 1;
- `CLAUDE_CODE_MAX_OUTPUT_TOKENS` set low makes Claude Code ask again several times and then fail, so `max_tokens` is
  checked and not passed on: the question's time and its bounded output limit the answer;
- the fresh folder leaves no project entry in the home's `.claude.json`.

## Different from §10

- `max_tokens` is checked, not applied (above).
- The input tokens include the cached ones.
- The two switches in the environment.
- A body declared over the bound answers the server's `413`, as every route does, not `400`.

## What stays the same

Every other route, the account lanes file and its checks, signing in and out, and `GET /v1/capabilities`. Builds that
use a lane read its file as before.

## Checks

- `internal/serviceapi` (`accountlaneask_test.go`): the answer and its `null` counts; the request log has the path
  and nothing of the question or the answer; every refusal's status and code; `403` without the grant, `501` without
  the file, `401` anonymous; each rule of the body, at and past the relay's limits, tools and tool blocks refused, and a
  body past 1 MB accepted.
- `internal/accountlane` (`ask_linux_test.go`, with a fake `claude` that records its arguments, environment, folder
  and standard input): the arguments; the environment exactly the lane's (the controller's keys, token and proxy set
  and not passed); the standard input of a five-turn conversation with pictures; the folder empty, 0700 and removed;
  the result read, `is_error`, an error subtype, no result, too much output, no `claude`; the refusals before anything
  runs; busy, and a wait that ends in an answer; the timeout killing the process group's child; closing ABCP stopping
  a question; the settings checked.

## Rollout

1. Deploy the binary built from `main` with no build running, as for any ABCP restart. The flags' defaults serve; the
   host's `account-lanes.json` and `grants.json` do not change.
2. The owner signs the Claude lane in on Admin → AI → Your own accounts (`claude setup-token`; both lanes were signed
   out on 2026-10-31 at 11:10 UTC).
3. Live check: one question through the route to the signed-in lane; the Codex lane `400`; a signed-out lane `409`.
4. Repo C's relay asks the lane for Try it (Repo C's half of AE3).

## Next

- Repo C: the AI card's **Use my own account** choice, the relay's use of it in Try it, and the fallback to the own key
  or credits when the lane is signed out, busy or failing.
- A Codex lane, once Codex can run with no tools at all.

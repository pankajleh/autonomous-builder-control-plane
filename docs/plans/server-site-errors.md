# An app on a server: its recent errors, when asked (Repo C BF-07c1)

Status: **built.** Repo C's design note `BF07_WHEN_A_RUNNING_APP_GOES_WRONG.md` §4.3 (the owner, 2026-10-24: "all as
recommended, go ahead", every §7 answer A; the queue: "please continue building as per queue"). The same for a share link
(7c2) waits: a hosted app is started with `docker exec --detach` in a container that runs `/bin/sleep` with
`--log-driver=none`, so its output is not kept anywhere ABCP can read; keeping it, bounded, is designed first.

## Why

Repo C's card says when a running app is unwell (7a), Ask knows how it is doing (7b) and More Changes can fix what is
wrong (7d), but none of them can say *why*: the app's own output, where Node prints the error and its stack, stays on the
server. Repo C may read it only when a person asks, never on a timer, and treats it as untrusted text.

## The read

`GET /v1/targets/{key}/sites/{site}/errors` (no body; through the targets grant; beside operations, since it changes
nothing; `Cache-Control: no-store`) answers `SiteErrorsV1`:

| Field | What |
|---|---|
| `schema_version` | `SiteErrorsV1` |
| `target`, `site`, `read_at` | what was read and when (UTC, RFC 3339) |
| `running` | the app container's state is `running` |
| `restarts` | Docker's restart count for the container |
| `exit_code` | its last exit code, only when it is not running |
| `out_of_memory` | Docker says it was killed for memory |
| `lines` | at most 50 error lines of its own output from the last 24 hours, newest last, at most 16 KB, each at most 400 bytes |
| `truncated` | more lines were found than are answered |

Only a Node app (a site whose database ABCP keeps) on a confirmed target is read: a static site, an unknown site or a bad
name is `site_unknown` (404), a target not confirmed is `target_not_ready` (409) and nothing is signed in to. An app with no
container answers `running: false`, no `exit_code` and no lines.

## On the server

One script: `docker inspect` for the state, restarts, exit code and memory kill (`none 0 0 false` when there is no
container, and then no output is read); then `docker logs --since 24h --tail 2000` of the app container, standard error
with standard output, through `grep` for lines that read as errors (error, exception, fatal, unhandled, uncaught, refused,
denied, failed, crash, panic, timeout, traceback, the `ECONN`/`EADDR`/`ENOENT`/`EACCES` codes, and stack frames), at most
the last 200, each cut at 600 bytes. Apps are now started with `--log-driver json-file --log-opt max-size=1m --log-opt
max-file=2`, so Docker keeps at most 2 MB of an app's output whatever the server's own settings; an app started before
keeps the server's default until its next start.

## Before it leaves ABCP

Control characters are dropped (tabs become spaces), each line is cut to 400 bytes, and `Redact` replaces, in order: a
connection string's password (`scheme://[hidden]@host`), Bearer and Basic credentials (`[token]`), the value of any
key whose name says password, secret, token, API key, auth, cookie, session, signature or private key (`[hidden]`), a JSON
Web Token (`[token]`), keys with a known prefix (`sk_`, `pk_`, `rk_`, `rzp_`, `whsec_`, `ghp_`, `gho_`, `github_pat_`,
`xoxb-` and the like: `[key]`), an email address (`[email]`), 32 or more hex characters and 40 or more base64 characters
mixing digits with both cases (`[secret]`), and a run of ten or more digits with spaces, dots, dashes, brackets or a
leading `+` (`[number]`), dates set aside so a time stamp stays. Then the last 50 lines are kept, and from the newest back
as many as fit in 16 KB.

## Tests

`internal/remotetarget/errors_test.go`: the state line, `none`, an answer without its marker, control characters, at most
50 lines and 16 KB with the newest kept, and every replacement above with what must stay (an error's words, a time stamp,
a stack frame's file, an address and port). `internal/remotetarget/errors_linux_test.go`: on the fake server, a quiet app,
a restarting one whose output mixes ordinary lines, a database password, an email address, a token and a phone number,
only its error lines answered with them replaced, one script that changes nothing, a container gone, and the refusals
(static site, unknown site, bad name, unknown target, unconfirmed target); the app's start carries the log options.
`internal/serviceapi/deployments_test.go`: the route, `no-store`, a body or a wrong method refused, the errors mapped, and
the targets grant required.

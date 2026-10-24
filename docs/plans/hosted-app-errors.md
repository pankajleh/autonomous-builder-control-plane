# A share link's app: its recent errors, kept as it runs (Repo C BF-07c2)

Status: **built**, not live yet. Repo C's design note `BF07_WHEN_A_RUNNING_APP_GOES_WRONG.md` §13 (the owner, 2026-10-24: "all as
recommended, go ahead", every §7 answer A; the queue: "please continue building as per queue"). The same for an app on a
server is live (`docs/plans/server-site-errors.md`, #135).

## Why

A hosted app (a share link) starts its own server with `docker exec --detach` in a container whose main process is
`/bin/sleep`, run with `--log-driver=none`: whatever the server prints, Node's errors and stacks included, goes nowhere.
Repo C's card can say a share link's app is unwell but not why. For an app on a server, Docker keeps its output and ABCP
reads it when asked; here there is nothing to read, so ABCP has to keep it.

## Keeping it

A hosted app's presented service, its own server, now runs **attached**: the same `docker exec` less `--detach`, its
standard output and error both read by ABCP for as long as it runs. Only that one service of a hosted start; previews,
probes and a hosted app's other services (its database) start detached as before. The exec ends when the server does, or
when ABCP stops the generation (it ends the attached command first, then removes the containers as before); a start that
fails after the server began ends it too.

Each line read is looked at once and dropped unless it reads as an error (`applog.ErrorLine`, the same words as on a
server). A kept line has its control characters dropped, is cut to 400 bytes and goes through `applog.Redact` at once, so
secrets are never kept even in memory. A line longer than 4 KB is looked at for its first 4 KB only.

What a key keeps lives in ABCP's memory, per hosting key, across the key's generations (restarts, stops, updates and
restores), from the app's first start after ABCP's own:

| Kept | Bound |
|---|---|
| error lines, newest last, with when | at most 50, from the last 24 hours |
| when the server ended on its own, with its exit code | at most 64, from the last 24 hours |
| when the supervisor started the app again (a generation ended without a stop) | at most 64, from the last 24 hours |

About 20 KB of lines per key at most. A restart of ABCP starts afresh, which `since` says.

## The read

`GET /v1/hosted/{key}/errors` (no body; through the hosting grant, the key's own principal only; `Cache-Control:
no-store`; a write is refused) answers `HostedErrorsV1`:

| Field | What |
|---|---|
| `schema_version` | `HostedErrorsV1` |
| `hosting_key`, `read_at` | what was read and when (UTC, RFC 3339) |
| `since` | when ABCP began keeping this app's output; absent when it has not run since ABCP started |
| `running` | the key is `READY` |
| `restarts` | the supervisor's restarts in the last 24 hours |
| `exit_code` | the last code the server ended with on its own in the last 24 hours, if it did |
| `lines` | the kept lines, at most 50 and 16 KB, from the newest back |
| `truncated` | more lines were kept in the last 24 hours than are answered |

There is no `out_of_memory`: the server is an exec inside the container, which Docker does not report on; a kill by the
memory limit shows as exit code 137. A key that does not exist, or another principal's, is `dependency_not_found` (404). The
answer is checked before it is sent: the key, the stamps, at most 50 lines of at most 400 bytes, none with a line break,
16 KB in all.

## Shared with servers

`internal/applog` holds what servers and share links share: `ErrorLine`, `Printable` and `Redact` (moved from
`internal/remotetarget`, unchanged).

## Tests

`internal/preview/hosted_output_test.go`: only error lines kept, whole across writes, cut, control characters dropped and
private details replaced; a line's end in a later write; at most 50 lines and 16 KB with `truncated`; lines, ends and
restarts forgotten after a day. `internal/preview/hosted_output_linux_test.go`: the app's server runs attached, never also
detached, with its setup file and in its own container, and its error line is kept redacted; an end of its own is heard
with its code; a stop ends the attached server and is not taken for an end; a preview's server never runs attached; a
start that fails after the server began ends it; the key's errors come from the output across generations, count the
restarts, and are read only by the key's principal. `internal/serviceapi/hosting_test.go`: the route, `no-store`, a write
refused, malformed answers never served, an unknown key 404 and the hosting grant required. `internal/applog`: the
replacements, moved with `Redact`.

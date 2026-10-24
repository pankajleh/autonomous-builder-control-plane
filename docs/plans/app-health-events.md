# Apps that stop answering: told once, and once again when back (Repo C BF-07e)

Status: **live** since 16:26:27 UTC on 2026-10-24 (`9049cd7`, #139; records #140). Repo C's design note `BF07_WHEN_A_RUNNING_APP_GOES_WRONG.md` §4.5 (the owner, 2026-10-24:
"all as recommended, go ahead"; §7 question 4, A: an email and a WhatsApp message once when an app stops answering for 10
minutes, and once when it is back). This is ABCP's half: it notices, and keeps events for Repo C to read. Repo C sends the
messages.

## Why

Repo C must never poll every running app for this (its polling budget rule). ABCP already knows how a share link's app is
doing (its supervisor checks health every 5 seconds), but nothing watches an app on a server once it is deployed: its address
is checked only while a deploy or update runs.

## What is watched

| Kind | Looked at | Healthy | Not looked at |
|---|---|---|---|
| `hosted` (a share link) | every 30 seconds, from the keys' own state | `READY` and `HEALTHY` | a key its owner stopped or never started |
| `site` (an app on a server) | every 2 minutes, one HTTPS request per site from the target's own address, four at a time, 20 seconds each | a Node app answers `/health`, a static site `/`, with 200 and a valid certificate for its host | a paused site, a site never deployed, a target not confirmed |

A site being deployed, removed or operated on (a backup, an update) is **busy**: what is known of it stays as it was. A
share link starting again after a failure, or failed for good, is unwell.

## The rule

An app unwell for 10 minutes without a break gets one `down` event, with when it became unwell. The same app healthy again
gets one `up` event. An app its owner paused, stopped or took off is forgotten without an event, so a pause is never told
as an outage and a later start is a fresh start. Each kind is its own sweep: a sweep of servers never forgets a share
link, and a sweep that could not look (targets unreadable, hosted instances unavailable, ABCP stopping) changes nothing.

What each app is known to be (unwell since, down) and the newest 500 events live in one private file,
`<service-root>/appwatch/watch.json` (mode 0600, written whole and synced before it replaces the old one), so a restart of
ABCP neither forgets an app that is down nor tells it twice.

## The read

`GET /v1/app-health/events?after=<seq>&limit=<n>` (the principal that controls hosting, Repo C's; `limit` 1 to 100,
default 100; `Cache-Control: no-store`; a write or any other parameter is refused) answers `AppHealthEventsV1`: the events
after `after`, oldest first, each `{seq, at, kind, hosting_key | target + site, state: down | up, since}`; `last_seq`, the
newest there is; and `truncated`, true when events after the cursor were already dropped. Repo C keeps its cursor and reads
once a minute: one request for every app together.

## Tests

`internal/appwatch/appwatch_test.go`: nothing before 10 minutes, one `down` with its `since`, busy keeps the clock, one `up`,
a stop forgotten without an event; each kind its own sweep, a forgotten down app sends no `up`, the state survives a
restart; paging after a cursor, at most 500 kept with `truncated`; `Run` records what each source sees and nothing for a
source that could not look. `internal/remotetarget/watch_test.go`: one request per running site from the target's address
(`/health` for a Node app, `/` for a static site), busy sites not asked, paused and never-deployed sites and unconfirmed
targets left out, a sweep cut short not taken as complete. `internal/preview/hosted_output_linux_test.go`: the hosted sweep
sees a ready key as healthy, a degraded one as unwell, a stopped one not at all, and answers false once closed.
`internal/serviceapi/hosting_test.go`: the route with its cursor and defaults, `no-store`, bad parameters and writes
refused, the hosting grant required, no watch no events.

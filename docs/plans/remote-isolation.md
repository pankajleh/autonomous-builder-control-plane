# Remote isolation check: two apps on one server kept apart (BF-14 proof P1b)

Repo C's design note `BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md` (§10, P1b: "two apps share the Postgres; each login is refused
on the other's database; one app's crash leaves the other up"), needed before any customer's app goes on our AWS servers
(§11). The apps on a server already have one login, one database and one internal network each (#95), and the kit's
Postgres in server mode lets a login reach only the database of its own name (`host sameuser … scram-sha-256`) with
`CONNECT` revoked from everyone else; the proof is that the server says so when it is tried. ABCP alone holds a target's
SSH key and runs only fixed scripts on it, so the proof is an ABCP operation.

## The check

`POST /v1/targets/{key}/isolation` `{request_id, site, other, crash}` (the targets grant, like every target route) starts
an operation of kind `isolation` on `site`. Both must be Node apps on the target that are live (`site_unknown`,
`site_stopped`); `site` and `other` must differ. The same request answers the same operation; the same request id asking
for another app or crash is refused. Steps:

- **`own_login`**: the site's login, read from its `database.env` on the server, connects over TCP (as an app does) to its
  own database.
- **`other_login`** and **`login_back`**: each app's login is tried on the other's database and must be refused, and
  `has_database_privilege(…, 'CONNECT')` must be false. The detail says how the server refused ("refused by the server's
  access rules").
- **`network`**: from each app's container, the other's is tried by its name and by each of its addresses
  (`busybox wget`, 3 seconds); both must be unreachable.
- With `crash`: **`crash`** kills the site's container (`docker kill --signal KILL`) and says whether it came back by
  itself; **`other_answers`** waits up to 10 seconds for the other's `/health`; **`start_again`** starts the site's
  container and waits for its `/health`. `start_again` always runs once the kill was tried, whatever came between.

The passwords go to the Postgres container on its input and never on a command line or in ABCP's records; nothing else
on the server changes (with `crash`, the site is down for the seconds between the kill and its `/health`).

## Live (#113)

On host 1 since 07:49 UTC on 2026-10-15 (ABCP `9c9eeb8`). Proof P1b passed at 07:50 UTC with the QA account's apps: three
pairs (gVisor and standard runtimes, both ways), every login refused on the other app's database by the server's access
rules with `CONNECT` not granted, no container reaching another, and `p2-kudos` killed while `team-kudos-app` kept
answering, then started again (15 seconds for the whole check).

# A Shopify app's one way out, and its settings

Date: 2026-09-30

Exact base: `75f9180` (PR #66)

Repo C design note `docs/product/design-notes/A5_SHOPIFY_APPS.md` §8 (A5.2), agreed with the owner: a Shopify app
must reach Shopify's Admin API and nothing else, and it needs its Shopify settings when it starts. Repo C's runtime image
for such apps is `autobuild/shopify-app-runtime` (Repo C P0186).

## What changes

- **Profiles.** A hosted profile may name `"egress": ["*.myshopify.com:443"]`, the only entry v1 accepts. One of its
  services may name `"settings"`, each one of `SHOPIFY_API_KEY`, `SHOPIFY_API_SECRET`, `SHOPIFY_APP_URL` and `SCOPES`,
  once. Neither is accepted on a preview profile. Both are omitted when unset, so every other profile keeps its digest.
- **The egress proxy** (`egress.go`). A group whose profile names egress gets an HTTP `CONNECT` proxy on its network's
  gateway, port 3129. Every service starts with `HTTPS_PROXY=http://<gateway>:3129` and `NODE_USE_ENV_PROXY=1`, so
  Node's `fetch`, which Shopify's library uses, goes through it.
  - It accepts only `CONNECT <one label>.myshopify.com:443`. Anything else is refused: another name, port, an address
    instead of a name, a longer name (403); another method (405); a malformed or oversized head (400).
  - It resolves the name itself and connects only to a public address from that answer (not private, loopback,
    link-local, carrier NAT, benchmarking, documentation or NAT64); none reachable is a 502.
  - It holds at most 32 tunnels, ends each after 30 minutes, and on Stop closes every one. It counts what it opened and
    refused; it never sees content, since TLS stays end to end between the app and Shopify.
  - A start that fails after the proxy opened closes it again; Stop closes it with the group.
- **Settings.** A hosted start carries `settings`, exactly the names the profile's settings service lists (else the
  start is ineligible, as is any setting for a profile that takes none). Values are one line of printable UTF-8, 1 to
  1024 bytes, not starting with a space; at most 8.
  - They are written to the key's private `settings.env` (0600, `NAME=VALUE` lines, written whole and renamed) before the
    state that starts the instance. Each start replaces it; a start without settings removes it; a purge removes it.
  - The settings service is started with `docker exec --env-file <file>`. Since `env -i` would clear them, its start
    keeps the environment Docker gives the exec instead: the image's own, which `imageSafe` allows only from a short
    list, and the file's; `HOSTNAME` is dropped. Every other service is started with `env -i` as before.
  - Values never appear in a receipt (a start's receipt digests the settings' names only, so a replay with the same
    request ID and other values is still a replay), a state file, the hosted view, a log or a command's arguments.
- **The probe.** For a profile with egress, each service sends `CONNECT example.com:443` to the proxy with busybox
  `printf` and `nc`, and must get `403`. Afterwards the proxy must have refused exactly one request per service and
  opened none. Without the owner's rule for port 3129 the request is dropped and the profile is not proved.

## The owner's rule

In the same `abcp-preview-isolation` unit, inserted after the drop so it sits above it:

```
iptables -I INPUT -s 10.213.0.0/16 -p tcp --dport 3129 -m conntrack --ctstate NEW -j ACCEPT
```

Groups without egress have nothing listening on 3129, and the probe keeps proving every other host port out of reach
(its host listener takes a random port).

## Tests

- `egress_test.go`: the allow-list (one label, port 443, no addresses); public addresses only; a tunnel carries data both
  ways, including bytes sent with the request; every refusal and its status; only allowed names are looked up, in lower
  case, and only public answers dialled, the next one when a dial fails; Close ends open tunnels; the 33rd concurrent
  tunnel is refused. Stable over 50 runs and under `-race`.
- `egress_linux_test.go`: profile validation; request validation (count, names, values); the settings file (0600, exact
  content, the runtime given its path, no value anywhere else the key keeps or the view shows, a replay, a restart of
  the controller, a purge); the exact start arguments with and without settings or a proxy; an egress group runs its
  proxy with the settings file and closes it on Stop and on a failed start (which found a shadowed error that would have
  left the proxy open); the probe proves an egress profile only with the owner's rule.

`go vet ./...`, `go test ./internal/preview ./internal/serviceapi` and the same under `-race` pass. The whole suite's
`internal/mergelifecycle` failures (`TestTask3FinalClosureM02…`, `M03…`) fail the same way on `75f9180` without this
change.

## Rollout

Nothing changes until a profile names egress or settings. Then:
1. build from the merge commit, announce on Repo C's board #93, and restart; every existing profile is proved as before;
2. the owner adds the rule above;
3. Repo C adds `shopify-app-hosted-v1` (A5.2c), which the probe proves only with the rule.

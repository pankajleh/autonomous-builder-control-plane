# Remote names: whether a site's address points at a target, and checking a site again (BF-14.3a)

Status: **built in #101**; not live yet. Repo C design note `BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md` §11 (BF-14.3a, P0489),
under the owner's "go with your recommendation options" (2026-10-13), for the owner's "Customer buys their own domain
anywhere". Builds on `docs/plans/remote-sites.md`.

## What changes

- **`GET /v1/targets/{key}/names/{host}`** answers a `NameV1`: the target's address, the addresses the host has now (A and
  AAAA, deduplicated, IPv4 first, at most eight), `points` when one of them is the target's, and `checked_at`. One lookup
  through ABCP's resolver with a five-second limit: no SSH, nothing recorded, the target's state does not matter. The host
  follows the plan's rules (a lowercase public DNS name, no address), otherwise `400`; a host that does not resolve is an
  answer with no addresses, not an error.
- **`POST /v1/targets/{key}/sites/{name}/check`** `{request_id}`: an operation (kind `check_site`, one step `check`) that
  runs the site's check once more, with no SSH session: the host must point at the target, then the site must answer over
  HTTPS with its release (a static site's `index.html`, a Node app's `/health`) within the check window, as at the end of
  a deploy. The site's last check is recorded either way. When it answers, a `failed` site whose release is still the one
  checked becomes `live`; a `live` site stays `live`; when it does not answer, the site stays as it was. Only `live` and
  `failed` sites are checked: a stopped app is `409 site_stopped`, a site left `unknown` is `400` (the drift check
  reconciles it), a site being changed is `409 target_busy`; the same request answers the same operation.

ABCP writes nobody's DNS.

## Tests

`internal/remotetarget/names_linux_test.go`: a name that points at the target, one that points elsewhere, one that points
at both (IPv4 first, no duplicates) and one that does not resolve, without a session and without recording anything;
hosts refused by the plan's rules and an unknown target; a site whose name did not point when it was deployed is checked
again, stays `failed` while the name points elsewhere, and becomes `live` once it points, without opening a session;
the same request is one check; a live site checked again stays live; an unknown site, a malformed site name or request id,
and a request id another operation used are refused; a stopped Node app is refused, a started one is checked on
`/health`, and a site left unknown is refused. `internal/serviceapi/deployments_test.go` gains both routes and their
malformed forms.

## Going live

Restart ABCP on the merged commit with no build running (same flags), and check that both routes answer `404` for an
unknown target. Repo C's side (**Check the name** and **Check again** on the Servers page) follows.

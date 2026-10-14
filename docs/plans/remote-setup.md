# Remote setup: the first administrator of a Node app on a target (BF-14.2c)

Status: **live since 2026-10-14 04:51 UTC on `1d24614`** (#103, recorded in #104); the owner's Node app on host 1 has its token. Repo C design note `BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md`
§11 (BF-14.2c, P0494), under the owner's "go with your recommendation options" (2026-10-13). Builds on
`docs/plans/remote-sites.md` (Node apps) and `docs/plans/remote-lifecycle.md`.

## Why

An app built with the template library's accounts block has no administrator until someone makes the first one with the
app's private setup token, which the app reads from `BOOTSTRAP_TOKEN`. A preview gets the sample token (#81); an app on a
target got none, so nobody could sign in to it unless it allowed sign-up.

## What changes

- **Each Node app's own setup token:** 32 lowercase hex characters (128 bits from `crypto/rand`), made the first time the
  app starts and kept with the app's database record in the target's record (mode 0600). The sites list never shows it.
- **Every start sends it** (a deploy, a switch back after a failed update, a restore, **Start**): the start script reads it
  on its input, refuses anything that is not such a token, writes `~/autobuild/apps/<site>/setup.env`
  (`BOOTSTRAP_TOKEN=…`, mode 0600) beside `database.env`, and passes both to the app's container with `--env-file`. No
  command line and no script holds it.
- **It lasts while the app's data lasts:** an update, a stop and a start keep it; removing the app's data removes it with
  the database (and `setup.env`), so an app put back on a fresh database gets a new one.
- **`GET /v1/targets/{key}/sites/{name}/setup`** answers `SiteSetupV1` (`target`, `site`, `host`, `token`) to the service
  principal holding `targets.control`. A static site is `400`, a site not on the target (or being taken off) `404`, and
  an app that has not started since ABCP keeps tokens `409 setup_pending`: starting it again makes one.

## Tests

`internal/remotetarget/setup_linux_test.go`: a Node app's first deploy writes `setup.env` with its token (mode 0600) and
passes it to the container; neither the docker command lines nor any script holds it; an update, a stop and a start keep
it; a site taken off answers `404`; removing its data removes `setup.env`, and the app put back gets a new token. A static
site, an unknown site, a malformed name and an unknown target are refused; an app whose record has no token answers
`setup_pending` until it starts again. The start script refuses an empty, short, uppercase or shell-laden token.
`internal/serviceapi/deployments_test.go` gains the route, its malformed forms and `setup_pending`.

## Going live

Restart ABCP on the merged commit with no build running (same flags), and check that the route answers `404` for an
unknown target. An app on a target already gets its token at its next start. Repo C's side (**Set up as administrator** on
the Servers page) follows.

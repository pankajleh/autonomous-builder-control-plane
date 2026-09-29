# Preview profiles may pass an app's own cookies (Repo C A4.4)

Status: implemented in this PR. Requested by Repo C's approved design note
`docs/product/design-notes/A4_4_APP_SIGN_IN_IN_PREVIEWS.md` (owner approval 2026-09-29).

## Why

Repo C's web apps with a server and database (A4) sign people in with their own session cookie. The presentation
proxy removed every `Cookie` and `Set-Cookie`, so signing in inside a preview never stuck. That rule was right while all
previews shared one web origin; with Repo C's per-app hosts (B2, live 2026-09-29) each preview has its own host.

## Change

- `PreviewProfileV1` gains `app_cookies` (bool, `omitempty`). Profiles without it keep their JSON and digest.
- With it, `present()` passes the request's `Cookie` minus any cookie named `__Host-preview*` or `__Host-unlock*`
  (the gateway's own), and the response's `Set-Cookie` headers except those names. `Authorization`,
  `Proxy-Authorization`, `Forwarded` and `X-Forwarded-*` are still removed. Without it, nothing changes.
- The isolation probe, limits and everything else about the runtime are unchanged.

## Tests

- `TestControllerProxyPassesOnlyAnAppsOwnCookiesWhenTheProfileOptsIn`: both ways with and without the flag; the
  gateway's cookies and credentials never reach the app.
- `TestAppCookiesIsOptInAndLeavesOtherProfilesDigestsAlone`.
- The existing `TestControllerProxyBindsLoopbackAndOnlyDialsPresentedService` keeps proving the default.

## Rollout

Build the controller from this merge, and replace the live controller `feda373` with one announced restart, together with
`node-pg-v1` gaining `"app_cookies": true`. The live controller `feda373` would reject a profile file naming the field,
so the file changes only with the new binary.

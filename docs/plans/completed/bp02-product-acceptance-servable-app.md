# BP-02: product acceptance checks for a servable app; previews last an hour

Date: 2026-09-28

Exact base: `2bd85f7c308f7a37546e68037c76855977b03a24`

Found while building real products end to end, which the user asked for on 2026-09-28 ("finish the products until they are served").

## Observation

Run `admission-44e95f10…` (the user's background remover) finished implementation and review with five checkpoints, then failed at branch acceptance.

The local-integration product acceptance command was still the P1 closure-proof check: it passed only if a changed file contained the literal line `P1-CLOSURE-VERIFICATION-OK`. Every real product build therefore failed at its last step.

That build also showed what a real check must catch. It was specced before Repo C P0064 made the AI kickoff platform-aware, and it produced a Vite, React and TypeScript project whose `index.html` loads `/src/main.tsx`. A preview serves the repository as static files and cannot run that.

## Change

**`product-verification` acceptance** (`deploy/local-integration/abcp-config/manifest-template.json`) now checks that the candidate is a servable static app:

- the build changed at least one file, and `git diff --check` is clean;
- `index.html` is at the repository root, and `README.md` (the preview health path) is kept;
- every `<script src>` and `<link rel=stylesheet href>` in every page is:
  - not loaded from another site (the preview gateway's content security policy allows only the app's own files);
  - a file that exists in the repository;
  - a plain `.js`, `.mjs` or `.css` file, which a browser runs without a build step.

Failures print one plain `ACCEPTANCE_FAILED:` reason. The check was exercised on seven candidates: no change, a good app, a TSX entry, a CDN script, a missing file, a relative reference with a query string, and a removed README.

**Preview lifetime.** The `demo-v1` preview profile (repository `example/product`) keeps a preview for 3600 s instead of 300 s, the preview contract's maximum. The customer page tells people their app link lasts about an hour.

## Deploy

1. Copy both files to the host `abcp-config/`, in place.
2. Restart ABCP with no run executing; profiles are read at start-up.

Admissions made before the restart keep the manifest they were admitted with.

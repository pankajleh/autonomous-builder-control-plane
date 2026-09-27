# BP-01 provider replay semantic correction

Date: 2026-09-27

Exact base: `34dec9ee8d725304c0c56aa7c53118113759711c`

## Problem

A fresh PX-07 proof after the first replay-continuity correction exposed two remaining provider-detail false positives:

1. Ralphex live tailing can emit a `section` event with the label only in `text`, while historical replay also fills the provider-only `section` field with the same label. BP-01 bound that duplicate field into `SourceDigest`, so a semantically identical replay was reported as an integrity failure.
2. A newly admitted run can briefly exist before its Ralphex session appears in the sidecar. An immediate activity subscriber therefore produced a durable `Implementation detail unavailable` UNKNOWN before the provider session was observable, poisoning later preview eligibility even though the run was healthy.

## Bounded correction

- Canonicalize the redundant provider-only `section` field away for section events before computing provider source identity; normalized `text` remains the projected and identity-bound section meaning.
- Preserve compatibility with already-persisted BP-01 live section digests while making historical replay stable; a real text change still changes `SourceDigest` and `ActivityID`.
- Retry initial provider unavailability for a bounded six-attempt startup window only while no verified provider proof exists.
- After a provider proof exists, provider loss remains immediately fail-closed.
- Once the startup retry budget is exhausted, unavailability remains WARNING / UNKNOWN.
- Integrity failures are never suppressed.
- Preserve the existing exact event-1 replay correction, real later gap detection, terminal-cleanup handling, preview trust rules, run-state, acceptance, admission, merge and authority semantics.

## Acceptance

```text
go test ./internal/activity ./internal/preview ./internal/serviceapi ./cmd/abcp
go test -race ./internal/activity ./internal/preview ./internal/serviceapi ./cmd/abcp
go vet ./internal/activity ./internal/preview ./internal/serviceapi ./cmd/abcp
go test ./...
git diff --check
```

# CI Evidence Ingestion Contract

Status: accepted EP-005 implementation contract.

## Purpose

`internal/cilifecycle` collects and retains bounded GitHub CI observations for one exact governed head SHA. Its `STABLE` outcome means only that two complete normalized observations matched while three sampled reads of the governed head ref remained equal to that SHA during the recorded interval.

`STABLE` is neutral evidence. Empty, queued, pending, failed, cancelled, neutral, stale, mixed, or successful provider states can all be stable. This package does not decide whether required checks exist, whether a conclusion is acceptable, whether evidence is fresh enough to merge, or whether a merge is authorized.

## V1 evidence

The v1 contract separately preserves check suites, check runs, and every bounded legacy commit-status history item. Numeric provider IDs remain identities and deterministic sort keys only. Provider node IDs, app IDs, suite relationships, exact-case names and contexts, raw states and conclusions, exact head SHAs, and provider timestamps remain distinct fields. No latest-status selection, case folding, synthetic identity, policy evaluation, or provider chronology inference is allowed.

Constructors validate the closed representability tables, canonical UTC RFC3339Nano timestamps, exact SHA binding, unique numeric and nonempty node identities, and run-to-suite/app relationships. They sort collections by numeric provider ID, deep-copy aggregate inputs, and return immutable values. Fixed-order v1 wire structs produce canonical JSON. Retained readers reject unknown fields, trailing JSON, noncanonical bytes, unsupported versions or kinds, and any value that cannot be reconstructed through the public constructors.

## Bounds and identity

Production collection policy is controller owned and is covered in full by `ProductionLimitsSHA256`. The package admits at most 256 suites, 256 runs, 256 statuses, 30 request provenance records, 8 KiB per provenance record, 320 KiB outside the semantic sweeps, and 8 MiB for a complete bundle. Object caps are 1,846 bytes per suite, 3,431 bytes per run, and 9,728 bytes per legacy status. One semantic sweep is capped at 3,842,529 bytes; the calculated maximum bundle profile is 8,012,738 bytes.

All size checks use the production Go JSON encoder. The maximum profile accounts for JSON's sixfold escaping of `<`, `>`, and `&`, plus quote, backslash, U+2028, and U+2029 escaping. Constructors reject over-limit aggregates before evidence publication.

Semantic sweep digests cover every normalized semantic field. Request and response envelope digests are retained in ordered provenance separately from request IDs and transport timestamps. Stable collection identity binds repository, exact head SHA, normalized semantic digest, and the ordered request/response digest chain. Collection time, response-observation time, and provider-state time are separate ranges.

## Ownership boundary

This package owns read-only evidence integrity, bounded observational stability, immutable publication, and replay. A later governed merge-approval and authorization track owns required-check policy, accepted conclusions, freshness decisions, expected-base protection, merge method selection, writes, domain transitions, and post-merge acceptance. CI ingestion must not add those meanings to its schema or API.

The frozen `internal/githublifecycle` schemas and authority canonical bytes remain unchanged. Rollback stops invoking CI ingestion; it never rewrites or deletes published evidence, reservations, or ledger events. Future evidence schema versions add readers and must not reinterpret v1 bytes.

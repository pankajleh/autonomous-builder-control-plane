# Run Evidence Retention Policy

## Purpose

ABCP must preserve enough durable evidence to reconstruct authority and acceptance without allowing Ubuntu hot storage to grow without bound.

This document defines policy only. Automatic archive/prune workers remain a later production-hardening capability; no silent deletion is authorized by this document alone.

## Storage classes

- **Hot** — recent evidence on the execution host for fast inspection.
- **Warm** — verified compressed/object-storage archive after the hot window.
- **Cold** — long-term minimal governance/audit records; bulky logs may expire.
- **Hold/Permanent** — explicitly retained runs such as security incidents, release evidence, or human-designated investigations.

Default durations are configuration, not hard-coded architecture. A production policy may begin with approximately 30 days hot and 180 days warm, then refine by risk/cost.

## Never-delete-first invariant

Local evidence may be pruned only after:

1. archive publication succeeds when archival is required;
2. archive manifest and artifact hashes are verified;
3. durable archive URI/location is recorded;
4. the ledger records archive completion;
5. the retention policy confirms the run is not on hold;
6. deletion/pruning is itself recorded as an event.

Archive or verification failure must preserve the local evidence and surface a retention failure.## Long-lived minimal record

Even after bulky artifacts expire, retain the compact governance record for the configured long-term period, normally indefinitely for project history:

- run/attempt identity and timestamps;
- authority/context hashes;
- state-transition ledger or durable projection;
- candidate/base/merge Git identities where applicable;
- acceptance summary;
- artifact hash manifest;
- archive location and retention/deletion events.

Bulky stdout/stderr, temporary snapshots, and intermediate command artifacts may expire according to policy unless placed on hold.

## Retention metadata

Each governed run should eventually carry `retention_class`, `created_at`, `archive_after`, `delete_after`, `hold`, and `policy_version`. Retention decisions must be attributable and auditable.

## Host footprint

Evidence stored on disk primarily consumes filesystem capacity, not persistent process RAM. The system must continue to bound live subprocess output and must expose storage usage/retention status before automatic pruning is enabled.

## Future enforcement boundary

Phase 6 production hardening may add archive/object-storage integration and a scheduled janitor. That worker must be policy-driven, idempotent, hash-verifying, fail-closed, and unable to delete held or unverified evidence.
# ADR-0003: Event Ledger Is Authoritative; Dashboard Is a Projection

**Status:** Accepted

## Decision

Persist control-plane events in an append-only ledger and derive current status from those events.

Ralphex dashboard status, progress filenames, notification state, and agent prose are evidence inputs but not authority.

## Rationale

The audit observed stale/incorrect dashboard final states, reused session representations, and no cross-plan integration state.

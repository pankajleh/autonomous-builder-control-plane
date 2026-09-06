# ADR-0004: Cross-Model Review Is Risk-Triggered, Not Default

**Status:** Accepted

## Context

EXP-09A and EXP-09B each used an identical controlled candidate for same-model and cross-model review. Both directions tied at 3/3 seeded defects fixed.

## Decision

Default to native Ralphex same-model multi-agent review plus deterministic controller-run acceptance.

Invoke an independent second model only when policy/risk requires it.

## Rationale

Cross-model review showed qualitative diversity but no measured defect-count uplift on the controlled specimens. Deterministic acceptance therefore has higher default value per unit of cost and latency.

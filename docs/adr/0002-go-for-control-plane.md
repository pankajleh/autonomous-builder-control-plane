# ADR-0002: Use Go for the Initial Control Plane

**Status:** Accepted for initial implementation

## Decision

Use Go for the control-plane runtime.

## Rationale

The control plane is process-, signal-, filesystem-, Git- and concurrency-heavy. Go provides a small deployment footprint and strong standard-library support for those responsibilities. It also aligns operationally with the Ubuntu/Ralphex environment while remaining a separate codebase.

## Constraint

Do not import Ralphex internals as a library in the foundation. Use a subprocess adapter so the audited binary remains the behavioral boundary.

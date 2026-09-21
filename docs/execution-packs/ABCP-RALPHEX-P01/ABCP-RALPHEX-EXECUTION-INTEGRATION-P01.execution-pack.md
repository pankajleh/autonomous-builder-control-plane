# ABCP-RALPHEX-EXECUTION-INTEGRATION-P01

## Objective
Use the existing Repo B execution path with the approved Ralphex runtime.

## In scope
- Replace local /usr/bin/true execution profile stub.
- Controller-owned Ralphex binary pinning.
- One generated executable ### Task 1 plan.
- Existing internal/run → internal/ralphex path.
- Candidate branch and evidence validation.

## Out of scope
- Repo C changes.
- Public admission API changes.
- Provider redesign.
- V3 capability work.
- Upstream Ralphex changes.
- Governance redesign.
- Cleanup/refactors.

## Acceptance
ABCP admission → abcp run/controller → internal/run → internal/ralphex → real Ralphex → candidate branch → evidence → existing acceptance.

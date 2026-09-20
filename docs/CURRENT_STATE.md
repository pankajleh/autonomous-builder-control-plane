# Current Project State

Date: 2026-09-20

Operational role: current checkpoint and authority projection for Repo B / ABCP. Immutable execution-pack/evidence artifacts and Git/GitHub objects remain authoritative if this projection conflicts with them.

## Repository checkpoint

- Repository: `pankajleh/autonomous-builder-control-plane`.
- Remote `main`: `3d6a841e722c9c67c83d835a52178b09ca16776d` (PR #22 merge).
- Current retained platform boundary: **EP-006 service/API baseline plus the bounded Repo C product run-admission extension**.
- Post-EP-006 A/B/C assurance expansion remains discarded and is not part of current runtime authority.
- Repo C is the product/UX authority; ABCP owns execution admission, authoritative run lifecycle/projections/evidence/actions, and provider coordination after admission.
- Repo A / Dev-Agent and Ralphex remain execution/provider mechanisms beneath ABCP; Repo C does not call them directly.

## Rebaseline and P01 chain

| Checkpoint | Exact identity | Result |
|---|---|---|
| EP-006 retained baseline | retained EP-006 commit `f1f4af3c8783d870dcab85a1f86756e6ed4efcee` | service/API projections/actions retained; later assurance expansion removed |
| Repo B rebaseline publication | PR #21 head `4040744d9db924ce4e174f7281fdc9f58db7818f`; merge `821e0476600c9299652b88a016c24ba471bf8a6b` | `main` re-established on the retained EP-006 tree |
| P01 authority base | `4d45202f5b411d9c91caf5fef906d1eff9b26e4b` | frozen Repo C↔ABCP admission architecture, execution pack, review contract and evidence discipline |
| P01 final tested executable | `38eda0c3ce489dbe5e51ad297ac3374d98fadcd7` | product-facing admission + replay/reconciliation hardening; all required gates passed, PostgreSQL integration skipped only because the authorized DSN was unavailable |
| P01 publication | PR #22 head `f1ae22fc3132513bbe5c411058e506676d9fb069`; merge `3d6a841e722c9c67c83d835a52178b09ca16776d` | product run admission is integrated on `main` |

## Current product-facing service boundary

ABCP now provides the retained EP-006 read/action surfaces plus bounded product run admission:

- authenticated capability discovery;
- run list/detail, events, timeline and evidence;
- evidence download;
- cancel;
- human-decision recording;
- action status;
- `POST /v1/runs` when admission is configured.

The admission contract accepts only the frozen schema-v1 product-safe request, resolves controller-owned private execution configuration, binds one stable request identity to one durable ABCP run identity, and fails closed on conflicting replay, repository-base mismatch or unresolved reconciliation.

`run_admission` is independent of `retry`, `resume` and `recovery`; those capabilities remain false unless separately designed and authorized.

## Deliberate limits

Current `main` does **not** claim:

- Repo C UI completion;
- Repo C adapter/runtime deployment completion;
- human-decision continuation/resume;
- general retry/recovery;
- provider-selection redesign;
- live production/AWS deployment;
- restored Assurance Capsule / post-EP-006 A-B-C methodology.

The pre-merge evidence document for P01 intentionally records the human merge gate as not yet done at evidence freeze time. Git/GitHub merge records above are the authoritative post-publication result.

## Next bounded integration boundary

Repo C P02 consumes the merged P01 admission contract through its own adapter/correlation layer. Any further ABCP capability is added only when a concrete Repo C product workflow proves it is required and the corresponding design authority is explicitly frozen. Review/discovery alone does not create new scope.

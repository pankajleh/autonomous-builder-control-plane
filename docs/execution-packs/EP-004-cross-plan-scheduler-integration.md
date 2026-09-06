# EP-004 — Cross-plan Scheduler and Integration

**Status:** Authorized for implementation
**Roadmap phase:** Phase 3 — Cross-plan scheduler and integration
**Base:** `db56b1f8cf32561be6b707db4bbf046f4c24e067`
**Parent goal:** Move from independently accepted branches to governed cross-plan scheduling and integration without granting GitHub merge authority.

## Why this EP exists

EP-003 completed governed recovery/blocker control. Phase 3 is the first point where multiple accepted candidate branches may be reasoned about together. The control plane must preserve exact provenance, deterministic ordering, fail-closed conflict handling, and the existing state-machine boundary before any candidate can become `READY_FOR_MERGE`.

## In-scope roadmap deliverables

- accepted-candidate queue;
- final-diff risk analysis;
- disposable integration workspaces;
- textual conflict capture;
- combined acceptance;
- semantic conflict classification;
- `READY_FOR_MERGE` gate.

## Out of scope

Do not add GitHub PR creation/CI/merge automation, service APIs, dashboard UI, deployment, production acceptance, worker leasing, multi-host scheduling, or automatic evidence retention deletion.

## Controlled parallelism

Phase 3 will use serial contract freeze followed by bounded parallel fan-out. No two parallel tasks may own the same authoritative contract. Shared models are frozen before fan-out; implementation tracks use isolated branches/worktrees and disjoint package ownership; final-diff analysis and combined acceptance are required before serial integration authority can advance state.
## Phase 3 execution decomposition

1. **Foundation (serial):** accepted-candidate queue + final-diff risk model + frozen shared contracts.
2. **Integration workspace track:** disposable integration workspace + textual conflict evidence.
3. **Combined acceptance track:** controller-owned combined acceptance + semantic-conflict classification.
4. **Gate assembly (serial):** integrate accepted tracks and implement the `READY_FOR_MERGE` transition gate.

Tracks 2 and 3 may run in parallel only after the foundation contracts are accepted and their package/file ownership is proven disjoint. Gate assembly remains serial authority.

## Foundation acceptance for this plan

The first bounded plan under EP-004 implements only roadmap bullets 1 and 2. It must:

- represent only controller-accepted candidate branches with exact run/attempt/branch/head/acceptance provenance;
- provide deterministic queue ordering and duplicate/replay protection;
- analyze the committed final diff of accepted candidates without modifying target branches;
- classify path overlap and contract-sensitive risk deterministically;
- emit evidence suitable for later integration selection;
- freeze a small shared contract that later parallel tracks can consume without editing it;
- stop at `BRANCH_ACCEPTED`/integration selection authority and never emit `READY_FOR_MERGE` itself.

## Safety invariants

- Ralphex completion is never candidate acceptance or merge authority.
- `IMPLEMENTATION_COMPLETED → READY_FOR_MERGE` remains forbidden.
- Queue/risk decisions must be reproducible from exact Git SHAs and evidence.
- No plan may silently depend on chat history; EP-004 tasks use verified context capsules.
- Unknown/ambiguous Git state fails closed rather than guessing.
- Structured argv only; no secret values in ledger or evidence payloads.
- Go standard library only unless separately authorized.
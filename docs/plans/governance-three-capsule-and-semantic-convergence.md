# Governance hardening — three-capsule lineage and semantic convergence

Status: DESIGN CANDIDATE. This document is governed by Capsule A and must receive independent exact-head design review before implementation.

## Problem

File/path bounds, iteration counts, and wall-clock time are necessary but insufficient. A reviewer can still widen a correction semantically by discovering new root-cause families, fixing them, increasing the diff, then reviewing the enlarged diff again. That creates a self-feeding review loop while remaining mechanically "bounded".

Chat history and operator wrappers are not authority. The product must fail closed when capsule lineage, phase use, semantic blocker scope, mutation scope, exact candidate identity, or execution ceilings are violated.

## Decision: context-capsule-v3

`context-capsule-v1` and `context-capsule-v2` remain parseable historical evidence. New governed autonomous execution moves to `context-capsule-v3`. V3 keeps the existing hashed-source/base model and adds a mandatory `phase_authority` object.

The only phase stages are:

- `A_DESIGN`: one immutable capsule shared by design and design review.
- `B_IMPLEMENTATION`: one immutable child of A shared by implementation and Ralphex review/fix.
- `C_ACCEPTANCE_MERGE`: one immutable child of B shared by deterministic acceptance, final read-only exact-head review, PR publication/merge authorization, and merge.

A capsule is an authority envelope rooted at its start/base SHA. Candidate HEAD changes inside an allowed mutable phase do not rewrite the capsule. Exact candidate HEAD is a separate checkpoint input and must descend from the capsule base.

## V3 phase_authority

V3 adds the following canonical data:

- `stage`: exactly A, B, or C.
- `allowed_operations`: exact closed operation set for that stage.
- `parent`: absent for A; mandatory for B/C and binds parent capsule file SHA-256, parent internal capsule SHA-256, parent stage, and the exact accepted predecessor HEAD.
- `observation_scope`: sorted unique semantic IDs the reviewer may inspect/report.
- `blocking_scope`: sorted unique semantic IDs allowed to block the current authority; must be a subset of observation scope.
- `mutation_scope`: sorted unique semantic IDs allowed to justify code/document mutation; must be a subset of blocking scope.
- `authorized_findings`: sorted unique defect/root-cause IDs. New independent IDs cannot become blockers or mutation authority inside the same capsule.
- `authorized_invariants`: sorted unique invariant IDs. A finding with a new textual description may block only when it maps to one of these already-authorized invariants and does not create a new root-cause family.
- `allowed_paths`: repository-relative exact paths or `dir/**` prefixes that may change under a mutable operation. No regex is accepted.
- `execution_bounds`: mandatory for B, forbidden for C, optional/non-Ralphex for A. B binds max Ralphex iterations, per-session timeout, idle timeout, whole-run timeout, and `finalize=false`.

Canonical arrays are sorted and duplicate-free. Unknown fields, malformed IDs, absolute/traversal paths, broad `**`, zero/unbounded ceilings, and contradictory stage settings fail closed.

## Fixed operation sets

A permits exactly `design-planning` and `design-review`. Design review is read-only; only `design-planning` may mutate A `allowed_paths`.

B permits exactly `implementation` and `implementation-review`. Both use the same capsule. Review/fix mutation is permitted only inside B mutation scope and allowed paths.

C permits exactly `acceptance`, `final-review`, and `merge-authorization`. All repository operations under C are read-only. Merge is a separately governed remote effect and can occur only for C's accepted/reviewed exact candidate.

Ralphex is never a design authority. Governed Ralphex full/tasks-only/review invocation requires B.

## Derivation

`ValidateDerivation(parent, child)` is mandatory before child authority is accepted.

A has no parent.

B must name A's exact file SHA-256 and internal capsule SHA-256 and set `parent.accepted_head` to the independently accepted design HEAD. B base SHA must equal that accepted design HEAD. B observation/blocking/mutation scopes and allowed paths may refine but never contradict A's declared design authority/non-goals.

C must name B's exact file SHA-256/internal digest and exact converged implementation HEAD. C base SHA equals that HEAD. C has no repository mutation paths. Because B already binds A, C transitively proves A -> B -> C.

A child with a missing/wrong parent, wrong accepted predecessor HEAD, skipped stage, or mismatched base fails closed.

## Semantic black-hole breaker

The product distinguishes `OBSERVE`, `BLOCK`, and `MUTATE` permission.

A reviewer may observe/report outside blocker scope only as a deferred observation. It may not classify that observation as a current blocker and it may not mutate for it.

For B correction/review operations, the authorized finding/root-invariant set can only stay equal or shrink. It may never grow. A newly discovered independent root-cause family produces `SCOPE_EXPANSION_REQUIRED`; it is recorded and deferred to a new bounded authority. It does not become current mutation authority.

`DESIGN_GAP` is similarly terminal for the current B authority: implementation cannot amend architecture to solve it.

A review/fix iteration must declare its blocker IDs and mutation-justification IDs in a canonical `ReviewScopeReportV1`. `ValidateReviewScopeReport` rejects:

- a blocker that is neither an authorized finding nor mapped to an authorized invariant;
- mutation justified by a non-blocking/deferred finding;
- a new root-cause ID promoted into the active set;
- active finding-set growth from the prior validated report;
- a report bound to the wrong capsule or candidate HEAD;
- mutation outside allowed paths.

A valid report can therefore discover arbitrary observations without expanding authority.

## Exact-head checkpoints

Sharing a capsule does not weaken exact-head validation.

- A design review binds the exact design candidate HEAD separately.
- B review/fix starts from B base and every report binds the candidate HEAD it reviewed. Resulting commits must be descendants of B base and within allowed paths.
- C is minted only at the converged B HEAD.
- Acceptance and final review both bind C plus the same exact candidate HEAD.
- Final review is read-only. Any repository mutation invalidates the C checkpoint and requires return to B; after convergence a new C' is minted.
- PR head, accepted head, final-reviewed head, merge-authorized head, and the C candidate must be byte-identical Git object IDs. Drift fails closed.

## Ralphex execution ceilings

B carries mandatory bounded execution settings. Product validation rejects missing or weaker runtime bounds. Initial production ceiling profile:

- max Ralphex iterations: 10 or lower;
- per executor session: 90 minutes or lower;
- idle timeout: 45 minutes or lower;
- whole Ralphex invocation: 3 hours or lower;
- finalize: false;
- exactly one incomplete executable Task/Iteration section;
- Codex task/review effort: xhigh.

The adapter must emit these controller-owned values as structured Ralphex argv. Repository/local config cannot widen them.

These are safety ceilings, not target durations. Semantic convergence policy remains mandatory even when all time ceilings are present.

## Product validation surface

Implement in the ABCP product, not only shell wrappers:

1. V3 build/parse/verify and backward-readable V1/V2.
2. `ValidateCapsuleUsage` for stage/operation/read-only/current-head rules.
3. `ValidateDerivation` for A -> B -> C cryptographic lineage.
4. `ValidateCandidatePaths` for exact/prefix path authority.
5. `ValidateReviewScopeReport` for observe/block/mutate separation and monotonic active finding set.
6. Ralphex authority/command validation for B and all execution ceilings.
7. CLI validation commands sufficient for deterministic acceptance and operator diagnostics.

The controller must call the library validators before execution/effect. CLI commands expose the same library logic; they are not a weaker alternate policy.

## Failure classes

Use deterministic fail-closed classifications in validation results/errors:

- `CAPSULE_STAGE_INVALID`
- `CAPSULE_LINEAGE_INVALID`
- `CAPSULE_USAGE_INVALID`
- `CANDIDATE_HEAD_INVALID`
- `MUTATION_SCOPE_VIOLATION`
- `SCOPE_EXPANSION_REQUIRED`
- `DESIGN_GAP`
- `EXECUTION_BOUNDS_INVALID`
- `FINAL_REVIEW_INVALIDATED`

No one of these implicitly authorizes retry, design amendment, scope widening, PR publication, or merge.

## Fresh-chat bootstrap contract

A fresh ChatGPT session is not trusted authority. It must read the canonical architecture entry point and the ABCP governance policy, then verify live exact Git/ABCP state before proposing or authorizing work.

The cross-repository bootstrap must name, at minimum:

1. `pankajleh/dev_agent_automation_platform/docs/architecture/autonomous-builder/README.md`
2. `pankajleh/dev_agent_automation_platform/docs/architecture/autonomous-builder/Autonomous_Software_Builder_Architecture_and_Current_State.md`
3. `pankajleh/autonomous-builder-control-plane/docs/architecture/AUTONOMOUS_EXECUTION_GOVERNANCE.md`
4. `pankajleh/autonomous-builder-control-plane/docs/architecture/CONTEXT_AUTHORITY_AND_CAPSULE_POLICY.md`
5. the active execution pack/plan and the active A/B/C capsule evidence identified by live ABCP state.

The README/current-state docs must link the product governance document explicitly. Durable Git authority outranks memory or a pasted prior-chat summary.

## Compatibility and migration

V1/V2 parse/verify behavior remains unchanged for historical evidence. They cannot satisfy V3-only lineage/semantic-scope validators. Existing completed runs are not retroactively upgraded.

New autonomous work after this governance change must use V3. If an in-flight legacy V2 run already began before activation, its evidence remains historical and may finish under the authority with which it started, but it cannot mint a V3 child without an explicit migration/derivation checkpoint.

## Bounded implementation task

### Task 1: Implement three-capsule governance validation foundation

- [ ] Add `context-capsule-v3` schema, canonical validation, phase stages, parent binding, semantic scopes, allowed paths, and execution bounds while preserving V1/V2 evidence readability.
- [ ] Add deterministic usage, A->B->C derivation, candidate-path, and review-scope-report validators with adversarial tests.
- [ ] Make new governed Ralphex execution require a B V3 capsule and controller-owned iteration/session/idle/wall-clock/finalize bounds; emit supported Ralphex argv structurally.
- [ ] Add `AUTONOMOUS_EXECUTION_GOVERNANCE.md` and supersede contradictory v2-era prose in the context/adapter contracts.
- [ ] Add CLI diagnostics/validation entry points backed by the same library validators.
- [ ] Run full Go tests, race tests for changed packages, vet, gofmt, git diff --check, and scope audit.

Non-goals: EP-005 merge Task-1 behavior, live GitHub provider implementation, NUC runtime activation, automatic PR merge, and rewriting historical capsule artifacts.

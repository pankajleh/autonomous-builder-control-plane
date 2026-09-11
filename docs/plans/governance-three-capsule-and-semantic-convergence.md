# Governance hardening — three-capsule lineage and semantic convergence

Status: DESIGN CORRECTED under Capsule A. Implementation is forbidden until an exact-head re-review returns Critical 0 / Major 0.

## 1. Problem and non-negotiable objective

Mechanical bounds alone do not make autonomous work semantically bounded. A reviewer that can discover a new root-cause family, turn it into a blocker, mutate for it, enlarge the diff, then review that enlarged surface can form a self-feeding loop while every path/time/iteration check still passes.

Chat history, operator memory, Desktop Commander, shell wrappers, and reviewer prose are not authority. ABCP must make the workflow fail closed from durable repository/controller evidence.

The governed development workflow uses exactly three immutable context authority envelopes:

`A_DESIGN -> B_IMPLEMENTATION -> C_ACCEPTANCE_MERGE`.

A is shared by design and design review. B is shared by implementation and its bounded Ralphex review/fix cycle. C is shared by deterministic acceptance, final read-only exact-head review, PR publication, merge authorization, merge, and post-merge acceptance evidence. Exact candidate HEAD checkpoints remain separate from the immutable capsule.

## 2. Versioning and canonical wire constants

`context-capsule-v1` and `context-capsule-v2` keep their existing parse/verify semantics for historical evidence. This change introduces `context-capsule-v3`; old bytes/hashes never acquire new meaning.

Canonical V3 stage wire values are exactly:

- `A_DESIGN`
- `B_IMPLEMENTATION`
- `C_ACCEPTANCE_MERGE`

Canonical V3 operation values used by this workflow are exactly:

- `design-planning`
- `design-review`
- `implementation`
- `implementation-review`
- `acceptance`
- `final-review`
- `pr-publication`
- `merge-authorization`
- `post-merge-acceptance`

Golden canonical-JSON and SHA-256 test vectors are mandatory for all three stage shapes. `deployment`, `recovery`, and `maintenance` remain on their current separately governed V2 model until a later explicit migration; V3 activation does not strand them.

## 3. V3 phase authority

V3 retains project/plan/task/repository/base, source hashes, invariants, non-goals, predecessor outcomes, and capsule digest. It adds mandatory `phase_authority`:

- `stage` — one canonical stage above.
- `allowed_operations` — canonical sorted unique exact operation set allowed by the stage.
- `parent` — absent for A; mandatory for B/C.
- `observation_scope_ids` — semantic registry IDs that may be inspected/reported.
- `blocking_scope_ids` — IDs allowed to block; exact subset of observation scope.
- `mutation_scope_ids` — IDs allowed to justify repository mutation; exact subset of blocking scope.
- `authorized_finding_ids` — exact known finding IDs when the B review profile is correction mode.
- `authorized_invariant_ids` — exact immutable reviewed invariant/obligation IDs.
- `allowed_paths` — exact repository-relative files or `dir/**` prefixes; absolute paths, traversal, globstar-only/broad-root patterns, regex, symlink-derived paths, and duplicates are invalid.
- `review_profile` — `NONE`, `INITIAL_IMPLEMENTATION`, or `CORRECTION` as stage rules require.
- `execution_bounds` — mandatory for B; absent for C; A does not authorize Ralphex.

All arrays are canonical sorted/unique. Every identifier has conservative length/character bounds.

### Fixed operation sets

A permits exactly `{design-planning, design-review}`. `design-review` is read-only. Design-planning may mutate only A's design-document `allowed_paths`.

B permits exactly `{implementation, implementation-review}`. Ralphex full/tasks-only/review is authorized only by B. Implementation-review mutation requires a controller mutation lease described below.

C permits exactly `{acceptance, final-review, pr-publication, merge-authorization, post-merge-acceptance}`. Repository content is read-only throughout C. Provider/GitHub effects remain separately bounded by the existing lifecycle authority; C is an additional mandatory lineage/exact-head gate, never a replacement for provider-specific authorization.

## 4. Immutable semantic registry

A design produces a canonical `SemanticAuthorityRegistryV1` as part of the reviewed design output. It is not reviewer-authored during implementation.

Each entry binds:

- stable `rule_id`;
- `kind` = `INVARIANT`, `KNOWN_FINDING`, `NON_GOAL`, or `SCOPE`;
- canonical obligation/predicate text;
- evidence class required to claim violation/closure;
- permitted correction relation and owning component/path family;
- deterministic validator/test identity when one exists;
- whether a model-only semantic judgment is allowed to report an observation.

The registry bytes/digest are bound by the A design-acceptance checkpoint. B and C refer to registry entries only by IDs plus that immutable digest. A reviewer cannot mint or redefine a rule ID. A claimed finding whose mapping cannot be established under the bound registry is `SCOPE_EXPANSION_REQUIRED`, not current blocker authority.

Model judgment may identify that code violates a pre-existing registry obligation, but it cannot change the obligation, evidence class, correction relation, or owning scope. Deterministic tests/validators remain authoritative where registered.

## 5. Controller checkpoints and grants

Child lineage is not established by a child self-asserting an accepted predecessor SHA. ABCP defines strict-canonical, immutable `PhaseCheckpointV1` records. Every checkpoint binds repository identity, capsule file SHA-256, capsule internal SHA-256, exact candidate commit, operation, verdict, evidence refs/digests, controller policy identity, sequence, predecessor-checkpoint digest, and controller event identity.

Required checkpoint kinds are:

- `DESIGN_ACCEPTED`
- `IMPLEMENTATION_CONVERGED`
- `ACCEPTANCE_PASSED`
- `FINAL_REVIEW_CLEAN`
- `PR_PUBLISHED`
- `MERGE_AUTHORIZED`
- `POST_MERGE_ACCEPTED`

A checkpoint chain is non-forkable: one controller-owned durable tip per governed workflow; the next sequence must name the current exact tip digest. Replay is create-or-verify only; a competing next record is conflict/integrity failure.

### A -> B grant

A is minted before design, so A itself does not pretend to know the final implementation path set. Instead, `DESIGN_ACCEPTED` binds the exact reviewed design HEAD, the semantic registry digest, and a canonical `NextStageGrantV1` for B. That grant defines the maximum B operations, observation/block/mutation IDs, authorized invariants/findings or initial-review profile, allowed implementation paths, and maximum execution ceilings approved by the reviewed design.

B `parent` binds all of:

- A capsule file SHA-256;
- A internal capsule SHA-256;
- `A_DESIGN` stage;
- exact `DESIGN_ACCEPTED` checkpoint digest;
- exact accepted design HEAD;
- exact B grant digest.

B base SHA must equal the accepted design HEAD. Every B field must equal or narrow the B grant by deterministic set inclusion/numeric tightening. The grant also carries mandatory floors: `required_blocking_scope_ids`, `required_invariant_ids`, `required_operations`, and `required_final_review_ids`; a child may not remove or weaken those floors. B must preserve every mandatory A-reviewed obligation needed for implementation correctness. No natural-language “does not contradict” test is used.

### B -> C grant

`IMPLEMENTATION_CONVERGED` similarly binds B, the exact converged implementation HEAD, final validated review-scope chain tip, and a `NextStageGrantV1` for C. That C grant is controller-derived from the B grant plus the exact validated convergence outcome: it may tighten maxima but must carry forward every mandatory final-review/invariant floor inherited from A/B. C parent binds B capsule digests, B checkpoint digest, converged HEAD, and C grant digest. C base SHA equals that converged HEAD and C fields must equal/narrow maxima while containing all mandatory floors. A B->C grant that drops a required A/B obligation fails `CAPSULE_LINEAGE_INVALID`.

This produces a cryptographically and controller-evidence-bound A -> DESIGN_ACCEPTED/grant -> B -> IMPLEMENTATION_CONVERGED/grant -> C chain.

## 6. V3 source and candidate verification

A/B/C sharing across authorized commits requires V3 verification to be different from V1/V2 checkout-bound verification.

V1/V2 behavior is unchanged: their current verifier continues to require the governed checkout at base and hashes the live source files as historically defined.

V3 source verification is replacement-resistant and reads exact bytes from the immutable Git object tree at `capsule.base_sha`, not from the mutable working tree. ABCP invokes Git with replacement objects disabled, verifies `base_sha^{commit}` exactly, resolves each repository-relative source through the commit tree, rejects symlink/submodule/non-regular modes for source files, reads the exact blob with bounded plumbing, and verifies its SHA-256. No current-HEAD equality is needed to verify the immutable capsule source snapshot.

`ValidateCandidateV1` is separate. It verifies an exact lowercase commit object with replacement objects disabled, proves capsule base is an ancestor of candidate, computes the exact base...candidate changed-path set/digest, and validates it against stage/path authority. Before and after design review, review/fix mutation, acceptance, and final review, the governed workspace must be clean: no index/worktree/untracked drift and no unsafe submodule state. Read-only C checks run against the exact candidate checkout/object state, never ambient dirty bytes.

## 7. Semantic black-hole breaker

ABCP distinguishes three permissions:

`OBSERVE != BLOCK != MUTATE`.

A review may report an observation outside blocker authority only as `DEFERRED`. Deferred observations cannot block the current phase and cannot receive a mutation lease.

### INITIAL_IMPLEMENTATION profile

B issuance pre-authorizes a finite invariant registry set and a numeric `max_initial_active_findings` ceiling. Review pass 0 may establish one canonical active finding set, each finding mapped to a pre-authorized invariant/registry entry and bounded by the ceiling. That set is frozen by the first validated `ReviewScopeReportV1`. Later review passes may keep or remove active finding IDs; they may not add an ID or activate another invariant. A later independent C/M issue is deferred and yields `SCOPE_EXPANSION_REQUIRED` if it must block release.

### CORRECTION profile

B issuance already names the exact `authorized_finding_ids` and owning invariant IDs from predecessor evidence. Review pass 0 cannot add another blocker ID. Every subsequent active set must be an exact subset of the issued allowlist and of the preceding active set.

A `DESIGN_GAP` never acquires mutation authority under B. It stops convergence and requires a new A-governed design lineage.

## 8. ReviewScopeReport, mutation leases, and non-forkable review chain

`ReviewScopeReportV1` is strict-canonical and binds:

- B capsule file/internal digests and registry digest;
- monotonically increasing review sequence;
- predecessor report digest (absent only at sequence 0);
- exact reviewed pre-fix HEAD;
- active blocker finding IDs with registry rule IDs/severity/evidence refs;
- deferred observation IDs/evidence;
- requested mutation-justification IDs;
- exact reviewer/provider identity and review evidence digest.

ABCP persists one controller-owned review-tip record and accepts only a report extending that exact tip. Fork, replay with changed bytes, missing sequence, candidate mismatch, active-set growth, unauthorized blocker, or unregistered semantic mapping fails closed.

A valid blocking report does not itself authorize editing. ABCP derives a one-use `MutationLeaseV1` from the exact validated report and registry: lease blocker/mutation IDs must be members of the report active set and B mutation scope; lease `allowed_paths` is the deterministic intersection of B `allowed_paths` and every selected registry rule's permitted correction/path family; changed-file/byte ceilings may only tighten B limits. The controller persists lease state `ISSUED` under the same workflow lock and records its digest as the only consumable lease tip. Ralphex/Codex receives only this lease for that fix batch.

Lease consumption is atomic and non-forkable. Before any repository mutation ABCP CAS-transitions the exact lease digest from `ISSUED` to `CONSUMING`; a second consumer/replay fails. After mutation, ABCP validates a strict-canonical `MutationReceiptV1` binding lease digest, report digest, exact pre-fix HEAD, exact resulting commit, exact diff/path digest and counts, and predecessor receipt-tip digest. Under the workflow lock it independently checks ancestry/paths/diff, atomically appends the receipt, advances the receipt tip, and marks the lease `CONSUMED`. A crash after `CONSUMING` is recovery-blocking until deterministic reconciliation proves one result or escalates; the lease is never reissued. Unleased mutation, competing receipt, mutation for deferred/new findings, path escape, diff limit violation, dirty exit, or mismatched result HEAD fails the phase and cannot produce `IMPLEMENTATION_CONVERGED`.

Ralphex or model success text is never acceptance evidence.

## 9. Ralphex product-owned execution bounds

B carries mandatory `ExecutionBoundsV1`. The controller owns all values; repository `.ralphex` config cannot widen or replace them.

Initial ceiling profile is:

- `max_iterations <= 10` and `>= 1`;
- `session_timeout <= 90m` and `> 0`;
- `idle_timeout <= 45m` and `> 0`;
- `wall_clock_timeout <= 3h` and `> 0`;
- `finalize = false` only;
- exactly one incomplete executable Task/Iteration section;
- Codex task and review effort exactly `xhigh`.

`RalphexCapabilityV1` binds the pinned source/binary identity and exact support for `--max-iterations`, `--session-timeout`, `--idle-timeout`, `--skip-finalize`, `--base-ref`, selected executor/model/effort flags, isolated config, and the governed review/fix handoff mode described below. If the selected binary/adapter capability does not prove every requested bound or handoff property, admission fails `EXECUTION_BOUNDS_INVALID` before spawn. Native Ralphex review/fix is not admitted merely because it can review and mutate; it must expose a controller-enforced stop boundary before each fix batch, or ABCP must run review and fix as separate bounded invocations so report validation and lease issuance happen before mutation.

`ExecutionBoundsV1` also carries B-wide cumulative ceilings: `max_ralphex_invocations`, `max_review_reports`, `max_mutation_leases`, `max_total_fix_batches`, and `aggregate_wall_clock_timeout`. Each is finite, positive, controller-owned, and may only tighten from the accepted design grant. Counters and aggregate elapsed time are durable workflow state and never reset by starting another process. Hitting any ceiling ends B with a bounded non-success outcome; it does not mint fresh authority or reset counters.

The adapter emits all per-invocation controls structurally as argv. `wall_clock_timeout` is enforced by the ABCP process supervisor for one invocation; `aggregate_wall_clock_timeout` is measured across all B-governed invocations including review/fix handoffs and rate-limit waits. On Linux, governed execution must run inside a controller-created cgroup v2 scope (or an equivalently proven containment primitive) whose identity is bound in evidence; all spawned descendants must remain in that scope. Expiry first requests graceful termination, then bounded forced kill of the scope, and ABCP verifies the scope is empty before recording termination. If containment creation, membership verification, or empty-scope proof is unavailable, admission fails `EXECUTION_BOUNDS_INVALID`. `session_timeout` and `idle_timeout` use the pinned Ralphex capability semantics. `max_iterations` is the native per-invocation loop ceiling. `finalize=false` always emits `--skip-finalize` and isolated config cannot re-enable it. Rate-limit wait is separately bounded and consumes aggregate wall clock.

These are safety ceilings, not target durations. Semantic review convergence rules above remain mandatory. No controller path may loop by issuing a fresh B for the same unresolved active finding set without a new accepted design/checkpoint lineage.

## 10. C exact-head acceptance and merge chain

C is minted only at the exact `IMPLEMENTATION_CONVERGED` HEAD. `ACCEPTANCE_PASSED` binds C and that exact candidate plus deterministic acceptance evidence. `FINAL_REVIEW_CLEAN` must immediately extend that checkpoint, bind the same exact candidate, be read-only, and record Critical 0 / Major 0 under C blocker scope.

Any repository/content change after C minting or after either checkpoint invalidates the C chain. Work returns to B authority if still valid for the required correction; after convergence a new B convergence checkpoint/grant and new C' are required. C is never patched in place.

`PR_PUBLISHED` binds the exact same candidate as PR head plus repository/PR stable identity and publication evidence. `MERGE_AUTHORIZED` must extend it and additionally bind the existing EP-005 lifecycle merge authorization/seal identity: exact target repository/base branch and expected base OID, exact PR/head OID, merge method, authenticated acting principal, accepted integration/policy evidence, expected result recipe/OID where applicable, and provider capability evidence. Any target/head/policy/principal drift invalidates merge authorization.

The provider-specific EP-005 merge contract remains authoritative for atomic write/reconciliation. C cannot weaken it. Post-effect reconciliation produces `POST_MERGE_ACCEPTED` only after the exact lifecycle result/post-merge proof is accepted. Thus candidate-head equality alone is never treated as sufficient merge safety.

## 11. Activation and legacy evidence

V3 becomes mandatory only for the A/B/C autonomous-development workflow after a precise `GovernanceActivationV1` record is committed/merged and durably recorded by the controller. The activation record binds policy version/digest, activation repository commit, activation sequence/time, and the finite set of already-issued V2 authority digests allowed to finish.

A V2 authority is grandfathered only if its exact digest appears in that activation record and was durably issued before activation. A caller cannot claim “in flight.” No post-activation V2 authority can enter the A/B/C workflow or mint a V3 child.

V2 deployment/recovery/maintenance remain valid under their existing contracts until separately migrated.

## 12. Product validation/API surface

Implement as product library validators called by controller paths, with CLI commands exposing the same logic for deterministic acceptance/diagnostics:

1. V3 build/parse/immutable-base-tree verify plus unchanged V1/V2 verification.
2. `ValidateCapsuleUsageV3` for fixed stage/operation/read-only rules.
3. `ValidatePhaseCheckpointV1` and non-forkable checkpoint-chain transitions.
4. `ValidateDerivationV3` for exact A checkpoint/grant -> B and B checkpoint/grant -> C.
5. `ValidateCandidateV1` for replacement-resistant commit/ancestry/clean/path/diff proof.
6. `ValidateReviewScopeReportV1` for registry mapping, profile rules, active-set monotonicity, report-tip chaining, and deferred separation.
7. `Issue/ValidateMutationLeaseV1` and `ValidateMutationReceiptV1` for review-fix authority.
8. Ralphex capability/execution-bound validation and structural argv emission.
9. C checkpoint-chain validation hooks that the existing PR/merge lifecycle must consume before effect.

The controller calls validators before execution/effect; CLI is not an alternate weaker authority path.

## 13. Deterministic failure classes

At minimum:

- `CAPSULE_STAGE_INVALID`
- `CAPSULE_LINEAGE_INVALID`
- `CAPSULE_USAGE_INVALID`
- `CHECKPOINT_CHAIN_INVALID`
- `CANDIDATE_HEAD_INVALID`
- `MUTATION_SCOPE_VIOLATION`
- `SCOPE_EXPANSION_REQUIRED`
- `DESIGN_GAP`
- `REVIEW_CHAIN_INVALID`
- `EXECUTION_BOUNDS_INVALID`
- `FINAL_REVIEW_INVALIDATED`

None grants retry, scope expansion, design amendment, publication, or merge.

## 14. Fresh-chat bootstrap authority

A fresh ChatGPT session is stateless and non-authoritative. It must read durable authorities, then query exact live Git/ABCP evidence. Memory or a pasted prior-chat summary may help navigation but can never override Git/controller evidence.

Mandatory cross-repository entry set:

1. `pankajleh/dev_agent_automation_platform/docs/architecture/autonomous-builder/README.md`
2. `pankajleh/dev_agent_automation_platform/docs/architecture/autonomous-builder/Autonomous_Software_Builder_Architecture_and_Current_State.md`
3. `pankajleh/autonomous-builder-control-plane/docs/architecture/AUTONOMOUS_EXECUTION_GOVERNANCE.md`
4. `pankajleh/autonomous-builder-control-plane/docs/architecture/CONTEXT_AUTHORITY_AND_CAPSULE_POLICY.md`
5. the active execution pack/plan named by live state plus the active Capsule A/B/C, checkpoint-chain tip, exact candidate HEAD, and authority/evidence digests.

The two `dev_agent_automation_platform` entry documents must link the ABCP governance documents. ABCP current-state/progress projections should also identify active governance policy/capsule/checkpoint state when available.

## 15. Review findings closed by this revision

The first revision closed Codex M-01 through M-09 and ChatGPT M-01 through M-04. The exact-head bounded re-review then identified only six closure defects in those mechanisms; this revision is intentionally limited to those six: mandatory review floors and transitive A/B/C obligation preservation; atomic one-use mutation-lease consumption; controller-visible review->lease->fix handoff before mutation; B-wide cumulative execution ceilings; descendant containment/verified teardown; and deterministic lease-path intersection with semantic correction authority.

The next design re-review is a closure-only review. It may verify these six defects against the previously reviewed design and Capsule-A authority. It must not reopen unrelated architecture, search for new optimization opportunities, or promote a newly observed independent concern into this correction cycle. Any independent concern is `DEFERRED` and requires separate authority.

## 16. Bounded implementation task

### Task 1: Implement the three-capsule governance validation foundation

- [ ] Add V3 schema/build/parse/base-tree verification, canonical stages/operations, phase authority, parent/grant bindings, semantic registry references, allowed paths, review profiles, and execution bounds while preserving V1/V2 bytes/behavior.
- [ ] Add strict checkpoint/grant, derivation, candidate/path, review-scope-report chain, mutation-lease/receipt validators and adversarial/golden tests.
- [ ] Make new governed Ralphex A/B/C-workflow execution require B V3 and controller-owned max-iterations/session/idle/wall-clock/finalize bounds; structurally emit required Ralphex flags and fail closed on unsupported capability.
- [ ] Add `AUTONOMOUS_EXECUTION_GOVERNANCE.md`; supersede contradictory v2-era prose in context and Ralphex adapter contracts without rewriting historical evidence.
- [ ] Add bounded CLI validation/diagnostic commands backed by the same library validators.
- [ ] Add the activation/grandfathering contract and hooks needed so future A/B/C runs cannot fall back to V2 by assertion.
- [ ] Run gofmt, full Go tests/vet, changed-package race tests, CLI tests, golden vectors, git diff --check, and exact changed-path audit.

Implementation non-goals: EP-005 merge Task-1 semantics, live GitHub transport changes, NUC runtime activation, inventing a replacement PR merge protocol, and rewriting historical V1/V2 capsule artifacts.

# Autonomous Execution Governance

## Governing objective

ABCP prevents autonomous development from turning newly observed concerns into self-expanding blocker and mutation authority. Durable controller evidence, not chat state or model output, governs every transition.

The workflow uses exactly three immutable context capsules:

`A_DESIGN -> DESIGN_ACCEPTED/grant -> B_IMPLEMENTATION -> IMPLEMENTATION_CONVERGED/grant -> C_ACCEPTANCE_MERGE`

A owns design and read-only design acceptance. B owns implementation and a bounded, semantically monotone review/fix cycle. C owns deterministic acceptance, read-only exact-head final review, PR publication, merge authorization, and post-merge proof. Provider effects still require the existing EP-005 lifecycle contract; C only adds mandatory lineage and exact-head gates.

## Controller evidence chain

Strict-canonical `PhaseCheckpointV1` records form one non-forkable sequence with an exact durable tip. Each record binds repository, capsule file/internal digests, candidate commit, operation/verdict, evidence, controller policy/event identity, sequence, and predecessor digest. Create-or-verify replay is permitted; a competing next record is integrity failure.

`NextStageGrantV1` records set child maxima and mandatory floors. A-to-B binds the accepted design HEAD and reviewed semantic registry. B-to-C binds the converged implementation HEAD and final review-scope tip and preserves A/B invariant and final-review floors transitively. Children may narrow sets and numeric ceilings but may never drop a floor.

Candidate verification disables replacement objects, proves ancestry, computes canonical changed paths/digest and bounded diff size, enforces exact path authority, and requires a clean workspace and safe submodules. C checkpoints through publication and authorization bind one unchanged candidate. Any content change invalidates C and returns to valid B authority or a newly accepted lineage.

## Semantic convergence and mutation

The immutable semantic registry is authored and reviewed under A. Review reports can reference known rule IDs but cannot change predicates, evidence classes, owners, correction relations, or paths.

Initial B review freezes its first bounded blocker set. Correction B starts with an exact authorized finding set. Every later active set is a subset of the preceding set. Independent observations are deferred; an issue that must newly block requires scope expansion/new authority. Design gaps return to A.

Mutation requires a controller-derived lease for active, mutation-authorized findings. Lease paths are the deterministic intersection of B paths and semantic correction paths. The workflow lock permits one exact lease tip to move once from issued to consuming to consumed, with an independently checked receipt. A consuming-state crash is recovery-blocking and never causes reissue.

## Bounded execution

Ralphex admission validates the pinned capability, structurally emits every native limit, uses isolated configuration, and requires a controller handoff before each fix batch. Per-invocation and cumulative counts/time/diff ceilings only tighten. Linux descendants remain in a controller-created cgroup-v2 scope through verified empty teardown. Missing capability or containment fails before mutation.

## Activation, failure classes, and fresh sessions

`GovernanceActivationV1` is the only V3 cutover authority. It names the policy/commit/sequence/time and the finite pre-issued V2 digests allowed to finish. V2 deployment, recovery, and maintenance remain on their current contracts.

Validators return stable classes: `CAPSULE_STAGE_INVALID`, `CAPSULE_LINEAGE_INVALID`, `CAPSULE_USAGE_INVALID`, `CHECKPOINT_CHAIN_INVALID`, `CANDIDATE_HEAD_INVALID`, `MUTATION_SCOPE_VIOLATION`, `SCOPE_EXPANSION_REQUIRED`, `DESIGN_GAP`, `REVIEW_CHAIN_INVALID`, `EXECUTION_BOUNDS_INVALID`, and `FINAL_REVIEW_INVALIDATED`. None grants retry or wider authority.

A fresh session begins with this document, `CONTEXT_AUTHORITY_AND_CAPSULE_POLICY.md`, the active execution pack/plan, active capsule and checkpoint tip, exact candidate HEAD, and controller evidence digests. Cross-repository entry documents must link here; memory and pasted summaries cannot override live Git/controller state.

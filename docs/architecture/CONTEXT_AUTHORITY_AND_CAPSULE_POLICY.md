# Context Authority and Capsule Policy

## Status and authority

`context-capsule-v3` is the product contract for the A/B/C autonomous-development workflow after a committed `GovernanceActivationV1`. The earlier universal-v2 and one-capsule-per-commit operating prose is superseded for that workflow by this document and `AUTONOMOUS_EXECUTION_GOVERNANCE.md`.

V1 and V2 bytes retain their historical parse and verification meaning. V2 remains the separately governed format for deployment, recovery, and maintenance until an explicit migration. No activation record retroactively strengthens old evidence.

## Durable authority hierarchy

Fresh sessions are stateless. They read, in order, architecture and ADRs, roadmap/current state, the active execution pack and task plan, the active capsule/checkpoint tip, and exact controller evidence. Chat history, model memory, shell wrappers, and reviewer prose are navigation aids only.

The autonomous-development workflow has exactly three immutable capsules:

`A_DESIGN -> B_IMPLEMENTATION -> C_ACCEPTANCE_MERGE`

- A permits design planning and read-only design review. Its mutation paths are limited to the named design documents.
- B permits implementation and implementation review. Review-driven mutation additionally requires a controller-issued, one-use mutation lease.
- C permits acceptance, read-only exact-head final review, PR publication, merge authorization, and post-merge acceptance. Repository content is read-only throughout C.

Exact candidate commits remain checkpoint evidence rather than mutable capsule fields.

## V3 phase authority

Every V3 capsule binds the existing project, plan, task, repository, base, source, invariant, non-goal, and predecessor fields plus `phase_authority`. Phase authority fixes the canonical stage and operation set; parent checkpoint and grant; immutable semantic registry digest; observation, blocking, and mutation rule IDs; authorized invariant/finding IDs; exact paths or concrete `dir/**` prefixes; review profile; and, for B, execution bounds.

All arrays are sorted and unique. Blocking is a subset of observation; mutation is a subset of blocking. Absolute paths, traversal, regex/glob syntax other than `dir/**`, symlink-derived paths, and broad-root patterns fail closed.

A child cannot establish lineage by naming a predecessor commit. A `PhaseCheckpointV1` and `NextStageGrantV1` establish it. B and C must equal or narrow grant maxima and retain every required blocker, invariant, operation, and final-review floor. B-to-C derivation also proves those floors were preserved transitively.

## Source and candidate verification

V1/V2 verification remains checkout-bound exactly as before: repository HEAD equals capsule base and live source bytes match the recorded hashes.

V3 verification disables Git replacement objects, resolves the exact base commit, reads regular-file blobs from that immutable commit tree, and hashes those bytes. A later clean candidate HEAD therefore does not invalidate the capsule's immutable source proof.

Candidate verification is separate. It proves exact lowercase commit identity, base ancestry, a clean index/worktree/untracked/submodule state, the exact `base...candidate` path digest and diff bounds, and stage path authority. C requires the exact converged base HEAD with no repository-content change.

## Semantic authority and review convergence

`OBSERVE != BLOCK != MUTATE`.

The A-reviewed `SemanticAuthorityRegistryV1` fixes each rule, predicate, evidence class, correction relation, owner/path family, deterministic validator identity, and whether model-only observation is permitted. A reviewer cannot mint or redefine a rule.

For `INITIAL_IMPLEMENTATION`, report zero establishes a bounded active-finding set. Later reports may retain or remove IDs but never add one. For `CORRECTION`, every blocker is already named by B and the same monotonic rule applies. An unmapped or newly blocking concern returns `SCOPE_EXPANSION_REQUIRED`; a `DESIGN_GAP` returns to a new A lineage.

`ReviewScopeReportV1` records one non-forkable report tip. Deferred observations never block and never justify mutation. A validated blocking report is still read-only: the controller deterministically intersects B paths with registry correction paths and issues a single-use `MutationLeaseV1`. The lease must CAS from `ISSUED` to `CONSUMING` before edits. An independently validated `MutationReceiptV1` advances the receipt tip and marks it `CONSUMED`. Replay, fork, crash ambiguity, path escape, dirty exit, or diff-bound violation blocks convergence.

## Activation and startup

V3 becomes mandatory only when `GovernanceActivationV1` is committed/merged and durably recorded. The record binds policy digest, activation commit/sequence/time, and a finite sorted list of V2 authority digests issued before activation. Only exact listed digests are grandfathered. A caller's “in flight” assertion is not evidence.

The controller supplies capsule path and exact file SHA through its validated environment. Fresh executors verify those bytes and the repository evidence before work. CLI diagnostics (`context-build`, `context-verify`, and `governance-*-validate`) invoke the same library validators; they are not weaker alternate authority paths.

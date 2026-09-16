# Assurance Governance Enforcement — A Assurance Model

Status: **A_DESIGN correction candidate — implementation forbidden until fresh exact-head design/assurance review is 0C/0M**

Base authority: merge commit `04e28eb7807acecc16161d6e33fb20bf88476ce7` (PR #19). Normative policy: `docs/architecture/ASSURANCE_MODEL_AND_PROOF_OBLIGATION_POLICY.md`. Functional plan: `docs/plans/assurance-proof-obligation-governance-enforcement.md`.

## Scope and implementation authority

This is code-bearing ABCP work. `documentation_only` is forbidden. B mutation authority is limited to these concrete paths/prefixes:

- `internal/context/**`
- `internal/governance/**`
- `internal/authority/**`
- `internal/run/**`
- `internal/acceptance/**`
- `internal/authoritybackend/**`
- `cmd/abcp/**`
- `scripts/acceptance/assurance-governance-dogfood.sh`
- `scripts/acceptance/assurance-governance-postmerge.sh`
- `scripts/acceptance/assurance-go-test-exact.py`
- `go.mod`
- `go.sum`
- `docs/plans/assurance-proof-obligation-governance-enforcement.md`
- `docs/plans/completed/assurance-proof-obligation-governance-enforcement.md`

`docs/plans/assurance-proof-obligation-governance-enforcement-assurance.md` is immutable throughout B/C. The functional plan may change only task checkbox/status markers and its final byte-identical semantic move to the named completed path; any other semantic plan edit is `MUTATION_SCOPE_VIOLATION`. Any need for another package/path is `SCOPE_EXPANSION_REQUIRED` and returns to A.

Exactly three capsules remain authoritative: A owns this assurance model, B implements against it, and C independently executes branch-stage evidence and performs read-only exact-head review. No reviewer, validator, policy manifest, or evidence artifact is a fourth authority surface.

Finite cumulative final-review correction-reentry ceiling: **2 correction-B lineages per accepted A lineage**. One reentry is consumed only by the atomic durable controller-state CAS that publishes a new correction-B grant. A failed-C report may contain multiple mapped findings but consumes one reentry when one correction-B grant is published. Exact replay consumes zero additional reentries. After two successful correction-B grant publications, any later C implementation blocker returns to A.

## Frozen invariants

| ID | Invariant |
|---|---|
| AG-I01 | Successor assurance activation is authorized only from an exact existing merge/post-merge lifecycle authority for the policy commit, exact predecessor activation, authenticated acting principal, protected base branch, and strict controller-recomputed policy manifest. |
| AG-I02 | Every applicable failure scenario has stable referential traceability to invariant → proof obligation → one or more exact evidence cells. |
| AG-I03 | Assurance strengthens A/B/C without creating mutable reviewer authority or a fourth capsule. |
| AG-I04 | Work defaults code-bearing; any documentation-only exemption is controller-owned, path/semantic bounded, and exact-diff revalidated. |
| AG-I05 | Every evidence cell binds one proof obligation, one evidence class, one lifecycle stage, one exact subject rule, and one validator/artifact identity. |
| AG-I06 | B/C cannot weaken the frozen assurance model; model insufficiency returns to A as `ASSURANCE_MODEL_GAP`. |
| AG-I07 | C implementation findings invalidate C and may enter only bounded controller-derived correction-B authority from frozen A evidence. |
| AG-I08 | Assurance records are deterministic, strict, bounded, versioned, create-or-verify idempotent, and do not reinterpret historical V1/V2/V3 bytes. |
| AG-I09 | Fresh sessions fail closed when activation, model, proof, matrix, lineage, repository, backend, or exact-subject bindings are absent, stale, replaced, expired-by-state, or inconsistent. |
| AG-I10 | Every new durable authority publication uses durable content-addressed artifact → atomic controller-state CAS; crash/ambiguity cannot fork, reissue, or partially authorize state. |
| AG-I11 | Repository/controller/path/object identity cannot be substituted to obtain another lineage's assurance authority or evidence. |
| AG-I12 | Physical resources, retries, evidence, validator processes, execution time/output, and correction reentries are atomically reserved/charged and bounded cumulatively across the accepted A lineage. |
| AG-I13 | A→B, B→C, correction-B→replacement-C, integration, merge authorization, and post-merge derivations preserve the activated assurance digests and mandatory floors transitively. |
| AG-I14 | Cancellation, timeout, and external-observation ambiguity never create implicit retry authority; uncertain durable mutation is reconciled before another mutation attempt. |
| AG-I15 | Durable A/B/C grants and checkpoints have explicit state-based validity; they never silently expire by wall clock, while existing time-bounded execution/mutation leases remain unusable after their frozen expiry. |
| AG-I16 | Every validator attempt has one controller-issued reservation/ordinal, predecessor revision, secret-free execution policy, exact tool identity, and deterministic terminal evidence selection. |
| AG-I17 | Required integration acceptance is a merge-authorization prerequisite; any C invalidation races atomically with merge authority, and post-merge failure cannot be misreported as completion or silently rolled back. |
| AG-I18 | The production workflow-authority backend is a specified PostgreSQL linearizable CAS implementation with durable bootstrap, authenticated namespace, and fresh-process composition; backend absence/unavailability is fail-closed. |

## Stable failure-scenario registry

| Scenario ID | Dimension | Scenario | Invariants | Proof obligations |
|---|---|---|---|---|
| AG-S-001 | crash consistency | process dies after successor policy artifact durability but before activation CAS | AG-I01,AG-I10 | AG-PO-001,AG-PO-003,AG-PO-007,AG-PO-020,AG-PO-038 |
| AG-S-002 | crash consistency | process dies after successor activation CAS response is lost/ambiguous | AG-I01,AG-I10,AG-I14 | AG-PO-003,AG-PO-020,AG-PO-021,AG-PO-038 |
| AG-S-003 | crash consistency | process dies while publishing A model/grant/checkpoint artifact before controller CAS | AG-I02,AG-I10,AG-I13 | AG-PO-007,AG-PO-009,AG-PO-020 |
| AG-S-004 | crash consistency | process dies while publishing evidence/stage checkpoint | AG-I05,AG-I10,AG-I16 | AG-PO-007,AG-PO-014,AG-PO-020,AG-PO-033 |
| AG-S-005 | crash consistency | process dies while publishing failed-C record or correction-B grant/counter | AG-I07,AG-I10 | AG-PO-017,AG-PO-018,AG-PO-019,AG-PO-020 |
| AG-S-006 | concurrency/races | two successor activation installations race | AG-I01,AG-I10 | AG-PO-003,AG-PO-020,AG-PO-038 |
| AG-S-007 | concurrency/races | competing A→B/B→C/correction derivations race | AG-I07,AG-I10,AG-I13 | AG-PO-009,AG-PO-010,AG-PO-011,AG-PO-019,AG-PO-020 |
| AG-S-008 | concurrency/races | evidence/stage writers or resource reservations race | AG-I05,AG-I10,AG-I12,AG-I16 | AG-PO-014,AG-PO-020,AG-PO-032,AG-PO-033 |
| AG-S-009 | retry/replay | exact activation/model/grant/checkpoint/evidence request is retried | AG-I08,AG-I10,AG-I16 | AG-PO-003,AG-PO-007,AG-PO-033 |
| AG-S-010 | retry/replay | conflicting duplicate durable record uses same logical identity with different bytes | AG-I08,AG-I10 | AG-PO-004,AG-PO-007 |
| AG-S-011 | retry/replay | failed-C/correction request is replayed after success/restart | AG-I07,AG-I12 | AG-PO-017,AG-PO-018,AG-PO-019 |
| AG-S-012 | authority identity | policy/model/matrix/candidate bytes or digest are stale/replaced | AG-I01,AG-I09,AG-I13 | AG-PO-001,AG-PO-009,AG-PO-010,AG-PO-011,AG-PO-023,AG-PO-038 |
| AG-S-013 | authority identity | predecessor V3 authority is reissued or used after successor activation when not grandfathered | AG-I01,AG-I08,AG-I09 | AG-PO-002,AG-PO-005,AG-PO-023 |
| AG-S-014 | partial failure | content-addressed child is durable but no controller-state CAS references it | AG-I10 | AG-PO-007,AG-PO-020 |
| AG-S-015 | partial failure | controller state attempts to reference missing/conflicting child artifact | AG-I09,AG-I10 | AG-PO-007,AG-PO-020,AG-PO-023 |
| AG-S-016 | cancellation | cancellation occurs before durable child artifact | AG-I14 | AG-PO-021 |
| AG-S-017 | cancellation | cancellation/timeout occurs after CAS may have been submitted | AG-I10,AG-I14 | AG-PO-020,AG-PO-021 |
| AG-S-018 | cancellation | validator times out after partial stdout/stderr/evidence | AG-I05,AG-I12,AG-I14,AG-I16 | AG-PO-014,AG-PO-021,AG-PO-025,AG-PO-033 |
| AG-S-019 | resource exhaustion | assurance model/proof/evidence cardinality or bytes exceed profile | AG-I08,AG-I12 | AG-PO-004,AG-PO-025,AG-PO-032 |
| AG-S-020 | resource exhaustion | candidate/result churn attempts to reset validator/retry/evidence ceilings | AG-I12 | AG-PO-019,AG-PO-025,AG-PO-032 |
| AG-S-021 | resource exhaustion | validator leaks descriptors/processes or exceeds memory/output/time/fan-out | AG-I12 | AG-PO-025,AG-PO-032 |
| AG-S-022 | restart/recovery | fresh process opens after any durable publication boundary | AG-I09,AG-I10,AG-I18 | AG-PO-020,AG-PO-023,AG-PO-034 |
| AG-S-023 | malformed/forged | duplicate/unknown fields, invalid IDs/stages, forged digests, or oversized canonical input | AG-I02,AG-I08,AG-I12 | AG-PO-004,AG-PO-006,AG-PO-025 |
| AG-S-024 | filesystem identity | repository/controller path, symlink, or object identity is substituted | AG-I09,AG-I11 | AG-PO-023,AG-PO-024 |
| AG-S-025 | external dependency | authoritative Git object/ref or PostgreSQL backend is unavailable/delayed | AG-I09,AG-I14,AG-I18 | AG-PO-022,AG-PO-023,AG-PO-034 |
| AG-S-026 | external dependency | Git/ref/backend observation changes, duplicates, or is inconsistent between observation/use | AG-I09,AG-I14,AG-I18 | AG-PO-022,AG-PO-023,AG-PO-034 |
| AG-S-027 | compatibility/version skew | eligible pre-successor V3 lineage continues under predecessor policy | AG-I01,AG-I08 | AG-PO-002,AG-PO-005,AG-PO-010 |
| AG-S-028 | compatibility/version skew | unlisted/post-successor V3 lineage attempts predecessor policy | AG-I01,AG-I08,AG-I09 | AG-PO-002,AG-PO-005,AG-PO-010,AG-PO-023 |
| AG-S-029 | security boundary | another repository/controller identity reuses assurance/grant/evidence/backend namespace | AG-I09,AG-I11,AG-I18 | AG-PO-012,AG-PO-023,AG-PO-024,AG-PO-034 |
| AG-S-030 | model mutation | B attempts to change/N-A/weaken model, matrix, stage, validator, or journey | AG-I02,AG-I06 | AG-PO-006,AG-PO-013,AG-PO-027 |
| AG-S-031 | C authority | C reviewer/finding attempts direct repository mutation | AG-I03,AG-I07 | AG-PO-017,AG-PO-018 |
| AG-S-032 | assurance gap | missing/misclassified scenario or evidence requirement attempts correction B | AG-I06,AG-I07 | AG-PO-013,AG-PO-026 |
| AG-S-033 | documentation bypass | governance/runtime-affecting diff claims `documentation_only` | AG-I04,AG-I09 | AG-PO-008,AG-PO-023 |
| AG-S-034 | evidence binding | evidence from wrong stage/subject/weaker class/attempt tries to satisfy a cell | AG-I05,AG-I09,AG-I16 | AG-PO-014,AG-PO-015,AG-PO-016,AG-PO-027,AG-PO-033 |
| AG-S-035 | integration boundary | mock/fake substitutes for required production-composition boundary | AG-I05 | AG-PO-015,AG-PO-016,AG-PO-028,AG-PO-030,AG-PO-034 |
| AG-S-036 | integration/E2E | assembled CLI/controller/Ralphex journey disagrees with isolated tests | AG-I02,AG-I05,AG-I09 | AG-PO-015,AG-PO-016,AG-PO-029,AG-PO-030,AG-PO-035 |
| AG-S-037 | correction ceiling | two correction-B grants succeed, then a third is attempted after restart/new C | AG-I07,AG-I12 | AG-PO-011,AG-PO-018,AG-PO-019 |
| AG-S-038 | assurance escape | foreseeable Critical/Major first appears in C and continuation is attempted without new A | AG-I06,AG-I07,AG-I09 | AG-PO-026 |
| AG-S-039 | authority expiry | durable grant/checkpoint is reused after its state successor invalidates it, or an expired execution/mutation lease is replayed | AG-I09,AG-I15 | AG-PO-031 |
| AG-S-040 | resource accounting | process dies/competes after resource reservation but before validator finalization/cleanup | AG-I10,AG-I12,AG-I16 | AG-PO-020,AG-PO-032,AG-PO-033 |
| AG-S-041 | evidence replay | multiple validator attempts exist for one cell/subject and a stale/failed attempt is selected | AG-I05,AG-I16 | AG-PO-014,AG-PO-033 |
| AG-S-042 | backend durability | PostgreSQL CAS/bootstrap/auth namespace is missing, duplicated, unavailable, or returns ambiguous commit outcome | AG-I09,AG-I10,AG-I18 | AG-PO-020,AG-PO-034 |
| AG-S-043 | dogfood containment | integration dogfood attempts nested dogfood or resets/escapes parent counters/containment | AG-I12,AG-I16 | AG-PO-025,AG-PO-032,AG-PO-035 |
| AG-S-044 | test non-vacuity | named Go validator exits zero while zero expected test events actually ran | AG-I05,AG-I16 | AG-PO-033,AG-PO-036 |
| AG-S-045 | lifecycle ordering | integration fails or C invalidates concurrently with merge authorization/publication/post-merge | AG-I13,AG-I17 | AG-PO-016,AG-PO-017,AG-PO-037 |

All minimum policy dimensions are applicable. No failure-model dimension is `not_applicable` for this controller-governance implementation. Evidence-class applicability is separately frozen below; `runtime` and `production` are explicitly N/A because this execution pack does not deploy a long-running production ABCP service.

## Frozen proof obligations

| ID | Falsifiable claim | Invariants |
|---|---|---|
| AG-PO-001 | `AssurancePolicyManifestV1` binds exact predecessor activation digest, exact authorized merge-result policy commit/tree, exact protected base branch, authenticated acting principal, successor capsule wire version `context-capsule-v4`, assurance schema `assurance-policy-v1`, and sorted canonical full policy path+SHA256 entries recomputed from that commit; no ambient/caller digest is trusted. | AG-I01,AG-I08,AG-I09 |
| AG-PO-002 | successor activation grandfather entries are controller-derived exactly from valid nonterminal predecessor-V3 lineage/checkpoint state at the pre-CAS revision; listed entries remain predecessor-policy-only, and unlisted/post-activation V3 lineages cannot use predecessor policy or mint successor children. | AG-I01,AG-I08,AG-I09 |
| AG-PO-003 | successor activation installation is one atomic compare-and-swap over predecessor activation + expected controller revision; exact replay is idempotent and competing/conflicting installation changes no authority. | AG-I01,AG-I08,AG-I10 |
| AG-PO-004 | every assurance record strict-parses canonically; sorted-set reorder hashes identically while duplicate/unknown/ambiguous/oversized fields fail before authority/evidence allocation. | AG-I08,AG-I12 |
| AG-PO-005 | historical V1/V2/V3 bytes retain prior meaning; predecessor V3 may continue only through exact grandfather entries and never authorizes successor-schema A. | AG-I01,AG-I08 |
| AG-PO-006 | every `AG-S-*`, `AG-I*`, `AG-PO-*`, evidence cell, validator, and journey reference resolves uniquely; every scenario reaches evidence and every proof obligation has ≥1 exact evidence cell. | AG-I02,AG-I05 |
| AG-PO-007 | every durable assurance artifact is content-addressed and create-or-verify; conflicting bytes for a logical/content identity fail, durable unreferenced artifacts grant no authority, and only atomic controller-state CAS may make a child authoritative. | AG-I08,AG-I10 |
| AG-PO-008 | work defaults code-bearing; only controller-bound A classification can grant `documentation_only`, authoritative governance/policy is ineligible, and exact B/C diff semantic/path revalidation invalidates a false exemption. | AG-I04,AG-I09 |
| AG-PO-009 | A→B binds exact accepted-A checkpoint/head, activated policy-manifest/model/proof/matrix digests, final-review profile, mandatory floors, exact B paths, execution bounds, and correction-reentry ceiling. | AG-I02,AG-I03,AG-I13 |
| AG-PO-010 | B→C binds exact converged B head/review tip and transitively preserves every activated assurance digest, mandatory floor, final-review profile, and remaining lineage-wide resource/reentry counters. | AG-I05,AG-I13 |
| AG-PO-011 | correction-B→replacement-C preserves the same accepted A assurance digests/floors and exact failed-C ancestry while only narrowing paths/findings/bounds; it cannot reset lineage-wide counters. | AG-I07,AG-I12,AG-I13 |
| AG-PO-012 | missing/malformed/unactivated assurance authority, wrong repository/controller identity, or invalid A/B derivation blocks B/Ralphex admission before mutation. | AG-I09,AG-I11,AG-I13 |
| AG-PO-013 | B cannot modify/reclassify/N-A/weaken the frozen assurance model; any insufficient model has precedence as `ASSURANCE_MODEL_GAP` and yields zero correction mutation authority. | AG-I02,AG-I06 |
| AG-PO-014 | B convergence requires every B-stage cell for the exact candidate; wrong-stage, wrong-subject, weaker-class, unavailable, or incomplete evidence cannot satisfy a cell. | AG-I05,AG-I09 |
| AG-PO-015 | C independently executes exactly C_BRANCH_ACCEPTANCE cells against one unchanged exact candidate and validates all required predecessor B cells/digests. | AG-I03,AG-I05,AG-I09 |
| AG-PO-016 | integration and post-merge cells execute only at their frozen stages against exact integration/merge subjects; later-stage evidence cannot satisfy C early. Production class is N/A for this non-deployment execution pack. | AG-I05,AG-I09 |
| AG-PO-017 | a sufficient-model C implementation blocker produces a durable failed-C record and C invalidation but never mutation authority by reviewer assertion. | AG-I03,AG-I07,AG-I10 |
| AG-PO-018 | correction B may be derived only by controller CAS from exact failed-C evidence mapped to frozen A semantics/correction paths; unmapped findings return to A. | AG-I07,AG-I10 |
| AG-PO-019 | correction reentry is consumed only by atomic publication of a correction-B grant; exact replay consumes zero, concurrent attempts cannot double-consume, counters survive restart/new B/C, exactly two grants may publish, and a third is denied and returns to A. | AG-I07,AG-I12 |
| AG-PO-020 | every new authority/evidence family follows the frozen publication-boundary matrix; termination or ambiguous CAS recovers to exact success, provable non-application, or recovery-blocked fail-closed without fork/reissue. | AG-I10,AG-I14 |
| AG-PO-021 | cancellation/timeout before durable publication authorizes cleanup only; after possible CAS submission retry authority is zero until exact controller-state reconciliation proves applied/not-applied; applied state wins over cancellation. | AG-I10,AG-I14 |
| AG-PO-022 | Git/external observations are bounded, exact-object/ref bound, and stability-checked immediately before authority use; unavailable/delayed/inconsistent observations fail closed and authorize no mutation/retry. | AG-I09,AG-I14 |
| AG-PO-023 | every fresh session validates exact activated policy/model/proof/matrix/capsule/checkpoint/candidate/repository identities from durable evidence and rejects absent, stale, replaced, or wrong-version components. | AG-I09,AG-I11,AG-I13 |
| AG-PO-024 | descriptor-relative/no-follow repository/controller-state access and exact repository identity prevent path/symlink/object substitution or cross-repository authority reuse. | AG-I09,AG-I11 |
| AG-PO-025 | all structural, evidence, retry, validator, process, output, time, concurrency, and reentry limits in the resource profile are lineage-cumulative where specified, pre-reserved/charged atomically, and physically enforced without reset by subject/candidate churn. | AG-I12 |
| AG-PO-026 | every foreseeable Critical/Major assurance escape is durably recorded and atomically marks the lineage A-required; no further B/C implementation authority may issue until a new A completeness review. | AG-I06,AG-I07,AG-I09 |
| AG-PO-027 | the evidence matrix is strict: one cell = one proof ID + one evidence class + one stage + one subject rule + one validator ID; cells and validator registry are canonical authority, not prose suggestions. | AG-I02,AG-I05 |
| AG-PO-028 | fake/mock evidence may satisfy only explicitly named unit/contract cells; required smoke/integration cells use the production composition boundaries frozen below, otherwise evidence is invalid. | AG-I05 |
| AG-PO-029 | J-01..J-05 execute with the exact validator IDs and boundary rules below, including two successful correction reentries and denial of the third. | AG-I02,AG-I05,AG-I07,AG-I12 |
| AG-PO-030 | the real dogfood integration uses assembled ABCP CLI/controller, real Git/filesystem/controller state, and pinned Ralphex/Codex on a disposable repository; model narration or an in-process fake cannot substitute. | AG-I03,AG-I05,AG-I09 |
| AG-PO-031 | Durable A/B/C grants/checkpoints have no wall-clock expiry but become unusable immediately when controller state advances beyond their exact predecessor/tip or explicitly invalidates them; existing execution reservations/mutation leases preserve their frozen time expiry and fail closed after it. | AG-I09,AG-I15 |
| AG-PO-032 | Every bounded resource is atomically reserved in controller state before execution, charged with a lineage-global ordinal, finalized to actual usage after evidence durability, and remains conservatively charged at the reservation maximum after crash/ambiguous cleanup until controller reconciliation proves safe release; parallel/rejected/orphan work cannot oversubscribe ceilings. | AG-I10,AG-I12,AG-I16 |
| AG-PO-033 | Every validator attempt binds a controller-issued lineage-global invocation ordinal/reservation digest, predecessor revision, cell/subject, working-directory identity, secret-free environment-policy digest, authenticated principal, executable/toolchain/Ralphex/Codex digests as applicable, and terminal evidence digest; a stage checkpoint selects exactly one successful attempt per cell and stale/failed/replayed attempts cannot satisfy it. | AG-I05,AG-I08,AG-I16 |
| AG-PO-034 | Assembled durable controller operations use `PostgresWorkflowAuthorityBackendV1` over PostgreSQL with one authenticated authority domain/namespace, strict bootstrap schema, serializable/atomic revision CAS, fresh-process visibility, and fail-closed connection/commit ambiguity; backendless CLI constructors cannot satisfy smoke/integration. | AG-I09,AG-I10,AG-I18 |
| AG-PO-035 | J-05 is the sole one-level nested dogfood graph: the outer integration controller reserves all inner graph ceilings up front, passes an unforgeable parent reservation/nesting-depth=1 binding, disables publication/merge and further dogfood inside the child, and reconciles/charges the child under the parent lineage. | AG-I12,AG-I16 |
| AG-PO-036 | Every named Go-test validator consumes `go test -json` through the frozen non-vacuity wrapper and PASS requires the exact expected package/test to emit the required positive `run` and `pass` event count with zero fail events; a zero-match Go exit code is validation failure. | AG-I05,AG-I16 |
| AG-PO-037 | Merge authorization is impossible until exact-C branch acceptance, clean exact-head final review, and all required integration cells PASS; C invalidation and merge authorization race through one controller revision/CAS, merge effect revalidates that tip, and post-merge failure records non-completion with rollback only via separately governed change authority. | AG-I13,AG-I17 |
| AG-PO-038 | `GovernanceActivationV2` may install `context-capsule-v4` only when derived from exact existing EP-005 merge authorization + `POST_MERGE_ACCEPTED` evidence for the same repository/base branch/merge-result policy commit and authenticated principal, chained to exact installed `GovernanceActivationV1`; caller-selected unmerged commits/schema versions/paths fail. | AG-I01,AG-I09,AG-I13 |

## Successor activation and grandfather authority

The successor wire contract is frozen now so B cannot invent it:

- new capsule policy constant: `PolicyVersionV4 = "context-capsule-v4"`;
- assurance manifest schema: `AssurancePolicyManifestV1.SchemaVersion = "assurance-policy-v1"`;
- successor activation kind: `GovernanceActivationV2` with `PolicyVersion = "context-capsule-v4"`;
- V1/V2/V3 bytes and historical validators remain unchanged; only exact grandfathered V3 lineages may finish under V3 after V4 activation.

`GovernanceActivationV2` is not a new approver. Its installation authority is `PolicyUpgradeAuthorityV1`, deterministically derived by the controller from the existing EP-005 lifecycle chain for the exact policy commit: repository identity, target protected base branch `main` for this repository, expected pre-merge base OID, exact reviewed PR/head OID, authenticated acting principal, merge method, exact merge-result commit/tree, `MERGE_AUTHORIZED` checkpoint digest, `POST_MERGE_ACCEPTED` checkpoint/evidence digest, and existing provider/policy evidence digests. `GovernanceActivationV2.ActivationRepositoryCommit` must equal that merge-result commit and its tree must equal the post-merge-accepted tree. The installed `GovernanceActivationV1` digest is the mandatory predecessor. A caller may supply a request, but cannot select/override the authorized commit, branch, principal, policy files, schema version, or grandfather set.

The controller reads these exact canonical full paths from the authorized commit and recomputes each SHA-256: `docs/architecture/AUTONOMOUS_EXECUTION_GOVERNANCE.md`, `docs/architecture/CONTEXT_AUTHORITY_AND_CAPSULE_POLICY.md`, `docs/architecture/IMPLEMENTATION_DESIGN_GATE.md`, `docs/architecture/DECISION_AND_ACCEPTANCE_POLICY.md`, and `docs/architecture/ASSURANCE_MODEL_AND_PROOF_OBLIGATION_POLICY.md`. `AssurancePolicyManifestV1` contains those full path+digest entries plus exact authorized commit/tree, repository identity, predecessor activation digest, V4/schema wire constants, lifecycle-authority digest, and acting-principal identity.

Immediately before successor CAS, the controller derives `GrandfatheredV3LineageV1[]` from durable predecessor-policy state at the expected revision. Each entry binds repository/controller identity, accepted A capsule/checkpoint digest, current capsule stage/file digest, checkpoint tip/grant digest when present, exact candidate SHA, predecessor policy version, and issuance/revision identity. The activation record contains exactly that controller-derived nonterminal eligible set. A raced lineage issuance changes the expected revision and forces recomputation. Grandfathered entries may finish already-issued predecessor lineage only; they cannot mint a new A, switch policy, or become V4 parents. Unlisted/post-activation V3 and any caller-selected grandfather entry fail.

## Durable publication, recovery, and cleanup matrix

All new authority artifacts use one protocol: canonical bytes → content-addressed final artifact durably fsynced → one expected-revision backend CAS referencing exact digest. Durable unreferenced artifacts are non-authoritative orphans. No authority depends on deleting them. PostgreSQL transaction commit ambiguity is reconciled by loading exact controller identity/revision and comparing expected digest before any mutation retry.

| Family | Before durable prepare | Prepared/durable / authoritative CAS absent | CAS submitted/response ambiguous | CAS proven applied | Cleanup/recovery rule |
|---|---|---|---|---|---|
| successor activation/manifest | no authority | orphan manifest only | reload PostgreSQL state; no second CAS until exact V2 activation digest/revision resolved | V4 successor active | never delete referenced manifest; conflicting orphan fails closed |
| A model/checkpoint + A→B grant | predecessor A only | orphan only | reconcile exact checkpoint/grant digests and revision | B derivation may use exact bound grant | durable orphan harmless/non-authoritative |
| B convergence + B→C grant | B remains current | orphan only | reconcile exact converged head/review/grant state | replacement C may be derived | no C mint from ambiguous/unreferenced artifact |
| validator resource reservation | no process may start | reservation CAS atomically charges invocation/slots/max output/evidence-byte budget and assigns lineage ordinal | reload reservation digest/ordinal before spawn/retry | exactly one attempt may use reservation | abandoned reservation stays charged at maximum until process-dead + artifact reconciliation CAS proves releasable remainder; invocation/rejected-attempt charge never refunded |
| stage evidence/checkpoint | reservation/attempt only | evidence artifact non-authoritative | reconcile exact evidence digest + reservation + stage revision | selected cell evidence authoritative for exact subject | partial output remains incomplete; stage CAS selects one successful attempt/cell only |
| failed-C record/C invalidation | C remains valid until invalidation CAS | orphan finding packet only | reconcile failed-C digest + invalidated-C state | C invalid; zero mutation authority | no correction grant until exact failed-C state proven |
| correction-B grant/reentry counter | failed C, prior counter | orphan proposed grant only | reconcile atomic `{counter+1, grant_digest}` CAS | one reentry consumed | replay verifies existing grant without increment; no separate reservation |
| integration acceptance | clean C but no merge authority | integration evidence non-authoritative | reconcile integration checkpoint digest/revision | exact C gains integration-passed prerequisite only | integration failure keeps merge unauthorized and returns through frozen failure semantics |
| merge authorization | C+integration required | proposed merge artifact only | reconcile exact merge-authorized tip before provider effect | provider may execute only against exact tip/revision | concurrent C invalidation vs merge authorization uses one CAS; loser reloads and cannot effect |
| post-merge acceptance | merge result exists but completion false | post-merge evidence non-authoritative | reconcile exact result/evidence | project may claim merged-state acceptance | failure records `POST_MERGE_FAILED`; no automatic Git rollback; any revert is a separately governed A/B/C change |
| assurance escape/A-required | current lineage until CAS | orphan escape only | reconcile escape digest + A-required flag | new B/C blocked | only new accepted A lineage clears requirement |

Cancellation/timeout before durable artifact permits bounded temp cleanup. After artifact durability but before CAS, cancellation leaves a harmless orphan and grants zero authority. Once CAS may have been submitted, retry authority is zero until reconciliation proves applied/not-applied state. Backend unavailability produces `RECOVERY_REQUIRED`/`VALIDATION_UNAVAILABLE` and no second mutation. Applied state wins over late cancellation.

## Frozen assurance resource profile

Structural admission maxima per assurance model: 32 dimensions, 128 stable scenarios, 64 invariants, 128 proof obligations, 256 evidence cells, 32 journeys, 256 validator identities; ID ≤128 UTF-8 bytes, claim/rationale/description ≤4096 bytes, each canonical authority record ≤1 MiB.

Lineage-cumulative ceilings from accepted A through post-merge: ≤1024 controller-issued validator invocation ordinals, ≤512 rejected assurance mutation/admission requests, ≤128 external Git/backend observation attempts, ≤2048 retained evidence refs, ≤512 MiB retained assurance evidence bytes, ≤4 concurrently active validator reservations, and exactly 2 correction-B reentries. Candidate/result/subject, B/C generation, process, restart, and nested dogfood cannot reset these counters.

Controller state keeps only aggregate counters, next lineage ordinal, at most four active reservation digests, stage-selected evidence digests, and current authority tips; completed attempt detail stays in content-addressed evidence so the ≤1 MiB controller-state bound does not grow with 1024 attempts.

Before a validator starts, one PostgreSQL-backed controller CAS creates `AssuranceResourceReservationV1` and atomically charges: one invocation ordinal, one active slot, validator maximum stdout+stderr allowance, validator maximum evidence-byte allowance, max descendant PID allowance, and validator timeout allowance. If reservation fails, no subprocess starts. Terminal finalization CAS releases the active slot and unused byte reservation but never refunds invocation/rejected-attempt counts. Crash/unknown process state retains the maximum reservation charge until fresh-process reconciliation proves the process/cgroup empty and classifies every produced artifact; unreferenced durable artifacts are either charged to retained evidence or deleted before byte credit is released. Every external observation and rejected mutation increments its lineage counter in the same controller CAS that authorizes/records the attempt, so competing attempts cannot oversubscribe.

Validator physical ceilings: max 4 concurrent validator processes; each invocation timeout ≤30 minutes; stdout ≤16 MiB and stderr ≤16 MiB; max 64 descendant PIDs; cgroup-v2 `memory.max` ≤2 GiB; `pids.max` ≤64; validator `RLIMIT_NOFILE` ≤256. Required Linux smoke/resource cells are `VALIDATION_UNAVAILABLE` if controls cannot be established. Controller backend CAS contention loop ≤8 observed compare failures per requested mutation; each compare failure is charged as an external/backend observation. Ambiguous commit has zero mutation retry until exact state reconciliation.

J-05 has the sole nesting exception: an outer integration-stage reservation may start exactly one child ABCP graph with `nesting_depth=1`. Before child start, the outer controller reserves the child graph's declared maximum validator invocations/evidence bytes/parallel slots from the parent lineage. Child controller authority carries the parent reservation digest and may only consume that reserved sub-budget; child publication/merge and child dogfood are disabled. No depth >1 is valid. Child crash leaves its maximum sub-budget charged until parent reconciliation.

Existing B `ExecutionBoundsV1` Ralphex invocation/review/mutation/time ceilings remain additional mandatory floors and cannot reset across correction reentries.

## Production workflow-authority backend

This execution pack freezes `PostgresWorkflowAuthorityBackendV1` as the production implementation of `WorkflowAuthorityBackendV1`; B may not substitute host-local files, SQLite, process memory, or an unspecified service. Implementation lives under `internal/authoritybackend/**` and pins `github.com/jackc/pgx/v5 v5.11.0` in `go.mod`/`go.sum`.

The backend uses PostgreSQL table `abcp_workflow_authority_v1(authority_domain text, controller_identity text, revision bigint, canonical_state bytea, state_sha256 char(64), updated_at timestamptz, primary key(authority_domain, controller_identity))`. Bootstrap is an explicit trusted operator/controller operation: create/verify the exact schema/table/constraints, initialize one repository/controller row from an already validated predecessor bootstrap record, and reject conflicting initialization. `AuthorityDomainV1()` returns the configured immutable domain ID, not a DSN. `LoadWorkflowStateV1` reads the exact row. `CompareAndSwapWorkflowStateV1` is one transaction/statement equivalent to `UPDATE ... SET revision=$new, canonical_state=$bytes, state_sha256=$digest WHERE authority_domain=$domain AND controller_identity=$id AND revision=$expected`, requires exactly one row for success, and verifies post-commit revision/digest after any ambiguous connection outcome before retry authority exists.

Assembled CLI/run composition requires trusted operator flags `--authority-backend=postgres --authority-domain=<id> --authority-dsn-file=<root-owned-path>`. The DSN file is outside repository/evidence roots, opened no-follow with restrictive permissions, and its bytes are never logged, hashed into public evidence, inherited by validator children, or accepted from a repository manifest. Evidence records only backend kind, authority-domain ID, PostgreSQL server identity/version, TLS mode, and a secret-free connection-policy digest. Missing/unreadable DSN, wrong domain/schema, unavailable PostgreSQL, or backendless `OpenControllerV1` is fail-closed for durable operations.

Required smoke/integration PostgreSQL is provisioned reproducibly on the Linux/amd64 devagent/CI runner with Docker using official `postgres:17.6-bookworm` platform manifest digest `sha256:45cd22f8d32e189d245403954882f88e7a8714301fda80dab6da90f1265b25a3`. The harness creates an isolated container/network/database, binds PostgreSQL only to loopback, generates an ephemeral password into a root-readable temporary DSN file outside repository/evidence roots, waits for authenticated readiness, and destroys container/network/DSN after reconciliation. Evidence records image digest/server version/container identity and secret-free connection policy, never the password/DSN. Docker/image unavailability is `VALIDATION_UNAVAILABLE`, not permission to substitute an in-memory backend.

J-01/J-02/J-03/J-04 use isolated databases/schemas through this production adapter. J-05 uses a separately isolated database/schema on the same pinned real PostgreSQL engine and proves fresh-process visibility. An in-memory fake cannot satisfy required smoke/integration cells.

## Evidence-class applicability

`unit`, `static`, `contract`, `fault_injection`, `race_concurrency`, `crash_restart`, `resource`, `replay_idempotency`, `security_negative`, `smoke`, `integration`, `migration`, and `e2e` are applicable and have exact cells below. `runtime` and `production` are explicitly **not applicable** because this pack changes/install-tests the CLI/controller but does not deploy a long-running production ABCP service; no runtime/production cell may be pre-satisfied and external-build readiness remains a later separately authorized runtime/deployment claim.


## Validator registry

`${A_ACCEPTED_HEAD}`, `${CANDIDATE_HEAD}`, `${MERGE_RESULT}`, and runtime paths are controller-supplied immutable bindings, never ambient shell input. Process evidence records only the secret-free allowlisted environment policy described below.

`AG-VAL-GO-EXACT` is the frozen non-vacuity runner implemented by `python3 scripts/acceptance/assurance-go-test-exact.py`. Exact-test mode runs `go test -json` itself (no shell pipe), requires the named package and test, requires exactly the requested positive `run` and `pass` event count, zero matching fail events, and Go exit 0; zero-match is failure. Suite mode requires Go exit 0 plus ≥1 positive test event for every `--require-package`. It records the exact Go executable SHA256, `go version`, module graph digest, package/test/count/race request, and parsed event-count artifact.

| Validator ID | Stage | Exact command / authority | Purpose |
|---|---|---|---|
| AG-VAL-B-001 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --suite ./internal/context ./internal/governance ./internal/authority ./internal/authoritybackend/... --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/context --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/governance --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/authority --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/authoritybackend/postgres` | canonical contracts/backend/admission suite non-vacuity |
| AG-VAL-B-002 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceConcurrentReplayAndDerivation --count 10 --race` | concurrent/replay authority |
| AG-VAL-B-003 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssurancePublicationCrashMatrix --count 1` | subprocess kill/reopen at every publication boundary |
| AG-VAL-B-004 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceCancellationAndObservationAmbiguity --count 1` | cancellation/external ambiguity |
| AG-VAL-B-005 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceEvidenceAndStageBinding --count 1` | exact cell/stage/subject/attempt semantics |
| AG-VAL-B-006 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceScopeClassificationAndPlanImmutability --count 1` | docs bypass/frozen A/B paths |
| AG-VAL-B-007 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceResourceReservationAccounting --count 10 --race` | atomic lineage resource ceilings/reservation/recovery |
| AG-VAL-B-008 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceCanonicalUnitSemantics --count 1` | explicit unit evidence |
| AG-VAL-B-009 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/authoritybackend/postgres --test TestPostgresWorkflowAuthorityBackendCAS --count 10 --race` | real PostgreSQL CAS/bootstrap/ambiguity semantics |
| AG-VAL-B-010 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceAuthorityValidityAndLeaseExpiry --count 1` | state expiry + timed lease expiry |
| AG-VAL-C-001 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --suite ./... --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/governance --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/authority --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/run --require-package github.com/pankajleh/autonomous-builder-control-plane/cmd/abcp` | full uncached repository tests with non-vacuity |
| AG-VAL-C-002 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --suite ./... --race --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/governance --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/run` | full race suite with non-vacuity |
| AG-VAL-C-003 | C_BRANCH_ACCEPTANCE | `go vet ./...` | static analysis |
| AG-VAL-C-004 | C_BRANCH_ACCEPTANCE | `git diff --check ${A_ACCEPTED_HEAD}...${CANDIDATE_HEAD}` | exact candidate hygiene |
| AG-VAL-C-005 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./cmd/abcp --test TestAssuranceFreshSessionAuthoritySmoke --count 1` | J-01 real CLI/PostgreSQL fresh-session smoke |
| AG-VAL-C-006 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceFailedCCorrectionReentryIntegration --count 1` | J-02 two reentries + third denial |
| AG-VAL-C-007 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceGapReturnsToAIntegration --count 1` | J-03 model-gap return to A |
| AG-VAL-C-008 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./cmd/abcp --test TestAssuranceDocumentationBypassSmoke --count 1` | J-04 documentation bypass |
| AG-VAL-C-009 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssurancePhysicalResourceAndCrashStress --count 10 --race` | cgroup/fd/process/resource stress |
| AG-VAL-C-010 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceLifecycleOrderingAndInvalidationRace --count 10 --race` | integration-before-merge and C-invalidation CAS race |
| AG-VAL-I-001 | INTEGRATION_ACCEPTANCE | `scripts/acceptance/assurance-governance-dogfood.sh ${A_ACCEPTED_HEAD} ${CANDIDATE_HEAD} ${PARENT_RESOURCE_RESERVATION}` | J-05 sole depth-1 disposable-repo ABCP/Ralphex/Codex dogfood |
| AG-VAL-PM-001 | POST_MERGE_ACCEPTANCE | `scripts/acceptance/assurance-governance-postmerge.sh ${MERGE_RESULT}` | merged-state compatibility + fresh installed-local CLI/PostgreSQL smoke |

## Evidence artifact schema

Before every process validator, the controller publishes `AssuranceResourceReservationV1` by atomic PostgreSQL CAS. It binds accepted-A lineage digest, lineage-global invocation ordinal, attempt ordinal for the cell+subject, predecessor controller revision, cell/proof/stage/subject/validator IDs, reserved output/evidence/PID/time/slot ceilings, parent reservation digest/nesting depth when present, and reservation SHA256. Only that exact reservation may start the process.

Every terminal attempt emits strict-canonical `AssuranceEvidenceCellV1`: `kind`, schema version, repository/controller/authority-domain identity, accepted-A lineage digest, reservation digest, lineage invocation ordinal, cell attempt ordinal, predecessor/final controller revisions, cell/proof/class/stage/validator IDs, exact subject kind/identity, working-directory repository identity+relative path, secret-free environment-policy digest, authenticated acting principal ID, exact argv digest (or library-validator identity), executable/toolchain digests (`abcp`, Go/Python/Git/PostgreSQL client/server as applicable), exact Ralphex binary digest and Codex model/effort/executor identity when applicable, start/end timestamps, exit/outcome, stdout/stderr refs+SHA256, parsed non-vacuity artifact for Go validators, resource artifact when required, and evidence-record SHA256. Unknown/duplicate fields, mismatches, wrong subject/revision/reservation, missing required refs, oversized artifacts, or unfrozen validator identity fail closed.

Environment policy is allowlist-only and secret-free. It may record values such as locale, `PATH` executable identity set, `GOMODCACHE` policy, and explicitly safe ABCP test variables. Secret-bearing names/values (`OPENAI_API_KEY`, tokens, DSNs, passwords, cookies, SSH material, cloud credentials) are never copied to evidence or child environments by generic inheritance. Secret access required by Codex/PostgreSQL is injected through trusted process composition/file descriptors/files outside evidence roots; evidence records only provider/principal identity and a secret-free policy digest.

Multiple attempts are deterministic: each new authorized attempt receives the next controller-issued ordinal and consumes resources. Failed/unavailable attempts never satisfy a cell. A lifecycle stage checkpoint atomically selects exactly one PASS evidence digest for each required cell at the current exact subject/revision; after checkpoint publication, later/replayed attempts cannot replace selection without a new valid stage lineage. Exact replay of already sealed evidence is create-or-verify and consumes no new ordinal only when no subprocess is rerun.

Candidate subjects bind repository identity + exact commit + exact tree + accepted-A lineage digest. Integration subjects additionally bind disposable integration repository identity + source candidate + exact integration commit/tree + parent resource reservation. Post-merge subjects bind repository identity + exact merge commit/tree + source candidate + integration checkpoint digest. Artifact refs are controller-owned content-addressed evidence; output alone is never authority.

Crash/restart cells retain `AssuranceCrashBoundaryEvidenceV1` naming family/boundary/kill point, pre-kill revision/reservation, fresh-process revision, expected/observed digest, orphan digests, and recovery outcome (`APPLIED`, `NOT_APPLIED`, `RECOVERY_BLOCKED`). Resource cells retain `AssuranceResourceEvidenceV1` with reserved/final charged counters plus cgroup memory/PIDs, process/FD counts, output/evidence bytes, elapsed time, concurrency peak, cleanup result, and remaining lineage budget.

## Production composition / fake boundary matrix

| Boundary | Unit/B fake allowed? | Required real boundary |
|---|---|---|
| Git repository/object/ref | parser helpers may fake bytes only | C/J-01 and J-05 use real temporary Git repositories and real Git object/ref commands |
| controller durable authority state/CAS | canonical functions may use fixtures | C integrations use real `PostgresWorkflowAuthorityBackendV1` with isolated database/schema and fresh-process reconnect |
| filesystem durability/path identity | pure parser fixtures only | crash/resource cells use real filesystem/fsync/rename/open identity and fresh process |
| process containment/resource limits | unit mock only for local API behavior | AG-VAL-C-009 uses real Linux cgroup v2 + RLIMIT controls; unavailable is not PASS |
| Ralphex/Codex execution | B may use deterministic helper for local controller unit tests | J-05 uses pinned real Ralphex binary + Codex executor under sole depth-1 reservation; unavailable is `VALIDATION_UNAVAILABLE` |
| PostgreSQL credentials | fakes may test parser rejection | real smoke uses trusted DSN file outside repo/evidence; secret bytes never enter evidence/child generic env |
| GitHub/remote merge provider | not modified by this implementation | successor activation consumes existing merged/post-merge lifecycle evidence; J-05 inner graph disables publication/merge |

## Strict evidence-cell registry

One row is one cell. A cell has exactly one proof ID, one evidence class, one lifecycle stage, one subject rule, and one validator ID.

| Cell ID | Proof | Class | Stage | Subject | Validator |
|---|---|---|---|---|---|
| AG-E-001 | AG-PO-001 | contract | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-001 |
| AG-E-002 | AG-PO-002 | migration | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-001 |
| AG-E-003 | AG-PO-003 | race_concurrency | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-002 |
| AG-E-004 | AG-PO-004 | security_negative | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-001 |
| AG-E-005 | AG-PO-005 | migration | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-001 |
| AG-E-006 | AG-PO-006 | contract | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-006 |
| AG-E-007 | AG-PO-007 | replay_idempotency | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-002 |
| AG-E-008 | AG-PO-008 | security_negative | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-006 |
| AG-E-009 | AG-PO-009 | contract | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-001 |
| AG-E-010 | AG-PO-010 | contract | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-001 |
| AG-E-011 | AG-PO-011 | contract | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-001 |
| AG-E-012 | AG-PO-012 | security_negative | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-006 |
| AG-E-013 | AG-PO-013 | security_negative | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-006 |
| AG-E-014 | AG-PO-014 | contract | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-005 |
| AG-E-015 | AG-PO-015 | smoke | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-005 |
| AG-E-016 | AG-PO-016 | contract | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-001 |
| AG-E-017 | AG-PO-017 | integration | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-006 |
| AG-E-018 | AG-PO-018 | integration | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-006 |
| AG-E-019 | AG-PO-019 | integration | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-006 |
| AG-E-020 | AG-PO-020 | crash_restart | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-003 |
| AG-E-021 | AG-PO-021 | fault_injection | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-004 |
| AG-E-022 | AG-PO-022 | fault_injection | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-004 |
| AG-E-023 | AG-PO-023 | smoke | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-005 |
| AG-E-024 | AG-PO-024 | security_negative | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-009 |
| AG-E-025 | AG-PO-025 | resource | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-009 |
| AG-E-026 | AG-PO-026 | integration | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-007 |
| AG-E-027 | AG-PO-027 | contract | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-005 |
| AG-E-028 | AG-PO-028 | integration | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-005 |
| AG-E-029 | AG-PO-029 | integration | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-006 |
| AG-E-030 | AG-PO-030 | e2e | INTEGRATION_ACCEPTANCE | exact depth-1 disposable integration subject | AG-VAL-I-001 |
| AG-E-031 | AG-PO-020 | crash_restart | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-009 |
| AG-E-032 | AG-PO-025 | static | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-003 |
| AG-E-033 | AG-PO-025 | race_concurrency | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-002 |
| AG-E-034 | AG-PO-006 | static | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-004 |
| AG-E-035 | AG-PO-005 | migration | POST_MERGE_ACCEPTANCE | exact merge-result commit/tree | AG-VAL-PM-001 |
| AG-E-036 | AG-PO-023 | replay_idempotency | POST_MERGE_ACCEPTANCE | exact merge-result commit/tree | AG-VAL-PM-001 |
| AG-E-037 | AG-PO-009 | race_concurrency | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-002 |
| AG-E-038 | AG-PO-010 | race_concurrency | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-002 |
| AG-E-039 | AG-PO-014 | race_concurrency | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-002 |
| AG-E-040 | AG-PO-025 | resource | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-007 |
| AG-E-041 | AG-PO-008 | smoke | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-008 |
| AG-E-042 | AG-PO-004 | unit | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-008 |
| AG-E-043 | AG-PO-031 | contract | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-010 |
| AG-E-044 | AG-PO-032 | resource | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-007 |
| AG-E-045 | AG-PO-033 | replay_idempotency | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-005 |
| AG-E-046 | AG-PO-034 | integration | C_BRANCH_ACCEPTANCE | unchanged exact C candidate + real PostgreSQL backend | AG-VAL-C-005 |
| AG-E-047 | AG-PO-034 | contract | B_IMPLEMENTATION | exact B candidate + PostgreSQL | AG-VAL-B-009 |
| AG-E-048 | AG-PO-035 | e2e | INTEGRATION_ACCEPTANCE | exact parent-reserved depth-1 dogfood subject | AG-VAL-I-001 |
| AG-E-049 | AG-PO-036 | unit | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-008 |
| AG-E-050 | AG-PO-036 | contract | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-001 |
| AG-E-051 | AG-PO-037 | race_concurrency | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-010 |
| AG-E-052 | AG-PO-037 | integration | INTEGRATION_ACCEPTANCE | exact integration subject + unchanged C tip | AG-VAL-I-001 |
| AG-E-053 | AG-PO-038 | security_negative | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-001 |
| AG-E-054 | AG-PO-038 | smoke | C_BRANCH_ACCEPTANCE | unchanged exact C candidate + lifecycle authority | AG-VAL-C-005 |
| AG-E-055 | AG-PO-033 | resource | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-007 |
| AG-E-056 | AG-PO-034 | replay_idempotency | POST_MERGE_ACCEPTANCE | exact merge result + fresh PostgreSQL reconnect | AG-VAL-PM-001 |

Every AG-PO-001..038 has at least one cell. Every registered validator is referenced by ≥1 cell. Required unit/static/contract/fault/race/crash/resource/replay/security/smoke/integration/migration/e2e cells cannot be substituted by another class. Missing/unavailable execution of a sufficient cell is `ASSURANCE_EVIDENCE_INCOMPLETE`; failing validator is an implementation finding; missing/wrong cell/validator/stage/subject/boundary is `ASSURANCE_MODEL_GAP`.

## Critical journeys

**J-01 / AG-VAL-C-005 — Fresh-session authority smoke.** Real temporary Git repository + assembled `abcp` CLI + real `PostgresWorkflowAuthorityBackendV1`. Consume an exact existing merge/post-merge policy-upgrade authority, install/replay exact V4 successor activation, validate A model → A-to-B → B admission → B-to-C. Re-run with changed policy digest, unmerged commit, wrong acting principal/base branch, model digest, repo identity, symlink/path, candidate SHA, schema wire value, and unlisted predecessor V3 lineage; every case fails before mutation.

**J-02 / AG-VAL-C-006 — Failed-C bounded correction integration.** One accepted A: C1 mapped implementation finding → failed-C1 → correction-B1 counter=1. Converge C2 → mapped finding → correction-B2 counter=2. Restart/fresh PostgreSQL connection, converge C3, present third mapped blocker, prove no grant/counter remains 2/return-A. Concurrent/exact replay at each reentry proves no double consumption. Counter consumption is correction-grant CAS, not finding count.

**J-03 / AG-VAL-C-007 — Assurance-gap return-to-A.** Missing scenario, unjustified N/A, wrong cell stage/subject/attempt, missing real boundary, and inadequate proof variants each yield `ASSURANCE_MODEL_GAP`, zero correction grant/reentry consumption, A-required state.

**J-04 / AG-VAL-C-008 — Documentation bypass smoke.** Controller-bound docs-only classification over a truly non-authoritative doc passes; candidate then touches governance/runtime bytes and exact-diff revalidation invalidates exemption before acceptance. Cell AG-E-041 makes this smoke mandatory.

**J-05 / AG-VAL-I-001 — Real one-level dogfood E2E.** Outer integration controller reserves exact child sub-budget under parent lineage and launches depth=1 child only. Build exact candidate `abcp`; use real Git/filesystem/PostgreSQL/cgroup and pinned Ralphex+Codex on disposable toy repository for non-trivial code change. Execute child A→B→C but disable PR publication/merge and further dogfood. Child evidence/counters reconcile into parent reservation. Any boundary unavailable is `VALIDATION_UNAVAILABLE`; narration/in-process fake cannot satisfy.

## Functional-plan mutation and B convergence

The six-task functional plan references frozen AG-PO IDs. B may mark task checkboxes/status only after corresponding validators pass and may perform final semantic-byte-preserving move to `docs/plans/completed/assurance-proof-obligation-governance-enforcement.md`. A/B/C validator rejects any B diff that semantically changes task scope, proof bindings, non-goals, or this assurance model.

Lifecycle ordering is frozen: `DESIGN_ACCEPTED → B IMPLEMENTATION_CONVERGED → C BRANCH_ACCEPTED → C FINAL_REVIEW_CLEAN → required INTEGRATION_ACCEPTED → MERGE_AUTHORIZED → merge provider effect → POST_MERGE_ACCEPTED`. PR publication may occur after `FINAL_REVIEW_CLEAN` before integration, but publication grants no merge authority. `MERGE_AUTHORIZED` CAS requires current unchanged C tip/review digest plus exact integration checkpoint digest and zero invalidation. A concurrent failed-C/assurance-gap invalidation and merge authorization compete on one PostgreSQL controller revision; only one CAS wins. Provider effect revalidates exact merge-authorized revision/tip immediately before mutation, so a later invalidation/revocation blocks effect.

Integration failure leaves C unmerged and merge unauthorized; an implementation failure returns through bounded correction-B only if the frozen model is sufficient, while an assurance gap returns A. After actual merge, post-merge acceptance failure records `POST_MERGE_FAILED`/project incomplete and blocks external-build readiness. No automatic rollback authority exists: any revert/repair is a separately designed/governed change, preserving the actual Git result as evidence.

C exact-head final review is read-only and classifies every blocker as frozen-obligation violation or `ASSURANCE_MODEL_GAP`. Repository publication/merge requires unchanged exact candidate, all B/C cells PASS, exact-head Critical=0/Major=0, clean repository, unexhausted lineage ceilings, and required integration PASS before merge authorization. External-build readiness is not claimed by repository merge alone.

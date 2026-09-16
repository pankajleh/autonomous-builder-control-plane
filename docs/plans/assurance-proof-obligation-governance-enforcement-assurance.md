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
- `internal/mergelifecycle/**`
- `cmd/abcp/**`
- `scripts/acceptance/assurance-governance-dogfood.sh`
- `scripts/acceptance/assurance-governance-postmerge.sh`
- `scripts/acceptance/assurance-go-test-exact.py`
- `scripts/acceptance/assurance-graph-validate.py`
- `scripts/acceptance/assurance-lifecycle-integration.sh`
- `scripts/acceptance/assurance-runner-preflight.sh`
- `scripts/acceptance/assurance-secret-scan.py`
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
| AG-I19 | Credential-bearing execution is partitioned from retained evidence: secret-capable children cannot expose credential bytes to tool subprocesses or retained raw output, and exact secret-value scanning gates every stage that used credentials. |
| AG-I20 | V4 lifecycle side effects linearize through explicit controller-owned stage/effect tips; merge submission is authorized only by one unconsumed `MergeEffectLeaseV1`, so invalidation and external effect cannot both win the same pre-submission state. |

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
| AG-S-045 | lifecycle ordering | integration fails or C invalidates concurrently with merge authorization/publication/post-merge | AG-I13,AG-I17,AG-I20 | AG-PO-016,AG-PO-017,AG-PO-037,AG-PO-043 |
| AG-S-046 | secret egress | credential-bearing Codex/PostgreSQL execution prints, copies, or exposes credential bytes to a tool subprocess, diff, or retained evidence | AG-I11,AG-I19 | AG-PO-041 |
| AG-S-047 | merge effect race | C invalidation races the last controller decision before the provider merge effect, or the provider result is ambiguous after lease issuance | AG-I10,AG-I17,AG-I20 | AG-PO-020,AG-PO-037,AG-PO-043 |
| AG-S-048 | compatibility/version skew | a grandfathered V3 C fails after V4 activation and attempts to mint a V4 correction descendant | AG-I01,AG-I08,AG-I09 | AG-PO-002,AG-PO-005,AG-PO-042 |
| AG-S-049 | structural assurance | syntactically valid registries contain semantically disconnected or duplicate scenario→invariant→proof→cell→validator links | AG-I02,AG-I05 | AG-PO-006,AG-PO-040 |
| AG-S-050 | dogfood delegated authority | a depth-1 child activation escapes its parent reservation/repository/budget or attempts publication, merge, or nested dogfood | AG-I09,AG-I12,AG-I16 | AG-PO-035,AG-PO-044 |

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
| AG-PO-039 | Repository static hygiene is independently mandatory: exact candidate diff has no whitespace/error markers and `go vet ./...` succeeds, but these checks satisfy only this static-hygiene claim and never substitute for resource, graph, lifecycle, or integration evidence. | AG-I05 |
| AG-PO-040 | The assurance graph validator parses the frozen registries, rejects duplicate/undefined/semantically disconnected IDs, proves every scenario invariant is protected by at least one listed proof, every proof has evidence, every validator is used, and J-01..J-05 each have an AG-PO-029 cell. | AG-I02,AG-I05 |
| AG-PO-041 | Credential-bearing execution satisfies the frozen secret-egress partition: secret bytes are unavailable to Codex tool subprocesses, raw output from secret-capable processes is never retained as assurance evidence, and exact credential-value scanning of candidate diff plus retained evidence returns zero matches before PASS. | AG-I11,AG-I19 |
| AG-PO-042 | Every successor wire record has the exact frozen canonical schema/digest preimage below; `ControllerStateV1` bytes are retained as immutable predecessor evidence and one atomic V1→V2 state-upgrade CAS makes strict `ControllerStateV2` the sole mutable state without reinterpretation. Grandfathered V3 may only finish its already-issued normal path; any V3 C failure after V4 activation becomes `A_REQUIRED` and cannot mint a V4 correction child. | AG-I01,AG-I08,AG-I09,AG-I10 |
| AG-PO-043 | V4 lifecycle follows the exact transition graph below; integration precedes PR publication, and the external merge call is impossible until an atomic one-use `MergeEffectLeaseV1` CAS changes the exact lineage tip from `MERGE_AUTHORIZED` to `MERGE_SUBMITTING`. Invalidation and lease issuance are mutually exclusive at that tip; ambiguous submission is reconciliation-only. | AG-I10,AG-I13,AG-I17,AG-I20 |
| AG-PO-044 | J-05 is a deterministic executable fixture with frozen initial files/task/black-box result, exact parent-reserved child ceilings, pinned Ralphex/Codex identities, and parent-issued `DOGFOOD_DELEGATED` V4 activation constrained to one child repository at depth 1 with publication/merge/dogfood disabled. | AG-I09,AG-I12,AG-I16 |

## Canonical successor wire contracts and validity predicates

All records in this section use **strict canonical JSON**. The parser rejects duplicate or unknown fields, invalid UTF-8, floats where an integer is specified, non-canonical timestamps, non-lowercase SHA-256 hex, and fields outside their declared cardinality. Object field serialization order is exactly the order listed below. Set arrays are sorted bytewise by their stable ID/path key and reject duplicates; semantic-order arrays retain declared order. Every record digest is SHA-256 over the UTF-8 canonical JSON of exactly its listed fields with its own terminal `*_sha256` field **absent**, never present as an empty string. The sealed form appends that digest as the final field. Optional (`?`) fields are omitted rather than encoded as null.

Primitive notation is frozen: `str` = bounded UTF-8 string, `sha256` = 64 lowercase hex, `oid` = repository-native full Git OID, `u64` = JSON non-negative integer, `bool` = JSON boolean, `ts` = UTC RFC3339 seconds, `[]T(set:key)` = bytewise sorted unique set, and `[]T(order)` = semantic order.

| Record / schema | Exact field order before self digest |
|---|---|
| `PolicyUpgradeAuthorityV1` / `policy-upgrade-authority-v1` | `kind:str, schema_version:str, repository_identity:sha256, base_branch:str, expected_premerge_base_oid:oid, reviewed_head_oid:oid, merge_result_oid:oid, merge_result_tree_oid:oid, merge_method:str, acting_principal:str, merge_authorized_checkpoint_sha256:sha256, post_merge_accepted_checkpoint_sha256:sha256, provider_evidence_sha256:sha256, policy_evidence_sha256:sha256, predecessor_activation_sha256:sha256` |
| `AssurancePolicyManifestV1` / `assurance-policy-v1` | `kind, schema_version, repository_identity, activation_repository_commit:oid, activation_repository_tree:oid, predecessor_activation_sha256, lifecycle_authority_sha256, acting_principal, capsule_policy_version:str, assurance_schema_version:str, policy_files:[]PolicyFileV1(set:path)` where `PolicyFileV1={path:str,sha256:sha256}` |
| `GrandfatheredV3LineageV1` | `repository_identity, controller_identity:sha256, accepted_a_capsule_sha256, accepted_a_checkpoint_sha256, stage:str, capsule_file_sha256, checkpoint_tip_sha256?:sha256, next_stage_grant_sha256?:sha256, candidate_oid:oid, policy_version:str, issuance_sequence:u64, source_state_revision:u64` |
| `GovernanceActivationV2` / `governance-activation-v2` | `kind, schema_version, policy_version="context-capsule-v4", predecessor_activation_sha256, policy_manifest_sha256, policy_upgrade_authority_sha256, activation_repository_commit:oid, activation_repository_tree:oid, activation_sequence:u64, grandfathered_v3:[]GrandfatheredV3LineageV1(set:accepted_a_capsule_sha256)` |
| `AssuranceModelV1` / `assurance-model-v1` | `kind, schema_version, repository_identity, accepted_a_head:oid, policy_manifest_sha256, failure_scenarios:[]ScenarioV1(set:id), invariants:[]InvariantV1(set:id), proof_obligations:[]ProofV1(set:id), validators:[]ValidatorV1(set:id), evidence_cells:[]CellV1(set:id), journeys:[]JourneyV1(set:id), resource_profile_sha256, correction_reentry_limit:u64, allowed_b_paths:[]str(set:value)`; the nested records contain exactly the columns of the frozen registries below plus their bounded description/command/subject strings. |
| `IntegrationFailureV1` / `integration-failure-v1` | `kind, schema_version, accepted_a_lineage_sha256, c_stage_tip_sha256, integration_subject_sha256, integration_validator_id:str, classification:str(IMPLEMENTATION_FINDING|ASSURANCE_MODEL_GAP), finding_ids:[]str(set:value), evidence_sha256, created_sequence:u64` |
| `FailedStageV1` / `failed-stage-v1` | `kind, schema_version, accepted_a_lineage_sha256, stage:str, candidate_oid:oid, candidate_tree_oid:oid, stage_tip_sha256, classification:str(IMPLEMENTATION_FINDING|ASSURANCE_MODEL_GAP), finding_ids:[]str(set:value), correction_paths:[]str(set:value), evidence_sha256, integration_failure_sha256?:sha256, invalidated_tip_sha256, created_sequence:u64`; `integration_failure_sha256` is present iff `stage=INTEGRATION_ACCEPTANCE` and must bind the exact `IntegrationFailureV1`. |
| `CorrectionBGrantV1` / `correction-b-grant-v1` | `kind, schema_version, accepted_a_lineage_sha256, failed_stage_sha256, prior_c_tip_sha256, correction_generation:u64, correction_reentry_ordinal:u64, finding_ids:[]str(set:value), allowed_paths:[]str(set:value), execution_bounds_sha256, remaining_resource_budget_sha256` |
| `AssuranceResourceReservationV1` / `assurance-resource-reservation-v1` | `kind, schema_version, accepted_a_lineage_sha256, invocation_ordinal:u64, cell_attempt_ordinal:u64, cell_id:str, proof_id:str, stage:str, subject_sha256, validator_id:str, resource_profile_id:str, predecessor_stage_tip_sha256, reserved_elapsed_ms:u64, reserved_output_bytes:u64, reserved_evidence_bytes:u64, reserved_transient_bytes:u64, reserved_pids:u64, parent_reservation_sha256?:sha256, nesting_depth:u64` |
| `AssuranceEvidenceCellV1` / `assurance-evidence-cell-v1` | `kind, schema_version, accepted_a_lineage_sha256, reservation_sha256, invocation_ordinal, cell_attempt_ordinal, cell_id, proof_id, evidence_class:str, stage:str, subject_sha256, validator_id, execution_identity_sha256, started_at:ts, ended_at:ts, outcome:str(PASS|FAIL|VALIDATION_UNAVAILABLE), structured_result_sha256, stdout_sha256?:sha256, stderr_sha256?:sha256, resource_evidence_sha256?:sha256, secret_scan_sha256?:sha256` |
| `StageEvidenceSelectionV1` / `stage-evidence-selection-v1` | `kind, schema_version, accepted_a_lineage_sha256, stage, subject_sha256, predecessor_stage_tip_sha256, selected_cells:[]SelectedCellV1(set:cell_id)` with `SelectedCellV1={cell_id,evidence_sha256,reservation_sha256}`, `remaining_budget_sha256` |
| `DogfoodChildActivationV1` / `dogfood-child-activation-v1` | `kind, schema_version, activation_mode="DOGFOOD_DELEGATED", parent_lineage_sha256, parent_integration_tip_sha256, parent_reservation_sha256, child_repository_identity, child_baseline_tree_oid:oid, policy_manifest_sha256, assurance_model_sha256, nesting_depth=1, child_resource_budget_sha256, allowed_operations:[]str(set:value)`; `allowed_operations` must equal `{A_DESIGN,B_IMPLEMENTATION,C_BRANCH_ACCEPTANCE}`. |
| `MergeEffectLeaseV1` / `merge-effect-lease-v1` | `kind, schema_version, accepted_a_lineage_sha256, candidate_oid, candidate_tree_oid, integration_tip_sha256, pr_published_tip_sha256, merge_authorized_tip_sha256, provider_attempt_sha256, effect_ordinal:u64, effect_state="MERGE_SUBMITTING", issued_sequence:u64, consumed:bool=false` |
| `ControllerStateV2` / `governance-controller-state-v2` | `kind, schema_version, controller_identity, repository_identity, revision:u64, predecessor_state_v1_artifact_sha256, predecessor_state_v1_revision:u64, activation_v2_sha256?:sha256, active_lineage_sha256?:sha256, stage_generation:u64, active_stage:str, active_stage_tip_sha256?:sha256, invalidated_stage_tip_sha256?:sha256, a_required:bool, correction_reentries_used:u64, active_resource_reservations:[]sha256(set:value), selected_stage_evidence_sha256?:sha256, merge_effect_lease_sha256?:sha256, merge_effect_state:str(NONE|MERGE_SUBMITTING|APPLIED|NOT_APPLIED|RECOVERY_REQUIRED), cumulative_resource_counters:ResourceCountersV1, grandfathered_v3:[]GrandfatheredV3LineageV1(set:accepted_a_capsule_sha256)` |

Nested registry/resource schemas are equally frozen; no implementation may infer fields from Markdown columns:

| Nested record | Exact field order |
|---|---|
| `ScenarioV1` | `id:str, dimension:str, scenario:str, invariant_ids:[]str(set:value), proof_ids:[]str(set:value)` |
| `InvariantV1` | `id:str, claim:str` |
| `ProofV1` | `id:str, claim:str, invariant_ids:[]str(set:value)` |
| `ValidatorV1` | `id:str, stage:str, command:str, purpose:str` |
| `CellV1` | `id:str, proof_id:str, evidence_class:str, stage:str, subject_rule:str, validator_id:str` |
| `JourneyV1` | `id:str, validator_id:str, title:str, subject_rule:str, required_boundary_ids:[]str(set:value)` |
| `ResourceCountersV1` | `validator_invocations:u64, rejected_assurance_attempts:u64, external_observations:u64, retained_evidence_refs:u64, retained_evidence_bytes:u64, transient_bytes:u64, elapsed_ms:u64, active_validator_reservations:u64` |
| `SelectedCellV1` | `cell_id:str, evidence_sha256:sha256, reservation_sha256:sha256` |
| `PolicyFileV1` | `path:str, sha256:sha256` |

The `AssuranceModelV1` digest therefore commits these exact nested shapes as well as all nested values. `CellV1.stage` is one closed lifecycle-stage token only; journey identity belongs in `JourneyV1.id`/the subject rule and is never concatenated into a stage token.

`ControllerStateV1` is never parsed as V2. Upgrade first stores the exact validated V1 canonical bytes as a content-addressed immutable artifact, then one expected-revision PostgreSQL CAS replaces the mutable row with `ControllerStateV2` whose `predecessor_state_v1_artifact_sha256` and revision exactly identify those bytes. Exact replay verifies the same V2 bytes. Any competing/mismatching upgrade fails closed. After the CAS, only V2 is mutable; historical V1 validators continue to validate the retained V1 artifact under the old schema.

### Executable validity predicates

`HistoricalValid(record)` means its strict canonical bytes/digest and its complete ancestor chain validate under the schema that created it. Historical validity is immutable and is never revoked by later controller revisions. `OperationallyUsable(record,state,operation)` is a separate pure predicate and **never compares record creation revision to the current global revision**.

| Record class | `OperationallyUsable` iff |
|---|---|
| A/B/C stage grant or checkpoint | `HistoricalValid`; same repository/controller/accepted-A lineage; record generation equals `state.stage_generation`; its digest equals the exact `active_stage_tip_sha256` or required predecessor tip for the requested transition; `a_required=false`; it is not `invalidated_stage_tip_sha256`; requested operation belongs to that stage. Resource/evidence CAS revision advances do not change this result. |
| resource reservation | its digest is in `active_resource_reservations`, subject/stage generation still match, budget remains charged, and it is not finalized/consumed. |
| selected stage evidence | digest equals `selected_stage_evidence_sha256`, exact subject and stage generation match, and every selected cell is historically valid PASS evidence. |
| correction-B grant | failed-stage record is historical-valid implementation finding, `correction_reentry_ordinal<=2`, grant digest is active stage tip for the next generation, and A-required is false. |
| failed-stage / integration-failure record | historical evidence only: strict record and bound evidence are `HistoricalValid`; it never authorizes mutation by itself. Only the controller CAS that invalidates the stage and (if permitted) later publishes `CorrectionBGrantV1` creates operational state. |
| time-bounded execution/mutation lease | all exact lineage/tip predicates above **and** the pre-existing frozen expiry has not elapsed; expiry affects operational use only. |
| `MergeEffectLeaseV1` | digest equals state lease tip, state is `MERGE_SUBMITTING`, `consumed=false`, exact candidate/integration/PR/merge-authorized tips match. Once the provider call may have been submitted it becomes reconciliation authority, not revocable pre-submit authority. |
| grandfathered V3 | exact activation grandfather entry matches the already-issued V3 lineage and its permitted next V3 transition. It can never parent V4. If a grandfathered V3 C obtains a blocking failure after V4 activation, controller atomically marks it terminal `A_REQUIRED`; no correction-B descendant is permitted and repair begins a new V4 A. |

Only explicit stage transition/invalidation/effect CAS changes `stage_generation`, active stage/tip, invalidation, A-required, or merge-effect state. Validator reservation/finalization, evidence retention, external-observation accounting, and unrelated counters may advance `ControllerStateV2.revision` but cannot invalidate otherwise usable stage authority.

## Successor activation and grandfather authority

`PolicyVersionV4 = "context-capsule-v4"`. `GovernanceActivationV2` is installed only from `PolicyUpgradeAuthorityV1` derived by the controller from the exact existing EP-005 `MERGE_AUTHORIZED` plus `POST_MERGE_ACCEPTED` chain for this repository's protected `main` branch. The activation commit/tree/principal/merge evidence are therefore controller-derived rather than caller-selected. The controller recomputes the five canonical architecture-policy file digests from that exact merge-result commit and seals `AssurancePolicyManifestV1`.

Immediately before the activation CAS, the controller derives the exact sorted `GrandfatheredV3LineageV1[]` from nonterminal V3 lineages in the predecessor state. A concurrent issuance changes expected revision and forces recomputation. Grandfathered V3 may execute only the already-issued V3 transitions represented by its entry. It cannot mint a new A, become a V4 parent, switch policy, or create a V4 correction descendant. In particular, a blocking C failure after V4 activation terminates that grandfather entry as `A_REQUIRED`; remediation starts a new V4 A.

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
| merge effect lease/submission | no provider call | durable lease artifact alone is non-authoritative | atomic CAS from exact `MERGE_AUTHORIZED` tip publishes lease + `MERGE_SUBMITTING`; if response is ambiguous reload exact lease/tip before any action | provider call may occur at most once under exact lease; after process loss or possible call submission, reconciliation is read-only | after lease issuance a crash is treated as possibly submitted; provider mutation retry is forbidden until exact `NOT_APPLIED` reconciliation and separately governed new effect authority |
| post-merge acceptance | merge result exists but completion false | post-merge evidence non-authoritative | reconcile exact result/evidence | project may claim merged-state acceptance | failure records `POST_MERGE_FAILED`; no automatic Git rollback; any revert is a separately governed A/B/C change |
| assurance escape/A-required | current lineage until CAS | orphan escape only | reconcile escape digest + A-required flag | new B/C blocked | only new accepted A lineage clears requirement |

Cancellation/timeout before durable artifact permits bounded temp cleanup. After artifact durability but before CAS, cancellation leaves a harmless orphan and grants zero authority. Once CAS may have been submitted, retry authority is zero until reconciliation proves applied/not-applied state. Backend unavailability produces `RECOVERY_REQUIRED`/`VALIDATION_UNAVAILABLE` and no second mutation. Applied state wins over late cancellation.

## Frozen assurance resource profile

Structural admission maxima per assurance model remain: 32 dimensions, 128 stable scenarios, 64 invariants, 128 proof obligations, 256 evidence cells, 32 journeys, 256 validator identities; ID ≤128 UTF-8 bytes, claim/rationale/description ≤4096 bytes, each canonical authority record ≤1 MiB.

Lineage-cumulative ceilings from accepted A through post-merge are: ≤1024 validator invocation ordinals, ≤512 rejected assurance mutation/admission attempts, ≤128 external Git/backend observations, ≤2048 retained evidence refs, ≤512 MiB retained assurance evidence, ≤8 GiB cumulative transient workspace/cache materialization, ≤24 hours aggregate validator elapsed time, ≤4 concurrently active validator reservations, and exactly 2 correction-B grants. Candidate/result/subject, B/C generation, process restart, and child dogfood cannot reset them. Reservation CAS atomically charges the maximum elapsed/output/evidence/transient/PID/concurrency allowance before execution; finalization may release only unused byte/concurrency reservation, never invocation/rejected-attempt/elapsed already consumed. Ambiguous cleanup keeps the maximum charged until reconciliation proves release safe.

Three resource profiles are frozen:

| Profile | memory.max | pids.max | cpu.max | RLIMIT_NOFILE | wall time | stdout/stderr retained | evidence max | transient scratch/cache max |
|---|---:|---:|---|---:|---:|---:|---:|---:|
| `NORMAL_V1` | 2 GiB | 64 | `200000 100000` (2 CPUs) | 256 | 30 min | 16 MiB each for non-secret-capable validators | 32 MiB | 512 MiB |
| `SECRET_CAPABLE_V1` | 2 GiB | 64 | `200000 100000` | 256 | 30 min | **0 raw bytes retained**; structured controller result only | 32 MiB | 512 MiB |
| `DOGFOOD_V1` | 4 GiB | 128 | `400000 100000` (4 CPUs) | 512 | 90 min | **0 raw bytes retained from credential-bearing Ralphex/Codex** | 128 MiB | 2 GiB |

`AssuranceResourceReservationV1.resource_profile_id` selects one exact row. All non-dogfood validators run inside a controller-created child cgroup under `/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice`. Preflight proves `cpu,memory,pids` are available; it may enable `+cpu` in the parent only after recording the prior subtree-control value and restores only the controller-added bit after all owned children are empty/removed. It writes and reads back `cpu.max`, `memory.max`, and `pids.max`, applies `prlimit --nofile`, and refuses execution if any limit cannot be established. `systemd-run` is not an authority or dependency.

Non-mutating validators use `/usr/bin/bwrap`: source and module cache are read-only, working/tmp/GOCACHE are bounded tmpfs inside the validator cgroup. A controlled dependency-prefetch step may populate a content-addressed cache only from exact `go.sum`/module-graph identity before evidence execution; otherwise network/cache need returns `VALIDATION_UNAVAILABLE`. No validator may write an unbounded host `GOCACHE`, `GOMODCACHE`, workspace, Docker log, or temp directory.

`J-05` is the sole depth-1 exception. The outer controller reserves the complete `DOGFOOD_V1` child allowance in advance and child counters debit the parent lineage. No depth >1, child publication/merge, or child dogfood is valid. Child crash leaves its maximum reserved until parent reconciliation proves its cgroup/processes/tmpfs/container absent.

The exact runner preflight is `scripts/acceptance/assurance-runner-preflight.sh`. PASS requires Docker server 29.7.2 or a byte-identical pre-approved capability record with cgroup v2/overlayfs semantics, writable delegated cgroup controls above, `/usr/bin/bwrap`, `/usr/bin/prlimit`, `/usr/bin/unshare`, exact pinned Ralphex/Codex identities for J-05, exact PostgreSQL image digest, and sufficient remaining lineage budgets. Missing capability is `VALIDATION_UNAVAILABLE`, never fallback authority.

PostgreSQL smoke/integration uses only `postgres@sha256:f3bd19c606e442c3d7bdfa8002e03fe260a1023351e0ea4598032022b68dd6e3` (verified linux/amd64 `postgres:17.6-bookworm`). Container creation uses a reservation-derived unique name and labels carrying reservation/lineage/image digests plus: `--read-only`, tmpfs `/var/lib/postgresql/data:rw,noexec,nosuid,size=512m` and `/run/postgresql:rw,noexec,nosuid,size=64m`, `--memory=1g --memory-swap=1g --pids-limit=128 --cpus=2 --ulimit nofile=256:256 --shm-size=64m --log-driver=none`, and a loopback-only published ephemeral port. After create ambiguity the harness may not create again until `docker inspect <exact-name>` proves absent or proves one container with the exact image digest and reservation labels; a conflicting object is `RECOVERY_REQUIRED`. Cleanup waits for removal and verifies the exact name/container ID absent before resource credit is released.

Existing B `ExecutionBoundsV1` Ralphex invocation/review/mutation/time ceilings remain additional mandatory floors and cannot reset across correction reentries.

## Production workflow-authority backend

This pack freezes `PostgresWorkflowAuthorityBackendV1` under `internal/authoritybackend/**` and pins `github.com/jackc/pgx/v5 v5.7.6` in `go.mod`/`go.sum`; v5.7.6 declares Go 1.23 and therefore preserves the repository's `go 1.23` contract. Host-local files, SQLite, memory, or an unspecified service cannot satisfy durable V4 operations.

The exact schema is created only by the bootstrap role:

```sql
CREATE TABLE abcp_authority_domain_v1 (
  authority_domain text PRIMARY KEY CHECK (length(authority_domain) BETWEEN 1 AND 128),
  runtime_role name NOT NULL UNIQUE,
  bootstrap_provenance_sha256 char(64) NOT NULL CHECK (bootstrap_provenance_sha256 ~ '^[0-9a-f]{64}$')
);
CREATE TABLE abcp_workflow_authority_v1 (
  authority_domain text NOT NULL REFERENCES abcp_authority_domain_v1(authority_domain) ON DELETE RESTRICT,
  controller_identity char(64) NOT NULL CHECK (controller_identity ~ '^[0-9a-f]{64}$'),
  revision bigint NOT NULL CHECK (revision > 0),
  canonical_state bytea NOT NULL CHECK (octet_length(canonical_state) BETWEEN 1 AND 1048576),
  state_sha256 char(64) NOT NULL CHECK (state_sha256 ~ '^[0-9a-f]{64}$'),
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (authority_domain, controller_identity)
);
```

`abcp governance-backend-bootstrap --repository <repo> --authority-domain <id> --bootstrap-dsn-file <path> --runtime-role <role> --predecessor-state <ControllerStateV1-artifact>` is the only bootstrap operation. It validates repository/controller identity, strict predecessor V1 bytes/digest, and bootstrap provenance; in one transaction it creates/verifies the exact schema, binds `authority_domain→runtime_role`, and inserts the initial row at revision 1. Concurrent exact bootstrap is idempotent only when schema, domain binding, controller identity, canonical bytes and digest all match; any difference fails. The bootstrap role owns DDL and INSERT and is not usable by ordinary ABCP execution. The runtime role receives only CONNECT plus SELECT/UPDATE on these exact rows/tables; it cannot CREATE/ALTER/DROP/INSERT/DELETE/GRANT. Every runtime connection verifies `current_user` equals the domain's recorded runtime role before loading authority.

`LoadWorkflowStateV1/V2` selects the exact `(authority_domain,controller_identity)` row and independently computes `sha256(canonical_state)` in application code; mismatch with `state_sha256`, row revision vs decoded state revision, role/domain binding, strict state schema, or repository/controller identity fails closed. CAS is one transaction with `SET LOCAL lock_timeout='2s'; SET LOCAL statement_timeout='10s'` and exactly one `UPDATE ... SET revision=$new,canonical_state=$bytes,state_sha256=$sha,updated_at=clock_timestamp() WHERE authority_domain=$domain AND controller_identity=$id AND revision=$expected`; success requires one row and a read-back of exact revision/digest. Ambiguous commit forbids another mutation until fresh connection read-back proves the exact proposed revision/digest applied or exact expected revision still present; any third state is `RECOVERY_REQUIRED`.

Connection policy is frozen: connect timeout 5s; operation context 15s; transaction timeout 15s; `lock_timeout=2s`; `statement_timeout=10s`; `idle_in_transaction_session_timeout=15s`; pool `MaxConns=4, MinConns=0, MaxConnLifetime=30m, MaxConnIdleTime=5m, HealthCheckPeriod=30s`. Test harness uses loopback-only PostgreSQL with SCRAM password and `sslmode=disable`; this profile is invalid off loopback. Operator/external PostgreSQL requires TLS `verify-full` plus trusted CA and hostname. Cleartext/non-loopback disable/`prefer`/`require` without verification are invalid.

Common CLI backend composition owns `--authority-backend=postgres --authority-domain=<id> --authority-dsn-file=<no-follow-owner-only-path>` and is mandatory for these exact existing durable commands: `run`, `governance-usage-validate` when it opens controller state, `governance-checkpoint-validate`, `governance-review-advance`, `governance-lease-issue`, `governance-lease-begin`, `governance-receipt-validate`, and `governance-activation-install`. This pack freezes these exact new durable command names: `governance-backend-bootstrap`, `governance-state-upgrade`, `assurance-evidence-select`, `assurance-failed-stage-record`, `assurance-correction-grant`, `assurance-integration-advance`, `assurance-pr-publish`, `assurance-merge-authorize`, `assurance-merge-effect-lease`, `assurance-merge-reconcile`, and `assurance-post-merge-advance`. Every one except bootstrap uses the runtime PostgreSQL role/common composition; bootstrap uses only `--bootstrap-dsn-file` and never runtime credentials. Pure parse-only validators may remain backendless. A durable command reaching controller mutation without this composition is an error, not a local fallback.

The DSN/credentials reside outside repository/evidence roots and are not accepted from repository manifests. Evidence contains only backend kind, authority-domain, current role identity, server version/TLS profile, and secret-free connection-policy digest.

## Evidence-class applicability

`unit`, `static`, `contract`, `fault_injection`, `race_concurrency`, `crash_restart`, `resource`, `replay_idempotency`, `security_negative`, `smoke`, `integration`, `migration`, and `e2e` are applicable and have exact cells below. `runtime` and `production` are explicitly **not applicable** because this pack changes/install-tests the CLI/controller but does not deploy a long-running production ABCP service; no runtime/production cell may be pre-satisfied and external-build readiness remains a later separately authorized runtime/deployment claim.


## Validator registry

`${A_ACCEPTED_HEAD}`, `${CANDIDATE_HEAD}`, `${MERGE_RESULT}` and runtime paths are controller-supplied immutable bindings. Every Go-test invocation goes through `scripts/acceptance/assurance-go-test-exact.py`. The wrapper requires an explicit `--count` for **both exact and suite modes**, always forwards `-count=N`, and rejects `N<1`; C/B suites use `--count 1`. It parses `go test -json`, rejects any package-level `(cached)` indication, requires Go exit 0 and positive `run/pass` evidence for each required package/test, and fails zero-match. No cached result is acceptance evidence.

| Validator ID | Stage | Exact command / authority | Purpose |
|---|---|---|---|
| AG-VAL-B-001 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --suite ./internal/context ./internal/governance ./internal/authority ./internal/authoritybackend/... ./internal/mergelifecycle/... --count 1 --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/context --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/governance --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/authority --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/authoritybackend/postgres --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/mergelifecycle` | uncached contracts/backend suite |
| AG-VAL-B-002 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceConcurrentReplayAndDerivation --count 10 --race` | concurrent/replay authority |
| AG-VAL-B-003 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssurancePublicationCrashMatrix --count 1` | publication crash matrix |
| AG-VAL-B-004 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceCancellationAndObservationAmbiguity --count 1` | cancellation/ambiguity |
| AG-VAL-B-005 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceEvidenceAndStageBinding --count 1` | evidence/selection semantics |
| AG-VAL-B-006 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceScopeClassificationAndPlanImmutability --count 1` | docs bypass/frozen A |
| AG-VAL-B-007 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceResourceReservationAccounting --count 10 --race` | atomic resource accounting |
| AG-VAL-B-008 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceCanonicalUnitSemantics --count 1` | unit evidence |
| AG-VAL-B-009 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/authoritybackend/postgres --test TestPostgresWorkflowAuthorityBackendCAS --count 10 --race` | PostgreSQL bootstrap/CAS/ambiguity |
| AG-VAL-B-010 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceOperationalValidity --count 1` | historical vs operational validity |
| AG-VAL-B-011 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-graph-validate.py --model docs/plans/assurance-proof-obligation-governance-enforcement-assurance.md` | semantic graph closure |
| AG-VAL-B-012 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceWireStateUpgradeAndGrandfathering --count 1` | exact wire/state migration/V3 grandfathering |
| AG-VAL-C-001 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --suite ./... --count 1 --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/governance --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/authority --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/run --require-package github.com/pankajleh/autonomous-builder-control-plane/cmd/abcp` | full uncached tests |
| AG-VAL-C-002 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --suite ./... --count 1 --race --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/governance --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/run` | full uncached race suite |
| AG-VAL-C-003 | C_BRANCH_ACCEPTANCE | `go vet ./...` | static hygiene only |
| AG-VAL-C-004 | C_BRANCH_ACCEPTANCE | `git diff --check ${A_ACCEPTED_HEAD}...${CANDIDATE_HEAD}` | diff hygiene only |
| AG-VAL-C-005 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./cmd/abcp --test TestAssuranceFreshSessionAuthoritySmoke --count 1` | J-01 real CLI/PostgreSQL smoke |
| AG-VAL-C-006 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceFailedStageCorrectionReentryIntegration --count 1` | J-02 C/integration failure corrections |
| AG-VAL-C-007 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceGapReturnsToAIntegration --count 1` | J-03 model gap |
| AG-VAL-C-008 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./cmd/abcp --test TestAssuranceDocumentationBypassSmoke --count 1` | J-04 docs bypass |
| AG-VAL-C-009 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssurancePhysicalResourceAndCrashStress --count 10 --race` | resource/crash stress |
| AG-VAL-C-010 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/mergelifecycle --test TestAssuranceMergeEffectLeaseInvalidationRace --count 20 --race` | merge effect lease linearization |
| AG-VAL-C-011 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-secret-scan.py --candidate ${CANDIDATE_HEAD} --evidence-selection ${C_EVIDENCE_SELECTION} --secret-fd 3` | credential egress zero-match scan |
| AG-VAL-C-012 | C_BRANCH_ACCEPTANCE | `scripts/acceptance/assurance-runner-preflight.sh --profile NORMAL_V1 --postgres-required` | physical runner/backend preflight |
| AG-VAL-I-001 | INTEGRATION_ACCEPTANCE | `scripts/acceptance/assurance-governance-dogfood.sh ${A_ACCEPTED_HEAD} ${CANDIDATE_HEAD} ${PARENT_RESOURCE_RESERVATION}` | J-05 deterministic depth-1 dogfood |
| AG-VAL-I-002 | INTEGRATION_ACCEPTANCE | `scripts/acceptance/assurance-lifecycle-integration.sh ${CANDIDATE_HEAD} ${C_STAGE_TIP}` | real controller/mergelifecycle integration-before-PR/effect-lease flow |
| AG-VAL-PM-001 | POST_MERGE_ACCEPTANCE | `scripts/acceptance/assurance-governance-postmerge.sh ${MERGE_RESULT}` | merged-state compatibility/fresh PostgreSQL reconnect |

## Evidence artifact schema

`AssuranceResourceReservationV1`, `AssuranceEvidenceCellV1` and `StageEvidenceSelectionV1` use the exact canonical schemas above. Process execution identity additionally hashes: working-directory repository identity/relative path, allowlisted non-secret environment policy, acting principal ID, exact argv, executable/toolchain hashes, cgroup/profile, and (when applicable) Ralphex source/binary plus Codex CLI/model/effort/executor identities. Attempts are lineage-global ordinals; failed/unavailable attempts consume their reservation and never satisfy a cell. Stage CAS selects exactly one PASS evidence digest per required cell.

Secret handling is a trust boundary, not a logging convention. A process marked `SECRET_CAPABLE_V1` (Codex/Ralphex provider execution, PostgreSQL bootstrap/runtime client, Docker credential setup) may receive credential material only through controller-owned ephemeral memory/file/FD outside repository/evidence roots. Its **raw stdout/stderr are never copied into `AssuranceEvidenceCellV1` or retained artifact storage**, even on failure; controller records only bounded structured exit/status fields and digests of explicitly classified secret-free protocol results. Before launch, J-05 requires a capability probe demonstrating a Codex tool subprocess cannot read the credential sentinel/file/FD used by the parent Codex process; inability to prove that isolation is `VALIDATION_UNAVAILABLE`.

The controller retains exact credential byte values only in ephemeral memory long enough to run `assurance-secret-scan.py` against the exact candidate diff and every retained evidence artifact produced by that credential-bearing stage. FD 3 is a controller-owned pipe carrying one or more frames `uint32_be_length || secret_bytes`, terminated by a zero-length frame; the scanner never accepts secret bytes through argv, environment, repository files, or retained evidence. PASS requires zero exact-value occurrences. The scan artifact contains artifact identities, byte counts, scanner version/digest and zero/nonzero match counts only; it never contains the credential value. A positive match fails the stage and quarantines the offending artifact outside authoritative evidence. Generic environment capture, command lines, DSNs, passwords, tokens, cookies, SSH/cloud material and raw secret-capable output are forbidden evidence fields.

Non-secret-capable validators may retain stdout/stderr under their resource profile. Crash evidence records boundary/kill point/pre/post revisions and recovery outcome; resource evidence records reserved/final counters plus cgroup/process/FD/output/evidence/transient/elapsed observations. Candidate subjects bind exact repository identity/commit/tree/A-lineage; integration adds integration repo/source candidate/parent reservation; post-merge adds exact merge result/tree/integration tip.

## Production composition / fake boundary matrix

| Boundary | Unit/B fake allowed? | Required real boundary |
|---|---|---|
| Git repository/object/ref | parser fixtures | C/J-01/J-05 use real temporary Git repositories/objects |
| controller durable state/CAS | canonical pure fixtures | C/integration use real PostgreSQL adapter and fresh reconnect |
| filesystem/path durability | parser fixtures | crash/resource use real filesystem and fresh process |
| cgroup/process limits | API mock for unit only | C preflight/resource use real delegated cgroup v2 + prlimit/bwrap |
| PostgreSQL | parser/mock for local unit only | smoke/integration use exact pinned Docker image/production adapter |
| Ralphex/Codex | deterministic helper for local controller unit only | J-05 uses exact pinned official Ralphex + Codex identities and credential-isolation probe |
| merge provider | deterministic provider may be used to force races in unit | integration uses real `internal/mergelifecycle` controller and effect-lease handoff; no external GitHub merge is performed by J-05 |

## Strict evidence-cell registry

One row = one proof + one evidence class + one stage + one subject + one validator. The mapping below is semantic authority; a validator cannot satisfy a claim outside the row naming it.

| Cell ID | Proof | Class | Stage | Subject | Validator |
|---|---|---|---|---|---|
| AG-E-001 | AG-PO-001 | contract | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-012 |
| AG-E-002 | AG-PO-002 | migration | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-012 |
| AG-E-003 | AG-PO-003 | race_concurrency | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-002 |
| AG-E-004 | AG-PO-004 | security_negative | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-001 |
| AG-E-005 | AG-PO-005 | migration | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-012 |
| AG-E-006 | AG-PO-006 | contract | B_IMPLEMENTATION | frozen assurance model | AG-VAL-B-011 |
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
| AG-E-019 | AG-PO-019 | race_concurrency | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-006 |
| AG-E-020 | AG-PO-020 | crash_restart | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-003 |
| AG-E-021 | AG-PO-021 | fault_injection | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-004 |
| AG-E-022 | AG-PO-022 | fault_injection | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-004 |
| AG-E-023 | AG-PO-023 | smoke | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-005 |
| AG-E-024 | AG-PO-024 | security_negative | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-009 |
| AG-E-025 | AG-PO-025 | resource | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-009 |
| AG-E-026 | AG-PO-026 | integration | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-007 |
| AG-E-027 | AG-PO-027 | contract | B_IMPLEMENTATION | frozen assurance model | AG-VAL-B-011 |
| AG-E-028 | AG-PO-028 | integration | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-005 |
| AG-E-029 | AG-PO-029 | smoke | C_BRANCH_ACCEPTANCE | J-01 unchanged exact C candidate | AG-VAL-C-005 |
| AG-E-030 | AG-PO-029 | integration | C_BRANCH_ACCEPTANCE | J-02 unchanged exact C candidate | AG-VAL-C-006 |
| AG-E-031 | AG-PO-029 | integration | C_BRANCH_ACCEPTANCE | J-03 unchanged exact C candidate | AG-VAL-C-007 |
| AG-E-032 | AG-PO-029 | smoke | C_BRANCH_ACCEPTANCE | J-04 unchanged exact C candidate | AG-VAL-C-008 |
| AG-E-033 | AG-PO-029 | e2e | INTEGRATION_ACCEPTANCE | J-05 exact dogfood subject | AG-VAL-I-001 |
| AG-E-034 | AG-PO-030 | e2e | INTEGRATION_ACCEPTANCE | exact dogfood subject | AG-VAL-I-001 |
| AG-E-035 | AG-PO-031 | contract | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-010 |
| AG-E-036 | AG-PO-032 | resource | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-007 |
| AG-E-037 | AG-PO-032 | race_concurrency | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-009 |
| AG-E-038 | AG-PO-033 | replay_idempotency | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-005 |
| AG-E-039 | AG-PO-034 | contract | B_IMPLEMENTATION | exact B candidate + PostgreSQL | AG-VAL-B-009 |
| AG-E-040 | AG-PO-034 | smoke | C_BRANCH_ACCEPTANCE | unchanged exact C + PostgreSQL | AG-VAL-C-005 |
| AG-E-041 | AG-PO-035 | e2e | INTEGRATION_ACCEPTANCE | exact parent-reserved dogfood | AG-VAL-I-001 |
| AG-E-042 | AG-PO-036 | unit | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-008 |
| AG-E-043 | AG-PO-036 | contract | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-001 |
| AG-E-044 | AG-PO-037 | race_concurrency | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-010 |
| AG-E-045 | AG-PO-037 | integration | INTEGRATION_ACCEPTANCE | exact C/integration stage tips | AG-VAL-I-002 |
| AG-E-046 | AG-PO-038 | security_negative | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-012 |
| AG-E-047 | AG-PO-038 | smoke | C_BRANCH_ACCEPTANCE | unchanged exact C + lifecycle authority | AG-VAL-C-005 |
| AG-E-048 | AG-PO-039 | static | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-003 |
| AG-E-049 | AG-PO-039 | static | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-004 |
| AG-E-050 | AG-PO-040 | contract | B_IMPLEMENTATION | frozen assurance model | AG-VAL-B-011 |
| AG-E-051 | AG-PO-041 | security_negative | C_BRANCH_ACCEPTANCE | unchanged exact C/evidence | AG-VAL-C-011 |
| AG-E-052 | AG-PO-041 | e2e | INTEGRATION_ACCEPTANCE | exact dogfood subject | AG-VAL-I-001 |
| AG-E-053 | AG-PO-042 | migration | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-012 |
| AG-E-054 | AG-PO-042 | smoke | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-005 |
| AG-E-055 | AG-PO-043 | race_concurrency | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-010 |
| AG-E-056 | AG-PO-043 | integration | INTEGRATION_ACCEPTANCE | exact C/integration/merge tips | AG-VAL-I-002 |
| AG-E-057 | AG-PO-044 | e2e | INTEGRATION_ACCEPTANCE | exact frozen J-05 fixture | AG-VAL-I-001 |
| AG-E-058 | AG-PO-025 | resource | C_BRANCH_ACCEPTANCE | physical runner capability | AG-VAL-C-012 |
| AG-E-059 | AG-PO-034 | integration | INTEGRATION_ACCEPTANCE | real PostgreSQL/backend lifecycle | AG-VAL-I-002 |
| AG-E-060 | AG-PO-023 | replay_idempotency | POST_MERGE_ACCEPTANCE | exact merge result + fresh backend | AG-VAL-PM-001 |
| AG-E-061 | AG-PO-005 | migration | POST_MERGE_ACCEPTANCE | exact merge-result commit/tree | AG-VAL-PM-001 |
| AG-E-062 | AG-PO-033 | race_concurrency | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-002 |

Every AG-PO-001..044 has ≥1 exact cell and every validator above is referenced. Static hygiene proves only AG-PO-039. Graph completeness proves AG-PO-006/027/040. Physical resource claims use resource/preflight validators. Merge-order/effect claims use C-010/I-002. AG-PO-029 has five distinct journey cells, one per J-01..J-05.

Frozen current registry cardinality: **50 scenarios, 20 invariants, 44 proof obligations, 62 evidence cells, 27 validators, and 5 critical journeys**. These cardinalities are committed by `AssuranceModelV1`; a changed count changes the model digest and requires A authority.

## Critical journeys

**J-01 / AG-VAL-C-005 — Fresh-session authority smoke.** Real temporary Git repository + assembled `abcp` + real PostgreSQL. Consume exact merged/post-merge `PolicyUpgradeAuthorityV1`, perform the V1→V2 controller-state upgrade, install/replay exact V4 activation, then A→B→C. Mutated manifest/commit/principal/base/schema/repo/path/candidate/grandfather inputs fail before mutation.

**J-02 / AG-VAL-C-006 — Bounded failed-stage correction.** From one accepted A: C1 implementation finding → `FailedStageV1` → correction-B1 (counter 1); replacement C2 → implementation finding → correction-B2 (counter 2); restart/fresh backend → C3 blocker → no grant, counter remains 2, A required. The same flow is repeated with an integration-stage implementation finding before PR publication. Model-gap variants consume zero correction count. Concurrent/exact replay cannot double-consume.

**J-03 / AG-VAL-C-007 — Assurance-gap return-to-A.** Missing scenario, unjustified N/A, wrong cell/subject/attempt, missing real boundary, or inadequate proof produces `ASSURANCE_MODEL_GAP`, zero mutation/reentry authority, and A-required state.

**J-04 / AG-VAL-C-008 — Documentation bypass smoke.** Narrow non-authoritative docs-only classification may pass; a candidate touching governance/runtime bytes invalidates it before acceptance.

**J-05 / AG-VAL-I-001 — Frozen deterministic dogfood.** The fixture bytes are exactly the UTF-8 bytes in these fenced blocks, including LF newlines and the final LF; tabs shown in Go indentation are one U+0009 byte. No CRLF normalization is permitted.

`go.mod` (41 bytes; SHA-256 `890f65191c956c6601f033eefc794abff1d63cdc05479e17f3309b68181face3`):

```text
module example.com/abcp-dogfood

go 1.23
```

`numbers/numbers.go` (148 bytes; SHA-256 `ba8c8918c5c3a6b354962dd663f5b65fe04160a814d72c9f64097456f2851e48`):

```go
package numbers

// UniqueSorted returns the sorted unique values from in without mutating in.
func UniqueSorted(in []int) []int {
	panic("TODO")
}
```

`numbers/numbers_test.go` (574 bytes; SHA-256 `0a032466c1f8801128bef9d93cf7327168eeabc78349d752763ce33181813a2d`):

```go
package numbers_test

import (
	"reflect"
	"testing"

	"example.com/abcp-dogfood/numbers"
)

func TestUniqueSorted(t *testing.T) {
	in := []int{3, -1, 3, 2, -1}
	want := []int{-1, 2, 3}
	got := numbers.UniqueSorted(in)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UniqueSorted() = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(in, []int{3, -1, 3, 2, -1}) {
		t.Fatalf("input mutated: %v", in)
	}
	var nilInput []int
	nilGot := numbers.UniqueSorted(nilInput)
	if nilGot != nil && len(nilGot) != 0 {
		t.Fatalf("nil input result = %v, want nil or empty", nilGot)
	}
}
```

The fixture-set digest is SHA-256 `93e1dcef80ff14255ec1b78a8932d90bbcc9d1679d54dc0e741d961d5e92cc6e` over each sorted entry `path || NUL || decimal-byte-length || NUL || bytes`. '' '
The harness creates a fresh Git repo from exactly those bytes with fixed identity `ABCP Dogfood <dogfood@example.invalid>`, UTC timestamp `2000-01-01T00:00:00Z`, branch `main`, message `baseline`, then verifies the fixture digest before authority issuance. The task is exactly: **implement `numbers.UniqueSorted` so it returns ascending unique integers, does not mutate input, and preserves nil-or-empty semantics; change production code only.** PASS requires `go test -count=1 ./...`, unchanged test/go.mod bytes, zero secret scan matches, and only the expected production-file diff.

Outer integration reserves `DOGFOOD_V1` plus child maxima of 16 validator invocations, 128 MiB retained evidence, 2 GiB transient scratch, 90 min elapsed, one active child graph. It issues `DogfoodChildActivationV1` mode `DOGFOOD_DELEGATED`, depth 1, exact child repository/baseline tree, parent integration tip/reservation, and exact V4 policy/model digests. Child allowed operations are only A_DESIGN/B_IMPLEMENTATION/C_BRANCH_ACCEPTANCE; PR publication, merge, post-merge, another dogfood, and authority reuse outside that repository/reservation are invalid.

Tool authority is exact: Ralphex origin `https://github.com/umputun/ralphex.git`, source `319e30618352a1b43e4be1b8a894c6c05e6d5fa8`, binary SHA-256 `9ad47b083aaaf34eed4b35ca82f3c92d1a3b7641921be0ea606cbd1cd1c3adac`; Codex CLI 0.149.0 binary SHA-256 `134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477`, executor `codex`, model `gpt-5.6-sol`, reasoning effort `xhigh`. Credential-isolation capability probe and runner preflight must PASS before launch.

## Functional-plan mutation and B convergence

The assurance model is immutable throughout B/C. Functional-plan changes are limited to status/checkbox progression and final semantic move. B convergence requires every B cell against one exact candidate; C independently executes its cells and performs read-only exact-head review.

The V4 lifecycle transition graph is exactly:

`DESIGN_ACCEPTED → IMPLEMENTATION_CONVERGED → BRANCH_ACCEPTED → FINAL_REVIEW_CLEAN → INTEGRATION_ACCEPTED → PR_PUBLISHED → MERGE_AUTHORIZED → MERGE_SUBMITTING → (MERGE_APPLIED → POST_MERGE_ACCEPTED | MERGE_NOT_APPLIED | RECOVERY_REQUIRED)`.

`PR_PUBLISHED` before `INTEGRATION_ACCEPTED` is invalid. Thus an integration failure occurs before a PR can become stale. Integration failure atomically publishes `FailedStageV1` and invalidates the C tip: `ASSURANCE_MODEL_GAP` sets A-required and grants no correction; `IMPLEMENTATION_FINDING` may issue one `CorrectionBGrantV1` under the same cumulative two-reentry counter used for C-review findings.

`MERGE_AUTHORIZED` requires unchanged candidate/C-review/integration/PR tips. The next external-effect authorization is a **single atomic CAS** that consumes that exact tip, publishes one unconsumed `MergeEffectLeaseV1`, and changes controller merge state to `MERGE_SUBMITTING`. C/stage invalidation and lease issuance therefore compete on one predecessor tip: if invalidation wins, no lease/provider call is legal; if lease wins, later observations cannot retroactively revoke an effect that may already have been submitted. `internal/mergelifecycle` must require the exact lease before `Provider.SubmitTarget` and mark it submitted/consumed according to durable outcome. A timeout/cancellation/connection ambiguity after possible submission authorizes **read-only reconciliation only**. Another provider mutation is forbidden unless reconciliation proves exact `MERGE_NOT_APPLIED` and a separately governed new effect authority is issued; `RECOVERY_REQUIRED` is fail-closed. Post-submission blockers are evidence for completion/repair governance, not retroactive C correction authority.

Post-merge failure records non-completion and blocks external-build readiness; no automatic rollback exists. Any revert/repair is a new governed A/B/C change. Publication/merge requires all applicable B/C/integration cells PASS, exact-head Critical=0/Major=0, clean unchanged candidate, unexhausted lineage budgets, and operationally usable exact tips.

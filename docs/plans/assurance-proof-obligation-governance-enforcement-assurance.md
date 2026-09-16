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
- `internal/prlifecycle/**`
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
| AG-I20 | V4 lifecycle side effects linearize through controller-owned intent + one-winner effect claims; PR/merge submission is impossible without the exact ephemeral single-consume capability produced only for the winning claim CAS. |
| AG-I21 | Every remote write effect (PR publication or merge submission) requires a controller CAS that has exactly one winner and yields an ephemeral one-shot capability whose raw token is never durable; after possible submission only read-only reconciliation is authorized. |
| AG-I22 | PostgreSQL enforces authority-domain isolation in the database itself with forced row-level security, runtime-role predicates derived from `current_user`, non-owner/no-bypass roles, fixed schema/search-path, and exact grants/revocations. |
| AG-I23 | Every digest-referenced governance/evidence ancestor is retrievable cross-host from a shared immutable content-addressed artifact backend in the same authority domain. |
| AG-I24 | The append-only run ledger remains operational run-state truth while PostgreSQL owns assurance/governance coordination; every cross-boundary transition binds the exact ledger event digest and is crash-reconcilable without dual-authoritative state. |
| AG-I25 | V4 activation never migrates a live V3 workflow: activation requires a controller-proven drained predecessor V3 state, preserves its exact final bytes/revision as immutable evidence, and starts V2 at predecessor revision + 1 with an empty grandfather set. |
| AG-I26 | PR publication is an external effect with the same intent/claim/reconciliation discipline as merge submission and cannot occur before integration acceptance. |
| AG-I27 | Correction-B paths are controller-derived only from the frozen semantic registry, validated finding rule IDs/affected paths, and the original B scope; reviewer/finding bytes cannot directly supply mutation paths. |
| AG-I28 | Secret-capable execution is bound in validator authority and every credential-bearing lifecycle stage must pass a complete retained-artifact inventory scan before its stage can pass. |
| AG-I29 | Resource profile/container capability is frozen in validator authority, not selected by callers; controller reservations cover the full process/container/cache footprint against a worker-wide aggregate capacity. |
| AG-I30 | J-05 delegation binds a frozen policy floor and exact toy plan/baseline; it authorizes child A only, and the child A must mint its own repository-bound model before any B/C authority exists. |

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
| AG-S-013 | authority identity | V4 activation is attempted while predecessor V3 is not drained, or historical V3 authority is presented operationally after V4 activation | AG-I01,AG-I08,AG-I09,AG-I25 | AG-PO-002,AG-PO-005,AG-PO-023,AG-PO-049 |
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
| AG-S-027 | compatibility/version skew | activation is attempted while predecessor V3 has a nonterminal lineage or unresolved effect | AG-I01,AG-I08,AG-I25 | AG-PO-002,AG-PO-005,AG-PO-049 |
| AG-S-028 | compatibility/version skew | drained historical V3 authority is replayed operationally after V4 activation | AG-I01,AG-I08,AG-I09,AG-I25 | AG-PO-005,AG-PO-023,AG-PO-049 |
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
| AG-S-048 | compatibility/version skew | drained historical V3 bytes are reinterpreted as V4 authority or used to mint a V4 descendant without the successor activation/A chain | AG-I01,AG-I08,AG-I09,AG-I25 | AG-PO-002,AG-PO-005,AG-PO-042,AG-PO-049 |
| AG-S-049 | structural assurance | syntactically valid registries contain semantically disconnected or duplicate scenario→invariant→proof→cell→validator links | AG-I02,AG-I05 | AG-PO-006,AG-PO-040 |
| AG-S-050 | dogfood delegated authority | a depth-1 child activation escapes its parent reservation/repository/budget or attempts publication, merge, or nested dogfood | AG-I09,AG-I12,AG-I16 | AG-PO-035,AG-PO-044 |

| AG-S-051 | remote-effect claim | two hosts/processes observe the same merge-authorized tip and both attempt provider submission | AG-I20,AG-I21 | AG-PO-043,AG-PO-045 |
| AG-S-052 | database isolation | one runtime credential directly selects/updates another authority domain/controller row or artifact | AG-I11,AG-I18,AG-I22 | AG-PO-046 |
| AG-S-053 | cross-host recovery | fresh controller host has state digests but cannot load predecessor/model/grant/evidence bytes | AG-I09,AG-I23 | AG-PO-050 |
| AG-S-054 | migration/version skew | V4 activation occurs while predecessor V3 has active invocation/grant/lease/nonterminal checkpoint | AG-I08,AG-I25 | AG-PO-049 |
| AG-S-055 | PR effect race | C/integration invalidation races PR HTTP submission or process dies after claim | AG-I17,AG-I21,AG-I26 | AG-PO-054 |
| AG-S-056 | durable-state split | ledger READY/MERGED/FAILED state and PostgreSQL stage tip diverge across crash boundary | AG-I10,AG-I17,AG-I24 | AG-PO-055 |
| AG-S-057 | resource profile downgrade | caller selects a weaker resource/secret profile or container escapes aggregate reservation | AG-I12,AG-I16,AG-I29 | AG-PO-051 |
| AG-S-058 | secret inventory | B, integration, post-merge, failed or orphan evidence containing a credential is omitted from later scan | AG-I19,AG-I28 | AG-PO-041,AG-PO-052 |
| AG-S-059 | child authority | J-05 reuses parent ABCP assurance model for toy repository or runs Ralphex without exact frozen plan/capsule authority | AG-I09,AG-I30 | AG-PO-044,AG-PO-053 |
| AG-S-060 | correction minting | reviewer/finding packet supplies correction paths not derivable from frozen A semantic registry | AG-I07,AG-I27 | AG-PO-018,AG-PO-048 |
| AG-S-061 | immutable artifact conflict | same digest/logical artifact identity is absent, cross-domain, oversized, or resolves to conflicting bytes | AG-I08,AG-I10,AG-I23 | AG-PO-007,AG-PO-050 |

All minimum policy dimensions are applicable. No failure-model dimension is `not_applicable` for this controller-governance implementation. Evidence-class applicability is separately frozen below; `runtime` and `production` are explicitly N/A because this execution pack does not deploy a long-running production ABCP service.

## Frozen proof obligations

| ID | Falsifiable claim | Invariants |
|---|---|---|
| AG-PO-001 | `AssurancePolicyManifestV1` binds exact predecessor activation digest, exact authorized merge-result policy commit/tree, exact protected base branch, authenticated acting principal, successor capsule wire version `context-capsule-v4`, assurance schema `assurance-policy-v1`, and sorted canonical full policy path+SHA256 entries recomputed from that commit; no ambient/caller digest is trusted. | AG-I01,AG-I08,AG-I09 |
| AG-PO-002 | successor activation is permitted only when controller-derived `V3Drained` is true; the eligible in-flight predecessor set is therefore exactly empty. Any nonterminal V3 blocks activation, and after V4 activation all V3 records are historical evidence only. | AG-I01,AG-I08,AG-I09,AG-I25 |
| AG-PO-003 | successor activation installation is one atomic compare-and-swap over predecessor activation + expected controller revision; exact replay is idempotent and competing/conflicting installation changes no authority. | AG-I01,AG-I08,AG-I10 |
| AG-PO-004 | every assurance record strict-parses canonically; sorted-set reorder hashes identically while duplicate/unknown/ambiguous/oversized fields fail before authority/evidence allocation. | AG-I08,AG-I12 |
| AG-PO-005 | historical V1/V2/V3 bytes retain prior meaning and validators; V4 never reinterprets them. Because activation is drain-before-upgrade, no V3 operational continuation exists after V4 activation and historical V3 can never parent successor A/B/C. | AG-I01,AG-I08,AG-I25 |
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
| AG-PO-042 | Every successor wire record has the exact frozen canonical schema/digest preimage below; exact drained `ControllerStateV1` bytes/revision are retained as immutable predecessor evidence, `GovernanceActivationV2.grandfathered_v3` is exactly empty, and V2 starts at predecessor revision + 1 without reinterpretation or live-state migration. | AG-I01,AG-I08,AG-I09,AG-I10,AG-I25 |
| AG-PO-043 | V4 lifecycle follows the exact transition graph below; integration precedes PR publication, and external merge is impossible until the one-winner `MergeEffectClaimV1` CAS changes the exact tip to `MERGE_SUBMITTING` and yields its non-durable single-consume capability. Invalidation and claim issuance are mutually exclusive; ambiguous submission is reconciliation-only. | AG-I10,AG-I13,AG-I17,AG-I20,AG-I21 |
| AG-PO-044 | J-05 is a deterministic executable fixture with frozen initial files/task/black-box result, exact parent-reserved child ceilings, pinned Ralphex/Codex identities, and parent-issued `DOGFOOD_DELEGATED` V4 activation constrained to one child repository at depth 1 with publication/merge/dogfood disabled. | AG-I09,AG-I12,AG-I16 |
| AG-PO-045 | Merge submission has one atomic pre-call winner: controller CAS installs `MergeEffectClaimV1` with claimant identity and hash of a random 256-bit ephemeral claim token; only the winning process retains the raw token in a single-consume in-memory capability. No other host can call; loss after claim is reconciliation-only and a new effect requires exact NOT_APPLIED plus new authority. | AG-I20,AG-I21 |
| AG-PO-046 | PostgreSQL itself prevents cross-domain reads/writes: forced RLS derives domain from authenticated `current_user`, runtime roles are non-owner/non-superuser/non-bypass, PUBLIC is revoked, search path/schema ownership is fixed, domain bindings are runtime-read-only, and workflow/artifact DML is permitted only through matching-domain policies. | AG-I11,AG-I18,AG-I22 |
| AG-PO-047 | Every V4 authoritative/referenced record has a frozen strict-canonical schema, enum/bounds, digest preimage and canonical vector, including capsule/phase authority/checkpoint/grant/work class/semantic registry/final-review profile/effect claims/evidence/resource/escape/post-merge-failure records. | AG-I02,AG-I08 |
| AG-PO-048 | The accepted A binds a complete `SemanticAuthorityRegistryV2` and `FinalReviewProfileV1`; each implementation finding carries a frozen rule ID but **no path authority**. The controller publishes `ControllerAffectedPathSetV1` from exact accepted-B base→failed-candidate Git diff paths intersected with frozen rule scope and original-B scope; correction grants may contain only that controller-derived intersection. | AG-I07,AG-I27 |
| AG-PO-049 | V4 activation is drain-before-upgrade: predecessor V3 must satisfy `V3Drained`, grandfather set is exactly empty, exact terminal V1 bytes/revision are stored immutably, and initial V2 row is revision `v1.revision+1`; otherwise activation fails `V3_DRAIN_REQUIRED`. | AG-I08,AG-I25 |
| AG-PO-050 | A PostgreSQL immutable artifact table stores every digest-referenced authority/evidence artifact under forced domain isolation with create-or-verify SHA/size/kind rules; state CAS may reference only already durable artifacts, and fresh hosts resolve full ancestor chains by digest. | AG-I09,AG-I10,AG-I23 |
| AG-PO-051 | Validator authority fixes process profile, secret capability, network/container requirements and maximum resource vector. Reservation cannot weaken it and atomically charges process plus optional container/tmpfs/cache footprint against lineage and worker-wide capacities before launch. | AG-I12,AG-I16,AG-I29 |
| AG-PO-052 | Every credential-bearing B/C/integration/post-merge stage has an immutable complete retained-artifact inventory and a stage-local exact-secret zero-match scan before PASS. Real upstream/PostgreSQL/Docker credentials are never argv/env and raw secret-capable output is never retained; the sole Codex-parent exception is the short-lived `ABCP_BROKER_SESSION_TOKEN`, which is not an upstream credential, is excluded from tool-shell inheritance, and may exist only after harmless probes prove shell-env, parent-proc and loopback isolation under `CODEX_BROKER_V1`. | AG-I19,AG-I28 |
| AG-PO-053 | J-05 contains exact plan bytes/hash, deterministic baseline commit/tree and a `DogfoodPolicyFloorV1`; parent delegation authorizes child A only, child A produces its own repository-bound assurance model/capsule/checkpoint, and only that accepted child model may derive B/C. | AG-I09,AG-I30 |
| AG-PO-054 | PR publication has one-winner `PREffectClaimV1` semantics bound to integration/C tips and exact request digest; ambiguous or lost claim is reconciliation-only, and `internal/prlifecycle` cannot issue the HTTP write without the ephemeral single-consume claim capability. | AG-I17,AG-I21,AG-I26 |
| AG-PO-055 | Ledger/PostgreSQL composition is explicit: ledger is run-state truth, PostgreSQL is coordination authority, every V4 checkpoint/effect binds exact ledger event digest, merge requires READY event + matching PG claim, terminal ledger event is written before PG settlement, and every crash gap is reconcile-only with `PostMergeFailureV1` for failed acceptance. | AG-I10,AG-I17,AG-I24 |

## Canonical successor wire contracts and validity predicates

All records here use strict canonical JSON: unknown/duplicate fields, invalid UTF-8, floats for integers, invalid enums, non-lowercase SHA-256, invalid full Git OIDs, unsorted/duplicate set arrays, oversized strings/arrays, or non-canonical UTC RFC3339-second timestamps fail. Object serialization order is exactly the listed field order. Optional fields are omitted, never `null`. A record digest is SHA-256 of the canonical UTF-8 JSON with its terminal self-digest field absent; the sealed form appends that field last. IDs/paths are ≤128/1024 bytes unless a smaller bound is stated; text claims/commands are ≤4096 bytes; arrays are ≤256 entries; authority records are ≤1 MiB.

Closed enums: `phase={A_DESIGN,B_IMPLEMENTATION,C_ACCEPTANCE_MERGE}`; evidence stages `{B_IMPLEMENTATION,C_BRANCH_ACCEPTANCE,INTEGRATION_ACCEPTANCE,POST_MERGE_ACCEPTANCE}`; work class `{CODE_BEARING,DOCUMENTATION_ONLY}`; finding class `{IMPLEMENTATION_FINDING,ASSURANCE_MODEL_GAP}`; correction relation `{IMPLEMENTATION_CORRECTABLE,A_REQUIRED}`; secret capability `{NONE,POSTGRES,CODEX_PROVIDER,DOCKER_PASSWORD}`; effect status `{NONE,PR_SUBMITTING,PR_PUBLISHED,MERGE_SUBMITTING,MERGE_APPLIED,MERGE_NOT_APPLIED,RECOVERY_REQUIRED}`.

| Record / schema | Exact field order before self digest |
|---|---|
| `PolicyUpgradeAuthorityV1` / `policy-upgrade-authority-v1` | `kind,schema_version,repository_identity,base_branch,expected_premerge_base_oid,reviewed_head_oid,merge_result_oid,merge_result_tree_oid,merge_method,acting_principal,merge_authorized_checkpoint_sha256,post_merge_accepted_checkpoint_sha256,provider_evidence_sha256,policy_evidence_sha256,predecessor_activation_sha256` |
| `WorkClassificationV1` / `work-classification-v1` | `kind,schema_version,classification,eligible_paths:[]str(set),rationale,classifier_identity` |
| `GovernanceActivationV2` / `governance-activation-v2` | `kind,schema_version,policy_version="context-capsule-v4",predecessor_activation_sha256,policy_manifest_sha256,policy_upgrade_authority_sha256,activation_repository_commit,activation_repository_tree,activation_sequence:u64,predecessor_state_v1_artifact_sha256,predecessor_state_v1_revision:u64,grandfathered_v3:[]str(set)`; `grandfathered_v3` must be exactly empty. |
| `AssurancePolicyManifestV1` / `assurance-policy-v1` | `kind,schema_version,repository_identity,activation_repository_commit,activation_repository_tree,predecessor_activation_sha256,lifecycle_authority_sha256,acting_principal,capsule_policy_version,assurance_schema_version,policy_files:[]PolicyFileV1(set:path)` |
| `FinalReviewProfileV1` / `final-review-profile-v1` | `kind,schema_version,profile_id,provider,model,reasoning_effort,fresh_session:bool,required_critical:u64,required_major:u64,classification_vocabulary:[]str(order)` |
| `SemanticAuthorityRegistryV2` / `semantic-authority-registry-v2` | `kind,schema_version,repository_identity,accepted_a_head,component_scopes:[]ComponentScopeV1(set:id),rules:[]SemanticRuleV2(set:rule_id),final_review_profile_sha256` |
| `AssuranceModelV1` / `assurance-model-v1` | `kind,schema_version,repository_identity,accepted_a_head,policy_manifest_sha256,semantic_registry_sha256,work_classification_sha256,final_review_profile_sha256,proof_obligation_set_sha256,evidence_matrix_sha256,resource_profile_set_sha256,failure_scenarios:[]ScenarioV1(set:id),invariants:[]InvariantV1(set:id),proof_obligations:[]ProofV1(set:id),validators:[]ValidatorV1(set:id),evidence_cells:[]CellV1(set:id),journeys:[]JourneyV1(set:id),resource_profiles:[]ResourceProfileV1(set:id),correction_reentry_limit:u64,allowed_b_paths:[]str(set)`; the three set digests are recomputed from the exact nested canonical projections below and mismatch fails. |
| `ProofObligationSetV1` / `proof-obligation-set-v1` | `kind,schema_version,repository_identity,accepted_a_head,proof_obligations:[]ProofV1(set:id)` |
| `EvidenceMatrixV1` / `evidence-matrix-v1` | `kind,schema_version,repository_identity,accepted_a_head,validators:[]ValidatorV1(set:id),evidence_cells:[]CellV1(set:id),journeys:[]JourneyV1(set:id)` |
| `ResourceProfileSetV1` / `resource-profile-set-v1` | `kind,schema_version,repository_identity,accepted_a_head,resource_profiles:[]ResourceProfileV1(set:id),lineage_budget_sha256,worker_capacity_sha256` |
| `PhaseAuthorityV4` / `phase-authority-v4` | `stage,allowed_operations:[]str(set),observation_scope_ids:[]str(set),blocking_scope_ids:[]str(set),mutation_scope_ids:[]str(set),authorized_invariant_ids:[]str(set),authorized_finding_ids:[]str(set),allowed_paths:[]str(set),review_profile_id,execution_bounds_sha256?:sha256,correction_reentry_limit:u64` |
| `ContextCapsuleV4` / `context-capsule-v4` | `kind,schema_version,policy_version="context-capsule-v4",repository_identity,project_id,plan_id,task_id,base_oid,source_oid,phase,operation,predecessor_capsule_sha256?:sha256,predecessor_checkpoint_sha256?:sha256,policy_manifest_sha256,assurance_model_sha256,semantic_registry_sha256,proof_obligation_set_sha256,evidence_matrix_sha256,resource_profile_set_sha256,work_classification_sha256,final_review_profile_sha256,phase_authority:PhaseAuthorityV4` |
| `PhaseCheckpointV2` / `phase-checkpoint-v2` | `kind,schema_version,repository_identity,accepted_a_lineage_sha256,stage,sequence:u64,predecessor_checkpoint_sha256?:sha256,capsule_sha256,candidate_oid,candidate_tree_oid,ledger_event_sha256,proof_obligation_set_sha256,evidence_matrix_sha256,resource_profile_set_sha256,evidence_selection_sha256?:sha256,review_report_sha256?:sha256,review_critical:u64,review_major:u64,lifecycle_binding_sha256?:sha256,next_stage_grant_sha256?:sha256` |
| `StageGrantV2` / `stage-grant-v2` | `kind,schema_version,repository_identity,accepted_a_lineage_sha256,from_checkpoint_sha256,to_phase,policy_manifest_sha256,assurance_model_sha256,semantic_registry_sha256,proof_obligation_set_sha256,evidence_matrix_sha256,resource_profile_set_sha256,work_classification_sha256,final_review_profile_sha256,allowed_paths:[]str(set),allowed_operations:[]str(set),authorized_finding_ids:[]str(set),execution_bounds_sha256?:sha256,remaining_resource_budget_sha256,correction_reentry_limit:u64,correction_reentries_used:u64` |
| `FindingEvidenceV2` / `finding-evidence-v2` | `kind,schema_version,finding_id,rule_id,classification,affected_path_set_sha256,evidence_sha256,review_report_sha256` |
| `ControllerAffectedPathSetV1` / `controller-affected-path-set-v1` | `kind,schema_version,finding_id,base_oid,candidate_oid,changed_paths:[]str(set),rule_scope_paths:[]str(set),original_b_paths:[]str(set),derived_affected_paths:[]str(set),derivation="GIT_DIFF_RULE_SCOPE_INTERSECTION_V1"` |
| `IntegrationFailureV1` / `integration-failure-v1` | `kind,schema_version,accepted_a_lineage_sha256,c_stage_tip_sha256,integration_subject_sha256,integration_validator_id,classification,finding_ids:[]str(set),evidence_sha256,created_sequence:u64` |
| `FailedStageV1` / `failed-stage-v1` | `kind,schema_version,accepted_a_lineage_sha256,stage,candidate_oid,candidate_tree_oid,stage_tip_sha256,classification,finding_ids:[]str(set),evidence_sha256,integration_failure_sha256?:sha256,invalidated_tip_sha256,created_sequence:u64` (contains **no correction paths**) |
| `CorrectionBGrantV1` / `correction-b-grant-v1` | `kind,schema_version,accepted_a_lineage_sha256,failed_stage_sha256,prior_c_tip_sha256,correction_generation:u64,correction_reentry_ordinal:u64,finding_ids:[]str(set),affected_path_sets:[]sha256(set),derived_allowed_paths:[]str(set),semantic_registry_sha256,original_b_scope_sha256,execution_bounds_sha256,remaining_resource_budget_sha256` |
| `AssuranceEscapeV1` / `assurance-escape-v1` | `kind,schema_version,accepted_a_lineage_sha256,discovery_stage,candidate_oid,missed_scenario_ids:[]str(set),affected_invariant_ids:[]str(set),affected_proof_ids:[]str(set),evidence_sha256,required_action="A_REQUIRED"` |
| `ResourceProfileV1` / `resource-profile-v1` | `id,process_memory_bytes:u64,process_pids:u64,process_cpu_micros_per_period:u64,process_cpu_period_micros:u64,nofile:u64,wall_ms:u64,stdout_bytes:u64,stderr_bytes:u64,evidence_bytes:u64,transient_bytes:u64,container_memory_bytes:u64,container_pids:u64,container_cpu_micros_per_period:u64,container_tmpfs_bytes:u64,secret_capability,network_policy` |
| `ResourceBudgetV1` / `resource-budget-v1` | `kind,schema_version,validator_invocations:u64,rejected_attempts:u64,external_observations:u64,retained_evidence_bytes:u64,transient_bytes:u64,elapsed_ms:u64,correction_reentries:u64` |
| `WorkerCapacityReservationV1` / `worker-capacity-reservation-v1` | `kind,schema_version,reservation_id,process_memory_bytes:u64,process_pids:u64,process_cpu_micros:u64,nofile:u64,container_memory_bytes:u64,container_pids:u64,container_cpu_micros:u64,container_tmpfs_bytes:u64` |
| `PathSetV1` / `path-set-v1` | `kind,schema_version,paths:[]str(set)` |
| `AssuranceResourceReservationV1` / `assurance-resource-reservation-v1` | `kind,schema_version,accepted_a_lineage_sha256,invocation_ordinal,cell_attempt_ordinal,cell_id,proof_id,stage,subject_sha256,validator_id,resource_profile_id,predecessor_state_revision:u64,predecessor_stage_tip_sha256,reserved_worker_capacity_sha256,parent_reservation_sha256?:sha256,nesting_depth:u64` |
| `ExecutionIdentityV1` / `execution-identity-v1` | `kind,schema_version,repository_identity,working_directory,argv_sha256,environment_policy_sha256,executable_sha256,toolchain_sha256,resource_profile_id,secret_capability,codex_provider_isolation_profile_sha256?:sha256,ralphex_sha256?:sha256,codex_sha256?:sha256,codex_model?:str,codex_effort?:str` |
| `CodexProviderIsolationProfileV1` / `codex-provider-isolation-profile-v1` | `kind,schema_version,id="CODEX_BROKER_V1",provider_id="abcp-broker",wire_api="responses",requires_openai_auth=false,env_key_name="ABCP_BROKER_SESSION_TOKEN",sandbox_mode="workspace-write",shell_excluded_env_names:["ABCP_BROKER_SESSION_TOKEN"],loopback_only:bool=true,tool_network_must_be_denied:bool=true,parent_environ_must_be_denied:bool=true,model="gpt-5.6-sol",reasoning_effort="xhigh"` |
| `AssuranceEvidenceCellV1` / `assurance-evidence-cell-v1` | `kind,schema_version,accepted_a_lineage_sha256,reservation_sha256,invocation_ordinal,cell_attempt_ordinal,cell_id,proof_id,evidence_class,stage,subject_sha256,validator_id,execution_identity_sha256,started_at,ended_at,outcome,structured_result_sha256,stdout_sha256?:sha256,stderr_sha256?:sha256,resource_evidence_sha256?:sha256,secret_scan_sha256?:sha256` |
| `StageEvidenceSelectionV1` / `stage-evidence-selection-v1` | `kind,schema_version,accepted_a_lineage_sha256,stage,subject_sha256,predecessor_stage_tip_sha256,inventory_sha256,selected_cells:[]SelectedCellV1(set:cell_id),remaining_budget_sha256` |
| `RetainedArtifactInventoryV1` / `retained-artifact-inventory-v1` | `kind,schema_version,accepted_a_lineage_sha256,stage,through_artifact_sequence:u64,artifact_entries:[]ArtifactInventoryEntryV1(set:artifact_sha256),inventory_complete:bool=true` |
| `SecretScanEvidenceV1` / `secret-scan-evidence-v1` | `kind,schema_version,accepted_a_lineage_sha256,stage,inventory_sha256,scanner_sha256,secret_count:u64,scanned_bytes:u64,match_count:u64` |
| `SecretEgressIncidentV1` / `secret-egress-incident-v1` | `kind,schema_version,accepted_a_lineage_sha256,stage,artifact_kind,artifact_sha256?:sha256,scanner_sha256,match_count:u64,raw_bytes_retained:bool=false,created_sequence:u64` |
| `CrashBoundaryEvidenceV1` / `crash-boundary-evidence-v1` | `kind,schema_version,family,boundary,pre_state_sha256,post_state_sha256?:sha256,artifact_sha256?:sha256,recovery_outcome` |
| `ResourceEvidenceV1` / `resource-evidence-v1` | `kind,schema_version,reservation_sha256,process_peak_memory_bytes,process_peak_pids,process_cpu_usec,nofile_peak,container_peak_memory_bytes,container_peak_pids,container_cpu_usec,tmpfs_bytes,retained_evidence_bytes,transient_bytes,elapsed_ms,cleanup_outcome` |
| `DogfoodPolicyFloorV1` / `dogfood-policy-floor-v1` | `kind,schema_version,parent_lineage_sha256,policy_manifest_sha256,required_failure_dimensions:[]str(set),required_evidence_classes:[]str(set),required_final_review_profile_sha256,forbidden_operations:[]str(set),max_child_budget_sha256` |
| `DogfoodChildActivationV1` / `dogfood-child-activation-v1` | `kind,schema_version,activation_mode="DOGFOOD_DELEGATED",parent_lineage_sha256,parent_integration_tip_sha256,parent_reservation_sha256,child_repository_identity,child_baseline_oid,child_baseline_tree_oid,child_plan_path,child_plan_sha256,policy_floor_sha256,codex_provider_isolation_profile_sha256,nesting_depth=1,allowed_operations:["A_DESIGN"]` |
| `PREffectIntentV1` / `pr-effect-intent-v1` | `kind,schema_version,accepted_a_lineage_sha256,candidate_oid,candidate_tree_oid,integration_tip_sha256,final_review_tip_sha256,pr_request_sha256,effect_ordinal:u64` |
| `PREffectClaimV1` / `pr-effect-claim-v1` | `kind,schema_version,intent_sha256,claimant_instance_sha256,claim_token_sha256,provider_request_sha256,claim_sequence:u64,effect_state="PR_SUBMITTING"` |
| `MergeEffectIntentV1` / `merge-effect-intent-v1` | `kind,schema_version,accepted_a_lineage_sha256,candidate_oid,candidate_tree_oid,ledger_ready_event_sha256,pr_published_tip_sha256,integration_tip_sha256,merge_authorized_tip_sha256,provider_request_sha256,effect_ordinal:u64` |
| `MergeEffectClaimV1` / `merge-effect-claim-v1` | `kind,schema_version,intent_sha256,claimant_instance_sha256,claim_token_sha256,provider_request_sha256,claim_sequence:u64,effect_state="MERGE_SUBMITTING"` |
| `PostMergeFailureV1` / `post-merge-failure-v1` | `kind,schema_version,accepted_a_lineage_sha256,merge_result_oid,merge_result_tree_oid,ledger_terminal_event_sha256,post_merge_evidence_sha256,reason_code,created_sequence:u64` |
| `ControllerStateV2` / `governance-controller-state-v2` | `kind,schema_version,controller_identity,repository_identity,revision,predecessor_state_v1_artifact_sha256,predecessor_state_v1_revision,activation_v2_sha256,active_lineage_sha256?:sha256,stage_generation,active_stage,active_stage_tip_sha256?:sha256,invalidated_stage_tip_sha256?:sha256,ledger_event_tip_sha256?:sha256,a_required,correction_reentries_used,active_resource_reservations:[]sha256(set),selected_stage_evidence_sha256?:sha256,pr_effect_intent_sha256?:sha256,pr_effect_claim_sha256?:sha256,merge_effect_intent_sha256?:sha256,merge_effect_claim_sha256?:sha256,effect_state,cumulative_resource_counters:ResourceCountersV2` |

Nested exact records: `PolicyFileV1={path,sha256}`; `ScenarioV1={id,dimension,scenario,invariant_ids:[]str(set),proof_ids:[]str(set)}`; `InvariantV1={id,claim}`; `ProofV1={id,claim,invariant_ids:[]str(set)}`; `ValidatorV1={id,stage,command,purpose,resource_profile_id,secret_capability,requires_postgres:bool,requires_container:bool}`; `CellV1={id,proof_id,evidence_class,stage,subject_rule,validator_id}`; `JourneyV1={id,validator_id,title,subject_rule,required_boundary_ids:[]str(set)}`; `ComponentScopeV1={id,allowed_paths:[]str(set)}`; `SemanticRuleV2={rule_id,proof_id,correction_relation,owner_component,allowed_scope_ids:[]str(set)}`; `SelectedCellV1={cell_id,evidence_sha256,reservation_sha256}`; `ArtifactInventoryEntryV1={artifact_sha256,artifact_kind,artifact_sequence:u64,byte_size:u64,outcome,stage}`; `ResourceCountersV2={validator_invocations,rejected_assurance_attempts,external_observations,retained_evidence_refs,retained_evidence_bytes,transient_bytes,elapsed_ms,active_validator_reservations,worker_memory_reserved_bytes,worker_pids_reserved,worker_cpu_micros_reserved,worker_nofile_reserved}`.

Canonical test vectors are frozen: WorkClassification vector digest `d79b2b9c6e77db88f096bfe5597ec56599369ee4d34cf3c23a64ae685fd5f684` for `{"kind":"WorkClassificationV1","schema_version":"work-classification-v1","classification":"CODE_BEARING","eligible_paths":[],"rationale":"ABCP assurance-governance enforcement changes authority-bearing code","classifier_identity":"controller"}`; FinalReviewProfile digest `073885532416375d9cb7e81d674812131eabfd1929867daa041490c854b49553` for `{"kind":"FinalReviewProfileV1","schema_version":"final-review-profile-v1","profile_id":"ABCP_XHIGH_FRESH_V1","provider":"codex","model":"gpt-5.6-sol","reasoning_effort":"xhigh","fresh_session":true,"required_critical":0,"required_major":0,"classification_vocabulary":["ASSURANCE_MODEL_GAP","IMPLEMENTATION_FINDING"]}`.

The manifest contains exactly these sorted full paths: `docs/architecture/ASSURANCE_MODEL_AND_PROOF_OBLIGATION_POLICY.md`, `docs/architecture/AUTONOMOUS_EXECUTION_GOVERNANCE.md`, `docs/architecture/CONTEXT_AUTHORITY_AND_CAPSULE_POLICY.md`, `docs/architecture/DECISION_AND_ACCEPTANCE_POLICY.md`, `docs/architecture/IMPLEMENTATION_DESIGN_GATE.md`.

`proof_obligation_set_sha256`, `evidence_matrix_sha256`, and `resource_profile_set_sha256` are independent content-addressed artifacts, not aliases for the whole assurance-model digest. A validates the three canonical projections from the frozen model, and every A checkpoint, A→B grant, B/C capsule, B→C grant and C checkpoint must preserve the same exact values. Any missing/mismatched projection digest is lineage invalidation, never an inferred fallback.

### Frozen semantic registry and final-review authority

Component scopes are frozen: `PF-GOV={internal/context/**,internal/governance/**,internal/authority/**,cmd/abcp/**}`; `PF-BACKEND={internal/authoritybackend/**,cmd/abcp/**,go.mod,go.sum}`; `PF-RUN={internal/run/**,internal/acceptance/**,cmd/abcp/**}`; `PF-MERGE={internal/mergelifecycle/**,cmd/abcp/**}`; `PF-PR={internal/prlifecycle/**,cmd/abcp/**}`; `PF-ACCEPT={scripts/acceptance/assurance-go-test-exact.py,scripts/acceptance/assurance-graph-validate.py,scripts/acceptance/assurance-lifecycle-integration.sh,scripts/acceptance/assurance-runner-preflight.sh}`; `PF-SECRET={scripts/acceptance/assurance-secret-scan.py,internal/run/**,internal/authoritybackend/**,cmd/abcp/**}`; `PF-RESOURCE={internal/run/**,internal/acceptance/**,scripts/acceptance/assurance-runner-preflight.sh,cmd/abcp/**}`.

Every runtime finding has a unique `finding_id`, exactly one frozen `rule_id=AG-PO-nnn`, classification and evidence digest; reviewer/model output is **never path authority**. The controller recomputes `changed_paths` from the exact accepted-B base→failed-candidate Git diff, expands the frozen semantic rule scope, intersects those with the original accepted-B allowed paths, and publishes `ControllerAffectedPathSetV1`. For implementation findings, `derived_allowed_paths` is the union of each validated finding's `derived_affected_paths`; every member must therefore be in `rule scope ∩ original B scope ∩ controller-recomputed changed paths`. Empty intersection, a required correction outside that intersection, an unmapped rule, or any model gap returns A. The bound final-review profile is the canonical `ABCP_XHIGH_FRESH_V1` vector above.

| Rule | Relation | Scope |
|---|---|---|
| AG-PO-001 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-002 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-003 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-004 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-005 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-006 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-007 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-008 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-009 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-010 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-011 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-012 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-013 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-014 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-015 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-016 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-017 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-018 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-019 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-020 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-021 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-022 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-023 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-024 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-025 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-026 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-027 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-028 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-029 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-030 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-031 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-032 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-033 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-034 | IMPLEMENTATION_CORRECTABLE | PF-BACKEND |
| AG-PO-035 | IMPLEMENTATION_CORRECTABLE | PF-RUN |
| AG-PO-036 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-037 | IMPLEMENTATION_CORRECTABLE | PF-MERGE |
| AG-PO-038 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-039 | IMPLEMENTATION_CORRECTABLE | PF-ACCEPT |
| AG-PO-040 | IMPLEMENTATION_CORRECTABLE | PF-ACCEPT |
| AG-PO-041 | IMPLEMENTATION_CORRECTABLE | PF-SECRET |
| AG-PO-042 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-043 | IMPLEMENTATION_CORRECTABLE | PF-MERGE |
| AG-PO-044 | IMPLEMENTATION_CORRECTABLE | PF-RUN |
| AG-PO-045 | IMPLEMENTATION_CORRECTABLE | PF-MERGE |
| AG-PO-046 | IMPLEMENTATION_CORRECTABLE | PF-BACKEND |
| AG-PO-047 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-048 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-049 | IMPLEMENTATION_CORRECTABLE | PF-GOV |
| AG-PO-050 | IMPLEMENTATION_CORRECTABLE | PF-BACKEND |
| AG-PO-051 | IMPLEMENTATION_CORRECTABLE | PF-RESOURCE |
| AG-PO-052 | IMPLEMENTATION_CORRECTABLE | PF-SECRET |
| AG-PO-053 | IMPLEMENTATION_CORRECTABLE | PF-RUN |
| AG-PO-054 | IMPLEMENTATION_CORRECTABLE | PF-PR |
| AG-PO-055 | IMPLEMENTATION_CORRECTABLE | PF-MERGE |

### Executable validity and drain-before-upgrade

`HistoricalValid(record)` validates immutable canonical bytes and full digest ancestor chain under the schema that created them. `OperationallyUsable(record,state,operation)` additionally requires same repository/controller/A lineage, exact current generation/tip, no invalidation/A-required flag, correct operation, and any existing time-bound lease not expired. Resource/evidence/accounting CAS revisions do **not** invalidate stage authority; only stage/effect transition/invalidation CAS changes stage generation/tip.

`V3Drained(v1)` is true only when `ActiveInvocation=nil`; no mutation lease is ISSUED/STARTED; the bound append-only ledger has no current `READY_FOR_MERGE` or unresolved submission barrier; `internal/prlifecycle` and `internal/mergelifecycle` durable stores have no unresolved write/UNKNOWN effect for the bound repository/run; and either (a) no V3 lineage was ever started (`BCapsuleSHA256==""` and `CheckpointTip=nil`) or (b) `CheckpointTip.Kind==POST_MERGE_ACCEPTED`. Historical review/finding/grant/derivation fields may remain as evidence but cannot be operational. V4 activation rejects otherwise with `V3_DRAIN_REQUIRED`. Therefore this design has **no live V3 grandfather continuation** and `GovernanceActivationV2.grandfathered_v3` is absent/empty by construction.

Upgrade stores exact terminal `ControllerStateV1` canonical bytes in the immutable artifact backend, verifies the decoded V1 `Revision=r`, then the bootstrap/upgrade CAS creates `ControllerStateV2` at revision `r+1` referencing that artifact/revision. No V1 field is reinterpreted or discarded while live because activation is impossible until drained.

## Successor activation and immutable artifact authority

`GovernanceActivationV2` installs V4 only from exact existing merge-authorized + post-merge-accepted authority for the policy merge commit, exact installed predecessor activation, authenticated principal and protected base. The controller recomputes the exact five manifest files above. V4 activation additionally requires `V3Drained`, the immutable predecessor artifact, and the PostgreSQL database-isolation bootstrap below.

Every digest referenced by V2 state/capsules/checkpoints/grants/model/evidence must resolve in `abcp_v4.abcp_authority_artifact_v1` in the same authenticated domain. Artifact publication is create-or-verify: compute digest/size before INSERT; commit immutable artifact first; conflicting same digest/kind/bytes is integrity failure; state CAS may then reference only an already durable artifact. A crash between artifact commit and state CAS leaves a harmless orphan included in artifact inventory. Fresh hosts traverse ancestors exclusively by these digests; host-local files are never authority.

## Durable publication and remote-effect recovery matrix

| Family | Atomic authority boundary | Crash/ambiguity semantics |
|---|---|---|
| immutable artifact | artifact INSERT/create-or-verify commit | orphan is non-authoritative; conflict/inaccessibility fails closed |
| stage/checkpoint/grant/evidence selection | expected-revision V2 CAS referencing durable artifact | ambiguous CAS is read-back/reconcile only; no second mutation until exact applied/not-applied proof |
| resource reservation | expected-revision V2 CAS charges lineage+worker vector and assigns ordinal | subprocess/container cannot start before applied reservation; unknown cleanup remains maximum-charged |
| failed stage / correction grant | failed-stage invalidation CAS, then separately derived correction-grant CAS | finding packet alone has zero mutation authority; grant paths are registry-derived |
| PR effect | `PREffectIntentV1` durable → one CAS to `PR_SUBMITTING` + `PREffectClaimV1` token hash | exactly one winning process retains raw 256-bit token in a one-shot in-memory capability; loss/possible HTTP submission is reconciliation-only; new write requires exact NOT_APPLIED + new intent/claim |
| merge effect | `MergeEffectIntentV1` durable → one CAS to `MERGE_SUBMITTING` + `MergeEffectClaimV1` token hash | same one-winner rule; claim is bound to exact READY ledger event, PR/integration/merge tips and provider request; after possible submission no blind retry |
| post-merge | exact terminal ledger event/evidence then V2 settlement CAS | ledger result remains factual run truth; failed acceptance emits `PostMergeFailureV1`, sets PG non-completion, no automatic rollback |

Raw effect tokens are generated from 256 bits of OS CSPRNG immediately before the winning claim CAS; only SHA-256(token) is durable. The provider adapter accepts an in-memory `OneShotEffectCapability` object whose `Take()` atomically succeeds once and verifies the raw token against state hash before crossing the HTTP/provider boundary. Another host can read the hash but never obtains the capability. If the winner dies before/after the call, no process may reconstruct/reuse it; only read-only provider reconciliation may establish `NOT_APPLIED`/`APPLIED`/`RECOVERY_REQUIRED`.

## Frozen resource authority

Validator authority—not the caller—binds `resource_profile_id`, `secret_capability`, `requires_postgres`, and `requires_container`. Reservation must equal or strengthen those exact values and cannot substitute a profile. Worker admission capacity is frozen at 12 GiB aggregate reserved memory, 384 aggregate PIDs, 6 aggregate CPU equivalents (`600000/100000`) and 4096 aggregate NOFILE units; devagent preflight additionally requires ≥8 logical CPUs and ≥16 GiB physical RAM. At most three normal validators are active, while DOGFOOD+PostgreSQL consumes its combined reservation and excludes any combination exceeding worker capacity.

Profiles: `NORMAL_V1`: process 2 GiB/64 PIDs/2 CPU/NOFILE256/30m/output16+16MiB/evidence32MiB/transient512MiB; `SECRET_CAPABLE_V1`: same but raw output retention 0; `DOGFOOD_V1`: process 4 GiB/128 PIDs/4 CPU/NOFILE512/90m/evidence128MiB/transient2GiB; `POSTGRES_CONTAINER_V1`: container 1 GiB/128 PIDs/2 CPU/tmpfs576MiB/shm64MiB/no logs/read-only root. A validator requiring PostgreSQL atomically reserves both its process profile and container profile against the worker vector.

User-process validators use controller-created delegated cgroup v2 children plus `prlimit`; source/module inputs are read-only and scratch/GOCACHE are bounded tmpfs. Docker uses its independent daemon cgroup (the host uses systemd Docker cgroup driver, so the design does **not** pretend the container is under the user delegated cgroup); instead the controller separately reserves the complete container vector and enforces Docker `--memory=1g --memory-swap=1g --pids-limit=128 --cpus=2 --ulimit nofile=256:256 --read-only --shm-size=64m --log-driver=none` plus bounded tmpfs. Shared pre-pulled immutable image layers are trusted runner baseline, not lineage scratch; container writable layer is read-only and all writable mounts are bounded tmpfs. Create ambiguity is resolved by reservation-derived exact name/image/labels before another create; cleanup proves container/name absent before releasing its reserved vector.

Lineage ceilings remain ≤1024 invocations, ≤512 rejected attempts, ≤128 external observations, ≤2048 retained artifact refs, ≤512MiB retained evidence, ≤8GiB cumulative transient bytes, ≤24h aggregate validator elapsed, and exactly two correction grants. Subject/process/restart/child churn cannot reset them.

## PostgreSQL authority-domain and artifact backend

Implementation is `PostgresWorkflowAuthorityBackendV1` under `internal/authoritybackend/**` with `github.com/jackc/pgx/v5 v5.7.6` (Go 1.23 compatible). Schema is fixed `abcp_v4`; bootstrap owner is never used by normal execution. Bootstrap revokes `CREATE` on database public schema from PUBLIC and `ALL` on `abcp_v4`/tables/sequences from PUBLIC; runtime roles are LOGIN but `NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`, do not own schema/tables, and receive no role memberships permitting bypass. Runtime sessions set/verify `search_path=pg_catalog,abcp_v4` and reject any mismatch.

```sql
CREATE SCHEMA abcp_v4 AUTHORIZATION <bootstrap_owner>;
CREATE TABLE abcp_v4.abcp_authority_domain_v1 (
  authority_domain text PRIMARY KEY CHECK (length(authority_domain) BETWEEN 1 AND 128),
  runtime_role name NOT NULL UNIQUE,
  bootstrap_provenance_sha256 char(64) NOT NULL CHECK (bootstrap_provenance_sha256 ~ '^[0-9a-f]{64}$')
);
CREATE TABLE abcp_v4.abcp_workflow_authority_v1 (
  authority_domain text NOT NULL REFERENCES abcp_v4.abcp_authority_domain_v1(authority_domain) ON DELETE RESTRICT,
  controller_identity char(64) NOT NULL CHECK (controller_identity ~ '^[0-9a-f]{64}$'),
  revision bigint NOT NULL CHECK (revision > 0), canonical_state bytea NOT NULL CHECK (octet_length(canonical_state) BETWEEN 1 AND 1048576),
  state_sha256 char(64) NOT NULL CHECK (state_sha256 ~ '^[0-9a-f]{64}$'), updated_at timestamptz NOT NULL,
  PRIMARY KEY(authority_domain,controller_identity)
);
CREATE TABLE abcp_v4.abcp_authority_artifact_v1 (
  authority_domain text NOT NULL REFERENCES abcp_v4.abcp_authority_domain_v1(authority_domain) ON DELETE RESTRICT,
  artifact_sha256 char(64) NOT NULL CHECK (artifact_sha256 ~ '^[0-9a-f]{64}$'), artifact_kind text NOT NULL,
  lineage_sha256 char(64), stage text NOT NULL, artifact_sequence bigint GENERATED ALWAYS AS IDENTITY,
  canonical_bytes bytea NOT NULL CHECK (octet_length(canonical_bytes) BETWEEN 1 AND 33554432), byte_size bigint NOT NULL,
  created_at timestamptz NOT NULL, PRIMARY KEY(authority_domain,artifact_sha256),
  UNIQUE(artifact_sequence),
  CHECK (lineage_sha256 IS NULL OR lineage_sha256 ~ '^[0-9a-f]{64}$'),
  CHECK (byte_size=octet_length(canonical_bytes))
);
```

Exact isolation DDL additionally includes:

```sql
ALTER TABLE abcp_v4.abcp_authority_domain_v1 ENABLE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_authority_domain_v1 FORCE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_workflow_authority_v1 ENABLE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_workflow_authority_v1 FORCE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_authority_artifact_v1 ENABLE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_authority_artifact_v1 FORCE ROW LEVEL SECURITY;
CREATE POLICY abcp_domain_self_select ON abcp_v4.abcp_authority_domain_v1 FOR SELECT USING (runtime_role=current_user);
CREATE POLICY abcp_workflow_self_select ON abcp_v4.abcp_workflow_authority_v1 FOR SELECT USING (authority_domain IN (SELECT authority_domain FROM abcp_v4.abcp_authority_domain_v1 WHERE runtime_role=current_user));
CREATE POLICY abcp_workflow_self_update ON abcp_v4.abcp_workflow_authority_v1 FOR UPDATE USING (authority_domain IN (SELECT authority_domain FROM abcp_v4.abcp_authority_domain_v1 WHERE runtime_role=current_user)) WITH CHECK (authority_domain IN (SELECT authority_domain FROM abcp_v4.abcp_authority_domain_v1 WHERE runtime_role=current_user));
CREATE POLICY abcp_artifact_self_select ON abcp_v4.abcp_authority_artifact_v1 FOR SELECT USING (authority_domain IN (SELECT authority_domain FROM abcp_v4.abcp_authority_domain_v1 WHERE runtime_role=current_user));
CREATE POLICY abcp_artifact_self_insert ON abcp_v4.abcp_authority_artifact_v1 FOR INSERT WITH CHECK (authority_domain IN (SELECT authority_domain FROM abcp_v4.abcp_authority_domain_v1 WHERE runtime_role=current_user));
REVOKE ALL ON SCHEMA abcp_v4 FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA abcp_v4 FROM PUBLIC;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA abcp_v4 TO <runtime_role>;
GRANT SELECT ON abcp_v4.abcp_authority_domain_v1 TO <runtime_role>;
GRANT SELECT,UPDATE ON abcp_v4.abcp_workflow_authority_v1 TO <runtime_role>;
GRANT SELECT,INSERT ON abcp_v4.abcp_authority_artifact_v1 TO <runtime_role>;
GRANT USAGE,SELECT ON SEQUENCE abcp_v4.abcp_authority_artifact_v1_artifact_sequence_seq TO <runtime_role>;
```

All three tables use `ENABLE ROW LEVEL SECURITY` and `FORCE ROW LEVEL SECURITY`. Domain SELECT policy is `runtime_role=current_user`. Workflow SELECT/UPDATE and artifact SELECT/INSERT policies require `authority_domain IN (SELECT authority_domain FROM abcp_v4.abcp_authority_domain_v1 WHERE runtime_role=current_user)` for both USING/WITH CHECK. Runtime receives only USAGE schema, SELECT on its RLS-filtered domain row, SELECT/UPDATE on workflow, SELECT/INSERT on artifact; **no UPDATE on domain**, no DELETE/TRUNCATE/DDL. Bootstrap verifies exact policies, FORCE flags, grants, owner identities and role attributes on every open. Direct SQL with another domain is denied by RLS even with a caller-supplied domain string.

Bootstrap requires a drained V1 artifact and inserts initial V2 workflow row at `revision=v1.revision+1`; it never inserts revision 1 blindly. Artifact create-or-verify uses `INSERT ... ON CONFLICT DO NOTHING` followed by same-transaction SELECT and application SHA/kind/size/byte equality; conflicting bytes fail. State CAS is one bounded transaction and RLS-authenticated current-user domain. Application independently hashes loaded state/artifact bytes. Commit ambiguity is fresh-connection read-back only.

Connection bounds remain connect 5s, operation/transaction 15s, lock 2s, statement 10s, idle-in-transaction 15s, pool MaxConns4/Min0/lifetime30m/idle5m/health30s. Harness is loopback SCRAM with `sslmode=disable`; non-loopback operator DB requires `verify-full` + CA/hostname. The exact PostgreSQL image remains `postgres@sha256:f3bd19c606e442c3d7bdfa8002e03fe260a1023351e0ea4598032022b68dd6e3` linux/amd64 17.6-bookworm.

Existing durable CLI names that open controller state (`run`, `governance-usage-validate` when stateful, `governance-checkpoint-validate`, `governance-review-advance`, `governance-lease-issue`, `governance-lease-begin`, `governance-receipt-validate`, `governance-activation-install`) and new V4 commands (`governance-backend-bootstrap`, `governance-state-upgrade`, `assurance-evidence-select`, `assurance-failed-stage-record`, `assurance-correction-grant`, `assurance-integration-advance`, `assurance-pr-publish`, `assurance-merge-authorize`, `assurance-merge-effect-claim`, `assurance-merge-reconcile`, `assurance-post-merge-advance`) use the common PostgreSQL composition. Parse-only validation may remain backendless.

## Evidence-class applicability

`unit`, `static`, `contract`, `fault_injection`, `race_concurrency`, `crash_restart`, `resource`, `replay_idempotency`, `security_negative`, `smoke`, `integration`, `migration`, and `e2e` are applicable. `runtime` and `production` are N/A because this pack installs/tests controller/CLI governance but deploys no long-running ABCP production service; external-build readiness remains a later separately authorized deployment claim.

## Validator registry

Every Go test runs only through `scripts/acceptance/assurance-go-test-exact.py` with explicit `--count`; wrapper always forwards `-count=N`, rejects cached package results/zero-match, and records parsed run/pass/fail events. `ValidatorV1` authority below also freezes resource/secret/container properties; callers cannot weaken them.

| Validator ID | Stage | Exact command / authority | Profile / secret | Purpose |
|---|---|---|---|---|
| AG-VAL-B-001 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --suite ./internal/context ./internal/governance ./internal/authority ./internal/authoritybackend/... ./internal/mergelifecycle/... ./internal/prlifecycle/... --count 1 --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/governance --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/authoritybackend/postgres --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/mergelifecycle --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/prlifecycle` | NORMAL_V1/NONE | contracts suite |
| AG-VAL-B-002 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceConcurrentReplayAndDerivation --count 10 --race` | NORMAL_V1/NONE | authority concurrency |
| AG-VAL-B-003 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssurancePublicationCrashMatrix --count 1` | NORMAL_V1/NONE | crash matrix |
| AG-VAL-B-004 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceCancellationAndObservationAmbiguity --count 1` | NORMAL_V1/NONE | ambiguity |
| AG-VAL-B-005 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceEvidenceAndStageBinding --count 1` | NORMAL_V1/NONE | evidence semantics |
| AG-VAL-B-006 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceScopeClassificationAndPlanImmutability --count 1` | NORMAL_V1/NONE | docs bypass |
| AG-VAL-B-007 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceResourceReservationAccounting --count 10 --race` | NORMAL_V1/NONE | resource accounting |
| AG-VAL-B-008 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceCanonicalUnitSemantics --count 1` | NORMAL_V1/NONE | unit semantics |
| AG-VAL-B-009 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/authoritybackend/postgres --test TestPostgresWorkflowAuthorityBackendCAS --count 10 --race` | SECRET_CAPABLE_V1/POSTGRES + POSTGRES_CONTAINER_V1 | backend CAS |
| AG-VAL-B-010 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceOperationalValidity --count 1` | NORMAL_V1/NONE | validity |
| AG-VAL-B-011 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-graph-validate.py --model docs/plans/assurance-proof-obligation-governance-enforcement-assurance.md` | NORMAL_V1/NONE | semantic graph |
| AG-VAL-B-012 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceWireStateUpgradeAndDrain --count 1` | NORMAL_V1/NONE | wire/drain-only upgrade |
| AG-VAL-B-013 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceEffectClaimSingleWinner --count 20 --race` | NORMAL_V1/NONE | one-winner PR/merge claim |
| AG-VAL-B-014 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/authoritybackend/postgres --test TestPostgresDomainIsolationArtifactStore --count 10 --race` | SECRET_CAPABLE_V1/POSTGRES + POSTGRES_CONTAINER_V1 | RLS/artifact/fresh-host |
| AG-VAL-B-015 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceDrainUpgradeAndSemanticRegistry --count 1` | NORMAL_V1/NONE | drain + correction derivation |
| AG-VAL-B-016 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceResourceProfileBinding --count 10 --race` | NORMAL_V1/NONE | profile no-downgrade/full vector |
| AG-VAL-B-017 | B_IMPLEMENTATION | `python3 scripts/acceptance/assurance-secret-scan.py --stage B_IMPLEMENTATION --inventory ${B_ARTIFACT_INVENTORY}` | SECRET_CAPABLE_V1/POSTGRES | B credential inventory scan |
| AG-VAL-C-001 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --suite ./... --count 1 --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/governance --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/run --require-package github.com/pankajleh/autonomous-builder-control-plane/cmd/abcp` | NORMAL_V1/NONE | full tests |
| AG-VAL-C-002 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --suite ./... --count 1 --race --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/governance --require-package github.com/pankajleh/autonomous-builder-control-plane/internal/run` | NORMAL_V1/NONE | race suite |
| AG-VAL-C-003 | C_BRANCH_ACCEPTANCE | `go vet ./...` | NORMAL_V1/NONE | static only |
| AG-VAL-C-004 | C_BRANCH_ACCEPTANCE | `git diff --check ${A_ACCEPTED_HEAD}...${CANDIDATE_HEAD}` | NORMAL_V1/NONE | diff hygiene only |
| AG-VAL-C-005 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./cmd/abcp --test TestAssuranceFreshSessionAuthoritySmoke --count 1` | SECRET_CAPABLE_V1/POSTGRES + POSTGRES_CONTAINER_V1 | J-01 |
| AG-VAL-C-006 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceFailedStageCorrectionReentryIntegration --count 1` | NORMAL_V1/NONE | J-02 |
| AG-VAL-C-007 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssuranceGapReturnsToAIntegration --count 1` | NORMAL_V1/NONE | J-03 |
| AG-VAL-C-008 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./cmd/abcp --test TestAssuranceDocumentationBypassSmoke --count 1` | NORMAL_V1/NONE | J-04 |
| AG-VAL-C-009 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/governance --test TestAssurancePhysicalResourceAndCrashStress --count 10 --race` | NORMAL_V1/NONE | physical resource stress |
| AG-VAL-C-010 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/mergelifecycle --test TestAssuranceMergeEffectClaimInvalidationRace --count 20 --race` | NORMAL_V1/NONE | merge claim race |
| AG-VAL-C-011 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-secret-scan.py --stage C_BRANCH_ACCEPTANCE --inventory ${C_ARTIFACT_INVENTORY}` | SECRET_CAPABLE_V1/POSTGRES | C credential inventory scan |
| AG-VAL-C-012 | C_BRANCH_ACCEPTANCE | `scripts/acceptance/assurance-runner-preflight.sh --profile NORMAL_V1 --postgres-required` | NORMAL_V1/NONE | runner capacity |
| AG-VAL-C-013 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/prlifecycle --test TestAssurancePREffectClaimInvalidationRace --count 20 --race` | NORMAL_V1/NONE | PR effect race |
| AG-VAL-C-014 | C_BRANCH_ACCEPTANCE | `python3 scripts/acceptance/assurance-go-test-exact.py --package ./internal/mergelifecycle --test TestAssuranceLedgerPostgresHandoffCrashMatrix --count 1` | NORMAL_V1/NONE | ledger/PG crash ordering |
| AG-VAL-I-001 | INTEGRATION_ACCEPTANCE | `scripts/acceptance/assurance-governance-dogfood.sh ${A_ACCEPTED_HEAD} ${CANDIDATE_HEAD} ${PARENT_RESOURCE_RESERVATION}` | DOGFOOD_V1/CODEX_PROVIDER + POSTGRES_CONTAINER_V1 | J-05 |
| AG-VAL-I-002 | INTEGRATION_ACCEPTANCE | `scripts/acceptance/assurance-lifecycle-integration.sh ${CANDIDATE_HEAD} ${C_STAGE_TIP}` | SECRET_CAPABLE_V1/POSTGRES + POSTGRES_CONTAINER_V1 | real PR/merge/ledger coordination |
| AG-VAL-I-003 | INTEGRATION_ACCEPTANCE | `python3 scripts/acceptance/assurance-secret-scan.py --stage INTEGRATION_ACCEPTANCE --inventory ${I_ARTIFACT_INVENTORY}` | SECRET_CAPABLE_V1/CODEX_PROVIDER | integration inventory scan |
| AG-VAL-PM-001 | POST_MERGE_ACCEPTANCE | `scripts/acceptance/assurance-governance-postmerge.sh ${MERGE_RESULT}` | SECRET_CAPABLE_V1/POSTGRES + POSTGRES_CONTAINER_V1 | merged-state/fresh-host smoke |
| AG-VAL-PM-002 | POST_MERGE_ACCEPTANCE | `python3 scripts/acceptance/assurance-secret-scan.py --stage POST_MERGE_ACCEPTANCE --inventory ${PM_ARTIFACT_INVENTORY}` | SECRET_CAPABLE_V1/POSTGRES | post-merge inventory scan |

## Evidence and secret inventory authority

Every attempt first publishes `AssuranceResourceReservationV1`; then execution emits an `ExecutionIdentityV1`, evidence/resource records and an immutable artifact row. The artifact backend assigns a database-global monotonically increasing identity `artifact_sequence`; inventory queries are RLS-domain-filtered and additionally bind lineage+stage, so callers cannot skip same-lineage artifacts by choosing a sequence. `RetainedArtifactInventoryV1` is complete through a frozen sequence and enumerates **all** authoritative retained PASS/FAIL/VALIDATION_UNAVAILABLE/unselected/orphan artifacts in that lineage+stage; the controller queries the artifact table itself, so a caller cannot omit entries. A positive secret match prevents stage PASS and stores only a redacted `SecretEgressIncidentV1` metadata record; offending raw bytes/raw output are never inserted into the authority artifact table.

Real upstream provider credentials never enter the Codex process, repository, child environment, argv, config file, or evidence. J-05 starts a controller-owned loopback-only `CodexProviderBrokerV1` that holds the upstream credential in controller memory/FD and exposes only the exact OpenAI Responses-compatible routes required by the pinned client. Codex 0.149.0 is invoked with `model_provider="abcp-broker"` and an ephemeral config equivalent to `[model_providers.abcp-broker] base_url="http://127.0.0.1:<reserved-port>/v1", env_key="ABCP_BROKER_SESSION_TOKEN", wire_api="responses", requires_openai_auth=false`; the session token is 256-bit controller CSPRNG, short-lived, bound to one execution identity/model/request budget, and is not an upstream credential. `shell_environment_policy.exclude=["ABCP_BROKER_SESSION_TOKEN"]` is mandatory. Before any real broker session, the exact Codex sandbox/config runs harmless-sentinel probes proving (a) the token-name is absent from tool-shell environment, (b) a tool subprocess cannot read a same-shaped marker from the Codex parent process environment/proc view, and (c) a tool subprocess cannot connect to the broker loopback endpoint while the Codex client can. Any readable marker, reachable tool connection, inability to prove the parent-process boundary, provider/config drift, or probe ambiguity is `VALIDATION_UNAVAILABLE`; no real provider credential/session is then started. The broker rejects non-loopback clients, wrong/expired/replayed session tokens, unbound model identities and non-allowlisted routes; strips inbound authorization before injecting the upstream credential; and never logs either credential or session token.

PostgreSQL/Docker passwords are controller-generated into 0400 files in an ephemeral 0700 directory outside repo/evidence roots; Docker receives only `POSTGRES_PASSWORD_FILE=/run/secrets/postgres_password` and a read-only bind of that file. Secret-capable process raw stdout/stderr is scanned in bounded memory and discarded; only structured secret-free results are retained. Exact secret values (including the broker session token and upstream credential inside the controller boundary) are delivered to `assurance-secret-scan.py` over controller-owned FD3 length frames, never argv/env/file evidence.

A credential-bearing stage cannot publish its evidence-selection checkpoint until its complete inventory and matching stage-local PO-041/PO-052 zero-match scan are PASS. B, C, integration and post-merge each have such cells.

## Strict evidence-cell registry

One row is exactly one proof/class/stage/subject/validator. Existing AG-E-001..062 retain their meanings from the prior A revision except AG-PO-041 now additionally requires stage-local inventory cells below. New closure cells are:

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
| AG-E-063 | AG-PO-045 | race_concurrency | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-013 |
| AG-E-064 | AG-PO-045 | integration | INTEGRATION_ACCEPTANCE | exact merge effect subject | AG-VAL-I-002 |
| AG-E-065 | AG-PO-046 | security_negative | B_IMPLEMENTATION | exact PostgreSQL authority domain | AG-VAL-B-014 |
| AG-E-066 | AG-PO-046 | integration | INTEGRATION_ACCEPTANCE | real multi-role PostgreSQL | AG-VAL-I-002 |
| AG-E-067 | AG-PO-047 | contract | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-015 |
| AG-E-068 | AG-PO-048 | contract | B_IMPLEMENTATION | frozen semantic registry | AG-VAL-B-015 |
| AG-E-069 | AG-PO-049 | migration | B_IMPLEMENTATION | exact drained V1→V2 transition | AG-VAL-B-015 |
| AG-E-070 | AG-PO-050 | contract | B_IMPLEMENTATION | immutable artifact backend | AG-VAL-B-014 |
| AG-E-071 | AG-PO-050 | crash_restart | C_BRANCH_ACCEPTANCE | fresh-host artifact traversal | AG-VAL-C-014 |
| AG-E-072 | AG-PO-051 | resource | B_IMPLEMENTATION | frozen validator/profile set | AG-VAL-B-016 |
| AG-E-073 | AG-PO-051 | resource | C_BRANCH_ACCEPTANCE | physical worker/container capacity | AG-VAL-C-012 |
| AG-E-074 | AG-PO-052 | security_negative | B_IMPLEMENTATION | complete B artifact inventory | AG-VAL-B-017 |
| AG-E-075 | AG-PO-052 | security_negative | C_BRANCH_ACCEPTANCE | complete C artifact inventory | AG-VAL-C-011 |
| AG-E-076 | AG-PO-052 | security_negative | INTEGRATION_ACCEPTANCE | complete integration artifact inventory | AG-VAL-I-003 |
| AG-E-077 | AG-PO-052 | security_negative | POST_MERGE_ACCEPTANCE | complete post-merge artifact inventory | AG-VAL-PM-002 |
| AG-E-078 | AG-PO-053 | e2e | INTEGRATION_ACCEPTANCE | exact delegated J-05 child | AG-VAL-I-001 |
| AG-E-079 | AG-PO-054 | race_concurrency | C_BRANCH_ACCEPTANCE | unchanged C+integration tip | AG-VAL-C-013 |
| AG-E-080 | AG-PO-054 | integration | INTEGRATION_ACCEPTANCE | real PR effect claim/reconcile | AG-VAL-I-002 |
| AG-E-081 | AG-PO-055 | crash_restart | C_BRANCH_ACCEPTANCE | exact ledger↔PG handoff | AG-VAL-C-014 |
| AG-E-082 | AG-PO-055 | integration | INTEGRATION_ACCEPTANCE | real ledger/PR/merge coordination | AG-VAL-I-002 |
| AG-E-083 | AG-PO-041 | security_negative | B_IMPLEMENTATION | complete B artifact inventory | AG-VAL-B-017 |
| AG-E-084 | AG-PO-041 | security_negative | POST_MERGE_ACCEPTANCE | complete post-merge inventory | AG-VAL-PM-002 |

All AG-PO-001..055 must have at least one exact cell. The graph validator expands the preserved AG-E-001..062 plus AG-E-063..084 and rejects any duplicate, missing proof/validator, stage mismatch, unused validator, profile/capability downgrade, or missing AG-PO-029 J-01..J-05 row.

## Critical journeys

**J-01 / AG-VAL-C-005 — Fresh-session authority smoke.** Real Git + real PostgreSQL. Start from an exact **drained** V1 state and immutable predecessor artifact, bootstrap V2 at `r+1`, install V4 manifest, derive A→B→C, then prove wrong commit/principal/domain/schema/artifact/capsule/candidate inputs fail before mutation. A non-drained V3 state must fail `V3_DRAIN_REQUIRED`.

**J-02 / AG-VAL-C-006 — bounded correction.** C1 implementation finding → failed-stage → correction-B1; replacement C2 → correction-B2; restart C3 blocker → no third grant/A-required. Reviewer/finding paths are ignored as authority; controller derives correction paths only from frozen semantic rule scope + original B scope + controller-recomputed exact accepted-B base→failed-candidate Git diff paths. Repeat for integration-stage finding before PR publication.

**J-03 / AG-VAL-C-007 — assurance-gap return A.** Missing/misclassified scenario/N-A, inadequate proof, missing boundary, wrong cell/subject/profile, or disconnected semantic rule produces `ASSURANCE_MODEL_GAP`, zero correction grant, A-required.

**J-04 / AG-VAL-C-008 — documentation bypass.** Controller-owned non-authoritative docs-only classification passes only for eligible docs; governance/runtime byte appears → exact-diff revalidation invalidates exemption before acceptance.

**J-05 / AG-VAL-I-001 — deterministic delegated dogfood.** Frozen baseline contains exactly four files:

- `docs/plans/unique-sorted.md` 497 bytes SHA-256 `9aeb0465e37a67f280b27ab7994f50a815e1b76e13d24dbb686db977f22b6500`;
- `go.mod` 41 bytes SHA-256 `890f65191c956c6601f033eefc794abff1d63cdc05479e17f3309b68181face3`;
- `numbers/numbers.go` 148 bytes SHA-256 `ba8c8918c5c3a6b354962dd663f5b65fe04160a814d72c9f64097456f2851e48`;
- `numbers/numbers_test.go` 536 bytes SHA-256 `8a87e111af64b9534bf23aa5f90b43f924e439ab01ab124a64b92f03a90489ae`.

The other three file bytes are also frozen exactly:

`go.mod`
```text
module example.com/abcp-dogfood

go 1.23
```

`numbers/numbers.go`
```go
package numbers

// UniqueSorted returns the sorted unique values from in without mutating in.
func UniqueSorted(in []int) []int {
	panic("TODO")
}
```

`numbers/numbers_test.go`
```go
package numbers_test

import (
	"reflect"
	"testing"

	"example.com/abcp-dogfood/numbers"
)

func TestUniqueSorted(t *testing.T) {
	in := []int{3, -1, 3, 2, -1}
	before := append([]int(nil), in...)
	got := numbers.UniqueSorted(in)
	want := []int{-1, 2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if !reflect.DeepEqual(in, before) {
		t.Fatalf("input mutated: got %v want %v", in, before)
	}
	if gotNil := numbers.UniqueSorted(nil); len(gotNil) != 0 {
		t.Fatalf("nil input: got %v", gotNil)
	}
}
```

Fixture-set digest over sorted `path NUL decimal-length NUL bytes` is `7dcaed92fb671a8c7961dd535f06d704a4f10ead8af84f86e47f7243fdf0b6b0`. With fixed `ABCP Dogfood <dogfood@example.invalid>`, UTC `2000-01-01T00:00:00Z`, branch `main`, message `baseline`, the exact baseline commit is `eb301906130c0d7ac13b7d90942d469753ab7708`, tree `6b50353385765d0809844bc6ef2415ce112f7e2b`.

The exact plan bytes are:

```markdown
# Plan: Implement UniqueSorted

## Overview
Implement numbers.UniqueSorted for the frozen ABCP dogfood fixture. Change production code only.

## Validation Commands
- `go test -count=1 ./...`

### Task 1: Implement UniqueSorted
- [ ] Implement numbers.UniqueSorted to return ascending unique integers without mutating input.
- [ ] Preserve nil-or-empty semantics for nil input.
- [ ] Do not modify go.mod, tests, or the plan requirements.
- [ ] Run `go test -count=1 ./...`.
- [ ] Mark completed.
```

`DogfoodChildActivationV1` binds this plan path/hash + baseline commit/tree, `DogfoodPolicyFloorV1`, and the exact `CODEX_BROKER_V1` isolation-profile digest; it authorizes only child `A_DESIGN`. The child A creates a new toy-repository `AssuranceModelV1`/semantic registry/capsule and must achieve child `DESIGN_ACCEPTED` before parent controller may issue child B. It is forbidden to reuse the parent ABCP model digest. Child B/C remain bounded to parent reservation; PR publication, merge, post-merge and nested dogfood are forbidden. Child maxima: 16 validator invocations, 128MiB retained evidence, 2GiB transient, 90m elapsed, one graph. Tool identities remain Ralphex `umputun/ralphex@319e30618352a1b43e4be1b8a894c6c05e6d5fa8` binary `9ad47b083aaaf34eed4b35ca82f3c92d1a3b7641921be0ea606cbd1cd1c3adac`; Codex CLI 0.149.0 `134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477`, model `gpt-5.6-sol`, effort `xhigh`, executor `codex`. Parent AG-VAL-I-001 cannot start the child until the harmless `CODEX_BROKER_V1` isolation probes PASS; a probe failure is evidence-incomplete/validation-unavailable, not permission to fall back to local Codex credentials.

## Functional-plan mutation and B convergence

The A model is immutable in B/C. The functional plan may only progress status/checkboxes or move semantically unchanged to completed. B convergence requires all B cells, including complete B secret inventory scan; C independently executes C cells and exact-head review.

V4 coordination order is exactly:

`DESIGN_ACCEPTED → IMPLEMENTATION_CONVERGED → BRANCH_ACCEPTED → FINAL_REVIEW_CLEAN → INTEGRATION_ACCEPTED → PR_SUBMITTING → PR_PUBLISHED → MERGE_AUTHORIZED → MERGE_SUBMITTING → (MERGE_APPLIED → POST_MERGE_ACCEPTED | MERGE_NOT_APPLIED | POST_MERGE_FAILED | RECOVERY_REQUIRED)`.

### PR effect linearization

After integration, controller seals `PREffectIntentV1`. PR invalidation vs effect claim competes on one PostgreSQL predecessor tip. Winner claim CAS installs `PREffectClaimV1`, sets `PR_SUBMITTING`, and returns the one-shot in-memory token capability only to the CAS winner. `internal/prlifecycle` accepts that capability plus exact intent and refuses any HTTP write otherwise. Process death/timeout after claim is reconciliation-only: read existing PRs/ref/provider evidence for exact request identity. Proven NOT_APPLIED allows a separately governed new intent/claim; APPLIED settles `PR_PUBLISHED`; ambiguous remains `RECOVERY_REQUIRED`. No `internal/githublifecycle/**` mutation is required.

### Merge and ledger↔PostgreSQL handoff

The append-only ledger remains the sole operational run-state truth (`READY_FOR_MERGE`, terminal MERGED/FAILED etc.); PostgreSQL never duplicates those domain states. Instead each V4 stage/effect binds an exact ledger event digest. `MergeEffectIntentV1` can exist only when the current ledger tip is exact `READY_FOR_MERGE` for the same run/candidate and PG is `MERGE_AUTHORIZED`; it binds that READY event digest. The claim CAS sets `MERGE_SUBMITTING` and only the one-shot claim capability can enter `internal/mergelifecycle.Provider.SubmitTarget`.

If provider effect is APPLIED, existing merge lifecycle first durably appends its exact terminal ledger event/result/proof. Only after reopening/verifying that ledger event may PG CAS settle `MERGE_APPLIED` and later `POST_MERGE_ACCEPTED`. If provider is proven NOT_APPLIED, PG may settle `MERGE_NOT_APPLIED`; ambiguity remains `RECOVERY_REQUIRED`. A crash after ledger terminal but before PG settlement is recovered by reading exact bound ledger event; a crash after PG claim but before any ledger terminal cannot infer result and is reconciliation-only. Thus no crash creates two authoritative run states.

Post-merge acceptance failure leaves the factual merge/MERGED ledger event intact, stores `PostMergeFailureV1`, and sets PG coordination state `POST_MERGE_FAILED`; external-build readiness stays false. Any revert/repair is a new governed A/B/C change.

Publication/merge requires all applicable B/C/integration cells PASS, exact-head 0 Critical/0 Major, unchanged clean candidate, complete secret inventories, unexhausted budgets, operationally usable exact tips, and one-winner effect capabilities. B scope includes the minimal required `internal/prlifecycle/**` and `internal/mergelifecycle/**`; `internal/githublifecycle/**` remains frozen.

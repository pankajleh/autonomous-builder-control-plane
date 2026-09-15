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
- `cmd/abcp/**`
- `scripts/acceptance/assurance-governance-dogfood.sh`
- `scripts/acceptance/assurance-governance-postmerge.sh`
- `docs/plans/assurance-proof-obligation-governance-enforcement.md`
- `docs/plans/completed/assurance-proof-obligation-governance-enforcement.md`

`docs/plans/assurance-proof-obligation-governance-enforcement-assurance.md` is immutable throughout B/C. The functional plan may change only task checkbox/status markers and its final byte-identical semantic move to the named completed path; any other semantic plan edit is `MUTATION_SCOPE_VIOLATION`. Any need for another package/path is `SCOPE_EXPANSION_REQUIRED` and returns to A.

Exactly three capsules remain authoritative: A owns this assurance model, B implements against it, and C independently executes branch-stage evidence and performs read-only exact-head review. No reviewer, validator, policy manifest, or evidence artifact is a fourth authority surface.

Finite cumulative final-review correction-reentry ceiling: **2 correction-B lineages per accepted A lineage**. One reentry is consumed only by the atomic durable controller-state CAS that publishes a new correction-B grant. A failed-C report may contain multiple mapped findings but consumes one reentry when one correction-B grant is published. Exact replay consumes zero additional reentries. After two successful correction-B grant publications, any later C implementation blocker returns to A.

## Frozen invariants

| ID | Invariant |
|---|---|
| AG-I01 | Successor assurance activation is authorized only by the existing controller activation operation, exact predecessor activation, exact repository commit, and a strict policy manifest whose file digests are recomputed from that commit. |
| AG-I02 | Every applicable failure scenario has stable referential traceability to invariant → proof obligation → one or more exact evidence cells. |
| AG-I03 | Assurance strengthens A/B/C without creating mutable reviewer authority or a fourth capsule. |
| AG-I04 | Work defaults code-bearing; any documentation-only exemption is controller-owned, path/semantic bounded, and exact-diff revalidated. |
| AG-I05 | Every evidence cell binds one proof obligation, one evidence class, one lifecycle stage, one exact subject rule, and one validator/artifact identity. |
| AG-I06 | B/C cannot weaken the frozen assurance model; model insufficiency returns to A as `ASSURANCE_MODEL_GAP`. |
| AG-I07 | C implementation findings invalidate C and may enter only bounded controller-derived correction-B authority from frozen A evidence. |
| AG-I08 | Assurance records are deterministic, strict, bounded, versioned, create-or-verify idempotent, and do not reinterpret historical V1/V2/V3 bytes. |
| AG-I09 | Fresh sessions fail closed when activation, model, proof, matrix, lineage, repository, or exact-subject bindings are absent, stale, replaced, or inconsistent. |
| AG-I10 | Every new durable authority publication uses durable content-addressed artifact → atomic controller-state CAS; crash/ambiguity cannot fork, reissue, or partially authorize state. |
| AG-I11 | Repository/controller/path/object identity cannot be substituted to obtain another lineage's assurance authority or evidence. |
| AG-I12 | Physical resources, retries, evidence, validator processes, execution time/output, and correction reentries are bounded cumulatively across the accepted A lineage. |
| AG-I13 | A→B, B→C, correction-B→replacement-C, integration, and post-merge derivations preserve the activated assurance digests and mandatory floors transitively. |
| AG-I14 | Cancellation, timeout, and external-observation ambiguity never create implicit retry authority; uncertain durable mutation is reconciled before another mutation attempt. |

## Stable failure-scenario registry

| Scenario ID | Dimension | Scenario | Invariants | Proof obligations |
|---|---|---|---|---|
| AG-S-001 | crash consistency | process dies after successor policy artifact durability but before activation CAS | AG-I01,I10 | AG-PO-001,003,007,020 |
| AG-S-002 | crash consistency | process dies after successor activation CAS response is lost/ambiguous | AG-I01,I10,I14 | AG-PO-003,020,021 |
| AG-S-003 | crash consistency | process dies while publishing A model/grant/checkpoint artifact before controller CAS | AG-I02,I10,I13 | AG-PO-007,009,020 |
| AG-S-004 | crash consistency | process dies while publishing evidence/stage checkpoint | AG-I05,I10 | AG-PO-007,014,015,020 |
| AG-S-005 | crash consistency | process dies while publishing failed-C record or correction-B grant/counter | AG-I07,I10 | AG-PO-017,018,019,020 |
| AG-S-006 | concurrency/races | two successor activation installations race | AG-I01,I10 | AG-PO-003,020 |
| AG-S-007 | concurrency/races | competing A→B/B→C/correction derivations race | AG-I07,I10,I13 | AG-PO-009,010,011,019,020 |
| AG-S-008 | concurrency/races | evidence/stage writers or correction reentries race | AG-I05,I07,I10 | AG-PO-014,015,019,020 |
| AG-S-009 | retry/replay | exact activation/model/grant/checkpoint/evidence request is retried | AG-I08,I10 | AG-PO-003,007 |
| AG-S-010 | retry/replay | conflicting duplicate durable record uses same logical identity with different bytes | AG-I08,I10 | AG-PO-004,007 |
| AG-S-011 | retry/replay | failed-C/correction request is replayed after success/restart | AG-I07,I12 | AG-PO-017,018,019 |
| AG-S-012 | authority identity | policy/model/matrix/candidate bytes or digest are stale/replaced | AG-I01,I09,I13 | AG-PO-001,009,010,011,023 |
| AG-S-013 | authority identity | predecessor V3 authority is reissued or used after successor activation when not grandfathered | AG-I01,I08,I09 | AG-PO-002,005,023 |
| AG-S-014 | partial failure | content-addressed child is durable but no controller-state CAS references it | AG-I10 | AG-PO-007,020 |
| AG-S-015 | partial failure | controller state attempts to reference missing/conflicting child artifact | AG-I09,I10 | AG-PO-007,020,023 |
| AG-S-016 | cancellation | cancellation occurs before durable child artifact | AG-I14 | AG-PO-021 |
| AG-S-017 | cancellation | cancellation/timeout occurs after CAS may have been submitted | AG-I10,I14 | AG-PO-020,021 |
| AG-S-018 | cancellation | validator times out after partial stdout/stderr/evidence | AG-I05,I12,I14 | AG-PO-014,021,025 |
| AG-S-019 | resource exhaustion | assurance model/proof/evidence cardinality or bytes exceed profile | AG-I08,I12 | AG-PO-004,025 |
| AG-S-020 | resource exhaustion | candidate/result churn attempts to reset validator/retry/evidence ceilings | AG-I12 | AG-PO-019,025 |
| AG-S-021 | resource exhaustion | validator leaks descriptors/processes or exceeds memory/output/time/fan-out | AG-I12 | AG-PO-025 |
| AG-S-022 | restart/recovery | fresh process opens after any durable publication boundary | AG-I09,I10 | AG-PO-020,023 |
| AG-S-023 | malformed/forged | duplicate/unknown fields, invalid IDs/stages, forged digests, or oversized canonical input | AG-I02,I08,I12 | AG-PO-004,006,025 |
| AG-S-024 | filesystem identity | repository/controller path, symlink, or object identity is substituted | AG-I09,I11 | AG-PO-023,AG-PO-024 |
| AG-S-025 | external dependency | authoritative Git object/ref is unavailable or delayed | AG-I09,I14 | AG-PO-022,023 |
| AG-S-026 | external dependency | Git/ref observation changes, duplicates, or is inconsistent between observations/use | AG-I09,I14 | AG-PO-022,023 |
| AG-S-027 | compatibility/version skew | eligible pre-successor V3 lineage continues under predecessor policy | AG-I01,I08 | AG-PO-002,AG-PO-005,AG-PO-010 |
| AG-S-028 | compatibility/version skew | unlisted/post-successor V3 lineage attempts predecessor policy | AG-I01,I08,I09 | AG-PO-002,AG-PO-005,AG-PO-010,AG-PO-023 |
| AG-S-029 | security boundary | another repository/controller identity reuses assurance/grant/evidence | AG-I09,I11 | AG-PO-012,023,024 |
| AG-S-030 | model mutation | B attempts to change/N-A/weaken model, matrix, stage, validator, or journey | AG-I02,I06 | AG-PO-006,AG-PO-013,AG-PO-027 |
| AG-S-031 | C authority | C reviewer/finding attempts direct repository mutation | AG-I03,I07 | AG-PO-017,018 |
| AG-S-032 | assurance gap | missing/misclassified scenario or evidence requirement attempts correction B | AG-I06,I07 | AG-PO-013,026 |
| AG-S-033 | documentation bypass | governance/runtime-affecting diff claims `documentation_only` | AG-I04,I09 | AG-PO-008,023 |
| AG-S-034 | evidence binding | evidence from wrong stage/subject or weaker class attempts satisfaction | AG-I05,I09 | AG-PO-014,015,016,027 |
| AG-S-035 | integration boundary | mock/fake substitutes for required production-composition boundary | AG-I05 | AG-PO-015,AG-PO-016,AG-PO-028,AG-PO-030 |
| AG-S-036 | integration/E2E | assembled CLI/controller/Ralphex journey disagrees with isolated tests | AG-I02,I05,I09 | AG-PO-015,AG-PO-016,AG-PO-029,AG-PO-030 |
| AG-S-037 | correction ceiling | two correction-B grants succeed, then a third is attempted after restart/new C | AG-I07,I12 | AG-PO-011, AG-PO-018, AG-PO-019 |
| AG-S-038 | assurance escape | foreseeable Critical/Major first appears in C and continuation is attempted without new A | AG-I06,I07,I09 | AG-PO-026 |

All minimum policy dimensions are applicable. No failure-model dimension is `not_applicable` for this controller-governance implementation.

## Frozen proof obligations

| ID | Falsifiable claim | Invariants |
|---|---|---|
| AG-PO-001 | `AssurancePolicyManifestV1` binds exact predecessor activation digest, exact activation repository commit, successor schema version, and sorted canonical policy-file path+SHA256 entries recomputed from that commit; no ambient digest is trusted. | AG-I01,AG-I08,AG-I09 |
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
| AG-PO-025 | all structural, evidence, retry, validator, process, output, time, concurrency, and reentry limits in the resource profile are lineage-cumulative where specified and physically enforced without reset by subject/candidate churn. | AG-I12 |
| AG-PO-026 | every foreseeable Critical/Major assurance escape is durably recorded and atomically marks the lineage A-required; no further B/C implementation authority may issue until a new A completeness review. | AG-I06,AG-I07,AG-I09 |
| AG-PO-027 | the evidence matrix is strict: one cell = one proof ID + one evidence class + one stage + one subject rule + one validator ID; cells and validator registry are canonical authority, not prose suggestions. | AG-I02,AG-I05 |
| AG-PO-028 | fake/mock evidence may satisfy only explicitly named unit/contract cells; required smoke/integration cells use the production composition boundaries frozen below, otherwise evidence is invalid. | AG-I05 |
| AG-PO-029 | J-01..J-05 execute with the exact validator IDs and boundary rules below, including two successful correction reentries and denial of the third. | AG-I02,AG-I05,AG-I07,AG-I12 |
| AG-PO-030 | the real dogfood integration uses assembled ABCP CLI/controller, real Git/filesystem/controller state, and pinned Ralphex/Codex on a disposable repository; model narration or an in-process fake cannot substitute. | AG-I03,AG-I05,AG-I09 |

## Successor activation and grandfather authority

`AssurancePolicyManifestV1` is not a new approver. The existing controller activation operation remains the sole installation authority. The caller supplies an exact repository commit and predecessor activation digest; the controller itself reads the canonical policy paths from that exact commit, recomputes their SHA-256 digests, canonicalizes the manifest, and verifies the predecessor activation against durable controller state. Required policy entries are `AUTONOMOUS_EXECUTION_GOVERNANCE.md`, `CONTEXT_AUTHORITY_AND_CAPSULE_POLICY.md`, `IMPLEMENTATION_DESIGN_GATE.md`, `DECISION_AND_ACCEPTANCE_POLICY.md`, and `ASSURANCE_MODEL_AND_PROOF_OBLIGATION_POLICY.md` plus the successor schema version. No caller-selected “approved digest” is authoritative.

Immediately before successor CAS, the controller derives `GrandfatheredV3LineageV1[]` from its durable predecessor-policy state at the expected revision. Each entry binds repository/controller identity, accepted A capsule/checkpoint digest, current capsule stage/file digest, current checkpoint tip/grant digest when present, exact candidate SHA, predecessor policy version, and issuance/revision identity. The activation record must contain exactly that controller-derived nonterminal eligible set, not a caller-selected superset/subset. The CAS simultaneously installs successor activation and freezes the grandfather list. A raced lineage issuance changes the expected revision and forces recomputation. Grandfathered entries may finish their already-issued predecessor lineage only; they cannot mint a new A, switch policy mid-lineage, or become successor-schema parents.

## Durable publication, recovery, and cleanup matrix

All new authority artifacts use one protocol: canonical bytes → content-addressed final artifact durably fsynced → one expected-revision controller CAS referencing exact digest. Durable unreferenced artifacts are non-authoritative orphans. No authority depends on deleting them.

| Family | Before artifact durability | Artifact durable / CAS absent | CAS submitted/response ambiguous | CAS proven applied | Cleanup/recovery rule |
|---|---|---|---|---|---|
| successor activation/manifest | no authority; temp may be removed | orphan only; exact retry create-or-verifies bytes | reload state at durable backend; no second CAS until exact activation digest/revision is resolved | successor active; cancellation cannot roll back | never delete referenced manifest; conflicting orphan quarantined/fails closed |
| A model/checkpoint + A→B grant | predecessor A only | orphan only | reconcile exact checkpoint/grant digests and revision | B derivation may use exact bound grant | temp cleanup pre-durability; durable orphan harmless/non-authoritative |
| B convergence + B→C grant | B remains current | orphan only | reconcile exact converged head/review/grant state | replacement C may be derived | no C mint from ambiguous/unreferenced artifact |
| stage evidence/checkpoint | prior stage evidence only | evidence artifact non-authoritative | reconcile stage checkpoint reference | cell/stage evidence authoritative for exact subject | partial stdout/stderr retained only as incomplete evidence, never PASS |
| failed-C record/C invalidation | C remains valid until atomic invalidation CAS | orphan finding packet only | reconcile failed-C digest + invalidated-C state | C invalid; zero mutation authority | no correction grant until exact failed-C state is proven |
| correction-B grant/reentry counter | failed C, prior counter | orphan proposed grant only | reconcile atomic `{counter+1, grant_digest}` CAS | exactly one reentry consumed and grant authoritative | never reserve separately; exact replay verifies existing grant without increment |
| assurance escape/A-required | current lineage until CAS | orphan escape artifact only | reconcile exact escape digest + A-required flag | lineage blocks new B/C authority | A-required cannot be cleared except by new accepted A lineage |

Cancellation/timeout before durable artifact permits bounded temp cleanup. After artifact durability but before CAS, cancellation leaves a harmless orphan and grants zero authority. Once CAS may have been submitted, retry authority is zero until reconciliation proves exact applied/not-applied state. Backend unavailability leaves `RECOVERY_REQUIRED`/validation-unavailable semantics and no second mutation. Applied state always wins over late cancellation.

## Frozen assurance resource profile

Structural admission maxima per assurance model: 32 dimensions, 128 stable scenarios, 64 invariants, 128 proof obligations, 256 evidence cells, 32 journeys, 256 validator identities; ID ≤128 UTF-8 bytes, claim/rationale/description ≤4096 bytes, each canonical authority record ≤1 MiB.

Lineage-cumulative ceilings from accepted A through post-merge: ≤1024 validator invocations, ≤512 rejected assurance mutation/admission attempts, ≤128 external Git/authority observation attempts, ≤2048 retained evidence refs, ≤512 MiB retained assurance evidence bytes, and exactly 2 correction-B reentries. These counters are controller-owned and never reset by candidate/result/subject, B/C, process, or restart changes.

Validator physical ceilings: max 4 concurrent validator processes; each invocation timeout ≤30 minutes; stdout ≤16 MiB and stderr ≤16 MiB; max 64 descendant PIDs; cgroup-v2 `memory.max` ≤2 GiB; `pids.max` ≤64; validator process `RLIMIT_NOFILE` ≤256. Required Linux smoke/resource cells are `VALIDATION_UNAVAILABLE` if these containment controls cannot be established. Controller CAS retry loop ≤8 attempts per requested mutation; an ambiguous CAS has zero mutation retry until state reconciliation. No validator may recursively spawn another ABCP validation graph.

Existing B `ExecutionBoundsV1` Ralphex invocation/review/mutation/time ceilings remain additional mandatory floors and also cannot reset across correction reentries.

## Validator registry

`${A_ACCEPTED_HEAD}`, `${CANDIDATE_HEAD}`, `${MERGE_RESULT}`, and runtime paths below are controller-supplied immutable bindings, never ambient shell input. Exact substituted argv and environment are retained as evidence.

| Validator ID | Stage | Exact command / authority | Purpose |
|---|---|---|---|
| AG-VAL-B-001 | B_IMPLEMENTATION | `go test -count=1 ./internal/context ./internal/governance ./internal/authority` | canonical contracts, activation, lineage, admission |
| AG-VAL-B-002 | B_IMPLEMENTATION | `go test -count=10 -race -run '^TestAssuranceConcurrentReplayAndDerivation$' ./internal/governance` | concurrent/replay authority |
| AG-VAL-B-003 | B_IMPLEMENTATION | `go test -count=1 -run '^TestAssurancePublicationCrashMatrix$' ./internal/governance` | subprocess kill/reopen at every publication boundary |
| AG-VAL-B-004 | B_IMPLEMENTATION | `go test -count=1 -run '^TestAssuranceCancellationAndObservationAmbiguity$' ./internal/governance` | cancellation/external ambiguity |
| AG-VAL-B-005 | B_IMPLEMENTATION | `go test -count=1 -run '^TestAssuranceEvidenceAndStageBinding$' ./internal/governance ./internal/acceptance` | exact cell/stage/subject semantics |
| AG-VAL-B-006 | B_IMPLEMENTATION | `go test -count=1 -run '^TestAssuranceScopeClassificationAndPlanImmutability$' ./internal/context ./internal/governance ./internal/authority` | documentation bypass, frozen A, B paths |
| AG-VAL-B-007 | B_IMPLEMENTATION | `go test -count=1 -run '^TestAssuranceResourceProfile$' ./internal/governance ./internal/run ./internal/acceptance` | structural/lineage resource ceilings |
| AG-VAL-C-001 | C_BRANCH_ACCEPTANCE | `go test -count=1 ./...` | full uncached repository tests |
| AG-VAL-C-002 | C_BRANCH_ACCEPTANCE | `go test -count=1 -race ./...` | full race suite |
| AG-VAL-C-003 | C_BRANCH_ACCEPTANCE | `go vet ./...` | static analysis |
| AG-VAL-C-004 | C_BRANCH_ACCEPTANCE | `git diff --check ${A_ACCEPTED_HEAD}...${CANDIDATE_HEAD}` | exact candidate hygiene |
| AG-VAL-C-005 | C_BRANCH_ACCEPTANCE | `go test -count=1 -run '^TestAssuranceFreshSessionAuthoritySmoke$' ./cmd/abcp` | J-01 real CLI/controller fresh-session smoke |
| AG-VAL-C-006 | C_BRANCH_ACCEPTANCE | `go test -count=1 -run '^TestAssuranceFailedCCorrectionReentryIntegration$' ./internal/governance ./cmd/abcp` | J-02 two reentries + third denial |
| AG-VAL-C-007 | C_BRANCH_ACCEPTANCE | `go test -count=1 -run '^TestAssuranceGapReturnsToAIntegration$' ./internal/governance ./cmd/abcp` | J-03 model-gap return to A |
| AG-VAL-C-008 | C_BRANCH_ACCEPTANCE | `go test -count=1 -run '^TestAssuranceDocumentationBypassSmoke$' ./cmd/abcp` | J-04 documentation bypass |
| AG-VAL-C-009 | C_BRANCH_ACCEPTANCE | `go test -count=10 -race -run '^TestAssurancePhysicalResourceAndCrashStress$' ./internal/governance ./internal/run ./internal/acceptance` | real cgroup/fd/process/resource stress |
| AG-VAL-I-001 | INTEGRATION_ACCEPTANCE | `scripts/acceptance/assurance-governance-dogfood.sh ${A_ACCEPTED_HEAD} ${CANDIDATE_HEAD}` | J-05 real disposable-repo ABCP/Ralphex/Codex dogfood |
| AG-VAL-PM-001 | POST_MERGE_ACCEPTANCE | `scripts/acceptance/assurance-governance-postmerge.sh ${MERGE_RESULT}` | merged-state compatibility + fresh installed-local CLI smoke |

Production evidence class is **not applicable** to this execution pack because it does not deploy ABCP to a production environment. No production cell may be pre-satisfied. External-build readiness remains a separate post-merge/activation claim and requires separately authorized deployment/runtime evidence.

## Evidence artifact schema

Every validator emits one or more strict-canonical `AssuranceEvidenceCellV1` records. Required fields are: `kind`, schema version, repository/controller identity, accepted-A lineage digest, cell ID, proof ID, evidence class, lifecycle stage, validator ID, exact subject kind/identity, exact substituted argv digest (or deterministic library-validator identity), start/end timestamps, exit/outcome classification, stdout/stderr artifact refs+SHA256 when a process is used, resource-observation ref+SHA256 when the cell has physical ceilings, and the evidence-record SHA256. Unknown/duplicate fields, mismatched cell metadata, wrong subject, missing required refs, oversized artifacts, or a validator identity not frozen in this A model fail closed.

Candidate subjects bind repository identity + exact commit + exact tree + accepted-A lineage digest. Integration subjects additionally bind disposable integration repository identity + source candidate + exact integration commit/tree. Post-merge subjects bind repository identity + exact merge commit/tree + source candidate. Artifact refs are controller-owned, regular-file/content-addressed evidence under the existing evidence-retention rules. A command's stdout/stderr alone is never authority; the canonical evidence record binds the command result to its frozen cell.

Crash/restart cells also retain a strict `AssuranceCrashBoundaryEvidenceV1` child artifact naming family, boundary ID, kill point, pre-kill controller revision, fresh-process reconciled revision, expected/observed authoritative digest, orphan artifact digests, and recovery outcome (`APPLIED`, `NOT_APPLIED`, or `RECOVERY_BLOCKED`). Resource cells retain `AssuranceResourceEvidenceV1` with lineage counters plus observed cgroup memory/PID ceilings, process count, FD count, output bytes, retained evidence refs/bytes, elapsed time, and concurrency peak.

## Production composition / fake boundary matrix

| Boundary | Unit/B fake allowed? | Required real boundary |
|---|---|---|
| Git repository/object/ref | parser helpers may fake bytes only | C/J-01 and J-05 use real temporary Git repositories and real Git object/ref commands |
| controller durable authority state/CAS | pure canonical functions may use fixtures | C integrations use production `ControllerV1` persistence/backend implementation in isolated namespace |
| filesystem durability/path identity | pure parser fixtures only | crash/resource cells use real filesystem, fsync/rename/open identity and fresh process |
| process containment/resource limits | unit mock allowed only for local API behavior | AG-VAL-C-009 uses real Linux cgroup v2 + RLIMIT controls; unavailable is not PASS |
| Ralphex/Codex execution | B may use deterministic helper process for controller unit tests | J-05 uses the pinned real Ralphex binary and Codex executor; unavailable is `VALIDATION_UNAVAILABLE` |
| GitHub/remote merge provider | not part of this implementation | not required by this execution pack; existing EP-005 contracts remain unchanged |

## Strict evidence-cell registry

One row is one cell. A cell has exactly one proof ID, one evidence class, one lifecycle stage, one subject rule, and one validator ID.

| Cell ID | Proof | Class | Stage | Subject | Validator |
|---|---|---|---|---|---|
| AG-E-001 | AG-PO-001 | contract | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-001 |
| AG-E-002 | AG-PO-002 | contract | B_IMPLEMENTATION | exact B candidate | AG-VAL-B-001 |
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
| AG-E-030 | AG-PO-030 | e2e | INTEGRATION_ACCEPTANCE | exact disposable integration repository + candidate commit/tree | AG-VAL-I-001 |
| AG-E-031 | AG-PO-020 | crash_restart | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-009 |
| AG-E-032 | AG-PO-025 | static | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-003 |
| AG-E-033 | AG-PO-025 | race_concurrency | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-002 |
| AG-E-034 | AG-PO-006 | static | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | AG-VAL-C-004 |
| AG-E-035 | AG-PO-005 | migration | POST_MERGE_ACCEPTANCE | exact merge-result commit/tree | AG-VAL-PM-001 |
| AG-E-036 | AG-PO-023 | replay_idempotency | POST_MERGE_ACCEPTANCE | exact merge-result commit/tree | AG-VAL-PM-001 |

Every AG-PO-001..030 has at least one cell. Required smoke/integration cells cannot be substituted by unit/contract results. Missing/unavailable execution of a sufficient cell is `ASSURANCE_EVIDENCE_INCOMPLETE`; a failing validator is an implementation finding; a missing/wrong cell/validator/stage/subject/boundary is `ASSURANCE_MODEL_GAP`.

## Critical journeys

**J-01 / AG-VAL-C-005 — Fresh-session authority smoke.** Real temporary Git repository + production ControllerV1 + assembled `abcp` CLI. Install/replay exact successor activation, validate A assurance model → A-to-B → B admission → B-to-C subject. Re-run with one changed policy digest, model digest, repo identity, symlink/path, candidate SHA, and unlisted predecessor lineage; every case fails before mutation.

**J-02 / AG-VAL-C-006 — Failed-C bounded correction integration.** Starting from one accepted A, create C1 with a mapped implementation finding; publish failed-C1 then correction-B1 and prove counter=1. Converge to C2, repeat with mapped finding; publish correction-B2 and prove counter=2. Restart controller/process, converge to C3, present a third mapped implementation blocker, and prove no correction grant, counter remains 2, return-to-A required. Concurrent and exact replay attempts at each reentry prove no double consumption. Counter consumption is the atomic CAS that publishes the correction-B grant, not finding count.

**J-03 / AG-VAL-C-007 — Assurance-gap return-to-A.** Present missing scenario, unjustified N/A, wrong cell stage/subject, missing real boundary, and inadequate proof variants during B/C. Each yields `ASSURANCE_MODEL_GAP`, zero correction grant/reentry consumption, and A-required lineage state.

**J-04 / AG-VAL-C-008 — Documentation bypass smoke.** Controller-bound docs-only classification over a non-authoritative doc passes its narrow classifier; then candidate diff touches governance policy/runtime-affecting bytes and exact-diff revalidation invalidates the exemption before B/C acceptance.

**J-05 / AG-VAL-I-001 — Real dogfood E2E.** Build exact candidate `abcp`; use real Git/filesystem/ControllerV1/cgroup and pinned real Ralphex+Codex against a disposable toy repository for a non-trivial code change. Execute A→B→C with independent C smoke/integration evidence and exact-head review inputs. Any missing real boundary is `VALIDATION_UNAVAILABLE`; model narration/in-process fake cannot satisfy this journey.

## Functional-plan mutation and B convergence

The six-task functional plan references frozen AG-PO IDs. B may mark task checkboxes/status only after corresponding validators pass and may perform the final semantic-byte-preserving move to `docs/plans/completed/assurance-proof-obligation-governance-enforcement.md`. A/B/C validator must reject any B diff that semantically changes task scope, proof bindings, non-goals, or this assurance model.

C exact-head final review is read-only and must classify every blocker as either violation of a frozen obligation or `ASSURANCE_MODEL_GAP`. Publication/merge requires unchanged exact candidate, all B and C cells PASS, C review Critical=0/Major=0, clean repository state, and no exhausted lineage ceilings. Integration/post-merge cells then run at their exact frozen stages. External-build readiness is still not claimed by repository merge alone.

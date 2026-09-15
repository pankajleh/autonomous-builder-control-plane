# Assurance Governance Enforcement — A Assurance Model

Status: **A_DESIGN candidate — implementation forbidden until fresh exact-head design/assurance review is 0C/0M**

Base authority: merge commit `04e28eb7807acecc16161d6e33fb20bf88476ce7` (PR #19). Normative policy: `docs/architecture/ASSURANCE_MODEL_AND_PROOF_OBLIGATION_POLICY.md`. Functional plan: `docs/plans/assurance-proof-obligation-governance-enforcement.md`.

## Scope and implementation authority

This is code-bearing ABCP work. `documentation_only` is forbidden. The implementation may change only `internal/contextcapsule/**`, `internal/governance/**`, `internal/acceptance/**` when stage-evidence execution requires it, `cmd/abcp/**`, directly associated tests/fixtures, and this plan/completed-plan transition. Any need to change unrelated lifecycle/provider/service packages is `SCOPE_EXPANSION_REQUIRED` and returns to A.

Exactly three capsules remain authoritative: A owns this assurance model, B implements against it, and C independently executes branch-stage evidence and performs read-only exact-head review. No reviewer or test may mint a fourth authority surface.

Finite cumulative final-review correction-reentry ceiling for this A lineage: **2** controller-derived `B_CORRECTION` lineages. The ceiling is lineage-cumulative and may not reset after another B/C attempt. A third blocking C implementation finding returns to A.

## Frozen invariants

| ID | Invariant |
|---|---|
| AG-I01 | Only a successor activation chained to the installed governance activation and bound to an approved policy manifest can authorize the new assurance schema. |
| AG-I02 | Every applicable failure scenario has immutable traceability to invariant → proof obligation → staged evidence; no blank or weaker substitute is accepted. |
| AG-I03 | Assurance strengthens A/B/C without creating mutable reviewer authority or a fourth capsule. |
| AG-I04 | Work defaults code-bearing; any documentation-only exemption is controller-owned, path/semantic bounded, and exact-diff revalidated. |
| AG-I05 | Every evidence requirement is bound to exactly one lifecycle stage and exact candidate/result/deployment subject. |
| AG-I06 | B/C cannot weaken the frozen assurance model; model insufficiency returns to A with `ASSURANCE_MODEL_GAP`. |
| AG-I07 | C implementation findings invalidate C and can enter only bounded controller-derived correction B authority from frozen A evidence. |
| AG-I08 | Canonical assurance records are deterministic, strict, bounded, versioned, and do not reinterpret historical V1/V2/V3 bytes. |
| AG-I09 | Fresh sessions fail closed when assurance activation, model, proof, matrix, lineage, or exact subject bindings are absent or inconsistent. |
| AG-I10 | Durable assurance/controller state survives process termination without fork/reissue/partial-authority acceptance. |
| AG-I11 | Repository/controller identity and paths cannot be replaced or crossed to obtain another lineage's assurance authority. |
| AG-I12 | Physical resources, evidence cardinality/bytes, retries, correction reentries, and validator execution are finitely bounded. |

## Failure/threat model

| Dimension | Classification | Named scenario(s) | Invariants | Proof obligations |
|---|---|---|---|---|
| crash consistency | applicable | termination while publishing successor activation, model/grant/checkpoint, failed-C record, or correction-B derivation | AG-I01,I07,I10 | AG-PO-001, AG-PO-002, AG-PO-014, AG-PO-015, AG-PO-016, AG-PO-017 |
| concurrency/races | applicable | competing activation installs, A/B/C derivations, evidence writes, or correction reentries | AG-I01,I07,I10 | AG-PO-002, AG-PO-016, AG-PO-017 |
| retry/replay/idempotency | applicable | duplicate activation/model/evidence/checkpoint/correction requests | AG-I01,I07,I08 | AG-PO-002, AG-PO-004, AG-PO-016 |
| authority replacement/staleness | applicable | stale policy digest, replaced model/matrix, moved candidate, replayed failed-C finding | AG-I01,I05,I07,I09 | AG-PO-001, AG-PO-007, AG-PO-009, AG-PO-010, AG-PO-011, AG-PO-014, AG-PO-015, AG-PO-016, AG-PO-018 |
| partial ordered side effects | applicable | durable child record without parent/tip or tip without complete child authority | AG-I01,I07,I10 | AG-PO-001, AG-PO-014, AG-PO-015, AG-PO-016, AG-PO-017 |
| cancellation/timeout/ambiguity | applicable | validator/review/controller stops after possible durable mutation | AG-I07,I10 | AG-PO-014, AG-PO-015, AG-PO-016, AG-PO-017 |
| physical resource exhaustion | applicable | excessive model/proof/evidence cells, evidence bytes, validator fan-out, correction reentries | AG-I08,I12 | AG-PO-004, AG-PO-015, AG-PO-016, AG-PO-020 |
| restart/recovery/fail-closed | applicable | fresh process resumes after any durable assurance boundary | AG-I09,I10 | AG-PO-002, AG-PO-008, AG-PO-017, AG-PO-018 |
| malformed/forged/oversized input | applicable | duplicate/unknown fields, ambiguous IDs, invalid stages, forged digests, oversized matrices | AG-I02,I08,I12 | AG-PO-003, AG-PO-004, AG-PO-006, AG-PO-008, AG-PO-020 |
| filesystem/path/object identity | applicable | symlink/path/repository substitution of policy/model/evidence/controller state | AG-I09,I11 | AG-PO-018, AG-PO-019 |
| external dependency behavior | applicable | Git object/ref unavailable or changes during policy/candidate verification | AG-I01,I05,I09 | AG-PO-001, AG-PO-010, AG-PO-011, AG-PO-012, AG-PO-018 |
| compatibility/version skew | applicable | predecessor V3 lineage vs successor assurance schema/activation | AG-I01,I08,I09 | AG-PO-001, AG-PO-002, AG-PO-005, AG-PO-018 |
| tenant/security/privacy boundary | applicable | another repository/controller identity attempts to reuse assurance/grant/evidence | AG-I09,I11 | AG-PO-008, AG-PO-018, AG-PO-019 |
| integration/E2E composition | applicable | real CLI/controller fresh-session A→B→C journey disagrees with isolated validators | AG-I02,I05,I09 | AG-PO-006, AG-PO-009, AG-PO-010, AG-PO-011, AG-PO-012, AG-PO-013, AG-PO-018, AG-PO-021, AG-PO-022 |

Every scenario above maps through the named invariants and proof obligations into the evidence matrix below. No dimension is `not_applicable` for this controller-governance implementation.

## Frozen assurance resource profile

These are admission maxima, not targets: at most 32 failure dimensions, 128 scenarios, 64 invariants, 128 proof obligations, 256 evidence cells, 32 critical journeys, and 256 distinct deterministic validator identities per assurance model. Each ID is at most 128 UTF-8 bytes, each rationale/claim/description at most 4096 UTF-8 bytes, and each strict-canonical assurance record is at most 1 MiB encoded. Each lifecycle stage may execute at most 256 matrix validator invocations for one candidate/result subject. Cumulative final-review correction reentries remain exactly 2 for this lineage. Inputs exceeding any ceiling fail before durable child authority or validator execution.

## Frozen proof obligations

| ID | Falsifiable claim | Invariants |
|---|---|---|
| AG-PO-001 | Only the unique valid successor activation chained to the exact predecessor activation and approved policy-manifest digest authorizes assurance schema use. | I01,I08,I09 |
| AG-PO-002 | A competing successor activation, unactivated schema/policy, or over-broad grandfather set is rejected without changing authority. | I01,I08,I10 |
| AG-PO-003 | Every applicable dimension has ≥1 scenario and every scenario reaches ≥1 invariant, proof obligation, and evidence cell; broken traceability is rejected before DESIGN_ACCEPTED. | I02,I06 |
| AG-PO-004 | Assurance/model/proof/evidence records strict-parse canonically; reordering canonical sets hashes identically while duplicates/unknown/ambiguous/oversized values fail. | I08,I12 |
| AG-PO-005 | Historical V1/V2/V3 records preserve their prior parse/verification meaning and cannot authorize the successor schema. | I01,I08 |
| AG-PO-006 | `documentation_only` cannot be self-declared and is invalidated by any exact candidate diff or semantic effect outside the controller-bound exemption. | I04,I09 |
| AG-PO-007 | A-to-B authority binds activated policy/model/proof/matrix digests, mandatory floors, exact allowed paths, and cumulative correction-reentry ceiling. | I02,I03,I06,I07 |
| AG-PO-008 | Missing/malformed assurance authority prevents B/Ralphex admission before mutation. | I06,I09 |
| AG-PO-009 | B cannot mutate/reclassify/N-A/weaken the assurance model or evidence requirements. | I02,I06 |
| AG-PO-010 | B convergence requires every B-stage evidence cell for the exact candidate and rejects wrong-stage, wrong-subject, weaker-class, or unavailable evidence. | I02,I05,I09 |
| AG-PO-011 | C independently executes only C_BRANCH_ACCEPTANCE cells for one unchanged exact candidate and validates required predecessor B evidence. | I03,I05,I09 |
| AG-PO-012 | Integration/post-merge/production cells cannot satisfy C early and later stages reject evidence bound to the wrong result/deployment identity. | I05,I09 |
| AG-PO-013 | Model insufficiency has precedence as `ASSURANCE_MODEL_GAP` and cannot mint B mutation authority. | I03,I06 |
| AG-PO-014 | A sufficient-model implementation violation in C produces a durable failed-C record but no mutation authority by itself. | I03,I07,I10 |
| AG-PO-015 | Controller-derived correction B requires exact failed-C evidence, frozen A mapping and correction paths, and consumes one non-resetting lineage reentry; unmapped/exhausted findings return to A. | I07,I10,I12 |
| AG-PO-016 | Duplicate/concurrent failed-C or correction derivations cannot fork, double-consume, or reset the correction-reentry ceiling. | I07,I10,I12 |
| AG-PO-017 | Process termination at each new durable publication boundary recovers to the same committed generation, provable rollback, or fail-closed state without reissue/fork. | I01,I07,I10 |
| AG-PO-018 | Fresh process/session validates exact activated policy/model/proof/matrix/candidate lineage from durable evidence and rejects missing/replaced/stale components. | I09,I11 |
| AG-PO-019 | Repository/path/symlink/object substitution cannot redirect assurance authority or evidence to another repository/controller identity. | I09,I11 |
| AG-PO-020 | Controller-enforced cardinality, byte, retry, validator, and correction-reentry bounds remain physically effective under repeated rejected/concurrent operations. | I08,I12 |
| AG-PO-021 | Every Critical/Major assurance escape is durably recorded and forces a new A completeness review before new implementation authority. | I06,I07,I09 |
| AG-PO-022 | A real assembled ABCP CLI/controller dogfood journey proves A→B→C traceability, branch smoke, and integration composition independently of coding-agent narration. | I02,I03,I05,I09 |

## Evidence matrix

| Proof IDs | Class | Stage | Exact subject | Required validator/evidence |
|---|---|---|---|---|
| AG-PO-001, AG-PO-002, AG-PO-003, AG-PO-004, AG-PO-005 | unit, contract, security_negative | B_IMPLEMENTATION | exact B candidate commit/tree | focused strict-canonical/activation compatibility tests |
| AG-PO-001, AG-PO-002, AG-PO-016, AG-PO-017 | race_concurrency, crash_restart, replay_idempotency | B_IMPLEMENTATION | exact B candidate | race tests plus subprocess kill/reopen boundary matrix |
| AG-PO-003, AG-PO-006, AG-PO-007, AG-PO-008, AG-PO-009, AG-PO-010, AG-PO-013 | unit, contract, security_negative | B_IMPLEMENTATION | exact B candidate | governance/context-capsule tests proving A/B completeness and fail-closed admission |
| AG-PO-010, AG-PO-011, AG-PO-012, AG-PO-018 | smoke | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | fresh-process `abcp` CLI smoke validating capsule/assurance and branch acceptance |
| AG-PO-011, AG-PO-012, AG-PO-013, AG-PO-014, AG-PO-015, AG-PO-016, AG-PO-021 | integration | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | assembled governance controller integration test including failed-C → bounded correction-B path |
| AG-PO-017, AG-PO-018, AG-PO-019, AG-PO-020 | resource, race_concurrency, crash_restart, security_negative | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | independent controller-run stress/fault suite with physical resource observations |
| AG-PO-004, AG-PO-005, AG-PO-008, AG-PO-009, AG-PO-010, AG-PO-011, AG-PO-012, AG-PO-013, AG-PO-014, AG-PO-015, AG-PO-016, AG-PO-017, AG-PO-018, AG-PO-019, AG-PO-020 | static | C_BRANCH_ACCEPTANCE | unchanged exact C candidate | `go vet ./...`, full `go test -count=1 ./...`, full `go test -count=1 -race ./...`, `git diff --check` |
| AG-PO-022 | integration, smoke, e2e | INTEGRATION_ACCEPTANCE | exact disposable integration result | real toy-repository A→B→C dogfood journey using assembled CLI/controller |
| AG-PO-001, AG-PO-005, AG-PO-018 | migration, replay_idempotency | POST_MERGE_ACCEPTANCE | exact merge result commit/tree | merged-state compatibility and successor-activation replay tests |
| AG-PO-022 | runtime | POST_MERGE_ACCEPTANCE | exact merge result | installed local ABCP CLI fresh-session smoke; no external-build-readiness claim yet |
| AG-PO-022 | production | PRODUCTION_ACCEPTANCE | future immutable deployment identity | explicitly deferred until separately authorized deployment; cannot be pre-satisfied |

A failed required validator is an implementation finding. Missing/unavailable evidence for an otherwise sufficient row is `ASSURANCE_EVIDENCE_INCOMPLETE`. A wrong/missing row, stage, subject, validator, journey, or scenario mapping is `ASSURANCE_MODEL_GAP` and returns to A.

## Critical smoke/integration journeys

**J-01 Fresh-session authority smoke.** From a clean fresh process, validate successor activation → A assurance model → A-to-B grant → B capsule/admission for an exact repository/candidate. Replace any one digest, repository identity, path, or candidate and require fail-closed rejection before mutation.

**J-02 Failed-C bounded correction integration.** Drive an exact candidate through C branch acceptance, inject one valid frozen-obligation implementation finding, persist failed-C evidence, derive one correction B with exact finding/path authority, prove the reviewer itself cannot mutate, then prove duplicate/concurrent derivation does not double-consume or reset the ceiling.

**J-03 Assurance-gap return-to-A integration.** Present a materially missing/misclassified scenario or wrong evidence-stage model during B/C and prove `ASSURANCE_MODEL_GAP`, zero correction mutation authority, and required new A lineage.

**J-04 Documentation bypass negative smoke.** Attempt a documentation-only lineage whose exact diff touches governance/runtime-affecting bytes and prove exemption invalidation before B/C acceptance.

**J-05 Dogfood E2E.** Use the assembled ABCP CLI/controller on a disposable toy repository to run a non-trivial code change through A→B→C with independently rerun smoke/integration evidence and exact-head review; no chat/model assertion may substitute for evidence.

## Review and convergence rules

A design/assurance reviewer reviews this entire model for completeness before any implementation. B implementation/review may only address frozen obligations and allowed paths. A missing threat/proof/evidence requirement is not a B finding: it is `ASSURANCE_MODEL_GAP` and returns here.

C exact-head final review is read-only and must classify every blocker as either a violation of a frozen obligation or an assurance-model gap. At most two controller-derived correction-B lineages may follow implementation findings under this A authority. Publication/merge requires unchanged exact candidate, all C-stage cells PASS, 0 Critical / 0 Major final review, and clean repository state.

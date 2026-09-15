# Implementation Design Gate

## 1. Purpose

Substantial implementation must not discover foreseeable authority, trust-boundary, resource-bound, state-machine, or failure-model decisions only after code review. Before Ralphex/Codex implementation begins, the controller must review and accept both the task design and its assurance model against this gate and `ASSURANCE_MODEL_AND_PROOF_OBLIGATION_POLICY.md`.

## 2. Required design checks

A task design must explicitly cover, where applicable:

1. **Authority binding** — every trusted claim names the immutable authority that permits it.
2. **Provenance graph** — authority → input → computation → evidence → state transition is traceable.
3. **Trust boundaries** — caller-controlled paths, SHAs, refs, branches, policy, reviews, blockers, external results, subprocesses, and remote responses are classified.
4. **Resource bounds** — candidate counts, commands, evidence refs/bytes, remote pages, retries, timeouts, and fan-out are bounded before implementation.
5. **Filesystem/evidence safety** — controller-root containment, regular-file proof, symlink/special-file rejection, bounded reads, and identity recheck are specified where local evidence is consumed.
6. **Git integrity** — exact object IDs, expected heads, replacement-ref resistance, branch identity, and bounded/cancellable Git execution are specified where Git is authoritative.
7. **State failure matrix** — every failure point identifies the last durable state, next allowed state, and required evidence. Returning an error must not silently strand durable state unless the durable ledger/evidence substrate itself is unavailable or ambiguous.
8. **Determinism** — canonical ordering/serialization, timestamps, commit identity, evidence naming, and digests are specified where results become authority.
9. **Cleanup matrix** — success, validation failure, cancellation, timeout, publication failure, and cleanup failure behavior is explicit.
10. **Adversarial tests** — task-specific forged identity, moved head, mutated evidence, oversized input/fan-out, cancellation, provider ambiguity, and transition failure cases are enumerated before coding.
11. **External side-effect identity** — remote writes bind the non-secret authenticated acting principal/app-installation identity into authority, request provenance, and evidence; secrets themselves never enter plans or ledgers.
12. **Ambiguous-write cancellation** — cancellation/deadline after a remote write may have been submitted is classified as ambiguous, never as a clean retryable failure; retry authority defaults to zero unless a later reconciliation proves the prior outcome.
13. **Merge/content lineage** — when a VCS host may synthesize a new commit (merge/squash/rebase), the design distinguishes accepted head identity from post-merge commit identity and specifies authority-bound merge method plus verifiable content/lineage evidence.
14. **Assurance completeness** — every invariant maps to stable numbered proof obligations; every proof obligation maps to required deterministic evidence classes; every minimum failure-model dimension is applicable or explicitly not applicable with rationale.
15. **Smoke/integration journeys** — critical assembled-system journeys and production composition boundaries are named before coding. Required smoke/integration evidence cannot be replaced by unit tests or unbound mocks.
16. **Failure-boundary proof** — where durability or ordered side effects exist, the design enumerates meaningful pre/post-persistence boundaries and specifies fresh-process/recovery evidence rather than relying only on injected in-process errors.

## 3. Review sequence

```text
roadmap authority
→ execution pack / bounded plan
→ controller design/threat/assurance review
→ freeze invariants + failure model + proof obligations + evidence matrix
→ independent design/assurance review when available or risk-required
→ DESIGN_ACCEPTED
→ Ralphex/Codex implementation
→ deterministic ABCP acceptance
→ exact-head Critical/Major review
```

Independent-provider failure before a complete verdict follows the provider-fallback policy in `DECISION_AND_ACCEPTANCE_POLICY.md`. At A, a clean fallback requires the exact design head to have passed the canonical design/assurance validators; it does not require post-implementation branch acceptance. A substantive provider finding is not provider failure and must be corrected.

## 4. Parallel execution rule

Parallelize implementation only after shared authority/contract ownership is frozen and file/package ownership is disjoint. Parallel runs may read a shared authoritative contract, but two parallel runs must not both mutate it unless ordering is explicit.

## 5. Evidence

The execution pack/plan or its bound context capsule must record the design-gate and assurance-gate outcomes. The A evidence must bind the assurance policy/model, proof-obligation registry, required-evidence matrix, and smoke/integration journey matrix. The final PR/merge audit must preserve the exact implementation SHA, proof-obligation traceability, acceptance evidence, assurance escapes if any, and Critical/Major review verdict.

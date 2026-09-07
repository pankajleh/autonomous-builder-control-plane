# Implementation Design Gate

## 1. Purpose

Substantial implementation must not discover foreseeable authority, trust-boundary, resource-bound, or state-machine decisions only after code review. Before Ralphex/Codex implementation begins, the controller must review and accept the task design against this gate.

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

## 3. Review sequence

```text
roadmap authority
→ execution pack / bounded plan
→ controller design/threat review
→ independent cross-model design review when available or risk-required
→ DESIGN_ACCEPTED
→ Ralphex/Codex implementation
→ deterministic ABCP acceptance
→ exact-head Critical/Major review
```

Independent-provider failure before a complete verdict follows the provider-fallback policy in `DECISION_AND_ACCEPTANCE_POLICY.md`. A substantive provider finding is not provider failure and must be corrected.

## 4. Parallel execution rule

Parallelize implementation only after shared authority/contract ownership is frozen and file/package ownership is disjoint. Parallel runs may read a shared authoritative contract, but two parallel runs must not both mutate it unless ordering is explicit.

## 5. Evidence

The execution plan or its bound context capsule must record the design-gate outcome. The final PR/merge audit must preserve the exact implementation SHA, acceptance evidence, and Critical/Major review verdict.

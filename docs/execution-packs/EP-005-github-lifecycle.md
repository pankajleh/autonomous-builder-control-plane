# EP-005 — GitHub Lifecycle

## 1. Roadmap authority

Phase 4 of `docs/roadmap/IMPLEMENTATION_ROADMAP.md` authorizes exactly these deliverables:

- PR creation/update with exact head SHA;
- CI evidence ingestion;
- merge approval policy;
- expected-head merge protection;
- post-merge acceptance.

## 2. Objective

Advance a controller-approved `READY_FOR_MERGE` candidate through the GitHub pull-request and merge lifecycle without weakening exact-head, provenance, review, or acceptance authority. GitHub is an external execution surface; remote observations and writes are never self-authenticating simply because an API call succeeded.

## 3. Non-goals

Do not implement the Phase 5 service/API/dashboard, deployment/production acceptance, worker leasing, multi-host execution, secret-broker integration, retention automation, or unrelated repository-management features.

## 4. Decomposition

1. **Foundation — serial:** freeze GitHub lifecycle identities, provider boundary, remote snapshot/evidence contracts, merge-authority inputs, resource limits, and fail-closed classifications.
2. **PR lifecycle:** create or update one PR only for the exact governed repository/base/head identity; remote divergence or ambiguity blocks.
3. **CI ingestion:** ingest bounded CI/check evidence tied to the exact candidate head SHA; pending, missing, stale, truncated, or ambiguous evidence cannot satisfy policy.
4. **Merge authorization:** bind merge approval policy, the allowed merge method, exact accepted head SHA, and the expected pre-merge base-tip SHA; a substantive reviewer/policy blocker or moved head/base prevents merge.
5. **Post-merge acceptance — serial:** verify the merge result and target branch identity, including strategy-aware content/lineage proof when GitHub synthesizes a new commit, capture immutable remote/Git evidence, and emit the governed `MERGED` transition only from the exact `READY_FOR_MERGE` authority.

## 5. Foundation ownership

The first bounded plan owns a new `internal/githublifecycle/` package plus its tests and foundation contract documentation. It may read existing authority/domain/ledger/evidence contracts but must not mutate Phase 3 scheduler/integration contracts.

The foundation is intentionally network-free: it defines immutable domain inputs, provider interfaces, canonical snapshots and validation policy before any GitHub write implementation is authorized.

## 6. Trust and design invariants

- Repository identity, base branch, head branch, exact head SHA, PR number/node identity when known, and expected target SHA are explicit values, never inferred from display text.
- Remote provider responses are bounded and validated before they become controller evidence.
- PR/CI data is always tied to an exact head SHA; stale CI from another SHA cannot satisfy acceptance.
- Merge calls must eventually require the exact accepted head SHA, expected pre-merge base-tip SHA, an authority-bound approval policy, and an authority-bound allowed merge method.
- The post-merge commit SHA is not assumed to equal the accepted PR head SHA. Frozen contracts must support strategy-aware proof using the merge method, base-before/base-after/result identities, accepted/result tree identities, and parent/lineage data where applicable.
- The authenticated acting principal/app-installation identity is a non-secret authority value and is included in every remote-write request/evidence record; credentials/tokens are never serialized into authority or evidence.
- Provider/network failure is distinct from substantive CI/review/policy failure. A cancellation or deadline after a write may have been submitted is an ambiguous write outcome.
- Remote side effects are counted and evidence-backed; retries are never implicit when write success is ambiguous, and retry authority defaults to zero until explicit reconciliation proves the prior write outcome.
- `READY_FOR_MERGE → MERGED` remains serial and controller-owned.
- Every task must pass `IMPLEMENTATION_DESIGN_GATE.md` before implementation.

## 7. Completion

EP-005 is complete only when all five Phase 4 roadmap bullets are implemented, independently accepted, reviewed at exact heads, and merged. Foundation completion alone is not Phase 4 completion.

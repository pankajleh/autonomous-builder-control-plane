# G0-B Development Admission — Repo B Execution Pack

Status: **DESIGN_ACCEPTED — IMPLEMENTATION AUTHORITY PRECURSOR**

Authority: Repo C `G0-DEVELOPMENT-TOOLING-CUTOVER-CAPSULE-004`
Capsule SHA-256: `ce15cc729f43afc8927acc5389a1a675c69d0cbe4f30501955de8655a5116066`
Correction-02 frozen authority: `8d90a5ff506bd965bfc9e533d2be0b53c2adf73d`
Repo C authorization head: `bd0c1793889d1e84c911464cdf2f52f07a34e0ac`
Repo B implementation base: `12e292afb93fd201485cedc734397c006140c202`
Rollback tag: `checkpoint/e2e-engine-v1-2026-09-21`

## Objective

Add only the strict development-only `POST /v1/development-runs` admission seam frozen by CAPSULE-004 while keeping product `POST /v1/runs` request fields and semantics unchanged.

G0-B reuses existing durable admission receipt/binding, launch locking, run catalog, controller-owned profile materialization, Ralphex execution and acceptance machinery. It MUST NOT create a second controller, scheduler, retry/recovery path, provider policy, or merge path.

## Allowed paths

- `internal/serviceapi/**`
- `internal/runadmission/**`
- `cmd/abcp/**`
- `docs/architecture/**` only if needed for the frozen development-admission contract
- `docs/execution-packs/G0-DEVELOPMENT-TOOLING-CUTOVER/**`
## Frozen public contract

`POST /v1/development-runs` accepts strict schema-v1 JSON containing exactly:
- `schema_version = 1`
- `request_id`
- `profile_id`
- `development_capsule_id`
- `development_slice_id`
- `development_capsule_sha256`
- `repository_base_sha`
- bounded UTF-8 `task_markdown`
- required `delegated_actor { subject_id, subject_type }`

`task_markdown` MUST be the exact immutable development-capsule bytes and its SHA-256 MUST equal `development_capsule_sha256`.

Success returns the existing opaque run response shape with `run_url=/v1/runs/{run_id}`. Existing run projection/evidence/action reads remain unchanged.

No field is added to strict `GET /v1/capabilities` in G0.

Caller input MUST NOT select filesystem paths, manifest, Ralphex argv/config, model/provider, worktree, evidence paths, workflow-authority store, acceptance policy, merge authority, or recovery policy.

Authentication/authorization reuses the existing machine-service principal plus `may_assert_delegated_actor` mechanism. No new role, authority string, or permission model is introduced by G0.

The development endpoint accepts only the frozen controller-owned profile ID `repo-c-development-v1`. Any other profile ID, including the retained product profile `local-p02`, fails closed as unknown/unconfigured for development admission.

## Bootstrap executor boundary

Implementation MUST use the repository's pre-G0 Ralphex/Codex workflow. The new development endpoint MUST NOT be used to implement itself.

The approved Ralphex implementation runtime remains:
- binary SHA-256 `9ad47b083aaaf34eed4b35ca82f3c92d1a3b7641921be0ea606cbd1cd1c3adac`
- detached source SHA `319e30618352a1b43e4be1b8a894c6c05e6d5fa8`
## Design-gate analysis

1. **Authority binding:** every new development admission claim derives only from CAPSULE-004; product ProductAuthorization semantics are not reused.
2. **Provenance:** authenticated service principal + delegated actor + strict request -> development-authority semantic namespace/digest -> durable receipt/binding -> exact `repo-c-development-v1` profile -> controller-owned materialization -> existing run identity/evidence. Product and development request identities MUST NOT alias merely because principal/request ID strings match.
3. **Trust boundary:** all public JSON is caller-controlled; profile/repository/runtime/manifest/worktree/provider/acceptance configuration remains controller-owned.
4. **Resource bounds:** reuse existing HTTP/body/JSON, admission materialization, process and acceptance bounds; no new fan-out or retry loop.
5. **Filesystem safety:** public request contains no paths; existing profile/materialization safety remains authoritative.
6. **Git integrity:** repository base must equal the controller-bound repository HEAD; admission MUST NOT fetch/reset/merge to satisfy caller input.
7. **Failure matrix:** malformed/unknown/duplicate/case-alias/non-UTF8/oversized input, unknown profile, base mismatch, receipt conflict/corruption, materialization failure and launch ambiguity all fail closed with existing typed error semantics.
8. **Determinism:** within the development-authority namespace, same authenticated principal + request ID + semantic payload replays one durable run; semantic conflict fails closed. Product-admission and development-admission durable identities remain distinct.
9. **Cleanup:** no new cleanup domain; existing admission/run cleanup semantics remain authoritative.
10. **Adversarial tests:** product/dev shape separation, unknown/duplicate/case-alias fields, capsule digest mismatch, profile/base mismatch, injection attempts, replay conflict, binding corruption and ambiguous launch are required.
11. **External identity:** authenticated principal remains trusted actor; delegated actor is attribution only and participates in semantic identity.
12. **Ambiguous write:** uncertain launch outcome cannot fabricate success or grant a new request identity; exact replay reconciles only existing durable state.
13. **Merge/content lineage:** implementation success does not grant publication or merge; merge remains separately human-authorized.

## Development materialization identity

The generated plan contains exactly one executable heading: `### Task 1: Implement the frozen development slice`. The complete submitted capsule is inert inside a collision-safe Markdown fence.

Controller-built context uses:
- roadmap phase `repo-c-development`;
- execution pack = development capsule ID;
- plan = development slice ID;
- task = request ID;
- owned scope = `Development slice <development_slice_id>`.

It MUST NOT emit ProductAuthorization/product-task labels for development authority.

Design outcome: `DESIGN_ACCEPTED` after controller-fallback review returned 0 Critical / 0 Major. The independent provider attempts produced no complete verdict and carry no authority.

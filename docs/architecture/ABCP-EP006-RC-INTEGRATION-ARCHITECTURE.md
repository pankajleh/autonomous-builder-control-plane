# ABCP EP-006 ↔ Repo C Integration Architecture

Status: **FROZEN FOR ABCP-RC-P01 RUN-ADMISSION DELIVERY**
Date: 2026-09-19

## 1. Purpose and authority

This document freezes the product-facing integration boundary between Repo C (`autonomous-development-platform`) and Repo B / ABCP (`autonomous-builder-control-plane`) for the first missing capability: new-run admission.

Authority precedence for ABCP-RC-P01 is:

1. explicit user/product decisions;
2. this architecture authority;
3. `ABCP-RC-P01-RUN-ADMISSION.execution-pack.md`;
4. exact implementation base SHA and allowed-path boundary;
5. executable tests/code;
6. evidence and PR narrative.

Implementation or review MAY NOT discover new product/architecture scope. A proposed action is valid only when it is required by this authority or the active execution pack, or when it fixes a reproducible defect in the explicitly changed in-scope behavior. Later-pack redesign, adjacent capability discovery, style/refactor preference and unrelated pre-existing defects are not actionable in P01.

## 2. Frozen repository snapshots

Repo B retained product baseline:
- `main` merge: `821e0476600c9299652b88a016c24ba471bf8a6b`;
- tree: `fb4dc989a5c53812c6e2772b6b00c8d7e405d38b`;
- retained EP-006 commit `f1f4af3c8783d870dcab85a1f86756e6ed4efcee` has the same tree.

Existing admission implementation candidate:
- commit `551eeade43261ac3c24ae393419b880bc0e5827a`;
- it is implementation input for P01, not authority to broaden this contract.
Repo C authority snapshot:
- commit: `b5de18b79765be089317d86786bf64956452ae10`;
- `docs/product/CURRENT_REPO_C_P1_PRODUCT_CONTRACT.md` SHA-256 `ffc01430674db7fd342647f2f0697691f9f58a9510558f3d199cb3da43d10d9e`;
- `docs/architecture/ABCP_ADAPTER_BOUNDARY.md` SHA-256 `1560ce20c6b4066b342978daefde8fcf00752a11e0761781a508f88873320268`;
- `docs/architecture/REPO_C_ABCP_RECONCILIATION.md` SHA-256 `0fd5c11f59a27367bbbb3ed8e19856a22929d5ac221e4f49190b35b78c8ae81b`;
- `ProductAuthorization.java` SHA-256 `a5c09c4ea825b4c44d3bb41779a7fe51e54155dda812359235df0120845646ac`.

Repo C's frozen product fact is `ProductAuthorization(authorizationId, taskId, versionId, manifestDigest, actorId)`: one immutable human approval of one exact current product version and manifest digest. It is not ABCP execution authority.

## 3. Responsibility boundary

### Repo C owns before admission
Repo C owns product intent, the exact product task/version/artifact snapshot, deterministic product readiness, the immutable `ProductAuthorization`, browser/API authentication and authorization, and the user-facing workflow.

Repo C may translate the exact authorized snapshot into the bounded ABCP admission request. It MUST NOT create ABCP execution authority, choose private controller/runtime paths, manufacture a run identifier, recreate ABCP lifecycle truth, or expose provider-private mechanics.

### Repo B / ABCP owns at and after admission
ABCP owns admission validation, private execution-authority materialization, durable run identity, launch ownership, lifecycle/event/evidence truth, governed actions and existing EP-006 projections.

### Execution providers
Repo A / Dev-Agent and Ralphex remain execution mechanisms below ABCP. P01 does not alter provider contracts, provider selection, model selection or execution-loop policy.
## 4. Definition of admission

Admission is the bounded transition from one exact, already product-authorized Repo C snapshot into one durable ABCP run identity.

A successful admission MUST:
- authenticate the machine-to-machine caller under the existing EP-006 service authentication model;
- accept delegated human/operator attribution only under the existing trusted-proxy grant model;
- validate one exact product authorization/task/version/manifest digest and exact repository base;
- resolve only controller-owned private execution configuration;
- durably bind the semantic request to exactly one ABCP run identity before reporting success;
- ensure exact replay does not create a second logical run or duplicate launch;
- launch only through the existing ABCP run path;
- return an opaque `run_id` and canonical `run_url`.

`202 Accepted` means ABCP durably accepted ownership of that run admission. It does not mean implementation, verification, acceptance, publication or merge completed.

## 5. Product-facing request contract

P01 freezes the request at schema version 1 with these product-visible fields:
- `schema_version = 1`;
- `request_id`: stable idempotency identity for one logical admission attempt family;
- `profile_id`: public logical admission profile selected from controller-configured profiles; it does not expose or grant provider/runtime authority;
- `product_authorization_id`;
- `product_task_id`;
- `product_version_id`;
- `product_manifest_sha256`;
- `repository_base_sha`;
- bounded UTF-8 `task_markdown`, deterministically representing the authorized product snapshot for execution handoff;
- required `delegated_actor { subject_id, subject_type }`, with subject type `user` or `operator`.

No caller-supplied filesystem path, private authority manifest, Ralphex argv/config, worktree path, evidence path, PID/process identity, workflow-authority store, merge authority or provider-private configuration is permitted in the public admission request.
## 6. Trust and actor model

The authenticated EP-006 service principal is the caller ABCP trusts for transport authentication and configured service authority.

The delegated actor is attribution asserted by that trusted Repo C proxy. It identifies the human/operator whose exact product authorization caused the submission. It is not independent ABCP end-user authentication and MUST NOT gain authority beyond the service principal's configured ability to assert delegated actors.

The submitted `product_authorization_id`, task/version IDs, manifest digest and delegated actor MUST remain durably attributable to the admitted run through controller-owned admission material/evidence. Repo C remains responsible for proving that its own ProductAuthorization row is current and valid before submission.

## 7. Idempotency and ambiguous outcomes

Idempotency is scoped by authenticated service principal plus `request_id`.

For the same principal and request identity:
- exact semantic replay MUST resolve to the same durable `run_id`;
- conflicting semantic reuse MUST fail closed and MUST NOT create another run;
- transport loss or timeout after submission MUST be reconciled against durable ABCP admission/run state before any new logical request is created;
- a retry of the same logical admission uses the same `request_id` and exact semantic payload.

ABCP MUST NOT convert an ambiguous durable/launch outcome into a second run or an unconditional success. If exact state cannot be proven within the bounded request, return a typed retryable/reconciliation outcome and preserve the existing durable identity for later reconciliation.

## 8. Durable run identity

`run_id` is ABCP-owned and opaque to Repo C. Its public invariant is uniqueness and stable replay binding; its derivation algorithm is not a Repo C contract.

A success response is exactly:
- `run_id`;
- `run_url = /v1/runs/{run_id}`.

After runtime registration, existing EP-006 run detail/events/timeline/evidence/action surfaces are authoritative for execution truth. Repo C may persist the opaque reference and cached projections, but cached product state cannot strengthen or contradict ABCP.
## 9. Controller-owned admission profile

`profile_id` is a bounded public selector for a preconfigured controller-owned admission profile. It is not a provider-selection API.

The controller-owned profile may bind repository identity/path, private manifest template, ignored input/materialization directory, ledger/evidence/cgroup roots and workflow-authority configuration. Those values stay private to ABCP.

The configured repository must be at the submitted exact `repository_base_sha` at admission. A mismatch fails closed; P01 does not fetch, reset, merge or otherwise mutate the repository to make the request fit.

## 10. Compatibility requirements

P01 MUST preserve the retained EP-006 behavior for:
- authentication and authority grants;
- capability discovery;
- run list/detail, events, timeline and evidence;
- evidence download;
- cancel;
- human-decision recording;
- action status.

When admission is not configured, `run_admission=false` and `POST /v1/runs` remains unsupported. Enabling admission MUST NOT imply `retry`, `resume` or `recovery`.

## 11. Explicit non-goals

P01 does not authorize:
- Repo C adapter or database migration;
- Repo C UI wiring;
- human-decision continuation/resume;
- general retry/recovery or failed-run replay;
- provider selection/redesign;
- Repo A/MCP/NUC changes;
- Ralphex behavior/configuration changes;
- publication, integration or merge authority changes;
- Assurance Capsule, post-EP-006 A/B/C, V4 validator/proof-obligation work;
- broad governance redesign;
- live infrastructure/AWS mutation.

Human-decision continuation is a separate later bounded capability. Repo C adapter/correlation and UI workflow are separate later packs after P01 is merged and accepted.
## 12. Review boundary

Review asks only: does the complete P01 base-to-head implementation satisfy this frozen contract and the active execution pack without an in-scope correctness/security/compatibility defect or out-of-scope mutation?

A review suggestion is actionable only when it demonstrates:
- a direct violation of this architecture or the P01 execution pack;
- a reproducible defect in explicitly changed/in-scope behavior;
- an in-scope security, correctness, replay/idempotency, durability or compatibility defect;
- an implementation mutation outside the allowed boundary.

Anything else is `OUT_OF_SCOPE_REVIEW` for P01, including a different preferred architecture, a later capability, general recovery, provider redesign, cleanup, style, or unrelated pre-existing defect.

The authorized review sequence is Review 1, then at most one correction/final Review 2 if Review 1 caused accepted corrections. There is no Review 3.

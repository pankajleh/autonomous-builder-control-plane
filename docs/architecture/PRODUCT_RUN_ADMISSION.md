# Product Run Admission

Status: **BOUNDED EP-006 EXTENSION**

This extension adds a product-safe way for a trusted product service such as Repo C to start an ABCP run without exposing `authority.Manifest`, filesystem paths, Ralphex arguments, process identities, evidence paths, or controller policy through the public API.

## HTTP contract

`POST /v1/runs` is available only when `abcp serve` is started with a valid protected `--admission-profile-file`. Otherwise `run_admission=false` and the route remains unsupported.

The authenticated request is strict JSON with exactly:

- `schema_version=1`
- `request_id`
- `profile_id`
- `product_authorization_id`
- `product_task_id`
- `product_version_id`
- `product_manifest_sha256`
- `repository_base_sha`
- bounded UTF-8 `task_markdown`
- required `delegated_actor` (`user` or `operator`)

A successful first admission or exact replay returns `202 Accepted` with `run_id` and `run_url`.

## Idempotency and execution

Run identity is deterministic from authenticated principal plus `request_id`. A durable admission receipt is persisted before launch. Exact semantic replay returns the same run; conflicting reuse of the same request identity fails closed.

The controller-owned profile maps only `profile_id` to private repository/execution configuration. It must be a protected regular file and binds repository path/identity, manifest template, ignored materialization directory, ledger/evidence/cgroup roots, and a protected workflow-authority configuration path.

Admission deterministically materializes the product plan, V2 context capsule, and private authority manifest, then starts the existing `abcp run` command. A per-run launch lock suppresses duplicate launch; a registered catalog run is never relaunched.

The configured repository base must equal the submitted `repository_base_sha`. Admission material is required to live under a Git-ignored repository-relative directory.

## Workflow authority persistence

Autonomous V2 execution uses the existing `governance.WorkflowAuthorityBackendV1` contract through the narrow `internal/workflowauthoritypg` PostgreSQL implementation. Schema installation is an explicit operator step; ABCP does not create or migrate the database schema at runtime.

The protected workflow-authority file contains only schema version, PostgreSQL connection string, and the fixed authority-domain SHA-256. Database operations use exact revision CAS, canonical-state digest verification, bounded timeouts, and independent read-back reconciliation after ambiguous writes.

## Deliberate exclusions

This extension does not add retry, resume, recovery, publication, merge, provider selection, or a second execution engine. Existing EP-006 projections, evidence, cancel, human-decision, action-status, authentication, and authorization behavior remains unchanged.
